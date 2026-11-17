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

// Package okx provides the public SPOT catalogue and ping loop, credential
// parsing with the account's registration site, signed REST v5 requests, and
// private-stream dial, login and subscription helpers.
package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// SpotInstrumentsURI is the request URI (path and instType=SPOT query) of the
// public SPOT instrument catalogue.
const SpotInstrumentsURI = "/api/v5/public/instruments?instType=SPOT"

// Instrument holds the exchange's raw SPOT identifiers and trading rules.
// OKX documents every field as a string. A wrong JSON type fails the fetch
// with a decode error. Absent or null fields yield "", which callers must
// treat as not tradable or not sizable. MinSz, LotSz and TickSz are raw decimal
// strings that callers convert to domain types at their edge.
type Instrument struct {
	InstID   string `json:"instId"`
	BaseCcy  string `json:"baseCcy"`
	QuoteCcy string `json:"quoteCcy"`
	State    string `json:"state"`
	MinSz    string `json:"minSz"`
	LotSz    string `json:"lotSz"`
	TickSz   string `json:"tickSz"`
}

type instrumentsResponse struct {
	Code string       `json:"code"`
	Msg  string       `json:"msg"`
	Data []Instrument `json:"data"`
}

// FetchInstruments fetches the SPOT catalogue using the caller's client and
// host.
func FetchInstruments(
	ctx context.Context, client *http.Client, baseURL string,
) ([]Instrument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+SpotInstrumentsURI, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("okx instruments: close response body", "error", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("okx instruments: unexpected status %d", resp.StatusCode)
	}
	var payload instrumentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("okx instruments: decode: %w", err)
	}
	if payload.Code != "" && payload.Code != "0" {
		msg := strings.TrimSpace(payload.Msg)
		if msg == "" {
			msg = "request failed"
		}
		return nil, fmt.Errorf("okx instruments: %s: %s", payload.Code, msg)
	}
	return payload.Data, nil
}
