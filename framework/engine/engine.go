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

// Package engine defines the adapter seam between the Pit Officer control plane
// and an engine implementation. A concrete adapter owns the engine lifecycle and
// translates between Pit Officer's domain types and its native model types.
//
// Pit Officer adds no risk logic in front of the engine: the adapter builds the
// one engine at process start, configures policies, blocks accounts, observes,
// and stops the engine. All policy evaluation stays inside the engine.
//
// Officer keeps one engine handle and applies policy-limit edits dynamically
// through the binding's runtime Configure surface when the SDK supports the
// change. Full backup restore, database reset, and policy changes the runtime
// Configure surface cannot express rebuild a replacement engine from the
// persisted store snapshot before making it live.
package engine

import (
	"context"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

// Snapshot is the engine state the control plane builds the engine from at
// process start: accounts (and their stored engine ids and blocked state), the
// account groups, the persisted balances, and the complete set of risk barriers
// across the typed limit tables. BuildOpenPitEngine consumes it once. A
// Snapshot is plain data with no native handles, so the store layer can assemble
// it without cgo.
//
// The accounts and groups carry their stored engine ids (EngineAccountID /
// EngineGroupID), assigned collision-free by the connector. The engine adapter
// builds its code-to-engine-id resolver from them, so it never hashes a code
// into an engine id. Persisted dictionary additions and alias renames can then
// be published through DictionaryResolver without replacing the engine.
type Snapshot struct {
	// Accounts are the accounts to apply, including their stored engine account
	// id, blocked state and block reason. Group membership is read from each
	// account's GroupCode.
	Accounts []domain.Account
	// RateLimits are the persisted rate-limit barriers.
	RateLimits []domain.LimitRate
	// OrderSizeLimits are the persisted order-size barriers.
	OrderSizeLimits []domain.LimitOrderSize
	// SpotFundsPnlBoundsLimits are the persisted SpotFunds self-computed
	// P&L-bounds barriers.
	SpotFundsPnlBoundsLimits []domain.LimitSpotFundsPnlBounds
	// Groups are the persisted account groups, carrying their stored engine group
	// id, blocked state and reason. Membership lives on Accounts; this seeds group
	// blocks only.
	Groups []domain.AccountGroup
	// Balances are the persisted per-(account, asset) holdings to seed into the
	// spot-funds policy as absolute adjustments.
	Balances []domain.Balance
}

// LimitSet is the complete typed barrier set for one policy, the carrier
// ConfigurePolicy applies on the live handle. Only the slice matching the named
// policy is consumed; the others are ignored. It lets a single ConfigurePolicy
// method retune any risk policy from its own typed table without
// a per-policy method on the interface.
type LimitSet struct {
	// RateLimits is the complete rate-limit barrier set (used for rate_limit).
	RateLimits []domain.LimitRate
	// OrderSizeLimits is the complete order-size barrier set (order_size_limit).
	OrderSizeLimits []domain.LimitOrderSize
	// SpotFundsPnlBoundsLimits is the complete SpotFunds self-computed
	// P&L-bounds barrier set (spot_funds_pnl_bounds_kill_switch).
	SpotFundsPnlBoundsLimits []domain.LimitSpotFundsPnlBounds
}

// PolicyConfigurationResult is the accepted outcome of a live policy update.
// AccountBlocks must be mirrored durably before the node admits subsequent
// account work.
type PolicyConfigurationResult struct {
	AccountBlocks []domain.AccountBlock
}

// AdjustmentResult is the outcome of one ApplyAccountAdjustment call. Exactly
// one of Accepted/Rejected is non-nil, mirroring the binding's accept/reject
// split for a single-asset adjustment.
type AdjustmentResult struct {
	// Accepted carries the resulting per-field delta and absolute on accept.
	Accepted *domain.AdjustmentOutcomeAccepted
	// Rejected carries the first reject on reject.
	Rejected *domain.AdjustmentOutcomeRejected
	// AccountBlocks are the blocks the engine latched while committing the
	// adjustment batch this result belongs to (an out-of-bounds force-set trips
	// the account's kill-switch). The engine has already applied them and the
	// caller must mirror them durably in the same operation - deferring to a
	// later fill or post-trade event is not allowed. They are batch-level, so
	// every result of one batch repeats the same blocks; mirror them keyed by
	// account.
	AccountBlocks []domain.AccountBlock
}

// AdjustmentBatchReject is an atomic account-adjustment batch reject from the
// engine. No per-request outcome is committed when this is returned.
type AdjustmentBatchReject = domain.AdjustmentOutcomeRejected

// OrderResult is the outcome of one SubmitOrder pre-trade call. On accept the
// reservation is committed inside the adapter and Lock holds the SDK-serialized
// reservation lock captured before commit; on reject Rejects holds the engine
// rejects.
type OrderResult struct {
	// Lock is the SDK-serialized reservation lock captured before the reservation
	// was committed, ready to persist verbatim on the order. Nil when the order
	// locked nothing. Display prices are derived later via LockDisplayPrices.
	Lock []byte
	// Blocks are account blocks the engine recorded while creating the
	// reservation. They are non-empty only for a non-enforcing drop-copy path and
	// must be mirrored durably with the accepted order.
	Blocks []domain.ExecutionAccountBlock
	// Rejects are the engine pre-trade rejects; non-empty only when not accepted.
	Rejects []domain.OrderReject
	// Outcomes are the per-asset balance effects the reservation produced (held
	// funds and incoming quantity). They must be persisted so the balance
	// snapshot reflects the committed reservation before any fill arrives.
	Outcomes []BalanceOutcome
	// SettlementLockPrice is the settlement-leg lock price (the price a later
	// fill settles at) as a decimal string; empty when no price was locked.
	SettlementLockPrice string
	// LeavesQuantity is the order's canonical open base quantity. Quantity orders
	// carry their amount directly; volume orders are converted at the settlement
	// lock price.
	LeavesQuantity string
	// EstimateSource is how the lock price was derived: domain.EstimateSourceLimit
	// when the order carried a limit price, else domain.EstimateSourceMarketMark.
	EstimateSource string
	// Accepted reports whether the pre-trade passed and was committed.
	Accepted bool
}

// BalanceOutcome is one per-asset account-adjustment outcome produced by the
// engine. The asset is carried next to the outcome because the accepted payload
// intentionally contains only values and deltas.
//
// Invariant: the engine emits at most one BalanceOutcome per asset. Downstream
// persistence keys balance updates by asset on that basis (the node maps these
// onto domain.BalanceSettlement for the atomic RecordOrderSettlement tx), so
// duplicate-asset outcomes would double-count realized P&L. Aggregating multiple
// per-asset outcomes is a deferred outcome-model rework.
type BalanceOutcome struct {
	Asset   string                           `json:"asset"`
	Outcome domain.AdjustmentOutcomeAccepted `json:"outcome"`
}

// ImmediateResult is the outcome of one SubmitImmediate call. On accept the
// reservation is committed and settled in the same call via a synthetic
// ApplyExecutionReport at the captured lock price, so the held amount nets to
// zero; on reject Rejects holds the engine rejects and nothing settles.
type ImmediateResult struct {
	// Lock is the SDK-serialized reservation lock captured before commit, ready
	// to persist verbatim on the order. Nil when the order locked nothing. Display
	// prices are derived later via LockDisplayPrices.
	Lock []byte
	// Blocks are the account blocks the engine recorded while settling the
	// immediate fill.
	Blocks []domain.ExecutionAccountBlock
	// Outcomes are the per-asset adjustment outcomes produced by the immediate
	// fill settlement, each tagged with its asset (both the base and the quote
	// leg of a spot fill settle).
	Outcomes []BalanceOutcome
	// AccountPnl is the authoritative SpotFunds account-currency P&L snapshot
	// produced by the immediate fill settlement. Empty means no amount outcome.
	AccountPnl string
	// AccountPnlHaltReason is the authoritative reason SpotFunds could not
	// calculate account P&L for the immediate fill settlement.
	AccountPnlHaltReason domain.PnlHaltReason
	// SettlementLockPrice is the settlement-leg lock price the fill settled at as
	// a decimal string; empty when no price was locked.
	SettlementLockPrice string
	// FillQuantity is the base quantity settled by the immediate fill.
	FillQuantity string
	// EstimateSource is how the lock price was derived: domain.EstimateSourceLimit
	// when the order carried a limit price, else domain.EstimateSourceMarketMark.
	EstimateSource string
	// Rejects are the engine pre-trade rejects; non-empty only when not accepted.
	Rejects []domain.OrderReject
	// Accepted reports whether the pre-trade passed and the fill settled.
	Accepted bool
}

// ExecutionReportPersistence is the Officer write set produced after the engine
// applies an execution report. Report-owned order, commission, trade, and event
// fields are copied from the original report, except terminal leaves are
// normalized after the engine consumes the release quantity; engine-owned
// account effects are limited to Balances and Blocks.
type ExecutionReportPersistence struct {
	// Trade is the optional trade row to persist.
	Trade *domain.Trade
	// Commission is the report-level fee or rebate. It is retained even when the
	// report carries no trade.
	Commission *domain.Commission
	// OrderStatus is the order status change the engine-facing layer accepted.
	OrderStatus domain.OrderStatus
	// AccountPnl is the SpotFunds account-currency P&L snapshot. Empty means
	// that the execution report carried no P&L amount.
	AccountPnl string
	// AccountPnlHaltReason is the engine-reported reason account P&L was not
	// calculated. Empty with a non-empty AccountPnl clears a prior halt.
	AccountPnlHaltReason domain.PnlHaltReason
	// Leaves is the remaining open quantity to persist; terminal settlements use
	// zero after the engine consumes the report's release quantity. Empty leaves
	// the stored value unchanged.
	Leaves string
	// Balances are the per-asset balance outcomes returned by the engine.
	Balances []domain.BalanceSettlement
	// Events are the order lifecycle events to append.
	Events []domain.OrderEvent
	// Blocks are account blocks returned by the engine.
	Blocks []domain.ExecutionAccountBlock
}

// ExecutionReportResult is the outcome of one ApplyExecutionReport call: the
// account blocks the engine recorded, any per-asset adjustment outcomes policies
// produced, and the explicit persistence write set the node must apply.
type ExecutionReportResult struct {
	// Persistence is the explicit write set returned by the engine-facing layer.
	// Nil means the report produced no Officer-side writes.
	Persistence *ExecutionReportPersistence
	// ReportID is the DB-authoritative handle of the persisted report request.
	// The node fills it after the settlement transaction commits.
	ReportID domain.ExternalID
	// Blocks are the account blocks the engine recorded for this report.
	Blocks []domain.ExecutionAccountBlock
	// Outcomes are the per-asset adjustment outcomes policies produced, each
	// tagged with its asset (both the base and the quote leg of a spot fill
	// settle).
	Outcomes []BalanceOutcome
}

// AccountLane is the engine view captured for one account-synchronized lane
// callback. Methods on this handle run directly on the already-routed engine
// lane; callers must not call Engine.RunAccountSynchronized from inside them.
type AccountLane interface {
	BlockAccount(ctx context.Context, id domain.AccountID, reason string) error
	UnblockAccount(ctx context.Context, id domain.AccountID) error
	SetAccountCurrency(ctx context.Context, id domain.AccountID, currency string) error
	ClearAccountCurrency(ctx context.Context, id domain.AccountID) error
	// SetAccountPnl force-sets the live SpotFunds account-currency P&L
	// accumulator to pnl, an absolute decimal-string assignment that also clears
	// any halt latched on it. The returned blocks are the account blocks the
	// engine latched while applying the assignment: a seeded value can breach its
	// own kill-switch barrier, so the caller must mirror them rather than assume
	// an accepted assignment leaves the account tradable.
	SetAccountPnl(
		ctx context.Context, id domain.AccountID, pnl string,
	) ([]domain.AccountBlock, error)
	// SetAccountPnlState force-sets either a numeric account P&L or a halted
	// state. Exactly one of pnl and haltReason must be non-empty.
	SetAccountPnlState(
		ctx context.Context,
		id domain.AccountID,
		pnl string,
		haltReason domain.PnlHaltReason,
	) ([]domain.AccountBlock, error)
	ApplyAccountAdjustmentBatch(
		ctx context.Context, account domain.AccountID, reqs []domain.AdjustmentRequest,
	) ([]AdjustmentResult, *AdjustmentBatchReject, error)
	ApplyAccountAdjustment(
		ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
	) (AdjustmentResult, error)
	SubmitOrder(ctx context.Context, o domain.Order) (OrderResult, error)
	SubmitImmediate(ctx context.Context, o domain.Order) (ImmediateResult, error)
	ApplyExecutionReport(
		ctx context.Context, in domain.ExecutionReportInput,
	) (ExecutionReportResult, error)
	CheckOrder(ctx context.Context, probe domain.OrderProbe) (domain.CheckResult, error)
}

// GroupLane is the engine view captured for one group-synchronized lane
// callback.
type GroupLane interface {
	BlockGroup(ctx context.Context, groupID, reason string) error
	UnblockGroup(ctx context.Context, groupID string) error
	SetGroupCurrency(ctx context.Context, groupID, currency string) error
	ClearGroupCurrency(ctx context.Context, groupID string) error
	RegisterGroup(ctx context.Context, accounts []domain.AccountID, groupID string) error
	UnregisterGroup(ctx context.Context, accounts []domain.AccountID, groupID string) error
}

// BuildFunc builds an engine from a seed snapshot. The local node calls it at
// construction and for administrative rebuilds. Production wires it to the app
// engine adapter bound to a runtime-library path. Tests substitute a fake.
type BuildFunc func(snap Snapshot) (Engine, error)

// DictionaryResolver is the optional live dictionary capability of an Engine
// adapter. Definitions passed here must already be persisted and carry their
// stable numeric engine ids. Each successful call publishes the complete
// resolver change before it returns; failures leave every alias unchanged.
//
// These methods update Officer's alias resolver only. They do not model an SDK
// account registry and must not replace the engine or its market-data sink.
type DictionaryResolver interface {
	AddAccountResolverEntry(account domain.Account) error
	RenameAccountResolverEntry(oldCode domain.AccountID, account domain.Account) error
	AddGroupResolverEntry(group domain.AccountGroup) error
	RenameGroupResolverEntry(oldCode string, group domain.AccountGroup) error
	RemoveGroupResolverEntry(group domain.AccountGroup) error
}

// Health reports the observable condition of an engine adapter for the
// dashboard and health checks.
type Health struct {
	// Version is the engine SDK/runtime version, as reported by Engine.Version.
	Version string
	// BuildProfile is the engine build profile (for example "release"), as
	// reported by Engine.BuildProfile.
	BuildProfile string
	// Running reports whether the underlying engine handle is live (built and
	// not yet stopped).
	Running bool
}

// Engine is the control plane's view of one engine instance.
//
// Implementations are not required to be safe for concurrent use unless the
// underlying engine was built with a concurrent sync policy; callers must honor
// the binding's threading contract.
type Engine interface {
	// Version returns the engine SDK/runtime version string.
	Version() string

	// BuildProfile returns the engine build profile string.
	BuildProfile() string

	// Running reports whether the engine handle is live (built and not
	// stopped).
	Running() bool

	// ConfigurePolicy reconfigures one policy from its complete typed barrier set
	// on the live engine handle via the binding's Configure surface. Only the
	// LimitSet slice matching policy is consumed. The returned blocks have
	// already been applied by the engine and must be persisted by the caller.
	ConfigurePolicy(
		ctx context.Context, policy string, limits LimitSet,
	) (PolicyConfigurationResult, error)

	// RunAccountSynchronized runs fn on the engine's account-synchronized lane.
	// Account-scoped node operations use this to keep Officer's own checks,
	// engine calls, and persistence ordered with every engine call for account.
	RunAccountSynchronized(
		ctx context.Context, account domain.AccountID, fn func(AccountLane) error,
	) error

	// RunGroupSynchronized runs fn on the engine's group-synchronized lane.
	RunGroupSynchronized(
		ctx context.Context, groupID string, fn func(GroupLane) error,
	) error

	// MarketDataSink returns the quote sink backed by the engine's market-data
	// service. The connector manager drains normalized quotes into it; the sink
	// registers instruments on first sight and pushes quotes through the binding.
	// It is valid until Stop closes this engine handle; backup restore swaps the
	// engine and reconnects feeds to the replacement handle's sink.
	MarketDataSink() marketdata.Sink

	// Stop halts the engine and releases the underlying native resources,
	// including the owned market-data service (closed after the engine stops).
	// After Stop the engine is no longer usable. Stop is idempotent.
	Stop()
}
