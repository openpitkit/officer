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
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

func etoroReadRequest(t *testing.T, ctx context.Context, conn *websocket.Conn, operation string) string {
	t.Helper()
	kind, raw, err := conn.Read(ctx)
	if err != nil {
		t.Error("eToro control frame missing")
		return ""
	}
	var frame struct {
		ID        string          `json:"id"`
		Operation string          `json:"operation"`
		Data      json.RawMessage `json:"data"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil || json.Unmarshal(raw, &fields) != nil ||
		len(fields) != 3 || kind != websocket.MessageText || frame.Operation != operation {
		t.Error("incorrect eToro control envelope")
		return ""
	}
	if _, err := uuid.Parse(frame.ID); err != nil {
		t.Error("eToro request id is not a UUID")
		return ""
	}
	if operation == "Authenticate" {
		etoroAssertJSON(t, string(frame.Data), etoroTestCredentials)
	} else {
		etoroAssertJSON(t, string(frame.Data), `{"topics":["private:demo"]}`)
	}
	return frame.ID
}

func etoroAck(t *testing.T, ctx context.Context, conn *websocket.Conn, id, operation string) bool {
	t.Helper()
	return sendFrame(t, ctx, conn, websocket.MessageText,
		fmt.Sprintf(`{"id":%q,"operation":%q,"success":true}`, id, operation))
}

func etoroHandshake(t *testing.T, ctx context.Context, conn *websocket.Conn) bool {
	t.Helper()
	auth := etoroReadRequest(t, ctx, conn, "Authenticate")
	if auth == "" || !etoroAck(t, ctx, conn, auth, "Authenticate") {
		return false
	}
	sub := etoroReadRequest(t, ctx, conn, "Subscribe")
	if sub == "" || sub == auth {
		t.Error("Subscribe must have a fresh UUID")
		return false
	}
	return etoroAck(t, ctx, conn, sub, "Subscribe")
}

func etoroPush(t *testing.T, ctx context.Context, conn *websocket.Conn, typ, content string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"messages": []any{map[string]string{
		"topic": "private:demo", "type": typ, "id": uuid.NewString(), "content": content,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !sendFrame(t, ctx, conn, websocket.MessageText, string(raw)) {
		t.Fatal("could not push eToro update")
	}
}

func etoroOrderPush(t *testing.T, ctx context.Context, conn *websocket.Conn, id string) {
	t.Helper()
	// Deliberately false stream fill fields: only REST may supply executions.
	etoroPush(t, ctx, conn, "Trading.OrderForOpen.Update",
		`{"OrderID":`+id+`,"RequestGuid":"unrelated","StatusID":3,"ExecutedUnits":999,"EndRate":999}`)
}

func etoroStreamConnector(t *testing.T, server *httptest.Server) *etoroConnector {
	t.Helper()
	c, err := newEtoroConnector(domain.TradingConnection{
		Mode: domain.TradingModePaper, Credentials: etoroTestCredentials,
	}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.streamURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/stream"
	c.SetStatusReporter(func(bool, string) {})
	c.SetDiagnosticReporter(func(health.Diagnostic) {})
	t.Cleanup(c.Close)
	return c
}

func etoroSubscribe(t *testing.T, ctx context.Context, c *etoroConnector) <-chan fwtrading.Event {
	t.Helper()
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("eToro EventConnected missing after Subscribe acknowledgement")
	}
	return events
}

func etoroAssertOrderEvent(t *testing.T, event fwtrading.Event, client, venue string) {
	t.Helper()
	want := fwtrading.Event{Kind: fwtrading.EventOrder,
		Ref: fwtrading.OrderRef{ClientOrderID: client, VenueOrderID: venue}}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("stream must trigger Lookup with correlated Ref and no fill: got %+v, want %+v", event, want)
	}
}

func TestEtoroStreamTopicsAndReconnect(t *testing.T) {
	for _, demo := range []bool{true, false} {
		t.Run(fmt.Sprint(demo), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var mu sync.Mutex
			seen := make(map[string]bool)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				auth := etoroReadRequest(t, ctx, conn, "Authenticate")
				mu.Lock()
				if seen[auth] {
					t.Error("Authenticate reused a UUID on reconnect")
				}
				seen[auth] = true
				mu.Unlock()
				if !etoroAck(t, ctx, conn, auth, "Authenticate") {
					return
				}
				_, raw, err := conn.Read(ctx)
				if err != nil {
					t.Error("Subscribe missing")
					return
				}
				var sub struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(raw, &sub) != nil {
					t.Error("Subscribe unreadable")
					return
				}
				topic := "private"
				if demo {
					topic = "private:demo"
				}
				if _, err := uuid.Parse(sub.ID); err != nil || sub.ID == auth {
					t.Error("Subscribe UUID missing or reused")
				}
				etoroAssertJSON(t, string(raw), fmt.Sprintf(`{"id":%q,"operation":"Subscribe","data":{"topics":[%q]}}`, sub.ID, topic))
				etoroAck(t, ctx, conn, sub.ID, "Subscribe")
			}))
			defer server.Close()
			c := etoroStreamConnector(t, server)
			c.demo = demo
			events := etoroSubscribe(t, ctx, c)
			for range 2 {
				if nextEvent(t, events).Kind != fwtrading.EventConnected {
					t.Fatal("EventConnected missing on reconnect")
				}
			}
			c.Close()
		})
	}
}

func TestEtoroStreamAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		name, operation, response, message string
		refused                            bool
	}{
		{"invalid-key", "Authenticate", `{"id":%q,"operation":"Authenticate","success":false,"errorCode":"InvalidKey"}`, "authorization refused", true},
		{"unauthorized", "Authenticate", `{"id":%q,"operation":"Authenticate","success":false,"errorCode":"Unauthorized"}`, "authorization refused", true},
		{"wrong-id", "Authenticate", `{"id":"wrong","operation":"Authenticate","success":true,"unused":%q}`, "acknowledgement mismatch", false},
		{"wrong-operation", "Authenticate", `{"id":%q,"operation":"Subscribe","success":true}`, "acknowledgement mismatch", false},
		{"missing-success", "Authenticate", `{"id":%q,"operation":"Authenticate"}`, "acknowledgement mismatch", false},
		{"auth-failure", "Authenticate", `{"id":%q,"operation":"Authenticate","success":false,"errorCode":"TooManyRequests"}`, "operation failed", false},
		{"subscribe-failure", "Subscribe", `{"id":%q,"operation":"Subscribe","success":false,"errorCode":"Forbidden"}`, "operation failed", false},
		{"subscribe-mismatch", "Subscribe", `{"id":%q,"operation":"Authenticate","success":true}`, "acknowledgement mismatch", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			closed := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(closed)
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				id := etoroReadRequest(t, ctx, conn, "Authenticate")
				if tc.operation == "Subscribe" {
					if !etoroAck(t, ctx, conn, id, "Authenticate") {
						return
					}
					id = etoroReadRequest(t, ctx, conn, "Subscribe")
				}
				sendFrame(t, ctx, conn, websocket.MessageText, fmt.Sprintf(tc.response, id))
				if _, _, err := conn.Read(ctx); err == nil {
					t.Error("client sent a frame after a refused or mismatched acknowledgement")
				}
			}))
			defer server.Close()
			c := etoroStreamConnector(t, server)
			healthy, err := c.stream(ctx, make(chan fwtrading.Event, 1))
			if err == nil || !strings.Contains(err.Error(), tc.message) || errors.Is(err, errEtoroUnauthorized) != tc.refused || healthy {
				t.Fatalf("ack failure must be explicit (refused=%v): healthy=%v error=%v", tc.refused, healthy, err)
			}
			<-closed
		})
	}
}

func TestEtoroStreamCorrelation(t *testing.T) {
	for _, install := range []string{"send-before", "send-after", "lookup-after"} {
		t.Run(install, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			streams := make(chan *websocket.Conn, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/stream" {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case strings.HasSuffix(r.URL.Path, "/orders"):
						w.WriteHeader(http.StatusAccepted)
						_, _ = fmt.Fprint(w, `{"orderId":9007199254740993}`)
					case strings.HasSuffix(r.URL.Path, "/orders:lookup"):
						_, _ = fmt.Fprint(w, strings.Replace(etoroTestLookup, `"id":3`, `"id":5`, 1))
					case strings.HasSuffix(r.URL.Path, "/aggregate-portfolio"):
						_, _ = fmt.Fprint(w, `{"accountCurrency":"EUR"}`)
					default:
						t.Errorf("unexpected REST path: %s", r.URL.Path)
						w.WriteHeader(500)
					}
					return
				}
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if etoroHandshake(t, ctx, conn) {
					streams <- conn
					_, _, _ = conn.Read(ctx)
				}
			}))
			defer server.Close()
			c := etoroStreamConnector(t, server)
			diag := make(chan health.Diagnostic, 1)
			c.SetDiagnosticReporter(func(d health.Diagnostic) { diag <- d })
			events := etoroSubscribe(t, ctx, c)
			conn := <-streams
			mapOrder := func() {
				var err error
				if install == "lookup-after" {
					_, _, err = c.Lookup(ctx, fwtrading.OrderRef{ClientOrderID: etoroTestCOID, Side: domain.OrderSideBuy})
				} else {
					_, err = c.Send(ctx, etoroTestOrder())
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if install == "send-before" {
				mapOrder()
			}
			for range 3 {
				etoroOrderPush(t, ctx, conn, "9007199254740993")
				if install == "send-before" {
					etoroAssertOrderEvent(t, nextEvent(t, events), etoroTestCOID, "9007199254740993")
				}
			}
			if install != "send-before" {
				// A following diagnostic is a barrier after all three frames.
				etoroPush(t, ctx, conn, "barrier", "{}")
				select {
				case <-diag:
				case <-ctx.Done():
					t.Fatal("stream did not reach correlation barrier")
				}
				waitForTrading(t, "held update", func() bool {
					c.ordersMu.Lock()
					defer c.ordersMu.Unlock()
					return len(c.held) == 1
				})
				mapOrder()
				etoroAssertOrderEvent(t, nextEvent(t, events), etoroTestCOID, "9007199254740993")
			}
			c.mapOrder("42", "barrier")
			etoroOrderPush(t, ctx, conn, "42")
			etoroAssertOrderEvent(t, nextEvent(t, events), "barrier", "42")
			c.Close()
		})
	}
}

func TestEtoroStreamReconnectBackoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	attempts := make(chan time.Time, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts <- time.Now()
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		etoroHandshake(t, ctx, conn)
	}))
	defer server.Close()
	c := etoroStreamConnector(t, server)
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var previous time.Time
	for i := range 4 {
		if nextEvent(t, events).Kind != fwtrading.EventConnected {
			t.Fatal("Subscribe acknowledgement did not connect")
		}
		var at time.Time
		select {
		case at = <-attempts:
		case <-ctx.Done():
			t.Fatal("dial timestamp missing")
		}
		if i > 0 {
			minimum := etoroReconnectMin * time.Duration(1<<(i-1))
			if elapsed := at.Sub(previous); elapsed < minimum-25*time.Millisecond {
				t.Fatalf("reconnect spacing did not grow: dial %d waited %v, want at least %v", i+1, elapsed, minimum)
			}
		}
		previous = at
	}
	c.Close()
}

func TestEtoroStreamHealthyAfterOrder(t *testing.T) {
	for _, order := range []bool{false, true} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if etoroHandshake(t, ctx, conn) && order {
					etoroOrderPush(t, ctx, conn, "42")
				}
			}))
			defer server.Close()
			c := etoroStreamConnector(t, server)
			c.mapOrder("42", "mapped")
			events := make(chan fwtrading.Event, 2)
			healthy, err := c.stream(ctx, events)
			if err == nil || healthy != order {
				t.Fatalf("stream healthy = %v, want %v after order=%v; err=%v", healthy, order, order, err)
			}
			if event := <-events; event.Kind != fwtrading.EventConnected {
				t.Fatalf("first event = %+v, want connected", event)
			}
			if order {
				etoroAssertOrderEvent(t, <-events, "mapped", "42")
			}
		})
	}
}

func TestEtoroStreamTerminalLookupForgetsOrder(t *testing.T) {
	for _, status := range []string{"3", "4"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/orders:lookup"):
					_, _ = fmt.Fprint(w, strings.Replace(etoroTestLookup, `"id":3`, `"id":`+status, 1))
				case strings.HasSuffix(r.URL.Path, "/aggregate-portfolio"):
					_, _ = fmt.Fprint(w, `{"accountCurrency":"EUR"}`)
				default:
					t.Errorf("unexpected REST path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			c := etoroStreamConnector(t, server)
			var diagnostics []health.Diagnostic
			c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics = append(diagnostics, d) })
			c.orderUpdate("9007199254740993")
			c.mapOrder("9007199254740993", etoroTestCOID)
			_, found, err := c.Lookup(ctx, fwtrading.OrderRef{ClientOrderID: etoroTestCOID, VenueOrderID: "9007199254740993", Side: domain.OrderSideBuy})
			if err != nil || !found {
				t.Fatalf("terminal Lookup: found=%v err=%v", found, err)
			}
			c.ordersMu.Lock()
			_, mapped := c.orderIDs["9007199254740993"]
			held := slices.Clone(c.held)
			c.ordersMu.Unlock()
			if mapped {
				t.Error("terminal Lookup retained the OrderID mapping")
			}
			if len(held) != 0 {
				t.Fatalf("terminal Lookup retained held updates: %v", held)
			}
			if event, ok := c.orderUpdate("9007199254740993"); ok {
				t.Fatalf("late terminal update emitted EventOrder: %+v", event)
			}
			c.ordersMu.Lock()
			held = slices.Clone(c.held)
			c.ordersMu.Unlock()
			if len(held) != 0 {
				t.Fatalf("late terminal update was held: %v", held)
			}
			if event, ok := c.nextMappedUpdate(); ok {
				t.Fatalf("late terminal update left a mapped event: %+v", event)
			}
			if len(diagnostics) != 0 {
				t.Fatalf("terminal updates must be silent: %+v", diagnostics)
			}
		})
	}
}

func TestEtoroStreamFinishedBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/orders:lookup") {
			t.Errorf("unexpected REST path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"orderId":%s,"action":"open","status":{"id":4},"requestedUnits":1,"positionExecutions":[]}`, r.URL.Query().Get("orderId"))
	}))
	defer server.Close()
	c := etoroStreamConnector(t, server)
	var diagnostics []health.Diagnostic
	c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics = append(diagnostics, d) })
	lookup := func(id string) {
		t.Helper()
		c.mapOrder(id, etoroTestCOID)
		_, found, err := c.Lookup(ctx, fwtrading.OrderRef{ClientOrderID: etoroTestCOID, VenueOrderID: id, Side: domain.OrderSideBuy})
		if err != nil || !found {
			t.Fatalf("terminal Lookup %s: found=%v err=%v", id, found, err)
		}
		c.ordersMu.Lock()
		mapped, finished := len(c.orderIDs), len(c.finished)
		c.ordersMu.Unlock()
		if mapped != 0 || finished > etoroHeldLimit {
			t.Fatalf("terminal correlation state must stay bounded: mapped=%d finished=%d", mapped, finished)
		}
	}
	want := make([]string, 0, etoroHeldLimit)
	for i := 1; i <= etoroHeldLimit+2; i++ {
		id := strconv.Itoa(i)
		lookup(id)
		if i > 2 {
			want = append(want, id)
		}
	}
	lookup("3")
	c.ordersMu.Lock()
	finished := slices.Clone(c.finished)
	c.ordersMu.Unlock()
	if !slices.Equal(finished, want) {
		t.Fatalf("finished queue must retain the newest %d distinct orders in FIFO order: %v", etoroHeldLimit, finished)
	}
	if event, ok := c.orderUpdate("3"); ok {
		t.Fatalf("retained finished order emitted EventOrder: %+v", event)
	}
	if event, ok := c.orderUpdate("1"); ok {
		t.Fatalf("forgotten finished order emitted EventOrder: %+v", event)
	}
	c.ordersMu.Lock()
	held := slices.Clone(c.held)
	c.ordersMu.Unlock()
	if !slices.Equal(held, []string{"1"}) {
		t.Fatalf("only a forgotten finished order may be held: %v", held)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("finished queue eviction and late updates must be silent: %+v", diagnostics)
	}
}

func TestEtoroStreamUnknownPushOrderID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if !etoroHandshake(t, ctx, conn) {
			return
		}
		for _, content := range []string{"{}", "secret value", `{"OrderID":0}`, `{"OrderID":42}`} {
			etoroPush(t, ctx, conn, "future-type", content)
		}
		etoroOrderPush(t, ctx, conn, "43")
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := etoroStreamConnector(t, server)
	c.mapOrder("42", "mapped")
	c.mapOrder("43", "barrier")
	var diagnostics []health.Diagnostic
	c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics = append(diagnostics, d) })
	events := etoroSubscribe(t, ctx, c)
	etoroAssertOrderEvent(t, nextEvent(t, events), "mapped", "42")
	etoroAssertOrderEvent(t, nextEvent(t, events), "barrier", "43")
	c.Close()
	if len(diagnostics) != 1 || diagnostics[0].Detail != etoroPushUnknown {
		t.Fatalf("unknown pushes must diagnose once, trigger mapped OrderID, and ignore absent OrderID: %+v", diagnostics)
	}
}

func TestEtoroStreamHeldBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	streams := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if etoroHandshake(t, ctx, conn) {
			streams <- conn
			_, _, _ = conn.Read(ctx)
		}
	}))
	defer server.Close()
	c := etoroStreamConnector(t, server)
	diagnostics := make(chan health.Diagnostic, 3)
	c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics <- d })
	events := etoroSubscribe(t, ctx, c)
	conn := <-streams
	for i := 1; i <= etoroHeldLimit+2; i++ {
		etoroOrderPush(t, ctx, conn, fmt.Sprint(i))
	}
	for range 2 {
		select {
		case d := <-diagnostics:
			want := health.Diagnostic{
				Code: health.CodeInternalError, Kind: health.DiagKindProvider, Level: health.DiagWarn,
				Title: "The correlation queue for stream updates is full", Detail: etoroHeldEvicted,
				Remediation: "Updates for orders placed outside Officer can fill this queue. " +
					"An affected Officer order is resolved at the next stream reconnect or restart.",
			}
			if !reflect.DeepEqual(d, want) {
				t.Fatalf("eviction must report a correlation queue warning with reconnect recovery: got %+v, want %+v", d, want)
			}
		case <-ctx.Done():
			t.Fatal("held update eviction was silent")
		}
	}
	c.ordersMu.Lock()
	count, oldest := len(c.held), c.held[0]
	c.ordersMu.Unlock()
	if count != etoroHeldLimit || oldest != "3" {
		t.Fatalf("held queue must evict oldest at bound: count=%d oldest=%s", count, oldest)
	}
	c.mapOrder("1", "evicted")
	c.mapOrder("3", "retained")
	etoroAssertOrderEvent(t, nextEvent(t, events), "retained", "3")
	c.Close()
}

func TestEtoroStreamDiagnosticsAndHeartbeat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if !etoroHandshake(t, ctx, conn) {
			return
		}
		sendFrame(t, ctx, conn, websocket.MessageBinary, "\x01")
		for _, typ := range []string{"Trading.Position.Opened", "Trading.Position.Closed"} {
			etoroPush(t, ctx, conn, typ, "{}")
		}
		etoroPush(t, ctx, conn, "future-type", "secret value")
		etoroPush(t, ctx, conn, "future-type", "secret value")
		for _, content := range []string{"{", `{"OrderID":0}`, `{"OrderID":"bad"}`, "null"} {
			etoroPush(t, ctx, conn, "Trading.OrderForOpen.Update", content)
		}
		for _, raw := range []string{`{}`, `{"messages":null}`, `{"messages":[{"type":"future"}]}`} {
			sendFrame(t, ctx, conn, websocket.MessageText, raw)
		}
		etoroPush(t, ctx, conn, "Trading.OrderForCloseMultiple.Update", `{"OrderID":42}`)
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := etoroStreamConnector(t, server)
	c.mapOrder("42", "mapped")
	var mu sync.Mutex
	var diagnostics []health.Diagnostic
	c.SetDiagnosticReporter(func(d health.Diagnostic) {
		mu.Lock()
		defer mu.Unlock()
		diagnostics = append(diagnostics, d)
	})
	events := etoroSubscribe(t, ctx, c)
	etoroAssertOrderEvent(t, nextEvent(t, events), "mapped", "42")
	c.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(diagnostics) != 2 || diagnostics[0].Detail != etoroPushUnknown || diagnostics[1].Detail != etoroPushUnreadable {
		t.Fatalf("unknown and unreadable pushes must diagnose once and continue; position pushes ignored: %+v", diagnostics)
	}
	wantRemediation := []string{
		"Officer used only the order id from the push, when one was present, to refresh that order from the venue's order history, and kept reading the stream. This is a venue or Officer issue, not your configuration - please report it if it repeats.",
		venueStreamSkipped,
	}
	for i, d := range diagnostics {
		if d.Level != health.DiagWarn || d.Code != health.CodeUnparsableData || d.Kind != health.DiagKindProvider ||
			d.Title != "Unreadable data on the venue stream" ||
			d.Remediation != wantRemediation[i] {
			t.Fatalf("push diagnostic must describe how Officer handled that push: %+v", d)
		}
	}
}

func TestEtoroStreamRedaction(t *testing.T) {
	var credentials map[string]string
	if err := json.Unmarshal([]byte(etoroTestCredentials), &credentials); err != nil {
		t.Fatal(err)
	}
	var spellings []string
	for _, value := range credentials {
		escaped, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		quoted := strconv.Quote(value)
		spellings = append(spellings, value, string(escaped[1:len(escaped)-1]), quoted[1:len(quoted)-1], url.QueryEscape(value), url.PathEscape(value))
	}
	leak := strings.Join(spellings, " | ")
	for _, site := range []string{"ack-error", "close-error", "diagnostic", "invalid-envelope", "dial-error"} {
		t.Run(site, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if site == "ack-error" {
					id := etoroReadRequest(t, ctx, conn, "Authenticate")
					raw, _ := json.Marshal(map[string]any{"id": id, "operation": "Authenticate", "success": false, "errorCode": leak, "errorMessage": leak})
					sendFrame(t, ctx, conn, websocket.MessageText, string(raw))
					_, _, _ = conn.Read(ctx)
					return
				}
				if !etoroHandshake(t, ctx, conn) {
					return
				}
				switch site {
				case "close-error":
					// One spelling fits the websocket close reason limit.
					_ = conn.Close(websocket.StatusPolicyViolation, spellings[0])
				case "diagnostic":
					etoroPush(t, ctx, conn, leak, leak)
				case "invalid-envelope":
					sendFrame(t, ctx, conn, websocket.MessageText, "{"+leak)
				}
				_, _, _ = conn.Read(ctx)
			}))
			defer server.Close()
			c := etoroStreamConnector(t, server)
			observed := make(chan string, 4)
			check := func(message string) {
				for _, value := range spellings {
					if strings.Contains(message, value) {
						t.Error("credential spelling escaped connector redaction")
					}
				}
			}
			c.SetStatusReporter(func(ok bool, message string) {
				check(message)
				if !ok {
					observed <- message
				}
			})
			c.SetDiagnosticReporter(func(d health.Diagnostic) {
				raw, _ := json.Marshal(d)
				check(string(raw))
				observed <- d.Detail
			})
			if site == "dial-error" {
				c.streamURL = ":" + leak
			}
			if site != "diagnostic" {
				_, err := c.stream(ctx, make(chan fwtrading.Event, 1))
				if err == nil {
					t.Fatal("expected explicit stream error")
				}
				check(err.Error())
			}
			events, err := c.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if site != "ack-error" && site != "dial-error" {
				if nextEvent(t, events).Kind != fwtrading.EventConnected {
					t.Fatal("missing connected event")
				}
			}
			select {
			case message := <-observed:
				if message == "" {
					t.Fatal("stream failure was silent")
				}
			case <-ctx.Done():
				t.Fatal("stream error or diagnostic missing")
			}
			c.Close()
		})
	}
}

func TestEtoroStreamShutdown(t *testing.T) {
	for _, stage := range []string{"authenticate", "subscribe", "read", "publish", "backoff"} {
		for _, action := range []string{"close", "cancel"} {
			t.Run(stage+"/"+action, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				reached := make(chan struct{})
				serverDone := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(serverDone)
					conn := acceptAlpaca(t, w, r)
					if conn == nil {
						return
					}
					defer func() { _ = conn.CloseNow() }()
					id := etoroReadRequest(t, ctx, conn, "Authenticate")
					if stage == "authenticate" {
						close(reached)
						_, _, _ = conn.Read(ctx)
						return
					}
					if stage == "backoff" {
						sendFrame(t, ctx, conn, websocket.MessageText, fmt.Sprintf(`{"id":%q,"operation":"Authenticate","success":false,"errorCode":"Unauthorized"}`, id))
						return
					}
					if !etoroAck(t, ctx, conn, id, "Authenticate") {
						return
					}
					id = etoroReadRequest(t, ctx, conn, "Subscribe")
					if stage != "subscribe" {
						etoroAck(t, ctx, conn, id, "Subscribe")
					}
					if stage != "publish" {
						close(reached)
					}
					_, _, _ = conn.Read(ctx)
				}))
				defer server.Close()
				c := etoroStreamConnector(t, server)
				if stage == "publish" {
					c.SetStatusReporter(func(ok bool, _ string) {
						if ok {
							close(reached)
						}
					})
				}
				if stage == "backoff" {
					c.SetStatusReporter(func(ok bool, _ string) {
						if !ok {
							close(reached)
						}
					})
				}
				events, err := c.Subscribe(ctx)
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-reached:
				case <-ctx.Done():
					t.Fatal("stream did not reach shutdown stage")
				}
				if stage == "read" {
					_ = nextEvent(t, events)
				}
				ended := make(chan struct{})
				go func() {
					if action == "close" {
						c.Close()
					} else {
						cancel()
						<-c.lifecycle.done
					}
					close(ended)
				}()
				select {
				case <-ended:
				case <-time.After(time.Second):
					t.Fatal("stream goroutine did not end on Close or context cancellation")
				}
				select {
				case <-c.lifecycle.done:
				case <-time.After(time.Second):
					t.Fatal("Close returned before the stream goroutine ended")
				}
				for range events {
				}
				select {
				case <-serverDone:
				case <-time.After(time.Second):
					t.Fatal("stream reader did not release the socket")
				}
				c.Close()
				if _, err := c.Subscribe(context.Background()); err == nil {
					t.Fatal("closed connector subscribed again")
				}
			})
		}
	}
}
