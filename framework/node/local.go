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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

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
	build    engine.BuildFunc
	db       store.Store
	realm    store.RealmStore
	fatal    func(error)

	mutate     sync.Mutex
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

// fatalPostEngineAuditByCode routes a post-engine audit-write failure into the
// fatal-shutdown hook, identifying the subject by its operator-facing code
// (subjectKind is "account" or "group") rather than the engine surrogate. The
// admin block/group/set-group paths use it so the diagnostic never resolves or
// emits the DB/engine account id; the audit row itself already stores code and
// title only. Like fatalPostEnginePersistence this is the straight-to-fatal P7
// cascade: the engine mutation committed and its audit trail is now lost.
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
	pnlBoundsLimits, err := n.realm.ListPnlBoundsLimits(ctx, "")
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load pnl-bounds limits for build: %w", err)
	}
	groups, err := n.realm.ListGroups(ctx)
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load groups for build: %w", err)
	}
	balances, err := n.realm.ListBalances(ctx, "", "")
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load balances for build: %w", err)
	}

	snap := engine.Snapshot{
		Accounts:        accounts,
		RateLimits:      rateLimits,
		OrderSizeLimits: orderSizeLimits,
		PnlBoundsLimits: pnlBoundsLimits,
		Groups:          groups,
		Balances:        balances,
	}
	barriers := len(rateLimits) + len(orderSizeLimits) + len(pnlBoundsLimits)
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

// RestoreBackup imports a portable archive and rebuilds the live engine from
// the restored store snapshot when the restored sections affect runtime state.
func (n *localNode) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
	caller domain.Caller,
) (backup.RestoreSummary, marketdata.Sink, error) {
	// Decide the lock from the normalized scope, not the raw requested one. A
	// restore of an account-addressed section (audit log, activity history) force-
	// includes the accounts+groups dictionary for FK resolution, and landing a new
	// group there requires an engine rebuild the store gates on the applied rows.
	// The rebuild swaps the engine, so it must run under the exclusive restart gate
	// that quiesces every lane; take it whenever the normalized scope could write a
	// runtime dictionary. Only a purely observational restore (settings, or a
	// non-account-addressed section) takes the lighter mutation lock.
	if backup.TouchesRuntime(opts.Scope.Normalize()) {
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

	before, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		return backup.RestoreSummary{}, n.currentMarketDataSink(),
			fmt.Errorf("capture restore rollback backup: %w", err)
	}

	summary, err := n.realm.RestoreBackup(ctx, archive, opts)
	if err != nil {
		return backup.RestoreSummary{}, n.currentMarketDataSink(),
			fmt.Errorf("restore backup: %w", err)
	}

	if summary.RestartRequired {
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return backup.RestoreSummary{}, n.currentMarketDataSink(),
				n.rollbackStore(ctx, before, err)
		}
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionRestoreBackup,
		Detail: fmt.Sprintf(
			"restore backup realm %s mode %s",
			archive.Manifest.Realm.Code,
			opts.Mode,
		),
	}); err != nil {
		err = fmt.Errorf("audit restore backup: %w", err)
		if summary.RestartRequired {
			return backup.RestoreSummary{}, n.currentMarketDataSink(),
				n.rollbackStoreAndEngine(ctx, before, err)
		}
		return backup.RestoreSummary{}, n.currentMarketDataSink(),
			n.rollbackStore(ctx, before, err)
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
	// Reset recreates the backing database, so the previous realm handle is
	// stale; re-bind the fixed realm against the fresh database before the
	// rebuild reads from it.
	realm, err := n.db.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		return n.currentMarketDataSink(), fmt.Errorf("rebind realm after reset: %w", err)
	}
	n.realm = realm
	if err := n.ensureOperatorPrincipal(ctx, realm); err != nil {
		return n.currentMarketDataSink(), err
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return n.currentMarketDataSink(), err
	}
	// The reset audit intentionally carries no actor principal. The caller's
	// channel is preserved as the source so the reset stays attributable,
	// mirroring the system-sourced startup hydrate.
	if err := n.realm.AppendAudit(ctx, store.AuditEntry{
		Action: domain.AuditActionResetDatabase,
		Detail: "reset database from scratch",
		Source: caller.Source,
	}); err != nil {
		return n.currentMarketDataSink(),
			fmt.Errorf("audit reset database: %w", err)
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

func (n *localNode) rebuildEngineFromStore(ctx context.Context) error {
	snap, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load restored snapshot: %w", err)
	}
	next, err := n.build(snap)
	if err != nil {
		return fmt.Errorf("build restored engine: %w", err)
	}
	if next == nil {
		return fmt.Errorf("build restored engine returned nil")
	}
	next.SetReservationStore(n.realm)
	n.swapEngine(next)
	return nil
}

func (n *localNode) swapEngine(next engine.Engine) {
	n.engineMu.Lock()
	prev := n.engine
	n.engine = next
	n.engineMu.Unlock()
	// Never stop the handle just installed: a real build always returns a fresh
	// handle (prev != next), but a degenerate build that hands back the current
	// one must not be torn down out from under the node.
	if prev != nil && prev != next {
		prev.Stop()
	}
}

func (n *localNode) currentMarketDataSink() marketdata.Sink {
	n.engineMu.RLock()
	defer n.engineMu.RUnlock()
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

func (n *localNode) rollbackStore(ctx context.Context, rollback backup.Archive, err error) error {
	_, restoreErr := n.realm.RestoreBackup(ctx, rollback, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	})
	if restoreErr != nil {
		return errors.Join(err, fmt.Errorf("rollback restore backup: %w", restoreErr))
	}
	return err
}

func (n *localNode) rollbackStoreAndEngine(
	ctx context.Context,
	rollback backup.Archive,
	err error,
) error {
	err = n.rollbackStore(ctx, rollback, err)
	if rebuildErr := n.rebuildEngineFromStore(ctx); rebuildErr != nil {
		return errors.Join(err, fmt.Errorf("rollback restored engine: %w", rebuildErr))
	}
	return err
}

// ListAssets returns every persisted asset.
func (n *localNode) ListAssets(ctx context.Context) ([]domain.Asset, error) {
	page, err := n.ListAssetRows(ctx, store.AssetListFilter{})
	if err != nil {
		return nil, err
	}
	return page.Rows, nil
}

// ListAssetRows returns persisted assets matching filter, with total count.
func (n *localNode) ListAssetRows(
	ctx context.Context, filter store.AssetListFilter,
) (store.AssetListPage, error) {
	page, err := n.realm.ListAssetRows(ctx, filter)
	if err != nil {
		return store.AssetListPage{}, fmt.Errorf("list asset rows: %w", err)
	}
	return page, nil
}

// CreateAsset persists a new asset dictionary row and audits the action.
func (n *localNode) CreateAsset(
	ctx context.Context, asset domain.Asset, caller domain.Caller,
) (domain.Asset, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Asset{}, err
	}
	defer n.endMutation()

	if err := n.realm.CreateAsset(ctx, asset); err != nil {
		return domain.Asset{}, fmt.Errorf("create asset: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateAsset,
		Asset:  asset.Code,
		Detail: fmt.Sprintf("create asset %s", asset.Code),
	}); err != nil {
		return domain.Asset{}, fmt.Errorf("audit create asset: %w", err)
	}
	return asset, nil
}

// UpdateAsset replaces the asset's public code and mutable fields (title, asset
// class) and audits the action. The asset is not part of the engine resolver, so
// a code rename has no engine side-effect and no rebuild; dependent rows
// reference the asset by its surrogate id.
func (n *localNode) UpdateAsset(
	ctx context.Context, oldCode string, asset domain.Asset, caller domain.Caller,
) (domain.Asset, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Asset{}, err
	}
	defer n.endMutation()

	updated, err := n.realm.UpdateAsset(ctx, oldCode, asset)
	if err != nil {
		return domain.Asset{}, fmt.Errorf("update asset: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionUpdateAsset,
		Asset:  updated.Code,
		Detail: fmt.Sprintf("update asset %s -> %s", oldCode, updated.Code),
	}); err != nil {
		return domain.Asset{}, fmt.Errorf("audit update asset: %w", err)
	}
	return updated, nil
}

// DeleteAsset removes the asset, cascading its dependent rows when force is set,
// and audits the action. The asset is store-only, so there is no engine
// side-effect.
func (n *localNode) DeleteAsset(
	ctx context.Context, code string, force bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteAsset(ctx, code, force); err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionDeleteAsset,
		Asset:  code,
		Detail: fmt.Sprintf("delete asset %s", code),
	}); err != nil {
		return fmt.Errorf("audit delete asset: %w", err)
	}
	return nil
}

// ListAssetClasses returns every persisted asset class.
func (n *localNode) ListAssetClasses(
	ctx context.Context,
) ([]domain.AssetClass, error) {
	classes, err := n.realm.ListAssetClasses(ctx)
	if err != nil {
		return nil, fmt.Errorf("list asset classes: %w", err)
	}
	return classes, nil
}

// ListAssetClassRows returns persisted asset classes matching filter.
func (n *localNode) ListAssetClassRows(
	ctx context.Context, filter store.AssetClassListFilter,
) (store.AssetClassListPage, error) {
	page, err := n.realm.ListAssetClassRows(ctx, filter)
	if err != nil {
		return store.AssetClassListPage{}, fmt.Errorf("list asset class rows: %w", err)
	}
	return page, nil
}

// CreateAssetClass persists a new asset-class dictionary row and audits the
// action. The class is store-only, so there is no engine side-effect.
func (n *localNode) CreateAssetClass(
	ctx context.Context, class domain.AssetClass, caller domain.Caller,
) (domain.AssetClass, error) {
	if err := n.beginMutation(); err != nil {
		return domain.AssetClass{}, err
	}
	defer n.endMutation()

	if err := n.realm.CreateAssetClass(ctx, class); err != nil {
		return domain.AssetClass{}, fmt.Errorf("create asset class: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateAssetClass,
		Detail: fmt.Sprintf("create asset class %s", class.Code),
	}); err != nil {
		return domain.AssetClass{}, fmt.Errorf("audit create asset class: %w", err)
	}
	return class, nil
}

// UpdateAssetClass replaces the class's public code, title and notes and audits
// the action. The store cascades the asset link on a code rename; the class is
// store-only, so there is no engine side-effect.
func (n *localNode) UpdateAssetClass(
	ctx context.Context, oldCode string, class domain.AssetClass, caller domain.Caller,
) (domain.AssetClass, error) {
	if err := n.beginMutation(); err != nil {
		return domain.AssetClass{}, err
	}
	defer n.endMutation()

	updated, err := n.realm.UpdateAssetClass(ctx, oldCode, class)
	if err != nil {
		return domain.AssetClass{}, fmt.Errorf("update asset class: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionUpdateAssetClass,
		Detail: fmt.Sprintf("update asset class %s -> %s", oldCode, updated.Code),
	}); err != nil {
		return domain.AssetClass{}, fmt.Errorf("audit update asset class: %w", err)
	}
	return updated, nil
}

// DeleteAssetClass removes the class, clearing the asset link when force is set,
// and audits the action. The class is store-only, so there is no engine
// side-effect.
func (n *localNode) DeleteAssetClass(
	ctx context.Context, code string, force bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteAssetClass(ctx, code, force); err != nil {
		return fmt.Errorf("delete asset class: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionDeleteAssetClass,
		Detail: fmt.Sprintf("delete asset class %s", code),
	}); err != nil {
		return fmt.Errorf("audit delete asset class: %w", err)
	}
	return nil
}

// CreateAccount persists a new account, rebuilds the live engine so the account
// enters the resolver, and audits the action. The resolver is built from the
// snapshot and the engine has no incremental account registration, so a new
// account stays invisible to the engine — its adjustments, group moves, and
// orders reject as "unknown account" — until the engine is rebuilt from the
// store. This mirrors the rebuild DeleteAccount performs when the account set
// shrinks. The store assigns the engine account id and returns the populated
// account.
func (n *localNode) CreateAccount(
	ctx context.Context, account domain.Account, caller domain.Caller,
) (domain.Account, error) {
	if err := n.beginEngineRestart(); err != nil {
		return domain.Account{}, err
	}
	defer n.endEngineRestart()

	account, err := n.realm.CreateAccount(ctx, account)
	if err != nil {
		return domain.Account{}, fmt.Errorf("create account: %w", err)
	}

	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return domain.Account{}, fmt.Errorf("rebuild engine after account create: %w", err)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      account.Code,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("create account %s", account.Code),
	}); err != nil {
		return domain.Account{}, fmt.Errorf("audit create account: %w", err)
	}
	return account, nil
}

// SetAccountBlocked blocks or unblocks the account in the store, then the
// engine, reverting the store on engine failure, and audits the action.
func (n *localNode) SetAccountBlocked(
	ctx context.Context, key Key, blocked bool, reason string, caller domain.Caller,
) error {
	eng, done, err := n.beginLane()
	if err != nil {
		return err
	}
	defer done()

	// Pre-lane existence check: the engine resolves the account before entering
	// the lane and rejects an unknown code with ErrInvalid, so a missing account
	// must be surfaced as ErrNotFound here (before the lane) or the closure below
	// never runs to report it.
	if _, ok, err := n.realm.GetAccount(ctx, key.Account); err != nil {
		return fmt.Errorf("read account for block: %w", err)
	} else if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}

	// Serialize the prev-read, store write, engine block, revert, and audit on
	// the one account lane so concurrent same-account admin ops cannot interleave
	// the store write with the engine block and diverge. The lane also serializes
	// against fills and the execution-report kill-switch, which mutate the same
	// account's engine block state on that lane.
	return eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		prev, ok, err := n.realm.GetAccount(ctx, key.Account)
		if err != nil {
			return fmt.Errorf("read account for block: %w", err)
		}
		if !ok {
			return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
		}

		if err := n.realm.SetAccountBlocked(ctx, key.Account, blocked, reason); err != nil {
			return fmt.Errorf("set account blocked: %w", err)
		}

		if applyErr := n.applyBlock(ctx, lane, key.Account, blocked, reason); applyErr != nil {
			// Revert the store to the previously persisted blocked state.
			_ = n.realm.SetAccountBlocked(ctx, key.Account, prev.Blocked, prev.BlockReason)
			return fmt.Errorf("apply account block: %w", applyErr)
		}

		action := domain.AuditActionBlock
		detail := fmt.Sprintf("block account %s", key.Account)
		if !blocked {
			action = domain.AuditActionUnblock
			detail = fmt.Sprintf("unblock account %s", key.Account)
		}
		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:       action,
			Account:      key.Account,
			AccountTitle: prev.Title,
			Detail:       detail,
		}); err != nil {
			return n.fatalPostEngineAuditByCode(
				"audit account block", "account", key.Account.String(),
				fmt.Errorf("audit account block: %w", err),
			)
		}
		return nil
	})
}

// applyBlock applies the desired blocked state to the engine.
func (n *localNode) applyBlock(
	ctx context.Context, lane engine.AccountLane, id domain.AccountID, blocked bool, reason string,
) error {
	if blocked {
		return lane.BlockAccount(ctx, id, reason)
	}
	return lane.UnblockAccount(ctx, id)
}

// mirrorEngineBlocksAudit writes one system-sourced audit row per engine-recorded
// account block, naming the triggering order and the engine's reason. The block
// UPDATE itself is folded into RecordOrderSettlement's tx (atomic with the fill);
// this audit row is observational and runs post-commit as a best-effort write, so
// a crash between commit and audit leaves the account blocked but the audit cause
// briefly missing - an accepted weak-consistency window, since the audit is not
// part of the financial invariant.
func (n *localNode) mirrorEngineBlocksAudit(
	ctx context.Context, order domain.ExternalID,
	blocks []domain.ExecutionAccountBlock,
) error {
	for _, block := range blocks {
		// A kill-switch block is engine-initiated: it carries no actor principal
		// (the store rejects a non-existent principal code, and the audit row's
		// Actor is empty for system origin). SourceSystem marks the engine channel,
		// and the detail names the engine cause.
		if err := n.realm.AppendAudit(ctx, store.AuditEntry{
			Source:  domain.SourceSystem,
			Action:  domain.AuditActionBlock,
			Account: block.Account,
			Detail:  engineBlockDetail(order, block),
		}); err != nil {
			return fmt.Errorf("audit engine block: %w", err)
		}
	}
	return nil
}

// audit appends one audit row, stamping the caller's principal and source onto
// the entry. Every mutation routes its audit through here so attribution is
// applied uniformly.
func (n *localNode) audit(ctx context.Context, caller domain.Caller, entry store.AuditEntry) error {
	entry.Actor = caller.Principal
	entry.Source = caller.Source
	return n.realm.AppendAudit(ctx, entry)
}

// AppendAudit persists one audit row stamped with the caller. It is the seam
// the backend uses for control-plane actions that have no node-mutating
// counterpart (signing-key management, signing config, approval issue/confirm/
// cancel).
func (n *localNode) AppendAudit(
	ctx context.Context, entry store.AuditEntry, caller domain.Caller,
) error {
	return n.audit(ctx, caller, entry)
}

// GetAccountState returns the account row and the barriers whose scope has the
// account axis and matches the account.
func (n *localNode) GetAccountState(
	ctx context.Context, key Key,
) (domain.Account, AccountLimits, error) {
	account, ok, err := n.realm.GetAccount(ctx, key.Account)
	if err != nil {
		return domain.Account{}, AccountLimits{}, fmt.Errorf("get account: %w", err)
	}
	if !ok {
		return domain.Account{}, AccountLimits{}, fmt.Errorf(
			"account %q: %w", key.Account, domain.ErrNotFound)
	}
	limits, err := n.listLimits(ctx, key.Account)
	if err != nil {
		return domain.Account{}, AccountLimits{}, fmt.Errorf("list account limits: %w", err)
	}
	return account, limits, nil
}

// ListLimits returns the barriers that reference account, or all barriers when
// account is empty.
func (n *localNode) ListLimits(
	ctx context.Context, account domain.AccountID,
) (AccountLimits, error) {
	limits, err := n.listLimits(ctx, account)
	if err != nil {
		return AccountLimits{}, fmt.Errorf("list limits: %w", err)
	}
	return limits, nil
}

// ListPolicyRows returns the node's typed barriers flattened into one sorted,
// paged policy list.
func (n *localNode) ListPolicyRows(
	ctx context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	page, err := n.realm.ListPolicyRows(ctx, filter)
	if err != nil {
		return store.PolicyListPage{}, fmt.Errorf("list policy rows: %w", err)
	}
	return page, nil
}

// listLimits reads the three typed barrier tables narrowed to account (empty
// returns every barrier) and bundles them into the read-side AccountLimits.
func (n *localNode) listLimits(
	ctx context.Context, account domain.AccountID,
) (AccountLimits, error) {
	rate, err := n.realm.ListRateLimits(ctx, account)
	if err != nil {
		return AccountLimits{}, fmt.Errorf("list rate limits: %w", err)
	}
	orderSize, err := n.realm.ListOrderSizeLimits(ctx, account)
	if err != nil {
		return AccountLimits{}, fmt.Errorf("list order-size limits: %w", err)
	}
	pnlBounds, err := n.realm.ListPnlBoundsLimits(ctx, account)
	if err != nil {
		return AccountLimits{}, fmt.Errorf("list pnl-bounds limits: %w", err)
	}
	return AccountLimits{
		RateLimits:      rate,
		OrderSizeLimits: orderSize,
		PnlBoundsLimits: pnlBounds,
	}, nil
}

// PutRateLimit upserts the whole rate-limit barrier in the store, reconfigures
// the rate policy from the persisted full barrier set, reverts the store on
// engine-apply failure, and audits the action. It returns a replacement
// market-data sink only when the policy change had to rebuild the engine.
func (n *localNode) PutRateLimit(
	ctx context.Context, limit domain.LimitRate, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return nil, err
	}
	defer n.endEngineRestart()

	target := LimitTarget{
		Policy:  domain.PolicyRateLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	prev, hadPrev, err := n.readRateBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "rate limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.realm.PutRateLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put rate limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, domain.PolicyRateLimit)
	if applyErr != nil {
		n.revertRateBarrier(ctx, target, prev, hadPrev)
		return nil, fmt.Errorf("configure policy after limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setRateLimitDetail(limit),
	}); err != nil {
		return sink, fmt.Errorf("audit set limit: %w", err)
	}
	return sink, nil
}

// PutOrderSizeLimit upserts the whole order-size barrier and reconfigures the
// order-size policy, mirroring PutRateLimit for the rate policy.
func (n *localNode) PutOrderSizeLimit(
	ctx context.Context, limit domain.LimitOrderSize, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return nil, err
	}
	defer n.endEngineRestart()

	target := LimitTarget{
		Policy:  domain.PolicyOrderSizeLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	prev, hadPrev, err := n.readOrderSizeBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "order-size limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.realm.PutOrderSizeLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put order-size limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, domain.PolicyOrderSizeLimit)
	if applyErr != nil {
		n.revertOrderSizeBarrier(ctx, target, prev, hadPrev)
		return nil, fmt.Errorf("configure policy after limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setOrderSizeLimitDetail(limit),
	}); err != nil {
		return sink, fmt.Errorf("audit set limit: %w", err)
	}
	return sink, nil
}

// PutPnlBoundsLimit upserts the whole P&L-bounds barrier and reconfigures the
// P&L-bounds policy, mirroring PutRateLimit for the rate policy.
func (n *localNode) PutPnlBoundsLimit(
	ctx context.Context, limit domain.LimitPnlBounds, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return nil, err
	}
	defer n.endEngineRestart()

	target := LimitTarget{
		Policy:  domain.PolicyPnlBoundsKillSwitch,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	prev, hadPrev, err := n.readPnlBoundsBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "pnl-bounds limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.realm.PutPnlBoundsLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put pnl-bounds limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, domain.PolicyPnlBoundsKillSwitch)
	if applyErr != nil {
		n.revertPnlBoundsBarrier(ctx, target, prev, hadPrev)
		return nil, fmt.Errorf("configure policy after limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setPnlBoundsLimitDetail(limit),
	}); err != nil {
		return sink, fmt.Errorf("audit set limit: %w", err)
	}
	return sink, nil
}

// DeleteLimit removes the barrier addressed by target from its typed table,
// reconfigures the named policy from the persisted full barrier set, reverts the
// store on engine-apply failure, and audits the action. It returns a replacement
// market-data sink only when the policy change had to rebuild the engine.
func (n *localNode) DeleteLimit(
	ctx context.Context, target LimitTarget, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return nil, err
	}
	defer n.endEngineRestart()

	revert, err := n.deleteBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, target.Policy)
	if applyErr != nil {
		revert()
		return nil, fmt.Errorf("configure policy after delete limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionDeleteLimit,
		Account: target.Account,
		Detail:  deleteLimitDetail(target),
	}); err != nil {
		return sink, fmt.Errorf("audit delete limit: %w", err)
	}
	return sink, nil
}

// deleteBarrier reads the barrier currently stored at target (so a failed engine
// apply can be reverted), deletes it from its typed table, and returns a
// best-effort revert closure that re-puts the previous barrier when one existed.
func (n *localNode) deleteBarrier(
	ctx context.Context, target LimitTarget,
) (func(), error) {
	switch target.Policy {
	case domain.PolicyRateLimit:
		prev, hadPrev, err := n.readRateBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := n.realm.DeleteRateLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
			return nil, fmt.Errorf("delete rate limit: %w", err)
		}
		return func() { n.revertRateBarrier(ctx, target, prev, hadPrev) }, nil
	case domain.PolicyOrderSizeLimit:
		prev, hadPrev, err := n.readOrderSizeBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := n.realm.DeleteOrderSizeLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
			return nil, fmt.Errorf("delete order-size limit: %w", err)
		}
		return func() { n.revertOrderSizeBarrier(ctx, target, prev, hadPrev) }, nil
	case domain.PolicyPnlBoundsKillSwitch:
		prev, hadPrev, err := n.readPnlBoundsBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := n.realm.DeletePnlBoundsLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
			return nil, fmt.Errorf("delete pnl-bounds limit: %w", err)
		}
		return func() { n.revertPnlBoundsBarrier(ctx, target, prev, hadPrev) }, nil
	default:
		return nil, fmt.Errorf("unknown policy %q: %w", target.Policy, domain.ErrInvalid)
	}
}

// applyPolicyChangeLocked applies a just-persisted barrier change for policy
// to the engine via the runtime Configure surface. If the SDK cannot express
// the change dynamically, the persisted snapshot becomes the source of truth and
// the engine is rebuilt. Callers must hold the exclusive restart gate.
func (n *localNode) applyPolicyChangeLocked(
	ctx context.Context, policy string,
) (marketdata.Sink, error) {
	if err := n.reconfigurePolicy(ctx, policy); err != nil {
		if !errors.Is(err, domain.ErrNotImplemented) {
			return nil, err
		}
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return nil, err
		}
		return n.currentMarketDataSink(), nil
	}
	return nil, nil
}

// reconfigurePolicy re-reads the full typed barrier set for policy from the
// store and applies it to the engine via the runtime Configure surface. It
// returns the engine error verbatim so the caller can decide whether to revert
// or rebuild.
func (n *localNode) reconfigurePolicy(ctx context.Context, policy string) error {
	limits, err := n.policyLimitSet(ctx, policy)
	if err != nil {
		return err
	}
	return n.engine.ConfigurePolicy(ctx, policy, limits)
}

// policyLimitSet reads the full barrier set for one policy from its typed table
// and packs it into the engine.LimitSet the runtime Configure surface consumes.
// Only the slice matching policy is populated; ConfigurePolicy ignores the rest.
func (n *localNode) policyLimitSet(ctx context.Context, policy string) (engine.LimitSet, error) {
	switch policy {
	case domain.PolicyRateLimit:
		limits, err := n.realm.ListRateLimits(ctx, "")
		if err != nil {
			return engine.LimitSet{}, fmt.Errorf("read rate limits: %w", err)
		}
		return engine.LimitSet{RateLimits: limits}, nil
	case domain.PolicyOrderSizeLimit:
		limits, err := n.realm.ListOrderSizeLimits(ctx, "")
		if err != nil {
			return engine.LimitSet{}, fmt.Errorf("read order-size limits: %w", err)
		}
		return engine.LimitSet{OrderSizeLimits: limits}, nil
	case domain.PolicyPnlBoundsKillSwitch:
		limits, err := n.realm.ListPnlBoundsLimits(ctx, "")
		if err != nil {
			return engine.LimitSet{}, fmt.Errorf("read pnl-bounds limits: %w", err)
		}
		return engine.LimitSet{PnlBoundsLimits: limits}, nil
	default:
		return engine.LimitSet{}, fmt.Errorf("unknown policy %q: %w", policy, domain.ErrInvalid)
	}
}

// readRateBarrier returns the rate-limit barrier currently stored at target so a
// failed engine apply can be reverted. The bool is false when none exists.
func (n *localNode) readRateBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitRate, bool, error) {
	limits, err := n.realm.ListRateLimits(ctx, "")
	if err != nil {
		return domain.LimitRate{}, false, fmt.Errorf("read rate barrier: %w", err)
	}
	for _, limit := range limits {
		if limit.Scope == target.Scope && limit.Account == target.Account && limit.Asset == target.Asset {
			return limit, true, nil
		}
	}
	return domain.LimitRate{}, false, nil
}

func (n *localNode) ensureLimitAsset(
	ctx context.Context,
	scope string,
	asset string,
	operation string,
	caller domain.Caller,
) error {
	switch scope {
	case domain.ScopeAsset, domain.ScopeAccountAsset:
		_, err := n.ensureAutoCreatedAsset(ctx, asset, operation, caller)
		return err
	default:
		return nil
	}
}

// revertRateBarrier restores the rate-limit barrier at target to its previous
// state: re-put when it existed before, delete when it did not.
func (n *localNode) revertRateBarrier(
	ctx context.Context, target LimitTarget, prev domain.LimitRate, hadPrev bool,
) {
	if hadPrev {
		// Best-effort revert; the caller already surfaces the primary error.
		_ = n.realm.PutRateLimit(ctx, prev)
		return
	}
	// Best-effort revert; the caller already surfaces the primary error.
	_ = n.realm.DeleteRateLimit(ctx, target.Scope, target.Account, target.Asset)
}

// readOrderSizeBarrier mirrors readRateBarrier for the order-size table.
func (n *localNode) readOrderSizeBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitOrderSize, bool, error) {
	limits, err := n.realm.ListOrderSizeLimits(ctx, "")
	if err != nil {
		return domain.LimitOrderSize{}, false, fmt.Errorf("read order-size barrier: %w", err)
	}
	for _, limit := range limits {
		if limit.Scope == target.Scope && limit.Account == target.Account && limit.Asset == target.Asset {
			return limit, true, nil
		}
	}
	return domain.LimitOrderSize{}, false, nil
}

// revertOrderSizeBarrier mirrors revertRateBarrier for the order-size table.
func (n *localNode) revertOrderSizeBarrier(
	ctx context.Context, target LimitTarget, prev domain.LimitOrderSize, hadPrev bool,
) {
	if hadPrev {
		_ = n.realm.PutOrderSizeLimit(ctx, prev)
		return
	}
	_ = n.realm.DeleteOrderSizeLimit(ctx, target.Scope, target.Account, target.Asset)
}

// readPnlBoundsBarrier mirrors readRateBarrier for the P&L-bounds table.
func (n *localNode) readPnlBoundsBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitPnlBounds, bool, error) {
	limits, err := n.realm.ListPnlBoundsLimits(ctx, "")
	if err != nil {
		return domain.LimitPnlBounds{}, false, fmt.Errorf("read pnl-bounds barrier: %w", err)
	}
	for _, limit := range limits {
		if limit.Scope == target.Scope && limit.Account == target.Account && limit.Asset == target.Asset {
			return limit, true, nil
		}
	}
	return domain.LimitPnlBounds{}, false, nil
}

// revertPnlBoundsBarrier mirrors revertRateBarrier for the P&L-bounds table.
func (n *localNode) revertPnlBoundsBarrier(
	ctx context.Context, target LimitTarget, prev domain.LimitPnlBounds, hadPrev bool,
) {
	if hadPrev {
		_ = n.realm.PutPnlBoundsLimit(ctx, prev)
		return
	}
	_ = n.realm.DeletePnlBoundsLimit(ctx, target.Scope, target.Account, target.Asset)
}

// ListAudit returns the most recent n audit rows, newest first.
func (n *localNode) ListAudit(
	ctx context.Context, count int,
) ([]domain.AuditRow, error) {
	rows, err := n.realm.ListAudit(ctx, count)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	return rows, nil
}

// ListAuditFiltered returns the most recent count audit rows matching the
// filter, newest first.
func (n *localNode) ListAuditFiltered(
	ctx context.Context, filter domain.AuditFilter, count int,
) ([]domain.AuditRow, error) {
	rows, err := n.realm.ListAuditFiltered(ctx, filter, count)
	if err != nil {
		return nil, fmt.Errorf("list audit filtered: %w", err)
	}
	return rows, nil
}

// ListAuditRows returns audit rows matching filter.
func (n *localNode) ListAuditRows(
	ctx context.Context, filter store.AuditListFilter,
) (store.AuditListPage, error) {
	page, err := n.realm.ListAuditRows(ctx, filter)
	if err != nil {
		return store.AuditListPage{}, fmt.Errorf("list audit rows: %w", err)
	}
	return page, nil
}

// --- MCP access control -----------------------------------------------------

// ListMcpAccess returns the stored per-command MCP overrides keyed by command.
// MCP access is a control-plane-wide setting with no engine side-effect, so
// this is a plain store read.
func (n *localNode) ListMcpAccess(ctx context.Context) (map[string]bool, error) {
	access, err := n.realm.ListMcpAccess(ctx)
	if err != nil {
		return nil, fmt.Errorf("list mcp access: %w", err)
	}
	return access, nil
}

// SetMcpAccess upserts the enabled state for one MCP command and audits the
// action. There is no engine side-effect: gating happens in the MCP surface,
// so the store is the sole authority and nothing is applied to or reverted
// from the engine.
func (n *localNode) SetMcpAccess(
	ctx context.Context, command string, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetMcpAccess(ctx, command, enabled); err != nil {
		return fmt.Errorf("set mcp access: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMcpAccess,
		Detail: setMcpAccessDetail(command, enabled),
	}); err != nil {
		return fmt.Errorf("audit set mcp access: %w", err)
	}
	return nil
}

// GetUserSetting returns the stored value for (userID, key). User settings are
// a plain store read with no engine side-effect.
func (n *localNode) GetUserSetting(
	ctx context.Context, userID, key string,
) (string, bool, error) {
	value, ok, err := n.realm.GetUserSetting(ctx, userID, key)
	if err != nil {
		return "", false, fmt.Errorf("get user setting: %w", err)
	}
	return value, ok, nil
}

// SetUserSetting upserts one per-user setting. There is no engine side-effect
// and personal UI preferences are not audited, so this is a guarded store write.
func (n *localNode) SetUserSetting(
	ctx context.Context, userID, key, value string,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetUserSetting(ctx, userID, key, value); err != nil {
		return fmt.Errorf("set user setting: %w", err)
	}
	return nil
}

// --- market-data control plane ---------------------------------------------

func (n *localNode) ListMarketDataInstances(
	ctx context.Context,
) ([]domain.MarketDataInstance, error) {
	instances, err := n.realm.ListMarketDataInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("list market-data instances: %w", err)
	}
	return instances, nil
}

func (n *localNode) GetMarketDataInstance(
	ctx context.Context, id domain.ExternalID,
) (domain.MarketDataInstance, bool, error) {
	instance, ok, err := n.realm.GetMarketDataInstance(ctx, id)
	if err != nil {
		return domain.MarketDataInstance{}, false, fmt.Errorf("get market-data instance: %w", err)
	}
	return instance, ok, nil
}

func (n *localNode) CreateMarketDataInstance(
	ctx context.Context, instance domain.MarketDataInstance, caller domain.Caller,
) (domain.MarketDataInstance, error) {
	if err := n.beginMutation(); err != nil {
		return domain.MarketDataInstance{}, err
	}
	defer n.endMutation()

	created, err := n.realm.CreateMarketDataInstance(ctx, instance)
	if err != nil {
		return domain.MarketDataInstance{}, fmt.Errorf("create market-data instance: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("create market-data instance %s", created.ExternalID),
	}); err != nil {
		return domain.MarketDataInstance{}, fmt.Errorf("audit create market-data instance: %w", err)
	}
	return created, nil
}

func (n *localNode) SetMarketDataInstanceEnabled(
	ctx context.Context, id domain.ExternalID, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetMarketDataInstanceEnabled(ctx, id, enabled); err != nil {
		return fmt.Errorf("set market-data instance enabled: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: marketDataToggleDetail("instance", id.String(), enabled),
	}); err != nil {
		return fmt.Errorf("audit set market-data instance enabled: %w", err)
	}
	return nil
}

func (n *localNode) UpdateMarketDataInstanceSettings(
	ctx context.Context, id domain.ExternalID, label, credentials string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.UpdateMarketDataInstanceSettings(ctx, id, label, credentials); err != nil {
		return fmt.Errorf("update market-data instance settings: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("update market-data instance settings %s", id),
	}); err != nil {
		return fmt.Errorf("audit update market-data instance settings: %w", err)
	}
	return nil
}

func (n *localNode) DeleteMarketDataInstance(
	ctx context.Context, id domain.ExternalID, force bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteMarketDataInstance(ctx, id, force); err != nil {
		return fmt.Errorf("delete market-data instance: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("delete market-data instance %s", id),
	}); err != nil {
		return fmt.Errorf("audit delete market-data instance: %w", err)
	}
	return nil
}

func (n *localNode) ListMarketDataInstruments(
	ctx context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	instruments, err := n.realm.ListMarketDataInstruments(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("list market-data instruments: %w", err)
	}
	return instruments, nil
}

func (n *localNode) UpsertMarketDataInstrument(
	ctx context.Context, instrument domain.MarketDataInstrument, caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	if err := n.ensureMarketDataAssets(ctx, instrument, caller); err != nil {
		return err
	}
	if err := n.realm.UpsertMarketDataInstrument(ctx, instrument); err != nil {
		return fmt.Errorf("upsert market-data instrument: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("upsert market-data instrument %s/%s",
			instrument.Instance, instrument.ExternalSymbol),
	}); err != nil {
		return fmt.Errorf("audit upsert market-data instrument: %w", err)
	}
	return nil
}

func (n *localNode) ensureMarketDataAssets(
	ctx context.Context, instrument domain.MarketDataInstrument, caller domain.Caller,
) error {
	created, err := n.ensureAutoCreatedAssets(ctx, instrument.BaseAsset,
		instrument.QuoteAsset, "market-data instrument upsert", caller)
	if err != nil {
		return err
	}
	if created {
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return fmt.Errorf("rebuild engine after asset create: %w", err)
		}
	}
	return nil
}

func (n *localNode) SetMarketDataInstrumentEnabled(
	ctx context.Context, instance domain.ExternalID, externalSymbol string, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetMarketDataInstrumentEnabled(
		ctx, instance, externalSymbol, enabled,
	); err != nil {
		return fmt.Errorf("set market-data instrument enabled: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: marketDataToggleDetail("instrument", instance.String()+"/"+externalSymbol, enabled),
	}); err != nil {
		return fmt.Errorf("audit set market-data instrument enabled: %w", err)
	}
	return nil
}

func (n *localNode) DeleteMarketDataInstrument(
	ctx context.Context, instance domain.ExternalID, externalSymbol string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteMarketDataInstrument(ctx, instance, externalSymbol); err != nil {
		return fmt.Errorf("delete market-data instrument: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("delete market-data instrument %s/%s", instance, externalSymbol),
	}); err != nil {
		return fmt.Errorf("audit delete market-data instrument: %w", err)
	}
	return nil
}

func (n *localNode) ListMarketDataQuotes(
	ctx context.Context, instance domain.ExternalID,
) ([]domain.MarketDataQuote, error) {
	quotes, err := n.realm.ListMarketDataQuotes(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("list market-data quotes: %w", err)
	}
	return quotes, nil
}

// --- account group & notes --------------------------------------------------

// SetAccountGroup sets or clears the account's group in the store, then moves
// it on the engine (unregister from the old group, register into the new),
// reverts the store on engine failure, and audits the action.
//
// A brand-new target group is auto-created and the engine rebuilt from the store
// under the exclusive engine-restart gate before the lane, so the live resolver
// knows the group when the in-lane RegisterGroup resolves it; an already-known
// target group skips the gate and goes straight to the lane. The account move
// itself (prev-read, store link write, engine membership move, revert, audit)
// runs on the one account lane so concurrent same-account admin ops cannot
// interleave and diverge.
func (n *localNode) SetAccountGroup(
	ctx context.Context, key Key, groupCode string, caller domain.Caller,
) error {
	// Pre-lane existence check: the engine resolves the account before entering
	// the lane and rejects an unknown code with ErrInvalid, so a missing account
	// must be surfaced as ErrNotFound here (before the lane) or the closure below
	// never runs to report it.
	if _, ok, err := n.realm.GetAccount(ctx, key.Account); err != nil {
		return fmt.Errorf("read account for set group: %w", err)
	} else if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}

	// Auto-create the target group and rebuild the engine before entering the
	// lane, so the live resolver knows it when the in-lane RegisterGroup resolves
	// it and so the rebuild never stops a live lane's runtime. Only a genuinely
	// new group escalates to the gate; an existing one is already registered.
	if err := n.ensureGroupRegisteredExclusive(ctx, groupCode); err != nil {
		return err
	}

	eng, done, err := n.beginLane()
	if err != nil {
		return err
	}
	defer done()

	// Serialize the prev-read, store link write, engine membership move, revert,
	// and audit on the one account lane. The prev.GroupCode read must live inside
	// the lane so the unregister/register move is computed from lane-serialized
	// state and never from a group that a concurrent move already changed.
	return eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		prev, ok, err := n.realm.GetAccount(ctx, key.Account)
		if err != nil {
			return fmt.Errorf("read account for set group: %w", err)
		}
		if !ok {
			return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
		}
		if prev.GroupCode == groupCode {
			return nil
		}

		if err := n.realm.SetAccountGroup(ctx, key.Account, groupCode); err != nil {
			return fmt.Errorf("set account group: %w", err)
		}

		if applyErr := n.applyGroupMove(
			ctx, eng, key.Account, prev.GroupCode, groupCode,
		); applyErr != nil {
			// Best-effort revert; the caller already surfaces the primary error.
			_ = n.realm.SetAccountGroup(ctx, key.Account, prev.GroupCode)
			return fmt.Errorf("apply account group: %w", applyErr)
		}

		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:       domain.AuditActionSetGroup,
			Account:      key.Account,
			AccountTitle: prev.Title,
			Detail:       setAccountGroupDetail(key.Account, groupCode),
		}); err != nil {
			return n.fatalPostEngineAuditByCode(
				"audit set account group", "account", key.Account.String(),
				fmt.Errorf("audit set account group: %w", err),
			)
		}
		return nil
	})
}

// ensureGroupRegisteredExclusive auto-creates the target group record and
// rebuilds the engine from the store under the exclusive engine-restart gate
// when the group is not already known, so the live resolver knows it before the
// caller enters an account lane and RegisterGroup resolves it. An empty code
// ("no group") and an already-persisted group skip the gate: an existing store
// group is already engine-registered because its creation rebuilt the engine.
func (n *localNode) ensureGroupRegisteredExclusive(
	ctx context.Context, groupCode string,
) error {
	if groupCode == "" {
		return nil
	}
	if _, ok, err := n.realm.GetGroup(ctx, groupCode); err != nil {
		return fmt.Errorf("read group for set: %w", err)
	} else if ok {
		return nil
	}
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	if err := n.ensureGroupRecordLocked(ctx, groupCode); err != nil {
		return fmt.Errorf("ensure group record on set: %w", err)
	}
	// The auto-created record is unknown to the resolver until the engine is
	// rebuilt from the store, so rebuild before the lane runs.
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return fmt.Errorf("rebuild engine after group ensure: %w", err)
	}
	return nil
}

// applyGroupMove moves one account between groups on the engine. Each concrete
// group membership mutation runs on that group's synchronized lane. An empty
// group code means "no group", so clearing only unregisters and setting from
// none only registers.
func (n *localNode) applyGroupMove(
	ctx context.Context, eng engine.Engine, id domain.AccountID, oldGroup, newGroup string,
) error {
	accounts := []domain.AccountID{id}
	if oldGroup != "" {
		if err := eng.RunGroupSynchronized(ctx, oldGroup, func(lane engine.GroupLane) error {
			return lane.UnregisterGroup(ctx, accounts, oldGroup)
		}); err != nil {
			return err
		}
	}
	if newGroup != "" {
		if err := eng.RunGroupSynchronized(ctx, newGroup, func(lane engine.GroupLane) error {
			return lane.RegisterGroup(ctx, accounts, newGroup)
		}); err != nil {
			if oldGroup != "" {
				_ = eng.RunGroupSynchronized(ctx, oldGroup, func(lane engine.GroupLane) error {
					return lane.RegisterGroup(ctx, accounts, oldGroup)
				})
			}
			return err
		}
	}
	return nil
}

// SetAccountNotes replaces the account's notes in the store and audits the
// action. Notes never reach the engine, so there is no engine side-effect and
// no account-lane hop is needed: it writes only the notes store row, which no
// engine lane touches.
func (n *localNode) SetAccountNotes(
	ctx context.Context, key Key, notes string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetAccountNotes(ctx, key.Account, notes); err != nil {
		return fmt.Errorf("set account notes: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetNotes,
		Account: key.Account,
		Detail:  fmt.Sprintf("set notes account %s", key.Account),
	}); err != nil {
		return fmt.Errorf("audit set account notes: %w", err)
	}
	return nil
}

// UpdateAccount replaces an account's public code and title, rebuilds the
// engine resolver, and audits the change. It takes no account lane: the code
// change is applied by a full engine rebuild (which cannot run inside a lane),
// already serialized against every lane by the exclusive restart gate.
func (n *localNode) UpdateAccount(
	ctx context.Context,
	key Key,
	account domain.Account,
	caller domain.Caller,
) (domain.Account, error) {
	if err := n.beginEngineRestart(); err != nil {
		return domain.Account{}, err
	}
	defer n.endEngineRestart()

	prev, ok, err := n.realm.GetAccount(ctx, key.Account)
	if err != nil {
		return domain.Account{}, fmt.Errorf("read account for update: %w", err)
	}
	if !ok {
		return domain.Account{},
			fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}
	updated, err := n.realm.UpdateAccount(ctx, key.Account, account)
	if err != nil {
		return domain.Account{}, fmt.Errorf("update account: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return domain.Account{},
			fmt.Errorf("rebuild engine after account update: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionUpdateAccount,
		Account:      updated.Code,
		AccountTitle: updated.Title,
		Detail: fmt.Sprintf(
			"update account %s -> %s",
			prev.Code,
			updated.Code,
		),
	}); err != nil {
		return domain.Account{}, fmt.Errorf("audit update account: %w", err)
	}
	return updated, nil
}

// DeleteAccount removes an account from the store, rebuilds the engine from the
// surviving rows, and audits the action.
func (n *localNode) DeleteAccount(
	ctx context.Context, key Key, force bool, caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	account, ok, err := n.realm.GetAccount(ctx, key.Account)
	if err != nil {
		return fmt.Errorf("read account for delete: %w", err)
	}
	if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}
	if err := n.realm.DeleteAccount(ctx, key.Account, force); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return fmt.Errorf("rebuild engine after account delete: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionDeleteAccount,
		Account:      key.Account,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("delete account %s", key.Account),
	}); err != nil {
		return fmt.Errorf("audit delete account: %w", err)
	}
	return nil
}

// --- groups -----------------------------------------------------------------

// CreateGroup persists a new account group, rebuilds the live engine so the
// group enters the resolver, and audits the action. Like CreateAccount, the
// resolver has no incremental group registration, so a runtime-created group is
// unknown to the engine — a later group move or group-scoped barrier would
// reject as "unknown group" — until the engine is rebuilt from the store. The
// store assigns the engine group id and returns the populated group.
func (n *localNode) CreateGroup(
	ctx context.Context, group domain.AccountGroup, caller domain.Caller,
) (domain.AccountGroup, error) {
	if err := n.beginEngineRestart(); err != nil {
		return domain.AccountGroup{}, err
	}
	defer n.endEngineRestart()

	created, err := n.realm.CreateGroup(ctx, group)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("create group: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("rebuild engine after group create: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateGroup,
		Detail: fmt.Sprintf("create group %s", group.Code),
	}); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("audit create group: %w", err)
	}
	return created, nil
}

// ListGroups returns every persisted group.
func (n *localNode) ListGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	groups, err := n.realm.ListGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return groups, nil
}

// ListGroupRows returns persisted groups matching filter.
func (n *localNode) ListGroupRows(
	ctx context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	page, err := n.realm.ListGroupRows(ctx, filter)
	if err != nil {
		return store.GroupListPage{}, fmt.Errorf("list group rows: %w", err)
	}
	return page, nil
}

// GetGroup returns the group and its member accounts. The bool is false when no
// such group exists.
func (n *localNode) GetGroup(
	ctx context.Context, code string,
) (domain.AccountGroup, []domain.Account, bool, error) {
	group, ok, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, nil, false, fmt.Errorf("get group: %w", err)
	}
	if !ok {
		return domain.AccountGroup{}, nil, false, nil
	}
	members, err := n.realm.ListGroupAccounts(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, nil, false, fmt.Errorf("list group accounts: %w", err)
	}
	return group, members, true, nil
}

// SetGroupNotes replaces a group's notes in the store and audits the action.
// If no group record exists yet (e.g. the group is known only via account
// membership), a default record is created first so the update succeeds.
//
// Auto-creating that record must also register the group in the engine, or the
// live resolver would not know it and a later group move into this code would
// reject as "unknown group". The ensure runs under the exclusive engine-restart
// gate, which takes the mutation lock internally, so it must run before
// beginMutation (never while mutate is held) or it self-deadlocks; the notes
// write then runs under beginMutation and finds the row the ensure created.
func (n *localNode) SetGroupNotes(
	ctx context.Context, code, notes string, caller domain.Caller,
) error {
	if err := n.ensureGroupRegisteredExclusive(ctx, code); err != nil {
		return fmt.Errorf("ensure group for set notes: %w", err)
	}

	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetGroupNotes(ctx, code, notes); err != nil {
		return fmt.Errorf("set group notes: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetGroupNotes,
		Detail: fmt.Sprintf("set notes group %s", code),
	}); err != nil {
		return fmt.Errorf("audit set group notes: %w", err)
	}
	return nil
}

// UpdateGroup replaces a group's public code and title, rebuilds the engine
// resolver, and audits the change.
func (n *localNode) UpdateGroup(
	ctx context.Context,
	oldCode string,
	group domain.AccountGroup,
	caller domain.Caller,
) (domain.AccountGroup, error) {
	if err := n.beginEngineRestart(); err != nil {
		return domain.AccountGroup{}, err
	}
	defer n.endEngineRestart()

	prev, ok, err := n.realm.GetGroup(ctx, oldCode)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("read group for update: %w", err)
	}
	if !ok {
		return domain.AccountGroup{},
			fmt.Errorf("group %q: %w", oldCode, domain.ErrNotFound)
	}
	updated, err := n.realm.UpdateGroup(ctx, oldCode, group)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("update group: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return domain.AccountGroup{},
			fmt.Errorf("rebuild engine after group update: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionUpdateGroup,
		Detail: fmt.Sprintf(
			"update group %s -> %s",
			prev.Code,
			updated.Code,
		),
	}); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("audit update group: %w", err)
	}
	return updated, nil
}

// SetGroupBlocked blocks or unblocks the group in the store, then the engine,
// reverts the store on engine failure, and audits the action. If no group
// record exists yet (e.g. the group is known only via account membership), a
// default record is created first so the operation succeeds.
//
// The block spans every member account, so a group has no single account lane
// to serialize on; instead the whole op runs under the exclusive engine-restart
// gate, which quiesces every account lane. That gate serves two ends: a missing
// group is auto-created and the engine rebuilt from the store so the resolver
// knows it before the block runs, and the group effect is ordered against every
// member-account fill, report, and block. Concurrent engine-restart-class admin
// requests are rejected with domain.ErrEngineRestarting; in-lane requests (fills,
// reports, account blocks) block on the gate and proceed once the group block
// completes.
func (n *localNode) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string, caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	prev, existed, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return fmt.Errorf("read group for block: %w", err)
	}
	if !existed {
		if err := n.ensureGroupRecordLocked(ctx, code); err != nil {
			return fmt.Errorf("ensure group for block: %w", err)
		}
		// The auto-created record is unknown to the resolver until the engine is
		// rebuilt from the store, so rebuild before the block runs.
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return fmt.Errorf("rebuild engine after group ensure: %w", err)
		}
	}

	if err := n.realm.SetGroupBlocked(ctx, code, blocked, reason); err != nil {
		return fmt.Errorf("set group blocked: %w", err)
	}

	applyErr := n.engine.RunGroupSynchronized(ctx, code, func(lane engine.GroupLane) error {
		return n.applyGroupBlock(ctx, lane, code, blocked, reason)
	})
	if applyErr != nil {
		// Best-effort revert; the caller already surfaces the primary error.
		_ = n.realm.SetGroupBlocked(ctx, code, prev.Blocked, prev.BlockReason)
		return fmt.Errorf("apply group block: %w", applyErr)
	}

	action := domain.AuditActionBlockGroup
	detail := fmt.Sprintf("block group %s", code)
	if !blocked {
		action = domain.AuditActionUnblockGroup
		detail = fmt.Sprintf("unblock group %s", code)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: action,
		Detail: detail,
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit group block", "group", code,
			fmt.Errorf("audit group block: %w", err),
		)
	}
	return nil
}

// ensureGroupRecord creates an account_groups record for code if one does not
// already exist. ErrAlreadyExists is treated as success so the call is
// idempotent.
func (n *localNode) ensureGroupRecordLocked(ctx context.Context, code string) error {
	_, err := n.realm.CreateGroup(ctx, domain.AccountGroup{Code: code})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return err
	}
	return nil
}

// applyGroupBlock applies the desired blocked state for a group to the engine
// through the supplied group view. SetGroupBlocked passes the live engine under
// the exclusive restart gate; the business CSV import passes a group lane.
func (n *localNode) applyGroupBlock(
	ctx context.Context, lane engine.GroupLane, code string, blocked bool, reason string,
) error {
	if blocked {
		return lane.BlockGroup(ctx, code, reason)
	}
	return lane.UnblockGroup(ctx, code)
}

// DeleteGroup removes the group from the store and audits the action. A group
// is store-only (membership lives on accounts), so there is no engine
// side-effect.
func (n *localNode) DeleteGroup(
	ctx context.Context, code string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteGroup(ctx, code); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionDeleteGroup,
		Detail: fmt.Sprintf("delete group %s", code),
	}); err != nil {
		return fmt.Errorf("audit delete group: %w", err)
	}
	return nil
}

// --- spot funds -------------------------------------------------------------

// ApplyBusinessCSVImport persists a prepared business CSV import atomically in
// the store and mirrors the selected rows into the live engine. The engine
// adjustments, group moves, and blocks are applied first; the selected rows are
// then persisted in one store transaction. That transaction is all-or-nothing,
// so a failed import writes nothing. Because the engine effects already ran when
// it fails, the engine is reconciled from the persisted store state so the
// engine and store never diverge.
func (n *localNode) ApplyBusinessCSVImport(
	ctx context.Context,
	in store.BusinessCSVImport,
	caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	rollback, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		return fmt.Errorf("capture business CSV rollback backup: %w", err)
	}

	seenBalances := make(map[string]struct{}, len(in.Balances))
	for _, balance := range in.Balances {
		balanceKey := businessCSVImportBalanceKey(balance)
		if _, ok := seenBalances[balanceKey]; ok {
			return fmt.Errorf("duplicate position snapshot %s/%s: %w",
				balance.Account, balance.Asset, domain.ErrInvalid)
		}
		seenBalances[balanceKey] = struct{}{}
	}

	ensuredGroups := make(map[string]bool)
	pendingAccounts := make(map[string]domain.Account)
	type accountGroupMove struct {
		account domain.AccountID
		old     string
		next    string
	}
	groupMoves := make([]accountGroupMove, 0)
	for _, row := range in.Groups {
		if row.Exists {
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action: domain.AuditActionSetGroupNotes,
				Detail: fmt.Sprintf("set notes group %s", row.Group.Code),
			}))
		} else {
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action: domain.AuditActionCreateGroup,
				Detail: fmt.Sprintf("create group %s", row.Group.Code),
			}))
		}
		action := domain.AuditActionBlockGroup
		detail := fmt.Sprintf("block group %s", row.Group.Code)
		if !row.Group.Blocked {
			action = domain.AuditActionUnblockGroup
			detail = fmt.Sprintf("unblock group %s", row.Group.Code)
		}
		in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
			Action: action,
			Detail: detail,
		}))
	}

	for _, row := range in.Accounts {
		accountKey := businessCSVImportAccountKey(row.Account)
		prev := domain.Account{Code: row.Account.Code}
		if row.Exists {
			if pending, ok := pendingAccounts[accountKey]; ok {
				prev = pending
			} else {
				var ok bool
				var err error
				prev, ok, err = n.realm.GetAccount(ctx, row.Account.Code)
				if err != nil {
					return fmt.Errorf("read account for business CSV import: %w", err)
				}
				if !ok {
					return fmt.Errorf("account %q: %w", row.Account.Code, domain.ErrNotFound)
				}
			}
		}
		if row.Account.GroupCode != "" && !ensuredGroups[row.Account.GroupCode] {
			_, ok, err := n.realm.GetGroup(ctx, row.Account.GroupCode)
			if err != nil {
				return fmt.Errorf("read group for business CSV import: %w", err)
			}
			if !ok {
				in.Groups = append(in.Groups, store.BusinessCSVImportGroup{
					Group: domain.AccountGroup{Code: row.Account.GroupCode},
				})
			}
			ensuredGroups[row.Account.GroupCode] = true
		}
		if prev.GroupCode != row.Account.GroupCode {
			groupMoves = append(groupMoves, accountGroupMove{
				account: row.Account.Code,
				old:     prev.GroupCode,
				next:    row.Account.GroupCode,
			})
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action:  domain.AuditActionSetGroup,
				Account: row.Account.Code,
				Detail:  setAccountGroupDetail(row.Account.Code, row.Account.GroupCode),
			}))
		}
		// Account blocks for CSV-created accounts run after the import rebuilds
		// the resolver; see the engine-effects phase below.
		if !row.Exists {
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action:  domain.AuditActionCreateAccount,
				Account: row.Account.Code,
				Detail:  fmt.Sprintf("create account %s", row.Account.Code),
			}))
		}
		in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
			Action:  domain.AuditActionSetNotes,
			Account: row.Account.Code,
			Detail:  fmt.Sprintf("set notes account %s", row.Account.Code),
		}))
		action := domain.AuditActionBlock
		detail := fmt.Sprintf("block account %s", row.Account.Code)
		if !row.Account.Blocked {
			action = domain.AuditActionUnblock
			detail = fmt.Sprintf("unblock account %s", row.Account.Code)
		}
		in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
			Action:  action,
			Account: row.Account.Code,
			Detail:  detail,
		}))
		pendingAccounts[accountKey] = row.Account
	}

	preStore := store.BusinessCSVImport{
		Groups:   append([]store.BusinessCSVImportGroup(nil), in.Groups...),
		Accounts: append([]store.BusinessCSVImportAccount(nil), in.Accounts...),
	}
	if len(preStore.Groups) > 0 || len(preStore.Accounts) > 0 {
		if err := n.realm.ApplyBusinessCSVImport(ctx, preStore); err != nil {
			return n.rollbackStore(ctx, rollback, fmt.Errorf("apply business CSV dictionaries: %w", err))
		}
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("rebuild engine after business CSV dictionaries: %w", err))
		}
		for i := range in.Groups {
			in.Groups[i].Exists = true
		}
		for i := range in.Accounts {
			in.Accounts[i].Exists = true
		}
	}

	for _, row := range in.Groups {
		applyErr := n.engine.RunGroupSynchronized(ctx, row.Group.Code,
			func(lane engine.GroupLane) error {
				return n.applyGroupBlock(ctx, lane, row.Group.Code,
					row.Group.Blocked, row.Group.BlockReason)
			})
		if applyErr != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("apply group block: %w", applyErr))
		}
	}
	for _, row := range in.Accounts {
		applyErr := n.engine.RunAccountSynchronized(ctx, row.Account.Code,
			func(lane engine.AccountLane) error {
				return n.applyBlock(ctx, lane, row.Account.Code,
					row.Account.Blocked, row.Account.BlockReason)
			})
		if applyErr != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("apply account block: %w", applyErr))
		}
	}
	for _, move := range groupMoves {
		if err := n.applyGroupMove(ctx, n.engine, move.account, move.old, move.next); err != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("apply account group: %w", err))
		}
	}

	balanceGroups := make(map[domain.AccountID][]domain.Balance)
	balanceAccounts := make([]domain.AccountID, 0)
	for _, balance := range in.Balances {
		if _, ok := balanceGroups[balance.Account]; !ok {
			balanceAccounts = append(balanceAccounts, balance.Account)
		}
		balanceGroups[balance.Account] = append(balanceGroups[balance.Account], balance)
	}
	for _, account := range balanceAccounts {
		balances := balanceGroups[account]
		reqs := make([]domain.AdjustmentRequest, 0, len(balances))
		for _, balance := range balances {
			reqs = append(reqs, snapshotAdjustmentRequest(balance))
		}
		var results []engine.AdjustmentResult
		var batchReject *engine.AdjustmentBatchReject
		if err := n.engine.RunAccountSynchronized(ctx, account, func(lane engine.AccountLane) error {
			var err error
			results, batchReject, err = lane.ApplyAccountAdjustmentBatch(ctx, account, reqs)
			return err
		}); err != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("apply position snapshot adjustment: %w", err))
		}
		if batchReject != nil {
			return n.rollbackStoreAndEngine(ctx, rollback,
				fmt.Errorf("position snapshot adjustment batch for account %s rejected: %s: %w",
					account, batchReject.Reason, domain.ErrInvalid))
		}
		if len(results) == 0 {
			continue
		}
		for i, result := range results {
			if i >= len(balances) {
				return n.rollbackStoreAndEngine(ctx, rollback,
					fmt.Errorf("position snapshot adjustment batch for account %s returned extra outcome %d for %d requests: %w",
						account, len(results), len(balances), domain.ErrInvalid))
			}
			balance := balances[i]
			req := reqs[i]
			rec := domain.AccountAdjustmentRecord{
				Account:   balance.Account,
				Source:    caller.Source,
				Principal: caller.Principal,
				Request:   req,
				Accepted:  result.Accepted,
				Rejected:  result.Rejected,
				Asset:     balance.Asset,
			}
			if result.Rejected != nil {
				return n.rollbackStoreAndEngine(ctx, rollback,
					fmt.Errorf("position %s/%s snapshot adjustment rejected: %s: %w",
						balance.Account, balance.Asset, result.Rejected.Reason, domain.ErrInvalid))
			}
			if adjustmentResultNoChange(result) {
				continue
			}
			in.Adjustments = append(in.Adjustments, rec)
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action:  domain.AuditActionAdjustment,
				Account: balance.Account,
				Detail:  importPositionSnapshotDetail(balance, result.Accepted != nil),
			}))
		}
	}

	if err := n.realm.ApplyBusinessCSVImport(ctx, in); err != nil {
		return n.rollbackStoreAndEngine(ctx, rollback,
			fmt.Errorf("apply business CSV import: %w", err))
	}
	return nil
}

func businessCSVImportAccountKey(account domain.Account) string {
	return string(account.Code)
}

func businessCSVImportBalanceKey(balance domain.Balance) string {
	return string(balance.Account) + "\x00" + balance.Asset
}

func (n *localNode) auditEntry(
	caller domain.Caller, entry store.AuditEntry,
) store.AuditEntry {
	entry.Actor = caller.Principal
	entry.Source = caller.Source
	return entry
}

func snapshotAdjustmentRequest(snapshot domain.Balance) domain.AdjustmentRequest {
	return domain.AdjustmentRequest{
		Asset:             snapshot.Asset,
		AverageEntryPrice: snapshot.AverageEntryPrice,
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Available,
		},
		Held: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Held,
		},
		Incoming: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Incoming,
		},
	}
}

func (n *localNode) ensureAdjustmentExternalIDUnused(
	ctx context.Context, externalID domain.ExternalID,
) error {
	if externalID.IsZero() {
		return nil
	}
	page, err := n.realm.ListAdjustmentRows(ctx, store.AdjustmentListFilter{
		ExternalID: externalID,
		Page:       store.PageSpec{Limit: 1},
	})
	if err != nil {
		return fmt.Errorf("check adjustment external id: %w", err)
	}
	if len(page.Rows) > 0 {
		return fmt.Errorf("adjustment %q: %w", externalID, domain.ErrAlreadyExists)
	}
	return nil
}

// ensureAutoCreatedAccount creates the account when it does not exist yet so an
// adjustment, order, or execution report against a fresh account succeeds in
// one call instead of failing with an unknown-account error. The new account
// joins the default group (empty GroupCode, no group assigned); its id is
// validated the same way CreateAccount validates it, and the creation is
// audited like a standalone CreateAccount, naming operation as the trigger. It
// reports whether the account was newly created so the caller rebuilds the
// engine once before entering the account lane. It runs under the mutation lock
// the caller already holds and must not rebuild the engine itself: a rebuild
// stops the live engine's async runtime, which would deadlock if run inside a
// lane callback.
func (n *localNode) ensureAutoCreatedAccount(
	ctx context.Context, id domain.AccountID, operation string, caller domain.Caller,
) (bool, error) {
	if _, ok, err := n.realm.GetAccount(ctx, id); err != nil {
		return false, fmt.Errorf("read account for %s: %w", operation, err)
	} else if ok {
		return false, nil
	}
	if err := domain.ValidateAccountID(id); err != nil {
		return false, err
	}
	account, err := n.realm.CreateAccount(ctx, domain.Account{Code: id})
	if err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return false, nil
		}
		return false, fmt.Errorf("create account for %s: %w", operation, err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      account.Code,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("auto-created account %s by %s", account.Code, operation),
	}); err != nil {
		return false, fmt.Errorf("audit create account: %w", err)
	}
	return true, nil
}

// ensureAccountAndAssetsRegistered auto-creates the account and each named asset
// pre-lane and rebuilds the engine once when anything was newly created, so the
// live resolver knows the account and both assets before the caller enters the
// account lane. Empty asset codes are skipped. The rebuild swaps n.engine, so
// the caller must read n.engine again after this returns.
func (n *localNode) ensureAccountAndAssetsRegistered(
	ctx context.Context, id domain.AccountID, operation string,
	caller domain.Caller, assets ...string,
) error {
	accountCreated, err := n.ensureAutoCreatedAccount(ctx, id, operation, caller)
	if err != nil {
		return err
	}
	assetsCreated := false
	for _, code := range assets {
		if code == "" {
			continue
		}
		created, err := n.ensureAutoCreatedAsset(ctx, code, operation, caller)
		if err != nil {
			return err
		}
		assetsCreated = assetsCreated || created
	}
	if accountCreated || assetsCreated {
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return fmt.Errorf("rebuild engine after auto-create: %w", err)
		}
	}
	return nil
}

func (n *localNode) accountOrAssetsNeedAutoCreate(
	ctx context.Context, id domain.AccountID, assets ...string,
) (bool, error) {
	if _, ok, err := n.realm.GetAccount(ctx, id); err != nil {
		return false, fmt.Errorf("read account for auto-create check: %w", err)
	} else if !ok {
		return true, nil
	}
	for _, code := range assets {
		if code == "" {
			continue
		}
		if _, ok, err := n.realm.GetAsset(ctx, code); err != nil {
			return false, fmt.Errorf("read asset for auto-create check: %w", err)
		} else if !ok {
			return true, nil
		}
	}
	return false, nil
}

func (n *localNode) ensureAccountAndAssetsRegisteredExclusive(
	ctx context.Context, id domain.AccountID, operation string,
	caller domain.Caller, assets ...string,
) error {
	needed, err := n.accountOrAssetsNeedAutoCreate(ctx, id, assets...)
	if err != nil {
		return err
	}
	if !needed {
		return nil
	}
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()
	return n.ensureAccountAndAssetsRegistered(ctx, id, operation, caller, assets...)
}

// ApplyAdjustment applies one spot-funds adjustment through the engine, which
// is the authority for the resulting holdings. An adjustment to an account or
// asset that does not exist yet auto-creates it before applying, so an operator
// can fund a fresh account or a fresh asset in one call. On accept it writes the
// recomputed balance snapshot and the accepted record; on reject it records the
// rejected adjustment and leaves balances unchanged. If the engine returns no
// account modification, the call returns ErrNoChange without recording an
// adjustment or adjustment audit.
func (n *localNode) ApplyAdjustment(
	ctx context.Context, key Key, externalID domain.ExternalID,
	req domain.AdjustmentRequest, caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	// Auto-create the account and asset and rebuild the engine before entering
	// the lane, so the live resolver knows them when RunAccountSynchronized
	// resolves the account and so the rebuild never stops a live lane's runtime.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "adjustment", caller, req.Asset,
	); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := n.ensureAdjustmentExternalIDUnused(ctx, externalID); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	defer done()

	var stored domain.AccountAdjustmentRecord
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		result, err := lane.ApplyAccountAdjustment(ctx, key.Account, req)
		if err != nil {
			return fmt.Errorf("apply adjustment: %w", err)
		}
		if adjustmentResultNoChange(result) {
			return domain.ErrNoChange
		}

		// A non-zero externalID is the caller-supplied handle, carried verbatim onto
		// the record so the store uses it (else the store mints one on append).
		rec := domain.AccountAdjustmentRecord{
			ExternalID: externalID,
			Account:    key.Account,
			Source:     caller.Source,
			Principal:  caller.Principal,
			Request:    req,
			Accepted:   result.Accepted,
			Rejected:   result.Rejected,
			Asset:      req.Asset,
		}

		var balance *domain.Balance
		var deleteBalance *store.BalanceKey
		if result.Accepted != nil {
			var err error
			balance, deleteBalance, _, err =
				n.adjustedBalanceCommand(ctx, key, req, *result.Accepted)
			if err != nil {
				return err
			}
		}

		audit := n.auditEntry(caller, store.AuditEntry{
			Action:  domain.AuditActionAdjustment,
			Account: key.Account,
			Asset:   req.Asset,
			Detail:  adjustmentDetail(key.Account, req.Asset, result.Accepted != nil),
		})
		stored, err = n.realm.RecordAccountAdjustment(ctx, store.AccountAdjustmentPersistence{
			UpsertBalance: balance,
			DeleteBalance: deleteBalance,
			Adjustment:    rec,
			Audit:         audit,
		})
		if err != nil {
			return n.fatalPostEnginePersistence(
				"record account adjustment",
				accountID,
				fmt.Errorf("record adjustment: %w", err),
			)
		}
		return nil
	}); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	return stored, nil
}

// ImportPositionSnapshot imports a persisted balance snapshot through the
// engine adjustment path, then stores the full snapshot. The engine has no
// setter for cumulative realized P&L, so the adjustment synchronizes
// available/held/incoming/average-entry-price while Officer persists the
// historical realized_pnl value from the snapshot.
func (n *localNode) ImportPositionSnapshot(
	ctx context.Context, key Key, externalID domain.ExternalID,
	snapshot domain.Balance, caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	// Register the account and asset and rebuild pre-lane (see ApplyAdjustment).
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "position snapshot import", caller, snapshot.Asset,
	); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := n.ensureAdjustmentExternalIDUnused(ctx, externalID); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	defer done()

	var stored domain.AccountAdjustmentRecord
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		req := domain.AdjustmentRequest{
			Asset:             snapshot.Asset,
			AverageEntryPrice: snapshot.AverageEntryPrice,
			Balance: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Available,
			},
			Held: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Held,
			},
			Incoming: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Incoming,
			},
		}
		result, err := lane.ApplyAccountAdjustment(ctx, key.Account, req)
		if err != nil {
			return fmt.Errorf("apply position snapshot adjustment: %w", err)
		}
		if adjustmentResultNoChange(result) {
			return domain.ErrNoChange
		}

		rec := domain.AccountAdjustmentRecord{
			ExternalID: externalID,
			Account:    key.Account,
			Source:     caller.Source,
			Principal:  caller.Principal,
			Request:    req,
			Accepted:   result.Accepted,
			Rejected:   result.Rejected,
			Asset:      snapshot.Asset,
		}

		var balance *domain.Balance
		var deleteBalance *store.BalanceKey
		if result.Accepted != nil {
			_, _, err := n.realm.GetBalance(ctx, key.Account, snapshot.Asset)
			if err != nil {
				return fmt.Errorf("read balance for position snapshot: %w", err)
			}
			balanceValue := snapshot
			balanceValue.Account = key.Account
			balance, deleteBalance = balanceSnapshotCommand(balanceValue)
		}

		auditSnapshot := snapshot
		auditSnapshot.Account = key.Account
		audit := n.auditEntry(caller, store.AuditEntry{
			Action:  domain.AuditActionAdjustment,
			Account: key.Account,
			Detail:  importPositionSnapshotDetail(auditSnapshot, result.Accepted != nil),
		})
		stored, err = n.realm.RecordAccountAdjustment(ctx, store.AccountAdjustmentPersistence{
			UpsertBalance: balance,
			DeleteBalance: deleteBalance,
			Adjustment:    rec,
			Audit:         audit,
		})
		if err != nil {
			return n.fatalPostEnginePersistence(
				"record position snapshot adjustment",
				accountID,
				fmt.Errorf("record position snapshot adjustment: %w", err),
			)
		}
		return nil
	}); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	return stored, nil
}

func adjustmentResultNoChange(result engine.AdjustmentResult) bool {
	return result.Accepted == nil && result.Rejected == nil
}

func (n *localNode) adjustedBalanceCommand(
	ctx context.Context, key Key, req domain.AdjustmentRequest,
	outcome domain.AdjustmentOutcomeAccepted,
) (*domain.Balance, *store.BalanceKey, string, error) {
	prev, _, err := n.realm.GetBalance(ctx, key.Account, req.Asset)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read balance for adjustment: %w", err)
	}
	realizedPnl, err := domain.AddDecimals(prev.RealizedPnl, outcome.RealizedPnlDelta)
	if err != nil {
		return nil, nil, "", fmt.Errorf("accumulate realized pnl: %w", err)
	}
	balance := domain.Balance{
		Account:           key.Account,
		Asset:             req.Asset,
		Available:         pick(outcome.BalanceResult, prev.Available),
		Held:              pick(outcome.HeldResult, prev.Held),
		Incoming:          pick(outcome.IncomingResult, prev.Incoming),
		RealizedPnl:       realizedPnl,
		AverageEntryPrice: pick(req.AverageEntryPrice, prev.AverageEntryPrice),
	}
	upsert, deleteKey := balanceSnapshotCommand(balance)
	return upsert, deleteKey, prev.AverageEntryPrice, nil
}

func balanceSnapshotCommand(balance domain.Balance) (*domain.Balance, *store.BalanceKey) {
	if balanceIsEmpty(balance) {
		return nil, &store.BalanceKey{Account: balance.Account, Asset: balance.Asset}
	}
	return &balance, nil
}

func (n *localNode) persistAdjustedBalance(
	ctx context.Context, key Key, req domain.AdjustmentRequest, outcome domain.AdjustmentOutcomeAccepted,
) error {
	balance, deleteBalance, _, err := n.adjustedBalanceCommand(ctx, key, req, outcome)
	if err != nil {
		return err
	}
	if deleteBalance != nil {
		err := n.realm.DeleteBalance(ctx, deleteBalance.Account, deleteBalance.Asset)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("delete empty adjusted balance: %w", err)
		}
		return nil
	}
	if balance != nil {
		if err := n.realm.UpsertBalance(ctx, *balance); err != nil {
			return fmt.Errorf("upsert adjusted balance: %w", err)
		}
	}
	return nil
}

func balanceIsEmpty(balance domain.Balance) bool {
	return decimalZeroOrEmpty(balance.Available) &&
		decimalZeroOrEmpty(balance.Held) &&
		decimalZeroOrEmpty(balance.Incoming) &&
		decimalZeroOrEmpty(balance.RealizedPnl) &&
		decimalZeroOrEmpty(balance.AverageEntryPrice)
}

func decimalZeroOrEmpty(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := decimal.NewFromString(value)
	return err == nil && parsed.IsZero()
}

// balanceSettlementsFrom maps the engine's per-asset outcomes onto the domain
// settlement carrier RecordOrderSettlement consumes. The store persists the
// engine-returned result fields and accumulates realized-P&L deltas inside the
// settlement tx. The engine emits at most one outcome per asset (see
// engine.BalanceOutcome), so the slice carries no duplicate-asset entries that
// would double-count realized P&L.
func balanceSettlementsFrom(outcomes []engine.BalanceOutcome) []domain.BalanceSettlement {
	if len(outcomes) == 0 {
		return nil
	}
	settlements := make([]domain.BalanceSettlement, 0, len(outcomes))
	for _, outcome := range outcomes {
		settlements = append(settlements, domain.BalanceSettlement{
			Asset:   outcome.Asset,
			Outcome: outcome.Outcome,
		})
	}
	return settlements
}

func accountBlockSettlementsFrom(
	order domain.ExternalID, blocks []domain.ExecutionAccountBlock,
) []domain.ExecutionAccountBlock {
	if len(blocks) == 0 {
		return nil
	}
	settlements := make([]domain.ExecutionAccountBlock, 0, len(blocks))
	for _, block := range blocks {
		block.Reason = engineBlockReason(order, block)
		settlements = append(settlements, block)
	}
	return settlements
}

// fillSettlementEvent builds one lifecycle event stamped with the caller for a
// fill/settlement tx. The store assigns the id and timestamp on append.
func fillSettlementEvent(
	order domain.ExternalID, typ domain.OrderEventType, caller domain.Caller, payload domain.OrderEventPayload,
) domain.OrderEvent {
	return domain.OrderEvent{
		Order:     order,
		Type:      typ,
		Source:    caller.Source,
		Principal: caller.Principal,
		Payload:   payload,
	}
}

func stampExecutionReportPersistence(
	persistence engine.ExecutionReportPersistence, caller domain.Caller,
) engine.ExecutionReportPersistence {
	for i := range persistence.Events {
		persistence.Events[i].Source = caller.Source
		persistence.Events[i].Principal = caller.Principal
	}
	if persistence.Trade != nil {
		persistence.Trade.Source = caller.Source
		persistence.Trade.Principal = caller.Principal
	}
	return persistence
}

func accountBlockPayload(blocks []domain.ExecutionAccountBlock) domain.OrderEventPayload {
	if len(blocks) == 0 {
		return domain.OrderEventPayload{}
	}
	// The engine currently emits at most one account block per fill. Keep the
	// event payload singular and raw: account state stores engineBlockReason
	// with order context, while this event preserves the engine block contract.
	block := blocks[0]
	return domain.OrderEventPayload{
		RejectCode:    block.Code,
		RejectScope:   "account",
		RejectReason:  block.Reason,
		RejectDetails: block.Details,
	}
}

// pick returns next when it is non-empty, otherwise prev. It carries an
// unchanged balance field forward when the outcome reported no value for it.
func pick(next, prev string) string {
	if next != "" {
		return next
	}
	return prev
}

// ListBalances returns the balance rows filtered by the non-empty account and
// asset.
func (n *localNode) ListBalances(
	ctx context.Context, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	balances, err := n.realm.ListBalances(ctx, account, asset)
	if err != nil {
		return nil, fmt.Errorf("list balances: %w", err)
	}
	return balances, nil
}

// ListBalanceRows returns the balance rows filtered by the typed list filter.
func (n *localNode) ListBalanceRows(
	ctx context.Context, filter store.BalanceListFilter,
) (store.BalanceListPage, error) {
	balances, err := n.realm.ListBalanceRows(ctx, filter)
	if err != nil {
		return store.BalanceListPage{}, fmt.Errorf("list balance rows: %w", err)
	}
	return balances, nil
}

// GetBalance returns the balance for (account, asset).
func (n *localNode) GetBalance(
	ctx context.Context, account domain.AccountID, asset string,
) (domain.Balance, bool, error) {
	balance, ok, err := n.realm.GetBalance(ctx, account, asset)
	if err != nil {
		return domain.Balance{}, false, fmt.Errorf("get balance: %w", err)
	}
	return balance, ok, nil
}

// ListAdjustments returns the most recent n adjustments for an account.
func (n *localNode) ListAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.AccountAdjustmentRecord, error) {
	records, err := n.realm.ListAdjustments(ctx, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list adjustments: %w", err)
	}
	return records, nil
}

// ListAdjustmentRows returns adjustments matching filter.
func (n *localNode) ListAdjustmentRows(
	ctx context.Context, filter store.AdjustmentListFilter,
) (store.AdjustmentListPage, error) {
	page, err := n.realm.ListAdjustmentRows(ctx, filter)
	if err != nil {
		return store.AdjustmentListPage{}, fmt.Errorf("list adjustment rows: %w", err)
	}
	return page, nil
}

// --- trading ----------------------------------------------------------------

// SubmitOrder runs the pre-trade submit on the account lane and persists the
// order, submitted event, and engine outcome in one store transaction. On accept
// it records pre_trade_accepted and reservation_committed, persists the lock,
// balance outcomes, and committed status; on reject it records
// pre_trade_rejected and rejected status. An order for an account Officer does
// not know yet auto-creates it (like a fresh adjustment target), so submitting
// against an unknown account is processed rather than rejected as invalid.
func (n *localNode) SubmitOrder(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, error) {
	// Register the account and both order assets and rebuild pre-lane so the
	// resolver knows them before RunAccountSynchronized resolves the account.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "submit order", caller, o.BaseAsset, o.QuoteAsset,
	); err != nil {
		return domain.Order{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.Order{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, err
	}
	defer done()

	var order domain.Order
	var result engine.OrderResult
	engineApplied := false
	draft := submittedOrderDraft(key, o, caller)
	submitted := submittedOrderEvent(caller)
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		var err error
		order, err = n.realm.RecordOrderSubmission(
			ctx,
			draft,
			submitted,
			func(persisted domain.Order) (domain.OrderSettlement, error) {
				result, err = lane.SubmitOrder(ctx, persisted)
				if err != nil {
					return domain.OrderSettlement{}, fmt.Errorf("submit order: %w", err)
				}
				engineApplied = true
				if result.Accepted {
					return orderAcceptedSettlement(key, persisted, result, caller), nil
				}
				return orderRejectedSettlement(key, persisted, result.Rejects, caller), nil
			},
		)
		if err != nil {
			if engineApplied {
				return n.fatalPostEnginePersistence(
					"record order submission", accountID, err,
				)
			}
			return err
		}

		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:  domain.AuditActionSubmitOrder,
			Account: key.Account,
			Detail:  submitOrderDetail(order, result.Accepted),
		}); err != nil {
			return n.fatalPostEnginePersistence(
				"audit submit order",
				accountID,
				fmt.Errorf("audit submit order: %w", err),
			)
		}
		return nil
	}); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func orderAcceptedSettlement(
	key Key, order domain.Order, result engine.OrderResult, caller domain.Caller,
) domain.OrderSettlement {
	return domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusCommitted,
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventReservationCommitted, caller, domain.OrderEventPayload{}),
		},
		Lock:    result.Lock,
		SetLock: true,
	}
}

func orderRejectedSettlement(
	key Key, order domain.Order, rejects []domain.OrderReject, caller domain.Caller,
) domain.OrderSettlement {
	payload := domain.OrderEventPayload{}
	if len(rejects) > 0 {
		r := rejects[0]
		payload.RejectCode = r.Code
		payload.RejectScope = r.Scope
		payload.RejectPolicy = r.Policy
		payload.RejectReason = r.Reason
		payload.RejectDetails = r.Details
	}
	return domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusRejected,
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeRejected, caller, payload),
		},
	}
}

// SubmitHold records the order, runs the engine pre-trade keeping the
// reservation held, and persists the accept/reject lifecycle. On accept the
// held amount stays reserved on engine storage; the order is left accepted and
// the result carries the approval id, lock, and settlement estimate. On reject
// the order is recorded rejected. The backend audits the issued approval.
func (n *localNode) SubmitHold(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, engine.HoldResult, error) {
	// Register the account and both order assets and rebuild pre-lane (see
	// SubmitOrder).
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "submit hold", caller, o.BaseAsset, o.QuoteAsset,
	); err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}
	defer done()

	var order domain.Order
	var result engine.HoldResult
	engineApplied := false
	draft := submittedOrderDraft(key, o, caller)
	submitted := submittedOrderEvent(caller)
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		var err error
		order, err = n.realm.RecordOrderSubmission(
			ctx,
			draft,
			submitted,
			func(persisted domain.Order) (domain.OrderSettlement, error) {
				result, err = lane.ReserveHold(ctx, persisted)
				if err != nil {
					return domain.OrderSettlement{}, fmt.Errorf("reserve hold: %w", err)
				}
				engineApplied = true
				if !result.Accepted {
					return orderRejectedSettlement(key, persisted, result.Rejects, caller), nil
				}
				return holdAcceptedSettlement(key, persisted, result, caller), nil
			},
		)
		if err != nil {
			if engineApplied {
				return n.fatalPostEnginePersistence(
					"record hold submission", accountID, err,
				)
			}
			return err
		}
		return nil
	}); err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}
	return order, result, nil
}

// SubmitImmediate records the order, runs the engine pre-trade and, on accept,
// commits and settles the fill in the same engine call at the captured lock
// price so the held amount nets to zero, then persists the filled lifecycle. On
// reject the order is recorded rejected. The backend audits the issued approval.
func (n *localNode) SubmitImmediate(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, engine.ImmediateResult, error) {
	// Register the account and both order assets and rebuild pre-lane (see
	// SubmitOrder).
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "submit immediate", caller, o.BaseAsset, o.QuoteAsset,
	); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	defer done()

	var order domain.Order
	var result engine.ImmediateResult
	engineApplied := false
	draft := submittedOrderDraft(key, o, caller)
	submitted := submittedOrderEvent(caller)
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		var err error
		order, err = n.realm.RecordOrderSubmission(
			ctx,
			draft,
			submitted,
			func(persisted domain.Order) (domain.OrderSettlement, error) {
				result, err = lane.SubmitImmediate(ctx, persisted)
				if err != nil {
					return domain.OrderSettlement{}, fmt.Errorf("submit immediate: %w", err)
				}
				engineApplied = true
				if !result.Accepted {
					return orderRejectedSettlement(key, persisted, result.Rejects, caller), nil
				}
				return immediateAcceptedSettlement(key, persisted, result, caller), nil
			},
		)
		if err != nil {
			if engineApplied {
				return n.fatalPostEnginePersistence(
					"record immediate submission", accountID, err,
				)
			}
			return err
		}
		if result.Accepted {
			if err := n.mirrorEngineBlocksAudit(ctx, order.ExternalID, result.Blocks); err != nil {
				return n.fatalPostEnginePersistence(
					"audit immediate engine blocks", accountID, err,
				)
			}
		}
		return nil
	}); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	return order, result, nil
}

// ConfirmHeld commits the held reservation through the engine and records the
// committed lifecycle on the order. The backend audits the confirmation. The
// returned bool reports whether force actually bypassed a terminal-order guard.
func (n *localNode) ConfirmHeld(
	ctx context.Context, order domain.ExternalID,
	approvalID string, caller domain.Caller, force bool,
) (domain.Order, bool, error) {
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, false, err
	}
	defer done()

	// Outer fetch is for routing only (the account keys the lane); the inner fetch
	// inside the lane re-reads the authoritative status under serialization with
	// fills and reports, closing the TOCTOU window on the terminal-order guard.
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("get order: %w", err)
	}
	accountID, err := n.accountDiagnosticID(ctx, detail.Order.Account)
	if err != nil {
		return domain.Order{}, false, err
	}
	var confirmed domain.Order
	var forcedBypass bool
	if err := eng.RunAccountSynchronized(ctx, detail.Order.Account, func(lane engine.AccountLane) error {
		detail, err := n.realm.GetOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("get order: %w", err)
		}
		if err := domain.RequireOrderModifiable(detail.Order.Status, force); err != nil {
			return fmt.Errorf(
				"order %s is in terminal status %q: %w",
				order, detail.Order.Status, err)
		}
		forcedBypass = force && domain.OrderStatusTerminal(detail.Order.Status)
		if detail.Order.Status == domain.OrderStatusCommitted {
			confirmed = detail.Order
			return nil
		}

		if err := lane.CommitHeld(ctx, approvalID); err != nil {
			if !errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("commit held: %w", err)
			}
			// The native handle is gone (post-restart): fall back to the persisted
			// intent. The fallback verifies the intent then drives the same atomic
			// resolve below.
			if err := n.commitHeldIntentFallback(ctx, approvalID); err != nil {
				return err
			}
		}
		// One atomic store transaction: intent flip + order status advance + the
		// reservation_committed event. Without force, AllowedFrom={accepted} is
		// TOCTOU-safe inside the same account lane as fills and reports. Force drops
		// the store guard because it means straight to the engine with no Officer
		// checks.
		allowedFrom := []domain.OrderStatus{domain.OrderStatusAccepted}
		if force {
			allowedFrom = nil
		}
		if err := n.realm.ResolveOrderReservation(ctx, domain.ReservationResolution{
			ApprovalID:  approvalID,
			IntentState: domain.ReservationIntentStateCommitted,
			OrderStatus: domain.OrderStatusCommitted,
			AllowedFrom: allowedFrom,
			Events: []domain.OrderEvent{{
				Order:     order,
				Type:      domain.OrderEventReservationCommitted,
				Source:    caller.Source,
				Principal: caller.Principal,
			}},
			Order: order,
		}); err != nil {
			wrapped := fmt.Errorf("resolve confirm: %w", err)
			return n.fatalPostEnginePersistence(
				"resolve held confirmation", accountID, wrapped,
			)
		}
		detail, err = n.realm.GetOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("get order: %w", err)
		}
		confirmed = detail.Order
		return nil
	}); err != nil {
		return domain.Order{}, false, err
	}
	return confirmed, forcedBypass, nil
}

// CancelHeld rolls back the held reservation through the engine and records the
// cancelled lifecycle on the order. The backend audits the cancellation. The
// returned bool reports whether force actually bypassed a terminal-order guard.
func (n *localNode) CancelHeld(
	ctx context.Context, order domain.ExternalID,
	approvalID string, caller domain.Caller, force bool,
) (domain.Order, bool, error) {
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, false, err
	}
	defer done()

	// Outer fetch is for routing only; the inner fetch inside the lane re-reads
	// the authoritative status under serialization with fills and reports, closing
	// the TOCTOU window on the terminal-order guard.
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("get order: %w", err)
	}
	accountID, err := n.accountDiagnosticID(ctx, detail.Order.Account)
	if err != nil {
		return domain.Order{}, false, err
	}
	var cancelled domain.Order
	var forcedBypass bool
	if err := eng.RunAccountSynchronized(ctx, detail.Order.Account, func(lane engine.AccountLane) error {
		detail, err := n.realm.GetOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("get order: %w", err)
		}
		if err := domain.RequireOrderModifiable(detail.Order.Status, force); err != nil {
			return fmt.Errorf(
				"order %s is in terminal status %q: %w",
				order, detail.Order.Status, err)
		}
		forcedBypass = force && domain.OrderStatusTerminal(detail.Order.Status)

		if err := lane.RollbackHeld(ctx, approvalID); err != nil {
			if !errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("rollback held: %w", err)
			}
			// Native handle gone (post-restart): release the held balance effects
			// through the engine from the persisted intent. The intent flip + order
			// status + events are still done by the atomic resolve below.
			if err := n.rollbackHeldIntentFallback(ctx, lane, approvalID, accountID); err != nil {
				return err
			}
		}
		// One atomic store transaction: intent flip + cancelled status +
		// reservation_rolled_back and cancelled events. Without force,
		// AllowedFrom={accepted} is TOCTOU-safe inside the same account lane as fills
		// and reports. Force drops the store guard because it means straight to the
		// engine with no Officer checks.
		allowedFrom := []domain.OrderStatus{domain.OrderStatusAccepted}
		if force {
			allowedFrom = nil
		}
		if err := n.realm.ResolveOrderReservation(ctx, domain.ReservationResolution{
			ApprovalID:  approvalID,
			IntentState: domain.ReservationIntentStateRolledBack,
			OrderStatus: domain.OrderStatusCancelled,
			AllowedFrom: allowedFrom,
			Events: []domain.OrderEvent{
				{
					Order:     order,
					Type:      domain.OrderEventReservationRolledBack,
					Source:    caller.Source,
					Principal: caller.Principal,
				},
				{
					Order:     order,
					Type:      domain.OrderEventCancelled,
					Source:    caller.Source,
					Principal: caller.Principal,
				},
			},
			Order: order,
		}); err != nil {
			wrapped := fmt.Errorf("resolve cancel: %w", err)
			return n.fatalPostEnginePersistence(
				"resolve held cancellation", accountID, wrapped,
			)
		}
		detail, err = n.realm.GetOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("get order: %w", err)
		}
		cancelled = detail.Order
		return nil
	}); err != nil {
		return domain.Order{}, false, err
	}
	return cancelled, forcedBypass, nil
}

// ReconcileOrphans reports persisted held reservation intents after boot. It
// runs once after the engine is built; held balance effects remain durable in
// the store and are not rolled back here.
func (n *localNode) ReconcileOrphans(ctx context.Context) (int, error) {
	eng, done, err := n.beginLane()
	if err != nil {
		return 0, err
	}
	defer done()
	count, err := eng.ReconcileOrphans(ctx)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// commitHeldIntentFallback handles a confirm whose native handle is gone (post
// restart): it verifies the persisted held intent still exists. The intent flip
// itself is left to the caller's atomic ResolveOrderReservation, so the fallback
// confirm has the same all-or-nothing guarantee and status guard as the normal
// path.
//
// If the TTL sweeper already rolled back the intent, openReservationIntent
// returns ErrNotFound. GetReservationIntent is then used to distinguish a
// swept (terminal) intent from one that never existed: the former returns
// ErrConflict (409) and the latter keeps ErrNotFound (404).
func (n *localNode) commitHeldIntentFallback(ctx context.Context, approvalID string) error {
	if _, err := n.openReservationIntent(ctx, approvalID); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("commit held after engine restart: %w", err)
		}
		// Not in the open set - check whether it exists in a terminal state.
		swept, found, lookupErr := n.realm.GetReservationIntent(ctx, approvalID)
		if lookupErr != nil {
			return fmt.Errorf("commit held after engine restart: %w", lookupErr)
		}
		if found && isTerminalIntentState(swept.State) {
			return fmt.Errorf("confirm reservation %q: %w", approvalID, domain.ErrConflict)
		}
		return fmt.Errorf("commit held after engine restart: %w", err)
	}
	return nil
}

func (n *localNode) rollbackHeldIntentFallback(
	ctx context.Context, lane engine.AccountLane, approvalID string, accountID string,
) error {
	intent, err := n.openReservationIntent(ctx, approvalID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("rollback held after engine restart: %w", err)
		}
		// Not in the open set - check whether it exists in a terminal state.
		swept, found, lookupErr := n.realm.GetReservationIntent(ctx, approvalID)
		if lookupErr != nil {
			return fmt.Errorf("rollback held after engine restart: %w", lookupErr)
		}
		if found && isTerminalIntentState(swept.State) {
			return fmt.Errorf("cancel reservation %q: %w", approvalID, domain.ErrConflict)
		}
		return fmt.Errorf("rollback held after engine restart: %w", err)
	}
	_, outcomes, err := decodeReservationIntentPayload(intent.ParamsJSON)
	if err != nil {
		return fmt.Errorf("decode held reservation payload: %w", err)
	}
	if len(outcomes) == 0 {
		return fmt.Errorf(
			"held reservation %q has no persisted balance outcomes to release: %w",
			approvalID, domain.ErrConflict)
	}
	key := Key{Account: intent.Account}
	for _, outcome := range outcomes {
		if err := n.releaseHeldBalance(ctx, lane, key, accountID, outcome); err != nil {
			return err
		}
	}
	// The intent flip and order status/events are left to the caller's atomic
	// ResolveOrderReservation. The balance release above stays outside that tx: it
	// is an engine adjustment with its own outcome and its own persistence, and it
	// is idempotent once the native handle is gone.
	return nil
}

// isTerminalIntentState reports whether s is a terminal reservation state
// (committed or rolled_back).
func isTerminalIntentState(s domain.ReservationIntentState) bool {
	return s == domain.ReservationIntentStateCommitted ||
		s == domain.ReservationIntentStateRolledBack
}

func (n *localNode) openReservationIntent(
	ctx context.Context, approvalID string,
) (domain.ReservationIntent, error) {
	intents, err := n.realm.ListOpenReservationIntents(ctx)
	if err != nil {
		return domain.ReservationIntent{}, fmt.Errorf("list open reservation intents: %w", err)
	}
	for _, intent := range intents {
		if intent.ApprovalID == approvalID {
			return intent, nil
		}
	}
	return domain.ReservationIntent{}, fmt.Errorf(
		"reservation %q: %w", approvalID, domain.ErrNotFound)
}

type reservationIntentPayload struct {
	Order    domain.Order            `json:"order"`
	Outcomes []engine.BalanceOutcome `json:"outcomes,omitempty"`
}

func decodeReservationIntentPayload(raw string) (
	domain.Order, []engine.BalanceOutcome, error,
) {
	var payload reservationIntentPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return domain.Order{}, nil, fmt.Errorf(
			"malformed reservation intent payload: %w", domain.ErrInvalid)
	}
	if len(payload.Outcomes) == 0 || payload.Order.ExternalID.IsZero() ||
		payload.Order.Account == "" {
		return domain.Order{}, nil, fmt.Errorf(
			"malformed reservation intent payload: %w", domain.ErrInvalid)
	}
	return payload.Order, payload.Outcomes, nil
}

func (n *localNode) releaseHeldBalance(
	ctx context.Context,
	lane engine.AccountLane,
	key Key,
	accountID string,
	held engine.BalanceOutcome,
) error {
	req, err := releaseHeldRequest(held)
	if err != nil {
		return err
	}
	if req.Balance == nil && req.Held == nil && req.Incoming == nil {
		return nil
	}
	result, err := lane.ApplyAccountAdjustment(ctx, key.Account, req)
	if err != nil {
		return fmt.Errorf("release held through engine: %w", err)
	}
	if result.Rejected != nil {
		return fmt.Errorf(
			"release held rejected by engine: %s: %w",
			result.Rejected.Reason, domain.ErrConflict)
	}
	if result.Accepted == nil {
		return fmt.Errorf("release held produced no accepted outcome: %w", domain.ErrConflict)
	}
	if err := n.persistAdjustedBalance(ctx, key, req, *result.Accepted); err != nil {
		return n.fatalPostEnginePersistence("release held balance", accountID, err)
	}
	return nil
}

func releaseHeldRequest(held engine.BalanceOutcome) (domain.AdjustmentRequest, error) {
	req := domain.AdjustmentRequest{Asset: held.Asset}
	var err error
	if req.Balance, err = inverseAdjustmentAmount(held.Outcome.BalanceDelta); err != nil {
		return domain.AdjustmentRequest{}, fmt.Errorf("release held available: %w", err)
	}
	if req.Held, err = inverseAdjustmentAmount(held.Outcome.HeldDelta); err != nil {
		return domain.AdjustmentRequest{}, fmt.Errorf("release held amount: %w", err)
	}
	if req.Incoming, err = inverseAdjustmentAmount(held.Outcome.IncomingDelta); err != nil {
		return domain.AdjustmentRequest{}, fmt.Errorf("release held incoming: %w", err)
	}
	return req, nil
}

func inverseAdjustmentAmount(delta string) (*domain.AdjustmentAmount, error) {
	if delta == "" {
		return nil, nil
	}
	parsed, err := decimal.NewFromString(delta)
	if err != nil {
		return nil, fmt.Errorf("delta %q is not a valid decimal: %w", delta, domain.ErrInvalid)
	}
	return &domain.AdjustmentAmount{
		Mode:  domain.AdjustmentModeDelta,
		Value: parsed.Neg().String(),
	}, nil
}

func submittedOrderDraft(key Key, o domain.Order, caller domain.Caller) domain.Order {
	o.Account = key.Account
	o.Source = caller.Source
	o.Principal = caller.Principal
	o.Status = domain.OrderStatusSubmitted
	return o
}

func submittedOrderEvent(caller domain.Caller) domain.OrderEvent {
	return fillSettlementEvent(
		domain.ExternalID(""),
		domain.OrderEventSubmitted,
		caller,
		domain.OrderEventPayload{},
	)
}

func holdAcceptedSettlement(
	key Key, order domain.Order, result engine.HoldResult, caller domain.Caller,
) domain.OrderSettlement {
	return domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusAccepted,
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
		},
		Lock:    result.Lock,
		SetLock: true,
	}
}

func immediateAcceptedSettlement(
	key Key, order domain.Order, result engine.ImmediateResult, caller domain.Caller,
) domain.OrderSettlement {
	fillPayload := accountBlockPayload(result.Blocks)
	fillPayload.FillQuantity = result.FillQuantity
	fillPayload.FillPrice = result.SettlementLockPrice
	fillPayload.FillLockPrice = result.SettlementLockPrice
	return domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventReservationCommitted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventFill, caller, fillPayload),
		},
		Trade: &domain.Trade{
			Order:      order.ExternalID,
			Account:    key.Account,
			Source:     caller.Source,
			Principal:  caller.Principal,
			BaseAsset:  order.BaseAsset,
			QuoteAsset: order.QuoteAsset,
			Side:       order.Side,
			Quantity:   result.FillQuantity,
			Price:      result.SettlementLockPrice,
			LockPrice:  result.SettlementLockPrice,
		},
		Blocks:  accountBlockSettlementsFrom(order.ExternalID, result.Blocks),
		Lock:    result.Lock,
		SetLock: true,
	}
}

// ensureAutoCreatedAssets creates the base and quote assets when missing and
// reports whether either was newly created. It is a pure store helper: it never
// rebuilds the engine, so the caller decides when the rebuild is safe (pre-lane
// for account-scoped operations).
func (n *localNode) ensureAutoCreatedAssets(
	ctx context.Context, baseAsset string, quoteAsset string,
	operation string, caller domain.Caller,
) (bool, error) {
	created := false
	for _, code := range []string{baseAsset, quoteAsset} {
		if code == "" {
			continue
		}
		assetCreated, err := n.ensureAutoCreatedAsset(ctx, code, operation, caller)
		if err != nil {
			return false, err
		}
		created = created || assetCreated
	}
	return created, nil
}

func (n *localNode) ensureAutoCreatedAsset(
	ctx context.Context, code string, operation string, caller domain.Caller,
) (bool, error) {
	if _, ok, err := n.realm.GetAsset(ctx, code); err != nil {
		return false, fmt.Errorf("read asset for %s: %w", operation, err)
	} else if ok {
		return false, nil
	}
	if err := domain.ValidateAsset(code); err != nil {
		return false, err
	}
	if err := n.realm.CreateAssetClass(ctx, domain.AssetClass{
		Code:  autoCreatedAssetClassCode,
		Title: autoCreatedAssetClassTitle,
		Notes: autoCreatedAssetClassNotes,
	}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return false, fmt.Errorf("create auto-created asset class: %w", err)
	}
	if err := n.realm.CreateAsset(ctx, domain.Asset{
		Code:       code,
		AssetClass: autoCreatedAssetClassCode,
	}); err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return false, nil
		}
		return false, fmt.Errorf("create auto-created asset %s: %w", code, err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateAsset,
		Asset:  code,
		Detail: fmt.Sprintf("auto-created asset %s by %s", code, operation),
	}); err != nil {
		return false, fmt.Errorf("audit auto-created asset: %w", err)
	}
	return true, nil
}

// ApplyExecutionReport applies every report through the engine, then persists
// the execution-report persistence in one atomic RecordOrderSettlement transaction:
// report events, optional trade, per-asset balances, engine-block UPDATEs, and
// the reflected status/leaves. The node does not commit or roll back held
// reservations around the report; any account effects must come from the engine
// persistence. The observational block-audit row is written post-commit. A report
// against an account Officer does not know yet auto-creates it, so it is
// processed rather than rejected as invalid.
func (n *localNode) ApplyExecutionReport(
	ctx context.Context, key Key, in domain.ExecutionReportInput, caller domain.Caller,
) (engine.ExecutionReportResult, error) {
	status := domain.ExecutionReportTargetStatus(in)
	if !domain.OrderStatusSupported(status) {
		return engine.ExecutionReportResult{}, fmt.Errorf(
			"invalid execution report status %q: %w", status, domain.ErrInvalid)
	}
	// The report is a fact from the venue; caller cancellation must not cancel it.
	ctx = context.WithoutCancel(ctx)

	routeDetail, err := n.realm.GetOrder(ctx, in.Order)
	if err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("get execution report order: %w", err)
	}
	account := routeDetail.Order.Account

	// Register the order account and assets and rebuild pre-lane so the resolver
	// knows them before RunAccountSynchronized resolves the account.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, account, "execution report", caller,
		routeDetail.Order.BaseAsset, routeDetail.Order.QuoteAsset,
	); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, account)
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	defer done()

	var result engine.ExecutionReportResult
	if err := eng.RunAccountSynchronized(ctx, account, func(lane engine.AccountLane) error {
		detail, err := n.realm.GetOrder(ctx, in.Order)
		if err != nil {
			return fmt.Errorf("get execution report order: %w", err)
		}
		if err := domain.RequireOrderModifiable(detail.Order.Status, in.Force); err != nil {
			return err
		}
		forcedTerminalBypass := in.Force && domain.OrderStatusTerminal(detail.Order.Status)
		in.Account = detail.Order.Account
		in.BaseAsset = detail.Order.BaseAsset
		in.QuoteAsset = detail.Order.QuoteAsset
		in.Side = detail.Order.Side
		if len(in.Lock) == 0 && len(detail.Order.Lock) > 0 {
			in.Lock = detail.Order.Lock
		}

		reservationApprovalID := ""
		reservationIntentState := domain.ReservationIntentState("")
		if detail.Order.Status == domain.OrderStatusAccepted {
			intent, found, err := n.realm.GetOpenReservationIntentByOrder(ctx, in.Order)
			if err != nil {
				return fmt.Errorf("get open reservation intent: %w", err)
			}
			if found {
				reservationApprovalID = intent.ApprovalID
				if executionReportCarriesFill(in) {
					reservationIntentState = domain.ReservationIntentStateCommitted
				} else if domain.OrderStatusTerminal(status) {
					reservationIntentState = domain.ReservationIntentStateRolledBack
				}
			}
		}

		applied, err := lane.ApplyExecutionReport(ctx, in)
		if err != nil {
			return fmt.Errorf("apply execution report: %w", err)
		}
		result = applied

		if result.Persistence == nil {
			return fmt.Errorf("apply execution report returned no persistence write set: %w", domain.ErrInvalid)
		}
		persistence := stampExecutionReportPersistence(*result.Persistence, caller)
		settlement := domain.OrderSettlement{
			Account:     in.Account,
			Order:       in.Order,
			OrderStatus: persistence.OrderStatus,
			Leaves:      persistence.Leaves,
			Balances:    persistence.Balances,
			Events:      persistence.Events,
			Trade:       persistence.Trade,
			Blocks:      accountBlockSettlementsFrom(in.Order, persistence.Blocks),
		}
		if reservationApprovalID != "" && reservationIntentState != "" {
			settlement.ReservationApprovalID = reservationApprovalID
			settlement.ReservationIntentState = reservationIntentState
		}
		if err := n.realm.RecordOrderSettlement(ctx, settlement); err != nil {
			return n.fatalPostEnginePersistence(
				"record execution report",
				accountID,
				fmt.Errorf("record execution report: %w", err),
			)
		}

		if err := n.mirrorEngineBlocksAudit(ctx, in.Order, result.Blocks); err != nil {
			return n.fatalPostEnginePersistence(
				"audit execution report engine blocks", accountID, err,
			)
		}

		detailText := executionReportDetail(in, status, len(result.Blocks))
		if forcedTerminalBypass {
			detailText += " forced=true"
		}
		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:  domain.AuditActionExecutionReport,
			Account: in.Account,
			Detail:  detailText,
		}); err != nil {
			return n.fatalPostEnginePersistence(
				"audit execution report",
				accountID,
				fmt.Errorf("audit execution report: %w", err),
			)
		}
		return nil
	}); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	return result, nil
}

func executionReportCarriesFill(in domain.ExecutionReportInput) bool {
	return in.FillQuantity != "" && in.FillPrice != ""
}

// PersistEventAttestation stamps the signed attestation envelope onto the
// order-history event named by its external id. It is write-once:
// store.PutEventAttestation sets the envelope only when none is present, so a
// retry never clobbers it. The event is already durable, so this mutation only
// adds the envelope; it never touches money or status.
func (n *localNode) PersistEventAttestation(
	ctx context.Context, _ Key, eventID domain.ExternalID, att domain.EventAttestation,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.PutEventAttestation(ctx, eventID, att); err != nil {
		return fmt.Errorf("persist event attestation: %w", err)
	}
	return nil
}

// GetOrder returns the order with its 1:1 signed approval, events and trades,
// addressed by the order's opaque external id.
func (n *localNode) GetOrder(
	ctx context.Context, order domain.ExternalID,
) (domain.OrderDetail, error) {
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.OrderDetail{}, fmt.Errorf("get order: %w", err)
	}
	return detail, nil
}

// ListOrders returns the most recent n orders for an account.
func (n *localNode) ListOrders(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.Order, error) {
	orders, err := n.realm.ListOrders(ctx, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	return orders, nil
}

// ListOrderRows returns orders matching filter.
func (n *localNode) ListOrderRows(
	ctx context.Context, filter store.OrderListFilter,
) (store.OrderListPage, error) {
	orders, err := n.realm.ListOrderRows(ctx, filter)
	if err != nil {
		return store.OrderListPage{}, fmt.Errorf("list order rows: %w", err)
	}
	return orders, nil
}

// ListAllOrders returns every matching order, newest first.
func (n *localNode) ListAllOrders(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Order, error) {
	orders, err := n.realm.ListAllOrders(ctx, account, source)
	if err != nil {
		return nil, fmt.Errorf("list all orders: %w", err)
	}
	return orders, nil
}

// CountOrders returns the total number of orders recorded in the realm.
func (n *localNode) CountOrders(ctx context.Context) (int, error) {
	count, err := n.realm.CountOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("count orders: %w", err)
	}
	return count, nil
}

// CountOrdersSince returns the number of orders in the realm at or after since.
func (n *localNode) CountOrdersSince(
	ctx context.Context, since time.Time,
) (int, error) {
	count, err := n.realm.CountOrdersSince(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("count orders since: %w", err)
	}
	return count, nil
}

// ListOrderEvents returns all events for the identified order, oldest first.
func (n *localNode) ListOrderEvents(
	ctx context.Context, order domain.ExternalID,
) ([]domain.OrderEvent, error) {
	events, err := n.realm.ListOrderEvents(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("list order events: %w", err)
	}
	return events, nil
}

// ListTrades returns the most recent n trades for an account.
func (n *localNode) ListTrades(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.Trade, error) {
	trades, err := n.realm.ListTrades(ctx, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list trades: %w", err)
	}
	return trades, nil
}

// ListAllTrades returns every matching trade, newest first.
func (n *localNode) ListAllTrades(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Trade, error) {
	trades, err := n.realm.ListAllTrades(ctx, account, source)
	if err != nil {
		return nil, fmt.Errorf("list all trades: %w", err)
	}
	return trades, nil
}

// ListTradeRows returns trades matching filter.
func (n *localNode) ListTradeRows(
	ctx context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	page, err := n.realm.ListTradeRows(ctx, filter)
	if err != nil {
		return store.TradeListPage{}, fmt.Errorf("list trade rows: %w", err)
	}
	return page, nil
}

// CheckOrder delegates the non-mutating pre-trade dry-run to the engine. The
// check mutates no state and writes no audit row, but it still runs through the
// account lane so dry-runs cannot observe account state out of order with fills.
func (n *localNode) CheckOrder(
	ctx context.Context, _ Key, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	eng, done := n.beginLaneRead()
	defer done()
	var out domain.CheckResult
	if err := eng.RunAccountSynchronized(ctx, probe.Account, func(lane engine.AccountLane) error {
		var err error
		out, err = lane.CheckOrder(ctx, probe)
		return err
	}); err != nil {
		return domain.CheckResult{}, err
	}
	return out, nil
}

// Close stops the engine and closes the store. It is idempotent: the engine's
// Stop and the store's Close are both idempotent.
func (n *localNode) Close() error {
	n.mutate.Lock()
	defer n.mutate.Unlock()
	n.engineMu.Lock()
	n.engine.Stop()
	n.engineMu.Unlock()
	if err := n.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
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
