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
	"encoding/json"
	"errors"
	"strconv"

	"github.com/coder/websocket"

	"go.openpit.dev/officer/internal/vendorhttp"
)

// WriteLogin authenticates the private stream. The caller reads the response.
func (c *Client) WriteLogin(ctx context.Context, conn vendorhttp.StreamWriter) error {
	if c.redact == nil {
		return errors.New("okx: credential redactor is required")
	}
	if conn == nil {
		return errors.New("okx: private stream is required")
	}
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	signature := sign(timestamp+"GET"+"/users/self/verify", c.credentials.APISecret)
	frame := struct {
		Op   string `json:"op"`
		Args [1]struct {
			APIKey     string `json:"apiKey"`
			Passphrase string `json:"passphrase"`
			Timestamp  string `json:"timestamp"`
			Sign       string `json:"sign"`
		} `json:"args"`
	}{Op: "login"}
	frame.Args[0].APIKey = c.credentials.APIKey
	frame.Args[0].Passphrase = c.credentials.Passphrase
	frame.Args[0].Timestamp = timestamp
	frame.Args[0].Sign = signature
	encoded, err := json.Marshal(frame)
	if err != nil {
		return vendorhttp.Wrap(c.redact, "okx", "encode login", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, encoded); err != nil {
		return vendorhttp.Wrap(c.redact, "okx", "write login", err)
	}
	return nil
}

// WriteSubscribe subscribes to spot orders. The caller reads the response.
func (c *Client) WriteSubscribe(ctx context.Context, conn vendorhttp.StreamWriter) error {
	if c.redact == nil {
		return errors.New("okx: credential redactor is required")
	}
	if conn == nil {
		return errors.New("okx: private stream is required")
	}
	err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"op":"subscribe","args":[{"channel":"orders","instType":"SPOT"}]}`))
	if err != nil {
		return vendorhttp.Wrap(c.redact, "okx", "write subscription", err)
	}
	return nil
}
