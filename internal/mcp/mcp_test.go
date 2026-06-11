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

package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/internal/domain"
)

// fakeSource is a controllable fake for the Source interface.
type fakeSource struct {
	status        Status
	statusErr     error
	account       domain.Account
	limits        []domain.Limit
	auditRows     []domain.AuditRow
	accStateErr   error
	listLimitsErr error
	listAuditErr  error
}

func (f *fakeSource) Status(_ context.Context) (Status, error) {
	return f.status, f.statusErr
}

func (f *fakeSource) GetAccountState(
	_ context.Context, id domain.AccountID,
) (domain.Account, []domain.Limit, error) {
	if f.accStateErr != nil {
		return domain.Account{}, nil, f.accStateErr
	}
	if f.account.ID != id {
		return domain.Account{}, nil, fmt.Errorf("account %q: %w", id, domain.ErrNotFound)
	}
	return f.account, f.limits, nil
}

func (f *fakeSource) ListLimits(
	_ context.Context, _ domain.AccountID,
) ([]domain.Limit, error) {
	return f.limits, f.listLimitsErr
}

func (f *fakeSource) ListAudit(_ context.Context, _ int) ([]domain.AuditRow, error) {
	return f.auditRows, f.listAuditErr
}

// callHealth invokes the health tool handler directly.
func callHealth(t *testing.T, src Source) *sdkmcp.CallToolResultFor[healthOutput] {
	t.Helper()
	h := healthHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[healthInput]{})
	if err != nil {
		t.Fatalf("healthHandler returned protocol error: %v", err)
	}
	return res
}

// callGetAccountState invokes the get_account_state handler directly.
func callGetAccountState(
	t *testing.T, src Source, account string,
) *sdkmcp.CallToolResultFor[getAccountStateOutput] {
	t.Helper()
	h := getAccountStateHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[getAccountStateInput]{
			Arguments: getAccountStateInput{Account: account},
		})
	if err != nil {
		t.Fatalf("getAccountStateHandler returned protocol error: %v", err)
	}
	return res
}

// callGetLimits invokes the get_limits handler directly.
func callGetLimits(
	t *testing.T, src Source, account string,
) *sdkmcp.CallToolResultFor[getLimitsOutput] {
	t.Helper()
	h := getLimitsHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[getLimitsInput]{
			Arguments: getLimitsInput{Account: account},
		})
	if err != nil {
		t.Fatalf("getLimitsHandler returned protocol error: %v", err)
	}
	return res
}

// callGetAudit invokes the get_audit handler directly.
func callGetAudit(
	t *testing.T, src Source, limit int,
) *sdkmcp.CallToolResultFor[getAuditOutput] {
	t.Helper()
	h := getAuditHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[getAuditInput]{
			Arguments: getAuditInput{Limit: limit},
		})
	if err != nil {
		t.Fatalf("getAuditHandler returned protocol error: %v", err)
	}
	return res
}

// requireNotToolError fails the test when the result carries IsError.
func requireNotToolError(t *testing.T, isErr bool) {
	t.Helper()
	if isErr {
		t.Fatal("expected success result, got tool error")
	}
}

// requireToolError fails the test when the result does not carry IsError.
func requireToolError(t *testing.T, isErr bool) {
	t.Helper()
	if !isErr {
		t.Fatal("expected tool error result, got success")
	}
}

// textContent returns the first TextContent text of result content, or "".
func textContent(content []sdkmcp.Content) string {
	for _, c := range content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// -- health --

func TestHealthHappyPath(t *testing.T) {
	src := &fakeSource{
		status: Status{
			Healthy: true,
			Nodes: []NodeHealth{{
				Engine: EngineHealth{Version: "v1.2.3", Running: true},
				Store:  StoreHealth{Path: "/tmp/db", Reachable: true},
			}},
		},
	}
	res := callHealth(t, src)
	requireNotToolError(t, res.IsError)
	if !res.StructuredContent.Healthy {
		t.Error("want healthy:true")
	}
	if len(res.StructuredContent.Nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(res.StructuredContent.Nodes))
	}
	if res.StructuredContent.Nodes[0].Engine.Version != "v1.2.3" {
		t.Error("wrong engine version")
	}
	if got := textContent(res.Content); got != "officer healthy: 1 node(s)" {
		t.Errorf("unexpected text: %q", got)
	}
}

func TestHealthDegraded(t *testing.T) {
	src := &fakeSource{
		status: Status{Healthy: false, Nodes: []NodeHealth{{}}},
	}
	res := callHealth(t, src)
	requireNotToolError(t, res.IsError)
	if res.StructuredContent.Healthy {
		t.Error("want healthy:false")
	}
	if got := textContent(res.Content); got != "officer degraded: 1 node(s)" {
		t.Errorf("unexpected text: %q", got)
	}
}

func TestHealthSourceFailure(t *testing.T) {
	src := &fakeSource{statusErr: fmt.Errorf("db offline")}
	res := callHealth(t, src)
	requireToolError(t, res.IsError)
	if got := textContent(res.Content); got != "officer status unavailable" {
		t.Errorf("unexpected error text: %q", got)
	}
}

// -- get_account_state --

func TestGetAccountStateHappyPath(t *testing.T) {
	src := &fakeSource{
		account: domain.Account{
			ID:      "acc-1",
			Tenant:  domain.DefaultTenant,
			Blocked: false,
		},
		limits: []domain.Limit{
			{
				Target: domain.LimitTarget{
					Tenant: domain.DefaultTenant,
					Policy: domain.PolicyRateLimit,
					Scope:  domain.ScopeAccount,
					Account: "acc-1",
				},
				Values: []domain.LimitValue{
					{Kind: domain.KindMaxOrders, Value: "100"},
					{Kind: domain.KindWindow, Value: "1s"},
				},
			},
		},
	}
	res := callGetAccountState(t, src, "acc-1")
	requireNotToolError(t, res.IsError)
	if res.StructuredContent.Account.ID != "acc-1" {
		t.Errorf("want id acc-1, got %q", res.StructuredContent.Account.ID)
	}
	if res.StructuredContent.Account.Blocked {
		t.Error("want blocked:false")
	}
	if len(res.StructuredContent.Limits) != 1 {
		t.Fatalf("want 1 limit, got %d", len(res.StructuredContent.Limits))
	}
	lim := res.StructuredContent.Limits[0]
	if lim.Policy != domain.PolicyRateLimit {
		t.Errorf("wrong policy: %q", lim.Policy)
	}
	if lim.Values[domain.KindMaxOrders] != "100" {
		t.Errorf("wrong max_orders: %q", lim.Values[domain.KindMaxOrders])
	}
}

func TestGetAccountStateNotFound(t *testing.T) {
	src := &fakeSource{account: domain.Account{ID: "other"}}
	res := callGetAccountState(t, src, "acc-1")
	requireToolError(t, res.IsError)
	txt := textContent(res.Content)
	if txt == "" || txt == "get account state failed" {
		t.Errorf("expected not-found message, got %q", txt)
	}
}

func TestGetAccountStateSourceFailure(t *testing.T) {
	src := &fakeSource{
		accStateErr: fmt.Errorf("internal error"),
		account:     domain.Account{ID: "acc-1"},
	}
	res := callGetAccountState(t, src, "acc-1")
	requireToolError(t, res.IsError)
	if got := textContent(res.Content); got != "get account state failed" {
		t.Errorf("unexpected error text: %q", got)
	}
}

func TestGetAccountStateEmptyAccount(t *testing.T) {
	res := callGetAccountState(t, &fakeSource{}, "")
	requireToolError(t, res.IsError)
	if got := textContent(res.Content); got != "account is required" {
		t.Errorf("unexpected error text: %q", got)
	}
}

func TestGetAccountStateBlockedAccount(t *testing.T) {
	src := &fakeSource{
		account: domain.Account{
			ID:          "acc-2",
			Tenant:      domain.DefaultTenant,
			Blocked:     true,
			BlockReason: "compliance hold",
		},
	}
	res := callGetAccountState(t, src, "acc-2")
	requireNotToolError(t, res.IsError)
	if !res.StructuredContent.Account.Blocked {
		t.Error("want blocked:true")
	}
	if res.StructuredContent.Account.BlockReason != "compliance hold" {
		t.Errorf("wrong block reason: %q", res.StructuredContent.Account.BlockReason)
	}
}

// -- get_limits --

func TestGetLimitsHappyPath(t *testing.T) {
	src := &fakeSource{
		limits: []domain.Limit{
			{
				Target: domain.LimitTarget{
					Policy: domain.PolicyOrderSizeLimit,
					Scope:  domain.ScopeBroker,
				},
				Values: []domain.LimitValue{
					{Kind: domain.KindMaxQuantity, Value: "500"},
				},
			},
			{
				Target: domain.LimitTarget{
					Policy: domain.PolicyRateLimit,
					Scope:  domain.ScopeAsset,
					Asset:  "BTC",
				},
				Values: []domain.LimitValue{
					{Kind: domain.KindMaxOrders, Value: "10"},
					{Kind: domain.KindWindow, Value: "1m"},
				},
			},
		},
	}
	res := callGetLimits(t, src, "")
	requireNotToolError(t, res.IsError)
	if len(res.StructuredContent.Limits) != 2 {
		t.Fatalf("want 2 limits, got %d", len(res.StructuredContent.Limits))
	}
}

func TestGetLimitsWithAccount(t *testing.T) {
	src := &fakeSource{limits: []domain.Limit{
		{
			Target: domain.LimitTarget{
				Policy:  domain.PolicyRateLimit,
				Scope:   domain.ScopeAccount,
				Account: "acc-1",
			},
			Values: []domain.LimitValue{
				{Kind: domain.KindMaxOrders, Value: "5"},
				{Kind: domain.KindWindow, Value: "1s"},
			},
		},
	}}
	res := callGetLimits(t, src, "acc-1")
	requireNotToolError(t, res.IsError)
	if len(res.StructuredContent.Limits) != 1 {
		t.Fatalf("want 1 limit, got %d", len(res.StructuredContent.Limits))
	}
	if res.StructuredContent.Limits[0].Account != "acc-1" {
		t.Errorf("wrong account: %q", res.StructuredContent.Limits[0].Account)
	}
}

func TestGetLimitsEmpty(t *testing.T) {
	res := callGetLimits(t, &fakeSource{}, "")
	requireNotToolError(t, res.IsError)
	if len(res.StructuredContent.Limits) != 0 {
		t.Errorf("want 0 limits, got %d", len(res.StructuredContent.Limits))
	}
}

func TestGetLimitsSourceFailure(t *testing.T) {
	src := &fakeSource{listLimitsErr: fmt.Errorf("store down")}
	res := callGetLimits(t, src, "")
	requireToolError(t, res.IsError)
	if got := textContent(res.Content); got != "list limits failed" {
		t.Errorf("unexpected error text: %q", got)
	}
}

// -- get_audit --

func TestGetAuditHappyPath(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 0, 0, 1, time.UTC)
	src := &fakeSource{
		auditRows: []domain.AuditRow{
			{
				ID:     12,
				At:     ts,
				Actor:  "operator",
				Action: domain.AuditActionSetLimit,
				Account: "acc-1",
				Detail: "set limit rate_limit account=acc-1 max_orders=100 window=1s",
			},
		},
	}
	res := callGetAudit(t, src, 0)
	requireNotToolError(t, res.IsError)
	if len(res.StructuredContent.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(res.StructuredContent.Entries))
	}
	e := res.StructuredContent.Entries[0]
	if e.ID != 12 {
		t.Errorf("wrong id: %d", e.ID)
	}
	if e.Actor != "operator" {
		t.Errorf("wrong actor: %q", e.Actor)
	}
	if e.Action != string(domain.AuditActionSetLimit) {
		t.Errorf("wrong action: %q", e.Action)
	}
	if e.Account != "acc-1" {
		t.Errorf("wrong account: %q", e.Account)
	}
	if !e.At.Equal(ts) {
		t.Errorf("wrong at: %v", e.At)
	}
}

func TestGetAuditDefaultLimit(t *testing.T) {
	// Source receives the default 50 when caller passes 0.
	src := &captureNSource{}
	h := getAuditHandler(src)
	_, _ = h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[getAuditInput]{
			Arguments: getAuditInput{Limit: 0},
		})
	if src.lastN != auditDefaultLimit {
		t.Errorf("want default %d, got %d", auditDefaultLimit, src.lastN)
	}
}

func TestGetAuditCapAt500(t *testing.T) {
	src := &captureNSource{}
	h := getAuditHandler(src)
	_, _ = h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[getAuditInput]{
			Arguments: getAuditInput{Limit: 99999},
		})
	if src.lastN != auditMaxLimit {
		t.Errorf("want cap %d, got %d", auditMaxLimit, src.lastN)
	}
}

func TestGetAuditSourceFailure(t *testing.T) {
	src := &fakeSource{listAuditErr: fmt.Errorf("db down")}
	res := callGetAudit(t, src, 10)
	requireToolError(t, res.IsError)
	if got := textContent(res.Content); got != "list audit failed" {
		t.Errorf("unexpected error text: %q", got)
	}
}

func TestGetAuditEmpty(t *testing.T) {
	res := callGetAudit(t, &fakeSource{}, 0)
	requireNotToolError(t, res.IsError)
	if len(res.StructuredContent.Entries) != 0 {
		t.Errorf("want 0 entries, got %d", len(res.StructuredContent.Entries))
	}
}

// captureNSource records the n passed to ListAudit for assertion in limit
// tests.
type captureNSource struct {
	lastN int
}

func (c *captureNSource) Status(_ context.Context) (Status, error) {
	return Status{}, nil
}
func (c *captureNSource) GetAccountState(_ context.Context, _ domain.AccountID) (
	domain.Account, []domain.Limit, error) {
	return domain.Account{}, nil, nil
}
func (c *captureNSource) ListLimits(_ context.Context, _ domain.AccountID) (
	[]domain.Limit, error) {
	return nil, nil
}
func (c *captureNSource) ListAudit(_ context.Context, n int) ([]domain.AuditRow, error) {
	c.lastN = n
	return nil, nil
}

// -- NewServer construction --

// TestNewServerSucceeds guards against panics from bad struct tags or other
// registration errors that only surface when AddTool infers the input schema.
func TestNewServerSucceeds(t *testing.T) {
	srv, err := NewServer(&fakeSource{}, nil)
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	if srv == nil {
		t.Fatal("NewServer returned nil server")
	}
}
