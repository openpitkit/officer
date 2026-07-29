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
-- (surrogate-PK identity), {{XID}} (external-id column), {{BOOL}}
-- (boolean) and {{DECIMAL}} (exact-decimal value with numeric comparison)
-- before the DDL runs. Identity model: an internal integer surrogate
-- key (id) joins rows and never leaves the store; a non-guessable external_id
-- is the public handle of machine records; the same surrogate id is the integer
-- id the engine runs an account or group on, so there is no separate engine id
-- column. Dictionaries
-- are addressed by a public code with a mutable display title. Money, price
-- and quantity are exact-decimal {{DECIMAL}} values (never float); an index on a
-- {{DECIMAL}} column orders numerically, so range scans and sorts use the index.
-- Timestamps are RFC3339Nano UTC TEXT; binary is BLOB. The schema_migration
-- bookkeeping table is created by the Go migration harness, not here.

-- Single-row identity of the realm this schema holds: recorded once for backup
-- labelling and future placement. It is not a foreign key on any other table.
CREATE TABLE realm (
    id          {{PK}},
    external_id {{XID}} UNIQUE,
    code        TEXT    NOT NULL,
    title       TEXT    NOT NULL DEFAULT ''
);

-- Closed domain enums use compact, stable integer references on high-volume
-- records. Their public string codes are restored by the SQLite connector.
CREATE TABLE source_kind (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE order_event_type (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE order_side (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE order_amount_kind (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE order_status (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE adjustment_status (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE audit_action (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE attestation_alg (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE attestation_request_type (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

CREATE TABLE attestation_mode (
    id   {{PK}},
    code TEXT NOT NULL UNIQUE
);

-- Tradable asset dictionary. code is the immutable human handle, unique per
-- realm; title is the mutable display string; class_id is the optional foreign
-- key into asset_class, cleared (SET NULL) when the class is deleted.
CREATE TABLE asset (
    id       {{PK}},
    code     TEXT NOT NULL UNIQUE,
    title    TEXT NOT NULL DEFAULT '',
    class_id INTEGER REFERENCES asset_class(id) ON DELETE SET NULL
);

CREATE INDEX idx_assets_class_code ON asset (class_id, code);
CREATE INDEX idx_assets_title_code ON asset (title, code);

-- Asset-class dictionary. A managed classification list, mirroring the
-- account_group dictionary but without the block concept: an asset class carries
-- only a public code, a mutable display title, and free-form notes. Assets link
-- to it by the asset.class_id foreign key, so a class rename needs no cascade and
-- a class delete clears the link via ON DELETE SET NULL.
CREATE TABLE asset_class (
    id    {{PK}},
    code  TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_asset_classes_title ON asset_class (title, code);

-- Principals dictionary: actors that initiate control-plane actions.
CREATE TABLE principal (
    id    {{PK}},
    code  TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT ''
);

-- Account groups dictionary. The engine runs the group on its surrogate id, so
-- there is no separate engine id column.
CREATE TABLE account_group (
    id           {{PK}},
    code         TEXT    NOT NULL UNIQUE,
    title        TEXT    NOT NULL DEFAULT '',
    currency_asset_id INTEGER REFERENCES asset(id) ON DELETE RESTRICT,
    notes        TEXT    NOT NULL DEFAULT '',
    blocked      {{BOOL}} NOT NULL DEFAULT 0,
    block_reason TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_account_groups_blocked ON account_group (blocked, code);
CREATE INDEX idx_account_groups_title ON account_group (title, code);

-- Accounts dictionary. The engine runs the account on its surrogate id, so there
-- is no separate engine id column. group_id links to a group and is cleared (SET
-- NULL), not cascaded, when the group is deleted so an account survives its
-- group's removal.
CREATE TABLE account (
    id           {{PK}},
    code         TEXT    NOT NULL UNIQUE,
    title        TEXT    NOT NULL DEFAULT '',
    group_id     INTEGER REFERENCES account_group(id) ON DELETE SET NULL,
    currency_asset_id INTEGER REFERENCES asset(id) ON DELETE RESTRICT,
    pnl          {{DECIMAL}} NOT NULL DEFAULT '0',
    pnl_halt_reason TEXT    NOT NULL DEFAULT '',
    notes        TEXT    NOT NULL DEFAULT '',
    blocked      {{BOOL}} NOT NULL DEFAULT 0,
    block_reason TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_accounts_group ON account (group_id);
CREATE INDEX idx_accounts_blocked ON account (blocked, code);
CREATE INDEX idx_accounts_title ON account (title, code);

-- Per-(account, asset) holdings snapshot. Amounts are exact {{DECIMAL}} values,
-- never float; the indexes below order numerically. realized_pnl stores the
-- latest engine-reported absolute (a fresh row starts at '0'). Both references
-- cascade so deleting an account or asset removes its balance.
CREATE TABLE balance (
    account_id          INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    asset_id            INTEGER NOT NULL REFERENCES asset(id)   ON DELETE CASCADE,
    available           {{DECIMAL}} NOT NULL DEFAULT '0',
    held                {{DECIMAL}} NOT NULL DEFAULT '0',
    incoming            {{DECIMAL}} NOT NULL DEFAULT '0',
    average_entry_price {{DECIMAL}} NOT NULL DEFAULT '',
    realized_pnl        {{DECIMAL}} NOT NULL DEFAULT '0',
    realized_pnl_halt_reason TEXT NOT NULL DEFAULT '',
    updated_at          TEXT    NOT NULL,
    PRIMARY KEY (account_id, asset_id)
);

CREATE INDEX idx_balances_asset   ON balance (asset_id);
CREATE INDEX idx_balances_available ON balance (available, account_id, asset_id);
CREATE INDEX idx_balances_held ON balance (held, account_id, asset_id);
CREATE INDEX idx_balances_incoming ON balance (incoming, account_id, asset_id);
CREATE INDEX idx_balances_average_entry_price
    ON balance (average_entry_price, account_id, asset_id);
CREATE INDEX idx_balances_realized_pnl
    ON balance (realized_pnl, account_id, asset_id);
CREATE INDEX idx_balances_updated_at ON balance (updated_at DESC, account_id, asset_id);

-- Per-policy typed limit tables (replacing the former EAV limits table). Each
-- carries a hardcoded enum scope and nullable account/asset axes present only
-- for scopes that carry them; the natural composite (scope, account, asset) is
-- the addressable key, so there is no external_id.

-- Rate-limit barriers: at most max_orders new orders per rolling window.
CREATE TABLE limit_rate (
    id         {{PK}},
    scope      TEXT NOT NULL,
    account_id INTEGER REFERENCES account(id) ON DELETE CASCADE,
    asset_id   INTEGER REFERENCES asset(id)   ON DELETE CASCADE,
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
    account_id   INTEGER REFERENCES account(id) ON DELETE CASCADE,
    asset_id     INTEGER REFERENCES asset(id)   ON DELETE CASCADE,
    max_quantity TEXT,
    max_notional TEXT
);

CREATE INDEX idx_limit_order_size_account ON limit_order_size (account_id);
CREATE INDEX idx_limit_order_size_asset ON limit_order_size (asset_id);
CREATE UNIQUE INDEX uq_limit_order_size
    ON limit_order_size (scope, COALESCE(account_id, 0), COALESCE(asset_id, 0));

-- SpotFunds self-computed account-currency P&L-bounds barriers. This Officer
-- meta-policy maps to the SDK SpotFunds policy and cascades by
-- global/account-group/account. P&L is always in the account currency, so the
-- barrier has no asset or currency axis.
CREATE TABLE limit_spot_funds_pnl_bound (
    id               {{PK}},
    scope            TEXT NOT NULL,
    account_id       INTEGER REFERENCES account(id)       ON DELETE CASCADE,
    account_group_id INTEGER REFERENCES account_group(id) ON DELETE CASCADE,
    lower_bound      TEXT,
    upper_bound      TEXT
);

CREATE INDEX idx_limit_spot_funds_pnl_bounds_account
    ON limit_spot_funds_pnl_bound (account_id);
CREATE INDEX idx_limit_spot_funds_pnl_bounds_account_group
    ON limit_spot_funds_pnl_bound (account_group_id);
CREATE UNIQUE INDEX uq_limit_spot_funds_pnl_bounds
    ON limit_spot_funds_pnl_bound (
        scope,
        COALESCE(account_id, 0),
        COALESCE(account_group_id, 0)
    );

-- Append-only history of spot-funds adjustments. request/outcome are opaque
-- JSON. principal is cleared (SET NULL) when the principal is removed; account
-- and asset cascade.
CREATE TABLE adjustment (
    id           {{PK}},
    external_id  {{XID}} UNIQUE,
    account_id   INTEGER NOT NULL REFERENCES account(id)   ON DELETE CASCADE,
    asset_id     INTEGER NOT NULL REFERENCES asset(id)     ON DELETE CASCADE,
    principal_id INTEGER REFERENCES principal(id)          ON DELETE SET NULL,
    at           TEXT    NOT NULL,
    source_id    INTEGER NOT NULL REFERENCES source_kind(id),
    status_id    INTEGER NOT NULL REFERENCES adjustment_status(id),
    request      TEXT    NOT NULL,
    outcome      TEXT    NOT NULL
);

CREATE INDEX idx_adjustments_account ON adjustment (account_id, at DESC, id DESC);
CREATE INDEX idx_adjustments_asset ON adjustment (asset_id);
CREATE INDEX idx_adjustments_principal ON adjustment (principal_id);
CREATE INDEX idx_adjustments_source  ON adjustment (source_id, at DESC, id DESC);
CREATE INDEX idx_adjustments_status ON adjustment (status_id, at DESC, id DESC);

-- Orders recorded by Officer (including rejected ones). amount_value and price
-- are exact {{DECIMAL}} values; their indexes order numerically. leaves_quantity
-- stores request-provided remaining quantity and later follows only the LeavesQty
-- value accepted from an execution report.
-- lock is the SDK-serialized pretrade.Lock blob, persisted verbatim; the store
-- never decodes it. price is empty for market orders. Signed attestations, when
-- present, live per-event in the event_attestation companion, not inline here.
CREATE TABLE order_record (
    id              {{PK}},
    external_id     {{XID}} UNIQUE,
    account_id      INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    base_asset_id   INTEGER NOT NULL REFERENCES asset(id)   ON DELETE CASCADE,
    quote_asset_id  INTEGER NOT NULL REFERENCES asset(id)   ON DELETE CASCADE,
    principal_id    INTEGER REFERENCES principal(id)        ON DELETE SET NULL,
    at              TEXT    NOT NULL,
    source_id       INTEGER NOT NULL REFERENCES source_kind(id),
    side_id         INTEGER NOT NULL REFERENCES order_side(id),
    amount_kind_id  INTEGER NOT NULL REFERENCES order_amount_kind(id),
    amount_value    {{DECIMAL}} NOT NULL,
    leaves_quantity {{DECIMAL}} NOT NULL DEFAULT '',
    price           {{DECIMAL}} NOT NULL DEFAULT '',
    status_id       INTEGER NOT NULL REFERENCES order_status(id),
    drop_copy       INTEGER NOT NULL DEFAULT 0,
    lock            BLOB
);

CREATE INDEX idx_orders_account ON order_record (account_id, at DESC, id DESC);
CREATE INDEX idx_orders_principal ON order_record (principal_id);
CREATE INDEX idx_orders_base_asset ON order_record (base_asset_id);
CREATE INDEX idx_orders_quote_asset ON order_record (quote_asset_id);
CREATE INDEX idx_orders_source  ON order_record (source_id, at DESC, id DESC);
CREATE INDEX idx_orders_side ON order_record (side_id, at DESC, id DESC);
CREATE INDEX idx_orders_status ON order_record (status_id, at DESC, id DESC);
CREATE INDEX idx_orders_amount_value ON order_record (amount_value, id DESC);
CREATE INDEX idx_orders_leaves_quantity ON order_record (leaves_quantity, id DESC);
CREATE INDEX idx_orders_price ON order_record (price, id DESC);

-- Immutable event stream for an order. payload is opaque JSON. principal is
-- cleared (SET NULL) when removed; the parent order cascades.
CREATE TABLE order_event (
    id           {{PK}},
    external_id  {{XID}} UNIQUE,
    order_id     INTEGER NOT NULL REFERENCES order_record(id)  ON DELETE CASCADE,
    principal_id INTEGER REFERENCES principal(id)       ON DELETE SET NULL,
    at           TEXT    NOT NULL,
    type_id      INTEGER NOT NULL REFERENCES order_event_type(id),
    source_id    INTEGER NOT NULL REFERENCES source_kind(id),
    payload      TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_order_events_order ON order_event (order_id, at DESC, id DESC);
CREATE INDEX idx_order_events_principal ON order_event (principal_id);

-- Signed attestation over a trading request and its recorded result, 1:1 with
-- the order_event that recorded the request; absent when no attestation was
-- issued. Every attested trading request (submit, execution report, confirm,
-- cancel) produces one event, and its attestation binds here to that event.
-- signing_key_id references the signing key surrogate and is RESTRICT so a key
-- in use cannot be dropped. NULL means an unsigned (eSign-off) envelope.
CREATE TABLE event_attestation (
    event_id       INTEGER PRIMARY KEY REFERENCES order_event(id) ON DELETE CASCADE,
    token          TEXT    NOT NULL,
    signing_key_id INTEGER REFERENCES signing_key(id) ON DELETE RESTRICT,
    alg_id         INTEGER NOT NULL REFERENCES attestation_alg(id),
    request_type_id INTEGER NOT NULL REFERENCES attestation_request_type(id),
    mode_id        INTEGER NOT NULL REFERENCES attestation_mode(id),
    issued_at      TEXT    NOT NULL
);

CREATE INDEX idx_event_attestations_signing_key ON event_attestation (signing_key_id);

-- One inbound execution-report request. external_id is the caller's handle on
-- the request; the one or two order events it produced link through
-- execution_report_event. The event payload remains the report snapshot.
CREATE TABLE execution_report (
    id          {{PK}},
    external_id {{XID}} UNIQUE,
    order_id    INTEGER NOT NULL REFERENCES order_record(id) ON DELETE CASCADE,
    at          TEXT    NOT NULL
);

CREATE INDEX idx_execution_report_order
    ON execution_report (order_id, at DESC, id DESC);

-- Report -> produced events (fill and/or status change). An event belongs to at
-- most one execution report.
CREATE TABLE execution_report_event (
    report_id INTEGER NOT NULL REFERENCES execution_report(id) ON DELETE CASCADE,
    event_id  INTEGER NOT NULL REFERENCES order_event(id) ON DELETE CASCADE,
    PRIMARY KEY (report_id, event_id)
);

CREATE UNIQUE INDEX uq_execution_report_event
    ON execution_report_event (event_id);

-- Per-fill trade records ("reports"); one row per fill. lock_price is empty when
-- not applicable. commission_amount/commission_currency preserve the signed
-- per-fill fee or rebate in its own currency. order, account and asset cascade;
-- principal is cleared.
CREATE TABLE trade (
    id             {{PK}},
    external_id    {{XID}} UNIQUE,
    order_id       INTEGER NOT NULL REFERENCES order_record(id)   ON DELETE CASCADE,
    account_id     INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    base_asset_id  INTEGER NOT NULL REFERENCES asset(id)   ON DELETE CASCADE,
    quote_asset_id INTEGER NOT NULL REFERENCES asset(id)   ON DELETE CASCADE,
    principal_id   INTEGER REFERENCES principal(id)        ON DELETE SET NULL,
    at             TEXT    NOT NULL,
    source_id      INTEGER NOT NULL REFERENCES source_kind(id),
    side_id        INTEGER NOT NULL REFERENCES order_side(id),
    quantity       TEXT    NOT NULL,
    price          TEXT    NOT NULL,
    lock_price     TEXT    NOT NULL DEFAULT '',
    commission_amount   TEXT NOT NULL DEFAULT '',
    commission_currency TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_trades_account ON trade (account_id, at DESC, id DESC);
CREATE INDEX idx_trades_principal ON trade (principal_id);
CREATE INDEX idx_trades_base_asset ON trade (base_asset_id);
CREATE INDEX idx_trades_quote_asset ON trade (quote_asset_id);
CREATE INDEX idx_trades_source  ON trade (source_id, at DESC, id DESC);
CREATE INDEX idx_trades_order   ON trade (order_id);
CREATE INDEX idx_trades_side ON trade (side_id, at DESC, id DESC);
CREATE INDEX idx_trades_quantity ON trade (quantity COLLATE DECIMAL, id DESC);
CREATE INDEX idx_trades_price ON trade (price COLLATE DECIMAL, id DESC);
CREATE INDEX idx_trades_lock_price ON trade (lock_price COLLATE DECIMAL, id DESC);

-- Append-only audit trail. Account and actor codes/titles are immutable
-- snapshots, so compliance history survives dictionary deletes unchanged.
-- account_id is only a nullable identity link used for current-code filtering
-- after account renames; deleting the account clears the link, not the row.
CREATE TABLE audit (
    id            {{PK}},
    external_id   {{XID}} UNIQUE,
    account_id    INTEGER REFERENCES account(id) ON DELETE SET NULL,
    account_code  TEXT NOT NULL DEFAULT '',
    account_title TEXT NOT NULL DEFAULT '',
    asset_code    TEXT NOT NULL DEFAULT '',
    actor_code    TEXT NOT NULL DEFAULT '',
    actor_title   TEXT NOT NULL DEFAULT '',
    at            TEXT NOT NULL,
    action_id     INTEGER NOT NULL REFERENCES audit_action(id),
    source_id     INTEGER NOT NULL REFERENCES source_kind(id),
    detail        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_at ON audit (at DESC, id DESC);
CREATE INDEX idx_audit_account_id ON audit (account_id, at DESC, id DESC);
CREATE INDEX idx_audit_account_code ON audit (account_code, at DESC, id DESC);
CREATE INDEX idx_audit_asset_code ON audit (asset_code, at DESC, id DESC);
CREATE INDEX idx_audit_action ON audit (action_id, at DESC, id DESC);
CREATE INDEX idx_audit_source ON audit (source_id, at DESC, id DESC);
CREATE INDEX idx_audit_actor_code ON audit (actor_code, at DESC, id DESC);

-- Market-data connector instances. provider is a hardcoded enum; label is a
-- unique, case-insensitive operator-facing name; credentials is an opaque JSON
-- blob; enabled gates runtime participation.
CREATE TABLE market_data_instance (
    id          {{PK}},
    external_id {{XID}} UNIQUE,
    provider    TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '' COLLATE NOCASE UNIQUE,
    credentials TEXT NOT NULL DEFAULT '',
    enabled     {{BOOL}} NOT NULL DEFAULT 0
);

-- Per-instrument selection for an instance: the external source symbol mapped to
-- an engine instrument (base, quote). The instance reference cascades.
CREATE TABLE market_data_instrument (
    id              {{PK}},
    instance_id     INTEGER NOT NULL REFERENCES market_data_instance(id) ON DELETE CASCADE,
    external_symbol TEXT    NOT NULL,
    base_asset_id   INTEGER NOT NULL REFERENCES asset(id)  ON DELETE CASCADE,
    quote_asset_id  INTEGER NOT NULL REFERENCES asset(id)  ON DELETE CASCADE,
    enabled         {{BOOL}} NOT NULL DEFAULT 0,
    manual_price    TEXT    NOT NULL DEFAULT '',
    UNIQUE (instance_id, external_symbol)
);

CREATE INDEX idx_market_data_instruments_base_asset
    ON market_data_instrument (base_asset_id);
CREATE INDEX idx_market_data_instruments_quote_asset
    ON market_data_instrument (quote_asset_id);

-- Latest normalized quote per configured instrument, 1:1 with the instrument and
-- overwritten on each update (an operational snapshot, not a tick history).
CREATE TABLE market_data_quote (
    instrument_id INTEGER PRIMARY KEY REFERENCES market_data_instrument(id) ON DELETE CASCADE,
    mark          TEXT NOT NULL DEFAULT '',
    bid           TEXT NOT NULL DEFAULT '',
    ask           TEXT NOT NULL DEFAULT '',
    as_of         TEXT NOT NULL,
    received_at   TEXT NOT NULL
);

-- Ed25519 signing keypairs. key_id is the key's own UUID handle. private_key is
-- a plaintext BLOB (at-rest encryption deferred). active=1 marks the signing key.
CREATE TABLE signing_key (
    id          {{PK}},
    key_id      TEXT    NOT NULL UNIQUE,
    alg         TEXT    NOT NULL,
    private_key BLOB    NOT NULL,
    public_key  BLOB    NOT NULL,
    created_at  TEXT    NOT NULL,
    active      {{BOOL}} NOT NULL DEFAULT 0
);

CREATE INDEX idx_signing_keys_active ON signing_key (active);

-- Global signing configuration; key is a hardcoded enum, not a dictionary.
CREATE TABLE signing_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Intentional default: fresh installs ship with eSign OFF ('1'). The operator
-- opts in to signing; this is not a leftover.
INSERT OR IGNORE INTO signing_config (key, value) VALUES ('no_esign', '1');

-- Per-command MCP access overrides; command is the key, enabled is the toggle.
CREATE TABLE mcp_access (
    command TEXT PRIMARY KEY,
    enabled {{BOOL}} NOT NULL
);

-- Per-user UI settings as a key-value store; setting_key is a hardcoded enum.
CREATE TABLE user_setting (
    user_id       TEXT NOT NULL,
    setting_key   TEXT NOT NULL,
    setting_value TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, setting_key)
);
