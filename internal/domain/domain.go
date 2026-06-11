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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/shopspring/decimal"
)

// DefaultTenant is the implicit tenant used in single-process deployments.
const DefaultTenant TenantID = "default"

// Sentinel errors returned by all layers; callers check with errors.Is.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrInvalid       = errors.New("invalid")
	// ErrNotImplemented marks an operation that needs an engine SDK capability
	// that is not available yet. The engine is built once at process start and
	// never rebuilt, so a change the runtime configure surface cannot express
	// (for example adding or removing a barrier where only retuning is
	// supported) wraps this sentinel instead of silently rebuilding.
	ErrNotImplemented = errors.New("not implemented")
)

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
)

// AccountID identifies a single trading account within a tenant.
type AccountID string

// String returns the raw account identifier.
func (id AccountID) String() string { return string(id) }

// TenantID identifies an isolation boundary that owns accounts.
type TenantID string

// String returns the raw tenant identifier.
func (id TenantID) String() string { return string(id) }

// Account is the control-plane view of an engine account.
type Account struct {
	// Tenant is the isolation boundary that owns the account.
	Tenant TenantID
	// ID is the account identifier, unique within Tenant.
	ID AccountID
	// GroupID is the operator-facing group identifier; empty means no group.
	// This is NOT the engine's group hash — it is the human-readable id
	// stored in AccountGroup.ID and resolved to a hash by the node layer.
	GroupID string
	// Notes is a free-form reference string. Never forwarded to the engine.
	// Max 4096 bytes; validated by ValidateNotes.
	Notes string
	// BlockReason is the human-readable reason the account was blocked.
	// Empty when the account is not blocked.
	BlockReason string
	// Blocked reports whether the account is currently kill-switched.
	Blocked bool
}

// LimitTarget identifies the policy+scope+account+asset combination that a
// single risk barrier applies to. It is the composite key for the limits table.
type LimitTarget struct {
	Tenant  TenantID
	Policy  string
	Scope   string
	Account AccountID // empty unless scope has the account axis
	Asset   string    // empty unless scope has the asset axis
}

// LimitValue is a single kind=value pair within a barrier.
type LimitValue struct {
	Kind  string
	Value string
}

// Limit is the control-plane view of one risk barrier: the full set of kinds
// for one LimitTarget. Values is always sorted by Kind.
type Limit struct {
	Target LimitTarget
	Values []LimitValue
}

// AuditAction classifies a control-plane action recorded in the audit trail.
type AuditAction string

const (
	AuditActionHydrate         AuditAction = "hydrate"
	AuditActionCreateAccount   AuditAction = "create_account"
	AuditActionBlock           AuditAction = "block"
	AuditActionUnblock         AuditAction = "unblock"
	AuditActionSetLimit        AuditAction = "set_limit"
	AuditActionDeleteLimit     AuditAction = "delete_limit"
	AuditActionSetGroupNotes   AuditAction = "set_group_notes"
	AuditActionBlockGroup      AuditAction = "block_group"
	AuditActionUnblockGroup    AuditAction = "unblock_group"
	AuditActionSetNotes        AuditAction = "set_notes"
	AuditActionSetGroup        AuditAction = "set_group"
	AuditActionAdjustment      AuditAction = "adjustment"
	AuditActionCreateGroup     AuditAction = "create_group"
	AuditActionDeleteGroup     AuditAction = "delete_group"
	AuditActionSubmitOrder     AuditAction = "submit_order"
	AuditActionExecutionReport AuditAction = "execution_report"
)

// AuditRow is the persisted, immutable record of a single control-plane action.
type AuditRow struct {
	// At is the wall-clock time the action was recorded, in UTC.
	At time.Time
	// Actor identifies who initiated the action (the Principal).
	Actor string
	// Action is the category of the recorded action.
	Action AuditAction
	// Tenant is the isolation boundary the action targeted, if any.
	Tenant TenantID
	// Account is the account the action targeted, if any.
	Account AccountID
	// Detail is a short human-readable description of the action.
	Detail string
	// Source is the channel through which the action was initiated.
	Source Source
	// ID is the store-assigned monotonically increasing row identifier.
	ID int64
}

// OrderProbe carries the parameters for a non-mutating order check.
// Provisional: SDK has no non-mutating dry-run yet.
type OrderProbe struct {
	Account    AccountID
	Instrument string
	Side       string
	Quantity   string
	Price      string // optional; empty means market/unpriced
}

// CheckResult is the outcome of a non-mutating order check.
// Provisional: SDK has no non-mutating dry-run yet.
type CheckResult struct {
	Passed  bool
	Rejects []string
}

// ValidateAccountID returns an error wrapping ErrInvalid when id is not a
// well-formed account identifier: non-empty, at most 64 chars, printable
// ASCII, no leading or trailing whitespace.
func ValidateAccountID(id AccountID) error {
	s := string(id)
	if s == "" {
		return fmt.Errorf("account id is empty: %w", ErrInvalid)
	}
	if len(s) > 64 {
		return fmt.Errorf("account id exceeds 64 chars: %w", ErrInvalid)
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

// ValidateLimit enforces the policy/scope/kind vocabulary and value rules.
func ValidateLimit(l Limit) error {
	t := l.Target
	switch t.Policy {
	case PolicyRateLimit, PolicyOrderSizeLimit, PolicyPnlBoundsKillSwitch:
	default:
		return fmt.Errorf("unknown policy %q: %w", t.Policy, ErrInvalid)
	}

	if err := validateScope(t.Policy, t.Scope); err != nil {
		return err
	}
	if err := validateScopeAxes(t); err != nil {
		return err
	}
	if len(l.Values) == 0 {
		return fmt.Errorf("limit has no values: %w", ErrInvalid)
	}
	return validateValues(t.Policy, l.Values)
}

// validateScope checks the policy/scope combination is allowed.
func validateScope(policy, scope string) error {
	allowed := allowedScopes[policy]
	for _, s := range allowed {
		if s == scope {
			return nil
		}
	}
	return fmt.Errorf("scope %q not allowed for policy %q: %w", scope, policy, ErrInvalid)
}

// allowedScopes maps each policy to its permitted scopes.
var allowedScopes = map[string][]string{
	PolicyRateLimit:           {ScopeBroker, ScopeAsset, ScopeAccount, ScopeAccountAsset},
	PolicyOrderSizeLimit:      {ScopeBroker, ScopeAsset, ScopeAccountAsset},
	PolicyPnlBoundsKillSwitch: {ScopeAsset, ScopeAccountAsset},
}

// validateScopeAxes checks that Account and Asset fields are present/absent
// consistently with the scope definition.
func validateScopeAxes(t LimitTarget) error {
	needsAccount := t.Scope == ScopeAccount || t.Scope == ScopeAccountAsset
	needsAsset := t.Scope == ScopeAsset || t.Scope == ScopeAccountAsset

	if needsAccount && t.Account == "" {
		return fmt.Errorf("scope %q requires account: %w", t.Scope, ErrInvalid)
	}
	if !needsAccount && t.Account != "" {
		return fmt.Errorf("scope %q must not have account: %w", t.Scope, ErrInvalid)
	}
	if needsAsset {
		if err := validateAsset(t.Asset); err != nil {
			return err
		}
	} else if t.Asset != "" {
		return fmt.Errorf("scope %q must not have asset: %w", t.Scope, ErrInvalid)
	}
	if needsAccount {
		if err := ValidateAccountID(t.Account); err != nil {
			return err
		}
	}
	return nil
}

// validateValues enforces the kind+value rules per policy.
func validateValues(policy string, vals []LimitValue) error {
	kindSet := make(map[string]string, len(vals))
	for _, v := range vals {
		if _, dup := kindSet[v.Kind]; dup {
			return fmt.Errorf("duplicate kind %q: %w", v.Kind, ErrInvalid)
		}
		kindSet[v.Kind] = v.Value
	}

	switch policy {
	case PolicyRateLimit:
		return validateRateLimit(kindSet)
	case PolicyOrderSizeLimit:
		return validateOrderSizeLimit(kindSet)
	case PolicyPnlBoundsKillSwitch:
		return validatePnlBounds(kindSet)
	}
	return nil
}

func validateRateLimit(kinds map[string]string) error {
	for k := range kinds {
		if k != KindMaxOrders && k != KindWindow {
			return fmt.Errorf("unknown kind %q for rate_limit: %w", k, ErrInvalid)
		}
	}
	maxOrders, hasMax := kinds[KindMaxOrders]
	window, hasWindow := kinds[KindWindow]
	if !hasMax || !hasWindow {
		return fmt.Errorf("rate_limit requires both max_orders and window: %w", ErrInvalid)
	}
	// Require a canonical decimal-digit string: the engine parses max_orders
	// with strconv.ParseUint, which rejects exponents and decimal points
	// ("1e9", "100.0"). Validate the same acceptance set here so a contract-
	// valid value is never rejected later at engine apply time.
	n, err := strconv.ParseUint(maxOrders, 10, 64)
	if err != nil || n == 0 || n > 1_000_000_000 {
		return fmt.Errorf("max_orders must be an integer > 0 and <= 1e9: %w", ErrInvalid)
	}
	if err := validateGoDuration(window); err != nil {
		return fmt.Errorf("window: %w", err)
	}
	return nil
}

func validateOrderSizeLimit(kinds map[string]string) error {
	for k := range kinds {
		if k != KindMaxQuantity && k != KindMaxNotional {
			return fmt.Errorf("unknown kind %q for order_size_limit: %w", k, ErrInvalid)
		}
	}
	_, hasQty := kinds[KindMaxQuantity]
	_, hasNot := kinds[KindMaxNotional]
	if !hasQty && !hasNot {
		return fmt.Errorf(
			"order_size_limit requires at least max_quantity or max_notional: %w",
			ErrInvalid,
		)
	}
	if v, ok := kinds[KindMaxQuantity]; ok {
		if err := validatePositiveDecimal(v); err != nil {
			return fmt.Errorf("max_quantity: %w", err)
		}
	}
	if v, ok := kinds[KindMaxNotional]; ok {
		if err := validatePositiveDecimal(v); err != nil {
			return fmt.Errorf("max_notional: %w", err)
		}
	}
	return nil
}

func validatePnlBounds(kinds map[string]string) error {
	for k := range kinds {
		if k != KindLowerBound && k != KindUpperBound {
			return fmt.Errorf("unknown kind %q for pnl_bounds_kill_switch: %w", k, ErrInvalid)
		}
	}
	lowerStr, hasLower := kinds[KindLowerBound]
	upperStr, hasUpper := kinds[KindUpperBound]
	if !hasLower && !hasUpper {
		return fmt.Errorf(
			"pnl_bounds_kill_switch requires at least lower_bound or upper_bound: %w",
			ErrInvalid,
		)
	}
	var lowerD, upperD decimal.Decimal
	var err error
	if hasLower {
		lowerD, err = decimal.NewFromString(lowerStr)
		if err != nil {
			return fmt.Errorf("lower_bound is not a valid decimal: %w", ErrInvalid)
		}
	}
	if hasUpper {
		upperD, err = decimal.NewFromString(upperStr)
		if err != nil {
			return fmt.Errorf("upper_bound is not a valid decimal: %w", ErrInvalid)
		}
	}
	if hasLower && hasUpper && lowerD.GreaterThan(upperD) {
		return fmt.Errorf("lower_bound must be <= upper_bound: %w", ErrInvalid)
	}
	return nil
}

// validatePositiveDecimal returns an error when s is not a positive decimal.
func validatePositiveDecimal(s string) error {
	d, err := decimal.NewFromString(s)
	if err != nil || !d.IsPositive() {
		return fmt.Errorf("%q is not a positive decimal: %w", s, ErrInvalid)
	}
	return nil
}

// ValidateAdjustmentRequest returns an error wrapping ErrInvalid when req is
// not well-formed at the boundary. It checks:
//   - Asset is present and passes ValidateAsset.
//   - Each present amount (Balance/Held/Incoming) has a recognised Mode
//     (absolute or delta) and a non-empty decimal Value.
//   - Each present bound's Lower/Upper, when non-empty, is a valid decimal.
//
// Business rules (whether the resulting balance is legal, etc.) remain the
// engine's responsibility.
func ValidateAdjustmentRequest(req AdjustmentRequest) error {
	if err := validateAsset(req.Asset); err != nil {
		return err
	}
	for _, amt := range []*AdjustmentAmount{req.Balance, req.Held, req.Incoming} {
		if amt == nil {
			continue
		}
		switch amt.Mode {
		case AdjustmentModeAbsolute, AdjustmentModeDelta:
		default:
			return fmt.Errorf("adjustment amount mode %q is not valid (want absolute or delta): %w", amt.Mode, ErrInvalid)
		}
		if !isDecimalString(amt.Value) {
			return fmt.Errorf("adjustment amount value %q is not a valid decimal: %w", amt.Value, ErrInvalid)
		}
	}
	for _, b := range []*AdjustmentBounds{req.BalanceBounds, req.HeldBounds, req.IncomingBounds} {
		if b == nil {
			continue
		}
		if b.Lower != "" && !isDecimalString(b.Lower) {
			return fmt.Errorf("adjustment bounds lower %q is not a valid decimal: %w", b.Lower, ErrInvalid)
		}
		if b.Upper != "" && !isDecimalString(b.Upper) {
			return fmt.Errorf("adjustment bounds upper %q is not a valid decimal: %w", b.Upper, ErrInvalid)
		}
	}
	return nil
}

// isDecimalString reports whether s is a syntactically valid decimal number:
// an optional leading '-', one or more digits, and at most one '.'. It never
// allocates and adds no dependencies; it is used for boundary pre-validation
// only (the engine performs the authoritative parse).
func isDecimalString(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[i] == '-' {
		i++
	}
	if i == len(s) {
		return false
	}
	hasDot := false
	hasDigit := false
	for ; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			hasDigit = true
			continue
		}
		if c == '.' && !hasDot {
			hasDot = true
			continue
		}
		return false
	}
	return hasDigit
}

// validateGoDuration returns an error when s is not a valid Go duration string
// with value > 0 and <= 24h.
func validateGoDuration(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a valid Go duration: %w", s, ErrInvalid)
	}
	if d <= 0 {
		return fmt.Errorf("duration must be > 0: %w", ErrInvalid)
	}
	if d > 24*time.Hour {
		return fmt.Errorf("duration must be <= 24h: %w", ErrInvalid)
	}
	return nil
}

// SortLimitValues sorts a slice of LimitValue in place by Kind.
func SortLimitValues(vals []LimitValue) {
	sort.Slice(vals, func(i, j int) bool { return vals[i].Kind < vals[j].Kind })
}

