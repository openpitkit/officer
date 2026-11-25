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

package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/scheduler"
	fwstore "go.openpit.dev/officer/framework/store"
)

type scheduledTestTask func(context.Context, scheduler.Run) (json.RawMessage, error)

func (task scheduledTestTask) Run(ctx context.Context, run scheduler.Run) (json.RawMessage, error) {
	return task(ctx, run)
}

func scheduledTestFactory(task scheduledTestTask) scheduler.Factory {
	return func(kind string, config json.RawMessage) (scheduler.Task, error) {
		// This switch belongs to composition, not to scheduler. Only this
		// concrete implementation interprets its opaque JSON arguments.
		switch kind {
		case "counter":
			var args struct {
				Label string `json:"label"`
			}
			if err := json.Unmarshal(config, &args); err != nil {
				return nil, err
			}
			if args.Label == "" {
				return nil, fmt.Errorf("counter requires label: %w", scheduler.ErrInvalid)
			}
			return task, nil
		default:
			return nil, scheduler.ErrUnknownKind
		}
	}
}

var schedulerBase = time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC)

func scheduledSpec(kind string, termination scheduler.Termination) scheduler.JobSpec {
	spec := scheduler.JobSpec{
		Kind: "counter", Config: json.RawMessage(`{"label":"A"}`),
		Schedule:    scheduler.Schedule{Kind: kind, StartAt: schedulerBase},
		Termination: termination,
	}
	if kind == scheduler.Interval {
		spec.Schedule.Every = time.Hour
	}
	return spec
}

func newScheduledTestRuntime(t *testing.T, st fwstore.Store, task scheduledTestTask) (*scheduler.Scheduler, *SchedulerStore) {
	t.Helper()
	jobs, err := NewSchedulerStore(context.Background(), st, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := scheduler.New(jobs, scheduledTestFactory(task))
	if err != nil {
		t.Fatal(err)
	}
	return runtime, jobs
}

func scheduledJob(t *testing.T, runtime *scheduler.Scheduler, id string) scheduler.Job {
	t.Helper()
	job, err := runtime.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func scheduledTick(t *testing.T, runtime *scheduler.Scheduler, at time.Time) scheduler.Execution {
	t.Helper()
	execution, ran, err := runtime.Tick(context.Background(), at)
	if err != nil || !ran {
		t.Fatalf("Tick = %+v, %v, %v", execution, ran, err)
	}
	return execution
}

func TestSchedulerOneDefinitionForHundredRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	var calls int
	runtime, _ := newScheduledTestRuntime(t, st, func(context.Context, scheduler.Run) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"done":true}`), nil
	})
	job, err := runtime.Add(ctx, scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Count, MaxRuns: 100}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 100; run++ {
		execution := scheduledTick(t, runtime, schedulerBase.Add(time.Duration(run)*time.Hour))
		if execution.Outcome != scheduler.Succeeded {
			t.Fatalf("execution = %+v", execution)
		}
		rows, err := runtime.List(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatalf("definitions = %d, %v", len(rows), err)
		}
	}
	job = scheduledJob(t, runtime, job.ID)
	if calls != 100 || job.CompletedRuns != 100 || job.State != scheduler.Completed {
		t.Fatalf("calls=%d job=%+v", calls, job)
	}
	if _, ran, err := runtime.Tick(ctx, schedulerBase.Add(100*time.Hour)); err != nil || ran {
		t.Fatalf("terminated job ran: %v %v", ran, err)
	}
	history, err := runtime.History(ctx, job.ID)
	if err != nil || len(history) != 100 {
		t.Fatalf("history=%d %v", len(history), err)
	}
	if err := runtime.Remove(ctx, job.ID, job.Revision); err != nil {
		t.Fatal(err)
	}
	history, err = runtime.History(ctx, job.ID)
	if err != nil || len(history) != 100 {
		t.Fatalf("removed job lost history: %d %v", len(history), err)
	}
}

func TestSchedulerIndependentJobsAndTypedFactory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	runtime, _ := newScheduledTestRuntime(t, st, func(context.Context, scheduler.Run) (json.RawMessage, error) { return nil, nil })
	for i := 0; i < 3; i++ {
		if _, err := runtime.Add(ctx, scheduledSpec(scheduler.Once, scheduler.Termination{}), scheduler.Ready); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := runtime.List(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatalf("independent definitions=%d %v", len(rows), err)
	}
	bad := scheduledSpec(scheduler.Once, scheduler.Termination{})
	bad.Kind = "does_not_exist"
	if _, err := runtime.Add(ctx, bad, scheduler.Ready); !errors.Is(err, scheduler.ErrUnknownKind) {
		t.Fatalf("unknown kind=%v", err)
	}
	bad.Kind, bad.Config = "counter", json.RawMessage(`{"label":""}`)
	if _, err := runtime.Add(ctx, bad, scheduler.Ready); !errors.Is(err, scheduler.ErrInvalid) {
		t.Fatalf("invalid typed args=%v", err)
	}
	rows, err = runtime.List(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rejected definition mutated store: %d %v", len(rows), err)
	}
}

func TestSchedulerPendingJobsSurviveStoreRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	var calls int
	task := scheduledTestTask(func(context.Context, scheduler.Run) (json.RawMessage, error) { calls++; return nil, nil })
	runtime, _ := newScheduledTestRuntime(t, st, task)
	one, err := runtime.Add(ctx, scheduledSpec(scheduler.Once, scheduler.Termination{}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := runtime.Add(ctx, scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Forever}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.BindExecutionPolicyProviders(policyFixtureClassifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	runtime, jobs := newScheduledTestRuntime(t, reopened, task)
	if err := jobs.Recover(ctx, schedulerBase.Add(10*time.Hour)); err != nil {
		t.Fatal(err)
	}
	scheduledTick(t, runtime, schedulerBase.Add(10*time.Hour+30*time.Minute))
	scheduledTick(t, runtime, schedulerBase.Add(10*time.Hour+30*time.Minute))
	if calls != 2 || scheduledJob(t, runtime, one.ID).State != scheduler.Completed {
		t.Fatalf("calls=%d", calls)
	}
	repeat = scheduledJob(t, runtime, repeat.ID)
	if repeat.CompletedRuns != 1 || !repeat.Next.At.Equal(schedulerBase.Add(11*time.Hour)) {
		t.Fatalf("overdue recurrence lost anchor or expanded backlog: %+v", repeat)
	}
	if _, ran, err := runtime.Tick(ctx, schedulerBase.Add(10*time.Hour+31*time.Minute)); err != nil || ran {
		t.Fatalf("backlog replay: %v %v", ran, err)
	}
}

func TestSchedulerUntilStartBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                 string
		tick                 time.Time
		wantCalls, wantCount int64
	}{
		{"exact end may start", schedulerBase.Add(time.Hour), 1, 1},
		{"after end cannot start", schedulerBase.Add(time.Hour + time.Nanosecond), 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			st, _ := newTestStore(t)
			var calls int64
			runtime, _ := newScheduledTestRuntime(t, st, func(context.Context, scheduler.Run) (json.RawMessage, error) { calls++; return nil, nil })
			spec := scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Until, Until: schedulerBase.Add(time.Hour)})
			job, err := runtime.Add(context.Background(), spec, scheduler.Ready)
			if err != nil {
				t.Fatal(err)
			}
			scheduledTick(t, runtime, test.tick)
			job = scheduledJob(t, runtime, job.ID)
			if calls != test.wantCalls || job.CompletedRuns != test.wantCount || job.State != scheduler.Completed {
				t.Fatalf("calls=%d job=%+v", calls, job)
			}
		})
	}
}

func TestSchedulerClaimsExcludeConcurrentCallbacksAndEdits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var calls atomic.Int32
	task := scheduledTestTask(func(context.Context, scheduler.Run) (json.RawMessage, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil, nil
	})
	first, _ := newScheduledTestRuntime(t, st, task)
	second, _ := newScheduledTestRuntime(t, st, task)
	job, err := first.Add(ctx, scheduledSpec(scheduler.Once, scheduler.Termination{}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := first.Tick(ctx, schedulerBase); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("Tick returned before callback entered: %v", err)
	}
	active := scheduledJob(t, second, job.ID)
	if _, ran, err := second.Tick(ctx, schedulerBase); err != nil || ran {
		t.Fatalf("duplicate claim: %v %v", ran, err)
	}
	if err := second.Pause(ctx, job.ID, active.Revision); !errors.Is(err, scheduler.ErrConflict) {
		t.Fatalf("pause active=%v", err)
	}
	if err := second.Update(ctx, job.ID, active.Revision, job.Spec, schedulerBase); !errors.Is(err, scheduler.ErrConflict) {
		t.Fatalf("edit active=%v", err)
	}
	if err := second.Remove(ctx, job.ID, active.Revision); !errors.Is(err, scheduler.ErrConflict) {
		t.Fatalf("remove active=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("callbacks=%d", calls.Load())
	}
}

func TestSchedulerInterruptedClaimNeverReplaysWithoutDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	var gotRun scheduler.Run
	runtime, jobs := newScheduledTestRuntime(t, st, func(_ context.Context, run scheduler.Run) (json.RawMessage, error) { gotRun = run; return nil, nil })
	job, err := runtime.Add(ctx, scheduledSpec(scheduler.Once, scheduler.Termination{}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := jobs.Claim(ctx, schedulerBase)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := jobs.Recover(ctx, schedulerBase.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	runtime, _ = newScheduledTestRuntime(t, st, func(_ context.Context, run scheduler.Run) (json.RawMessage, error) { gotRun = run; return nil, nil })
	if _, ran, err := runtime.Tick(ctx, schedulerBase.Add(time.Minute)); err != nil || ran {
		t.Fatalf("uncertain callback replay: %v %v", ran, err)
	}
	job = scheduledJob(t, runtime, job.ID)
	if job.State != scheduler.Interrupted || job.Error == "" || job.CompletedRuns != 0 {
		t.Fatalf("recovery=%+v", job)
	}
	if err := runtime.Resume(ctx, job.ID, job.Revision, scheduler.Continue, schedulerBase); !errors.Is(err, scheduler.ErrInvalid) {
		t.Fatalf("implicit replay=%v", err)
	}
	if err := runtime.Resume(ctx, job.ID, job.Revision, scheduler.Retry, schedulerBase); err != nil {
		t.Fatal(err)
	}
	scheduledTick(t, runtime, schedulerBase.Add(2*time.Minute))
	if gotRun.ID != claim.Active.ID || gotRun.Attempt == claim.Active.Attempt {
		t.Fatalf("idempotence key/attempt: old=%+v new=%+v", claim.Active, gotRun)
	}
	history, err := runtime.History(ctx, job.ID)
	if err != nil || len(history) != 2 || history[0].Outcome != scheduler.Interrupted {
		t.Fatalf("history=%+v %v", history, err)
	}
}

func TestSchedulerFailedPartialResultDoesNotCountOrRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	var calls int
	runtime, _ := newScheduledTestRuntime(t, st, func(context.Context, scheduler.Run) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"A":"done","B":"failed"}`), errors.New("B failed")
	})
	job, err := runtime.Add(ctx, scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Count, MaxRuns: 2}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	execution := scheduledTick(t, runtime, schedulerBase)
	if execution.Outcome != scheduler.Failed || execution.Error != "B failed" || len(execution.Result) == 0 {
		t.Fatalf("failure=%+v", execution)
	}
	job = scheduledJob(t, runtime, job.ID)
	if job.State != scheduler.Failed || job.CompletedRuns != 0 {
		t.Fatalf("failed progress=%+v", job)
	}
	if _, ran, err := runtime.Tick(ctx, schedulerBase.Add(time.Hour)); err != nil || ran || calls != 1 {
		t.Fatalf("automatic retry: %v %v calls=%d", ran, err, calls)
	}
}

func TestSchedulerBackupRoundTripAndRunningRestoreGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, realm := newTestStore(t)
	task := scheduledTestTask(func(context.Context, scheduler.Run) (json.RawMessage, error) {
		return json.RawMessage(`{"done":true}`), nil
	})
	runtime, jobs := newScheduledTestRuntime(t, st, task)
	job, err := runtime.Add(ctx, scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Count, MaxRuns: 3}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	scheduledTick(t, runtime, schedulerBase)
	archive, err := realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	target, targetRealm := newTestStore(t)
	if _, err := targetRealm.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: backup.Scope{All: true}}); err != nil {
		t.Fatal(err)
	}
	targetRuntime, targetJobs := newScheduledTestRuntime(t, target, task)
	if err := targetJobs.Recover(ctx, schedulerBase.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	restored := scheduledJob(t, targetRuntime, job.ID)
	if restored.CompletedRuns != 1 || restored.Last == nil || !restored.Next.At.Equal(schedulerBase.Add(time.Hour)) {
		t.Fatalf("restored=%+v", restored)
	}
	scheduledTick(t, targetRuntime, schedulerBase.Add(time.Hour))
	if scheduledJob(t, targetRuntime, job.ID).CompletedRuns != 2 {
		t.Fatal("restored count lost")
	}
	if _, ok, err := jobs.Claim(ctx, schedulerBase.Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	if _, err := realm.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: backup.Scope{All: true}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("restore invalidated active claim: %v", err)
	}
	if err := st.Reset(ctx); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reset invalidated active claim: %v", err)
	}
	activeArchive, err := realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	activeTarget, activeRealm := newTestStore(t)
	if _, err := activeRealm.RestoreBackup(ctx, activeArchive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: backup.Scope{All: true}}); err != nil {
		t.Fatal(err)
	}
	activeRuntime, _ := newScheduledTestRuntime(t, activeTarget, task)
	restored = scheduledJob(t, activeRuntime, job.ID)
	if restored.State != scheduler.Interrupted || restored.Active != nil || restored.Error == "" {
		t.Fatalf("restored active job=%+v", restored)
	}
}

func TestSchedulerDailyGapSkipsAndOverlapDoesNotRepeat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, test := range []struct {
		name, clock, start, tick, second string
		wantCalls                        int
		wantOutcome                      string
	}{
		{"gap", "02:30", "2030-03-10T06:00:00Z", "2030-03-10T07:00:00Z", "2030-03-10T07:01:00Z", 0, scheduler.Skipped},
		{"overlap", "01:30", "2030-11-03T04:00:00Z", "2030-11-03T05:30:00Z", "2030-11-03T06:30:00Z", 1, scheduler.Succeeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			st, _ := newTestStore(t)
			calls := 0
			runtime, _ := newScheduledTestRuntime(t, st, func(context.Context, scheduler.Run) (json.RawMessage, error) { calls++; return nil, nil })
			spec := scheduledSpec(scheduler.Daily, scheduler.Termination{Mode: scheduler.Count, MaxRuns: 2})
			var err error
			spec.Schedule.StartAt, err = time.Parse(time.RFC3339, test.start)
			if err != nil {
				t.Fatal(err)
			}
			spec.Schedule.Time, spec.Schedule.Timezone = test.clock, "America/New_York"
			job, err := runtime.Add(ctx, spec, scheduler.Ready)
			if err != nil {
				t.Fatal(err)
			}
			at, err := time.Parse(time.RFC3339, test.tick)
			if err != nil {
				t.Fatal(err)
			}
			if got := scheduledTick(t, runtime, at); got.Outcome != test.wantOutcome {
				t.Fatalf("outcome=%+v", got)
			}
			second, err := time.Parse(time.RFC3339, test.second)
			if err != nil {
				t.Fatal(err)
			}
			if _, ran, err := runtime.Tick(ctx, second); err != nil || ran || calls != test.wantCalls {
				t.Fatalf("repeated minute: calls=%d ran=%v err=%v", calls, ran, err)
			}
			if got := scheduledJob(t, runtime, job.ID); got.CompletedRuns != int64(test.wantCalls) {
				t.Fatalf("skip counted as completion: %+v", got)
			}
		})
	}
}

type failedSchedulerCompletion struct{ scheduler.Store }

func (failedSchedulerCompletion) Finish(context.Context, scheduler.Job, scheduler.Execution) error {
	return errors.New("completion persistence failed")
}

func TestSchedulerPostEffectPersistenceFailureStaysUncertain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	calls := 0
	task := scheduledTestTask(func(context.Context, scheduler.Run) (json.RawMessage, error) { calls++; return nil, nil })
	_, jobs := newScheduledTestRuntime(t, st, task)
	runtime, err := scheduler.New(failedSchedulerCompletion{jobs}, scheduledTestFactory(task))
	if err != nil {
		t.Fatal(err)
	}
	job, err := runtime.Add(ctx, scheduledSpec(scheduler.Once, scheduler.Termination{}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	if _, ran, err := runtime.Tick(ctx, schedulerBase); err == nil || !ran {
		t.Fatalf("store failure reported success: ran=%v err=%v", ran, err)
	}
	if got := scheduledJob(t, runtime, job.ID); got.State != scheduler.Running || got.CompletedRuns != 0 {
		t.Fatalf("uncertain completion=%+v", got)
	}
	if err := jobs.Recover(ctx, schedulerBase.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	runtime, _ = newScheduledTestRuntime(t, st, task)
	if _, ran, err := runtime.Tick(ctx, schedulerBase.Add(time.Minute)); err != nil || ran || calls != 1 {
		t.Fatalf("effects replayed: calls=%d ran=%v err=%v", calls, ran, err)
	}
}

func TestSchedulerStopJoinsCancelledCallback(t *testing.T) {
	t.Parallel()
	st, _ := newTestStore(t)
	entered, exited := make(chan struct{}), make(chan struct{})
	cancelled, release := make(chan struct{}), make(chan struct{})
	runtime, _ := newScheduledTestRuntime(t, st, func(ctx context.Context, _ scheduler.Run) (json.RawMessage, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		close(exited)
		return nil, ctx.Err()
	})
	spec := scheduledSpec(scheduler.Once, scheduler.Termination{})
	spec.Schedule.StartAt = time.Now().Add(-time.Hour)
	job, err := runtime.Add(t.Context(), spec, scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Stop() })
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	<-entered // Worker started.
	stopped := make(chan error, 1)
	go func() { stopped <- runtime.Stop() }()
	select {
	case <-cancelled:
	case err := <-stopped:
		t.Fatalf("Stop returned before callback cancellation: %v", err)
	}
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before joining the callback: %v", err)
	default:
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Stop returned before callback exited")
	}
	if scheduledJob(t, runtime, job.ID).State != scheduler.Failed {
		t.Fatal("shutdown discarded callback outcome")
	}
}

func TestSchedulerUpdateRetainsCountTermination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	runtime, _ := newScheduledTestRuntime(t, st, func(context.Context, scheduler.Run) (json.RawMessage, error) { return nil, nil })
	job, err := runtime.Add(ctx, scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Count, MaxRuns: 2}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	scheduledTick(t, runtime, schedulerBase)
	job = scheduledJob(t, runtime, job.ID)
	if err := runtime.Pause(ctx, job.ID, job.Revision); err != nil {
		t.Fatal(err)
	}
	job = scheduledJob(t, runtime, job.ID)
	spec := job.Spec
	spec.Schedule.StartAt = schedulerBase.Add(24 * time.Hour)
	spec.Termination.MaxRuns = 1
	if err := runtime.Update(ctx, job.ID, job.Revision, spec, schedulerBase); err != nil {
		t.Fatal(err)
	}
	if got := scheduledJob(t, runtime, job.ID); got.CompletedRuns != 1 || got.State != scheduler.Completed {
		t.Fatalf("editing a future boundary bypassed completed count: %+v", got)
	}
}

func TestSchedulerCompletedDefinitionsSurviveReopenAndRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := newTestStore(t)
	calls := 0
	task := scheduledTestTask(func(context.Context, scheduler.Run) (json.RawMessage, error) { calls++; return nil, nil })
	runtime, _ := newScheduledTestRuntime(t, st, task)
	counted, err := runtime.Add(ctx, scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Count, MaxRuns: 1}), scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	scheduledTick(t, runtime, schedulerBase)
	until := time.Now().UTC().Add(-time.Hour)
	spec := scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Until, Until: until})
	spec.Schedule.StartAt = until.Add(-time.Hour)
	expired, err := runtime.Add(ctx, spec, scheduler.Ready)
	if err != nil {
		t.Fatal(err)
	}
	scheduledTick(t, runtime, time.Now().UTC())
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.BindExecutionPolicyProviders(policyFixtureClassifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	runtime, jobs := newScheduledTestRuntime(t, reopened, task)
	if err := jobs.Recover(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	realm, err := reopened.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	target, targetRealm := newTestStore(t)
	if _, err := targetRealm.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: backup.Scope{All: true}}); err != nil {
		t.Fatal(err)
	}
	targetRuntime, _ := newScheduledTestRuntime(t, target, task)
	for _, reader := range []*scheduler.Scheduler{runtime, targetRuntime} {
		rows, err := reader.List(ctx)
		if err != nil || len(rows) != 2 {
			t.Fatalf("completed definitions lost: %d %v", len(rows), err)
		}
		for _, expected := range []struct {
			id    string
			count int64
		}{{counted.ID, 1}, {expired.ID, 0}} {
			job := scheduledJob(t, reader, expected.id)
			if job.State != scheduler.Completed || !job.Next.At.IsZero() || job.CompletedRuns != expected.count || job.Last == nil {
				t.Fatalf("completed definition changed: %+v", job)
			}
		}
		if _, ran, err := reader.Tick(ctx, schedulerBase.Add(48*time.Hour)); err != nil || ran {
			t.Fatalf("completed definition replay: %v %v", ran, err)
		}
	}
	if calls != 1 {
		t.Fatalf("expired/completed callback ran: %d", calls)
	}
}

// Successful-count persistence warrants generated cases as well as the
// hundred-run regression: varying the failure point must never consume a
// success, expand definitions or turn a failed partial execution into success.
func FuzzSchedulerPersistedCount(f *testing.F) {
	f.Add(uint8(1), uint8(0))
	f.Add(uint8(7), uint8(4))
	f.Fuzz(func(t *testing.T, limit, failure uint8) {
		ctx := context.Background()
		st, _ := newTestStore(t)
		maxRuns := int64(limit%10 + 1)
		failAt, calls := int(failure%uint8(maxRuns))+1, 0
		runtime, _ := newScheduledTestRuntime(t, st, func(context.Context, scheduler.Run) (json.RawMessage, error) {
			calls++
			if calls == failAt {
				return json.RawMessage(`{"partial":true}`), errors.New("partial execution")
			}
			return nil, nil
		})
		job, err := runtime.Add(ctx, scheduledSpec(scheduler.Interval, scheduler.Termination{Mode: scheduler.Count, MaxRuns: maxRuns}), scheduler.Ready)
		if err != nil {
			t.Fatal(err)
		}
		var successes int64
		for attempt := 0; attempt < int(maxRuns)+1; attempt++ {
			at := schedulerBase.Add(time.Duration(attempt) * time.Hour)
			execution := scheduledTick(t, runtime, at)
			if execution.Outcome == scheduler.Succeeded {
				successes++
			}
			job = scheduledJob(t, runtime, job.ID)
			if job.CompletedRuns != successes {
				t.Fatalf("persisted count=%d successes=%d", job.CompletedRuns, successes)
			}
			rows, err := runtime.List(ctx)
			if err != nil || len(rows) != 1 {
				t.Fatalf("definitions=%d %v", len(rows), err)
			}
			if job.State == scheduler.Failed {
				if err := runtime.Resume(ctx, job.ID, job.Revision, scheduler.Skip, at); err != nil {
					t.Fatal(err)
				}
			}
		}
		job = scheduledJob(t, runtime, job.ID)
		if job.State != scheduler.Completed || successes != maxRuns {
			t.Fatalf("terminal count=%d job=%+v", successes, job)
		}
	})
}
