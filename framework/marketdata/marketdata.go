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

import "time"

// FreshnessTTL is the officer-wide quote freshness window: a quote whose
// source AsOf is older than this is displayed as absent.
const FreshnessTTL = 70 * time.Second

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

// Sink receives normalized quotes and forwards them to the engine.
type Sink interface {
	// Push forwards one normalized quote into the engine. It returns an error if
	// the quote cannot be registered or pushed.
	Push(update QuoteUpdate) error
}
