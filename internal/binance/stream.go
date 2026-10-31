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
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

// StreamWriter is the write surface for a WebSocket API subscription.
type StreamWriter interface {
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
}

// DialStream opens a WebSocket API connection with a context-bounded dial
// and the same redirect refusal as REST. The caller reads the responses.
func DialStream(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: &http.Client{CheckRedirect: refuseRedirect},
	})
	return conn, err
}

// WriteSubscribe sends one signed user data subscription as a text frame.
// The caller owns response and event parsing. HMAC parameters are sorted
// by name and joined without URL escaping, as the WebSocket API requires.
func WriteSubscribe(ctx context.Context, conn StreamWriter,
	credentials Credentials, id string, now time.Time,
) error {
	timestamp := now.UnixMilli()
	payload := "apiKey=" + credentials.APIKey + "&timestamp=" +
		strconv.FormatInt(timestamp, 10)
	frame := struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params struct {
			APIKey    string `json:"apiKey"`
			Timestamp int64  `json:"timestamp"`
			Signature string `json:"signature"`
		} `json:"params"`
	}{ID: id, Method: "userDataStream.subscribe.signature"}
	frame.Params.APIKey = credentials.APIKey
	frame.Params.Timestamp = timestamp
	frame.Params.Signature = sign(payload, credentials.APISecret)
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, encoded)
}
