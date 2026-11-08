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
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

type forceAuthorizeFunc func(context.Context, domain.Caller, string) error

func (f forceAuthorizeFunc) Authorize(ctx context.Context, caller domain.Caller, identifier string) error {
	return f(ctx, caller, identifier)
}

type forceNodeCall struct {
	action domain.AuditAction
	target string
	force  bool
	caller domain.Caller
}

type forceAuditCall struct {
	entry  store.AuditEntry
	caller domain.Caller
	ctx    context.Context
}

type forceTestNode struct {
	node.Node
	order    domain.OrderDetail
	calls    []forceNodeCall
	audits   []forceAuditCall
	auditErr error
	onAudit  func()
}

func (n *forceTestNode) DeleteAccount(_ context.Context, id domain.AccountID, force bool, caller domain.Caller) error {
	n.calls = append(n.calls, forceNodeCall{domain.AuditActionDeleteAccount, string(id), force, caller})
	return nil
}

func (n *forceTestNode) DeleteGroup(_ context.Context, code string, force bool, caller domain.Caller) error {
	n.calls = append(n.calls, forceNodeCall{domain.AuditActionDeleteGroup, code, force, caller})
	return nil
}

func (n *forceTestNode) DeleteAsset(_ context.Context, code string, force bool, caller domain.Caller) error {
	n.calls = append(n.calls, forceNodeCall{domain.AuditActionDeleteAsset, code, force, caller})
	return nil
}

func (n *forceTestNode) DeleteAssetClass(_ context.Context, code string, force bool, caller domain.Caller) error {
	n.calls = append(n.calls, forceNodeCall{domain.AuditActionDeleteAssetClass, code, force, caller})
	return nil
}

func (n *forceTestNode) GetOrder(context.Context, domain.ExternalID) (domain.OrderDetail, error) {
	return n.order, nil
}

func (n *forceTestNode) ApplyExecutionReport(_ context.Context, in domain.ExecutionReportInput, caller domain.Caller) (engine.ExecutionReportResult, error) {
	n.calls = append(n.calls, forceNodeCall{domain.AuditActionExecutionReport, string(in.Order), in.Force, caller})
	return engine.ExecutionReportResult{}, nil
}

func (n *forceTestNode) AppendAudit(ctx context.Context, entry store.AuditEntry, caller domain.Caller) error {
	n.audits = append(n.audits, forceAuditCall{entry, caller, ctx})
	if n.onAudit != nil {
		n.onAudit()
	}
	return n.auditErr
}

func TestForceAuthorization(t *testing.T) {
	t.Parallel()
	for _, op := range []struct {
		action domain.AuditAction
		target string
		entry  store.AuditEntry
		code   domain.AuditDetailCode
		params domain.AuditDetailParams
		invoke func(*Service, context.Context, bool) error
	}{
		{
			action: domain.AuditActionDeleteAccount, target: "acc-1",
			entry:  store.AuditEntry{Action: domain.AuditActionDeleteAccount, Account: "acc-1"},
			code:   domain.AuditDetailDeleteAccountForceRefused,
			params: domain.AuditDetailParams{"account": "acc-1"},
			invoke: func(s *Service, ctx context.Context, force bool) error {
				return s.DeleteAccount(ctx, "acc-1", force)
			},
		},
		{
			action: domain.AuditActionDeleteGroup, target: "group-1",
			entry:  store.AuditEntry{Action: domain.AuditActionDeleteGroup, Group: "group-1"},
			code:   domain.AuditDetailDeleteGroupForceRefused,
			params: domain.AuditDetailParams{"group": "group-1"},
			invoke: func(s *Service, ctx context.Context, force bool) error {
				return s.DeleteGroup(ctx, "group-1", force)
			},
		},
		{
			action: domain.AuditActionDeleteAsset, target: "USD",
			entry:  store.AuditEntry{Action: domain.AuditActionDeleteAsset, Asset: "USD"},
			code:   domain.AuditDetailDeleteAssetForceRefused,
			params: domain.AuditDetailParams{"asset": "USD"},
			invoke: func(s *Service, ctx context.Context, force bool) error {
				return s.DeleteAsset(ctx, "USD", force)
			},
		},
		{
			action: domain.AuditActionDeleteAssetClass, target: "equity",
			entry:  store.AuditEntry{Action: domain.AuditActionDeleteAssetClass},
			code:   domain.AuditDetailDeleteAssetClassForceRefused,
			params: domain.AuditDetailParams{"class": "equity"},
			invoke: func(s *Service, ctx context.Context, force bool) error {
				return s.DeleteAssetClass(ctx, "equity", force)
			},
		},
		{
			action: domain.AuditActionExecutionReport, target: "order-1",
			entry:  store.AuditEntry{Action: domain.AuditActionExecutionReport, Account: "acc-1"},
			code:   domain.AuditDetailExecutionReportForceRefused,
			params: domain.AuditDetailParams{"account": "acc-1", "order": "order-1"},
			invoke: func(s *Service, ctx context.Context, force bool) error {
				_, _, err := s.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
					Order: "order-1", OrderStatus: domain.OrderStatusFilled, Force: force,
					FillQuantity: "1", FillPrice: "100",
				})
				return err
			},
		},
	} {
		t.Run(string(op.action), func(t *testing.T) {
			caller := domain.Caller{Source: domain.SourceAPI, Principal: "operator-1"}
			refusal := fmt.Errorf("forced operation denied: %w", domain.ErrForbidden)
			identifier := "force:" + string(op.action)
			for _, dropCopy := range []bool{true, false} {
				if !dropCopy && op.action != domain.AuditActionExecutionReport {
					continue
				}
				t.Run(fmt.Sprintf("refused/drop_copy=%t", dropCopy), func(t *testing.T) {
					n := &forceTestNode{order: domain.OrderDetail{Order: domain.Order{
						ExternalID: "order-1", Account: "acc-1", DropCopy: dropCopy,
					}}}
					calls := 0
					ctx, cancel := context.WithCancel(auth.ContextWithCaller(context.Background(), caller))
					cancel()
					authorizer := forceAuthorizeFunc(func(gotCtx context.Context, gotCaller domain.Caller, gotID string) error {
						calls++
						if gotCaller != caller || gotID != identifier || gotCtx != ctx {
							t.Errorf("Authorize got caller=%+v identifier=%q context=%v", gotCaller, gotID, gotCtx)
						}
						return refusal
					})
					s, err := New(n, nil, nil, testLockSettlementPrice, WithForceAuthorizer(authorizer))
					if err != nil {
						t.Fatal(err)
					}
					if op.action == domain.AuditActionDeleteAsset {
						n.onAudit = func() {
							done := make(chan error, 1)
							go func() { done <- s.RestartMarketData(context.Background()) }()
							select {
							case err := <-done:
								if err != nil {
									t.Fatalf("RestartMarketData during refusal audit: %v", err)
								}
							case <-time.After(5 * time.Second):
								t.Fatal("RestartMarketData blocked during refusal audit: DeleteAsset holds marketDataMu")
							}
						}
					}
					if err := op.invoke(s, ctx, true); err != refusal || !errors.Is(err, domain.ErrForbidden) {
						t.Fatalf("forced operation = %v, want unchanged refusal %v", err, refusal)
					}
					if calls != 1 || len(n.calls) != 0 || len(n.audits) != 1 {
						t.Fatalf("authorizer calls=%d node calls=%v audit calls=%d", calls, n.calls, len(n.audits))
					}
					params := domain.AuditDetailParams{"refusal": refusal.Error()}
					maps.Copy(params, op.params)
					want := op.entry
					want.Detail = domain.NewAuditDetail(op.code, params)
					audit := n.audits[0]
					if !reflect.DeepEqual(audit.entry, want) || audit.caller != caller {
						t.Errorf("AppendAudit = %+v caller=%+v, want %+v caller=%+v", audit.entry, audit.caller, want, caller)
					}
					if audit.ctx.Err() != nil || audit.ctx.Done() != nil {
						t.Errorf("audit context was cancelled: %v", audit.ctx.Err())
					}
					if got, err := auth.CallerFromContext(audit.ctx); err != nil || got != caller {
						t.Errorf("audit context lost caller: %+v, %v", got, err)
					}
				})
			}
			for _, tc := range []struct {
				name      string
				force     bool
				caller    domain.Caller
				install   bool
				refusal   error
				wantCalls int
			}{
				{name: "default", force: true, caller: caller},
				{name: "allowed", force: true, caller: caller, install: true, wantCalls: 1},
				{name: "not_forced", caller: caller, install: true, refusal: refusal},
				{name: "system", force: true, caller: auth.SystemCaller(), install: true, refusal: refusal},
			} {
				t.Run(tc.name, func(t *testing.T) {
					n := &forceTestNode{order: domain.OrderDetail{Order: domain.Order{
						ExternalID: "order-1", Account: "acc-1", DropCopy: true,
					}}}
					calls := 0
					var opts []Option
					if tc.install {
						opts = append(opts, WithForceAuthorizer(forceAuthorizeFunc(func(_ context.Context, got domain.Caller, id string) error {
							calls++
							if got != tc.caller || id != identifier {
								t.Errorf("Authorize got caller=%+v identifier=%q", got, id)
							}
							return tc.refusal
						})))
					}
					s, err := New(n, nil, nil, testLockSettlementPrice, opts...)
					if err != nil {
						t.Fatal(err)
					}
					ctx := auth.ContextWithCaller(context.Background(), tc.caller)
					if err := op.invoke(s, ctx, tc.force); err != nil {
						t.Fatalf("operation: %v", err)
					}
					want := []forceNodeCall{{op.action, op.target, tc.force, tc.caller}}
					if calls != tc.wantCalls || !reflect.DeepEqual(n.calls, want) || len(n.audits) != 0 {
						t.Errorf("authorizer calls=%d node calls=%+v audits=%d; want calls=%d node=%+v no audits", calls, n.calls, len(n.audits), tc.wantCalls, want)
					}
				})
			}
		})
	}
}

func TestForceAuthorizationAuditFailure(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("audit storage unavailable")
	refusal := fmt.Errorf("force denied: %w", domain.ErrForbidden)
	n := &forceTestNode{auditErr: auditErr}
	s, err := New(n, nil, nil, testLockSettlementPrice, WithForceAuthorizer(forceAuthorizeFunc(
		func(context.Context, domain.Caller, string) error { return refusal },
	)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.ContextWithCaller(context.Background(), domain.Caller{Source: domain.SourcePanel, Principal: "operator-1"})
	err = s.DeleteAccount(ctx, "acc-1", true)
	if !errors.Is(err, auditErr) || errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("DeleteAccount = %v, want audit error, not refusal", err)
	}
	want := fmt.Sprintf("audit refused %s (%v): %v", ForceDeleteAccount, refusal, auditErr)
	if err.Error() != want || len(n.calls) != 0 || len(n.audits) != 1 {
		t.Errorf("error=%v node calls=%v audits=%d, want %q, no node calls and one audit attempt", err, n.calls, len(n.audits), want)
	}
}

func TestNewRejectsNilForceAuthorizer(t *testing.T) {
	t.Parallel()
	s, err := New(&forceTestNode{}, nil, nil, testLockSettlementPrice, WithForceAuthorizer(nil))
	if s != nil || err == nil || !strings.Contains(err.Error(), "backend: nil force authorizer") {
		t.Fatalf("New(nil force authorizer) = %v, %v", s, err)
	}
}
