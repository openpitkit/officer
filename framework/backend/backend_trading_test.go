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

package backend

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

type tradingTestRuntime struct {
	TradingRuntime
	providers                                  []trading.Provider
	states                                     map[domain.ExternalID]trading.ConnectionState
	identity                                   string
	checkErr, restartErr, verifyErr, searchErr error
	checks                                     []domain.TradingConnection
	restarts                                   int
	verified, searched                         string
	searchEntered, searchRelease               chan struct{}
	symbolErr                                  func(domain.TradingConnection) error
}

func (rt *tradingTestRuntime) Providers() []trading.Provider { return rt.providers }
func (rt *tradingTestRuntime) ConnectionStates() map[domain.ExternalID]trading.ConnectionState {
	return rt.states
}
func (rt *tradingTestRuntime) CheckAccount(_ context.Context, c domain.TradingConnection) (trading.AccountInfo, error) {
	rt.checks = append(rt.checks, c)
	return trading.AccountInfo{Identity: rt.identity, Status: "ACTIVE"}, rt.checkErr
}
func (rt *tradingTestRuntime) Restart() error { rt.restarts++; return rt.restartErr }
func (rt *tradingTestRuntime) VerifySymbol(_ context.Context, connection domain.TradingConnection, external string) (trading.SymbolVerification, error) {
	rt.verified = external
	err := rt.verifyErr
	if rt.symbolErr != nil {
		err = rt.symbolErr(connection)
	}
	return trading.SymbolVerification{Exists: true, Tradable: true, Details: "stock"}, err
}
func (rt *tradingTestRuntime) SearchSymbols(_ context.Context, connection domain.TradingConnection, query string) ([]trading.SymbolMatch, error) {
	rt.searched = query
	if rt.searchEntered != nil {
		close(rt.searchEntered)
		<-rt.searchRelease
	}
	err := rt.searchErr
	if rt.symbolErr != nil {
		err = rt.symbolErr(connection)
	}
	return []trading.SymbolMatch{{Symbol: "AAPL", Name: "Apple", Tradable: true}}, err
}

type tradingTestNode struct {
	node.Node
	connections []domain.TradingConnection
	instruments []domain.TradingInstrument
	access      []domain.TradingAccess
	// groups maps an account to its current group; ListTradingAccess hands a
	// member its group's grants whole, as the store does.
	groups         map[domain.AccountID]string
	sealed         bool
	writes         []string
	callers        []domain.Caller
	requireFees    bool
	writeErr       error
	connectionRead func()
}

func (n *tradingTestNode) CredentialsSealed() bool { return n.sealed }
func (n *tradingTestNode) ListTradingConnections(context.Context) ([]domain.TradingConnection, error) {
	return n.connections, nil
}
func (n *tradingTestNode) GetTradingConnection(_ context.Context, id domain.ExternalID) (domain.TradingConnection, bool, error) {
	for _, c := range n.connections {
		if c.ExternalID == id {
			if n.connectionRead != nil {
				n.connectionRead()
			}
			return c, true, nil
		}
	}
	return domain.TradingConnection{}, false, nil
}
func (n *tradingTestNode) write(name string, caller domain.Caller) error {
	n.writes = append(n.writes, name)
	n.callers = append(n.callers, caller)
	return n.writeErr
}
func (n *tradingTestNode) CreateTradingConnection(_ context.Context, c domain.TradingConnection, caller domain.Caller) (domain.TradingConnection, error) {
	if c.FeeAccount != "" {
		return domain.TradingConnection{}, domain.ErrInvalid
	}
	if err := n.write("create", caller); err != nil {
		return domain.TradingConnection{}, err
	}
	for _, existing := range n.connections {
		if existing.Provider == c.Provider && existing.VenueIdentity == c.VenueIdentity {
			return domain.TradingConnection{}, domain.ErrAlreadyExists
		}
	}
	c.ExternalID = "created"
	n.connections = append(n.connections, c)
	return c, nil
}
func (n *tradingTestNode) UpdateTradingConnectionSettings(_ context.Context, id domain.ExternalID, label, credentials string, caller domain.Caller) error {
	if err := n.write("update", caller); err != nil {
		return err
	}
	for i := range n.connections {
		if n.connections[i].ExternalID == id {
			n.connections[i].Label = label
			n.connections[i].Credentials = credentials
			return nil
		}
	}
	return domain.ErrNotFound
}
func (n *tradingTestNode) SetTradingConnectionEnabled(_ context.Context, id domain.ExternalID, enabled bool, caller domain.Caller) error {
	if err := n.write("enable", caller); err != nil {
		return err
	}
	for i := range n.connections {
		if n.connections[i].ExternalID == id {
			n.connections[i].Enabled = enabled
			return nil
		}
	}
	return domain.ErrNotFound
}
func (n *tradingTestNode) ListTradingInstruments(_ context.Context, id domain.ExternalID) ([]domain.TradingInstrument, error) {
	var result []domain.TradingInstrument
	for _, inst := range n.instruments {
		if inst.Connection == id {
			result = append(result, inst)
		}
	}
	return result, nil
}
func (n *tradingTestNode) UpsertTradingInstrument(_ context.Context, inst domain.TradingInstrument, caller domain.Caller) error {
	if err := n.write("instrument", caller); err != nil {
		return err
	}
	n.instruments = append(n.instruments, inst)
	return nil
}
func (n *tradingTestNode) DeleteTradingInstrument(_ context.Context, id domain.ExternalID, external string, caller domain.Caller) error {
	if err := n.write("delete-instrument", caller); err != nil {
		return err
	}
	for i, inst := range n.instruments {
		if inst.Connection == id && inst.ExternalSymbol == external {
			n.instruments = append(n.instruments[:i], n.instruments[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func (n *tradingTestNode) ListTradingAccessForConnection(_ context.Context, id domain.ExternalID) ([]store.TradingAccessListRow, error) {
	var result []store.TradingAccessListRow
	for _, access := range n.access {
		if access.Connection == id {
			result = append(result, store.TradingAccessListRow{Access: access, Group: n.groups[access.Account]})
		}
	}
	return result, nil
}
func (n *tradingTestNode) ListTradingAccess(_ context.Context, account domain.AccountID) ([]domain.TradingAccess, error) {
	var result []domain.TradingAccess
	for _, access := range n.access {
		if access.Account == account || (access.AccountGroup != "" && access.AccountGroup == n.groups[account]) {
			result = append(result, access)
		}
	}
	return result, nil
}
func (n *tradingTestNode) PutTradingAccess(_ context.Context, access domain.TradingAccess, requireFeeAccount bool, caller domain.Caller) error {
	n.requireFees = requireFeeAccount
	if err := n.write("access", caller); err != nil {
		return err
	}
	n.access = append(n.access, access)
	return nil
}
func (n *tradingTestNode) RemoveTradingAccess(_ context.Context, id domain.ExternalID, account domain.AccountID, accountGroup string, caller domain.Caller) error {
	if err := n.write("remove-access", caller); err != nil {
		return err
	}
	subject := domain.TradingAccess{Account: account, AccountGroup: accountGroup}
	for i, access := range n.access {
		if access.Connection == id && access.SameSubject(subject) {
			n.access = append(n.access[:i], n.access[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func (n *tradingTestNode) SetTradingConnectionFeeAccount(_ context.Context, id domain.ExternalID, account domain.AccountID, caller domain.Caller) error {
	if err := n.write("fee-account", caller); err != nil {
		return err
	}
	for i := range n.connections {
		if n.connections[i].ExternalID == id {
			n.connections[i].FeeAccount = account
			return nil
		}
	}
	return domain.ErrNotFound
}
func (*tradingTestNode) GetAccountState(_ context.Context, account domain.AccountID) (domain.Account, node.AccountLimits, error) {
	if account != "acct-1" {
		return domain.Account{}, node.AccountLimits{}, domain.ErrNotFound
	}
	return domain.Account{Code: account}, node.AccountLimits{}, nil
}

func tradingTestConnection() domain.TradingConnection {
	return domain.TradingConnection{ExternalID: "connection-1", Provider: "venue", Label: "Paper", Mode: domain.TradingModePaper, VenueIdentity: "venue-1", Credentials: `{"apiKey":"test-key-value","apiSecret":"test-secret-value"}`, Enabled: true}
}

func newTradingTestService(t *testing.T) (*Service, *tradingTestNode, *tradingTestRuntime) {
	t.Helper()
	n := &tradingTestNode{}
	rt := &tradingTestRuntime{identity: "venue-1", providers: []trading.Provider{{Type: "venue", Title: "Venue", VenueAccounts: true, VerifiesSymbols: true, SearchesSymbols: true, UnlinkedFees: true}}}
	s, err := New(n, nil, nil, testLockSettlementPrice, WithTradingRuntime(rt))
	if err != nil {
		t.Fatal(err)
	}
	return s, n, rt
}

func TestTradingCreate(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enabled           bool
		mode              domain.TradingMode
		provider, label   string
		checkErr, wantErr error
	}{
		{name: "enabled", enabled: true, mode: domain.TradingModePaper, provider: " venue ", label: " Paper "},
		{name: "disabled", mode: domain.TradingModePaper, provider: "venue", label: "Paper"},
		{name: "live", mode: domain.TradingModeLive, provider: "venue", label: "Paper", wantErr: domain.ErrLiveTradingUnavailable},
		{name: "unknown-mode", mode: "other", provider: "venue", label: "Paper", wantErr: domain.ErrInvalid},
		{name: "unknown-provider", mode: domain.TradingModePaper, provider: "missing", label: "Paper", wantErr: domain.ErrInvalid},
		{name: "label-required", mode: domain.TradingModePaper, provider: "venue", label: " ", wantErr: domain.ErrInvalid},
		{name: "unsupported", mode: domain.TradingModePaper, provider: "venue", label: "Paper", checkErr: trading.ErrUnsupported, wantErr: domain.ErrInvalid},
		{name: "check-failure", mode: domain.TradingModePaper, provider: "venue", label: "Paper", checkErr: errors.New("test-secret-value"), wantErr: domain.ErrUpstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			rt.checkErr = tc.checkErr
			c := tradingTestConnection()
			c.VenueIdentity = "caller-supplied-identity"
			c.Enabled, c.Mode, c.Provider, c.Label = tc.enabled, tc.mode, tc.provider, tc.label
			c.Credentials = " " + c.Credentials + " "
			created, err := s.CreateTradingConnection(systemCtx(), c)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("create error = %v, want %v", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "test-secret-value") {
					t.Fatal("credential leaked in create error")
				}
				if len(n.writes) != 0 || rt.restarts != 0 {
					t.Fatal("refused create mutated state")
				}
				if tc.checkErr == nil && len(rt.checks) != 0 {
					t.Fatal("invalid create reached venue")
				}
				if tc.wantErr == domain.ErrUpstream && !strings.Contains(err.Error(), "check account for create") {
					t.Fatal("error omitted operation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if created.VenueIdentity != "venue-1" || created.Label != "Paper" || created.Provider != "venue" {
				t.Fatalf("incorrect created settings: identity=%s label=%s provider=%s", created.VenueIdentity, created.Label, created.Provider)
			}
			if created.Credentials != "" || strings.Contains(fmt.Sprintf("%#v", created), "test-secret-value") {
				t.Fatal("create returned credential values")
			}
			if n.connections[0].Credentials != tradingTestConnection().Credentials {
				t.Fatal("create did not store credentials")
			}
			if len(rt.checks) != 1 || rt.checks[0].VenueIdentity != "" {
				t.Fatal("venue did not identify the draft")
			}
			wantRestarts := 0
			if tc.enabled {
				wantRestarts = 1
			}
			if rt.restarts != wantRestarts || !reflect.DeepEqual(n.callers, []domain.Caller{auth.SystemCaller()}) {
				t.Fatal("incorrect restart or caller")
			}
			c.Label = "Other"
			if _, err := s.CreateTradingConnection(systemCtx(), c); !errors.Is(err, domain.ErrAlreadyExists) {
				t.Fatalf("duplicate identity = %v", err)
			}
			if len(n.connections) != 1 || rt.restarts != wantRestarts {
				t.Fatal("duplicate changed configuration")
			}
		})
	}
}

func TestTradingUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, label, credentials, identity string
		storedCredentials                  string
		wantChecks, wantRestarts           int
		wantErr, checkErr                  error
	}{
		{name: "label-only", label: " Renamed ", identity: "venue-1"},
		{name: "label-only-noncanonical", label: " Renamed ", credentials: `{}`, identity: "venue-1", storedCredentials: `{ "apiSecret": "test-secret-value", "apiKey": "test-key-value" }`},
		{name: "same-account", credentials: `{"apiKey":"new-key"}`, identity: "venue-1", wantChecks: 1, wantRestarts: 1},
		{name: "empty-value", credentials: `{"apiKey":""}`, identity: "venue-1"},
		{name: "other-account", label: "Renamed", credentials: `{"apiKey":"new-key"}`, identity: "venue-2", wantChecks: 1, wantErr: domain.ErrConflict},
		{name: "check-failure", credentials: `{"apiKey":"new-key"}`, checkErr: errors.New("test-secret-value"), wantChecks: 1, wantErr: domain.ErrUpstream},
		{name: "array-refused", credentials: `[]`, identity: "venue-1", wantErr: domain.ErrInvalid},
		{name: "null-refused", credentials: `null`, identity: "venue-1", wantErr: domain.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			n.connections = []domain.TradingConnection{tradingTestConnection()}
			if tc.storedCredentials != "" {
				n.connections[0].Credentials = tc.storedCredentials
			}
			before := n.connections[0]
			rt.identity = tc.identity
			rt.checkErr = tc.checkErr
			err := s.UpdateTradingConnectionSettings(systemCtx(), " connection-1 ", tc.label, tc.credentials)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("update = %v, want %v", err, tc.wantErr)
			}
			if len(rt.checks) != tc.wantChecks || rt.restarts != tc.wantRestarts {
				t.Fatalf("checks/restarts = %d/%d", len(rt.checks), rt.restarts)
			}
			if tc.wantErr != nil {
				if strings.Contains(err.Error(), "test-secret-value") {
					t.Fatal("credential leaked in update error")
				}
				if n.connections[0] != before || len(n.writes) != 0 {
					t.Fatal("refused update changed configuration")
				}
				return
			}
			got := n.connections[0]
			want := before
			if tc.storedCredentials != "" {
				want.Credentials = tradingTestConnection().Credentials
			}
			if tc.label != "" {
				want.Label = "Renamed"
			}
			if tc.wantChecks > 0 {
				want.Credentials = `{"apiKey":"new-key","apiSecret":"test-secret-value"}`
			}
			if got != want {
				t.Fatal("update lost stored settings or omitted credential keys")
			}
		})
	}
}

func TestTradingCreateRefusesFeeAccountBeforeVenue(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	c := tradingTestConnection()
	c.FeeAccount = "fee-account"
	_, err := s.CreateTradingConnection(systemCtx(), c)
	if len(rt.checks) != 0 || len(n.writes) != 0 || rt.restarts != 0 {
		t.Fatal("create with fee account reached venue or mutated state")
	}
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "set the fee account after creating the connection") {
		t.Fatalf("create with fee account = %v", err)
	}
}

func TestTradingSearchDoesNotBlockEnable(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	n.connections = []domain.TradingConnection{tradingTestConnection()}
	n.connections[0].Enabled = false
	rt.searchEntered = make(chan struct{})
	rt.searchRelease = make(chan struct{})
	searchDone := make(chan error, 1)
	go func() {
		_, err := s.SearchTradingSymbols(context.Background(), "connection-1", "AAPL")
		searchDone <- err
	}()
	<-rt.searchEntered
	enableDone := make(chan error, 1)
	go func() {
		enableDone <- s.SetTradingConnectionEnabled(systemCtx(), "connection-1", true)
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	blocked := false
	select {
	case err := <-enableDone:
		if err != nil {
			t.Error(err)
		}
	case <-timer.C:
		blocked = true
	}
	close(rt.searchRelease)
	if err := <-searchDone; err != nil {
		t.Error(err)
	}
	if blocked {
		<-enableDone
		t.Fatal("slow symbol search blocked connection enable")
	}
	if !n.connections[0].Enabled || rt.restarts != 1 {
		t.Fatal("enable did not save and restart while search was pending")
	}
}

func TestTradingStatusRedactsOverlappingCredentials(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	c := tradingTestConnection()
	// Array order makes the former short-first replacement deterministic.
	c.Credentials = `{"values":["overlap","overlap-long"],"empty":""}`
	n.connections = []domain.TradingConnection{c}
	rt.states = map[domain.ExternalID]trading.ConnectionState{
		c.ExternalID: {Unavailable: "denied overlap-long overlap"},
	}
	status, err := s.GetTrading(systemCtx())
	if err != nil {
		t.Fatal(err)
	}
	if got := status.Connections[0].Unavailable; got != "denied [redacted] [redacted]" {
		t.Fatalf("overlapping credential redaction = %q", got)
	}
}

func TestTradingCreateCredentialRefusalIsValidation(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	rt.checkErr = fmt.Errorf("trading: build connector: %w",
		domain.NewValidationError("/credentials/apiKey", "format", "key test-key-value is malformed"))
	_, err := s.CreateTradingConnection(systemCtx(), tradingTestConnection())
	if !errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrUpstream) {
		t.Fatalf("create error = %v, want validation and not upstream", err)
	}
	if got := domain.ValidationPointer(err); got != "/credentials/apiKey" {
		t.Fatalf("pointer = %q, want /credentials/apiKey", got)
	}
	if err.Error() != "trading: build connector: key [redacted] is malformed" {
		t.Fatalf("credential refusal text = %q, want the value redacted", err)
	}
	if len(n.writes) != 0 || rt.restarts != 0 {
		t.Fatal("refused create mutated state")
	}
}

func TestTradingCreateBlankCredentialKeepsRefusalText(t *testing.T) {
	s, _, rt := newTradingTestService(t)
	rt.checkErr = domain.NewValidationError("/credentials/apiKey", "required", "alpaca: apiKey is required")
	c := tradingTestConnection()
	c.Credentials = `{"apiKey":" ","apiSecret":"test-secret-value"}`
	_, err := s.CreateTradingConnection(systemCtx(), c)
	if err == nil || err.Error() != "alpaca: apiKey is required" {
		t.Fatalf("blank key refusal = %v, want the text intact", err)
	}
}

func TestTradingCreateProviderInvalidOutsideCredentialsIsUpstream(t *testing.T) {
	s, _, rt := newTradingTestService(t)
	rt.checkErr = fmt.Errorf("trading: provider %q returned no connector: %w", "venue", domain.ErrInvalid)
	_, err := s.CreateTradingConnection(systemCtx(), tradingTestConnection())
	if !errors.Is(err, domain.ErrUpstream) {
		t.Fatalf("create error = %v, want upstream", err)
	}
}

func TestTradingUpstreamCause(t *testing.T) {
	for _, cause := range []string{
		"apiKey and apiSecret are required",
		"401 unauthorized: test-key-value test-secret-value",
		"request timeout: test-key-value test-secret-value",
	} {
		for _, operation := range []string{"create", "update", "check", "verify", "search"} {
			t.Run(operation+"/"+strings.Split(cause, ":")[0], func(t *testing.T) {
				s, n, rt := newTradingTestService(t)
				n.connections = []domain.TradingConnection{tradingTestConnection()}
				rt.checkErr, rt.verifyErr, rt.searchErr = errors.New(cause), errors.New(cause), errors.New(cause)
				var err error
				switch operation {
				case "create":
					_, err = s.CreateTradingConnection(systemCtx(), tradingTestConnection())
				case "update":
					err = s.UpdateTradingConnectionSettings(systemCtx(), "connection-1", "", `{"extra":"value"}`)
				case "check":
					_, err = s.CheckTradingConnection(context.Background(), "connection-1")
				case "verify":
					_, err = s.VerifyTradingSymbol(context.Background(), "connection-1", "AAPL")
				case "search":
					_, err = s.SearchTradingSymbols(context.Background(), "connection-1", "AAPL")
				}
				if !errors.Is(err, domain.ErrUpstream) {
					t.Fatalf("upstream error = %v", err)
				}
				expected := strings.NewReplacer("test-key-value", "[redacted]", "test-secret-value", "[redacted]").Replace(cause)
				if !strings.Contains(err.Error(), expected) {
					t.Fatalf("cause missing from error: %v", err)
				}
				for _, secret := range []string{"test-key-value", "test-secret-value"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatal("upstream error leaked credentials")
					}
				}
			})
		}
	}
}

func TestTradingSymbolRotationUsesSnapshot(t *testing.T) {
	for _, operation := range []string{"verify", "search"} {
		t.Run(operation, func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			snapshot := tradingTestConnection()
			n.connections = []domain.TradingConnection{snapshot}
			const rotatedCredentials = `{"apiKey":"rotated-key-value","apiSecret":"rotated-secret-value"}`
			n.connectionRead = func() {
				n.connectionRead = nil
				if err := s.UpdateTradingConnectionSettings(systemCtx(), "connection-1", "", rotatedCredentials); err != nil {
					t.Fatal(err)
				}
			}
			rt.symbolErr = func(connection domain.TradingConnection) error {
				if !reflect.DeepEqual(connection, snapshot) {
					t.Error("runtime did not receive the service connection snapshot")
				}
				return fmt.Errorf("venue rejected credentials %s", connection.Credentials)
			}
			var err error
			switch operation {
			case "verify":
				_, err = s.VerifyTradingSymbol(context.Background(), "connection-1", "AAPL")
			case "search":
				_, err = s.SearchTradingSymbols(context.Background(), "connection-1", "AAPL")
			}
			if n.connections[0].Credentials != rotatedCredentials || rt.restarts != 1 {
				t.Fatal("credentials were not rotated between the service read and venue call")
			}
			if !errors.Is(err, domain.ErrUpstream) {
				t.Fatalf("upstream error = %v", err)
			}
			const expected = `venue rejected credentials {"apiKey":"[redacted]","apiSecret":"[redacted]"}`
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("redacted venue cause missing: %v", err)
			}
			for _, value := range []string{"test-key-value", "test-secret-value", "rotated-key-value", "rotated-secret-value"} {
				if strings.Contains(err.Error(), value) {
					t.Fatalf("upstream error leaked credentials: %v", err)
				}
			}
		})
	}
}

func TestTradingStatus(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	c := tradingTestConnection()
	c.Credentials = `{"apiKey":"test-key-value","apiSecret":"test-secret-value","empty":"","unset":null,"nested":{"password":"nested-secret"}}`
	n.connections = []domain.TradingConnection{c, c, c}
	n.connections[1].ExternalID = "connection-2"
	n.connections[2].ExternalID = "connection-3"
	n.connections[2].Enabled = false
	n.connections[0].FeeAccount = "fee-account"
	n.instruments = []domain.TradingInstrument{{Connection: c.ExternalID, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}}
	n.access = []domain.TradingAccess{{Account: "acct-1", Connection: c.ExternalID}, {AccountGroup: "desk", Connection: c.ExternalID, VenueAccount: "remote-1"}}
	n.groups = map[domain.AccountID]string{"acct-1": "desk"}
	wantAccess := []store.TradingAccessListRow{{Access: n.access[0], Group: "desk"}, {Access: n.access[1]}}
	rt.states = map[domain.ExternalID]trading.ConnectionState{c.ExternalID: {}, "connection-2": {Unavailable: "denied test-secret-value nested-secret"}}
	for _, sealed := range []bool{true, false} {
		n.sealed = sealed
		got, err := s.GetTrading(systemCtx())
		if err != nil {
			t.Fatal(err)
		}
		rendering := fmt.Sprintf("%#v", got)
		for _, secret := range []string{"test-secret-value", "test-key-value", "nested-secret"} {
			if strings.Contains(rendering, secret) {
				t.Fatal("status leaked credential value")
			}
		}
		if got.CredentialsSealed != sealed {
			t.Fatal("sealed flag did not pass through")
		}
		if got.Connections[0].State != "running" || got.Connections[1].State != "unavailable" || got.Connections[2].State != "stopped" {
			t.Fatal("runtime states were not mapped")
		}
		if got.Connections[1].Unavailable != "denied [redacted] [redacted]" {
			t.Fatalf("unavailable reason = %q", got.Connections[1].Unavailable)
		}
		if !reflect.DeepEqual(got.Connections[0].Secrets, map[string]bool{"apiKey": true, "apiSecret": true, "nested": true}) {
			t.Fatal("secret key presence is wrong")
		}
		if got.Connections[0].FeeAccount != "fee-account" || !reflect.DeepEqual(got.Connections[0].Instruments, n.instruments) {
			t.Fatal("status lost configuration")
		}
		if !reflect.DeepEqual(got.Connections[0].Access, wantAccess) {
			t.Fatalf("status access = %+v, want every grant with the granted account's group %+v", got.Connections[0].Access, wantAccess)
		}
		if len(got.Providers) != 1 || got.Providers[0] != (TradingProvider{Type: "venue", Title: "Venue", VenueAccounts: true, VerifiesSymbols: true, SearchesSymbols: true, UnlinkedFees: true}) {
			t.Fatal("provider capabilities were not mapped")
		}
	}
}

func TestTradingAccessAndFeeAccount(t *testing.T) {
	for _, unlinked := range []bool{true, false} {
		t.Run(fmt.Sprint(unlinked), func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			n.connections = []domain.TradingConnection{tradingTestConnection()}
			rt.providers[0].UnlinkedFees = unlinked
			access := domain.TradingAccess{Account: "acct-1", Connection: "connection-1"}
			if err := s.PutTradingAccess(systemCtx(), access); err != nil {
				t.Fatal(err)
			}
			if n.requireFees != unlinked || !reflect.DeepEqual(n.access, []domain.TradingAccess{access}) {
				t.Fatal("access did not pass provider fee policy")
			}
			for _, account := range []domain.AccountID{"fees", ""} {
				err := s.SetTradingFeeAccount(systemCtx(), access.Connection, account)
				if unlinked {
					if err != nil || n.connections[0].FeeAccount != account {
						t.Fatalf("fee account set/clear = %v", err)
					}
				} else if !errors.Is(err, domain.ErrInvalid) || len(n.writes) != 1 {
					t.Fatal("fee-less provider allowed fee account mutation")
				}
			}
			n.writeErr = domain.ErrFeeAccountRequired
			if err := s.PutTradingAccess(systemCtx(), access); !errors.Is(err, domain.ErrFeeAccountRequired) {
				t.Fatal("node fee rejection lost its sentinel")
			}
		})
	}
}

func TestTradingGroupAccess(t *testing.T) {
	for _, unlinked := range []bool{true, false} {
		t.Run(fmt.Sprint(unlinked), func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			n.connections = []domain.TradingConnection{tradingTestConnection()}
			rt.providers[0].UnlinkedFees = unlinked
			group := domain.TradingAccess{AccountGroup: "desk", Connection: "connection-1", VenueAccount: "remote-1"}
			if err := s.PutTradingAccess(systemCtx(), group); err != nil {
				t.Fatal(err)
			}
			if n.requireFees != unlinked || !reflect.DeepEqual(n.access, []domain.TradingAccess{group}) {
				t.Fatalf("group grant reached the node as %+v with requireFeeAccount=%t, want it unchanged with %t", n.access, n.requireFees, unlinked)
			}
			n.writeErr = domain.ErrFeeAccountRequired
			if err := s.PutTradingAccess(systemCtx(), group); !errors.Is(err, domain.ErrFeeAccountRequired) {
				t.Fatalf("node fee refusal of a group grant = %v, want its sentinel", err)
			}
		})
	}
}

func TestTradingRemoveAccessSubject(t *testing.T) {
	s, n, _ := newTradingTestService(t)
	n.connections = []domain.TradingConnection{tradingTestConnection()}
	account := domain.TradingAccess{Account: "desk", Connection: "connection-1"}
	group := domain.TradingAccess{AccountGroup: "desk", Connection: "connection-1"}
	n.access = []domain.TradingAccess{account, group}
	for _, tc := range []struct {
		name, account, accountGroup, pointer string
	}{
		{"neither", "", "", "/account"},
		{"both", "desk", "desk", "/accountGroup"},
	} {
		err := s.RemoveTradingAccess(systemCtx(), "connection-1", domain.AccountID(tc.account), tc.accountGroup)
		if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != tc.pointer {
			t.Fatalf("%s subject = %v at %q, want invalid at %s", tc.name, err, domain.ValidationPointer(err), tc.pointer)
		}
	}
	if len(n.writes) != 0 {
		t.Fatalf("malformed subject reached the node: %v", n.writes)
	}
	if err := s.RemoveTradingAccess(systemCtx(), "connection-1", "", "desk"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(n.access, []domain.TradingAccess{account}) {
		t.Fatalf("group revoke left %+v, want only the same-code account grant", n.access)
	}
}

func TestTradingDestinations(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	n.connections = []domain.TradingConnection{tradingTestConnection(), tradingTestConnection(), tradingTestConnection()}
	n.connections[0].Label = "Zebra"
	n.connections[1].ExternalID, n.connections[1].Label, n.connections[1].Mode = "connection-2", "Alpha", domain.TradingModeLive
	n.connections[2].ExternalID, n.connections[2].Label, n.connections[2].Enabled = "connection-3", "Beta", false
	// connection-2 reaches acct-1 through its group's grant, which the store
	// returns whole: AccountGroup set, Account empty.
	n.groups = map[domain.AccountID]string{"acct-1": "desk"}
	n.access = []domain.TradingAccess{{Account: "acct-1", Connection: "connection-1", VenueAccount: "remote-1"}, {AccountGroup: "desk", Connection: "connection-2", VenueAccount: "remote-2"}, {Account: "acct-1", Connection: "connection-3"}}
	rt.states = map[domain.ExternalID]trading.ConnectionState{"connection-1": {}, "connection-2": {Unavailable: "offline"}}
	for _, tc := range []struct {
		mode domain.TradingMode
		want []domain.ExternalID
	}{
		{"", []domain.ExternalID{"connection-2", "connection-3", "connection-1"}},
		{domain.TradingModePaper, []domain.ExternalID{"connection-3", "connection-1"}},
		{domain.TradingModeLive, []domain.ExternalID{"connection-2"}},
	} {
		got, err := s.ListTradingDestinations(systemCtx(), "acct-1", tc.mode)
		if err != nil {
			t.Fatal(err)
		}
		var ids []domain.ExternalID
		for _, d := range got {
			ids = append(ids, d.Connection)
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Fatalf("destinations mode=%s = %v", tc.mode, ids)
		}
		for _, d := range got {
			if d.Connection == "connection-2" && (d.Unavailable != "offline" || d.State != "unavailable" || d.VenueAccount != "remote-2") {
				t.Fatal("destination lost binding or runtime status")
			}
			if d.Connection == "connection-3" && (d.Enabled || d.State != "stopped") {
				t.Fatal("disabled destination was not preserved")
			}
		}
	}
	if _, err := s.ListTradingDestinations(systemCtx(), "missing", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account did not fail")
	}
	if _, err := s.ListTradingDestinations(systemCtx(), "acct-1", "other"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid mode did not fail")
	}
}

func TestTradingCapabilities(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	n.connections = []domain.TradingConnection{tradingTestConnection()}
	info, err := s.CheckTradingConnection(context.Background(), "connection-1")
	if err != nil || info.Identity != "venue-1" || rt.checks[0] != n.connections[0] {
		t.Fatal("account check did not use stored connection")
	}
	verified, err := s.VerifyTradingSymbol(context.Background(), "connection-1", " AAPL ")
	if err != nil || verified != (TradingSymbolVerification{Supported: true, Exists: true, Tradable: true, Details: "stock"}) || rt.verified != "AAPL" {
		t.Fatal("symbol verification failed")
	}
	searched, err := s.SearchTradingSymbols(context.Background(), "connection-1", " Apple ")
	if err != nil || !searched.Supported || len(searched.Matches) != 1 || searched.Matches[0].Symbol != "AAPL" || rt.searched != "Apple" {
		t.Fatal("symbol search failed")
	}
	rt.verifyErr, rt.searchErr = trading.ErrUnsupported, trading.ErrUnsupported
	verified, err = s.VerifyTradingSymbol(context.Background(), "connection-1", "AAPL")
	if err != nil || verified.Supported {
		t.Fatal("unsupported verification failed")
	}
	searched, err = s.SearchTradingSymbols(context.Background(), "connection-1", "Apple")
	if err != nil || searched.Supported || len(searched.Matches) != 0 {
		t.Fatal("unsupported search failed")
	}
	rt.checkErr, rt.verifyErr, rt.searchErr = errors.New("test-secret-value"), errors.New("test-secret-value"), errors.New("test-secret-value")
	for name, call := range map[string]func(string, string) error{
		"check": func(id, _ string) error { _, err := s.CheckTradingConnection(context.Background(), id); return err },
		"verify": func(id, query string) error {
			_, err := s.VerifyTradingSymbol(context.Background(), id, query)
			return err
		},
		"search": func(id, query string) error {
			_, err := s.SearchTradingSymbols(context.Background(), id, query)
			return err
		},
	} {
		if err := call("connection-1", "AAPL"); !errors.Is(err, domain.ErrUpstream) || strings.Contains(err.Error(), "test-secret-value") || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s error = %v", name, err)
		}
		if err := call("missing", "AAPL"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s missing connection = %v", name, err)
		}
		if name != "check" {
			if err := call("connection-1", " "); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("%s blank query = %v", name, err)
			}
		}
	}
}

func TestTradingMutations(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	n.connections = []domain.TradingConnection{tradingTestConnection()}
	ctx := auth.ContextWithCaller(context.Background(), domain.Caller{Source: domain.SourceAPI, Principal: "operator"})
	inst := domain.TradingInstrument{Connection: "connection-1", ExternalSymbol: " AAPL ", BaseAsset: " AAPL ", QuoteAsset: " USD ", Enabled: true}
	if err := s.UpsertTradingInstrument(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if n.instruments[0].ExternalSymbol != "AAPL" || n.instruments[0].BaseAsset != "AAPL" || n.instruments[0].QuoteAsset != "USD" {
		t.Fatal("instrument was not trimmed")
	}
	if err := s.DeleteTradingInstrument(ctx, " connection-1 ", " AAPL "); err != nil || len(n.instruments) != 0 {
		t.Fatal("instrument deletion failed")
	}
	inst.BaseAsset = "USD"
	if err := s.UpsertTradingInstrument(ctx, inst); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid instrument accepted")
	}
	if err := s.DeleteTradingInstrument(ctx, "connection-1", " "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("blank instrument accepted")
	}
	access := domain.TradingAccess{Account: "acct-1", Connection: "connection-1"}
	if err := s.PutTradingAccess(ctx, access); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTradingAccess(ctx, access.Connection, access.Account, ""); err != nil || len(n.access) != 0 {
		t.Fatal("access removal failed")
	}
	if err := s.SetTradingConnectionEnabled(ctx, " connection-1 ", false); err != nil || n.connections[0].Enabled || rt.restarts != 0 {
		t.Fatal("disable restarted or failed")
	}
	if err := s.SetTradingConnectionEnabled(ctx, "connection-1", true); err != nil || !n.connections[0].Enabled || rt.restarts != 1 {
		t.Fatal("enable did not restart")
	}
	if err := s.SetTradingConnectionEnabled(ctx, "missing", true); !errors.Is(err, domain.ErrNotFound) || rt.restarts != 1 {
		t.Fatal("failed enable restarted")
	}
	caller, _ := auth.CallerFromContext(ctx)
	for _, got := range n.callers {
		if got != caller {
			t.Fatal("mutation lost its caller")
		}
	}
}

func tradingTestCalls(s *Service, ctx context.Context) map[string]func() error {
	return map[string]func() error{
		"get":    func() error { _, err := s.GetTrading(ctx); return err },
		"create": func() error { _, err := s.CreateTradingConnection(ctx, tradingTestConnection()); return err },
		"update": func() error { return s.UpdateTradingConnectionSettings(ctx, "connection-1", "Other", "") },
		"enable": func() error { return s.SetTradingConnectionEnabled(ctx, "connection-1", true) },
		"check":  func() error { _, err := s.CheckTradingConnection(ctx, "connection-1"); return err },
		"instrument": func() error {
			return s.UpsertTradingInstrument(ctx, domain.TradingInstrument{Connection: "connection-1", ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD"})
		},
		"delete-instrument": func() error { return s.DeleteTradingInstrument(ctx, "connection-1", "AAPL") },
		"verify":            func() error { _, err := s.VerifyTradingSymbol(ctx, "connection-1", "AAPL"); return err },
		"search":            func() error { _, err := s.SearchTradingSymbols(ctx, "connection-1", "Apple"); return err },
		"access": func() error {
			return s.PutTradingAccess(ctx, domain.TradingAccess{Account: "acct-1", Connection: "connection-1"})
		},
		"remove-access": func() error { return s.RemoveTradingAccess(ctx, "connection-1", "acct-1", "") },
		"fee-account":   func() error { return s.SetTradingFeeAccount(ctx, "connection-1", "fees") },
		"destinations":  func() error { _, err := s.ListTradingDestinations(ctx, "acct-1", ""); return err },
	}
}

func TestTradingRequiresCaller(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	n.connections = []domain.TradingConnection{tradingTestConnection()}
	for name, call := range tradingTestCalls(s, context.Background()) {
		if name == "check" || name == "verify" || name == "search" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("missing caller = %v", err)
			}
		})
	}
	if len(n.writes) != 0 || len(rt.checks) != 0 || rt.restarts != 0 {
		t.Fatal("missing caller caused side effects")
	}
}

func TestTradingRequiresRuntime(t *testing.T) {
	s := &Service{scope: EveryAccount{}}
	for name, call := range tradingTestCalls(s, systemCtx()) {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, domain.ErrNotImplemented) || !strings.Contains(err.Error(), "trading runtime is not configured") {
				t.Fatalf("missing runtime = %v", err)
			}
		})
	}
}

func TestTradingRestartFailure(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	rt.restartErr = domain.ErrConflict
	if _, err := s.CreateTradingConnection(systemCtx(), tradingTestConnection()); !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "configuration was saved") {
		t.Fatalf("create restart failure = %v", err)
	}
	if len(n.connections) != 1 {
		t.Fatal("restart failure lost durable create")
	}
	if err := s.UpdateTradingConnectionSettings(systemCtx(), "created", "", `{"apiKey":"new-key"}`); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("update restart failure = %v", err)
	}
	if !strings.Contains(n.connections[0].Credentials, "new-key") {
		t.Fatal("restart failure lost durable update")
	}
	n.writeErr = domain.ErrAlreadyExists
	before := rt.restarts
	if err := s.UpdateTradingConnectionSettings(systemCtx(), "created", "", `{"apiKey":"another-key"}`); !errors.Is(err, domain.ErrAlreadyExists) || rt.restarts != before {
		t.Fatal("failed node update restarted runtime")
	}
}
