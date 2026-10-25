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

package accountmode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/internal/store/sqlite"
)

// clientCaller is a client of the control plane; auth.SystemCaller() is the
// venue processing.
var clientCaller = domain.Caller{
	Source: domain.SourceAPI, Principal: domain.PrincipalOperator,
}

// desk is the default account of the single-account scope under test; other
// is an account it does not act on.
const (
	desk  domain.AccountID = "desk"
	other domain.AccountID = "other"
)

func TestSingleAccountScopeForClient(t *testing.T) {
	scope := singleAccount{account: desk}
	if got := scope.Features(); got != (backend.AccountFeatures{Account: desk}) {
		t.Fatalf("Features() = %+v, want the default account fixed", got)
	}
	restriction := domain.AccountRestriction{Account: desk, IncludeUnowned: true}
	for _, requested := range []domain.AccountID{"", desk} {
		if got, err := scope.Account(clientCaller, requested); err != nil || got != desk {
			t.Errorf("Account(%q) = %q, %v; want %q", requested, got, err, desk)
		}
		if got, err := scope.List(clientCaller, requested); err != nil || got != restriction {
			t.Errorf("List(%q) = %+v, %v; want %+v", requested, got, err, restriction)
		}
		if err := scope.Visible(clientCaller, requested); err != nil {
			t.Errorf("Visible(%q) = %v, want nil", requested, err)
		}
	}

	_, err := scope.Account(clientCaller, other)
	assertSingleModeRefusal(t, "Account", err, "/account")
	if err != nil && !strings.Contains(err.Error(), `"desk"`) {
		t.Errorf("Account refusal %q does not name the default account", err)
	}
	_, err = scope.List(clientCaller, other)
	assertSingleModeRefusal(t, "List", err, "/account")
	if err := scope.Visible(clientCaller, other); !errors.Is(err, domain.ErrNotFound) ||
		strings.Contains(err.Error(), string(other)) {
		t.Errorf("Visible(%q) = %v, want not found naming no account", other, err)
	}

	for requested, want := range map[domain.MissingAccountPolicy]domain.MissingAccountPolicy{
		"":                          domain.MissingAccountReject,
		domain.MissingAccountCreate: domain.MissingAccountCreate,
		domain.MissingAccountReject: domain.MissingAccountReject,
		"maybe":                     "maybe",
	} {
		if got, err := scope.MissingAccount(clientCaller, requested); err != nil || got != want {
			t.Errorf("MissingAccount(%q) = %q, %v; want %q", requested, got, err, want)
		}
	}

	if err := scope.Group(clientCaller, ""); err != nil {
		t.Errorf("Group(\"\") = %v, want nil", err)
	}
	assertSingleModeRefusal(t, "Group", scope.Group(clientCaller, "desk-group"), "/accountGroup")
	if err := scope.ManageAccounts(clientCaller); !errors.Is(err, domain.ErrAccountsNotManaged) {
		t.Errorf("ManageAccounts = %v, want accounts not managed", err)
	}
}

func TestSingleAccountScopeForSystemCaller(t *testing.T) {
	scope := singleAccount{account: desk}
	every := backend.EveryAccount{}
	system := auth.SystemCaller()
	for _, account := range []domain.AccountID{"", desk, other} {
		got, gotErr := scope.Account(system, account)
		want, wantErr := every.Account(system, account)
		if got != want || !errors.Is(gotErr, wantErr) {
			t.Errorf("Account(%q) = %q, %v; want %q, %v", account, got, gotErr, want, wantErr)
		}
		gotList, gotErr := scope.List(system, account)
		wantList, wantErr := every.List(system, account)
		if gotList != wantList || !errors.Is(gotErr, wantErr) {
			t.Errorf("List(%q) = %+v, %v; want %+v, %v", account, gotList, gotErr, wantList, wantErr)
		}
		if got, want := scope.Visible(system, account), every.Visible(system, account); !errors.Is(got, want) {
			t.Errorf("Visible(%q) = %v, want %v", account, got, want)
		}
	}
	for _, policy := range []domain.MissingAccountPolicy{
		"", domain.MissingAccountCreate, domain.MissingAccountReject,
	} {
		got, gotErr := scope.MissingAccount(system, policy)
		want, wantErr := every.MissingAccount(system, policy)
		if got != want || !errors.Is(gotErr, wantErr) {
			t.Errorf("MissingAccount(%q) = %q, %v; want %q, %v", policy, got, gotErr, want, wantErr)
		}
	}
	for _, group := range []string{"", "desk-group"} {
		if got, want := scope.Group(system, group), every.Group(system, group); !errors.Is(got, want) {
			t.Errorf("Group(%q) = %v, want %v", group, got, want)
		}
	}
	if got, want := scope.ManageAccounts(system), every.ManageAccounts(system); !errors.Is(got, want) {
		t.Errorf("ManageAccounts = %v, want %v", got, want)
	}
}

// TestSingleAccountScopeReaskedWithItsAnswer re-asks the scope with its own
// answers, as SendPaperOrder does with the account and policy it resolved.
func TestSingleAccountScopeReaskedWithItsAnswer(t *testing.T) {
	scope := singleAccount{account: desk}
	account, err := scope.Account(clientCaller, "")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if again, err := scope.Account(clientCaller, account); err != nil || again != account {
		t.Errorf("Account(%q) = %q, %v; want the same answer", account, again, err)
	}
	policy, err := scope.MissingAccount(clientCaller, "")
	if err != nil {
		t.Fatalf("MissingAccount: %v", err)
	}
	if again, err := scope.MissingAccount(clientCaller, policy); err != nil || again != policy {
		t.Errorf("MissingAccount(%q) = %q, %v; want the same answer", policy, again, err)
	}
}

func TestLoadBuildsTheStoredScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings [][2]string
		want     backend.AccountScope
	}{
		{"no mode", nil, backend.EveryAccount{}},
		{"multi", [][2]string{{SettingKeyMode, ModeMulti}}, backend.EveryAccount{}},
		{
			"single",
			[][2]string{{SettingKeyMode, "single:desk"}},
			singleAccount{account: desk},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newTestNode(t)
			createAccounts(t, n, desk)
			storeSettings(t, n, tc.settings...)
			got, err := Load(context.Background(), n)
			if err != nil || got != tc.want {
				t.Fatalf("Load = %#v, %v; want %#v", got, err, tc.want)
			}
			if rows := auditRows(t, n, domain.AuditActionCreateAccount); len(rows) != 1 {
				t.Fatalf("create_account audit rows = %+v, want only the seeded one", rows)
			}
		})
	}
}

func TestLoadRecreatesMissingDefaultAccount(t *testing.T) {
	ctx := context.Background()
	n := newTestNode(t)
	storeSettings(t, n, [2]string{SettingKeyMode, "single:desk"})
	got, err := Load(ctx, n)
	if err != nil || got != (singleAccount{account: desk}) {
		t.Fatalf("Load = %#v, %v; want the single-account scope of desk", got, err)
	}
	if _, _, err := n.GetAccountState(ctx, desk); err != nil {
		t.Fatalf("default account after Load: %v", err)
	}
	rows := auditRows(t, n, domain.AuditActionCreateAccount)
	if len(rows) != 1 || rows[0].Account != desk ||
		rows[0].Source != domain.SourceSystem || rows[0].Actor != "" {
		t.Fatalf("create_account audit rows = %+v, want one for desk by the system", rows)
	}
}

func TestLoadRefusesCorruptSetting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings [][2]string
		want     []string
	}{
		{
			"single without an account",
			[][2]string{{SettingKeyMode, ModeSingle}},
			[]string{SettingKeyMode, `"single"`},
		},
		{
			"single with an empty account",
			[][2]string{{SettingKeyMode, "single:"}},
			[]string{SettingKeyMode, "default account"},
		},
		{
			"single with an invalid account",
			[][2]string{{SettingKeyMode, "single: desk"}},
			[]string{SettingKeyMode, "default account"},
		},
		{
			"unknown mode",
			[][2]string{{SettingKeyMode, "solo"}},
			[]string{SettingKeyMode, `"solo"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newTestNode(t)
			storeSettings(t, n, tc.settings...)
			scope, err := Load(context.Background(), n)
			if err == nil {
				t.Fatalf("Load = %#v, want an error", scope)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Load error %q does not name %s", err, want)
				}
			}
			if rows := auditRows(t, n, domain.AuditActionCreateAccount); len(rows) != 0 {
				t.Errorf("create_account audit rows = %+v, want none", rows)
			}
		})
	}
}

func TestChosenReadsWithoutCreatingAccounts(t *testing.T) {
	for _, tc := range []struct {
		name, value             string
		stored, chosen, invalid bool
	}{
		{name: "absent"},
		{name: "multi", value: ModeMulti, stored: true, chosen: true},
		{name: "single missing account", value: "single:desk", stored: true, chosen: true},
		{name: "empty", stored: true, invalid: true},
		{name: "unknown", value: "solo", stored: true, invalid: true},
		{name: "invalid account", value: "single: desk", stored: true, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newTestNode(t)
			if tc.stored {
				storeSettings(t, n, [2]string{SettingKeyMode, tc.value})
			}
			chosen, err := Chosen(context.Background(), n)
			if chosen != tc.chosen || (err != nil) != tc.invalid {
				t.Fatalf("Chosen = %t, %v; want %t, invalid=%t", chosen, err, tc.chosen, tc.invalid)
			}
			if rows := auditRows(t, n, domain.AuditActionCreateAccount); len(rows) != 0 {
				t.Fatalf("Chosen created accounts: %+v", rows)
			}
		})
	}
}

func TestReadReportsStoredChoice(t *testing.T) {
	ctx := clientContext()
	n := newTestNode(t)
	cp := newTestControlPlane(t, n, backend.EveryAccount{})
	state, err := Read(ctx, cp)
	if err != nil || state.Mode != nil || state.Account != "" ||
		!state.Features.Accounts() || state.RestartRequired {
		t.Fatalf("Read with no mode = %s, %v; want no mode and no restart", describe(state), err)
	}
	storeSettings(t, n, [2]string{SettingKeyMode, ModeMulti})
	state, err = Read(ctx, cp)
	if err != nil || state.Mode == nil || *state.Mode != ModeMulti || state.Account != "" ||
		state.RestartRequired {
		t.Fatalf("Read under multi = %s, %v; want multi, no account and no restart",
			describe(state), err)
	}
	storeSettings(t, n, [2]string{SettingKeyMode, "single:desk"})
	state, err = Read(ctx, cp)
	if err != nil || state.Mode == nil || *state.Mode != ModeSingle || state.Account != "desk" ||
		!state.RestartRequired {
		t.Fatalf("Read under single = %s, %v; want single on desk and a restart",
			describe(state), err)
	}
	storeSettings(t, n, [2]string{SettingKeyMode, "solo"})
	if state, err := Read(ctx, cp); err == nil || !strings.Contains(err.Error(), `"solo"`) {
		t.Fatalf("Read with an unknown mode = %s, %v; want an error naming it", describe(state), err)
	}
}

func TestApplyMulti(t *testing.T) {
	ctx := clientContext()
	t.Run("running multi", func(t *testing.T) {
		n := newTestNode(t)
		cp := newTestControlPlane(t, n, backend.EveryAccount{})
		state, err := Apply(ctx, cp, Request{Mode: ModeMulti})
		assertState(t, state, err, ModeMulti, "", "", false)
		assertStored(t, n, ModeMulti, "")
	})
	t.Run("running single", func(t *testing.T) {
		n := newTestNode(t)
		createAccounts(t, n, desk)
		storeSettings(t, n, [2]string{SettingKeyMode, "single:desk"})
		cp := newTestControlPlane(t, n, singleAccount{account: desk})
		state, err := Apply(ctx, cp, Request{Mode: ModeMulti})
		assertState(t, state, err, ModeMulti, "", desk, true)
		assertStored(t, n, ModeMulti, "")
		if features := cp.AccountFeatures(); features.Account != desk {
			t.Fatalf("running features after Apply = %+v, want desk kept", features)
		}
		// Choosing the running account again, by omission or by name,
		// cancels the pending switch.
		for _, account := range []string{"", "desk"} {
			state, err = Apply(ctx, cp, Request{Mode: ModeSingle, Account: account})
			assertState(t, state, err, ModeSingle, desk, desk, false)
			assertStored(t, n, ModeSingle, desk)
		}
	})
}

func TestRoutes(t *testing.T) {
	want := []struct{ id, method, pattern string }{
		{"account-mode.get", http.MethodGet, "/account-mode"},
		{"account-mode.put", http.MethodPut, "/account-mode"},
	}
	routes := Routes(nil)
	if len(routes) != len(want) {
		t.Fatalf("routes = %d, want %d", len(routes), len(want))
	}
	for i, route := range routes {
		if route.ID != want[i].id || route.Method != want[i].method ||
			route.Pattern != want[i].pattern || route.Handler == nil {
			t.Errorf("route %d = (%q, %q, %q), want (%q, %q, %q) with a handler",
				i, route.ID, route.Method, route.Pattern,
				want[i].id, want[i].method, want[i].pattern)
		}
	}
}

func TestApplySingleWhileRunningMulti(t *testing.T) {
	ctx := clientContext()
	t.Run("named account", func(t *testing.T) {
		n := newTestNode(t)
		createAccounts(t, n, desk, other)
		cp := newTestControlPlane(t, n, backend.EveryAccount{})
		state, err := Apply(ctx, cp, Request{Mode: ModeSingle, Account: "other"})
		assertState(t, state, err, ModeSingle, other, "", true)
		assertStored(t, n, ModeSingle, other)
	})
	t.Run("the one account", func(t *testing.T) {
		n := newTestNode(t)
		createAccounts(t, n, desk)
		cp := newTestControlPlane(t, n, backend.EveryAccount{})
		state, err := Apply(ctx, cp, Request{Mode: ModeSingle})
		assertState(t, state, err, ModeSingle, desk, "", true)
		assertStored(t, n, ModeSingle, desk)
	})
	t.Run("no account", func(t *testing.T) {
		n := newTestNode(t)
		cp := newTestControlPlane(t, n, backend.EveryAccount{})
		state, err := Apply(ctx, cp, Request{Mode: ModeSingle})
		assertState(t, state, err, ModeSingle, defaultAccount, "", true)
		assertStored(t, n, ModeSingle, defaultAccount)
		rows := auditRows(t, n, domain.AuditActionCreateAccount)
		if len(rows) != 1 || rows[0].Account != defaultAccount ||
			rows[0].Source != clientCaller.Source || rows[0].Actor != clientCaller.Principal {
			t.Fatalf("create_account audit rows = %+v, want one for default by the client", rows)
		}
	})
}

func TestApplyRefusals(t *testing.T) {
	ctx := clientContext()
	for _, tc := range []struct {
		name     string
		accounts []domain.AccountID
		// running is the default account of a process that runs
		// single-account mode; empty for multi-account mode.
		running domain.AccountID
		req     Request
		check   func(error) bool
	}{
		{
			"unknown mode", nil, "", Request{Mode: "solo"},
			validationAt("/mode", "enum"),
		},
		{
			"no mode", nil, "", Request{},
			validationAt("/mode", "required"),
		},
		{
			"multi with an account", []domain.AccountID{desk}, "",
			Request{Mode: ModeMulti, Account: "desk"},
			validationAt("/account", "unsupported_value"),
		},
		{
			"single with a missing account", []domain.AccountID{desk}, "",
			Request{Mode: ModeSingle, Account: "other"},
			func(err error) bool {
				var missing domain.AccountMissingError
				return errors.As(err, &missing) && missing.Account == other
			},
		},
		{
			"single among several accounts", []domain.AccountID{desk, other}, "",
			Request{Mode: ModeSingle},
			validationAt("/account", "required"),
		},
		{
			"single with another account while running single",
			[]domain.AccountID{desk, other}, desk,
			Request{Mode: ModeSingle, Account: "other"},
			func(err error) bool {
				return errors.Is(err, domain.ErrAccountsNotManaged) &&
					strings.Contains(err.Error(), "switch to multi-account mode and restart")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newTestNode(t)
			createAccounts(t, n, tc.accounts...)
			var scope backend.AccountScope = backend.EveryAccount{}
			if tc.running != "" {
				storeSettings(t, n, [2]string{SettingKeyMode, "single:" + string(tc.running)})
				scope = singleAccount{account: tc.running}
			}
			before := snapshot(t, n)
			state, err := Apply(ctx, newTestControlPlane(t, n, scope), tc.req)
			if !tc.check(err) {
				t.Fatalf("Apply = %s, %v; want the refusal", describe(state), err)
			}
			if after := snapshot(t, n); after != before {
				t.Fatalf("refused Apply changed the store: %s, was %s", after, before)
			}
		})
	}
}

// TestApplyConcurrentOppositeModes applies opposite modes at once, round after
// round: whichever request lands last, the stored choice must be one the next
// start accepts.
func TestApplyConcurrentOppositeModes(t *testing.T) {
	// perMode requests of each mode race in every round.
	const rounds, perMode = 50, 4
	ctx := clientContext()
	n := newTestNode(t)
	createAccounts(t, n, desk)
	cp := newTestControlPlane(t, n, backend.EveryAccount{})
	for round := range rounds {
		start := make(chan struct{})
		errs := make(chan error, 2*perMode)
		var wg sync.WaitGroup
		for i := range 2 * perMode {
			mode := ModeSingle
			if i%2 == 1 {
				mode = ModeMulti
			}
			wg.Go(func() {
				<-start
				_, err := Apply(ctx, cp, Request{Mode: mode})
				errs <- err
			})
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: Apply: %v", round, err)
			}
		}
		if _, err := Load(context.Background(), n); err != nil {
			t.Fatalf("round %d: stored %s does not load: %v", round, storedChoice(t, n), err)
		}
	}
}

// TestSingleAccountExportRefusalsPointAtFilters exports every entity with a
// filter single-account mode refuses: the refusal names the filter in the
// export request body, not the list input it reached.
func TestSingleAccountExportRefusalsPointAtFilters(t *testing.T) {
	ctx := clientContext()
	n := newTestNode(t)
	createAccounts(t, n, desk, other)
	cp := newTestControlPlane(t, n, singleAccount{account: desk})
	otherAccount := businesscsv.ExportFilter{Account: other}
	for _, tc := range []struct {
		name    string
		entity  businesscsv.Entity
		filter  businesscsv.ExportFilter
		pointer string
	}{
		{
			"group of accounts", businesscsv.EntityAccounts,
			businesscsv.ExportFilter{GroupCode: "desks", GroupCodeSet: true},
			"/filters/groupCode",
		},
		{"account of accounts", businesscsv.EntityAccounts, otherAccount, "/filters/account"},
		{"account of positions", businesscsv.EntityPositions, otherAccount, "/filters/account"},
		{"account of orders", businesscsv.EntityOrders, otherAccount, "/filters/account"},
		{"account of trades", businesscsv.EntityTrades, otherAccount, "/filters/account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cp.ExportBusinessCSV(ctx, backend.BusinessCSVExportRequest{
				Entity: tc.entity, Delimiter: businesscsv.DelimiterComma, Filter: tc.filter,
			})
			assertSingleModeRefusal(t, "ExportBusinessCSV", err, tc.pointer)
		})
	}
}

// assertSingleModeRefusal checks a single-account-mode refusal: a validation
// error at pointer with the single_account_mode constraint.
func assertSingleModeRefusal(t *testing.T, method string, err error, pointer string) {
	t.Helper()
	if !validationAt(pointer, "single_account_mode")(err) {
		t.Errorf("%s refusal = %v, want single_account_mode at %s", method, err, pointer)
	}
}

// validationAt matches a validation error at pointer with constraint.
func validationAt(pointer, constraint string) func(error) bool {
	return func(err error) bool {
		return errors.Is(err, domain.ErrInvalid) &&
			domain.ValidationPointer(err) == pointer &&
			domain.ValidationConstraint(err) == constraint
	}
}

// assertState checks the State Apply answered: the stored mode and account,
// the running process's default account (empty for every account), and
// whether a restart is required.
func assertState(
	t *testing.T, state State, err error,
	mode string, account, running domain.AccountID, restart bool,
) {
	t.Helper()
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if state.Mode == nil || *state.Mode != mode || state.Account != string(account) ||
		state.Features != (backend.AccountFeatures{Account: running}) ||
		state.RestartRequired != restart {
		t.Fatalf("Apply = %s, want mode %q, account %q, running %q, restart %v",
			describe(state), mode, account, running, restart)
	}
}

// assertStored checks the stored mode setting: the mode, followed by ":" and
// the account when one is given.
func assertStored(t *testing.T, n node.Node, mode string, account domain.AccountID) {
	t.Helper()
	want := mode
	if account != "" {
		want += ":" + string(account)
	}
	if got := storedChoice(t, n); got != fmt.Sprintf("%q", want) {
		t.Fatalf("stored %s, want %q", got, want)
	}
}

// snapshot describes the stored choice and the count of setting writes.
func snapshot(t *testing.T, n node.Node) string {
	t.Helper()
	return fmt.Sprintf("%s, %d setting writes",
		storedChoice(t, n), len(auditRows(t, n, domain.AuditActionSetSetting)))
}

// storedChoice describes the stored mode setting.
func storedChoice(t *testing.T, n node.Node) string {
	t.Helper()
	value, ok, err := n.Setting(context.Background(), SettingKeyMode)
	if err != nil {
		t.Fatalf("Setting(%s): %v", SettingKeyMode, err)
	}
	if !ok {
		return "<absent>"
	}
	return fmt.Sprintf("%q", value)
}

func describe(state State) string {
	mode := "<nil>"
	if state.Mode != nil {
		mode = *state.Mode
	}
	return fmt.Sprintf("{mode %q, account %q, features %+v, restart %v}",
		mode, state.Account, state.Features, state.RestartRequired)
}

func clientContext() context.Context {
	return auth.ContextWithCaller(context.Background(), clientCaller)
}

// newTestNode opens a local node, over the native engine, on a new SQLite
// database.
func newTestNode(t *testing.T) node.Node {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "officer.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		t.Fatalf("Migrate: %v", err)
	}
	n, _, err := node.NewLocalNode(
		ctx, domain.DefaultRealm, st, engine.NewOpenPitEngineBuildFunc(),
		func(err error) { t.Errorf("unexpected fatal shutdown: %v", err) },
	)
	if err != nil {
		_ = st.Close()
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// newTestControlPlane returns the Officer control plane over n running scope.
func newTestControlPlane(
	t *testing.T, n node.Node, scope backend.AccountScope,
) backend.ControlPlane {
	t.Helper()
	cp, err := backend.New(
		n, nil, nil, engine.LockSettlementPrice, backend.WithAccountScope(scope),
	)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	return cp
}

// createAccounts creates the accounts through the node, as a client.
func createAccounts(t *testing.T, n node.Node, accounts ...domain.AccountID) {
	t.Helper()
	for _, account := range accounts {
		if _, err := n.CreateAccount(
			context.Background(), domain.Account{Code: account}, clientCaller,
		); err != nil {
			t.Fatalf("CreateAccount(%s): %v", account, err)
		}
	}
}

// storeSettings writes the key-value settings in order through the node, as
// the system.
func storeSettings(t *testing.T, n node.Node, settings ...[2]string) {
	t.Helper()
	for _, setting := range settings {
		if err := n.SetSetting(
			context.Background(), setting[0], setting[1], auth.SystemCaller(),
		); err != nil {
			t.Fatalf("SetSetting(%s): %v", setting[0], err)
		}
	}
}

// auditRows returns the node's audit rows of action.
func auditRows(t *testing.T, n node.Node, action domain.AuditAction) []domain.AuditRow {
	t.Helper()
	rows, err := n.ListAudit(context.Background(), 1000)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var matched []domain.AuditRow
	for _, row := range rows {
		if row.Action == action {
			matched = append(matched, row)
		}
	}
	return matched
}
