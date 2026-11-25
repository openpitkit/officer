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

package openalgo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
)

type memoryStreamWriter struct {
	ctx     context.Context
	typ     websocket.MessageType
	payload []byte
	err     error
	calls   int
}

func TestSubscribeQuotesFrame(t *testing.T) {
	w := &memoryStreamWriter{}
	instruments := []Instrument{{Exchange: "NSE", Symbol: "RELIANCE"}, {Exchange: "BSE", Symbol: "INFY"}}
	if err := WriteSubscribeQuotes(context.Background(), w, "quotes-1", instruments); err != nil {
		t.Fatal("could not write quote request")
	}
	var got struct {
		Action, Mode string
		RequestID    string `json:"request_id"`
		Symbols      []Instrument
	}
	if json.Unmarshal(w.payload, &got) != nil || w.typ != websocket.MessageText ||
		got.Action != "subscribe" || got.Mode != "Quote" || got.RequestID != "quotes-1" ||
		len(got.Symbols) != 2 || got.Symbols[0] != instruments[0] || got.Symbols[1] != instruments[1] {
		t.Fatal("quote subscription wire shape changed")
	}
	for _, id := range []string{"", " "} {
		if WriteSubscribeQuotes(context.Background(), w, id, instruments) == nil {
			t.Fatal("empty request id accepted")
		}
	}
	if WriteSubscribeQuotes(context.Background(), w, "r", nil) == nil {
		t.Fatal("empty subscription accepted")
	}
	if WriteSubscribeQuotes(context.Background(), nil, "r", instruments) == nil {
		t.Fatal("nil writer accepted")
	}
	w.err = errors.New("write refused")
	if !errors.Is(WriteSubscribeQuotes(context.Background(), w, "r", instruments), w.err) {
		t.Fatal("write failure lost its cause")
	}
}

func TestParseSubscribeQuotesReply(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		valid         bool
		refusals      int
		streamError   bool
	}{
		{"success", `{"type":"subscribe","status":"success","request_id":"r","subscriptions":[{"symbol":"A","exchange":"NSE","status":"success","mode":"Quote"}]}`, true, 0, false},
		{"partial", `{"type":"subscribe","status":"partial","request_id":"r","subscriptions":[{"symbol":"A","exchange":"NSE","status":"success"},{"symbol":"B","exchange":"BSE","status":"error","message":"test-key-marker"}]}`, true, 1, false},
		{"all-refused", `{"type":"subscribe","status":"partial","request_id":"r","subscriptions":[{"symbol":"A","exchange":"NSE","status":"error"}]}`, true, 1, false},
		{"error", `{"status":"error","code":"test-key-marker","message":"test-key-marker","request_id":"r"}`, false, 0, true},
		{"error-without-id", `{"status":"error","code":"test-key-marker","message":"test-key-marker"}`, false, 0, true},
		{"wrong-id", `{"type":"subscribe","status":"success","request_id":"other"}`, false, 0, false},
		{"wrong-error-id", `{"status":"error","code":"BROKER_ERROR","request_id":"other"}`, false, 0, false},
		{"missing-id", `{"type":"subscribe","status":"success"}`, false, 0, false},
		{"wrong-type", `{"type":"auth","status":"success","request_id":"r"}`, false, 0, false},
		{"wrong-status", `{"type":"subscribe","status":"pending","request_id":"r"}`, false, 0, false},
		{"partial-without-rows", `{"type":"subscribe","status":"partial","request_id":"r"}`, false, 0, false},
		{"partial-without-refusal", `{"type":"subscribe","status":"partial","request_id":"r","subscriptions":[{"exchange":"NSE","symbol":"A","status":"success"}]}`, false, 0, false},
		{"success-with-refusal", `{"type":"subscribe","status":"success","request_id":"r","subscriptions":[{"exchange":"NSE","symbol":"A","status":"error"}]}`, false, 0, false},
		{"missing-instrument", `{"type":"subscribe","status":"partial","request_id":"r","subscriptions":[{"symbol":"A","status":"error"}]}`, false, 0, false},
		{"missing-symbol", `{"type":"subscribe","status":"partial","request_id":"r","subscriptions":[{"exchange":"NSE","status":"error"}]}`, false, 0, false},
		{"invalid-row-status", `{"type":"subscribe","status":"partial","request_id":"r","subscriptions":[{"exchange":"NSE","symbol":"A","status":"pending"}]}`, false, 0, false},
		{"duplicate-row", `{"type":"subscribe","status":"partial","request_id":"r","subscriptions":[{"exchange":"NSE","symbol":"A","status":"error"},{"exchange":"NSE","symbol":"A","status":"error"}]}`, false, 0, false},
		{"missing-row", `{"type":"subscribe","status":"success","request_id":"r","subscriptions":[]}`, false, 0, false},
		{"extra-row", `{"type":"subscribe","status":"success","request_id":"r","subscriptions":[{"exchange":"NSE","symbol":"A","status":"success"},{"exchange":"BSE","symbol":"B","status":"success"}]}`, false, 0, false},
		{"unrequested-row", `{"type":"subscribe","status":"success","request_id":"r","subscriptions":[{"exchange":"NSE","symbol":"B","status":"success"}]}`, false, 0, false},
		{"duplicate-success", `{"type":"subscribe","status":"success","request_id":"r","subscriptions":[{"exchange":"NSE","symbol":"A","status":"success"},{"exchange":"NSE","symbol":"A","status":"success"}]}`, false, 0, false},
		{"malformed", `{"test-key-marker"`, false, 0, false},
		{"array", `[]`, false, 0, false},
		{"null", `null`, false, 0, false},
		{"invalid-field", `{"request_id":123}`, false, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requested := []Instrument{{Exchange: "NSE", Symbol: "A"}}
			if tc.name == "partial" || tc.name == "duplicate-success" {
				requested = append(requested, Instrument{Exchange: "BSE", Symbol: "B"})
			}
			refused, err := ParseSubscribeQuotesReply([]byte(tc.payload), "r", requested, testRedactor(t))
			if (err == nil) != tc.valid || len(refused) != tc.refusals {
				t.Fatal("quote acknowledgement acceptance changed")
			}
			var streamErr *StreamError
			if errors.As(err, &streamErr) != tc.streamError {
				t.Fatal("quote refusal error type changed")
			}
			if streamErr != nil {
				assertNoKey(t, streamErr.Error())
				if streamErr.Op != "subscription" || streamErr.Code != "[redacted]" || streamErr.Message != "[redacted]" {
					t.Fatal("quote error lost operation or redaction")
				}
			}
			if err != nil {
				assertNoKey(t, err.Error())
			}
			if tc.name == "partial" {
				if refused[0].Exchange != "BSE" || refused[0].Symbol != "B" || refused[0].Message != "[redacted]" {
					t.Fatal("quote refusal lost its instrument or redaction")
				}
			}
		})
	}
	if _, err := ParseSubscribeQuotesReply([]byte(`{}`), "r", nil, nil); err == nil {
		t.Fatal("nil redactor accepted")
	}
	if _, err := ParseSubscribeQuotesReply([]byte(`{"type":"subscribe","status":"success","request_id":""}`), "", nil, testRedactor(t)); err == nil {
		t.Fatal("empty expected request id accepted")
	}
}

func TestParseSubscribeQuotesErrorWithoutID(t *testing.T) {
	for _, code := range []string{"SERVER_ERROR", "PROCESSING_ERROR"} {
		for _, id := range []string{"", "r", "other"} {
			t.Run(code+"/"+id, func(t *testing.T) {
				payload, err := json.Marshal(map[string]string{
					"status": "error", "code": code,
					"message": "test-key-marker", "request_id": id,
				})
				if err != nil {
					t.Fatal("could not encode error fixture")
				}
				_, err = ParseSubscribeQuotesReply(payload, "r", []Instrument{{Exchange: "NSE", Symbol: "A"}}, testRedactor(t))
				var streamErr *StreamError
				if id == "other" {
					if errors.As(err, &streamErr) || err == nil || err.Error() != "openalgo: unexpected quote subscription reply" {
						t.Fatal("unrelated error reply was accepted")
					}
				} else if !errors.As(err, &streamErr) || streamErr.Code != code || streamErr.Message != "[redacted]" {
					t.Fatal("outstanding request error lost its type or redaction")
				}
			})
		}
	}
}

func TestParseMarketData(t *testing.T) {
	for _, tc := range []struct {
		name, data, mark string
	}{
		{"exact-number", `{"ltp":1187.750000000000000001,"timestamp":1712572800123}`, "1187.750000000000000001"},
		{"exact-string", `{"ltp":"1187.7500","timestamp":1712572800123}`, "1187.7500"},
		{"tiny", `{"ltp":"0.00000000000000000001","timestamp":1712572800123}`, "0.00000000000000000001"},
		{"higher-mode", `{"ltp":1187.75,"timestamp":1712572800123,"depth":{"buy":[]},"volume":50,"open":1100}`, "1187.75"},
		{"missing-ltp", `{"timestamp":1712572800123}`, ""},
		{"zero-ltp", `{"ltp":0,"timestamp":1712572800123}`, ""},
		{"zero-string-ltp", `{"ltp":"00.000","timestamp":1712572800123}`, ""},
		{"negative-ltp", `{"ltp":-1,"timestamp":1712572800123}`, ""},
		{"plus-zero", `{"ltp":"+0","timestamp":1712572800123}`, ""},
		{"plus-zero-decimal", `{"ltp":"+0.00","timestamp":1712572800123}`, ""},
		{"plus-positive", `{"ltp":"+1.5","timestamp":1712572800123}`, "+1.5"},
		{"null-ltp", `{"ltp":null,"timestamp":1712572800123}`, ""},
		{"boolean-ltp", `{"ltp":true,"timestamp":1712572800123}`, ""},
		{"invalid-ltp", `{"ltp":"test-key-marker","timestamp":1712572800123}`, ""},
		{"exponent-ltp", `{"ltp":1e3,"timestamp":1712572800123}`, ""},
		{"missing-timestamp", `{"ltp":1}`, ""},
		{"null-timestamp", `{"ltp":1,"timestamp":null}`, ""},
		{"text-timestamp", `{"ltp":1,"timestamp":"1712572800123"}`, ""},
		{"boolean-timestamp", `{"ltp":1,"timestamp":true}`, ""},
		{"fractional-timestamp", `{"ltp":1,"timestamp":1712572800123.5}`, ""},
		{"overflow-timestamp", `{"ltp":1,"timestamp":9223372036854775808}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"type":"market_data","symbol":"RELIANCE","exchange":"NSE","mode":2,"data":` + tc.data + `}`
			got, err := ParseMarketData([]byte(payload), testRedactor(t))
			if tc.mark == "" {
				if err == nil || !strings.HasPrefix(err.Error(), "openalgo: unreadable market data: ") ||
					got.Instrument != (Instrument{Exchange: "NSE", Symbol: "RELIANCE"}) {
					t.Fatal("invalid measurement accepted or error changed")
				}
				return
			}
			if err != nil || string(got.LTP) != tc.mark || got.Symbol != "RELIANCE" || got.Exchange != "NSE" ||
				!got.Timestamp.Equal(time.UnixMilli(1712572800123)) {
				t.Fatal("market data lost exact price, source time or instrument")
			}
		})
	}
	for _, payload := range []string{
		`{"type":"market_data","exchange":"NSE","data":{"ltp":1,"timestamp":1}}`,
		`{"type":"market_data","symbol":"A","data":{"ltp":1,"timestamp":1}}`,
		`{"type":"market_data","exchange":" ","symbol":"A","data":{"ltp":1,"timestamp":1}}`,
		`{"type":"market_data","exchange":"NSE","symbol":" ","data":{"ltp":1,"timestamp":1}}`,
		`{"test-key-marker"`, `[]`,
	} {
		if _, err := ParseMarketData([]byte(payload), testRedactor(t)); err == nil || err.Error() != "openalgo: unreadable market data" {
			t.Fatal("unreadable market frame accepted or error changed")
		}
	}
	if _, err := ParseMarketData([]byte(`{"type":"other"}`), testRedactor(t)); !errors.Is(err, ErrUnknownPush) {
		t.Fatal("unknown push classification changed")
	}
	for _, tc := range []struct {
		data, reason string
	}{
		{`{}`, "missing ltp"},
		{`{"ltp":0}`, "non-positive ltp"},
		{`{"ltp":1}`, "missing timestamp"},
	} {
		got, err := ParseMarketData([]byte(`{"type":"market_data","exchange":"NSE","symbol":"A","data":`+tc.data+`}`), testRedactor(t))
		if err == nil || err.Error() != "openalgo: unreadable market data: "+tc.reason ||
			got.Instrument != (Instrument{Exchange: "NSE", Symbol: "A"}) {
			t.Fatal("measurement error lost its instrument or reason")
		}
	}
	if _, err := ParseMarketData([]byte(`{"type":"pong","status":"success"}`), testRedactor(t)); !errors.Is(err, ErrStreamPong) {
		t.Fatal("successful pong was not recognized")
	}
	if _, err := ParseMarketData([]byte(`{"type":"pong"}`), testRedactor(t)); !errors.Is(err, ErrUnknownPush) {
		t.Fatal("incomplete pong accepted as liveness")
	}
	_, err := ParseMarketData([]byte(`{"status":"error","code":"test-key-marker","message":"test-key-marker"}`), testRedactor(t))
	var streamErr *StreamError
	if !errors.As(err, &streamErr) || streamErr.Op != "subscription" || streamErr.Code != "[redacted]" || streamErr.Message != "[redacted]" {
		t.Fatal("market data error type or redaction changed")
	}
	if _, err := ParseMarketData([]byte(`{}`), nil); err == nil {
		t.Fatal("nil market data redactor accepted")
	}
	got, err := ParseMarketData([]byte(`{"type":"market_data","symbol":"A","exchange":"NSE","data":{"ltp":1,"timestamp":0}}`), testRedactor(t))
	if err != nil || !got.Timestamp.Equal(time.UnixMilli(0)) {
		t.Fatal("explicit epoch timestamp changed")
	}
}

func FuzzParseMarketData(f *testing.F) {
	for _, seed := range []string{
		`{"type":"market_data","symbol":"A","exchange":"NSE","data":{"ltp":"0.0001","timestamp":1712572800123}}`,
		`{"type":"market_data","symbol":"A","exchange":"BSE","data":{"ltp":0,"timestamp":null}}`,
		`{"type":"market_data","symbol":"A","exchange":"NSE","data":{"ltp":1,"timestamp":9223372036854775808}}`,
		`{"status":"error","code":"test-key-marker","message":"test-key-marker"}`, `null`,
		`{"type":"market_data","symbol":"A","exchange":"NSE","data":{"ltp":"+0","timestamp":1}}`,
		`{"type":"market_data","symbol":"A","exchange":"NSE","data":{"ltp":"+0.00","timestamp":1}}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, payload string) {
		quote, err := ParseMarketData([]byte(payload), strings.NewReplacer("test-key-marker", "[redacted]"))
		if err != nil {
			if strings.Contains(err.Error(), "test-key-marker") {
				t.Fatal("parser error exposed a credential")
			}
			return
		}
		price, parseErr := decimal.NewFromString(string(quote.LTP))
		if quote.Symbol == "" || quote.Exchange == "" || parseErr != nil || price.Cmp(decimal.Zero) <= 0 {
			t.Fatal("accepted market data violates measurement invariants")
		}
		var wire struct {
			Data struct {
				LTP       json.RawMessage `json:"ltp"`
				Timestamp *int64          `json:"timestamp"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(payload), &wire) != nil || wire.Data.Timestamp == nil ||
			quote.Timestamp.UnixMilli() != *wire.Data.Timestamp {
			t.Fatal("accepted quote changed its source price or timestamp")
		}
		source := string(wire.Data.LTP)
		if strings.HasPrefix(source, `"`) && json.Unmarshal(wire.Data.LTP, &source) != nil {
			t.Fatal("accepted invalid price string")
		}
		if string(quote.LTP) != source {
			t.Fatal("accepted quote changed the source price text")
		}
	})
}

func (w *memoryStreamWriter) Write(ctx context.Context, typ websocket.MessageType, payload []byte) error {
	w.ctx, w.typ, w.payload = ctx, typ, append([]byte(nil), payload...)
	w.calls++
	return w.err
}

func TestStreamAuthFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &memoryStreamWriter{}
	if err := WriteStreamAuth(ctx, writer, testKey); err != nil {
		t.Fatal("valid auth frame refused")
	}
	if writer.calls != 1 || writer.ctx != ctx || writer.typ != websocket.MessageText || string(writer.payload) != `{"action":"authenticate","api_key":"test-key-marker"}` {
		t.Fatal("auth frame bytes, context or message type changed")
	}
	writer = &memoryStreamWriter{}
	if err := WriteStreamFrame(ctx, writer, struct {
		Value string `json:"value"`
	}{"text"}); err != nil || string(writer.payload) != `{"value":"text"}` {
		t.Fatal("generic JSON frame changed")
	}
	for _, key := range []string{"", " \t\n "} {
		writer := &memoryStreamWriter{}
		if err := WriteStreamAuth(ctx, writer, key); err == nil || writer.calls != 0 {
			t.Fatal("blank key was sent")
		}
	}
	if err := WriteStreamFrame(ctx, nil, struct{}{}); err == nil {
		t.Fatal("nil writer accepted")
	}
	writer = &memoryStreamWriter{}
	var encodeErr *json.UnsupportedTypeError
	if err := WriteStreamFrame(ctx, writer, make(chan int)); !errors.As(err, &encodeErr) || writer.calls != 0 ||
		err.Error() != "openalgo: encode stream frame: "+encodeErr.Error() {
		t.Fatal("frame encoding cause hidden or frame sent")
	}
	cause := errors.New("writer echoed " + testKey)
	writer = &memoryStreamWriter{err: cause}
	err := WriteStreamAuth(ctx, writer, testKey)
	if err == nil || !errors.Is(err, cause) ||
		err.Error() != "openalgo: write stream frame: "+cause.Error() {
		t.Fatal("writer cause lost or hidden")
	}
}

func TestParseStreamAuthReply(t *testing.T) {
	for _, tc := range []struct {
		name, payload, code, message string
		valid, refused               bool
	}{
		{"success", `{"type":"auth","status":"success","message":"Authentication successful","broker":"zerodha"}`, "", "", true, false},
		{"auth_failure", `{"status":"error","code":"AUTHENTICATION_ERROR","message":"bad test-key-marker"}`, "AUTHENTICATION_ERROR", "bad [redacted]", false, true},
		{"broker_failure", `{"status":"error","code":"BROKER_ERROR","message":"session lost"}`, "BROKER_ERROR", "session lost", false, true},
		{"code_redaction", `{"status":"error","code":"test-key-marker","message":"test-key-marker"}`, "[redacted]", "[redacted]", false, true},
		{"error_without_message", `{"status":"error"}`, "", "", false, true},
		{"no_type", `{"status":"success","message":"test-key-marker"}`, "", "", false, false},
		{"other_type", `{"type":"market_data","status":"success","message":"test-key-marker"}`, "", "", false, false},
		{"no_status", `{"type":"auth","message":"test-key-marker"}`, "", "", false, false},
		{"unknown_status", `{"type":"auth","status":"pending","message":"test-key-marker"}`, "", "", false, false},
		{"malformed", `{"test-key-marker"`, "", "", false, false},
		{"wrong_shape", `[{"message":"test-key-marker"}]`, "", "", false, false},
		{"wrong_type", `{"type":"auth","status":123,"message":"test-key-marker"}`, "", "", false, false},
		{"null", `null`, "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ParseStreamAuthReply([]byte(tc.payload), testRedactor(t))
			if (err == nil) != tc.valid {
				t.Fatal("stream auth reply acceptance changed")
			}
			if err == nil {
				return
			}
			assertNoKey(t, err.Error())
			var streamErr *StreamError
			if errors.As(err, &streamErr) != tc.refused {
				t.Fatal("stream refusal error type lost")
			}
			if tc.refused && (streamErr.Op != "authentication" || streamErr.Code != tc.code || streamErr.Message != tc.message) {
				t.Fatal("stream venue text changed or not redacted")
			}
		})
	}
	if err := ParseStreamAuthReply([]byte(`{"type":"auth","status":"success"}`), nil); err == nil {
		t.Fatal("nil redactor accepted")
	}
}

func TestSubscribeOrdersFrame(t *testing.T) {
	ctx := context.Background()
	w := &memoryStreamWriter{}
	if err := WriteSubscribeOrders(ctx, w, "request-1"); err != nil ||
		w.ctx != ctx || w.calls != 1 || w.typ != websocket.MessageText ||
		string(w.payload) != `{"action":"subscribe_orders","request_id":"request-1"}` {
		t.Fatal("subscription frame bytes, context or message type changed")
	}
	for _, id := range []string{"", " \t\n "} {
		w := &memoryStreamWriter{}
		if WriteSubscribeOrders(ctx, w, id) == nil || w.calls != 0 {
			t.Fatal("blank subscription request id was sent")
		}
	}
	if WriteSubscribeOrders(ctx, nil, "request-1") == nil {
		t.Fatal("subscription accepted a nil writer")
	}
	cause := errors.New("test writer failure")
	if !errors.Is(WriteSubscribeOrders(ctx, &memoryStreamWriter{err: cause}, "request-1"), cause) {
		t.Fatal("subscription lost its writer error")
	}
}

func TestParseSubscribeOrdersReply(t *testing.T) {
	for _, tc := range []struct {
		name, payload  string
		valid, refused bool
	}{
		{"success", `{"type":"subscribe_orders","status":"success","request_id":"r","extra":123}`, true, false},
		{"refused", `{"status":"error","code":"test-key-marker","message":"test-key-marker","request_id":"r"}`, false, true},
		{"missing-error-fields", `{"status":"error"}`, false, true},
		{"wrong-id", `{"type":"subscribe_orders","status":"success","request_id":"other"}`, false, false},
		{"missing-id", `{"type":"subscribe_orders","status":"success"}`, false, false},
		{"wrong-type", `{"type":"auth","status":"success","request_id":"r"}`, false, false},
		{"missing-type", `{"status":"success","request_id":"r"}`, false, false},
		{"wrong-status", `{"type":"subscribe_orders","status":"pending","request_id":"r"}`, false, false},
		{"missing-status", `{"type":"subscribe_orders","request_id":"r"}`, false, false},
		{"malformed", `{"test-key-marker"`, false, false},
		{"array", `[]`, false, false},
		{"invalid-field", `{"request_id":123}`, false, false},
		{"null", `null`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ParseSubscribeOrdersReply([]byte(tc.payload), "r", testRedactor(t))
			if (err == nil) != tc.valid {
				t.Fatal("subscription reply acceptance changed")
			}
			if err == nil {
				return
			}
			assertNoKey(t, err.Error())
			var streamErr *StreamError
			if errors.As(err, &streamErr) != tc.refused {
				t.Fatal("subscription refusal type lost")
			}
			if tc.name == "refused" && (streamErr.Op != "subscription" ||
				streamErr.Code != "[redacted]" || streamErr.Message != "[redacted]") {
				t.Fatal("subscription refusal lost its operation or redaction")
			}
		})
	}
	if ParseSubscribeOrdersReply([]byte(`{}`), "r", nil) == nil {
		t.Fatal("subscription accepted a nil redactor")
	}
}

func TestParseOrderUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, payload           string
		want                    OrderUpdate
		valid, unknown, refused bool
	}{
		{"absent-filled", `{"type":"order_update","mode":"analyze","orderid":"123","order_status":"open","ignored":true}`, OrderUpdate{"analyze", "123", "open", ""}, true, false, false},
		{"zero-filled", `{"type":"order_update","mode":"live","orderid":123,"order_status":"cancelled","filled_quantity":0}`, OrderUpdate{"live", "123", "cancelled", "0"}, true, false, false},
		{"decimal", `{"type":"order_update","mode":"analyze","orderid":"123","order_status":"complete","filled_quantity":1.25}`, OrderUpdate{"analyze", "123", "complete", "1.25"}, true, false, false},
		{"decimal-string", `{"type":"order_update","mode":"analyze","orderid":"123","order_status":"complete","filled_quantity":"1.250"}`, OrderUpdate{"analyze", "123", "complete", "1.250"}, true, false, false},
		{"null-filled", `{"type":"order_update","mode":"analyze","orderid":"123","order_status":"open","filled_quantity":null}`, OrderUpdate{"analyze", "123", "open", ""}, true, false, false},
		{"unknown-type", `{"type":"heartbeat","filled_quantity":true}`, OrderUpdate{}, false, true, false},
		{"absent-type", `{}`, OrderUpdate{}, false, true, false},
		{"refused", `{"status":"error","code":"test-key-marker","message":"test-key-marker"}`, OrderUpdate{}, false, false, true},
		{"error-precedence", `{"type":"order_update","status":"error","code":"test-key-marker","message":"test-key-marker"}`, OrderUpdate{}, false, false, true},
		{"exponent", `{"type":"order_update","mode":"analyze","orderid":"123","order_status":"open","filled_quantity":1e2}`, OrderUpdate{}, false, false, false},
		{"exponent-string", `{"type":"order_update","mode":"analyze","orderid":"123","order_status":"open","filled_quantity":"1e2"}`, OrderUpdate{}, false, false, false},
		{"invalid-filled", `{"type":"order_update","mode":"analyze","orderid":"123","order_status":"open","filled_quantity":true}`, OrderUpdate{}, false, false, false},
		{"blank-id", `{"type":"order_update","mode":"analyze","orderid":"  ","order_status":"open"}`, OrderUpdate{}, false, false, false},
		{"missing-id", `{"type":"order_update","mode":"analyze","order_status":"open"}`, OrderUpdate{}, false, false, false},
		{"null-id", `{"type":"order_update","mode":"analyze","orderid":null,"order_status":"open"}`, OrderUpdate{}, false, false, false},
		{"fractional-id", `{"type":"order_update","mode":"analyze","orderid":1.2,"order_status":"open"}`, OrderUpdate{}, false, false, false},
		{"unknown-mode", `{"type":"order_update","mode":"test-key-marker","orderid":"123","order_status":"open"}`, OrderUpdate{}, false, false, false},
		{"missing-mode", `{"type":"order_update","orderid":"123","order_status":"open"}`, OrderUpdate{}, false, false, false},
		{"blank-status", `{"type":"order_update","mode":"analyze","orderid":"123","order_status":" \t"}`, OrderUpdate{}, false, false, false},
		{"missing-status", `{"type":"order_update","mode":"analyze","orderid":"123"}`, OrderUpdate{}, false, false, false},
		{"malformed", `{"test-key-marker"`, OrderUpdate{}, false, false, false},
		{"array", `[]`, OrderUpdate{}, false, false, false},
		{"invalid-type", `{"type":1}`, OrderUpdate{}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			update, err := ParseOrderUpdate([]byte(tc.payload), testRedactor(t))
			if (err == nil) != tc.valid || update != tc.want ||
				errors.Is(err, ErrUnknownPush) != tc.unknown {
				t.Fatal("order push acceptance or values changed")
			}
			if err == nil {
				return
			}
			assertNoKey(t, err.Error())
			var streamErr *StreamError
			if errors.As(err, &streamErr) != tc.refused {
				t.Fatal("push refusal type lost")
			}
			if tc.refused && (streamErr.Op != "subscription" ||
				streamErr.Code != "[redacted]" || streamErr.Message != "[redacted]") {
				t.Fatal("push refusal operation or redaction lost")
			}
			if !tc.refused && !tc.unknown && err.Error() != "openalgo: unreadable order push" {
				t.Fatal("unreadable push exposed payload values")
			}
		})
	}
	if _, err := ParseOrderUpdate([]byte(`{}`), nil); err == nil {
		t.Fatal("order push accepted a nil redactor")
	}
}

func TestStreamErrorText(t *testing.T) {
	for _, op := range []string{"authentication", "subscription"} {
		err := &StreamError{Op: op, Code: "REFUSED", Message: "reason"}
		if err.Error() != "openalgo: stream "+op+" failed (REFUSED): reason" {
			t.Fatal("stream error text lost operation, code or message")
		}
	}
}
