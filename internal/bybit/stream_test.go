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

package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type frameWriter struct {
	typ  websocket.MessageType
	data []byte
	err  error
	ctx  context.Context
}

func (w *frameWriter) Write(ctx context.Context, typ websocket.MessageType, data []byte) error {
	w.ctx, w.typ, w.data = ctx, typ, append([]byte(nil), data...)
	return w.err
}

func TestPrivateAuthFrame(t *testing.T) {
	w := &frameWriter{}
	ctx := t.Context()
	expires := time.UnixMilli(1700000005123)
	if err := WriteAuth(ctx, w, signedCredentials, expires); err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Op   string            `json:"op"`
		Args []json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(w.data, &frame); err != nil {
		t.Fatal(err)
	}
	if w.typ != websocket.MessageText || w.ctx != ctx || frame.Op != "auth" || len(frame.Args) != 3 {
		t.Fatalf("wrong auth frame: %s", w.data)
	}
	var key, signature string
	var expiry int64
	if err := json.Unmarshal(frame.Args[0], &key); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(frame.Args[1], &expiry); err != nil {
		t.Fatal("expiry is not a JSON number")
	}
	if err := json.Unmarshal(frame.Args[2], &signature); err != nil {
		t.Fatal(err)
	}
	if key != signedCredentials.APIKey || expiry != expires.UnixMilli() || signature != independentHMAC(signedCredentials.APISecret, "GET/realtime1700000005123") {
		t.Fatal("auth arguments or signature differ from protocol")
	}
	if strings.Contains(string(w.data), signedCredentials.APISecret) {
		t.Fatal("secret escaped HMAC key")
	}
	sentinel := context.Canceled
	w.err = sentinel
	if err := WriteAuth(ctx, w, signedCredentials, expires); !errors.Is(err, sentinel) {
		t.Fatalf("writer error lost: %v", err)
	}
}

func TestPrivateSubscribeFrame(t *testing.T) {
	w := &frameWriter{}
	ctx := t.Context()
	if err := WriteSubscribe(ctx, w, "order", "execution"); err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Op   string   `json:"op"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(w.data, &frame); err != nil {
		t.Fatal(err)
	}
	if w.typ != websocket.MessageText || w.ctx != ctx || frame.Op != "subscribe" || !reflect.DeepEqual(frame.Args, []string{"order", "execution"}) {
		t.Fatalf("wrong subscribe frame: %s", w.data)
	}
	w.data = nil
	if err := WriteSubscribe(ctx, w); err == nil || len(w.data) != 0 {
		t.Fatal("empty subscription was written")
	}
	w.err = errors.New("closed")
	if err := WriteSubscribe(ctx, w, "order"); err == nil {
		t.Fatal("write failure lost")
	}
}
