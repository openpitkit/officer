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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
)

// DataURL is the public market data host; ExchangeInfoPath is its catalogue
// endpoint.
const (
	DataURL          = "https://data-api.binance.vision"
	ExchangeInfoPath = "/api/v3/exchangeInfo"
)

// ExchangeInfo holds the venue's symbol catalogue.
type ExchangeInfo struct {
	Symbols []Symbol `json:"symbols"`
}

// Symbol describes a venue symbol and its spot trading capabilities.
// Binance documents Status as a string, IsSpotTradingAllowed as a boolean,
// and OrderTypes as an array of strings. A wrong JSON type in these fields
// fails the fetch with a decode error. Absent or null fields yield their zero
// values: "", false, and nil; callers must treat these as not tradable.
// Filters remain raw because each filterType has its own shape; callers decode
// the types they need.
type Symbol struct {
	Symbol               string            `json:"symbol"`
	Status               string            `json:"status"`
	BaseAsset            string            `json:"baseAsset"`
	QuoteAsset           string            `json:"quoteAsset"`
	IsSpotTradingAllowed bool              `json:"isSpotTradingAllowed"`
	OrderTypes           []string          `json:"orderTypes"`
	Filters              []json.RawMessage `json:"filters"`
}

// FetchExchangeInfo fetches the symbol catalogue from baseURL using client.
// The caller supplies the HTTP client and host; failed requests are not
// retried.
func FetchExchangeInfo(
	ctx context.Context, client *http.Client, baseURL string,
) (ExchangeInfo, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, baseURL+ExchangeInfoPath, nil,
	)
	if err != nil {
		return ExchangeInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ExchangeInfo{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("binance exchangeInfo: close response body", "error", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return ExchangeInfo{}, fmt.Errorf(
			"binance exchangeInfo: unexpected status %d", resp.StatusCode,
		)
	}
	return decodeExchangeInfo(resp.Body)
}

// FetchSymbol retrieves one symbol from the supplied REST host. Only Binance's
// invalid-symbol response (HTTP 400, code -1121) means the symbol is absent;
// inconsistent catalogues and all other failures return an error.
func FetchSymbol(ctx context.Context, client *http.Client,
	baseURL, symbol string,
) (Symbol, bool, error) {
	endpoint := baseURL + ExchangeInfoPath + "?symbol=" + url.QueryEscape(symbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Symbol{}, false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Symbol{}, false, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("binance exchangeInfo: close response body", "error", err)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusBadRequest {
			var failure struct {
				Code int `json:"code"`
			}
			if json.NewDecoder(resp.Body).Decode(&failure) == nil &&
				failure.Code == -1121 {
				return Symbol{}, false, nil
			}
		}
		return Symbol{}, false, fmt.Errorf(
			"binance exchangeInfo: unexpected status %d", resp.StatusCode,
		)
	}
	info, err := decodeExchangeInfo(resp.Body)
	if err != nil {
		return Symbol{}, false, err
	}
	if len(info.Symbols) != 1 || info.Symbols[0].Symbol != symbol {
		return Symbol{}, false, fmt.Errorf(
			"binance exchangeInfo: expected exactly one matching symbol",
		)
	}
	return info.Symbols[0], true, nil
}

func decodeExchangeInfo(body io.Reader) (ExchangeInfo, error) {
	var info ExchangeInfo
	if err := json.NewDecoder(body).Decode(&info); err != nil {
		return ExchangeInfo{}, fmt.Errorf("binance exchangeInfo: decode: %w", err)
	}
	return info, nil
}
