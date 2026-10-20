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

-- The tables Pit Officer keeps apart from the shared framework schema, which
-- other compositions translate into their own databases: the access mode, the
-- credentials of token mode, the panel sessions and the record of sealing.
-- They are one migration with its own bookkeeping table, edited in place.
-- Timestamps are fixed-width RFC3339Nano UTC TEXT; binary is BLOB. None of
-- these tables enters a backup, and a restore leaves them untouched: the
-- access state belongs to the installation, not to the data it holds.

-- Single-row access mode. A clean database starts open; a database without
-- the row is refused at startup rather than given a mode.
CREATE TABLE access_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    mode      TEXT NOT NULL CHECK (mode IN ('token', 'open'))
);

INSERT INTO access_state (singleton, mode) VALUES (1, 'open');

-- The secrets of token mode, one row per kind: the panel password's argon2id
-- hash in PHC form and the values of the two tokens. value is sealed with the
-- master key when one is configured and plaintext otherwise, like every other
-- stored secret; the kind is the row id the sealed value is bound to.
CREATE TABLE access_credential (
    kind      TEXT PRIMARY KEY
              CHECK (kind IN ('panel_password', 'api_token', 'agent_token')),
    value     BLOB NOT NULL,
    issued_at TEXT NOT NULL
);

-- Panel sessions. id_hash is the SHA-256 of the session id, which only the
-- browser's cookie holds, so the table holds no secret and is not sealed.
CREATE TABLE panel_session (
    id_hash      BLOB PRIMARY KEY CHECK (length(id_hash) = 32),
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL
);

-- What sealing the database recorded: when, and how many stored secrets held
-- a value then. Those had been stored in the clear and must be considered
-- exposed. The row exists only once the database has been sealed.
CREATE TABLE seal_report (
    singleton        INTEGER PRIMARY KEY CHECK (singleton = 1),
    resealed_secrets INTEGER NOT NULL CHECK (resealed_secrets >= 0),
    sealed_at        TEXT NOT NULL
);
