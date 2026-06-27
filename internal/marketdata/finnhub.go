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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/domain"
)

const (
	finnhubWSBaseURL       = "wss://ws.finnhub.io"
	finnhubQuoteURL        = "https://finnhub.io/api/v1/quote"
	finnhubSearchURL       = "https://finnhub.io/api/v1/search"
	finnhubCryptoSymbolURL = "https://finnhub.io/api/v1/crypto/symbol"
	finnhubSymbolLimit     = 50
	finnhubHTTPTimeout     = 10 * time.Second

	// finnhubCryptoCacheTTL bounds how long a fetched per-exchange crypto
	// catalogue is reused. The catalogue is large (thousands of pairs) and
	// effectively static, and a fresh connector is built per resolve, so caching
	// keeps symbol search from refetching it on every keystroke.
	finnhubCryptoCacheTTL = 12 * time.Hour
	// finnhubMaxCryptoMatches caps how many crypto candidates one search returns
	// so a broad query ("ETH") cannot flood the resolver list.
	finnhubMaxCryptoMatches = 25

	defaultFinnhubReconnectMin = 250 * time.Millisecond
	defaultFinnhubReconnectMax = 5 * time.Second
	// finnhubSnapshotMaxAge bounds how old a REST /quote opening snapshot may be
	// and still be published as the first mark. It mirrors the engine freshness
	// window (FreshnessTTL): a snapshot within it is a quote the engine would
	// accept as current, so dropping it only starves the instrument of an opening
	// price; an older one already reads as absent and is not worth publishing.
	finnhubSnapshotMaxAge = FreshnessTTL
)

type finnhubConnector struct {
	dial          func(context.Context, string) (finnhubConn, error)
	sleep         func(context.Context, time.Duration) error
	quote         func(context.Context, string, string) (finnhubQuote, error)
	search        func(context.Context, string, string) ([]finnhubSearchResult, error)
	cryptoSymbols func(context.Context, string, string) ([]finnhubCryptoSymbol, error)
	now           func() time.Time
	report        StatusReporter
	diagReport    DiagnosticReporter
	reconnectMin  time.Duration
	reconnectMax  time.Duration
	readTimeout   time.Duration
	token         string

	subsMu sync.Mutex
	subs   []finnhubSubscription
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type finnhubConn interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close(websocket.StatusCode, string) error
}

type liveFinnhubConn struct {
	conn *websocket.Conn
}

type finnhubSubscription struct {
	Subscription
	symbol string
}

type finnhubSubscribeFrame struct {
	Type   string `json:"type"`
	Symbol string `json:"symbol"`
}

type finnhubMessage struct {
	Type    string         `json:"type"`
	Message string         `json:"msg"`
	Data    []finnhubTrade `json:"data"`
}

type finnhubTrade struct {
	Symbol string          `json:"s"`
	Price  json.RawMessage `json:"p"`
	Time   int64           `json:"t"`
}

// NewFinnhubConnector builds a Finnhub streaming connector from instance
// credentials.
func NewFinnhubConnector(
	instance domain.MarketDataInstance,
) (*finnhubConnector, error) {
	token, err := parseFinnhubToken(instance.Credentials)
	if err != nil {
		return nil, err
	}
	return &finnhubConnector{
		dial:          dialFinnhub,
		sleep:         sleepContext,
		quote:         fetchFinnhubQuote,
		search:        fetchFinnhubSearchResults,
		cryptoSymbols: fetchFinnhubCryptoSymbols,
		now:           time.Now,
		reconnectMin:  defaultFinnhubReconnectMin,
		reconnectMax:  defaultFinnhubReconnectMax,
		readTimeout:   defaultWebsocketReadTimeout,
		token:         token,
	}, nil
}

func parseFinnhubToken(raw string) (string, error) {
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("finnhub credentials: %w", err)
	}
	token := strings.TrimSpace(payload.Token)
	if token == "" {
		return "", errors.New("finnhub credentials: token is required")
	}
	return token, nil
}

func dialFinnhub(ctx context.Context, streamURL string) (finnhubConn, error) {
	conn, _, err := websocket.Dial(ctx, streamURL, nil)
	if err != nil {
		return nil, err
	}
	return liveFinnhubConn{conn: conn}, nil
}

func (c liveFinnhubConn) Read(ctx context.Context) ([]byte, error) {
	_, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func (c liveFinnhubConn) Write(ctx context.Context, payload []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func (c liveFinnhubConn) Close(code websocket.StatusCode, reason string) error {
	return c.conn.Close(code, reason)
}

// SetStatusReporter installs the manager's runtime reporter.
func (c *finnhubConnector) SetStatusReporter(report StatusReporter) {
	c.report = report
}

func (c *finnhubConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

// SetDiagnosticReporter installs the manager's structured diagnostic reporter.
func (c *finnhubConnector) SetDiagnosticReporter(report DiagnosticReporter) {
	c.diagReport = report
}

func (c *finnhubConnector) reportDiag(diag Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

// References returns the Finnhub WebSocket docs and general API docs URLs.
func (c *finnhubConnector) References() (ProviderReferences, bool) {
	return ProviderReferences{
		DocsURL:    "https://finnhub.io/docs/api/websocket-trades",
		SymbolsURL: "https://finnhub.io/docs/api",
	}, true
}

// VerifySymbol checks external against Finnhub's symbol-search endpoint. The
// endpoint returns both displaySymbol and symbol; the WebSocket subscription
// uses symbol, so verification must compare that field.
func (c *finnhubConnector) VerifySymbol(
	ctx context.Context, external string,
) (SymbolVerification, error) {
	matches, err := c.SearchSymbols(ctx, SymbolSearchQuery{Query: external})
	if err != nil {
		return SymbolVerification{}, err
	}
	symbol := strings.TrimSpace(external)
	if symbol == "" {
		return SymbolVerification{}, nil
	}
	if match, ok := finnhubExactSymbolMatch(matches, symbol); ok {
		details := finnhubSymbolDetails(match)
		quoteCtx, cancel := context.WithTimeout(ctx, finnhubHTTPTimeout)
		quote, err := c.quote(quoteCtx, match.Symbol, c.token)
		cancel()
		if err != nil {
			// The symbol is in the catalogue but its quote could not be fetched
			// (e.g. the API plan lacks entitlement for that venue and Finnhub
			// answers 403). That is a property of the symbol, not a failure of
			// the check, so report it as recognized-but-unquotable rather than
			// erroring out and leaving the operator with no verdict.
			return SymbolVerification{
				Details: finnhubSymbolUnavailableDetails(details),
			}, nil
		}
		if !finnhubQuoteUsableForVerify(quote, c.nowUTC()) {
			return SymbolVerification{
				Details: finnhubSymbolUnavailableDetails(details),
			}, nil
		}
		return SymbolVerification{
			Exists:  true,
			Details: details,
		}, nil
	}
	folded := strings.ToUpper(symbol)
	if folded != symbol {
		for _, match := range matches {
			if match.Symbol == folded {
				return SymbolVerification{Suggestion: folded}, nil
			}
		}
	}
	return SymbolVerification{}, nil
}

func finnhubExactSymbolMatch(
	matches []SymbolMatch, symbol string,
) (SymbolMatch, bool) {
	for _, match := range matches {
		if match.Symbol == symbol {
			return match, true
		}
	}
	return SymbolMatch{}, false
}

func finnhubSymbolDetails(match SymbolMatch) string {
	parts := make([]string, 0, 3)
	if match.Name != "" {
		parts = append(parts, match.Name)
	}
	if match.SecType != "" {
		parts = append(parts, match.SecType)
	}
	if match.Exchange != "" {
		parts = append(parts, match.Exchange)
	}
	return strings.Join(parts, ", ")
}

// finnhubExchangeFromSymbol extracts the venue from an exchange-qualified Finnhub
// symbol. Crypto and FX symbols are "EXCHANGE:PAIR" (e.g. "BINANCE:ETHUSDT"); the
// part before the first colon is the venue the price is sourced from. Bare equity
// tickers ("AAPL") carry no prefix and report no venue.
func finnhubExchangeFromSymbol(symbol string) string {
	if i := strings.IndexByte(symbol, ':'); i > 0 {
		return strings.TrimSpace(symbol[:i])
	}
	return ""
}

// finnhubCryptoSearchExchanges are the crypto venues SearchSymbols scans. They
// cover the most liquid USDT and USD pairs the operator is likely to want; the
// list is intentionally short to bound how many catalogues a search fetches.
var finnhubCryptoSearchExchanges = []string{"BINANCE", "COINBASE"}

// symbolAfterColon returns the pair part of an exchange-qualified symbol
// ("BINANCE:ETHUSDT" -> "ETHUSDT"), or the input unchanged when not qualified.
func symbolAfterColon(symbol string) string {
	if i := strings.LastIndexByte(symbol, ':'); i >= 0 {
		return symbol[i+1:]
	}
	return symbol
}

// finnhubCompactPair strips pair separators so typed forms like "ETH/USDT" and
// "ETH-USD" compare against a catalogue's compact symbol ("ETHUSDT").
func finnhubCompactPair(s string) string {
	return strings.NewReplacer("/", "", "-", "", "_", "", " ", "").Replace(s)
}

// finnhubMajorQuoteAssets are the settlement assets an operator most likely
// wants, so a base-asset hit quoted in one of them ranks above the same base
// quoted in an exotic currency and survives the per-search cap.
var finnhubMajorQuoteAssets = map[string]struct{}{
	"USDT": {}, "USD": {}, "USDC": {}, "FDUSD": {}, "BUSD": {}, "EUR": {},
}

// finnhubCryptoMatchRank reports whether a crypto catalogue entry matches query
// and how closely (lower is better), so an exact base-asset hit in a major quote
// (ETH/USDT) ranks above the same base in an exotic quote (ETH/MXN), which in
// turn ranks above incidental substring hits. ok is false when it does not match.
func finnhubCryptoMatchRank(sym finnhubCryptoSymbol, query string) (int, bool) {
	q := strings.ToUpper(strings.TrimSpace(query))
	if q == "" {
		return 0, false
	}
	symbol := strings.ToUpper(strings.TrimSpace(sym.Symbol)) // "BINANCE:ETHUSDT"
	if q == symbol {
		return 0, true
	}
	display := strings.ToUpper(sym.DisplaySymbol) // "ETH/USDT"
	base, quote := display, ""
	if i := strings.IndexByte(display, '/'); i >= 0 {
		base, quote = display[:i], display[i+1:] // "ETH", "USDT"
	}
	pair := finnhubCompactPair(symbolAfterColon(symbol)) // "ETHUSDT"
	qCompact := finnhubCompactPair(q)
	_, majorQuote := finnhubMajorQuoteAssets[quote]
	switch {
	case base == q && majorQuote:
		return 0, true
	case base == q:
		return 1, true
	case strings.HasPrefix(base, q):
		return 2, true
	case qCompact != "" && pair == qCompact:
		return 2, true
	case qCompact != "" && strings.Contains(pair, qCompact):
		return 3, true
	default:
		return 0, false
	}
}

func finnhubSymbolUnavailableDetails(details string) string {
	if details == "" {
		return "symbol exists, but Finnhub has no fresh quote for it"
	}
	return details + "; no fresh Finnhub quote"
}

func finnhubQuoteUsableForVerify(quote finnhubQuote, now time.Time) bool {
	update, ok := quoteUpdateFromFinnhubQuote(quote, finnhubSubscription{})
	if !ok {
		return false
	}
	return !finnhubSnapshotStale(update.AsOf, now)
}

// SearchSymbols searches Finnhub's symbol catalogue and maps the provider
// response onto the generic symbol-match shape used by the operator panel.
func (c *finnhubConnector) SearchSymbols(
	ctx context.Context, query SymbolSearchQuery,
) ([]SymbolMatch, error) {
	q := strings.TrimSpace(query.Query)
	if q == "" {
		return nil, nil
	}
	searchCtx, cancel := context.WithTimeout(ctx, finnhubHTTPTimeout)
	defer cancel()

	results, err := c.search(searchCtx, q, c.token)
	if err != nil {
		return nil, err
	}
	equities := make([]SymbolMatch, 0, len(results))
	for _, result := range results {
		symbol := strings.TrimSpace(result.Symbol)
		if symbol == "" {
			continue
		}
		equities = append(equities, SymbolMatch{
			Symbol:   symbol,
			Name:     strings.TrimSpace(result.Description),
			SecType:  strings.TrimSpace(result.Type),
			Exchange: finnhubExchangeFromSymbol(symbol),
		})
	}
	// Finnhub's /search endpoint does not index crypto or FX pairs, so a bare
	// "ETH" only ever resolves to equities. Scan the crypto catalogue too and
	// surface those venue-qualified pairs first, where the operator is most
	// likely looking when they type a crypto base.
	return append(c.searchFinnhubCrypto(ctx, q), equities...), nil
}

// searchFinnhubCrypto scans the configured crypto venues' catalogues for pairs
// matching query and maps them to venue-qualified SymbolMatches. A venue whose
// catalogue cannot be fetched is skipped, never failing the whole search. The
// result is ranked (exact base-asset hits first) and capped.
func (c *finnhubConnector) searchFinnhubCrypto(
	ctx context.Context, query string,
) []SymbolMatch {
	if c.cryptoSymbols == nil {
		return []SymbolMatch{}
	}
	type ranked struct {
		match SymbolMatch
		rank  int
	}
	var found []ranked
	for _, exchange := range finnhubCryptoSearchExchanges {
		fetchCtx, cancel := context.WithTimeout(ctx, finnhubHTTPTimeout)
		symbols, err := c.cryptoSymbols(fetchCtx, exchange, c.token)
		cancel()
		if err != nil {
			continue
		}
		for _, sym := range symbols {
			rank, ok := finnhubCryptoMatchRank(sym, query)
			if !ok {
				continue
			}
			symbol := strings.TrimSpace(sym.Symbol)
			if symbol == "" {
				continue
			}
			found = append(found, ranked{
				match: SymbolMatch{
					Symbol:   symbol,
					Name:     strings.TrimSpace(sym.Description),
					SecType:  "Crypto",
					Exchange: finnhubExchangeFromSymbol(symbol),
				},
				rank: rank,
			})
		}
	}
	slices.SortStableFunc(found, func(a, b ranked) int { return a.rank - b.rank })
	if len(found) > finnhubMaxCryptoMatches {
		found = found[:finnhubMaxCryptoMatches]
	}
	out := make([]SymbolMatch, len(found))
	for i, r := range found {
		out[i] = r.match
	}
	return out
}

// ToleratesSilence reports that a quiet Finnhub feed is expected: trade ticks
// only arrive while a market is open and the symbol trades, so a connected feed
// with valid symbols may legitimately deliver nothing. The operator confirms a
// symbol on demand with verify instead of a recurring no-data warning.
func (c *finnhubConnector) ToleratesSilence() bool { return true }

// Diagnose flags only configured symbols Finnhub does not recognize. A known
// symbol that is merely silent (closed market, illiquid pair) is expected and
// is left to the on-demand verify button, so it produces no recurring warning.
func (c *finnhubConnector) Diagnose(ctx context.Context) ([]Diagnostic, error) {
	c.subsMu.Lock()
	subs := append([]finnhubSubscription(nil), c.subs...)
	c.subsMu.Unlock()

	var findings []Diagnostic
	for _, sub := range subs {
		matches, err := c.SearchSymbols(ctx, SymbolSearchQuery{Query: sub.symbol})
		if err != nil {
			return nil, err
		}
		// A recognized-but-silent symbol is normal (closed market, illiquid pair)
		// and is checked on demand via the per-symbol verify button, so it raises
		// no diagnostic here. Only a symbol Finnhub does not know is actionable.
		if _, ok := finnhubExactSymbolMatch(matches, sub.symbol); !ok {
			findings = append(findings, providerUnknownSymbolDiag(
				"Finnhub",
				sub.External,
				sub.Base,
				sub.Quote,
			))
		}
	}
	return findings, nil
}

func (c *finnhubConnector) Subscribe(
	ctx context.Context, subs []Subscription,
) (<-chan QuoteUpdate, error) {
	normalized, err := normalizeFinnhubSubscriptions(subs)
	if err != nil {
		return nil, err
	}
	c.subsMu.Lock()
	c.subs = normalized
	c.subsMu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	out := make(chan QuoteUpdate)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(out)
		c.emitSnapshots(runCtx, normalized, out)
		c.run(runCtx, normalized, out)
	}()
	return out, nil
}

func (c *finnhubConnector) run(
	ctx context.Context, subs []finnhubSubscription, out chan<- QuoteUpdate,
) {
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

func (c *finnhubConnector) stream(
	ctx context.Context, subs []finnhubSubscription, out chan<- QuoteUpdate,
) (bool, error) {
	conn, err := c.dial(ctx, finnhubStreamURL(c.token))
	if err != nil {
		return false, redactFinnhubStreamError(err, c.token)
	}
	c.reportStatus(true, "")
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	if err := writeFinnhubSubscribe(ctx, conn, subs); err != nil {
		return false, err
	}

	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	reads := websocketReadLoop(readCtx, conn.Read)
	var unparsable unparsableTracker
	delivered := false

	for {
		payload, err := nextWebsocketRead(ctx, reads, c.readTimeout)
		if err != nil {
			return delivered, err
		}
		frame := parseFinnhubFrame(payload, subs)
		if frame.ignored {
			continue
		}
		if frame.statusError != "" {
			c.reportStatus(false, frame.statusError)
			continue
		}
		if frame.unparsedReason != "" {
			unparsable.recordUnparsed(c.reportDiag)
			continue
		}
		updates := frame.updates
		unparsable.recordParsed()
		delivered = true
		for _, update := range updates {
			select {
			case <-ctx.Done():
				return delivered, ctx.Err()
			case out <- update:
			}
		}
	}
}

func (c *finnhubConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}

func (c *finnhubConnector) emitSnapshots(
	ctx context.Context, subs []finnhubSubscription, out chan<- QuoteUpdate,
) {
	if c.quote == nil {
		return
	}
	for _, sub := range subs {
		quoteCtx, cancel := context.WithTimeout(ctx, finnhubHTTPTimeout)
		quote, err := c.quote(quoteCtx, sub.symbol, c.token)
		cancel()
		if err != nil {
			continue
		}
		update, ok := quoteUpdateFromFinnhubQuote(quote, sub)
		if !ok {
			continue
		}
		// A stale REST snapshot already reads as absent to the engine, so it is
		// dropped silently; the per-symbol verify button reports staleness on
		// demand rather than this path emitting a recurring warning.
		if finnhubSnapshotStale(update.AsOf, c.nowUTC()) {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case out <- update:
		}
	}
}

func (c *finnhubConnector) nowUTC() time.Time {
	if c.now == nil {
		return time.Now().UTC()
	}
	now := c.now()
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func normalizeFinnhubSubscriptions(
	subs []Subscription,
) ([]finnhubSubscription, error) {
	if len(subs) > finnhubSymbolLimit {
		return nil, fmt.Errorf("finnhub subscription limit exceeded: %d > %d", len(subs), finnhubSymbolLimit)
	}
	normalized := make([]finnhubSubscription, 0, len(subs))
	for _, sub := range subs {
		symbol := strings.TrimSpace(sub.External)
		if symbol == "" {
			symbol = strings.TrimSpace(sub.Base)
		}
		if symbol == "" {
			return nil, fmt.Errorf("finnhub subscription %s/%s: empty symbol", sub.Base, sub.Quote)
		}
		normalized = append(normalized, finnhubSubscription{
			Subscription: sub,
			symbol:       symbol,
		})
	}
	return normalized, nil
}

func finnhubStreamURL(token string) string {
	values := url.Values{}
	values.Set("token", token)
	return finnhubWSBaseURL + "?" + values.Encode()
}

func redactFinnhubStreamError(err error, token string) error {
	if err == nil {
		return nil
	}

	if urlErr, ok := err.(*url.Error); ok {
		redacted := *urlErr
		redacted.URL = redactFinnhubStreamURL(urlErr.URL)
		redacted.Err = redactFinnhubStreamError(urlErr.Err, token)
		return &redacted
	}

	token = strings.TrimSpace(token)
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "REDACTED"))
}

func redactFinnhubStreamURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	values := parsed.Query()
	if _, ok := values["token"]; !ok {
		return rawURL
	}
	values.Set("token", "REDACTED")
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

type finnhubSearchResponse struct {
	Count  int                   `json:"count"`
	Result []finnhubSearchResult `json:"result"`
}

type finnhubQuote struct {
	Current json.RawMessage `json:"c"`
	Time    int64           `json:"t"`
}

type finnhubSearchResult struct {
	Description   string `json:"description"`
	DisplaySymbol string `json:"displaySymbol"`
	Symbol        string `json:"symbol"`
	Type          string `json:"type"`
}

// finnhubCryptoSymbol is one entry of a venue's crypto catalogue
// (/crypto/symbol): Symbol is the streamable id ("BINANCE:ETHUSDT"),
// DisplaySymbol the human pair ("ETH/USDT").
type finnhubCryptoSymbol struct {
	Description   string `json:"description"`
	DisplaySymbol string `json:"displaySymbol"`
	Symbol        string `json:"symbol"`
}

type finnhubCryptoCacheEntry struct {
	symbols   []finnhubCryptoSymbol
	fetchedAt time.Time
}

var (
	finnhubCryptoCacheMu sync.Mutex
	finnhubHTTPClient    = http.DefaultClient
	// finnhubCryptoCache holds each venue's crypto catalogue keyed by exchange.
	// The catalogue is the same for every token, so it is shared process-wide and
	// refreshed per finnhubCryptoCacheTTL.
	finnhubCryptoCache = map[string]finnhubCryptoCacheEntry{}
)

// fetchFinnhubCryptoSymbols returns one venue's crypto catalogue, served from a
// process-wide per-exchange cache when fresh. Errors are not cached.
func fetchFinnhubCryptoSymbols(
	ctx context.Context, exchange, token string,
) ([]finnhubCryptoSymbol, error) {
	key := strings.ToUpper(strings.TrimSpace(exchange))
	finnhubCryptoCacheMu.Lock()
	entry, ok := finnhubCryptoCache[key]
	finnhubCryptoCacheMu.Unlock()
	if ok && time.Since(entry.fetchedAt) < finnhubCryptoCacheTTL {
		return entry.symbols, nil
	}

	values := url.Values{}
	values.Set("exchange", key)
	values.Set("token", token)
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		finnhubCryptoSymbolURL+"?"+values.Encode(),
		nil,
	)
	if err != nil {
		return nil, err
	}
	resp, err := finnhubHTTPClient.Do(req)
	if err != nil {
		return nil, redactFinnhubStreamError(err, token)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"finnhub crypto symbols: unexpected status %d",
			resp.StatusCode,
		)
	}
	var out []finnhubCryptoSymbol
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("finnhub crypto symbols: decode: %w", err)
	}

	finnhubCryptoCacheMu.Lock()
	finnhubCryptoCache[key] = finnhubCryptoCacheEntry{
		symbols:   out,
		fetchedAt: time.Now(),
	}
	finnhubCryptoCacheMu.Unlock()
	return out, nil
}

func fetchFinnhubQuote(
	ctx context.Context, symbol, token string,
) (finnhubQuote, error) {
	values := url.Values{}
	values.Set("symbol", symbol)
	values.Set("token", token)
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		finnhubQuoteURL+"?"+values.Encode(),
		nil,
	)
	if err != nil {
		return finnhubQuote{}, err
	}
	resp, err := finnhubHTTPClient.Do(req)
	if err != nil {
		return finnhubQuote{}, redactFinnhubStreamError(err, token)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return finnhubQuote{}, fmt.Errorf(
			"finnhub quote: unexpected status %d",
			resp.StatusCode,
		)
	}
	var out finnhubQuote
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return finnhubQuote{}, fmt.Errorf("finnhub quote: decode: %w", err)
	}
	return out, nil
}

func fetchFinnhubSearchResults(
	ctx context.Context, query, token string,
) ([]finnhubSearchResult, error) {
	values := url.Values{}
	values.Set("q", query)
	values.Set("token", token)
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		finnhubSearchURL+"?"+values.Encode(),
		nil,
	)
	if err != nil {
		return nil, err
	}
	resp, err := finnhubHTTPClient.Do(req)
	if err != nil {
		return nil, redactFinnhubStreamError(err, token)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"finnhub symbol search: unexpected status %d",
			resp.StatusCode,
		)
	}
	var out finnhubSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("finnhub symbol search: decode: %w", err)
	}
	return out.Result, nil
}

func quoteUpdateFromFinnhubQuote(
	quote finnhubQuote, sub finnhubSubscription,
) (QuoteUpdate, bool) {
	if quote.Time <= 0 {
		return QuoteUpdate{}, false
	}
	mark := rawDecimalString(quote.Current)
	if mark == "" || mark == "0" {
		return QuoteUpdate{}, false
	}
	return QuoteUpdate{
		AsOf:  time.Unix(quote.Time, 0).UTC(),
		Base:  sub.Base,
		Quote: sub.Quote,
		Mark:  mark,
	}, true
}

func finnhubSnapshotStale(asOf, now time.Time) bool {
	if asOf.IsZero() {
		return true
	}
	return now.Sub(asOf.UTC()) > finnhubSnapshotMaxAge
}

func writeFinnhubSubscribe(
	ctx context.Context, conn finnhubConn, subs []finnhubSubscription,
) error {
	for _, sub := range subs {
		payload, err := json.Marshal(finnhubSubscribeFrame{
			Type:   "subscribe",
			Symbol: sub.symbol,
		})
		if err != nil {
			return err
		}
		if err := conn.Write(ctx, payload); err != nil {
			return err
		}
	}
	return nil
}

func parseFinnhubQuoteUpdates(
	payload []byte, subs []finnhubSubscription,
) []QuoteUpdate {
	return parseFinnhubFrame(payload, subs).updates
}

type finnhubFrameResult struct {
	updates        []QuoteUpdate
	statusError    string
	unparsedReason string
	ignored        bool
}

func parseFinnhubFrame(
	payload []byte, subs []finnhubSubscription,
) finnhubFrameResult {
	var message finnhubMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return finnhubFrameResult{unparsedReason: "json decode: " + err.Error()}
	}
	switch strings.TrimSpace(message.Type) {
	case "trade":
	case "ping":
		return finnhubFrameResult{ignored: true}
	case "error":
		msg := strings.TrimSpace(message.Message)
		if msg == "" {
			msg = "provider error frame"
		}
		return finnhubFrameResult{statusError: "finnhub: " + msg}
	case "":
		return finnhubFrameResult{unparsedReason: "missing frame type"}
	default:
		return finnhubFrameResult{
			unparsedReason: "unexpected frame type " + message.Type,
		}
	}
	if len(message.Data) == 0 {
		return finnhubFrameResult{ignored: true}
	}

	updates := make([]QuoteUpdate, 0, len(message.Data))
	for _, trade := range message.Data {
		if update, ok := quoteUpdateFromFinnhubTrade(trade, subs); ok {
			updates = append(updates, update)
		}
	}
	if len(updates) == 0 {
		return finnhubFrameResult{
			unparsedReason: "trade frame had no subscribed valid trade updates",
		}
	}
	return finnhubFrameResult{updates: updates}
}

func quoteUpdateFromFinnhubTrade(
	trade finnhubTrade, subs []finnhubSubscription,
) (QuoteUpdate, bool) {
	if trade.Time <= 0 {
		return QuoteUpdate{}, false
	}
	symbol := strings.TrimSpace(trade.Symbol)
	if symbol == "" {
		return QuoteUpdate{}, false
	}
	price := rawDecimalString(trade.Price)
	if price == "" {
		return QuoteUpdate{}, false
	}

	for _, sub := range subs {
		if sub.symbol != symbol {
			continue
		}
		return QuoteUpdate{
			AsOf:  time.UnixMilli(trade.Time).UTC(),
			Base:  sub.Base,
			Quote: sub.Quote,
			Mark:  price,
		}, true
	}
	return QuoteUpdate{}, false
}
