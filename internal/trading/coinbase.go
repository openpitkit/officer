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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/secret"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/coinbase"
	"go.openpit.dev/officer/internal/symbolsearch"
)

const coinbaseHTTPTimeout = 10 * time.Second

type coinbaseConnector struct {
	client       *coinbase.Client
	credentials  coinbase.Credentials
	streamURL    string
	lifecycle    streamLifecycle
	assetsMu     sync.Mutex
	symbols      map[string]coinbaseProduct
	reportStatus health.StatusReporter
	reportDiag   health.DiagnosticReporter
}

// NewCoinbaseConnector builds a live spot connector without network access.
func NewCoinbaseConnector(connection domain.TradingConnection) (fwtrading.Connector, error) {
	return newCoinbaseConnector(connection, coinbase.LiveRESTURL, coinbase.LiveStreamURL)
}

func newCoinbaseConnector(connection domain.TradingConnection, restURL, streamURL string) (*coinbaseConnector, error) {
	if connection.Mode != domain.TradingModeLive {
		return nil, domain.NewValidationError("/mode", "unsupported_value",
			"coinbase: trading provider supports live mode only")
	}
	credentials, err := coinbase.ParseCredentials(connection.Credentials)
	if err != nil {
		return nil, domain.WithValidationPointer("/credentials"+domain.ValidationPointer(err), err)
	}
	_, redact, err := secret.CredentialRedactor(connection.Credentials)
	if err != nil {
		return nil, fmt.Errorf("coinbase: %w", err)
	}
	return &coinbaseConnector{
		client:      coinbase.NewClient(restURL, credentials, redact, &http.Client{Timeout: coinbaseHTTPTimeout}),
		credentials: credentials, streamURL: streamURL,
		symbols: make(map[string]coinbaseProduct),
	}, nil
}

var (
	_ fwtrading.Connector        = (*coinbaseConnector)(nil)
	_ fwtrading.PreChecker       = (*coinbaseConnector)(nil)
	_ fwtrading.AccountChecker   = (*coinbaseConnector)(nil)
	_ fwtrading.SymbolSearcher   = (*coinbaseConnector)(nil)
	_ fwtrading.SymbolVerifier   = (*coinbaseConnector)(nil)
	_ health.StatusReporting     = (*coinbaseConnector)(nil)
	_ health.DiagnosticReporting = (*coinbaseConnector)(nil)
)

func (c *coinbaseConnector) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	_, err := c.client.Do(ctx, method, path, query, body, out)
	return err
}

func (c *coinbaseConnector) upstream(detail string) error {
	return fmt.Errorf("coinbase: %s: %w", c.client.Scrub(detail), domain.ErrUpstream)
}

type coinbaseProduct struct {
	ID              string `json:"product_id"`
	Type            string `json:"product_type"`
	Status          string `json:"status"`
	Base            string `json:"base_currency_id"`
	Quote           string `json:"quote_currency_id"`
	BaseIncrement   string `json:"base_increment"`
	BaseMin         string `json:"base_min_size"`
	BaseMax         string `json:"base_max_size"`
	PriceIncrement  string `json:"price_increment"`
	QuoteMin        string `json:"quote_min_size"`
	TradingDisabled bool   `json:"trading_disabled"`
	Disabled        bool   `json:"is_disabled"`
	CancelOnly      bool   `json:"cancel_only"`
	ViewOnly        bool   `json:"view_only"`
	LimitOnly       bool   `json:"limit_only"`
	PostOnly        bool   `json:"post_only"`
}

func (p coinbaseProduct) tradable() bool {
	// The status enum and unknown-product 404 are undocumented. Only online
	// is allowed; the live read-only acceptance check pins both assumptions.
	return p.Type == "SPOT" && p.Status == "online" && !p.TradingDisabled &&
		!p.Disabled && !p.CancelOnly && !p.ViewOnly
}

func (c *coinbaseConnector) cacheSymbol(p coinbaseProduct) {
	c.assetsMu.Lock()
	defer c.assetsMu.Unlock()
	c.symbols[p.ID] = p
}

func coinbaseNotFound(err error) bool {
	var apiErr *coinbase.APIError
	return errors.As(err, &apiErr) && apiErr.BodyDecoded && apiErr.Status == http.StatusNotFound
}

func (c *coinbaseConnector) fetchSymbol(ctx context.Context, symbol string) (coinbaseProduct, bool, error) {
	var p coinbaseProduct
	err := c.do(ctx, http.MethodGet, "/api/v3/brokerage/products/"+url.PathEscape(symbol),
		url.Values{"get_tradability_status": {"true"}}, nil, &p)
	// A decoded 404 for an unknown product is pinned by live acceptance;
	// the operation reference documents only 200 and a generic error.
	if coinbaseNotFound(err) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if p.ID != symbol {
		return p, false, c.upstream("product id does not match the requested symbol")
	}
	c.cacheSymbol(p)
	return p, true, nil
}

func (c *coinbaseConnector) PreCheck(ctx context.Context, order fwtrading.Order) error {
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fmt.Errorf("%w: Coinbase requires quantity orders", fwtrading.ErrUnsupported)
	}
	limit := order.LimitPrice != ""
	if _, err := parseVenueRoute(order.Route, limit, "Coinbase"); err != nil {
		return err
	}
	p, found, err := c.fetchSymbol(ctx, order.Symbol)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: unknown Coinbase symbol", fwtrading.ErrRejected)
	}
	if !p.tradable() {
		return fmt.Errorf("%w: symbol must allow online spot trading (status %q)", fwtrading.ErrRejected, c.client.Scrub(p.Status))
	}
	if p.PostOnly || (!limit && p.LimitOnly) {
		return fmt.Errorf("%w: symbol does not support the order type", fwtrading.ErrRejected)
	}
	qty, qtyErr := domain.ParseOpenQuantity(order.Quantity)
	if qtyErr != nil || !qty.IsPositive() {
		return fmt.Errorf("%w: quantity must be positive", fwtrading.ErrRejected)
	}
	step, stepErr := domain.ParseOpenQuantity(p.BaseIncrement)
	minQty, minErr := domain.ParseOpenQuantity(p.BaseMin)
	maxQty, maxErr := domain.ParseOpenQuantity(p.BaseMax)
	minQuote, quoteErr := domain.ParseOpenQuantity(p.QuoteMin)
	if stepErr != nil || minErr != nil || maxErr != nil || quoteErr != nil ||
		!step.IsPositive() || !maxQty.IsPositive() || minQty.GreaterThan(maxQty) {
		return c.upstream("unreadable product decimals")
	}
	if !qty.Mod(step).IsZero() || qty.LessThan(minQty) || qty.GreaterThan(maxQty) {
		return fmt.Errorf("%w: quantity violates product increment or bounds", fwtrading.ErrRejected)
	}
	if limit {
		priceStep, priceErr := domain.ParseOpenQuantity(p.PriceIncrement)
		if priceErr != nil || !priceStep.IsPositive() {
			return c.upstream("unreadable product decimals")
		}
		price, err := domain.ParseOpenQuantity(order.LimitPrice)
		if err != nil || !price.IsPositive() || !price.Mod(priceStep).IsZero() {
			return fmt.Errorf("%w: price violates product increment", fwtrading.ErrRejected)
		}
		if qty.Mul(price).LessThan(minQuote) {
			return fmt.Errorf("%w: order is below quote minimum", fwtrading.ErrRejected)
		}
	}
	return nil
}

func (c *coinbaseConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fwtrading.Ack{}, fmt.Errorf("%w: Coinbase requires quantity orders", fwtrading.ErrUnsupported)
	}
	tif, err := parseVenueRoute(order.Route, order.LimitPrice != "", "Coinbase")
	if err != nil {
		return fwtrading.Ack{}, err
	}
	side := strings.ToUpper(string(order.Side))
	if side != "BUY" && side != "SELL" {
		return fwtrading.Ack{}, fmt.Errorf("%w: unsupported order side", fwtrading.ErrRejected)
	}
	config := map[string]any{"base_size": order.Quantity}
	kind := "market_market_ioc"
	if order.LimitPrice != "" {
		config["limit_price"] = order.LimitPrice
		switch tif {
		case "GTC":
			kind = "limit_limit_gtc"
			config["post_only"] = false
		case "IOC":
			kind = "sor_limit_ioc"
		case "FOK":
			kind = "limit_limit_fok"
		}
	}
	body := map[string]any{
		"client_order_id": order.ClientOrderID, "product_id": order.Symbol, "side": side,
		"order_configuration": map[string]any{kind: config},
	}
	var response struct {
		Success *bool `json:"success"`
		Ack     struct {
			ID       string `json:"order_id"`
			ClientID string `json:"client_order_id"`
		} `json:"success_response"`
		Failure struct {
			Reason  string `json:"new_order_failure_reason"`
			Error   string `json:"error"`
			Message string `json:"message"`
			Details string `json:"error_details"`
		} `json:"error_response"`
	}
	err = c.do(ctx, http.MethodPost, "/api/v3/brokerage/orders", nil, body, &response)
	if err != nil {
		var apiErr *coinbase.APIError
		if errors.As(err, &apiErr) && apiErr.BodyDecoded && apiErr.Status >= 400 &&
			apiErr.Status < 500 && apiErr.Status != 408 &&
			!strings.Contains(apiErr.Code+apiErr.Message+apiErr.ErrorDetails+apiErr.PreviewFailureReason, "DUPLICATE_CLIENT_ORDER_ID") {
			return fwtrading.Ack{}, fmt.Errorf("%w: %w", fwtrading.ErrRejected, err)
		}
		return fwtrading.Ack{}, fmt.Errorf("coinbase: send outcome unknown: %w", err)
	}
	if response.Success != nil && !*response.Success {
		failure := response.Failure
		reason := failure.Reason
		if reason == "" {
			reason = failure.Error
		}
		detail := c.client.Scrub(fmt.Sprintf("%s: %s: %s", reason, failure.Message, failure.Details))
		// A reused client id can return the existing order. It is never a
		// definitive rejection, including when success is false.
		if failure.Reason == "DUPLICATE_CLIENT_ORDER_ID" || failure.Error == "DUPLICATE_CLIENT_ORDER_ID" {
			return fwtrading.Ack{}, fmt.Errorf("coinbase: send outcome unknown: %q", detail)
		}
		return fwtrading.Ack{}, fmt.Errorf("%w: coinbase: %q", fwtrading.ErrRejected, detail)
	}
	if response.Success == nil || response.Ack.ID == "" || response.Ack.ClientID != order.ClientOrderID {
		return fwtrading.Ack{}, errors.New("coinbase: send outcome unknown: unusable acknowledgement")
	}
	return fwtrading.Ack{VenueOrderID: response.Ack.ID}, nil
}

type coinbaseOrder struct {
	ID            string `json:"order_id"`
	ClientID      string `json:"client_order_id"`
	Product       string `json:"product_id"`
	Status        string `json:"status"`
	Filled        string `json:"filled_size"`
	Reason        string `json:"reject_reason"`
	Message       string `json:"reject_message"`
	Configuration map[string]struct {
		BaseSize string `json:"base_size"`
	} `json:"order_configuration"`
}

func (c *coinbaseConnector) proofRequired(what string) error {
	c.reportDiag(health.Diagnostic{
		Level: health.DiagError, Kind: health.DiagKindProvider, Code: health.CodeInvalidProviderConfig,
		Title:       "Coinbase requires strong customer authentication for API history",
		Detail:      "Coinbase demands an SCA proof token for this key's history.",
		Remediation: "Officer keeps the order pending and retries. Check the Coinbase account's SCA settings for API keys.",
	})
	return fmt.Errorf("coinbase: venue requires strong customer authentication (proof token) to list %s", what)
}

func (c *coinbaseConnector) findOrder(ctx context.Context, ref fwtrading.OrderRef) (coinbaseOrder, bool, error) {
	if ref.VenueOrderID != "" {
		var response struct {
			Order coinbaseOrder `json:"order"`
		}
		err := c.do(ctx, http.MethodGet, "/api/v3/brokerage/orders/historical/"+url.PathEscape(ref.VenueOrderID), nil, nil, &response)
		if coinbaseNotFound(err) {
			return coinbaseOrder{}, false, nil
		}
		return response.Order, err == nil, err
	}
	if ref.Symbol == "" || ref.Side == "" || ref.ClientOrderID == "" {
		return coinbaseOrder{}, false, errors.New("coinbase: client order lookup requires symbol, side and client order id")
	}
	side := strings.ToUpper(string(ref.Side))
	if side != "BUY" && side != "SELL" {
		return coinbaseOrder{}, false, errors.New("coinbase: invalid lookup side")
	}
	query := url.Values{"product_ids": {ref.Symbol}, "order_side": {side}}
	var match coinbaseOrder
	count := 0
	seen := make(map[string]bool)
	for {
		var page struct {
			Orders  *[]coinbaseOrder `json:"orders"`
			HasNext *bool            `json:"has_next"`
			Cursor  string           `json:"cursor"`
			Proof   bool             `json:"proof_token_required"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/v3/brokerage/orders/historical/batch", query, nil, &page); err != nil {
			return match, false, err
		}
		if page.Proof {
			return match, false, c.proofRequired("orders")
		}
		if page.Orders == nil || page.HasNext == nil {
			return match, false, c.upstream("incomplete order list")
		}
		for _, order := range *page.Orders {
			if order.ClientID == ref.ClientOrderID {
				match = order
				count++
			}
		}
		if !*page.HasNext {
			break
		}
		if page.Cursor == "" || seen[page.Cursor] {
			return match, false, c.upstream("order cursor did not advance")
		}
		seen[page.Cursor] = true
		query.Set("cursor", page.Cursor)
	}
	if count > 1 {
		return match, false, c.upstream("more than one order has the client order id")
	}
	return match, count == 1, nil
}

func (c *coinbaseConnector) Lookup(ctx context.Context, ref fwtrading.OrderRef) (snapshot fwtrading.Snapshot, found bool, err error) {
	if c.reportDiag == nil {
		return fwtrading.Snapshot{}, false, errors.New("coinbase: lookup requires a diagnostic reporter")
	}
	defer func() {
		if errors.Is(err, coinbase.ErrUnparsableResponse) {
			c.reportDiag(venueLookupUnparsable("An order lookup returned an unreadable Coinbase response."))
		}
	}()
	order, found, err := c.findOrder(ctx, ref)
	if err != nil || !found {
		return fwtrading.Snapshot{}, false, err
	}
	snapshot, err = c.snapshot(ctx, ref, order)
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	return snapshot, true, nil
}

func (c *coinbaseConnector) snapshot(ctx context.Context, ref fwtrading.OrderRef, order coinbaseOrder) (fwtrading.Snapshot, error) {
	if order.ID == "" || order.Product == "" || (ref.VenueOrderID != "" && ref.VenueOrderID != order.ID) ||
		(ref.ClientOrderID != "" && ref.ClientOrderID != order.ClientID) || (ref.Symbol != "" && ref.Symbol != order.Product) {
		return fwtrading.Snapshot{}, c.upstream("order identity does not match the reference")
	}
	var status fwtrading.VenueStatus
	switch order.Status {
	case "PENDING", "QUEUED", "OPEN", "EDIT_QUEUED", "CANCEL_QUEUED":
		status = fwtrading.VenueStatusOpen
	case "FILLED":
		status = fwtrading.VenueStatusFilled
	case "CANCELLED", "EXPIRED", "FAILED":
		status = fwtrading.VenueStatusCancelled
	default:
		detail := fmt.Sprintf("unknown venue order status %q", c.client.Scrub(order.Status))
		c.reportDiag(venueLookupUnparsable("An order lookup returned an order status Officer does not know."))
		return fwtrading.Snapshot{}, c.upstream(detail)
	}
	if len(order.Configuration) != 1 {
		return fwtrading.Snapshot{}, c.upstream("unreadable order configuration")
	}
	var qty decimal.Decimal
	for _, config := range order.Configuration {
		var err error
		qty, err = domain.ParseOpenQuantity(config.BaseSize)
		if err != nil || !qty.IsPositive() {
			return fwtrading.Snapshot{}, c.upstream("unreadable order quantity")
		}
	}
	filled, err := domain.ParseOpenQuantity(order.Filled)
	if err != nil || filled.GreaterThan(qty) {
		return fwtrading.Snapshot{}, c.upstream("unreadable filled quantity")
	}
	entries, err := c.fills(ctx, order.ID)
	if err != nil {
		return fwtrading.Snapshot{}, err
	}
	snapshot := fwtrading.Snapshot{VenueOrderID: order.ID, Status: status, FilledQuantity: order.Filled}
	if order.Status == "FAILED" {
		snapshot.Reason = fmt.Sprintf("%q: %q", c.client.Scrub(order.Reason), c.client.Scrub(order.Message))
	}
	quote := ""
	if len(entries) > 0 {
		c.assetsMu.Lock()
		p, cached := c.symbols[order.Product]
		c.assetsMu.Unlock()
		if !cached {
			var found bool
			p, found, err = c.fetchSymbol(ctx, order.Product)
			if err != nil {
				return fwtrading.Snapshot{}, err
			}
			if !found {
				return fwtrading.Snapshot{}, c.upstream("fill product is unknown")
			}
		}
		quote = p.Quote
		if quote == "" {
			return fwtrading.Snapshot{}, c.upstream("fill product has no quote currency")
		}
	}
	cumulative := decimal.Zero
	for _, entry := range entries {
		amount, amountErr := domain.ParseOpenQuantity(entry.Size)
		price, priceErr := domain.ParseOpenQuantity(entry.Price)
		fee, feeErr := domain.ParseOpenQuantity(entry.Commission)
		if amountErr != nil || !amount.IsPositive() || priceErr != nil || !price.IsPositive() || feeErr != nil {
			return fwtrading.Snapshot{}, c.upstream("unreadable fill decimals")
		}
		cumulative = cumulative.Add(amount)
		leaves := qty.Sub(cumulative)
		if leaves.IsNegative() {
			return fwtrading.Snapshot{}, c.upstream("fills exceed order quantity")
		}
		snapshot.Fills = append(snapshot.Fills, fwtrading.Fill{
			VenueTradeID: entry.TradeID,
			Quantity:     entry.Size, Price: entry.Price, CumQuantity: cumulative.String(), LeavesQuantity: leaves.String(),
			Final: cumulative.Equal(qty), At: entry.at,
			Commission: &domain.Commission{Amount: fee.String(), Currency: quote}, CommissionLeg: fwtrading.CommissionLegQuote,
		})
	}
	if !cumulative.Equal(filled) {
		return fwtrading.Snapshot{}, c.upstream("fill history sum differs from filled_size")
	}
	return snapshot, nil
}

type coinbaseFill struct {
	ID          string `json:"entry_id"`
	TradeID     string `json:"trade_id"`
	OrderID     string `json:"order_id"`
	Type        string `json:"trade_type"`
	SizeInQuote bool   `json:"size_in_quote"`
	Time        string `json:"trade_time"`
	Size        string `json:"size"`
	Price       string `json:"price"`
	Commission  string `json:"commission"`
	at          time.Time
	content     any
}

func (c *coinbaseConnector) fills(ctx context.Context, orderID string) ([]coinbaseFill, error) {
	query := url.Values{"order_ids": {orderID}}
	seen := make(map[string]coinbaseFill)
	cursors := make(map[string]bool)
	for {
		var page struct {
			Fills  *[]json.RawMessage `json:"fills"`
			Cursor string             `json:"cursor"`
			Proof  bool               `json:"proof_token_required"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/v3/brokerage/orders/historical/fills", query, nil, &page); err != nil {
			return nil, err
		}
		if page.Proof {
			return nil, c.proofRequired("fills")
		}
		if page.Fills == nil {
			return nil, c.upstream("incomplete fill list")
		}
		for _, raw := range *page.Fills {
			var fill coinbaseFill
			if json.Unmarshal(raw, &fill) != nil {
				return nil, c.upstream("unreadable fill")
			}
			if fill.ID == "" || fill.TradeID == "" || fill.OrderID != orderID || fill.SizeInQuote {
				return nil, c.upstream("unusable fill identity or size_in_quote")
			}
			if fill.Type != "FILL" {
				return nil, c.upstream(fmt.Sprintf("unsupported trade_type %q", c.client.Scrub(fill.Type)))
			}
			var err error
			fill.at, err = time.Parse(time.RFC3339Nano, fill.Time)
			if err != nil {
				return nil, c.upstream("unreadable fill trade_time")
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&fill.content); err != nil {
				return nil, c.upstream("unreadable fill content")
			}
			if previous, exists := seen[fill.ID]; exists && !reflect.DeepEqual(previous.content, fill.content) {
				return nil, c.upstream("conflicting duplicate fill entry_id")
			}
			seen[fill.ID] = fill
		}
		if page.Cursor == "" {
			break
		}
		if cursors[page.Cursor] {
			return nil, c.upstream("fill cursor did not advance")
		}
		cursors[page.Cursor] = true
		query.Set("cursor", page.Cursor)
	}
	entries := make([]coinbaseFill, 0, len(seen))
	for _, fill := range seen {
		entries = append(entries, fill)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].at.Equal(entries[j].at) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].at.Before(entries[j].at)
	})
	return entries, nil
}

func (c *coinbaseConnector) CheckAccount(ctx context.Context) (fwtrading.AccountInfo, error) {
	var response struct {
		Identity string `json:"portfolio_uuid"`
		Type     string `json:"portfolio_type"`
		CanTrade bool   `json:"can_trade"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v3/brokerage/key_permissions", nil, nil, &response); err != nil {
		return fwtrading.AccountInfo{}, err
	}
	if response.Identity == "" {
		return fwtrading.AccountInfo{}, c.upstream("key permissions have no portfolio_uuid")
	}
	status := response.Type
	if !response.CanTrade {
		status += ", trading disabled"
	}
	return fwtrading.AccountInfo{Identity: response.Identity, Status: c.client.Scrub(status)}, nil
}

func (c *coinbaseConnector) VerifySymbol(ctx context.Context, external string) (fwtrading.SymbolVerification, error) {
	p, found, err := c.fetchSymbol(ctx, external)
	if err != nil {
		return fwtrading.SymbolVerification{}, err
	}
	details := ""
	if found {
		details = c.client.Scrub(p.Base + "/" + p.Quote + " " + p.Status)
	}
	return fwtrading.SymbolVerification{Exists: found, Tradable: found && p.tradable(), Details: details}, nil
}

func (c *coinbaseConnector) SearchSymbols(ctx context.Context, query string) ([]fwtrading.SymbolMatch, error) {
	if strings.TrimSpace(query) == "" {
		return []fwtrading.SymbolMatch{}, nil
	}
	params := url.Values{"product_type": {"SPOT"}, "get_tradability_status": {"true"}}
	known := make(map[string]coinbaseProduct)
	seen := make(map[string]bool)
	for {
		var page struct {
			Products   *[]coinbaseProduct `json:"products"`
			Pagination *struct {
				HasNext bool   `json:"has_next"`
				Next    string `json:"next_cursor"`
			} `json:"pagination"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/v3/brokerage/products", params, nil, &page); err != nil {
			return nil, err
		}
		if page.Products == nil {
			return nil, c.upstream("incomplete product catalogue")
		}
		for _, p := range *page.Products {
			if p.ID == "" {
				return nil, c.upstream("catalogue product has no id")
			}
			c.cacheSymbol(p)
			if p.Type == "SPOT" {
				known[p.ID] = p
			}
		}
		// Coinbase does not document a paging procedure or a default limit.
		if page.Pagination == nil || !page.Pagination.HasNext {
			break
		}
		next := page.Pagination.Next
		if next == "" || seen[next] {
			return nil, c.upstream("product cursor did not advance")
		}
		seen[next] = true
		params.Set("cursor", next)
	}
	matches := make([]fwtrading.SymbolMatch, 0)
	for _, id := range symbolsearch.Rank(known, query) {
		p := known[id]
		matches = append(matches, fwtrading.SymbolMatch{Symbol: p.ID, Name: p.Base + "/" + p.Quote,
			Class: "crypto", Exchange: "Coinbase", BaseAsset: p.Base, QuoteAsset: p.Quote, Tradable: p.tradable()})
	}
	return matches, nil
}
