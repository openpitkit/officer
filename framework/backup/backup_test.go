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

// Unit tests for the realm-portable archive contract: scope/selector filtering,
// dictionary-first carriage, and runtime-section classification. The
// store-level export -> restore round-trip (identity preservation, isolated <->
// shared moves, modes, FK resolution and engine-id reassignment) lives in the
// store package, where a real connector is available.
package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	if archive.Manifest.FormatVersion != FormatVersion {
		t.Fatalf("fixture formatVersion = %d, want %d", archive.Manifest.FormatVersion, FormatVersion)
	}
	data := archive.Data
	if len(data.Assets) != 2 {
		t.Fatalf("assets = %d, want 2", len(data.Assets))
	}
	if len(data.Accounts) != 1 || data.Accounts[0].Code != "acc-1" ||
		data.Accounts[0].GroupCode != "grp-1" ||
		data.Accounts[0].BlockPolicy != "SpotFundsPolicy" ||
		data.Accounts[0].BlockCode != domain.RejectCodePnlKillSwitchTriggered ||
		data.Accounts[0].BlockReason != "lower bound breached: pnl -501 is below -500" ||
		data.Accounts[0].BlockDetails != "account pnl -501 is below lower bound -500" {
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
	if data.Orders[0].Order.Account != "acc-1" ||
		data.Orders[0].Order.AmountValue != "10" {
		t.Fatalf("order fields lost by JSON decoding: %+v",
			data.Orders[0].Order)
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

func TestValidateAccountBlockRejectsMalformedCause(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		account Account
		want    string
	}{
		{
			name: "cause on unblocked account",
			account: Account{
				Code: "acc-1", BlockCode: domain.RejectCodePnlKillSwitchTriggered,
			},
			want: "while not blocked",
		},
		{
			name: "unknown code",
			account: Account{
				Code: "acc-1", Blocked: true, BlockCode: "future_unknown_code",
			},
			want: "unknown blockCode \"future_unknown_code\"",
		},
		{
			name: "typed fields without code",
			account: Account{
				Code: "acc-1", Blocked: true, BlockPolicy: "SpotFundsPolicy",
			},
			want: "without blockCode",
		},
		{
			name: "over-length policy",
			account: Account{
				Code:        "acc-1",
				Blocked:     true,
				BlockPolicy: strings.Repeat("p", 4097),
				BlockCode:   domain.RejectCodePnlKillSwitchTriggered,
			},
			want: "blockPolicy",
		},
		{
			name: "non-UTF-8 details",
			account: Account{
				Code:         "acc-1",
				Blocked:      true,
				BlockCode:    domain.RejectCodePnlKillSwitchTriggered,
				BlockDetails: string([]byte{0xff}),
			},
			want: "blockDetails",
		},
		{
			name: "NUL details",
			account: Account{
				Code:         "acc-1",
				Blocked:      true,
				BlockCode:    domain.RejectCodePnlKillSwitchTriggered,
				BlockDetails: "before\x00after",
			},
			want: "blockDetails",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAccountBlock(test.account)
			if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateAccountBlock() = %v, want ErrInvalid containing %q", err, test.want)
			}
		})
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

func TestFilterDataAccountsGroupsCarriesDefaultCurrencyForGroupedAccount(t *testing.T) {
	data := fixtureData()
	data.DefaultGroupCurrency = "USD"
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionAccountsGroups},
		Accounts: EntitySelector{Accounts: []string{"acc-1"}},
	})
	if filtered.DefaultGroupCurrency != "USD" {
		t.Fatalf("default group currency = %q, want USD",
			filtered.DefaultGroupCurrency)
	}
	if got := accountCodes(filtered.Accounts); !sameStrings(got, []string{"acc-1"}) {
		t.Fatalf("accounts = %v, want acc-1", got)
	}
	if got := groupCodes(filtered.Groups); !sameStrings(got, []string{"grp-1"}) {
		t.Fatalf("groups = %v, want grp-1 only", got)
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

func TestFilterDataSpotFundsPnlBoundsIncludeSelectedAccountsGroup(t *testing.T) {
	data := fixtureData()
	filtered := FilterData(data, Scope{
		Sections: []Section{SectionRiskLimits},
		Accounts: EntitySelector{Accounts: []string{"acc-1"}},
	})
	if len(filtered.SpotFundsPnlBoundsLimits) != 1 {
		t.Fatalf("spotFundsPnlBoundsLimits = %+v, want one grp-1 barrier",
			filtered.SpotFundsPnlBoundsLimits)
	}
	got := filtered.SpotFundsPnlBoundsLimits[0]
	if got.Scope != domain.ScopeAccountGroup ||
		got.AccountGroup != "grp-1" {
		t.Fatalf("spotFundsPnlBoundsLimit = %+v, want grp-1 group barrier", got)
	}
}

// TestSpotFundsPnlBoundsListPerAxis files bounds of every axis into the list of
// their axis, encodes and decodes the archive data, and reads them back with
// their axes: no list encodes an axis field, and a bound of an unknown axis is
// refused.
func TestSpotFundsPnlBoundsListPerAxis(t *testing.T) {
	bounds := []domain.LimitSpotFundsPnlBounds{
		{Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1000"},
		{Axis: domain.SpotFundsPnlAxisUnrealized, Scope: domain.ScopeAccount, Account: "acc-1", Currency: "USD", LowerBound: "-200"},
		{Axis: domain.SpotFundsPnlAxisTotal, Scope: domain.ScopeAccountGroup, AccountGroup: "grp-1", Currency: "EUR", UpperBound: "900"},
	}
	var data Data
	if err := data.SetSpotFundsPnlBounds(bounds); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var lists map[string]json.RawMessage
	if err := json.Unmarshal(raw, &lists); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"spotFundsPnlBoundsLimits", "spotFundsUnrealizedPnlBoundsLimits", "spotFundsTotalPnlBoundsLimits",
	} {
		var list []map[string]any
		if err := json.Unmarshal(lists[key], &list); err != nil || len(list) != 1 {
			t.Fatalf("%s = %s (error %v), want one bound", key, lists[key], err)
		}
		for field := range list[0] {
			if strings.EqualFold(field, "axis") {
				t.Errorf("%s encodes an axis field: %s", key, lists[key])
			}
		}
	}
	var decoded Data
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := decoded.SpotFundsPnlBounds(); !reflect.DeepEqual(got, bounds) {
		t.Fatalf("decoded bounds = %+v, want %+v", got, bounds)
	}

	if err := data.SetSpotFundsPnlBounds([]domain.LimitSpotFundsPnlBounds{
		{Axis: "position", Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1"},
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown axis error = %v, want ErrInvalid", err)
	}
}

// TestSpotFundsPnlBoundsFromArchiveWithoutAxes decodes the data of an archive
// written before the unrealized and total axes existed: its one list holds
// realized bounds, and there are no bounds of the other axes.
func TestSpotFundsPnlBoundsFromArchiveWithoutAxes(t *testing.T) {
	var data Data
	if err := json.Unmarshal([]byte(`{"spotFundsPnlBoundsLimits": [
		{"scope": "global", "currency": "USD", "lowerBound": "-1000"}
	]}`), &data); err != nil {
		t.Fatal(err)
	}
	want := []domain.LimitSpotFundsPnlBounds{{
		Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1000",
	}}
	if got := data.SpotFundsPnlBounds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("bounds = %+v, want %+v", got, want)
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
	if len(filtered.ExecutionReports) != 1 ||
		filtered.ExecutionReports[0].Order != keptXID {
		t.Fatalf("execution reports = %+v, want only the kept order's", filtered.ExecutionReports)
	}
	if len(filtered.ExecutionReportEvents) != 1 ||
		filtered.ExecutionReportEvents[0].Report != filtered.ExecutionReports[0].ExternalID ||
		filtered.ExecutionReportEvents[0].Event != filtered.OrderEvents[0].ExternalID {
		t.Fatalf("execution report links = %+v, want only kept children", filtered.ExecutionReportEvents)
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
	// Settings-only and audit-only do not rebuild the
	// engine.
	for _, s := range []Section{
		SectionGeneralSettings, SectionTrading, SectionAuditLog,
	} {
		if TouchesRuntime(Scope{Sections: []Section{s}}) {
			t.Fatalf("section %q must not touch runtime", s)
		}
	}
}

func TestTradingScopeCarriesRequiredParentsAndIsAllOrNothing(t *testing.T) {
	t.Parallel()

	connectionA := domain.ExternalID("01JABCDEFGHJKMNPQRSTVWXY1")
	connectionB := domain.ExternalID("01JABCDEFGHJKMNPQRSTVWXY2")
	data := Data{
		TradingConnections: []TradingConnection{
			{ExternalID: connectionA, Label: "A"},
			{ExternalID: connectionB, Label: "B"},
		},
		TradingAccess: []TradingAccess{
			{Account: "acc-1", Connection: connectionA},
			{Account: "acc-2", Connection: connectionB},
			{AccountGroup: "grp-2", Connection: connectionB},
		},
	}
	scope := Scope{
		Sections: []Section{SectionTrading},
		Accounts: EntitySelector{Accounts: []string{"acc-1"}},
	}
	normalized := scope.Normalize()
	for _, section := range []Section{
		SectionAccountsGroups, SectionActivityHistory, SectionTrading,
	} {
		if !normalized.Included(section) {
			t.Fatalf("trading scope omitted required section %q: %+v", section, normalized.Sections)
		}
	}
	filtered := FilterData(data, scope)
	if !reflect.DeepEqual(filtered.TradingConnections, data.TradingConnections) ||
		!reflect.DeepEqual(filtered.TradingAccess, data.TradingAccess) {
		t.Fatalf("account selector narrowed trading data: %+v", filtered)
	}

	activity := Scope{Sections: []Section{SectionActivityHistory}}.Normalize()
	if !activity.Included(SectionTrading) {
		t.Fatalf("activity scope omitted trading: %+v", activity.Sections)
	}
}

func TestTradingAccessWireCarriesOneSubject(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		access TradingAccess
		want   string
	}{
		{TradingAccess{Account: "acc-1", Connection: "c"}, `{"account":"acc-1","connection":"c"}`},
		{
			TradingAccess{AccountGroup: "grp-1", Connection: "c", VenueAccount: "v"},
			`{"accountGroup":"grp-1","connection":"c","venueAccount":"v"}`,
		},
	} {
		raw, err := json.Marshal(test.access)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != test.want {
			t.Fatalf("wire = %s, want %s", raw, test.want)
		}
		var decoded TradingAccess
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded != test.access {
			t.Fatalf("decoded = %+v, want %+v; err=%v", decoded, test.access, err)
		}
	}
}

func TestArchiveNonReleaseMarker(t *testing.T) {
	t.Run("release bytes unchanged", func(t *testing.T) {
		archive := Archive{CredentialForm: CredentialFormPlaintext}
		raw, err := json.Marshal(archive)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"credentialForm":"plaintext","manifest":{"formatVersion":0,"createdAt":"0001-01-01T00:00:00Z","source":"","realm":{"code":""},"sections":null},"data":{}}`
		if string(raw) != want {
			t.Fatalf("release archive JSON = %s, want %s", raw, want)
		}
		var decoded Archive
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Manifest.NonRelease {
			t.Fatal("unmarked archive decoded as non-release")
		}
	})
	t.Run("non-release round trip", func(t *testing.T) {
		archive := Archive{Manifest: Manifest{NonRelease: true}}
		raw, err := json.Marshal(archive)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(`"nonRelease":true`)) {
			t.Fatalf("non-release marker missing: %s", raw)
		}
		var decoded Archive
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if !decoded.Manifest.NonRelease {
			t.Fatal("non-release marker lost on round trip")
		}
	})
}

func TestRestoreNonReleaseFieldsJSON(t *testing.T) {
	raw, err := json.Marshal(RestoreOptions{DisableTradingConnections: true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("DisableTrading")) || bytes.Contains(raw, []byte("disableTrading")) {
		t.Fatalf("restore seam leaked into JSON: %s", raw)
	}
	for _, names := range [][]string{nil, {"paper desk"}} {
		raw, err := json.Marshal(RestoreSummary{TradingDisabledNonRelease: names})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(`"tradingDisabledNonRelease"`)) != (len(names) != 0) {
			t.Fatalf("summary field presence for %v: %s", names, raw)
		}
		var decoded RestoreSummary
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded.TradingDisabledNonRelease, names) {
			t.Fatalf("summary names = %v, want %v", decoded.TradingDisabledNonRelease, names)
		}
	}
}

func TestNewArchiveOmitsVersionAndCarriesRealmLabel(t *testing.T) {
	created := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	archive := NewArchive(created, "src", RealmLabel{Code: "desk-a", Title: "Desk A"},
		Scope{All: true}, fixtureData(), CredentialFormPlaintext)
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

func TestNewArchiveExcludesBalanceAccountCurrency(t *testing.T) {
	if _, found := reflect.TypeOf(Balance{}).FieldByName("AccountCurrency"); found {
		t.Fatal("portable balance carries derived account currency")
	}
	archive := NewArchive(
		time.Now(), "src", RealmLabel{}, Scope{All: true}, fixtureData(), CredentialFormPlaintext,
	)
	raw, err := json.Marshal(archive)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "AccountCurrency") {
		t.Fatalf("archive leaks derived balance currency: %s", raw)
	}
}

func TestArchiveUsesPortableLowerCamelCaseKeys(t *testing.T) {
	raw, err := json.Marshal(Data{
		Balances: []Balance{{
			UpdatedAt:             time.Unix(1, 0).UTC(),
			Available:             "100",
			Held:                  "20",
			Incoming:              "30",
			RealizedPnl:           "40",
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
			AverageEntryPrice:     "185.00",
			Asset:                 "AAPL",
			Account:               "acc-1",
		}},
		MarketDataInstances: []MarketDataInstance{{
			ExternalID:  mustXID(t2Bytes()),
			Provider:    domain.MarketDataProviderBYO,
			Label:       "manual",
			Credentials: []byte{0xff, 0x00, 0xfe},
			Enabled:     true,
		}},
		MarketDataInstruments: []MarketDataInstrument{{
			Instance:       mustXID(t1Bytes()),
			ExternalSymbol: "AAPLUSD",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
			ManualPrice:    "185.00",
			Enabled:        true,
		}},
	})
	if err != nil {
		t.Fatalf("marshal archive: %v", err)
	}
	for _, key := range []string{
		`"updatedAt"`, `"available"`, `"held"`, `"incoming"`, `"realizedPnl"`,
		`"realizedPnlHaltReason"`, `"averageEntryPrice"`, `"asset"`, `"account"`,
		`"instance"`, `"externalSymbol"`, `"baseAsset"`, `"quoteAsset"`,
		`"manualPrice"`, `"externalId"`, `"provider"`, `"label"`,
		`"credentials"`, `"enabled"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("archive omits portable key %s: %s", key, raw)
		}
	}
}

func TestMarketDataCredentialsJSONRoundTrip(t *testing.T) {
	want := []byte{0x01, 0xff, 0x00, 0xfe, 0x80, 0x7f}
	archive := Archive{
		CredentialForm: CredentialFormSealed,
		Data: Data{MarketDataInstances: []MarketDataInstance{{
			ExternalID:  mustXID(t1Bytes()),
			Provider:    domain.MarketDataProviderBYO,
			Label:       "sealed-feed",
			Credentials: want,
		}}},
	}
	raw, err := json.Marshal(archive)
	if err != nil {
		t.Fatalf("marshal archive: %v", err)
	}
	var roundTrip Archive
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("unmarshal archive: %v", err)
	}
	if roundTrip.CredentialForm != CredentialFormSealed {
		t.Fatalf("credential form = %q, want %q", roundTrip.CredentialForm, CredentialFormSealed)
	}
	if len(roundTrip.Data.MarketDataInstances) != 1 {
		t.Fatalf("market-data instances = %d, want 1", len(roundTrip.Data.MarketDataInstances))
	}
	got := roundTrip.Data.MarketDataInstances[0]
	if !bytes.Equal(got.Credentials, want) {
		t.Fatalf("credential round-trip bytes = %v, want %v", got.Credentials, want)
	}
}

func TestCredentialFormDeclarationIsRequiredAndKnown(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "absent", raw: `{}`},
		{name: "null", raw: `{"credentialForm":null}`},
		{name: "unknown", raw: `{"credentialForm":"future"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var archive Archive
			if err := json.Unmarshal([]byte(test.raw), &archive); err != nil {
				t.Fatalf("unmarshal archive: %v", err)
			}
			if err := ValidateCredentialForm(archive.CredentialForm); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ValidateCredentialForm(%q) = %v, want ErrInvalid",
					archive.CredentialForm, err)
			}
		})
	}
}

func TestSigningKeyArchiveFormHasNoPrivateMaterial(t *testing.T) {
	if _, found := reflect.TypeOf(SigningKey{}).FieldByName("PrivateKey"); found {
		t.Fatal("portable signing key exposes private material")
	}
	raw, err := json.Marshal(SigningKey{
		KeyID: "key-1", Alg: "ed25519", PublicKey: []byte("public"), Active: true,
	})
	if err != nil {
		t.Fatalf("marshal signing key: %v", err)
	}
	if bytes.Contains(raw, []byte("private")) {
		t.Fatalf("portable signing key JSON contains private material field: %s", raw)
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
		Assets: []Asset{
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
		Balances: []Balance{
			{Account: "acc-1", Asset: "USD", Available: "10"},
			{Account: "acc-2", Asset: "USD", Available: "20"},
			{Account: "acc-3", Asset: "USD", Available: "30"},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeBroker, MaxQuantity: "1000"},
			{Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-1", Asset: "AAPL", MaxQuantity: "100"},
			{Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-2", Asset: "AAPL", MaxQuantity: "200"},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{
				Axis:         domain.SpotFundsPnlAxisRealized,
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "grp-1",
				Currency:     "USD",
				LowerBound:   "-100",
			},
			{
				Axis:         domain.SpotFundsPnlAxisRealized,
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "grp-2",
				Currency:     "USD",
				LowerBound:   "-200",
			},
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
		ExecutionReports: []ExecutionReportRecord{
			{ExternalID: "report-1", Order: order1XID, At: now},
			{ExternalID: "report-2", Order: order2XID, At: now},
		},
		ExecutionReportEvents: []ExecutionReportEventLink{
			{Report: "report-1", Event: mustXID(e1Bytes())},
			{Report: "report-2", Event: mustXID(e2Bytes())},
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

func balanceAccounts(balances []Balance) []string {
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
