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

// Adjustments-group tests: append/list round-trip, external-id opacity,
// opaque JSON preserved byte-for-byte, source filter, principal SET NULL after
// principal delete, cascade on account delete.

package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// seedAdjustmentFixtures creates the dictionary rows adjustment tests need.
func seedAdjustmentFixtures(t *testing.T) (context.Context, RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return ctx, rs
}

// sampleAdjustment returns a fully-populated accepted adjustment record.
func sampleAdjustment() domain.AccountAdjustmentRecord {
	return domain.AccountAdjustmentRecord{
		Account:   "acc-1",
		Asset:     "AAPL",
		Principal: "operator",
		Source:    domain.SourcePanel,
		Request: domain.AdjustmentRequest{
			Asset: "AAPL",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "100",
			},
		},
		Accepted: &domain.AdjustmentOutcomeAccepted{
			BalanceDelta:      "100",
			BalanceResult:     "100",
			RealizedPnlDelta:  "0",
			RealizedPnlResult: "0",
		},
	}
}

func TestAdjustmentAppendListRoundTrip(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	in := sampleAdjustment()
	stored, err := rs.AppendAdjustment(ctx, in)
	if err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}

	// ExternalID must be populated and 22 chars.
	if stored.ExternalID.IsZero() {
		t.Fatal("AppendAdjustment did not populate ExternalID")
	}
	if got := len(stored.ExternalID.String()); got != domain.ExternalIDStringLen {
		t.Fatalf("external id string len = %d, want %d", got, domain.ExternalIDStringLen)
	}
	// At must be set.
	if stored.At.IsZero() {
		t.Fatal("AppendAdjustment did not populate At")
	}

	// ListAdjustments returns the record.
	list, err := rs.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListAdjustments len = %d, want 1", len(list))
	}
	got := list[0]
	if got.ExternalID != stored.ExternalID {
		t.Fatalf("external id mismatch: got %v, want %v", got.ExternalID, stored.ExternalID)
	}
	if got.Account != "acc-1" || got.Asset != "AAPL" || got.Principal != "operator" {
		t.Fatalf("references: account=%q asset=%q principal=%q", got.Account, got.Asset, got.Principal)
	}
	if got.Source != domain.SourcePanel {
		t.Fatalf("source = %q, want panel", got.Source)
	}
	// Request round-trip.
	if got.Request.Asset != "AAPL" || got.Request.Balance == nil {
		t.Fatalf("request round-trip failed: %+v", got.Request)
	}
	if got.Request.Balance.Mode != domain.AdjustmentModeAbsolute || got.Request.Balance.Value != "100" {
		t.Fatalf("request.balance = %+v", got.Request.Balance)
	}
	if got.Request.RealizedPnl != "" {
		t.Fatalf("request.realized_pnl = %q, want empty", got.Request.RealizedPnl)
	}
	// Outcome round-trip.
	if got.Accepted == nil {
		t.Fatal("accepted outcome is nil after round-trip")
	}
	if got.Accepted.BalanceDelta != "100" || got.Accepted.BalanceResult != "100" {
		t.Fatalf("accepted outcome = %+v", got.Accepted)
	}
	if got.Rejected != nil {
		t.Fatalf("rejected should be nil for accepted adjustment, got %+v", got.Rejected)
	}
}

func TestRecordAccountAdjustmentPersistsRealizedPnlInSameTx(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	stored, err := rs.RecordAccountAdjustment(ctx, fwstore.AccountAdjustmentPersistence{
		RealizedPnl: &fwstore.BalanceRealizedPnlPersistence{
			Account:     "acc-1",
			Asset:       "AAPL",
			RealizedPnl: "-12.50",
		},
		Adjustment: domain.AccountAdjustmentRecord{
			Account: "acc-1",
			Asset:   "AAPL",
			Source:  domain.SourcePanel,
			Request: domain.AdjustmentRequest{
				Asset:       "AAPL",
				RealizedPnl: "-12.50",
			},
			Accepted: &domain.AdjustmentOutcomeAccepted{
				RealizedPnlResult: "-12.50",
			},
		},
		Audit: AuditEntry{
			Action:  domain.AuditActionAdjustment,
			Account: "acc-1",
			Asset:   "AAPL",
			Source:  domain.SourcePanel,
			Detail:  "set balance realized_pnl account acc-1 asset=AAPL realized_pnl=-12.50",
		},
	})
	if err != nil {
		t.Fatalf("RecordAccountAdjustment: %v", err)
	}
	if stored.ExternalID.IsZero() {
		t.Fatal("stored adjustment external id is zero")
	}

	balance, ok, err := rs.GetBalance(ctx, "acc-1", "AAPL")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if !ok || balance.RealizedPnl != "-12.50" {
		t.Fatalf("balance = %+v ok=%v, want realized_pnl -12.50", balance, ok)
	}
	rows, err := rs.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "realized_pnl=-12.50") {
		t.Fatalf("audit rows = %+v, want realized pnl audit", rows)
	}
}

func TestRecordAccountAdjustmentDeletesEmptyRealizedPnlBalance(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)
	seedBalance(t, ctx, rs, "acc-1", "AAPL", "", "", "", "1")

	_, err := rs.RecordAccountAdjustment(ctx, fwstore.AccountAdjustmentPersistence{
		RealizedPnl: &fwstore.BalanceRealizedPnlPersistence{
			Account:     "acc-1",
			Asset:       "AAPL",
			RealizedPnl: "0",
		},
		Adjustment: domain.AccountAdjustmentRecord{
			Account: "acc-1",
			Asset:   "AAPL",
			Source:  domain.SourcePanel,
			Request: domain.AdjustmentRequest{
				Asset:       "AAPL",
				RealizedPnl: "0",
			},
			Accepted: &domain.AdjustmentOutcomeAccepted{
				RealizedPnlResult: "0",
			},
		},
		Audit: AuditEntry{
			Action:  domain.AuditActionAdjustment,
			Account: "acc-1",
			Asset:   "AAPL",
			Source:  domain.SourcePanel,
			Detail:  "set balance realized_pnl account acc-1 asset=AAPL realized_pnl=0",
		},
	})
	if err != nil {
		t.Fatalf("RecordAccountAdjustment: %v", err)
	}
	if _, ok := getBalanceRow(t, ctx, rs, "acc-1", "AAPL"); ok {
		t.Fatal("balance row survived zero realized_pnl adjustment")
	}
}

func TestAdjustmentRejectedOutcome(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	in := domain.AccountAdjustmentRecord{
		Account: "acc-1",
		Asset:   "AAPL",
		Source:  domain.SourcePanel,
		Request: domain.AdjustmentRequest{Asset: "AAPL"},
		Rejected: &domain.AdjustmentOutcomeRejected{
			Code:   "insufficient_funds",
			Reason: "balance too low",
		},
	}
	stored, err := rs.AppendAdjustment(ctx, in)
	if err != nil {
		t.Fatalf("AppendAdjustment (rejected): %v", err)
	}

	list, err := rs.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	got := list[0]
	if got.ExternalID != stored.ExternalID {
		t.Fatalf("external id mismatch")
	}
	if got.Accepted != nil {
		t.Fatalf("accepted should be nil for rejected outcome")
	}
	if got.Rejected == nil || got.Rejected.Code != "insufficient_funds" {
		t.Fatalf("rejected outcome = %+v", got.Rejected)
	}
}

func TestAdjustmentExternalIDOpacity(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	a, err := rs.AppendAdjustment(ctx, sampleAdjustment())
	if err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}
	b, err := rs.AppendAdjustment(ctx, sampleAdjustment())
	if err != nil {
		t.Fatalf("AppendAdjustment (second): %v", err)
	}

	// Two draws must differ.
	if a.ExternalID == b.ExternalID {
		t.Fatal("two AppendAdjustment calls produced the same external id")
	}
	// Both must be 22-char base64url; parse them back.
	for _, id := range []domain.ExternalID{a.ExternalID, b.ExternalID} {
		if _, err := domain.ParseExternalID(id.String()); err != nil {
			t.Fatalf("ParseExternalID(%q): %v", id.String(), err)
		}
	}
}

func TestAdjustmentSourceFilter(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	panelRec := sampleAdjustment()
	panelRec.Source = domain.SourcePanel

	apiRec := sampleAdjustment()
	apiRec.Source = domain.SourceAPI

	if _, err := rs.AppendAdjustment(ctx, panelRec); err != nil {
		t.Fatalf("AppendAdjustment(panel): %v", err)
	}
	if _, err := rs.AppendAdjustment(ctx, apiRec); err != nil {
		t.Fatalf("AppendAdjustment(api): %v", err)
	}

	// Filter to panel.
	panelList, err := rs.ListAdjustments(ctx, "acc-1", domain.SourcePanel, 10)
	if err != nil {
		t.Fatalf("ListAdjustments(panel): %v", err)
	}
	if len(panelList) != 1 || panelList[0].Source != domain.SourcePanel {
		t.Fatalf("panel filter returned %+v", panelList)
	}

	// No source filter returns both.
	all, err := rs.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments(all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered len = %d, want 2", len(all))
	}
}

func TestAdjustmentNonPositiveLimitReturnsEmpty(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	if _, err := rs.AppendAdjustment(ctx, sampleAdjustment()); err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}

	list, err := rs.ListAdjustments(ctx, "acc-1", "", 0)
	if err != nil {
		t.Fatalf("ListAdjustments(n=0): %v", err)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("ListAdjustments(n=0) = %v, want non-nil empty slice", list)
	}
}

func TestAdjustmentPrincipalSetNullAfterPrincipalDelete(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	if _, err := rs.AppendAdjustment(ctx, sampleAdjustment()); err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}

	// Delete the principal; the adjustment's principal_id must become NULL.
	if err := rs.DeletePrincipal(ctx, "operator"); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}

	list, err := rs.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments after principal delete: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("adjustment disappeared after principal delete")
	}
	if list[0].Principal != "" {
		t.Fatalf("principal = %q after delete, want empty (SET NULL)", list[0].Principal)
	}
}

func TestAdjustmentCascadeOnAccountDelete(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	if _, err := rs.AppendAdjustment(ctx, sampleAdjustment()); err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}

	if err := rs.DeleteAccount(ctx, "acc-1", true); err != nil {
		t.Fatalf("DeleteAccount(force): %v", err)
	}

	// The adjustment must be gone (CASCADE).
	r := rs.(*realmStore)
	var n int
	if err := r.rawDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM adjustment`).Scan(&n); err != nil {
		t.Fatalf("count adjustments: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 adjustments after account cascade, got %d", n)
	}
}

func TestAdjustmentBlocksAssetDeleteWithoutForce(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	if _, err := rs.AppendAdjustment(ctx, sampleAdjustment()); err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}

	if err := rs.DeleteAsset(ctx, "AAPL", false); !errors.Is(err, domain.ErrHasDependents) {
		t.Fatalf("DeleteAsset(no force) error = %v, want ErrHasDependents", err)
	}
	list, err := rs.ListAdjustments(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments after blocked delete: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("adjustments after blocked delete = %d, want 1", len(list))
	}
	if err := rs.DeleteAsset(ctx, "AAPL", true); err != nil {
		t.Fatalf("DeleteAsset(force): %v", err)
	}
	list, err = rs.ListAdjustments(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments after force delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("adjustments after force delete = %d, want 0", len(list))
	}
}

func TestAdjustmentUnknownAccountIsInvalid(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	rec := sampleAdjustment()
	rec.Account = "ghost"
	_, err := rs.AppendAdjustment(ctx, rec)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AppendAdjustment(unknown account) error = %v, want ErrInvalid", err)
	}
}

func TestAdjustmentUnknownAssetIsInvalid(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	rec := sampleAdjustment()
	rec.Asset = "GHOST"
	_, err := rs.AppendAdjustment(ctx, rec)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AppendAdjustment(unknown asset) error = %v, want ErrInvalid", err)
	}
}

func TestAdjustmentOrderedNewestFirst(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	// Insert three records; they get strictly increasing timestamps from nowStr().
	var ids []domain.ExternalID
	for i := 0; i < 3; i++ {
		stored, err := rs.AppendAdjustment(ctx, sampleAdjustment())
		if err != nil {
			t.Fatalf("AppendAdjustment %d: %v", i, err)
		}
		ids = append(ids, stored.ExternalID)
	}

	list, err := rs.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	// Newest first: the last inserted id is the first returned.
	if list[0].ExternalID != ids[2] {
		t.Fatalf("first result = %v, want %v (newest)", list[0].ExternalID, ids[2])
	}
}

func TestAdjustmentListRowsAccountFilterPagesFilteredSet(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}

	for _, account := range []domain.AccountID{
		"acc-2", "acc-1", "acc-2", "acc-1", "acc-2", "acc-1",
	} {
		rec := sampleAdjustment()
		rec.Account = account
		if _, err := rs.AppendAdjustment(ctx, rec); err != nil {
			t.Fatalf("AppendAdjustment(%s): %v", account, err)
		}
	}

	first, err := rs.ListAdjustmentRows(ctx, fwstore.AdjustmentListFilter{
		Account: fwstore.ExactTextMatcher("acc-1"),
		Page:    fwstore.PageSpec{Limit: 2},
	})
	if err != nil {
		t.Fatalf("ListAdjustmentRows(first): %v", err)
	}
	if first.Total != 3 || len(first.Rows) != 2 {
		t.Fatalf("first page total/len = %d/%d, want 3/2", first.Total, len(first.Rows))
	}
	for _, row := range first.Rows {
		if row.Account != "acc-1" {
			t.Fatalf("unfiltered row leaked into account page: %+v", first.Rows)
		}
	}

	second, err := rs.ListAdjustmentRows(ctx, fwstore.AdjustmentListFilter{
		Account: fwstore.ExactTextMatcher("acc-1"),
		Page:    fwstore.PageSpec{Limit: 2, Offset: 2},
	})
	if err != nil {
		t.Fatalf("ListAdjustmentRows(second): %v", err)
	}
	if second.Total != 3 || len(second.Rows) != 1 {
		t.Fatalf("second page total/len = %d/%d, want 3/1", second.Total, len(second.Rows))
	}
	if second.Rows[0].Account != "acc-1" {
		t.Fatalf("second page row = %+v", second.Rows[0])
	}
}

// TestAppendAdjustmentSuppliedExternalIDUsedVerbatim verifies that a caller-
// supplied external id is written verbatim: the returned id equals the supplied
// one and the row is addressable by it through a listing.
func TestAppendAdjustmentSuppliedExternalIDUsedVerbatim(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	supplied := domain.ExternalID("caller-adjustment-1")
	rec := sampleAdjustment()
	rec.ExternalID = supplied

	stored, err := rs.AppendAdjustment(ctx, rec)
	if err != nil {
		t.Fatalf("AppendAdjustment(supplied id): %v", err)
	}
	if stored.ExternalID != supplied {
		t.Fatalf("returned id = %q, want supplied %q",
			stored.ExternalID.String(), supplied.String())
	}

	list, err := rs.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d adjustments, want 1", len(list))
	}
	if list[0].ExternalID != supplied {
		t.Fatalf("row external id = %q, want %q",
			list[0].ExternalID.String(), supplied.String())
	}
}

// TestAppendAdjustmentDuplicateSuppliedExternalIDConflicts verifies that a
// second append with the same supplied external id is rejected with
// domain.ErrAlreadyExists and that no second row is written.
func TestAppendAdjustmentDuplicateSuppliedExternalIDConflicts(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)
	r := rs.(*realmStore)

	supplied := domain.ExternalID("caller-adjustment-1")
	first := sampleAdjustment()
	first.ExternalID = supplied
	if _, err := rs.AppendAdjustment(ctx, first); err != nil {
		t.Fatalf("AppendAdjustment(first): %v", err)
	}

	second := sampleAdjustment()
	second.ExternalID = supplied
	_, err := rs.AppendAdjustment(ctx, second)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("AppendAdjustment(dup id) = %v, want ErrAlreadyExists", err)
	}
	if msg := err.Error(); !strings.Contains(msg, supplied.String()) {
		t.Fatalf("conflict error %q does not name the external id %q",
			msg, supplied.String())
	}

	var n int
	if err := r.rawDB().QueryRowContext(
		ctx, `SELECT COUNT(*) FROM adjustment`,
	).Scan(&n); err != nil {
		t.Fatalf("count adjustments: %v", err)
	}
	if n != 1 {
		t.Fatalf("adjustments row count = %d, want 1 (no second row)", n)
	}
}

// TestAppendAdjustmentGeneratesExternalIDWhenAbsent verifies that a zero
// supplied id is replaced by a freshly generated 22-char external id.
func TestAppendAdjustmentGeneratesExternalIDWhenAbsent(t *testing.T) {
	ctx, rs := seedAdjustmentFixtures(t)

	rec := sampleAdjustment()
	if !rec.ExternalID.IsZero() {
		t.Fatal("sample adjustment must carry a zero external id")
	}
	stored, err := rs.AppendAdjustment(ctx, rec)
	if err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}
	if stored.ExternalID.IsZero() {
		t.Fatal("AppendAdjustment returned a zero external id")
	}
	if got := len(stored.ExternalID.String()); got != domain.ExternalIDStringLen {
		t.Fatalf("generated id len = %d, want %d", got, domain.ExternalIDStringLen)
	}
}
