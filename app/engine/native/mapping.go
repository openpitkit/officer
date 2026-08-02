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

package native

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"unicode"

	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pkg/optional"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/pretrade/policies"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

// --- engine-id resolution ---------------------------------------------------

// idResolver maps domain dictionary aliases onto stored integer engine ids.
// Copies share one state so passing a resolver through mapping helpers never
// copies its mutex. Aliases can be published or renamed while hot-path lookups
// continue; numeric ids remain stable and are never derived by hashing aliases.
type idResolver struct {
	shared *idResolverState
}

type idResolverState struct {
	mu sync.RWMutex

	accounts       map[domain.AccountID]param.AccountID
	accountAliases map[uint64]domain.AccountID
	groups         map[string]param.AccountGroupID
	groupAliases   map[uint32]string
}

// newIDResolver builds the resolver from the snapshot's accounts and groups,
// converting each stored engine id with the binding's integer constructors
// (param.NewAccountIDFromUint64 / NewAccountGroupIDFromUint32). An account whose
// engine id is unassigned (zero) or out of range, or a group whose engine id is
// unassigned/out of range, is corruption of our own persisted ids and aborts the
// build.
func newIDResolver(accounts []domain.Account, groups []domain.AccountGroup) (idResolver, error) {
	r := newEmptyIDResolver(len(accounts), len(groups))
	for _, account := range accounts {
		if err := r.addAccountResolverEntry(account); err != nil {
			return idResolver{}, err
		}
	}
	for _, group := range groups {
		if err := r.addGroupResolverEntry(group); err != nil {
			return idResolver{}, err
		}
	}
	return r, nil
}

func newEmptyIDResolver(accountCount, groupCount int) idResolver {
	return idResolver{shared: &idResolverState{
		accounts:       make(map[domain.AccountID]param.AccountID, accountCount),
		accountAliases: make(map[uint64]domain.AccountID, accountCount),
		groups:         make(map[string]param.AccountGroupID, groupCount),
		groupAliases:   make(map[uint32]string, groupCount),
	}}
}

func (r *idResolver) ensureInitialized() {
	if r.shared == nil {
		*r = newEmptyIDResolver(0, 0)
	}
}

// account resolves an account code to its engine account id. An unknown code is
// caller input (e.g. an order for an account the live engine does not run) and
// wraps domain.ErrInvalid so the HTTP surface reports 400 rather than 500.
func (r idResolver) account(code domain.AccountID) (param.AccountID, error) {
	if r.shared == nil {
		return param.AccountID{}, unknownAccountAliasError(code)
	}
	r.shared.mu.RLock()
	id, ok := r.shared.accounts[code]
	r.shared.mu.RUnlock()
	if !ok {
		return param.AccountID{}, unknownAccountAliasError(code)
	}
	return id, nil
}

func (r idResolver) accountIDSnapshot() map[domain.AccountID]param.AccountID {
	if r.shared == nil {
		return map[domain.AccountID]param.AccountID{}
	}
	r.shared.mu.RLock()
	defer r.shared.mu.RUnlock()

	accounts := make(map[domain.AccountID]param.AccountID, len(r.shared.accounts))
	for alias, id := range r.shared.accounts {
		accounts[alias] = id
	}
	return accounts
}

// group resolves a group code to its engine group id. An unknown code is caller
// input and wraps domain.ErrInvalid.
func (r idResolver) group(code string) (param.AccountGroupID, error) {
	if r.shared == nil {
		return param.AccountGroupID{}, unknownGroupAliasError(code)
	}
	r.shared.mu.RLock()
	id, ok := r.shared.groups[code]
	r.shared.mu.RUnlock()
	if !ok {
		return param.AccountGroupID{}, unknownGroupAliasError(code)
	}
	return id, nil
}

func unknownAccountAliasError(code domain.AccountID) error {
	return fmt.Errorf("engine: unknown account resolver alias %q: %w", code, domain.ErrInvalid)
}

func unknownGroupAliasError(code string) error {
	return fmt.Errorf("engine: unknown group resolver alias %q: %w", code, domain.ErrInvalid)
}

func (r idResolver) addAccountResolverEntry(account domain.Account) error {
	id, err := engineAccountID(account.EngineAccountID, account.Code)
	if err != nil {
		return err
	}
	if r.shared == nil {
		return fmt.Errorf("engine: account resolver is not initialized")
	}

	idKey := uint64(id.Handle())
	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()
	if _, exists := r.shared.accounts[account.Code]; exists {
		return fmt.Errorf(
			"engine: account resolver alias %q already exists: %w",
			account.Code, domain.ErrAlreadyExists,
		)
	}
	if alias, exists := r.shared.accountAliases[idKey]; exists {
		return fmt.Errorf(
			"engine: account engine id %d already belongs to resolver alias %q: %w",
			account.EngineAccountID, alias, domain.ErrInvalid,
		)
	}
	r.shared.accounts[account.Code] = id
	r.shared.accountAliases[idKey] = account.Code
	return nil
}

func (r idResolver) renameAccountResolverEntry(
	oldCode domain.AccountID, account domain.Account,
) error {
	targetID, err := engineAccountID(account.EngineAccountID, account.Code)
	if err != nil {
		return err
	}
	if r.shared == nil {
		return unknownAccountAliasError(oldCode)
	}

	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()
	currentID, exists := r.shared.accounts[oldCode]
	if !exists {
		return unknownAccountAliasError(oldCode)
	}
	if currentID.Handle() != targetID.Handle() {
		return fmt.Errorf(
			"engine: account resolver alias %q has engine id %s, target %q has %d: %w",
			oldCode, currentID, account.Code, account.EngineAccountID, domain.ErrInvalid,
		)
	}
	if oldCode == account.Code {
		return nil
	}
	if _, exists := r.shared.accounts[account.Code]; exists {
		return fmt.Errorf(
			"engine: account resolver alias %q already exists: %w",
			account.Code, domain.ErrAlreadyExists,
		)
	}

	delete(r.shared.accounts, oldCode)
	r.shared.accounts[account.Code] = currentID
	r.shared.accountAliases[uint64(currentID.Handle())] = account.Code
	return nil
}

func (r idResolver) addGroupResolverEntry(group domain.AccountGroup) error {
	id, err := resolverGroupID(group)
	if err != nil {
		return err
	}
	if r.shared == nil {
		return fmt.Errorf("engine: group resolver is not initialized")
	}

	idKey := uint32(id.Handle())
	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()
	if _, exists := r.shared.groups[group.Code]; exists {
		return fmt.Errorf(
			"engine: group resolver alias %q already exists: %w",
			group.Code, domain.ErrAlreadyExists,
		)
	}
	if alias, exists := r.shared.groupAliases[idKey]; exists {
		return fmt.Errorf(
			"engine: group engine id %d already belongs to resolver alias %q: %w",
			group.EngineGroupID, alias, domain.ErrInvalid,
		)
	}
	r.shared.groups[group.Code] = id
	r.shared.groupAliases[idKey] = group.Code
	return nil
}

func (r idResolver) renameGroupResolverEntry(
	oldCode string, group domain.AccountGroup,
) error {
	targetID, err := resolverGroupID(group)
	if err != nil {
		return err
	}
	if r.shared == nil {
		return unknownGroupAliasError(oldCode)
	}

	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()
	currentID, exists := r.shared.groups[oldCode]
	if !exists {
		return unknownGroupAliasError(oldCode)
	}
	if currentID.Handle() != targetID.Handle() {
		return fmt.Errorf(
			"engine: group resolver alias %q has engine id %s, target %q has %d: %w",
			oldCode, currentID, group.Code, group.EngineGroupID, domain.ErrInvalid,
		)
	}
	if oldCode == group.Code {
		return nil
	}
	if _, exists := r.shared.groups[group.Code]; exists {
		return fmt.Errorf(
			"engine: group resolver alias %q already exists: %w",
			group.Code, domain.ErrAlreadyExists,
		)
	}

	delete(r.shared.groups, oldCode)
	r.shared.groups[group.Code] = currentID
	r.shared.groupAliases[uint32(currentID.Handle())] = group.Code
	return nil
}

func (r idResolver) removeGroupResolverEntry(group domain.AccountGroup) error {
	targetID, err := resolverGroupID(group)
	if err != nil {
		return err
	}
	if r.shared == nil {
		return unknownGroupAliasError(group.Code)
	}

	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()
	currentID, exists := r.shared.groups[group.Code]
	if !exists {
		return unknownGroupAliasError(group.Code)
	}
	if currentID.Handle() != targetID.Handle() {
		return fmt.Errorf(
			"engine: group resolver alias %q has engine id %s, removal target has %d: %w",
			group.Code, currentID, group.EngineGroupID, domain.ErrInvalid,
		)
	}

	delete(r.shared.groups, group.Code)
	delete(r.shared.groupAliases, uint32(currentID.Handle()))
	return nil
}

func resolverGroupID(group domain.AccountGroup) (param.AccountGroupID, error) {
	if group.Code == "" {
		if group.EngineGroupID != 0 {
			return param.AccountGroupID{}, fmt.Errorf(
				"engine: default group engine id %d, want 0: %w",
				group.EngineGroupID, domain.ErrInvalid,
			)
		}
		return param.DefaultAccountGroup, nil
	}
	return engineGroupID(group.EngineGroupID, group.Code)
}

// engineAccountID converts a stored engine account id into a param.AccountID via
// the binding's uint64 constructor. A zero or out-of-range value is corruption
// of our own persisted id, not caller input, so it is an internal error.
func engineAccountID(id domain.EngineAccountID, code domain.AccountID) (param.AccountID, error) {
	if err := domain.ValidateEngineAccountID(id); err != nil {
		return param.AccountID{}, fmt.Errorf(
			"engine: account %q engine id: %w", code, err)
	}
	return param.NewAccountIDFromUint64(id.Uint64()), nil
}

// engineGroupID converts a stored engine group id into a param.AccountGroupID
// via the binding's uint32 constructor. A zero or out-of-range value is
// corruption of our own persisted id, so it is an internal error.
func engineGroupID(id domain.EngineGroupID, code string) (param.AccountGroupID, error) {
	if err := domain.ValidateEngineGroupID(id); err != nil {
		return param.AccountGroupID{}, fmt.Errorf(
			"engine: group %q engine id: %w", code, err)
	}
	group, err := param.NewAccountGroupIDFromUint32(id.Uint32())
	if err != nil {
		return param.AccountGroupID{}, fmt.Errorf(
			"engine: group %q engine id %d: %w", code, id.Uint32(), err)
	}
	return group, nil
}

// --- rate-limit translation -------------------------------------------------

// rateLimitAxes maps a rate-limit barrier set onto the public Configure axes:
// an optional broker barrier and per-asset/account/account-asset slices. The
// asset/account/account-asset slices are always non-nil so a Configure call
// replaces each axis wholesale (an empty slice clears the axis); barriers are
// added and removed at runtime and a surviving key keeps its live counter. The
// caller wraps broker in the explicit optional update shape, so nil clears the
// broker axis instead of leaving it unchanged.
func rateLimitAxes(limits []domain.LimitRate, res idResolver) (
	*policies.RateLimitBrokerBarrier,
	[]policies.RateLimitAssetBarrier,
	[]policies.RateLimitAccountBarrier,
	[]policies.RateLimitAccountAssetBarrier,
	error,
) {
	var broker *policies.RateLimitBrokerBarrier
	assets := []policies.RateLimitAssetBarrier{}
	accounts := []policies.RateLimitAccountBarrier{}
	accountAssets := []policies.RateLimitAccountAssetBarrier{}

	for _, limit := range limits {
		rate, err := rateLimitValue(limit)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		switch limit.Scope {
		case domain.ScopeBroker:
			// The axis holds one barrier; a second one would silently replace the
			// first and lose a configured limit.
			if broker != nil {
				return nil, nil, nil, nil, fmt.Errorf(
					"engine: rate_limit carries more than one broker barrier")
			}
			broker = &policies.RateLimitBrokerBarrier{Limit: rate}
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Asset)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			assets = append(assets, policies.RateLimitAssetBarrier{
				Limit:           rate,
				SettlementAsset: asset,
			})
		case domain.ScopeAccount:
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			accounts = append(accounts, policies.RateLimitAccountBarrier{
				Limit:     rate,
				AccountID: account,
			})
		case domain.ScopeAccountAsset:
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			asset, err := newAsset(limit.Asset)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			accountAssets = append(accountAssets, policies.RateLimitAccountAssetBarrier{
				Limit:           rate,
				AccountID:       account,
				SettlementAsset: asset,
			})
		default:
			return nil, nil, nil, nil, fmt.Errorf(
				"engine: rate_limit unsupported scope %q", limit.Scope)
		}
	}
	return broker, assets, accounts, accountAssets, nil
}

// orderSizeAxes maps an order-size barrier set onto the public Configure axes:
// an optional broker barrier and per-asset/account-asset slices. The slices are
// always non-nil so a Configure call replaces each axis wholesale (an empty
// slice clears the axis). The caller wraps broker in the explicit optional
// update shape, so nil clears the broker axis instead of leaving it unchanged.
// The at-least-one-barrier rule is enforced before this mapper runs.
func orderSizeAxes(limits []domain.LimitOrderSize, res idResolver) (
	*policies.OrderSizeBrokerBarrier,
	[]policies.OrderSizeAssetBarrier,
	[]policies.OrderSizeAccountAssetBarrier,
	error,
) {
	var broker *policies.OrderSizeBrokerBarrier
	assets := []policies.OrderSizeAssetBarrier{}
	accountAssets := []policies.OrderSizeAccountAssetBarrier{}

	for _, limit := range limits {
		size, err := orderSizeValue(limit)
		if err != nil {
			return nil, nil, nil, err
		}
		switch limit.Scope {
		case domain.ScopeBroker:
			// The axis holds one barrier; a second one would silently replace the
			// first and lose a configured limit.
			if broker != nil {
				return nil, nil, nil, fmt.Errorf(
					"engine: order_size_limit carries more than one broker barrier")
			}
			broker = &policies.OrderSizeBrokerBarrier{Limit: size}
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Asset)
			if err != nil {
				return nil, nil, nil, err
			}
			assets = append(assets, policies.OrderSizeAssetBarrier{
				Limit:           size,
				SettlementAsset: asset,
			})
		case domain.ScopeAccountAsset:
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, nil, nil, err
			}
			asset, err := newAsset(limit.Asset)
			if err != nil {
				return nil, nil, nil, err
			}
			accountAssets = append(accountAssets, policies.OrderSizeAccountAssetBarrier{
				Limit:           size,
				AccountID:       account,
				SettlementAsset: asset,
			})
		default:
			return nil, nil, nil, fmt.Errorf(
				"engine: order_size_limit unsupported scope %q", limit.Scope)
		}
	}
	return broker, assets, accountAssets, nil
}

// spotFundsPnlBoundsAxes maps SpotFunds self-computed account-currency P&L
// bounds onto the runtime Configure axes. Every returned slice is non-nil so
// Configure replaces all axes wholesale.
func spotFundsPnlBoundsAxes(
	limits []domain.LimitSpotFundsPnlBounds,
	res idResolver,
) (
	optional.Option[*policies.SpotFundsPnlBoundsBarrier],
	[]policies.SpotFundsPnlBoundsAccountGroupBarrier,
	[]policies.SpotFundsPnlBoundsAccountBarrier,
	error,
) {
	// A nil value in a set Option clears the global axis. Runtime configuration
	// must replace the complete stored set, not leave a removed global barrier
	// live on the engine.
	global := optional.Some[*policies.SpotFundsPnlBoundsBarrier](nil)
	globalSet := false
	groups := []policies.SpotFundsPnlBoundsAccountGroupBarrier{}
	accounts := []policies.SpotFundsPnlBoundsAccountBarrier{}

	for _, limit := range limits {
		barrier, err := spotFundsPnlBoundsBarrier(limit)
		if err != nil {
			return global, nil, nil, err
		}
		switch limit.Scope {
		case domain.ScopeGlobal:
			// The axis holds one barrier; a second one would silently replace the
			// first and lose a configured bound.
			if globalSet {
				return global, nil, nil, fmt.Errorf(
					"engine: spot_funds_pnl_bounds_kill_switch carries more than one global barrier")
			}
			global = optional.Some(&barrier)
			globalSet = true
		case domain.ScopeAccountGroup:
			group, err := res.group(limit.AccountGroup)
			if err != nil {
				return global, nil, nil, err
			}
			groups = append(groups, policies.SpotFundsPnlBoundsAccountGroupBarrier{
				Barrier:        barrier,
				AccountGroupID: group,
			})
		case domain.ScopeAccount:
			account, err := res.account(limit.Account)
			if err != nil {
				return global, nil, nil, err
			}
			accounts = append(accounts, policies.SpotFundsPnlBoundsAccountBarrier{
				Barrier:   barrier,
				AccountID: account,
			})
		default:
			return global, nil, nil, fmt.Errorf(
				"engine: spot_funds_pnl_bounds_kill_switch unsupported scope %q",
				limit.Scope,
			)
		}
	}
	return global, groups, accounts, nil
}

// rateLimitReady maps a rate-limit barrier set onto a ready builder. Each
// domain barrier carries both max_orders and window (domain validation
// guarantees it), distributed across the broker/asset/account/account-asset
// axes by scope.
func rateLimitReady(limits []domain.LimitRate, res idResolver) (*policies.RateLimitReadyBuilder, error) {
	builder := policies.BuildRateLimit()
	ready := builder.PolicyGroupID(0)

	var (
		broker        *policies.RateLimitBrokerBarrier
		assets        []policies.RateLimitAssetBarrier
		accounts      []policies.RateLimitAccountBarrier
		accountAssets []policies.RateLimitAccountAssetBarrier
	)
	for _, limit := range limits {
		rate, err := rateLimitValue(limit)
		if err != nil {
			return nil, err
		}
		switch limit.Scope {
		case domain.ScopeBroker:
			// The builder takes one broker barrier; a second one would silently
			// replace the first and lose a configured limit.
			if broker != nil {
				return nil, fmt.Errorf("engine: rate_limit carries more than one broker barrier")
			}
			broker = &policies.RateLimitBrokerBarrier{Limit: rate}
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Asset)
			if err != nil {
				return nil, err
			}
			assets = append(assets, policies.RateLimitAssetBarrier{
				Limit:           rate,
				SettlementAsset: asset,
			})
		case domain.ScopeAccount:
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, err
			}
			accounts = append(accounts, policies.RateLimitAccountBarrier{
				Limit:     rate,
				AccountID: account,
			})
		case domain.ScopeAccountAsset:
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, err
			}
			asset, err := newAsset(limit.Asset)
			if err != nil {
				return nil, err
			}
			accountAssets = append(accountAssets, policies.RateLimitAccountAssetBarrier{
				Limit:           rate,
				AccountID:       account,
				SettlementAsset: asset,
			})
		default:
			return nil, fmt.Errorf("engine: rate_limit unsupported scope %q", limit.Scope)
		}
	}

	if broker != nil {
		ready = ready.BrokerBarrier(*broker)
	}
	ready = ready.AssetBarriers(assets...).
		AccountBarriers(accounts...).
		AccountAssetBarriers(accountAssets...)
	return ready, nil
}

// orderSizeReady maps an order-size-limit barrier set onto a ready builder.
func orderSizeReady(limits []domain.LimitOrderSize, res idResolver) (*policies.OrderSizeLimitReadyBuilder, error) {
	builder := policies.BuildOrderSizeLimit()
	ready := builder.PolicyGroupID(0)

	var (
		broker        *policies.OrderSizeBrokerBarrier
		assets        []policies.OrderSizeAssetBarrier
		accountAssets []policies.OrderSizeAccountAssetBarrier
	)
	for _, limit := range limits {
		size, err := orderSizeValue(limit)
		if err != nil {
			return nil, err
		}
		switch limit.Scope {
		case domain.ScopeBroker:
			// The builder takes one broker barrier; a second one would silently
			// replace the first and lose a configured limit.
			if broker != nil {
				return nil, fmt.Errorf("engine: order_size_limit carries more than one broker barrier")
			}
			broker = &policies.OrderSizeBrokerBarrier{Limit: size}
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Asset)
			if err != nil {
				return nil, err
			}
			assets = append(assets, policies.OrderSizeAssetBarrier{
				Limit:           size,
				SettlementAsset: asset,
			})
		case domain.ScopeAccountAsset:
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, err
			}
			asset, err := newAsset(limit.Asset)
			if err != nil {
				return nil, err
			}
			accountAssets = append(accountAssets, policies.OrderSizeAccountAssetBarrier{
				Limit:           size,
				AccountID:       account,
				SettlementAsset: asset,
			})
		default:
			return nil, fmt.Errorf("engine: order_size_limit unsupported scope %q", limit.Scope)
		}
	}

	if broker != nil {
		ready = ready.BrokerBarrier(*broker)
	}
	ready = ready.AssetBarriers(assets...).AccountAssetBarriers(accountAssets...)
	return ready, nil
}

// rateLimitValue extracts the engine rate limit from a typed rate barrier.
// Domain validation already bounds max_orders to (0, 1e9] and window to (0, 24h].
func rateLimitValue(limit domain.LimitRate) (policies.RateLimit, error) {
	if limit.MaxOrders > math.MaxUint {
		return policies.RateLimit{}, fmt.Errorf(
			"engine: rate_limit max_orders %d exceeds platform uint", limit.MaxOrders)
	}
	return policies.RateLimit{MaxOrders: uint(limit.MaxOrders), Window: limit.Window}, nil
}

// orderSizeValue extracts the engine order-size limit from a typed order-size
// barrier.
//
// The binding's OrderSizeLimit always carries both a quantity and a notional
// cap and rejects an order when it exceeds either (a strict ">"). The domain
// barrier may omit one dimension ("at least one required"), meaning that
// dimension must not constrain. The boundary adapter maps an omitted dimension
// to the largest representable value so it never triggers a reject; this is a
// transport-only sentinel and never participates in any calculation.
func orderSizeValue(limit domain.LimitOrderSize) (policies.OrderSizeLimit, error) {
	maxQty, err := param.NewQuantityFromInt64(math.MaxInt64)
	if err != nil {
		return policies.OrderSizeLimit{}, fmt.Errorf(
			"engine: order_size_limit unbounded quantity: %w", err)
	}
	maxNotional, err := param.NewVolumeFromInt64(math.MaxInt64)
	if err != nil {
		return policies.OrderSizeLimit{}, fmt.Errorf(
			"engine: order_size_limit unbounded notional: %w", err)
	}
	out := policies.OrderSizeLimit{MaxQuantity: maxQty, MaxNotional: maxNotional}

	if limit.MaxQuantity != "" {
		q, err := param.NewQuantityFromString(limit.MaxQuantity)
		if err != nil {
			return policies.OrderSizeLimit{}, fmt.Errorf(
				"engine: order_size_limit max_quantity %q: %w", limit.MaxQuantity, err)
		}
		out.MaxQuantity = q
	}
	if limit.MaxNotional != "" {
		n, err := param.NewVolumeFromString(limit.MaxNotional)
		if err != nil {
			return policies.OrderSizeLimit{}, fmt.Errorf(
				"engine: order_size_limit max_notional %q: %w", limit.MaxNotional, err)
		}
		out.MaxNotional = n
	}
	return out, nil
}

func spotFundsPnlBoundsBarrier(
	limit domain.LimitSpotFundsPnlBounds,
) (policies.SpotFundsPnlBoundsBarrier, error) {
	lower, upper, err := pnlBoundOptions(
		limit.LowerBound,
		limit.UpperBound,
		"spot_funds_pnl_bounds",
	)
	if err != nil {
		return policies.SpotFundsPnlBoundsBarrier{}, err
	}
	return policies.SpotFundsPnlBoundsBarrier{
		LowerBound: lower,
		UpperBound: upper,
	}, nil
}

func pnlBoundOptions(
	lowerBound string,
	upperBound string,
	label string,
) (optional.Option[param.Pnl], optional.Option[param.Pnl], error) {
	lower := optional.None[param.Pnl]()
	upper := optional.None[param.Pnl]()
	if lowerBound != "" {
		p, err := param.NewPnlFromString(lowerBound)
		if err != nil {
			return lower, upper, fmt.Errorf(
				"engine: %s lower_bound %q: %w", label, lowerBound, err)
		}
		lower = optional.Some(p)
	}
	if upperBound != "" {
		p, err := param.NewPnlFromString(upperBound)
		if err != nil {
			return lower, upper, fmt.Errorf(
				"engine: %s upper_bound %q: %w", label, upperBound, err)
		}
		upper = optional.Some(p)
	}
	return lower, upper, nil
}

// newAsset parses a caller-supplied asset code into a param.Asset. A bad format
// is caller input, so it wraps domain.ErrInvalid (HTTP 400).
func newAsset(code string) (param.Asset, error) {
	asset, err := param.NewAsset(code)
	if err != nil {
		return param.Asset{}, fmt.Errorf("engine: asset %q: %w: %w", code, err, domain.ErrInvalid)
	}
	return asset, nil
}

// --- account adjustment translation ----------------------------------------

// accountAdjustmentFromRequest maps one domain adjustment request onto a single
// model.AccountAdjustment: a balance operation on req.Asset with optional
// average-entry-price, per-field absolute|delta amounts, and optional bounds.
func accountAdjustmentFromRequest(
	req domain.AdjustmentRequest,
) (model.AccountAdjustment, error) {
	asset, err := newAsset(req.Asset)
	if err != nil {
		return model.AccountAdjustment{}, err
	}

	balanceOp := model.AccountAdjustmentBalanceOperationValues{
		Asset: optional.Some(asset),
	}
	if req.AverageEntryPrice != "" {
		price, perr := param.NewPriceFromString(req.AverageEntryPrice)
		if perr != nil {
			return model.AccountAdjustment{}, fmt.Errorf(
				"engine: adjustment average_entry_price %q: %w: %w",
				req.AverageEntryPrice, perr, domain.ErrInvalid)
		}
		balanceOp.AverageEntryPrice = optional.Some(price)
	}
	if req.RealizedPnl != "" || req.RealizedPnlHaltReason != "" {
		pnlState, perr := pnlStateFrom(req.RealizedPnl, req.RealizedPnlHaltReason)
		if perr != nil {
			return model.AccountAdjustment{}, perr
		}
		balanceOp.RealizedPnl = optional.Some(pnlState)
	}

	amount, err := adjustmentAmountValues(req)
	if err != nil {
		return model.AccountAdjustment{}, err
	}

	values := model.AccountAdjustmentValues{
		BalanceOperation: optional.Some(
			model.NewAccountAdjustmentBalanceOperationFromValues(balanceOp)),
		Amount: optional.Some(model.NewAccountAdjustmentAmountFromValues(amount)),
	}
	if bounds, ok, berr := adjustmentBoundsValues(req); berr != nil {
		return model.AccountAdjustment{}, berr
	} else if ok {
		values.Bounds = optional.Some(model.NewAccountAdjustmentBoundsFromValues(bounds))
	}

	// NewAccountAdjustmentFromValues never errors (the discriminated operation
	// makes a balance/position conflict unrepresentable), but thread it anyway.
	adjustment, err := model.NewAccountAdjustmentFromValues(values)
	if err != nil {
		return model.AccountAdjustment{}, fmt.Errorf("engine: build account adjustment: %w", err)
	}
	return adjustment, nil
}

func pnlStateFrom(value string, haltReason domain.PnlHaltReason) (model.PnlState, error) {
	if haltReason != "" {
		reason, err := pnlHaltReasonToSDK(haltReason)
		if err != nil {
			return model.PnlState{}, err
		}
		state, err := model.NewPnlHaltedState(reason)
		if err != nil {
			return model.PnlState{}, fmt.Errorf(
				"engine: adjustment realized_pnl halt %q: %w: %w",
				haltReason, err, domain.ErrInvalid,
			)
		}
		return state, nil
	}
	pnl, err := param.NewPnlFromString(value)
	if err != nil {
		return model.PnlState{}, fmt.Errorf(
			"engine: adjustment realized_pnl %q: %w: %w", value, err, domain.ErrInvalid,
		)
	}
	return model.NewPnlState(pnl), nil
}

func pnlHaltReasonToSDK(reason domain.PnlHaltReason) (model.PnlHaltReason, error) {
	switch reason {
	case domain.PnlHaltReasonMissingFx:
		return model.PnlHaltReasonMissingFx, nil
	case domain.PnlHaltReasonMissingAccountCurrency:
		return model.PnlHaltReasonMissingAccountCurrency, nil
	case domain.PnlHaltReasonMissingInitialPnl:
		return model.PnlHaltReasonMissingInitialPnl, nil
	case domain.PnlHaltReasonMissingCostBasis:
		return model.PnlHaltReasonMissingCostBasis, nil
	case domain.PnlHaltReasonArithmeticOverflow:
		return model.PnlHaltReasonArithmeticOverflow, nil
	default:
		return 0, fmt.Errorf("engine: unsupported realized_pnl halt %q: %w", reason, domain.ErrInvalid)
	}
}

// adjustmentAmountValues maps the per-field balance/held/incoming amounts of a
// request onto the binding's amount values, each as an absolute or delta.
func adjustmentAmountValues(
	req domain.AdjustmentRequest,
) (model.AccountAdjustmentAmountValues, error) {
	var values model.AccountAdjustmentAmountValues
	if req.Balance != nil {
		amt, err := adjustmentAmountFrom(*req.Balance, "balance")
		if err != nil {
			return values, err
		}
		values.Balance = optional.Some(amt)
	}
	if req.Held != nil {
		amt, err := adjustmentAmountFrom(*req.Held, "held")
		if err != nil {
			return values, err
		}
		values.Held = optional.Some(amt)
	}
	if req.Incoming != nil {
		amt, err := adjustmentAmountFrom(*req.Incoming, "incoming")
		if err != nil {
			return values, err
		}
		values.Incoming = optional.Some(amt)
	}
	return values, nil
}

// adjustmentAmountFrom maps one domain amount field onto a param.AdjustmentAmount,
// honoring its absolute|delta mode.
func adjustmentAmountFrom(
	a domain.AdjustmentAmount, field string,
) (param.AdjustmentAmount, error) {
	size, err := param.NewPositionSizeFromString(a.Value)
	if err != nil {
		return param.AdjustmentAmount{}, fmt.Errorf(
			"engine: adjustment %s value %q: %w: %w", field, a.Value, err, domain.ErrInvalid)
	}
	switch a.Mode {
	case domain.AdjustmentModeAbsolute:
		return param.NewAbsoluteAdjustmentAmount(size), nil
	case domain.AdjustmentModeDelta:
		return param.NewDeltaAdjustmentAmount(size), nil
	default:
		return param.AdjustmentAmount{}, fmt.Errorf(
			"engine: adjustment %s unknown mode %q: %w", field, a.Mode, domain.ErrInvalid)
	}
}

// adjustmentBoundsValues maps the optional per-field bounds of a request onto
// the binding's bounds values. The bool is false when the request set no bound.
func adjustmentBoundsValues(
	req domain.AdjustmentRequest,
) (model.AccountAdjustmentBoundsValues, bool, error) {
	var values model.AccountAdjustmentBoundsValues
	hasBound := false
	set := func(b *domain.AdjustmentBounds, field string,
		lower, upper *optional.Option[param.PositionSize],
	) error {
		if b == nil {
			return nil
		}
		if b.Lower != "" {
			v, err := param.NewPositionSizeFromString(b.Lower)
			if err != nil {
				return fmt.Errorf(
					"engine: adjustment %s lower bound %q: %w: %w", field, b.Lower, err, domain.ErrInvalid)
			}
			*lower = optional.Some(v)
			hasBound = true
		}
		if b.Upper != "" {
			v, err := param.NewPositionSizeFromString(b.Upper)
			if err != nil {
				return fmt.Errorf(
					"engine: adjustment %s upper bound %q: %w: %w", field, b.Upper, err, domain.ErrInvalid)
			}
			*upper = optional.Some(v)
			hasBound = true
		}
		return nil
	}
	if err := set(req.BalanceBounds, "balance", &values.BalanceLower, &values.BalanceUpper); err != nil {
		return values, false, err
	}
	if err := set(req.HeldBounds, "held", &values.HeldLower, &values.HeldUpper); err != nil {
		return values, false, err
	}
	if err := set(req.IncomingBounds, "incoming", &values.IncomingLower, &values.IncomingUpper); err != nil {
		return values, false, err
	}
	return values, hasBound, nil
}

// outcomeAcceptedFromList extracts the accepted outcome for asset from a binding
// outcome list: the per-field delta and resulting absolute, including the
// settlement-asset realized P&L. A field absent in every outcome reports as the
// empty string (it was not adjusted). The spot-funds policy emits one outcome
// carrying the single adjusted asset's entry.
//
// One outcome per asset is expected, and a second one for the same asset is an
// error rather than a quiet choice: reporting the last would drop engine numbers,
// and combining them would invent an answer (summing deltas and reconciling
// absolutes is a model rework across the engine adapter and node persistence).
func outcomeAcceptedFromList(
	outcomes []accountadjustment.Outcome, asset string,
) (domain.AdjustmentOutcomeAccepted, bool, error) {
	var result domain.AdjustmentOutcomeAccepted
	found := false
	for _, outcome := range outcomes {
		entry := outcome.Entry
		if entry.Asset.String() != asset {
			continue
		}
		if found {
			return domain.AdjustmentOutcomeAccepted{}, false, fmt.Errorf(
				"engine: account adjustment returned several outcomes for asset %q", asset)
		}
		accepted, err := outcomeAcceptedFromEntry(entry)
		if err != nil {
			return domain.AdjustmentOutcomeAccepted{}, false, err
		}
		result = accepted
		found = true
	}
	return result, found, nil
}

// balanceOutcomesFromList emits one BalanceOutcome per engine outcome and
// performs no per-asset dedup; mergeBalanceOutcomes combines the phases.
func balanceOutcomesFromList(
	outcomes []accountadjustment.Outcome,
) ([]BalanceOutcome, error) {
	result := make([]BalanceOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		entry := outcome.Entry
		asset := entry.Asset.String()
		if asset == "" {
			continue
		}
		accepted, err := outcomeAcceptedFromEntry(entry)
		if err != nil {
			return nil, err
		}
		result = append(result, BalanceOutcome{Asset: asset, Outcome: accepted})
	}
	return result, nil
}

func mergeBalanceOutcomes(phases ...[]BalanceOutcome) ([]BalanceOutcome, error) {
	merged := make([]BalanceOutcome, 0)
	byAsset := make(map[string]int)
	for _, phase := range phases {
		for _, outcome := range phase {
			if outcome.Asset == "" {
				return nil, fmt.Errorf("engine: merge balance outcome without asset")
			}
			index, ok := byAsset[outcome.Asset]
			if !ok {
				byAsset[outcome.Asset] = len(merged)
				merged = append(merged, outcome)
				continue
			}
			combined, err := mergeAcceptedOutcomes(
				merged[index].Outcome, outcome.Outcome,
			)
			if err != nil {
				return nil, fmt.Errorf("engine: merge balance outcome %q: %w", outcome.Asset, err)
			}
			merged[index].Outcome = combined
		}
	}
	return merged, nil
}

func mergeAcceptedOutcomes(
	current, next domain.AdjustmentOutcomeAccepted,
) (domain.AdjustmentOutcomeAccepted, error) {
	var err error
	if current.BalanceDelta, err = mergeOutcomeDelta(
		current.BalanceDelta, next.BalanceDelta,
	); err != nil {
		return domain.AdjustmentOutcomeAccepted{}, err
	}
	if current.HeldDelta, err = mergeOutcomeDelta(
		current.HeldDelta, next.HeldDelta,
	); err != nil {
		return domain.AdjustmentOutcomeAccepted{}, err
	}
	if current.IncomingDelta, err = mergeOutcomeDelta(
		current.IncomingDelta, next.IncomingDelta,
	); err != nil {
		return domain.AdjustmentOutcomeAccepted{}, err
	}
	if current.RealizedPnlDelta, err = mergeOutcomeDelta(
		current.RealizedPnlDelta, next.RealizedPnlDelta,
	); err != nil {
		return domain.AdjustmentOutcomeAccepted{}, err
	}
	if next.BalanceResult != "" {
		current.BalanceResult = next.BalanceResult
	}
	if next.HeldResult != "" {
		current.HeldResult = next.HeldResult
	}
	if next.IncomingResult != "" {
		current.IncomingResult = next.IncomingResult
	}
	if next.RealizedPnlHaltReason != "" {
		current.RealizedPnlResult = ""
		current.RealizedPnlHaltReason = next.RealizedPnlHaltReason
	} else if next.RealizedPnlResult != "" {
		current.RealizedPnlResult = next.RealizedPnlResult
		current.RealizedPnlHaltReason = ""
	}
	if next.AverageEntryPrice != "" {
		current.AverageEntryPrice = next.AverageEntryPrice
	}
	return current, nil
}

func mergeOutcomeDelta(current, next string) (string, error) {
	if current == "" {
		return next, nil
	}
	if next == "" {
		return current, nil
	}
	return domain.AddDecimals(current, next)
}

func outcomeAcceptedFromEntry(
	entry accountadjustment.AccountOutcomeEntry,
) (domain.AdjustmentOutcomeAccepted, error) {
	var result domain.AdjustmentOutcomeAccepted
	if amt, ok := entry.Balance.Get(); ok {
		result.BalanceDelta = amt.Delta.String()
		result.BalanceResult = amt.Absolute.String()
	}
	if amt, ok := entry.Held.Get(); ok {
		result.HeldDelta = amt.Delta.String()
		result.HeldResult = amt.Absolute.String()
	}
	if amt, ok := entry.Incoming.Get(); ok {
		result.IncomingDelta = amt.Delta.String()
		result.IncomingResult = amt.Absolute.String()
	}
	if pnlOutcome, ok := entry.RealizedPnl.Get(); ok {
		if haltReason, halted := pnlOutcome.HaltReason(); halted {
			reason, err := pnlHaltReasonFromSDK(haltReason)
			if err != nil {
				return domain.AdjustmentOutcomeAccepted{}, fmt.Errorf(
					"engine: outcome for asset %q: %w", entry.Asset.String(), err,
				)
			}
			result.RealizedPnlHaltReason = reason
		} else if amount, computed := pnlOutcome.Amount(); computed {
			result.RealizedPnlDelta = amount.Delta.String()
			result.RealizedPnlResult = amount.Absolute.String()
		}
	}
	if price, ok := entry.AverageEntryPrice.Get(); ok {
		result.AverageEntryPrice = price.String()
	}
	return result, nil
}

func spotFundsAccountPnlFromList(
	account param.AccountID,
	outcomes []accountadjustment.AccountPnlOutcome,
) (string, domain.PnlHaltReason, error) {
	selected := false
	var pnl string
	var haltReason domain.PnlHaltReason
	for _, outcome := range outcomes {
		if outcome.AccountID != account {
			slog.Warn(
				"skip spot funds account pnl outcome",
				"account_id", outcome.AccountID,
				"reason", "account does not match execution report",
			)
			continue
		}
		if amount, computed := outcome.Amount(); computed && amount.Delta.IsZero() {
			continue
		}
		if selected {
			slog.Warn(
				"skip spot funds account pnl outcome",
				"account_id", outcome.AccountID,
				"reason", "matching outcome was already selected",
			)
			continue
		}
		selected = true
		if engineHaltReason, halted := outcome.HaltReason(); halted {
			reason, err := pnlHaltReasonFromSDK(engineHaltReason)
			if err != nil {
				return "", "", fmt.Errorf("engine: account pnl outcome: %w", err)
			}
			haltReason = reason
		} else if amount, computed := outcome.Amount(); computed {
			pnl = amount.Absolute.String()
		}
	}
	return pnl, haltReason, nil
}

// pnlHaltReasonFromSDK maps an engine halt reason onto its domain constant.
//
// A reason this seam does not know is an engine/Officer version mismatch, not
// operator input: substituting a placeholder would persist a halt that
// pnlHaltReasonToSDK cannot replay, blocking the next engine rebuild. Fail here
// instead, so a new engine reason must be added to both directions at once.
func pnlHaltReasonFromSDK(
	reason model.PnlHaltReason,
) (domain.PnlHaltReason, error) {
	switch reason {
	case model.PnlHaltReasonMissingFx:
		return domain.PnlHaltReasonMissingFx, nil
	case model.PnlHaltReasonMissingAccountCurrency:
		return domain.PnlHaltReasonMissingAccountCurrency, nil
	case model.PnlHaltReasonMissingInitialPnl:
		return domain.PnlHaltReasonMissingInitialPnl, nil
	case model.PnlHaltReasonMissingCostBasis:
		return domain.PnlHaltReasonMissingCostBasis, nil
	case model.PnlHaltReasonArithmeticOverflow:
		return domain.PnlHaltReasonArithmeticOverflow, nil
	default:
		return "", fmt.Errorf("engine: unrecognized realized_pnl halt reason %d", reason)
	}
}

// outcomeRejectedFrom maps a batch error onto the domain rejected outcome,
// carrying the index of the adjustment the engine stopped on so the reject stays
// attributable to its request.
//
// A batch error without a reject is a broken engine contract: the batch failed
// and named no cause, and Officer has no answer to put in its place.
func outcomeRejectedFrom(
	batch reject.AccountAdjustmentBatchError,
) (domain.AdjustmentOutcomeRejected, error) {
	if len(batch.Rejects) == 0 {
		return domain.AdjustmentOutcomeRejected{}, fmt.Errorf(
			"engine: account adjustment batch rejected at index %d without a reject",
			batch.FailedAdjustmentIndex)
	}
	rejected := adjustmentRejectFrom(batch.Rejects[0])
	rejected.FailedAdjustmentIndex = batch.FailedAdjustmentIndex
	return rejected, nil
}

// adjustmentRejectFrom maps one binding reject onto the domain rejected outcome.
func adjustmentRejectFrom(r reject.Reject) domain.AdjustmentOutcomeRejected {
	return domain.AdjustmentOutcomeRejected{
		Code:    rejectCodeName(r.Code),
		Scope:   rejectScopeName(r.Scope),
		Policy:  r.Policy,
		Reason:  sanitizeText(r.Reason),
		Details: sanitizeText(r.Details),
	}
}

// --- order translation ------------------------------------------------------

// orderModelFrom maps a domain order onto a model.Order operation view:
// instrument (base/quote), account (resolved to its stored engine id), side,
// trade amount (quantity or volume), and the optional limit price (omitted for
// market orders).
func orderModelFrom(o domain.Order, res idResolver) (model.Order, error) {
	account, err := res.account(o.Account)
	if err != nil {
		return model.Order{}, err
	}
	return orderModelFromAccount(o, account)
}

func orderModelFromAccount(o domain.Order, account param.AccountID) (model.Order, error) {
	base, err := newAsset(o.BaseAsset)
	if err != nil {
		return model.Order{}, err
	}
	quote, err := newAsset(o.QuoteAsset)
	if err != nil {
		return model.Order{}, err
	}
	side, err := orderSide(o.Side)
	if err != nil {
		return model.Order{}, err
	}
	amount, err := tradeAmountFrom(o.AmountKind, o.AmountValue)
	if err != nil {
		return model.Order{}, err
	}

	order := model.NewOrder()
	view := order.EnsureOperationView()
	view.SetInstrument(param.NewInstrument(base, quote))
	view.SetAccountID(account)
	view.SetSide(side)
	view.SetTradeAmount(amount)
	if o.Price != "" {
		price, perr := param.NewPriceFromString(o.Price)
		if perr != nil {
			return model.Order{}, fmt.Errorf(
				"engine: order price %q: %w: %w", o.Price, perr, domain.ErrInvalid)
		}
		view.SetPrice(price)
	}
	return order, nil
}

// orderSide maps a domain order side onto a param.Side.
func orderSide(side domain.OrderSide) (param.Side, error) {
	switch side {
	case domain.OrderSideBuy:
		return param.SideBuy, nil
	case domain.OrderSideSell:
		return param.SideSell, nil
	default:
		return 0, fmt.Errorf("engine: unknown order side %q: %w", side, domain.ErrInvalid)
	}
}

// tradeAmountFrom maps a domain amount kind and value onto a param.TradeAmount.
func tradeAmountFrom(kind domain.OrderAmountKind, value string) (param.TradeAmount, error) {
	switch kind {
	case domain.OrderAmountKindQuantity:
		q, err := param.NewQuantityFromString(value)
		if err != nil {
			return param.TradeAmount{}, fmt.Errorf(
				"engine: order quantity %q: %w: %w", value, err, domain.ErrInvalid)
		}
		return param.NewQuantityTradeAmount(q), nil
	case domain.OrderAmountKindVolume:
		v, err := param.NewVolumeFromString(value)
		if err != nil {
			return param.TradeAmount{}, fmt.Errorf(
				"engine: order volume %q: %w: %w", value, err, domain.ErrInvalid)
		}
		return param.NewVolumeTradeAmount(v), nil
	default:
		return param.TradeAmount{}, fmt.Errorf(
			"engine: unknown order amount kind %q: %w", kind, domain.ErrInvalid)
	}
}

// orderRejectsFrom maps a binding reject slice onto domain order rejects.
func orderRejectsFrom(rejects []reject.Reject) []domain.OrderReject {
	out := make([]domain.OrderReject, 0, len(rejects))
	for _, r := range rejects {
		out = append(out, domain.OrderReject{
			Code:    rejectCodeName(r.Code),
			Scope:   rejectScopeName(r.Scope),
			Policy:  r.Policy,
			Reason:  sanitizeText(r.Reason),
			Details: sanitizeText(r.Details),
		})
	}
	return out
}

// --- execution report translation -------------------------------------------

// executionReportFrom maps a domain execution-report input onto a
// model.ExecutionReport: the operation (instrument/account/side) and the fill
// (last trade price+quantity, leaves quantity, the terminal-status flag, and the
// pre-trade lock - the input's own lock when it carries one, else one
// reconstructed from the single reference price). The engine requires leaves
// quantity and the terminal flag to settle the fill; an empty or invalid leaves
// quantity is caller error (ErrInvalid). The account is resolved to its stored
// engine id.
func executionReportFrom(in domain.ExecutionReportInput, res idResolver) (model.ExecutionReport, error) {
	account, err := res.account(in.Account)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	return executionReportFromAccount(in, account)
}

func executionReportFromAccount(
	in domain.ExecutionReportInput, account param.AccountID,
) (model.ExecutionReport, error) {
	base, err := newAsset(in.BaseAsset)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	quote, err := newAsset(in.QuoteAsset)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	side, err := orderSide(in.Side)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	leaves, err := param.NewQuantityFromString(in.LeavesQuantity)
	if err != nil {
		return model.ExecutionReport{}, fmt.Errorf(
			"engine: leaves quantity %q: %w: %w", in.LeavesQuantity, err, domain.ErrInvalid)
	}

	hasFill, err := executionReportHasFill(in)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	targetStatus := domain.ExecutionReportTargetStatus(in)
	if !hasFill && (targetStatus == domain.OrderStatusFilled ||
		targetStatus == domain.OrderStatusPartiallyFilled) {
		return model.ExecutionReport{}, fmt.Errorf(
			"engine: fill status %q requires fill price and quantity: %w",
			targetStatus, domain.ErrInvalid)
	}

	lockBytes, err := executionReportLockBytes(in)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	isFinal := domain.OrderStatusTerminal(targetStatus)

	report := model.NewExecutionReport()
	op := report.EnsureOperationView()
	op.SetInstrument(param.NewInstrument(base, quote))
	op.SetAccountID(account)
	op.SetSide(side)

	fill := report.EnsureFillView()
	if hasFill {
		price, err := param.NewPriceFromString(in.FillPrice)
		if err != nil {
			return model.ExecutionReport{}, fmt.Errorf(
				"engine: fill price %q: %w: %w", in.FillPrice, err, domain.ErrInvalid)
		}
		quantity, err := param.NewQuantityFromString(in.FillQuantity)
		if err != nil {
			return model.ExecutionReport{}, fmt.Errorf(
				"engine: fill quantity %q: %w: %w", in.FillQuantity, err, domain.ErrInvalid)
		}
		fill.SetLastTrade(model.NewExecutionReportTrade(price, quantity))
	}
	// Commission is execution-report payload, not a LastTrade attribute. Keep
	// this outside hasFill so commission-only corrections reach SpotFunds.
	if in.Commission != nil {
		commission, err := commissionFrom(*in.Commission)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		fill.SetFee(commission)
	}
	fill.SetLeavesQuantity(leaves)
	fill.SetIsFinal(isFinal)
	if lockBytes != nil {
		fill.SetLock(lockBytes)
	}
	return report, nil
}

func executionReportHasFill(in domain.ExecutionReportInput) (bool, error) {
	hasQuantity := in.FillQuantity != ""
	hasPrice := in.FillPrice != ""
	if hasQuantity != hasPrice {
		return false, fmt.Errorf(
			"engine: fill price and quantity must be provided together: %w",
			domain.ErrInvalid)
	}
	return hasQuantity, nil
}

func executionReportPersistenceFrom(
	in domain.ExecutionReportInput,
	blocks []domain.ExecutionAccountBlock,
	outcomes []BalanceOutcome,
	accountPnl string,
	accountPnlHaltReason domain.PnlHaltReason,
) engine.ExecutionReportPersistence {
	recordedLeaves := domain.ExecutionReportPersistedLeaves(in)
	// The payload holds one reject slot while the engine may report several
	// blocks, so it carries none of them: every block travels whole in Blocks.
	// Filling the slot would mean picking one block and naming a scope the block
	// record does not carry.
	payload := domain.OrderEventPayload{
		FillQuantity:   in.FillQuantity,
		FillPrice:      in.FillPrice,
		FillLockPrice:  in.LockPrice,
		LeavesQuantity: in.LeavesQuantity,
		OrderStatus:    string(in.OrderStatus),
		Commission:     in.Commission,
	}

	events := make([]domain.OrderEvent, 0, 2)
	hasFill := in.FillQuantity != "" && in.FillPrice != ""
	if hasFill {
		events = append(events, domain.OrderEvent{
			Order:   in.Order,
			Type:    domain.OrderEventFill,
			Payload: payload,
		})
	}
	if eventType, ok := domain.ExecutionReportStatusChangeEvent(in.OrderStatus); ok {
		events = append(events, domain.OrderEvent{
			Order:   in.Order,
			Type:    eventType,
			Payload: payload,
		})
	}

	var trade *domain.Trade
	if hasFill {
		trade = &domain.Trade{
			Order:      in.Order,
			Account:    in.Account,
			BaseAsset:  in.BaseAsset,
			QuoteAsset: in.QuoteAsset,
			Side:       in.Side,
			Quantity:   in.FillQuantity,
			Price:      in.FillPrice,
			LockPrice:  in.LockPrice,
			Commission: in.Commission,
		}
	}

	return engine.ExecutionReportPersistence{
		Trade:                trade,
		Commission:           in.Commission,
		OrderStatus:          in.OrderStatus,
		AccountPnl:           accountPnl,
		AccountPnlHaltReason: accountPnlHaltReason,
		Leaves:               recordedLeaves,
		Balances:             executionBalanceSettlementsFrom(outcomes),
		Events:               events,
		Blocks:               blocks,
	}
}

func executionBalanceSettlementsFrom(outcomes []BalanceOutcome) []domain.BalanceSettlement {
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

func sdkFeeFromContractAmount(field, s string) (param.Fee, error) {
	fee, err := param.NewFeeFromString(s)
	if err != nil {
		return param.Fee{}, fmt.Errorf("engine: %s %q: %w: %w", field, s, err, domain.ErrInvalid)
	}
	fee, err = fee.CheckedNeg()
	if err != nil {
		return param.Fee{}, fmt.Errorf("engine: %s %q: %w: %w", field, s, err, domain.ErrInvalid)
	}
	return fee, nil
}

func commissionFrom(c domain.Commission) (param.MonetaryAmount, error) {
	amount, err := sdkFeeFromContractAmount("commission amount", c.Amount)
	if err != nil {
		return param.MonetaryAmount{}, err
	}
	currency, err := newAsset(c.Currency)
	if err != nil {
		return param.MonetaryAmount{}, fmt.Errorf(
			"engine: commission currency %q: %w", c.Currency, err)
	}
	return param.NewMonetaryAmount(amount, currency), nil
}

// immediateFillQuantity resolves the base quantity an immediate fill settles.
// For a quantity order it is the order amount; for a volume order it is the
// volume converted to base quantity at the settlement price (Volume divided by
// price). An empty settlement price on a volume order is an error: a fill cannot
// be sized without a price.
func immediateFillQuantity(o domain.Order, settlementPrice string) (string, error) {
	switch o.AmountKind {
	case domain.OrderAmountKindQuantity:
		return o.AmountValue, nil
	case domain.OrderAmountKindVolume:
		if settlementPrice == "" {
			return "", fmt.Errorf(
				"engine: cannot size volume fill without a settlement price: %w",
				domain.ErrInvalid)
		}
		volume, err := param.NewVolumeFromString(o.AmountValue)
		if err != nil {
			return "", fmt.Errorf(
				"engine: order volume %q: %w: %w", o.AmountValue, err, domain.ErrInvalid)
		}
		price, err := param.NewPriceFromString(settlementPrice)
		if err != nil {
			return "", fmt.Errorf(
				"engine: settlement price %q: %w: %w", settlementPrice, err, domain.ErrInvalid)
		}
		quantity, err := volume.CalculateQuantity(price)
		if err != nil {
			return "", fmt.Errorf("engine: size volume fill: %w", err)
		}
		return quantity.String(), nil
	default:
		return "", fmt.Errorf(
			"engine: unknown order amount kind %q: %w", o.AmountKind, domain.ErrInvalid)
	}
}

// fillLockBytes reconstructs a default-group pre-trade lock carrying one
// reference price and returns the in-process bytes an execution-report fill
// hands to the binding via fill.SetLock. An empty price yields a nil slice (no
// lock attached). This is NOT the persistence path: the fill lock is consumed by
// the binding within the same call, so Lock.Bytes() (the in-process layout) is
// correct here, whereas the durable order/reservation lock is serialized through
// the lock seam (marshalLock).
//
// It is the last-resort fallback for reports that carry no lock of their own.
// The reconstruction holds a single default-policy-group entry, so it cannot
// stand in for an engine lock that recorded other policy groups: a caller that
// has the engine's lock must pass it through instead.
func fillLockBytes(lockPrice string) ([]byte, error) {
	if lockPrice == "" {
		return nil, nil
	}
	price, err := param.NewPriceFromString(lockPrice)
	if err != nil {
		return nil, fmt.Errorf("engine: lock price %q: %w: %w", lockPrice, err, domain.ErrInvalid)
	}
	lock, err := pretrade.NewLockFromEntries([]pretrade.Entry{
		{PolicyGroupID: model.DefaultPolicyGroupID, Price: price},
	})
	if err != nil {
		return nil, fmt.Errorf("engine: build fill lock: %w", err)
	}
	return lock.Bytes(), nil
}

// executionReportLockBytes resolves the lock a report hands to the binding. A
// carried lock is the engine's own artifact and is passed through verbatim
// (decoded from the durable seam encoding into the in-process layout); only a
// report without one falls back to the single-price reconstruction.
func executionReportLockBytes(in domain.ExecutionReportInput) ([]byte, error) {
	if len(in.Lock) > 0 {
		lock, err := unmarshalLock(in.Lock)
		if err != nil {
			return nil, fmt.Errorf("engine: execution report lock: %w: %w", err, domain.ErrInvalid)
		}
		return lock.Bytes(), nil
	}
	return fillLockBytes(in.LockPrice)
}

// executionBlocksFrom maps the engine-recorded account blocks of a post-trade
// result onto domain blocks, stamping each with account (the binding's block
// record does not name the account).
func executionBlocksFrom(
	blocks []reject.AccountBlock, account domain.AccountID,
) []domain.ExecutionAccountBlock {
	out := make([]domain.ExecutionAccountBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, domain.ExecutionAccountBlock{
			Account: account,
			Policy:  sanitizeText(b.Policy),
			Code:    rejectCodeName(b.Code),
			Reason:  sanitizeText(b.Reason),
			Details: sanitizeText(b.Details),
		})
	}
	return out
}

// policyConfigurationBlocksFrom maps blocks emitted while configuring one
// account's policy state. Configuration blocks name neither the Officer account
// nor the policy update's target in the generic binding result, so the adapter
// carries both across the engine boundary.
func policyConfigurationBlocksFrom(
	blocks []reject.AccountBlock, account domain.AccountID, policy string,
) []domain.AccountBlock {
	out := make([]domain.AccountBlock, 0, len(blocks))
	for _, b := range blocks {
		blockPolicy := sanitizeText(b.Policy)
		if blockPolicy == "" {
			blockPolicy = policy
		}
		out = append(out, domain.AccountBlock{
			Account: account,
			Policy:  blockPolicy,
			Code:    rejectCodeName(b.Code),
			Reason:  sanitizeText(b.Reason),
			Details: sanitizeText(b.Details),
		})
	}
	return out
}

// accountBlockFrom maps a dry-run report's would-be account block onto a domain
// block, stamping it with account (the binding's block record does not name the
// account). It is the single-block counterpart of executionBlocksFrom, used by
// the pre-trade dry-run.
//
// block (report.AccountBlock()) is non-nil only when an account-scope reject
// would have latched a fresh block during this dry-run, e.g. a kill-switch
// policy tripping on the probe. A nil block is the engine's answer - nothing
// would latch - and is returned as nil: an account that is already kill-switched
// re-emits its standing block as an account-scope reject, which reaches the
// caller through the report's reject list, so there is nothing here to derive.
func accountBlockFrom(
	block *reject.AccountBlock, account domain.AccountID,
) *domain.ExecutionAccountBlock {
	if block == nil {
		return nil
	}
	return &domain.ExecutionAccountBlock{
		Account: account,
		Policy:  sanitizeText(block.Policy),
		Code:    rejectCodeName(block.Code),
		Reason:  sanitizeText(block.Reason),
		Details: sanitizeText(block.Details),
	}
}

// --- reject code/scope naming -----------------------------------------------

// sanitizeText coerces a binding-supplied reject/block string to valid,
// printable UTF-8: invalid byte sequences are dropped and control characters
// (including the C1 range) are stripped, preserving normal text and spaces. It
// is applied defensively at the officer boundary to every reject/block
// Reason/Details the engine surfaces.
//
// Root cause is upstream: the SDK Go binding returns garbage (non-UTF-8) reason/
// details for an account-blocked (Engine-scope) dry-run reject, so the control
// plane scrubs them here rather than render mojibake. An empty result is fine
// when the field was entirely garbage.
//
// Encoding is the only judgement made here. Whatever survives the scrub is the
// engine's own wording and is transcribed as is, including short values such as
// "0.5", ">10" or "-5%" that carry the whole answer.
func sanitizeText(s string) string {
	if s == "" {
		return ""
	}
	valid := strings.ToValidUTF8(s, "")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, valid))
}

// rejectCodeName maps a binding reject code onto a stable lower-snake string for
// the control plane's persisted records. Unknown codes fall back to a numeric
// "code_<n>" form so a new SDK code never loses information.
func rejectCodeName(code reject.Code) string {
	if name, ok := rejectCodeNames[code]; ok {
		return name
	}
	return fmt.Sprintf("code_%d", uint32(code))
}

// rejectScopeName maps a binding reject scope onto a stable string.
func rejectScopeName(scope reject.Scope) string {
	switch scope {
	case reject.ScopeOrder:
		return "order"
	case reject.ScopeAccount:
		return "account"
	default:
		return fmt.Sprintf("scope_%d", uint8(scope))
	}
}

// rejectCodeNames is the stable name table for the binding's predefined reject
// codes. It is the persisted vocabulary, decoupled from the numeric enum values
// so a code reorder in the SDK cannot silently change stored strings.
var rejectCodeNames = map[reject.Code]string{
	reject.CodeMissingRequiredField:            "missing_required_field",
	reject.CodeInvalidFieldFormat:              "invalid_field_format",
	reject.CodeInvalidFieldValue:               "invalid_field_value",
	reject.CodeUnsupportedOrderType:            "unsupported_order_type",
	reject.CodeUnsupportedTimeInForce:          "unsupported_time_in_force",
	reject.CodeUnsupportedOrderAttribute:       "unsupported_order_attribute",
	reject.CodeDuplicateClientOrderID:          "duplicate_client_order_id",
	reject.CodeTooLateToEnter:                  "too_late_to_enter",
	reject.CodeExchangeClosed:                  "exchange_closed",
	reject.CodeUnknownInstrument:               "unknown_instrument",
	reject.CodeUnknownAccount:                  "unknown_account",
	reject.CodeUnknownVenue:                    "unknown_venue",
	reject.CodeUnknownClearingAccount:          "unknown_clearing_account",
	reject.CodeUnknownCollateralAsset:          "unknown_collateral_asset",
	reject.CodeInsufficientFunds:               "insufficient_funds",
	reject.CodeInsufficientMargin:              "insufficient_margin",
	reject.CodeInsufficientPosition:            "insufficient_position",
	reject.CodeCreditLimitExceeded:             "credit_limit_exceeded",
	reject.CodeRiskLimitExceeded:               "risk_limit_exceeded",
	reject.CodeOrderExceedsLimit:               "order_exceeds_limit",
	reject.CodeOrderQtyExceedsLimit:            "order_qty_exceeds_limit",
	reject.CodeOrderNotionalExceedsLimit:       "order_notional_exceeds_limit",
	reject.CodePositionLimitExceeded:           "position_limit_exceeded",
	reject.CodeConcentrationLimitExceeded:      "concentration_limit_exceeded",
	reject.CodeLeverageLimitExceeded:           "leverage_limit_exceeded",
	reject.CodeRateLimitExceeded:               "rate_limit_exceeded",
	reject.CodePnlKillSwitchTriggered:          "pnl_kill_switch_triggered",
	reject.CodeAccountBlocked:                  "account_blocked",
	reject.CodeAccountNotAuthorized:            "account_not_authorized",
	reject.CodeComplianceRestriction:           "compliance_restriction",
	reject.CodeInstrumentRestricted:            "instrument_restricted",
	reject.CodeJurisdictionRestriction:         "jurisdiction_restriction",
	reject.CodeWashTradePrevention:             "wash_trade_prevention",
	reject.CodeSelfMatchPrevention:             "self_match_prevention",
	reject.CodeShortSaleRestriction:            "short_sale_restriction",
	reject.CodeRiskConfigurationMissing:        "risk_configuration_missing",
	reject.CodeReferenceDataUnavailable:        "reference_data_unavailable",
	reject.CodeOrderValueCalculationFailed:     "order_value_calculation_failed",
	reject.CodeSystemUnavailable:               "system_unavailable",
	reject.CodeMarkPriceUnavailable:            "mark_price_unavailable",
	reject.CodeAccountAdjustmentBoundsExceeded: "account_adjustment_bounds_exceeded",
	reject.CodeArithmeticOverflow:              "arithmetic_overflow",
	reject.CodeCustom:                          "custom",
	reject.CodeOther:                           "other",
}
