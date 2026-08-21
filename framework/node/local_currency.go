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
	"strconv"

	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/param"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// SetAccountCurrency sets or clears account currency through an SDK account
// chain whose final engine hook persists the store state and audit. Effective
// currency changes are rejected when the account has non-zero positions or
// P&L, a halted P&L, active orders, or active limits.
func (n *localNode) SetAccountCurrency(
	ctx context.Context, key Key, currency string, caller domain.Caller,
) error {
	if err := n.ensureAccountCurrencyAssetRegisteredExclusive(
		ctx, key.Account, currency, caller,
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
	source, err := eng.AccountID(key.Account)
	if err != nil {
		return err
	}
	resolver := eng
	type accountCurrencyChainState struct {
		administrativeChainState
		previous domain.Account
	}
	state := &accountCurrencyChainState{
		administrativeChainState: administrativeChainState{ctx: ctx},
	}
	begin := func(context.Context) (*accountCurrencyChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = ctxErr
			return nil, state.err
		}
		previous, ok, readErr := n.realm.GetAccount(state.ctx, key.Account)
		if readErr != nil {
			state.err = fmt.Errorf("read account for currency: %w", readErr)
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
			state.err = fmt.Errorf("read account for currency: %w", validateErr)
			return nil, state.err
		}
		nextEffective, _ := domain.ResolveCurrencyCascade(
			currency,
			previous.GroupCurrency,
			previous.DefaultCurrency,
		)
		if guardErr := n.guardEffectiveCurrencyChange(
			state.ctx,
			[]domain.AccountID{key.Account},
			previous.EffectiveCurrency,
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
		if previous.Currency == currency {
			state.err = domain.ErrNoChange
			return nil, state.err
		}
		state.previous = previous
		state.failureOperation = "apply account currency"
		return state, nil
	}
	persist := func(state *accountCurrencyChainState) error {
		state.engineApplied = true
		mutationCtx := context.WithoutCancel(state.ctx)
		state.failureOperation = "set account currency"
		if err := n.realm.SetAccountCurrency(
			mutationCtx, key.Account, currency,
		); err != nil {
			state.err = fmt.Errorf("set account currency: %w", err)
			return state.err
		}
		state.persistenceCompleted = true
		state.failureOperation = "audit account currency"
		if err := n.audit(mutationCtx, caller, store.AuditEntry{
			Action:       domain.AuditActionSetAccountCurrency,
			Account:      key.Account,
			AccountTitle: state.previous.Title,
			Detail: currencyDetail(
				"set account currency",
				key.Account.String(),
				state.previous.Currency,
				currency,
			),
		}); err != nil {
			state.err = fmt.Errorf("audit account currency: %w", err)
			return state.err
		}
		return nil
	}
	builder := asyncengine.Chain(source, begin)
	if currency == "" {
		builder.ClearAccountCurrency(
			asyncengine.ClearCurrencyHooks[*accountCurrencyChainState]{
				OnCleared: func(
					_ context.Context, state *accountCurrencyChainState,
				) error {
					return persist(state)
				},
			},
		)
	} else {
		builder.SetAccountCurrency(
			asyncengine.CurrencyHooks[*accountCurrencyChainState]{
				Currency: func(
					_ context.Context, state *accountCurrencyChainState,
				) (param.Asset, error) {
					return n.administrativeCurrencyAsset(
						state.ctx, resolver, currency,
					)
				},
				OnSet: func(
					_ context.Context, state *accountCurrencyChainState,
				) error {
					return persist(state)
				},
			},
		)
	}
	runner := builder.Finally(func(
		_ context.Context,
		state *accountCurrencyChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.err = n.administrativeChainTerminalError(
			"account currency",
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
	runErr := accountChainRunError(
		"set account currency", state.err, chainErr,
	)
	if errors.Is(runErr, domain.ErrNoChange) {
		return nil
	}
	if !state.engineApplied {
		return runErr
	}
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
	return n.reconcileAdministrativeChainFailure(
		ctx, "account currency", runErr,
	)
}

// ensureAccountCurrencyAssetRegisteredExclusive publishes a missing currency
// asset before the account lane opens. Its guard call is a best-effort filter,
// not a duplicate of the lane's: publication cannot happen inside the lane, so
// without it a change the lane rejects would still leave an asset, a resolver
// alias and an audit row behind. The lane repeats the check under
// serialization and stays the authority.
func (n *localNode) ensureAccountCurrencyAssetRegisteredExclusive(
	ctx context.Context,
	account domain.AccountID,
	currency string,
	caller domain.Caller,
) error {
	if currency == "" {
		return nil
	}
	previous, ok, err := n.realm.GetAccount(ctx, account)
	if err != nil {
		return fmt.Errorf("read account for currency: %w", err)
	}
	if !ok {
		return fmt.Errorf("account %q: %w", account, domain.ErrNotFound)
	}
	if previous.Currency == currency {
		return nil
	}
	nextEffective, _ := domain.ResolveCurrencyCascade(
		currency,
		previous.GroupCurrency,
		previous.DefaultCurrency,
	)
	if err := n.guardEffectiveCurrencyChange(
		ctx,
		[]domain.AccountID{account},
		previous.EffectiveCurrency,
		nextEffective,
	); err != nil {
		if !errors.Is(err, domain.ErrConflict) {
			return err
		}
		return domain.NewCurrencyChangeBlockedError(
			domain.ScopeAccount, account.String(), err,
		)
	}
	if _, ok, err := n.realm.GetAsset(ctx, currency); err != nil {
		return fmt.Errorf("read currency asset for registration: %w", err)
	} else if ok {
		return nil
	}
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()
	return n.ensureCurrencyAsset(ctx, currency, "set account currency", caller)
}

func (n *localNode) administrativeCurrencyAsset(
	ctx context.Context,
	resolver engine.DictionaryResolver,
	currency string,
) (param.Asset, error) {
	return n.administrativeAsset(ctx, resolver, currency, "currency asset")
}

func (n *localNode) administrativeAsset(
	ctx context.Context,
	resolver engine.DictionaryResolver,
	code string,
	kind string,
) (param.Asset, error) {
	asset, ok, err := n.realm.GetAsset(ctx, code)
	if err != nil {
		return param.Asset{}, fmt.Errorf("read %s: %w", kind, err)
	}
	if !ok {
		return param.Asset{}, fmt.Errorf(
			"%s %q: %w", kind, code, domain.ErrNotFound,
		)
	}
	if err := domain.ValidateEngineAssetID(asset.EngineAssetID); err != nil {
		return param.Asset{}, fmt.Errorf(
			"%s %q engine id: %w", kind, code, err,
		)
	}
	ready, err := resolver.ResolveAsset(code)
	if err != nil {
		return param.Asset{}, fmt.Errorf(
			"resolve %s %q from the live dictionary: %w",
			kind,
			code,
			err,
		)
	}
	if ready.Safe() != strconv.FormatUint(asset.EngineAssetID.Uint64(), 10) {
		return param.Asset{}, fmt.Errorf(
			"%s %q engine identity does not match the live resolver: %w",
			kind,
			code,
			domain.ErrInvalid,
		)
	}
	return ready, nil
}

// SetGroupCurrency sets or clears a persisted account-group currency through a
// group-sourced SDK chain whose final engine hook commits the store and audit.
func (n *localNode) SetGroupCurrency(
	ctx context.Context, code string, currency string, caller domain.Caller,
) error {
	return n.setGroupCurrency(ctx, code, currency, caller, false)
}

// SetDefaultGroupCurrency sets or clears the reserved default group currency
// through its group-sourced SDK chain.
func (n *localNode) SetDefaultGroupCurrency(
	ctx context.Context, currency string, caller domain.Caller,
) error {
	return n.setGroupCurrency(ctx, "", currency, caller, true)
}

func (n *localNode) setGroupCurrency(
	ctx context.Context,
	code string,
	currency string,
	caller domain.Caller,
	defaultGroup bool,
) error {
	operation := "set group currency"
	if defaultGroup {
		operation = "set default group currency"
	}
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()

	prevCurrency := ""
	var previous domain.AccountGroup
	if !defaultGroup {
		prev, ok, err := n.realm.GetGroup(ctx, code)
		if err != nil {
			return fmt.Errorf("read group for currency: %w", err)
		}
		if !ok {
			return fmt.Errorf("group %q: %w", code, domain.ErrNotFound)
		}
		prevCurrency = prev.Currency
		if prevCurrency == currency {
			return nil
		}
		if err := n.guardGroupCurrencyChange(ctx, code, currency); err != nil {
			if !errors.Is(err, domain.ErrConflict) {
				return err
			}
			return domain.NewCurrencyChangeBlockedError(
				domain.ScopeAccountGroup, code, err,
			)
		}
		previous = prev
	} else {
		if prev, ok, err := n.realm.GetGroup(ctx, ""); err != nil {
			return fmt.Errorf("read default group currency: %w", err)
		} else if ok {
			prevCurrency = prev.Currency
			previous = prev
		}
		if err := n.guardDefaultGroupCurrencyChange(ctx, currency); err != nil {
			if !errors.Is(err, domain.ErrConflict) {
				return err
			}
			return domain.NewCurrencyChangeBlockedError(
				domain.ScopeAccountGroup, "-", err,
			)
		}
	}
	if prevCurrency == currency {
		return nil
	}
	if err := n.ensureCurrencyAsset(ctx, currency, operation, caller); err != nil {
		return err
	}
	eng := n.currentEngine()
	resolver := eng
	source, err := administrativeGroupSource(resolver, previous)
	if err != nil {
		return err
	}
	if err := validateGroupAdministrativeSource(previous, source); err != nil {
		return fmt.Errorf("read group for currency: %w", err)
	}
	label := code
	if defaultGroup {
		label = "default"
	}
	type groupCurrencyChainState struct {
		administrativeChainState
		previous domain.AccountGroup
	}
	state := &groupCurrencyChainState{
		administrativeChainState: administrativeChainState{ctx: ctx},
	}
	begin := func(context.Context) (*groupCurrencyChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = ctxErr
			return nil, state.err
		}
		current, found, readErr := n.realm.GetGroup(state.ctx, code)
		if readErr != nil {
			state.err = fmt.Errorf("read group for currency: %w", readErr)
			return nil, state.err
		}
		if !defaultGroup && !found {
			state.err = fmt.Errorf("group %q: %w", code, domain.ErrNotFound)
			return nil, state.err
		}
		if !found {
			current = domain.AccountGroup{Code: ""}
		}
		if validateErr := validateGroupAdministrativeSource(
			current, source,
		); validateErr != nil {
			state.err = fmt.Errorf("read group for currency: %w", validateErr)
			return nil, state.err
		}
		if current.Currency == currency {
			state.err = domain.ErrNoChange
			return nil, state.err
		}
		if defaultGroup {
			if guardErr := n.guardDefaultGroupCurrencyChange(
				state.ctx, currency,
			); guardErr != nil {
				if !errors.Is(guardErr, domain.ErrConflict) {
					state.err = guardErr
					return nil, state.err
				}
				state.err = domain.NewCurrencyChangeBlockedError(
					domain.ScopeAccountGroup, "-", guardErr,
				)
				return nil, state.err
			}
		} else if guardErr := n.guardGroupCurrencyChange(
			state.ctx, code, currency,
		); guardErr != nil {
			if !errors.Is(guardErr, domain.ErrConflict) {
				state.err = guardErr
				return nil, state.err
			}
			state.err = domain.NewCurrencyChangeBlockedError(
				domain.ScopeAccountGroup, code, guardErr,
			)
			return nil, state.err
		}
		state.previous = current
		state.failureOperation = "apply group currency"
		return state, nil
	}
	persist := func(state *groupCurrencyChainState) error {
		state.engineApplied = true
		mutationCtx := context.WithoutCancel(state.ctx)
		state.failureOperation = "set group currency"
		if err := n.realm.SetGroupCurrency(
			mutationCtx, code, currency,
		); err != nil {
			state.err = fmt.Errorf("set group currency: %w", err)
			return state.err
		}
		state.persistenceCompleted = true
		state.failureOperation = "audit group currency"
		if err := n.audit(mutationCtx, caller, store.AuditEntry{
			Action: domain.AuditActionSetGroupCurrency,
			Group:  code,
			Detail: currencyDetail(
				"set group currency",
				label,
				state.previous.Currency,
				currency,
			),
		}); err != nil {
			state.err = fmt.Errorf("audit group currency: %w", err)
			return state.err
		}
		return nil
	}
	builder := asyncengine.Chain(source, begin)
	if currency == "" {
		builder.ClearAccountGroupCurrency(
			asyncengine.ClearCurrencyHooks[*groupCurrencyChainState]{
				OnCleared: func(
					_ context.Context, state *groupCurrencyChainState,
				) error {
					return persist(state)
				},
			},
		)
	} else {
		builder.SetAccountGroupCurrency(
			asyncengine.CurrencyHooks[*groupCurrencyChainState]{
				Currency: func(
					_ context.Context, state *groupCurrencyChainState,
				) (param.Asset, error) {
					return n.administrativeCurrencyAsset(
						state.ctx, resolver, currency,
					)
				},
				OnSet: func(
					_ context.Context, state *groupCurrencyChainState,
				) error {
					return persist(state)
				},
			},
		)
	}
	runner := builder.Finally(func(
		_ context.Context,
		state *groupCurrencyChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.err = n.administrativeChainTerminalError(
			"group currency",
			"group",
			label,
			&state.administrativeChainState,
			outcome,
		)
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	runErr := accountChainRunError(
		operation, state.err, chainErr,
	)
	if errors.Is(runErr, domain.ErrNoChange) {
		return nil
	}
	if !state.engineApplied {
		return runErr
	}
	return n.reconcileAdministrativeChainFailure(
		ctx, "group currency", runErr,
	)
}

func (n *localNode) ensureCurrencyAsset(
	ctx context.Context,
	currency string,
	operation string,
	caller domain.Caller,
) error {
	if currency == "" {
		return nil
	}
	_, err := n.ensureAutoCreatedAsset(ctx, currency, operation, caller)
	return err
}

func (n *localNode) guardGroupCurrencyChange(
	ctx context.Context,
	groupCode string,
	currency string,
) error {
	accounts, err := n.realm.ListGroupAccounts(ctx, groupCode)
	if err != nil {
		return fmt.Errorf("list group accounts for currency: %w", err)
	}
	candidates := make([]domain.AccountID, 0, len(accounts))
	for _, account := range accounts {
		if account.Currency != "" {
			continue
		}
		nextEffective, _ := domain.ResolveCurrencyCascade(
			"", currency, account.DefaultCurrency,
		)
		if account.EffectiveCurrency != nextEffective {
			candidates = append(candidates, account.Code)
		}
	}
	return n.guardCurrencyCandidates(ctx, candidates)
}

func (n *localNode) guardDefaultGroupCurrencyChange(
	ctx context.Context,
	currency string,
) error {
	accounts, err := n.realm.ListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list accounts for default currency: %w", err)
	}
	candidates := make([]domain.AccountID, 0, len(accounts))
	for _, account := range accounts {
		if account.Currency != "" || account.GroupCurrency != "" {
			continue
		}
		if account.EffectiveCurrency != currency {
			candidates = append(candidates, account.Code)
		}
	}
	return n.guardCurrencyCandidates(ctx, candidates)
}

func (n *localNode) guardEffectiveCurrencyChange(
	ctx context.Context,
	accounts []domain.AccountID,
	before string,
	after string,
) error {
	if before == after {
		return nil
	}
	return n.guardCurrencyCandidates(ctx, accounts)
}

type currencyChangeCandidatesBlockedError struct {
	offenders []domain.AccountID
}

func (e currencyChangeCandidatesBlockedError) Error() string {
	return fmt.Sprintf(
		"%d account(s) carry state that prevents a currency change: %s",
		len(e.offenders), domain.ErrConflict,
	)
}

func (e currencyChangeCandidatesBlockedError) Unwrap() error {
	return domain.ErrConflict
}

func (n *localNode) guardCurrencyCandidates(
	ctx context.Context,
	accounts []domain.AccountID,
) error {
	if len(accounts) == 0 {
		return nil
	}
	offenders, err := n.realm.ListAccountsBlockingCurrencyChange(ctx, accounts)
	if err != nil {
		return fmt.Errorf("list currency change blockers: %w", err)
	}
	if len(offenders) == 0 {
		return nil
	}
	return currencyChangeCandidatesBlockedError{offenders: offenders}
}

func (n *localNode) guardGroupDeleteCurrencyChange(
	ctx context.Context, accounts []domain.Account,
) error {
	candidates := make([]domain.AccountID, 0, len(accounts))
	for _, account := range accounts {
		if account.Currency != "" {
			continue
		}
		nextEffective, _ := domain.ResolveCurrencyCascade(
			"", "", account.DefaultCurrency,
		)
		if account.EffectiveCurrency != nextEffective {
			candidates = append(candidates, account.Code)
		}
	}
	return n.guardCurrencyCandidates(ctx, candidates)
}

func currencyDetail(prefix, target, before, after string) string {
	return fmt.Sprintf(
		"%s %s %s -> %s",
		prefix,
		target,
		currencyValue(before),
		currencyValue(after),
	)
}

func currencyValue(currency string) string {
	if currency == "" {
		return "<unset>"
	}
	return currency
}
