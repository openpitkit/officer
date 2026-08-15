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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	krakenStreamURL       = "wss://ws.kraken.com/v2"
	krakenAssetPairsURL   = "https://api.kraken.com/0/public/AssetPairs?assetVersion=1"
	krakenDiagnoseTimeout = 10 * time.Second

	defaultKrakenReconnectMin = 250 * time.Millisecond
	defaultKrakenReconnectMax = 5 * time.Second
	defaultKrakenReadTimeout  = 30 * time.Second
)

type krakenConnector struct {
	dial         func(context.Context, string) (krakenConn, error)
	sleep        func(context.Context, time.Duration) error
	fetchSymbols func(ctx context.Context) (map[string]struct{}, error)
	report       StatusReporter
	diagReport   DiagnosticReporter
	reconnectMin time.Duration
	reconnectMax time.Duration
	readTimeout  time.Duration

	// subs is set by Subscribe and read later by Diagnose/watchdog paths. The
	// manager wires those after Subscribe returns, so no extra lock is needed.
	subs []krakenSubscription

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type krakenConn interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close(websocket.StatusCode, string) error
}

type liveKrakenConn struct {
	conn *websocket.Conn
}

type krakenSubscription struct {
	Subscription
	symbol string
}

type krakenSubscribeRequest struct {
	Method string                `json:"method"`
	Params krakenSubscribeParams `json:"params"`
}

type krakenSubscribeParams struct {
	Channel      string   `json:"channel"`
	Symbol       []string `json:"symbol"`
	EventTrigger string   `json:"event_trigger"`
	Snapshot     bool     `json:"snapshot"`
}

type krakenFrameHeader struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
	Method  string          `json:"method"`
	Channel string          `json:"channel"`
	Error   string          `json:"error"`
	Type    string          `json:"type"`
}

func NewKrakenConnector() *krakenConnector {
	return &krakenConnector{
		dial:         dialKraken,
		sleep:        sleepContext,
		fetchSymbols: fetchKrakenSymbols,
		reconnectMin: defaultKrakenReconnectMin,
		reconnectMax: defaultKrakenReconnectMax,
		readTimeout:  defaultKrakenReadTimeout,
	}
}

func dialKraken(ctx context.Context, streamURL string) (krakenConn, error) {
	conn, _, err := websocket.Dial(ctx, streamURL, nil)
	if err != nil {
		return nil, err
	}
	return liveKrakenConn{conn: conn}, nil
}

func (c liveKrakenConn) Read(ctx context.Context) ([]byte, error) {
	_, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func (c liveKrakenConn) Write(ctx context.Context, payload []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func (c liveKrakenConn) Close(code websocket.StatusCode, reason string) error {
	return c.conn.Close(code, reason)
}

func (c *krakenConnector) SetStatusReporter(report StatusReporter) {
	c.report = report
}

func (c *krakenConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

func (c *krakenConnector) SetDiagnosticReporter(report DiagnosticReporter) {
	c.diagReport = report
}

func (c *krakenConnector) reportDiag(diag Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

func (c *krakenConnector) Diagnose(ctx context.Context) ([]Diagnostic, error) {
	diagCtx, cancel := context.WithTimeout(ctx, krakenDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(diagCtx)
	if err != nil {
		return nil, err
	}

	var findings []Diagnostic
	for _, sub := range c.subs {
		if _, ok := known[sub.symbol]; !ok {
			findings = append(findings, krakenUnknownSymbolDiag(sub, known))
		}
	}
	return findings, nil
}

func (c *krakenConnector) References() (ProviderReferences, bool) {
	return ProviderReferences{
		DocsURL:    "https://docs.kraken.com/exchange/api-reference/spot-websocket-v2/ticker",
		SymbolsURL: krakenAssetPairsURL,
	}, true
}

func (c *krakenConnector) VerifySymbol(
	ctx context.Context, external string,
) (SymbolVerification, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, krakenDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(verifyCtx)
	if err != nil {
		return SymbolVerification{}, err
	}

	symbol := strings.TrimSpace(external)
	if symbol == "" {
		return SymbolVerification{}, nil
	}
	if _, ok := known[symbol]; ok {
		return SymbolVerification{Exists: true}, nil
	}
	canonical := canonicalKrakenSymbol(symbol)
	if canonical != symbol {
		if _, ok := known[canonical]; ok {
			return SymbolVerification{Suggestion: canonical}, nil
		}
	}
	if folded := strings.ToUpper(symbol); folded != symbol && folded != canonical {
		if _, ok := known[folded]; ok {
			return SymbolVerification{Suggestion: folded}, nil
		}
	}
	return SymbolVerification{}, nil
}

func (c *krakenConnector) SearchSymbols(
	ctx context.Context, query SymbolSearchQuery,
) ([]SymbolMatch, error) {
	searchCtx, cancel := context.WithTimeout(ctx, krakenDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(searchCtx)
	if err != nil {
		return nil, err
	}
	return searchSymbolsFromSet(known, query, "SPOT"), nil
}

func (c *krakenConnector) Subscribe(
	ctx context.Context, subs []Subscription,
) (<-chan QuoteUpdate, error) {
	normalized, err := normalizeKrakenSubscriptions(subs)
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

func (c *krakenConnector) run(
	ctx context.Context, subs []krakenSubscription, out chan<- QuoteUpdate,
) {
	subs = c.validateSymbols(ctx, subs)
	if len(subs) == 0 {
		return
	}

	attempt := 0
	for {
		received, err := c.stream(ctx, subs, out)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}

		c.reportStatus(false, "stream error: "+err.Error())
		if received {
			attempt = 0
		}
		attempt++
		if err := c.sleep(ctx, backoff(attempt, c.reconnectMin, c.reconnectMax)); err != nil {
			return
		}
	}
}

func (c *krakenConnector) validateSymbols(
	ctx context.Context, subs []krakenSubscription,
) []krakenSubscription {
	if c.fetchSymbols == nil {
		return subs
	}
	validateCtx, cancel := context.WithTimeout(ctx, krakenDiagnoseTimeout)
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
		if _, ok := known[sub.symbol]; ok {
			valid = append(valid, sub)
		} else {
			c.reportDiag(krakenUnknownSymbolDiag(sub, known))
		}
	}
	return valid
}

func krakenUnknownSymbolDiag(
	sub krakenSubscription, known map[string]struct{},
) Diagnostic {
	remediation := "Remove this instrument and add one with a valid Kraken WS symbol" +
		" (e.g. BTC/USD). See the valid symbols list."
	suggestions := symbolPrefixSuggestionsFromSet(known, sub.External)
	if len(suggestions) > 0 {
		remediation += " Did you mean: " + strings.Join(suggestions, ", ") + "?"
	}
	return Diagnostic{
		Level:       DiagError,
		Code:        CodeUnknownSymbol,
		Kind:        DiagKindConfig,
		Instrument:  sub.External,
		Title:       fmt.Sprintf("Symbol %q not found on Kraken", sub.External),
		Detail:      fmt.Sprintf("The configured external symbol %q is not a Kraken WS ticker symbol.", sub.External),
		Remediation: remediation,
		Actions: []DiagnosticAction{
			{Type: ActionRemoveInstrument, Target: sub.External},
			{Type: ActionOpenSymbols},
		},
	}
}

func (c *krakenConnector) stream(
	ctx context.Context, subs []krakenSubscription, out chan<- QuoteUpdate,
) (bool, error) {
	conn, err := c.dial(ctx, krakenStreamURL)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	payload, err := krakenSubscribePayload(subs)
	if err != nil {
		return false, err
	}
	if err := conn.Write(ctx, payload); err != nil {
		return false, err
	}
	c.reportStatus(true, "")

	const unparsableThreshold = 3
	var framesReceived, framesParsed int
	unparsableReported := false

	for {
		payload, err := readStreamFrame(ctx, c.readTimeout, conn.Read)
		if err != nil {
			return framesParsed > 0, err
		}

		msg, ok := krakenSubscribeError(payload)
		if ok {
			c.reportStatus(false, "kraken: "+msg)
			continue
		}

		updates, ok := parseKrakenQuoteUpdates(payload, subs)
		if !ok {
			if krakenFrameIsRoutine(payload) {
				continue
			}
			if !unparsableReported {
				framesReceived++
				if framesReceived >= unparsableThreshold && framesParsed == 0 {
					unparsableReported = true
					c.reportDiag(Diagnostic{
						Level:       DiagError,
						Code:        CodeUnparsableData,
						Kind:        DiagKindProvider,
						Title:       "Receiving data but cannot parse it",
						Detail:      "The source is sending data but Officer could not decode it (likely a format mismatch).",
						Remediation: "This is an internal issue, not your configuration - please report it.",
						Actions:     []DiagnosticAction{{Type: ActionRestart}},
					})
				}
			}
			continue
		}

		framesParsed += len(updates)
		for _, update := range updates {
			select {
			case <-ctx.Done():
				return framesParsed > 0, ctx.Err()
			case out <- update:
			}
		}
	}
}

func (c *krakenConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}

type krakenAssetPairsResponse struct {
	Result map[string]struct{} `json:"result"`
	Error  []string            `json:"error"`
}

func fetchKrakenSymbols(ctx context.Context) (map[string]struct{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, krakenAssetPairsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kraken AssetPairs: unexpected status %d", resp.StatusCode)
	}
	var pairs krakenAssetPairsResponse
	if err := json.NewDecoder(resp.Body).Decode(&pairs); err != nil {
		return nil, fmt.Errorf("kraken AssetPairs: decode: %w", err)
	}
	if len(pairs.Error) > 0 {
		return nil, fmt.Errorf("kraken AssetPairs: %s", strings.Join(pairs.Error, "; "))
	}
	return krakenSymbolsFromAssetPairs(pairs), nil
}

func krakenSymbolsFromAssetPairs(pairs krakenAssetPairsResponse) map[string]struct{} {
	set := make(map[string]struct{}, len(pairs.Result))
	for symbol := range pairs.Result {
		symbol = canonicalKrakenSymbol(symbol)
		if symbol != "" {
			set[symbol] = struct{}{}
		}
	}
	return set
}

func normalizeKrakenSubscriptions(
	subs []Subscription,
) ([]krakenSubscription, error) {
	normalized := make([]krakenSubscription, 0, len(subs))
	for _, sub := range subs {
		symbol := strings.TrimSpace(sub.External)
		if symbol == "" {
			return nil, missingExternalSymbolError("kraken", sub)
		}
		symbol = canonicalKrakenSymbol(symbol)
		if symbol == "" {
			return nil, invalidExternalSymbolError("kraken", sub.External)
		}
		normalized = append(normalized, krakenSubscription{
			Subscription: sub,
			symbol:       symbol,
		})
	}
	return normalized, nil
}

func krakenSubscribePayload(subs []krakenSubscription) ([]byte, error) {
	symbols := make([]string, 0, len(subs))
	for _, sub := range subs {
		symbols = append(symbols, sub.symbol)
	}
	slices.Sort(symbols)
	payload, err := json.Marshal(krakenSubscribeRequest{
		Method: "subscribe",
		Params: krakenSubscribeParams{
			Channel:      "ticker",
			Symbol:       symbols,
			EventTrigger: "bbo",
			Snapshot:     true,
		},
	})
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func krakenFrameIsRoutine(payload []byte) bool {
	var frame krakenFrameHeader
	if err := json.Unmarshal(payload, &frame); err != nil {
		return false
	}
	if frame.Channel == "heartbeat" || frame.Channel == "status" {
		return true
	}
	return frame.Method == "subscribe" && (frame.Success == nil || *frame.Success)
}

func krakenSubscribeError(payload []byte) (string, bool) {
	var frame krakenFrameHeader
	if err := json.Unmarshal(payload, &frame); err != nil {
		return "", false
	}
	if frame.Method != "subscribe" || frame.Success == nil || *frame.Success {
		return "", false
	}
	if frame.Error != "" {
		return frame.Error, true
	}
	return "subscribe failed", true
}

func parseKrakenQuoteUpdates(
	payload []byte, subs []krakenSubscription,
) ([]QuoteUpdate, bool) {
	var frame krakenFrameHeader
	if err := json.Unmarshal(payload, &frame); err != nil {
		return nil, false
	}
	if frame.Channel != "ticker" || frame.Data == nil {
		return nil, false
	}

	// Decode rows into an exact-key map so future fields cannot collide through
	// Go's case-insensitive struct matching.
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(frame.Data, &rows); err != nil {
		return nil, false
	}
	updates := make([]QuoteUpdate, 0, len(rows))
	for _, fields := range rows {
		update, ok := quoteUpdateFromKrakenFields(fields, subs)
		if ok {
			updates = append(updates, update)
		}
	}
	if len(updates) == 0 {
		return nil, false
	}
	return updates, true
}

func quoteUpdateFromKrakenFields(
	fields map[string]json.RawMessage, subs []krakenSubscription,
) (QuoteUpdate, bool) {
	symbol := canonicalKrakenSymbol(rawString(fields["symbol"]))
	if symbol == "" {
		return QuoteUpdate{}, false
	}
	timestampText := rawString(fields["timestamp"])
	if timestampText == "" {
		return QuoteUpdate{}, false
	}
	asOf, err := time.Parse(time.RFC3339Nano, timestampText)
	if err != nil {
		return QuoteUpdate{}, false
	}

	for _, sub := range subs {
		if sub.symbol != symbol {
			continue
		}
		return QuoteUpdate{
			AsOf:  asOf.UTC(),
			Base:  sub.Base,
			Quote: sub.Quote,
			Mark:  rawDecimalString(fields["last"]),
			Bid:   rawDecimalString(fields["bid"]),
			Ask:   rawDecimalString(fields["ask"]),
		}, true
	}
	return QuoteUpdate{}, false
}

func canonicalKrakenSymbol(symbol string) string {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return ""
	}
	parts := strings.Split(symbol, "/")
	if len(parts) != 2 {
		return symbol
	}
	base := canonicalKrakenAsset(parts[0])
	quote := canonicalKrakenAsset(parts[1])
	if base == "" || quote == "" {
		return ""
	}
	return base + "/" + quote
}

func canonicalKrakenAsset(asset string) string {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	switch asset {
	case "XBT":
		return "BTC"
	case "XDG":
		return "DOGE"
	default:
		return asset
	}
}
