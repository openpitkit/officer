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
	"context"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
)

// Subscription is one enabled instrument of an instance. External is the
// provider symbol and must be supplied by configuration. Base and Quote form an
// opaque asset key: connectors return them verbatim in QuoteUpdate and never
// derive provider values from them.
type Subscription struct {
	// External is the configured provider-side symbol (e.g. "AAPL").
	External string
	// Base is the stable internal identifier of the underlying asset. Connectors
	// must return it unchanged and must never derive provider values from it.
	Base domain.EngineAssetID
	// Quote is the stable internal identifier of the settlement asset. Connectors
	// must return it unchanged and must never derive provider values from it.
	Quote domain.EngineAssetID
	// SyntheticInverse reports whether the applied runtime also emits the
	// reverse pair for this subscription.
	SyntheticInverse bool
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

// Diagnosable is the optional capability a connector implements to perform
// active self-diagnosis against the provider. The manager calls it once when a
// live feed has been silent for diagnoseGrace. Returning an error means the
// diagnosis itself failed (not that any symbol is bad); returning an empty
// slice means the provider sees the symbols as valid.
type Diagnosable interface {
	Diagnose(ctx context.Context) ([]health.Diagnostic, error)
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

// Pushable is the optional capability a connector implements to accept a quote
// pushed by the operator rather than pulled from a source. The bring-your-own
// connector implements it; streaming providers do not. The manager uses it to
// deliver an operator-set manual mark price once - at startup and on upsert -
// onto the connector's subscription channel, from where it drains into the sink
// like any other quote. Push must stop waiting when ctx is cancelled.
type Pushable interface {
	Push(ctx context.Context, update QuoteUpdate) error
}
