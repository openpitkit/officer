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

package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Scheduler owns one sequential worker. Store claims also protect against
// concurrent external Tick calls. Hosts stop and join the worker before closing
// or replacing its store; no distributed worker lease is provided.
type Scheduler struct {
	store   Store
	factory Factory
	tickMu  sync.Mutex
	lifeMu  sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	runErr  error
}

// Error reports a worker failure while a host is still serving. Stop also
// returns this error. Hosts must surface it or fail-stop; ready jobs alone do
// not prove that a worker is running.
func (s *Scheduler) Error() error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	return s.runErr
}

// New constructs a scheduler without starting it or recovering uncertain work.
// Start performs recovery. A host driving Tick itself must call Store.Recover
// once, after the old worker has stopped, before admitting ticks.
func New(store Store, factory Factory) (*Scheduler, error) {
	if store == nil || factory == nil {
		return nil, fmt.Errorf("scheduler requires store and task factory: %w", ErrInvalid)
	}
	return &Scheduler{store: store, factory: factory}, nil
}

func (s *Scheduler) validateSpec(spec JobSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	task, err := s.factory(spec.Kind, spec.Config)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("task factory returned no implementation: %w", ErrInvalid)
	}
	return nil
}

// Add validates a definition and atomically persists it as Ready or Paused,
// as explicitly requested. A finished definition is stored as Completed.
// Three independently added definitions have three independent random IDs.
func (s *Scheduler) Add(ctx context.Context, spec JobSpec, initialState string) (Job, error) {
	if initialState != Ready && initialState != Paused {
		return Job{}, fmt.Errorf("initial job state must be ready or paused: %w", ErrInvalid)
	}
	if err := s.validateSpec(spec); err != nil {
		return Job{}, err
	}
	next, more, err := First(spec)
	if err != nil {
		return Job{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Job{}, fmt.Errorf("generate scheduled job ID: %w", err)
	}
	job := Job{ID: hex.EncodeToString(random[:]), Spec: spec, State: initialState, Next: next, Revision: 1}
	if !more {
		job.State, job.Next = Completed, Occurrence{}
	}
	if err := s.store.Create(ctx, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Get returns a definition with its durable status.
func (s *Scheduler) Get(ctx context.Context, id string) (Job, error) {
	return s.store.Get(ctx, id)
}

// List returns definitions, never a list of expanded future occurrences.
func (s *Scheduler) List(ctx context.Context) ([]Job, error) { return s.store.List(ctx) }

// History returns recorded past attempts, including partial failures.
func (s *Scheduler) History(ctx context.Context, id string) ([]Execution, error) {
	return s.store.History(ctx, id)
}

func (s *Scheduler) editable(ctx context.Context, id string, revision int64) (Job, error) {
	job, err := s.store.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.Revision != revision || job.State == Running {
		return Job{}, ErrConflict
	}
	return job, nil
}

// Update edits an inactive definition and leaves it paused for explicit
// activation. Successful count and past outcomes are retained. Recurrences
// start on their next future boundary, without erasing their original anchor.
func (s *Scheduler) Update(ctx context.Context, id string, revision int64, spec JobSpec, now time.Time) error {
	if err := s.validateSpec(spec); err != nil {
		return err
	}
	job, err := s.editable(ctx, id, revision)
	if err != nil {
		return err
	}
	next, more, err := First(spec)
	if err == nil && more && spec.Schedule.Kind != Once && !next.At.After(now) {
		next, more, err = Next(spec, next, now, job.CompletedRuns)
	}
	if err != nil {
		return err
	}
	more = more && withinEnd(spec, next, job.CompletedRuns)
	job.Spec, job.Next, job.State, job.Error = spec, next, Paused, ""
	if !more {
		job.State, job.Next = Completed, Occurrence{}
	}
	return s.store.Replace(ctx, job)
}

// Pause prevents future claims. It refuses an in-flight claim rather than
// cancelling a callback whose effects might already have committed.
func (s *Scheduler) Pause(ctx context.Context, id string, revision int64) error {
	job, err := s.editable(ctx, id, revision)
	if err != nil {
		return err
	}
	if job.State != Ready && job.State != Paused {
		return ErrConflict
	}
	job.State = Paused
	return s.store.Replace(ctx, job)
}

// Resume actions. Retry explicitly accepts potentially repeated external
// effects and retains the occurrence's idempotence key. Skip advances without
// counting success. Continue is only for an ordinary paused definition.
const (
	Continue = "continue"
	Retry    = "retry"
	Skip     = "skip"
)

// Resume requires an explicit decision for failed or interrupted work. It
// never silently replays an uncertain callback after process recovery.
func (s *Scheduler) Resume(ctx context.Context, id string, revision int64, action string, now time.Time) error {
	job, err := s.editable(ctx, id, revision)
	if err != nil {
		return err
	}
	switch {
	case job.State == Paused && action == Continue:
		job.State = Ready
	case (job.State == Failed || job.State == Interrupted) && action == Retry:
		job.State, job.Error = Ready, ""
	case (job.State == Failed || job.State == Interrupted) && action == Skip:
		if err := advance(&job, now); err != nil {
			return err
		}
		job.Error = ""
	default:
		return fmt.Errorf("resume action does not match job state: %w", ErrInvalid)
	}
	return s.store.Replace(ctx, job)
}

// Remove deletes an inactive definition; its execution history remains.
func (s *Scheduler) Remove(ctx context.Context, id string, revision int64) error {
	return s.store.Remove(ctx, id, revision)
}

func advance(job *Job, now time.Time) error {
	next, more, err := Next(job.Spec, job.Next, now, job.CompletedRuns)
	if err != nil {
		return err
	}
	job.State, job.Next = Ready, next
	if !more {
		job.State, job.Next = Completed, Occurrence{}
	}
	return nil
}

// Tick claims and records at most one due occurrence. A task error is returned
// in Execution and persisted as Failed, with no automatic retry. The error
// return reports a scheduler/store failure; a post-effect store error leaves
// the claim Running for explicit interruption recovery. now is the host's tick
// instant, allowing event-loop hosts to drive scheduling without another timer.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) (Execution, bool, error) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Execution{}, false, err
	}
	if actual := time.Now().UTC(); actual.After(now) {
		now = actual
	}
	job, claimed, err := s.store.Claim(ctx, now)
	if err != nil || !claimed {
		return Execution{}, false, err
	}
	execution := Execution{Run: *job.Active, Outcome: Succeeded}
	var taskErr error
	var advanceErr error
	expired := job.Spec.Termination.Mode == Until && now.After(job.Spec.Termination.Until)
	if expired {
		execution.Outcome, execution.Reason = Skipped, "termination_elapsed"
	} else if job.Next.Skip {
		execution.Outcome, execution.Reason = Skipped, "missing_local_minute"
	} else {
		var task Task
		task, taskErr = s.factory(job.Spec.Kind, job.Spec.Config)
		if taskErr == nil {
			if task == nil {
				taskErr = fmt.Errorf("task factory returned no implementation")
			} else if job.Spec.Termination.Mode == Until && time.Now().After(job.Spec.Termination.Until) {
				execution.Outcome, execution.Reason = Skipped, "termination_elapsed"
			} else {
				execution.Result, taskErr = task.Run(ctx, execution.Run)
			}
		}
		if len(execution.Result) > 0 && !json.Valid(execution.Result) {
			taskErr = errors.Join(taskErr, fmt.Errorf("task returned invalid JSON result"))
			execution.Result = nil
		}
	}
	finished := time.Now().UTC()
	if now.After(finished) {
		finished = now.UTC()
	}
	execution.FinishedAt = finished
	job.Active = nil
	var skipped SkippedOccurrence
	if errors.As(taskErr, &skipped) && skipped.Reason != "" {
		execution.Outcome, execution.Reason, taskErr = Skipped, skipped.Reason, nil
	}
	if taskErr != nil {
		execution.Outcome, execution.Error = Failed, taskErr.Error()
		job.State, job.Error = Failed, taskErr.Error()
	} else {
		if execution.Outcome == Succeeded {
			job.CompletedRuns++
		}
		if err := advance(&job, finished); err != nil {
			job.State, job.Error = Failed, "calculate next occurrence: "+err.Error()
			advanceErr = fmt.Errorf("calculate next occurrence: %w", err)
		}
	}
	job.Last = &execution
	// Once a callback has run, cancellation must not discard its durable
	// outcome. A failed write remains an uncertain claim, never a success.
	if err := s.store.Finish(context.WithoutCancel(ctx), job, execution); err != nil {
		return execution, true, fmt.Errorf("persist execution completion: %w", err)
	}
	if advanceErr != nil {
		return execution, true, advanceErr
	}
	return execution, true, nil
}

// Start recovers interrupted claims, then starts one worker. Only one worker
// may own this store; stop and join the previous owner before calling Start.
func (s *Scheduler) Start(ctx context.Context) error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.cancel != nil {
		return ErrConflict
	}
	s.tickMu.Lock()
	err := s.store.Recover(ctx, time.Now().UTC())
	s.tickMu.Unlock()
	if err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.cancel, s.done, s.runErr = cancel, make(chan struct{}), nil
	go s.work(workerCtx, s.done)
	return nil
}

func (s *Scheduler) work(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			execution, ran, err := s.Tick(ctx, now)
			if err != nil {
				if !errors.Is(err, context.Canceled) || ctx.Err() == nil {
					s.lifeMu.Lock()
					s.runErr = err
					s.lifeMu.Unlock()
					slog.Error("scheduler stopped", "error", err)
				}
				return
			}
			if ran && execution.Error != "" {
				slog.Error("scheduled task failed", "job", execution.JobID, "run", execution.ID, "error", execution.Error)
			}
		}
	}
}

// Stop cancels and joins the worker before returning any fatal store error.
// It does not close the shared store. A callback must cooperate with context
// cancellation; shutdown waits for its outcome to be persisted.
func (s *Scheduler) Stop() error {
	s.lifeMu.Lock()
	if s.cancel == nil {
		s.lifeMu.Unlock()
		return nil
	}
	s.cancel()
	done := s.done
	s.lifeMu.Unlock()
	<-done
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.done == done {
		s.cancel, s.done = nil, nil
	}
	return s.runErr
}
