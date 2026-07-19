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

package backend

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// ApplyAdjustment validates the account id and asset, routes to the owning
// node, and applies one spot-funds adjustment. The returned record carries the
// accepted-or-rejected outcome; a policy reject is a successful call, not an
// error, and is persisted to the adjustment history and audit log like an
// accepted one. An adjustment to an account that does not exist yet auto-creates
// it in the default group (no group assigned) before applying.
//
// externalID is the caller-supplied external id for the adjustment record. When
// non-zero it is used verbatim and must be canonical; a duplicate is rejected by
// the store with domain.ErrAlreadyExists. When zero the store mints one.
func (s *Service) ApplyAdjustment(
	ctx context.Context,
	account domain.AccountID,
	externalID domain.ExternalID,
	req domain.AdjustmentRequest,
) (domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if req.RealizedPnl != "" {
		normalized, err := domain.AddDecimals("", req.RealizedPnl)
		if err != nil {
			return domain.AccountAdjustmentRecord{}, err
		}
		req.RealizedPnl = normalized
	}
	n, err := s.router.Route(keyFor(account))
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("backend: route account: %w", err)
	}
	return n.ApplyAdjustment(ctx, keyFor(account), externalID, req, auth.CallerFromContext(ctx))
}

// SetBalanceRealizedPnl validates and stores the current realized P&L snapshot
// for one per-(account, asset) balance row through the account-adjustment
// history path. This is not a SpotFunds account-currency kill-switch accumulator
// seed.
func (s *Service) SetBalanceRealizedPnl(
	ctx context.Context,
	account domain.AccountID,
	asset string,
	realizedPnl string,
) (domain.Balance, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return domain.Balance{}, err
	}
	if err := domain.ValidateAsset(asset); err != nil {
		return domain.Balance{}, err
	}
	normalized, err := domain.AddDecimals("", realizedPnl)
	if err != nil {
		return domain.Balance{}, err
	}
	n, err := s.router.Route(keyFor(account))
	if err != nil {
		return domain.Balance{}, fmt.Errorf("backend: route account: %w", err)
	}
	return n.SetBalanceRealizedPnl(
		ctx,
		keyFor(account),
		asset,
		normalized,
		auth.CallerFromContext(ctx),
	)
}

// ImportPositionSnapshot validates and imports a complete persisted position
// snapshot. It is intentionally narrower than the public adjustment API: CSV
// import needs to round-trip realized P&L that the engine adjustment request
// cannot express as an absolute field.
func (s *Service) ImportPositionSnapshot(
	ctx context.Context, externalID domain.ExternalID, snapshot domain.Balance,
) (domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(snapshot.Account); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if _, err := domain.AddDecimals("", snapshot.RealizedPnl); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	n, err := s.router.Route(keyFor(snapshot.Account))
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("backend: route account: %w", err)
	}
	return n.ImportPositionSnapshot(
		ctx, keyFor(snapshot.Account), externalID, snapshot, auth.CallerFromContext(ctx))
}

// ListBalances returns the balance rows for the realm, optionally narrowed to a
// non-empty account and/or asset. It aggregates across nodes.
func (s *Service) ListBalances(
	ctx context.Context, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	page, err := s.ListBalanceRows(ctx, store.BalanceListFilter{
		Account: store.ExactTextMatcher(account.String()),
		Asset:   store.ExactTextMatcher(asset),
	})
	if err != nil {
		return nil, err
	}
	balances := make([]domain.Balance, 0, len(page.Rows))
	for _, row := range page.Rows {
		balances = append(balances, row.Balance)
	}
	return balances, nil
}

// ListBalanceRows returns balance rows aggregated across all nodes.
func (s *Service) ListBalanceRows(
	ctx context.Context, filter store.BalanceListFilter,
) (store.BalanceListPage, error) {
	if err := filter.Validate(); err != nil {
		return store.BalanceListPage{}, err
	}
	nodes := s.router.All()
	if len(nodes) == 1 {
		return nodes[0].ListBalanceRows(ctx, filter)
	}
	nodeFilter := filter
	nodeFilter.Page = nodePageForMerge(filter.Page)
	balances := make([]store.BalanceListRow, 0)
	total := 0
	for i, n := range nodes {
		part, err := n.ListBalanceRows(ctx, nodeFilter)
		if err != nil {
			return store.BalanceListPage{},
				fmt.Errorf("backend: node %d list balance rows: %w", i, err)
		}
		total += part.Total
		balances = append(balances, part.Rows...)
	}
	sortBalanceRows(balances, filter.Sort)
	balances = pageBalanceRows(balances, filter.Page)
	return store.BalanceListPage{Rows: balances, Total: total}, nil
}

// ListAdjustments validates the account id and returns the most recent n
// adjustments for the account, newest first; an empty source returns all.
func (s *Service) ListAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return nil, err
	}
	target, err := s.router.Route(keyFor(account))
	if err != nil {
		return nil, fmt.Errorf("backend: route account: %w", err)
	}
	return target.ListAdjustments(ctx, account, source, n)
}

// ListAllAdjustments returns the most recent n adjustments aggregated across
// all nodes and accounts, optionally narrowed to a non-empty account and/or
// source.
// It backs GET /adjustments. Per-node results are already newest-first; the
// merged slice is sorted newest-first and bounded to n.
func (s *Service) ListAllAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
		return s.ListAdjustments(ctx, account, source, n)
	}
	recs := make([]domain.AccountAdjustmentRecord, 0)
	for i, target := range s.router.All() {
		part, err := target.ListAdjustments(ctx, "", source, n)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list adjustments: %w", i, err)
		}
		recs = append(recs, part...)
	}
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].At.Equal(recs[j].At) {
			return recs[i].At.After(recs[j].At)
		}
		return recs[i].ExternalID.String() > recs[j].ExternalID.String()
	})
	if n > 0 && len(recs) > n {
		recs = recs[:n]
	}
	return recs, nil
}

// ListAdjustmentRows returns adjustments with DB-side filters/counts from each
// node.
func (s *Service) ListAdjustmentRows(
	ctx context.Context, filter store.AdjustmentListFilter,
) (store.AdjustmentListPage, error) {
	nodes := s.router.All()
	if len(nodes) == 1 {
		target, ok := nodes[0].(adjustmentRowNode)
		if !ok {
			return store.AdjustmentListPage{}, fmt.Errorf(
				"backend: node list adjustment rows: %w", domain.ErrNotImplemented,
			)
		}
		return target.ListAdjustmentRows(ctx, filter)
	}
	rows := make([]domain.AccountAdjustmentRecord, 0)
	total := 0
	nodeFilter := filter
	nodeFilter.Page = nodePageForMerge(filter.Page)
	for i, n := range nodes {
		target, ok := n.(adjustmentRowNode)
		if !ok {
			return store.AdjustmentListPage{}, fmt.Errorf(
				"backend: node %d list adjustment rows: %w", i, domain.ErrNotImplemented,
			)
		}
		part, err := target.ListAdjustmentRows(ctx, nodeFilter)
		if err != nil {
			return store.AdjustmentListPage{}, fmt.Errorf(
				"backend: node %d list adjustment rows: %w", i, err,
			)
		}
		total += part.Total
		rows = append(rows, part.Rows...)
	}
	sortAdjustmentRows(rows, filter.Sort)
	rows = pageAdjustmentRows(rows, filter.Page)
	return store.AdjustmentListPage{Rows: rows, Total: total}, nil
}

func sortAdjustmentRows(rows []domain.AccountAdjustmentRecord, spec store.SortSpec) {
	desc := spec.Descending
	column := spec.Column
	if column == "" {
		column = "at"
		desc = true
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch column {
		case "account":
			cmp = strings.Compare(left.Account.String(), right.Account.String())
		case "asset":
			cmp = strings.Compare(left.Asset, right.Asset)
		case "principal":
			cmp = strings.Compare(left.Principal, right.Principal)
		case "source":
			cmp = strings.Compare(string(left.Source), string(right.Source))
		case "status":
			cmp = strings.Compare(adjustmentStatus(left), adjustmentStatus(right))
		case "at":
			cmp = timeCompare(left.At, right.At)
		default:
			cmp = timeCompare(left.At, right.At)
		}
		if cmp == 0 {
			cmp = strings.Compare(left.ExternalID.String(), right.ExternalID.String())
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func adjustmentStatus(rec domain.AccountAdjustmentRecord) string {
	if rec.Rejected != nil {
		return string(domain.AdjustmentStatusRejected)
	}
	return string(domain.AdjustmentStatusAccepted)
}
