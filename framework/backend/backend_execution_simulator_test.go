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
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"reflect"
	"strings"
	"testing"
)

type testPolicyNode struct {
	*tradingTestNode
	state                     domain.TestExecutionPolicyState
	policyReads, policyWrites int
	caller                    domain.Caller
}

func (n *testPolicyNode) GetTestExecutionPolicy(context.Context, domain.ExternalID) (domain.TestExecutionPolicyState, error) {
	n.policyReads++
	return n.state, nil
}
func (n *testPolicyNode) ReplaceTestExecutionPolicy(_ context.Context, _ domain.ExternalID, policy domain.TestExecutionPolicy, caller domain.Caller) (domain.TestExecutionPolicyState, error) {
	n.policyWrites++
	n.caller = caller
	n.state.Revision++
	n.state.Policy = policy
	return n.state, nil
}

func TestTestPolicyBackendOrder(t *testing.T) {
	for _, mutation := range []bool{false, true} {
		for _, kind := range []string{"missing", "venue", "unregistered", "classification-error", "test", "invalid"} {
			t.Run(kind+map[bool]string{false: " get", true: " put"}[mutation], func(t *testing.T) {
				s, base, rt := newTradingTestService(t)
				connection := tradingTestConnection()
				base.connections = []domain.TradingConnection{connection}
				n := &testPolicyNode{tradingTestNode: base, state: domain.TestExecutionPolicyState{Revision: 1, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}}}}
				s.node = n
				policy := domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}}
				id := connection.ExternalID.String()
				var want error
				switch kind {
				case "missing":
					id = "invisible"
					rt.providers = nil
					policy = domain.TestExecutionPolicy{}
					want = domain.ErrNotFound
				case "venue":
					policy = domain.TestExecutionPolicy{}
					want = domain.ErrExecutionPolicyUnsupported
				case "unregistered":
					rt.providers = nil
					policy = domain.TestExecutionPolicy{}
					want = domain.ErrTradingProviderUnavailable
				case "classification-error":
					want = fmt.Errorf("fixture classification: %w", domain.ErrTradingProviderUnavailable)
					rt.classifyErr = want
				default:
					rt.providers[0].ExecutionPolicy = true
					if kind == "invalid" && mutation {
						policy.Rules = []domain.TestExecutionRule{{Mode: "execute", PriceVariant: "normal"}}
						want = domain.ErrInvalid
					}
				}
				before := append([]domain.TradingConnection(nil), base.connections...)
				var state domain.TestExecutionPolicyState
				var err error
				if mutation {
					state, err = s.ReplaceTestExecutionPolicy(systemCtx(), id, policy)
				} else {
					state, err = s.GetTestExecutionPolicy(systemCtx(), id)
				}
				if !errors.Is(err, want) {
					t.Fatalf("error=%v want %v", err, want)
				}
				if kind == "classification-error" && err != want {
					t.Fatalf("classification error changed: got %v, want %v", err, want)
				}
				if kind == "unknown-class" && (!strings.Contains(err.Error(), string(connection.Provider)) || !strings.Contains(err.Error(), "unknown-class")) {
					t.Fatalf("classification error omits provider or class: %v", err)
				}
				if want != nil {
					if n.policyReads+n.policyWrites != 0 {
						t.Fatal("policy accessed before refusal")
					}
					if kind == "invalid" && domain.ValidationPointer(err) != "/rules/0/mask/baseAsset" {
						t.Fatalf("pointer=%q", domain.ValidationPointer(err))
					}
				} else if state.Revision != 1+uint64(n.policyWrites) {
					t.Fatalf("state=%+v", state)
				}
				if !reflect.DeepEqual(before, base.connections) || len(base.writes) != 0 || len(rt.checks) != 0 || rt.restarts != 0 {
					t.Fatal("connection settings or runtime changed")
				}
			})
		}
	}
}

func TestTestPolicyBackendTightenOnly(t *testing.T) {
	s, base, rt := newTradingTestService(t)
	base.connections = []domain.TradingConnection{tradingTestConnection()}
	rt.providers[0].ExecutionPolicy = true
	n := &testPolicyNode{tradingTestNode: base, state: domain.TestExecutionPolicyState{Revision: 1}}
	s.node = n
	ctx := auth.ContextWithCaller(context.Background(), tradingTestAgent)
	got, err := s.ReplaceTestExecutionPolicy(ctx, "connection-1", domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}})
	if err != nil || got.Revision != 2 || n.policyWrites != 1 || n.caller != tradingTestAgent {
		t.Fatalf("state=%+v caller=%+v error=%v", got, n.caller, err)
	}
}

func TestTestPolicyBackendRequiresCaller(t *testing.T) {
	s, _, _ := newTradingTestService(t)
	if _, err := s.GetTestExecutionPolicy(context.Background(), "missing"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("get accepted missing caller")
	}
	if _, err := s.ReplaceTestExecutionPolicy(context.Background(), "missing", domain.TestExecutionPolicy{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("put accepted missing caller")
	}
}
