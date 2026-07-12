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

package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestLocalNode_ApplyAdjustmentHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "100",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "supplied-adj-id")
	req := domain.AdjustmentRequest{
		Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "100",
		},
	}
	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), supplied, req, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.ExternalID != supplied {
		t.Fatalf("record id = %q, want supplied %q", rec.ExternalID, supplied)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].ExternalID != supplied {
		t.Fatalf("stored adjustments = %+v, want one under supplied id", records)
	}

	// A second adjustment reusing the supplied id conflicts.
	_, err = n.ApplyAdjustment(ctx, testKey("acc-1"), supplied, req, testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

func TestLocalNode_ApplyAdjustmentNoChangeDoesNotPersist(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentNoop = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "0"},
		}, testCaller)
	if !errors.Is(err, domain.ErrNoChange) {
		t.Fatalf("ApplyAdjustment error = %v, want ErrNoChange", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want one", eng.adjustmentCalls)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("stored adjustments = %+v, want none", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("audit rows = %+v, want none", rows)
	}
}

func TestLocalNode_SetBalanceRealizedPnlPersistsAdjustmentRecord(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	balance, err := n.SetBalanceRealizedPnl(
		ctx,
		testKey("acc-1"),
		"USD",
		"-12.50",
		testCaller,
	)
	if err != nil {
		t.Fatalf("SetBalanceRealizedPnl: %v", err)
	}
	if balance.RealizedPnl != "-12.50" {
		t.Fatalf("realized pnl = %q, want -12.50", balance.RealizedPnl)
	}
	if len(eng.adjustmentCalls) != 0 {
		t.Fatalf("engine adjustment calls = %+v, want none", eng.adjustmentCalls)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 ||
		records[0].Request.RealizedPnl != "-12.50" ||
		records[0].Accepted == nil {
		t.Fatalf("stored adjustments = %+v, want realized pnl adjustment", records)
	}
	// The realized-pnl-only outcome carries the persisted result and its delta
	// from the previous (absent, hence zero) stored value.
	if records[0].Accepted.RealizedPnlResult != "-12.50" ||
		records[0].Accepted.RealizedPnlDelta != "-12.5" {
		t.Fatalf("accepted outcome = %+v, want result -12.50 delta -12.5",
			records[0].Accepted)
	}
	stored, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if !ok || stored.RealizedPnl != "-12.50" {
		t.Fatalf("stored balance = %+v ok=%v, want realized pnl -12.50", stored, ok)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 ||
		!strings.Contains(rows[0].Detail, "realized_pnl=-12.50") {
		t.Fatalf("audit rows = %+v, want realized pnl detail", rows)
	}
}

func TestLocalNode_RealizedPnlPersistenceUsesOneWriterPerRequest(t *testing.T) {
	t.Parallel()
	var probe *accountAdjustmentRecordProbeRealm
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		probe = &accountAdjustmentRecordProbeRealm{RealmStore: r}
		return probe
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult:     "10",
		RealizedPnlDelta:  "99",
		RealizedPnlResult: "99",
	}
	n := newTestNodeWithStore(t, st, eng)
	seedTestAccount(t, n.realm, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:       "USD",
			RealizedPnl: "-12.50",
			Balance:     &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "10"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	// The public outcome reflects the persisted realized P&L (the supplied
	// -12.50), not the engine's own 99 cross-check absolute. The delta is the
	// signed change from the previous (absent, hence zero) stored value.
	if rec.Accepted == nil ||
		rec.Accepted.RealizedPnlResult != "-12.50" ||
		rec.Accepted.RealizedPnlDelta != "-12.5" {
		t.Fatalf("accepted outcome = %+v, want result -12.50 delta -12.5", rec.Accepted)
	}
	if len(probe.records) != 1 {
		t.Fatalf("recorded persistence calls = %d, want 1", len(probe.records))
	}
	combined := probe.records[0]
	if combined.RealizedPnl != nil {
		t.Fatalf("combined adjustment realized-pnl carrier = %+v, want nil", combined.RealizedPnl)
	}
	if combined.UpsertBalance == nil || combined.UpsertBalance.RealizedPnl != "-12.50" {
		t.Fatalf("combined adjustment upsert = %+v, want realized_pnl -12.50", combined.UpsertBalance)
	}
	stored, ok, err := n.realm.GetBalance(ctx, "acc-1", "USD")
	if err != nil {
		t.Fatalf("GetBalance(combined): %v", err)
	}
	if !ok || stored.RealizedPnl != "-12.50" {
		t.Fatalf("combined stored balance = %+v ok=%v, want realized_pnl -12.50", stored, ok)
	}

	balance, err := n.SetBalanceRealizedPnl(ctx, testKey("acc-1"), "JPY", "5.25", testCaller)
	if err != nil {
		t.Fatalf("SetBalanceRealizedPnl(create): %v", err)
	}
	if balance.RealizedPnl != "5.25" {
		t.Fatalf("created realized pnl = %q, want 5.25", balance.RealizedPnl)
	}
	created, ok, err := n.realm.GetBalance(ctx, "acc-1", "JPY")
	if err != nil {
		t.Fatalf("GetBalance(created): %v", err)
	}
	if !ok || created.RealizedPnl != "5.25" {
		t.Fatalf("created balance = %+v ok=%v, want realized_pnl 5.25", created, ok)
	}

	_, err = n.SetBalanceRealizedPnl(ctx, testKey("acc-1"), "JPY", "0", testCaller)
	if err != nil {
		t.Fatalf("SetBalanceRealizedPnl(delete): %v", err)
	}
	if _, ok, err := n.realm.GetBalance(ctx, "acc-1", "JPY"); err != nil || ok {
		t.Fatalf("GetBalance(deleted) = ok %v err %v, want no balance row", ok, err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want only the combined adjustment", eng.adjustmentCalls)
	}
	if len(probe.records) != 3 {
		t.Fatalf("recorded persistence calls = %d, want combined/create/delete", len(probe.records))
	}
	if probe.records[1].RealizedPnl == nil || probe.records[1].UpsertBalance != nil {
		t.Fatalf("realized-only create persistence = %+v, want realized-pnl carrier only", probe.records[1])
	}
	if probe.records[2].RealizedPnl == nil || probe.records[2].UpsertBalance != nil {
		t.Fatalf("realized-only delete persistence = %+v, want realized-pnl carrier only", probe.records[2])
	}
}

// TestLocalNode_ApplyAdjustmentNoEngineChangeStillPersistsRealizedPnl asserts
// that a no-engine-change adjustment carrying a realized P&L still persists it
// as a realized-pnl-only adjustment, while a pure no-op returns ErrNoChange.
func TestLocalNode_ApplyAdjustmentNoEngineChangeStillPersistsRealizedPnl(t *testing.T) {
	t.Parallel()
	var probe *accountAdjustmentRecordProbeRealm
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		probe = &accountAdjustmentRecordProbeRealm{RealmStore: r}
		return probe
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	eng.adjustmentNoop = true
	n := newTestNodeWithStore(t, st, eng)
	seedTestAccount(t, n.realm, "acc-1")

	// An engine field that nets no change (absolute Available equal to the
	// seeded zero balance) plus a realized P&L must persist the realized P&L.
	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:       "USD",
			RealizedPnl: "-12.50",
			Balance:     &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "0"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment(no engine change + realized pnl) = %v, want no error", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted realized pnl adjustment", rec)
	}
	if rec.Request.RealizedPnl != "-12.50" {
		t.Fatalf("record request realized pnl = %q, want -12.50", rec.Request.RealizedPnl)
	}
	if rec.Accepted.RealizedPnlResult != "-12.50" ||
		rec.Accepted.RealizedPnlDelta != "-12.5" {
		t.Fatalf("accepted outcome = %+v, want result -12.50 delta -12.5", rec.Accepted)
	}
	stored, ok, err := n.realm.GetBalance(ctx, "acc-1", "USD")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if !ok || stored.RealizedPnl != "-12.50" {
		t.Fatalf("stored balance = %+v ok=%v, want realized_pnl -12.50", stored, ok)
	}
	realizedWriters := 0
	for _, p := range probe.records {
		if p.RealizedPnl == nil {
			continue
		}
		realizedWriters++
		if p.UpsertBalance != nil {
			t.Fatalf("realized pnl persistence = %+v, want realized-pnl carrier only", p)
		}
	}
	if realizedWriters != 1 {
		t.Fatalf("persistence calls carrying realized pnl = %d, want exactly 1", realizedWriters)
	}

	// A pure no-op (engine nets no change, no realized P&L) still returns
	// ErrNoChange and records nothing further.
	before := len(probe.records)
	_, err = n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "0"},
		}, testCaller)
	if !errors.Is(err, domain.ErrNoChange) {
		t.Fatalf("pure no-op error = %v, want ErrNoChange", err)
	}
	if len(probe.records) != before {
		t.Fatalf("pure no-op recorded %d extra persistence calls, want 0",
			len(probe.records)-before)
	}
}

func TestLocalNode_ApplyAdjustmentStoreFailureFatalsWithoutCompensation(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record adjustment failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failAccountAdjustmentRecordRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceDelta:  "5",
		BalanceResult: "15",
	}
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	seedTestAccount(t, n.realm, "acc-1")
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10", AverageEntryPrice: "42",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:             "USD",
			AverageEntryPrice: "99",
			Balance:           &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "5"},
		}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("ApplyAdjustment error = %v, want store failure", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want only the requested apply", eng.adjustmentCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine adjustment persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record account adjustment"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record adjustment failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestLocalNode_ApplyAdjustmentGeneratesExternalIDWhenAbsent covers the
// absent-id path: a zero id lets the store mint one.
func TestLocalNode_ApplyAdjustmentGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""), domain.AdjustmentRequest{
		Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "100",
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.ExternalID.IsZero() {
		t.Fatalf("record id is zero, want a generated id")
	}
}

// TestLocalNode_ApplyAdjustmentRejectedIsRecorded checks that a policy reject
// (e.g. a SpotFunds P&L kill-switch) on an existing account persists the rejected attempt
// to the adjustment history and the audit log, leaving balances untouched.
func TestLocalNode_ApplyAdjustmentRejectedIsRecorded(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "spot_funds_pnl_bounds_kill_switch",
		Policy: "spot_funds_pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}

	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance after reject = ok %v err %v, want no balance row", ok, err)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAccount checks that an
// adjustment to a non-existent account auto-creates it in the default group (no
// group assigned), so the engine resolves it and applies the adjustment, and
// the creation is audited.
func TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	// Enforce the resolver so an adjustment to an unknown account would error
	// unless the auto-create runs first and rebuilds the engine.
	eng.enforceResolver = true
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	rec, err := n.ApplyAdjustment(ctx, testKey("fresh"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}

	account, ok, err := st.GetAccount(ctx, "fresh")
	if err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	if account.GroupCode != "" {
		t.Fatalf("auto-created account group = %q, want default (empty)", account.GroupCode)
	}

	records, err := st.ListAdjustments(ctx, "fresh", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Accepted == nil {
		t.Fatalf("stored adjustments = %+v, want one accepted record", records)
	}

	createRows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAccount},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create): %v", err)
	}
	if len(createRows) != 1 {
		t.Fatalf("create-account audit rows = %+v, want one", createRows)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreateThenReject checks that a rejected
// adjustment to a non-existent account still auto-creates the account and
// records the rejected attempt in both history and audit.
func TestLocalNode_ApplyAdjustmentAutoCreateThenReject(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "spot_funds_pnl_bounds_kill_switch",
		Policy: "spot_funds_pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	rec, err := n.ApplyAdjustment(ctx, testKey("fresh"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	if _, ok, err := st.GetAccount(ctx, "fresh"); err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	records, err := st.ListAdjustments(ctx, "fresh", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}
}

// TestLocalNode_ApplyAdjustmentRejectsMalformedAccountID checks that a malformed
// account id is rejected with domain.ErrInvalid and no account is auto-created.
func TestLocalNode_ApplyAdjustmentRejectsMalformedAccountID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	bad := domain.AccountID("bad-id ")
	_, err := n.ApplyAdjustment(ctx, testKey(bad), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(malformed) = %v, want ErrInvalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, bad); err != nil || ok {
		t.Fatalf("GetAccount(malformed) = ok %v err %v, want absent", ok, err)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAsset checks that an adjustment
// referencing a non-existent asset auto-creates it in the auto-created class, so
// the engine resolves it and applies the adjustment, and the creation is
// audited with the originating operation.
func TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "GOLD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.Title != "" || asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset = %+v, want empty title and %q class", asset,
			autoCreatedAssetClassCode)
	}

	createRows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(createRows) != 1 ||
		createRows[0].Asset != "GOLD" ||
		!strings.Contains(createRows[0].Detail, "auto-created asset GOLD by adjustment") {
		t.Fatalf("create-asset audit rows = %+v, want one for GOLD", createRows)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreateAssetThenReject checks that a rejected
// adjustment to a non-existent asset still auto-creates the asset and records
// the rejected attempt in both history and audit.
func TestLocalNode_ApplyAdjustmentAutoCreateAssetThenReject(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "spot_funds_pnl_bounds_kill_switch",
		Policy: "spot_funds_pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "GOLD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}
}

// TestLocalNode_ApplyAdjustmentRejectsMalformedAsset checks that a malformed
// asset id is rejected with domain.ErrInvalid and no asset is auto-created.
func TestLocalNode_ApplyAdjustmentRejectsMalformedAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	bad := "US D"
	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   bad,
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(malformed asset) = %v, want ErrInvalid", err)
	}
	if _, ok, err := st.GetAsset(ctx, bad); err != nil || ok {
		t.Fatalf("GetAsset(malformed) = ok %v err %v, want absent", ok, err)
	}
}

// TestLocalNode_ApplyAdjustmentAcceptedOverridesEngineRealizedPnl checks that
// when the request supplies a realized P&L, the accepted outcome reflects the
// persisted override and its delta from the previous stored value, not the
// engine's own cross-check absolute.
func TestLocalNode_ApplyAdjustmentAcceptedOverridesEngineRealizedPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult:     "10",
		RealizedPnlDelta:  "99",
		RealizedPnlResult: "99",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "5", RealizedPnl: "-2",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:       "USD",
			RealizedPnl: "-12.5",
			Balance:     &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "10"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}
	if rec.Accepted.RealizedPnlResult != "-12.5" {
		t.Fatalf("realized pnl result = %q, want persisted -12.5 not engine 99",
			rec.Accepted.RealizedPnlResult)
	}
	if rec.Accepted.RealizedPnlDelta != "-10.5" {
		t.Fatalf("realized pnl delta = %q, want -10.5 (-12.5 minus prev -2)",
			rec.Accepted.RealizedPnlDelta)
	}
	stored, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: ok=%v err=%v", ok, err)
	}
	if stored.RealizedPnl != "-12.5" || stored.Available != "10" {
		t.Fatalf("stored balance = %+v, want realized_pnl -12.5 available 10", stored)
	}
}

// TestLocalNode_ApplyAdjustmentAcceptedReflectsAccumulatedRealizedPnl checks
// that with no supplied realized P&L the accepted outcome reports Officer's
// accumulated stored value (previous plus engine delta), not the engine's own
// cross-check absolute, and carries the engine delta.
func TestLocalNode_ApplyAdjustmentAcceptedReflectsAccumulatedRealizedPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult:     "10",
		RealizedPnlDelta:  "3",
		RealizedPnlResult: "99",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "5", RealizedPnl: "7",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "10"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}
	if rec.Accepted.RealizedPnlResult != "10" {
		t.Fatalf("realized pnl result = %q, want accumulated 10 (prev 7 + delta 3) not engine 99",
			rec.Accepted.RealizedPnlResult)
	}
	if rec.Accepted.RealizedPnlDelta != "3" {
		t.Fatalf("realized pnl delta = %q, want engine delta 3", rec.Accepted.RealizedPnlDelta)
	}
	stored, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: ok=%v err=%v", ok, err)
	}
	if stored.RealizedPnl != "10" {
		t.Fatalf("stored realized pnl = %q, want accumulated 10", stored.RealizedPnl)
	}
}

// TestLocalNode_ApplyAdjustmentRejectsEmptyAssetOnRealizedPnlOnly checks that a
// realized-pnl-only adjustment (which bypasses the engine seam that would reject
// the asset) rejects an empty asset with domain.ErrInvalid and persists nothing,
// preserving the (account, asset) balance-row invariant.
func TestLocalNode_ApplyAdjustmentRejectsEmptyAssetOnRealizedPnlOnly(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{Asset: "", RealizedPnl: "10"}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(empty asset) = %v, want ErrInvalid", err)
	}
	if len(eng.adjustmentCalls) != 0 {
		t.Fatalf("engine adjustment calls = %+v, want none", eng.adjustmentCalls)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("stored adjustments = %+v, want none", records)
	}
	balances, err := st.ListBalances(ctx, "acc-1", "")
	if err != nil {
		t.Fatalf("ListBalances: %v", err)
	}
	if len(balances) != 0 {
		t.Fatalf("balances = %+v, want no row for empty asset", balances)
	}
}
