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

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/store"
)

func TestBackupExportCarriesVersionAndScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedBackupStore(t, ctx, s, "backup note")

	archive, err := s.ExportBackup(ctx, backup.Scope{
		Sections: []backup.Section{
			backup.SectionAccountsGroups,
			backup.SectionPositions,
		},
		Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}},
		Positions: backup.EntitySelector{
			Accounts: []string{"acc-1"},
		},
	})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.Manifest.Format != backup.Format {
		t.Fatalf("format = %q, want %q", archive.Manifest.Format, backup.Format)
	}
	if archive.Manifest.FormatVersion != backup.CurrentFormatVersion {
		t.Fatalf("format version = %d, want %d",
			archive.Manifest.FormatVersion, backup.CurrentFormatVersion)
	}
	if archive.Manifest.SchemaVersion != 3 {
		t.Fatalf("schema version = %d, want 3", archive.Manifest.SchemaVersion)
	}
	if len(archive.Data.Accounts) != 1 || archive.Data.Accounts[0].ID != "acc-1" {
		t.Fatalf("unexpected accounts: %+v", archive.Data.Accounts)
	}
	if len(archive.Data.Balances) != 1 ||
		archive.Data.Balances[0].Account != "acc-1" {
		t.Fatalf("unexpected balances: %+v", archive.Data.Balances)
	}
}

func TestBackupRestoreInsertMissingKeepsExisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openStore(t)
	seedBackupStore(t, ctx, target, "target")
	summary, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeInsertMissing,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.Skipped[backup.SectionAccountsGroups] == 0 {
		t.Fatalf("want skipped account/group rows, got %+v", summary)
	}
	account, ok, err := target.GetAccount(ctx, domain.DefaultTenant, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Notes != "target" {
		t.Fatalf("notes = %q, want target", account.Notes)
	}
}

func TestBackupRestoreOverwriteReplacesExisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openStore(t)
	seedBackupStore(t, ctx, target, "target")
	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	account, ok, err := target.GetAccount(ctx, domain.DefaultTenant, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Notes != "from backup" {
		t.Fatalf("notes = %q, want from backup", account.Notes)
	}
}

func TestBackupRestoreOverwriteUpdatesOrdersWithoutDeletingChildren(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	sourceOrder := seedBackupOrder(t, ctx, source, "source", "101")
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openStore(t)
	seedBackupStore(t, ctx, target, "target")
	targetOrder := seedBackupOrder(t, ctx, target, "target", "99")
	extraEvent, err := target.AppendOrderEvent(ctx, domain.OrderEvent{
		OrderID:   targetOrder.Order.ID,
		Type:      domain.OrderEventCancelled,
		Source:    domain.SourceAPI,
		Principal: "target-extra-event",
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent extra: %v", err)
	}
	extraTrade, err := target.CreateTrade(ctx, domain.Trade{
		OrderID:    targetOrder.Order.ID,
		Tenant:     domain.DefaultTenant,
		Account:    "acc-1",
		Source:     domain.SourceAPI,
		Principal:  "target-extra-trade",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Quantity:   "0.25",
		Price:      "102",
	})
	if err != nil {
		t.Fatalf("CreateTrade extra: %v", err)
	}

	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	got, err := target.GetOrder(ctx, domain.DefaultTenant, targetOrder.Order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Order.Principal != "source-order" || got.Order.Price != "101" {
		t.Fatalf("order not updated from archive: %+v", got.Order)
	}
	if !hasOrderEvent(got.Events, sourceOrder.Events[0].ID, "source-event") {
		t.Fatalf("archived event not applied: %+v", got.Events)
	}
	if !hasOrderEvent(got.Events, extraEvent.ID, "target-extra-event") {
		t.Fatalf("pre-existing event not preserved: %+v", got.Events)
	}
	if !hasTrade(got.Trades, sourceOrder.Trades[0].ID, "source-trade") {
		t.Fatalf("archived trade not applied: %+v", got.Trades)
	}
	if !hasTrade(got.Trades, extraTrade.ID, "target-extra-trade") {
		t.Fatalf("pre-existing trade not preserved: %+v", got.Trades)
	}
}

func TestBackupRestoreOverwriteKeepsMissingMarketDataChildren(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openStore(t)
	seedBackupStore(t, ctx, target, "target")
	if err := target.UpdateMarketDataInstanceSettings(
		ctx, "manual-1", "Target Manual", `{"token":"target"}`,
	); err != nil {
		t.Fatalf("UpdateMarketDataInstanceSettings: %v", err)
	}
	if err := target.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		InstanceID:     "manual-1",
		ExternalSymbol: "MSFTUSD",
		BaseAsset:      "MSFT",
		QuoteAsset:     "USD",
		ManualPrice:    "250",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument extra: %v", err)
	}
	now := time.Date(2026, 6, 22, 11, 30, 0, 0, time.UTC)
	if err := target.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		InstanceID:     "manual-1",
		ExternalSymbol: "MSFTUSD",
		BaseAsset:      "MSFT",
		QuoteAsset:     "USD",
		Mark:           "250",
		AsOf:           now,
		ReceivedAt:     now,
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote extra: %v", err)
	}
	beforeInstruments, err := target.ListMarketDataInstruments(ctx, "manual-1")
	if err != nil {
		t.Fatalf("ListMarketDataInstruments before: %v", err)
	}
	beforeQuotes, err := target.ListMarketDataQuotes(ctx, "manual-1")
	if err != nil {
		t.Fatalf("ListMarketDataQuotes before: %v", err)
	}
	if len(beforeInstruments) != 2 || len(beforeQuotes) != 2 {
		t.Fatalf("target fixture counts: instruments=%d quotes=%d",
			len(beforeInstruments), len(beforeQuotes))
	}

	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	instance, ok, err := target.GetMarketDataInstance(ctx, "manual-1")
	if err != nil || !ok {
		t.Fatalf("GetMarketDataInstance: ok=%v err=%v", ok, err)
	}
	if instance.Label != "Manual" || instance.Credentials != "" {
		t.Fatalf("instance not updated from archive: %+v", instance)
	}
	instruments, err := target.ListMarketDataInstruments(ctx, "manual-1")
	if err != nil {
		t.Fatalf("ListMarketDataInstruments after: %v", err)
	}
	quotes, err := target.ListMarketDataQuotes(ctx, "manual-1")
	if err != nil {
		t.Fatalf("ListMarketDataQuotes after: %v", err)
	}
	if len(instruments) != 2 || !hasInstrument(instruments, "MSFTUSD", "250") {
		t.Fatalf("missing live instrument was not preserved: %+v", instruments)
	}
	if len(quotes) != 2 || !hasQuote(quotes, "MSFTUSD", "250") {
		t.Fatalf("missing live quote was not preserved: %+v", quotes)
	}
}

func TestBackupRestoreReplaceAllDeletesSelectedSections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openStore(t)
	seedBackupStore(t, ctx, target, "target")
	if err := target.CreateAccount(ctx, domain.Account{
		Tenant: domain.DefaultTenant,
		ID:     "acc-2",
		Notes:  "delete me",
	}); err != nil {
		t.Fatalf("CreateAccount acc-2: %v", err)
	}
	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	accounts, err := target.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "acc-1" {
		t.Fatalf("accounts after replace = %+v, want only acc-1", accounts)
	}
}

func TestBackupRestoreReplaceAllRestoresExportedMarketData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openStore(t)
	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	instruments, err := target.ListMarketDataInstruments(ctx, "manual-1")
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != 1 || !hasInstrument(instruments, "AAPLUSD", "100") {
		t.Fatalf("instruments after replace = %+v, want AAPLUSD", instruments)
	}
	quotes, err := target.ListMarketDataQuotes(ctx, "manual-1")
	if err != nil {
		t.Fatalf("ListMarketDataQuotes: %v", err)
	}
	if len(quotes) != 1 || !hasQuote(quotes, "AAPLUSD", "100") {
		t.Fatalf("quotes after replace = %+v, want AAPLUSD", quotes)
	}
}

func TestBackupRestoreReplaceAllKeepsUnselectedAccountRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	scope := backup.Scope{
		Sections: []backup.Section{
			backup.SectionAccountsGroups,
			backup.SectionPositions,
		},
		Accounts:  backup.EntitySelector{Accounts: []string{"acc-1"}},
		Positions: backup.EntitySelector{Accounts: []string{"acc-1"}},
	}
	archive, err := source.ExportBackup(ctx, scope)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openStore(t)
	seedBackupStore(t, ctx, target, "target")
	if err := target.CreateAccount(ctx, domain.Account{
		Tenant: domain.DefaultTenant,
		ID:     "acc-2",
		Notes:  "keep me",
	}); err != nil {
		t.Fatalf("CreateAccount acc-2: %v", err)
	}
	if err := target.UpsertBalance(ctx, domain.Balance{
		Tenant:      domain.DefaultTenant,
		Account:     "acc-2",
		Asset:       "USD",
		Available:   "22",
		Held:        "0",
		Incoming:    "0",
		RealizedPnl: "0",
		UpdatedAt:   time.Date(2026, 6, 22, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertBalance acc-2: %v", err)
	}

	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope,
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	account, ok, err := target.GetAccount(ctx, domain.DefaultTenant, "acc-2")
	if err != nil || !ok {
		t.Fatalf("GetAccount acc-2: ok=%v err=%v", ok, err)
	}
	if account.Notes != "keep me" {
		t.Fatalf("acc-2 notes = %q, want keep me", account.Notes)
	}
	balances, err := target.ListBalances(ctx, domain.DefaultTenant, "acc-2", "")
	if err != nil {
		t.Fatalf("ListBalances acc-2: %v", err)
	}
	if len(balances) != 1 || balances[0].Available != "22" {
		t.Fatalf("acc-2 balances = %+v, want preserved", balances)
	}
}

func TestBackupRestoreRejectsUnknownMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openStore(t)
	seedBackupStore(t, ctx, target, "target")
	_, err = target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreMode("bogus"),
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup error = %v, want ErrInvalid", err)
	}
	account, ok, err := target.GetAccount(ctx, domain.DefaultTenant, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Notes != "target" {
		t.Fatalf("notes = %q, want target", account.Notes)
	}
}

func TestBackupRestoreRejectsSchemaMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openStore(t)
	seedBackupStore(t, ctx, source, "from backup")
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	archive.Manifest.SchemaVersion++

	target := openStore(t)
	_, err = target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup error = %v, want ErrInvalid", err)
	}
}

func TestBackupRestoreRollsBackOnFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	target := openStore(t)
	seedBackupStore(t, ctx, target, "target")
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	archive := backup.NewArchive(now, "test", 3, backup.Scope{All: true}, backup.Data{
		Groups: []domain.AccountGroup{{
			Tenant: domain.DefaultTenant,
			ID:     "grp-rollback",
		}},
		Accounts: []domain.Account{{
			Tenant:  domain.DefaultTenant,
			ID:      "acc-rollback",
			GroupID: "grp-rollback",
			Notes:   "partial insert must roll back",
		}},
		Adjustments: []domain.AccountAdjustmentRecord{{
			ID:      100,
			Tenant:  domain.DefaultTenant,
			Account: "acc-rollback",
			At:      now,
			Source:  domain.SourceAPI,
			Request: domain.AdjustmentRequest{Asset: "USD"},
		}},
	})

	_, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want failure")
	}
	account, ok, err := target.GetAccount(ctx, domain.DefaultTenant, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount acc-1: ok=%v err=%v", ok, err)
	}
	if account.Notes != "target" {
		t.Fatalf("acc-1 notes = %q, want target", account.Notes)
	}
	if _, ok, err := target.GetAccount(
		ctx, domain.DefaultTenant, "acc-rollback",
	); err != nil || ok {
		t.Fatalf("rollback account ok=%v err=%v, want absent", ok, err)
	}
}

func TestBackupRestoreSkipsOrphanMarketDataQuotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 12, 30, 0, 0, time.UTC)
	archive := backup.Archive{
		Manifest: backup.Manifest{
			CreatedAt:     now,
			Format:        backup.Format,
			Source:        "test",
			SchemaVersion: 3,
			FormatVersion: backup.CurrentFormatVersion,
			Sections:      backup.AllSections,
		},
		Data: backup.Data{
			MarketDataQuotes: []domain.MarketDataQuote{{
				InstanceID:     "missing-instance",
				ExternalSymbol: "AAPLUSD",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Mark:           "100",
				AsOf:           now,
				ReceivedAt:     now,
			}},
		},
	}

	for _, mode := range []backup.RestoreMode{
		backup.RestoreModeInsertMissing,
		backup.RestoreModeOverwrite,
		backup.RestoreModeReplaceAll,
	} {
		t.Run(string(mode), func(t *testing.T) {
			target := openStore(t)
			summary, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: backup.Scope{All: true},
				Mode:  mode,
			})
			if err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			if summary.Skipped[backup.SectionMarketDataQuotes] != 1 {
				t.Fatalf("skipped = %+v, want one market-data quote",
					summary.Skipped)
			}
			quotes, listErr := target.ListMarketDataQuotes(ctx, "")
			if listErr != nil {
				t.Fatalf("ListMarketDataQuotes: %v", listErr)
			}
			if len(quotes) != 0 {
				t.Fatalf("orphan market-data quotes inserted: %+v", quotes)
			}
		})
	}
}

func TestBackupRestoreCountsMixedMarketDataQuotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 12, 45, 0, 0, time.UTC)
	archive := backup.NewArchive(now, "test", 3, backup.Scope{All: true},
		backup.Data{
			MarketDataInstances: []domain.MarketDataInstance{{
				ID:      "manual-1",
				Type:    domain.MarketDataProviderBYO,
				Label:   "Manual",
				Enabled: true,
			}},
			MarketDataInstruments: []domain.MarketDataInstrument{{
				InstanceID:     "manual-1",
				ExternalSymbol: "AAPLUSD",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				ManualPrice:    "100",
				Enabled:        true,
			}},
			MarketDataQuotes: []domain.MarketDataQuote{
				{
					InstanceID:     "manual-1",
					ExternalSymbol: "AAPLUSD",
					BaseAsset:      "AAPL",
					QuoteAsset:     "USD",
					Mark:           "100",
					AsOf:           now,
					ReceivedAt:     now,
				},
				{
					InstanceID:     "missing-instance",
					ExternalSymbol: "MSFTUSD",
					BaseAsset:      "MSFT",
					QuoteAsset:     "USD",
					Mark:           "250",
					AsOf:           now,
					ReceivedAt:     now,
				},
			},
		})

	target := openStore(t)
	summary, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.Applied[backup.SectionMarketDataQuotes] != 1 ||
		summary.Skipped[backup.SectionMarketDataQuotes] != 1 {
		t.Fatalf("quote summary = %+v, want applied=1 skipped=1", summary)
	}
	quotes, err := target.ListMarketDataQuotes(ctx, "")
	if err != nil {
		t.Fatalf("ListMarketDataQuotes: %v", err)
	}
	if len(quotes) != 1 || !hasQuote(quotes, "AAPLUSD", "100") {
		t.Fatalf("quotes = %+v, want only valid AAPLUSD", quotes)
	}
}

func TestBackupRestoreReplaceAllQuotesOnlySkipsMissingTargetInstruments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 12, 50, 0, 0, time.UTC)
	target := openStore(t)
	if err := target.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		ID:      "manual-1",
		Type:    domain.MarketDataProviderBYO,
		Label:   "Manual",
		Enabled: true,
	}); err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if err := target.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		InstanceID:     "manual-1",
		ExternalSymbol: "AAPLUSD",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		ManualPrice:    "100",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	if err := target.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		InstanceID:     "manual-1",
		ExternalSymbol: "AAPLUSD",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Mark:           "100",
		AsOf:           now,
		ReceivedAt:     now,
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}
	archive := backup.NewArchive(now, "test", 3,
		backup.Scope{Sections: []backup.Section{backup.SectionMarketDataQuotes}},
		backup.Data{MarketDataQuotes: []domain.MarketDataQuote{{
			InstanceID:     "manual-1",
			ExternalSymbol: "MSFTUSD",
			BaseAsset:      "MSFT",
			QuoteAsset:     "USD",
			Mark:           "250",
			AsOf:           now,
			ReceivedAt:     now,
		}}})

	summary, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionMarketDataQuotes}},
		Mode:  backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.Applied[backup.SectionMarketDataQuotes] != 0 ||
		summary.Skipped[backup.SectionMarketDataQuotes] != 1 {
		t.Fatalf("quote summary = %+v, want applied=0 skipped=1", summary)
	}
	quotes, err := target.ListMarketDataQuotes(ctx, "")
	if err != nil {
		t.Fatalf("ListMarketDataQuotes: %v", err)
	}
	if len(quotes) != 0 {
		t.Fatalf("quotes after quotes-only replace = %+v, want none", quotes)
	}
}

func TestBackupRestoreRejectsOrphanMarketDataInstrumentsAndRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 13, 0, 0, 0, time.UTC)
	for _, mode := range []backup.RestoreMode{
		backup.RestoreModeInsertMissing,
		backup.RestoreModeOverwrite,
		backup.RestoreModeReplaceAll,
	} {
		t.Run(string(mode), func(t *testing.T) {
			target := openStore(t)
			archive := backup.NewArchive(now, "test", 3,
				backup.Scope{All: true}, backup.Data{
					Accounts: []domain.Account{{
						Tenant: domain.DefaultTenant,
						ID:     "acc-rollback",
					}},
					MarketDataInstruments: []domain.MarketDataInstrument{{
						InstanceID:     "missing-instance",
						ExternalSymbol: "AAPLUSD",
						BaseAsset:      "AAPL",
						QuoteAsset:     "USD",
						Enabled:        true,
					}},
				})
			_, err := target.RestoreBackup(ctx, archive,
				backup.RestoreOptions{
					Scope: backup.Scope{All: true},
					Mode:  mode,
				})
			if err == nil {
				t.Fatal("RestoreBackup succeeded, want dependent row failure")
			}
			_, ok, getErr := target.GetAccount(
				ctx, domain.DefaultTenant, "acc-rollback",
			)
			if getErr != nil || ok {
				t.Fatalf("rollback account ok=%v err=%v, want absent",
					ok, getErr)
			}
		})
	}
}

func TestBackupRestoreDropsOrphanActivityChildrenBeforeSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 13, 30, 0, 0, time.UTC)
	archive := backup.NewArchive(now, "test", 3, backup.Scope{All: true},
		backup.Data{
			Accounts: []domain.Account{{
				Tenant: domain.DefaultTenant,
				ID:     "acc-preserved",
			}},
			OrderEvents: []domain.OrderEvent{{
				ID:      10,
				OrderID: 999,
				At:      now,
				Type:    domain.OrderEventSubmitted,
				Source:  domain.SourceAPI,
			}},
			Trades: []domain.Trade{{
				ID:         20,
				OrderID:    999,
				Tenant:     domain.DefaultTenant,
				Account:    "acc-preserved",
				At:         now,
				Source:     domain.SourceAPI,
				BaseAsset:  "AAPL",
				QuoteAsset: "USD",
				Side:       domain.OrderSideBuy,
				Quantity:   "1",
				Price:      "100",
			}},
		})

	target := openStore(t)
	summary, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.Applied[backup.SectionActivityHistory] != 0 {
		t.Fatalf("activity summary = %+v, want no child rows applied", summary)
	}
	if _, ok, err := target.GetAccount(
		ctx, domain.DefaultTenant, "acc-preserved",
	); err != nil || !ok {
		t.Fatalf("GetAccount acc-preserved: ok=%v err=%v", ok, err)
	}
	events, err := target.ListOrderEvents(ctx, domain.DefaultTenant, 999)
	if err != nil {
		t.Fatalf("ListOrderEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("orphan events restored: %+v", events)
	}
	trades, err := target.ListTrades(ctx, domain.DefaultTenant, "", "", 10)
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(trades) != 0 {
		t.Fatalf("orphan trades restored: %+v", trades)
	}
}

func TestBackupRestoreReplaceAllResetsAutoSequences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	target := openStore(t)
	archive := backup.NewArchive(now, "test", 3, backup.Scope{All: true},
		backup.Data{
			Accounts: []domain.Account{{
				Tenant: domain.DefaultTenant,
				ID:     "acc-seq",
			}},
			Adjustments: []domain.AccountAdjustmentRecord{
				acceptedAdjustmentRecord(75, "acc-seq", now),
			},
			Orders: []domain.Order{{
				ID:          100,
				Tenant:      domain.DefaultTenant,
				Account:     "acc-seq",
				At:          now,
				Source:      domain.SourceAPI,
				Principal:   "restore",
				BaseAsset:   "AAPL",
				QuoteAsset:  "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "1",
				Price:       "100",
				Status:      domain.OrderStatusSubmitted,
			}},
			OrderEvents: []domain.OrderEvent{{
				ID:      150,
				OrderID: 100,
				At:      now,
				Type:    domain.OrderEventSubmitted,
				Source:  domain.SourceAPI,
			}},
			Trades: []domain.Trade{{
				ID:         200,
				OrderID:    100,
				Tenant:     domain.DefaultTenant,
				Account:    "acc-seq",
				At:         now,
				Source:     domain.SourceAPI,
				BaseAsset:  "AAPL",
				QuoteAsset: "USD",
				Side:       domain.OrderSideBuy,
				Quantity:   "1",
				Price:      "100",
			}},
			Audit: []domain.AuditRow{{
				ID:     250,
				At:     now,
				Actor:  "restore",
				Action: domain.AuditActionRestoreBackup,
				Tenant: domain.DefaultTenant,
				Source: domain.SourceAPI,
			}},
		})

	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	order, err := target.CreateOrder(ctx, domain.Order{
		Tenant:      domain.DefaultTenant,
		Account:     "acc-seq",
		Source:      domain.SourceAPI,
		BaseAsset:   "MSFT",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Status:      domain.OrderStatusSubmitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.ID <= 100 {
		t.Fatalf("new order id = %d, want > 100", order.ID)
	}
	event, err := target.AppendOrderEvent(ctx, domain.OrderEvent{
		OrderID: order.ID,
		Type:    domain.OrderEventSubmitted,
		Source:  domain.SourceAPI,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if event.ID <= 150 {
		t.Fatalf("new event id = %d, want > 150", event.ID)
	}
	trade, err := target.CreateTrade(ctx, domain.Trade{
		OrderID:    order.ID,
		Tenant:     domain.DefaultTenant,
		Account:    "acc-seq",
		Source:     domain.SourceAPI,
		BaseAsset:  "MSFT",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Quantity:   "1",
		Price:      "100",
	})
	if err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	if trade.ID <= 200 {
		t.Fatalf("new trade id = %d, want > 200", trade.ID)
	}
	adjustment, err := target.AppendAdjustment(ctx,
		acceptedAdjustmentRecord(0, "acc-seq", now))
	if err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}
	if adjustment.ID <= 75 {
		t.Fatalf("new adjustment id = %d, want > 75", adjustment.ID)
	}
	if err := target.AppendAudit(ctx, store.AuditEntry{
		Actor:  "test",
		Action: domain.AuditActionHydrate,
		Tenant: domain.DefaultTenant,
		Source: domain.SourceAPI,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	rows, err := target.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if !hasAuditIDGreaterThan(rows, 250) {
		t.Fatalf("audit rows = %+v, want a new id > 250", rows)
	}
}

func TestBackupRestoreVersionFixtureIntoCurrentSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join("..", "backup", "testdata", "v1_full.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var archive backup.Archive
	if err := json.Unmarshal(raw, &archive); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	target := openStore(t)
	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	account, ok, err := target.GetAccount(ctx, domain.DefaultTenant, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.GroupID != "grp-1" || account.Notes != "primary" {
		t.Fatalf("account not preserved: %+v", account)
	}
	balances, err := target.ListBalances(ctx, domain.DefaultTenant, "acc-1", "")
	if err != nil {
		t.Fatalf("ListBalances: %v", err)
	}
	if len(balances) != 1 || balances[0].Available != "10" {
		t.Fatalf("balances not preserved: %+v", balances)
	}
	instances, err := target.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "manual-1" {
		t.Fatalf("market-data instances not preserved: %+v", instances)
	}
}

type backupOrderFixture struct {
	Order  domain.Order
	Events []domain.OrderEvent
	Trades []domain.Trade
}

func seedBackupOrder(
	t *testing.T,
	ctx context.Context,
	s store.Store,
	label string,
	price string,
) backupOrderFixture {
	t.Helper()
	order, err := s.CreateOrder(ctx, domain.Order{
		Tenant:      domain.DefaultTenant,
		Account:     "acc-1",
		Source:      domain.SourceAPI,
		Principal:   label + "-order",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       price,
		Status:      domain.OrderStatusSubmitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder %s: %v", label, err)
	}
	event, err := s.AppendOrderEvent(ctx, domain.OrderEvent{
		OrderID:   order.ID,
		Type:      domain.OrderEventSubmitted,
		Source:    domain.SourceAPI,
		Principal: label + "-event",
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent %s: %v", label, err)
	}
	trade, err := s.CreateTrade(ctx, domain.Trade{
		OrderID:    order.ID,
		Tenant:     domain.DefaultTenant,
		Account:    "acc-1",
		Source:     domain.SourceAPI,
		Principal:  label + "-trade",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Quantity:   "1",
		Price:      price,
	})
	if err != nil {
		t.Fatalf("CreateTrade %s: %v", label, err)
	}
	return backupOrderFixture{
		Order:  order,
		Events: []domain.OrderEvent{event},
		Trades: []domain.Trade{trade},
	}
}

func hasOrderEvent(events []domain.OrderEvent, id int64, principal string) bool {
	for _, event := range events {
		if event.ID == id && event.Principal == principal {
			return true
		}
	}
	return false
}

func hasTrade(trades []domain.Trade, id int64, principal string) bool {
	for _, trade := range trades {
		if trade.ID == id && trade.Principal == principal {
			return true
		}
	}
	return false
}

func hasInstrument(
	instruments []domain.MarketDataInstrument,
	symbol string,
	manualPrice string,
) bool {
	for _, instrument := range instruments {
		if instrument.ExternalSymbol == symbol &&
			instrument.ManualPrice == manualPrice {
			return true
		}
	}
	return false
}

func hasQuote(quotes []domain.MarketDataQuote, symbol string, mark string) bool {
	for _, quote := range quotes {
		if quote.ExternalSymbol == symbol && quote.Mark == mark {
			return true
		}
	}
	return false
}

func hasAuditIDGreaterThan(rows []domain.AuditRow, threshold int64) bool {
	for _, row := range rows {
		if row.ID > threshold {
			return true
		}
	}
	return false
}

func acceptedAdjustmentRecord(
	id int64,
	account domain.AccountID,
	at time.Time,
) domain.AccountAdjustmentRecord {
	return domain.AccountAdjustmentRecord{
		ID:        id,
		Tenant:    domain.DefaultTenant,
		Account:   account,
		At:        at,
		Source:    domain.SourceAPI,
		Principal: "test",
		Request: domain.AdjustmentRequest{
			Asset: "USD",
		},
		Accepted: &domain.AdjustmentOutcomeAccepted{
			BalanceDelta:      "1",
			BalanceResult:     "1",
			HeldDelta:         "0",
			HeldResult:        "0",
			IncomingDelta:     "0",
			IncomingResult:    "0",
			RealizedPnlDelta:  "0",
			RealizedPnlResult: "0",
		},
	}
}

func seedBackupStore(
	t *testing.T,
	ctx context.Context,
	s store.Store,
	accountNote string,
) {
	t.Helper()
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	if err := s.CreateGroup(ctx, domain.AccountGroup{
		Tenant: domain.DefaultTenant,
		ID:     "grp-1",
		Notes:  "group",
	}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := s.CreateAccount(ctx, domain.Account{
		Tenant:  domain.DefaultTenant,
		ID:      "acc-1",
		GroupID: "grp-1",
		Notes:   accountNote,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.UpsertBalance(ctx, domain.Balance{
		Tenant:      domain.DefaultTenant,
		Account:     "acc-1",
		Asset:       "USD",
		Available:   "10",
		Held:        "0",
		Incoming:    "0",
		RealizedPnl: "0",
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	if err := s.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	if err := s.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		ID:      "manual-1",
		Type:    domain.MarketDataProviderBYO,
		Label:   "Manual",
		Enabled: true,
	}); err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if err := s.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		InstanceID:     "manual-1",
		ExternalSymbol: "AAPLUSD",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		ManualPrice:    "100",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	if err := s.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		InstanceID:     "manual-1",
		ExternalSymbol: "AAPLUSD",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Mark:           "100",
		AsOf:           now,
		ReceivedAt:     now,
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}
}
