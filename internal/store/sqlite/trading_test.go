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

package sqlite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestCredentialsSealed(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(fmt.Sprintf("sealed=%v", sealed), func(t *testing.T) {
			var opts []Option
			if sealed {
				opts = append(opts, WithMasterKey(mustStoreMasterKey(t, 0x71)))
			}
			_, rs := newTestStore(t, opts...)
			if got := rs.CredentialsSealed(); got != sealed {
				t.Fatalf("CredentialsSealed = %v, want %v", got, sealed)
			}
		})
	}
}

func TestTradingVenueIdentityStorage(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	got, found, err := rs.GetTradingConnection(ctx, connection.ExternalID)
	if err != nil || !found || got.VenueIdentity != connection.VenueIdentity {
		t.Fatalf("venue identity not read back: found=%v identity=%q err=%v", found, got.VenueIdentity, err)
	}
	listed, err := rs.ListTradingConnections(ctx)
	if err != nil || len(listed) != 1 || listed[0].VenueIdentity != connection.VenueIdentity {
		t.Fatal("listed venue identity differs")
	}
	duplicate := connection
	duplicate.ExternalID = ""
	duplicate.Label = "second connection"
	_, err = rs.CreateTradingConnection(ctx, duplicate)
	if !errors.Is(err, domain.ErrAlreadyExists) || !strings.Contains(err.Error(), "venue identity") || !strings.Contains(err.Error(), connection.VenueIdentity) {
		t.Fatalf("duplicate venue identity error = %v, want named ErrAlreadyExists", err)
	}
	if strings.Contains(err.Error(), connection.Credentials) {
		t.Fatal("duplicate error exposes credentials")
	}
	duplicate.VenueIdentity = "different-account"
	if _, err := rs.CreateTradingConnection(ctx, duplicate); err != nil {
		t.Fatalf("different identity rejected: %v", err)
	}
	duplicate.Label = "other provider"
	duplicate.Provider = "another-provider"
	duplicate.VenueIdentity = connection.VenueIdentity
	if _, err := rs.CreateTradingConnection(ctx, duplicate); err != nil {
		t.Fatalf("different provider rejected: %v", err)
	}
	duplicate = connection
	duplicate.ExternalID = ""
	duplicate.Label = strings.ToUpper(connection.Label)
	duplicate.VenueIdentity = "third-account"
	_, err = rs.CreateTradingConnection(ctx, duplicate)
	if !errors.Is(err, domain.ErrAlreadyExists) || !strings.Contains(err.Error(), "label") {
		t.Fatalf("duplicate label error = %v", err)
	}
	duplicate = connection
	duplicate.Label = "duplicate id"
	duplicate.VenueIdentity = "fourth-account"
	_, err = rs.CreateTradingConnection(ctx, duplicate)
	if !errors.Is(err, domain.ErrAlreadyExists) || !strings.Contains(err.Error(), connection.ExternalID.String()) {
		t.Fatalf("duplicate id error = %v", err)
	}
}

func TestUpdateTradingConnectionSettings(t *testing.T) {
	ctx, rs, original := seedTradingFixtures(t)
	const credentials = `{"key":"replacement-secret"}`
	if err := rs.UpdateTradingConnectionSettings(ctx, original.ExternalID, "new label", credentials); err != nil {
		t.Fatal(err)
	}
	got, found, err := rs.GetTradingConnection(ctx, original.ExternalID)
	if err != nil || !found {
		t.Fatalf("get updated connection: found=%v err=%v", found, err)
	}
	want := original
	want.Label, want.Credentials = "new label", credentials
	if got != want {
		t.Fatal("settings were not replaced or immutable fields changed")
	}
	if err := rs.UpdateTradingConnectionSettings(ctx, "absent", "absent", credentials); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("absent connection error = %v, want ErrNotFound", err)
	}
	other := original
	other.ExternalID, other.Label, other.VenueIdentity = "", "taken label", "other-account"
	if _, err := rs.CreateTradingConnection(ctx, other); err != nil {
		t.Fatal(err)
	}
	err = rs.UpdateTradingConnectionSettings(ctx, original.ExternalID, "TAKEN LABEL", credentials)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate label error = %v, want ErrAlreadyExists", err)
	}
	if strings.Contains(err.Error(), credentials) {
		t.Fatal("update error exposes credentials")
	}
	got, found, err = rs.GetTradingConnection(ctx, original.ExternalID)
	if err != nil || !found || got != want {
		t.Fatal("failed update changed stored settings")
	}
}

func TestUpdateTradingConnectionSettingsRejectsInvalid(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(fmt.Sprintf("sealed=%v", sealed), func(t *testing.T) {
			ctx := context.Background()
			var rs store.RealmStore
			if sealed {
				_, rs = newTestStore(t, WithMasterKey(mustStoreMasterKey(t, 0x72)))
			} else {
				_, rs = newTestStore(t)
			}
			original, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
				Provider: domain.TradingProviderAlpaca, Label: "paper", Mode: domain.TradingModePaper,
				VenueIdentity: "paper-account", Credentials: `{"key":"original-secret"}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			var originalStored []byte
			if err := rs.(*realmStore).rawDB().QueryRowContext(ctx,
				"SELECT credentials FROM trading_connection WHERE external_id = ?", original.ExternalID.Bytes(),
			).Scan(&originalStored); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name, label, credentials string
			}{
				{"empty label", "", "{}"},
				{"label leading space", " paper", "{}"},
				{"label trailing space", "paper ", "{}"},
				{"credentials leading space", "renamed", " {}"},
				{"credentials trailing space", "renamed", "{} "},
				{"empty credentials", "renamed", ""},
				{"invalid credentials", "renamed", "{"},
				{"array credentials", "renamed", "[]"},
				{"string credentials", "renamed", `"secret"`},
				{"number credentials", "renamed", "1"},
				{"boolean credentials", "renamed", "true"},
				{"null credentials", "renamed", "null"},
				{"trailing JSON", "renamed", "{} {}"},
			} {
				t.Run(test.name, func(t *testing.T) {
					err := rs.UpdateTradingConnectionSettings(ctx, original.ExternalID, test.label, test.credentials)
					if !errors.Is(err, domain.ErrInvalid) {
						t.Errorf("invalid settings error = %v, want ErrInvalid", err)
					}
					got, found, err := rs.GetTradingConnection(ctx, original.ExternalID)
					if err != nil || !found || got != original {
						t.Errorf("invalid settings changed connection: found=%v err=%v", found, err)
					}
					var stored []byte
					if err := rs.(*realmStore).rawDB().QueryRowContext(ctx,
						"SELECT credentials FROM trading_connection WHERE external_id = ?", original.ExternalID.Bytes(),
					).Scan(&stored); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(stored, originalStored) {
						t.Error("invalid settings changed stored credentials")
					}
				})
			}
		})
	}
}

func TestUpdateTradingConnectionSettingsSealed(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t, WithMasterKey(mustStoreMasterKey(t, 0x72)))
	connection, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "sealed", Mode: domain.TradingModePaper,
		VenueIdentity: "sealed-account", Credentials: `{"key":"old-secret"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	const credentials = `{"key":"new-sealed-secret"}`
	if err := rs.UpdateTradingConnectionSettings(ctx, connection.ExternalID, "renamed", credentials); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := rs.(*realmStore).rawDB().QueryRowContext(ctx,
		"SELECT credentials FROM trading_connection WHERE external_id = ?", connection.ExternalID.Bytes(),
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stored, []byte(credentials)) || bytes.Contains(stored, []byte("new-sealed-secret")) {
		t.Fatal("updated credentials stored as plaintext")
	}
	got, found, err := rs.GetTradingConnection(ctx, connection.ExternalID)
	if err != nil || !found || got.Credentials != credentials || got.Label != "renamed" {
		t.Fatal("sealed settings update did not round trip")
	}
	other := connection
	other.ExternalID, other.Label, other.VenueIdentity = "", "other", "other-account"
	other, err = rs.CreateTradingConnection(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rs.(*realmStore).rawDB().ExecContext(ctx,
		"UPDATE trading_connection SET credentials = ? WHERE external_id = ?", stored, other.ExternalID.Bytes(),
	); err != nil {
		t.Fatal(err)
	}
	got, _, err = rs.GetTradingConnection(ctx, other.ExternalID)
	if err == nil || got.Credentials != "" {
		t.Fatal("updated credentials accepted after row move")
	}
}

func TestDeleteTradingInstrument(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	for _, symbol := range []string{"AAPL", "MSFT"} {
		if err := rs.UpsertTradingInstrument(ctx, domain.TradingInstrument{
			Connection: connection.ExternalID, ExternalSymbol: symbol, BaseAsset: symbol, QuoteAsset: "USD",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rs.DeleteTradingInstrument(ctx, connection.ExternalID, "AAPL"); err != nil {
		t.Fatal(err)
	}
	instruments, err := rs.ListTradingInstruments(ctx, connection.ExternalID)
	if err != nil || len(instruments) != 1 || instruments[0].ExternalSymbol != "MSFT" {
		t.Fatal("delete did not remove exactly one mapping")
	}
	for _, id := range []domain.ExternalID{connection.ExternalID, "absent"} {
		if err := rs.DeleteTradingInstrument(ctx, id, "AAPL"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("missing mapping/connection error = %v, want ErrNotFound", err)
		}
	}
}

func TestRemoveTradingAccess(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	access := domain.TradingAccess{Account: "acc-1", Connection: connection.ExternalID, VenueAccount: "venue-account"}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatal(err)
	}
	other := domain.TradingAccess{Account: "acc-2", Connection: connection.ExternalID}
	for _, binding := range []domain.TradingAccess{access, other} {
		if err := rs.PutTradingAccess(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	if err := rs.RemoveTradingAccess(ctx, access.Connection, access.Account, ""); err != nil {
		t.Fatal(err)
	}
	bindings, err := rs.ListTradingAccessForConnection(ctx, connection.ExternalID)
	if err != nil || !reflect.DeepEqual(bindings, []store.TradingAccessListRow{{Access: other}}) {
		t.Fatal("remove did not delete exactly the account/connection binding")
	}
	if err := rs.RemoveTradingAccess(ctx, access.Connection, access.Account, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing binding error = %v, want ErrNotFound", err)
	}
	// An unknown account reads exactly as an absent grant.
	for _, missing := range []domain.TradingAccess{
		{Account: access.Account, Connection: "absent"},
		{Account: "absent", Connection: access.Connection},
	} {
		err := rs.RemoveTradingAccess(ctx, missing.Connection, missing.Account, "")
		want := fmt.Sprintf("trading access %q: %v", missing.Connection.String(), domain.ErrNotFound)
		if !errors.Is(err, domain.ErrNotFound) || err.Error() != want {
			t.Fatalf("missing account/connection error = %v, want %q", err, want)
		}
	}
}

func TestTradingSchemaModeVocabulary(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	db := rs.(*realmStore).rawDB()
	for _, mode := range []string{"live", "paper"} {
		if _, err := db.ExecContext(ctx, "UPDATE trading_connection SET mode = ? WHERE external_id = ?", mode, connection.ExternalID.Bytes()); err != nil {
			t.Fatalf("schema rejected %s: %v", mode, err)
		}
	}
	for _, mode := range []string{"test", "real"} {
		if _, err := db.ExecContext(ctx, "UPDATE trading_connection SET mode = ? WHERE external_id = ?", mode, connection.ExternalID.Bytes()); err == nil {
			t.Fatalf("schema accepted old mode %q", mode)
		}
	}
}

func seedTradingFixtures(
	t *testing.T,
) (context.Context, store.RealmStore, domain.TradingConnection) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	for _, asset := range []string{"AAPL", "MSFT", "USD", "EUR"} {
		if _, err := rs.CreateAsset(ctx, domain.Asset{Code: asset}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	connection, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider:      domain.TradingProviderAlpaca,
		Label:         "Alpaca paper",
		VenueIdentity: "paper-account",
		Mode:          domain.TradingModePaper,
		Credentials:   `{"key":"fixture"}`,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("CreateTradingConnection: %v", err)
	}
	if connection.ExternalID.IsZero() {
		t.Fatal("CreateTradingConnection did not generate an external id")
	}
	return ctx, rs, connection
}

func createTradingOrder(
	t *testing.T, ctx context.Context, rs store.RealmStore,
) domain.Order {
	t.Helper()
	order, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	return order
}

func TestEarliestVenueOrderTime(t *testing.T) {
	ctx, rs, first := seedTradingFixtures(t)
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "Other", Mode: domain.TradingModePaper, VenueIdentity: "other-account",
		Credentials: `{}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.ExternalID{first.ExternalID, second.ExternalID, "unknown"} {
		at, found, err := rs.EarliestVenueOrderTime(ctx, id)
		if err != nil || found || !at.IsZero() {
			t.Fatalf("empty connection: at=%v found=%v err=%v", at, found, err)
		}
	}
	links := make([]domain.VenueOrder, 0)
	for i, connection := range []domain.ExternalID{second.ExternalID, first.ExternalID, first.ExternalID} {
		order := createTradingOrder(t, ctx, rs)
		link, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
			Order: order.ExternalID, Connection: connection, Route: `{}`,
			ClientOrderID: order.ExternalID.String(),
		})
		if err != nil {
			t.Fatal(err)
		}
		links = append(links, link)
		if i == 1 {
			if err := rs.UpdateOrderStatus(ctx, order.ExternalID, domain.OrderStatusCancelled); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, connection := range []domain.ExternalID{second.ExternalID, first.ExternalID} {
		at, found, err := rs.EarliestVenueOrderTime(ctx, connection)
		if err != nil || !found || !at.Equal(links[i].CreatedAt) {
			t.Fatalf("earliest link for connection %s: at=%v want=%v found=%v err=%v",
				connection, at, links[i].CreatedAt, found, err)
		}
	}
}

func TestListTradingAccessForConnection(t *testing.T) {
	ctx, rs, first := seedTradingFixtures(t)
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "Other", Mode: domain.TradingModePaper, VenueIdentity: "other-account",
		Credentials: `{}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.ExternalID{first.ExternalID, second.ExternalID, "unknown"} {
		rows, err := rs.ListTradingAccessForConnection(ctx, id)
		if err != nil || len(rows) != 0 {
			t.Fatalf("empty connection: rows=%v err=%v", rows, err)
		}
	}
	for _, group := range []string{"desk", "other"} {
		if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2", GroupCode: "desk"}); err != nil {
		t.Fatal(err)
	}
	want := []store.TradingAccessListRow{
		{Access: domain.TradingAccess{AccountGroup: "desk", Connection: first.ExternalID, VenueAccount: "z"}},
		{Access: domain.TradingAccess{AccountGroup: "other", Connection: first.ExternalID}},
		{Access: domain.TradingAccess{Account: "acc-1", Connection: first.ExternalID}},
		{Access: domain.TradingAccess{Account: "acc-2", Connection: first.ExternalID, VenueAccount: "a"}, Group: "desk"},
	}
	other := domain.TradingAccess{Account: "acc-2", Connection: second.ExternalID}
	for _, row := range []domain.TradingAccess{want[3].Access, other, want[1].Access, want[2].Access, want[0].Access} {
		if err := rs.PutTradingAccess(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := rs.ListTradingAccessForConnection(ctx, first.ExternalID)
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("connection access ordering/isolation: got=%v want=%v err=%v", rows, want, err)
	}
	rows, err = rs.ListTradingAccessForConnection(ctx, second.ExternalID)
	if err != nil || !reflect.DeepEqual(rows, []store.TradingAccessListRow{{Access: other, Group: "desk"}}) {
		t.Fatalf("other connection access: got=%v err=%v", rows, err)
	}
	if err := rs.SetAccountGroup(ctx, "acc-2", "other"); err != nil {
		t.Fatal(err)
	}
	rows, err = rs.ListTradingAccessForConnection(ctx, second.ExternalID)
	if err != nil || !reflect.DeepEqual(rows, []store.TradingAccessListRow{{Access: other, Group: "other"}}) {
		t.Fatalf("membership is not read at list time: got=%v err=%v", rows, err)
	}
}

func TestTradingConnectionFeeAccount(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "fees"}); err != nil {
		t.Fatal(err)
	}
	for _, account := range []domain.AccountID{"fees", "acc-1", ""} {
		if err := rs.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, account); err != nil {
			t.Fatal(err)
		}
		got, found, err := rs.GetTradingConnection(ctx, connection.ExternalID)
		if err != nil || !found || got.FeeAccount != account {
			t.Fatalf("fee account = %q, want %q, found=%v err=%v", got.FeeAccount, account, found, err)
		}
		connections, err := rs.ListTradingConnections(ctx)
		if err != nil || len(connections) != 1 {
			t.Fatalf("list connections = %+v, err=%v", connections, err)
		}
		for _, listed := range connections {
			if listed.FeeAccount != account {
				t.Fatalf("listed fee account = %q, want %q", listed.FeeAccount, account)
			}
		}
	}
	for _, tc := range []struct {
		name       string
		connection domain.ExternalID
		account    domain.AccountID
		want       error
	}{
		{"unknown connection", "missing", "fees", domain.ErrNotFound},
		{"clear unknown connection", "missing", "", domain.ErrNotFound},
		{"unknown account", connection.ExternalID, "missing", domain.ErrNotFound},
		{"invalid account", connection.ExternalID, " fees", domain.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := rs.SetTradingConnectionFeeAccount(ctx, tc.connection, tc.account); !errors.Is(err, tc.want) {
				t.Fatalf("set fee account error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCreateTradingConnectionRefusesFeeAccount(t *testing.T) {
	for _, account := range []domain.AccountID{"acc-1", "missing"} {
		t.Run(account.String(), func(t *testing.T) {
			ctx, rs, connection := seedTradingFixtures(t)
			connection.ExternalID = "refused"
			connection.Label, connection.VenueIdentity = "refused", "refused"
			connection.FeeAccount = account
			if _, err := rs.CreateTradingConnection(ctx, connection); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("create fee account error = %v, want ErrInvalid", err)
			}
			if _, found, err := rs.GetTradingConnection(ctx, connection.ExternalID); err != nil || found {
				t.Fatalf("refused create persisted connection: found=%v err=%v", found, err)
			}
		})
	}
}

func TestGetTradingFeeAccounts(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	if _, _, err := rs.GetTradingFeeAccounts(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown connection error = %v", err)
	}
	check := func(wantFee domain.AccountID, wantBound []domain.AccountID) {
		t.Helper()
		fee, bound, err := rs.GetTradingFeeAccounts(ctx, connection.ExternalID)
		if err != nil || fee != wantFee || !reflect.DeepEqual(bound, wantBound) {
			t.Fatalf("fee snapshot = %q, %v, err=%v; want %q, %v", fee, bound, err, wantFee, wantBound)
		}
	}
	check("", []domain.AccountID{})
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatal(err)
	}
	if err := rs.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "acc-2"); err != nil {
		t.Fatal(err)
	}
	check("acc-2", []domain.AccountID{})
	for _, account := range []domain.AccountID{"acc-2", "acc-1"} {
		if err := rs.PutTradingAccess(ctx, domain.TradingAccess{Connection: connection.ExternalID, Account: account}); err != nil {
			t.Fatal(err)
		}
	}
	check("acc-2", []domain.AccountID{"acc-1", "acc-2"})
	if err := rs.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, ""); err != nil {
		t.Fatal(err)
	}
	check("", []domain.AccountID{"acc-1", "acc-2"})
	if err := rs.RemoveTradingAccess(ctx, connection.ExternalID, "acc-2", ""); err != nil {
		t.Fatal(err)
	}
	check("", []domain.AccountID{"acc-1"})

	// A granted group adds its current members; acc-1 holds a grant of its own
	// as well and is counted once. An empty group adds nobody.
	for _, group := range []string{"desk", "empty"} {
		if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
			t.Fatal(err)
		}
		if err := rs.PutTradingAccess(ctx, domain.TradingAccess{Connection: connection.ExternalID, AccountGroup: group}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-3", GroupCode: "desk"}); err != nil {
		t.Fatal(err)
	}
	if err := rs.SetAccountGroup(ctx, "acc-1", "desk"); err != nil {
		t.Fatal(err)
	}
	check("", []domain.AccountID{"acc-1", "acc-3"})
	if err := rs.SetAccountGroup(ctx, "acc-3", ""); err != nil {
		t.Fatal(err)
	}
	check("", []domain.AccountID{"acc-1"})
	if err := rs.RemoveTradingAccess(ctx, connection.ExternalID, "acc-1", ""); err != nil {
		t.Fatal(err)
	}
	check("", []domain.AccountID{"acc-1"})
	if err := rs.RemoveTradingAccess(ctx, connection.ExternalID, "", "desk"); err != nil {
		t.Fatal(err)
	}
	check("", []domain.AccountID{})
}

func TestDeleteTradingFeeAccountRefused(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			ctx, rs, connection := seedTradingFixtures(t)
			if err := rs.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "acc-1"); err != nil {
				t.Fatal(err)
			}
			if err := rs.PutTradingAccess(ctx, domain.TradingAccess{Connection: connection.ExternalID, Account: "acc-1"}); err != nil {
				t.Fatal(err)
			}
			err := rs.DeleteAccount(ctx, "acc-1", force)
			if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), connection.Label) {
				t.Fatalf("delete fee account error = %v, want conflict naming connection %q", err, connection.Label)
			}
			if _, found, err := rs.GetAccount(ctx, "acc-1"); err != nil || !found {
				t.Fatalf("refused deletion removed account: found=%v err=%v", found, err)
			}
			got, found, err := rs.GetTradingConnection(ctx, connection.ExternalID)
			if err != nil || !found || got.FeeAccount != "acc-1" {
				t.Fatalf("refused deletion broke fee account: %+v, found=%v err=%v", got, found, err)
			}
			accesses, err := rs.ListTradingAccessForConnection(ctx, connection.ExternalID)
			if err != nil || len(accesses) != 1 {
				t.Fatalf("refused deletion removed bindings: %+v, err=%v", accesses, err)
			}
			if err := rs.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, ""); err != nil {
				t.Fatal(err)
			}
			if err := rs.RemoveTradingAccess(ctx, connection.ExternalID, "acc-1", ""); err != nil {
				t.Fatal(err)
			}
			if err := rs.DeleteAccount(ctx, "acc-1", force); err != nil {
				t.Fatalf("delete after clearing fee account: %v", err)
			}
		})
	}
}

func TestTradingConnectionCreateGetListEnable(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	first, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		ExternalID:    "connection-b",
		Provider:      domain.TradingProviderAlpaca,
		Label:         "Second",
		VenueIdentity: "second-account",
		Mode:          domain.TradingModePaper,
		Credentials:   `{"key":"second"}`,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("CreateTradingConnection(first): %v", err)
	}
	if first.ExternalID != "connection-b" {
		t.Fatal("CreateTradingConnection did not preserve the supplied external id")
	}
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		ExternalID:    "connection-a",
		Provider:      domain.TradingProviderAlpaca,
		Label:         "First",
		VenueIdentity: "first-account",
		Mode:          domain.TradingModeLive,
		Credentials:   `{"key":"first"}`,
	})
	if err != nil {
		t.Fatalf("CreateTradingConnection(second): %v", err)
	}

	got, ok, err := rs.GetTradingConnection(ctx, first.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetTradingConnection: ok=%v err=%v", ok, err)
	}
	if got.ExternalID != first.ExternalID || got.Provider != first.Provider ||
		got.Label != first.Label || got.Mode != first.Mode ||
		got.Enabled != first.Enabled {
		t.Fatal("GetTradingConnection did not preserve non-secret fields")
	}
	if got.Credentials != first.Credentials {
		t.Fatal("GetTradingConnection did not preserve credentials")
	}
	if _, ok, err := rs.GetTradingConnection(ctx, "missing"); err != nil || ok {
		t.Fatalf("GetTradingConnection(missing): ok=%v err=%v", ok, err)
	}

	connections, err := rs.ListTradingConnections(ctx)
	if err != nil {
		t.Fatalf("ListTradingConnections: %v", err)
	}
	if len(connections) != 2 ||
		connections[0].ExternalID != second.ExternalID ||
		connections[1].ExternalID != first.ExternalID {
		t.Fatal("ListTradingConnections is not ordered by external id")
	}

	if err := rs.SetTradingConnectionEnabled(ctx, second.ExternalID, true); err != nil {
		t.Fatalf("SetTradingConnectionEnabled: %v", err)
	}
	got, ok, err = rs.GetTradingConnection(ctx, second.ExternalID)
	if err != nil || !ok || !got.Enabled {
		t.Fatalf("enabled connection read: ok=%v enabled=%v err=%v", ok, got.Enabled, err)
	}
	if err := rs.SetTradingConnectionEnabled(ctx, "missing", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetTradingConnectionEnabled(missing) error = %v", err)
	}
}

func TestTradingConnectionValidationAndUniqueness(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	connection := domain.TradingConnection{
		ExternalID:    "connection-id",
		Provider:      domain.TradingProviderAlpaca,
		Label:         "Alpaca",
		VenueIdentity: "alpaca-account",
		Mode:          domain.TradingModePaper,
		Credentials:   `{}`,
	}
	if _, err := rs.CreateTradingConnection(ctx, connection); err != nil {
		t.Fatalf("CreateTradingConnection: %v", err)
	}
	duplicateID := connection
	duplicateID.Label = "Other"
	if _, err := rs.CreateTradingConnection(ctx, duplicateID); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate external id error = %v", err)
	}
	duplicateLabel := connection
	duplicateLabel.ExternalID = "other-id"
	duplicateLabel.Label = "ALPACA"
	if _, err := rs.CreateTradingConnection(ctx, duplicateLabel); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("case-insensitive duplicate label error = %v", err)
	}
	invalidMode := connection
	invalidMode.ExternalID = "invalid-mode"
	invalidMode.Label = "Invalid mode"
	invalidMode.Mode = "unknown"
	if _, err := rs.CreateTradingConnection(ctx, invalidMode); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid mode error = %v", err)
	}
	invalidCredentials := connection
	invalidCredentials.ExternalID = "invalid-credentials"
	invalidCredentials.Label = "Invalid credentials"
	invalidCredentials.Credentials = "[]"
	if _, err := rs.CreateTradingConnection(ctx, invalidCredentials); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid credentials error does not wrap ErrInvalid")
	}
}

func TestTradingInstrumentUpsertFindListAndUniqueness(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	instrument := domain.TradingInstrument{
		Connection:     connection.ExternalID,
		ExternalSymbol: "AAPL",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Enabled:        true,
	}
	if err := rs.UpsertTradingInstrument(ctx, instrument); err != nil {
		t.Fatalf("UpsertTradingInstrument: %v", err)
	}
	got, ok, err := rs.FindTradingInstrument(
		ctx, connection.ExternalID, "AAPL", "USD",
	)
	if err != nil || !ok {
		t.Fatalf("FindTradingInstrument: ok=%v err=%v", ok, err)
	}
	if got.ExternalSymbol != instrument.ExternalSymbol ||
		got.BaseAsset != instrument.BaseAsset ||
		got.QuoteAsset != instrument.QuoteAsset || !got.Enabled {
		t.Fatal("FindTradingInstrument did not populate the stored mapping")
	}

	updated := instrument
	updated.BaseAsset = "MSFT"
	updated.Enabled = false
	if err := rs.UpsertTradingInstrument(ctx, updated); err != nil {
		t.Fatalf("UpsertTradingInstrument(update by symbol): %v", err)
	}
	if _, ok, err := rs.FindTradingInstrument(
		ctx, connection.ExternalID, "AAPL", "USD",
	); err != nil || ok {
		t.Fatalf("old instrument pair after update: ok=%v err=%v", ok, err)
	}
	got, ok, err = rs.FindTradingInstrument(
		ctx, connection.ExternalID, "MSFT", "USD",
	)
	if err != nil || !ok || got.Enabled {
		t.Fatalf("updated instrument: ok=%v enabled=%v err=%v", ok, got.Enabled, err)
	}

	if err := rs.UpsertTradingInstrument(ctx, domain.TradingInstrument{
		Connection:     connection.ExternalID,
		ExternalSymbol: "MSFT-OTHER",
		BaseAsset:      "MSFT",
		QuoteAsset:     "USD",
	}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate asset pair error = %v", err)
	}
	if err := rs.UpsertTradingInstrument(ctx, domain.TradingInstrument{
		Connection:     connection.ExternalID,
		ExternalSymbol: "EURUSD",
		BaseAsset:      "EUR",
		QuoteAsset:     "USD",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertTradingInstrument(second): %v", err)
	}
	instruments, err := rs.ListTradingInstruments(ctx, connection.ExternalID)
	if err != nil {
		t.Fatalf("ListTradingInstruments: %v", err)
	}
	if len(instruments) != 2 || instruments[0].ExternalSymbol != "AAPL" ||
		instruments[1].ExternalSymbol != "EURUSD" {
		t.Fatal("ListTradingInstruments is not ordered by external symbol")
	}
}

func TestTradingInstrumentUnknownReferences(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	base := domain.TradingInstrument{
		Connection:     connection.ExternalID,
		ExternalSymbol: "AAPL",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
	}
	unknownConnection := base
	unknownConnection.Connection = "missing"
	if err := rs.UpsertTradingInstrument(ctx, unknownConnection); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown connection error = %v", err)
	}
	unknownBase := base
	unknownBase.BaseAsset = "UNKNOWNB"
	assertTradingAssetMissing(t, rs.UpsertTradingInstrument(ctx, unknownBase), "UNKNOWNB")
	unknownQuote := base
	unknownQuote.QuoteAsset = "UNKNOWNQ"
	assertTradingAssetMissing(t, rs.UpsertTradingInstrument(ctx, unknownQuote), "UNKNOWNQ")
	instruments, err := rs.ListTradingInstruments(ctx, connection.ExternalID)
	if err != nil {
		t.Fatalf("ListTradingInstruments: %v", err)
	}
	if len(instruments) != 0 {
		t.Fatalf("instruments after refused upserts = %+v, want none", instruments)
	}
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "UNKNOWNQ"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if err := rs.UpsertTradingInstrument(ctx, unknownQuote); err != nil {
		t.Fatalf("upsert after creating the missing asset: %v", err)
	}
}

func assertTradingAssetMissing(t *testing.T, err error, asset string) {
	t.Helper()
	var missing domain.AssetMissingError
	if !errors.As(err, &missing) || missing.Asset != asset {
		t.Fatalf("unknown asset error = %v, want AssetMissingError for %q", err, asset)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown asset error = %v, must not be ErrNotFound", err)
	}
}

func TestTradingAccessPutListReplaceAndUnknown(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		ExternalID: "connection-a", Provider: domain.TradingProviderAlpaca,
		Label: "Alpaca live", VenueIdentity: "live-account",
		Mode: domain.TradingModeLive, Credentials: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstAccess := domain.TradingAccess{Account: "acc-1", Connection: connection.ExternalID, VenueAccount: "z"}
	access := domain.TradingAccess{Account: "acc-1", Connection: second.ExternalID, VenueAccount: "b"}
	for _, binding := range []domain.TradingAccess{firstAccess, access} {
		if err := rs.PutTradingAccess(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	access.VenueAccount = "a"
	if err := rs.PutTradingAccess(ctx, access); err != nil {
		t.Fatalf("replace binding: %v", err)
	}
	if err := rs.PutTradingAccess(ctx, access); err != nil {
		t.Fatalf("repeat binding: %v", err)
	}
	bindings, err := rs.ListTradingAccess(ctx, "acc-1")
	want := []domain.TradingAccess{access, firstAccess}
	sort.Slice(want, func(i, j int) bool {
		return want[i].Connection < want[j].Connection
	})
	if err != nil || !reflect.DeepEqual(bindings, want) {
		t.Fatalf("bindings = %v, want replaced binding and unaffected connection; err=%v", bindings, err)
	}
	access.VenueAccount = ""
	for i := range want {
		if want[i].Connection == access.Connection {
			want[i] = access
		}
	}
	if err := rs.PutTradingAccess(ctx, access); err != nil {
		t.Fatal(err)
	}
	bindings, err = rs.ListTradingAccess(ctx, "acc-1")
	if err != nil || !reflect.DeepEqual(bindings, want) {
		t.Fatal("empty venue account did not replace the bound account")
	}
	for _, missing := range []domain.TradingAccess{
		{Account: "missing", Connection: connection.ExternalID},
		{Account: "acc-1", Connection: "missing"},
	} {
		if err := rs.PutTradingAccess(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown account/connection error = %v, want ErrNotFound", err)
		}
	}
}

func TestTradingGroupAccessPutReplaceRemove(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	for _, group := range []string{"desk", "other"} {
		if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
			t.Fatal(err)
		}
	}
	// A default currency materialises the default group as a row with an
	// empty code; no grant may ever reach it.
	if err := rs.SetGroupCurrency(ctx, "", "USD"); err != nil {
		t.Fatal(err)
	}
	check := func(step string, want ...domain.TradingAccess) {
		t.Helper()
		rows, err := rs.ListTradingAccessForConnection(ctx, connection.ExternalID)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]domain.TradingAccess, 0, len(rows))
		for _, row := range rows {
			got = append(got, row.Access)
		}
		if !reflect.DeepEqual(got, append([]domain.TradingAccess{}, want...)) {
			t.Fatalf("%s: grants = %+v, want %+v", step, got, want)
		}
	}
	desk := domain.TradingAccess{AccountGroup: "desk", Connection: connection.ExternalID, VenueAccount: "a"}
	other := domain.TradingAccess{AccountGroup: "other", Connection: connection.ExternalID}
	for _, grant := range []domain.TradingAccess{other, desk} {
		if err := rs.PutTradingAccess(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	check("put", desk, other)
	desk.VenueAccount = ""
	if err := rs.PutTradingAccess(ctx, desk); err != nil {
		t.Fatalf("replace group grant: %v", err)
	}
	check("replace", desk, other)
	if err := rs.RemoveTradingAccess(ctx, connection.ExternalID, "", "desk"); err != nil {
		t.Fatal(err)
	}
	check("remove", other)
	if err := rs.RemoveTradingAccess(ctx, connection.ExternalID, "", "desk"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("absent group grant error = %v, want ErrNotFound", err)
	}

	for _, missing := range []domain.TradingAccess{
		{AccountGroup: "missing", Connection: connection.ExternalID},
		{AccountGroup: "desk", Connection: "missing"},
	} {
		if err := rs.PutTradingAccess(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("put %+v error = %v, want ErrNotFound", missing, err)
		}
		if err := rs.RemoveTradingAccess(ctx, missing.Connection, "", missing.AccountGroup); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("remove %+v error = %v, want ErrNotFound", missing, err)
		}
	}
	for _, invalid := range []domain.TradingAccess{
		{Connection: connection.ExternalID},
		{Account: "acc-1", AccountGroup: "other", Connection: connection.ExternalID},
		{AccountGroup: domain.ReservedGroupCode, Connection: connection.ExternalID},
	} {
		if err := rs.PutTradingAccess(ctx, invalid); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("put %+v error = %v, want ErrInvalid", invalid, err)
		}
		if err := rs.RemoveTradingAccess(ctx, invalid.Connection, invalid.Account, invalid.AccountGroup); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("remove %+v error = %v, want ErrInvalid", invalid, err)
		}
	}
	check("refused writes", other)
}

func TestListTradingAccessEffectiveGrants(t *testing.T) {
	ctx, rs, first := seedTradingFixtures(t)
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "Other", Mode: domain.TradingModePaper, VenueIdentity: "other-account",
		Credentials: `{}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"desk", "other"} {
		if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rs.SetAccountGroup(ctx, "acc-1", "desk"); err != nil {
		t.Fatal(err)
	}
	check := func(step string, want ...domain.TradingAccess) {
		t.Helper()
		want = append([]domain.TradingAccess{}, want...)
		sort.Slice(want, func(i, j int) bool { return want[i].Connection < want[j].Connection })
		got, err := rs.ListTradingAccess(ctx, "acc-1")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: effective grants = %+v, want %+v; err=%v", step, got, want, err)
		}
	}
	put := func(access domain.TradingAccess) {
		t.Helper()
		if err := rs.PutTradingAccess(ctx, access); err != nil {
			t.Fatal(err)
		}
	}
	deskFirst := domain.TradingAccess{AccountGroup: "desk", Connection: first.ExternalID, VenueAccount: "desk-venue"}
	put(deskFirst)
	check("group grant only", deskFirst)
	own := domain.TradingAccess{Account: "acc-1", Connection: first.ExternalID}
	put(own)
	check("own grant without venue account wins whole", own)
	own.VenueAccount = "own-venue"
	put(own)
	check("own grant with another venue account wins whole", own)
	deskSecond := domain.TradingAccess{AccountGroup: "desk", Connection: second.ExternalID, VenueAccount: "b"}
	put(deskSecond)
	otherFirst := domain.TradingAccess{AccountGroup: "other", Connection: first.ExternalID, VenueAccount: "other-venue"}
	put(otherFirst)
	check("own and group grants on different connections", own, deskSecond)
	if err := rs.RemoveTradingAccess(ctx, first.ExternalID, "acc-1", ""); err != nil {
		t.Fatal(err)
	}
	check("own grant removed falls back to the group's", deskFirst, deskSecond)
	if err := rs.SetAccountGroup(ctx, "acc-1", "other"); err != nil {
		t.Fatal(err)
	}
	check("moved to another group", otherFirst)
	if err := rs.SetAccountGroup(ctx, "acc-1", ""); err != nil {
		t.Fatal(err)
	}
	check("outside any group")
	if got, err := rs.ListTradingAccess(ctx, "missing"); err != nil || len(got) != 0 {
		t.Fatalf("unknown account grants = %+v, err=%v", got, err)
	}
}

func TestTradingAccessSchemaConstraints(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}); err != nil {
		t.Fatal(err)
	}
	db := rs.(*realmStore).rawDB()
	var accountRow, groupRow, connectionRow int64
	for _, lookup := range []struct {
		query string
		arg   any
		dest  *int64
	}{
		{`SELECT id FROM account WHERE code = ?`, "acc-1", &accountRow},
		{`SELECT id FROM account_group WHERE code = ?`, "desk", &groupRow},
		{`SELECT id FROM trading_connection WHERE external_id = ?`, connection.ExternalID.Bytes(), &connectionRow},
	} {
		if err := db.QueryRowContext(ctx, lookup.query, lookup.arg).Scan(lookup.dest); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(account, group any) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO trading_access
			 (account_id, account_group_id, connection_id, venue_account)
			 VALUES (?, ?, ?, '')`,
			account, group, connectionRow,
		)
		return err
	}
	if insert(nil, nil) == nil {
		t.Fatal("schema accepted a grant without a subject")
	}
	if insert(accountRow, groupRow) == nil {
		t.Fatal("schema accepted a grant with two subjects")
	}
	for _, subject := range [][2]any{{accountRow, nil}, {nil, groupRow}} {
		if err := insert(subject[0], subject[1]); err != nil {
			t.Fatal(err)
		}
		if insert(subject[0], subject[1]) == nil {
			t.Fatalf("schema accepted a second grant of one connection to %v", subject)
		}
	}
	if err := rs.DeleteGroup(ctx, "desk", false); err != nil {
		t.Fatalf("delete granted group: %v", err)
	}
	rows, err := rs.ListTradingAccessForConnection(ctx, connection.ExternalID)
	want := []store.TradingAccessListRow{{Access: domain.TradingAccess{Account: "acc-1", Connection: connection.ExternalID}}}
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("grants after group delete = %+v, want only the account grant; err=%v", rows, err)
	}
}

func TestVenueOrderCreateGetFindAndUniqueness(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	firstOrder := createTradingOrder(t, ctx, rs)
	secondOrder := createTradingOrder(t, ctx, rs)
	link := domain.VenueOrder{
		Order:         firstOrder.ExternalID,
		Connection:    connection.ExternalID,
		Route:         `{"time_in_force":"day"}`,
		ClientOrderID: "client-1",
	}
	created, err := rs.CreateVenueOrder(ctx, link)
	if err != nil {
		t.Fatalf("CreateVenueOrder: %v", err)
	}
	if created.CreatedAt.IsZero() || created.CreatedAt.Location() != time.UTC {
		t.Fatal("CreateVenueOrder did not stamp a UTC creation time")
	}
	got, ok, err := rs.GetVenueOrder(ctx, firstOrder.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetVenueOrder: ok=%v err=%v", ok, err)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Fatal("GetVenueOrder did not return a UTC creation time")
	}
	assertVenueOrderEqual(t, got, created)
	got, ok, err = rs.FindVenueOrderByClientID(
		ctx, connection.ExternalID, link.ClientOrderID,
	)
	if err != nil || !ok {
		t.Fatalf("FindVenueOrderByClientID: ok=%v err=%v", ok, err)
	}
	assertVenueOrderEqual(t, got, created)
	if _, ok, err := rs.GetVenueOrder(ctx, secondOrder.ExternalID); err != nil || ok {
		t.Fatalf("GetVenueOrder(missing): ok=%v err=%v", ok, err)
	}
	if _, ok, err := rs.FindVenueOrderByClientID(
		ctx, connection.ExternalID, "missing",
	); err != nil || ok {
		t.Fatalf("FindVenueOrderByClientID(missing): ok=%v err=%v", ok, err)
	}

	if _, err := rs.CreateVenueOrder(ctx, link); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate Officer order link error = %v", err)
	}
	duplicateClient := link
	duplicateClient.Order = secondOrder.ExternalID
	if _, err := rs.CreateVenueOrder(ctx, duplicateClient); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate client order id error = %v", err)
	}
	acknowledgedAtCreate := link
	acknowledgedAtCreate.Order = secondOrder.ExternalID
	acknowledgedAtCreate.ClientOrderID = "client-2"
	acknowledgedAtCreate.SendAttempted = true
	acknowledgedAtCreate.VenueOrderID = "venue-2"
	if _, err := rs.CreateVenueOrder(
		ctx, acknowledgedAtCreate,
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("pre-acknowledged create error = %v", err)
	}
	sendAttemptedAtCreate := link
	sendAttemptedAtCreate.Order = secondOrder.ExternalID
	sendAttemptedAtCreate.ClientOrderID = "client-3"
	sendAttemptedAtCreate.SendAttempted = true
	if _, err := rs.CreateVenueOrder(
		ctx, sendAttemptedAtCreate,
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("send-attempted create error = %v", err)
	}
	unknownOrder := link
	unknownOrder.Order = "missing"
	unknownOrder.ClientOrderID = "missing-order"
	if _, err := rs.CreateVenueOrder(ctx, unknownOrder); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown Officer order error = %v", err)
	}
	unknownConnection := link
	unknownConnection.Order = secondOrder.ExternalID
	unknownConnection.Connection = "missing"
	unknownConnection.ClientOrderID = "missing-connection"
	if _, err := rs.CreateVenueOrder(ctx, unknownConnection); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown venue connection error = %v", err)
	}
}

func TestVenueOrderAcknowledgementAndDelete(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	firstOrder := createTradingOrder(t, ctx, rs)
	secondOrder := createTradingOrder(t, ctx, rs)
	thirdOrder := createTradingOrder(t, ctx, rs)
	for i, order := range []domain.Order{firstOrder, secondOrder, thirdOrder} {
		if _, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
			Order:         order.ExternalID,
			Connection:    connection.ExternalID,
			Route:         `{}`,
			ClientOrderID: []string{"client-1", "client-2", "client-3"}[i],
		}); err != nil {
			t.Fatalf("CreateVenueOrder(%d): %v", i, err)
		}
	}
	if err := rs.SetVenueOrderID(ctx, firstOrder.ExternalID, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty venue order id error = %v", err)
	}
	if err := rs.SetVenueOrderID(ctx, "missing", "venue-missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing venue order link error = %v", err)
	}
	if err := rs.MarkVenueOrderSendAttempted(
		ctx, "missing",
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing send-attempted link error = %v", err)
	}
	if err := rs.MarkVenueOrderSendAttempted(
		ctx, thirdOrder.ExternalID,
	); err != nil {
		t.Fatalf("MarkVenueOrderSendAttempted: %v", err)
	}
	if err := rs.MarkVenueOrderSendAttempted(
		ctx, thirdOrder.ExternalID,
	); err != nil {
		t.Fatalf("MarkVenueOrderSendAttempted(idempotent): %v", err)
	}
	marked, ok, err := rs.GetVenueOrder(ctx, thirdOrder.ExternalID)
	if err != nil || !ok || !marked.SendAttempted || marked.VenueOrderID != "" {
		t.Fatalf("marked venue order: order=%+v ok=%v err=%v", marked, ok, err)
	}
	if err := rs.DeleteVenueOrder(
		ctx, thirdOrder.ExternalID,
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("delete send-attempted venue order error = %v", err)
	}
	if err := rs.SetVenueOrderID(ctx, firstOrder.ExternalID, "venue-1"); err != nil {
		t.Fatalf("SetVenueOrderID: %v", err)
	}
	acknowledged, ok, err := rs.GetVenueOrder(ctx, firstOrder.ExternalID)
	if err != nil || !ok || !acknowledged.SendAttempted {
		t.Fatalf(
			"acknowledged venue order: order=%+v ok=%v err=%v",
			acknowledged, ok, err,
		)
	}
	if err := rs.SetVenueOrderID(ctx, firstOrder.ExternalID, "venue-1"); err != nil {
		t.Fatalf("SetVenueOrderID(same): %v", err)
	}
	if err := rs.SetVenueOrderID(ctx, firstOrder.ExternalID, "venue-other"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("replace venue order id error = %v", err)
	}
	if err := rs.DeleteVenueOrder(ctx, firstOrder.ExternalID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("delete acknowledged venue order error = %v", err)
	}
	if err := rs.DeleteVenueOrder(ctx, secondOrder.ExternalID); err != nil {
		t.Fatalf("DeleteVenueOrder(unacknowledged): %v", err)
	}
	if err := rs.DeleteVenueOrder(ctx, secondOrder.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteVenueOrder(missing) error = %v", err)
	}
}

func TestListOpenVenueOrdersUsesDomainTerminalStatuses(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	tests := []struct {
		status domain.OrderStatus
		order  domain.ExternalID
	}{
		{status: domain.OrderStatusSubmitted},
		{status: domain.OrderStatusAccepted},
		{status: domain.OrderStatusRejected},
		{status: domain.OrderStatusCommitted},
		{status: domain.OrderStatusRolledBack},
		{status: domain.OrderStatusFilled},
		{status: domain.OrderStatusPartiallyFilled},
		{status: domain.OrderStatusCancelled},
	}
	for i := range tests {
		test := &tests[i]
		t.Run(string(test.status), func(t *testing.T) {
			order := createTradingOrder(t, ctx, rs)
			if err := rs.UpdateOrderStatus(
				ctx, order.ExternalID, test.status,
			); err != nil {
				t.Fatalf("UpdateOrderStatus: %v", err)
			}
			if _, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
				Order:         order.ExternalID,
				Connection:    connection.ExternalID,
				Route:         `{}`,
				ClientOrderID: string(test.status),
			}); err != nil {
				t.Fatalf("CreateVenueOrder: %v", err)
			}
			test.order = order.ExternalID
		})
	}
	sharedClientID := "shared-client"
	firstSharedOrder := createTradingOrder(t, ctx, rs)
	if _, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
		Order:         firstSharedOrder.ExternalID,
		Connection:    connection.ExternalID,
		Route:         `{}`,
		ClientOrderID: sharedClientID,
	}); err != nil {
		t.Fatalf("CreateVenueOrder(first shared client id): %v", err)
	}
	secondConnection, err := rs.CreateTradingConnection(
		ctx,
		domain.TradingConnection{
			Provider:      domain.TradingProviderAlpaca,
			Label:         "Alpaca second paper",
			VenueIdentity: "second-paper-account",
			Mode:          domain.TradingModePaper,
			Credentials:   `{"key":"second"}`,
			Enabled:       true,
		},
	)
	if err != nil {
		t.Fatalf("CreateTradingConnection(second): %v", err)
	}
	secondOrder := createTradingOrder(t, ctx, rs)
	if _, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
		Order:         secondOrder.ExternalID,
		Connection:    secondConnection.ExternalID,
		Route:         `{}`,
		ClientOrderID: sharedClientID,
	}); err != nil {
		t.Fatalf("CreateVenueOrder(second shared client id): %v", err)
	}

	open, err := rs.ListOpenVenueOrders(ctx, connection.ExternalID)
	if err != nil {
		t.Fatalf("ListOpenVenueOrders: %v", err)
	}
	openByOrder := make(map[domain.ExternalID]bool, len(open))
	for _, order := range open {
		if order.Connection != connection.ExternalID ||
			order.Order == secondOrder.ExternalID {
			t.Fatal("ListOpenVenueOrders returned another connection's row")
		}
		openByOrder[order.Order] = true
	}
	wantOpen := 1 // the requested connection's shared client id row
	for _, test := range tests {
		if !domain.OrderStatusTerminal(test.status) {
			wantOpen++
		}
	}
	if len(open) != wantOpen {
		t.Errorf("ListOpenVenueOrders returned %d rows, want %d", len(open), wantOpen)
	}
	if !sort.SliceIsSorted(open, func(i, j int) bool {
		if !open[i].CreatedAt.Equal(open[j].CreatedAt) {
			return open[i].CreatedAt.Before(open[j].CreatedAt)
		}
		return open[i].Order < open[j].Order
	}) {
		t.Error("ListOpenVenueOrders rows are not ordered by creation time, then order id")
	}
	for _, test := range tests {
		got := openByOrder[test.order]
		want := !domain.OrderStatusTerminal(test.status)
		if got != want {
			t.Errorf("status %s membership = %v, want %v", test.status, got, want)
		}
	}
	if !openByOrder[firstSharedOrder.ExternalID] {
		t.Error(
			"ListOpenVenueOrders omitted requested connection's shared client id row",
		)
	}
	secondOpen, err := rs.ListOpenVenueOrders(ctx, secondConnection.ExternalID)
	if err != nil {
		t.Fatalf("ListOpenVenueOrders(second): %v", err)
	}
	if len(secondOpen) != 1 || secondOpen[0].Order != secondOrder.ExternalID ||
		secondOpen[0].Connection != secondConnection.ExternalID {
		t.Fatal("ListOpenVenueOrders(second) did not isolate the connection")
	}
	firstFound, ok, err := rs.FindVenueOrderByClientID(
		ctx, connection.ExternalID, sharedClientID,
	)
	if err != nil || !ok || firstFound.Order != firstSharedOrder.ExternalID {
		t.Fatalf(
			"FindVenueOrderByClientID(first): order=%+v ok=%v err=%v",
			firstFound, ok, err,
		)
	}
	secondFound, ok, err := rs.FindVenueOrderByClientID(
		ctx, secondConnection.ExternalID, sharedClientID,
	)
	if err != nil || !ok || secondFound.Order != secondOrder.ExternalID {
		t.Fatalf(
			"FindVenueOrderByClientID(second): order=%+v ok=%v err=%v",
			secondFound, ok, err,
		)
	}
}

func assertVenueOrderEqual(
	t *testing.T, got, want domain.VenueOrder,
) {
	t.Helper()
	if got.Order != want.Order || got.Connection != want.Connection ||
		got.VenueAccount != want.VenueAccount || got.Route != want.Route ||
		got.ClientOrderID != want.ClientOrderID ||
		got.SendAttempted != want.SendAttempted ||
		got.VenueOrderID != want.VenueOrderID ||
		!got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatal("venue order round trip changed a field")
	}
}
