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
// Please see https://officer.openpit.dev and the OWNERS file for details.

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

// applyGroupRuntime applies a group's currency and block state to the live
// engine in one exclusive section. Group currency is an exclusive operation,
// and inside the section a reason replacement - unblock, then block - cannot
// interleave with member account lanes. engineEntered reports that an engine
// call was made, so a failure may have changed engine state; such a failure is
// marked asyncengine.ErrChainRetryUnsafe.
func (n *localNode) applyGroupRuntime(
	ctx context.Context,
	eng engine.Engine,
	group domain.AccountGroup,
	expectStored bool,
	operation string,
	currency *string,
	block *groupRuntimeBlock,
) (engineEntered bool, err error) {
	source, err := administrativeGroupSource(eng, group)
	if err != nil {
		return false, err
	}
	err = engine.RunExclusive(
		ctx, eng.AsyncEngine(),
		func(driver asyncengine.Driver) error {
			currencyAsset, err := n.validateGroupRuntime(
				ctx, eng, group, expectStored, source, operation, currency, block,
			)
			if err != nil {
				return err
			}
			engineEntered = true
			accounts := driver.Accounts()
			if currency != nil {
				if *currency == "" {
					if err := accounts.ClearGroupCurrency(source); err != nil {
						return fmt.Errorf("%s: clear group currency: %w", operation, err)
					}
				} else if err := accounts.SetGroupCurrency(
					source, currencyAsset,
				); err != nil {
					return fmt.Errorf("%s: set group currency: %w", operation, err)
				}
			}
			if block == nil {
				return nil
			}
			if err := accounts.UnblockGroup(source); err != nil {
				return fmt.Errorf("%s: unblock group: %w", operation, err)
			}
			if !block.blocked {
				return nil
			}
			if err := accounts.BlockGroup(source, block.reason); err != nil {
				return fmt.Errorf("%s: block group: %w", operation, err)
			}
			return nil
		},
	)
	if err != nil && engineEntered {
		err = errors.Join(err, asyncengine.ErrChainRetryUnsafe)
	}
	return engineEntered, err
}

// validateGroupRuntime re-reads the group inside the exclusive section and
// refuses the update when its stored state no longer matches what the caller
// applies. It returns the engine currency asset when currency sets one.
func (n *localNode) validateGroupRuntime(
	ctx context.Context,
	eng engine.Engine,
	group domain.AccountGroup,
	expectStored bool,
	source param.AccountGroupID,
	operation string,
	currency *string,
	block *groupRuntimeBlock,
) (param.Asset, error) {
	if err := ctx.Err(); err != nil {
		return param.Asset{}, fmt.Errorf("engine: %s cancelled: %w", operation, err)
	}
	current, found, err := n.realm.GetGroup(ctx, group.Code)
	if err != nil {
		return param.Asset{}, fmt.Errorf("read group for %s: %w", operation, err)
	}
	if expectStored != found {
		if !found {
			return param.Asset{}, fmt.Errorf(
				"group %q: %w", group.Code, domain.ErrNotFound,
			)
		}
		return param.Asset{}, fmt.Errorf(
			"deleted group %q reappeared before %s: %w",
			group.Code, operation, domain.ErrInvalid,
		)
	}
	validated := group
	if found {
		validated = current
	}
	if err := validateGroupAdministrativeSource(validated, source); err != nil {
		return param.Asset{}, fmt.Errorf("read group for %s: %w", operation, err)
	}
	if found && currency != nil && current.Currency != *currency {
		return param.Asset{}, fmt.Errorf(
			"group %q currency changed from %q to %q: %w",
			current.Code, *currency, current.Currency, domain.ErrInvalid,
		)
	}
	if found && block != nil && (current.Blocked != block.blocked ||
		current.BlockReason != block.reason) {
		return param.Asset{}, fmt.Errorf(
			"group %q block state changed before %s: %w",
			current.Code, operation, domain.ErrInvalid,
		)
	}
	if currency == nil || *currency == "" {
		return param.Asset{}, nil
	}
	return n.administrativeCurrencyAsset(ctx, eng, *currency)
}

type groupMembershipSource struct {
	group  domain.AccountGroup
	stored bool
	source param.AccountGroupID
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

// validateGroupMembershipRuntime re-reads a restored account and the groups of
// its move inside the exclusive section and refuses the move when the account
// no longer belongs to expectedGroup in the store or a group's persistence or
// engine identity changed.
func (n *localNode) validateGroupMembershipRuntime(
	ctx context.Context,
	account domain.Account,
	accountSource param.AccountID,
	expectedGroup string,
	groups []groupMembershipSource,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: apply account group cancelled: %w", err)
	}
	current, found, err := n.realm.GetAccount(ctx, account.Code)
	if err != nil {
		return fmt.Errorf("read account for group membership: %w", err)
	}
	if !found {
		return fmt.Errorf("account %q: %w", account.Code, domain.ErrNotFound)
	}
	if err := validateAccountAdministrativeSource(
		current, accountSource,
	); err != nil {
		return fmt.Errorf("read account for group membership: %w", err)
	}
	if current.GroupCode != expectedGroup {
		return fmt.Errorf(
			"account %q group changed from %q to %q: %w",
			current.Code, expectedGroup, current.GroupCode, domain.ErrInvalid,
		)
	}
	for _, group := range groups {
		currentGroup, stored, err := n.realm.GetGroup(ctx, group.group.Code)
		if err != nil {
			return fmt.Errorf("read group for account membership: %w", err)
		}
		if stored != group.stored {
			return fmt.Errorf(
				"group %q persistence changed before account membership: %w",
				group.group.Code, domain.ErrInvalid,
			)
		}
		validated := group.group
		if stored {
			validated = currentGroup
		}
		if err := validateGroupAdministrativeSource(
			validated, group.source,
		); err != nil {
			return fmt.Errorf("read group for account membership: %w", err)
		}
	}
	return nil
}

// applyGroupMove moves an account the store already records in newGroup from
// oldGroup to newGroup on the live engine in one exclusive section, so no
// engine call observes it in the default group between leaving one group and
// joining the other. engineEntered reports that an engine call was made, so a
// failure may have changed engine state.
func (n *localNode) applyGroupMove(
	ctx context.Context,
	eng engine.Engine,
	account domain.Account,
	oldGroup string,
	newGroup string,
	groupFallback map[string]domain.AccountGroup,
) (engineEntered bool, err error) {
	accountSource, err := eng.AccountID(account.Code)
	if err != nil {
		return false, err
	}
	if err := validateAccountAdministrativeSource(
		account, accountSource,
	); err != nil {
		return false, err
	}
	groups := make([]groupMembershipSource, 0, 2)
	var from, to *param.AccountGroupID
	for _, code := range accountGroupAuditCodes(oldGroup, newGroup) {
		group, err := n.runtimeGroupMembershipSource(
			ctx, eng, code, groupFallback,
		)
		if err != nil {
			return false, err
		}
		groups = append(groups, group)
		source := group.source
		if code == oldGroup {
			from = &source
		}
		if code == newGroup {
			to = &source
		}
	}
	err = engine.RunExclusive(
		ctx, eng.AsyncEngine(),
		func(driver asyncengine.Driver) error {
			if err := n.validateGroupMembershipRuntime(
				ctx, account, accountSource, newGroup, groups,
			); err != nil {
				return err
			}
			engineEntered = true
			if _, err := moveAccountGroup(
				driver.Accounts(), []param.AccountID{accountSource}, from, to,
			); err != nil {
				return fmt.Errorf("apply account group: %w", err)
			}
			return nil
		},
	)
	return engineEntered, err
}

// groupMembership is the membership half of the engine's account
// administration that moveAccountGroup drives.
type groupMembership interface {
	RegisterGroup([]param.AccountID, param.AccountGroupID) error
	UnregisterGroup([]param.AccountID, param.AccountGroupID) error
}

// moveAccountGroup moves accountIDs from one group to another. It must run
// inside an exclusive section, so no engine call observes the accounts in the
// default group between the two steps. from or to is nil on the default-group
// side of the move. A failed registration registers the accounts back into
// from; restored reports that this revert succeeded, so the engine holds the
// membership it had before the move. Every error is marked
// asyncengine.ErrChainRetryUnsafe: an engine call was made.
func moveAccountGroup(
	accounts groupMembership,
	accountIDs []param.AccountID,
	from *param.AccountGroupID,
	to *param.AccountGroupID,
) (restored bool, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, asyncengine.ErrChainRetryUnsafe)
		}
	}()
	if from != nil {
		if err := accounts.UnregisterGroup(accountIDs, *from); err != nil {
			return false, fmt.Errorf("unregister account group: %w", err)
		}
	}
	if to == nil {
		return false, nil
	}
	if err := accounts.RegisterGroup(accountIDs, *to); err != nil {
		err = fmt.Errorf("register account group: %w", err)
		if from == nil {
			return false, err
		}
		if revertErr := accounts.RegisterGroup(accountIDs, *from); revertErr != nil {
			return false, errors.Join(
				err,
				fmt.Errorf("restore previous account group: %w", revertErr),
			)
		}
		return true, err
	}
	return false, nil
}
