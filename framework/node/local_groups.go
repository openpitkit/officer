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

	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// --- groups -----------------------------------------------------------------

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
	resolver := eng

	created, err := n.realm.CreateGroup(ctx, group)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("create group: %w", err)
	}
	if err := resolver.AddGroupResolverEntry(created); err != nil {
		rollbackErr := n.realm.DeleteGroup(
			context.WithoutCancel(ctx), created.Code, false,
		)
		resultErr := errors.Join(
			fmt.Errorf("publish group resolver entry: %w", err),
			optionalOperationError("rollback created group", rollbackErr),
		)
		if rollbackErr != nil {
			resultErr = n.reconcileEngineAfterFailure(
				context.WithoutCancel(ctx),
				"reconcile engine after group rollback failure",
				resultErr,
			).err
		}
		return domain.AccountGroup{}, internalPostCommitNodeMutationError(resultErr)
	}
	if created.Currency != "" || created.Blocked {
		var initialCurrency *string
		if created.Currency != "" {
			initialCurrency = &created.Currency
		}
		var initialBlock *groupRuntimeBlock
		if created.Blocked {
			initialBlock = &groupRuntimeBlock{
				blocked: true,
				reason:  created.BlockReason,
			}
		}
		engineApplied, chainErr := n.runGroupRuntimeChain(
			ctx,
			eng,
			created,
			true,
			"apply initial group runtime state",
			initialCurrency,
			initialBlock,
		)
		err = chainErr
		if err != nil {
			if !engineApplied {
				mutationCtx := context.WithoutCancel(ctx)
				resolverErr := resolver.RemoveGroupResolverEntry(created)
				rollbackErr := n.realm.DeleteGroup(
					mutationCtx, created.Code, false,
				)
				if resolverErr == nil && rollbackErr == nil {
					return domain.AccountGroup{}, err
				}
				cause := errors.Join(
					err,
					optionalOperationError(
						"rollback created group resolver", resolverErr,
					),
					optionalOperationError("rollback created group", rollbackErr),
				)
				return domain.AccountGroup{}, internalPostCommitNodeMutationError(
					n.reconcileEngineAfterFailure(
						mutationCtx,
						"reconcile engine after group create compensation failure",
						cause,
					).err,
				)
			}
			mutationCtx := context.WithoutCancel(ctx)
			resolverErr := resolver.RemoveGroupResolverEntry(created)
			rollbackErr := n.realm.DeleteGroup(mutationCtx, created.Code, false)
			// A failed SDK call may have mutated before returning. Rebuild even
			// when resolver/store compensation succeeded so no inaccessible group
			// block state survives on the old engine.
			cause := errors.Join(
				fmt.Errorf("apply created group runtime state: %w", err),
				optionalOperationError("rollback created group resolver", resolverErr),
				optionalOperationError("rollback created group", rollbackErr),
			)
			return domain.AccountGroup{}, internalPostCommitNodeMutationError(
				n.reconcileEngineAfterFailure(
					mutationCtx, "reconcile engine after group create failure", cause,
				).err,
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

	resolver := n.currentEngine()
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
	resolver := n.currentEngine()
	err = resolver.RenameGroupResolverEntry(prev.Code, updated)
	if err != nil {
		mutationCtx := context.WithoutCancel(ctx)
		_, rollbackErr := n.realm.UpdateGroup(mutationCtx, updated.Code, prev)
		if rollbackErr != nil {
			cause := errors.Join(
				fmt.Errorf("publish group resolver rename: %w", err),
				fmt.Errorf("rollback group rename: %w", rollbackErr),
			)
			return domain.AccountGroup{}, internalPostCommitNodeMutationError(
				n.reconcileEngineAfterFailure(
					mutationCtx, "reconcile engine after group rename failure", cause,
				).err,
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

// SetGroupBlocked blocks or unblocks a group through a group-sourced SDK chain.
// Its final engine hook persists the store state and audit row. If no group
// record exists yet (e.g. the group is known only via account membership), a
// default record is created first so the operation succeeds.
//
// The block spans every member account, so the live identity gate quiesces all
// admitted account lanes. The SDK chain itself routes on the group's synthetic
// administrative key; the gate is not a replacement lane.
func (n *localNode) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string, caller domain.Caller,
) error {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()

	eng := n.currentEngine()
	resolver := eng
	prev, _, err := n.ensureGroupRegisteredLocked(ctx, code, resolver)
	if err != nil {
		return fmt.Errorf("read group for block: %w", err)
	}
	source, err := administrativeGroupSource(resolver, prev)
	if err != nil {
		return err
	}
	if err := validateGroupAdministrativeSource(prev, source); err != nil {
		return fmt.Errorf("read group for block: %w", err)
	}
	type groupBlockChainState struct {
		administrativeChainState
		previous domain.AccountGroup
	}
	state := &groupBlockChainState{
		administrativeChainState: administrativeChainState{ctx: ctx},
	}
	begin := func(context.Context) (*groupBlockChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = fmt.Errorf("engine: group block cancelled: %w", ctxErr)
			return nil, state.err
		}
		previous, ok, readErr := n.realm.GetGroup(state.ctx, code)
		if readErr != nil {
			state.err = fmt.Errorf("read group for block: %w", readErr)
			return nil, state.err
		}
		if !ok {
			state.err = fmt.Errorf("group %q: %w", code, domain.ErrNotFound)
			return nil, state.err
		}
		if validateErr := validateGroupAdministrativeSource(
			previous, source,
		); validateErr != nil {
			state.err = fmt.Errorf("read group for block: %w", validateErr)
			return nil, state.err
		}
		state.previous = previous
		state.failureOperation = "apply group block"
		return state, nil
	}
	persist := func(state *groupBlockChainState) error {
		state.engineApplied = true
		mutationCtx := context.WithoutCancel(state.ctx)
		state.failureOperation = "set group blocked"
		if err := n.realm.SetGroupBlocked(
			mutationCtx, code, blocked, reason,
		); err != nil {
			state.err = fmt.Errorf("set group blocked: %w", err)
			return state.err
		}
		state.persistenceCompleted = true
		action := domain.AuditActionBlockGroup
		detail := blockGroupDetail(code, reason)
		if !blocked {
			action = domain.AuditActionUnblockGroup
			detail = unblockGroupDetail(code, reason)
		}
		state.failureOperation = "audit group block"
		if err := n.audit(mutationCtx, caller, store.AuditEntry{
			Action: action,
			Group:  code,
			Detail: detail,
		}); err != nil {
			state.err = fmt.Errorf("audit group block: %w", err)
			return state.err
		}
		return nil
	}
	builder := asyncengine.Chain(source, begin)
	if blocked {
		// The live identity gate held by SetGroupBlocked quiesces every member
		// account lane, so the unblock needed to replace the reason cannot expose
		// an order to a temporarily unblocked group tier.
		builder.UnblockAccountGroup(
			asyncengine.UnblockHooks[*groupBlockChainState]{
				OnUnblocked: func(
					_ context.Context, state *groupBlockChainState,
				) error {
					state.engineApplied = true
					return nil
				},
			},
		)
		builder.BlockAccountGroup(
			asyncengine.BlockHooks[*groupBlockChainState]{
				Reason: func(
					_ context.Context, _ *groupBlockChainState,
				) (string, error) {
					return reason, nil
				},
				OnBlocked: func(
					_ context.Context, state *groupBlockChainState,
				) error {
					return persist(state)
				},
			},
		)
	} else {
		builder.UnblockAccountGroup(
			asyncengine.UnblockHooks[*groupBlockChainState]{
				OnUnblocked: func(
					_ context.Context, state *groupBlockChainState,
				) error {
					return persist(state)
				},
			},
		)
	}
	runner := builder.Finally(func(
		_ context.Context,
		state *groupBlockChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.err = n.administrativeChainTerminalError(
			"group block",
			"group",
			code,
			&state.administrativeChainState,
			outcome,
		)
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	if state.err == nil && chainErr != nil {
		operation := "block group"
		if !blocked {
			operation = "unblock group"
		}
		chainErr = groupAdministrativeBlockError(operation, code, chainErr)
	}
	runErr := accountChainRunError("group block", state.err, chainErr)
	needsReconciliation := administrativeChainNeedsReconciliation(runErr)
	if needsReconciliation && state.engineApplied {
		return n.reconcileAdministrativeChainFailure(
			ctx, "group block", runErr,
		)
	}
	var blockErr *reject.AccountBlockError
	if errors.As(runErr, &blockErr) {
		return runErr
	}
	return n.reconcileAdministrativeChainFailure(ctx, "group block", runErr)
}

func groupAdministrativeBlockError(operation, group string, err error) error {
	var blockErr *reject.AccountBlockError
	if errors.As(err, &blockErr) &&
		blockErr.Kind == reject.AccountBlockErrorKindReservedGroup {
		return fmt.Errorf(
			"engine: %s %q: %w: %w",
			operation,
			group,
			err,
			domain.ErrReservedGroup,
		)
	}
	return err
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
		rollbackErr := n.realm.DeleteGroup(
			context.WithoutCancel(ctx), group.Code, false,
		)
		resultErr := errors.Join(
			fmt.Errorf("publish group resolver entry: %w", err),
			optionalOperationError("rollback ensured group", rollbackErr),
		)
		if rollbackErr != nil {
			resultErr = n.reconcileEngineAfterFailure(
				context.WithoutCancel(ctx),
				"reconcile engine after group ensure rollback failure",
				resultErr,
			).err
		}
		return domain.AccountGroup{}, false,
			internalPostCommitNodeMutationError(resultErr)
	}
	return group, true, nil
}

// DeleteGroup removes the group, detaches its member accounts, rebuilds the
// engine from the resulting snapshot, and audits the action. Account-owned
// trading and compliance history remains intact.
func (n *localNode) DeleteGroup(
	ctx context.Context, code string, force bool, caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	_, ok, err := n.realm.GetGroup(ctx, code)
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
	if err := n.guardGroupDeleteCurrencyChange(ctx, members); err != nil {
		if !errors.Is(err, domain.ErrConflict) {
			return err
		}
		return domain.NewCurrencyChangeBlockedError(
			domain.ScopeAccountGroup, code, err,
		)
	}

	snapshot, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load snapshot for group delete: %w", err)
	}
	destroyedPnlBounds := 0
	for _, limit := range snapshot.SpotFundsPnlBoundsLimits {
		if isGroupSpotFundsPnlBoundsLimit(limit, code) {
			destroyedPnlBounds++
		}
	}
	snapshot = snapshotWithoutGroup(snapshot, code)
	previousEngine := n.currentEngine()
	next, err := n.build(snapshot)
	if err != nil {
		return fmt.Errorf("build engine for group delete: %w", err)
	}
	if next == nil {
		return fmt.Errorf("build engine for group delete returned nil")
	}
	if next == previousEngine {
		return fmt.Errorf("build engine for group delete returned current engine")
	}
	durableCtx := context.WithoutCancel(ctx)
	if err := n.realm.DeleteGroup(durableCtx, code, force); err != nil {
		next.Stop()
		return fmt.Errorf("delete group: %w", err)
	}
	prev := n.swapEngine(next)
	if prev != nil && prev != next {
		prev.Stop()
	}
	if err := n.mirrorSeedAccountBlocks(durableCtx, next); err != nil {
		return n.fatalPostEngineAuditByCode(
			"mirror group delete seed blocks", "group", code,
			fmt.Errorf("mirror group delete seed blocks: %w", err),
		)
	}
	// The group row carries counts rather than one unbounded identifier list;
	// the per-account rows carry the identities in their structured field.
	summary := store.AuditEntry{
		Action: domain.AuditActionDeleteGroup,
		Group:  code,
		Detail: fmt.Sprintf(
			"delete group %s; detachedAccounts=%d destroyedSpotFundsPnlBounds=%d",
			code,
			len(members),
			destroyedPnlBounds,
		),
	}
	if err := n.auditGroupDelete(
		durableCtx, caller, summary, code, members,
	); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit delete group", "group", code,
			fmt.Errorf("audit delete group: %w", err),
		)
	}
	return nil
}

// auditGroupDeleteChunk bounds one group-delete audit transaction. Membership
// has no upper bound, so a single batch would grow one store write transaction
// with the size of the largest group. It buys nothing on the exclusive restart
// gate: DeleteGroup holds that gate across every batch. Dropping the
// per-account rows past a cap is not an option instead: the structured account
// field is what keeps an account-filtered audit read complete.
const auditGroupDeleteChunk = 256

// auditGroupDelete files the deletion under every detached account and then,
// last, under the group. Each batch is atomic on its own, so a failure part way
// through leaves account rows without the summary that counts them - never a
// durable summary claiming detachments that were never filed. The caller
// escalates that failure, which is the same fatal path a single failed batch
// takes.
func (n *localNode) auditGroupDelete(
	ctx context.Context,
	caller domain.Caller,
	summary store.AuditEntry,
	code string,
	members []domain.Account,
) error {
	entries := make([]store.AuditEntry, 0, auditGroupDeleteChunk)
	for _, member := range members {
		entries = append(entries, store.AuditEntry{
			Action:  domain.AuditActionDeleteGroup,
			Account: member.Code,
			Group:   code,
			Detail: fmt.Sprintf(
				"delete group %s; detach account %s",
				code,
				member.Code,
			),
		})
		if len(entries) < auditGroupDeleteChunk {
			continue
		}
		if err := n.auditBatch(ctx, caller, entries); err != nil {
			return err
		}
		entries = entries[:0]
	}
	// The summary counts the per-account rows, so it lands after them: its
	// presence means the rows it counts are already durable.
	entries = append(entries, summary)
	return n.auditBatch(ctx, caller, entries)
}

func snapshotWithoutGroup(
	snapshot engine.Snapshot,
	group string,
) engine.Snapshot {
	for index := range snapshot.Accounts {
		account := &snapshot.Accounts[index]
		if account.GroupCode != group {
			continue
		}
		account.GroupCode = ""
		account.GroupCurrency = ""
		account.EffectiveCurrency, account.CurrencyOrigin =
			domain.ResolveCurrencyCascade(
				account.Currency, "", account.DefaultCurrency,
			)
	}
	snapshot.Groups = slices.DeleteFunc(
		snapshot.Groups,
		func(row domain.AccountGroup) bool { return row.Code == group },
	)
	snapshot.SpotFundsPnlBoundsLimits = slices.DeleteFunc(
		snapshot.SpotFundsPnlBoundsLimits,
		func(row domain.LimitSpotFundsPnlBounds) bool {
			return isGroupSpotFundsPnlBoundsLimit(row, group)
		},
	)
	return snapshot
}

func isGroupSpotFundsPnlBoundsLimit(
	limit domain.LimitSpotFundsPnlBounds, group string,
) bool {
	return limit.Scope == domain.ScopeAccountGroup && limit.AccountGroup == group
}
