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
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
)

const (
	binanceStreamBaseURL   = "wss://data-stream.binance.vision/stream"
	binanceExchangeInfoURL = "https://data-api.binance.vision/api/v3/exchangeInfo"
	binanceDiagnoseTimeout = 10 * time.Second
	binanceReadLimit       = 64 << 10

	defaultBinanceReconnectMin = 250 * time.Millisecond
	defaultBinanceReconnectMax = 5 * time.Second
	defaultBinanceReadTimeout  = 30 * time.Second
)

type binanceConnector struct {
	dial         func(context.Context, string) (binanceConn, error)
	sleep        func(context.Context, time.Duration) error
	fetchSymbols func(ctx context.Context) (map[string]struct{}, error)
	report       fwmarketdata.StatusReporter
	diagReport   fwmarketdata.DiagnosticReporter
	reconnectMin time.Duration
	reconnectMax time.Duration
	readTimeout  time.Duration

	// subs is the full normalized subscription set Diagnose reads from the
	// watchdog goroutine. Subscribe assigns it before starting run, so Diagnose
	// observes a stable slice; runtime filtering must not mutate this backing
	// array.
	subs []binanceSubscription

	active  binanceConn
	closing bool
	cancel  context.CancelFunc
	mu      sync.Mutex
	wg      sync.WaitGroup
}

type binanceConn interface {
	Read(context.Context) ([]byte, error)
	Close(websocket.StatusCode, string) error
}

type liveBinanceConn struct {
	conn *websocket.Conn
}

type binanceSubscription struct {
	fwmarketdata.Subscription
	symbol string
	stream string
}

type binanceCombinedMessage struct {
	Data json.RawMessage `json:"data"`
}

// binanceControlFrame matches the error/ack frames Binance sends on the
// combined-stream endpoint instead of ticker data; a normal ticker frame has
// no top-level "error", so Error stays nil.
type binanceControlFrame struct {
	Error *binanceErrorDetail `json:"error"`
}

type binanceErrorDetail struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// binanceTickerEvent holds the fields extracted from a 24hrTicker data object.
// It is populated by exact-key lookup from a map[string]json.RawMessage, never
// via struct-tag unmarshal, so Go's case-insensitive key matching cannot route
// "C" (statistics close time, a number) into the "c" (last price, a string)
// field or cause any other case-twin collision.
type binanceTickerEvent struct {
	EventTime int64
	Symbol    string
	Last      string
	Bid       string
	Ask       string
}

func NewBinanceConnector() *binanceConnector {
	return &binanceConnector{
		dial:         dialBinance,
		sleep:        sleepContext,
		fetchSymbols: fetchBinanceSymbols,
		reconnectMin: defaultBinanceReconnectMin,
		reconnectMax: defaultBinanceReconnectMax,
		readTimeout:  defaultBinanceReadTimeout,
	}
}

func dialBinance(ctx context.Context, streamURL string) (binanceConn, error) {
	conn, _, err := websocket.Dial(ctx, streamURL, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(binanceReadLimit)
	return liveBinanceConn{conn: conn}, nil
}

func (c liveBinanceConn) Read(ctx context.Context) ([]byte, error) {
	_, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func (c liveBinanceConn) Close(code websocket.StatusCode, reason string) error {
	return c.conn.Close(code, reason)
}

// SetStatusReporter installs the manager's runtime reporter. The manager calls
// it before Subscribe, so the background goroutine reads a stable value.
func (c *binanceConnector) SetStatusReporter(report fwmarketdata.StatusReporter) {
	c.report = report
}

// reportStatus forwards a connection-state transition to the reporter, no-op
// when none is installed.
func (c *binanceConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

// SetDiagnosticReporter installs the manager's diagnostic reporter. The manager
// calls it before Subscribe, so the background goroutine reads a stable value.
func (c *binanceConnector) SetDiagnosticReporter(report fwmarketdata.DiagnosticReporter) {
	c.diagReport = report
}

// reportDiag forwards a structured diagnostic to the reporter, no-op when none
// is installed.
func (c *binanceConnector) reportDiag(diag fwmarketdata.Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

// Diagnose fetches the Binance exchangeInfo and returns a structured diagnostic
// for each stored subscription whose uppercased symbol is not listed. An empty
// slice means all symbols are valid. Returns an error on HTTP/decode failure so
// the manager can record a self_diagnosis_failed diagnostic.
func (c *binanceConnector) Diagnose(ctx context.Context) ([]fwmarketdata.Diagnostic, error) {
	diagCtx, cancel := context.WithTimeout(ctx, binanceDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(diagCtx)
	if err != nil {
		return nil, err
	}

	var findings []fwmarketdata.Diagnostic
	for _, sub := range c.subs {
		if _, ok := known[sub.symbol]; !ok {
			findings = append(findings, binanceUnknownSymbolDiag(sub, known))
		}
	}
	return findings, nil
}

// References returns the Binance API documentation and symbol list URLs.
func (c *binanceConnector) References() (fwmarketdata.ProviderReferences, bool) {
	return fwmarketdata.ProviderReferences{
		DocsURL:    "https://developers.binance.com/docs/binance-spot-api-docs/web-socket-streams",
		SymbolsURL: binanceExchangeInfoURL,
	}, true
}

// VerifySymbol checks external against the Binance symbol catalogue. The
// catalogue is keyed by uppercased symbols (see fetchBinanceSymbols), so a
// trimmed exact-case hit means the operator typed a listed symbol -> Exists.
// Otherwise an uppercased lookup finds a case-folded variant the operator likely
// meant and returns it as a suggestion. A blank external never exists and yields
// no suggestion. An error is returned only when the catalogue fetch fails.
func (c *binanceConnector) VerifySymbol(
	ctx context.Context, external string,
) (fwmarketdata.SymbolVerification, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, binanceDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(verifyCtx)
	if err != nil {
		return fwmarketdata.SymbolVerification{}, err
	}

	symbol := strings.TrimSpace(external)
	if symbol == "" {
		return fwmarketdata.SymbolVerification{}, nil
	}
	if _, ok := known[symbol]; ok {
		return fwmarketdata.SymbolVerification{Exists: true}, nil
	}
	// Not present as typed; surface the canonical (uppercased) variant when the
	// catalogue lists it, so the UI can offer "did you mean ETHUSDT?".
	if folded := strings.ToUpper(symbol); folded != symbol {
		if _, ok := known[folded]; ok {
			return fwmarketdata.SymbolVerification{Suggestion: folded}, nil
		}
	}
	return fwmarketdata.SymbolVerification{}, nil
}

func (c *binanceConnector) SearchSymbols(
	ctx context.Context, query fwmarketdata.SymbolSearchQuery,
) ([]fwmarketdata.SymbolMatch, error) {
	searchCtx, cancel := context.WithTimeout(ctx, binanceDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(searchCtx)
	if err != nil {
		return nil, err
	}
	return searchSymbolsFromSet(known, query, "SPOT"), nil
}

func (c *binanceConnector) Subscribe(
	ctx context.Context, subs []fwmarketdata.Subscription,
) (<-chan fwmarketdata.QuoteUpdate, error) {
	normalized, err := normalizeBinanceSubscriptions(subs)
	if err != nil {
		return nil, err
	}
	c.subs = normalized

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.closing = false
	c.mu.Unlock()

	out := make(chan fwmarketdata.QuoteUpdate)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(out)
		c.run(runCtx, normalized, out)
	}()
	return out, nil
}

func (c *binanceConnector) run(
	ctx context.Context, subs []binanceSubscription, out chan<- fwmarketdata.QuoteUpdate,
) {
	// Pre-flight: validate configured symbols once before dialing. This runs in
	// the connector's goroutine, never under any manager lock. reportDiag locks
	// the manager internally and is safe to call from here.
	subs = c.validateSymbols(ctx, subs)
	if len(subs) == 0 {
		c.reportStatus(false, "binance: no valid symbols")
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

		// A real failure (dial/stream): surface it before backing off; clean
		// shutdown took the context.Canceled branch above and is not reported.
		c.reportStatus(false, "stream error: "+err.Error())
		attempt++
		if err := c.sleep(ctx, backoff(attempt, c.reconnectMin, c.reconnectMax)); err != nil {
			return
		}
	}
}

// validateSymbols fetches the Binance symbol list once (bounded by
// binanceDiagnoseTimeout), partitions subs into valid and dropped, reports each
// dropped symbol via diagReport, and returns only the valid subset. On fetch
// error it warns and returns all subs unchanged (graceful degradation).
// Called from the connector's goroutine; never under any manager lock.
func (c *binanceConnector) validateSymbols(
	ctx context.Context, subs []binanceSubscription,
) []binanceSubscription {
	// No symbol fetcher configured: pre-flight validation is unavailable, so
	// proceed with the subs unchanged (same graceful degradation as a fetch
	// error below).
	if c.fetchSymbols == nil {
		return subs
	}
	validateCtx, cancel := context.WithTimeout(ctx, binanceDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(validateCtx)
	if err != nil {
		c.reportDiag(fwmarketdata.Diagnostic{
			Level:       DiagWarn,
			Code:        CodeSelfDiagnosisFailed,
			Kind:        DiagKindProvider,
			Title:       "Self-diagnosis failed",
			Detail:      "symbol validation unavailable: " + err.Error(),
			Remediation: "Couldn't validate against the provider; try Restart feeds.",
			Actions:     []fwmarketdata.DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
		})
		return subs
	}

	// Keep c.subs intact for Diagnose: it may run concurrently later and must see
	// every configured symbol, including the ones this runtime pass drops.
	valid := make([]binanceSubscription, 0, len(subs))
	for _, sub := range subs {
		if _, ok := known[sub.symbol]; ok {
			valid = append(valid, sub)
		} else {
			c.reportDiag(binanceUnknownSymbolDiag(sub, known))
		}
	}
	return valid
}

// binanceUnknownSymbolDiag builds a structured unknown-symbol diagnostic for a
// subscription that is not listed on Binance, with close external candidates.
func binanceUnknownSymbolDiag(
	sub binanceSubscription, known map[string]struct{},
) fwmarketdata.Diagnostic {
	remediation := "Remove this instrument and add one with a valid Binance symbol" +
		" (e.g. ETHUSDT). See the valid symbols list."
	suggestions := symbolPrefixSuggestionsFromSet(known, sub.External)
	if len(suggestions) > 0 {
		remediation += " Did you mean: " + strings.Join(suggestions, ", ") + "?"
	}
	return fwmarketdata.Diagnostic{
		Level:       DiagError,
		Code:        CodeUnknownSymbol,
		Kind:        DiagKindConfig,
		Instrument:  sub.External,
		Title:       fmt.Sprintf("Symbol %q not found on Binance", sub.External),
		Detail:      fmt.Sprintf("The configured external symbol %q is not a Binance trading symbol.", sub.External),
		Remediation: remediation,
		Actions: []fwmarketdata.DiagnosticAction{
			{Type: ActionRemoveInstrument, Target: sub.External},
			{Type: ActionOpenSymbols},
		},
	}
}

// stream dials, reads messages, and forwards parsed quotes to out. It returns
// true when at least one quote was parsed; run uses it to reset the backoff.
func (c *binanceConnector) stream(
	ctx context.Context, subs []binanceSubscription, out chan<- fwmarketdata.QuoteUpdate,
) (bool, error) {
	conn, err := c.dial(ctx, binanceStreamURL(subs))
	if err != nil {
		return false, err
	}
	select {
	case <-ctx.Done():
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return false, ctx.Err()
	default:
	}
	c.setActiveConn(conn)
	// Connected (or recovered after a prior failure): clear any error state.
	c.reportStatus(true, "")
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		c.clearActiveConn()
	}()

	// One-shot "receiving data but cannot parse it" diagnostic. We emit it at
	// most once per stream call: after unparsableThreshold frames arrive with
	// zero successfully parsed, we fire the diagnostic and stop counting.
	const unparsableThreshold = 3
	var unparsableSeen, framesParsed int
	unparsableReported := false

	for {
		payload, err := readStreamFrame(ctx, c.readTimeout, conn.Read)
		if err != nil {
			return framesParsed > 0, err
		}

		// A persistent config problem (e.g. invalid symbol) keeps the WS open
		// but replaces ticker data with an error frame; surface it and keep
		// the connection so valid streams still deliver. Benign acks
		// ({"result":null,...}) have no "error" object and fall through.
		var frame binanceControlFrame
		if err := json.Unmarshal(payload, &frame); err == nil &&
			frame.Error != nil && frame.Error.Msg != "" {
			c.reportStatus(false, "binance: "+frame.Error.Msg)
			continue
		}

		update, ok := parseBinanceQuoteUpdate(payload, subs)
		if !ok {
			if !unparsableReported {
				unparsableSeen++
				if unparsableSeen >= unparsableThreshold && framesParsed == 0 {
					unparsableReported = true
					c.reportDiag(fwmarketdata.Diagnostic{
						Level:       DiagError,
						Code:        CodeUnparsableData,
						Kind:        DiagKindProvider,
						Title:       "Receiving data but cannot parse it",
						Detail:      "The source is sending data but Officer could not decode it (likely a format mismatch).",
						Remediation: "This is an internal issue, not your configuration - please report it.",
						Actions:     []fwmarketdata.DiagnosticAction{{Type: ActionRestart}},
					})
				}
			}
			continue
		}

		framesParsed++
		select {
		case <-ctx.Done():
			return framesParsed > 0, ctx.Err()
		case out <- update:
		}
	}
}

func (c *binanceConnector) Close() {
	c.mu.Lock()
	c.closing = true
	cancel := c.cancel
	active := c.active
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if active != nil {
		_ = active.Close(websocket.StatusNormalClosure, "")
	}
	c.wg.Wait()
}

func (c *binanceConnector) setActiveConn(conn binanceConn) {
	c.mu.Lock()
	closing := c.closing
	if !closing {
		c.active = conn
	}
	c.mu.Unlock()
	if closing {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

func (c *binanceConnector) clearActiveConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = nil
}

// binanceExchangeInfoResponse carries only the symbol field we need.
type binanceExchangeInfoResponse struct {
	Symbols []struct {
		Symbol string `json:"symbol"`
	} `json:"symbols"`
}

// fetchBinanceSymbols fetches the Binance exchange info and returns a set of
// uppercased symbols. It is the default implementation of fetchSymbols.
func fetchBinanceSymbols(ctx context.Context) (map[string]struct{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, binanceExchangeInfoURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance exchangeInfo: unexpected status %d", resp.StatusCode)
	}
	var info binanceExchangeInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("binance exchangeInfo: decode: %w", err)
	}
	set := make(map[string]struct{}, len(info.Symbols))
	for _, s := range info.Symbols {
		set[strings.ToUpper(s.Symbol)] = struct{}{}
	}
	return set, nil
}

func normalizeBinanceSubscriptions(
	subs []fwmarketdata.Subscription,
) ([]binanceSubscription, error) {
	normalized := make([]binanceSubscription, 0, len(subs))
	for _, sub := range subs {
		symbol := strings.TrimSpace(sub.External)
		if symbol == "" {
			return nil, missingExternalSymbolError("binance", sub)
		}
		symbol = strings.ToUpper(symbol)
		if symbol == "" {
			return nil, invalidExternalSymbolError("binance", sub.External)
		}
		normalized = append(normalized, binanceSubscription{
			Subscription: sub,
			symbol:       symbol,
			stream:       strings.ToLower(symbol) + "@ticker",
		})
	}
	return normalized, nil
}

// binanceStreamURL builds the combined-stream URL. The streams query value must
// contain literal "@" and "/" separators; percent-encoding (%40, %2F) causes
// Binance to treat the whole value as one unknown stream and send no data.
func binanceStreamURL(subs []binanceSubscription) string {
	streams := make([]string, 0, len(subs))
	for _, sub := range subs {
		streams = append(streams, sub.stream)
	}
	slices.Sort(streams)
	return binanceStreamBaseURL + "?streams=" + strings.Join(streams, "/")
}

// rawString unmarshals a json.RawMessage as a Go string. Returns "" on any
// error (missing key, null, wrong type); callers treat empty as zero.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// rawInt64 unmarshals a json.RawMessage as an int64. Returns 0 on any error.
func rawInt64(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0
	}
	return n
}

// rawBool unmarshals a json.RawMessage as a bool and reports whether decoding
// succeeded.
func rawBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func parseBinanceQuoteUpdate(
	payload []byte, subs []binanceSubscription,
) (fwmarketdata.QuoteUpdate, bool) {
	// Decode the envelope; Data stays as raw JSON so we can re-decode it by
	// exact key below, bypassing Go's case-insensitive struct matching.
	var message binanceCombinedMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return fwmarketdata.QuoteUpdate{}, false
	}

	// Decode the ticker data object into a map so each key is matched exactly.
	// This prevents "C" (statistics close time, a number) from being routed
	// into the "c" (last price, a string) field and other case-twin collisions.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(message.Data, &fields); err != nil {
		return fwmarketdata.QuoteUpdate{}, false
	}

	event := binanceTickerEvent{
		EventTime: rawInt64(fields["E"]),
		Symbol:    rawString(fields["s"]),
		Last:      rawString(fields["c"]),
		Bid:       rawString(fields["b"]),
		Ask:       rawString(fields["a"]),
	}
	return quoteUpdateFromBinanceEvent(event, subs)
}

func quoteUpdateFromBinanceEvent(
	event binanceTickerEvent, subs []binanceSubscription,
) (fwmarketdata.QuoteUpdate, bool) {
	if event.EventTime <= 0 {
		return fwmarketdata.QuoteUpdate{}, false
	}
	symbol := strings.ToUpper(strings.TrimSpace(event.Symbol))
	if symbol == "" {
		return fwmarketdata.QuoteUpdate{}, false
	}

	for _, sub := range subs {
		if sub.symbol != symbol {
			continue
		}
		return fwmarketdata.QuoteUpdate{
			AsOf:  time.UnixMilli(event.EventTime).UTC(),
			Base:  sub.Base,
			Quote: sub.Quote,
			Mark:  event.Last,
			Bid:   event.Bid,
			Ask:   event.Ask,
		}, true
	}
	return fwmarketdata.QuoteUpdate{}, false
}

func backoff(attempt int, minDelay, maxDelay time.Duration) time.Duration {
	if attempt <= 0 {
		return minDelay
	}
	delay := minDelay
	for range attempt - 1 {
		if delay >= maxDelay/2 {
			return maxDelay
		}
		delay *= 2
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
