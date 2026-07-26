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
// then persisted in one store transaction. Newly persisted account and group
// ids are published to the live resolver before those effects run, without
// replacing the engine. The transaction is all-or-nothing, so a failed import
// writes nothing. Because engine effects already ran when it fails, the engine
// is reconciled from the persisted store state so the engine and store never
// diverge. Any account the engine kill-switched while applying a P&L or position
// snapshot is mirrored once the transaction commits.
func businessCSVRollbackScope() backup.Scope {
	return backup.Scope{
		Sections: []backup.Section{
			backup.SectionAccountsGroups,
			backup.SectionPositions,
			backup.SectionActivityHistory,
			backup.SectionAuditLog,
		},
		Accounts:  backup.EntitySelector{All: true},
		Positions: backup.EntitySelector{All: true},
	}
}

func (n *localNode) ApplyBusinessCSVImport(
	ctx context.Context,
	in store.BusinessCSVImport,
	caller domain.Caller,
) error {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()
	eng := n.currentEngine()

	rollback, err := n.realm.ExportBackup(ctx, businessCSVRollbackScope())
	if err != nil {
		return fmt.Errorf("capture business CSV rollback backup: %w", err)
	}
	rollbackCtx := context.WithoutCancel(ctx)

	seenBalances := make(map[string]struct{}, len(in.Balances))
	for _, balance := range in.Balances {
		balanceKey := businessCSVImportBalanceKey(balance)
		if _, ok := seenBalances[balanceKey]; ok {
			return fmt.Errorf("duplicate position snapshot %s/%s: %w",
				balance.Account, balance.Asset, domain.ErrInvalid)
		}
		seenBalances[balanceKey] = struct{}{}
	}

	ensuredGroups := make(map[string]bool, len(in.Groups))
	for _, row := range in.Groups {
		ensuredGroups[row.Group.Code] = true
	}
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
		// Account blocks for CSV-created accounts run after their persisted numeric
		// ids are published to the live resolver; see the engine-effects phase.
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
	newGroups := make([]string, 0, len(preStore.Groups))
	for _, row := range preStore.Groups {
		if !row.Exists {
			newGroups = append(newGroups, row.Group.Code)
		}
	}
	newAccounts := make([]domain.AccountID, 0, len(preStore.Accounts))
	for _, row := range preStore.Accounts {
		if !row.Exists {
			newAccounts = append(newAccounts, row.Account.Code)
		}
	}
	if len(preStore.Groups) > 0 || len(preStore.Accounts) > 0 {
		if err := n.realm.ApplyBusinessCSVImport(ctx, preStore); err != nil {
			// This is the first transactional store write and no engine effect has
			// run yet. RealmStore guarantees the transaction is all-or-nothing, so
			// the current engine and the pre-import store already agree.
			return fmt.Errorf("apply business CSV dictionaries: %w", err)
		}
		if len(newGroups) > 0 || len(newAccounts) > 0 {
			resolver, err := requireDictionaryResolver(eng)
			if err != nil {
				return n.rollbackStoreAndEngine(rollbackCtx, rollback,
					fmt.Errorf("resolve live business CSV dictionary: %w", err))
			}
			for _, code := range newGroups {
				group, ok, err := n.realm.GetGroup(ctx, code)
				if err != nil {
					return n.rollbackStoreAndEngine(rollbackCtx, rollback,
						fmt.Errorf("read persisted business CSV group %q: %w", code, err))
				}
				if !ok {
					return n.rollbackStoreAndEngine(rollbackCtx, rollback,
						fmt.Errorf("persisted business CSV group %q: %w", code, domain.ErrNotFound))
				}
				if err := resolver.AddGroupResolverEntry(group); err != nil {
					return n.rollbackStoreAndEngine(rollbackCtx, rollback,
						fmt.Errorf("publish business CSV group %q: %w", code, err))
				}
			}
			for _, code := range newAccounts {
				account, ok, err := n.realm.GetAccount(ctx, code)
				if err != nil {
					return n.rollbackStoreAndEngine(rollbackCtx, rollback,
						fmt.Errorf("read persisted business CSV account %q: %w", code, err))
				}
				if !ok {
					return n.rollbackStoreAndEngine(rollbackCtx, rollback,
						fmt.Errorf("persisted business CSV account %q: %w", code, domain.ErrNotFound))
				}
				if err := resolver.AddAccountResolverEntry(account); err != nil {
					return n.rollbackStoreAndEngine(rollbackCtx, rollback,
						fmt.Errorf("publish business CSV account %q: %w", code, err))
				}
			}
		}
		for i := range in.Groups {
			in.Groups[i].Exists = true
		}
		for i := range in.Accounts {
			in.Accounts[i].Exists = true
		}
	}

	for _, row := range in.Groups {
		applyErr := eng.RunGroupSynchronized(ctx, row.Group.Code,
			func(lane engine.GroupLane) error {
				if err := applyGroupCurrency(
					ctx, lane, row.Group.Code, row.Group.Currency,
				); err != nil {
					return fmt.Errorf("apply group currency: %w", err)
				}
				return n.applyGroupBlock(ctx, lane, row.Group.Code,
					row.Group.Blocked, row.Group.BlockReason)
			})
		if applyErr != nil {
			return n.rollbackStoreAndEngine(rollbackCtx, rollback,
				fmt.Errorf("apply group block: %w", applyErr))
		}
	}
	// Match cold hydration order: group membership must be live before an
	// account P&L seed evaluates group-scoped barriers and kill-switches.
	for _, move := range groupMoves {
		if err := n.applyGroupMove(ctx, eng, move.account, move.old, move.next); err != nil {
			return n.rollbackStoreAndEngine(rollbackCtx, rollback,
				fmt.Errorf("apply account group: %w", err))
		}
	}

	importedAccounts := make(map[domain.AccountID]store.BusinessCSVImportAccount, len(in.Accounts))
	for _, row := range in.Accounts {
		importedAccounts[row.Account.Code] = row
	}
	persistedAccounts, err := n.realm.ListAccounts(ctx)
	if err != nil {
		return n.rollbackStoreAndEngine(rollbackCtx, rollback,
			fmt.Errorf("list accounts after business CSV dictionaries: %w", err))
	}
	pnlBlocked := make(map[domain.AccountID]struct{})
	var pnlBlocks []domain.AccountBlock
	for _, account := range persistedAccounts {
		row, imported := importedAccounts[account.Code]
		if !imported {
			continue
		}
		applyErr := eng.RunAccountSynchronized(ctx, account.Code,
			func(lane engine.AccountLane) error {
				// Account runtime state carries only the explicit account override.
				// Group/default inheritance is owned by the group lane above.
				if err := applyAccountCurrency(
					ctx, lane, account.Code, account.Currency,
				); err != nil {
					return fmt.Errorf("apply account currency: %w", err)
				}
				if row.PnlSpecified {
					pnl := row.Account.Pnl
					if pnl == "" {
						pnl = "0"
					}
					if row.Account.PnlHaltReason != "" {
						pnl = ""
					}
					blocks, err := lane.SetAccountPnlState(
						ctx,
						account.Code,
						pnl,
						row.Account.PnlHaltReason,
					)
					if err != nil {
						return fmt.Errorf("apply account pnl: %w", err)
					}
					if len(blocks) > 0 {
						pnlBlocked[account.Code] = struct{}{}
						pnlBlocks = append(pnlBlocks, blocks...)
					}
				}
				if _, blockedByPnl := pnlBlocked[account.Code]; blockedByPnl &&
					!row.Account.Blocked {
					return nil
				}
				return n.applyBlock(ctx, lane, account.Code,
					row.Account.Blocked, row.Account.BlockReason)
			})
		if applyErr != nil {
			return n.rollbackStoreAndEngine(rollbackCtx, rollback,
				fmt.Errorf("apply business CSV account %q: %w", account.Code, applyErr))
		}
	}
	if len(pnlBlocked) > 0 {
		audits := in.Audits[:0]
		for _, audit := range in.Audits {
			_, blockedByPnl := pnlBlocked[audit.Account]
			if blockedByPnl && audit.Action == domain.AuditActionUnblock {
				continue
			}
			audits = append(audits, audit)
		}
		in.Audits = audits
	}
	// Blocks the engine latched while committing the position-snapshot batches.
	// The mirror is deferred until the import transaction commits: that write
	// carries every account row's CSV blocked flag and would otherwise clear a
	// block the engine just latched. No lane can read the gap - the import holds
	// the live identity-publication gate throughout.
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
		if err := eng.RunAccountSynchronized(ctx, account, func(lane engine.AccountLane) error {
			var err error
			results, batchReject, err = lane.ApplyAccountAdjustmentBatch(ctx, account, reqs)
			return err
		}); err != nil {
			return n.rollbackStoreAndEngine(rollbackCtx, rollback,
				fmt.Errorf("apply position snapshot adjustment: %w", err))
		}
		if batchReject != nil {
			return n.rollbackStoreAndEngine(rollbackCtx, rollback,
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
				return n.rollbackStoreAndEngine(rollbackCtx, rollback,
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
				return n.rollbackStoreAndEngine(rollbackCtx, rollback,
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
		return n.rollbackStoreAndEngine(rollbackCtx, rollback,
			fmt.Errorf("apply business CSV import: %w", err))
	}
	// The import is committed, so a mirror failure has no store state left to
	// roll back to; the sink's fatal contract owns the outcome from here.
	return n.mirrorAdjustmentAccountBlocks(ctx, append(pnlBlocks, adjustmentBlocks...))
}

func businessCSVImportAccountKey(account domain.Account) string {
	return string(account.Code)
}

func businessCSVImportBalanceKey(balance domain.Balance) string {
	return string(balance.Account) + "\x00" + balance.Asset
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
// audited like a standalone CreateAccount, naming operation as the trigger. The
// caller holds the live identity-publication gate, so the store-assigned numeric
// id is published to the current resolver before any account lane can admit work
// for the alias.
func (n *localNode) ensureAutoCreatedAccount(
	ctx context.Context, id domain.AccountID, operation string, caller domain.Caller,
) error {
	if _, ok, err := n.realm.GetAccount(ctx, id); err != nil {
		return fmt.Errorf("read account for %s: %w", operation, err)
	} else if ok {
		return nil
	}
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	account, err := n.realm.CreateAccount(ctx, domain.Account{Code: id})
	if err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return nil
		}
		return fmt.Errorf("create account for %s: %w", operation, err)
	}
	resolver, err := requireDictionaryResolver(n.currentEngine())
	if err != nil {
		return n.rollbackAutoCreatedAccountPublication(
			ctx,
			account,
			fmt.Errorf("resolve live account dictionary for %s: %w", operation, err),
		)
	}
	if err := resolver.AddAccountResolverEntry(account); err != nil {
		return n.rollbackAutoCreatedAccountPublication(
			ctx,
			account,
			fmt.Errorf("publish auto-created account for %s: %w", operation, err),
		)
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      account.Code,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("auto-created account %s by %s", account.Code, operation),
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit auto-created account", "account", account.Code.String(),
			fmt.Errorf("audit create account: %w", err),
		)
	}
	return nil
}

func (n *localNode) rollbackAutoCreatedAccountPublication(
	ctx context.Context, account domain.Account, cause error,
) error {
	durableCtx := context.WithoutCancel(ctx)
	if err := n.realm.DeleteAccount(durableCtx, account.Code, false); err == nil {
		return cause
	} else {
		reconcileErr := n.rebuildEngineFromStore(durableCtx)
		combined := errors.Join(
			cause,
			fmt.Errorf("rollback auto-created account %q: %w", account.Code, err),
			reconcileErr,
		)
		return n.fatalPostEngineAuditByCode(
			"rollback auto-created account publication",
			"account",
			account.Code.String(),
			combined,
		)
	}
}

// ensureAccountAndAssetsRegistered auto-creates the account, then each named
// asset, pre-lane. The account's stable numeric id is published to the live
// resolver; assets are Officer dictionary state and need no engine replacement.
// Empty asset codes are skipped.
func (n *localNode) ensureAccountAndAssetsRegistered(
	ctx context.Context, id domain.AccountID, operation string,
	caller domain.Caller, assets ...string,
) error {
	if err := n.ensureAutoCreatedAccount(ctx, id, operation, caller); err != nil {
		return err
	}
	for _, code := range assets {
		if code == "" {
			continue
		}
		if _, err := n.ensureAutoCreatedAsset(ctx, code, operation, caller); err != nil {
			return err
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
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()
	return n.ensureAccountAndAssetsRegistered(ctx, id, operation, caller, assets...)
}
