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

// Package engine adapts the native OpenPit engine to the framework engine seam.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"go.openpit.dev/openpit"
	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	bindmd "go.openpit.dev/openpit/marketdata"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pkg/optional"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/pretrade/policies"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
)

// MarketDataFreshnessTTL is the quote lifetime the engine's market-data service
// is built with. It mirrors marketdata.FreshnessTTL so the engine and backend
// use one officer-wide freshness contract.
const MarketDataFreshnessTTL = marketdata.FreshnessTTL

// defaultQuoteTTL is the service-wide quote lifetime the engine's market-data
// service is built with. Risk-grade freshness (seconds-fresh): a quote older
// than this reads as absent. Making it configurable is planned. It is built from
// the single market-data freshness window shared with backend.
var defaultQuoteTTL = bindmd.WithinTTL(MarketDataFreshnessTTL)

// defaultMarketOrderSlippageBps is the worst-case slippage applied when sizing
// market-order reservations in the spot-funds policy (1500 bps = 15%, the
// binding's documented conservative default).
const defaultMarketOrderSlippageBps uint16 = 1500

// Registered policy names. The built-in risk policies register under these
// fixed names; the Configure surface targets a policy by name.
const (
	nameRateLimit      = policies.RateLimitPolicyName
	nameOrderSizeLimit = policies.OrderSizeLimitPolicyName
	nameSpotFunds      = policies.SpotFundsPolicyName
)

// openPitEngine is the concrete Engine adapter wrapping one OpenPit engine
// handle and its market-data service.
//
// The handle is built by NewOpenPitEngineBuildFunc and lives until Stop. Online
// limit changes are applied through the binding's Configure surface. rate_limit
// and order_size_limit retune their axes wholesale (barriers added and removed
// at runtime). The spot-funds policy is registered at build time; its
// P&L-bounds axes are reconfigured at runtime, including broker-axis removal
// through the explicit optional update shape. The residual changes the
// Configure surface cannot express are configuring a policy that was not
// registered at build time and removing its last barrier: the SDK requires at
// least one barrier both when a policy is built and after every update. Those
// return errors wrapping domain.ErrNotImplemented, telling the node to rebuild
// from the persisted store snapshot to avoid divergent store and engine state.
//
// Engines built for one local node share a market-data service generation.
// Replacing an engine stops only that handle. Service ownership and rotation
// follow the contract on sharedMarketDataService.
//
// The adapter tracks the set of policies registered at build time. All handle
// fields and runtime Configure state are guarded by mu. Account-scoped engine
// calls run through asyncengine lanes and use the handle snapshot captured before
// submission instead of taking this mutex on the hot path.
type openPitEngine struct {
	async        *asyncengine.AsyncEngine
	configurator configure.Configurator

	// res resolves mutable dictionary aliases to stable stored integer engine ids.
	// Its shared state is safe for concurrent hot-path lookup and publication.
	res idResolver

	// sink publishes normalized quotes into marketDataService. It owns the
	// first-sight register cache and is safe for concurrent Push (the connector
	// manager drains multiple feeds into it).
	sink              *marketDataSink
	marketDataService *sharedMarketDataService

	// registered is the set of policy names live on the handle. ConfigurePolicy
	// of an unregistered policy is a not-implemented stub: there is no runtime
	// policy-registration API.
	registered map[string]struct{}

	// seedAccountBlocks are the blocks the engine latched while this handle was
	// seeded: a seeded P&L that is halted, or that breaches its own barrier,
	// kill-switches the account before the handle ever goes live. The build seam
	// hands back only the Engine, so they ride on the handle until the node
	// drains them into the store. Immutable after construction.
	seedAccountBlocks []domain.AccountBlock

	mu      sync.RWMutex
	running bool
}

// sharedMarketDataService owns one native service generation shared by engine
// handles. Once published to the node, it has exactly two release points:
// rotation after a database reset and final node shutdown. Each calls Close,
// which releases the generation once through sync.Once. Before rotation, the
// caller must quiesce every quote publisher. The live engine remains valid
// because its policies own independent refcounted clones of the native handle.
//
// Generation state is meaningful only while the node's restart gate is held;
// that gate also serializes the builder's captured service pointer.
type sharedMarketDataService struct {
	service *bindmd.Service
	once    sync.Once
	closed  atomic.Bool
}

func (s *sharedMarketDataService) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.closed.Store(true)
		s.service.Close()
	})
}

func (s *sharedMarketDataService) isClosed() bool {
	return s != nil && s.closed.Load()
}

// newOpenPitEngine wraps an already-built *openpit.Engine and its shared
// market-data service into the Engine adapter. registered is the set of policy
// names live on the handle and is taken over by the adapter. The adapter never
// closes the service; its release follows the sharedMarketDataService lifetime
// contract. res is the code-to-engine-id resolver built from the same Snapshot.
// seedAccountBlocks are the blocks the engine reported while applying persisted
// account P&L state, carried on the handle for the node to mirror.
//
// newOpenPitEngine wraps an already-built async engine and shared market-data
// service. The async engine owns the underlying native engine lifecycle; the
// shared service remains owned by the node generation.
func newOpenPitEngine(
	async *asyncengine.AsyncEngine,
	configurator configure.Configurator,
	service *sharedMarketDataService,
	registered map[string]struct{},
	seedAccountBlocks []domain.AccountBlock,
	res idResolver,
) fwengine.Engine {
	if registered == nil {
		registered = make(map[string]struct{})
	}
	res.ensureInitialized()
	adapter := &openPitEngine{
		async:             async,
		configurator:      configurator,
		res:               res,
		sink:              newMarketDataSink(service.service, res),
		marketDataService: service,
		registered:        registered,
		seedAccountBlocks: seedAccountBlocks,
		running:           async != nil,
	}
	return adapter
}

// AddAccountResolverEntry publishes a newly persisted account alias and its
// stable engine id. The SDK accepts the numeric id dynamically, so this changes
// only Officer's dictionary resolver and does not replace the engine handle.
func (e *openPitEngine) AddAccountResolverEntry(account domain.Account) error {
	return e.res.addAccountResolverEntry(account)
}

// AddAssetResolverEntry publishes a newly persisted asset alias and its stable
// engine id before any engine call may use the human code.
func (e *openPitEngine) AddAssetResolverEntry(asset domain.Asset) error {
	return e.res.addAssetResolverEntry(asset)
}

// RenameAccountResolverEntry atomically replaces an account alias while
// preserving the persisted engine id.
func (e *openPitEngine) RenameAccountResolverEntry(
	oldCode domain.AccountID, account domain.Account,
) error {
	return e.res.renameAccountResolverEntry(oldCode, account)
}

// RenameAssetResolverEntry atomically replaces an asset alias while reusing
// the ready engine asset built for its unchanged persisted engine id.
func (e *openPitEngine) RenameAssetResolverEntry(
	oldCode string, asset domain.Asset,
) error {
	return e.res.renameAssetResolverEntry(oldCode, asset)
}

// RemoveAssetResolverEntry removes a persisted asset alias from Officer's
// resolver. It does not remove SDK state held under the asset's ready id or
// replace the engine handle.
func (e *openPitEngine) RemoveAssetResolverEntry(asset domain.Asset) error {
	return e.res.removeAssetResolverEntry(asset)
}

// AddGroupResolverEntry publishes a newly persisted group alias and its stable
// engine id without replacing the engine handle.
func (e *openPitEngine) AddGroupResolverEntry(group domain.AccountGroup) error {
	return e.res.addGroupResolverEntry(group)
}

// RenameGroupResolverEntry atomically replaces a group alias while preserving
// the persisted engine id.
func (e *openPitEngine) RenameGroupResolverEntry(
	oldCode string, group domain.AccountGroup,
) error {
	return e.res.renameGroupResolverEntry(oldCode, group)
}

// RemoveGroupResolverEntry removes a persisted group alias from Officer's
// resolver. It does not remove SDK account state or replace the engine handle.
func (e *openPitEngine) RemoveGroupResolverEntry(group domain.AccountGroup) error {
	return e.res.removeGroupResolverEntry(group)
}

// SeedAccountBlocks returns the account blocks the engine latched while this
// handle was seeded. The node mirrors them into its store before it admits
// account work, so an account the engine already kill-switched never reads as
// tradable. It is constant for the handle's lifetime and safe to call again.
func (e *openPitEngine) SeedAccountBlocks() []domain.AccountBlock {
	return e.seedAccountBlocks
}

// RestoredAccountBlockCause maps a persisted Officer block back to the SDK
// value used by the async online-restore path.
func (e *openPitEngine) RestoredAccountBlockCause(
	block domain.AccountBlock,
) (reject.AccountBlock, error) {
	return accountBlockCauseFrom(block)
}

// NewOpenPitEngineBuildFunc returns the stage-2 OpenPit builder for one local
// node. Its engine handles share one market-data service generation. A closed
// wrapper is treated as absent and replaced by the next build; later builds
// share that replacement. Service lifetime follows the sharedMarketDataService
// contract. Each engine uses AccountSync; Officer routes account and group work
// through asyncengine lanes. The builder always registers the built-in
// order-validation policy, and registers each risk policy that has at least one
// barrier in the snapshot. Blocked accounts are then applied with their
// persisted reasons.
//
// Each handle lives until Stop. Administrative operations and SDK-unsupported
// transitions may build a replacement under the node's rebuild gate. On any
// seeding failure the engine is released; if this build created the service
// because the builder had none yet, it is released too.
//
// Seeding a P&L that is halted, or that breaches its own barrier, makes the
// engine kill-switch the account while the handle is still being built - an
// ordinary restart that reseeds an accumulated loss past the barrier does this.
// Those blocks are harvested from the one authoritative per-account seed path
// and carried on the returned Engine (see openPitEngine.SeedAccountBlocks) so
// the node can mirror them durably; discarding them would leave the store
// showing a tradable account the engine rejects.
//
// The native runtime is the one the OpenPit SDK loaded at process start: the
// library OPENPIT_RUNTIME_LIBRARY_PATH names, or the SDK's embedded runtime when
// that variable is unset.
func NewOpenPitEngineBuildFunc() fwengine.BuildFunc {
	var service *sharedMarketDataService
	return func(snap fwengine.Snapshot) (fwengine.Engine, error) {
		eng, nextService, err := buildOpenPitEngine(snap, service)
		if err != nil {
			return nil, err
		}
		service = nextService
		return eng, nil
	}
}

func buildOpenPitEngine(
	snap fwengine.Snapshot,
	service *sharedMarketDataService,
) (fwengine.Engine, *sharedMarketDataService, error) {
	// Build the code-to-engine-id resolver from the snapshot's stored engine ids
	// before any binding call so seeding and the hot path both use the stored
	// integer ids, never a hashed string.
	res, err := newIDResolver(snap.Accounts, snap.Groups, snap.Assets)
	if err != nil {
		return nil, service, err
	}

	if service.isClosed() {
		service = nil
	}
	var sdkService *bindmd.Service
	if service != nil {
		sdkService = service.service
	}
	eng, sdkService, registered, buildBlocks, created, err := buildEngine(
		snap, res, sdkService,
	)
	if err != nil {
		return nil, service, err
	}
	if created {
		service = &sharedMarketDataService{service: sdkService}
	}

	// On any seeding failure, release the engine and, only when this build created
	// the service, release the service too: the adapter is not yet constructed, so
	// its Stop would not run.
	var async *asyncengine.AsyncEngine
	releaseOnErr := func(err error) (fwengine.Engine, *sharedMarketDataService, error) {
		if async != nil {
			if stopErr := async.StopGraceful(context.Background()); stopErr != nil {
				err = errors.Join(err, fmt.Errorf("engine: stop async dispatcher: %w", stopErr))
			}
		} else {
			eng.Stop()
		}
		if created {
			service.Close()
		}
		return nil, service, err
	}

	if err := hydrateGroups(eng, snap.Accounts, res); err != nil {
		return releaseOnErr(err)
	}
	if err := hydrateCurrencies(eng, snap.Accounts, snap.Groups, res); err != nil {
		return releaseOnErr(err)
	}
	async, err = asyncengine.NewBuilder(eng).
		WithStopUnderlying(eng.Stop).
		Dynamic().
		Build()
	if err != nil {
		return releaseOnErr(fmt.Errorf("engine: build async account dispatcher: %w", err))
	}
	accountPnlBlocks, err := seedSpotFundsAccountPnls(async, snap.Accounts, res)
	if err != nil {
		return releaseOnErr(err)
	}
	if err := blockGroups(eng, snap.Groups); err != nil {
		return releaseOnErr(err)
	}
	if err := seedBalances(eng, snap.Balances, res); err != nil {
		return releaseOnErr(err)
	}
	return newOpenPitEngine(
		async,
		eng.Configure(),
		service,
		registered,
		append(buildBlocks, accountPnlBlocks...),
		res,
	), service, nil
}

func barrierCount(policy string, limits fwengine.LimitSet) int {
	switch policy {
	case domain.PolicyRateLimit:
		return len(limits.RateLimits)
	case domain.PolicyOrderSizeLimit:
		return len(limits.OrderSizeLimits)
	case domain.PolicySpotFundsPnlBoundsKillSwitch:
		return len(limits.SpotFundsPnlBoundsLimits)
	default:
		return 0
	}
}

// Version returns the engine SDK/runtime version string.
func (e *openPitEngine) Version() string { return openpit.GetVersion() }

// BuildProfile returns the engine build profile string.
func (e *openPitEngine) BuildProfile() string { return openpit.GetBuildProfile() }

// Running reports whether the engine handle is live (built and not stopped).
func (e *openPitEngine) Running() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.running
}

// AsyncEngine returns the account dispatcher owned by this adapter.
func (e *openPitEngine) AsyncEngine() *asyncengine.AsyncEngine {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.async
}

// AccountID resolves an Officer account alias to its stable SDK chain key.
func (e *openPitEngine) AccountID(
	account domain.AccountID,
) (param.AccountID, error) {
	return e.res.account(account)
}

// ResolveAsset resolves a published Officer asset alias to its SDK value.
func (e *openPitEngine) ResolveAsset(code string) (param.Asset, error) {
	return e.res.asset(code)
}

// ResolveGroup resolves a published Officer group alias to its SDK value.
func (e *openPitEngine) ResolveGroup(
	code string,
) (param.AccountGroupID, error) {
	return e.res.group(code)
}

// ExecutionReportModel maps an Officer report to the SDK chain input.
func (e *openPitEngine) ExecutionReportModel(
	in domain.ExecutionReportInput,
	reservationRemainder string,
) (model.ExecutionReport, error) {
	return executionReportFrom(in, reservationRemainder, e.res)
}

// AccountAdjustmentModels maps Officer adjustment requests to one SDK batch.
func (e *openPitEngine) AccountAdjustmentModels(
	reqs []domain.AdjustmentRequest,
) ([]model.AccountAdjustment, error) {
	adjustments := make([]model.AccountAdjustment, 0, len(reqs))
	for _, req := range reqs {
		adjustment, err := accountAdjustmentFromRequest(req, e.res)
		if err != nil {
			return nil, err
		}
		adjustments = append(adjustments, adjustment)
	}
	return adjustments, nil
}

// SpotFundsAccountPnlAssignment maps a persisted numeric or halted P&L state
// to the SDK value consumed by the account-chain step.
func (e *openPitEngine) SpotFundsAccountPnlAssignment(
	pnl string,
	haltReason domain.PnlHaltReason,
) (asyncengine.SpotFundsAccountPnlAssignment, error) {
	if pnl != "" && haltReason != "" {
		return asyncengine.SpotFundsAccountPnlAssignment{}, fmt.Errorf(
			"engine: account pnl state has both value %q and halt reason %q: %w",
			pnl, haltReason, domain.ErrInvalid,
		)
	}
	state, err := pnlStateFrom(pnl, haltReason)
	if err != nil {
		return asyncengine.SpotFundsAccountPnlAssignment{}, fmt.Errorf(
			"engine: account pnl state: %w", err,
		)
	}
	return asyncengine.SpotFundsAccountPnlAssignment{
		PolicyName: policies.SpotFundsPolicyName,
		State:      state,
	}, nil
}

// AppliedSpotFundsAccountPnl materializes blocks returned after the SDK has
// finalized the account-chain P&L step.
func (e *openPitEngine) AppliedSpotFundsAccountPnl(
	account domain.AccountID,
	_ string,
	_ domain.PnlHaltReason,
	result configure.PolicyConfigurationResult,
) ([]domain.AccountBlock, error) {
	return policyConfigurationBlocksFrom(
		result.AccountBlocks,
		account,
		domain.PolicySpotFundsPnlBoundsKillSwitch,
	), nil
}

// OrderModel resolves an Officer order to the SDK model used as a chain source.
func (e *openPitEngine) OrderModel(o domain.Order) (model.Order, error) {
	accountID, err := e.res.account(o.Account)
	if err != nil {
		return model.Order{}, err
	}
	return orderModelFromAccount(o, accountID, e.res)
}

// CheckOrderModel resolves an Officer dry-run probe to an SDK order model.
func (e *openPitEngine) CheckOrderModel(
	probe domain.OrderProbe,
) (model.Order, error) {
	return e.OrderModel(domain.Order{
		Account:     probe.Account,
		BaseAsset:   probe.BaseAsset,
		QuoteAsset:  probe.QuoteAsset,
		Side:        probe.Side,
		AmountKind:  probe.AmountKind,
		AmountValue: probe.AmountValue,
		Price:       probe.Price,
	})
}

// ConfigurePolicy applies the change to the live handle via the Configure
// surface. limits is the complete barrier set for policy.
//
// rate_limit / order_size_limit: the full axes are rebuilt from the complete
// barrier set and replaced in one Configure call. Barriers, including a broker
// barrier, are added and removed at runtime while the policy remains non-empty.
// Configuring an unregistered policy returns a not-implemented stub. Removing
// the last barrier returns the same stub except for
// spot_funds_pnl_bounds_kill_switch, whose separate SDK surface can clear empty
// axes online. Live state is changed only on success.
func (e *openPitEngine) ConfigurePolicy(
	ctx context.Context, policy string, limits fwengine.LimitSet,
) (fwengine.PolicyConfigurationResult, error) {
	if err := ctx.Err(); err != nil {
		return fwengine.PolicyConfigurationResult{}, fmt.Errorf("engine: configure cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return fwengine.PolicyConfigurationResult{}, fmt.Errorf("engine: configure on stopped engine")
	}

	if _, ok := e.registered[policyName(policy)]; !ok {
		return fwengine.PolicyConfigurationResult{}, fmt.Errorf(
			"engine: cannot configure unregistered policy %q: runtime policy "+
				"registration is not supported by the SDK yet: %w",
			policy, domain.ErrNotImplemented)
	}
	if policy != domain.PolicySpotFundsPnlBoundsKillSwitch &&
		barrierCount(policy, limits) == 0 {
		return fwengine.PolicyConfigurationResult{}, fmt.Errorf(
			"engine: cannot remove the last barrier of policy %q: empty policy "+
				"settings are rejected by the SDK: %w",
			policy, domain.ErrNotImplemented)
	}
	switch policy {
	case domain.PolicyRateLimit:
		if err := e.configureRateLimitLocked(limits.RateLimits); err != nil {
			return fwengine.PolicyConfigurationResult{}, err
		}
		return fwengine.PolicyConfigurationResult{}, nil
	case domain.PolicyOrderSizeLimit:
		if err := e.configureOrderSizeLocked(limits.OrderSizeLimits); err != nil {
			return fwengine.PolicyConfigurationResult{}, err
		}
		return fwengine.PolicyConfigurationResult{}, nil
	case domain.PolicySpotFundsPnlBoundsKillSwitch:
		return e.configureSpotFundsPnlBoundsLocked(limits.SpotFundsPnlBoundsLimits)
	default:
		return fwengine.PolicyConfigurationResult{}, fmt.Errorf("engine: configure unknown policy %q", policy)
	}
}

// configureRateLimitLocked replaces the rate-limit axes on the live handle from
// the complete barrier set. The asset/account/account-asset axes are passed as
// non-nil (possibly empty) slices so each is replaced wholesale; barriers are
// added and removed at runtime and a surviving key keeps its live counter. The
// broker axis uses the explicit optional update shape, so a nil broker clears it
// rather than leaving it unchanged. Callers must hold e.mu.
func (e *openPitEngine) configureRateLimitLocked(limits []domain.LimitRate) error {
	broker, assets, accounts, accountAssets, err := rateLimitAxes(limits, e.res)
	if err != nil {
		return err
	}
	if err := e.configurator.RateLimitUpdate(
		nameRateLimit, optional.Some(broker), assets, accounts, accountAssets,
	); err != nil {
		return fmt.Errorf("engine: configure rate_limit: %w", err)
	}
	return nil
}

// configureOrderSizeLocked replaces the order-size axes on the live handle from
// the complete barrier set. Empty asset/account-asset axes are passed as empty
// non-nil slices so they are cleared rather than left unchanged. The Configure
// broker axis uses the explicit optional update shape, so a nil broker clears it
// rather than leaving it unchanged. Callers must hold e.mu.
func (e *openPitEngine) configureOrderSizeLocked(limits []domain.LimitOrderSize) error {
	broker, assets, accountAssets, err := orderSizeAxes(limits, e.res)
	if err != nil {
		return err
	}
	if err := e.configurator.OrderSizeLimitUpdate(
		nameOrderSizeLimit, optional.Some(broker), assets, accountAssets,
	); err != nil {
		return fmt.Errorf("engine: configure order_size_limit: %w", err)
	}
	return nil
}

// configureSpotFundsPnlBoundsLocked replaces the SpotFunds self-computed
// account-currency P&L bounds axes on the live handle from the complete barrier
// set. Empty axes are passed as empty non-nil slices so they are cleared rather
// than left unchanged. Runtime bounds updates never touch the live account P&L
// accumulator; account P&L is set through the dedicated account operation.
// Callers must hold e.mu.
func (e *openPitEngine) configureSpotFundsPnlBoundsLocked(
	limits []domain.LimitSpotFundsPnlBounds,
) (fwengine.PolicyConfigurationResult, error) {
	return configureSpotFundsPnlBounds(e.configurator, e.res, limits)
}

func configureSpotFundsPnlBounds(
	configurator configure.Configurator,
	res idResolver,
	limits []domain.LimitSpotFundsPnlBounds,
) (fwengine.PolicyConfigurationResult, error) {
	global, groups, accounts, err := spotFundsPnlBoundsAxes(limits, res)
	if err != nil {
		return fwengine.PolicyConfigurationResult{}, err
	}
	outcomes, err := configurator.SpotFundsPnlBoundsKillSwitch(
		policies.SpotFundsPolicyName, global, groups, accounts,
	)
	if err != nil {
		return fwengine.PolicyConfigurationResult{}, fmt.Errorf(
			"engine: configure spot_funds_pnl_bounds_kill_switch: %w", err,
		)
	}
	blocks, err := policyConfigurationBlockOutcomesFrom(
		outcomes.AccountBlocks, res, domain.PolicySpotFundsPnlBoundsKillSwitch,
	)
	if err != nil {
		return fwengine.PolicyConfigurationResult{}, err
	}
	return fwengine.PolicyConfigurationResult{AccountBlocks: blocks}, nil
}

// AppliedAccountAdjustmentBatch maps the canonical SDK batch result back to
// the request order without performing another engine operation.
func (e *openPitEngine) AppliedAccountAdjustmentBatch(
	account domain.AccountID,
	reqs []domain.AdjustmentRequest,
	batch accountadjustment.BatchResult,
) ([]fwengine.AdjustmentResult, *domain.AdjustmentOutcomeRejected, error) {
	if rej, ok := batch.BatchError.Get(); ok {
		rejected, err := outcomeRejectedFrom(rej)
		if err != nil {
			return nil, nil, err
		}
		return nil, &rejected, nil
	}
	// The engine reports the kill-switch it latched while committing the batch -
	// a force-set that lands out of the account's P&L bounds blocks the account
	// there and then. The block is batch-level, so it is stamped on every result
	// of this batch; the caller mirrors blocks keyed by account, which collapses
	// the repeats. Waiting for a later fill to rediscover the block is forbidden.
	blocks := policyConfigurationBlocksFrom(
		batch.AccountBlocks, account, domain.PolicySpotFundsPnlBoundsKillSwitch,
	)
	outcomes := batch.Outcomes
	if len(outcomes) == 0 && len(blocks) == 0 {
		return nil, nil, nil
	}
	results := make([]fwengine.AdjustmentResult, 0, len(reqs))
	for _, req := range reqs {
		result := fwengine.AdjustmentResult{AccountBlocks: blocks}
		accepted, ok, err := outcomeAcceptedFromList(outcomes, req.Asset, e.res)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			result.Accepted = &accepted
		}
		results = append(results, result)
	}
	return results, nil, nil
}

// RejectedOrder materializes the rejected branch of an SDK order chain.
func (e *openPitEngine) RejectedOrder(
	_ domain.Order, rejects []reject.Reject,
) fwengine.OrderResult {
	return fwengine.OrderResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}
}

// ReservedOrder captures every persistence value while reservation is live.
func (e *openPitEngine) ReservedOrder(
	o domain.Order, result asyncengine.OperationResult,
) (fwengine.OrderResult, error) {
	return e.materializeOrderResult(o, result, nil, "reservation")
}

// AppliedDropCopyOrder captures the operation values and requested block while
// the drop-copy operation can still be rolled back.
func (e *openPitEngine) AppliedDropCopyOrder(
	o domain.Order, result asyncengine.DropCopyResult,
) (fwengine.OrderResult, error) {
	block, err := result.AccountBlock()
	if err != nil {
		return fwengine.OrderResult{}, err
	}
	return e.materializeOrderResult(o, result, block, "drop-copy")
}

func (e *openPitEngine) materializeOrderResult(
	o domain.Order,
	result asyncengine.OperationResult,
	block *reject.AccountBlock,
	operationName string,
) (fwengine.OrderResult, error) {
	lock, err := result.Lock()
	if err != nil {
		return fwengine.OrderResult{}, fmt.Errorf(
			"engine: read %s lock: %w", operationName, err,
		)
	}
	lockBytes, settlement, err := capturePreTradeOutput(lock, o)
	if err != nil {
		return fwengine.OrderResult{}, err
	}
	adjustments, err := result.AccountAdjustments()
	if err != nil {
		return fwengine.OrderResult{}, err
	}
	outcomes, err := balanceOutcomesFromList(adjustments, e.res)
	if err != nil {
		return fwengine.OrderResult{}, err
	}
	var blocks []reject.AccountBlock
	if block != nil {
		blocks = append(blocks, *block)
	}
	return fwengine.OrderResult{
		Accepted:            true,
		Lock:                lockBytes,
		Blocks:              executionBlocksFrom(blocks, o.Account),
		Outcomes:            outcomes,
		SettlementLockPrice: settlement,
	}, nil
}

// CheckedOrder converts the capability-limited dry-run result to Officer's
// domain verdict.
func (e *openPitEngine) CheckedOrder(
	probe domain.OrderProbe, result asyncengine.OrderCheckResult,
) (domain.CheckResult, error) {
	if !result.Passed() {
		block, err := result.AccountBlock()
		if err != nil {
			return domain.CheckResult{}, fmt.Errorf(
				"engine: read dry-run account block: %w", err,
			)
		}
		return domain.CheckResult{
			Passed:     false,
			Rejects:    orderRejectsFrom(result.Rejects()),
			WouldBlock: accountBlockFrom(block, probe.Account),
		}, nil
	}
	lock, err := result.Lock()
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf(
			"engine: read dry-run lock: %w", err,
		)
	}
	prices, err := lock.Prices()
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf(
			"engine: read dry-run lock prices: %w", err,
		)
	}
	lockPrice := settlementPrice(
		pricesToStrings(prices), "dry-run:"+string(probe.Account),
	)
	return domain.CheckResult{Passed: true, WouldLockPrice: lockPrice}, nil
}

// SettledExecutionReport maps the canonical SDK post-trade result without
// performing another engine operation.
func (e *openPitEngine) SettledExecutionReport(
	in domain.ExecutionReportInput,
	accountID param.AccountID,
	result pretrade.PostTradeResult,
) (fwengine.ExecutionReportResult, error) {
	blocks := executionBlocksFrom(result.AccountBlocks, in.Account)
	outcomes, err := balanceOutcomesFromList(result.AccountAdjustments, e.res)
	if err != nil {
		return fwengine.ExecutionReportResult{}, err
	}
	accountPnl, accountPnlHaltReason, err := spotFundsAccountPnlFromList(
		accountID,
		result.AccountPnls,
	)
	if err != nil {
		return fwengine.ExecutionReportResult{}, err
	}
	persistence := executionReportPersistenceFrom(
		in,
		blocks,
		outcomes,
		accountPnl,
		accountPnlHaltReason,
	)
	return fwengine.ExecutionReportResult{
		Persistence: &persistence,
		Blocks:      blocks,
		Outcomes:    outcomes,
	}, nil
}

// CheckOrder runs the pre-trade pipeline for probe as a non-mutating dry-run.
// It builds the same model.Order as SubmitOrder and runs ExecutePreTradeDryRun,
// which evaluates every policy but commits nothing: no reservation is taken and
// no account state changes. On pass it reads the would-be settlement price
// directly from the live lock; on reject it returns the engine
// rejects plus the account block the engine would record. The dry-run report
// owns native memory and never escapes the adapter.
// MarketDataSink returns the quote sink backed by the node's shared market-data
// service. Rebuilds create a current sink and resolver over that service.
func (e *openPitEngine) MarketDataSink() marketdata.Sink {
	return e.sink
}

// Stop halts the engine and releases its engine-specific native resources. It
// does not release the shared market-data service; that follows the
// sharedMarketDataService lifetime contract. Stop is idempotent.
func (e *openPitEngine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	e.mu.Unlock()

	async := e.async
	if async != nil {
		if err := async.StopGraceful(context.Background()); err != nil {
			// A graceful-stop failure must not leave the native handle owned by
			// the dispatcher while the market-data service is closed below.
			if hardErr := async.StopHard(context.Background()); hardErr != nil {
				slog.Error(
					"hard engine stop failed after graceful stop",
					"graceful_error", err,
					"hard_error", hardErr,
				)
				return
			}
		}
	}

	e.mu.Lock()
	e.async = nil
	e.configurator = configure.Configurator{}
	e.mu.Unlock()
}

// CloseMarketDataService releases the current generation according to the
// sharedMarketDataService lifetime contract.
func (e *openPitEngine) CloseMarketDataService() {
	e.marketDataService.Close()
}

// policyName maps a domain policy id to the name the policy registers under.
func policyName(policy string) string {
	switch policy {
	case domain.PolicyRateLimit:
		return nameRateLimit
	case domain.PolicyOrderSizeLimit:
		return nameOrderSizeLimit
	case domain.PolicySpotFundsPnlBoundsKillSwitch:
		return nameSpotFunds
	default:
		return policy
	}
}

// buildEngine constructs an OpenPit engine plus its market-data service,
// registering the order-validation policy plus each risk policy that has at
// least one barrier in the snapshot. It returns the engine, the service, the set
// of registered policy names, and a reserved empty block result retained by the
// private build seam. Account P&L is seeded later through AsyncEngine. The SDK
// rejects empty rate-limit and order-size-limit settings, so those policies are
// not registered until their snapshots contain a barrier. res resolves
// account/group codes to engine ids for account-scoped barriers.
//
// When service is nil, the market-data service is built before the engine (the
// engine builder requires it to exist first), even when nothing is configured:
// an empty service is a harmless registry the connector manager fills at
// runtime. A service created by this call is closed on a later build failure; a
// supplied service stays live. On success the node builder retains the service
// across engine rebuilds.
//
// The spot-funds policy is always registered and wired to the market-data
// service via WithMarketOrders(service, defaultMarketOrderSlippageBps): market
// orders are priced off the mark quote rather than rejected. Instruments without
// a live quote reject with MarkPriceUnavailable instead of UnsupportedOrderType.
// Persisted account blocks are applied immediately after Build, before a P&L
// barrier can evaluate the implicit zero accumulator and latch a different
// first cause. Persisted account P&L state is applied later through
// AsyncEngine lanes.
// Runtime P&L-bounds changes only replace barrier axes; they never reset or
// seed the live accumulator. Operator-configurable slippage and runtime
// enable/disable remain future work.
func buildEngine(
	snap fwengine.Snapshot, res idResolver, service *bindmd.Service,
) (
	*openpit.Engine,
	*bindmd.Service,
	map[string]struct{},
	[]domain.AccountBlock,
	bool,
	error,
) {
	// The market-data service is intentionally FullSync for every non-NoSync
	// engine builder, and it must be built before the AccountSync engine.
	eb := openpit.NewEngineBuilder().AccountSync()
	created := service == nil
	if created {
		var err error
		service, err = eb.MarketData(defaultQuoteTTL).Build()
		if err != nil {
			return nil, nil, nil, nil, false, fmt.Errorf(
				"engine: build market-data service: %w", err,
			)
		}
	}
	closeCreatedService := func() {
		if created {
			service.Close()
		}
	}

	// OrderValidation first, then SpotFunds, then the conditional risk policies.
	// SpotFunds must be registered for balance seeding and spot holdings to take
	// effect. WithMarketOrders wires the already-built service so market orders
	// are priced off the mark quote; runtime slippage config remains future work.
	builder := eb.
		Builtin(policies.BuildOrderValidation()).
		Builtin(policies.BuildSpotFunds().
			WithMarketOrders(service, defaultMarketOrderSlippageBps).
			PolicyGroupID(0))

	// SpotFunds is always registered by the build path above.
	registered := map[string]struct{}{nameSpotFunds: {}}
	if len(snap.RateLimits) > 0 {
		ready, err := rateLimitReady(snap.RateLimits, res)
		if err != nil {
			builder.Close()
			closeCreatedService()
			return nil, nil, nil, nil, false, err
		}
		builder = builder.Builtin(ready)
		registered[nameRateLimit] = struct{}{}
	}
	if len(snap.OrderSizeLimits) > 0 {
		ready, err := orderSizeReady(snap.OrderSizeLimits, res)
		if err != nil {
			builder.Close()
			closeCreatedService()
			return nil, nil, nil, nil, false, err
		}
		builder = builder.Builtin(ready)
		registered[nameOrderSizeLimit] = struct{}{}
	}
	eng, err := builder.Build()
	if err != nil {
		closeCreatedService()
		return nil, nil, nil, nil, false, fmt.Errorf(
			"engine: build openpit engine: %w", err,
		)
	}
	releaseOnErr := func(
		err error,
	) (
		*openpit.Engine,
		*bindmd.Service,
		map[string]struct{},
		[]domain.AccountBlock,
		bool,
		error,
	) {
		eng.Stop()
		closeCreatedService()
		return nil, nil, nil, nil, false, err
	}
	if err := applyBlocks(eng, snap.Accounts); err != nil {
		return releaseOnErr(err)
	}
	var buildBlocks []domain.AccountBlock
	if len(snap.SpotFundsPnlBoundsLimits) > 0 {
		configuration, err := configureSpotFundsPnlBounds(
			eng.Configure(), res, snap.SpotFundsPnlBoundsLimits,
		)
		if err != nil {
			return releaseOnErr(err)
		}
		buildBlocks = configuration.AccountBlocks
	}
	return eng, service, registered, buildBlocks, created, nil
}

// applyBlocks blocks every blocked account in accounts on the engine with its
// persisted cause, addressing each by its stored engine account id.
func applyBlocks(eng *openpit.Engine, accounts []domain.Account) error {
	handle := eng.Accounts()
	for _, account := range accounts {
		if !account.Blocked {
			continue
		}
		accountID, err := engineAccountID(account.EngineAccountID, account.Code)
		if err != nil {
			return fmt.Errorf("engine: block account %q: %w", account.Code, err)
		}
		if account.BlockCode == "" {
			handle.Block(accountID, account.BlockReason)
			continue
		}
		cause, err := accountBlockCauseFrom(domain.AccountBlock{
			Account: account.Code,
			Policy:  account.BlockPolicy,
			Code:    account.BlockCode,
			Reason:  account.BlockReason,
			Details: account.BlockDetails,
		})
		if err != nil {
			return fmt.Errorf(
				"engine: block account %q with persisted code %q: %w",
				account.Code, account.BlockCode, err,
			)
		}
		if err := handle.BlockWithCause(accountID, cause); err != nil {
			return fmt.Errorf(
				"engine: block account %q with persisted code %q: %w",
				account.Code, account.BlockCode, err,
			)
		}
	}
	return nil
}

// hydrateGroups registers each account that carries a non-empty GroupCode into
// its group on the engine, one RegisterGroup call per group, addressing accounts
// and groups by their stored engine ids. RegisterGroup is all-or-nothing; a
// register error signals corruption of our own persisted membership and aborts
// startup. The group's engine id is resolved through res, which carries every
// snapshot group's stored engine group id.
func hydrateGroups(eng *openpit.Engine, accounts []domain.Account, res idResolver) error {
	byGroup := make(map[string][]param.AccountID)
	order := make([]string, 0)
	for _, account := range accounts {
		if account.GroupCode == "" {
			continue
		}
		id, err := engineAccountID(account.EngineAccountID, account.Code)
		if err != nil {
			return fmt.Errorf("engine: hydrate group account %q: %w", account.Code, err)
		}
		if _, seen := byGroup[account.GroupCode]; !seen {
			order = append(order, account.GroupCode)
		}
		byGroup[account.GroupCode] = append(byGroup[account.GroupCode], id)
	}

	handle := eng.Accounts()
	for _, groupCode := range order {
		group, err := res.group(groupCode)
		if err != nil {
			return err
		}
		if err := handle.RegisterGroup(byGroup[groupCode], group); err != nil {
			return fmt.Errorf("engine: register group %q: %w", groupCode, err)
		}
	}
	return nil
}

// blockGroups blocks every blocked group in groups on the engine with its
// persisted reason, addressing each by its stored engine group id.
func blockGroups(eng *openpit.Engine, groups []domain.AccountGroup) error {
	handle := eng.Accounts()
	for _, group := range groups {
		if group.Code == "" {
			if group.Blocked {
				return fmt.Errorf("engine: block default group: %w", domain.ErrReservedGroup)
			}
			continue
		}
		if !group.Blocked {
			continue
		}
		id, err := engineGroupID(group.EngineGroupID, group.Code)
		if err != nil {
			return err
		}
		if err := handle.BlockGroup(id, group.BlockReason); err != nil {
			return groupBlockError("block group", group.Code, err)
		}
	}
	return nil
}

func groupBlockError(op, _ string, err error) error {
	var blockErr *reject.AccountBlockError
	if errors.As(err, &blockErr) &&
		blockErr.Kind == reject.AccountBlockErrorKindReservedGroup {
		return fmt.Errorf(
			"engine: %s: %w: %w", op, err, domain.ErrReservedGroup,
		)
	}
	return fmt.Errorf("engine: %s: %w", op, err)
}

func hydrateCurrencies(
	eng *openpit.Engine,
	accounts []domain.Account,
	groups []domain.AccountGroup,
	res idResolver,
) error {
	return applyCurrencies(eng.Accounts(), accounts, groups, res)
}

type currencyAccounts interface {
	SetGroupCurrency(param.AccountGroupID, param.Asset) error
	SetCurrency(param.AccountID, param.Asset) error
}

func applyCurrencies(
	handle currencyAccounts,
	accounts []domain.Account,
	groups []domain.AccountGroup,
	res idResolver,
) error {
	for _, group := range groups {
		if group.Currency == "" {
			continue
		}
		groupID, err := res.group(group.Code)
		if err != nil {
			return err
		}
		asset, err := res.asset(group.Currency)
		if err != nil {
			return fmt.Errorf("engine: group %q currency %q: %w", group.Code, group.Currency, err)
		}
		if err := handle.SetGroupCurrency(groupID, asset); err != nil {
			return fmt.Errorf("engine: set group %q currency: %w", group.Code, err)
		}
	}
	for _, account := range accounts {
		if account.Currency == "" {
			continue
		}
		accountID, err := res.account(account.Code)
		if err != nil {
			return err
		}
		asset, err := res.asset(account.Currency)
		if err != nil {
			return fmt.Errorf(
				"engine: account %q currency %q: %w",
				account.Code,
				account.Currency,
				err,
			)
		}
		if err := handle.SetCurrency(accountID, asset); err != nil {
			return fmt.Errorf("engine: set account %q currency: %w", account.Code, err)
		}
	}
	return nil
}

// seedBalances applies each persisted balance as one absolute account
// adjustment, setting available/held/incoming, average-entry-price, and
// realized PnL so the spot-funds policy starts from the stored holdings. A
// reject or error signals corruption of our own persisted values and aborts
// startup.
func seedBalances(eng *openpit.Engine, balances []domain.Balance, res idResolver) error {
	for _, balance := range balances {
		account, err := res.account(balance.Account)
		if err != nil {
			return fmt.Errorf("engine: seed balance account %q: %w", balance.Account, err)
		}
		adjustment, err := balanceSeedAdjustment(balance, res)
		if err != nil {
			return err
		}
		batch, err := eng.ApplyAccountAdjustment(account, []model.AccountAdjustment{adjustment})
		if err != nil {
			return fmt.Errorf("engine: seed balance %s/%s: %w",
				balance.Account, balance.Asset, err)
		}
		if rej, ok := batch.BatchError.Get(); ok {
			return fmt.Errorf("engine: seed balance %s/%s rejected: %s: %w",
				balance.Account, balance.Asset, seedRejectReason(rej), domain.ErrInvalid)
		}
	}
	return nil
}

type spotFundsAccountPnlSeed struct {
	code    domain.AccountID
	account param.AccountID
	state   model.PnlState
}

func spotFundsAccountPnlSeeds(
	accounts []domain.Account,
	res idResolver,
) ([]spotFundsAccountPnlSeed, error) {
	seeds := make([]spotFundsAccountPnlSeed, 0)
	for _, account := range accounts {
		if account.Pnl == "" && account.PnlHaltReason == "" {
			continue
		}
		accountID, err := res.account(account.Code)
		if err != nil {
			return nil, fmt.Errorf(
				"engine: seed account pnl account %q: %w", account.Code, err)
		}
		state, err := pnlStateFrom(account.Pnl, account.PnlHaltReason)
		if err != nil {
			return nil, fmt.Errorf("engine: seed account pnl %q: %w", account.Code, err)
		}
		seeds = append(seeds, spotFundsAccountPnlSeed{
			code:    account.Code,
			account: accountID,
			state:   state,
		})
	}
	return seeds, nil
}

// seedSpotFundsAccountPnls applies one authoritative persisted P&L state per
// account.
func seedSpotFundsAccountPnls(
	async *asyncengine.AsyncEngine,
	accounts []domain.Account,
	res idResolver,
) ([]domain.AccountBlock, error) {
	if async == nil {
		return nil, fmt.Errorf("engine: seed account pnl without async dispatcher")
	}
	seeds, err := spotFundsAccountPnlSeeds(accounts, res)
	if err != nil {
		return nil, err
	}
	var blocks []domain.AccountBlock
	for _, seed := range seeds {
		configuration, err := fwengine.SetSpotFundsAccountPnl(
			context.Background(),
			async,
			seed.account,
			asyncengine.SpotFundsAccountPnlAssignment{
				PolicyName: policies.SpotFundsPolicyName,
				State:      seed.state,
			},
		)
		if err != nil {
			return nil, fmt.Errorf(
				"engine: seed spot funds account pnl: %w", err,
			)
		}
		blocks = append(blocks, policyConfigurationBlocksFrom(
			configuration.AccountBlocks,
			seed.code,
			domain.PolicySpotFundsPnlBoundsKillSwitch,
		)...)
	}
	return blocks, nil
}

// balanceSeedAdjustment builds the absolute adjustment that seeds one stored
// balance: available/held/incoming set absolutely when the persisted value is
// non-empty, left nil when empty. adjustmentAmountValues treats a nil pointer
// as not set, so the binding never sees an empty string.
func balanceSeedAdjustment(
	balance domain.Balance,
	res idResolver,
) (model.AccountAdjustment, error) {
	absField := func(v string) *domain.AdjustmentAmount {
		if v == "" {
			return nil
		}
		return &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: v}
	}
	req := domain.AdjustmentRequest{
		Asset:                 balance.Asset,
		AverageEntryPrice:     balance.AverageEntryPrice,
		RealizedPnl:           balance.RealizedPnl,
		RealizedPnlHaltReason: balance.RealizedPnlHaltReason,
		Balance:               absField(balance.Available),
		Held:                  absField(balance.Held),
		Incoming:              absField(balance.Incoming),
	}
	return accountAdjustmentFromRequest(req, res)
}

// seedRejectReason renders the first reject of a seed batch error for the
// startup failure message.
func seedRejectReason(batch reject.AccountAdjustmentBatchError) string {
	if len(batch.Rejects) == 0 {
		return "no reject detail"
	}
	r := batch.Rejects[0]
	return fmt.Sprintf("%s %s", rejectCodeName(r.Code), sanitizeText(r.Reason))
}
