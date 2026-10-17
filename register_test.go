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

package officer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openpit "go.openpit.dev/openpit"
	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/pretrade/policies"
	"go.openpit.dev/openpit/reject"

	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/internal/accountmode"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/mcptools"
)

type venueReportEngine struct {
	*fakeEngine
	async *asyncengine.AsyncEngine
}

func (e *venueReportEngine) AsyncEngine() *asyncengine.AsyncEngine {
	return e.async
}

func (*venueReportEngine) AccountID(domain.AccountID) (param.AccountID, error) {
	return param.NewAccountIDFromUint64(1), nil
}

type linkBeforeReportNode struct {
	node.Node
	realm      store.RealmStore
	connection domain.ExternalID
}

func (n *linkBeforeReportNode) ApplyExecutionReport(ctx context.Context, in domain.ExecutionReportInput, caller domain.Caller) (engine.ExecutionReportResult, error) {
	if _, err := n.realm.CreateVenueOrder(ctx, domain.VenueOrder{
		Order: in.Order, Connection: n.connection, ClientOrderID: "client-order-1", Route: "{}",
	}); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	return n.Node.ApplyExecutionReport(ctx, in, caller)
}

func TestManualReportRefusesVenueLinkCreatedAfterServiceEntry(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "officer.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	driver, err := openpit.NewEngineBuilder().AccountSync().Builtin(policies.BuildOrderValidation()).Build()
	if err != nil {
		t.Fatal(err)
	}
	async, err := asyncengine.NewBuilder(driver).WithStopUnderlying(driver.Stop).Dynamic().MaxQueues(0).Build()
	if err != nil {
		driver.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = async.StopGraceful(ctx) })
	n, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, st, func(engine.Snapshot) (engine.Engine, error) {
		return &venueReportEngine{fakeEngine: &fakeEngine{running: true, sink: &fakeSink{}}, async: async}, nil
	}, failOnFatal(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	caller := domain.Caller{Source: domain.SourceAPI, Principal: "operator"}
	for _, code := range []string{"AAPL", "USD"} {
		if _, err := realm.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatal(err)
	}
	order, err := realm.CreateOrder(ctx, domain.Order{
		Account: "acc-1", Source: caller.Source, Principal: caller.Principal,
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
		Status: domain.OrderStatusSubmitted, DropCopy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: "venue", Label: "Paper", Mode: domain.TradingModePaper,
		VenueIdentity: "venue-1", Credentials: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := backend.New(&linkBeforeReportNode{Node: n, realm: realm, connection: connection.ExternalID}, nil, nil, func([]byte, domain.Order) (string, error) {
		return "", errUnexpectedOrderChain
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ApplyExecutionReport(auth.ContextWithCaller(ctx, caller), domain.ExecutionReportInput{
		Order: order.ExternalID, OrderStatus: domain.OrderStatusAccepted,
	})
	if !errors.Is(err, domain.ErrVenueOrderManaged) {
		t.Fatalf("manual report after venue link = %v", err)
	}
	if _, linked, err := realm.GetVenueOrder(ctx, order.ExternalID); err != nil || !linked {
		t.Fatalf("interleaving did not create venue link: %v", err)
	}
	detail, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil || detail.Order.Status != order.Status || len(detail.Events) != 0 || len(detail.Trades) != 0 {
		t.Fatalf("refused manual report changed order: %v", err)
	}
}

func TestRegisterTradingComposition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "officer.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	// Invalid credentials exercise the registered constructor without
	// network I/O.
	_, err = realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "paper", Mode: domain.TradingModePaper, VenueIdentity: "paper-account",
		Credentials: "{}", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	builder := frameworkapp.NewBuilder()
	if err := Register(builder, Config{}); err != nil {
		t.Fatal(err)
	}
	builder.SetStoreFactory(func() (store.Store, error) { return st, nil })
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	var logs bytes.Buffer
	app, err := builder.Build(ctx, slog.New(slog.NewTextHandler(&logs, nil)), failOnFatal(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	status, err := app.Service().GetTrading(
		auth.ContextWithCaller(ctx, auth.SystemCaller()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Providers) != 1 || status.Providers[0].Type != domain.TradingProviderAlpaca ||
		len(status.Connections) != 1 || status.Connections[0].State != "unavailable" {
		t.Fatal("trading runtime was not wired into the control plane before Start")
	}
	cancel()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "alpaca: apiKey is required") {
		t.Fatalf("registered Alpaca constructor was not called: %s", logs.String())
	}
}

func TestRegisterBuildUsesPopulatedMCPCatalog(t *testing.T) {
	ctx := auth.ContextWithCaller(context.Background(), auth.SystemCaller())
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetNodeBuilder(fakeEngineNodeBuilder)

	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	commands, err := app.Service().ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	expected := frameworkmcp.NewToolRegistry()
	mcptools.RegisterTools(expected, nil)
	if len(commands) != len(expected.Descriptors()) {
		t.Fatalf("ListMcpAccess returned %d commands, want %d", len(commands), len(expected.Descriptors()))
	}

	getAccount, ok := findCommand(commands, "get_account_state")
	if !ok {
		t.Fatalf("get_account_state missing from composed catalogue")
	}
	if !getAccount.Command.DefaultEnabled || !getAccount.Enabled {
		t.Fatalf("get_account_state default state = %+v", getAccount)
	}
	if err := app.Service().SetMcpAccess(ctx, "get_account_state", false); err != nil {
		t.Fatalf("SetMcpAccess(get_account_state): %v", err)
	}
}

func TestRegisterPassesMasterKeyToSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	key := testMasterKey(t)

	builder := frameworkapp.NewBuilder()
	if err := Register(builder, Config{SQLitePath: path, MasterKey: &key}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatalf("Build with master key: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	withoutKey := frameworkapp.NewBuilder()
	if err := Register(withoutKey, Config{SQLitePath: path}); err != nil {
		t.Fatalf("Register without key: %v", err)
	}
	withoutKey.SetNodeBuilder(fakeEngineNodeBuilder)
	_, err = withoutKey.Build(ctx, slog.Default(), failOnFatal(t))
	if err == nil {
		t.Fatal("Build without the master key returned nil error for a sealed database")
	}
	if !strings.Contains(err.Error(), "no master key was supplied") {
		t.Fatalf("Build without the master key returned the wrong error: %v", err)
	}
}

// TestRegisterSigningKeyRequiresMasterKey drives, over REST, the real
// composition: without a master key, generating and importing a signing key are
// refused with master_key_required and no private key reaches the database;
// with one, both succeed and every stored private key is sealed.
func TestRegisterSigningKeyRequiresMasterKey(t *testing.T) {
	seed := bytes.Repeat([]byte{0x6b}, ed25519.SeedSize)
	importBody := fmt.Sprintf(
		`{"key":%q,"format":"raw-base64"}`, base64.StdEncoding.EncodeToString(seed),
	)

	clearPath := filepath.Join(t.TempDir(), "officer.db")
	app, handler := serveSeededApp(t, Config{SQLitePath: clearPath}, nil)
	for _, route := range []struct{ path, body string }{
		{"/signing/keys/generate", ""},
		{"/signing/keys/import", importBody},
	} {
		refused := restCall(t, handler, http.MethodPost, route.path, route.body, http.StatusConflict)
		if errObj, _ := refused["error"].(map[string]any); errObj["code"] != "master_key_required" ||
			errObj["message"] != domain.ErrMasterKeyRequired.Error() {
			t.Fatalf("POST %s without a master key = %v, want master_key_required", route.path, refused)
		}
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close without a master key: %v", err)
	}
	if stored := storedSigningPrivateKeys(t, clearPath); len(stored) != 0 {
		t.Fatalf("signing private keys stored without a master key = %d, want 0", len(stored))
	}

	key := testMasterKey(t)
	sealedPath := filepath.Join(t.TempDir(), "officer.db")
	app, handler = serveSeededApp(t, Config{SQLitePath: sealedPath, MasterKey: &key}, nil)
	restCall(t, handler, http.MethodPost, "/signing/keys/generate", "", http.StatusCreated)
	restCall(t, handler, http.MethodPost, "/signing/keys/import", importBody, http.StatusCreated)
	if err := app.Close(); err != nil {
		t.Fatalf("Close with a master key: %v", err)
	}
	stored := storedSigningPrivateKeys(t, sealedPath)
	if len(stored) != 2 {
		t.Fatalf("signing private keys stored with a master key = %d, want 2", len(stored))
	}
	for _, privateKey := range stored {
		if len(privateKey) == ed25519.SeedSize || bytes.Contains(privateKey, seed) {
			t.Fatal("a signing private key is stored in the clear")
		}
	}
}

// storedSigningPrivateKeys reads every non-empty signing_key.private_key of the
// SQLite database at path as stored, past the store and its sealing.
func storedSigningPrivateKeys(t *testing.T, path string) [][]byte {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT private_key FROM signing_key WHERE length(private_key) > 0`)
	if err != nil {
		t.Fatalf("read signing private keys: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var stored [][]byte
	for rows.Next() {
		var privateKey []byte
		if err := rows.Scan(&privateKey); err != nil {
			t.Fatalf("scan signing private key: %v", err)
		}
		stored = append(stored, privateKey)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate signing private keys: %v", err)
	}
	return stored
}

func TestBuildRejectsNodeBuilderWithoutEngine(t *testing.T) {
	ctx := context.Background()
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var opened store.Store
	builder.SetNodeBuilder(func(
		buildCtx context.Context, st store.Store, fatalHook frameworkapp.FatalShutdownHook,
	) (node.Node, engine.Engine, error) {
		opened = st
		built, _, err := fakeEngineNodeBuilder(buildCtx, st, fatalHook)
		return built, nil, err
	})

	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a node hook that returned no engine")
	}
	if !strings.Contains(err.Error(), "no engine") {
		t.Fatalf("Build error = %v, want the missing engine named", err)
	}
	// Build closed the node, and with it the store the node owns.
	if _, err := opened.ForRealm(ctx, domain.DefaultRealm); err == nil ||
		!strings.Contains(err.Error(), "sqlite is closed") {
		t.Fatalf("ForRealm after the rejected Build = %v, want the store closed", err)
	}
}

func TestBuildRejectsMissingAuthorizer(t *testing.T) {
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	builder.SetAuthorizer(nil)

	app, err := builder.Build(context.Background(), slog.Default(), failOnFatal(t))
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a nil authorizer")
	}
	if !strings.Contains(err.Error(), "nil authorizer") {
		t.Fatalf("Build error = %v, want the missing authorizer named", err)
	}
}

func TestBuildRejectsMissingCallerResolver(t *testing.T) {
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	builder.SetCallerResolver(nil)

	app, err := builder.Build(context.Background(), slog.Default(), failOnFatal(t))
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a nil caller resolver")
	}
	if !strings.Contains(err.Error(), "nil caller resolver") {
		t.Fatalf("Build error = %v, want the missing caller resolver named", err)
	}
}

func TestBuildRejectsNilFatalHook(t *testing.T) {
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetStoreFactory(func() (store.Store, error) {
		t.Error("Build opened the store despite a nil fatal shutdown hook")
		return nil, errors.New("store must not be opened")
	})
	builder.SetNodeBuilder(fakeEngineNodeBuilder)

	var nilHook frameworkapp.FatalShutdownHook
	app, err := builder.Build(context.Background(), slog.Default(), nilHook)
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a nil fatal shutdown hook")
	}
	if !strings.Contains(err.Error(), "nil fatal shutdown hook") {
		t.Fatalf("Build error = %v, want the missing fatal shutdown hook named", err)
	}
}

// testMasterKey returns the fixed master key of the compositions that seal
// their database.
func testMasterKey(t *testing.T) secret.MasterKey {
	t.Helper()
	key, err := secret.ParseMasterKey("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	return key
}

// failOnFatal returns the fatal-shutdown hook of a test that expects no
// fail-stop: any call fails the test.
func failOnFatal(t *testing.T) frameworkapp.FatalShutdownHook {
	t.Helper()
	return func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}
}

func TestRESTMutationAuditUsesResolvedPrincipal(t *testing.T) {
	const principal = "alice"
	app, realm := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: principal, Role: "admin"}, nil
	})
	if err := realm.CreatePrincipal(context.Background(), domain.Principal{Code: principal}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	handler, err := app.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/mcp-access/get_account_state",
		strings.NewReader(`{"enabled":false}`),
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mutation status = %d, body = %q", rec.Code, rec.Body.String())
	}
	rows, err := app.Service().ListAudit(
		auth.ContextWithCaller(context.Background(), auth.SystemCaller()), 100,
	)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	row := auditRowForAction(t, rows, domain.AuditActionSetMcpAccess)
	if row.Actor != principal || row.Source != domain.SourceAPI {
		t.Fatalf("audit caller = (%q, %q), want (%q, %q)",
			row.Actor, row.Source, principal, domain.SourceAPI)
	}
}

func TestRecordServiceLifecycleUsesContextCaller(t *testing.T) {
	const principal = "lifecycle-admin"
	app, realm := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	})
	if err := realm.CreatePrincipal(context.Background(), domain.Principal{Code: principal}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	caller := domain.Caller{Source: domain.SourcePanel, Principal: principal, Role: "admin"}
	if err := app.RecordServiceLifecycle(
		auth.ContextWithCaller(context.Background(), caller),
		domain.AuditActionRestartService, "restart service requested",
	); err != nil {
		t.Fatalf("RecordServiceLifecycle: %v", err)
	}
	rows, err := app.Service().ListAudit(
		auth.ContextWithCaller(context.Background(), auth.SystemCaller()), 100,
	)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	row := auditRowForAction(t, rows, domain.AuditActionRestartService)
	if row.Actor != principal || row.Source != domain.SourcePanel {
		t.Fatalf("audit caller = (%q, %q), want (%q, %q)",
			row.Actor, row.Source, principal, domain.SourcePanel)
	}
}

func TestRecordServiceLifecycleRejectsUnresolvedCaller(t *testing.T) {
	ctx := context.Background()
	app, realm := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	})
	before, err := realm.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit before: %v", err)
	}
	err = app.RecordServiceLifecycle(
		ctx, domain.AuditActionRestartService, "restart service requested",
	)
	if err == nil || !strings.Contains(err.Error(), "no caller in context") {
		t.Errorf("RecordServiceLifecycle without a caller = %v, want the missing caller named", err)
	}
	after, err := realm.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("audit rows = %d after the rejected request, want %d: %+v",
			len(after), len(before), after)
	}
}

func TestRecordServiceLifecycleRecordsExplicitSystemCaller(t *testing.T) {
	app, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	})
	if err := app.RecordServiceLifecycle(
		auth.ContextWithCaller(
			context.Background(), domain.Caller{Source: domain.SourceSystem},
		),
		domain.AuditActionRestartService, "restart service requested",
	); err != nil {
		t.Fatalf("RecordServiceLifecycle: %v", err)
	}
	rows, err := app.Service().ListAudit(
		auth.ContextWithCaller(context.Background(), auth.SystemCaller()), 100,
	)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	row := auditRowForAction(t, rows, domain.AuditActionRestartService)
	if row.Actor != "" || row.Source != domain.SourceSystem {
		t.Fatalf("audit caller = (%q, %q), want (%q, %q)",
			row.Actor, row.Source, "", domain.SourceSystem)
	}
}

// TestRegisterSingleAccountMode drives, over REST, the real composition started
// on a database that stores single-account mode for the account "default": a
// client never names the account and any other one is refused, lists show the
// default account alone, the account set cannot change, and a stored switch
// back to multi-account mode waits for a restart.
func TestRegisterSingleAccountMode(t *testing.T) {
	// The composition generates a signing key, which needs a master key.
	key := testMasterKey(t)
	_, handler := serveSeededApp(t, Config{
		SQLitePath: filepath.Join(t.TempDir(), "officer.db"), MasterKey: &key,
	}, map[string]string{
		accountmode.SettingKeyMode: "single:default",
	})
	assertAccountMode(t, handler, http.MethodGet, "",
		`{"mode":"single","account":"default",`+
			`"features":{"accounts":false,"account":"default"},"restartRequired":false}`)

	restCall(t, handler, http.MethodPost, "/signing/keys/generate", "", http.StatusCreated)
	order := `"baseAsset":"AAPL","quoteAsset":"USD","side":"buy",` +
		`"amountKind":"quantity","amountValue":"1","price":"100"`
	submitted := restCall(t, handler, http.MethodPost, "/orders/submit",
		`{`+order+`,"mode":"hold"}`, http.StatusCreated)
	id, _ := submitted["id"].(string)
	detail := restCall(t, handler, http.MethodGet, "/orders/"+url.PathEscape(id), "", http.StatusOK)
	if stored, _ := detail["order"].(map[string]any); stored["account"] != "default" {
		t.Fatalf("submitted order = %v, want it bound to the default account", stored)
	}

	refused := restCall(t, handler, http.MethodPost, "/orders/check",
		`{"account":"other",`+order+`}`, http.StatusUnprocessableEntity)
	problems, _ := refused["errors"].([]any)
	if problem, _ := firstOf(problems).(map[string]any); len(problems) != 1 ||
		problem["pointer"] != "/account" || problem["constraint"] != "single_account_mode" {
		t.Fatalf("check naming another account = %v, want single_account_mode at /account", refused)
	}

	if got := listedAccounts(t, handler); !slices.Equal(got, []string{"default"}) {
		t.Fatalf("listed accounts = %v, want the default account alone", got)
	}
	created := restCall(t, handler, http.MethodPost, "/accounts", `{"code":"desk"}`, http.StatusConflict)
	if errObj, _ := created["error"].(map[string]any); errObj["code"] != "accounts_not_managed" {
		t.Fatalf("account create = %v, want accounts_not_managed", created)
	}

	assertAccountMode(t, handler, http.MethodPut, `{"mode":"multi"}`,
		`{"mode":"multi","account":"",`+
			`"features":{"accounts":false,"account":"default"},"restartRequired":true}`)
	if got := listedAccounts(t, handler); !slices.Equal(got, []string{"default"}) {
		t.Fatalf("listed accounts after the stored switch = %v, want the default account alone", got)
	}
	switched := restCall(t, handler, http.MethodPut, "/account-mode",
		`{"mode":"single","account":"other"}`, http.StatusConflict)
	if errObj, _ := switched["error"].(map[string]any); errObj["code"] != "accounts_not_managed" ||
		!strings.Contains(fmt.Sprint(errObj["message"]), "switch to multi-account mode and restart") {
		t.Fatalf("switch to another default account = %v, want accounts_not_managed with the way out", switched)
	}
}

// TestRegisterWithoutAccountMode starts the real composition on a database
// with no stored account mode: clients see every account, as before the mode
// existed, the mode reads as not chosen, and refused mode choices answer with
// their own codes.
func TestRegisterWithoutAccountMode(t *testing.T) {
	_, handler := serveSeededApp(
		t, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")}, nil,
	)
	assertAccountMode(t, handler, http.MethodGet, "",
		`{"mode":null,"account":"","features":{"accounts":true,"account":""},"restartRequired":false}`)
	got := listedAccounts(t, handler)
	slices.Sort(got)
	if !slices.Equal(got, []string{"default", "other"}) {
		t.Fatalf("listed accounts = %v, want every account", got)
	}
	refused := restCall(t, handler, http.MethodPut, "/account-mode", `{"mode":"solo"}`,
		http.StatusUnprocessableEntity)
	problems, _ := refused["errors"].([]any)
	if problem, _ := firstOf(problems).(map[string]any); len(problems) != 1 ||
		problem["pointer"] != "/mode" || problem["constraint"] != "enum" {
		t.Fatalf("unknown mode = %v, want enum at /mode", refused)
	}
	missing := restCall(t, handler, http.MethodPut, "/account-mode",
		`{"mode":"single","account":"ghost"}`, http.StatusNotFound)
	if errObj, _ := missing["error"].(map[string]any); errObj["code"] != "account_missing" ||
		errObj["account"] != "ghost" {
		t.Fatalf("single with a missing account = %v, want account_missing for ghost", missing)
	}
}

// TestRegisterSingleAccountModeMCPSchemas lists the tools over the served MCP
// endpoint of the real composition started in single-account mode: the MCP
// registrar sees the scope Register installs, so submit_order neither requires
// an account nor offers an account group.
func TestRegisterSingleAccountModeMCPSchemas(t *testing.T) {
	_, handler := serveSeededApp(t, Config{
		SQLitePath: filepath.Join(t.TempDir(), "officer.db"),
	}, map[string]string{
		accountmode.SettingKeyMode: "single:default",
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, sdkmcp.NewStreamableClientTransport(server.URL+"/mcp", nil))
	if err != nil {
		t.Fatalf("connect MCP: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	index := slices.IndexFunc(list.Tools, func(tool *sdkmcp.Tool) bool {
		return tool.Name == "submit_order"
	})
	if index < 0 {
		t.Fatal("submit_order missing from tools/list")
	}
	raw, err := json.Marshal(list.Tools[index].InputSchema)
	if err != nil {
		t.Fatalf("marshal submit_order input schema: %v", err)
	}
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode submit_order input schema: %v", err)
	}
	if _, group := schema.Properties["accountGroup"]; slices.Contains(schema.Required, "account") ||
		group || schema.Properties["account"] == nil {
		t.Fatalf("submit_order input schema = %s, want account optional and no accountGroup", raw)
	}
}

// TestRegisterRouteBaseline pins the v1 surface of the assembled app: the
// httpapi registry, then the account-mode routes, then the route manifest.
func TestRegisterRouteBaseline(t *testing.T) {
	app, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	})
	handler, err := app.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/route-manifest.json", nil))
	var manifest struct {
		Routes []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"routes"`
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("route manifest = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("decode route manifest: %v", err)
	}
	got := make([]string, 0, len(manifest.Routes))
	for _, route := range manifest.Routes {
		got = append(got, route.Method+" "+route.Path)
	}
	var want []string
	for _, route := range httpapi.NewRouteRegistry(app.Service(), nil).Routes() {
		want = append(want, route.Method+" "+route.Pattern)
	}
	want = append(want, "GET /account-mode", "PUT /account-mode", "GET /route-manifest.json")
	if !slices.Equal(got, want) {
		t.Fatalf("assembled route surface mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

// serveSeededApp builds the real composition - Register over cfg, with
// Register's own node over the native engine - on a database seeded, sealed
// with cfg's master key when it carries one, with the accounts "default" and
// "other", the assets AAPL and USD, and settings, and returns the app and its
// served handler.
func serveSeededApp(
	t *testing.T, cfg Config, settings map[string]string,
) (*frameworkapp.App, http.Handler) {
	t.Helper()
	ctx := context.Background()
	var opts []sqlite.Option
	if cfg.MasterKey != nil {
		opts = append(opts, sqlite.WithMasterKey(*cfg.MasterKey))
	}
	st, err := sqlite.New(cfg.SQLitePath, domain.DefaultRealm, opts...)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		t.Fatalf("Migrate: %v", err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		_ = st.Close()
		t.Fatalf("ForRealm: %v", err)
	}
	for _, code := range []domain.AccountID{"default", "other"} {
		if _, err := realm.CreateAccount(ctx, domain.Account{Code: code}); err != nil {
			t.Fatalf("CreateAccount(%s): %v", code, err)
		}
	}
	for _, code := range []string{"AAPL", "USD"} {
		if _, err := realm.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	for key, value := range settings {
		if err := realm.SetSetting(ctx, key, value); err != nil {
			t.Fatalf("SetSetting(%s): %v", key, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	builder := frameworkapp.NewBuilder()
	if err := Register(builder, cfg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetSPAFactory(func() (fs.FS, error) {
		return fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, nil
	})
	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	handler, err := app.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	return app, handler
}

// restCall serves one /api/v1 request through handler and decodes its JSON
// answer, failing the test unless the status is want.
func restCall(
	t *testing.T, handler http.Handler, method, path, body string, want int,
) map[string]any {
	t.Helper()
	rec := serveREST(handler, method, path, body)
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: decode %q: %v", method, path, rec.Body, err)
	}
	return out
}

// assertAccountMode serves one /account-mode request and checks its exact wire
// answer.
func assertAccountMode(t *testing.T, handler http.Handler, method, body, want string) {
	t.Helper()
	rec := serveREST(handler, method, "/account-mode", body)
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != want {
		t.Fatalf("%s /account-mode = %d %s, want 200 %s", method, rec.Code, got, want)
	}
}

func serveREST(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// listedAccounts returns the codes GET /accounts lists.
func listedAccounts(t *testing.T, handler http.Handler) []string {
	t.Helper()
	listed, _ := restCall(t, handler, http.MethodGet, "/accounts", "", http.StatusOK)["accounts"].([]any)
	codes := make([]string, 0, len(listed))
	for _, account := range listed {
		row, _ := account.(map[string]any)
		codes = append(codes, fmt.Sprint(row["code"]))
	}
	return codes
}

// firstOf returns the first element, or nil for none.
func firstOf(items []any) any {
	if len(items) == 0 {
		return nil
	}
	return items[0]
}

func buildTestApp(
	t *testing.T,
	resolver func(*http.Request) (domain.Caller, error),
) (*frameworkapp.App, store.RealmStore) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.New(
		filepath.Join(t.TempDir(), "officer.db"), domain.DefaultRealm,
	)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	builder := frameworkapp.NewBuilder()
	if err := Register(builder, Config{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetStoreFactory(func() (store.Store, error) { return st, nil })
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	builder.SetCallerResolver(resolver)
	builder.SetSPAFactory(func() (fs.FS, error) {
		return fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, nil
	})
	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	return app, realm
}

func auditRowForAction(
	t *testing.T, rows []domain.AuditRow, action domain.AuditAction,
) domain.AuditRow {
	t.Helper()
	for _, row := range rows {
		if row.Action == action {
			return row
		}
	}
	t.Fatalf("audit action %q not found in %+v", action, rows)
	return domain.AuditRow{}
}

func TestBuildStopsEngineReturnedWithoutNode(t *testing.T) {
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	eng := &fakeEngine{running: true, sink: &fakeSink{}}
	builder.SetNodeBuilder(func(
		context.Context, store.Store, frameworkapp.FatalShutdownHook,
	) (node.Node, engine.Engine, error) {
		return nil, eng, nil
	})

	app, err := builder.Build(context.Background(), slog.Default(), failOnFatal(t))
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a node hook that returned no node")
	}
	if !strings.Contains(err.Error(), "no node") {
		t.Fatalf("Build error = %v, want the missing node named", err)
	}
	if eng.running {
		t.Fatal("engine returned with a nil node was not stopped")
	}
	if len(eng.lifecycle) != 2 ||
		eng.lifecycle[0] != "stop" || eng.lifecycle[1] != "close market data" {
		t.Fatalf("engine shutdown calls = %v, want stop then close market data", eng.lifecycle)
	}
}

// fakeEngineNodeBuilder replaces the composition's node hook with the same local
// node over a fake engine, so these tests need no native runtime.
func fakeEngineNodeBuilder(
	ctx context.Context, st store.Store, fatalHook frameworkapp.FatalShutdownHook,
) (node.Node, engine.Engine, error) {
	return node.NewLocalNode(
		ctx,
		domain.DefaultRealm,
		st,
		func(engine.Snapshot) (engine.Engine, error) {
			return &fakeEngine{running: true, sink: &fakeSink{}}, nil
		},
		fatalHook,
	)
}

func findCommand(commands []backend.McpCommand, name string) (backend.McpCommand, bool) {
	for _, cmd := range commands {
		if cmd.Command.Name == name {
			return cmd, true
		}
	}
	return backend.McpCommand{}, false
}

type fakeSink struct{}

func (s *fakeSink) Push(marketdata.QuoteUpdate) error { return nil }

type fakeEngine struct {
	sink      marketdata.Sink
	running   bool
	lifecycle []string
}

var errUnexpectedOrderChain = errors.New("unexpected order chain call")

func (*fakeEngine) AsyncEngine() *asyncengine.AsyncEngine {
	panic(errUnexpectedOrderChain)
}

func (*fakeEngine) AccountID(domain.AccountID) (param.AccountID, error) {
	return param.AccountID{}, errUnexpectedOrderChain
}

func (*fakeEngine) ExecutionReportModel(
	domain.ExecutionReportInput, string,
) (model.ExecutionReport, error) {
	return model.ExecutionReport{}, errUnexpectedOrderChain
}

func (*fakeEngine) SettledExecutionReport(
	domain.ExecutionReportInput,
	param.AccountID,
	pretrade.PostTradeResult,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, errUnexpectedOrderChain
}

func (*fakeEngine) AccountAdjustmentModels(
	[]domain.AdjustmentRequest,
) ([]model.AccountAdjustment, error) {
	return nil, errUnexpectedOrderChain
}

func (*fakeEngine) AppliedAccountAdjustmentBatch(
	domain.AccountID,
	[]domain.AdjustmentRequest,
	accountadjustment.BatchResult,
) ([]engine.AdjustmentResult, *domain.AdjustmentOutcomeRejected, error) {
	return nil, nil, errUnexpectedOrderChain
}

func (*fakeEngine) SpotFundsAccountPnlAssignment(
	string,
	domain.PnlHaltReason,
) (asyncengine.SpotFundsAccountPnlAssignment, error) {
	return asyncengine.SpotFundsAccountPnlAssignment{}, errUnexpectedOrderChain
}

func (*fakeEngine) AppliedSpotFundsAccountPnl(
	domain.AccountID,
	string,
	domain.PnlHaltReason,
	configure.PolicyConfigurationResult,
) ([]domain.AccountBlock, error) {
	return nil, errUnexpectedOrderChain
}

func (*fakeEngine) OrderModel(domain.Order) (model.Order, error) {
	return model.Order{}, errUnexpectedOrderChain
}

func (*fakeEngine) CheckOrderModel(domain.OrderProbe) (model.Order, error) {
	return model.Order{}, errUnexpectedOrderChain
}

func (*fakeEngine) RejectedOrder(
	domain.Order, []reject.Reject,
) (engine.OrderResult, error) {
	panic(errUnexpectedOrderChain)
}

func (*fakeEngine) ReservedOrder(
	domain.Order, asyncengine.OperationResult,
) (engine.OrderResult, error) {
	return engine.OrderResult{}, errUnexpectedOrderChain
}

func (*fakeEngine) AppliedDropCopyOrder(
	domain.Order, asyncengine.DropCopyResult,
) (engine.OrderResult, error) {
	return engine.OrderResult{}, errUnexpectedOrderChain
}

func (*fakeEngine) RejectedImmediate(
	domain.Order, []reject.Reject,
) (engine.ImmediateResult, error) {
	panic(errUnexpectedOrderChain)
}

func (*fakeEngine) PrepareImmediateReservation(
	domain.Order, asyncengine.OperationResult,
) (engine.ImmediatePreparation, error) {
	return engine.ImmediatePreparation{}, errUnexpectedOrderChain
}

func (*fakeEngine) PrepareImmediateDropCopy(
	domain.Order, asyncengine.DropCopyResult,
) (engine.ImmediatePreparation, error) {
	return engine.ImmediatePreparation{}, errUnexpectedOrderChain
}

func (*fakeEngine) SettleImmediate(
	domain.Order, engine.ImmediatePreparation, pretrade.PostTradeResult,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{}, errUnexpectedOrderChain
}

func (*fakeEngine) CheckedOrder(
	domain.OrderProbe, asyncengine.OrderCheckResult,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, errUnexpectedOrderChain
}

func (e *fakeEngine) Version() string      { return "fake" }
func (e *fakeEngine) BuildProfile() string { return "test" }
func (e *fakeEngine) Running() bool        { return e.running }

func (e *fakeEngine) ConfigurePolicy(
	context.Context,
	string,
	engine.LimitSet,
) (engine.PolicyConfigurationResult, error) {
	return engine.PolicyConfigurationResult{}, nil
}

func (*fakeEngine) AddAccountResolverEntry(domain.Account) error { return nil }
func (*fakeEngine) AddAssetResolverEntry(domain.Asset) error     { return nil }
func (*fakeEngine) AddGroupResolverEntry(domain.AccountGroup) error {
	return nil
}
func (*fakeEngine) RenameAccountResolverEntry(domain.AccountID, domain.Account) error {
	return nil
}
func (*fakeEngine) RenameAssetResolverEntry(string, domain.Asset) error { return nil }
func (*fakeEngine) RenameGroupResolverEntry(string, domain.AccountGroup) error {
	return nil
}
func (*fakeEngine) RemoveAssetResolverEntry(domain.Asset) error { return nil }
func (*fakeEngine) RemoveGroupResolverEntry(domain.AccountGroup) error {
	return nil
}
func (*fakeEngine) ResolveAsset(string) (param.Asset, error) {
	return param.Asset{}, errUnexpectedOrderChain
}
func (*fakeEngine) ResolveGroup(string) (param.AccountGroupID, error) {
	return param.AccountGroupID{}, errUnexpectedOrderChain
}

func (e *fakeEngine) MarketDataSink() marketdata.Sink { return e.sink }

func (e *fakeEngine) Stop() {
	e.running = false
	e.lifecycle = append(e.lifecycle, "stop")
}

func (e *fakeEngine) CloseMarketDataService() {
	e.lifecycle = append(e.lifecycle, "close market data")
}
