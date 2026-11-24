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
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/openalgo"
)

func openAlgoReadStreamRequest(t *testing.T, ctx context.Context, conn *websocket.Conn, action string) string {
	t.Helper()
	typ, payload, err := conn.Read(ctx)
	var request struct {
		Action    string `json:"action"`
		APIKey    string `json:"api_key"`
		RequestID string `json:"request_id"`
	}
	if err != nil || typ != websocket.MessageText ||
		json.Unmarshal(payload, &request) != nil || request.Action != action {
		t.Error("invalid fake OpenAlgo stream request")
		return ""
	}
	if action == "authenticate" && request.APIKey != openAlgoTestKey {
		t.Error("incorrect fake OpenAlgo stream authentication")
	}
	if action == "subscribe_orders" {
		if _, err := uuid.Parse(request.RequestID); err != nil {
			t.Error("OpenAlgo subscription request id is not a UUID")
		}
	}
	return request.RequestID
}

func openAlgoWriteStream(t *testing.T, ctx context.Context, conn *websocket.Conn, typ websocket.MessageType, payload string) {
	t.Helper()
	if err := conn.Write(ctx, typ, []byte(payload)); err != nil {
		t.Error("write fake OpenAlgo stream frame failed")
	}
}

func openAlgoStreamHandshake(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	openAlgoReadStreamRequest(t, ctx, conn, "authenticate")
	openAlgoWriteStream(t, ctx, conn, websocket.MessageText, `{"type":"auth","status":"success"}`)
	id := openAlgoReadStreamRequest(t, ctx, conn, "subscribe_orders")
	openAlgoWriteStream(t, ctx, conn, websocket.MessageText,
		fmt.Sprintf(`{"type":"subscribe_orders","status":"success","request_id":%q}`, id))
}

func openAlgoStreamServer(t *testing.T, ctx context.Context, c *openAlgoConnector, serve func(*websocket.Conn)) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error("accept fake OpenAlgo stream failed")
			return
		}
		defer func() { _ = conn.CloseNow() }()
		serve(conn)
	}))
	t.Cleanup(server.Close)
	c.wsURL = "ws" + strings.TrimPrefix(server.URL, "http")
	c.SetStatusReporter(func(_ bool, message string) { openAlgoAssertSafe(t, message) })
	t.Cleanup(c.Close)
}

type openAlgoTestStream struct {
	conn        *websocket.Conn
	events      <-chan fwtrading.Event
	diagnostics <-chan health.Diagnostic
}

func openAlgoConnectStream(t *testing.T, ctx context.Context, v *openAlgoTestVenue) openAlgoTestStream {
	t.Helper()
	sockets := make(chan *websocket.Conn, 1)
	openAlgoStreamServer(t, ctx, v.c, func(conn *websocket.Conn) {
		openAlgoStreamHandshake(t, ctx, conn)
		sockets <- conn
		_, _, _ = conn.Read(ctx)
	})
	diagnostics := make(chan health.Diagnostic, 2*orderCorrelationLimit)
	v.c.SetDiagnosticReporter(func(diag health.Diagnostic) {
		encoded, err := json.Marshal(diag)
		if err != nil {
			t.Error("encode fake OpenAlgo stream diagnostic failed")
		}
		openAlgoAssertSafe(t, string(encoded))
		diagnostics <- diag
	})
	events, err := v.c.Subscribe(ctx)
	openAlgoAssertError(t, err, nil, "")
	if event := nextEvent(t, events); event.Kind != fwtrading.EventConnected {
		t.Fatal("OpenAlgo stream did not publish connected event")
	}
	select {
	case conn := <-sockets:
		return openAlgoTestStream{conn, events, diagnostics}
	case <-ctx.Done():
		t.Fatal("OpenAlgo stream handshake did not finish")
		return openAlgoTestStream{}
	}
}

func openAlgoPush(t *testing.T, ctx context.Context, stream openAlgoTestStream, mode, id, status, filled string) {
	t.Helper()
	quantity := ""
	if filled != "" {
		quantity = `,"filled_quantity":` + filled
	}
	openAlgoWriteStream(t, ctx, stream.conn, websocket.MessageText,
		fmt.Sprintf(`{"type":"order_update","mode":%q,"orderid":%q,"order_status":%q%s}`,
			mode, id, status, quantity))
}

func openAlgoAssertStreamOrder(t *testing.T, events <-chan fwtrading.Event, clientID, venueID string) {
	t.Helper()
	event := nextEvent(t, events)
	if event.Kind != fwtrading.EventOrder || event.Ref.ClientOrderID != clientID || event.Ref.VenueOrderID != venueID {
		t.Fatal("OpenAlgo order event lost its kind or correlated IDs")
	}
}

func openAlgoStreamBarrier(t *testing.T, ctx context.Context, c *openAlgoConnector, stream openAlgoTestStream) {
	t.Helper()
	c.mapOrder("barrier", "barrier-client")
	mode := "live"
	if c.mode == domain.TradingModePaper {
		mode = "analyze"
	}
	openAlgoPush(t, ctx, stream, mode, "barrier", "open", "")
	openAlgoAssertStreamOrder(t, stream.events, "barrier-client", "barrier")
}

func TestOpenAlgoStreamCorrelation(t *testing.T) {
	for _, install := range []string{"send-before", "send-after", "lookup-after", "mode-mismatch-ack"} {
		t.Run(install, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			overrides := map[string]openAlgoTestReply{"orderstatus": {200, openAlgoTestStatus("123", "open")}}
			if install == "mode-mismatch-ack" {
				overrides["placeorder"] = openAlgoTestReply{200, `{"status":"success","orderid":"123"}`}
			}
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, overrides)
			stream := openAlgoConnectStream(t, ctx, v)
			mapOrder := func() {
				if install == "lookup-after" {
					_, found, err := v.c.Lookup(ctx, fwtrading.OrderRef{VenueOrderID: "123", ClientOrderID: "officer-client-id"})
					openAlgoAssertError(t, err, nil, "")
					if !found {
						t.Fatal("open-order Lookup failed to find the order")
					}
				} else {
					ack, err := v.c.Send(ctx, openAlgoTestOrder())
					openAlgoAssertError(t, err, nil, "")
					if ack.VenueOrderID != "123" {
						t.Fatal("Send lost the acknowledged order ID")
					}
				}
			}
			if install == "send-before" {
				mapOrder()
			}
			openAlgoPush(t, ctx, stream, "analyze", "123", "open", "2")
			if install != "send-before" {
				openAlgoStreamBarrier(t, ctx, v.c, stream)
				v.c.ordersMu.Lock()
				held := slices.Contains(v.c.held, "123")
				v.c.ordersMu.Unlock()
				if !held {
					t.Fatal("unmapped push was not held before acknowledgement")
				}
				mapOrder()
			}
			openAlgoAssertStreamOrder(t, stream.events, "officer-client-id", "123")
			v.c.closedMu.Lock()
			stored := len(v.c.closedFills)
			v.c.closedMu.Unlock()
			if stored != 0 {
				t.Fatal("an open push stored a closed-order confirmation")
			}
			if install == "mode-mismatch-ack" {
				openAlgoPush(t, ctx, stream, "live", "123", "cancelled", "2")
				openAlgoStreamBarrier(t, ctx, v.c, stream)
				v.c.closedMu.Lock()
				remaining := len(v.c.closedFills) + len(v.c.closedIDs)
				v.c.closedMu.Unlock()
				if remaining != 0 {
					t.Fatal("other-mode D1 push stored a closed confirmation")
				}
			}
		})
	}
}

func TestOpenAlgoStreamAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		name, stage, reply, message string
		binary, refused             bool
	}{
		{"auth-refused", "auth", `{"status":"error","code":"AUTHENTICATION_ERROR","message":"bad ` + openAlgoTestKey + `"}`, "authorization refused", false, true},
		{"auth-other-error", "auth", `{"status":"error","code":"BROKER_ERROR","message":"` + openAlgoTestKey + `"}`, "authentication failed", false, false},
		{"auth-malformed", "auth", `{"` + openAlgoTestKey + `"`, "invalid stream authentication reply", false, false},
		{"auth-binary", "auth", `{}`, "expected text authentication reply", true, false},
		{"subscribe-refused", "subscribe", `{"status":"error","code":"REFUSED","message":"` + openAlgoTestKey + `"}`, "subscription failed", false, false},
		{"subscribe-wrong-id", "subscribe", `{"type":"subscribe_orders","status":"success","request_id":"wrong"}`, "unexpected stream subscription reply", false, false},
		{"subscribe-binary", "subscribe", `{}`, "expected text subscription reply", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
			openAlgoStreamServer(t, ctx, v.c, func(conn *websocket.Conn) {
				openAlgoReadStreamRequest(t, ctx, conn, "authenticate")
				if tc.stage == "subscribe" {
					openAlgoWriteStream(t, ctx, conn, websocket.MessageText, `{"type":"auth","status":"success"}`)
					openAlgoReadStreamRequest(t, ctx, conn, "subscribe_orders")
				}
				typ := websocket.MessageText
				if tc.binary {
					typ = websocket.MessageBinary
				}
				openAlgoWriteStream(t, ctx, conn, typ, tc.reply)
				_, _, _ = conn.Read(ctx)
			})
			healthy, err := v.c.stream(ctx, make(chan fwtrading.Event, 1))
			if err != nil {
				openAlgoAssertSafe(t, err.Error())
			}
			if healthy || err == nil || !strings.Contains(err.Error(), tc.message) ||
				errors.Is(err, errOpenAlgoUnauthorized) != tc.refused {
				t.Fatal("stream handshake refusal lost its cause or operation")
			}
			if tc.refused {
				var streamErr *openalgo.StreamError
				if !errors.As(err, &streamErr) || streamErr.Op != "authentication" {
					t.Fatal("authentication refusal lost the typed upstream cause")
				}
			}
		})
	}
}

func TestOpenAlgoStreamFailureStatuses(t *testing.T) {
	for _, failure := range []string{"auth", "dial"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
			if failure == "auth" {
				openAlgoStreamServer(t, ctx, v.c, func(conn *websocket.Conn) {
					openAlgoReadStreamRequest(t, ctx, conn, "authenticate")
					openAlgoWriteStream(t, ctx, conn, websocket.MessageText,
						`{"status":"error","code":"AUTHENTICATION_ERROR","message":"bad `+openAlgoTestKey+`"}`)
					_, _, _ = conn.Read(ctx)
				})
			} else {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusForbidden)
				}))
				t.Cleanup(server.Close)
				v.c.wsURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/" + openAlgoTestKey
				t.Cleanup(v.c.Close)
			}
			statuses := make(chan string, 10)
			v.c.SetStatusReporter(func(connected bool, message string) {
				openAlgoAssertSafe(t, message)
				if !connected {
					statuses <- message
				}
			})
			_, err := v.c.Subscribe(ctx)
			openAlgoAssertError(t, err, nil, "")
			select {
			case message := <-statuses:
				if failure == "dial" && !strings.HasPrefix(message, "openalgo: dial stream: ") {
					t.Fatal("stream dial status lost its vendor label")
				}
				if failure == "auth" && !strings.Contains(message, "authorization refused") {
					t.Fatal("stream auth status lost its refusal")
				}
			case <-ctx.Done():
				t.Fatal("stream failure did not report status")
			}
			v.c.Close()
		})
	}
}

func TestOpenAlgoStreamDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
	stream := openAlgoConnectStream(t, ctx, v)
	v.c.mapOrder("binary-order", "binary-client")
	for range 2 {
		openAlgoWriteStream(t, ctx, stream.conn, websocket.MessageText, `{"type":"unknown","message":"`+openAlgoTestKey+`"}`)
		openAlgoWriteStream(t, ctx, stream.conn, websocket.MessageText, `{"type":"order_update","mode":"`+openAlgoTestKey+`"}`)
		openAlgoWriteStream(t, ctx, stream.conn, websocket.MessageBinary,
			`{"type":"order_update","mode":"analyze","orderid":"binary-order","order_status":"open"}`)
	}
	openAlgoStreamBarrier(t, ctx, v.c, stream)
	for _, detail := range []string{openAlgoPushUnknown, openAlgoPushUnreadable} {
		select {
		case diag := <-stream.diagnostics:
			if diag.Level != health.DiagWarn || diag.Code != health.CodeUnparsableData ||
				diag.Detail != detail || diag.Remediation != venueStreamSkipped {
				t.Fatal("unparsable stream diagnostic changed")
			}
		case <-ctx.Done():
			t.Fatal("missing stream diagnostic")
		}
	}
	select {
	case <-stream.diagnostics:
		t.Fatal("stream repeated the same warning")
	default:
	}
}

func TestOpenAlgoStreamModes(t *testing.T) {
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			v := newOpenAlgoTestVenue(t, mode, nil)
			stream := openAlgoConnectStream(t, ctx, v)
			v.c.mapOrder("123", "client")
			other := "live"
			if mode == domain.TradingModeLive {
				other = "analyze"
			}
			openAlgoPush(t, ctx, stream, other, "123", "cancelled", "2")
			openAlgoStreamBarrier(t, ctx, v.c, stream)
			v.c.closedMu.Lock()
			stored := len(v.c.closedFills)
			v.c.closedMu.Unlock()
			if stored != 0 {
				t.Fatal("another mode confirmed this connection's closed fills")
			}
			select {
			case <-stream.diagnostics:
				t.Fatal("another mode produced a diagnostic")
			default:
			}
		})
	}
}

func TestOpenAlgoStreamClosedQuantityHandoff(t *testing.T) {
	for _, status := range []string{"cancelled", "rejected", "expired"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper,
				map[string]openAlgoTestReply{"orderstatus": {200, openAlgoTestStatus("123", status)}})
			stream := openAlgoConnectStream(t, ctx, v)
			ref := fwtrading.OrderRef{VenueOrderID: "123", ClientOrderID: "client"}
			v.c.mapOrder("123", "client")
			for _, quantity := range []string{"", "0", "2"} {
				openAlgoPush(t, ctx, stream, "analyze", "123", status, quantity)
				openAlgoAssertStreamOrder(t, stream.events, "client", "123")
				snapshot, found, err := v.c.Lookup(ctx, ref)
				if !found {
					t.Fatal("closed Lookup lost the order")
				}
				if quantity != "2" {
					openAlgoAssertError(t, err, errOpenAlgoCancelledFillsUnconfirmed, "")
					v.c.closedMu.Lock()
					stored, exists := v.c.closedFills["123"]
					v.c.closedMu.Unlock()
					if string(stored) != quantity || exists != (quantity != "") {
						t.Fatal("failed Lookup changed its closed confirmation")
					}
					if _, ready, _ := v.c.correlate("123"); !ready {
						t.Fatal("failed Lookup forgot its order mapping")
					}
					continue
				}
				openAlgoAssertError(t, err, nil, "")
				if snapshot.Status != fwtrading.VenueStatusCancelled || snapshot.FilledQuantity != "2" ||
					len(snapshot.Fills) != 1 || snapshot.Fills[0].Price != "100" {
					t.Fatal("confirmed closed Lookup did not book the REST fills")
				}
			}
			v.c.closedMu.Lock()
			remaining := len(v.c.closedFills) + len(v.c.closedIDs)
			v.c.closedMu.Unlock()
			if remaining != 0 {
				t.Fatal("successful closed Lookup retained its confirmation")
			}
			openAlgoPush(t, ctx, stream, "analyze", "123", status, "2")
			openAlgoStreamBarrier(t, ctx, v.c, stream)
			v.c.closedMu.Lock()
			remaining = len(v.c.closedFills) + len(v.c.closedIDs)
			v.c.closedMu.Unlock()
			if remaining != 0 {
				t.Fatal("closed push after finish retained a confirmation")
			}
		})
	}
}

func TestOpenAlgoClosedQuantityBound(t *testing.T) {
	v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
	for i := range orderCorrelationLimit {
		v.c.storeClosedQuantity(fmt.Sprint(i), "0")
	}
	v.c.storeClosedQuantity("0", "2")
	if len(v.c.closedIDs) != orderCorrelationLimit || v.c.closedIDs[0] != "0" || v.c.closedFills["0"] != "2" {
		t.Fatal("replacement grew or reordered the closed confirmation FIFO")
	}
	v.c.storeClosedQuantity("next", "3")
	if len(v.c.closedFills) != orderCorrelationLimit || len(v.c.closedIDs) != orderCorrelationLimit ||
		v.c.closedIDs[0] != "1" || v.c.closedFills["next"] != "3" {
		t.Fatal("closed confirmation FIFO failed to evict the oldest ID")
	}
	if _, exists := v.c.closedFills["0"]; exists {
		t.Fatal("closed confirmation map retained the evicted quantity")
	}
	v.c.forgetClosedQuantity("128")
	v.c.forgetClosedQuantity("missing")
	if len(v.c.closedIDs) != orderCorrelationLimit-1 || len(v.c.closedFills) != orderCorrelationLimit-1 {
		t.Fatal("forgetting a quantity changed the wrong FIFO entries")
	}
	v.c.storeClosedQuantity("128", "4")
	if v.c.closedIDs[len(v.c.closedIDs)-1] != "128" || len(v.c.closedFills) != orderCorrelationLimit {
		t.Fatal("reinserted confirmation did not join the FIFO tail")
	}
}

func TestOpenAlgoClosedQuantityPrefersUnmapped(t *testing.T) {
	v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
	v.c.mapOrder("0", "client")
	for i := range orderCorrelationLimit {
		v.c.storeClosedQuantity(fmt.Sprint(i), "2")
	}
	v.c.storeClosedQuantity("next", "3")
	if len(v.c.closedFills) != orderCorrelationLimit ||
		len(v.c.closedIDs) != orderCorrelationLimit || v.c.closedIDs[0] != "0" ||
		v.c.closedIDs[1] != "2" || v.c.closedFills["0"] != "2" ||
		v.c.closedFills["next"] != "3" {
		t.Fatal("unrelated account traffic evicted a mapped confirmation")
	}
	if _, exists := v.c.closedFills["1"]; exists {
		t.Fatal("oldest unmapped confirmation survived eviction")
	}
	for _, id := range v.c.closedIDs {
		v.c.mapOrder(id, "client")
	}
	v.c.storeClosedQuantity("all-mapped", "4")
	if _, exists := v.c.closedFills["0"]; exists {
		t.Fatal("full mapped store did not evict the oldest confirmation")
	}
	if len(v.c.closedFills) != orderCorrelationLimit ||
		len(v.c.closedIDs) != orderCorrelationLimit || v.c.closedIDs[0] != "2" {
		t.Fatal("mapped eviction changed the wrong confirmation")
	}
}

func TestOpenAlgoStreamClosedBeforeAck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v := newOpenAlgoTestVenue(t, domain.TradingModePaper,
		map[string]openAlgoTestReply{"orderstatus": {200, openAlgoTestStatus("123", "cancelled")}})
	stream := openAlgoConnectStream(t, ctx, v)
	openAlgoPush(t, ctx, stream, "analyze", "123", "cancelled", "2")
	openAlgoStreamBarrier(t, ctx, v.c, stream)
	v.c.closedMu.Lock()
	quantity := v.c.closedFills["123"]
	v.c.closedMu.Unlock()
	if quantity != "2" {
		t.Fatal("unmapped closed push lost its confirmation")
	}
	_, err := v.c.Send(ctx, openAlgoTestOrder())
	openAlgoAssertError(t, err, nil, "")
	openAlgoAssertStreamOrder(t, stream.events, "officer-client-id", "123")
	snapshot, _, err := v.c.Lookup(ctx, fwtrading.OrderRef{
		VenueOrderID: "123", ClientOrderID: "officer-client-id",
	})
	openAlgoAssertError(t, err, nil, "")
	if snapshot.FilledQuantity != "2" || snapshot.Status != fwtrading.VenueStatusCancelled {
		t.Fatal("held closed push did not reconcile after acknowledgement")
	}
}

func FuzzOpenAlgoClosedQuantityFIFO(f *testing.F) {
	f.Add([]byte{0, 1, 0, 0, 1, 0, 1, 1, 0, 1, 2, 0})
	f.Add([]byte{2, 1, 0, 0, 1, 0, 3, 1, 0, 0, 1, 0})
	burst := []byte{2, 0, 0}
	for i := range orderCorrelationLimit + 2 {
		burst = append(burst, 0, byte(i), byte(i>>8))
	}
	f.Add(burst)
	f.Fuzz(func(t *testing.T, operations []byte) {
		c := &openAlgoConnector{
			orderCorrelator: newOrderCorrelator(),
			closedFills:     make(map[string]openalgo.Decimal),
		}
		type entry struct {
			id       string
			quantity openalgo.Decimal
		}
		var model []entry
		mapped := make(map[string]bool)
		var finished []string
		for i := 0; i+2 < len(operations); i += 3 {
			id := fmt.Sprint(int(operations[i+1]) | int(operations[i+2])<<8)
			quantity := openalgo.Decimal(fmt.Sprint(i))
			position := slices.IndexFunc(model, func(e entry) bool { return e.id == id })
			switch operations[i] % 4 {
			case 1:
				c.forgetClosedQuantity(id)
				if position >= 0 {
					model = append(model[:position], model[position+1:]...)
				}
			case 2:
				c.mapOrder(id, "client")
				mapped[id] = true
			case 3:
				c.finishOrder(id)
				delete(mapped, id)
				if !slices.Contains(finished, id) {
					finished = append(finished, id)
					if len(finished) > orderCorrelationLimit {
						finished = finished[1:]
					}
				}
			case 0:
				c.storeClosedQuantity(id, quantity)
				if slices.Contains(finished, id) {
					break
				}
				if position >= 0 {
					model[position].quantity = quantity
				} else {
					if len(model) == orderCorrelationLimit {
						oldest := slices.IndexFunc(model, func(e entry) bool {
							return !mapped[e.id]
						})
						if oldest < 0 {
							oldest = 0
						}
						model = append(model[:oldest], model[oldest+1:]...)
					}
					model = append(model, entry{id, quantity})
				}
			}
			if len(c.closedIDs) != len(model) || len(c.closedFills) != len(model) {
				t.Fatal("closed confirmation store diverged from its FIFO model")
			}
			for j, want := range model {
				if c.closedIDs[j] != want.id || c.closedFills[want.id] != want.quantity {
					t.Fatal("closed confirmation order or value diverged from model")
				}
			}
		}
	})
}

func TestOpenAlgoStreamHeldBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
	stream := openAlgoConnectStream(t, ctx, v)
	for i := range orderCorrelationLimit + 1 {
		openAlgoPush(t, ctx, stream, "analyze", fmt.Sprint(i), "open", "")
	}
	openAlgoStreamBarrier(t, ctx, v.c, stream)
	select {
	case diag := <-stream.diagnostics:
		if diag.Detail != openAlgoHeldEvicted || diag.Level != health.DiagWarn {
			t.Fatal("held update eviction lost its venue diagnostic")
		}
	case <-ctx.Done():
		t.Fatal("held update eviction did not report a diagnostic")
	}
	v.c.ordersMu.Lock()
	defer v.c.ordersMu.Unlock()
	if len(v.c.held) != orderCorrelationLimit || v.c.held[0] != "1" {
		t.Fatal("held update FIFO failed to evict its oldest ID")
	}
}

func TestOpenAlgoStreamReadFailures(t *testing.T) {
	for _, failure := range []string{"error-frame", "close"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
			openAlgoStreamServer(t, ctx, v.c, func(conn *websocket.Conn) {
				openAlgoStreamHandshake(t, ctx, conn)
				if failure == "error-frame" {
					openAlgoWriteStream(t, ctx, conn, websocket.MessageText,
						`{"status":"error","code":"SESSION_ERROR","message":"`+openAlgoTestKey+`"}`)
					_, _, _ = conn.Read(ctx)
				} else {
					_ = conn.Close(websocket.StatusPolicyViolation, "closed "+openAlgoTestKey)
				}
			})
			_, err := v.c.stream(ctx, make(chan fwtrading.Event, 2))
			if err == nil {
				t.Fatal("stream read failure did not end the session")
			}
			openAlgoAssertSafe(t, err.Error())
			if failure == "error-frame" && !strings.Contains(err.Error(), "subscription failed (SESSION_ERROR)") {
				t.Fatal("stream error frame lost its operation")
			}
			if failure == "close" && !strings.Contains(err.Error(), "closed ") {
				t.Fatal("stream lost the peer's read error")
			}
		})
	}
}

func TestOpenAlgoStreamClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
	stream := openAlgoConnectStream(t, ctx, v)
	if _, err := v.c.Subscribe(ctx); err == nil {
		t.Fatal("OpenAlgo subscribed twice")
	}
	closed := make(chan struct{})
	go func() { v.c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("Close did not join the idle stream")
	}
	select {
	case <-v.c.lifecycle.done:
	default:
		t.Fatal("Close returned before the stream ended")
	}
	if _, ok := <-stream.events; ok {
		t.Fatal("Close left the event channel open")
	}
	v.c.Close()
	if _, err := v.c.Subscribe(ctx); err == nil {
		t.Fatal("closed OpenAlgo connector subscribed again")
	}
}
