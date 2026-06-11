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
// Please see https://openpit.dev and the OWNERS file for details.

package backend_test

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/node"
)

// fakeNode records the commands routed to it and returns canned data. It never
// touches an engine or a store, so the backend validation/routing tests run in
// isolation.
type fakeNode struct {
	version  string
	accounts []domain.Account
	limits   []domain.Limit
	audit    []domain.AuditRow

	putLimitCalls    []domain.Limit
	deleteLimitCalls []domain.LimitTarget
	createCalls      []node.Key
	blockCalls       []blockCall

	getAccountErr error
}

type blockCall struct {
	key     node.Key
	blocked bool
	reason  string
}

func (n *fakeNode) Health(context.Context) (node.Health, error) {
	return node.Health{}, nil
}
func (n *fakeNode) EngineVersion() string { return n.version }
func (n *fakeNode) Owns(node.Key) bool    { return true }

func (n *fakeNode) ListAccounts(context.Context) ([]domain.Account, error) {
	return n.accounts, nil
}

func (n *fakeNode) CreateAccount(
	_ context.Context, key node.Key, _ domain.Caller,
) (domain.Account, error) {
	n.createCalls = append(n.createCalls, key)
	account := domain.Account{Tenant: key.Tenant, ID: key.Account}
	n.accounts = append(n.accounts, account)
	return account, nil
}

func (n *fakeNode) SetAccountBlocked(
	_ context.Context, key node.Key, blocked bool, reason string, _ domain.Caller,
) error {
	n.blockCalls = append(n.blockCalls, blockCall{key, blocked, reason})
	return nil
}

func (n *fakeNode) GetAccountState(
	_ context.Context, key node.Key,
) (domain.Account, []domain.Limit, error) {
	if n.getAccountErr != nil {
		return domain.Account{}, nil, n.getAccountErr
	}
	return domain.Account{Tenant: key.Tenant, ID: key.Account}, n.limits, nil
}

func (n *fakeNode) ListLimits(
	context.Context, domain.AccountID,
) ([]domain.Limit, error) {
	return n.limits, nil
}

func (n *fakeNode) PutLimit(_ context.Context, limit domain.Limit, _ domain.Caller) error {
	n.putLimitCalls = append(n.putLimitCalls, limit)
	return nil
}

func (n *fakeNode) DeleteLimit(
	_ context.Context, target domain.LimitTarget, _ domain.Caller,
) error {
	n.deleteLimitCalls = append(n.deleteLimitCalls, target)
	return nil
}

func (n *fakeNode) SetAccountGroup(
	context.Context, node.Key, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) SetAccountNotes(
	context.Context, node.Key, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) CreateGroup(context.Context, domain.AccountGroup, domain.Caller) error {
	return nil
}

func (n *fakeNode) ListGroups(
	context.Context, domain.TenantID,
) ([]domain.AccountGroup, error) {
	return nil, nil
}

func (n *fakeNode) GetGroup(
	context.Context, domain.TenantID, string,
) (domain.AccountGroup, []domain.Account, bool, error) {
	return domain.AccountGroup{}, nil, false, nil
}

func (n *fakeNode) SetGroupNotes(
	context.Context, domain.TenantID, string, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) SetGroupBlocked(
	context.Context, domain.TenantID, string, bool, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) DeleteGroup(context.Context, domain.TenantID, string, domain.Caller) error {
	return nil
}

func (n *fakeNode) ApplyAdjustment(
	context.Context, node.Key, domain.AdjustmentRequest, domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	return domain.AccountAdjustmentRecord{}, nil
}

func (n *fakeNode) ListBalances(
	context.Context, domain.TenantID, domain.AccountID, string,
) ([]domain.Balance, error) {
	return nil, nil
}

func (n *fakeNode) GetBalance(
	context.Context, domain.TenantID, domain.AccountID, string,
) (domain.Balance, bool, error) {
	return domain.Balance{}, false, nil
}

func (n *fakeNode) ListAdjustments(
	context.Context, domain.TenantID, domain.AccountID, domain.Source, int,
) ([]domain.AccountAdjustmentRecord, error) {
	return nil, nil
}

func (n *fakeNode) SubmitOrder(
	context.Context, node.Key, domain.Order, domain.Caller,
) (domain.Order, error) {
	return domain.Order{}, nil
}

func (n *fakeNode) ApplyExecutionReport(
	context.Context, node.Key, domain.ExecutionReportInput, domain.Caller,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, nil
}

func (n *fakeNode) GetOrder(
	context.Context, domain.TenantID, int64,
) (domain.OrderDetail, error) {
	return domain.OrderDetail{}, nil
}

func (n *fakeNode) ListOrders(
	context.Context, domain.TenantID, domain.AccountID, domain.Source, int,
) ([]domain.Order, error) {
	return nil, nil
}

func (n *fakeNode) ListOrderEvents(
	context.Context, domain.TenantID, int64,
) ([]domain.OrderEvent, error) {
	return nil, nil
}

func (n *fakeNode) ListTrades(
	context.Context, domain.TenantID, domain.AccountID, domain.Source, int,
) ([]domain.Trade, error) {
	return nil, nil
}

func (n *fakeNode) ListAudit(context.Context, int) ([]domain.AuditRow, error) {
	return n.audit, nil
}

func (n *fakeNode) CheckOrder(
	context.Context, node.Key, domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

func (n *fakeNode) Close() error { return nil }

// fakeRouter routes every key to the single fake node.
type fakeRouter struct{ node *fakeNode }

func (r *fakeRouter) Route(node.Key) (node.Node, error) { return r.node, nil }
func (r *fakeRouter) All() []node.Node                  { return []node.Node{r.node} }

func newTestService() (*backend.Service, *fakeNode) {
	fn := &fakeNode{}
	return backend.New(&fakeRouter{node: fn}), fn
}

func TestService_CreateAccountValidates(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	if _, err := svc.CreateAccount(ctx, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
	if len(fn.createCalls) != 0 {
		t.Fatalf("invalid input must not reach the node")
	}

	if _, err := svc.CreateAccount(ctx, "acc-1"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if len(fn.createCalls) != 1 {
		t.Fatalf("valid create must route to node")
	}
}

func TestService_PutLimitValidatesBeforeRouting(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// account scope on order_size_limit is not allowed - validation must reject
	// before the node is touched.
	bad := domain.Limit{
		Target: domain.LimitTarget{
			Policy:  domain.PolicyOrderSizeLimit,
			Scope:   domain.ScopeAccount,
			Account: "acc-1",
		},
		Values: []domain.LimitValue{{Kind: domain.KindMaxQuantity, Value: "1"}},
	}
	if err := svc.PutLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.putLimitCalls) != 0 {
		t.Fatalf("invalid limit must not reach the node")
	}
}

func TestService_PutLimitChecksAccountExists(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.getAccountErr = domain.ErrNotFound
	ctx := context.Background()

	limit := domain.Limit{
		Target: domain.LimitTarget{
			Policy:  domain.PolicyRateLimit,
			Scope:   domain.ScopeAccountAsset,
			Account: "acc-1",
			Asset:   "BTC",
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := svc.PutLimit(ctx, limit); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound for missing account, got %v", err)
	}
	if len(fn.putLimitCalls) != 0 {
		t.Fatalf("missing account must not reach PutLimit")
	}
}

func TestService_PutLimitSetsDefaultTenant(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	limit := domain.Limit{
		Target: domain.LimitTarget{
			Policy: domain.PolicyRateLimit,
			Scope:  domain.ScopeBroker,
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := svc.PutLimit(ctx, limit); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}
	if len(fn.putLimitCalls) != 1 {
		t.Fatalf("want one PutLimit call")
	}
	if fn.putLimitCalls[0].Target.Tenant != domain.DefaultTenant {
		t.Fatalf("tenant not defaulted: %q", fn.putLimitCalls[0].Target.Tenant)
	}
}

func TestService_DeleteLimitValidatesTarget(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// broker scope is not allowed for pnl_bounds; target validation must reject.
	bad := domain.LimitTarget{
		Policy: domain.PolicyPnlBoundsKillSwitch,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.deleteLimitCalls) != 0 {
		t.Fatalf("invalid target must not reach the node")
	}

	good := domain.LimitTarget{
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, good); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if len(fn.deleteLimitCalls) != 1 {
		t.Fatalf("valid delete must route to node")
	}
	if fn.deleteLimitCalls[0].Tenant != domain.DefaultTenant {
		t.Fatalf("tenant not defaulted on delete")
	}
}

func TestService_BlockAccountValidates(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	if err := svc.BlockAccount(ctx, "", "risk"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
	if len(fn.blockCalls) != 0 {
		t.Fatalf("invalid id must not reach the node")
	}

	if err := svc.BlockAccount(ctx, "acc-1", "risk"); err != nil {
		t.Fatalf("BlockAccount: %v", err)
	}
	if len(fn.blockCalls) != 1 || !fn.blockCalls[0].blocked ||
		fn.blockCalls[0].reason != "risk" {
		t.Fatalf("block not routed correctly: %+v", fn.blockCalls)
	}
}

func TestService_AggregatesReads(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.accounts = []domain.Account{{ID: "a"}, {ID: "b"}}
	fn.limits = []domain.Limit{{Target: domain.LimitTarget{Policy: domain.PolicyRateLimit}}}
	fn.audit = []domain.AuditRow{{ID: 1}}
	ctx := context.Background()

	accounts, err := svc.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("ListAccounts: %v len=%d", err, len(accounts))
	}
	limits, err := svc.ListLimits(ctx, "")
	if err != nil || len(limits) != 1 {
		t.Fatalf("ListLimits: %v len=%d", err, len(limits))
	}
	rows, err := svc.ListAudit(ctx, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListAudit: %v len=%d", err, len(rows))
	}
}
