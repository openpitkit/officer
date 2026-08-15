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

	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
)

// FreshnessTTL is the officer-wide quote freshness window.
const FreshnessTTL = fwmarketdata.FreshnessTTL

// QuoteUpdate is one quote normalized to an instrument, ready for the sink.
type QuoteUpdate = fwmarketdata.QuoteUpdate

// Sink receives normalized quotes and forwards them to the engine.
type Sink = fwmarketdata.Sink

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

type DiagnosticAction = fwmarketdata.DiagnosticAction
type Diagnostic = fwmarketdata.Diagnostic
type Subscription = fwmarketdata.Subscription
type Connector = fwmarketdata.Connector
type ProviderReferences = fwmarketdata.ProviderReferences
type Referenceable = fwmarketdata.Referenceable
type SymbolVerification = fwmarketdata.SymbolVerification
type SymbolVerifier = fwmarketdata.SymbolVerifier
type SymbolMatch = fwmarketdata.SymbolMatch
type SymbolSearchQuery = fwmarketdata.SymbolSearchQuery
type SymbolSearcher = fwmarketdata.SymbolSearcher

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

type symbolSuggestionCandidate struct {
	symbol       string
	prefixLength int
}

const unknownSymbolSuggestionLimit = 3

// symbolPrefixSuggestionsFromSet returns deterministic candidates ranked by
// their normalized prefix shared with the configured external symbol.
func symbolPrefixSuggestionsFromSet(
	known map[string]struct{}, external string,
) []string {
	normalizedExternal := normalizedSymbolSearchKey(external)
	if normalizedExternal == "" {
		return nil
	}

	candidates := make([]symbolSuggestionCandidate, 0)
	for symbol := range known {
		normalizedSymbol := normalizedSymbolSearchKey(symbol)
		prefixLength := sharedSymbolPrefixLength(
			normalizedExternal,
			normalizedSymbol,
		)
		if prefixLength == 0 {
			continue
		}
		candidates = append(candidates, symbolSuggestionCandidate{
			symbol:       symbol,
			prefixLength: prefixLength,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].prefixLength == candidates[j].prefixLength {
			return candidates[i].symbol < candidates[j].symbol
		}
		return candidates[i].prefixLength > candidates[j].prefixLength
	})
	if len(candidates) > unknownSymbolSuggestionLimit {
		candidates = candidates[:unknownSymbolSuggestionLimit]
	}

	suggestions := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		suggestions = append(suggestions, candidate.symbol)
	}
	return suggestions
}

func sharedSymbolPrefixLength(left, right string) int {
	length := len(left)
	if len(right) < length {
		length = len(right)
	}
	for i := 0; i < length; i++ {
		if left[i] != right[i] {
			return i
		}
	}
	return length
}

func searchSymbolsFromSet(
	known map[string]struct{}, query SymbolSearchQuery, secType string,
) []SymbolMatch {
	q := strings.TrimSpace(query.Query)
	if q == "" {
		return []SymbolMatch{}
	}
	normQuery := normalizedSymbolSearchKey(q)
	if normQuery == "" {
		return []SymbolMatch{}
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

func providerUnknownSymbolDiag(provider, external string) Diagnostic {
	return Diagnostic{
		Level:      DiagError,
		Code:       CodeUnknownSymbol,
		Kind:       DiagKindConfig,
		Instrument: external,
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

func missingExternalSymbolError(provider string, sub Subscription) error {
	return fmt.Errorf(
		"%s subscription for asset key %d/%d: external symbol is missing",
		provider,
		sub.Base,
		sub.Quote,
	)
}

func invalidExternalSymbolError(provider, external string) error {
	return fmt.Errorf("%s subscription external symbol %q is invalid", provider, external)
}

type DiagnosticReporter = fwmarketdata.DiagnosticReporter
type DiagnosticReporting = fwmarketdata.DiagnosticReporting

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

type Diagnosable = fwmarketdata.Diagnosable
type SilenceTolerant = fwmarketdata.SilenceTolerant
type StatusReporter = fwmarketdata.StatusReporter
type StatusReporting = fwmarketdata.StatusReporting
type Pushable = fwmarketdata.Pushable
