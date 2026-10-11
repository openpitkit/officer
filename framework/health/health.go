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

// Package health is the diagnostic model shared by market-data sources and
// trading connections: structured diagnostics with their levels, kinds, codes
// and remediation actions, the reporters a source uses to push diagnostics and
// connection state to its owner, and the bounded diagnostic buffer.
package health

import (
	"log/slog"
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

// bufferCap bounds a source's diagnostic buffer; Record drops the oldest
// entries beyond it.
const bufferCap = 20

// DiagnosticAction is a machine-readable remediation step the UI turns into a
// button. Target is the affected external symbol for instrument actions.
type DiagnosticAction struct {
	Type   string
	Target string
}

// Diagnostic is one structured, resolution-oriented problem for a source.
// Code and Kind are machine-readable; Title and Detail are human-readable.
// Instrument is the affected external symbol (empty when source-wide).
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

// DiagnosticReporter lets a source push a structured diagnostic to its owner
// at any time (config validation, dropped symbols, runtime issues).
type DiagnosticReporter func(diag Diagnostic)

// DiagnosticReporting is the optional capability a source implements to
// receive a DiagnosticReporter from its owner.
type DiagnosticReporting interface {
	SetDiagnosticReporter(report DiagnosticReporter)
}

// StatusReporter receives a source's runtime connection state. ok=true means
// connected/receiving; ok=false carries a short failure message. It is called
// from the source's background goroutine, never synchronously from the call
// that starts the source.
type StatusReporter func(ok bool, errMsg string)

// StatusReporting is the optional capability a source implements to report its
// runtime connection state back to its owner. Sources that cannot fail at
// connection time (push-based BYO, in-memory mock) need not implement it.
type StatusReporting interface {
	SetStatusReporter(report StatusReporter)
}

// Record appends diag to diags and returns the result. A zero diag.At is set
// to time.Now().UTC() first.
//
// When the last element of diags has the same Code, Instrument and Detail as
// diag, Record refreshes that element's At in place - writing through to the
// caller's backing array - logs nothing, and returns diags with the same
// length. Only the last element is compared, so diagnostics that alternate are
// each recorded.
//
// Otherwise Record emits exactly one record on logger with message diag.Title
// and attributes attrs, then "code", then "instrument" and "detail" when they
// are non-empty, at level Debug when diag.Routine is set, Error for DiagError,
// Warn for DiagWarn and Info for anything else. It then appends diag and keeps
// only the newest 20 entries.
//
// Record does not synchronise; the caller guards diags.
func Record(logger *slog.Logger, diags []Diagnostic, diag Diagnostic, attrs ...any) []Diagnostic {
	if diag.At.IsZero() {
		diag.At = time.Now().UTC()
	}
	if n := len(diags); n > 0 {
		last := &diags[n-1]
		if last.Code == diag.Code &&
			last.Instrument == diag.Instrument &&
			last.Detail == diag.Detail {
			last.At = diag.At
			return diags
		}
	}
	// Emit exactly one log record per new (non-deduped) diagnostic. The
	// attributes are built in a fresh slice so a caller's attrs backing array
	// is never written.
	logAttrs := make([]any, 0, len(attrs)+6)
	logAttrs = append(logAttrs, attrs...)
	logAttrs = append(logAttrs, "code", diag.Code)
	if diag.Instrument != "" {
		logAttrs = append(logAttrs, "instrument", diag.Instrument)
	}
	if diag.Detail != "" {
		logAttrs = append(logAttrs, "detail", diag.Detail)
	}
	switch {
	case diag.Routine:
		logger.Debug(diag.Title, logAttrs...)
	case diag.Level == DiagError:
		logger.Error(diag.Title, logAttrs...)
	case diag.Level == DiagWarn:
		logger.Warn(diag.Title, logAttrs...)
	default:
		logger.Info(diag.Title, logAttrs...)
	}
	diags = append(diags, diag)
	if len(diags) > bufferCap {
		diags = diags[len(diags)-bufferCap:]
	}
	return diags
}
