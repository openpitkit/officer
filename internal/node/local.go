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
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/store"
)

// localNode is the in-process Node: one engine and one store running together
// behind the control plane. In the single-binary deployment there is exactly
// one localNode and it owns every account.
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
	store    store.Store

	mutate     sync.Mutex
	restarting atomic.Bool
}

// NewLocalNode builds the single in-process Node: it loads the seed snapshot
// from the store (accounts, the full barrier set, groups, and balances), builds
// the one engine from it via build, bundles the engine and store, and appends
// one startup audit row.
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
// the seed cannot be loaded, or if the engine cannot be built.
func NewLocalNode(
	ctx context.Context, st store.Store, build engine.BuildFunc,
) (Node, engine.Engine, error) {
	if st == nil {
		return nil, nil, fmt.Errorf("nil store")
	}
	if build == nil {
		return nil, nil, fmt.Errorf("nil engine build func")
	}

	n := &localNode{store: st, build: build}

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

	if err := st.AppendAudit(ctx, store.AuditEntry{
		Actor:  "system",
		Action: domain.AuditActionHydrate,
		Detail: counts,
		Source: domain.SourceSystem,
	}); err != nil {
		eng.Stop()
		return nil, nil, fmt.Errorf("audit build: %w", err)
	}

	return n, eng, nil
}

// loadSnapshot reads the full engine seed from the store: accounts (with their
// blocked state and group membership), the complete barrier set, groups, and
// balances. It returns the assembled snapshot and a human-readable counts
// summary for the hydrate audit detail. It runs once, at NewLocalNode, to seed
// the single engine build.
func (n *localNode) loadSnapshot(ctx context.Context) (engine.Snapshot, string, error) {
	accounts, err := n.store.ListAccounts(ctx)
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load accounts for build: %w", err)
	}
	limits, err := n.store.ListLimits(ctx, "")
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load limits for build: %w", err)
	}
	groups, err := n.store.ListGroups(ctx, domain.DefaultTenant)
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load groups for build: %w", err)
	}
	balances, err := n.store.ListBalances(ctx, domain.DefaultTenant, "", "")
	if err != nil {
		return engine.Snapshot{}, "", fmt.Errorf("load balances for build: %w", err)
	}

	snap := engine.Snapshot{
		Accounts: accounts,
		Limits:   limits,
		Groups:   groups,
		Balances: balances,
	}
	counts := fmt.Sprintf(
		"hydrate %d accounts %d barriers %d groups %d balances",
		len(accounts), len(limits), len(groups), len(balances),
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

	schemaVersion, schemaErr := n.store.SchemaVersion(ctx)
	reachable := n.store.Ping(ctx) == nil
	storeHealth := store.StoreHealth{
		Path:          n.store.Path(),
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
	accounts, err := n.store.ListAccounts(ctx)
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

	archive, err := n.store.ExportBackup(ctx, scope)
	if err != nil {
		return backup.Archive{}, fmt.Errorf("export backup: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionExportBackup,
		Detail: fmt.Sprintf(
			"export backup format v%d sections %d",
			archive.Manifest.FormatVersion,
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

	before, err := n.store.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		return backup.RestoreSummary{}, n.currentMarketDataSink(),
			fmt.Errorf("capture restore rollback backup: %w", err)
	}

	summary, err := n.store.RestoreBackup(ctx, archive, opts)
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
			"restore backup format v%d mode %s",
			archive.Manifest.FormatVersion,
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

	if err := n.store.Reset(ctx); err != nil {
		return n.currentMarketDataSink(), fmt.Errorf("reset database: %w", err)
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
	next.SetReservationStore(n.store)
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
	_, restoreErr := n.store.RestoreBackup(ctx, rollback, backup.RestoreOptions{
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
// is nothing to apply or revert.
func (n *localNode) CreateAccount(
	ctx context.Context, key Key, caller domain.Caller,
) (domain.Account, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Account{}, err
	}
	defer n.endMutation()

	account := domain.Account{Tenant: key.Tenant, ID: key.Account}
	if err := n.store.CreateAccount(ctx, account); err != nil {
		return domain.Account{}, fmt.Errorf("create account: %w", err)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionCreateAccount,
		Tenant:  key.Tenant,
		Account: key.Account,
		Detail:  fmt.Sprintf("create account %s", key.Account),
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

	prev, ok, err := n.store.GetAccount(ctx, key.Tenant, key.Account)
	if err != nil {
		return fmt.Errorf("read account for block: %w", err)
	}
	if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}

	if err := n.store.SetAccountBlocked(
		ctx, key.Tenant, key.Account, blocked, reason,
	); err != nil {
		return fmt.Errorf("set account blocked: %w", err)
	}

	if applyErr := n.applyBlock(ctx, key.Account, blocked, reason); applyErr != nil {
		// Revert the store to the previously persisted blocked state.
		_ = n.store.SetAccountBlocked(
			ctx, key.Tenant, key.Account, prev.Blocked, prev.BlockReason,
		)
		return fmt.Errorf("apply account block: %w", applyErr)
	}

	action := domain.AuditActionBlock
	detail := fmt.Sprintf("block account %s", key.Account)
	if !blocked {
		action = domain.AuditActionUnblock
		detail = fmt.Sprintf("unblock account %s", key.Account)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  action,
		Tenant:  key.Tenant,
		Account: key.Account,
		Detail:  detail,
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
	ctx context.Context, tenant domain.TenantID, orderID int64,
	blocks []domain.ExecutionAccountBlock,
) error {
	for _, block := range blocks {
		if err := n.store.AppendAudit(ctx, store.AuditEntry{
			Actor:   "engine",
			Source:  domain.SourceSystem,
			Action:  domain.AuditActionBlock,
			Tenant:  tenant,
			Account: block.Account,
			Detail:  engineBlockDetail(orderID, block),
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
	return n.store.AppendAudit(ctx, entry)
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
) (domain.Account, []domain.Limit, error) {
	account, ok, err := n.store.GetAccount(ctx, key.Tenant, key.Account)
	if err != nil {
		return domain.Account{}, nil, fmt.Errorf("get account: %w", err)
	}
	if !ok {
		return domain.Account{}, nil, fmt.Errorf(
			"account %q: %w", key.Account, domain.ErrNotFound)
	}
	limits, err := n.store.ListLimits(ctx, key.Account)
	if err != nil {
		return domain.Account{}, nil, fmt.Errorf("list account limits: %w", err)
	}
	return account, limits, nil
}

// ListLimits returns the barriers that reference account, or all barriers when
// account is empty.
func (n *localNode) ListLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.Limit, error) {
	limits, err := n.store.ListLimits(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("list limits: %w", err)
	}
	return limits, nil
}

// PutLimit upserts the whole barrier in the store, reconfigures its policy from
// the persisted full barrier set, reverts the store on engine-apply failure, and
// audits the action. It returns a replacement market-data sink only when the
// policy change had to rebuild the engine.
func (n *localNode) PutLimit(
	ctx context.Context, limit domain.Limit, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginMutation(); err != nil {
		return nil, err
	}
	defer n.endMutation()

	target := limit.Target
	prev, hadPrev, err := n.readBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	if err := n.store.PutLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, target.Policy)
	if applyErr != nil {
		n.revertBarrier(ctx, target, prev, hadPrev)
		return nil, fmt.Errorf("configure policy after limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Tenant:  target.Tenant,
		Account: target.Account,
		Detail:  setLimitDetail(limit),
	}); err != nil {
		return sink, fmt.Errorf("audit set limit: %w", err)
	}
	return sink, nil
}

// DeleteLimit removes the barrier from the store, reconfigures its policy from
// the persisted full barrier set, reverts the store on engine-apply failure, and
// audits the action. It returns a replacement market-data sink only when the
// policy change had to rebuild the engine.
func (n *localNode) DeleteLimit(
	ctx context.Context, target domain.LimitTarget, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginMutation(); err != nil {
		return nil, err
	}
	defer n.endMutation()

	prev, hadPrev, err := n.readBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	if err := n.store.DeleteLimit(ctx, target); err != nil {
		return nil, fmt.Errorf("delete limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, target.Policy)
	if applyErr != nil {
		n.revertBarrier(ctx, target, prev, hadPrev)
		return nil, fmt.Errorf("configure policy after delete limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionDeleteLimit,
		Tenant:  target.Tenant,
		Account: target.Account,
		Detail:  deleteLimitDetail(target),
	}); err != nil {
		return sink, fmt.Errorf("audit delete limit: %w", err)
	}
	return sink, nil
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

// reconfigurePolicy re-reads the full barrier set for policy from the store and
// applies it to the engine via the runtime Configure surface. It returns the
// engine error verbatim so the caller can decide whether to revert or rebuild.
func (n *localNode) reconfigurePolicy(ctx context.Context, policy string) error {
	limits, err := n.store.ListPolicyLimits(ctx, policy)
	if err != nil {
		return fmt.Errorf("read policy limits: %w", err)
	}
	return n.engine.ConfigurePolicy(ctx, policy, limits)
}

// readBarrier returns the barrier currently stored at target so a failed
// engine apply can be reverted. The bool is false when no such barrier exists.
func (n *localNode) readBarrier(
	ctx context.Context, target domain.LimitTarget,
) (domain.Limit, bool, error) {
	limits, err := n.store.ListPolicyLimits(ctx, target.Policy)
	if err != nil {
		return domain.Limit{}, false, fmt.Errorf("read barrier: %w", err)
	}
	for _, limit := range limits {
		if limit.Target == target {
			return limit, true, nil
		}
	}
	return domain.Limit{}, false, nil
}

// revertBarrier restores the store barrier at target to its previous state:
// re-put when it existed before, delete when it did not.
func (n *localNode) revertBarrier(
	ctx context.Context, target domain.LimitTarget, prev domain.Limit, hadPrev bool,
) {
	if hadPrev {
		// Best-effort revert; the caller already surfaces the primary error.
		_ = n.store.PutLimit(ctx, prev)
		return
	}
	// Best-effort revert; the caller already surfaces the primary error.
	_ = n.store.DeleteLimit(ctx, target)
}

// ListAudit returns the most recent n audit rows, newest first.
func (n *localNode) ListAudit(
	ctx context.Context, count int,
) ([]domain.AuditRow, error) {
	rows, err := n.store.ListAudit(ctx, count)
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
	rows, err := n.store.ListAuditFiltered(ctx, filter, count)
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
	access, err := n.store.ListMcpAccess(ctx)
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

	if err := n.store.SetMcpAccess(ctx, command, enabled); err != nil {
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
	value, ok, err := n.store.GetUserSetting(ctx, userID, key)
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

	if err := n.store.SetUserSetting(ctx, userID, key, value); err != nil {
		return fmt.Errorf("set user setting: %w", err)
	}
	return nil
}

// --- market-data control plane ---------------------------------------------

func (n *localNode) ListMarketDataInstances(
	ctx context.Context,
) ([]domain.MarketDataInstance, error) {
	instances, err := n.store.ListMarketDataInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("list market-data instances: %w", err)
	}
	return instances, nil
}

func (n *localNode) GetMarketDataInstance(
	ctx context.Context, id string,
) (domain.MarketDataInstance, bool, error) {
	instance, ok, err := n.store.GetMarketDataInstance(ctx, id)
	if err != nil {
		return domain.MarketDataInstance{}, false, fmt.Errorf("get market-data instance: %w", err)
	}
	return instance, ok, nil
}

func (n *localNode) CreateMarketDataInstance(
	ctx context.Context, instance domain.MarketDataInstance, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.store.CreateMarketDataInstance(ctx, instance); err != nil {
		return fmt.Errorf("create market-data instance: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("create market-data instance %s", instance.ID),
	}); err != nil {
		return fmt.Errorf("audit create market-data instance: %w", err)
	}
	return nil
}

func (n *localNode) SetMarketDataInstanceEnabled(
	ctx context.Context, id string, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.store.SetMarketDataInstanceEnabled(ctx, id, enabled); err != nil {
		return fmt.Errorf("set market-data instance enabled: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: marketDataToggleDetail("instance", id, enabled),
	}); err != nil {
		return fmt.Errorf("audit set market-data instance enabled: %w", err)
	}
	return nil
}

func (n *localNode) UpdateMarketDataInstanceSettings(
	ctx context.Context, id, label, credentials string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.store.UpdateMarketDataInstanceSettings(ctx, id, label, credentials); err != nil {
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
	ctx context.Context, id string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.store.DeleteMarketDataInstance(ctx, id); err != nil {
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
	ctx context.Context, instanceID string,
) ([]domain.MarketDataInstrument, error) {
	instruments, err := n.store.ListMarketDataInstruments(ctx, instanceID)
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

	if err := n.store.UpsertMarketDataInstrument(ctx, instrument); err != nil {
		return fmt.Errorf("upsert market-data instrument: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("upsert market-data instrument %s/%s",
			instrument.InstanceID, instrument.ExternalSymbol),
	}); err != nil {
		return fmt.Errorf("audit upsert market-data instrument: %w", err)
	}
	return nil
}

func (n *localNode) SetMarketDataInstrumentEnabled(
	ctx context.Context, instanceID, externalSymbol string, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.store.SetMarketDataInstrumentEnabled(
		ctx, instanceID, externalSymbol, enabled,
	); err != nil {
		return fmt.Errorf("set market-data instrument enabled: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: marketDataToggleDetail("instrument", instanceID+"/"+externalSymbol, enabled),
	}); err != nil {
		return fmt.Errorf("audit set market-data instrument enabled: %w", err)
	}
	return nil
}

func (n *localNode) DeleteMarketDataInstrument(
	ctx context.Context, instanceID, externalSymbol string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.store.DeleteMarketDataInstrument(ctx, instanceID, externalSymbol); err != nil {
		return fmt.Errorf("delete market-data instrument: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("delete market-data instrument %s/%s", instanceID, externalSymbol),
	}); err != nil {
		return fmt.Errorf("audit delete market-data instrument: %w", err)
	}
	return nil
}

func (n *localNode) ListMarketDataQuotes(
	ctx context.Context, instanceID string,
) ([]domain.MarketDataQuote, error) {
	quotes, err := n.store.ListMarketDataQuotes(ctx, instanceID)
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
	ctx context.Context, key Key, groupID string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	prev, ok, err := n.store.GetAccount(ctx, key.Tenant, key.Account)
	if err != nil {
		return fmt.Errorf("read account for set group: %w", err)
	}
	if !ok {
		return fmt.Errorf("account %q: %w", key.Account, domain.ErrNotFound)
	}
	if prev.GroupID == groupID {
		return nil
	}

	if err := n.store.SetAccountGroup(ctx, key.Tenant, key.Account, groupID); err != nil {
		return fmt.Errorf("set account group: %w", err)
	}

	if applyErr := n.applyGroupMove(ctx, key.Account, prev.GroupID, groupID); applyErr != nil {
		// Best-effort revert; the caller already surfaces the primary error.
		_ = n.store.SetAccountGroup(ctx, key.Tenant, key.Account, prev.GroupID)
		return fmt.Errorf("apply account group: %w", applyErr)
	}

	// Ensure a group record exists for the new group so it appears in
	// ListGroups and is actionable via block/unblock/notes. Treat
	// ErrAlreadyExists as success (idempotent).
	if groupID != "" {
		createErr := n.store.CreateGroup(ctx, domain.AccountGroup{
			Tenant: key.Tenant,
			ID:     groupID,
		})
		if createErr != nil && !errors.Is(createErr, domain.ErrAlreadyExists) {
			return fmt.Errorf("ensure group record on set: %w", createErr)
		}
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetGroup,
		Tenant:  key.Tenant,
		Account: key.Account,
		Detail:  setAccountGroupDetail(key.Account, groupID),
	}); err != nil {
		return fmt.Errorf("audit set account group: %w", err)
	}
	return nil
}

// applyGroupMove moves one account between groups on the engine: it
// unregisters from oldGroup when set and registers into newGroup when set. An
// empty group id means "no group", so clearing only unregisters and setting
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

	if err := n.store.SetAccountNotes(ctx, key.Tenant, key.Account, notes); err != nil {
		return fmt.Errorf("set account notes: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetNotes,
		Tenant:  key.Tenant,
		Account: key.Account,
		Detail:  fmt.Sprintf("set notes account %s", key.Account),
	}); err != nil {
		return fmt.Errorf("audit set account notes: %w", err)
	}
	return nil
}

// --- groups -----------------------------------------------------------------

// CreateGroup persists a new account group and audits the action. A group is
// store-only: membership lives on accounts, so there is no engine side-effect
// at creation.
func (n *localNode) CreateGroup(
	ctx context.Context, group domain.AccountGroup, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.store.CreateGroup(ctx, group); err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateGroup,
		Tenant: group.Tenant,
		Detail: fmt.Sprintf("create group %s", group.ID),
	}); err != nil {
		return fmt.Errorf("audit create group: %w", err)
	}
	return nil
}

// ListGroups returns every persisted group for the tenant.
func (n *localNode) ListGroups(
	ctx context.Context, tenant domain.TenantID,
) ([]domain.AccountGroup, error) {
	groups, err := n.store.ListGroups(ctx, tenant)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return groups, nil
}

// GetGroup returns the group and its member accounts. The bool is false when no
// such group exists.
func (n *localNode) GetGroup(
	ctx context.Context, tenant domain.TenantID, id string,
) (domain.AccountGroup, []domain.Account, bool, error) {
	group, ok, err := n.store.GetGroup(ctx, tenant, id)
	if err != nil {
		return domain.AccountGroup{}, nil, false, fmt.Errorf("get group: %w", err)
	}
	if !ok {
		return domain.AccountGroup{}, nil, false, nil
	}
	members, err := n.store.ListGroupAccounts(ctx, tenant, id)
	if err != nil {
		return domain.AccountGroup{}, nil, false, fmt.Errorf("list group accounts: %w", err)
	}
	return group, members, true, nil
}

// SetGroupNotes replaces a group's notes in the store and audits the action.
// If no group record exists yet (e.g. the group is known only via account
// membership), a default record is created first so the update succeeds.
func (n *localNode) SetGroupNotes(
	ctx context.Context, tenant domain.TenantID, id, notes string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.ensureGroupRecordLocked(ctx, tenant, id); err != nil {
		return fmt.Errorf("ensure group for set notes: %w", err)
	}

	if err := n.store.SetGroupNotes(ctx, tenant, id, notes); err != nil {
		return fmt.Errorf("set group notes: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetGroupNotes,
		Tenant: tenant,
		Detail: fmt.Sprintf("set notes group %s", id),
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
	ctx context.Context, tenant domain.TenantID, id string, blocked bool, reason string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.ensureGroupRecordLocked(ctx, tenant, id); err != nil {
		return fmt.Errorf("ensure group for block: %w", err)
	}

	prev, ok, err := n.store.GetGroup(ctx, tenant, id)
	if err != nil {
		return fmt.Errorf("read group for block: %w", err)
	}
	if !ok {
		return fmt.Errorf("group %q: %w", id, domain.ErrNotFound)
	}

	if err := n.store.SetGroupBlocked(ctx, tenant, id, blocked, reason); err != nil {
		return fmt.Errorf("set group blocked: %w", err)
	}

	if applyErr := n.applyGroupBlock(ctx, id, blocked, reason); applyErr != nil {
		// Best-effort revert; the caller already surfaces the primary error.
		_ = n.store.SetGroupBlocked(ctx, tenant, id, prev.Blocked, prev.BlockReason)
		return fmt.Errorf("apply group block: %w", applyErr)
	}

	action := domain.AuditActionBlockGroup
	detail := fmt.Sprintf("block group %s", id)
	if !blocked {
		action = domain.AuditActionUnblockGroup
		detail = fmt.Sprintf("unblock group %s", id)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: action,
		Tenant: tenant,
		Detail: detail,
	}); err != nil {
		return fmt.Errorf("audit group block: %w", err)
	}
	return nil
}

// ensureGroupRecordLocked creates an account_groups record for (tenant, id) if
// one does not already exist. ErrAlreadyExists is treated as success so the
// call is idempotent. Callers must hold mutate.
func (n *localNode) ensureGroupRecordLocked(
	ctx context.Context, tenant domain.TenantID, id string,
) error {
	err := n.store.CreateGroup(ctx, domain.AccountGroup{Tenant: tenant, ID: id})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return err
	}
	return nil
}

// applyGroupBlock applies the desired blocked state for a group to the engine.
func (n *localNode) applyGroupBlock(
	ctx context.Context, id string, blocked bool, reason string,
) error {
	if blocked {
		return n.engine.BlockGroup(ctx, id, reason)
	}
	return n.engine.UnblockGroup(ctx, id)
}

// DeleteGroup removes the group from the store and audits the action. A group
// is store-only (membership lives on accounts), so there is no engine
// side-effect.
func (n *localNode) DeleteGroup(
	ctx context.Context, tenant domain.TenantID, id string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.store.DeleteGroup(ctx, tenant, id); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionDeleteGroup,
		Tenant: tenant,
		Detail: fmt.Sprintf("delete group %s", id),
	}); err != nil {
		return fmt.Errorf("audit delete group: %w", err)
	}
	return nil
}

// --- spot funds -------------------------------------------------------------

// ApplyAdjustment applies one spot-funds adjustment through the engine, which
// is the authority for the resulting holdings. On accept it writes the
// recomputed balance snapshot and the accepted record; on reject it records
// the rejected adjustment and leaves balances unchanged. Either way it audits
// the action.
func (n *localNode) ApplyAdjustment(
	ctx context.Context, key Key, req domain.AdjustmentRequest, caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	if err := n.beginMutation(); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	defer n.endMutation()

	result, err := n.engine.ApplyAccountAdjustment(ctx, key.Account, req)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("apply adjustment: %w", err)
	}

	rec := domain.AccountAdjustmentRecord{
		Tenant:    key.Tenant,
		Account:   key.Account,
		Source:    caller.Source,
		Principal: caller.Principal,
		Request:   req,
		Accepted:  result.Accepted,
		Rejected:  result.Rejected,
	}

	if result.Accepted != nil {
		if err := n.persistAdjustedBalance(ctx, key, req, *result.Accepted); err != nil {
			return domain.AccountAdjustmentRecord{}, err
		}
	}

	stored, err := n.store.AppendAdjustment(ctx, rec)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("append adjustment: %w", err)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionAdjustment,
		Tenant:  key.Tenant,
		Account: key.Account,
		Detail:  adjustmentDetail(key.Account, req.Asset, result.Accepted != nil),
	}); err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("audit adjustment: %w", err)
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
	prev, _, err := n.store.GetBalance(ctx, key.Tenant, key.Account, req.Asset)
	if err != nil {
		return fmt.Errorf("read balance for adjustment: %w", err)
	}
	realizedPnl, err := domain.AddDecimals(prev.RealizedPnl, outcome.RealizedPnlDelta)
	if err != nil {
		return fmt.Errorf("accumulate realized pnl: %w", err)
	}
	balance := domain.Balance{
		Tenant:            key.Tenant,
		Account:           key.Account,
		Asset:             req.Asset,
		Available:         pick(outcome.BalanceResult, prev.Available),
		Held:              pick(outcome.HeldResult, prev.Held),
		Incoming:          pick(outcome.IncomingResult, prev.Incoming),
		RealizedPnl:       realizedPnl,
		AverageEntryPrice: pick(req.AverageEntryPrice, prev.AverageEntryPrice),
	}
	if err := n.store.UpsertBalance(ctx, balance); err != nil {
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
	orderID int64, blocks []domain.ExecutionAccountBlock,
) []domain.ExecutionAccountBlock {
	if len(blocks) == 0 {
		return nil
	}
	settlements := make([]domain.ExecutionAccountBlock, 0, len(blocks))
	for _, block := range blocks {
		block.Reason = engineBlockReason(orderID, block)
		settlements = append(settlements, block)
	}
	return settlements
}

// fillSettlementEvent builds one lifecycle event stamped with the caller for a
// fill/settlement tx. The store assigns the id and timestamp on append.
func fillSettlementEvent(
	orderID int64, typ domain.OrderEventType, caller domain.Caller, payload domain.OrderEventPayload,
) domain.OrderEvent {
	return domain.OrderEvent{
		OrderID:   orderID,
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

// ListBalances returns the balance rows for the tenant filtered by the
// non-empty account and asset.
func (n *localNode) ListBalances(
	ctx context.Context, tenant domain.TenantID, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	balances, err := n.store.ListBalances(ctx, tenant, account, asset)
	if err != nil {
		return nil, fmt.Errorf("list balances: %w", err)
	}
	return balances, nil
}

// GetBalance returns the balance for (tenant, account, asset).
func (n *localNode) GetBalance(
	ctx context.Context, tenant domain.TenantID, account domain.AccountID, asset string,
) (domain.Balance, bool, error) {
	balance, ok, err := n.store.GetBalance(ctx, tenant, account, asset)
	if err != nil {
		return domain.Balance{}, false, fmt.Errorf("get balance: %w", err)
	}
	return balance, ok, nil
}

// ListAdjustments returns the most recent n adjustments for an account.
func (n *localNode) ListAdjustments(
	ctx context.Context, tenant domain.TenantID, account domain.AccountID, source domain.Source, count int,
) ([]domain.AccountAdjustmentRecord, error) {
	records, err := n.store.ListAdjustments(ctx, tenant, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list adjustments: %w", err)
	}
	return records, nil
}

// --- trading ----------------------------------------------------------------

// SubmitOrder records the order as submitted, runs the engine pre-trade, and
// persists the lifecycle. On accept it records pre_trade_accepted and
// reservation_committed events, persists the captured lock prices, and sets the
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

	o.Tenant = key.Tenant
	o.Account = key.Account
	o.Source = caller.Source
	o.Principal = caller.Principal
	o.Status = domain.OrderStatusSubmitted

	order, err := n.store.CreateOrder(ctx, o)
	if err != nil {
		return domain.Order{}, fmt.Errorf("create order: %w", err)
	}
	if err := n.appendOrderEvent(ctx, order.ID, domain.OrderEventSubmitted, caller, domain.OrderEventPayload{}); err != nil {
		return domain.Order{}, err
	}

	result, err := n.engine.SubmitOrder(ctx, order)
	if err != nil {
		return domain.Order{}, fmt.Errorf("submit order: %w", err)
	}

	if result.Accepted {
		order, err = n.recordOrderAccepted(ctx, key, order, result, caller)
	} else {
		order, err = n.recordOrderRejected(ctx, key, order, result, caller)
	}
	if err != nil {
		return domain.Order{}, err
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSubmitOrder,
		Tenant:  key.Tenant,
		Account: key.Account,
		Detail:  submitOrderDetail(order, result.Accepted),
	}); err != nil {
		return domain.Order{}, fmt.Errorf("audit submit order: %w", err)
	}
	return order, nil
}

// recordOrderAccepted records the accept path: pre_trade_accepted and
// reservation_committed events, the captured lock prices on the order, and the
// committed status.
func (n *localNode) recordOrderAccepted(
	ctx context.Context, key Key, order domain.Order, result engine.OrderResult, caller domain.Caller,
) (domain.Order, error) {
	if err := n.store.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Tenant:      key.Tenant,
		Account:     key.Account,
		OrderID:     order.ID,
		OrderStatus: domain.OrderStatusCommitted,
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ID, domain.OrderEventReservationCommitted, caller, domain.OrderEventPayload{}),
		},
		LockPrices:    result.LockPrices,
		SetLockPrices: true,
	}); err != nil {
		return domain.Order{}, fmt.Errorf("record order accepted: %w", err)
	}
	order.LockPrices = result.LockPrices
	order.Status = domain.OrderStatusCommitted
	return order, nil
}

// recordOrderRejected records the reject path: one pre_trade_rejected event
// carrying the first reject and the rejected status.
func (n *localNode) recordOrderRejected(
	ctx context.Context, key Key, order domain.Order, result engine.OrderResult, caller domain.Caller,
) (domain.Order, error) {
	payload := domain.OrderEventPayload{}
	if len(result.Rejects) > 0 {
		r := result.Rejects[0]
		payload.RejectCode = r.Code
		payload.RejectScope = r.Scope
		payload.RejectPolicy = r.Policy
		payload.RejectReason = r.Reason
		payload.RejectDetails = r.Details
	}
	if err := n.appendOrderEvent(ctx, order.ID, domain.OrderEventPreTradeRejected, caller, payload); err != nil {
		return domain.Order{}, err
	}
	if err := n.store.UpdateOrderStatus(ctx, key.Tenant, order.ID, domain.OrderStatusRejected); err != nil {
		return domain.Order{}, fmt.Errorf("order status rejected: %w", err)
	}
	order.Status = domain.OrderStatusRejected
	return order, nil
}

// SubmitHold records the order, runs the engine pre-trade keeping the
// reservation held, and persists the accept/reject lifecycle. On accept the
// held amount stays reserved on engine storage; the order is left accepted and
// the result carries the approval id, lock prices, and settlement estimate. On
// reject the order is recorded rejected. The backend audits the issued approval.
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
		order, err = n.recordOrderRejected(ctx, key, order, engine.OrderResult{Rejects: result.Rejects}, caller)
		if err != nil {
			return domain.Order{}, engine.HoldResult{}, err
		}
		return order, result, nil
	}

	// One atomic store transaction: pre_trade_accepted event, per-asset held
	// balances (realized-pnl accumulated inside the tx), lock prices, and the
	// accepted status. No trade, no blocks on the hold-accept path. AllowedFrom is
	// left empty: the order is fresh (submitted) so the status advance is
	// unguarded.
	if err := n.store.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Tenant:        key.Tenant,
		Account:       key.Account,
		OrderID:       order.ID,
		OrderStatus:   domain.OrderStatusAccepted,
		Balances:      balanceSettlementsFrom(result.Outcomes),
		Events:        []domain.OrderEvent{fillSettlementEvent(order.ID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{})},
		LockPrices:    result.LockPrices,
		SetLockPrices: true,
	}); err != nil {
		return domain.Order{}, engine.HoldResult{}, fmt.Errorf("record hold accept: %w", err)
	}
	order.LockPrices = result.LockPrices
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
		order, err = n.recordOrderRejected(ctx, key, order, engine.OrderResult{Rejects: result.Rejects}, caller)
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
	// engine-block UPDATEs, lock prices, and the filled status. AllowedFrom guards
	// against terminal-status clobber (e.g. a duplicate fill for an already-filled
	// order). The block-audit rows are observational and stay a post-commit
	// best-effort write below.
	if err := n.store.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Tenant:      key.Tenant,
		Account:     key.Account,
		OrderID:     order.ID,
		OrderStatus: domain.OrderStatusFilled,
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ID, domain.OrderEventReservationCommitted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ID, domain.OrderEventFill, caller, fillPayload),
		},
		Trade: &domain.Trade{
			OrderID:    order.ID,
			Tenant:     key.Tenant,
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
		Blocks:        accountBlockSettlementsFrom(order.ID, result.Blocks),
		LockPrices:    result.LockPrices,
		SetLockPrices: true,
	}); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, fmt.Errorf("record immediate fill: %w", err)
	}
	if err := n.mirrorEngineBlocksAudit(ctx, key.Tenant, order.ID, result.Blocks); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	order.LockPrices = result.LockPrices
	order.Status = domain.OrderStatusFilled
	return order, result, nil
}

// ConfirmHeld commits the held reservation through the engine and records the
// committed lifecycle on the order. The backend audits the confirmation.
func (n *localNode) ConfirmHeld(
	ctx context.Context, tenant domain.TenantID, orderID int64, approvalID string, caller domain.Caller,
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
	// reservation_committed event. The AllowedFrom={accepted} guard is TOCTOU-safe
	// - a fill that flipped the order to filled before this tx yields ErrConflict
	// (surfaced as the node's conflict error) and writes nothing, preserving the
	// filled status. The native commit already happened; a store failure here does
	// not re-resolve the handle (the engine's finishResolve already ran).
	if err := n.store.ResolveOrderReservation(ctx, domain.ReservationResolution{
		Tenant:      tenant,
		ApprovalID:  approvalID,
		IntentState: domain.ReservationIntentStateCommitted,
		OrderStatus: domain.OrderStatusCommitted,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusAccepted},
		Events: []domain.OrderEvent{{
			OrderID:   orderID,
			Type:      domain.OrderEventReservationCommitted,
			Source:    caller.Source,
			Principal: caller.Principal,
		}},
		OrderID: orderID,
	}); err != nil {
		return domain.Order{}, fmt.Errorf("resolve confirm: %w", err)
	}
	detail, err := n.store.GetOrder(ctx, tenant, orderID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	return detail.Order, nil
}

// CancelHeld rolls back the held reservation through the engine and records the
// cancelled lifecycle on the order. The backend audits the cancellation.
func (n *localNode) CancelHeld(
	ctx context.Context, tenant domain.TenantID, orderID int64, approvalID string, caller domain.Caller,
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
		if err := n.rollbackHeldIntentFallback(ctx, tenant, approvalID); err != nil {
			return domain.Order{}, err
		}
	}
	// One atomic store transaction: intent flip + cancelled status +
	// reservation_rolled_back and cancelled events. AllowedFrom={accepted} is
	// TOCTOU-safe: a late fill that flipped the order to filled before this tx
	// yields ErrConflict (surfaced as the node's conflict error) and writes
	// nothing, so a filled order is never clobbered into cancelled.
	if err := n.store.ResolveOrderReservation(ctx, domain.ReservationResolution{
		Tenant:      tenant,
		ApprovalID:  approvalID,
		IntentState: domain.ReservationIntentStateRolledBack,
		OrderStatus: domain.OrderStatusCancelled,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusAccepted},
		Events: []domain.OrderEvent{
			{
				OrderID:   orderID,
				Type:      domain.OrderEventReservationRolledBack,
				Source:    caller.Source,
				Principal: caller.Principal,
			},
			{
				OrderID:   orderID,
				Type:      domain.OrderEventCancelled,
				Source:    caller.Source,
				Principal: caller.Principal,
			},
		},
		OrderID: orderID,
	}); err != nil {
		return domain.Order{}, fmt.Errorf("resolve cancel: %w", err)
	}
	detail, err := n.store.GetOrder(ctx, tenant, orderID)
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
		swept, found, lookupErr := n.store.GetReservationIntent(ctx, approvalID)
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
	ctx context.Context, tenant domain.TenantID, approvalID string,
) error {
	intent, err := n.openReservationIntent(ctx, approvalID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("rollback held after engine restart: %w", err)
		}
		// Not in the open set - check whether it exists in a terminal state.
		swept, found, lookupErr := n.store.GetReservationIntent(ctx, approvalID)
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
	key := Key{Tenant: tenant, Account: intent.Account}
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
	intents, err := n.store.ListOpenReservationIntents(ctx)
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
		if payload.Order.ID != 0 || payload.Order.Account != "" || payload.Outcomes != nil {
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
// with the caller and routing key. Callers hold n.mutate.
func (n *localNode) recordSubmittedOrder(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, error) {
	o.Tenant = key.Tenant
	o.Account = key.Account
	o.Source = caller.Source
	o.Principal = caller.Principal
	o.Status = domain.OrderStatusSubmitted

	order, err := n.store.CreateOrder(ctx, o)
	if err != nil {
		return domain.Order{}, fmt.Errorf("create order: %w", err)
	}
	if err := n.appendOrderEvent(ctx, order.ID, domain.OrderEventSubmitted, caller, domain.OrderEventPayload{}); err != nil {
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
	// trade, engine-block UPDATEs, and the fill status. AllowedFrom guards against
	// terminal-status clobber (a duplicate or late execution report for an order
	// already in a terminal state returns ErrConflict and writes nothing). The
	// block-audit rows are observational and stay a post-commit best-effort write below.
	if err := n.store.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Tenant:      key.Tenant,
		Account:     key.Account,
		OrderID:     in.OrderID,
		OrderStatus: status,
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(in.OrderID, domain.OrderEventFill, caller, fillPayload),
		},
		Trade: &domain.Trade{
			OrderID:    in.OrderID,
			Tenant:     key.Tenant,
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
		Blocks: accountBlockSettlementsFrom(in.OrderID, result.Blocks),
	}); err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("record execution report: %w", err)
	}

	if err := n.mirrorEngineBlocksAudit(ctx, key.Tenant, in.OrderID, result.Blocks); err != nil {
		return engine.ExecutionReportResult{}, err
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionExecutionReport,
		Tenant:  key.Tenant,
		Account: key.Account,
		Detail:  executionReportDetail(in, len(result.Blocks)),
	}); err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("audit execution report: %w", err)
	}
	return result, nil
}

// appendOrderEvent records one order-lifecycle event stamped with the caller.
func (n *localNode) appendOrderEvent(
	ctx context.Context, orderID int64, typ domain.OrderEventType, caller domain.Caller, payload domain.OrderEventPayload,
) error {
	if _, err := n.store.AppendOrderEvent(ctx, domain.OrderEvent{
		OrderID:   orderID,
		Type:      typ,
		Source:    caller.Source,
		Principal: caller.Principal,
		Payload:   payload,
	}); err != nil {
		return fmt.Errorf("append order event %s: %w", typ, err)
	}
	return nil
}

// GetOrder returns the order with its events and trades.
func (n *localNode) GetOrder(
	ctx context.Context, tenant domain.TenantID, id int64,
) (domain.OrderDetail, error) {
	detail, err := n.store.GetOrder(ctx, tenant, id)
	if err != nil {
		return domain.OrderDetail{}, fmt.Errorf("get order: %w", err)
	}
	return detail, nil
}

// ListOrders returns the most recent n orders for an account.
func (n *localNode) ListOrders(
	ctx context.Context, tenant domain.TenantID, account domain.AccountID, source domain.Source, count int,
) ([]domain.Order, error) {
	orders, err := n.store.ListOrders(ctx, tenant, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	return orders, nil
}

// CountOrders returns the total number of orders recorded for the tenant.
func (n *localNode) CountOrders(
	ctx context.Context, tenant domain.TenantID,
) (int, error) {
	count, err := n.store.CountOrders(ctx, tenant)
	if err != nil {
		return 0, fmt.Errorf("count orders: %w", err)
	}
	return count, nil
}

// CountOrdersSince returns the number of orders for the tenant at or after
// since.
func (n *localNode) CountOrdersSince(
	ctx context.Context, tenant domain.TenantID, since time.Time,
) (int, error) {
	count, err := n.store.CountOrdersSince(ctx, tenant, since)
	if err != nil {
		return 0, fmt.Errorf("count orders since: %w", err)
	}
	return count, nil
}

// ListOrderEvents returns all events for the identified order, oldest first.
func (n *localNode) ListOrderEvents(
	ctx context.Context, tenant domain.TenantID, orderID int64,
) ([]domain.OrderEvent, error) {
	events, err := n.store.ListOrderEvents(ctx, tenant, orderID)
	if err != nil {
		return nil, fmt.Errorf("list order events: %w", err)
	}
	return events, nil
}

// ListTrades returns the most recent n trades for an account.
func (n *localNode) ListTrades(
	ctx context.Context, tenant domain.TenantID, account domain.AccountID, source domain.Source, count int,
) ([]domain.Trade, error) {
	trades, err := n.store.ListTrades(ctx, tenant, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list trades: %w", err)
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
	if err := n.store.Close(); err != nil {
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
		return nil, fmt.Errorf("%w: tenant=%q account=%q",
			ErrNoOwner, key.Tenant, key.Account)
	}
	return r.node, nil
}

// All returns a fresh one-element snapshot of the node set.
func (r *localRouter) All() []Node {
	return []Node{r.node}
}
