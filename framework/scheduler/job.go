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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Errors distinguish invalid input, absent jobs, conflicting state and a task
// type the host factory does not implement.
var (
	ErrInvalid     = errors.New("invalid scheduler input")
	ErrNotFound    = errors.New("scheduled job not found")
	ErrConflict    = errors.New("scheduled job state conflict")
	ErrUnknownKind = errors.New("unknown scheduled task kind")
)

// Task is one concrete implementation instantiated from typed JSON config.
// A handler must use Run.ID for idempotence when its effects allow it. An error
// may accompany a partial JSON result; it never counts as a completed run.
type Task interface {
	Run(context.Context, Run) (json.RawMessage, error)
}

// SkippedOccurrence is an explicit task decision to skip this boundary without
// counting a success. Reason is a stable non-empty diagnostic code; optional
// result JSON can carry task-owned details. Other errors remain failures.
type SkippedOccurrence struct{ Reason string }

func (e SkippedOccurrence) Error() string { return e.Reason }

// Factory is the host's hardcoded kind-to-implementation switch. It validates
// config and returns an error for unknown kinds or invalid typed configuration.
// Persisted kind names are stable keys, never reflection or executable names.
type Factory func(kind string, config json.RawMessage) (Task, error)

// Schedule defines the first instant and either an elapsed interval or a daily
// wall-clock rule. StartAt is required for every kind; for daily it is the
// inclusive earliest boundary. Daily missing minutes are skipped and repeated
// minutes use their first occurrence. Unused fields must be empty.
type Schedule struct {
	Kind     string        `json:"kind"`
	StartAt  time.Time     `json:"startAt"`
	Every    time.Duration `json:"every,omitempty"`
	Time     string        `json:"time,omitempty"`
	Timezone string        `json:"timezone,omitempty"`
}

// Schedule kinds.
const (
	Once     = "once"
	Interval = "interval"
	Daily    = "daily"
)

// Termination is explicit for recurrences: forever, count (including the first
// completed execution), or until (scheduled_at <= Until). A one-off has no
// termination fields. Failed, interrupted and skipped occurrences do not count.
type Termination struct {
	Mode    string    `json:"mode,omitempty"`
	MaxRuns int64     `json:"maxRuns,omitempty"`
	Until   time.Time `json:"until,omitempty"`
}

// Recurrence termination modes.
const (
	Forever = "forever"
	Count   = "count"
	Until   = "until"
)

// JobSpec is a persisted task definition. Config contains only the concrete
// task's typed JSON configuration; callbacks and credentials do not belong here.
type JobSpec struct {
	Kind        string          `json:"kind"`
	Config      json.RawMessage `json:"config"`
	Schedule    Schedule        `json:"schedule"`
	Termination Termination     `json:"termination"`
}

// Validate checks the task-independent definition. The factory checks Config.
func (s JobSpec) Validate() error {
	if s.Kind == "" || strings.TrimSpace(s.Kind) != s.Kind {
		return fmt.Errorf("task kind is required: %w", ErrInvalid)
	}
	if !json.Valid(s.Config) {
		return fmt.Errorf("task config must be JSON: %w", ErrInvalid)
	}
	if s.Schedule.StartAt.IsZero() {
		return fmt.Errorf("schedule startAt is required: %w", ErrInvalid)
	}
	if s.Schedule.StartAt.Year() < 1 || s.Schedule.StartAt.Year() > 9998 {
		return fmt.Errorf("schedule startAt is out of range: %w", ErrInvalid)
	}
	switch s.Schedule.Kind {
	case Once, Interval:
		if s.Schedule.Time != "" || s.Schedule.Timezone != "" {
			return fmt.Errorf("only daily schedules take a clock and timezone: %w", ErrInvalid)
		}
		if (s.Schedule.Kind == Once && s.Schedule.Every != 0) ||
			(s.Schedule.Kind == Interval && s.Schedule.Every <= 0) {
			return fmt.Errorf("interval must be positive and only used for interval schedules: %w", ErrInvalid)
		}
	case Daily:
		clock, err := time.Parse("15:04", s.Schedule.Time)
		if err != nil || clock.Format("15:04") != s.Schedule.Time || s.Schedule.Every != 0 {
			return fmt.Errorf("daily schedule requires HH:mm and no interval: %w", ErrInvalid)
		}
		if s.Schedule.Timezone == "" || s.Schedule.Timezone == "Local" {
			return fmt.Errorf("daily schedule requires an IANA timezone: %w", ErrInvalid)
		}
		if _, err := time.LoadLocation(s.Schedule.Timezone); err != nil {
			return fmt.Errorf("unknown timezone %q: %w", s.Schedule.Timezone, ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown schedule kind %q: %w", s.Schedule.Kind, ErrInvalid)
	}
	t := s.Termination
	if s.Schedule.Kind == Once {
		if t != (Termination{}) {
			return fmt.Errorf("one-off schedule has no recurrence termination: %w", ErrInvalid)
		}
		return nil
	}
	switch t.Mode {
	case Forever:
		if t.MaxRuns != 0 || !t.Until.IsZero() {
			return fmt.Errorf("forever takes no count or end: %w", ErrInvalid)
		}
	case Count:
		if t.MaxRuns <= 0 || !t.Until.IsZero() {
			return fmt.Errorf("count requires positive maxRuns and no end: %w", ErrInvalid)
		}
	case Until:
		if t.MaxRuns != 0 || t.Until.IsZero() || t.Until.Before(s.Schedule.StartAt) {
			return fmt.Errorf("until requires an end at or after startAt and no count: %w", ErrInvalid)
		}
	default:
		return fmt.Errorf("recurrence termination must be explicit: %w", ErrInvalid)
	}
	return nil
}

// Occurrence is one calculated boundary, stored in the job row rather than
// expanded into rows for future runs. Skip marks a missing daily DST minute.
type Occurrence struct {
	At   time.Time `json:"at"`
	Day  string    `json:"day,omitempty"`
	Skip bool      `json:"skip,omitempty"`
}

// Run identifies the currently claimed occurrence. ID stays stable for the
// same job and occurrence, across pause/resume and process restarts.
type Run struct {
	ID         string     `json:"id"`
	JobID      string     `json:"jobId"`
	Attempt    int64      `json:"attempt"`
	Occurrence Occurrence `json:"occurrence"`
	StartedAt  time.Time  `json:"startedAt"`
}

// Execution is past execution history, separate from the single job row.
// Error holds a failed or uncertain execution's diagnostic. Result is optional
// JSON emitted by the task, including any partial results when it fails.
type Execution struct {
	Run
	FinishedAt time.Time       `json:"finishedAt"`
	Outcome    string          `json:"outcome"`
	Reason     string          `json:"reason,omitempty"`
	Error      string          `json:"error,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}

// Job states and execution outcomes.
const (
	Ready       = "ready"
	Paused      = "paused"
	Running     = "running"
	Failed      = "failed"
	Interrupted = "interrupted"
	Completed   = "completed"
	Succeeded   = "succeeded"
	Skipped     = "skipped"
)

// Job contains a definition and its durable progress in one row. Revision is
// an optimistic concurrency token. Editing a running claim is always refused.
type Job struct {
	ID            string     `json:"id"`
	Spec          JobSpec    `json:"spec"`
	State         string     `json:"state"`
	Next          Occurrence `json:"next"`
	CompletedRuns int64      `json:"completedRuns"`
	Active        *Run       `json:"active,omitempty"`
	Last          *Execution `json:"last,omitempty"`
	Error         string     `json:"error,omitempty"`
	Revision      int64      `json:"revision"`
}

// Clone copies mutable JSON arguments, result bytes and optional snapshots.
func (j Job) Clone() Job {
	j.Spec.Config = slices.Clone(j.Spec.Config)
	if j.Active != nil {
		active := *j.Active
		j.Active = &active
	}
	if j.Last != nil {
		last := j.Last.Clone()
		j.Last = &last
	}
	return j
}

// Clone copies the task's opaque JSON result bytes.
func (e Execution) Clone() Execution {
	e.Result = slices.Clone(e.Result)
	return e
}

// Validate checks a portable job's structural consistency. Only the concrete
// task factory interprets Config; restoring a definition does not execute it.
func (j Job) Validate() error {
	if err := j.Spec.Validate(); err != nil {
		return err
	}
	if j.ID == "" || j.Revision <= 0 || j.CompletedRuns < 0 {
		return fmt.Errorf("invalid job identity or progress: %w", ErrInvalid)
	}
	if j.Spec.Termination.Mode == Count && j.CompletedRuns >= j.Spec.Termination.MaxRuns && j.State != Completed {
		return fmt.Errorf("job has exhausted its successful-run count: %w", ErrInvalid)
	}
	if j.State != Completed && j.Next.At.Before(j.Spec.Schedule.StartAt) {
		return fmt.Errorf("job boundary precedes its schedule start: %w", ErrInvalid)
	}
	if (j.State == Failed || j.State == Interrupted) && j.Error == "" {
		return fmt.Errorf("failed or interrupted job requires an error: %w", ErrInvalid)
	}
	switch j.State {
	case Ready, Paused, Failed, Interrupted:
		if j.Next.At.IsZero() || j.Active != nil {
			return fmt.Errorf("inactive job needs next boundary and no claim: %w", ErrInvalid)
		}
	case Running:
		if j.Active == nil || j.Active.JobID != j.ID || j.Active.ID == "" ||
			j.Active.StartedAt.IsZero() || j.Active.Attempt <= 0 || j.Active.Occurrence != j.Next {
			return fmt.Errorf("running job needs its exact active claim: %w", ErrInvalid)
		}
	case Completed:
		if j.Active != nil || !j.Next.At.IsZero() {
			return fmt.Errorf("completed job has active work: %w", ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown job state %q: %w", j.State, ErrInvalid)
	}
	if j.Last != nil {
		if err := j.Last.Validate(); err != nil {
			return err
		}
		if j.Last.JobID != j.ID {
			return fmt.Errorf("execution belongs to another job: %w", ErrInvalid)
		}
	}
	return nil
}

// Validate rejects malformed past execution records before persistence/import.
func (e Execution) Validate() error {
	if e.ID == "" || e.JobID == "" || e.Attempt <= 0 || e.Occurrence.At.IsZero() ||
		e.StartedAt.IsZero() || e.FinishedAt.IsZero() {
		return fmt.Errorf("execution identity and timestamps are required: %w", ErrInvalid)
	}
	if len(e.Result) > 0 && !json.Valid(e.Result) {
		return fmt.Errorf("execution result must be JSON: %w", ErrInvalid)
	}
	switch e.Outcome {
	case Succeeded, Skipped:
		if e.Error != "" {
			return fmt.Errorf("successful or skipped execution carries error: %w", ErrInvalid)
		}
	case Failed, Interrupted:
		if e.Error == "" {
			return fmt.Errorf("failed execution needs error: %w", ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown execution outcome %q: %w", e.Outcome, ErrInvalid)
	}
	return nil
}

// Store atomically claims due jobs, preserves one row per definition and
// commits completion with its past execution history. It is separate from a
// host's domain store. Recover is called only after the previous worker has
// stopped; this package does not implement distributed worker leases.
type Store interface {
	Create(context.Context, Job) error
	Get(context.Context, string) (Job, error)
	List(context.Context) ([]Job, error)
	Replace(context.Context, Job) error
	Remove(context.Context, string, int64) error
	Claim(context.Context, time.Time) (Job, bool, error)
	Finish(context.Context, Job, Execution) error
	Recover(context.Context, time.Time) error
	History(context.Context, string) ([]Execution, error)
}
