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

package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Source identifies the channel through which a mutation was initiated.
type Source string

const (
	// SourcePanel is the operator web panel.
	SourcePanel Source = "panel"
	// SourceAPI is the HTTP management API.
	SourceAPI Source = "api"
	// SourceMCP is the MCP (model context protocol) tool surface.
	SourceMCP Source = "mcp"
	// SourceSystem is an internal system-initiated action.
	SourceSystem Source = "system"
)

// ValidateSource returns ErrInvalid when s is not a recognised Source value.
func ValidateSource(s Source) error {
	switch s {
	case SourcePanel, SourceAPI, SourceMCP, SourceSystem:
		return nil
	default:
		return fmt.Errorf("unknown source %q: %w", s, ErrInvalid)
	}
}

// Caller carries attribution for a recorded mutation. Later phases stamp it
// from request middleware; today callers fill it at call sites.
type Caller struct {
	// Source is the channel that initiated the action.
	Source Source
	// Principal is the identity string (e.g. "operator", "system").
	Principal string
	// Role is reserved for future authorisation checks; leave empty now.
	Role string
}

// AccountGroup is the control-plane view of a named account grouping. Membership
// is expressed via Account.GroupID; an account belongs to at most one group,
// matching the engine's group-hash semantics.
type AccountGroup struct {
	// Tenant is the isolation boundary that owns the group.
	Tenant TenantID
	// ID is the operator-facing group identifier, unique within Tenant.
	ID string
	// Notes is a free-form reference string, never forwarded to the engine.
	Notes string
	// BlockReason is the human-readable reason the group was blocked.
	BlockReason string
	// Blocked reports whether the group is currently kill-switched.
	Blocked bool
}

// ValidateGroupID returns ErrInvalid for group IDs that are empty, exceed 64
// chars, have leading/trailing whitespace, or contain non-printable characters.
// Mirrors ValidateAccountID — groups and accounts share the same id contract.
func ValidateGroupID(id string) error {
	if id == "" {
		return fmt.Errorf("group id is empty: %w", ErrInvalid)
	}
	if len(id) > 64 {
		return fmt.Errorf("group id exceeds 64 chars: %w", ErrInvalid)
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("group id has leading or trailing whitespace: %w", ErrInvalid)
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("group id contains non-printable character: %w", ErrInvalid)
		}
	}
	return nil
}

// ValidateNotes returns ErrInvalid when notes exceed the 4096-character limit.
func ValidateNotes(notes string) error {
	if len(notes) > 4096 {
		return fmt.Errorf("notes exceed 4096 chars: %w", ErrInvalid)
	}
	return nil
}

// Balance is the per-(account, asset) holdings snapshot. All amounts are exact
// decimal strings; never float. UpdatedAt is the wall-clock time the row was
// last written.
//
// Seam: when margin support is added a parallel "Position" type will carry
// instrument+collateral+average-entry-price; Balance is spot-only.
type Balance struct {
	// UpdatedAt is the wall-clock time this snapshot was last written.
	UpdatedAt time.Time
	// Available is the portion freely available for new orders.
	Available string
	// Held is the amount currently reserved by open orders.
	Held string
	// Incoming is funds in-flight (e.g. pending settlement).
	Incoming string
	// RealizedPnl is the cumulative settlement-asset realized P&L for this
	// (account, asset). It is delta-accumulated from operation outcomes, never
	// overwritten with an engine-reported absolute; a fresh row starts at "0".
	RealizedPnl string
	// AverageEntryPrice is optional; empty when not applicable.
	AverageEntryPrice string
	// Asset identifies the asset, e.g. "AAPL".
	Asset string
	// Account is the account that owns this balance.
	Account AccountID
	// Tenant is the isolation boundary.
	Tenant TenantID
}

// --- Market-data config -----------------------------------------------------

// Market-data provider types. Each value is the `type` discriminator stored on a
// MarketDataInstance and the switch key the connector manager constructs from.
const (
	// MarketDataProviderBYO is the bring-your-own provider: the customer pushes
	// their own quotes.
	MarketDataProviderBYO = "byo"
	// MarketDataProviderBinance is the public Binance spot feed.
	MarketDataProviderBinance = "binance"
	// MarketDataProviderMock is the synthetic provider used for demos and tests.
	MarketDataProviderMock = "mock"
)

// MarketDataInstance is one configured market-data source. Multiple instances of
// the same Type may coexist (e.g. two BYO feeds). Credentials is an opaque JSON
// blob, unencrypted for now; BYO and mock
// leave it empty.
type MarketDataInstance struct {
	// ID is the operator-facing instance identifier, unique across instances.
	ID string
	// Type is the provider discriminator (e.g. MarketDataProviderBYO).
	Type string
	// Label is a free-form human-readable name; may be empty.
	Label string
	// Credentials is an opaque provider-specific JSON blob; empty for BYO/mock.
	Credentials string
	// Enabled reports whether the instance participates in the runtime.
	Enabled bool
}

// MarketDataInstrument is one instrument of an instance: the external source
// symbol mapped to an engine instrument (base, quote). Per-instrument
// enable/disable lives here, so an instance can carry instruments that are
// configured but not yet streaming.
type MarketDataInstrument struct {
	// InstanceID is the owning instance.
	InstanceID string
	// ExternalSymbol is the source-side symbol (e.g. "AAPL").
	ExternalSymbol string
	// BaseAsset is the instrument underlying asset (e.g. "AAPL").
	BaseAsset string
	// QuoteAsset is the instrument settlement asset (e.g. "USD").
	QuoteAsset string
	// Enabled reports whether this instrument is subscribed at runtime.
	Enabled bool
}

// MarketDataQuote is the latest normalized quote observed for one configured
// market-data instrument.
type MarketDataQuote struct {
	// AsOf is the source observation time.
	AsOf time.Time
	// ReceivedAt is when Officer received and persisted the quote.
	ReceivedAt time.Time
	// InstanceID is the owning instance.
	InstanceID string
	// ExternalSymbol is the source-side symbol.
	ExternalSymbol string
	// BaseAsset is the instrument underlying asset.
	BaseAsset string
	// QuoteAsset is the instrument settlement asset.
	QuoteAsset string
	// Mark is the mark price as an exact decimal string; empty when absent.
	Mark string
	// Bid is the best-bid price as an exact decimal string; empty when absent.
	Bid string
	// Ask is the best-ask price as an exact decimal string; empty when absent.
	Ask string
}

// --- Adjustment types -------------------------------------------------------

// AdjustmentAmountMode describes how a per-field adjustment value is applied.
type AdjustmentAmountMode string

const (
	// AdjustmentModeAbsolute replaces the field with the given value.
	AdjustmentModeAbsolute AdjustmentAmountMode = "absolute"
	// AdjustmentModeDelta adds the value to the current field.
	AdjustmentModeDelta AdjustmentAmountMode = "delta"
)

// AdjustmentAmount carries one amount field (balance/held/incoming) in an
// adjustment request.
type AdjustmentAmount struct {
	// Mode is how the value is applied.
	Mode AdjustmentAmountMode `json:"mode"`
	// Value is an exact decimal string.
	Value string `json:"value"`
}

// AdjustmentBounds constrains acceptable resulting values for a single field.
type AdjustmentBounds struct {
	// Lower is an optional inclusive lower bound (exact decimal string).
	Lower string `json:"lower,omitempty"`
	// Upper is an optional inclusive upper bound (exact decimal string).
	Upper string `json:"upper,omitempty"`
}

// AdjustmentRequest is the payload of one spot-funds adjustment operation.
type AdjustmentRequest struct {
	// Asset identifies the asset being adjusted.
	Asset string `json:"asset"`
	// AverageEntryPrice is an optional replacement for the avg-entry field.
	AverageEntryPrice string `json:"average_entry_price,omitempty"`
	// Balance adjustment for the available field.
	Balance *AdjustmentAmount `json:"balance,omitempty"`
	// BalanceBounds optionally constrains the resulting balance.
	BalanceBounds *AdjustmentBounds `json:"balance_bounds,omitempty"`
	// Held adjustment for the held field.
	Held *AdjustmentAmount `json:"held,omitempty"`
	// HeldBounds optionally constrains the resulting held value.
	HeldBounds *AdjustmentBounds `json:"held_bounds,omitempty"`
	// Incoming adjustment for the incoming field.
	Incoming *AdjustmentAmount `json:"incoming,omitempty"`
	// IncomingBounds optionally constrains the resulting incoming value.
	IncomingBounds *AdjustmentBounds `json:"incoming_bounds,omitempty"`
}

// AdjustmentOutcomeAccepted carries the per-asset result of a successful
// adjustment.
type AdjustmentOutcomeAccepted struct {
	// BalanceDelta is the signed change applied to available (exact decimal).
	BalanceDelta string `json:"balance_delta"`
	// BalanceResult is the resulting absolute available value.
	BalanceResult string `json:"balance_result"`
	// HeldDelta is the signed change applied to held.
	HeldDelta string `json:"held_delta"`
	// HeldResult is the resulting absolute held value.
	HeldResult string `json:"held_result"`
	// IncomingDelta is the signed change applied to incoming.
	IncomingDelta string `json:"incoming_delta"`
	// IncomingResult is the resulting absolute incoming value.
	IncomingResult string `json:"incoming_result"`
	// RealizedPnlDelta is the signed change applied to the settlement-asset
	// realized P&L by this operation. It is accumulated onto the stored
	// balance; the engine's reported absolute is for display/cross-check only.
	RealizedPnlDelta string `json:"realized_pnl_delta"`
	// RealizedPnlResult is the engine-reported cumulative realized P&L after the
	// operation. Display/cross-check only; never persisted as an absolute.
	RealizedPnlResult string `json:"realized_pnl_result"`
}

// AdjustmentOutcomeRejected carries the structured rejection reason.
type AdjustmentOutcomeRejected struct {
	Code    string `json:"code"`
	Scope   string `json:"scope,omitempty"`
	Policy  string `json:"policy,omitempty"`
	Reason  string `json:"reason"`
	Details string `json:"details,omitempty"`
}

// AccountAdjustmentRecord is the append-only history of a single spot-funds
// edit. Payload and outcome are typed; the store marshals them to JSON.
type AccountAdjustmentRecord struct {
	// At is the wall-clock time the adjustment was submitted.
	At time.Time
	// Request is the adjustment payload submitted by the caller.
	Request AdjustmentRequest
	// Accepted is non-nil when the engine accepted the adjustment.
	Accepted *AdjustmentOutcomeAccepted
	// Rejected is non-nil when the engine rejected the adjustment.
	Rejected *AdjustmentOutcomeRejected
	// Principal identifies who initiated the action.
	Principal string
	// Tenant is the isolation boundary.
	Tenant TenantID
	// Account is the account that was adjusted.
	Account AccountID
	// Source is the channel that originated the adjustment.
	Source Source
	// ID is the store-assigned monotonically increasing identifier.
	ID int64
}

// AdjustmentStatus summarises the outcome of an adjustment for indexed queries.
type AdjustmentStatus string

const (
	AdjustmentStatusAccepted AdjustmentStatus = "accepted"
	AdjustmentStatusRejected AdjustmentStatus = "rejected"
)

// --- Order types ------------------------------------------------------------

// OrderSide is the direction of a trading order.
type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

// OrderAmountKind distinguishes quantity-based from volume-based order sizing.
type OrderAmountKind string

const (
	OrderAmountKindQuantity OrderAmountKind = "quantity"
	OrderAmountKindVolume   OrderAmountKind = "volume"
)

// OrderStatus is the lifecycle state of an order recorded by Officer.
type OrderStatus string

const (
	OrderStatusSubmitted       OrderStatus = "submitted"
	OrderStatusAccepted        OrderStatus = "accepted"
	OrderStatusRejected        OrderStatus = "rejected"
	OrderStatusCommitted       OrderStatus = "committed"
	OrderStatusRolledBack      OrderStatus = "rolled_back"
	OrderStatusFilled          OrderStatus = "filled"
	OrderStatusPartiallyFilled OrderStatus = "partially_filled"
	OrderStatusCancelled       OrderStatus = "cancelled"
)

// Order is the Officer-side record of an order that passed through the system,
// including rejected ones. All monetary/size values are exact decimal strings.
type Order struct {
	// At is the wall-clock time the order was submitted.
	At time.Time
	// AmountValue is the size of the order (exact decimal string).
	AmountValue string
	// Price is the limit price (exact decimal string); empty for market orders.
	Price string
	// Principal identifies who submitted the order.
	Principal string
	// BaseAsset is the asset being bought or sold.
	BaseAsset string
	// QuoteAsset is the asset used for pricing.
	QuoteAsset string
	// LockPrices is an optional list of reference prices for PnL locks
	// (exact decimal strings). Marshalled to JSON in the store.
	LockPrices []string
	// Tenant is the isolation boundary.
	Tenant TenantID
	// Account is the account that placed the order.
	Account AccountID
	// Source is the channel that submitted the order.
	Source Source
	// Side is buy or sell.
	Side OrderSide
	// AmountKind distinguishes quantity-based from volume-based sizing.
	AmountKind OrderAmountKind
	// Status is the current lifecycle state.
	Status OrderStatus
	// ID is the store-assigned order identifier (string of int64).
	ID int64
}

// OrderEventType classifies a single event in an order's lifecycle.
type OrderEventType string

const (
	OrderEventSubmitted             OrderEventType = "submitted"
	OrderEventPreTradeAccepted      OrderEventType = "pre_trade_accepted"
	OrderEventPreTradeRejected      OrderEventType = "pre_trade_rejected"
	OrderEventReservationCommitted  OrderEventType = "reservation_committed"
	OrderEventReservationRolledBack OrderEventType = "reservation_rolled_back"
	OrderEventFill                  OrderEventType = "fill"
	OrderEventCancelled             OrderEventType = "cancelled"
)

// OrderEventPayload is the JSON-marshalled variant payload for an order event.
// Only the fields relevant to the event type are populated.
type OrderEventPayload struct {
	// Reject fields — populated for pre_trade_rejected events.
	RejectCode    string `json:"reject_code,omitempty"`
	RejectScope   string `json:"reject_scope,omitempty"`
	RejectPolicy  string `json:"reject_policy,omitempty"`
	RejectReason  string `json:"reject_reason,omitempty"`
	RejectDetails string `json:"reject_details,omitempty"`

	// Fill fields — populated for fill events.
	FillQuantity  string `json:"fill_quantity,omitempty"`
	FillPrice     string `json:"fill_price,omitempty"`
	FillLockPrice string `json:"fill_lock_price,omitempty"`
}

// OrderEvent is one immutable record in an order's event stream.
type OrderEvent struct {
	// At is the wall-clock time the event was recorded.
	At time.Time
	// Payload carries the event-specific structured data.
	Payload OrderEventPayload
	// Principal identifies who triggered the event.
	Principal string
	// Source is the channel that triggered the event.
	Source Source
	// Type classifies the event.
	Type OrderEventType
	// OrderID links back to the parent order.
	OrderID int64
	// ID is the store-assigned event identifier.
	ID int64
}

// Trade is the per-fill record backing the standalone trades list. One Trade is
// recorded for each fill event. All monetary values are exact decimal strings.
type Trade struct {
	// At is the wall-clock time the fill was recorded.
	At time.Time
	// Quantity is the filled quantity (exact decimal string).
	Quantity string
	// Price is the fill price (exact decimal string).
	Price string
	// LockPrice is the reference price used for PnL locking; empty if none.
	LockPrice string
	// Principal identifies who submitted the originating order.
	Principal string
	// BaseAsset is the asset that was filled.
	BaseAsset string
	// QuoteAsset is the asset used for pricing.
	QuoteAsset string
	// Tenant is the isolation boundary.
	Tenant TenantID
	// Account is the account that received the fill.
	Account AccountID
	// Source is the channel that submitted the originating order.
	Source Source
	// Side is the direction of the fill.
	Side OrderSide
	// OrderID links back to the originating order.
	OrderID int64
	// ID is the store-assigned trade identifier.
	ID int64
}

// OrderDetail bundles an order together with its events and trades; returned
// by GetOrder.
type OrderDetail struct {
	Order  Order
	Events []OrderEvent
	Trades []Trade
}

// OrderReject is the transport-agnostic view of one engine pre-trade reject.
// It is the boundary shape the engine adapter translates a reject.Reject into;
// the node copies it onto a pre_trade_rejected OrderEvent. Code and Scope are
// stable strings, not the binding's native enum values.
type OrderReject struct {
	Code    string `json:"code"`
	Scope   string `json:"scope,omitempty"`
	Policy  string `json:"policy,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Details string `json:"details,omitempty"`
}

// ExecutionReportInput is the post-trade fill the node hands the engine to
// settle. It is spot-only this phase: instrument is (BaseAsset, QuoteAsset),
// the fill carries one quantity and price, and LockPrice is the single
// reference price captured at reservation time (empty when the order carried no
// lock). All values are exact decimal strings; never float.
type ExecutionReportInput struct {
	// BaseAsset is the instrument underlying asset that was filled.
	BaseAsset string
	// QuoteAsset is the instrument settlement asset.
	QuoteAsset string
	// FillQuantity is the filled quantity (exact decimal string).
	FillQuantity string
	// FillPrice is the fill price (exact decimal string).
	FillPrice string
	// LockPrice is the reference price for the fill's PnL lock; empty when the
	// originating order carried no lock.
	LockPrice string
	// Account is the account that received the fill.
	Account AccountID
	// Side is the direction of the fill.
	Side OrderSide
	// OrderID links the fill to the Officer order it settles. The fill event and
	// trade reference it (order_events/trades are NOT NULL FKs to orders), and
	// the order's status is reflected from the fill.
	OrderID int64
	// Final reports whether this fill closes the order: true reflects status
	// filled, false reflects partially_filled.
	Final bool
}

// ExecutionAccountBlock is one engine-recorded account block returned by an
// execution report. The engine has already applied the block; the node mirrors
// it into the store. Account is carried from the report's account, since the
// binding's block record does not name the account itself.
type ExecutionAccountBlock struct {
	// Account is the account the engine blocked.
	Account AccountID
	// Code is the stable reject code that triggered the block.
	Code string
	// Reason is the human-readable block reason.
	Reason string
	// Details is the case-specific block detail.
	Details string
}
