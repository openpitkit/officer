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

// Unit tests for the realm-portable archive contract: scope/selector filtering,
// dictionary-first carriage, and the touched-runtime engine-rebuild signal. The
// store-level export -> restore round-trip (identity preservation, isolated <->
// shared moves, modes, FK resolution and engine-id reassignment) lives in the
// store package, where a real connector is available.
package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// TestFixtureArchiveDecodes loads the on-disk fixture and asserts it carries the
// portable identity model end to end: dictionaries by code, machine records by
// external id, links by code/external id, no version field.
func TestFixtureArchiveDecodes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "full_archive.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var archive Archive
	if err := json.Unmarshal(raw, &archive); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	if archive.Manifest.Realm.Code != "default" {
		t.Fatalf("realm code = %q, want default", archive.Manifest.Realm.Code)
	}
	data := archive.Data
	if len(data.Assets) != 2 {
		t.Fatalf("assets = %d, want 2", len(data.Assets))
	}
	if len(data.Accounts) != 1 || data.Accounts[0].Code != "acc-1" ||
		data.Accounts[0].GroupCode != "grp-1" {
		t.Fatalf("accounts not portable: %+v", data.Accounts)
	}
	if len(data.Groups) != 1 || data.Groups[0].Code != "grp-1" {
		t.Fatalf("groups not portable: %+v", data.Groups)
	}
	if len(data.OrderSizeLimits) != 1 ||
		data.OrderSizeLimits[0].Account != "acc-1" ||
		data.OrderSizeLimits[0].MaxQuantity != "100" {
		t.Fatalf("limits not portable: %+v", data.OrderSizeLimits)
	}
	if len(data.Orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(data.Orders))
	}
	// The order is addressed by a 22-char external id, and its event/trade link
	// back to it by the same external id.
	orderXID := data.Orders[0].Order.ExternalID
	if orderXID.IsZero() {
		t.Fatal("order external id is zero")
	}
	if len(data.OrderEvents) != 1 || data.OrderEvents[0].Order != orderXID {
		t.Fatalf("order event link not by external id: %+v", data.OrderEvents)
	}
	if len(data.Trades) != 1 || data.Trades[0].Order != orderXID {
		t.Fatalf("trade link not by external id: %+v", data.Trades)
	}
	// Money/quantity assertions use the domain value types' decimal strings.
	if data.Trades[0].Quantity != "10" || data.Trades[0].Price != "150" {
		t.Fatalf("trade decimals = %+v", data.Trades[0])
	}
	if data.McpAccess["submit_order"] != true {
		t.Fatalf("mcp access not preserved: %+v", data.McpAccess)
	}
}

func TestFilterDataAlwaysCarriesDictionaries(t *testing.T) {
	data := fixtureData()
	// A positions-only scope still carries the asset and principal dictionaries so
	// the balance foreign keys resolve on import.
	filtered := FilterData(data, Scope{Sections: []Section{SectionPositions}})
	if len(filtered.AssetClasses) != 1 {
		t.Fatalf("asset classes dropped from positions-only scope: %+v", filtered.AssetClasses)
	}
	if len(filtered.Assets) != 2 {
		t.Fatalf("assets dropped from positions-only scope: %+v", filtered.Assets)
	}
	if len(filtered.Principals) != 1 {
		t.Fatalf("principals dropped from positions-only scope: %+v", filtered.Principals)
	}
	// Positions are account-addressed, so the scope force-includes the
	// accounts+groups dictionary: the balance rows resolve against parent accounts
	// (and their groups) that the archive must therefore carry. With no account
	// narrowing every account and group travels.
	if got := accountCodes(filtered.Accounts); !sameStrings(got, []string{"acc-1", "acc-2", "acc-3"}) {
		t.Fatalf("positions-only scope accounts = %v, want all accounts force-included", got)
	}
	if got := groupCodes(filtered.Groups); !sameStrings(got, []string{"grp-1", "grp-2", "grp-unused"}) {
		t.Fatalf("positions-only scope groups = %v, want all groups force-included", got)
	}
}

func TestFilterDataAccountsGroupsByCode(t *testing.T) {
	data := fixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionAccountsGroups},
		Accounts: EntitySelector{Accounts: []string{" acc-1 ", "acc-1"}, Groups: []string{"grp-2"}},
	})
	if got := accountCodes(filtered.Accounts); !sameStrings(got, []string{"acc-1", "acc-2"}) {
		t.Fatalf("accounts = %v, want acc-1, acc-2", got)
	}
	if got := groupCodes(filtered.Groups); !sameStrings(got, []string{"grp-1", "grp-2"}) {
		t.Fatalf("groups = %v, want grp-1, grp-2", got)
	}
}

func TestFilterDataPositionsUsePositionsSelector(t *testing.T) {
	data := fixtureData()
	filtered := FilterData(data, Scope{
		Sections:  []Section{SectionPositions},
		Accounts:  EntitySelector{Accounts: []string{"acc-1"}},
		Positions: EntitySelector{Groups: []string{"grp-2"}},
	})
	if got := balanceAccounts(filtered.Balances); !sameStrings(got, []string{"acc-2"}) {
		t.Fatalf("balance accounts = %v, want only acc-2", got)
	}
}

func TestFilterDataLimitsRespectAccountSelectorAndGlobalRows(t *testing.T) {
	data := fixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionRiskLimits},
		Accounts: EntitySelector{Accounts: []string{"acc-1"}},
	})
	// With a specific account selector, only that account's barriers travel; the
	// scope-wide (no-account) barrier is dropped, matching the prior behaviour
	// where a global row required an unfiltered selector to be included.
	if got := orderSizeAccounts(filtered.OrderSizeLimits); !sameStrings(got, []string{"acc-1"}) {
		t.Fatalf("filtered limit accounts = %v, want only acc-1", got)
	}

	filtered = FilterData(data, Scope{Sections: []Section{SectionRiskLimits}})
	if got := orderSizeAccounts(filtered.OrderSizeLimits); !sameStrings(got, []string{"", "acc-1", "acc-2"}) {
		t.Fatalf("unfiltered limit accounts = %v, want global, acc-1, acc-2", got)
	}
}

func TestFilterDataActivityHistoryKeepsOnlySelectedOrderChildren(t *testing.T) {
	data := fixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionActivityHistory},
		Accounts: EntitySelector{Groups: []string{"grp-1"}},
	})
	// Only acc-1's order (and its child event/trade) survive; the orphan child of
	// an unselected order is dropped.
	if len(filtered.Orders) != 1 || filtered.Orders[0].Order.Account != "acc-1" {
		t.Fatalf("orders = %+v, want only acc-1's order", filtered.Orders)
	}
	keptXID := filtered.Orders[0].Order.ExternalID
	if len(filtered.OrderEvents) != 1 || filtered.OrderEvents[0].Order != keptXID {
		t.Fatalf("order events = %+v, want only the kept order's", filtered.OrderEvents)
	}
	if len(filtered.Trades) != 1 || filtered.Trades[0].Order != keptXID {
		t.Fatalf("trades = %+v, want only the kept order's", filtered.Trades)
	}
}

func TestFilterDataAuditRespectsAccountSelectorAndGlobalRows(t *testing.T) {
	data := fixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionAuditLog},
		Accounts: EntitySelector{Accounts: []string{"acc-1"}},
	})
	// A specific account selector drops the global (empty-account) audit row, the
	// same rule the limit and balance filters apply.
	if got := auditAccounts(filtered.Audit); !sameStrings(got, []string{"acc-1"}) {
		t.Fatalf("filtered audit accounts = %v, want only acc-1", got)
	}
}

func TestTouchesRuntime(t *testing.T) {
	if !TouchesRuntime(Scope{All: true}) {
		t.Fatal("All scope must touch runtime")
	}
	if !TouchesRuntime(Scope{Sections: []Section{SectionPositions}}) {
		t.Fatal("positions must touch runtime")
	}
	// Settings-only and user-settings-only and audit-only do not rebuild the
	// engine.
	for _, s := range []Section{SectionGeneralSettings, SectionUserSettings, SectionAuditLog} {
		if TouchesRuntime(Scope{Sections: []Section{s}}) {
			t.Fatalf("section %q must not touch runtime", s)
		}
	}
}

func TestNewArchiveOmitsVersionAndCarriesRealmLabel(t *testing.T) {
	created := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	archive := NewArchive(created, "src", RealmLabel{Code: "desk-a", Title: "Desk A"},
		Scope{All: true}, fixtureData())
	if archive.Manifest.Realm.Code != "desk-a" || archive.Manifest.Realm.Title != "Desk A" {
		t.Fatalf("realm label = %+v", archive.Manifest.Realm)
	}
	if !archive.Manifest.CreatedAt.Equal(created) {
		t.Fatalf("createdAt = %v, want %v", archive.Manifest.CreatedAt, created)
	}
	if archive.Manifest.FormatVersion != FormatVersion {
		t.Fatalf("formatVersion = %d, want %d", archive.Manifest.FormatVersion, FormatVersion)
	}
	// Round-trips through JSON with the explicit format version.
	raw, err := json.Marshal(archive)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	manifest := generic["manifest"].(map[string]any)
	if got := int(manifest["formatVersion"].(float64)); got != FormatVersion {
		t.Fatalf("json formatVersion = %d, want %d", got, FormatVersion)
	}
}

func TestFilenameStable(t *testing.T) {
	created := time.Date(2026, 6, 26, 12, 30, 15, 0, time.UTC)
	if got := Filename(created); got != "pit-officer-backup-20260626T123015Z.json" {
		t.Fatalf("Filename = %q", got)
	}
}

// --- Fixture data ------------------------------------------------------------

func fixtureData() Data {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	order1XID := mustXID(t1Bytes())
	order2XID := mustXID(t2Bytes())
	return Data{
		AssetClasses: []domain.AssetClass{
			{Code: "equity", Title: "Equity"},
		},
		Assets: []domain.Asset{
			{Code: "AAPL", Title: "Apple", AssetClass: "equity"},
			{Code: "USD", Title: "US Dollar"},
		},
		Principals: []domain.Principal{{Code: "operator", Title: "Operator"}},
		Accounts: []Account{
			{Code: "acc-1", GroupCode: "grp-1"},
			{Code: "acc-2", GroupCode: "grp-2"},
			{Code: "acc-3"},
		},
		Groups: []AccountGroup{
			{Code: "grp-1"},
			{Code: "grp-2"},
			{Code: "grp-unused"},
		},
		Balances: []domain.Balance{
			{Account: "acc-1", Asset: "USD", Available: "10"},
			{Account: "acc-2", Asset: "USD", Available: "20"},
			{Account: "acc-3", Asset: "USD", Available: "30"},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeBroker, MaxQuantity: "1000"},
			{Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", MaxQuantity: "100"},
			{Scope: domain.ScopeAccountAsset, Account: "acc-2", Asset: "AAPL", MaxQuantity: "200"},
		},
		Adjustments: []domain.AccountAdjustmentRecord{
			{ExternalID: mustXID(a1Bytes()), Account: "acc-1", Asset: "USD", At: now},
			{ExternalID: mustXID(a2Bytes()), Account: "acc-2", Asset: "USD", At: now},
		},
		Orders: []OrderRecord{
			{Order: domain.Order{ExternalID: order1XID, Account: "acc-1", At: now}},
			{Order: domain.Order{ExternalID: order2XID, Account: "acc-2", At: now}},
		},
		OrderEvents: []domain.OrderEvent{
			{ExternalID: mustXID(e1Bytes()), Order: order1XID, At: now},
			{ExternalID: mustXID(e2Bytes()), Order: order2XID, At: now},
		},
		Trades: []domain.Trade{
			{ExternalID: mustXID(tr1Bytes()), Order: order1XID, Account: "acc-1", At: now},
			{ExternalID: mustXID(tr2Bytes()), Order: order2XID, Account: "acc-2", At: now},
		},
		Audit: []domain.AuditRow{
			{ExternalID: mustXID(au0Bytes()), Action: domain.AuditActionHydrate},
			{ExternalID: mustXID(au1Bytes()), Account: "acc-1", Action: domain.AuditActionSetNotes},
			{ExternalID: mustXID(au2Bytes()), Account: "acc-2", Action: domain.AuditActionSetNotes},
		},
		McpAccess: map[string]bool{"submit_order": true},
	}
}

func mustXID(b []byte) domain.ExternalID {
	id, err := domain.GeneratedExternalIDFromBytes(b)
	if err != nil {
		panic(err)
	}
	return id
}

// Distinct random-byte seeds for generated fixture ids.
func t1Bytes() []byte  { return seed(0x01) }
func t2Bytes() []byte  { return seed(0x02) }
func a1Bytes() []byte  { return seed(0x11) }
func a2Bytes() []byte  { return seed(0x12) }
func e1Bytes() []byte  { return seed(0x21) }
func e2Bytes() []byte  { return seed(0x22) }
func tr1Bytes() []byte { return seed(0x31) }
func tr2Bytes() []byte { return seed(0x32) }
func au0Bytes() []byte { return seed(0x40) }
func au1Bytes() []byte { return seed(0x41) }
func au2Bytes() []byte { return seed(0x42) }

func seed(b byte) []byte {
	out := make([]byte, domain.ExternalIDByteLen)
	for i := range out {
		out[i] = b
	}
	return out
}

func accountCodes(accounts []Account) []string {
	out := make([]string, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, a.Code)
	}
	return out
}

func groupCodes(groups []AccountGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Code)
	}
	return out
}

func balanceAccounts(balances []domain.Balance) []string {
	out := make([]string, 0, len(balances))
	for _, b := range balances {
		out = append(out, b.Account.String())
	}
	return out
}

func orderSizeAccounts(limits []domain.LimitOrderSize) []string {
	out := make([]string, 0, len(limits))
	for _, l := range limits {
		out = append(out, l.Account.String())
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

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, v := range got {
		counts[v]++
	}
	for _, v := range want {
		if counts[v] == 0 {
			return false
		}
		counts[v]--
	}
	return true
}
