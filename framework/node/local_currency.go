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
	"strings"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// SetAccountCurrency sets or clears the account-level currency in the store and
// live engine, rejecting effective-currency changes for accounts holding
// non-zero positions or P&L, whose values are denominated in the old currency
// and would need recomputing. A halted P&L does not reject the change; the
// human Officer decides what to do with such an account.
func (n *localNode) SetAccountCurrency(
	ctx context.Context, key Key, currency string, caller domain.Caller,
) error {
	eng, done, err := n.beginLane()
	if err != nil {
		return err
	}
	defer done()

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
			return err
		}
		if prev.Currency == currency {
			return nil
		}
		if err := n.ensureCurrencyAsset(
			ctx, currency, "set account currency", caller,
		); err != nil {
			return err
		}
		resetPnl, err := n.shouldResetHaltedAccountPnl(ctx, key.Account, prev)
		if err != nil {
			return err
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
		if resetPnl {
			blocks, resetErr := lane.SetAccountPnl(mutationCtx, key.Account, "0")
			if resetErr != nil {
				return n.revertAccountCurrency(
					mutationCtx,
					lane,
					key.Account,
					prev.Currency,
					fmt.Errorf("reset account pnl: %w", resetErr),
				)
			}
			if err := n.persistHaltedAccountPnlReset(
				mutationCtx, key.Account, prev, caller, blocks,
			); err != nil {
				return err
			}
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

func (n *localNode) shouldResetHaltedAccountPnl(
	ctx context.Context, account domain.AccountID, prev domain.Account,
) (bool, error) {
	if prev.PnlHaltReason == "" {
		return false, nil
	}
	open, err := n.realm.ListAccountsWithOpenBalances(ctx, []domain.AccountID{account})
	if err != nil {
		return false, fmt.Errorf("list account balances for pnl reset: %w", err)
	}
	return len(open) == 0, nil
}

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
			fmt.Errorf("revert account currency: %w", revertErr),
		)
	}
	return cause
}

func (n *localNode) persistHaltedAccountPnlReset(
	ctx context.Context,
	account domain.AccountID,
	prev domain.Account,
	caller domain.Caller,
	blocks []domain.AccountBlock,
) error {
	if err := n.realm.SetAccountPnl(ctx, account, "0", ""); err != nil {
		return n.fatalPostEngineAuditByCode(
			"record account pnl reset", "account", account.String(),
			fmt.Errorf("record account pnl reset: %w", err),
		)
	}
	if err := n.mirrorPolicyConfigurationBlocks(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, blocks,
	); err != nil {
		return err
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionSetAccountCurrency,
		Account:      account,
		AccountTitle: prev.Title,
		Detail: fmt.Sprintf(
			"reset halted account pnl %s %s -> 0 on currency change",
			account.String(),
			prev.PnlHaltReason,
		),
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit account pnl reset", "account", account.String(),
			fmt.Errorf("audit account pnl reset: %w", err),
		)
	}
	return nil
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
		if err := n.guardGroupCurrencyChange(ctx, code, currency); err != nil {
			return err
		}
	} else {
		if prev, ok, err := n.realm.GetGroup(ctx, ""); err != nil {
			return fmt.Errorf("read default group currency: %w", err)
		} else if ok {
			prevCurrency = prev.Currency
		}
		if err := n.guardDefaultGroupCurrencyChange(ctx, currency); err != nil {
			return err
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
			return n.reconcileEngineAfterFailure(
				mutationCtx, "reconcile engine after group currency failure", cause,
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
			"",
			currency,
			account.DefaultCurrency,
		)
		if account.EffectiveCurrency != nextEffective {
			candidates = append(candidates, account.Code)
		}
	}
	return n.guardCurrencyCandidates(ctx, candidates)
}

func (n *localNode) guardGroupDeleteCurrencyChange(
	ctx context.Context,
	groupCode string,
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
			"",
			"",
			account.DefaultCurrency,
		)
		if account.EffectiveCurrency != nextEffective {
			candidates = append(candidates, account.Code)
		}
	}
	return n.guardCurrencyCandidatesWithMessage(
		ctx,
		candidates,
		"deleting group would change effective currency for account(s) %s holding non-zero positions or P&L",
	)
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

func (n *localNode) guardCurrencyCandidates(
	ctx context.Context,
	accounts []domain.AccountID,
) error {
	return n.guardCurrencyCandidatesWithMessage(
		ctx,
		accounts,
		"account(s) %s hold non-zero positions or P&L so currency cannot change",
	)
}

func (n *localNode) guardCurrencyCandidatesWithMessage(
	ctx context.Context,
	accounts []domain.AccountID,
	message string,
) error {
	if len(accounts) == 0 {
		return nil
	}
	offenders, err := n.realm.ListAccountsWithOpenBalances(ctx, accounts)
	if err != nil {
		return fmt.Errorf("list currency guard balances: %w", err)
	}
	if len(offenders) == 0 {
		return nil
	}
	parts := make([]string, 0, len(offenders))
	for _, offender := range offenders {
		parts = append(parts, offender.String())
	}
	return fmt.Errorf(
		message+": %w",
		strings.Join(parts, ", "),
		domain.ErrInvalid,
	)
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
