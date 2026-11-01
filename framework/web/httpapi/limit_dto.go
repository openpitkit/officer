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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package httpapi

import (
	"context"
	"fmt"
	"math"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

// The DTOs below are the JSON wire shapes of REST's typed limit barriers, with
// their mapping onto the domain, the read-back of a stored barrier, the answer
// of a put and the per-policy list of barriers. They are shared here so a
// second surface can take and answer the same body.

// RateLimitDTO is the wire shape of a rate-limit barrier.
type RateLimitDTO struct {
	Scope     string `json:"scope"`
	Account   string `json:"account"`
	Asset     string `json:"asset"`
	WindowMs  int64  `json:"windowMs"`
	MaxOrders uint64 `json:"maxOrders"`
}

// OrderSizeLimitDTO is the wire shape of an order-size barrier. The ceilings are
// exact decimal strings; an unset ceiling is the empty string.
type OrderSizeLimitDTO struct {
	Scope       string `json:"scope"`
	Account     string `json:"account"`
	Asset       string `json:"asset"`
	MaxQuantity string `json:"maxQuantity"`
	MaxNotional string `json:"maxNotional"`
}

// SpotFundsPnlBoundsLimitDTO is the wire shape of a currency-valued SpotFunds
// self-computed P&L-bounds barrier.
type SpotFundsPnlBoundsLimitDTO struct {
	Scope        string `json:"scope"`
	Account      string `json:"account"`
	AccountGroup string `json:"accountGroup"`
	Currency     string `json:"currency"`
	LowerBound   string `json:"lowerBound"`
	UpperBound   string `json:"upperBound"`
}

// SpotFundsLimitModeDTO is the global Spot Funds mode, whether its meta-policy
// row is stored, and the visible accounts without an effective currency
// affected by a P&L bound. AccountsWithoutCurrency is always a non-nil JSON
// array.
type SpotFundsLimitModeDTO struct {
	Mode                    string   `json:"mode"`
	Stored                  bool     `json:"stored"`
	AccountsWithoutCurrency []string `json:"accountsWithoutCurrency"`
}

// SpotFundsLimitModeLimitDTO is the wire shape of the Spot Funds limit-mode
// meta-policy: its scope, global only, and its mode.
type SpotFundsLimitModeLimitDTO struct {
	Scope string `json:"scope"`
	Mode  string `json:"mode"`
}

// ToSpotFundsLimitModeDTO maps the domain mode state onto its wire shape.
func ToSpotFundsLimitModeDTO(state domain.SpotFundsLimitModeState) SpotFundsLimitModeDTO {
	accounts := make([]string, 0, len(state.AccountsWithoutCurrency))
	for _, code := range state.AccountsWithoutCurrency {
		accounts = append(accounts, string(code))
	}
	return SpotFundsLimitModeDTO{
		Mode: string(state.Mode), Stored: state.Stored, AccountsWithoutCurrency: accounts,
	}
}

// PutLimitResultDTO is the answer of a limit put: the barrier as stored, under
// the key of its kind. Exactly one field is set.
type PutLimitResultDTO struct {
	RateLimit                         *RateLimitDTO               `json:"rateLimit,omitempty"`
	OrderSizeLimit                    *OrderSizeLimitDTO          `json:"orderSizeLimit,omitempty"`
	SpotFundsPnlBoundsLimit           *SpotFundsPnlBoundsLimitDTO `json:"spotFundsPnlBoundsLimit,omitempty"`
	SpotFundsUnrealizedPnlBoundsLimit *SpotFundsPnlBoundsLimitDTO `json:"spotFundsUnrealizedPnlBoundsLimit,omitempty"`
	SpotFundsTotalPnlBoundsLimit      *SpotFundsPnlBoundsLimitDTO `json:"spotFundsTotalPnlBoundsLimit,omitempty"`
	SpotFundsLimitMode                *SpotFundsLimitModeLimitDTO `json:"spotFundsLimitMode,omitempty"`
}

// AccountLimitsDTO is the per-policy view of typed barriers, an account's or
// every account's. Each slice is always a non-nil JSON array. The Spot Funds
// P&L bounds are listed per axis, each under the key of its own policy.
type AccountLimitsDTO struct {
	RateLimits                         []RateLimitDTO               `json:"rateLimits"`
	OrderSizeLimits                    []OrderSizeLimitDTO          `json:"orderSizeLimits"`
	SpotFundsPnlBoundsLimits           []SpotFundsPnlBoundsLimitDTO `json:"spotFundsPnlBoundsLimits"`
	SpotFundsUnrealizedPnlBoundsLimits []SpotFundsPnlBoundsLimitDTO `json:"spotFundsUnrealizedPnlBoundsLimits"`
	SpotFundsTotalPnlBoundsLimits      []SpotFundsPnlBoundsLimitDTO `json:"spotFundsTotalPnlBoundsLimits"`
	SpotFundsLimitModes                []SpotFundsLimitModeLimitDTO `json:"spotFundsLimitModes"`
}

// FromRateLimitDTO maps a wire rate-limit body onto the domain barrier. A
// windowMs whose window in nanoseconds does not fit time.Duration is a
// validation error on /windowMs: converted, it would wrap into a window nobody
// sent. The window's own range is the domain's to check.
func FromRateLimitDTO(dto RateLimitDTO) (domain.LimitRate, error) {
	if dto.WindowMs > math.MaxInt64/int64(time.Millisecond) ||
		dto.WindowMs < math.MinInt64/int64(time.Millisecond) {
		return domain.LimitRate{}, domain.NewValidationError(
			"/windowMs", "range", "window is outside the representable duration range",
		)
	}
	return domain.LimitRate{
		Scope:     domain.LimitScope(dto.Scope),
		Account:   domain.AccountID(dto.Account),
		Asset:     dto.Asset,
		Window:    time.Duration(dto.WindowMs) * time.Millisecond,
		MaxOrders: dto.MaxOrders,
	}, nil
}

// FromOrderSizeLimitDTO maps a wire order-size body onto the domain barrier.
// Decimal values are carried through verbatim; never parsed to float.
func FromOrderSizeLimitDTO(dto OrderSizeLimitDTO) domain.LimitOrderSize {
	return domain.LimitOrderSize{
		Scope:       domain.LimitScope(dto.Scope),
		Account:     domain.AccountID(dto.Account),
		Asset:       dto.Asset,
		MaxQuantity: dto.MaxQuantity,
		MaxNotional: dto.MaxNotional,
	}
}

// FromSpotFundsPnlBoundsLimitDTO maps a wire SpotFunds P&L-bounds body onto
// the domain barrier of axis, which the route or policy names and the body
// does not carry. Decimal values are carried through verbatim; never parsed to
// float.
func FromSpotFundsPnlBoundsLimitDTO(
	axis domain.SpotFundsPnlAxis,
	dto SpotFundsPnlBoundsLimitDTO,
) domain.LimitSpotFundsPnlBounds {
	return domain.LimitSpotFundsPnlBounds{
		Axis:         axis,
		Scope:        domain.LimitScope(dto.Scope),
		Account:      domain.AccountID(dto.Account),
		AccountGroup: dto.AccountGroup,
		Currency:     dto.Currency,
		LowerBound:   dto.LowerBound,
		UpperBound:   dto.UpperBound,
	}
}

// FromSpotFundsLimitModeLimitDTO maps a wire Spot Funds limit-mode body onto
// the domain meta-policy, verbatim; the domain validates it.
func FromSpotFundsLimitModeLimitDTO(
	dto SpotFundsLimitModeLimitDTO,
) domain.LimitSpotFundsLimitMode {
	return domain.LimitSpotFundsLimitMode{
		Scope: domain.LimitScope(dto.Scope),
		Mode:  domain.SpotFundsLimitMode(dto.Mode),
	}
}

// ToSpotFundsLimitModeLimitDTO maps a domain.LimitSpotFundsLimitMode onto the
// wire DTO.
func ToSpotFundsLimitModeLimitDTO(
	l domain.LimitSpotFundsLimitMode,
) SpotFundsLimitModeLimitDTO {
	return SpotFundsLimitModeLimitDTO{Scope: string(l.Scope), Mode: string(l.Mode)}
}

// ToRateLimitDTO maps a domain.LimitRate onto the wire DTO.
func ToRateLimitDTO(l domain.LimitRate) RateLimitDTO {
	return RateLimitDTO{
		Scope:     string(l.Scope),
		Account:   string(l.Account),
		Asset:     l.Asset,
		WindowMs:  l.Window.Milliseconds(),
		MaxOrders: l.MaxOrders,
	}
}

// ToOrderSizeLimitDTO maps a domain.LimitOrderSize onto the wire DTO.
func ToOrderSizeLimitDTO(l domain.LimitOrderSize) OrderSizeLimitDTO {
	return OrderSizeLimitDTO{
		Scope:       string(l.Scope),
		Account:     string(l.Account),
		Asset:       l.Asset,
		MaxQuantity: l.MaxQuantity,
		MaxNotional: l.MaxNotional,
	}
}

// ToSpotFundsPnlBoundsLimitDTO maps a domain.LimitSpotFundsPnlBounds onto the
// wire DTO.
func ToSpotFundsPnlBoundsLimitDTO(
	l domain.LimitSpotFundsPnlBounds,
) SpotFundsPnlBoundsLimitDTO {
	return SpotFundsPnlBoundsLimitDTO{
		Scope:        string(l.Scope),
		Account:      string(l.Account),
		AccountGroup: l.AccountGroup,
		Currency:     l.Currency,
		LowerBound:   l.LowerBound,
		UpperBound:   l.UpperBound,
	}
}

// ToAccountLimitsDTO maps the per-policy typed barriers onto the wire DTO.
// Each slice is a non-nil JSON array.
func ToAccountLimitsDTO(limits node.AccountLimits) AccountLimitsDTO {
	rates := make([]RateLimitDTO, 0, len(limits.RateLimits))
	for _, l := range limits.RateLimits {
		rates = append(rates, ToRateLimitDTO(l))
	}
	sizes := make([]OrderSizeLimitDTO, 0, len(limits.OrderSizeLimits))
	for _, l := range limits.OrderSizeLimits {
		sizes = append(sizes, ToOrderSizeLimitDTO(l))
	}
	spotFundsPnls := map[domain.SpotFundsPnlAxis][]SpotFundsPnlBoundsLimitDTO{}
	for _, axis := range domain.SpotFundsPnlAxes {
		spotFundsPnls[axis] = []SpotFundsPnlBoundsLimitDTO{}
	}
	for _, l := range limits.SpotFundsPnlBoundsLimits {
		spotFundsPnls[l.Axis] = append(spotFundsPnls[l.Axis], ToSpotFundsPnlBoundsLimitDTO(l))
	}
	modes := make([]SpotFundsLimitModeLimitDTO, 0, len(limits.SpotFundsLimitModes))
	for _, l := range limits.SpotFundsLimitModes {
		modes = append(modes, ToSpotFundsLimitModeLimitDTO(l))
	}
	return AccountLimitsDTO{
		RateLimits:                         rates,
		OrderSizeLimits:                    sizes,
		SpotFundsPnlBoundsLimits:           spotFundsPnls[domain.SpotFundsPnlAxisRealized],
		SpotFundsUnrealizedPnlBoundsLimits: spotFundsPnls[domain.SpotFundsPnlAxisUnrealized],
		SpotFundsTotalPnlBoundsLimits:      spotFundsPnls[domain.SpotFundsPnlAxisTotal],
		SpotFundsLimitModes:                modes,
	}
}

// ToPutSpotFundsPnlBoundsLimitResultDTO answers a Spot Funds P&L-bounds put
// with the barrier as stored, under the key of its axis's policy.
func ToPutSpotFundsPnlBoundsLimitResultDTO(
	l domain.LimitSpotFundsPnlBounds,
) (PutLimitResultDTO, error) {
	dto := ToSpotFundsPnlBoundsLimitDTO(l)
	switch l.Axis {
	case domain.SpotFundsPnlAxisRealized:
		return PutLimitResultDTO{SpotFundsPnlBoundsLimit: &dto}, nil
	case domain.SpotFundsPnlAxisUnrealized:
		return PutLimitResultDTO{SpotFundsUnrealizedPnlBoundsLimit: &dto}, nil
	case domain.SpotFundsPnlAxisTotal:
		return PutLimitResultDTO{SpotFundsTotalPnlBoundsLimit: &dto}, nil
	default:
		return PutLimitResultDTO{}, fmt.Errorf("httpapi: unknown Spot Funds P&L axis %q", l.Axis)
	}
}

// LimitReader is what reading a stored barrier back needs: the account
// surfaces the composition offers and the barriers stored for an account.
// backend.ControlPlane satisfies it.
type LimitReader interface {
	AccountFeatures() backend.AccountFeatures
	ListLimits(ctx context.Context, account domain.AccountID) (node.AccountLimits, error)
}

// PersistedSpotFundsLimitMode reads back the stored row of the Spot Funds
// limit-mode meta-policy, global only. It answers domain.ErrNotFound when no
// mode is stored.
func PersistedSpotFundsLimitMode(
	ctx context.Context, svc LimitReader,
) (domain.LimitSpotFundsLimitMode, error) {
	limits, err := svc.ListLimits(ctx, "")
	if err != nil {
		return domain.LimitSpotFundsLimitMode{}, err
	}
	if len(limits.SpotFundsLimitModes) == 0 {
		return domain.LimitSpotFundsLimitMode{}, domain.ErrNotFound
	}
	return limits.SpotFundsLimitModes[0], nil
}

// storedLimitAccount is the account a barrier put with account is stored
// under: account itself, except that a scope with an account axis naming none,
// in a composition that does not offer accounts, is stored under the one
// account the service's account scope supplied.
func storedLimitAccount(
	svc LimitReader, scope domain.LimitScope, account domain.AccountID,
) domain.AccountID {
	if scope.HasAccountAxis() {
		return svc.AccountFeatures().AccountFor(account)
	}
	return account
}

// PersistedRateLimit reads back the rate-limit barrier stored at target's
// address. It answers domain.ErrNotFound when none is stored there.
func PersistedRateLimit(
	ctx context.Context, svc LimitReader, target domain.LimitRate,
) (domain.LimitRate, error) {
	target.Account = storedLimitAccount(svc, target.Scope, target.Account)
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitRate{}, err
	}
	for _, limit := range limits.RateLimits {
		if sameLimitAddress(limit.Scope, limit.Account, limit.Asset,
			target.Scope, target.Account, target.Asset) {
			return limit, nil
		}
	}
	return domain.LimitRate{}, domain.ErrNotFound
}

// PersistedOrderSizeLimit reads back the order-size barrier stored at
// target's address. It answers domain.ErrNotFound when none is stored there.
func PersistedOrderSizeLimit(
	ctx context.Context, svc LimitReader, target domain.LimitOrderSize,
) (domain.LimitOrderSize, error) {
	target.Account = storedLimitAccount(svc, target.Scope, target.Account)
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitOrderSize{}, err
	}
	for _, limit := range limits.OrderSizeLimits {
		if sameLimitAddress(limit.Scope, limit.Account, limit.Asset,
			target.Scope, target.Account, target.Asset) {
			return limit, nil
		}
	}
	return domain.LimitOrderSize{}, domain.ErrNotFound
}

// PersistedSpotFundsPnlBoundsLimit reads back the SpotFunds P&L-bounds
// barrier stored at target's address. It answers domain.ErrNotFound when none
// is stored there.
func PersistedSpotFundsPnlBoundsLimit(
	ctx context.Context, svc LimitReader, target domain.LimitSpotFundsPnlBounds,
) (domain.LimitSpotFundsPnlBounds, error) {
	target.Account = storedLimitAccount(svc, target.Scope, target.Account)
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitSpotFundsPnlBounds{}, err
	}
	for _, limit := range limits.SpotFundsPnlBoundsLimits {
		if sameSpotFundsPnlBoundsAddress(limit, target) {
			return limit, nil
		}
	}
	return domain.LimitSpotFundsPnlBounds{}, domain.ErrNotFound
}

func sameLimitAddress(
	leftScope domain.LimitScope, leftAccount domain.AccountID, leftAsset string,
	rightScope domain.LimitScope, rightAccount domain.AccountID, rightAsset string,
) bool {
	return leftScope == rightScope && leftAccount == rightAccount && leftAsset == rightAsset
}

func sameSpotFundsPnlBoundsAddress(
	left domain.LimitSpotFundsPnlBounds,
	right domain.LimitSpotFundsPnlBounds,
) bool {
	return left.Axis == right.Axis &&
		left.Scope == right.Scope &&
		left.Account == right.Account &&
		left.AccountGroup == right.AccountGroup
}
