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
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

// fakeSource is a controllable fake for the Source interface.
type fakeSource struct {
	status        Status
	statusErr     error
	account       domain.Account
	limits        node.AccountLimits
	orderDetail   domain.OrderDetail
	getOrderErr   error
	auditRows     []domain.AuditRow
	auditFilter   domain.AuditFilter
	checkResult   domain.CheckResult
	checkProbes   []domain.OrderProbe
	accStateErr   error
	listLimitsErr error
	listAuditErr  error
	checkErr      error
	setMDCalls    []setMDCall

	// disabledCommands lists command names the fake reports as disabled; any
	// command not listed is enabled. cmdEnabledErr, when set, is returned from
	// CommandEnabled so the fail-open path can be exercised.
	disabledCommands map[string]bool
	cmdEnabledErr    error
}

type setMDCall struct {
	instanceID     string
	externalSymbol string
	enabled        bool
}

func (f *fakeSource) CommandEnabled(_ context.Context, command string) (bool, error) {
	if f.cmdEnabledErr != nil {
		return false, f.cmdEnabledErr
	}
	if f.disabledCommands[command] {
		return false, nil
	}
	return true, nil
}

func (f *fakeSource) Status(_ context.Context) (Status, error) {
	return f.status, f.statusErr
}

func (f *fakeSource) GetAccountState(
	_ context.Context, id domain.AccountID,
) (domain.Account, node.AccountLimits, error) {
	if f.accStateErr != nil {
		return domain.Account{}, node.AccountLimits{}, f.accStateErr
	}
	if f.account.Code != id {
		return domain.Account{}, node.AccountLimits{},
			fmt.Errorf("account %q: %w", id, domain.ErrNotFound)
	}
	return f.account, f.limits, nil
}

func (f *fakeSource) ListLimits(
	_ context.Context, _ domain.AccountID,
) (node.AccountLimits, error) {
	return f.limits, f.listLimitsErr
}

func (f *fakeSource) GetOrder(
	_ context.Context, _ string,
) (domain.OrderDetail, error) {
	return f.orderDetail, f.getOrderErr
}

func (f *fakeSource) ListAudit(_ context.Context, _ int) ([]domain.AuditRow, error) {
	return f.auditRows, f.listAuditErr
}

func (f *fakeSource) ListAuditFiltered(
	_ context.Context, filter domain.AuditFilter, _ int,
) ([]domain.AuditRow, error) {
	f.auditFilter = filter
	return f.auditRows, f.listAuditErr
}

func (f *fakeSource) CheckOrder(
	_ context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	if f.checkErr != nil {
		return domain.CheckResult{}, f.checkErr
	}
	f.checkProbes = append(f.checkProbes, probe)
	return f.checkResult, nil
}

func (f *fakeSource) SetMarketDataInstrumentEnabled(
	_ context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	f.setMDCalls = append(f.setMDCalls, setMDCall{
		instanceID:     instanceID,
		externalSymbol: externalSymbol,
		enabled:        enabled,
	})
	return nil
}

// fakeSource stubs for the approval-token methods. They are never called in
// the existing tests; approval_test.go overrides them via approvalFakeSource.
func (f *fakeSource) SubmitOrderToken(
	_ context.Context, _ domain.Order, _ string, _ domain.MissingAccountPolicy,
) (SubmitOrderTokenResult, error) {
	return SubmitOrderTokenResult{}, nil
}

func (f *fakeSource) SubmitDropCopyOrder(
	context.Context, domain.Order, domain.MissingAccountPolicy,
) (SubmitDropCopyOrderResult, error) {
	return SubmitDropCopyOrderResult{}, nil
}

func (f *fakeSource) ConfirmExecution(
	_ context.Context, _ string, _ string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}

func (f *fakeSource) CancelOrder(
	_ context.Context, _ string, _, _ string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
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

// callGetOrder invokes the get_order handler directly.
func callGetOrder(
	t *testing.T, src Source, orderExternalID string,
) *sdkmcp.CallToolResultFor[getOrderOutput] {
	t.Helper()
	h := getOrderHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[getOrderInput]{
			Arguments: getOrderInput{OrderExternalID: orderExternalID},
		})
	if err != nil {
		t.Fatalf("getOrderHandler returned protocol error: %v", err)
	}
	return res
}

// callCheckOrder invokes the check_order handler directly.
func callCheckOrder(
	t *testing.T, src Source, in checkOrderInput,
) *sdkmcp.CallToolResultFor[checkOrderOutput] {
	t.Helper()
	h := checkOrderHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[checkOrderInput]{Arguments: in})
	if err != nil {
		t.Fatalf("checkOrderHandler returned protocol error: %v", err)
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

// mustExternalID parses a 22-char wire form into an ExternalID, failing the test
// on a malformed handle. It lets the tests build machine-record fixtures keyed by
// their opaque public handle, the only id the MCP surface ever speaks.
func mustExternalID(t *testing.T, s string) domain.ExternalID {
	t.Helper()
	id, err := domain.ParseExternalID(s)
	if err != nil {
		t.Fatalf("parse external id %q: %v", s, err)
	}
	return id
}

// noSurrogateIDFields are JSON keys that would betray a leaked surrogate or
// engine id on the MCP wire. Public `id` fields are opaque caller-visible ids;
// structuredContent is checked here only for explicitly internal identifiers.
var noSurrogateIDFields = []string{
	`"engineId":`, `"engineAccountId":`, `"surrogate":`, `"rowId":`,
}

// assertNoSurrogateID marshals v to JSON and fails if it carries any forbidden
// id field. It returns true on a clean payload so callers can assert inline.
func assertNoSurrogateID(t *testing.T, v any) bool {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal for surrogate-id scan: %v", err)
	}
	clean := true
	for _, field := range noSurrogateIDFields {
		if strings.Contains(string(raw), field) {
			t.Errorf("forbidden id field %s present in MCP output: %s", field, raw)
			clean = false
		}
	}
	return clean
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
			Code:    "acc-1",
			Title:   "Desk One",
			Blocked: false,
		},
		limits: node.AccountLimits{
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAccount,
					Account:   "acc-1",
					MaxOrders: 100,
					Window:    time.Second,
				},
			},
		},
	}
	res := callGetAccountState(t, src, "acc-1")
	requireNotToolError(t, res.IsError)
	if res.StructuredContent.Account.Code != "acc-1" {
		t.Errorf("want code acc-1, got %q", res.StructuredContent.Account.Code)
	}
	if res.StructuredContent.Account.Title != "Desk One" {
		t.Errorf("want title Desk One, got %q", res.StructuredContent.Account.Title)
	}
	if res.StructuredContent.Account.Blocked {
		t.Error("want blocked:false")
	}
	if len(res.StructuredContent.Limits.RateLimits) != 1 {
		t.Fatalf("want 1 rate limit, got %d", len(res.StructuredContent.Limits.RateLimits))
	}
	rate := res.StructuredContent.Limits.RateLimits[0]
	if rate.MaxOrders != 100 {
		t.Errorf("wrong max_orders: %d", rate.MaxOrders)
	}
	if rate.Account != "acc-1" {
		t.Errorf("wrong account: %q", rate.Account)
	}
	if !assertNoSurrogateID(t, res.StructuredContent.Account) {
		t.Error("account DTO leaked a surrogate/engine id")
	}
}

func TestGetAccountStateNotFound(t *testing.T) {
	src := &fakeSource{account: domain.Account{Code: "other"}}
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
		account:     domain.Account{Code: "acc-1"},
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
			Code:        "acc-2",
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
		limits: node.AccountLimits{
			OrderSizeLimits: []domain.LimitOrderSize{
				{
					Scope:       domain.ScopeBroker,
					MaxQuantity: "500",
				},
			},
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAsset,
					Asset:     "AAPL",
					MaxOrders: 10,
					Window:    time.Minute,
				},
			},
		},
	}
	res := callGetLimits(t, src, "")
	requireNotToolError(t, res.IsError)
	got := res.StructuredContent.Limits
	if len(got.OrderSizeLimits) != 1 {
		t.Fatalf("want 1 order-size limit, got %d", len(got.OrderSizeLimits))
	}
	if len(got.RateLimits) != 1 {
		t.Fatalf("want 1 rate limit, got %d", len(got.RateLimits))
	}
	if got.OrderSizeLimits[0].MaxQuantity != "500" {
		t.Errorf("wrong max_quantity: %q", got.OrderSizeLimits[0].MaxQuantity)
	}
}

func TestGetLimitsWithAccount(t *testing.T) {
	src := &fakeSource{limits: node.AccountLimits{
		RateLimits: []domain.LimitRate{
			{
				Scope:     domain.ScopeAccount,
				Account:   "acc-1",
				MaxOrders: 5,
				Window:    time.Second,
			},
		},
	}}
	res := callGetLimits(t, src, "acc-1")
	requireNotToolError(t, res.IsError)
	if len(res.StructuredContent.Limits.RateLimits) != 1 {
		t.Fatalf("want 1 rate limit, got %d", len(res.StructuredContent.Limits.RateLimits))
	}
	if res.StructuredContent.Limits.RateLimits[0].Account != "acc-1" {
		t.Errorf("wrong account: %q", res.StructuredContent.Limits.RateLimits[0].Account)
	}
}

func TestGetLimitsSpotFundsPnlBoundsOnly(t *testing.T) {
	src := &fakeSource{limits: node.AccountLimits{
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{
				Scope:      domain.ScopeAccount,
				Account:    "acc-spot",
				LowerBound: "-10.25",
				UpperBound: "99.50",
			},
		},
	}}
	res := callGetLimits(t, src, "acc-spot")
	requireNotToolError(t, res.IsError)
	if got := textContent(res.Content); got != "1 limit(s)" {
		t.Fatalf("summary = %q, want 1 limit(s)", got)
	}
	limits := res.StructuredContent.Limits
	if len(limits.RateLimits) != 0 ||
		len(limits.OrderSizeLimits) != 0 {
		t.Fatalf("spot-funds-only result included other limits: %+v", limits)
	}
	if len(limits.SpotFundsPnlBoundsLimits) != 1 {
		t.Fatalf(
			"want 1 spot-funds P&L limit, got %d",
			len(limits.SpotFundsPnlBoundsLimits),
		)
	}
	got := limits.SpotFundsPnlBoundsLimits[0]
	if got.Scope != domain.ScopeAccount ||
		got.Account != "acc-spot" ||
		got.LowerBound != "-10.25" ||
		got.UpperBound != "99.50" {
		t.Fatalf("spot-funds P&L limit = %+v", got)
	}
}

func TestGetLimitsEmpty(t *testing.T) {
	res := callGetLimits(t, &fakeSource{}, "")
	requireNotToolError(t, res.IsError)
	got := res.StructuredContent.Limits
	if len(got.RateLimits)+len(got.OrderSizeLimits)+len(got.SpotFundsPnlBoundsLimits) != 0 {
		t.Errorf("want 0 limits, got %+v", got)
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
	auditEID := mustExternalID(t, "AAAAAAAAAAAAAAAAAAAAAQ")
	src := &fakeSource{
		auditRows: []domain.AuditRow{
			{
				ExternalID: auditEID,
				At:         ts,
				Actor:      "operator",
				Action:     domain.AuditActionSetLimit,
				Account:    "acc-1",
				Detail:     "set limit rate_limit account=acc-1 max_orders=100 window=1s",
			},
		},
	}
	res := callGetAudit(t, src, 0)
	requireNotToolError(t, res.IsError)
	if len(res.StructuredContent.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(res.StructuredContent.Entries))
	}
	e := res.StructuredContent.Entries[0]
	if e.ExternalID != auditEID.String() {
		t.Errorf("wrong external id: %q", e.ExternalID)
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
	domain.Account, node.AccountLimits, error) {
	return domain.Account{}, node.AccountLimits{}, nil
}
func (c *captureNSource) ListLimits(_ context.Context, _ domain.AccountID) (
	node.AccountLimits, error) {
	return node.AccountLimits{}, nil
}
func (c *captureNSource) GetOrder(_ context.Context, _ string) (
	domain.OrderDetail, error) {
	return domain.OrderDetail{}, nil
}
func (c *captureNSource) ListAudit(_ context.Context, n int) ([]domain.AuditRow, error) {
	c.lastN = n
	return nil, nil
}
func (c *captureNSource) ListAuditFiltered(
	_ context.Context, _ domain.AuditFilter, n int,
) ([]domain.AuditRow, error) {
	c.lastN = n
	return nil, nil
}
func (c *captureNSource) CheckOrder(_ context.Context, _ domain.OrderProbe) (
	domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}
func (c *captureNSource) SetMarketDataInstrumentEnabled(
	context.Context, string, string, bool,
) error {
	return nil
}
func (c *captureNSource) CommandEnabled(context.Context, string) (bool, error) {
	return true, nil
}
func (c *captureNSource) SubmitOrderToken(
	context.Context, domain.Order, string, domain.MissingAccountPolicy,
) (SubmitOrderTokenResult, error) {
	return SubmitOrderTokenResult{}, nil
}

func (c *captureNSource) SubmitDropCopyOrder(
	context.Context, domain.Order, domain.MissingAccountPolicy,
) (SubmitDropCopyOrderResult, error) {
	return SubmitDropCopyOrderResult{}, nil
}
func (c *captureNSource) ConfirmExecution(
	context.Context, string, string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}
func (c *captureNSource) CancelOrder(
	context.Context, string, string, string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}

// -- get_order --

// TestGetOrderHappyPath: get_order returns the order keyed by its external id,
// its submit-verdict attestation read back from the verdict event, and its fills
// with their display prices. It asserts the opaque lock is never serialized and
// no surrogate or engine id appears anywhere in the structured output.
func TestGetOrderHappyPath(t *testing.T) {
	orderEID := mustExternalID(t, "b3JkZXItZXh0ZXJuYWwtMQ")
	tradeEID := mustExternalID(t, "dHJhZGUtZXh0ZXJuYWwtMQ")
	eventEID := mustExternalID(t, "ZXZlbnQtZXh0ZXJuYWwtMQ")
	src := &fakeSource{
		orderDetail: domain.OrderDetail{
			Order: domain.Order{
				ExternalID:  orderEID,
				Account:     "acc-1",
				BaseAsset:   "BTC",
				QuoteAsset:  "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "0.5",
				Price:       "50000",
				Status:      domain.OrderStatusFilled,
				// Lock is the opaque pre-trade blob; it must never reach the wire.
				Lock: []byte{0x01, 0x02, 0x03, 0x04},
			},
			Events: []domain.OrderEvent{
				{
					ExternalID: eventEID,
					Order:      orderEID,
					Type:       domain.OrderEventPreTradeAccepted,
					Attestation: &domain.EventAttestation{
						Token:       "eyAPPROVAL",
						KeyID:       "key-1",
						Alg:         "ed25519",
						RequestType: domain.AttestationRequestSubmit,
						Mode:        "hold",
						IssuedAt:    "2026-06-11T10:00:00Z",
					},
				},
			},
			Trades: []domain.Trade{
				{
					ExternalID: tradeEID,
					Order:      orderEID,
					Side:       domain.OrderSideBuy,
					Quantity:   "0.5",
					Price:      "49995",
					LockPrice:  "50000",
				},
			},
		},
	}

	res := callGetOrder(t, src, orderEID.String())
	requireNotToolError(t, res.IsError)
	out := res.StructuredContent
	if out.Order.ExternalID != orderEID.String() {
		t.Errorf("order external id: want %q got %q", orderEID.String(), out.Order.ExternalID)
	}
	if out.Order.Status != string(domain.OrderStatusFilled) {
		t.Errorf("status: want filled got %q", out.Order.Status)
	}
	if out.Approval == nil {
		t.Fatal("approval read-back: want non-nil approval")
	}
	if out.Approval.Token != "eyAPPROVAL" {
		t.Errorf("approval token: want eyAPPROVAL got %q", out.Approval.Token)
	}
	if !out.Approval.Signed {
		t.Error("approval signed: want true for alg ed25519")
	}
	if len(out.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(out.Trades))
	}
	if out.Trades[0].Price != "49995" || out.Trades[0].LockPrice != "50000" {
		t.Errorf("display prices not surfaced: %+v", out.Trades[0])
	}

	// The opaque lock blob must never be serialized.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	for _, banned := range []string{
		`"lock"`, `"Lock"`, "AQIDBA", `"externalId"`, `"orderExternalId"`,
	} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("opaque lock leaked into MCP output (%q): %s", banned, raw)
		}
	}
	assertNoSurrogateID(t, out)
}

// TestGetOrderCommission: get_order surfaces the same commission data the HTTP
// DTOs expose - per-order commission subtotals on the order and the per-fill
// commission on each trade - so an MCP client is not blind to it.
func TestGetOrderCommission(t *testing.T) {
	orderEID := mustExternalID(t, "b3JkZXItY29tbWlzc2lvbg")
	tradeEID := mustExternalID(t, "dHJhZGUtY29tbWlzc2lvbg")
	src := &fakeSource{
		orderDetail: domain.OrderDetail{
			Order: domain.Order{
				ExternalID:  orderEID,
				Account:     "acc-1",
				BaseAsset:   "BTC",
				QuoteAsset:  "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "0.5",
				Status:      domain.OrderStatusFilled,
				CommissionSubtotals: []domain.Commission{
					{Amount: "-0.12", Currency: "USD"},
				},
			},
			Trades: []domain.Trade{
				{
					ExternalID: tradeEID,
					Order:      orderEID,
					Side:       domain.OrderSideBuy,
					Quantity:   "0.5",
					Price:      "49995",
					Commission: &domain.Commission{Amount: "-0.12", Currency: "USD"},
				},
			},
		},
	}

	res := callGetOrder(t, src, orderEID.String())
	requireNotToolError(t, res.IsError)
	out := res.StructuredContent
	if len(out.Order.CommissionSubtotals) != 1 {
		t.Fatalf("want 1 order commission subtotal, got %+v", out.Order.CommissionSubtotals)
	}
	if sub := out.Order.CommissionSubtotals[0]; sub.Amount != "-0.12" || sub.Currency != "USD" {
		t.Fatalf("order commission subtotal = %+v, want -0.12/USD", sub)
	}
	if len(out.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(out.Trades))
	}
	if c := out.Trades[0].Commission; c == nil || c.Amount != "-0.12" || c.Currency != "USD" {
		t.Fatalf("trade commission = %+v, want -0.12/USD", out.Trades[0].Commission)
	}
}

// TestGetOrderUnsigned: an order with no approval read-back yields a nil approval
// (the order is unsigned), not a zero-value envelope.
func TestGetOrderUnsigned(t *testing.T) {
	orderEID := mustExternalID(t, "b3JkZXItZXh0ZXJuYWwtMg")
	src := &fakeSource{
		orderDetail: domain.OrderDetail{
			Order: domain.Order{
				ExternalID: orderEID,
				Account:    "acc-1",
				Status:     domain.OrderStatusSubmitted,
			},
		},
	}
	res := callGetOrder(t, src, orderEID.String())
	requireNotToolError(t, res.IsError)
	if res.StructuredContent.Approval != nil {
		t.Errorf("want nil approval for unsigned order, got %+v", res.StructuredContent.Approval)
	}
}

// TestGetOrderMissingID: get_order rejects an empty external id before reaching
// the source.
func TestGetOrderMissingID(t *testing.T) {
	res := callGetOrder(t, &fakeSource{}, "   ")
	requireToolError(t, res.IsError)
	if got := textContent(res.Content); got != "id is required" {
		t.Errorf("unexpected error text: %q", got)
	}
}

// TestGetOrderNotFound: an unknown external id maps onto a not-found tool error
// naming the handle, distinct from the generic failure message.
func TestGetOrderNotFound(t *testing.T) {
	src := &fakeSource{getOrderErr: fmt.Errorf("order: %w", domain.ErrNotFound)}
	res := callGetOrder(t, src, "b3JkZXItZXh0ZXJuYWwteA")
	requireToolError(t, res.IsError)
	txt := textContent(res.Content)
	if !strings.Contains(txt, "not found") {
		t.Errorf("expected not-found message, got %q", txt)
	}
}

// TestGetOrderSourceFailure: a non-not-found source error surfaces the generic
// failure message.
func TestGetOrderSourceFailure(t *testing.T) {
	src := &fakeSource{getOrderErr: fmt.Errorf("store down")}
	res := callGetOrder(t, src, "b3JkZXItZXh0ZXJuYWwteQ")
	requireToolError(t, res.IsError)
	if got := textContent(res.Content); got != "get order failed" {
		t.Errorf("unexpected error text: %q", got)
	}
}

// -- check_order --

func TestCheckOrderPass(t *testing.T) {
	t.Parallel()
	src := &fakeSource{checkResult: domain.CheckResult{
		Passed: true, WouldLockPrices: []string{"100"},
	}}
	res := callCheckOrder(t, src, checkOrderInput{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: "buy", AmountKind: "quantity", AmountValue: "1", Price: "100",
	})
	requireNotToolError(t, res.IsError)
	if !res.StructuredContent.Passed {
		t.Fatalf("want passed:true")
	}
	if len(res.StructuredContent.WouldDisplayPrices) != 1 {
		t.Fatalf("want 1 display price, got %d", len(res.StructuredContent.WouldDisplayPrices))
	}
	if len(src.checkProbes) != 1 || src.checkProbes[0].Side != domain.OrderSideBuy {
		t.Fatalf("probe not forwarded with mapped fields: %+v", src.checkProbes)
	}
	if got := textContent(res.Content); got != "check acc-1: pass" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestCheckOrderReject(t *testing.T) {
	t.Parallel()
	src := &fakeSource{checkResult: domain.CheckResult{
		Passed: false,
		Rejects: []domain.OrderReject{
			{Code: "insufficient_funds", Scope: "account", Policy: "spot_funds"},
		},
		WouldBlock: &domain.ExecutionAccountBlock{Account: "acc-1", Code: "account_blocked"},
	}}
	res := callCheckOrder(t, src, checkOrderInput{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: "buy", AmountKind: "quantity", AmountValue: "1", Price: "100",
	})
	requireNotToolError(t, res.IsError)
	if res.StructuredContent.Passed {
		t.Fatalf("want passed:false")
	}
	if len(res.StructuredContent.Rejects) != 1 ||
		res.StructuredContent.Rejects[0].Code != "insufficient_funds" {
		t.Fatalf("reject not mapped: %+v", res.StructuredContent.Rejects)
	}
	if res.StructuredContent.WouldBlock == nil ||
		res.StructuredContent.WouldBlock.Account != "acc-1" {
		t.Fatalf("would-block not mapped: %+v", res.StructuredContent.WouldBlock)
	}
	if got := textContent(res.Content); got != "check acc-1: reject - 1 reason(s)" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestCheckOrderMissingAccount(t *testing.T) {
	t.Parallel()
	src := &fakeSource{}
	res := callCheckOrder(t, src, checkOrderInput{Account: "  "})
	requireToolError(t, res.IsError)
	if len(src.checkProbes) != 0 {
		t.Fatalf("missing account must not reach the source")
	}
}

func TestCheckOrderSourceFailure(t *testing.T) {
	t.Parallel()
	src := &fakeSource{checkErr: fmt.Errorf("boom")}
	res := callCheckOrder(t, src, checkOrderInput{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: "buy", AmountKind: "quantity", AmountValue: "1",
	})
	requireToolError(t, res.IsError)
}

// -- set_market_data_instrument --

func TestSetMarketDataInstrumentGatedAndDelegates(t *testing.T) {
	t.Parallel()
	disabled := &fakeSource{
		disabledCommands: map[string]bool{setMarketDataInstrumentToolName: true},
	}
	h := setMarketDataInstrumentHandler(disabled)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[setMarketDataInstrumentInput]{
			Arguments: setMarketDataInstrumentInput{
				InstanceExternalID: "mock-1", ExternalSymbol: "AAPL", Enabled: true,
			},
		})
	if err != nil {
		t.Fatalf("handler protocol error: %v", err)
	}
	requireNotToolError(t, res.IsError)
	if got := textContent(res.Content); got == "" {
		t.Fatalf("disabled command should return a notice")
	}
	if len(disabled.setMDCalls) != 0 {
		t.Fatalf("disabled command must not mutate: %+v", disabled.setMDCalls)
	}

	enabled := &fakeSource{}
	h = setMarketDataInstrumentHandler(enabled)
	res, err = h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[setMarketDataInstrumentInput]{
			Arguments: setMarketDataInstrumentInput{
				InstanceExternalID: "mock-1", ExternalSymbol: "AAPL", Enabled: true,
			},
		})
	if err != nil {
		t.Fatalf("handler protocol error: %v", err)
	}
	requireNotToolError(t, res.IsError)
	if len(enabled.setMDCalls) != 1 || enabled.setMDCalls[0].instanceID != "mock-1" ||
		enabled.setMDCalls[0].externalSymbol != "AAPL" || !enabled.setMDCalls[0].enabled {
		t.Fatalf("toggle not delegated: %+v", enabled.setMDCalls)
	}
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
