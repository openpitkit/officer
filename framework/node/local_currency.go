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
	"maps"
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
	ctx context.Context, account domain.AccountID, currency string, caller domain.Caller,
) error {
	if err := n.ensureAccountCurrencyAssetRegisteredExclusive(
		ctx, account, currency, caller,
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
	source, err := eng.AccountID(account)
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
		previous, ok, readErr := n.realm.GetAccount(state.ctx, account)
		if readErr != nil {
			state.err = fmt.Errorf("read account for currency: %w", readErr)
			return nil, state.err
		}
		if !ok {
			state.err = fmt.Errorf(
				"account %q: %w", account, domain.ErrNotFound,
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
			[]domain.AccountID{account},
			previous.EffectiveCurrency,
			nextEffective,
		); guardErr != nil {
			if !errors.Is(guardErr, domain.ErrConflict) {
				state.err = guardErr
				return nil, state.err
			}
			state.err = n.auditCurrencyChangeRefusal(state.ctx, caller, store.AuditEntry{
				Action:  domain.AuditActionSetAccountCurrency,
				Account: account, AccountTitle: previous.Title,
			}, domain.AuditDetailSetAccountCurrencyRefused, currencyDetailParams(
				domain.AuditDetailParams{"account": account.String()},
				previous.Currency, currency,
			), domain.NewCurrencyChangeBlockedError(
				domain.ScopeAccount, account.String(), guardErr,
			))
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
			mutationCtx, account, currency,
		); err != nil {
			state.err = fmt.Errorf("set account currency: %w", err)
			return state.err
		}
		state.persistenceCompleted = true
		state.failureOperation = "audit account currency"
		if err := n.audit(mutationCtx, caller, store.AuditEntry{
			Action:       domain.AuditActionSetAccountCurrency,
			Account:      account,
			AccountTitle: state.previous.Title,
			Detail: domain.NewAuditDetail(
				domain.AuditDetailSetAccountCurrency,
				currencyDetailParams(
					domain.AuditDetailParams{"account": account.String()},
					state.previous.Currency, currency,
				),
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
			account.String(),
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
		return n.auditCurrencyChangeRefusal(ctx, caller, store.AuditEntry{
			Action:  domain.AuditActionSetAccountCurrency,
			Account: account, AccountTitle: previous.Title,
		}, domain.AuditDetailSetAccountCurrencyRefused, currencyDetailParams(
			domain.AuditDetailParams{"account": account.String()},
			previous.Currency, currency,
		), domain.NewCurrencyChangeBlockedError(
			domain.ScopeAccount, account.String(), err,
		))
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

// SetGroupCurrency sets or clears a persisted account-group currency. Group
// currency is an exclusive operation, so the engine change runs in one exclusive
// section, and the store and audit are committed in it after the engine change.
func (n *localNode) SetGroupCurrency(
	ctx context.Context, code string, currency string, caller domain.Caller,
) error {
	return n.setGroupCurrency(ctx, code, currency, caller, false)
}

// SetDefaultGroupCurrency sets or clears the reserved default group currency
// the same way SetGroupCurrency does.
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
			return n.auditCurrencyChangeRefusal(ctx, caller, store.AuditEntry{
				Action: domain.AuditActionSetGroupCurrency, Group: code,
			}, domain.AuditDetailSetGroupCurrencyRefused, currencyDetailParams(
				domain.AuditDetailParams{"group": code}, prevCurrency, currency,
			), domain.NewCurrencyChangeBlockedError(
				domain.ScopeAccountGroup, code, err,
			))
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
			return n.auditCurrencyChangeRefusal(ctx, caller, store.AuditEntry{
				Action: domain.AuditActionSetGroupCurrency,
			}, domain.AuditDetailSetDefaultGroupCurrencyRefused, currencyDetailParams(
				nil, prevCurrency, currency,
			), domain.NewCurrencyChangeBlockedError(
				domain.ScopeAccountGroup, "-", err,
			))
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
	detailCode := domain.AuditDetailSetGroupCurrency
	detailTarget := domain.AuditDetailParams{"group": code}
	if defaultGroup {
		label = "default"
		detailCode = domain.AuditDetailSetDefaultGroupCurrency
		detailTarget = nil
	}
	engineEntered := false
	// persistFailure names the store step that failed after the engine changed
	// the currency; the fatal hook runs only once the section has ended.
	persistFailure := ""
	runErr := engine.RunExclusive(
		ctx, eng.AsyncEngine(),
		func(driver asyncengine.Driver) error {
			current, err := n.validateGroupCurrencyChange(
				ctx, code, currency, caller, defaultGroup, source,
			)
			if err != nil {
				return err
			}
			var asset param.Asset
			if currency != "" {
				asset, err = n.administrativeCurrencyAsset(ctx, resolver, currency)
				if err != nil {
					return err
				}
			}
			engineEntered = true
			accounts := driver.Accounts()
			if currency == "" {
				err = accounts.ClearGroupCurrency(source)
			} else {
				err = accounts.SetGroupCurrency(source, asset)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", operation, err)
			}
			// The store follows the engine inside the section, so no lane task
			// runs between the two.
			mutationCtx := context.WithoutCancel(ctx)
			persistFailure = "set group currency"
			if err := n.realm.SetGroupCurrency(mutationCtx, code, currency); err != nil {
				return fmt.Errorf("set group currency: %w", err)
			}
			persistFailure = "audit group currency"
			if err := n.audit(mutationCtx, caller, store.AuditEntry{
				Action: domain.AuditActionSetGroupCurrency,
				Group:  code,
				Detail: domain.NewAuditDetail(detailCode, currencyDetailParams(
					detailTarget, current.Currency, currency,
				)),
			}); err != nil {
				return fmt.Errorf("audit group currency: %w", err)
			}
			return nil
		},
	)
	switch {
	case runErr == nil, errors.Is(runErr, domain.ErrNoChange):
		return nil
	case !engineEntered:
		return runErr
	case persistFailure != "":
		return errors.Join(
			n.fatalPostEngineAuditByCode(persistFailure, "group", label, runErr),
			asyncengine.ErrChainRetryUnsafe,
		)
	default:
		return errors.Join(runErr, asyncengine.ErrChainRetryUnsafe)
	}
}

// validateGroupCurrencyChange re-reads the group inside the exclusive section
// and refuses the change when the group's engine identity moved, when the
// currency is already set (domain.ErrNoChange), or when the change would move
// a guarded effective currency. It returns the group as stored.
func (n *localNode) validateGroupCurrencyChange(
	ctx context.Context,
	code string,
	currency string,
	caller domain.Caller,
	defaultGroup bool,
	source param.AccountGroupID,
) (domain.AccountGroup, error) {
	if err := ctx.Err(); err != nil {
		return domain.AccountGroup{}, err
	}
	current, found, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("read group for currency: %w", err)
	}
	if !defaultGroup && !found {
		return domain.AccountGroup{}, fmt.Errorf(
			"group %q: %w", code, domain.ErrNotFound,
		)
	}
	if !found {
		current = domain.AccountGroup{Code: ""}
	}
	if err := validateGroupAdministrativeSource(current, source); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("read group for currency: %w", err)
	}
	if current.Currency == currency {
		return domain.AccountGroup{}, domain.ErrNoChange
	}
	if defaultGroup {
		guardErr := n.guardDefaultGroupCurrencyChange(ctx, currency)
		if guardErr == nil {
			return current, nil
		}
		if !errors.Is(guardErr, domain.ErrConflict) {
			return domain.AccountGroup{}, guardErr
		}
		return domain.AccountGroup{}, n.auditCurrencyChangeRefusal(ctx, caller, store.AuditEntry{
			Action: domain.AuditActionSetGroupCurrency,
		}, domain.AuditDetailSetDefaultGroupCurrencyRefused, currencyDetailParams(
			nil, current.Currency, currency,
		), domain.NewCurrencyChangeBlockedError(
			domain.ScopeAccountGroup, "-", guardErr,
		))
	}
	guardErr := n.guardGroupCurrencyChange(ctx, code, currency)
	if guardErr == nil {
		return current, nil
	}
	if !errors.Is(guardErr, domain.ErrConflict) {
		return domain.AccountGroup{}, guardErr
	}
	return domain.AccountGroup{}, n.auditCurrencyChangeRefusal(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetGroupCurrency, Group: code,
	}, domain.AuditDetailSetGroupCurrencyRefused, currencyDetailParams(
		domain.AuditDetailParams{"group": code}, current.Currency, currency,
	), domain.NewCurrencyChangeBlockedError(
		domain.ScopeAccountGroup, code, guardErr,
	))
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
	offenders           []domain.AccountID
	currencyValuedLimit bool
}

func (e currencyChangeCandidatesBlockedError) Error() string {
	if e.currencyValuedLimit {
		return fmt.Sprintf("%d account(s): %s: %s", len(e.offenders),
			domain.ErrCurrencyValuedLimit, domain.ErrConflict)
	}
	return fmt.Sprintf(
		"%d account(s) carry state that prevents a currency change: %s",
		len(e.offenders), domain.ErrConflict,
	)
}

func (e currencyChangeCandidatesBlockedError) Unwrap() error {
	return domain.ErrConflict
}

func (e currencyChangeCandidatesBlockedError) Is(target error) bool {
	return target == domain.ErrCurrencyValuedLimit && e.currencyValuedLimit
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
	blockedAccounts := make([]domain.AccountID, 0, len(offenders))
	currencyValuedLimit := true
	for _, offender := range offenders {
		blockedAccounts = append(blockedAccounts, offender.Account)
		if !offender.CurrencyValuedLimit || offender.Other {
			currencyValuedLimit = false
		}
	}
	return currencyChangeCandidatesBlockedError{
		offenders: blockedAccounts, currencyValuedLimit: currencyValuedLimit,
	}
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

// auditCurrencyChangeRefusal records the attempted denomination change before
// returning its business refusal. code is the refused variant of the change's
// detail; params carry its values, and the refusal is added to them. An audit
// failure is an operational error.
func (n *localNode) auditCurrencyChangeRefusal(
	ctx context.Context, caller domain.Caller, entry store.AuditEntry,
	code domain.AuditDetailCode, params domain.AuditDetailParams, refusal error,
) error {
	params["refusal"] = refusal.Error()
	entry.Detail = domain.NewAuditDetail(code, params)
	if err := n.audit(context.WithoutCancel(ctx), caller, entry); err != nil {
		return fmt.Errorf("audit refused currency change (%v): %w", refusal, err)
	}
	return refusal
}

// currencyDetailParams names both sides of a denomination change next to the
// target values it applies to.
func currencyDetailParams(target domain.AuditDetailParams, before, after string) domain.AuditDetailParams {
	params := domain.AuditDetailParams{
		"from": currencyValue(before),
		"to":   currencyValue(after),
	}
	maps.Copy(params, target)
	return params
}

func currencyValue(currency string) string {
	if currency == "" {
		return domain.AuditDetailUnset
	}
	return currency
}
