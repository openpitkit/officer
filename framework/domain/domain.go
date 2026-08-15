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

var sentinelErrors []error

func registerSentinel(err error) error {
	sentinelErrors = append(sentinelErrors, err)
	return err
}

// Sentinel errors returned by all layers; callers check with errors.Is.
var (
	ErrNotFound      = registerSentinel(errors.New("not found"))
	ErrAlreadyExists = registerSentinel(errors.New("already exists"))
	ErrInvalid       = registerSentinel(errors.New("invalid"))
	// ErrForbidden marks a request whose caller is known but not authorized for
	// the requested route or command.
	ErrForbidden = registerSentinel(errors.New("forbidden"))
	// ErrTooLarge marks an input that exceeds a hard server-side size limit.
	// HTTP surfaces map it to 413 so callers can split bulk payloads instead of
	// retrying the same request.
	ErrTooLarge = registerSentinel(errors.New("too large"))
	// ErrConflict marks an operation that cannot proceed because the target is
	// already in a terminal or incompatible state. The surface layer maps it to
	// an HTTP 409.
	ErrConflict = registerSentinel(errors.New("conflict"))
	// ErrExecutionReportRequired marks an order whose recorded execution-report
	// activity makes an inferred lifecycle shortcut unsafe. The caller must submit
	// a complete execution report instead.
	ErrExecutionReportRequired = registerSentinel(errors.New(
		"order has execution-report activity; submit an explicit execution report",
	))
	// ErrHasDependents marks a delete that would cascade-delete dependent rows
	// without an explicit force flag. The concrete error carries the blockers.
	ErrHasDependents = registerSentinel(errors.New("has dependents"))
	// ErrAccountMissing marks a request that named an account code Officer does
	// not know and asked to reject rather than create it (see
	// MissingAccountPolicy). It is deliberately not an ErrNotFound: the account
	// is a request parameter the same call could have created, not an addressed
	// resource that turned out to be absent. The concrete error carries the code.
	ErrAccountMissing = registerSentinel(errors.New("account does not exist"))
	// ErrTerminalOrder marks the remaining Officer safety net for terminal
	// orders; callers can bypass it with force and route straight to the engine.
	ErrTerminalOrder = registerSentinel(errors.New("order in terminal status"))
	// ErrNoChange marks a successful engine command that produced no account
	// modifications for Officer to persist.
	ErrNoChange = registerSentinel(errors.New("no change"))
	// ErrNotImplemented marks an operation that needs an engine SDK capability
	// that is not available yet and cannot be represented as a full engine
	// rebuild from persisted state.
	ErrNotImplemented = registerSentinel(errors.New("not implemented"))
	// ErrEngineRestarting marks a short administrative restart window where the
	// live engine is being rebuilt from persisted state. Mutating surfaces reject
	// new requests during that window instead of queuing work behind the restart.
	ErrEngineRestarting = registerSentinel(errors.New("engine restarting"))
	// ErrUpstream marks a failure to reach or get a usable answer from an
	// external provider (e.g. a market-data REST call returning 403/429 or
	// timing out). It is an expected operational condition, not a server bug, so
	// handlers surface it as a 502 with a plain message instead of a 500
	// "unhandled internal error".
	ErrUpstream = registerSentinel(errors.New("upstream provider error"))
)

// IsSentinel reports whether err is one of the domain sentinel values.
// It compares identity and does not inspect wrapped causes.
func IsSentinel(err error) bool {
	for _, sentinel := range sentinelErrors {
		if err == sentinel {
			return true
		}
	}
	return false
}

// DependentCount names one dependent row kind that blocks a destructive delete.
type DependentCount struct {
	Kind  string
	Count int
}

// HasDependentsError carries the blockers for a destructive delete.
type HasDependentsError struct {
	Dependents []DependentCount
}

// CurrencyChangeBlockedError carries the target whose currency cannot change
// while its current economic state is non-empty.
type CurrencyChangeBlockedError struct {
	Scope    string
	TargetID string
	Cause    error
}

// Error returns the guard's human-readable explanation.
func (e CurrencyChangeBlockedError) Error() string { return e.Cause.Error() }

// Unwrap preserves the generic conflict contract for non-HTTP callers.
func (e CurrencyChangeBlockedError) Unwrap() error { return e.Cause }

// NewCurrencyChangeBlockedError creates a structured currency guard error.
func NewCurrencyChangeBlockedError(scope, targetID string, cause error) error {
	return CurrencyChangeBlockedError{
		Scope:    scope,
		TargetID: targetID,
		Cause:    cause,
	}
}

// Error returns the stable delete-policy error text.
func (e HasDependentsError) Error() string { return ErrHasDependents.Error() }

// Unwrap lets callers match the typed error with errors.Is.
func (e HasDependentsError) Unwrap() error { return ErrHasDependents }

// NewHasDependentsError creates a typed delete-policy error.
func NewHasDependentsError(dependents []DependentCount) error {
	return HasDependentsError{Dependents: dependents}
}

// AccountMissingError carries the account code a rejecting request named, so a
// surface can report it as a structured field instead of parsing the message.
type AccountMissingError struct {
	Account AccountID
}

// Error names the account the request could not resolve.
func (e AccountMissingError) Error() string {
	return fmt.Sprintf("account %q does not exist", e.Account)
}

// Unwrap lets callers match the typed error with errors.Is.
func (e AccountMissingError) Unwrap() error { return ErrAccountMissing }

// NewAccountMissingError creates a typed missing-account error.
func NewAccountMissingError(account AccountID) error {
	return AccountMissingError{Account: account}
}

// MissingAccountPolicy is the caller's explicit choice for a request that names
// an account code Officer does not know yet. Officer can register the account on
// the spot, which is what an integration bootstrapping itself wants, or refuse
// the request, which is what an operator guarding against a typo wants. Neither
// is safe as a silent default, so every mutation that supports on-demand
// account registration makes the choice explicit.
type MissingAccountPolicy string

const (
	// MissingAccountCreate registers the named account with default settings (no
	// group, no currency) before the request is applied.
	MissingAccountCreate MissingAccountPolicy = "create"
	// MissingAccountReject fails the request with ErrAccountMissing when the
	// named account does not exist.
	MissingAccountReject MissingAccountPolicy = "reject"
)

// ValidateMissingAccountPolicy returns an error wrapping ErrInvalid when policy
// is empty or is not a recognised MissingAccountPolicy value.
func ValidateMissingAccountPolicy(policy MissingAccountPolicy) error {
	switch policy {
	case MissingAccountCreate, MissingAccountReject:
		return nil
	case "":
		return fmt.Errorf("missingAccount is required: %w", ErrInvalid)
	default:
		return fmt.Errorf(
			"missingAccount %q must be %q or %q: %w",
			string(policy), MissingAccountCreate, MissingAccountReject, ErrInvalid,
		)
	}
}

// ParseMissingAccountPolicy validates a wire value of the missingAccount request
// parameter and returns the typed policy.
func ParseMissingAccountPolicy(value string) (MissingAccountPolicy, error) {
	policy := MissingAccountPolicy(value)
	if err := ValidateMissingAccountPolicy(policy); err != nil {
		return "", err
	}
	return policy, nil
}

// ValidateDropCopyMissingAccountPolicy enforces the drop-copy exception: a
// report describes an execution that already happened, so an unknown account
// must be registered rather than used to reject the report.
func ValidateDropCopyMissingAccountPolicy(policy MissingAccountPolicy) error {
	if err := ValidateMissingAccountPolicy(policy); err != nil {
		return err
	}
	if policy != MissingAccountCreate {
		return fmt.Errorf(
			"drop-copy reports an execution that already happened and cannot reject a missing account; use missingAccount=%s: %w",
			MissingAccountCreate, ErrInvalid,
		)
	}
	return nil
}

// Policy identifiers.
const (
	PolicyRateLimit                    = "rate_limit"
	PolicyOrderSizeLimit               = "order_size_limit"
	PolicySpotFundsPnlBoundsKillSwitch = "spot_funds_pnl_bounds_kill_switch"
)

// Scope identifiers.
const (
	ScopeBroker       = "broker"
	ScopeGlobal       = "global"
	ScopeAsset        = "asset"
	ScopeAccount      = "account"
	ScopeAccountGroup = "account_group"
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
	// Pnl is the latest SpotFunds account-currency P&L snapshot. It is empty
	// whenever the account has no P&L value: while PnlHaltReason is set, because
	// a halted accumulator has no numeric value, and on an account the engine has
	// not reported on yet. Empty is the absence of a value, never zero. Its
	// currency is EffectiveCurrency and is deliberately not repeated here.
	Pnl string
	// PnlHaltReason explains why the engine could not calculate account P&L on
	// its latest reported operation. Empty means the latest P&L is authoritative.
	PnlHaltReason PnlHaltReason
	// Currency is the account-level realized P&L currency asset code. Empty
	// means the account inherits from its group, then from the reserved default
	// group, and finally disables realized P&L tracking when all tiers are empty.
	Currency string
	// EffectiveCurrency is the displayed currency after resolving the account,
	// account-group, and reserved default-group tiers. Empty means no tier is set.
	EffectiveCurrency string
	// CurrencyOrigin names the tier that supplied EffectiveCurrency: "account",
	// "group", "default", or empty when no tier is set.
	CurrencyOrigin string
	// GroupCurrency is the currency set on GroupCode, when any.
	GroupCurrency string
	// DefaultCurrency is the currency set on the reserved default group, when any.
	DefaultCurrency string
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

const (
	// CurrencyOriginAccount means the account tier supplied the effective currency.
	CurrencyOriginAccount = "account"
	// CurrencyOriginGroup means the account group tier supplied the effective
	// currency.
	CurrencyOriginGroup = "group"
	// CurrencyOriginDefault means the reserved default group supplied the
	// effective currency.
	CurrencyOriginDefault = "default"
)

// ResolveCurrencyCascade resolves the three account-currency tiers.
func ResolveCurrencyCascade(
	accountCurrency string,
	groupCurrency string,
	defaultCurrency string,
) (string, string) {
	switch {
	case accountCurrency != "":
		return accountCurrency, CurrencyOriginAccount
	case groupCurrency != "":
		return groupCurrency, CurrencyOriginGroup
	case defaultCurrency != "":
		return defaultCurrency, CurrencyOriginDefault
	default:
		return "", ""
	}
}

// AuditAction classifies a control-plane action recorded in the audit trail.
type AuditAction string

const (
	AuditActionHydrate            AuditAction = "hydrate"
	AuditActionCreateAsset        AuditAction = "create_asset"
	AuditActionUpdateAsset        AuditAction = "update_asset"
	AuditActionDeleteAsset        AuditAction = "delete_asset"
	AuditActionCreateAccount      AuditAction = "create_account"
	AuditActionUpdateAccount      AuditAction = "update_account"
	AuditActionDeleteAccount      AuditAction = "delete_account"
	AuditActionBlock              AuditAction = "block"
	AuditActionUnblock            AuditAction = "unblock"
	AuditActionSetLimit           AuditAction = "set_limit"
	AuditActionDeleteLimit        AuditAction = "delete_limit"
	AuditActionSetGroupNotes      AuditAction = "set_group_notes"
	AuditActionBlockGroup         AuditAction = "block_group"
	AuditActionUnblockGroup       AuditAction = "unblock_group"
	AuditActionSetNotes           AuditAction = "set_notes"
	AuditActionSetGroup           AuditAction = "set_group"
	AuditActionSetAccountCurrency AuditAction = "set_account_currency"
	AuditActionSetGroupCurrency   AuditAction = "set_group_currency"
	AuditActionAdjustment         AuditAction = "adjustment"
	AuditActionCreateGroup        AuditAction = "create_group"
	AuditActionUpdateGroup        AuditAction = "update_group"
	AuditActionDeleteGroup        AuditAction = "delete_group"
	AuditActionCreateAssetClass   AuditAction = "create_asset_class"
	AuditActionUpdateAssetClass   AuditAction = "update_asset_class"
	AuditActionDeleteAssetClass   AuditAction = "delete_asset_class"
	AuditActionSubmitOrder        AuditAction = "submit_order"
	AuditActionSubmitDropCopy     AuditAction = "submit_drop_copy_order"
	AuditActionExecutionReport    AuditAction = "execution_report"
	AuditActionSetMcpAccess       AuditAction = "set_mcp_access"
	AuditActionSetMarketData      AuditAction = "set_market_data"
	AuditActionExportBackup       AuditAction = "export_backup"
	AuditActionRestoreBackup      AuditAction = "restore_backup"
	AuditActionExportBusinessCSV  AuditAction = "export_business_csv"
	AuditActionResetDatabase      AuditAction = "reset_database"
	AuditActionRestartService     AuditAction = "restart_service"
	AuditActionStopService        AuditAction = "stop_service"

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
	case AuditActionSubmitOrder, AuditActionSubmitDropCopy, AuditActionExecutionReport:
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
		AuditActionSetAccountCurrency,
		AuditActionSetGroupCurrency,
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
		AuditActionResetDatabase,
		AuditActionRestartService,
		AuditActionStopService,
		AuditActionGenerateSigningKey,
		AuditActionImportSigningKey,
		AuditActionSetSigningConfig,
		AuditActionApprovalIssued,
		AuditActionApprovalFailed,
		AuditActionApprovalConfirmed,
		AuditActionApprovalCancelled,
		AuditActionSubmitOrder,
		AuditActionSubmitDropCopy,
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
	// Group is the code of the account group the action targeted; empty when
	// none. It is a structured snapshot so a reader selects a group's rows by
	// identity instead of matching the free-form detail text.
	Group string
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
// On pass WouldLockPrice holds the settlement price the engine would lock at reservation
// time; on reject Rejects holds the structured engine rejects and WouldBlock,
// when non-nil, is the account block the engine would record.
type CheckResult struct {
	// WouldBlock is the account block the engine would record; nil when none.
	WouldBlock *ExecutionAccountBlock
	// Rejects are the engine pre-trade rejects; empty when the check passed.
	Rejects []OrderReject
	// WouldLockPrice is the settlement lock price (exact decimal string); empty
	// when nothing would be locked.
	WouldLockPrice string
	// Passed reports whether the order would pass pre-trade.
	Passed bool
}

// ValidateAccountID returns an error wrapping ErrInvalid when id is not a
// well-formed account identifier: non-empty, not a relative path segment, at
// most 64 code points, valid UTF-8, printable, no leading or trailing
// whitespace.
func ValidateAccountID(id AccountID) error {
	s := string(id)
	if s == "" {
		return fmt.Errorf("account id is empty: %w", ErrInvalid)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("account id contains invalid UTF-8: %w", ErrInvalid)
	}
	if isPathDotSegment(s) {
		return fmt.Errorf("account id %q is a reserved path segment: %w", s, ErrInvalid)
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
// well-formed asset identifier (non-empty, not a relative path segment,
// valid UTF-8, printable, no whitespace, at most 32 chars). It is the boundary
// format check the spot-funds and trading surfaces apply to an asset id; it
// never checks existence.
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
// non-empty, not a relative path segment, valid UTF-8, no whitespace,
// printable, at most 32 chars. The asset code is the one dictionary string that
// crosses the FFI verbatim, so a control character must not survive the
// boundary.
func validateAsset(asset string) error {
	if asset == "" {
		return fmt.Errorf("asset is empty: %w", ErrInvalid)
	}
	if !utf8.ValidString(asset) {
		return fmt.Errorf("asset contains invalid UTF-8: %w", ErrInvalid)
	}
	if isPathDotSegment(asset) {
		return fmt.Errorf("asset %q is a reserved path segment: %w", asset, ErrInvalid)
	}
	if len(asset) > 32 {
		return fmt.Errorf("asset exceeds 32 chars: %w", ErrInvalid)
	}
	for _, r := range asset {
		if unicode.IsSpace(r) {
			return fmt.Errorf("asset contains whitespace: %w", ErrInvalid)
		}
		if !unicode.IsPrint(r) {
			return fmt.Errorf("asset contains non-printable character: %w", ErrInvalid)
		}
	}
	return nil
}

// AddDecimals returns the exact decimal sum of base and delta as a string. An
// empty operand is treated as zero. It is used for exact decimal validation and
// normalization at API/import boundaries. Returns ErrInvalid when either
// operand is a non-empty, non-decimal string.
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
