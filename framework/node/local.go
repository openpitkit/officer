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
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionResetDatabase,
		Detail: "reset database from scratch",
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
