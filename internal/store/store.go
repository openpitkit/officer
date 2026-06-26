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
// The seam is built around the Realm choke-point: a Store opens a backend, and
// every table operation hangs off a realm-scoped RealmStore obtained through
// Store.ForRealm. One schema or database holds exactly one realm, so business
// uniqueness is per realm automatically and the realm is never a per-row column.
//
// The first implementation is backed by SQLite (modernc.org/sqlite, a pure-Go
// driver); the OSS connector is single-realm. The seam is kept narrow and
// backend-agnostic, with the few per-backend SQL tokens isolated behind Dialect,
// so a different backend can slot in without touching the node or backend layers.
//
// Identity model surfaced by this interface: dictionaries (accounts, groups,
// assets, principals, market-data instances, signing keys) are addressed by
// their immutable code; machine records (orders, trades, order events,
// adjustments, audit rows) are addressed by their opaque external id. The
// internal surrogate key never crosses the interface boundary. The engine ids
// are internal too: they are populated on the entity the engine layer rebuilds
// its tree from (and read back for that internal use), but they are never
// serialized (json:"-") and are never a public handle.
package store

import (
	"context"
	"time"

	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/domain"
)

// AuditEntry is the input to AppendAudit: the fields the caller supplies for a
// new audit record. The store assigns the surrogate key, the external id, and
// the timestamp. Account and Actor are optional dictionary references by code,
// snapshot strings, preserved unchanged when dictionaries are later deleted.
type AuditEntry struct {
	// Actor is the code of the principal who initiated the action; empty for a
	// system-initiated action.
	Actor string
	// ActorTitle is the actor title captured at write time.
	ActorTitle string
	// Action is the category of the recorded action.
	Action domain.AuditAction
	// Account is the code of the account the action targeted, if any.
	Account domain.AccountID
	// AccountTitle is the account title captured at write time.
	AccountTitle string
	// Detail is a short human-readable description of the action.
	Detail string
	// Source is the channel that initiated the action.
	Source domain.Source
}

// BusinessCSVImportGroup is one account-group row selected for a transactional
// business CSV import.
type BusinessCSVImportGroup struct {
	Group  domain.AccountGroup
	Exists bool
}

// BusinessCSVImportAccount is one account row selected for a transactional
// business CSV import.
type BusinessCSVImportAccount struct {
	Account domain.Account
	Exists  bool
}

// BusinessCSVImport writes all selected business CSV rows and their per-row
// audit records in one store transaction.
type BusinessCSVImport struct {
	Groups      []BusinessCSVImportGroup
	Accounts    []BusinessCSVImportAccount
	Balances    []domain.Balance
	Adjustments []domain.AccountAdjustmentRecord
	Audits      []AuditEntry
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

// Store is the backend handle for the Pit Officer control plane. It owns the
// connection lifecycle and the schema; all data access goes through a
// realm-scoped RealmStore from ForRealm.
//
// The OSS SQLite connector is single-realm: it serves one fixed realm and
// rejects a non-matching realm id with an error wrapping domain.ErrInvalid.
type Store interface {
	// ForRealm returns the data-access handle bound to realm. The single-realm
	// connector accepts only its own realm id (and domain.DefaultRealm) and
	// returns an error wrapping domain.ErrInvalid for any other id.
	ForRealm(ctx context.Context, realm domain.RealmID) (RealmStore, error)

	// Migrate brings the database schema up to the version this build expects.
	Migrate(ctx context.Context) error

	// SchemaVersion returns the schema version currently applied to the database.
	SchemaVersion(ctx context.Context) (int, error)

	// Ping verifies that the database is reachable and responsive.
	Ping(ctx context.Context) error

	// Path returns the on-disk location of the database.
	Path() string

	// Reset closes, recreates, and migrates the backing database from scratch.
	Reset(ctx context.Context) error

	// Close releases the database connection. It is idempotent.
	Close() error
}

// RealmStore is the realm-scoped data-access seam: every table operation for one
// realm goes through it. The realm is bound by the connector when the handle is
// created and never appears as a method parameter.
//
// Method contract: every method that takes a context.Context honors
// cancellation and deadlines. Read methods return a non-nil empty slice when
// there is nothing to return. Methods that address a specific row map internal
// not-found/conflict conditions onto domain.ErrNotFound, domain.ErrAlreadyExists
// and domain.ErrConflict. A reference to an unknown dictionary code is reported
// as an error wrapping domain.ErrInvalid.
type RealmStore interface {
	// --- Assets (dictionary, addressed by code) ---

	// CreateAsset persists a new asset dictionary row. Returns
	// domain.ErrAlreadyExists when the code already exists.
	CreateAsset(ctx context.Context, asset domain.Asset) error

	// GetAsset returns the asset with the given code. The bool is false when no
	// such asset exists.
	GetAsset(ctx context.Context, code string) (domain.Asset, bool, error)

	// ListAssets returns every asset, ordered by code.
	ListAssets(ctx context.Context) ([]domain.Asset, error)

	// UpdateAsset replaces the mutable fields (title, asset class) of the asset
	// identified by code. Returns domain.ErrNotFound when absent.
	UpdateAsset(ctx context.Context, asset domain.Asset) error

	// DeleteAsset removes the asset and cascades its dependent rows when force is
	// true. Without force, cascade-destroying dependents return ErrHasDependents.
	DeleteAsset(ctx context.Context, code string, force bool) error

	// --- Principals (dictionary, addressed by code) ---

	// CreatePrincipal persists a new principal dictionary row. Returns
	// domain.ErrAlreadyExists when the code already exists.
	CreatePrincipal(ctx context.Context, principal domain.Principal) error

	// GetPrincipal returns the principal with the given code. The bool is false
	// when no such principal exists.
	GetPrincipal(ctx context.Context, code string) (domain.Principal, bool, error)

	// ListPrincipals returns every principal, ordered by code.
	ListPrincipals(ctx context.Context) ([]domain.Principal, error)

	// UpdatePrincipal replaces the mutable title of the principal identified by
	// code. Returns domain.ErrNotFound when absent.
	UpdatePrincipal(ctx context.Context, principal domain.Principal) error

	// DeletePrincipal removes the principal; references to it are cleared
	// (SET NULL). Returns domain.ErrNotFound when absent.
	DeletePrincipal(ctx context.Context, code string) error

	// --- Account groups (dictionary, addressed by code) ---

	// CreateGroup persists a new account group, assigning a collision-free engine
	// group id. It returns the stored group with EngineGroupID populated so the
	// engine layer can build its tree. Returns domain.ErrAlreadyExists when the
	// code already exists.
	CreateGroup(ctx context.Context, group domain.AccountGroup) (domain.AccountGroup, error)

	// GetGroup returns the group with the given code. The bool is false when no
	// such group exists.
	GetGroup(ctx context.Context, code string) (domain.AccountGroup, bool, error)

	// ListGroups returns every group, ordered by code.
	ListGroups(ctx context.Context) ([]domain.AccountGroup, error)

	// SetGroupNotes replaces the notes of the identified group. Returns
	// domain.ErrNotFound when absent.
	SetGroupNotes(ctx context.Context, code, notes string) error

	// SetGroupBlocked updates the blocked flag and block reason of the identified
	// group. Returns domain.ErrNotFound when absent.
	SetGroupBlocked(ctx context.Context, code string, blocked bool, reason string) error

	// DeleteGroup removes the group; member accounts have their group link cleared
	// (SET NULL). Returns domain.ErrNotFound when absent.
	DeleteGroup(ctx context.Context, code string) error

	// ListGroupAccounts returns every account whose group is code, ordered by
	// account code.
	ListGroupAccounts(ctx context.Context, code string) ([]domain.Account, error)

	// --- Accounts (dictionary, addressed by code) ---

	// CreateAccount persists a new account, assigning a collision-free engine
	// account id. It returns the stored account with EngineAccountID populated so
	// the engine layer can build its tree. The group link, when set, resolves the
	// account's group code to an existing group. Returns domain.ErrAlreadyExists
	// when the code already exists, or an error wrapping domain.ErrInvalid when
	// the referenced group code is unknown.
	CreateAccount(ctx context.Context, account domain.Account) (domain.Account, error)

	// GetAccount returns the account with the given code. The bool is false when
	// no such account exists.
	GetAccount(ctx context.Context, code domain.AccountID) (domain.Account, bool, error)

	// ListAccounts returns every account, ordered by code.
	ListAccounts(ctx context.Context) ([]domain.Account, error)

	// SetAccountBlocked updates the blocked flag and block reason of the
	// identified account. Returns domain.ErrNotFound when absent.
	SetAccountBlocked(
		ctx context.Context, code domain.AccountID, blocked bool, reason string,
	) error

	// SetAccountGroup sets the group link of the identified account to the group
	// with groupCode; an empty groupCode clears it. Returns domain.ErrNotFound
	// when the account is absent, or an error wrapping domain.ErrInvalid when the
	// group code is unknown.
	SetAccountGroup(ctx context.Context, code domain.AccountID, groupCode string) error

	// SetAccountNotes replaces the notes of the identified account. Returns
	// domain.ErrNotFound when absent.
	SetAccountNotes(ctx context.Context, code domain.AccountID, notes string) error

	// DeleteAccount removes the account and cascades its dependent rows when
	// force is true. Without force, cascade-destroying dependents return
	// ErrHasDependents.
	DeleteAccount(ctx context.Context, code domain.AccountID, force bool) error

	// --- Spot-funds balances (addressed by account+asset code) ---

	// UpsertBalance inserts or replaces the balance snapshot for the
	// (account, asset) the balance names. An unknown account or asset code is an
	// error wrapping domain.ErrInvalid.
	UpsertBalance(ctx context.Context, balance domain.Balance) error

	// GetBalance returns the balance for (account, asset). The bool is false when
	// no row exists.
	GetBalance(
		ctx context.Context, account domain.AccountID, asset string,
	) (domain.Balance, bool, error)

	// ListBalances returns balance rows matching the non-empty filter fields. An
	// empty account and asset returns every balance.
	ListBalances(
		ctx context.Context, account domain.AccountID, asset string,
	) ([]domain.Balance, error)

	// DeleteBalance removes the balance for (account, asset). Returns
	// domain.ErrNotFound when absent.
	DeleteBalance(ctx context.Context, account domain.AccountID, asset string) error

	// --- Per-policy limits (addressed by the scope+account+asset composite) ---

	// ListRateLimits returns every rate-limit barrier. When account is non-empty
	// only barriers carrying that account are returned.
	ListRateLimits(ctx context.Context, account domain.AccountID) ([]domain.LimitRate, error)

	// PutRateLimit upserts one rate-limit barrier keyed by its
	// (scope, account, asset) composite. Unknown account/asset codes are an error
	// wrapping domain.ErrInvalid.
	PutRateLimit(ctx context.Context, limit domain.LimitRate) error

	// DeleteRateLimit removes the rate-limit barrier with the given composite.
	// Returns domain.ErrNotFound when absent.
	DeleteRateLimit(
		ctx context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
	) error

	// ListOrderSizeLimits returns every order-size barrier, optionally narrowed to
	// the given account.
	ListOrderSizeLimits(
		ctx context.Context, account domain.AccountID,
	) ([]domain.LimitOrderSize, error)

	// PutOrderSizeLimit upserts one order-size barrier keyed by its composite.
	PutOrderSizeLimit(ctx context.Context, limit domain.LimitOrderSize) error

	// DeleteOrderSizeLimit removes the order-size barrier with the given
	// composite. Returns domain.ErrNotFound when absent.
	DeleteOrderSizeLimit(
		ctx context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
	) error

	// ListPnlBoundsLimits returns every P&L-bounds barrier, optionally narrowed to
	// the given account.
	ListPnlBoundsLimits(
		ctx context.Context, account domain.AccountID,
	) ([]domain.LimitPnlBounds, error)

	// PutPnlBoundsLimit upserts one P&L-bounds barrier keyed by its composite.
	PutPnlBoundsLimit(ctx context.Context, limit domain.LimitPnlBounds) error

	// DeletePnlBoundsLimit removes the P&L-bounds barrier with the given
	// composite. Returns domain.ErrNotFound when absent.
	DeletePnlBoundsLimit(
		ctx context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
	) error

	// --- Account adjustments (machine record, addressed by external id) ---

	// AppendAdjustment records an adjustment outcome. The store assigns the
	// external id and timestamp and returns the record with them populated.
	AppendAdjustment(
		ctx context.Context, rec domain.AccountAdjustmentRecord,
	) (domain.AccountAdjustmentRecord, error)

	// ListAdjustments returns the most recent n adjustments for an account,
	// newest first. An empty source returns all sources; a non-positive n returns
	// an empty slice.
	ListAdjustments(
		ctx context.Context,
		account domain.AccountID,
		source domain.Source,
		n int,
	) ([]domain.AccountAdjustmentRecord, error)

	// --- Orders (machine record, addressed by external id) ---

	// CreateOrder inserts a new order, assigning its external id. It returns the
	// order with ExternalID and At populated. Unknown account/asset codes are an
	// error wrapping domain.ErrInvalid.
	CreateOrder(ctx context.Context, o domain.Order) (domain.Order, error)

	// UpdateOrderStatus updates the status of the identified order. Returns
	// domain.ErrNotFound when absent.
	UpdateOrderStatus(ctx context.Context, id domain.ExternalID, status domain.OrderStatus) error

	// SetOrderLock replaces the persisted SDK-serialized lock blob of the
	// identified order. The lock is captured after the pre-trade reservation
	// commits, so it cannot be set at CreateOrder time. A nil lock clears the
	// column. Returns domain.ErrNotFound when absent.
	SetOrderLock(ctx context.Context, id domain.ExternalID, lock []byte) error

	// PutOrderApproval stamps the signed approval envelope onto the identified
	// order, write-once: it inserts the 1:1 order_approvals row only when the
	// order carries none yet, so a retry or a later write never clobbers an
	// already-issued envelope. A no-op (already stamped, or missing order) is not
	// an error: the envelope is best-effort and the order is the durable trail.
	PutOrderApproval(ctx context.Context, id domain.ExternalID, env domain.OrderApproval) error

	// GetOrder returns the order with its events and trades. Returns
	// domain.ErrNotFound when absent.
	GetOrder(ctx context.Context, id domain.ExternalID) (domain.OrderDetail, error)

	// ListOrders returns the most recent n orders for an account, newest first.
	// An empty source returns all sources; a non-positive n returns an empty
	// slice.
	ListOrders(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Order, error)

	// ListAllOrders returns every order for an account, newest first. It is used
	// by exports that must not inherit UI display limits.
	ListAllOrders(
		ctx context.Context, account domain.AccountID, source domain.Source,
	) ([]domain.Order, error)

	// CountOrders returns the total number of orders recorded in the realm.
	CountOrders(ctx context.Context) (int, error)

	// CountOrdersSince returns the number of orders whose at timestamp is at or
	// after since. Timestamps are compared as RFC3339Nano UTC text.
	CountOrdersSince(ctx context.Context, since time.Time) (int, error)

	// RecordOrderSettlement persists one fill/settlement atomically in a single
	// transaction: per-asset balances (realized P&L delta-accumulated inside the
	// tx), the optional trade, the engine-applied account blocks, the optional
	// lock rewrite, the fill event(s), and the order status advance commit or roll
	// back together. When AllowedFrom is non-empty the status UPDATE is guarded and
	// a disallowed current status yields domain.ErrConflict with nothing written;
	// otherwise a missing order yields domain.ErrNotFound. The block-audit row is
	// NOT part of this tx; callers write it separately after a successful commit.
	RecordOrderSettlement(ctx context.Context, st domain.OrderSettlement) error

	// --- Order events (machine record, addressed by external id) ---

	// AppendOrderEvent adds an event to an order's event stream. The store assigns
	// the external id and timestamp.
	AppendOrderEvent(ctx context.Context, ev domain.OrderEvent) (domain.OrderEvent, error)

	// ListOrderEvents returns all events for the identified order, oldest first.
	ListOrderEvents(ctx context.Context, order domain.ExternalID) ([]domain.OrderEvent, error)

	// --- Trades (machine record, addressed by external id) ---

	// CreateTrade records a fill as a standalone trade row. The store assigns the
	// external id and timestamp.
	CreateTrade(ctx context.Context, t domain.Trade) (domain.Trade, error)

	// ListTrades returns the most recent n trades for an account, newest first.
	// An empty source returns all sources; a non-positive n returns an empty
	// slice.
	ListTrades(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Trade, error)

	// ListAllTrades returns every trade for an account, newest first. It is used
	// by exports that must not inherit UI display limits.
	ListAllTrades(
		ctx context.Context, account domain.AccountID, source domain.Source,
	) ([]domain.Trade, error)

	// --- Audit trail (machine record, addressed by external id) ---

	// AppendAudit persists a new append-only audit record.
	AppendAudit(ctx context.Context, entry AuditEntry) error

	// ListAudit returns the most recent n audit rows, newest first. A non-positive
	// n returns an empty slice.
	ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error)

	// ListAuditFiltered returns the most recent n audit rows matching the filter,
	// newest first, applying the account, source, and action filters in the query
	// so the limit bounds the filtered set. A zero-value filter matches all rows;
	// a non-positive n returns an empty slice.
	ListAuditFiltered(
		ctx context.Context, filter domain.AuditFilter, n int,
	) ([]domain.AuditRow, error)

	// --- Market-data instances (dictionary, addressed by external id) ---

	// CreateMarketDataInstance persists a new instance, assigning its external id,
	// and returns it with ExternalID populated. Returns domain.ErrAlreadyExists on
	// a duplicate label.
	CreateMarketDataInstance(
		ctx context.Context, instance domain.MarketDataInstance,
	) (domain.MarketDataInstance, error)

	// GetMarketDataInstance returns the instance with the given external id. The
	// bool is false when absent.
	GetMarketDataInstance(
		ctx context.Context, id domain.ExternalID,
	) (domain.MarketDataInstance, bool, error)

	// ListMarketDataInstances returns every instance, ordered by external id.
	ListMarketDataInstances(ctx context.Context) ([]domain.MarketDataInstance, error)

	// ListEnabledMarketDataInstances returns the enabled instances, ordered by
	// external id.
	ListEnabledMarketDataInstances(ctx context.Context) ([]domain.MarketDataInstance, error)

	// SetMarketDataInstanceEnabled toggles the enabled flag of an instance.
	// Returns domain.ErrNotFound when absent.
	SetMarketDataInstanceEnabled(ctx context.Context, id domain.ExternalID, enabled bool) error

	// UpdateMarketDataInstanceSettings replaces the editable settings (label,
	// credentials) of an instance. Returns domain.ErrNotFound when absent, or
	// domain.ErrAlreadyExists on a duplicate label.
	UpdateMarketDataInstanceSettings(
		ctx context.Context, id domain.ExternalID, label, credentials string,
	) error

	// DeleteMarketDataInstance removes the instance and (by cascade) its
	// instruments and quotes when force is true. Without force, instruments
	// return ErrHasDependents.
	DeleteMarketDataInstance(ctx context.Context, id domain.ExternalID, force bool) error

	// --- Market-data instruments and quotes ---

	// UpsertMarketDataInstrument inserts or replaces one instrument of an
	// instance, keyed by (instance, external symbol). Unknown asset codes are an
	// error wrapping domain.ErrInvalid.
	UpsertMarketDataInstrument(ctx context.Context, instrument domain.MarketDataInstrument) error

	// ListMarketDataInstruments returns every instrument of the instance, ordered
	// by external symbol.
	ListMarketDataInstruments(
		ctx context.Context, instance domain.ExternalID,
	) ([]domain.MarketDataInstrument, error)

	// ListEnabledMarketDataInstruments returns the enabled instruments of the
	// instance, ordered by external symbol.
	ListEnabledMarketDataInstruments(
		ctx context.Context, instance domain.ExternalID,
	) ([]domain.MarketDataInstrument, error)

	// SetMarketDataInstrumentEnabled toggles the enabled flag of one instrument.
	// Returns domain.ErrNotFound when absent.
	SetMarketDataInstrumentEnabled(
		ctx context.Context, instance domain.ExternalID, externalSymbol string, enabled bool,
	) error

	// DeleteMarketDataInstrument removes one instrument of an instance. Returns
	// domain.ErrNotFound when absent.
	DeleteMarketDataInstrument(
		ctx context.Context, instance domain.ExternalID, externalSymbol string,
	) error

	// UpsertMarketDataQuote records the latest normalized quote for one configured
	// instrument, keyed by (instance, external symbol).
	UpsertMarketDataQuote(ctx context.Context, quote domain.MarketDataQuote) error

	// ListMarketDataQuotes returns latest quotes for one instance, ordered by
	// external symbol. A zero instance returns quotes for all instances.
	ListMarketDataQuotes(
		ctx context.Context, instance domain.ExternalID,
	) ([]domain.MarketDataQuote, error)

	// --- Signing keys and config (key_id is the key's own UUID handle) ---

	// UpsertSigningKey inserts or replaces a signing key row. PrivateKey must be
	// populated; it is stored as a BLOB.
	UpsertSigningKey(ctx context.Context, key domain.SigningKey) error

	// GetActiveSigningKey returns the currently active key with PrivateKey
	// populated. The bool is false when no active key exists.
	GetActiveSigningKey(ctx context.Context) (domain.SigningKey, bool, error)

	// GetSigningKey returns the key with the given key id, PrivateKey populated.
	// Returns domain.ErrNotFound when absent.
	GetSigningKey(ctx context.Context, keyID string) (domain.SigningKey, error)

	// ListSigningKeys returns all keys ordered by created_at DESC. PrivateKey is
	// NOT populated; use GetSigningKey/GetActiveSigningKey for private-key access.
	ListSigningKeys(ctx context.Context) ([]domain.SigningKey, error)

	// DeactivateAllSigningKeys sets active=0 for every signing key.
	DeactivateAllSigningKeys(ctx context.Context) error

	// GetSigningConfig returns the value for the given config key. The bool is
	// false when absent.
	GetSigningConfig(ctx context.Context, key string) (string, bool, error)

	// SetSigningConfig upserts the value for the given config key.
	SetSigningConfig(ctx context.Context, key, value string) error

	// --- MCP access control ---

	// ListMcpAccess returns the stored per-command MCP enable/disable overrides,
	// keyed by command name. An empty store returns a non-nil empty map.
	ListMcpAccess(ctx context.Context) (map[string]bool, error)

	// SetMcpAccess upserts the enabled state for one command.
	SetMcpAccess(ctx context.Context, command string, enabled bool) error

	// --- User settings ---

	// GetUserSetting returns the stored value for (userID, key). The bool is false
	// when absent; callers then apply their default.
	GetUserSetting(ctx context.Context, userID, key string) (string, bool, error)

	// SetUserSetting upserts one per-user key-value setting.
	SetUserSetting(ctx context.Context, userID, key, value string) error

	// ListUserSettings returns every persisted user setting, ordered for stable
	// backups. An empty store returns a non-nil empty slice.
	ListUserSettings(ctx context.Context) ([]domain.UserSetting, error)

	// --- Reservation intents (approval_id is the row's own UUID handle) ---

	// UpsertReservationIntent inserts or replaces a reservation intent row.
	UpsertReservationIntent(ctx context.Context, intent domain.ReservationIntent) error

	// GetReservationIntent returns the reservation intent for approvalID
	// regardless of state; found is false when absent.
	GetReservationIntent(
		ctx context.Context, approvalID string,
	) (domain.ReservationIntent, bool, error)

	// ListOpenReservationIntents returns all intents whose state is held.
	ListOpenReservationIntents(ctx context.Context) ([]domain.ReservationIntent, error)

	// SetReservationIntentState updates the state of the identified intent.
	// Returns domain.ErrNotFound when absent.
	SetReservationIntentState(
		ctx context.Context, approvalID string, state domain.ReservationIntentState,
	) error

	// ResolveOrderReservation resolves one reservation atomically in a single
	// transaction: the intent state flip, the order status advance, and the
	// lifecycle event(s) commit or roll back together. The order status UPDATE is
	// guarded by AllowedFrom; a current status outside the guard yields
	// domain.ErrConflict with nothing written, and a missing order yields
	// domain.ErrNotFound. A missing intent row is tolerated as a no-op; a zero
	// Order skips the order/event writes and only flips the intent.
	ResolveOrderReservation(ctx context.Context, r domain.ReservationResolution) error

	// --- Business CSV ---

	// ApplyBusinessCSVImport persists all selected business CSV rows and their
	// per-row audit records in one transaction. Any store error rolls the whole
	// import back, including audit rows.
	ApplyBusinessCSVImport(ctx context.Context, in BusinessCSVImport) error

	// --- Backup / restore (realm-portable) ---

	// ExportBackup reads the bound realm and emits a portable archive holding
	// only the rows the scope includes. Identity in the archive is portable:
	// dictionaries carry their code (plus title), machine records their external
	// id, and every cross-row link is expressed by code or external id. No
	// surrogate key and no engine id is serialized; the realm round-trips between
	// an isolated and a shared database with its public identity intact.
	ExportBackup(ctx context.Context, scope backup.Scope) (backup.Archive, error)

	// RestoreBackup imports a portable archive into the bound realm under opts.
	// Dictionaries are inserted first so foreign keys resolve, letting the
	// connector assign fresh surrogate and engine ids in the target database
	// while preserving the archive's code and external id values; machine records
	// then resolve their cross-row links through those preserved identities. The
	// restore honors opts.Mode (replace-all, overwrite, insert-missing) and the
	// scope selectors, and reports per-section counts. RestoreSummary.Restart
	// Required is the engine-rebuild signal: it is true when the restore touched
	// runtime-affecting sections, telling the node to rebuild the live engine.
	RestoreBackup(
		ctx context.Context, archive backup.Archive, opts backup.RestoreOptions,
	) (backup.RestoreSummary, error)
}
