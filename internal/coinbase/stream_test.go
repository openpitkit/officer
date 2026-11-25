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

package coinbase

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type streamWriterFunc func(context.Context, websocket.MessageType, []byte) error

func (f streamWriterFunc) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	return f(ctx, typ, p)
}

func TestWriteSubscribe(t *testing.T) {
	identity := newIdentity(t)
	var previousNonce string
	var calls int
	for _, channel := range []string{"user", "heartbeats"} {
		before := time.Now().Unix()
		writer := streamWriterFunc(func(_ context.Context, typ websocket.MessageType, p []byte) error {
			calls++
			if typ != websocket.MessageText {
				t.Error("subscription must be one text frame")
			}
			var frame map[string]json.RawMessage
			if json.Unmarshal(p, &frame) != nil || len(frame) != 3 {
				t.Fatal("subscription must contain only type, channel and jwt")
			}
			var gotType, gotChannel, token string
			if json.Unmarshal(frame["type"], &gotType) != nil || gotType != "subscribe" ||
				json.Unmarshal(frame["channel"], &gotChannel) != nil || gotChannel != channel ||
				json.Unmarshal(frame["jwt"], &token) != nil {
				t.Fatal("incorrect subscription frame")
			}
			now := time.Now().Unix()
			parts := strings.Split(token, ".")
			if len(parts) != 3 {
				t.Fatal("subscription JWT missing")
			}
			// The call can cross a second boundary; read nbf without rounding.
			claims, err := decodeClaims(token)
			if err != nil || claims.NotBefore < before || claims.NotBefore > now {
				t.Fatal("WS JWT nbf is outside the call interval")
			}
			nonce := verifyJWT(t, token, identity, "", claims.NotBefore)
			if nonce == previousNonce {
				t.Error("each subscription must mint a fresh nonce")
			}
			previousNonce = nonce
			return nil
		})
		if err := WriteSubscribe(context.Background(), writer, channel, identity.credentials); err != nil {
			t.Fatal("subscription failed")
		}
	}
	if calls != 2 {
		t.Error("each call must write exactly one frame")
	}
}

func decodeClaims(token string) (struct {
	NotBefore int64 `json:"nbf"`
}, error) {
	var claims struct {
		NotBefore int64 `json:"nbf"`
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, errors.New("invalid token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, err
	}
	err = json.Unmarshal(payload, &claims)
	return claims, err
}

func TestWriteSubscribeErrorRedaction(t *testing.T) {
	identity := newIdentity(t)
	for _, hidden := range []bool{false, true} {
		var minted string
		writer := streamWriterFunc(func(_ context.Context, _ websocket.MessageType, p []byte) error {
			var frame struct {
				JWT string `json:"jwt"`
			}
			if json.Unmarshal(p, &frame) != nil {
				t.Fatal("invalid frame")
			}
			minted = frame.JWT
			err := errors.New(string(p) + identity.credentials.APIKeyID + identity.pem)
			if hidden {
				return hiddenCause{err}
			}
			return err
		})
		err := WriteSubscribe(context.Background(), writer, "user", identity.credentials)
		assertSecretFree(t, err, identity, minted)
	}
	writer := streamWriterFunc(func(context.Context, websocket.MessageType, []byte) error {
		t.Error("invalid credentials reached writer")
		return nil
	})
	assertSecretFree(t, WriteSubscribe(context.Background(), writer, "user", Credentials{}), identity)
}

//nolint:staticcheck // Deliberately corrupt scalars that valid-key APIs cannot construct.
func TestWriteSubscribeRejectsInvalidKeys(t *testing.T) {
	identity := newIdentity(t)
	for _, tc := range []struct {
		name string
		edit func(*Credentials)
	}{
		{"off_curve", func(c *Credentials) { c.PrivateKey.X, c.PrivateKey.Y = big.NewInt(1), big.NewInt(1) }},
		{"scalar_at_order", func(c *Credentials) { c.PrivateKey.D = new(big.Int).Set(c.PrivateKey.Params().N) }},
		{"scalar_too_large", func(c *Credentials) { c.PrivateKey.D = new(big.Int).Lsh(big.NewInt(1), 256) }},
		{"scalar_zero", func(c *Credentials) { c.PrivateKey.D = big.NewInt(0) }},
		{"scalar_negative", func(c *Credentials) { c.PrivateKey.D = big.NewInt(-1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials := identity.credentials
			key := *credentials.PrivateKey
			credentials.PrivateKey = &key
			tc.edit(&credentials)
			writer := streamWriterFunc(func(context.Context, websocket.MessageType, []byte) error {
				t.Error("invalid key reached the writer")
				return nil
			})
			err := WriteSubscribe(context.Background(), writer, "user", credentials)
			assertSecretFree(t, err, identity)
			if err.Error() != "coinbase: sign JWT failed" {
				t.Error("invalid key must fail during signing")
			}
		})
	}
}

func TestDialStreamReadLimit(t *testing.T) {
	for _, size := range []int{32769, maxStreamMessageBytes, maxStreamMessageBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			payload := bytes.Repeat([]byte("x"), size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error("accept stream failed")
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx := r.Context()
				if err := conn.Write(ctx, websocket.MessageText, payload); err != nil && size <= maxStreamMessageBytes {
					if t.Context().Err() == nil {
						t.Error("write bounded frame failed")
					}
				}
			}))
			defer server.Close()
			ctx := t.Context()
			conn, err := DialStream(ctx, server.URL)
			if err != nil {
				t.Fatal("dial stream failed")
			}
			defer func() { _ = conn.CloseNow() }()
			typ, got, err := conn.Read(ctx)
			if size > maxStreamMessageBytes {
				if !errors.Is(err, websocket.ErrMessageTooBig) {
					t.Fatal("oversize stream frame must return ErrMessageTooBig")
				}
			} else if err != nil || typ != websocket.MessageText || !bytes.Equal(got, payload) {
				t.Fatal("bounded frame above the default limit was not read")
			}
		})
	}
}

func TestDialStreamAndSubscribe(t *testing.T) {
	identity := newIdentity(t)
	frames := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error("accept stream failed")
			return
		}
		defer func() { _ = conn.CloseNow() }()
		typ, p, err := conn.Read(r.Context())
		if err != nil || typ != websocket.MessageText {
			if t.Context().Err() == nil {
				t.Error("subscription not received as text")
			}
			return
		}
		frames <- p
	}))
	defer server.Close()
	ctx := t.Context()
	conn, err := DialStream(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal("dial stream failed")
	}
	defer func() { _ = conn.CloseNow() }()
	if WriteSubscribe(ctx, conn, "user", identity.credentials) != nil {
		t.Fatal("write subscription failed")
	}
	select {
	case p := <-frames:
		var frame map[string]json.RawMessage
		if json.Unmarshal(p, &frame) != nil || len(frame) != 3 {
			t.Error("wire subscription shape changed")
		}
	case <-ctx.Done():
		t.Fatal("subscription was not delivered")
	}
}
