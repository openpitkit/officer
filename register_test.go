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

package officer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/internal/access"
	"go.openpit.dev/officer/internal/access/accesstest"
	"go.openpit.dev/officer/internal/accountmode"
	"go.openpit.dev/officer/internal/apitest"
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
	path := filepath.Join(t.TempDir(), "officer.db")
	st, err := sqlite.New(path, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	// Invalid credentials exercise the registered constructor without
	// network I/O.
	_, err = realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "paper", Mode: domain.TradingModePaper, VenueIdentity: "paper-account",
		Credentials: "{}", Enabled: true,
	})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	builder := frameworkapp.NewBuilder()
	if err := Register(builder, Config{SQLitePath: path}); err != nil {
		t.Fatal(err)
	}
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
	accesstest.CompleteOpen(t, handler, accountmode.Request{Mode: accountmode.ModeMulti}, false)
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
	accesstest.CompleteOpen(t, handler, accountmode.Request{Mode: accountmode.ModeMulti}, true)
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
	app, realm, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: principal, Role: "admin"}, nil
	}, nil)
	if err := realm.CreatePrincipal(context.Background(), domain.Principal{Code: principal}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	handler, err := app.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	accesstest.CompleteOpen(t, handler, accountmode.Request{Mode: accountmode.ModeMulti}, false)
	req := httptest.NewRequest(
		http.MethodPut,
		"http://127.0.0.1/api/v1/mcp-access/get_account_state",
		strings.NewReader(`{"enabled":false}`),
	)
	req.Header.Set("Content-Type", "application/json")
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
	app, realm, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	}, nil)
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
	app, realm, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	}, nil)
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
	app, _, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	}, nil)
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
	assertAccountMode(t, handler, "/app/api/v1", http.MethodGet, "",
		`{"mode":"single","account":"default",`+
			`"features":{"accounts":false,"account":"default"},"restartRequired":false}`)
	if got := listedAccounts(t, handler, "/app/api/v1"); !slices.Equal(got, []string{"default"}) {
		t.Fatalf("listed accounts in setup = %v, want the default account alone", got)
	}
	accesstest.CompleteOpen(t, handler,
		accountmode.Request{Mode: accountmode.ModeSingle, Account: "default"}, true)

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

	if got := listedAccounts(t, handler, "/api/v1"); !slices.Equal(got, []string{"default"}) {
		t.Fatalf("listed accounts = %v, want the default account alone", got)
	}
	created := restCall(t, handler, http.MethodPost, "/accounts", `{"code":"desk"}`, http.StatusConflict)
	if errObj, _ := created["error"].(map[string]any); errObj["code"] != "accounts_not_managed" {
		t.Fatalf("account create = %v, want accounts_not_managed", created)
	}

	assertAccountMode(t, handler, "/api/v1", http.MethodPut, `{"mode":"multi"}`,
		`{"mode":"multi","account":"",`+
			`"features":{"accounts":false,"account":"default"},"restartRequired":true}`)
	if got := listedAccounts(t, handler, "/api/v1"); !slices.Equal(got, []string{"default"}) {
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
	assertAccountMode(t, handler, "/app/api/v1", http.MethodGet, "",
		`{"mode":null,"account":"","features":{"accounts":true,"account":""},"restartRequired":false}`)
	got := listedAccounts(t, handler, "/app/api/v1")
	slices.Sort(got)
	if !slices.Equal(got, []string{"default", "other"}) {
		t.Fatalf("listed accounts = %v, want every account", got)
	}
	refused := restCall(t, handler, http.MethodPut, "/app/api/v1/account-mode", `{"mode":"solo"}`,
		http.StatusUnprocessableEntity)
	problems, _ := refused["errors"].([]any)
	if problem, _ := firstOf(problems).(map[string]any); len(problems) != 1 ||
		problem["pointer"] != "/mode" || problem["constraint"] != "enum" {
		t.Fatalf("unknown mode = %v, want enum at /mode", refused)
	}
	missing := restCall(t, handler, http.MethodPut, "/app/api/v1/account-mode",
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
	accesstest.CompleteOpen(t, handler,
		accountmode.Request{Mode: accountmode.ModeSingle, Account: "default"}, false)
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

// TestRegisterRouteBaseline pins the route manifest of the assembled serve
// handler: the open access mode; every v1 route - the httpapi registry, the
// account-mode routes, then the lifecycle routes the serve wrapper adds - as api
// on /api/v1 and as panel on /app/api/v1, with the manifest on /api/v1 only; the
// panel-only access routes, public or panel; the agent MCP methods; the public
// mounts and SPA; and exactly the panel routes that answer during first-run
// setup.
func TestRegisterRouteBaseline(t *testing.T) {
	app, _, opened := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	}, nil)
	sealReports, err := sealReportSource(opened)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.BuildServeHandler(nil, "/mcp", httpapi.ServiceLifecycleRoutes(
		http.NotFoundHandler(), http.NotFoundHandler(),
	)...)
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	accesstest.CompleteOpen(t, handler, accountmode.Request{Mode: accountmode.ModeMulti}, false)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "http://127.0.0.1/api/v1/route-manifest.json", nil,
	))
	var manifest struct {
		AccessMode string `json:"accessMode"`
		Routes     []struct {
			Method      string `json:"method"`
			Path        string `json:"path"`
			Access      string `json:"access"`
			DuringSetup bool   `json:"duringSetup"`
		} `json:"routes"`
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("route manifest = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("decode route manifest: %v", err)
	}
	if manifest.AccessMode != "open" {
		t.Fatalf("access mode = %q, want open", manifest.AccessMode)
	}
	var got, duringSetup []string
	for _, route := range manifest.Routes {
		got = append(got, route.Method+" "+route.Path+" "+route.Access)
		if route.DuringSetup {
			duringSetup = append(duringSetup, route.Method+" "+route.Path)
		}
	}
	type operation struct{ method, pattern string }
	var v1 []operation
	for _, route := range httpapi.NewRouteRegistry(app.Service(), nil, sealReports).Routes() {
		v1 = append(v1, operation{route.Method, route.Pattern})
	}
	v1 = append(v1,
		operation{http.MethodGet, "/account-mode"},
		operation{http.MethodPut, "/account-mode"},
		operation{http.MethodPost, "/service/restart"},
		operation{http.MethodPost, "/service/stop"},
	)
	var want []string
	for _, op := range v1 {
		want = append(want, op.method+" /api/v1"+op.pattern+" api")
	}
	want = append(want, "GET /api/v1/route-manifest.json api")
	for _, op := range v1 {
		want = append(want, op.method+" /app/api/v1"+op.pattern+" panel")
	}
	want = append(want,
		"GET /app/api/v1/session public",
		"POST /app/api/v1/session public",
		"DELETE /app/api/v1/session panel",
		"DELETE /app/api/v1/sessions panel",
		"POST /app/api/v1/session/recovery public",
		"POST /app/api/v1/session/recovery/reset public",
		"GET /app/api/v1/setup panel",
		"PUT /app/api/v1/setup/progress panel",
		"PUT /app/api/v1/setup/encryption panel",
		"POST /app/api/v1/setup/complete panel",
		"GET /app/api/v1/access panel",
		"PUT /app/api/v1/access/mode panel",
		"PUT /app/api/v1/access/password panel",
		"GET /app/api/v1/access/tokens/{kind} panel",
		"POST /app/api/v1/access/tokens/{kind}/regenerate panel",
		"GET /mcp agent", "POST /mcp agent", "DELETE /mcp agent",
		"GET /healthz public", "GET /api/openapi.yaml public", "GET /docs public",
		"GET /docs/swagger-ui.css public", "GET /docs/swagger-ui-bundle.js public",
		"GET /docs/swagger-initializer.js public",
		"GET /docs/swagger-ui-bundle.js.LICENSE.txt public",
		"GET /docs/LICENSE public", "GET /docs/NOTICE public",
		"GET /* public",
	)
	if !slices.Equal(got, want) {
		t.Fatalf("assembled route surface mismatch\ngot:  %v\nwant: %v", got, want)
	}
	wantDuringSetup := []string{
		"GET /app/api/v1/credential-storage",
		"GET /app/api/v1/accounts",
		"POST /app/api/v1/signing/keys/generate",
		"POST /app/api/v1/signing/keys/import",
		"GET /app/api/v1/signing/keys",
		"GET /app/api/v1/signing/keys/active/public",
		"GET /app/api/v1/signing/keys/{keyId}/public",
		"GET /app/api/v1/signing/config",
		"PUT /app/api/v1/signing/config",
		"GET /app/api/v1/account-mode",
		"PUT /app/api/v1/account-mode",
		"POST /app/api/v1/service/restart",
		"GET /app/api/v1/session",
		"GET /app/api/v1/setup",
		"PUT /app/api/v1/setup/progress",
		"PUT /app/api/v1/setup/encryption",
		"POST /app/api/v1/setup/complete",
	}
	slices.Sort(duringSetup)
	slices.Sort(wantDuringSetup)
	if !slices.Equal(duringSetup, wantDuringSetup) {
		t.Fatalf("routes answering during setup mismatch\ngot:  %v\nwant: %v",
			duringSetup, wantDuringSetup)
	}
}

// TestRegisterAccessModeFollowsService drives the real composition, with
// Register's own caller resolver, across a switch of the stored access mode:
// the route manifest and the panel's GET /session report open mode to a
// request without credentials; once token mode is on, the same handler
// refuses that manifest request with the bearer challenge and reports token
// mode to a request with the API token, and GET /session reports token mode.
func TestRegisterAccessModeFollowsService(t *testing.T) {
	app, _, opened := buildTestApp(t, nil, nil)
	accessState, ok := opened.(access.Store)
	if !ok {
		t.Fatal("the composed store holds no access state")
	}
	handler, err := app.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	accesstest.CompleteOpen(t, handler, accountmode.Request{Mode: accountmode.ModeMulti}, false)
	manifestMode := func(authorization string, wantStatus int) string {
		t.Helper()
		req := httptest.NewRequest(
			http.MethodGet, "http://127.0.0.1/api/v1/route-manifest.json", nil,
		)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("route manifest = %d, want %d", rec.Code, wantStatus)
		}
		if wantStatus != http.StatusOK {
			return rec.Header().Get("WWW-Authenticate")
		}
		var manifest struct {
			AccessMode string `json:"accessMode"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
			t.Fatalf("decode route manifest: %v", err)
		}
		return manifest.AccessMode
	}
	sessionMode := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(
			http.MethodGet, "http://127.0.0.1/app/api/v1/session", nil,
		))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /app/api/v1/session = %d, want 200: %s", rec.Code, rec.Body)
		}
		var status struct {
			AccessMode string `json:"accessMode"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
			t.Fatalf("decode GET /app/api/v1/session: %v", err)
		}
		return status.AccessMode
	}
	if mode := manifestMode("", http.StatusOK); mode != "open" {
		t.Fatalf("access mode = %q, want open", mode)
	}
	if mode := sessionMode(); mode != "open" {
		t.Fatalf("session access mode = %q, want open", mode)
	}
	const apiToken = "pit_api_register-test"
	enableTokenMode(t, accessState, apiToken)
	if challenge := manifestMode("", http.StatusUnauthorized); challenge !=
		`Bearer realm="pit-officer"` {
		t.Fatalf("WWW-Authenticate = %q, want the bearer challenge", challenge)
	}
	if mode := manifestMode("Bearer "+apiToken, http.StatusOK); mode != "token" {
		t.Fatalf("access mode = %q, want token", mode)
	}
	if mode := sessionMode(); mode != "token" {
		t.Fatalf("session access mode = %q, want token", mode)
	}
}

// TestRegisterPanelAccessRoutesWithoutAccessService builds Register's
// composition with a service factory a host replaced, which leaves it no
// access Service: the public GET /app/api/v1/session and a non-public route
// with a host resolver answer 500 internal instead of reaching a missing
// Service or passing the installed authorizer.
func TestRegisterPanelAccessRoutesWithoutAccessService(t *testing.T) {
	app, _, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	}, func(
		_ context.Context,
		n node.Node,
		md backend.MarketDataRuntime,
		rt backend.TradingRuntime,
		signer fwsigning.Service,
		_ *marketdata.Registry,
		_ *frameworkmcp.ToolRegistry,
	) (backend.ControlPlane, error) {
		return backend.New(n, md, signer, func([]byte, domain.Order) (string, error) {
			return "", errUnexpectedOrderChain
		}, backend.WithTradingRuntime(rt))
	})
	handler, err := app.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	for _, path := range []string{"/app/api/v1/session", "/app/api/v1/mcp-access"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(
			http.MethodGet, "http://127.0.0.1"+path, nil,
		))
		var answer struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		err = json.Unmarshal(rec.Body.Bytes(), &answer)
		if rec.Code != http.StatusInternalServerError || err != nil ||
			answer.Error.Code != "internal" {
			t.Fatalf("GET %s = %d, error code %q, want 500 internal",
				path, rec.Code, answer.Error.Code)
		}
	}
}

// enableTokenMode switches the stored access mode to token mode with apiToken
// as the API token, past the access Service, which no app exposes.
func enableTokenMode(t *testing.T, state access.Store, apiToken string) {
	t.Helper()
	now := time.Now()
	if err := state.EnableTokenMode(context.Background(), access.Credentials{
		Password:   access.Password{Hash: "$argon2id$test", SetAt: now},
		APIToken:   apiToken,
		AgentToken: "pit_mcp_register-test",
	}, access.Session{
		IDHash:     sha256.Sum256([]byte("register-test")),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("EnableTokenMode: %v", err)
	}
}

// fixedModeAccessStore publishes a chosen mode and implements Snapshot only.
type fixedModeAccessStore struct {
	access.Store
	mode access.Mode
}

func (s fixedModeAccessStore) Snapshot() access.Snapshot {
	return access.Snapshot{Mode: s.mode}
}

// TestManifestAccessModeFailsClosed checks that the route manifest gets the
// empty access mode, which it refuses as an internal error, never open mode:
// without an access Service and for a mode it does not know.
func TestManifestAccessModeFailsClosed(t *testing.T) {
	if mode := manifestAccessMode(nil); mode != "" {
		t.Fatalf("manifestAccessMode(nil) = %q, want the empty mode", mode)
	}
	service, err := access.New(
		fixedModeAccessStore{mode: "unknown"}, struct{ access.Auditor }{}, io.Discard,
		struct{ access.SetupDecisions }{},
	)
	if err != nil {
		t.Fatalf("access.New: %v", err)
	}
	if mode := manifestAccessMode(service); mode != "" {
		t.Fatalf("manifestAccessMode(unknown mode) = %q, want the empty mode", mode)
	}
}

// TestRegisterBrowserGuards drives the real composition the way a page open in
// the operator's browser can: a cross-site or text/plain write and a write
// under a re-bound host are refused before any handler runs and change
// nothing, while the panel's own same-origin JSON writes still work on both
// mounts.
func TestRegisterBrowserGuards(t *testing.T) {
	app, handler := serveSeededApp(
		t, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")}, nil,
	)
	accesstest.CompleteOpen(t, handler, accountmode.Request{Mode: accountmode.ModeMulti}, false)
	if mcpCommandEnabled(t, app, "create_account") {
		t.Fatal("create_account is enabled before the hostile writes")
	}
	for _, tc := range []struct {
		name, method, target, contentType, body string
		header                                  map[string]string
		status                                  int
		code                                    string
	}{
		{
			name: "cross-site database reset", method: http.MethodPost,
			target:      "http://127.0.0.1/api/v1/database/reset",
			contentType: "text/plain", body: `{"confirm":true}`,
			header: map[string]string{"Origin": "http://attacker.example"},
			status: http.StatusForbidden, code: "origin_not_allowed",
		},
		{
			name: "cross-site MCP access change", method: http.MethodPut,
			target:      "http://127.0.0.1/api/v1/mcp-access/create_account",
			contentType: "text/plain", body: `{"enabled":true}`,
			header: map[string]string{"Origin": "http://attacker.example"},
			status: http.StatusForbidden, code: "origin_not_allowed",
		},
		{
			name: "text/plain database reset", method: http.MethodPost,
			target:      "http://127.0.0.1/api/v1/database/reset",
			contentType: "text/plain", body: `{"confirm":true}`,
			status: http.StatusUnsupportedMediaType, code: "unsupported_media_type",
		},
		{
			// A page on a domain re-bound to loopback is same-origin with
			// itself, so only the Host guard tells it apart.
			name: "re-bound host database reset", method: http.MethodPost,
			target:      "http://attacker.example/api/v1/database/reset",
			contentType: "application/json", body: `{"confirm":true}`,
			header: map[string]string{
				"Origin": "http://attacker.example", "Sec-Fetch-Site": "same-origin",
			},
			status: http.StatusForbidden, code: "host_not_allowed",
		},
		{
			name: "foreign host health check", method: http.MethodGet,
			target: "http://officer.example/healthz",
			status: http.StatusForbidden, code: "host_not_allowed",
		},
	} {
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.contentType)
		for name, value := range tc.header {
			req.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var answer struct {
			Error struct{ Code string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil ||
			rec.Code != tc.status || answer.Error.Code != tc.code {
			t.Fatalf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
	}
	got := listedAccounts(t, handler, "/api/v1")
	slices.Sort(got)
	if !slices.Equal(got, []string{"default", "other"}) {
		t.Fatalf("listed accounts after the refused writes = %v, want default and other", got)
	}
	if mcpCommandEnabled(t, app, "create_account") {
		t.Fatal("a refused write enabled create_account")
	}

	// Officer answers under its loopback names, on any port.
	for _, host := range []string{"localhost:8787", "[::1]:8787", "127.0.0.1:9000"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(
			http.MethodGet, "http://"+host+"/healthz", nil,
		))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /healthz under %s = %d %s, want 200", host, rec.Code, rec.Body)
		}
	}

	// The panel's own writes: same origin, JSON bodies, on both mounts.
	for _, tc := range []struct {
		prefix  string
		enabled bool
	}{
		{"/app/api/v1", false},
		{"/api/v1", true},
	} {
		req := httptest.NewRequest(
			http.MethodPut,
			"http://127.0.0.1:8787"+tc.prefix+"/mcp-access/get_account_state",
			strings.NewReader(fmt.Sprintf(`{"enabled":%t}`, tc.enabled)),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://127.0.0.1:8787")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("same-origin JSON write on %s = %d %s, want 200", tc.prefix, rec.Code, rec.Body)
		}
		if got := mcpCommandEnabled(t, app, "get_account_state"); got != tc.enabled {
			t.Fatalf("get_account_state enabled after the write on %s = %v, want %v",
				tc.prefix, got, tc.enabled)
		}
	}
}

// TestRegisterAllowedHosts pins that the operator's host names add to the
// loopback names rather than replace them, and that an invalid one fails
// Register.
func TestRegisterAllowedHosts(t *testing.T) {
	_, handler := serveSeededApp(t, Config{
		SQLitePath:   filepath.Join(t.TempDir(), "officer.db"),
		AllowedHosts: []string{"officer"},
	}, nil)
	for _, host := range []string{"officer:8787", "OFFICER", "127.0.0.1:8787", "localhost"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(
			http.MethodGet, "http://"+host+"/healthz", nil,
		))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /healthz under %s = %d %s, want 200", host, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "http://attacker.example/healthz", nil,
	))
	var answer struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil ||
		rec.Code != http.StatusForbidden || answer.Error.Code != "host_not_allowed" {
		t.Fatalf("GET /healthz under attacker.example = %d %s, want 403 host_not_allowed",
			rec.Code, rec.Body)
	}

	if err := Register(frameworkapp.NewBuilder(), Config{
		AllowedHosts: []string{"officer:8787"},
	}); err == nil {
		t.Fatal("Register accepted the allowed host officer:8787")
	}
}

// mcpCommandEnabled reports whether the composed MCP catalogue of app enables
// the command name.
func mcpCommandEnabled(t *testing.T, app *frameworkapp.App, name string) bool {
	t.Helper()
	commands, err := app.Service().ListMcpAccess(
		auth.ContextWithCaller(context.Background(), auth.SystemCaller()),
	)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	command, ok := findCommand(commands, name)
	if !ok {
		t.Fatalf("%s missing from the composed MCP catalogue", name)
	}
	return command.Enabled
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

// restCall serves a request through handler and decodes its JSON answer,
// failing unless the status is want. A path starting with /app/api/v1/ is
// served as given; any other path gets the /api/v1 prefix.
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
func assertAccountMode(t *testing.T, handler http.Handler, prefix, method, body, want string) {
	t.Helper()
	path := strings.TrimPrefix(prefix, "/api/v1") + "/account-mode"
	rec := serveREST(handler, method, path, body)
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != want {
		t.Fatalf("%s /account-mode = %d %s, want 200 %s", method, rec.Code, got, want)
	}
}

// serveREST serves a path starting with /app/api/v1/ as given; any other path
// gets the /api/v1 prefix.
func serveREST(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	if !strings.HasPrefix(path, "/app/api/v1/") {
		path = "/api/v1" + path
	}
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// listedAccounts returns the codes GET /accounts lists.
func listedAccounts(t *testing.T, handler http.Handler, prefix string) []string {
	t.Helper()
	path := strings.TrimPrefix(prefix, "/api/v1") + "/accounts"
	listed, _ := restCall(t, handler, http.MethodGet, path, "", http.StatusOK)["accounts"].([]any)
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

// buildTestApp builds Register's composition, over a fake engine, on a fresh
// database and returns the app, its realm and the store Register opened.
// resolver replaces Register's caller resolver and serviceFactory its service
// factory; nil keeps Register's.
func buildTestApp(
	t *testing.T,
	resolver func(*http.Request) (domain.Caller, error),
	serviceFactory frameworkapp.ServiceFactory,
) (*frameworkapp.App, store.RealmStore, store.Store) {
	t.Helper()
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
		return fakeEngineNodeBuilder(buildCtx, st, fatalHook)
	})
	if resolver != nil {
		builder.SetCallerResolver(resolver)
	}
	if serviceFactory != nil {
		builder.SetServiceFactory(serviceFactory)
	}
	builder.SetSPAFactory(func() (fs.FS, error) {
		return fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, nil
	})
	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	realm, err := opened.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	return app, realm, opened
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

func TestManifestAccessModeSetup(t *testing.T) {
	service, err := access.New(
		fixedModeAccessStore{mode: access.ModeSetup}, struct{ access.Auditor }{},
		io.Discard, struct{ access.SetupDecisions }{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := manifestAccessMode(service); got != httpx.AccessModeSetup {
		t.Fatalf("manifestAccessMode(setup) = %q, want setup", got)
	}
}

func TestRegisterFirstRun(t *testing.T) {
	for _, mode := range []string{"open", "token"} {
		t.Run(mode, func(t *testing.T) {
			app, _, _ := buildTestApp(t, nil, nil)
			handler, err := app.BuildServeHandler(nil, "/mcp")
			if err != nil {
				t.Fatalf("BuildServeHandler: %v", err)
			}
			var cookie *http.Cookie
			call := func(method, path, body string, want int) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, "http://127.0.0.1:8787"+path,
					strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if cookie != nil {
					req.AddCookie(cookie)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				apitest.ExpectStatus(t, method+" "+path, rec, want)
				return rec
			}
			assertSetup := func() {
				t.Helper()
				rec := call(http.MethodGet, "/app/api/v1/session", "", http.StatusOK)
				const want = `{"accessMode":"setup","setupComplete":false,"authenticated":false}`
				if strings.TrimSpace(rec.Body.String()) != want {
					t.Fatalf("session before setup = %s, want %s", rec.Body, want)
				}
				for _, path := range []string{
					"/api/v1/accounts", "/api/v1/route-manifest.json", "/mcp", "/app/api/v1/mcp-access",
				} {
					rec := call(http.MethodGet, path, "", http.StatusServiceUnavailable)
					var answer struct {
						Error struct{ Code, Message string } `json:"error"`
					}
					const message = "Pit Officer is not set up yet. Open http://127.0.0.1:8787" +
						" in a browser on this computer and finish the first-run setup."
					if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil ||
						answer.Error.Code != "setup_required" || answer.Error.Message != message {
						t.Fatalf("GET %s = %s, want setup_required with the addressed sentence", path, rec.Body)
					}
				}
			}
			assertSetup()
			rec := call(http.MethodGet, "/app/api/v1/setup", "", http.StatusOK)
			if strings.TrimSpace(rec.Body.String()) != `{"step":"welcome","encryptionDeclinedAt":null}` {
				t.Fatalf("fresh setup state = %s, want welcome with no encryption decision", rec.Body)
			}
			call(http.MethodPut, "/app/api/v1/setup/progress", `{"step":"accounts"}`, http.StatusNoContent)
			rec = call(http.MethodPost, "/app/api/v1/setup/complete",
				`{"access":"open","acknowledged":true}`, http.StatusConflict)
			if !strings.Contains(rec.Body.String(), "account mode") ||
				!strings.Contains(rec.Body.String(), "encryption") {
				t.Fatalf("completion without decisions = %s, want both missing decisions", rec.Body)
			}
			accounts := accountmode.Request{Mode: accountmode.ModeMulti}
			if mode == "open" {
				accesstest.CompleteOpen(t, handler, accounts, false)
			} else {
				cookie = accesstest.CompleteToken(t, handler, accounts, "first run test password")
				if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
					t.Fatal("setup completion issued a session cookie without the panel protections")
				}
				for kind, prefix := range map[string]string{"api": "pit_api_", "agent": "pit_mcp_"} {
					rec := call(http.MethodGet, "/app/api/v1/access/tokens/"+kind, "", http.StatusOK)
					var answer struct{ Token string }
					if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil ||
						!strings.HasPrefix(answer.Token, prefix) || answer.Token == prefix {
						t.Fatalf("setup completion issued no %s token", kind)
					}
					method, path, body := http.MethodGet, "/api/v1/accounts", ""
					if kind == "agent" {
						method, path = http.MethodPost, "/mcp"
						body = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
							`"protocolVersion":"2025-03-26","capabilities":{},` +
							`"clientInfo":{"name":"first-run-test","version":"1"}}}`
					}
					req := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Accept", "application/json, text/event-stream")
					req.Header.Set("Authorization", "Bearer "+answer.Token)
					authRec := httptest.NewRecorder()
					handler.ServeHTTP(authRec, req)
					apitest.ExpectStatus(t, kind+" token "+method+" "+path, authRec, http.StatusOK)
					if kind == "agent" && (!strings.Contains(authRec.Body.String(), `"result":`) ||
						strings.Contains(authRec.Body.String(), `"error":`)) {
						t.Fatal("agent token MCP initialize returned no success result")
					}
				}
			}
			rec = call(http.MethodGet, "/app/api/v1/session", "", http.StatusOK)
			want := fmt.Sprintf(`{"accessMode":%q,"setupComplete":true,"authenticated":%t}`, mode, mode == "token")
			if strings.TrimSpace(rec.Body.String()) != want {
				t.Fatalf("session after setup = %s, want %s", rec.Body, want)
			}
			if mode == "open" {
				call(http.MethodGet, "/api/v1/accounts", "", http.StatusOK)
			} else {
				call(http.MethodGet, "/api/v1/accounts", "", http.StatusUnauthorized)
			}
			for _, route := range []struct{ method, path, body string }{
				{http.MethodGet, "/setup", ""},
				{http.MethodPut, "/setup/progress", `{"step":"access"}`},
				{http.MethodPut, "/setup/encryption", `{"encryption":"none","acknowledged":true}`},
				{http.MethodPost, "/setup/complete", `{"access":"open","acknowledged":true}`},
			} {
				rec := call(route.method, "/app/api/v1"+route.path, route.body, http.StatusConflict)
				if code, _ := apitest.Refusal(rec); code != "setup_complete" {
					t.Fatalf("%s %s after completion = %s, want setup_complete", route.method, route.path, rec.Body)
				}
			}
			call(http.MethodPost, "/app/api/v1/database/reset", `{"confirm":true}`, http.StatusOK)
			assertSetup()
			rec = call(http.MethodGet, "/app/api/v1/setup", "", http.StatusOK)
			if strings.TrimSpace(rec.Body.String()) != `{"step":"welcome","encryptionDeclinedAt":null}` {
				t.Fatalf("setup after reset = %s, want welcome with no encryption decision", rec.Body)
			}
		})
	}
}

func TestRegisterSetupAccessAuthorizer(t *testing.T) {
	app, _, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	}, nil)
	handler, err := app.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path   string
		status int
		code   string
	}{
		{"/app/api/v1/setup", 200, ""},
		{"/app/api/v1/mcp-access", 503, "setup_required"},
		{"/api/v1/accounts", 503, "setup_required"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1"+tc.path, nil))
		var answer struct {
			Step  string `json:"step"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		err := json.Unmarshal(rec.Body.Bytes(), &answer)
		if rec.Code != tc.status || err != nil || answer.Error.Code != tc.code {
			t.Fatalf("GET %s = %d, error code %q, want %d %s",
				tc.path, rec.Code, answer.Error.Code, tc.status, tc.code)
		}
		if tc.status == 200 && answer.Step != "welcome" {
			t.Fatalf("GET /setup step = %q, want welcome", answer.Step)
		}
		if tc.code == "setup_required" && answer.Error.Message != domain.ErrSetupRequired.Error() {
			t.Fatalf("GET %s message = %q, want bare setup_required %q",
				tc.path, answer.Error.Message, domain.ErrSetupRequired.Error())
		}
	}

	decisions := setupDecisions{service: app.Service()}
	chosen, err := decisions.AccountModeChosen(context.Background())
	if err != nil || chosen {
		t.Fatalf("unchosen account mode = %t, %v, want false", chosen, err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "http://127.0.0.1/app/api/v1/account-mode",
		strings.NewReader(`{"mode":"multi"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("choose account mode = %d %s", rec.Code, rec.Body)
	}
	chosen, err = decisions.AccountModeChosen(context.Background())
	if err != nil || !chosen {
		t.Fatalf("saved account mode = %t, %v, want true", chosen, err)
	}
}

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
