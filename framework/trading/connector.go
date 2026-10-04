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

package trading

import (
	"context"
	"errors"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// ErrRejected means the venue refused, or would refuse, the order. No venue
// order exists, including when a pre-check returns this error.
var ErrRejected = errors.New("trading: venue rejected the order")

// ErrUnsupported means the connector cannot trade this order or instrument.
var ErrUnsupported = errors.New("trading: not supported by the connector")

// ErrAdjustmentRejected means the core rejected a venue fee adjustment. The
// adjustment id has been consumed; the wrapped error must include the reason.
var ErrAdjustmentRejected = errors.New("trading: adjustment rejected")

// Order carries the mapped venue instrument and exact Officer order amount.
type Order struct {
	ClientOrderID string // Runtime-generated; unique per connection.
	Symbol        string // Venue symbol from the instrument mapping.
	Side          domain.OrderSide
	AmountKind    domain.OrderAmountKind
	Quantity      string // Exact decimal Officer order amount.
	LimitPrice    string // Exact decimal; empty for a market order.
	VenueAccount  string // Empty when the venue has no account selection.
	Route         string // Connector-read per-order JSON.
}

// Ack is the venue's acknowledgement of a successful send.
type Ack struct{ VenueOrderID string }

// OrderRef identifies an order even before its venue id is known.
type OrderRef struct{ ClientOrderID, VenueOrderID string }

// Fill is one execution, preserving venue quantities and commission currency.
type Fill struct {
	Quantity       string // This execution's exact decimal quantity.
	Price          string // This execution's exact decimal price.
	CumQuantity    string // Cumulative filled quantity after this execution.
	LeavesQuantity string // Venue open quantity; non-negative plain decimal.
	Final          bool   // The venue reports fully filled with this execution.
	Commission     *domain.Commission
	At             time.Time
}

// VenueStatus is the venue's current lifecycle state.
type VenueStatus string

const (
	VenueStatusOpen   VenueStatus = "open"
	VenueStatusFilled VenueStatus = "filled"
	// VenueStatusCancelled includes venue cancellation, expiry and rejection.
	VenueStatusCancelled VenueStatus = "cancelled"
	// VenueStatusReplaced requires operator attention; replacements are not
	// followed.
	VenueStatusReplaced VenueStatus = "replaced"
)

// Snapshot contains every execution so far, in execution order.
type Snapshot struct {
	VenueOrderID   string
	Status         VenueStatus
	FilledQuantity string // Venue cumulative filled quantity.
	Fills          []Fill
	Reason         string // Venue terminal text, intended for logs.
}

// EventKind identifies a stream message the runtime must process.
type EventKind string

const (
	// EventConnected requests reconciliation after every stream (re)connect.
	EventConnected EventKind = "connected"
	// EventFill carries one execution in Fill.
	EventFill EventKind = "fill"
	// EventOrder requests Lookup for another order change, including terminals.
	EventOrder EventKind = "order"
)

// Event identifies a venue order by client id. EventOrder data is read through
// Lookup, so a terminal stream message cannot bypass missing fills.
// Every event about an Officer order must carry its Officer ClientOrderID in
// Ref, even when the venue order id is also known.
type Event struct {
	Kind EventKind
	Ref  OrderRef
	Fill Fill
}

// Connector places and tracks orders on one trading connection. The connector
// owns credentials; its errors and venue text must never contain credentials.
// Every event about an Officer order must carry the ClientOrderID supplied to
// Send. The runtime keys venue orders by that id, not by the venue order id.
type Connector interface {
	// Subscribe starts a self-reconnecting stream. Every (re)connect emits
	// EventConnected; cancellation or Close ends and closes the channel.
	Subscribe(ctx context.Context) (<-chan Event, error)
	// Send acknowledges with a non-empty venue id. ErrRejected and
	// ErrUnsupported are definitive: no order exists. Other errors leave the
	// outcome unknown and must not cause another send.
	Send(ctx context.Context, order Order) (Ack, error)
	// Lookup returns found=false when the venue does not know the client id.
	Lookup(ctx context.Context, ref OrderRef) (Snapshot, bool, error)
	Close()
}

// PreChecker checks venue rules before Officer takes a reservation. A venue
// refusal wraps ErrRejected or ErrUnsupported.
type PreChecker interface {
	PreCheck(ctx context.Context, order Order) error
}

// SymbolVerifier checks a mapped venue symbol without opening a stream.
type SymbolVerifier interface {
	VerifySymbol(ctx context.Context, external string) (SymbolVerification, error)
}

// SymbolVerification reports whether the venue knows and can trade a symbol.
type SymbolVerification struct {
	Exists   bool
	Tradable bool
	Details  string
}

// FeeReader is implemented by a venue that posts fees separately from fills.
// Its errors and fee text must never contain credentials.
type FeeReader interface {
	// Fees returns one venue page, oldest first, strictly after the activity
	// id after. Empty after starts at since. Next is the last activity id to
	// continue from, or empty for the last page. Fees preceding a read error
	// may be returned and consumed before retrying after their last id.
	// Reads and asset discovery must honor context cancellation.
	Fees(ctx context.Context, since time.Time, after string) (fees []Fee, next string, err error)
	// InstrumentAssets returns the venue's base and quote asset codes for a
	// venue symbol, without network access.
	InstrumentAssets(symbol string) (base, quote string, err error)
}

// FeeStatus determines whether a posted fee can be applied as an adjustment.
type FeeStatus string

const (
	// FeeExecuted is a posted fee to apply.
	FeeExecuted FeeStatus = "executed"
	// FeeNotApplicable is never applied; Detail explains the venue shape or
	// status requiring operator attention.
	FeeNotApplicable FeeStatus = "not_applicable"
)

// Fee is a separate venue activity, never a computed execution commission.
type Fee struct {
	ID     string    // Venue fee id, unique per connection.
	Asset  string    // Venue asset code charged, set for FeeExecuted.
	Amount string    // Signed exact decimal balance delta; negative is a charge.
	Status FeeStatus // Whether to apply the activity.
	Detail string    // Venue text or reason it is not applicable.
	At     time.Time // Activity creation time.
}
