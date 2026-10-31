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
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/secret"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/binance"
	"go.openpit.dev/officer/internal/symbolsearch"
)

const (
	binanceHTTPTimeout      = 10 * time.Second
	binanceDecimalMaxLength = 40
	binanceTradePageSize    = 1000
)

type binanceAssets struct{ base, quote string }

type binanceConnector struct {
	client      *binance.Client
	httpClient  *http.Client
	credentials binance.Credentials
	restURL     string
	streamURL   string
	redact      *strings.Replacer
	lifecycle   streamLifecycle
	assetsMu    sync.Mutex
	symbols     map[string]binanceAssets

	reportStatus health.StatusReporter
	reportDiag   health.DiagnosticReporter
}

var (
	_ fwtrading.Connector        = (*binanceConnector)(nil)
	_ fwtrading.PreChecker       = (*binanceConnector)(nil)
	_ fwtrading.AccountChecker   = (*binanceConnector)(nil)
	_ fwtrading.SymbolSearcher   = (*binanceConnector)(nil)
	_ fwtrading.SymbolVerifier   = (*binanceConnector)(nil)
	_ health.StatusReporting     = (*binanceConnector)(nil)
	_ health.DiagnosticReporting = (*binanceConnector)(nil)
)

// NewBinanceConnector selects testnet or live endpoints explicitly from Mode.
// Construction validates credentials without making a network request.
func NewBinanceConnector(
	connection domain.TradingConnection,
) (fwtrading.Connector, error) {
	var restURL, streamURL string
	switch connection.Mode {
	case domain.TradingModePaper:
		restURL, streamURL = binance.TestnetRESTURL, binance.TestnetStreamURL
	case domain.TradingModeLive:
		restURL, streamURL = binance.LiveRESTURL, binance.LiveStreamURL
	default:
		return nil, errors.New("binance: unsupported trading mode")
	}
	return newBinanceConnector(connection, restURL, streamURL)
}

func newBinanceConnector(connection domain.TradingConnection,
	restURL, streamURL string,
) (*binanceConnector, error) {
	credentials, err := binance.ParseCredentials(connection.Credentials)
	if err != nil {
		pointer := "/credentials" + domain.ValidationPointer(err)
		return nil, domain.WithValidationPointer(pointer, err)
	}
	_, redact, err := secret.CredentialRedactor(connection.Credentials)
	if err != nil {
		return nil, fmt.Errorf("binance: %w", err)
	}
	httpClient := &http.Client{Timeout: binanceHTTPTimeout}
	return &binanceConnector{
		client:     binance.NewClient(restURL, credentials, redact, httpClient),
		httpClient: httpClient, credentials: credentials,
		restURL: restURL, streamURL: streamURL, redact: redact,
		symbols: make(map[string]binanceAssets),
	}, nil
}

func parseBinanceRoute(raw string, limit bool) (string, error) {
	var route map[string]string
	if json.Unmarshal([]byte(raw), &route) != nil || route == nil {
		return "", fmt.Errorf("%w: invalid Binance route JSON",
			fwtrading.ErrRejected)
	}
	for field := range route {
		if field != "timeInForce" {
			return "", fmt.Errorf("%w: unknown Binance route field",
				fwtrading.ErrRejected)
		}
	}
	tif, present := route["timeInForce"]
	if !limit {
		if present {
			return "", fmt.Errorf("%w: market route must be empty",
				fwtrading.ErrRejected)
		}
		return "", nil
	}
	switch tif {
	case "GTC", "IOC", "FOK":
		return tif, nil
	default:
		return "", fmt.Errorf("%w: limit timeInForce must be GTC, IOC or FOK",
			fwtrading.ErrRejected)
	}
}

func parseBinanceDecimal(raw string, positive bool) (decimal.Decimal, error) {
	if len(raw) > binanceDecimalMaxLength {
		return decimal.Decimal{}, errors.New("decimal exceeds length limit")
	}
	if strings.HasPrefix(raw, "+") {
		return decimal.Decimal{}, errors.New("invalid plain decimal")
	}
	value, err := domain.ParseOpenQuantity(raw)
	if err != nil || (positive && !value.IsPositive()) {
		return decimal.Decimal{}, errors.New("invalid plain decimal")
	}
	return value, nil
}

func (c *binanceConnector) cacheSymbol(symbol binance.Symbol) {
	if symbol.BaseAsset == "" || symbol.QuoteAsset == "" {
		return
	}
	c.assetsMu.Lock()
	defer c.assetsMu.Unlock()
	if _, exists := c.symbols[symbol.Symbol]; !exists {
		c.symbols[symbol.Symbol] = binanceAssets{
			base: symbol.BaseAsset, quote: symbol.QuoteAsset,
		}
	}
}

func (c *binanceConnector) fetchSymbol(ctx context.Context,
	symbol string,
) (binance.Symbol, bool, error) {
	asset, found, err := binance.FetchSymbol(ctx, c.httpClient,
		c.restURL, symbol)
	if err != nil {
		return binance.Symbol{}, false, fmt.Errorf(
			"binance: symbol lookup: %s: %w",
			c.redact.Replace(err.Error()), domain.ErrUpstream)
	}
	if found {
		c.cacheSymbol(asset)
	}
	return asset, found, nil
}

// PreCheck checks the spot rules whose inputs are known before submission.
// Unchecked venue filters and market notional are left to the venue's
// definitive refusal in Send; no reservation is made by this connector.
func (c *binanceConnector) PreCheck(ctx context.Context,
	order fwtrading.Order,
) error {
	limit := order.LimitPrice != ""
	if _, err := parseBinanceRoute(order.Route, limit); err != nil {
		return err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fmt.Errorf("%w: Binance requires quantity orders",
			fwtrading.ErrUnsupported)
	}
	symbol, found, err := c.fetchSymbol(ctx, order.Symbol)
	if err != nil {
		return err
	}
	if !found || symbol.Status != "TRADING" || !symbol.IsSpotTradingAllowed {
		return fmt.Errorf("%w: symbol must allow spot trading",
			fwtrading.ErrRejected)
	}
	orderType := "MARKET"
	if limit {
		orderType = "LIMIT"
	}
	if !slices.Contains(symbol.OrderTypes, orderType) {
		return fmt.Errorf("%w: symbol does not support the order type",
			fwtrading.ErrRejected)
	}
	qty, err := parseBinanceDecimal(order.Quantity, true)
	if err != nil {
		return fmt.Errorf("%w: quantity must be a positive plain decimal",
			fwtrading.ErrRejected)
	}
	var price decimal.Decimal
	if limit {
		price, err = parseBinanceDecimal(order.LimitPrice, true)
		if err != nil {
			return fmt.Errorf("%w: price must be a positive plain decimal",
				fwtrading.ErrRejected)
		}
	}
	for _, raw := range symbol.Filters {
		if err := checkBinanceFilter(raw, limit, qty, price); err != nil {
			return err
		}
	}
	return nil
}

func checkBinanceFilter(raw json.RawMessage, limit bool,
	qty, price decimal.Decimal,
) error {
	var fields map[string]json.RawMessage
	var filterType string
	if json.Unmarshal(raw, &fields) != nil ||
		json.Unmarshal(fields["filterType"], &filterType) != nil ||
		filterType == "" {
		return fmt.Errorf("binance: unreadable filter type: %w",
			domain.ErrUpstream)
	}
	read := func(name string) (decimal.Decimal, error) {
		var text string
		if json.Unmarshal(fields[name], &text) == nil {
			if value, err := parseBinanceDecimal(text, false); err == nil {
				return value, nil
			}
		}
		return decimal.Decimal{}, fmt.Errorf(
			"binance: invalid %s %s: %w", filterType, name,
			domain.ErrUpstream)
	}
	var value decimal.Decimal
	var minimum, maximum, step string
	switch {
	case filterType == "PRICE_FILTER" && limit:
		value = price
		minimum, maximum, step = "minPrice", "maxPrice", "tickSize"
	case filterType == "LOT_SIZE" && limit:
		value = qty
		minimum, maximum, step = "minQty", "maxQty", "stepSize"
	case filterType == "MARKET_LOT_SIZE" && !limit:
		value = qty
		minimum, maximum, step = "minQty", "maxQty", "stepSize"
	case (filterType == "NOTIONAL" || filterType == "MIN_NOTIONAL") &&
		limit:
		min, err := read("minNotional")
		if err != nil {
			return err
		}
		notional := price.Mul(qty)
		var max decimal.Decimal
		if filterType == "NOTIONAL" && fields["maxNotional"] != nil {
			max, err = read("maxNotional")
			if err != nil {
				return err
			}
		}
		if notional.LessThan(min) ||
			(max.IsPositive() && notional.GreaterThan(max)) {
			return fmt.Errorf("%w: notional is outside venue bounds",
				fwtrading.ErrRejected)
		}
		return nil
	default:
		return nil
	}
	increment, err := read(step)
	if err != nil {
		return err
	}
	if filterType == "MARKET_LOT_SIZE" && increment.IsZero() {
		return nil
	}
	min, err := read(minimum)
	if err != nil {
		return err
	}
	max, err := read(maximum)
	if err != nil {
		return err
	}
	if value.LessThan(min) || (max.IsPositive() && value.GreaterThan(max)) {
		return fmt.Errorf("%w: value is outside %s bounds",
			fwtrading.ErrRejected, filterType)
	}
	if increment.IsPositive() && !value.Sub(min).Mod(increment).IsZero() {
		return fmt.Errorf("%w: value is not a %s increment",
			fwtrading.ErrRejected, filterType)
	}
	return nil
}

type binanceOrder struct {
	ID            json.Number `json:"orderId"`
	ClientOrderID string      `json:"clientOrderId"`
	Status        string      `json:"status"`
	Quantity      string      `json:"origQty"`
	Filled        string      `json:"executedQty"`
}

func binanceID(id json.Number) bool {
	if id == "" {
		return false
	}
	for _, char := range id.String() {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func (c *binanceConnector) Send(ctx context.Context,
	order fwtrading.Order,
) (fwtrading.Ack, error) {
	limit := order.LimitPrice != ""
	tif, err := parseBinanceRoute(order.Route, limit)
	if err != nil {
		return fwtrading.Ack{}, err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fwtrading.Ack{}, fmt.Errorf("%w: Binance requires quantity orders",
			fwtrading.ErrUnsupported)
	}
	side := strings.ToUpper(string(order.Side))
	if side != "BUY" && side != "SELL" {
		return fwtrading.Ack{}, fmt.Errorf("%w: unsupported order side",
			fwtrading.ErrRejected)
	}
	params := url.Values{
		"symbol": {order.Symbol}, "side": {side}, "type": {"MARKET"},
		"quantity": {order.Quantity}, "newClientOrderId": {order.ClientOrderID},
		"newOrderRespType": {"ACK"},
	}
	if limit {
		params.Set("type", "LIMIT")
		params.Set("timeInForce", tif)
		params.Set("price", order.LimitPrice)
	}
	var response binanceOrder
	_, err = c.client.Signed(ctx, http.MethodPost, "/api/v3/order",
		params, &response)
	if err != nil {
		var apiErr *binance.APIError
		if errors.As(err, &apiErr) && apiErr.BodyDecoded &&
			apiErr.Status >= 400 && apiErr.Status < 500 &&
			apiErr.Status != 408 &&
			apiErr.Code != -1006 && apiErr.Code != -1007 {
			duplicate := apiErr.Code == -2010 && strings.Contains(
				strings.ToLower(apiErr.Message), "duplicate order sent")
			if !duplicate {
				return fwtrading.Ack{}, fmt.Errorf("%w: %w",
					fwtrading.ErrRejected, err)
			}
		}
		return fwtrading.Ack{}, fmt.Errorf(
			"binance: send outcome unknown: %w", err)
	}
	if !binanceID(response.ID) ||
		response.ClientOrderID != order.ClientOrderID {
		return fwtrading.Ack{}, errors.New(
			"binance: send outcome unknown: unusable acknowledgement")
	}
	return fwtrading.Ack{VenueOrderID: response.ID.String()}, nil
}

func binanceVenueStatus(status string) (fwtrading.VenueStatus, bool) {
	switch status {
	case "NEW", "PENDING_NEW", "PARTIALLY_FILLED", "PENDING_CANCEL":
		return fwtrading.VenueStatusOpen, true
	case "FILLED":
		return fwtrading.VenueStatusFilled, true
	case "CANCELED", "REJECTED", "EXPIRED", "EXPIRED_IN_MATCH":
		return fwtrading.VenueStatusCancelled, true
	default:
		return "", false
	}
}

func (c *binanceConnector) Lookup(ctx context.Context,
	ref fwtrading.OrderRef,
) (fwtrading.Snapshot, bool, error) {
	if c.reportDiag == nil {
		return fwtrading.Snapshot{}, false, errors.New(
			"binance: lookup requires a diagnostic reporter")
	}
	if ref.Symbol == "" {
		return fwtrading.Snapshot{}, false, errors.New(
			"binance: order symbol is required")
	}
	params := url.Values{"symbol": {ref.Symbol}}
	// Send both IDs to reject a different order after a symbol remap.
	if ref.VenueOrderID != "" {
		params.Set("orderId", ref.VenueOrderID)
	}
	if ref.ClientOrderID != "" {
		params.Set("origClientOrderId", ref.ClientOrderID)
	}
	if ref.VenueOrderID == "" && ref.ClientOrderID == "" {
		return fwtrading.Snapshot{}, false, errors.New(
			"binance: order reference is required")
	}
	var order binanceOrder
	_, err := c.client.Signed(ctx, http.MethodGet, "/api/v3/order",
		params, &order)
	var apiErr *binance.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 400 &&
		apiErr.BodyDecoded && apiErr.Code == -2013 {
		return fwtrading.Snapshot{}, false, nil
	}
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	if !binanceID(order.ID) {
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"binance: lookup has no usable order id: %w", domain.ErrUpstream)
	}
	status, known := binanceVenueStatus(order.Status)
	if !known {
		c.reportDiag(binanceLookupUnparsable())
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"binance: unknown venue order status %q: %w",
			c.redact.Replace(order.Status), domain.ErrUpstream)
	}
	qty, qtyErr := parseBinanceDecimal(order.Quantity, true)
	filled, filledErr := parseBinanceDecimal(order.Filled, false)
	if qtyErr != nil || filledErr != nil || filled.GreaterThan(qty) {
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"binance: unreadable lookup quantities: %w", domain.ErrUpstream)
	}
	trades, err := c.trades(ctx, ref.Symbol, order.ID.String())
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	snapshot := fwtrading.Snapshot{
		VenueOrderID: order.ID.String(), Status: status,
		FilledQuantity: order.Filled,
	}
	cumulative := decimal.Zero
	for _, trade := range trades {
		amount, amountErr := parseBinanceDecimal(trade.Quantity, true)
		_, priceErr := parseBinanceDecimal(trade.Price, true)
		if amountErr != nil || priceErr != nil || trade.At <= 0 {
			return fwtrading.Snapshot{}, false, fmt.Errorf(
				"binance: unreadable trade: %w", domain.ErrUpstream)
		}
		cumulative = cumulative.Add(amount)
		leaves := qty.Sub(cumulative)
		if leaves.IsNegative() {
			return fwtrading.Snapshot{}, false, fmt.Errorf(
				"binance: trades exceed order quantity: %w", domain.ErrUpstream)
		}
		commission, leg, err := c.commission(ctx, ref.Symbol,
			trade.Commission, trade.CommissionAsset)
		if err != nil {
			return fwtrading.Snapshot{}, false, err
		}
		snapshot.Fills = append(snapshot.Fills, fwtrading.Fill{
			Quantity: trade.Quantity, Price: trade.Price,
			CumQuantity: cumulative.String(), LeavesQuantity: leaves.String(),
			Final: cumulative.Equal(qty), At: time.UnixMilli(trade.At).UTC(),
			Commission: commission, CommissionLeg: leg,
		})
	}
	return snapshot, true, nil
}

func binanceLookupUnparsable() health.Diagnostic {
	return health.Diagnostic{
		Level: health.DiagError, Code: health.CodeUnparsableData,
		Kind: health.DiagKindProvider, Title: "Unknown order data from the venue",
		Detail: "An order lookup returned an order status Officer does not know.",
		Remediation: "Officer keeps the order pending and retries the " +
			"lookup; the order is not reconciled until the venue reports " +
			"data Officer knows. This is a venue or Officer issue, not your " +
			"configuration - please report it.",
	}
}

type binanceTrade struct {
	ID              *int64      `json:"id"`
	OrderID         json.Number `json:"orderId"`
	Quantity        string      `json:"qty"`
	Price           string      `json:"price"`
	Commission      string      `json:"commission"`
	CommissionAsset string      `json:"commissionAsset"`
	At              int64       `json:"time"`
}

func (c *binanceConnector) trades(ctx context.Context,
	symbol, orderID string,
) ([]binanceTrade, error) {
	params := url.Values{
		"symbol": {symbol}, "orderId": {orderID}, "limit": {"1000"},
		"fromId": {"0"},
	}
	var trades []binanceTrade
	seen := make(map[int64]bool)
	var cursor int64
	for {
		var page []binanceTrade
		_, err := c.client.Signed(ctx, http.MethodGet, "/api/v3/myTrades",
			params, &page)
		if err != nil {
			return nil, fmt.Errorf("binance: trade lookup: %w: %w",
				domain.ErrUpstream, err)
		}
		last := cursor
		for _, trade := range page {
			if trade.ID == nil || *trade.ID < cursor || seen[*trade.ID] ||
				trade.OrderID.String() != orderID {
				return nil, fmt.Errorf(
					"binance: inconsistent trade ids or pagination: %w",
					domain.ErrUpstream)
			}
			seen[*trade.ID] = true
			if *trade.ID > last {
				last = *trade.ID
			}
		}
		trades = append(trades, page...)
		if len(page) < binanceTradePageSize {
			break
		}
		if last == math.MaxInt64 {
			return nil, fmt.Errorf("binance: trade cursor exhausted: %w",
				domain.ErrUpstream)
		}
		cursor = last + 1
		params.Set("fromId", strconv.FormatInt(cursor, 10))
	}
	sort.Slice(trades, func(i, j int) bool {
		return *trades[i].ID < *trades[j].ID
	})
	return trades, nil
}

func (c *binanceConnector) commission(ctx context.Context,
	symbol, amount, asset string,
) (*domain.Commission, fwtrading.CommissionLeg, error) {
	value, err := parseBinanceDecimal(amount, false)
	if err != nil || (asset == "" && !value.IsZero()) {
		return nil, "", fmt.Errorf("binance: unreadable commission: %w",
			domain.ErrUpstream)
	}
	if value.IsZero() {
		return nil, "", nil
	}
	c.assetsMu.Lock()
	assets, found := c.symbols[symbol]
	c.assetsMu.Unlock()
	if !found {
		if _, found, err = c.fetchSymbol(ctx, symbol); err != nil {
			return nil, "", err
		}
		c.assetsMu.Lock()
		assets = c.symbols[symbol]
		c.assetsMu.Unlock()
	}
	if !found || assets.base == "" || assets.quote == "" {
		return nil, "", fmt.Errorf("binance: symbol assets are missing: %w",
			domain.ErrUpstream)
	}
	var leg fwtrading.CommissionLeg
	switch asset {
	case assets.base:
		leg = fwtrading.CommissionLegBase
	case assets.quote:
		leg = fwtrading.CommissionLegQuote
	}
	return &domain.Commission{Amount: amount, Currency: asset}, leg, nil
}

func (c *binanceConnector) CheckAccount(
	ctx context.Context,
) (fwtrading.AccountInfo, error) {
	account, err := c.client.Account(ctx)
	if err != nil {
		return fwtrading.AccountInfo{}, err
	}
	if !binanceID(account.UID) {
		return fwtrading.AccountInfo{}, fmt.Errorf(
			"binance: account has no usable uid: %w", domain.ErrUpstream)
	}
	status := account.AccountType
	if !account.CanTrade {
		status += ", trading disabled"
	}
	return fwtrading.AccountInfo{
		Identity: account.UID.String(), Status: status,
	}, nil
}

func (c *binanceConnector) VerifySymbol(ctx context.Context,
	external string,
) (fwtrading.SymbolVerification, error) {
	symbol, found, err := c.fetchSymbol(ctx, external)
	if err != nil || !found {
		return fwtrading.SymbolVerification{}, err
	}
	return fwtrading.SymbolVerification{
		Exists:   true,
		Tradable: symbol.Status == "TRADING" && symbol.IsSpotTradingAllowed,
		Details:  symbol.BaseAsset + "/" + symbol.QuoteAsset + " " + symbol.Status,
	}, nil
}

func (c *binanceConnector) SearchSymbols(ctx context.Context,
	query string,
) ([]fwtrading.SymbolMatch, error) {
	if strings.TrimSpace(query) == "" {
		return []fwtrading.SymbolMatch{}, nil
	}
	info, err := binance.FetchExchangeInfo(ctx, c.httpClient, c.restURL)
	if err != nil {
		return nil, fmt.Errorf("binance: catalogue: %s: %w",
			c.redact.Replace(err.Error()), domain.ErrUpstream)
	}
	known := make(map[string]binance.Symbol)
	for _, symbol := range info.Symbols {
		c.cacheSymbol(symbol)
		if symbol.IsSpotTradingAllowed {
			known[symbol.Symbol] = symbol
		}
	}
	matches := make([]fwtrading.SymbolMatch, 0)
	for _, name := range symbolsearch.Rank(known, query) {
		symbol := known[name]
		matches = append(matches, fwtrading.SymbolMatch{
			Symbol: symbol.Symbol, Name: symbol.BaseAsset + "/" + symbol.QuoteAsset,
			Class: "crypto", Exchange: "Binance",
			BaseAsset: symbol.BaseAsset, QuoteAsset: symbol.QuoteAsset,
			Tradable: symbol.Status == "TRADING" && symbol.IsSpotTradingAllowed,
		})
	}
	return matches, nil
}
