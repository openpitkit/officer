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

// Package domain holds the transport-agnostic value types shared across the
// Pit Officer control plane. These types are Pit Officer's own vocabulary; they
// are deliberately independent of the OpenPit binding's param types so that the
// store, node, and backend layers never depend on cgo or the native runtime.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// DefaultUserID is the implicit user used until real users exist; every user
// setting is currently stored under it. It is a fixed, well-known UUID rather
// than a plain label so that once settings are shared across nodes, databases,
// and clusters, user ids are globally unique and never collide between users.
const DefaultUserID = "00000000-0000-4000-8000-000000000001"

// User-setting keys. Values are opaque strings interpreted by the surface that
// owns the setting.
const (
	// UserSettingWelcomeSeen records that the operator dismissed the first-run
	// welcome dialog with "don't show again". Value "1" means seen.
	UserSettingWelcomeSeen = "welcome_seen"
)

// UserSetting is one persisted per-user key-value preference.
type UserSetting struct {
	UserID string
	Key    string
	Value  string
}

// Sentinel errors returned by all layers; callers check with errors.Is.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrInvalid       = errors.New("invalid")
	// ErrForbidden marks a request whose caller is known but not authorized for
	// the requested route or command.
	ErrForbidden = errors.New("forbidden")
	// ErrTooLarge marks an input that exceeds a hard server-side size limit.
	// HTTP surfaces map it to 413 so callers can split bulk payloads instead of
	// retrying the same request.
	ErrTooLarge = errors.New("too large")
	// ErrConflict marks an operation that cannot proceed because the target is
	// already in a terminal or incompatible state - for example confirming an
	// approval whose held reservation was already rolled back or expired. The
	// surface layer maps it to an HTTP 409.
	ErrConflict = errors.New("conflict")
	// ErrHasDependents marks a delete that would cascade-delete dependent rows
	// without an explicit force flag. The concrete error carries the blockers.
	ErrHasDependents = errors.New("has dependents")
	// ErrTerminalOrder marks the remaining Officer safety net for terminal
	// orders; callers can bypass it with force and route straight to the engine.
	ErrTerminalOrder = errors.New("order in terminal status")
	// ErrNoChange marks a successful engine command that produced no account
	// modifications for Officer to persist.
	ErrNoChange = errors.New("no change")
	// ErrNotImplemented marks an operation that needs an engine SDK capability
	// that is not available yet and cannot be represented as a full engine
	// rebuild from persisted state.
	ErrNotImplemented = errors.New("not implemented")
	// ErrEngineRestarting marks a short administrative restart window where the
	// live engine is being rebuilt from persisted state. Mutating surfaces reject
	// new requests during that window instead of queuing work behind the restart.
	ErrEngineRestarting = errors.New("engine restarting")
	// ErrUpstream marks a failure to reach or get a usable answer from an
	// external provider (e.g. a market-data REST call returning 403/429 or
	// timing out). It is an expected operational condition, not a server bug, so
	// handlers surface it as a 502 with a plain message instead of a 500
	// "unhandled internal error".
	ErrUpstream = errors.New("upstream provider error")
)

// DependentCount names one dependent row kind that blocks a destructive delete.
type DependentCount struct {
	Kind  string
	Count int
}

// HasDependentsError carries the blockers for a destructive delete.
type HasDependentsError struct {
	Dependents []DependentCount
}

// Error returns the stable delete-policy error text.
func (e HasDependentsError) Error() string { return ErrHasDependents.Error() }

// Unwrap lets callers match the typed error with errors.Is.
func (e HasDependentsError) Unwrap() error { return ErrHasDependents }

// NewHasDependentsError creates a typed delete-policy error.
func NewHasDependentsError(dependents []DependentCount) error {
	return HasDependentsError{Dependents: dependents}
}

// Policy identifiers.
const (
	PolicyRateLimit           = "rate_limit"
	PolicyOrderSizeLimit      = "order_size_limit"
	PolicyPnlBoundsKillSwitch = "pnl_bounds_kill_switch"
)

// Scope identifiers.
const (
	ScopeBroker       = "broker"
	ScopeAsset        = "asset"
	ScopeAccount      = "account"
	ScopeAccountAsset = "account_asset"
)

// Kind identifiers.
const (
	KindMaxOrders   = "max_orders"
	KindWindow      = "window"
	KindMaxQuantity = "max_quantity"
	KindMaxNotional = "max_notional"
	KindLowerBound  = "lower_bound"
	KindUpperBound  = "upper_bound"
	KindInitialPnl  = "initial_pnl"
)

// AccountID is the operator-chosen account code: the human handle a dictionary
// account is addressed by. It is unique per realm and does not leak record
// counts the way an autoincrement would. The engine id and the internal
// surrogate key are separate and never travel as this code.
type AccountID string

// String returns the raw account code.
func (id AccountID) String() string { return string(id) }

// Account is the control-plane view of an engine account: a dictionary entity
// addressed by its public Code, displayed under a mutable Title, and run on the
// engine under EngineAccountID. The surrogate key never appears here.
type Account struct {
	// Code is the operator-chosen account code, unique per realm.
	Code AccountID
	// Title is the mutable human-readable display name; may be empty.
	Title string
	// GroupCode links the account to its group by the group's public code;
	// empty means the account belongs to no group. The store resolves it to the
	// group's surrogate key; the engine group id is derived from the group.
	GroupCode string
	// Notes is a free-form reference string. Never forwarded to the engine.
	// Max 4096 bytes; validated by ValidateNotes.
	Notes string
	// BlockReason is the human-readable reason the account was blocked.
	// Empty when the account is not blocked.
	BlockReason string
	// EngineAccountID is the integer id the engine runs this account on: the
	// account row's surrogate id. It is internal and never serialized on the wire
	// (json:"-"); zero means unassigned. The engine layer consumes it on read
	// paths, but it is never a public handle.
	EngineAccountID EngineAccountID `json:"-"`
	// Blocked reports whether the account is currently kill-switched.
	Blocked bool
}

// AuditAction classifies a control-plane action recorded in the audit trail.
type AuditAction string

const (
	AuditActionHydrate           AuditAction = "hydrate"
	AuditActionCreateAsset       AuditAction = "create_asset"
	AuditActionUpdateAsset       AuditAction = "update_asset"
	AuditActionDeleteAsset       AuditAction = "delete_asset"
	AuditActionCreateAccount     AuditAction = "create_account"
	AuditActionUpdateAccount     AuditAction = "update_account"
	AuditActionDeleteAccount     AuditAction = "delete_account"
	AuditActionBlock             AuditAction = "block"
	AuditActionUnblock           AuditAction = "unblock"
	AuditActionSetLimit          AuditAction = "set_limit"
	AuditActionDeleteLimit       AuditAction = "delete_limit"
	AuditActionSetGroupNotes     AuditAction = "set_group_notes"
	AuditActionBlockGroup        AuditAction = "block_group"
	AuditActionUnblockGroup      AuditAction = "unblock_group"
	AuditActionSetNotes          AuditAction = "set_notes"
	AuditActionSetGroup          AuditAction = "set_group"
	AuditActionAdjustment        AuditAction = "adjustment"
	AuditActionCreateGroup       AuditAction = "create_group"
	AuditActionUpdateGroup       AuditAction = "update_group"
	AuditActionDeleteGroup       AuditAction = "delete_group"
	AuditActionCreateAssetClass  AuditAction = "create_asset_class"
	AuditActionUpdateAssetClass  AuditAction = "update_asset_class"
	AuditActionDeleteAssetClass  AuditAction = "delete_asset_class"
	AuditActionSubmitOrder       AuditAction = "submit_order"
	AuditActionExecutionReport   AuditAction = "execution_report"
	AuditActionSetMcpAccess      AuditAction = "set_mcp_access"
	AuditActionSetMarketData     AuditAction = "set_market_data"
	AuditActionExportBackup      AuditAction = "export_backup"
	AuditActionRestoreBackup     AuditAction = "restore_backup"
	AuditActionExportBusinessCSV AuditAction = "export_business_csv"
	AuditActionImportBusinessCSV AuditAction = "import_business_csv"
	AuditActionResetDatabase     AuditAction = "reset_database"

	// Signing-key lifecycle.
	AuditActionGenerateSigningKey AuditAction = "generate_signing_key"
	AuditActionImportSigningKey   AuditAction = "import_signing_key"
	AuditActionSetSigningConfig   AuditAction = "set_signing_config"

	// Approval token lifecycle.
	AuditActionApprovalIssued    AuditAction = "approval_issued"
	AuditActionApprovalFailed    AuditAction = "approval_failed"
	AuditActionApprovalConfirmed AuditAction = "approval_confirmed"
	AuditActionApprovalCancelled AuditAction = "approval_cancelled"
)

// AuditCategory groups audit actions by operator relevance. Trading actions are
// the high-volume order/fill stream; control actions are everything else
// (account administration, limits, blocks, keys, backups), which an operator
// usually wants to read without trading noise.
type AuditCategory string

const (
	// AuditCategoryControl is the control-plane administration stream.
	AuditCategoryControl AuditCategory = "control"
	// AuditCategoryTrading is the high-volume order/execution stream.
	AuditCategoryTrading AuditCategory = "trading"
)

// Category classifies an audit action. Order submissions and execution reports
// are trading activity; every other action is control-plane.
func (a AuditAction) Category() AuditCategory {
	switch a {
	case AuditActionSubmitOrder, AuditActionExecutionReport:
		return AuditCategoryTrading
	default:
		return AuditCategoryControl
	}
}

// AllAuditActions returns every audit action constant, control actions first.
// It is the source of truth for the audit filter catalogue; new actions must be
// added here so the default control-only listing keeps excluding trading noise.
func AllAuditActions() []AuditAction {
	return []AuditAction{
		AuditActionHydrate,
		AuditActionCreateAsset,
		AuditActionUpdateAsset,
		AuditActionDeleteAsset,
		AuditActionCreateAccount,
		AuditActionUpdateAccount,
		AuditActionDeleteAccount,
		AuditActionBlock,
		AuditActionUnblock,
		AuditActionSetLimit,
		AuditActionDeleteLimit,
		AuditActionSetGroupNotes,
		AuditActionBlockGroup,
		AuditActionUnblockGroup,
		AuditActionSetNotes,
		AuditActionSetGroup,
		AuditActionAdjustment,
		AuditActionCreateGroup,
		AuditActionUpdateGroup,
		AuditActionDeleteGroup,
		AuditActionCreateAssetClass,
		AuditActionUpdateAssetClass,
		AuditActionDeleteAssetClass,
		AuditActionSetMcpAccess,
		AuditActionSetMarketData,
		AuditActionExportBackup,
		AuditActionRestoreBackup,
		AuditActionExportBusinessCSV,
		AuditActionImportBusinessCSV,
		AuditActionResetDatabase,
		AuditActionGenerateSigningKey,
		AuditActionImportSigningKey,
		AuditActionSetSigningConfig,
		AuditActionApprovalIssued,
		AuditActionApprovalFailed,
		AuditActionApprovalConfirmed,
		AuditActionApprovalCancelled,
		AuditActionSubmitOrder,
		AuditActionExecutionReport,
	}
}

// AuditActionsByCategory returns every audit action in the given category,
// in the canonical order of AllAuditActions.
func AuditActionsByCategory(category AuditCategory) []AuditAction {
	var out []AuditAction
	for _, action := range AllAuditActions() {
		if action.Category() == category {
			out = append(out, action)
		}
	}
	return out
}

// AuditActionsForCategory resolves a category query value to an action
// include-set. "" and "control" select the control plane; "trading" selects the
// trading stream; "all" disables the action filter (nil). known is false for any
// other value, so callers can choose to reject or default it. A nil action slice
// means no filter (all actions).
func AuditActionsForCategory(category string) (actions []AuditAction, known bool) {
	switch category {
	case "", string(AuditCategoryControl):
		return AuditActionsByCategory(AuditCategoryControl), true
	case string(AuditCategoryTrading):
		return AuditActionsByCategory(AuditCategoryTrading), true
	case "all":
		return nil, true
	default:
		return nil, false
	}
}

// AuditFilter narrows an audit listing. A zero value disables every filter and
// matches all rows: an empty Account or Source matches any, and a nil/empty
// Actions matches any action; otherwise only the listed actions are returned.
type AuditFilter struct {
	Account AccountID
	Source  Source
	Actions []AuditAction
}

// AuditRow is the persisted, immutable record of a single control-plane action.
// It is a machine record: its public handle is ExternalID, the surrogate key
// never appears here. Account and Actor are immutable snapshot strings, so
// deleting the dictionaries they named does not mutate audit history.
type AuditRow struct {
	// At is the wall-clock time the action was recorded, in UTC.
	At time.Time
	// ExternalID is the opaque public handle of this audit row.
	ExternalID ExternalID
	// Actor is the code of the principal who initiated the action; empty when
	// the action was system-initiated.
	Actor string
	// ActorTitle is the actor title captured when the row was written.
	ActorTitle string
	// Action is the category of the recorded action.
	Action AuditAction
	// Account is the code of the account the action targeted; empty when none.
	Account AccountID
	// AccountTitle is the account title captured when the row was written.
	AccountTitle string
	// Asset is the code of the asset the action targeted; empty when none.
	Asset string
	// Detail is a short human-readable description of the action.
	Detail string
	// Source is the channel through which the action was initiated.
	Source Source
}

// OrderProbe carries the submit-order inputs for a non-mutating order check.
// It mirrors the SubmitOrder request fields exactly; the engine runs the same
// pre-trade pipeline as a dry-run, mutating nothing. All monetary/size values
// are exact decimal strings.
type OrderProbe struct {
	// Account is the account the order would be placed for.
	Account AccountID
	// BaseAsset is the asset that would be bought or sold.
	BaseAsset string
	// QuoteAsset is the asset used for pricing.
	QuoteAsset string
	// AmountValue is the size of the order (exact decimal string).
	AmountValue string
	// Price is the limit price (exact decimal string); empty means
	// market/unpriced.
	Price string
	// Side is buy or sell.
	Side OrderSide
	// AmountKind distinguishes quantity-based from volume-based sizing.
	AmountKind OrderAmountKind
}

// CheckResult is the outcome of a non-mutating order check (pre-trade dry-run).
// On pass WouldLockPrices holds the prices the engine would lock at reservation
// time; on reject Rejects holds the structured engine rejects and WouldBlock,
// when non-nil, is the account block the engine would record.
type CheckResult struct {
	// WouldBlock is the account block the engine would record; nil when none.
	WouldBlock *ExecutionAccountBlock
	// Rejects are the engine pre-trade rejects; empty when the check passed.
	Rejects []OrderReject
	// WouldLockPrices are the reservation lock prices the order would lock
	// (exact decimal strings); empty when nothing would be locked.
	WouldLockPrices []string
	// Passed reports whether the order would pass pre-trade.
	Passed bool
}

// ValidateAccountID returns an error wrapping ErrInvalid when id is not a
// well-formed account identifier: non-empty, at most 64 code points, printable,
// no leading or trailing whitespace.
func ValidateAccountID(id AccountID) error {
	s := string(id)
	if s == "" {
		return fmt.Errorf("account id is empty: %w", ErrInvalid)
	}
	if utf8.RuneCountInString(s) > 64 {
		return fmt.Errorf("account id exceeds 64 code points: %w", ErrInvalid)
	}
	if strings.TrimSpace(s) != s {
		return fmt.Errorf("account id has leading or trailing whitespace: %w", ErrInvalid)
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("account id contains non-printable character: %w", ErrInvalid)
		}
	}
	return nil
}

// ValidateAsset returns an error wrapping ErrInvalid when asset is not a
// well-formed asset identifier (non-empty, no whitespace, at most 32 chars). It
// is the boundary format check the spot-funds and trading surfaces apply to an
// asset id; it never checks existence.
func ValidateAsset(asset string) error {
	return validateAsset(asset)
}

// ValidateMarketDataMark returns an error wrapping ErrInvalid when mark is a
// non-empty, non-decimal mark price. An empty string is valid and means "no
// manual price". The mark maps to the engine Quote mark, a signed Option<Price>,
// so sign and zero are accepted; only decimal syntax is checked here.
func ValidateMarketDataMark(mark string) error {
	if mark == "" {
		return nil
	}
	if _, err := decimal.NewFromString(mark); err != nil {
		return fmt.Errorf("mark %q is not a valid decimal: %w", mark, ErrInvalid)
	}
	return nil
}

// ValidateMarketDataStrike returns an error wrapping ErrInvalid when strike is a
// non-empty, non-decimal search criterion (e.g. "abc", "NaN", "Inf"). An empty
// string is valid and applies no constraint. Unlike a mark, a strike is a search
// filter rather than a price, so it is checked for decimal syntax only; zero and
// negative values are not rejected here, mirroring the connector's accepted form.
func ValidateMarketDataStrike(strike string) error {
	if strike == "" {
		return nil
	}
	if _, err := decimal.NewFromString(strike); err != nil {
		return fmt.Errorf("strike %q is not a valid decimal: %w", strike, ErrInvalid)
	}
	return nil
}

// validateAsset returns an error when the asset string is not well-formed:
// non-empty, no whitespace, at most 32 chars.
func validateAsset(asset string) error {
	if asset == "" {
		return fmt.Errorf("asset is empty: %w", ErrInvalid)
	}
	if len(asset) > 32 {
		return fmt.Errorf("asset exceeds 32 chars: %w", ErrInvalid)
	}
	for _, r := range asset {
		if unicode.IsSpace(r) {
			return fmt.Errorf("asset contains whitespace: %w", ErrInvalid)
		}
	}
	return nil
}

// AddDecimals returns the exact decimal sum of base and delta as a string. An
// empty operand is treated as zero so a fresh accumulator (or an absent delta)
// is handled without a special case. It is used to accumulate balance
// quantities (realized P&L) by applying the per-operation delta to the stored
// value rather than overwriting with an absolute. Returns ErrInvalid when
// either operand is a non-empty, non-decimal string.
func AddDecimals(base, delta string) (string, error) {
	baseD := decimal.Zero
	deltaD := decimal.Zero
	var err error
	if base != "" {
		baseD, err = decimal.NewFromString(base)
		if err != nil {
			return "", fmt.Errorf("base %q is not a valid decimal: %w", base, ErrInvalid)
		}
	}
	if delta != "" {
		deltaD, err = decimal.NewFromString(delta)
		if err != nil {
			return "", fmt.Errorf("delta %q is not a valid decimal: %w", delta, ErrInvalid)
		}
	}
	return baseD.Add(deltaD).String(), nil
}

// validatePositiveDecimal returns an error when s is not a positive decimal.
func validatePositiveDecimal(s string) error {
	d, err := decimal.NewFromString(s)
	if err != nil || !d.IsPositive() {
		return fmt.Errorf("%q is not a positive decimal: %w", s, ErrInvalid)
	}
	return nil
}

// Adjustment field formats (asset, amount mode/value, bounds) are validated by
// the engine seam: NewAsset rejects an empty asset, param.NewPositionSizeFromString
// rejects a non-decimal amount or bound, and an unrecognised amount mode is
// rejected there too - all as domain.ErrInvalid. Officer adds no boundary
// pre-validation for them.
