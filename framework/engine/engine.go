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

	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

// Snapshot is the engine state the control plane builds the engine from at
// process start: accounts (and their stored engine ids and blocked state), the
// account groups, the persisted balances, and the complete set of risk barriers
// across the typed limit tables. The build function returned by
// NewOpenPitEngineBuildFunc consumes it for each engine build. A Snapshot is
// plain data with no native handles, so the store layer can assemble it without
// cgo.
//
// The accounts and groups carry their stored engine ids (EngineAccountID /
// EngineGroupID), assigned collision-free by the connector. The engine adapter
// builds its code-to-engine-id resolver from them, so it never hashes a code
// into an engine id. Persisted dictionary additions and alias renames can then
// be published through DictionaryResolver without replacing the engine.
type Snapshot struct {
	// Assets are the persisted assets, carrying their stored engine asset ids so
	// the adapter can build a code-to-id resolver from them.
	Assets []domain.Asset
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

// OrderResult is the outcome of one SubmitOrder pre-trade call. On accept the
// reservation is committed inside the adapter and Lock holds the SDK-serialized
// reservation lock captured before commit; on reject Rejects holds the engine
// rejects.
type OrderResult struct {
	// Lock is the non-empty SDK-serialized reservation lock captured before the
	// reservation was committed, ready to persist verbatim on an accepted order.
	// Display prices are derived later via LockSettlementPrice.
	Lock []byte
	// Blocks are account blocks the engine recorded while creating the
	// reservation. They are non-empty only for a non-enforcing drop-copy path and
	// must be mirrored durably with the accepted order.
	Blocks []domain.AccountBlock
	// Rejects are the engine pre-trade rejects; non-empty only when not accepted.
	Rejects []domain.OrderReject
	// Outcomes are the per-asset balance effects the reservation produced (held
	// funds and incoming quantity). They must be persisted so the balance
	// snapshot reflects the committed reservation before any fill arrives.
	Outcomes []BalanceOutcome
	// SettlementLockPrice is the display settlement leg decoded from the engine
	// lock as a decimal string; empty when no price was locked.
	SettlementLockPrice string
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
// reservation is committed and settled in the same call via an execution
// report carrying the request quantity, request limit price (or the lock price
// for a market order), and the original engine lock. On reject Rejects holds
// the engine rejects and nothing settles.
type ImmediateResult struct {
	// Persistence is the authoritative execution-report write set produced by
	// the same adapter path as a caller-driven execution report.
	Persistence *ExecutionReportPersistence
	// ExecutionReport is the audit-safe immutable report request accepted by the
	// engine. Its external ID is assigned by the store with the settlement.
	ExecutionReport *domain.ExecutionReportRequest
	// Lock is the non-empty SDK-serialized reservation lock captured before
	// commit, ready to persist verbatim on an accepted order. Display prices are
	// derived later via LockSettlementPrice.
	Lock []byte
	// Blocks are the account blocks the engine recorded while settling the
	// immediate fill.
	Blocks []domain.AccountBlock
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
	// SettlementLockPrice is the display settlement leg decoded from the engine
	// lock as a decimal string; empty when no price was locked.
	SettlementLockPrice string
	// FillQuantity is the request's base quantity settled by the immediate fill.
	FillQuantity string
	// TradePrice is the fill price recorded for the immediate order. It is the
	// request limit price, or the engine lock price for a market order.
	TradePrice string
	// Rejects are the engine pre-trade rejects; non-empty only when not accepted.
	Rejects []domain.OrderReject
	// Accepted reports whether the pre-trade passed and the fill settled.
	Accepted bool
}

// ImmediatePreparation is the caller-owned state derived from a pending
// reservation or drop-copy operation before it is committed. ExecutionReport
// must be applied only after the chain commits that pending operation.
type ImmediatePreparation struct {
	// ExecutionReport is the SDK report derived while the pending operation can
	// still be rolled back.
	ExecutionReport model.ExecutionReport
	// ReportInput is the immutable Officer report payload represented by
	// ExecutionReport.
	ReportInput domain.ExecutionReportInput
	// Outcomes are the pre-trade balance effects to merge with post-trade
	// outcomes after the report settles.
	Outcomes []BalanceOutcome
	// Blocks are drop-copy account blocks captured before commit.
	Blocks []domain.AccountBlock
	// ReconciliationState describes the engine mutation finalized before the
	// execution report is applied.
	ReconciliationState string
}

// OrderChainAdapter maps Officer order values to the SDK chain surface and
// materializes capability-limited hook results. The node owns chain ordering
// and persistence; the adapter owns only SDK representation and resolver work.
type OrderChainAdapter interface {
	// AsyncEngine returns the account dispatcher on which the node runs order
	// chains. A nil engine is invalid and fails through asyncengine's explicit
	// ErrChainEngineMissing result.
	AsyncEngine() *asyncengine.AsyncEngine
	OrderModel(domain.Order) (model.Order, error)
	CheckOrderModel(domain.OrderProbe) (model.Order, error)
	RejectedOrder(domain.Order, []reject.Reject) OrderResult
	ReservedOrder(domain.Order, asyncengine.OperationResult) (OrderResult, error)
	AppliedDropCopyOrder(
		domain.Order, asyncengine.DropCopyResult,
	) (OrderResult, error)
	RejectedImmediate(domain.Order, []reject.Reject) ImmediateResult
	PrepareImmediateReservation(
		domain.Order, asyncengine.OperationResult,
	) (ImmediatePreparation, error)
	PrepareImmediateDropCopy(
		domain.Order, asyncengine.DropCopyResult,
	) (ImmediatePreparation, error)
	SettleImmediate(
		domain.Order, ImmediatePreparation, pretrade.PostTradeResult,
	) (ImmediateResult, error)
	CheckedOrder(
		domain.OrderProbe, asyncengine.OrderCheckResult,
	) (domain.CheckResult, error)
}

// AccountChainAdapter maps Officer execution-report and account-adjustment
// values to the SDK account-chain surface and materializes the canonical
// results exposed to chain hooks. The node owns chain ordering and persistence;
// the adapter owns only SDK representation and resolver work.
type AccountChainAdapter interface {
	// AccountID resolves an Officer account alias to its stable SDK chain key.
	AccountID(domain.AccountID) (param.AccountID, error)
	// ExecutionReportModel maps a report with reservationRemainder set to
	// the order's recorded reservation remainder. It is never empty and is not
	// the caller-reported leaves quantity.
	ExecutionReportModel(
		in domain.ExecutionReportInput,
		reservationRemainder string,
	) (model.ExecutionReport, error)
	SettledExecutionReport(
		domain.ExecutionReportInput,
		param.AccountID,
		pretrade.PostTradeResult,
	) (ExecutionReportResult, error)
	AccountAdjustmentModels(
		[]domain.AdjustmentRequest,
	) ([]model.AccountAdjustment, error)
	AppliedAccountAdjustmentBatch(
		domain.AccountID,
		[]domain.AdjustmentRequest,
		accountadjustment.BatchResult,
	) ([]AdjustmentResult, *domain.AdjustmentOutcomeRejected, error)
	SpotFundsAccountPnlAssignment(
		string, domain.PnlHaltReason,
	) (asyncengine.SpotFundsAccountPnlAssignment, error)
	AppliedSpotFundsAccountPnl(
		domain.AccountID,
		string,
		domain.PnlHaltReason,
		configure.PolicyConfigurationResult,
	) ([]domain.AccountBlock, error)
}

type spotFundsAccountPnlChainState struct {
	ctx        context.Context
	assignment asyncengine.SpotFundsAccountPnlAssignment
	result     configure.PolicyConfigurationResult
	err        error
}

// SetSpotFundsAccountPnl runs one already-mapped SpotFunds P&L assignment on
// its account chain. Engine adapters supply SDK values and materialize the
// returned configuration; framework owns the chain lifecycle and ordering.
func SetSpotFundsAccountPnl(
	ctx context.Context,
	async *asyncengine.AsyncEngine,
	account param.AccountID,
	assignment asyncengine.SpotFundsAccountPnlAssignment,
) (configure.PolicyConfigurationResult, error) {
	state := &spotFundsAccountPnlChainState{
		ctx:        ctx,
		assignment: assignment,
	}
	begin := func(context.Context) (*spotFundsAccountPnlChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = ctxErr
			return nil, state.err
		}
		return state, nil
	}
	builder := asyncengine.Chain(account, begin)
	builder.SetSpotFundsAccountPnl(
		asyncengine.SpotFundsAccountPnlHooks[*spotFundsAccountPnlChainState]{
			Assignment: func(
				_ context.Context,
				state *spotFundsAccountPnlChainState,
			) (asyncengine.SpotFundsAccountPnlAssignment, error) {
				return state.assignment, nil
			},
			OnSet: func(
				_ context.Context,
				state *spotFundsAccountPnlChainState,
				result configure.PolicyConfigurationResult,
			) error {
				state.result = result
				return nil
			},
		},
	)
	runner := builder.Finally(func(
		_ context.Context,
		state *spotFundsAccountPnlChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.err = outcome.Err
		return nil
	})
	_, chainErr := runner.Run(
		ctx, async,
	).Await(context.Background())
	if chainErr != nil {
		return configure.PolicyConfigurationResult{}, chainErr
	}
	if state.err != nil {
		return configure.PolicyConfigurationResult{}, state.err
	}
	return state.result, nil
}

// ExecutionReportPersistence is the Officer write set produced after the engine
// applies an execution report. Report-owned order, commission, trade, and event
// fields are copied from the original report; engine-owned account effects are
// limited to Balances and Blocks. Leaves is the caller-reported open quantity
// that the order records verbatim.
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
	// Leaves is the caller-reported open quantity to persist verbatim on the
	// order. An empty value leaves the stored quantity unchanged.
	Leaves string
	// Balances are the per-asset balance outcomes returned by the engine.
	Balances []domain.BalanceSettlement
	// Events are the order lifecycle events to append.
	Events []domain.OrderEvent
	// Blocks are account blocks returned by the engine.
	Blocks []domain.AccountBlock
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
	Blocks []domain.AccountBlock
	// Outcomes are the per-asset adjustment outcomes policies produced, each
	// tagged with its asset (both the base and the quote leg of a spot fill
	// settle).
	Outcomes []BalanceOutcome
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
	AddAssetResolverEntry(asset domain.Asset) error
	ResolveAsset(code string) (param.Asset, error)
	RenameAccountResolverEntry(oldCode domain.AccountID, account domain.Account) error
	RenameAssetResolverEntry(oldCode string, asset domain.Asset) error
	RemoveAssetResolverEntry(asset domain.Asset) error
	AddGroupResolverEntry(group domain.AccountGroup) error
	ResolveGroup(code string) (param.AccountGroupID, error)
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
	OrderChainAdapter
	AccountChainAdapter
	DictionaryResolver

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

	// MarketDataSink returns the quote sink backed by the engine's market-data
	// service. The connector manager drains normalized quotes into it; the sink
	// registers instruments on first sight and pushes quotes through the binding.
	// Rebuilds replace the engine-specific sink and resolver. Engines may share
	// the underlying service across those rebuilds.
	MarketDataSink() marketdata.Sink

	// Stop halts the engine and releases its engine-specific native resources. It
	// must not close a market-data service the engine shares; that is
	// CloseMarketDataService. After Stop the engine is no longer usable. Stop is
	// idempotent.
	Stop()

	// CloseMarketDataService releases the market-data service the engine shares
	// with the engines rebuilt before and after it. The node calls it after the
	// final Stop at shutdown and when a database reset rotates the service.
	CloseMarketDataService()
}
