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

package trading

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/secret"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/bybit"
	"go.openpit.dev/officer/internal/symbolsearch"
)

const (
	bybitHTTPTimeout       = 10 * time.Second
	bybitLookupMargin      = time.Minute
	bybitLookupWindow      = 7 * 24 * time.Hour
	bybitLookupOrderStatus = "An order lookup returned an order status Officer does not know."
	bybitLookupExtraFees   = "An order lookup returned unsupported extra fees."
	bybitEUSite            = "eu"
)

type bybitConnector struct {
	client             *bybit.Client
	httpClient         *http.Client
	credentials        bybit.Credentials
	site               string
	restURL, streamURL string
	redact             *strings.Replacer
	lifecycle          streamLifecycle
	assetsMu           sync.Mutex
	symbols            map[string]bybit.Instrument
	reportStatus       health.StatusReporter
	reportDiag         health.DiagnosticReporter
}

// NewBybitConnector connects spot trading to Bybit testnet in paper mode or
// to Bybit Global in live mode. Credentials can select the EU testnet.
func NewBybitConnector(connection domain.TradingConnection) (fwtrading.Connector, error) {
	switch connection.Mode {
	case domain.TradingModePaper:
		return newBybitConnector(connection, bybit.TestnetRESTURL,
			bybit.TestnetPrivateStreamURL)
	case domain.TradingModeLive:
		return newBybitConnector(connection, bybit.LiveRESTURL,
			bybit.LivePrivateStreamURL)
	default:
		return nil, errors.New("bybit: unsupported trading mode")
	}
}

func newBybitConnector(connection domain.TradingConnection,
	restURL, streamURL string,
) (*bybitConnector, error) {
	credentials, site, err := bybit.ParseCredentials(connection.Credentials)
	if err != nil {
		return nil, domain.WithValidationPointer(
			"/credentials"+domain.ValidationPointer(err), err)
	}
	switch site {
	case "", "global":
	case bybitEUSite:
		if connection.Mode != domain.TradingModePaper {
			return nil, domain.NewValidationError("/credentials/site", "unsupported_value",
				"bybit: the EU site is available in paper mode")
		}
		restURL, streamURL = bybit.EUTestnetRESTURL, bybit.EUTestnetPrivateStreamURL
	default:
		return nil, domain.NewValidationError("/credentials/site", "invalid",
			"bybit: site must be global or eu")
	}
	_, redact, err := secret.CredentialRedactor(connection.Credentials)
	if err != nil {
		return nil, fmt.Errorf("bybit: %w", err)
	}
	httpClient := &http.Client{Timeout: bybitHTTPTimeout}
	return &bybitConnector{
		client:     bybit.NewClient(restURL, credentials, redact, httpClient),
		httpClient: httpClient, credentials: credentials, site: site,
		restURL: restURL, streamURL: streamURL, redact: redact,
		symbols: make(map[string]bybit.Instrument),
	}, nil
}

func (c *bybitConnector) cacheSymbol(symbol bybit.Instrument) {
	if symbol.BaseCoin == "" || symbol.QuoteCoin == "" {
		return
	}
	c.assetsMu.Lock()
	defer c.assetsMu.Unlock()
	c.symbols[symbol.Symbol] = symbol
}

func (c *bybitConnector) fetchSymbol(ctx context.Context,
	symbol string,
) (bybit.InstrumentRules, bool, error) {
	rules, found, err := bybit.FetchInstrument(ctx, c.httpClient,
		c.restURL, "spot", symbol)
	if err != nil {
		return bybit.InstrumentRules{}, false, fmt.Errorf(
			"bybit: symbol lookup: %s: %w",
			c.redact.Replace(err.Error()), domain.ErrUpstream)
	}
	if found {
		c.cacheSymbol(rules.Instrument)
	}
	return rules, found, nil
}

func (c *bybitConnector) coins(ctx context.Context,
	symbol string,
) (string, string, error) {
	c.assetsMu.Lock()
	asset, found := c.symbols[symbol]
	c.assetsMu.Unlock()
	if !found {
		rules, exists, err := c.fetchSymbol(ctx, symbol)
		if err != nil {
			return "", "", err
		}
		asset, found = rules.Instrument, exists
	}
	if !found || asset.BaseCoin == "" || asset.QuoteCoin == "" {
		return "", "", fmt.Errorf("bybit: symbol assets are missing: %w",
			domain.ErrUpstream)
	}
	return asset.BaseCoin, asset.QuoteCoin, nil
}

func (c *bybitConnector) PreCheck(ctx context.Context, order fwtrading.Order) error {
	if _, err := buildBybitOrderRequest(order); err != nil {
		return err
	}
	rules, found, err := c.fetchSymbol(ctx, order.Symbol)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: symbol must allow spot trading", fwtrading.ErrRejected)
	}
	return checkBybitRules(order, rules)
}

func (c *bybitConnector) Send(ctx context.Context,
	order fwtrading.Order,
) (fwtrading.Ack, error) {
	request, err := buildBybitOrderRequest(order)
	if err != nil {
		return fwtrading.Ack{}, err
	}
	var record bybitOrder
	if _, err := c.client.Post(ctx, "/v5/order/create", request, &record); err != nil {
		return fwtrading.Ack{}, bybitSendError(err)
	}
	return bybitCreateOrderAck(record, order.ClientOrderID)
}

func (c *bybitConnector) Lookup(ctx context.Context,
	ref fwtrading.OrderRef,
) (fwtrading.Snapshot, bool, error) {
	if c.reportDiag == nil {
		return fwtrading.Snapshot{}, false, errors.New(
			"bybit: lookup requires a diagnostic reporter")
	}
	if ref.Symbol == "" {
		return fwtrading.Snapshot{}, false, errors.New("bybit: order symbol is required")
	}
	side, ok := bybitOrderSide(ref.Side)
	if !ok {
		return fwtrading.Snapshot{}, false, errors.New("bybit: order side is required")
	}
	if ref.ClientOrderID == "" && ref.VenueOrderID == "" {
		return fwtrading.Snapshot{}, false, errors.New("bybit: order reference is required")
	}
	params := url.Values{"category": {"spot"}, "symbol": {ref.Symbol}}
	if ref.ClientOrderID != "" {
		params.Set("orderLinkId", ref.ClientOrderID)
	} else {
		params.Set("orderId", ref.VenueOrderID)
	}
	record, found, err := c.lookupOrder(ctx, "/v5/order/realtime", params)
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	if !found {
		if ref.CreatedAt.IsZero() {
			return fwtrading.Snapshot{}, false, fmt.Errorf(
				"bybit: order creation time is required for history: %w", domain.ErrUpstream)
		}
		start := ref.CreatedAt.Add(-bybitLookupMargin)
		params.Set("startTime", strconv.FormatInt(start.UnixMilli(), 10))
		params.Set("endTime", strconv.FormatInt(start.Add(bybitLookupWindow).UnixMilli(), 10))
		record, found, err = c.lookupOrder(ctx, "/v5/order/history", params)
		if err != nil || !found {
			return fwtrading.Snapshot{}, false, err
		}
	}
	if record.Symbol != ref.Symbol || record.Side != side ||
		(ref.ClientOrderID != "" && record.OrderLinkID != ref.ClientOrderID) ||
		(ref.VenueOrderID != "" && record.OrderID != ref.VenueOrderID) {
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"bybit: lookup order reference mismatch: %w", domain.ErrUpstream)
	}
	filled, err := parsePlainDecimal(record.CumExecQty, false)
	if err != nil {
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"bybit: unreadable executed quantity: %w", domain.ErrUpstream)
	}
	var executions []bybitExecution
	if !filled.IsZero() {
		executions, err = c.executions(ctx, record)
		if err != nil {
			return fwtrading.Snapshot{}, false, err
		}
	}
	var base, quote string
	if len(executions) != 0 {
		base, quote, err = c.coins(ctx, ref.Symbol)
		if err != nil {
			return fwtrading.Snapshot{}, false, err
		}
	}
	snapshot, err := record.snapshot(executions, base, quote)
	if err != nil {
		switch {
		case errors.Is(err, errBybitOrderStatus):
			c.reportDiag(venueLookupUnparsable(bybitLookupOrderStatus))
		case errors.Is(err, errBybitExtraFees):
			c.reportDiag(venueLookupUnparsable(bybitLookupExtraFees))
		}
		return fwtrading.Snapshot{}, false, err
	}
	snapshot.Reason = c.redact.Replace(snapshot.Reason)
	return snapshot, true, nil
}

func (c *bybitConnector) lookupOrder(ctx context.Context, path string,
	params url.Values,
) (bybitOrder, bool, error) {
	var result struct {
		List []bybitOrder `json:"list"`
	}
	if _, err := c.client.Get(ctx, path, params, &result); err != nil {
		return bybitOrder{}, false, err
	}
	if result.List == nil || len(result.List) > 1 {
		return bybitOrder{}, false, fmt.Errorf(
			"bybit: lookup requires a list with at most one order: %w", domain.ErrUpstream)
	}
	if len(result.List) == 0 {
		return bybitOrder{}, false, nil
	}
	return result.List[0], true, nil
}

func (c *bybitConnector) executions(ctx context.Context,
	order bybitOrder,
) ([]bybitExecution, error) {
	ms, ok := bybitMilliseconds(order.CreatedTime)
	if !ok {
		return nil, fmt.Errorf("bybit: unreadable order creation time: %w", domain.ErrUpstream)
	}
	start := time.UnixMilli(ms).Add(-bybitLookupMargin).UnixMilli()
	until := time.Now().Add(bybitLookupMargin).UnixMilli()
	if start > until {
		return nil, fmt.Errorf("bybit: order creation time is in the future: %w", domain.ErrUpstream)
	}
	params := url.Values{"category": {"spot"}, "symbol": {order.Symbol},
		"orderId": {order.OrderID}, "limit": {"100"}}
	var executions []bybitExecution
	for start <= until {
		end := min(start+bybitLookupWindow.Milliseconds(), until)
		params.Set("startTime", strconv.FormatInt(start, 10))
		params.Set("endTime", strconv.FormatInt(end, 10))
		params.Del("cursor")
		seen := make(map[string]bool)
		for {
			var page struct {
				List           []bybitExecution `json:"list"`
				NextPageCursor string           `json:"nextPageCursor"`
			}
			if _, err := c.client.Get(ctx, "/v5/execution/list", params, &page); err != nil {
				return nil, err
			}
			if page.List == nil || (page.NextPageCursor != "" && seen[page.NextPageCursor]) {
				return nil, fmt.Errorf("bybit: unreadable executions or pagination: %w", domain.ErrUpstream)
			}
			executions = append(executions, page.List...)
			if page.NextPageCursor == "" {
				break
			}
			seen[page.NextPageCursor] = true
			params.Set("cursor", page.NextPageCursor)
		}
		// Timestamps are inclusive milliseconds; do not leave a boundary gap.
		start = end + 1
	}
	return executions, nil
}

func (c *bybitConnector) CheckAccount(ctx context.Context) (fwtrading.AccountInfo, error) {
	var account struct {
		ReadOnly    *int `json:"readOnly"`
		Permissions struct {
			Spot []string `json:"Spot"`
		} `json:"permissions"`
		UTA    *int            `json:"uta"`
		UserID json.RawMessage `json:"userID"`
	}
	if _, err := c.client.Get(ctx, "/v5/user/query-api", nil, &account); err != nil {
		return fwtrading.AccountInfo{}, err
	}
	id := string(account.UserID)
	if !bybitDigits(id) || strings.Trim(id, "0") == "" {
		return fwtrading.AccountInfo{}, fmt.Errorf("bybit: account has no usable user id: %w", domain.ErrUpstream)
	}
	if account.UTA == nil || (*account.UTA != 0 && *account.UTA != 1) ||
		account.ReadOnly == nil || (*account.ReadOnly != 0 && *account.ReadOnly != 1) ||
		account.Permissions.Spot == nil {
		return fwtrading.AccountInfo{}, fmt.Errorf("bybit: unreadable account status: %w", domain.ErrUpstream)
	}
	status := "classic"
	if *account.UTA == 1 {
		status = "unified"
	}
	if *account.ReadOnly == 1 {
		status += ", read-only"
	}
	if !slices.Contains(account.Permissions.Spot, "SpotTrade") {
		status += ", spot trading not permitted"
	}
	if c.site == bybitEUSite {
		id = "bybit.eu:" + id
	}
	return fwtrading.AccountInfo{Identity: id, Status: status}, nil
}

func (c *bybitConnector) VerifySymbol(ctx context.Context,
	external string,
) (fwtrading.SymbolVerification, error) {
	symbol, found, err := c.fetchSymbol(ctx, external)
	if err != nil || !found {
		return fwtrading.SymbolVerification{}, err
	}
	return fwtrading.SymbolVerification{Exists: true,
		Tradable: symbol.Status == "Trading",
		Details:  c.redact.Replace(symbol.BaseCoin + "/" + symbol.QuoteCoin + " " + symbol.Status),
	}, nil
}

func (c *bybitConnector) SearchSymbols(ctx context.Context,
	query string,
) ([]fwtrading.SymbolMatch, error) {
	if strings.TrimSpace(query) == "" {
		return []fwtrading.SymbolMatch{}, nil
	}
	catalogue, err := bybit.FetchInstruments(ctx, c.httpClient, c.restURL, "spot")
	if err != nil {
		return nil, fmt.Errorf("bybit: catalogue: %s: %w", c.redact.Replace(err.Error()), domain.ErrUpstream)
	}
	known := make(map[string]bybit.Instrument)
	for _, symbol := range catalogue {
		c.cacheSymbol(symbol)
		known[symbol.Symbol] = symbol
	}
	matches := make([]fwtrading.SymbolMatch, 0)
	for _, name := range symbolsearch.Rank(known, query) {
		symbol := known[name]
		matches = append(matches, fwtrading.SymbolMatch{
			Symbol: symbol.Symbol, Name: symbol.BaseCoin + "/" + symbol.QuoteCoin,
			Class: "crypto", Exchange: "Bybit", BaseAsset: symbol.BaseCoin,
			QuoteAsset: symbol.QuoteCoin, Tradable: symbol.Status == "Trading",
		})
	}
	return matches, nil
}
