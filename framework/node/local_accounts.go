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
	"slices"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// CreateAccount persists a new account, publishes its stable engine id into the
// live resolver, applies the initial runtime state through synchronized lanes,
// and audits the action. The live identity gate keeps the engine, dispatcher,
// and market-data sink in place while no admitted lane can observe a partial
// identity publication.
func (n *localNode) CreateAccount(
	ctx context.Context, account domain.Account, caller domain.Caller,
) (domain.Account, error) {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return domain.Account{}, err
	}
	defer n.endLiveIdentityPublication()

	if account.Currency != "" {
		if _, err := n.ensureAutoCreatedAsset(
			ctx, account.Currency, "create account currency", caller,
		); err != nil {
			return domain.Account{}, err
		}
	}
	eng := n.currentEngine()
	resolver, err := requireDictionaryResolver(eng)
	if err != nil {
		return domain.Account{}, err
	}

	created, err := n.realm.CreateAccount(ctx, account)
	if err != nil {
		return domain.Account{}, fmt.Errorf("create account: %w", err)
	}

	if err := resolver.AddAccountResolverEntry(created); err != nil {
		rollbackErr := n.realm.DeleteAccount(context.WithoutCancel(ctx), created.Code, true)
		resultErr := errors.Join(
			fmt.Errorf("publish account resolver entry: %w", err),
			optionalOperationError("rollback created account", rollbackErr),
		)
		if rollbackErr != nil {
			resultErr = n.reconcileEngineAfterFailure(
				context.WithoutCancel(ctx),
				"reconcile engine after account rollback failure",
				resultErr,
			)
		}
		return domain.Account{}, resultErr
	}

	applyErr := eng.RunAccountSynchronized(ctx, created.Code, func(lane engine.AccountLane) error {
		if created.Currency != "" {
			if err := lane.SetAccountCurrency(ctx, created.Code, created.Currency); err != nil {
				return fmt.Errorf("apply initial account currency: %w", err)
			}
		}
		if created.Blocked {
			if err := lane.BlockAccount(ctx, created.Code, created.BlockReason); err != nil {
				return fmt.Errorf("apply initial account block: %w", err)
			}
		}
		return nil
	})
	if applyErr == nil && created.GroupCode != "" {
		applyErr = n.applyGroupMove(ctx, eng, created.Code, "", created.GroupCode)
	}
	if applyErr != nil {
		// Account retirement is not available in the SDK yet. Once the resolver
		// alias was published, a failed initial runtime mutation is reconciled by
		// deleting the store row and rebuilding from the surviving snapshot.
		mutationCtx := context.WithoutCancel(ctx)
		rollbackErr := n.realm.DeleteAccount(mutationCtx, created.Code, true)
		cause := errors.Join(
			fmt.Errorf("apply created account runtime state: %w", applyErr),
			optionalOperationError("rollback created account", rollbackErr),
		)
		return domain.Account{}, n.reconcileEngineAfterFailure(
			mutationCtx, "reconcile engine after account create failure", cause,
		)
	}

	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      created.Code,
		AccountTitle: created.Title,
		Detail:       fmt.Sprintf("create account %s", created.Code),
	}); err != nil {
		return domain.Account{}, n.fatalPostEngineAuditByCode(
			"audit create account", "account", created.Code.String(),
			fmt.Errorf("audit create account: %w", err),
		)
	}
	return created, nil
}

func optionalOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

type liveIdentityReconcileError struct {
	cause error
}

func (e *liveIdentityReconcileError) Error() string { return e.cause.Error() }
func (e *liveIdentityReconcileError) Unwrap() error { return e.cause }

// SetAccountBlocked blocks or unblocks the account in the store, then the
// engine, reverting the store on engine failure, and audits the action. missing
// decides whether a kill-switch aimed at an account Officer does not know yet
// registers it first or is rejected.
func (n *localNode) SetAccountBlocked(
	ctx context.Context, key Key, blocked bool, reason string,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) error {
	// Resolve the account before entering the lane: the exclusive helper takes
	// the identity gate itself, which the lane's read lock would deadlock against,
	// and a created account must be published before the lane resolves it.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, missing, blockOperation(blocked), caller,
	); err != nil {
		return err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return err
	}

	runErr := func() error {
		// The engine resolves the account before entering the lane and rejects an
		// unknown code with ErrInvalid, so surface ErrNotFound before submission.
		// Only a delete racing this call can reach it: the account was resolved
		// above.
		if _, ok, err := n.realm.GetAccount(ctx, key.Account); err != nil {
			return fmt.Errorf("read account for block: %w", err)
		} else if !ok {
			return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
		}

		return eng.RunAccountSynchronized(
			ctx, key.Account, func(lane engine.AccountLane) error {
				prev, ok, err := n.realm.GetAccount(ctx, key.Account)
				if err != nil {
					return fmt.Errorf("read account for block: %w", err)
				}
				if !ok {
					return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
				}
				if err := n.realm.SetAccountBlocked(
					ctx, key.Account, blocked, reason,
				); err != nil {
					return fmt.Errorf("set account blocked: %w", err)
				}

				if applyErr := n.applyBlock(
					ctx, lane, key.Account, blocked, reason,
				); applyErr != nil {
					mutationCtx := context.WithoutCancel(ctx)
					revertEngineErr := n.applyBlock(
						mutationCtx, lane, key.Account,
						prev.Blocked, prev.BlockReason,
					)
					revertStoreErr := n.realm.SetAccountBlocked(
						mutationCtx, key.Account,
						prev.Blocked, prev.BlockReason,
					)
					cause := errors.Join(
						fmt.Errorf("apply account block: %w", applyErr),
						optionalOperationError(
							"revert account block runtime", revertEngineErr,
						),
						optionalOperationError(
							"revert account block store", revertStoreErr,
						),
					)
					if revertEngineErr != nil || revertStoreErr != nil {
						return &liveIdentityReconcileError{cause: cause}
					}
					return cause
				}

				action := domain.AuditActionBlock
				detail := blockDetail(key.Account, reason)
				if !blocked {
					action = domain.AuditActionUnblock
					detail = unblockDetail(key.Account, reason)
				}
				if err := n.audit(
					context.WithoutCancel(ctx), caller, store.AuditEntry{
						Action:       action,
						Account:      key.Account,
						AccountTitle: prev.Title,
						Detail:       detail,
					},
				); err != nil {
					return n.fatalPostEngineAuditByCode(
						"audit account block", "account", key.Account.String(),
						fmt.Errorf("audit account block: %w", err),
					)
				}
				return nil
			},
		)
	}()

	var reconcileErr *liveIdentityReconcileError
	if !errors.As(runErr, &reconcileErr) {
		done()
		return runErr
	}
	if err := n.beginEngineRestartFromLane(done); err != nil {
		return n.fatalReconciliation(
			"acquire account block reconciliation gate",
			errors.Join(reconcileErr.cause, err),
		)
	}
	defer n.endEngineRestart()
	return n.reconcileEngineAfterFailure(
		context.WithoutCancel(ctx),
		"reconcile engine after account block failure",
		reconcileErr.cause,
	)
}

// blockOperation names the triggering operation recorded on an account created
// by a kill-switch request.
func blockOperation(blocked bool) string {
	if blocked {
		return "block account"
	}
	return "unblock account"
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

		// The engine reason is normalized for restore validation, never rewritten.
		if err := n.realm.SetAccountBlocked(
			ctx, block.Account, true, domain.NormalizeReason(block.Reason),
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

// audit appends one audit row, stamping the caller's principal and source onto
// the entry. Every mutation routes its audit through here so attribution is
// applied uniformly.
func (n *localNode) audit(ctx context.Context, caller domain.Caller, entry store.AuditEntry) error {
	entry.Actor = caller.Principal
	entry.Source = caller.Source
	return n.realm.AppendAudit(ctx, entry)
}

func (n *localNode) auditBatch(
	ctx context.Context, caller domain.Caller, entries []store.AuditEntry,
) error {
	for i := range entries {
		entries[i].Actor = caller.Principal
		entries[i].Source = caller.Source
	}
	return n.realm.AppendAuditBatch(ctx, entries)
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
// A brand-new target group is persisted and published into the live resolver
// under the identity gate. The account move itself still runs through the
// account lane and the concrete old/new group lanes. missing decides whether an
// account Officer does not know yet is registered before the move or rejected.
func (n *localNode) SetAccountGroup(
	ctx context.Context, key Key, groupCode string,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) error {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()

	// The identity gate is already held, so the account is resolved through the
	// non-acquiring helper; ensureAccountAndAssetsRegisteredExclusive would
	// deadlock re-entering the same gate.
	if err := n.ensureAccount(
		ctx, key.Account, missing, "set account group", caller,
	); err != nil {
		return err
	}
	eng := n.currentEngine()
	resolver, err := requireDictionaryResolver(eng)
	if err != nil {
		return err
	}
	if groupCode != "" {
		if _, _, err := n.ensureGroupRegisteredLocked(ctx, groupCode, resolver); err != nil {
			return fmt.Errorf("ensure group for set: %w", err)
		}
	}

	runErr := eng.RunAccountSynchronized(ctx, key.Account, func(engine.AccountLane) error {
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
				if !errors.Is(err, domain.ErrConflict) {
					return err
				}
				return domain.NewCurrencyChangeBlockedError(
					domain.ScopeAccount, key.Account.String(), err,
				)
			}
		}

		if err := n.realm.SetAccountGroup(ctx, key.Account, groupCode); err != nil {
			return fmt.Errorf("set account group: %w", err)
		}

		if applyErr := n.applyGroupMove(
			ctx, eng, key.Account, prev.GroupCode, groupCode,
		); applyErr != nil {
			revertStoreErr := n.realm.SetAccountGroup(
				context.WithoutCancel(ctx), key.Account, prev.GroupCode,
			)
			return &liveIdentityReconcileError{cause: errors.Join(
				fmt.Errorf("apply account group: %w", applyErr),
				optionalOperationError("revert account group store", revertStoreErr),
			)}
		}
		detail := setAccountGroupDetail(key.Account, prev.GroupCode, groupCode)
		// The move is a membership event of both groups, so it is filed under each
		// of them; a group that loses a member must not have to be found through
		// the destination group's rows.
		groupCodes := accountGroupAuditCodes(prev.GroupCode, groupCode)
		entries := make([]store.AuditEntry, 0, len(groupCodes))
		for _, code := range groupCodes {
			entries = append(entries, store.AuditEntry{
				Action:       domain.AuditActionSetGroup,
				Account:      key.Account,
				AccountTitle: prev.Title,
				Group:        code,
				Detail:       detail,
			})
		}
		if err := n.auditBatch(context.WithoutCancel(ctx), caller, entries); err != nil {
			return n.fatalPostEngineAuditByCode(
				"audit set account group", "account", key.Account.String(),
				fmt.Errorf("audit set account group: %w", err),
			)
		}
		return nil
	})
	var reconcileErr *liveIdentityReconcileError
	if errors.As(runErr, &reconcileErr) {
		return n.reconcileEngineAfterFailure(
			context.WithoutCancel(ctx),
			"reconcile engine after account group failure",
			reconcileErr.cause,
		)
	}
	return runErr
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
			var revertErr error
			if oldGroup != "" {
				mutationCtx := context.WithoutCancel(ctx)
				revertErr = eng.RunGroupSynchronized(
					mutationCtx, oldGroup, func(lane engine.GroupLane) error {
						return lane.RegisterGroup(mutationCtx, accounts, oldGroup)
					},
				)
			}
			return errors.Join(
				err,
				optionalOperationError("restore previous account group", revertErr),
			)
		}
	}
	return nil
}

// accountGroupAuditCodes lists the group codes a membership change is filed
// under: both the origin and the destination when the account moves between two
// groups, and only the non-empty side when it joins from or leaves to no group.
// The empty code is the reserved default group and collects no membership rows.
func accountGroupAuditCodes(prevGroupCode, groupCode string) []string {
	codes := make([]string, 0, 2)
	if prevGroupCode != "" {
		codes = append(codes, prevGroupCode)
	}
	if groupCode != "" && groupCode != prevGroupCode {
		codes = append(codes, groupCode)
	}
	return codes
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

// UpdateAccount replaces an account's public code and title and atomically
// renames its live resolver alias without replacing the engine. The stable
// EngineAccountID keeps every SDK-owned account state attached to the same
// account while the operator-facing code changes.
func (n *localNode) UpdateAccount(
	ctx context.Context,
	key Key,
	account domain.Account,
	caller domain.Caller,
) (domain.Account, error) {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return domain.Account{}, err
	}
	defer n.endLiveIdentityPublication()

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
	resolver, err := requireDictionaryResolver(n.currentEngine())
	if err == nil {
		err = resolver.RenameAccountResolverEntry(prev.Code, updated)
	}
	if err != nil {
		mutationCtx := context.WithoutCancel(ctx)
		_, rollbackErr := n.realm.UpdateAccount(mutationCtx, updated.Code, prev)
		if rollbackErr != nil {
			cause := errors.Join(
				fmt.Errorf("publish account resolver rename: %w", err),
				fmt.Errorf("rollback account rename: %w", rollbackErr),
			)
			return domain.Account{}, n.reconcileEngineAfterFailure(
				mutationCtx, "reconcile engine after account rename failure", cause,
			)
		}
		return domain.Account{}, fmt.Errorf("publish account resolver rename: %w", err)
	}
	detail := updateAccountDetail(prev.Code, updated.Code)
	entries := []store.AuditEntry{{
		Action:       domain.AuditActionUpdateAccount,
		Account:      updated.Code,
		AccountTitle: updated.Title,
		Detail:       detail,
	}}
	if prev.Code != updated.Code {
		entries[0].Detail += " (record under new code)"
		entries = append([]store.AuditEntry{{
			Action:       domain.AuditActionUpdateAccount,
			Account:      prev.Code,
			AccountTitle: prev.Title,
			Detail:       detail + " (record under old code)",
		}}, entries...)
	}
	if err := n.auditBatch(context.WithoutCancel(ctx), caller, entries); err != nil {
		return domain.Account{}, n.fatalPostEngineAuditByCode(
			"audit update account", "account", updated.Code.String(),
			fmt.Errorf("audit update account: %w", err),
		)
	}
	return updated, nil
}

// DeleteAccount removes an account from the store, rebuilds the engine from the
// surviving rows, and audits the action. Account retirement is not available
// through the SDK runtime API yet, so this remains an intentional rebuild path.
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

	snapshot, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load snapshot for account delete: %w", err)
	}
	snapshot = snapshotWithoutAccount(snapshot, account.Code)
	previousEngine := n.currentEngine()
	next, err := n.build(snapshot)
	if err != nil {
		return fmt.Errorf("build engine for account delete: %w", err)
	}
	if next == nil {
		return fmt.Errorf("build engine for account delete returned nil")
	}
	if next == previousEngine {
		return fmt.Errorf("build engine for account delete returned current engine")
	}
	transition, err := n.beginMarketDataTransition(next)
	if err != nil {
		next.Stop()
		return fmt.Errorf("prepare account delete market data: %w", err)
	}
	if err := n.replayMarketDataInto(ctx, next); err != nil {
		n.cancelMarketDataTransition(transition)
		next.Stop()
		return fmt.Errorf("replay market data for account delete: %w", err)
	}

	// DeleteAccount is the durable commit point. The transition flushes every
	// provider update first, then executes this hook while both sink-routing
	// locks are held. A failed store transaction restores the old sink route and
	// leaves the old engine current. A successful transaction is followed only
	// by the infallible engine/sink pointer swap.
	durableCtx := context.WithoutCancel(ctx)
	prev, err := n.commitMarketDataTransitionWithHook(transition, next, func() error {
		if err := n.realm.DeleteAccount(durableCtx, account.Code, force); err != nil {
			return fmt.Errorf("delete account: %w", err)
		}
		return nil
	})
	if err != nil {
		n.cancelMarketDataTransition(transition)
		next.Stop()
		return fmt.Errorf("commit account delete engine transition: %w", err)
	}
	if prev != nil && prev != next {
		prev.Stop()
	}
	if err := n.mirrorSeedAccountBlocks(durableCtx, next); err != nil {
		return n.fatalPostEngineAuditByCode(
			"mirror account delete seed blocks", "account", account.Code.String(),
			fmt.Errorf("mirror account delete seed blocks: %w", err),
		)
	}
	if err := n.audit(durableCtx, caller, store.AuditEntry{
		Action:       domain.AuditActionDeleteAccount,
		Account:      account.Code,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("delete account %s", account.Code),
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit delete account", "account", account.Code.String(),
			fmt.Errorf("audit delete account: %w", err),
		)
	}
	return nil
}

func snapshotWithoutAccount(
	snapshot engine.Snapshot, account domain.AccountID,
) engine.Snapshot {
	snapshot.Accounts = slices.DeleteFunc(snapshot.Accounts, func(row domain.Account) bool {
		return row.Code == account
	})
	snapshot.Balances = slices.DeleteFunc(snapshot.Balances, func(row domain.Balance) bool {
		return row.Account == account
	})
	snapshot.RateLimits = slices.DeleteFunc(
		snapshot.RateLimits,
		func(row domain.LimitRate) bool { return row.Account == account },
	)
	snapshot.OrderSizeLimits = slices.DeleteFunc(
		snapshot.OrderSizeLimits,
		func(row domain.LimitOrderSize) bool { return row.Account == account },
	)
	snapshot.SpotFundsPnlBoundsLimits = slices.DeleteFunc(
		snapshot.SpotFundsPnlBoundsLimits,
		func(row domain.LimitSpotFundsPnlBounds) bool { return row.Account == account },
	)
	return snapshot
}
