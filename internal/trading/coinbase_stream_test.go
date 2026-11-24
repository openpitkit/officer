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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

type coinbaseTestSocket struct {
	conn   *websocket.Conn
	frames []map[string]any
}

func coinbaseReadSubscriptions(ctx context.Context, conn *websocket.Conn) ([]map[string]any, error) {
	frames := make([]map[string]any, 2)
	for i := range frames {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &frames[i]); err != nil {
			return nil, err
		}
	}
	return frames, nil
}

func coinbaseTestStream(t *testing.T) (*coinbaseConnector, context.Context, <-chan coinbaseTestSocket, <-chan string, <-chan health.Diagnostic) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	sockets := make(chan coinbaseTestSocket, 8)
	c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		frames, err := coinbaseReadSubscriptions(ctx, conn)
		if err != nil {
			return
		}
		select {
		case sockets <- coinbaseTestSocket{conn: conn, frames: frames}:
		case <-ctx.Done():
			return
		}
		_, _, _ = conn.Read(ctx)
	})
	t.Cleanup(cancel)
	failures := make(chan string, 16)
	diags := make(chan health.Diagnostic, 16)
	c.SetStatusReporter(func(ok bool, message string) {
		if !ok {
			select {
			case failures <- message:
			case <-ctx.Done():
			}
		}
	})
	c.SetDiagnosticReporter(func(d health.Diagnostic) {
		select {
		case diags <- d:
		case <-ctx.Done():
		}
	})
	return c, ctx, sockets, failures, diags
}

func coinbaseNextSocket(t *testing.T, ctx context.Context, sockets <-chan coinbaseTestSocket) coinbaseTestSocket {
	t.Helper()
	select {
	case s := <-sockets:
		return s
	case <-ctx.Done():
		t.Fatal("Coinbase stream did not subscribe")
		return coinbaseTestSocket{}
	}
}

func coinbaseNextEvent(t *testing.T, events <-chan fwtrading.Event) fwtrading.Event {
	t.Helper()
	select {
	case e, ok := <-events:
		if !ok {
			t.Fatal("stream closed early")
		}
		return e
	case <-time.After(8 * time.Second):
		t.Fatal("Coinbase event missing")
		return fwtrading.Event{}
	}
}

func coinbasePushFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, channel string, sequence int, events any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"channel": channel, "sequence_num": sequence, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "events": events})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		if t.Context().Err() == nil {
			t.Fatal(err)
		}
	}
}

func coinbaseUserEvent(kind string, orders ...map[string]any) []any {
	if orders == nil {
		orders = []map[string]any{}
	}
	return []any{map[string]any{"type": kind, "orders": orders}}
}

func TestCoinbaseStreamSubscriptionAndUpdates(t *testing.T) {
	c, ctx, sockets, failures, _ := coinbaseTestStream(t)
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := coinbaseNextSocket(t, ctx, sockets)
	for i, channel := range []string{"user", "heartbeats"} {
		if len(s.frames[i]) != 3 || s.frames[i]["type"] != "subscribe" || s.frames[i]["channel"] != channel || s.frames[i]["jwt"] == "" {
			t.Fatalf("subscribe frame: %+v", s.frames[i])
		}
	}
	if s.frames[0]["jwt"] == s.frames[1]["jwt"] {
		t.Fatal("subscribe JWT reused")
	}
	coinbasePushFrame(t, ctx, s.conn, "subscriptions", 0, map[string]any{})
	coinbasePushFrame(t, ctx, s.conn, "future_channel", 1, map[string]any{})
	heartbeat := map[string]any{"current_time": time.Now().UTC().Format(time.RFC3339Nano), "heartbeat_counter": "1"}
	coinbasePushFrame(t, ctx, s.conn, "heartbeats", 2, heartbeat)
	coinbasePushFrame(t, ctx, s.conn, "heartbeats", 3, []any{heartbeat})
	select {
	case e := <-events:
		t.Fatalf("connected before user snapshot: %+v", e)
	case <-time.After(40 * time.Millisecond):
	}
	coinbasePushFrame(t, ctx, s.conn, "user", 4, coinbaseUserEvent("snapshot"))
	if e := coinbaseNextEvent(t, events); e.Kind != fwtrading.EventConnected {
		t.Fatalf("snapshot did not connect: %+v", e)
	}
	coinbasePushFrame(t, ctx, s.conn, "user", 5, coinbaseUserEvent("snapshot", map[string]any{"order_id": "ignored", "client_order_id": "ignored"}))
	raw := []byte(`{"channel":"future_channel","sequence_num":6,"message":{"future":true},"timestamp":42,"events":false}`)
	if err := s.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
	coinbasePushFrame(t, ctx, s.conn, "user", 7, coinbaseUserEvent("update", map[string]any{"order_id": "foreign"}, map[string]any{"order_id": "venue-order", "client_order_id": "officer-order", "cumulative_quantity": "999", "total_fees": "999"}))
	var e fwtrading.Event
	select {
	case e = <-events:
	case text := <-failures:
		t.Fatalf("unknown channel ended the live session: %s", text)
	case <-sockets:
		t.Fatal("unknown channel opened a new socket")
	case <-time.After(time.Second):
		t.Fatal("next update after unknown channel was not delivered")
	}
	if e.Kind != fwtrading.EventOrder || e.Ref.ClientOrderID != "officer-order" || e.Ref.VenueOrderID != "venue-order" || e.Fill.Quantity != "" {
		t.Fatalf("order update must only request REST lookup: %+v", e)
	}
	select {
	case e := <-events:
		t.Fatalf("snapshot or stream money emitted: %+v", e)
	case text := <-failures:
		t.Fatal(text)
	case <-sockets:
		t.Fatal("unknown channel ended the live session")
	case <-time.After(40 * time.Millisecond):
	}
	c.Close()
	if _, ok := <-events; ok {
		t.Fatal("Close did not close events")
	}
}

func TestCoinbaseStreamSequenceReconnect(t *testing.T) {
	for _, sequence := range []int{0, 2} {
		t.Run(fmt.Sprint(sequence), func(t *testing.T) {
			c, ctx, sockets, failures, _ := coinbaseTestStream(t)
			events, err := c.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s := coinbaseNextSocket(t, ctx, sockets)
			coinbasePushFrame(t, ctx, s.conn, "user", 0, coinbaseUserEvent("snapshot"))
			if e := coinbaseNextEvent(t, events); e.Kind != fwtrading.EventConnected {
				t.Fatal(e)
			}
			coinbasePushFrame(t, ctx, s.conn, "future_channel", sequence, map[string]any{})
			select {
			case message := <-failures:
				if !strings.Contains(message, "sequence") {
					t.Fatal(message)
				}
			case <-ctx.Done():
				t.Fatal("sequence failure missing")
			}
			s = coinbaseNextSocket(t, ctx, sockets)
			coinbasePushFrame(t, ctx, s.conn, "user", 0, coinbaseUserEvent("snapshot"))
			if e := coinbaseNextEvent(t, events); e.Kind != fwtrading.EventConnected {
				t.Fatal(e)
			}
		})
	}
}

func TestCoinbaseStreamDeadlines(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(snapshot), func(t *testing.T) {
			c, ctx, sockets, failures, _ := coinbaseTestStream(t)
			events, err := c.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s := coinbaseNextSocket(t, ctx, sockets)
			started := time.Now()
			if snapshot {
				coinbasePushFrame(t, ctx, s.conn, "user", 0, coinbaseUserEvent("snapshot"))
				_ = coinbaseNextEvent(t, events)
			} else {
				go func() {
					for i := 0; i < 7; i++ {
						raw, _ := json.Marshal(map[string]any{"channel": "heartbeats", "timestamp": time.Now().Format(time.RFC3339Nano), "sequence_num": i, "events": map[string]any{"current_time": "now", "heartbeat_counter": i}})
						if s.conn.Write(ctx, websocket.MessageText, raw) != nil {
							return
						}
						select {
						case <-time.After(time.Second):
						case <-ctx.Done():
							return
						}
					}
				}()
			}
			select {
			case message := <-failures:
				if message == "" {
					t.Fatal("empty timeout status")
				}
			case <-ctx.Done():
				t.Fatal("deadline missing")
			}
			want := coinbaseReadTimeout
			if !snapshot {
				want = coinbaseSubscribeTimeout
			}
			if elapsed := time.Since(started); elapsed < want-200*time.Millisecond || elapsed > want+2*time.Second {
				t.Fatalf("deadline=%s want=%s", elapsed, want)
			}
			if !snapshot {
				select {
				case e := <-events:
					t.Fatalf("timeout authenticated stream: %+v", e)
				default:
				}
			}
		})
	}
}

func TestCoinbaseStreamErrorAndRedaction(t *testing.T) {
	for _, kind := range []string{"string", "object", "escaped-object", "array", "number", "boolean", "null", "absent"} {
		t.Run(kind, func(t *testing.T) {
			c, ctx, sockets, failures, diags := coinbaseTestStream(t)
			events, err := c.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s := coinbaseNextSocket(t, ctx, sockets)
			message := "denied " + coinbaseTestAPIKeyID + " " + s.frames[0]["jwt"].(string)
			frame := map[string]any{"type": "error"}
			want := "denied"
			switch kind {
			case "string":
				frame["message"] = message
			case "object":
				frame["message"] = map[string]string{"reason": "denied", "secret": message}
				want = "message is not a JSON string"
			case "escaped-object":
				escapedKey := strings.ReplaceAll(strings.Replace(coinbaseTestAPIKeyID, "o", `\u006f`, 1), "/", `\/`)
				frame["message"] = json.RawMessage(`{"detail":"key ` + escapedKey + ` rejected"}`)
				want = "message is not a JSON string"
			case "array":
				frame["message"] = []string{message}
				want = "message is not a JSON string"
			case "number":
				frame["message"] = 42
				want = "message is not a JSON string"
			case "boolean":
				frame["message"] = true
				want = "message is not a JSON string"
			case "null":
				frame["message"] = nil
				want = "message is not a JSON string"
			case "absent":
				want = "message is absent"
			}
			raw, _ := json.Marshal(frame)
			if err := s.conn.Write(ctx, websocket.MessageText, raw); err != nil {
				t.Fatal(err)
			}
			select {
			case text := <-failures:
				if !strings.Contains(text, want) || strings.Contains(text, coinbaseTestAPIKeyID) || strings.Contains(text, "eyJ") {
					t.Errorf("want %q; unsafe status: %s", want, text)
				}
				for _, fragment := range []string{"apiKeys", "test", "secret-key-name"} {
					if strings.Contains(text, fragment) {
						t.Errorf("status leaked key fragment %q: %s", fragment, text)
					}
				}
			case <-ctx.Done():
				t.Fatal("refusal status missing")
			}
			select {
			case d := <-diags:
				for _, fragment := range []string{"apiKeys", "test", "secret-key-name"} {
					if strings.Contains(d.Detail, fragment) {
						t.Errorf("diagnostic leaked key fragment %q: %s", fragment, d.Detail)
					}
				}
				raw, _ := json.Marshal(d)
				if !strings.Contains(d.Detail, want) || d.Code != health.CodeInvalidProviderConfig || strings.Contains(string(raw), coinbaseTestAPIKeyID) || strings.Contains(string(raw), "eyJ") {
					t.Errorf("want %q; unsafe refusal diagnostic: %s", want, raw)
				}
			case <-ctx.Done():
				t.Fatal("refusal diagnostic missing")
			}
			select {
			case <-sockets:
				t.Fatal("authorization refusal did not hold reconnect")
			case e := <-events:
				t.Fatalf("refusal connected: %+v", e)
			case <-time.After(2 * coinbaseReconnectMin):
			}
			if kind == "escaped-object" {
				t.Run("returned-error", func(t *testing.T) {
					c, ctx, sockets, _, _ := coinbaseTestStream(t)
					result := make(chan error, 1)
					go func() {
						_, err := c.stream(ctx, make(chan fwtrading.Event))
						result <- err
					}()
					s := coinbaseNextSocket(t, ctx, sockets)
					if err := s.conn.Write(ctx, websocket.MessageText, raw); err != nil {
						t.Fatal(err)
					}
					select {
					case err := <-result:
						if !errors.Is(err, errCoinbaseUnauthorized) {
							t.Fatalf("want authorization refusal, got %v", err)
						}
						for _, fragment := range []string{"apiKeys", "test", "secret-key-name"} {
							if strings.Contains(err.Error(), fragment) {
								t.Errorf("returned error leaked key fragment %q: %v", fragment, err)
							}
						}
						if !strings.Contains(err.Error(), want) {
							t.Errorf("want %q in returned error: %v", want, err)
						}
					case <-ctx.Done():
						t.Fatal("stream error missing")
					}
				})
			}
		})
	}
}

func TestCoinbaseStreamReadErrorRedaction(t *testing.T) {
	c, ctx, sockets, _, _ := coinbaseTestStream(t)
	result := make(chan error, 1)
	go func() {
		_, err := c.stream(ctx, make(chan fwtrading.Event))
		result <- err
	}()
	s := coinbaseNextSocket(t, ctx, sockets)
	_ = s.conn.Close(websocket.StatusPolicyViolation, coinbaseTestAPIKeyID)
	select {
	case err := <-result:
		if err == nil || strings.Contains(err.Error(), coinbaseTestAPIKeyID) {
			t.Fatalf("unsafe stream read error: %v", err)
		}
		if errors.Unwrap(err) != nil {
			t.Fatal("stream read error retained an unscrubbed cause")
		}
	case <-ctx.Done():
		t.Fatal("stream read error missing")
	}
}

func TestCoinbaseStreamMalformedHeartbeatKeepsConnection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timestamp any
		events    any
		detail    string
	}{
		{"timestamp-type", 42, map[string]any{}, coinbaseHeartbeatTimestampUnreadable},
		{"timestamp-format", "bad", map[string]any{}, coinbaseHeartbeatTimestampInvalid},
		{"events-type", "2026-11-12T10:00:00Z", "bad", coinbaseHeartbeatUnreadable},
		{"events-empty", "2026-11-12T10:00:00Z", []any{}, coinbaseHeartbeatEmpty},
		{"events-null", "2026-11-12T10:00:00Z", nil, coinbaseHeartbeatEmpty},
		{"event-type", "2026-11-12T10:00:00Z", []any{42}, coinbaseHeartbeatEventUnreadable},
		{"event-fields", "2026-11-12T10:00:00Z", map[string]any{"current_time": "now"}, coinbaseHeartbeatEventUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ctx, sockets, failures, diags := coinbaseTestStream(t)
			events, err := c.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s := coinbaseNextSocket(t, ctx, sockets)
			coinbasePushFrame(t, ctx, s.conn, "user", 0, coinbaseUserEvent("snapshot"))
			if e := coinbaseNextEvent(t, events); e.Kind != fwtrading.EventConnected {
				t.Fatalf("snapshot did not connect: %+v", e)
			}
			for sequence := 1; sequence <= 2; sequence++ {
				raw, err := json.Marshal(map[string]any{
					"channel": "heartbeats", "sequence_num": sequence,
					"timestamp": tc.timestamp, "events": tc.events,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.conn.Write(ctx, websocket.MessageText, raw); err != nil {
					t.Fatal(err)
				}
				if sequence == 1 {
					select {
					case d := <-diags:
						if d.Code != health.CodeUnparsableData || d.Level != health.DiagWarn || d.Detail != tc.detail || d.Remediation != venueStreamSkipped {
							t.Fatalf("want skipped heartbeat warning, got %+v", d)
						}
					case <-ctx.Done():
						t.Fatal("heartbeat diagnostic missing")
					}
				}
			}
			coinbasePushFrame(t, ctx, s.conn, "user", 3, coinbaseUserEvent("update",
				map[string]any{"order_id": "venue-order", "client_order_id": "officer-order"}))
			select {
			case e := <-events:
				if e.Kind != fwtrading.EventOrder || e.Ref.VenueOrderID != "venue-order" || e.Ref.ClientOrderID != "officer-order" {
					t.Fatalf("update after heartbeat: %+v", e)
				}
			case failure := <-failures:
				t.Fatalf("malformed heartbeat ended session: %s", failure)
			case <-ctx.Done():
				t.Fatal("update after heartbeat missing")
			}
			select {
			case d := <-diags:
				t.Fatalf("repeated heartbeat warning: %+v", d)
			case <-sockets:
				t.Fatal("malformed heartbeat reconnected")
			default:
			}
		})
	}
}

func TestCoinbaseStreamHeartbeatTimestampDoesNotSilenceUserError(t *testing.T) {
	for _, tc := range []struct {
		name            string
		timestamp       any
		heartbeatDetail string
		userDetail      string
	}{
		{"timestamp-type", 42, "A heartbeat message had no readable timestamp.", coinbaseTimestampUnreadable},
		{"timestamp-format", "bad", "A heartbeat message had an invalid timestamp.", coinbaseTimestampInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ctx, sockets, failures, diags := coinbaseTestStream(t)
			events, err := c.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s := coinbaseNextSocket(t, ctx, sockets)
			coinbasePushFrame(t, ctx, s.conn, "user", 0, coinbaseUserEvent("snapshot"))
			if e := coinbaseNextEvent(t, events); e.Kind != fwtrading.EventConnected {
				t.Fatalf("snapshot did not connect: %+v", e)
			}
			for i, frame := range []struct {
				channel string
				level   string
				detail  string
				remedy  string
			}{
				{"heartbeats", health.DiagWarn, tc.heartbeatDetail, venueStreamSkipped},
				{"user", health.DiagError, tc.userDetail, venueStreamReconciled},
			} {
				raw, err := json.Marshal(map[string]any{
					"channel": frame.channel, "sequence_num": i + 1,
					"timestamp": tc.timestamp, "events": []any{},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.conn.Write(ctx, websocket.MessageText, raw); err != nil {
					t.Fatal(err)
				}
				select {
				case d := <-diags:
					if d.Code != health.CodeUnparsableData || d.Level != frame.level || d.Detail != frame.detail || d.Remediation != frame.remedy {
						t.Errorf("%s timestamp diagnostic: %+v", frame.channel, d)
					}
				case <-ctx.Done():
					t.Fatal("timestamp diagnostic missing")
				case <-time.After(time.Second):
					t.Errorf("%s timestamp diagnostic missing", frame.channel)
				}
			}
			select {
			case message := <-failures:
				if !strings.Contains(message, tc.userDetail) {
					t.Fatalf("unexpected session failure: %s", message)
				}
			case <-ctx.Done():
				t.Fatal("bad user timestamp did not end session")
			}
		})
	}
}

func TestCoinbaseStreamUnparsable(t *testing.T) {
	for _, tc := range []struct {
		name, raw, detail string
	}{
		{"bad-frame", `{`, coinbaseFrameUnreadable},
		{"bad-user-events", `{"channel":"user","sequence_num":1,"timestamp":"2026-11-12T10:00:00Z","events":"bad"}`, coinbaseUserEventsUnreadable},
		{"empty-user-events", `{"channel":"user","sequence_num":1,"timestamp":"2026-11-12T10:00:00Z","events":[]}`, coinbaseUserEventsUnreadable},
		{"timestamp-type", `{"channel":"user","sequence_num":1,"timestamp":42,"events":[{"type":"snapshot","orders":[]}]}`, coinbaseTimestampUnreadable},
		{"bad-timestamp", `{"channel":"user","sequence_num":1,"timestamp":"bad","events":[{"type":"snapshot","orders":[]}]}`, coinbaseTimestampInvalid},
		{"bad-sequence", `{"channel":"user","sequence_num":"bad","timestamp":"2026-11-12T10:00:00Z","events":[{"type":"snapshot","orders":[]}]}`, coinbaseFrameUnreadable},
		{"negative-sequence", `{"channel":"user","sequence_num":-1,"timestamp":"2026-11-12T10:00:00Z","events":[]}`, coinbaseSequenceUnreadable},
		{"missing-user-sequence", `{"channel":"user","timestamp":"2026-11-12T10:00:00Z","events":[{"type":"snapshot","orders":[]}]}`, coinbaseSequenceUnreadable},
		{"missing-heartbeats-sequence", `{"channel":"heartbeats","timestamp":"2026-11-12T10:00:00Z","events":[{"current_time":"2026-11-12T10:00:00Z","heartbeat_counter":"1"}]}`, coinbaseSequenceUnreadable},
		{"missing-orders", `{"channel":"user","sequence_num":1,"timestamp":"2026-11-12T10:00:00Z","events":[{"type":"update"}]}`, coinbaseOrdersAbsent},
		{"missing-order-id", `{"channel":"user","sequence_num":1,"timestamp":"2026-11-12T10:00:00Z","events":[{"type":"update","orders":[{"client_order_id":"officer-order"}]}]}`, coinbaseOrderIDAbsent},
		{"unknown-event", `{"channel":"user","sequence_num":1,"timestamp":"2026-11-12T10:00:00Z","events":[{"type":"future","orders":[]}]}`, coinbaseUserEventUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ctx, sockets, failures, diags := coinbaseTestStream(t)
			events, err := c.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s := coinbaseNextSocket(t, ctx, sockets)
			coinbasePushFrame(t, ctx, s.conn, "user", 0, coinbaseUserEvent("snapshot"))
			if e := coinbaseNextEvent(t, events); e.Kind != fwtrading.EventConnected {
				t.Fatalf("snapshot did not connect: %+v", e)
			}
			if err := s.conn.Write(ctx, websocket.MessageText, []byte(tc.raw)); err != nil {
				t.Fatal(err)
			}
			select {
			case d := <-diags:
				if d.Code != health.CodeUnparsableData || d.Level != health.DiagError || d.Detail != tc.detail || d.Remediation != venueStreamReconciled {
					t.Fatal(d)
				}
			case <-ctx.Done():
				t.Fatal("unparsable diagnostic missing")
			case <-time.After(time.Second):
				t.Fatal("unparsable diagnostic missing")
			}
			select {
			case <-failures:
			case <-ctx.Done():
				t.Fatal("bad frame did not end session")
			}
		})
	}
}

func TestCoinbaseStreamRedirectRefused(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	failures := make(chan string, 4)
	c.SetStatusReporter(func(ok bool, message string) {
		if !ok {
			failures <- message
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-failures:
	case <-ctx.Done():
		t.Fatal("redirect refusal missing")
	}
	if targetCalls.Load() != 0 {
		t.Fatal("stream followed a redirect")
	}
}
