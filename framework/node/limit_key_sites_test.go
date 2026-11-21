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

package node

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

func TestLimitKeySites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		axis string
		pair [2]domain.KeyedLimit
	}{
		{
			axis: "position_account",
			pair: [2]domain.KeyedLimit{
				domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "first", Asset: "AAPL", MaxPosition: "10"},
				domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "second", Asset: "AAPL", MaxPosition: "20"},
			},
		},
		{
			axis: "position_asset",
			pair: [2]domain.KeyedLimit{
				domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "first", Asset: "AAPL", MaxPosition: "10"},
				domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "first", Asset: "EUR", MaxPosition: "20"},
			},
		},
		{
			axis: "policy",
			pair: [2]domain.KeyedLimit{
				domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-10"},
				domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisUnrealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-10"},
			},
		},
		{
			axis: "mode",
			pair: [2]domain.KeyedLimit{
				domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "10"},
				domain.LimitOrderSize{Mode: domain.RuleModeReviewWithoutHold, Scope: domain.ScopeBroker, MaxQuantity: "10"},
			},
		},
		{
			axis: "scope",
			pair: [2]domain.KeyedLimit{
				domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "10"},
				domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeSettlementAsset, Asset: "AAPL", MaxNotional: "10"},
			},
		},
		{
			axis: "account",
			pair: [2]domain.KeyedLimit{
				domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, Account: "first", Window: time.Second, MaxOrders: 10},
				domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, Account: "second", Window: time.Second, MaxOrders: 10},
			},
		},
		{
			axis: "account_group",
			pair: [2]domain.KeyedLimit{
				domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccountGroup, AccountGroup: "first", Currency: "USD", LowerBound: "-10"},
				domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccountGroup, AccountGroup: "second", Currency: "USD", LowerBound: "-10"},
			},
		},
		{
			axis: "asset",
			pair: [2]domain.KeyedLimit{
				domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAsset, Asset: "AAPL", Window: time.Second, MaxOrders: 10},
				domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAsset, Asset: "EUR", Window: time.Second, MaxOrders: 10},
			},
		},
	} {
		t.Run(tc.axis, func(t *testing.T) {
			t.Parallel()
			keys := [2]domain.LimitKey{}
			for i, limit := range tc.pair {
				key, err := limit.Key()
				if err != nil {
					t.Fatal(err)
				}
				keys[i] = key
			}
			t.Run("audit", func(t *testing.T) {
				setDetails := [2]domain.AuditDetail{}
				deleteDetails := [2]domain.AuditDetail{}
				for i, key := range keys {
					setDetails[i] = keySiteSetDetail(t, key, tc.pair[i])
					deleteDetails[i] = deleteLimitDetail(key)
					requireKeySiteDetailAxes(t, key, setDetails[i])
					requireKeySiteDetailAxes(t, key, deleteDetails[i])
				}
				if reflect.DeepEqual(setDetails[0], setDetails[1]) {
					t.Fatalf("set audit merged %s axis: %+v", tc.axis, setDetails[0])
				}
				if reflect.DeepEqual(deleteDetails[0], deleteDetails[1]) {
					t.Fatalf("delete audit merged %s axis: %+v", tc.axis, deleteDetails[0])
				}
			})
			t.Run("write_delete", func(t *testing.T) {
				ctx := context.Background()
				n, _ := newLimitWriteNode(t)
				var built engine.Snapshot
				n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
					return fakeBuild(newFakeEngine(), &built)(snapshot)
				}
				if tc.axis == "account_group" {
					for _, code := range []string{"first", "second"} {
						if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: code}, testCaller); err != nil {
							t.Fatal(err)
						}
					}
				}
				for i, limit := range tc.pair {
					before := keySiteAuditCount(t, n)
					var err error
					switch limit := limit.(type) {
					case domain.LimitRate:
						_, err = n.PutRateLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
					case domain.LimitOrderSize:
						_, err = n.PutOrderSizeLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
					case domain.LimitPosition:
						_, err = n.PutPositionLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
					case domain.LimitSpotFundsPnlBounds:
						_, err = n.PutSpotFundsPnlBoundsLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
					default:
						t.Fatalf("unsupported test limit %T", limit)
					}
					if err != nil {
						t.Fatalf("Add barrier %d: %v", i, err)
					}
					requireKeySiteAudit(t, n, before, domain.AuditActionSetLimit, keys[i], keySiteSetDetail(t, keys[i], limit))
				}
				requireKeySiteStored(t, n, tc.pair[:]...)
				key := keys[0]
				before := keySiteAuditCount(t, n)
				if _, err := n.DeleteLimit(ctx, LimitTarget{
					Policy: key.Policy(), Mode: key.Mode(), Scope: key.Scope(),
					Account: key.Account(), AccountGroup: key.AccountGroup(), Asset: key.Asset(),
				}, testCaller); err != nil {
					t.Fatal(err)
				}
				requireKeySiteAudit(t, n, before, domain.AuditActionDeleteLimit, key, deleteLimitDetail(key))
				requireKeySiteStored(t, n, tc.pair[1])
				if _, found := readKeySiteLimit(t, n, keys[0]); found {
					t.Fatal("deleted key still resolves to a barrier")
				}
			})
			t.Run("restore", func(t *testing.T) {
				_, count := keySiteSnapshot(t, tc.pair[:]...)
				if count != 2 {
					t.Fatalf("restore map merged %s axis: got %d entries, want 2", tc.axis, count)
				}
			})
			t.Run("revert", func(t *testing.T) {
				ctx := context.Background()
				n, _ := newLimitWriteNode(t)
				var built engine.Snapshot
				n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
					return fakeBuild(newFakeEngine(), &built)(snapshot)
				}
				if tc.axis == "account_group" {
					for _, code := range []string{"first", "second"} {
						if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: code}, testCaller); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Resolve both accounts before injecting engine failure.
				for _, key := range keys {
					if key.Account() != "" {
						if _, found, err := n.realm.GetAccount(ctx, key.Account()); err == nil && !found {
							if _, err := n.CreateAccount(ctx, testAccount(key.Account()), testCaller); err != nil {
								t.Fatal(err)
							}
						} else if err != nil {
							t.Fatal(err)
						}
					}
				}
				var neighbours []domain.KeyedLimit
				if tc.axis == "mode" {
					// Both modes are installed at another scope, so the pair's
					// second Add stores then configures instead of rebuilding.
					for _, mode := range []domain.RuleMode{keys[0].Mode(), keys[1].Mode()} {
						limit := domain.LimitOrderSize{Mode: mode, Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "100"}
						if err := putKeySiteLimit(n, limit); err != nil {
							t.Fatal(err)
						}
						neighbours = append(neighbours, limit)
					}
				}
				if err := putKeySiteLimit(n, tc.pair[0]); err != nil {
					t.Fatal(err)
				}
				n.currentEngine().(*fakeEngine).failConfigure = true
				if err := putKeySiteLimit(n, tc.pair[1]); err == nil {
					t.Fatal("Add succeeded despite engine failure")
				}
				want := append(neighbours, tc.pair[0])
				requireKeySiteStored(t, n, want...)
			})
		})
	}
}

func requireKeySiteStored(t *testing.T, n *localNode, want ...domain.KeyedLimit) {
	t.Helper()
	listed, err := n.ListLimits(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	count := len(listed.RateLimits) + len(listed.OrderSizeLimits) + len(listed.PositionLimits) + len(listed.SpotFundsPnlBoundsLimits)
	if count != len(want) {
		t.Fatalf("stored barrier count = %d, want %d: %+v", count, len(want), listed)
	}
	for _, limit := range want {
		key, err := limit.Key()
		if err != nil {
			t.Fatal(err)
		}
		got, found := readKeySiteLimit(t, n, key)
		if !found || !reflect.DeepEqual(got, limit) {
			t.Fatalf("read barrier = %+v, found %t; want %+v", got, found, limit)
		}
	}
}

func readKeySiteLimit(t *testing.T, n *localNode, key domain.LimitKey) (domain.KeyedLimit, bool) {
	t.Helper()
	var limit domain.KeyedLimit
	var found bool
	var err error
	switch key.Policy() {
	case domain.PolicyRateLimit:
		limit, found, err = n.readRateBarrier(context.Background(), key)
	case domain.PolicyOrderSizeLimit:
		limit, found, err = n.readOrderSizeBarrier(context.Background(), key)
	case domain.PolicyPositionLimit:
		limit, found, err = n.readPositionBarrier(context.Background(), key)
	case domain.PolicySpotFundsPnlBoundsKillSwitch, domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch:
		limit, found, err = n.readSpotFundsPnlBoundsBarrier(context.Background(), key)
	default:
		t.Fatalf("unsupported test policy %q", key.Policy())
	}
	if err != nil {
		t.Fatal(err)
	}
	return limit, found
}

func keySiteSnapshot(t *testing.T, limits ...domain.KeyedLimit) (restoreRuntimeSnapshot, int) {
	t.Helper()
	data := backup.Data{}
	var pnl []domain.LimitSpotFundsPnlBounds
	for _, limit := range limits {
		switch limit := limit.(type) {
		case domain.LimitRate:
			data.RateLimits = append(data.RateLimits, limit)
		case domain.LimitOrderSize:
			data.OrderSizeLimits = append(data.OrderSizeLimits, limit)
		case domain.LimitPosition:
			data.PositionLimits = append(data.PositionLimits, limit)
		case domain.LimitSpotFundsPnlBounds:
			pnl = append(pnl, limit)
		default:
			t.Fatalf("unsupported test limit %T", limit)
		}
	}
	if err := data.SetSpotFundsPnlBounds(pnl); err != nil {
		t.Fatal(err)
	}
	rate, err := limitMap(data.RateLimits)
	if err != nil {
		t.Fatal(err)
	}
	orderSize, err := limitMap(data.OrderSizeLimits)
	if err != nil {
		t.Fatal(err)
	}
	spotFunds, err := limitMap(data.SpotFundsPnlBounds())
	if err != nil {
		t.Fatal(err)
	}
	position, err := limitMap(data.PositionLimits)
	if err != nil {
		t.Fatal(err)
	}
	return restoreRuntimeSnapshot{data: data}, len(rate) + len(orderSize) + len(position) + len(spotFunds)
}

func putKeySiteLimit(n *localNode, limit domain.KeyedLimit) error {
	ctx := context.Background()
	var err error
	switch limit := limit.(type) {
	case domain.LimitRate:
		_, err = n.PutRateLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
	case domain.LimitOrderSize:
		_, err = n.PutOrderSizeLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
	case domain.LimitPosition:
		_, err = n.PutPositionLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
	case domain.LimitSpotFundsPnlBounds:
		_, err = n.PutSpotFundsPnlBoundsLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
	default:
		return errors.New("unsupported test barrier")
	}
	return err
}

func keySiteSetDetail(t *testing.T, key domain.LimitKey, limit domain.KeyedLimit) domain.AuditDetail {
	t.Helper()
	switch limit := limit.(type) {
	case domain.LimitRate:
		return setRateLimitDetail(key, limit)
	case domain.LimitOrderSize:
		return setOrderSizeLimitDetail(key, limit)
	case domain.LimitPosition:
		return setPositionLimitDetail(key, limit)
	case domain.LimitSpotFundsPnlBounds:
		return setSpotFundsPnlBoundsLimitDetail(key, limit)
	default:
		t.Fatalf("unsupported test barrier %T", limit)
		return domain.AuditDetail{}
	}
}

func requireKeySiteDetailAxes(t *testing.T, key domain.LimitKey, detail domain.AuditDetail) {
	t.Helper()
	for name, value := range map[string]string{"policy": key.Policy(), "scope": string(key.Scope())} {
		if detail.Params[name] != value {
			t.Fatalf("audit parameter %s = %q, want %q", name, detail.Params[name], value)
		}
	}
	for name, value := range map[string]string{
		"mode": string(key.Mode()), "account": key.Account().String(),
		"account_group": key.AccountGroup(), "asset": key.Asset(),
	} {
		if value == "" {
			continue
		}
		found := false
		for _, field := range detail.Fields {
			if field.Name == name && field.Value == value {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("audit detail missing axis %s=%q: %+v", name, value, detail)
		}
	}
}

func keySiteAuditCount(t *testing.T, n *localNode) int {
	t.Helper()
	rows, err := n.realm.ListAudit(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func requireKeySiteAudit(t *testing.T, n *localNode, before int, action domain.AuditAction, key domain.LimitKey, detail domain.AuditDetail) {
	t.Helper()
	rows, err := n.realm.ListAudit(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, row := range rows[:len(rows)-before] {
		if row.Action != action {
			continue
		}
		writes++
		requireKeySiteDetailAxes(t, key, row.Detail)
		if !reflect.DeepEqual(row.Detail, detail) || row.Account != key.Account() || row.Group != key.AccountGroup() {
			t.Fatalf("audit barrier = %+v, want detail %+v account %q group %q", row, detail, key.Account(), key.AccountGroup())
		}
	}
	if writes != 1 {
		t.Fatalf("new %s audit rows = %d, want 1", action, writes)
	}
}

func TestStoredLimitKeyCorruption(t *testing.T) {
	badRate := domain.LimitRate{Mode: domain.RuleModeReject, Scope: "bogus", MaxOrders: 1, Window: time.Second}
	valid := domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxOrders: 2, Window: time.Second}
	key, err := valid.Key()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("lookup", func(t *testing.T) {
		limit, found, err := domain.FindLimit([]domain.LimitRate{badRate, valid}, key)
		if found || limit != (domain.LimitRate{}) {
			t.Fatalf("corrupt row lookup = %+v, %t; want zero, false", limit, found)
		}
		requireStoredKeyCorruption(t, err)
	})
	t.Run("map", func(t *testing.T) {
		rates, err := limitMap([]domain.LimitRate{badRate})
		if rates != nil {
			t.Fatalf("corrupt map = %+v, want nil", rates)
		}
		requireStoredKeyCorruption(t, err)
	})
}

func requireStoredKeyCorruption(t *testing.T, err error) {
	t.Helper()
	if err == nil || errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "stored barrier") || !strings.Contains(err.Error(), "holds an invalid key") {
		t.Fatalf("stored key error = %v, want corruption without ErrInvalid validation chain", err)
	}
}
