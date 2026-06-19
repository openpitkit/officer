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
-- Please see https://openpit.dev and the OWNERS file for details.

-- Ed25519 signing keypairs. Private key is stored as a plaintext BLOB (at-rest
-- encryption is deferred). active=1 means this key is used for new tokens;
-- inactive keys are retained so connectors can verify in-flight tokens.
CREATE TABLE signing_keys (
    key_id      TEXT    PRIMARY KEY,
    alg         TEXT    NOT NULL,
    private_key BLOB    NOT NULL,
    public_key  BLOB    NOT NULL,
    created_at  TEXT    NOT NULL,
    active      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_signing_keys_active ON signing_keys (active);

-- Global signing configuration. Currently a single key: no_esign ("0"/"1").
CREATE TABLE signing_config ( key TEXT PRIMARY KEY, value TEXT NOT NULL );
INSERT OR IGNORE INTO signing_config (key, value) VALUES ('no_esign', '0');

-- Reservation intent rows survive process restart. On boot any non-terminal
-- (state='held') row is orphaned and rolled back; the native reservation handle
-- does not survive restart. params_json and lock_prices_json are compact JSON.
CREATE TABLE reservation_intents (
    approval_id      TEXT PRIMARY KEY,
    order_id         INTEGER NOT NULL,
    account          TEXT NOT NULL,
    params_json      TEXT NOT NULL,
    lock_prices_json TEXT NOT NULL,
    issued_at        TEXT NOT NULL,
    expires_at       TEXT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'held'
);
CREATE INDEX idx_reservation_intents_state ON reservation_intents (state);
