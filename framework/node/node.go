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

// Package node defines the seam between the Pit Officer control plane and an
// execution target. An execution target bundles one engine and its backing
// store, bound to a single realm. In the single-binary deployment there is
// exactly one node, a LocalNode running the engine and the realm-scoped store
// in-process. In a future distributed deployment a routing node fans requests
// out to shards; the control plane reaches all of them only through a
// NodeRouter, so backend code never assumes a single node.
//
// Identity at this seam follows the store: accounts and groups are addressed by
// their public code, machine records (orders, order events) by their opaque
// external id. The realm is bound once when the node is built and never appears
// as a method parameter; the single-binary node binds domain.DefaultRealm.
package node

import (
	"context"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

// Key is the routing key that identifies which node owns a given account. With
// the realm bound on the node, the account code alone resolves an account to
// exactly one node, so the key carries only the account code.
type Key struct {
	// Account is the public code of the account being routed.
	Account domain.AccountID
}

// Health reports the observable condition of a node: the status of its engine
// and of its store, aggregated for the dashboard and health checks.
type Health struct {
	// Engine is the health of the node's engine.
	Engine engine.Health
	// Store is the health of the node's store.
	Store store.StoreHealth
}

// AccountLimits bundles the three typed barrier sets returned by the account-
// addressed limit reads. Each slice holds the barriers of one policy; an empty
// slice means the policy carries no matching barrier. It is the read-side
// counterpart of the typed Put* mutators on the Node surface.
type AccountLimits struct {
	// RateLimits are the matching rate-limit barriers.
	RateLimits []domain.LimitRate
	// OrderSizeLimits are the matching order-size barriers.
	OrderSizeLimits []domain.LimitOrderSize
	// PnlBoundsLimits are the matching P&L-bounds barriers.
	PnlBoundsLimits []domain.LimitPnlBounds
}

// LimitTarget addresses a single typed barrier for deletion: the policy it
// belongs to plus the (scope, account, asset) composite the store keys it by.
// The optional Account/Asset axes are present exactly when Scope carries them.
type LimitTarget struct {
	// Policy is the policy the barrier belongs to (e.g. domain.PolicyRateLimit).
	Policy string
	// Scope is the axis combination the barrier applies to.
	Scope domain.LimitScope
	// Account is the account axis; empty unless Scope carries it.
	Account domain.AccountID
	// Asset is the asset axis; empty unless Scope carries it.
	Asset string
}

// Node is one execution target: an engine plus its realm-scoped store, behind a
// stable address the control plane can route to.
//
// The command methods below are the operations a future shard serves over RPC.
// In the single-binary deployment they all run in-process against the local
// engine and the bound realm store. Every mutation follows one protocol (see
// localNode): the store is the source of truth and is written first, the engine
// is applied from persisted state, the store is reverted on engine failure, and
// an audit row is appended last. Runtime administrative changes rebuild the
// engine and reject new mutating requests while the rebuild is in progress.
type Node interface {
	// Health returns the current aggregate health of the node's engine and
	// store.
	Health(ctx context.Context) (Health, error)

	// EngineVersion returns the version string of the node's current engine. The
	// version is the source for the MCP server version stamp.
	EngineVersion() string

	// Owns reports whether this node is responsible for the given routing key.
	Owns(key Key) bool

	// ListAccounts returns every persisted account owned by this node.
	ListAccounts(ctx context.Context) ([]domain.Account, error)

	// ListAccountRows returns persisted accounts matching filter, with
	// list-only aggregate counts and total count before paging.
	ListAccountRows(
		ctx context.Context, filter store.AccountListFilter,
	) (store.AccountListPage, error)

	// ExportBackup returns a portable archive for the node's persisted state and
	// audits the action.
	ExportBackup(
		ctx context.Context,
		scope backup.Scope,
		caller domain.Caller,
	) (backup.Archive, error)

	// RestoreBackup imports a portable archive and rehydrates the live engine
	// from the restored state. The returned sink belongs to the new engine when
	// market-data runtime needs to reconnect after restore.
	RestoreBackup(
		ctx context.Context,
		archive backup.Archive,
		opts backup.RestoreOptions,
		caller domain.Caller,
	) (backup.RestoreSummary, marketdata.Sink, error)

	// ResetDatabase recreates the store from scratch, rebuilds the live engine,
	// and audits the reset in the new database.
	ResetDatabase(ctx context.Context, caller domain.Caller) (marketdata.Sink, error)

	// CurrentMarketDataSink returns the quote sink of the node's current engine.
	// The engine is rebuilt (and its market-data service replaced) by account,
	// group, restore, and reset mutations, so the market-data runtime must
	// re-adopt this sink on restart rather than caching one across a rebuild.
	CurrentMarketDataSink() marketdata.Sink

	// ListAssets returns every persisted asset.
	ListAssets(ctx context.Context) ([]domain.Asset, error)

	// ListAssetRows returns persisted assets matching filter, with total count
	// before paging.
	ListAssetRows(
		ctx context.Context, filter store.AssetListFilter,
	) (store.AssetListPage, error)

	// CreateAsset persists a new asset and audits the action.
	CreateAsset(ctx context.Context, asset domain.Asset, caller domain.Caller) (domain.Asset, error)

	// UpdateAsset replaces the asset's public code and mutable fields and audits
	// the action. A code rename is safe because dependent rows reference the asset
	// by its surrogate id.
	UpdateAsset(
		ctx context.Context, oldCode string, asset domain.Asset, caller domain.Caller,
	) (domain.Asset, error)

	// DeleteAsset removes the asset, cascading dependents when force is set, and
	// audits the action.
	DeleteAsset(ctx context.Context, code string, force bool, caller domain.Caller) error

	// ListAssetClasses returns every persisted asset class.
	ListAssetClasses(ctx context.Context) ([]domain.AssetClass, error)

	// ListAssetClassRows returns persisted asset classes matching filter, with the
	// aggregate asset count and total count before paging.
	ListAssetClassRows(
		ctx context.Context, filter store.AssetClassListFilter,
	) (store.AssetClassListPage, error)

	// CreateAssetClass persists a new asset class and audits the action.
	CreateAssetClass(
		ctx context.Context, class domain.AssetClass, caller domain.Caller,
	) (domain.AssetClass, error)

	// UpdateAssetClass replaces the class's public code, title and notes,
	// cascading the asset link on a code rename, and audits the action.
	UpdateAssetClass(
		ctx context.Context, oldCode string, class domain.AssetClass, caller domain.Caller,
	) (domain.AssetClass, error)

	// DeleteAssetClass removes the class, clearing the asset link when force is
	// set, and audits the action.
	DeleteAssetClass(ctx context.Context, code string, force bool, caller domain.Caller) error

	// CreateAccount persists a new account and audits the action. The account
	// carries its public code and optional title; caller carries the attribution
	// stamped on the audit.
	// It returns the stored account with its engine account id populated.
	CreateAccount(ctx context.Context, account domain.Account, caller domain.Caller) (domain.Account, error)

	// SetAccountBlocked blocks or unblocks the account in the store and engine
	// and audits the action.
	SetAccountBlocked(
		ctx context.Context, key Key, blocked bool, reason string, caller domain.Caller,
	) error

	// SetAccountGroup sets or clears the account's group membership in the store
	// and engine (unregistering from the old group and registering into the new),
	// then audits the action. An empty groupCode clears membership.
	SetAccountGroup(ctx context.Context, key Key, groupCode string, caller domain.Caller) error

	// SetAccountCurrency sets or clears the account-level realized P&L currency.
	SetAccountCurrency(ctx context.Context, key Key, currency string, caller domain.Caller) error

	// SetAccountNotes replaces the account's free-form notes in the store and
	// audits the action. Notes never reach the engine.
	SetAccountNotes(ctx context.Context, key Key, notes string, caller domain.Caller) error

	// UpdateAccount replaces the account's public code and display title in the
	// store, rebuilds the engine resolver, and audits the action.
	UpdateAccount(
		ctx context.Context,
		key Key,
		account domain.Account,
		caller domain.Caller,
	) (domain.Account, error)

	// DeleteAccount removes the account and audits the action. Destructive
	// cascades require force.
	DeleteAccount(ctx context.Context, key Key, force bool, caller domain.Caller) error

	// GetAccountState returns the account row and the barriers whose scope has
	// the account axis and matches the account.
	GetAccountState(ctx context.Context, key Key) (domain.Account, AccountLimits, error)

	// ListLimits returns the barriers that reference the account, or all
	// barriers when account is empty.
	ListLimits(ctx context.Context, account domain.AccountID) (AccountLimits, error)

	// ListPolicyRows returns the node's typed barriers flattened into one sorted,
	// paged policy list, with the total matching count before paging.
	ListPolicyRows(
		ctx context.Context, filter store.PolicyListFilter,
	) (store.PolicyListPage, error)

	// PutRateLimit upserts the whole rate-limit barrier in the store,
	// reconfigures the live rate-limit policy from the persisted full barrier
	// set, audits the action, and returns a replacement market-data sink only
	// when the engine was rebuilt.
	PutRateLimit(
		ctx context.Context, limit domain.LimitRate, caller domain.Caller,
	) (marketdata.Sink, error)

	// PutOrderSizeLimit upserts the whole order-size barrier and reconfigures the
	// live order-size policy, as PutRateLimit does for the rate policy.
	PutOrderSizeLimit(
		ctx context.Context, limit domain.LimitOrderSize, caller domain.Caller,
	) (marketdata.Sink, error)

	// PutPnlBoundsLimit upserts the whole P&L-bounds barrier and reconfigures the
	// live P&L-bounds policy, as PutRateLimit does for the rate policy.
	PutPnlBoundsLimit(
		ctx context.Context, limit domain.LimitPnlBounds, caller domain.Caller,
	) (marketdata.Sink, error)

	// DeleteLimit removes the barrier addressed by (policy, scope, account,
	// asset) from the store, reconfigures the named policy from the persisted
	// full barrier set, audits the action, and returns a replacement market-data
	// sink only when the engine was rebuilt.
	DeleteLimit(
		ctx context.Context, target LimitTarget, caller domain.Caller,
	) (marketdata.Sink, error)

	// CreateGroup persists a new account group (store-only; membership lives on
	// accounts) and audits the action. It returns the stored group with its
	// engine group id populated.
	CreateGroup(
		ctx context.Context, group domain.AccountGroup, caller domain.Caller,
	) (domain.AccountGroup, error)

	// ListGroups returns every persisted group.
	ListGroups(ctx context.Context) ([]domain.AccountGroup, error)

	// ListGroupRows returns persisted groups matching filter, with list-only
	// aggregate counts and total count before paging.
	ListGroupRows(
		ctx context.Context, filter store.GroupListFilter,
	) (store.GroupListPage, error)

	// GetGroup returns the group and its member accounts. The bool is false when
	// no such group exists.
	GetGroup(
		ctx context.Context, code string,
	) (domain.AccountGroup, []domain.Account, bool, error)

	// SetGroupNotes replaces a group's notes in the store and audits the action.
	SetGroupNotes(ctx context.Context, code, notes string, caller domain.Caller) error

	// SetGroupCurrency sets or clears a group-level realized P&L currency.
	SetGroupCurrency(ctx context.Context, code, currency string, caller domain.Caller) error

	// SetDefaultGroupCurrency sets or clears the reserved default group currency.
	SetDefaultGroupCurrency(ctx context.Context, currency string, caller domain.Caller) error

	// UpdateGroup replaces the group's public code and display title in the
	// store, rebuilds the engine resolver, and audits the action.
	UpdateGroup(
		ctx context.Context,
		oldCode string,
		group domain.AccountGroup,
		caller domain.Caller,
	) (domain.AccountGroup, error)

	// SetGroupBlocked blocks or unblocks the group in the store and engine and
	// audits the action.
	SetGroupBlocked(
		ctx context.Context, code string, blocked bool, reason string, caller domain.Caller,
	) error

	// DeleteGroup removes the group (store-only) and audits the action.
	DeleteGroup(ctx context.Context, code string, caller domain.Caller) error

	// ApplyBusinessCSVImport persists a prepared business CSV import and its
	// per-row audit records atomically in the store, then applies the matching
	// live-engine projection changes.
	ApplyBusinessCSVImport(
		ctx context.Context,
		in store.BusinessCSVImport,
		caller domain.Caller,
	) error

	// ApplyAdjustment applies one spot-funds adjustment through the engine,
	// persists the resulting balance snapshot and adjustment record, and audits
	// the action. On reject the balances are left unchanged and the rejected
	// record is recorded. externalID is the caller-supplied id for the adjustment
	// record: when non-zero it is carried onto the record verbatim (the store
	// rejects a duplicate with domain.ErrAlreadyExists); when zero the store mints
	// one.
	ApplyAdjustment(
		ctx context.Context, key Key, externalID domain.ExternalID,
		req domain.AdjustmentRequest, caller domain.Caller,
	) (domain.AccountAdjustmentRecord, error)

	// ImportPositionSnapshot applies the engine-relevant fields of a persisted
	// position snapshot through the spot-funds adjustment path, then stores the
	// full snapshot and audits the import operation. externalID follows
	// ApplyAdjustment semantics for the internal adjustment record.
	ImportPositionSnapshot(
		ctx context.Context, key Key, externalID domain.ExternalID,
		snapshot domain.Balance, caller domain.Caller,
	) (domain.AccountAdjustmentRecord, error)

	// ListBalances returns the balance rows filtered by the non-empty account and
	// asset.
	ListBalances(
		ctx context.Context, account domain.AccountID, asset string,
	) ([]domain.Balance, error)

	// ListBalanceRows returns balance rows matching filter, with total count
	// before paging.
	ListBalanceRows(
		ctx context.Context, filter store.BalanceListFilter,
	) (store.BalanceListPage, error)

	// GetBalance returns the balance for (account, asset). The bool is false when
	// no row exists.
	GetBalance(
		ctx context.Context, account domain.AccountID, asset string,
	) (domain.Balance, bool, error)

	// ListAdjustments returns the most recent n adjustments for an account,
	// newest first; an empty source returns all sources.
	ListAdjustments(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.AccountAdjustmentRecord, error)

	// SubmitOrder records the order, runs the engine pre-trade, persists the
	// lifecycle events and final status, and audits the action.
	SubmitOrder(ctx context.Context, key Key, o domain.Order, caller domain.Caller) (domain.Order, error)

	// SubmitHold records the order, runs the engine pre-trade keeping the
	// reservation held, persists the accept/reject lifecycle, and returns the
	// recorded order with the engine hold result. On accept the held amount stays
	// reserved on engine storage until ConfirmHeld or CancelHeld resolves it; the
	// order status is left accepted. On reject the order is recorded rejected and
	// the result carries the engine rejects. It does not audit; the backend audits
	// approval_issued.
	SubmitHold(
		ctx context.Context, key Key, o domain.Order, caller domain.Caller,
	) (domain.Order, engine.HoldResult, error)

	// SubmitImmediate records the order, runs the engine pre-trade and, on accept,
	// commits and settles the fill in the same engine call at the captured lock
	// price, persists the lifecycle (filled on accept, rejected on reject), and
	// returns the recorded order with the engine immediate result. It does not
	// audit; the backend audits approval_issued.
	SubmitImmediate(
		ctx context.Context, key Key, o domain.Order, caller domain.Caller,
	) (domain.Order, engine.ImmediateResult, error)

	// ConfirmHeld commits the held reservation identified by approvalID through the
	// engine, then atomically flips the intent, advances the order to committed,
	// and records the reservation_committed event in one store transaction (the
	// backend audits approval_confirmed). The order is addressed by its opaque
	// external id. The order status advance is guarded against the accepted state: a
	// fill that already moved the order to a terminal status (e.g. filled) yields
	// domain.ErrConflict and nothing is written, preserving the fill. A second
	// confirm on an already-resolved reservation returns domain.ErrConflict; an
	// unknown reservation returns domain.ErrNotFound. The returned bool reports
	// whether force actually bypassed a terminal-order guard, so the backend
	// audits forced=true only on a real bypass.
	ConfirmHeld(
		ctx context.Context, order domain.ExternalID,
		approvalID string, caller domain.Caller, force bool,
	) (domain.Order, bool, error)

	// CancelHeld rolls back the held reservation identified by approvalID through
	// the engine, then atomically flips the intent, advances the order to
	// cancelled, and records the reservation_rolled_back and cancelled events in
	// one store transaction (the backend audits approval_cancelled). The order is
	// addressed by its opaque external id. The order status advance is guarded
	// against the accepted state: a late fill that already moved the order to filled
	// yields domain.ErrConflict and nothing is written, so the fill is never
	// clobbered. It is tolerant of an already-resolved reservation in the engine
	// (idempotent native rollback). The returned bool reports whether force
	// actually bypassed a terminal-order guard, so the backend audits forced=true
	// only on a real bypass.
	CancelHeld(
		ctx context.Context, order domain.ExternalID,
		approvalID string, caller domain.Caller, force bool,
	) (domain.Order, bool, error)

	// ReconcileOrphans reports persisted reservation intents still held after
	// restart. Held balance effects are durable, and confirm/cancel can fall
	// back to the persisted intent when the native handle is gone. It returns the
	// number found.
	ReconcileOrphans(ctx context.Context) (int, error)

	// ApplyExecutionReport applies every report through the engine, then persists
	// the engine-returned patch in one atomic store transaction; observational
	// block-audit rows are written post-commit, and the action is audited.
	ApplyExecutionReport(
		ctx context.Context, key Key, in domain.ExecutionReportInput, caller domain.Caller,
	) (engine.ExecutionReportResult, error)

	// PersistEventAttestation stamps the signed attestation envelope onto the
	// order-history event addressed by its opaque external id. It is write-once:
	// the envelope is set only when the event carries none yet, so a retry never
	// clobbers the issued envelope. Signing is additive and runs after the event
	// is already durable, so this never mutates money or status.
	PersistEventAttestation(
		ctx context.Context, key Key, eventID domain.ExternalID, att domain.EventAttestation,
	) error

	// GetOrder returns the order with its events (each carrying its 1:1 signed
	// attestation when issued) and trades, addressed by the order's opaque
	// external id.
	GetOrder(ctx context.Context, order domain.ExternalID) (domain.OrderDetail, error)

	// ListOrders returns the most recent n orders for an account, newest first;
	// an empty source returns all sources.
	ListOrders(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Order, error)

	// ListOrderRows returns orders matching filter, with total count before
	// paging.
	ListOrderRows(
		ctx context.Context, filter store.OrderListFilter,
	) (store.OrderListPage, error)

	// ListAllOrders returns every order matching filters, newest first.
	ListAllOrders(
		ctx context.Context, account domain.AccountID, source domain.Source,
	) ([]domain.Order, error)

	// CountOrders returns the total number of orders recorded in the realm.
	CountOrders(ctx context.Context) (int, error)

	// CountActiveOrders returns orders in the working lifecycle set.
	CountActiveOrders(ctx context.Context) (int, error)

	// CountOrdersSince returns the number of orders recorded in the realm whose
	// timestamp is at or after since.
	CountOrdersSince(ctx context.Context, since time.Time) (int, error)

	// ListOrderEvents returns all events for the identified order, oldest first,
	// addressed by the order's opaque external id.
	ListOrderEvents(ctx context.Context, order domain.ExternalID) ([]domain.OrderEvent, error)

	// ListTrades returns the most recent n trades for an account, newest first;
	// an empty source returns all sources.
	ListTrades(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Trade, error)

	// ListAllTrades returns every trade matching filters, newest first.
	ListAllTrades(
		ctx context.Context, account domain.AccountID, source domain.Source,
	) ([]domain.Trade, error)

	// ListAudit returns the most recent n audit rows, newest first.
	ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error)

	// ListAuditFiltered returns the most recent n audit rows matching the filter,
	// newest first. A zero-value filter matches all rows.
	ListAuditFiltered(
		ctx context.Context, filter domain.AuditFilter, n int,
	) ([]domain.AuditRow, error)

	// AppendAudit persists one audit row stamped with the caller. It is the seam
	// the backend uses to record control-plane actions that have no node-mutating
	// counterpart (signing-key management, signing config, and approval token
	// issue/confirm/cancel). The entry's Actor and Source are taken from caller.
	AppendAudit(ctx context.Context, entry store.AuditEntry, caller domain.Caller) error

	// ListMcpAccess returns the stored per-command MCP enable/disable overrides
	// keyed by command name. MCP access is a control-plane-wide setting with no
	// engine side-effect, so it is a store passthrough.
	ListMcpAccess(ctx context.Context) (map[string]bool, error)

	// SetMcpAccess upserts the enabled state for one MCP command. The command is
	// validated against the catalogue by the backend before it reaches here.
	SetMcpAccess(ctx context.Context, command string, enabled bool, caller domain.Caller) error

	// GetUserSetting returns the stored value for (userID, key); ok is false when
	// no such setting exists. User settings are a store passthrough with no engine
	// side-effect.
	GetUserSetting(ctx context.Context, userID, key string) (value string, ok bool, err error)

	// SetUserSetting upserts one per-user key-value setting. Like MCP access it has
	// no engine side-effect; unlike it, personal UI preferences are not audited.
	SetUserSetting(ctx context.Context, userID, key, value string) error

	// ListMarketDataInstances returns all configured market-data source instances.
	ListMarketDataInstances(ctx context.Context) ([]domain.MarketDataInstance, error)

	// GetMarketDataInstance returns the configured instance identified by its
	// external id. The bool is false when no such instance exists.
	GetMarketDataInstance(
		ctx context.Context, id domain.ExternalID,
	) (domain.MarketDataInstance, bool, error)

	// CreateMarketDataInstance persists one market-data source instance and
	// audits the action. It returns the stored instance with its external id
	// populated. It has no engine side-effect.
	CreateMarketDataInstance(
		ctx context.Context, instance domain.MarketDataInstance, caller domain.Caller,
	) (domain.MarketDataInstance, error)

	// SetMarketDataInstanceEnabled toggles one market-data source instance and
	// audits the action. It changes persisted config only; runtime refresh is a
	// separate concern.
	SetMarketDataInstanceEnabled(
		ctx context.Context, id domain.ExternalID, enabled bool, caller domain.Caller,
	) error

	// UpdateMarketDataInstanceSettings replaces editable source settings and
	// audits the action. It changes persisted config only; runtime refresh is a
	// separate concern.
	UpdateMarketDataInstanceSettings(
		ctx context.Context, id domain.ExternalID, label, credentials string, caller domain.Caller,
	) error

	// DeleteMarketDataInstance removes one market-data source instance and
	// audits the action.
	DeleteMarketDataInstance(
		ctx context.Context, id domain.ExternalID, force bool, caller domain.Caller,
	) error

	// ListMarketDataInstruments returns every configured instrument of an
	// instance, addressed by the instance's external id.
	ListMarketDataInstruments(
		ctx context.Context, instance domain.ExternalID,
	) ([]domain.MarketDataInstrument, error)

	// UpsertMarketDataInstrument inserts or replaces one instrument mapping and
	// audits the action. It has no engine side-effect.
	UpsertMarketDataInstrument(
		ctx context.Context, instrument domain.MarketDataInstrument, caller domain.Caller,
	) error

	// SetMarketDataInstrumentEnabled toggles one instrument mapping and audits
	// the action.
	SetMarketDataInstrumentEnabled(
		ctx context.Context, instance domain.ExternalID, externalSymbol string, enabled bool, caller domain.Caller,
	) error

	// DeleteMarketDataInstrument removes one instrument mapping and audits the
	// action.
	DeleteMarketDataInstrument(
		ctx context.Context, instance domain.ExternalID, externalSymbol string, caller domain.Caller,
	) error

	// ListMarketDataQuotes returns latest quote snapshots for one instance, or
	// every instance when instance is the zero external id.
	ListMarketDataQuotes(
		ctx context.Context, instance domain.ExternalID,
	) ([]domain.MarketDataQuote, error)

	// CheckOrder runs a non-mutating pre-trade dry-run for probe against the
	// engine, returning whether the order would pass plus the would-be lock or
	// block. It mutates nothing and writes no audit row.
	CheckOrder(
		ctx context.Context, key Key, probe domain.OrderProbe,
	) (domain.CheckResult, error)

	// Close shuts the node down: it stops the engine and closes the store.
	// After Close the node is no longer usable. Close is idempotent.
	Close() error
}

// NodeRouter resolves routing keys to nodes and enumerates the full node set.
// It is the only way the backend reaches a node, so the same control-plane code
// works whether there is one LocalNode or many shards.
type NodeRouter interface {
	// Route returns the node that owns the given routing key. It returns an
	// error when no node owns the key.
	Route(key Key) (Node, error)

	// All returns every node known to the router. The returned slice is a
	// snapshot; mutating it does not affect the router.
	All() []Node
}
