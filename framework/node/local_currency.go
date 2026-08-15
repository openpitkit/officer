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

// SetAccountCurrency sets or clears the account-level currency in the store and
// live engine. Effective-currency changes are rejected when the account has
// non-zero positions or P&L, a halted P&L, active orders, or active limits.
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
	defer endLane()
	return eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		prev, ok, err := n.realm.GetAccount(ctx, key.Account)
		if err != nil {
			return fmt.Errorf("read account for currency: %w", err)
		}
		if !ok {
			return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
		}
		nextEffective, _ := domain.ResolveCurrencyCascade(
			currency,
			prev.GroupCurrency,
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
		if prev.Currency == currency {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		mutationCtx := context.WithoutCancel(ctx)

		if err := n.realm.SetAccountCurrency(mutationCtx, key.Account, currency); err != nil {
			return fmt.Errorf("set account currency: %w", err)
		}
		if applyErr := applyAccountCurrency(
			mutationCtx, lane, key.Account, currency,
		); applyErr != nil {
			return n.revertAccountCurrency(
				mutationCtx,
				lane,
				key.Account,
				prev.Currency,
				fmt.Errorf("apply account currency: %w", applyErr),
			)
		}
		if err := n.audit(mutationCtx, caller, store.AuditEntry{
			Action:       domain.AuditActionSetAccountCurrency,
			Account:      key.Account,
			AccountTitle: prev.Title,
			Detail: currencyDetail(
				"set account currency",
				key.Account.String(),
				prev.Currency,
				currency,
			),
		}); err != nil {
			return n.fatalPostEngineAuditByCode(
				"audit account currency", "account", key.Account.String(),
				fmt.Errorf("audit account currency: %w", err),
			)
		}
		return nil
	})
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

// revertAccountCurrency undoes a persisted currency write once applying it to
// the live engine has failed. A successful revert restores only the account's
// currency in engine and store to the value it held before the attempt - any
// currency asset that ensureAccountCurrencyAssetRegisteredExclusive registered
// earlier in the same request stays committed - so cause keeps the
// classification the engine reported for it: the caller really did submit the
// currency the engine rejected. When the revert itself fails, engine and store
// are left disagreeing about the account's currency instead: the original
// apply cause and the revert failure both flow into the fatal-shutdown hook
// and into the error handed back to the caller, and that returned error is
// terminal so neither cause can be reclassified as a caller fault.
func (n *localNode) revertAccountCurrency(
	ctx context.Context,
	lane engine.AccountLane,
	account domain.AccountID,
	previous string,
	cause error,
) error {
	revertEngineErr := applyAccountCurrency(ctx, lane, account, previous)
	var revertStoreErr error
	if revertEngineErr == nil {
		revertStoreErr = n.realm.SetAccountCurrency(ctx, account, previous)
	}
	if revertErr := errors.Join(revertEngineErr, revertStoreErr); revertErr != nil {
		return n.fatalPostEngineAuditByCode(
			"revert account currency", "account", account.String(),
			errors.Join(
				cause,
				fmt.Errorf("revert account currency: %w", revertErr),
			),
		)
	}
	return cause
}

func applyAccountCurrency(
	ctx context.Context,
	lane engine.AccountLane,
	account domain.AccountID,
	currency string,
) error {
	if currency == "" {
		return lane.ClearAccountCurrency(ctx, account)
	}
	return lane.SetAccountCurrency(ctx, account, currency)
}

// SetGroupCurrency sets or clears a persisted account-group currency and
// applies the group-level SDK state through its async-engine group lane.
func (n *localNode) SetGroupCurrency(
	ctx context.Context, code string, currency string, caller domain.Caller,
) error {
	return n.setGroupCurrency(ctx, code, currency, caller, false)
}

// SetDefaultGroupCurrency sets or clears the reserved default group currency and
// applies the default-group SDK state through its async-engine group lane.
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
	} else {
		if prev, ok, err := n.realm.GetGroup(ctx, ""); err != nil {
			return fmt.Errorf("read default group currency: %w", err)
		} else if ok {
			prevCurrency = prev.Currency
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
	if err := n.realm.SetGroupCurrency(ctx, code, currency); err != nil {
		return fmt.Errorf("set group currency: %w", err)
	}
	eng := n.currentEngine()
	applyErr := eng.RunGroupSynchronized(ctx, code, func(lane engine.GroupLane) error {
		return applyGroupCurrency(ctx, lane, code, currency)
	})
	if applyErr != nil {
		mutationCtx := context.WithoutCancel(ctx)
		revertRuntimeErr := eng.RunGroupSynchronized(
			mutationCtx, code, func(lane engine.GroupLane) error {
				return applyGroupCurrency(mutationCtx, lane, code, prevCurrency)
			},
		)
		revertStoreErr := n.realm.SetGroupCurrency(mutationCtx, code, prevCurrency)
		if revertRuntimeErr != nil || revertStoreErr != nil {
			cause := errors.Join(
				fmt.Errorf("apply group currency: %w", applyErr),
				optionalOperationError("restore group currency runtime", revertRuntimeErr),
				optionalOperationError("restore group currency store", revertStoreErr),
			)
			return internalPostCommitNodeMutationError(
				n.reconcileEngineAfterFailure(
					mutationCtx, "reconcile engine after group currency failure", cause,
				).err,
			)
		}
		return fmt.Errorf("apply group currency: %w", applyErr)
	}
	label := code
	if defaultGroup {
		label = "default"
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action: domain.AuditActionSetGroupCurrency,
		Group:  code,
		Detail: currencyDetail("set group currency", label, prevCurrency, currency),
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit group currency", "group", label,
			fmt.Errorf("audit group currency: %w", err),
		)
	}
	return nil
}

func applyGroupCurrency(
	ctx context.Context,
	lane engine.GroupLane,
	group string,
	currency string,
) error {
	if currency == "" {
		return lane.ClearGroupCurrency(ctx, group)
	}
	return lane.SetGroupCurrency(ctx, group, currency)
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
