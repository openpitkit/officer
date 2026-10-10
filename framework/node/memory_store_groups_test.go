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
	"fmt"
	"slices"
	"sort"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (r *memoryRealm) CreateGroup(
	_ context.Context, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	if _, ok := r.groups[group.Code]; ok {
		return domain.AccountGroup{}, domain.ErrAlreadyExists
	}
	r.nextGroupID++
	group.EngineGroupID = domain.EngineGroupID(r.nextGroupID)
	r.groups[group.Code] = group
	return group, nil
}

func (r *memoryRealm) GetGroup(
	_ context.Context, code string,
) (domain.AccountGroup, bool, error) {
	group, ok := r.groups[code]
	return group, ok, nil
}

func (r *memoryRealm) ListGroups(context.Context) ([]domain.AccountGroup, error) {
	out := make([]domain.AccountGroup, 0, len(r.groups))
	for _, group := range r.groups {
		if group.Code == "" {
			continue
		}
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// ListGroupRows mirrors the SQLite group list. A non-zero account restriction
// keeps only the groups the restricted account is a member of, counting that
// member alone, and omits the default group.
func (r *memoryRealm) ListGroupRows(
	ctx context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	if filter.Venue != (store.VenueFilter{}) {
		return store.GroupListPage{}, fmt.Errorf(
			"memory store: venue filter is not supported: %w", domain.ErrNotImplemented)
	}
	groups, err := r.ListGroups(ctx)
	if err != nil {
		return store.GroupListPage{}, err
	}
	restricted := filter.Restriction.Account != ""
	out := make([]store.GroupListRow, 0, len(groups)+1)
	if !restricted {
		defaultRow := store.GroupListRow{
			Group: domain.AccountGroup{Code: "", Currency: r.groups[""].Currency},
		}
		for _, account := range r.accounts {
			if account.GroupCode != "" {
				continue
			}
			defaultRow.AccountCount++
			for _, balance := range r.balances {
				if balance.Account == account.Code {
					defaultRow.PositionCount++
				}
			}
		}
		out = append(out, defaultRow)
	}
	total := 0
	for _, group := range groups {
		row := store.GroupListRow{
			Group: group, VenueAccess: r.venueAccess("", group.Code),
		}
		for _, account := range r.accounts {
			if account.GroupCode != group.Code ||
				!restrictionMatches(filter.Restriction, account.Code) {
				continue
			}
			row.AccountCount++
			for _, balance := range r.balances {
				if balance.Account == account.Code {
					row.PositionCount++
				}
			}
		}
		if restricted && row.AccountCount == 0 {
			continue
		}
		out = append(out, row)
		total++
	}
	return store.GroupListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) SetGroupNotes(_ context.Context, code, notes string) error {
	group, ok := r.groups[code]
	if !ok {
		return domain.ErrNotFound
	}
	group.Notes = notes
	r.groups[code] = group
	return nil
}

func (r *memoryRealm) SetGroupCurrency(
	_ context.Context, code, currency string,
) error {
	if currency != "" {
		if _, ok := r.assets[currency]; !ok {
			return domain.ErrInvalid
		}
	}
	group, ok := r.groups[code]
	if !ok {
		if code != "" {
			return domain.ErrNotFound
		}
		group = domain.AccountGroup{Code: ""}
	}
	group.Currency = currency
	r.groups[code] = group
	return nil
}

func (r *memoryRealm) UpdateGroup(
	_ context.Context, oldCode string, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	current, ok := r.groups[oldCode]
	if !ok {
		return domain.AccountGroup{}, domain.ErrNotFound
	}
	if oldCode != group.Code {
		if _, ok := r.groups[group.Code]; ok {
			return domain.AccountGroup{}, domain.ErrAlreadyExists
		}
		delete(r.groups, oldCode)
		for id, account := range r.accounts {
			if account.GroupCode == oldCode {
				account.GroupCode = group.Code
				r.accounts[id] = account
			}
		}
		// The group's trading grants follow the rename, as SQLite's grants
		// reference the group row the rename keeps; tradingAccess is guarded
		// by mu like every trading method.
		r.mu.Lock()
		renamed := domain.TradingAccess{AccountGroup: oldCode}
		for i, access := range r.tradingAccess {
			if access.SameSubject(renamed) {
				r.tradingAccess[i].AccountGroup = group.Code
			}
		}
		r.mu.Unlock()
	}
	current.Code = group.Code
	current.Title = group.Title
	r.groups[group.Code] = current
	return current, nil
}

func (r *memoryRealm) SetGroupBlocked(
	_ context.Context, code string, blocked bool, reason string,
) error {
	group, ok := r.groups[code]
	if !ok {
		return domain.ErrNotFound
	}
	group.Blocked = blocked
	group.BlockReason = reason
	r.groups[code] = group
	return nil
}

func (r *memoryRealm) DeleteGroup(
	_ context.Context, code string, force bool,
) error {
	if _, ok := r.groups[code]; !ok {
		return domain.ErrNotFound
	}
	if !force {
		count := 0
		for _, limit := range r.spotFundsPnlBoundsLimits {
			if limit.Scope == domain.ScopeAccountGroup && limit.AccountGroup == code {
				count++
			}
		}
		if count > 0 {
			return domain.NewHasDependentsError([]domain.DependentCount{
				{Kind: "limit_spot_funds_pnl_bound", Count: count},
			})
		}
	}
	delete(r.groups, code)
	for id, account := range r.accounts {
		if account.GroupCode == code {
			account.GroupCode = ""
			r.accounts[id] = account
		}
	}
	for key, limit := range r.spotFundsPnlBoundsLimits {
		if limit.Scope == domain.ScopeAccountGroup && limit.AccountGroup == code {
			delete(r.spotFundsPnlBoundsLimits, key)
		}
	}
	// The group's trading grants go with it, as SQLite's ON DELETE CASCADE
	// drops them; tradingAccess is guarded by mu like every trading method.
	r.mu.Lock()
	defer r.mu.Unlock()
	group := domain.TradingAccess{AccountGroup: code}
	r.tradingAccess = slices.DeleteFunc(r.tradingAccess, func(access domain.TradingAccess) bool {
		return access.SameSubject(group)
	})
	return nil
}

func (r *memoryRealm) ListGroupAccounts(
	_ context.Context, code string,
) ([]domain.Account, error) {
	var out []domain.Account
	for _, account := range r.accounts {
		if account.GroupCode == code {
			out = append(out, r.accountWithCurrencyCascade(account))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}
