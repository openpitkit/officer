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

package node

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

type engineRebuildPhase uint8

const (
	engineRebuildPrepared engineRebuildPhase = iota
	engineRebuildCommitted
)

type engineRebuildError struct {
	err   error
	phase engineRebuildPhase
}

func (e *engineRebuildError) Error() string {
	phase := "prepared"
	if e.phase == engineRebuildCommitted {
		phase = "committed"
	}
	return fmt.Sprintf("%s engine rebuild failure: %v", phase, e.err)
}

func (e *engineRebuildError) Unwrap() error {
	return e.err
}

// localNode is the in-process Node: one engine and one realm-scoped store
// running together behind the control plane. In the single-binary deployment
// there is exactly one localNode, bound to domain.DefaultRealm, and it owns
// every account.
//
// The node keeps two store references. db is the connection-lifecycle handle
// (migrate, ping, path, reset, schema, close, and ForRealm); realm is the
// realm-scoped data-access handle every table operation routes through. The
// single-realm connector resolves ForRealm to one fixed realm, so realm is
// re-obtained only when ResetDatabase recreates the backing database.
//
// mutate serializes store-only control-plane writes. Account and group engine
// effects route through the engine's async lanes instead; multi-account or
// rebuild-style operations use the exclusive restart gate below.
//
// Administrative snapshot changes and policy changes the SDK cannot reconfigure
// dynamically rebuild the engine from persisted state. While that rebuild
// window is open, new mutating requests are rejected rather than queued behind
// the rebuild.
type localNode struct {
	engineMu sync.RWMutex
	engine   engine.Engine
	// marketDataTransition fans provider updates into both the current and the
	// freshly built engine while persisted quotes are replayed before a swap.
	marketDataTransition marketdata.Sink
	build                engine.BuildFunc
	db                   store.Store
	realm                store.RealmStore
	fatal                func(error)

	mutate     sync.Mutex
	reportMu   sync.Mutex
	laneGate   sync.RWMutex
	restarting atomic.Bool
}

const (
	autoCreatedAssetClassCode  = "auto-created"
	autoCreatedAssetClassTitle = "Auto-created assets"
	autoCreatedAssetClassNotes = "Assets automatically created while handling " +
		"operations that referenced unknown assets."
)

// LocalOption customizes a local node instance.
type LocalOption func(*localNode)

// WithFatalShutdownHook wires the process-level fail-stop hook for
// unrecoverable post-engine persistence failures.
func WithFatalShutdownHook(hook func(error)) LocalOption {
	return func(n *localNode) {
		if hook != nil {
			n.fatal = hook
		}
	}
}

// NewLocalNode builds the single in-process Node: it binds the fixed realm via
// store.ForRealm(domain.DefaultRealm), loads the seed snapshot from that realm
// (accounts, the full typed barrier set, groups, and balances), builds the one
// engine from it via build, bundles the engine and store, and appends one
// startup audit row.
//
// build is retained for administrative rebuilds after persisted snapshot
// changes such as backup restore, database reset, or unsupported dynamic policy
// changes.
//
// It returns the node and the engine handle. The node owns the engine and
// store: Close stops the engine and closes the store. The returned handle is
// the permanent handle, but live version/health should still be read through
// the node (Health, EngineVersion) for a uniform access path, because restore
// can swap the current engine. It returns an error if any dependency is nil, if
// the realm cannot be bound, if the seed cannot be loaded, or if the engine
// cannot be built.
func NewLocalNode(
	ctx context.Context, st store.Store, build engine.BuildFunc, opts ...LocalOption,
) (Node, engine.Engine, error) {
	if st == nil {
		return nil, nil, fmt.Errorf("nil store")
	}
	if build == nil {
		return nil, nil, fmt.Errorf("nil engine build func")
	}

	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		return nil, nil, fmt.Errorf("bind realm: %w", err)
	}

	n := &localNode{
		db:    st,
		realm: realm,
		build: build,
		fatal: func(error) {},
	}
	for _, opt := range opts {
		opt(n)
	}
	if err := n.ensureOperatorPrincipal(ctx, realm); err != nil {
		return nil, nil, err
	}

	snap, counts, err := n.loadSnapshot(ctx)
	if err != nil {
		return nil, nil, err
	}

	eng, err := build(snap)
	if err != nil {
		return nil, nil, fmt.Errorf("build engine: %w", err)
	}
	if eng == nil {
		return nil, nil, fmt.Errorf("build returned nil engine")
	}
	n.engine = eng

	// A system-initiated action carries no actor principal (the audit row's Actor
	// is empty for system origin; SourceSystem marks the channel). Stamping a
	// literal "system" actor would reference a non-existent principal dictionary
	// code and be rejected by the store.
	if err := realm.AppendAudit(ctx, store.AuditEntry{
		Action: domain.AuditActionHydrate,
		Detail: counts,
		Source: domain.SourceSystem,
	}); err != nil {
		eng.Stop()
		return nil, nil, fmt.Errorf("audit build: %w", err)
	}

	// Seeding the snapshot's persisted P&L can kill-switch an account before the
	// node serves anything (an accumulated loss restored past its barrier blocks
	// on sight). Mirror those blocks before the node is handed out.
	if err := n.mirrorSeedAccountBlocks(ctx, eng); err != nil {
		eng.Stop()
		return nil, nil, fmt.Errorf("mirror seed account blocks: %w", err)
	}

	return n, eng, nil
}

func (n *localNode) accountDiagnosticID(
	ctx context.Context, account domain.AccountID,
) (string, error) {
	if account == "" {
		return "unknown", nil
	}
	record, ok, err := n.realm.GetAccount(ctx, account)
	if err != nil {
		return "", fmt.Errorf("read diagnostic account id %s: %w", account, err)
	}
	if !ok || record.EngineAccountID == 0 {
		return "", fmt.Errorf("diagnostic account id %s: %w", account, domain.ErrNotFound)
	}
	return strconv.FormatUint(record.EngineAccountID.Uint64(), 10), nil
}

// fatalPostEnginePersistence routes a post-engine persistence failure into the
// fatal-shutdown hook. The engine mutation already committed, so a failed store
// write leaves engine and store divergent; the P7 cascade is intentionally
// straight-to-fatal (it does not block the account first) because there is no
// safe in-process state to fall back to once the durable record is lost.
func (n *localNode) fatalPostEnginePersistence(
	operation string, accountID string, err error,
) error {
	if err == nil {
		return nil
	}
	if accountID == "" {
		accountID = "unknown"
	}
	n.fatal(fmt.Errorf(
		"operation=%q account_id=%s: post-engine persistence failure: %w",
		operation, accountID, err,
	))
	return err
}

// fatalPostCommitAudit routes an audit failure after a durable store mutation
// into the fatal-shutdown hook. The caller has already reported success to its
// transactional persistence seam, so continuing would let a retry duplicate
// history that the first request committed without its required audit row.
func (n *localNode) fatalPostCommitAudit(
	operation string, accountID string, err error,
) error {
	if err == nil {
		return nil
	}
	if accountID == "" {
		accountID = "unknown"
	}
	n.fatal(fmt.Errorf(
		"operation=%q account_id=%s: post-commit audit failure: %w",
		operation, accountID, err,
	))
	return err
}

// fatalPostEngineAuditByCode routes a post-engine audit-write failure into the
// fatal-shutdown hook, identifying the subject by its operator-facing code
// (subjectKind is "account", "asset", "group", or "policy") rather than the
// engine surrogate. The admin block, group, asset, and limit paths use it so
// the diagnostic never resolves or emits the DB/engine account id; the audit row
// itself already stores code and title only. Like fatalPostEnginePersistence
// this is the straight-to-fatal P7 cascade: the engine mutation committed and
// its audit trail is now lost.
func (n *localNode) fatalPostEngineAuditByCode(
	operation, subjectKind, code string, err error,
) error {
	if err == nil {
		return nil
	}
	if code == "" {
		code = "unknown"
	}
	n.fatal(fmt.Errorf(
		"operation=%q %s=%s: post-engine persistence failure: %w",
		operation, subjectKind, code, err,
	))
	return err
}

func (n *localNode) fatalReconciliation(operation string, err error) error {
	if err == nil {
		return nil
	}
	n.fatal(fmt.Errorf(
		"operation=%q: engine/store reconciliation failure: %w",
		operation, err,
	))
	return err
}

func (n *localNode) reconcileEngineAfterFailure(
	ctx context.Context, operation string, cause error,
) error {
	rebuildErr := n.rebuildEngineFromStore(ctx)
	if rebuildErr == nil {
		return cause
	}
	return n.fatalReconciliation(operation, errors.Join(
		cause,
		fmt.Errorf("rebuild engine from current store: %w", rebuildErr),
	))
}

// loadSnapshot reads the full engine seed from the bound realm: accounts (with
// their blocked state and group membership), the complete typed barrier set,
// groups, and balances. It returns the assembled snapshot and a human-readable
// counts summary for the hydrate audit detail. It runs once at NewLocalNode and
// again on every administrative engine rebuild.
func (n *localNode) loadSnapshot(ctx context.Context) (engine.Snapshot, string, error) {
	accounts, err := n.realm.ListAccounts(ctx)
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load accounts for build: %w", err)
	}
	rateLimits, err := n.realm.ListRateLimits(ctx, "")
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load rate limits for build: %w", err)
	}
	orderSizeLimits, err := n.realm.ListOrderSizeLimits(ctx, "")
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load order-size limits for build: %w", err)
	}
	spotFundsPnlBoundsLimits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf(
			"load spot funds pnl-bounds limits for build: %w", err,
		)
	}
	groups, err := n.realm.ListGroups(ctx)
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load groups for build: %w", err)
	}
	if defaultGroup, ok, err := n.realm.GetGroup(ctx, ""); err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load default group for build: %w", err)
	} else if ok {
		groups = append(groups, defaultGroup)
	}
	balances, err := n.realm.ListBalances(ctx, "", "")
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load balances for build: %w", err)
	}

	snap := engine.Snapshot{
		Accounts:                 accounts,
		RateLimits:               rateLimits,
		OrderSizeLimits:          orderSizeLimits,
		SpotFundsPnlBoundsLimits: spotFundsPnlBoundsLimits,
		Groups:                   groups,
		Balances:                 balances,
	}
	barriers := len(rateLimits) + len(orderSizeLimits) + len(spotFundsPnlBoundsLimits)
	counts := fmt.Sprintf(
		"hydrate %d accounts %d barriers %d groups %d balances",
		len(accounts), barriers, len(groups), len(balances),
	)
	return snap, counts, nil
}

// Health returns the aggregate health of the node's engine and store. The store
// leg runs a Ping so StoreHealth.Reachable reflects the live connection; a
// failed Ping is reported as unreachable rather than returned as an error, so
// the dashboard can show a degraded-but-running node.
func (n *localNode) Health(ctx context.Context) (Health, error) {
	n.engineMu.RLock()
	engineHealth := engine.Health{
		Version:      n.engine.Version(),
		BuildProfile: n.engine.BuildProfile(),
		Running:      n.engine.Running(),
	}
	n.engineMu.RUnlock()

	schemaVersion, schemaErr := n.db.SchemaVersion(ctx)
	reachable := n.db.Ping(ctx) == nil
	storeHealth := store.StoreHealth{
		Path:          n.db.Path(),
		SchemaVersion: schemaVersion,
		Reachable:     reachable,
	}
	// A schema read failure means the store is not usable: surface it as
	// unreachable instead of failing the whole health probe.
	if schemaErr != nil {
		storeHealth.Reachable = false
	}

	return Health{Engine: engineHealth, Store: storeHealth}, nil
}

// EngineVersion returns the version of the node's current engine. The MCP
// version source routes through here so restore-swapped engines are observed.
func (n *localNode) EngineVersion() string {
	n.engineMu.RLock()
	defer n.engineMu.RUnlock()
	return n.engine.Version()
}

// Owns reports whether this node is responsible for the given routing key. The
// single node owns every account, so it always returns true.
func (n *localNode) Owns(Key) bool { return true }

// ListAccounts returns every persisted account owned by this node.
func (n *localNode) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	accounts, err := n.realm.ListAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	return accounts, nil
}

// ListAccountRows returns persisted accounts matching filter.
func (n *localNode) ListAccountRows(
	ctx context.Context, filter store.AccountListFilter,
) (store.AccountListPage, error) {
	accounts, err := n.realm.ListAccountRows(ctx, filter)
	if err != nil {
		return store.AccountListPage{}, fmt.Errorf("list account rows: %w", err)
	}
	return accounts, nil
}

// ExportBackup returns a portable archive of this node's persisted state.
func (n *localNode) ExportBackup(
	ctx context.Context,
	scope backup.Scope,
	caller domain.Caller,
) (backup.Archive, error) {
	if err := n.beginMutation(); err != nil {
		return backup.Archive{}, err
	}
	defer n.endMutation()

	archive, err := n.realm.ExportBackup(ctx, scope)
	if err != nil {
		return backup.Archive{}, fmt.Errorf("export backup: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionExportBackup,
		Detail: fmt.Sprintf(
			"export backup realm %s sections %d",
			archive.Manifest.Realm.Code,
			len(archive.Manifest.Sections),
		),
	}); err != nil {
		return backup.Archive{}, fmt.Errorf("audit export backup: %w", err)
	}
	return archive, nil
}

// RestoreBackup imports a portable archive and publishes every runtime delta
// the engine can express through its synchronized online surfaces. A replacement
// engine is built only for an SDK lifecycle gap or for recovery after a partial
// online mutation.
func effectiveRestoreScope(
	requested backup.Scope,
	available []backup.Section,
) backup.Scope {
	scope := requested.Normalize()
	availableSet := make(map[backup.Section]struct{}, len(available))
	for _, section := range available {
		availableSet[section] = struct{}{}
	}
	if scope.All {
		scope.All = false
		scope.Sections = append([]backup.Section(nil), available...)
		return scope
	}
	sections := scope.Sections[:0]
	for _, section := range scope.Sections {
		if _, ok := availableSet[section]; ok {
			sections = append(sections, section)
		}
	}
	scope.Sections = sections
	return scope
}

func (n *localNode) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
	caller domain.Caller,
) (backup.RestoreSummary, marketdata.Sink, error) {
	normalizedScope := effectiveRestoreScope(
		opts.Scope, archive.Manifest.Sections,
	)
	runtimeScope := backup.TouchesRuntime(normalizedScope)
	if runtimeScope {
		// Reserve restart ownership before waiting for the exclusive gates. A
		// concurrent rebuild must reject instead of reserving restarting while this
		// restore commits and then forcing a fatal post-commit collision. The
		// reservation does not itself require an engine replacement: expressible
		// deltas still publish into the current engine below.
		if err := n.beginEngineRestart(); err != nil {
			return backup.RestoreSummary{}, n.currentMarketDataSink(), err
		}
		defer n.endEngineRestart()
	} else {
		if err := n.beginMutation(); err != nil {
			return backup.RestoreSummary{}, n.currentMarketDataSink(), err
		}
		defer n.endMutation()
	}

	var beforeRuntime restoreRuntimeSnapshot
	var rollback backup.Archive
	var err error
	if runtimeScope {
		beforeRuntime, _, err = n.captureRestoreRuntimeSnapshot(ctx)
	} else {
		rollback, err = n.realm.ExportBackup(ctx, normalizedScope)
	}
	if err != nil {
		return backup.RestoreSummary{}, n.currentMarketDataSink(),
			fmt.Errorf("capture restore rollback state: %w", err)
	}

	effectiveOpts := opts
	effectiveOpts.Scope = normalizedScope
	summary, err := n.realm.RestoreBackup(ctx, archive, effectiveOpts)
	if err != nil {
		return backup.RestoreSummary{}, n.currentMarketDataSink(),
			fmt.Errorf("restore backup: %w", err)
	}
	durableCtx := context.WithoutCancel(ctx)
	// A runtime restore is already the committed desired truth. Portable
	// rollback would assign fresh stable engine ids and can overwrite quotes
	// arriving from the market-data manager outside this gate. Do not roll it
	// back behind a serving engine; fail-stop and let restart hydrate the
	// committed store. Non-runtime scopes have no engine identity and are safe
	// to roll back with their exact scoped archive.
	failAfterCommit := func(operation string, cause error) error {
		if runtimeScope {
			return n.fatalReconciliation(operation, cause)
		}
		return n.rollbackStore(
			durableCtx, rollback, normalizedScope, cause,
		)
	}

	var afterRuntime restoreRuntimeSnapshot
	plan := restoreRuntimePlan{}
	if runtimeScope {
		afterRuntime, _, err = n.captureRestoreRuntimeSnapshot(durableCtx)
		if err != nil {
			return backup.RestoreSummary{}, n.currentMarketDataSink(),
				failAfterCommit(
					"capture committed restore runtime state",
					fmt.Errorf("capture restored runtime snapshot: %w", err),
				)
		}
		plan = classifyRestoreRuntimeDelta(beforeRuntime, afterRuntime)
		if plan.marketDataNeedsClear && !plan.rebuild {
			if _, ok := n.currentMarketDataSink().(marketdata.QuoteClearer); !ok {
				plan.rebuild = true
				plan.reason = "live market-data sink cannot clear restored quotes"
			}
		}
	}

	if plan.rebuild {
		if err := n.rebuildEngineFromStore(durableCtx); err != nil {
			return backup.RestoreSummary{}, n.currentMarketDataSink(),
				failAfterCommit(
					"rebuild engine for committed backup restore",
					fmt.Errorf(
						"rebuild engine for backup restore (%s): %w",
						plan.reason, err,
					),
				)
		}
		summary.RestartRequired = true
	} else {
		summary.RestartRequired = false
		if plan.runtimeChanged {
			if err := n.applyRestoreRuntimeDelta(
				durableCtx, beforeRuntime, afterRuntime, plan,
			); err != nil {
				return backup.RestoreSummary{}, n.currentMarketDataSink(),
					failAfterCommit(
						"publish committed backup restore online",
						fmt.Errorf("publish restored runtime state online: %w", err),
					)
			}
		}
	}

	if err := n.audit(durableCtx, caller, store.AuditEntry{
		Action: domain.AuditActionRestoreBackup,
		Detail: fmt.Sprintf(
			"restore backup realm %s mode %s",
			archive.Manifest.Realm.Code,
			opts.Mode,
		),
	}); err != nil {
		return backup.RestoreSummary{}, n.currentMarketDataSink(),
			failAfterCommit(
				"audit committed backup restore",
				fmt.Errorf("audit restore backup: %w", err),
			)
	}

	return summary, n.currentMarketDataSink(), nil
}

func (n *localNode) ResetDatabase(
	ctx context.Context,
	caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return n.currentMarketDataSink(), err
	}
	defer n.endEngineRestart()

	if err := n.db.Reset(ctx); err != nil {
		return n.currentMarketDataSink(), fmt.Errorf("reset database: %w", err)
	}
	durableCtx := context.WithoutCancel(ctx)
	// Reset is the durable commit point: the previous store no longer exists.
	// Every later failure is fatal because the old engine cannot be reconciled
	// by rolling the database back.
	committedFailure := func(operation string, err error) (marketdata.Sink, error) {
		return n.currentMarketDataSink(), n.fatalReconciliation(operation, err)
	}
	realm, err := n.db.ForRealm(durableCtx, domain.DefaultRealm)
	if err != nil {
		return committedFailure(
			"rebind realm after database reset",
			fmt.Errorf("rebind realm after reset: %w", err),
		)
	}
	n.realm = realm
	if err := n.ensureOperatorPrincipal(durableCtx, realm); err != nil {
		return committedFailure("ensure operator after database reset", err)
	}
	if err := n.rebuildEngineFromStore(durableCtx); err != nil {
		return committedFailure(
			"rebuild engine after database reset",
			fmt.Errorf("rebuild engine after reset: %w", err),
		)
	}
	if err := n.audit(durableCtx, caller, store.AuditEntry{
		Action: domain.AuditActionResetDatabase,
		Detail: "reset database from scratch",
	}); err != nil {
		return committedFailure(
			"audit database reset",
			fmt.Errorf("audit reset database: %w", err),
		)
	}
	return n.currentMarketDataSink(), nil
}

func (n *localNode) ensureOperatorPrincipal(
	ctx context.Context, realm store.RealmStore,
) error {
	// Public surfaces stamp this placeholder actor until authentication lands.
	err := realm.CreatePrincipal(ctx, domain.Principal{Code: domain.PrincipalOperator})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return fmt.Errorf("ensure operator principal: %w", err)
	}
	return nil
}

// seedAccountBlockSource is the optional capability an engine adapter exposes to
// report the account blocks the engine latched while the handle was seeded from
// persisted state: a restored P&L that is halted, or that already breaches its
// barrier, kill-switches the account as it is seeded. engine.BuildFunc hands the
// node only the Engine, so the blocks ride on the fresh handle and the node
// drains them here. An adapter that cannot block while seeding just omits it.
type seedAccountBlockSource interface {
	SeedAccountBlocks() []domain.AccountBlock
}

// mirrorSeedAccountBlocks persists and audits the blocks eng latched while being
// seeded. The engine already rejects every order for these accounts, so the
// store must record the block before any lane runs, or the account reads as
// tradable while every order dies with no audit row naming the cause. Callers
// hold the restart or live-policy gate, so no lane can observe the gap.
func (n *localNode) mirrorSeedAccountBlocks(
	ctx context.Context, eng engine.Engine,
) error {
	source, ok := eng.(seedAccountBlockSource)
	if !ok {
		return nil
	}
	return n.mirrorPolicyConfigurationBlocks(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, source.SeedAccountBlocks(),
	)
}

func (n *localNode) rebuildEngineFromStore(ctx context.Context) error {
	preparedFailure := func(err error) error {
		return &engineRebuildError{err: err, phase: engineRebuildPrepared}
	}
	snap, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return preparedFailure(fmt.Errorf("load restored snapshot: %w", err))
	}
	next, err := n.build(snap)
	if err != nil {
		return preparedFailure(fmt.Errorf("build restored engine: %w", err))
	}
	if next == nil {
		return preparedFailure(fmt.Errorf("build restored engine returned nil"))
	}
	transition, err := n.beginMarketDataTransition(next)
	if err != nil {
		if next != n.currentEngine() {
			next.Stop()
		}
		return preparedFailure(fmt.Errorf("prepare restored engine market data: %w", err))
	}
	if err := n.replayMarketDataInto(ctx, next); err != nil {
		n.cancelMarketDataTransition(transition)
		if next != n.currentEngine() {
			next.Stop()
		}
		return preparedFailure(fmt.Errorf("replay market data into restored engine: %w", err))
	}
	prev, err := n.commitMarketDataTransition(transition, next)
	if err != nil {
		if next != n.currentEngine() {
			next.Stop()
		}
		return preparedFailure(fmt.Errorf("commit restored engine market data: %w", err))
	}
	if prev != nil && prev != next {
		prev.Stop()
	}
	// The rebuilt handle is already live. Any failure from this point must be
	// handled as committed: rolling the store back would put it behind the
	// replacement engine that is now serving.
	if err := n.mirrorSeedAccountBlocks(ctx, next); err != nil {
		return &engineRebuildError{
			err:   fmt.Errorf("mirror rebuilt engine seed blocks: %w", err),
			phase: engineRebuildCommitted,
		}
	}
	return nil
}

func (n *localNode) currentMarketDataSink() marketdata.Sink {
	n.engineMu.RLock()
	defer n.engineMu.RUnlock()
	if n.marketDataTransition != nil {
		return n.marketDataTransition
	}
	return n.engine.MarketDataSink()
}

func (n *localNode) currentEngine() engine.Engine {
	n.engineMu.RLock()
	defer n.engineMu.RUnlock()
	return n.engine
}

func (n *localNode) beginLane() (engine.Engine, func(), error) {
	n.laneGate.RLock()
	if n.restarting.Load() {
		n.laneGate.RUnlock()
		return nil, nil, fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	return n.currentEngine(), n.laneGate.RUnlock, nil
}

func (n *localNode) beginLaneRead() (engine.Engine, func()) {
	n.laneGate.RLock()
	return n.currentEngine(), n.laneGate.RUnlock
}

// CurrentMarketDataSink returns the current engine's quote sink so the
// market-data runtime can re-adopt it after an engine rebuild.
func (n *localNode) CurrentMarketDataSink() marketdata.Sink {
	return n.currentMarketDataSink()
}

func (n *localNode) beginMutation() error {
	if n.restarting.Load() {
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	n.mutate.Lock()
	if n.restarting.Load() {
		n.mutate.Unlock()
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	return nil
}

func (n *localNode) endMutation() {
	n.mutate.Unlock()
}

// beginLivePolicyConfiguration excludes account lanes while a policy update is
// applied and its engine-reported account blocks are mirrored into the store.
// Unlike beginEngineRestart it keeps the live engine in place and therefore
// does not set restarting.
func (n *localNode) beginLivePolicyConfiguration() error {
	n.laneGate.Lock()
	if n.restarting.Load() {
		n.laneGate.Unlock()
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	n.mutate.Lock()
	if n.restarting.Load() {
		n.mutate.Unlock()
		n.laneGate.Unlock()
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	return nil
}

func (n *localNode) endLivePolicyConfiguration() {
	n.mutate.Unlock()
	n.laneGate.Unlock()
}

// beginLiveIdentityPublication quiesces every account lane while Officer
// publishes account or group dictionary changes into the live adapter and
// engine. This is not a synthetic group lane or an engine restart: it keeps the
// current engine and market-data sink in place and does not set restarting.
func (n *localNode) beginLiveIdentityPublication() error {
	n.laneGate.Lock()
	if n.restarting.Load() {
		n.laneGate.Unlock()
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	n.mutate.Lock()
	if n.restarting.Load() {
		n.mutate.Unlock()
		n.laneGate.Unlock()
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	return nil
}

func (n *localNode) endLiveIdentityPublication() {
	n.mutate.Unlock()
	n.laneGate.Unlock()
}

func (n *localNode) beginEngineRestartFromLane(endLane func()) error {
	if !n.restarting.CompareAndSwap(false, true) {
		endLane()
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting,
		)
	}
	// Reserve the restart before releasing the admitted lane. New lane and
	// store-only mutations now reject instead of entering the uncertain state.
	endLane()
	n.laneGate.Lock()
	n.mutate.Lock()
	return nil
}

func (n *localNode) beginEngineRestart() error {
	if !n.restarting.CompareAndSwap(false, true) {
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	n.laneGate.Lock()
	n.mutate.Lock()
	return nil
}

func (n *localNode) endEngineRestart() {
	// Clear the restart flag before releasing the mutation lock so a mutating
	// request that wins the lock next sees restarting=false and proceeds, rather
	// than racing the flag and rejecting spuriously.
	n.restarting.Store(false)
	n.mutate.Unlock()
	n.laneGate.Unlock()
}

func (n *localNode) rollbackStore(
	ctx context.Context,
	rollback backup.Archive,
	scope backup.Scope,
	err error,
) error {
	_, restoreErr := n.realm.RestoreBackup(ctx, rollback, backup.RestoreOptions{
		Scope: scope,
		Mode:  backup.RestoreModeReplaceAll,
	})
	if restoreErr != nil {
		return n.fatalReconciliation(
			"rollback store",
			errors.Join(err, fmt.Errorf("rollback restore backup: %w", restoreErr)),
		)
	}
	return err
}

// localRouter is the single-node NodeRouter for the single-binary deployment.
// It resolves every owned key to the one node and enumerates exactly that node.
type localRouter struct {
	node Node
}

// NewLocalRouter returns a NodeRouter over a single node. Route returns that
// node for any key it owns and an error otherwise; All returns the one node. It
// returns an error if n is nil.
func NewLocalRouter(n Node) (NodeRouter, error) {
	if n == nil {
		return nil, fmt.Errorf("nil node for router")
	}
	return &localRouter{node: n}, nil
}

// ErrNoOwner is returned by Route when no node owns the requested key.
var ErrNoOwner = errors.New("no node owns key")

// Route returns the single node when it owns key, otherwise ErrNoOwner.
func (r *localRouter) Route(key Key) (Node, error) {
	if !r.node.Owns(key) {
		return nil, fmt.Errorf("%w: account=%q", ErrNoOwner, key.Account)
	}
	return r.node, nil
}

// All returns a fresh one-element snapshot of the node set.
func (r *localRouter) All() []Node {
	return []Node{r.node}
}
