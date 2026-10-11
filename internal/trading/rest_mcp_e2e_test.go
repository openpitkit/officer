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
	t              *testing.T
	ctx            context.Context
	caller         domain.Caller
	builder        *app.Builder
	app            *app.App
	router         http.Handler
	realm          store.RealmStore
	logs           tradingE2ELog
	venueMu        sync.Mutex
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

func newTradingE2E(t *testing.T, sealed bool) *tradingE2E {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	f := &tradingE2E{t: t, ctx: ctx, caller: domain.Caller{Principal: domain.PrincipalOperator, Source: domain.SourceAPI}, requests: make(map[string]int), streams: make(chan struct{}, 3)}
	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			conn := acceptAlpaca(t, w, r)
			if conn == nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			if !readAuth(t, ctx, conn) {
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
					_ = conn.Close(websocket.StatusPolicyViolation, "refused test-key-marker test-secret-marker")
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
	provider.Build = func(c domain.TradingConnection) (fwtrading.Connector, error) {
		return newAlpacaConnector(c, venue.URL, "ws"+strings.TrimPrefix(venue.URL, "http")+"/stream")
	}
	dbPath := t.TempDir() + "/officer.db"
	var options []sqlite.Option
	if sealed {
		key, err := secret.ParseMasterKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
		tradingE2EMust(t, err)
		options = append(options, sqlite.WithMasterKey(key))
	}
	f.builder = app.NewBuilder()
	f.builder.SetStoreFactory(func() (store.Store, error) { return sqlite.New(dbPath, domain.DefaultRealm, options...) })
	f.builder.SetNodeBuilder(func(ctx context.Context, st store.Store, fatal app.FatalShutdownHook) (node.Node, fwengine.Engine, error) {
		var err error
		f.realm, err = st.ForRealm(ctx, domain.DefaultRealm)
		if err != nil {
			return nil, nil, err
		}
		return node.NewLocalNode(ctx, domain.DefaultRealm, st, engine.NewOpenPitEngineBuildFunc(), fatal)
	})
	f.builder.SetSigningFactory(func(st store.RealmStore) (fwsigning.Service, error) { return signing.New(st) })
	f.builder.SetServiceFactory(func(_ context.Context, n node.Node, md backend.MarketDataRuntime, rt backend.TradingRuntime, signer fwsigning.Service,
		_ *marketdata.Registry, tools *frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
		return backend.New(n, md, signer, engine.LockSettlementPrice, backend.WithTradingRuntime(rt), backend.WithMCPCatalogProvider(tools))
	})
	f.builder.AddToolRegistrar(func(reg *frameworkmcp.ToolRegistry, _ frameworkmcp.Source) { mcptools.RegisterTools(reg, nil) })
	f.builder.SetAuthorizer(httpx.AllowAll{})
	f.builder.SetCallerResolver(func(*http.Request) (domain.Caller, error) { return f.caller, nil })
	f.builder.SetRouteConfigBuilder(httpapi.RouteConfig)
	f.builder.SetSPAFactory(func() (fs.FS, error) { return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test")}}, nil })
	tradingE2EMust(t, f.builder.RegisterTradingProvider(provider))
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&f.logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
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
	var err error
	f.app, err = f.builder.Build(auth.ContextWithCaller(f.ctx, f.caller), slog.Default(), func(err error) { f.t.Errorf("fatal app shutdown: %v", err) })
	tradingE2EMust(f.t, err)
	connections, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(f.t, err)
	for _, connection := range connections {
		if connection.Enabled {
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
		select {
		case <-f.streams:
			f.pendingStreams--
		case <-f.ctx.Done():
			f.t.Fatal("venue stream handshake did not complete")
		}
	}
}

func (f *tradingE2E) close() {
	f.t.Helper()
	defer func() { tradingE2EMust(f.t, f.app.Close()) }()
	f.waitStreams()
}

func (f *tradingE2E) rest(method, path string, body any, status int) []byte {
	f.t.Helper()
	raw, err := json.Marshal(body)
	tradingE2EMust(f.t, err)
	r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw)).WithContext(f.ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if method == http.MethodPost && path == "/trading/connections" && w.Code == http.StatusCreated {
		if tradingE2EDecode[tradingE2ECreated](f.t, w.Body.Bytes()).Connection.Enabled {
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
		f.t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w.Body.Bytes()
}

// Uses the integration harness's Build/NewInMemoryTransports path with the
// real service.
func (f *tradingE2E) callMCP(name string, input any) (*sdkmcp.CallToolResult, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
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
	session, err := client.Connect(ctx, clientTransport)
	tradingE2EMust(f.t, err)
	result, callErr := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: input})
	_ = session.Close()
	cancel()
	if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
		f.t.Fatal(err)
	}
	return result, callErr
}

func (f *tradingE2E) mcp(name string, input any, wantError bool) *sdkmcp.CallToolResult {
	f.t.Helper()
	result, callErr := f.callMCP(name, input)
	tradingE2EMust(f.t, callErr)
	if name == "create_trading_connection" && !result.IsError {
		if tradingE2EStructured[tradingE2ECreated](f.t, result).Connection.Enabled {
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

func TestTradingRESTEndToEnd(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(fmt.Sprintf("sealed=%t", sealed), func(t *testing.T) {
			f := newTradingE2E(t, sealed)
			id := f.createREST()
			tradingE2EAssertStorage(t, tradingE2EDecode[map[string]bool](t, f.rest(http.MethodGet, "/credential-storage", nil, http.StatusOK)), sealed)
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

func TestTradingMCPEndToEnd(t *testing.T) {
	f := newTradingE2E(t, true)
	input := tradingE2ECreateInput("Paper", "paper")
	result := f.mcp("create_trading_connection", input, false)
	if len(result.Content) != 1 {
		t.Fatalf("disabled tool content = %v", result.Content)
	}
	if text, ok := result.Content[0].(*sdkmcp.TextContent); !ok || !strings.Contains(text.Text, "disabled") {
		t.Fatalf("disabled refusal = %v", result.Content)
	}
	connections, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	if len(connections) != 0 {
		t.Fatal("disabled mutating tool created a connection")
	}
	f.venueMu.Lock()
	accountCalls := f.requests["/v2/account"]
	f.venueMu.Unlock()
	if accountCalls != 0 {
		t.Fatal("disabled mutating tool reached the venue")
	}
	for _, name := range []string{"create_trading_connection", "check_trading_connection", "set_trading_instrument", "grant_trading_access"} {
		f.rest(http.MethodPut, "/mcp-access/"+name, map[string]any{"enabled": true}, http.StatusOK)
	}
	c := tradingE2EStructured[tradingE2ECreated](t, f.mcp("create_trading_connection", input, false)).Connection
	tradingE2EAssertCreated(t, c)
	tradingE2EAssertStorage(t, tradingE2EStructured[map[string]bool](t, f.mcp("get_credential_storage", map[string]any{}, false)), true)
	id := c.ID
	check := tradingE2EStructured[struct {
		VenueIdentity string
		VenueStatus   string
	}](t,
		f.mcp("check_trading_connection", map[string]any{"connection": id}, false))
	if check.VenueIdentity != "paper-account" || check.VenueStatus != "ACTIVE" {
		t.Fatalf("MCP check = %+v", check)
	}
	f.mcp("set_trading_instrument", map[string]any{"connection": id, "externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, false)
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
	f.mcp("grant_trading_access", map[string]any{"connection": id, "account": "desk", "venueAccount": ""}, false)
	tradingE2EAssertDestinations(t, tradingE2EStructured[tradingE2EDestinations](t, f.mcp("list_trading_destinations", map[string]any{"account": "desk", "mode": "paper"}, false)), id)
	tradingE2EAssertSnapshot(t, tradingE2EStructured[tradingE2ESnapshot](t, f.mcp("get_trading", map[string]any{}, false)), id)
	f.assertVenueReads()
}

func TestTradingRefusalsEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	live := tradingE2ECreateInput("Live", "live")
	errBody := tradingE2EDecode[struct{ Error struct{ Code string } }](t, f.rest(http.MethodPost, "/trading/connections", live, http.StatusForbidden))
	if errBody.Error.Code != "live_trading_unavailable" {
		t.Fatalf("live error code = %s", errBody.Error.Code)
	}
	f.rest(http.MethodPut, "/mcp-access/create_trading_connection", map[string]any{"enabled": true}, http.StatusOK)
	result := f.mcp("create_trading_connection", live, true)
	if len(result.Content) != 1 {
		t.Fatalf("live MCP content = %v", result.Content)
	}
	if text, ok := result.Content[0].(*sdkmcp.TextContent); !ok || !strings.HasPrefix(text.Text, "live_trading_unavailable: ") {
		t.Fatalf("live MCP error = %v", result.Content)
	}
	f.createREST()
	second := tradingE2ECreateInput("Second", "paper")
	second["credentials"] = map[string]string{"apiKey": "second-key-marker", "apiSecret": "second-secret-marker"}
	errBody = tradingE2EDecode[struct{ Error struct{ Code string } }](t,
		f.rest(http.MethodPost, "/trading/connections", second, http.StatusConflict))
	if errBody.Error.Code != "conflict" {
		t.Fatalf("duplicate venue error = %s", errBody.Error.Code)
	}
	duplicate := f.mcp("create_trading_connection", tradingE2ECreateInput("Third", "paper"), true)
	if len(duplicate.Content) != 1 {
		t.Fatalf("duplicate MCP content = %v", duplicate.Content)
	}
	if text, ok := duplicate.Content[0].(*sdkmcp.TextContent); !ok || !strings.Contains(text.Text, `trading connection venue identity "paper-account": already exists`) {
		t.Fatalf("duplicate MCP error = %v", duplicate.Content)
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
	// A single-space value must not be redacted out of the refusal text.
	blank["credentials"] = map[string]string{"apiKey": "test-key-marker", "apiSecret": " "}
	result := f.mcp("create_trading_connection", blank, true)
	if len(result.Content) != 1 {
		t.Fatalf("blank secret MCP content = %v", result.Content)
	}
	if text, ok := result.Content[0].(*sdkmcp.TextContent); !ok ||
		text.Text != "validation: trading: build connector: alpaca: apiSecret is required" {
		t.Fatalf("blank secret MCP error = %v", result.Content)
	}
	connections, err := f.realm.ListTradingConnections(f.ctx)
	tradingE2EMust(t, err)
	f.venueMu.Lock()
	accountCalls := f.requests["/v2/account"]
	f.venueMu.Unlock()
	if len(connections) != 0 || accountCalls != 0 {
		t.Fatalf("blank credentials created %d connections and reached the venue %d times", len(connections), accountCalls)
	}
}

func TestTradingMissingAssetEndToEnd(t *testing.T) {
	f := newTradingE2E(t, false)
	id := f.createREST()
	f.rest(http.MethodPost, "/assets", map[string]any{"code": "BTC"}, http.StatusCreated)
	path := "/trading/connections/" + id + "/instruments"
	instrument := map[string]any{"externalSymbol": "BTC/USDT", "baseAsset": "BTC", "quoteAsset": "USDT", "enabled": true}
	missing := tradingE2EDecode[struct {
		Error struct {
			Code  string `json:"code"`
			Asset string `json:"asset"`
		} `json:"error"`
	}](t, f.rest(http.MethodPut, path, instrument, http.StatusNotFound))
	if missing.Error.Code != "asset_missing" || missing.Error.Asset != "USDT" {
		t.Fatalf("missing asset error = %+v", missing.Error)
	}
	f.rest(http.MethodPut, "/mcp-access/set_trading_instrument", map[string]any{"enabled": true}, http.StatusOK)
	result := f.mcp("set_trading_instrument", map[string]any{"connection": id, "externalSymbol": "BTC/USDT", "baseAsset": "BTC", "quoteAsset": "USDT", "enabled": true}, true)
	if len(result.Content) != 1 {
		t.Fatalf("missing asset MCP content = %v", result.Content)
	}
	if text, ok := result.Content[0].(*sdkmcp.TextContent); !ok ||
		!strings.HasPrefix(text.Text, "asset_missing: ") || !strings.Contains(text.Text, `"USDT"`) {
		t.Fatalf("missing asset MCP error = %v", result.Content)
	}
	assets, err := f.realm.ListAssets(f.ctx)
	tradingE2EMust(t, err)
	for _, asset := range assets {
		if asset.Code == "USDT" {
			t.Fatal("refused instrument created its missing asset")
		}
	}
	f.rest(http.MethodPost, "/assets", map[string]any{"code": "USDT"}, http.StatusCreated)
	f.rest(http.MethodPut, path, instrument, http.StatusOK)
}

func (f *tradingE2E) wait(what string, check func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	logs := f.logs.String()
	if len(logs) > 1500 {
		logs = logs[len(logs)-1500:]
	}
	f.t.Logf("slog tail: %s", logs)
	f.t.Fatalf("timed out waiting for %s", what)
}

func (f *tradingE2E) sendPaper(surface string, input map[string]any, status int, code string) []byte {
	f.t.Helper()
	if surface == "REST" {
		raw := f.rest(http.MethodPost, "/orders/send-paper", input, status)
		if code != "" {
			got := tradingE2EDecode[struct{ Error struct{ Code string } }](f.t, raw)
			if got.Error.Code != code {
				f.t.Fatalf("send error code = %s, want %s", got.Error.Code, code)
			}
		}
		return raw
	}
	result := f.mcp("send_paper_order", input, status != http.StatusCreated)
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

func (f *tradingE2E) setupSend(surface string) string {
	f.t.Helper()
	var id string
	if surface == "REST" {
		id = f.createREST()
		path := "/trading/connections/" + id
		f.rest(http.MethodPut, path+"/instruments", map[string]any{"externalSymbol": "AAPL",
			"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, http.StatusOK)
		f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
	} else {
		for _, name := range []string{"create_trading_connection", "set_trading_instrument",
			"grant_trading_access", "send_paper_order", "set_trading_fee_account"} {
			f.rest(http.MethodPut, "/mcp-access/"+name, map[string]any{"enabled": true}, http.StatusOK)
		}
		id = tradingE2EStructured[tradingE2ECreated](f.t,
			f.mcp("create_trading_connection", tradingE2ECreateInput("Paper", "paper"), false)).Connection.ID
		f.mcp("set_trading_instrument", map[string]any{"connection": id, "externalSymbol": "AAPL",
			"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, false)
		f.mcp("grant_trading_access", map[string]any{"connection": id, "account": "desk"}, false)
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
			if surface == "REST" {
				f.rest(http.MethodPut, "/trading/connections/"+id+"/fee-account",
					map[string]any{"account": "fees"}, http.StatusOK)
			} else {
				f.mcp("set_trading_fee_account", map[string]any{"connection": id, "account": "fees"}, false)
			}
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
			f.wait("paper order filled", func() bool {
				detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(sent.ID))
				tradingE2EMust(t, err)
				return detail.Order.Status == domain.OrderStatusFilled
			})
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
			f.wait("sent-order fee booked after restart", func() bool {
				rows, err := f.realm.ListAdjustments(f.ctx, "fees", domain.SourceSystem, 10)
				tradingE2EMust(t, err)
				return len(rows) == 1
			})
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

func TestTradingSendPaperRefusalsEndToEnd(t *testing.T) {
	for _, surface := range []string{"REST", "MCP"} {
		t.Run(surface, func(t *testing.T) {
			f := newTradingE2E(t, true)
			if surface == "MCP" {
				result := f.mcp("send_paper_order", tradingE2ESendInput(""), false)
				text, ok := result.Content[0].(*sdkmcp.TextContent)
				if !ok || !strings.Contains(text.Text, "disabled") {
					t.Fatal("paper send must default to disabled")
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
					if err == nil || !strings.Contains(err.Error(), "unmarshaling") {
						t.Fatal("MCP must refuse a non-object route at decode")
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
			tradingE2EMust(t, f.realm.PutTradingAccess(f.ctx, domain.TradingAccess{Account: "desk", Connection: live.ExternalID}))
			input = tradingE2ESendInput(live.ExternalID.String())
			input["id"] = "wrong-mode"
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
				f.wait("fee booked to configured account", func() bool {
					rows, err := f.realm.ListAdjustments(f.ctx, "fees", domain.SourceSystem, 10)
					tradingE2EMust(t, err)
					return len(rows) == 1
				})
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
				f.wait("venue_fee_skipped audit", func() bool {
					rows, err := f.realm.ListAuditFiltered(f.ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionVenueFeeSkipped}}, 10)
					tradingE2EMust(t, err)
					return len(rows) == 1
				})
				rows, err := f.realm.ListAuditFiltered(f.ctx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionVenueFeeSkipped}}, 10)
				tradingE2EMust(t, err)
				if rows[0].Source != domain.SourceSystem || rows[0].Actor != "" || !strings.Contains(rows[0].Detail, id) || !strings.Contains(rows[0].Detail, "regulatory-fee") {
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
	group := map[string]any{"connection": id, "accountGroup": "team"}
	refused := f.mcp("grant_trading_access", group, true)
	if text, ok := refused.Content[0].(*sdkmcp.TextContent); !ok || !strings.HasPrefix(text.Text, "fee_account_required: ") {
		t.Fatalf("group grant without a fee account = %v", refused.Content)
	}
	feeAccount := "/trading/connections/" + id + "/fee-account"
	f.rest(http.MethodPut, feeAccount, map[string]any{"account": "fees"}, http.StatusOK)
	if granted := tradingE2EStructured[map[string]any](t, f.mcp("grant_trading_access", group, false)); !reflect.DeepEqual(granted, group) {
		t.Fatalf("group grant output = %v", granted)
	}
	// team has a single member, so only the group grant refuses the clear.
	type refusal struct{ Error struct{ Code string } }
	if code := tradingE2EDecode[refusal](t, f.rest(http.MethodDelete, feeAccount, nil, http.StatusConflict)).Error.Code; code != "fee_account_required" {
		t.Fatalf("fee account clear under a group grant: %s", code)
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
			f.wait("degraded connection", func() bool { return f.health().State == "degraded" })
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
	f.wait("unparsable_data diagnostic", func() bool { return len(f.health().Diagnostics) != 0 })
	viaREST, viaMCP := f.connectionSurfaces()
	if viaREST != viaMCP {
		t.Fatalf("REST connection %s differs from MCP %s", viaREST, viaMCP)
	}
	got := tradingE2EDecode[tradingE2EConnection](t, []byte(viaREST))
	if got.State != "running" || got.Error != "" || len(got.Diagnostics) != 1 {
		t.Fatalf("connection after a broken update = %+v", got)
	}
	if diag := got.Diagnostics[0]; diag.Code != "unparsable_data" || diag.Level != "error" ||
		diag.Kind != "provider" || diag.Title != alpacaUnparsableTitle || diag.Detail != alpacaHeaderUnreadable {
		t.Fatalf("broken update diagnostic = %+v", diag)
	}
}
