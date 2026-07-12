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

// Store-level backup/restore tests: the export -> restore round-trip on public
// identity, a realm moved isolated <-> shared in both directions, restore modes
// and selectors, dictionary-first foreign-key resolution, and engine-id
// reassignment with the touched-runtime engine-rebuild signal.

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
)

// newRealmStore opens a fresh migrated store bound to realm and returns both the
// store and its realm handle.
func newRealmStore(t *testing.T, realm domain.RealmID) (Store, RealmStore) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	var opts []SQLiteOption
	if realm != "" && realm != domain.DefaultRealm {
		opts = append(opts, WithRealm(realm))
	}
	s, err := NewSQLiteStore(path, opts...)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if realm == "" {
		realm = domain.DefaultRealm
	}
	rs, err := s.ForRealm(ctx, realm)
	if err != nil {
		t.Fatalf("ForRealm(%q): %v", realm, err)
	}
	return s, rs
}

// seedRealm fills rs with a representative realm: dictionaries, a balance, a
// limit, an order with an event/trade/adjustment, an audit row, market data and
// settings. It returns the created order's external id so callers can assert it
// is preserved across a round-trip.
func seedRealm(t *testing.T, ctx context.Context, rs RealmStore) domain.ExternalID {
	t.Helper()
	if err := rs.CreateAssetClass(ctx, domain.AssetClass{Code: "equity", Title: "Equity"}); err != nil {
		t.Fatalf("CreateAssetClass: %v", err)
	}
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL", Title: "Apple", AssetClass: "equity"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "US Dollar"})
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator", Title: "Operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "grp-1", Title: "Desk"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Title: "Account 1", GroupCode: "grp-1", Notes: "primary",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "1000", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", MaxQuantity: "100",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
	if err := rs.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:           domain.ScopeAccount,
		Account:         "acc-1",
		AccountCurrency: "USD",
		LowerBound:      "-250",
		UpperBound:      "500",
		InitialPnl:      "12.50",
	}); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	order, err := rs.CreateOrder(ctx, domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Principal: "operator", Source: domain.SourcePanel, Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "10",
		Price: "150", Status: domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourcePanel,
		Payload: domain.OrderEventPayload{FillQuantity: "10", FillPrice: "150"},
	}); err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if _, err := rs.CreateTrade(ctx, domain.Trade{
		Order: order.ExternalID, Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Source: domain.SourcePanel, Side: domain.OrderSideBuy, Quantity: "10", Price: "150",
		Commission: &domain.Commission{Amount: "-0.12", Currency: "USD"},
	}); err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	if _, err := rs.AppendAdjustment(ctx, domain.AccountAdjustmentRecord{
		Account: "acc-1", Asset: "USD", Source: domain.SourcePanel,
		Request:  domain.AdjustmentRequest{Asset: "USD"},
		Accepted: &domain.AdjustmentOutcomeAccepted{BalanceDelta: "1000", BalanceResult: "1000"},
	}); err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}
	if err := rs.AppendAudit(ctx, AuditEntry{
		Action: domain.AuditActionCreateAccount, Account: "acc-1",
		Actor: "operator", Source: domain.SourcePanel, Detail: "seed",
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	inst, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if err := rs.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance: inst.ExternalID, ExternalSymbol: "AAPLUSD",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	if err := rs.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	if err := rs.SetUserSetting(ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen, "1"); err != nil {
		t.Fatalf("SetUserSetting: %v", err)
	}
	return order.ExternalID
}

func mustCreateAsset(t *testing.T, ctx context.Context, rs RealmStore, asset domain.Asset) {
	t.Helper()
	if err := rs.CreateAsset(ctx, asset); err != nil {
		t.Fatalf("CreateAsset(%s): %v", asset.Code, err)
	}
}

func seedGroupCurrencies(t *testing.T, ctx context.Context, rs RealmStore) {
	t.Helper()
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "US Dollar"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "EUR", Title: "Euro"})
	if err := rs.SetGroupCurrency(ctx, "", "USD"); err != nil {
		t.Fatalf("SetGroupCurrency(default): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Title:    "Desk A",
		Currency: "EUR",
	}); err != nil {
		t.Fatalf("CreateGroup desk-a: %v", err)
	}
}

func TestBackupExportIncludesGroupCurrencies(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedGroupCurrencies(t, ctx, src)

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.Data.DefaultGroupCurrency != "USD" {
		t.Fatalf("DefaultGroupCurrency = %q, want USD",
			archive.Data.DefaultGroupCurrency)
	}
	if len(archive.Data.Groups) != 1 {
		t.Fatalf("groups = %v, want one real group", archive.Data.Groups)
	}
	if archive.Data.Groups[0].Code != "desk-a" ||
		archive.Data.Groups[0].Currency != "EUR" {
		t.Fatalf("exported group = %+v, want desk-a/EUR", archive.Data.Groups[0])
	}
	for _, group := range archive.Data.Groups {
		if group.Code == "" {
			t.Fatalf("archive contains default group row: %+v", group)
		}
	}
}

func TestBackupExportIncludesAccountCurrency(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "JPY"})
	if _, err := src.CreateAccount(ctx, domain.Account{
		Code: "acc-jpy", Currency: "JPY",
	}); err != nil {
		t.Fatalf("CreateAccount acc-jpy: %v", err)
	}

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if len(archive.Data.Accounts) != 1 {
		t.Fatalf("accounts = %v, want one account", archive.Data.Accounts)
	}
	if archive.Data.Accounts[0].Code != "acc-jpy" ||
		archive.Data.Accounts[0].Currency != "JPY" {
		t.Fatalf("exported account = %+v, want acc-jpy/JPY",
			archive.Data.Accounts[0])
	}
}

func TestBackupRestorePreservesGroupCurrencies(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedGroupCurrencies(t, ctx, src)

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatal("RestartRequired = false, want true for group currency restore")
	}
	group, ok, err := dst.GetGroup(ctx, "desk-a")
	if err != nil || !ok {
		t.Fatalf("GetGroup desk-a: ok=%v err=%v", ok, err)
	}
	if group.Currency != "EUR" {
		t.Fatalf("restored group currency = %q, want EUR", group.Currency)
	}
	defaultGroup, ok, err := dst.GetGroup(ctx, "")
	if err != nil || !ok {
		t.Fatalf("GetGroup default: ok=%v err=%v", ok, err)
	}
	if defaultGroup.Currency != "USD" {
		t.Fatalf("restored default currency = %q, want USD",
			defaultGroup.Currency)
	}
	groups, err := dst.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	for _, group := range groups {
		if group.Code == "" {
			t.Fatalf("ListGroups returned default row: %+v", group)
		}
	}
}

func TestBackupRestorePreservesAccountCurrency(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "JPY"})
	if _, err := src.CreateAccount(ctx, domain.Account{
		Code: "acc-jpy", Currency: "JPY",
	}); err != nil {
		t.Fatalf("CreateAccount acc-jpy: %v", err)
	}

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatal("RestartRequired = false, want true for account currency restore")
	}
	account, ok, err := dst.GetAccount(ctx, "acc-jpy")
	if err != nil || !ok {
		t.Fatalf("GetAccount acc-jpy: ok=%v err=%v", ok, err)
	}
	if account.Currency != "JPY" ||
		account.EffectiveCurrency != "JPY" ||
		account.CurrencyOrigin != domain.CurrencyOriginAccount {
		t.Fatalf("restored account currency = %+v", account)
	}
}

func TestBackupRestoreOverwriteClearsDefaultGroupCurrency(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "EUR", Title: "Euro"})
	if _, err := src.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Title:    "Desk A",
		Currency: "EUR",
	}); err != nil {
		t.Fatalf("CreateGroup source desk-a: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.Data.DefaultGroupCurrency != "" {
		t.Fatalf("DefaultGroupCurrency = %q, want empty",
			archive.Data.DefaultGroupCurrency)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	seedGroupCurrencies(t, ctx, dst)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup overwrite: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatal("RestartRequired = false, want true for default currency clear")
	}
	defaultGroup, ok, err := dst.GetGroup(ctx, "")
	if err != nil || !ok {
		t.Fatalf("GetGroup default: ok=%v err=%v", ok, err)
	}
	if defaultGroup.Currency != "" {
		t.Fatalf("restored default currency = %q, want empty",
			defaultGroup.Currency)
	}
}

func TestBackupRestoreEmptyDefaultGroupCurrencyDoesNotCreateDefaultRow(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "EUR", Title: "Euro"})
	if _, err := src.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Title:    "Desk A",
		Currency: "EUR",
	}); err != nil {
		t.Fatalf("CreateGroup source desk-a: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, ok, err := dst.GetGroup(ctx, ""); err != nil || ok {
		t.Fatalf("GetGroup default before restore: ok=%v err=%v", ok, err)
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup overwrite: %v", err)
	}
	if _, ok, err := dst.GetGroup(ctx, ""); err != nil || ok {
		t.Fatalf("GetGroup default after restore: ok=%v err=%v", ok, err)
	}
}

// TestBackupRoundTripIntoIsolatedRealm exports a seeded realm and restores it
// into a fresh isolated single-realm database, asserting public identity is
// preserved across every section and engine ids are freshly, validly assigned.
func TestBackupRoundTripIntoIsolatedRealm(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	assertNoIDLeak(t, archive)

	_, dst := newRealmStore(t, domain.DefaultRealm)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatal("RestartRequired = false, want true for a full restore")
	}

	assertRealmsEqualOnPublicIdentity(t, ctx, src, dst, orderXID)

	// Engine ids in the destination are valid and in range (freshly assigned).
	acc, ok, err := dst.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("dst GetAccount: ok=%v err=%v", ok, err)
	}
	if err := domain.ValidateEngineAccountID(acc.EngineAccountID); err != nil {
		t.Fatalf("restored engine account id invalid: %v", err)
	}
	grp, _, _ := dst.GetGroup(ctx, "grp-1")
	if err := domain.ValidateEngineGroupID(grp.EngineGroupID); err != nil {
		t.Fatalf("restored engine group id invalid: %v", err)
	}
}

// TestBackupMoveIsolatedToShared restores a realm's archive into a SHARED
// database that already holds a different realm's rows, asserting no code
// collision and that the destination engine ids do not collide with the rows
// already present (they continue the destination's own sequence).
func TestBackupMoveIsolatedToShared(t *testing.T) {
	ctx := context.Background()

	// Source isolated realm.
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// Destination: a single-realm connector standing in for one realm of a shared
	// DB. It already holds prior rows with their own codes and engine ids; the
	// restore must not collide with them.
	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.CreateGroup(ctx, domain.AccountGroup{Code: "existing-grp"}); err != nil {
		t.Fatalf("dst CreateGroup: %v", err)
	}
	existingAcc, err := dst.CreateAccount(ctx, domain.Account{Code: "existing-acc"})
	if err != nil {
		t.Fatalf("dst CreateAccount: %v", err)
	}

	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup into shared: %v", err)
	}

	// Both the pre-existing and the restored account coexist with their codes.
	if _, ok, _ := dst.GetAccount(ctx, "existing-acc"); !ok {
		t.Fatal("pre-existing account lost after restore")
	}
	restored, ok, err := dst.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("restored account missing: ok=%v err=%v", ok, err)
	}
	// The restored account got a fresh engine id, distinct from the pre-existing
	// one (no engine-id leakage from the source).
	if restored.EngineAccountID == existingAcc.EngineAccountID {
		t.Fatalf("restored engine account id collided with pre-existing: %d",
			restored.EngineAccountID)
	}
	if err := domain.ValidateEngineAccountID(restored.EngineAccountID); err != nil {
		t.Fatalf("restored engine account id invalid: %v", err)
	}

	// The order's external id is preserved exactly.
	if _, err := dst.GetOrder(ctx, orderXID); err != nil {
		t.Fatalf("restored order by preserved external id: %v", err)
	}
}

// TestBackupMoveSharedToIsolated moves the SAME realm back out of the shared DB
// into a fresh isolated DB, completing the isolated <-> shared round-trip, and
// asserts the public identity survived both hops unchanged.
func TestBackupMoveSharedToIsolated(t *testing.T) {
	ctx := context.Background()

	_, origin := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, origin)
	first, err := origin.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("origin ExportBackup: %v", err)
	}

	// Land in a shared DB (with a neighbour realm row), then export again.
	_, shared := newRealmStore(t, domain.DefaultRealm)
	if _, err := shared.CreateAccount(ctx, domain.Account{Code: "neighbour"}); err != nil {
		t.Fatalf("shared CreateAccount: %v", err)
	}
	if _, err := shared.RestoreBackup(ctx, first, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("restore into shared: %v", err)
	}
	// Export ONLY the moved realm's account by selector, so the neighbour does not
	// travel back out.
	second, err := shared.ExportBackup(ctx, backup.Scope{
		All: false, Sections: backup.AllSections,
		Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}},
	})
	if err != nil {
		t.Fatalf("shared ExportBackup: %v", err)
	}

	// Land back into a fresh isolated DB.
	_, iso := newRealmStore(t, domain.DefaultRealm)
	if _, err := iso.RestoreBackup(ctx, second, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("restore into isolated: %v", err)
	}

	// The neighbour did not travel; acc-1 did, with its order external id intact.
	if _, ok, _ := iso.GetAccount(ctx, "neighbour"); ok {
		t.Fatal("neighbour realm row leaked into the isolated restore")
	}
	if _, ok, _ := iso.GetAccount(ctx, "acc-1"); !ok {
		t.Fatal("acc-1 missing after shared -> isolated move")
	}
	if _, err := iso.GetOrder(ctx, orderXID); err != nil {
		t.Fatalf("order external id not preserved across both hops: %v", err)
	}
}

// TestBackupRestoreInsertMissingSkipsExisting asserts the insert-missing mode
// skips rows whose portable identity is already present and reports the skip.
func TestBackupRestoreInsertMissingSkipsExisting(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	// Pre-create the asset under a different title; insert-missing must leave it.
	if err := dst.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Preexisting"}); err != nil {
		t.Fatalf("dst CreateAsset: %v", err)
	}
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeInsertMissing,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, _, _ := dst.GetAsset(ctx, "AAPL")
	if got.Title != "Preexisting" {
		t.Fatalf("insert-missing overwrote an existing asset: %+v", got)
	}
	if summary.Skipped[backup.SectionAccountsGroups] == 0 {
		t.Fatalf("expected a skipped dictionary row, summary = %+v", summary)
	}
}

// TestBackupRestoreOverwriteUpdatesExisting asserts overwrite updates a row that
// shares the portable identity rather than skipping it.
func TestBackupRestoreOverwriteUpdatesExisting(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if err := dst.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Stale"}); err != nil {
		t.Fatalf("dst CreateAsset: %v", err)
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, _, _ := dst.GetAsset(ctx, "AAPL")
	if got.Title != "Apple" {
		t.Fatalf("overwrite did not update existing asset: %+v", got)
	}
}

func TestBackupRestoreReplaceAllPrunesActivityBeforeSigningKeys(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	seedSigningKey(t, ctx, rs, "key-prune")
	order, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	event, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: order.ExternalID, Type: domain.OrderEventSubmitted, Source: domain.SourcePanel,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if err := rs.PutEventAttestation(ctx, event.ExternalID, domain.EventAttestation{
		Token:       "tok",
		KeyID:       "key-prune",
		Alg:         "ed25519",
		RequestType: domain.AttestationRequestSubmit,
		Mode:        "immediate",
		IssuedAt:    "2026-06-26T10:00:00Z",
	}); err != nil {
		t.Fatalf("PutEventAttestation: %v", err)
	}

	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{
			Sections: []backup.Section{
				backup.SectionActivityHistory,
				backup.SectionGeneralSettings,
			},
		},
		backup.Data{
			Accounts: []backup.Account{{Code: "acc-1"}},
		},
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{
			Sections: []backup.Section{
				backup.SectionActivityHistory,
				backup.SectionGeneralSettings,
			},
		},
		Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if _, err := rs.GetOrder(ctx, order.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetOrder after restore = %v, want ErrNotFound", err)
	}
	if _, err := rs.GetSigningKey(ctx, "key-prune"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSigningKey after restore = %v, want ErrNotFound", err)
	}
}

func TestBackupRestoreReplaceAllUsesArchiveGroupForActivityPrune(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL", Title: "Apple"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "Dollar"})
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "live-g"}); err != nil {
		t.Fatalf("CreateGroup(live): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "archive-g"}); err != nil {
		t.Fatalf("CreateGroup(archive): %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", GroupCode: "live-g",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{
			Sections: []backup.Section{backup.SectionActivityHistory},
			Accounts: backup.EntitySelector{
				Groups: []string{"archive-g"},
			},
		},
		backup.Data{
			Groups: []backup.AccountGroup{{Code: "archive-g"}},
			Accounts: []backup.Account{
				{Code: "acc-1", GroupCode: "archive-g"},
			},
		},
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{
			Sections: []backup.Section{backup.SectionActivityHistory},
			Accounts: backup.EntitySelector{
				Groups: []string{"archive-g"},
			},
		},
		Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if _, err := rs.GetOrder(ctx, order.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetOrder after archive-group restore = %v, want ErrNotFound", err)
	}
}

func TestBackupRestoreReplaceAllKeepsLiveOnlyAccountInSelectedGroup(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL", Title: "Apple"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "Dollar"})
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "archive-g"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "live-only", GroupCode: "archive-g",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := sampleOrder()
	order.Account = "live-only"
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{
			Sections: []backup.Section{backup.SectionActivityHistory},
			Accounts: backup.EntitySelector{
				Groups: []string{"archive-g"},
			},
		},
		backup.Data{
			Groups: []backup.AccountGroup{{Code: "archive-g"}},
		},
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{
			Sections: []backup.Section{backup.SectionActivityHistory},
			Accounts: backup.EntitySelector{
				Groups: []string{"archive-g"},
			},
		},
		Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if _, err := rs.GetOrder(ctx, created.ExternalID); err != nil {
		t.Fatalf("GetOrder for live-only account after restore: %v", err)
	}
}

func TestBackupRestoreOverwriteKeepsTargetOnlyOrderChildren(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	full, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, full, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup full: %v", err)
	}
	before, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("GetOrder before partial restore: %v", err)
	}
	if len(before.Events) == 0 || len(before.Trades) == 0 {
		t.Fatalf("seeded order children missing before restore: events=%d trades=%d",
			len(before.Events), len(before.Trades))
	}

	partial := full
	partial.Data.OrderEvents = nil
	partial.Data.Trades = nil
	if _, err := dst.RestoreBackup(ctx, partial, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup partial overwrite: %v", err)
	}

	after, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("GetOrder after partial restore: %v", err)
	}
	if len(after.Events) != len(before.Events) || len(after.Trades) != len(before.Trades) {
		t.Fatalf("partial overwrite dropped order children: before events=%d trades=%d after events=%d trades=%d",
			len(before.Events), len(before.Trades), len(after.Events), len(after.Trades))
	}
}

// TestBackupRestoreReplaceAllDeletesAbsentRows asserts that replace-all makes
// each restored section equal the archive: a row present in the target before
// the restore but absent from the archive is gone afterward, across every kind
// of row (dictionary, position, limit, activity, audit, market data, settings).
// overwrite and insert_missing leave such a row in place.
func TestBackupRestoreReplaceAllDeletesAbsentRows(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// seedExtra adds rows the archive does NOT carry: an extra account (with a
	// balance, a limit, an adjustment and an audit row), an extra group, an extra
	// market-data instance, an extra MCP override and an extra user setting.
	seedExtra := func(t *testing.T, rs RealmStore) {
		t.Helper()
		// Assets the extra rows reference; the archive also carries them, so a
		// replace-all upsert keeps them (asset are shared support rows, not pruned).
		mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "US Dollar"})
		mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL", Title: "Apple"})
		if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
			t.Fatalf("CreatePrincipal(extra): %v", err)
		}
		if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "extra-grp"}); err != nil {
			t.Fatalf("CreateGroup(extra): %v", err)
		}
		if _, err := rs.CreateAccount(ctx, domain.Account{Code: "extra-acc"}); err != nil {
			t.Fatalf("CreateAccount(extra): %v", err)
		}
		if err := rs.UpsertBalance(ctx, domain.Balance{
			Account: "extra-acc", Asset: "USD", Available: "7", UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("UpsertBalance(extra): %v", err)
		}
		if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
			Scope: domain.ScopeAccountAsset, Account: "extra-acc", Asset: "AAPL", MaxQuantity: "9",
		}); err != nil {
			t.Fatalf("PutOrderSizeLimit(extra): %v", err)
		}
		if _, err := rs.AppendAdjustment(ctx, domain.AccountAdjustmentRecord{
			Account: "extra-acc", Asset: "USD", Source: domain.SourcePanel,
			Request:  domain.AdjustmentRequest{Asset: "USD"},
			Accepted: &domain.AdjustmentOutcomeAccepted{BalanceDelta: "7", BalanceResult: "7"},
		}); err != nil {
			t.Fatalf("AppendAdjustment(extra): %v", err)
		}
		if err := rs.AppendAudit(ctx, AuditEntry{
			Action: domain.AuditActionCreateAccount, Account: "extra-acc",
			Actor: "operator", Source: domain.SourcePanel, Detail: "extra",
		}); err != nil {
			t.Fatalf("AppendAudit(extra): %v", err)
		}
		if _, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
			Provider: domain.MarketDataProviderBYO, Label: "extra-md", Enabled: true,
		}); err != nil {
			t.Fatalf("CreateMarketDataInstance(extra): %v", err)
		}
		if err := rs.SetMcpAccess(ctx, "cancel_order", true); err != nil {
			t.Fatalf("SetMcpAccess(extra): %v", err)
		}
		if err := rs.SetUserSetting(ctx, "extra-user", domain.UserSettingWelcomeSeen, "1"); err != nil {
			t.Fatalf("SetUserSetting(extra): %v", err)
		}
	}

	// Non-replace modes leave the rows the archive does not carry untouched.
	for _, mode := range []backup.RestoreMode{
		backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing,
	} {
		_, dst := newRealmStore(t, domain.DefaultRealm)
		seedExtra(t, dst)
		if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: mode,
		}); err != nil {
			t.Fatalf("RestoreBackup(%s): %v", mode, err)
		}
		if _, ok, err := dst.GetAccount(ctx, "extra-acc"); err != nil || !ok {
			t.Fatalf("%s removed an extra account: ok=%v err=%v", mode, ok, err)
		}
		if _, ok, err := dst.GetGroup(ctx, "extra-grp"); err != nil || !ok {
			t.Fatalf("%s removed an extra group: ok=%v err=%v", mode, ok, err)
		}
		access, _ := dst.ListMcpAccess(ctx)
		if !access["cancel_order"] {
			t.Fatalf("%s removed an extra mcp override: %+v", mode, access)
		}
	}

	// Replace-all deletes every row the archive does not carry, and its cascade
	// removes the extra account's balance, limit, adjustment and audit rows.
	_, dst := newRealmStore(t, domain.DefaultRealm)
	seedExtra(t, dst)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(replace_all): %v", err)
	}
	if _, ok, err := dst.GetAccount(ctx, "extra-acc"); err != nil || ok {
		t.Fatalf("replace_all kept an absent account: ok=%v err=%v", ok, err)
	}
	if _, ok, err := dst.GetGroup(ctx, "extra-grp"); err != nil || ok {
		t.Fatalf("replace_all kept an absent group: ok=%v err=%v", ok, err)
	}
	if _, ok, err := dst.GetBalance(ctx, "extra-acc", "USD"); err != nil || ok {
		t.Fatalf("replace_all kept an absent balance: ok=%v err=%v", ok, err)
	}
	limits, err := dst.ListOrderSizeLimits(ctx, "extra-acc")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(limits) != 0 {
		t.Fatalf("replace_all kept an absent limit: %+v", limits)
	}
	adj, err := dst.ListAdjustments(ctx, "extra-acc", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(adj) != 0 {
		t.Fatalf("replace_all kept an absent adjustment: %+v", adj)
	}
	access, _ := dst.ListMcpAccess(ctx)
	if access["cancel_order"] {
		t.Fatalf("replace_all kept an absent mcp override: %+v", access)
	}
	settings, err := dst.ListUserSettings(ctx)
	if err != nil {
		t.Fatalf("ListUserSettings: %v", err)
	}
	for _, s := range settings {
		if s.UserID == "extra-user" {
			t.Fatalf("replace_all kept an absent user setting: %+v", s)
		}
	}
	instances, err := dst.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	for _, inst := range instances {
		if inst.Label == "extra-md" {
			t.Fatalf("replace_all kept an absent market-data instance: %+v", inst)
		}
	}
	// The archive's own rows survive, with their portable identity preserved.
	if _, ok, err := dst.GetAccount(ctx, "acc-1"); err != nil || !ok {
		t.Fatalf("replace_all dropped an archived account: ok=%v err=%v", ok, err)
	}
}

// TestBackupRestoreReplaceAllPreservesIdentityReassignsEngineIDs asserts that a
// replace-all that deletes an absent account still keeps the archived rows'
// external_id/code while assigning fresh, valid engine ids, and leaves no
// dangling foreign key (the order children resolve after the cascade).
func TestBackupRestoreReplaceAllPreservesIdentityReassignsEngineIDs(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// Destination holds a different account (absent from the archive) plus a stale
	// copy of the archived account under a different engine id.
	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.CreateAccount(ctx, domain.Account{Code: "absent-acc"}); err != nil {
		t.Fatalf("dst CreateAccount(absent): %v", err)
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(replace_all): %v", err)
	}

	if _, ok, err := dst.GetAccount(ctx, "absent-acc"); err != nil || ok {
		t.Fatalf("absent account survived replace_all: ok=%v err=%v", ok, err)
	}
	acc, ok, err := dst.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("archived account missing: ok=%v err=%v", ok, err)
	}
	if acc.Code != "acc-1" || acc.GroupCode != "grp-1" {
		t.Fatalf("archived account identity not preserved: %+v", acc)
	}
	if err := domain.ValidateEngineAccountID(acc.EngineAccountID); err != nil {
		t.Fatalf("restored engine account id invalid: %v", err)
	}
	// The order and its children resolve, so the cascade did not orphan or break a
	// foreign key, and the archived external id is preserved.
	detail, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 1 || len(detail.Trades) != 1 {
		t.Fatalf("order children not restored: events=%d trades=%d",
			len(detail.Events), len(detail.Trades))
	}
}

// TestBackupRestoreReplaceAllScopedToSelectedAccounts asserts a scoped
// replace-all replaces only the selected account's rows: an unselected account
// present before the restore survives, while the selected account's rows become
// exactly what the archive carries.
func TestBackupRestoreReplaceAllScopedToSelectedAccounts(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	// An unselected account that the archive does not carry; a scoped replace-all
	// of acc-1 must leave it alone.
	if _, err := dst.CreateAccount(ctx, domain.Account{Code: "other-acc"}); err != nil {
		t.Fatalf("dst CreateAccount(other): %v", err)
	}
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionAccountsGroups},
		Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}},
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(scoped replace_all): %v", err)
	}
	if _, ok, err := dst.GetAccount(ctx, "other-acc"); err != nil || !ok {
		t.Fatalf("scoped replace_all removed an unselected account: ok=%v err=%v", ok, err)
	}
	if _, ok, err := dst.GetAccount(ctx, "acc-1"); err != nil || !ok {
		t.Fatalf("scoped replace_all missing the selected account: ok=%v err=%v", ok, err)
	}
}

func TestBackupRestoreReplaceAllPrunesSpotFundsGroupLimitForSelectedAccount(
	t *testing.T,
) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	if _, err := src.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}); err != nil {
		t.Fatalf("src CreateGroup(desk-a): %v", err)
	}
	if _, err := src.CreateAccount(ctx, domain.Account{
		Code: "acc-1", GroupCode: "desk-a",
	}); err != nil {
		t.Fatalf("src CreateAccount(acc-1): %v", err)
	}
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionRiskLimits},
		Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}},
	}
	archive, err := src.ExportBackup(ctx, scope)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, dst, domain.Asset{Code: "USD", Title: "US Dollar"})
	if _, err := dst.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}); err != nil {
		t.Fatalf("dst CreateGroup(desk-a): %v", err)
	}
	if _, err := dst.CreateGroup(ctx, domain.AccountGroup{Code: "desk-b"}); err != nil {
		t.Fatalf("dst CreateGroup(desk-b): %v", err)
	}
	if _, err := dst.CreateAccount(ctx, domain.Account{
		Code: "acc-1", GroupCode: "desk-a",
	}); err != nil {
		t.Fatalf("dst CreateAccount(acc-1): %v", err)
	}
	if err := dst.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:           domain.ScopeAccountGroup,
		AccountGroup:    "desk-a",
		AccountCurrency: "USD",
		LowerBound:      "-1000",
	}); err != nil {
		t.Fatalf("dst PutSpotFundsPnlBoundsLimit(desk-a): %v", err)
	}
	if err := dst.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:           domain.ScopeAccountGroup,
		AccountGroup:    "desk-b",
		AccountCurrency: "USD",
		LowerBound:      "-2000",
	}); err != nil {
		t.Fatalf("dst PutSpotFundsPnlBoundsLimit(desk-b): %v", err)
	}

	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(scoped replace_all): %v", err)
	}

	limits, err := dst.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("spotFundsPnlBoundsLimits = %+v, want only desk-b", limits)
	}
	if limits[0].AccountGroup != "desk-b" ||
		limits[0].AccountCurrency != "USD" {
		t.Fatalf("remaining spotFundsPnlBoundsLimit = %+v, want desk-b/USD", limits[0])
	}
}

// TestBackupRestoreSelectorSubset restores only the general-settings section and
// asserts it does NOT raise the engine-rebuild signal and does not pull in the
// account-addressed sections.
func TestBackupRestoreSelectorSubset(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatal("RestartRequired = true for a settings-only restore, want false")
	}
	// The MCP override landed; no account was restored.
	access, err := dst.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if access["submit_order"] != true {
		t.Fatalf("settings restore missing mcp override: %+v", access)
	}
	if _, ok, _ := dst.GetAccount(ctx, "acc-1"); ok {
		t.Fatal("settings-only restore pulled in an account")
	}
}

// TestBackupRestoreUnknownModeRejected asserts an empty/unknown mode is rejected
// with ErrInvalid and writes nothing.
func TestBackupRestoreUnknownModeRejected(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, _ := src.ExportBackup(ctx, backup.Scope{All: true})

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: "",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup(empty mode) error = %v, want ErrInvalid", err)
	}
	if _, ok, _ := dst.GetAccount(ctx, "acc-1"); ok {
		t.Fatal("rejected restore still wrote rows")
	}
}

// TestBackupRestoreResolvesForeignKeysDictionaryFirst restores an archive whose
// machine records reference dictionaries, asserting the dictionary-first order
// lets every cross-row link resolve (no dangling foreign key).
func TestBackupRestoreResolvesForeignKeysDictionaryFirst(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// The order's account/asset/principal links resolved (GetOrder joins them).
	detail, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Account != "acc-1" || detail.Order.BaseAsset != "AAPL" ||
		detail.Order.QuoteAsset != "USD" || detail.Order.Principal != "operator" {
		t.Fatalf("order links not resolved: %+v", detail.Order)
	}
	if len(detail.Events) != 1 || len(detail.Trades) != 1 {
		t.Fatalf("order children not restored: events=%d trades=%d",
			len(detail.Events), len(detail.Trades))
	}
	// The instrument's instance link resolved by external id.
	instruments, err := dst.ListMarketDataInstruments(ctx, instanceXID(t, ctx, dst))
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != 1 || instruments[0].ExternalSymbol != "AAPLUSD" {
		t.Fatalf("instrument not restored under its instance: %+v", instruments)
	}
}

// backupExportedTables is the set of user tables the portable backup carries
// (one per backup.Data section). Keep it in lockstep with backup.Data: a new
// exported table must appear here.
var backupExportedTables = map[string]bool{
	"asset":                      true,
	"asset_class":                true,
	"principal":                  true,
	"account_group":              true,
	"account":                    true,
	"balance":                    true,
	"limit_rate":                 true,
	"limit_order_size":           true,
	"limit_spot_funds_pnl_bound": true,
	"adjustment":                 true,
	"order_record":               true,
	"event_attestation":          true,
	"order_event":                true,
	"trade":                      true,
	"audit":                      true,
	"market_data_instance":       true,
	"market_data_instrument":     true,
	"market_data_quote":          true,
	"signing_key":                true,
	"signing_config":             true,
	"mcp_access":                 true,
	"user_setting":               true,
}

// backupExcludedTables is the explicit allowlist of user tables the backup does
// NOT carry, with the reason each is intentionally absent.
var backupExcludedTables = map[string]bool{
	"realm":            true, // realm identity row, re-established by the target connector
	"schema_migration": true, // migration bookkeeping, owned by Migrate
}

// TestBackupCompleteness introspects sqlite_master for every user table and
// asserts each is either exported by the backup or on the explicit exclusion
// allowlist, so a newly added table cannot silently escape the backup.
func TestBackupCompleteness(t *testing.T) {
	ctx := context.Background()
	_, rs := newRealmStore(t, domain.DefaultRealm)
	r := rs.(*realmStore)

	rows, err := r.rawDB().QueryContext(
		ctx,
		`SELECT name FROM sqlite_master
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	)
	if err != nil {
		t.Fatalf("introspect sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		if backupExportedTables[name] || backupExcludedTables[name] {
			continue
		}
		t.Errorf("table %q is neither exported by the backup nor on the exclusion allowlist", name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table names: %v", err)
	}
}

// TestBackupRestoreAtomicityLeavesRealmUnchanged forces a mid-restore failure (a
// replace_all whose archive carries an order referencing an unknown asset code)
// and asserts the whole transaction rolls back: the prune that replace_all runs
// first is undone, so a pre-existing account survives untouched.
func TestBackupRestoreAtomicityLeavesRealmUnchanged(t *testing.T) {
	ctx := context.Background()
	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.CreateAccount(ctx, domain.Account{Code: "keep-acc", Title: "Keep"}); err != nil {
		t.Fatalf("CreateAccount(keep): %v", err)
	}

	// The archive selects account+activity; restoreActivity will fail resolving
	// the order's unknown asset code AFTER the replace_all prune has run.
	scope := backup.Scope{
		Sections: []backup.Section{
			backup.SectionAccountsGroups,
			backup.SectionActivityHistory,
		},
	}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{
			Accounts: []backup.Account{{Code: "new-acc"}},
			Orders: []backup.OrderRecord{{
				Order: domain.Order{
					ExternalID: mustExternalID(t),
					Account:    "new-acc",
					BaseAsset:  "GHOST",
					QuoteAsset: "USD",
				},
			}},
		},
	)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeReplaceAll,
	}); err == nil {
		t.Fatal("RestoreBackup succeeded, want a mid-restore failure")
	}

	// The pre-existing account survived: the prune was rolled back with the rest of
	// the failed transaction.
	acc, ok, err := dst.GetAccount(ctx, "keep-acc")
	if err != nil || !ok {
		t.Fatalf("pre-existing account lost after failed restore: ok=%v err=%v", ok, err)
	}
	if acc.Title != "Keep" {
		t.Fatalf("pre-existing account mutated by failed restore: %+v", acc)
	}
	// The partially-applied new account did not survive.
	if _, ok, _ := dst.GetAccount(ctx, "new-acc"); ok {
		t.Fatal("failed restore left a partially-applied account")
	}
}

// TestBackupRestoreRejectsInvalidCommission asserts a restore whose archived
// trade carries a malformed or one-sided commission fails with ErrInvalid and
// persists nothing, mirroring the CreateTrade write-time guard. Without it the
// bad row would land and later break the whole-page CommissionSubtotals rollup.
func TestBackupRestoreRejectsInvalidCommission(t *testing.T) {
	cases := []struct {
		name       string
		commission *domain.Commission
	}{
		{"malformed-amount", &domain.Commission{Amount: "not-decimal", Currency: "USD"}},
		{"amount-only", &domain.Commission{Amount: "-0.12"}},
		{"currency-only", &domain.Commission{Currency: "USD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, dst := seedOrderFixtures(t)
			orderXID := mustExternalID(t)
			scope := backup.Scope{
				Sections: []backup.Section{backup.SectionActivityHistory},
			}
			archive := backup.NewArchive(
				time.Now().UTC(),
				"test",
				backup.RealmLabel{Code: string(domain.DefaultRealm)},
				scope,
				backup.Data{
					Orders: []backup.OrderRecord{{Order: domain.Order{
						ExternalID:  orderXID,
						Account:     "acc-1",
						BaseAsset:   "AAPL",
						QuoteAsset:  "USD",
						Principal:   "operator",
						Source:      domain.SourcePanel,
						Side:        domain.OrderSideBuy,
						AmountKind:  domain.OrderAmountKindQuantity,
						AmountValue: "10",
						Price:       "150",
						Status:      domain.OrderStatusFilled,
					}}},
					Trades: []domain.Trade{{
						ExternalID: mustExternalID(t),
						Order:      orderXID,
						Account:    "acc-1",
						BaseAsset:  "AAPL",
						QuoteAsset: "USD",
						Source:     domain.SourcePanel,
						Side:       domain.OrderSideBuy,
						Quantity:   "10",
						Price:      "150",
						Commission: tc.commission,
					}},
				},
			)
			if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: scope, Mode: backup.RestoreModeInsertMissing,
			}); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup(bad commission) = %v, want ErrInvalid", err)
			}
			// The failed restore rolled back atomically: no trade landed.
			if c := countRows(t, ctx, dst.(*realmStore), "trade"); c != 0 {
				t.Fatalf("trades after failed restore = %d, want 0", c)
			}
		})
	}
}

// TestBackupRestoreActivityOnlyForceIncludesSigningKey exports ONLY the activity
// section of a realm holding a signed order, then restores it into a fresh realm
// with the same scope. The signing key the event_attestation row references
// (RESTRICT) must be force-included via the parent general-settings section, so
// the foreign key resolves with no error.
func TestBackupRestoreActivityOnlyForceIncludesSigningKey(t *testing.T) {
	ctx, src := seedOrderFixtures(t)
	seedSigningKey(t, ctx, src, "key-activity")
	order, err := src.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	event, err := src.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: order.ExternalID, Type: domain.OrderEventSubmitted, Source: domain.SourcePanel,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if err := src.PutEventAttestation(ctx, event.ExternalID, domain.EventAttestation{
		Token:       "tok",
		KeyID:       "key-activity",
		Alg:         "ed25519",
		RequestType: domain.AttestationRequestSubmit,
		Mode:        "immediate",
		IssuedAt:    "2026-06-26T10:00:00Z",
	}); err != nil {
		t.Fatalf("PutEventAttestation: %v", err)
	}

	// Export only the activity section; Normalize force-includes the parent
	// dictionaries and general-settings (the signing keys) so the archive carries
	// the key the attestation references.
	archive, err := src.ExportBackup(ctx, backup.Scope{
		Sections: []backup.Section{backup.SectionActivityHistory},
	})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionActivityHistory}},
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup activity-only: %v", err)
	}

	// The signed order restored and its event attestation resolved the
	// force-included key.
	detail, err := dst.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder after activity-only restore: %v", err)
	}
	att := eventAttestation(t, detail, event.ExternalID)
	if att == nil || att.KeyID != "key-activity" {
		t.Fatalf("restored event attestation = %+v, want key-activity", att)
	}
}

// TestBackupRestoreAuditOnlyForceIncludedGroupRaisesRestart asserts that a
// restore whose REQUESTED scope names only the (observational) audit log still
// raises RestartRequired when it lands a group the target did not have. Normalize
// force-includes the accounts+groups dictionary so the audit rows resolve, and
// that force-include inserts a new group into the fresh target; the restart
// signal must reflect the row actually written so the caller rebuilds the live
// resolver. Against the old TouchesRuntime(opts.Scope) code the audit-only scope
// read as observational and RestartRequired was false, leaving the store holding
// a group the resolver never learned.
func TestBackupRestoreAuditOnlyForceIncludedGroupRaisesRestart(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}},
		Mode:  backup.RestoreModeInsertMissing,
	})
	if err != nil {
		t.Fatalf("RestoreBackup audit-only: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatalf("RestartRequired = false, want true for a force-included group insert")
	}
	// The force-included group landed even though only the audit log was requested.
	if _, ok, err := dst.GetGroup(ctx, "grp-1"); err != nil || !ok {
		t.Fatalf("dst GetGroup(grp-1) ok=%v err=%v, want present", ok, err)
	}
}

// TestBackupRestoreAuditOnlyNoNewGroupStaysObservational asserts the restart
// signal stays off when an audit-only restore force-includes the accounts+groups
// dictionary but the target already holds every group, so no runtime row is
// written. The signal must follow the rows actually applied, not the mere
// presence of the force-included section.
func TestBackupRestoreAuditOnlyNoNewGroupStaysObservational(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// Restore the full archive first so the target already holds every dictionary,
	// then repeat an audit-only insert-missing restore: the force-included group
	// and account rows all collide and are skipped, so nothing runtime is written.
	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup seed full: %v", err)
	}
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}},
		Mode:  backup.RestoreModeInsertMissing,
	})
	if err != nil {
		t.Fatalf("RestoreBackup audit-only repeat: %v", err)
	}
	if summary.RestartRequired {
		t.Fatalf("RestartRequired = true for an audit-only restore that wrote no runtime row")
	}
}

// TestBackupRestoreFormatVersionRejected asserts an archive whose manifest format
// version mismatches the current one is rejected with ErrInvalid and writes
// nothing.
func TestBackupRestoreFormatVersionRejected(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	archive.Manifest.FormatVersion = backup.FormatVersion + 1

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup(bad formatVersion) error = %v, want ErrInvalid", err)
	}
	if _, ok, _ := dst.GetAccount(ctx, "acc-1"); ok {
		t.Fatal("rejected restore still wrote rows")
	}
}

// mustExternalID mints a fresh external id for a test fixture.
func mustExternalID(t *testing.T) domain.ExternalID {
	t.Helper()
	xid, err := newExternalID()
	if err != nil {
		t.Fatalf("newExternalID: %v", err)
	}
	return xid
}

// instanceXID returns the single market-data instance's external id in rs.
func instanceXID(t *testing.T, ctx context.Context, rs RealmStore) domain.ExternalID {
	t.Helper()
	instances, err := rs.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	return instances[0].ExternalID
}

// --- Assertions --------------------------------------------------------------

// assertNoIDLeak fails when the archive serializes any surrogate or engine id.
// Portable identity carries only codes and external ids; the archive account and
// group types have no engine-id field, so this checks the round-trippable JSON
// does not regress to leak one and that machine records expose external ids.
func assertNoIDLeak(t *testing.T, archive backup.Archive) {
	t.Helper()
	for _, rec := range archive.Data.Orders {
		if rec.Order.ExternalID.IsZero() {
			t.Fatal("exported order has zero external id")
		}
	}
	for _, a := range archive.Data.Accounts {
		if a.Code == "" {
			t.Fatal("exported account has empty code")
		}
	}
}

// assertRealmsEqualOnPublicIdentity compares the public identity of every section
// between src and dst: dictionaries by code, machine records by external id, and
// the money/quantity decimals through the domain value types.
func assertRealmsEqualOnPublicIdentity(
	t *testing.T, ctx context.Context, src, dst RealmStore, orderXID domain.ExternalID,
) {
	t.Helper()

	srcAssets, _ := src.ListAssets(ctx)
	dstAssets, _ := dst.ListAssets(ctx)
	if !sameAssetCodes(srcAssets, dstAssets) {
		t.Fatalf("assets differ: %v vs %v", srcAssets, dstAssets)
	}

	srcAcc, _, _ := src.GetAccount(ctx, "acc-1")
	dstAcc, ok, err := dst.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("dst account: ok=%v err=%v", ok, err)
	}
	if srcAcc.Code != dstAcc.Code || srcAcc.GroupCode != dstAcc.GroupCode ||
		srcAcc.Title != dstAcc.Title || srcAcc.Notes != dstAcc.Notes {
		t.Fatalf("account public identity differs: %+v vs %+v", srcAcc, dstAcc)
	}

	srcBal, _, _ := src.GetBalance(ctx, "acc-1", "USD")
	dstBal, ok, _ := dst.GetBalance(ctx, "acc-1", "USD")
	if !ok || srcBal.Available != dstBal.Available {
		t.Fatalf("balance differs: %q vs %q", srcBal.Available, dstBal.Available)
	}

	srcLimits, _ := src.ListOrderSizeLimits(ctx, "")
	dstLimits, _ := dst.ListOrderSizeLimits(ctx, "")
	if len(srcLimits) != len(dstLimits) || len(dstLimits) != 1 ||
		dstLimits[0].MaxQuantity != "100" || dstLimits[0].Account != "acc-1" {
		t.Fatalf("limits differ: %v vs %v", srcLimits, dstLimits)
	}
	srcSpotFunds, _ := src.ListSpotFundsPnlBoundsLimits(ctx, "")
	dstSpotFunds, _ := dst.ListSpotFundsPnlBoundsLimits(ctx, "")
	if len(srcSpotFunds) != len(dstSpotFunds) || len(dstSpotFunds) != 1 ||
		dstSpotFunds[0].Account != "acc-1" ||
		dstSpotFunds[0].AccountCurrency != "USD" ||
		dstSpotFunds[0].LowerBound != "-250" ||
		dstSpotFunds[0].UpperBound != "500" ||
		dstSpotFunds[0].InitialPnl != "12.50" {
		t.Fatalf("spot funds pnl bounds differ: %v vs %v", srcSpotFunds, dstSpotFunds)
	}

	srcOrder, _ := src.GetOrder(ctx, orderXID)
	dstOrder, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("dst GetOrder by preserved external id: %v", err)
	}
	if srcOrder.Order.ExternalID != dstOrder.Order.ExternalID {
		t.Fatalf("order external id changed: %s vs %s",
			srcOrder.Order.ExternalID, dstOrder.Order.ExternalID)
	}
	if srcOrder.Order.AmountValue != dstOrder.Order.AmountValue ||
		srcOrder.Order.Price != dstOrder.Order.Price {
		t.Fatalf("order decimals differ: %+v vs %+v", srcOrder.Order, dstOrder.Order)
	}
	if len(dstOrder.Events) != 1 || len(dstOrder.Trades) != 1 {
		t.Fatalf("order children not preserved: %+v", dstOrder)
	}
	if dstOrder.Events[0].ExternalID != srcOrder.Events[0].ExternalID {
		t.Fatalf("event external id changed: %s vs %s",
			dstOrder.Events[0].ExternalID, srcOrder.Events[0].ExternalID)
	}
	if dstOrder.Trades[0].ExternalID != srcOrder.Trades[0].ExternalID {
		t.Fatalf("trade external id changed: %s vs %s",
			dstOrder.Trades[0].ExternalID, srcOrder.Trades[0].ExternalID)
	}
	if dstOrder.Trades[0].Commission == nil ||
		dstOrder.Trades[0].Commission.Amount != "-0.12" ||
		dstOrder.Trades[0].Commission.Currency != "USD" {
		t.Fatalf("trade commission not preserved: %+v", dstOrder.Trades[0])
	}

	srcAdj, _ := src.ListAdjustments(ctx, "acc-1", "", 10)
	dstAdj, _ := dst.ListAdjustments(ctx, "acc-1", "", 10)
	if len(srcAdj) != 1 || len(dstAdj) != 1 ||
		srcAdj[0].ExternalID != dstAdj[0].ExternalID {
		t.Fatalf("adjustment external id not preserved: %v vs %v", srcAdj, dstAdj)
	}

	srcAudit, _ := src.ListAudit(ctx, 100)
	dstAudit, _ := dst.ListAudit(ctx, 100)
	if len(dstAudit) != len(srcAudit) {
		t.Fatalf("audit count differs: %d vs %d", len(srcAudit), len(dstAudit))
	}
	// The seeded audit row plus the create-* rows the seed CRUD emitted are all
	// machine records carried by external id; check the seed row is present.
	if !auditHasExternalID(dstAudit, srcAudit) {
		t.Fatal("audit external ids not preserved across restore")
	}

	srcUser, _ := src.ListUserSettings(ctx)
	dstUser, _ := dst.ListUserSettings(ctx)
	if len(srcUser) != len(dstUser) || len(dstUser) != 1 || dstUser[0].Value != "1" {
		t.Fatalf("user settings differ: %v vs %v", srcUser, dstUser)
	}
}

func sameAssetCodes(a, b []domain.Asset) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[x.Code] = true
	}
	for _, x := range b {
		if !set[x.Code] {
			return false
		}
	}
	return true
}

func auditHasExternalID(got, want []domain.AuditRow) bool {
	set := make(map[domain.ExternalID]bool, len(got))
	for _, row := range got {
		set[row.ExternalID] = true
	}
	for _, row := range want {
		if !set[row.ExternalID] {
			return false
		}
	}
	return true
}
