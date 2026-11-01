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
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// newSpotFundsLimitModeStore returns a migrated memory store whose default
// realm holds the Spot Funds limit mode stored (when stored is true) as
// value.
func newSpotFundsLimitModeStore(
	t *testing.T, value string, stored bool,
) (store.Store, store.RealmStore) {
	t.Helper()
	st := newMemoryStore("node.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if stored {
		if err := realm.SetSetting(ctx, domain.SettingKeySpotFundsLimitMode, value); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
	}
	return st, realm
}

// TestNewLocalNode_SpotFundsLimitModeReachesBuild proves the build snapshot
// carries the stored Spot Funds limit mode, and that an absent key is Enforce,
// the documented default.
func TestNewLocalNode_SpotFundsLimitModeReachesBuild(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		value         string
		stored        bool
		wantTrackOnly bool
	}{
		{"absent", "", false, false},
		{"enforce", "enforce", true, false},
		{"track_only", "track_only", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, _ := newSpotFundsLimitModeStore(t, tc.value, tc.stored)
			ctx := context.Background()

			// Start from the opposite value, so a build that never sets the
			// field cannot pass.
			captured := engine.Snapshot{SpotFundsTrackOnly: !tc.wantTrackOnly}
			n, _, err := NewLocalNode(
				ctx, domain.DefaultRealm, st,
				fakeBuild(newFakeEngine(), &captured), failOnFatal(t),
			)
			if err != nil {
				t.Fatalf("NewLocalNode: %v", err)
			}
			t.Cleanup(func() { _ = n.Close() })
			if captured.SpotFundsTrackOnly != tc.wantTrackOnly {
				t.Fatalf("build SpotFundsTrackOnly = %v, want %v",
					captured.SpotFundsTrackOnly, tc.wantTrackOnly)
			}
		})
	}
}

// TestNewLocalNode_UnknownSpotFundsLimitModeFailsBuild proves an unknown stored
// value is an error naming the key and the value, never a default, and no
// engine is built. The error is store corruption, not a caller's invalid
// input, so it carries no validation chain a surface would turn into a 422.
func TestNewLocalNode_UnknownSpotFundsLimitModeFailsBuild(t *testing.T) {
	t.Parallel()
	st, _ := newSpotFundsLimitModeStore(t, "relaxed", true)

	built := false
	build := func(engine.Snapshot) (engine.Engine, error) {
		built = true
		return newFakeEngine(), nil
	}
	n, _, err := NewLocalNode(
		context.Background(), domain.DefaultRealm, st, build, failOnFatal(t),
	)
	if err == nil {
		_ = n.Close()
		t.Fatal("NewLocalNode with an unknown stored mode succeeded, want error")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("NewLocalNode error = %v, want no ErrInvalid for a corrupt stored value", err)
	}
	if pointer, constraint := domain.ValidationPointer(err), domain.ValidationConstraint(err); pointer != "" || constraint != "" {
		t.Fatalf("NewLocalNode error validation pointer, constraint = %q, %q; want none", pointer, constraint)
	}
	for _, want := range []string{
		"load spot funds limit mode for build",
		domain.SettingKeySpotFundsLimitMode,
		`"relaxed"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("NewLocalNode error %q does not contain %q", err, want)
		}
	}
	if built {
		t.Fatal("engine built from a snapshot with an unknown mode")
	}
}

// TestLocalNode_RebuildReadsStoredSpotFundsLimitMode proves a rebuild from the
// store reads the mode again rather than keeping the one the node started
// with.
func TestLocalNode_RebuildReadsStoredSpotFundsLimitMode(t *testing.T) {
	t.Parallel()
	st, realm := newSpotFundsLimitModeStore(t, "", false)
	ctx := context.Background()

	var captured engine.Snapshot
	n, _, err := NewLocalNode(
		ctx, domain.DefaultRealm, st,
		fakeBuild(newFakeEngine(), &captured), failOnFatal(t),
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if captured.SpotFundsTrackOnly {
		t.Fatal("initial build SpotFundsTrackOnly = true, want false with no key")
	}

	if err := realm.SetSetting(
		ctx, domain.SettingKeySpotFundsLimitMode,
		string(domain.SpotFundsLimitModeTrackOnly),
	); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := n.(*localNode).rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	if !captured.SpotFundsTrackOnly {
		t.Fatal("rebuild SpotFundsTrackOnly = false, want true from the stored track_only")
	}
}

func TestLocalNode_SpotFundsLimitModeWriteAppliesAuditsAndRebuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	var captured engine.Snapshot
	builds := 0
	n.build = func(snap engine.Snapshot) (engine.Engine, error) {
		builds++
		captured = snap
		return eng, nil
	}
	before := auditRowCount(t, ctx, realm)
	state, err := n.SpotFundsLimitMode(ctx)
	if err != nil || state.Mode != domain.SpotFundsLimitModeEnforce || state.Stored ||
		state.AccountsWithoutCurrency != nil {
		t.Fatalf("initial state = %+v, %v; want enforce, no stored row and nil accounts", state, err)
	}
	if len(eng.spotFundsTrackOnlyCalls) != 0 || auditRowCount(t, ctx, realm) != before {
		t.Fatal("reading the mode changed the engine or audit")
	}

	for i, mode := range []domain.SpotFundsLimitMode{
		domain.SpotFundsLimitModeTrackOnly,
		domain.SpotFundsLimitModeTrackOnly,
		domain.SpotFundsLimitModeEnforce,
	} {
		if err := n.SetSpotFundsLimitMode(ctx, mode, testCaller); err != nil {
			t.Fatalf("SetSpotFundsLimitMode(%s): %v", mode, err)
		}
		requireStoredSpotFundsLimitMode(t, realm, string(mode), true)
		wantCalls := []bool{true, true, false}[:i+1]
		if !reflect.DeepEqual(eng.spotFundsTrackOnlyCalls, wantCalls) {
			t.Fatalf("engine mode calls = %v, want %v", eng.spotFundsTrackOnlyCalls, wantCalls)
		}
		state, err := n.SpotFundsLimitMode(ctx)
		if err != nil || state.Mode != mode || !state.Stored || state.AccountsWithoutCurrency != nil {
			t.Fatalf("read state = %+v, %v; want stored mode %s and nil accounts", state, err, mode)
		}
		// Every write is exactly one set_limit row, never a set_setting.
		if total := auditRowCount(t, ctx, realm); total != before+i+1 {
			t.Fatalf("audit rows = %d, want %d", total, before+i+1)
		}
		rows := modeAuditRows(t, ctx, realm, domain.AuditActionSetLimit)
		if len(rows) != i+1 {
			t.Fatalf("set_limit audits = %d, want %d", len(rows), i+1)
		}
		for _, row := range rows {
			if row.Detail.Code != domain.AuditDetailSetLimit ||
				row.Detail.Params["scope"] != string(domain.ScopeGlobal) ||
				len(row.Detail.Fields) != 1 || row.Detail.Fields[0].Name != "mode" ||
				row.Actor != testCaller.Principal || row.Source != testCaller.Source {
				t.Fatalf("set_limit audit = %+v, want global mode field and operator attribution", row)
			}
		}
		values := make(map[string]int)
		for _, row := range rows {
			values[row.Detail.Fields[0].Value]++
		}
		if values["track_only"] != min(i+1, 2) || values["enforce"] != max(i-1, 0) {
			t.Fatalf("audit mode values = %v, want %d track_only and %d enforce", values, min(i+1, 2), max(i-1, 0))
		}
		if i == 0 {
			if builds != 0 {
				t.Fatalf("mode write rebuilt the engine %d times, want none", builds)
			}
			if err := n.rebuildEngineFromStore(ctx); err != nil {
				t.Fatalf("rebuildEngineFromStore: %v", err)
			}
			if !captured.SpotFundsTrackOnly || builds != 1 {
				t.Fatalf("rebuild mode = %v, builds = %d; want true, 1", captured.SpotFundsTrackOnly, builds)
			}
		}
	}
}

func TestLocalNode_SpotFundsLimitModeEngineErrorReverts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		stored bool
		prev   domain.SpotFundsLimitMode
		next   domain.SpotFundsLimitMode
	}{
		{"absent", false, domain.SpotFundsLimitModeEnforce, domain.SpotFundsLimitModeTrackOnly},
		{"track_only", true, domain.SpotFundsLimitModeTrackOnly, domain.SpotFundsLimitModeEnforce},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			ctx := context.Background()
			if tc.stored {
				if err := n.SetSpotFundsLimitMode(ctx, tc.prev, testCaller); err != nil {
					t.Fatalf("set previous mode: %v", err)
				}
			}
			before := auditRowCount(t, ctx, realm)
			engineErr := errors.New("mode engine failure")
			eng.spotFundsTrackOnlyErr = engineErr
			n.build = func(engine.Snapshot) (engine.Engine, error) {
				t.Fatal("failed mode write rebuilt the engine")
				return nil, nil
			}
			err := n.SetSpotFundsLimitMode(ctx, tc.next, testCaller)
			if !errors.Is(err, engineErr) {
				t.Fatalf("SetSpotFundsLimitMode error = %v, want engine failure", err)
			}
			// The revert restores the stored state it found: an absent key
			// stays absent, so a failed write leaves no policy row behind.
			if tc.stored {
				requireStoredSpotFundsLimitMode(t, realm, string(tc.prev), true)
			} else {
				requireStoredSpotFundsLimitMode(t, realm, "", false)
			}
			if after := auditRowCount(t, ctx, realm); after != before {
				t.Fatalf("audit rows = %d, want unchanged %d after engine failure", after, before)
			}
			state, err := n.SpotFundsLimitMode(ctx)
			if err != nil || state.Mode != tc.prev {
				t.Fatalf("state after engine failure = %+v, %v; want %s", state, err, tc.prev)
			}
		})
	}
}

func TestLocalNode_SpotFundsLimitModeTightenOnly(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	ctx := context.Background()
	before := auditRowCount(t, ctx, realm)
	err := n.SetSpotFundsLimitMode(ctx, domain.SpotFundsLimitModeTrackOnly, agentCaller)
	if !errors.Is(err, domain.ErrLoosening) {
		t.Fatalf("agent enforce -> track_only error = %v, want ErrLoosening", err)
	}
	requireStoredSpotFundsLimitMode(t, realm, "", false)
	if len(eng.spotFundsTrackOnlyCalls) != 0 || auditRowCount(t, ctx, realm) != before {
		t.Fatal("refused loosening changed the engine or audit")
	}
	if err := n.SetSpotFundsLimitMode(ctx, domain.SpotFundsLimitModeTrackOnly, testCaller); err != nil {
		t.Fatalf("operator set track_only: %v", err)
	}
	if err := n.SetSpotFundsLimitMode(ctx, domain.SpotFundsLimitModeEnforce, agentCaller); err != nil {
		t.Fatalf("agent track_only -> enforce: %v", err)
	}
	requireStoredSpotFundsLimitMode(t, realm, "enforce", true)
	if !reflect.DeepEqual(eng.spotFundsTrackOnlyCalls, []bool{true, false}) {
		t.Fatalf("engine mode calls = %v, want [true false]", eng.spotFundsTrackOnlyCalls)
	}
	if rows := modeAuditRows(t, ctx, realm, domain.AuditActionSetLimit); len(rows) != 2 {
		t.Fatalf("set_limit audits = %d, want 2", len(rows))
	}
}

func TestLocalNode_SpotFundsLimitModeInvalid(t *testing.T) {
	t.Parallel()
	for _, mode := range []domain.SpotFundsLimitMode{"", "relaxed"} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			// Validation must precede even the live-configuration gate.
			n.restarting.Store(true)
			err := n.SetSpotFundsLimitMode(context.Background(), mode, testCaller)
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != "/mode" {
				t.Fatalf("invalid mode error = %v, pointer = %q; want ErrInvalid at /mode", err, domain.ValidationPointer(err))
			}
			requireStoredSpotFundsLimitMode(t, realm, "", false)
			if len(eng.spotFundsTrackOnlyCalls) != 0 ||
				len(modeAuditRows(t, context.Background(), realm, domain.AuditActionSetLimit)) != 0 {
				t.Fatal("invalid mode changed the engine or audit")
			}
		})
	}
}

// TestLocalNode_SpotFundsLimitModeListsStoredRow proves the barrier list
// carries the stored meta-policy row only while one is stored, and never in a
// list narrowed to an account, since the row is global.
func TestLocalNode_SpotFundsLimitModeListsStoredRow(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	ctx := context.Background()
	limits, err := n.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("list without a stored mode: %v", err)
	}
	if len(limits.SpotFundsLimitModes) != 0 {
		t.Fatalf("modes without a stored mode = %+v, want none", limits.SpotFundsLimitModes)
	}
	if err := realm.SetSetting(ctx, domain.SettingKeySpotFundsLimitMode, "track_only"); err != nil {
		t.Fatalf("seed mode: %v", err)
	}
	limits, err = n.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("list with a stored mode: %v", err)
	}
	want := []domain.LimitSpotFundsLimitMode{{Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeTrackOnly}}
	if !reflect.DeepEqual(limits.SpotFundsLimitModes, want) {
		t.Fatalf("modes = %+v, want %+v", limits.SpotFundsLimitModes, want)
	}
	limits, err = n.ListLimits(ctx, "acc-1")
	if err != nil {
		t.Fatalf("list narrowed to an account: %v", err)
	}
	if len(limits.SpotFundsLimitModes) != 0 {
		t.Fatalf("account list modes = %+v, want none", limits.SpotFundsLimitModes)
	}
}

func TestLocalNode_SpotFundsLimitModeCorruptStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	ctx := context.Background()
	if err := realm.SetSetting(ctx, domain.SettingKeySpotFundsLimitMode, "relaxed"); err != nil {
		t.Fatalf("seed corrupt mode: %v", err)
	}
	_, readErr := n.SpotFundsLimitMode(ctx)
	writeErr := n.SetSpotFundsLimitMode(ctx, domain.SpotFundsLimitModeEnforce, testCaller)
	_, listErr := n.ListLimits(ctx, "")
	for name, err := range map[string]error{"read": readErr, "write": writeErr, "list": listErr} {
		if err == nil || errors.Is(err, domain.ErrInvalid) ||
			domain.ValidationPointer(err) != "" || domain.ValidationConstraint(err) != "" {
			t.Fatalf("%s corrupt mode error = %v, want corruption without caller validation", name, err)
		}
		if !strings.Contains(err.Error(), domain.SettingKeySpotFundsLimitMode) ||
			!strings.Contains(err.Error(), `"relaxed"`) {
			t.Fatalf("%s corrupt mode error = %v, want key and stored value", name, err)
		}
	}
	requireStoredSpotFundsLimitMode(t, realm, "relaxed", true)
	if len(eng.spotFundsTrackOnlyCalls) != 0 || len(modeAuditRows(t, ctx, realm, domain.AuditActionSetLimit)) != 0 {
		t.Fatal("corrupt mode write changed the engine or audit")
	}
}

func TestLocalNode_SpotFundsLimitModeAccountsWithoutCurrency(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	ctx := context.Background()
	for _, group := range []domain.AccountGroup{
		{Code: "desk"}, {Code: "dollars", Currency: "USD"},
	} {
		if _, err := realm.CreateGroup(ctx, group); err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
	}
	for _, account := range []domain.Account{
		{Code: "unbound"},
		{Code: "own"},
		{Code: "group", GroupCode: "desk"},
		{Code: "currency", Currency: "USD", GroupCode: "desk"},
		{Code: "inherited", GroupCode: "dollars"},
	} {
		if _, err := realm.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
	}
	for _, bound := range []domain.LimitSpotFundsPnlBounds{
		// One bound per axis: the warning counts every Spot Funds P&L axis.
		{Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccountGroup, AccountGroup: "desk", Currency: "USD", LowerBound: "-100"},
		{Axis: domain.SpotFundsPnlAxisUnrealized, Scope: domain.ScopeAccountGroup, AccountGroup: "dollars", Currency: "USD", LowerBound: "-100"},
		{Axis: domain.SpotFundsPnlAxisTotal, Scope: domain.ScopeAccount, Account: "own", Currency: "EUR", LowerBound: "-100"},
	} {
		if err := realm.PutSpotFundsPnlBoundsLimit(ctx, bound); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
		}
	}
	if err := realm.UpsertBalance(ctx, domain.Balance{
		Account: "unbound", Asset: "USD", Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	blockers, err := realm.ListAccountsBlockingCurrencyChange(ctx, []domain.AccountID{"unbound"})
	if err != nil {
		t.Fatalf("ListAccountsBlockingCurrencyChange: %v", err)
	}
	if len(blockers) != 1 || blockers[0].Account != "unbound" || !blockers[0].Other || blockers[0].CurrencyValuedLimit {
		t.Fatalf("blockers = %+v, want unbound with only Other", blockers)
	}
	accounts, err := realm.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	before := auditRowCount(t, ctx, realm)
	for _, global := range []bool{false, true} {
		if global {
			if err := realm.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
				Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1000",
			}); err != nil {
				t.Fatalf("put global bound: %v", err)
			}
		}
		var want []domain.AccountID
		for _, account := range accounts {
			if account.Code == "own" || account.Code == "group" || (global && account.Code == "unbound") {
				want = append(want, account.Code)
			}
		}
		state, err := n.SpotFundsLimitMode(ctx)
		if err != nil || state.Mode != domain.SpotFundsLimitModeEnforce || !reflect.DeepEqual(state.AccountsWithoutCurrency, want) {
			t.Fatalf("global=%v: state = %+v, %v; want enforce, accounts %v in ListAccounts order", global, state, err, want)
		}
	}
	if len(eng.spotFundsTrackOnlyCalls) != 0 || auditRowCount(t, ctx, realm) != before {
		t.Fatal("reading accounts changed the engine or audit")
	}
}

func TestLocalNode_SpotFundsLimitModeStoreFailure(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	n.realm = &failSetSettingRealm{RealmStore: realm}
	err := n.SetSpotFundsLimitMode(context.Background(), domain.SpotFundsLimitModeTrackOnly, testCaller)
	if !errors.Is(err, errSetSettingStore) {
		t.Fatalf("SetSpotFundsLimitMode error = %v, want store failure", err)
	}
	requireStoredSpotFundsLimitMode(t, realm, "", false)
	if len(eng.spotFundsTrackOnlyCalls) != 0 ||
		len(modeAuditRows(t, context.Background(), realm, domain.AuditActionSetLimit)) != 0 {
		t.Fatal("failed store write changed the engine or audit")
	}
}

func TestLocalNode_SpotFundsLimitModeAuditFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	auditErr := errors.New("mode audit failure")
	n.realm = &failActionAuditRealm{RealmStore: realm, action: domain.AuditActionSetLimit, err: auditErr}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	err := n.SetSpotFundsLimitMode(context.Background(), domain.SpotFundsLimitModeTrackOnly, testCaller)
	if !errors.Is(err, auditErr) || !errors.Is(fatalErr, auditErr) {
		t.Fatalf("write error = %v, fatal error = %v; want audit failure in both", err, fatalErr)
	}
	if !strings.Contains(fatalErr.Error(), `operation="audit spot funds limit mode"`) ||
		!strings.Contains(fatalErr.Error(), "policy="+domain.PolicySpotFundsLimitMode) {
		t.Fatalf("fatal error = %v, want audit operation and policy", fatalErr)
	}
	requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
	if !reflect.DeepEqual(eng.spotFundsTrackOnlyCalls, []bool{true}) {
		t.Fatalf("engine mode calls = %v, want [true] before fatal audit", eng.spotFundsTrackOnlyCalls)
	}
	if rows := modeAuditRows(t, context.Background(), realm, domain.AuditActionSetLimit); len(rows) != 0 {
		t.Fatalf("failed set_limit audits = %+v, want none", rows)
	}
}

type failSpotFundsLimitModeRevertRealm struct {
	store.RealmStore
	calls int
	err   error
}

// SetSetting fails the second write, the revert of a stored previous value.
func (r *failSpotFundsLimitModeRevertRealm) SetSetting(ctx context.Context, key, value string) error {
	r.calls++
	if r.calls == 2 {
		return r.err
	}
	return r.RealmStore.SetSetting(ctx, key, value)
}

// DeleteSetting fails every removal: on the set path the only removal is the
// revert of a key that was absent.
func (r *failSpotFundsLimitModeRevertRealm) DeleteSetting(context.Context, string) error {
	return r.err
}

func TestLocalNode_SpotFundsLimitModeRevertFailureFatals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		stored bool
	}{
		{"absent", false},
		{"stored", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			eng.spotFundsTrackOnlyErr = errors.New("mode engine failure")
			n, realm := newTestNode(t, eng)
			if tc.stored {
				if err := realm.SetSetting(context.Background(), domain.SettingKeySpotFundsLimitMode, "enforce"); err != nil {
					t.Fatalf("seed mode: %v", err)
				}
			}
			revertErr := errors.New("mode revert failure")
			n.realm = &failSpotFundsLimitModeRevertRealm{RealmStore: realm, err: revertErr}
			var fatalErr error
			n.fatal = func(err error) { fatalErr = err }
			err := n.SetSpotFundsLimitMode(context.Background(), domain.SpotFundsLimitModeTrackOnly, testCaller)
			if !errors.Is(err, revertErr) || !errors.Is(fatalErr, revertErr) {
				t.Fatalf("write error = %v, fatal error = %v; want revert failure in both", err, fatalErr)
			}
			if !strings.Contains(fatalErr.Error(), `operation="revert spot funds limit mode"`) ||
				!strings.Contains(fatalErr.Error(), "policy="+domain.PolicySpotFundsLimitMode) {
				t.Fatalf("fatal error = %v, want revert operation and policy", fatalErr)
			}
			if rows := modeAuditRows(t, context.Background(), realm, domain.AuditActionSetLimit); len(rows) != 0 {
				t.Fatalf("set_limit audits after failed revert = %+v, want none", rows)
			}
		})
	}
}

type cancelSpotFundsLimitModeEngine struct {
	*fakeEngine
	cancel context.CancelFunc
}

func (e *cancelSpotFundsLimitModeEngine) SetSpotFundsTrackOnly(ctx context.Context, trackOnly bool) error {
	e.cancel()
	return e.fakeEngine.SetSpotFundsTrackOnly(ctx, trackOnly)
}

type spotFundsLimitModeCancellationProbeRealm struct {
	store.RealmStore
}

func (r *spotFundsLimitModeCancellationProbeRealm) SetSetting(
	ctx context.Context, key, value string,
) error {
	if key == domain.SettingKeySpotFundsLimitMode {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return r.RealmStore.SetSetting(ctx, key, value)
}

func (r *spotFundsLimitModeCancellationProbeRealm) DeleteSetting(
	ctx context.Context, key string,
) error {
	if key == domain.SettingKeySpotFundsLimitMode {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return r.RealmStore.DeleteSetting(ctx, key)
}

func (r *spotFundsLimitModeCancellationProbeRealm) AppendAudit(
	ctx context.Context, entry store.AuditEntry,
) error {
	if entry.Action == domain.AuditActionSetLimit || entry.Action == domain.AuditActionDeleteLimit {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return r.RealmStore.AppendAudit(ctx, entry)
}

func TestLocalNode_SpotFundsLimitModeFinishesAfterCancellation(t *testing.T) {
	t.Parallel()
	for _, failEngine := range []bool{false, true} {
		name := "audit"
		if failEngine {
			name = "revert"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			n.realm = &spotFundsLimitModeCancellationProbeRealm{RealmStore: realm}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n.engine = &cancelSpotFundsLimitModeEngine{fakeEngine: eng, cancel: cancel}
			engineErr := errors.New("mode engine failure")
			if failEngine {
				eng.spotFundsTrackOnlyErr = engineErr
			}
			err := n.SetSpotFundsLimitMode(ctx, domain.SpotFundsLimitModeTrackOnly, testCaller)
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("engine did not cancel the caller context")
			}
			if failEngine {
				if !errors.Is(err, engineErr) {
					t.Fatalf("write error = %v, want engine failure after durable revert", err)
				}
				// The key was absent, so the durable revert removes it.
				requireStoredSpotFundsLimitMode(t, realm, "", false)
				if rows := modeAuditRows(t, context.Background(), realm, domain.AuditActionSetLimit); len(rows) != 0 {
					t.Fatalf("set_limit audits = %+v, want none after revert", rows)
				}
				return
			}
			if err != nil {
				t.Fatalf("write after engine canceled caller: %v", err)
			}
			requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
			if rows := modeAuditRows(t, context.Background(), realm, domain.AuditActionSetLimit); len(rows) != 1 {
				t.Fatalf("set_limit audits = %d, want 1 after caller cancellation", len(rows))
			}
		})
	}
}

// spotFundsLimitModeTarget addresses the one global row of the Spot Funds
// limit-mode meta-policy.
var spotFundsLimitModeTarget = LimitTarget{
	Policy: domain.PolicySpotFundsLimitMode,
	Scope:  domain.ScopeGlobal,
}

// TestLocalNode_SpotFundsLimitModeDelete proves removing the meta-policy row
// deletes the stored mode, applies Enforce to the live engine without a
// rebuild, audits one delete_limit, and that a tighten-only caller may make
// it, since Enforce only tightens.
func TestLocalNode_SpotFundsLimitModeDelete(t *testing.T) {
	t.Parallel()
	for name, caller := range map[string]domain.Caller{
		"operator": testCaller, "tighten_only": agentCaller,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			ctx := context.Background()
			if err := n.SetSpotFundsLimitMode(ctx, domain.SpotFundsLimitModeTrackOnly, testCaller); err != nil {
				t.Fatalf("set track_only: %v", err)
			}
			n.build = func(engine.Snapshot) (engine.Engine, error) {
				t.Fatal("mode delete rebuilt the engine")
				return nil, nil
			}
			before := auditRowCount(t, ctx, realm)
			sink, err := n.DeleteLimit(ctx, spotFundsLimitModeTarget, caller)
			if err != nil || sink != nil {
				t.Fatalf("DeleteLimit = %v, %v; want nil sink and no error", sink, err)
			}
			requireStoredSpotFundsLimitMode(t, realm, "", false)
			if !reflect.DeepEqual(eng.spotFundsTrackOnlyCalls, []bool{true, false}) {
				t.Fatalf("engine mode calls = %v, want [true false]", eng.spotFundsTrackOnlyCalls)
			}
			state, err := n.SpotFundsLimitMode(ctx)
			if err != nil || state.Mode != domain.SpotFundsLimitModeEnforce || state.Stored {
				t.Fatalf("state after delete = %+v, %v; want enforce with no stored row", state, err)
			}
			if after := auditRowCount(t, ctx, realm); after != before+1 {
				t.Fatalf("audit rows = %d, want %d", after, before+1)
			}
			rows := modeAuditRows(t, ctx, realm, domain.AuditActionDeleteLimit)
			if len(rows) != 1 || rows[0].Detail.Code != domain.AuditDetailDeleteLimit ||
				rows[0].Detail.Params["scope"] != string(domain.ScopeGlobal) ||
				len(rows[0].Detail.Fields) != 0 ||
				rows[0].Actor != caller.Principal || rows[0].Source != caller.Source {
				t.Fatalf("delete_limit audits = %+v, want one global row attributed to the caller", rows)
			}

			// The row is gone: a second delete answers like an absent barrier.
			_, err = n.DeleteLimit(ctx, spotFundsLimitModeTarget, caller)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("second delete error = %v, want ErrNotFound", err)
			}
			if len(eng.spotFundsTrackOnlyCalls) != 2 || auditRowCount(t, ctx, realm) != before+1 {
				t.Fatal("deleting an absent mode changed the engine or audit")
			}
		})
	}
}

// TestLocalNode_SpotFundsLimitModeDeleteRefusals proves an absent row and a
// non-global address are refused before the engine or the audit is touched,
// and that the address is validated even before the live-configuration gate.
func TestLocalNode_SpotFundsLimitModeDeleteRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		target  LimitTarget
		stored  bool
		want    error
		pointer string
	}{
		{"absent", spotFundsLimitModeTarget, false, domain.ErrNotFound, ""},
		{"account_scope", LimitTarget{
			Policy: domain.PolicySpotFundsLimitMode, Scope: domain.ScopeAccount, Account: "acc",
		}, true, domain.ErrInvalid, "/scope"},
		{"global_with_account", LimitTarget{
			Policy: domain.PolicySpotFundsLimitMode, Scope: domain.ScopeGlobal, Account: "acc",
		}, true, domain.ErrInvalid, "/account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			ctx := context.Background()
			if tc.stored {
				if err := realm.SetSetting(ctx, domain.SettingKeySpotFundsLimitMode, "track_only"); err != nil {
					t.Fatalf("seed mode: %v", err)
				}
			}
			if tc.want == domain.ErrInvalid {
				// The address check must precede even the live-configuration
				// gate; an absent row is only found under it.
				n.restarting.Store(true)
			}
			before := auditRowCount(t, ctx, realm)
			_, err := n.DeleteLimit(ctx, tc.target, agentCaller)
			if !errors.Is(err, tc.want) {
				t.Fatalf("DeleteLimit error = %v, want %v", err, tc.want)
			}
			if tc.pointer != "" && domain.ValidationPointer(err) != tc.pointer {
				t.Fatalf("DeleteLimit pointer = %q, want %q", domain.ValidationPointer(err), tc.pointer)
			}
			if tc.stored {
				requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
			}
			if len(eng.spotFundsTrackOnlyCalls) != 0 || auditRowCount(t, ctx, realm) != before {
				t.Fatal("refused mode delete changed the engine or audit")
			}
		})
	}
}

// TestLocalNode_SpotFundsLimitModeDeleteEngineErrorRestores proves a refused
// engine apply puts the removed value back, so the stored mode and the engine
// agree, and nothing is audited.
func TestLocalNode_SpotFundsLimitModeDeleteEngineErrorRestores(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	ctx := context.Background()
	if err := realm.SetSetting(ctx, domain.SettingKeySpotFundsLimitMode, "track_only"); err != nil {
		t.Fatalf("seed mode: %v", err)
	}
	engineErr := errors.New("mode engine failure")
	eng.spotFundsTrackOnlyErr = engineErr
	before := auditRowCount(t, ctx, realm)
	_, err := n.DeleteLimit(ctx, spotFundsLimitModeTarget, testCaller)
	if !errors.Is(err, engineErr) {
		t.Fatalf("DeleteLimit error = %v, want engine failure", err)
	}
	requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
	if auditRowCount(t, ctx, realm) != before {
		t.Fatal("failed mode delete was audited")
	}
}

// TestLocalNode_SpotFundsLimitModeDeleteFatals proves a failed restore after a
// refused engine apply, and a failed audit after a successful one, both route
// to the fatal hook naming the meta-policy.
func TestLocalNode_SpotFundsLimitModeDeleteFatals(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("mode delete audit failure")
	for _, tc := range []struct {
		name      string
		operation string
		failure   error
		wrap      func(store.RealmStore, *fakeEngine) store.RealmStore
	}{
		// On the delete path the restore is the only realm-setting write.
		{"restore", "revert spot funds limit mode", errSetSettingStore, func(
			realm store.RealmStore, eng *fakeEngine,
		) store.RealmStore {
			eng.spotFundsTrackOnlyErr = errors.New("mode engine failure")
			return &failSetSettingRealm{RealmStore: realm}
		}},
		{"audit", "audit spot funds limit mode", auditErr, func(
			realm store.RealmStore, _ *fakeEngine,
		) store.RealmStore {
			return &failActionAuditRealm{RealmStore: realm, action: domain.AuditActionDeleteLimit, err: auditErr}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			ctx := context.Background()
			if err := realm.SetSetting(ctx, domain.SettingKeySpotFundsLimitMode, "track_only"); err != nil {
				t.Fatalf("seed mode: %v", err)
			}
			failure := tc.failure
			n.realm = tc.wrap(realm, eng)
			var fatalErr error
			n.fatal = func(err error) { fatalErr = err }
			_, err := n.DeleteLimit(ctx, spotFundsLimitModeTarget, testCaller)
			if !errors.Is(err, failure) || !errors.Is(fatalErr, failure) {
				t.Fatalf("delete error = %v, fatal error = %v; want the failure in both", err, fatalErr)
			}
			if !strings.Contains(fatalErr.Error(), `operation="`+tc.operation+`"`) ||
				!strings.Contains(fatalErr.Error(), "policy="+domain.PolicySpotFundsLimitMode) {
				t.Fatalf("fatal error = %v, want operation %q and the policy", fatalErr, tc.operation)
			}
			if rows := modeAuditRows(t, ctx, realm, domain.AuditActionDeleteLimit); len(rows) != 0 {
				t.Fatalf("delete_limit audits = %+v, want none", rows)
			}
		})
	}
}

// TestLocalNode_SpotFundsLimitModeDeleteFinishesAfterCancellation proves a
// caller cancellation during the engine apply neither skips the audit of a
// done delete nor the restore of a refused one.
func TestLocalNode_SpotFundsLimitModeDeleteFinishesAfterCancellation(t *testing.T) {
	t.Parallel()
	for _, failEngine := range []bool{false, true} {
		name := "audit"
		if failEngine {
			name = "restore"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := realm.SetSetting(ctx, domain.SettingKeySpotFundsLimitMode, "track_only"); err != nil {
				t.Fatalf("seed mode: %v", err)
			}
			n.realm = &spotFundsLimitModeCancellationProbeRealm{RealmStore: realm}
			n.engine = &cancelSpotFundsLimitModeEngine{fakeEngine: eng, cancel: cancel}
			engineErr := errors.New("mode engine failure")
			if failEngine {
				eng.spotFundsTrackOnlyErr = engineErr
			}
			_, err := n.DeleteLimit(ctx, spotFundsLimitModeTarget, testCaller)
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("engine did not cancel the caller context")
			}
			rows := modeAuditRows(t, context.Background(), realm, domain.AuditActionDeleteLimit)
			if failEngine {
				if !errors.Is(err, engineErr) {
					t.Fatalf("delete error = %v, want engine failure after durable restore", err)
				}
				requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
				if len(rows) != 0 {
					t.Fatalf("delete_limit audits = %+v, want none after restore", rows)
				}
				return
			}
			if err != nil {
				t.Fatalf("delete after engine canceled caller: %v", err)
			}
			requireStoredSpotFundsLimitMode(t, realm, "", false)
			if len(rows) != 1 {
				t.Fatalf("delete_limit audits = %d, want 1 after caller cancellation", len(rows))
			}
		})
	}
}

// modeAuditRows returns the audit rows of action that address the Spot Funds
// limit-mode meta-policy.
func modeAuditRows(
	t *testing.T, ctx context.Context, realm store.RealmStore, action domain.AuditAction,
) []domain.AuditRow {
	t.Helper()
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{action},
	}, 100)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	var out []domain.AuditRow
	for _, row := range rows {
		if row.Detail.Params["policy"] == domain.PolicySpotFundsLimitMode {
			out = append(out, row)
		}
	}
	return out
}

func requireStoredSpotFundsLimitMode(t *testing.T, realm store.RealmStore, want string, wantStored bool) {
	t.Helper()
	value, stored, err := realm.GetSetting(context.Background(), domain.SettingKeySpotFundsLimitMode)
	if err != nil || stored != wantStored || value != want {
		t.Fatalf("stored mode = %q, %v, %v; want %q, %v, nil", value, stored, err, want, wantStored)
	}
}
