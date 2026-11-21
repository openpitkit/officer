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

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

type persistedLimitReader struct {
	features backend.AccountFeatures
	limits   node.AccountLimits
	err      error
	listed   []domain.AccountID
}

func (r *persistedLimitReader) AccountFeatures() backend.AccountFeatures {
	return r.features
}

func (r *persistedLimitReader) ListLimits(_ context.Context, account domain.AccountID) (node.AccountLimits, error) {
	r.listed = append(r.listed, account)
	return r.limits, r.err
}

func checkPersistedPair[Limit interface {
	domain.KeyedLimit
	comparable
}](t *testing.T, rows []Limit, limits node.AccountLimits,
	read func(context.Context, LimitReader, Limit) (Limit, error),
) {
	t.Helper()
	reader := &persistedLimitReader{limits: limits}
	for i, target := range rows {
		got, err := read(context.Background(), reader, target)
		if err != nil || got != target {
			t.Fatalf("barrier %d: got %+v, error %v; want %+v", i, got, err, target)
		}
	}
}

func TestPersistedLimitIdentity(t *testing.T) {
	t.Parallel()
	t.Run("policy", func(t *testing.T) {
		rows := []domain.LimitSpotFundsPnlBounds{
			{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-10"},
			{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisUnrealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-10"},
		}
		checkPersistedPair(t, rows, node.AccountLimits{SpotFundsPnlBoundsLimits: rows}, PersistedSpotFundsPnlBoundsLimit)
	})
	t.Run("mode", func(t *testing.T) {
		rows := []domain.LimitOrderSize{
			{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "10"},
			{Mode: domain.RuleModeReviewWithoutHold, Scope: domain.ScopeBroker, MaxQuantity: "10"},
		}
		checkPersistedPair(t, rows, node.AccountLimits{OrderSizeLimits: rows}, PersistedOrderSizeLimit)
	})
	t.Run("scope", func(t *testing.T) {
		rows := []domain.LimitOrderSize{
			{Mode: domain.RuleModeReject, Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "10"},
			{Mode: domain.RuleModeReject, Scope: domain.ScopeSettlementAsset, Asset: "AAPL", MaxQuantity: "10"},
		}
		checkPersistedPair(t, rows, node.AccountLimits{OrderSizeLimits: rows}, PersistedOrderSizeLimit)
	})
	t.Run("account", func(t *testing.T) {
		rows := []domain.LimitRate{
			{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, Account: "desk-alpha", MaxOrders: 10},
			{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, Account: "desk-beta", MaxOrders: 10},
		}
		checkPersistedPair(t, rows, node.AccountLimits{RateLimits: rows}, PersistedRateLimit)
	})
	t.Run("account_group", func(t *testing.T) {
		rows := []domain.LimitSpotFundsPnlBounds{
			{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccountGroup, AccountGroup: "group-alpha", Currency: "USD", LowerBound: "-10"},
			{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccountGroup, AccountGroup: "group-beta", Currency: "USD", LowerBound: "-10"},
		}
		checkPersistedPair(t, rows, node.AccountLimits{SpotFundsPnlBoundsLimits: rows}, PersistedSpotFundsPnlBoundsLimit)
	})
	t.Run("asset", func(t *testing.T) {
		rows := []domain.LimitRate{
			{Mode: domain.RuleModeReject, Scope: domain.ScopeAsset, Asset: "AAPL", MaxOrders: 10},
			{Mode: domain.RuleModeReject, Scope: domain.ScopeAsset, Asset: "MSFT", MaxOrders: 10},
		}
		checkPersistedPair(t, rows, node.AccountLimits{RateLimits: rows}, PersistedRateLimit)
	})
	t.Run("position_asset", func(t *testing.T) {
		rows := []domain.LimitPosition{
			{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "desk-alpha", Asset: "AAPL", MaxPosition: "10"},
			{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "desk-alpha", Asset: "MSFT", MaxPosition: "10"},
		}
		checkPersistedPair(t, rows, node.AccountLimits{PositionLimits: rows}, PersistedPositionLimit)
	})
}

func checkPersistedErrors[Limit domain.KeyedLimit](t *testing.T,
	target, corrupt Limit, limits node.AccountLimits,
	read func(context.Context, LimitReader, Limit) (Limit, error),
) {
	t.Helper()
	ctx := context.Background()
	reader := &persistedLimitReader{}
	if _, err := read(ctx, reader, target); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing barrier: got %v, want ErrNotFound", err)
	}
	reader.limits = limits
	_, err := read(ctx, reader, target)
	if err == nil || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("stored key error must be corruption, not ErrInvalid: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%+v", corrupt)) {
		t.Fatalf("stored key error does not name barrier %+v: %v", corrupt, err)
	}
	reader.listed = nil
	if _, err := read(ctx, reader, corrupt); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("target key error = %v, want ErrInvalid", err)
	}
	if len(reader.listed) != 0 {
		t.Fatalf("invalid target called ListLimits: %v", reader.listed)
	}
	reader.err = errors.New("list failed")
	if _, err := read(ctx, reader, target); err != reader.err {
		t.Fatalf("list error = %v, want %v", err, reader.err)
	}
}

func TestPersistedLimitErrors(t *testing.T) {
	t.Parallel()
	t.Run("rate", func(t *testing.T) {
		target := domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker}
		corrupt := domain.LimitRate{Mode: domain.RuleModeReject, Scope: "corrupt-rate"}
		checkPersistedErrors(t, target, corrupt, node.AccountLimits{RateLimits: []domain.LimitRate{corrupt}}, PersistedRateLimit)
	})
	t.Run("order_size", func(t *testing.T) {
		target := domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker}
		corrupt := domain.LimitOrderSize{Mode: "corrupt-mode", Scope: domain.ScopeBroker}
		checkPersistedErrors(t, target, corrupt, node.AccountLimits{OrderSizeLimits: []domain.LimitOrderSize{corrupt}}, PersistedOrderSizeLimit)
	})
	t.Run("position", func(t *testing.T) {
		target := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "desk-alpha", Asset: "AAPL"}
		corrupt := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: "corrupt-position"}
		checkPersistedErrors(t, target, corrupt, node.AccountLimits{PositionLimits: []domain.LimitPosition{corrupt}}, PersistedPositionLimit)
	})
	t.Run("pnl", func(t *testing.T) {
		target := domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeGlobal}
		corrupt := domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: "corrupt-axis", Scope: domain.ScopeGlobal}
		checkPersistedErrors(t, target, corrupt, node.AccountLimits{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{corrupt}}, PersistedSpotFundsPnlBoundsLimit)
	})
}

func TestPersistedLimitNormalizesAccountBeforeKey(t *testing.T) {
	t.Parallel()
	stored := domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, Account: "scoped-account", MaxOrders: 10}
	reader := &persistedLimitReader{
		features: backend.AccountFeatures{Account: stored.Account},
		limits:   node.AccountLimits{RateLimits: []domain.LimitRate{stored}},
	}
	target := stored
	target.Account = ""
	target.MaxOrders = 20
	got, err := PersistedRateLimit(context.Background(), reader, target)
	if err != nil || got != stored {
		t.Fatalf("normalized echo = %+v, error %v; want %+v", got, err, stored)
	}
	if len(reader.listed) != 1 || reader.listed[0] != stored.Account {
		t.Fatalf("ListLimits accounts = %v, want [%s]", reader.listed, stored.Account)
	}
}
