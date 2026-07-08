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
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

// ListLimits returns the typed barriers that reference account, aggregated
// across all nodes. An empty account returns all barriers.
func (s *Service) ListLimits(
	ctx context.Context, account domain.AccountID,
) (node.AccountLimits, error) {
	var out node.AccountLimits
	for i, n := range s.router.All() {
		part, err := n.ListLimits(ctx, account)
		if err != nil {
			return node.AccountLimits{}, fmt.Errorf("backend: node %d list limits: %w", i, err)
		}
		out.RateLimits = append(out.RateLimits, part.RateLimits...)
		out.OrderSizeLimits = append(out.OrderSizeLimits, part.OrderSizeLimits...)
		out.PnlBoundsLimits = append(out.PnlBoundsLimits, part.PnlBoundsLimits...)
	}
	return out, nil
}

// ListPolicyRows returns the typed barriers across all nodes flattened into one
// sorted, paged policy list. Limits are sharded by account, so each node is
// queried with the filter and an offset-inclusive page bound, the per-node
// ordered slices are merge-sorted on the same SortSpec the connector applied,
// the per-node totals are summed, and the global page window is taken last.
func (s *Service) ListPolicyRows(
	ctx context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	nodes := s.router.All()
	if len(nodes) == 1 {
		return nodes[0].ListPolicyRows(ctx, filter)
	}
	nodeFilter := filter
	nodeFilter.Page = nodePageForMerge(filter.Page)
	rows := make([]store.PolicyListRow, 0)
	total := 0
	for i, n := range nodes {
		part, err := n.ListPolicyRows(ctx, nodeFilter)
		if err != nil {
			return store.PolicyListPage{},
				fmt.Errorf("backend: node %d list policy rows: %w", i, err)
		}
		total += part.Total
		rows = append(rows, part.Rows...)
	}
	sortPolicyRows(rows, filter.Sort)
	rows = pagePolicyRows(rows, filter.Page)
	return store.PolicyListPage{Rows: rows, Total: total}, nil
}

// PutRateLimit validates the rate-limit barrier, routes to the owning node, and
// upserts it. The barrier may name an account that does not exist yet: a policy
// rule can be created before the account is registered.
func (s *Service) PutRateLimit(ctx context.Context, limit domain.LimitRate) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	n, err := s.router.Route(keyFor(limit.Account))
	if err != nil {
		return fmt.Errorf("backend: route limit: %w", err)
	}
	sink, err := n.PutRateLimit(ctx, limit, auth.CallerFromContext(ctx))
	return s.finishLimitChange(sink, err)
}

// PutOrderSizeLimit validates the order-size barrier, routes to the owning node,
// and upserts it.
func (s *Service) PutOrderSizeLimit(ctx context.Context, limit domain.LimitOrderSize) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	n, err := s.router.Route(keyFor(limit.Account))
	if err != nil {
		return fmt.Errorf("backend: route limit: %w", err)
	}
	sink, err := n.PutOrderSizeLimit(ctx, limit, auth.CallerFromContext(ctx))
	return s.finishLimitChange(sink, err)
}

// PutPnlBoundsLimit validates the P&L-bounds barrier, routes to the owning node,
// and upserts it.
func (s *Service) PutPnlBoundsLimit(ctx context.Context, limit domain.LimitPnlBounds) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	n, err := s.router.Route(keyFor(limit.Account))
	if err != nil {
		return fmt.Errorf("backend: route limit: %w", err)
	}
	sink, err := n.PutPnlBoundsLimit(ctx, limit, auth.CallerFromContext(ctx))
	return s.finishLimitChange(sink, err)
}

// DeleteLimit validates the target's scope/axes for its policy, routes to the
// owning node, and removes the addressed barrier.
func (s *Service) DeleteLimit(ctx context.Context, target node.LimitTarget) error {
	if err := validateLimitTarget(target); err != nil {
		return err
	}
	n, err := s.router.Route(keyFor(target.Account))
	if err != nil {
		return fmt.Errorf("backend: route limit: %w", err)
	}
	sink, err := n.DeleteLimit(ctx, target, auth.CallerFromContext(ctx))
	return s.finishLimitChange(sink, err)
}

func (s *Service) finishLimitChange(sink marketdata.Sink, err error) error {
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

// validateLimitTarget checks a delete target's policy, scope, and axes without a
// value payload: it builds a minimal valid typed barrier for the target's policy
// and validates only its scope/axes, so a delete addresses a barrier by its
// (policy, scope, account, asset) composite alone. An unknown policy is invalid.
func validateLimitTarget(target node.LimitTarget) error {
	switch target.Policy {
	case domain.PolicyRateLimit:
		return domain.LimitRate{
			Scope: target.Scope, Account: target.Account, Asset: target.Asset,
			MaxOrders: 1, Window: time.Second,
		}.Validate()
	case domain.PolicyOrderSizeLimit:
		return domain.LimitOrderSize{
			Scope: target.Scope, Account: target.Account, Asset: target.Asset,
			MaxQuantity: "1",
		}.Validate()
	case domain.PolicyPnlBoundsKillSwitch:
		return domain.LimitPnlBounds{
			Scope: target.Scope, Account: target.Account, Asset: target.Asset,
			LowerBound: "0",
		}.Validate()
	default:
		return fmt.Errorf("unknown policy %q: %w", target.Policy, domain.ErrInvalid)
	}
}
