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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/scheduler"
)

// testExecutionKind identifies a single delayed test-order execution.
const testExecutionKind = "test_execution"

type testExecutionConfig struct {
	ExecutionID domain.ExternalID `json:"executionId"`
}

type testExecutionCall struct {
	ctx context.Context
	run func(time.Time)
}

type testExecutionTask struct {
	owner  *Runtime
	config testExecutionConfig
}

func (r *Runtime) testExecutionTask(args json.RawMessage) (scheduler.Task, error) {
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	var config testExecutionConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("test execution arguments: %v: %w", err, scheduler.ErrInvalid)
	}
	if config.ExecutionID.IsZero() {
		return nil, fmt.Errorf("test execution ID is required: %w", scheduler.ErrInvalid)
	}
	return testExecutionTask{owner: r, config: config}, nil
}

func (task testExecutionTask) Run(ctx context.Context, run scheduler.Run) (json.RawMessage, error) {
	r := task.owner
	r.executionMu.Lock()
	call, found := r.executions[task.config.ExecutionID]
	delete(r.executions, task.config.ExecutionID)
	r.executionMu.Unlock()
	// The scheduler record survives a restart; the test order book does not.
	// Cancellation and a lost book must never recreate an executable order.
	if !found || call.ctx.Err() != nil || ctx.Err() != nil {
		return nil, scheduler.SkippedOccurrence{Reason: "test_order_unavailable"}
	}
	call.run(run.StartedAt)
	return nil, nil
}

// Schedule registers test execution as a standard persisted Once job. Only its
// execution ID is stored; the order callback belongs to the live test book.
// Cancellation withdraws that callback. The job records a skipped occurrence
// when due after cancellation, a connector restart or a process restart.
func (r *Runtime) Schedule(ctx context.Context, at time.Time, run func(time.Time)) (func() bool, error) {
	if ctx == nil || at.IsZero() || run == nil {
		return nil, fmt.Errorf("test execution requires context, deadline and handler")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := domain.NewExternalID()
	if err != nil {
		return nil, err
	}
	config, err := json.Marshal(testExecutionConfig{ExecutionID: id})
	if err != nil {
		return nil, err
	}
	r.executionMu.Lock()
	if r.executions == nil {
		r.executionMu.Unlock()
		return nil, domain.ErrConflict
	}
	r.executions[id] = testExecutionCall{ctx: ctx, run: run}
	r.executionMu.Unlock()
	_, err = r.worker.Add(ctx, scheduler.JobSpec{
		Kind: testExecutionKind, Config: config,
		Schedule: scheduler.Schedule{Kind: scheduler.Once, StartAt: at},
	}, scheduler.Ready)
	if err != nil {
		r.executionMu.Lock()
		delete(r.executions, id)
		r.executionMu.Unlock()
		return nil, err
	}
	return func() bool {
		r.executionMu.Lock()
		defer r.executionMu.Unlock()
		_, found := r.executions[id]
		delete(r.executions, id)
		return found
	}, nil
}
