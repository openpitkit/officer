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
// Concrete connectors can live outside this module. The seam is kept narrow and
// backend-agnostic. framework/store/schema owns the canonical DDL and isolates
// the few per-backend SQL tokens behind its Dialect interface, so a different
// backend can slot in without touching the node or backend layers.
//
// Identity model surfaced by this interface: dictionaries (accounts, groups,
// assets, principals, market-data instances, signing keys) are addressed by
// their public code; machine records (orders, trades, order events,
// adjustments, audit rows) are addressed by their opaque external id. The
// internal surrogate key never crosses the interface boundary. The engine ids
// are internal too: they are populated on the entity the engine layer rebuilds
// its tree from (and read back for that internal use), but they are never
// serialized (json:"-") and are never a public handle.
package store

import (
	"context"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
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
	// Asset is the code of the asset the action targeted, if any.
	Asset string
	// Group is the code of the account group the action targeted, if any. Group
	// actions carry no account, so this is the only structured handle a reader
	// can select them by; without it a group's rows are only reachable through
	// a substring match on Detail, which a crafted code can spoof.
	Group string
	// Detail is a short human-readable description of the action.
	Detail string
	// Source is the channel that initiated the action.
	Source domain.Source
}

// EventAttestor signs or intentionally skips one just-appended order event
// before the surrounding store transaction commits.
type EventAttestor func(
	ctx context.Context, event domain.OrderEvent,
) (domain.EventAttestation, bool, error)

// BalanceKey addresses one balance snapshot by account and asset.
type BalanceKey struct {
	Account domain.AccountID
	Asset   string
}

// AccountAdjustmentPersistence is the atomic persistence command for one
// engine-applied account adjustment.
type AccountAdjustmentPersistence struct {
	UpsertBalance *domain.Balance
	DeleteBalance *BalanceKey
	Adjustment    domain.AccountAdjustmentRecord
	Audit         AuditEntry
}

// TextMatcher is a parsed user text pattern. Fragments are literal text
// fragments that must appear in order. AnchorStart requires the first fragment
// to match from the value start; AnchorEnd requires the final fragment to end
// at the value end.
type TextMatcher struct {
	Fragments   []string
	AnchorStart bool
	AnchorEnd   bool
}

// Empty reports whether the matcher has no literal fragments or anchors.
func (m TextMatcher) Empty() bool {
	return len(m.Fragments) == 0 && !m.AnchorStart && !m.AnchorEnd
}

// ExactTextMatcher returns a matcher that requires value verbatim, anchored at
// both ends. An empty value yields the empty (unrestricted) matcher. It is the
// single constructor for an exact text filter, so connectors and the backend
// merge never re-derive one from a raw string.
func ExactTextMatcher(value string) TextMatcher {
	if value == "" {
		return TextMatcher{}
	}
	return TextMatcher{
		Fragments:   []string{value},
		AnchorStart: true,
		AnchorEnd:   true,
	}
}

// StatusFilter narrows rows by blocked state.
type StatusFilter string

const (
	// StatusFilterAll leaves blocked state unrestricted.
	StatusFilterAll StatusFilter = ""
	// StatusFilterActive selects rows that are not blocked.
	StatusFilterActive StatusFilter = "active"
	// StatusFilterBlocked selects rows that are blocked.
	StatusFilterBlocked StatusFilter = "blocked"
)

// CountFilter narrows rows by whether an aggregate count is zero or non-zero.
type CountFilter string

const (
	// CountFilterAll leaves the aggregate unrestricted.
	CountFilterAll CountFilter = ""
	// CountFilterHas selects rows with at least one aggregate member.
	CountFilterHas CountFilter = "has"
	// CountFilterNone selects rows with no aggregate members.
	CountFilterNone CountFilter = "none"
)

// CountRangeFilter narrows rows by an aggregate count value.
type CountRangeFilter struct {
	Min          *int
	Max          *int
	Equal        *int
	NotEqual     *int
	MinExclusive bool
	MaxExclusive bool
}

// Empty returns true when the aggregate count is unrestricted.
func (f CountRangeFilter) Empty() bool {
	return f.Min == nil && f.Max == nil && f.Equal == nil && f.NotEqual == nil
}

// DecimalRangeFilter narrows rows by a plain decimal value. Bounds are exact
// decimal strings validated upstream; the connector compares them numerically
// (the column carries a numeric collation), never an encoded key.
type DecimalRangeFilter struct {
	Min          *string
	Max          *string
	Equal        *string
	NotEqual     *string
	MinExclusive bool
	MaxExclusive bool
}

// Empty returns true when the decimal value is unrestricted.
func (f DecimalRangeFilter) Empty() bool {
	return f.Min == nil && f.Max == nil && f.Equal == nil && f.NotEqual == nil
}

// DenominatedDecimalRangeFilter narrows rows by a decimal value that is only
// comparable within a single denomination. Currency is the asset code the
// bounds are expressed in; a row denominated in any other asset is excluded
// rather than compared across denominations, and so is a row with no
// denomination at all. Currency carries no default: a bare number cannot be
// compared against a denominated column, so Validate rejects a non-empty Range
// without one.
type DenominatedDecimalRangeFilter struct {
	Range    DecimalRangeFilter
	Currency string
}

// Empty returns true when the decimal value is unrestricted.
func (f DenominatedDecimalRangeFilter) Empty() bool { return f.Range.Empty() }

// Validate reports whether the filter carries the denomination its bounds need.
// field names the filter for the returned error.
func (f DenominatedDecimalRangeFilter) Validate(field string) error {
	if !f.Range.Empty() && f.Currency == "" {
		return fmt.Errorf("%s: %w", field, ErrCurrencyRequired)
	}
	return nil
}

// ErrCurrencyRequired reports a threshold on a denominated value that names no
// currency to compare in.
var ErrCurrencyRequired = fmt.Errorf(
	"currency is required to compare this value: %w", domain.ErrInvalid,
)

// TimeRangeFilter narrows rows by RFC3339Nano UTC text timestamps.
type TimeRangeFilter struct {
	Min          *time.Time
	Max          *time.Time
	MinExclusive bool
	MaxExclusive bool
}

// Empty returns true when the timestamp is unrestricted.
func (f TimeRangeFilter) Empty() bool {
	return f.Min == nil && f.Max == nil
}

// SortSpec describes a whitelisted table sort key.
type SortSpec struct {
	Column     string
	Descending bool
}

// Empty returns true when the natural table order should be used.
func (s SortSpec) Empty() bool {
	return s.Column == ""
}

// PageSpec describes a server-side page window.
type PageSpec struct {
	Limit  int
	Offset int
}

// Empty returns true when no LIMIT/OFFSET should be applied.
func (p PageSpec) Empty() bool {
	return p.Limit <= 0 && p.Offset <= 0
}

// AccountListFilter narrows account-list reads in the store.
type AccountListFilter struct {
	// GroupCode narrows accounts to one exact group code. A non-nil empty
	// string selects accounts with no assigned group.
	GroupCode   *string
	Code        TextMatcher
	BlockReason TextMatcher
	Status      StatusFilter
	Position    CountRangeFilter
	Sort        SortSpec
	Page        PageSpec
}

// AccountListRow is an account row plus list-only aggregates.
type AccountListRow struct {
	Account       domain.Account
	PositionCount int
}

// AccountListPage is a paged account-list result.
type AccountListPage struct {
	Rows  []AccountListRow
	Total int
}

// AssetListFilter narrows asset-list reads in the store.
type AssetListFilter struct {
	Code  TextMatcher
	Class TextMatcher
	Sort  SortSpec
	Page  PageSpec
}

// AssetListPage is a paged asset-list result.
type AssetListPage struct {
	Rows  []domain.Asset
	Total int
}

// OrderListFilter narrows order-list reads in the store.
type OrderListFilter struct {
	Account    domain.AccountID
	Source     domain.Source
	ExternalID TextMatcher
	Side       *domain.OrderSide
	Status     []domain.OrderStatus
	BaseAsset  TextMatcher
	QuoteAsset TextMatcher
	Amount     DecimalRangeFilter
	Price      DecimalRangeFilter
	At         TimeRangeFilter
	Sort       SortSpec
	Page       PageSpec
}

// OrderListRow is an order row plus list-only data.
type OrderListRow struct {
	Order domain.Order
	// Signed reports whether the order carries a persisted Ed25519-signed
	// approval envelope (alg "ed25519"); false when unsigned or eSign-off.
	Signed bool
}

// OrderListPage is a paged order-list result.
type OrderListPage struct {
	Rows  []OrderListRow
	Total int
}

// BalanceListFilter narrows balance-list reads in the store. Available, Held
// and Incoming are quantities of the row's own asset, so the Asset matcher
// already denominates them. AverageEntryPrice and RealizedPnl are denominated
// in the account currency instead, which varies per row, so each carries the
// currency its own bounds are expressed in.
type BalanceListFilter struct {
	Account           TextMatcher
	GroupCode         *string
	Asset             TextMatcher
	Available         DecimalRangeFilter
	Held              DecimalRangeFilter
	Incoming          DecimalRangeFilter
	AverageEntryPrice DenominatedDecimalRangeFilter
	RealizedPnl       DenominatedDecimalRangeFilter
	UpdatedAt         TimeRangeFilter
	Sort              SortSpec
	Page              PageSpec
}

// Validate reports whether every denominated threshold names its currency.
func (f BalanceListFilter) Validate() error {
	if err := f.AverageEntryPrice.Validate("averageEntryPrice"); err != nil {
		return err
	}
	return f.RealizedPnl.Validate("realizedPnl")
}

// BalanceListRow is a balance row plus list-only data.
type BalanceListRow struct {
	Balance domain.Balance
}

// BalanceListPage is a paged balance-list result.
type BalanceListPage struct {
	Rows  []BalanceListRow
	Total int
}

// GroupListFilter narrows account-group list reads in the store.
type GroupListFilter struct {
	Code        TextMatcher
	Notes       TextMatcher
	BlockReason TextMatcher
	Status      StatusFilter
	Position    CountRangeFilter
	Account     CountRangeFilter
	Sort        SortSpec
	Page        PageSpec
}

// GroupListRow is a group row plus list-only aggregates.
type GroupListRow struct {
	Group         domain.AccountGroup
	AccountCount  int
	PositionCount int
}

// GroupListPage is a paged group-list result. Total counts the real groups
// matching the filter; the synthetic default group is not part of Total.
type GroupListPage struct {
	Rows  []GroupListRow
	Total int
}

// AssetClassListFilter narrows asset-class list reads in the store.
type AssetClassListFilter struct {
	Code  TextMatcher
	Notes TextMatcher
	Sort  SortSpec
	Page  PageSpec
}

// AssetClassListRow is an asset-class row plus list-only aggregates.
type AssetClassListRow struct {
	Class      domain.AssetClass
	AssetCount int
}

// AssetClassListPage is a paged asset-class list result. Total counts the
// classes matching the filter before paging.
type AssetClassListPage struct {
	Rows  []AssetClassListRow
	Total int
}

// PolicyKind discriminates the typed limit barriers flattened into the
// unified policy list. Its string values match the engine policy constants in
// the domain package (domain.PolicyRateLimit and siblings).
type PolicyKind string

const (
	// PolicyKindRate is the rate-limit barrier.
	PolicyKindRate PolicyKind = "rate_limit"
	// PolicyKindOrderSize is the order-size barrier.
	PolicyKindOrderSize PolicyKind = "order_size_limit"
	// PolicyKindSpotFundsPnlBounds is the SpotFunds self-computed P&L-bounds
	// kill-switch barrier.
	PolicyKindSpotFundsPnlBounds PolicyKind = "spot_funds_pnl_bounds_kill_switch"
)

// PolicyListFilter narrows the unified policy-list read. Account mirrors the
// Limits UI account filter; Kind, when set, restricts to one barrier kind.
type PolicyListFilter struct {
	Account      TextMatcher
	AccountGroup TextMatcher
	Asset        TextMatcher
	Kind         *PolicyKind
	Sort         SortSpec
	Page         PageSpec
}

// PolicyListRow is one barrier flattened into the common policy shape: the kind
// discriminator, the (scope, account, asset) composite all three barriers share,
// and exactly one populated typed value matching Kind. The money and size fields
// stay on the domain value types, never collapsed to a float.
type PolicyListRow struct {
	Kind               PolicyKind
	Scope              domain.LimitScope
	Account            domain.AccountID
	AccountGroup       string
	Asset              string
	Rate               *domain.LimitRate
	OrderSize          *domain.LimitOrderSize
	SpotFundsPnlBounds *domain.LimitSpotFundsPnlBounds
}

// PolicyListPage is a paged policy-list result with the total matching count
// before paging.
type PolicyListPage struct {
	Rows  []PolicyListRow
	Total int
}

// AdjustmentListFilter narrows the append-only adjustment list.
type AdjustmentListFilter struct {
	Account    TextMatcher
	Asset      TextMatcher
	ExternalID domain.ExternalID
	Source     domain.Source
	Status     *domain.AdjustmentStatus
	At         TimeRangeFilter
	Sort       SortSpec
	Page       PageSpec
}

// AdjustmentListPage is a paged adjustment-list result.
type AdjustmentListPage struct {
	Rows  []domain.AccountAdjustmentRecord
	Total int
}

// TradeListFilter narrows the append-only trade list.
type TradeListFilter struct {
	Account    TextMatcher
	ExternalID domain.ExternalID
	BaseAsset  TextMatcher
	QuoteAsset TextMatcher
	Side       *domain.OrderSide
	Source     domain.Source
	At         TimeRangeFilter
	Quantity   DecimalRangeFilter
	Price      DecimalRangeFilter
	LockPrice  DecimalRangeFilter
	Sort       SortSpec
	Page       PageSpec
}

// TradeListPage is a paged trade-list result.
type TradeListPage struct {
	Rows  []domain.Trade
	Total int
}

// AuditListFilter narrows the append-only audit list.
type AuditListFilter struct {
	Account    TextMatcher
	Asset      TextMatcher
	Group      TextMatcher
	ExternalID domain.ExternalID
	Actor      TextMatcher
	Source     domain.Source
	Actions    []domain.AuditAction
	Category   domain.AuditCategory
	At         TimeRangeFilter
	Page       PageSpec
}

// AuditListPage is a paged audit-list result.
type AuditListPage struct {
	Rows  []domain.AuditRow
	Total int
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

	// ListAssetRows returns persisted assets matching filter, with total count
	// before paging.
	ListAssetRows(ctx context.Context, filter AssetListFilter) (AssetListPage, error)

	// UpdateAsset replaces the public code and mutable fields (title, asset class)
	// of the asset identified by oldCode. A code rename is safe because balances
	// and other rows reference the asset by its surrogate id. Returns
	// domain.ErrNotFound when oldCode is absent, or domain.ErrAlreadyExists when
	// asset.Code already exists.
	UpdateAsset(
		ctx context.Context, oldCode string, asset domain.Asset,
	) (domain.Asset, error)

	// DeleteAsset removes the asset and cascades its dependent rows when force is
	// true. Without force, cascade-destroying dependents return ErrHasDependents.
	DeleteAsset(ctx context.Context, code string, force bool) error

	// --- Asset classes (dictionary, addressed by code) ---

	// CreateAssetClass persists a new asset-class dictionary row. Returns
	// domain.ErrAlreadyExists when the code already exists.
	CreateAssetClass(ctx context.Context, class domain.AssetClass) error

	// GetAssetClass returns the asset class with the given code. The bool is false
	// when no such class exists.
	GetAssetClass(ctx context.Context, code string) (domain.AssetClass, bool, error)

	// ListAssetClasses returns every asset class, ordered by code.
	ListAssetClasses(ctx context.Context) ([]domain.AssetClass, error)

	// ListAssetClassRows returns asset classes matching filter, with the aggregate
	// count of assets referencing each class and the total count before paging.
	ListAssetClassRows(
		ctx context.Context, filter AssetClassListFilter,
	) (AssetClassListPage, error)

	// UpdateAssetClass replaces the public code, title and notes of the class
	// identified by oldCode. On a code rename the referenced class row is updated
	// under the store's asset-class relationship so existing assets keep their
	// class. Returns domain.ErrNotFound when oldCode is absent, or
	// domain.ErrAlreadyExists when class.Code already exists.
	UpdateAssetClass(
		ctx context.Context, oldCode string, class domain.AssetClass,
	) (domain.AssetClass, error)

	// DeleteAssetClass removes the class. When assets still reference it, force
	// clears their class relationship in the same transaction; without force the
	// referencing assets are reported as ErrHasDependents. Returns
	// domain.ErrNotFound when absent.
	DeleteAssetClass(ctx context.Context, code string, force bool) error

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

	// ListGroupRows returns groups matching filter, with aggregate account and
	// position counts and the total count of real groups before paging. The list
	// surface includes the synthetic default group (code empty) for accounts
	// without a group; persisted dictionary reads through ListGroups do not. The
	// default group is pinned first on the first page (offset zero) and excluded
	// from the real-group sort, paging window, and Total.
	ListGroupRows(ctx context.Context, filter GroupListFilter) (GroupListPage, error)

	// SetGroupNotes replaces the notes of the identified group. Returns
	// domain.ErrNotFound when absent.
	SetGroupNotes(ctx context.Context, code, notes string) error

	// SetGroupCurrency sets or clears the currency asset for the identified
	// group. The empty code addresses the reserved default group tier.
	SetGroupCurrency(ctx context.Context, code, currency string) error

	// UpdateGroup replaces the public code and mutable title of the identified
	// group. Returns domain.ErrNotFound when oldCode is absent, or
	// domain.ErrAlreadyExists when group.Code already exists.
	UpdateGroup(
		ctx context.Context, oldCode string, group domain.AccountGroup,
	) (domain.AccountGroup, error)

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

	// ListAccountRows returns accounts matching filter, with aggregate position
	// counts and total count before paging.
	ListAccountRows(ctx context.Context, filter AccountListFilter) (AccountListPage, error)

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

	// SetAccountCurrency sets or clears the account-level currency asset.
	SetAccountCurrency(ctx context.Context, code domain.AccountID, currency string) error

	// SetAccountPnl replaces the account-currency P&L snapshot and its halt
	// reason together, mirroring an assignment the engine already applied. An
	// empty haltReason records that a non-empty pnl is authoritative. A non-empty
	// haltReason stores no numeric value even if pnl is also supplied. Returns
	// domain.ErrNotFound when the account is absent, or an error wrapping
	// domain.ErrInvalid when pnl is not a decimal.
	SetAccountPnl(
		ctx context.Context,
		code domain.AccountID,
		pnl string,
		haltReason domain.PnlHaltReason,
	) error

	// SetAccountNotes replaces the notes of the identified account. Returns
	// domain.ErrNotFound when absent.
	SetAccountNotes(ctx context.Context, code domain.AccountID, notes string) error

	// UpdateAccount replaces the public code and mutable title of the identified
	// account. Returns domain.ErrNotFound when oldCode is absent, or
	// domain.ErrAlreadyExists when account.Code already exists.
	UpdateAccount(
		ctx context.Context, oldCode domain.AccountID, account domain.Account,
	) (domain.Account, error)

	// DeleteAccount removes the account and cascades its dependent rows when
	// force is true. Without force, cascade-destroying dependents return
	// ErrHasDependents.
	DeleteAccount(ctx context.Context, code domain.AccountID, force bool) error

	// --- Spot-funds balances (addressed by account+asset code) ---

	// UpsertBalance inserts or replaces the balance snapshot for the
	// (account, asset) the balance names. An unknown account or asset code is an
	// error wrapping domain.ErrInvalid. AccountCurrency is read-only and derived
	// from account settings when the balance is read.
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

	// ListAccountsWithOpenBalances returns account codes carrying a non-zero
	// balance field, cost basis or account P&L. A halted P&L holds no trustworthy
	// number and never counts an account as open - neither the halt itself nor
	// any value retained behind it. Balance fields and cost basis are not P&L and
	// count whether or not a P&L is halted. Empty accounts means all accounts.
	ListAccountsWithOpenBalances(
		ctx context.Context, accounts []domain.AccountID,
	) ([]domain.AccountID, error)

	// ListBalanceRows returns balances matching filter, with total count before
	// paging. It returns filter validation errors, including a denominated range
	// whose bounds do not name a currency.
	ListBalanceRows(
		ctx context.Context, filter BalanceListFilter,
	) (BalanceListPage, error)

	// DeleteBalance removes the balance for (account, asset). Returns
	// domain.ErrNotFound when absent.
	DeleteBalance(ctx context.Context, account domain.AccountID, asset string) error

	// --- Per-policy limits (addressed by the scope+account+asset composite) ---

	// ListPolicyRows returns the three typed barrier tables flattened into one
	// sorted, paged list, with the total matching count before paging. It is the
	// list surface behind the Limits page; the per-kind reads below stay for the
	// engine-reconfigure and backup paths.
	ListPolicyRows(ctx context.Context, filter PolicyListFilter) (PolicyListPage, error)

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

	// ListSpotFundsPnlBoundsLimits returns every SpotFunds self-computed P&L
	// barrier, optionally narrowed to the given account.
	ListSpotFundsPnlBoundsLimits(
		ctx context.Context, account domain.AccountID,
	) ([]domain.LimitSpotFundsPnlBounds, error)

	// PutSpotFundsPnlBoundsLimit upserts one SpotFunds self-computed P&L
	// barrier keyed by its composite.
	PutSpotFundsPnlBoundsLimit(ctx context.Context, limit domain.LimitSpotFundsPnlBounds) error

	// DeleteSpotFundsPnlBoundsLimit removes the SpotFunds self-computed P&L
	// barrier with the given composite. Returns domain.ErrNotFound when absent.
	DeleteSpotFundsPnlBoundsLimit(
		ctx context.Context,
		scope domain.LimitScope,
		account domain.AccountID,
		accountGroup string,
	) error

	// --- Account adjustments (machine record, addressed by external id) ---

	// AppendAdjustment records an adjustment outcome. The store assigns the
	// external id and timestamp and returns the record with them populated.
	AppendAdjustment(
		ctx context.Context, rec domain.AccountAdjustmentRecord,
	) (domain.AccountAdjustmentRecord, error)

	// RecordAccountAdjustment persists the balance snapshot command, adjustment
	// record, and audit row in one transaction.
	RecordAccountAdjustment(
		ctx context.Context, in AccountAdjustmentPersistence,
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

	// ListAdjustmentRows returns adjustments matching filter, with total count
	// before paging.
	ListAdjustmentRows(
		ctx context.Context, filter AdjustmentListFilter,
	) (AdjustmentListPage, error)

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

	// PutEventAttestation stamps the signed attestation envelope onto the
	// identified order-history event, write-once: it inserts the 1:1
	// event_attestation row only when the event carries none yet, so a retry or a
	// later write never clobbers an already-issued envelope. An already-stamped
	// event is a no-op; a missing event returns domain.ErrNotFound so callers
	// cannot silently commit an unsigned event.
	PutEventAttestation(
		ctx context.Context, eventID domain.ExternalID, att domain.EventAttestation,
	) error

	// GetOrder returns the order with its events (each carrying its 1:1
	// attestation when present) and trades. Returns domain.ErrNotFound when
	// absent.
	GetOrder(ctx context.Context, id domain.ExternalID) (domain.OrderDetail, error)

	// ListOrders returns the most recent n orders for an account, newest first.
	// An empty source returns all sources; a non-positive n returns an empty
	// slice.
	ListOrders(
		ctx context.Context, account domain.AccountID, source domain.Source, n int,
	) ([]domain.Order, error)

	// ListOrderRows returns orders matching filter, with total count before
	// paging.
	ListOrderRows(
		ctx context.Context, filter OrderListFilter,
	) (OrderListPage, error)

	// ListAllOrders returns every order for an account, newest first. It is used
	// by exports that must not inherit UI display limits.
	ListAllOrders(
		ctx context.Context, account domain.AccountID, source domain.Source,
	) ([]domain.Order, error)

	// CountOrders returns the total number of orders recorded in the realm.
	CountOrders(ctx context.Context) (int, error)

	// CountActiveOrders returns orders in the working lifecycle set.
	CountActiveOrders(ctx context.Context) (int, error)

	// CountOrdersSince returns the number of orders whose at timestamp is at or
	// after since. Timestamps are compared as RFC3339Nano UTC text.
	CountOrdersSince(ctx context.Context, since time.Time) (int, error)

	// ExecutionReportExists reports whether id is already bound to a persisted
	// execution-report request in this realm.
	ExecutionReportExists(ctx context.Context, id domain.ExternalID) (bool, error)

	// RecordOrderSettlement persists one fill/settlement atomically in a single
	// transaction: the optional execution-report identity and all of its event
	// links, per-asset engine balance snapshots, the optional trade, the
	// engine-applied account blocks, the optional
	// lock rewrite, the fill event(s),
	// and the order status advance commit or roll back together. When AllowedFrom
	// is non-empty the status UPDATE is guarded and a disallowed current status
	// yields domain.ErrConflict with nothing written; otherwise a missing order
	// yields domain.ErrNotFound. The block-audit row is NOT part of this tx;
	// callers write it separately after a successful commit.
	RecordOrderSettlement(
		ctx context.Context, st domain.OrderSettlement,
	) (domain.ExternalID, error)

	// RecordOrderSubmission persists the submitted order and submitted event,
	// invokes apply with the persisted order while the same SQL transaction is
	// open, and then persists the returned settlement in that transaction.
	// Implementations roll the transaction back when apply returns an error.
	RecordOrderSubmission(
		ctx context.Context,
		order domain.Order,
		submitted domain.OrderEvent,
		apply func(domain.Order) (domain.OrderSettlement, error),
	) (domain.Order, error)

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

	// ListTradeRows returns trades matching filter, with total count before
	// paging.
	ListTradeRows(ctx context.Context, filter TradeListFilter) (TradeListPage, error)

	// --- Audit trail (machine record, addressed by external id) ---

	// AppendAudit persists a new append-only audit record.
	AppendAudit(ctx context.Context, entry AuditEntry) error

	// AppendAuditBatch persists all audit records atomically. An empty batch is a
	// no-op; any validation or write failure leaves every entry unapplied.
	AppendAuditBatch(ctx context.Context, entries []AuditEntry) error

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

	// ListAuditRows returns audit entries matching filter, with total count
	// before paging.
	ListAuditRows(ctx context.Context, filter AuditListFilter) (AuditListPage, error)

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

	// DeleteMarketDataInstance removes the source and its feed-owned
	// instruments and quotes. Global assets referenced by those instruments
	// are preserved.
	DeleteMarketDataInstance(ctx context.Context, id domain.ExternalID) error

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
	// scope selectors, and reports per-section counts. The store does not decide
	// whether an engine replacement is required; the node classifies and publishes
	// the committed runtime delta online, setting RestoreSummary.RestartRequired
	// only if it actually replaces the engine.
	RestoreBackup(
		ctx context.Context, archive backup.Archive, opts backup.RestoreOptions,
	) (backup.RestoreSummary, error)
}
