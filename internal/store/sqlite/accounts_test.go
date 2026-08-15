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

// Accounts-group tests: the account P&L snapshot setter that mirrors an
// engine-applied assignment.

package sqlite

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestSetAccountPnl(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code:          "acc-1",
		Pnl:           "12.5",
		PnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// The value and the halt are one fact: zeroing the P&L retires the halt with
	// it in the same write.
	if err := rs.SetAccountPnl(ctx, "acc-1", "0", ""); err != nil {
		t.Fatalf("SetAccountPnl: %v", err)
	}
	account, ok, err := rs.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v, ok = %v", err, ok)
	}
	if account.Pnl != "0" || account.PnlHaltReason != "" {
		t.Fatalf(
			"account pnl = %q halt = %q, want 0 with no halt",
			account.Pnl, account.PnlHaltReason,
		)
	}

	if err := rs.SetAccountPnl(
		ctx, "acc-1", "", domain.PnlHaltReasonMissingFx,
	); err != nil {
		t.Fatalf("SetAccountPnl(empty halt): %v", err)
	}
	account, ok, err = rs.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount(empty halt): %v, ok = %v", err, ok)
	}
	if account.Pnl != "" || account.PnlHaltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf(
			"account pnl = %q halt = %q, want no value with missing_fx",
			account.Pnl,
			account.PnlHaltReason,
		)
	}
	var storedPnl any
	if err := rs.(*realmStore).rawDB().QueryRowContext(
		ctx, `SELECT pnl FROM account WHERE code = ?`, "acc-1",
	).Scan(&storedPnl); err != nil {
		t.Fatalf("read stored halted pnl: %v", err)
	}
	if storedPnl != nil {
		t.Fatalf("stored halted pnl = %#v, want NULL", storedPnl)
	}

	// The halt itself is account-currency state and blocks a denomination change.
	blockers, err := rs.ListAccountsBlockingCurrencyChange(
		ctx, []domain.AccountID{"acc-1"},
	)
	if err != nil {
		t.Fatalf("ListAccountsBlockingCurrencyChange: %v", err)
	}
	if len(blockers) != 1 || blockers[0] != "acc-1" {
		t.Fatalf("currency blockers = %v, want acc-1", blockers)
	}
}

func TestSetAccountPnlRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := rs.SetAccountPnl(ctx, "acc-1", "not-a-number", ""); !errors.Is(
		err, domain.ErrInvalid,
	) {
		t.Fatalf("SetAccountPnl(non-decimal) = %v, want ErrInvalid", err)
	}
	if err := rs.SetAccountPnl(ctx, "acc-1", "0", "no-such-reason"); !errors.Is(
		err, domain.ErrInvalid,
	) {
		t.Fatalf("SetAccountPnl(unknown halt) = %v, want ErrInvalid", err)
	}
	if err := rs.SetAccountPnl(ctx, "missing", "0", ""); !errors.Is(
		err, domain.ErrNotFound,
	) {
		t.Fatalf("SetAccountPnl(missing account) = %v, want ErrNotFound", err)
	}
}

func TestUpdateAccountPreservesStableIdentityAndDependents(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	for _, asset := range []string{"AAPL", "USD"} {
		if _, err := rs.CreateAsset(ctx, domain.Asset{Code: asset}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	created, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Title: "Before",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account:           created.Code,
		Asset:             "AAPL",
		Available:         "2",
		RealizedPnl:       "3",
		AverageEntryPrice: "10",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	order, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	updated, err := rs.UpdateAccount(ctx, created.Code, domain.Account{
		Code: "acc-renamed", Title: "After",
	})
	if err != nil {
		t.Fatalf("UpdateAccount with dependents: %v", err)
	}
	if updated.EngineAccountID != created.EngineAccountID {
		t.Fatalf(
			"engine account id changed: %d -> %d",
			created.EngineAccountID,
			updated.EngineAccountID,
		)
	}
	if updated.Title != "After" {
		t.Fatalf("updated title = %q, want After", updated.Title)
	}
	if _, ok, err := rs.GetBalance(ctx, created.Code, "AAPL"); err != nil || ok {
		t.Fatalf("GetBalance(old code) = ok %v err %v, want no row", ok, err)
	}
	balance, ok, err := rs.GetBalance(ctx, updated.Code, "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance(new code) = %+v ok %v err %v", balance, ok, err)
	}
	if balance.Available != "2" || balance.RealizedPnl != "3" ||
		balance.AverageEntryPrice != "10" {
		t.Fatalf("renamed balance = %+v, want original economic state", balance)
	}
	detail, err := rs.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder after rename: %v", err)
	}
	if detail.Order.Account != updated.Code {
		t.Fatalf("order account = %q, want %q", detail.Order.Account, updated.Code)
	}
}
