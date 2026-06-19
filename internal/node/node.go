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
// store. In the single-binary deployment there is exactly one node, a
// LocalNode running the engine and store in-process. In a future distributed
// deployment a routing node fans requests out to shards; the control plane
// reaches all of them only through a NodeRouter, so backend code never
// assumes a single node.
package node

import (
	"context"
	"time"

	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/store"
)

// Key is the routing key that identifies which node owns a given account. It is
// the {tenant, account} pair: a single account always resolves to exactly one
// node.
type Key struct {
	// Tenant is the isolation boundary that owns the account.
	Tenant domain.TenantID
	// Account is the account identifier within the tenant.
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

// Node is one execution target: an engine plus its backing store, behind a
// stable address the control plane can route to.
//
// The command methods below are the operations a future shard serves over RPC.
// In the single-binary deployment they all run in-process against the local
// engine and store. Every mutation follows one protocol (see localNode): the
// store is the source of truth and is written first, the engine is applied from
// persisted state, the store is reverted on engine failure, and an audit row is
// appended last. Runtime administrative changes rebuild the engine and reject
// new mutating requests while the rebuild is in progress.
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

	// CreateAccount persists a new account and audits the action. The account
	// is identified by key; caller carries the attribution stamped on the audit.
	CreateAccount(ctx context.Context, key Key, caller domain.Caller) (domain.Account, error)

	// SetAccountBlocked blocks or unblocks the account in the store and engine
	// and audits the action.
	SetAccountBlocked(
		ctx context.Context, key Key, blocked bool, reason string, caller domain.Caller,
	) error

	// SetAccountGroup sets or clears the account's group membership in the store
	// and engine (unregistering from the old group and registering into the new),
	// then audits the action. An empty groupID clears membership.
	SetAccountGroup(ctx context.Context, key Key, groupID string, caller domain.Caller) error

	// SetAccountNotes replaces the account's free-form notes in the store and
	// audits the action. Notes never reach the engine.
	SetAccountNotes(ctx context.Context, key Key, notes string, caller domain.Caller) error

	// GetAccountState returns the account row and the barriers whose scope has
	// the account axis and matches the account.
	GetAccountState(ctx context.Context, key Key) (domain.Account, []domain.Limit, error)

	// ListLimits returns the barriers that reference the account, or all
	// barriers when account is empty.
	ListLimits(ctx context.Context, account domain.AccountID) ([]domain.Limit, error)

	// PutLimit upserts the whole barrier in the store, reconfigures the live
	// policy from the persisted full barrier set, audits the action, and returns
	// a replacement market-data sink only when the engine was rebuilt.
	PutLimit(ctx context.Context, limit domain.Limit, caller domain.Caller) (marketdata.Sink, error)

	// DeleteLimit removes the barrier from the store, reconfigures the live
	// policy from the persisted full barrier set, audits the action, and returns
	// a replacement market-data sink only when the engine was rebuilt.
	DeleteLimit(ctx context.Context, target domain.LimitTarget, caller domain.Caller) (marketdata.Sink, error)

	// CreateGroup persists a new account group (store-only; membership lives on
	// accounts) and audits the action.
	CreateGroup(ctx context.Context, group domain.AccountGroup, caller domain.Caller) error

	// ListGroups returns every persisted group for the tenant.
	ListGroups(ctx context.Context, tenant domain.TenantID) ([]domain.AccountGroup, error)

	// GetGroup returns the group and its member accounts. The bool is false when
	// no such group exists.
	GetGroup(
		ctx context.Context, tenant domain.TenantID, id string,
	) (domain.AccountGroup, []domain.Account, bool, error)

	// SetGroupNotes replaces a group's notes in the store and audits the action.
	SetGroupNotes(ctx context.Context, tenant domain.TenantID, id, notes string, caller domain.Caller) error

	// SetGroupBlocked blocks or unblocks the group in the store and engine and
	// audits the action.
	SetGroupBlocked(
		ctx context.Context, tenant domain.TenantID, id string, blocked bool, reason string, caller domain.Caller,
	) error

	// DeleteGroup removes the group (store-only) and audits the action.
	DeleteGroup(ctx context.Context, tenant domain.TenantID, id string, caller domain.Caller) error

	// ApplyAdjustment applies one spot-funds adjustment through the engine,
	// persists the resulting balance snapshot and adjustment record, and audits
	// the action. On reject the balances are left unchanged and the rejected
	// record is recorded.
	ApplyAdjustment(
		ctx context.Context, key Key, req domain.AdjustmentRequest, caller domain.Caller,
	) (domain.AccountAdjustmentRecord, error)

	// ListBalances returns the balance rows for the tenant filtered by the
	// non-empty account and asset.
	ListBalances(
		ctx context.Context, tenant domain.TenantID, account domain.AccountID, asset string,
	) ([]domain.Balance, error)

	// GetBalance returns the balance for (tenant, account, asset). The bool is
	// false when no row exists.
	GetBalance(
		ctx context.Context, tenant domain.TenantID, account domain.AccountID, asset string,
	) (domain.Balance, bool, error)

	// ListAdjustments returns the most recent n adjustments for an account,
	// newest first; an empty source returns all sources.
	ListAdjustments(
		ctx context.Context, tenant domain.TenantID, account domain.AccountID, source domain.Source, n int,
	) ([]domain.AccountAdjustmentRecord, error)

	// SubmitOrder records the order, runs the engine pre-trade, persists the
	// lifecycle events and final status, and audits the action.
	SubmitOrder(ctx context.Context, key Key, o domain.Order, caller domain.Caller) (domain.Order, error)

	// SubmitHold records the order, runs the engine pre-trade keeping the
	// reservation held, persists the accept/reject lifecycle, and returns the
	// recorded order with the engine hold result. On accept the held amount stays
	// reserved on engine storage until ConfirmHeld or CancelHeld resolves it (or
	// the engine's TTL sweeper auto-rolls it back); the order status is left
	// accepted. On reject the order is recorded rejected and the result carries the
	// engine rejects. It does not audit; the backend audits approval_issued.
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
	// engine, records the reservation_committed event and committed status on the
	// order, and audits nothing (the backend audits approval_confirmed). A second
	// confirm on an already-resolved reservation returns domain.ErrConflict; an
	// unknown reservation returns domain.ErrNotFound.
	ConfirmHeld(
		ctx context.Context, tenant domain.TenantID, orderID int64, approvalID string, caller domain.Caller,
	) (domain.Order, error)

	// CancelHeld rolls back the held reservation identified by approvalID through
	// the engine, records the reservation_rolled_back event and cancelled status on
	// the order, and audits nothing (the backend audits approval_cancelled). It is
	// tolerant of an already-resolved reservation (idempotent).
	CancelHeld(
		ctx context.Context, tenant domain.TenantID, orderID int64, approvalID string, caller domain.Caller,
	) (domain.Order, error)

	// ReconcileOrphans reports persisted reservation intents still held after
	// restart. Held balance effects are durable, and confirm/cancel can fall
	// back to the persisted intent when the native handle is gone. It returns the
	// number found.
	ReconcileOrphans(ctx context.Context) (int, error)

	// ApplyExecutionReport settles a fill through the engine, records the fill
	// event and trade, mirrors any engine-recorded account blocks into the store,
	// reflects the order status, and audits the action.
	ApplyExecutionReport(
		ctx context.Context, key Key, in domain.ExecutionReportInput, caller domain.Caller,
	) (engine.ExecutionReportResult, error)

	// GetOrder returns the order with its events and trades.
	GetOrder(ctx context.Context, tenant domain.TenantID, id int64) (domain.OrderDetail, error)

	// ListOrders returns the most recent n orders for an account, newest first;
	// an empty source returns all sources.
	ListOrders(
		ctx context.Context, tenant domain.TenantID, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Order, error)

	// CountOrders returns the total number of orders recorded for the tenant.
	CountOrders(ctx context.Context, tenant domain.TenantID) (int, error)

	// CountOrdersSince returns the number of orders recorded for the tenant whose
	// timestamp is at or after since.
	CountOrdersSince(ctx context.Context, tenant domain.TenantID, since time.Time) (int, error)

	// ListOrderEvents returns all events for the identified order, oldest first.
	ListOrderEvents(ctx context.Context, tenant domain.TenantID, orderID int64) ([]domain.OrderEvent, error)

	// ListTrades returns the most recent n trades for an account, newest first;
	// an empty source returns all sources.
	ListTrades(
		ctx context.Context, tenant domain.TenantID, account domain.AccountID, source domain.Source, n int,
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

	// ListMarketDataInstances returns all configured market-data source instances.
	ListMarketDataInstances(ctx context.Context) ([]domain.MarketDataInstance, error)

	// GetMarketDataInstance returns the configured instance identified by id. The
	// bool is false when no such instance exists.
	GetMarketDataInstance(
		ctx context.Context, id string,
	) (domain.MarketDataInstance, bool, error)

	// CreateMarketDataInstance persists one market-data source instance and
	// audits the action. It has no engine side-effect.
	CreateMarketDataInstance(
		ctx context.Context, instance domain.MarketDataInstance, caller domain.Caller,
	) error

	// SetMarketDataInstanceEnabled toggles one market-data source instance and
	// audits the action. It changes persisted config only; runtime refresh is a
	// separate concern.
	SetMarketDataInstanceEnabled(ctx context.Context, id string, enabled bool, caller domain.Caller) error

	// UpdateMarketDataInstanceSettings replaces editable source settings and
	// audits the action. It changes persisted config only; runtime refresh is a
	// separate concern.
	UpdateMarketDataInstanceSettings(
		ctx context.Context, id, label, credentials string, caller domain.Caller,
	) error

	// DeleteMarketDataInstance removes one market-data source instance and
	// audits the action.
	DeleteMarketDataInstance(ctx context.Context, id string, caller domain.Caller) error

	// ListMarketDataInstruments returns every configured instrument of an
	// instance.
	ListMarketDataInstruments(
		ctx context.Context, instanceID string,
	) ([]domain.MarketDataInstrument, error)

	// UpsertMarketDataInstrument inserts or replaces one instrument mapping and
	// audits the action. It has no engine side-effect.
	UpsertMarketDataInstrument(
		ctx context.Context, instrument domain.MarketDataInstrument, caller domain.Caller,
	) error

	// SetMarketDataInstrumentEnabled toggles one instrument mapping and audits
	// the action.
	SetMarketDataInstrumentEnabled(
		ctx context.Context, instanceID, externalSymbol string, enabled bool, caller domain.Caller,
	) error

	// DeleteMarketDataInstrument removes one instrument mapping and audits the
	// action.
	DeleteMarketDataInstrument(
		ctx context.Context, instanceID, externalSymbol string, caller domain.Caller,
	) error

	// ListMarketDataQuotes returns latest quote snapshots for one instance, or
	// every instance when instanceID is empty.
	ListMarketDataQuotes(ctx context.Context, instanceID string) ([]domain.MarketDataQuote, error)

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
