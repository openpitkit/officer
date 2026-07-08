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
	"sort"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (r *memoryRealm) CreatePrincipal(
	_ context.Context, principal domain.Principal,
) error {
	if _, ok := r.principals[principal.Code]; ok {
		return domain.ErrAlreadyExists
	}
	r.principals[principal.Code] = principal
	return nil
}

func (r *memoryRealm) GetPrincipal(
	_ context.Context, code string,
) (domain.Principal, bool, error) {
	principal, ok := r.principals[code]
	return principal, ok, nil
}

func (r *memoryRealm) ListPrincipals(context.Context) ([]domain.Principal, error) {
	out := make([]domain.Principal, 0, len(r.principals))
	for _, principal := range r.principals {
		out = append(out, principal)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (r *memoryRealm) UpdatePrincipal(
	_ context.Context, principal domain.Principal,
) error {
	if _, ok := r.principals[principal.Code]; !ok {
		return domain.ErrNotFound
	}
	r.principals[principal.Code] = principal
	return nil
}

func (r *memoryRealm) DeletePrincipal(_ context.Context, code string) error {
	if _, ok := r.principals[code]; !ok {
		return domain.ErrNotFound
	}
	delete(r.principals, code)
	return nil
}

func (r *memoryRealm) CreateAccount(
	_ context.Context, account domain.Account,
) (domain.Account, error) {
	if _, ok := r.accounts[account.Code]; ok {
		return domain.Account{}, domain.ErrAlreadyExists
	}
	if account.GroupCode != "" {
		if _, ok := r.groups[account.GroupCode]; !ok {
			return domain.Account{}, domain.ErrInvalid
		}
	}
	r.nextAccountID++
	account.EngineAccountID = r.nextAccountID
	r.accounts[account.Code] = account
	return account, nil
}

func (r *memoryRealm) GetAccount(
	_ context.Context, code domain.AccountID,
) (domain.Account, bool, error) {
	account, ok := r.accounts[code]
	account = r.accountWithCurrencyCascade(account)
	return account, ok, nil
}

func (r *memoryRealm) ListAccounts(context.Context) ([]domain.Account, error) {
	out := make([]domain.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		out = append(out, r.accountWithCurrencyCascade(account))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (r *memoryRealm) ListAccountRows(
	ctx context.Context, filter store.AccountListFilter,
) (store.AccountListPage, error) {
	accounts, err := r.ListAccounts(ctx)
	if err != nil {
		return store.AccountListPage{}, err
	}
	out := make([]store.AccountListRow, 0, len(accounts))
	for _, account := range accounts {
		row := store.AccountListRow{Account: account}
		for _, balance := range r.balances {
			if balance.Account == account.Code {
				row.PositionCount++
			}
		}
		if !textMatchesAny(filter.Code, string(row.Account.Code), row.Account.Title) ||
			!textMatches(filter.BlockReason, row.Account.BlockReason) ||
			!statusMatches(filter.Status, row.Account.Blocked) ||
			!countMatches(filter.Position, row.PositionCount) {
			continue
		}
		if filter.GroupCode != nil && row.Account.GroupCode != *filter.GroupCode {
			continue
		}
		out = append(out, row)
	}
	sortAccountRows(out, filter.Sort)
	total := len(out)
	out = pageRows(out, filter.Page)
	return store.AccountListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) SetAccountBlocked(
	_ context.Context, code domain.AccountID, blocked bool, reason string,
) error {
	account, ok := r.accounts[code]
	if !ok {
		return domain.ErrNotFound
	}
	account.Blocked = blocked
	account.BlockReason = reason
	r.accounts[code] = account
	return nil
}

func (r *memoryRealm) SetAccountGroup(
	_ context.Context, code domain.AccountID, groupCode string,
) error {
	account, ok := r.accounts[code]
	if !ok {
		return domain.ErrNotFound
	}
	if groupCode != "" {
		if _, ok := r.groups[groupCode]; !ok {
			return domain.ErrInvalid
		}
	}
	account.GroupCode = groupCode
	r.accounts[code] = account
	return nil
}

func (r *memoryRealm) SetAccountCurrency(
	_ context.Context, code domain.AccountID, currency string,
) error {
	account, ok := r.accounts[code]
	if !ok {
		return domain.ErrNotFound
	}
	if currency != "" {
		if _, ok := r.assets[currency]; !ok {
			return domain.ErrInvalid
		}
	}
	account.Currency = currency
	r.accounts[code] = account
	return nil
}

func (r *memoryRealm) SetAccountNotes(
	_ context.Context, code domain.AccountID, notes string,
) error {
	account, ok := r.accounts[code]
	if !ok {
		return domain.ErrNotFound
	}
	account.Notes = notes
	r.accounts[code] = account
	return nil
}

func (r *memoryRealm) accountWithCurrencyCascade(account domain.Account) domain.Account {
	groupCurrency := ""
	if account.GroupCode != "" {
		if group, ok := r.groups[account.GroupCode]; ok {
			groupCurrency = group.Currency
		}
	}
	defaultCurrency := ""
	if group, ok := r.groups[""]; ok {
		defaultCurrency = group.Currency
	}
	account.GroupCurrency = groupCurrency
	account.DefaultCurrency = defaultCurrency
	account.EffectiveCurrency, account.CurrencyOrigin =
		domain.ResolveCurrencyCascade(account.Currency, groupCurrency, defaultCurrency)
	return account
}

func (r *memoryRealm) UpdateAccount(
	_ context.Context, oldCode domain.AccountID, account domain.Account,
) (domain.Account, error) {
	current, ok := r.accounts[oldCode]
	if !ok {
		return domain.Account{}, domain.ErrNotFound
	}
	if oldCode != account.Code {
		if _, ok := r.accounts[account.Code]; ok {
			return domain.Account{}, domain.ErrAlreadyExists
		}
		delete(r.accounts, oldCode)
		r.renameAccountReferences(oldCode, account.Code)
	}
	current.Code = account.Code
	current.Title = account.Title
	r.accounts[account.Code] = current
	return current, nil
}

func (r *memoryRealm) renameAccountReferences(oldCode, newCode domain.AccountID) {
	nextBalances := map[string]domain.Balance{}
	for key, balance := range r.balances {
		if balance.Account == oldCode {
			balance.Account = newCode
			key = balanceKey(newCode, balance.Asset)
		}
		nextBalances[key] = balance
	}
	r.balances = nextBalances

	r.rateLimits = renameAccountLimits(r.rateLimits, oldCode, newCode)
	r.orderSizeLimits = renameAccountLimits(r.orderSizeLimits, oldCode, newCode)
	r.pnlBoundsLimits = renameAccountLimits(r.pnlBoundsLimits, oldCode, newCode)

	for i := range r.adjustments {
		if r.adjustments[i].Account == oldCode {
			r.adjustments[i].Account = newCode
		}
	}
	for id, order := range r.orders {
		if order.Account == oldCode {
			order.Account = newCode
			r.orders[id] = order
		}
	}
	for i := range r.trades {
		if r.trades[i].Account == oldCode {
			r.trades[i].Account = newCode
		}
	}
}

func renameAccountLimits[Limit interface {
	domain.LimitRate | domain.LimitOrderSize | domain.LimitPnlBounds
}](
	limits map[string]Limit,
	oldCode domain.AccountID,
	newCode domain.AccountID,
) map[string]Limit {
	out := map[string]Limit{}
	for key, limit := range limits {
		switch typed := any(limit).(type) {
		case domain.LimitRate:
			if typed.Account == oldCode {
				typed.Account = newCode
				key = limitKey(typed.Scope, typed.Account, typed.Asset)
			}
			out[key] = any(typed).(Limit)
		case domain.LimitOrderSize:
			if typed.Account == oldCode {
				typed.Account = newCode
				key = limitKey(typed.Scope, typed.Account, typed.Asset)
			}
			out[key] = any(typed).(Limit)
		case domain.LimitPnlBounds:
			if typed.Account == oldCode {
				typed.Account = newCode
				key = limitKey(typed.Scope, typed.Account, typed.Asset)
			}
			out[key] = any(typed).(Limit)
		}
	}
	return out
}

func (r *memoryRealm) DeleteAccount(
	_ context.Context, code domain.AccountID, _ bool,
) error {
	if _, ok := r.accounts[code]; !ok {
		return domain.ErrNotFound
	}
	delete(r.accounts, code)
	return nil
}

func sortAccountRows(rows []store.AccountListRow, spec store.SortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch spec.Column {
		case "blockReason":
			cmp = compareStrings(left.Account.BlockReason, right.Account.BlockReason)
		case "group":
			cmp = compareStrings(left.Account.GroupCode, right.Account.GroupCode)
		case "positionCount":
			cmp = compareInts(left.PositionCount, right.PositionCount)
		case "status":
			cmp = compareBools(left.Account.Blocked, right.Account.Blocked)
		case "title":
			cmp = compareStrings(left.Account.Title, right.Account.Title)
		default:
			cmp = compareStrings(string(left.Account.Code), string(right.Account.Code))
		}
		if cmp == 0 {
			cmp = compareStrings(string(left.Account.Code), string(right.Account.Code))
		}
		return sortCompare(cmp, spec.Descending)
	})
}
