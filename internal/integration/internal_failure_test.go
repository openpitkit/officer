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
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/internal/jobs"
	"go.openpit.dev/officer/mcptools"
)

// The tests below pin the public answer to a failure of Officer's own: its
// stored data is corrupt, or a stored reference no longer resolves. The
// documented contract answers it as an internal failure - HTTP 500 with code
// internal, and a tool error with code internal - while a request that is
// itself wrong still answers validation (422). The stored data is created
// through the normal API and then corrupted with raw SQL in the database file
// of the node, which is the failure being answered.

const (
	corruptTestPolicyConnection = "test-policy-connection"
	corruptVenueConnection      = "venue-policy-connection"
	corruptAlertID              = "event"
)

// corruptDB runs query against the database file at path and fails the test
// unless it changed exactly one row.
func corruptDB(t *testing.T, path, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	}()
	result, err := db.ExecContext(t.Context(), query, args...)
	if err != nil {
		t.Fatalf("corrupt %q: %v", query, err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("corrupt %q changed %d rows (%v), want 1", query, n, err)
	}
}

// corruptStoredCredentials replaces the credentials of the row id of table
// with plaintext sealed under the master key of the test node, as the store
// seals what it writes.
func corruptStoredCredentials(t *testing.T, path, table, id, plaintext string) {
	t.Helper()
	key, err := secret.ParseMasterKey("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	aad, err := secret.BuildAAD(string(domain.DefaultRealm), table, "credentials", id)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secret.Seal(key, aad, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	corruptDB(t, path, `UPDATE `+table+` SET credentials = ? WHERE external_id = ?`,
		sealed, domain.ExternalID(id).Bytes())
}

// restAnswer is the status and the error code of one REST answer.
type restAnswer struct {
	status int
	code   string
	body   string
}

// restRouter serves the routes of the control plane and, when alertService is
// set, the alert routes, as the application mounts them.
func restRouter(t *testing.T, svc *backend.Service, alertService *alerts.Service) http.Handler {
	t.Helper()
	registry := httpapi.NewRouteRegistry(svc, nil, func(context.Context) (time.Time, int, bool, error) {
		return time.Time{}, 0, false, nil
	})
	routes := registry.Routes()
	if alertService != nil {
		routes = append(routes, httpapi.AlertRoutes(alertService)...)
	}
	router := chi.NewRouter()
	for _, route := range routes {
		router.Method(route.Method, route.Pattern, route.Handler)
	}
	return router
}

func restCall(
	t *testing.T, router http.Handler, caller domain.Caller, method, path, body string,
) restAnswer {
	t.Helper()
	req := httptest.NewRequestWithContext(
		auth.ContextWithCaller(t.Context(), caller), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var wire struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	answer := restAnswer{status: rec.Code, body: rec.Body.String()}
	if json.Unmarshal(rec.Body.Bytes(), &wire) == nil {
		answer.code = wire.Error.Code
		if len(wire.Errors) > 0 {
			answer.code = wire.Errors[0].Code
		}
	}
	return answer
}

func requireInternalREST(t *testing.T, what string, got restAnswer) {
	t.Helper()
	if got.status != http.StatusInternalServerError || got.code != "internal" {
		t.Errorf("%s = %d %q, want 500 internal: %s", what, got.status, got.code, got.body)
	}
}

func requireValidationREST(t *testing.T, what string, got restAnswer) {
	t.Helper()
	if got.status != http.StatusUnprocessableEntity || got.code != "validation" {
		t.Errorf("%s = %d %q, want 422 validation: %s", what, got.status, got.code, got.body)
	}
}

// toolAnswer is the outcome of one MCP tool call.
type toolAnswer struct {
	isError bool
	text    string
	// code is the error code the tool reports: the error body in its structured
	// content when it has one, otherwise the code its text starts with.
	code string
}

func toolCall(
	t *testing.T, session *sdkmcp.ClientSession, name string, in map[string]any,
) toolAnswer {
	t.Helper()
	res, wire := callSessionTool(t, session, name, in)
	answer := toolAnswer{isError: res.IsError, text: textContent(res.Content)}
	if wire != nil {
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
			Errors []struct {
				Code string `json:"code"`
			} `json:"errors"`
		}
		if json.Unmarshal(wire, &body) == nil {
			answer.code = body.Error.Code
			if len(body.Errors) > 0 {
				answer.code = body.Errors[0].Code
			}
		}
	}
	if answer.code == "" {
		answer.code, _, _ = strings.Cut(answer.text, ":")
	}
	return answer
}

func requireInternalTool(t *testing.T, name string, got toolAnswer) {
	t.Helper()
	if !got.isError || got.code != "internal" {
		t.Errorf("%s = error %t code %q %q, want a tool error with code internal",
			name, got.isError, got.code, got.text)
	}
}

func requireValidationTool(t *testing.T, name string, got toolAnswer) {
	t.Helper()
	if !got.isError || got.code != "validation" {
		t.Errorf("%s = error %t code %q %q, want a tool error with code validation",
			name, got.isError, got.code, got.text)
	}
}

// controlPlaneSession opens an MCP session over the Officer tools of svc.
func controlPlaneSession(t *testing.T, svc *backend.Service) *sdkmcp.ClientSession {
	t.Helper()
	session, _ := officerSession(t, testCtx(t), enabledControlPlaneSource{frameworkmcp.SourceFor(svc)})
	return session
}

var (
	// personalREST and personalMCP are the trusted per-user identities the alert
	// routes and tools require.
	personalREST = domain.Caller{Source: domain.SourceAPI, Principal: "alice"}
	personalMCP  = domain.Caller{Source: domain.SourceMCP, Principal: "alice"}
)

// corruptAlertFixture stores one open alert through the alert service and
// returns the database file, the REST router and an MCP session of the same
// service.
func corruptAlertFixture(t *testing.T) (path string, router http.Handler, session *sdkmcp.ClientSession) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "officer.db")
	target, realm := newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
	valid := func(raw json.RawMessage) error {
		if string(raw) != "{}" {
			return domain.ErrInvalid
		}
		return nil
	}
	service, err := alerts.New(realm.(alerts.Store), []alerts.Type{{
		Name: "test", Validate: valid,
		Actions: map[string]alerts.Action{"accept": {
			Validate: valid,
			Resolve: func(context.Context, alerts.Event, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"accepted":true}`), nil
			},
		}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Publish(systemCtx(), alerts.Event{
		ID: corruptAlertID, Type: "test", At: time.Now().UTC(),
		Source: domain.SourceSystem, Payload: json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("publish alert: %v", err)
	}
	svc := newOfficerService(t, target, nil, nil)
	reg := officerToolRegistry()
	mcptools.RegisterAlertTools(reg, service)
	session, _ = officerSessionOver(t, testCtx(t), reg,
		enabledControlPlaneSource{frameworkmcp.SourceFor(svc)}, personalMCP)
	return path, restRouter(t, svc, service), session
}

// corruptAlertRows are the ways a stored alert row stops being one the alert
// service can read back.
var corruptAlertRows = []struct{ name, query string }{
	{"unknown type", `UPDATE actionable_alert SET type = 'retired'`},
	{"payload the type refuses", `UPDATE actionable_alert SET payload = '{"unexpected":true}'`},
	{"payload that is not JSON", `UPDATE actionable_alert SET payload = '{'`},
}

// corruptTestPolicyRulesSQL stores well-formed JSON whose rule the policy
// validation rejects (an empty mask value), the shape a client would get a 422
// for if it sent it. Found in storage, it is Officer's own data gone wrong.
const corruptTestPolicyRulesSQL = `UPDATE trading_test_policy SET rules = '[{"mask":{"baseAsset":"","quoteAsset":"*"},"mode":"execute","priceVariant":"normal","fills":1}]'`

// requireInternalError fails unless err is a host wiring or storage failure:
// it answers 500 `internal`, never the 422 `validation` of bad client input.
// A service call made without a caller in its context is such a failure.
func requireInternalError(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error, want an internal failure", what)
	}
	if public := httpx.DescribeErr(err); public.Status != http.StatusInternalServerError || public.Code != "internal" {
		t.Fatalf("%s = %d %q (%v), want 500 internal", what, public.Status, public.Code, err)
	}
}

func TestRESTCorruptStoredAlertAnswersInternal(t *testing.T) {
	t.Parallel()
	for _, tc := range corruptAlertRows {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path, router, _ := corruptAlertFixture(t)
			// The request itself is wrong: still validation.
			requireValidationREST(t, "GET /alerts?principal=alice",
				restCall(t, router, personalREST, http.MethodGet, "/alerts?principal=alice", ""))
			requireValidationREST(t, "PUT /alerts/event/read without read",
				restCall(t, router, personalREST, http.MethodPut, "/alerts/"+corruptAlertID+"/read", `{}`))

			corruptDB(t, path, tc.query)
			requireInternalREST(t, "GET /alerts",
				restCall(t, router, personalREST, http.MethodGet, "/alerts", ""))
			requireInternalREST(t, "GET /alerts/event",
				restCall(t, router, personalREST, http.MethodGet, "/alerts/"+corruptAlertID, ""))
			requireInternalREST(t, "POST /alerts/event/actions/accept",
				restCall(t, router, personalREST, http.MethodPost,
					"/alerts/"+corruptAlertID+"/actions/accept", `{"input":{}}`))
		})
	}
}

func TestMCPCorruptStoredAlertAnswersInternal(t *testing.T) {
	t.Parallel()
	for _, tc := range corruptAlertRows {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path, _, session := corruptAlertFixture(t)
			requireValidationTool(t, "set_alert_read without read",
				toolCall(t, session, "set_alert_read", map[string]any{"id": corruptAlertID}))

			corruptDB(t, path, tc.query)
			requireInternalTool(t, "list_alerts", toolCall(t, session, "list_alerts", map[string]any{}))
			requireInternalTool(t, "get_alert",
				toolCall(t, session, "get_alert", map[string]any{"id": corruptAlertID}))
			requireInternalTool(t, "resolve_alert", toolCall(t, session, "resolve_alert",
				map[string]any{"id": corruptAlertID, "action": "accept", "input": map[string]any{}}))
		})
	}
}

// corruptTradingFixture builds the trading control plane over the database
// file path, with a connection of the provider with an execution policy and a
// connection of a provider without one, both holding credentials.
func corruptTradingFixture(t *testing.T) (path string, svc *backend.Service) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "officer.db")
	svc, _, _ = policyControlPlaneAt(t, path)
	return path, svc
}

const corruptReviewOrder = "review-policy-order"

// corruptReviewFixture stores one drop-copy order that is not awaiting review
// and then gives it an order review row, the state the stored order-review
// invariant forbids: a review row exists exactly for orders awaiting review.
// It returns the database file and the service over it.
func corruptReviewFixture(t *testing.T) (path string, svc *backend.Service) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "officer.db")
	ctx := systemCtx()
	// A settlement the store refuses after the engine committed shuts the node
	// down by design; the answer of the request that caused it is what is pinned.
	target, _ := newLocalNodeAtOnFatal(t, path, engine.NewOpenPitEngineBuildFunc(),
		func(err error) { t.Logf("node shut down: %v", err) })
	svc = newOfficerService(t, target, nil, nil)
	for _, code := range []string{"USD", "AAPL"} {
		if _, err := svc.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.CreateAccount(ctx, domain.Account{Code: "review-desk", Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitDropCopyOrder(ctx, domain.Order{
		ExternalID: corruptReviewOrder, Account: "review-desk", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "150",
	}, domain.MissingAccountCreate); err != nil {
		t.Fatalf("drop-copy submit: %v", err)
	}
	corruptDB(t, path, `INSERT INTO order_review (order_id, mode_id, held)
		 SELECT o.id, (SELECT id FROM attestation_mode ORDER BY id LIMIT 1), 0
		 FROM order_record o WHERE o.external_id = ?`,
		domain.ExternalID(corruptReviewOrder).Bytes())
	return path, svc
}

// corruptResetScheduleFixture stores the realm-wide P&L reset schedule through
// the REST route, as an operator does, over the real reset runtime, then turns
// the stored definition into a schedule kind the reset task does not run on.
// It returns the router and an MCP session over the reset tools.
func corruptResetScheduleFixture(t *testing.T) (router http.Handler, session *sdkmcp.ClientSession, corrupt func()) {
	t.Helper()
	ctx := systemCtx()
	path := filepath.Join(t.TempDir(), "officer.db")
	target, _, st := newLocalNodeStoreAt(t, path, engine.NewOpenPitEngineBuildFunc(),
		func(err error) { t.Logf("node shut down: %v", err) })
	host, ok := target.(node.PnlResetHost)
	if !ok {
		t.Fatalf("local node %T is not a P&L reset host", target)
	}
	provider, ok := st.(jobs.StoreProvider)
	if !ok {
		t.Fatalf("store %T has no scheduler store", st)
	}
	jobStore, err := provider.SchedulerStore(ctx, target.Realm())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := jobs.New(target, host, jobStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	svc := newOfficerService(t, target, nil, nil)
	control, err := backend.NewAccountPnlResetControl(svc, host, runtime, backend.EveryAccount{})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.InstallPnlResetScheduler(runtime); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	routes := &httpx.RouteRegistry{}
	httpapi.RegisterAccountPnlResetRoutes(routes, svc, control)
	chiRouter := chi.NewRouter()
	for _, route := range routes.Routes() {
		chiRouter.Method(route.Method, route.Pattern, route.Handler)
	}
	operator := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	saved := restCall(t, chiRouter, operator, http.MethodPut, "/account-pnl-reset-schedule",
		`{"enabled":true,"time":"09:00","timezone":"UTC","termination":{"mode":"forever"},"revision":0}`)
	if saved.status != http.StatusOK {
		t.Fatalf("PUT schedule = %d: %s", saved.status, saved.body)
	}
	reg := frameworkmcp.NewToolRegistry()
	mcptools.RegisterAccountPnlResetTools(reg, control)
	session, closeSession := officerSessionOver(t, testCtx(t),
		reg, enabledControlPlaneSource{frameworkmcp.SourceFor(svc)}, personalMCP)
	t.Cleanup(closeSession)
	return chiRouter, session, func() {
		corruptDB(t, path, `UPDATE scheduler_job SET spec = json_remove(json_set(spec, '$.schedule.kind', 'once'), '$.schedule.time', '$.schedule.timezone', '$.termination.mode', '$.termination.maxRuns', '$.termination.until')`)
	}
}

func TestRESTCorruptStoredResetScheduleAnswersInternal(t *testing.T) {
	t.Parallel()
	router, _, corrupt := corruptResetScheduleFixture(t)
	operator := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	if got := restCall(t, router, operator, http.MethodGet, "/account-pnl-reset-schedule", ""); got.status != http.StatusOK {
		t.Fatalf("GET schedule before corruption = %d: %s", got.status, got.body)
	}
	missingRevision := `{"enabled":true,"time":"09:00","timezone":"UTC","termination":{"mode":"forever"}}`
	requireValidationREST(t, "PUT schedule without a revision",
		restCall(t, router, operator, http.MethodPut, "/account-pnl-reset-schedule", missingRevision))

	corrupt()
	requireInternalREST(t, "GET schedule over a stored definition that is not daily",
		restCall(t, router, operator, http.MethodGet, "/account-pnl-reset-schedule", ""))
}

func TestRESTResetScheduleRetryOutsideFailedStateAnswersValidation(t *testing.T) {
	t.Parallel()
	router, _, _ := corruptResetScheduleFixture(t)
	operator := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	current := restCall(t, router, operator, http.MethodGet, "/account-pnl-reset-schedule", "")
	var state backend.AccountPnlResetScheduleState
	if current.status != http.StatusOK {
		t.Fatalf("GET schedule = %d: %s", current.status, current.body)
	}
	if err := json.Unmarshal([]byte(current.body), &state); err != nil {
		t.Fatal(err)
	}
	if state.Job == nil {
		t.Fatal("saved schedule has no job")
	}
	body, err := json.Marshal(map[string]any{"revision": state.Job.Revision, "action": "retry"})
	if err != nil {
		t.Fatal(err)
	}
	requireValidationREST(t, "retry of a ready reset schedule",
		restCall(t, router, operator, http.MethodPost, "/account-pnl-reset-schedule/recover", string(body)))
}

func TestMCPCorruptStoredResetScheduleAnswersInternal(t *testing.T) {
	t.Parallel()
	_, session, corrupt := corruptResetScheduleFixture(t)
	if got := toolCall(t, session, "get_account_pnl_reset_schedule", map[string]any{}); got.isError {
		t.Fatalf("get_account_pnl_reset_schedule before corruption = %+v", got)
	}

	corrupt()
	requireInternalTool(t, "get_account_pnl_reset_schedule over a stored definition that is not daily",
		toolCall(t, session, "get_account_pnl_reset_schedule", map[string]any{}))
}

const corruptReviewFill = `{"status":"filled","quantity":"1","price":"150","leavesQuantity":"0"}`

func TestRESTCorruptStoredOrderReviewAnswersInternal(t *testing.T) {
	t.Parallel()
	_, svc := corruptReviewFixture(t)
	router := restRouter(t, svc, nil)
	report := "/orders/" + corruptReviewOrder + "/execution-reports"
	operator := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	requireValidationREST(t, "POST execution report with a quantity that is not a number",
		restCall(t, router, operator, http.MethodPost, report,
			`{"status":"filled","quantity":"abc","price":"150","leavesQuantity":"0"}`))
	requireInternalREST(t, "POST execution report over an order whose stored review row disagrees with its status",
		restCall(t, router, operator, http.MethodPost, report, corruptReviewFill))
}

func TestMCPCorruptStoredOrderReviewAnswersInternal(t *testing.T) {
	t.Parallel()
	_, svc := corruptReviewFixture(t)
	if err := svc.SetMcpAccess(systemCtx(), "report_fill", true); err != nil {
		t.Fatal(err)
	}
	session := controlPlaneSession(t, svc)
	requireValidationTool(t, "report_fill with a quantity that is not a number",
		toolCall(t, session, "report_fill", map[string]any{
			"order": corruptReviewOrder, "status": "filled", "quantity": "abc", "price": "150", "leavesQuantity": "0",
		}))
	requireInternalTool(t, "report_fill over an order whose stored review row disagrees with its status",
		toolCall(t, session, "report_fill", map[string]any{
			"order": corruptReviewOrder, "status": "filled", "quantity": "1", "price": "150", "leavesQuantity": "0",
		}))
}

func TestRESTCorruptStoredTradingCredentialsAnswerInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptTradingFixture(t)
	router := restRouter(t, svc, nil)
	settings := "/trading/connections/" + corruptVenueConnection + "/settings"
	requireValidationREST(t, "PUT settings with credentials that are not an object",
		restCall(t, router, personalREST, http.MethodPut, settings, `{"credentials":"text"}`))

	corruptStoredCredentials(t, path, "trading_connection", corruptVenueConnection, `[1]`)
	requireInternalREST(t, "PUT settings over stored credentials that are not an object",
		restCall(t, router, personalREST, http.MethodPut, settings, `{"label":"Renamed"}`))
	requireValidationREST(t, "PUT settings with credentials that are not an object, stored data corrupt",
		restCall(t, router, personalREST, http.MethodPut, settings, `{"credentials":"text"}`))
}

func TestMCPCorruptStoredTradingCredentialsAnswerInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptTradingFixture(t)
	session := controlPlaneSession(t, svc)
	// The tool takes its credentials as a typed object and trims its label, so
	// no request of this tool is a validation failure of the settings: there is
	// no input-side counterpart to pin here.
	corruptStoredCredentials(t, path, "trading_connection", corruptVenueConnection, `[1]`)
	requireInternalTool(t, "update_trading_connection over stored credentials that are not an object",
		toolCall(t, session, "update_trading_connection",
			map[string]any{"connection": corruptVenueConnection, "label": "Renamed"}))
}

func TestRESTCorruptStoredTradingProviderAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptTradingFixture(t)
	router := restRouter(t, svc, nil)
	access := "/trading/connections/" + corruptVenueConnection + "/access"
	requireValidationREST(t, "PUT access naming no account",
		restCall(t, router, personalREST, http.MethodPut, access, `{}`))

	// The stored connection names a provider the runtime does not register.
	corruptDB(t, path, `UPDATE trading_connection SET provider = 'retired' WHERE external_id = ?`,
		domain.ExternalID(corruptVenueConnection).Bytes())
	requireInternalREST(t, "PUT access of a connection with an unregistered provider",
		restCall(t, router, personalREST, http.MethodPut, access, `{"account":"other"}`))
	requireValidationREST(t, "PUT access naming no account, stored provider unregistered",
		restCall(t, router, personalREST, http.MethodPut, access, `{}`))
}

func TestMCPCorruptStoredTradingProviderAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptTradingFixture(t)
	session := controlPlaneSession(t, svc)
	both := map[string]any{"connection": corruptVenueConnection, "account": "other", "accountGroup": "desks"}
	requireValidationTool(t, "grant_trading_access naming an account and a group",
		toolCall(t, session, "grant_trading_access", both))

	corruptDB(t, path, `UPDATE trading_connection SET provider = 'retired' WHERE external_id = ?`,
		domain.ExternalID(corruptVenueConnection).Bytes())
	requireInternalTool(t, "grant_trading_access of a connection with an unregistered provider",
		toolCall(t, session, "grant_trading_access",
			map[string]any{"connection": corruptVenueConnection, "account": "other"}))
	requireValidationTool(t, "grant_trading_access naming an account and a group, stored provider unregistered",
		toolCall(t, session, "grant_trading_access", both))
}

func TestRESTCorruptStoredTestPolicyAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptTradingFixture(t)
	router := restRouter(t, svc, nil)
	policy := "/trading/connections/" + corruptTestPolicyConnection + "/test-policy"
	requireValidationREST(t, "PUT test-policy carrying a revision",
		restCall(t, router, personalREST, http.MethodPut, policy, `{"revision":"7","rules":[]}`))
	requireValidationREST(t, "POST backup export of an unknown section",
		restCall(t, router, personalREST, http.MethodPost, "/backup/export",
			`{"scope":{"sections":["no_such_section"]}}`))

	corruptDB(t, path, corruptTestPolicyRulesSQL)
	requireInternalREST(t, "GET test-policy over corrupt stored rules",
		restCall(t, router, personalREST, http.MethodGet, policy, ""))
	// Backup export is a REST-only surface: the MCP tool set has no export tool.
	requireInternalREST(t, "POST backup export over corrupt stored rules",
		restCall(t, router, personalREST, http.MethodPost, "/backup/export", `{"scope":{"all":true}}`))
}

func TestMCPCorruptStoredTestPolicyAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptTradingFixture(t)
	session := controlPlaneSession(t, svc)
	requireValidationTool(t, "set_test_execution_policy with an empty mask",
		toolCall(t, session, "set_test_execution_policy", map[string]any{
			"connection": corruptTestPolicyConnection,
			"policy": map[string]any{"rules": []any{map[string]any{
				"mask": map[string]any{"baseAsset": "", "quoteAsset": "*"},
				"mode": "execute", "priceVariant": "normal", "fills": 1,
			}}},
		}))

	corruptDB(t, path, corruptTestPolicyRulesSQL)
	requireInternalTool(t, "get_test_execution_policy over corrupt stored rules",
		toolCall(t, session, "get_test_execution_policy",
			map[string]any{"connection": corruptTestPolicyConnection}))
}

// TestRESTCorruptStoredMarketDataCredentialsAnswerInternal covers REST only:
// the MCP tool set has no tool that reads or changes market-data credentials.
// The instance list does not read credentials, so only the settings update
// reaches the corrupt data.
func TestRESTCorruptStoredMarketDataCredentialsAnswerInternal(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "officer.db")
	target, _ := newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
	md := &fakeMarketDataRuntime{}
	md.setRegistry(firstPartyMarketDataRegistry(t))
	svc := newOfficerService(t, target, md, nil)
	created, err := svc.CreateMarketDataInstance(systemCtx(), domain.MarketDataInstance{
		Provider:    domain.MarketDataProviderAlpaca,
		Credentials: `{"apiKey":"key-value","apiSecret":"secret-value"}`,
	})
	if err != nil {
		t.Fatalf("create market-data instance: %v", err)
	}
	router := restRouter(t, svc, nil)
	settings := "/market-data/instances/" + created.ExternalID.String() + "/settings"
	requireValidationREST(t, "PUT settings with credentials that are not JSON",
		restCall(t, router, personalREST, http.MethodPut, settings, `{"credentials":"not json"}`))

	corruptStoredCredentials(t, path, "market_data_instance", created.ExternalID.String(), `[1]`)
	requireInternalREST(t, "PUT settings over stored credentials that are not an object",
		restCall(t, router, personalREST, http.MethodPut, settings, `{"credentials":"{\"apiKey\":\"new-key\"}"}`))
}

// corruptEngineIdentityFixture builds the control plane over a local node with
// the account desk and the assets USD and AAPL, and returns the database file.
func corruptEngineIdentityFixture(t *testing.T) (path string, svc *backend.Service) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "officer.db")
	target, _ := newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
	svc = newOfficerService(t, target, nil, nil)
	for _, code := range []string{"USD", "AAPL"} {
		if _, err := svc.CreateAsset(systemCtx(), domain.Asset{Code: code}); err != nil {
			t.Fatalf("create asset %s: %v", code, err)
		}
	}
	if _, err := svc.CreateAccount(systemCtx(), domain.Account{Code: "desk"}); err != nil {
		t.Fatalf("create account desk: %v", err)
	}
	return path, svc
}

// corruptAssetEngineIdentity renumbers the stored asset USD, whose row number
// is the identity the engine knows it by, so the stored identity no longer
// matches the one the live engine dictionary resolves.
func corruptAssetEngineIdentity(t *testing.T, path string) {
	t.Helper()
	corruptDB(t, path, `UPDATE asset SET id = id + 1000 WHERE code = 'USD'`)
}

func TestRESTCorruptStoredEngineIdentityAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptEngineIdentityFixture(t)
	router := restRouter(t, svc, nil)
	currency := "/accounts/desk/currency"
	requireValidationREST(t, "PUT account currency of a malformed asset code",
		restCall(t, router, personalREST, http.MethodPut, currency, `{"currency":"bad code"}`))

	corruptAssetEngineIdentity(t, path)
	requireInternalREST(t, "PUT account currency over a stored asset identity the engine does not know",
		restCall(t, router, personalREST, http.MethodPut, currency, `{"currency":"USD"}`))
	requireValidationREST(t, "PUT account currency of a malformed asset code, stored identity corrupt",
		restCall(t, router, personalREST, http.MethodPut, currency, `{"currency":"bad code"}`))
}

func TestMCPCorruptStoredEngineIdentityAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptEngineIdentityFixture(t)
	session := controlPlaneSession(t, svc)
	requireValidationTool(t, "set_account_currency of a malformed asset code",
		toolCall(t, session, "set_account_currency", map[string]any{"account": "desk", "currency": "bad code"}))

	corruptAssetEngineIdentity(t, path)
	requireInternalTool(t, "set_account_currency over a stored asset identity the engine does not know",
		toolCall(t, session, "set_account_currency", map[string]any{"account": "desk", "currency": "USD"}))
	requireValidationTool(t, "set_account_currency of a malformed asset code, stored identity corrupt",
		toolCall(t, session, "set_account_currency", map[string]any{"account": "desk", "currency": "bad code"}))
}

func TestRESTMissingLiveAssetAliasAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptEngineIdentityFixture(t)
	router := restRouter(t, svc, nil)
	corruptDB(t, path, `UPDATE asset SET code = 'EUR' WHERE code = 'USD'`)
	requireInternalREST(t, "set currency to a stored asset missing from the live dictionary",
		restCall(t, router, personalREST, http.MethodPut, "/accounts/desk/currency", `{"currency":"EUR"}`))
}

func TestMCPMissingLiveAssetAliasAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptEngineIdentityFixture(t)
	session := controlPlaneSession(t, svc)
	corruptDB(t, path, `UPDATE asset SET code = 'EUR' WHERE code = 'USD'`)
	requireInternalTool(t, "set currency to a stored asset missing from the live dictionary",
		toolCall(t, session, "set_account_currency", map[string]any{"account": "desk", "currency": "EUR"}))
}

func TestRESTMissingLiveGroupAliasAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptEngineIdentityFixture(t)
	router := restRouter(t, svc, nil)
	created := restCall(t, router, personalREST, http.MethodPost, "/groups", `{"code":"risk"}`)
	if created.status != http.StatusCreated {
		t.Fatalf("create group = %d: %s", created.status, created.body)
	}
	corruptDB(t, path, `UPDATE account_group SET code = 'renamed' WHERE code = 'risk'`)
	requireInternalREST(t, "block a stored group missing from the live dictionary",
		restCall(t, router, personalREST, http.MethodPost, "/groups/renamed/block", `{"reason":"risk"}`))
}

func corruptOrderSizeFixture(t *testing.T) (path string, svc *backend.Service) {
	t.Helper()
	path, svc = corruptEngineIdentityFixture(t)
	for _, limit := range []domain.LimitOrderSize{
		{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "100"},
		{Mode: domain.RuleModeReject, Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "10"},
	} {
		if _, err := svc.PutOrderSizeLimit(systemCtx(), limit, domain.LimitWriteAdd, domain.MissingAccountReject); err != nil {
			t.Fatalf("create order-size barrier: %v", err)
		}
	}
	return path, svc
}

func TestRESTCorruptStoredOrderSizeSetAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptOrderSizeFixture(t)
	router := restRouter(t, svc, nil)
	const endpoint = "/limits/order-size"
	const invalid = `{"scope":"broker","maxQuantity":"broken"}`
	requireValidationREST(t, "invalid requested order-size cap",
		restCall(t, router, personalREST, http.MethodPut, endpoint, invalid))
	corruptDB(t, path, `UPDATE limit_order_size SET max_quantity = 'broken' WHERE scope = 'underlying_asset'`)
	requireInternalREST(t, "valid broker cap update over a corrupt neighboring stored barrier",
		restCall(t, router, personalREST, http.MethodPut, endpoint, `{"scope":"broker","maxQuantity":"50"}`))
	requireValidationREST(t, "invalid requested cap while stored data is corrupt",
		restCall(t, router, personalREST, http.MethodPut, endpoint, invalid))
}

func TestMCPCorruptStoredOrderSizeSetAnswersInternal(t *testing.T) {
	t.Parallel()
	path, svc := corruptOrderSizeFixture(t)
	session := controlPlaneSession(t, svc)
	invalid := map[string]any{"policy": "order_size_limit", "scope": "broker", "maxQuantity": "broken"}
	requireValidationTool(t, "invalid requested order-size cap", toolCall(t, session, "update_limit", invalid))
	corruptDB(t, path, `UPDATE limit_order_size SET max_quantity = 'broken' WHERE scope = 'underlying_asset'`)
	requireInternalTool(t, "valid broker cap update over a corrupt neighboring stored barrier",
		toolCall(t, session, "update_limit", map[string]any{"policy": "order_size_limit", "scope": "broker", "maxQuantity": "50"}))
	requireValidationTool(t, "invalid requested cap while stored data is corrupt", toolCall(t, session, "update_limit", invalid))
}
