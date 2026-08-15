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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	bybitPublicFeedBaseURL  = "wss://stream.bybit.com/v5/public/"
	bybitInstrumentsBaseURL = "https://api.bybit.com/v5/market/instruments-info"
	defaultBybitCategory    = "spot"

	defaultBybitReconnectMin = 250 * time.Millisecond
	defaultBybitReconnectMax = 5 * time.Second
	defaultBybitPingInterval = 20 * time.Second
	bybitDiagnoseTimeout     = 10 * time.Second
)

type bybitConnector struct {
	dial         func(context.Context, string) (bybitConn, error)
	sleep        func(context.Context, time.Duration) error
	fetchSymbols func(context.Context, string) (map[string]struct{}, error)
	report       StatusReporter
	diagReport   DiagnosticReporter
	reconnectMin time.Duration
	reconnectMax time.Duration
	pingInterval time.Duration
	readTimeout  time.Duration
	category     string

	subs []bybitSubscription

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type bybitConn interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close(websocket.StatusCode, string) error
}

type liveBybitConn struct {
	conn *websocket.Conn
}

type bybitSubscription struct {
	Subscription
	symbol string
	topic  string
}

type bybitCredentials struct {
	Category string `json:"category"`
}

type bybitOpRequest struct {
	Op   string   `json:"op"`
	Args []string `json:"args,omitempty"`
}

type bybitTickerPatch struct {
	Symbol       string
	LastPrice    string
	Bid1Price    string
	Ask1Price    string
	HasSymbol    bool
	HasLastPrice bool
	HasBid1Price bool
	HasAsk1Price bool
}

type bybitTickerSnapshot struct {
	Symbol    string
	LastPrice string
	Bid1Price string
	Ask1Price string
}

type bybitTickerEvent struct {
	Patch bybitTickerPatch
	Type  string
	Topic string
	TS    int64
}

var _ Connector = (*bybitConnector)(nil)
var _ SymbolVerifier = (*bybitConnector)(nil)
var _ Diagnosable = (*bybitConnector)(nil)

// NewBybitConnector builds a Bybit v5 ticker-stream connector.
func NewBybitConnector(credentials string) (*bybitConnector, error) {
	category, err := bybitCategoryFromCredentials(credentials)
	if err != nil {
		return nil, err
	}
	return &bybitConnector{
		dial:         dialBybit,
		sleep:        sleepContext,
		fetchSymbols: fetchBybitSymbols,
		reconnectMin: defaultBybitReconnectMin,
		reconnectMax: defaultBybitReconnectMax,
		pingInterval: defaultBybitPingInterval,
		readTimeout:  defaultWebsocketReadTimeout,
		category:     category,
	}, nil
}

func dialBybit(ctx context.Context, feedURL string) (bybitConn, error) {
	conn, _, err := websocket.Dial(ctx, feedURL, nil)
	if err != nil {
		return nil, err
	}
	return liveBybitConn{conn: conn}, nil
}

func (c liveBybitConn) Read(ctx context.Context) ([]byte, error) {
	_, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func (c liveBybitConn) Write(ctx context.Context, payload []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func (c liveBybitConn) Close(code websocket.StatusCode, reason string) error {
	return c.conn.Close(code, reason)
}

// SetStatusReporter installs the manager's runtime reporter.
func (c *bybitConnector) SetStatusReporter(report StatusReporter) {
	c.report = report
}

func (c *bybitConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

// SetDiagnosticReporter installs the manager's structured diagnostic reporter.
func (c *bybitConnector) SetDiagnosticReporter(report DiagnosticReporter) {
	c.diagReport = report
}

func (c *bybitConnector) reportDiag(diag Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

// References returns the Bybit API documentation and instrument-list URLs.
func (c *bybitConnector) References() (ProviderReferences, bool) {
	return ProviderReferences{
		DocsURL:    "https://bybit-exchange.github.io/docs/v5/websocket/public/ticker",
		SymbolsURL: bybitInstrumentsURL(c.category),
	}, true
}

// VerifySymbol checks external against the Bybit instrument catalogue for the
// connector's configured category.
func (c *bybitConnector) VerifySymbol(
	ctx context.Context, external string,
) (SymbolVerification, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, bybitDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(verifyCtx, c.category)
	if err != nil {
		return SymbolVerification{}, err
	}
	return verifySymbolFromSet(known, external), nil
}

func (c *bybitConnector) SearchSymbols(
	ctx context.Context, query SymbolSearchQuery,
) ([]SymbolMatch, error) {
	searchCtx, cancel := context.WithTimeout(ctx, bybitDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(searchCtx, c.category)
	if err != nil {
		return nil, err
	}
	return searchSymbolsFromSet(known, query, strings.ToUpper(c.category)), nil
}

// Diagnose returns a diagnostic for each stored Bybit subscription whose symbol
// is not listed by the provider catalogue for the configured category.
func (c *bybitConnector) Diagnose(ctx context.Context) ([]Diagnostic, error) {
	diagCtx, cancel := context.WithTimeout(ctx, bybitDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(diagCtx, c.category)
	if err != nil {
		return nil, err
	}
	var findings []Diagnostic
	for _, sub := range c.subs {
		if _, ok := known[sub.symbol]; ok {
			continue
		}
		findings = append(findings, providerUnknownSymbolDiag(
			"Bybit",
			sub.External,
		))
	}
	return findings, nil
}

func (c *bybitConnector) Subscribe(
	ctx context.Context, subs []Subscription,
) (<-chan QuoteUpdate, error) {
	normalized, err := normalizeBybitSubscriptions(subs)
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

func (c *bybitConnector) run(
	ctx context.Context, subs []bybitSubscription, out chan<- QuoteUpdate,
) {
	subs = c.validateSymbols(ctx, subs)
	if len(subs) == 0 {
		c.reportStatus(false, "bybit: no valid symbols")
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

func (c *bybitConnector) validateSymbols(
	ctx context.Context, subs []bybitSubscription,
) []bybitSubscription {
	if c.fetchSymbols == nil {
		return subs
	}
	validateCtx, cancel := context.WithTimeout(ctx, bybitDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(validateCtx, c.category)
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
		if _, ok := known[sub.symbol]; ok {
			valid = append(valid, sub)
			continue
		}
		c.reportDiag(providerUnknownSymbolDiag(
			"Bybit",
			sub.External,
		))
	}
	return valid
}

func (c *bybitConnector) stream(
	ctx context.Context, subs []bybitSubscription, out chan<- QuoteUpdate,
) (bool, error) {
	conn, err := c.dial(ctx, bybitPublicFeedBaseURL+c.category)
	if err != nil {
		return false, err
	}
	c.reportStatus(true, "")
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	payload, err := bybitSubscribePayload(subs)
	if err != nil {
		return false, err
	}
	if err := conn.Write(ctx, payload); err != nil {
		return false, err
	}

	pingCtx, cancelPing := context.WithCancel(ctx)
	defer cancelPing()
	pingErr := make(chan error, 1)
	go bybitPingLoop(pingCtx, conn, c.pingInterval, pingErr)

	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	reads := websocketReadLoop(readCtx, conn.Read)
	state := make(map[string]bybitTickerSnapshot, len(subs))
	var unparsable unparsableTracker
	delivered := false

	for {
		payload, err := nextWebsocketRead(ctx, reads, c.readTimeout, pingErr)
		if err != nil {
			return delivered, err
		}
		if msg, ok := bybitControlError(payload); ok {
			c.reportStatus(false, "bybit: "+msg)
			continue
		}
		if bybitRoutineControl(payload) {
			continue
		}
		update, ok := parseBybitQuoteUpdate(payload, subs, state)
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

func (c *bybitConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}

func bybitCategoryFromCredentials(credentials string) (string, error) {
	trimmed := strings.TrimSpace(credentials)
	if trimmed == "" {
		return defaultBybitCategory, nil
	}
	var parsed bybitCredentials
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return "", fmt.Errorf("bybit credentials: %w", err)
	}
	category := strings.ToLower(strings.TrimSpace(parsed.Category))
	if category == "" {
		return defaultBybitCategory, nil
	}
	switch category {
	case "spot", "linear", "inverse", "option":
		return category, nil
	default:
		return "", fmt.Errorf("bybit category %q is not supported", parsed.Category)
	}
}

func bybitInstrumentsURL(category string) string {
	trimmed := strings.TrimSpace(category)
	if trimmed == "" {
		trimmed = defaultBybitCategory
	}
	values := url.Values{}
	values.Set("category", trimmed)
	return bybitInstrumentsBaseURL + "?" + values.Encode()
}

func normalizeBybitSubscriptions(
	subs []Subscription,
) ([]bybitSubscription, error) {
	normalized := make([]bybitSubscription, 0, len(subs))
	for _, sub := range subs {
		symbol := strings.TrimSpace(sub.External)
		if symbol == "" {
			return nil, missingExternalSymbolError("bybit", sub)
		}
		symbol = strings.ToUpper(symbol)
		if symbol == "" {
			return nil, missingExternalSymbolError("bybit", sub)
		}
		topic := "tickers." + symbol
		normalized = append(normalized, bybitSubscription{
			Subscription: sub,
			symbol:       symbol,
			topic:        topic,
		})
	}
	return normalized, nil
}

func bybitSubscribePayload(subs []bybitSubscription) ([]byte, error) {
	args := make([]string, 0, len(subs))
	for _, sub := range subs {
		args = append(args, sub.topic)
	}
	return json.Marshal(bybitOpRequest{Op: "subscribe", Args: args})
}

func bybitPingLoop(
	ctx context.Context,
	conn bybitConn,
	interval time.Duration,
	errs chan<- error,
) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			payload, err := json.Marshal(bybitOpRequest{Op: "ping"})
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
			if err := conn.Write(ctx, payload); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}
}

type bybitInstrumentsResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []struct {
			Symbol string `json:"symbol"`
		} `json:"list"`
		NextPageCursor string `json:"nextPageCursor"`
	} `json:"result"`
}

func fetchBybitSymbols(
	ctx context.Context, category string,
) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	cursor := ""
	for {
		reqURL, err := url.Parse(bybitInstrumentsBaseURL)
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
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		var payload bybitInstrumentsResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		closeErr := resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("bybit instruments: unexpected status %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("bybit instruments: decode: %w", decodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("bybit instruments: close body: %w", closeErr)
		}
		if payload.RetCode != 0 {
			msg := strings.TrimSpace(payload.RetMsg)
			if msg == "" {
				msg = "request failed"
			}
			return nil, fmt.Errorf("bybit instruments: %d: %s", payload.RetCode, msg)
		}
		for _, item := range payload.Result.List {
			if symbol := strings.TrimSpace(item.Symbol); symbol != "" {
				set[symbol] = struct{}{}
			}
		}
		cursor = strings.TrimSpace(payload.Result.NextPageCursor)
		if cursor == "" {
			return set, nil
		}
	}
}

func bybitControlError(payload []byte) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return "", false
	}
	if _, ok := fields["success"]; !ok {
		return "", false
	}
	success, ok := rawBool(fields["success"])
	if !ok || success {
		return "", false
	}
	msg := rawString(fields["ret_msg"])
	if msg == "" {
		msg = rawString(fields["retMsg"])
	}
	if msg == "" {
		msg = "request rejected"
	}
	if op := rawString(fields["op"]); op != "" {
		msg = op + ": " + msg
	}
	return msg, true
}

func bybitRoutineControl(payload []byte) bool {
	if strings.TrimSpace(string(payload)) == "pong" {
		return true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return false
	}
	if rawString(fields["op"]) == "pong" {
		return true
	}
	if success, ok := rawBool(fields["success"]); ok && success {
		return true
	}
	return false
}

func parseBybitQuoteUpdate(
	payload []byte,
	subs []bybitSubscription,
	state map[string]bybitTickerSnapshot,
) (QuoteUpdate, bool) {
	event, ok := parseBybitTickerEvent(payload)
	if !ok {
		return QuoteUpdate{}, false
	}
	if event.TS <= 0 {
		return QuoteUpdate{}, false
	}
	symbol := strings.ToUpper(strings.TrimSpace(event.Patch.Symbol))
	if symbol == "" {
		symbol = strings.TrimPrefix(strings.ToUpper(event.Topic), "TICKERS.")
	}
	if symbol == "" {
		return QuoteUpdate{}, false
	}
	for _, sub := range subs {
		if sub.symbol != symbol && !strings.EqualFold(sub.topic, event.Topic) {
			continue
		}
		snapshot, ok := mergeBybitTickerEvent(event, sub.symbol, state)
		if !ok {
			return QuoteUpdate{}, false
		}
		return QuoteUpdate{
			AsOf:  time.UnixMilli(event.TS).UTC(),
			Base:  sub.Base,
			Quote: sub.Quote,
			Mark:  snapshot.LastPrice,
			Bid:   snapshot.Bid1Price,
			Ask:   snapshot.Ask1Price,
		}, true
	}
	return QuoteUpdate{}, false
}

func parseBybitTickerEvent(payload []byte) (bybitTickerEvent, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return bybitTickerEvent{}, false
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(fields["data"], &data); err != nil {
		return bybitTickerEvent{}, false
	}
	event := bybitTickerEvent{
		Type:  rawString(fields["type"]),
		Topic: rawString(fields["topic"]),
		TS:    rawInt64(fields["ts"]),
		Patch: bybitTickerPatch{
			Symbol:       rawString(data["symbol"]),
			LastPrice:    rawString(data["lastPrice"]),
			Bid1Price:    rawString(data["bid1Price"]),
			Ask1Price:    rawString(data["ask1Price"]),
			HasSymbol:    hasJSONKey(data, "symbol"),
			HasLastPrice: hasJSONKey(data, "lastPrice"),
			HasBid1Price: hasJSONKey(data, "bid1Price"),
			HasAsk1Price: hasJSONKey(data, "ask1Price"),
		},
	}
	return event, true
}

func mergeBybitTickerEvent(
	event bybitTickerEvent,
	symbol string,
	state map[string]bybitTickerSnapshot,
) (bybitTickerSnapshot, bool) {
	if event.Type != "" && event.Type != "snapshot" && event.Type != "delta" {
		return bybitTickerSnapshot{}, false
	}
	if state == nil {
		state = make(map[string]bybitTickerSnapshot)
	}
	current, exists := state[symbol]
	if !exists && event.Type == "delta" {
		return bybitTickerSnapshot{}, false
	}
	if !exists || event.Type == "snapshot" || event.Type == "" {
		current = bybitTickerSnapshot{Symbol: symbol}
	}

	if event.Patch.HasSymbol && strings.TrimSpace(event.Patch.Symbol) != "" {
		current.Symbol = strings.ToUpper(strings.TrimSpace(event.Patch.Symbol))
	}
	if event.Patch.HasLastPrice {
		current.LastPrice = event.Patch.LastPrice
	}
	if event.Patch.HasBid1Price {
		current.Bid1Price = event.Patch.Bid1Price
	}
	if event.Patch.HasAsk1Price {
		current.Ask1Price = event.Patch.Ask1Price
	}
	if current.LastPrice == "" && current.Bid1Price == "" && current.Ask1Price == "" {
		return bybitTickerSnapshot{}, false
	}
	state[symbol] = current
	return current, true
}

func hasJSONKey(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}
