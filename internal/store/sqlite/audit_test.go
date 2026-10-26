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

// Audit-group tests: append/list with the external id surfaced (22-char handle,
// surrogate id never exposed), newest-first ordering, the account/source/action
// filters applied in the query, and that deleting an account or principal leaves
// immutable audit snapshots intact, so the compliance trail survives deletes.

package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// seedAuditFixtures creates an account and an actor principal for the audit tests.
func seedAuditFixtures(t *testing.T) (context.Context, fwstore.RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	return ctx, rs
}

func TestAuditDetailFieldsRoundTrip(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	detail := domain.NewAuditDetail(domain.AuditDetailSetLimit,
		domain.AuditDetailParams{"policy": domain.PolicyRateLimit, "scope": "account"},
		domain.AuditDetailField{Name: "account", Value: "acc-1"},
		domain.AuditDetailField{Name: "max_orders", Value: "100"},
		domain.AuditDetailField{Name: "window", Value: "1s"},
	)
	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Actor:   "operator",
		Action:  domain.AuditActionSetLimit,
		Account: "acc-1",
		Detail:  detail,
		Source:  domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListAudit len = %d, want 1", len(rows))
	}
	// The tail reads back in the order it was written.
	if !reflect.DeepEqual(rows[0].Detail, detail) {
		t.Fatalf("detail = %+v, want %+v", rows[0].Detail, detail)
	}
}

func TestAuditAppendListAndExternalID(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	orderDetail := domain.NewAuditDetail(domain.AuditDetailSubmitOrderRejected, domain.AuditDetailParams{
		"order":   "aW50ZWdyYXRpb24tYXVkaQ",
		"account": "acc-1",
		"base":    "AAPL",
		"quote":   "USD",
		"side":    string(domain.OrderSideBuy),
	})
	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Actor:   "operator",
		Action:  domain.AuditActionSubmitOrder,
		Account: "acc-1",
		OrderID: "aW50ZWdyYXRpb24tYXVkaQ", Verdict: "reject", RejectCode: "order_qty_exceeds_limit",
		Detail: orderDetail,
		Source: domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	// A system-initiated row with no account/actor.
	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Action: domain.AuditActionHydrate,
		Detail: domain.NewAuditDetail(domain.AuditDetailHydrate, domain.AuditDetailParams{
			"assets": "0", "accounts": "0", "barriers": "0", "groups": "0", "balances": "0",
		}),
		Source: domain.SourceSystem,
	}); err != nil {
		t.Fatalf("AppendAudit(system): %v", err)
	}

	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListAudit len = %d, want 2", len(rows))
	}

	// Newest first: the system hydrate row was appended last.
	if rows[0].Action != domain.AuditActionHydrate {
		t.Fatalf("ListAudit[0].Action = %q, want hydrate (newest first)", rows[0].Action)
	}
	// External id is the 22-char public handle; the surrogate id is never surfaced
	// (AuditRow has no such field).
	for _, row := range rows {
		if row.ExternalID.IsZero() {
			t.Fatal("audit row external id is zero")
		}
		if len(row.ExternalID.String()) != domain.ExternalIDStringLen {
			t.Fatalf("external id string len = %d, want %d",
				len(row.ExternalID.String()), domain.ExternalIDStringLen)
		}
	}

	// The control-action row surfaces the account and actor codes; the system row
	// surfaces neither and defaults its source to system.
	control := rows[1]
	if control.OrderID != "aW50ZWdyYXRpb24tYXVkaQ" || control.Verdict != "reject" || control.RejectCode != "order_qty_exceeds_limit" {
		t.Fatalf("decision fields = %+v", control)
	}
	if control.Account != "acc-1" || control.Actor != "operator" {
		t.Fatalf("control row refs = (%q, %q), want (acc-1, operator)", control.Account, control.Actor)
	}
	if control.Source != domain.SourcePanel {
		t.Fatalf("control row source = %q, want panel", control.Source)
	}
	// The detail reads back with the code and values it was written with.
	if !reflect.DeepEqual(control.Detail, orderDetail) {
		t.Fatalf("control row detail = %+v, want %+v", control.Detail, orderDetail)
	}
	exact, err := rs.ListAuditRows(ctx, fwstore.AuditListFilter{
		ExternalID: control.ExternalID,
		Page:       fwstore.PageSpec{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListAuditRows(external id): %v", err)
	}
	if len(exact.Rows) != 1 ||
		!reflect.DeepEqual(exact.Rows[0], control) {
		t.Fatalf("external id page = %+v", exact.Rows)
	}
	system := rows[0]
	if system.Account != "" || system.Actor != "" {
		t.Fatalf("system row refs = (%q, %q), want empty", system.Account, system.Actor)
	}
	if system.Source != domain.SourceSystem {
		t.Fatalf("system row source = %q, want system (default)", system.Source)
	}

	// A non-positive n returns an empty slice.
	empty, err := rs.ListAudit(ctx, 0)
	if err != nil {
		t.Fatalf("ListAudit(0): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListAudit(0) len = %d, want 0", len(empty))
	}
}

func TestAppendAuditValidatesDecisionMetadata(t *testing.T) {
	valid := []struct {
		name  string
		entry fwstore.AuditEntry
	}{
		{
			name: "accepted order",
			entry: fwstore.AuditEntry{
				Action:  domain.AuditActionSubmitOrder,
				OrderID: "opaque-accepted-order",
				Verdict: "accept",
			},
		},
		{
			name: "rejected drop copy with code",
			entry: fwstore.AuditEntry{
				Action:     domain.AuditActionSubmitDropCopy,
				OrderID:    "opaque-rejected-drop-copy",
				Verdict:    "reject",
				RejectCode: "order_size",
			},
		},
		{
			name: "rejected order without code",
			entry: fwstore.AuditEntry{
				Action:  domain.AuditActionSubmitOrder,
				OrderID: "opaque-rejected-order",
				Verdict: "reject",
			},
		},
		{
			name: "unrelated action without decision metadata",
			entry: fwstore.AuditEntry{
				Action: domain.AuditActionCreateAccount,
			},
		},
	}
	for _, tc := range valid {
		t.Run("valid "+tc.name, func(t *testing.T) {
			ctx, rs := seedAuditFixtures(t)
			entry := tc.entry
			entry.Source = domain.SourcePanel
			if err := rs.AppendAudit(ctx, entry); err != nil {
				t.Fatalf("AppendAudit: %v", err)
			}
			rows, err := rs.ListAudit(ctx, 10)
			if err != nil {
				t.Fatalf("ListAudit: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("audit rows = %+v, want one", rows)
			}
		})
	}

	invalid := []struct {
		name  string
		entry fwstore.AuditEntry
	}{
		{
			name: "unknown verdict",
			entry: fwstore.AuditEntry{
				Action:  domain.AuditActionSubmitOrder,
				OrderID: "opaque-order",
				Verdict: "maybe",
			},
		},
		{
			name: "missing order id",
			entry: fwstore.AuditEntry{
				Action:  domain.AuditActionSubmitOrder,
				Verdict: "accept",
			},
		},
		{
			name: "missing verdict",
			entry: fwstore.AuditEntry{
				Action:  domain.AuditActionSubmitDropCopy,
				OrderID: "opaque-order",
			},
		},
		{
			name: "accept with reject code",
			entry: fwstore.AuditEntry{
				Action:  domain.AuditActionSubmitOrder,
				OrderID: "opaque-order",
				Verdict: "accept", RejectCode: "order_size",
			},
		},
		{
			name: "unrelated action with order id",
			entry: fwstore.AuditEntry{
				Action:  domain.AuditActionCreateAccount,
				OrderID: "opaque-order",
			},
		},
		{
			name: "unrelated action with verdict",
			entry: fwstore.AuditEntry{
				Action:  domain.AuditActionCreateAccount,
				Verdict: "accept",
			},
		},
		{
			name: "unrelated action with reject code",
			entry: fwstore.AuditEntry{
				Action:     domain.AuditActionCreateAccount,
				RejectCode: "order_size",
			},
		},
	}
	for _, tc := range invalid {
		t.Run("invalid "+tc.name, func(t *testing.T) {
			ctx, rs := seedAuditFixtures(t)
			// The source is valid so the rejection is the decision rule's.
			entry := tc.entry
			entry.Source = domain.SourcePanel
			err := rs.AppendAudit(ctx, entry)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("AppendAudit error = %v, want ErrInvalid", err)
			}
			rows, listErr := rs.ListAudit(ctx, 10)
			if listErr != nil {
				t.Fatalf("ListAudit: %v", listErr)
			}
			if len(rows) != 0 {
				t.Fatalf("rejected audit entry persisted rows: %+v", rows)
			}
		})
	}
}

func TestAppendAuditBatchIsAtomic(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	err := rs.AppendAuditBatch(ctx, []fwstore.AuditEntry{
		{
			Action: domain.AuditActionCreateAccount,
			Detail: domain.NewAuditDetail(
				domain.AuditDetailCreateAccount, domain.AuditDetailParams{"account": "acc-1"},
			),
			Source: domain.SourcePanel,
		},
		{
			Action: domain.AuditAction("unknown_action"),
			Detail: domain.NewAuditDetail(
				domain.AuditDetailCreateAccount, domain.AuditDetailParams{"account": "acc-2"},
			),
			Source: domain.SourcePanel,
		},
	})
	if err == nil {
		t.Fatal("AppendAuditBatch succeeded with an invalid second entry")
	}
	rows, listErr := rs.ListAudit(ctx, 10)
	if listErr != nil {
		t.Fatalf("ListAudit: %v", listErr)
	}
	if len(rows) != 0 {
		t.Fatalf("failed batch persisted partial rows: %+v", rows)
	}

	err = rs.AppendAuditBatch(ctx, []fwstore.AuditEntry{
		{
			Action: domain.AuditActionCreateAccount,
			Detail: domain.NewAuditDetail(
				domain.AuditDetailCreateAccount, domain.AuditDetailParams{"account": "acc-1"},
			),
			Source: domain.SourcePanel,
		},
		{
			Action:  domain.AuditActionCreateAccount,
			OrderID: "decision-on-unrelated-action",
			Detail: domain.NewAuditDetail(
				domain.AuditDetailCreateAccount, domain.AuditDetailParams{"account": "acc-2"},
			),
			Source: domain.SourcePanel,
		},
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AppendAuditBatch metadata error = %v, want ErrInvalid", err)
	}
	rows, listErr = rs.ListAudit(ctx, 10)
	if listErr != nil {
		t.Fatalf("ListAudit after metadata batch: %v", listErr)
	}
	if len(rows) != 0 {
		t.Fatalf("invalid metadata batch persisted partial rows: %+v", rows)
	}

	if err := rs.AppendAuditBatch(ctx, []fwstore.AuditEntry{
		{Action: domain.AuditActionCreateAccount, Detail: domain.NewAuditDetail(
			domain.AuditDetailCreateAccount, domain.AuditDetailParams{"account": "acc-1"},
		), Source: domain.SourcePanel},
		{Action: domain.AuditActionUpdateAccount, Detail: domain.NewAuditDetail(
			domain.AuditDetailUpdateAccount, domain.AuditDetailParams{"account": "acc-1"},
		), Source: domain.SourcePanel},
	}); err != nil {
		t.Fatalf("AppendAuditBatch: %v", err)
	}
	rows, err = rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit after successful batch: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("successful batch rows = %d, want 2", len(rows))
	}
}

func TestAuditAppendUnknownRefIsSnapshot(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Action:  domain.AuditActionBlock,
		Account: "ghost",
		Actor:   "ghost",
		Source:  domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit(unknown refs): %v", err)
	}
	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "ghost" || rows[0].Actor != "ghost" {
		t.Fatalf("unknown refs snapshot = %+v", rows)
	}
}

// TestAuditTitleSnapshotPersistedVerbatim covers the title snapshot path: an
// entry that carries non-empty account/actor titles persists them as given, and
// ListAudit reads them back unchanged - no lookup is consulted.
func TestAuditTitleSnapshotPersistedVerbatim(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Actor:        "operator",
		ActorTitle:   "Snapshot Operator",
		Action:       domain.AuditActionBlock,
		Account:      "acc-1",
		AccountTitle: "Snapshot Account",
		Source:       domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListAudit len = %d, want 1", len(rows))
	}
	if rows[0].AccountTitle != "Snapshot Account" {
		t.Fatalf("account title = %q, want Snapshot Account", rows[0].AccountTitle)
	}
	if rows[0].ActorTitle != "Snapshot Operator" {
		t.Fatalf("actor title = %q, want Snapshot Operator", rows[0].ActorTitle)
	}
}

// TestAuditTitleBackfilledFromLiveRows covers the lookupTitle safety-net: an
// entry with empty titles but codes matching live account/principal rows
// backfills both titles from those rows at write time.
func TestAuditTitleBackfilledFromLiveRows(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Title: "Live Account",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{
		Code: "operator", Title: "Live Operator",
	}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Actor:   "operator",
		Action:  domain.AuditActionBlock,
		Account: "acc-1",
		Source:  domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListAudit len = %d, want 1", len(rows))
	}
	if rows[0].AccountTitle != "Live Account" {
		t.Fatalf("account title = %q, want Live Account (backfilled)", rows[0].AccountTitle)
	}
	if rows[0].ActorTitle != "Live Operator" {
		t.Fatalf("actor title = %q, want Live Operator (backfilled)", rows[0].ActorTitle)
	}
}

func TestAuditFiltered(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}

	mustAppend := func(e fwstore.AuditEntry) {
		t.Helper()
		if err := rs.AppendAudit(ctx, e); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	mustAppend(fwstore.AuditEntry{Action: domain.AuditActionBlock, Account: "acc-1", Source: domain.SourcePanel})
	mustAppend(fwstore.AuditEntry{Action: domain.AuditActionUnblock, Account: "acc-1", Source: domain.SourceAPI})
	mustAppend(fwstore.AuditEntry{Action: domain.AuditActionBlock, Account: "acc-2", Source: domain.SourcePanel})
	mustAppend(fwstore.AuditEntry{
		Action:  domain.AuditActionSubmitOrder,
		Account: "acc-2", Source: domain.SourceMCP,
		OrderID: "opaque-filter-order", Verdict: "accept",
	})

	// Account filter narrows to acc-1's two rows.
	rows, err := rs.ListAuditFiltered(ctx, domain.AuditFilter{Account: "acc-1"}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(account): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("account filter len = %d, want 2", len(rows))
	}

	// Source filter narrows to the two panel rows.
	rows, err = rs.ListAuditFiltered(ctx, domain.AuditFilter{Source: domain.SourcePanel}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(source): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("source filter len = %d, want 2", len(rows))
	}

	// Action filter narrows to the two block rows.
	rows, err = rs.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(actions): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("action filter len = %d, want 2", len(rows))
	}

	// Combined account+action filter narrows to acc-1's single block row.
	rows, err = rs.ListAuditFiltered(ctx, domain.AuditFilter{
		Account: "acc-1",
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(combined): %v", err)
	}
	if len(rows) != 1 || rows[0].Action != domain.AuditActionBlock || rows[0].Account != "acc-1" {
		t.Fatalf("combined filter = %+v", rows)
	}

	// The limit bounds the filtered set, not a pre-filter window: 1 of the 2 block
	// rows, newest first (acc-2's block was appended after acc-1's).
	rows, err = rs.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 1)
	if err != nil {
		t.Fatalf("ListAuditFiltered(limit): %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "acc-2" {
		t.Fatalf("limit-bounded filter = %+v, want one acc-2 block row", rows)
	}
}

// TestAuditAssetFilter verifies the asset filter on the paged list path: an
// adjustment row and the asset create/update/delete rows are filterable by the
// asset code they recorded, and rows with no asset are excluded.
func TestAuditAssetFilter(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)
	mustAppend := func(entry fwstore.AuditEntry) {
		t.Helper()
		entry.Source = domain.SourcePanel
		if err := rs.AppendAudit(ctx, entry); err != nil {
			t.Fatalf("AppendAudit(%s): %v", entry.Detail, err)
		}
	}
	adjustmentDetail := func(asset string) domain.AuditDetail {
		return domain.NewAuditDetail(domain.AuditDetailAdjustmentAccepted, domain.AuditDetailParams{
			"account":        "acc-1",
			"asset":          asset,
			"adjustmentType": string(domain.AdjustmentTypeCorrection),
		})
	}
	assetParams := domain.AuditDetailParams{"asset": "AAPL"}
	mustAppend(fwstore.AuditEntry{
		Action: domain.AuditActionAdjustment, Account: "acc-1", Asset: "AAPL",
		Detail: adjustmentDetail("AAPL"),
	})
	mustAppend(fwstore.AuditEntry{
		Action: domain.AuditActionCreateAsset, Asset: "AAPL",
		Detail: domain.NewAuditDetail(domain.AuditDetailCreateAsset, assetParams),
	})
	mustAppend(fwstore.AuditEntry{
		Action: domain.AuditActionUpdateAsset, Asset: "AAPL",
		Detail: domain.NewAuditDetail(domain.AuditDetailUpdateAsset, assetParams),
	})
	mustAppend(fwstore.AuditEntry{
		Action: domain.AuditActionDeleteAsset, Asset: "AAPL",
		Detail: domain.NewAuditDetail(domain.AuditDetailDeleteAsset, assetParams),
	})
	// Rows with no asset, or a different asset, must be excluded.
	mustAppend(fwstore.AuditEntry{
		Action: domain.AuditActionBlock, Account: "acc-1",
		Detail: domain.NewAuditDetail(
			domain.AuditDetailBlockAccount, domain.AuditDetailParams{"account": "acc-1"},
		),
	})
	mustAppend(fwstore.AuditEntry{
		Action: domain.AuditActionAdjustment, Account: "acc-1", Asset: "MSFT",
		Detail: adjustmentDetail("MSFT"),
	})

	page, err := rs.ListAuditRows(ctx, fwstore.AuditListFilter{
		Asset: fwstore.ExactTextMatcher("AAPL"),
		Page:  fwstore.PageSpec{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListAuditRows(asset): %v", err)
	}
	if len(page.Rows) != 4 {
		t.Fatalf("asset filter len = %d, want 4", len(page.Rows))
	}
	wantActions := map[domain.AuditAction]bool{
		domain.AuditActionAdjustment:  true,
		domain.AuditActionCreateAsset: true,
		domain.AuditActionUpdateAsset: true,
		domain.AuditActionDeleteAsset: true,
	}
	for _, row := range page.Rows {
		if row.Asset != "AAPL" {
			t.Fatalf("row asset = %q, want AAPL", row.Asset)
		}
		if !wantActions[row.Action] {
			t.Fatalf("unexpected action %q in asset-filtered rows", row.Action)
		}
	}
}

func TestAuditAccountFilterUsesImmutableCodeSnapshot(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)
	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Action:  domain.AuditActionBlock,
		Account: "acc-1",
		Detail: domain.NewAuditDetail(
			domain.AuditDetailBlockAccount, domain.AuditDetailParams{"account": "acc-1"},
		),
		Source: domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if _, err := rs.UpdateAccount(ctx, "acc-1", domain.Account{
		Code:  "acc-renamed",
		Title: "",
	}); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	page, err := rs.ListAuditRows(ctx, fwstore.AuditListFilter{
		Account: fwstore.ExactTextMatcher("acc-renamed"),
		Actions: []domain.AuditAction{
			domain.AuditActionBlock,
		},
	})
	if err != nil {
		t.Fatalf("ListAuditRows(renamed account): %v", err)
	}
	if len(page.Rows) != 0 {
		t.Fatalf("new-code filter pulled old snapshot rows: %+v", page.Rows)
	}

	page, err = rs.ListAuditRows(ctx, fwstore.AuditListFilter{
		Account: fwstore.ExactTextMatcher("acc-1"),
		Actions: []domain.AuditAction{
			domain.AuditActionBlock,
		},
	})
	if err != nil {
		t.Fatalf("ListAuditRows(old account): %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Account != "acc-1" {
		t.Fatalf("old-code snapshot rows = %+v, want original block", page.Rows)
	}
}

func TestAuditListRowsKeysetPageFilter(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "robot"}); err != nil {
		t.Fatalf("CreatePrincipal(robot): %v", err)
	}
	mustAppend := func(entry fwstore.AuditEntry) {
		t.Helper()
		if err := rs.AppendAudit(ctx, entry); err != nil {
			t.Fatalf("AppendAudit(%s): %v", entry.Detail, err)
		}
	}
	rowOld := domain.NewAuditDetail(
		domain.AuditDetailBlockAccount, domain.AuditDetailParams{"account": "acc-2"},
	)
	rowMid := domain.NewAuditDetail(
		domain.AuditDetailUnblockAccount, domain.AuditDetailParams{"account": "acc-1"},
	)
	rowNew := domain.NewAuditDetail(
		domain.AuditDetailBlockAccount, domain.AuditDetailParams{"account": "acc-1"},
	)
	rowOther := domain.NewAuditDetail(domain.AuditDetailSubmitOrderCommitted, domain.AuditDetailParams{
		"order":   "opaque-page-order",
		"account": "acc-1",
		"base":    "AAPL",
		"quote":   "USD",
		"side":    string(domain.OrderSideBuy),
	})
	mustAppend(fwstore.AuditEntry{
		Actor: "operator", Action: domain.AuditActionBlock,
		Account: "acc-2", Source: domain.SourcePanel, Detail: rowOld,
	})
	mustAppend(fwstore.AuditEntry{
		Actor: "operator", Action: domain.AuditActionUnblock,
		Account: "acc-1", Source: domain.SourcePanel, Detail: rowMid,
	})
	mustAppend(fwstore.AuditEntry{
		Actor: "operator", Action: domain.AuditActionBlock,
		Account: "acc-1", Source: domain.SourcePanel, Detail: rowNew,
	})
	mustAppend(fwstore.AuditEntry{
		Actor: "robot", Action: domain.AuditActionSubmitOrder,
		Account: "acc-1", Source: domain.SourceMCP, Detail: rowOther,
		OrderID: "opaque-page-order", Verdict: "accept",
	})

	r := rs.(*realmStore)
	times := map[string]string{
		rowOld.Text:   "2026-01-01T00:00:00Z",
		rowMid.Text:   "2026-01-02T00:00:00Z",
		rowNew.Text:   "2026-01-03T00:00:00Z",
		rowOther.Text: "2026-01-04T00:00:00Z",
	}
	for detail, at := range times {
		if _, err := r.rawDB().ExecContext(
			ctx, `UPDATE audit SET at = ? WHERE detail = ?`, at, detail,
		); err != nil {
			t.Fatalf("update audit at %s: %v", detail, err)
		}
	}
	minAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	page, err := rs.ListAuditRows(ctx, fwstore.AuditListFilter{
		Actor:  fwstore.ExactTextMatcher("operator"),
		Source: domain.SourcePanel,
		Actions: []domain.AuditAction{
			domain.AuditActionBlock,
			domain.AuditActionUnblock,
		},
		At:   fwstore.TimeRangeFilter{Min: &minAt},
		Page: fwstore.PageSpec{Limit: 1},
	})
	if err != nil {
		t.Fatalf("ListAuditRows(first): %v", err)
	}
	if page.Total != 2 || len(page.Rows) != 1 {
		t.Fatalf("page total/len = %d/%d, want 2/1", page.Total, len(page.Rows))
	}
	if page.Rows[0].Detail.Text != rowNew.Text {
		t.Fatalf("first row detail = %q, want %q", page.Rows[0].Detail.Text, rowNew.Text)
	}

	second, err := rs.ListAuditRows(ctx, fwstore.AuditListFilter{
		Actor:  fwstore.ExactTextMatcher("operator"),
		Source: domain.SourcePanel,
		Actions: []domain.AuditAction{
			domain.AuditActionBlock,
			domain.AuditActionUnblock,
		},
		At:   fwstore.TimeRangeFilter{Min: &minAt},
		Page: fwstore.PageSpec{Limit: 1, Offset: 1},
	})
	if err != nil {
		t.Fatalf("ListAuditRows(second): %v", err)
	}
	if second.Total != 2 || len(second.Rows) != 1 {
		t.Fatalf("second total/len = %d/%d, want 2/1", second.Total, len(second.Rows))
	}
	if second.Rows[0].Detail.Text != rowMid.Text {
		t.Fatalf("second page = %+v", second.Rows)
	}
}

// TestAuditListUsesIndexNoTempSort asserts the audit list query
// (newest-first, no filter and default control-category filter, with and
// without an account restriction) and the restricted filtered query are
// satisfied by an index and never trigger a full TEMP B-TREE sort.
func TestAuditListUsesIndexNoTempSort(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)
	r := rs.(*realmStore)
	dictionaries, err := r.dictionaries()
	if err != nil {
		t.Fatalf("dictionaries: %v", err)
	}
	tx, err := r.rawDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin audit fixture transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	// Populate a few thousand rows so the query planner's index choice is not an
	// artifact of a near-empty table. One transaction keeps fixture preparation
	// from performing thousands of separate durable commits on Windows.
	bulk := domain.NewAuditDetail(
		domain.AuditDetailBlockAccount, domain.AuditDetailParams{"account": "acc-1"},
	)
	for i := 0; i < 4000; i++ {
		if err := appendAudit(ctx, tx, dictionaries, fwstore.AuditEntry{
			Actor: "operator", Action: domain.AuditActionBlock,
			Account: "acc-1", Source: domain.SourcePanel, Detail: bulk,
		}); err != nil {
			t.Fatalf("AppendAudit(%d): %v", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit audit fixtures: %v", err)
	}

	explain := func(query string, queryArgs []any) string {
		t.Helper()
		rows, err := r.rawDB().QueryContext(
			ctx, "EXPLAIN QUERY PLAN "+query, queryArgs...,
		)
		if err != nil {
			t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var plan strings.Builder
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				t.Fatalf("scan plan: %v", err)
			}
			plan.WriteString(detail)
			plan.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate plan: %v", err)
		}
		return plan.String()
	}

	assertPlanIndexedNoSort := func(name, plan string) {
		t.Helper()
		if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
			t.Errorf("%s: plan contains temp b-tree sort:\n%s", name, plan)
		}
		if !strings.Contains(plan, "USING INDEX") &&
			!strings.Contains(plan, "USING COVERING INDEX") {
			t.Errorf("%s: plan does not use an index:\n%s", name, plan)
		}
	}

	assertIndexedNoSort := func(name string, filter fwstore.AuditListFilter) {
		t.Helper()
		clauses, args, err := auditListClauses(dictionaries, filter)
		if err != nil {
			t.Fatalf("auditListClauses: %v", err)
		}
		query, queryArgs := buildAuditListQuery(clauses, args, filter.Page)
		assertPlanIndexedNoSort(name, explain(query, queryArgs))
	}

	restriction := domain.AccountRestriction{Account: "acc-1", IncludeUnowned: true}

	// Default browse view: newest-first, no action filter.
	assertIndexedNoSort("no filter", fwstore.AuditListFilter{
		Page: fwstore.PageSpec{Limit: 50},
	})
	// Default frontend browse view: control category filters trading noise while
	// preserving the newest-first index order.
	assertIndexedNoSort("control category", fwstore.AuditListFilter{
		Category: domain.AuditCategoryControl,
		Page:     fwstore.PageSpec{Limit: 50},
	})
	// The same views under a restricted account scope, which also keeps the rows
	// recorded with no account.
	assertIndexedNoSort("restriction", fwstore.AuditListFilter{
		Restriction: restriction,
		Page:        fwstore.PageSpec{Limit: 50},
	})
	assertIndexedNoSort("restriction and control category", fwstore.AuditListFilter{
		Restriction: restriction,
		Category:    domain.AuditCategoryControl,
		Page:        fwstore.PageSpec{Limit: 50},
	})

	// The recent-activity read under a restricted account scope.
	query, queryArgs, err := buildAuditFilteredQuery(
		dictionaries, domain.AuditFilter{Restriction: restriction}, 50,
	)
	if err != nil {
		t.Fatalf("buildAuditFilteredQuery: %v", err)
	}
	assertPlanIndexedNoSort("filtered restriction", explain(query, queryArgs))

	// An exact account filter on top of the restriction still reads that
	// account's own index range in order.
	query, queryArgs, err = buildAuditFilteredQuery(
		dictionaries,
		domain.AuditFilter{Account: "acc-1", Restriction: restriction},
		50,
	)
	if err != nil {
		t.Fatalf("buildAuditFilteredQuery(account): %v", err)
	}
	plan := explain(query, queryArgs)
	assertPlanIndexedNoSort("filtered restriction and account", plan)
	if !strings.Contains(plan, "idx_audit_account_code") {
		t.Errorf("filtered restriction and account: plan does not use "+
			"idx_audit_account_code:\n%s", plan)
	}
}

// TestAuditTrailPreservedOnAccountDelete verifies an account delete neither
// updates nor deletes its immutable audit snapshots, which remain selectable by
// the recorded account code. The same holds for a deleted actor principal.
func TestAuditTrailPreservedOnAccountDelete(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	blocked := domain.NewAuditDetail(
		domain.AuditDetailBlockAccountReason,
		domain.AuditDetailParams{"account": "acc-1", "reason": "blocked then deleted"},
	)
	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Actor:        "operator",
		ActorTitle:   "Snapshot Operator",
		Action:       domain.AuditActionBlock,
		Account:      "acc-1",
		AccountTitle: "Snapshot Account",
		Detail:       blocked,
		Source:       domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	// Sanity: the row carries both refs before any delete.
	before, _ := rs.ListAudit(ctx, 10)
	if len(before) != 1 || before[0].Account != "acc-1" || before[0].Actor != "operator" {
		t.Fatalf("pre-delete row = %+v", before)
	}
	originalXID := before[0].ExternalID

	// Deleting the account leaves the audit row untouched.
	deleteAccountRaw(t, ctx, rs, "acc-1")
	after, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit after account delete: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("audit row count after account delete = %d, want 1 (trail preserved)", len(after))
	}
	if after[0].ExternalID != originalXID {
		t.Fatalf("audit row external id changed after delete: %v != %v", after[0].ExternalID, originalXID)
	}
	if after[0].Account != "acc-1" || after[0].AccountTitle != "Snapshot Account" {
		t.Fatalf("account snapshot after delete = (%q, %q), want (acc-1, Snapshot Account)",
			after[0].Account, after[0].AccountTitle)
	}
	// The actor is still present (only the account was deleted).
	if after[0].Actor != "operator" {
		t.Fatalf("actor after account delete = %q, want operator", after[0].Actor)
	}
	// The action and detail are untouched - the compliance content survives.
	if after[0].Action != domain.AuditActionBlock ||
		!reflect.DeepEqual(after[0].Detail, blocked) ||
		after[0].Source != domain.SourcePanel {
		t.Fatalf("audit content after delete = %+v", after[0])
	}
	filtered, err := rs.ListAuditRows(ctx, fwstore.AuditListFilter{
		Account: fwstore.ExactTextMatcher("acc-1"),
		Page:    fwstore.PageSpec{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListAuditRows(recorded account): %v", err)
	}
	if len(filtered.Rows) != 1 || filtered.Rows[0].ExternalID != originalXID {
		t.Fatalf("recorded-account audit rows = %+v, want original row", filtered.Rows)
	}

	// Now delete the actor principal: the row must still survive with the actor
	// snapshot intact.
	if err := rs.DeletePrincipal(ctx, "operator"); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	final, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit after principal delete: %v", err)
	}
	if len(final) != 1 {
		t.Fatalf("audit row count after principal delete = %d, want 1 (trail preserved)", len(final))
	}
	if final[0].Actor != "operator" || final[0].ActorTitle != "Snapshot Operator" {
		t.Fatalf("actor snapshot after principal delete = (%q, %q), want (operator, Snapshot Operator)",
			final[0].Actor, final[0].ActorTitle)
	}
	if final[0].Action != domain.AuditActionBlock {
		t.Fatalf("audit action after principal delete = %q, want block", final[0].Action)
	}
}

// deleteAccountRaw deletes an account by code with direct SQL through the shared
// connection so the test isolates audit snapshot behavior from store delete
// policy.
func deleteAccountRaw(
	t *testing.T, ctx context.Context, rs fwstore.RealmStore, code domain.AccountID,
) {
	t.Helper()
	r := rs.(*realmStore)
	res, err := r.rawDB().ExecContext(
		ctx, `DELETE FROM account WHERE code = ?`, code.String(),
	)
	if err != nil {
		t.Fatalf("delete account %q: %v", code, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("delete account %q rows: %v", code, err)
	}
	if n == 0 {
		t.Fatalf("delete account %q affected no rows", code)
	}
}
