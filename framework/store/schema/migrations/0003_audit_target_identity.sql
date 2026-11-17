-- Copyright The Pit Project Owners. All rights reserved.
-- SPDX-License-Identifier: Apache-2.0
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.
--
-- Please see https://officer.openpit.dev and the OWNERS file for details.

-- Audit identity snapshots outlive dictionary deletes and are never foreign
-- keys. Zero explicitly denotes an unavailable identity. No historical ID is
-- reconstructed from a mutable code.
ALTER TABLE audit ADD COLUMN account_identity BIGINT NOT NULL DEFAULT 0;
ALTER TABLE audit ADD COLUMN group_identity BIGINT NOT NULL DEFAULT 0;

-- Position ceilings have exactly one scope: an account and an asset.
-- The scope column lets the shared limit-table code apply unchanged.
CREATE TABLE limit_position (
    id           {{PK}},
    scope        TEXT NOT NULL CHECK (scope = 'account_asset'),
    account_id   INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    asset_id     INTEGER NOT NULL REFERENCES asset(id)   ON DELETE CASCADE,
    max_position TEXT NOT NULL
);

CREATE INDEX idx_limit_position_account ON limit_position (account_id);
CREATE INDEX idx_limit_position_asset ON limit_position (asset_id);
CREATE UNIQUE INDEX uq_limit_position ON limit_position (scope, account_id, asset_id);

-- One row per job, including its recurrence and progress. Task arguments and
-- schedule definition live in spec; mutable progress has one scalar authority.
-- No future occurrence rows are materialized.
CREATE TABLE scheduler_job (
    job_id   TEXT PRIMARY KEY,
    spec     TEXT NOT NULL,
    state    TEXT NOT NULL CHECK (state IN
             ('ready', 'paused', 'running', 'failed', 'interrupted', 'completed')),
    next_at  TEXT,
    next_day TEXT NOT NULL,
    next_skip {{BOOL}} NOT NULL,
    completed_runs BIGINT NOT NULL CHECK (completed_runs >= 0),
    run_id   TEXT,
    run_started_at TEXT,
    run_attempt BIGINT,
    last_run_id TEXT,
    last_attempt BIGINT,
    error TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    CHECK ((state = 'running' AND run_id IS NOT NULL AND
            run_started_at IS NOT NULL AND run_attempt IS NOT NULL AND run_attempt > 0) OR
           (state <> 'running' AND run_id IS NULL AND
            run_started_at IS NULL AND run_attempt IS NULL)),
    CHECK ((state = 'completed' AND next_at IS NULL) OR
           (state <> 'completed' AND next_at IS NOT NULL)),
    CHECK ((last_run_id IS NULL AND last_attempt IS NULL) OR
           (last_run_id IS NOT NULL AND last_attempt IS NOT NULL AND last_attempt > 0))
);
CREATE INDEX scheduler_job_due ON scheduler_job (state, next_at, job_id);

-- Past outcomes survive job removal. There is deliberately no foreign key
-- from history to the editable/removable job definition.
CREATE TABLE scheduler_execution (
    run_id TEXT NOT NULL,
    attempt BIGINT NOT NULL CHECK (attempt > 0),
    job_id TEXT NOT NULL,
    record TEXT NOT NULL,
    PRIMARY KEY (run_id, attempt)
);
CREATE INDEX scheduler_execution_job ON scheduler_execution (job_id, run_id);

-- Past successful account effects, not future recurrence occurrences. A rename
-- retains the live identity; deleting an account deletes its receipts.
CREATE TABLE account_pnl_reset_receipt (
    run_id TEXT NOT NULL,
    account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    PRIMARY KEY (run_id, account_id)
);
-- Open events and personal marks live in the durable realm database.
CREATE TABLE actionable_alert (
    id {{PK}},
    external_id {{XID}} UNIQUE,
    type TEXT NOT NULL,
    at TEXT NOT NULL,
    source TEXT NOT NULL,
    payload TEXT NOT NULL
);
CREATE TABLE alert_read_state (
    alert_id BIGINT NOT NULL REFERENCES actionable_alert(id) ON DELETE CASCADE,
    principal TEXT NOT NULL,
    is_read {{BOOL}} NOT NULL,
    PRIMARY KEY(alert_id, principal)
);
CREATE TABLE alert_delivery (
    alert_id BIGINT NOT NULL REFERENCES actionable_alert(id) ON DELETE CASCADE,
    channel TEXT NOT NULL,
    failed {{BOOL}} NOT NULL,
    PRIMARY KEY(alert_id, channel)
);
CREATE INDEX idx_actionable_alert_at ON actionable_alert(at, id);

CREATE TABLE trading_test_policy (
    connection_id INTEGER PRIMARY KEY REFERENCES trading_connection(id) ON DELETE CASCADE,
    revision      INTEGER NOT NULL CHECK (revision > 0),
    rules         TEXT    NOT NULL
);
