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

	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

type accountRuntimePnl struct {
	value       string
	storedValue string
	haltReason  domain.PnlHaltReason
}

type accountRuntimeBlock struct {
	blocked bool
	cause   domain.AccountBlock
}

type restoredAccountBlockCauseMapper interface {
	RestoredAccountBlockCause(domain.AccountBlock) (reject.AccountBlock, error)
}

type accountRuntimeChainState struct {
	ctx           context.Context
	expected      domain.Account
	currencyAsset param.Asset
	pnlAssignment asyncengine.SpotFundsAccountPnlAssignment
	pnlBlocks     []domain.AccountBlock
	err           error
	engineApplied bool
}

func runtimeChainTerminalError(
	stateErr error,
	engineApplied bool,
	outcome asyncengine.ChainOutcome,
) (bool, error) {
	engineApplied = engineApplied ||
		errors.Is(outcome.Err, asyncengine.ErrChainRetryUnsafe)
	if !engineApplied {
		return false, stateErr
	}
	return true, chainRootCause(outcome.Err)
}

// runAccountRuntimeChain requires its caller to hold an exclusive lane gate
// whenever block requests a reason replacement. The SDK exposes that update as
// unblock followed by block; without the gate an order could pass between them.
func (n *localNode) runAccountRuntimeChain(
	ctx context.Context,
	eng engine.Engine,
	account domain.Account,
	operation string,
	currency *string,
	pnl *accountRuntimePnl,
	block *accountRuntimeBlock,
) ([]domain.AccountBlock, bool, error) {
	source, err := eng.AccountID(account.Code)
	if err != nil {
		return nil, false, err
	}
	if err := validateAccountAdministrativeSource(account, source); err != nil {
		return nil, false, err
	}
	var typedCause reject.AccountBlock
	typedBlock := block != nil && block.blocked && block.cause.Code != ""
	if typedBlock {
		mapper, ok := eng.(restoredAccountBlockCauseMapper)
		if !ok {
			return nil, false, fmt.Errorf(
				"engine: restore typed account block %q: adapter does not support typed causes",
				account.Code,
			)
		}
		typedCause, err = mapper.RestoredAccountBlockCause(block.cause)
		if err != nil {
			return nil, false, fmt.Errorf(
				"engine: restore typed account block %q with code %q: %w",
				account.Code, block.cause.Code, err,
			)
		}
	}
	state := &accountRuntimeChainState{
		ctx:      ctx,
		expected: account,
	}
	hasEngineOperation := currency != nil || pnl != nil || block != nil
	begin := func(context.Context) (*accountRuntimeChainState, error) {
		if !hasEngineOperation {
			return state, nil
		}
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = fmt.Errorf("engine: %s cancelled: %w", operation, ctxErr)
			return nil, state.err
		}
		current, found, readErr := n.realm.GetAccount(
			state.ctx, state.expected.Code,
		)
		if readErr != nil {
			state.err = fmt.Errorf(
				"read account for %s: %w", operation, readErr,
			)
			return nil, state.err
		}
		if !found {
			state.err = fmt.Errorf(
				"account %q: %w", state.expected.Code, domain.ErrNotFound,
			)
			return nil, state.err
		}
		if validateErr := validateAccountAdministrativeSource(
			current, source,
		); validateErr != nil {
			state.err = fmt.Errorf(
				"read account for %s: %w", operation, validateErr,
			)
			return nil, state.err
		}
		if currency != nil && current.Currency != *currency {
			state.err = fmt.Errorf(
				"account %q currency changed from %q to %q: %w",
				current.Code, *currency, current.Currency, domain.ErrInvalid,
			)
			return nil, state.err
		}
		if pnl != nil && (current.Pnl != pnl.storedValue ||
			current.PnlHaltReason != pnl.haltReason) {
			state.err = fmt.Errorf(
				"account %q pnl state changed before %s: %w",
				current.Code, operation, domain.ErrInvalid,
			)
			return nil, state.err
		}
		if block != nil && (current.Blocked != block.blocked ||
			current.BlockReason != block.cause.Reason ||
			current.BlockPolicy != block.cause.Policy ||
			current.BlockCode != block.cause.Code ||
			current.BlockDetails != block.cause.Details) {
			state.err = fmt.Errorf(
				"account %q block state changed before %s: %w",
				current.Code, operation, domain.ErrInvalid,
			)
			return nil, state.err
		}
		if currency != nil && *currency != "" {
			asset, assetErr := n.administrativeCurrencyAsset(
				state.ctx, eng, *currency,
			)
			if assetErr != nil {
				state.err = assetErr
				return nil, state.err
			}
			state.currencyAsset = asset
		}
		if pnl != nil {
			assignment, assignmentErr := eng.SpotFundsAccountPnlAssignment(
				pnl.value, pnl.haltReason,
			)
			if assignmentErr != nil {
				state.err = assignmentErr
				return nil, state.err
			}
			state.pnlAssignment = assignment
		}
		return state, nil
	}
	builder := asyncengine.Chain(source, begin)
	stepCount := 0
	if currency != nil {
		stepCount++
		if *currency == "" {
			builder.ClearAccountCurrency(
				asyncengine.ClearCurrencyHooks[*accountRuntimeChainState]{
					OnCleared: func(
						_ context.Context, state *accountRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
		} else {
			builder.SetAccountCurrency(
				asyncengine.CurrencyHooks[*accountRuntimeChainState]{
					Currency: func(
						_ context.Context, state *accountRuntimeChainState,
					) (param.Asset, error) {
						return state.currencyAsset, nil
					},
					OnSet: func(
						_ context.Context, state *accountRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
		}
	}
	if pnl != nil {
		stepCount++
		builder.SetSpotFundsAccountPnl(
			asyncengine.SpotFundsAccountPnlHooks[*accountRuntimeChainState]{
				Assignment: func(
					_ context.Context, state *accountRuntimeChainState,
				) (asyncengine.SpotFundsAccountPnlAssignment, error) {
					return state.pnlAssignment, nil
				},
				OnSet: func(
					_ context.Context,
					state *accountRuntimeChainState,
					result configure.PolicyConfigurationResult,
				) error {
					state.engineApplied = true
					blocks, resultErr := eng.AppliedSpotFundsAccountPnl(
						state.expected.Code,
						pnl.value,
						pnl.haltReason,
						result,
					)
					if resultErr != nil {
						state.err = resultErr
						return state.err
					}
					state.pnlBlocks = blocks
					return nil
				},
			},
		)
	}
	if block != nil {
		stepCount++
		if block.blocked {
			builder.UnblockAccount(
				asyncengine.UnblockHooks[*accountRuntimeChainState]{
					OnUnblocked: func(
						_ context.Context, state *accountRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
			if typedBlock {
				// The caller submits the typed block after this chain resolves. A
				// chain hook runs on the account lane's own worker and must not
				// submit another task to that same bounded queue.
			} else {
				builder.BlockAccount(
					asyncengine.BlockHooks[*accountRuntimeChainState]{
						Reason: func(
							context.Context, *accountRuntimeChainState,
						) (string, error) {
							return block.cause.Reason, nil
						},
						OnBlocked: func(
							_ context.Context, state *accountRuntimeChainState,
						) error {
							state.engineApplied = true
							return nil
						},
					},
				)
			}
		} else {
			builder.UnblockAccount(
				asyncengine.UnblockHooks[*accountRuntimeChainState]{
					OnUnblocked: func(
						_ context.Context, state *accountRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
		}
	}
	if stepCount == 0 {
		builder.Then(func(
			context.Context, *accountRuntimeChainState,
		) error {
			return nil
		})
	}
	runner := builder.Finally(func(
		_ context.Context,
		state *accountRuntimeChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.engineApplied, state.err = runtimeChainTerminalError(
			state.err, state.engineApplied, outcome,
		)
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	runErr := accountChainRunError(operation, state.err, chainErr)
	if runErr == nil && typedBlock {
		pending := eng.AsyncEngine().Accounts().BlockWithCause(
			state.ctx, source, typedCause,
		)
		if _, waitErr := pending.Await(context.Background()); waitErr != nil {
			state.engineApplied = true
			runErr = fmt.Errorf(
				"%s typed account block: %w",
				operation, errors.Join(waitErr, asyncengine.ErrChainRetryUnsafe),
			)
		} else {
			state.engineApplied = true
		}
	}
	return state.pnlBlocks, state.engineApplied, runErr
}

type groupRuntimeBlock struct {
	blocked bool
	reason  string
}

type groupRuntimeChainState struct {
	ctx           context.Context
	expected      domain.AccountGroup
	expectStored  bool
	currencyAsset param.Asset
	err           error
	engineApplied bool
}

// runGroupRuntimeChain requires its caller to hold an exclusive lane gate
// whenever block requests a reason replacement. A group-keyed chain does not
// serialize with member account lanes, and the SDK update is unblock+block.
func (n *localNode) runGroupRuntimeChain(
	ctx context.Context,
	eng engine.Engine,
	group domain.AccountGroup,
	expectStored bool,
	operation string,
	currency *string,
	block *groupRuntimeBlock,
) (bool, error) {
	source, err := administrativeGroupSource(eng, group)
	if err != nil {
		return false, err
	}
	state := &groupRuntimeChainState{
		ctx:          ctx,
		expected:     group,
		expectStored: expectStored,
	}
	begin := func(context.Context) (*groupRuntimeChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = fmt.Errorf("engine: %s cancelled: %w", operation, ctxErr)
			return nil, state.err
		}
		current, found, readErr := n.realm.GetGroup(
			state.ctx, state.expected.Code,
		)
		if readErr != nil {
			state.err = fmt.Errorf(
				"read group for %s: %w", operation, readErr,
			)
			return nil, state.err
		}
		if state.expectStored != found {
			if !found {
				state.err = fmt.Errorf(
					"group %q: %w", state.expected.Code, domain.ErrNotFound,
				)
			} else {
				state.err = fmt.Errorf(
					"deleted group %q reappeared before %s: %w",
					state.expected.Code, operation, domain.ErrInvalid,
				)
			}
			return nil, state.err
		}
		validated := state.expected
		if found {
			validated = current
		}
		if validateErr := validateGroupAdministrativeSource(
			validated, source,
		); validateErr != nil {
			state.err = fmt.Errorf(
				"read group for %s: %w", operation, validateErr,
			)
			return nil, state.err
		}
		if found && currency != nil && current.Currency != *currency {
			state.err = fmt.Errorf(
				"group %q currency changed from %q to %q: %w",
				current.Code, *currency, current.Currency, domain.ErrInvalid,
			)
			return nil, state.err
		}
		if found && block != nil && (current.Blocked != block.blocked ||
			current.BlockReason != block.reason) {
			state.err = fmt.Errorf(
				"group %q block state changed before %s: %w",
				current.Code, operation, domain.ErrInvalid,
			)
			return nil, state.err
		}
		if currency != nil && *currency != "" {
			asset, assetErr := n.administrativeCurrencyAsset(
				state.ctx, eng, *currency,
			)
			if assetErr != nil {
				state.err = assetErr
				return nil, state.err
			}
			state.currencyAsset = asset
		}
		return state, nil
	}
	builder := asyncengine.Chain(source, begin)
	stepCount := 0
	if currency != nil {
		stepCount++
		if *currency == "" {
			builder.ClearAccountGroupCurrency(
				asyncengine.ClearCurrencyHooks[*groupRuntimeChainState]{
					OnCleared: func(
						_ context.Context, state *groupRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
		} else {
			builder.SetAccountGroupCurrency(
				asyncengine.CurrencyHooks[*groupRuntimeChainState]{
					Currency: func(
						_ context.Context, state *groupRuntimeChainState,
					) (param.Asset, error) {
						return state.currencyAsset, nil
					},
					OnSet: func(
						_ context.Context, state *groupRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
		}
	}
	if block != nil {
		stepCount++
		if block.blocked {
			builder.UnblockAccountGroup(
				asyncengine.UnblockHooks[*groupRuntimeChainState]{
					OnUnblocked: func(
						_ context.Context, state *groupRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
			builder.BlockAccountGroup(
				asyncengine.BlockHooks[*groupRuntimeChainState]{
					Reason: func(
						context.Context, *groupRuntimeChainState,
					) (string, error) {
						return block.reason, nil
					},
					OnBlocked: func(
						_ context.Context, state *groupRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
		} else {
			builder.UnblockAccountGroup(
				asyncengine.UnblockHooks[*groupRuntimeChainState]{
					OnUnblocked: func(
						_ context.Context, state *groupRuntimeChainState,
					) error {
						state.engineApplied = true
						return nil
					},
				},
			)
		}
	}
	if stepCount == 0 {
		builder.Then(func(
			context.Context, *groupRuntimeChainState,
		) error {
			return nil
		})
	}
	runner := builder.Finally(func(
		_ context.Context,
		state *groupRuntimeChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.engineApplied, state.err = runtimeChainTerminalError(
			state.err, state.engineApplied, outcome,
		)
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	return state.engineApplied,
		accountChainRunError(operation, state.err, chainErr)
}

type groupMembershipSource struct {
	group  domain.AccountGroup
	stored bool
	source param.AccountGroupID
}

type groupMembershipRuntimeState struct {
	ctx           context.Context
	account       domain.Account
	expectedGroup string
	group         groupMembershipSource
	err           error
	engineApplied bool
}

func (n *localNode) runtimeGroupMembershipSource(
	ctx context.Context,
	eng engine.Engine,
	code string,
	fallback map[string]domain.AccountGroup,
) (groupMembershipSource, error) {
	group, stored, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return groupMembershipSource{}, err
	}
	if !stored {
		var ok bool
		group, ok = fallback[code]
		if !ok {
			return groupMembershipSource{}, fmt.Errorf(
				"group %q: %w", code, domain.ErrNotFound,
			)
		}
	}
	source, err := administrativeGroupSource(eng, group)
	if err != nil {
		return groupMembershipSource{}, err
	}
	return groupMembershipSource{
		group: group, stored: stored, source: source,
	}, nil
}

func (n *localNode) runGroupMembershipRuntimeChain(
	ctx context.Context,
	eng engine.Engine,
	account domain.Account,
	expectedGroup string,
	group groupMembershipSource,
	register bool,
) (bool, error) {
	accountSource, err := eng.AccountID(account.Code)
	if err != nil {
		return false, err
	}
	if err := validateAccountAdministrativeSource(
		account, accountSource,
	); err != nil {
		return false, err
	}
	state := &groupMembershipRuntimeState{
		ctx:           ctx,
		account:       account,
		expectedGroup: expectedGroup,
		group:         group,
	}
	begin := func(context.Context) (*groupMembershipRuntimeState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = fmt.Errorf(
				"engine: apply account group cancelled: %w", ctxErr,
			)
			return nil, state.err
		}
		current, found, readErr := n.realm.GetAccount(
			state.ctx, state.account.Code,
		)
		if readErr != nil {
			state.err = fmt.Errorf(
				"read account for group membership: %w", readErr,
			)
			return nil, state.err
		}
		if !found {
			state.err = fmt.Errorf(
				"account %q: %w", state.account.Code, domain.ErrNotFound,
			)
			return nil, state.err
		}
		if validateErr := validateAccountAdministrativeSource(
			current, accountSource,
		); validateErr != nil {
			state.err = fmt.Errorf(
				"read account for group membership: %w", validateErr,
			)
			return nil, state.err
		}
		if current.GroupCode != state.expectedGroup {
			state.err = fmt.Errorf(
				"account %q group changed from %q to %q: %w",
				current.Code,
				state.expectedGroup,
				current.GroupCode,
				domain.ErrInvalid,
			)
			return nil, state.err
		}
		currentGroup, stored, readErr := n.realm.GetGroup(
			state.ctx, state.group.group.Code,
		)
		if readErr != nil {
			state.err = fmt.Errorf(
				"read group for account membership: %w", readErr,
			)
			return nil, state.err
		}
		if stored != state.group.stored {
			state.err = fmt.Errorf(
				"group %q persistence changed before account membership: %w",
				state.group.group.Code, domain.ErrInvalid,
			)
			return nil, state.err
		}
		validatedGroup := state.group.group
		if stored {
			validatedGroup = currentGroup
		}
		if validateErr := validateGroupAdministrativeSource(
			validatedGroup, state.group.source,
		); validateErr != nil {
			state.err = fmt.Errorf(
				"read group for account membership: %w", validateErr,
			)
			return nil, state.err
		}
		return state, nil
	}
	builder := asyncengine.Chain(group.source, begin)
	accountIDs := []param.AccountID{accountSource}
	if register {
		builder.RegisterAccountGroup(
			accountIDs,
			asyncengine.RegisterAccountGroupHooks[*groupMembershipRuntimeState]{
				OnRegistered: func(
					_ context.Context, state *groupMembershipRuntimeState,
				) error {
					state.engineApplied = true
					return nil
				},
			},
		)
	} else {
		builder.UnregisterAccountGroup(
			accountIDs,
			asyncengine.UnregisterAccountGroupHooks[*groupMembershipRuntimeState]{
				OnUnregistered: func(
					_ context.Context, state *groupMembershipRuntimeState,
				) error {
					state.engineApplied = true
					return nil
				},
			},
		)
	}
	runner := builder.Finally(func(
		_ context.Context,
		state *groupMembershipRuntimeState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.engineApplied, state.err = runtimeChainTerminalError(
			state.err, state.engineApplied, outcome,
		)
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	return state.engineApplied, accountChainRunError(
		"apply account group", state.err, chainErr,
	)
}

func (n *localNode) applyGroupMove(
	ctx context.Context,
	eng engine.Engine,
	account domain.Account,
	oldGroup string,
	newGroup string,
	groupFallback map[string]domain.AccountGroup,
) (bool, error) {
	engineApplied := false
	groups := make(map[string]groupMembershipSource, 2)
	for _, code := range accountGroupAuditCodes(oldGroup, newGroup) {
		group, err := n.runtimeGroupMembershipSource(
			ctx, eng, code, groupFallback,
		)
		if err != nil {
			return false, err
		}
		groups[code] = group
	}
	if oldGroup != "" {
		applied, err := n.runGroupMembershipRuntimeChain(
			ctx, eng, account, newGroup, groups[oldGroup], false,
		)
		engineApplied = engineApplied || applied
		if err != nil {
			return engineApplied, err
		}
	}
	if newGroup == "" {
		return engineApplied, nil
	}
	applied, err := n.runGroupMembershipRuntimeChain(
		ctx, eng, account, newGroup, groups[newGroup], true,
	)
	engineApplied = engineApplied || applied
	if err == nil {
		return engineApplied, nil
	}
	var revertErr error
	if oldGroup != "" {
		mutationCtx := context.WithoutCancel(ctx)
		applied, revertErr = n.runGroupMembershipRuntimeChain(
			mutationCtx,
			eng,
			account,
			newGroup,
			groups[oldGroup],
			true,
		)
		engineApplied = engineApplied || applied
	}
	return engineApplied, errors.Join(
		err,
		optionalOperationError("restore previous account group", revertErr),
	)
}
