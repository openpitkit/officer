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

package openalgo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// SymbolForm describes the supported NSE/BSE external symbol form.
const SymbolForm = "OpenAlgo symbol must be NSE:SYMBOL or BSE:SYMBOL without surrounding whitespace"

var spotExchanges = [...]string{"NSE", "BSE"}

// ParseSpotSymbol accepts NSE/BSE symbols without surrounding whitespace.
func ParseSpotSymbol(external string) (Instrument, error) {
	exchange, symbol, ok := strings.Cut(external, ":")
	if !ok || exchange == "" || symbol == "" ||
		exchange != strings.TrimSpace(exchange) || symbol != strings.TrimSpace(symbol) {
		return Instrument{}, errors.New(SymbolForm)
	}
	for _, supported := range spotExchanges {
		if exchange == supported {
			return Instrument{Exchange: exchange, Symbol: symbol}, nil
		}
	}
	return Instrument{}, errors.New(SymbolForm)
}

func (i Instrument) String() string {
	return i.Exchange + ":" + i.Symbol
}

// VerifySpotSymbol looks up an NSE/BSE symbol and describes its trading units.
// Malformed symbols and missing instruments return exists=false.
func VerifySpotSymbol(ctx context.Context, client *Client, external string) (exists bool, details string, err error) {
	instrument, err := ParseSpotSymbol(external)
	if err != nil {
		return false, SymbolForm, nil
	}
	info, err := client.Symbol(ctx, instrument.Symbol, instrument.Exchange)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return false, "", nil
		}
		return false, "", err
	}
	return true, fmt.Sprintf("%s; lot size %s; tick size %s", info.Name, info.LotSize, info.TickSize), nil
}

// SearchSpotSymbols searches NSE/BSE, or the exchange prefix in query.
// Returned rows retain the vendor's fields for each caller's result mapping.
func SearchSpotSymbols(ctx context.Context, client *Client, query string) ([]SymbolInfo, error) {
	query = strings.TrimSpace(query)
	matches := make([]SymbolInfo, 0)
	if query == "" {
		return matches, nil
	}
	exchanges := spotExchanges[:]
	for _, exchange := range spotExchanges {
		if prefix := exchange + ":"; strings.HasPrefix(query, prefix) {
			exchanges = []string{exchange}
			query = strings.TrimSpace(strings.TrimPrefix(query, prefix))
			if query == "" {
				return matches, nil
			}
			break
		}
	}
	for _, exchange := range exchanges {
		rows, err := client.Search(ctx, query, exchange)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Exchange != exchange {
				return nil, errors.New("openalgo: search result exchange differs from the requested exchange")
			}
			matches = append(matches, row)
		}
	}
	return matches, nil
}
