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

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

func TestCountingLimitModeStorage(t *testing.T) {
	for _, tc := range []struct {
		policy string
		scope  domain.LimitScope
	}{
		{domain.PolicyRateLimit, domain.ScopeAccountAsset},
		{domain.PolicyPositionLimit, domain.ScopeAccountAsset},
		{domain.PolicySpotFundsPnlBoundsKillSwitch, domain.ScopeGlobal},
		{domain.PolicySpotFundsPnlBoundsKillSwitch, domain.ScopeAccountGroup},
		{domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch, domain.ScopeGlobal},
		{domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch, domain.ScopeAccountGroup},
		{domain.PolicySpotFundsTotalPnlBoundsKillSwitch, domain.ScopeGlobal},
		{domain.PolicySpotFundsTotalPnlBoundsKillSwitch, domain.ScopeAccountGroup},
		{domain.PolicySpotFundsPnlBoundsKillSwitch, domain.ScopeAccount},
		{domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch, domain.ScopeAccount},
		{domain.PolicySpotFundsTotalPnlBoundsKillSwitch, domain.ScopeAccount},
	} {
		policy := tc.policy
		t.Run(policy+"/"+string(tc.scope), func(t *testing.T) {
			ctx, rs := seedLimitFixtures(t)
			if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}); err != nil {
				t.Fatal(err)
			}
			modes := []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithHold, domain.RuleModeReviewWithoutHold}
			want := make([]domain.KeyedLimit, 0, len(modes))
			for _, mode := range modes {
				var limit domain.KeyedLimit = domain.LimitRate{Mode: mode, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", MaxOrders: 10, Window: time.Second}
				if policy == domain.PolicyPositionLimit {
					limit = domain.LimitPosition{Mode: mode, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", MaxPosition: "10"}
				} else if axis, ok := domain.SpotFundsPnlAxisOf(policy); ok {
					bound := domain.LimitSpotFundsPnlBounds{Mode: mode, Axis: axis, Scope: tc.scope, Currency: "USD", LowerBound: "-10"}
					switch tc.scope {
					case domain.ScopeAccountGroup:
						bound.AccountGroup = "desk"
					case domain.ScopeAccount:
						bound.Account = "acc-1"
					}
					limit = bound
				}
				putSQLKeyLimit(t, ctx, rs, limit)
				want = append(want, limit)
			}
			requireSQLKeyLimits(t, ctx, rs, want...)
			switch policy {
			case domain.PolicyRateLimit:
				changed := want[1].(domain.LimitRate)
				changed.MaxOrders = 5
				want[1] = changed
			case domain.PolicyPositionLimit:
				changed := want[1].(domain.LimitPosition)
				changed.MaxPosition = "5"
				want[1] = changed
			default:
				changed := want[1].(domain.LimitSpotFundsPnlBounds)
				changed.LowerBound = "-5"
				want[1] = changed
			}
			putSQLKeyLimit(t, ctx, rs, want[1])
			requireSQLKeyLimits(t, ctx, rs, want...)
			kind := fwstore.PolicyKind(policy)
			for _, mode := range modes {
				page, err := rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{Kind: &kind, Mode: mode})
				if err != nil || page.Total != 1 || len(page.Rows) != 1 {
					t.Fatalf("list %s: %+v, %v", mode, page, err)
				}
				var limit domain.KeyedLimit = page.Rows[0].Rate
				if policy == domain.PolicyPositionLimit {
					limit = page.Rows[0].Position
				} else if _, ok := domain.SpotFundsPnlAxisOf(policy); ok {
					limit = page.Rows[0].SpotFundsPnlBounds
				}
				key, err := limit.Key()
				if err != nil || key.Mode() != mode {
					t.Fatalf("listed key=%v error=%v, want %s", key, err, mode)
				}
			}
			scope := backup.Scope{Sections: []backup.Section{backup.SectionRiskLimits}, Accounts: backup.EntitySelector{All: true}}
			archive, err := rs.ExportBackup(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			key, err := want[1].Key()
			if err != nil {
				t.Fatal(err)
			}
			if err := rs.DeleteLimit(ctx, key); err != nil {
				t.Fatal(err)
			}
			requireSQLKeyLimits(t, ctx, rs, want[0], want[2])
			if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeReplaceAll}); err != nil {
				t.Fatal(err)
			}
			requireSQLKeyLimits(t, ctx, rs, want...)
			switch policy {
			case domain.PolicyRateLimit:
				archive.Data.RateLimits = append(archive.Data.RateLimits[:1:1], archive.Data.RateLimits[2:]...)
			case domain.PolicyPositionLimit:
				archive.Data.PositionLimits = append(archive.Data.PositionLimits[:1:1], archive.Data.PositionLimits[2:]...)
			default:
				bounds := archive.Data.SpotFundsPnlBounds()
				bounds = append(bounds[:1:1], bounds[2:]...)
				if err := archive.Data.SetSpotFundsPnlBounds(bounds); err != nil {
					t.Fatal(err)
				}
			}
			// Stored ordering is reject, review_with_hold, review_without_hold.
			if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeReplaceAll}); err != nil {
				t.Fatal(err)
			}
			requireSQLKeyLimits(t, ctx, rs, want[0], want[2])
		})
	}
}

func TestScanPolicyRowUnknownKindIsInternalError(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)
	db, err := rs.(*realmStore).db()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT 'unknown', 'global', NULL, NULL, NULL, NULL,
		NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	})
	if !rows.Next() {
		t.Fatalf("no row: %v", rows.Err())
	}
	if _, err := scanPolicyRow(rows); err == nil || errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), `unknown policy kind "unknown"`) {
		t.Fatalf("scan error = %v, want internal error naming kind", err)
	}
}

func TestSQLLimitTableRejectsInvalidKeyShape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape limitKeyShape
	}{
		{"zero", limitKeyShapeInvalid},
		{"unknown", limitKeyShape(255)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := limitTable{name: "limit_rate", noun: "rate limit", keyShape: tc.shape}
			t.Run("where", func(t *testing.T) {
				_, err := table.pnlAxisKeyed()
				if err == nil || errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("pnlAxisKeyed error = %v, want internal error", err)
				}
				if !strings.Contains(err.Error(), table.name) {
					t.Fatalf("pnlAxisKeyed error = %v, want table", err)
				}
			})
			t.Run("prune", func(t *testing.T) {
				query, err := table.pruneQuery()
				if err == nil || errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("pruneQuery error = %v, want internal error", err)
				}
				if !strings.Contains(err.Error(), table.name) {
					t.Fatalf("pruneQuery error = %v, want table", err)
				}
				if query != "" {
					t.Fatalf("pruneQuery = %q, want empty query", query)
				}
			})
		})
	}
}

func TestSQLLimitTableRejectsGenericPnlPrune(t *testing.T) {
	table := limitTable{name: "limit_spot_funds_pnl_bound", keyShape: limitKeyShapePnlAxis}
	query, err := table.pruneQuery()
	if err == nil || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("pruneQuery error = %v, want internal error", err)
	}
	if !strings.Contains(err.Error(), table.name) {
		t.Fatalf("pruneQuery error = %v, want table", err)
	}
	if query != "" {
		t.Fatalf("pruneQuery = %q, want empty query", query)
	}
}

func requireSQLKeyPrune(t *testing.T, ctx context.Context, rs fwstore.RealmStore, data backup.Data, want []domain.KeyedLimit) {
	t.Helper()
	db, err := rs.(*realmStore).db()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var table string
	switch want[0].(type) {
	case domain.LimitRate:
		table = "limit_rate"
	case domain.LimitOrderSize:
		table = "limit_order_size"
	case domain.LimitPosition:
		table = "limit_position"
	case domain.LimitSpotFundsPnlBounds:
		table = "limit_spot_funds_pnl_bound"
	default:
		t.Fatalf("unsupported barrier %T", want[0])
	}
	// Insertion order identifies the pair independently of key predicates.
	before := sqlKeyRowIDs(t, ctx, tx, table)
	if len(before) != 2 {
		t.Fatalf("before prune row ids = %v, want pair", before)
	}
	summary := backup.NewSummary()
	rt := restoreTx{tx: tx, mode: backup.RestoreModeReplaceAll, summary: &summary}
	if err := rt.pruneLimits(ctx, backup.EntitySelector{All: true}, accountScope{matchesAll: true}, data); err != nil {
		t.Fatal(err)
	}
	got := sqlKeyRowIDs(t, ctx, tx, table)
	expected := before[:len(want)]
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("pruned %s row ids = %v, want %v before restore inserts", table, got, expected)
	}
}

func sqlKeyRowIDs(t *testing.T, ctx context.Context, q sqlQueryer, table string) []int64 {
	t.Helper()
	rows, err := q.QueryContext(ctx, `SELECT id FROM `+table+` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSQLLimitKeyArchiveValidationOrder(t *testing.T) {
	t.Run("limit_context", func(t *testing.T) {
		ctx, rs := seedLimitFixtures(t)
		scope := backup.Scope{Sections: []backup.Section{backup.SectionRiskLimits}, Accounts: backup.EntitySelector{All: true}}
		archive, err := rs.ExportBackup(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		archive.Data.RateLimits = []domain.LimitRate{{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, MaxOrders: 1, Window: time.Second}}
		var messages []string
		for _, mode := range []backup.RestoreMode{backup.RestoreModeOverwrite, backup.RestoreModeReplaceAll} {
			_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: mode})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("%s invalid archive = %v, want ErrInvalid", mode, err)
			}
			messages = append(messages, err.Error())
		}
		if messages[0] != messages[1] {
			t.Fatalf("archive limit errors differ: overwrite = %q, replace-all = %q", messages[0], messages[1])
		}
		if !strings.HasPrefix(messages[0], `store: restore rate_limit scope "account" account "" asset "":`) {
			t.Fatalf("archive limit error = %q, want restore row context", messages[0])
		}
	})
	t.Run("group_precedes_limit", func(t *testing.T) {
		ctx, rs := seedLimitFixtures(t)
		scope := backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups, backup.SectionRiskLimits}, Accounts: backup.EntitySelector{All: true}}
		archive, err := rs.ExportBackup(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		archive.Data.Groups = []backup.AccountGroup{{Code: "bad", Title: strings.Repeat("x", 257)}}
		archive.Data.RateLimits = []domain.LimitRate{{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, MaxOrders: 1, Window: time.Second}}
		_, err = rs.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeReplaceAll})
		if !errors.Is(err, domain.ErrInvalid) || !strings.HasPrefix(err.Error(), `store: restore group "bad":`) {
			t.Fatalf("archive error = %v, want group error before limit error", err)
		}
	})
}

func TestSQLLimitKeyPruneIgnoresCorruptionOutsideScope(t *testing.T) {
	for _, subject := range []string{"rate_account", "pnl_account", "pnl_group"} {
		t.Run(subject, func(t *testing.T) {
			ctx, rs := seedLimitFixtures(t)
			if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
				t.Fatal(err)
			}
			if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "other"}); err != nil {
				t.Fatal(err)
			}
			scope := backup.Scope{Sections: []backup.Section{backup.SectionRiskLimits}, Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}}}
			archive, err := rs.ExportBackup(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			db, err := rs.(*realmStore).db()
			if err != nil {
				t.Fatal(err)
			}
			table := "limit_spot_funds_pnl_bound"
			switch subject {
			case "rate_account":
				table = "limit_rate"
				_, err = db.ExecContext(ctx, `INSERT INTO limit_rate (scope, account_id, max_orders, window) VALUES ('bogus', (SELECT id FROM account WHERE code = 'acc-2'), 1, '1s')`)
			case "pnl_account":
				_, err = db.ExecContext(ctx, `INSERT INTO limit_spot_funds_pnl_bound (axis, scope, account_id, currency_asset_id, lower_bound) VALUES ('realized', 'bogus', (SELECT id FROM account WHERE code = 'acc-2'), (SELECT id FROM asset WHERE code = 'USD'), '-10')`)
			case "pnl_group":
				_, err = db.ExecContext(ctx, `INSERT INTO limit_spot_funds_pnl_bound (axis, scope, account_group_id, currency_asset_id, lower_bound) VALUES ('realized', 'bogus', (SELECT id FROM account_group WHERE code = 'other'), (SELECT id FROM asset WHERE code = 'USD'), '-10')`)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := sqlKeyRowIDs(t, ctx, db, table)
			if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeReplaceAll}); err != nil {
				t.Fatalf("narrowed restore consulted out-of-scope %s: %v", subject, err)
			}
			if got := sqlKeyRowIDs(t, ctx, db, table); !reflect.DeepEqual(got, before) {
				t.Fatalf("out-of-scope %s row ids = %v, want %v", subject, got, before)
			}
		})
	}
}

func TestSQLLimitKeyAxes(t *testing.T) {
	for _, tc := range []struct {
		axis string
		pair [2]domain.KeyedLimit
	}{
		{"order_size_account", [2]domain.KeyedLimit{
			domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-1", Asset: "AAPL", MaxQuantity: "10"},
			domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-2", Asset: "AAPL", MaxQuantity: "10"},
		}},
		{"order_size_asset", [2]domain.KeyedLimit{
			domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "10"},
			domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeUnderlyingAsset, Asset: "USD", MaxQuantity: "20"},
		}},
		{"pnl_account", [2]domain.KeyedLimit{
			domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccount, Account: "acc-1", Currency: "USD", LowerBound: "-10"},
			domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccount, Account: "acc-2", Currency: "USD", LowerBound: "-20"},
		}},
		{"position_account", [2]domain.KeyedLimit{
			domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", MaxPosition: "10"},
			domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-2", Asset: "AAPL", MaxPosition: "20"},
		}},
		{"position_asset", [2]domain.KeyedLimit{
			domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", MaxPosition: "10"},
			domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "USD", MaxPosition: "20"},
		}},
		{"policy", [2]domain.KeyedLimit{
			domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-10"},
			domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisUnrealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-20"},
		}},
		{"mode", [2]domain.KeyedLimit{
			domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "10"},
			domain.LimitOrderSize{Mode: domain.RuleModeReviewWithoutHold, Scope: domain.ScopeBroker, MaxQuantity: "20"},
		}},
		{"scope", [2]domain.KeyedLimit{
			domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "10"},
			domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeSettlementAsset, Asset: "AAPL", MaxNotional: "20"},
		}},
		{"account", [2]domain.KeyedLimit{
			domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, Account: "acc-1", MaxOrders: 10, Window: time.Second},
			domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAccount, Account: "acc-2", MaxOrders: 20, Window: time.Second},
		}},
		{"account_group", [2]domain.KeyedLimit{
			domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccountGroup, AccountGroup: "first", Currency: "USD", LowerBound: "-10"},
			domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccountGroup, AccountGroup: "second", Currency: "USD", LowerBound: "-20"},
		}},
		{"asset", [2]domain.KeyedLimit{
			domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAsset, Asset: "AAPL", MaxOrders: 10, Window: time.Second},
			domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeAsset, Asset: "USD", MaxOrders: 20, Window: time.Second},
		}},
	} {
		t.Run(tc.axis, func(t *testing.T) {
			for _, operation := range []string{"write", "delete", "restore", "prune_one", "prune_both", "prune_one_direct", "prune_both_direct"} {
				t.Run(operation, func(t *testing.T) {
					ctx, rs := seedLimitFixtures(t)
					if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
						t.Fatal(err)
					}
					for _, group := range []string{"first", "second"} {
						if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
							t.Fatal(err)
						}
					}
					// Raw inserts isolate delete and prune from upsert predicates.
					for _, limit := range tc.pair {
						if operation == "write" {
							putSQLKeyLimit(t, ctx, rs, limit)
						} else {
							insertSQLKeyLimit(t, ctx, rs, limit)
						}
					}
					want := tc.pair[:]
					switch operation {
					case "delete":
						key, err := tc.pair[0].Key()
						if err != nil {
							t.Fatal(err)
						}
						if err := rs.DeleteLimit(ctx, key); err != nil {
							t.Fatal(err)
						}
						want = tc.pair[1:]
					case "restore", "prune_one", "prune_both", "prune_one_direct", "prune_both_direct":
						scope := backup.Scope{Sections: []backup.Section{backup.SectionRiskLimits}, Accounts: backup.EntitySelector{All: true}}
						archive, err := rs.ExportBackup(ctx, scope)
						if err != nil {
							t.Fatal(err)
						}
						mode := backup.RestoreModeReplaceAll
						switch operation {
						case "restore":
							mode = backup.RestoreModeOverwrite
							for _, table := range []string{"limit_rate", "limit_order_size", "limit_position", "limit_spot_funds_pnl_bound"} {
								db, err := rs.(*realmStore).db()
								if err != nil {
									t.Fatal(err)
								}
								if _, err := db.ExecContext(ctx, `DELETE FROM `+table); err != nil {
									t.Fatal(err)
								}
							}
						case "prune_one", "prune_one_direct":
							want = tc.pair[:1]
							archive.Data.RateLimits = nil
							archive.Data.OrderSizeLimits = nil
							archive.Data.PositionLimits = nil
							if err := archive.Data.SetSpotFundsPnlBounds(nil); err != nil {
								t.Fatal(err)
							}
							switch first := tc.pair[0].(type) {
							case domain.LimitRate:
								archive.Data.RateLimits = []domain.LimitRate{first}
							case domain.LimitOrderSize:
								archive.Data.OrderSizeLimits = []domain.LimitOrderSize{first}
							case domain.LimitPosition:
								archive.Data.PositionLimits = []domain.LimitPosition{first}
							case domain.LimitSpotFundsPnlBounds:
								if err := archive.Data.SetSpotFundsPnlBounds([]domain.LimitSpotFundsPnlBounds{first}); err != nil {
									t.Fatal(err)
								}
							}
						}
						if strings.HasSuffix(operation, "_direct") {
							requireSQLKeyPrune(t, ctx, rs, archive.Data, want)
							return
						}
						if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: mode}); err != nil {
							t.Fatal(err)
						}
					}
					requireSQLKeyLimits(t, ctx, rs, want...)
				})
			}
		})
	}
}

func putSQLKeyLimit(t *testing.T, ctx context.Context, rs fwstore.RealmStore, limit domain.KeyedLimit) {
	t.Helper()
	var err error
	switch limit := limit.(type) {
	case domain.LimitRate:
		err = rs.PutRateLimit(ctx, limit)
	case domain.LimitOrderSize:
		err = rs.PutOrderSizeLimit(ctx, limit)
	case domain.LimitPosition:
		err = rs.PutPositionLimit(ctx, limit)
	case domain.LimitSpotFundsPnlBounds:
		err = rs.PutSpotFundsPnlBoundsLimit(ctx, limit)
	default:
		t.Fatalf("unsupported barrier %T", limit)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func insertSQLKeyLimit(t *testing.T, ctx context.Context, rs fwstore.RealmStore, limit domain.KeyedLimit) {
	t.Helper()
	db, err := rs.(*realmStore).db()
	if err != nil {
		t.Fatal(err)
	}
	switch limit := limit.(type) {
	case domain.LimitRate:
		_, err = db.ExecContext(ctx, `INSERT INTO limit_rate (scope, account_id, asset_id, max_orders, window) VALUES (?, (SELECT id FROM account WHERE code = ?), (SELECT id FROM asset WHERE code = ?), ?, ?)`, limit.Scope, limit.Account, limit.Asset, int64(limit.MaxOrders), limit.Window.String())
	case domain.LimitOrderSize:
		_, err = db.ExecContext(ctx, `INSERT INTO limit_order_size (scope, account_id, asset_id, max_quantity, max_notional, mode) VALUES (?, (SELECT id FROM account WHERE code = ?), (SELECT id FROM asset WHERE code = ?), ?, ?, ?)`, limit.Scope, limit.Account, limit.Asset, nullableString(limit.MaxQuantity), nullableString(limit.MaxNotional), limit.Mode)
	case domain.LimitPosition:
		_, err = db.ExecContext(ctx, `INSERT INTO limit_position (scope, account_id, asset_id, max_position) VALUES (?, (SELECT id FROM account WHERE code = ?), (SELECT id FROM asset WHERE code = ?), ?)`, limit.Scope, limit.Account, limit.Asset, limit.MaxPosition)
	case domain.LimitSpotFundsPnlBounds:
		_, err = db.ExecContext(ctx, `INSERT INTO limit_spot_funds_pnl_bound (axis, scope, account_id, account_group_id, currency_asset_id, lower_bound, upper_bound, mode) VALUES (?, ?, (SELECT id FROM account WHERE code = ?), (SELECT id FROM account_group WHERE code = ?), (SELECT id FROM asset WHERE code = ?), ?, ?, ?)`, limit.Axis, limit.Scope, limit.Account, limit.AccountGroup, limit.Currency, nullableString(limit.LowerBound), nullableString(limit.UpperBound), limit.Mode)
	default:
		t.Fatalf("unsupported barrier %T", limit)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func requireSQLKeyLimits(t *testing.T, ctx context.Context, rs fwstore.RealmStore, want ...domain.KeyedLimit) {
	t.Helper()
	data := snapshotBackupData(t, ctx, rs)
	got := make(map[domain.LimitKey]domain.KeyedLimit)
	var all []domain.KeyedLimit
	for _, l := range data.RateLimits {
		all = append(all, l)
	}
	for _, l := range data.OrderSizeLimits {
		all = append(all, l)
	}
	for _, l := range data.PositionLimits {
		all = append(all, l)
	}
	for _, l := range data.SpotFundsPnlBounds() {
		all = append(all, l)
	}
	if len(all) != len(want) {
		t.Fatalf("stored barrier count = %d, want %d: %+v", len(all), len(want), all)
	}
	for _, l := range all {
		key, err := l.Key()
		if err != nil {
			t.Fatal(err)
		}
		got[key] = l
	}
	for _, l := range want {
		key, err := l.Key()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[key], l) {
			t.Fatalf("stored barrier = %+v, want %+v", got[key], l)
		}
	}
}

func mustSQLLimitKey(t *testing.T, policy string, mode domain.RuleMode, scope domain.LimitScope, account domain.AccountID, group, asset string) domain.LimitKey {
	t.Helper()
	key, err := domain.NewLimitKey(policy, mode, scope, account, group, asset)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSQLLimitKeyRejectsBeforeStorage(t *testing.T) {
	ctx := context.Background()
	s, rs := newTestStore(t)
	limit := domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxOrders: 10, Window: time.Second}
	putSQLKeyLimit(t, ctx, rs, limit)
	foreign := mustSQLLimitKey(t, domain.PolicySpotFundsLimitMode, "", domain.ScopeGlobal, "", "", "")
	for _, key := range []domain.LimitKey{{}, foreign} {
		err := rs.DeleteLimit(ctx, key)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("DeleteLimit(%+v) = %v, want ErrInvalid", key, err)
		}
		if err.Error() != fmt.Sprintf("store: policy %q has no limit barrier table: invalid", key.Policy()) {
			t.Fatalf("DeleteLimit(%+v) message = %v, want policy and missing barrier table", key, err)
		}
		requireSQLKeyLimits(t, ctx, rs, limit)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []domain.LimitKey{{}, foreign} {
		if err := rs.DeleteLimit(ctx, key); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("closed DeleteLimit(%+v) = %v, want ErrInvalid before database access", key, err)
		}
	}
}

func TestSQLLimitKeyPruneRejectsCorruptStoredRow(t *testing.T) {
	for _, table := range []string{"limit_rate", "limit_spot_funds_pnl_bound"} {
		t.Run(table, func(t *testing.T) {
			ctx, rs := seedLimitFixtures(t)
			scope := backup.Scope{Sections: []backup.Section{backup.SectionRiskLimits}, Accounts: backup.EntitySelector{All: true}}
			archive, err := rs.ExportBackup(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			db, err := rs.(*realmStore).db()
			if err != nil {
				t.Fatal(err)
			}
			if table == "limit_rate" {
				_, err = db.ExecContext(ctx, `INSERT INTO limit_rate (scope, max_orders, window) VALUES ('bogus', 1, '1s')`)
			} else {
				_, err = db.ExecContext(ctx, `INSERT INTO limit_spot_funds_pnl_bound (axis, scope, currency_asset_id, lower_bound) VALUES ('realized', 'bogus', (SELECT id FROM asset WHERE code = 'USD'), '-10')`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeReplaceAll}); err == nil || errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), table) {
				t.Fatalf("prune corrupt %s = %v, want stored-row corruption naming table without ErrInvalid", table, err)
			}
			var count int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("corrupt stored rows = %d, want 1 after rollback", count)
			}
		})
	}
}
