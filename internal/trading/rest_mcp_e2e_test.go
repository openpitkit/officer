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

package trading

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	fwtrading "go.openpit.dev/officer/framework/trading"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/internal/logtest"
	"go.openpit.dev/officer/internal/polltest"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/mcptools"
	"go.openpit.dev/officer/signing"
)

type tradingE2ELog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (l *tradingE2ELog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.Write(p)
}

func (l *tradingE2ELog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.String()
}

type tradingE2E struct {
	t       *testing.T
	ctx     context.Context
	caller  domain.Caller
	builder *app.Builder
	app     *app.App
	router  http.Handler
	realm   store.RealmStore
	core    fwengine.Engine
	logs    tradingE2ELog
	venueMu sync.Mutex
	// closing, guarded by venueMu, marks app shutdown, when the connector
	// may abandon a dialed stream anywhere in its handshake: before sending
	// its auth frame, or before answering the venue's close frame. Such an
	// abandoned stream is not a venue failure.
	closing        bool
	fees           []map[string]string
	accountError   bool
	accountRefusal string
	requests       map[string]int
	streams        chan struct{}
	pendingStreams int
	stream         *websocket.Conn
	// streamFault, when set, makes the venue fail every stream handshake
	// after the auth frame: "unauthorized" refuses it, "close" closes the
	// connection with a reason that echoes the credentials.
	streamFault string
	posts       []map[string]string
	venueOrder  map[string]string
}

func tradingE2EMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func tradingE2EDecode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	tradingE2EMust(t, json.Unmarshal(raw, &out))
	return out
}

func tradingE2ENoSecrets(t *testing.T, where string, raw []byte) {
	t.Helper()
	for _, marker := range []string{"test-key-marker", "test-secret-marker", "second-key-marker", "second-secret-marker"} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Fatalf("credential leaked in %s", where)
		}
	}
}

func newTradingE2E(t *testing.T, sealed bool, providers ...fwtrading.Provider) *tradingE2E {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &tradingE2E{t: t, ctx: ctx, caller: domain.Caller{Principal: domain.PrincipalOperator, Source: domain.SourceAPI}, requests: make(map[string]int), streams: make(chan struct{}, 3)}
	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			conn := acceptAlpaca(t, w, r)
			if conn == nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			_, raw, err := conn.Read(ctx)
			if err != nil {
				if !f.isClosing() {
					t.Error("auth frame missing")
				}
				return
			}
			if !checkAuthFrame(t, raw) {
				return
			}
			f.venueMu.Lock()
			fault := f.streamFault
			f.venueMu.Unlock()
			if fault != "" {
				switch fault {
				case "unauthorized":
					sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":"unauthorized","action":"authenticate"}}`)
				case "close":
					if err := conn.Close(websocket.StatusPolicyViolation, "refused test-key-marker test-secret-marker"); err != nil {
						if !f.isClosing() {
							t.Error(err)
						}
					}
				default:
					t.Errorf("unknown stream fault %q", fault)
				}
				// A refused attempt releases one waiter; retries find
				// the slot taken and do not block.
				select {
				case f.streams <- struct{}{}:
				default:
				}
				return
			}
			if !completeHandshake(t, ctx, conn) {
				return
			}
			f.venueMu.Lock()
			f.stream = conn
			f.venueMu.Unlock()
			select {
			case f.streams <- struct{}{}:
			case <-ctx.Done():
				return
			}
			_, _, _ = conn.Read(ctx)
			return
		}
		f.venueMu.Lock()
		refusal := f.accountRefusal
		f.venueMu.Unlock()
		// A set refusal answers before the gate below, so it can echo
		// credentials other than the shared markers.
		if refusal != "" && r.URL.Path == "/v2/account" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 40110000, "message": refusal})
			return
		}
		primary := r.Header.Get("APCA-API-KEY-ID") == "test-key-marker" && r.Header.Get("APCA-API-SECRET-KEY") == "test-secret-marker"
		secondary := r.Header.Get("APCA-API-KEY-ID") == "second-key-marker" && r.Header.Get("APCA-API-SECRET-KEY") == "second-secret-marker"
		if !primary && !secondary {
			t.Error("missing venue credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.venueMu.Lock()
		defer f.venueMu.Unlock()
		f.requests[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/account":
			if f.accountError {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"code":40110000,"message":"test-key-marker test-secret-marker"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"id":"paper-account","status":"ACTIVE"}`)
		case "/v2/assets/AAPL":
			_, _ = fmt.Fprint(w, `{"symbol":"AAPL","name":"Fake Apple","class":"us_equity","exchange":"NASDAQ","status":"active","tradable":true,"fractionable":true}`)
		case "/v2/assets":
			if r.URL.Query().Get("asset_class") == "crypto" {
				_, _ = fmt.Fprint(w, `[]`)
				return
			}
			_, _ = fmt.Fprint(w, `[{"symbol":"AAPL","name":"Fake Apple","class":"us_equity","exchange":"NASDAQ","status":"active","tradable":true}]`)
		case "/v2/orders":
			if r.Method != http.MethodPost {
				t.Error("expected venue order POST")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error("invalid venue order body")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.posts = append(f.posts, body)
			f.venueOrder = map[string]string{"id": "venue-1",
				"client_order_id": body["client_order_id"], "status": "new",
				"qty": body["qty"], "filled_qty": "0"}
			_ = json.NewEncoder(w).Encode(f.venueOrder)
		case "/v2/orders/venue-1":
			_ = json.NewEncoder(w).Encode(f.venueOrder)
		case "/v2/account/activities/FILL":
			_, _ = fmt.Fprint(w, `[]`)
		case "/v2/account/activities":
			page := f.fees
			if page == nil || r.URL.Query().Get("page_token") != "" {
				page = []map[string]string{}
			}
			_ = json.NewEncoder(w).Encode(page)
		default:
			t.Errorf("unexpected venue request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(venue.Close)
	provider := AlpacaProvider()
	provider.Build = func(ctx fwtrading.BuildContext) (fwtrading.Connector, error) {
		c := ctx.Connection

		return newAlpacaConnector(c, venue.URL, "ws"+strings.TrimPrefix(venue.URL, "http")+"/stream")
	}
	// Persist across app restarts without testing power-loss durability.
	dbPath := t.TempDir() + "/officer.db?_pragma=journal_mode(MEMORY)&_pragma=synchronous(OFF)"
	var options []sqlite.Option
	if sealed {
		key, err := secret.ParseMasterKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
		tradingE2EMust(t, err)
		options = append(options, sqlite.WithMasterKey(key))
	}
	f.builder = app.NewBuilder()
	// Each start opens the store anew; the route config reads its seal report.
	var sealReports httpapi.SealReportSource
	f.builder.SetStoreFactory(func() (store.Store, error) {
		st, err := sqlite.New(dbPath, domain.DefaultRealm, options...)
		if err != nil {
			return nil, err
		}
		reader, ok := st.(interface {
			SealReport(ctx context.Context) (sqlite.SealReport, bool, error)
		})
		if !ok {
			_ = st.Close()
			return nil, errors.New("the SQLite store does not implement SealReport")
		}
		sealReports = func(ctx context.Context) (time.Time, int, bool, error) {
			report, sealed, err := reader.SealReport(ctx)
			return report.SealedAt, report.ResealedSecrets, sealed, err
		}
		return st, nil
	})
	f.builder.SetNodeBuilder(func(ctx context.Context, st store.Store, fatal app.FatalShutdownHook) (node.Node, fwengine.Engine, error) {
		var err error
		f.realm, err = st.ForRealm(ctx, domain.DefaultRealm)
		if err != nil {
			return nil, nil, err
		}
		n, core, err := node.NewLocalNode(ctx, domain.DefaultRealm, st, engine.NewOpenPitEngineBuildFunc(), fatal)
		f.core = core
		return n, core, err
	})
	f.builder.SetSigningFactory(func(st store.RealmStore) (fwsigning.Service, error) { return signing.New(st) })
	f.builder.SetServiceFactory(func(_ context.Context, n node.Node, md backend.MarketDataRuntime, rt backend.TradingRuntime, signer fwsigning.Service,
		_ *marketdata.Registry, tools *frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
		return backend.New(n, md, signer, engine.LockSettlementPrice, backend.WithTradingRuntime(rt), backend.WithMCPCatalogProvider(tools))
	})
	f.builder.AddToolRegistrar(func(reg *frameworkmcp.ToolRegistry, _ frameworkmcp.Source) { mcptools.RegisterTools(reg, nil) })
	f.builder.SetAuthorizer(httpx.AllowAll{})
	f.builder.SetCallerResolver(func(*http.Request) (domain.Caller, error) { return f.caller, nil })
	host, err := httpx.AllowedHosts([]string{"127.0.0.1"})
	tradingE2EMust(t, err)
	f.builder.SetBrowserGuards(httpx.BrowserGuards{Host: host, Origin: httpx.SameOrigin(), ContentType: httpx.JSONBody()})
	f.builder.SetRouteConfigBuilder(func(svc backend.ControlPlane, logs httpx.LogSource) app.RouteConfig {
		config := httpapi.RouteConfig(svc, logs, sealReports)
		config.AccessMode = func() httpx.AccessMode { return httpx.AccessModeOpen }
		return config
	})
	f.builder.SetSPAFactory(func() (fs.FS, error) { return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test")}}, nil })
	tradingE2EMust(t, f.builder.RegisterTradingProvider(provider))
	for _, extra := range providers {
		tradingE2EMust(t, f.builder.RegisterTradingProvider(extra))
	}
	logtest.SetDefault(t, slog.NewTextHandler(&f.logs, nil))
	f.start()
	t.Cleanup(f.close)
	for _, code := range []string{"desk", "other", "fees"} {
		f.rest(http.MethodPost, "/accounts", map[string]any{"code": code, "currency": "USD"}, http.StatusCreated)
	}
	f.rest(http.MethodPost, "/assets", map[string]any{"code": "AAPL"}, http.StatusCreated)
	return f
}

func (f *tradingE2E) start() {
	f.t.Helper()
	f.venueMu.Lock()
	f.closing = false
	f.venueMu.Unlock()
	var err error
	f.app, err = f.builder.Build(auth.ContextWithCaller(f.ctx, f.caller), slog.Default(), func(err error) { f.t.Errorf("fatal app shutdown: %v", err) })
	tradingE2EMust(f.t, err)
	connections, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(f.t, err)
	for _, connection := range connections {
		if connection.Enabled && connection.Provider == domain.TradingProviderAlpaca {
			f.pendingStreams++
		}
	}
	f.waitStreams()
	f.router, err = f.app.BuildServeHandler(nil, "/mcp")
	tradingE2EMust(f.t, err)
}

func (f *tradingE2E) waitStreams() {
	f.t.Helper()
	for f.pendingStreams > 0 {
		<-f.streams // Venue stream handshake completed.
		f.pendingStreams--
	}
}

// isClosing reports whether the app is shutting down; see closing.
func (f *tradingE2E) isClosing() bool {
	f.venueMu.Lock()
	defer f.venueMu.Unlock()
	return f.closing
}

func (f *tradingE2E) close() {
	f.t.Helper()
	defer func() {
		f.venueMu.Lock()
		f.closing = true
		f.venueMu.Unlock()
		tradingE2EMust(f.t, f.app.Close())
	}()
	f.waitStreams()
}

func (f *tradingE2E) rest(method, path string, body any, status int) []byte {
	f.t.Helper()
	raw, err := json.Marshal(body)
	tradingE2EMust(f.t, err)
	r := httptest.NewRequest(method, "http://127.0.0.1/api/v1"+path, bytes.NewReader(raw)).WithContext(f.ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if method == http.MethodPost && path == "/trading/connections" && w.Code == http.StatusCreated {
		connection := tradingE2EDecode[tradingE2ECreated](f.t, w.Body.Bytes()).Connection
		if connection.Enabled && connection.Provider == domain.TradingProviderAlpaca {
			f.pendingStreams++
			f.waitStreams()
		}
	}
	if path != "/backup/export" {
		tradingE2ENoSecrets(f.t, "REST "+path, w.Body.Bytes())
		if bytes.Contains(w.Body.Bytes(), []byte(`"credentials":`)) {
			f.t.Fatal("REST returned credentials field")
		}
	}
	if w.Code != status {
		f.t.Fatalf("%s %s: status %d, want %d: %s\ncontext: %v\napplication logs:\n%s",
			method, path, w.Code, status, w.Body.String(), f.ctx.Err(), f.logs.String())
	}
	return w.Body.Bytes()
}

// Uses the integration harness's Build/NewInMemoryTransports path with the
// real service.
func (f *tradingE2E) callMCP(name string, input any) (*sdkmcp.CallToolResult, error) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	reg := frameworkmcp.NewToolRegistry()
	mcptools.RegisterTools(reg, nil)
	caller := f.caller
	caller.Source = domain.SourceMCP
	server, err := frameworkmcp.Build(reg, frameworkmcp.SourceFor(f.app.Service()), nil, httpx.AllowAll{}, caller)
	tradingE2EMust(f.t, err)
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	tradingE2EMust(f.t, err)
	result, callErr := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: input})
	// Stop the server before closing the client: the transport is a synchronous
	// pipe, and a client that hangs up while the server still writes the tail of
	// its last response fails the server's Run with a closed pipe.
	cancel()
	runErr := <-serverErr
	_ = session.Close()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		f.t.Fatal(runErr)
	}
	return result, callErr
}

func (f *tradingE2E) mcp(name string, input any, wantError bool) *sdkmcp.CallToolResult {
	f.t.Helper()
	result, callErr := f.callMCP(name, input)
	tradingE2EMust(f.t, callErr)
	if name == "create_trading_connection" && !result.IsError {
		connection := tradingE2EStructured[tradingE2ECreated](f.t, result).Connection
		if connection.Enabled && connection.Provider == domain.TradingProviderAlpaca {
			f.pendingStreams++
			f.waitStreams()
		}
	}
	raw, err := json.Marshal(result)
	tradingE2EMust(f.t, err)
	tradingE2ENoSecrets(f.t, "MCP "+name, raw)
	if bytes.Contains(raw, []byte(`"credentials":`)) {
		f.t.Fatal("MCP returned credentials field")
	}
	if result.IsError != wantError {
		f.t.Fatalf("%s: IsError = %t, want %t: %s", name, result.IsError, wantError, raw)
	}
	return result
}

// mcpLoosening calls the MCP tool name as the agent and requires its refusal
// as a loosening.
func (f *tradingE2E) mcpLoosening(name string, input any) {
	f.t.Helper()
	result := f.mcp(name, input, true)
	if len(result.Content) != 1 {
		f.t.Fatalf("%s content = %v", name, result.Content)
	}
	if text, ok := result.Content[0].(*sdkmcp.TextContent); !ok || !strings.HasPrefix(text.Text, "loosening_refused: ") {
		f.t.Fatalf("%s refusal = %+v, want loosening_refused", name, result.Content[0])
	}
}

func tradingE2EStructured[T any](t *testing.T, result *sdkmcp.CallToolResult) T {
	t.Helper()
	if result.StructuredContent == nil {
		t.Fatal("missing MCP structured content")
	}
	raw, err := json.Marshal(result.StructuredContent)
	tradingE2EMust(t, err)
	return tradingE2EDecode[T](t, raw)
}

type tradingE2EConnection struct {
	ID            string                     `json:"id"`
	Provider      string                     `json:"provider"`
	Label         string                     `json:"label"`
	Mode          string                     `json:"mode"`
	VenueIdentity string                     `json:"venueIdentity"`
	FeeAccount    *string                    `json:"feeAccount"`
	Enabled       bool                       `json:"enabled"`
	Secrets       map[string]bool            `json:"secrets"`
	Instruments   []domain.TradingInstrument `json:"instruments"`
	Access        []domain.TradingAccess     `json:"access"`
	State         string                     `json:"state"`
	Error         string                     `json:"error"`
	Diagnostics   []struct {
		Level, Code, Kind, Title, Detail string
	} `json:"diagnostics"`
}

type tradingE2ECreated struct {
	Connection tradingE2EConnection `json:"connection"`
}
type tradingE2ESnapshot struct {
	Providers   []json.RawMessage      `json:"providers"`
	Connections []tradingE2EConnection `json:"connections"`
}
type tradingE2ETrading struct {
	Trading tradingE2ESnapshot `json:"trading"`
}
type tradingE2EDestinations struct {
	Destinations []struct {
		Connection string `json:"connection"`
		Mode       string `json:"mode"`
		Enabled    bool   `json:"enabled"`
	} `json:"destinations"`
}

func tradingE2ECreateInput(label, mode string) map[string]any {
	return map[string]any{"provider": "alpaca", "label": label, "mode": mode,
		"credentials": map[string]string{"apiKey": "test-key-marker", "apiSecret": "test-secret-marker"}, "enabled": true}
}

func tradingE2EAssertCreated(t *testing.T, c tradingE2EConnection) {
	t.Helper()
	if c.ID == "" || c.Provider != "alpaca" || c.Label != "Paper" || c.Mode != "paper" || c.VenueIdentity != "paper-account" || !c.Enabled ||
		len(c.Secrets) != 2 || !c.Secrets["apiKey"] || !c.Secrets["apiSecret"] || c.Instruments == nil || len(c.Instruments) != 0 ||
		c.Access == nil || len(c.Access) != 0 || c.FeeAccount == nil || *c.FeeAccount != "" {
		t.Fatalf("created connection shape = %+v", c)
	}
}

func (f *tradingE2E) createREST() string {
	f.t.Helper()
	c := tradingE2EDecode[tradingE2ECreated](f.t, f.rest(http.MethodPost, "/trading/connections", tradingE2ECreateInput("Paper", "paper"), http.StatusCreated)).Connection
	tradingE2EAssertCreated(f.t, c)
	return c.ID
}

func (f *tradingE2E) createLiveREST() string {
	f.t.Helper()
	c := tradingE2EDecode[tradingE2ECreated](f.t, f.rest(http.MethodPost, "/trading/connections", tradingE2ECreateInput("Live", "live"), http.StatusCreated)).Connection
	if c.Mode != "live" {
		f.t.Fatalf("live connection mode = %q", c.Mode)
	}
	return c.ID
}

func tradingE2EAssertSnapshot(t *testing.T, s tradingE2ESnapshot, id string) {
	t.Helper()
	if len(s.Providers) != 1 || len(s.Connections) != 1 {
		t.Fatalf("trading snapshot shape = %+v", s)
	}
	c := s.Connections[0]
	if c.ID != id || c.VenueIdentity != "paper-account" || c.Mode != "paper" || !c.Secrets["apiKey"] || !c.Secrets["apiSecret"] ||
		len(c.Instruments) != 1 || c.Instruments[0].ExternalSymbol != "AAPL" || c.Instruments[0].BaseAsset != "AAPL" || c.Instruments[0].QuoteAsset != "USD" || !c.Instruments[0].Enabled ||
		len(c.Access) != 1 || c.Access[0].Account != "desk" {
		t.Fatalf("configured connection = %+v", c)
	}
}

// tradingE2EAssertStorage checks the installation's credential-storage fact as
// served on the wire, once a trading connection holding keys exists. A map
// makes a missing or extra field fail too.
func tradingE2EAssertStorage(t *testing.T, got map[string]bool, sealed bool) {
	t.Helper()
	want := map[string]bool{"credentialsSealed": sealed, "plaintextTradingKeys": !sealed, "plaintextMarketDataKeys": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("credential storage = %v, want %v", got, want)
	}
}

// tradingE2EAssertRESTStorage checks the REST credential-storage answer: the
// fact the MCP tool also serves, and the seal report - null and 0 on a
// database never sealed, and on one sealed when it was created an RFC 3339
// seal time with no resealed secret. A map makes a missing or extra field
// fail too.
func tradingE2EAssertRESTStorage(t *testing.T, got map[string]any, sealed bool) {
	t.Helper()
	sealedAt, present := got["sealedAt"]
	if !present {
		t.Fatalf("credential storage %v carries no sealedAt", got)
	}
	delete(got, "sealedAt")
	want := map[string]any{
		"credentialsSealed": sealed, "plaintextTradingKeys": !sealed,
		"plaintextMarketDataKeys": false, "resealedSecrets": float64(0),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("credential storage = %v, want %v", got, want)
	}
	if !sealed {
		if sealedAt != nil {
			t.Fatalf("sealedAt = %v on a database never sealed, want null", sealedAt)
		}
		return
	}
	text, ok := sealedAt.(string)
	if !ok {
		t.Fatalf("sealedAt = %v on a sealed database, want a time", sealedAt)
	}
	if _, err := time.Parse(time.RFC3339, text); err != nil {
		t.Fatalf("sealedAt = %q, want RFC 3339: %v", text, err)
	}
}

func tradingE2EAssertDestinations(t *testing.T, d tradingE2EDestinations, id string) {
	t.Helper()
	if len(d.Destinations) != 1 || d.Destinations[0].Connection != id || d.Destinations[0].Mode != "paper" || !d.Destinations[0].Enabled {
		t.Fatalf("paper destinations = %+v", d)
	}
}

func (f *tradingE2E) assertVenueReads() {
	f.t.Helper()
	f.venueMu.Lock()
	defer f.venueMu.Unlock()
	for _, path := range []string{"/v2/account", "/v2/assets/AAPL", "/v2/assets"} {
		if f.requests[path] == 0 {
			f.t.Fatalf("venue was not called: %s", path)
		}
	}
}

// accountChecks counts the venue account checks so far.
func (f *tradingE2E) accountChecks() int {
	f.venueMu.Lock()
	defer f.venueMu.Unlock()
	return f.requests["/v2/account"]
}

func TestTradingRESTEndToEnd(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(fmt.Sprintf("sealed=%t", sealed), func(t *testing.T) {
			f := newTradingE2E(t, sealed)
			id := f.createREST()
			tradingE2EAssertRESTStorage(t, tradingE2EDecode[map[string]any](t, f.rest(http.MethodGet, "/credential-storage", nil, http.StatusOK)), sealed)
			path := "/trading/connections/" + id
			check := tradingE2EDecode[struct {
				Check struct {
					VenueIdentity string `json:"venueIdentity"`
					VenueStatus   string `json:"venueStatus"`
				} `json:"check"`
			}](t,
				f.rest(http.MethodPost, path+"/check", map[string]any{}, http.StatusOK))
			if check.Check.VenueIdentity != "paper-account" || check.Check.VenueStatus != "ACTIVE" {
				t.Fatalf("check = %+v", check)
			}
			f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
			verification := tradingE2EDecode[struct {
				Verification struct {
					Supported bool
					Exists    bool
					Tradable  bool
				} `json:"verification"`
			}](t,
				f.rest(http.MethodPost, path+"/verify-symbol", map[string]any{"externalSymbol": "AAPL"}, http.StatusOK))
			if !verification.Verification.Supported || !verification.Verification.Exists || !verification.Verification.Tradable {
				t.Fatalf("verification = %+v", verification)
			}
			search := tradingE2EDecode[backend.TradingSymbolSearch](t,
				f.rest(http.MethodPost, path+"/search-symbols", map[string]any{"query": "AAPL"}, http.StatusOK))
			if !search.Supported || len(search.Matches) != 1 || search.Matches[0].Symbol != "AAPL" || search.Matches[0].Name != "Fake Apple" || !search.Matches[0].Tradable {
				t.Fatalf("search = %+v", search)
			}
			f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
			tradingE2EAssertDestinations(t, tradingE2EDecode[tradingE2EDestinations](t, f.rest(http.MethodGet, "/accounts/desk/trading-destinations?mode=paper", nil, http.StatusOK)), id)
			tradingE2EAssertSnapshot(t, tradingE2EDecode[tradingE2ETrading](t, f.rest(http.MethodGet, "/trading", nil, http.StatusOK)).Trading, id)
			f.assertVenueReads()
		})
	}
}

func TestEtoroRegistrationRESTAndMCPEndToEnd(t *testing.T) {
	f := newTradingE2E(t, true)
	var mu sync.Mutex
	missingCID := false
	expectedAPIKey, expectedUserKey := "test-key-marker", "test-secret-marker"
	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/me" || r.URL.RawQuery != "" {
			t.Errorf("unexpected eToro registration request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		apiKey, userKey, missing := expectedAPIKey, expectedUserKey, missingCID
		mu.Unlock()
		if r.Header.Get("x-api-key") != apiKey || r.Header.Get("x-user-key") != userKey {
			t.Error("eToro registration credential headers do not match the expected key pair")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("x-request-id") == "" {
			t.Error("eToro registration request is missing request id")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if missing {
			_, _ = fmt.Fprint(w, `{"gcid":7,"realCid":99,"username":"operator"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"gcid":7,"realCid":99,"demoCid":12345,"username":"operator"}`)
	}))
	t.Cleanup(venue.Close)
	p := EtoroProvider()
	p.Build = func(ctx fwtrading.BuildContext) (fwtrading.Connector, error) {
		c := ctx.Connection

		return newEtoroConnector(c, venue.URL)
	}
	tradingE2EMust(t, f.app.Close())
	tradingE2EMust(t, f.builder.RegisterTradingProvider(p))
	tradingE2EMust(t, f.builder.RegisterTradingProvider(OKXProvider()))
	f.start()
	input := map[string]any{
		"provider": "etoro", "label": "eToro demo", "mode": "paper", "enabled": false,
		"credentials": map[string]string{"apiKey": "test-key-marker", "userKey": "test-secret-marker"},
	}
	created := tradingE2EDecode[tradingE2ECreated](t,
		f.rest(http.MethodPost, "/trading/connections", input, http.StatusCreated)).Connection
	if created.Provider != "etoro" || created.Mode != "paper" || created.VenueIdentity != "12345" || created.Enabled {
		t.Fatalf("eToro REST registration = %+v", created)
	}
	path := "/trading/connections/" + created.ID
	mu.Lock()
	expectedAPIKey, expectedUserKey = "second-key-marker", "second-secret-marker"
	mu.Unlock()
	f.rest(http.MethodPut, path+"/settings", map[string]any{
		"credentials": map[string]string{"apiKey": "second-key-marker", "userKey": "second-secret-marker"},
	}, http.StatusOK)
	check := tradingE2EDecode[struct {
		Check struct{ VenueIdentity, VenueStatus string }
	}](t, f.rest(http.MethodPost, path+"/check", map[string]any{}, http.StatusOK)).Check
	if check.VenueIdentity != "12345" || check.VenueStatus != "" {
		t.Fatalf("eToro REST identity after key rotation = %+v", check)
	}
	f.rest(http.MethodPut, "/mcp-access/check_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
	mcpCheck := tradingE2EStructured[struct{ VenueIdentity, VenueStatus string }](t,
		f.mcp("check_trading_connection", map[string]any{"connection": created.ID}, false))
	if mcpCheck != check {
		t.Fatalf("eToro MCP account = %+v, want %+v", mcpCheck, check)
	}
	assertCatalogue := func(snapshot tradingE2ESnapshot) {
		t.Helper()
		var got []backend.TradingProvider
		for _, raw := range snapshot.Providers {
			got = append(got, tradingE2EDecode[backend.TradingProvider](t, raw))
		}
		want := []backend.TradingProvider{
			{
				ExecutionPolicy: false,
				Type:            "alpaca", Title: "Alpaca", VerifiesSymbols: true, SearchesSymbols: true, UnlinkedFees: true,
			},
			{
				ExecutionPolicy: false,
				Type:            "etoro", Title: "eToro", VerifiesSymbols: true, SearchesSymbols: true,
			},
			{Type: "okx", Title: "OKX", VerifiesSymbols: true, SearchesSymbols: true},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("eToro registration catalogue = %+v, want %+v", got, want)
		}
		if len(snapshot.Connections) != 1 || snapshot.Connections[0].VenueIdentity != "12345" {
			t.Fatalf("eToro catalogue connections = %+v", snapshot.Connections)
		}
	}
	assertCatalogue(tradingE2EDecode[tradingE2ETrading](t,
		f.rest(http.MethodGet, "/trading", nil, http.StatusOK)).Trading)
	assertCatalogue(tradingE2EStructured[tradingE2ESnapshot](t, f.mcp("get_trading", map[string]any{}, false)))
	mu.Lock()
	missingCID = true
	mu.Unlock()
	f.rest(http.MethodPost, path+"/check", map[string]any{}, http.StatusBadGateway)
	f.mcp("check_trading_connection", map[string]any{"connection": created.ID}, true)
}

func TestTradingMCPEndToEnd(t *testing.T) {
	f := newTradingE2E(t, true)
	result := f.mcp("create_trading_connection", tradingE2ECreateInput("Paper", "paper"), true)
	if len(result.Content) != 1 {
		t.Fatalf("disabled tool content = %v", result.Content)
	}
	if text, ok := result.Content[0].(*sdkmcp.TextContent); !ok ||
		!strings.Contains(text.Text, `Command "create_trading_connection" is disabled in the Pit Officer panel`) {
		t.Fatalf("disabled refusal = %v", result.Content)
	}
	connections, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	if len(connections) != 0 {
		t.Fatal("disabled mutating tool created a connection")
	}
	if f.accountChecks() != 0 {
		t.Fatal("disabled mutating tool reached the venue")
	}
	// An agent may not create, map or grant, so the operator does it over REST.
	id := f.setupSend("REST")
	f.rest(http.MethodPut, "/mcp-access/check_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
	tradingE2EAssertStorage(t, tradingE2EStructured[map[string]bool](t, f.mcp("get_credential_storage", map[string]any{}, false)), true)
	check := tradingE2EStructured[struct {
		VenueIdentity string
		VenueStatus   string
	}](t,
		f.mcp("check_trading_connection", map[string]any{"connection": id}, false))
	if check.VenueIdentity != "paper-account" || check.VenueStatus != "ACTIVE" {
		t.Fatalf("MCP check = %+v", check)
	}
	verification := tradingE2EStructured[struct {
		Supported bool
		Exists    bool
		Tradable  bool
	}](t,
		f.mcp("verify_trading_symbol", map[string]any{"connection": id, "externalSymbol": "AAPL"}, false))
	if !verification.Supported || !verification.Exists || !verification.Tradable {
		t.Fatalf("MCP verification = %+v", verification)
	}
	search := tradingE2EStructured[backend.TradingSymbolSearch](t, f.mcp("search_trading_symbols", map[string]any{"connection": id, "query": "AAPL"}, false))
	if !search.Supported || len(search.Matches) != 1 || search.Matches[0].Name != "Fake Apple" || search.Matches[0].Symbol != "AAPL" || !search.Matches[0].Tradable {
		t.Fatalf("MCP search = %+v", search)
	}
	tradingE2EAssertDestinations(t, tradingE2EStructured[tradingE2EDestinations](t, f.mcp("list_trading_destinations", map[string]any{"account": "desk", "mode": "paper"}, false)), id)
	tradingE2EAssertSnapshot(t, tradingE2EStructured[tradingE2ESnapshot](t, f.mcp("get_trading", map[string]any{}, false)), id)
	f.assertVenueReads()
}

func TestTradingRefusalsEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	live := tradingE2ECreateInput("Live", "live")
	f.rest(http.MethodPut, "/mcp-access/create_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
	// The agent is refused before its mode is read.
	f.mcpLoosening("create_trading_connection", live)
	f.createREST()
	second := tradingE2ECreateInput("Second", "paper")
	second["credentials"] = map[string]string{"apiKey": "second-key-marker", "apiSecret": "second-secret-marker"}
	errBody := tradingE2EDecode[struct{ Error struct{ Code string } }](t,
		f.rest(http.MethodPost, "/trading/connections", second, http.StatusConflict))
	if errBody.Error.Code != "conflict" {
		t.Fatalf("duplicate venue error = %s", errBody.Error.Code)
	}
	// The agent is refused before its venue account is compared.
	f.mcpLoosening("create_trading_connection", tradingE2ECreateInput("Third", "paper"))
	connections, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	if len(connections) != 1 {
		t.Fatalf("connections after the refused creates = %d, want the operator's one", len(connections))
	}
}

func TestTestConnectionSettingsValidationEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false, TestProvider())
	input := map[string]any{
		"provider": "test", "label": "Test desk", "mode": "live", "credentials": map[string]any{}, "enabled": false,
	}
	requireProblem := func(raw []byte, pointer, constraint string) {
		t.Helper()
		problem := tradingE2EDecode[httpx.ProblemDetails](t, raw)
		if problem.Status != http.StatusUnprocessableEntity || len(problem.Errors) != 1 ||
			problem.Errors[0].Code != "validation" || problem.Errors[0].Pointer != pointer || problem.Errors[0].Constraint != constraint {
			t.Fatalf("settings problem = %+v, want %s/%s", problem, pointer, constraint)
		}
	}
	requireProblem(f.rest(http.MethodPost, "/trading/connections", input, http.StatusUnprocessableEntity), "/mode", "enum")
	connections, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	if len(connections) != 0 {
		t.Fatal("invalid mode created a connection")
	}
	f.rest(http.MethodPut, "/mcp-access/create_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
	f.mcpLoosening("create_trading_connection", input)
	input["mode"] = "paper"
	created := tradingE2EDecode[tradingE2ECreated](t, f.rest(http.MethodPost, "/trading/connections", input, http.StatusCreated))
	id := created.Connection.ID
	before, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	credentials := map[string]any{"unexpected": "test-key-marker"}
	requireProblem(f.rest(http.MethodPut, "/trading/connections/"+id+"/settings", map[string]any{
		"label": "Must not save", "credentials": credentials,
	}, http.StatusUnprocessableEntity), "/credentials/unexpected", "forbidden")
	f.rest(http.MethodPut, "/mcp-access/update_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
	f.mcpLoosening("update_trading_connection", map[string]any{"connection": id, "credentials": credentials})
	after, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refused settings changed the saved connection")
	}
	for _, invalid := range []any{nil, []any{}, "", "{}", 1} {
		requireProblem(f.rest(http.MethodPut, "/trading/connections/"+id+"/settings", map[string]any{
			"label": "Must not save", "credentials": invalid,
		}, http.StatusUnprocessableEntity), "/credentials", "type")
	}
	for _, value := range []any{"", nil} {
		requireProblem(f.rest(http.MethodPut, "/trading/connections/"+id+"/settings", map[string]any{
			"label": "Must not save", "credentials": map[string]any{"unexpected": value},
		}, http.StatusUnprocessableEntity), "/credentials/unexpected", "forbidden")
	}
	after, err = f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid credential values changed the saved connection")
	}
	f.rest(http.MethodPut, "/trading/connections/"+id+"/settings", map[string]any{
		"label": "Empty credentials", "credentials": map[string]any{},
	}, http.StatusOK)
	updated, found, err := f.realm.GetTradingConnection(f.ctx, domain.ExternalID(id))
	tradingE2EMust(t, err)
	if !found || updated.Label != "Empty credentials" || updated.Credentials != before[0].Credentials || updated.VenueIdentity != before[0].VenueIdentity {
		t.Fatalf("empty-object settings = %+v, found=%t", updated, found)
	}
	f.mcp("update_trading_connection", map[string]any{
		"connection": id, "label": "Empty MCP credentials", "credentials": map[string]any{},
	}, false)
	f.mcp("update_trading_connection", map[string]any{
		"connection": id, "label": "Omitted MCP credentials",
	}, false)
	f.rest(http.MethodPut, "/trading/connections/"+id+"/settings", map[string]any{
		"label": "Renamed",
	}, http.StatusOK)
	updated, found, err = f.realm.GetTradingConnection(f.ctx, domain.ExternalID(id))
	tradingE2EMust(t, err)
	if !found || updated.Label != "Renamed" || updated.Credentials != "{}" {
		t.Fatalf("valid settings = %+v, found=%t", updated, found)
	}
}

func TestTradingBlankCredentialEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	blank := tradingE2ECreateInput("Paper", "paper")
	// A single-space value must not be redacted out of the refusal text.
	blank["credentials"] = map[string]string{"apiKey": " ", "apiSecret": "test-secret-marker"}
	problem := tradingE2EDecode[httpx.ProblemDetails](t, f.rest(http.MethodPost, "/trading/connections", blank, http.StatusUnprocessableEntity))
	if len(problem.Errors) != 1 || problem.Errors[0].Code != "validation" ||
		problem.Errors[0].Pointer != "/credentials/apiKey" || problem.Errors[0].Constraint != "required" ||
		!strings.Contains(problem.Detail, "apiKey is required") {
		t.Fatalf("blank key problem = %+v", problem)
	}
	f.rest(http.MethodPut, "/mcp-access/create_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
	// The agent is refused before its credentials are validated.
	f.mcpLoosening("create_trading_connection", blank)
	connections, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	if checks := f.accountChecks(); len(connections) != 0 || checks != 0 {
		t.Fatalf("blank credentials created %d connections and reached the venue %d times", len(connections), checks)
	}
}

func TestTradingMissingAssetEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	id := f.createREST()
	f.rest(http.MethodPost, "/assets", map[string]any{"code": "BTC"}, http.StatusCreated)
	path := "/trading/connections/" + id + "/instruments"
	hasAsset := func(code string) bool {
		assets, err := f.realm.ListAssets(f.ctx)
		tradingE2EMust(t, err)
		for _, asset := range assets {
			if asset.Code == code {
				return true
			}
		}
		return false
	}
	f.rest(http.MethodPut, "/mcp-access/set_trading_instrument", map[string]any{"enabled": true}, http.StatusOK)
	// The agent is refused before its assets are looked up.
	f.mcpLoosening("set_trading_instrument", map[string]any{"connection": id, "externalSymbol": "BTC/USDT", "baseAsset": "BTC", "quoteAsset": "USDT", "enabled": true})
	if hasAsset("USDT") {
		t.Fatal("refused instrument created its missing asset")
	}
	// An asset code Officer cannot accept is refused, and nothing is created:
	// not even the request's other, acceptable missing asset.
	unusable := map[string]any{"externalSymbol": "ETHUSDT", "baseAsset": "ETH", "quoteAsset": "US DT", "enabled": true}
	refused := tradingE2EDecode[struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}](t, f.rest(http.MethodPut, path, unusable, http.StatusUnprocessableEntity))
	if len(refused.Errors) == 0 || refused.Errors[0].Code != "validation" || hasAsset("ETH") {
		t.Fatalf("unusable asset error = %+v", refused.Errors)
	}
	// As a market-data instrument does, the operator's mapping creates USDT.
	instrument := map[string]any{"externalSymbol": "BTC/USDT", "baseAsset": "BTC", "quoteAsset": "USDT", "enabled": true}
	f.rest(http.MethodPut, path, instrument, http.StatusOK)
	if !hasAsset("USDT") {
		t.Fatal("instrument did not create its missing asset")
	}
}

func (f *tradingE2E) sendPaper(surface string, input map[string]any, status int, code string) []byte {
	f.t.Helper()
	return f.sendVenue(surface, domain.TradingModePaper, input, status, code)
}

// sendVenue sends over REST (POST /orders/send-<mode>) or MCP
// (send_<mode>_order) and returns the response body or structured content.
func (f *tradingE2E) sendVenue(surface string, mode domain.TradingMode, input map[string]any, status int, code string) []byte {
	f.t.Helper()
	if surface == "REST" {
		raw := f.rest(http.MethodPost, "/orders/send-"+string(mode), input, status)
		if code != "" {
			got := tradingE2EDecode[struct{ Error struct{ Code string } }](f.t, raw)
			if got.Error.Code != code {
				f.t.Fatalf("send error code = %s, want %s", got.Error.Code, code)
			}
		}
		return raw
	}
	result := f.mcp("send_"+string(mode)+"_order", input, status != http.StatusCreated)
	if status != http.StatusCreated {
		if code != "" {
			text, ok := result.Content[0].(*sdkmcp.TextContent)
			if !ok || !strings.HasPrefix(text.Text, code+": ") {
				f.t.Fatalf("send refusal did not carry %s", code)
			}
		}
		return nil
	}
	raw, err := json.Marshal(result.StructuredContent)
	tradingE2EMust(f.t, err)
	return raw
}

// setupSend has the operator create a connection, map AAPL and grant desk over
// REST, and returns the connection id. For the MCP surface it also enables
// send_paper_order, the call the agent then makes.
func (f *tradingE2E) setupSend(surface string) string {
	f.t.Helper()
	id := f.createREST()
	path := "/trading/connections/" + id
	f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "AAPL",
		"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
	f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
	if surface == "MCP" {
		f.rest(http.MethodPut, "/mcp-access/send_paper_order", map[string]any{"enabled": true}, http.StatusOK)
	}
	return id
}

func tradingE2ESendInput(connection string) map[string]any {
	return map[string]any{"account": "desk", "baseAsset": "AAPL", "quoteAsset": "USD",
		"side": "buy", "amountKind": "quantity", "amountValue": "10", "price": "2",
		"destination": map[string]any{"connection": connection,
			"route": map[string]any{"timeInForce": "day"}}}
}

func TestTradingSendPaperEndToEnd(t *testing.T) {
	for _, surface := range []string{"REST", "MCP"} {
		t.Run(surface, func(t *testing.T) {
			f := newTradingE2E(t, true)
			id := f.setupSend(surface)
			for _, account := range []string{"desk", "fees"} {
				f.rest(http.MethodPost, "/accounts/"+account+"/adjustments?missingAccount=reject",
					map[string]any{"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
						"balance": map[string]any{"mode": "absolute", "value": "1000"}}, http.StatusCreated)
			}
			f.rest(http.MethodPut, "/trading/connections/"+id+"/fee-account",
				map[string]any{"account": "fees"}, http.StatusOK)
			raw := f.sendPaper(surface, tradingE2ESendInput(id), http.StatusCreated, "")
			sent := tradingE2EDecode[struct {
				ID, Verdict, SignedApproval, KeyID string
				VenueSend                          struct {
					Status     string
					VenueOrder struct {
						Connection, ClientOrderID, VenueOrderID string
						SendAttempted                           bool
					}
				}
			}](t, raw)
			link := sent.VenueSend.VenueOrder
			if sent.ID == "" || sent.Verdict != "accept" || sent.VenueSend.Status != "acknowledged" ||
				link.Connection != id || link.ClientOrderID == "" || link.VenueOrderID != "venue-1" || !link.SendAttempted {
				t.Fatalf("paper send acknowledgement = %s", raw)
			}
			shape := tradingE2EDecode[map[string]json.RawMessage](t, raw)
			for _, field := range []string{"signedApproval", "keyId", "id", "verdict"} {
				if _, ok := shape[field]; !ok {
					t.Fatalf("missing approval field %s", field)
				}
			}
			if bytes.Contains(raw, []byte(`:null`)) || bytes.Contains(raw, []byte(`"error"`)) || bytes.Contains(raw, []byte(`"reasons"`)) {
				t.Fatalf("unexpected optional send fields: %s", raw)
			}
			f.venueMu.Lock()
			posts, stream := len(f.posts), f.stream
			post := f.posts[0]
			f.venueMu.Unlock()
			if posts != 1 || post["symbol"] != "AAPL" || post["qty"] != "10" || post["limit_price"] != "2" ||
				post["time_in_force"] != "day" || post["client_order_id"] != link.ClientOrderID {
				t.Fatalf("venue order = %+v", post)
			}
			frame := strings.NewReplacer(`"venue"`, `"venue-1"`,
				`"client"`, fmt.Sprintf("%q", link.ClientOrderID),
				`"3.000000000000000001"`, `"10"`).Replace(updateFrame("fill", "10", "10", "2"))
			if !sendFrame(t, f.ctx, stream, websocket.MessageBinary, frame) {
				t.Fatal("fill stream failed")
			}
			polltest.Until(func() bool {
				detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(sent.ID))
				tradingE2EMust(t, err)
				return detail.Order.Status == domain.OrderStatusFilled
			}) // paper order filled
			detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(sent.ID))
			tradingE2EMust(t, err)
			wantSource := domain.SourceAPI
			if surface == "MCP" {
				wantSource = domain.SourceMCP
			}
			if detail.Order.Source != wantSource {
				t.Fatal("send lost its caller source")
			}
			if len(detail.Trades) != 1 || detail.Trades[0].Quantity != "10" || detail.Trades[0].Price != "2" {
				t.Fatalf("filled order trades = %+v", detail.Trades)
			}
			f.rest(http.MethodGet, "/orders/"+sent.ID, nil, http.StatusOK)
			rejected := tradingE2ESendInput(id)
			rejected["id"], rejected["amountValue"] = "risk-reject", "10000"
			rejectRaw := f.sendPaper(surface, rejected, http.StatusCreated, "")
			rejectShape := tradingE2EDecode[map[string]json.RawMessage](t, rejectRaw)
			if string(rejectShape["verdict"]) != `"reject"` || len(rejectShape["reasons"]) == 0 || rejectShape["venueSend"] != nil {
				t.Fatalf("risk reject = %s", rejectRaw)
			}
			f.venueMu.Lock()
			posts = len(f.posts)
			f.venueMu.Unlock()
			if posts != 1 {
				t.Fatal("risk reject reached venue")
			}
			f.close()
			f.venueMu.Lock()
			f.fees = []map[string]string{{"id": "sent-order-fee", "activity_type": "FEE",
				"status": "executed", "currency": "USD", "net_amount": "-0.25",
				"date": time.Now().UTC().Format("2006-01-02")}}
			f.venueMu.Unlock()
			f.start()
			polltest.Until(func() bool {
				rows, err := f.realm.ListAdjustments(f.ctx, "fees", domain.SourceSystem, 10)
				tradingE2EMust(t, err)
				return len(rows) == 1
			}) // sent-order fee booked after restart
			fees, err := f.realm.ListAdjustments(f.ctx, "fees", domain.SourceSystem, 10)
			tradingE2EMust(t, err)
			if fees[0].Accepted == nil || fees[0].Rejected != nil ||
				fees[0].ExternalID != domain.ExternalID("trading:"+id+":fee:sent-order-fee") ||
				fees[0].Request.Balance == nil || fees[0].Request.Balance.Value != "-0.25" {
				t.Fatal("FEE was not booked to the configured account")
			}
			balance, found, err := f.realm.GetBalance(f.ctx, "fees", "USD")
			tradingE2EMust(t, err)
			if !found || !decimal.RequireFromString(balance.Available).Equal(decimal.RequireFromString("999.75")) {
				t.Fatalf("fee account balance = %+v", balance)
			}
			rows, err := f.realm.ListAdjustments(f.ctx, "desk", domain.SourceSystem, 10)
			tradingE2EMust(t, err)
			if len(rows) != 0 {
				t.Fatal("unlinked fee reached trading account")
			}
			f.rest(http.MethodGet, "/audit?limit=500", nil, http.StatusOK)
			f.close()
			tradingE2ENoSecrets(t, "paper send logs", []byte(f.logs.String()))
		})
	}
}

type retryTestConnector struct {
	*testConnector
	t        *testing.T
	released chan fwtrading.Snapshot
}

func (c *retryTestConnector) ReleaseOrder(id string) {
	snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: id})
	if err == nil && found {
		c.released <- snapshot
	}
	c.testConnector.ReleaseOrder(id)
}

func (c *retryTestConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	ack, err := c.testConnector.Send(ctx, order)
	var refusal *fwtrading.TestExecutionRefusal
	if errors.As(err, &refusal) && refusal.Code == "test_policy_even_rejected" {
		// Retry before the worker persists terminal and releases the book entry.
		retryAck, retryErr := c.testConnector.Send(ctx, order)
		var retryRefusal *fwtrading.TestExecutionRefusal
		if !errors.As(retryErr, &retryRefusal) || !reflect.DeepEqual(refusal, retryRefusal) || ack != retryAck {
			c.t.Errorf("refused connector retry changed outcome: %v -> %v", err, retryErr)
		}
	}
	return ack, err
}

func TestTestExecutionPipelineEndToEnd(t *testing.T) {
	for _, surface := range []string{"REST", "MCP"} {
		t.Run(surface, func(t *testing.T) {
			quotes := &testQuotes{}
			clock := newTestRestClock()
			clock.set(time.Now())
			var connector fwtrading.Connector
			provider := TestProvider()
			provider.Build = func(build fwtrading.BuildContext) (fwtrading.Connector, error) {
				build.Quotes = quotes
				build.Schedule = clock.schedule
				c, err := newTestConnector(build, clock.now)
				if err != nil {
					return nil, err
				}
				c = &retryTestConnector{testConnector: c.(*testConnector), t: t, released: make(chan fwtrading.Snapshot, 64)}
				if !build.Connection.ExternalID.IsZero() {
					connector = c
				}
				return c, err
			}
			f := newTradingE2E(t, true, provider)
			created := tradingE2EDecode[tradingE2ECreated](t, f.rest(http.MethodPost, "/trading/connections", map[string]any{
				"provider": "test", "label": "Test desk", "mode": "paper", "credentials": map[string]any{}, "enabled": true,
			}, http.StatusCreated))
			id := created.Connection.ID
			if !strings.HasPrefix(created.Connection.VenueIdentity, "test:") {
				t.Fatalf("local identity = %s", created.Connection.VenueIdentity)
			}
			path := "/trading/connections/" + id
			f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "opaque-route-id",
				"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
			f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
			f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject", map[string]any{
				"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
				"balance": map[string]any{"mode": "absolute", "value": "1000"},
			}, http.StatusCreated)
			f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject", map[string]any{
				"type": "correction", "asset": "AAPL", "averageEntryPrice": "100", "realizedPnl": "0",
				"balance": map[string]any{"mode": "absolute", "value": "0"},
			}, http.StatusCreated)
			if surface == "MCP" {
				f.rest(http.MethodPut, "/mcp-access/send_paper_order", map[string]any{"enabled": true}, http.StatusOK)
			}
			setRules := func(rules []any) {
				f.rest(http.MethodPut, path+"/test-policy", map[string]any{"rules": rules}, http.StatusOK)
			}
			execute := []any{map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
				"mode": "execute", "priceVariant": "normal"}}
			setRules(execute)
			base, found, err := f.realm.GetAsset(f.ctx, "AAPL")
			tradingE2EMust(t, err)
			if !found {
				t.Fatal("base asset missing")
			}
			quote, found, err := f.realm.GetAsset(f.ctx, "USD")
			tradingE2EMust(t, err)
			if !found {
				t.Fatal("quote asset missing")
			}
			now := time.Now()
			quotes.snapshots = []marketdata.QuoteSnapshot{{BaseAssetID: base.EngineAssetID, QuoteAssetID: quote.EngineAssetID,
				MarketDataQuote: domain.MarketDataQuote{Instance: "source", ExternalSymbol: "AAPL", AsOf: now,
					ReceivedAt: now, Mark: "100", Bid: "99", Ask: "101"}}}
			tradingE2EMust(t, f.core.MarketDataSink().Push(marketdata.QuoteUpdate{
				Base: base.EngineAssetID, Quote: quote.EngineAssetID, AsOf: now, Mark: "100", Bid: "99", Ask: "101"}))
			type sendResponse struct {
				ID, Verdict, SignedApproval, KeyID string
				VenueSend                          struct {
					Status     string
					Refusal    *fwtrading.TestExecutionRefusal
					VenueOrder struct{ ClientOrderID, VenueOrderID string }
				}
			}
			send := func(orderID, price, side, amount string) sendResponse {
				input := map[string]any{"id": orderID, "account": "desk", "baseAsset": "AAPL", "quoteAsset": "USD",
					"side": side, "amountKind": "quantity", "amountValue": amount,
					"destination": map[string]any{"connection": id, "route": map[string]any{}}}
				if price != "" {
					input["price"] = price
				}
				return tradingE2EDecode[sendResponse](t, f.sendPaper(surface, input, http.StatusCreated, ""))
			}
			for _, tc := range []struct{ id, limit, price, side string }{
				{"test-limit", "105", "105", "buy"}, {"test-market", "", "101", "buy"},
				{"test-realized-pnl", "110", "110", "sell"},
			} {
				result := send(tc.id, tc.limit, tc.side, "1")
				link := result.VenueSend.VenueOrder
				if result.Verdict != "accept" || result.SignedApproval == "" ||
					result.VenueSend.Status != "acknowledged" || result.VenueSend.Refusal != nil ||
					link.VenueOrderID != "test:"+id+":"+link.ClientOrderID {
					t.Fatalf("send acknowledgement = %+v", result)
				}
				polltest.Until(func() bool {
					detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(result.ID))
					tradingE2EMust(t, err)
					return detail.Order.Status == domain.OrderStatusFilled
				}) // test order filled through sink
				polltest.Until(func() bool {
					_, found, err := connector.Lookup(f.ctx, fwtrading.OrderRef{ClientOrderID: link.ClientOrderID})
					requireTestBookLookup(t, found, err)
					return !found
				}) // test book released
				detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(result.ID))
				tradingE2EMust(t, err)
				if len(detail.Trades) != 1 || detail.Trades[0].Quantity != "1" || detail.Trades[0].Price != tc.price {
					t.Fatalf("pipeline trade = %+v, want px %s", detail.Trades, tc.price)
				}
				wantSource := domain.SourceAPI
				if surface == "MCP" {
					wantSource = domain.SourceMCP
				}
				if detail.Order.Source != wantSource {
					t.Fatal("order lost transport source")
				}
				raw := f.rest(http.MethodGet, "/orders/"+result.ID, nil, http.StatusOK)
				if !bytes.Contains(raw, []byte(`"filled"`)) || !bytes.Contains(raw, []byte(`"price":"`+tc.price+`"`)) {
					t.Fatalf("GET order lost fill: %s", raw)
				}
			}
			for _, asset := range []struct{ code, available, average, pnl string }{{"USD", "904", "1", "0"}, {"AAPL", "1", "103", "7"}} {
				balance, found, err := f.realm.GetBalance(f.ctx, "desk", asset.code)
				tradingE2EMust(t, err)
				if !found || balance.RealizedPnl == "" || balance.AverageEntryPrice == "" {
					t.Fatalf("engine balance/P&L %s is incomplete: %+v", asset.code, balance)
				}
				if !found || !decimal.RequireFromString(balance.Available).Equal(decimal.RequireFromString(asset.available)) ||
					!decimal.RequireFromString(balance.Held).IsZero() || !decimal.RequireFromString(balance.RealizedPnl).Equal(decimal.RequireFromString(asset.pnl)) ||
					!decimal.RequireFromString(balance.AverageEntryPrice).Equal(decimal.RequireFromString(asset.average)) {
					t.Fatalf("engine balance/P&L %s = %+v", asset.code, balance)
				}
			}
			trades := f.rest(http.MethodGet, "/trades?account=desk", nil, http.StatusOK)
			if !bytes.Contains(trades, []byte(`"price":"105"`)) || !bytes.Contains(trades, []byte(`"price":"101"`)) {
				t.Fatalf("GET trades = %s", trades)
			}
			positions := tradingE2EDecode[struct {
				Balances []struct{ Asset, Available, Held, AverageEntryPrice, RealizedPnl string }
			}](t, f.rest(http.MethodGet, "/balances?account=desk", nil, http.StatusOK))
			foundPosition := false
			for _, position := range positions.Balances {
				if position.Asset == "AAPL" {
					foundPosition = true
					if position.Available != "1" || position.Held != "0" || position.AverageEntryPrice != "103" || position.RealizedPnl != "7" {
						t.Fatalf("GET position = %+v", position)
					}
				}
			}
			if !foundPosition {
				t.Fatal("GET balances omitted the position")
			}
			setRules([]any{map[string]any{
				"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
				"mode": "split", "fills": 3, "priceVariant": "normal",
			}})
			for _, amount := range []string{"1.00", "1", "0.02"} {
				result := send("test-split-"+amount, "100", "buy", amount)
				if amount != "1.00" {
					refusal := result.VenueSend.Refusal
					if result.VenueSend.Status != "refused" || refusal == nil ||
						refusal.Code != "test_split_unrepresentable" {
						t.Fatalf("split %s refusal = %+v", amount, result)
					}
					detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(result.ID))
					tradingE2EMust(t, err)
					if len(detail.Trades) != 0 || detail.Order.Status != domain.OrderStatusCancelled {
						t.Fatalf("refused split emitted trades: %+v", detail)
					}
					continue
				}
				if result.VenueSend.Status != "acknowledged" || result.VenueSend.Refusal != nil {
					t.Fatalf("split acknowledgement = %+v", result)
				}
				polltest.Until(func() bool {
					detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(result.ID))
					tradingE2EMust(t, err)
					return detail.Order.Status == domain.OrderStatusFilled
				}) // split filled through sink
				detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(result.ID))
				tradingE2EMust(t, err)
				if detail.Order.AmountValue != amount || len(detail.Trades) != 3 {
					t.Fatalf("split lost precision or trades: %+v", detail)
				}
				quantities := make(map[string]int)
				for _, trade := range detail.Trades {
					quantities[trade.Quantity]++
					if trade.Price != "100" || trade.Commission != nil {
						t.Fatalf("split trade = %+v", trade)
					}
				}
				if !reflect.DeepEqual(quantities, map[string]int{"0.34": 1, "0.33": 2}) {
					t.Fatalf("split quantities = %v", quantities)
				}
				reports := map[domain.ExternalID]*domain.ExecutionReportRequest{}
				for _, event := range detail.Events {
					if report := event.Payload.ExecutionReport; report != nil && report.FillQuantity != "" {
						reports[report.ExternalID] = report
					}
				}
				want := map[string]struct{ quantity, status string }{
					"0.66": {"0.34", "partially_filled"}, "0.33": {"0.33", "partially_filled"},
					"0": {"0.33", "filled"},
				}
				if len(reports) != len(want) {
					t.Fatalf("split reports = %+v", reports)
				}
				for _, report := range reports {
					expected, ok := want[report.LeavesQuantity]
					if !ok || report.FillQuantity != expected.quantity || string(report.OrderStatus) != expected.status {
						t.Fatalf("split report = %+v", report)
					}
				}
			}
			setRules([]any{map[string]any{
				"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
				"mode": "reject_even", "priceVariant": "normal",
			}})
			for i := range 2 {
				result := send(fmt.Sprintf("test-reject-even-%d", i), "100", "buy", "1")
				if i == 0 {
					if result.VenueSend.Status != "acknowledged" || result.VenueSend.Refusal != nil {
						t.Fatalf("odd acknowledgement = %+v", result)
					}
					polltest.Until(func() bool {
						detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(result.ID))
						tradingE2EMust(t, err)
						return detail.Order.Status == domain.OrderStatusFilled
					}) // odd order filled
				} else {
					refusal := result.VenueSend.Refusal
					if result.VenueSend.Status != "refused" || refusal == nil ||
						refusal.Code != "test_policy_even_rejected" ||
						refusal.Message != "Test policy rejected every second order" {
						t.Fatalf("even refusal = %+v", result)
					}
					detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(result.ID))
					tradingE2EMust(t, err)
					if len(detail.Trades) != 0 || detail.Order.Status != domain.OrderStatusCancelled {
						t.Fatalf("even refusal emitted trades: %+v", detail)
					}
				}
			}
			setRules([]any{map[string]any{
				"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
				"mode": "rest", "priceVariant": "normal", "wait": map[string]any{"kind": "finite", "seconds": 10},
			}})
			result := send("test-rest-no-ask", "", "buy", "1")
			if result.VenueSend.Status != "acknowledged" || result.VenueSend.Refusal != nil {
				t.Fatalf("finite rest Ack = %+v", result)
			}
			timer := clock.next(t)
			quotes.snapshots[0].Ask = ""
			quotes.snapshots[0].AsOf = clock.now().Add(10 * time.Second)
			clock.set(clock.now().Add(10 * time.Second))
			timer.ticks <- clock.now()
			polltest.Until(func() bool {
				detail, err := f.realm.GetOrder(f.ctx, "test-rest-no-ask")
				tradingE2EMust(t, err)
				return detail.Order.Status == domain.OrderStatusCancelled
			}) // finite rest without ask cancelled
			detail, err := f.realm.GetOrder(f.ctx, "test-rest-no-ask")
			tradingE2EMust(t, err)
			if len(detail.Trades) != 0 {
				t.Fatalf("delayed refusal invented trades: %+v", detail.Trades)
			}
			var delayed fwtrading.Snapshot
			for delayed.VenueOrderID != result.VenueSend.VenueOrder.VenueOrderID {
				delayed = <-connector.(*retryTestConnector).released // Snapshot released.
			}
			if delayed.Status != fwtrading.VenueStatusCancelled || len(delayed.Fills) != 0 || !strings.Contains(delayed.Reason, "has no ask") {
				t.Fatalf("delayed snapshot = %+v", delayed)
			}
			diagnostics := f.health().Diagnostics
			if len(diagnostics) != 1 || diagnostics[0].Code != "test_quote_side_missing" || diagnostics[0].Detail != delayed.Reason {
				t.Fatalf("delayed diagnostic = %+v", diagnostics)
			}
			history := f.rest(http.MethodGet, "/orders/test-rest-no-ask", nil, http.StatusOK)
			if bytes.Contains(history, []byte(delayed.Reason)) || bytes.Contains(history, []byte("test_quote_side_missing")) || bytes.Contains(history, []byte(`"reason"`)) {
				t.Fatalf("delayed reason leaked into history: %s", history)
			}
			for _, code := range []string{"test_policy_no_match", "test_quote_side_missing"} {
				if code == "test_policy_no_match" {
					setRules([]any{})
				} else {
					setRules(execute)
					quotes.snapshots[0].Ask = ""
				}
				result := send(code, "", "buy", "1")
				refusal := result.VenueSend.Refusal
				if result.VenueSend.Status != "refused" || refusal == nil || refusal.Code != code ||
					refusal.BaseAsset != "AAPL" || refusal.QuoteAsset != "USD" || refusal.RuleIndexes == nil {
					t.Fatalf("typed refusal = %+v", result)
				}
				detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(result.ID))
				tradingE2EMust(t, err)
				if detail.Order.Status != domain.OrderStatusCancelled || len(detail.Trades) != 0 {
					t.Fatalf("refusal invented a trade: %+v", detail)
				}
			}
			input := map[string]any{"id": "test-volume", "account": "desk", "baseAsset": "AAPL", "quoteAsset": "USD",
				"side": "buy", "amountKind": "volume", "amountValue": "10",
				"destination": map[string]any{"connection": id, "route": map[string]any{}}}
			code := ""
			if surface == "MCP" {
				code = "validation"
			}
			raw := f.sendPaper(surface, input, http.StatusUnprocessableEntity, code)
			if surface == "REST" && !bytes.Contains(raw, []byte(`"pointer":"/amountKind"`)) {
				t.Fatalf("volume pointer = %s", raw)
			}
			if _, err := f.realm.GetOrder(f.ctx, "test-volume"); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("volume created an order: %v", err)
			}
		})
	}
}

type quietTestConnector struct {
	*testConnector
	connected chan<- struct{}
	partial   bool
}

func (c *quietTestConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	ack, err := c.testConnector.Send(ctx, order)
	if err == nil && c.partial {
		c.mu.Lock()
		entry := c.book[order.ClientOrderID]
		entry.snapshot.Fills = entry.snapshot.Fills[:1]
		entry.snapshot.Fills[0].Final = false
		entry.snapshot.FilledQuantity = entry.snapshot.Fills[0].CumQuantity
		entry.snapshot.Status = fwtrading.VenueStatusOpen
		entry.state = "open"
		c.mu.Unlock()
	}
	return ack, err
}

func (c *quietTestConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	in, err := c.testConnector.Subscribe(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan fwtrading.Event)
	go func() {
		defer close(out)
		for event := range in {
			if event.Kind != fwtrading.EventConnected && !c.partial {
				continue
			}
			select {
			case out <- event:
				if event.Kind == fwtrading.EventConnected {
					close(c.connected)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func TestTestExecutionLostBookDisabledEndToEnd(t *testing.T) {
	for _, mode := range []string{"infinite", "split"} {
		t.Run(mode, func(t *testing.T) {
			connected := make(chan struct{})
			provider := TestProvider()
			provider.Build = func(build fwtrading.BuildContext) (fwtrading.Connector, error) {
				c, err := newTestConnector(build, time.Now)
				if err != nil {
					return nil, err
				}
				if build.Connection.Enabled {
					return &quietTestConnector{testConnector: c.(*testConnector), connected: connected, partial: mode == "split"}, nil
				}
				return c, nil
			}
			f := newTradingE2E(t, true, provider)
			created := tradingE2EDecode[tradingE2ECreated](t, f.rest(http.MethodPost, "/trading/connections", map[string]any{
				"provider": "test", "label": "Restart test", "mode": "paper", "credentials": map[string]any{}, "enabled": true,
			}, http.StatusCreated))
			<-connected // Test source connected.
			id := created.Connection.ID
			path := "/trading/connections/" + id
			f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "route-id",
				"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
			f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
			rule := map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"}, "mode": "rest",
				"wait": map[string]any{"kind": "infinite"}}
			if mode == "split" {
				rule = map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
					"mode": "split", "fills": 3, "priceVariant": "normal"}
			}
			f.rest(http.MethodPut, path+"/test-policy", map[string]any{"rules": []any{rule}}, http.StatusOK)
			f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject", map[string]any{
				"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
				"balance": map[string]any{"mode": "absolute", "value": "1000"},
			}, http.StatusCreated)
			input := tradingE2ESendInput(id)
			input["id"], input["amountValue"] = "lost-book", "1.00"
			input["destination"].(map[string]any)["route"] = map[string]any{}
			f.sendPaper("REST", input, http.StatusCreated, "")
			wantTrades := 0
			if mode == "split" {
				wantTrades = 1
			}
			polltest.Until(func() bool {
				detail, err := f.realm.GetOrder(f.ctx, "lost-book")
				tradingE2EMust(t, err)
				return len(detail.Trades) == wantTrades
			}) // initial partial execution applied
			detail, err := f.realm.GetOrder(f.ctx, "lost-book")
			tradingE2EMust(t, err)
			before := detail.Trades
			link, found, err := f.realm.GetVenueOrder(f.ctx, "lost-book")
			tradingE2EMust(t, err)
			if domain.OrderStatusTerminal(detail.Order.Status) || len(before) != wantTrades || !found || link.VenueOrderID == "" {
				t.Fatalf("lost-book setup = %+v", detail)
			}
			if mode == "split" && (before[0].Quantity != "0.34" || before[0].Price != "2") {
				t.Fatalf("partial split = %+v", before)
			}
			tradingE2EMust(t, f.realm.SetTradingConnectionEnabled(f.ctx, domain.ExternalID(id), false))
			f.close()
			f.start()
			polltest.Until(func() bool {
				detail, err := f.realm.GetOrder(f.ctx, "lost-book")
				tradingE2EMust(t, err)
				return detail.Order.Status == domain.OrderStatusCancelled
			}) // disabled lost-book remainder cancelled
			detail, err = f.realm.GetOrder(f.ctx, "lost-book")
			tradingE2EMust(t, err)
			if !reflect.DeepEqual(detail.Trades, before) {
				t.Fatalf("book loss changed earlier trades: %+v -> %+v", before, detail.Trades)
			}
			open, err := f.realm.ListOpenVenueOrders(f.ctx, domain.ExternalID(id))
			tradingE2EMust(t, err)
			if len(open) != 0 {
				t.Fatal("disabled lost-book link remained open")
			}
		})
	}
}

// TestTradingSendLiveEndToEnd creates a live connection over REST and sends
// through it on both surfaces against the fake venue: the paper pipeline
// through a live connection, which the paper send refuses.
func newTestExecutionScenario(t *testing.T) (*tradingE2E, string, *testQuotes, *testRestClock) {
	t.Helper()
	quotes := &testQuotes{}
	clock := newTestRestClock()
	clock.set(time.Now())
	provider := TestProvider()
	provider.Build = func(build fwtrading.BuildContext) (fwtrading.Connector, error) {
		build.Quotes = quotes
		build.Schedule = clock.schedule
		return newTestConnector(build, clock.now)
	}
	f := newTradingE2E(t, true, provider)
	created := tradingE2EDecode[tradingE2ECreated](t, f.rest(http.MethodPost, "/trading/connections", map[string]any{
		"provider": "test", "label": "Public scenario", "mode": "paper", "credentials": map[string]any{}, "enabled": true,
	}, http.StatusCreated))
	id := created.Connection.ID
	path := "/trading/connections/" + id
	f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "opaque-route-id",
		"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
	f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
	f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject", map[string]any{
		"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
		"balance": map[string]any{"mode": "absolute", "value": "1000"},
	}, http.StatusCreated)
	f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject", map[string]any{
		"type": "correction", "asset": "AAPL", "averageEntryPrice": "100", "realizedPnl": "0",
		"balance": map[string]any{"mode": "absolute", "value": "0"},
	}, http.StatusCreated)
	base, found, err := f.realm.GetAsset(f.ctx, "AAPL")
	tradingE2EMust(t, err)
	if !found {
		t.Fatal("base asset missing")
	}
	quote, found, err := f.realm.GetAsset(f.ctx, "USD")
	tradingE2EMust(t, err)
	if !found {
		t.Fatal("quote asset missing")
	}
	now := clock.now()
	quotes.snapshots = []marketdata.QuoteSnapshot{{BaseAssetID: base.EngineAssetID, QuoteAssetID: quote.EngineAssetID,
		MarketDataQuote: domain.MarketDataQuote{Instance: "source", ExternalSymbol: "AAPL", AsOf: now,
			ReceivedAt: now, Mark: "100", Bid: "99", Ask: "101"}}}
	tradingE2EMust(t, f.core.MarketDataSink().Push(marketdata.QuoteUpdate{
		Base: base.EngineAssetID, Quote: quote.EngineAssetID, AsOf: now, Mark: "100", Bid: "99", Ask: "101"}))
	return f, id, quotes, clock
}

type testExecutionE2EOrder struct {
	Order  struct{ Status, Source string }
	Trades []struct{ ID, Quantity, Price string }
}

func (f *tradingE2E) testExecutionOrder(id string) testExecutionE2EOrder {
	f.t.Helper()
	return tradingE2EDecode[testExecutionE2EOrder](f.t, f.rest(http.MethodGet, "/orders/"+id, nil, http.StatusOK))
}

func (f *tradingE2E) assertTestExecutionFill(id, price string, quantities map[string]int) {
	f.t.Helper()
	polltest.Until(func() bool {
		return f.testExecutionOrder(id).Order.Status == "filled"
	}) // public order filled
	detail := f.testExecutionOrder(id)
	got := make(map[string]int)
	listed := tradingE2EDecode[struct {
		Trades []struct{ ID, Quantity, Price string }
	}](f.t,
		f.rest(http.MethodGet, "/trades?account=desk", nil, http.StatusOK))
	for _, trade := range detail.Trades {
		got[trade.Quantity]++
		if trade.Price != price || trade.ID == "" {
			f.t.Fatalf("GET order trade = %+v, want price %s", trade, price)
		}
		found := false
		for _, row := range listed.Trades {
			if row.ID == trade.ID {
				found = true
				if row.Quantity != trade.Quantity || row.Price != trade.Price {
					f.t.Fatalf("GET trades row = %+v, order trade = %+v", row, trade)
				}
			}
		}
		if !found {
			f.t.Fatalf("GET trades omitted order trade %+v", trade)
		}
	}
	if !reflect.DeepEqual(got, quantities) {
		f.t.Fatalf("GET order fill quantities = %v, want %v", got, quantities)
	}
}

func (f *tradingE2E) assertTestExecutionPolicy(id, revision string, rules []any) {
	f.t.Helper()
	raw, err := json.Marshal(map[string]any{"connection": id, "revision": revision, "rules": rules})
	tradingE2EMust(f.t, err)
	want := tradingE2EDecode[map[string]any](f.t, raw)
	rest := tradingE2EDecode[map[string]any](f.t,
		f.rest(http.MethodGet, "/trading/connections/"+id+"/test-policy", nil, http.StatusOK))
	mcp := tradingE2EStructured[map[string]any](f.t,
		f.mcp("get_test_execution_policy", map[string]any{"connection": id}, false))
	if !reflect.DeepEqual(rest, want) || !reflect.DeepEqual(mcp, want) {
		f.t.Fatalf("policy state: REST=%v MCP=%v, want %v", rest, mcp, want)
	}
}

func (f *tradingE2E) sendTestExecution(surface, connection, id, price, side, amount string) []byte {
	f.t.Helper()
	input := tradingE2ESendInput(connection)
	input["id"], input["side"], input["amountValue"] = id, side, amount
	input["destination"].(map[string]any)["route"] = map[string]any{}
	if price == "" {
		delete(input, "price")
	} else {
		input["price"] = price
	}
	return f.sendPaper(surface, input, http.StatusCreated, "")
}

func TestTestExecutionPublicPolicyEndToEnd(t *testing.T) {
	f, id, _, _ := newTestExecutionScenario(t)
	f.assertTestExecutionPolicy(id, "1", []any{})
	path := "/trading/connections/" + id + "/test-policy"
	rule := map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
		"mode": "execute", "priceVariant": "normal"}
	rules := []any{rule}
	f.rest(http.MethodPut, path, map[string]any{"rules": rules}, http.StatusOK)
	f.assertTestExecutionPolicy(id, "2", rules)
	f.mcp("set_test_execution_policy", map[string]any{"connection": id, "policy": map[string]any{"rules": rules}}, true)
	f.assertTestExecutionPolicy(id, "2", rules)
	for _, command := range []string{"set_test_execution_policy", "send_paper_order"} {
		f.rest(http.MethodPut, "/mcp-access/"+command, map[string]any{"enabled": true}, http.StatusOK)
	}
	for i, tc := range []struct{ variant, buy, sell, dollars, pnl string }{
		{"worst", "101", "99", "998", "-2"},
		{"best", "99", "101", "1000", "0"},
		{"normal", "101", "99", "998", "-2"},
	} {
		t.Logf("price variant %s", tc.variant)
		rule["priceVariant"] = tc.variant
		result := f.mcp("set_test_execution_policy", map[string]any{"connection": id,
			"policy": map[string]any{"rules": rules}}, false)
		state := tradingE2EStructured[struct{ Connection, Revision string }](t, result)
		if state.Connection != id || state.Revision != fmt.Sprint(i+3) {
			t.Fatalf("MCP replacement state = %+v", state)
		}
		f.assertTestExecutionPolicy(id, fmt.Sprint(i+3), rules)
		for _, order := range []struct{ side, price string }{{"buy", tc.buy}, {"sell", tc.sell}} {
			orderID := tc.variant + "-" + order.side
			ack := tradingE2EDecode[struct{ VenueSend struct{ Status string } }](t,
				f.sendTestExecution("MCP", id, orderID, "", order.side, "1"))
			if ack.VenueSend.Status != "acknowledged" {
				t.Fatalf("MCP send = %+v", ack)
			}
			f.assertTestExecutionFill(orderID, order.price, map[string]int{"1": 1})
			if source := f.testExecutionOrder(orderID).Order.Source; source != string(domain.SourceMCP) {
				t.Fatalf("GET order source = %s", source)
			}
		}
		positions := tradingE2EDecode[struct {
			Balances []struct{ Asset, Available, Held, RealizedPnl string }
		}](t, f.rest(http.MethodGet, "/balances?account=desk", nil, http.StatusOK))
		found := map[string]bool{}
		for _, position := range positions.Balances {
			if position.Asset != "USD" && position.Asset != "AAPL" {
				continue
			}
			found[position.Asset] = true
			available, pnl := tc.dollars, "0"
			if position.Asset == "AAPL" {
				available, pnl = "0", tc.pnl
			}
			if !decimal.RequireFromString(position.Available).Equal(decimal.RequireFromString(available)) ||
				!decimal.RequireFromString(position.Held).IsZero() ||
				!decimal.RequireFromString(position.RealizedPnl).Equal(decimal.RequireFromString(pnl)) {
				t.Fatalf("GET balance = %+v, want available %s pnl %s", position, available, pnl)
			}
			balance, ok, err := f.realm.GetBalance(f.ctx, "desk", position.Asset)
			tradingE2EMust(t, err)
			if !ok || !decimal.RequireFromString(balance.Available).Equal(decimal.RequireFromString(available)) ||
				!decimal.RequireFromString(balance.Held).IsZero() ||
				!decimal.RequireFromString(balance.RealizedPnl).Equal(decimal.RequireFromString(pnl)) {
				t.Fatalf("engine balance = %+v, found %t", balance, ok)
			}
		}
		if !found["USD"] || found["AAPL"] != (tc.pnl != "0") {
			t.Fatalf("GET balances assets = %+v, want AAPL only for nonzero P&L", positions)
		}
	}
	f.mcp("set_test_execution_policy", map[string]any{"connection": id, "policy": map[string]any{"rules": rules}}, false)
	f.assertTestExecutionPolicy(id, "6", rules)
}

func TestTestExecutionMissingAskLimitEndToEnd(t *testing.T) {
	for _, surface := range []string{"REST", "MCP"} {
		t.Run(surface, func(t *testing.T) {
			f, id, quotes, _ := newTestExecutionScenario(t)
			f.rest(http.MethodPut, "/trading/connections/"+id+"/test-policy", map[string]any{"rules": []any{
				map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
					"mode": "execute", "priceVariant": "normal"},
			}}, http.StatusOK)
			if surface == "MCP" {
				f.rest(http.MethodPut, "/mcp-access/send_paper_order", map[string]any{"enabled": true}, http.StatusOK)
			}
			quotes.snapshots[0].Ask = ""
			f.sendTestExecution(surface, id, "limit-without-ask", "105", "buy", "1")
			f.assertTestExecutionFill("limit-without-ask", "105", map[string]int{"1": 1})
		})
	}
}

func TestTestExecutionRestDeadlineEndToEnd(t *testing.T) {
	f, id, quotes, clock := newTestExecutionScenario(t)
	f.rest(http.MethodPut, "/trading/connections/"+id+"/test-policy", map[string]any{"rules": []any{
		map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"}, "mode": "rest",
			"priceVariant": "normal", "wait": map[string]any{"kind": "finite", "seconds": 10}},
	}}, http.StatusOK)
	f.sendTestExecution("REST", id, "finite-deadline", "", "buy", "1")
	timer := clock.next(t)
	if timer.duration != 10*time.Second {
		t.Fatalf("rest timer = %s, want 10s", timer.duration)
	}
	assertOpen := func() {
		t.Helper()
		detail := f.testExecutionOrder("finite-deadline")
		if detail.Order.Status == "filled" || detail.Order.Status == "cancelled" || len(detail.Trades) != 0 {
			t.Fatalf("rest executed before deadline: %+v", detail)
		}
	}
	assertOpen()
	clock.set(clock.now().Add(9 * time.Second))
	timer.ticks <- clock.now()
	timer = clock.next(t)
	if timer.duration != time.Second {
		t.Fatalf("remaining rest timer = %s, want 1s", timer.duration)
	}
	assertOpen()
	quotes.snapshots[0].Ask = "103"
	quotes.snapshots[0].AsOf = clock.now().Add(time.Second)
	clock.set(clock.now().Add(time.Second))
	timer.ticks <- clock.now()
	f.assertTestExecutionFill("finite-deadline", "103", map[string]int{"1": 1})
}

func TestTestExecutionInfiniteRestartEndToEnd(t *testing.T) {
	f, id, _, clock := newTestExecutionScenario(t)
	rules := []any{map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
		"mode": "rest", "wait": map[string]any{"kind": "infinite"}}}
	f.rest(http.MethodPut, "/trading/connections/"+id+"/test-policy", map[string]any{"rules": rules}, http.StatusOK)
	f.assertTestExecutionPolicy(id, "2", rules)
	f.sendTestExecution("REST", id, "infinite-restart", "", "buy", "1")
	clock.set(clock.now().Add(24 * time.Hour))
	select {
	case timer := <-clock.armed:
		timer.ticks <- clock.now()
		t.Fatalf("infinite rest armed an execution timer for %s", timer.duration)
	default:
	}
	detail := f.testExecutionOrder("infinite-restart")
	if detail.Order.Status == "filled" || detail.Order.Status == "cancelled" || len(detail.Trades) != 0 {
		t.Fatalf("infinite rest stopped waiting: %+v", detail)
	}
	f.close()
	f.start()
	polltest.Until(func() bool {
		return f.testExecutionOrder("infinite-restart").Order.Status == "cancelled"
	}) // enabled lost-book order cancelled
	if detail := f.testExecutionOrder("infinite-restart"); len(detail.Trades) != 0 {
		t.Fatalf("restart invented trades: %+v", detail)
	}
	f.assertTestExecutionPolicy(id, "2", rules)
	trades := tradingE2EDecode[struct{ Trades []any }](t,
		f.rest(http.MethodGet, "/trades?account=desk", nil, http.StatusOK))
	if len(trades.Trades) != 0 {
		t.Fatalf("restart created trades: %+v", trades)
	}
}

func TestTestExecutionSplitTwoEndToEnd(t *testing.T) {
	for _, surface := range []string{"REST", "MCP"} {
		t.Run(surface, func(t *testing.T) {
			f, id, _, _ := newTestExecutionScenario(t)
			f.rest(http.MethodPut, "/trading/connections/"+id+"/test-policy", map[string]any{"rules": []any{
				map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
					"mode": "split", "fills": 2, "priceVariant": "normal"},
			}}, http.StatusOK)
			if surface == "MCP" {
				f.rest(http.MethodPut, "/mcp-access/send_paper_order", map[string]any{"enabled": true}, http.StatusOK)
			}
			f.sendTestExecution(surface, id, "split-two", "105", "buy", "1.00")
			f.assertTestExecutionFill("split-two", "105", map[string]int{"0.50": 2})
		})
	}
}

func TestTestExecutionIsolationGrantEndToEnd(t *testing.T) {
	f, id, _, _ := newTestExecutionScenario(t)
	venue := f.createLiveREST()
	f.rest(http.MethodPut, "/trading/connections/"+venue+"/fee-account", map[string]any{"account": "fees"}, http.StatusOK)
	f.rest(http.MethodPut, "/trading/connections/"+venue+"/access", map[string]any{"account": "desk"}, http.StatusOK)
	destinations := tradingE2EDecode[tradingE2EDestinations](t, f.rest(http.MethodGet, "/accounts/desk/trading-destinations?mode=paper", nil, http.StatusOK))
	tradingE2EAssertDestinations(t, destinations, id)
}

func TestSimulatorAndPaperVenueShareAccountEndToEnd(t *testing.T) {
	f, simulator, _, _ := newTestExecutionScenario(t)
	venue := f.createREST()
	path := "/trading/connections/" + venue
	f.rest(http.MethodPut, path+"/fee-account", map[string]any{"account": "fees"}, http.StatusOK)
	f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
	for _, surface := range []string{"REST", "MCP"} {
		var destinations tradingE2EDestinations
		if surface == "REST" {
			destinations = tradingE2EDecode[tradingE2EDestinations](t, f.rest(http.MethodGet, "/accounts/desk/trading-destinations?mode=paper", nil, http.StatusOK))
		} else {
			destinations = tradingE2EStructured[tradingE2EDestinations](t, f.mcp("list_trading_destinations", map[string]any{"account": "desk", "mode": "paper"}, false))
		}
		found := map[string]bool{}
		for _, destination := range destinations.Destinations {
			if destination.Mode != "paper" {
				t.Fatalf("%s mode = %q", surface, destination.Mode)
			}
			found[destination.Connection] = true
		}
		if len(found) != 2 || !found[simulator] || !found[venue] {
			t.Fatalf("%s destinations = %+v", surface, destinations)
		}
	}
}

func TestTestExecutionIsolationPublicMessageEndToEnd(t *testing.T) {
	for _, path := range []string{"account grant", "group grant", "membership"} {
		t.Run(path, func(t *testing.T) {
			f, testID, _, _ := newTestExecutionScenario(t)
			account := strings.Repeat("\U0001F600", 64)
			f.rest(http.MethodPost, "/accounts", map[string]any{"code": account, "currency": "USD"}, http.StatusCreated)
			f.rest(http.MethodPut, "/trading/connections/"+testID+"/access", map[string]any{"account": account}, http.StatusOK)
			venue := f.createLiveREST()
			venuePath := "/trading/connections/" + venue
			f.rest(http.MethodPut, venuePath+"/fee-account", map[string]any{"account": "fees"}, http.StatusOK)
			switch path {
			case "account grant":
				f.rest(http.MethodPut, venuePath+"/access", map[string]any{"account": account}, http.StatusOK)
			case "group grant":
				f.group("mixed", account)
				f.rest(http.MethodPut, venuePath+"/access", map[string]any{"accountGroup": "mixed"}, http.StatusOK)
			case "membership":
				f.group("mixed")
				f.rest(http.MethodPut, venuePath+"/access", map[string]any{"accountGroup": "mixed"}, http.StatusOK)
				f.rest(http.MethodPut, "/accounts/"+account+"/group?missingAccount=reject", map[string]any{"group": "mixed"}, http.StatusOK)
			}
			if path != "membership" {
				f.rest(http.MethodPut, "/mcp-access/grant_trading_access", map[string]any{"enabled": true}, http.StatusOK)
				input := map[string]any{"connection": venue}
				if path == "account grant" {
					input["account"] = account
				} else {
					input["accountGroup"] = "mixed"
				}
				f.mcpLoosening("grant_trading_access", input)
			}
		})
	}
}

func TestTestExecutionIsolationEscapedAccountEndToEnd(t *testing.T) {
	f, testID, _, _ := newTestExecutionScenario(t)
	account := "\"\\<>& a\u0301" + strings.Repeat("\U0001F600", 56)
	if len([]rune(account)) != 64 {
		t.Fatal("escape-heavy account must have exactly 64 code points")
	}
	f.rest(http.MethodPost, "/accounts", map[string]any{"code": account, "currency": "USD"}, http.StatusCreated)
	f.rest(http.MethodPut, "/trading/connections/"+testID+"/access", map[string]any{"account": account}, http.StatusOK)
	venue := f.createLiveREST()
	f.rest(http.MethodPut, "/trading/connections/"+venue+"/access", map[string]any{"account": account}, http.StatusOK)
}

type testExecutionSingleScope struct {
	backend.EveryAccount
	account domain.AccountID
}

func (s testExecutionSingleScope) Features() backend.AccountFeatures {
	return backend.AccountFeatures{Account: s.account}
}

func TestTestExecutionIsolationCreatedMessageEndToEnd(t *testing.T) {
	f, testID, _, _ := newTestExecutionScenario(t)
	account := strings.Repeat("\U0001F600", 64)
	f.rest(http.MethodPost, "/accounts", map[string]any{"code": account, "currency": "USD"}, http.StatusCreated)
	f.rest(http.MethodPut, "/trading/connections/"+testID+"/access", map[string]any{"account": account}, http.StatusOK)
	f.close()
	f.builder.SetServiceFactory(func(_ context.Context, n node.Node, md backend.MarketDataRuntime, rt backend.TradingRuntime, signer fwsigning.Service, _ *marketdata.Registry, tools *frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
		return backend.New(n, md, signer, engine.LockSettlementPrice, backend.WithTradingRuntime(rt), backend.WithMCPCatalogProvider(tools), backend.WithAccountScope(testExecutionSingleScope{account: domain.AccountID(account)}))
	})
	f.start()
	input := tradingE2ECreateInput("Created with mixed access", "live")
	input["enabled"] = false
	f.rest(http.MethodPost, "/trading/connections", input, http.StatusCreated)
}

func TestTestExecutionPolicyErrorParityEndToEnd(t *testing.T) {
	f, id, _, _ := newTestExecutionScenario(t)
	f.rest(http.MethodPut, "/mcp-access/set_test_execution_policy", map[string]any{"enabled": true}, http.StatusOK)
	path := "/trading/connections/" + id + "/test-policy"
	rules := []any{map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
		"mode": "execute", "priceVariant": "normal"}}
	f.rest(http.MethodPut, path, map[string]any{"rules": rules}, http.StatusOK)
	for _, tc := range []struct {
		name, pointer, constraint string
		rule                      map[string]any
	}{
		{"fills", "/rules/0/fills", "enum", map[string]any{"mask": map[string]any{"baseAsset": "AAPL", "quoteAsset": "USD"},
			"mode": "split", "priceVariant": "normal", "fills": 4}},
		{"mask", "/rules/0/mask/baseAsset", "required", map[string]any{"mask": map[string]any{"baseAsset": "", "quoteAsset": "USD"},
			"mode": "execute", "priceVariant": "normal"}},
	} {
		t.Logf("policy error %s", tc.name)
		policy := map[string]any{"rules": []any{tc.rule}}
		raw := f.rest(http.MethodPut, path, policy, http.StatusUnprocessableEntity)
		problem := tradingE2EDecode[struct {
			Status int
			Errors []struct{ Code, Pointer, Constraint string }
		}](t, raw)
		if problem.Status != 422 || len(problem.Errors) != 1 || problem.Errors[0].Code != "validation" ||
			problem.Errors[0].Pointer != tc.pointer || problem.Errors[0].Constraint != tc.constraint {
			t.Fatalf("policy problem = %s", raw)
		}
		f.assertTestExecutionPolicy(id, "2", rules)
		result := f.mcp("set_test_execution_policy", map[string]any{"connection": id, "policy": policy}, true)
		mcpBody := tradingE2EStructured[map[string]any](t, result)
		restBody := tradingE2EDecode[map[string]any](t, raw)
		if !reflect.DeepEqual(mcpBody, restBody) {
			t.Fatalf("policy error parity: REST=%v MCP=%v", restBody, mcpBody)
		}
		f.assertTestExecutionPolicy(id, "2", rules)
	}
	venue := f.createREST()
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		var input any
		tool := "get_test_execution_policy"
		arguments := map[string]any{"connection": venue}
		if method == http.MethodPut {
			input = map[string]any{"rules": rules}
			arguments["policy"] = input
			tool = "set_test_execution_policy"
		}
		raw := f.rest(method, "/trading/connections/"+venue+"/test-policy", input, http.StatusConflict)
		body := tradingE2EDecode[struct {
			Error struct{ Code, Message string }
		}](t, raw)
		if body.Error.Code != "execution_policy_unsupported" || body.Error.Message == "" {
			t.Fatalf("venue policy error = %s", raw)
		}
		result := f.mcp(tool, arguments, true)
		mcpBody := tradingE2EStructured[map[string]any](t, result)
		restBody := tradingE2EDecode[map[string]any](t, raw)
		if !reflect.DeepEqual(mcpBody, restBody) {
			t.Fatalf("409 policy error parity: REST=%v MCP=%v", restBody, mcpBody)
		}
		f.assertTestExecutionPolicy(id, "2", rules)
	}
}

func TestTradingSendLiveEndToEnd(t *testing.T) {
	for _, surface := range []string{"REST", "MCP"} {
		t.Run(surface, func(t *testing.T) {
			f := newTradingE2E(t, true)
			if surface == "MCP" {
				result := f.mcp("send_live_order", tradingE2ESendInput(""), true)
				text, ok := result.Content[0].(*sdkmcp.TextContent)
				if !ok || !strings.Contains(text.Text, `Command "send_live_order" is disabled in the Pit Officer panel`) {
					t.Fatal("live send must default to disabled and refuse with the panel notice")
				}
			}
			created := tradingE2EDecode[tradingE2ECreated](t, f.rest(http.MethodPost, "/trading/connections",
				tradingE2ECreateInput("Live", "live"), http.StatusCreated)).Connection
			if created.ID == "" || created.Mode != "live" || !created.Enabled || created.VenueIdentity != "paper-account" {
				t.Fatalf("live connection = %+v", created)
			}
			id, path := created.ID, "/trading/connections/"+created.ID
			f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "AAPL",
				"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
			f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
			for _, account := range []string{"desk", "fees"} {
				f.rest(http.MethodPost, "/accounts/"+account+"/adjustments?missingAccount=reject",
					map[string]any{"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
						"balance": map[string]any{"mode": "absolute", "value": "1000"}}, http.StatusCreated)
			}
			f.rest(http.MethodPut, path+"/fee-account", map[string]any{"account": "fees"}, http.StatusOK)
			if surface == "MCP" {
				for _, tool := range []string{"send_paper_order", "send_live_order"} {
					f.rest(http.MethodPut, "/mcp-access/"+tool, map[string]any{"enabled": true}, http.StatusOK)
				}
			}
			f.sendPaper(surface, tradingE2ESendInput(id), http.StatusConflict, "trading_mode_mismatch")
			raw := f.sendVenue(surface, domain.TradingModeLive, tradingE2ESendInput(id), http.StatusCreated, "")
			sent := tradingE2EDecode[struct {
				ID, Verdict string
				VenueSend   struct {
					Status     string
					VenueOrder struct{ Connection, VenueOrderID string }
				}
			}](t, raw)
			if sent.ID == "" || sent.Verdict != "accept" || sent.VenueSend.Status != "acknowledged" ||
				sent.VenueSend.VenueOrder.Connection != id || sent.VenueSend.VenueOrder.VenueOrderID != "venue-1" {
				t.Fatalf("live send acknowledgement = %s", raw)
			}
			f.venueMu.Lock()
			posts := len(f.posts)
			f.venueMu.Unlock()
			if posts != 1 {
				t.Fatalf("venue posts = %d, want 1: the refused paper send must not reach the venue", posts)
			}
			f.close()
			tradingE2ENoSecrets(t, "live send logs", []byte(f.logs.String()))
		})
	}
}

func TestTradingSendPaperAlpacaGoodTillCancelledMCP(t *testing.T) {
	f := newTradingE2E(t, true)
	id := f.setupSend("MCP")
	f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject",
		map[string]any{"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
			"balance": map[string]any{"mode": "absolute", "value": "1000"}}, http.StatusCreated)
	balances := f.rest(http.MethodGet, "/balances?account=desk", nil, http.StatusOK)
	input := tradingE2ESendInput(id)
	input["id"] = "uppercase-gtc"
	input["destination"].(map[string]any)["route"] = map[string]any{"timeInForce": "GTC"}
	result := f.mcp("send_paper_order", input, true)
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.Contains(text.Text, "Alpaca timeInForce must be one of day, gtc, opg, cls, ioc, fok (case-sensitive)") {
		t.Fatalf("Alpaca refusal must name the venue and exact allowed spellings: %+v", result.Content)
	}
	if _, err := f.realm.GetOrder(f.ctx, "uppercase-gtc"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("refused route created an order: %v", err)
	}
	if after := f.rest(http.MethodGet, "/balances?account=desk", nil, http.StatusOK); !bytes.Equal(after, balances) {
		t.Fatalf("refused route changed balances or reservations: %s", after)
	}
	f.venueMu.Lock()
	posts := len(f.posts)
	f.venueMu.Unlock()
	if posts != 0 {
		t.Fatal("uppercase Alpaca timeInForce reached venue")
	}
	input["id"] = "lowercase-gtc"
	input["destination"].(map[string]any)["route"] = map[string]any{"timeInForce": "gtc"}
	raw := f.sendPaper("MCP", input, http.StatusCreated, "")
	sent := tradingE2EDecode[struct {
		ID, Verdict string
		VenueSend   struct {
			Status     string
			VenueOrder struct {
				VenueOrderID  string
				SendAttempted bool
			}
		}
	}](t, raw)
	if sent.ID != "lowercase-gtc" || sent.Verdict != "accept" || sent.VenueSend.Status != "acknowledged" ||
		sent.VenueSend.VenueOrder.VenueOrderID != "venue-1" || !sent.VenueSend.VenueOrder.SendAttempted {
		t.Fatalf("lowercase Alpaca gtc acknowledgement = %s", raw)
	}
	f.venueMu.Lock()
	defer f.venueMu.Unlock()
	if len(f.posts) != 1 || f.posts[0]["time_in_force"] != "gtc" {
		t.Fatalf("Alpaca gtc venue requests = %+v", f.posts)
	}
}

func TestTradingSendPaperRefusalsEndToEnd(t *testing.T) {
	for _, surface := range []string{"REST", "MCP"} {
		t.Run(surface, func(t *testing.T) {
			f := newTradingE2E(t, true)
			if surface == "MCP" {
				result := f.mcp("send_paper_order", tradingE2ESendInput(""), true)
				text, ok := result.Content[0].(*sdkmcp.TextContent)
				if !ok || !strings.Contains(text.Text, `Command "send_paper_order" is disabled in the Pit Officer panel`) {
					t.Fatal("paper send must default to disabled and refuse with the panel notice")
				}
				f.rest(http.MethodPut, "/mcp-access/send_paper_order", map[string]any{"enabled": true}, http.StatusOK)
			}
			missing := tradingE2ESendInput("")
			missing["id"] = "no-destination"
			f.sendPaper(surface, missing, http.StatusConflict, "no_destination")
			id := f.setupSend(surface)
			for _, price := range []string{"1e2147483647", "1e-2147483648"} {
				input := tradingE2ESendInput(id)
				input["id"], input["price"] = "bad-price", price
				raw := f.sendPaper(surface, input, http.StatusUnprocessableEntity, "")
				if surface == "REST" && (!bytes.Contains(raw, []byte("limit price")) || bytes.Contains(raw, []byte(price))) {
					t.Fatal("exponent refusal must name the field without its value")
				}
			}
			for _, route := range []any{nil, "{}", []any{}, 1, true} {
				input := tradingE2ESendInput(id)
				input["id"] = "bad-route"
				input["destination"].(map[string]any)["route"] = route
				if surface == "MCP" && route != nil {
					_, err := f.callMCP("send_paper_order", input)
					var rpcErr *jsonrpc.Error
					if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams || !strings.Contains(err.Error(), "unmarshaling") {
						t.Fatalf("non-object route: got %v, want JSON-RPC invalid params at decode", err)
					}
					tradingE2ENoSecrets(t, "MCP route decode", []byte(err.Error()))
				} else {
					f.sendPaper(surface, input, http.StatusUnprocessableEntity, "")
				}
			}
			input := tradingE2ESendInput(id)
			input["id"] = "missing-route"
			delete(input["destination"].(map[string]any), "route")
			f.sendPaper(surface, input, http.StatusUnprocessableEntity, "")
			input = tradingE2ESendInput(id)
			input["id"] = "wrong-venue-account"
			input["destination"].(map[string]any)["venueAccount"] = "another-account"
			f.sendPaper(surface, input, http.StatusForbidden, "")
			input = tradingE2ESendInput("unknown-connection")
			input["id"] = "not-found"
			f.sendPaper(surface, input, http.StatusNotFound, "")
			live, err := f.realm.CreateTradingConnection(f.ctx, domain.TradingConnection{
				Provider: domain.TradingProviderAlpaca, Label: "Live", Mode: domain.TradingModeLive,
				VenueIdentity: "live-account", Credentials: tradingCredentials, Enabled: true})
			tradingE2EMust(t, err)
			_, err = f.realm.CreateAccount(f.ctx, domain.Account{Code: "live-desk", Currency: "USD"})
			tradingE2EMust(t, err)
			tradingE2EMust(t, f.realm.PutTradingAccess(f.ctx, domain.TradingAccess{Account: "live-desk", Connection: live.ExternalID}))
			input = tradingE2ESendInput(live.ExternalID.String())
			input["id"] = "wrong-mode"
			input["account"] = "live-desk"
			f.sendPaper(surface, input, http.StatusConflict, "trading_mode_mismatch")
			second, err := f.realm.CreateTradingConnection(f.ctx, domain.TradingConnection{
				Provider: domain.TradingProviderAlpaca, Label: "Second", Mode: domain.TradingModePaper,
				VenueIdentity: "second-account", Credentials: tradingCredentials, Enabled: true})
			tradingE2EMust(t, err)
			tradingE2EMust(t, f.realm.PutTradingAccess(f.ctx, domain.TradingAccess{Account: "desk", Connection: second.ExternalID}))
			input = tradingE2ESendInput("")
			input["id"] = "ambiguous"
			raw := f.sendPaper(surface, input, http.StatusConflict, "destination_ambiguous")
			if surface == "REST" {
				body := tradingE2EDecode[struct {
					Error struct{ Candidates []struct{ Connection string } }
				}](t, raw)
				if len(body.Error.Candidates) != 2 {
					t.Fatalf("ambiguous candidates = %s", raw)
				}
				candidates := map[string]bool{}
				for _, candidate := range body.Error.Candidates {
					candidates[candidate.Connection] = true
				}
				if !candidates[id] || !candidates[second.ExternalID.String()] {
					t.Fatal("wrong ambiguity candidates")
				}
			} else {
				result := f.mcp("send_paper_order", input, true)
				text := result.Content[0].(*sdkmcp.TextContent).Text
				if !strings.Contains(text, id) || !strings.Contains(text, second.ExternalID.String()) {
					t.Fatal("MCP ambiguity omitted candidates")
				}
			}
			for _, orderID := range []string{"no-destination", "bad-price", "bad-route", "missing-route",
				"wrong-venue-account", "not-found", "wrong-mode", "ambiguous"} {
				_, err := f.realm.GetOrder(f.ctx, domain.ExternalID(orderID))
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("refusal created order %s: %v", orderID, err)
				}
			}
			f.venueMu.Lock()
			posts := len(f.posts)
			f.venueMu.Unlock()
			if posts != 0 {
				t.Fatal("refused send reached venue")
			}
			f.rest(http.MethodGet, "/audit?limit=500", nil, http.StatusOK)
			f.close()
			tradingE2ENoSecrets(t, "refused send logs", []byte(f.logs.String()))
		})
	}
}

func TestTradingFeeAccountEndToEnd(t *testing.T) {
	for _, feeAccount := range []bool{true, false} {
		t.Run(fmt.Sprintf("feeAccount=%t", feeAccount), func(t *testing.T) {
			f := newTradingE2E(t, true)
			id := f.createREST()
			path := "/trading/connections/" + id
			f.rest(http.MethodPost, "/accounts/fees/adjustments?missingAccount=reject", map[string]any{"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
				"balance": map[string]any{"mode": "absolute", "value": "10"}}, http.StatusCreated)
			f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
			f.rest(http.MethodPut, path+"/fee-account", map[string]any{"account": "fees"}, http.StatusOK)
			for _, code := range []string{"desk", "other"} {
				f.rest(http.MethodPut, path+"/access", map[string]any{"account": code}, http.StatusOK)
			}
			// Fee intake starts at the first persisted venue order.
			// Seed history without exercising the send path
			// or leaving an order to reconcile.
			order, err := f.realm.CreateOrder(f.ctx, domain.Order{Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD",
				Source: domain.SourceAPI, Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
				AmountValue: "1", Price: "2", Leaves: "0", ReservedQuantity: "0", Status: domain.OrderStatusCancelled})
			tradingE2EMust(t, err)
			_, err = f.realm.CreateVenueOrder(f.ctx, domain.VenueOrder{Order: order.ExternalID, Connection: domain.ExternalID(id),
				Route: `{"timeInForce":"day"}`, ClientOrderID: "historical-order"})
			tradingE2EMust(t, err)
			tradingE2EMust(t, f.realm.MarkVenueOrderSendAttempted(f.ctx, order.ExternalID))
			tradingE2EMust(t, f.realm.SetVenueOrderID(f.ctx, order.ExternalID, "historical-venue-order"))
			if !feeAccount {
				// REST prevents ambiguous fee routing. Seed the
				// persisted state to exercise the runtime's
				// defensive skip after restart.
				tradingE2EMust(t, f.realm.SetTradingConnectionFeeAccount(f.ctx, domain.ExternalID(id), ""))
			}
			f.close()
			f.venueMu.Lock()
			f.fees = []map[string]string{{"id": "regulatory-fee", "activity_type": "FEE", "status": "executed", "currency": "USD", "net_amount": "-0.25", "date": time.Now().UTC().Format("2006-01-02")}}
			f.venueMu.Unlock()
			f.start()
			if feeAccount {
				polltest.Until(func() bool {
					rows, err := f.realm.ListAdjustments(f.ctx, "fees", domain.SourceSystem, 10)
					tradingE2EMust(t, err)
					return len(rows) == 1
				}) // fee booked to configured account
				rows, err := f.realm.ListAdjustments(f.ctx, "fees", domain.SourceSystem, 10)
				tradingE2EMust(t, err)
				row := rows[0]
				if row.ExternalID != domain.ExternalID("trading:"+id+":fee:regulatory-fee") || row.Asset != "USD" || row.Accepted == nil || row.Rejected != nil || row.Principal != "" ||
					row.Request.Balance == nil || row.Request.Balance.Mode != domain.AdjustmentModeDelta || row.Request.Balance.Value != "-0.25" {
					t.Fatalf("fee adjustment = %+v", row)
				}
				balance, found, err := f.realm.GetBalance(f.ctx, "fees", "USD")
				tradingE2EMust(t, err)
				value, err := decimal.NewFromString(balance.Available)
				tradingE2EMust(t, err)
				if !found || !value.Equal(decimal.RequireFromString("9.75")) {
					t.Fatalf("fee balance = %+v, found %t", balance, found)
				}
				// The REST deposit on the same account is not a venue fee.
				venueFees := tradingE2EDecode[struct {
					Adjustments []struct {
						ID      string
						Request struct{ Type string }
					}
					Total int
				}](t, f.rest(http.MethodGet, "/adjustments?type=venue_fee", nil, http.StatusOK))
				if venueFees.Total != 1 || len(venueFees.Adjustments) != 1 ||
					venueFees.Adjustments[0].ID != "trading:"+id+":fee:regulatory-fee" || venueFees.Adjustments[0].Request.Type != "venue_fee" {
					t.Fatalf("venue_fee adjustments = %+v", venueFees)
				}
			} else {
				polltest.Until(func() bool {
					rows, err := f.realm.ListAuditFiltered(f.ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionVenueFeeSkipped}}, 10)
					tradingE2EMust(t, err)
					return len(rows) == 1
				}) // venue_fee_skipped audit
				rows, err := f.realm.ListAuditFiltered(f.ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionVenueFeeSkipped}}, 10)
				tradingE2EMust(t, err)
				if rows[0].Source != domain.SourceSystem || rows[0].Actor != "" || !strings.Contains(rows[0].Detail.Text, id) || !strings.Contains(rows[0].Detail.Text, "regulatory-fee") {
					t.Fatalf("skipped fee audit = %+v", rows)
				}
			}
			for _, code := range []string{"desk", "other", "fees"} {
				rows, err := f.realm.ListAdjustments(f.ctx, domain.AccountID(code), domain.SourceSystem, 10)
				tradingE2EMust(t, err)
				want := 0
				if feeAccount && code == "fees" {
					want = 1
				}
				if len(rows) != want {
					t.Fatalf("%s adjustments = %d, want %d", code, len(rows), want)
				}
			}
			tradingE2ENoSecrets(t, "fee logs", []byte(f.logs.String()))
		})
	}
}

// A REST adjustment without a type is refused as a validation problem at
// /type, and nothing is recorded.
func TestAdjustmentTypeRequiredEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	problem := tradingE2EDecode[httpx.ProblemDetails](t, f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject",
		map[string]any{"asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
			"balance": map[string]any{"mode": "absolute", "value": "1000"}}, http.StatusUnprocessableEntity))
	if len(problem.Errors) != 1 || problem.Errors[0].Code != "validation" ||
		problem.Errors[0].Pointer != "/type" || problem.Errors[0].Constraint != "required" ||
		!strings.Contains(problem.Detail, "type is required") {
		t.Fatalf("missing type problem = %+v", problem)
	}
	listed := tradingE2EDecode[struct{ Total int }](t, f.rest(http.MethodGet, "/adjustments", nil, http.StatusOK))
	if listed.Total != 0 {
		t.Fatalf("refused adjustment recorded %d rows", listed.Total)
	}
}

// A REST adjustment whose resulting balance leaves its bounds is recorded as
// rejected for its bounds and changes no balance, although its change lies
// within them: the bounds reach the engine and constrain the result.
func TestAdjustmentBoundsEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	const path = "/accounts/desk/adjustments?missingAccount=reject"
	f.rest(http.MethodPost, path, map[string]any{"type": "deposit", "asset": "USD",
		"balance": map[string]any{"mode": "absolute", "value": "100"}}, http.StatusCreated)
	adjusted := tradingE2EDecode[struct {
		Adjustment struct {
			Status  string
			Outcome httpx.AdjustmentOutcomeDTO
		}
	}](t, f.rest(http.MethodPost, path, map[string]any{"type": "deposit", "asset": "USD",
		"balance":       map[string]any{"mode": "delta", "value": "1"},
		"balanceBounds": map[string]any{"lower": "0", "upper": "100"}}, http.StatusCreated)).Adjustment
	if rejected := adjusted.Outcome.Rejected; adjusted.Status != "rejected" || rejected == nil ||
		rejected.Code != domain.RejectCodeAccountAdjustmentBoundsExceeded {
		t.Fatalf("adjustment past its bounds = %+v, want it rejected for its bounds", adjusted)
	}
	balances := tradingE2EDecode[struct{ Balances []struct{ Available string } }](t,
		f.rest(http.MethodGet, "/balances?account=desk&asset=USD", nil, http.StatusOK)).Balances
	if len(balances) != 1 || balances[0].Available != "100" {
		t.Fatalf("desk USD balances after the rejection = %+v, want 100 unchanged", balances)
	}
}

// An immediate order without a price is refused on REST and MCP alike as a
// validation error at /price with the same core message; neither surface
// records the order nor creates the account it names, and the refused attempt
// is audited as approval_failed.
func TestImmediateWithoutPriceRefusedEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	order := map[string]any{"account": "fresh", "baseAsset": "AAPL", "quoteAsset": "USD",
		"side": "buy", "amountKind": "quantity", "amountValue": "1", "mode": "immediate"}
	problem := tradingE2EDecode[httpx.ProblemDetails](t, f.rest(http.MethodPost, "/orders/submit?missingAccount=create",
		order, http.StatusUnprocessableEntity))
	if problem.Status != http.StatusUnprocessableEntity || len(problem.Errors) != 1 ||
		problem.Errors[0].Code != "validation" || problem.Errors[0].Pointer != "/price" ||
		problem.Errors[0].Constraint != "required" {
		t.Fatalf("priceless immediate problem = %+v", problem)
	}
	f.rest(http.MethodPut, "/mcp-access/submit_order", map[string]any{"enabled": true}, http.StatusOK)
	input := map[string]any{"missingAccount": "create"}
	for key, value := range order {
		input[key] = value
	}
	result := f.mcp("submit_order", input, true)
	want := "validation: " + problem.Detail
	if text, ok := result.Content[0].(*sdkmcp.TextContent); !ok || text.Text != want {
		t.Fatalf("priceless immediate MCP error = %+v, want %q", result.Content[0], want)
	}
	listed := tradingE2EDecode[struct{ Total int }](t, f.rest(http.MethodGet, "/orders", nil, http.StatusOK))
	if listed.Total != 0 {
		t.Fatalf("refused immediate recorded %d orders", listed.Total)
	}
	if _, ok, err := f.realm.GetAccount(f.ctx, "fresh"); err != nil || ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want not created", ok, err)
	}
	failed, err := f.realm.ListAuditFiltered(f.ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionApprovalFailed}}, 10)
	if err != nil || len(failed) != 2 {
		t.Fatalf("approval_failed audit = %+v err %v, want one row per refused attempt", failed, err)
	}
	sources := map[domain.Source]int{}
	for _, row := range failed {
		if row.Account != "fresh" {
			t.Fatalf("approval_failed audit row = %+v, want account fresh", row)
		}
		sources[row.Source]++
	}
	if sources[domain.SourceAPI] != 1 || sources[domain.SourceMCP] != 1 {
		t.Fatalf("approval_failed audit sources = %v, want one api and one mcp", sources)
	}
}

// group creates an account group and moves members into it.
func (f *tradingE2E) group(code string, members ...string) {
	f.t.Helper()
	f.rest(http.MethodPost, "/groups", map[string]any{"code": code}, http.StatusCreated)
	for _, member := range members {
		f.rest(http.MethodPut, "/accounts/"+member+"/group?missingAccount=reject",
			map[string]any{"group": code}, http.StatusOK)
	}
}

func TestTradingGroupAccessEndToEnd(t *testing.T) {
	f := newTradingE2E(t, true)
	f.rest(http.MethodPost, "/accounts", map[string]any{"code": "outside", "currency": "USD"}, http.StatusCreated)
	f.group("team", "desk", "other")
	f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject",
		map[string]any{"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
			"balance": map[string]any{"mode": "absolute", "value": "1000"}}, http.StatusCreated)
	id := f.createREST()
	path := "/trading/connections/" + id
	f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "AAPL",
		"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
	type refusal struct{ Error struct{ Code string } }
	group := map[string]any{"accountGroup": "team"}
	refused := tradingE2EDecode[refusal](t, f.rest(http.MethodPut, path+"/access", group, http.StatusConflict))
	if refused.Error.Code != "fee_account_required" {
		t.Fatalf("group grant without a fee account: %s", refused.Error.Code)
	}
	f.rest(http.MethodPut, path+"/fee-account", map[string]any{"account": "fees"}, http.StatusOK)
	f.rest(http.MethodPut, path+"/access", group, http.StatusOK)
	f.rest(http.MethodPut, path+"/access", map[string]any{"account": "other"}, http.StatusOK)
	// desk holds no grant of its own, so it sends through its group's grant.
	raw := f.sendPaper("REST", tradingE2ESendInput(id), http.StatusCreated, "")
	sent := tradingE2EDecode[struct {
		Verdict   string
		VenueSend struct {
			Status     string
			VenueOrder struct{ Connection string }
		}
	}](t, raw)
	if sent.Verdict != "accept" || sent.VenueSend.Status != "acknowledged" || sent.VenueSend.VenueOrder.Connection != id {
		t.Fatalf("member send through the group grant = %s", raw)
	}
	tradingE2EAssertDestinations(t, tradingE2EDecode[tradingE2EDestinations](t,
		f.rest(http.MethodGet, "/accounts/desk/trading-destinations?mode=paper", nil, http.StatusOK)), id)
	outside := tradingE2ESendInput(id)
	outside["id"], outside["account"] = "outside-group", "outside"
	f.sendPaper("REST", outside, http.StatusForbidden, "forbidden")
	snapshot := tradingE2EDecode[struct {
		Trading struct {
			Connections []struct{ Access []map[string]string }
		}
	}](t, f.rest(http.MethodGet, "/trading", nil, http.StatusOK)).Trading
	want := []map[string]string{
		{"account": "", "accountGroup": "team", "venueAccount": "", "group": ""},
		{"account": "other", "accountGroup": "", "venueAccount": "", "group": "team"},
	}
	if len(snapshot.Connections) != 1 || !reflect.DeepEqual(snapshot.Connections[0].Access, want) {
		t.Fatalf("connection access = %+v", snapshot.Connections)
	}
	refused = tradingE2EDecode[refusal](t, f.rest(http.MethodDelete, path+"/fee-account", nil, http.StatusConflict))
	if refused.Error.Code != "fee_account_required" {
		t.Fatalf("fee account clear with several accounts able to send: %s", refused.Error.Code)
	}
	f.rest(http.MethodDelete, path+"/access?accountGroup=team", nil, http.StatusNoContent)
	revoked := tradingE2ESendInput(id)
	revoked["id"] = "group-revoked"
	f.sendPaper("REST", revoked, http.StatusForbidden, "forbidden")
	// The own grant of other leaves a single account that can send.
	f.rest(http.MethodDelete, path+"/fee-account", nil, http.StatusNoContent)
	for _, orderID := range []string{"outside-group", "group-revoked"} {
		if _, err := f.realm.GetOrder(f.ctx, domain.ExternalID(orderID)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("refused send created order %s: %v", orderID, err)
		}
	}
	f.venueMu.Lock()
	posts := len(f.posts)
	f.venueMu.Unlock()
	if posts != 1 {
		t.Fatalf("venue orders = %d, want 1", posts)
	}
}

func TestTradingGroupAccessMCPEndToEnd(t *testing.T) {
	f := newTradingE2E(t, true)
	f.group("team", "desk")
	id := f.createREST()
	for _, name := range []string{"grant_trading_access", "revoke_trading_access"} {
		f.rest(http.MethodPut, "/mcp-access/"+name, map[string]any{"enabled": true}, http.StatusOK)
	}
	access := func() []map[string]string {
		t.Helper()
		snapshot := tradingE2EStructured[struct {
			Connections []struct{ Access []map[string]string }
		}](t, f.mcp("get_trading", map[string]any{}, false))
		if len(snapshot.Connections) != 1 {
			t.Fatalf("connections = %+v", snapshot.Connections)
		}
		return snapshot.Connections[0].Access
	}
	group := map[string]any{"connection": id, "accountGroup": "team"}
	// The agent is refused before the fee account is asked for.
	f.mcpLoosening("grant_trading_access", group)
	if got := access(); len(got) != 0 {
		t.Fatalf("access after the refused group grant = %v", got)
	}
	path := "/trading/connections/" + id
	feeAccount := path + "/fee-account"
	f.rest(http.MethodPut, feeAccount, map[string]any{"account": "fees"}, http.StatusOK)
	f.rest(http.MethodPut, path+"/access", map[string]any{"accountGroup": "team"}, http.StatusOK)
	// team has a single member, so only the group grant refuses the clear.
	type refusal struct{ Error struct{ Code string } }
	if code := tradingE2EDecode[refusal](t, f.rest(http.MethodDelete, feeAccount, nil, http.StatusConflict)).Error.Code; code != "fee_account_required" {
		t.Fatalf("fee account clear under a group grant: %s", code)
	}
	if got, want := access(), []map[string]string{{"account": "", "accountGroup": "team", "venueAccount": "", "group": ""}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group grant access = %v", got)
	}
	member := map[string]any{"account": "desk", "mode": "paper"}
	tradingE2EAssertDestinations(t, tradingE2EStructured[tradingE2EDestinations](t, f.mcp("list_trading_destinations", member, false)), id)
	if revoked := tradingE2EStructured[map[string]any](t, f.mcp("revoke_trading_access", group, false)); !reflect.DeepEqual(revoked, group) {
		t.Fatalf("group revoke output = %v", revoked)
	}
	if got := access(); len(got) != 0 {
		t.Fatalf("access after the group revoke = %v", got)
	}
	if left := tradingE2EStructured[tradingE2EDestinations](t, f.mcp("list_trading_destinations", member, false)); len(left.Destinations) != 0 {
		t.Fatalf("member destinations after the group revoke = %+v", left)
	}
	f.rest(http.MethodDelete, feeAccount, nil, http.StatusNoContent)
}

// agentState reads what an agent's trading call could change: the only
// connection as GET /trading serves it, without the health its venue stream
// moves, and the credentials stored for it, which no surface serves.
func (f *tradingE2E) agentState(id string) (tradingE2EConnection, string) {
	f.t.Helper()
	c := f.health()
	c.State, c.Error, c.Diagnostics = "", "", nil
	stored, found, err := f.realm.GetTradingConnection(f.ctx, domain.ExternalID(id))
	tradingE2EMust(f.t, err)
	if !found {
		f.t.Fatalf("trading connection %s is not stored", id)
	}
	return c, stored.Credentials
}

// An agent drives the trading tools over MCP into the real core and the fake
// venue. Each call an operator could make that widens what can be sent or
// moves where venue fees are booked - among them the revoke of the last
// account that can send through a connection without a fee account - answers
// loosening_refused and changes nothing, and the create and the credential
// change reach no venue; a label-only update, any other revoke and a disable
// pass.
func TestTradingAgentTightenOnlyEndToEnd(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		f := newTradingE2E(t, false)
		f.rest(http.MethodPut, "/mcp-access/create_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
		f.mcpLoosening("create_trading_connection", tradingE2ECreateInput("Paper", "paper"))
		connections, err := f.realm.ListTradingConnections(f.ctx)
		tradingE2EMust(t, err)
		if checks := f.accountChecks(); len(connections) != 0 || checks != 0 {
			t.Fatalf("refused create stored %d connections and reached the venue %d times", len(connections), checks)
		}
	})
	// configured has the operator create a connection over REST, map AAPL,
	// grant desk and set the fee account fees, beside the group team and the
	// asset MSFT, and enables tools for the agent.
	configured := func(t *testing.T, tools ...string) (*tradingE2E, string) {
		t.Helper()
		f := newTradingE2E(t, false)
		f.group("team")
		f.rest(http.MethodPost, "/assets", map[string]any{"code": "MSFT"}, http.StatusCreated)
		id := f.setupSend("REST")
		f.rest(http.MethodPut, "/trading/connections/"+id+"/fee-account", map[string]any{"account": "fees"}, http.StatusOK)
		for _, name := range tools {
			f.rest(http.MethodPut, "/mcp-access/"+name, map[string]any{"enabled": true}, http.StatusOK)
		}
		return f, id
	}
	for _, tc := range []struct {
		name, tool string
		input      map[string]any
	}{
		{"credential change", "update_trading_connection", map[string]any{
			"credentials": map[string]string{"apiKey": "second-key-marker", "apiSecret": "second-secret-marker"}}},
		{"mapping add", "set_trading_instrument", map[string]any{
			"externalSymbol": "MSFT", "baseAsset": "MSFT", "quoteAsset": "USD", "enabled": true}},
		{"mapping disable", "set_trading_instrument", map[string]any{
			"externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": false}},
		{"mapping delete", "delete_trading_instrument", map[string]any{"externalSymbol": "AAPL"}},
		{"account grant", "grant_trading_access", map[string]any{"account": "other"}},
		{"group grant", "grant_trading_access", map[string]any{"accountGroup": "team"}},
		{"fee account set", "set_trading_fee_account", map[string]any{"account": "other"}},
		{"fee account clear", "clear_trading_fee_account", map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, id := configured(t, tc.tool)
			before, credentials := f.agentState(id)
			checks := f.accountChecks()
			tc.input["connection"] = id
			f.mcpLoosening(tc.tool, tc.input)
			after, stored := f.agentState(id)
			if !reflect.DeepEqual(after, before) || stored != credentials || f.accountChecks() != checks {
				t.Fatalf("refused %s left %+v, credentials changed %t, venue checks %d -> %d",
					tc.name, after, stored != credentials, checks, f.accountChecks())
			}
		})
	}
	t.Run("narrowing", func(t *testing.T) {
		f, id := configured(t, "update_trading_connection", "revoke_trading_access", "set_trading_connection_enabled")
		want, credentials := f.agentState(id)
		checks := f.accountChecks()
		f.mcp("update_trading_connection", map[string]any{"connection": id, "label": "Renamed"}, false)
		f.mcp("revoke_trading_access", map[string]any{"connection": id, "account": "desk"}, false)
		f.mcp("set_trading_connection_enabled", map[string]any{"connection": id, "enabled": false}, false)
		want.Label, want.Access, want.Enabled = "Renamed", []domain.TradingAccess{}, false
		narrowed, stored := f.agentState(id)
		if !reflect.DeepEqual(narrowed, want) || stored != credentials || f.accountChecks() != checks {
			t.Fatalf("narrowed connection = %+v, want %+v with its credentials and no venue check", narrowed, want)
		}
		// The connection the agent disabled stays disabled.
		f.mcpLoosening("set_trading_connection_enabled", map[string]any{"connection": id, "enabled": true})
		if after, _ := f.agentState(id); !reflect.DeepEqual(after, narrowed) {
			t.Fatalf("refused enable left %+v", after)
		}
	})
	// Without a fee account the Alpaca connection's venue fees are debited to
	// the one account that can send through it, so the agent may not revoke
	// that account's grant: the fees would then be skipped.
	t.Run("last sender revoke", func(t *testing.T) {
		f := newTradingE2E(t, false)
		id := f.setupSend("REST")
		f.rest(http.MethodPut, "/mcp-access/revoke_trading_access", map[string]any{"enabled": true}, http.StatusOK)
		before, _ := f.agentState(id)
		f.mcpLoosening("revoke_trading_access", map[string]any{"connection": id, "account": "desk"})
		after, _ := f.agentState(id)
		if !reflect.DeepEqual(after, before) || len(after.Access) != 1 || after.Access[0].Account != "desk" {
			t.Fatalf("refused revoke left %+v, want the grant to desk kept", after)
		}
	})
}

func TestTradingSecretsEndToEnd(t *testing.T) {
	for _, sealed := range []bool{true, false} {
		t.Run(fmt.Sprintf("sealed=%t", sealed), func(t *testing.T) {
			f := newTradingE2E(t, sealed)
			id := f.createREST()
			f.rest(http.MethodGet, "/trading", nil, http.StatusOK)
			f.mcp("get_trading", map[string]any{}, false)
			f.rest(http.MethodPut, "/mcp-access/check_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
			f.venueMu.Lock()
			f.accountError = true
			f.venueMu.Unlock()
			f.rest(http.MethodPost, "/trading/connections/"+id+"/check", map[string]any{}, http.StatusBadGateway)
			f.mcp("check_trading_connection", map[string]any{"connection": id}, true)
			f.rest(http.MethodPost, "/trading/connections", tradingE2ECreateInput("Failed", "paper"), http.StatusBadGateway)
			f.rest(http.MethodPut, "/mcp-access/create_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
			f.mcp("create_trading_connection", tradingE2ECreateInput("Failed", "paper"), true)
			f.venueMu.Lock()
			f.accountError = false
			f.venueMu.Unlock()
			raw := f.rest(http.MethodPost, "/backup/export", map[string]any{"scope": map[string]any{"all": true}}, http.StatusOK)
			archive := tradingE2EDecode[backup.Archive](t, raw)
			wantForm := backup.CredentialFormPlaintext
			if sealed {
				wantForm = backup.CredentialFormSealed
				tradingE2ENoSecrets(t, "sealed backup", raw)
			}
			if archive.CredentialForm != wantForm || len(archive.Data.TradingConnections) != 1 {
				t.Fatalf("backup credential form = %s, connections = %d", archive.CredentialForm, len(archive.Data.TradingConnections))
			}
			credentials := archive.Data.TradingConnections[0].Credentials
			if sealed {
				tradingE2ENoSecrets(t, "decoded sealed credentials", credentials)
				if len(credentials) == 0 || bytes.Equal(credentials, []byte(tradingCredentials)) {
					t.Fatal("sealed credential payload missing")
				}
			} else {
				if !bytes.Equal(credentials, []byte(tradingCredentials)) {
					t.Fatal("plaintext backup credentials differ from input")
				}
			}
			rows, err := f.realm.ListAuditFiltered(f.ctx, domain.AuditFilter{}, 500)
			tradingE2EMust(t, err)
			if len(rows) == 0 {
				t.Fatal("audit capture is empty")
			}
			audit, err := json.Marshal(rows)
			tradingE2EMust(t, err)
			tradingE2ENoSecrets(t, "audit", audit)
			f.close()
			logs := f.logs.String()
			warningFound := false
			for line := range strings.SplitSeq(logs, "\n") {
				if strings.Contains(line, `level=WARN msg="upstream provider request failed"`) &&
					strings.Contains(line, "alpaca: HTTP 401 (code 40110000):") {
					warningFound = true
					tradingE2ENoSecrets(t, "WriteErr warning", []byte(line))
				}
			}
			if !warningFound {
				t.Fatal("WriteErr warning with decoded Alpaca refusal was not captured")
			}
			tradingE2ENoSecrets(t, "slog", []byte(logs))
		})
	}
}

func TestTradingCredentialSpellingsEndToEnd(t *testing.T) {
	credentials := map[string]string{"apiKey": "echo-key-marker", "apiSecret": `e2e "s" \ <x&y> +u/v`}
	stored, err := json.Marshal(credentials)
	tradingE2EMust(t, err)
	// The apiSecret in each form the backend redacts. No row contains another,
	// so each form is checked on its own.
	spellings := []struct{ form, text string }{
		{"raw", `e2e "s" \ <x&y> +u/v`},
		{"json", `e2e \"s\" \\ \u003cx\u0026y\u003e +u/v`},
		{"json_unescaped_html", `e2e \"s\" \\ <x&y> +u/v`},
		{"url_query", `e2e+%22s%22+%5C+%3Cx%26y%3E+%2Bu%2Fv`},
		{"url_path", `e2e%20%22s%22%20%5C%20%3Cx&y%3E%20+u%2Fv`},
	}
	for _, spelling := range spellings {
		t.Run(spelling.form, func(t *testing.T) {
			f := newTradingE2E(t, false)
			// Stored directly and disabled: the fake venue's credential gate
			// and stream know only the shared markers.
			connection, err := f.realm.CreateTradingConnection(f.ctx, domain.TradingConnection{
				Provider: domain.TradingProviderAlpaca, Label: "Echo", Mode: domain.TradingModePaper,
				VenueIdentity: "echo-account", Credentials: string(stored)})
			tradingE2EMust(t, err)
			id := connection.ExternalID.String()
			f.venueMu.Lock()
			f.accountRefusal = "denied " + spelling.text
			f.venueMu.Unlock()
			for _, name := range []string{"check_trading_connection", "create_trading_connection"} {
				f.rest(http.MethodPut, "/mcp-access/"+name, map[string]any{"enabled": true}, http.StatusOK)
			}
			create := tradingE2ECreateInput("Echo", "paper")
			create["credentials"] = credentials
			surfaces := map[string]string{
				"REST check":  string(f.rest(http.MethodPost, "/trading/connections/"+id+"/check", map[string]any{}, http.StatusBadGateway)),
				"REST create": string(f.rest(http.MethodPost, "/trading/connections", create, http.StatusBadGateway)),
			}
			for name, input := range map[string]map[string]any{
				"check_trading_connection":  {"connection": id},
				"create_trading_connection": create,
			} {
				text, ok := f.mcp(name, input, true).Content[0].(*sdkmcp.TextContent)
				if !ok {
					t.Fatalf("MCP %s returned no text", name)
				}
				surfaces["MCP "+name] = text.Text
			}
			f.close()
			logs := f.logs.String()
			surfaces["slog"] = logs
			for where, text := range surfaces {
				for _, s := range spellings {
					if strings.Contains(text, s.text) {
						t.Fatalf("%s keeps the %s spelling of the secret", where, s.form)
					}
				}
			}
			// The slog text handler quotes the logged error, escaping a leaked
			// spelling once more.
			for _, s := range spellings {
				quoted := strconv.Quote(s.text)
				if strings.Contains(logs, quoted[1:len(quoted)-1]) {
					t.Fatalf("slog keeps the %s spelling of the secret, quoted", s.form)
				}
			}
			warningFound := false
			for line := range strings.SplitSeq(logs, "\n") {
				if strings.Contains(line, `level=WARN msg="upstream provider request failed"`) &&
					strings.Contains(line, "alpaca: HTTP 401 (code 40110000): denied [redacted]") {
					warningFound = true
				}
			}
			if !warningFound {
				t.Fatal("WriteErr warning with the redacted venue refusal was not captured")
			}
		})
	}
}

// health reads the only trading connection from GET /trading.
func (f *tradingE2E) health() tradingE2EConnection {
	f.t.Helper()
	connections := tradingE2EDecode[tradingE2ETrading](f.t, f.rest(http.MethodGet, "/trading", nil, http.StatusOK)).Trading.Connections
	if len(connections) != 1 {
		f.t.Fatalf("connections = %+v", connections)
	}
	return connections[0]
}

// connectionSurfaces returns the only trading connection as GET /trading and
// MCP get_trading serve it, each re-encoded with sorted keys. A reconnecting
// stream refreshes its diagnostic's time, so it reads until two REST reads
// around the MCP read agree.
func (f *tradingE2E) connectionSurfaces() (viaREST, viaMCP string) {
	f.t.Helper()
	canonical := func(connections []json.RawMessage) string {
		f.t.Helper()
		if len(connections) != 1 {
			f.t.Fatalf("connections = %d, want 1", len(connections))
		}
		var value any
		tradingE2EMust(f.t, json.Unmarshal(connections[0], &value))
		raw, err := json.Marshal(value)
		tradingE2EMust(f.t, err)
		return string(raw)
	}
	readREST := func() string {
		f.t.Helper()
		return canonical(tradingE2EDecode[struct {
			Trading struct{ Connections []json.RawMessage }
		}](f.t, f.rest(http.MethodGet, "/trading", nil, http.StatusOK)).Trading.Connections)
	}
	for range 10 {
		before := readREST()
		viaMCP = canonical(tradingE2EStructured[struct{ Connections []json.RawMessage }](f.t,
			f.mcp("get_trading", map[string]any{}, false)).Connections)
		if readREST() == before {
			return before, viaMCP
		}
	}
	f.t.Fatal("trading connection changed between every pair of reads")
	return "", ""
}

// A venue stream that refuses authorization, or closes during the
// handshake, degrades the connection on REST and MCP alike with its cause
// and a connection_error diagnostic, and sends through it are still accepted.
func TestTradingStreamFailureEndToEnd(t *testing.T) {
	for _, tc := range []struct{ fault, cause string }{
		{"unauthorized", "alpaca: stream authorization refused"},
		{"close", "StatusPolicyViolation"},
	} {
		t.Run(tc.fault, func(t *testing.T) {
			f := newTradingE2E(t, false)
			f.venueMu.Lock()
			f.streamFault = tc.fault
			f.venueMu.Unlock()
			f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject",
				map[string]any{"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
					"balance": map[string]any{"mode": "absolute", "value": "1000"}}, http.StatusCreated)
			id := f.setupSend("REST")
			polltest.Until(func() bool {
				return f.health().State == "degraded"
			}) // degraded connection
			viaREST, viaMCP := f.connectionSurfaces()
			if viaREST != viaMCP {
				t.Fatalf("REST connection %s differs from MCP %s", viaREST, viaMCP)
			}
			tradingE2ENoSecrets(t, "degraded connection", []byte(viaREST))
			got := tradingE2EDecode[tradingE2EConnection](t, []byte(viaREST))
			if got.State != "degraded" || !strings.Contains(got.Error, tc.cause) {
				t.Fatalf("degraded connection = %+v, want error naming %q", got, tc.cause)
			}
			found := false
			for _, diag := range got.Diagnostics {
				found = found || (diag.Code == "connection_error" && diag.Detail == got.Error)
			}
			if !found {
				t.Fatalf("diagnostics = %+v, want connection_error with the error", got.Diagnostics)
			}
			raw := f.sendPaper("REST", tradingE2ESendInput(id), http.StatusCreated, "")
			sent := tradingE2EDecode[struct {
				Verdict   string
				VenueSend struct{ Status string }
			}](t, raw)
			f.venueMu.Lock()
			posts := len(f.posts)
			f.venueMu.Unlock()
			if sent.Verdict != "accept" || sent.VenueSend.Status != "acknowledged" || posts != 1 {
				t.Fatalf("send through a degraded connection = %s, venue posts %d", raw, posts)
			}
		})
	}
}

// A trade update the connector cannot read shows on REST and MCP alike as an
// unparsable_data diagnostic while the connection stays running.
func TestTradingUnparsableUpdateEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	f.createREST()
	f.venueMu.Lock()
	stream := f.stream
	f.venueMu.Unlock()
	if !sendFrame(t, f.ctx, stream, websocket.MessageText, `{"stream":"trade_updates","data":{"event":42}}`) {
		t.Fatal("broken update stream failed")
	}
	polltest.Until(func() bool {
		return len(f.health().Diagnostics) != 0
	}) // unparsable_data diagnostic
	viaREST, viaMCP := f.connectionSurfaces()
	if viaREST != viaMCP {
		t.Fatalf("REST connection %s differs from MCP %s", viaREST, viaMCP)
	}
	got := tradingE2EDecode[tradingE2EConnection](t, []byte(viaREST))
	if got.State != "running" || got.Error != "" || len(got.Diagnostics) != 1 {
		t.Fatalf("connection after a broken update = %+v", got)
	}
	if diag := got.Diagnostics[0]; diag.Code != "unparsable_data" || diag.Level != "error" ||
		diag.Kind != "provider" || diag.Title != venueStreamUnparsableTitle || diag.Detail != alpacaHeaderUnreadable {
		t.Fatalf("broken update diagnostic = %+v", diag)
	}
}
