// Copyright The Pit Project Owners. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Please see https://officer.openpit.dev and the OWNERS file for details.

// Package jobs supplies Officer's concrete scheduled tasks and local owner.
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/scheduler"
	"go.openpit.dev/officer/framework/store"
)

// PnlResetKind is the stable persisted type key of the all-account task.
const PnlResetKind = "reset_realized_account_pnl"

// StoreProvider explicitly advertises local scheduler persistence.
type StoreProvider interface {
	SchedulerStore(context.Context, domain.RealmID) (scheduler.Store, error)
}

// Runtime owns one worker and serializes schedule changes with store lifecycle
// operations. Callbacks use the node directly and never acquire mu, so joining
// them before a restore/reset lane gate cannot deadlock.
type Runtime struct {
	mu     sync.Mutex
	node   node.Node
	host   node.PnlResetHost
	worker *scheduler.Scheduler
	since  atomic.Pointer[time.Time]
	closed bool
}

// New constructs the explicitly installed local task owner without starting it.
func New(n node.Node, host node.PnlResetHost, st scheduler.Store) (*Runtime, error) {
	if n == nil || host == nil || st == nil {
		return nil, domain.ErrInvalid
	}
	r := &Runtime{node: n, host: host}
	now := time.Now().UTC()
	r.since.Store(&now)
	worker, err := scheduler.New(st, r.factory)
	if err != nil {
		return nil, err
	}
	r.worker = worker
	return r, nil
}

type allAccountPnlReset struct{ owner *Runtime }

func (r *Runtime) factory(kind string, args json.RawMessage) (scheduler.Task, error) {
	switch kind {
	case PnlResetKind:
		// Only this concrete task interprets its JSON arguments. There is no
		// account filter: this task always owns every account of the realm.
		if raw := bytes.TrimSpace(args); len(raw) == 0 || raw[0] != '{' {
			return nil, fmt.Errorf("P&L reset arguments must be an object: %w", scheduler.ErrInvalid)
		}
		decoder := json.NewDecoder(bytes.NewReader(args))
		decoder.DisallowUnknownFields()
		var config struct{}
		if err := decoder.Decode(&config); err != nil {
			return nil, fmt.Errorf("P&L reset arguments: %v: %w", err, scheduler.ErrInvalid)
		}
		return allAccountPnlReset{owner: r}, nil
	default:
		return nil, fmt.Errorf("task %q: %w", kind, scheduler.ErrUnknownKind)
	}
}

// AccountResetOutcome reports one independent account result; a batch is never
// a transaction over multiple accounts.
type AccountResetOutcome struct {
	Account domain.AccountID `json:"account"`
	Error   string           `json:"error"`
}

// PnlResetResult is a concrete task's opaque JSON result.
type PnlResetResult struct {
	SkippedReason string                `json:"skippedReason"`
	Accounts      []AccountResetOutcome `json:"accounts"`
}

func (task allAccountPnlReset) Run(ctx context.Context, run scheduler.Run) (json.RawMessage, error) {
	r := task.owner
	history, err := r.worker.History(ctx, run.JobID)
	if err != nil {
		return nil, err
	}
	explicitRetry := false
	for _, prior := range history {
		if prior.ID == run.ID && (prior.Outcome == scheduler.Failed || prior.Outcome == scheduler.Interrupted) {
			explicitRetry = true
		}
	}
	// Never catch up an occurrence predating this worker start/resume. A live
	// timer has a one-minute admission window, so a delayed old boundary also
	// cannot erase a later trading day's new P&L.
	if !explicitRetry && (run.Occurrence.At.Before(*r.since.Load()) || !time.Now().UTC().Before(run.Occurrence.At.Add(time.Minute))) {
		result, err := json.Marshal(PnlResetResult{SkippedReason: "missed_boundary", Accounts: []AccountResetOutcome{}})
		if err != nil {
			return nil, err
		}
		return result, scheduler.SkippedOccurrence{Reason: "missed_boundary"}
	}
	accounts, err := r.node.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	result := PnlResetResult{Accounts: make([]AccountResetOutcome, 0, len(accounts))}
	var failures []error
	for _, account := range accounts {
		_, err := r.host.ResetAccountPnl(auth.ContextWithCaller(ctx, auth.SystemCaller()), account.Code, run.ID, auth.SystemCaller())
		outcome := AccountResetOutcome{Account: account.Code}
		if err != nil {
			outcome.Error = err.Error()
			failures = append(failures, fmt.Errorf("account %s: %w", account.Code, err))
		}
		result.Accounts = append(result.Accounts, outcome)
	}
	payload, marshalErr := json.Marshal(result)
	return payload, errors.Join(append(failures, marshalErr)...)
}

func (r *Runtime) definition(ctx context.Context) (*scheduler.Job, error) {
	rows, err := r.worker.List(ctx)
	if err != nil {
		return nil, err
	}
	return r.validateDefinitions(rows)
}

func (r *Runtime) validateDefinitions(rows []scheduler.Job) (*scheduler.Job, error) {
	var found *scheduler.Job
	for _, row := range rows {
		if _, err := r.factory(row.Spec.Kind, row.Spec.Config); err != nil {
			return nil, err
		}
		if row.Spec.Kind != PnlResetKind {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("multiple realm-wide P&L reset definitions: %w", domain.ErrConflict)
		}
		if row.Spec.Schedule.Kind != scheduler.Daily {
			return nil, fmt.Errorf("P&L reset requires a daily schedule: %w", domain.ErrInvalid)
		}
		copy := row
		found = &copy
	}
	return found, nil
}

// ValidateRestore checks the resulting concrete task set while Suspend holds
// lifecycle ownership. Generic archives may carry jobs this host cannot run.
func (r *Runtime) ValidateRestore(ctx context.Context, archive backup.Archive, opts backup.RestoreOptions) error {
	if !opts.Scope.Normalize().Included(backup.SectionGeneralSettings) {
		return nil
	}
	incoming := backup.FilterData(archive.Data, opts.Scope).ScheduledJobs
	var rows []scheduler.Job
	switch opts.Mode {
	case backup.RestoreModeReplaceAll:
		rows = incoming
	case backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing:
		var err error
		rows, err = r.worker.List(ctx)
		if err != nil {
			return err
		}
		byID := make(map[string]int, len(rows))
		for i, job := range rows {
			byID[job.ID] = i
		}
		for _, job := range incoming {
			if i, exists := byID[job.ID]; exists {
				if opts.Mode == backup.RestoreModeOverwrite {
					rows[i] = job
				}
			} else {
				byID[job.ID] = len(rows)
				rows = append(rows, job)
			}
		}
	default:
		return domain.ErrInvalid
	}
	_, err := r.validateDefinitions(rows)
	return scheduleError(err)
}

func (r *Runtime) start() error {
	if r.closed {
		return fmt.Errorf("reset scheduler is closed")
	}
	if _, err := r.definition(context.Background()); err != nil {
		return err
	}
	now := time.Now().UTC()
	r.since.Store(&now)
	return r.worker.Start(context.Background())
}

// Start starts the worker after composition has installed its node ownership.
func (r *Runtime) Start() error { r.mu.Lock(); defer r.mu.Unlock(); return r.start() }

// Suspend stops and joins work, retaining exclusive configuration ownership
// until its returned resume function is called after the node releases gates.
func (r *Runtime) Suspend() (func() error, error) {
	r.mu.Lock()
	if err := r.worker.Stop(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	return func() error { defer r.mu.Unlock(); return r.start() }, nil
}

// Close permanently stops and joins the worker before the node closes its DB.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return r.worker.Stop()
}

func scheduleError(err error) error {
	switch {
	case errors.Is(err, scheduler.ErrInvalid), errors.Is(err, scheduler.ErrUnknownKind):
		return fmt.Errorf("reset schedule: %v: %w", err, domain.ErrInvalid)
	case errors.Is(err, scheduler.ErrConflict):
		return fmt.Errorf("reset schedule changed or is running: %w", domain.ErrConflict)
	case errors.Is(err, scheduler.ErrNotFound):
		return domain.ErrNotFound
	default:
		return err
	}
}

func (r *Runtime) state(ctx context.Context) (backend.AccountPnlResetScheduleState, error) {
	job, err := r.definition(ctx)
	if err != nil {
		return backend.AccountPnlResetScheduleState{}, err
	}
	state := backend.AccountPnlResetScheduleState{Job: job}
	if err := r.worker.Error(); err != nil {
		state.RuntimeError = err.Error()
	}
	if r.closed {
		state.RuntimeError = "reset scheduler is closed"
	}
	return state, nil
}

// Read returns explicit absence or the persisted definition and worker health.
func (r *Runtime) Read(ctx context.Context) (backend.AccountPnlResetScheduleState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state(ctx)
}

func (r *Runtime) audit(ctx context.Context, caller domain.Caller, value string) error {
	err := r.node.AppendAudit(context.WithoutCancel(ctx), store.AuditEntry{
		Action: domain.AuditActionSetSetting,
		Detail: domain.NewAuditDetail(domain.AuditDetailSetSetting, domain.AuditDetailParams{"key": "account_pnl_reset_schedule", "value": value}),
	}, caller)
	if err != nil {
		return r.host.PnlResetSchedulerFailure(err)
	}
	return nil
}

// Save retains the definition's identity and successful count. Failed or
// interrupted work requires an explicit recovery decision before editing.
func (r *Runtime) Save(ctx context.Context, input backend.AccountPnlResetScheduleInput, caller domain.Caller) (backend.AccountPnlResetScheduleState, error) {
	if caller.TightenOnly() {
		return backend.AccountPnlResetScheduleState{}, domain.ErrLoosening
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return backend.AccountPnlResetScheduleState{}, domain.ErrConflict
	}
	previous, err := r.definition(ctx)
	if err != nil {
		return backend.AccountPnlResetScheduleState{}, err
	}
	if (previous == nil && input.Revision != 0) || (previous != nil && input.Revision != previous.Revision) {
		return backend.AccountPnlResetScheduleState{}, domain.ErrConflict
	}
	now := time.Now().UTC()
	start := now
	if previous != nil {
		if previous.State == scheduler.Failed || previous.State == scheduler.Interrupted {
			return backend.AccountPnlResetScheduleState{}, fmt.Errorf("choose retry or skip for the interrupted/failed occurrence first: %w", domain.ErrConflict)
		}
		start = previous.Spec.Schedule.StartAt
	}
	spec := scheduler.JobSpec{Kind: PnlResetKind, Config: json.RawMessage(`{}`),
		Schedule: scheduler.Schedule{Kind: scheduler.Daily, StartAt: start, Time: input.Time, Timezone: input.Timezone}, Termination: input.Termination}
	if err := spec.Validate(); err != nil {
		return backend.AccountPnlResetScheduleState{}, scheduleError(err)
	}
	var job scheduler.Job
	if previous == nil {
		initialState := scheduler.Paused
		if input.Enabled {
			initialState = scheduler.Ready
		}
		job, err = r.worker.Add(ctx, spec, initialState)
	} else {
		err = r.worker.Update(ctx, previous.ID, previous.Revision, spec, now)
		if err == nil {
			ctx = context.WithoutCancel(ctx)
			job, err = r.worker.Get(ctx, previous.ID)
		}
	}
	if err != nil {
		return backend.AccountPnlResetScheduleState{}, scheduleError(err)
	}
	ctx = context.WithoutCancel(ctx)
	if input.Enabled && job.State == scheduler.Paused {
		err = r.worker.Resume(ctx, job.ID, job.Revision, scheduler.Continue, now)
	}
	if err != nil {
		return backend.AccountPnlResetScheduleState{}, scheduleError(err)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return backend.AccountPnlResetScheduleState{}, err
	}
	if err := r.audit(ctx, caller, string(encoded)); err != nil {
		return backend.AccountPnlResetScheduleState{}, err
	}
	return r.state(ctx)
}

// Recover explicitly retries with the same run key or skips an uncertain
// occurrence. A retry's account-chain journal preserves prior successes.
func (r *Runtime) Recover(ctx context.Context, revision int64, action string, caller domain.Caller) (backend.AccountPnlResetScheduleState, error) {
	if caller.TightenOnly() {
		return backend.AccountPnlResetScheduleState{}, domain.ErrLoosening
	}
	if action != scheduler.Retry && action != scheduler.Skip {
		return backend.AccountPnlResetScheduleState{}, domain.ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	job, err := r.definition(ctx)
	if err != nil {
		return backend.AccountPnlResetScheduleState{}, err
	}
	if job == nil {
		return backend.AccountPnlResetScheduleState{}, domain.ErrNotFound
	}
	if err := r.worker.Resume(ctx, job.ID, revision, action, time.Now().UTC()); err != nil {
		return backend.AccountPnlResetScheduleState{}, scheduleError(err)
	}
	ctx = context.WithoutCancel(ctx)
	if err := r.audit(ctx, caller, action); err != nil {
		return backend.AccountPnlResetScheduleState{}, err
	}
	return r.state(ctx)
}
