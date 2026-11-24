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
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/scheduler"
)

func TestTestExecutionUsesOnce(t *testing.T) {
	_, n, _, host, st := resetTaskFixture(t)
	r, err := New(n, host, st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	deadline := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	calls := 0
	stop, err := r.Schedule(ctx, deadline, func(at time.Time) {
		if at.Before(deadline) {
			t.Error("execution ran before its deadline")
		}
		calls++
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("jobs = %+v %v", rows, err)
	}
	job := rows[0]
	if job.Spec.Kind != testExecutionKind || job.Spec.Schedule.Kind != scheduler.Once || !job.Spec.Schedule.StartAt.Equal(deadline) {
		t.Fatalf("execution is not a standard Once job: %+v", job)
	}
	var config testExecutionConfig
	if err := json.Unmarshal(job.Spec.Config, &config); err != nil || config.ExecutionID.IsZero() {
		t.Fatalf("execution config = %+v %v", config, err)
	}
	if _, ran, err := r.worker.Tick(ctx, deadline.Add(-time.Nanosecond)); err != nil || ran || calls != 0 {
		t.Fatalf("early tick: ran=%v calls=%d err=%v", ran, calls, err)
	}
	execution, ran, err := r.worker.Tick(ctx, deadline)
	if err != nil || !ran || execution.Outcome != scheduler.Succeeded || calls != 1 {
		t.Fatalf("execution = %+v ran=%v calls=%d err=%v", execution, ran, calls, err)
	}
	if _, ran, err := r.worker.Tick(ctx, deadline.Add(time.Hour)); err != nil || ran || calls != 1 || stop() {
		t.Fatalf("Once repeated: ran=%v calls=%d err=%v", ran, calls, err)
	}
	job, err = st.Get(ctx, job.ID)
	if err != nil || job.State != scheduler.Completed || job.CompletedRuns != 1 {
		t.Fatalf("completed job = %+v %v", job, err)
	}
}

func TestTestExecutionUnavailableBookIsSkipped(t *testing.T) {
	for _, reason := range []string{"cancel", "context", "restart"} {
		t.Run(reason, func(t *testing.T) {
			_, n, _, host, st := resetTaskFixture(t)
			r, err := New(n, host, st)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := r.Close(); err != nil {
					t.Error(err)
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deadline := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
			stop, err := r.Schedule(ctx, deadline, func(time.Time) { t.Error("unavailable test order executed") })
			if err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "cancel":
				if !stop() || stop() {
					t.Fatal("cancellation is not idempotent")
				}
			case "context":
				cancel()
			case "restart":
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
				r, err = New(n, host, st)
				if err != nil {
					t.Fatal(err)
				}
			}
			execution, ran, err := r.worker.Tick(context.Background(), deadline)
			if err != nil || !ran || execution.Outcome != scheduler.Skipped || execution.Reason != "test_order_unavailable" {
				t.Fatalf("lost book execution = %+v ran=%v err=%v", execution, ran, err)
			}
		})
	}
}

type failedTestExecutionStore struct{ scheduler.Store }

func (s failedTestExecutionStore) Create(context.Context, scheduler.Job) error {
	return errors.New("store write failed")
}

func TestTestExecutionRegistrationFailure(t *testing.T) {
	_, n, _, host, st := resetTaskFixture(t)
	r, err := New(n, host, failedTestExecutionStore{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	stop, err := r.Schedule(context.Background(), time.Now().Add(time.Second), func(time.Time) { t.Fatal("failed registration executed") })
	if err == nil || stop != nil || len(r.executions) != 0 {
		t.Fatalf("registration failure: stop=%v err=%v calls=%d", stop != nil, err, len(r.executions))
	}
}

func TestTestExecutionRegistrationDuringSchedulerSuspension(t *testing.T) {
	_, n, _, host, st := resetTaskFixture(t)
	r, err := New(n, host, st)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := r.Suspend()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := resume(); err != nil {
			t.Error(err)
		}
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	done := make(chan error, 1)
	go func() {
		stop, err := r.Schedule(context.Background(), time.Now().Add(time.Hour), func(time.Time) { t.Error("cancelled order executed") })
		if err == nil && !stop() {
			err = errors.New("registration could not be cancelled")
		}
		done <- err
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTestExecutionRegistrationAfterClose(t *testing.T) {
	_, n, _, host, st := resetTaskFixture(t)
	r, err := New(n, host, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = r.Schedule(context.Background(), time.Now().Add(time.Second), func(time.Time) { t.Error("closed runtime executed") })
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("closed registration = %v", err)
	}
}
