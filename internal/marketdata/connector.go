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

// Package marketdata is the officer-side connector framework that feeds external
// quotes into the engine. A Connector subscribes to a source and
// emits normalized QuoteUpdate values; the Manager fans them into a Sink that
// pushes into the engine's market-data service. The framework is pure Go - only
// the Sink (implemented in the engine package) touches the binding.
package marketdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// FreshnessTTL is the officer-wide quote freshness window: a quote whose
// source AsOf is older than this is displayed as absent. The engine receives
// current Officer snapshots, so its own MarketDataFreshnessTTL starts from the
// push receipt time. The value is mirrored by backend.MarketDataFreshnessTTL
// and caps how old a one-shot REST snapshot may be and still be worth
// publishing.
const FreshnessTTL = 70 * time.Second

const (
	DiagError = "error"
	DiagWarn  = "warn"
	DiagInfo  = "info"

	// Diagnostic kinds - who resolves the problem.
	DiagKindConfig      = "config"      // operator configuration
	DiagKindEnvironment = "environment" // network/region/reachability
	DiagKindProvider    = "provider"    // provider-side or internal

	// Diagnostic codes.
	CodeUnknownSymbol         = "unknown_symbol"
	CodeNoEnabledInstruments  = "no_enabled_instruments"
	CodeUnsupportedProvider   = "unsupported_provider"
	CodeInvalidProviderConfig = "invalid_provider_config"
	CodeConnectionError       = "connection_error"
	CodeNoData                = "no_data"
	CodeDataFreshness         = "data_freshness"
	CodeInternalError         = "internal_error"
	CodeSelfDiagnosisFailed   = "self_diagnosis_failed"
	CodeUnparsableData        = "unparsable_data"

	// Action types - the UI maps these to buttons.
	ActionRestart          = "restart"
	ActionOpenDocs         = "open_docs"
	ActionOpenSymbols      = "open_symbols"
	ActionRemoveInstrument = "remove_instrument"
)

// DiagnosticAction is a machine-readable remediation step the UI turns into a
// button. Target is the affected external symbol for instrument actions.
type DiagnosticAction struct {
	Type   string
	Target string
}

// Diagnostic is one structured, resolution-oriented problem for an instance.
// Code and Kind are machine-readable; Title and Detail are human-readable.
// Instrument is the affected external symbol (empty when instance-wide).
// At is the most-recent occurrence time.
type Diagnostic struct {
	At          time.Time
	Actions     []DiagnosticAction
	Level       string
	Code        string
	Kind        string
	Title       string
	Detail      string
	Remediation string
	Instrument  string
	// Routine marks a routine/informational provider notification that should be
	// logged at DEBUG instead of INFO. It is still recorded and shown like any
	// other diagnostic; only the log level differs.
	Routine bool
}

// QuoteUpdate is one quote normalized to an instrument, ready for the sink.
// Price fields are exact decimal strings (empty means absent), matching
// param.NewPriceFromString; only the fields the engine consumes are carried
// (mark/bid/ask + timestamp). AsOf is the source observation time.
type QuoteUpdate struct {
	// AsOf is the source observation time of the quote.
	AsOf time.Time
	// Base is the instrument underlying asset (e.g. "AAPL").
	Base string
	// Quote is the instrument settlement asset (e.g. "USD").
	Quote string
	// Mark is the mark price as an exact decimal string; empty when absent.
	Mark string
	// Bid is the best-bid price as an exact decimal string; empty when absent.
	Bid string
	// Ask is the best-ask price as an exact decimal string; empty when absent.
	Ask string
}

// Subscription is one enabled instrument of an instance: the external source
// symbol and the instrument (base, quote) it maps to. The manager builds these
// from the stored instrument rows; connectors tag emitted QuoteUpdates with
// Base/Quote.
type Subscription struct {
	// External is the source-side symbol (e.g. "AAPL").
	External string
	// Base is the instrument underlying asset the symbol maps to.
	Base string
	// Quote is the instrument settlement asset the symbol maps to.
	Quote string
}

// Connector streams quotes from one external source. It is stream-first:
// Subscribe returns a channel that delivers QuoteUpdates until the context is
// cancelled or Close is called. Configuration from credentials happens at
// construction, so Subscribe only needs the instrument set.
type Connector interface {
	// Subscribe starts streaming quotes for subs and returns the channel they
	// arrive on. The channel is closed when the connector stops (ctx cancelled
	// or Close called). An error is returned if the subscription cannot start.
	Subscribe(ctx context.Context, subs []Subscription) (<-chan QuoteUpdate, error)

	// Close releases the connector's resources and stops any background work.
	// It is safe to call once; further reads from the channel drain and end.
	Close()
}

// ProviderReferences are operator help links a connector can expose: provider
// technical/API docs and the authoritative list of valid symbols. Either field
// may be empty.
type ProviderReferences struct {
	DocsURL    string
	SymbolsURL string
}

// Referenceable is the optional capability a connector implements to expose
// help links. ok=false means the provider offers none (the UI prints nothing).
type Referenceable interface {
	References() (refs ProviderReferences, ok bool)
}

// SymbolVerification is the outcome of checking one external symbol against the
// provider's catalogue. Exists is true when the symbol is present as typed.
// Details carries optional provider metadata for the matched symbol. When Exists
// is false, Suggestion optionally carries a case-folded catalogue variant the
// operator likely meant (e.g. "ETHUSDT" for "ethusdt"); it is empty when no
// variant matches.
type SymbolVerification struct {
	Exists     bool
	Suggestion string
	Details    string
}

// SymbolVerifier is the optional capability a connector implements to check
// whether an external symbol exists in the provider's catalogue. A connector
// that cannot answer this (no symbol catalogue) does not implement it, so the
// capability is absent. VerifySymbol returns an error only on a catalogue-fetch
// or transport failure, never to signal that the symbol is unknown.
type SymbolVerifier interface {
	VerifySymbol(ctx context.Context, external string) (SymbolVerification, error)
}

// SymbolMatch is one contract the provider resolved for a symbol search. Name is
// the human-readable long name when the provider supplies one (empty otherwise).
// The remaining fields carry the resolved contract specifics; they are empty
// when the provider reports none.
type SymbolMatch struct {
	Symbol                       string
	Name                         string
	SecType                      string
	Exchange                     string
	PrimaryExchange              string
	Currency                     string
	LastTradeDateOrContractMonth string
	Right                        string
	Multiplier                   string
	LocalSymbol                  string
	TradingClass                 string
	ConID                        string
	Strike                       string
}

// SymbolSearchQuery is one symbol-resolve request: the typed Query (the symbol)
// plus the contract criteria that scope the resolve. Empty optional fields are
// left unset on the request contract.
type SymbolSearchQuery struct {
	Query                        string
	SecType                      string
	Exchange                     string
	Currency                     string
	LastTradeDateOrContractMonth string
	Right                        string
	Strike                       string
}

// SymbolSearcher is the optional capability a connector implements to resolve
// the provider's catalogue for contracts matching the query criteria. A
// connector that cannot answer this (no resolvable catalogue) does not implement
// it, so the capability is absent. Caller-supplied criteria are validated at the
// API boundary, so SearchSymbols returns an error only on a connect/transport/
// provider failure; "no matches" is an empty slice, not an error.
type SymbolSearcher interface {
	SearchSymbols(ctx context.Context, query SymbolSearchQuery) ([]SymbolMatch, error)
}

const symbolSearchLimit = 50

func verifySymbolFromSet(
	known map[string]struct{}, external string,
) SymbolVerification {
	symbol := strings.TrimSpace(external)
	if symbol == "" {
		return SymbolVerification{}
	}
	if _, ok := known[symbol]; ok {
		return SymbolVerification{Exists: true}
	}
	folded := strings.ToUpper(symbol)
	if folded != symbol {
		if _, ok := known[folded]; ok {
			return SymbolVerification{Suggestion: folded}
		}
	}
	return SymbolVerification{}
}

type setSymbolMatch struct {
	symbol string
	score  int
}

func searchSymbolsFromSet(
	known map[string]struct{}, query SymbolSearchQuery, secType string,
) []SymbolMatch {
	q := strings.TrimSpace(query.Query)
	if q == "" {
		return nil
	}
	normQuery := normalizedSymbolSearchKey(q)
	if normQuery == "" {
		return nil
	}
	base, quote, hasPair := splitSymbolPair(q)
	scored := make([]setSymbolMatch, 0)
	for symbol := range known {
		score := symbolSearchScore(symbol, q, normQuery, base, quote, hasPair)
		if score == 0 {
			continue
		}
		scored = append(scored, setSymbolMatch{symbol: symbol, score: score})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].symbol < scored[j].symbol
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > symbolSearchLimit {
		scored = scored[:symbolSearchLimit]
	}
	matches := make([]SymbolMatch, 0, len(scored))
	for _, item := range scored {
		matches = append(matches, SymbolMatch{
			Symbol:  item.symbol,
			SecType: secType,
		})
	}
	return matches
}

func symbolSearchScore(
	symbol, query, normQuery, base, quote string, hasPair bool,
) int {
	trimmed := strings.TrimSpace(symbol)
	if trimmed == "" {
		return 0
	}
	if trimmed == query {
		return 1000
	}
	if strings.EqualFold(trimmed, query) {
		return 950
	}
	normSymbol := normalizedSymbolSearchKey(trimmed)
	if normSymbol == "" {
		return 0
	}
	if hasPair && normSymbol == base+quote {
		return 900
	}
	if normSymbol == normQuery {
		return 850
	}
	if strings.HasPrefix(normSymbol, normQuery) {
		return 650
	}
	if strings.Contains(normSymbol, normQuery) {
		return 400
	}
	upperQuery := strings.ToUpper(query)
	upperSymbol := strings.ToUpper(trimmed)
	if strings.Contains(upperSymbol, upperQuery) {
		return 350
	}
	return 0
}

func normalizedSymbolSearchKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		switch r {
		case '/', '\\', '_', '-', ' ', '\t', '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func splitSymbolPair(value string) (base, quote string, ok bool) {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case '/', '\\', '_', '-', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
	if len(parts) != 2 {
		return "", "", false
	}
	base = normalizedSymbolSearchKey(parts[0])
	quote = normalizedSymbolSearchKey(parts[1])
	if base == "" || quote == "" {
		return "", "", false
	}
	return base, quote, true
}

func providerUnknownSymbolDiag(
	provider, external, base, quote string,
) Diagnostic {
	return Diagnostic{
		Level:      DiagError,
		Code:       CodeUnknownSymbol,
		Kind:       DiagKindConfig,
		Instrument: base + "/" + quote,
		Title:      fmt.Sprintf("Symbol %q not found on %s", external, provider),
		Detail: fmt.Sprintf(
			"The configured external symbol %q is not a %s instrument.",
			external,
			provider,
		),
		Remediation: "Remove this instrument and add one with a valid provider symbol. See the valid symbols list.",
		Actions: []DiagnosticAction{
			{Type: ActionRemoveInstrument, Target: external},
			{Type: ActionOpenSymbols},
		},
	}
}

// DiagnosticReporter lets a connector push a structured diagnostic to the
// manager at any time (config validation, dropped symbols, runtime issues).
type DiagnosticReporter func(diag Diagnostic)

// DiagnosticReporting is the optional capability a connector implements to
// receive a DiagnosticReporter from the manager.
type DiagnosticReporting interface {
	SetDiagnosticReporter(report DiagnosticReporter)
}

type websocketReadResult struct {
	payload []byte
	err     error
}

const defaultWebsocketReadTimeout = FreshnessTTL

func websocketReadLoop(
	ctx context.Context,
	read func(context.Context) ([]byte, error),
) <-chan websocketReadResult {
	reads := make(chan websocketReadResult, 1)
	go func() {
		defer close(reads)
		for {
			payload, err := read(ctx)
			select {
			case reads <- websocketReadResult{payload: payload, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return reads
}

func nextWebsocketRead(
	ctx context.Context,
	reads <-chan websocketReadResult,
	timeout time.Duration,
	errs ...<-chan error,
) ([]byte, error) {
	var timeoutC <-chan time.Time
	var timer *time.Timer
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timeoutC = timer.C
		defer timer.Stop()
	}

	var errC <-chan error
	if len(errs) > 0 {
		errC = errs[0]
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timeoutC:
		return nil, fmt.Errorf("websocket read timeout after %s", timeout)
	case err := <-errC:
		if err == nil {
			return nil, io.EOF
		}
		return nil, err
	case result, ok := <-reads:
		if !ok {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		return result.payload, result.err
	}
}

func rawDecimalString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		return rawString(raw)
	}
	var n json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&n); err != nil {
		return ""
	}
	return n.String()
}

type unparsableTracker struct {
	framesReceived int
	framesParsed   int
	reported       bool
}

const unparsableThreshold = 3

func (t *unparsableTracker) recordParsed() {
	t.framesParsed++
}

func (t *unparsableTracker) recordUnparsed(report func(Diagnostic)) {
	if t.reported {
		return
	}
	t.framesReceived++
	if t.framesReceived < unparsableThreshold || t.framesParsed != 0 {
		return
	}
	t.reported = true
	report(Diagnostic{
		Level:       DiagError,
		Code:        CodeUnparsableData,
		Kind:        DiagKindProvider,
		Title:       "Receiving data but cannot parse it",
		Detail:      "The source is sending data but Officer could not decode it (likely a format mismatch).",
		Remediation: "This is an internal issue, not your configuration - please report it.",
		Actions:     []DiagnosticAction{{Type: ActionRestart}},
	})
}

// Diagnosable is the optional capability a connector implements to perform
// active self-diagnosis against the provider. The manager calls it once when a
// live feed has been silent for diagnoseGrace. Returning an error means the
// diagnosis itself failed (not that any symbol is bad); returning an empty
// slice means the provider sees the symbols as valid.
type Diagnosable interface {
	Diagnose(ctx context.Context) ([]Diagnostic, error)
}

// SilenceTolerant is the optional capability a connector implements to declare
// that a connected-but-silent feed with valid symbols is an expected state
// (e.g. equities outside market hours). When self-diagnosis finds nothing
// wrong, the silent-feed watchdog suppresses the generic "No market data"
// warning for such a provider; the operator confirms liveness on demand via the
// per-symbol verify button instead.
type SilenceTolerant interface {
	ToleratesSilence() bool
}

// StatusReporter receives a connector's runtime connection state. ok=true means
// connected/receiving; ok=false carries a short failure message. It is called
// from the connector's background goroutine, never synchronously from Subscribe.
type StatusReporter func(ok bool, errMsg string)

// StatusReporting is the optional capability a connector implements to report
// its runtime connection state back to the manager. Connectors that cannot fail
// at connection time (push-based BYO, in-memory mock) need not implement it.
type StatusReporting interface {
	SetStatusReporter(report StatusReporter)
}

// Pushable is the optional capability a connector implements to accept a quote
// pushed by the operator rather than pulled from a source. The bring-your-own
// connector implements it; streaming providers do not. The manager uses it to
// deliver an operator-set manual mark price once - at startup and on upsert -
// onto the connector's subscription channel, from where it drains into the sink
// like any other quote.
type Pushable interface {
	Push(update QuoteUpdate)
}

// Sink receives normalized quotes and forwards them to the engine. It is
// implemented in the engine package over the binding's market-data service.
type Sink interface {
	// Push forwards one normalized quote into the engine. It returns an error if
	// the quote cannot be registered or pushed.
	Push(update QuoteUpdate) error
}
