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

package node

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestTradingCredentialsSealed(t *testing.T) {
	n, realm := newTestNode(t, newFakeEngine())
	for _, sealed := range []bool{false, true} {
		realm.(*memoryRealm).credentialsSealed = sealed
		if got := n.CredentialsSealed(); got != sealed {
			t.Fatalf("CredentialsSealed = %v, want %v", got, sealed)
		}
	}
}

func TestTradingMutationsAuditAndGate(t *testing.T) {
	const credentials = `{"key":"do-not-audit-trading-secret"}`
	ctx := context.Background()
	for _, operation := range []string{"create", "settings", "enable", "fee account", "clear fee account", "upsert", "delete", "grant", "grant without venue account", "remove"} {
		t.Run(operation, func(t *testing.T) {
			n, realm := newTestNode(t, newFakeEngine())
			connection := domain.TradingConnection{
				ExternalID: "connection", Provider: domain.TradingProviderAlpaca,
				Label: "paper", Mode: domain.TradingModePaper, VenueIdentity: "venue-account",
				Credentials: credentials,
			}
			if operation != "create" {
				var err error
				connection, err = realm.CreateTradingConnection(ctx, connection)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := realm.CreateAccount(ctx, domain.Account{Code: "account"}); err != nil {
				t.Fatal(err)
			}
			if operation == "clear fee account" {
				if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "account"); err != nil {
					t.Fatal(err)
				}
			}
			instrument := domain.TradingInstrument{Connection: connection.ExternalID, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD"}
			access := domain.TradingAccess{Connection: connection.ExternalID, Account: "account", VenueAccount: "venue-subaccount"}
			if operation == "grant without venue account" {
				access.VenueAccount = ""
			}
			if operation == "delete" {
				if err := realm.UpsertTradingInstrument(ctx, instrument); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "remove" {
				if err := realm.PutTradingAccess(ctx, access); err != nil {
					t.Fatal(err)
				}
			}
			var mutate func() error
			var detail string
			switch operation {
			case "create":
				mutate = func() error { _, err := n.CreateTradingConnection(ctx, connection, testCaller); return err }
				detail = "create trading connection connection (alpaca, paper)"
			case "settings":
				mutate = func() error {
					return n.UpdateTradingConnectionSettings(ctx, connection.ExternalID, "renamed", credentials, testCaller)
				}
				detail = "update trading connection settings connection"
			case "enable":
				mutate = func() error { return n.SetTradingConnectionEnabled(ctx, connection.ExternalID, true, testCaller) }
				detail = "enable trading connection connection"
			case "fee account":
				mutate = func() error {
					return n.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "account", testCaller)
				}
				detail = "set trading connection fee account connection"
			case "clear fee account":
				mutate = func() error { return n.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "", testCaller) }
				detail = "clear trading connection fee account connection"
			case "upsert":
				mutate = func() error { return n.UpsertTradingInstrument(ctx, instrument, testCaller) }
				detail = "upsert trading instrument connection/AAPL"
			case "delete":
				mutate = func() error { return n.DeleteTradingInstrument(ctx, connection.ExternalID, "AAPL", testCaller) }
				detail = "delete trading instrument connection/AAPL"
			case "grant":
				mutate = func() error { return n.PutTradingAccess(ctx, access, true, testCaller) }
				detail = "put trading access connection/account/venue-subaccount"
			case "grant without venue account":
				mutate = func() error { return n.PutTradingAccess(ctx, access, true, testCaller) }
				detail = "put trading access connection/account"
			case "remove":
				mutate = func() error { return n.RemoveTradingAccess(ctx, access.Account, access.Connection, testCaller) }
				detail = "remove trading access connection/account"
			}
			beforeConnections, err := realm.ListTradingConnections(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeInstruments, err := realm.ListTradingInstruments(ctx, connection.ExternalID)
			if err != nil {
				t.Fatal(err)
			}
			beforeAccess, err := realm.ListTradingAccess(ctx, "account")
			if err != nil {
				t.Fatal(err)
			}
			n.restarting.Store(true)
			err = mutate()
			n.restarting.Store(false)
			if !errors.Is(err, domain.ErrEngineRestarting) {
				t.Fatalf("mutation gate error = %v, want ErrEngineRestarting", err)
			}
			afterConnections, err := realm.ListTradingConnections(ctx)
			if err != nil {
				t.Fatal(err)
			}
			afterInstruments, err := realm.ListTradingInstruments(ctx, connection.ExternalID)
			if err != nil {
				t.Fatal(err)
			}
			afterAccess, err := realm.ListTradingAccess(ctx, "account")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeConnections, afterConnections) || !reflect.DeepEqual(beforeInstruments, afterInstruments) || !reflect.DeepEqual(beforeAccess, afterAccess) {
				t.Fatal("rejected mutation wrote trading state")
			}
			rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
			if err != nil || len(rows) != 0 {
				t.Fatal("rejected mutation wrote audit")
			}
			if err := mutate(); err != nil {
				t.Fatalf("mutation failed: %v", err)
			}
			rows, err = realm.ListAuditFiltered(ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
			if err != nil || len(rows) != 1 {
				t.Fatalf("set_trading rows = %d, want exactly one; err=%v", len(rows), err)
			}
			if operation == "grant" || operation == "grant without venue account" || operation == "remove" || operation == "fee account" || operation == "clear fee account" {
				if rows[0].Account != access.Account {
					t.Errorf("audit account = %q, want %q", rows[0].Account, access.Account)
				}
			}
			if rows[0].Detail != detail {
				t.Fatalf("detail = %q, want %q", rows[0].Detail, detail)
			}
			if strings.Contains(rows[0].Detail, credentials) || strings.Contains(rows[0].Detail, "do-not-audit-trading-secret") {
				t.Fatal("audit contains credentials")
			}
			rejected := &rejectTradingRealm{RealmStore: realm}
			n.realm = rejected
			if err := mutate(); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("realm write error = %v, want ErrInvalid", err)
			}
			rows, err = realm.ListAuditFiltered(ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
			if err != nil || len(rows) != 1 {
				t.Fatal("failed realm write added an audit row")
			}
		})
	}
}

type rejectTradingRealm struct{ store.RealmStore }

func (r *rejectTradingRealm) SetTradingConnectionFeeAccount(context.Context, domain.ExternalID, domain.AccountID) error {
	return domain.ErrInvalid
}

func newTradingFeeAccountNode(t *testing.T) (*localNode, store.RealmStore, domain.TradingConnection) {
	t.Helper()
	n, realm := newTestNode(t, newFakeEngine())
	for _, account := range []domain.AccountID{"first", "second", "fees"} {
		if _, err := realm.CreateAccount(context.Background(), domain.Account{Code: account}); err != nil {
			t.Fatal(err)
		}
	}
	connection, err := realm.CreateTradingConnection(context.Background(), domain.TradingConnection{
		ExternalID: "connection", Provider: domain.TradingProviderAlpaca,
		Label: "paper", Mode: domain.TradingModePaper, VenueIdentity: "venue-account",
		Credentials: `{"key":"do-not-audit-trading-secret"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n, realm, connection
}

func TestTradingFeeAccountBindingRules(t *testing.T) {
	for _, tc := range []struct {
		name        string
		bound       []domain.AccountID
		feeAccount  domain.AccountID
		require     bool
		wantRefusal bool
	}{
		{name: "first unlinked binding", require: true},
		{name: "second unlinked binding refused", bound: []domain.AccountID{"second"}, require: true, wantRefusal: true},
		{name: "linked fees allow second binding", bound: []domain.AccountID{"second"}},
		{name: "fee account allows second binding", bound: []domain.AccountID{"second"}, feeAccount: "fees", require: true},
		{name: "re-put single binding", bound: []domain.AccountID{"first"}, require: true},
		{name: "re-put always allowed", bound: []domain.AccountID{"first", "second"}, require: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			n, realm, connection := newTradingFeeAccountNode(t)
			for _, account := range tc.bound {
				if err := realm.PutTradingAccess(ctx, domain.TradingAccess{Connection: connection.ExternalID, Account: account}); err != nil {
					t.Fatal(err)
				}
			}
			if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, tc.feeAccount); err != nil {
				t.Fatal(err)
			}
			access := domain.TradingAccess{Connection: connection.ExternalID, Account: "first", VenueAccount: "changed"}
			err := n.PutTradingAccess(ctx, access, tc.require, testCaller)
			if tc.wantRefusal {
				if !errors.Is(err, domain.ErrFeeAccountRequired) {
					t.Fatalf("second binding error = %v, want ErrFeeAccountRequired", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			accesses, err := realm.ListTradingAccessForConnection(ctx, connection.ExternalID)
			if err != nil {
				t.Fatal(err)
			}
			written := false
			for _, got := range accesses {
				if got == access {
					written = true
				}
			}
			if written == tc.wantRefusal {
				t.Fatalf("binding written = %v, refusal = %v", written, tc.wantRefusal)
			}
			rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantRefusal {
				if len(rows) != 0 {
					t.Fatal("refused binding was audited as a mutation")
				}
			} else if len(rows) != 1 || rows[0].Account != "first" || rows[0].Detail != "put trading access connection/first/changed" || rows[0].Actor != testCaller.Principal || rows[0].Source != testCaller.Source {
				t.Fatalf("binding audit = %+v", rows)
			}
		})
	}
}

func TestTradingFeeAccountClearingRules(t *testing.T) {
	for _, tc := range []struct {
		name        string
		bound       []domain.AccountID
		wantRefusal bool
	}{
		{name: "no bindings"},
		{name: "one binding", bound: []domain.AccountID{"first"}},
		{name: "duplicate same account", bound: []domain.AccountID{"first", "first"}},
		{name: "two distinct bindings", bound: []domain.AccountID{"first", "second"}, wantRefusal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			n, realm, connection := newTradingFeeAccountNode(t)
			for _, account := range tc.bound {
				realm.(*memoryRealm).tradingAccess = append(realm.(*memoryRealm).tradingAccess, domain.TradingAccess{Connection: connection.ExternalID, Account: account})
			}
			if err := n.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "fees", testCaller); err != nil {
				t.Fatalf("set fee account with bindings: %v", err)
			}
			rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
			if err != nil || len(rows) != 1 || rows[0].Account != "fees" || rows[0].Detail != "set trading connection fee account connection" || rows[0].Actor != testCaller.Principal || rows[0].Source != testCaller.Source {
				t.Fatalf("set fee account audit = %+v, err=%v", rows, err)
			}
			err = n.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "", testCaller)
			if tc.wantRefusal {
				if !errors.Is(err, domain.ErrFeeAccountRequired) {
					t.Fatalf("clear error = %v, want ErrFeeAccountRequired", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			got, found, err := realm.GetTradingConnection(ctx, connection.ExternalID)
			if err != nil || !found {
				t.Fatalf("connection: found=%v err=%v", found, err)
			}
			wantAccount, wantRows := domain.AccountID(""), 2
			if tc.wantRefusal {
				wantAccount, wantRows = "fees", 1
			}
			if got.FeeAccount != wantAccount {
				t.Fatalf("fee account = %q, want %q", got.FeeAccount, wantAccount)
			}
			rows, err = realm.ListAuditFiltered(ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
			if err != nil || len(rows) != wantRows {
				t.Fatalf("audit rows = %d, want %d, err=%v", len(rows), wantRows, err)
			}
			if !tc.wantRefusal && (rows[0].Account != "fees" || rows[0].Detail != "clear trading connection fee account connection") {
				t.Fatalf("clear audit = %+v, want previous account and clear detail", rows[0])
			}
			if tc.wantRefusal {
				if err := n.RemoveTradingAccess(ctx, "second", connection.ExternalID, testCaller); err != nil {
					t.Fatal(err)
				}
				if err := n.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "", testCaller); err != nil {
					t.Fatalf("clear after revocation: %v", err)
				}
			}
		})
	}
}

func TestTradingFeeAccountUnknownReferences(t *testing.T) {
	for _, tc := range []struct {
		name       string
		connection domain.ExternalID
		account    domain.AccountID
		want       error
	}{
		{"unknown connection", "missing", "fees", domain.ErrNotFound},
		{"clear unknown connection", "missing", "", domain.ErrNotFound},
		{"unknown account", "connection", "missing", domain.ErrNotFound},
		{"invalid account", "connection", " fees", domain.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, realm, _ := newTradingFeeAccountNode(t)
			if err := n.SetTradingConnectionFeeAccount(context.Background(), tc.connection, tc.account, testCaller); !errors.Is(err, tc.want) {
				t.Fatalf("set fee account error = %v, want %v", err, tc.want)
			}
			rows, err := realm.ListAuditFiltered(context.Background(), domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
			if err != nil || len(rows) != 0 {
				t.Fatalf("failed set fee account audit = %+v, err=%v", rows, err)
			}
		})
	}
}

func TestRecordVenueFeeSkipped(t *testing.T) {
	n, realm, connection := newTradingFeeAccountNode(t)
	caller := domain.Caller{Source: domain.SourceSystem}
	n.restarting.Store(true)
	if err := n.RecordVenueFeeSkipped(context.Background(), connection.ExternalID, "fee-id", "unmapped fee asset EUR", caller); !errors.Is(err, domain.ErrEngineRestarting) {
		t.Fatalf("restarting fee skip error = %v, want ErrEngineRestarting", err)
	}
	n.restarting.Store(false)
	rows, err := realm.ListAuditFiltered(context.Background(), domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionVenueFeeSkipped}}, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("refused fee skip wrote audit = %+v, err=%v", rows, err)
	}
	if err := n.RecordVenueFeeSkipped(context.Background(), connection.ExternalID, "fee-id", "unmapped fee asset EUR", caller); err != nil {
		t.Fatal(err)
	}
	rows, err = realm.ListAuditFiltered(context.Background(), domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionVenueFeeSkipped}}, 10)
	if err != nil || len(rows) != 1 || rows[0].Action.Category() != domain.AuditCategoryControl || rows[0].Account != "" || rows[0].Actor != "" || rows[0].Source != domain.SourceSystem || rows[0].Detail != "venue fee skipped connection connection fee fee-id: unmapped fee asset EUR" {
		t.Fatalf("venue fee skip audit = %+v, err=%v", rows, err)
	}
	if strings.Contains(rows[0].Detail, "do-not-audit-trading-secret") {
		t.Fatal("venue fee skip audit contains credentials")
	}
}

func TestMemoryTradingFeeAccountReferences(t *testing.T) {
	ctx := context.Background()
	_, realm, connection := newTradingFeeAccountNode(t)
	if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "fees"); err != nil {
		t.Fatal(err)
	}
	if _, err := realm.UpdateAccount(ctx, "fees", domain.Account{Code: "renamed"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := realm.GetTradingConnection(ctx, connection.ExternalID)
	if err != nil || !found || got.FeeAccount != "renamed" {
		t.Fatalf("renamed fee account = %q, found=%v err=%v", got.FeeAccount, found, err)
	}
	for _, force := range []bool{false, true} {
		if err := realm.DeleteAccount(ctx, "renamed", force); !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), connection.Label) {
			t.Fatalf("delete memory fee account force=%v: %v", force, err)
		}
	}
	if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, ""); err != nil {
		t.Fatal(err)
	}
	if err := realm.DeleteAccount(ctx, "renamed", true); err != nil {
		t.Fatalf("delete after clearing memory fee account: %v", err)
	}
}

func TestMemoryCreateTradingConnectionRefusesFeeAccount(t *testing.T) {
	for _, account := range []domain.AccountID{"fees", "missing"} {
		t.Run(account.String(), func(t *testing.T) {
			_, realm, connection := newTradingFeeAccountNode(t)
			connection.ExternalID = "refused"
			connection.Label, connection.VenueIdentity = "refused", "refused"
			connection.FeeAccount = account
			if _, err := realm.CreateTradingConnection(context.Background(), connection); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("create fee account error = %v, want ErrInvalid", err)
			}
			if _, found, err := realm.GetTradingConnection(context.Background(), connection.ExternalID); err != nil || found {
				t.Fatalf("refused create persisted connection: found=%v err=%v", found, err)
			}
		})
	}
}

func TestMemoryGetTradingFeeAccounts(t *testing.T) {
	ctx := context.Background()
	_, realm, connection := newTradingFeeAccountNode(t)
	if _, _, err := realm.GetTradingFeeAccounts(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown connection error = %v", err)
	}
	check := func(wantFee domain.AccountID, wantBound []domain.AccountID) {
		t.Helper()
		fee, bound, err := realm.GetTradingFeeAccounts(ctx, connection.ExternalID)
		if err != nil || fee != wantFee || !reflect.DeepEqual(bound, wantBound) {
			t.Fatalf("fee snapshot = %q, %v, err=%v; want %q, %v", fee, bound, err, wantFee, wantBound)
		}
	}
	check("", []domain.AccountID{})
	if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "fees"); err != nil {
		t.Fatal(err)
	}
	check("fees", []domain.AccountID{})
	realm.(*memoryRealm).tradingAccess = []domain.TradingAccess{
		{Connection: connection.ExternalID, Account: "second"},
		{Connection: connection.ExternalID, Account: "first", VenueAccount: "a"},
		{Connection: connection.ExternalID, Account: "first", VenueAccount: "b"},
		{Connection: "other", Account: "fees"},
	}
	check("fees", []domain.AccountID{"first", "second"})
	if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, ""); err != nil {
		t.Fatal(err)
	}
	check("", []domain.AccountID{"first", "second"})
}

func (r *rejectTradingRealm) CreateTradingConnection(ctx context.Context, connection domain.TradingConnection) (domain.TradingConnection, error) {
	return domain.TradingConnection{}, fmt.Errorf("rejected trading write: %w", domain.ErrInvalid)
}

func (r *rejectTradingRealm) UpdateTradingConnectionSettings(ctx context.Context, id domain.ExternalID, label, credentials string) error {
	return fmt.Errorf("rejected trading write: %w", domain.ErrInvalid)
}

func (r *rejectTradingRealm) SetTradingConnectionEnabled(ctx context.Context, id domain.ExternalID, enabled bool) error {
	return fmt.Errorf("rejected trading write: %w", domain.ErrInvalid)
}

func (r *rejectTradingRealm) UpsertTradingInstrument(ctx context.Context, instrument domain.TradingInstrument) error {
	return fmt.Errorf("rejected trading write: %w", domain.ErrInvalid)
}

func (r *rejectTradingRealm) DeleteTradingInstrument(ctx context.Context, connection domain.ExternalID, externalSymbol string) error {
	return fmt.Errorf("rejected trading write: %w", domain.ErrInvalid)
}

func (r *rejectTradingRealm) PutTradingAccess(ctx context.Context, access domain.TradingAccess) error {
	return fmt.Errorf("rejected trading write: %w", domain.ErrInvalid)
}

func (r *rejectTradingRealm) RemoveTradingAccess(ctx context.Context, account domain.AccountID, connection domain.ExternalID) error {
	return fmt.Errorf("rejected trading write: %w", domain.ErrInvalid)
}
