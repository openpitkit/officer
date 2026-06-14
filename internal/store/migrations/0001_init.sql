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

-- Initial control-plane schema, created from scratch in one migration:
-- accounts, account groups, risk-limit barriers, spot-funds balances,
-- account-adjustment history, the trading ledger (orders, order events,
-- trades), and the append-only audit trail. The schema_migrations bookkeeping
-- table (version + applied_at) is created by the Go migration harness, not here.

CREATE TABLE accounts (
    tenant       TEXT    NOT NULL,
    id           TEXT    NOT NULL,
    blocked      INTEGER NOT NULL DEFAULT 0,
    block_reason TEXT    NOT NULL DEFAULT '',
    group_id     TEXT    NOT NULL DEFAULT '',
    notes        TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (tenant, id)
);

CREATE INDEX idx_accounts_group ON accounts (tenant, group_id);

-- Account groups: operator-facing named groupings. Membership is expressed by
-- accounts.group_id = account_groups.id.
CREATE TABLE account_groups (
    tenant       TEXT    NOT NULL,
    id           TEXT    NOT NULL,
    notes        TEXT    NOT NULL DEFAULT '',
    blocked      INTEGER NOT NULL DEFAULT 0,
    block_reason TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (tenant, id)
);

-- Risk-limit barriers, addressed by (policy, scope, account, asset, kind).
CREATE TABLE limits (
    tenant  TEXT NOT NULL,
    policy  TEXT NOT NULL,
    scope   TEXT NOT NULL,
    account TEXT NOT NULL DEFAULT '',
    asset   TEXT NOT NULL DEFAULT '',
    kind    TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (tenant, policy, scope, account, asset, kind)
);

CREATE INDEX idx_limits_account ON limits (tenant, account);

-- Per-(account, asset) holdings snapshot. Amounts are exact TEXT decimals,
-- never REAL. updated_at is RFC3339Nano UTC (TEXT, same as audit.at).
-- Seam: a future margin "positions" table will live beside this one; the
-- schema is kept spot-only intentionally.
-- realized_pnl is delta-accumulated: each operation's realized-PnL delta is
-- added to the stored value, never overwritten with the engine's reported
-- absolute. A fresh row starts at '0', so accumulated deltas equal the
-- cumulative.
CREATE TABLE balances (
    tenant               TEXT NOT NULL,
    account              TEXT NOT NULL,
    asset                TEXT NOT NULL,
    available            TEXT NOT NULL DEFAULT '0',
    held                 TEXT NOT NULL DEFAULT '0',
    incoming             TEXT NOT NULL DEFAULT '0',
    average_entry_price  TEXT NOT NULL DEFAULT '',
    realized_pnl         TEXT NOT NULL DEFAULT '0',
    updated_at           TEXT NOT NULL,
    PRIMARY KEY (tenant, account, asset)
);

CREATE INDEX idx_balances_account ON balances (tenant, account);
CREATE INDEX idx_balances_asset   ON balances (tenant, asset);

-- Append-only history of spot-funds adjustments. request/outcome are JSON;
-- asset and status are indexed columns for recent-with-limit / by-account
-- queries. JSON is intentional: the payload is variant-heavy and does not need
-- the relational decomposition applied to limits.
CREATE TABLE adjustments (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant    TEXT    NOT NULL,
    account   TEXT    NOT NULL,
    at        TEXT    NOT NULL,
    source    TEXT    NOT NULL,
    principal TEXT    NOT NULL DEFAULT '',
    asset     TEXT    NOT NULL,
    status    TEXT    NOT NULL,
    request   TEXT    NOT NULL,
    outcome   TEXT    NOT NULL
);

CREATE INDEX idx_adjustments_account ON adjustments (tenant, account, at DESC, id DESC);
CREATE INDEX idx_adjustments_source  ON adjustments (tenant, source,  at DESC, id DESC);

-- Orders recorded by Officer (including rejected ones). lock_prices is a JSON
-- array of exact-decimal strings; price is empty for market orders.
CREATE TABLE orders (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant       TEXT    NOT NULL,
    account      TEXT    NOT NULL,
    at           TEXT    NOT NULL,
    source       TEXT    NOT NULL,
    principal    TEXT    NOT NULL DEFAULT '',
    base_asset   TEXT    NOT NULL,
    quote_asset  TEXT    NOT NULL,
    side         TEXT    NOT NULL,
    amount_kind  TEXT    NOT NULL,
    amount_value TEXT    NOT NULL,
    price        TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL,
    lock_prices  TEXT    NOT NULL DEFAULT '[]'
);

CREATE INDEX idx_orders_account ON orders (tenant, account, at DESC, id DESC);
CREATE INDEX idx_orders_source  ON orders (tenant, source,  at DESC, id DESC);

-- Immutable event stream for an order. payload is a JSON OrderEventPayload.
CREATE TABLE order_events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id  INTEGER NOT NULL REFERENCES orders(id),
    at        TEXT    NOT NULL,
    type      TEXT    NOT NULL,
    source    TEXT    NOT NULL,
    principal TEXT    NOT NULL DEFAULT '',
    payload   TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_order_events_order ON order_events (order_id, at DESC, id DESC);

-- Per-fill trade records; one row per fill event. lock_price is empty when not
-- applicable.
CREATE TABLE trades (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id    INTEGER NOT NULL REFERENCES orders(id),
    tenant      TEXT    NOT NULL,
    account     TEXT    NOT NULL,
    at          TEXT    NOT NULL,
    source      TEXT    NOT NULL,
    principal   TEXT    NOT NULL DEFAULT '',
    base_asset  TEXT    NOT NULL,
    quote_asset TEXT    NOT NULL,
    side        TEXT    NOT NULL,
    quantity    TEXT    NOT NULL,
    price       TEXT    NOT NULL,
    lock_price  TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_trades_account ON trades (tenant, account, at DESC, id DESC);
CREATE INDEX idx_trades_source  ON trades (tenant, source,  at DESC, id DESC);
CREATE INDEX idx_trades_order   ON trades (order_id);

-- Append-only audit trail of control-plane actions.
CREATE TABLE audit (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      TEXT    NOT NULL,
    actor   TEXT    NOT NULL,
    action  TEXT    NOT NULL,
    tenant  TEXT    NOT NULL DEFAULT '',
    account TEXT    NOT NULL DEFAULT '',
    detail  TEXT    NOT NULL DEFAULT '',
    source  TEXT    NOT NULL DEFAULT 'system'
);

CREATE INDEX idx_audit_at ON audit (at DESC, id DESC);

-- Market-data connector configuration. One row per configured
-- source instance; per-instance instrument selection lives in
-- market_data_instruments. `credentials` is an opaque provider-specific JSON
-- blob, unencrypted for now (at-rest encryption is planned); BYO/mock leave
-- it empty. `label` is a unique operator-facing source name, case-insensitive.
-- `enabled` is a 0/1 flag gating runtime participation.
CREATE TABLE market_data_instances (
    id          TEXT    PRIMARY KEY,
    type        TEXT    NOT NULL,
    label       TEXT    NOT NULL DEFAULT '' COLLATE NOCASE UNIQUE,
    credentials TEXT    NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 0
);

-- Per-instrument selection for an instance: the external source symbol mapped
-- to an engine instrument (base, quote). Per-instrument enable/disable lives
-- here. The instance reference cascades on delete so removing an instance drops
-- its instruments.
CREATE TABLE market_data_instruments (
    instance_id     TEXT    NOT NULL REFERENCES market_data_instances(id) ON DELETE CASCADE,
    external_symbol TEXT    NOT NULL,
    base_asset      TEXT    NOT NULL,
    quote_asset     TEXT    NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (instance_id, external_symbol)
);

-- Latest normalized quote per configured instrument. This is an operational
-- snapshot, not a tick history: the connector manager overwrites a row on every
-- update so the panel and dashboard can show current mark/bid/ask + as-of.
CREATE TABLE market_data_quotes (
    instance_id     TEXT    NOT NULL,
    external_symbol TEXT    NOT NULL,
    base_asset      TEXT    NOT NULL,
    quote_asset     TEXT    NOT NULL,
    mark            TEXT    NOT NULL DEFAULT '',
    bid             TEXT    NOT NULL DEFAULT '',
    ask             TEXT    NOT NULL DEFAULT '',
    as_of           TEXT    NOT NULL,
    received_at     TEXT    NOT NULL,
    PRIMARY KEY (instance_id, external_symbol),
    FOREIGN KEY (instance_id, external_symbol)
        REFERENCES market_data_instruments(instance_id, external_symbol)
        ON DELETE CASCADE
);

-- Per-command MCP access control. One row per command the operator has
-- explicitly toggled away from its catalogue default; commands without a row
-- resolve to their default in the catalogue. `enabled` is a 0/1 flag.
CREATE TABLE mcp_access (
    command TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL
);
