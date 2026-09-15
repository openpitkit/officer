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
	"errors"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

// ListLimits returns the typed barriers that reference account. An empty
// account returns all barriers.
func (s *Service) ListLimits(
	ctx context.Context, account domain.AccountID,
) (node.AccountLimits, error) {
	return s.node.ListLimits(ctx, account)
}

// ListPolicyRows returns the typed barriers flattened into one sorted, paged
// policy list, with the total matching count before paging.
func (s *Service) ListPolicyRows(
	ctx context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	return s.node.ListPolicyRows(ctx, filter)
}

// PutRateLimit validates the rate-limit barrier and upserts it. A barrier whose
// scope carries an account axis may name an account
// that does not exist yet; missing is the caller's required choice between
// registering that account and rejecting the request with
// domain.ErrAccountMissing, and is ignored for a scope that names no account.
func (s *Service) PutRateLimit(
	ctx context.Context, limit domain.LimitRate, missing domain.MissingAccountPolicy,
) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	if err := validateMissingAccountPolicy(limit.Account, missing); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	n := s.node
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	sink, err := n.PutRateLimit(ctx, limit, missing, caller)
	return s.finishLimitChangeLocked(sink, err)
}

// PutOrderSizeLimit validates the order-size barrier and upserts it. missing
// follows PutRateLimit.
func (s *Service) PutOrderSizeLimit(
	ctx context.Context,
	limit domain.LimitOrderSize,
	missing domain.MissingAccountPolicy,
) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	if err := validateMissingAccountPolicy(limit.Account, missing); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	n := s.node
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	sink, err := n.PutOrderSizeLimit(ctx, limit, missing, caller)
	return s.finishLimitChangeLocked(sink, err)
}

// PutSpotFundsPnlBoundsLimit validates the SpotFunds P&L-bounds barrier and
// upserts it. missing follows PutRateLimit.
func (s *Service) PutSpotFundsPnlBoundsLimit(
	ctx context.Context,
	limit domain.LimitSpotFundsPnlBounds,
	missing domain.MissingAccountPolicy,
) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateAsset(limit.Currency); err != nil {
		return err
	}
	if err := validateMissingAccountPolicy(limit.Account, missing); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	n := s.node
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	sink, err := n.PutSpotFundsPnlBoundsLimit(
		ctx, limit, missing, caller,
	)
	return s.finishLimitChangeLocked(sink, err)
}

// DeleteLimit validates the target's scope/axes for its policy and removes the
// addressed barrier.
func (s *Service) DeleteLimit(ctx context.Context, target node.LimitTarget) error {
	if err := validateLimitTarget(target); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	n := s.node
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	sink, err := n.DeleteLimit(ctx, target, caller)
	return s.finishLimitChangeLocked(sink, err)
}

func (s *Service) finishLimitChangeLocked(sink marketdata.Sink, err error) error {
	if sink == nil || s.md == nil {
		return err
	}
	s.md.Stop()
	restoreErr := s.restoreMarketDataAfterBackup(sink)
	if err != nil {
		return errors.Join(err, restoreErr)
	}
	return restoreErr
}

// validateLimitTarget checks the delete target's policy, scope, and axes.
func validateLimitTarget(target node.LimitTarget) error {
	return domain.ValidateLimitScopeAndAxes(
		target.Policy,
		target.Scope,
		target.Account,
		target.Asset,
		target.AccountGroup,
	)
}
