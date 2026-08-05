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
	"go.openpit.dev/officer/framework/engine"
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
	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), supplied, req, domain.MissingAccountCreate, testCaller)
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
	_, err = n.ApplyAdjustment(ctx, testKey("acc-1"), supplied, req, domain.MissingAccountCreate, testCaller)
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
		}, domain.MissingAccountCreate, testCaller)
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

func TestLocalNode_ApplyAdjustmentRejectsAverageOnlyForMissingBalance(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{AverageEntryPrice: "1"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{Asset: "USD", AverageEntryPrice: "1"}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, domain.ErrNoChange) {
		t.Fatalf("ApplyAdjustment error = %v, want ErrNoChange", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want one validation call", eng.adjustmentCalls)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("stored adjustments = %+v, want none", records)
	}
}

func TestLocalNode_ApplyAdjustmentDoesNotInventRealizedPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "1", AverageEntryPrice: "99",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingInitialPnl,
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset: "BTC", AverageEntryPrice: "99",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "1"},
		}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if len(eng.adjustmentCalls) != 1 || eng.adjustmentCalls[0].req.RealizedPnl != "" {
		t.Fatalf("engine adjustment calls = %+v, want caller's empty realized PnL", eng.adjustmentCalls)
	}
	stored, ok, err := st.GetBalance(ctx, "acc-1", "BTC")
	if err != nil || !ok {
		t.Fatalf("GetBalance: ok=%v err=%v", ok, err)
	}
	if stored.RealizedPnlHaltReason != domain.PnlHaltReasonMissingInitialPnl {
		t.Fatalf("stored halt = %q, want missing_initial_pnl", stored.RealizedPnlHaltReason)
	}
	if stored.RealizedPnl != "" {
		t.Fatalf("stored realized pnl = %q, want unset", stored.RealizedPnl)
	}
}

func TestLocalNode_AdjustedBalanceKeepsRealizedPnlState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		previous   *domain.Balance
		outcome    domain.AdjustmentOutcomeAccepted
		wantPnl    string
		wantReason domain.PnlHaltReason
	}{
		{
			name:    "new balance keeps pnl unset",
			outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "1"},
		},
		{
			name: "explicit zero is authoritative",
			outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "1", RealizedPnlResult: "0",
			},
			wantPnl: "0",
		},
		{
			name: "omission preserves numeric pnl",
			previous: &domain.Balance{
				Available: "1", RealizedPnl: "7.5",
			},
			outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "2"},
			wantPnl: "7.5",
		},
		{
			name: "omission preserves pnl halt",
			previous: &domain.Balance{
				Available:             "1",
				RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
			},
			outcome:    domain.AdjustmentOutcomeAccepted{BalanceResult: "2"},
			wantReason: domain.PnlHaltReasonMissingFx,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, st := newTestNode(t, eng)
			ctx := context.Background()
			seedTestAccount(t, st, "acc-1")
			if test.previous != nil {
				previous := *test.previous
				previous.Account = "acc-1"
				previous.Asset = "USD"
				if err := st.UpsertBalance(ctx, previous); err != nil {
					t.Fatalf("UpsertBalance: %v", err)
				}
			}

			balance, err := n.adjustedBalance(ctx, testKey("acc-1"), "USD", test.outcome)
			if err != nil {
				t.Fatalf("adjustedBalance: %v", err)
			}
			if balance.RealizedPnl != test.wantPnl ||
				balance.RealizedPnlHaltReason != test.wantReason {
				t.Fatalf(
					"realized pnl state = %q/%q, want %q/%q",
					balance.RealizedPnl,
					balance.RealizedPnlHaltReason,
					test.wantPnl,
					test.wantReason,
				)
			}
		})
	}
}

// balanceReadLaneProbeRealm records whether balance reads happen inside the
// account lane. Adjustment persistence still reads the previous balance there,
// even though Officer no longer derives any hidden input from it.
type balanceReadLaneProbeRealm struct {
	store.RealmStore
	eng    *fakeEngine
	inLane []bool
}

func (s *balanceReadLaneProbeRealm) GetBalance(
	ctx context.Context, account domain.AccountID, asset string,
) (domain.Balance, bool, error) {
	s.inLane = append(s.inLane, s.eng.insideLane())
	return s.RealmStore.GetBalance(ctx, account, asset)
}

// TestLocalNode_ApplyAdjustmentDoesNotReplayStoredRealizedPnl asserts that a
// stored value is not injected when the caller only changes average entry price.
func TestLocalNode_ApplyAdjustmentDoesNotReplayStoredRealizedPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "1", AverageEntryPrice: "101",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingInitialPnl,
	}
	var probe *balanceReadLaneProbeRealm
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		probe = &balanceReadLaneProbeRealm{RealmStore: r, eng: eng}
		return probe
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	n := newTestNodeWithStore(t, st, eng)
	seedTestAccount(t, n.realm, "acc-1")
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "1",
		RealizedPnl: "12.5", AverageEntryPrice: "100",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	// Average entry price is present but realized P&L is deliberately absent.
	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset: "USD", AverageEntryPrice: "101",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "1"},
		}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}

	if len(probe.inLane) == 0 {
		t.Fatal("no balance read recorded for adjustment persistence")
	}
	for i, inLane := range probe.inLane {
		if !inLane {
			t.Fatalf("balance read %d of %d ran outside the account lane, want every "+
				"read serialized by it", i, len(probe.inLane))
		}
	}
	if len(eng.adjustmentCalls) != 1 || eng.adjustmentCalls[0].req.RealizedPnl != "" {
		t.Fatalf("engine adjustment calls = %+v, want caller's empty realized PnL",
			eng.adjustmentCalls)
	}
	if rec.Request.RealizedPnl != "" {
		t.Fatalf("returned record request realized pnl = %q, want the caller's original (empty)",
			rec.Request.RealizedPnl)
	}

	records, err := n.realm.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("stored adjustments = %+v, want one", records)
	}
	if records[0].Request.RealizedPnl != "" {
		t.Fatalf("persisted request realized pnl = %q, want the caller's original (empty)",
			records[0].Request.RealizedPnl)
	}
	if records[0].Request.AverageEntryPrice != "101" {
		t.Fatalf("persisted request average entry price = %q, want the submitted 101",
			records[0].Request.AverageEntryPrice)
	}
	if records[0].Accepted == nil ||
		records[0].Accepted.RealizedPnlHaltReason != domain.PnlHaltReasonMissingInitialPnl {
		t.Fatalf("accepted outcome = %+v, want SDK missing_initial_pnl halt", records[0].Accepted)
	}
}

func TestLocalNode_SetBalanceRealizedPnlPersistsAdjustmentRecord(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		RealizedPnlDelta:  "-12.5",
		RealizedPnlResult: "-12.50",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	balance, err := n.SetBalanceRealizedPnl(
		ctx,
		testKey("acc-1"),
		"USD",
		"-12.50",
		domain.MissingAccountCreate, testCaller,
	)
	if err != nil {
		t.Fatalf("SetBalanceRealizedPnl: %v", err)
	}
	if balance.RealizedPnl != "-12.50" {
		t.Fatalf("realized pnl = %q, want -12.50", balance.RealizedPnl)
	}
	if len(eng.adjustmentCalls) != 1 || eng.adjustmentCalls[0].req.RealizedPnl != "-12.50" {
		t.Fatalf("engine adjustment calls = %+v, want realized pnl forwarded once", eng.adjustmentCalls)
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
	// The recorded values are the engine outcome, without Officer recomputing it.
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
		!strings.Contains(rows[0].Detail, "adjustment account acc-1 asset=USD accepted") {
		t.Fatalf("audit rows = %+v, want engine adjustment detail", rows)
	}
}

func TestLocalNode_SetBalanceRealizedPnlKeepsAccountCurrencyAfterDeletingFinalRow(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		RealizedPnlDelta:  "0",
		RealizedPnlResult: "0",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := st.CreateAccount(ctx, domain.Account{
		Code:     "acc-1",
		Currency: "USD",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	balance, err := n.SetBalanceRealizedPnl(
		ctx, testKey("acc-1"), "USD", "0", domain.MissingAccountCreate, testCaller,
	)
	if err != nil {
		t.Fatalf("SetBalanceRealizedPnl: %v", err)
	}
	if balance.RealizedPnl != "0" || balance.AccountCurrency != "USD" {
		t.Fatalf("balance = %+v, want zero realized PnL in USD", balance)
	}
	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance(deleted) = ok %v err %v, want no balance row", ok, err)
	}
}

func TestLocalNode_SetBalanceRealizedPnlClearsOnlyPositionHalt(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		RealizedPnlDelta:  "6",
		RealizedPnlResult: "5",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := st.CreateAccount(ctx, domain.Account{
		Code:          "acc-1",
		PnlHaltReason: domain.PnlHaltReasonArithmeticOverflow,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account:               "acc-1",
		Asset:                 "USD",
		RealizedPnl:           "-1",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	balance, err := n.SetBalanceRealizedPnl(
		ctx,
		testKey("acc-1"),
		"USD",
		"5",
		domain.MissingAccountCreate, testCaller,
	)
	if err != nil {
		t.Fatalf("SetBalanceRealizedPnl: %v", err)
	}
	if balance.RealizedPnlHaltReason != "" {
		t.Fatalf(
			"balance halt = %q, want cleared",
			balance.RealizedPnlHaltReason,
		)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.PnlHaltReason != domain.PnlHaltReasonArithmeticOverflow {
		t.Fatalf(
			"account halt = %q, want preserved arithmetic_overflow",
			account.PnlHaltReason,
		)
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
		AverageEntryPrice: "88",
	}
	n := newTestNodeWithStore(t, st, eng)
	seedTestAccount(t, n.realm, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:       "USD",
			RealizedPnl: "-12.50",
			Balance:     &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "10"},
		}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	// The public outcome and balance reflect the engine values verbatim, even
	// when they differ from the submitted value.
	if rec.Accepted == nil ||
		rec.Accepted.RealizedPnlResult != "99" ||
		rec.Accepted.RealizedPnlDelta != "99" {
		t.Fatalf("accepted outcome = %+v, want engine result/delta 99", rec.Accepted)
	}
	if len(probe.records) != 1 {
		t.Fatalf("recorded persistence calls = %d, want 1", len(probe.records))
	}
	combined := probe.records[0]
	if combined.UpsertBalance == nil || combined.UpsertBalance.RealizedPnl != "99" {
		t.Fatalf("combined adjustment upsert = %+v, want engine realized_pnl 99", combined.UpsertBalance)
	}
	stored, ok, err := n.realm.GetBalance(ctx, "acc-1", "USD")
	if err != nil {
		t.Fatalf("GetBalance(combined): %v", err)
	}
	if !ok || stored.RealizedPnl != "99" {
		t.Fatalf("combined stored balance = %+v ok=%v, want engine realized_pnl 99", stored, ok)
	}

	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		RealizedPnlDelta:  "5.25",
		RealizedPnlResult: "5.25",
	}
	balance, err := n.SetBalanceRealizedPnl(ctx, testKey("acc-1"), "JPY", "5.25", domain.MissingAccountCreate, testCaller)
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

	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		RealizedPnlDelta:  "-5.25",
		RealizedPnlResult: "0",
	}
	balance, err = n.SetBalanceRealizedPnl(ctx, testKey("acc-1"), "JPY", "0", domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SetBalanceRealizedPnl(zero): %v", err)
	}
	if balance.RealizedPnl != "0" {
		t.Fatalf("zero realized pnl = %q, want 0", balance.RealizedPnl)
	}
	if _, ok, err := n.realm.GetBalance(ctx, "acc-1", "JPY"); err != nil || ok {
		t.Fatalf("GetBalance(deleted) = ok %v err %v, want no balance row", ok, err)
	}
	if len(eng.adjustmentCalls) != 3 {
		t.Fatalf("engine adjustment calls = %+v, want all three adjustments", eng.adjustmentCalls)
	}
	if len(probe.records) != 3 {
		t.Fatalf("recorded persistence calls = %d, want combined/create/delete", len(probe.records))
	}
	if probe.records[1].UpsertBalance == nil || probe.records[1].UpsertBalance.RealizedPnl != "5.25" {
		t.Fatalf("realized-only create persistence = %+v, want engine balance snapshot", probe.records[1])
	}
	if probe.records[2].DeleteBalance == nil || probe.records[2].UpsertBalance != nil {
		t.Fatalf("realized-only delete persistence = %+v, want engine-driven delete", probe.records[2])
	}
}

func TestBalanceIsEmptyRequiresEveryEconomicValueToBeZero(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		balance domain.Balance
		want    bool
	}{
		{name: "unset", balance: domain.Balance{}, want: true},
		{name: "exact zero pnl", balance: domain.Balance{RealizedPnl: "0"}, want: true},
		{name: "missing fx halt", balance: domain.Balance{
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
		}},
		{name: "missing account currency halt", balance: domain.Balance{
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
		}},
		{name: "missing initial pnl halt", balance: domain.Balance{
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingInitialPnl,
		}},
		{name: "missing cost basis halt", balance: domain.Balance{
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
		}},
		{name: "arithmetic overflow halt", balance: domain.Balance{
			RealizedPnlHaltReason: domain.PnlHaltReasonArithmeticOverflow,
		}},
		{name: "available", balance: domain.Balance{Available: "1"}},
		{name: "held", balance: domain.Balance{Held: "-1"}},
		{name: "incoming", balance: domain.Balance{Incoming: "0.1"}},
		{name: "realized pnl", balance: domain.Balance{RealizedPnl: "-0.1"}},
		{
			name: "halt does not hide available",
			balance: domain.Balance{
				Available:             "1",
				RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
			},
		},
		{
			name:    "average entry price",
			balance: domain.Balance{AverageEntryPrice: "10"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := balanceIsEmpty(test.balance); got != test.want {
				t.Fatalf("balanceIsEmpty(%+v) = %v, want %v", test.balance, got, test.want)
			}
		})
	}
}

// TestLocalNode_ApplyAdjustmentNoEngineChangePersistsNothing asserts that an
// absent engine outcome is authoritative even when the request carried PnL.
func TestLocalNode_ApplyAdjustmentNoEngineChangePersistsNothing(t *testing.T) {
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

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:       "USD",
			RealizedPnl: "-12.50",
			Balance:     &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "0"},
		}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, domain.ErrNoChange) {
		t.Fatalf("ApplyAdjustment(no engine outcome) = %v, want ErrNoChange", err)
	}
	if len(probe.records) != 0 {
		t.Fatalf("no-outcome adjustment persisted %+v, want nothing", probe.records)
	}

	// A pure no-op (engine nets no change, no realized P&L) still returns
	// ErrNoChange and records nothing further.
	before := len(probe.records)
	_, err = n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "0"},
		}, domain.MissingAccountCreate, testCaller)
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
		}, domain.MissingAccountCreate, testCaller)
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
	}, domain.MissingAccountCreate, testCaller)
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
		}, domain.MissingAccountCreate, testCaller)
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
	// unless the auto-create publishes the account before the lane starts.
	eng.enforceResolver = true
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	rec, err := n.ApplyAdjustment(ctx, testKey("fresh"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "100"},
		}, domain.MissingAccountCreate, testCaller)
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
		}, domain.MissingAccountCreate, testCaller)
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
			Asset:   "GOLD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(malformed) = %v, want ErrInvalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, bad); err != nil || ok {
		t.Fatalf("GetAccount(malformed) = ok %v err %v, want absent", ok, err)
	}
	if _, ok, err := st.GetAsset(ctx, "GOLD"); err != nil || ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want no asset side effect", ok, err)
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
		}, domain.MissingAccountCreate, testCaller)
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
		}, domain.MissingAccountCreate, testCaller)
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
		}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(malformed asset) = %v, want ErrInvalid", err)
	}
	if _, ok, err := st.GetAsset(ctx, bad); err != nil || ok {
		t.Fatalf("GetAsset(malformed) = ok %v err %v, want absent", ok, err)
	}
}

// TestLocalNode_ApplyAdjustmentPersistsEngineRealizedPnl checks that Officer
// never replaces the engine outcome with the requested value.
func TestLocalNode_ApplyAdjustmentPersistsEngineRealizedPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult:     "10",
		RealizedPnlDelta:  "99",
		RealizedPnlResult: "99",
		AverageEntryPrice: "88",
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
			Asset:             "USD",
			AverageEntryPrice: "77",
			RealizedPnl:       "-12.5",
			Balance:           &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "10"},
		}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}
	if rec.Accepted.RealizedPnlResult != "99" {
		t.Fatalf("realized pnl result = %q, want engine 99",
			rec.Accepted.RealizedPnlResult)
	}
	if rec.Accepted.RealizedPnlDelta != "99" {
		t.Fatalf("realized pnl delta = %q, want engine 99",
			rec.Accepted.RealizedPnlDelta)
	}
	stored, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: ok=%v err=%v", ok, err)
	}
	if stored.RealizedPnl != "99" || stored.Available != "10" ||
		stored.AverageEntryPrice != "88" {
		t.Fatalf("stored balance = %+v, want engine realized_pnl 99 average 88 available 10", stored)
	}
}

// TestLocalNode_ApplyAdjustmentPersistsEngineRealizedPnlAbsolute checks that
// Officer stores the engine absolute instead of accumulating its delta.
func TestLocalNode_ApplyAdjustmentPersistsEngineRealizedPnlAbsolute(t *testing.T) {
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
		}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}
	if rec.Accepted.RealizedPnlResult != "99" {
		t.Fatalf("realized pnl result = %q, want engine 99",
			rec.Accepted.RealizedPnlResult)
	}
	if rec.Accepted.RealizedPnlDelta != "3" {
		t.Fatalf("realized pnl delta = %q, want engine delta 3", rec.Accepted.RealizedPnlDelta)
	}
	stored, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: ok=%v err=%v", ok, err)
	}
	if stored.RealizedPnl != "99" {
		t.Fatalf("stored realized pnl = %q, want engine 99", stored.RealizedPnl)
	}
}

// TestLocalNode_ApplyAdjustmentRejectsEmptyAssetOnRealizedPnlOnly checks that a
// realized-pnl-only adjustment rejects an empty asset with domain.ErrInvalid
// and persists nothing, preserving the balance-row key invariant.
func TestLocalNode_ApplyAdjustmentRejectsEmptyAssetOnRealizedPnlOnly(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{Asset: "", RealizedPnl: "10"}, domain.MissingAccountCreate, testCaller)
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

// An out-of-bounds force-set trips the account kill-switch in the engine while
// the adjustment commits. The store must show the account blocked when the call
// returns; deferring to a later fill would leave it reading as tradable.
func TestLocalNode_ApplyAdjustmentMirrorsEngineAccountBlock(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentBatchResults = []engine.AdjustmentResult{{
		Accepted: &domain.AdjustmentOutcomeAccepted{RealizedPnlResult: "-500"},
		AccountBlocks: []domain.AccountBlock{{
			Account: "acc-1",
			Policy:  domain.PolicySpotFundsPnlBoundsKillSwitch,
			Code:    "pnl_bounds",
			Reason:  "realized pnl below bound",
			Details: "bound=-100",
		}},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	if _, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{Asset: "USD", RealizedPnl: "-500"},
		domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}

	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount = ok %v, err %v; want present", ok, err)
	}
	if !account.Blocked {
		t.Fatalf("account = %+v, want blocked by the engine kill-switch", account)
	}
	if !strings.Contains(account.BlockReason, "realized pnl below bound") {
		t.Fatalf("block reason = %q, want the engine reason", account.BlockReason)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(block): %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "acc-1" {
		t.Fatalf("block audit rows = %+v, want one naming acc-1", rows)
	}
}

// A block reported with no accepted or rejected outcome still has to land: the
// engine has already applied it, so ErrNoChange must not swallow it.
func TestLocalNode_ApplyAdjustmentMirrorsBlockWithoutOutcome(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentBatchResults = []engine.AdjustmentResult{{
		AccountBlocks: []domain.AccountBlock{{
			Account: "acc-1",
			Policy:  domain.PolicySpotFundsPnlBoundsKillSwitch,
			Code:    "pnl_bounds",
			Reason:  "realized pnl below bound",
		}},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{Asset: "USD", RealizedPnl: "-500"}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, domain.ErrNoChange) {
		t.Fatalf("ApplyAdjustment = %v, want ErrNoChange", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount = ok %v, err %v; want present", ok, err)
	}
	if !account.Blocked {
		t.Fatalf("account = %+v, want blocked despite the no-change outcome", account)
	}
}

// The common case: an adjustment the engine accepts without tripping any
// kill-switch must leave the account's block state untouched.
func TestLocalNode_ApplyAdjustmentWithoutBlocksLeavesBlockState(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	if _, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}

	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount = ok %v, err %v; want present", ok, err)
	}
	if account.Blocked {
		t.Fatalf("account = %+v, want unblocked", account)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(block): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("block audit rows = %+v, want none", rows)
	}
}
