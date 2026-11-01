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
	"fmt"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
)

type limitTestNode struct {
	node.Node

	deleteCalls    int
	putCalls       int
	modeState      domain.SpotFundsLimitModeState
	modeErr        error
	modeReadCalls  int
	modeWriteCalls int
	modeWrite      domain.SpotFundsLimitMode
	modeCaller     domain.Caller
	modeWriteErr   error
}

func (n *limitTestNode) SpotFundsLimitMode(context.Context) (domain.SpotFundsLimitModeState, error) {
	n.modeReadCalls++
	return n.modeState, n.modeErr
}

func (n *limitTestNode) SetSpotFundsLimitMode(_ context.Context, mode domain.SpotFundsLimitMode, caller domain.Caller) error {
	n.modeWriteCalls++
	n.modeWrite, n.modeCaller = mode, caller
	return n.modeWriteErr
}

type limitModeTestScope struct{ EveryAccount }

func (limitModeTestScope) Visible(_ domain.Caller, code domain.AccountID) error {
	if code == "acc-2" {
		return fmt.Errorf("hidden account: %w", domain.ErrNotFound)
	}
	return nil
}

func TestServiceSpotFundsLimitModeVisibility(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		scope AccountScope
		want  []domain.AccountID
	}{
		{"filtered", limitModeTestScope{}, []domain.AccountID{"acc-1", "acc-3"}},
		{"every account", EveryAccount{}, []domain.AccountID{"acc-1", "acc-2", "acc-3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []domain.AccountID{"acc-1", "acc-2", "acc-3"}
			n := &limitTestNode{modeState: domain.SpotFundsLimitModeState{
				Mode:                    domain.SpotFundsLimitModeTrackOnly,
				AccountsWithoutCurrency: slices.Clone(original),
			}}
			svc := newLimitTestService(t, n)
			svc.scope = tc.scope
			state, err := svc.SpotFundsLimitMode(systemCtx())
			if err != nil {
				t.Fatal(err)
			}
			if state.Mode != domain.SpotFundsLimitModeTrackOnly {
				t.Fatalf("mode = %q, want track_only", state.Mode)
			}
			if !slices.Equal(state.AccountsWithoutCurrency, tc.want) {
				t.Fatalf("accounts = %v, want %v", state.AccountsWithoutCurrency, tc.want)
			}
			if !slices.Equal(n.modeState.AccountsWithoutCurrency, original) {
				t.Fatalf("node accounts mutated = %v, want %v", n.modeState.AccountsWithoutCurrency, original)
			}
			if n.modeReadCalls != 1 {
				t.Fatalf("node read calls = %d, want 1", n.modeReadCalls)
			}
		})
	}
}

func TestServiceSpotFundsLimitModeReadError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("mode read failed")
	n := &limitTestNode{modeErr: wantErr}
	_, err := newLimitTestService(t, n).SpotFundsLimitMode(systemCtx())
	if err != wantErr {
		t.Fatalf("error = %v, want unchanged node error %v", err, wantErr)
	}
	if n.modeReadCalls != 1 {
		t.Fatalf("node read calls = %d, want 1", n.modeReadCalls)
	}
}

func TestServiceSpotFundsLimitModeRequiresCaller(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, wantErr := auth.CallerFromContext(ctx)
	n := &limitTestNode{}
	svc := newLimitTestService(t, n)
	_, readErr := svc.SpotFundsLimitMode(ctx)
	writeErr := svc.PutSpotFundsLimitMode(ctx, domain.LimitSpotFundsLimitMode{
		Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeTrackOnly,
	})
	for _, err := range []error{readErr, writeErr} {
		if err == nil || err.Error() != wantErr.Error() || !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("error = %v, want auth error %v", err, wantErr)
		}
	}
	if n.modeReadCalls != 0 || n.modeWriteCalls != 0 {
		t.Fatalf("missing caller reached node: reads=%d writes=%d", n.modeReadCalls, n.modeWriteCalls)
	}
}

func TestServicePutSpotFundsLimitModeForwardsCaller(t *testing.T) {
	t.Parallel()
	caller := domain.Caller{Source: domain.SourceMCP, Principal: "mode-test"}
	ctx := auth.ContextWithCaller(context.Background(), caller)
	for _, wantErr := range []error{nil, domain.ErrLoosening} {
		n := &limitTestNode{modeWriteErr: wantErr}
		svc := newLimitTestService(t, n)
		svc.scope = limitModeTestScope{}
		err := svc.PutSpotFundsLimitMode(ctx, domain.LimitSpotFundsLimitMode{
			Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeTrackOnly,
		})
		if err != wantErr {
			t.Fatalf("error = %v, want unchanged node error %v", err, wantErr)
		}
		if n.modeWriteCalls != 1 || n.modeWrite != domain.SpotFundsLimitModeTrackOnly || n.modeCaller != caller {
			t.Fatalf("node write = (%d, %q, %+v), want (1, track_only, %+v)", n.modeWriteCalls, n.modeWrite, n.modeCaller, caller)
		}
	}
}

// TestServicePutSpotFundsLimitModeValidatesBeforeNode proves the meta-policy
// is global only and needs a recognised mode, and that a refused value never
// reaches the node.
func TestServicePutSpotFundsLimitModeValidatesBeforeNode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		limit   domain.LimitSpotFundsLimitMode
		pointer string
	}{
		{"no scope", domain.LimitSpotFundsLimitMode{Mode: domain.SpotFundsLimitModeEnforce}, "/scope"},
		{"account scope", domain.LimitSpotFundsLimitMode{
			Scope: domain.ScopeAccount, Mode: domain.SpotFundsLimitModeEnforce,
		}, "/scope"},
		{"broker scope", domain.LimitSpotFundsLimitMode{
			Scope: domain.ScopeBroker, Mode: domain.SpotFundsLimitModeEnforce,
		}, "/scope"},
		{"no mode", domain.LimitSpotFundsLimitMode{Scope: domain.ScopeGlobal}, "/mode"},
		{"unknown mode", domain.LimitSpotFundsLimitMode{Scope: domain.ScopeGlobal, Mode: "relaxed"}, "/mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := &limitTestNode{}
			err := newLimitTestService(t, n).PutSpotFundsLimitMode(systemCtx(), tc.limit)
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != tc.pointer {
				t.Fatalf("error = %v, pointer = %q; want ErrInvalid at %s", err, domain.ValidationPointer(err), tc.pointer)
			}
			if n.modeWriteCalls != 0 {
				t.Fatalf("invalid meta-policy reached node: writes=%d", n.modeWriteCalls)
			}
		})
	}
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
	return &Service{node: n, scope: EveryAccount{}}
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
			name: "spot funds limit mode global",
			target: node.LimitTarget{
				Policy: domain.PolicySpotFundsLimitMode,
				Scope:  domain.ScopeGlobal,
			},
			valid: true,
		},
		{
			name: "spot funds limit mode account",
			target: node.LimitTarget{
				Policy:  domain.PolicySpotFundsLimitMode,
				Scope:   domain.ScopeAccount,
				Account: "acc-1",
			},
		},
		{
			name: "spot funds limit mode global with asset",
			target: node.LimitTarget{
				Policy: domain.PolicySpotFundsLimitMode,
				Scope:  domain.ScopeGlobal,
				Asset:  "USD",
			},
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
	ctx := systemCtx()
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
	ctx := systemCtx()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			callsBefore := n.putCalls
			err := svc.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
				Axis:       domain.SpotFundsPnlAxisRealized,
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
