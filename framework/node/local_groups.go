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

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// --- groups -----------------------------------------------------------------

func requireDictionaryResolver(eng engine.Engine) (engine.DictionaryResolver, error) {
	resolver, ok := eng.(engine.DictionaryResolver)
	if !ok {
		return nil, fmt.Errorf("engine does not support live dictionary updates: %w", domain.ErrNotImplemented)
	}
	return resolver, nil
}

// CreateGroup persists a new account group, publishes its stable engine id, and
// applies its group-owned runtime state without replacing the engine or sink.
func (n *localNode) CreateGroup(
	ctx context.Context, group domain.AccountGroup, caller domain.Caller,
) (domain.AccountGroup, error) {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return domain.AccountGroup{}, err
	}
	defer n.endLiveIdentityPublication()

	if group.Currency != "" {
		if _, err := n.ensureAutoCreatedAsset(
			ctx, group.Currency, "create group currency", caller,
		); err != nil {
			return domain.AccountGroup{}, err
		}
	}
	eng := n.currentEngine()
	resolver, err := requireDictionaryResolver(eng)
	if err != nil {
		return domain.AccountGroup{}, err
	}

	created, err := n.realm.CreateGroup(ctx, group)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("create group: %w", err)
	}
	if err := resolver.AddGroupResolverEntry(created); err != nil {
		rollbackErr := n.realm.DeleteGroup(context.WithoutCancel(ctx), created.Code)
		resultErr := errors.Join(
			fmt.Errorf("publish group resolver entry: %w", err),
			optionalOperationError("rollback created group", rollbackErr),
		)
		if rollbackErr != nil {
			resultErr = n.reconcileEngineAfterFailure(
				context.WithoutCancel(ctx),
				"reconcile engine after group rollback failure",
				resultErr,
			)
		}
		return domain.AccountGroup{}, resultErr
	}
	if created.Currency != "" || created.Blocked {
		err = eng.RunGroupSynchronized(ctx, created.Code, func(lane engine.GroupLane) error {
			if created.Currency != "" {
				if err := lane.SetGroupCurrency(ctx, created.Code, created.Currency); err != nil {
					return fmt.Errorf("apply initial group currency: %w", err)
				}
			}
			if created.Blocked {
				return n.applyGroupBlock(ctx, lane, created.Code, true, created.BlockReason)
			}
			return nil
		})
		if err != nil {
			mutationCtx := context.WithoutCancel(ctx)
			resolverErr := resolver.RemoveGroupResolverEntry(created)
			rollbackErr := n.realm.DeleteGroup(mutationCtx, created.Code)
			// A failed SDK call may have mutated before returning. Rebuild even
			// when resolver/store compensation succeeded so no inaccessible group
			// block state survives on the old engine.
			cause := errors.Join(
				fmt.Errorf("apply created group runtime state: %w", err),
				optionalOperationError("rollback created group resolver", resolverErr),
				optionalOperationError("rollback created group", rollbackErr),
			)
			return domain.AccountGroup{}, n.reconcileEngineAfterFailure(
				mutationCtx, "reconcile engine after group create failure", cause,
			)
		}
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action: domain.AuditActionCreateGroup,
		Group:  created.Code,
		Detail: fmt.Sprintf("create group %s", group.Code),
	}); err != nil {
		return domain.AccountGroup{}, n.fatalPostEngineAuditByCode(
			"audit create group", "group", created.Code,
			fmt.Errorf("audit create group: %w", err),
		)
	}
	return created, nil
}

// ListGroups returns every persisted group.
func (n *localNode) ListGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	groups, err := n.realm.ListGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return groups, nil
}

// ListGroupRows returns persisted groups matching filter.
func (n *localNode) ListGroupRows(
	ctx context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	page, err := n.realm.ListGroupRows(ctx, filter)
	if err != nil {
		return store.GroupListPage{}, fmt.Errorf("list group rows: %w", err)
	}
	return page, nil
}

// GetGroup returns the group and its member accounts. The bool is false when no
// such group exists.
func (n *localNode) GetGroup(
	ctx context.Context, code string,
) (domain.AccountGroup, []domain.Account, bool, error) {
	group, ok, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, nil, false, fmt.Errorf("get group: %w", err)
	}
	if !ok {
		return domain.AccountGroup{}, nil, false, nil
	}
	members, err := n.realm.ListGroupAccounts(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, nil, false, fmt.Errorf("list group accounts: %w", err)
	}
	return group, members, true, nil
}

// SetGroupNotes replaces a group's notes in the store and audits the action.
// If no group record exists yet (e.g. the group is known only via account
// membership), a default record is created first so the update succeeds.
//
// Auto-creating that record also publishes the assigned engine group id into
// the live resolver under the identity gate.
func (n *localNode) SetGroupNotes(
	ctx context.Context, code, notes string, caller domain.Caller,
) error {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()

	resolver, err := requireDictionaryResolver(n.currentEngine())
	if err != nil {
		return err
	}
	_, created, err := n.ensureGroupRegisteredLocked(ctx, code, resolver)
	if err != nil {
		return fmt.Errorf("ensure group for set notes: %w", err)
	}

	if err := n.realm.SetGroupNotes(ctx, code, notes); err != nil {
		return fmt.Errorf("set group notes: %w", err)
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action: domain.AuditActionSetGroupNotes,
		Group:  code,
		Detail: fmt.Sprintf("set notes group %s", code),
	}); err != nil {
		if !created {
			return fmt.Errorf("audit set group notes: %w", err)
		}
		return n.fatalPostEngineAuditByCode(
			"audit set group notes", "group", code,
			fmt.Errorf("audit set group notes: %w", err),
		)
	}
	return nil
}

// UpdateGroup replaces a group's public code and title while retaining its
// stable EngineGroupID and SDK-owned membership and block state.
func (n *localNode) UpdateGroup(
	ctx context.Context,
	oldCode string,
	group domain.AccountGroup,
	caller domain.Caller,
) (domain.AccountGroup, error) {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return domain.AccountGroup{}, err
	}
	defer n.endLiveIdentityPublication()

	prev, ok, err := n.realm.GetGroup(ctx, oldCode)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("read group for update: %w", err)
	}
	if !ok {
		return domain.AccountGroup{},
			fmt.Errorf("group %q: %w", oldCode, domain.ErrNotFound)
	}
	updated, err := n.realm.UpdateGroup(ctx, oldCode, group)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("update group: %w", err)
	}
	resolver, err := requireDictionaryResolver(n.currentEngine())
	if err == nil {
		err = resolver.RenameGroupResolverEntry(prev.Code, updated)
	}
	if err != nil {
		mutationCtx := context.WithoutCancel(ctx)
		_, rollbackErr := n.realm.UpdateGroup(mutationCtx, updated.Code, prev)
		if rollbackErr != nil {
			cause := errors.Join(
				fmt.Errorf("publish group resolver rename: %w", err),
				fmt.Errorf("rollback group rename: %w", rollbackErr),
			)
			return domain.AccountGroup{}, n.reconcileEngineAfterFailure(
				mutationCtx, "reconcile engine after group rename failure", cause,
			)
		}
		return domain.AccountGroup{}, fmt.Errorf("publish group resolver rename: %w", err)
	}
	detail := updateGroupDetail(prev.Code, updated.Code)
	// A rename concerns both codes: file it under each so selecting by the old
	// code shows where the group went and selecting by the new one shows where it
	// came from. A title-only update leaves one code and one row.
	codes := renamedGroupAuditCodes(prev.Code, updated.Code)
	entries := make([]store.AuditEntry, 0, len(codes))
	for _, code := range codes {
		entries = append(entries, store.AuditEntry{
			Action: domain.AuditActionUpdateGroup,
			Group:  code,
			Detail: detail,
		})
	}
	if err := n.auditBatch(context.WithoutCancel(ctx), caller, entries); err != nil {
		return domain.AccountGroup{}, n.fatalPostEngineAuditByCode(
			"audit update group", "group", updated.Code,
			fmt.Errorf("audit update group: %w", err),
		)
	}
	return updated, nil
}

// renamedGroupAuditCodes lists the group codes a group update is filed under:
// both codes for a rename, the single unchanged code otherwise.
func renamedGroupAuditCodes(prevCode, code string) []string {
	if prevCode == code {
		return []string{code}
	}
	return []string{prevCode, code}
}

// SetGroupBlocked blocks or unblocks the group in the store, then the engine,
// reverts the store on engine failure, and audits the action. If no group
// record exists yet (e.g. the group is known only via account membership), a
// default record is created first so the operation succeeds.
//
// The block spans every member account, so the live identity gate quiesces all
// admitted account lanes. The SDK mutation itself still runs through the real
// async-engine group lane; the gate is not a synthetic replacement lane.
func (n *localNode) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string, caller domain.Caller,
) error {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()

	eng := n.currentEngine()
	resolver, err := requireDictionaryResolver(eng)
	if err != nil {
		return err
	}
	prev, _, err := n.ensureGroupRegisteredLocked(ctx, code, resolver)
	if err != nil {
		return fmt.Errorf("read group for block: %w", err)
	}

	if err := n.realm.SetGroupBlocked(ctx, code, blocked, reason); err != nil {
		return fmt.Errorf("set group blocked: %w", err)
	}

	applyErr := eng.RunGroupSynchronized(ctx, code, func(lane engine.GroupLane) error {
		return n.applyGroupBlock(ctx, lane, code, blocked, reason)
	})
	if applyErr != nil {
		mutationCtx := context.WithoutCancel(ctx)
		revertEngineErr := eng.RunGroupSynchronized(
			mutationCtx, code, func(lane engine.GroupLane) error {
				return n.applyGroupBlock(
					mutationCtx, lane, code, prev.Blocked, prev.BlockReason,
				)
			},
		)
		revertStoreErr := n.realm.SetGroupBlocked(
			mutationCtx, code, prev.Blocked, prev.BlockReason,
		)
		if revertEngineErr != nil || revertStoreErr != nil {
			cause := errors.Join(
				fmt.Errorf("apply group block: %w", applyErr),
				optionalOperationError("revert group block runtime", revertEngineErr),
				optionalOperationError("revert group block store", revertStoreErr),
			)
			return n.reconcileEngineAfterFailure(
				mutationCtx, "reconcile engine after group block failure", cause,
			)
		}
		return fmt.Errorf("apply group block: %w", applyErr)
	}

	action := domain.AuditActionBlockGroup
	detail := blockGroupDetail(code, reason)
	if !blocked {
		action = domain.AuditActionUnblockGroup
		detail = unblockGroupDetail(code, reason)
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action: action,
		Group:  code,
		Detail: detail,
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit group block", "group", code,
			fmt.Errorf("audit group block: %w", err),
		)
	}
	return nil
}

// ensureGroupRegisteredLocked returns the persisted group and, when absent,
// creates it and publishes its stable id. The caller must hold the live identity
// gate so no lane can observe the store and resolver between those writes.
func (n *localNode) ensureGroupRegisteredLocked(
	ctx context.Context,
	code string,
	resolver engine.DictionaryResolver,
) (domain.AccountGroup, bool, error) {
	group, ok, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, false, err
	}
	if ok {
		return group, false, nil
	}
	group, err = n.realm.CreateGroup(ctx, domain.AccountGroup{Code: code})
	if err != nil {
		return domain.AccountGroup{}, false, err
	}
	if err := resolver.AddGroupResolverEntry(group); err != nil {
		rollbackErr := n.realm.DeleteGroup(context.WithoutCancel(ctx), group.Code)
		resultErr := errors.Join(
			fmt.Errorf("publish group resolver entry: %w", err),
			optionalOperationError("rollback ensured group", rollbackErr),
		)
		if rollbackErr != nil {
			resultErr = n.reconcileEngineAfterFailure(
				context.WithoutCancel(ctx),
				"reconcile engine after group ensure rollback failure",
				resultErr,
			)
		}
		return domain.AccountGroup{}, false, resultErr
	}
	return group, true, nil
}

// applyGroupBlock applies the desired blocked state through a synchronized
// group lane while the caller holds the live identity gate.
func (n *localNode) applyGroupBlock(
	ctx context.Context, lane engine.GroupLane, code string, blocked bool, reason string,
) error {
	if blocked {
		return lane.BlockGroup(ctx, code, reason)
	}
	return lane.UnblockGroup(ctx, code)
}

// DeleteGroup detaches the group's members, clears its reachable runtime state,
// removes the store row, and finally removes the resolver alias. The operation
// keeps the live engine on its normal success path; rebuild is reserved for a
// failed compensation after a partial mutation.
func (n *localNode) DeleteGroup(
	ctx context.Context, code string, caller domain.Caller,
) error {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()

	group, ok, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return fmt.Errorf("read group for delete: %w", err)
	}
	if !ok {
		return fmt.Errorf("group %q: %w", code, domain.ErrNotFound)
	}
	members, err := n.realm.ListGroupAccounts(ctx, code)
	if err != nil {
		return fmt.Errorf("list group accounts for delete: %w", err)
	}
	if err := n.guardGroupDeleteCurrencyChange(ctx, code); err != nil {
		return err
	}
	haltedAccounts := haltedAccountsForGroupDeletion(members)
	eng := n.currentEngine()
	resolver, err := requireDictionaryResolver(eng)
	if err != nil {
		return err
	}
	spotFundsLimits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		return fmt.Errorf("list group policies for delete: %w", err)
	}
	hasGroupSpotFundsBarrier := false
	for _, limit := range spotFundsLimits {
		if limit.Scope == domain.ScopeAccountGroup && limit.AccountGroup == code {
			hasGroupSpotFundsBarrier = true
			break
		}
	}

	memberIDs := make([]domain.AccountID, 0, len(members))
	for _, account := range members {
		memberIDs = append(memberIDs, account.Code)
	}

	revertRuntime := func(
		mutationCtx context.Context,
		currencyTouched bool,
		membershipTouched bool,
		blockTouched bool,
	) error {
		var revertErr error
		if currencyTouched {
			err := eng.RunGroupSynchronized(
				mutationCtx, code, func(lane engine.GroupLane) error {
					return applyGroupCurrency(mutationCtx, lane, code, group.Currency)
				},
			)
			revertErr = errors.Join(
				revertErr, optionalOperationError("restore group currency", err),
			)
		}
		if membershipTouched && len(memberIDs) != 0 {
			err := eng.RunGroupSynchronized(
				mutationCtx, code, func(lane engine.GroupLane) error {
					return lane.RegisterGroup(mutationCtx, memberIDs, code)
				},
			)
			revertErr = errors.Join(revertErr, optionalOperationError("restore group members", err))
		}
		if blockTouched {
			err := eng.RunGroupSynchronized(
				mutationCtx, code, func(lane engine.GroupLane) error {
					return lane.BlockGroup(mutationCtx, code, group.BlockReason)
				},
			)
			revertErr = errors.Join(revertErr, optionalOperationError("restore group block", err))
		}
		return revertErr
	}
	failRuntime := func(
		cause error,
		currencyTouched bool,
		membershipTouched bool,
		blockTouched bool,
	) error {
		mutationCtx := context.WithoutCancel(ctx)
		revertErr := revertRuntime(
			mutationCtx, currencyTouched, membershipTouched, blockTouched,
		)
		if revertErr == nil {
			return cause
		}
		return n.reconcileEngineAfterFailure(
			mutationCtx,
			"reconcile engine after group delete failure",
			errors.Join(cause, revertErr),
		)
	}

	currencyTouched := true
	err = eng.RunGroupSynchronized(ctx, code, func(lane engine.GroupLane) error {
		return lane.ClearGroupCurrency(ctx, code)
	})
	if err != nil {
		return failRuntime(
			fmt.Errorf("clear deleted group currency: %w", err), true, false, false,
		)
	}

	membershipRemoved := false
	if len(memberIDs) != 0 {
		err = eng.RunGroupSynchronized(ctx, code, func(lane engine.GroupLane) error {
			return lane.UnregisterGroup(ctx, memberIDs, code)
		})
		if err != nil {
			return failRuntime(
				fmt.Errorf("unregister deleted group members: %w", err),
				currencyTouched, true, false,
			)
		}
		membershipRemoved = true
	}

	restateBlocks, restateErr := n.restateHaltedAccountPnlsRuntime(
		ctx, eng, haltedAccounts,
	)
	if restateErr != nil {
		mutationCtx := context.WithoutCancel(ctx)
		revertErr := revertRuntime(
			mutationCtx, currencyTouched, membershipRemoved, false,
		)
		return n.reconcileEngineAfterFailure(
			mutationCtx,
			"reconcile engine after group delete pnl restatement failure",
			errors.Join(
				fmt.Errorf("restate halted account pnl after group delete: %w", restateErr),
				revertErr,
			),
		)
	}
	if err := n.mirrorPolicyConfigurationBlocks(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, restateBlocks,
	); err != nil {
		return err
	}
	if err := n.persistHaltedAccountPnlRestatements(
		context.WithoutCancel(ctx), haltedAccounts,
	); err != nil {
		return n.fatalPostEngineAuditByCode(
			"persist halted account pnl after group delete",
			"group",
			code,
			fmt.Errorf("persist halted account pnl: %w", err),
		)
	}

	unblocked := false
	if group.Blocked {
		err = eng.RunGroupSynchronized(ctx, code, func(lane engine.GroupLane) error {
			return lane.UnblockGroup(ctx, code)
		})
		if err != nil {
			return failRuntime(
				fmt.Errorf("unblock deleted group: %w", err),
				currencyTouched, membershipRemoved, true,
			)
		}
		unblocked = true
	}

	if err := n.realm.DeleteGroup(ctx, code); err != nil {
		return failRuntime(
			fmt.Errorf("delete group: %w", err),
			currencyTouched, membershipRemoved, unblocked,
		)
	}
	if err := resolver.RemoveGroupResolverEntry(group); err != nil {
		mutationCtx := context.WithoutCancel(ctx)
		return n.reconcileEngineAfterFailure(
			mutationCtx,
			"reconcile engine after group resolver removal failure",
			fmt.Errorf("remove group resolver entry: %w", err),
		)
	}
	if hasGroupSpotFundsBarrier {
		result, configureErr := n.reconfigurePolicy(
			ctx, domain.PolicySpotFundsPnlBoundsKillSwitch,
		)
		if configureErr != nil {
			mutationCtx := context.WithoutCancel(ctx)
			return n.reconcileEngineAfterFailure(
				mutationCtx,
				"reconcile engine after group policy delete failure",
				fmt.Errorf("configure policies after group delete: %w", configureErr),
			)
		}
		if err := n.mirrorPolicyConfigurationBlocks(
			context.WithoutCancel(ctx),
			domain.PolicySpotFundsPnlBoundsKillSwitch,
			result.AccountBlocks,
		); err != nil {
			return err
		}
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action: domain.AuditActionDeleteGroup,
		Group:  code,
		Detail: fmt.Sprintf("delete group %s", code),
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit delete group", "group", code,
			fmt.Errorf("audit delete group: %w", err),
		)
	}
	return nil
}
