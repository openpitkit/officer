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

package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"go.openpit.dev/openpit"
	"go.openpit.dev/openpit/asyncengine"
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

// envRuntimeLibraryPath is the binding environment variable that pins the
// native runtime library to an explicit pre-extracted path, bypassing the
// binding's own extraction. Pit Officer sets it from
// config.Config.RuntimeLibraryPath before the first binding call so operators
// can run against a known dylib.
const envRuntimeLibraryPath = "OPENPIT_RUNTIME_LIBRARY_PATH"

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
	eng   *openpit.Engine
	async *asyncengine.AsyncEngine

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

type accountLane struct {
	owner        *openPitEngine
	eng          *openpit.Engine
	accountAlias domain.AccountID
	accountID    param.AccountID
}

type groupLane struct {
	owner      *openPitEngine
	eng        *openpit.Engine
	accountIDs map[domain.AccountID]param.AccountID
	groupAlias string
	groupID    param.AccountGroupID
}

// newOpenPitEngine wraps an already-built *openpit.Engine and its shared
// market-data service into the Engine adapter. registered is the set of policy
// names live on the handle and is taken over by the adapter. The adapter never
// closes the service; its release follows the sharedMarketDataService lifetime
// contract. res is the code-to-engine-id resolver built from the same Snapshot.
// seedAccountBlocks are the blocks the engine reported while applying persisted
// account P&L state, carried on the handle for the node to mirror.
func newOpenPitEngine(
	eng *openpit.Engine,
	async *asyncengine.AsyncEngine,
	service *sharedMarketDataService,
	registered map[string]struct{},
	seedAccountBlocks []domain.AccountBlock,
	res idResolver,
) Engine {
	if registered == nil {
		registered = make(map[string]struct{})
	}
	res.ensureInitialized()
	adapter := &openPitEngine{
		eng:               eng,
		async:             async,
		res:               res,
		sink:              newMarketDataSink(service.service, res),
		marketDataService: service,
		registered:        registered,
		seedAccountBlocks: seedAccountBlocks,
		running:           eng != nil && async != nil,
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
// runtimeLibraryPath, when non-empty, is exported as
// OPENPIT_RUNTIME_LIBRARY_PATH before the engine is built so the binding loads
// that explicit native runtime instead of extracting its embedded copy. It must
// be set before any other binding call in the process; this builder is the
// binding entry point, so setting it here is sufficient.
func NewOpenPitEngineBuildFunc(runtimeLibraryPath string) fwengine.BuildFunc {
	var service *sharedMarketDataService
	return func(snap Snapshot) (Engine, error) {
		eng, nextService, err := buildOpenPitEngine(runtimeLibraryPath, snap, service)
		if err != nil {
			return nil, err
		}
		service = nextService
		return eng, nil
	}
}

func buildOpenPitEngine(
	runtimeLibraryPath string,
	snap Snapshot,
	service *sharedMarketDataService,
) (Engine, *sharedMarketDataService, error) {
	if runtimeLibraryPath != "" {
		if err := os.Setenv(envRuntimeLibraryPath, runtimeLibraryPath); err != nil {
			return nil, service, fmt.Errorf(
				"engine: set %s: %w", envRuntimeLibraryPath, err,
			)
		}
	}

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
	releaseOnErr := func(err error) (Engine, *sharedMarketDataService, error) {
		if async != nil {
			_ = async.StopGraceful(context.Background())
		}
		eng.Stop()
		if created {
			service.Close()
		}
		return nil, service, err
	}

	if err := applyBlocks(eng, snap.Accounts); err != nil {
		return releaseOnErr(err)
	}
	if err := hydrateGroups(eng, snap.Accounts, res); err != nil {
		return releaseOnErr(err)
	}
	if err := hydrateCurrencies(eng, snap.Accounts, snap.Groups, res); err != nil {
		return releaseOnErr(err)
	}
	async, err = asyncengine.NewBuilder(eng).Dynamic().Build()
	if err != nil {
		return releaseOnErr(fmt.Errorf("engine: build async account dispatcher: %w", err))
	}
	accountPnlBlocks, err := seedSpotFundsAccountPnls(
		async, eng, snap.Accounts, res,
	)
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
		eng,
		async,
		service,
		registered,
		append(buildBlocks, accountPnlBlocks...),
		res,
	), service, nil
}

func barrierCount(policy string, limits LimitSet) int {
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
	ctx context.Context, policy string, limits LimitSet,
) (PolicyConfigurationResult, error) {
	if err := ctx.Err(); err != nil {
		return PolicyConfigurationResult{}, fmt.Errorf("engine: configure cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return PolicyConfigurationResult{}, fmt.Errorf("engine: configure on stopped engine")
	}

	if _, ok := e.registered[policyName(policy)]; !ok {
		return PolicyConfigurationResult{}, fmt.Errorf(
			"engine: cannot configure unregistered policy %q: runtime policy "+
				"registration is not supported by the SDK yet: %w",
			policy, domain.ErrNotImplemented)
	}
	if policy != domain.PolicySpotFundsPnlBoundsKillSwitch &&
		barrierCount(policy, limits) == 0 {
		return PolicyConfigurationResult{}, fmt.Errorf(
			"engine: cannot remove the last barrier of policy %q: empty policy "+
				"settings are rejected by the SDK: %w",
			policy, domain.ErrNotImplemented)
	}
	switch policy {
	case domain.PolicyRateLimit:
		if err := e.configureRateLimitLocked(limits.RateLimits); err != nil {
			return PolicyConfigurationResult{}, err
		}
		return PolicyConfigurationResult{}, nil
	case domain.PolicyOrderSizeLimit:
		if err := e.configureOrderSizeLocked(limits.OrderSizeLimits); err != nil {
			return PolicyConfigurationResult{}, err
		}
		return PolicyConfigurationResult{}, nil
	case domain.PolicySpotFundsPnlBoundsKillSwitch:
		return e.configureSpotFundsPnlBoundsLocked(limits.SpotFundsPnlBoundsLimits)
	default:
		return PolicyConfigurationResult{}, fmt.Errorf("engine: configure unknown policy %q", policy)
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
	if err := e.eng.Configure().RateLimitUpdate(
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
	if err := e.eng.Configure().OrderSizeLimitUpdate(
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
) (PolicyConfigurationResult, error) {
	return configureSpotFundsPnlBounds(e.eng, e.res, limits)
}

func configureSpotFundsPnlBounds(
	eng *openpit.Engine,
	res idResolver,
	limits []domain.LimitSpotFundsPnlBounds,
) (PolicyConfigurationResult, error) {
	global, groups, accounts, err := spotFundsPnlBoundsAxes(limits, res)
	if err != nil {
		return PolicyConfigurationResult{}, err
	}
	outcomes, err := eng.Configure().SpotFundsPnlBoundsKillSwitch(
		policies.SpotFundsPolicyName, global, groups, accounts,
	)
	if err != nil {
		return PolicyConfigurationResult{}, fmt.Errorf(
			"engine: configure spot_funds_pnl_bounds_kill_switch: %w", err,
		)
	}
	blocks, err := policyConfigurationBlockOutcomesFrom(
		outcomes.AccountBlocks, res, domain.PolicySpotFundsPnlBoundsKillSwitch,
	)
	if err != nil {
		return PolicyConfigurationResult{}, err
	}
	return PolicyConfigurationResult{AccountBlocks: blocks}, nil
}

// BlockAccount kill-switches the account in the live engine. Block keeps the
// first recorded reason, so an already-blocked account is unblocked and
// re-blocked to refresh the reason.
func (e *openPitEngine) BlockAccount(
	ctx context.Context, id domain.AccountID, reason string,
) error {
	return e.RunAccountSynchronized(ctx, id, func(lane fwengine.AccountLane) error {
		return lane.BlockAccount(ctx, id, reason)
	})
}

func (l accountLane) BlockAccount(
	ctx context.Context, id domain.AccountID, reason string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: block cancelled: %w", err)
	}

	accountID, err := l.routedAccountID(id)
	if err != nil {
		return fmt.Errorf("engine: block account %q: %w", id, err)
	}
	accounts := l.eng.Accounts()
	accounts.Unblock(accountID)
	accounts.Block(accountID, reason)
	return nil
}

// UnblockAccount lifts the engine block on the account.
func (e *openPitEngine) UnblockAccount(ctx context.Context, id domain.AccountID) error {
	return e.RunAccountSynchronized(ctx, id, func(lane fwengine.AccountLane) error {
		return lane.UnblockAccount(ctx, id)
	})
}

func (l accountLane) UnblockAccount(ctx context.Context, id domain.AccountID) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: unblock cancelled: %w", err)
	}

	accountID, err := l.routedAccountID(id)
	if err != nil {
		return fmt.Errorf("engine: unblock account %q: %w", id, err)
	}
	l.eng.Accounts().Unblock(accountID)
	return nil
}

// SetAccountCurrency sets the account's explicit currency on the live engine.
func (l accountLane) SetAccountCurrency(
	ctx context.Context, id domain.AccountID, currency string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: set account currency cancelled: %w", err)
	}
	accountID, err := l.routedAccountID(id)
	if err != nil {
		return fmt.Errorf("engine: set account currency %q: %w", id, err)
	}
	asset, err := l.owner.res.asset(currency)
	if err != nil {
		return fmt.Errorf("engine: account currency %q: %w", currency, err)
	}
	if err := l.eng.Accounts().SetCurrency(accountID, asset); err != nil {
		return fmt.Errorf("engine: set account currency %q: %w", id, err)
	}
	return nil
}

// ClearAccountCurrency clears the account's explicit currency on the live
// engine.
func (l accountLane) ClearAccountCurrency(ctx context.Context, id domain.AccountID) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: clear account currency cancelled: %w", err)
	}
	accountID, err := l.routedAccountID(id)
	if err != nil {
		return fmt.Errorf("engine: clear account currency %q: %w", id, err)
	}
	l.eng.Accounts().ClearCurrency(accountID)
	return nil
}

// SetAccountPnl force-sets the live SpotFunds account P&L accumulator through
// the SDK's absolute upsert, which also clears a latched halt. The assignment
// can itself kill-switch the account when the value breaches its own barrier,
// so the blocks the SDK reports are mapped back for the node to mirror.
func (l accountLane) SetAccountPnl(
	ctx context.Context, id domain.AccountID, pnl string,
) ([]domain.AccountBlock, error) {
	return l.SetAccountPnlState(ctx, id, pnl, "")
}

// SetAccountPnlState force-sets a numeric or halted SpotFunds account P&L
// state. A halted state carries only its reason; a numeric state carries only
// its decimal value.
func (l accountLane) SetAccountPnlState(
	ctx context.Context,
	id domain.AccountID,
	pnl string,
	haltReason domain.PnlHaltReason,
) ([]domain.AccountBlock, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("engine: set account pnl state cancelled: %w", err)
	}
	accountID, err := l.routedAccountID(id)
	if err != nil {
		return nil, fmt.Errorf("engine: set account pnl state %q: %w", id, err)
	}
	if pnl != "" && haltReason != "" {
		return nil, fmt.Errorf(
			"engine: account pnl state has both value %q and halt reason %q: %w",
			pnl, haltReason, domain.ErrInvalid,
		)
	}
	state, err := pnlStateFrom(pnl, haltReason)
	if err != nil {
		return nil, fmt.Errorf("engine: account pnl state: %w", err)
	}
	configuration, err := l.eng.Configure().SetSpotFundsAccountPnl(
		policies.SpotFundsPolicyName,
		accountID,
		state,
	)
	if err != nil {
		return nil, fmt.Errorf("engine: set account pnl state %q: %w", id, err)
	}
	return policyConfigurationBlocksFrom(
		configuration.AccountBlocks, id, domain.PolicySpotFundsPnlBoundsKillSwitch,
	), nil
}

// ApplyAccountAdjustmentBatch applies one atomic SDK account-adjustment batch
// for account, maps accepted per-asset outcomes back to the request order, and
// surfaces any account block the engine latched while committing the batch.
func (e *openPitEngine) ApplyAccountAdjustmentBatch(
	ctx context.Context, account domain.AccountID, reqs []domain.AdjustmentRequest,
) ([]AdjustmentResult, *AdjustmentBatchReject, error) {
	var results []AdjustmentResult
	var batchReject *AdjustmentBatchReject
	err := e.RunAccountSynchronized(ctx, account, func(lane fwengine.AccountLane) error {
		var err error
		results, batchReject, err = lane.ApplyAccountAdjustmentBatch(ctx, account, reqs)
		return err
	})
	return results, batchReject, err
}

func (l accountLane) ApplyAccountAdjustmentBatch(
	ctx context.Context, account domain.AccountID, reqs []domain.AdjustmentRequest,
) ([]AdjustmentResult, *AdjustmentBatchReject, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("engine: adjustment cancelled: %w", err)
	}
	if len(reqs) == 0 {
		return nil, nil, nil
	}

	accountID, err := l.routedAccountID(account)
	if err != nil {
		return nil, nil, err
	}
	adjustments := make([]model.AccountAdjustment, 0, len(reqs))
	for _, req := range reqs {
		adjustment, err := accountAdjustmentFromRequest(req, l.owner.res)
		if err != nil {
			return nil, nil, err
		}
		adjustments = append(adjustments, adjustment)
	}

	batch, err := l.eng.ApplyAccountAdjustment(accountID, adjustments)
	if err != nil {
		return nil, nil, fmt.Errorf("engine: apply account adjustment: %w", err)
	}
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
	results := make([]AdjustmentResult, 0, len(reqs))
	for _, req := range reqs {
		result := AdjustmentResult{AccountBlocks: blocks}
		accepted, ok, err := outcomeAcceptedFromList(outcomes, req.Asset, l.owner.res)
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

// ApplyAccountAdjustment applies one spot-funds adjustment for account and
// returns its accept/reject outcome.
func (e *openPitEngine) ApplyAccountAdjustment(
	ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (AdjustmentResult, error) {
	var result AdjustmentResult
	err := e.RunAccountSynchronized(ctx, account, func(lane fwengine.AccountLane) error {
		var err error
		result, err = lane.ApplyAccountAdjustment(ctx, account, req)
		return err
	})
	return result, err
}

func (l accountLane) ApplyAccountAdjustment(
	ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (AdjustmentResult, error) {
	results, batchReject, err := l.ApplyAccountAdjustmentBatch(
		ctx, account, []domain.AdjustmentRequest{req},
	)
	if err != nil {
		return AdjustmentResult{}, err
	}
	if batchReject != nil {
		return AdjustmentResult{Rejected: batchReject}, nil
	}
	if len(results) == 0 {
		return AdjustmentResult{}, nil
	}
	if len(results) != 1 {
		return AdjustmentResult{}, fmt.Errorf(
			"engine: adjustment returned %d outcomes", len(results),
		)
	}
	return results[0], nil
}

// SubmitOrder runs the pre-trade pipeline for o. On accept it serializes the
// reservation lock through the lock seam before committing the reservation; the
// native reservation is committed-and-closed (or rolled back on a capture error)
// and never escapes the adapter.
func (e *openPitEngine) SubmitOrder(
	ctx context.Context, o domain.Order,
) (OrderResult, error) {
	var result OrderResult
	err := e.RunAccountSynchronized(ctx, o.Account, func(lane fwengine.AccountLane) error {
		var err error
		result, err = lane.SubmitOrder(ctx, o)
		return err
	})
	return result, err
}

func (l accountLane) SubmitOrder(ctx context.Context, o domain.Order) (OrderResult, error) {
	if err := ctx.Err(); err != nil {
		return OrderResult{}, fmt.Errorf("engine: submit order cancelled: %w", err)
	}

	accountID, err := l.routedAccountID(o.Account)
	if err != nil {
		return OrderResult{}, err
	}
	order, err := orderModelFromAccount(o, accountID, l.owner.res)
	if err != nil {
		return OrderResult{}, err
	}

	if o.DropCopy {
		return l.submitDropCopyOrder(o, order)
	}

	reservation, rejects, err := l.eng.ExecutePreTrade(order)
	if err != nil {
		return OrderResult{}, wrapPreTradeError(err)
	}
	if rejects != nil {
		return OrderResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}

	lock, err := reservation.Lock()
	if err != nil {
		reservation.RollbackAndClose()
		return OrderResult{}, fmt.Errorf("engine: read reservation lock: %w", err)
	}
	lockBytes, settlement, err := capturePreTradeOutput(lock, o)
	if err != nil {
		reservation.RollbackAndClose()
		return OrderResult{}, err
	}
	// Capture the reservation's balance effects (held funds, incoming quantity)
	// before closing, so the caller can mirror them into the balance snapshot.
	adjustments, err := reservation.AccountAdjustments()
	if err != nil {
		reservation.RollbackAndClose()
		return OrderResult{}, err
	}
	outcomes, err := balanceOutcomesFromList(adjustments, l.owner.res)
	if err != nil {
		reservation.RollbackAndClose()
		return OrderResult{}, err
	}
	reservation.CommitAndClose()
	return OrderResult{
		Accepted:            true,
		Lock:                lockBytes,
		Outcomes:            outcomes,
		SettlementLockPrice: settlement,
	}, nil
}

// submitDropCopyOrder records a historical order through the drop-copy rollback
// window: the same shape as the regular reservation branch above. Everything
// Officer must persist is derived while the applied mutations can still be
// compensated, and the commit - the point of no return - happens only once no
// derivation is left that could fail.
func (l accountLane) submitDropCopyOrder(
	o domain.Order, order model.Order,
) (OrderResult, error) {
	operation, rejects, err := l.eng.ApplyDropCopy(order)
	if err != nil {
		return OrderResult{}, wrapPreTradeError(err)
	}
	if rejects != nil {
		return OrderResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}
	if operation == nil {
		return OrderResult{}, fmt.Errorf(
			"engine: drop-copy returned neither an operation nor rejects",
		)
	}
	return finalizeDropCopy(operation, func() (OrderResult, error) {
		lock, err := operation.Lock()
		if err != nil {
			return OrderResult{}, fmt.Errorf(
				"engine: read drop-copy lock: %w", err,
			)
		}
		lockBytes, settlement, err := capturePreTradeOutput(lock, o)
		if err != nil {
			return OrderResult{}, err
		}
		adjustments, err := operation.AccountAdjustments()
		if err != nil {
			return OrderResult{}, err
		}
		outcomes, err := balanceOutcomesFromList(adjustments, l.owner.res)
		if err != nil {
			return OrderResult{}, err
		}
		block, err := operation.AccountBlock()
		if err != nil {
			return OrderResult{}, err
		}
		var blocks []reject.AccountBlock
		if block != nil {
			blocks = append(blocks, *block)
		}
		return OrderResult{
			Accepted:            true,
			Lock:                lockBytes,
			Blocks:              executionBlocksFrom(blocks, o.Account),
			Outcomes:            outcomes,
			SettlementLockPrice: settlement,
		}, nil
	})
}

// finalizeDropCopy commits only a fully materialized operation. On a derivation
// error it compensates every applied mutation before releasing the native
// handle. Rollback is intentionally void in the SDK: a callback failure arms
// the engine-wide kill switch and appears on the next pre-trade call as
// SystemUnavailable. The SDK exposes neither a finalizer result nor a live
// kill-switch query, so Officer cannot mirror that block here without guessing.
func finalizeDropCopy[Result any](
	operation *pretrade.DropCopyOperation,
	materialize func() (Result, error),
) (Result, error) {
	defer operation.Close()
	result, err := materialize()
	if err != nil {
		operation.Rollback()
		var zero Result
		return zero, err
	}
	operation.Commit()
	return result, nil
}

func wrapPreTradeError(err error) error {
	return fmt.Errorf("engine: execute pre-trade: %w", err)
}

func (e *openPitEngine) RunAccountSynchronized(
	ctx context.Context, account domain.AccountID, fn func(fwengine.AccountLane) error,
) error {
	accountID, err := e.res.account(account)
	if err != nil {
		return err
	}
	e.mu.RLock()
	eng := e.eng
	async := e.async
	running := e.running
	e.mu.RUnlock()
	if !running || async == nil || eng == nil {
		return fmt.Errorf("engine: account-synchronized mutation on stopped engine")
	}
	lane := accountLane{
		owner: e, eng: eng, accountAlias: account, accountID: accountID,
	}
	future := async.Submit(ctx, accountID, func() error { return fn(lane) })
	_, err = future.Await(context.Background())
	return err
}

func (e *openPitEngine) RunGroupSynchronized(
	ctx context.Context, groupID string, fn func(fwengine.GroupLane) error,
) error {
	group := param.DefaultAccountGroup
	if groupID != "" {
		var err error
		group, err = e.res.group(groupID)
		if err != nil {
			return err
		}
	}
	e.mu.RLock()
	eng := e.eng
	async := e.async
	running := e.running
	e.mu.RUnlock()
	if !running || async == nil || eng == nil {
		return fmt.Errorf("engine: group-synchronized mutation on stopped engine")
	}
	lane := groupLane{
		owner: e, eng: eng,
		accountIDs: e.res.accountIDSnapshot(),
		groupAlias: groupID,
		groupID:    group,
	}
	future := async.Submit(ctx, groupRoutingKey(group), func() error { return fn(lane) })
	_, err := future.Await(context.Background())
	return err
}

func groupRoutingKey(group param.AccountGroupID) param.AccountID {
	const groupRoutingKeyMask = uint64(1) << 63
	return param.NewAccountIDFromUint64(groupRoutingKeyMask | uint64(group.Handle()))
}

func (l accountLane) routedAccountID(alias domain.AccountID) (param.AccountID, error) {
	if alias == l.accountAlias {
		return l.accountID, nil
	}
	resolved, err := l.owner.res.account(alias)
	if err != nil {
		return param.AccountID{}, err
	}
	if resolved.Handle() != l.accountID.Handle() {
		return param.AccountID{}, fmt.Errorf(
			"engine: account lane routed alias %q to id %s, argument %q resolves to %s: %w",
			l.accountAlias, l.accountID, alias, resolved, domain.ErrInvalid,
		)
	}
	return l.accountID, nil
}

func (l groupLane) routedGroupID(alias string) (param.AccountGroupID, error) {
	if alias == l.groupAlias {
		return l.groupID, nil
	}
	resolved, err := l.owner.res.group(alias)
	if err != nil {
		return param.AccountGroupID{}, err
	}
	if resolved.Handle() != l.groupID.Handle() {
		return param.AccountGroupID{}, fmt.Errorf(
			"engine: group lane routed alias %q to id %s, argument %q resolves to %s: %w",
			l.groupAlias, l.groupID, alias, resolved, domain.ErrInvalid,
		)
	}
	return l.groupID, nil
}

func (l groupLane) routedAccountIDs(
	aliases []domain.AccountID,
) ([]param.AccountID, error) {
	ids := make([]param.AccountID, 0, len(aliases))
	for _, alias := range aliases {
		id, ok := l.accountIDs[alias]
		if !ok {
			return nil, unknownAccountAliasError(alias)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ApplyExecutionReport settles a report and returns the engine-owned persistence
// persistence plus any adjustment outcomes. Blocks are stamped with the report's
// account, since the binding's block record does not name the account.
func (e *openPitEngine) ApplyExecutionReport(
	ctx context.Context, in domain.ExecutionReportInput, leavesQuantity string,
) (ExecutionReportResult, error) {
	var result ExecutionReportResult
	err := e.RunAccountSynchronized(ctx, in.Account, func(lane fwengine.AccountLane) error {
		var err error
		result, err = lane.ApplyExecutionReport(ctx, in, leavesQuantity)
		return err
	})
	return result, err
}

func (l accountLane) ApplyExecutionReport(
	ctx context.Context, in domain.ExecutionReportInput, leavesQuantity string,
) (ExecutionReportResult, error) {
	accountID, err := l.routedAccountID(in.Account)
	if err != nil {
		return ExecutionReportResult{}, err
	}
	report, err := executionReportFromAccount(in, accountID, leavesQuantity, l.owner.res)
	if err != nil {
		return ExecutionReportResult{}, err
	}

	result, err := l.eng.ApplyExecutionReport(report)
	if err != nil {
		return ExecutionReportResult{}, fmt.Errorf("engine: apply execution report: %w", err)
	}

	blocks := executionBlocksFrom(result.AccountBlocks, in.Account)
	outcomes, err := balanceOutcomesFromList(result.AccountAdjustments, l.owner.res)
	if err != nil {
		return ExecutionReportResult{}, err
	}
	accountPnl, accountPnlHaltReason, err := spotFundsAccountPnlFromList(
		accountID,
		result.AccountPnls,
	)
	if err != nil {
		return ExecutionReportResult{}, err
	}
	persistence := executionReportPersistenceFrom(
		in,
		blocks,
		outcomes,
		accountPnl,
		accountPnlHaltReason,
	)
	return ExecutionReportResult{
		Persistence: &persistence,
		Blocks:      blocks,
		Outcomes:    outcomes,
	}, nil
}

func (l groupLane) RegisterGroup(
	ctx context.Context, accounts []domain.AccountID, groupID string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: register group cancelled: %w", err)
	}

	ids, err := l.routedAccountIDs(accounts)
	if err != nil {
		return err
	}
	group, err := l.routedGroupID(groupID)
	if err != nil {
		return err
	}
	if err := l.eng.Accounts().RegisterGroup(ids, group); err != nil {
		return fmt.Errorf("engine: register group %q: %w", groupID, err)
	}
	return nil
}

func (l groupLane) UnregisterGroup(
	ctx context.Context, accounts []domain.AccountID, groupID string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: unregister group cancelled: %w", err)
	}

	ids, err := l.routedAccountIDs(accounts)
	if err != nil {
		return err
	}
	group, err := l.routedGroupID(groupID)
	if err != nil {
		return err
	}
	if err := l.eng.Accounts().UnregisterGroup(ids, group); err != nil {
		return fmt.Errorf("engine: unregister group %q: %w", groupID, err)
	}
	return nil
}

func (l groupLane) BlockGroup(ctx context.Context, groupID, reason string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: block group cancelled: %w", err)
	}

	group, err := l.routedGroupID(groupID)
	if err != nil {
		return err
	}
	accounts := l.eng.Accounts()
	// Block keeps the first recorded reason, so the reason is refreshed through
	// ReplaceGroupBlockReason instead of an unblock/re-block pair: group work runs
	// on its own lane, concurrently with the orders of its member accounts, so
	// lifting the block would let an order pass the group tier unblocked.
	if err := accounts.BlockGroup(group, reason); err != nil {
		return groupBlockError("block group", groupID, err)
	}
	if err := accounts.ReplaceGroupBlockReason(group, reason); err != nil {
		return groupBlockError("block group", groupID, err)
	}
	return nil
}

// groupBlockError translates an engine account-block failure. The engine keeps
// its default group reserved and refuses to block, unblock, or re-reason it, so
// that refusal is surfaced as the typed reserved-group error instead of an
// opaque internal failure.
func groupBlockError(op, groupID string, err error) error {
	var blockErr *reject.AccountBlockError
	if errors.As(err, &blockErr) &&
		blockErr.Kind == reject.AccountBlockErrorKindReservedGroup {
		// Both errors stay in the chain: callers match the sentinel, logs keep the
		// engine's own diagnostic.
		return fmt.Errorf(
			"engine: %s %q: %w: %w", op, groupID, err, domain.ErrReservedGroup,
		)
	}
	return fmt.Errorf("engine: %s %q: %w", op, groupID, err)
}

func (l groupLane) UnblockGroup(ctx context.Context, groupID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: unblock group cancelled: %w", err)
	}

	group, err := l.routedGroupID(groupID)
	if err != nil {
		return err
	}
	if err := l.eng.Accounts().UnblockGroup(group); err != nil {
		return groupBlockError("unblock group", groupID, err)
	}
	return nil
}

func (l groupLane) SetGroupCurrency(
	ctx context.Context, groupID, currency string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: set group currency cancelled: %w", err)
	}

	group, err := l.routedGroupID(groupID)
	if err != nil {
		return err
	}
	asset, err := l.owner.res.asset(currency)
	if err != nil {
		return fmt.Errorf("engine: group %q currency %q: %w", groupID, currency, err)
	}
	if err := l.eng.Accounts().SetGroupCurrency(group, asset); err != nil {
		return fmt.Errorf("engine: set group %q currency: %w", groupID, err)
	}
	return nil
}

func (l groupLane) ClearGroupCurrency(ctx context.Context, groupID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: clear group currency cancelled: %w", err)
	}

	group, err := l.routedGroupID(groupID)
	if err != nil {
		return err
	}
	l.eng.Accounts().ClearGroupCurrency(group)
	return nil
}

// CheckOrder runs the pre-trade pipeline for probe as a non-mutating dry-run.
// It builds the same model.Order as SubmitOrder and runs ExecutePreTradeDryRun,
// which evaluates every policy but commits nothing: no reservation is taken and
// no account state changes. On pass it reads the would-be settlement price
// directly from the live lock; on reject it returns the engine
// rejects plus the account block the engine would record. The dry-run report
// owns native memory and never escapes the adapter.
func (e *openPitEngine) CheckOrder(
	ctx context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	var result domain.CheckResult
	err := e.RunAccountSynchronized(ctx, probe.Account, func(lane fwengine.AccountLane) error {
		var err error
		result, err = lane.CheckOrder(ctx, probe)
		return err
	})
	return result, err
}

func (l accountLane) CheckOrder(
	ctx context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.CheckResult{}, fmt.Errorf("engine: check order cancelled: %w", err)
	}

	accountID, err := l.routedAccountID(probe.Account)
	if err != nil {
		return domain.CheckResult{}, err
	}
	order, err := orderModelFromAccount(domain.Order{
		Account:     probe.Account,
		BaseAsset:   probe.BaseAsset,
		QuoteAsset:  probe.QuoteAsset,
		Side:        probe.Side,
		AmountKind:  probe.AmountKind,
		AmountValue: probe.AmountValue,
		Price:       probe.Price,
	}, accountID, l.owner.res)
	if err != nil {
		return domain.CheckResult{}, err
	}

	report, err := l.eng.ExecutePreTradeDryRun(order)
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("engine: execute pre-trade dry-run: %w", err)
	}
	defer report.Close()

	passed, err := report.IsPass()
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("engine: read dry-run verdict: %w", err)
	}
	if !passed {
		rejects, err := report.Rejects()
		if err != nil {
			return domain.CheckResult{}, fmt.Errorf(
				"engine: read dry-run rejects: %w", err,
			)
		}
		block, err := report.AccountBlock()
		if err != nil {
			return domain.CheckResult{}, fmt.Errorf(
				"engine: read dry-run account block: %w", err,
			)
		}
		return domain.CheckResult{
			Passed:     false,
			Rejects:    orderRejectsFrom(rejects),
			WouldBlock: accountBlockFrom(block, probe.Account),
		}, nil
	}

	lock, err := report.Lock()
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("engine: read dry-run lock: %w", err)
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
		_ = async.StopGraceful(context.Background())
	}

	e.mu.Lock()
	eng := e.eng
	e.async = nil
	e.eng = nil
	e.mu.Unlock()

	if eng != nil {
		eng.Stop()
	}
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
// Persisted account P&L state is applied after Build through AsyncEngine lanes.
// Runtime P&L-bounds changes only replace barrier axes; they never reset or
// seed the live accumulator. Operator-configurable slippage and runtime
// enable/disable remain future work.
func buildEngine(
	snap Snapshot, res idResolver, service *bindmd.Service,
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
	var buildBlocks []domain.AccountBlock
	if len(snap.SpotFundsPnlBoundsLimits) > 0 {
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
		configuration, err := configureSpotFundsPnlBounds(
			eng, res, snap.SpotFundsPnlBoundsLimits,
		)
		if err != nil {
			return releaseOnErr(err)
		}
		buildBlocks = configuration.AccountBlocks
	}
	return eng, service, registered, buildBlocks, created, nil
}

// applyBlocks blocks every blocked account in accounts on the engine with its
// persisted reason, addressing each by its stored engine account id.
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
		handle.Block(accountID, account.BlockReason)
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
	eng *openpit.Engine,
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
		var seedBlocks []domain.AccountBlock
		future := async.Submit(context.Background(), seed.account, func() error {
			configuration, err := eng.Configure().SetSpotFundsAccountPnl(
				policies.SpotFundsPolicyName,
				seed.account,
				seed.state,
			)
			if err != nil {
				return err
			}
			seedBlocks = policyConfigurationBlocksFrom(
				configuration.AccountBlocks,
				seed.code,
				domain.PolicySpotFundsPnlBoundsKillSwitch,
			)
			return nil
		})
		_, err := future.Await(context.Background())
		if err != nil {
			return nil, fmt.Errorf("engine: seed spot funds account pnl: %w", err)
		}
		blocks = append(blocks, seedBlocks...)
	}
	return blocks, nil
}

// balanceSeedAdjustment builds the absolute adjustment that seeds one stored
// balance: available/held/incoming set absolutely when the persisted value is
// non-empty, left nil (not set) when empty. A nil field is the correct "absent"
// signal to the adjustment-values mapper (adjustmentAmountValues treats a nil
// pointer as not set, so the binding never sees an empty string). This guards
// against legacy rows where INSERT OR REPLACE wrote "" before the orZero write
// chokepoint was added.
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
