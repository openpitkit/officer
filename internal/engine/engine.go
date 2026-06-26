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
// and the OpenPit binding. The concrete adapter that satisfies Engine wraps a
// *openpit.Engine (go.openpit.dev/openpit); it owns the engine's lifecycle and
// translates between Pit Officer's domain types and the binding's param/model
// types.
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
	"time"

	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/marketdata"
)

// Snapshot is the engine state the control plane builds the engine from at
// process start: accounts (and their stored engine ids and blocked state), the
// account groups, the persisted balances, and the complete set of risk barriers
// across the three typed limit tables. BuildOpenPitEngine consumes it once. A
// Snapshot is plain data with no native handles, so the store layer can assemble
// it without cgo.
//
// The accounts and groups carry their stored engine ids (EngineAccountID /
// EngineGroupID), assigned collision-free by the connector. The engine adapter
// builds its code-to-engine-id resolver from them, so it never hashes a code
// into an engine id. The engine is rebuilt from a fresh full Snapshot whenever
// the account/group/limit set changes, so the resolver always covers every
// account and group the live handle runs.
type Snapshot struct {
	// Accounts are the accounts to apply, including their stored engine account
	// id, blocked state and block reason. Group membership is read from each
	// account's GroupCode.
	Accounts []domain.Account
	// RateLimits are the persisted rate-limit barriers.
	RateLimits []domain.LimitRate
	// OrderSizeLimits are the persisted order-size barriers.
	OrderSizeLimits []domain.LimitOrderSize
	// PnlBoundsLimits are the persisted P&L-bounds barriers.
	PnlBoundsLimits []domain.LimitPnlBounds
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
// method retune any of the three risk policies from its own typed table without
// a per-policy method on the interface.
type LimitSet struct {
	// RateLimits is the complete rate-limit barrier set (used for rate_limit).
	RateLimits []domain.LimitRate
	// OrderSizeLimits is the complete order-size barrier set (order_size_limit).
	OrderSizeLimits []domain.LimitOrderSize
	// PnlBoundsLimits is the complete P&L-bounds barrier set
	// (pnl_bounds_kill_switch).
	PnlBoundsLimits []domain.LimitPnlBounds
}

// barrierCount returns the number of barriers carried for policy. ConfigurePolicy
// uses it to detect the empty-set case (removing the last barrier of a policy),
// which the runtime Configure surface cannot express.
func (s LimitSet) barrierCount(policy string) int {
	switch policy {
	case domain.PolicyRateLimit:
		return len(s.RateLimits)
	case domain.PolicyOrderSizeLimit:
		return len(s.OrderSizeLimits)
	case domain.PolicyPnlBoundsKillSwitch:
		return len(s.PnlBoundsLimits)
	default:
		return 0
	}
}

// AdjustmentResult is the outcome of one ApplyAccountAdjustment call. Exactly
// one of Accepted/Rejected is non-nil, mirroring the binding's accept/reject
// split for a single-asset adjustment.
type AdjustmentResult struct {
	// Accepted carries the resulting per-field delta and absolute on accept.
	Accepted *domain.AdjustmentOutcomeAccepted
	// Rejected carries the first reject on reject.
	Rejected *domain.AdjustmentOutcomeRejected
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
	// Rejects are the engine pre-trade rejects; non-empty only when not accepted.
	Rejects []domain.OrderReject
	// Outcomes are the per-asset balance effects the reservation produced (held
	// funds and incoming quantity). They must be persisted so the balance
	// snapshot reflects the committed reservation before any fill arrives.
	Outcomes []BalanceOutcome
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

// HoldResult is the outcome of one ReserveHold call on accept. The native
// reservation stays held inside the engine adapter's registry, keyed by
// ApprovalID, until CommitHeld or RollbackHeld resolves it (or the TTL sweeper
// rolls it back). On reject ReserveHold returns Rejects with Accepted false and
// holds nothing.
type HoldResult struct {
	// ExpiresAt is when the TTL sweeper auto-rolls the hold back.
	ExpiresAt time.Time
	// ApprovalID is the server UUID identifying the held reservation; it is the
	// reservation id surfaced to clients. Empty when not accepted.
	ApprovalID string
	// Lock is the SDK-serialized reservation lock captured at issue, ready to
	// persist verbatim on the order and reservation intent. Nil when the order
	// locked nothing. Display prices are derived later via LockDisplayPrices.
	Lock []byte
	// SettlementLockPrice is the settlement-leg lock price (the price a fill
	// settles at) as a decimal string; empty when no price was locked.
	SettlementLockPrice string
	// EstimateSource is how the lock price was derived: domain.EstimateSourceLimit
	// when the order carried a limit price, else domain.EstimateSourceMarketMark.
	EstimateSource string
	// Outcomes are the per-asset balance effects produced by the reservation.
	// They must be persisted so a later engine rebuild can seed the held amounts.
	Outcomes []BalanceOutcome
	// Rejects are the engine pre-trade rejects; non-empty only when not accepted.
	Rejects []domain.OrderReject
	// Accepted reports whether the pre-trade passed and the hold was registered.
	Accepted bool
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

// ExecutionReportResult is the outcome of one ApplyExecutionReport call: the
// account blocks the engine recorded and any per-asset adjustment outcomes
// policies produced.
type ExecutionReportResult struct {
	// Blocks are the account blocks the engine recorded for this report.
	Blocks []domain.ExecutionAccountBlock
	// Outcomes are the per-asset adjustment outcomes policies produced, each
	// tagged with its asset (both the base and the quote leg of a spot fill
	// settle).
	Outcomes []BalanceOutcome
}

// BuildFunc builds an engine from a seed snapshot. The local node calls it at
// construction and for administrative rebuilds. Production wires it to
// BuildOpenPitEngine bound to a runtime-library path. Tests substitute a fake.
type BuildFunc func(snap Snapshot) (Engine, error)

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

// Engine is the control plane's view of one OpenPit engine instance. The
// concrete implementation wraps *openpit.Engine.
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
	// LimitSet slice matching policy is consumed.
	ConfigurePolicy(ctx context.Context, policy string, limits LimitSet) error

	// BlockAccount kill-switches the account in the engine, gating its pre-trade
	// orders until it is unblocked. reason is recorded with the block.
	BlockAccount(ctx context.Context, id domain.AccountID, reason string) error

	// UnblockAccount lifts the engine block on the account. Unblocking an
	// account that is not blocked is a no-op.
	UnblockAccount(ctx context.Context, id domain.AccountID) error

	// ApplyAccountAdjustmentBatch applies a batch of spot-funds adjustments for
	// account on the live engine. The SDK batch is atomic for the account: on a
	// batch reject no per-request outcomes are committed.
	ApplyAccountAdjustmentBatch(
		ctx context.Context, account domain.AccountID, reqs []domain.AdjustmentRequest,
	) ([]AdjustmentResult, *AdjustmentBatchReject, error)

	// ApplyAccountAdjustment applies one spot-funds adjustment for account on the
	// live engine and returns its accept/reject outcome. It delegates to
	// ApplyAccountAdjustmentBatch with a one-request batch.
	ApplyAccountAdjustment(
		ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
	) (AdjustmentResult, error)

	// SubmitOrder runs the pre-trade pipeline for o on the live engine. On accept
	// it serializes the reservation lock, commits the reservation, and returns
	// Accepted with the serialized lock; on reject it returns the engine rejects.
	// The native reservation never escapes the adapter.
	SubmitOrder(ctx context.Context, o domain.Order) (OrderResult, error)

	// ReserveHold runs the pre-trade pipeline for o on the live engine and, on
	// accept, keeps the reservation held: the held amount stays reserved on
	// engine storage until CommitHeld or RollbackHeld resolves it, or the TTL
	// sweeper auto-rolls it back. It registers the held reservation under a fresh
	// approval id and persists a reservation intent, then returns that id, the
	// serialized lock, the settlement-leg price and estimate source, and the
	// expiry. On reject it holds nothing and returns the engine rejects. A held
	// reservation holds no engine/storage lock between calls, so a multi-minute
	// TTL blocks nothing.
	ReserveHold(ctx context.Context, o domain.Order) (HoldResult, error)

	// CommitHeld commits the held reservation identified by approvalID, realizing
	// its reservation permanently. It does the native commit and the in-memory
	// single-resolve guard only; durable persistence (intent flip + order status +
	// event) is the node's atomic ResolveOrderReservation. The Held->Resolving flip
	// happens before the native commit, so a concurrent or repeated resolve cannot
	// trigger the binding's double-commit panic: a second CommitHeld on an
	// already-resolved id returns domain.ErrConflict. An unknown id returns
	// domain.ErrNotFound.
	CommitHeld(ctx context.Context, approvalID string) error

	// RollbackHeld rolls back the held reservation identified by approvalID,
	// returning the held amount to available. Like CommitHeld it does the native
	// rollback and the in-memory guard only; the node persists the resolution
	// atomically via ResolveOrderReservation. (The TTL sweeper, by contrast, owns
	// its swept rollback end to end and persists through the engine's own store
	// handle.) It is tolerant of an already-resolved id (idempotent no-op for a
	// terminal entry). An unknown id returns domain.ErrNotFound.
	RollbackHeld(ctx context.Context, approvalID string) error

	// SubmitImmediate runs the pre-trade pipeline for o and, on accept, commits
	// the reservation and settles a fill in the same call via a synthetic
	// ApplyExecutionReport at the captured settlement lock price, so the held
	// amount nets to zero. It returns the lock prices, settlement price, and
	// estimate source; on reject it returns the engine rejects and settles
	// nothing.
	SubmitImmediate(ctx context.Context, o domain.Order) (ImmediateResult, error)

	// SetReservationStore attaches the persistence layer the adapter uses to keep
	// reservation intents durable across the hold lifecycle. It is called once at
	// boot, before the first ReserveHold and before ReconcileOrphans. A nil store
	// keeps holds in memory only.
	SetReservationStore(store ReservationStore)

	// ReconcileOrphans reports every persisted reservation intent still in the
	// held state. Native reservation handles do not survive a restart, but held
	// balance effects reseed from the store and resolution can fall back to the
	// persisted intent. It returns the number found.
	ReconcileOrphans(ctx context.Context) (int, error)

	// ApplyExecutionReport settles a fill on the live engine and returns the
	// account blocks the engine recorded plus any adjustment outcomes.
	ApplyExecutionReport(
		ctx context.Context, in domain.ExecutionReportInput,
	) (ExecutionReportResult, error)

	// RegisterGroup atomically registers accounts into the group identified by
	// groupID on the live engine. An account belongs to exactly one non-default
	// group.
	RegisterGroup(ctx context.Context, accounts []domain.AccountID, groupID string) error

	// UnregisterGroup atomically removes accounts from the group identified by
	// groupID on the live engine.
	UnregisterGroup(ctx context.Context, accounts []domain.AccountID, groupID string) error

	// BlockGroup kill-switches every account in groupID on the live engine,
	// recording reason. Re-blocking refreshes the reason.
	BlockGroup(ctx context.Context, groupID, reason string) error

	// UnblockGroup lifts the engine block on groupID. Unblocking an unblocked
	// group is a no-op.
	UnblockGroup(ctx context.Context, groupID string) error

	// CheckOrder runs the pre-trade pipeline for probe as a non-mutating
	// dry-run on the live engine. It returns whether the order would pass plus
	// the reasons: on pass the would-be reservation lock prices, on reject the
	// engine rejects and the account block the engine would record. It commits
	// nothing - no reservation is taken and no account state changes - so it is
	// idempotent and safe to call repeatedly.
	CheckOrder(ctx context.Context, probe domain.OrderProbe) (domain.CheckResult, error)

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
