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

// Package bybit provides instruments, signed REST requests and WebSocket
// frames.
package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const (
	// LiveRESTURL is Bybit's public REST host.
	LiveRESTURL = "https://api.bybit.com"
	// InstrumentsPath is the instruments catalogue endpoint.
	InstrumentsPath = "/v5/market/instruments-info"
)

// Instrument identifies a venue symbol, its base and quote assets, and its
// trading status. Absent fields decode to empty strings.
type Instrument struct {
	Symbol    string `json:"symbol"`
	BaseCoin  string `json:"baseCoin"`
	QuoteCoin string `json:"quoteCoin"`
	Status    string `json:"status"`
}

// LotSizeFilter holds enforced spot lot rules as exact decimal strings.
// Absent fields decode to empty strings; callers validate what they require.
// Deprecated minOrderQty, maxOrderQty and maxOrderAmt are not modelled.
type LotSizeFilter struct {
	BasePrecision     string `json:"basePrecision"`
	QuotePrecision    string `json:"quotePrecision"`
	MinOrderAmt       string `json:"minOrderAmt"`
	MaxLimitOrderQty  string `json:"maxLimitOrderQty"`
	MaxMarketOrderQty string `json:"maxMarketOrderQty"`
}

// PriceFilter holds the enforced spot tick size as an exact decimal string.
// Absent fields decode to empty strings; callers validate what they require.
type PriceFilter struct {
	TickSize string `json:"tickSize"`
}

// InstrumentRules embeds the symbol and trading status and holds its enforced
// spot fields. Decimal values retain their exact strings. Absent fields decode
// to empty strings; callers validate what they require.
type InstrumentRules struct {
	Instrument
	LotSizeFilter LotSizeFilter `json:"lotSizeFilter"`
	PriceFilter   PriceFilter   `json:"priceFilter"`
}

// FetchInstruments fetches all catalogue pages using the supplied client,
// host and category. It returns entries as decoded, without filtering or
// trimming them, and does not retry failed requests.
// It refuses pages without retCode or with an absent or null result.list.
func FetchInstruments(
	ctx context.Context, client *http.Client, baseURL, category string,
) ([]Instrument, error) {
	var instruments []Instrument
	cursor := ""
	for {
		reqURL, err := url.Parse(baseURL + InstrumentsPath)
		if err != nil {
			return nil, err
		}
		values := reqURL.Query()
		values.Set("category", category)
		if cursor != "" {
			values.Set("cursor", cursor)
		}
		reqURL.RawQuery = values.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var payload struct {
			RetCode *int   `json:"retCode"`
			RetMsg  string `json:"retMsg"`
			Result  struct {
				List           []Instrument `json:"list"`
				NextPageCursor string       `json:"nextPageCursor"`
			} `json:"result"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		if err := resp.Body.Close(); err != nil {
			slog.Warn("bybit instruments: close response body", "error", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("bybit instruments: unexpected status %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("bybit instruments: decode: %w", decodeErr)
		}
		if payload.RetCode == nil {
			return nil, fmt.Errorf("bybit instruments: decode: retCode is required")
		}
		if *payload.RetCode != 0 {
			return nil, instrumentsRefusal(*payload.RetCode, payload.RetMsg)
		}
		if payload.Result.List == nil {
			return nil, fmt.Errorf("bybit instruments: decode: result.list is required")
		}
		instruments = append(instruments, payload.Result.List...)
		cursor = strings.TrimSpace(payload.Result.NextPageCursor)
		if cursor == "" {
			return instruments, nil
		}
	}
}

// FetchInstrument requests exactly one matching instrument without pagination.
// For spot, a successful response with list: [] returns zero rules, false,
// and nil for not found. This rule rests on observed venue responses for
// unknown spot symbols; Bybit's documentation does not define that response.
// Other categories filter by status or base coin by default, so their empty
// list is an error. Multiple or mismatched records return an error.
func FetchInstrument(ctx context.Context, client *http.Client, baseURL, category, symbol string) (InstrumentRules, bool, error) {
	if client == nil {
		return InstrumentRules{}, false, fmt.Errorf("bybit instruments: HTTP client is required")
	}
	if category == "" || symbol == "" {
		return InstrumentRules{}, false, fmt.Errorf("bybit instruments: category and symbol are required")
	}
	query := url.Values{"category": {category}, "symbol": {symbol}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+InstrumentsPath+"?"+query.Encode(), nil)
	if err != nil {
		return InstrumentRules{}, false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return InstrumentRules{}, false, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("bybit instruments: close response body", "error", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return InstrumentRules{}, false, fmt.Errorf("bybit instruments: unexpected status %d", resp.StatusCode)
	}
	var payload struct {
		RetCode *int   `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []InstrumentRules `json:"list"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return InstrumentRules{}, false, fmt.Errorf("bybit instruments: decode: %w", err)
	}
	if payload.RetCode == nil {
		return InstrumentRules{}, false, fmt.Errorf("bybit instruments: decode: retCode is required")
	}
	if *payload.RetCode != 0 {
		return InstrumentRules{}, false, instrumentsRefusal(*payload.RetCode, payload.RetMsg)
	}
	if payload.Result.List == nil {
		return InstrumentRules{}, false, fmt.Errorf("bybit instruments: decode: result.list is required")
	}
	if len(payload.Result.List) == 0 && category == "spot" {
		return InstrumentRules{}, false, nil
	}
	if len(payload.Result.List) != 1 || payload.Result.List[0].Symbol != symbol {
		return InstrumentRules{}, false, fmt.Errorf("bybit instruments: expected exactly one matching symbol")
	}
	return payload.Result.List[0], true, nil
}

func instrumentsRefusal(code int, retMsg string) error {
	msg := strings.TrimSpace(retMsg)
	if msg == "" {
		msg = "request failed"
	}
	return fmt.Errorf("bybit instruments: %d: %s", code, msg)
}
