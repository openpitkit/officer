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
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// CreateAccount persists a new account, rebuilds the live engine so the account
// enters the resolver, and audits the action. The resolver is built from the
// snapshot and the engine has no incremental account registration, so a new
// account stays invisible to the engine — its adjustments, group moves, and
// orders reject as "unknown account" — until the engine is rebuilt from the
// store. This mirrors the rebuild DeleteAccount performs when the account set
// shrinks. The store assigns the engine account id and returns the populated
// account.
func (n *localNode) CreateAccount(
	ctx context.Context, account domain.Account, caller domain.Caller,
) (domain.Account, error) {
	if err := n.beginEngineRestart(); err != nil {
		return domain.Account{}, err
	}
	defer n.endEngineRestart()

	if account.Currency != "" {
		if _, err := n.ensureAutoCreatedAsset(
			ctx, account.Currency, "create account currency", caller,
		); err != nil {
			return domain.Account{}, err
		}
	}

	account, err := n.realm.CreateAccount(ctx, account)
	if err != nil {
		return domain.Account{}, fmt.Errorf("create account: %w", err)
	}

	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return domain.Account{}, fmt.Errorf("rebuild engine after account create: %w", err)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      account.Code,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("create account %s", account.Code),
	}); err != nil {
		return domain.Account{}, fmt.Errorf("audit create account: %w", err)
	}
	return account, nil
}

// SetAccountBlocked blocks or unblocks the account in the store, then the
// engine, reverting the store on engine failure, and audits the action.
func (n *localNode) SetAccountBlocked(
	ctx context.Context, key Key, blocked bool, reason string, caller domain.Caller,
) error {
	eng, done, err := n.beginLane()
	if err != nil {
		return err
	}
	defer done()

	// Pre-lane existence check: the engine resolves the account before entering
	// the lane and rejects an unknown code with ErrInvalid, so a missing account
	// must be surfaced as ErrNotFound here (before the lane) or the closure below
	// never runs to report it.
	if _, ok, err := n.realm.GetAccount(ctx, key.Account); err != nil {
		return fmt.Errorf("read account for block: %w", err)
	} else if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}

	// Serialize the prev-read, store write, engine block, revert, and audit on
	// the one account lane so concurrent same-account admin ops cannot interleave
	// the store write with the engine block and diverge. The lane also serializes
	// against fills and the execution-report kill-switch, which mutate the same
	// account's engine block state on that lane.
	return eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		prev, ok, err := n.realm.GetAccount(ctx, key.Account)
		if err != nil {
			return fmt.Errorf("read account for block: %w", err)
		}
		if !ok {
			return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
		}

		if err := n.realm.SetAccountBlocked(ctx, key.Account, blocked, reason); err != nil {
			return fmt.Errorf("set account blocked: %w", err)
		}

		if applyErr := n.applyBlock(ctx, lane, key.Account, blocked, reason); applyErr != nil {
			// Revert the store to the previously persisted blocked state.
			_ = n.realm.SetAccountBlocked(ctx, key.Account, prev.Blocked, prev.BlockReason)
			return fmt.Errorf("apply account block: %w", applyErr)
		}

		action := domain.AuditActionBlock
		detail := fmt.Sprintf("block account %s", key.Account)
		if !blocked {
			action = domain.AuditActionUnblock
			detail = fmt.Sprintf("unblock account %s", key.Account)
		}
		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:       action,
			Account:      key.Account,
			AccountTitle: prev.Title,
			Detail:       detail,
		}); err != nil {
			return n.fatalPostEngineAuditByCode(
				"audit account block", "account", key.Account.String(),
				fmt.Errorf("audit account block: %w", err),
			)
		}
		return nil
	})
}

// applyBlock applies the desired blocked state to the engine.
func (n *localNode) applyBlock(
	ctx context.Context, lane engine.AccountLane, id domain.AccountID, blocked bool, reason string,
) error {
	if blocked {
		return lane.BlockAccount(ctx, id, reason)
	}
	return lane.UnblockAccount(ctx, id)
}

// mirrorEngineBlocksAudit writes one system-sourced audit row per engine-recorded
// account block, naming the triggering order and the engine's reason. The block
// UPDATE itself is folded into RecordOrderSettlement's tx (atomic with the fill);
// this audit row is observational and runs post-commit as a best-effort write, so
// a crash between commit and audit leaves the account blocked but the audit cause
// briefly missing - an accepted weak-consistency window, since the audit is not
// part of the financial invariant.
func (n *localNode) mirrorEngineBlocksAudit(
	ctx context.Context, order domain.ExternalID,
	blocks []domain.ExecutionAccountBlock,
) error {
	for _, block := range blocks {
		// A kill-switch block is engine-initiated: it carries no actor principal
		// (the store rejects a non-existent principal code, and the audit row's
		// Actor is empty for system origin). SourceSystem marks the engine channel,
		// and the detail names the engine cause.
		if err := n.realm.AppendAudit(ctx, store.AuditEntry{
			Source:  domain.SourceSystem,
			Action:  domain.AuditActionBlock,
			Account: block.Account,
			Detail:  engineBlockDetail(order, block),
		}); err != nil {
			return fmt.Errorf("audit engine block: %w", err)
		}
	}
	return nil
}

// mirrorPolicyConfigurationBlocks persists and audits blocks the engine already
// applied while accepting a policy update. The caller holds the live-policy or
// restart gate, so no account lane can observe the SDK block before the store
// reflects it.
func (n *localNode) mirrorPolicyConfigurationBlocks(
	ctx context.Context, policy string, blocks []domain.AccountBlock,
) error {
	// The engine has already accepted the update and applied these blocks. A
	// client disconnect must not cancel the durable mirror and leave the account
	// appearing tradable to Officer while the engine rejects it.
	ctx = context.WithoutCancel(ctx)
	seen := make(map[domain.AccountID]struct{}, len(blocks))
	for _, block := range blocks {
		if block.Account == "" {
			return n.fatalPostEngineAuditByCode(
				"record policy configuration block", "account", "unknown",
				fmt.Errorf("policy configuration block has no account"),
			)
		}
		if _, duplicate := seen[block.Account]; duplicate {
			continue
		}
		seen[block.Account] = struct{}{}
		account, ok, err := n.realm.GetAccount(ctx, block.Account)
		if err != nil {
			return n.fatalPostEngineAuditByCode(
				"read policy configuration block state", "account", block.Account.String(),
				fmt.Errorf("read policy configuration block state: %w", err),
			)
		}
		if !ok {
			return n.fatalPostEngineAuditByCode(
				"record policy configuration block", "account", block.Account.String(),
				fmt.Errorf("account not found: %w", domain.ErrNotFound),
			)
		}
		if account.Blocked {
			continue
		}

		if err := n.realm.SetAccountBlocked(
			ctx, block.Account, true, policyConfigurationBlockReason(policy, block),
		); err != nil {
			return n.fatalPostEngineAuditByCode(
				"record policy configuration block", "account", block.Account.String(),
				fmt.Errorf("record policy configuration block: %w", err),
			)
		}
		if err := n.realm.AppendAudit(ctx, store.AuditEntry{
			Source:  domain.SourceSystem,
			Action:  domain.AuditActionBlock,
			Account: block.Account,
			Detail:  policyConfigurationBlockDetail(policy, block),
		}); err != nil {
			return n.fatalPostEngineAuditByCode(
				"audit policy configuration block", "account", block.Account.String(),
				fmt.Errorf("audit policy configuration block: %w", err),
			)
		}
	}
	return nil
}

func (n *localNode) mirrorPolicyConfigurationPnls(
	ctx context.Context, updates []engine.AccountPnlUpdate,
) error {
	ctx = context.WithoutCancel(ctx)
	for _, update := range updates {
		if update.Account == "" {
			return n.fatalPostEngineAuditByCode(
				"record policy configuration pnl", "account", "unknown",
				fmt.Errorf("policy configuration pnl update has no account"),
			)
		}
		if err := n.realm.SetAccountPnl(ctx, update.Account, update.Pnl, ""); err != nil {
			return n.fatalPostEngineAuditByCode(
				"record policy configuration pnl", "account", update.Account.String(),
				fmt.Errorf("record policy configuration pnl: %w", err),
			)
		}
	}
	return nil
}

// audit appends one audit row, stamping the caller's principal and source onto
// the entry. Every mutation routes its audit through here so attribution is
// applied uniformly.
func (n *localNode) audit(ctx context.Context, caller domain.Caller, entry store.AuditEntry) error {
	entry.Actor = caller.Principal
	entry.Source = caller.Source
	return n.realm.AppendAudit(ctx, entry)
}

// AppendAudit persists one audit row stamped with the caller. It is the seam
// the backend uses for control-plane actions that have no node-mutating
// counterpart (signing-key management, signing config, approval issue/confirm/
// cancel).
func (n *localNode) AppendAudit(
	ctx context.Context, entry store.AuditEntry, caller domain.Caller,
) error {
	return n.audit(ctx, caller, entry)
}

// GetAccountState returns the account row and the barriers whose scope has the
// account axis and matches the account.
func (n *localNode) GetAccountState(
	ctx context.Context, key Key,
) (domain.Account, AccountLimits, error) {
	account, ok, err := n.realm.GetAccount(ctx, key.Account)
	if err != nil {
		return domain.Account{}, AccountLimits{}, fmt.Errorf("get account: %w", err)
	}
	if !ok {
		return domain.Account{}, AccountLimits{}, fmt.Errorf(
			"account %q: %w", key.Account, domain.ErrNotFound)
	}
	limits, err := n.listLimits(ctx, key.Account)
	if err != nil {
		return domain.Account{}, AccountLimits{}, fmt.Errorf("list account limits: %w", err)
	}
	return account, limits, nil
}

// --- account group & notes --------------------------------------------------

// SetAccountGroup sets or clears the account's group in the store, then moves
// it on the engine (unregister from the old group, register into the new),
// reverts the store on engine failure, and audits the action.
//
// A brand-new target group is auto-created and the engine rebuilt from the store
// under the exclusive engine-restart gate before the lane, so the live resolver
// knows the group when the in-lane RegisterGroup resolves it; an already-known
// target group skips the gate and goes straight to the lane. The account move
// itself (prev-read, store link write, engine membership move, revert, audit)
// runs on the one account lane so concurrent same-account admin ops cannot
// interleave and diverge.
func (n *localNode) SetAccountGroup(
	ctx context.Context, key Key, groupCode string, caller domain.Caller,
) error {
	// Pre-lane existence check: the engine resolves the account before entering
	// the lane and rejects an unknown code with ErrInvalid, so a missing account
	// must be surfaced as ErrNotFound here (before the lane) or the closure below
	// never runs to report it.
	if _, ok, err := n.realm.GetAccount(ctx, key.Account); err != nil {
		return fmt.Errorf("read account for set group: %w", err)
	} else if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}

	// Auto-create the target group and rebuild the engine before entering the
	// lane, so the live resolver knows it when the in-lane RegisterGroup resolves
	// it and so the rebuild never stops a live lane's runtime. Only a genuinely
	// new group escalates to the gate; an existing one is already registered.
	if err := n.ensureGroupRegisteredExclusive(ctx, groupCode); err != nil {
		return err
	}

	eng, done, err := n.beginLane()
	if err != nil {
		return err
	}
	defer done()

	// Serialize the prev-read, store link write, engine membership move, revert,
	// and audit on the one account lane. The prev.GroupCode read must live inside
	// the lane so the unregister/register move is computed from lane-serialized
	// state and never from a group that a concurrent move already changed.
	return eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		prev, ok, err := n.realm.GetAccount(ctx, key.Account)
		if err != nil {
			return fmt.Errorf("read account for set group: %w", err)
		}
		if !ok {
			return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
		}
		if prev.GroupCode == groupCode {
			return nil
		}
		if prev.Currency == "" {
			nextGroupCurrency := ""
			if groupCode != "" {
				group, ok, err := n.realm.GetGroup(ctx, groupCode)
				if err != nil {
					return fmt.Errorf("read target group for currency: %w", err)
				}
				if !ok {
					return fmt.Errorf("group %q: %w", groupCode, domain.ErrNotFound)
				}
				nextGroupCurrency = group.Currency
			}
			nextEffective, _ := domain.ResolveCurrencyCascade(
				"",
				nextGroupCurrency,
				prev.DefaultCurrency,
			)
			if err := n.guardEffectiveCurrencyChange(
				ctx,
				[]domain.AccountID{key.Account},
				prev.EffectiveCurrency,
				nextEffective,
			); err != nil {
				return err
			}
		}

		if err := n.realm.SetAccountGroup(ctx, key.Account, groupCode); err != nil {
			return fmt.Errorf("set account group: %w", err)
		}

		if applyErr := n.applyGroupMove(
			ctx, eng, key.Account, prev.GroupCode, groupCode,
		); applyErr != nil {
			// Best-effort revert; the caller already surfaces the primary error.
			_ = n.realm.SetAccountGroup(ctx, key.Account, prev.GroupCode)
			return fmt.Errorf("apply account group: %w", applyErr)
		}

		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:       domain.AuditActionSetGroup,
			Account:      key.Account,
			AccountTitle: prev.Title,
			Detail:       setAccountGroupDetail(key.Account, groupCode),
		}); err != nil {
			return n.fatalPostEngineAuditByCode(
				"audit set account group", "account", key.Account.String(),
				fmt.Errorf("audit set account group: %w", err),
			)
		}
		return nil
	})
}

// ensureGroupRegisteredExclusive auto-creates the target group record and
// rebuilds the engine from the store under the exclusive engine-restart gate
// when the group is not already known, so the live resolver knows it before the
// caller enters an account lane and RegisterGroup resolves it. An empty code
// ("no group") and an already-persisted group skip the gate: an existing store
// group is already engine-registered because its creation rebuilt the engine.
func (n *localNode) ensureGroupRegisteredExclusive(
	ctx context.Context, groupCode string,
) error {
	if groupCode == "" {
		return nil
	}
	if _, ok, err := n.realm.GetGroup(ctx, groupCode); err != nil {
		return fmt.Errorf("read group for set: %w", err)
	} else if ok {
		return nil
	}
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	if err := n.ensureGroupRecordLocked(ctx, groupCode); err != nil {
		return fmt.Errorf("ensure group record on set: %w", err)
	}
	// The auto-created record is unknown to the resolver until the engine is
	// rebuilt from the store, so rebuild before the lane runs.
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return fmt.Errorf("rebuild engine after group ensure: %w", err)
	}
	return nil
}

// applyGroupMove moves one account between groups on the engine. Each concrete
// group membership mutation runs on that group's synchronized lane. An empty
// group code means "no group", so clearing only unregisters and setting from
// none only registers.
func (n *localNode) applyGroupMove(
	ctx context.Context, eng engine.Engine, id domain.AccountID, oldGroup, newGroup string,
) error {
	accounts := []domain.AccountID{id}
	if oldGroup != "" {
		if err := eng.RunGroupSynchronized(ctx, oldGroup, func(lane engine.GroupLane) error {
			return lane.UnregisterGroup(ctx, accounts, oldGroup)
		}); err != nil {
			return err
		}
	}
	if newGroup != "" {
		if err := eng.RunGroupSynchronized(ctx, newGroup, func(lane engine.GroupLane) error {
			return lane.RegisterGroup(ctx, accounts, newGroup)
		}); err != nil {
			if oldGroup != "" {
				_ = eng.RunGroupSynchronized(ctx, oldGroup, func(lane engine.GroupLane) error {
					return lane.RegisterGroup(ctx, accounts, oldGroup)
				})
			}
			return err
		}
	}
	return nil
}

// SetAccountNotes replaces the account's notes in the store and audits the
// action. Notes never reach the engine, so there is no engine side-effect and
// no account-lane hop is needed: it writes only the notes store row, which no
// engine lane touches.
func (n *localNode) SetAccountNotes(
	ctx context.Context, key Key, notes string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetAccountNotes(ctx, key.Account, notes); err != nil {
		return fmt.Errorf("set account notes: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetNotes,
		Account: key.Account,
		Detail:  fmt.Sprintf("set notes account %s", key.Account),
	}); err != nil {
		return fmt.Errorf("audit set account notes: %w", err)
	}
	return nil
}

// UpdateAccount replaces an account's public code and title, rebuilds the
// engine resolver, and audits the change. It takes no account lane: the code
// change is applied by a full engine rebuild (which cannot run inside a lane),
// already serialized against every lane by the exclusive restart gate.
func (n *localNode) UpdateAccount(
	ctx context.Context,
	key Key,
	account domain.Account,
	caller domain.Caller,
) (domain.Account, error) {
	if err := n.beginEngineRestart(); err != nil {
		return domain.Account{}, err
	}
	defer n.endEngineRestart()

	prev, ok, err := n.realm.GetAccount(ctx, key.Account)
	if err != nil {
		return domain.Account{}, fmt.Errorf("read account for update: %w", err)
	}
	if !ok {
		return domain.Account{},
			fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}
	updated, err := n.realm.UpdateAccount(ctx, key.Account, account)
	if err != nil {
		return domain.Account{}, fmt.Errorf("update account: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return domain.Account{},
			fmt.Errorf("rebuild engine after account update: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionUpdateAccount,
		Account:      updated.Code,
		AccountTitle: updated.Title,
		Detail: fmt.Sprintf(
			"update account %s -> %s",
			prev.Code,
			updated.Code,
		),
	}); err != nil {
		return domain.Account{}, fmt.Errorf("audit update account: %w", err)
	}
	return updated, nil
}

// DeleteAccount removes an account from the store, rebuilds the engine from the
// surviving rows, and audits the action.
func (n *localNode) DeleteAccount(
	ctx context.Context, key Key, force bool, caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	account, ok, err := n.realm.GetAccount(ctx, key.Account)
	if err != nil {
		return fmt.Errorf("read account for delete: %w", err)
	}
	if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}
	if err := n.realm.DeleteAccount(ctx, key.Account, force); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return fmt.Errorf("rebuild engine after account delete: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionDeleteAccount,
		Account:      key.Account,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("delete account %s", key.Account),
	}); err != nil {
		return fmt.Errorf("audit delete account: %w", err)
	}
	return nil
}
