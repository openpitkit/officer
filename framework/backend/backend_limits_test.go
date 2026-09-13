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
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
)

type limitTestNode struct {
	node.Node

	deleteCalls int
	putCalls    int
}

func (n *limitTestNode) DeleteLimit(
	_ context.Context, _ node.LimitTarget, _ domain.Caller,
) (marketdata.Sink, error) {
	n.deleteCalls++
	return nil, nil
}

func (n *limitTestNode) PutSpotFundsPnlBoundsLimit(
	_ context.Context,
	_ domain.LimitSpotFundsPnlBounds,
	_ domain.MissingAccountPolicy,
	_ domain.Caller,
) (marketdata.Sink, error) {
	n.putCalls++
	return nil, nil
}

func newLimitTestService(t *testing.T, n *limitTestNode) *Service {
	t.Helper()
	return &Service{node: n}
}

func TestServiceDeleteLimitValidatesOnlyTargetAxes(t *testing.T) {
	t.Parallel()

	targets := []struct {
		name   string
		target node.LimitTarget
		valid  bool
	}{
		{
			name: "rate broker",
			target: node.LimitTarget{
				Policy: domain.PolicyRateLimit,
				Scope:  domain.ScopeBroker,
			},
			valid: true,
		},
		{
			name: "rate asset",
			target: node.LimitTarget{
				Policy: domain.PolicyRateLimit,
				Scope:  domain.ScopeAsset,
				Asset:  "AAPL",
			},
			valid: true,
		},
		{
			name: "rate account",
			target: node.LimitTarget{
				Policy:  domain.PolicyRateLimit,
				Scope:   domain.ScopeAccount,
				Account: "acc-1",
			},
			valid: true,
		},
		{
			name: "rate account asset",
			target: node.LimitTarget{
				Policy:  domain.PolicyRateLimit,
				Scope:   domain.ScopeAccountAsset,
				Account: "acc-1",
				Asset:   "AAPL",
			},
			valid: true,
		},
		{
			name: "order size broker",
			target: node.LimitTarget{
				Policy: domain.PolicyOrderSizeLimit,
				Scope:  domain.ScopeBroker,
			},
			valid: true,
		},
		{
			name: "order size underlying asset",
			target: node.LimitTarget{
				Policy: domain.PolicyOrderSizeLimit,
				Scope:  domain.ScopeUnderlyingAsset,
				Asset:  "AAPL",
			},
			valid: true,
		},
		{
			name: "order size settlement asset",
			target: node.LimitTarget{
				Policy: domain.PolicyOrderSizeLimit,
				Scope:  domain.ScopeSettlementAsset,
				Asset:  "USD",
			},
			valid: true,
		},
		{
			name: "order size account underlying asset",
			target: node.LimitTarget{
				Policy:  domain.PolicyOrderSizeLimit,
				Scope:   domain.ScopeAccountUnderlyingAsset,
				Account: "acc-1",
				Asset:   "AAPL",
			},
			valid: true,
		},
		{
			name: "order size account settlement asset",
			target: node.LimitTarget{
				Policy:  domain.PolicyOrderSizeLimit,
				Scope:   domain.ScopeAccountSettlementAsset,
				Account: "acc-1",
				Asset:   "USD",
			},
			valid: true,
		},
		{
			name: "spot funds pnl global",
			target: node.LimitTarget{
				Policy: domain.PolicySpotFundsPnlBoundsKillSwitch,
				Scope:  domain.ScopeGlobal,
			},
			valid: true,
		},
		{
			name: "spot funds pnl account group",
			target: node.LimitTarget{
				Policy:       domain.PolicySpotFundsPnlBoundsKillSwitch,
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "desk-a",
			},
			valid: true,
		},
		{
			name: "spot funds pnl account",
			target: node.LimitTarget{
				Policy:  domain.PolicySpotFundsPnlBoundsKillSwitch,
				Scope:   domain.ScopeAccount,
				Account: "acc-1",
			},
			valid: true,
		},
		{
			name: "global with account",
			target: node.LimitTarget{
				Policy:  domain.PolicySpotFundsPnlBoundsKillSwitch,
				Scope:   domain.ScopeGlobal,
				Account: "acc-1",
			},
		},
		{
			name: "account without account",
			target: node.LimitTarget{
				Policy: domain.PolicySpotFundsPnlBoundsKillSwitch,
				Scope:  domain.ScopeAccount,
			},
		},
		{
			name: "account group on rate policy",
			target: node.LimitTarget{
				Policy:       domain.PolicyRateLimit,
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "desk-a",
			},
		},
		{
			name: "asset on broker scope",
			target: node.LimitTarget{
				Policy: domain.PolicyRateLimit,
				Scope:  domain.ScopeBroker,
				Asset:  "AAPL",
			},
		},
		{
			name: "unknown policy",
			target: node.LimitTarget{
				Policy: "not-a-policy",
				Scope:  domain.ScopeBroker,
			},
		},
	}

	n := &limitTestNode{}
	svc := newLimitTestService(t, n)
	ctx := context.Background()
	for _, tc := range targets {
		t.Run(tc.name, func(t *testing.T) {
			callsBefore := n.deleteCalls
			err := svc.DeleteLimit(ctx, tc.target)
			if tc.valid {
				if err != nil {
					t.Fatalf("DeleteLimit(%+v): %v", tc.target, err)
				}
				if n.deleteCalls != callsBefore+1 {
					t.Fatalf("delete calls = %d, want %d", n.deleteCalls, callsBefore+1)
				}
				return
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("DeleteLimit(%+v) = %v, want ErrInvalid", tc.target, err)
			}
			if n.deleteCalls != callsBefore {
				t.Fatalf("invalid target reached node: calls=%d, want %d", n.deleteCalls, callsBefore)
			}
		})
	}
}

func TestServicePutSpotFundsPnlBoundsLimitValidatesCurrencyBeforeNode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		currency   string
		wantErr    bool
		wantDetail string
	}{
		{name: "whitespace only", currency: " ", wantErr: true},
		{name: "too long", currency: strings.Repeat("X", 33), wantErr: true},
		{name: "contains space", currency: "US D", wantErr: true},
		{
			name:       "empty",
			currency:   "",
			wantErr:    true,
			wantDetail: "currency is required",
		},
		{name: "well formed", currency: "USD"},
	}

	n := &limitTestNode{}
	svc := newLimitTestService(t, n)
	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			callsBefore := n.putCalls
			err := svc.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
				Scope:      domain.ScopeGlobal,
				Currency:   tc.currency,
				LowerBound: "-1",
			}, domain.MissingAccountCreate)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("PutSpotFundsPnlBoundsLimit = %v, want ErrInvalid", err)
				}
				if tc.wantDetail != "" && !strings.Contains(err.Error(), tc.wantDetail) {
					t.Fatalf("error = %q, want %q", err, tc.wantDetail)
				}
				if n.putCalls != callsBefore {
					t.Fatalf("invalid currency reached node: calls=%d, want %d", n.putCalls, callsBefore)
				}
				return
			}
			if err != nil {
				t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
			}
			if n.putCalls != callsBefore+1 {
				t.Fatalf("put calls = %d, want %d", n.putCalls, callsBefore+1)
			}
		})
	}
}
