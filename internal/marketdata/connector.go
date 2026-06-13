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

// Sink receives normalized quotes and forwards them to the engine. It is
// implemented in the engine package over the binding's market-data service.
type Sink interface {
	// Push forwards one normalized quote into the engine. It returns an error if
	// the quote cannot be registered or pushed.
	Push(update QuoteUpdate) error
}
