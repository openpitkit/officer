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

// Balances-group tests: upsert/get/list/delete round-trips, realized_pnl
// semantics (UpsertBalance writes the caller-supplied absolute; settleBalanceTx
// accumulates), cascade on account/asset delete, and unknown-FK-code error.

package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// seedBalanceFixtures seeds two asset and an account and returns the realm
// handle ready for balance tests.
func seedBalanceFixtures(t *testing.T) (context.Context, RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Apple"}); err != nil {
		t.Fatalf("CreateAsset(AAPL): %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD", Title: "Dollar"}); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return ctx, rs
}

func TestBalanceUpsertGetDeleteRoundTrip(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	bal := domain.Balance{
		Account:               "acc-1",
		Asset:                 "AAPL",
		Available:             "100",
		Held:                  "10",
		Incoming:              "5",
		RealizedPnl:           "2.5",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
		AverageEntryPrice:     "150",
		UpdatedAt:             time.Now().UTC().Truncate(time.Second),
	}
	if err := rs.UpsertBalance(ctx, bal); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	got, ok, err := rs.GetBalance(ctx, "acc-1", "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance: ok=%v err=%v", ok, err)
	}
	if got.Account != "acc-1" || got.Asset != "AAPL" {
		t.Fatalf("GetBalance codes = %q/%q, want acc-1/AAPL", got.Account, got.Asset)
	}
	if got.Available != "100" || got.Held != "10" || got.Incoming != "5" {
		t.Fatalf("GetBalance amounts = avail=%q held=%q incoming=%q", got.Available, got.Held, got.Incoming)
	}
	if got.RealizedPnl != "2.5" {
		t.Fatalf("GetBalance realized_pnl = %q, want 2.5", got.RealizedPnl)
	}
	if got.RealizedPnlHaltReason != domain.PnlHaltReasonMissingCostBasis {
		t.Fatalf("GetBalance realized PnL halt reason = %q", got.RealizedPnlHaltReason)
	}
	if got.AverageEntryPrice != "150" {
		t.Fatalf("GetBalance avg_entry = %q, want 150", got.AverageEntryPrice)
	}

	// GetBalance of an absent row returns ok=false.
	_, ok, err = rs.GetBalance(ctx, "acc-1", "USD")
	if err != nil || ok {
		t.Fatalf("GetBalance(missing): ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// DeleteBalance removes the row.
	if err := rs.DeleteBalance(ctx, "acc-1", "AAPL"); err != nil {
		t.Fatalf("DeleteBalance: %v", err)
	}
	_, ok, err = rs.GetBalance(ctx, "acc-1", "AAPL")
	if err != nil || ok {
		t.Fatalf("GetBalance after delete: ok=%v err=%v", ok, err)
	}

	// Second delete is ErrNotFound.
	if err := rs.DeleteBalance(ctx, "acc-1", "AAPL"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteBalance(missing) error = %v, want ErrNotFound", err)
	}
}

func TestBalanceUpsertOverwritesRealizedPnlAbsolute(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	// First upsert with realized_pnl = "10".
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "AAPL",
		Available: "100", RealizedPnl: "10",
	}); err != nil {
		t.Fatalf("UpsertBalance (first): %v", err)
	}
	// Second upsert overwrites with realized_pnl = "25" (snapshot semantics:
	// the caller supplies the absolute value; the store does not accumulate).
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "AAPL",
		Available: "200", RealizedPnl: "25",
	}); err != nil {
		t.Fatalf("UpsertBalance (second): %v", err)
	}
	got, _, _ := rs.GetBalance(ctx, "acc-1", "AAPL")
	if got.RealizedPnl != "25" {
		t.Fatalf("realized_pnl after two upserts = %q, want 25 (absolute overwrite)", got.RealizedPnl)
	}
	if got.Available != "200" {
		t.Fatalf("available after second upsert = %q, want 200", got.Available)
	}
}

func TestBalanceSettlePersistsEngineRealizedPnlAbsolute(t *testing.T) {
	// This test exercises the SDK absolute through RecordOrderSettlement, which
	// calls settleBalanceTx internally.
	ctx, rs := seedBalanceFixtures(t)

	// Seed an order needed by the settlement path.
	order, err := rs.CreateOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Source:      domain.SourcePanel,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "150",
		Status:      domain.OrderStatusSubmitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// Seed a starting balance.
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "AAPL",
		Available: "100", RealizedPnl: "5",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	// The engine reports the resulting absolute as 8.
	st := domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Balances: []domain.BalanceSettlement{
			{
				Asset: "AAPL",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult:     "90",
					RealizedPnlDelta:  "3",
					RealizedPnlResult: "8",
				},
			},
		},
		Events: []domain.OrderEvent{
			{Source: domain.SourcePanel, Type: domain.OrderEventFill},
		},
	}
	if _, err := rs.RecordOrderSettlement(ctx, st); err != nil {
		t.Fatalf("RecordOrderSettlement (first): %v", err)
	}
	got, _, _ := rs.GetBalance(ctx, "acc-1", "AAPL")
	if got.RealizedPnl != "8" {
		t.Fatalf("realized_pnl after settle = %q, want engine absolute 8", got.RealizedPnl)
	}
}

func TestBalanceListFilters(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "AAPL", Available: "1", RealizedPnl: "0",
	}); err != nil {
		t.Fatalf("UpsertBalance(AAPL): %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "2", RealizedPnl: "0",
	}); err != nil {
		t.Fatalf("UpsertBalance(USD): %v", err)
	}

	// Both present, empty filter returns all.
	all, err := rs.ListBalances(ctx, "", "")
	if err != nil {
		t.Fatalf("ListBalances(all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListBalances(all) len = %d, want 2", len(all))
	}

	// Filter by account.
	byAcc, err := rs.ListBalances(ctx, "acc-1", "")
	if err != nil {
		t.Fatalf("ListBalances(by account): %v", err)
	}
	if len(byAcc) != 2 {
		t.Fatalf("ListBalances(acc-1) len = %d, want 2", len(byAcc))
	}

	// Filter by asset.
	byAsset, err := rs.ListBalances(ctx, "", "AAPL")
	if err != nil {
		t.Fatalf("ListBalances(by asset): %v", err)
	}
	if len(byAsset) != 1 || byAsset[0].Asset != "AAPL" {
		t.Fatalf("ListBalances(AAPL) = %+v", byAsset)
	}

	// Non-nil empty slice when nothing matches.
	none, err := rs.ListBalances(ctx, "acc-1", "EUR")
	if err != nil {
		t.Fatalf("ListBalances(no match): %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Fatalf("ListBalances(no match) = %v, want non-nil empty slice", none)
	}
}

func TestListAccountsWithOpenBalancesIncludesBalanceRowsAndAccountPnl(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatalf("CreateAccount acc-2: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-3", Pnl: "0.000000000000000000000000000000000001",
	}); err != nil {
		t.Fatalf("CreateAccount acc-3: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-4", Pnl: "0.000"}); err != nil {
		t.Fatalf("CreateAccount acc-4: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-5", PnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
	}); err != nil {
		t.Fatalf("CreateAccount acc-5: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-6"}); err != nil {
		t.Fatalf("CreateAccount acc-6: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account:     "acc-1",
		Asset:       "AAPL",
		RealizedPnl: "2.5",
	}); err != nil {
		t.Fatalf("UpsertBalance realized P&L: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account:           "acc-2",
		Asset:             "USD",
		AverageEntryPrice: "100",
	}); err != nil {
		t.Fatalf("UpsertBalance average entry: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-6", Asset: "AAPL",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
	}); err != nil {
		t.Fatalf("UpsertBalance halt-only: %v", err)
	}

	got, err := rs.ListAccountsWithOpenBalances(ctx, nil)
	if err != nil {
		t.Fatalf("ListAccountsWithOpenBalances(all): %v", err)
	}
	if strings.Join(accountIDsToStrings(got), ",") != "acc-1,acc-2,acc-3" {
		t.Fatalf("ListAccountsWithOpenBalances(all) = %v, want halt-only accounts excluded", got)
	}

	filtered, err := rs.ListAccountsWithOpenBalances(ctx, []domain.AccountID{"acc-2"})
	if err != nil {
		t.Fatalf("ListAccountsWithOpenBalances(filtered): %v", err)
	}
	if len(filtered) != 1 || filtered[0] != "acc-2" {
		t.Fatalf("ListAccountsWithOpenBalances(filtered) = %v, want acc-2", filtered)
	}

	filtered, err = rs.ListAccountsWithOpenBalances(ctx, []domain.AccountID{"acc-3"})
	if err != nil {
		t.Fatalf("ListAccountsWithOpenBalances(account P&L): %v", err)
	}
	if len(filtered) != 1 || filtered[0] != "acc-3" {
		t.Fatalf("ListAccountsWithOpenBalances(account P&L) = %v, want acc-3", filtered)
	}

	// acc-5 (account halt) and acc-6 (position halt) carry a halt flag and no
	// number. A halted P&L is not a value that could need recomputing in a new
	// currency, so neither account is open and neither blocks a currency change.
	filtered, err = rs.ListAccountsWithOpenBalances(ctx, []domain.AccountID{"acc-5", "acc-6"})
	if err != nil {
		t.Fatalf("ListAccountsWithOpenBalances(halt-only): %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("ListAccountsWithOpenBalances(halt-only) = %v, want empty", filtered)
	}
}

// A number behind a halt is historical, not authoritative: the engine retains
// the prior P&L when it reports a halt with no value. Quantities and cost basis
// are not P&L and keep counting behind a halt.
func TestListAccountsWithOpenBalancesExcludesValuesBehindPnlHalt(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	for _, account := range []domain.Account{
		// The stale number the engine left behind when it halted.
		{
			Code:          "acc-halted-pnl",
			Pnl:           "-50000",
			PnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
		},
		{Code: "acc-healthy-pnl", Pnl: "12.5"},
		{Code: "acc-halted-realized"},
		{Code: "acc-halted-quantity"},
		{Code: "acc-halted-cost-basis"},
	} {
		if _, err := rs.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount(%s): %v", account.Code, err)
		}
	}
	for _, balance := range []domain.Balance{
		{
			Account:               "acc-halted-realized",
			Asset:                 "AAPL",
			RealizedPnl:           "2.5",
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
		},
		// A halted position still holds real units.
		{
			Account:               "acc-halted-quantity",
			Asset:                 "AAPL",
			Available:             "7",
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
		},
		// Cost basis is applied from the report's own inputs, so it survives a
		// P&L halt as a real number that a currency change would have to redo.
		{
			Account:               "acc-halted-cost-basis",
			Asset:                 "AAPL",
			AverageEntryPrice:     "150",
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
		},
	} {
		if err := rs.UpsertBalance(ctx, balance); err != nil {
			t.Fatalf("UpsertBalance(%s): %v", balance.Account, err)
		}
	}

	got, err := rs.ListAccountsWithOpenBalances(ctx, []domain.AccountID{
		"acc-halted-pnl", "acc-healthy-pnl", "acc-halted-realized",
		"acc-halted-quantity", "acc-halted-cost-basis",
	})
	if err != nil {
		t.Fatalf("ListAccountsWithOpenBalances: %v", err)
	}
	want := "acc-halted-cost-basis,acc-halted-quantity,acc-healthy-pnl"
	if strings.Join(accountIDsToStrings(got), ",") != want {
		t.Fatalf("ListAccountsWithOpenBalances = %v, want %s", got, want)
	}
}

func accountIDsToStrings(ids []domain.AccountID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

func TestBalanceListRowsSortFilterPageOrdersDecimalsNumerically(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := rs.SetAccountGroup(ctx, "acc-1", "desk"); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-3"}); err != nil {
		t.Fatalf("CreateAccount(acc-3): %v", err)
	}
	// A negative and an arbitrary-precision value prove the column's DECIMAL
	// collation orders numerically with no fixed-width window.
	for _, balance := range []domain.Balance{
		{Account: "acc-1", Asset: "AAPL", Available: "10", RealizedPnl: "0"},
		{Account: "acc-1", Asset: "USD", Available: "2.5", RealizedPnl: "0"},
		{Account: "acc-2", Asset: "AAPL", Available: "3", RealizedPnl: "0"},
		{
			Account: "acc-2", Asset: "USD",
			Available:   "3.0000000001",
			RealizedPnl: "0",
		},
		{
			Account: "acc-3", Asset: "AAPL",
			Available:   "1000000000000000000000000000000000.0001",
			RealizedPnl: "0",
		},
		{Account: "acc-3", Asset: "USD", Available: "-4", RealizedPnl: "0"},
	} {
		if err := rs.UpsertBalance(ctx, balance); err != nil {
			t.Fatalf("UpsertBalance(%s/%s): %v", balance.Account, balance.Asset, err)
		}
	}

	group := "desk"
	page, err := rs.ListBalanceRows(ctx, fwstore.BalanceListFilter{
		GroupCode: &group,
		Sort:      fwstore.SortSpec{Column: "available"},
		Page:      fwstore.PageSpec{Limit: 1, Offset: 1},
	})
	if err != nil {
		t.Fatalf("ListBalanceRows(group sort page): %v", err)
	}
	if page.Total != 2 || len(page.Rows) != 1 {
		t.Fatalf("page total/len = %d/%d, want 2/1", page.Total, len(page.Rows))
	}
	if got := page.Rows[0].Balance.Available; got != "10" {
		t.Fatalf("second sorted available = %q, want 10", got)
	}

	min := "3"
	page, err = rs.ListBalanceRows(ctx, fwstore.BalanceListFilter{
		Available: fwstore.DecimalRangeFilter{Min: &min},
		Sort:      fwstore.SortSpec{Column: "available"},
	})
	if err != nil {
		t.Fatalf("ListBalanceRows(range): %v", err)
	}
	got := []string{}
	for _, row := range page.Rows {
		got = append(got, row.Balance.Available)
	}
	want := []string{
		"3", "3.0000000001", "10",
		"1000000000000000000000000000000000.0001",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("range sorted values = %v, want %v", got, want)
	}
}

func TestBalanceCascadeOnAccountDelete(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "AAPL", Available: "1", RealizedPnl: "0",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	if err := rs.DeleteAccount(ctx, "acc-1", false); !errors.Is(err, domain.ErrHasDependents) {
		t.Fatalf("DeleteAccount(no force) error = %v, want ErrHasDependents", err)
	}
	if err := rs.DeleteAccount(ctx, "acc-1", true); err != nil {
		t.Fatalf("DeleteAccount(force): %v", err)
	}

	// Balance row must be gone (CASCADE).
	all, err := rs.ListBalances(ctx, "", "")
	if err != nil {
		t.Fatalf("ListBalances after account delete: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0 balances after account cascade, got %d", len(all))
	}
}

func TestBalanceCascadeOnAssetDelete(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "AAPL", Available: "1", RealizedPnl: "0",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	// DeleteAsset cascades to balance.
	if err := rs.DeleteAsset(ctx, "AAPL", true); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}

	all, err := rs.ListBalances(ctx, "", "")
	if err != nil {
		t.Fatalf("ListBalances after asset delete: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0 balances after asset cascade, got %d", len(all))
	}
}

func TestBalanceUnknownAccountIsInvalid(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "ghost", Asset: "AAPL", Available: "1", RealizedPnl: "0",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("UpsertBalance(unknown account) error = %v, want ErrInvalid", err)
	}
}

func TestBalanceUnknownAssetIsInvalid(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "GHOST", Available: "1", RealizedPnl: "0",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("UpsertBalance(unknown asset) error = %v, want ErrInvalid", err)
	}
}

// A position's realized P&L and average entry price are denominated in the
// account currency, which the account itself may inherit. Every tier of the
// cascade must denominate the row exactly as a directly assigned currency does.
func TestBalanceAccountCurrencyResolvesEveryCascadeTier(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "EUR", Title: "Euro"}); err != nil {
		t.Fatalf("CreateAsset(EUR): %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "GBP", Title: "Pound"}); err != nil {
		t.Fatalf("CreateAsset(GBP): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "grp-1"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(
		ctx, domain.Account{Code: "acc-grp", GroupCode: "grp-1"},
	); err != nil {
		t.Fatalf("CreateAccount(acc-grp): %v", err)
	}
	for _, code := range []domain.AccountID{"acc-1", "acc-grp"} {
		if err := rs.UpsertBalance(ctx, domain.Balance{
			Account: code, Asset: "AAPL", AverageEntryPrice: "10", RealizedPnl: "1",
		}); err != nil {
			t.Fatalf("UpsertBalance(%s): %v", code, err)
		}
	}

	currencyOf := func(t *testing.T, account domain.AccountID) string {
		t.Helper()
		got, ok, err := rs.GetBalance(ctx, account, "AAPL")
		if err != nil || !ok {
			t.Fatalf("GetBalance(%s): ok=%v err=%v", account, ok, err)
		}
		return got.AccountCurrency
	}

	// No tier set: the values carry no unit.
	if got := currencyOf(t, "acc-1"); got != "" {
		t.Fatalf("AccountCurrency with no tier set = %q, want empty", got)
	}

	// Default group tier is the last resort.
	if err := rs.SetGroupCurrency(ctx, "", "GBP"); err != nil {
		t.Fatalf("SetGroupCurrency(default): %v", err)
	}
	if got := currencyOf(t, "acc-grp"); got != "GBP" {
		t.Fatalf("AccountCurrency from default tier = %q, want GBP", got)
	}

	// The account's own group outranks the default group.
	if err := rs.SetGroupCurrency(ctx, "grp-1", "EUR"); err != nil {
		t.Fatalf("SetGroupCurrency(grp-1): %v", err)
	}
	if got := currencyOf(t, "acc-grp"); got != "EUR" {
		t.Fatalf("AccountCurrency from group tier = %q, want EUR", got)
	}

	// The account tier outranks both.
	if err := rs.SetAccountCurrency(ctx, "acc-grp", "USD"); err != nil {
		t.Fatalf("SetAccountCurrency(acc-grp): %v", err)
	}
	if got := currencyOf(t, "acc-grp"); got != "USD" {
		t.Fatalf("AccountCurrency from account tier = %q, want USD", got)
	}
}

// The motivating case: two accounts hold the same asset but keep their P&L in
// different currencies. A threshold given in one currency must never be
// compared against a row denominated in the other, so the currency the
// threshold names selects which rows the numeric bounds even apply to.
func TestBalanceListRowsComparesOnlyWithinNamedCurrency(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "EUR", Title: "Euro"}); err != nil {
		t.Fatalf("CreateAsset(EUR): %v", err)
	}
	for _, seed := range []struct {
		account  domain.AccountID
		currency string
		pnl      string
	}{
		{"acc-usd", "USD", "100"},
		{"acc-eur", "EUR", "100"},
		{"acc-none", "", "100"},
	} {
		if _, err := rs.CreateAccount(
			ctx, domain.Account{Code: seed.account},
		); err != nil {
			t.Fatalf("CreateAccount(%s): %v", seed.account, err)
		}
		if seed.currency != "" {
			if err := rs.SetAccountCurrency(
				ctx, seed.account, seed.currency,
			); err != nil {
				t.Fatalf("SetAccountCurrency(%s): %v", seed.account, err)
			}
		}
		if err := rs.UpsertBalance(ctx, domain.Balance{
			Account:           seed.account,
			Asset:             "AAPL",
			AverageEntryPrice: seed.pnl,
			RealizedPnl:       seed.pnl,
		}); err != nil {
			t.Fatalf("UpsertBalance(%s): %v", seed.account, err)
		}
	}

	min50 := "50"
	listAccounts := func(t *testing.T, filter fwstore.BalanceListFilter) []string {
		t.Helper()
		page, err := rs.ListBalanceRows(ctx, filter)
		if err != nil {
			t.Fatalf("ListBalanceRows: %v", err)
		}
		got := make([]string, 0, len(page.Rows))
		for _, row := range page.Rows {
			got = append(got, string(row.Balance.Account))
		}
		if page.Total != len(got) {
			t.Fatalf("Total = %d, want %d matching rows", page.Total, len(got))
		}
		return got
	}

	// Every row clears 50 numerically, but only the USD row is denominated in
	// the currency the threshold was given in.
	got := listAccounts(t, fwstore.BalanceListFilter{
		RealizedPnl: fwstore.DenominatedDecimalRangeFilter{
			Range:    fwstore.DecimalRangeFilter{Min: &min50},
			Currency: "USD",
		},
	})
	if len(got) != 1 || got[0] != "acc-usd" {
		t.Fatalf("realized P&L >= 50 USD = %v, want [acc-usd]", got)
	}

	// Naming the other currency selects the other row, not both.
	got = listAccounts(t, fwstore.BalanceListFilter{
		AverageEntryPrice: fwstore.DenominatedDecimalRangeFilter{
			Range:    fwstore.DecimalRangeFilter{Min: &min50},
			Currency: "EUR",
		},
	})
	if len(got) != 1 || got[0] != "acc-eur" {
		t.Fatalf("avg entry price >= 50 EUR = %v, want [acc-eur]", got)
	}

	// A row whose account names no currency has an unlabelled number and so
	// cannot satisfy a threshold that names one.
	got = listAccounts(t, fwstore.BalanceListFilter{
		RealizedPnl: fwstore.DenominatedDecimalRangeFilter{
			Range:    fwstore.DecimalRangeFilter{Min: &min50},
			Currency: "GBP",
		},
	})
	if len(got) != 0 {
		t.Fatalf("realized P&L >= 50 GBP = %v, want no rows", got)
	}

	// Without a numeric condition the currency constrains nothing: it scopes a
	// comparison, it is not an identity filter.
	got = listAccounts(t, fwstore.BalanceListFilter{
		Asset: fwstore.ExactTextMatcher("AAPL"),
	})
	if len(got) != 3 {
		t.Fatalf("unfiltered AAPL rows = %v, want every seeded row", got)
	}
}

func TestBalanceListRowsRejectsDenominatedRangeWithoutCurrency(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)
	min := "50"
	_, err := rs.ListBalanceRows(ctx, fwstore.BalanceListFilter{
		RealizedPnl: fwstore.DenominatedDecimalRangeFilter{
			Range: fwstore.DecimalRangeFilter{Min: &min},
		},
	})
	if !errors.Is(err, fwstore.ErrCurrencyRequired) {
		t.Fatalf("ListBalanceRows invalid filter = %v, want ErrCurrencyRequired", err)
	}
}

// An inherited currency denominates its rows exactly as a directly assigned one
// does, so a threshold must reach rows whose account never names a currency of
// its own.
func TestBalanceListRowsComparesAgainstInheritedCurrency(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "grp-1"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(
		ctx, domain.Account{Code: "acc-grp", GroupCode: "grp-1"},
	); err != nil {
		t.Fatalf("CreateAccount(acc-grp): %v", err)
	}
	if err := rs.SetGroupCurrency(ctx, "grp-1", "USD"); err != nil {
		t.Fatalf("SetGroupCurrency: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-grp", Asset: "AAPL", RealizedPnl: "100",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	min50 := "50"
	page, err := rs.ListBalanceRows(ctx, fwstore.BalanceListFilter{
		RealizedPnl: fwstore.DenominatedDecimalRangeFilter{
			Range:    fwstore.DecimalRangeFilter{Min: &min50},
			Currency: "USD",
		},
	})
	if err != nil {
		t.Fatalf("ListBalanceRows: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Balance.Account != "acc-grp" {
		t.Fatalf("rows = %+v, want the group-inherited USD row", page.Rows)
	}
	if got := page.Rows[0].Balance.AccountCurrency; got != "USD" {
		t.Fatalf("AccountCurrency = %q, want USD", got)
	}
}

func TestBalanceListRowsUsesEveryCurrencyCascadeTier(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)
	for _, code := range []string{"EUR", "GBP"} {
		if err := rs.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "grp-1"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	for _, account := range []domain.Account{
		{Code: "acc-default"},
		{Code: "acc-group", GroupCode: "grp-1"},
		{Code: "acc-account", GroupCode: "grp-1", Currency: "USD"},
	} {
		if _, err := rs.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount(%s): %v", account.Code, err)
		}
		if err := rs.UpsertBalance(ctx, domain.Balance{
			Account: account.Code, Asset: "AAPL", RealizedPnl: "100",
		}); err != nil {
			t.Fatalf("UpsertBalance(%s): %v", account.Code, err)
		}
	}
	if err := rs.SetGroupCurrency(ctx, "", "GBP"); err != nil {
		t.Fatalf("SetGroupCurrency(default): %v", err)
	}
	if err := rs.SetGroupCurrency(ctx, "grp-1", "EUR"); err != nil {
		t.Fatalf("SetGroupCurrency(grp-1): %v", err)
	}

	min := "50"
	for _, want := range []struct {
		account  domain.AccountID
		currency string
	}{
		{account: "acc-default", currency: "GBP"},
		{account: "acc-group", currency: "EUR"},
		{account: "acc-account", currency: "USD"},
	} {
		t.Run(want.currency, func(t *testing.T) {
			page, err := rs.ListBalanceRows(ctx, fwstore.BalanceListFilter{
				RealizedPnl: fwstore.DenominatedDecimalRangeFilter{
					Range:    fwstore.DecimalRangeFilter{Min: &min},
					Currency: want.currency,
				},
			})
			if err != nil {
				t.Fatalf("ListBalanceRows: %v", err)
			}
			if len(page.Rows) != 1 || page.Rows[0].Balance.Account != want.account {
				t.Fatalf("rows = %+v, want account %q", page.Rows, want.account)
			}
			if got := page.Rows[0].Balance.AccountCurrency; got != want.currency {
				t.Fatalf("account currency = %q, want %q", got, want.currency)
			}
		})
	}
}

func TestBalanceEmptyAmountsDefaultToZero(t *testing.T) {
	ctx, rs := seedBalanceFixtures(t)

	// Zero-value amounts should persist as "0", not an empty string.
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "AAPL",
	}); err != nil {
		t.Fatalf("UpsertBalance(zero amounts): %v", err)
	}
	got, ok, err := rs.GetBalance(ctx, "acc-1", "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance: ok=%v err=%v", ok, err)
	}
	if got.Available != "0" || got.Held != "0" || got.Incoming != "0" || got.RealizedPnl != "0" {
		t.Fatalf("zero amounts not stored as 0: %+v", got)
	}
}
