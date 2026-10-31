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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/signing"
)

type etoroRuntimeTest struct {
	t       *testing.T
	ctx     context.Context
	caller  domain.Caller
	dbPath  string
	st      store.Store
	realm   store.RealmStore
	n       node.Node
	runtime *fwtrading.Runtime
	server  *httptest.Server
	streams chan *websocket.Conn
	stream  *websocket.Conn
	c       *etoroConnector
	dest    domain.TradingDestination

	mu           sync.Mutex
	posts        []etoroTestRequest
	lookups      []string
	requestID    string
	action       string
	status       int
	executions   int
	loseResponse bool
	lookupReady  bool
	postAccepted chan struct{}
	postRelease  chan struct{}
	reports      []domain.ExecutionReportInput
}

func (h *etoroRuntimeTest) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

func newEtoroRuntimeTest(t *testing.T) *etoroRuntimeTest {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	h := &etoroRuntimeTest{
		t: t, ctx: ctx, caller: domain.Caller{Principal: domain.PrincipalOperator, Source: domain.SourceAPI},
		dbPath:  t.TempDir() + "/officer.db?_pragma=journal_mode(MEMORY)&_pragma=synchronous(OFF)",
		streams: make(chan *websocket.Conn, 4), status: 1, lookupReady: true,
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.server.Close)
	t.Cleanup(h.close)
	h.open()
	_, err := h.n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, h.caller)
	h.must(err)
	_, err = h.n.CreateAsset(ctx, domain.Asset{Code: "AAPL"}, h.caller)
	h.must(err)
	for asset, amount := range map[string]string{"USD": "1000", "EUR": "100", "AAPL": "10"} {
		id, err := domain.NewExternalID()
		h.must(err)
		funding, err := h.n.ApplyAdjustment(ctx, "desk", id, domain.AdjustmentRequest{
			Type: domain.AdjustmentTypeDeposit, Asset: asset, AverageEntryPrice: "1", RealizedPnl: "0",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: amount},
		}, domain.MissingAccountReject, h.caller)
		h.must(err)
		if funding.Accepted == nil {
			t.Fatalf("funding rejected: %+v", funding)
		}
	}
	connection, err := h.realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: "etoro", Label: "demo", Mode: domain.TradingModePaper, VenueIdentity: "demo-account",
		Credentials: etoroTestCredentials, Enabled: true,
	})
	h.must(err)
	h.must(h.realm.UpsertTradingInstrument(ctx, domain.TradingInstrument{
		Connection: connection.ExternalID, ExternalSymbol: "1001", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}))
	h.must(h.realm.PutTradingAccess(ctx, domain.TradingAccess{Account: "desk", Connection: connection.ExternalID}))
	h.dest = domain.TradingDestination{Connection: connection.ExternalID, Route: "{}"}
	h.start()
	return h
}

func (h *etoroRuntimeTest) open() {
	var err error
	h.st, err = sqlite.New(h.dbPath, domain.DefaultRealm)
	h.must(err)
	h.must(h.st.Migrate(h.ctx))
	h.n, _, err = node.NewLocalNode(h.ctx, domain.DefaultRealm, h.st,
		engine.NewOpenPitEngineBuildFunc(), func(err error) { h.t.Errorf("fatal node shutdown: %v", err) })
	h.must(err)
	h.realm, err = h.st.ForRealm(h.ctx, domain.DefaultRealm)
	h.must(err)
}

func (h *etoroRuntimeTest) start() {
	signer, err := signing.New(h.realm)
	h.must(err)
	service, err := backend.New(h.n, nil, signer, engine.LockSettlementPrice)
	h.must(err)
	registry := fwtrading.NewRegistry()
	// C4 intentionally registers only in this test's registry; C5 owns the
	// production provider and domain constant.
	h.must(registry.Register(fwtrading.Provider{Type: "etoro", Title: "eToro", Build: func(connection domain.TradingConnection) (fwtrading.Connector, error) {
		c, err := newEtoroConnector(connection, h.server.URL)
		if err != nil {
			return nil, err
		}
		c.streamURL = "ws" + strings.TrimPrefix(h.server.URL, "http") + "/stream"
		h.c = c
		return c, nil
	}}))
	h.runtime, err = fwtrading.NewRuntime(registry, h.realm,
		func(ctx context.Context, in domain.ExecutionReportInput) error {
			h.mu.Lock()
			h.reports = append(h.reports, in)
			h.mu.Unlock()
			_, _, err := service.ApplyExecutionReport(auth.ContextWithCaller(ctx, auth.SystemCaller()), in)
			return err
		},
		func(context.Context, domain.AccountID, domain.ExternalID, domain.AdjustmentRequest) error {
			h.t.Error("eToro emitted a separate fee adjustment")
			return errors.New("unexpected fee adjustment")
		}, func(context.Context, domain.ExternalID, string, string) error {
			h.t.Error("eToro emitted an unlinked fee")
			return errors.New("unexpected fee")
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.must(err)
	h.must(h.runtime.Start(auth.ContextWithCaller(h.ctx, h.caller)))
	select {
	case h.stream = <-h.streams:
	case <-h.ctx.Done():
		h.t.Fatal("eToro runtime stream did not connect")
	}
	base, found, err := h.realm.GetAsset(h.ctx, "AAPL")
	h.must(err)
	if !found {
		h.t.Fatal("AAPL asset missing")
	}
	quote, found, err := h.realm.GetAsset(h.ctx, "USD")
	h.must(err)
	if !found {
		h.t.Fatal("USD asset missing")
	}
	h.must(h.n.CurrentMarketDataSink().Push(marketdata.QuoteUpdate{
		Base: base.EngineAssetID, Quote: quote.EngineAssetID,
		AsOf: time.Now(), Mark: "2", Bid: "2", Ask: "2",
	}))
}

func (h *etoroRuntimeTest) close() {
	if h.runtime != nil {
		h.runtime.Stop()
		h.runtime = nil
	}
	if h.n != nil {
		h.must(h.n.Close())
		h.n = nil
	}
	if h.st != nil {
		h.must(h.st.Close())
		h.st = nil
	}
}

func (h *etoroRuntimeTest) restart() {
	h.close()
	h.open()
	h.start()
}

func (h *etoroRuntimeTest) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/stream" {
		conn := acceptAlpaca(h.t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if etoroHandshake(h.t, h.ctx, conn) {
			select {
			case h.streams <- conn:
			case <-h.ctx.Done():
				return
			}
			_, _, _ = conn.Read(h.ctx)
		}
		return
	}
	var credentials map[string]string
	if json.Unmarshal([]byte(etoroTestCredentials), &credentials) != nil ||
		r.Header.Get("x-api-key") != credentials["apiKey"] || r.Header.Get("x-user-key") != credentials["userKey"] {
		h.t.Error("missing eToro REST credentials")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v2/trading/info/demo/eligibility":
		_, _ = io.WriteString(w, etoroTestEligibility)
	case "/api/v1/trading/info/demo/portfolio":
		_, _ = io.WriteString(w, `{"clientPortfolio":{"positions":[`+etoroTestPosition+`]}}`)
	case "/api/v1/trading/info/demo/aggregate-portfolio":
		_, _ = io.WriteString(w, `{"accountCurrency":"EUR"}`)
	case "/api/v3/trading/execution/demo/orders", "/api/v1/trading/execution/demo/market-close-orders/positions/50":
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost {
			h.t.Error("invalid eToro order request")
			w.WriteHeader(400)
			return
		}
		h.mu.Lock()
		h.posts = append(h.posts, etoroTestRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("x-request-id"), string(body)})
		h.requestID = r.Header.Get("x-request-id")
		h.action = "open"
		if strings.Contains(r.URL.Path, "/market-close-orders/") {
			h.action = "close"
		}
		action, lose, accepted, release := h.action, h.loseResponse, h.postAccepted, h.postRelease
		h.mu.Unlock()
		if accepted != nil {
			close(accepted)
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if lose {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				h.t.Error("could not drop accepted POST response")
				return
			}
			_ = conn.Close()
			return
		}
		if action == "close" {
			_, _ = io.WriteString(w, `{"orderForClose":{"orderID":9007199254740993}}`)
		} else {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"orderId":9007199254740993}`)
		}
	case "/api/v2/trading/info/demo/orders:lookup":
		h.mu.Lock()
		defer h.mu.Unlock()
		h.lookups = append(h.lookups, r.URL.RawQuery)
		if !h.lookupReady {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"message":"lookup not ready"}`)
			return
		}
		if r.URL.Query().Get("orderId") != "9007199254740993" &&
			(r.URL.Query().Get("referenceId") == "" || r.URL.Query().Get("referenceId") != h.requestID) {
			h.t.Error("lookup must use venue orderId or original referenceId")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		executions := []json.RawMessage{}
		if h.action == "open" {
			if h.executions >= 1 {
				executions = append(executions, json.RawMessage(`{"positionId":2,"openingData":{"units":0.1,"avgPrice":1.5,"executionTime":"2026-10-02T10:00:00Z","fees":0.01,"taxes":0}}`))
			}
			if h.executions == 2 {
				// Deliberately reversed on the wire; Lookup sorts executions.
				executions = append([]json.RawMessage{json.RawMessage(`{"positionId":3,"openingData":{"units":0.2,"avgPrice":2,"executionTime":"2026-10-02T10:00:01Z","fees":0.02,"taxes":0}}`)}, executions...)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"orderId": json.Number("9007199254740993"), "action": h.action,
			"requestedUnits": json.Number("0.3"), "status": map[string]int{"id": h.status}, "positionExecutions": executions,
		})
	case "/api/v1/trading/info/demo/close-orders/9007199254740993":
		h.mu.Lock()
		defer h.mu.Unlock()
		position := `{"positionID":50}`
		if h.status == 3 {
			position = `{"positionID":50,"units":0.3,"rate":2,"occurred":"2026-10-02T10:00:00Z"}`
		}
		_, _ = fmt.Fprintf(w, `{"orderID":9007199254740993,"statusID":%d,"positions":[%s]}`, h.status, position)
	default:
		h.t.Errorf("unexpected eToro request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *etoroRuntimeTest) draft(side domain.OrderSide, quantity, price string) domain.Order {
	return domain.Order{Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: side, AmountKind: domain.OrderAmountKindQuantity, AmountValue: quantity, Price: price}
}

func (h *etoroRuntimeTest) submit(side domain.OrderSide, price string) domain.Order {
	h.t.Helper()
	draft := h.draft(side, "0.3", price)
	h.must(h.runtime.PreCheck(h.ctx, draft, h.dest))
	order, err := h.n.SubmitOrder(h.ctx, draft, domain.MissingAccountReject, h.caller)
	h.must(err)
	if order.Status != domain.OrderStatusCommitted {
		h.t.Fatalf("pre-trade did not commit order: %+v", order)
	}
	return order
}

func (h *etoroRuntimeTest) detail(order domain.Order) domain.OrderDetail {
	h.t.Helper()
	detail, err := h.realm.GetOrder(h.ctx, order.ExternalID)
	h.must(err)
	return detail
}

func (h *etoroRuntimeTest) state(status, executions int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status, h.executions = status, executions
}

func (h *etoroRuntimeTest) push() {
	etoroOrderPush(h.t, h.ctx, h.stream, "9007199254740993")
}

func (h *etoroRuntimeTest) streamBarrier() {
	h.t.Helper()
	etoroOrderPush(h.t, h.ctx, h.stream, "42")
	waitForTrading(h.t, "stream event delivery barrier", func() bool {
		h.c.ordersMu.Lock()
		defer h.c.ordersMu.Unlock()
		return slices.Equal(h.c.held, []string{"42"})
	})
}

func (h *etoroRuntimeTest) waitStatus(order domain.Order, status domain.OrderStatus) {
	h.t.Helper()
	waitForTrading(h.t, string(status), func() bool { return h.detail(order).Order.Status == status })
}

func (h *etoroRuntimeTest) assertBooked(order domain.Order, count int, usd, base, eur string) {
	h.t.Helper()
	detail := h.detail(order)
	if len(detail.Trades) != count {
		h.t.Fatalf("executions must book exactly once: got %d trades, want %d", len(detail.Trades), count)
	}
	wants := []struct{ quantity, price, commission string }{{"0.1", "1.5", "0.01"}, {"0.2", "2", "0.02"}}
	if order.Side == domain.OrderSideSell {
		wants = []struct{ quantity, price, commission string }{{"0.3", "2", ""}}
	}
	for i, trade := range detail.Trades {
		assertDecimal(h.t, trade.Quantity, wants[i].quantity)
		assertDecimal(h.t, trade.Price, wants[i].price)
		if trade.Side != order.Side || trade.Account != "desk" || trade.BaseAsset != "AAPL" || trade.QuoteAsset != "USD" {
			h.t.Fatalf("execution trade identity changed: %+v", trade)
		}
		if wants[i].commission == "" {
			if trade.Commission != nil {
				h.t.Fatal("close execution invented a commission")
			}
		} else {
			if trade.Commission == nil || trade.Commission.Currency != "EUR" {
				h.t.Fatal("execution commission must use aggregate portfolio accountCurrency")
			}
			assertDecimal(h.t, trade.Commission.Amount, wants[i].commission)
		}
	}
	if count > 0 && order.Side == domain.OrderSideBuy {
		if len(detail.Order.CommissionSubtotals) != 1 || detail.Order.CommissionSubtotals[0].Currency != "EUR" {
			h.t.Fatalf("commission subtotal missing: %+v", detail.Order.CommissionSubtotals)
		}
		fee := "0.01"
		if count == 2 {
			fee = "0.03"
		}
		assertDecimal(h.t, detail.Order.CommissionSubtotals[0].Amount, fee)
	} else if len(detail.Order.CommissionSubtotals) != 0 {
		h.t.Fatal("unexpected commission subtotal")
	}
	h.assertBalances(usd, base, eur)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.posts) != 1 {
		h.t.Fatalf("order must never be resent: venue POST count = %d, want 1", len(h.posts))
	}
	for _, event := range detail.Events {
		if event.Payload.ExecutionReport != nil && (event.Source != domain.SourceSystem || event.Principal != "") {
			h.t.Fatal("venue execution was not attributed to the system")
		}
	}
}

func (h *etoroRuntimeTest) assertBalances(usd, base, eur string) {
	h.t.Helper()
	for asset, want := range map[string]string{"USD": usd, "AAPL": base, "EUR": eur} {
		balance, found, err := h.n.GetBalance(h.ctx, "desk", asset)
		h.must(err)
		if !found {
			h.t.Fatalf("%s balance missing", asset)
		}
		assertDecimal(h.t, balance.Available, want)
		assertDecimal(h.t, balance.Held, "0")
	}
}

func TestEtoroRuntimeEndToEnd(t *testing.T) {
	t.Run("market-buy-two-executions", func(t *testing.T) {
		h := newEtoroRuntimeTest(t)
		order := h.submit(domain.OrderSideBuy, "")
		link, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		h.must(err)
		if link.VenueOrderID != "9007199254740993" {
			t.Fatal("venue acknowledgement id lost precision")
		}
		h.mu.Lock()
		post := h.posts[0]
		h.mu.Unlock()
		etoroAssertJSON(t, post.body, `{"action":"open","transaction":"buy","instrumentId":1001,"settlementType":"real","leverage":1,"orderType":"mkt","units":0.3}`)
		h.state(3, 2)
		h.push()
		h.waitStatus(order, domain.OrderStatusFilled)
		h.assertBooked(order, 2, "999.45", "10.3", "99.97")
	})
	t.Run("lost-post-response", func(t *testing.T) {
		h := newEtoroRuntimeTest(t)
		h.mu.Lock()
		h.loseResponse = true
		h.lookupReady = false
		h.mu.Unlock()
		order := h.submit(domain.OrderSideBuy, "")
		link, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		if err == nil || !strings.Contains(err.Error(), "outcome unknown") || link.VenueOrderID != "" {
			t.Fatalf("lost POST response must leave outcome unknown without venue id: link=%+v err=%v", link, err)
		}
		if _, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest); err == nil {
			t.Fatal("runtime accepted a resend of an unknown outcome")
		}
		h.state(3, 2)
		h.mu.Lock()
		h.lookupReady = true
		h.mu.Unlock()
		_ = h.stream.CloseNow()
		h.waitStatus(order, domain.OrderStatusFilled)
		h.assertBooked(order, 2, "999.45", "10.3", "99.97")
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.lookups) == 0 || !strings.Contains(h.lookups[len(h.lookups)-1], "referenceId="+h.requestID) {
			t.Fatal("unknown outcome was not reconciled by referenceId")
		}
	})
	t.Run("restart-before-venue-id-persisted", func(t *testing.T) {
		h := newEtoroRuntimeTest(t)
		h.mu.Lock()
		h.loseResponse, h.lookupReady = true, false
		h.mu.Unlock()
		order := h.submit(domain.OrderSideBuy, "")
		_, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		if err == nil {
			t.Fatal("lost acknowledgement was accepted")
		}
		h.runtime.Stop()
		persisted, found, err := h.realm.GetVenueOrder(h.ctx, order.ExternalID)
		h.must(err)
		if !found || persisted.VenueOrderID != "" || !persisted.SendAttempted || len(h.detail(order).Trades) != 0 {
			t.Fatal("restart fixture must persist send attempt without venue id or trades")
		}
		h.mu.Lock()
		h.status, h.executions, h.lookupReady = 3, 2, true
		h.mu.Unlock()
		h.restart()
		h.waitStatus(order, domain.OrderStatusFilled)
		h.assertBooked(order, 2, "999.45", "10.3", "99.97")
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.lookups) == 0 || !strings.Contains(h.lookups[len(h.lookups)-1], "referenceId="+h.requestID) {
			t.Fatal("restart did not resolve the persisted client id by referenceId")
		}
	})
	t.Run("early-duplicate-reordered-updates", func(t *testing.T) {
		h := newEtoroRuntimeTest(t)
		h.mu.Lock()
		h.postAccepted, h.postRelease = make(chan struct{}), make(chan struct{})
		accepted, release := h.postAccepted, h.postRelease
		h.status, h.executions = 3, 2
		h.mu.Unlock()
		order := h.submit(domain.OrderSideBuy, "")
		result := make(chan error, 1)
		go func() {
			_, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
			result <- err
		}()
		select {
		case <-accepted:
		case <-h.ctx.Done():
			t.Fatal("POST was not accepted")
		}
		for range 3 {
			h.push()
		}
		waitForTrading(t, "update held before POST response", func() bool {
			h.c.ordersMu.Lock()
			defer h.c.ordersMu.Unlock()
			return len(h.c.held) == 1
		})
		close(release)
		h.must(<-result)
		h.waitStatus(order, domain.OrderStatusFilled)
		for _, status := range []int{3, 5, 1, 3} {
			etoroPush(t, h.ctx, h.stream, "Trading.OrderForOpen.Update", fmt.Sprintf(`{"OrderID":9007199254740993,"StatusID":%d,"ExecutedUnits":999}`, status))
		}
		h.streamBarrier()
		h.runtime.Stop()
		h.assertBooked(order, 2, "999.45", "10.3", "99.97")
	})
	t.Run("replay-after-booked-partial-fill", func(t *testing.T) {
		h := newEtoroRuntimeTest(t)
		order := h.submit(domain.OrderSideBuy, "")
		_, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		h.must(err)
		h.state(5, 1)
		h.push()
		h.waitStatus(order, domain.OrderStatusPartiallyFilled)
		if len(h.detail(order).Trades) != 1 {
			t.Fatal("partial execution was not persisted before restart")
		}
		for range 3 {
			h.push()
		}
		h.streamBarrier()
		h.restart()
		h.mu.Lock()
		before := len(h.lookups)
		h.mu.Unlock()
		h.push()
		waitForTrading(t, "partial replay lookup", func() bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return len(h.lookups) > before
		})
		h.state(3, 2)
		h.push()
		h.waitStatus(order, domain.OrderStatusFilled)
		h.assertBooked(order, 2, "999.45", "10.3", "99.97")
	})
	t.Run("ioc-partial-then-status-9", func(t *testing.T) {
		h := newEtoroRuntimeTest(t)
		h.dest.Route = `{"timeInForce":"ioc"}`
		order := h.submit(domain.OrderSideBuy, "2")
		_, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		h.must(err)
		h.state(5, 1)
		h.push()
		h.waitStatus(order, domain.OrderStatusPartiallyFilled)
		h.state(9, 1)
		h.push()
		h.waitStatus(order, domain.OrderStatusCancelled)
		h.assertBooked(order, 1, "999.85", "10.1", "99.99")
		h.mu.Lock()
		defer h.mu.Unlock()
		etoroAssertJSON(t, h.posts[0].body, `{"action":"open","transaction":"buy","instrumentId":1001,"settlementType":"real","leverage":1,"orderType":"limitIOC","units":0.3,"limitRate":2}`)
		for _, report := range h.reports {
			if report.Order == order.ExternalID && report.FillQuantity != "" && report.OrderStatus != domain.OrderStatusPartiallyFilled {
				t.Fatal("IOC cancellation produced a Final fill")
			}
		}
	})
	t.Run("sell-closes-one-position", func(t *testing.T) {
		h := newEtoroRuntimeTest(t)
		order := h.submit(domain.OrderSideSell, "")
		_, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		h.must(err)
		h.state(3, 1)
		etoroPush(t, h.ctx, h.stream, "Trading.OrderForCloseMultiple.Update", `{"OrderID":9007199254740993}`)
		h.waitStatus(order, domain.OrderStatusFilled)
		h.assertBooked(order, 1, "1000.6", "9.7", "100")
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.posts[0].path != "/api/v1/trading/execution/demo/market-close-orders/positions/50" {
			t.Fatal("sell did not close the qualifying position")
		}
		etoroAssertJSON(t, h.posts[0].body, `{"InstrumentID":1001,"UnitsToDeduct":0.3}`)
	})
	t.Run("pre-trade-reject", func(t *testing.T) {
		h := newEtoroRuntimeTest(t)
		h.dest.Route = `{"timeInForce":"ioc"}`
		draft := h.draft(domain.OrderSideBuy, "5", "201")
		h.must(h.runtime.PreCheck(h.ctx, draft, h.dest))
		order, err := h.n.SubmitOrder(h.ctx, draft, domain.MissingAccountReject, h.caller)
		h.must(err)
		if order.Status != domain.OrderStatusRejected {
			t.Fatalf("pre-trade did not reject: %+v", order)
		}
		h.assertBalances("1000", "10", "100")
		if _, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest); err == nil {
			t.Fatal("runtime accepted a pre-trade rejected order")
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		detail := h.detail(order)
		if len(h.posts) != 0 || len(h.lookups) != 0 || len(detail.Trades) != 0 || len(detail.Order.CommissionSubtotals) != 0 {
			t.Fatalf("pre-trade reject reached order endpoints: posts=%d lookups=%d", len(h.posts), len(h.lookups))
		}
	})
}
