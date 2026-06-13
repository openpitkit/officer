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

package engine

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.openpit.dev/openpit"
	bindmd "go.openpit.dev/openpit/marketdata"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade/policies"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/marketdata"
)

// defaultQuoteTTL is the service-wide quote lifetime the engine's market-data
// service is built with. Risk-grade freshness (seconds-fresh): a quote older
// than this reads as absent. Making it configurable is planned.
var defaultQuoteTTL = bindmd.WithinTTL(10 * time.Second)

// envRuntimeLibraryPath is the binding environment variable that pins the
// native runtime library to an explicit pre-extracted path, bypassing the
// binding's own extraction. Pit Officer sets it from
// config.Config.RuntimeLibraryPath before the first binding call so operators
// can run against a known dylib.
const envRuntimeLibraryPath = "OPENPIT_RUNTIME_LIBRARY_PATH"

// Registered policy names. The built-in risk policies register under these
// fixed names; the Configure surface targets a policy by name.
const (
	nameRateLimit           = policies.RateLimitPolicyName
	nameOrderSizeLimit      = policies.OrderSizeLimitPolicyName
	namePnlBoundsKillSwitch = policies.PnlBoundsKillSwitchPolicyName
	nameSpotFunds           = policies.SpotFundsPolicyName
)

// openPitEngine is the concrete Engine adapter wrapping one OpenPit engine
// handle and its market-data service.
//
// The handle is built by BuildOpenPitEngine and lives until Stop; officer never
// rebuilds it. Limit changes are applied dynamically through the binding's
// Configure surface. rate_limit, order_size_limit, and pnl_bounds_kill_switch
// retune their axes wholesale (barriers added and removed at runtime). The
// spot-funds policy is registered with its default settings and is not
// reconfigured at runtime. The residual changes the Configure surface cannot
// express are exactly: configuring a policy that was not registered at build
// time (no runtime registration API), removing the last barrier of a registered
// policy (the SDK cannot unregister), and dropping a broker barrier of
// rate_limit or order_size while other barriers remain (broker is an Option the
// surface cannot clear in isolation). Each returns an error wrapping
// domain.ErrNotImplemented, which the node surfaces to its caller; closing such
// a gap is engine-side SDK work, not an officer rebuild.
//
// The adapter owns the market-data service for the whole process life (the
// engine is never rebuilt, so the service handle is stable). The service is
// built unconditionally and Stop closes it after stopping the engine, so
// connector producers must stop before Stop.
//
// The adapter tracks the minimal state this requires: the set of policies
// registered at build time, and per-policy whether a broker barrier is currently
// live (for the broker-drop guard), all updated only on a successful call. All
// handle access and this state are serialized behind mu: the mu gives
// store+engine+audit atomicity across multi-step operations; the engine itself
// is built FullSync so individual binding calls are safe under goroutine
// migration across OS threads (Go does not pin goroutines, making NoSync unsafe
// in this environment).
type openPitEngine struct {
	eng *openpit.Engine

	// sink publishes normalized quotes into marketDataService. It owns the
	// first-sight register cache and is safe for concurrent Push (the connector
	// manager drains multiple feeds into it).
	sink *marketDataSink
	// marketDataService is the engine's quote registry, built before the engine
	// and closed by Stop after the engine stops.
	marketDataService *bindmd.Service

	// registered is the set of policy names live on the handle. ConfigurePolicy
	// of an unregistered policy is a not-implemented stub: there is no runtime
	// policy-registration API.
	registered map[string]struct{}
	// brokerPresent reports, per policy name, whether that policy currently
	// carries a broker barrier on the handle. Only rate_limit and order_size_limit
	// have a broker axis the Configure surface cannot clear in isolation (a nil
	// broker leaves it unchanged), so a reconfigure that drops the broker barrier
	// while other barriers remain is a not-implemented stub rather than a silent
	// store/engine divergence. Updated only on a successful Configure.
	brokerPresent map[string]bool

	mu      sync.Mutex
	running bool
}

// newOpenPitEngine wraps an already-built *openpit.Engine and its market-data
// service into the Engine adapter. registered is the set of policy names live on
// the handle and brokerPresent records, per policy, whether it was built with a
// broker barrier; both are taken over by the adapter. The adapter takes
// ownership of both the handle and the service lifecycle: neither must be
// stopped/closed directly afterwards; use Engine.Stop instead, which stops the
// engine and then closes the service. BuildOpenPitEngine is the only caller; it
// assembles the tracked state and owns the service before the engine is built.
func newOpenPitEngine(
	eng *openpit.Engine,
	service *bindmd.Service,
	registered map[string]struct{},
	brokerPresent map[string]bool,
) Engine {
	if registered == nil {
		registered = make(map[string]struct{})
	}
	if brokerPresent == nil {
		brokerPresent = make(map[string]bool)
	}
	return &openPitEngine{
		eng:               eng,
		sink:              newMarketDataSink(service),
		marketDataService: service,
		registered:        registered,
		brokerPresent:     brokerPresent,
		running:           eng != nil,
	}
}

// BuildOpenPitEngine builds the one stage-2 OpenPit engine plus its market-data
// service and returns them wrapped as an Engine. The engine uses the FullSync
// mode so that binding calls are safe under goroutine migration: Go does not
// guarantee that a goroutine stays on the OS thread that built the handle, so
// NoSync would fault intermittently on the multi-cgo SubmitOrder path. It always
// registers the built-in order-validation policy, and registers each risk policy
// that has at least one barrier in snap.Limits (reusing the mapping.go ready
// builders). Blocked accounts from snap.Accounts are then applied with their
// persisted reasons.
//
// The handle and the market-data service live until Stop. Officer builds the
// engine exactly once and never rebuilds it: a change the runtime Configure
// surface cannot express is an SDK gap, surfaced as an error, not
// a trigger to call BuildOpenPitEngine again. On any seeding failure both the
// engine and the already-built service are released so no native resource leaks.
//
// runtimeLibraryPath, when non-empty, is exported as
// OPENPIT_RUNTIME_LIBRARY_PATH before the engine is built so the binding loads
// that explicit native runtime instead of extracting its embedded copy. It must
// be set before any other binding call in the process; BuildOpenPitEngine is
// the binding entry point, so setting it here is sufficient.
func BuildOpenPitEngine(runtimeLibraryPath string, snap Snapshot) (Engine, error) {
	if runtimeLibraryPath != "" {
		if err := os.Setenv(envRuntimeLibraryPath, runtimeLibraryPath); err != nil {
			return nil, fmt.Errorf("engine: set %s: %w", envRuntimeLibraryPath, err)
		}
	}

	byPolicy := make(map[string][]domain.Limit)
	for _, limit := range snap.Limits {
		policy := limit.Target.Policy
		byPolicy[policy] = append(byPolicy[policy], limit)
	}

	eng, service, registered, err := buildEngine(byPolicy)
	if err != nil {
		return nil, err
	}

	// On any seeding failure stop the engine and close the service: the adapter
	// is not yet constructed, so its Stop would not run.
	releaseOnErr := func(err error) (Engine, error) {
		eng.Stop()
		service.Close()
		return nil, err
	}

	if err := applyBlocks(eng, snap.Accounts); err != nil {
		return releaseOnErr(err)
	}
	if err := hydrateGroups(eng, snap.Accounts); err != nil {
		return releaseOnErr(err)
	}
	if err := blockGroups(eng, snap.Groups); err != nil {
		return releaseOnErr(err)
	}
	if err := seedBalances(eng, snap.Balances); err != nil {
		return releaseOnErr(err)
	}

	brokerPresent := map[string]bool{
		nameRateLimit:      hasBrokerBarrier(byPolicy[domain.PolicyRateLimit]),
		nameOrderSizeLimit: hasBrokerBarrier(byPolicy[domain.PolicyOrderSizeLimit]),
	}
	return newOpenPitEngine(eng, service, registered, brokerPresent), nil
}

// hasBrokerBarrier reports whether a barrier set contains a broker-scoped
// barrier.
func hasBrokerBarrier(limits []domain.Limit) bool {
	for _, limit := range limits {
		if limit.Target.Scope == domain.ScopeBroker {
			return true
		}
	}
	return false
}

// Version returns the engine SDK/runtime version string.
func (e *openPitEngine) Version() string { return openpit.GetVersion() }

// BuildProfile returns the engine build profile string.
func (e *openPitEngine) BuildProfile() string { return openpit.GetBuildProfile() }

// Running reports whether the engine handle is live (built and not stopped).
func (e *openPitEngine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// ConfigurePolicy applies the change to the live handle via the Configure
// surface. limits is the complete barrier set for policy.
//
// rate_limit / order_size_limit / pnl_bounds_kill_switch: the full axes are
// rebuilt from the complete barrier set and replaced in one Configure call;
// barriers are added and removed at runtime. rate_limit and order_size_limit
// additionally return a not-implemented stub when the reconfigure would drop a
// broker barrier the Configure surface cannot clear in isolation. Configuring an
// unregistered policy, or removing the last barrier of a registered policy,
// returns a not-implemented stub. The tracked state is updated only on success.
func (e *openPitEngine) ConfigurePolicy(
	ctx context.Context, policy string, limits []domain.Limit,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: configure cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return fmt.Errorf("engine: configure on stopped engine")
	}

	if _, ok := e.registered[policyName(policy)]; !ok {
		return fmt.Errorf(
			"engine: cannot configure unregistered policy %q: runtime policy "+
				"registration is not supported by the SDK yet: %w",
			policy, domain.ErrNotImplemented)
	}
	if len(limits) == 0 {
		return fmt.Errorf(
			"engine: cannot remove the last barrier of policy %q: empty policy "+
				"settings are not supported by the SDK yet: %w",
			policy, domain.ErrNotImplemented)
	}

	switch policy {
	case domain.PolicyRateLimit:
		return e.configureRateLimitLocked(limits)
	case domain.PolicyOrderSizeLimit:
		return e.configureOrderSizeLocked(limits)
	case domain.PolicyPnlBoundsKillSwitch:
		return e.configurePnlBoundsLocked(limits)
	default:
		return fmt.Errorf("engine: configure unknown policy %q", policy)
	}
}

// configureRateLimitLocked replaces the rate-limit axes on the live handle from
// the complete barrier set. The asset/account/account-asset axes are passed as
// non-nil (possibly empty) slices so each is replaced wholesale; barriers are
// added and removed at runtime and a surviving key keeps its live counter. The
// broker axis is a pointer (nil = unchanged), so dropping the broker barrier
// while other barriers remain is a not-implemented stub - the Configure surface
// cannot clear it in isolation, and silently leaving the old broker barrier on
// the handle would diverge the engine from the store. Callers must hold e.mu.
func (e *openPitEngine) configureRateLimitLocked(limits []domain.Limit) error {
	nextBroker := hasBrokerBarrier(limits)
	if e.brokerPresent[nameRateLimit] && !nextBroker {
		return fmt.Errorf(
			"engine: cannot remove a rate_limit broker barrier: the SDK's "+
				"rate-limit configure cannot clear a broker barrier in isolation: %w",
			domain.ErrNotImplemented)
	}

	broker, assets, accounts, accountAssets, err := rateLimitAxes(limits)
	if err != nil {
		return err
	}
	if err := e.eng.Configure().RateLimit(
		nameRateLimit, broker, assets, accounts, accountAssets,
	); err != nil {
		return fmt.Errorf("engine: configure rate_limit: %w", err)
	}
	e.brokerPresent[nameRateLimit] = nextBroker
	return nil
}

// configureOrderSizeLocked replaces the order-size axes on the live handle from
// the complete barrier set. Empty asset/account-asset axes are passed as empty
// non-nil slices so they are cleared rather than left unchanged. The Configure
// surface cannot clear a broker barrier in isolation (a nil broker leaves it
// unchanged), so dropping the broker barrier while other barriers remain is a
// not-implemented stub - silently leaving the old broker barrier on the handle
// would diverge the engine from the store. Callers must hold e.mu.
func (e *openPitEngine) configureOrderSizeLocked(limits []domain.Limit) error {
	nextBroker := hasBrokerBarrier(limits)
	if e.brokerPresent[nameOrderSizeLimit] && !nextBroker {
		return fmt.Errorf(
			"engine: cannot remove an order_size_limit broker barrier: the SDK's "+
				"order-size configure cannot clear a broker barrier in isolation: %w",
			domain.ErrNotImplemented)
	}

	broker, assets, accountAssets, err := orderSizeAxes(limits)
	if err != nil {
		return err
	}
	if err := e.eng.Configure().OrderSizeLimit(
		nameOrderSizeLimit, broker, assets, accountAssets,
	); err != nil {
		return fmt.Errorf("engine: configure order_size_limit: %w", err)
	}
	e.brokerPresent[nameOrderSizeLimit] = nextBroker
	return nil
}

// configurePnlBoundsLocked replaces the P&L bounds axes on the live handle from
// the complete barrier set. Empty axes are passed as empty non-nil slices so
// they are cleared rather than left unchanged. The account axis uses the Update
// shape: it retunes bounds without resetting the live accumulated P&L. Callers
// must hold e.mu.
func (e *openPitEngine) configurePnlBoundsLocked(limits []domain.Limit) error {
	brokers, accounts, err := pnlBoundsAxes(limits)
	if err != nil {
		return err
	}
	if err := e.eng.Configure().PnlBoundsKillSwitch(
		namePnlBoundsKillSwitch, brokers, accounts,
	); err != nil {
		return fmt.Errorf("engine: configure pnl_bounds_kill_switch: %w", err)
	}
	return nil
}

// BlockAccount kill-switches the account in the live engine. Block keeps the
// first recorded reason, so an already-blocked account is unblocked and
// re-blocked to refresh the reason.
func (e *openPitEngine) BlockAccount(
	ctx context.Context, id domain.AccountID, reason string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: block cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return fmt.Errorf("engine: block on stopped engine")
	}

	accountID, err := param.NewAccountIDFromString(id.String())
	if err != nil {
		return fmt.Errorf("engine: block account %q: %w", id, err)
	}
	accounts := e.eng.Accounts()
	accounts.Unblock(accountID)
	accounts.Block(accountID, reason)
	return nil
}

// UnblockAccount lifts the engine block on the account.
func (e *openPitEngine) UnblockAccount(ctx context.Context, id domain.AccountID) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: unblock cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return fmt.Errorf("engine: unblock on stopped engine")
	}

	accountID, err := param.NewAccountIDFromString(id.String())
	if err != nil {
		return fmt.Errorf("engine: unblock account %q: %w", id, err)
	}
	e.eng.Accounts().Unblock(accountID)
	return nil
}

// ApplyAccountAdjustment applies one spot-funds adjustment for account and
// returns its accept/reject outcome. The request maps to one balance operation
// on a single asset, so the outcome reads from that asset's entry.
func (e *openPitEngine) ApplyAccountAdjustment(
	ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (AdjustmentResult, error) {
	if err := ctx.Err(); err != nil {
		return AdjustmentResult{}, fmt.Errorf("engine: adjustment cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return AdjustmentResult{}, fmt.Errorf("engine: adjustment on stopped engine")
	}

	accountID, err := newAccountID(account)
	if err != nil {
		return AdjustmentResult{}, err
	}
	adjustment, err := accountAdjustmentFromRequest(req)
	if err != nil {
		return AdjustmentResult{}, err
	}

	batch, outcomes, err := e.eng.ApplyAccountAdjustment(
		accountID, []model.AccountAdjustment{adjustment})
	if err != nil {
		return AdjustmentResult{}, fmt.Errorf("engine: apply account adjustment: %w", err)
	}
	if rej, ok := batch.Get(); ok {
		rejected := outcomeRejectedFrom(rej)
		return AdjustmentResult{Rejected: &rejected}, nil
	}
	accepted := outcomeAcceptedFromList(outcomes, req.Asset)
	return AdjustmentResult{Accepted: &accepted}, nil
}

// SubmitOrder runs the pre-trade pipeline for o. On accept it captures the
// reservation lock prices as decimal strings before committing the reservation;
// the native reservation is committed-and-closed (or rolled back on a capture
// error) and never escapes the adapter.
func (e *openPitEngine) SubmitOrder(
	ctx context.Context, o domain.Order,
) (OrderResult, error) {
	if err := ctx.Err(); err != nil {
		return OrderResult{}, fmt.Errorf("engine: submit order cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return OrderResult{}, fmt.Errorf("engine: submit order on stopped engine")
	}

	order, err := orderModelFrom(o)
	if err != nil {
		return OrderResult{}, err
	}

	reservation, rejects, err := e.eng.ExecutePreTrade(order)
	if err != nil {
		return OrderResult{}, fmt.Errorf("engine: execute pre-trade: %w", err)
	}
	if rejects != nil {
		return OrderResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}

	// Capture lock prices as decimal strings before closing; the reservation
	// must not outlive this call.
	prices, err := reservation.Lock().Prices()
	if err != nil {
		reservation.RollbackAndClose()
		return OrderResult{}, fmt.Errorf("engine: read reservation lock: %w", err)
	}
	lockPrices := make([]string, 0, len(prices))
	for _, price := range prices {
		lockPrices = append(lockPrices, price.String())
	}
	reservation.CommitAndClose()
	return OrderResult{Accepted: true, LockPrices: lockPrices}, nil
}

// ApplyExecutionReport settles a fill and returns the account blocks the engine
// recorded plus any adjustment outcomes. Blocks are stamped with the report's
// account, since the binding's block record does not name the account.
func (e *openPitEngine) ApplyExecutionReport(
	ctx context.Context, in domain.ExecutionReportInput,
) (ExecutionReportResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionReportResult{}, fmt.Errorf("engine: execution report cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return ExecutionReportResult{}, fmt.Errorf("engine: execution report on stopped engine")
	}

	report, err := executionReportFrom(in)
	if err != nil {
		return ExecutionReportResult{}, err
	}

	result, err := e.eng.ApplyExecutionReport(report)
	if err != nil {
		return ExecutionReportResult{}, fmt.Errorf("engine: apply execution report: %w", err)
	}

	outcomes := make([]domain.AdjustmentOutcomeAccepted, 0, len(result.AccountAdjustmentOutcomes))
	if len(result.AccountAdjustmentOutcomes) > 0 {
		outcomes = append(outcomes, outcomeAcceptedFromList(
			result.AccountAdjustmentOutcomes, in.BaseAsset))
	}
	return ExecutionReportResult{
		Blocks:   executionBlocksFrom(result.AccountBlocks, in.Account),
		Outcomes: outcomes,
	}, nil
}

// RegisterGroup atomically registers accounts into groupID on the live engine.
func (e *openPitEngine) RegisterGroup(
	ctx context.Context, accounts []domain.AccountID, groupID string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: register group cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return fmt.Errorf("engine: register group on stopped engine")
	}

	ids, err := accountIDs(accounts)
	if err != nil {
		return err
	}
	group, err := newAccountGroupID(groupID)
	if err != nil {
		return err
	}
	if err := e.eng.Accounts().RegisterGroup(ids, group); err != nil {
		return fmt.Errorf("engine: register group %q: %w", groupID, err)
	}
	return nil
}

// UnregisterGroup atomically removes accounts from groupID on the live engine.
func (e *openPitEngine) UnregisterGroup(
	ctx context.Context, accounts []domain.AccountID, groupID string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: unregister group cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return fmt.Errorf("engine: unregister group on stopped engine")
	}

	ids, err := accountIDs(accounts)
	if err != nil {
		return err
	}
	group, err := newAccountGroupID(groupID)
	if err != nil {
		return err
	}
	if err := e.eng.Accounts().UnregisterGroup(ids, group); err != nil {
		return fmt.Errorf("engine: unregister group %q: %w", groupID, err)
	}
	return nil
}

// BlockGroup kill-switches every account in groupID on the live engine. Block
// keeps the first recorded reason, so the group is unblocked and re-blocked to
// refresh the reason, mirroring BlockAccount.
func (e *openPitEngine) BlockGroup(ctx context.Context, groupID, reason string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: block group cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return fmt.Errorf("engine: block group on stopped engine")
	}

	group, err := newAccountGroupID(groupID)
	if err != nil {
		return err
	}
	accounts := e.eng.Accounts()
	if err := accounts.UnblockGroup(group); err != nil {
		return fmt.Errorf("engine: block group %q: %w", groupID, err)
	}
	if err := accounts.BlockGroup(group, reason); err != nil {
		return fmt.Errorf("engine: block group %q: %w", groupID, err)
	}
	return nil
}

// UnblockGroup lifts the engine block on groupID.
func (e *openPitEngine) UnblockGroup(ctx context.Context, groupID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: unblock group cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return fmt.Errorf("engine: unblock group on stopped engine")
	}

	group, err := newAccountGroupID(groupID)
	if err != nil {
		return err
	}
	if err := e.eng.Accounts().UnblockGroup(group); err != nil {
		return fmt.Errorf("engine: unblock group %q: %w", groupID, err)
	}
	return nil
}

// accountIDs parses a domain account-id slice into binding account ids.
func accountIDs(accounts []domain.AccountID) ([]param.AccountID, error) {
	ids := make([]param.AccountID, 0, len(accounts))
	for _, account := range accounts {
		id, err := newAccountID(account)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// CheckOrder runs the pre-trade pipeline for probe as a non-mutating dry-run.
// It builds the same model.Order as SubmitOrder and runs ExecutePreTradeDryRun,
// which evaluates every policy but commits nothing: no reservation is taken and
// no account state changes. On pass it captures the would-be reservation lock
// prices exactly as SubmitOrder captures them; on reject it returns the engine
// rejects plus the account block the engine would record. The dry-run report
// owns native memory and never escapes the adapter.
func (e *openPitEngine) CheckOrder(
	ctx context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.CheckResult{}, fmt.Errorf("engine: check order cancelled: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return domain.CheckResult{}, fmt.Errorf("engine: check order on stopped engine")
	}

	order, err := orderModelFrom(domain.Order{
		Account:     probe.Account,
		BaseAsset:   probe.BaseAsset,
		QuoteAsset:  probe.QuoteAsset,
		Side:        probe.Side,
		AmountKind:  probe.AmountKind,
		AmountValue: probe.AmountValue,
		Price:       probe.Price,
	})
	if err != nil {
		return domain.CheckResult{}, err
	}

	report, err := e.eng.ExecutePreTradeDryRun(order)
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("engine: execute pre-trade dry-run: %w", err)
	}
	defer report.Close()

	if !report.IsPass() {
		return domain.CheckResult{
			Passed:     false,
			Rejects:    orderRejectsFrom(report.Rejects()),
			WouldBlock: accountBlockFrom(report.AccountBlock(), probe.Account),
		}, nil
	}

	prices, err := report.Lock().Prices()
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("engine: read dry-run lock: %w", err)
	}
	lockPrices := make([]string, 0, len(prices))
	for _, price := range prices {
		lockPrices = append(lockPrices, price.String())
	}
	return domain.CheckResult{Passed: true, WouldLockPrices: lockPrices}, nil
}

// MarketDataSink returns the quote sink backed by the engine's market-data
// service. It is valid for the whole process: the engine is never rebuilt, so
// the backing service handle is stable until Stop closes it.
func (e *openPitEngine) MarketDataSink() marketdata.Sink {
	return e.sink
}

// Stop halts the engine and releases native resources. It stops the engine
// first, then closes the market-data service, so quote producers (the connector
// manager) must be stopped before Stop. It is idempotent.
func (e *openPitEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return
	}
	e.running = false
	if e.eng != nil {
		e.eng.Stop()
	}
	if e.marketDataService != nil {
		e.marketDataService.Close()
		e.marketDataService = nil
	}
}

// policyName maps a domain policy id to the name the policy registers under.
func policyName(policy string) string {
	switch policy {
	case domain.PolicyRateLimit:
		return nameRateLimit
	case domain.PolicyOrderSizeLimit:
		return nameOrderSizeLimit
	case domain.PolicyPnlBoundsKillSwitch:
		return namePnlBoundsKillSwitch
	default:
		return policy
	}
}

// buildEngine constructs an OpenPit engine plus its market-data service,
// registering the order-validation policy plus each risk policy that has at
// least one barrier in byPolicy. It returns the engine, the service, and the set
// of registered policy names. byPolicy may be nil. The risk policies validate
// their barrier topology at build time, so a policy with no barriers is simply
// not registered.
//
// The market-data service is built unconditionally and before the engine (the
// engine builder requires the service to exist first), even when nothing is
// configured: an empty service is a harmless registry the connector manager
// fills at runtime. On a service-build failure the engine build fails; on an
// engine-build failure the already-created service is closed so the native
// resource never leaks. On success the caller owns the returned service and must
// close it (via Engine.Stop).
//
// The spot-funds policy is always registered with its default settings: it is
// the authority for spot balances, so balance seeding and spot-holdings
// reservations depend on it. It stays limit-only - the service is NOT wired into
// it (no WithMarketOrders) this phase, so market orders still reject
// UnsupportedOrderType and price-free behavior is unchanged. Dynamic
// price-dependent consumption is planned.
func buildEngine(
	byPolicy map[string][]domain.Limit,
) (*openpit.Engine, *bindmd.Service, map[string]struct{}, error) {
	// The market-data service is built from the engine builder so its sync mode
	// is derived (FullSync), and it must be built before the engine.
	eb := openpit.NewEngineBuilder().FullSync()
	service, err := eb.MarketData(defaultQuoteTTL).Build()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("engine: build market-data service: %w", err)
	}

	// OrderValidation first, then SpotFunds (default settings, limit-only), then
	// the conditional risk policies. SpotFunds must be registered for balance
	// seeding and spot holdings to take effect. PolicyGroupID(0) reaches the
	// ready builder without WithMarketOrders, keeping the policy limit-only.
	builder := eb.
		Builtin(policies.BuildOrderValidation()).
		Builtin(policies.BuildSpotFunds().PolicyGroupID(0))
	// TODO: price-dependent policy consumption

	// SpotFunds is always registered by the build path above.
	registered := map[string]struct{}{nameSpotFunds: {}}
	if rate := byPolicy[domain.PolicyRateLimit]; len(rate) > 0 {
		ready, err := rateLimitReady(rate)
		if err != nil {
			builder.Close()
			service.Close()
			return nil, nil, nil, err
		}
		builder = builder.Builtin(ready)
		registered[nameRateLimit] = struct{}{}
	}
	if size := byPolicy[domain.PolicyOrderSizeLimit]; len(size) > 0 {
		ready, err := orderSizeReady(size)
		if err != nil {
			builder.Close()
			service.Close()
			return nil, nil, nil, err
		}
		builder = builder.Builtin(ready)
		registered[nameOrderSizeLimit] = struct{}{}
	}
	if pnl := byPolicy[domain.PolicyPnlBoundsKillSwitch]; len(pnl) > 0 {
		ready, err := pnlBoundsReady(pnl)
		if err != nil {
			builder.Close()
			service.Close()
			return nil, nil, nil, err
		}
		builder = builder.Builtin(ready)
		registered[namePnlBoundsKillSwitch] = struct{}{}
	}

	eng, err := builder.Build()
	if err != nil {
		service.Close()
		return nil, nil, nil, fmt.Errorf("engine: build openpit engine: %w", err)
	}
	return eng, service, registered, nil
}

// applyBlocks blocks every blocked account in accounts on the engine with its
// persisted reason.
func applyBlocks(eng *openpit.Engine, accounts []domain.Account) error {
	handle := eng.Accounts()
	for _, account := range accounts {
		if !account.Blocked {
			continue
		}
		accountID, err := param.NewAccountIDFromString(account.ID.String())
		if err != nil {
			return fmt.Errorf("engine: block account %q: %w", account.ID, err)
		}
		handle.Block(accountID, account.BlockReason)
	}
	return nil
}

// hydrateGroups registers each account that carries a non-empty GroupID into its
// group on the engine, one RegisterGroup call per group. RegisterGroup is
// all-or-nothing; a register error signals corruption of our own persisted
// membership and aborts startup.
func hydrateGroups(eng *openpit.Engine, accounts []domain.Account) error {
	byGroup := make(map[string][]param.AccountID)
	order := make([]string, 0)
	for _, account := range accounts {
		if account.GroupID == "" {
			continue
		}
		id, err := param.NewAccountIDFromString(account.ID.String())
		if err != nil {
			return fmt.Errorf("engine: hydrate group account %q: %w", account.ID, err)
		}
		if _, seen := byGroup[account.GroupID]; !seen {
			order = append(order, account.GroupID)
		}
		byGroup[account.GroupID] = append(byGroup[account.GroupID], id)
	}

	handle := eng.Accounts()
	for _, groupID := range order {
		group, err := newAccountGroupID(groupID)
		if err != nil {
			return err
		}
		if err := handle.RegisterGroup(byGroup[groupID], group); err != nil {
			return fmt.Errorf("engine: register group %q: %w", groupID, err)
		}
	}
	return nil
}

// blockGroups blocks every blocked group in groups on the engine with its
// persisted reason.
func blockGroups(eng *openpit.Engine, groups []domain.AccountGroup) error {
	handle := eng.Accounts()
	for _, group := range groups {
		if !group.Blocked {
			continue
		}
		id, err := newAccountGroupID(group.ID)
		if err != nil {
			return err
		}
		if err := handle.BlockGroup(id, group.BlockReason); err != nil {
			return fmt.Errorf("engine: block group %q: %w", group.ID, err)
		}
	}
	return nil
}

// seedBalances applies each persisted balance as one absolute account
// adjustment, setting available/held/incoming (and average-entry-price when
// present) so the spot-funds policy starts from the stored holdings. A reject or
// error signals corruption of our own persisted values and aborts startup.
func seedBalances(eng *openpit.Engine, balances []domain.Balance) error {
	for _, balance := range balances {
		account, err := param.NewAccountIDFromString(balance.Account.String())
		if err != nil {
			return fmt.Errorf("engine: seed balance account %q: %w", balance.Account, err)
		}
		adjustment, err := balanceSeedAdjustment(balance)
		if err != nil {
			return err
		}
		batch, _, err := eng.ApplyAccountAdjustment(
			account, []model.AccountAdjustment{adjustment})
		if err != nil {
			return fmt.Errorf("engine: seed balance %s/%s: %w",
				balance.Account, balance.Asset, err)
		}
		if rej, ok := batch.Get(); ok {
			return fmt.Errorf("engine: seed balance %s/%s rejected: %s: %w",
				balance.Account, balance.Asset, seedRejectReason(rej), domain.ErrInvalid)
		}
	}
	return nil
}

// balanceSeedAdjustment builds the absolute adjustment that seeds one stored
// balance: available/held/incoming set absolutely when the persisted value is
// non-empty, left nil (not set) when empty. A nil field is the correct "absent"
// signal to the adjustment-values mapper (adjustmentAmountValues treats a nil
// pointer as not set, so the binding never sees an empty string). This guards
// against legacy rows where INSERT OR REPLACE wrote "" before the orZero write
// chokepoint was added.
func balanceSeedAdjustment(balance domain.Balance) (model.AccountAdjustment, error) {
	absField := func(v string) *domain.AdjustmentAmount {
		if v == "" {
			return nil
		}
		return &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: v}
	}
	req := domain.AdjustmentRequest{
		Asset:             balance.Asset,
		AverageEntryPrice: balance.AverageEntryPrice,
		Balance:           absField(balance.Available),
		Held:              absField(balance.Held),
		Incoming:          absField(balance.Incoming),
	}
	return accountAdjustmentFromRequest(req)
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
