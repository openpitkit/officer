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
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"

	"go.openpit.dev/officer/internal/vendorhttp"
)

// The user channel carries open orders. Allow large snapshots beyond the
// WebSocket default while keeping each message's memory bounded.
const maxStreamMessageBytes = 8 << 20

// DialStream opens a credential-free URL with redirects refused and default
// TLS verification. ctx bounds the dial; incoming messages have a size limit.
func DialStream(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, err := vendorhttp.DialStream(ctx, "coinbase", url)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(maxStreamMessageBytes)
	return conn, nil
}

// WriteSubscribe sends one subscription with a fresh JWT and no product filter.
func WriteSubscribe(ctx context.Context, conn vendorhttp.StreamWriter, channel string,
	credentials Credentials,
) error {
	token, err := mintJWT(credentials, "", time.Now(), rand.Reader)
	if err != nil {
		return err
	}
	frame, err := json.Marshal(struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		JWT     string `json:"jwt"`
	}{"subscribe", channel, token})
	if err != nil {
		return errors.New("coinbase: encode subscription failed")
	}
	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		// A writer may echo the entire authenticated frame in any error layer.
		return vendorhttp.NewRequestError("coinbase: write subscription failed",
			errors.New("stream write failed"))
	}
	return nil
}
