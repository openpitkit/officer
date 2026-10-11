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

package alpaca

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

// StreamWriter is the write half of a stream connection: the Write method of
// *websocket.Conn, which satisfies it.
type StreamWriter interface {
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
}

// DialStream opens a stream connection to url. Redirects are refused, as for
// REST, so the auth frame cannot reach a host chosen by a redirect response.
// It has no timeout of its own: ctx bounds the dial.
func DialStream(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: &http.Client{CheckRedirect: refuseRedirect}})
	return conn, err
}

// WriteStreamFrame writes frame as one JSON text message.
func WriteStreamFrame(ctx context.Context, conn StreamWriter, frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

// WriteStreamAuth writes the auth frame that both the trading and the market
// data streams expect. The key and secret members are Alpaca's wire protocol,
// not credential aliases.
func WriteStreamAuth(ctx context.Context, conn StreamWriter, credentials Credentials) error {
	return WriteStreamFrame(ctx, conn, struct {
		Action string `json:"action"`
		Key    string `json:"key"`
		Secret string `json:"secret"`
	}{"auth", credentials.APIKey, credentials.APISecret})
}
