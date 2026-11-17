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

package okx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"go.openpit.dev/officer/internal/vendorhttp"
)

func TestPrivateFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frames := make(chan bool, 1)
	timestamp := strconv.FormatInt(fixedTime.Unix(), 10)
	signature := expectedSignature(timestamp + "GET" + "/users/self/verify")
	expectedLogin := `{"op":"login","args":[{"apiKey":"test-key-marker",` +
		`"passphrase":"test-passphrase-marker","timestamp":"` +
		timestamp + `","sign":"` + signature + `"}]}`
	expectedSubscribe := `{"op":"subscribe","args":[{"channel":"orders","instType":"SPOT"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			frames <- false
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for _, expected := range []string{expectedLogin, expectedSubscribe} {
			kind, payload, err := conn.Read(ctx)
			if err != nil || kind != websocket.MessageText || string(payload) != expected {
				frames <- false
				return
			}
		}
		frames <- true
	}))
	defer server.Close()
	conn, err := vendorhttp.DialStream(ctx, "okx", "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal("private stream dial failed")
	}
	defer func() { _ = conn.CloseNow() }()
	client := testClient(t, "https://example.invalid", server.Client())
	if err := client.WriteLogin(ctx, conn); err != nil {
		t.Fatal("login write failed")
	}
	if err := client.WriteSubscribe(ctx, conn); err != nil {
		t.Fatal("subscription write failed")
	}
	select {
	case valid := <-frames:
		if !valid {
			t.Fatal("wrong private frame, timestamp, signature or message type")
		}
	case <-ctx.Done():
		t.Fatal("private frames were not received")
	}
}

func TestPrivateWriterFailures(t *testing.T) {
	client := testClient(t, "https://example.invalid", &http.Client{})
	ctx := context.Background()
	for _, write := range []func(context.Context, vendorhttp.StreamWriter) error{
		client.WriteLogin, client.WriteSubscribe,
	} {
		if err := write(ctx, nil); err == nil || err.Error() != "okx: private stream is required" {
			t.Fatal("missing stream was accepted or returned the wrong error")
		}
	}
	client = NewClient("https://example.invalid", client.credentials, nil, &http.Client{})
	for _, write := range []func(context.Context, vendorhttp.StreamWriter) error{
		client.WriteLogin, client.WriteSubscribe,
	} {
		if err := write(ctx, nil); err == nil ||
			err.Error() != "okx: credential redactor is required" {
			t.Fatal("missing redactor was accepted or returned the wrong error")
		}
	}
}

func TestPrivateWriteErrorsPreserveCause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	client := testClient(t, "https://example.invalid", server.Client())
	for _, tc := range []struct {
		name  string
		write func(context.Context, vendorhttp.StreamWriter) error
	}{
		{"login", client.WriteLogin},
		{"subscribe", client.WriteSubscribe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := vendorhttp.DialStream(ctx, "okx", "ws"+strings.TrimPrefix(server.URL, "http"))
			if err != nil {
				t.Fatal("private stream dial failed")
			}
			_ = conn.CloseNow()
			if err := tc.write(ctx, conn); !errors.Is(err, net.ErrClosed) {
				t.Fatal("write error lost the closed-connection cause")
			}
		})
	}
}
