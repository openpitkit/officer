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
	"sort"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (r *memoryRealm) UpsertBalance(_ context.Context, balance domain.Balance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.balances[balanceKey(balance.Account, balance.Asset)] = balance
	return nil
}

func (r *memoryRealm) GetBalance(
	_ context.Context, account domain.AccountID, asset string,
) (domain.Balance, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	balance, ok := r.balances[balanceKey(account, asset)]
	return balance, ok, nil
}

func (r *memoryRealm) ListBalances(
	_ context.Context, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []domain.Balance
	for _, balance := range r.balances {
		if account != "" && balance.Account != account {
			continue
		}
		if asset != "" && balance.Asset != asset {
			continue
		}
		out = append(out, balance)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account == out[j].Account {
			return out[i].Asset < out[j].Asset
		}
		return out[i].Account < out[j].Account
	})
	return out, nil
}

func (r *memoryRealm) ListAccountsWithOpenBalances(
	_ context.Context, accounts []domain.AccountID,
) ([]domain.AccountID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	allow := make(map[domain.AccountID]struct{}, len(accounts))
	for _, account := range accounts {
		allow[account] = struct{}{}
	}
	seen := make(map[domain.AccountID]struct{})
	for _, balance := range r.balances {
		if len(allow) > 0 {
			if _, ok := allow[balance.Account]; !ok {
				continue
			}
		}
		if balanceIsEmpty(balance) {
			continue
		}
		seen[balance.Account] = struct{}{}
	}
	out := make([]domain.AccountID, 0, len(seen))
	for account := range seen {
		out = append(out, account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (r *memoryRealm) ListBalanceRows(
	ctx context.Context, filter store.BalanceListFilter,
) (store.BalanceListPage, error) {
	balances, err := r.ListBalances(ctx, "", "")
	if err != nil {
		return store.BalanceListPage{}, err
	}
	rows := make([]store.BalanceListRow, 0, len(balances))
	for _, balance := range balances {
		row := store.BalanceListRow{Balance: balance}
		if !textMatches(filter.Account, string(balance.Account)) ||
			!textMatches(filter.Asset, balance.Asset) ||
			!decimalMatches(filter.Available, balance.Available) ||
			!decimalMatches(filter.Held, balance.Held) ||
			!decimalMatches(filter.Incoming, balance.Incoming) ||
			!decimalMatches(filter.AverageEntryPrice, balance.AverageEntryPrice) ||
			!decimalMatches(filter.RealizedPnl, balance.RealizedPnl) ||
			!timeMatches(filter.UpdatedAt, balance.UpdatedAt) {
			continue
		}
		if filter.GroupCode != nil {
			account, ok := r.accounts[balance.Account]
			if !ok || account.GroupCode != *filter.GroupCode {
				continue
			}
		}
		rows = append(rows, row)
	}
	sortBalanceRows(rows, filter.Sort)
	total := len(rows)
	rows = pageRows(rows, filter.Page)
	return store.BalanceListPage{Rows: rows, Total: total}, nil
}

func (r *memoryRealm) DeleteBalance(
	_ context.Context, account domain.AccountID, asset string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := balanceKey(account, asset)
	if _, ok := r.balances[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.balances, key)
	return nil
}

func sortBalanceRows(rows []store.BalanceListRow, spec store.SortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Balance, rows[j].Balance
		cmp := 0
		switch spec.Column {
		case "asset":
			cmp = compareStrings(left.Asset, right.Asset)
		case "available":
			cmp = compareDecimalText(left.Available, right.Available)
		case "held":
			cmp = compareDecimalText(left.Held, right.Held)
		case "incoming":
			cmp = compareDecimalText(left.Incoming, right.Incoming)
		case "averageEntryPrice":
			cmp = compareDecimalText(left.AverageEntryPrice, right.AverageEntryPrice)
		case "realizedPnl":
			cmp = compareDecimalText(left.RealizedPnl, right.RealizedPnl)
		case "updatedAt":
			cmp = compareTimes(left.UpdatedAt, right.UpdatedAt)
		default:
			cmp = compareStrings(string(left.Account), string(right.Account))
		}
		if cmp == 0 {
			cmp = compareStrings(string(left.Account), string(right.Account))
		}
		if cmp == 0 {
			cmp = compareStrings(left.Asset, right.Asset)
		}
		return sortCompare(cmp, spec.Descending)
	})
}

func (r *memoryRealm) AppendAdjustment(
	_ context.Context, rec domain.AccountAdjustmentRecord,
) (domain.AccountAdjustmentRecord, error) {
	if rec.ExternalID.IsZero() {
		rec.ExternalID = r.nextExternalID()
	} else {
		for _, existing := range r.adjustments {
			if existing.ExternalID == rec.ExternalID {
				return domain.AccountAdjustmentRecord{}, domain.ErrAlreadyExists
			}
		}
	}
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	r.adjustments = append(r.adjustments, rec)
	return rec, nil
}

func (r *memoryRealm) RecordAccountAdjustment(
	ctx context.Context, in store.AccountAdjustmentPersistence,
) (domain.AccountAdjustmentRecord, error) {
	snapshot := r.exportData(ctx)
	if in.UpsertBalance != nil {
		if err := r.UpsertBalance(ctx, *in.UpsertBalance); err != nil {
			r.restoreData(snapshot)
			return domain.AccountAdjustmentRecord{}, err
		}
	}
	if in.DeleteBalance != nil {
		if err := r.DeleteBalance(ctx, in.DeleteBalance.Account, in.DeleteBalance.Asset); err != nil &&
			!errors.Is(err, domain.ErrNotFound) {
			r.restoreData(snapshot)
			return domain.AccountAdjustmentRecord{}, err
		}
	}
	stored, err := r.AppendAdjustment(ctx, in.Adjustment)
	if err != nil {
		r.restoreData(snapshot)
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := r.AppendAudit(ctx, in.Audit); err != nil {
		r.restoreData(snapshot)
		return domain.AccountAdjustmentRecord{}, err
	}
	return stored, nil
}

func (r *memoryRealm) ListAdjustments(
	_ context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if n <= 0 {
		return []domain.AccountAdjustmentRecord{}, nil
	}
	var out []domain.AccountAdjustmentRecord
	for i := len(r.adjustments) - 1; i >= 0 && len(out) < n; i-- {
		rec := r.adjustments[i]
		if account != "" && rec.Account != account {
			continue
		}
		if source != "" && rec.Source != source {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func (r *memoryRealm) ListAdjustmentRows(
	ctx context.Context, filter store.AdjustmentListFilter,
) (store.AdjustmentListPage, error) {
	records, err := r.ListAdjustments(ctx, "", "", len(r.adjustments))
	if err != nil {
		return store.AdjustmentListPage{}, err
	}
	out := make([]domain.AccountAdjustmentRecord, 0, len(records))
	for _, rec := range records {
		if !textMatches(filter.Account, rec.Account.String()) ||
			!textMatches(filter.Asset, rec.Asset) {
			continue
		}
		if filter.Source != "" && rec.Source != filter.Source {
			continue
		}
		if filter.Status != nil && adjustmentStatus(rec) != *filter.Status {
			continue
		}
		out = append(out, rec)
	}
	total := len(out)
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(out))
		end := min(start+filter.Page.Limit, len(out))
		out = out[start:end]
	}
	return store.AdjustmentListPage{Rows: out, Total: total}, nil
}

func adjustmentStatus(rec domain.AccountAdjustmentRecord) domain.AdjustmentStatus {
	if rec.Rejected != nil {
		return domain.AdjustmentStatusRejected
	}
	return domain.AdjustmentStatusAccepted
}
