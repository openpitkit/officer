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

package etoro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/coder/websocket"

	"go.openpit.dev/officer/internal/vendorhttp"
)

// StreamURL serves demo and real accounts; subscriptions select private topics.
const StreamURL = "wss://ws.etoro.com/ws"

// WriteStreamFrame writes one JSON text message. Write errors wrap the cause;
// the writer must not include frame contents in its errors. Encoding errors
// omit details that may contain frame credentials.
func WriteStreamFrame(ctx context.Context, conn vendorhttp.StreamWriter, frame any) error {
	if conn == nil {
		return errors.New("etoro: stream writer is required")
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return errors.New("etoro: encode stream frame failed")
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("etoro: write stream frame: %w", err)
	}
	return nil
}

// WriteAuthenticate writes the caller's id so its acknowledgement can be matched.
func WriteAuthenticate(ctx context.Context, conn vendorhttp.StreamWriter, id string, credentials Credentials) error {
	if err := credentials.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("etoro: stream request id is required")
	}
	return WriteStreamFrame(ctx, conn, struct {
		ID        string `json:"id"`
		Operation string `json:"operation"`
		Data      struct {
			UserKey string `json:"userKey"`
			APIKey  string `json:"apiKey"`
		} `json:"data"`
	}{id, "Authenticate", struct {
		UserKey string `json:"userKey"`
		APIKey  string `json:"apiKey"`
	}{credentials.UserKey, credentials.APIKey}})
}

// WriteSubscribe writes the caller's id and topics, without requesting a snapshot.
func WriteSubscribe(ctx context.Context, conn vendorhttp.StreamWriter, id string, topics []string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("etoro: stream request id is required")
	}
	if len(topics) == 0 {
		return errors.New("etoro: stream topics are required")
	}
	return WriteStreamFrame(ctx, conn, struct {
		ID        string `json:"id"`
		Operation string `json:"operation"`
		Data      struct {
			Topics []string `json:"topics"`
		} `json:"data"`
	}{id, "Subscribe", struct {
		Topics []string `json:"topics"`
	}{topics}})
}
