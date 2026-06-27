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
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/auth"
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
// mutate serializes every mutation so the store-write, engine-apply,
// revert-on-failure, and audit-append steps run atomically with respect to each
// other. Reads do not take it.
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

	mutate     sync.Mutex
	restarting atomic.Bool
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
	ctx context.Context, st store.Store, build engine.BuildFunc,
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

	n := &localNode{db: st, realm: realm, build: build}

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
	if backup.TouchesRuntime(opts.Scope) {
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
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return n.currentMarketDataSink(), err
	}
	// The reset audit lands in the freshly-recreated database, whose principal
	// dictionary is empty, so it carries no actor principal (the store rejects a
	// non-existent actor code). The caller's channel is preserved as the source so
	// the reset stays attributable, mirroring the system-sourced startup hydrate.
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
	if prev != nil {
		prev.Stop()
	}
}

func (n *localNode) currentMarketDataSink() marketdata.Sink {
	n.engineMu.RLock()
	defer n.engineMu.RUnlock()
	return n.engine.MarketDataSink()
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
	n.mutate.Lock()
	return nil
}

func (n *localNode) endEngineRestart() {
	n.mutate.Unlock()
	n.restarting.Store(false)
}

func (n *localNode) beginEngineRestartLocked() error {
	if !n.restarting.CompareAndSwap(false, true) {
		return fmt.Errorf(
			"engine restart in progress; mutating requests are rejected until rebuild completes: %w",
			domain.ErrEngineRestarting)
	}
	return nil
}

func (n *localNode) endEngineRestartLocked() {
	n.restarting.Store(false)
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

// CreateAccount persists a new account and audits the action. The account has
// no engine side-effect: an unblocked account is the engine's default, so there
// is nothing to apply or revert. The store assigns the engine account id and
// returns the populated account.
func (n *localNode) CreateAccount(
	ctx context.Context, key Key, caller domain.Caller,
) (domain.Account, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Account{}, err
	}
	defer n.endMutation()

	account, err := n.realm.CreateAccount(ctx, domain.Account{Code: key.Account})
	if err != nil {
		return domain.Account{}, fmt.Errorf("create account: %w", err)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      key.Account,
		AccountTitle: account.Title,
		Detail:       fmt.Sprintf("create account %s", key.Account),
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
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

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

	if applyErr := n.applyBlock(ctx, key.Account, blocked, reason); applyErr != nil {
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
		return fmt.Errorf("audit account block: %w", err)
	}
	return nil
}

// applyBlock applies the desired blocked state to the engine.
func (n *localNode) applyBlock(
	ctx context.Context, id domain.AccountID, blocked bool, reason string,
) error {
	if blocked {
		return n.engine.BlockAccount(ctx, id, reason)
	}
	return n.engine.UnblockAccount(ctx, id)
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
	if err := n.beginMutation(); err != nil {
		return nil, err
	}
	defer n.endMutation()

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
	if err := n.beginMutation(); err != nil {
		return nil, err
	}
	defer n.endMutation()

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
	if err := n.beginMutation(); err != nil {
		return nil, err
	}
	defer n.endMutation()

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
	if err := n.beginMutation(); err != nil {
		return nil, err
	}
	defer n.endMutation()

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
// the engine is rebuilt. Callers must hold mutate.
func (n *localNode) applyPolicyChangeLocked(
	ctx context.Context, policy string,
) (marketdata.Sink, error) {
	if err := n.reconfigurePolicy(ctx, policy); err != nil {
		if !errors.Is(err, domain.ErrNotImplemented) {
			return nil, err
		}
		if err := n.beginEngineRestartLocked(); err != nil {
			return nil, err
		}
		defer n.endEngineRestartLocked()
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
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

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
func (n *localNode) SetAccountGroup(
	ctx context.Context, key Key, groupCode string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

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

	// Ensure a group record exists for the new group before linking the account
	// to it (the store rejects an unknown group code). Treat ErrAlreadyExists as
	// success (idempotent).
	if groupCode != "" {
		if _, createErr := n.realm.CreateGroup(ctx, domain.AccountGroup{
			Code: groupCode,
		}); createErr != nil && !errors.Is(createErr, domain.ErrAlreadyExists) {
			return fmt.Errorf("ensure group record on set: %w", createErr)
		}
	}

	if err := n.realm.SetAccountGroup(ctx, key.Account, groupCode); err != nil {
		return fmt.Errorf("set account group: %w", err)
	}

	if applyErr := n.applyGroupMove(ctx, key.Account, prev.GroupCode, groupCode); applyErr != nil {
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
		return fmt.Errorf("audit set account group: %w", err)
	}
	return nil
}

// applyGroupMove moves one account between groups on the engine: it
// unregisters from oldGroup when set and registers into newGroup when set. An
// empty group code means "no group", so clearing only unregisters and setting
// from none only registers.
func (n *localNode) applyGroupMove(
	ctx context.Context, id domain.AccountID, oldGroup, newGroup string,
) error {
	accounts := []domain.AccountID{id}
	if oldGroup != "" {
		if err := n.engine.UnregisterGroup(ctx, accounts, oldGroup); err != nil {
			return err
		}
	}
	if newGroup != "" {
		if err := n.engine.RegisterGroup(ctx, accounts, newGroup); err != nil {
			// Re-register into the old group to undo the unregister above.
			if oldGroup != "" {
				_ = n.engine.RegisterGroup(ctx, accounts, oldGroup)
			}
			return err
		}
	}
	return nil
}

// SetAccountNotes replaces the account's notes in the store and audits the
// action. Notes never reach the engine, so there is no engine side-effect.
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

// DeleteAccount removes an account from the store, rebuilds the engine from the
// surviving rows, and audits the action.
func (n *localNode) DeleteAccount(
	ctx context.Context, key Key, force bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

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

// CreateGroup persists a new account group and audits the action. A group is
// store-only: membership lives on accounts, so there is no engine side-effect
// at creation. The store assigns the engine group id and returns the populated
// group.
func (n *localNode) CreateGroup(
	ctx context.Context, group domain.AccountGroup, caller domain.Caller,
) (domain.AccountGroup, error) {
	if err := n.beginMutation(); err != nil {
		return domain.AccountGroup{}, err
	}
	defer n.endMutation()

	created, err := n.realm.CreateGroup(ctx, group)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("create group: %w", err)
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
func (n *localNode) SetGroupNotes(
	ctx context.Context, code, notes string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.ensureGroupRecordLocked(ctx, code); err != nil {
		return fmt.Errorf("ensure group for set notes: %w", err)
	}

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

// SetGroupBlocked blocks or unblocks the group in the store, then the engine,
// reverts the store on engine failure, and audits the action. If no group
// record exists yet (e.g. the group is known only via account membership), a
// default record is created first so the operation succeeds.
func (n *localNode) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.ensureGroupRecordLocked(ctx, code); err != nil {
		return fmt.Errorf("ensure group for block: %w", err)
	}

	prev, ok, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return fmt.Errorf("read group for block: %w", err)
	}
	if !ok {
		return fmt.Errorf("group %q: %w", code, domain.ErrNotFound)
	}

	if err := n.realm.SetGroupBlocked(ctx, code, blocked, reason); err != nil {
		return fmt.Errorf("set group blocked: %w", err)
	}

	if applyErr := n.applyGroupBlock(ctx, code, blocked, reason); applyErr != nil {
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
		return fmt.Errorf("audit group block: %w", err)
	}
	return nil
}

// ensureGroupRecordLocked creates an account_groups record for code if one does
// not already exist. ErrAlreadyExists is treated as success so the call is
// idempotent. Callers must hold mutate.
func (n *localNode) ensureGroupRecordLocked(ctx context.Context, code string) error {
	_, err := n.realm.CreateGroup(ctx, domain.AccountGroup{Code: code})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return err
	}
	return nil
}

// applyGroupBlock applies the desired blocked state for a group to the engine.
func (n *localNode) applyGroupBlock(
	ctx context.Context, code string, blocked bool, reason string,
) error {
	if blocked {
		return n.engine.BlockGroup(ctx, code, reason)
	}
	return n.engine.UnblockGroup(ctx, code)
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
// the store and mirrors the selected rows into the live engine.
func (n *localNode) ApplyBusinessCSVImport(
	ctx context.Context,
	in store.BusinessCSVImport,
	caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	ensuredGroups := make(map[string]bool)
	pendingAccounts := make(map[string]domain.Account)
	registerGroups := make(map[string][]domain.AccountID)
	unregisterGroups := make(map[string][]domain.AccountID)
	for _, row := range in.Groups {
		if row.Group.Blocked {
			if err := n.applyGroupBlock(ctx, row.Group.Code, true, row.Group.BlockReason); err != nil {
				return fmt.Errorf("apply group block: %w", err)
			}
		} else if err := n.applyGroupBlock(ctx, row.Group.Code, false, ""); err != nil {
			return fmt.Errorf("apply group unblock: %w", err)
		}
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
			if prev.GroupCode != "" {
				unregisterGroups[prev.GroupCode] = append(
					unregisterGroups[prev.GroupCode], row.Account.Code,
				)
			}
			if row.Account.GroupCode != "" {
				registerGroups[row.Account.GroupCode] = append(
					registerGroups[row.Account.GroupCode], row.Account.Code,
				)
			}
			in.Audits = append(in.Audits, n.auditEntry(caller, store.AuditEntry{
				Action:  domain.AuditActionSetGroup,
				Account: row.Account.Code,
				Detail:  setAccountGroupDetail(row.Account.Code, row.Account.GroupCode),
			}))
		}
		if err := n.applyBlock(
			ctx, row.Account.Code, row.Account.Blocked, row.Account.BlockReason,
		); err != nil {
			return fmt.Errorf("apply account block: %w", err)
		}
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
	if err := n.applyGroupMovesBatch(ctx, unregisterGroups, registerGroups); err != nil {
		return fmt.Errorf("apply account groups: %w", err)
	}

	balanceGroups := make(map[domain.AccountID][]domain.Balance)
	balanceAccounts := make([]domain.AccountID, 0)
	seenBalances := make(map[string]struct{}, len(in.Balances))
	for _, balance := range in.Balances {
		balanceKey := businessCSVImportBalanceKey(balance)
		if _, ok := seenBalances[balanceKey]; ok {
			return fmt.Errorf("duplicate position snapshot %s/%s: %w",
				balance.Account, balance.Asset, domain.ErrInvalid)
		}
		seenBalances[balanceKey] = struct{}{}
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
		results, batchReject, err := n.engine.ApplyAccountAdjustmentBatch(ctx, account, reqs)
		if err != nil {
			return fmt.Errorf("apply position snapshot adjustment: %w", err)
		}
		if batchReject != nil {
			return fmt.Errorf("position snapshot adjustment batch for account %s rejected: %s: %w",
				account, batchReject.Reason, domain.ErrInvalid)
		}
		if len(results) != len(balances) {
			return fmt.Errorf("position snapshot adjustment batch for account %s returned %d outcomes for %d requests: %w",
				account, len(results), len(balances), domain.ErrInvalid)
		}
		for i, balance := range balances {
			req := reqs[i]
			result := results[i]
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
				return fmt.Errorf("position %s/%s snapshot adjustment rejected: %s: %w",
					balance.Account, balance.Asset, result.Rejected.Reason, domain.ErrInvalid)
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
		return fmt.Errorf("apply business CSV import: %w", err)
	}
	return nil
}

func (n *localNode) applyGroupMovesBatch(
	ctx context.Context,
	unregisterGroups map[string][]domain.AccountID,
	registerGroups map[string][]domain.AccountID,
) error {
	oldGroups := sortedGroupIDs(unregisterGroups)
	for _, group := range oldGroups {
		if err := n.engine.UnregisterGroup(ctx, unregisterGroups[group], group); err != nil {
			return err
		}
	}
	registeredGroups := make([]string, 0, len(registerGroups))
	for _, group := range sortedGroupIDs(registerGroups) {
		if err := n.engine.RegisterGroup(ctx, registerGroups[group], group); err != nil {
			for i := len(registeredGroups) - 1; i >= 0; i-- {
				registeredGroup := registeredGroups[i]
				_ = n.engine.UnregisterGroup(
					ctx, registerGroups[registeredGroup], registeredGroup,
				)
			}
			for _, oldGroup := range oldGroups {
				_ = n.engine.RegisterGroup(ctx, unregisterGroups[oldGroup], oldGroup)
			}
			return err
		}
		registeredGroups = append(registeredGroups, group)
	}
	return nil
}

func sortedGroupIDs(groups map[string][]domain.AccountID) []string {
	ids := make([]string, 0, len(groups))
	for id, accounts := range groups {
		if len(accounts) > 0 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
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

// ApplyAdjustment applies one spot-funds adjustment through the engine, which
// is the authority for the resulting holdings. On accept it writes the
// recomputed balance snapshot and the accepted record; on reject it records
// the rejected adjustment and leaves balances unchanged. Either way it audits
// the action.
func (n *localNode) ApplyAdjustment(
	ctx context.Context, key Key, externalID domain.ExternalID,
	req domain.AdjustmentRequest, caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	if err := n.beginMutation(); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	defer n.endMutation()

	result, err := n.engine.ApplyAccountAdjustment(ctx, key.Account, req)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("apply adjustment: %w", err)
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

	if result.Accepted != nil {
		if err := n.persistAdjustedBalance(ctx, key, req, *result.Accepted); err != nil {
			return domain.AccountAdjustmentRecord{}, err
		}
	}

	stored, err := n.realm.AppendAdjustment(ctx, rec)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("append adjustment: %w", err)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionAdjustment,
		Account: key.Account,
		Detail:  adjustmentDetail(key.Account, req.Asset, result.Accepted != nil),
	}); err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("audit adjustment: %w", err)
	}
	return stored, nil
}

// ImportPositionSnapshot imports a persisted balance snapshot through the
// engine adjustment path, then stores the full snapshot. The engine has no
// setter for cumulative realized P&L, so the adjustment synchronizes
// available/held/incoming/average-entry-price while Officer persists the
// historical realized_pnl value from the snapshot.
func (n *localNode) ImportPositionSnapshot(
	ctx context.Context, key Key, snapshot domain.Balance, caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	if err := n.beginMutation(); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	defer n.endMutation()

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
	result, err := n.engine.ApplyAccountAdjustment(ctx, key.Account, req)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("apply position snapshot adjustment: %w", err)
	}

	rec := domain.AccountAdjustmentRecord{
		Account:   key.Account,
		Source:    caller.Source,
		Principal: caller.Principal,
		Request:   req,
		Accepted:  result.Accepted,
		Rejected:  result.Rejected,
		Asset:     snapshot.Asset,
	}

	if result.Accepted != nil {
		balance := snapshot
		balance.Account = key.Account
		if err := n.realm.UpsertBalance(ctx, balance); err != nil {
			return domain.AccountAdjustmentRecord{}, fmt.Errorf("upsert position snapshot: %w", err)
		}
	}

	stored, err := n.realm.AppendAdjustment(ctx, rec)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("append position snapshot adjustment: %w", err)
	}

	auditSnapshot := snapshot
	auditSnapshot.Account = key.Account
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionAdjustment,
		Account: key.Account,
		Detail:  importPositionSnapshotDetail(auditSnapshot, result.Accepted != nil),
	}); err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("audit position snapshot adjustment: %w", err)
	}
	return stored, nil
}

// persistAdjustedBalance writes the new (account, asset) balance snapshot. The
// available/held/incoming fields are the engine-reported resulting absolutes
// (carried forward when the outcome did not report them). Realized P&L is the
// one accumulator: the outcome's per-operation delta is added to the stored
// value rather than overwritten with the engine's reported absolute, so the
// stored figure stays correct across operations (a fresh row starts at "0", so
// accumulated deltas equal the cumulative). The average-entry-price follows the
// request when it set one, else the previous stored value.
func (n *localNode) persistAdjustedBalance(
	ctx context.Context, key Key, req domain.AdjustmentRequest, outcome domain.AdjustmentOutcomeAccepted,
) error {
	prev, _, err := n.realm.GetBalance(ctx, key.Account, req.Asset)
	if err != nil {
		return fmt.Errorf("read balance for adjustment: %w", err)
	}
	realizedPnl, err := domain.AddDecimals(prev.RealizedPnl, outcome.RealizedPnlDelta)
	if err != nil {
		return fmt.Errorf("accumulate realized pnl: %w", err)
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
	if err := n.realm.UpsertBalance(ctx, balance); err != nil {
		return fmt.Errorf("upsert balance: %w", err)
	}
	return nil
}

// balanceSettlementsFrom maps the engine's per-asset fill outcomes onto the
// domain settlement carrier RecordOrderSettlement consumes. The store applies the
// same balance/held/incoming carry-forward and realized-pnl accumulation the
// former persistFillBalance did, but inside the settlement tx. The engine emits
// at most one outcome per asset (see engine.BalanceOutcome), so the slice carries
// no duplicate-asset entries that would double-count realized P&L.
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

// --- trading ----------------------------------------------------------------

// SubmitOrder records the order as submitted, runs the engine pre-trade, and
// persists the lifecycle. On accept it records pre_trade_accepted and
// reservation_committed events, persists the captured lock blob, and sets the
// order committed; on reject it records pre_trade_rejected and sets the order
// rejected. The order row is the durable trail, so a recorded order is never
// rolled back; the engine outcome only drives later events and status.
func (n *localNode) SubmitOrder(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Order{}, err
	}
	defer n.endMutation()

	order, err := n.recordSubmittedOrder(ctx, key, o, caller)
	if err != nil {
		return domain.Order{}, err
	}

	result, err := n.engine.SubmitOrder(ctx, order)
	if err != nil {
		return domain.Order{}, fmt.Errorf("submit order: %w", err)
	}

	if result.Accepted {
		order, err = n.recordOrderAccepted(ctx, key, order, result, caller)
	} else {
		order, err = n.recordOrderRejected(ctx, order, result.Rejects, caller)
	}
	if err != nil {
		return domain.Order{}, err
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSubmitOrder,
		Account: key.Account,
		Detail:  submitOrderDetail(order, result.Accepted),
	}); err != nil {
		return domain.Order{}, fmt.Errorf("audit submit order: %w", err)
	}
	return order, nil
}

// recordOrderAccepted records the accept path: pre_trade_accepted and
// reservation_committed events, the captured lock blob on the order, and the
// committed status.
func (n *localNode) recordOrderAccepted(
	ctx context.Context, key Key, order domain.Order, result engine.OrderResult, caller domain.Caller,
) (domain.Order, error) {
	if err := n.realm.RecordOrderSettlement(ctx, domain.OrderSettlement{
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
	}); err != nil {
		return domain.Order{}, fmt.Errorf("record order accepted: %w", err)
	}
	order.Lock = result.Lock
	order.Status = domain.OrderStatusCommitted
	return order, nil
}

// recordOrderRejected records the reject path: one pre_trade_rejected event
// carrying the first reject and the rejected status.
func (n *localNode) recordOrderRejected(
	ctx context.Context, order domain.Order, rejects []domain.OrderReject, caller domain.Caller,
) (domain.Order, error) {
	payload := domain.OrderEventPayload{}
	if len(rejects) > 0 {
		r := rejects[0]
		payload.RejectCode = r.Code
		payload.RejectScope = r.Scope
		payload.RejectPolicy = r.Policy
		payload.RejectReason = r.Reason
		payload.RejectDetails = r.Details
	}
	if err := n.appendOrderEvent(ctx, order.ExternalID, domain.OrderEventPreTradeRejected, caller, payload); err != nil {
		return domain.Order{}, err
	}
	if err := n.realm.UpdateOrderStatus(ctx, order.ExternalID, domain.OrderStatusRejected); err != nil {
		return domain.Order{}, fmt.Errorf("order status rejected: %w", err)
	}
	order.Status = domain.OrderStatusRejected
	return order, nil
}

// SubmitHold records the order, runs the engine pre-trade keeping the
// reservation held, and persists the accept/reject lifecycle. On accept the
// held amount stays reserved on engine storage; the order is left accepted and
// the result carries the approval id, lock, and settlement estimate. On reject
// the order is recorded rejected. The backend audits the issued approval.
func (n *localNode) SubmitHold(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, engine.HoldResult, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}
	defer n.endMutation()

	order, err := n.recordSubmittedOrder(ctx, key, o, caller)
	if err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}

	result, err := n.engine.ReserveHold(ctx, order)
	if err != nil {
		return domain.Order{}, engine.HoldResult{}, fmt.Errorf("reserve hold: %w", err)
	}

	if !result.Accepted {
		order, err = n.recordOrderRejected(ctx, order, result.Rejects, caller)
		if err != nil {
			return domain.Order{}, engine.HoldResult{}, err
		}
		return order, result, nil
	}

	// One atomic store transaction: pre_trade_accepted event, per-asset held
	// balances (realized-pnl accumulated inside the tx), lock blob, and the
	// accepted status. No trade, no blocks on the hold-accept path. AllowedFrom is
	// left empty: the order is fresh (submitted) so the status advance is
	// unguarded.
	if err := n.realm.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusAccepted,
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events:      []domain.OrderEvent{fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{})},
		Lock:        result.Lock,
		SetLock:     true,
	}); err != nil {
		return domain.Order{}, engine.HoldResult{}, fmt.Errorf("record hold accept: %w", err)
	}
	order.Lock = result.Lock
	order.Status = domain.OrderStatusAccepted
	return order, result, nil
}

// SubmitImmediate records the order, runs the engine pre-trade and, on accept,
// commits and settles the fill in the same engine call at the captured lock
// price so the held amount nets to zero, then persists the filled lifecycle. On
// reject the order is recorded rejected. The backend audits the issued approval.
func (n *localNode) SubmitImmediate(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, engine.ImmediateResult, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	defer n.endMutation()

	order, err := n.recordSubmittedOrder(ctx, key, o, caller)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}

	result, err := n.engine.SubmitImmediate(ctx, order)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, fmt.Errorf("submit immediate: %w", err)
	}

	if !result.Accepted {
		order, err = n.recordOrderRejected(ctx, order, result.Rejects, caller)
		if err != nil {
			return domain.Order{}, engine.ImmediateResult{}, err
		}
		return order, result, nil
	}

	fillQuantity := result.FillQuantity
	if fillQuantity == "" {
		fillQuantity = order.AmountValue
	}
	fillPayload := accountBlockPayload(result.Blocks)
	fillPayload.FillQuantity = fillQuantity
	fillPayload.FillPrice = result.SettlementLockPrice
	fillPayload.FillLockPrice = result.SettlementLockPrice
	// One atomic store transaction: pre_trade_accepted + reservation_committed +
	// fill events, per-asset balances (realized-pnl accumulated in-tx), the trade,
	// engine-block UPDATEs, lock blob, and the filled status. AllowedFrom guards
	// against terminal-status clobber (e.g. a duplicate fill for an already-filled
	// order). The block-audit rows are observational and stay a post-commit
	// best-effort write below.
	if err := n.realm.RecordOrderSettlement(ctx, domain.OrderSettlement{
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
			Quantity:   fillQuantity,
			Price:      result.SettlementLockPrice,
			LockPrice:  result.SettlementLockPrice,
		},
		Blocks:  accountBlockSettlementsFrom(order.ExternalID, result.Blocks),
		Lock:    result.Lock,
		SetLock: true,
	}); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, fmt.Errorf("record immediate fill: %w", err)
	}
	if err := n.mirrorEngineBlocksAudit(ctx, order.ExternalID, result.Blocks); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	order.Lock = result.Lock
	order.Status = domain.OrderStatusFilled
	return order, result, nil
}

// ConfirmHeld commits the held reservation through the engine and records the
// committed lifecycle on the order. The backend audits the confirmation.
func (n *localNode) ConfirmHeld(
	ctx context.Context, order domain.ExternalID,
	approvalID string, caller domain.Caller, force bool,
) (domain.Order, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Order{}, err
	}
	defer n.endMutation()

	if err := n.engine.CommitHeld(ctx, approvalID); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return domain.Order{}, fmt.Errorf("commit held: %w", err)
		}
		// The native handle is gone (post-restart): fall back to the persisted
		// intent. The fallback verifies the intent then drives the same atomic
		// resolve below, so the status guard still protects a fill that landed in
		// the crash window.
		if err := n.commitHeldIntentFallback(ctx, approvalID); err != nil {
			return domain.Order{}, err
		}
	}
	// One atomic store transaction: intent flip + order status advance + the
	// reservation_committed event. Without force, AllowedFrom={accepted} is
	// TOCTOU-safe: a concurrent fill that flipped the order to filled before this
	// tx yields ErrConflict and writes nothing. Force drops the entire
	// AllowedFrom store guard, including that concurrent-fill protection, because
	// it means straight to the engine with no Officer checks. The native commit
	// already happened; a store failure here does not re-resolve the handle.
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
		return domain.Order{}, fmt.Errorf("resolve confirm: %w", err)
	}
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	return detail.Order, nil
}

// CancelHeld rolls back the held reservation through the engine and records the
// cancelled lifecycle on the order. The backend audits the cancellation.
func (n *localNode) CancelHeld(
	ctx context.Context, order domain.ExternalID,
	approvalID string, caller domain.Caller, force bool,
) (domain.Order, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Order{}, err
	}
	defer n.endMutation()

	if err := n.engine.RollbackHeld(ctx, approvalID); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return domain.Order{}, fmt.Errorf("rollback held: %w", err)
		}
		// Native handle gone (post-restart): release the held balance effects
		// through the engine from the persisted intent. The intent flip + order
		// status + events are still done by the atomic resolve below.
		if err := n.rollbackHeldIntentFallback(ctx, approvalID); err != nil {
			return domain.Order{}, err
		}
	}
	// One atomic store transaction: intent flip + cancelled status +
	// reservation_rolled_back and cancelled events. Without force,
	// AllowedFrom={accepted} is TOCTOU-safe: a concurrent fill that flipped the
	// order to filled before this tx yields ErrConflict and writes nothing. Force
	// drops the entire AllowedFrom store guard, including that concurrent-fill
	// protection, because it means straight to the engine with no Officer checks.
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
		return domain.Order{}, fmt.Errorf("resolve cancel: %w", err)
	}
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	return detail.Order, nil
}

// ReconcileOrphans reports persisted held reservation intents after boot. It
// runs once after the engine is built; held balance effects remain durable in
// the store and are not rolled back here.
func (n *localNode) ReconcileOrphans(ctx context.Context) (int, error) {
	if err := n.beginMutation(); err != nil {
		return 0, err
	}
	defer n.endMutation()

	count, err := n.engine.ReconcileOrphans(ctx)
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
	ctx context.Context, approvalID string,
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
		if err := n.releaseHeldBalance(ctx, key, outcome); err != nil {
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
	if err := json.Unmarshal([]byte(raw), &payload); err == nil {
		if !payload.Order.ExternalID.IsZero() || payload.Order.Account != "" || payload.Outcomes != nil {
			return payload.Order, payload.Outcomes, nil
		}
	}
	var order domain.Order
	if err := json.Unmarshal([]byte(raw), &order); err != nil {
		return domain.Order{}, nil, err
	}
	return order, nil, nil
}

func (n *localNode) releaseHeldBalance(
	ctx context.Context, key Key, held engine.BalanceOutcome,
) error {
	req, err := releaseHeldRequest(held)
	if err != nil {
		return err
	}
	if req.Balance == nil && req.Held == nil && req.Incoming == nil {
		return nil
	}
	result, err := n.engine.ApplyAccountAdjustment(ctx, key.Account, req)
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
	return n.persistAdjustedBalance(ctx, key, req, *result.Accepted)
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

// recordSubmittedOrder creates the order record and its submitted event, stamped
// with the caller and routing key. The store assigns the order's external id and
// timestamp on insert. Callers hold n.mutate.
func (n *localNode) recordSubmittedOrder(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, error) {
	o.Account = key.Account
	o.Source = caller.Source
	o.Principal = caller.Principal
	o.Status = domain.OrderStatusSubmitted

	order, err := n.realm.CreateOrder(ctx, o)
	if err != nil {
		return domain.Order{}, fmt.Errorf("create order: %w", err)
	}
	if err := n.appendOrderEvent(ctx, order.ExternalID, domain.OrderEventSubmitted, caller, domain.OrderEventPayload{}); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

// ApplyExecutionReport settles a fill through the engine, then persists the fill
// event, trade, per-asset balances, engine-block UPDATEs, and the reflected
// status (from in.Final) in one atomic RecordOrderSettlement transaction so a
// crash never leaves a half-settled fill. The engine already applied the block,
// so the store UPDATE only follows; the observational block-audit row is written
// post-commit.
func (n *localNode) ApplyExecutionReport(
	ctx context.Context, key Key, in domain.ExecutionReportInput, caller domain.Caller,
) (engine.ExecutionReportResult, error) {
	if err := n.beginMutation(); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	defer n.endMutation()

	in.Account = key.Account
	result, err := n.engine.ApplyExecutionReport(ctx, in)
	if err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("apply execution report: %w", err)
	}

	status := domain.OrderStatusPartiallyFilled
	if in.Final {
		status = domain.OrderStatusFilled
	}
	fillPayload := accountBlockPayload(result.Blocks)
	fillPayload.FillQuantity = in.FillQuantity
	fillPayload.FillPrice = in.FillPrice
	fillPayload.FillLockPrice = in.LockPrice

	// One atomic store transaction: the fill event, per-asset balances (balance/
	// held/incoming follow the engine's resulting absolutes; realized P&L is
	// delta-accumulated in-tx, never overwritten with the reported absolute - a
	// spot fill settles both legs, so each outcome is keyed on its own asset), the
	// trade, engine-block UPDATEs, and the fill status. Without force, AllowedFrom
	// guards terminal-status clobber. Force drops the entire AllowedFrom store
	// guard, including TOCTOU protection against a concurrent fill, because it
	// means straight to the engine with no Officer checks. The block-audit rows
	// are observational and stay a post-commit best-effort write below.
	allowedFrom := domain.OrderStatusesEligibleForFill()
	if in.Force {
		allowedFrom = nil
	}
	if err := n.realm.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     key.Account,
		Order:       in.Order,
		OrderStatus: status,
		AllowedFrom: allowedFrom,
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(in.Order, domain.OrderEventFill, caller, fillPayload),
		},
		Trade: &domain.Trade{
			Order:      in.Order,
			Account:    key.Account,
			Source:     caller.Source,
			Principal:  caller.Principal,
			BaseAsset:  in.BaseAsset,
			QuoteAsset: in.QuoteAsset,
			Side:       in.Side,
			Quantity:   in.FillQuantity,
			Price:      in.FillPrice,
			LockPrice:  in.LockPrice,
		},
		Blocks: accountBlockSettlementsFrom(in.Order, result.Blocks),
	}); err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("record execution report: %w", err)
	}

	if err := n.mirrorEngineBlocksAudit(ctx, in.Order, result.Blocks); err != nil {
		return engine.ExecutionReportResult{}, err
	}

	detail := executionReportDetail(in, len(result.Blocks))
	if in.Force {
		detail += " forced=true"
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionExecutionReport,
		Account: key.Account,
		Detail:  detail,
	}); err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("audit execution report: %w", err)
	}
	return result, nil
}

// appendOrderEvent records one order-lifecycle event stamped with the caller.
func (n *localNode) appendOrderEvent(
	ctx context.Context, order domain.ExternalID, typ domain.OrderEventType, caller domain.Caller, payload domain.OrderEventPayload,
) error {
	if _, err := n.realm.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:     order,
		Type:      typ,
		Source:    caller.Source,
		Principal: caller.Principal,
		Payload:   payload,
	}); err != nil {
		return fmt.Errorf("append order event %s: %w", typ, err)
	}
	return nil
}

// PersistOrderApproval stamps the signed approval envelope onto the order and
// records the approval_issued event. It is write-once: store.PutOrderApproval
// sets the envelope only when none is present, so a retry or a later fill never
// clobbers it. The order is already durable, so this mutation only adds the
// envelope; it never touches money or status.
func (n *localNode) PersistOrderApproval(
	ctx context.Context, key Key, order domain.ExternalID, env domain.OrderApproval,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.PutOrderApproval(ctx, order, env); err != nil {
		return fmt.Errorf("persist order approval: %w", err)
	}
	caller := auth.CallerFromContext(ctx)
	if err := n.appendOrderEvent(
		ctx, order, domain.OrderEventApprovalIssued, caller, domain.OrderEventPayload{},
	); err != nil {
		return err
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

// CheckOrder delegates the non-mutating pre-trade dry-run to the engine. The
// check mutates no state, so it takes no mutate lock and writes no audit row.
func (n *localNode) CheckOrder(
	ctx context.Context, _ Key, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	n.engineMu.RLock()
	defer n.engineMu.RUnlock()
	return n.engine.CheckOrder(ctx, probe)
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
