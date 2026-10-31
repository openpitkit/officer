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

package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type recordingWriter struct {
	writes  int
	typ     websocket.MessageType
	payload []byte
	ctx     context.Context
	err     error
}

func (w *recordingWriter) Write(ctx context.Context,
	typ websocket.MessageType, payload []byte,
) error {
	w.writes++
	w.ctx, w.typ, w.payload = ctx, typ, append([]byte(nil), payload...)
	return w.err
}

func TestWriteSubscribe(t *testing.T) {
	credentials := Credentials{
		APIKey: "test+key/marker", APISecret: testCredentials.APISecret,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var conn recordingWriter
	err := WriteSubscribe(ctx, &conn, credentials, "subscription-1",
		time.UnixMilli(1499827319559))
	if err != nil {
		t.Fatal(err)
	}
	if conn.writes != 1 || conn.typ != websocket.MessageText || conn.ctx != ctx {
		t.Fatal("subscribe was not one text frame with the supplied context")
	}
	mac := hmac.New(sha256.New, []byte(credentials.APISecret))
	_, _ = mac.Write([]byte(
		"apiKey=test+key/marker&timestamp=1499827319559"))
	signature := hex.EncodeToString(mac.Sum(nil))
	want := `{"id":"subscription-1",` +
		`"method":"userDataStream.subscribe.signature",` +
		`"params":{"apiKey":"test+key/marker",` +
		`"timestamp":1499827319559,"signature":"` + signature + `"}}`
	if string(conn.payload) != want {
		t.Error("subscription frame differs from the exact signed frame")
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(conn.payload, &frame); err != nil {
		t.Fatal(err)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(frame["params"], &params); err != nil {
		t.Fatal(err)
	}
	if len(frame) != 3 || len(params) != 3 ||
		string(params["timestamp"]) != "1499827319559" {
		t.Error("frame fields or numeric timestamp are incorrect")
	}
	conn = recordingWriter{err: context.Canceled}
	err = WriteSubscribe(ctx, &conn, credentials, "subscription-1", time.Now())
	if !errors.Is(err, context.Canceled) || conn.writes != 1 {
		t.Error("subscription write error was lost or retried")
	}
}

func TestDialStreamRefusesRedirect(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			reached.Store(true)
			conn, err := websocket.Accept(w, r, nil)
			if err == nil {
				_ = conn.CloseNow()
			}
		}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := DialStream(ctx, server.URL)
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil || reached.Load() {
		t.Error("stream dial followed a redirect")
	}
}

func TestDialStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"status":200}`))
		}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := DialStream(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	typ, payload, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText ||
		string(payload) != `{"status":200}` {
		t.Error("stream connection could not read a server frame")
	}
	ctx, cancelDial := context.WithCancel(context.Background())
	cancelDial()
	_, err = DialStream(ctx, server.URL)
	if !errors.Is(err, context.Canceled) {
		t.Error("stream dial lost context cancellation")
	}
}
