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
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/alpaca"
)

const (
	alpacaHTTPTimeout      = 10 * time.Second
	alpacaPaperStreamURL   = "wss://paper-api.alpaca.markets/stream"
	alpacaLiveStreamURL    = "wss://api.alpaca.markets/stream"
	alpacaFillPageSize     = 100
	alpacaDecimalMaxLength = 40
)

type alpacaConnector struct {
	client      *alpaca.Client
	credentials alpaca.Credentials
	streamURL   string
	externalID  domain.ExternalID
	mu          sync.Mutex
	closed      bool
	cancel      context.CancelFunc
	done        chan struct{}
	feeMu       sync.Mutex
	feeSymbols  map[string]string
}

var (
	_ fwtrading.Connector      = (*alpacaConnector)(nil)
	_ fwtrading.PreChecker     = (*alpacaConnector)(nil)
	_ fwtrading.SymbolVerifier = (*alpacaConnector)(nil)
)

// NewAlpacaConnector selects paper or live endpoints explicitly from Mode.
// Construction validates credentials without making a network request.
func NewAlpacaConnector(connection domain.TradingConnection) (fwtrading.Connector, error) {
	var restURL, streamURL string
	switch connection.Mode {
	case domain.TradingModeTest:
		restURL, streamURL = alpaca.PaperRESTURL, alpacaPaperStreamURL
	case domain.TradingModeReal:
		restURL, streamURL = alpaca.LiveRESTURL, alpacaLiveStreamURL
	default:
		return nil, errors.New("alpaca: unsupported trading mode")
	}
	c, err := newAlpacaConnector(connection, restURL, streamURL)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func newAlpacaConnector(connection domain.TradingConnection, restURL, streamURL string) (*alpacaConnector, error) {
	credentials, err := alpaca.ParseCredentials(connection.Credentials)
	if err != nil {
		return nil, err
	}
	return &alpacaConnector{
		client:      alpaca.NewClient(restURL, credentials, &http.Client{Timeout: alpacaHTTPTimeout}),
		credentials: credentials, streamURL: streamURL,
		externalID: connection.ExternalID,
	}, nil
}

func parseAlpacaRoute(raw string) (string, error) {
	var route map[string]string
	if err := json.Unmarshal([]byte(raw), &route); err != nil {
		return "", fmt.Errorf("%w: invalid Alpaca route JSON", fwtrading.ErrRejected)
	}
	for field := range route {
		if field != "timeInForce" {
			return "", fmt.Errorf("%w: unknown Alpaca route field", fwtrading.ErrRejected)
		}
	}
	tif := route["timeInForce"]
	if tif == "" {
		return "", fmt.Errorf("%w: timeInForce is required", fwtrading.ErrRejected)
	}
	switch tif {
	case "day", "gtc", "opg", "cls", "ioc", "fok":
		return tif, nil
	default:
		return "", fmt.Errorf("%w: unsupported timeInForce", fwtrading.ErrRejected)
	}
}

func positiveDecimal(raw, field string) (decimal.Decimal, error) {
	value, err := decimal.NewFromString(raw)
	if err != nil || !value.IsPositive() {
		return decimal.Decimal{}, fmt.Errorf("%w: %s must be a positive decimal", fwtrading.ErrRejected, field)
	}
	return value, nil
}

func checkAlpacaIncrement(value decimal.Decimal, raw, field string) error {
	if raw == "" {
		return nil
	}
	increment, err := parseAlpacaDecimal(raw, field, true)
	if err != nil {
		return err
	}
	if !value.Mod(increment).IsZero() {
		return fmt.Errorf("%w: value must be a multiple of %s", fwtrading.ErrRejected, field)
	}
	return nil
}

func parseAlpacaDecimal(raw, field string, positive bool) (decimal.Decimal, error) {
	if len(raw) > alpacaDecimalMaxLength {
		return decimal.Decimal{}, fmt.Errorf("alpaca: invalid venue %s: decimal exceeds %d characters: %w", field, alpacaDecimalMaxLength, domain.ErrUpstream)
	}
	value, err := domain.ParseOpenQuantity(raw)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("alpaca: invalid venue %s: expected a non-negative plain decimal: %w", field, domain.ErrUpstream)
	}
	if positive && !value.IsPositive() {
		return decimal.Decimal{}, fmt.Errorf("alpaca: invalid venue %s: expected a positive decimal: %w", field, domain.ErrUpstream)
	}
	return value, nil
}

func (c *alpacaConnector) PreCheck(ctx context.Context, order fwtrading.Order) error {
	tif, err := parseAlpacaRoute(order.Route)
	if err != nil {
		return err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fmt.Errorf("%w: Alpaca requires quantity orders", fwtrading.ErrUnsupported)
	}
	asset, found, err := c.client.Asset(ctx, order.Symbol)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: asset not found", fwtrading.ErrRejected)
	}
	if asset.Status != "active" || !asset.Tradable {
		return fmt.Errorf("%w: asset must be active and tradable", fwtrading.ErrRejected)
	}
	if asset.Class != "us_equity" && asset.Class != "crypto" {
		return fmt.Errorf("%w: asset class is not supported", fwtrading.ErrUnsupported)
	}
	if asset.Class == "crypto" {
		if _, _, err := c.InstrumentAssets(asset.Symbol); err != nil || !strings.Contains(asset.Symbol, "/") {
			return fmt.Errorf("%w: crypto asset has no canonical BASE/QUOTE symbol", domain.ErrUpstream)
		}
		if order.Symbol != asset.Symbol {
			return fmt.Errorf("%w: configure canonical crypto symbol %s", fwtrading.ErrUnsupported, asset.Symbol)
		}
	}
	qty, err := positiveDecimal(order.Quantity, "quantity")
	if err != nil {
		return err
	}
	var price decimal.Decimal
	if order.LimitPrice != "" {
		price, err = positiveDecimal(order.LimitPrice, "limit price")
		if err != nil {
			return err
		}
	}
	if asset.Class == "us_equity" {
		if !qty.IsInteger() {
			if !asset.Fractionable {
				return fmt.Errorf("%w: fractional quantity requires a fractionable asset", fwtrading.ErrRejected)
			}
			if tif != "day" {
				return fmt.Errorf("%w: fractional equity quantity requires day timeInForce", fwtrading.ErrRejected)
			}
		}
		if order.LimitPrice != "" {
			places := int32(2)
			if price.LessThan(decimal.NewFromInt(1)) {
				places = 4
			}
			if !price.Equal(price.Truncate(places)) {
				return fmt.Errorf("%w: equity limit price allows at most %d decimal places", fwtrading.ErrRejected, places)
			}
		}
		return nil
	}
	if tif != "gtc" && tif != "ioc" {
		return fmt.Errorf("%w: crypto timeInForce must be gtc or ioc", fwtrading.ErrRejected)
	}
	if asset.MinOrderSize != "" {
		minimum, err := parseAlpacaDecimal(asset.MinOrderSize, "min_order_size", true)
		if err != nil {
			return err
		}
		if qty.LessThan(minimum) {
			return fmt.Errorf("%w: quantity is below min_order_size", fwtrading.ErrRejected)
		}
	}
	if err := checkAlpacaIncrement(qty, asset.MinTradeIncrement, "min_trade_increment"); err != nil {
		return err
	}
	if order.LimitPrice != "" {
		return checkAlpacaIncrement(price, asset.PriceIncrement, "price_increment")
	}
	return nil
}

type alpacaOrder struct {
	ID             string `json:"id"`
	ClientOrderID  string `json:"client_order_id"`
	Status         string `json:"status"`
	Quantity       string `json:"qty"`
	FilledQuantity string `json:"filled_qty"`
}

func (c *alpacaConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	tif, err := parseAlpacaRoute(order.Route)
	if err != nil {
		return fwtrading.Ack{}, err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fwtrading.Ack{}, fmt.Errorf("%w: Alpaca requires quantity orders", fwtrading.ErrUnsupported)
	}
	body := struct {
		Symbol        string           `json:"symbol"`
		Quantity      string           `json:"qty"`
		Side          domain.OrderSide `json:"side"`
		Type          string           `json:"type"`
		TimeInForce   string           `json:"time_in_force"`
		LimitPrice    string           `json:"limit_price,omitempty"`
		ClientOrderID string           `json:"client_order_id"`
	}{order.Symbol, order.Quantity, order.Side, "market", tif, order.LimitPrice, order.ClientOrderID}
	if order.LimitPrice != "" {
		body.Type = "limit"
	}
	var response alpacaOrder
	status, err := c.client.Request(ctx, http.MethodPost, "/v2/orders", body, &response)
	if err != nil {
		var apiErr *alpaca.APIError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 408 {
			uncertain := apiErr.Status == 422 && (!apiErr.BodyDecoded || strings.Contains(strings.ToLower(apiErr.Message), "client_order_id"))
			if !uncertain {
				return fwtrading.Ack{}, fmt.Errorf("%w: %v", fwtrading.ErrRejected, err)
			}
		}
		return fwtrading.Ack{}, fmt.Errorf("alpaca: send outcome unknown: %w", err)
	}
	if status != http.StatusOK || response.ID == "" {
		return fwtrading.Ack{}, errors.New("alpaca: send outcome unknown: expected HTTP 200 with an order id")
	}
	return fwtrading.Ack{VenueOrderID: response.ID}, nil
}

func alpacaVenueStatus(status string) fwtrading.VenueStatus {
	switch status {
	case "filled":
		return fwtrading.VenueStatusFilled
	case "canceled", "expired", "rejected":
		return fwtrading.VenueStatusCancelled
	case "replaced":
		return fwtrading.VenueStatusReplaced
	default:
		return fwtrading.VenueStatusOpen
	}
}

type alpacaActivity struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	Quantity       string    `json:"qty"`
	Price          string    `json:"price"`
	CumQuantity    string    `json:"cum_qty"`
	LeavesQuantity string    `json:"leaves_qty"`
	At             time.Time `json:"transaction_time"`
}

func (c *alpacaConnector) Lookup(ctx context.Context, ref fwtrading.OrderRef) (fwtrading.Snapshot, bool, error) {
	path := "/v2/orders/" + url.PathEscape(ref.VenueOrderID)
	if ref.VenueOrderID == "" {
		if ref.ClientOrderID == "" {
			return fwtrading.Snapshot{}, false, errors.New("alpaca: order reference is required")
		}
		path = "/v2/orders:by_client_order_id?client_order_id=" + url.QueryEscape(ref.ClientOrderID)
	}
	var order alpacaOrder
	_, err := c.client.Request(ctx, http.MethodGet, path, nil, &order)
	var apiErr *alpaca.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return fwtrading.Snapshot{}, false, nil
	}
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	if order.ID == "" {
		return fwtrading.Snapshot{}, false, errors.New("alpaca: lookup response is missing order id")
	}
	snapshot := fwtrading.Snapshot{VenueOrderID: order.ID, Status: alpacaVenueStatus(order.Status), FilledQuantity: order.FilledQuantity}
	query := url.Values{"order_id": {order.ID}, "direction": {"asc"}, "page_size": {"100"}}
	for {
		var page []alpacaActivity
		_, err := c.client.Request(ctx, http.MethodGet, "/v2/account/activities/FILL?"+query.Encode(), nil, &page)
		if err != nil {
			return fwtrading.Snapshot{}, false, err
		}
		for _, activity := range page {
			snapshot.Fills = append(snapshot.Fills, fwtrading.Fill{
				Quantity: activity.Quantity, Price: activity.Price,
				CumQuantity: activity.CumQuantity, LeavesQuantity: activity.LeavesQuantity,
				Final: activity.Type == "fill", At: activity.At,
			})
		}
		if len(page) < alpacaFillPageSize {
			break
		}
		token := page[len(page)-1].ID
		if token == "" || token == query.Get("page_token") {
			return fwtrading.Snapshot{}, false, errors.New("alpaca: fill pagination did not advance")
		}
		query.Set("page_token", token)
	}
	return snapshot, true, nil
}

func (c *alpacaConnector) VerifySymbol(ctx context.Context, external string) (fwtrading.SymbolVerification, error) {
	asset, found, err := c.client.Asset(ctx, external)
	if err != nil {
		return fwtrading.SymbolVerification{}, err
	}
	if !found {
		return fwtrading.SymbolVerification{}, nil
	}
	details := asset.Class + " " + asset.Exchange + " " + asset.Name
	if asset.Fractionable {
		details += ", fractionable"
	}
	return fwtrading.SymbolVerification{Exists: true, Tradable: asset.Status == "active" && asset.Tradable, Details: details}, nil
}
