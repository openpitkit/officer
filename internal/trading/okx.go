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
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	"go.openpit.dev/officer/internal/okx"
	"go.openpit.dev/officer/internal/symbolsearch"
	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	okxHTTPTimeout         = 10 * time.Second
	okxFillPageSize        = 100
	okxLookupStatusUnknown = "An OKX order lookup returned an order status Officer does not know."
)

type okxConnector struct {
	client             *okx.Client
	httpClient         *http.Client
	restURL, streamURL string
	redact             *strings.Replacer
	lifecycle          streamLifecycle
	pingInterval       time.Duration
	pongTimeout        time.Duration
	assetsMu           sync.Mutex
	symbols            map[string]okx.Instrument
	reportStatus       health.StatusReporter
	reportDiag         health.DiagnosticReporter
}

var (
	_ fwtrading.Connector        = (*okxConnector)(nil)
	_ fwtrading.PreChecker       = (*okxConnector)(nil)
	_ fwtrading.AccountChecker   = (*okxConnector)(nil)
	_ fwtrading.SymbolVerifier   = (*okxConnector)(nil)
	_ fwtrading.SymbolSearcher   = (*okxConnector)(nil)
	_ health.StatusReporting     = (*okxConnector)(nil)
	_ health.DiagnosticReporting = (*okxConnector)(nil)
)

// NewOKXConnector trades spot in the site's demo or live environment.
// Construction validates credentials and mode without contacting the venue.
func NewOKXConnector(connection domain.TradingConnection) (fwtrading.Connector, error) {
	return newOKXConnector(connection, "", "")
}

func newOKXConnector(
	connection domain.TradingConnection, restURL, streamURL string,
) (*okxConnector, error) {
	if !domain.TradingModeSupported(connection.Mode) {
		return nil, errors.New("okx: unsupported trading mode")
	}
	credentials, err := okx.ParseCredentials(connection.Credentials)
	if err != nil {
		return nil, domain.WithValidationPointer("/credentials"+domain.ValidationPointer(err), err)
	}
	_, redact, err := secret.CredentialRedactor(connection.Credentials)
	if err != nil {
		return nil, fmt.Errorf("okx: %w", err)
	}
	demo := connection.Mode == domain.TradingModePaper
	rest, stream, err := okx.Endpoints(credentials.Site, demo)
	if err != nil {
		return nil, err
	}
	// The internal constructor accepts explicit endpoints for venue tests.
	if restURL == "" {
		restURL = rest
	}
	if streamURL == "" {
		streamURL = stream
	}
	httpClient := vendorhttp.CopyClient(&http.Client{Timeout: okxHTTPTimeout})
	if demo {
		httpClient = okx.DemoHTTPClient(httpClient)
	}
	return &okxConnector{
		client:     okx.NewClient(restURL, credentials, redact, httpClient),
		httpClient: httpClient, restURL: restURL, streamURL: streamURL,
		redact: redact, symbols: make(map[string]okx.Instrument),
		pingInterval: okxDefaultPingInterval, pongTimeout: okxDefaultPongTimeout,
	}, nil
}

func okxClientID(coid string) (string, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(coid)
	if err != nil || len(raw) != domain.ExternalIDByteLen ||
		len(coid) != domain.ExternalIDStringLen {
		return "", fmt.Errorf("%w: OKX client order id must encode exactly 16 bytes",
			fwtrading.ErrRejected)
	}
	return hex.EncodeToString(raw), nil
}

func okxOfficerID(clOrdID string) (string, bool) {
	if len(clOrdID) != 32 || clOrdID != strings.ToLower(clOrdID) {
		return "", false
	}
	raw, err := hex.DecodeString(clOrdID)
	if err != nil {
		return "", false
	}
	id, err := domain.GeneratedExternalIDFromBytes(raw)
	return string(id), err == nil
}

func parseOKXRoute(raw string, limit bool) (string, error) {
	tif, err := parseSpotRoute(raw, "OKX", limit)
	if err != nil {
		return "", err
	}
	switch tif {
	case "":
		return "market", nil
	case "GTC":
		return "limit", nil
	case "IOC":
		return "ioc", nil
	case "FOK":
		return "fok", nil
	default:
		return "", fmt.Errorf(
			"okx: limit timeInForce must be GTC, IOC or FOK: %w", fwtrading.ErrRejected)
	}
}

func (c *okxConnector) instruments(ctx context.Context) ([]okx.Instrument, error) {
	rows, err := okx.FetchInstruments(ctx, c.httpClient, c.restURL)
	if err != nil {
		return nil, fmt.Errorf("okx: catalogue: %s: %w",
			c.redact.Replace(err.Error()), domain.ErrUpstream)
	}
	c.assetsMu.Lock()
	for _, row := range rows {
		c.symbols[row.InstID] = row
	}
	c.assetsMu.Unlock()
	return rows, nil
}

func (c *okxConnector) fetchSymbol(
	ctx context.Context, symbol string,
) (okx.Instrument, bool, error) {
	rows, err := c.instruments(ctx)
	if err != nil {
		return okx.Instrument{}, false, err
	}
	for _, row := range rows {
		if row.InstID == symbol {
			return row, true, nil
		}
	}
	return okx.Instrument{}, false, nil
}

func (c *okxConnector) PreCheck(ctx context.Context, order fwtrading.Order) error {
	if _, err := parseOKXRoute(order.Route, order.LimitPrice != ""); err != nil {
		return err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fmt.Errorf("%w: OKX requires quantity orders", fwtrading.ErrUnsupported)
	}
	instrument, found, err := c.fetchSymbol(ctx, order.Symbol)
	if err != nil {
		return err
	}
	if !found || instrument.State != "live" {
		return fmt.Errorf("%w: symbol must allow spot trading", fwtrading.ErrRejected)
	}
	qty, err := parsePlainDecimal(order.Quantity, true)
	if err != nil {
		return fmt.Errorf("%w: quantity must be a positive plain decimal", fwtrading.ErrRejected)
	}
	minimum, minErr := parsePlainDecimal(instrument.MinSz, true)
	lot, lotErr := parsePlainDecimal(instrument.LotSz, true)
	if minErr != nil || lotErr != nil {
		return fmt.Errorf("okx: unreadable instrument quantity rules: %w", domain.ErrUpstream)
	}
	if qty.LessThan(minimum) || !qty.Mod(lot).IsZero() {
		return fmt.Errorf("%w: quantity must meet minSz and lotSz", fwtrading.ErrRejected)
	}
	if order.LimitPrice != "" {
		price, priceErr := parsePlainDecimal(order.LimitPrice, true)
		if priceErr != nil {
			return fmt.Errorf("%w: price must be a positive plain decimal", fwtrading.ErrRejected)
		}
		tick, tickErr := parsePlainDecimal(instrument.TickSz, true)
		if tickErr != nil {
			return fmt.Errorf("okx: unreadable instrument tickSz: %w", domain.ErrUpstream)
		}
		if !price.Mod(tick).IsZero() {
			return fmt.Errorf("%w: price must meet tickSz", fwtrading.ErrRejected)
		}
	}
	return nil
}

// Duplicate identifiers and transient processing failures cannot prove refusal.
func okxUnknownCode(code string) bool {
	switch code {
	case "50001", "50004", "50011", "50013", "50026", "50040",
		"50042", "50071", "51011", "51016", "51149":
		return true
	}
	return false
}

func okxSendRefused(err error) bool {
	var apiErr *okx.APIError
	if !errors.As(err, &apiErr) || !isDecimalID(apiErr.Code) || okxUnknownCode(apiErr.Code) {
		return false
	}
	for _, item := range apiErr.Items {
		if okxUnknownCode(item.Code) {
			return false
		}
	}
	if apiErr.Status == http.StatusOK {
		return len(apiErr.Items) == 1 && isDecimalID(apiErr.Items[0].Code) &&
			apiErr.Items[0].Code != "0"
	}
	return apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 408 && apiErr.Code != "0"
}

func (c *okxConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	orderType, err := parseOKXRoute(order.Route, order.LimitPrice != "")
	if err != nil {
		return fwtrading.Ack{}, err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fwtrading.Ack{}, fmt.Errorf("%w: OKX requires quantity orders",
			fwtrading.ErrUnsupported)
	}
	if order.Side != domain.OrderSide("buy") && order.Side != domain.OrderSide("sell") {
		return fwtrading.Ack{}, fmt.Errorf("%w: unsupported order side", fwtrading.ErrRejected)
	}
	clientID, err := okxClientID(order.ClientOrderID)
	if err != nil {
		return fwtrading.Ack{}, err
	}
	body := map[string]string{
		"instId": order.Symbol, "tdMode": "cash", "side": string(order.Side),
		"clOrdId": clientID, "sz": order.Quantity, "ordType": orderType,
	}
	if orderType == "market" {
		body["tgtCcy"] = "base_ccy"
	} else {
		body["px"] = order.LimitPrice
	}
	var rows []struct {
		OrderID  string `json:"ordId"`
		ClientID string `json:"clOrdId"`
		Code     string `json:"sCode"`
		Message  string `json:"sMsg"`
	}
	status, err := c.client.Signed(ctx, http.MethodPost, "/api/v5/trade/order", body, &rows)
	if err == nil && len(rows) == 1 && rows[0].Code != "0" {
		if !isDecimalID(rows[0].Code) {
			return fwtrading.Ack{}, errors.New("okx: send outcome unknown: unreadable item code")
		}
		// sMsg is untrusted and could contain the request signature.
		err = &okx.APIError{Status: status, Code: "1",
			Items: []okx.ItemError{{Code: c.redact.Replace(rows[0].Code)}}}
	}
	if err != nil {
		if okxSendRefused(err) {
			return fwtrading.Ack{}, fmt.Errorf("%w: %w", fwtrading.ErrRejected, err)
		}
		return fwtrading.Ack{}, fmt.Errorf("okx: send outcome unknown: %w", err)
	}
	if len(rows) != 1 || rows[0].Code != "0" || !isDecimalID(rows[0].OrderID) ||
		rows[0].ClientID != clientID {
		return fwtrading.Ack{}, errors.New("okx: send outcome unknown: unusable acknowledgement")
	}
	return fwtrading.Ack{VenueOrderID: rows[0].OrderID}, nil
}

type okxOrder struct {
	OrderID  string `json:"ordId"`
	ClientID string `json:"clOrdId"`
	Symbol   string `json:"instId"`
	State    string `json:"state"`
	Quantity string `json:"sz"`
	Filled   string `json:"accFillSz"`
}

type okxTrade struct {
	OrderID  string `json:"ordId"`
	TradeID  string `json:"tradeId"`
	BillID   string `json:"billId"`
	Quantity string `json:"fillSz"`
	Price    string `json:"fillPx"`
	At       string `json:"fillTime"`
	Fee      string `json:"fee"`
	FeeCcy   string `json:"feeCcy"`
}

func okxVenueStatus(state string) (fwtrading.VenueStatus, bool) {
	switch state {
	case "live", "partially_filled":
		return fwtrading.VenueStatusOpen, true
	case "filled":
		return fwtrading.VenueStatusFilled, true
	case "canceled", "mmp_canceled":
		return fwtrading.VenueStatusCancelled, true
	}
	return "", false
}

func (c *okxConnector) Lookup(
	ctx context.Context, ref fwtrading.OrderRef,
) (fwtrading.Snapshot, bool, error) {
	if c.reportDiag == nil {
		return fwtrading.Snapshot{}, false, errors.New("okx: lookup requires a diagnostic reporter")
	}
	if ref.Symbol == "" {
		return fwtrading.Snapshot{}, false, errors.New("okx: order symbol is required")
	}
	if ref.ClientOrderID == "" && ref.VenueOrderID == "" {
		return fwtrading.Snapshot{}, false, errors.New("okx: order reference is required")
	}
	params := url.Values{"instId": {ref.Symbol}}
	if ref.VenueOrderID != "" {
		params.Set("ordId", ref.VenueOrderID)
	}
	if ref.ClientOrderID != "" {
		id, err := okxClientID(ref.ClientOrderID)
		if err != nil {
			return fwtrading.Snapshot{}, false, err
		}
		params.Set("clOrdId", id)
	}
	var rows []okxOrder
	_, err := c.client.Signed(ctx, http.MethodGet,
		"/api/v5/trade/order?"+params.Encode(), nil, &rows)
	var apiErr *okx.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "51603" {
		return fwtrading.Snapshot{}, false, nil
	}
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	if len(rows) != 1 {
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"okx: ambiguous order lookup: %w", domain.ErrUpstream)
	}
	order := rows[0]
	clientID, own := okxOfficerID(order.ClientID)
	if !own || !isDecimalID(order.OrderID) || order.Symbol != ref.Symbol ||
		(ref.ClientOrderID != "" && clientID != ref.ClientOrderID) ||
		(ref.VenueOrderID != "" && order.OrderID != ref.VenueOrderID) {
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"okx: inconsistent lookup identity: %w", domain.ErrUpstream)
	}
	status, known := okxVenueStatus(order.State)
	if !known {
		c.reportDiag(venueLookupUnparsable(okxLookupStatusUnknown))
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"okx: unknown venue order status: %w", domain.ErrUpstream)
	}
	filled, err := parsePlainDecimal(order.Filled, false)
	if err != nil {
		return fwtrading.Snapshot{}, false, fmt.Errorf(
			"okx: unreadable lookup quantities: %w", domain.ErrUpstream)
	}
	var trades []okxTrade
	if !filled.IsZero() {
		trades, err = c.trades(ctx, ref.Symbol, order.OrderID, filled)
	}
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	snapshot, err := c.snapshot(ctx, order, status, trades)
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	return snapshot, true, nil
}

func (c *okxConnector) trades(
	ctx context.Context, symbol, orderID string, filled decimal.Decimal,
) ([]okxTrade, error) {
	known := make(map[string]okxTrade)
	cum := decimal.Zero
	for _, endpoint := range []string{"/api/v5/trade/fills", "/api/v5/trade/fills-history"} {
		params := url.Values{
			"instType": {"SPOT"}, "instId": {symbol}, "ordId": {orderID}, "limit": {"100"},
		}
		cursors := make(map[string]bool)
		for {
			var page []okxTrade
			_, err := c.client.Signed(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil, &page)
			if err != nil {
				return nil, fmt.Errorf("okx: fill history: %w: %w", domain.ErrUpstream, err)
			}
			for _, trade := range page {
				if trade.OrderID != orderID || !isDecimalID(trade.TradeID) ||
					!isDecimalID(trade.BillID) {
					return nil, fmt.Errorf(
						"okx: inconsistent fill identity: %w", domain.ErrUpstream)
				}
				if previous, exists := known[trade.TradeID]; exists && previous != trade {
					return nil, fmt.Errorf(
						"okx: conflicting duplicate fill: %w", domain.ErrUpstream)
				}
				if _, exists := known[trade.TradeID]; !exists {
					amount, err := parsePlainDecimal(trade.Quantity, true)
					if err != nil {
						return nil, fmt.Errorf("okx: unreadable fill: %w", domain.ErrUpstream)
					}
					cum = cum.Add(amount)
				}
				known[trade.TradeID] = trade
			}
			if len(page) == 0 {
				break
			}
			cursor := page[len(page)-1].BillID
			if cursors[cursor] {
				return nil, fmt.Errorf("okx: repeated fill cursor: %w", domain.ErrUpstream)
			}
			cursors[cursor] = true
			if len(page) < okxFillPageSize {
				break
			}
			params.Set("after", cursor)
		}
		if cum.Equal(filled) {
			break
		}
	}
	trades := make([]okxTrade, 0, len(known))
	for _, trade := range known {
		trades = append(trades, trade)
	}
	// Check times before sorting, so malformed input never chooses an order.
	for _, trade := range trades {
		if _, err := okxFillTime(trade.At); err != nil {
			return nil, err
		}
	}
	sort.Slice(trades, func(i, j int) bool {
		a, _ := strconv.ParseInt(trades[i].At, 10, 64)
		b, _ := strconv.ParseInt(trades[j].At, 10, 64)
		if a != b {
			return a < b
		}
		return len(trades[i].TradeID) < len(trades[j].TradeID) ||
			(len(trades[i].TradeID) == len(trades[j].TradeID) &&
				trades[i].TradeID < trades[j].TradeID)
	})
	return trades, nil
}

func okxFillTime(raw string) (time.Time, error) {
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, fmt.Errorf("okx: unreadable fill time: %w", domain.ErrUpstream)
	}
	return time.UnixMilli(ms).UTC(), nil
}

func (c *okxConnector) snapshot(
	ctx context.Context, order okxOrder, status fwtrading.VenueStatus, trades []okxTrade,
) (fwtrading.Snapshot, error) {
	qty, qtyErr := parsePlainDecimal(order.Quantity, true)
	filled, filledErr := parsePlainDecimal(order.Filled, false)
	if qtyErr != nil || filledErr != nil || filled.GreaterThan(qty) {
		return fwtrading.Snapshot{}, fmt.Errorf(
			"okx: unreadable lookup quantities: %w", domain.ErrUpstream)
	}
	snapshot := fwtrading.Snapshot{
		VenueOrderID: order.OrderID, Status: status, FilledQuantity: order.Filled,
	}
	cum := decimal.Zero
	for i, trade := range trades {
		amount, amountErr := parsePlainDecimal(trade.Quantity, true)
		_, priceErr := parsePlainDecimal(trade.Price, true)
		at, timeErr := okxFillTime(trade.At)
		if amountErr != nil || priceErr != nil || timeErr != nil {
			return fwtrading.Snapshot{}, fmt.Errorf("okx: unreadable fill: %w", domain.ErrUpstream)
		}
		cum = cum.Add(amount)
		leaves := qty.Sub(cum)
		if leaves.IsNegative() {
			return fwtrading.Snapshot{}, fmt.Errorf(
				"okx: fills exceed order quantity: %w", domain.ErrUpstream)
		}
		commission, leg, err := c.commission(ctx, order.Symbol, trade.Fee, trade.FeeCcy, true)
		if err != nil {
			return fwtrading.Snapshot{}, err
		}
		snapshot.Fills = append(snapshot.Fills, fwtrading.Fill{
			Quantity: trade.Quantity, Price: trade.Price,
			CumQuantity: cum.String(), LeavesQuantity: leaves.String(),
			Final: i == len(trades)-1 && status == fwtrading.VenueStatusFilled && cum.Equal(qty),
			At:    at, Commission: commission, CommissionLeg: leg,
		})
	}
	if !cum.Equal(filled) {
		return fwtrading.Snapshot{}, fmt.Errorf(
			"okx: incomplete fill history: %w", domain.ErrUpstream)
	}
	return snapshot, nil
}

func (c *okxConnector) commission(
	ctx context.Context, symbol, fee, currency string, fetch bool,
) (*domain.Commission, fwtrading.CommissionLeg, error) {
	value, err := parsePlainDecimal(strings.TrimPrefix(fee, "-"), false)
	if err != nil || (currency == "" && !value.IsZero()) {
		return nil, "", fmt.Errorf("okx: unreadable commission: %w", domain.ErrUpstream)
	}
	if value.IsZero() {
		return nil, "", nil
	}
	if !strings.HasPrefix(fee, "-") {
		value = value.Neg()
	}
	c.assetsMu.Lock()
	instrument, found := c.symbols[symbol]
	c.assetsMu.Unlock()
	if !found && fetch {
		instrument, found, err = c.fetchSymbol(ctx, symbol)
		if err != nil {
			return nil, "", err
		}
	}
	if !found || instrument.BaseCcy == "" || instrument.QuoteCcy == "" {
		return nil, "", fmt.Errorf("okx: symbol assets are missing: %w", domain.ErrUpstream)
	}
	var leg fwtrading.CommissionLeg
	switch currency {
	case instrument.BaseCcy:
		leg = fwtrading.CommissionLegBase
	case instrument.QuoteCcy:
		leg = fwtrading.CommissionLegQuote
	}
	return &domain.Commission{Amount: value.String(), Currency: currency}, leg, nil
}

func (c *okxConnector) CheckAccount(ctx context.Context) (fwtrading.AccountInfo, error) {
	uid, err := c.client.AccountUID(ctx)
	if err != nil {
		return fwtrading.AccountInfo{}, err
	}
	return fwtrading.AccountInfo{Identity: uid}, nil
}

func (c *okxConnector) VerifySymbol(
	ctx context.Context, external string,
) (fwtrading.SymbolVerification, error) {
	instrument, found, err := c.fetchSymbol(ctx, external)
	if err != nil || !found {
		return fwtrading.SymbolVerification{}, err
	}
	return fwtrading.SymbolVerification{Exists: true, Tradable: instrument.State == "live",
		Details: instrument.BaseCcy + "/" + instrument.QuoteCcy + " " + instrument.State}, nil
}

func (c *okxConnector) SearchSymbols(
	ctx context.Context, query string,
) ([]fwtrading.SymbolMatch, error) {
	if strings.TrimSpace(query) == "" {
		return []fwtrading.SymbolMatch{}, nil
	}
	rows, err := c.instruments(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]okx.Instrument, len(rows))
	for _, row := range rows {
		known[row.InstID] = row
	}
	matches := make([]fwtrading.SymbolMatch, 0)
	for _, name := range symbolsearch.Rank(known, query) {
		row := known[name]
		matches = append(matches, fwtrading.SymbolMatch{
			Symbol: name, Name: row.BaseCcy + "/" + row.QuoteCcy,
			Class: "crypto", Exchange: "OKX", BaseAsset: row.BaseCcy,
			QuoteAsset: row.QuoteCcy, Tradable: row.State == "live",
		})
	}
	return matches, nil
}
