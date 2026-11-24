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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
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

type coinbaseRuntimeTest struct {
	t             *testing.T
	ctx           context.Context
	n             node.Node
	realm         store.RealmStore
	runtime       *fwtrading.Runtime
	dest          domain.TradingDestination
	sockets       chan *websocket.Conn
	stream        *websocket.Conn
	sequence      int
	mu            sync.Mutex
	clientID      string
	order         map[string]any
	fills         []any
	posts         int
	clientLookups int
	loseResponse  bool
	ready         bool
}

func (h *coinbaseRuntimeTest) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

func newCoinbaseRuntimeTest(t *testing.T) *coinbaseRuntimeTest {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	h := &coinbaseRuntimeTest{t: t, ctx: ctx, sockets: make(chan *websocket.Conn, 4), ready: true, order: coinbaseRESTOrder(), fills: []any{}}
	h.order["filled_size"] = "0"
	server := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(server.Close)
	st, err := sqlite.New(t.TempDir()+"/officer.db?_pragma=journal_mode(MEMORY)&_pragma=synchronous(OFF)", domain.DefaultRealm)
	h.must(err)
	h.must(st.Migrate(ctx))
	registry := fwtrading.NewRegistry()
	h.must(registry.Register(fwtrading.Provider{Type: domain.TradingProviderCoinbase, Title: "Coinbase", LiveOnly: true, Build: func(ctx fwtrading.BuildContext) (fwtrading.Connector, error) {
		return newCoinbaseConnector(ctx.Connection, server.URL, "ws"+strings.TrimPrefix(server.URL, "http")+"/stream")
	}}))
	h.must(st.BindExecutionPolicyProviders(registry.SupportsExecutionPolicy))
	h.n, _, err = node.NewLocalNode(ctx, domain.DefaultRealm, st, engine.NewOpenPitEngineBuildFunc(), func(err error) { t.Errorf("fatal node shutdown: %v", err) })
	h.must(err)
	t.Cleanup(func() {
		cancel()
		if h.runtime != nil {
			h.runtime.Stop()
		}
		h.must(h.n.Close())
		h.must(st.Close())
	})
	h.realm, err = st.ForRealm(ctx, domain.DefaultRealm)
	h.must(err)
	caller := domain.Caller{Principal: domain.PrincipalOperator, Source: domain.SourceAPI}
	_, err = h.n.CreateAccount(ctx, domain.Account{Code: "desk", Currency: "USD"}, caller)
	h.must(err)
	_, err = h.n.CreateAsset(ctx, domain.Asset{Code: "BTC"}, caller)
	h.must(err)
	for asset, amount := range map[string]string{"USD": "1000", "BTC": "10"} {
		id, err := domain.NewExternalID()
		h.must(err)
		result, err := h.n.ApplyAdjustment(ctx, "desk", id, domain.AdjustmentRequest{Type: domain.AdjustmentTypeDeposit, Asset: asset, AverageEntryPrice: "1", RealizedPnl: "0", Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: amount}}, domain.MissingAccountReject, caller)
		h.must(err)
		if result.Accepted == nil {
			t.Fatal("funding rejected")
		}
	}
	connection := coinbaseTestConnection(t)
	connection.Provider = domain.TradingProviderCoinbase
	connection.Label = "live"
	connection.VenueIdentity = "portfolio"
	connection.Enabled = true
	connection, err = h.realm.CreateTradingConnection(ctx, connection)
	h.must(err)
	h.must(h.realm.UpsertTradingInstrument(ctx, domain.TradingInstrument{Connection: connection.ExternalID, ExternalSymbol: "BTC-USD", BaseAsset: "BTC", QuoteAsset: "USD", Enabled: true}))
	h.must(h.realm.PutTradingAccess(ctx, domain.TradingAccess{Account: "desk", Connection: connection.ExternalID}))
	h.dest = domain.TradingDestination{Connection: connection.ExternalID, Route: `{"timeInForce":"GTC"}`}
	signer, err := signing.New(h.realm)
	h.must(err)
	service, err := backend.New(h.n, nil, signer, engine.LockSettlementPrice)
	h.must(err)
	h.runtime, err = fwtrading.NewRuntime(registry, h.realm, nil, func(ctx context.Context, in domain.ExecutionReportInput) error {
		_, _, err := service.ApplyExecutionReport(auth.ContextWithCaller(ctx, auth.SystemCaller()), in)
		return err
	},
		func(context.Context, domain.AccountID, domain.ExternalID, domain.AdjustmentRequest) error {
			t.Error("unexpected separate fee adjustment")
			return errors.New("unexpected fee adjustment")
		},
		func(context.Context, domain.ExternalID, string, string) error {
			t.Error("unexpected unlinked fee")
			return errors.New("unexpected unlinked fee")
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.must(err)
	h.must(h.runtime.Start(auth.ContextWithCaller(ctx, caller)))
	h.nextSocket()
	base, found, err := h.realm.GetAsset(ctx, "BTC")
	h.must(err)
	if !found {
		t.Fatal("BTC missing")
	}
	quote, found, err := h.realm.GetAsset(ctx, "USD")
	h.must(err)
	if !found {
		t.Fatal("USD missing")
	}
	h.must(h.n.CurrentMarketDataSink().Push(marketdata.QuoteUpdate{Base: base.EngineAssetID, Quote: quote.EngineAssetID, AsOf: time.Now(), Mark: "10", Bid: "10", Ask: "10"}))
	return h
}

func (h *coinbaseRuntimeTest) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/stream" {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			h.t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if _, err := coinbaseReadSubscriptions(h.ctx, conn); err != nil {
			return
		}
		raw, err := json.Marshal(map[string]any{"channel": "user", "sequence_num": 0, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "events": coinbaseUserEvent("snapshot")})
		if err != nil {
			h.t.Error(err)
			return
		}
		if conn.Write(h.ctx, websocket.MessageText, raw) != nil {
			return
		}
		select {
		case h.sockets <- conn:
		case <-h.ctx.Done():
			return
		}
		_, _, _ = conn.Read(h.ctx)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.URL.Path {
	case "/api/v3/brokerage/products/BTC-USD":
		p := coinbaseTestProduct()
		p["quote_currency_id"] = "USDC"
		writeJSON(h.t, w, p)
	case "/api/v3/brokerage/orders":
		h.posts++
		var body struct {
			ClientID string `json:"client_order_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			h.t.Error(err)
			return
		}
		h.clientID = body.ClientID
		h.order["client_order_id"] = h.clientID
		if h.loseResponse {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				h.t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		writeJSON(h.t, w, map[string]any{"success": true, "success_response": map[string]string{"order_id": "venue-order", "client_order_id": h.clientID}})
	case "/api/v3/brokerage/orders/historical/venue-order", "/api/v3/brokerage/orders/historical/batch", "/api/v3/brokerage/orders/historical/fills":
		if !h.ready {
			w.WriteHeader(503)
			writeJSON(h.t, w, map[string]string{"error": "UNAVAILABLE", "message": "history not ready"})
			return
		}
		switch r.URL.Path {
		case "/api/v3/brokerage/orders/historical/batch":
			h.clientLookups++
			if q := r.URL.Query(); q.Get("product_ids") != "BTC-USD" || q.Get("order_side") != "BUY" || q.Has("client_order_id") {
				h.t.Errorf("client lookup filters: %s", r.URL)
			}
			writeJSON(h.t, w, map[string]any{"orders": []any{h.order}, "has_next": false, "cursor": ""})
		case "/api/v3/brokerage/orders/historical/fills":
			writeJSON(h.t, w, map[string]any{"fills": h.fills, "cursor": ""})
		default:
			writeJSON(h.t, w, map[string]any{"order": h.order})
		}
	default:
		h.t.Errorf("unexpected Coinbase runtime request: %s", r.URL)
		w.WriteHeader(500)
	}
}

func (h *coinbaseRuntimeTest) nextSocket() {
	select {
	case h.stream = <-h.sockets:
		h.sequence = 1
	case <-h.ctx.Done():
		h.t.Fatal("runtime stream missing")
	}
}

func (h *coinbaseRuntimeTest) submit() domain.Order {
	h.t.Helper()
	caller := domain.Caller{Principal: domain.PrincipalOperator, Source: domain.SourceAPI}
	draft := domain.Order{Account: "desk", BaseAsset: "BTC", QuoteAsset: "USD", Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "10"}
	h.must(h.runtime.PreCheck(h.ctx, draft, h.dest))
	order, err := h.n.SubmitOrder(h.ctx, draft, domain.MissingAccountReject, caller)
	h.must(err)
	if order.Status != domain.OrderStatusCommitted {
		h.t.Fatalf("order not committed: %+v", order)
	}
	return order
}

func (h *coinbaseRuntimeTest) setFills(status string, fills ...map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cum := decimal.Zero
	h.fills = []any{}
	for _, fill := range fills {
		h.fills = append(h.fills, fill)
		qty, err := decimal.NewFromString(fill["size"].(string))
		h.must(err)
		cum = cum.Add(qty)
	}
	h.order["status"], h.order["filled_size"], h.ready = status, cum.String(), true
}

func (h *coinbaseRuntimeTest) push() {
	h.mu.Lock()
	id := h.clientID
	h.mu.Unlock()
	coinbasePushFrame(h.t, h.ctx, h.stream, "user", h.sequence, coinbaseUserEvent("update", map[string]any{"order_id": "venue-order", "client_order_id": id, "cumulative_quantity": "999", "total_fees": "999"}))
	h.sequence++
}

func (h *coinbaseRuntimeTest) wait(order domain.Order, status domain.OrderStatus, count int) {
	h.t.Helper()
	waitForTrading(h.t, "Coinbase runtime booking", func() bool {
		detail, err := h.realm.GetOrder(h.ctx, order.ExternalID)
		h.must(err)
		return detail.Order.Status == status && len(detail.Trades) == count
	})
}

func (h *coinbaseRuntimeTest) assertBooked(order domain.Order, count int, usd, btc, fee string) {
	h.t.Helper()
	detail, err := h.realm.GetOrder(h.ctx, order.ExternalID)
	h.must(err)
	if len(detail.Trades) != count {
		h.t.Fatalf("fill count=%d want=%d", len(detail.Trades), count)
	}
	h.mu.Lock()
	venueFills := append([]any(nil), h.fills...)
	h.mu.Unlock()
	for i, trade := range detail.Trades {
		fill := venueFills[i].(map[string]any)
		assertDecimal(h.t, trade.Quantity, fill["size"].(string))
		assertDecimal(h.t, trade.Price, fill["price"].(string))
		if trade.Commission == nil || trade.Commission.Currency != "USD" {
			h.t.Fatalf("venue quote commission was not mapped: %+v", trade)
		}
		assertDecimal(h.t, trade.Commission.Amount, fill["commission"].(string))
		if trade.BaseAsset != "BTC" || trade.QuoteAsset != "USD" {
			h.t.Fatalf("trade assets: %+v", trade)
		}
	}
	if len(detail.Order.CommissionSubtotals) != 1 || detail.Order.CommissionSubtotals[0].Currency != "USD" {
		h.t.Fatal("quote commission subtotal missing")
	}
	assertDecimal(h.t, detail.Order.CommissionSubtotals[0].Amount, fee)
	for asset, want := range map[string]string{"USD": usd, "BTC": btc} {
		balance, found, err := h.n.GetBalance(h.ctx, "desk", asset)
		h.must(err)
		if !found {
			h.t.Fatal("balance missing")
		}
		assertDecimal(h.t, balance.Available, want)
		assertDecimal(h.t, balance.Held, "0")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.posts != 1 {
		h.t.Fatalf("order resent: posts=%d", h.posts)
	}
}

func TestCoinbaseRuntimeEndToEnd(t *testing.T) {
	t.Run("two-partials-then-venue-cancel", func(t *testing.T) {
		h := newCoinbaseRuntimeTest(t)
		order := h.submit()
		_, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		h.must(err)
		first := coinbaseRESTFill("a", "0.2")
		first["price"] = "9"
		h.setFills("OPEN", first)
		h.push()
		h.wait(order, domain.OrderStatusPartiallyFilled, 1)
		second := coinbaseRESTFill("b", "0.3")
		second["commission"] = "0.02"
		h.setFills("OPEN", first, second)
		h.push()
		h.push()
		h.wait(order, domain.OrderStatusPartiallyFilled, 2)
		h.setFills("CANCELLED", first, second)
		h.push()
		h.wait(order, domain.OrderStatusCancelled, 2)
		h.assertBooked(order, 2, "995.17", "10.5", "0.03")
	})
	t.Run("fill-during-disconnect", func(t *testing.T) {
		h := newCoinbaseRuntimeTest(t)
		order := h.submit()
		_, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		h.must(err)
		first := coinbaseRESTFill("a", "0.2")
		first["price"] = "9"
		h.setFills("OPEN", first)
		h.push()
		h.wait(order, domain.OrderStatusPartiallyFilled, 1)
		h.mu.Lock()
		h.ready = false
		h.mu.Unlock()
		_ = h.stream.CloseNow()
		second := coinbaseRESTFill("b", "0.8")
		second["commission"] = "0.02"
		h.setFills("FILLED", first, second)
		h.nextSocket()
		h.wait(order, domain.OrderStatusFilled, 2)
		h.assertBooked(order, 2, "990.17", "11", "0.03")
	})
	t.Run("lost-post-response", func(t *testing.T) {
		h := newCoinbaseRuntimeTest(t)
		h.mu.Lock()
		h.loseResponse, h.ready = true, false
		h.mu.Unlock()
		order := h.submit()
		link, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest)
		if !errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), "outcome unknown") || link.VenueOrderID != "" {
			t.Fatalf("lost response must stay unknown: %+v %v", link, err)
		}
		if _, err := h.runtime.Send(h.ctx, order.ExternalID, h.dest); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("resend accepted: %v", err)
		}
		h.setFills("FILLED", coinbaseRESTFill("a", "1"))
		_ = h.stream.CloseNow()
		h.nextSocket()
		h.wait(order, domain.OrderStatusFilled, 1)
		h.assertBooked(order, 1, "989.99", "11", "0.01")
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.clientLookups == 0 {
			t.Fatal("unknown send was not looked up by client id")
		}
	})
}
