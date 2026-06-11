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

// Package store defines the pluggable database connector seam for Pit Officer.
// The stage-1/2 implementation is backed by SQLite (modernc.org/sqlite, a
// pure-Go driver); the seam is kept narrow and database-agnostic so a larger
// database can back it later without touching the node or backend layers.
package store

import (
	"context"

	"go.openpit.dev/officer/internal/domain"
)

// AuditEntry is the input to AppendAudit: the fields the caller supplies for a
// new audit record. The store assigns the row id and timestamp.
type AuditEntry struct {
	// Actor identifies who initiated the action (the Principal).
	Actor string
	// Action is the category of the recorded action.
	Action domain.AuditAction
	// Tenant is the isolation boundary the action targeted, if any.
	Tenant domain.TenantID
	// Account is the account the action targeted, if any.
	Account domain.AccountID
	// Detail is a short human-readable description of the action.
	Detail string
	// Source is the channel that initiated the action.
	Source domain.Source
}

// StoreHealth reports the observable condition of the store.
type StoreHealth struct {
	// Path is the on-disk location of the database.
	Path string
	// SchemaVersion is the applied schema version.
	SchemaVersion int
	// Reachable reports whether the most recent Ping succeeded.
	Reachable bool
}

// Store is the persistence seam for the Pit Officer control plane.
//
// Method contract: every method that takes a context.Context honors
// cancellation and deadlines. Read methods return a non-nil empty slice when
// there is nothing to return. Methods that address a specific row map internal
// not-found/conflict conditions onto domain.ErrNotFound and
// domain.ErrAlreadyExists.
type Store interface {
	// Migrate brings the database schema up to the version this build expects.
	Migrate(ctx context.Context) error

	// SchemaVersion returns the schema version currently applied to the database.
	SchemaVersion(ctx context.Context) (int, error)

	// Ping verifies that the database is reachable and responsive.
	Ping(ctx context.Context) error

	// Path returns the on-disk location of the database.
	Path() string

	// ListAccounts returns every persisted account across all tenants.
	ListAccounts(ctx context.Context) ([]domain.Account, error)

	// GetAccount returns the account identified by (tenant, id). The bool is
	// false when no such account exists.
	GetAccount(ctx context.Context, tenant domain.TenantID, id domain.AccountID) (domain.Account, bool, error)

	// CreateAccount persists a new account. Returns domain.ErrAlreadyExists when
	// an account with the same (tenant, id) already exists.
	CreateAccount(ctx context.Context, account domain.Account) error

	// SetAccountBlocked updates the blocked flag and block reason for the
	// identified account. Returns domain.ErrNotFound when no such account exists.
	SetAccountBlocked(
		ctx context.Context,
		tenant domain.TenantID,
		id domain.AccountID,
		blocked bool,
		reason string,
	) error

	// SetAccountGroup sets the group_id for the identified account.
	// An empty groupID clears the membership. Returns domain.ErrNotFound when
	// no such account exists.
	SetAccountGroup(
		ctx context.Context,
		tenant domain.TenantID,
		id domain.AccountID,
		groupID string,
	) error

	// SetAccountNotes replaces the notes for the identified account.
	// Returns domain.ErrNotFound when no such account exists.
	SetAccountNotes(
		ctx context.Context,
		tenant domain.TenantID,
		id domain.AccountID,
		notes string,
	) error

	// ListLimits returns barriers grouped by (policy, scope, account, asset).
	// When account is non-empty only barriers that reference that account id are
	// returned. An empty account returns all rows.
	ListLimits(ctx context.Context, account domain.AccountID) ([]domain.Limit, error)

	// ListPolicyLimits returns all barriers for the given policy.
	ListPolicyLimits(ctx context.Context, policy string) ([]domain.Limit, error)

	// PutLimit upserts the whole barrier in one transaction: kinds for the target
	// not present in the payload are deleted; the rest are inserted or replaced.
	PutLimit(ctx context.Context, limit domain.Limit) error

	// DeleteLimit removes all kind rows for the given target. Returns
	// domain.ErrNotFound when no such barrier exists.
	DeleteLimit(ctx context.Context, target domain.LimitTarget) error

	// AppendAudit persists a new append-only audit record.
	AppendAudit(ctx context.Context, entry AuditEntry) error

	// ListAudit returns the most recent n audit rows, newest first. A
	// non-positive n returns an empty slice.
	ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error)

	// --- Account groups ---

	// CreateGroup persists a new account group. Returns domain.ErrAlreadyExists
	// when a group with the same (tenant, id) already exists.
	CreateGroup(ctx context.Context, group domain.AccountGroup) error

	// GetGroup returns the group identified by (tenant, id). The bool is false
	// when no such group exists.
	GetGroup(
		ctx context.Context,
		tenant domain.TenantID,
		id string,
	) (domain.AccountGroup, bool, error)

	// ListGroups returns every persisted group for the given tenant.
	ListGroups(ctx context.Context, tenant domain.TenantID) ([]domain.AccountGroup, error)

	// SetGroupNotes replaces the notes for the identified group.
	// Returns domain.ErrNotFound when no such group exists.
	SetGroupNotes(
		ctx context.Context,
		tenant domain.TenantID,
		id string,
		notes string,
	) error

	// SetGroupBlocked updates the blocked flag and block_reason for a group.
	// Returns domain.ErrNotFound when no such group exists.
	SetGroupBlocked(
		ctx context.Context,
		tenant domain.TenantID,
		id string,
		blocked bool,
		reason string,
	) error

	// DeleteGroup removes the group. Returns domain.ErrNotFound when absent.
	DeleteGroup(ctx context.Context, tenant domain.TenantID, id string) error

	// ListGroupAccounts returns all accounts whose group_id matches id.
	ListGroupAccounts(
		ctx context.Context,
		tenant domain.TenantID,
		groupID string,
	) ([]domain.Account, error)

	// --- Spot-funds balances ---

	// UpsertBalance inserts or replaces the balance snapshot for
	// (tenant, account, asset).
	UpsertBalance(ctx context.Context, b domain.Balance) error

	// GetBalance returns the balance for (tenant, account, asset). The bool is
	// false when no row exists.
	GetBalance(
		ctx context.Context,
		tenant domain.TenantID,
		account domain.AccountID,
		asset string,
	) (domain.Balance, bool, error)

	// ListBalances returns all balance rows matching the non-empty filter fields.
	// Pass empty account and asset to return all rows for the tenant.
	ListBalances(
		ctx context.Context,
		tenant domain.TenantID,
		account domain.AccountID,
		asset string,
	) ([]domain.Balance, error)

	// DeleteBalance removes the balance for (tenant, account, asset). Returns
	// domain.ErrNotFound when absent.
	DeleteBalance(
		ctx context.Context,
		tenant domain.TenantID,
		account domain.AccountID,
		asset string,
	) error

	// --- Account adjustments ---

	// AppendAdjustment records an adjustment outcome. The store assigns the ID
	// and timestamp.
	AppendAdjustment(
		ctx context.Context,
		rec domain.AccountAdjustmentRecord,
	) (domain.AccountAdjustmentRecord, error)

	// ListAdjustments returns the most recent n adjustments for an account,
	// newest first. Pass empty source to return all sources.
	ListAdjustments(
		ctx context.Context,
		tenant domain.TenantID,
		account domain.AccountID,
		source domain.Source,
		n int,
	) ([]domain.AccountAdjustmentRecord, error)

	// --- Orders ---

	// CreateOrder inserts a new order record. The store assigns the ID.
	// Returns the order with ID populated.
	CreateOrder(ctx context.Context, o domain.Order) (domain.Order, error)

	// UpdateOrderStatus updates the status of the identified order.
	// Returns domain.ErrNotFound when absent.
	UpdateOrderStatus(
		ctx context.Context,
		tenant domain.TenantID,
		id int64,
		status domain.OrderStatus,
	) error

	// SetOrderLockPrices replaces the persisted lock prices of the identified
	// order with prices (exact decimal strings). The reservation lock prices are
	// captured after the pre-trade reservation commits, so they cannot be set at
	// CreateOrder time. Returns domain.ErrNotFound when absent.
	SetOrderLockPrices(
		ctx context.Context,
		tenant domain.TenantID,
		id int64,
		prices []string,
	) error

	// GetOrder returns the order with its events and trades.
	// Returns domain.ErrNotFound when absent.
	GetOrder(
		ctx context.Context,
		tenant domain.TenantID,
		id int64,
	) (domain.OrderDetail, error)

	// ListOrders returns the most recent n orders for an account, newest first.
	// Pass empty source to return all sources.
	ListOrders(
		ctx context.Context,
		tenant domain.TenantID,
		account domain.AccountID,
		source domain.Source,
		n int,
	) ([]domain.Order, error)

	// --- Order events ---

	// AppendOrderEvent adds an event to an order's event stream.
	// The store assigns the ID and timestamp.
	AppendOrderEvent(ctx context.Context, ev domain.OrderEvent) (domain.OrderEvent, error)

	// ListOrderEvents returns all events for the identified order, oldest first.
	ListOrderEvents(
		ctx context.Context,
		tenant domain.TenantID,
		orderID int64,
	) ([]domain.OrderEvent, error)

	// --- Trades ---

	// CreateTrade records a fill as a standalone trade row.
	// The store assigns the ID and timestamp.
	CreateTrade(ctx context.Context, t domain.Trade) (domain.Trade, error)

	// ListTrades returns the most recent n trades for an account, newest first.
	// Pass empty source to return all sources.
	ListTrades(
		ctx context.Context,
		tenant domain.TenantID,
		account domain.AccountID,
		source domain.Source,
		n int,
	) ([]domain.Trade, error)

	// Close releases the database connection. It is idempotent.
	Close() error
}
