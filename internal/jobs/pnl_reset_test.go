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

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/scheduler"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/store/sqlite"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type partialResetHost struct {
	node.PnlResetHost
	fail bool
}

// Cancellation happens after the real SQLite creation commit and before any
// subsequent scheduler/store operation can use the request context.
type cancelAfterScheduledCreate struct {
	scheduler.Store
	cancel       context.CancelFunc
	createdState string
}

func (s *cancelAfterScheduledCreate) Create(ctx context.Context, job scheduler.Job) error {
	if err := s.Store.Create(ctx, job); err != nil {
		return err
	}
	s.createdState = job.State
	s.cancel()
	return nil
}

func TestDisabledScheduleCreationCannotCommitReadyBeforeCancellation(t *testing.T) {
	_, n, _, host, jobs := resetTaskFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupt := &cancelAfterScheduledCreate{Store: jobs, cancel: cancel}
	r, err := New(n, host, interrupt)
	if err != nil {
		t.Fatal(err)
	}
	spec := scheduler.JobSpec{Kind: PnlResetKind, Config: []byte("{}"), Schedule: scheduler.Schedule{Kind: scheduler.Daily, StartAt: time.Now().UTC(), Time: "09:00", Timezone: "UTC"}, Termination: scheduler.Termination{Mode: scheduler.Forever}}
	for _, state := range []string{"", scheduler.Running, scheduler.Failed, scheduler.Completed} {
		if _, err := r.worker.Add(ctx, spec, state); !errors.Is(err, scheduler.ErrInvalid) {
			t.Fatalf("invalid initial state %q accepted: %v", state, err)
		}
	}
	state, err := r.Save(ctx, backend.AccountPnlResetScheduleInput{Enabled: false, Time: "09:00", Timezone: "UTC", Termination: spec.Termination}, auth.SystemCaller())
	if err != nil || state.Job == nil || state.Job.State != scheduler.Paused {
		t.Fatalf("Save after committed creation cancellation=%+v %v", state, err)
	}
	rows, err := jobs.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || interrupt.createdState != scheduler.Paused || rows[0].State != scheduler.Paused || rows[0].CompletedRuns != 0 {
		t.Fatalf("disabled creation committed state=%q rows=%+v", interrupt.createdState, rows)
	}
	if rows[0].Next.At.IsZero() {
		t.Fatal("test requires a scheduled occurrence")
	}
	if _, claimed, err := jobs.Claim(context.Background(), rows[0].Next.At.Add(time.Second)); err != nil || claimed {
		t.Fatalf("disabled job claimed after its due instant: claimed=%v error=%v", claimed, err)
	}
	audit, err := n.ListAudit(context.Background(), 10)
	if err != nil || len(audit) == 0 || audit[0].Action != domain.AuditActionSetSetting || audit[0].Detail.Params["key"] != "account_pnl_reset_schedule" {
		t.Fatalf("creation audit after cancellation=%+v %v", audit, err)
	}
}

type cancelAfterScheduledReplace struct {
	scheduler.Store
	cancel context.CancelFunc
}

func (s *cancelAfterScheduledReplace) Replace(ctx context.Context, job scheduler.Job) error {
	if err := s.Store.Replace(ctx, job); err != nil {
		return err
	}
	s.cancel()
	return nil
}

func TestEnabledScheduleEditCompletesAfterCommittedCancellation(t *testing.T) {
	r, n, _, host, jobs := resetTaskFixture(t)
	ctx := context.Background()
	input := backend.AccountPnlResetScheduleInput{Enabled: true, Time: "09:00", Timezone: "UTC", Termination: scheduler.Termination{Mode: scheduler.Forever}}
	initial, err := r.Save(ctx, input, auth.SystemCaller())
	if err != nil {
		t.Fatal(err)
	}
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	r, err = New(n, host, &cancelAfterScheduledReplace{Store: jobs, cancel: cancel})
	if err != nil {
		t.Fatal(err)
	}
	input.Time, input.Revision = "10:00", initial.Job.Revision
	state, err := r.Save(request, input, auth.SystemCaller())
	if err != nil || state.Job == nil || state.Job.State != scheduler.Ready || state.Job.Spec.Schedule.Time != "10:00" {
		t.Fatalf("enabled edit did not finish after commit: %+v %v", state, err)
	}
	audit, err := n.ListAudit(ctx, 10)
	if err != nil || len(audit) == 0 || audit[0].Action != domain.AuditActionSetSetting || !strings.Contains(audit[0].Detail.Params["value"], "10:00") {
		t.Fatalf("enabled edit audit after cancellation=%+v %v", audit, err)
	}
}

func TestScheduleRecoveryCompletesReadbackAfterCommittedCancellation(t *testing.T) {
	for _, action := range []string{scheduler.Retry, scheduler.Skip} {
		t.Run(action, func(t *testing.T) {
			r, n, _, host, jobs := resetTaskFixture(t)
			ctx := context.Background()
			if _, err := n.CreateAccount(ctx, domain.Account{Code: "b", Currency: "USD"}, auth.SystemCaller()); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC)
			job, err := r.worker.Add(ctx, scheduler.JobSpec{Kind: PnlResetKind, Config: []byte("{}"), Schedule: scheduler.Schedule{Kind: scheduler.Daily, StartAt: at, Time: "09:00", Timezone: "UTC"}, Termination: scheduler.Termination{Mode: scheduler.Forever}}, scheduler.Ready)
			if err != nil {
				t.Fatal(err)
			}
			host.fail = true
			if _, _, err := r.worker.Tick(ctx, at); err != nil {
				t.Fatal(err)
			}
			failed, err := jobs.Get(ctx, job.ID)
			if err != nil || failed.State != scheduler.Failed {
				t.Fatalf("failed fixture=%+v %v", failed, err)
			}
			request, cancel := context.WithCancel(ctx)
			defer cancel()
			r, err = New(n, host, &cancelAfterScheduledReplace{Store: jobs, cancel: cancel})
			if err != nil {
				t.Fatal(err)
			}
			state, err := r.Recover(request, failed.Revision, action, auth.SystemCaller())
			if err != nil || state.Job == nil || state.Job.State != scheduler.Ready || state.Job.CompletedRuns != 0 {
				t.Fatalf("recovery readback after commit: %+v %v", state, err)
			}
			audit, err := n.ListAudit(ctx, 10)
			if err != nil || len(audit) == 0 || audit[0].Action != domain.AuditActionSetSetting || audit[0].Detail.Params["value"] != action {
				t.Fatalf("recovery audit after cancellation=%+v %v", audit, err)
			}
		})
	}
}

func TestRestoreRejectsInvalidResultingResetDefinitionsBeforeCommit(t *testing.T) {
	for _, mode := range []backup.RestoreMode{backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing, backup.RestoreModeReplaceAll} {
		for _, malformed := range []string{"second", "once", "args", "array", "unknown_kind"} {
			if mode == backup.RestoreModeReplaceAll && malformed == "second" {
				continue
			}
			t.Run(string(mode)+"/"+malformed, func(t *testing.T) {
				r, n, st, host, _ := resetTaskFixture(t)
				ctx := context.Background()
				if err := host.InstallPnlResetScheduler(r); err != nil {
					t.Fatal(err)
				}
				state, err := r.Save(ctx, backend.AccountPnlResetScheduleInput{Enabled: false, Time: "09:00", Timezone: "UTC", Termination: scheduler.Termination{Mode: scheduler.Forever}}, auth.SystemCaller())
				if err != nil {
					t.Fatal(err)
				}
				if malformed != "second" {
					if err := r.worker.Remove(ctx, state.Job.ID, state.Job.Revision); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, auth.SystemCaller()); err != nil {
					t.Fatal(err)
				}
				full := backup.Scope{All: true}
				before, err := st.ExportBackup(ctx, full)
				if err != nil {
					t.Fatal(err)
				}
				incoming := state.Job.Clone()
				// Different IDs participate in the resulting merge, even though
				// each incoming list contains just one otherwise valid job.
				incoming.ID = "foreign"
				switch malformed {
				case "once":
					incoming.Spec.Schedule = scheduler.Schedule{Kind: scheduler.Once, StartAt: incoming.Next.At}
					incoming.Spec.Termination = scheduler.Termination{}
				case "args":
					incoming.Spec.Config = []byte(`{"account":"desk"}`)
				case "array":
					incoming.Spec.Config = []byte(`[]`)
				case "unknown_kind":
					incoming.Spec.Kind = "foreign_task"
				}
				archive := backup.NewArchive(time.Now(), "test", before.Manifest.Realm, full, backup.Data{ScheduledJobs: []scheduler.Job{incoming}}, backup.CredentialFormPlaintext)
				_, _, err = n.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: mode, Scope: full}, auth.SystemCaller())
				if !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrConflict) {
					t.Errorf("invalid concrete restore accepted: %v", err)
				}
				after, err := st.ExportBackup(ctx, full)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before.Data, after.Data) {
					t.Error("refused restore changed persisted state")
				}
				if err := r.worker.Stop(); err != nil {
					t.Fatal(err)
				}
				if err := r.Start(); err != nil {
					t.Fatalf("restart after refused restore: %v", err)
				}
				got, err := r.Read(ctx)
				validState := (malformed == "second" && got.Job != nil && got.Job.ID == state.Job.ID) || (malformed != "second" && got.Job == nil)
				if err != nil || got.RuntimeError != "" || !validState {
					t.Fatalf("unusable runtime after refusal: %+v %v", got, err)
				}
			})
		}
	}
}

func (h *partialResetHost) ResetAccountPnl(ctx context.Context, code domain.AccountID, run string, caller domain.Caller) (domain.Account, error) {
	if h.fail && code == "b" {
		return domain.Account{}, errors.New("account b unavailable")
	}
	return h.PnlResetHost.ResetAccountPnl(ctx, code, run, caller)
}
func resetTaskFixture(t *testing.T) (*Runtime, node.Node, store.RealmStore, *partialResetHost, scheduler.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(filepath.Join(t.TempDir(), "jobs.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	n, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, db, engine.NewOpenPitEngineBuildFunc(), func(err error) { t.Errorf("unexpected fatal: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	st, err := db.ForRealm(ctx, n.Realm())
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := sqlite.NewSchedulerStore(ctx, db, n.Realm())
	if err != nil {
		t.Fatal(err)
	}
	host := &partialResetHost{PnlResetHost: n.(node.PnlResetHost)}
	r, err := New(n, host, jobs)
	if err != nil {
		t.Fatal(err)
	}
	return r, n, st, host, jobs
}
func taskFill(t *testing.T, n node.Node, code domain.AccountID, price string) {
	t.Helper()
	_, result, err := n.SubmitImmediate(context.Background(), domain.Order{Account: code, BaseAsset: "BTC", QuoteAsset: "USD", Side: domain.OrderSideSell, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: price, DropCopy: true}, domain.MissingAccountReject, auth.SystemCaller())
	if err != nil || !result.Accepted {
		t.Fatalf("fill %+v %v", result, err)
	}
}
func TestDailyResetPartialRetryRetainsSuccessfulAccountAfterRename(t *testing.T) {
	r, n, st, host, _ := resetTaskFixture(t)
	ctx := context.Background()
	caller := auth.SystemCaller()
	if _, err := n.CreateAsset(ctx, domain.Asset{Code: "BTC"}, caller); err != nil {
		t.Fatal(err)
	}
	for _, code := range []domain.AccountID{"a", "b"} {
		if _, err := n.CreateAccount(ctx, domain.Account{Code: code, Currency: "USD"}, caller); err != nil {
			t.Fatal(err)
		}
		if _, err := n.ApplyAdjustment(ctx, code, "", domain.AdjustmentRequest{Type: domain.AdjustmentTypeCorrection, Asset: "BTC", Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "2"}, AverageEntryPrice: "100"}, domain.MissingAccountReject, caller); err != nil {
			t.Fatal(err)
		}
		taskFill(t, n, code, "88")
	}
	at := time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC)
	job, err := r.worker.Add(ctx, scheduler.JobSpec{Kind: PnlResetKind, Config: []byte("{}"), Schedule: scheduler.Schedule{Kind: scheduler.Daily, StartAt: at, Time: "09:00", Timezone: "UTC"}, Termination: scheduler.Termination{Mode: scheduler.Count, MaxRuns: 1}}, scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	host.fail = true
	if _, _, err := r.worker.Tick(ctx, at); err != nil {
		t.Fatal(err)
	}
	failed, err := r.worker.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != scheduler.Failed || failed.CompletedRuns != 0 || failed.Last == nil || failed.Last.Error == "" {
		t.Fatalf("partial outcome=%+v", failed)
	}
	a, _, _ := st.GetAccount(ctx, "a")
	b, _, _ := st.GetAccount(ctx, "b")
	if a.Pnl != "0" || b.Pnl != "-12" {
		t.Fatalf("independent accounts a=%+v b=%+v", a, b)
	}
	taskFill(t, n, "a", "102")
	if _, err := n.UpdateAccount(ctx, "a", domain.Account{Code: "renamed"}, caller); err != nil {
		t.Fatal(err)
	}
	host.fail = false
	// A full restore preserves both failed job progress and account-owned
	// receipts, including the current code after rename.
	scope := backup.Scope{Sections: backup.AllSections}
	archive, err := n.ExportBackup(ctx, scope, caller)
	if err != nil {
		t.Fatal(err)
	}
	settings := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	if _, err := n.ExportBackup(ctx, settings, caller); !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "accounts_groups") {
		t.Fatalf("settings-only reset progress=%v", err)
	}
	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: settings}, caller); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("settings-only restore=%v", err)
	}
	restored, target, targetStore, _, _ := resetTaskFixture(t)
	if _, _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: scope}, caller); err != nil {
		t.Fatal(err)
	}
	r, n, st = restored, target, targetStore
	failed, err = r.worker.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.worker.Resume(ctx, job.ID, failed.Revision, scheduler.Retry, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.worker.Tick(ctx, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	done, err := r.worker.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ = st.GetAccount(ctx, "renamed")
	b, _, _ = st.GetAccount(ctx, "b")
	if done.State != scheduler.Completed || done.CompletedRuns != 1 || a.Pnl != "2" || b.Pnl != "0" {
		t.Fatalf("retry job=%+v a=%+v b=%+v", done, a, b)
	}
	taskFill(t, n, "b", "103")
	if _, _, err := r.worker.Tick(ctx, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	b, _, _ = st.GetAccount(ctx, "b")
	if b.Pnl != "3" {
		t.Fatalf("duplicate tick erased new fill: %+v", b)
	}
	audit, err := n.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	resets := 0
	for _, entry := range audit {
		if entry.Action == domain.AuditActionResetAccountPnl {
			resets++
			if entry.Source != domain.SourceSystem {
				t.Fatal("scheduled audit lost system source")
			}
		}
	}
	if resets != 2 {
		t.Fatalf("successful reset audit rows=%d", resets)
	}
	edited, err := r.Save(ctx, backend.AccountPnlResetScheduleInput{Enabled: false, Time: "10:00", Timezone: "UTC", Termination: scheduler.Termination{Mode: scheduler.Count, MaxRuns: 100}, Revision: done.Revision}, caller)
	if err != nil || edited.Job == nil || edited.Job.ID != job.ID || edited.Job.CompletedRuns != 1 {
		t.Fatalf("edit lost identity/progress: %+v %v", edited, err)
	}
}

func TestResetRuntimeReloadsAcrossRestoreAndDatabaseReset(t *testing.T) {
	r, n, _, host, _ := resetTaskFixture(t)
	ctx := context.Background()
	caller := auth.SystemCaller()
	if err := host.InstallPnlResetScheduler(r); err != nil {
		t.Fatal(err)
	}
	input := backend.AccountPnlResetScheduleInput{Enabled: false, Time: "09:00", Timezone: "UTC", Termination: scheduler.Termination{Mode: scheduler.Forever}}
	initial, err := r.Save(ctx, input, caller)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	scope := backup.Scope{Sections: backup.AllSections}
	archive, err := n.ExportBackup(ctx, scope, caller)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: scope}, caller); err != nil {
		t.Fatal(err)
	}
	state, err := r.Read(ctx)
	if err != nil || state.RuntimeError != "" || state.Job == nil || state.Job.ID != initial.Job.ID {
		t.Fatalf("restored runtime=%+v %v", state, err)
	}
	if _, err := n.ResetDatabase(ctx, caller); err != nil {
		t.Fatal(err)
	}
	state, err = r.Read(ctx)
	if err != nil || state.RuntimeError != "" || state.Job != nil {
		t.Fatalf("reset runtime=%+v %v", state, err)
	}
}

func TestDailyResetSkipsMissedBoundaryWithoutSuccess(t *testing.T) {
	r, n, st, _, _ := resetTaskFixture(t)
	ctx := context.Background()
	caller := auth.SystemCaller()
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, caller); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Minute)
	job, err := r.worker.Add(ctx, scheduler.JobSpec{Kind: PnlResetKind, Config: []byte("{}"), Schedule: scheduler.Schedule{Kind: scheduler.Daily, StartAt: at, Time: at.Format("15:04"), Timezone: "UTC"}, Termination: scheduler.Termination{Mode: scheduler.Forever}}, scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.worker.Tick(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, err := r.worker.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := st.GetAccount(ctx, "desk")
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletedRuns != 0 || got.Last == nil || got.Last.Outcome != scheduler.Skipped || got.Last.Reason != "missed_boundary" || account.Pnl != "" {
		t.Fatalf("missed boundary job=%+v account=%+v", got, account)
	}
}

type blockedResetHistory struct {
	scheduler.Store
	entered chan struct{}
	release chan struct{}
	prior   []scheduler.Execution
}

func (s *blockedResetHistory) History(ctx context.Context, _ string) ([]scheduler.Execution, error) {
	close(s.entered)
	select {
	case <-s.release:
		return s.prior, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestDailyResetSamplesCurrentTimeAfterBlockedPreparation(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_attempt", true: "explicit_retry"}[retry], func(t *testing.T) {
			_, n, st, host, jobs := resetTaskFixture(t)
			ctx := context.Background()
			if _, err := n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, auth.SystemCaller()); err != nil {
				t.Fatal(err)
			}
			blocked := &blockedResetHistory{Store: jobs, entered: make(chan struct{}), release: make(chan struct{})}
			if retry {
				blocked.prior = []scheduler.Execution{{Run: scheduler.Run{ID: "run"}, Outcome: scheduler.Failed}}
			}
			r, err := New(n, host, blocked)
			if err != nil {
				t.Fatal(err)
			}
			// Reproduce the captured, in-window timestamp returned by delayed
			// preparation. Current wall time is already beyond its window;
			// lifecycle admission is deliberately satisfied using existing state.
			at := time.Now().UTC().Add(-2 * time.Minute)
			r.since.Store(&at)
			task, err := r.factory(PnlResetKind, []byte("{}"))
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, err := task.Run(ctx, scheduler.Run{ID: "run", JobID: "job", Attempt: 1, Occurrence: scheduler.Occurrence{At: at}, StartedAt: at.Add(time.Second)})
				result <- err
			}()
			<-blocked.entered
			close(blocked.release)
			err = <-result
			var skipped scheduler.SkippedOccurrence
			account, _, readErr := st.GetAccount(ctx, "desk")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if retry {
				if err != nil || account.Pnl != "0" {
					t.Fatalf("explicit retry lost exemption: %+v %v", account, err)
				}
			} else if !errors.As(err, &skipped) || skipped.Reason != "missed_boundary" || account.Pnl != "" {
				t.Fatalf("stale preparation admitted a first reset: account=%+v err=%v", account, err)
			}
		})
	}
}

func TestInsertMissingValidatesResultingResetDefinition(t *testing.T) {
	r, n, st, host, _ := resetTaskFixture(t)
	ctx := context.Background()
	if err := host.InstallPnlResetScheduler(r); err != nil {
		t.Fatal(err)
	}
	state, err := r.Save(ctx, backend.AccountPnlResetScheduleInput{Enabled: false, Time: "09:00", Timezone: "UTC", Termination: scheduler.Termination{Mode: scheduler.Forever}}, auth.SystemCaller())
	if err != nil {
		t.Fatal(err)
	}
	incoming := state.Job.Clone()
	incoming.Spec.Config = json.RawMessage(`{"account":"foreign"}`)
	settings := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	archive, err := st.ExportBackup(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	archive.Data.ScheduledJobs = []scheduler.Job{incoming}
	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeInsertMissing, Scope: settings}, auth.SystemCaller()); err != nil {
		t.Fatalf("skipped incoming definition incorrectly validated instead of retained receiver: %v", err)
	}
	got, err := r.Read(ctx)
	if err != nil || got.Job == nil || string(got.Job.Spec.Config) != "{}" {
		t.Fatalf("insert-missing changed existing concrete definition: %+v %v", got, err)
	}
}

func TestDailyResetOverwriteRestoresEarlierFailureAndAccountEvidence(t *testing.T) {
	r, n, st, host, _ := resetTaskFixture(t)
	ctx, caller := context.Background(), auth.SystemCaller()
	if _, err := n.CreateAsset(ctx, domain.Asset{Code: "BTC"}, caller); err != nil {
		t.Fatal(err)
	}
	for _, code := range []domain.AccountID{"a", "b"} {
		if _, err := n.CreateAccount(ctx, domain.Account{Code: code, Currency: "USD"}, caller); err != nil {
			t.Fatal(err)
		}
		if _, err := n.ApplyAdjustment(ctx, code, "", domain.AdjustmentRequest{Type: domain.AdjustmentTypeCorrection, Asset: "BTC", Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "3"}, AverageEntryPrice: "100"}, domain.MissingAccountReject, caller); err != nil {
			t.Fatal(err)
		}
		taskFill(t, n, code, "88")
	}
	at := time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC)
	job, err := r.worker.Add(ctx, scheduler.JobSpec{Kind: PnlResetKind, Config: []byte("{}"), Schedule: scheduler.Schedule{Kind: scheduler.Daily, StartAt: at, Time: "09:00", Timezone: "UTC"}, Termination: scheduler.Termination{Mode: scheduler.Count, MaxRuns: 1}}, scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	host.fail = true
	if _, _, err := r.worker.Tick(ctx, at); err != nil {
		t.Fatal(err)
	}
	archive, err := n.ExportBackup(ctx, backup.Scope{All: true}, caller)
	if err != nil {
		t.Fatal(err)
	}
	retry := func() {
		t.Helper()
		failed, err := r.worker.Get(ctx, job.ID)
		if err != nil || failed.State != scheduler.Failed {
			t.Fatalf("retry fixture=%+v %v", failed, err)
		}
		if err := r.worker.Resume(ctx, job.ID, failed.Revision, scheduler.Retry, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.worker.Tick(ctx, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	host.fail = false
	retry()
	taskFill(t, n, "b", "103")
	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeOverwrite, Scope: backup.Scope{All: true}}, caller); err != nil {
		t.Fatal(err)
	}
	b, _, err := st.GetAccount(ctx, "b")
	if err != nil || b.Pnl != "-12" {
		t.Fatalf("earlier snapshot not restored: %+v %v", b, err)
	}
	retry()
	b, _, err = st.GetAccount(ctx, "b")
	done, jobErr := r.worker.Get(ctx, job.ID)
	if err != nil || jobErr != nil || b.Pnl != "0" || done.State != scheduler.Completed || done.CompletedRuns != 1 {
		t.Fatalf("later receipt skipped restored failed account: account=%+v job=%+v errors=%v %v", b, done, err, jobErr)
	}
}

func TestProgressFreeSettingsRestoreWithLiveResetReceipts(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner_absent", true: "owner_installed"}[installed], func(t *testing.T) {
			r, n, st, host, _ := resetTaskFixture(t)
			ctx, caller := context.Background(), auth.SystemCaller()
			if installed {
				if err := host.InstallPnlResetScheduler(r); err != nil {
					t.Fatal(err)
				}
			}
			state, err := r.Save(ctx, backend.AccountPnlResetScheduleInput{Enabled: false, Time: "09:00", Timezone: "UTC", Termination: scheduler.Termination{Mode: scheduler.Forever}}, caller)
			if err != nil {
				t.Fatal(err)
			}
			account, err := n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, caller)
			if err != nil {
				t.Fatal(err)
			}
			runID := state.Job.ID + ":2030-01-01"
			if _, err := host.ResetAccountPnl(ctx, account.Code, runID, caller); err != nil {
				t.Fatal(err)
			}
			full, err := n.ExportBackup(ctx, backup.Scope{All: true}, caller)
			if err != nil {
				t.Fatal(err)
			}
			settings := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
			if _, err := n.ExportBackup(ctx, settings, caller); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("operator export accepted omitted receipts: %v", err)
			}
			if _, _, err := n.RestoreBackup(ctx, full, backup.RestoreOptions{Mode: backup.RestoreModeOverwrite, Scope: settings}, caller); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("incoming progress accepted omitted receipts: %v", err)
			}
			_, _, source, _, _ := resetTaskFixture(t)
			archive, err := source.ExportBackup(ctx, settings)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeOverwrite, Scope: settings}, caller); err != nil {
				t.Fatalf("progress-free settings restore refused due to live receipts: %v", err)
			}
			recorded, err := st.(store.AccountPnlResetStore).AccountPnlResetRecorded(ctx, account.EngineAccountID, runID)
			if err != nil || !recorded {
				t.Fatalf("settings restore lost live account evidence: %v %v", recorded, err)
			}
			got, err := r.Read(ctx)
			if err != nil || got.Job == nil || got.Job.ID != state.Job.ID || got.RuntimeError != "" {
				t.Fatalf("settings restore stopped runtime: %+v %v", got, err)
			}
		})
	}
}
func TestResetScheduleRefusesMalformedInputAndAgents(t *testing.T) {
	r, _, _, _, _ := resetTaskFixture(t)
	ctx := context.Background()
	valid := backend.AccountPnlResetScheduleInput{Enabled: true, Time: "09:00", Timezone: "UTC", Termination: scheduler.Termination{Mode: scheduler.Forever}}
	for _, input := range []backend.AccountPnlResetScheduleInput{
		{Enabled: true, Time: "9:00", Timezone: "UTC", Termination: valid.Termination},
		{Enabled: true, Time: "09:00", Timezone: "Unknown/Zone", Termination: valid.Termination},
		{Enabled: true, Time: "09:00", Termination: valid.Termination},
	} {
		if _, err := r.Save(ctx, input, auth.SystemCaller()); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid input accepted: %+v %v", input, err)
		}
	}
	agent := domain.Caller{Source: domain.SourceMCP, Principal: "agent"}
	if _, err := r.Save(ctx, valid, agent); !errors.Is(err, domain.ErrLoosening) {
		t.Fatalf("agent save=%v", err)
	}
	if _, err := r.Recover(ctx, 1, scheduler.Retry, agent); !errors.Is(err, domain.ErrLoosening) {
		t.Fatalf("agent recover=%v", err)
	}
	state, err := r.Read(ctx)
	if err != nil || state.Job != nil {
		t.Fatalf("refused mutation created a job: %+v %v", state, err)
	}
}
