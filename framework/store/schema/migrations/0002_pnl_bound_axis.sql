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

-- SpotFunds P&L-bounds barriers gain the P&L axis they bound: realized,
-- unrealized at current prices, or their total. Each axis is an Officer policy
-- of its own with its own global/account-group/account cascade, so the axis
-- joins the composite key. The table is rebuilt rather than altered so that
-- axis carries no column default: every row written after this migration
-- states its axis. Every barrier a 0.1 database holds bounds realized P&L, the
-- only axis 0.1 had. The 0.1 table is renamed aside first, so the rebuilt one
-- is created under its final name; dropping the old table drops its indexes,
-- which are then recreated with the axis in the unique key.
ALTER TABLE limit_spot_funds_pnl_bound
    RENAME TO limit_spot_funds_pnl_bound_v1;

CREATE TABLE limit_spot_funds_pnl_bound (
    id                {{PK}},
    axis              TEXT NOT NULL
                      CHECK (axis IN ('realized', 'unrealized', 'total')),
    scope             TEXT NOT NULL,
    account_id        INTEGER REFERENCES account(id)       ON DELETE CASCADE,
    account_group_id  INTEGER REFERENCES account_group(id) ON DELETE CASCADE,
    currency_asset_id INTEGER NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
    lower_bound       TEXT,
    upper_bound       TEXT
);

INSERT INTO limit_spot_funds_pnl_bound
    (id, axis, scope, account_id, account_group_id, currency_asset_id,
     lower_bound, upper_bound)
SELECT id, 'realized', scope, account_id, account_group_id, currency_asset_id,
       lower_bound, upper_bound
FROM limit_spot_funds_pnl_bound_v1;

DROP TABLE limit_spot_funds_pnl_bound_v1;

CREATE INDEX idx_limit_spot_funds_pnl_bounds_account
    ON limit_spot_funds_pnl_bound (account_id);
CREATE INDEX idx_limit_spot_funds_pnl_bounds_account_group
    ON limit_spot_funds_pnl_bound (account_group_id);
CREATE INDEX idx_limit_spot_funds_pnl_bounds_currency_asset
    ON limit_spot_funds_pnl_bound (currency_asset_id);
CREATE UNIQUE INDEX uq_limit_spot_funds_pnl_bounds
    ON limit_spot_funds_pnl_bound (
        axis,
        scope,
        COALESCE(account_id, 0),
        COALESCE(account_group_id, 0)
    );
