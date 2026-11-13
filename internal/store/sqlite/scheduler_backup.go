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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/scheduler"
)

func refuseRunningSchedulerJobs(ctx context.Context, q sqlQueryer) error {
	var running bool
	if err := q.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM scheduler_job WHERE state = 'running')`).Scan(&running); err != nil {
		return err
	}
	if running {
		return fmt.Errorf("stop and join scheduled work before restore or reset: %w", domain.ErrConflict)
	}
	return nil
}

func (rt *restoreTx) restoreSchedulerJobs(ctx context.Context, jobs []scheduler.Job) error {
	for _, source := range jobs {
		job := source.Clone()
		if err := job.Validate(); err != nil {
			return fmt.Errorf("store: restore scheduled job: %v: %w", err, domain.ErrInvalid)
		}
		current, err := scanSchedulerJob(rt.tx.QueryRowContext(ctx,
			schedulerJobSelect+` WHERE j.job_id = ?`, job.ID))
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if rt.skip(backup.SectionGeneralSettings, exists) {
			continue
		}
		if job.State == scheduler.Running {
			interruptedSchedulerJob(&job, time.Now().UTC(), "restored an unfinished execution; effects are uncertain")
		}
		if job.Last != nil {
			if err := ensureSchedulerExecution(ctx, rt.tx, *job.Last); err != nil {
				return err
			}
		}
		if exists {
			// Retain the receiver's revision lineage: a stale UI write from
			// before restore must not match a source installation's revision.
			job.Revision = current.Revision
			err = updateSchedulerJob(ctx, rt.tx, job, "state <> 'running'", "")
		} else {
			err = insertSchedulerJob(ctx, rt.tx, job)
		}
		if err != nil {
			return err
		}
		rt.summary.AddApplied(backup.SectionGeneralSettings, 1)
	}
	return nil
}

func ensureSchedulerExecution(ctx context.Context, q sqlReadWriter, execution scheduler.Execution) error {
	if err := execution.Validate(); err != nil {
		return fmt.Errorf("store: restore scheduled execution: %v: %w", err, domain.ErrInvalid)
	}
	var stored string
	err := q.QueryRowContext(ctx,
		`SELECT record FROM scheduler_execution WHERE run_id = ? AND attempt = ?`,
		execution.ID, execution.Attempt).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return insertSchedulerExecution(ctx, q, execution)
	}
	if err != nil {
		return err
	}
	record, err := json.Marshal(execution)
	if err != nil {
		return err
	}
	var normalized bytes.Buffer
	if err := json.Compact(&normalized, []byte(stored)); err != nil {
		return err
	}
	if !bytes.Equal(normalized.Bytes(), record) {
		return fmt.Errorf("store: scheduled execution history conflicts with existing attempt: %w", domain.ErrInvalid)
	}
	return nil
}

func (rt *restoreTx) restoreSchedulerExecutions(ctx context.Context, executions []scheduler.Execution) error {
	for _, execution := range executions {
		exists, err := rowExists(ctx, rt.tx,
			`SELECT 1 FROM scheduler_execution WHERE run_id = ? AND attempt = ?`, execution.ID, execution.Attempt)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAuditLog, exists) {
			continue
		}
		if err := ensureSchedulerExecution(ctx, rt.tx, execution); err != nil {
			return err
		}
		rt.summary.AddApplied(backup.SectionAuditLog, 1)
	}
	return nil
}

func (rt *restoreTx) pruneSchedulerJobs(ctx context.Context, jobs []scheduler.Job) error {
	keep := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		keep[job.ID] = true
	}
	ids, err := scanStrings(rt.tx.QueryContext(ctx, `SELECT job_id FROM scheduler_job`))
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !keep[id] {
			if _, err := rt.tx.ExecContext(ctx, `DELETE FROM scheduler_job WHERE job_id = ?`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (rt *restoreTx) pruneSchedulerExecutions(ctx context.Context, executions []scheduler.Execution) error {
	keep := make(map[string]map[int64]bool)
	for _, execution := range executions {
		if keep[execution.ID] == nil {
			keep[execution.ID] = make(map[int64]bool)
		}
		keep[execution.ID][execution.Attempt] = true
	}
	stored, err := listSchedulerExecutions(ctx, rt.tx, "")
	if err != nil {
		return err
	}
	for _, execution := range stored {
		if keep[execution.ID][execution.Attempt] {
			continue
		}
		// A definitions-only restore carries the last outcome for status.
		// An audit-only restore cannot discard evidence a surviving job uses.
		if _, err := rt.tx.ExecContext(ctx,
			`DELETE FROM scheduler_execution WHERE run_id = ? AND attempt = ?
			 AND NOT EXISTS (SELECT 1 FROM scheduler_job WHERE last_run_id = ? AND last_attempt = ?)`,
			execution.ID, execution.Attempt, execution.ID, execution.Attempt); err != nil {
			return err
		}
	}
	return nil
}
