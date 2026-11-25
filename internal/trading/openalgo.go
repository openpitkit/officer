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
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/secret"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/openalgo"
)

var (
	_ fwtrading.Connector        = (*openAlgoConnector)(nil)
	_ health.StatusReporting     = (*openAlgoConnector)(nil)
	_ fwtrading.PreChecker       = (*openAlgoConnector)(nil)
	_ fwtrading.AccountChecker   = (*openAlgoConnector)(nil)
	_ fwtrading.SymbolSearcher   = (*openAlgoConnector)(nil)
	_ fwtrading.SymbolVerifier   = (*openAlgoConnector)(nil)
	_ health.DiagnosticReporting = (*openAlgoConnector)(nil)
)

type openAlgoConnector struct {
	*orderCorrelator
	client       *openalgo.Client
	mode         domain.TradingMode
	url          string
	redact       *strings.Replacer
	wsURL        string
	apiKey       string
	closedMu     sync.Mutex
	closedFills  map[string]openalgo.Decimal
	closedIDs    []string
	lifecycle    streamLifecycle
	reportStatus health.StatusReporter
	reportDiag   health.DiagnosticReporter
}

// NewOpenAlgoConnector trades through one OpenAlgo user's order API.
// Credentials require the REST URL, stream URL and API key.
func NewOpenAlgoConnector(connection domain.TradingConnection) (fwtrading.Connector, error) {
	c, err := newOpenAlgoConnector(connection)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func newOpenAlgoConnector(connection domain.TradingConnection) (*openAlgoConnector, error) {
	switch connection.Mode {
	case domain.TradingModePaper, domain.TradingModeLive:
	default:
		return nil, errors.New("openalgo: unsupported trading mode")
	}
	credentials, err := openalgo.ParseCredentials(connection.Credentials)
	if err != nil {
		return nil, domain.WithValidationPointer("/credentials"+domain.ValidationPointer(err), err)
	}
	_, redact, err := secret.CredentialRedactor(connection.Credentials, "url", "wsUrl")
	if err != nil {
		return nil, fmt.Errorf("openalgo: %w", err)
	}
	return &openAlgoConnector{
		orderCorrelator: newOrderCorrelator(),
		closedFills:     make(map[string]openalgo.Decimal),
		wsURL:           credentials.WSURL, apiKey: credentials.APIKey,
		client: openalgo.NewClient(credentials.URL, credentials.APIKey,
			&http.Client{Timeout: 10 * time.Second}, redact),
		mode: connection.Mode, url: strings.TrimRight(credentials.URL, "/"),
		redact: redact,
	}, nil
}

type openAlgoOrderInput struct {
	request         openalgo.PlaceOrderRequest
	quantity, price decimal.Decimal
}

func openAlgoOrder(order fwtrading.Order) (openAlgoOrderInput, error) {
	var route map[string]string
	if json.Unmarshal([]byte(order.Route), &route) != nil || len(route) != 1 ||
		(route["product"] != "CNC" && route["product"] != "MIS") {
		return openAlgoOrderInput{}, fmt.Errorf("%w: OpenAlgo route requires product CNC or MIS", fwtrading.ErrRejected)
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return openAlgoOrderInput{}, fmt.Errorf("%w: OpenAlgo requires quantity orders", fwtrading.ErrUnsupported)
	}
	if order.QuoteAsset != "INR" {
		return openAlgoOrderInput{}, fmt.Errorf("%w: OpenAlgo requires INR as the quote asset", fwtrading.ErrUnsupported)
	}
	instrument, err := openalgo.ParseSpotSymbol(order.Symbol)
	if err != nil {
		return openAlgoOrderInput{}, fmt.Errorf("%w: %s", fwtrading.ErrRejected, openalgo.SymbolForm)
	}
	var action string
	switch order.Side {
	case domain.OrderSideBuy:
		action = "BUY"
	case domain.OrderSideSell:
		action = "SELL"
	default:
		return openAlgoOrderInput{}, fmt.Errorf("%w: OpenAlgo requires buy or sell", fwtrading.ErrUnsupported)
	}
	quantity, err := positiveDecimal(order.Quantity, "OpenAlgo quantity")
	if err != nil {
		return openAlgoOrderInput{}, err
	}
	if !quantity.IsInteger() {
		return openAlgoOrderInput{}, fmt.Errorf("%w: OpenAlgo quantity must be a positive whole number", fwtrading.ErrRejected)
	}
	input := openAlgoOrderInput{
		quantity: quantity,
		request: openalgo.PlaceOrderRequest{
			Strategy: "Pit Officer", Symbol: instrument.Symbol, Exchange: instrument.Exchange,
			Action: action, Product: route["product"], Quantity: quantity.String(),
			PriceType: "MARKET",
		},
	}
	if order.LimitPrice != "" {
		price, err := positiveDecimal(order.LimitPrice, "OpenAlgo limit price")
		if err != nil {
			return openAlgoOrderInput{}, err
		}
		input.price = price
		input.request.PriceType, input.request.Price = "LIMIT", price.String()
	}
	return input, nil
}

func (c *openAlgoConnector) checkMode(ctx context.Context) (bool, error) {
	analyze, err := c.client.Analyzer(ctx)
	if err != nil {
		return false, err
	}
	if c.mode == domain.TradingModePaper && !analyze {
		return false, errors.New("openalgo: the connection is paper, but OpenAlgo is in live mode; switch OpenAlgo to analyzer mode")
	}
	if c.mode == domain.TradingModeLive && analyze {
		return false, errors.New("openalgo: the connection is live, but OpenAlgo is in analyzer mode; switch OpenAlgo to live mode")
	}
	return analyze, nil
}

// CheckAccount includes mode and address because OpenAlgo has no account id.
// Connections are unique by provider and identity, and a credential update
// cannot change identity; paper and live must remain distinct connections.
func (c *openAlgoConnector) CheckAccount(ctx context.Context) (fwtrading.AccountInfo, error) {
	broker, err := c.client.Ping(ctx)
	if err != nil {
		return fwtrading.AccountInfo{}, err
	}
	analyze, err := c.checkMode(ctx)
	if err != nil {
		return fwtrading.AccountInfo{}, err
	}
	status := "live"
	if analyze {
		status = "analyze"
	}
	return fwtrading.AccountInfo{Identity: broker + " " + status + " " + c.url, Status: status}, nil
}

func (c *openAlgoConnector) PreCheck(ctx context.Context, order fwtrading.Order) error {
	input, err := openAlgoOrder(order)
	if err != nil {
		return err
	}
	if _, err := c.checkMode(ctx); err != nil {
		return err
	}
	info, err := c.client.Symbol(ctx, input.request.Symbol, input.request.Exchange)
	if err != nil {
		var apiErr *openalgo.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return fmt.Errorf("%w: OpenAlgo does not know the symbol: %w", fwtrading.ErrRejected, err)
		}
		return err
	}
	lot, err := decimal.NewFromString(string(info.LotSize))
	if err != nil || !lot.IsPositive() {
		return errors.New("openalgo: venue lot size must be a positive decimal")
	}
	tick, err := decimal.NewFromString(string(info.TickSize))
	if err != nil || !tick.IsPositive() {
		return errors.New("openalgo: venue tick size must be a positive decimal")
	}
	if !input.quantity.Mod(lot).IsZero() {
		return fmt.Errorf("%w: OpenAlgo quantity must be a multiple of lot size %s", fwtrading.ErrRejected, lot.String())
	}
	if input.request.PriceType == "LIMIT" && !input.price.Mod(tick).IsZero() {
		return fmt.Errorf("%w: OpenAlgo price must be a multiple of tick size %s", fwtrading.ErrRejected, tick.String())
	}
	return nil
}

func (c *openAlgoConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	if c.reportDiag == nil {
		return fwtrading.Ack{}, fmt.Errorf("%w: openalgo send requires a diagnostic reporter", fwtrading.ErrUnsupported)
	}
	input, err := openAlgoOrder(order)
	if err != nil {
		return fwtrading.Ack{}, err
	}
	result, err := c.client.PlaceOrder(ctx, input.request)
	if err != nil {
		var apiErr *openalgo.APIError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 408 {
			return fwtrading.Ack{}, fmt.Errorf("%w: %w", fwtrading.ErrRejected, err)
		}
		return fwtrading.Ack{}, fmt.Errorf("openalgo: send outcome unknown: %w", err)
	}
	if result.Mode == "semi_auto" {
		c.reportDiag(health.Diagnostic{
			Level: health.DiagError, Code: health.CodeInvalidProviderConfig, Kind: health.DiagKindConfig,
			Title:  "OpenAlgo queued the order for approval",
			Detail: "OpenAlgo is in semi-auto mode and queued the order in its Action Center instead of sending it.",
			Remediation: "Officer cannot see whether the order was approved and keeps it pending with an unknown outcome. " +
				"Resolve the order in OpenAlgo's Action Center and switch OpenAlgo to auto mode.",
		})
		return fwtrading.Ack{}, errors.New("openalgo: send outcome unknown: order queued for approval")
	}
	if result.OrderID == "" {
		return fwtrading.Ack{}, errors.New("openalgo: send outcome unknown: missing order id")
	}
	if result.Mode != c.expectedRESTMode() {
		detail := "OpenAlgo did not place the order in live mode on a live connection; the order may be a sandbox order."
		if c.mode == domain.TradingModePaper {
			detail = "OpenAlgo did not place the order in analyzer mode on a paper connection; the order may be live at the broker."
		}
		c.reportDiag(health.Diagnostic{
			Level: health.DiagError, Code: health.CodeInvalidProviderConfig, Kind: health.DiagKindConfig,
			Title: "OpenAlgo order mode differs from the connection", Detail: detail,
			Remediation: "Officer recorded the order as sent. Check it in OpenAlgo and at the broker, " +
				"and set OpenAlgo's mode to match the connection.",
		})
	}
	c.mapOrder(string(result.OrderID), order.ClientOrderID)
	return fwtrading.Ack{VenueOrderID: string(result.OrderID)}, nil
}

func (c *openAlgoConnector) expectedRESTMode() string {
	if c.mode == domain.TradingModePaper {
		return "analyze"
	}
	return ""
}

func (c *openAlgoConnector) Lookup(ctx context.Context, ref fwtrading.OrderRef) (fwtrading.Snapshot, bool, error) {
	if c.reportDiag == nil {
		return fwtrading.Snapshot{}, true, errors.New("openalgo: lookup requires a diagnostic reporter")
	}
	if ref.VenueOrderID == "" {
		remediation := "Find the order in OpenAlgo's order book or Action Center and check it at the broker."
		if c.mode == domain.TradingModePaper {
			remediation = "With OpenAlgo in analyzer mode, find the order in its order book or Action Center."
		}
		c.reportLookupDiagnostic(ref, health.Diagnostic{
			Level: health.DiagError, Code: health.CodeInconsistentData, Kind: health.DiagKindProvider,
			Title: "OpenAlgo order outcome unknown",
			Detail: "Officer has no OpenAlgo order id for an order whose send outcome is unknown, " +
				"and OpenAlgo has no client order id to find it by.",
			Remediation: "Officer keeps the order pending and cannot reconcile it by itself. " +
				remediation,
		})
		return fwtrading.Snapshot{}, true, fmt.Errorf("openalgo: lookup requires a venue order id: %w", domain.ErrUpstream)
	}
	status, err := c.client.OrderStatus(ctx, ref.VenueOrderID)
	if err != nil {
		if errors.Is(err, openalgo.ErrInvalidResponse) {
			c.reportLookupDiagnostic(ref, venueLookupUnparsable("OpenAlgo returned an order status Officer cannot read."))
		}
		var apiErr *openalgo.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound && apiErr.EnvelopeDecoded {
			if err := c.checkLookupMode(ref, apiErr.Mode); err != nil {
				return fwtrading.Snapshot{}, true, err
			}
			detail := "OpenAlgo's order book does not list an order Officer sent; " +
				"OpenAlgo's order and trade books cover the current trading day only."
			remediation := "Officer keeps the order pending and cannot reconcile it after its trading day. " +
				"Check the order at the broker."
			if c.mode == domain.TradingModePaper {
				detail = "OpenAlgo's analyzer has no order with the id Officer recorded for it; " +
					"analyzer orders belong to the OpenAlgo user of the connection's API key."
				remediation = "Officer keeps the order pending and cannot reconcile it by itself. " +
					"Check that the connection's API key belongs to the OpenAlgo user that placed the order."
			}
			c.reportLookupDiagnostic(ref, health.Diagnostic{
				Level: health.DiagError, Code: health.CodeInconsistentData, Kind: health.DiagKindProvider,
				Title:       "OpenAlgo no longer lists the order",
				Detail:      detail,
				Remediation: remediation,
			})
			return fwtrading.Snapshot{}, true, fmt.Errorf("openalgo: order no longer listed: %w: %w", domain.ErrUpstream, err)
		}
		return fwtrading.Snapshot{}, true, err
	}
	if err := c.checkLookupMode(ref, status.Mode); err != nil {
		return fwtrading.Snapshot{}, true, err
	}
	if string(status.OrderID) != ref.VenueOrderID {
		c.reportLookupDiagnostic(ref, venueLookupUnparsable("OpenAlgo order lookup returned a different order id."))
		return fwtrading.Snapshot{}, true, fmt.Errorf("openalgo: lookup order id mismatch: %w", domain.ErrUpstream)
	}
	trades, err := c.client.TradeBook(ctx)
	if err != nil {
		if errors.Is(err, openalgo.ErrInvalidResponse) {
			c.reportLookupDiagnostic(ref, venueLookupUnparsable("OpenAlgo returned a trade book Officer cannot read."))
		}
		return fwtrading.Snapshot{}, true, err
	}
	if err := c.checkLookupMode(ref, trades.Mode); err != nil {
		return fwtrading.Snapshot{}, true, err
	}
	c.closedMu.Lock()
	terminalFilled := c.closedFills[ref.VenueOrderID]
	c.closedMu.Unlock()
	snapshot, err := openAlgoSnapshot(status, trades.Trades, terminalFilled)
	if err != nil {
		switch {
		case errors.Is(err, errOpenAlgoCancelledFillsUnconfirmed):
			remediation := "If this stays, compare Officer's fills with the order's fills at the broker."
			if c.mode == domain.TradingModePaper {
				remediation = "If this stays, compare Officer's fills with OpenAlgo's trade book in analyzer mode."
			}
			c.reportLookupDiagnostic(ref, health.Diagnostic{
				Level: health.DiagError, Code: health.CodeInconsistentData, Kind: health.DiagKindProvider,
				Title: "OpenAlgo fills of a closed order are unconfirmed",
				Detail: "OpenAlgo closed the order (cancelled, rejected or expired), " +
					"and Officer cannot confirm its filled quantity against the trade book.",
				Remediation: "Officer keeps the order pending and retries; it books the fills once the trade book " +
					"matches the filled quantity OpenAlgo's order stream reported. " +
					remediation,
			})
		case errors.Is(err, errOpenAlgoFillsIncomplete):
			if c.mode == domain.TradingModePaper {
				c.reportLookupDiagnostic(ref, health.Diagnostic{
					Level: health.DiagError, Code: health.CodeInconsistentData, Kind: health.DiagKindProvider,
					Title: "OpenAlgo fills of a complete order are missing",
					Detail: "OpenAlgo reports the order as complete, but its analyzer trade book does not show all of its fills; " +
						"the analyzer trade book covers the current session only.",
					Remediation: "Officer keeps the order pending and retries; it books the fills once the trade book shows them. " +
						"If this stays, the order's session has ended and OpenAlgo no longer shows its fills, so Officer cannot book them by itself.",
				})
			}
		default:
			c.reportLookupDiagnostic(ref, venueLookupUnparsable("OpenAlgo order lookup returned order or trade data Officer cannot interpret."))
		}
		return fwtrading.Snapshot{}, true, err
	}
	if snapshot.Status == fwtrading.VenueStatusFilled || snapshot.Status == fwtrading.VenueStatusCancelled {
		c.finishOrder(ref.VenueOrderID)
		c.forgetClosedQuantity(ref.VenueOrderID)
	} else {
		c.mapOrder(ref.VenueOrderID, ref.ClientOrderID)
	}
	return snapshot, true, nil
}

func (c *openAlgoConnector) checkLookupMode(ref fwtrading.OrderRef, mode string) error {
	if mode == c.expectedRESTMode() {
		return nil
	}
	reportedMode := "live"
	if mode == "analyze" {
		reportedMode = "analyzer"
	} else if mode != "" {
		reportedMode = fmt.Sprintf("%q", mode)
	}
	c.reportLookupDiagnostic(ref, health.Diagnostic{
		Level: health.DiagError, Code: health.CodeInvalidProviderConfig, Kind: health.DiagKindConfig,
		Title:  "OpenAlgo order mode differs from the connection",
		Detail: fmt.Sprintf("OpenAlgo answered in %s mode on a %s connection.", reportedMode, c.mode),
		Remediation: "Officer keeps the order pending and books nothing OpenAlgo reports in the other mode. " +
			"Set OpenAlgo's mode to match the connection.",
	})
	return fmt.Errorf("openalgo: lookup reply mode differs from the connection: %w", domain.ErrUpstream)
}

func (c *openAlgoConnector) reportLookupDiagnostic(ref fwtrading.OrderRef, diag health.Diagnostic) {
	diag.Instrument = c.redact.Replace(ref.Symbol)
	c.reportDiag(diag)
}

func (c *openAlgoConnector) VerifySymbol(ctx context.Context, external string) (fwtrading.SymbolVerification, error) {
	exists, details, err := openalgo.VerifySpotSymbol(ctx, c.client, external)
	if err != nil {
		return fwtrading.SymbolVerification{}, err
	}
	return fwtrading.SymbolVerification{
		Exists: exists, Tradable: exists, Details: details,
	}, nil
}

func (c *openAlgoConnector) SearchSymbols(ctx context.Context, query string) ([]fwtrading.SymbolMatch, error) {
	rows, err := openalgo.SearchSpotSymbols(ctx, c.client, query)
	if err != nil {
		return nil, err
	}
	matches := make([]fwtrading.SymbolMatch, 0)
	for _, row := range rows {
		matches = append(matches, fwtrading.SymbolMatch{
			Symbol: row.Exchange + ":" + row.Symbol, Name: row.Name,
			Class: row.InstrumentType, Exchange: row.Exchange, Tradable: true,
		})
	}
	return matches, nil
}

func (c *openAlgoConnector) SetDiagnosticReporter(report health.DiagnosticReporter) {
	c.reportDiag = report
}
