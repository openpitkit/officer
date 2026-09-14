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
	"sort"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// ApplyAdjustment validates the account id and asset and applies one
// spot-funds adjustment. The returned record carries the
// accepted-or-rejected outcome; a policy reject is a successful call, not an
// error, and is persisted to the adjustment history and audit log like an
// accepted one. missing is the caller's required choice for an account that does
// not exist yet: register it with no group and no currency, or fail with
// domain.ErrAccountMissing.
//
// externalID is the caller-supplied external id for the adjustment record. When
// non-zero it is used verbatim and must be canonical; a duplicate is rejected by
// the store with domain.ErrAlreadyExists. When zero the store mints one.
func (s *Service) ApplyAdjustment(
	ctx context.Context,
	account domain.AccountID,
	externalID domain.ExternalID,
	req domain.AdjustmentRequest,
	missing domain.MissingAccountPolicy,
) (domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := validateMissingAccountPolicy(account, missing); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if req.RealizedPnl != "" {
		normalized, err := domain.AddDecimals("", req.RealizedPnl)
		if err != nil {
			return domain.AccountAdjustmentRecord{}, err
		}
		req.RealizedPnl = normalized
	}
	return s.node.ApplyAdjustment(
		ctx, account, externalID, req, missing, auth.CallerFromContext(ctx),
	)
}

// SetBalanceRealizedPnl validates and stores the current realized P&L snapshot
// for one per-(account, asset) balance row through the account-adjustment
// history path. This is not a SpotFunds account-currency kill-switch accumulator
// seed. missing follows ApplyAdjustment.
func (s *Service) SetBalanceRealizedPnl(
	ctx context.Context,
	account domain.AccountID,
	asset string,
	realizedPnl string,
	missing domain.MissingAccountPolicy,
) (domain.Balance, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return domain.Balance{}, err
	}
	if err := validateMissingAccountPolicy(account, missing); err != nil {
		return domain.Balance{}, err
	}
	if err := domain.ValidateAsset(asset); err != nil {
		return domain.Balance{}, err
	}
	normalized, err := domain.AddDecimals("", realizedPnl)
	if err != nil {
		return domain.Balance{}, err
	}
	return s.node.SetBalanceRealizedPnl(
		ctx,
		account,
		asset,
		normalized,
		missing,
		auth.CallerFromContext(ctx),
	)
}

// ListBalances returns the balance rows for the realm, optionally narrowed to a
// non-empty account and/or asset.
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

// ListBalanceRows returns balance rows matching filter.
func (s *Service) ListBalanceRows(
	ctx context.Context, filter store.BalanceListFilter,
) (store.BalanceListPage, error) {
	if err := filter.Validate(); err != nil {
		return store.BalanceListPage{}, err
	}
	return s.node.ListBalanceRows(ctx, filter)
}

// ListAdjustments validates the account id and returns the most recent n
// adjustments for the account, newest first; an empty source returns all.
func (s *Service) ListAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return nil, err
	}
	return s.node.ListAdjustments(ctx, account, source, n)
}

// ListAllAdjustments returns the most recent n adjustments across accounts,
// newest first, optionally narrowed to a non-empty account and/or source.
func (s *Service) ListAllAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
		return s.node.ListAdjustments(ctx, account, source, n)
	}
	recs, err := s.node.ListAdjustments(ctx, "", source, n)
	if err != nil {
		return nil, err
	}
	// Newest first, as sortOrdersNewestFirst orders orders: fixed here rather
	// than left to the node, with ties broken on the external id.
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].At.Equal(recs[j].At) {
			return recs[i].At.After(recs[j].At)
		}
		return recs[i].ExternalID.String() > recs[j].ExternalID.String()
	})
	return recs, nil
}

// ListAdjustmentRows returns adjustments with DB-side filters and counts.
func (s *Service) ListAdjustmentRows(
	ctx context.Context, filter store.AdjustmentListFilter,
) (store.AdjustmentListPage, error) {
	return s.node.ListAdjustmentRows(ctx, filter)
}
