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
	for _, operation := range []string{"create", "settings", "enable", "fee account", "clear fee account", "upsert", "delete", "grant", "grant without venue account", "remove", "grant group", "remove group"} {
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
			// The account is a member of the group, so its effective grants
			// below also witness group grant writes.
			if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}); err != nil {
				t.Fatal(err)
			}
			if _, err := realm.CreateAccount(ctx, domain.Account{Code: "account", GroupCode: "desk"}); err != nil {
				t.Fatal(err)
			}
			if operation == "clear fee account" || operation == "grant group" {
				if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, "account"); err != nil {
					t.Fatal(err)
				}
			}
			instrument := domain.TradingInstrument{Connection: connection.ExternalID, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD"}
			access := domain.TradingAccess{Connection: connection.ExternalID, Account: "account", VenueAccount: "venue-subaccount"}
			if operation == "grant without venue account" {
				access.VenueAccount = ""
			}
			groupOperation := operation == "grant group" || operation == "remove group"
			if groupOperation {
				access.Account, access.AccountGroup = "", "desk"
			}
			if operation == "delete" {
				if err := realm.UpsertTradingInstrument(ctx, instrument); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "remove" || operation == "remove group" {
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
				mutate = func() error {
					return n.RemoveTradingAccess(ctx, access.Connection, access.Account, "", true, testCaller)
				}
				detail = "remove trading access connection/account"
			case "grant group":
				mutate = func() error { return n.PutTradingAccess(ctx, access, true, testCaller) }
				detail = "put trading access connection/group:desk/venue-subaccount"
			case "remove group":
				mutate = func() error {
					return n.RemoveTradingAccess(ctx, access.Connection, "", access.AccountGroup, true, testCaller)
				}
				detail = "remove trading access connection/group:desk"
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
				if rows[0].Account != access.Account || rows[0].Group != "" {
					t.Errorf("audit subject = %q/%q, want account %q", rows[0].Account, rows[0].Group, access.Account)
				}
			}
			if groupOperation && (rows[0].Account != "" || rows[0].Group != "desk") {
				t.Errorf("audit subject = %q/%q, want group desk", rows[0].Account, rows[0].Group)
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
		boundGroups []string
		// group, when set, makes the requested grant a grant to that group.
		group       string
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
		{name: "account refused beside a group grant", boundGroups: []string{"team"}, require: true, wantRefusal: true},
		{name: "account re-put beside a group grant", bound: []domain.AccountID{"first"}, boundGroups: []string{"team"}, require: true},
		{name: "first group grant refused", group: "team", require: true, wantRefusal: true},
		{name: "later group grant refused", bound: []domain.AccountID{"first"}, group: "team", require: true, wantRefusal: true},
		{name: "group re-put refused", boundGroups: []string{"team"}, group: "team", require: true, wantRefusal: true},
		{name: "fee account allows group grant", bound: []domain.AccountID{"second"}, group: "team", feeAccount: "fees", require: true},
		{name: "linked fees allow group grant", bound: []domain.AccountID{"second"}, group: "team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			n, realm, connection := newTradingFeeAccountNode(t)
			if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "team"}); err != nil {
				t.Fatal(err)
			}
			for _, account := range tc.bound {
				if err := realm.PutTradingAccess(ctx, domain.TradingAccess{Connection: connection.ExternalID, Account: account}); err != nil {
					t.Fatal(err)
				}
			}
			for _, group := range tc.boundGroups {
				if err := realm.PutTradingAccess(ctx, domain.TradingAccess{Connection: connection.ExternalID, AccountGroup: group}); err != nil {
					t.Fatal(err)
				}
			}
			if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, tc.feeAccount); err != nil {
				t.Fatal(err)
			}
			access := domain.TradingAccess{Connection: connection.ExternalID, Account: "first", VenueAccount: "changed"}
			wantDetail := "put trading access connection/first/changed"
			if tc.group != "" {
				access.Account, access.AccountGroup = "", tc.group
				wantDetail = "put trading access connection/group:team/changed"
			}
			err := n.PutTradingAccess(ctx, access, tc.require, testCaller)
			if tc.wantRefusal {
				if !errors.Is(err, domain.ErrFeeAccountRequired) {
					t.Fatalf("binding error = %v, want ErrFeeAccountRequired", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			grants, err := realm.ListTradingAccessForConnection(ctx, connection.ExternalID)
			if err != nil {
				t.Fatal(err)
			}
			written := false
			for _, got := range grants {
				if got.Access == access {
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
			} else if len(rows) != 1 || rows[0].Account != access.Account || rows[0].Group != access.AccountGroup || rows[0].Detail != wantDetail || rows[0].Actor != testCaller.Principal || rows[0].Source != testCaller.Source {
				t.Fatalf("binding audit = %+v", rows)
			}
		})
	}
}

func TestTradingAccessRejectsInvalidSubjectBeforeFeeRule(t *testing.T) {
	ctx := context.Background()
	n, realm, connection := newTradingFeeAccountNode(t)
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "team"}); err != nil {
		t.Fatal(err)
	}
	for _, access := range []domain.TradingAccess{
		{Connection: connection.ExternalID},
		{Connection: connection.ExternalID, Account: "first", AccountGroup: "team"},
		{Connection: connection.ExternalID, AccountGroup: domain.ReservedGroupCode},
	} {
		if err := n.PutTradingAccess(ctx, access, true, testCaller); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("put %+v error = %v, want ErrInvalid", access, err)
		}
		if err := n.RemoveTradingAccess(ctx, access.Connection, access.Account, access.AccountGroup, true, testCaller); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("remove %+v error = %v, want ErrInvalid", access, err)
		}
	}
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("refused subjects were audited: %+v, err=%v", rows, err)
	}
}

// failTradingAccessReadRealm fails every read of a connection's grants.
type failTradingAccessReadRealm struct {
	store.RealmStore
	err error
}

func (r *failTradingAccessReadRealm) ListTradingAccessForConnection(context.Context, domain.ExternalID) ([]store.TradingAccessListRow, error) {
	return nil, r.err
}

func TestTradingAccessGroupRefusalPrecedesGrantRead(t *testing.T) {
	ctx := context.Background()
	n, realm, connection := newTradingFeeAccountNode(t)
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "team"}); err != nil {
		t.Fatal(err)
	}
	readFailure := errors.New("grant read failed")
	n.realm = &failTradingAccessReadRealm{RealmStore: realm, err: readFailure}
	group := domain.TradingAccess{Connection: connection.ExternalID, AccountGroup: "team"}
	if err := n.PutTradingAccess(ctx, group, true, testCaller); err != domain.ErrFeeAccountRequired {
		t.Fatalf("group grant error = %v, want bare ErrFeeAccountRequired before the grant read", err)
	}
	account := domain.TradingAccess{Connection: connection.ExternalID, Account: "first"}
	if err := n.PutTradingAccess(ctx, account, true, testCaller); !errors.Is(err, readFailure) || !strings.Contains(err.Error(), "read trading access") {
		t.Fatalf("account grant error = %v, want the wrapped grant read failure", err)
	}
	grants, err := realm.ListTradingAccessForConnection(ctx, connection.ExternalID)
	if err != nil || len(grants) != 0 {
		t.Fatalf("refused grants were written: %+v, err=%v", grants, err)
	}
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionSetTrading}}, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("refused grants were audited: %+v, err=%v", rows, err)
	}
}

func TestTradingFeeAccountClearingRules(t *testing.T) {
	for _, tc := range []struct {
		name        string
		bound       []domain.AccountID
		groups      []string
		wantRefusal bool
		// revoke is the grant whose removal lets a refused clear through.
		revoke domain.TradingAccess
	}{
		{name: "no bindings"},
		{name: "one binding", bound: []domain.AccountID{"first"}},
		{name: "duplicate same account", bound: []domain.AccountID{"first", "first"}},
		{name: "two distinct bindings", bound: []domain.AccountID{"first", "second"}, wantRefusal: true, revoke: domain.TradingAccess{Account: "second"}},
		{name: "group grant with one member", groups: []string{"team"}, wantRefusal: true, revoke: domain.TradingAccess{AccountGroup: "team"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			n, realm, connection := newTradingFeeAccountNode(t)
			// One member keeps the sender count at one, so only the group
			// grant itself can refuse the clear.
			if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "team"}); err != nil {
				t.Fatal(err)
			}
			if err := realm.SetAccountGroup(ctx, "first", "team"); err != nil {
				t.Fatal(err)
			}
			for _, account := range tc.bound {
				realm.(*memoryRealm).tradingAccess = append(realm.(*memoryRealm).tradingAccess, domain.TradingAccess{Connection: connection.ExternalID, Account: account})
			}
			for _, group := range tc.groups {
				realm.(*memoryRealm).tradingAccess = append(realm.(*memoryRealm).tradingAccess, domain.TradingAccess{Connection: connection.ExternalID, AccountGroup: group})
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
				if err := n.RemoveTradingAccess(ctx, connection.ExternalID, tc.revoke.Account, tc.revoke.AccountGroup, true, testCaller); err != nil {
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

	// A granted group adds its current members; first also holds a grant of
	// its own and is counted once. An empty group adds nobody, not "".
	for _, group := range []string{"team", "empty"} {
		if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
			t.Fatal(err)
		}
		realm.(*memoryRealm).tradingAccess = append(realm.(*memoryRealm).tradingAccess,
			domain.TradingAccess{Connection: connection.ExternalID, AccountGroup: group})
	}
	for _, account := range []domain.AccountID{"first", "fees"} {
		if err := realm.SetAccountGroup(ctx, account, "team"); err != nil {
			t.Fatal(err)
		}
	}
	check("", []domain.AccountID{"fees", "first", "second"})
}

// TestMemoryTradingGroupAccess holds the memory realm to the SQLite contract
// for group grants: replacement and removal by subject, the effective list
// with the own grant winning whole, connection rows with membership, and a
// group delete cascading to the group's grants.
func TestMemoryTradingGroupAccess(t *testing.T) {
	ctx := context.Background()
	n, realm, first := newTradingFeeAccountNode(t)
	second, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
		ExternalID: "second", Provider: domain.TradingProviderAlpaca,
		Label: "second", Mode: domain.TradingModePaper, VenueIdentity: "second-account",
		Credentials: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"desk", "other"} {
		if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
			t.Fatal(err)
		}
	}
	if err := realm.SetAccountGroup(ctx, "first", "desk"); err != nil {
		t.Fatal(err)
	}
	put := func(access domain.TradingAccess) {
		t.Helper()
		if err := realm.PutTradingAccess(ctx, access); err != nil {
			t.Fatal(err)
		}
	}
	effective := func(step string, want ...domain.TradingAccess) {
		t.Helper()
		got, err := realm.ListTradingAccess(ctx, "first")
		if err != nil || !reflect.DeepEqual(got, append([]domain.TradingAccess{}, want...)) {
			t.Fatalf("%s: effective grants = %+v, want %+v; err=%v", step, got, want, err)
		}
	}

	deskFirst := domain.TradingAccess{AccountGroup: "desk", Connection: first.ExternalID, VenueAccount: "stale"}
	otherFirst := domain.TradingAccess{AccountGroup: "other", Connection: first.ExternalID, VenueAccount: "other-venue"}
	put(deskFirst)
	put(otherFirst)
	deskFirst.VenueAccount = "desk-venue"
	put(deskFirst)
	effective("group grant replaced in place, other group untouched", deskFirst)
	own := domain.TradingAccess{Account: "first", Connection: first.ExternalID}
	put(own)
	effective("own grant without venue account wins whole", own)
	deskSecond := domain.TradingAccess{AccountGroup: "desk", Connection: second.ExternalID, VenueAccount: "b"}
	put(deskSecond)
	effective("own and group grants on different connections", own, deskSecond)
	rows, err := realm.ListTradingAccessForConnection(ctx, first.ExternalID)
	want := []store.TradingAccessListRow{{Access: deskFirst}, {Access: otherFirst}, {Access: own, Group: "desk"}}
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("connection rows = %+v, want %+v; err=%v", rows, want, err)
	}

	if err := realm.RemoveTradingAccess(ctx, first.ExternalID, "first", ""); err != nil {
		t.Fatal(err)
	}
	effective("own grant removed falls back to the group's", deskFirst, deskSecond)
	if err := realm.RemoveTradingAccess(ctx, first.ExternalID, "", "other"); err != nil {
		t.Fatal(err)
	}
	effective("removing another group's grant keeps this one", deskFirst, deskSecond)
	if err := realm.RemoveTradingAccess(ctx, first.ExternalID, "", "other"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("absent group grant error = %v, want ErrNotFound", err)
	}
	if err := realm.SetAccountGroup(ctx, "first", ""); err != nil {
		t.Fatal(err)
	}
	effective("outside any group")

	if err := realm.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "missing", Connection: first.ExternalID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown group error = %v, want ErrNotFound", err)
	}
	// A default currency materialises the default group with an empty code;
	// the test node already holds the USD asset.
	if err := realm.SetGroupCurrency(ctx, "", "USD"); err != nil {
		t.Fatal(err)
	}
	if err := realm.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "", Connection: first.ExternalID}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("default group grant error = %v, want ErrInvalid", err)
	}

	// Deleting desk drops its grants on both connections, as SQLite's ON
	// DELETE CASCADE does, and keeps every other subject's grant; the clear
	// rule refused for desk's grant then lets the clear through.
	otherSecond := domain.TradingAccess{AccountGroup: "other", Connection: second.ExternalID}
	put(own)
	put(otherSecond)
	if err := realm.SetTradingConnectionFeeAccount(ctx, first.ExternalID, "fees"); err != nil {
		t.Fatal(err)
	}
	if err := n.SetTradingConnectionFeeAccount(ctx, first.ExternalID, "", testCaller); !errors.Is(err, domain.ErrFeeAccountRequired) {
		t.Fatalf("clear beside a group grant error = %v, want ErrFeeAccountRequired", err)
	}
	if err := realm.DeleteGroup(ctx, "desk", false); err != nil {
		t.Fatal(err)
	}
	for connection, want := range map[domain.ExternalID]store.TradingAccessListRow{
		first.ExternalID:  {Access: own},
		second.ExternalID: {Access: otherSecond},
	} {
		rows, err := realm.ListTradingAccessForConnection(ctx, connection)
		if err != nil || !reflect.DeepEqual(rows, []store.TradingAccessListRow{want}) {
			t.Fatalf("%s rows after group delete = %+v, want only %+v; err=%v", connection, rows, want, err)
		}
	}
	if err := n.SetTradingConnectionFeeAccount(ctx, first.ExternalID, "", testCaller); err != nil {
		t.Fatalf("clear after group delete: %v", err)
	}

	// Renaming desk to ops keeps its grants, as SQLite's grants follow the
	// group row the rename keeps: the member keeps access, the grant is
	// revoked by the new code, and a new desk inherits nothing.
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}); err != nil {
		t.Fatal(err)
	}
	if err := realm.SetAccountGroup(ctx, "first", "desk"); err != nil {
		t.Fatal(err)
	}
	if err := realm.RemoveTradingAccess(ctx, first.ExternalID, "first", ""); err != nil {
		t.Fatal(err)
	}
	put(deskFirst)
	put(deskSecond)
	if _, err := realm.UpdateGroup(ctx, "desk", domain.AccountGroup{Code: "ops"}); err != nil {
		t.Fatal(err)
	}
	opsFirst, opsSecond := deskFirst, deskSecond
	opsFirst.AccountGroup, opsSecond.AccountGroup = "ops", "ops"
	effective("renamed group keeps its grants", opsFirst, opsSecond)
	if err := realm.RemoveTradingAccess(ctx, first.ExternalID, "", "ops"); err != nil {
		t.Fatalf("revoke by the new code: %v", err)
	}
	effective("revoked by the new code", opsSecond)
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}); err != nil {
		t.Fatal(err)
	}
	if err := realm.SetAccountGroup(ctx, "first", "desk"); err != nil {
		t.Fatal(err)
	}
	effective("a new desk inherits nothing")
}

// tradingAgentState is what a refused trading write must leave as it was: the
// connection, its instrument mappings and grants, and the audit trail.
type tradingAgentState struct {
	connection  domain.TradingConnection
	instruments []domain.TradingInstrument
	grants      []store.TradingAccessListRow
	audit       []domain.AuditRow
}

// readTradingAgentState reads the tradingAgentState of connection from the
// store of n.
func readTradingAgentState(
	t *testing.T, n *localNode, connection domain.ExternalID,
) tradingAgentState {
	t.Helper()
	ctx := context.Background()
	stored, found, err := n.realm.GetTradingConnection(ctx, connection)
	if err != nil || !found {
		t.Fatalf("GetTradingConnection(%s): found=%v err=%v", connection, found, err)
	}
	instruments, err := n.realm.ListTradingInstruments(ctx, connection)
	if err != nil {
		t.Fatalf("ListTradingInstruments: %v", err)
	}
	grants, err := n.realm.ListTradingAccessForConnection(ctx, connection)
	if err != nil {
		t.Fatalf("ListTradingAccessForConnection: %v", err)
	}
	audit, err := n.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return tradingAgentState{
		connection: stored, instruments: instruments, grants: grants, audit: audit,
	}
}

// newAgentTradingNode builds a node holding the disabled connection of
// newTradingFeeAccountNode with the fee account fees, an AAPL mapping, a grant
// to the account first, and the group team, so that every trading write of
// TestLocalNode_AgentTradingWideningRefused succeeds for an operator.
func newAgentTradingNode(t *testing.T) (*localNode, domain.ExternalID) {
	t.Helper()
	ctx := context.Background()
	n, realm, connection := newTradingFeeAccountNode(t)
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "team"}); err != nil {
		t.Fatal(err)
	}
	if err := realm.UpsertTradingInstrument(ctx, domain.TradingInstrument{
		Connection: connection.ExternalID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := realm.PutTradingAccess(ctx, domain.TradingAccess{
		Connection: connection.ExternalID, Account: "first",
	}); err != nil {
		t.Fatal(err)
	}
	if err := realm.SetTradingConnectionFeeAccount(
		ctx, connection.ExternalID, "fees",
	); err != nil {
		t.Fatal(err)
	}
	return n, connection.ExternalID
}

// TestLocalNode_AgentTradingWideningRefused refuses an agent each trading
// write that widens what can be sent or changes how venue fees are booked - a
// grant to an account or a group, an enable of a disabled connection, a
// mapping's add, disable or delete, and a fee account's set or clear - before
// anything changes: no row and no audit entry. Every operator caller makes the
// same write as before.
func TestLocalNode_AgentTradingWideningRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		write func(*localNode, domain.ExternalID, domain.Caller) error
	}{
		{"grant an account", func(n *localNode, connection domain.ExternalID, caller domain.Caller) error {
			return n.PutTradingAccess(ctx, domain.TradingAccess{
				Connection: connection, Account: "second",
			}, true, caller)
		}},
		{"grant a group", func(n *localNode, connection domain.ExternalID, caller domain.Caller) error {
			return n.PutTradingAccess(ctx, domain.TradingAccess{
				Connection: connection, AccountGroup: "team",
			}, true, caller)
		}},
		{"enable the connection", func(n *localNode, connection domain.ExternalID, caller domain.Caller) error {
			return n.SetTradingConnectionEnabled(ctx, connection, true, caller)
		}},
		{"add a mapping", func(n *localNode, connection domain.ExternalID, caller domain.Caller) error {
			return n.UpsertTradingInstrument(ctx, domain.TradingInstrument{
				Connection: connection, ExternalSymbol: "AAPL.EU",
				BaseAsset: "AAPL", QuoteAsset: "EUR", Enabled: true,
			}, caller)
		}},
		{"disable a mapping", func(n *localNode, connection domain.ExternalID, caller domain.Caller) error {
			return n.UpsertTradingInstrument(ctx, domain.TradingInstrument{
				Connection: connection, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD",
			}, caller)
		}},
		{"delete a mapping", func(n *localNode, connection domain.ExternalID, caller domain.Caller) error {
			return n.DeleteTradingInstrument(ctx, connection, "AAPL", caller)
		}},
		{"set the fee account", func(n *localNode, connection domain.ExternalID, caller domain.Caller) error {
			return n.SetTradingConnectionFeeAccount(ctx, connection, "second", caller)
		}},
		{"clear the fee account", func(n *localNode, connection domain.ExternalID, caller domain.Caller) error {
			return n.SetTradingConnectionFeeAccount(ctx, connection, "", caller)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			t.Run("agent", func(t *testing.T) {
				t.Parallel()
				n, connection := newAgentTradingNode(t)
				before := readTradingAgentState(t, n, connection)
				requireLoosening(t, tc.name, tc.write(n, connection, agentCaller))
				if after := readTradingAgentState(t, n, connection); !reflect.DeepEqual(after, before) {
					t.Fatalf("refusal changed the trading state: before=%+v after=%+v", before, after)
				}
			})
			for name, caller := range operatorCallers {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					n, connection := newAgentTradingNode(t)
					before := readTradingAgentState(t, n, connection)
					if err := tc.write(n, connection, caller); err != nil {
						t.Fatalf("%s: %v", tc.name, err)
					}
					after := readTradingAgentState(t, n, connection)
					rows, err := n.realm.ListAuditFiltered(ctx, domain.AuditFilter{
						Actions: []domain.AuditAction{domain.AuditActionSetTrading},
					}, 10)
					if err != nil || len(rows) != 1 || rows[0].Source != caller.Source {
						t.Fatalf("%s audit = %+v err=%v, want one row from %s", tc.name, rows, err, caller.Source)
					}
					before.audit, after.audit = nil, nil
					if reflect.DeepEqual(after, before) {
						t.Fatalf("%s changed nothing: %+v", tc.name, after)
					}
				})
			}
		})
	}
}

// TestLocalNode_AgentTradingNarrowingPasses lets an agent narrow trading as
// before - revoke an account's grant or a group's, disable a connection - and
// refuses its enable of a connection that is already enabled, since the
// backend restarts the trading runtime after any enable.
func TestLocalNode_AgentTradingNarrowingPasses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, connection := newAgentTradingNode(t)
	if err := n.realm.PutTradingAccess(ctx, domain.TradingAccess{
		Connection: connection, AccountGroup: "team",
	}); err != nil {
		t.Fatal(err)
	}
	if err := n.realm.SetTradingConnectionEnabled(ctx, connection, true); err != nil {
		t.Fatal(err)
	}

	before := readTradingAgentState(t, n, connection)
	requireLoosening(t, "enable an enabled connection",
		n.SetTradingConnectionEnabled(ctx, connection, true, agentCaller))
	if after := readTradingAgentState(t, n, connection); !reflect.DeepEqual(after, before) {
		t.Fatalf("refusal changed the trading state: before=%+v after=%+v", before, after)
	}

	for _, write := range []struct {
		name string
		call func() error
	}{
		{"revoke an account grant", func() error {
			return n.RemoveTradingAccess(ctx, connection, "first", "", true, agentCaller)
		}},
		{"revoke a group grant", func() error {
			return n.RemoveTradingAccess(ctx, connection, "", "team", true, agentCaller)
		}},
		{"disable the connection", func() error {
			return n.SetTradingConnectionEnabled(ctx, connection, false, agentCaller)
		}},
	} {
		if err := write.call(); err != nil {
			t.Fatalf("%s: %v", write.name, err)
		}
	}
	after := readTradingAgentState(t, n, connection)
	if after.connection.Enabled || len(after.grants) != 0 {
		t.Fatalf(
			"after the agent narrowed: connection=%+v grants=%+v, want it disabled with no grant",
			after.connection, after.grants,
		)
	}
	rows, err := n.realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionSetTrading},
	}, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("narrowing audit = %+v err=%v, want three rows", rows, err)
	}
	for _, row := range rows {
		if row.Source != domain.SourceMCP {
			t.Fatalf("narrowing audit row %+v, want the agent's", row)
		}
	}
}

// newLastSenderNode builds the node of newTradingFeeAccountNode holding the
// grants, the group team with the accounts members, and the fee account
// feeAccount, written to the store directly so that no grant rule applies.
func newLastSenderNode(
	t *testing.T, grants []domain.TradingAccess, members []domain.AccountID,
	feeAccount domain.AccountID,
) (*localNode, domain.ExternalID) {
	t.Helper()
	ctx := context.Background()
	n, realm, connection := newTradingFeeAccountNode(t)
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "team"}); err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if err := realm.SetAccountGroup(ctx, member, "team"); err != nil {
			t.Fatal(err)
		}
	}
	for _, grant := range grants {
		grant.Connection = connection.ExternalID
		if err := realm.PutTradingAccess(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := realm.SetTradingConnectionFeeAccount(ctx, connection.ExternalID, feeAccount); err != nil {
		t.Fatal(err)
	}
	return n, connection.ExternalID
}

// TestLocalNode_AgentLastFeeSenderRevokeRefused refuses an agent, on a
// provider with unlinked fees, the revoke after which no grant of a connection
// without a fee account reaches an account while before it they reached
// exactly one, as the connection's venue fees would then be skipped instead of
// debited: no grant, fee account or audit row changes, and a failed read of
// the grants is returned, never passed. A revoke that leaves an account able
// to send, one from two reached accounts to none, whose fees are skipped
// already, one on a connection with a fee account, one on a provider without
// unlinked fees, and every operator caller's revoke pass.
func TestLocalNode_AgentLastFeeSenderRevokeRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first := domain.TradingAccess{Account: "first"}
	team := domain.TradingAccess{AccountGroup: "team"}
	for _, tc := range []struct {
		name       string
		grants     []domain.TradingAccess
		members    []domain.AccountID
		feeAccount domain.AccountID
		require    bool
		revoke     domain.TradingAccess
		refused    bool
	}{
		{name: "the only account grant", grants: []domain.TradingAccess{first}, require: true, revoke: first, refused: true},
		{name: "the only group grant", grants: []domain.TradingAccess{team}, members: []domain.AccountID{"first"}, require: true, revoke: team, refused: true},
		{name: "the only group grant to two members", grants: []domain.TradingAccess{team}, members: []domain.AccountID{"first", "second"}, require: true, revoke: team},
		{name: "beside an empty group's grant", grants: []domain.TradingAccess{first, team}, require: true, revoke: first, refused: true},
		{name: "beside a member's group grant", grants: []domain.TradingAccess{first, team}, members: []domain.AccountID{"second"}, require: true, revoke: first},
		{name: "with a fee account", grants: []domain.TradingAccess{first}, feeAccount: "fees", require: true, revoke: first},
		{name: "on linked fees", grants: []domain.TradingAccess{first}, revoke: first},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, connection := newLastSenderNode(t, tc.grants, tc.members, tc.feeAccount)
			before := readTradingAgentState(t, n, connection)
			err := n.RemoveTradingAccess(
				ctx, connection, tc.revoke.Account, tc.revoke.AccountGroup, tc.require, agentCaller,
			)
			after := readTradingAgentState(t, n, connection)
			if tc.refused {
				requireLoosening(t, tc.name, err)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("refusal changed the trading state: before=%+v after=%+v", before, after)
				}
				return
			}
			if err != nil {
				t.Fatalf("agent revoke %s: %v", tc.name, err)
			}
			if len(after.grants) != len(before.grants)-1 || len(after.audit) != len(before.audit)+1 {
				t.Fatalf("agent revoke %s left grants %+v and audit %+v", tc.name, after.grants, after.audit)
			}
		})
	}
	t.Run("failed grant read", func(t *testing.T) {
		t.Parallel()
		n, connection := newLastSenderNode(t, []domain.TradingAccess{first}, nil, "")
		before := readTradingAgentState(t, n, connection)
		readFailure := errors.New("grant read failed")
		realm := n.realm
		n.realm = &failTradingAccessReadRealm{RealmStore: realm, err: readFailure}
		err := n.RemoveTradingAccess(ctx, connection, "first", "", true, agentCaller)
		n.realm = realm
		if !errors.Is(err, readFailure) {
			t.Fatalf("revoke over a failed grant read = %v, want the read failure", err)
		}
		if after := readTradingAgentState(t, n, connection); !reflect.DeepEqual(after, before) {
			t.Fatalf("failed read changed the trading state: before=%+v after=%+v", before, after)
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, connection := newLastSenderNode(t, []domain.TradingAccess{first}, nil, "")
			before := readTradingAgentState(t, n, connection)
			if err := n.RemoveTradingAccess(ctx, connection, "first", "", true, caller); err != nil {
				t.Fatalf("%s revoke of the last sender: %v", name, err)
			}
			after := readTradingAgentState(t, n, connection)
			if len(after.grants) != 0 || len(after.audit) != len(before.audit)+1 {
				t.Fatalf("%s revoke left grants %+v and audit %+v", name, after.grants, after.audit)
			}
		})
	}
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

func (r *rejectTradingRealm) RemoveTradingAccess(ctx context.Context, connection domain.ExternalID, account domain.AccountID, accountGroup string) error {
	return fmt.Errorf("rejected trading write: %w", domain.ErrInvalid)
}
