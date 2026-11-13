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

// Package scheduler runs persistent tasks supplied by a host's static Factory.
// It does not own application authorization or domain effects.
// Each concrete Task owns the interpretation and validation of its opaque JSON
// arguments. The factory's stable type keys are hardcoded choices, never
// reflected classes, serialized callbacks or executable names.
//
// # Definitions and recurrence
//
// The Store persists one definition per independently submitted job, including
// recurrence progress. Add requires an explicit Ready or Paused initial state,
// persisted in the creation write. Repeating a job does not expand future rows.
// Past execution outcomes are separate immutable history.
//
// Schedules support a one-off instant, an anchored elapsed interval, or daily
// HH:mm in an explicit IANA timezone. Recurrences end never, after N successful
// completions including the first, or at an absolute instant. Both scheduled
// time and actual handler start must be at or before that end; an already running
// callback may finish later. Failures, interruptions and skips do not increment
// the successful count. Completed definitions and their counts remain stored.
//
// A pending one-off survives restart and runs when overdue. Recurrences execute
// at most one overdue occurrence, then advance to a future occurrence anchored
// to the original interval or calendar rule. A repeated DST minute runs once;
// a missing daily minute is recorded as skipped.
//
// # Claims and recovery
//
// The store atomically claims an occurrence before invoking its task; concurrent
// ticks cannot claim it twice. Run.ID remains stable across explicit retries so
// a task can implement effect-specific idempotence. Arbitrary external effects
// and persisted completion cannot be one atomic transaction. An unfinished claim
// becomes Interrupted on restart and is never silently replayed. Failures and
// partial outcomes require explicit retry or skip. Edits, pause and removal
// refuse an active claim.
//
// The host owns Start, Stop, cancellation and joining before closing or replacing
// its store. Worker errors remain visible through Scheduler.Error. The SQLite
// adapter is explicit, separate from the shared realm-store and node interfaces.
package scheduler
