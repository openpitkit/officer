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

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/web/httpapi/dto"
	"go.openpit.dev/officer/internal/store/sqlite"
)

type testPolicyStore struct {
	store.Store
	realm *testPolicyRealm
}

func (s *testPolicyStore) ForRealm(ctx context.Context, realm domain.RealmID) (store.RealmStore, error) {
	if s.realm != nil {
		return s.realm, nil
	}
	r, err := s.Store.ForRealm(ctx, realm)
	if err != nil {
		return nil, err
	}
	s.realm = &testPolicyRealm{RealmStore: r}
	return s.realm, nil
}

type testPolicyRealm struct {
	store.RealmStore
	failWrite, failAudit error
	interleave           *domain.TestExecutionPolicy
}

func (r *testPolicyRealm) ReplaceTestExecutionPolicy(ctx context.Context, id domain.ExternalID, policy domain.TestExecutionPolicy) (domain.TestExecutionPolicyState, domain.TestExecutionPolicyState, error) {
	if r.failWrite != nil {
		return domain.TestExecutionPolicyState{}, domain.TestExecutionPolicyState{}, r.failWrite
	}
	if r.interleave != nil {
		pending := r.interleave
		r.interleave = nil
		if _, _, err := r.RealmStore.ReplaceTestExecutionPolicy(ctx, id, *pending); err != nil {
			return domain.TestExecutionPolicyState{}, domain.TestExecutionPolicyState{}, err
		}
	}
	return r.RealmStore.ReplaceTestExecutionPolicy(ctx, id, policy)
}
func (r *testPolicyRealm) AppendAudit(ctx context.Context, entry store.AuditEntry) error {
	if entry.Detail.Code == domain.AuditDetailSetTestExecutionPolicy && r.failAudit != nil {
		return r.failAudit
	}
	return r.RealmStore.AppendAudit(ctx, entry)
}
func newTestPolicyNode(t *testing.T) (node.Node, *testPolicyRealm, domain.ExternalID) {
	t.Helper()
	ctx := systemCtx()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "policy.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.BindExecutionPolicyProviders(func(provider string) (bool, error) {
		if provider == "policy-fixture" {
			return true, nil
		}
		return false, domain.ErrTradingProviderUnavailable
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	wrapped := &testPolicyStore{Store: st}
	n, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, wrapped, engine.NewOpenPitEngineBuildFunc(), func(err error) { t.Errorf("fatal: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	c, err := wrapped.realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: "policy-fixture", Label: "Policy test", VenueIdentity: "policy-test", Mode: domain.TradingModePaper, Credentials: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	return n, wrapped.realm, c.ExternalID
}
func testPolicy(t *testing.T, mode domain.TestExecutionMode) domain.TestExecutionPolicy {
	t.Helper()
	mask, err := domain.ParseInstrumentMask("A*", "USD")
	if err != nil {
		t.Fatal(err)
	}
	rule := domain.TestExecutionRule{Mask: mask, Mode: mode, PriceVariant: "best"}
	if mode == "split" {
		fills := 3
		rule.Fills = &fills
	}
	if mode == "rest" {
		seconds := int64(10)
		rule.Wait = &domain.TestExecutionWait{Kind: "finite", Seconds: &seconds}
	}
	return domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{rule}}
}
func testPolicyAudits(t *testing.T, r store.RealmStore) []domain.AuditRow {
	t.Helper()
	rows, err := r.ListAudit(systemCtx(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.AuditRow
	for _, row := range rows {
		if row.Detail.Code == domain.AuditDetailSetTestExecutionPolicy {
			out = append(out, row)
		}
	}
	return out
}
func assertTestPolicyAudit(t *testing.T, row domain.AuditRow, id domain.ExternalID, oldState, newState domain.TestExecutionPolicyState, caller domain.Caller) {
	t.Helper()
	if row.Action != domain.AuditActionSetTrading || row.Source != caller.Source || row.Actor != caller.Principal {
		t.Fatalf("audit identity: %+v", row)
	}
	if want := (domain.AuditDetailParams{"connection": id.String(), "oldRevision": strconv.FormatUint(oldState.Revision, 10), "newRevision": strconv.FormatUint(newState.Revision, 10)}); !reflect.DeepEqual(row.Detail.Params, want) {
		t.Fatalf("audit params=%v want %v", row.Detail.Params, want)
	}
	oldRules, err := json.Marshal(dto.ToTestExecutionPolicyStateDTO(id, oldState).Rules)
	if err != nil {
		t.Fatal(err)
	}
	newRules, err := json.Marshal(dto.ToTestExecutionPolicyStateDTO(id, newState).Rules)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.AuditDetailFields{{Name: "oldRules", Value: string(oldRules)}, {Name: "newRules", Value: string(newRules)}}
	if !reflect.DeepEqual(row.Detail.Fields, want) {
		t.Fatalf("audit fields=%v want %v", row.Detail.Fields, want)
	}
}
func TestNodeTestPolicyAuditUsesCommittedStates(t *testing.T) {
	n, r, id := newTestPolicyNode(t)
	intervening := testPolicy(t, "split")
	r.interleave = &intervening
	replacement := testPolicy(t, "rest")
	got, err := n.ReplaceTestExecutionPolicy(systemCtx(), id, replacement, auth.SystemCaller())
	if err != nil || got.Revision != 3 || !reflect.DeepEqual(got.Policy, replacement) {
		t.Fatalf("replace=%+v error=%v", got, err)
	}
	rows := testPolicyAudits(t, r)
	if len(rows) != 1 {
		t.Fatalf("audit entries=%d want 1", len(rows))
	}
	assertTestPolicyAudit(t, rows[0], id, domain.TestExecutionPolicyState{Revision: 2, Policy: intervening}, got, auth.SystemCaller())
}
func TestNodeTestPolicyConcurrentAuditChain(t *testing.T) {
	n, r, id := newTestPolicyNode(t)
	policies := []domain.TestExecutionPolicy{testPolicy(t, "split"), testPolicy(t, "rest")}
	states := make(chan domain.TestExecutionPolicyState, 2)
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, policy := range policies {
		wg.Add(1)
		go func(policy domain.TestExecutionPolicy) {
			defer wg.Done()
			<-start
			state, err := n.ReplaceTestExecutionPolicy(systemCtx(), id, policy, auth.SystemCaller())
			states <- state
			failures <- err
		}(policy)
	}
	close(start)
	wg.Wait()
	close(states)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	expected := map[uint64]domain.TestExecutionPolicyState{1: {Revision: 1, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}}}}
	for state := range states {
		expected[state.Revision] = state
	}
	if len(expected) != 3 {
		t.Fatalf("revisions=%v", expected)
	}
	rows := testPolicyAudits(t, r)
	if len(rows) != 2 {
		t.Fatalf("audit count=%d", len(rows))
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Detail.Params["newRevision"] < rows[j].Detail.Params["newRevision"]
	})
	for i, row := range rows {
		assertTestPolicyAudit(t, row, id, expected[uint64(i+1)], expected[uint64(i+2)], auth.SystemCaller())
	}
}
func TestNodeTestPolicyStoreFailureHasNoAudit(t *testing.T) {
	n, r, id := newTestPolicyNode(t)
	cause := errors.New("store replace refused")
	r.failWrite = cause
	if _, err := n.ReplaceTestExecutionPolicy(systemCtx(), id, testPolicy(t, "execute"), auth.SystemCaller()); !errors.Is(err, cause) {
		t.Fatalf("replace error=%v", err)
	}
	if rows := testPolicyAudits(t, r); len(rows) != 0 {
		t.Fatalf("audit before failed store: %v", rows)
	}
	state, err := n.GetTestExecutionPolicy(systemCtx(), id)
	if err != nil || state.Revision != 1 || len(state.Policy.Rules) != 0 {
		t.Fatalf("state after failed store=%+v error=%v", state, err)
	}
}
func TestNodeTestPolicyAuditFailureKeepsCommit(t *testing.T) {
	n, r, id := newTestPolicyNode(t)
	cause := errors.New("audit refused")
	r.failAudit = cause
	policy := testPolicy(t, "split")
	if _, err := n.ReplaceTestExecutionPolicy(systemCtx(), id, policy, auth.SystemCaller()); !errors.Is(err, cause) {
		t.Fatalf("audit error=%v want %v", err, cause)
	}
	state, err := n.GetTestExecutionPolicy(systemCtx(), id)
	if err != nil || state.Revision != 2 || !reflect.DeepEqual(state.Policy, policy) {
		t.Fatalf("saved state=%+v error=%v", state, err)
	}
	r.failAudit = nil
	got, err := n.ReplaceTestExecutionPolicy(systemCtx(), id, policy, auth.SystemCaller())
	if err != nil || got.Revision != 3 {
		t.Fatalf("retry state=%+v error=%v", got, err)
	}
	rows := testPolicyAudits(t, r)
	if len(rows) != 1 {
		t.Fatalf("audits=%v", rows)
	}
	assertTestPolicyAudit(t, rows[0], id, state, got, auth.SystemCaller())
}
