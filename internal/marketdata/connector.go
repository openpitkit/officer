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
	"context"
	"time"
)

const (
	DiagError = "error"
	DiagWarn  = "warn"
	DiagInfo  = "info"

	// Diagnostic kinds - who resolves the problem.
	DiagKindConfig      = "config"      // operator configuration
	DiagKindEnvironment = "environment" // network/region/reachability
	DiagKindProvider    = "provider"    // provider-side or internal

	// Diagnostic codes.
	CodeUnknownSymbol        = "unknown_symbol"
	CodeNoEnabledInstruments = "no_enabled_instruments"
	CodeUnsupportedProvider  = "unsupported_provider"
	CodeConnectionError      = "connection_error"
	CodeNoData               = "no_data"
	CodeInternalError        = "internal_error"
	CodeSelfDiagnosisFailed  = "self_diagnosis_failed"
	CodeUnparsableData       = "unparsable_data"

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
// provider's catalogue. Exists is true when the symbol is present as typed. When
// Exists is false, Suggestion optionally carries a case-folded catalogue variant
// the operator likely meant (e.g. "ETHUSDT" for "ethusdt"); it is empty when no
// variant matches.
type SymbolVerification struct {
	Exists     bool
	Suggestion string
}

// SymbolVerifier is the optional capability a connector implements to check
// whether an external symbol exists in the provider's catalogue. A connector
// that cannot answer this (no symbol catalogue) does not implement it, so the
// capability is absent. VerifySymbol returns an error only on a catalogue-fetch
// or transport failure, never to signal that the symbol is unknown.
type SymbolVerifier interface {
	VerifySymbol(ctx context.Context, external string) (SymbolVerification, error)
}

// DiagnosticReporter lets a connector push a structured diagnostic to the
// manager at any time (config validation, dropped symbols, runtime issues).
type DiagnosticReporter func(diag Diagnostic)

// DiagnosticReporting is the optional capability a connector implements to
// receive a DiagnosticReporter from the manager.
type DiagnosticReporting interface {
	SetDiagnosticReporter(report DiagnosticReporter)
}

// Diagnosable is the optional capability a connector implements to perform
// active self-diagnosis against the provider. The manager calls it once when a
// live feed has been silent for diagnoseGrace. Returning an error means the
// diagnosis itself failed (not that any symbol is bad); returning an empty
// slice means the provider sees the symbols as valid.
type Diagnosable interface {
	Diagnose(ctx context.Context) ([]Diagnostic, error)
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
