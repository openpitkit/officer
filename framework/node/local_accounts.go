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
	"go.openpit.dev/openpit/param"

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
	resolver := eng

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
			).err
		}
		return domain.Account{}, internalPostCommitNodeMutationError(resultErr)
	}

	var initialCurrency *string
	if created.Currency != "" {
		initialCurrency = &created.Currency
	}
	var initialBlock *accountRuntimeBlock
	if created.Blocked {
		initialBlock = &accountRuntimeBlock{
			blocked: true,
			reason:  created.BlockReason,
		}
	}
	_, engineApplied, applyErr := n.runAccountRuntimeChain(
		ctx,
		eng,
		created,
		"apply initial account runtime state",
		initialCurrency,
		nil,
		initialBlock,
	)
	if applyErr == nil && created.GroupCode != "" {
		groupApplied, groupErr := n.applyGroupMove(
			ctx, eng, created, "", created.GroupCode, nil,
		)
		engineApplied = engineApplied || groupApplied
		applyErr = groupErr
	}
	if applyErr != nil {
		if !engineApplied {
			return domain.Account{}, applyErr
		}
		// Account retirement is not available in the SDK yet. Once the resolver
		// alias was published, a failed initial runtime mutation is reconciled by
		// deleting the store row and rebuilding from the surviving snapshot.
		mutationCtx := context.WithoutCancel(ctx)
		rollbackErr := n.realm.DeleteAccount(mutationCtx, created.Code, true)
		cause := errors.Join(
			fmt.Errorf("apply created account runtime state: %w", applyErr),
			optionalOperationError("rollback created account", rollbackErr),
		)
		return domain.Account{}, internalPostCommitNodeMutationError(
			n.reconcileEngineAfterFailure(
				mutationCtx, "reconcile engine after account create failure", cause,
			).err,
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

type administrativeChainState struct {
	ctx                  context.Context
	err                  error
	engineApplied        bool
	persistenceCompleted bool
	failureOperation     string
}

func (n *localNode) administrativeChainTerminalError(
	operation string,
	subjectKind string,
	subjectCode string,
	state *administrativeChainState,
	outcome asyncengine.ChainOutcome,
) error {
	if !state.engineApplied || outcome.Err == nil ||
		errors.Is(state.err, domain.ErrNoChange) {
		return state.err
	}
	switch state.err.(type) {
	case internalPostCommitNodeMutationFailure,
		*internalPostCommitNodeMutationFailure:
		return state.err
	}
	failureOperation := state.failureOperation
	if failureOperation == "" {
		failureOperation = operation
		if state.persistenceCompleted {
			failureOperation = "audit " + operation
		}
	}
	return n.fatalPostEngineAuditByCode(
		failureOperation,
		subjectKind,
		subjectCode,
		chainRootCause(outcome.Err),
	)
}

func isInternalPostCommitNodeMutation(err error) bool {
	var value internalPostCommitNodeMutationFailure
	if errors.As(err, &value) {
		return true
	}
	var pointer *internalPostCommitNodeMutationFailure
	return errors.As(err, &pointer)
}

func (n *localNode) reconcileAdministrativeChainFailure(
	ctx context.Context,
	operation string,
	err error,
) error {
	if !administrativeChainNeedsReconciliation(err) {
		return err
	}
	return internalPostCommitNodeMutationError(
		n.reconcileEngineAfterFailure(
			context.WithoutCancel(ctx),
			"reconcile engine after "+operation+" failure",
			err,
		).err,
	)
}

func administrativeChainNeedsReconciliation(err error) bool {
	return err != nil &&
		!isInternalPostCommitNodeMutation(err) &&
		errors.Is(err, asyncengine.ErrChainRetryUnsafe)
}

func validateAccountAdministrativeSource(
	account domain.Account,
	source param.AccountID,
) error {
	if err := domain.ValidateEngineAccountID(account.EngineAccountID); err != nil {
		return err
	}
	if account.EngineAccountID.Uint64() != uint64(source.Handle()) {
		return fmt.Errorf(
			"account %q engine identity does not match the live resolver: %w",
			account.Code,
			domain.ErrInvalid,
		)
	}
	return nil
}

func validateGroupAdministrativeSource(
	group domain.AccountGroup,
	source param.AccountGroupID,
) error {
	if group.Code == "" && group.EngineGroupID == 0 {
		if source == param.DefaultAccountGroup {
			return nil
		}
		return fmt.Errorf(
			"default group does not match the live resolver: %w",
			domain.ErrInvalid,
		)
	}
	if err := domain.ValidateEngineGroupID(group.EngineGroupID); err != nil {
		return err
	}
	if uint32(group.EngineGroupID) != uint32(source.Handle()) {
		return fmt.Errorf(
			"group %q engine identity does not match the live resolver: %w",
			group.Code,
			domain.ErrInvalid,
		)
	}
	return nil
}

func administrativeGroupSource(
	resolver engine.DictionaryResolver,
	group domain.AccountGroup,
) (param.AccountGroupID, error) {
	if group.Code == "" {
		if group.EngineGroupID != 0 {
			return param.AccountGroupID{}, fmt.Errorf(
				"default group has an invalid engine identity: %w",
				domain.ErrInvalid,
			)
		}
		return param.DefaultAccountGroup, nil
	}
	if err := domain.ValidateEngineGroupID(group.EngineGroupID); err != nil {
		return param.AccountGroupID{}, fmt.Errorf(
			"group %q engine id: %w", group.Code, err,
		)
	}
	source, err := resolver.ResolveGroup(group.Code)
	if err != nil {
		return param.AccountGroupID{}, fmt.Errorf(
			"resolve group %q from the live dictionary: %w",
			group.Code,
			err,
		)
	}
	if err := validateGroupAdministrativeSource(group, source); err != nil {
		return param.AccountGroupID{}, err
	}
	return source, nil
}

type accountBlockChainState struct {
	administrativeChainState
	previous domain.Account
	blocked  bool
	reason   string
}

type accountGroupMembershipChainState struct {
	administrativeChainState
	previous domain.Account
}

// SetAccountBlocked blocks or unblocks an account through one SDK account
// chain. The final engine hook persists the store state and audit row before
// the lane is released. missing decides whether a kill-switch aimed at an
// account Officer does not know yet registers it first or is rejected.
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
	eng, endLane, err := n.beginLane()
	if err != nil {
		return err
	}
	laneHeld := true
	defer func() {
		if laneHeld {
			endLane()
		}
	}()

	if _, ok, readErr := n.realm.GetAccount(ctx, key.Account); readErr != nil {
		return fmt.Errorf("read account for block: %w", readErr)
	} else if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}
	source, err := eng.AccountID(key.Account)
	if err != nil {
		return err
	}
	state := &accountBlockChainState{
		administrativeChainState: administrativeChainState{ctx: ctx},
		blocked:                  blocked,
		reason:                   reason,
	}
	begin := func(context.Context) (*accountBlockChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = fmt.Errorf("engine: account block cancelled: %w", ctxErr)
			return nil, state.err
		}
		previous, ok, readErr := n.realm.GetAccount(state.ctx, key.Account)
		if readErr != nil {
			state.err = fmt.Errorf("read account for block: %w", readErr)
			return nil, state.err
		}
		if !ok {
			state.err = fmt.Errorf(
				"account %q: %w", key.Account, domain.ErrNotFound,
			)
			return nil, state.err
		}
		if validateErr := validateAccountAdministrativeSource(
			previous, source,
		); validateErr != nil {
			state.err = fmt.Errorf("read account for block: %w", validateErr)
			return nil, state.err
		}
		state.previous = previous
		state.failureOperation = "apply account block"
		return state, nil
	}
	persist := func(state *accountBlockChainState) error {
		state.engineApplied = true
		mutationCtx := context.WithoutCancel(state.ctx)
		state.failureOperation = "set account blocked"
		if err := n.realm.SetAccountBlocked(
			mutationCtx, key.Account, state.blocked, state.reason,
		); err != nil {
			state.err = fmt.Errorf("set account blocked: %w", err)
			return state.err
		}
		state.persistenceCompleted = true
		action := domain.AuditActionBlock
		detail := blockDetail(key.Account, state.reason)
		if !state.blocked {
			action = domain.AuditActionUnblock
			detail = unblockDetail(key.Account, state.reason)
		}
		state.failureOperation = "audit account block"
		if err := n.audit(mutationCtx, caller, store.AuditEntry{
			Action:       action,
			Account:      key.Account,
			AccountTitle: state.previous.Title,
			Detail:       detail,
		}); err != nil {
			state.err = fmt.Errorf("audit account block: %w", err)
			return state.err
		}
		return nil
	}
	builder := asyncengine.Chain(source, begin)
	if blocked {
		builder.UnblockAccount(asyncengine.UnblockHooks[*accountBlockChainState]{
			OnUnblocked: func(
				_ context.Context, state *accountBlockChainState,
			) error {
				state.engineApplied = true
				return nil
			},
		})
		builder.BlockAccount(asyncengine.BlockHooks[*accountBlockChainState]{
			Reason: func(
				_ context.Context, state *accountBlockChainState,
			) (string, error) {
				return state.reason, nil
			},
			OnBlocked: func(
				_ context.Context, state *accountBlockChainState,
			) error {
				return persist(state)
			},
		})
	} else {
		builder.UnblockAccount(asyncengine.UnblockHooks[*accountBlockChainState]{
			OnUnblocked: func(
				_ context.Context, state *accountBlockChainState,
			) error {
				return persist(state)
			},
		})
	}
	runner := builder.Finally(func(
		_ context.Context,
		state *accountBlockChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.err = n.administrativeChainTerminalError(
			"account block",
			"account",
			key.Account.String(),
			&state.administrativeChainState,
			outcome,
		)
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	runErr := accountChainRunError("account block", state.err, chainErr)
	if administrativeChainNeedsReconciliation(runErr) {
		// Reserve restart ownership before releasing the admitted account lane.
		// beginEngineRestartFromLane consumes endLane on both outcomes.
		if restartErr := n.beginEngineRestartFromLane(endLane); restartErr != nil {
			laneHeld = false
			return internalPostCommitNodeMutationError(
				errors.Join(runErr, restartErr),
			)
		}
		laneHeld = false
		defer n.endEngineRestart()
	}
	return n.reconcileAdministrativeChainFailure(ctx, "account block", runErr)
}

// blockOperation names the triggering operation recorded on an account created
// by a kill-switch request.
func blockOperation(blocked bool) string {
	if blocked {
		return "block account"
	}
	return "unblock account"
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

// SetAccountGroup sets or clears an account's group through SDK membership
// chains, unregistering the old group before registering the new. The final
// membership hook persists the store link and audit rows.
//
// A brand-new target group is persisted and published into the live resolver
// under the identity gate. Membership chains are sourced from the concrete
// group but route through their first account. missing decides whether an
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
	resolver := eng
	if groupCode != "" {
		if _, _, err := n.ensureGroupRegisteredLocked(ctx, groupCode, resolver); err != nil {
			return fmt.Errorf("ensure group for set: %w", err)
		}
	}

	previous, ok, err := n.realm.GetAccount(ctx, key.Account)
	if err != nil {
		return fmt.Errorf("read account for set group: %w", err)
	}
	if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}
	if previous.GroupCode == groupCode {
		return nil
	}
	routingAccount, err := eng.AccountID(key.Account)
	if err != nil {
		return err
	}
	if err := validateAccountAdministrativeSource(previous, routingAccount); err != nil {
		return fmt.Errorf("read account for set group: %w", err)
	}
	accountIDs := []param.AccountID{routingAccount}

	groupSources := make(map[string]param.AccountGroupID, 2)
	for _, code := range accountGroupAuditCodes(previous.GroupCode, groupCode) {
		group, found, readErr := n.realm.GetGroup(ctx, code)
		if readErr != nil {
			return fmt.Errorf("read group for set account group: %w", readErr)
		}
		if !found {
			return fmt.Errorf("group %q: %w", code, domain.ErrNotFound)
		}
		groupSource, sourceErr := administrativeGroupSource(resolver, group)
		if sourceErr != nil {
			return sourceErr
		}
		groupSources[code] = groupSource
	}

	persist := func(state *accountGroupMembershipChainState) error {
		state.engineApplied = true
		mutationCtx := context.WithoutCancel(state.ctx)
		state.failureOperation = "set account group"
		if err := n.realm.SetAccountGroup(
			mutationCtx, key.Account, groupCode,
		); err != nil {
			state.err = fmt.Errorf("set account group: %w", err)
			return state.err
		}
		state.persistenceCompleted = true
		detail := setAccountGroupDetail(
			key.Account, state.previous.GroupCode, groupCode,
		)
		groupCodes := accountGroupAuditCodes(
			state.previous.GroupCode, groupCode,
		)
		entries := make([]store.AuditEntry, 0, len(groupCodes))
		for _, code := range groupCodes {
			entries = append(entries, store.AuditEntry{
				Action:       domain.AuditActionSetGroup,
				Account:      key.Account,
				AccountTitle: state.previous.Title,
				Group:        code,
				Detail:       detail,
			})
		}
		state.failureOperation = "audit set account group"
		if err := n.auditBatch(mutationCtx, caller, entries); err != nil {
			state.err = fmt.Errorf("audit set account group: %w", err)
			return state.err
		}
		return nil
	}
	runMembership := func(
		sourceCode string,
		register bool,
		completeStore bool,
	) (*accountGroupMembershipChainState, error) {
		source := groupSources[sourceCode]
		state := &accountGroupMembershipChainState{
			administrativeChainState: administrativeChainState{ctx: ctx},
		}
		begin := func(context.Context) (*accountGroupMembershipChainState, error) {
			if ctxErr := state.ctx.Err(); ctxErr != nil {
				state.err = fmt.Errorf(
					"engine: set account group cancelled: %w", ctxErr,
				)
				return nil, state.err
			}
			current, found, readErr := n.realm.GetAccount(
				state.ctx, key.Account,
			)
			if readErr != nil {
				state.err = fmt.Errorf(
					"read account for set group: %w", readErr,
				)
				return nil, state.err
			}
			if !found {
				state.err = fmt.Errorf(
					"account %q: %w", key.Account, domain.ErrNotFound,
				)
				return nil, state.err
			}
			if current.GroupCode != previous.GroupCode {
				state.err = fmt.Errorf(
					"account %q group changed from %q to %q: %w",
					key.Account,
					previous.GroupCode,
					current.GroupCode,
					domain.ErrInvalid,
				)
				return nil, state.err
			}
			if validateErr := validateAccountAdministrativeSource(
				current, routingAccount,
			); validateErr != nil {
				state.err = fmt.Errorf(
					"read account for set group: %w", validateErr,
				)
				return nil, state.err
			}
			group, found, readErr := n.realm.GetGroup(state.ctx, sourceCode)
			if readErr != nil {
				state.err = fmt.Errorf(
					"read group for set account group: %w", readErr,
				)
				return nil, state.err
			}
			if !found {
				state.err = fmt.Errorf(
					"group %q: %w", sourceCode, domain.ErrNotFound,
				)
				return nil, state.err
			}
			if validateErr := validateGroupAdministrativeSource(
				group, source,
			); validateErr != nil {
				state.err = fmt.Errorf(
					"read group for set account group: %w", validateErr,
				)
				return nil, state.err
			}
			if current.Currency == "" {
				nextGroupCurrency := ""
				if groupCode != "" {
					target, targetFound, targetErr := n.realm.GetGroup(
						state.ctx, groupCode,
					)
					if targetErr != nil {
						state.err = fmt.Errorf(
							"read target group for currency: %w", targetErr,
						)
						return nil, state.err
					}
					if !targetFound {
						state.err = fmt.Errorf(
							"group %q: %w", groupCode, domain.ErrNotFound,
						)
						return nil, state.err
					}
					if validateErr := validateGroupAdministrativeSource(
						target, groupSources[groupCode],
					); validateErr != nil {
						state.err = fmt.Errorf(
							"read target group for currency: %w", validateErr,
						)
						return nil, state.err
					}
					nextGroupCurrency = target.Currency
				}
				nextEffective, _ := domain.ResolveCurrencyCascade(
					"", nextGroupCurrency, current.DefaultCurrency,
				)
				if guardErr := n.guardEffectiveCurrencyChange(
					state.ctx,
					[]domain.AccountID{key.Account},
					current.EffectiveCurrency,
					nextEffective,
				); guardErr != nil {
					if !errors.Is(guardErr, domain.ErrConflict) {
						state.err = guardErr
						return nil, state.err
					}
					state.err = domain.NewCurrencyChangeBlockedError(
						domain.ScopeAccount, key.Account.String(), guardErr,
					)
					return nil, state.err
				}
			}
			state.previous = current
			state.failureOperation = "apply account group"
			return state, nil
		}
		builder := asyncengine.Chain(source, begin)
		if register {
			builder.RegisterAccountGroup(
				accountIDs,
				asyncengine.RegisterAccountGroupHooks[*accountGroupMembershipChainState]{
					OnRegistered: func(
						_ context.Context,
						state *accountGroupMembershipChainState,
					) error {
						state.engineApplied = true
						if completeStore {
							return persist(state)
						}
						return nil
					},
				},
			)
		} else {
			builder.UnregisterAccountGroup(
				accountIDs,
				asyncengine.UnregisterAccountGroupHooks[*accountGroupMembershipChainState]{
					OnUnregistered: func(
						_ context.Context,
						state *accountGroupMembershipChainState,
					) error {
						state.engineApplied = true
						if completeStore {
							return persist(state)
						}
						return nil
					},
				},
			)
		}
		runner := builder.Finally(func(
			_ context.Context,
			state *accountGroupMembershipChainState,
			outcome asyncengine.ChainOutcome,
		) error {
			state.err = n.administrativeChainTerminalError(
				"set account group",
				"account",
				key.Account.String(),
				&state.administrativeChainState,
				outcome,
			)
			return nil
		})
		_, chainErr := runner.Run(
			ctx, eng.AsyncEngine(),
		).Await(context.Background())
		return state, accountChainRunError(
			"set account group", state.err, chainErr,
		)
	}

	mutationApplied := false
	if previous.GroupCode != "" {
		state, runErr := runMembership(
			previous.GroupCode, false, groupCode == "",
		)
		mutationApplied = state.engineApplied
		if runErr != nil {
			if isInternalPostCommitNodeMutation(runErr) {
				return runErr
			}
			if !errors.Is(runErr, asyncengine.ErrChainRetryUnsafe) {
				return runErr
			}
			return internalPostCommitNodeMutationError(
				n.reconcileEngineAfterFailure(
					context.WithoutCancel(ctx),
					"reconcile engine after account group failure",
					runErr,
				).err,
			)
		}
	}
	if groupCode == "" {
		return nil
	}
	_, runErr := runMembership(groupCode, true, true)
	if runErr == nil {
		return nil
	}
	if isInternalPostCommitNodeMutation(runErr) {
		return runErr
	}
	if !mutationApplied && !errors.Is(runErr, asyncengine.ErrChainRetryUnsafe) {
		return runErr
	}
	if previous.GroupCode != "" {
		_, compensationErr := runMembership(
			previous.GroupCode, true, false,
		)
		if compensationErr == nil {
			return runErr
		}
		runErr = errors.Join(
			runErr,
			fmt.Errorf(
				"restore previous account group: %w",
				compensationErr,
			),
		)
	}
	return internalPostCommitNodeMutationError(
		n.reconcileEngineAfterFailure(
			context.WithoutCancel(ctx),
			"reconcile engine after account group failure",
			runErr,
		).err,
	)
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
	resolver := n.currentEngine()
	err = resolver.RenameAccountResolverEntry(prev.Code, updated)
	if err != nil {
		mutationCtx := context.WithoutCancel(ctx)
		_, rollbackErr := n.realm.UpdateAccount(mutationCtx, updated.Code, prev)
		if rollbackErr != nil {
			cause := errors.Join(
				fmt.Errorf("publish account resolver rename: %w", err),
				fmt.Errorf("rollback account rename: %w", rollbackErr),
			)
			return domain.Account{}, internalPostCommitNodeMutationError(
				n.reconcileEngineAfterFailure(
					mutationCtx, "reconcile engine after account rename failure", cause,
				).err,
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
	durableCtx := context.WithoutCancel(ctx)
	if err := n.realm.DeleteAccount(durableCtx, account.Code, force); err != nil {
		next.Stop()
		return fmt.Errorf("delete account: %w", err)
	}
	prev := n.swapEngine(next)
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
