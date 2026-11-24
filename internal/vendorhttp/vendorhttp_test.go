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

package vendorhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/internal/vendorhttp"
)

func TestDialStreamRefusesRedirect(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := vendorhttp.DialStream(ctx, "vendor", source.URL)
	if conn != nil {
		_ = conn.CloseNow()
	}
	if conn != nil || err == nil || !strings.Contains(err.Error(), "307") || hits.Load() != 0 {
		t.Fatal("stream redirect was not refused before reaching the second host")
	}
}

func TestDialStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error("accept stream failed")
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		typ, payload, err := conn.Read(ctx)
		if err != nil {
			t.Error("read stream frame failed")
			return
		}
		if err := conn.Write(ctx, typ, payload); err != nil {
			t.Error("echo stream frame failed")
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := vendorhttp.DialStream(ctx, "vendor", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	var writer vendorhttp.StreamWriter = conn
	if err := writer.Write(ctx, websocket.MessageText, []byte(`{"probe":true}`)); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || string(payload) != `{"probe":true}` {
		t.Fatal("stream did not exchange the text frame")
	}
}

func TestDialStreamErrorPreservesCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	conn, err := vendorhttp.DialStream(ctx, "venue", server.URL)
	if conn != nil {
		_ = conn.CloseNow()
		t.Fatal("timed out dial returned a connection")
	}
	if !errors.Is(err, context.DeadlineExceeded) ||
		!strings.HasPrefix(err.Error(), "venue: dial stream: ") {
		t.Fatal("stream dial lost the vendor, operation or deadline cause")
	}
}

func TestDialStreamTLSVerification(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := vendorhttp.DialStream(ctx, "vendor", server.URL)
	if conn != nil {
		_ = conn.CloseNow()
	}
	if conn != nil || err == nil || hits.Load() != 0 {
		t.Fatal("untrusted stream TLS certificate accepted")
	}
}

func TestSignHex(t *testing.T) {
	// RFC 4231, section 4.3, test case 2.
	const want = "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"
	if got := vendorhttp.SignHex("what do ya want for nothing?", "Jefe"); got != want {
		t.Fatalf("published signature = %q, want %q", got, want)
	}
}

func TestCopyClient(t *testing.T) {
	for _, name := range []string{"nil", "default policy", "caller policy"} {
		t.Run(name, func(t *testing.T) {
			var client *http.Client
			var policyCalls atomic.Int32
			callerError := errors.New("caller policy marker")
			if name != "nil" {
				client = &http.Client{Timeout: time.Second, Transport: http.DefaultTransport}
				if name == "caller policy" {
					client.CheckRedirect = func(*http.Request, []*http.Request) error {
						policyCalls.Add(1)
						return callerError
					}
				}
			}
			copied := vendorhttp.CopyClient(client)
			if client == nil {
				if copied != nil {
					t.Fatal("nil client became non-nil")
				}
				return
			}
			if copied == client {
				t.Fatal("caller client was not copied")
			}
			if copied.Timeout != client.Timeout || copied.Transport != client.Transport || copied.Jar != client.Jar {
				t.Fatal("client settings changed during copy")
			}
			if copied.CheckRedirect == nil {
				t.Fatal("copy has no redirect refusal policy")
			}
			if name == "caller policy" {
				if client.CheckRedirect == nil || client.CheckRedirect(nil, nil) != callerError {
					t.Fatal("caller redirect policy was mutated")
				}
				policyCalls.Store(0)
			} else if client.CheckRedirect != nil {
				t.Fatal("caller default policy was mutated")
			}
			var destinationCalls atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destinationCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer destination.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination.URL, http.StatusFound)
			}))
			defer source.Close()
			request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := copied.Do(request)
			if err != nil {
				t.Fatalf("redirect was not returned as a response: %v", err)
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			if response.StatusCode != http.StatusFound || destinationCalls.Load() != 0 || policyCalls.Load() != 0 {
				t.Fatal("copy followed a redirect or used the caller policy")
			}
		})
	}
}

func TestRefuseRedirect(t *testing.T) {
	for _, request := range []*http.Request{nil, {Method: http.MethodGet}} {
		if err := vendorhttp.RefuseRedirect(request, nil); err != http.ErrUseLastResponse {
			t.Fatal("redirect policy did not return http.ErrUseLastResponse")
		}
	}
}

func TestWrap(t *testing.T) {
	credential := `marker-"secret<&/+`
	raw, err := json.Marshal(map[string]string{"secret": credential})
	if err != nil {
		t.Fatal(err)
	}
	_, redactor, err := secret.CredentialRedactor(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, spelling string }{
		{"raw", credential},
		{"JSON", string(quoted[1 : len(quoted)-1])},
		{"query", url.QueryEscape(credential)},
		{"path", url.PathEscape(credential)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("failure " + tc.spelling)
			wrapped := vendorhttp.Wrap(redactor, "venue", "request", cause)
			if wrapped.Error() != "venue: request: failure [redacted]" {
				t.Fatal("request message was not fully redacted")
			}
			if errors.Unwrap(wrapped) != cause || !errors.Is(wrapped, cause) {
				t.Fatal("raw request cause was not retained")
			}
		})
	}
}

func TestNewRequestError(t *testing.T) {
	cause := errors.New("cause marker")
	wrapped := vendorhttp.NewRequestError("already [redacted]", cause)
	if wrapped.Error() != "already [redacted]" {
		t.Fatal("prepared message changed")
	}
	if errors.Unwrap(wrapped) != cause {
		t.Fatal("prepared cause changed")
	}
}
