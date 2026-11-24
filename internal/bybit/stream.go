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
	"strconv"
	"time"

	"github.com/coder/websocket"

	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	// LivePrivateStreamURL is Bybit's live private WebSocket endpoint.
	LivePrivateStreamURL = "wss://stream.bybit.com/v5/private"
	// TestnetPrivateStreamURL is Bybit's testnet private WebSocket endpoint.
	TestnetPrivateStreamURL = "wss://stream-testnet.bybit.com/v5/private"
)

// WriteAuth sends a text auth frame. The caller supplies a future expiry and
// parses the reply.
func WriteAuth(ctx context.Context, conn vendorhttp.StreamWriter, credentials Credentials, expires time.Time) error {
	expiresMs := expires.UnixMilli()
	frame := struct {
		Op   string `json:"op"`
		Args []any  `json:"args"`
	}{Op: "auth", Args: []any{
		credentials.APIKey, expiresMs,
		vendorhttp.SignHex("GET/realtime"+strconv.FormatInt(expiresMs, 10), credentials.APISecret),
	}}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, encoded)
}

// WriteSubscribe sends the requested topics in one text frame. Reply parsing
// belongs to the caller. At least one topic is required.
func WriteSubscribe(ctx context.Context, conn vendorhttp.StreamWriter, topics ...string) error {
	encoded, err := SubscribeFrame(topics...)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, encoded)
}

// SubscribeFrame builds a subscription frame for at least one topic.
func SubscribeFrame(topics ...string) ([]byte, error) {
	if len(topics) == 0 {
		return nil, errors.New("bybit: at least one subscription topic is required")
	}
	frame := struct {
		Op   string   `json:"op"`
		Args []string `json:"args"`
	}{Op: "subscribe", Args: topics}
	return json.Marshal(frame)
}
