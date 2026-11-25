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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

func TestListRowsReadOneSnapshot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"assets", func(t *testing.T) {
			_, writer := newTestStore(t)
			create := func(ctx context.Context, asset string) error {
				_, err := writer.CreateAsset(ctx, domain.Asset{Code: asset})
				return err
			}
			if err := create(t.Context(), "USD"); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, assetSelect, func(ctx context.Context) error {
				return create(ctx, "EUR")
			})
			page, err := reader.ListAssetRows(t.Context(), fwstore.AssetListFilter{})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"asset classes", func(t *testing.T) {
			_, writer := newTestStore(t)
			create := func(ctx context.Context, code string) error {
				return writer.CreateAssetClass(ctx, domain.AssetClass{Code: code})
			}
			if err := create(t.Context(), "fiat"); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, "SELECT c.code, c.title, c.notes, COUNT(a.id)", func(ctx context.Context) error {
				return create(ctx, "equity")
			})
			page, err := reader.ListAssetClassRows(t.Context(), fwstore.AssetClassListFilter{})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"groups", func(t *testing.T) {
			_, writer := newTestStore(t)
			create := func(ctx context.Context, code string) error {
				_, err := writer.CreateGroup(ctx, domain.AccountGroup{Code: code})
				return err
			}
			if err := create(t.Context(), "desk-east"); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, "SELECT g.id, g.code, g.title, ca.code", func(ctx context.Context) error {
				return create(ctx, "desk-west")
			})
			page, err := reader.ListGroupRows(t.Context(), fwstore.GroupListFilter{
				Code: fwstore.TextMatcher{Fragments: []string{"desk-"}, AnchorStart: true},
			})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"default group", func(t *testing.T) {
			_, writer := newTestStore(t)
			ctx := t.Context()
			if _, err := writer.CreateGroup(ctx, domain.AccountGroup{Code: "desk-east"}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, "SELECT g.id, g.code, g.title, ca.code", func(ctx context.Context) error {
				return writer.SetAccountGroup(ctx, "acc-1", "desk-east")
			})
			page, err := reader.ListGroupRows(t.Context(), fwstore.GroupListFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 || len(page.Rows) != 2 ||
				page.Rows[0].Group.Code != "" || page.Rows[1].Group.Code != "desk-east" {
				t.Fatalf("want the pinned default group and desk-east, got %+v", page)
			}
			defaultCount, deskCount := page.Rows[0].AccountCount, page.Rows[1].AccountCount
			if defaultCount+deskCount != 1 {
				t.Fatalf("acc-1 counted %d times across default (%d) and desk-east (%d), want once",
					defaultCount+deskCount, defaultCount, deskCount)
			}
		}},
		{"accounts", func(t *testing.T) {
			_, writer := newTestStore(t)
			create := func(ctx context.Context, code domain.AccountID) error {
				_, err := writer.CreateAccount(ctx, domain.Account{Code: code})
				return err
			}
			if err := create(t.Context(), "acc-1"); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, "SELECT a.id, a.code, a.title, a.pnl", func(ctx context.Context) error {
				return create(ctx, "acc-2")
			})
			page, err := reader.ListAccountRows(t.Context(), fwstore.AccountListFilter{})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"adjustments", func(t *testing.T) {
			_, writer := seedAdjustmentFixtures(t)
			appendAdjustment := func(ctx context.Context, balance, delta string) error {
				record := sampleAdjustment()
				record.Request.Balance.Value = balance
				record.Accepted.BalanceDelta = delta
				record.Accepted.BalanceResult = balance
				_, err := writer.AppendAdjustment(ctx, record)
				return err
			}
			if err := appendAdjustment(t.Context(), "100", "100"); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, adjustmentSelect, func(ctx context.Context) error {
				return appendAdjustment(ctx, "125", "25")
			})
			page, err := reader.ListAdjustmentRows(t.Context(), fwstore.AdjustmentListFilter{
				Account: fwstore.ExactTextMatcher("acc-1"),
			})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"audit", func(t *testing.T) {
			_, writer := seedAuditFixtures(t)
			appendAudit := func(ctx context.Context, maxOrders string) error {
				return writer.AppendAudit(ctx, fwstore.AuditEntry{
					Actor: "operator", Action: domain.AuditActionSetLimit,
					Account: "acc-1", Source: domain.SourcePanel,
					Detail: domain.NewAuditDetail(domain.AuditDetailSetLimit,
						domain.AuditDetailParams{"policy": domain.PolicyRateLimit, "scope": "account"},
						domain.AuditDetailField{Name: "account", Value: "acc-1"},
						domain.AuditDetailField{Name: "max_orders", Value: maxOrders},
						domain.AuditDetailField{Name: "window", Value: "1s"},
					),
				})
			}
			if err := appendAudit(t.Context(), "100"); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, "SELECT au.external_id, au.account_code", func(ctx context.Context) error {
				return appendAudit(ctx, "50")
			})
			page, err := reader.ListAuditRows(t.Context(), fwstore.AuditListFilter{})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"balances", func(t *testing.T) {
			_, writer := seedBalanceFixtures(t)
			upsert := func(ctx context.Context, asset, amount string) error {
				return writer.UpsertBalance(ctx, domain.Balance{
					Account: "acc-1", Asset: asset, Available: amount, RealizedPnl: "0",
				})
			}
			if err := upsert(t.Context(), "USD", "1000"); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, balanceSelect, func(ctx context.Context) error {
				return upsert(ctx, "AAPL", "10")
			})
			page, err := reader.ListBalanceRows(t.Context(), fwstore.BalanceListFilter{
				Account: fwstore.ExactTextMatcher("acc-1"),
			})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"policies", func(t *testing.T) {
			_, writer := seedLimitFixtures(t)
			put := func(ctx context.Context, asset string) error {
				return writer.PutRateLimit(ctx, domain.LimitRate{
					Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset,
					Account: "acc-1", Asset: asset, MaxOrders: 100, Window: time.Minute,
				})
			}
			if err := put(t.Context(), "AAPL"); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, "SELECT kind, scope, account_code, account_group_code", func(ctx context.Context) error {
				return put(ctx, "USD")
			})
			kind := fwstore.PolicyKindRate
			page, err := reader.ListPolicyRows(t.Context(), fwstore.PolicyListFilter{Kind: &kind})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"orders", func(t *testing.T) {
			_, writer := seedOrderFixtures(t)
			order := sampleOrder()
			order.Lock = nil
			order.Leaves = order.AmountValue
			if _, err := writer.CreateOrder(t.Context(), order); err != nil {
				t.Fatal(err)
			}
			reader := listSnapshotReader(t, writer, orderListSelect, func(ctx context.Context) error {
				order.Price = "151"
				_, err := writer.CreateOrder(ctx, order)
				return err
			})
			page, err := reader.ListOrderRows(t.Context(), fwstore.OrderListFilter{
				Account: "acc-1",
			})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
		{"order commissions", func(t *testing.T) {
			_, writer := seedOrderFixtures(t)
			created, settlement := seedSubmittedOrderWithFill(t, writer)
			reader := listSnapshotReader(t, writer, "SELECT o.external_id, t.commission_amount, t.commission_currency", func(ctx context.Context) error {
				_, err := writer.RecordOrderSettlement(ctx, settlement)
				return err
			})
			page, err := reader.ListOrderRows(t.Context(), fwstore.OrderListFilter{})
			assertSnapshotPage(t, page.Total, len(page.Rows), 1, err)
			order := page.Rows[0].Order
			submitted := order.Status == domain.OrderStatusSubmitted &&
				order.Leaves == created.Leaves && len(order.CommissionSubtotals) == 0
			filled := order.Status == domain.OrderStatusFilled && order.Leaves == settlement.Leaves &&
				commissionsEqual(order.CommissionSubtotals, []domain.Commission{*settlement.Trade.Commission})
			if !submitted && !filled {
				t.Fatalf("order has status %q, leaves %q, commissions %v, want a complete submitted or filled snapshot",
					order.Status, order.Leaves, order.CommissionSubtotals)
			}
		}},
		{"trades", func(t *testing.T) {
			_, writer := seedOrderFixtures(t)
			_, first := seedSubmittedOrderWithFill(t, writer)
			if _, err := writer.RecordOrderSettlement(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			_, second := seedSubmittedOrderWithFill(t, writer)
			reader := listSnapshotReader(t, writer, "SELECT t.external_id, o.external_id, a.code", func(ctx context.Context) error {
				_, err := writer.RecordOrderSettlement(ctx, second)
				return err
			})
			page, err := reader.ListTradeRows(t.Context(), fwstore.TradeListFilter{
				Account: fwstore.ExactTextMatcher("acc-1"),
			})
			assertSnapshotPage(t, page.Total, len(page.Rows), 2, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

func listSnapshotReader(
	t *testing.T, writer fwstore.RealmStore, pageSQL string, write func(context.Context) error,
) fwstore.RealmStore {
	t.Helper()
	return newSnapshotReader(t, writer.(*realmStore), func(query string) bool {
		return strings.Contains(query, pageSQL)
	}, write)
}

func assertSnapshotPage(t *testing.T, total, count, afterWriteCount int, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if total != count || (count != 1 && count != afterWriteCount) {
		t.Fatalf("page total/rows = %d/%d, want matching counts from before (1) or after (%d) the write",
			total, count, afterWriteCount)
	}
}
