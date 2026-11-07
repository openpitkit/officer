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
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

type tradingTestRuntime struct {
	TradingRuntime
	providers                                  []trading.Provider
	states                                     map[domain.ExternalID]trading.ConnectionStatus
	identity                                   string
	checkErr, restartErr, verifyErr, searchErr error
	checks                                     []domain.TradingConnection
	restarts                                   int
	verified, searched                         string
	searchEntered, searchRelease               chan struct{}
	symbolErr                                  func(domain.TradingConnection) error
}

func (rt *tradingTestRuntime) Providers() []trading.Provider { return rt.providers }
func (rt *tradingTestRuntime) ConnectionStatuses() map[domain.ExternalID]trading.ConnectionStatus {
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
func (n *tradingTestNode) RemoveTradingAccess(_ context.Context, id domain.ExternalID, account domain.AccountID, accountGroup string, requireFeeAccount bool, caller domain.Caller) error {
	n.requireFees = requireFeeAccount
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

func newTradingTestService(t *testing.T, opts ...Option) (*Service, *tradingTestNode, *tradingTestRuntime) {
	t.Helper()
	n := &tradingTestNode{}
	rt := &tradingTestRuntime{identity: "venue-1", providers: []trading.Provider{{Type: "venue", Title: "Venue", VenueAccounts: true, VerifiesSymbols: true, SearchesSymbols: true, UnlinkedFees: true}}}
	s, err := New(n, nil, nil, testLockSettlementPrice, append([]Option{WithTradingRuntime(rt)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return s, n, rt
}

func TestTradingNonReleaseConfirmation(t *testing.T) {
	for _, operation := range []string{"create", "enable"} {
		for _, nonRelease := range []bool{false, true} {
			for name, caller := range map[string]domain.Caller{
				"panel":  tradingTestOperators["panel"],
				"api":    tradingTestOperators["api"],
				"system": tradingTestOperators["system"],
				"mcp":    tradingTestAgent,
			} {
				for _, enabled := range []bool{false, true} {
					for _, confirmed := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/non-release=%t/%s/enabled=%t/confirmed=%t",
							operation, nonRelease, name, enabled, confirmed), func(t *testing.T) {
							var opts []Option
							if nonRelease {
								opts = append(opts, WithNonReleaseBuild())
							}
							s, n, rt := newTradingTestService(t, opts...)
							ctx := auth.ContextWithCaller(context.Background(), caller)
							c := tradingTestConnection()
							c.Enabled = enabled
							var wantErr error
							if caller.TightenOnly() && (operation == "create" || enabled) {
								wantErr = domain.ErrLoosening
							} else if nonRelease && enabled && (name != "panel" || !confirmed) {
								wantErr = domain.ErrNonReleaseConfirmation
							}
							var err error
							wantWrites, wantChecks, wantRestarts := 0, 0, 0
							if operation == "create" {
								_, err = s.CreateTradingConnection(ctx, c, confirmed)
							} else {
								c.Enabled = !enabled
								n.connections = []domain.TradingConnection{c}
								if caller.TightenOnly() && enabled {
									// The node enforces tighten-only enables.
									n.writeErr = domain.ErrLoosening
									wantWrites = 1
								}
								err = s.SetTradingConnectionEnabled(ctx, c.ExternalID.String(), enabled, confirmed)
							}
							if !errors.Is(err, wantErr) {
								t.Fatalf("%s = %v, want %v", operation, err, wantErr)
							}
							if wantErr == nil {
								wantWrites = 1
								if operation == "create" {
									wantChecks = 1
								}
								if enabled {
									wantRestarts = 1
								}
							}
							if len(rt.checks) != wantChecks || len(n.writes) != wantWrites || rt.restarts != wantRestarts {
								t.Fatalf("%s: venue checks %d, writes %v, restarts %d; want %d, %d, %d",
									operation, len(rt.checks), n.writes, rt.restarts, wantChecks, wantWrites, wantRestarts)
							}
							wantConnections := 1
							if operation == "create" && wantErr != nil {
								wantConnections = 0
							}
							if len(n.connections) != wantConnections {
								t.Fatalf("connections = %d, want %d", len(n.connections), wantConnections)
							}
							if wantConnections != 0 {
								wantEnabled := enabled
								if wantErr != nil {
									wantEnabled = !enabled
								}
								if n.connections[0].Enabled != wantEnabled {
									t.Fatalf("stored enabled = %t, want %t", n.connections[0].Enabled, wantEnabled)
								}
							}
							if wantErr == domain.ErrLoosening && errors.Is(err, domain.ErrNonReleaseConfirmation) {
								t.Fatalf("tighten-only refusal also wraps ErrNonReleaseConfirmation: %v", err)
							}
						})
					}
				}
			}
		}
	}
}

func TestTradingNonReleaseCreateConfirmationBeforeValidation(t *testing.T) {
	s, n, rt := newTradingTestService(t, WithNonReleaseBuild())
	c := tradingTestConnection()
	c.Mode = "invalid"
	ctx := auth.ContextWithCaller(context.Background(), tradingTestOperators["panel"])
	_, err := s.CreateTradingConnection(ctx, c, false)
	if !errors.Is(err, domain.ErrNonReleaseConfirmation) {
		t.Fatalf("create invalid settings without confirmation = %v, want ErrNonReleaseConfirmation", err)
	}
	if len(rt.checks) != 0 || len(n.writes) != 0 || rt.restarts != 0 {
		t.Fatalf("refused create: venue checks %d, writes %v, restarts %d, want none",
			len(rt.checks), n.writes, rt.restarts)
	}
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
		{name: "live", enabled: true, mode: domain.TradingModeLive, provider: "venue", label: "Live"},
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
			created, err := s.CreateTradingConnection(systemCtx(), c, false)
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
			if created.VenueIdentity != "venue-1" || created.Label != strings.TrimSpace(tc.label) ||
				created.Provider != "venue" || created.Mode != tc.mode {
				t.Fatalf("incorrect created settings: identity=%s label=%s provider=%s mode=%s",
					created.VenueIdentity, created.Label, created.Provider, created.Mode)
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
			if _, err := s.CreateTradingConnection(systemCtx(), c, false); !errors.Is(err, domain.ErrAlreadyExists) {
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

func TestTradingUpdateKeepsCredentialIntegersExact(t *testing.T) {
	s, n, _ := newTradingTestService(t)
	n.connections = []domain.TradingConnection{tradingTestConnection()}
	n.connections[0].Credentials = `{"apiKey":"test-key-value","apiSecret":"test-secret-value","stored":9007199254740993}`
	// 2^53+1 is the smallest integer a float64 round trip rewrites.
	if err := s.UpdateTradingConnectionSettings(systemCtx(), "connection-1", "", `{"apiKey":"new-key","updated":9007199254740993}`); err != nil {
		t.Fatal(err)
	}
	want := `{"apiKey":"new-key","apiSecret":"test-secret-value","stored":9007199254740993,"updated":9007199254740993}`
	if got := n.connections[0].Credentials; got != want {
		t.Fatalf("credentials = %s, want %s", got, want)
	}
}

func TestTradingCreateRefusesFeeAccountBeforeVenue(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	c := tradingTestConnection()
	c.FeeAccount = "fee-account"
	_, err := s.CreateTradingConnection(systemCtx(), c, false)
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
		enableDone <- s.SetTradingConnectionEnabled(systemCtx(), "connection-1", true, false)
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
		//nolint:staticcheck // S1005: drain before reporting the blocked call.
		_ = <-enableDone
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
	rt.states = map[domain.ExternalID]trading.ConnectionStatus{
		c.ExternalID: {State: trading.ConnectionUnavailable, Error: "denied overlap-long overlap"},
	}
	status, err := s.GetTrading(systemCtx())
	if err != nil {
		t.Fatal(err)
	}
	if got := status.Connections[0].Error; got != "denied [redacted] [redacted]" {
		t.Fatalf("overlapping credential redaction = %q", got)
	}
}

// tradingTestEchoed is a credential value whose raw, JSON-escaped (HTML-escaped
// or not), URL query-escaped and URL path-escaped spellings all differ.
const tradingTestEchoed = `echo "q" \ <a&b> +c/d`

// tradingTestEchoedCredentials stores tradingTestEchoed as the apiSecret.
const tradingTestEchoedCredentials = `{"apiKey":"test-key-value","apiSecret":"echo \"q\" \\ <a&b> +c/d"}`

type tradingTestSpelling struct{ form, text string }

// tradingTestEchoedSpellings spells tradingTestEchoed in each form the backend
// redacts. No row contains another, so each form is checked on its own.
var tradingTestEchoedSpellings = []tradingTestSpelling{
	{"raw", `echo "q" \ <a&b> +c/d`},
	{"json", `echo \"q\" \\ \u003ca\u0026b\u003e +c/d`},
	{"json_unescaped_html", `echo \"q\" \\ <a&b> +c/d`},
	{"url_query", `echo+%22q%22+%5C+%3Ca%26b%3E+%2Bc%2Fd`},
	{"url_path", `echo%20%22q%22%20%5C%20%3Ca&b%3E%20+c%2Fd`},
}

func tradingTestAssertRedacted(t *testing.T, where, text string, spellings []tradingTestSpelling) {
	t.Helper()
	for _, spelling := range spellings {
		if strings.Contains(text, spelling.text) {
			t.Fatalf("%s keeps the %s spelling: %q", where, spelling.form, text)
		}
	}
	if !strings.Contains(text, "[redacted]") {
		t.Fatalf("%s lacks the redaction placeholder: %q", where, text)
	}
}

func TestTradingRedactsCredentialSpellings(t *testing.T) {
	spellings := tradingTestEchoedSpellings
	for _, spelling := range spellings {
		t.Run(spelling.form, func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			c := tradingTestConnection()
			c.Credentials = tradingTestEchoedCredentials
			n.connections = []domain.TradingConnection{c}
			cause := errors.New("venue echoed " + spelling.text)
			rt.checkErr, rt.verifyErr, rt.searchErr = cause, cause, cause
			echoed := func() []health.Diagnostic {
				return []health.Diagnostic{{
					Level: health.DiagError, Code: health.CodeConnectionError,
					Kind:        health.DiagKindEnvironment,
					Title:       "title " + spelling.text,
					Detail:      "detail " + spelling.text,
					Remediation: "remediation " + spelling.text,
					Instrument:  "instrument " + spelling.text,
					Actions: []health.DiagnosticAction{{
						Type: health.ActionOpenSymbols, Target: "target " + spelling.text,
					}},
				}}
			}
			rt.states = map[domain.ExternalID]trading.ConnectionStatus{c.ExternalID: {
				State: trading.ConnectionDegraded, Error: cause.Error(),
				Diagnostics: echoed(),
			}}
			status, err := s.GetTrading(systemCtx())
			if err != nil {
				t.Fatal(err)
			}
			got := status.Connections[0]
			tradingTestAssertRedacted(t, "connection error", got.Error, spellings)
			if len(got.Diagnostics) != 1 || len(got.Diagnostics[0].Actions) != 1 {
				t.Fatalf("diagnostics = %+v, want one with one action", got.Diagnostics)
			}
			diag := got.Diagnostics[0]
			for where, text := range map[string]string{
				"diagnostic title": diag.Title, "diagnostic detail": diag.Detail,
				"diagnostic remediation":   diag.Remediation,
				"diagnostic instrument":    diag.Instrument,
				"diagnostic action target": diag.Actions[0].Target,
			} {
				tradingTestAssertRedacted(t, where, text, spellings)
			}
			if runtime := rt.states[c.ExternalID].Diagnostics; !reflect.DeepEqual(runtime, echoed()) {
				t.Fatalf("runtime diagnostics were written: %+v", runtime)
			}
			_, checkErr := s.CheckTradingConnection(context.Background(), "connection-1")
			_, verifyErr := s.VerifyTradingSymbol(context.Background(), "connection-1", "AAPL")
			_, searchErr := s.SearchTradingSymbols(context.Background(), "connection-1", "AAPL")
			for operation, err := range map[string]error{"check": checkErr, "verify": verifyErr, "search": searchErr} {
				if !errors.Is(err, domain.ErrUpstream) {
					t.Fatalf("%s error = %v, want upstream", operation, err)
				}
				tradingTestAssertRedacted(t, operation+" error", err.Error(), spellings)
			}
		})
	}
}

func TestTradingRedactsTrimmedCredential(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	c := tradingTestConnection()
	c.Credentials = `{"apiKey":"test-key-value","apiSecret":" \techo \"q\" \\ <a&b> +c/d \n"}`
	n.connections = []domain.TradingConnection{c}
	// The provider trims the stored value before use, so the venue echoes the
	// trimmed one.
	rt.checkErr = errors.New("venue echoed " + url.QueryEscape(tradingTestEchoed))
	_, err := s.CheckTradingConnection(context.Background(), "connection-1")
	if !errors.Is(err, domain.ErrUpstream) {
		t.Fatalf("upstream error = %v", err)
	}
	tradingTestAssertRedacted(t, "upstream error", err.Error(), tradingTestEchoedSpellings)
}

func TestTradingCreateCredentialRefusalIsValidation(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	rt.checkErr = fmt.Errorf("trading: build connector: %w",
		domain.NewValidationError("/credentials/apiKey", "format", "key test-key-value is malformed"))
	_, err := s.CreateTradingConnection(systemCtx(), tradingTestConnection(), false)
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
	_, err := s.CreateTradingConnection(systemCtx(), c, false)
	if !errors.Is(err, domain.ErrInvalid) || err.Error() != "alpaca: apiKey is required" ||
		domain.ValidationPointer(err) != "/credentials/apiKey" ||
		domain.ValidationConstraint(err) != "required" {
		t.Fatalf("blank key refusal = %v, want the text intact", err)
	}
}

func TestTradingCreateProviderInvalidOutsideCredentialsIsUpstream(t *testing.T) {
	s, _, rt := newTradingTestService(t)
	rt.checkErr = fmt.Errorf("trading: provider %q returned no connector: %w", "venue", domain.ErrInvalid)
	_, err := s.CreateTradingConnection(systemCtx(), tradingTestConnection(), false)
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
					_, err = s.CreateTradingConnection(systemCtx(), tradingTestConnection(), false)
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
	rt.states = map[domain.ExternalID]trading.ConnectionStatus{c.ExternalID: {State: trading.ConnectionRunning}, "connection-2": {State: trading.ConnectionUnavailable, Error: "denied test-secret-value nested-secret"}}
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
	if got.Connections[0].State != "running" || got.Connections[1].State != "unavailable" || got.Connections[2].State != "stopped" {
		t.Fatal("runtime states were not mapped")
	}
	if got.Connections[1].Error != "denied [redacted] [redacted]" {
		t.Fatalf("unavailable reason = %q", got.Connections[1].Error)
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

func TestTradingConnectionSettings(t *testing.T) {
	t.Parallel()

	const credentials = `{"host":"127.0.0.1","port":7497,"clientId":12,"apiKey":"test-key","apiSecret":"test-secret","token":"test-token","marketDataType":"live","contracts":{"AAPL":{"conId":123}}}`
	tests := []struct {
		name        string
		provider    string
		credentials string
		want        map[string]any
	}{
		{
			name: "ib allowlist", provider: domain.TradingProviderIB,
			credentials: credentials,
			want: map[string]any{
				"host": "127.0.0.1", "port": float64(7497), "clientId": float64(12),
			},
		},
		{
			name: "ib optional client id", provider: domain.TradingProviderIB,
			credentials: `{"host":"gateway","port":4002}`,
			want:        map[string]any{"host": "gateway", "port": float64(4002)},
		},
		{name: "alpaca", provider: domain.TradingProviderAlpaca, credentials: credentials},
		{name: "other provider", provider: "other", credentials: credentials},
		{name: "empty", provider: domain.TradingProviderIB},
		{name: "null", provider: domain.TradingProviderIB, credentials: `null`},
		{name: "array", provider: domain.TradingProviderIB, credentials: `[{"host":"gateway"}]`},
		{name: "string", provider: domain.TradingProviderIB, credentials: `"gateway"`},
		{name: "number", provider: domain.TradingProviderIB, credentials: `7497`},
		{name: "boolean", provider: domain.TradingProviderIB, credentials: `true`},
		{name: "invalid json", provider: domain.TradingProviderIB, credentials: `{"host":`},
		{name: "empty object", provider: domain.TradingProviderIB, credentials: `{}`},
		{name: "secrets only", provider: domain.TradingProviderIB, credentials: `{"apiKey":"test-key","apiSecret":"test-secret","token":"test-token"}`},
		{name: "wrong types", provider: domain.TradingProviderIB, credentials: `{"host":1,"port":"7497","clientId":true}`},
		{name: "blank host", provider: domain.TradingProviderIB, credentials: `{"host":"  "}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TradingConnectionSettings(domain.TradingConnection{
				Provider: tt.provider, Credentials: tt.credentials,
			})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("settings = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestTradingStatusSettings(t *testing.T) {
	for _, provider := range []string{domain.TradingProviderIB, domain.TradingProviderAlpaca} {
		t.Run(provider, func(t *testing.T) {
			s, n, _ := newTradingTestService(t)
			c := tradingTestConnection()
			c.Provider = provider
			c.Credentials = `{"host":"gateway","port":4002,"clientId":7,"apiKey":"test-key","apiSecret":"test-secret"}`
			n.connections = []domain.TradingConnection{c}
			got, err := s.GetTrading(systemCtx())
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if provider == domain.TradingProviderIB {
				want = map[string]any{"host": "gateway", "port": float64(4002), "clientId": float64(7)}
			}
			if len(got.Connections) != 1 || !reflect.DeepEqual(got.Connections[0].Settings, want) {
				t.Fatalf("status = %#v, want settings %#v", got, want)
			}
		})
	}
}

func TestTradingStatusRuntimeHealth(t *testing.T) {
	first := health.Diagnostic{
		At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Level: health.DiagWarn,
		Code: health.CodeConnectionError, Kind: health.DiagKindEnvironment,
		Title: "Trade updates stream refused", Detail: "stream refused",
		Actions: []health.DiagnosticAction{{Type: health.ActionRestart}},
	}
	second := health.Diagnostic{
		At: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), Level: health.DiagError,
		Code: health.CodeInternalError, Kind: health.DiagKindProvider,
		Title: "Stream closed", Detail: "closed by venue",
	}
	for _, tc := range []struct {
		name   string
		status *trading.ConnectionStatus
		state  string
		err    string
		diags  []health.Diagnostic
	}{
		{"running", &trading.ConnectionStatus{State: trading.ConnectionRunning, Diagnostics: []health.Diagnostic{first}}, "running", "", []health.Diagnostic{first}},
		{"degraded", &trading.ConnectionStatus{State: trading.ConnectionDegraded, Error: "stream refused", Diagnostics: []health.Diagnostic{first, second}}, "degraded", "stream refused", []health.Diagnostic{first, second}},
		{"unavailable", &trading.ConnectionStatus{State: trading.ConnectionUnavailable, Error: "offline"}, "unavailable", "offline", []health.Diagnostic{}},
		{"stopped", nil, "stopped", "", []health.Diagnostic{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			c := tradingTestConnection()
			n.connections = []domain.TradingConnection{c}
			rt.states = map[domain.ExternalID]trading.ConnectionStatus{}
			if tc.status != nil {
				rt.states[c.ExternalID] = *tc.status
			}
			got, err := s.GetTrading(systemCtx())
			if err != nil {
				t.Fatal(err)
			}
			connection := got.Connections[0]
			if connection.State != tc.state || connection.Error != tc.err {
				t.Fatalf("state, error = %q, %q, want %q, %q", connection.State, connection.Error, tc.state, tc.err)
			}
			if !reflect.DeepEqual(connection.Diagnostics, tc.diags) {
				t.Fatalf("diagnostics = %+v, want %+v in order", connection.Diagnostics, tc.diags)
			}
		})
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

// TestTradingRemoveAccessFeePolicy hands the node the connection provider's fee
// policy with an agent's revoke, so the node can refuse an agent the revoke of
// the last account that can send through a connection without a fee account.
func TestTradingRemoveAccessFeePolicy(t *testing.T) {
	agent := auth.ContextWithCaller(context.Background(), tradingTestAgent)
	for _, unlinked := range []bool{true, false} {
		t.Run(fmt.Sprint(unlinked), func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			n.connections = []domain.TradingConnection{tradingTestConnection()}
			rt.providers[0].UnlinkedFees = unlinked
			access := domain.TradingAccess{Account: "acct-1", Connection: "connection-1"}
			n.access = []domain.TradingAccess{access}
			n.requireFees = !unlinked
			if err := s.RemoveTradingAccess(agent, access.Connection, access.Account, ""); err != nil {
				t.Fatal(err)
			}
			if n.requireFees != unlinked || len(n.access) != 0 {
				t.Fatalf("revoke reached the node with requireFeeAccount=%t leaving %+v, want %t and no grant", n.requireFees, n.access, unlinked)
			}
		})
	}
}

// TestTradingRemoveAccessUnregisteredProvider keeps an operator's revoke on a
// connection whose provider is no longer registered as it always was: the
// panel, the REST API and the system remove the grant without resolving the
// provider. An agent's revoke there is refused with the provider's ErrInvalid
// and changes nothing: a revoke whose fee rule cannot be decided is never
// passed.
func TestTradingRemoveAccessUnregisteredProvider(t *testing.T) {
	access := domain.TradingAccess{Account: "acct-1", Connection: "connection-1"}
	newService := func(t *testing.T) (*Service, *tradingTestNode, *tradingTestRuntime) {
		t.Helper()
		s, n, rt := newTradingTestService(t)
		c := tradingTestConnection()
		c.Provider = "retired"
		n.connections = []domain.TradingConnection{c}
		n.access = []domain.TradingAccess{access}
		return s, n, rt
	}
	t.Run("agent", func(t *testing.T) {
		s, n, rt := newService(t)
		ctx := auth.ContextWithCaller(context.Background(), tradingTestAgent)
		err := s.RemoveTradingAccess(ctx, access.Connection, access.Account, "")
		if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "unknown trading provider") {
			t.Fatalf("agent revoke = %v, want the provider's ErrInvalid", err)
		}
		if len(n.writes) != 0 || !reflect.DeepEqual(n.access, []domain.TradingAccess{access}) ||
			len(rt.checks) != 0 || rt.restarts != 0 {
			t.Fatalf(
				"refused revoke: writes %v, grants %+v, venue checks %d, restarts %d, want none and the grant kept",
				n.writes, n.access, len(rt.checks), rt.restarts,
			)
		}
	})
	for name, caller := range tradingTestOperators {
		t.Run(name, func(t *testing.T) {
			s, n, _ := newService(t)
			ctx := auth.ContextWithCaller(context.Background(), caller)
			if err := s.RemoveTradingAccess(ctx, access.Connection, access.Account, ""); err != nil {
				t.Fatalf("revoke: %v", err)
			}
			if len(n.access) != 0 || !reflect.DeepEqual(n.callers, []domain.Caller{caller}) {
				t.Fatalf("revoke left %+v by %v, want no grant, removed by the caller", n.access, n.callers)
			}
		})
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
	rt.states = map[domain.ExternalID]trading.ConnectionStatus{"connection-1": {State: trading.ConnectionDegraded, Error: "stream refused"}, "connection-2": {State: trading.ConnectionUnavailable, Error: "offline"}}
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
			if d.Connection == "connection-1" && (d.Error != "stream refused" || d.State != "degraded") {
				t.Fatalf("degraded destination = %+v, want its runtime error", d)
			}
			if d.Connection == "connection-2" && (d.Error != "offline" || d.State != "unavailable" || d.VenueAccount != "remote-2") {
				t.Fatal("destination lost binding or runtime status")
			}
			if d.Connection == "connection-3" && (d.Enabled || d.State != "stopped" || d.Error != "") {
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
	if err := s.SetTradingConnectionEnabled(ctx, " connection-1 ", false, false); err != nil || n.connections[0].Enabled || rt.restarts != 0 {
		t.Fatal("disable restarted or failed")
	}
	if err := s.SetTradingConnectionEnabled(ctx, "connection-1", true, false); err != nil || !n.connections[0].Enabled || rt.restarts != 1 {
		t.Fatal("enable did not restart")
	}
	if err := s.SetTradingConnectionEnabled(ctx, "missing", true, false); !errors.Is(err, domain.ErrNotFound) || rt.restarts != 1 {
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
		"create": func() error { _, err := s.CreateTradingConnection(ctx, tradingTestConnection(), false); return err },
		"update": func() error { return s.UpdateTradingConnectionSettings(ctx, "connection-1", "Other", "") },
		"enable": func() error { return s.SetTradingConnectionEnabled(ctx, "connection-1", true, false) },
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
	if _, err := s.CreateTradingConnection(systemCtx(), tradingTestConnection(), false); !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "configuration was saved") {
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

// tradingTestAgent is an agent over MCP: the caller that may only tighten.
var tradingTestAgent = domain.Caller{
	Source: domain.SourceMCP, Principal: domain.PrincipalOperator,
}

// tradingTestOperators are the callers a trading write serves as it always
// did: the panel, the REST API and the system.
var tradingTestOperators = map[string]domain.Caller{
	"panel":  {Source: domain.SourcePanel, Principal: domain.PrincipalOperator},
	"api":    {Source: domain.SourceAPI, Principal: domain.PrincipalOperator},
	"system": auth.SystemCaller(),
}

// TestTradingAgentCreateRefusedBeforeVenue refuses an agent's create with
// ErrLoosening before the venue is asked for the account: nothing is stored
// and the runtime is not restarted. Every operator caller creates the
// connection as before.
func TestTradingAgentCreateRefusedBeforeVenue(t *testing.T) {
	t.Run("agent", func(t *testing.T) {
		s, n, rt := newTradingTestService(t)
		ctx := auth.ContextWithCaller(context.Background(), tradingTestAgent)
		_, err := s.CreateTradingConnection(ctx, tradingTestConnection(), false)
		if !errors.Is(err, domain.ErrLoosening) {
			t.Fatalf("agent create = %v, want ErrLoosening", err)
		}
		if len(rt.checks) != 0 || len(n.writes) != 0 || rt.restarts != 0 {
			t.Fatalf(
				"refused create: venue checks %d, writes %v, restarts %d, want none",
				len(rt.checks), n.writes, rt.restarts,
			)
		}
	})
	for name, caller := range tradingTestOperators {
		t.Run(name, func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			ctx := auth.ContextWithCaller(context.Background(), caller)
			if _, err := s.CreateTradingConnection(ctx, tradingTestConnection(), false); err != nil {
				t.Fatalf("create: %v", err)
			}
			if len(rt.checks) != 1 || len(n.connections) != 1 || rt.restarts != 1 ||
				!reflect.DeepEqual(n.callers, []domain.Caller{caller}) {
				t.Fatalf(
					"create: venue checks %d, connections %d, restarts %d, callers %v",
					len(rt.checks), len(n.connections), rt.restarts, n.callers,
				)
			}
		})
	}
}

// TestTradingAgentCredentialChangeRefusedBeforeVenue refuses an agent's
// credential change with ErrLoosening before the venue is called: the stored
// connection is kept and the runtime is not restarted. An agent's update that
// changes no credential - a label alone, or credentials equal to the stored
// ones - passes as before, without a venue check or a restart. Every operator
// caller changes the credentials as before.
func TestTradingAgentCredentialChangeRefusedBeforeVenue(t *testing.T) {
	agent := auth.ContextWithCaller(context.Background(), tradingTestAgent)
	for _, tc := range []struct {
		name, credentials string
		refused           bool
	}{
		{name: "credential change", credentials: `{"apiKey":"new-key"}`, refused: true},
		{name: "label only"},
		{name: "stored credentials", credentials: `{"apiKey":"test-key-value"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			n.connections = []domain.TradingConnection{tradingTestConnection()}
			before := n.connections[0]
			err := s.UpdateTradingConnectionSettings(agent, "connection-1", "Renamed", tc.credentials)
			if len(rt.checks) != 0 || rt.restarts != 0 {
				t.Fatalf("agent update: venue checks %d, restarts %d, want none", len(rt.checks), rt.restarts)
			}
			if tc.refused {
				if !errors.Is(err, domain.ErrLoosening) {
					t.Fatalf("agent credential change = %v, want ErrLoosening", err)
				}
				if n.connections[0] != before || len(n.writes) != 0 {
					t.Fatalf("refused update changed the connection: %+v writes %v", n.connections[0], n.writes)
				}
				return
			}
			if err != nil {
				t.Fatalf("agent update: %v", err)
			}
			want := before
			want.Label = "Renamed"
			if n.connections[0] != want || !reflect.DeepEqual(n.callers, []domain.Caller{tradingTestAgent}) {
				t.Fatalf("agent update stored %+v by %v, want %+v by the agent", n.connections[0], n.callers, want)
			}
		})
	}
	for name, caller := range tradingTestOperators {
		t.Run(name, func(t *testing.T) {
			s, n, rt := newTradingTestService(t)
			n.connections = []domain.TradingConnection{tradingTestConnection()}
			ctx := auth.ContextWithCaller(context.Background(), caller)
			if err := s.UpdateTradingConnectionSettings(ctx, "connection-1", "", `{"apiKey":"new-key"}`); err != nil {
				t.Fatalf("credential change: %v", err)
			}
			const want = `{"apiKey":"new-key","apiSecret":"test-secret-value"}`
			if got := n.connections[0].Credentials; len(rt.checks) != 1 || rt.restarts != 1 || got != want {
				t.Fatalf("credential change: venue checks %d, restarts %d, credentials %s", len(rt.checks), rt.restarts, got)
			}
		})
	}
}

// TestTradingAgentCredentialIntegerChangeRefused refuses an agent's change of
// a stored integer member above 2^53 to its neighbour, which a float64 decode
// takes for the same value, with ErrLoosening: nothing is written, the venue
// is not called and the runtime is not restarted. Every operator caller
// stores the same update as before, without a venue check or a restart.
func TestTradingAgentCredentialIntegerChangeRefused(t *testing.T) {
	// 2^53+1 and 2^53 decode to the same float64.
	const (
		stored = `{"apiKey":"test-key-value","apiSecret":"test-secret-value","account":9007199254740993}`
		update = `{"account":9007199254740992}`
	)
	newService := func(t *testing.T) (*Service, *tradingTestNode, *tradingTestRuntime) {
		t.Helper()
		s, n, rt := newTradingTestService(t)
		n.connections = []domain.TradingConnection{tradingTestConnection()}
		n.connections[0].Credentials = stored
		return s, n, rt
	}
	t.Run("agent", func(t *testing.T) {
		s, n, rt := newService(t)
		before := n.connections[0]
		ctx := auth.ContextWithCaller(context.Background(), tradingTestAgent)
		err := s.UpdateTradingConnectionSettings(ctx, "connection-1", "", update)
		if !errors.Is(err, domain.ErrLoosening) {
			t.Fatalf("agent integer change = %v, want ErrLoosening", err)
		}
		if n.connections[0] != before || len(n.writes) != 0 || len(rt.checks) != 0 || rt.restarts != 0 {
			t.Fatalf(
				"refused update: connection %+v, writes %v, venue checks %d, restarts %d, want it unchanged with none",
				n.connections[0], n.writes, len(rt.checks), rt.restarts,
			)
		}
	})
	for name, caller := range tradingTestOperators {
		t.Run(name, func(t *testing.T) {
			s, n, rt := newService(t)
			ctx := auth.ContextWithCaller(context.Background(), caller)
			if err := s.UpdateTradingConnectionSettings(ctx, "connection-1", "", update); err != nil {
				t.Fatalf("integer change: %v", err)
			}
			const want = `{"account":9007199254740992,"apiKey":"test-key-value","apiSecret":"test-secret-value"}`
			if got := n.connections[0].Credentials; got != want || len(rt.checks) != 0 || rt.restarts != 0 ||
				!reflect.DeepEqual(n.callers, []domain.Caller{caller}) {
				t.Fatalf(
					"integer change: credentials %s by %v, venue checks %d, restarts %d",
					got, n.callers, len(rt.checks), rt.restarts,
				)
			}
		})
	}
}

// TestTradingScopeAccountGrant covers single-account mode's own grant: a
// create and an enable write the scope account's grant as the system when the
// account holds none to the connection, its own or its group's, passing the
// provider's fee policy; a provider binding venue accounts, a disable and
// multi-account mode write none, and a refused grant fails the call.
func TestTradingScopeAccountGrant(t *testing.T) {
	const account = domain.AccountID("acct-1")
	panel := auth.ContextWithCaller(context.Background(), tradingTestOperators["panel"])
	single := fixedAccountScope{account: account}
	newService := func(t *testing.T, n node.Node, scope AccountScope, venueAccounts bool) (*Service, *tradingTestRuntime) {
		t.Helper()
		rt := &tradingTestRuntime{identity: "venue-1", providers: []trading.Provider{{
			Type: "venue", Title: "Venue", VenueAccounts: venueAccounts, UnlinkedFees: true,
		}}}
		s, err := New(n, nil, nil, testLockSettlementPrice, WithTradingRuntime(rt), WithAccountScope(scope))
		if err != nil {
			t.Fatal(err)
		}
		return s, rt
	}
	scopeGrant := func(connection domain.ExternalID) domain.TradingAccess {
		return domain.TradingAccess{Account: account, Connection: connection}
	}

	t.Run("create", func(t *testing.T) {
		n := &tradingTestNode{}
		s, _ := newService(t, n, single, false)
		created, err := s.CreateTradingConnection(panel, tradingTestConnection(), false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(n.access, []domain.TradingAccess{scopeGrant(created.ExternalID)}) {
			t.Fatalf("access after create = %+v, want the scope account's grant", n.access)
		}
		if !reflect.DeepEqual(n.writes, []string{"create", "access"}) ||
			n.callers[1] != auth.SystemCaller() || !n.requireFees {
			t.Fatalf("writes %v, callers %v, fee policy %t, want a system grant under the fee policy",
				n.writes, n.callers, n.requireFees)
		}
	})

	t.Run("enable stored connection once", func(t *testing.T) {
		stored := tradingTestConnection()
		stored.Enabled = false
		n := &tradingTestNode{connections: []domain.TradingConnection{stored}}
		s, rt := newService(t, n, single, false)
		for range 2 {
			if err := s.SetTradingConnectionEnabled(panel, stored.ExternalID.String(), true, false); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(n.access, []domain.TradingAccess{scopeGrant(stored.ExternalID)}) || rt.restarts != 2 {
			t.Fatalf("access %+v after %d restarts, want one scope grant after two", n.access, rt.restarts)
		}
		if !reflect.DeepEqual(n.writes, []string{"enable", "access", "enable"}) || n.callers[1] != auth.SystemCaller() {
			t.Fatalf("writes %v, callers %v, want one system grant", n.writes, n.callers)
		}
	})

	t.Run("group grant suffices", func(t *testing.T) {
		stored := tradingTestConnection()
		n := &tradingTestNode{
			connections: []domain.TradingConnection{stored},
			groups:      map[domain.AccountID]string{account: "desk"},
			access:      []domain.TradingAccess{{AccountGroup: "desk", Connection: stored.ExternalID}},
		}
		s, _ := newService(t, n, single, false)
		if err := s.SetTradingConnectionEnabled(panel, stored.ExternalID.String(), true, false); err != nil {
			t.Fatal(err)
		}
		if len(n.access) != 1 || !reflect.DeepEqual(n.writes, []string{"enable"}) {
			t.Fatalf("access %+v, writes %v, want the group grant alone", n.access, n.writes)
		}
	})

	t.Run("refused grant fails the enable", func(t *testing.T) {
		stored := tradingTestConnection()
		n := &refusingGrantNode{
			tradingTestNode: &tradingTestNode{connections: []domain.TradingConnection{stored}},
			err:             domain.ErrFeeAccountRequired,
		}
		s, rt := newService(t, n, single, false)
		err := s.SetTradingConnectionEnabled(panel, stored.ExternalID.String(), true, false)
		if !errors.Is(err, domain.ErrFeeAccountRequired) || !strings.Contains(err.Error(), string(account)) {
			t.Fatalf("enable = %v, want the grant refusal naming %s", err, account)
		}
		if rt.restarts != 1 {
			t.Fatalf("restarts = %d, want the stored enable started", rt.restarts)
		}
	})

	t.Run("failed restart keeps the grant", func(t *testing.T) {
		restartErr := errors.New("restart failed")
		n := &tradingTestNode{}
		s, rt := newService(t, n, single, false)
		rt.restartErr = restartErr
		_, err := s.CreateTradingConnection(panel, tradingTestConnection(), false)
		if !errors.Is(err, restartErr) || len(n.connections) != 1 {
			t.Fatalf("create = %v, want the restart failure after the store", err)
		}
		if !reflect.DeepEqual(n.access, []domain.TradingAccess{scopeGrant(n.connections[0].ExternalID)}) {
			t.Fatalf("access after a failed restart of the create = %+v, want the scope grant", n.access)
		}

		stored := tradingTestConnection()
		stored.Enabled = false
		n = &tradingTestNode{connections: []domain.TradingConnection{stored}}
		s, rt = newService(t, n, single, false)
		rt.restartErr = restartErr
		if err := s.SetTradingConnectionEnabled(panel, stored.ExternalID.String(), true, false); !errors.Is(err, restartErr) {
			t.Fatalf("enable = %v, want the restart failure", err)
		}
		if !reflect.DeepEqual(n.access, []domain.TradingAccess{scopeGrant(stored.ExternalID)}) {
			t.Fatalf("access after a failed restart of the enable = %+v, want the scope grant", n.access)
		}
	})

	t.Run("tighten-only caller writes no grant", func(t *testing.T) {
		agent := auth.ContextWithCaller(context.Background(), tradingTestAgent)
		n := &tradingTestNode{}
		s, _ := newService(t, n, single, false)
		if _, err := s.CreateTradingConnection(agent, tradingTestConnection(), false); !errors.Is(err, domain.ErrLoosening) {
			t.Fatalf("agent create = %v, want ErrLoosening", err)
		}

		stored := tradingTestConnection()
		stored.Enabled = false
		refusing := &refusingTightenOnlyEnableNode{
			tradingTestNode: &tradingTestNode{connections: []domain.TradingConnection{stored}},
		}
		s, rt := newService(t, refusing, single, false)
		if err := s.SetTradingConnectionEnabled(agent, stored.ExternalID.String(), true, false); !errors.Is(err, domain.ErrLoosening) {
			t.Fatalf("agent enable = %v, want ErrLoosening", err)
		}
		if len(n.writes) != 0 || len(refusing.writes) != 0 || len(refusing.access) != 0 || rt.restarts != 0 {
			t.Fatalf("writes %v and %v, access %+v, restarts %d, want none",
				n.writes, refusing.writes, refusing.access, rt.restarts)
		}
	})

	for _, tc := range []struct {
		name          string
		scope         AccountScope
		venueAccounts bool
	}{
		{name: "multi-account mode", scope: EveryAccount{}},
		{name: "venue accounts", scope: single, venueAccounts: true},
	} {
		t.Run(tc.name+" writes no grant", func(t *testing.T) {
			n := &tradingTestNode{}
			s, _ := newService(t, n, tc.scope, tc.venueAccounts)
			created, err := s.CreateTradingConnection(panel, tradingTestConnection(), false)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetTradingConnectionEnabled(panel, created.ExternalID.String(), true, false); err != nil {
				t.Fatal(err)
			}
			if len(n.access) != 0 {
				t.Fatalf("access = %+v, want none", n.access)
			}
		})
	}

	t.Run("disable writes no grant", func(t *testing.T) {
		n := &tradingTestNode{connections: []domain.TradingConnection{tradingTestConnection()}}
		s, _ := newService(t, n, single, false)
		if err := s.SetTradingConnectionEnabled(panel, "connection-1", false, false); err != nil {
			t.Fatal(err)
		}
		if len(n.access) != 0 {
			t.Fatalf("access = %+v, want none", n.access)
		}
	})
}

// refusingGrantNode refuses every grant with err, as the node's fee policy
// refuses one.
type refusingGrantNode struct {
	*tradingTestNode
	err error
}

func (n *refusingGrantNode) PutTradingAccess(context.Context, domain.TradingAccess, bool, domain.Caller) error {
	return n.err
}

// refusingTightenOnlyEnableNode refuses a tighten-only enable before writing
// anything, as the local node does.
type refusingTightenOnlyEnableNode struct {
	*tradingTestNode
}

func (n *refusingTightenOnlyEnableNode) SetTradingConnectionEnabled(ctx context.Context, id domain.ExternalID, enabled bool, caller domain.Caller) error {
	if enabled && caller.TightenOnly() {
		return domain.ErrLoosening
	}
	return n.tradingTestNode.SetTradingConnectionEnabled(ctx, id, enabled, caller)
}
