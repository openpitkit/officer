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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

func acceptAlpaca(t *testing.T, w http.ResponseWriter, r *http.Request) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error("websocket accept failed")
		return nil
	}
	return conn
}

func readAuth(t *testing.T, ctx context.Context, conn *websocket.Conn) bool {
	t.Helper()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Error("auth frame missing")
		return false
	}
	var auth map[string]string
	if json.Unmarshal(raw, &auth) != nil || len(auth) != 3 || auth["action"] != "auth" || auth["key"] != "test-key-marker" || auth["secret"] != "test-secret-marker" {
		t.Error("incorrect auth frame")
		return false
	}
	return true
}

func sendFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, raw string) bool {
	t.Helper()
	if conn.Write(ctx, kind, []byte(raw)) != nil {
		t.Error("server frame write failed")
		return false
	}
	return true
}

func completeHandshake(t *testing.T, ctx context.Context, conn *websocket.Conn) bool {
	t.Helper()
	if !sendFrame(t, ctx, conn, websocket.MessageBinary, `{"stream":"authorization","data":{"status":"authorized","action":"authenticate"}}`) {
		return false
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Error("listen frame missing")
		return false
	}
	var listen struct {
		Action string `json:"action"`
		Data   struct {
			Streams []string `json:"streams"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &listen) != nil || listen.Action != "listen" || len(listen.Data.Streams) != 1 || listen.Data.Streams[0] != "trade_updates" {
		t.Error("incorrect listen frame")
		return false
	}
	return sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"listening","data":{"streams":["trade_updates"]}}`)
}

func nextEvent(t *testing.T, events <-chan fwtrading.Event) fwtrading.Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream channel closed early")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("stream event timeout")
	}
	return fwtrading.Event{}
}

func subscribeAlpaca(t *testing.T, c *alpacaConnector) <-chan fwtrading.Event {
	t.Helper()
	events, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal("Subscribe failed")
	}
	return events
}

func assertClosed(t *testing.T, c *alpacaConnector, events <-chan fwtrading.Event) {
	t.Helper()
	c.Close()
	c.Close()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("Close did not end channel")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not close channel")
	}
}

func updateFrame(event, qty, cum, price string) string {
	return fmt.Sprintf(`{"stream":"trade_updates","data":{"event":%q,"execution_id":"execution","order":{"id":"venue","client_order_id":"client","qty":"3.000000000000000001","filled_qty":%q},"timestamp":"2026-09-01T10:00:00.123456789Z","qty":%q,"price":%q}}`, event, cum, qty, price)
}

func TestStreamEvents(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		for _, raw := range []string{"{", "null", updateFrame("future_event", "1", "1", "1"), updateFrame("partial_fill", "1", "bad", "1"), `{"stream":"trade_updates","data":{"event":"fill","timestamp":"bad"}}`} {
			if !sendFrame(t, ctx, conn, websocket.MessageText, raw) {
				return
			}
		}
		if !sendFrame(t, ctx, conn, websocket.MessageBinary, updateFrame("partial_fill", "1.000000000000000001", "1.000000000000000001", "0.123456789123456789")) {
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageText, updateFrame("fill", "2.000", "3.000000000000000001", "99999999999999.999999999")) {
			return
		}
		for _, event := range []string{"canceled", "expired", "rejected", "replaced"} {
			if !sendFrame(t, ctx, conn, websocket.MessageBinary, updateFrame(event, "", "", "")) {
				return
			}
		}
		<-release
	}))
	defer server.Close()
	defer close(release)
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("Connected missing after listen")
	}
	if event := nextEvent(t, events); event.Kind != fwtrading.EventOrder || event.Ref.VenueOrderID != "venue" {
		t.Fatal("invalid fill did not request reconciliation")
	}
	partial := nextEvent(t, events)
	if partial.Kind != fwtrading.EventFill || partial.Ref.ClientOrderID != "client" || partial.Ref.VenueOrderID != "venue" || partial.Fill.Quantity != "1.000000000000000001" || partial.Fill.Price != "0.123456789123456789" || partial.Fill.CumQuantity != "1.000000000000000001" || partial.Fill.LeavesQuantity != "2" || partial.Fill.Final || partial.Fill.At.Format(time.RFC3339Nano) != "2026-09-01T10:00:00.123456789Z" {
		t.Fatal("partial fill mapping lost exact fields")
	}
	final := nextEvent(t, events)
	if partial.Fill.Commission != nil || final.Fill.Commission != nil {
		t.Fatal("Alpaca stream attached a fee to a fill")
	}
	if final.Kind != fwtrading.EventFill || final.Ref.ClientOrderID != "client" || final.Ref.VenueOrderID != "venue" || final.Fill.Quantity != "2.000" || final.Fill.Price != "99999999999999.999999999" || final.Fill.CumQuantity != "3.000000000000000001" || final.Fill.LeavesQuantity != "0" || !final.Fill.Final || final.Fill.At.Format(time.RFC3339Nano) != "2026-09-01T10:00:00.123456789Z" {
		t.Fatal("final fill mapping lost exact fields")
	}
	for range 4 {
		event := nextEvent(t, events)
		if event.Kind != fwtrading.EventOrder || event.Ref.VenueOrderID != "venue" || event.Ref.ClientOrderID != "client" {
			t.Fatal("terminal update did not request lookup")
		}
	}
	assertClosed(t, c, events)
}

func TestStreamReconnectBackoff(t *testing.T) {
	attempts := make(chan time.Time, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts <- time.Now()
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if readAuth(t, ctx, conn) {
			completeHandshake(t, ctx, conn)
		}
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	var previous time.Time
	for i := range 4 {
		if nextEvent(t, events).Kind != fwtrading.EventConnected {
			t.Fatal("listen acknowledgement did not connect")
		}
		var at time.Time
		select {
		case at = <-attempts:
		case <-time.After(time.Second):
			t.Fatal("dial timestamp missing")
		}
		if i > 0 {
			minimum := alpacaReconnectMin * time.Duration(1<<(i-1))
			if at.Sub(previous) < minimum-25*time.Millisecond {
				t.Fatalf("reconnect spacing did not grow: dial %d waited %v, want at least %v", i+1, at.Sub(previous), minimum)
			}
		}
		previous = at
	}
	assertClosed(t, c, events)
}

func TestStreamSlowConsumerReconnectBackoff(t *testing.T) {
	attempts := make(chan time.Time, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		at := time.Now()
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		_ = conn.CloseNow()
		attempts <- at
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("listen acknowledgement did not connect")
	}
	for range 2 {
		select {
		case <-attempts:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not close after listen acknowledgement")
		}
	}
	<-time.After(alpacaReconnectMax + 50*time.Millisecond)
	releasedAt := time.Now()
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("blocked Connected missing")
	}
	select {
	case at := <-attempts:
		minimum := 2 * alpacaReconnectMin
		if elapsed := at.Sub(releasedAt); elapsed < minimum-25*time.Millisecond {
			t.Fatalf("slow consumer reset reconnect backoff: waited %v, want at least %v", elapsed, minimum)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect after releasing slow consumer missing")
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("reconnect Connected missing")
	}
	assertClosed(t, c, events)
}

func TestStreamRejectsRedirect(t *testing.T) {
	var targetDials, authFrames atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetDials.Add(1)
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, raw, err := conn.Read(ctx)
		var frame struct {
			Action string `json:"action"`
		}
		if err == nil && json.Unmarshal(raw, &frame) == nil && frame.Action == "auth" {
			authFrames.Add(1)
		}
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stream", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	healthy, err := c.stream(ctx, make(chan fwtrading.Event, 1))
	if targetDials.Load() != 0 || authFrames.Load() != 0 {
		t.Fatal("redirect target received a dial or auth frame")
	}
	if err == nil || healthy {
		t.Fatal("redirect was not a failed connection")
	}
}

func TestStreamHealthyConnection(t *testing.T) {
	for _, condition := range []string{"trade update", "uptime"} {
		t.Run(condition, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx, cancel := context.WithTimeout(context.Background(), 2*alpacaReconnectMax)
				defer cancel()
				if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
					return
				}
				if condition == "trade update" {
					sendFrame(t, ctx, conn, websocket.MessageText, updateFrame("partial_fill", "1", "1", "1"))
				} else {
					timer := time.NewTimer(alpacaReconnectMax + 50*time.Millisecond)
					defer timer.Stop()
					<-timer.C
				}
			}))
			defer server.Close()
			c := testAlpacaConnector(t, server)
			ctx, cancel := context.WithTimeout(context.Background(), 2*alpacaReconnectMax)
			defer cancel()
			events := make(chan fwtrading.Event, 2)
			healthy, err := c.stream(ctx, events)
			if err == nil || !healthy {
				t.Fatal("healthy connection did not allow backoff reset")
			}
			if nextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("Connected missing")
			}
			if condition == "trade update" && nextEvent(t, events).Kind != fwtrading.EventFill {
				t.Fatal("trade update missing")
			}
		})
	}
}

// alpacaStatus is one stream status report.
type alpacaStatus struct {
	ok      bool
	message string
}

// alpacaReports records what a connector reports to its owner.
type alpacaReports struct {
	mu          sync.Mutex
	statuses    []alpacaStatus
	diagnostics []health.Diagnostic
}

// recordAlpacaReports installs a fresh recorder as both reporters of c.
func recordAlpacaReports(c *alpacaConnector) *alpacaReports {
	r := &alpacaReports{}
	c.SetStatusReporter(r.status)
	c.SetDiagnosticReporter(r.diagnostic)
	return r
}

func (r *alpacaReports) status(ok bool, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statuses = append(r.statuses, alpacaStatus{ok, message})
}

func (r *alpacaReports) diagnostic(diag health.Diagnostic) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.diagnostics = append(r.diagnostics, diag)
}

func (r *alpacaReports) snapshot() ([]alpacaStatus, []health.Diagnostic) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.statuses), slices.Clone(r.diagnostics)
}

// streamStatuses installs a status reporter on c that delivers its first
// reports; the rest are dropped.
func streamStatuses(c *alpacaConnector) <-chan alpacaStatus {
	statuses := make(chan alpacaStatus, 8)
	c.SetStatusReporter(func(ok bool, message string) {
		select {
		case statuses <- alpacaStatus{ok, message}:
		default:
		}
	})
	return statuses
}

func nextStatus(t *testing.T, statuses <-chan alpacaStatus) alpacaStatus {
	t.Helper()
	select {
	case status := <-statuses:
		return status
	case <-time.After(2 * time.Second):
		t.Fatal("stream status timeout")
	}
	return alpacaStatus{}
}

func TestStreamRedactsOverlappingCredentials(t *testing.T) {
	for _, tc := range []struct{ name, key, secret, message string }{
		{"key contains secret", "overlap-long", "overlap", "refused overlap-long overlap"},
		{"secret contains key", "overlap", "overlap-long", "refused overlap-long overlap"},
		// The venue echoes the key URL-escaped, and the raw secret is a
		// prefix of that spelling.
		{"escaped key contains secret", "prefix/very-long-suffix", "prefix", "refused prefix%2Fvery-long-suffix prefix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if _, _, err := conn.Read(ctx); err != nil {
					t.Error("auth frame missing")
					return
				}
				sendFrame(t, ctx, conn, websocket.MessageText, `{"action":"error","data":{"error_message":"`+tc.message+`"}}`)
			}))
			defer server.Close()
			c := testAlpacaConnectorWith(t, server, `{"apiKey":"`+tc.key+`","apiSecret":"`+tc.secret+`"}`)
			statuses := streamStatuses(c)
			events := subscribeAlpaca(t, c)
			want := "alpaca: stream server error: refused [redacted] [redacted]"
			if status := nextStatus(t, statuses); status.ok || status.message != want {
				t.Fatalf("status = %+v, want %q", status, want)
			}
			assertClosed(t, c, events)
		})
	}
}

func TestStreamReportsUnparsableOncePerConnectionAndEveryOverfill(t *testing.T) {
	var attempts atomic.Int32
	overfill := updateFrame("fill", "1", "3.000000000000000002", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		for range 3 {
			for _, raw := range []string{"{", `{"stream":"trade_updates","data":{"event":42}}`, overfill} {
				if !sendFrame(t, ctx, conn, websocket.MessageText, raw) {
					return
				}
			}
		}
		if !sendFrame(t, ctx, conn, websocket.MessageText, updateFrame("fill", "1", "1", "1")) {
			return
		}
		if attempt > 1 {
			_, _, _ = conn.Read(ctx)
		}
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	reports := recordAlpacaReports(c)
	events := subscribeAlpaca(t, c)
	for range 2 {
		if nextEvent(t, events).Kind != fwtrading.EventConnected {
			t.Fatal("Connected missing")
		}
		for range 3 {
			if event := nextEvent(t, events); event.Kind != fwtrading.EventOrder {
				t.Fatalf("overfill emitted %+v, want an order event", event)
			}
		}
		if nextEvent(t, events).Kind != fwtrading.EventFill {
			t.Fatal("fill after the malformed frames missing")
		}
	}
	assertClosed(t, c, events)
	_, diagnostics := reports.snapshot()
	frame := *alpacaUnparsable(health.DiagWarn, alpacaFrameUnreadable, alpacaSkipped)
	header := *alpacaUnparsable(health.DiagError, alpacaHeaderUnreadable, alpacaDropped)
	connection := []health.Diagnostic{frame, header, alpacaOverfill, alpacaOverfill, alpacaOverfill}
	if want := slices.Repeat(connection, 2); !reflect.DeepEqual(diagnostics, want) {
		t.Fatalf("diagnostics = %+v, want each unparsable site once per connection and every overfill", diagnostics)
	}
}

// captureStreamLogs routes the default logger, at debug level, to the
// returned buffer until the test ends.
func captureStreamLogs(t *testing.T) *lockedLogBuffer {
	t.Helper()
	logs := &lockedLogBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// assertStreamLogsNothing fails on any connector log line but the debug line
// about closing the connection: failures and unreadable data are reported.
func assertStreamLogsNothing(t *testing.T, logs *lockedLogBuffer) {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if line != "" && !strings.Contains(line, `msg="alpaca: close stream connection"`) {
			t.Fatalf("connector logged instead of reporting: %s", line)
		}
	}
}

// alpacaOverfill is the diagnostic of a fill update that reports more filled
// than the order quantity.
var alpacaOverfill = health.Diagnostic{
	Level: health.DiagError, Code: health.CodeInconsistentData,
	Kind: health.DiagKindProvider, Title: alpacaInconsistentTitle,
	Detail: alpacaFillOverfilled, Remediation: alpacaNotBooked,
}

func TestStreamFailureReports(t *testing.T) {
	logs := captureStreamLogs(t)
	var attempts atomic.Int32
	timestamp := `"timestamp":"2026-09-01T10:00:00.123456789Z",`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) {
			return
		}
		if attempt == 2 {
			_ = conn.Close(websocket.StatusPolicyViolation, "refused test-key-marker test-secret-marker")
			return
		}
		if attempt > 2 {
			if completeHandshake(t, ctx, conn) {
				_, _, _ = conn.Read(ctx)
			}
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":[]}}`) ||
			!sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":"authorized"}}`) {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Error("listen frame missing")
			return
		}
		for _, raw := range []string{
			`{"stream":"listening","data":{"streams":"trade_updates"}}`,
			`{"stream":"listening","data":{"streams":["trade_updates"]}}`,
			"{",
			`{"stream":"trade_updates","data":{"event":42}}`,
			strings.Replace(updateFrame("fill", "1", "1", "1"), `"client_order_id":"client"`, `"client_order_id":null`, 1),
			strings.Replace(updateFrame("fill", "1", "1", "1"), timestamp, `"timestamp":"bad",`, 1),
			strings.Replace(updateFrame("fill", "1", "1", "1"), timestamp, "", 1),
			updateFrame("fill", "bad", "1", "1"),
			updateFrame("fill", "1", "3.000000000000000002", "1"),
			strings.Replace(updateFrame("fill", "1", "1", "1"), `"id":"venue"`, `"id":7`, 1),
			`{"stream":"trade_updates","data":{"event":"new","order":{"id":"venue"}}}`,
			`{"action":"error","data":{"error_message":"venue unavailable test-secret-marker test-key-marker"}}`,
		} {
			if !sendFrame(t, ctx, conn, websocket.MessageText, raw) {
				return
			}
		}
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	reports := recordAlpacaReports(c)
	events := subscribeAlpaca(t, c)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("Connected missing")
	}
	// The fill without a usable client order id emits nothing, and no update
	// below is booked as a fill.
	both := fwtrading.OrderRef{VenueOrderID: "venue", ClientOrderID: "client"}
	for _, want := range []struct {
		fill string
		ref  fwtrading.OrderRef
	}{
		{"an unreadable body", both},
		{"an unreadable timestamp", both},
		{"unreadable decimals", both},
		{"more filled than the order quantity", both},
		{"no usable venue order id", fwtrading.OrderRef{ClientOrderID: "client"}},
	} {
		if event := nextEvent(t, events); event.Kind != fwtrading.EventOrder || event.Ref != want.ref {
			t.Fatalf("fill with %s emitted %+v, want an order event for %+v", want.fill, event, want.ref)
		}
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("reconnect missing")
	}
	assertClosed(t, c, events)
	statuses, diagnostics := reports.snapshot()
	want := []health.Diagnostic{
		*alpacaUnparsable(health.DiagWarn, alpacaAuthUnreadable, alpacaSkipped),
		*alpacaUnparsable(health.DiagWarn, alpacaListenUnreadable, alpacaSkipped),
		*alpacaUnparsable(health.DiagWarn, alpacaFrameUnreadable, alpacaSkipped),
		*alpacaUnparsable(health.DiagError, alpacaHeaderUnreadable, alpacaDropped),
		*alpacaUnparsable(health.DiagError, alpacaRefUnreadable, alpacaDropped),
		*alpacaUnparsable(health.DiagWarn, alpacaFillUnreadable, alpacaReconciled),
		*alpacaUnparsable(health.DiagWarn, alpacaFillUntimed, alpacaReconciled),
		*alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled),
		alpacaOverfill,
		*alpacaUnparsable(health.DiagWarn, alpacaVenueIDUnusable, alpacaReconciled),
	}
	if len(diagnostics) != len(want) {
		t.Fatalf("diagnostics = %+v, want %d", diagnostics, len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(diagnostics[i], want[i]) {
			t.Fatalf("diagnostic %d = %+v, want %+v", i, diagnostics[i], want[i])
		}
	}
	if len(statuses) != 4 || !statuses[0].ok || statuses[1].ok || statuses[2].ok || !statuses[3].ok ||
		!strings.Contains(statuses[1].message, "venue unavailable") ||
		!strings.Contains(statuses[2].message, "StatusPolicyViolation") {
		t.Fatalf("statuses = %+v, want ok, both venue causes, ok", statuses)
	}
	for _, status := range statuses {
		if strings.Contains(status.message, "test-secret-marker") || strings.Contains(status.message, "test-key-marker") {
			t.Fatal("credentials leaked into a status report")
		}
	}
	assertStreamLogsNothing(t, logs)
}

func TestStreamBoundedDecimals(t *testing.T) {
	for _, field := range []string{"order.qty", "order.filled_qty", "qty", "price"} {
		for _, tc := range []struct{ name, value string }{
			{"exponent", "1e3"}, {"large exponent", "1e1000000000"},
			{"small exponent", "1e-2147483647"},
			{"overlong", strings.Repeat("1", alpacaDecimalMaxLength+1)},
			{"space", "1 "}, {"negative", "-1"}, {"malformed", "bad"},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				var frame map[string]any
				if json.Unmarshal([]byte(updateFrame("fill", "1", "1", "1")), &frame) != nil {
					t.Fatal("invalid fixture")
				}
				data := frame["data"].(map[string]any)
				if strings.HasPrefix(field, "order.") {
					data["order"].(map[string]any)[strings.TrimPrefix(field, "order.")] = tc.value
				} else {
					data[field] = tc.value
				}
				raw, err := json.Marshal(frame)
				if err != nil {
					t.Fatal("cannot encode fixture")
				}
				assertStreamReconciles(t, string(raw))
			})
		}
	}
}

func TestStreamUnusableUpdateReconciles(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"fill timestamp", strings.ReplaceAll(updateFrame("fill", "1", "1", "1"), "2026-09-01T10:00:00.123456789Z", "bad")},
		{"terminal timestamp", `{"stream":"trade_updates","data":{"event":"canceled","order":{"id":"venue","client_order_id":"client"},"timestamp":"bad"}}`},
		{"wrong decimal type", `{"stream":"trade_updates","data":{"event":"fill","order":{"id":"venue","client_order_id":"client","qty":{}}}}`},
		{"missing timestamp", `{"stream":"trade_updates","data":{"event":"fill","order":{"id":"venue","client_order_id":"client","qty":"1","filled_qty":"1"},"qty":"1","price":"1"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) { assertStreamReconciles(t, tc.raw) })
	}
	for _, raw := range []string{`{}`, `{"event":"fill","timestamp":"bad"}`, `{"event":"canceled","order":{"id":"","client_order_id":""}}`} {
		if _, ok, _ := (&alpacaConnector{}).parseAlpacaTradeUpdate(json.RawMessage(raw)); ok {
			t.Fatal("update without a usable ref was emitted")
		}
	}
}

func TestStreamTradeUpdateDiscriminatorAndReferences(t *testing.T) {
	type testCase struct {
		name, raw string
		want      fwtrading.OrderRef
	}
	cases := []testCase{
		{"unknown malformed timestamp", `{"event":"future_event","order":{"id":"venue","client_order_id":"client"},"timestamp":"bad"}`, fwtrading.OrderRef{}},
		{"unknown malformed decimals", `{"event":"future_event","order":{"id":"venue","client_order_id":"client","qty":{}},"qty":[],"price":false}`, fwtrading.OrderRef{}},
		{"unknown malformed order", `{"event":"future_event","order":7}`, fwtrading.OrderRef{}},
		{"malformed discriminator", `{"event":7,"order":{"id":"venue"},"timestamp":"bad"}`, fwtrading.OrderRef{}},
	}
	for _, event := range []string{"fill", "partial_fill", "canceled", "expired", "rejected", "replaced"} {
		for _, ref := range []struct {
			name, raw string
			want      fwtrading.OrderRef
		}{
			{"numeric venue id", `{"id":7,"client_order_id":"client"}`, fwtrading.OrderRef{ClientOrderID: "client"}},
			{"object client id", `{"id":"venue","client_order_id":{}}`, fwtrading.OrderRef{}},
			{"null venue id", `{"id":null,"client_order_id":"client"}`, fwtrading.OrderRef{ClientOrderID: "client"}},
			{"empty client id", `{"id":"venue","client_order_id":""}`, fwtrading.OrderRef{}},
			{"missing venue id", `{"client_order_id":"client"}`, fwtrading.OrderRef{ClientOrderID: "client"}},
			{"both ids unusable", `{"id":7,"client_order_id":{}}`, fwtrading.OrderRef{}},
		} {
			cases = append(cases, testCase{event + "/" + ref.name,
				fmt.Sprintf(`{"event":%q,"order":%s,"timestamp":"bad"}`, event, ref.raw), ref.want})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
					return
				}
				if !sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"trade_updates","data":`+tc.raw+`}`) {
					return
				}
				if !sendFrame(t, ctx, conn, websocket.MessageBinary, updateFrame("partial_fill", "1", "1", "1")) {
					return
				}
				_, _, _ = conn.Read(ctx)
			}))
			defer server.Close()
			c := testAlpacaConnector(t, server)
			events := subscribeAlpaca(t, c)
			if nextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("Connected missing")
			}
			event := nextEvent(t, events)
			if tc.want != (fwtrading.OrderRef{}) {
				if event.Kind != fwtrading.EventOrder || event.Ref != tc.want {
					t.Fatal("supported update did not reconcile with the usable order ref")
				}
				event = nextEvent(t, events)
			}
			if event.Kind != fwtrading.EventFill {
				t.Fatal("ignored or dropped update was emitted before the valid fill")
			}
			assertClosed(t, c, events)
		})
	}
}

func assertStreamReconciles(t *testing.T, raw string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageText, raw) {
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageBinary, updateFrame("partial_fill", "1", "1", "1")) {
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("Connected missing")
	}
	event := nextEvent(t, events)
	if event.Kind != fwtrading.EventOrder || event.Ref.ClientOrderID != "client" || event.Ref.VenueOrderID != "venue" {
		t.Fatal("unusable update with known ref did not request reconciliation")
	}
	if nextEvent(t, events).Kind != fwtrading.EventFill {
		t.Fatal("valid update after invalid payload was lost")
	}
	assertClosed(t, c, events)
}

func TestStreamReconnect(t *testing.T) {
	for _, failure := range []string{"close", "server error", "handshake server error"} {
		t.Run(failure, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := attempts.Add(1)
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if !readAuth(t, ctx, conn) {
					return
				}
				if attempt == 1 && failure == "handshake server error" {
					sendFrame(t, ctx, conn, websocket.MessageBinary, `{"action":"error","data":{"error_message":"test-secret-marker"}}`)
					return
				}
				if !completeHandshake(t, ctx, conn) {
					return
				}
				if attempt == 1 {
					if failure == "server error" {
						sendFrame(t, ctx, conn, websocket.MessageText, `{"action":"error","data":{"error_message":"test-secret-marker"}}`)
					}
					return
				}
				_, _, _ = conn.Read(ctx)
			}))
			defer server.Close()
			c := testAlpacaConnector(t, server)
			events := subscribeAlpaca(t, c)
			if nextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("Connected missing")
			}
			if failure != "handshake server error" && nextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("Connected missing after reconnect")
			}
			if attempts.Load() < 2 {
				t.Fatal("stream failed to reconnect")
			}
			assertClosed(t, c, events)
		})
	}
}

type lockedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *lockedLogBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

func TestStreamUnauthorized(t *testing.T) {
	logs := captureStreamLogs(t)
	attempts := make(chan time.Time, 4)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := count.Add(1)
		attempts <- time.Now()
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) {
			return
		}
		if attempt == 1 {
			sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":"unauthorized","action":"authenticate","extra":"test-secret-marker"}}`)
			return
		}
		if !completeHandshake(t, ctx, conn) {
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	reports := recordAlpacaReports(c)
	// The ok report holds the stream until the test has seen that
	// Connected is not delivered before it.
	reported, release := make(chan struct{}), make(chan struct{})
	releaseStream := sync.OnceFunc(func() { close(release) })
	defer releaseStream()
	c.SetStatusReporter(func(ok bool, message string) {
		reports.status(ok, message)
		if ok {
			close(reported)
			<-release
		}
	})
	events := subscribeAlpaca(t, c)
	var first, second time.Time
	select {
	case first = <-attempts:
	case <-time.After(time.Second):
		t.Fatal("initial connection missing")
	}
	select {
	case <-events:
		t.Fatal("unauthorized stream reported Connected")
	case <-time.After(alpacaReconnectMax / 2):
	}
	select {
	case second = <-attempts:
	case <-time.After(alpacaReconnectMax):
		t.Fatal("unauthorized stream not retried")
	}
	if second.Sub(first) < alpacaReconnectMax-50*time.Millisecond {
		t.Fatal("unauthorized retry did not use max backoff")
	}
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("authorized retry reported no ok status")
	}
	select {
	case <-events:
		t.Fatal("Connected delivered before the ok status report")
	case <-time.After(50 * time.Millisecond):
	}
	releaseStream()
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("authorized retry did not connect")
	}
	assertClosed(t, c, events)
	statuses, diagnostics := reports.snapshot()
	want := []alpacaStatus{{false, "alpaca: stream authorization refused"}, {true, ""}}
	if !reflect.DeepEqual(statuses, want) || len(diagnostics) != 0 {
		t.Fatalf("reports = %+v, %+v, want statuses %+v", statuses, diagnostics, want)
	}
	assertStreamLogsNothing(t, logs)
}

func TestStreamUnauthorizedHoldsMaxUntilHealthy(t *testing.T) {
	attempts := make(chan time.Time, 8)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := count.Add(1)
		at := time.Now()
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) {
			return
		}
		// The first dial is refused, the third delivers one trade update
		// and so is healthy, and every other one closes before any
		// authorization reply, an ordinary failure.
		switch attempt {
		case 1:
			sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":"unauthorized","action":"authenticate"}}`)
		case 3:
			if !completeHandshake(t, ctx, conn) || !sendFrame(t, ctx, conn, websocket.MessageText, updateFrame("partial_fill", "1", "1", "1")) {
				return
			}
		}
		// Reported once the handler's frames are done, so closing the
		// connector after the last dial cannot cut the handler short.
		attempts <- at
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	dials := make([]time.Time, 0, 4)
	nextDial := func() {
		t.Helper()
		select {
		case at := <-attempts:
			dials = append(dials, at)
		case <-time.After(alpacaReconnectMax + time.Second):
			t.Fatalf("dial %d missing", len(dials)+1)
		}
	}
	for range 3 {
		nextDial()
	}
	if waited := dials[2].Sub(dials[1]); waited < alpacaReconnectMax-50*time.Millisecond {
		t.Fatalf("failure after a refused authorization waited %v, want at least %v until a healthy stream", waited, alpacaReconnectMax)
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("healthy dial did not connect")
	}
	if nextEvent(t, events).Kind != fwtrading.EventFill {
		t.Fatal("healthy dial delivered no trade update")
	}
	healthyAt := time.Now()
	nextDial()
	if waited := dials[3].Sub(healthyAt); waited >= alpacaReconnectMax/2 {
		t.Fatalf("failure after a healthy stream waited %v, want under %v", waited, alpacaReconnectMax/2)
	}
	assertClosed(t, c, events)
}

func TestStreamRequiresReporters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	for _, missing := range []string{"status", "diagnostic"} {
		c := testAlpacaConnector(t, server)
		if missing == "status" {
			c.SetStatusReporter(nil)
		} else {
			c.SetDiagnosticReporter(nil)
		}
		if events, err := c.Subscribe(context.Background()); err == nil || events != nil {
			t.Fatalf("Subscribe without a %s reporter accepted", missing)
		}
	}
}

func TestStreamTradeUpdateProblems(t *testing.T) {
	data := func(frame string) string {
		var parsed alpacaFrame
		if json.Unmarshal([]byte(frame), &parsed) != nil {
			t.Fatal("invalid fixture")
		}
		return string(parsed.Data)
	}
	timestamp := `"timestamp":"2026-09-01T10:00:00.123456789Z",`
	good := updateFrame("fill", "1", "1", "1")
	notional := func(qty string) string {
		return `{"event":"fill","order":{"id":"venue","client_order_id":"client",` + qty +
			`"filled_qty":"0.5"},` + timestamp + `"qty":"0.5","price":"200"}`
	}
	notionalFill := notional(`"qty":null,"notional":"100",`)
	both := fwtrading.OrderRef{VenueOrderID: "venue", ClientOrderID: "client"}
	type testCase struct {
		name, raw string
		emit      fwtrading.EventKind
		ref       fwtrading.OrderRef
		problem   *health.Diagnostic
	}
	cases := []testCase{
		{"null", `null`, "", fwtrading.OrderRef{}, alpacaUnparsable(health.DiagError, alpacaHeaderUnreadable, alpacaDropped)},
		{"no event", `{"order":{"id":"venue"}}`, "", fwtrading.OrderRef{}, alpacaUnparsable(health.DiagError, alpacaHeaderUnreadable, alpacaDropped)},
		{"numeric event", `{"event":42,"order":{"id":"venue"}}`, "", fwtrading.OrderRef{}, alpacaUnparsable(health.DiagError, alpacaHeaderUnreadable, alpacaDropped)},
		{"order not an object", `{"event":"fill","order":7}`, "", fwtrading.OrderRef{}, alpacaUnparsable(health.DiagError, alpacaRefUnreadable, alpacaDropped)},
		{"no order ids", `{"event":"canceled","order":{"id":"","client_order_id":""}}`, "", fwtrading.OrderRef{}, alpacaUnparsable(health.DiagError, alpacaRefUnreadable, alpacaDropped)},
		{"unreadable body", data(strings.Replace(good, timestamp, `"timestamp":"bad",`, 1)), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillUnreadable, alpacaReconciled)},
		{"no timestamp", data(strings.Replace(good, timestamp, "", 1)), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillUntimed, alpacaReconciled)},
		{"bad decimals", data(updateFrame("fill", "bad", "1", "1")), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"overfill", data(updateFrame("fill", "1", "3.000000000000000002", "1")), fwtrading.EventOrder, both, &alpacaOverfill},
		{"partial overfill", data(updateFrame("partial_fill", "1", "3.000000000000000002", "1")), fwtrading.EventOrder, both, &alpacaOverfill},
		{"notional order", notionalFill, fwtrading.EventOrder, both, nil},
		{"notional order without qty", notional(`"notional":"100",`), fwtrading.EventOrder, both, nil},
		{"null qty without notional", notional(`"qty":null,`), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"empty notional", notional(`"qty":null,"notional":"",`), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"notional with bad qty", notional(`"qty":"bad","notional":"100",`), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"unreadable notional", notional(`"qty":null,"notional":"bad",`), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"zero notional", notional(`"qty":null,"notional":"0",`), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"notional with bad price", strings.Replace(notionalFill, `"price":"200"`, `"price":"bad"`, 1), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"notional with bad fill qty", strings.Replace(notionalFill, `"qty":"0.5"`, `"qty":"bad"`, 1), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"notional with bad filled qty", strings.Replace(notionalFill, `"filled_qty":"0.5"`, `"filled_qty":"bad"`, 1), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)},
		{"notional without timestamp", strings.Replace(notionalFill, timestamp, "", 1), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillUntimed, alpacaReconciled)},
		{"notional with unreadable body", strings.Replace(notionalFill, timestamp, `"timestamp":"bad",`, 1), fwtrading.EventOrder, both, alpacaUnparsable(health.DiagWarn, alpacaFillUnreadable, alpacaReconciled)},
		{"good fill", data(good), fwtrading.EventFill, both, nil},
		{"terminal", data(updateFrame("canceled", "", "", "")), fwtrading.EventOrder, both, nil},
		{"unknown event", `{"event":"new","order":{"id":"venue"}}`, "", fwtrading.OrderRef{}, nil},
		{"unknown malformed", `{"event":"future_event","order":7}`, "", fwtrading.OrderRef{}, nil},
	}
	// An otherwise readable fill or terminal update without a usable venue
	// order id is reconciled by its client order id; one without a usable
	// client order id is dropped.
	ids := `"id":"venue","client_order_id":"client"`
	for _, event := range []string{"fill", "canceled"} {
		frame := data(updateFrame(event, "1", "1", "1"))
		for _, venue := range []struct{ name, ids string }{
			{"numeric venue id", `"id":7,"client_order_id":"client"`},
			{"null venue id", `"id":null,"client_order_id":"client"`},
			{"missing venue id", `"client_order_id":"client"`},
			{"empty venue id", `"id":"","client_order_id":"client"`},
		} {
			cases = append(cases, testCase{event + "/" + venue.name, strings.Replace(frame, ids, venue.ids, 1),
				fwtrading.EventOrder, fwtrading.OrderRef{ClientOrderID: "client"},
				alpacaUnparsable(health.DiagWarn, alpacaVenueIDUnusable, alpacaReconciled)})
		}
		for _, client := range []struct{ name, ids string }{
			{"object client id", `"id":"venue","client_order_id":{}`},
			{"null client id", `"id":"venue","client_order_id":null`},
			{"missing client id", `"id":"venue"`},
			{"empty client id", `"id":"venue","client_order_id":""`},
		} {
			cases = append(cases, testCase{event + "/" + client.name, strings.Replace(frame, ids, client.ids, 1),
				"", fwtrading.OrderRef{}, alpacaUnparsable(health.DiagError, alpacaRefUnreadable, alpacaDropped)})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, ok, problem := (&alpacaConnector{}).parseAlpacaTradeUpdate(json.RawMessage(tc.raw))
			if ok != (tc.emit != "") || event.Kind != tc.emit || event.Ref != tc.ref {
				t.Fatalf("emitted %t %q %+v, want %q %+v", ok, event.Kind, event.Ref, tc.emit, tc.ref)
			}
			if !reflect.DeepEqual(problem, tc.problem) {
				t.Fatalf("problem = %+v, want %+v", problem, tc.problem)
			}
		})
	}
}

func TestStreamWaitsForListenAck(t *testing.T) {
	listening := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) {
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":"authorized"}}`) {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Error("listen missing")
			return
		}
		sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"listening","data":{"streams":[]}}`)
		close(listening)
		<-release
		sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"listening","data":{"streams":["trade_updates"]}}`)
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	reports := recordAlpacaReports(c)
	events := subscribeAlpaca(t, c)
	select {
	case <-listening:
	case <-time.After(time.Second):
		t.Fatal("listen request missing")
	}
	select {
	case <-events:
		t.Fatal("Connected before trade_updates listen ack")
	case <-time.After(50 * time.Millisecond):
	}
	if statuses, _ := reports.snapshot(); len(statuses) != 0 {
		t.Fatalf("statuses before trade_updates listen ack = %+v", statuses)
	}
	close(release)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("listen ack did not connect")
	}
	if statuses, _ := reports.snapshot(); !reflect.DeepEqual(statuses, []alpacaStatus{{true, ""}}) {
		t.Fatalf("statuses after Connected = %+v, want one ok", statuses)
	}
	assertClosed(t, c, events)
}

func TestStreamSilenceAndCancellation(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), alpacaHandshakeTimeout+3*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal("Subscribe failed")
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("Connected missing")
	}
	select {
	case <-events:
		t.Fatal("silent stream reconnected or ended")
	case <-time.After(alpacaHandshakeTimeout + 100*time.Millisecond):
	}
	if attempts.Load() != 1 {
		t.Fatal("silent stream redialed")
	}
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("cancellation produced an event")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close stream")
	}
	c.Close()
}

func TestStreamLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	statuses := streamStatuses(c)
	events := subscribeAlpaca(t, c)
	if status := nextStatus(t, statuses); status.ok || status.message == "" {
		t.Fatalf("dial failure status = %+v, want a failure with its cause", status)
	}
	if _, err := c.Subscribe(context.Background()); err == nil {
		t.Fatal("second subscription accepted")
	}
	assertClosed(t, c, events)
	if _, err := c.Subscribe(context.Background()); err == nil {
		t.Fatal("subscription after Close accepted")
	}
	c2 := testAlpacaConnector(t, server)
	c2.Close()
	c2.Close()
	if _, err := c2.Subscribe(context.Background()); err == nil {
		t.Fatal("Close before Subscribe ignored")
	}
}

func FuzzAlpacaTradeUpdate(f *testing.F) {
	for _, value := range []string{"1", "0", "-1", "1e1000000000", "1e-2147483647", strings.Repeat("1", alpacaDecimalMaxLength+1)} {
		var frame alpacaFrame
		_ = json.Unmarshal([]byte(updateFrame("fill", value, value, value)), &frame)
		f.Add(string(frame.Data))
	}
	for _, seed := range []string{`{}`, `{"event":"future_event"}`,
		`{"event":"fill","order":{"id":"venue","client_order_id":"client","qty":"1.000000000000000001","filled_qty":"1.000000000000000002"},"timestamp":"2026-09-01T10:00:00.123456789Z","qty":"1","price":"1"}`,
		`{"event":"partial_fill","order":{"id":"venue","client_order_id":"client","qty":"10","filled_qty":"1"},"timestamp":"2026-09-01T10:00:00.123456789Z","qty":"1","price":"1"}`,
		`{"event":"fill","order":{"id":"venue","client_order_id":"client","qty":"bad","filled_qty":"1"},"timestamp":"2026-09-01T10:00:00.123456789Z"}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		c := &alpacaConnector{}
		event, ok, _ := c.parseAlpacaTradeUpdate(json.RawMessage(raw))
		if !ok {
			return
		}
		if event.Ref.ClientOrderID == "" {
			t.Fatal("emitted event has no client order id")
		}
		if event.Kind != fwtrading.EventFill {
			return
		}
		if event.Ref.VenueOrderID == "" {
			t.Fatal("fill has no venue order id")
		}
		leaves, err := decimal.NewFromString(event.Fill.LeavesQuantity)
		if err != nil || leaves.IsNegative() {
			t.Fatal("fill leaves must be an exact non-negative decimal")
		}
		var update struct {
			Order alpacaOrder `json:"order"`
		}
		if json.Unmarshal([]byte(raw), &update) != nil {
			t.Fatal("fill emitted from an unreadable update")
		}
		qty, qtyErr := decimal.NewFromString(update.Order.Quantity)
		cum, cumErr := decimal.NewFromString(update.Order.FilledQuantity)
		if qtyErr != nil || cumErr != nil || !leaves.Equal(qty.Sub(cum)) {
			t.Fatal("fill leaves must be exactly the order quantity minus its filled quantity")
		}
		for _, value := range []string{event.Fill.Quantity, event.Fill.Price, event.Fill.CumQuantity} {
			if len(value) > alpacaDecimalMaxLength {
				t.Fatal("unbounded venue decimal reached a fill")
			}
			if _, err := domain.ParseOpenQuantity(value); err != nil {
				t.Fatal("non-plain venue decimal reached a fill")
			}
		}
	})
}
