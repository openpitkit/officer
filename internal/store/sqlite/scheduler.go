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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/scheduler"
	fwstore "go.openpit.dev/officer/framework/store"
)

// SchedulerStore explicitly binds the scheduler to this connector's realm DB.
// It resolves the live database on every operation, including after Reset.
type SchedulerStore struct{ realm *realmStore }

var _ scheduler.Store = (*SchedulerStore)(nil)

// NewSchedulerStore requires the SQLite connector and its bound realm. Other
// connectors are an explicit error; no in-memory or generic-setting substitute
// is installed. Migrate must have completed before this call.
func NewSchedulerStore(ctx context.Context, st fwstore.Store, realm domain.RealmID) (*SchedulerStore, error) {
	s, ok := st.(*sqliteStore)
	if !ok || s == nil {
		return nil, fmt.Errorf("scheduler requires the SQLite connector")
	}
	r, err := s.ForRealm(ctx, realm)
	if err != nil {
		return nil, err
	}
	bound, ok := r.(*realmStore)
	if !ok {
		return nil, fmt.Errorf("scheduler requires a SQLite realm")
	}
	return &SchedulerStore{realm: bound}, nil
}

func schedulerColumns(job scheduler.Job) ([]any, error) {
	if err := job.Validate(); err != nil {
		return nil, err
	}
	var next, run, started, attempt, lastID, lastAttempt any
	if !job.Next.At.IsZero() {
		next = timeStr(job.Next.At)
	}
	if job.Active != nil {
		run = job.Active.ID
		started, attempt = timeStr(job.Active.StartedAt), job.Active.Attempt
	}
	if job.Last != nil {
		lastID, lastAttempt = job.Last.ID, job.Last.Attempt
	}
	spec, err := json.Marshal(job.Spec)
	if err != nil {
		return nil, err
	}
	return []any{string(spec), job.State, next, job.Next.Day, job.Next.Skip,
		job.CompletedRuns, run, started, attempt, lastID, lastAttempt,
		job.Error, job.Revision, job.ID}, nil
}

const schedulerJobSelect = `SELECT j.job_id, j.spec, j.state, j.next_at,
 j.next_day, j.next_skip, j.completed_runs, j.run_id, j.run_started_at,
 j.run_attempt, j.error, j.revision, j.last_run_id, e.record
 FROM scheduler_job j LEFT JOIN scheduler_execution e
 ON j.last_run_id = e.run_id AND j.last_attempt = e.attempt`

func scanSchedulerJob(row sqlScanner) (scheduler.Job, error) {
	var job scheduler.Job
	var spec string
	var next, run, started, lastID, last sql.NullString
	var attempt sql.NullInt64
	if err := row.Scan(&job.ID, &spec, &job.State, &next, &job.Next.Day,
		&job.Next.Skip, &job.CompletedRuns, &run, &started, &attempt,
		&job.Error, &job.Revision, &lastID, &last); err != nil {
		return scheduler.Job{}, err
	}
	if err := json.Unmarshal([]byte(spec), &job.Spec); err != nil {
		return scheduler.Job{}, err
	}
	if next.Valid {
		parsed, err := parseStoredTime("scheduler next_at", next.String)
		if err != nil {
			return scheduler.Job{}, err
		}
		job.Next.At = parsed
	}
	if run.Valid {
		parsed, err := parseStoredTime("scheduler run_started_at", started.String)
		if err != nil {
			return scheduler.Job{}, err
		}
		if !attempt.Valid {
			return scheduler.Job{}, fmt.Errorf("stored scheduler claim has no attempt")
		}
		job.Active = &scheduler.Run{ID: run.String, JobID: job.ID,
			Attempt: attempt.Int64, Occurrence: job.Next, StartedAt: parsed}
	}
	if lastID.Valid != last.Valid {
		return scheduler.Job{}, fmt.Errorf("stored scheduler last execution is missing")
	}
	if last.Valid {
		var execution scheduler.Execution
		if err := json.Unmarshal([]byte(last.String), &execution); err != nil {
			return scheduler.Job{}, err
		}
		job.Last = &execution
	}
	if err := job.Validate(); err != nil {
		return scheduler.Job{}, fmt.Errorf("store: invalid scheduled job: %w", err)
	}
	return job, nil
}

// Create inserts exactly one definition row, without any future-run rows.
func (s *SchedulerStore) Create(ctx context.Context, job scheduler.Job) error {
	if job.Revision != 1 || job.Active != nil || job.CompletedRuns != 0 || job.Last != nil {
		return fmt.Errorf("new job must have fresh progress: %w", scheduler.ErrInvalid)
	}
	db, err := s.realm.db()
	if err != nil {
		return err
	}
	return insertSchedulerJob(ctx, db, job)
}

func insertSchedulerJob(ctx context.Context, q sqlExecer, job scheduler.Job) error {
	args, err := schedulerColumns(job)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO scheduler_job (spec, state, next_at, next_day, next_skip,
		 completed_runs, run_id, run_started_at, run_attempt, last_run_id,
		 last_attempt, error, revision, job_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	if err != nil {
		return fmt.Errorf("store: create scheduled job: %w", err)
	}
	return nil
}

// Get reads one job by its stable public identifier.
func (s *SchedulerStore) Get(ctx context.Context, id string) (scheduler.Job, error) {
	db, err := s.realm.db()
	if err != nil {
		return scheduler.Job{}, err
	}
	job, err := scanSchedulerJob(db.QueryRowContext(ctx, schedulerJobSelect+` WHERE j.job_id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scheduler.Job{}, scheduler.ErrNotFound
		}
		return scheduler.Job{}, fmt.Errorf("store: read scheduled job: %w", err)
	}
	return job, nil
}

// List returns definitions with their current progress, ordered by job ID.
func (s *SchedulerStore) List(ctx context.Context) ([]scheduler.Job, error) {
	db, err := s.realm.db()
	if err != nil {
		return nil, err
	}
	return listSchedulerJobs(ctx, db, "")
}

func listSchedulerJobs(ctx context.Context, q sqlQueryer, state string) (result []scheduler.Job, resultErr error) {
	rows, err := q.QueryContext(ctx,
		schedulerJobSelect+` WHERE (? = '' OR j.state = ?) ORDER BY j.job_id`, state, state)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	jobs := make([]scheduler.Job, 0)
	for rows.Next() {
		job, err := scanSchedulerJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// Replace performs a revision-checked edit, refusing every running claim.
// The supplied Revision is the version read by the caller; it advances once.
func (s *SchedulerStore) Replace(ctx context.Context, job scheduler.Job) error {
	if job.Active != nil || job.State == scheduler.Running {
		return scheduler.ErrConflict
	}
	db, err := s.realm.db()
	if err != nil {
		return err
	}
	return updateSchedulerJob(ctx, db, job, "state <> 'running'", "")
}

func updateSchedulerJob(ctx context.Context, q sqlExecer, job scheduler.Job, guard, runID string) error {
	previous := job.Revision
	job.Revision++
	args, err := schedulerColumns(job)
	if err != nil {
		return err
	}
	args = append(args, previous)
	if runID != "" {
		args = append(args, runID)
	}
	result, err := q.ExecContext(ctx,
		`UPDATE scheduler_job SET spec = ?, state = ?, next_at = ?, next_day = ?, next_skip = ?,
		 completed_runs = ?, run_id = ?, run_started_at = ?, run_attempt = ?,
		 last_run_id = ?, last_attempt = ?, error = ?, revision = ?
		 WHERE job_id = ? AND revision = ? AND `+guard, args...)
	if err != nil {
		return fmt.Errorf("store: update scheduled job: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return scheduler.ErrConflict
	}
	return nil
}

// Remove deletes an inactive definition, preserving its execution history.
func (s *SchedulerStore) Remove(ctx context.Context, id string, revision int64) error {
	db, err := s.realm.db()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx,
		`DELETE FROM scheduler_job WHERE job_id = ? AND revision = ? AND state <> 'running'`, id, revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return scheduler.ErrConflict
	}
	return nil
}

// Claim atomically takes the earliest due ready job before any callback runs.
// The single-realm connector serializes its transactions on one connection.
func (s *SchedulerStore) Claim(ctx context.Context, now time.Time) (result scheduler.Job, claimed bool, err error) {
	db, err := s.realm.db()
	if err != nil {
		return scheduler.Job{}, false, err
	}
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		return scheduler.Job{}, false, err
	}
	defer rollbackTransaction(ctx, &err, tx)
	job, err := scanSchedulerJob(tx.QueryRowContext(ctx,
		schedulerJobSelect+` WHERE j.state = 'ready' AND j.next_at <= ?
		 ORDER BY j.next_at, j.job_id LIMIT 1`, timeStr(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return scheduler.Job{}, false, nil
	}
	if err != nil {
		return scheduler.Job{}, false, err
	}
	job.State = scheduler.Running
	job.Active = &scheduler.Run{
		ID: job.ID + ":" + timeStr(job.Next.At), JobID: job.ID,
		Attempt: job.Revision + 1, Occurrence: job.Next, StartedAt: now.UTC(),
	}
	if err := updateSchedulerJob(ctx, tx, job, "state = 'ready'", ""); err != nil {
		return scheduler.Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return scheduler.Job{}, false, err
	}
	job.Revision++
	return job, true, nil
}

// Finish atomically persists the claim's outcome, progress and history.
// A stale completion can never overwrite a replacement definition or claim.
func (s *SchedulerStore) Finish(ctx context.Context, job scheduler.Job, execution scheduler.Execution) (err error) {
	if err := execution.Validate(); err != nil {
		return err
	}
	if execution.JobID != job.ID || job.Active != nil || job.Last == nil ||
		job.Last.ID != execution.ID || job.Last.Attempt != execution.Attempt {
		return fmt.Errorf("completion does not match job: %w", scheduler.ErrInvalid)
	}
	db, err := s.realm.db()
	if err != nil {
		return err
	}
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, &err, tx)
	if err := updateSchedulerJob(ctx, tx, job, "state = 'running' AND run_id = ?", execution.ID); err != nil {
		return err
	}
	if err := insertSchedulerExecution(ctx, tx, execution); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSchedulerExecution(ctx context.Context, q sqlExecer, execution scheduler.Execution) error {
	if err := execution.Validate(); err != nil {
		return err
	}
	record, err := json.Marshal(execution)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO scheduler_execution (run_id, attempt, job_id, record) VALUES (?, ?, ?, ?)`,
		execution.ID, execution.Attempt, execution.JobID, string(record))
	return err
}

// Recover marks every previously running claim interrupted without replaying
// its possibly committed effects. The caller must first stop the old worker.
func (s *SchedulerStore) Recover(ctx context.Context, now time.Time) (err error) {
	db, err := s.realm.db()
	if err != nil {
		return err
	}
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, &err, tx)
	jobs, err := listSchedulerJobs(ctx, tx, scheduler.Running)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		execution := interruptedSchedulerJob(&job, now, "worker stopped before completion; effects are uncertain")
		if err := updateSchedulerJob(ctx, tx, job, "state = 'running' AND run_id = ?", execution.ID); err != nil {
			return err
		}
		if err := insertSchedulerExecution(ctx, tx, execution); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func interruptedSchedulerJob(job *scheduler.Job, now time.Time, reason string) scheduler.Execution {
	execution := scheduler.Execution{
		Run: *job.Active, FinishedAt: now.UTC(), Outcome: scheduler.Interrupted, Error: reason,
	}
	job.State, job.Active, job.Last = scheduler.Interrupted, nil, &execution
	job.Error = reason
	return execution
}

// History returns past execution attempts, including failures and interruption.
func (s *SchedulerStore) History(ctx context.Context, jobID string) ([]scheduler.Execution, error) {
	db, err := s.realm.db()
	if err != nil {
		return nil, err
	}
	return listSchedulerExecutions(ctx, db, jobID)
}

func listSchedulerExecutions(ctx context.Context, q sqlQueryer, jobID string) (result []scheduler.Execution, resultErr error) {
	rows, err := q.QueryContext(ctx,
		`SELECT record FROM scheduler_execution WHERE (? = '' OR job_id = ?) ORDER BY run_id, attempt`, jobID, jobID)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	out := make([]scheduler.Execution, 0)
	for rows.Next() {
		var raw string
		var execution scheduler.Execution
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &execution); err != nil {
			return nil, err
		}
		if err := execution.Validate(); err != nil {
			return nil, err
		}
		out = append(out, execution)
	}
	return out, rows.Err()
}
