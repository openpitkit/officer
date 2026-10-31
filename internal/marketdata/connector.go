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

	"go.openpit.dev/officer/framework/health"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/internal/symbolsearch"
)

func verifySymbolFromSet[V any](
	known map[string]V, external string,
) fwmarketdata.SymbolVerification {
	symbol := strings.TrimSpace(external)
	if symbol == "" {
		return fwmarketdata.SymbolVerification{}
	}
	if _, ok := known[symbol]; ok {
		return fwmarketdata.SymbolVerification{Exists: true}
	}
	folded := strings.ToUpper(symbol)
	if folded != symbol {
		if _, ok := known[folded]; ok {
			return fwmarketdata.SymbolVerification{Suggestion: folded}
		}
	}
	return fwmarketdata.SymbolVerification{}
}

type symbolSuggestionCandidate struct {
	symbol       string
	prefixLength int
}

const unknownSymbolSuggestionLimit = 3

// symbolPrefixSuggestionsFromSet returns deterministic candidates ranked by
// their normalized prefix shared with the configured external symbol.
func symbolPrefixSuggestionsFromSet[V any](
	known map[string]V, external string,
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

func searchSymbolsFromSet[V any](
	known map[string]V, query fwmarketdata.SymbolSearchQuery, secType string,
) []fwmarketdata.SymbolMatch {
	symbols := symbolsearch.Rank(known, query.Query)
	matches := make([]fwmarketdata.SymbolMatch, 0, len(symbols))
	for _, symbol := range symbols {
		matches = append(matches, fwmarketdata.SymbolMatch{
			Symbol:  symbol,
			SecType: secType,
		})
	}
	return matches
}

// symbolAssets is a catalogue symbol's base and quote asset codes as the venue
// states them; both are empty when the venue states no pair for the symbol.
type symbolAssets struct {
	base  string
	quote string
}

// newSymbolAssets keeps the venue's base and quote codes only when it states
// both, so a match never carries half a pair.
func newSymbolAssets(base, quote string) symbolAssets {
	base = strings.TrimSpace(base)
	quote = strings.TrimSpace(quote)
	if base == "" || quote == "" {
		return symbolAssets{}
	}
	return symbolAssets{base: base, quote: quote}
}

// withSymbolAssets copies each match's base and quote asset codes from the
// catalogue the matches were searched in.
func withSymbolAssets(
	matches []fwmarketdata.SymbolMatch, known map[string]symbolAssets,
) []fwmarketdata.SymbolMatch {
	for i := range matches {
		assets := known[matches[i].Symbol]
		matches[i].BaseAsset = assets.base
		matches[i].QuoteAsset = assets.quote
	}
	return matches
}

func normalizedSymbolSearchKey(value string) string {
	return symbolsearch.NormalizedKey(value)
}

func splitSymbolPair(value string) (base, quote string, ok bool) {
	return symbolsearch.SplitPair(value)
}

func providerUnknownSymbolDiag(provider, external string) health.Diagnostic {
	return health.Diagnostic{
		Level:      health.DiagError,
		Code:       health.CodeUnknownSymbol,
		Kind:       health.DiagKindConfig,
		Instrument: external,
		Title:      fmt.Sprintf("Symbol %q not found on %s", external, provider),
		Detail: fmt.Sprintf(
			"The configured external symbol %q is not a %s instrument.",
			external,
			provider,
		),
		Remediation: "Remove this instrument and add one with a valid provider symbol. See the valid symbols list.",
		Actions: []health.DiagnosticAction{
			{Type: health.ActionRemoveInstrument, Target: external},
			{Type: health.ActionOpenSymbols},
		},
	}
}

func missingExternalSymbolError(provider string, sub fwmarketdata.Subscription) error {
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

type websocketReadResult struct {
	payload []byte
	err     error
}

const defaultWebsocketReadTimeout = fwmarketdata.FreshnessTTL

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

func (t *unparsableTracker) recordUnparsed(report func(health.Diagnostic)) {
	if t.reported {
		return
	}
	t.framesReceived++
	if t.framesReceived < unparsableThreshold || t.framesParsed != 0 {
		return
	}
	t.reported = true
	report(health.Diagnostic{
		Level:       health.DiagError,
		Code:        health.CodeUnparsableData,
		Kind:        health.DiagKindProvider,
		Title:       "Receiving data but cannot parse it",
		Detail:      "The source is sending data but Officer could not decode it (likely a format mismatch).",
		Remediation: "This is an internal issue, not your configuration - please report it.",
		Actions:     []health.DiagnosticAction{{Type: health.ActionRestart}},
	})
}
