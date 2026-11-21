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
	"slices"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

func samePositionLimits(a, b []domain.LimitPosition) bool {
	if len(a) != len(b) {
		return false
	}
	for _, limit := range a {
		if !slices.Contains(b, limit) {
			return false
		}
	}
	return true
}

func TestLocalNode_PositionLimitCRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}
	first := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc", Asset: "AAPL", MaxPosition: "100.00000001"}
	replacement := first
	replacement.MaxPosition = "90"
	review := first
	review.Mode = domain.RuleModeReviewWithHold
	other := first
	other.Account, other.Asset = "other", "USD"
	wantSets := [][]domain.LimitPosition{{first}, {replacement}, {replacement, review}, {replacement, review, other}}
	for i, limit := range []domain.LimitPosition{first, replacement, review, other} {
		if sink, err := n.PutPositionLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller); err != nil || sink != nil {
			t.Fatalf("PutPositionLimit = %T, %v, want online configure", sink, err)
		}
		if builds != 0 || n.currentEngine() != eng || len(eng.configureCalls) != i+1 {
			t.Fatalf("position put: builds=%d configure calls=%+v", builds, eng.configureCalls)
		}
		call := eng.configureCalls[i]
		listed, err := n.ListLimits(ctx, "")
		if err != nil || call.policy != domain.PolicyPositionLimit ||
			!samePositionLimits(call.limits.PositionLimits, wantSets[i]) ||
			!samePositionLimits(listed.PositionLimits, wantSets[i]) {
			t.Fatalf("position put configured %+v, listed %+v, %v; want %+v", call, listed, err, wantSets[i])
		}
		rows, err := st.ListAudit(ctx, 1)
		if err != nil || len(rows) != 1 || rows[0].Action != domain.AuditActionSetLimit ||
			rows[0].Account != limit.Account || rows[0].Detail.Code != domain.AuditDetailSetLimit ||
			rows[0].Detail.Params["policy"] != domain.PolicyPositionLimit ||
			!slices.Contains(rows[0].Detail.Fields, domain.AuditDetailField{Name: "max_position", Value: limit.MaxPosition}) {
			t.Fatalf("position set audit = %+v, %v", rows, err)
		}
	}
	listed, err := n.ListLimits(ctx, "acc")
	if err != nil || !samePositionLimits(listed.PositionLimits, []domain.LimitPosition{replacement, review}) {
		t.Fatalf("ListLimits = %+v, %v", listed, err)
	}
	deleteSets := [][]domain.LimitPosition{{review, other}, {other}, nil}
	for i, limit := range []domain.LimitPosition{replacement, review, other} {
		target := LimitTarget{Mode: limit.Mode, Policy: domain.PolicyPositionLimit, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset}
		if _, err := n.DeleteLimit(ctx, target, testCaller); err != nil {
			t.Fatal(err)
		}
		if builds != 0 || n.currentEngine() != eng || len(eng.configureCalls) != len(wantSets)+i+1 {
			t.Fatalf("position delete: builds=%d configure calls=%+v", builds, eng.configureCalls)
		}
		want := deleteSets[i]
		call := eng.configureCalls[len(eng.configureCalls)-1]
		stored, err := st.ListPositionLimits(ctx, "")
		if err != nil || call.policy != domain.PolicyPositionLimit ||
			!samePositionLimits(call.limits.PositionLimits, want) || !samePositionLimits(stored, want) {
			t.Fatalf("delete configured %+v, stored %+v, %v; want %+v", call, stored, err, want)
		}
		listed, err := n.ListLimits(ctx, "")
		if err != nil || !samePositionLimits(listed.PositionLimits, want) {
			t.Fatalf("delete listed %+v, %v; want %+v", listed, err, want)
		}
		rows, err := st.ListAudit(ctx, 1)
		key, keyErr := target.Key()
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		if err != nil || len(rows) != 1 || rows[0].Action != domain.AuditActionDeleteLimit ||
			!reflect.DeepEqual(rows[0].Detail, deleteLimitDetail(key)) {
			t.Fatalf("position delete audit = %+v, %v", rows, err)
		}
	}
	if builds != 0 || n.currentEngine() != eng {
		t.Fatalf("position CRUD rebuilt engine %d times", builds)
	}
}

func TestLocalNode_PositionLimitMissingAccount(t *testing.T) {
	t.Parallel()
	for _, missing := range []domain.MissingAccountPolicy{domain.MissingAccountCreate, domain.MissingAccountReject} {
		t.Run(string(missing), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			eng := newFakeEngine()
			eng.enforceResolver = true
			n, st := newTestNode(t, eng)
			limit := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "fresh", Asset: "AAPL", MaxPosition: "0"}
			_, err := n.PutPositionLimit(ctx, limit, domain.LimitWriteReplace, missing, testCaller)
			if missing == domain.MissingAccountReject {
				if !errors.Is(err, domain.ErrAccountMissing) {
					t.Fatalf("PutPositionLimit = %v, want ErrAccountMissing", err)
				}
				stored, err := st.ListPositionLimits(ctx, "")
				if err != nil || len(stored) != 0 || len(eng.configureCalls) != 0 {
					t.Fatalf("rejected put stored %+v, calls %+v, %v", stored, eng.configureCalls, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertAutoCreatedAccount(t, st, "fresh", "put position limit")
			if _, ok := eng.knownAccounts["fresh"]; !ok {
				t.Fatal("created account not registered in engine")
			}
			if len(eng.configureCalls) != 1 || !samePositionLimits(eng.configureCalls[0].limits.PositionLimits, []domain.LimitPosition{limit}) {
				t.Fatalf("configure = %+v", eng.configureCalls)
			}
		})
	}
}

func TestLocalNode_PositionLimitTightensOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	if _, err := n.CreateAccount(ctx, testAccount("acc"), testCaller); err != nil {
		t.Fatal(err)
	}
	createCurrencyAssets(t, n.realm, "AAPL")
	limit := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc", Asset: "AAPL", MaxPosition: "100"}
	for _, maximum := range []string{"100", "50", "0"} {
		limit.MaxPosition = maximum
		if _, err := n.PutPositionLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountReject, agentCaller); err != nil {
			t.Fatalf("agent put %s: %v", maximum, err)
		}
	}
	unchanged := refusalKeepsRiskState(t, n, probe)
	calls := len(eng.configureCalls)
	limit.MaxPosition = "0.00000001"
	_, err := n.PutPositionLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountReject, agentCaller)
	requireLoosening(t, "raise position limit", err)
	_, err = n.DeleteLimit(ctx, LimitTarget{Mode: domain.RuleModeReject, Policy: domain.PolicyPositionLimit, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset}, agentCaller)
	requireLoosening(t, "delete position limit", err)
	unchanged()
	stored, err := n.ListLimits(ctx, "acc")
	limit.MaxPosition = "0"
	if err != nil || !samePositionLimits(stored.PositionLimits, []domain.LimitPosition{limit}) || len(eng.configureCalls) != calls {
		t.Fatalf("refusal changed limits %+v or configure calls: %v", stored, err)
	}
}

func TestLocalNode_PositionLimitConfigureFailureRebuilds(t *testing.T) {
	t.Parallel()
	for _, failure := range []struct {
		name  string
		cause error
	}{
		{name: "partial", cause: errors.New("SDK configure failed")},
		{name: "not_implemented", cause: domain.ErrNotImplemented},
		{name: "validation", cause: domain.ErrInvalid},
	} {
		t.Run(failure.name, func(t *testing.T) {
			t.Parallel()
			for _, operation := range []string{"create", "replace", "delete"} {
				t.Run(operation, func(t *testing.T) {
					t.Parallel()
					ctx := context.Background()
					eng := newFakeEngine()
					n, probe := newRebuildProbeNode(t, eng)
					limit := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc", Asset: "AAPL", MaxPosition: "100"}
					var want []domain.LimitPosition
					if operation != "create" {
						if _, err := n.PutPositionLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller); err != nil {
							t.Fatal(err)
						}
						want = []domain.LimitPosition{limit}
					}
					configureErr := errors.New("partial position configure failed")
					eng.configureErr = fmt.Errorf("%w: %w", configureErr, failure.cause)
					before := probe.builds
					var err error
					if operation == "delete" {
						_, err = n.DeleteLimit(ctx, LimitTarget{Mode: domain.RuleModeReject, Policy: domain.PolicyPositionLimit, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset}, testCaller)
					} else {
						limit.MaxPosition = "200"
						_, err = n.PutPositionLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller)
					}
					if !errors.Is(err, failure.cause) || !errors.Is(err, configureErr) {
						t.Fatalf("configure error = %v, want wrapping %v and %v", err, configureErr, failure.cause)
					}
					if probe.builds != before+1 {
						t.Fatalf("rebuilds = %d, want exactly one from the reverted store", probe.builds-before)
					}
					stored, err := n.realm.ListPositionLimits(ctx, "")
					if err != nil || !samePositionLimits(stored, want) || !samePositionLimits(probe.last.PositionLimits, want) {
						t.Fatalf("failed configure stored %+v, rebuilt %+v, %v; want %+v", stored, probe.last.PositionLimits, err, want)
					}
				})
			}
		})
	}
}

func TestLocalNode_PositionLimitRebuildFailureFatals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	configureErr := errors.New("partial position configure failed")
	rebuildErr := errors.New("position reconciliation rebuild failed")
	eng.configureErr = configureErr
	n.build = func(engine.Snapshot) (engine.Engine, error) { return nil, rebuildErr }
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	_, err := n.PutPositionLimit(ctx, domain.LimitPosition{Mode: domain.RuleModeReject,
		Scope: domain.ScopeAccountAsset, Account: "acc", Asset: "AAPL", MaxPosition: "100",
	}, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, configureErr) || !errors.Is(err, rebuildErr) ||
		!errors.Is(fatalErr, configureErr) || !errors.Is(fatalErr, rebuildErr) {
		t.Fatalf("configure/rebuild error = %v, fatal = %v, want both failures and fail-stop", err, fatalErr)
	}
	stored, err := n.ListLimits(ctx, "")
	if err != nil || len(stored.PositionLimits) != 0 {
		t.Fatalf("failed reconciliation stored %+v, %v", stored.PositionLimits, err)
	}
}
