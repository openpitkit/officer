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
// Please see https://openpit.dev and the OWNERS file for details.

// Business CSV import group of the SQLite store. ApplyBusinessCSVImport
// persists all selected groups, account and balance — plus the adjustment
// records that applying a position snapshot produced and their audit rows — in
// one transaction. Groups and account are addressed by code; the store
// resolves codes to surrogate ids and assigns engine ids on create exactly as
// the normal create paths do. Balances resolve account and asset codes to
// surrogate ids. Adjustments resolve account, asset and the optional principal
// by code and receive a freshly generated external id. Audit rows resolve the
// optional account reference by code. An unknown code on any row yields
// domain.ErrInvalid at row granularity with a message that names the offending
// code.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// ApplyBusinessCSVImport persists all selected business CSV rows and their
// per-row audit records in one transaction. Any store error rolls the whole
// import back, including audit rows.
//
// Groups and account that do not yet exist are created (engine ids assigned
// by the connector). Groups and account that already exist are updated in
// place (engine id preserved). Balances are upserted. Each adjustment the node
// produced while applying a position snapshot is appended (the durable trace of
// the imported position). Audit rows are appended. An unknown group or asset
// code on any row yields domain.ErrInvalid.
func (r *realmStore) ApplyBusinessCSVImport(
	ctx context.Context, in fwstore.BusinessCSVImport,
) error {
	err := r.applyBusinessCSVImport(ctx, in)
	if err != nil && !isExpectedDomainError(err) {
		// Begin/commit/exec failures mean the store is no longer trustworthy;
		// fire the process-level fatal hook. Domain rejections (bad codes etc.)
		// stay regular errors the caller maps to a 4xx.
		r.store.fatal(err)
	}
	return err
}

func (r *realmStore) applyBusinessCSVImport(
	ctx context.Context, in fwstore.BusinessCSVImport,
) error {
	tx, err := r.db().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin business CSV import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := importGroups(ctx, tx, in.Groups); err != nil {
		return err
	}
	if err := importAccounts(ctx, tx, in.Accounts); err != nil {
		return err
	}
	if err := importBalances(ctx, tx, in.Balances); err != nil {
		return err
	}
	if err := importAdjustments(ctx, tx, in.Adjustments); err != nil {
		return err
	}
	if err := importAudit(ctx, tx, in.Audits); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit business CSV import: %w", err)
	}
	return nil
}

// importGroups creates or updates account groups inside the import transaction.
// New groups run on their surrogate id (the engine group id). Existing groups
// have their mutable fields (title, notes, blocked, block_reason) updated.
func importGroups(
	ctx context.Context, tx *sql.Tx, rows []fwstore.BusinessCSVImportGroup,
) error {
	for _, item := range rows {
		g := item.Group
		if !item.Exists {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO account_group
				 (code, title, notes, blocked, block_reason)
				 VALUES (?, ?, ?, ?, ?)`,
				g.Code, g.Title, g.Notes, g.Blocked, g.BlockReason,
			); err != nil {
				return fmt.Errorf("store: import group %q: %w", g.Code, err)
			}
		} else {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE account_group
				 SET title = ?, notes = ?, blocked = ?, block_reason = ?
				 WHERE code = ?`,
				g.Title, g.Notes, g.Blocked, g.BlockReason, g.Code,
			); err != nil {
				return fmt.Errorf("store: update group %q: %w", g.Code, err)
			}
		}
	}
	return nil
}

// importAccounts creates or updates account inside the import transaction.
// The optional group link is resolved by group code; an unknown group code
// yields domain.ErrInvalid. New account run on their surrogate id (the engine
// account id). Existing accounts have their mutable fields updated.
func importAccounts(
	ctx context.Context, tx *sql.Tx, rows []fwstore.BusinessCSVImportAccount,
) error {
	for _, item := range rows {
		a := item.Account
		groupID, err := optionalGroupID(ctx, tx, a.GroupCode)
		if err != nil {
			return fmt.Errorf(
				"store: import account %q: group code %q: %w",
				a.Code, a.GroupCode, err,
			)
		}
		if !item.Exists {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO account
				 (code, title, group_id, notes, blocked, block_reason)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				a.Code.String(), a.Title,
				groupID, a.Notes, a.Blocked, a.BlockReason,
			); err != nil {
				return fmt.Errorf("store: import account %q: %w", a.Code, err)
			}
		} else {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE account
				 SET title = ?, group_id = ?, notes = ?, blocked = ?, block_reason = ?
				 WHERE code = ?`,
				a.Title, groupID, a.Notes, a.Blocked, a.BlockReason, a.Code.String(),
			); err != nil {
				return fmt.Errorf("store: update account %q: %w", a.Code, err)
			}
		}
	}
	return nil
}

// importBalances upserts balance snapshots inside the import transaction.
// Account and asset codes are resolved to surrogate ids; an unknown code
// yields domain.ErrInvalid.
func importBalances(
	ctx context.Context, tx *sql.Tx, balances []domain.Balance,
) error {
	for _, b := range balances {
		accountID, err := resolveAccountID(ctx, tx, b.Account)
		if err != nil {
			return fmt.Errorf(
				"store: import balance account %q: %w", b.Account, err,
			)
		}
		assetID, err := resolveAssetID(ctx, tx, b.Asset)
		if err != nil {
			return fmt.Errorf(
				"store: import balance asset %q for account %q: %w",
				b.Asset, b.Account, err,
			)
		}
		updatedAt := nowStr()
		if !b.UpdatedAt.IsZero() {
			updatedAt = b.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		available := settleOrZero(b.Available)
		held := settleOrZero(b.Held)
		incoming := settleOrZero(b.Incoming)
		realizedPnl := settleOrZero(b.RealizedPnl)
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO balance
			 (account_id, asset_id, available, held,
			  incoming, realized_pnl, average_entry_price, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(account_id, asset_id) DO UPDATE SET
			  available = excluded.available,
			  held = excluded.held,
			  incoming = excluded.incoming,
			  realized_pnl = excluded.realized_pnl,
			  average_entry_price = excluded.average_entry_price,
			  updated_at = excluded.updated_at`,
			accountID, assetID,
			available, held, incoming, realizedPnl,
			b.AverageEntryPrice, updatedAt,
		); err != nil {
			return fmt.Errorf(
				"store: import balance %s/%s: %w", b.Account, b.Asset, err,
			)
		}
	}
	return nil
}

// importAdjustments appends the spot-funds adjustment records that the node
// produced while applying the imported position snapshots, inside the import
// transaction. Account and asset codes are resolved to surrogate ids; the
// optional principal is resolved by code (empty = NULL). Each record receives a
// freshly generated external id and the timestamp it carries (or now when
// unset); the typed request and outcome are marshalled to the opaque JSON
// columns exactly as AppendAdjustment does. An unknown code yields
// domain.ErrInvalid.
func importAdjustments(
	ctx context.Context, tx *sql.Tx, adjustments []domain.AccountAdjustmentRecord,
) error {
	for _, rec := range adjustments {
		accountID, err := resolveAccountID(ctx, tx, rec.Account)
		if err != nil {
			return fmt.Errorf(
				"store: import adjustment account %q: %w", rec.Account, err,
			)
		}
		assetID, err := resolveAssetID(ctx, tx, rec.Asset)
		if err != nil {
			return fmt.Errorf(
				"store: import adjustment asset %q for account %q: %w",
				rec.Asset, rec.Account, err,
			)
		}
		principalID, err := resolveOptionalPrincipalID(ctx, tx, rec.Principal)
		if err != nil {
			return fmt.Errorf(
				"store: import adjustment principal %q: %w", rec.Principal, err,
			)
		}
		reqJSON, err := json.Marshal(rec.Request)
		if err != nil {
			return fmt.Errorf("store: import adjustment request: %w", err)
		}
		outcomeJSON, err := marshalAdjustmentOutcome(rec)
		if err != nil {
			return err
		}
		xid, err := newExternalID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO adjustment
			 (external_id, account_id, asset_id, principal_id,
			  at, source, status, request, outcome)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			xid.Bytes(), accountID, assetID, principalID,
			atOrNow(rec.At), string(rec.Source), string(adjustmentStatus(rec)),
			string(reqJSON), string(outcomeJSON),
		); err != nil {
			return fmt.Errorf(
				"store: import adjustment %s/%s: %w", rec.Account, rec.Asset, err,
			)
		}
	}
	return nil
}

// importAudit appends the snapshot audit rows inside the import transaction.
// Account and actor titles are taken from the entry when present, else looked up
// by code; an empty account code inserts NULL.
func importAudit(
	ctx context.Context, tx *sql.Tx, audits []fwstore.AuditEntry,
) error {
	for _, entry := range audits {
		xid, err := newExternalID()
		if err != nil {
			return err
		}
		accountTitle := entry.AccountTitle
		if accountTitle == "" && entry.Account != "" {
			accountTitle = lookupTitle(ctx, tx, "account", entry.Account.String())
		}
		actorTitle := entry.ActorTitle
		if actorTitle == "" && entry.Actor != "" {
			actorTitle = lookupTitle(ctx, tx, "principal", entry.Actor)
		}
		accountID, err := lookupID(ctx, tx, "account", entry.Account.String())
		if err != nil {
			return err
		}
		source := entry.Source
		if source == "" {
			source = domain.SourceSystem
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO audit
			 (external_id, account_id, account_code, account_title, actor_code,
			  actor_title, at, action, source, detail)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			xid.Bytes(), accountID, entry.Account.String(), accountTitle,
			entry.Actor, actorTitle, nowStr(), string(entry.Action),
			string(source), entry.Detail,
		); err != nil {
			return fmt.Errorf("store: import audit row: %w", err)
		}
	}
	return nil
}
