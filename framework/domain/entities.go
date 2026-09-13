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
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
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

const (
	// PrincipalOperator is the open composition's explicit operator identity.
	PrincipalOperator = "operator"
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

// Caller carries the server-owned surface and resolved request identity used
// for authorization and mutation attribution.
type Caller struct {
	// Source is the channel that initiated the action.
	Source Source
	// Principal is the identity string (e.g. "operator", "system").
	Principal string
	// Role is reserved for future authorisation checks; leave empty now.
	Role string
}

// PnlHaltReason identifies why the engine stopped a realized-P&L calculation.
// Empty means that the engine reported an authoritative P&L amount.
type PnlHaltReason string

const (
	// PnlHaltReasonMissingFx means a required FX quote was unavailable.
	PnlHaltReasonMissingFx PnlHaltReason = "missing_fx"
	// PnlHaltReasonMissingAccountCurrency means the account had no effective
	// currency for the calculation.
	PnlHaltReasonMissingAccountCurrency PnlHaltReason = "missing_account_currency"
	// PnlHaltReasonMissingInitialPnl means an authoritative initial P&L value
	// was unavailable for a position accumulator.
	PnlHaltReasonMissingInitialPnl PnlHaltReason = "missing_initial_pnl"
	// PnlHaltReasonMissingCostBasis means the position lacked the cost basis
	// required to calculate realized P&L.
	PnlHaltReasonMissingCostBasis PnlHaltReason = "missing_cost_basis"
	// PnlHaltReasonArithmeticOverflow means exact P&L arithmetic exceeded the
	// supported numeric range.
	PnlHaltReasonArithmeticOverflow PnlHaltReason = "arithmetic_overflow"
)

// ValidatePnlHaltReason returns ErrInvalid unless r is empty (the engine
// reported an authoritative amount) or a reason the engine can emit.
//
// Persisted halt reasons are replayed into the engine when it is rebuilt at
// start, and a reason the engine does not accept fails that rebuild - so a
// value that reaches storage from an untrusted backup restore would block the
// next boot. Reject it at the boundary instead.
func ValidatePnlHaltReason(r PnlHaltReason) error {
	switch r {
	case "",
		PnlHaltReasonMissingFx,
		PnlHaltReasonMissingAccountCurrency,
		PnlHaltReasonMissingInitialPnl,
		PnlHaltReasonMissingCostBasis,
		PnlHaltReasonArithmeticOverflow:
		return nil
	default:
		return fmt.Errorf("unknown pnl halt reason %q: %w", r, ErrInvalid)
	}
}

// AccountGroup is the control-plane view of a named account grouping: a
// dictionary entity addressed by its public Code, displayed under a mutable
// Title, and run on the engine under EngineGroupID (its surrogate id).
// Membership is expressed via
// Account.GroupCode; an account belongs to at most one group. The surrogate key
// never appears here.
type AccountGroup struct {
	// Code is the operator-chosen group code, unique per realm.
	Code string
	// Title is the mutable human-readable display name; may be empty.
	Title string
	// Currency is the group-level realized P&L currency asset code. Empty means
	// member accounts inherit from the reserved default group unless they carry
	// an account-level currency.
	Currency string
	// Notes is a free-form reference string, never forwarded to the engine.
	Notes string
	// BlockReason is the human-readable reason the group was blocked.
	BlockReason string
	// EngineGroupID is the integer id the engine runs this group on: the group
	// row's surrogate id. It is internal and never serialized on the wire
	// (json:"-"); zero means unassigned. The engine adapter maps codes to this
	// identifier on inputs and maps it back to codes on outputs; it is never a
	// public handle.
	EngineGroupID EngineGroupID `json:"-"`
	// Blocked reports whether the group is currently kill-switched.
	Blocked bool
}

// Asset is a dictionary entity naming one tradable asset, addressed by its Code
// (e.g. "AAPL", "USD"), displayed under a Title, and optionally classified by
// AssetClass. Those three fields are operator-editable. EngineAssetID carries
// the row's surrogate key for internal engine use; it is never a public handle
// or wire field.
type Asset struct {
	// Code is the operator-chosen asset code, unique per realm and mutable.
	Code string
	// Title is the mutable human-readable display name; may be empty.
	Title string
	// AssetClass is an optional classification (e.g. "equity", "fx"); empty
	// when unset.
	AssetClass string
	// EngineAssetID is the integer id the engine runs this asset on: the asset
	// row's surrogate id. It is internal and never serialized on the wire
	// (json:"-"); zero means unassigned. The engine adapter maps codes to this
	// identifier on inputs and maps it back to codes on outputs; it is never a
	// public handle.
	EngineAssetID EngineAssetID `json:"-"`
}

// AssetClass is a dictionary entity naming one asset classification (e.g.
// "equity", "fx"), addressed by its public Code, displayed under a mutable
// Title, with free-form Notes. Assets link to it via Asset.AssetClass holding
// the class Code; a class carries no block concept. The surrogate key never
// appears here.
type AssetClass struct {
	// Code is the operator-chosen class code, unique per realm.
	Code string
	// Title is the mutable human-readable display name; may be empty.
	Title string
	// Notes is a free-form reference string.
	Notes string
}

// Principal is a dictionary entity naming one actor that initiates control-plane
// actions, addressed by its immutable Code, displayed under a mutable Title. The
// surrogate key never appears here.
type Principal struct {
	// Code is the immutable, operator-chosen principal code, unique per realm.
	Code string
	// Title is the mutable human-readable display name; may be empty.
	Title string
}

// ValidatePrincipalID returns ErrInvalid for principal codes that are empty,
// exceed 64 code points, have leading or trailing whitespace, contain invalid
// UTF-8, or contain non-printable characters.
func ValidatePrincipalID(id string) error {
	if id == "" {
		return fmt.Errorf("principal id is empty: %w", ErrInvalid)
	}
	if !utf8.ValidString(id) {
		return fmt.Errorf("principal id contains invalid UTF-8: %w", ErrInvalid)
	}
	if utf8.RuneCountInString(id) > 64 {
		return fmt.Errorf("principal id exceeds 64 code points: %w", ErrInvalid)
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf(
			"principal id has leading or trailing whitespace: %w", ErrInvalid,
		)
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return fmt.Errorf(
				"principal id contains non-printable character: %w", ErrInvalid,
			)
		}
	}
	return nil
}

// isPathDotSegment reports whether a dictionary code is a relative path
// segment. Such a code is not addressable through a normal client stack:
// browsers, proxies, and any path.Clean-ing intermediary silently rewrite a dot
// segment out of the URL, so the request that arrives names something else. A
// stored identity must not depend on every hop leaving it alone.
func isPathDotSegment(code string) bool {
	return code == "." || code == ".."
}

// ReservedGroupCode is the API sentinel that addresses the realm default group
// in a path position, as in PUT /groups/-/default/currency. It is reserved for
// groups only: no other dictionary route puts a literal segment where its code
// goes, so account, asset and asset-class codes are unaffected.
const ReservedGroupCode = "-"

// ValidateGroupID returns ErrInvalid for group codes that are empty, the
// reserved default-group sentinel, a relative path segment, exceed 64 code
// points, have leading/trailing whitespace, contain invalid UTF-8, or contain
// non-printable characters. Mirrors ValidateAccountID - groups and accounts
// share the same code contract, plus the group-only sentinel.
//
// The sentinel is rejected here rather than only at the REST boundary so every
// writer inherits it: a group actually named "-" would be shadowed by the
// default-group route and could never be renamed or deleted again.
func ValidateGroupID(id string) error {
	if id == "" {
		return invalidField("/code", "required", fmt.Errorf("group id is empty: %w", ErrInvalid))
	}
	if !utf8.ValidString(id) {
		return invalidField("/code", "format", fmt.Errorf("group id contains invalid UTF-8: %w", ErrInvalid))
	}
	if id == ReservedGroupCode {
		return invalidField("/code", "format", fmt.Errorf("group id %q is reserved: %w", id, ErrInvalid))
	}
	if isPathDotSegment(id) {
		return invalidField(
			"/code", "format",
			fmt.Errorf(
				"group id %q is a reserved path segment: %w", id, ErrInvalid,
			),
		)
	}
	if utf8.RuneCountInString(id) > 64 {
		return invalidField("/code", "max_length", fmt.Errorf("group id exceeds 64 code points: %w", ErrInvalid))
	}
	if strings.TrimSpace(id) != id {
		return invalidField(
			"/code", "format",
			fmt.Errorf(
				"group id has leading or trailing whitespace: %w", ErrInvalid,
			),
		)
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return invalidField(
				"/code", "format",
				fmt.Errorf(
					"group id contains non-printable character: %w", ErrInvalid,
				),
			)
		}
	}
	return nil
}

// ValidateAssetClassID returns ErrInvalid for asset-class codes that are empty,
// a relative path segment, exceed 64 code points, have leading/trailing
// whitespace, contain invalid UTF-8, or contain non-printable characters.
// Mirrors ValidateGroupID - asset classes are a dictionary addressed by the
// same code contract as groups.
func ValidateAssetClassID(id string) error {
	if id == "" {
		return invalidField("/code", "required", fmt.Errorf("asset class id is empty: %w", ErrInvalid))
	}
	if !utf8.ValidString(id) {
		return invalidField("/code", "format", fmt.Errorf("asset class id contains invalid UTF-8: %w", ErrInvalid))
	}
	if isPathDotSegment(id) {
		return invalidField(
			"/code", "format",
			fmt.Errorf(
				"asset class id %q is a reserved path segment: %w", id, ErrInvalid,
			),
		)
	}
	if utf8.RuneCountInString(id) > 64 {
		return invalidField("/code", "max_length", fmt.Errorf("asset class id exceeds 64 code points: %w", ErrInvalid))
	}
	if strings.TrimSpace(id) != id {
		return invalidField(
			"/code", "format",
			fmt.Errorf(
				"asset class id has leading or trailing whitespace: %w", ErrInvalid,
			),
		)
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return invalidField(
				"/code", "format",
				fmt.Errorf(
					"asset class id contains non-printable character: %w", ErrInvalid,
				),
			)
		}
	}
	return nil
}

// ValidateNotes returns ErrInvalid when notes contain invalid UTF-8 or exceed
// 4096 code points.
func ValidateNotes(notes string) error {
	if !utf8.ValidString(notes) {
		return invalidField("/notes", "format", fmt.Errorf("notes contain invalid UTF-8: %w", ErrInvalid))
	}
	if utf8.RuneCountInString(notes) > 4096 {
		return invalidField("/notes", "max_length", fmt.Errorf("notes exceed 4096 code points: %w", ErrInvalid))
	}
	return nil
}

// maxReasonRunes is the length bound of a free-form reason and isReasonRune its
// per-rune rule. ValidateReason and NormalizeReason are both written in terms of
// them so the write-side normalizer cannot drift from the rule the read side
// enforces.
const maxReasonRunes = 4096

func isReasonRune(r rune) bool { return unicode.IsPrint(r) }

// ValidateReason returns ErrInvalid when an operator-supplied free-form reason
// contains invalid UTF-8, exceeds 4096 code points, or contains non-printable
// characters. A reason is persisted on the entity, rendered verbatim into the
// audit detail line, and crosses the FFI as a string view, so a control
// character - a newline above all - could forge an extra audit or log record.
// Empty reasons are allowed.
func ValidateReason(reason string) error {
	if !utf8.ValidString(reason) {
		return invalidField("/reason", "format", fmt.Errorf("reason contains invalid UTF-8: %w", ErrInvalid))
	}
	if utf8.RuneCountInString(reason) > maxReasonRunes {
		return invalidField(
			"/reason", "max_length",
			fmt.Errorf(
				"reason exceeds %d code points: %w", maxReasonRunes, ErrInvalid,
			),
		)
	}
	for _, r := range reason {
		if !isReasonRune(r) {
			return invalidField(
				"/reason", "format",
				fmt.Errorf(
					"reason contains non-printable character: %w", ErrInvalid,
				),
			)
		}
	}
	return nil
}

// NormalizeReason returns reason in the exact shape ValidateReason accepts:
// non-printable runes are dropped, then the result is cut to the same length
// bound on a code-point boundary.
//
// It exists for machine-produced reasons only - an engine block cause mirrored
// onto the account row. Such a reason is not operator input, so rejecting it
// would discard a real kill-switch record; but it lands in a row the restore
// path validates, and Officer's own rollback archive is replayed through that
// same path. A reason legal only on the write side would make the realm
// unrestorable and kill the node on its next rollback. Operator-supplied
// reasons keep failing loudly through ValidateReason instead.
func NormalizeReason(reason string) string {
	printable := strings.Map(func(r rune) rune {
		if !isReasonRune(r) {
			return -1
		}
		return r
	}, reason)
	runes := 0
	for i := range printable {
		if runes == maxReasonRunes {
			return printable[:i]
		}
		runes++
	}
	return printable
}

// ValidateBlockReason applies the shared reason rule to an operator-supplied
// block reason. The prefix keeps the operator-facing wording of the block paths.
func ValidateBlockReason(reason string) error {
	if err := ValidateBlockText(reason); err != nil {
		return fmt.Errorf("block %w", err)
	}
	return nil
}

// ValidateBlockText applies the shared bounded-printable reason rule to any
// free-form text carried by an account block cause.
func ValidateBlockText(text string) error {
	return ValidateReason(text)
}

// ValidateTitle returns ErrInvalid when a display title contains invalid UTF-8,
// exceeds 256 code points, or contains non-printable characters. Empty titles
// are allowed.
func ValidateTitle(title string) error {
	if !utf8.ValidString(title) {
		return invalidField("/title", "format", fmt.Errorf("title contains invalid UTF-8: %w", ErrInvalid))
	}
	if utf8.RuneCountInString(title) > 256 {
		return invalidField("/title", "max_length", fmt.Errorf("title exceeds 256 code points: %w", ErrInvalid))
	}
	for _, r := range title {
		if !unicode.IsPrint(r) {
			return invalidField(
				"/title", "format",
				fmt.Errorf(
					"title contains non-printable character: %w", ErrInvalid,
				),
			)
		}
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
	// RealizedPnl is the cumulative realized P&L for this (account, asset), as
	// last reported by the engine, denominated in AccountCurrency. Empty means
	// the engine has not established a meaningful P&L value.
	RealizedPnl string
	// RealizedPnlHaltReason explains why the engine stopped calculating this
	// position P&L. When set, RealizedPnl is historical rather than current.
	RealizedPnlHaltReason PnlHaltReason
	// AverageEntryPrice is optional; empty when not applicable. It is
	// denominated in AccountCurrency.
	AverageEntryPrice string
	// AccountCurrency is the account's effective currency: the denomination of
	// RealizedPnl and AverageEntryPrice. One (account, asset) slot merges fills
	// against every quote asset the asset trades against, so the quote asset of
	// any single fill does not denominate these values - the account currency
	// does. Empty when no tier of the account's currency cascade is set, which
	// leaves both values without a unit; that is also what the engine reports
	// as PnlHaltReasonMissingAccountCurrency, since it cannot calculate a P&L
	// it has no currency to express. This read-only projection is derived on
	// reads and is never persisted.
	AccountCurrency string `json:"-"`
	// Asset is the code of the asset, e.g. "AAPL".
	Asset string
	// Account is the code of the account that owns this balance.
	Account AccountID
}

// --- Market-data config -----------------------------------------------------

// Market-data provider types. Each value is the `type` discriminator stored
// on a MarketDataInstance and the switch key the connector manager constructs
// from.
const (
	// MarketDataProviderBYO is the bring-your-own provider: the customer pushes
	// their own quotes.
	MarketDataProviderBYO = "byo"
	// MarketDataProviderBinance is the public Binance spot feed.
	MarketDataProviderBinance = "binance"
	// MarketDataProviderIB is the Interactive Brokers TWS/Gateway feed.
	MarketDataProviderIB = "ib"
	// MarketDataProviderKraken is the public Kraken spot feed.
	MarketDataProviderKraken = "kraken"
	// MarketDataProviderCoinbase is the public Coinbase market-data feed.
	MarketDataProviderCoinbase = "coinbase"
	// MarketDataProviderAlpaca is the Alpaca IEX market-data feed.
	MarketDataProviderAlpaca = "alpaca"
	// MarketDataProviderOKX is the public OKX market-data feed.
	MarketDataProviderOKX = "okx"
	// MarketDataProviderBybit is the public Bybit market-data feed.
	MarketDataProviderBybit = "bybit"
	// MarketDataProviderOANDA is the OANDA pricing-stream feed.
	MarketDataProviderOANDA = "oanda"
	// MarketDataProviderFinnhub is the Finnhub market-data feed.
	MarketDataProviderFinnhub = "finnhub"
	// MarketDataProviderMock is the synthetic provider used for demos and tests.
	MarketDataProviderMock = "mock"
)

// MarketDataInstance is one configured market-data source. It is a machine
// record: its public handle is ExternalID, the surrogate key never appears here.
// Multiple instances of the same Provider may coexist (e.g. two BYO feeds), so
// the unique, case-insensitive Label distinguishes them for the operator.
// Credentials is an opaque JSON blob that is plaintext at the domain boundary
// and sealed at rest when the store is sealed; BYO and mock leave it empty.
type MarketDataInstance struct {
	// ExternalID is the opaque public handle of this instance.
	ExternalID ExternalID
	// Provider is the provider discriminator (e.g. MarketDataProviderBYO).
	Provider string
	// Label is a free-form, unique, case-insensitive human-readable name; may be
	// empty.
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
	// Instance is the owning instance's opaque public handle.
	Instance ExternalID
	// ExternalSymbol is the source-side symbol (e.g. "AAPL").
	ExternalSymbol string
	// BaseAsset is the code of the instrument underlying asset (e.g. "AAPL").
	BaseAsset string
	// QuoteAsset is the code of the instrument settlement asset (e.g. "USD").
	QuoteAsset string
	// BaseAssetID is the underlying asset row's stable internal identifier. It
	// is never serialized on the wire (json:"-").
	BaseAssetID EngineAssetID `json:"-"`
	// QuoteAssetID is the settlement asset row's stable internal identifier. It
	// is never serialized on the wire (json:"-").
	QuoteAssetID EngineAssetID `json:"-"`
	// ManualPrice is the operator-set mark price as an exact decimal string;
	// empty when none. It applies only to bring-your-own (manual) instruments:
	// the operator sets it once and the manager pushes it into the engine as a
	// single quote at startup and on upsert. Streaming providers ignore it -
	// their marks come from the source.
	ManualPrice string
	// Enabled reports whether this instrument is subscribed at runtime.
	Enabled bool
}

// MarketDataQuote is the latest normalized quote observed for one configured
// market-data instrument.
type MarketDataQuote struct {
	// AsOf is the source observation time.
	AsOf time.Time
	// ReceivedAt is when Officer received the quote.
	ReceivedAt time.Time
	// Instance is the owning instance's opaque public handle.
	Instance ExternalID
	// ExternalSymbol is the source-side symbol.
	ExternalSymbol string
	// BaseAsset is the code of the instrument underlying asset.
	BaseAsset string
	// QuoteAsset is the code of the instrument settlement asset.
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
	// RealizedPnl is an optional replacement for the persisted cumulative
	// realized P&L snapshot. Supplying it re-arms a previously halted position
	// P&L accumulator with an authoritative value.
	RealizedPnl string `json:"realized_pnl,omitempty"`
	// RealizedPnlHaltReason force-sets the position P&L accumulator to a halted
	// state. Snapshot import uses it to restore a persisted halt; it takes
	// precedence over RealizedPnl, whose retained value remains historical.
	RealizedPnlHaltReason PnlHaltReason `json:"realized_pnl_halt_reason,omitempty"`
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
	// RealizedPnlDelta is the signed realized-P&L change reported by the engine.
	RealizedPnlDelta string `json:"realized_pnl_delta"`
	// RealizedPnlResult is the engine-reported cumulative realized P&L after the
	// operation and is the authoritative value Officer persists when present.
	RealizedPnlResult string `json:"realized_pnl_result"`
	// RealizedPnlHaltReason is reported when the engine stopped calculating this
	// position P&L. An empty reason with a non-empty result clears a prior halt.
	RealizedPnlHaltReason PnlHaltReason `json:"realized_pnl_halt_reason,omitempty"`
	// AverageEntryPrice is the current average entry price reported by the engine
	// when the outcome carries one.
	AverageEntryPrice string `json:"average_entry_price,omitempty"`
}

// AdjustmentOutcomeRejected carries the structured rejection reason.
type AdjustmentOutcomeRejected struct {
	Code    string `json:"code"`
	Scope   string `json:"scope,omitempty"`
	Policy  string `json:"policy,omitempty"`
	Reason  string `json:"reason"`
	Details string `json:"details,omitempty"`
	// FailedAdjustmentIndex is the zero-based index, within the submitted batch,
	// of the adjustment the engine rejected on. A batch reject is atomic, so it
	// is the only request the reject can be attributed to.
	FailedAdjustmentIndex int `json:"failed_adjustment_index"`
}

// AccountAdjustmentRecord is the append-only history of a single spot-funds
// edit. It is a machine record: its public handle is ExternalID, the surrogate
// key never appears here. Payload and outcome are typed; the store marshals them
// to JSON. Account and Asset are dictionary references by code; Principal is an
// optional principal reference by code, cleared (not cascaded) when the
// principal is removed.
type AccountAdjustmentRecord struct {
	// At is the wall-clock time the adjustment was submitted.
	At time.Time
	// ExternalID is the opaque public handle of this adjustment.
	ExternalID ExternalID
	// Request is the adjustment payload submitted by the caller.
	Request AdjustmentRequest
	// Accepted is non-nil when the engine accepted the adjustment.
	Accepted *AdjustmentOutcomeAccepted
	// Rejected is non-nil when the engine rejected the adjustment.
	Rejected *AdjustmentOutcomeRejected
	// Principal is the code of the principal who initiated the action; empty when
	// the reference was cleared or none was recorded.
	Principal string
	// Asset is the code of the asset that was adjusted.
	Asset string
	// Account is the code of the account that was adjusted.
	Account AccountID
	// Source is the channel that originated the adjustment.
	Source Source
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

// OrderStatusSupported reports whether status is one of the recognized order
// lifecycle states, so a report or request naming an unknown status is rejected
// as invalid rather than persisted.
func OrderStatusSupported(status OrderStatus) bool {
	switch status {
	case OrderStatusSubmitted,
		OrderStatusAccepted,
		OrderStatusRejected,
		OrderStatusCommitted,
		OrderStatusRolledBack,
		OrderStatusFilled,
		OrderStatusPartiallyFilled,
		OrderStatusCancelled:
		return true
	default:
		return false
	}
}

// OrderStatusesEligibleForFill returns the statuses a fill may legally advance
// FROM.
//
// Eligible sources:
//   - submitted: immediate order just created, fill closes it in one step
//   - accepted: order accepted by the engine, awaiting venue fill
//   - committed: venue accepted the order - fills arrive after commit
//   - partially_filled: mid-fill, advancing to the next partial or final fill
func OrderStatusesEligibleForFill() []OrderStatus {
	return []OrderStatus{
		OrderStatusSubmitted,
		OrderStatusAccepted,
		OrderStatusCommitted,
		OrderStatusPartiallyFilled,
	}
}

// ExecutionReportStatusChangeEvent maps an execution-report target status to a
// lifecycle event. Fill statuses have no entry here: settlement emits a fill or
// a dedicated fee-only commission event instead. The bool is false for an
// unmapped status.
func ExecutionReportStatusChangeEvent(status OrderStatus) (OrderEventType, bool) {
	switch status {
	case OrderStatusSubmitted:
		return OrderEventSubmitted, true
	case OrderStatusAccepted:
		return OrderEventPreTradeAccepted, true
	case OrderStatusRejected:
		return OrderEventPreTradeRejected, true
	case OrderStatusCommitted:
		return OrderEventCommitted, true
	case OrderStatusRolledBack:
		return OrderEventRolledBack, true
	case OrderStatusCancelled:
		return OrderEventCancelled, true
	default:
		return "", false
	}
}

type validationError struct {
	message    string
	pointer    string
	constraint string
}

func (e validationError) Error() string { return e.message }

func (e validationError) Unwrap() error { return ErrInvalid }

// ValidationPointer reports the JSON pointer of the refused request member, or
// an empty string when the refusal belongs to the report as a whole. The rule
// and the member it blames are decided together here; a surface that renders
// the pointer must read it rather than infer one from the message text.
func (e validationError) ValidationPointer() string {
	return e.pointer
}

func (e validationError) ValidationConstraint() string {
	return e.constraint
}

// NewValidationError creates a structured validation error.
func NewValidationError(pointer, constraint, message string) error {
	return validationError{
		message: message, pointer: pointer, constraint: constraint,
	}
}

func invalidField(pointer, constraint string, cause error) error {
	return NewValidationError(pointer, constraint, cause.Error())
}

// WithValidationPointer returns err with its JSON pointer replaced.
func WithValidationPointer(pointer string, err error) error {
	var validation validationError
	if !errors.As(err, &validation) {
		return err
	}
	validation.message = err.Error()
	validation.pointer = pointer
	return validation
}

func invalidExecutionReport(message string) error {
	return NewValidationError("", "execution_report", message)
}

// ValidationPointer returns the refused member's JSON pointer carried by a
// validation error, or an empty string when no single member is named.
func ValidationPointer(err error) string {
	var pointed interface{ ValidationPointer() string }
	if errors.As(err, &pointed) {
		return pointed.ValidationPointer()
	}
	return ""
}

// ValidationConstraint returns the machine-readable validation reason, if one
// is available.
func ValidationConstraint(err error) string {
	var constrained interface{ ValidationConstraint() string }
	if errors.As(err, &constrained) {
		return constrained.ValidationConstraint()
	}
	return ""
}

// executionReportCarriesEnginePayload reports whether an execution report has
// a fill or commission owned by the engine. Terminal reports route by status
// even when they carry neither.
func executionReportCarriesEnginePayload(in ExecutionReportInput) bool {
	return in.FillQuantity != "" || in.Commission != nil
}

// ExecutionReportRequiresEngine validates an execution report's routing shape
// and reports whether it needs engine settlement. Every fill requires caller
// leaves and targets a fill status. Workflow and terminal no-fill reports may
// carry leaves as state only; commission does not change their leaves rules.
// Non-terminal workflow reports without engine-owned payload are recorded
// directly. A workflow status still cannot carry caller-supplied
// settlement-lock fields.
func ExecutionReportRequiresEngine(in ExecutionReportInput) (bool, error) {
	status := in.OrderStatus
	if status == "" {
		return false, NewValidationError(
			"/status", "required", "status is required",
		)
	}
	if !OrderStatusSupported(status) {
		return false, NewValidationError(
			"/status", "format", fmt.Sprintf("invalid status %q", status),
		)
	}
	hasQuantity := in.FillQuantity != ""
	hasPrice := in.FillPrice != ""
	if hasQuantity != hasPrice {
		return false, invalidExecutionReport(
			"quantity and price must be provided together",
		)
	}
	isFillStatus := status == OrderStatusFilled || status == OrderStatusPartiallyFilled
	if hasQuantity && !isFillStatus {
		return false, invalidExecutionReport(
			"quantity and price are only allowed for filled or partially_filled statuses",
		)
	}
	if !hasQuantity && isFillStatus {
		return false, NewValidationError(
			"/quantity", "required_for_status",
			"quantity and price are required for filled or partially_filled statuses",
		)
	}
	requiresEngine := OrderStatusTerminal(status) || executionReportCarriesEnginePayload(in)
	if in.LeavesQuantity != "" {
		if err := ValidateLeavesQuantity(in.LeavesQuantity); err != nil {
			return false, err
		}
	}
	if in.LockPrice != "" {
		if err := validateExecutionReportLockPrice(in.LockPrice); err != nil {
			return false, err
		}
	}
	if in.Commission != nil {
		hasAmount := in.Commission.Amount != ""
		hasCurrency := in.Commission.Currency != ""
		if hasAmount != hasCurrency {
			return false, NewValidationError(
				"/commission",
				"paired_fields",
				"commission amount and currency must be provided together",
			)
		}
		if !hasAmount {
			return false, NewValidationError(
				"/commission", "required",
				"commission amount and currency are required",
			)
		}
	}
	if hasQuantity && in.LeavesQuantity == "" {
		return false, NewValidationError(
			"/leavesQuantity", "required",
			"leavesQuantity is required for reports with a fill",
		)
	}
	_, isWorkflowStatus := ExecutionReportStatusChangeEvent(status)
	// A non-terminal workflow report cannot carry caller-supplied settlement
	// lock fields. Officer injects the stored order lock only after an engine
	// route.
	if isWorkflowStatus && !OrderStatusTerminal(status) &&
		(in.LockPrice != "" || len(in.Lock) > 0) {
		return false, invalidExecutionReport(
			"settlement lock fields are not allowed for workflow statuses",
		)
	}
	return requiresEngine, nil
}

// ValidateLeavesQuantity checks a caller-supplied leaves value. The error
// blames the caller: use ParseOpenQuantity for a value Officer stored itself,
// where the same syntax failure is an internal fault instead.
func ValidateLeavesQuantity(value string) error {
	if _, err := ParseOpenQuantity(value); err != nil {
		return NewValidationError(
			"/leavesQuantity", "format",
			fmt.Sprintf("invalid leavesQuantity: %v", err),
		)
	}
	return nil
}

func validateExecutionReportLockPrice(value string) error {
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "eE") {
		return NewValidationError(
			"/lockPrice", "format",
			fmt.Sprintf("invalid lockPrice: %q is not a plain decimal", value),
		)
	}
	if _, err := decimal.NewFromString(value); err != nil {
		return NewValidationError(
			"/lockPrice", "format",
			fmt.Sprintf("invalid lockPrice: %q: %v", value, err),
		)
	}
	return nil
}

// ParseOpenQuantity parses an open base quantity - caller-reported leaves or
// Officer's recorded value - as a non-negative plain decimal.
// Scientific notation and surrounding space are refused so the string that was
// stored and signed is the string the engine later receives. The error carries
// no caller-fault sentinel: each caller decides whether the value came from a
// request or from stored state.
func ParseOpenQuantity(value string) (decimal.Decimal, error) {
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "eE") {
		return decimal.Decimal{}, fmt.Errorf("%q is not a plain decimal", value)
	}
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%q: %w", value, err)
	}
	if parsed.IsNegative() {
		return decimal.Decimal{}, fmt.Errorf("%q must not be negative", value)
	}
	return parsed, nil
}

// OrderStatusTerminal reports whether the status is a final lifecycle state.
func OrderStatusTerminal(status OrderStatus) bool {
	switch status {
	case OrderStatusRejected,
		OrderStatusRolledBack,
		OrderStatusFilled,
		OrderStatusCancelled:
		return true
	default:
		return false
	}
}

// RequireOrderModifiable returns ErrTerminalOrder when the order's status is
// terminal and the caller did not set force.
func RequireOrderModifiable(status OrderStatus, force bool) error {
	if !force && OrderStatusTerminal(status) {
		return ErrTerminalOrder
	}
	return nil
}

// Order is the Officer-side record of an order that passed through the system,
// including rejected ones. It is a machine record: its public handle is
// ExternalID, the surrogate key never appears here. All monetary/size values are
// exact decimal strings, never float. Signed attestations, when present, live
// per-event in EventAttestation, not inline on the order.
type Order struct {
	// At is the wall-clock time the order was submitted.
	At time.Time
	// ExternalID is the opaque public handle of this order.
	ExternalID ExternalID
	// AmountValue is the size of the order (exact decimal string).
	AmountValue string
	// Leaves is the current recorded open base quantity. A submitted order records
	// the engine-reported base delta, zero included; every later execution report
	// writes its caller's leaves verbatim, at terminal status too, and a report
	// that supplies none leaves the recorded value untouched.
	Leaves string
	// ReservedQuantity is the remaining base quantity of this order's own
	// pre-trade reservation. It follows engine balance deltas independently of
	// caller-reported Leaves and is retained across restart and backup restore.
	ReservedQuantity string
	// Price is the limit price (exact decimal string); empty for market orders.
	Price string
	// Principal is the code of the principal who submitted the order; empty when
	// the reference was cleared or none was recorded.
	Principal string
	// BaseAsset is the code of the asset being bought or sold.
	BaseAsset string
	// QuoteAsset is the code of the asset used for pricing.
	QuoteAsset string
	// Lock is the opaque SDK-serialized reservation lock (pretrade.Lock), held as
	// raw bytes; nil when the order carried no lock. Display prices are derived
	// later from the deserialized lock via the SDK; this package never decodes it.
	Lock []byte
	// CommissionSubtotals aggregates execution-report commissions by their own
	// currencies. Empty when the order has no recorded commission.
	CommissionSubtotals []Commission
	// Account is the code of the account that placed the order.
	Account AccountID
	// Source is the channel that submitted the order.
	Source Source
	// DropCopy reports that the order was submitted through the drop-copy
	// operation. Policies still ran and mutated their normal state, but their
	// rejects and account or group blocks did not prevent the submission.
	DropCopy bool `json:"dropCopy,omitempty"`
	// Side is buy or sell.
	Side OrderSide
	// AmountKind distinguishes quantity-based from volume-based sizing.
	AmountKind OrderAmountKind
	// Status is the current lifecycle state.
	Status OrderStatus
}

// Commission is a fee or rebate amount paired with its currency.
type Commission struct {
	// Amount is the exact decimal commission amount. Positive values are fees;
	// negative values are rebates.
	Amount string `json:"amount"`
	// Currency is the commission currency code.
	Currency string `json:"currency"`
}

// AttestationRequestType names the trading request an attestation binds. Every
// attested request produces one order-history event; the attestation over the
// request and its recorded result is bound 1:1 to that event and tagged with
// the request that produced it.
type AttestationRequestType string

const (
	// AttestationRequestSubmit is a pre-trade submit (accept or reject verdict).
	AttestationRequestSubmit AttestationRequestType = "submit"
	// AttestationRequestExecutionReport is a post-trade fill/settlement report.
	AttestationRequestExecutionReport AttestationRequestType = "execution_report"
	// AttestationRequestConfirm is an explicit order confirmation.
	AttestationRequestConfirm AttestationRequestType = "confirm"
	// AttestationRequestCancel is an explicit order cancellation.
	AttestationRequestCancel AttestationRequestType = "cancel"
)

// EventAttestation is the persisted signed attestation over one trading request
// and its recorded result, bound 1:1 to the order-history event that records it.
// Token is the exact base64url envelope bytes issued by the signer;
// the remaining fields are the envelope metadata carried for read-back without
// decoding the token. KeyID references the signing key by its own UUID handle.
// IssuedAt is an RFC3339Nano UTC string.
type EventAttestation struct {
	// Token is the exact base64url-encoded signed attestation envelope.
	Token string
	// KeyID is the signing key UUID that produced Token.
	KeyID string
	// Alg is the envelope signing algorithm ("ed25519" | "none").
	Alg string
	// RequestType is the trading request the attestation binds.
	RequestType AttestationRequestType
	// Mode is the wire submit mode the envelope carries ("hold" | "immediate").
	Mode string
	// IssuedAt is the envelope issue time (RFC3339Nano UTC).
	IssuedAt string
}

// OrderEventType classifies a single event in an order's lifecycle.
type OrderEventType string

const (
	OrderEventSubmitted        OrderEventType = "submitted"
	OrderEventPreTradeAccepted OrderEventType = "pre_trade_accepted"
	OrderEventPreTradeRejected OrderEventType = "pre_trade_rejected"
	OrderEventCommitted        OrderEventType = "committed"
	OrderEventRolledBack       OrderEventType = "rolled_back"
	OrderEventConfirmed        OrderEventType = "confirmed"
	OrderEventFill             OrderEventType = "fill"
	OrderEventCommission       OrderEventType = "commission"
	OrderEventCancelled        OrderEventType = "cancelled"
)

// OrderEventPayload is the JSON-marshalled variant payload for an order event.
// Only the fields relevant to the event type are populated.
type OrderEventPayload struct {
	// Reject fields - populated for pre_trade_rejected events and for fill
	// events that record an account block caused by the execution report.
	RejectCode    string `json:"reject_code,omitempty"`
	RejectScope   string `json:"reject_scope,omitempty"`
	RejectPolicy  string `json:"reject_policy,omitempty"`
	RejectReason  string `json:"reject_reason,omitempty"`
	RejectDetails string `json:"reject_details,omitempty"`
	// Rejects preserves the complete ordered engine reject list. The singular
	// fields above mirror its first entry for legacy event readers.
	Rejects []OrderReject `json:"rejects,omitempty"`

	// Fill fields - populated for fill events.
	FillQuantity  string `json:"fill_quantity,omitempty"`
	FillPrice     string `json:"fill_price,omitempty"`
	FillLockPrice string `json:"fill_lock_price,omitempty"`

	// Execution-report fields - copied verbatim from the accepted report request
	// so the event preserves the report contract even when it carries no trade.
	LeavesQuantity string `json:"leaves_quantity,omitempty"`
	OrderStatus    string `json:"order_status,omitempty"`
	// Commission is the structured report commission, preserving its own
	// currency independently of whether the report carried a fill.
	Commission *Commission `json:"commission,omitempty"`

	// ExecutionReport is the complete client-visible input as received by the
	// node. It is repeated on every event emitted for the same report so each
	// event remains a self-contained immutable audit record.
	ExecutionReport *ExecutionReportRequest `json:"execution_report,omitempty"`
}

// OrderEvent is one immutable record in an order's event stream. It is a machine
// record: its public handle is ExternalID, the surrogate key never appears here.
// It links to its parent order by the order's opaque handle.
type OrderEvent struct {
	// At is the wall-clock time the event was recorded.
	At time.Time
	// ExternalID is the opaque public handle of this event.
	ExternalID ExternalID
	// Order is the parent order's opaque public handle.
	Order ExternalID
	// Attestation is the event's 1:1 signed attestation over the request and its
	// recorded result, read back on GetOrder; nil when the event carries none.
	Attestation *EventAttestation
	// Payload carries the event-specific structured data.
	Payload OrderEventPayload
	// Principal is the code of the principal who triggered the event; empty when
	// the reference was cleared or none was recorded.
	Principal string
	// Source is the channel that triggered the event.
	Source Source
	// Type classifies the event.
	Type OrderEventType
}

// Trade is the per-fill record backing the standalone trades list ("reports").
// One Trade is recorded for each fill event. It is a machine record: its public
// handle is ExternalID, the surrogate key never appears here. It links to its
// originating order by the order's opaque handle. All monetary values are exact
// decimal strings, never float.
type Trade struct {
	// At is the wall-clock time the fill was recorded.
	At time.Time
	// ExternalID is the opaque public handle of this trade.
	ExternalID ExternalID
	// Order is the originating order's opaque public handle.
	Order ExternalID
	// Quantity is the filled quantity (exact decimal string).
	Quantity string
	// Price is the fill price (exact decimal string).
	Price string
	// LockPrice is the reference price used for PnL locking; empty if none.
	LockPrice string
	// Commission is the per-fill commission amount/currency pair, preserving its
	// own currency independently from the fill's quote asset.
	Commission *Commission
	// Principal is the code of the principal who submitted the originating order;
	// empty when the reference was cleared or none was recorded.
	Principal string
	// BaseAsset is the code of the asset that was filled.
	BaseAsset string
	// QuoteAsset is the code of the asset used for pricing.
	QuoteAsset string
	// Account is the code of the account that received the fill.
	Account AccountID
	// Source is the channel that submitted the originating order.
	Source Source
	// Side is the direction of the fill.
	Side OrderSide
}

// OrderDetail bundles an order together with its events and trades; returned by
// GetOrder. Each event carries its own 1:1 signed attestation (OrderEvent.
// Attestation) when one was issued; the order is "signed" when at least one of
// its events is attested.
type OrderDetail struct {
	Order Order
	// DisplayPrice is a presentation-only value restored from the opaque engine
	// lock by the backend seam. It is never part of persisted or backup data.
	DisplayPrice string `json:"-"`
	Events       []OrderEvent
	Trades       []Trade
}

// Signed reports the order-level rollup: the order carries at least one
// Ed25519-signed event attestation. An eSign-off ("none") attestation does not
// count as signed.
func (d OrderDetail) Signed() bool {
	for i := range d.Events {
		att := d.Events[i].Attestation
		if att != nil && att.Alg == "ed25519" {
			return true
		}
	}
	return false
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

// ExecutionReportInput records either a workflow transition or a post-trade
// settlement. Reports carrying a fill, targeting a terminal status, or carrying
// a commission are settled by the engine; all other non-terminal reports record
// workflow state directly. It is spot-only this phase: instrument is
// (BaseAsset, QuoteAsset), and LockPrice is the single reference price captured
// at pre-trade time. All values are exact decimal strings; never float.
type ExecutionReportInput struct {
	// ExternalID is the optional caller-controlled public handle of this report.
	// The store generates one when it is absent.
	ExternalID ExternalID
	// BaseAsset is the instrument underlying asset that was filled.
	BaseAsset string
	// QuoteAsset is the instrument settlement asset.
	QuoteAsset string
	// FillQuantity is the filled quantity (exact decimal string).
	FillQuantity string
	// FillPrice is the fill price (exact decimal string).
	FillPrice string
	// LeavesQuantity is the caller-reported open base quantity. When present,
	// Officer writes it verbatim onto the order, into the report event, and into
	// the signed attestation. Every fill requires it. Workflow and terminal
	// no-fill reports may omit it, leaving the recorded order value unchanged.
	LeavesQuantity string
	// LockPrice is the reference price for the fill's PnL lock; empty when the
	// originating order carried no lock.
	LockPrice string
	// Lock is the opaque SDK-serialized pre-trade lock persisted on the order.
	// It is the only lock the engine adapter accepts; LockPrice is display data
	// and is never used to assemble a replacement.
	Lock []byte
	// Commission is the optional report commission. When set, both Amount and
	// Currency must be present and are forwarded to the SDK independently of the
	// report's optional fill.
	Commission *Commission
	// Order is the opaque public handle of the Officer order this fill settles.
	// The fill event and trade reference it, and the order's status is reflected
	// from the fill.
	Order ExternalID
	// Account is the code of the account that received the fill.
	Account AccountID
	// Side is the direction of the fill.
	Side OrderSide
	// OrderStatus is the Officer lifecycle status to persist.
	OrderStatus OrderStatus
	// Force bypasses Officer's finalized-order safety check; when omitted or
	// false, an execution report on a terminal order returns 409 terminal_order.
	Force bool
}

// ExecutionReportRequest is the immutable, audit-safe snapshot of the public
// values supplied to ApplyExecutionReport before Officer enriches or normalizes
// the input for engine settlement. Fields intentionally do not use omitempty:
// the event payload must preserve empty, false, and nil values as part of the
// received report contract. The opaque pre-trade lock is engine-internal and is
// deliberately excluded from events and signed attestations.
type ExecutionReportRequest struct {
	ExternalID     ExternalID  `json:"id"`
	BaseAsset      string      `json:"baseAsset"`
	QuoteAsset     string      `json:"quoteAsset"`
	FillQuantity   string      `json:"fillQuantity"`
	FillPrice      string      `json:"fillPrice"`
	LeavesQuantity string      `json:"leavesQuantity"`
	LockPrice      string      `json:"lockPrice"`
	Commission     *Commission `json:"commission"`
	Order          ExternalID  `json:"order"`
	Account        AccountID   `json:"account"`
	Side           OrderSide   `json:"side"`
	OrderStatus    OrderStatus `json:"orderStatus"`
	Force          bool        `json:"force"`
}

// ExecutionReportRequestFromInput takes an audit-safe snapshot so later
// enrichment of the engine input, or caller mutation of referenced data, cannot
// change the immutable event record. It never copies the opaque pre-trade lock.
func ExecutionReportRequestFromInput(in ExecutionReportInput) *ExecutionReportRequest {
	var commission *Commission
	if in.Commission != nil {
		copyCommission := *in.Commission
		commission = &copyCommission
	}
	return &ExecutionReportRequest{
		ExternalID:     in.ExternalID,
		BaseAsset:      in.BaseAsset,
		QuoteAsset:     in.QuoteAsset,
		FillQuantity:   in.FillQuantity,
		FillPrice:      in.FillPrice,
		LeavesQuantity: in.LeavesQuantity,
		LockPrice:      in.LockPrice,
		Commission:     commission,
		Order:          in.Order,
		Account:        in.Account,
		Side:           in.Side,
		OrderStatus:    in.OrderStatus,
		Force:          in.Force,
	}
}

// AccountBlock is one engine-recorded account block. The engine has already
// applied the block; the node mirrors it into the store. Account is carried by
// the Officer adapter because the binding's block record does not name it.
type AccountBlock struct {
	// Account is the account the engine blocked.
	Account AccountID
	// Policy is the engine policy that produced the block.
	Policy string
	// Code is the stable reject code that triggered the block.
	Code string
	// Reason is the human-readable block reason.
	Reason string
	// Details is the case-specific block detail.
	Details string
}

// ExecutionAccountBlock is an AccountBlock produced while processing an
// execution report. Kept as an alias to preserve the execution-facing API.
type ExecutionAccountBlock = AccountBlock

// BalanceSettlement is one per-asset balance outcome of a fill, expressed as a
// plain data carrier so the store can persist it without importing the engine.
// The settlement tx persists engine-returned absolute result fields when
// present; absent result fields leave the previous component unchanged.
type BalanceSettlement struct {
	// Asset identifies the asset whose balance the fill moves.
	Asset string
	// Outcome carries the per-asset deltas and results from the engine.
	Outcome AdjustmentOutcomeAccepted
}

// OrderSettlement is one atomic fill/settlement persisted in a single store
// transaction: per-asset balances, the order status, fill event(s), the optional
// trade, account blocks the engine already applied, and the optional lock-price
// rewrite are all committed together or not at all. It moves the node's former
// read-modify-write fill persistence (GetBalance + UpsertBalance + UpdateOrder
// Status + AppendOrderEvent + CreateTrade + SetAccountBlocked) inside one tx so a
// crash can never leave a half-settled order. It is a plain data carrier: no
// methods, no engine/store imports.
type OrderSettlement struct {
	// Trade is the optional trade row to create in the same tx; nil to skip.
	Trade *Trade
	// ReportID identifies the execution-report request that produced this
	// settlement. Nil means the settlement was not produced by an execution
	// report; a non-nil zero value asks the store to generate the ID.
	ReportID *ExternalID
	// Account is the code of the account the fill settled against.
	Account AccountID
	// OrderStatus is the target status (e.g. filled or partially_filled).
	OrderStatus OrderStatus
	// AccountPnl is the SpotFunds account-currency P&L snapshot to persist. An
	// empty value leaves it unchanged unless AccountPnlHaltReason is set.
	AccountPnl string
	// AccountPnlHaltReason is the reason the engine could not calculate account
	// P&L for this settlement. An empty reason with a non-empty AccountPnl clears
	// a prior halt.
	AccountPnlHaltReason PnlHaltReason
	// Lock is the opaque SDK-serialized reservation lock to write when SetLock is
	// true; an empty (non-nil) slice clears it, nil leaves it. It replaces the
	// former decimal-array lock: display prices are derived from the deserialized
	// SDK lock, not stored as an array.
	Lock []byte
	// Leaves is the exact open base quantity supplied with this settlement.
	// Empty leaves the stored value unchanged; terminal status does not alter it.
	Leaves string
	// ReservedQuantity is the reservation remainder after applying the engine
	// outcomes. Empty leaves it unchanged; an explicit zero records exhaustion.
	ReservedQuantity string
	// Order is the opaque public handle of the order being settled.
	Order ExternalID
	// AllowedFrom is an optional status WHERE-guard: when non-empty the order
	// status UPDATE only advances rows already in one of these statuses, and a
	// disallowed current status yields domain.ErrConflict with nothing written.
	// Empty means unguarded (fill paths that may run from submitted/accepted/
	// partially_filled).
	AllowedFrom []OrderStatus
	// Balances are the per-asset engine outcomes to persist.
	Balances []BalanceSettlement
	// Events are the lifecycle events to append (e.g. fill).
	Events []OrderEvent
	// Blocks are engine-already-applied account blocks to mirror in the same tx
	// (the block UPDATE only; the observational audit row stays outside).
	Blocks []ExecutionAccountBlock
	// SetLock distinguishes a nil Lock ("leave unchanged") from an empty slice
	// ("clear"); only when true is the lock column rewritten.
	SetLock bool
}

// --- Signing keys -----------------------------------------------------------

// SigningKey is the control-plane view of one Ed25519 signing keypair.
// PrivateKey is populated only in contexts that need signing (never in DTOs).
type SigningKey struct {
	// CreatedAt is the wall-clock time the key was generated or imported.
	CreatedAt time.Time
	// KeyID is the server-assigned UUID identifying the key.
	KeyID string
	// Alg is the signing algorithm; currently always "ed25519".
	Alg string
	// PublicKey is the raw 32-byte Ed25519 public key.
	PublicKey []byte
	// PrivateKey is the raw 32-byte Ed25519 private key seed; nil when omitted.
	PrivateKey []byte
	// Active reports whether this is the current signing key.
	Active bool
}

// AttestationResult is the recorded result section of an attestation payload.
// It is present for execution reports and recorded lifecycle events;
// nil for a plain submit accept whose verdict and estimate already carry the
// assessment. All fields are always emitted when the section is present (never
// omitempty) so the canonical bytes stay deterministic. All monetary and size
// values are exact decimal strings; never float.
type AttestationResult struct {
	// Outcome is the coarse recorded outcome for the request: "applied" for an
	// execution report, "confirmed" for a confirm, "cancelled" for a cancel.
	Outcome string `json:"outcome"`
	// FillQuantity is the settled fill quantity (execution report); empty
	// otherwise.
	FillQuantity string `json:"fillQuantity"`
	// FillPrice is the settled fill price (execution report); empty otherwise.
	FillPrice string `json:"fillPrice"`
	// FillLockPrice is the display lock price carried beside the fill (execution
	// report); empty otherwise.
	FillLockPrice string `json:"fillLockPrice"`
	// Commission is the structured report commission (amount + currency); nil
	// when absent. Emitted as null when absent so the canonical signed bytes stay
	// deterministic.
	Commission *Commission `json:"commission"`
	// LeavesQuantity is the open base quantity this request bound. An execution
	// report and the cancel shortcut bind the leaves their own request carried;
	// an immediate submit's settling event binds the leaves of the report the
	// engine adapter built for it. A submit or confirm lifecycle event binds the
	// order's own recorded open quantity instead, because it attests the order's
	// state and not a report. Empty only when the bound source had no value.
	LeavesQuantity string `json:"leavesQuantity"`
	// OrderStatus is the order's lifecycle status after the request.
	OrderStatus string `json:"orderStatus"`
	// Blocks are the account blocks the engine recorded for this request, in
	// order; empty when none.
	Blocks []AttestationBlock `json:"blocks"`
}

// AttestationBlock is one engine-recorded account block bound in an attestation
// result. It mirrors ExecutionAccountBlock in the signed form.
type AttestationBlock struct {
	Account string `json:"account"`
	Policy  string `json:"policy"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// ApprovalPayload is the canonical signed attestation payload embedded in a
// token. It binds a trading request (its material params) and its recorded
// result, keyed to the order-history event that recorded it, so a verifier can
// prove "for THIS request Officer recorded THIS result". Field
// declaration order is the canonical wire order (Go json.Marshal emits fields in
// declaration order). All price/quantity fields are decimal strings; never
// float. (The type name is retained to avoid a repo-wide rename of the signing
// machinery; it now models every request type, not just an approval verdict.)
type ApprovalPayload struct {
	Version    int    `json:"version"`    // =1
	ApprovalID string `json:"approvalId"` // server-generated request id
	// ApprovalRef links an acknowledgement or shortcut to the submit approval it
	// acts on. It is empty for the original submit and for independent reports.
	ApprovalRef string `json:"approvalRef,omitempty"`
	// RequestType is the trading request this payload attests: "submit" |
	// "execution_report" | "confirm" | "cancel". Every issued payload stamps it;
	// an empty value is an unsupported payload, not a default.
	RequestType string `json:"requestType,omitempty"`
	Mode        string `json:"mode"` // "hold" | "immediate"
	// OrderExternalID is the order's opaque public handle, never the internal
	// surrogate key: a monotonic surrogate in a client-facing token would leak
	// record counts/existence.
	OrderExternalID string `json:"orderId,omitempty"`
	// EventExternalID is the opaque public handle of the order-history event this
	// attestation is bound 1:1 to. Empty on a submit token issued before the
	// event id is known (kept omitempty so those tokens stay deterministic).
	EventExternalID string `json:"eventId,omitempty"`

	// Bound order params - connector re-binds against the order it executes.
	Instrument     string `json:"instrument"`
	Venue          string `json:"venue,omitempty"`
	Side           string `json:"side"`       // "buy" | "sell"
	Quantity       string `json:"quantity"`   // decimal string, never float
	AmountKind     string `json:"amountKind"` // "quantity" | "volume"
	OrderType      string `json:"orderType"`  // "limit" | "market"
	LimitPrice     string `json:"limitPrice"` // decimal string; "" for market
	PriceCurrency  string `json:"priceCurrency"`
	TimeInForce    string `json:"timeInForce"`
	AccountID      string `json:"accountId"`
	AccountGroupID string `json:"accountGroupId,omitempty"`

	// Verdict / estimate.
	Verdict       string `json:"verdict"` // "accept" | "reject"
	PolicySummary string `json:"policySummary"`
	EstimatePrice string `json:"estimatePrice"` // decimal string = engine lock price

	// Lifecycle / anti-replay.
	IssuedAt string `json:"issuedAt"` // RFC3339Nano UTC
	Nonce    string `json:"nonce"`    // single-use, 128-bit base64url

	// Key binding.
	KeyID string `json:"keyId"`
	Alg   string `json:"alg"` // "ed25519" | "none"

	// Reject verdict - appended with omitempty so an accept envelope
	// (Verdict="accept", these empty) stays byte-identical to the pre-reject
	// canonical wire shape. Populated only when Verdict="reject".
	RejectCode    string `json:"rejectCode,omitempty"`
	RejectScope   string `json:"rejectScope,omitempty"`
	RejectPolicy  string `json:"rejectPolicy,omitempty"`
	RejectReason  string `json:"rejectReason,omitempty"`
	RejectDetails string `json:"rejectDetails,omitempty"`

	// Caller who triggered the request; empty when none was recorded. Appended
	// with omitempty so a submit token with no principal stays deterministic.
	Principal string `json:"principal,omitempty"`

	// ExecutionReport is the audit-safe original report input for an execution
	// report attestation; nil for all other request types. The opaque pre-trade
	// lock is never included.
	ExecutionReport *ExecutionReportRequest `json:"executionReport,omitempty"`
	// Result is the recorded outcome for the request. Present (non-nil) for
	// execution reports and recorded order lifecycle events beyond the submit
	// verdict/estimate above; nil for a plain submit decision.
	Result *AttestationResult `json:"result,omitempty"`
	// Rejects is the complete ordered engine reject list for a rejected submit.
	// The flat Reject* fields above mirror its first entry for legacy readers.
	Rejects []OrderReject `json:"rejects,omitempty"`
}
