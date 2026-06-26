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

-- Canonical control-plane schema for one realm, created from scratch in a
-- single migration. It is backend-agnostic: the dialect substitutes {{PK}}
-- (surrogate-PK identity), {{XID}} (16-byte external-id column) and {{BOOL}}
-- (boolean) before the DDL runs. Identity model: an internal integer surrogate
-- key (id) joins rows and never leaves the store; a non-guessable external_id
-- is the public handle of machine records; engine_account_id/engine_group_id are
-- the integer ids the engine runs on, assigned by the connector. Dictionaries
-- are addressed by an immutable code with a mutable display title. Money, price
-- and quantity are exact-decimal TEXT (never float); timestamps are RFC3339Nano
-- UTC TEXT; binary is BLOB. The schema_migrations bookkeeping table is created
-- by the Go migration harness, not here.

-- Single-row identity of the realm this schema holds: recorded once for backup
-- labelling and future placement. It is not a foreign key on any other table.
CREATE TABLE realm (
    id          {{PK}},
    external_id {{XID}} UNIQUE,
    code        TEXT    NOT NULL,
    title       TEXT    NOT NULL DEFAULT ''
);

-- Tradable assets dictionary. code is the immutable human handle, unique per
-- realm; title is the mutable display string; asset_class is optional.
CREATE TABLE assets (
    id          {{PK}},
    code        TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL DEFAULT '',
    asset_class TEXT
);

-- Principals dictionary: actors that initiate control-plane actions.
CREATE TABLE principals (
    id    {{PK}},
    code  TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT ''
);

-- Account groups dictionary. engine_group_id is the connector-assigned integer
-- the engine runs the group on; it is internal and never exposed on the wire.
CREATE TABLE account_groups (
    id              {{PK}},
    engine_group_id INTEGER NOT NULL UNIQUE,
    code            TEXT    NOT NULL UNIQUE,
    title           TEXT    NOT NULL DEFAULT '',
    notes           TEXT    NOT NULL DEFAULT '',
    blocked         {{BOOL}} NOT NULL DEFAULT 0,
    block_reason    TEXT    NOT NULL DEFAULT ''
);

-- Accounts dictionary. engine_account_id is the connector-assigned integer the
-- engine runs the account on (internal, never on the wire). group_id links to a
-- group and is cleared (SET NULL), not cascaded, when the group is deleted so an
-- account survives its group's removal.
CREATE TABLE accounts (
    id                {{PK}},
    engine_account_id INTEGER NOT NULL UNIQUE,
    code              TEXT    NOT NULL UNIQUE,
    title             TEXT    NOT NULL DEFAULT '',
    group_id          INTEGER REFERENCES account_groups(id) ON DELETE SET NULL,
    notes             TEXT    NOT NULL DEFAULT '',
    blocked           {{BOOL}} NOT NULL DEFAULT 0,
    block_reason      TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_accounts_group ON accounts (group_id);

-- Per-(account, asset) holdings snapshot. Amounts are exact TEXT decimals,
-- never float. realized_pnl is delta-accumulated (a fresh row starts at '0').
-- Both references cascade so deleting an account or asset removes its balances.
CREATE TABLE balances (
    account_id          INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    asset_id            INTEGER NOT NULL REFERENCES assets(id)   ON DELETE CASCADE,
    available           TEXT    NOT NULL DEFAULT '0',
    held                TEXT    NOT NULL DEFAULT '0',
    incoming            TEXT    NOT NULL DEFAULT '0',
    average_entry_price TEXT    NOT NULL DEFAULT '',
    realized_pnl        TEXT    NOT NULL DEFAULT '0',
    updated_at          TEXT    NOT NULL,
    PRIMARY KEY (account_id, asset_id)
);

CREATE INDEX idx_balances_asset   ON balances (asset_id);

-- Per-policy typed limit tables (replacing the former EAV limits table). Each
-- carries a hardcoded enum scope and nullable account/asset axes present only
-- for scopes that carry them; the natural composite (scope, account, asset) is
-- the addressable key, so there is no external_id.

-- Rate-limit barriers: at most max_orders new orders per rolling window.
CREATE TABLE limit_rate (
    id         {{PK}},
    scope      TEXT NOT NULL,
    account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    asset_id   INTEGER REFERENCES assets(id)   ON DELETE CASCADE,
    max_orders INTEGER NOT NULL,
    window     TEXT    NOT NULL
);

CREATE INDEX idx_limit_rate_account ON limit_rate (account_id);
CREATE INDEX idx_limit_rate_asset ON limit_rate (asset_id);
CREATE UNIQUE INDEX uq_limit_rate
    ON limit_rate (scope, COALESCE(account_id, 0), COALESCE(asset_id, 0));

-- Order-size barriers: per-order quantity and/or notional ceilings.
CREATE TABLE limit_order_size (
    id           {{PK}},
    scope        TEXT NOT NULL,
    account_id   INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    asset_id     INTEGER REFERENCES assets(id)   ON DELETE CASCADE,
    max_quantity TEXT,
    max_notional TEXT
);

CREATE INDEX idx_limit_order_size_account ON limit_order_size (account_id);
CREATE INDEX idx_limit_order_size_asset ON limit_order_size (asset_id);
CREATE UNIQUE INDEX uq_limit_order_size
    ON limit_order_size (scope, COALESCE(account_id, 0), COALESCE(asset_id, 0));

-- P&L-bounds kill-switch barriers: lower/upper accumulated-P&L bounds and an
-- optional seed for the per-account accumulator.
CREATE TABLE limit_pnl_bounds (
    id          {{PK}},
    scope       TEXT NOT NULL,
    account_id  INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    asset_id    INTEGER REFERENCES assets(id)   ON DELETE CASCADE,
    lower_bound TEXT,
    upper_bound TEXT,
    initial_pnl TEXT
);

CREATE INDEX idx_limit_pnl_bounds_account ON limit_pnl_bounds (account_id);
CREATE INDEX idx_limit_pnl_bounds_asset ON limit_pnl_bounds (asset_id);
CREATE UNIQUE INDEX uq_limit_pnl_bounds
    ON limit_pnl_bounds (scope, COALESCE(account_id, 0), COALESCE(asset_id, 0));

-- Append-only history of spot-funds adjustments. request/outcome are opaque
-- JSON. principal is cleared (SET NULL) when the principal is removed; account
-- and asset cascade.
CREATE TABLE adjustments (
    id           {{PK}},
    external_id  {{XID}} UNIQUE,
    account_id   INTEGER NOT NULL REFERENCES accounts(id)   ON DELETE CASCADE,
    asset_id     INTEGER NOT NULL REFERENCES assets(id)     ON DELETE CASCADE,
    principal_id INTEGER REFERENCES principals(id)          ON DELETE SET NULL,
    at           TEXT    NOT NULL,
    source       TEXT    NOT NULL,
    status       TEXT    NOT NULL,
    request      TEXT    NOT NULL,
    outcome      TEXT    NOT NULL
);

CREATE INDEX idx_adjustments_account ON adjustments (account_id, at DESC, id DESC);
CREATE INDEX idx_adjustments_asset ON adjustments (asset_id);
CREATE INDEX idx_adjustments_principal ON adjustments (principal_id);
CREATE INDEX idx_adjustments_source  ON adjustments (source, at DESC, id DESC);

-- Orders recorded by Officer (including rejected ones). lock is the
-- SDK-serialized pretrade.Lock blob, persisted verbatim; the store never decodes
-- it. price is empty for market orders. The signed approval, when present, lives
-- in the 1:1 order_approvals companion, not inline here.
CREATE TABLE orders (
    id             {{PK}},
    external_id    {{XID}} UNIQUE,
    account_id     INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    base_asset_id  INTEGER NOT NULL REFERENCES assets(id)   ON DELETE CASCADE,
    quote_asset_id INTEGER NOT NULL REFERENCES assets(id)   ON DELETE CASCADE,
    principal_id   INTEGER REFERENCES principals(id)        ON DELETE SET NULL,
    at             TEXT    NOT NULL,
    source         TEXT    NOT NULL,
    side           TEXT    NOT NULL,
    amount_kind    TEXT    NOT NULL,
    amount_value   TEXT    NOT NULL,
    price          TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL,
    lock           BLOB
);

CREATE INDEX idx_orders_account ON orders (account_id, at DESC, id DESC);
CREATE INDEX idx_orders_principal ON orders (principal_id);
CREATE INDEX idx_orders_base_asset ON orders (base_asset_id);
CREATE INDEX idx_orders_quote_asset ON orders (quote_asset_id);
CREATE INDEX idx_orders_source  ON orders (source, at DESC, id DESC);

-- Signed approval envelope, 1:1 with an order and absent when the order is
-- unsigned. signing_key_id references the signing key surrogate and is RESTRICT
-- so a key in use cannot be dropped.
CREATE TABLE order_approvals (
    order_id       INTEGER PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    token          TEXT    NOT NULL,
    signing_key_id INTEGER NOT NULL REFERENCES signing_keys(id) ON DELETE RESTRICT,
    alg            TEXT    NOT NULL,
    mode           TEXT    NOT NULL,
    issued_at      TEXT    NOT NULL,
    expires_at     TEXT    NOT NULL
);

CREATE INDEX idx_order_approvals_signing_key ON order_approvals (signing_key_id);

-- Immutable event stream for an order. payload is opaque JSON. principal is
-- cleared (SET NULL) when removed; the parent order cascades.
CREATE TABLE order_events (
    id           {{PK}},
    external_id  {{XID}} UNIQUE,
    order_id     INTEGER NOT NULL REFERENCES orders(id)  ON DELETE CASCADE,
    principal_id INTEGER REFERENCES principals(id)       ON DELETE SET NULL,
    at           TEXT    NOT NULL,
    type         TEXT    NOT NULL,
    source       TEXT    NOT NULL,
    payload      TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_order_events_order ON order_events (order_id, at DESC, id DESC);
CREATE INDEX idx_order_events_principal ON order_events (principal_id);

-- Per-fill trade records ("reports"); one row per fill. lock_price is empty when
-- not applicable. order, account and assets cascade; principal is cleared.
CREATE TABLE trades (
    id             {{PK}},
    external_id    {{XID}} UNIQUE,
    order_id       INTEGER NOT NULL REFERENCES orders(id)   ON DELETE CASCADE,
    account_id     INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    base_asset_id  INTEGER NOT NULL REFERENCES assets(id)   ON DELETE CASCADE,
    quote_asset_id INTEGER NOT NULL REFERENCES assets(id)   ON DELETE CASCADE,
    principal_id   INTEGER REFERENCES principals(id)        ON DELETE SET NULL,
    at             TEXT    NOT NULL,
    source         TEXT    NOT NULL,
    side           TEXT    NOT NULL,
    quantity       TEXT    NOT NULL,
    price          TEXT    NOT NULL,
    lock_price     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_trades_account ON trades (account_id, at DESC, id DESC);
CREATE INDEX idx_trades_principal ON trades (principal_id);
CREATE INDEX idx_trades_base_asset ON trades (base_asset_id);
CREATE INDEX idx_trades_quote_asset ON trades (quote_asset_id);
CREATE INDEX idx_trades_source  ON trades (source, at DESC, id DESC);
CREATE INDEX idx_trades_order   ON trades (order_id);

-- Append-only audit trail. Account and actor are immutable snapshots, not
-- foreign keys, so compliance history survives dictionary deletes unchanged.
CREATE TABLE audit (
    id            {{PK}},
    external_id   {{XID}} UNIQUE,
    account_code  TEXT NOT NULL DEFAULT '',
    account_title TEXT NOT NULL DEFAULT '',
    actor_code    TEXT NOT NULL DEFAULT '',
    actor_title   TEXT NOT NULL DEFAULT '',
    at            TEXT NOT NULL,
    action        TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'system',
    detail        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_at ON audit (at DESC, id DESC);
CREATE INDEX idx_audit_account_code ON audit (account_code, at DESC, id DESC);

-- Market-data connector instances. provider is a hardcoded enum; label is a
-- unique, case-insensitive operator-facing name; credentials is an opaque JSON
-- blob; enabled gates runtime participation.
CREATE TABLE market_data_instances (
    id          {{PK}},
    external_id {{XID}} UNIQUE,
    provider    TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '' COLLATE NOCASE UNIQUE,
    credentials TEXT NOT NULL DEFAULT '',
    enabled     {{BOOL}} NOT NULL DEFAULT 0
);

-- Per-instrument selection for an instance: the external source symbol mapped to
-- an engine instrument (base, quote). The instance reference cascades.
CREATE TABLE market_data_instruments (
    id              {{PK}},
    instance_id     INTEGER NOT NULL REFERENCES market_data_instances(id) ON DELETE CASCADE,
    external_symbol TEXT    NOT NULL,
    base_asset_id   INTEGER NOT NULL REFERENCES assets(id)  ON DELETE CASCADE,
    quote_asset_id  INTEGER NOT NULL REFERENCES assets(id)  ON DELETE CASCADE,
    enabled         {{BOOL}} NOT NULL DEFAULT 0,
    manual_price    TEXT    NOT NULL DEFAULT '',
    UNIQUE (instance_id, external_symbol)
);

CREATE INDEX idx_market_data_instruments_base_asset
    ON market_data_instruments (base_asset_id);
CREATE INDEX idx_market_data_instruments_quote_asset
    ON market_data_instruments (quote_asset_id);

-- Latest normalized quote per configured instrument, 1:1 with the instrument and
-- overwritten on each update (an operational snapshot, not a tick history).
CREATE TABLE market_data_quotes (
    instrument_id INTEGER PRIMARY KEY REFERENCES market_data_instruments(id) ON DELETE CASCADE,
    mark          TEXT NOT NULL DEFAULT '',
    bid           TEXT NOT NULL DEFAULT '',
    ask           TEXT NOT NULL DEFAULT '',
    as_of         TEXT NOT NULL,
    received_at   TEXT NOT NULL
);

-- Ed25519 signing keypairs. key_id is the key's own UUID handle. private_key is
-- a plaintext BLOB (at-rest encryption deferred). active=1 marks the signing key.
CREATE TABLE signing_keys (
    id          {{PK}},
    key_id      TEXT    NOT NULL UNIQUE,
    alg         TEXT    NOT NULL,
    private_key BLOB    NOT NULL,
    public_key  BLOB    NOT NULL,
    created_at  TEXT    NOT NULL,
    active      {{BOOL}} NOT NULL DEFAULT 0
);

CREATE INDEX idx_signing_keys_active ON signing_keys (active);

-- Global signing configuration; key is a hardcoded enum, not a dictionary.
CREATE TABLE signing_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO signing_config (key, value) VALUES ('no_esign', '0');

-- Per-command MCP access overrides; command is the key, enabled is the toggle.
CREATE TABLE mcp_access (
    command TEXT PRIMARY KEY,
    enabled {{BOOL}} NOT NULL
);

-- Per-user UI settings as a key-value store; setting_key is a hardcoded enum.
CREATE TABLE user_settings (
    user_id       TEXT NOT NULL,
    setting_key   TEXT NOT NULL,
    setting_value TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, setting_key)
);

-- Reservation intents survive process restart; on boot any held row is orphaned
-- and rolled back. approval_id is the row's own UUID handle (used in tokens).
-- params is opaque transient JSON; lock is the SDK-serialized pretrade.Lock blob.
CREATE TABLE reservation_intents (
    approval_id TEXT PRIMARY KEY,
    order_id    INTEGER REFERENCES orders(id)   ON DELETE CASCADE,
    account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    params      TEXT    NOT NULL,
    lock        BLOB,
    issued_at   TEXT    NOT NULL,
    expires_at  TEXT    NOT NULL,
    state       TEXT    NOT NULL DEFAULT 'held'
);

CREATE INDEX idx_reservation_intents_state ON reservation_intents (state);
