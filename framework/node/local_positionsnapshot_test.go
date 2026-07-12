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

func TestLocalNode_ImportPositionSnapshotPersistsRealizedPnlAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	snapshot := domain.Balance{
		Account:           "acc-1",
		Asset:             "USD",
		Available:         "100.25",
		Held:              "10.5",
		Incoming:          "2.75",
		RealizedPnl:       "7.125",
		AverageEntryPrice: "99.5",
	}
	rec, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), domain.ExternalID(""), snapshot, testCaller)
	if err != nil {
		t.Fatalf("ImportPositionSnapshot: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %d, want 1", len(eng.adjustmentCalls))
	}
	req := eng.adjustmentCalls[0].req
	if req.Asset != "USD" ||
		req.Balance == nil || req.Balance.Mode != domain.AdjustmentModeAbsolute ||
		req.Balance.Value != "100.25" ||
		req.Held == nil || req.Held.Mode != domain.AdjustmentModeAbsolute ||
		req.Held.Value != "10.5" ||
		req.Incoming == nil || req.Incoming.Mode != domain.AdjustmentModeAbsolute ||
		req.Incoming.Value != "2.75" ||
		req.AverageEntryPrice != "99.5" {
		t.Fatalf("adjustment request = %+v", req)
	}
	got, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if got.Available != "100.25" || got.Held != "10.5" ||
		got.Incoming != "2.75" || got.RealizedPnl != "7.125" ||
		got.AverageEntryPrice != "99.5" {
		t.Fatalf("balance = %+v", got)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 ||
		!strings.Contains(rows[0].Detail, "import position snapshot account acc-1 asset=USD") ||
		!strings.Contains(rows[0].Detail, "realized_pnl=7.125") {
		t.Fatalf("audit rows = %+v", rows)
	}
}

func TestLocalNode_ImportPositionSnapshotDuplicateExternalIDDoesNotApplyEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, n.realm, "acc-1")

	supplied := externalID(t, "snapshot-adj-id")
	snapshot := domain.Balance{Account: "acc-1", Asset: "USD", Available: "100"}
	if _, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), supplied, snapshot, testCaller); err != nil {
		t.Fatalf("first ImportPositionSnapshot: %v", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine calls after first import = %d, want 1", len(eng.adjustmentCalls))
	}
	_, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), supplied, snapshot, testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate snapshot id error = %v, want ErrAlreadyExists", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine calls after duplicate = %d, want no second apply", len(eng.adjustmentCalls))
	}
}

func TestLocalNode_ImportPositionSnapshotStoreFailureFatalsWithoutCompensation(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record snapshot adjustment failed")
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

	_, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.Balance{
			Account: "acc-1", Asset: "USD", Available: "15", AverageEntryPrice: "99",
		}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("ImportPositionSnapshot error = %v, want store failure", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want only the requested apply", eng.adjustmentCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine snapshot persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record position snapshot adjustment"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record snapshot adjustment failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

func TestLocalNode_ImportPositionSnapshotDeletesEmptyPosition(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD",
		Available: "100", AverageEntryPrice: "50",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	snapshot := domain.Balance{
		Account: "acc-1", Asset: "USD",
		Available: "0.00", Held: "0", Incoming: "0",
		RealizedPnl: "0", AverageEntryPrice: "0.000",
	}
	if _, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), domain.ExternalID(""), snapshot, testCaller); err != nil {
		t.Fatalf("ImportPositionSnapshot: %v", err)
	}
	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance after empty snapshot: ok=%v err=%v, want missing", ok, err)
	}
}

func TestLocalNode_ImportPositionSnapshotAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	snapshot := domain.Balance{
		Account:   "acc-1",
		Asset:     "GOLD",
		Available: "100.25",
	}
	if _, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), domain.ExternalID(""), snapshot, testCaller); err != nil {
		t.Fatalf("ImportPositionSnapshot: %v", err)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Asset != "GOLD" ||
		!strings.Contains(rows[0].Detail, "auto-created asset GOLD by position snapshot import") {
		t.Fatalf("create-asset audit rows = %+v, want snapshot auto-create", rows)
	}
}
