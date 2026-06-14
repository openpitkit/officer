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
// Please see https://openpit.dev and the OWNERS file for details.

package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	coinbaseFeedURL     = "wss://ws-feed.exchange.coinbase.com"
	coinbaseProductsURL = "https://api.exchange.coinbase.com/products"

	defaultCoinbaseReconnectMin = 250 * time.Millisecond
	defaultCoinbaseReconnectMax = 5 * time.Second
	coinbaseDiagnoseTimeout     = 10 * time.Second
)

type coinbaseConnector struct {
	dial         func(context.Context, string) (coinbaseConn, error)
	sleep        func(context.Context, time.Duration) error
	fetchSymbols func(context.Context) (map[string]struct{}, error)
	report       StatusReporter
	diagReport   DiagnosticReporter
	reconnectMin time.Duration
	reconnectMax time.Duration
	readTimeout  time.Duration

	subs []coinbaseSubscription

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type coinbaseConn interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close(websocket.StatusCode, string) error
}

type liveCoinbaseConn struct {
	conn *websocket.Conn
}

type coinbaseSubscription struct {
	Subscription
	productID string
}

type coinbaseSubscribeRequest struct {
	Type       string   `json:"type"`
	Channels   []string `json:"channels"`
	ProductIDs []string `json:"product_ids"`
}

// coinbaseTickerEvent holds ticker fields extracted by exact JSON keys. That
// keeps the adapter aligned with Binance's case-twin defense instead of relying
// on Go's case-insensitive struct matching.
type coinbaseTickerEvent struct {
	Time      string
	Type      string
	ProductID string
	Price     string
	BestBid   string
	BestAsk   string
}

var _ Connector = (*coinbaseConnector)(nil)
var _ SymbolVerifier = (*coinbaseConnector)(nil)
var _ Diagnosable = (*coinbaseConnector)(nil)

// NewCoinbaseConnector builds a Coinbase Exchange ticker-stream connector.
func NewCoinbaseConnector() *coinbaseConnector {
	return &coinbaseConnector{
		dial:         dialCoinbase,
		sleep:        sleepContext,
		fetchSymbols: fetchCoinbaseSymbols,
		reconnectMin: defaultCoinbaseReconnectMin,
		reconnectMax: defaultCoinbaseReconnectMax,
		readTimeout:  defaultWebsocketReadTimeout,
	}
}

func dialCoinbase(ctx context.Context, feedURL string) (coinbaseConn, error) {
	conn, _, err := websocket.Dial(ctx, feedURL, nil)
	if err != nil {
		return nil, err
	}
	return liveCoinbaseConn{conn: conn}, nil
}

func (c liveCoinbaseConn) Read(ctx context.Context) ([]byte, error) {
	_, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func (c liveCoinbaseConn) Write(ctx context.Context, payload []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func (c liveCoinbaseConn) Close(code websocket.StatusCode, reason string) error {
	return c.conn.Close(code, reason)
}

// SetStatusReporter installs the manager's runtime reporter.
func (c *coinbaseConnector) SetStatusReporter(report StatusReporter) {
	c.report = report
}

func (c *coinbaseConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

// SetDiagnosticReporter installs the manager's structured diagnostic reporter.
func (c *coinbaseConnector) SetDiagnosticReporter(report DiagnosticReporter) {
	c.diagReport = report
}

func (c *coinbaseConnector) reportDiag(diag Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

// References returns the Coinbase API documentation and product-list URLs.
func (c *coinbaseConnector) References() (ProviderReferences, bool) {
	return ProviderReferences{
		DocsURL:    "https://docs.cdp.coinbase.com/exchange/websocket-feed/channels",
		SymbolsURL: coinbaseProductsURL,
	}, true
}

// VerifySymbol checks external against the Coinbase product catalogue.
func (c *coinbaseConnector) VerifySymbol(
	ctx context.Context, external string,
) (SymbolVerification, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, coinbaseDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(verifyCtx)
	if err != nil {
		return SymbolVerification{}, err
	}
	return verifySymbolFromSet(known, external), nil
}

func (c *coinbaseConnector) SearchSymbols(
	ctx context.Context, query SymbolSearchQuery,
) ([]SymbolMatch, error) {
	searchCtx, cancel := context.WithTimeout(ctx, coinbaseDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(searchCtx)
	if err != nil {
		return nil, err
	}
	return searchSymbolsFromSet(known, query, "SPOT"), nil
}

// Diagnose returns a diagnostic for each stored Coinbase subscription whose
// product id is not listed by the provider catalogue.
func (c *coinbaseConnector) Diagnose(ctx context.Context) ([]Diagnostic, error) {
	diagCtx, cancel := context.WithTimeout(ctx, coinbaseDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(diagCtx)
	if err != nil {
		return nil, err
	}
	var findings []Diagnostic
	for _, sub := range c.subs {
		if _, ok := known[sub.productID]; ok {
			continue
		}
		findings = append(findings, providerUnknownSymbolDiag(
			"Coinbase",
			sub.External,
			sub.Base,
			sub.Quote,
		))
	}
	return findings, nil
}

func (c *coinbaseConnector) Subscribe(
	ctx context.Context, subs []Subscription,
) (<-chan QuoteUpdate, error) {
	normalized, err := normalizeCoinbaseSubscriptions(subs)
	if err != nil {
		return nil, err
	}
	c.subs = normalized

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	out := make(chan QuoteUpdate)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(out)
		c.run(runCtx, normalized, out)
	}()
	return out, nil
}

func (c *coinbaseConnector) run(
	ctx context.Context, subs []coinbaseSubscription, out chan<- QuoteUpdate,
) {
	subs = c.validateSymbols(ctx, subs)
	if len(subs) == 0 {
		c.reportStatus(false, "coinbase: no valid symbols")
		return
	}
	attempt := 0
	for {
		delivered, err := c.stream(ctx, subs, out)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		if delivered {
			attempt = 0
		}
		c.reportStatus(false, "stream error: "+err.Error())
		attempt++
		if err := c.sleep(ctx, backoff(attempt, c.reconnectMin, c.reconnectMax)); err != nil {
			return
		}
	}
}

func (c *coinbaseConnector) validateSymbols(
	ctx context.Context, subs []coinbaseSubscription,
) []coinbaseSubscription {
	if c.fetchSymbols == nil {
		return subs
	}
	validateCtx, cancel := context.WithTimeout(ctx, coinbaseDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(validateCtx)
	if err != nil {
		c.reportDiag(Diagnostic{
			Level:       DiagWarn,
			Code:        CodeSelfDiagnosisFailed,
			Kind:        DiagKindProvider,
			Title:       "Self-diagnosis failed",
			Detail:      "symbol validation unavailable: " + err.Error(),
			Remediation: "Couldn't validate against the provider; try Restart feeds.",
			Actions:     []DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
		})
		return subs
	}

	valid := subs[:0:0]
	for _, sub := range subs {
		if _, ok := known[sub.productID]; ok {
			valid = append(valid, sub)
			continue
		}
		c.reportDiag(providerUnknownSymbolDiag(
			"Coinbase",
			sub.External,
			sub.Base,
			sub.Quote,
		))
	}
	return valid
}

func (c *coinbaseConnector) stream(
	ctx context.Context, subs []coinbaseSubscription, out chan<- QuoteUpdate,
) (bool, error) {
	conn, err := c.dial(ctx, coinbaseFeedURL)
	if err != nil {
		return false, err
	}
	c.reportStatus(true, "")
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	payload, err := coinbaseSubscribePayload(subs)
	if err != nil {
		return false, err
	}
	if err := conn.Write(ctx, payload); err != nil {
		return false, err
	}

	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	reads := websocketReadLoop(readCtx, conn.Read)
	var (
		unparsable unparsableTracker
		delivered  bool
	)
	for {
		payload, err := nextWebsocketRead(ctx, reads, c.readTimeout)
		if err != nil {
			return delivered, err
		}
		if msg, ok := coinbaseControlError(payload); ok {
			c.reportStatus(false, "coinbase: "+msg)
			continue
		}
		update, ok := parseCoinbaseQuoteUpdate(payload, subs)
		if !ok {
			unparsable.recordUnparsed(c.reportDiag)
			continue
		}
		unparsable.recordParsed()
		delivered = true
		select {
		case <-ctx.Done():
			return delivered, ctx.Err()
		case out <- update:
		}
	}
}

func (c *coinbaseConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}

func normalizeCoinbaseSubscriptions(
	subs []Subscription,
) ([]coinbaseSubscription, error) {
	normalized := make([]coinbaseSubscription, 0, len(subs))
	for _, sub := range subs {
		productID := strings.TrimSpace(sub.External)
		if productID == "" {
			base := strings.TrimSpace(sub.Base)
			quote := strings.TrimSpace(sub.Quote)
			productID = base + "-" + quote
		}
		productID = strings.ToUpper(productID)
		if strings.Trim(productID, "-") == "" {
			return nil, fmt.Errorf("coinbase subscription %s/%s: empty symbol", sub.Base, sub.Quote)
		}
		normalized = append(normalized, coinbaseSubscription{
			Subscription: sub,
			productID:    productID,
		})
	}
	return normalized, nil
}

func coinbaseSubscribePayload(subs []coinbaseSubscription) ([]byte, error) {
	productIDs := make([]string, 0, len(subs))
	for _, sub := range subs {
		productIDs = append(productIDs, sub.productID)
	}
	req := coinbaseSubscribeRequest{
		Type:       "subscribe",
		ProductIDs: productIDs,
		Channels:   []string{"ticker"},
	}
	return json.Marshal(req)
}

type coinbaseProduct struct {
	ID string `json:"id"`
}

func fetchCoinbaseSymbols(ctx context.Context) (map[string]struct{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coinbaseProductsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coinbase products: unexpected status %d", resp.StatusCode)
	}
	var products []coinbaseProduct
	if err := json.NewDecoder(resp.Body).Decode(&products); err != nil {
		return nil, fmt.Errorf("coinbase products: decode: %w", err)
	}
	set := make(map[string]struct{}, len(products))
	for _, product := range products {
		if id := strings.TrimSpace(product.ID); id != "" {
			set[id] = struct{}{}
		}
	}
	return set, nil
}

func parseCoinbaseQuoteUpdate(
	payload []byte, subs []coinbaseSubscription,
) (QuoteUpdate, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return QuoteUpdate{}, false
	}
	message := coinbaseTickerEvent{
		Type:      rawString(fields["type"]),
		Time:      rawString(fields["time"]),
		ProductID: rawString(fields["product_id"]),
		Price:     rawString(fields["price"]),
		BestBid:   rawString(fields["best_bid"]),
		BestAsk:   rawString(fields["best_ask"]),
	}
	if message.Type != "ticker" {
		return QuoteUpdate{}, false
	}
	asOf, err := time.Parse(time.RFC3339Nano, message.Time)
	if err != nil {
		return QuoteUpdate{}, false
	}
	productID := strings.ToUpper(strings.TrimSpace(message.ProductID))
	if productID == "" {
		return QuoteUpdate{}, false
	}
	if message.Price == "" && message.BestBid == "" && message.BestAsk == "" {
		return QuoteUpdate{}, false
	}
	for _, sub := range subs {
		if sub.productID != productID {
			continue
		}
		return QuoteUpdate{
			AsOf:  asOf.UTC(),
			Base:  sub.Base,
			Quote: sub.Quote,
			Mark:  message.Price,
			Bid:   message.BestBid,
			Ask:   message.BestAsk,
		}, true
	}
	return QuoteUpdate{}, false
}

func coinbaseControlError(payload []byte) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return "", false
	}
	if rawString(fields["type"]) != "error" {
		return "", false
	}
	msg := rawString(fields["message"])
	if msg == "" {
		msg = "request rejected"
	}
	if reason := rawString(fields["reason"]); reason != "" {
		msg += ": " + reason
	}
	return msg, true
}
