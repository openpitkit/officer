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

package fixture

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

type venueHarness struct{ t *testing.T }

func (*venueHarness) isClosing() bool { return false }

func venueHandler(t, other *testing.T, harness *venueHarness) {
	httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		var authenticated atomic.Bool

		if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil {
			// ruleid: test-stub-close-guard
			t.Error(err)
		}
		_, raw, err := conn.Read(ctx)
		if err != nil {
			// ruleid: test-stub-close-guard
			t.Errorf("read: %v", err)
		}
		err = conn.Write(ctx, websocket.MessageText, raw)
		if err != nil {
			// ruleid: test-stub-close-guard
			t.Fatal(err)
		}
		if err = conn.Write(ctx, websocket.MessageText, raw); err != nil {
			// ruleid: test-stub-close-guard
			t.Fatalf("write: %v", err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			// ruleid: test-stub-close-guard
			t.Error(err)
		}
		if _, raw, err = conn.Read(ctx); err != nil {
			// ruleid: test-stub-close-guard
			t.Error(err)
		}
		_, raw, err = conn.Read(ctx)
		if err != nil || len(raw) == 0 {
			// ruleid: test-stub-close-guard
			t.Error("missing frame")
		}
		if conn.Write(ctx, websocket.MessageText, raw) != nil {
			if ctx.Err() == nil {
				// ruleid: test-stub-close-guard
				t.Error("write failed")
			}
		}
		if conn.Write(ctx, websocket.MessageText, raw) != nil {
			if !authenticated.Load() {
				// ruleid: test-stub-close-guard
				t.Error("write failed before authentication")
			}
		}
		if conn.Write(ctx, websocket.MessageText, raw) != nil {
			if other.Context().Err() == nil {
				// ruleid: test-stub-close-guard
				t.Error("write failed")
			}
		}
		if conn.Write(ctx, websocket.MessageText, raw) != nil && other.Context().Err() == nil {
			// ruleid: test-stub-close-guard
			t.Error("write failed")
		}
		if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil || t.Context().Err() == nil {
			// ruleid: test-stub-close-guard
			t.Error(err)
		}
		if t.Context().Err() == nil {
			t.Log("active test")
		} else if conn.Write(ctx, websocket.MessageText, raw) != nil {
			// ruleid: test-stub-close-guard
			t.Error("write failed")
		}
		if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil && t.Context().Err() == nil {
			// ruleid: test-stub-close-guard
			t.Error(err)
		}
		if !harness.isClosing() && conn != nil && conn.Write(ctx, websocket.MessageText, raw) != nil {
			// ruleid: test-stub-close-guard
			t.Error("write failed")
		}
		if authenticated.Load() && t.Context().Err() == nil && conn.Close(websocket.StatusGoingAway, "restart") != nil {
			// ruleid: test-stub-close-guard
			t.Error("close failed")
		}
		if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil && !harness.isClosing() {
			// ruleid: test-stub-close-guard
			t.Error(err)
		}
		if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil {
			if t.Context().Err() == nil {
				// ok: test-stub-close-guard
				t.Error(err)
			}
		}
		if _, _, err := conn.Read(ctx); !harness.isClosing() && err != nil {
			// ruleid: test-stub-close-guard
			t.Error(err)
		}
		if !harness.isClosing() {
			if _, _, err := conn.Read(ctx); err != nil {
				// ruleid: test-stub-close-guard
				t.Error(err)
			}
		}
		if t.Context().Err() == nil {
			if _, _, err := conn.Read(ctx); err != nil {
				// ruleid: test-stub-close-guard
				t.Error(err)
			}
		}
		if authenticated.Load() && !harness.isClosing() {
			if _, _, err := conn.Read(ctx); err != nil {
				// ruleid: test-stub-close-guard
				t.Error(err)
			}
		}
		if authenticated.Load() && t.Context().Err() == nil {
			if _, _, err := conn.Read(ctx); err != nil {
				// ruleid: test-stub-close-guard
				t.Error(err)
			}
		}
		if _, _, err := conn.Read(ctx); err != nil {
			if !harness.isClosing() {
				// ok: test-stub-close-guard
				t.Error(err)
			}
		}
		if !harness.isClosing() && conn.Write(ctx, websocket.MessageText, raw) != nil {
			// ruleid: test-stub-close-guard
			t.Error("write failed")
		}
		if conn.Write(ctx, websocket.MessageText, raw) != nil && !harness.isClosing() {
			// ruleid: test-stub-close-guard
			t.Error("write failed")
		}
		if conn.Write(ctx, websocket.MessageText, raw) != nil && t.Context().Err() == nil {
			// ruleid: test-stub-close-guard
			t.Error("write failed")
		}
		if conn.Write(ctx, websocket.MessageText, raw) != nil && !harness.isClosing() || !authenticated.Load() {
			// ruleid: test-stub-close-guard
			t.Error("write failed or authentication is missing")
		}
		if _, _, err := conn.Read(ctx); err != nil {
			if !harness.isClosing() || !authenticated.Load() {
				// ruleid: test-stub-close-guard
				t.Error(err)
			}
		}
		if _, _, err := conn.Read(ctx); err != nil {
			if authenticated.Load() && t.Context().Err() == nil {
				// ruleid: test-stub-close-guard
				t.Error(err)
			}
		}
	}))
}

func writeFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) bool {
	if conn.Write(ctx, websocket.MessageText, []byte("frame")) != nil {
		// ruleid: test-stub-close-guard
		t.Error("write failed")
		return false
	}
	return true
}

func guardedWrite(t *testing.T, ctx context.Context, conn *websocket.Conn) bool {
	if conn.Write(ctx, websocket.MessageText, []byte("frame")) != nil {
		if t.Context().Err() == nil {
			// ok: test-stub-close-guard
			t.Error("write failed")
		}
		return false
	}
	return true
}

func wrappedAfterEarlierError(t *testing.T, conn *websocket.Conn, harness *venueHarness, err error) {
	if err != nil {
		if !harness.isClosing() {
			if _, _, err := conn.Read(t.Context()); err != nil {
				// ruleid: test-stub-close-guard
				t.Error(err)
			}
		}
	}
}

func wrappedAfterEarlierContextError(t *testing.T, conn *websocket.Conn, err error) {
	if err != nil {
		if t.Context().Err() == nil {
			if _, _, err := conn.Read(t.Context()); err != nil {
				// ruleid: test-stub-close-guard
				t.Error(err)
			}
		}
	}
}

func closeFrame(t *testing.T, conn *websocket.Conn) {
	err := conn.Close(websocket.StatusGoingAway, "restart")
	if err != nil {
		// ruleid: test-stub-close-guard
		t.Error(err)
	}
}

func (*venueHarness) closeFrame(t *testing.T, conn *websocket.Conn) {
	if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil {
		// ruleid: test-stub-close-guard
		t.Error(err)
	}
}

func closeCallback(t *testing.T) func(*websocket.Conn) {
	return func(conn *websocket.Conn) {
		if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil {
			// ruleid: test-stub-close-guard
			t.Error(err)
		}
	}
}

func writeCallback(t *testing.T) func(*websocket.Conn) bool {
	return func(conn *websocket.Conn) bool {
		if err := conn.Write(t.Context(), websocket.MessageText, nil); err != nil {
			// ruleid: test-stub-close-guard
			t.Error(err)
			return false
		}
		return true
	}
}

func (h *venueHarness) writeFrame(conn *websocket.Conn) bool {
	if err := conn.Write(h.t.Context(), websocket.MessageText, nil); err != nil {
		// ruleid: test-stub-close-guard
		h.t.Error(err)
		return false
	}
	return true
}

func validateFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	_, raw, err := conn.Read(ctx)
	if err != nil {
		if t.Context().Err() == nil {
			// ok: test-stub-close-guard
			t.Error(err)
		}
		return
	}
	if !json.Valid(raw) {
		// ok: test-stub-close-guard
		t.Error("invalid JSON frame")
	}
	var frame map[string]string
	if err := json.Unmarshal(raw, &frame); err != nil {
		// ok: test-stub-close-guard
		t.Error(err)
	}
}

func nonShadowingInit(t *testing.T, conn *websocket.Conn) {
	_, raw, err := conn.Read(t.Context())
	if n := len(raw); err != nil || n == 0 {
		// ruleid: test-stub-close-guard
		t.Error("missing frame")
	}
}

func readWithLeadingHarnessGuard(t *testing.T, conn *websocket.Conn, harness *venueHarness) {
	_, _, err := conn.Read(t.Context())
	if !harness.isClosing() && err != nil {
		// ruleid: test-stub-close-guard
		t.Error(err)
	}
}

func readWithLeadingContextGuard(t *testing.T, conn *websocket.Conn) {
	_, _, err := conn.Read(t.Context())
	if t.Context().Err() == nil && err != nil {
		// ruleid: test-stub-close-guard
		t.Error(err)
	}
}

func readWithNegatedGroup(t *testing.T, conn *websocket.Conn, x bool) {
	_, _, err := conn.Read(t.Context())
	if err != nil && !(x && t.Context().Err() == nil) {
		// ruleid: test-stub-close-guard
		t.Error("read failed")
	}
}

func shadowingInit(t *testing.T, conn *websocket.Conn) {
	_, raw, err := conn.Read(t.Context())
	_ = err
	if err := json.Unmarshal(raw, new(string)); err != nil {
		// ok: test-stub-close-guard
		t.Error(err)
	}
	if _, err := json.Marshal(raw); err != nil {
		// ok: test-stub-close-guard
		t.Error(err)
	}
}

func reassigningInit(t *testing.T, conn *websocket.Conn, ctx context.Context) {
	_, raw, err := conn.Read(ctx)
	if err = json.Unmarshal(raw, new(string)); err != nil {
		// ok: test-stub-close-guard
		t.Error(err)
	}
	if _, err = json.Marshal(raw); err != nil {
		// ok: test-stub-close-guard
		t.Error(err)
	}
	if _, _, err = conn.Read(ctx); err != nil {
		// ruleid: test-stub-close-guard
		t.Error(err)
	}
}

func namedHandlerWithoutResult(t *testing.T, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	if _, _, err := conn.Read(r.Context()); err != nil {
		// ruleid: test-stub-close-guard
		t.Error(err)
	}
}

func namedHandler(t *testing.T, w http.ResponseWriter, r *http.Request) *websocket.Conn {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil
	}
	_, raw, err := conn.Read(r.Context())
	if err != nil || len(raw) == 0 {
		// ruleid: test-stub-close-guard
		t.Error("missing frame")
	}
	return conn
}

func reversedHandler(t *testing.T, r *http.Request, w http.ResponseWriter) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	if _, _, err := conn.Read(r.Context()); err != nil {
		// ruleid: test-stub-close-guard
		t.Error(err)
	}
}

func reversedHandlerResult(t *testing.T, r *http.Request, w http.ResponseWriter) *websocket.Conn {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil
	}
	if _, _, err := conn.Read(r.Context()); err != nil {
		// ruleid: test-stub-close-guard
		t.Error(err)
	}
	return conn
}

func (h *venueHarness) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	if _, _, err := conn.Read(r.Context()); err != nil {
		// ruleid: test-stub-close-guard
		h.t.Error(err)
	}
}

func (h *venueHarness) serveReversed(r *http.Request, w http.ResponseWriter) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	if _, _, err := conn.Read(r.Context()); err != nil {
		// ruleid: test-stub-close-guard
		h.t.Error(err)
	}
}

func closeOutsideHandler(t *testing.T, url string) {
	conn, _, err := websocket.Dial(t.Context(), url, nil)
	if err != nil {
		return
	}
	if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil {
		// ok: test-stub-close-guard
		t.Error(err)
	}
}

func rawCloseHandler(t *testing.T, conn net.Conn) {
	httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := conn.Close(); err != nil {
			// ok: test-stub-close-guard
			t.Error(err)
		}
	}))
}
