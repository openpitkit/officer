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

package node

import (
	"context"
	"errors"
	"fmt"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// --- spot funds -------------------------------------------------------------

// ApplyBusinessCSVImport persists a prepared business CSV import atomically in
// the store and mirrors the selected rows into the live engine. The engine
// adjustments, group moves, and blocks are applied first; the selected rows are
// then persisted in one store transaction. That transaction is all-or-nothing,
// so a failed import writes nothing. Because the engine effects already ran when
// it fails, the engine is reconciled from the persisted store state so the
// engine and store never diverge. Any account the engine kill-switched while
// applying a position snapshot is mirrored once the transaction commits.
func (n *localNode) ApplyBusinessCSVImport(
	ctx context.Context,
	in store.BusinessCSVImport,
	caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	rollback, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		return fmt.Errorf("capture business CSV rollback backup: %w", err)
	}

	seenBalances := make(map[string]struct{}, len(in.Balances))
	for _, balance := range in.Balances {
		balanceKey := businessCSVImportBalanceKey(balance)
		if _, ok := seenBalances[balanceKey]; ok {
			return fmt.Errorf("duplicate position snapshot %s/%s: %w",
				balance.Account, balance.Asset, domain.ErrInvalid)
		}
		seenBalances[balanceKey] = struct{}{}
	}

	ensuredGroups := make(map[string]bool)
	pendingAccounts := make(map[string]domain.Account)
	type accountGroupMove struct {
		account domain.AccountID
		old     string
		next    string
	}
	groupMoves := make([]accountGroupMove, 0)
	for _, row := range in.Groups {
		if row.Exists {
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action: domain.AuditActionSetGroupNotes,
				Detail: fmt.Sprintf("set notes group %s", row.Group.Code),
			}))
		} else {
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action: domain.AuditActionCreateGroup,
				Detail: fmt.Sprintf("create group %s", row.Group.Code),
			}))
		}
		action := domain.AuditActionBlockGroup
		detail := fmt.Sprintf("block group %s", row.Group.Code)
		if !row.Group.Blocked {
			action = domain.AuditActionUnblockGroup
			detail = fmt.Sprintf("unblock group %s", row.Group.Code)
		}
		in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
			Action: action,
			Detail: detail,
		}))
	}

	for _, row := range in.Accounts {
		accountKey := businessCSVImportAccountKey(row.Account)
		prev := domain.Account{Code: row.Account.Code}
		if row.Exists {
			if pending, ok := pendingAccounts[accountKey]; ok {
				prev = pending
			} else {
				var ok bool
				var err error
				prev, ok, err = n.realm.GetAccount(ctx, row.Account.Code)
				if err != nil {
					return fmt.Errorf("read account for business CSV import: %w", err)
				}
				if !ok {
					return fmt.Errorf("account %q: %w", row.Account.Code, domain.ErrNotFound)
				}
			}
		}
		if row.Account.GroupCode != "" && !ensuredGroups[row.Account.GroupCode] {
			_, ok, err := n.realm.GetGroup(ctx, row.Account.GroupCode)
			if err != nil {
				return fmt.Errorf("read group for business CSV import: %w", err)
			}
			if !ok {
				in.Groups = append(in.Groups, store.BusinessCSVImportGroup{
					Group: domain.AccountGroup{Code: row.Account.GroupCode},
				})
			}
			ensuredGroups[row.Account.GroupCode] = true
		}
		if prev.GroupCode != row.Account.GroupCode {
			groupMoves = append(groupMoves, accountGroupMove{
				account: row.Account.Code,
				old:     prev.GroupCode,
				next:    row.Account.GroupCode,
			})
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action:  domain.AuditActionSetGroup,
				Account: row.Account.Code,
				Detail:  setAccountGroupDetail(row.Account.Code, row.Account.GroupCode),
			}))
		}
		// Account blocks for CSV-created accounts run after the import rebuilds
		// the resolver; see the engine-effects phase below.
		if !row.Exists {
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action:  domain.AuditActionCreateAccount,
				Account: row.Account.Code,
				Detail:  fmt.Sprintf("create account %s", row.Account.Code),
			}))
		}
		in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
			Action:  domain.AuditActionSetNotes,
			Account: row.Account.Code,
			Detail:  fmt.Sprintf("set notes account %s", row.Account.Code),
		}))
		action := domain.AuditActionBlock
		detail := fmt.Sprintf("block account %s", row.Account.Code)
		if !row.Account.Blocked {
			action = domain.AuditActionUnblock
			detail = fmt.Sprintf("unblock account %s", row.Account.Code)
		}
		in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
			Action:  action,
			Account: row.Account.Code,
			Detail:  detail,
		}))
		pendingAccounts[accountKey] = row.Account
	}

	preStore := store.BusinessCSVImport{
		Groups:   append([]store.BusinessCSVImportGroup(nil), in.Groups...),
		Accounts: append([]store.BusinessCSVImportAccount(nil), in.Accounts...),
	}
	sdkSeedBlocks := make(map[domain.AccountID]struct{})
	if len(preStore.Groups) > 0 || len(preStore.Accounts) > 0 {
		if err := n.realm.ApplyBusinessCSVImport(ctx, preStore); err != nil {
			return n.rollbackStore(ctx, rollback, fmt.Errorf("apply business CSV dictionaries: %w", err))
		}
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("rebuild engine after business CSV dictionaries: %w", err))
		}
		for i := range in.Groups {
			in.Groups[i].Exists = true
		}
		for i := range in.Accounts {
			in.Accounts[i].Exists = true
		}
		sdkSeedBlocks, err = n.preserveSDKSeedAccountBlocks(ctx, &in)
		if err != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("preserve SDK seed account blocks: %w", err))
		}
	}

	for _, row := range in.Groups {
		applyErr := n.engine.RunGroupSynchronized(ctx, row.Group.Code,
			func(lane engine.GroupLane) error {
				return n.applyGroupBlock(ctx, lane, row.Group.Code,
					row.Group.Blocked, row.Group.BlockReason)
			})
		if applyErr != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("apply group block: %w", applyErr))
		}
	}
	for _, row := range in.Accounts {
		if _, preserve := sdkSeedBlocks[row.Account.Code]; preserve {
			continue
		}
		applyErr := n.engine.RunAccountSynchronized(ctx, row.Account.Code,
			func(lane engine.AccountLane) error {
				return n.applyBlock(ctx, lane, row.Account.Code,
					row.Account.Blocked, row.Account.BlockReason)
			})
		if applyErr != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("apply account block: %w", applyErr))
		}
	}
	for _, move := range groupMoves {
		if err := n.applyGroupMove(ctx, n.engine, move.account, move.old, move.next); err != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("apply account group: %w", err))
		}
	}

	// Blocks the engine latched while committing the position-snapshot batches.
	// The mirror is deferred until the import transaction commits: that write
	// carries every account row's CSV blocked flag and would otherwise clear a
	// block the engine just latched. No lane can read the gap - the import holds
	// the engine-restart gate throughout.
	var adjustmentBlocks []domain.AccountBlock
	balanceGroups := make(map[domain.AccountID][]domain.Balance)
	balanceAccounts := make([]domain.AccountID, 0)
	for _, balance := range in.Balances {
		if _, ok := balanceGroups[balance.Account]; !ok {
			balanceAccounts = append(balanceAccounts, balance.Account)
		}
		balanceGroups[balance.Account] = append(balanceGroups[balance.Account], balance)
	}
	for _, account := range balanceAccounts {
		balances := balanceGroups[account]
		reqs := make([]domain.AdjustmentRequest, 0, len(balances))
		for _, balance := range balances {
			reqs = append(reqs, snapshotAdjustmentRequest(balance))
		}
		var results []engine.AdjustmentResult
		var batchReject *engine.AdjustmentBatchReject
		if err := n.engine.RunAccountSynchronized(ctx, account, func(lane engine.AccountLane) error {
			var err error
			results, batchReject, err = lane.ApplyAccountAdjustmentBatch(ctx, account, reqs)
			return err
		}); err != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("apply position snapshot adjustment: %w", err))
		}
		if batchReject != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("position snapshot adjustment batch for account %s rejected: %s: %w",
					account, batchReject.Reason, domain.ErrInvalid))
		}
		if len(results) == 0 {
			continue
		}
		// Batch-level, so every result of this batch repeats them; the sink
		// collapses the repeats by account.
		adjustmentBlocks = append(adjustmentBlocks, results[0].AccountBlocks...)
		for i, result := range results {
			if i >= len(balances) {
				return n.rollbackStoreAndEngine(ctx, rollback,
					fmt.Errorf("position snapshot adjustment batch for account %s returned extra outcome %d for %d requests: %w",
						account, len(results), len(balances), domain.ErrInvalid))
			}
			balance := balances[i]
			req := reqs[i]
			rec := domain.AccountAdjustmentRecord{
				Account:   balance.Account,
				Source:    caller.Source,
				Principal: caller.Principal,
				Request:   req,
				Accepted:  result.Accepted,
				Rejected:  result.Rejected,
				Asset:     balance.Asset,
			}
			if result.Rejected != nil {
				return n.rollbackStoreAndEngine(ctx, rollback,
					fmt.Errorf("position %s/%s snapshot adjustment rejected: %s: %w",
						balance.Account, balance.Asset, result.Rejected.Reason, domain.ErrInvalid))
			}
			if adjustmentResultNoChange(result) {
				continue
			}
			in.Adjustments = append(in.Adjustments, rec)
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action:  domain.AuditActionAdjustment,
				Account: balance.Account,
				Detail:  importPositionSnapshotDetail(balance, result.Accepted != nil),
			}))
		}
	}

	if err := n.realm.ApplyBusinessCSVImport(ctx, in); err != nil {
		return n.rollbackStoreAndEngine(ctx, rollback,
			fmt.Errorf("apply business CSV import: %w", err))
	}
	// The import is committed, so a mirror failure has no store state left to
	// roll back to; the sink's fatal contract owns the outcome from here.
	return n.mirrorAdjustmentAccountBlocks(ctx, adjustmentBlocks)
}

func businessCSVImportAccountKey(account domain.Account) string {
	return string(account.Code)
}

func businessCSVImportBalanceKey(balance domain.Balance) string {
	return string(balance.Account) + "\x00" + balance.Asset
}

func (n *localNode) preserveSDKSeedAccountBlocks(
	ctx context.Context, in *store.BusinessCSVImport,
) (map[domain.AccountID]struct{}, error) {
	source, ok := n.engine.(seedAccountBlockSource)
	if !ok {
		return map[domain.AccountID]struct{}{}, nil
	}
	blocked := make(map[domain.AccountID]struct{})
	for _, block := range source.SeedAccountBlocks() {
		blocked[block.Account] = struct{}{}
	}
	if len(blocked) == 0 {
		return blocked, nil
	}

	winners := make(map[domain.AccountID]struct{})
	for i := range in.Accounts {
		row := &in.Accounts[i]
		if row.Account.Blocked {
			continue
		}
		if _, ok := blocked[row.Account.Code]; !ok {
			continue
		}
		stored, ok, err := n.realm.GetAccount(ctx, row.Account.Code)
		if err != nil {
			return nil, fmt.Errorf("read SDK-blocked account %q: %w", row.Account.Code, err)
		}
		if !ok || !stored.Blocked {
			return nil, fmt.Errorf(
				"SDK seed block for account %q was not mirrored: %w",
				row.Account.Code,
				domain.ErrInvalid,
			)
		}
		row.Account.Blocked = true
		row.Account.BlockReason = stored.BlockReason
		winners[row.Account.Code] = struct{}{}
	}
	if len(winners) == 0 {
		return winners, nil
	}
	audits := in.Audits[:0]
	for _, audit := range in.Audits {
		_, preserve := winners[audit.Account]
		if preserve && audit.Action == domain.AuditActionUnblock {
			continue
		}
		audits = append(audits, audit)
	}
	in.Audits = audits
	return winners, nil
}

func (n *localNode) auditEntry(
	caller domain.Caller, entry store.AuditEntry,
) store.AuditEntry {
	entry.Actor = caller.Principal
	entry.Source = caller.Source
	return entry
}

func snapshotAdjustmentRequest(snapshot domain.Balance) domain.AdjustmentRequest {
	req := domain.AdjustmentRequest{
		Asset:                 snapshot.Asset,
		AverageEntryPrice:     snapshot.AverageEntryPrice,
		RealizedPnlHaltReason: snapshot.RealizedPnlHaltReason,
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Available,
		},
		Held: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Held,
		},
		Incoming: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Incoming,
		},
	}
	if snapshot.RealizedPnlHaltReason == "" {
		req.RealizedPnl = snapshot.RealizedPnl
	}
	return req
}

func (n *localNode) ensureAdjustmentExternalIDUnused(
	ctx context.Context, externalID domain.ExternalID,
) error {
	if externalID.IsZero() {
		return nil
	}
	page, err := n.realm.ListAdjustmentRows(ctx, store.AdjustmentListFilter{
		ExternalID: externalID,
		Page:       store.PageSpec{Limit: 1},
	})
	if err != nil {
		return fmt.Errorf("check adjustment external id: %w", err)
	}
	if len(page.Rows) > 0 {
		return fmt.Errorf("adjustment %q: %w", externalID, domain.ErrAlreadyExists)
	}
	return nil
}

// ensureAutoCreatedAccount creates the account when it does not exist yet so an
// adjustment, order, or execution report against a fresh account succeeds in
// one call instead of failing with an unknown-account error. The new account
// joins the default group (empty GroupCode, no group assigned); its id is
// validated the same way CreateAccount validates it, and the creation is
// audited like a standalone CreateAccount, naming operation as the trigger. It
// reports whether the account was newly created so the caller rebuilds the
// engine once before entering the account lane. It runs under the mutation lock
// the caller already holds and must not rebuild the engine itself: a rebuild
// stops the live engine's async runtime, which would deadlock if run inside a
// lane callback.
func (n *localNode) ensureAutoCreatedAccount(
	ctx context.Context, id domain.AccountID, operation string, caller domain.Caller,
) (bool, error) {
	if _, ok, err := n.realm.GetAccount(ctx, id); err != nil {
		return false, fmt.Errorf("read account for %s: %w", operation, err)
	} else if ok {
		return false, nil
	}
	if err := domain.ValidateAccountID(id); err != nil {
		return false, err
	}
	account, err := n.realm.CreateAccount(ctx, domain.Account{Code: id})
	if err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return false, nil
		}
		return false, fmt.Errorf("create account for %s: %w", operation, err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      account.Code,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("auto-created account %s by %s", account.Code, operation),
	}); err != nil {
		return false, fmt.Errorf("audit create account: %w", err)
	}
	return true, nil
}

// ensureAccountAndAssetsRegistered auto-creates the account and each named asset
// pre-lane and rebuilds the engine once when anything was newly created, so the
// live resolver knows the account and both assets before the caller enters the
// account lane. Empty asset codes are skipped. The rebuild swaps n.engine, so
// the caller must read n.engine again after this returns.
func (n *localNode) ensureAccountAndAssetsRegistered(
	ctx context.Context, id domain.AccountID, operation string,
	caller domain.Caller, assets ...string,
) error {
	accountCreated, err := n.ensureAutoCreatedAccount(ctx, id, operation, caller)
	if err != nil {
		return err
	}
	assetsCreated := false
	for _, code := range assets {
		if code == "" {
			continue
		}
		created, err := n.ensureAutoCreatedAsset(ctx, code, operation, caller)
		if err != nil {
			return err
		}
		assetsCreated = assetsCreated || created
	}
	if accountCreated || assetsCreated {
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return fmt.Errorf("rebuild engine after auto-create: %w", err)
		}
	}
	return nil
}

func (n *localNode) accountOrAssetsNeedAutoCreate(
	ctx context.Context, id domain.AccountID, assets ...string,
) (bool, error) {
	if _, ok, err := n.realm.GetAccount(ctx, id); err != nil {
		return false, fmt.Errorf("read account for auto-create check: %w", err)
	} else if !ok {
		return true, nil
	}
	for _, code := range assets {
		if code == "" {
			continue
		}
		if _, ok, err := n.realm.GetAsset(ctx, code); err != nil {
			return false, fmt.Errorf("read asset for auto-create check: %w", err)
		} else if !ok {
			return true, nil
		}
	}
	return false, nil
}

func (n *localNode) ensureAccountAndAssetsRegisteredExclusive(
	ctx context.Context, id domain.AccountID, operation string,
	caller domain.Caller, assets ...string,
) error {
	needed, err := n.accountOrAssetsNeedAutoCreate(ctx, id, assets...)
	if err != nil {
		return err
	}
	if !needed {
		return nil
	}
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()
	return n.ensureAccountAndAssetsRegistered(ctx, id, operation, caller, assets...)
}
