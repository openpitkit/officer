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
// live engine, rejecting effective-currency changes for accounts with non-zero
// balance/P&L rows.
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

		if err := n.realm.SetAccountCurrency(ctx, key.Account, currency); err != nil {
			return fmt.Errorf("set account currency: %w", err)
		}
		if applyErr := applyAccountCurrency(ctx, lane, key.Account, currency); applyErr != nil {
			revertStoreErr := n.realm.SetAccountCurrency(ctx, key.Account, prev.Currency)
			revertEngineErr := applyAccountCurrency(ctx, lane, key.Account, prev.Currency)
			if revertErr := errors.Join(revertStoreErr, revertEngineErr); revertErr != nil {
				return n.fatalPostEngineAuditByCode(
					"revert account currency", "account", key.Account.String(),
					fmt.Errorf("revert account currency: %w", revertErr),
				)
			}
			return fmt.Errorf("apply account currency: %w", applyErr)
		}
		if err := n.audit(ctx, caller, store.AuditEntry{
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
// rebuilds the live engine from the new snapshot.
func (n *localNode) SetGroupCurrency(
	ctx context.Context, code string, currency string, caller domain.Caller,
) error {
	return n.setGroupCurrency(ctx, code, currency, caller, false)
}

// SetDefaultGroupCurrency sets or clears the reserved default group currency and
// rebuilds the live engine from the new snapshot.
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
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

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
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return fmt.Errorf("rebuild engine after group currency: %w", err)
	}
	label := code
	if defaultGroup {
		label = "default"
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetGroupCurrency,
		Detail: currencyDetail("set group currency", label, prevCurrency, currency),
	}); err != nil {
		return fmt.Errorf("audit group currency: %w", err)
	}
	return nil
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
		"deleting group would change effective currency for account(s) %s with balance or P&L rows",
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
		"account(s) %s hold balance or P&L rows so currency cannot change",
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
