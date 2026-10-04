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
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	fwtrading "go.openpit.dev/officer/framework/trading"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/signing"
)

func TestAlpacaRuntimeEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	caller := domain.Caller{Principal: domain.PrincipalOperator, Source: domain.SourceAPI}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	wait := func(what string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	assertDecimal := func(got, want string) {
		t.Helper()
		value, err := decimal.NewFromString(got)
		must(err)
		expected, err := decimal.NewFromString(want)
		must(err)
		if !value.Equal(expected) {
			t.Fatalf("decimal = %s, want %s", got, want)
		}
	}

	var venueMu sync.Mutex
	var posts []map[string]string
	orders := make(map[string]map[string]string)
	fills := make(map[string][]map[string]string)
	var fees []map[string]string
	streams := make(chan *websocket.Conn, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			conn := acceptAlpaca(t, w, r)
			if conn == nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
				return
			}
			select {
			case streams <- conn:
			case <-ctx.Done():
				return
			}
			_, _, _ = conn.Read(ctx)
			return
		}
		if r.Header.Get("APCA-API-KEY-ID") != "test-key-marker" ||
			r.Header.Get("APCA-API-SECRET-KEY") != "test-secret-marker" {
			t.Error("missing REST credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		venueMu.Lock()
		defer venueMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v2/assets/AAPL":
			_, _ = fmt.Fprint(w, `{"symbol":"AAPL","class":"us_equity","status":"active","tradable":true,"fractionable":true}`)
		case r.URL.Path == "/v2/orders" && r.Method == http.MethodPost:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode order: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			posts = append(posts, body)
			id := fmt.Sprintf("venue-%d", len(posts))
			orders[id] = map[string]string{
				"id": id, "client_order_id": body["client_order_id"], "status": "new",
				"qty": body["qty"], "filled_qty": "0",
			}
			_ = json.NewEncoder(w).Encode(orders[id])
		case r.URL.Path == "/v2/account/activities/FILL":
			page := fills[r.URL.Query().Get("order_id")]
			if page == nil {
				page = []map[string]string{}
			}
			_ = json.NewEncoder(w).Encode(page)
		case r.URL.Path == "/v2/account/activities":
			page := fees
			if r.URL.Query().Get("page_token") != "" || page == nil {
				page = []map[string]string{}
			}
			_ = json.NewEncoder(w).Encode(page)
		case strings.HasPrefix(r.URL.Path, "/v2/orders/"):
			order, ok := orders[strings.TrimPrefix(r.URL.Path, "/v2/orders/")]
			if !ok {
				t.Errorf("unknown venue order: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(order)
		default:
			t.Errorf("unexpected venue request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	dbPath := t.TempDir() + "/officer.db"
	st, err := sqlite.New(dbPath, domain.DefaultRealm)
	must(err)
	closeStore := st.Close
	t.Cleanup(func() { must(closeStore()) })
	must(st.Migrate(ctx))
	n, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, st,
		engine.NewOpenPitEngineBuildFunc(), func(err error) { t.Errorf("fatal node shutdown: %v", err) })
	must(err)
	closeNode := n.Close
	t.Cleanup(func() { must(closeNode()) })
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	must(err)
	signer, err := signing.New(realm)
	must(err)
	service, err := backend.New(n, nil, signer, engine.LockSettlementPrice)
	must(err)
	_, err = n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, caller)
	must(err)
	fundingID, err := domain.NewExternalID()
	must(err)
	funding, err := n.ApplyAdjustment(ctx, "desk", fundingID, domain.AdjustmentRequest{
		Asset: "USD", AverageEntryPrice: "1", RealizedPnl: "0",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "1000"},
	}, domain.MissingAccountReject, caller)
	must(err)
	if funding.Accepted == nil {
		t.Fatalf("funding rejected: %+v", funding)
	}
	_, err = n.CreateAsset(ctx, domain.Asset{Code: "AAPL"}, caller)
	must(err)
	connection, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "paper", Mode: domain.TradingModeTest,
		Credentials: tradingCredentials, Enabled: true,
	})
	must(err)
	must(realm.UpsertTradingInstrument(ctx, domain.TradingInstrument{
		Connection: connection.ExternalID, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}))
	must(realm.AddTradingAccess(ctx, domain.TradingAccess{Account: "desk", Connection: connection.ExternalID}))
	dest := domain.TradingDestination{Connection: connection.ExternalID, Route: `{"timeInForce":"day"}`}
	registry := fwtrading.NewRegistry()
	provider := AlpacaProvider()
	provider.Build = func(c domain.TradingConnection) (fwtrading.Connector, error) {
		return newAlpacaConnector(c, server.URL, "ws"+strings.TrimPrefix(server.URL, "http")+"/stream")
	}
	must(registry.Register(provider))
	reportSink := func(ctx context.Context, in domain.ExecutionReportInput) error {
		_, _, err := service.ApplyExecutionReport(auth.ContextWithCaller(ctx, auth.SystemCaller()), in)
		return err
	}
	start := func() (*fwtrading.Runtime, *websocket.Conn) {
		t.Helper()
		runtime, err := fwtrading.NewRuntime(registry, realm, reportSink,
			func(context.Context, domain.AccountID, domain.ExternalID, domain.AdjustmentRequest) error {
				t.Error("venue fee arrived before the app restart")
				return fmt.Errorf("unexpected fee before restart")
			}, slog.Default())
		must(err)
		t.Cleanup(runtime.Stop)
		must(runtime.Start(auth.ContextWithCaller(ctx, caller)))
		select {
		case stream := <-streams:
			return runtime, stream
		case <-ctx.Done():
			t.Fatal("venue stream did not connect")
			return nil, nil
		}
	}
	draft := func(quantity string) domain.Order {
		return domain.Order{Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: quantity, Price: "2"}
	}
	submit := func(quantity string) domain.Order {
		t.Helper()
		order, err := n.SubmitOrder(ctx, draft(quantity), domain.MissingAccountReject, caller)
		must(err)
		if order.Status != domain.OrderStatusCommitted {
			t.Fatalf("order was not held: %+v", order)
		}
		return order
	}
	detail := func(order domain.Order) domain.OrderDetail {
		t.Helper()
		result, err := realm.GetOrder(ctx, order.ExternalID)
		must(err)
		return result
	}
	assertBalances := func(usd, base string) {
		t.Helper()
		for asset, want := range map[string]string{"USD": usd, "AAPL": base} {
			balance, found, err := n.GetBalance(ctx, "desk", asset)
			must(err)
			if !found {
				t.Fatalf("%s balance missing", asset)
			}
			assertDecimal(balance.Available, want)
			assertDecimal(balance.Held, "0")
		}
	}

	runtime, stream := start()
	must(runtime.PreCheck(ctx, draft("10"), dest))
	first := submit("10")
	link, err := runtime.Send(ctx, first.ExternalID, dest)
	must(err)
	venueMu.Lock()
	firstPost := posts[0]
	postCount := len(posts)
	venueMu.Unlock()
	expected := map[string]string{"symbol": "AAPL", "qty": "10", "side": "buy",
		"type": "limit", "time_in_force": "day", "limit_price": "2", "client_order_id": link.ClientOrderID}
	if postCount != 1 || len(firstPost) != len(expected) {
		t.Fatalf("POST count or shape: %d, %+v", postCount, firstPost)
	}
	for field, want := range expected {
		if firstPost[field] != want {
			t.Fatalf("POST %s = %q, want %q", field, firstPost[field], want)
		}
	}
	if link.ClientOrderID == "" || link.VenueOrderID != "venue-1" {
		t.Fatalf("venue acknowledgement missing: %+v", link)
	}
	push := func(event, quantity, cumulative, price string) {
		t.Helper()
		raw := strings.NewReplacer(
			`"venue"`, fmt.Sprintf("%q", link.VenueOrderID),
			`"client"`, fmt.Sprintf("%q", link.ClientOrderID),
			`"3.000000000000000001"`, `"10"`,
		).Replace(updateFrame(event, quantity, cumulative, price))
		if !sendFrame(t, ctx, stream, websocket.MessageBinary, raw) {
			t.Fatal("could not stream venue fill")
		}
	}
	push("partial_fill", "3", "3", "1.5")
	wait("partial fill", func() bool { return detail(first).Order.Status == domain.OrderStatusPartiallyFilled })
	push("fill", "7", "10", "2")
	wait("final fill", func() bool { return detail(first).Order.Status == domain.OrderStatusFilled })
	firstDetail := detail(first)
	if len(firstDetail.Trades) != 2 {
		t.Fatalf("trade count = %d, want 2", len(firstDetail.Trades))
	}
	for i, want := range []struct{ quantity, price string }{{"3", "1.5"}, {"7", "2"}} {
		assertDecimal(firstDetail.Trades[i].Quantity, want.quantity)
		assertDecimal(firstDetail.Trades[i].Price, want.price)
	}
	for _, event := range firstDetail.Events {
		if event.Payload.ExecutionReport != nil && (event.Source != domain.SourceSystem || event.Principal != "") {
			t.Fatalf("execution report caller was not system: %+v", event)
		}
	}
	assertBalances("981.5", "10")

	rejected, err := n.SubmitOrder(ctx, draft("10000"), domain.MissingAccountReject, caller)
	must(err)
	if rejected.Status != domain.OrderStatusRejected {
		t.Fatalf("pre-trade did not reject: %+v", rejected)
	}
	if _, err := runtime.Send(ctx, rejected.ExternalID, dest); err == nil {
		t.Fatal("Send accepted a pre-trade-rejected order")
	}
	venueMu.Lock()
	postCount = len(posts)
	venueMu.Unlock()
	if postCount != 1 {
		t.Fatalf("rejected order reached the venue: %d POSTs", postCount)
	}

	second := submit("2")
	secondLink, err := runtime.Send(ctx, second.ExternalID, dest)
	must(err)
	runtime.Stop()
	venueMu.Lock()
	orders[secondLink.VenueOrderID]["status"] = "filled"
	orders[secondLink.VenueOrderID]["filled_qty"] = "2"
	fills[secondLink.VenueOrderID] = []map[string]string{{
		"id": "missed-fill", "activity_type": "FILL", "type": "fill", "qty": "2", "price": "2",
		"cum_qty": "2", "leaves_qty": "0", "transaction_time": time.Now().UTC().Format(time.RFC3339Nano),
		"order_id": secondLink.VenueOrderID, "order_status": "filled", "side": "buy", "symbol": "AAPL",
	}}
	fees = []map[string]string{{
		"id": "regulatory-fee", "activity_type": "FEE", "status": "executed", "currency": "USD",
		"net_amount": "-0.25", "date": time.Now().UTC().Format("2006-01-02"),
	}}
	venueMu.Unlock()
	if detail(second).Order.Status != domain.OrderStatusCommitted {
		t.Fatal("missed fill reached the stopped runtime")
	}
	must(n.Close())
	var logs bytes.Buffer
	startApp := func() *app.App {
		t.Helper()
		builder := app.NewBuilder()
		builder.SetStoreFactory(func() (store.Store, error) {
			var err error
			st, err = sqlite.New(dbPath, domain.DefaultRealm)
			return st, err
		})
		builder.SetNodeBuilder(func(ctx context.Context, st store.Store, fatal app.FatalShutdownHook) (node.Node, fwengine.Engine, error) {
			var eng fwengine.Engine
			var err error
			n, eng, err = node.NewLocalNode(ctx, domain.DefaultRealm, st, engine.NewOpenPitEngineBuildFunc(), fatal)
			return n, eng, err
		})
		builder.SetSigningFactory(func(st store.RealmStore) (fwsigning.Service, error) { return signing.New(st) })
		builder.SetServiceFactory(func(n node.Node, md backend.MarketDataRuntime, signer fwsigning.Service,
			_ *marketdata.Registry, _ *frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
			return backend.New(n, md, signer, engine.LockSettlementPrice)
		})
		builder.SetAuthorizer(httpx.AllowAll{})
		builder.SetCallerResolver(func(*http.Request) (domain.Caller, error) { return caller, nil })
		builder.SetRouteConfigBuilder(func(backend.ControlPlane, httpx.LogSource) app.RouteConfig { return app.RouteConfig{} })
		builder.SetSPAFactory(func() (fs.FS, error) { return fstest.MapFS{}, nil })
		must(builder.RegisterTradingProvider(provider))
		built, err := builder.Build(auth.ContextWithCaller(ctx, caller), slog.New(slog.NewTextHandler(&logs, nil)),
			func(err error) { t.Errorf("fatal app shutdown: %v", err) })
		must(err)
		t.Cleanup(func() { must(built.Close()) })
		realm, err = st.ForRealm(ctx, domain.DefaultRealm)
		must(err)
		select {
		case <-streams:
		case <-ctx.Done():
			t.Fatal("app venue stream did not connect")
		}
		return built
	}
	built := startApp()
	wait("reconciled fill", func() bool { return detail(second).Order.Status == domain.OrderStatusFilled })
	secondDetail := detail(second)
	if trades := secondDetail.Trades; len(trades) != 1 {
		t.Fatalf("reconciled trades = %d, want 1", len(trades))
	} else {
		assertDecimal(trades[0].Quantity, "2")
		assertDecimal(trades[0].Price, "2")
	}
	for _, event := range secondDetail.Events {
		if event.Payload.ExecutionReport != nil && (event.Source != domain.SourceSystem || event.Principal != "") {
			t.Fatalf("reconciled report caller was not system: %+v", event)
		}
	}
	wait("app fee adjustment", func() bool {
		records, err := realm.ListAdjustments(ctx, "desk", domain.SourceSystem, 10)
		must(err)
		return len(records) == 1
	})
	assertBalances("977.25", "12")
	must(built.Close())
	venueMu.Lock()
	// The core decimal maximum is valid input, but adding it to the funded
	// balance overflows and produces a rejected adjustment record.
	fees = append(fees, map[string]string{
		"id": "rejected-fee", "activity_type": "FEE", "status": "executed", "currency": "USD",
		"net_amount": "79228162514264337593543950335", "date": time.Now().UTC().Format("2006-01-02"),
	})
	venueMu.Unlock()
	built = startApp()
	wait("core-rejected app fee", func() bool {
		records, err := realm.ListAdjustments(ctx, "desk", domain.SourceSystem, 10)
		must(err)
		return len(records) == 2
	})
	assertBalances("977.25", "12")
	records, err := realm.ListAdjustments(ctx, "desk", domain.SourceSystem, 10)
	must(err)
	feeID := domain.ExternalID("trading:" + connection.ExternalID.String() + ":fee:regulatory-fee")
	if len(records) != 2 {
		t.Fatalf("venue fee adjustment = %+v", records)
	}
	for _, record := range records {
		if record.Asset != "USD" || record.Principal != "" || record.Request.Balance == nil ||
			record.Request.Balance.Mode != domain.AdjustmentModeDelta {
			t.Fatalf("venue fee adjustment = %+v", record)
		}
		switch record.ExternalID {
		case feeID:
			if record.Accepted == nil || record.Rejected != nil {
				t.Fatalf("venue fee was not accepted: %+v", record)
			}
			assertDecimal(record.Request.Balance.Value, "-0.25")
		case domain.ExternalID("trading:" + connection.ExternalID.String() + ":fee:rejected-fee"):
			if record.Rejected == nil || record.Accepted != nil {
				t.Fatalf("venue fee was not core-rejected: %+v", record)
			}
			if !strings.Contains(record.Rejected.Reason, "overflow") {
				t.Fatalf("unexpected core fee rejection: %+v", record.Rejected)
			}
			assertDecimal(record.Request.Balance.Value, "79228162514264337593543950335")
		default:
			t.Fatalf("unexpected system adjustment: %+v", record)
		}
	}
	venueMu.Lock()
	postCount = len(posts)
	venueMu.Unlock()
	if postCount != 2 {
		t.Fatalf("restart resent an order: %d POSTs", postCount)
	}
	must(built.Close())
	if !strings.Contains(logs.String(), "fee rejected-fee not applied: rejected by the core:") {
		t.Fatalf("app fee rejection was not reported: %s", logs.String())
	}
}
