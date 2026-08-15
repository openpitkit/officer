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

// Package marketdata defines the framework-level quote sink seam shared by
// engine and node implementations.
package marketdata

import (
	"math"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/domain"
)

// FreshnessTTL is the officer-wide quote freshness window: a quote whose
// source AsOf is older than this is displayed as absent.
const FreshnessTTL = 70 * time.Second

// QuoteUpdate is one quote normalized to an opaque asset key, ready for the
// sink. Connectors return Base and Quote verbatim from Subscription; they never
// derive them from a provider symbol. Price fields are exact decimal strings
// (empty means absent), matching param.NewPriceFromString; only the fields the
// engine consumes are carried (mark/bid/ask + timestamp). AsOf is the source
// observation time.
type QuoteUpdate struct {
	// AsOf is the source observation time of the quote.
	AsOf time.Time
	// Base is the stable internal identifier of the underlying asset.
	Base domain.EngineAssetID
	// Quote is the stable internal identifier of the settlement asset.
	Quote domain.EngineAssetID
	// Mark is the mark price as an exact decimal string; empty when absent.
	Mark string
	// Bid is the best-bid price as an exact decimal string; empty when absent.
	Bid string
	// Ask is the best-ask price as an exact decimal string; empty when absent.
	Ask string

	// The manual fields are manager-internal controls. A BYO manual clear travels
	// through the connector channel so it is ordered after every already queued
	// manual refresh without widening the public Sink contract. Generation and
	// token reject work from superseded manager lifecycles before touching a sink.
	clear            bool
	clearDone        chan error
	clearDirect      bool
	clearSynthetic   bool
	manual           bool
	manualGeneration uint64
	manualToken      uint64
}

// InvertQuote returns the synthetic reverse quote using exact decimal
// arithmetic. Fields that are empty, zero, or invalid are omitted; ok is false
// when no field can be inverted.
func InvertQuote(update QuoteUpdate) (inverted QuoteUpdate, ok bool) {
	inverted = QuoteUpdate{
		AsOf:  update.AsOf,
		Base:  update.Quote,
		Quote: update.Base,
	}
	inverted.Mark = invertDecimal(update.Mark)
	inverted.Bid = invertDecimal(update.Ask)
	inverted.Ask = invertDecimal(update.Bid)
	ok = inverted.Mark != "" || inverted.Bid != "" || inverted.Ask != ""
	return inverted, ok
}

func invertDecimal(value string) string {
	if value == "" {
		return ""
	}
	d, err := decimal.NewFromString(value)
	if err != nil || !d.IsPositive() {
		return ""
	}
	precision := int64(decimal.DivisionPrecision)
	coefficient := d.Coefficient()
	coefficient.Abs(coefficient)
	coefficientDigits := len(coefficient.String())
	adjustedExponent := int64(coefficientDigits-1) + int64(d.Exponent())
	if adjustedExponent >= 0 {
		precision += adjustedExponent
	}
	if precision > math.MaxInt32 {
		return ""
	}
	inverted := decimal.NewFromInt(1).DivRound(d, int32(precision))
	if inverted.IsZero() {
		return ""
	}
	return inverted.String()
}

// Sink receives normalized quotes and forwards them to the engine.
type Sink interface {
	// Push forwards one normalized quote into the engine. It returns an error if
	// the quote cannot be registered or pushed.
	Push(update QuoteUpdate) error
}

// QuoteClearer is an optional Sink capability for removing one live quote
// without replacing the engine. Native Officer engines implement it through
// the SDK market-data service; adapters that cannot clear may omit it.
type QuoteClearer interface {
	// Clear removes the stored quote for the identified instrument. Clearing an
	// instrument that has never been registered is a no-op.
	Clear(base, quote domain.EngineAssetID) error
}
