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

package integration_test

import (
	"context"
	"errors"
	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/store/sqlite"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

type failingResetRealm struct {
	store.RealmStore
	store.AccountPnlResetStore
}

func (r failingResetRealm) RecordAccountPnlReset(context.Context, domain.Account, string, store.AuditEntry) error {
	return errors.New("reset persistence unavailable")
}

type failingResetStore struct{ store.Store }

func (s failingResetStore) ForRealm(ctx context.Context, id domain.RealmID) (store.RealmStore, error) {
	realm, err := s.Store.ForRealm(ctx, id)
	if err != nil {
		return nil, err
	}
	return failingResetRealm{RealmStore: realm, AccountPnlResetStore: realm.(store.AccountPnlResetStore)}, nil
}
func TestLocalNode_ResetPostEnginePersistenceFailureInvokesFatalShutdown(t *testing.T) {
	ctx := systemCtx()
	db, err := sqlite.New(filepath.Join(t.TempDir(), "failure.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fatal := make(chan error, 2)
	n, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, failingResetStore{db}, engine.NewOpenPitEngineBuildFunc(), func(err error) { fatal <- err })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, auth.SystemCaller()); err != nil {
		t.Fatal(err)
	}
	seedResetPosition(t, n, "desk")
	realizeResetPosition(t, n, "desk", "88")
	if _, err := n.(node.AccountPnlResetter).ResetAccountPnl(ctx, "desk", "", auth.SystemCaller()); err == nil {
		t.Fatal("post-Engine persistence failure reported success")
	}
	if len(fatal) != 1 {
		t.Fatalf("fatal shutdown calls=%d", len(fatal))
	}
	realm, err := db.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := realm.GetAccount(ctx, "desk")
	if err != nil || account.Pnl != "-12" {
		t.Fatalf("failure invented durable zero %+v %v", account, err)
	}
}

func seedResetPosition(t *testing.T, n node.Node, account domain.AccountID) {
	t.Helper()
	ctx := systemCtx()
	caller := auth.SystemCaller()
	if _, err := n.CreateAsset(ctx, domain.Asset{Code: "BTC"}, caller); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ApplyAdjustment(ctx, account, "", domain.AdjustmentRequest{Type: domain.AdjustmentTypeCorrection, Asset: "BTC", Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "2"}, AverageEntryPrice: "100"}, domain.MissingAccountReject, caller); err != nil {
		t.Fatal(err)
	}
}

func TestLocalNode_ResetAndFillSerializeWithoutLostPnl(t *testing.T) {
	n, realm := newLocalNode(t)
	ctx := systemCtx()
	caller := auth.SystemCaller()
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, caller); err != nil {
		t.Fatal(err)
	}
	seedResetPosition(t, n, "desk")
	realizeResetPosition(t, n, "desk", "88")
	var group sync.WaitGroup
	group.Add(2)
	start := make(chan struct{})
	failures := make(chan error, 2)
	go func() {
		defer group.Done()
		<-start
		_, err := n.(node.AccountPnlResetter).ResetAccountPnl(ctx, "desk", "", domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator})
		failures <- err
	}()
	go func() {
		defer group.Done()
		<-start
		_, result, err := n.SubmitImmediate(ctx, domain.Order{Account: "desk", BaseAsset: "BTC", QuoteAsset: "USD", Side: domain.OrderSideSell, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "103", DropCopy: true}, domain.MissingAccountReject, caller)
		if err == nil && result.Verdict != domain.VerdictAccept {
			err = errors.New("fill rejected")
		}
		failures <- err
	}()
	close(start)
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	account, _, err := realm.GetAccount(ctx, "desk")
	if err != nil {
		t.Fatal(err)
	}
	audit, err := realm.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range audit {
		if entry.Action == domain.AuditActionResetAccountPnl {
			if entry.Actor != domain.PrincipalOperator || entry.Source != domain.SourcePanel {
				t.Fatalf("manual actor lost: %+v", entry)
			}
			previous := entry.Detail.Params["previousPnl"]
			if (previous == "-12" && account.Pnl == "3") || (previous == "-9" && account.Pnl == "0") {
				return
			}
			t.Fatalf("non-serialized account reset previous=%q final=%q", previous, account.Pnl)
		}
	}
	t.Fatal("reset audit missing")
}

func realizeResetPosition(t *testing.T, n node.Node, account domain.AccountID, price string) {
	t.Helper()
	order, result, err := n.SubmitImmediate(systemCtx(), domain.Order{Account: account, BaseAsset: "BTC", QuoteAsset: "USD", Side: domain.OrderSideSell, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: price, DropCopy: true}, domain.MissingAccountReject, auth.SystemCaller())
	if err != nil || result.Verdict != domain.VerdictAccept || order.Status != domain.OrderStatusFilled {
		t.Fatalf("real Engine fill = %+v %+v %v", order, result, err)
	}
}

func TestLocalNode_ForceAccountPnlResetRetainsHoldingsHistoryAndLatch(t *testing.T) {
	n, realm := newLocalNode(t)
	ctx := systemCtx()
	caller := auth.SystemCaller()
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, caller); err != nil {
		t.Fatal(err)
	}
	seedResetPosition(t, n, "desk")
	realizeResetPosition(t, n, "desk", "88")
	before, _, err := realm.GetAccount(ctx, "desk")
	if err != nil {
		t.Fatal(err)
	}
	if before.Pnl != "-12" {
		t.Fatalf("real Engine realized P&L = %q, want -12", before.Pnl)
	}
	balances, err := n.ListBalances(ctx, "desk", "")
	if err != nil {
		t.Fatal(err)
	}
	history, err := realm.ListAdjustments(ctx, "desk", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SetAccountBlocked(ctx, "desk", true, "retained risk halt", domain.MissingAccountReject, caller); err != nil {
		t.Fatal(err)
	}
	reset := n.(node.AccountPnlResetter)
	got, err := reset.ResetAccountPnl(ctx, "desk", "", caller)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pnl != "0" || got.PnlHaltReason != "" || !got.Blocked {
		t.Fatalf("reset state = %+v", got)
	}
	stored, _, err := realm.GetAccount(ctx, "desk")
	if err != nil || stored.Pnl != "0" || !stored.Blocked {
		t.Fatalf("persisted reset = %+v %v", stored, err)
	}
	afterBalances, err := n.ListBalances(ctx, "desk", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(balances, afterBalances) {
		t.Fatalf("reset changed holdings: before=%+v after=%+v", balances, afterBalances)
	}
	afterHistory, err := realm.ListAdjustments(ctx, "desk", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(history, afterHistory) {
		t.Fatal("reset changed adjustment history")
	}
	// A subsequent real SDK fill realizes P&L relative to the account's zero,
	// while its position accumulator retains its own cumulative history.
	realizeResetPosition(t, n, "desk", "103")
	got, _, err = realm.GetAccount(ctx, "desk")
	if err != nil || got.Pnl != "3" || !got.Blocked {
		t.Fatalf("continued SDK realization = %+v %v, want 3 with latch", got, err)
	}
	if _, err := reset.ResetAccountPnl(ctx, "missing", "", caller); !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("missing reset = %v", err)
	}
	if _, found, _ := realm.GetAccount(ctx, "missing"); found {
		t.Fatal("missing account was provisioned")
	}
	agent := domain.Caller{Source: domain.SourceMCP, Principal: "agent"}
	if _, err := reset.ResetAccountPnl(ctx, "desk", "", agent); !errors.Is(err, domain.ErrLoosening) {
		t.Fatalf("agent reset = %v", err)
	}
	got, _, _ = realm.GetAccount(ctx, "desk")
	if got.Pnl != "3" {
		t.Fatal("agent reset mutated P&L")
	}
}

func TestLocalNode_ForceResetUnsetAndRestoredHalt(t *testing.T) {
	n, realm := newLocalNode(t)
	ctx := systemCtx()
	caller := auth.SystemCaller()
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "unset", Currency: "USD"}, caller); err != nil {
		t.Fatal(err)
	}
	reset := n.(node.AccountPnlResetter)
	before, _, err := realm.GetAccount(ctx, "unset")
	if err != nil || before.Pnl != "" {
		t.Fatalf("unset=%+v %v", before, err)
	}
	got, err := reset.ResetAccountPnl(ctx, "unset", "", caller)
	if err != nil || got.Pnl != "0" || got.PnlHaltReason != "" {
		t.Fatalf("force unset=%+v %v", got, err)
	}
	scope := backup.Scope{Sections: backup.AllSections}
	archive, err := n.ExportBackup(ctx, scope, caller)
	if err != nil {
		t.Fatal(err)
	}
	archive.Data.Accounts[0].Pnl = ""
	archive.Data.Accounts[0].PnlHaltReason = domain.PnlHaltReasonMissingFx
	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: scope}, caller); err != nil {
		t.Fatal(err)
	}
	before, _, err = realm.GetAccount(ctx, "unset")
	if err != nil || before.PnlHaltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf("restored halt=%+v %v", before, err)
	}
	got, err = reset.ResetAccountPnl(ctx, "unset", "", caller)
	if err != nil || got.Pnl != "0" || got.PnlHaltReason != "" {
		t.Fatalf("force halt=%+v %v", got, err)
	}
}

func TestLocalNode_ResetReceiptSurvivesRenameAndFullRestore(t *testing.T) {
	n, realm := newLocalNode(t)
	ctx := systemCtx()
	caller := auth.SystemCaller()
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, caller); err != nil {
		t.Fatal(err)
	}
	seedResetPosition(t, n, "desk")
	realizeResetPosition(t, n, "desk", "93")
	reset := n.(node.AccountPnlResetter)
	runID := "job:2030-01-01T09:00:00.000000000Z"
	if _, err := reset.ResetAccountPnl(ctx, "desk", runID, caller); err != nil {
		t.Fatal(err)
	}
	realizeResetPosition(t, n, "desk", "102")
	if _, err := n.UpdateAccount(ctx, "desk", domain.Account{Code: "renamed"}, caller); err != nil {
		t.Fatal(err)
	}
	got, err := reset.ResetAccountPnl(ctx, "renamed", runID, caller)
	if err != nil || got.Pnl != "2" {
		t.Fatalf("rename retry = %+v %v", got, err)
	}
	archive, err := n.ExportBackup(ctx, backup.Scope{Sections: backup.AllSections}, caller)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Data.AccountPnlResetReceipts) != 1 || archive.Data.AccountPnlResetReceipts[0].Account != "renamed" {
		t.Fatalf("portable receipts=%+v", archive.Data.AccountPnlResetReceipts)
	}
	target, _ := newLocalNode(t)
	if _, _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeReplaceAll, Scope: backup.Scope{Sections: backup.AllSections}}, caller); err != nil {
		t.Fatal(err)
	}
	got, err = target.(node.AccountPnlResetter).ResetAccountPnl(ctx, "renamed", runID, caller)
	if err != nil || got.Pnl != "2" {
		t.Fatalf("restored retry = %+v %v", got, err)
	}
	entries, err := realm.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	resets := 0
	for _, entry := range entries {
		if entry.Action == domain.AuditActionResetAccountPnl {
			resets++
		}
	}
	if resets != 1 {
		t.Fatalf("duplicate retry audit count = %d", resets)
	}
}
