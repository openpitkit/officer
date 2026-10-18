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

package backend

import (
	"context"
	"errors"
	"slices"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

// ListLimits returns the typed barriers that reference account. An empty
// account returns all barriers. A named account is an exact account input, so
// the account scope resolves it before the node reads its barriers. The result
// keeps a barrier only when the scope shows the caller its account ("" for a
// barrier with no account) and the barrier names no group or a group the
// caller sees, by visibleGroups.
func (s *Service) ListLimits(
	ctx context.Context, account domain.AccountID,
) (node.AccountLimits, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return node.AccountLimits{}, err
	}
	if account != "" {
		if account, err = s.scope.Account(caller, account); err != nil {
			return node.AccountLimits{}, err
		}
	}
	groupVisible, err := s.visibleGroups(ctx, caller)
	if err != nil {
		return node.AccountLimits{}, err
	}
	limits, err := s.node.ListLimits(ctx, account)
	if err != nil {
		return node.AccountLimits{}, err
	}
	hidden := func(owner domain.AccountID, group string) bool {
		return s.scope.Visible(caller, owner) != nil ||
			(group != "" && !groupVisible(group))
	}
	limits.RateLimits = slices.DeleteFunc(
		slices.Clone(limits.RateLimits),
		func(limit domain.LimitRate) bool { return hidden(limit.Account, "") },
	)
	limits.OrderSizeLimits = slices.DeleteFunc(
		slices.Clone(limits.OrderSizeLimits),
		func(limit domain.LimitOrderSize) bool { return hidden(limit.Account, "") },
	)
	limits.SpotFundsPnlBoundsLimits = slices.DeleteFunc(
		slices.Clone(limits.SpotFundsPnlBoundsLimits),
		func(limit domain.LimitSpotFundsPnlBounds) bool {
			return hidden(limit.Account, limit.AccountGroup)
		},
	)
	return limits, nil
}

// ListPolicyRows returns the typed barriers flattened into one sorted, paged
// policy list, with the total matching count before paging, narrowed by the
// account scope's list restriction; the account filter is a text pattern, so
// the scope is asked with no requested account. The restriction replaces any
// filter.Restriction the caller supplies.
func (s *Service) ListPolicyRows(
	ctx context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	var err error
	filter.Restriction, err = s.listRestriction(ctx, "")
	if err != nil {
		return store.PolicyListPage{}, err
	}
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
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	scoped := limit
	scoped.Account, err = s.scopeLimitSubject(caller, limit.Scope, limit.Account, "")
	if err != nil {
		return err
	}
	scopedMissing, err := s.scope.MissingAccount(caller, missing)
	if err != nil {
		return err
	}
	if err := scoped.Validate(); err != nil {
		return err
	}
	if err := validateMissingAccountPolicy(limit.Account, missing); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	n := s.node
	sink, err := n.PutRateLimit(ctx, scoped, scopedMissing, caller)
	return s.finishLimitChangeLocked(sink, err)
}

// PutOrderSizeLimit validates the order-size barrier and upserts it. missing
// follows PutRateLimit.
func (s *Service) PutOrderSizeLimit(
	ctx context.Context,
	limit domain.LimitOrderSize,
	missing domain.MissingAccountPolicy,
) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	scoped := limit
	scoped.Account, err = s.scopeLimitSubject(caller, limit.Scope, limit.Account, "")
	if err != nil {
		return err
	}
	scopedMissing, err := s.scope.MissingAccount(caller, missing)
	if err != nil {
		return err
	}
	if err := scoped.Validate(); err != nil {
		return err
	}
	if err := validateMissingAccountPolicy(limit.Account, missing); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	n := s.node
	sink, err := n.PutOrderSizeLimit(ctx, scoped, scopedMissing, caller)
	return s.finishLimitChangeLocked(sink, err)
}

// PutSpotFundsPnlBoundsLimit validates the SpotFunds P&L-bounds barrier and
// upserts it. missing follows PutRateLimit.
func (s *Service) PutSpotFundsPnlBoundsLimit(
	ctx context.Context,
	limit domain.LimitSpotFundsPnlBounds,
	missing domain.MissingAccountPolicy,
) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	scoped := limit
	scoped.Account, err = s.scopeLimitSubject(
		caller, limit.Scope, limit.Account, limit.AccountGroup,
	)
	if err != nil {
		return err
	}
	scopedMissing, err := s.scope.MissingAccount(caller, missing)
	if err != nil {
		return err
	}
	if err := scoped.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateAsset(scoped.Currency); err != nil {
		return err
	}
	if err := validateMissingAccountPolicy(limit.Account, missing); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	n := s.node
	sink, err := n.PutSpotFundsPnlBoundsLimit(
		ctx, scoped, scopedMissing, caller,
	)
	return s.finishLimitChangeLocked(sink, err)
}

// DeleteLimit validates the target's scope/axes for its policy and removes the
// addressed barrier.
func (s *Service) DeleteLimit(ctx context.Context, target node.LimitTarget) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	target.Account, err = s.scopeLimitSubject(
		caller, target.Scope, target.Account, target.AccountGroup,
	)
	if err != nil {
		return err
	}
	if err := validateLimitTarget(target); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	n := s.node
	sink, err := n.DeleteLimit(ctx, target, caller)
	return s.finishLimitChangeLocked(sink, err)
}

// scopeLimitSubject passes the subject a limit scope carries through the
// account scope: the account of a scope with an account axis is resolved, the
// group of an account_group scope is checked, and any other scope consults
// nothing and keeps account as sent, so an axis-free limit never gains one.
func (s *Service) scopeLimitSubject(
	caller domain.Caller,
	scope domain.LimitScope,
	account domain.AccountID,
	group string,
) (domain.AccountID, error) {
	if scope.HasAccountAxis() {
		return s.scope.Account(caller, account)
	}
	if scope == domain.ScopeAccountGroup {
		if err := s.scope.Group(caller, group); err != nil {
			return "", err
		}
	}
	return account, nil
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
