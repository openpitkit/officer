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

package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

func TestMigrateArchiveFixtures(t *testing.T) {
	fixtures := []struct {
		file          string
		formatVersion int
	}{
		{file: "v1_full.json", formatVersion: 1},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", fixture.file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var archive Archive
			if err := json.Unmarshal(raw, &archive); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			if archive.Manifest.FormatVersion != fixture.formatVersion {
				t.Fatalf("fixture version = %d, want %d",
					archive.Manifest.FormatVersion, fixture.formatVersion)
			}
			migrated, err := MigrateArchive(archive)
			if err != nil {
				t.Fatalf("MigrateArchive: %v", err)
			}
			if migrated.Manifest.FormatVersion != CurrentFormatVersion {
				t.Fatalf("migrated version = %d, want %d",
					migrated.Manifest.FormatVersion, CurrentFormatVersion)
			}
			assertFixtureDataPreserved(t, migrated)
		})
	}
}

func TestMigrateArchiveRejectsInvalidVersions(t *testing.T) {
	base := Archive{Manifest: Manifest{
		Format:        Format,
		FormatVersion: CurrentFormatVersion,
	}}
	tests := []struct {
		name   string
		mutate func(*Archive)
	}{
		{
			name: "bad format",
			mutate: func(a *Archive) {
				a.Manifest.Format = "other"
			},
		},
		{
			name: "too old",
			mutate: func(a *Archive) {
				a.Manifest.FormatVersion = MinSupportedFormatVersion - 1
			},
		},
		{
			name: "future",
			mutate: func(a *Archive) {
				a.Manifest.FormatVersion = CurrentFormatVersion + 1
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := base
			tt.mutate(&archive)
			if _, err := MigrateArchive(archive); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("MigrateArchive error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestFilterDataDropsActivityOrphans(t *testing.T) {
	data := Data{
		Accounts: []domain.Account{{
			Tenant: domain.DefaultTenant,
			ID:     "acc-1",
		}},
		Orders: []domain.Order{{
			ID:      1,
			Tenant:  domain.DefaultTenant,
			Account: "acc-2",
		}},
		OrderEvents: []domain.OrderEvent{{
			ID:      10,
			OrderID: 2,
		}},
		Trades: []domain.Trade{{
			ID:      20,
			OrderID: 2,
			Tenant:  domain.DefaultTenant,
			Account: "acc-1",
		}},
	}
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionActivityHistory},
		Accounts: EntitySelector{
			Accounts: []string{"acc-1"},
		},
	})
	if len(filtered.Orders) != 0 ||
		len(filtered.OrderEvents) != 0 ||
		len(filtered.Trades) != 0 {
		t.Fatalf("activity orphans preserved: %+v", filtered)
	}
}

func TestFilterDataAccountsGroupsByAccountAndGroupSelectors(t *testing.T) {
	data := filterFixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionAccountsGroups},
		Accounts: EntitySelector{
			Accounts: []string{" acc-1 ", "acc-1"},
			Groups:   []string{"grp-2"},
		},
	})
	if got := accountIDs(filtered.Accounts); !sameStrings(got, []string{"acc-1", "acc-2"}) {
		t.Fatalf("accounts = %v, want acc-1 and acc-2", got)
	}
	if got := groupIDs(filtered.Groups); !sameStrings(got, []string{"grp-1", "grp-2"}) {
		t.Fatalf("groups = %v, want grp-1 and grp-2", got)
	}
	if len(filtered.Balances) != 0 || len(filtered.Limits) != 0 ||
		len(filtered.Orders) != 0 {
		t.Fatalf("unexpected non-account sections: %+v", filtered)
	}
}

func TestFilterDataPositionsUsePositionsSelectorOnly(t *testing.T) {
	data := filterFixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionPositions},
		Accounts: EntitySelector{
			Accounts: []string{"acc-1"},
		},
		Positions: EntitySelector{
			Groups: []string{"grp-2"},
		},
	})
	if got := balanceAccounts(filtered.Balances); !sameStrings(got, []string{"acc-2"}) {
		t.Fatalf("balance accounts = %v, want only acc-2", got)
	}
	if len(filtered.Accounts) != 0 || len(filtered.Groups) != 0 ||
		len(filtered.Limits) != 0 {
		t.Fatalf("unexpected non-position sections: %+v", filtered)
	}
}

func TestFilterDataLimitsRespectAccountSelectorsAndGlobalRows(t *testing.T) {
	data := filterFixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionRiskLimits},
		Accounts: EntitySelector{
			Accounts: []string{"acc-1"},
		},
	})
	if got := limitAccounts(filtered.Limits); !sameStrings(got, []string{"acc-1"}) {
		t.Fatalf("filtered limit accounts = %v, want only acc-1", got)
	}

	filtered = FilterData(data, Scope{
		Sections: []Section{SectionRiskLimits},
	})
	if got := limitAccounts(filtered.Limits); !sameStrings(got, []string{"", "acc-1", "acc-2"}) {
		t.Fatalf("unfiltered limit accounts = %v, want global, acc-1, acc-2", got)
	}
}

func TestFilterDataActivityHistoryKeepsOnlySelectedOrderChildren(t *testing.T) {
	data := filterFixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionActivityHistory},
		Accounts: EntitySelector{
			Groups: []string{"grp-1"},
		},
	})
	if got := orderIDs(filtered.Orders); !sameInt64s(got, []int64{1}) {
		t.Fatalf("orders = %v, want order 1", got)
	}
	if got := orderEventIDs(filtered.OrderEvents); !sameInt64s(got, []int64{10}) {
		t.Fatalf("order events = %v, want event 10", got)
	}
	if got := tradeIDs(filtered.Trades); !sameInt64s(got, []int64{100}) {
		t.Fatalf("trades = %v, want trade 100", got)
	}
	if got := adjustmentAccounts(filtered.Adjustments); !sameStrings(got, []string{"acc-1"}) {
		t.Fatalf("adjustments = %v, want acc-1", got)
	}
}

func TestFilterDataAuditRespectsAccountSelectorsAndGlobalRows(t *testing.T) {
	data := filterFixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionAuditLog},
		Accounts: EntitySelector{
			Accounts: []string{"acc-1"},
		},
	})
	if got := auditAccounts(filtered.Audit); !sameStrings(got, []string{"acc-1"}) {
		t.Fatalf("filtered audit accounts = %v, want only acc-1", got)
	}

	filtered = FilterData(data, Scope{
		Sections: []Section{SectionAuditLog},
	})
	if got := auditAccounts(filtered.Audit); !sameStrings(got, []string{"", "acc-1", "acc-2"}) {
		t.Fatalf("unfiltered audit accounts = %v, want global, acc-1, acc-2", got)
	}
}

func TestFilterDataMarketDataAndSettingsSectionsAreIndependent(t *testing.T) {
	data := filterFixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionMarketDataQuotes},
	})
	if len(filtered.MarketDataQuotes) != 1 ||
		len(filtered.MarketDataInstances) != 0 ||
		len(filtered.MarketDataInstruments) != 0 {
		t.Fatalf("quotes-only market-data = %+v", filtered)
	}
	filtered = FilterData(data, Scope{
		Sections: []Section{
			SectionMarketData,
			SectionGeneralSettings,
		},
	})
	if len(filtered.MarketDataInstances) != 1 ||
		len(filtered.MarketDataInstruments) != 1 ||
		len(filtered.MarketDataQuotes) != 0 {
		t.Fatalf("settings-only market-data = %+v", filtered)
	}
	if filtered.McpAccess["submit_order"] != true ||
		filtered.McpAccess["delete_limit"] != false {
		t.Fatalf("mcp access = %+v, want all settings", filtered.McpAccess)
	}
}

func assertFixtureDataPreserved(t *testing.T, archive Archive) {
	t.Helper()
	data := archive.Data
	if len(data.Accounts) != 1 || data.Accounts[0].ID != "acc-1" {
		t.Fatalf("accounts not preserved: %+v", data.Accounts)
	}
	if len(data.Groups) != 1 || data.Groups[0].ID != "grp-1" {
		t.Fatalf("groups not preserved: %+v", data.Groups)
	}
	if len(data.Balances) != 1 || data.Balances[0].Available != "10" {
		t.Fatalf("balances not preserved: %+v", data.Balances)
	}
	if len(data.Limits) != 1 || len(data.Limits[0].Values) != 1 {
		t.Fatalf("limits not preserved: %+v", data.Limits)
	}
	if len(data.MarketDataInstances) != 1 ||
		len(data.MarketDataInstruments) != 1 ||
		len(data.MarketDataQuotes) != 1 {
		t.Fatalf("market-data not preserved: %+v", data)
	}
	if len(data.Orders) != 1 || len(data.OrderEvents) != 1 ||
		len(data.Trades) != 1 || len(data.Adjustments) != 1 {
		t.Fatalf("activity history not preserved: %+v", data)
	}
	if len(data.Audit) != 1 || data.Audit[0].Action != "create_account" {
		t.Fatalf("audit not preserved: %+v", data.Audit)
	}
	if data.McpAccess["submit_order"] != true {
		t.Fatalf("mcp access not preserved: %+v", data.McpAccess)
	}
}

func filterFixtureData() Data {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	return Data{
		Accounts: []domain.Account{
			{Tenant: domain.DefaultTenant, ID: "acc-1", GroupID: "grp-1"},
			{Tenant: domain.DefaultTenant, ID: "acc-2", GroupID: "grp-2"},
			{Tenant: domain.DefaultTenant, ID: "acc-3"},
		},
		Groups: []domain.AccountGroup{
			{Tenant: domain.DefaultTenant, ID: "grp-1"},
			{Tenant: domain.DefaultTenant, ID: "grp-2"},
			{Tenant: domain.DefaultTenant, ID: "grp-unused"},
		},
		Balances: []domain.Balance{
			{Tenant: domain.DefaultTenant, Account: "acc-1", Asset: "USD"},
			{Tenant: domain.DefaultTenant, Account: "acc-2", Asset: "EUR"},
			{Tenant: domain.DefaultTenant, Account: "acc-3", Asset: "GBP"},
		},
		Limits: []domain.Limit{
			{Target: domain.LimitTarget{
				Tenant: domain.DefaultTenant, Policy: "rate", Scope: "broker",
			}, Values: []domain.LimitValue{{Kind: "max", Value: "10"}}},
			{Target: domain.LimitTarget{
				Tenant: domain.DefaultTenant, Policy: "rate",
				Scope: "account", Account: "acc-1",
			}, Values: []domain.LimitValue{{Kind: "max", Value: "11"}}},
			{Target: domain.LimitTarget{
				Tenant: domain.DefaultTenant, Policy: "rate",
				Scope: "account", Account: "acc-2",
			}, Values: []domain.LimitValue{{Kind: "max", Value: "12"}}},
		},
		MarketDataInstances: []domain.MarketDataInstance{{
			ID: "manual-1", Type: domain.MarketDataProviderBYO,
		}},
		MarketDataInstruments: []domain.MarketDataInstrument{{
			InstanceID: "manual-1", ExternalSymbol: "AAPLUSD",
		}},
		MarketDataQuotes: []domain.MarketDataQuote{{
			InstanceID: "manual-1", ExternalSymbol: "AAPLUSD",
		}},
		McpAccess: map[string]bool{
			"delete_limit": false,
			"submit_order": true,
		},
		Adjustments: []domain.AccountAdjustmentRecord{
			{ID: 1, Tenant: domain.DefaultTenant, Account: "acc-1"},
			{ID: 2, Tenant: domain.DefaultTenant, Account: "acc-2"},
		},
		Orders: []domain.Order{
			{ID: 1, Tenant: domain.DefaultTenant, Account: "acc-1", At: now},
			{ID: 2, Tenant: domain.DefaultTenant, Account: "acc-2", At: now},
		},
		OrderEvents: []domain.OrderEvent{
			{ID: 10, OrderID: 1, At: now},
			{ID: 20, OrderID: 2, At: now},
			{ID: 30, OrderID: 99, At: now},
		},
		Trades: []domain.Trade{
			{ID: 100, OrderID: 1, Tenant: domain.DefaultTenant, Account: "acc-1", At: now},
			{ID: 200, OrderID: 2, Tenant: domain.DefaultTenant, Account: "acc-2", At: now},
			{ID: 300, OrderID: 99, Tenant: domain.DefaultTenant, Account: "acc-1", At: now},
		},
		Audit: []domain.AuditRow{
			{ID: 1, Tenant: domain.DefaultTenant, Action: domain.AuditActionHydrate},
			{ID: 2, Tenant: domain.DefaultTenant, Account: "acc-1", Action: domain.AuditActionSetNotes},
			{ID: 3, Tenant: domain.DefaultTenant, Account: "acc-2", Action: domain.AuditActionSetNotes},
		},
	}
}

func accountIDs(accounts []domain.Account) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, account.ID.String())
	}
	return out
}

func groupIDs(groups []domain.AccountGroup) []string {
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.ID)
	}
	return out
}

func balanceAccounts(balances []domain.Balance) []string {
	out := make([]string, 0, len(balances))
	for _, balance := range balances {
		out = append(out, balance.Account.String())
	}
	return out
}

func limitAccounts(limits []domain.Limit) []string {
	out := make([]string, 0, len(limits))
	for _, limit := range limits {
		out = append(out, limit.Target.Account.String())
	}
	return out
}

func adjustmentAccounts(adjustments []domain.AccountAdjustmentRecord) []string {
	out := make([]string, 0, len(adjustments))
	for _, adjustment := range adjustments {
		out = append(out, adjustment.Account.String())
	}
	return out
}

func auditAccounts(rows []domain.AuditRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Account.String())
	}
	return out
}

func orderIDs(orders []domain.Order) []int64 {
	out := make([]int64, 0, len(orders))
	for _, order := range orders {
		out = append(out, order.ID)
	}
	return out
}

func orderEventIDs(events []domain.OrderEvent) []int64 {
	out := make([]int64, 0, len(events))
	for _, event := range events {
		out = append(out, event.ID)
	}
	return out
}

func tradeIDs(trades []domain.Trade) []int64 {
	out := make([]int64, 0, len(trades))
	for _, trade := range trades {
		out = append(out, trade.ID)
	}
	return out
}

func sameStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, value := range got {
		counts[value]++
	}
	for _, value := range want {
		if counts[value] == 0 {
			return false
		}
		counts[value]--
	}
	return true
}

func sameInt64s(got []int64, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[int64]int, len(want))
	for _, value := range got {
		counts[value]++
	}
	for _, value := range want {
		if counts[value] == 0 {
			return false
		}
		counts[value]--
	}
	return true
}
