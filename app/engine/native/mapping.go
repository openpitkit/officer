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
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pkg/optional"
	"go.openpit.dev/openpit/pretrade/policies"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
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
	// Resolver asset ids are copied with Safe; Unsafe borrows finalizer-backed
	// C memory and must not back resolver comparisons or map keys.
	assets       map[string]param.Asset
	assetIDs     map[domain.EngineAssetID]param.Asset
	assetAliases map[string]string
	groups       map[string]param.AccountGroupID
	groupAliases map[uint32]string
}

// newIDResolver builds the resolver from the snapshot's complete dictionary,
// converting persisted account and group ids with the binding's integer
// constructors and constructing each asset once from its decimal engine id. An
// unassigned or out-of-range persisted id is corruption of our own dictionary
// and aborts the build.
func newIDResolver(
	accounts []domain.Account,
	groups []domain.AccountGroup,
	assets []domain.Asset,
) (idResolver, error) {
	r := newEmptyIDResolver(len(accounts), len(groups), len(assets))
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
	for _, asset := range assets {
		if err := r.addAssetResolverEntry(asset); err != nil {
			return idResolver{}, err
		}
	}
	return r, nil
}

func newEmptyIDResolver(accountCount, groupCount, assetCount int) idResolver {
	return idResolver{shared: &idResolverState{
		accounts:       make(map[domain.AccountID]param.AccountID, accountCount),
		accountAliases: make(map[uint64]domain.AccountID, accountCount),
		assets:         make(map[string]param.Asset, assetCount),
		assetIDs:       make(map[domain.EngineAssetID]param.Asset, assetCount),
		assetAliases:   make(map[string]string, assetCount),
		groups:         make(map[string]param.AccountGroupID, groupCount),
		groupAliases:   make(map[uint32]string, groupCount),
	}}
}

func (r *idResolver) ensureInitialized() {
	if r.shared == nil {
		*r = newEmptyIDResolver(0, 0, 0)
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

func (r idResolver) accountAlias(id param.AccountID) (domain.AccountID, error) {
	if r.shared == nil {
		return "", unknownAccountIDError(id)
	}
	r.shared.mu.RLock()
	code, ok := r.shared.accountAliases[uint64(id.Handle())]
	r.shared.mu.RUnlock()
	if !ok {
		return "", unknownAccountIDError(id)
	}
	return code, nil
}

// asset resolves a human asset code to the immutable ready asset held by the
// adapter. An unknown code is caller input and therefore wraps domain.ErrInvalid.
func (r idResolver) asset(code string) (param.Asset, error) {
	if r.shared == nil {
		return param.Asset{}, unknownAssetAliasError(code)
	}
	r.shared.mu.RLock()
	asset, ok := r.shared.assets[code]
	r.shared.mu.RUnlock()
	if !ok {
		return param.Asset{}, unknownAssetAliasError(code)
	}
	return asset, nil
}

// assetByID resolves an immutable asset id to the ready asset constructed when
// the dictionary was published. Quote routing uses it to avoid any code lookup
// on the hot path.
func (r idResolver) assetByID(id domain.EngineAssetID) (param.Asset, error) {
	if r.shared == nil {
		return param.Asset{}, unknownAssetIDError(id)
	}
	r.shared.mu.RLock()
	asset, ok := r.shared.assetIDs[id]
	r.shared.mu.RUnlock()
	if !ok {
		return param.Asset{}, unknownAssetIDError(id)
	}
	return asset, nil
}

// assetAlias translates an asset returned by the engine back to its human code.
// A missing entry is our corrupt published dictionary, not caller input.
func (r idResolver) assetAlias(asset param.Asset) (string, error) {
	if r.shared == nil {
		return "", unknownAssetReadyIDError(asset)
	}
	r.shared.mu.RLock()
	code, ok := r.shared.assetAliases[asset.Safe()]
	r.shared.mu.RUnlock()
	if !ok {
		return "", unknownAssetReadyIDError(asset)
	}
	return code, nil
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

func unknownAccountIDError(id param.AccountID) error {
	return fmt.Errorf("engine: unknown account engine id %d", id.Handle())
}

func unknownAssetAliasError(code string) error {
	return fmt.Errorf("engine: unknown asset resolver alias %q: %w", code, domain.ErrInvalid)
}

func unknownAssetIDError(id domain.EngineAssetID) error {
	return fmt.Errorf("engine: unknown asset engine id %d: %w", id, marketdata.ErrUnknownAsset)
}

func unknownAssetReadyIDError(id param.Asset) error {
	return fmt.Errorf("engine: unknown asset engine id %q", id.String())
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

func (r idResolver) addAssetResolverEntry(asset domain.Asset) error {
	ready, err := engineAsset(asset.EngineAssetID, asset.Code)
	if err != nil {
		return err
	}
	if r.shared == nil {
		return fmt.Errorf("engine: asset resolver is not initialized")
	}

	id := ready.Safe()
	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()
	if _, exists := r.shared.assets[asset.Code]; exists {
		return fmt.Errorf(
			"engine: asset resolver alias %q already exists: %w",
			asset.Code, domain.ErrAlreadyExists,
		)
	}
	if alias, exists := r.shared.assetAliases[id]; exists {
		return fmt.Errorf(
			"engine: asset engine id %d already belongs to resolver alias %q: %w",
			asset.EngineAssetID, alias, domain.ErrInvalid,
		)
	}
	if _, exists := r.shared.assetIDs[asset.EngineAssetID]; exists {
		return fmt.Errorf(
			"engine: asset engine id %d already exists: %w",
			asset.EngineAssetID, domain.ErrInvalid,
		)
	}
	r.shared.assets[asset.Code] = ready
	r.shared.assetIDs[asset.EngineAssetID] = ready
	r.shared.assetAliases[id] = asset.Code
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

func (r idResolver) renameAssetResolverEntry(oldCode string, asset domain.Asset) error {
	if err := domain.ValidateEngineAssetID(asset.EngineAssetID); err != nil {
		return fmt.Errorf("engine: asset %q engine id: %w", asset.Code, err)
	}
	if r.shared == nil {
		return fmt.Errorf("engine: asset resolver is not initialized")
	}

	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()
	current, exists := r.shared.assets[oldCode]
	if !exists {
		return unknownAssetAliasError(oldCode)
	}
	id := current.Safe()
	if id != strconv.FormatUint(asset.EngineAssetID.Uint64(), 10) {
		return fmt.Errorf(
			"engine: asset resolver alias %q has engine id %q, target %q has %d: %w",
			oldCode, id, asset.Code, asset.EngineAssetID, domain.ErrInvalid,
		)
	}
	if oldCode == asset.Code {
		return nil
	}
	if _, exists := r.shared.assets[asset.Code]; exists {
		return fmt.Errorf(
			"engine: asset resolver alias %q already exists: %w",
			asset.Code, domain.ErrAlreadyExists,
		)
	}
	if alias, exists := r.shared.assetAliases[id]; !exists || alias != oldCode {
		return fmt.Errorf(
			"engine: asset resolver alias %q has corrupt engine id mapping: %w",
			oldCode, domain.ErrInvalid,
		)
	}

	delete(r.shared.assets, oldCode)
	r.shared.assets[asset.Code] = current
	r.shared.assetAliases[id] = asset.Code
	return nil
}

func (r idResolver) removeAssetResolverEntry(asset domain.Asset) error {
	if err := domain.ValidateEngineAssetID(asset.EngineAssetID); err != nil {
		return fmt.Errorf("engine: asset %q engine id: %w", asset.Code, err)
	}
	if r.shared == nil {
		return fmt.Errorf("engine: asset resolver is not initialized")
	}

	expectedID := strconv.FormatUint(asset.EngineAssetID.Uint64(), 10)
	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()
	current, exists := r.shared.assets[asset.Code]
	if !exists {
		return unknownAssetAliasError(asset.Code)
	}
	currentID := current.Safe()
	if currentID != expectedID {
		return fmt.Errorf(
			"engine: asset resolver alias %q has engine id %q, removal target has %d: %w",
			asset.Code, currentID, asset.EngineAssetID, domain.ErrInvalid,
		)
	}
	if alias, exists := r.shared.assetAliases[expectedID]; !exists || alias != asset.Code {
		return fmt.Errorf(
			"engine: asset resolver alias %q has corrupt engine id mapping: %w",
			asset.Code, domain.ErrInvalid,
		)
	}

	delete(r.shared.assets, asset.Code)
	delete(r.shared.assetIDs, asset.EngineAssetID)
	delete(r.shared.assetAliases, expectedID)
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

// engineAsset converts a persisted asset surrogate into the decimal opaque
// asset string accepted by the engine. It is called only while publishing an
// entry, so the C-backed param.Asset is constructed once per asset.
func engineAsset(id domain.EngineAssetID, code string) (param.Asset, error) {
	if err := domain.ValidateEngineAssetID(id); err != nil {
		return param.Asset{}, fmt.Errorf("engine: asset %q engine id: %w", code, err)
	}
	asset, err := param.NewAsset(strconv.FormatUint(id.Uint64(), 10))
	if err != nil {
		return param.Asset{}, fmt.Errorf("engine: asset %q engine id %d: %w", code, id, err)
	}
	return asset, nil
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
			asset, err := res.asset(limit.Asset)
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
			asset, err := res.asset(limit.Asset)
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
	assetIndexes := make(map[string]int)
	accountAssets := []policies.OrderSizeAccountAssetBarrier{}
	type accountAssetKey struct {
		account param.AccountID
		asset   string
	}
	accountAssetIndexes := make(map[accountAssetKey]int)

	for _, limit := range limits {
		if err := limit.Validate(); err != nil {
			return nil, nil, nil, fmt.Errorf(
				"engine: invalid order_size_limit: %w", err,
			)
		}
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
		case domain.ScopeUnderlyingAsset, domain.ScopeSettlementAsset:
			asset, err := res.asset(limit.Asset)
			if err != nil {
				return nil, nil, nil, err
			}
			assetKey := asset.Safe()
			if index, ok := assetIndexes[assetKey]; ok {
				merged, err := mergeOrderSizeValue(
					assets[index].Limit, size, fmt.Sprintf("asset %q", limit.Asset),
				)
				if err != nil {
					return nil, nil, nil, err
				}
				assets[index].Limit = merged
				break
			}
			assetIndexes[assetKey] = len(assets)
			assets = append(assets, policies.OrderSizeAssetBarrier{
				Limit: size,
				Asset: asset,
			})
		case domain.ScopeAccountUnderlyingAsset,
			domain.ScopeAccountSettlementAsset:
			account, err := res.account(limit.Account)
			if err != nil {
				return nil, nil, nil, err
			}
			asset, err := res.asset(limit.Asset)
			if err != nil {
				return nil, nil, nil, err
			}
			key := accountAssetKey{account: account, asset: asset.Safe()}
			if index, ok := accountAssetIndexes[key]; ok {
				merged, err := mergeOrderSizeValue(
					accountAssets[index].Limit,
					size,
					fmt.Sprintf("account %q asset %q", limit.Account, limit.Asset),
				)
				if err != nil {
					return nil, nil, nil, err
				}
				accountAssets[index].Limit = merged
				break
			}
			accountAssetIndexes[key] = len(accountAssets)
			accountAssets = append(accountAssets, policies.OrderSizeAccountAssetBarrier{
				Limit:     size,
				AccountID: account,
				Asset:     asset,
			})
		default:
			return nil, nil, nil, fmt.Errorf(
				"engine: order_size_limit unsupported scope %q", limit.Scope)
		}
	}
	return broker, assets, accountAssets, nil
}

// spotFundsPnlBoundsAxes maps currency-valued SpotFunds P&L bounds onto the
// runtime Configure axes. Every returned slice is non-nil so Configure replaces
// all axes wholesale.
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
		barrier, err := spotFundsPnlBoundsBarrier(limit, res)
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
			asset, err := res.asset(limit.Asset)
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
			asset, err := res.asset(limit.Asset)
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
	broker, assets, accountAssets, err := orderSizeAxes(limits, res)
	if err != nil {
		return nil, err
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
func orderSizeValue(limit domain.LimitOrderSize) (policies.OrderSizeLimit, error) {
	out := policies.OrderSizeLimit{
		MaxQuantity: optional.None[param.Quantity](),
		MaxNotional: optional.None[param.Volume](),
	}

	if limit.MaxQuantity != "" {
		q, err := param.NewQuantityFromString(limit.MaxQuantity)
		if err != nil {
			return policies.OrderSizeLimit{}, fmt.Errorf(
				"engine: order_size_limit max_quantity %q: %w", limit.MaxQuantity, err)
		}
		out.MaxQuantity = optional.Some(q)
	}
	if limit.MaxNotional != "" {
		n, err := param.NewVolumeFromString(limit.MaxNotional)
		if err != nil {
			return policies.OrderSizeLimit{}, fmt.Errorf(
				"engine: order_size_limit max_notional %q: %w", limit.MaxNotional, err)
		}
		out.MaxNotional = optional.Some(n)
	}
	return out, nil
}

// mergeOrderSizeValue combines complementary caps on one SDK barrier key and
// rejects the same metric appearing twice on that key.
func mergeOrderSizeValue(
	current, incoming policies.OrderSizeLimit, key string,
) (policies.OrderSizeLimit, error) {
	if current.MaxQuantity.IsSet() && incoming.MaxQuantity.IsSet() {
		return policies.OrderSizeLimit{}, fmt.Errorf(
			"engine: duplicate order_size_limit max_quantity for %s: %w",
			key,
			domain.ErrInvalid,
		)
	}
	if current.MaxNotional.IsSet() && incoming.MaxNotional.IsSet() {
		return policies.OrderSizeLimit{}, fmt.Errorf(
			"engine: duplicate order_size_limit max_notional for %s: %w",
			key,
			domain.ErrInvalid,
		)
	}
	if incoming.MaxQuantity.IsSet() {
		current.MaxQuantity = incoming.MaxQuantity
	}
	if incoming.MaxNotional.IsSet() {
		current.MaxNotional = incoming.MaxNotional
	}
	return current, nil
}

func spotFundsPnlBoundsBarrier(
	limit domain.LimitSpotFundsPnlBounds,
	res idResolver,
) (policies.SpotFundsPnlBoundsBarrier, error) {
	currency, err := res.asset(limit.Currency)
	if err != nil {
		return policies.SpotFundsPnlBoundsBarrier{}, err
	}
	lower, upper, err := pnlBoundOptions(
		limit.LowerBound,
		limit.UpperBound,
		"spot_funds_pnl_bounds",
	)
	if err != nil {
		return policies.SpotFundsPnlBoundsBarrier{}, err
	}
	return policies.SpotFundsPnlBoundsBarrier{
		Currency:   currency,
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

// --- account adjustment translation ----------------------------------------

// accountAdjustmentFromRequest maps one domain adjustment request onto a single
// model.AccountAdjustment: a balance operation on req.Asset with optional
// average-entry-price, per-field absolute|delta amounts, and optional bounds.
func accountAdjustmentFromRequest(
	req domain.AdjustmentRequest,
	res idResolver,
) (model.AccountAdjustment, error) {
	asset, err := res.asset(req.Asset)
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
	outcomes []accountadjustment.Outcome, asset string, res idResolver,
) (domain.AdjustmentOutcomeAccepted, bool, error) {
	var result domain.AdjustmentOutcomeAccepted
	found := false
	for _, outcome := range outcomes {
		entry := outcome.Entry
		entryAsset, err := res.assetAlias(entry.Asset)
		if err != nil {
			return domain.AdjustmentOutcomeAccepted{}, false, err
		}
		if entryAsset != asset {
			continue
		}
		if found {
			return domain.AdjustmentOutcomeAccepted{}, false, fmt.Errorf(
				"engine: account adjustment returned several outcomes for asset %q", asset)
		}
		accepted, err := outcomeAcceptedFromEntry(entry, res)
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
	res idResolver,
) ([]BalanceOutcome, error) {
	result := make([]BalanceOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		entry := outcome.Entry
		// Empty comparison reads only length, never the C buffer; Safe is unnecessary.
		if entry.Asset.Unsafe() == "" {
			continue
		}
		asset, err := res.assetAlias(entry.Asset)
		if err != nil {
			return nil, err
		}
		accepted, err := outcomeAcceptedFromEntry(entry, res)
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
	res idResolver,
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
			asset, err := res.assetAlias(entry.Asset)
			if err != nil {
				return domain.AdjustmentOutcomeAccepted{}, err
			}
			reason, err := pnlHaltReasonFromSDK(haltReason)
			if err != nil {
				return domain.AdjustmentOutcomeAccepted{}, fmt.Errorf(
					"engine: outcome for asset %q: %w", asset, err,
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

// spotFundsAccountPnlFromList picks the authoritative account P&L the engine
// published for the settling account, as a value and its halt reason.
//
// Precedence and publication are two jobs. An outcome reporting no change ranks
// below one that does, so it never preempts a halt the engine reports for the
// same account afterwards. Its absolute is still the engine's own number: when
// nothing outranks it, it is published, so a realization landing on zero reaches
// storage as "0" instead of absence. An empty value keeps the stored one, which
// is what an unchanged number restates anyway.
func spotFundsAccountPnlFromList(
	account param.AccountID,
	outcomes []accountadjustment.AccountPnlOutcome,
) (string, domain.PnlHaltReason, error) {
	selected := false
	var pnl string
	var unchangedPnl string
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
		amount, computed := outcome.Amount()
		if computed && amount.Delta.IsZero() {
			if unchangedPnl == "" {
				unchangedPnl = amount.Absolute.String()
			}
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
		} else if computed {
			pnl = amount.Absolute.String()
		}
	}
	if !selected {
		return unchangedPnl, "", nil
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
	return orderModelFromAccount(o, account, res)
}

func orderModelFromAccount(
	o domain.Order,
	account param.AccountID,
	res idResolver,
) (model.Order, error) {
	base, err := res.asset(o.BaseAsset)
	if err != nil {
		return model.Order{}, err
	}
	quote, err := res.asset(o.QuoteAsset)
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
// (last trade price+quantity, the recorded reservation remainder, the terminal
// status flag, and the original engine pre-trade lock). Caller-reported leaves
// remain separate for persistence. The account is resolved to its stored
// engine id.
func executionReportFrom(
	in domain.ExecutionReportInput,
	reservationRemainder string,
	res idResolver,
) (model.ExecutionReport, error) {
	account, err := res.account(in.Account)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	return executionReportFromAccount(in, account, reservationRemainder, res)
}

func executionReportFromAccount(
	in domain.ExecutionReportInput,
	account param.AccountID,
	reservationRemainder string,
	res idResolver,
) (model.ExecutionReport, error) {
	base, err := res.asset(in.BaseAsset)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	quote, err := res.asset(in.QuoteAsset)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	side, err := orderSide(in.Side)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	targetStatus := in.OrderStatus
	isFinal := domain.OrderStatusTerminal(targetStatus)
	var remainingReservation *param.Quantity
	if reservationRemainder != "" {
		value, err := param.NewQuantityFromString(reservationRemainder)
		if err != nil {
			return model.ExecutionReport{}, fmt.Errorf(
				"engine: reservation remainder %q: %w",
				reservationRemainder,
				err,
			)
		}
		remainingReservation = &value
	}

	hasFill, err := executionReportHasFill(in)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	if !hasFill && (targetStatus == domain.OrderStatusFilled ||
		targetStatus == domain.OrderStatusPartiallyFilled) {
		return model.ExecutionReport{}, fmt.Errorf(
			"engine: fill status %q requires fill price and quantity: %w",
			targetStatus, domain.ErrInvalid)
	}
	if hasFill && remainingReservation == nil {
		return model.ExecutionReport{}, errors.New(
			"engine: fill report has no reservation remainder")
	}
	lockBytes, err := executionReportLockBytes(in)
	if err != nil {
		return model.ExecutionReport{}, err
	}

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
		commission, err := commissionFrom(*in.Commission, res)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		fill.SetFee(commission)
	}
	if remainingReservation != nil {
		fill.SetRemainingReservedQuantity(*remainingReservation)
	}
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
	} else if in.Commission != nil {
		events = append(events, domain.OrderEvent{
			Order:   in.Order,
			Type:    domain.OrderEventCommission,
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
		Leaves:               in.LeavesQuantity,
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
	return fee, nil
}

func commissionFrom(c domain.Commission, res idResolver) (param.MonetaryAmount, error) {
	amount, err := sdkFeeFromContractAmount("commission amount", c.Amount)
	if err != nil {
		return param.MonetaryAmount{}, err
	}
	currency, err := res.asset(c.Currency)
	if err != nil {
		return param.MonetaryAmount{}, fmt.Errorf(
			"engine: commission currency %q: %w", c.Currency, err)
	}
	return param.NewMonetaryAmount(amount, currency), nil
}

// immediateFillQuantity returns the request quantity for a quantity order and
// the engine's applied base-asset delta for a volume order.
func immediateFillQuantity(
	o domain.Order, outcomes []BalanceOutcome,
) (string, error) {
	switch o.AmountKind {
	case domain.OrderAmountKindQuantity:
		return o.AmountValue, nil
	case domain.OrderAmountKindVolume:
		var fillQuantity string
		for _, outcome := range outcomes {
			if outcome.Asset != o.BaseAsset {
				continue
			}
			var candidate string
			switch o.Side {
			case domain.OrderSideBuy:
				candidate = outcome.Outcome.IncomingDelta
			case domain.OrderSideSell:
				candidate = outcome.Outcome.HeldDelta
			default:
				return "", fmt.Errorf(
					"engine: volume immediate order has unsupported side %q: %w",
					o.Side,
					domain.ErrInvalid,
				)
			}
			if candidate == "" {
				continue
			}
			if fillQuantity != "" {
				return "", fmt.Errorf(
					"engine: volume immediate order has several base-asset deltas for "+
						"asset %q",
					o.BaseAsset,
				)
			}
			fillQuantity = candidate
		}
		if fillQuantity == "" {
			return "", fmt.Errorf(
				"engine: volume immediate order has no base-asset delta for asset %q",
				o.BaseAsset,
			)
		}
		return fillQuantity, nil
	default:
		return "", fmt.Errorf(
			"engine: unsupported immediate order amount kind %q: %w",
			o.AmountKind,
			domain.ErrInvalid,
		)
	}
}

// executionReportLockBytes resolves the engine-produced lock a report hands
// back to the binding. The durable encoding is decoded only to recover the
// binding's in-process bytes; its content is never reconstructed or changed.
func executionReportLockBytes(in domain.ExecutionReportInput) ([]byte, error) {
	if len(in.Lock) == 0 {
		return nil, fmt.Errorf(
			"engine: execution report carries no request or stored order lock: %w",
			domain.ErrInvalid,
		)
	}
	lock, err := unmarshalLock(in.Lock)
	if err != nil {
		return nil, fmt.Errorf("engine: execution report lock: %w: %w", err, domain.ErrInvalid)
	}
	return lock.Bytes(), nil
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

func policyConfigurationBlockOutcomesFrom(
	outcomes []configure.AccountBlockOutcome, res idResolver, policy string,
) ([]domain.AccountBlock, error) {
	blocks := make([]domain.AccountBlock, 0, len(outcomes))
	for _, outcome := range outcomes {
		account, err := res.accountAlias(outcome.AccountID)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, policyConfigurationBlocksFrom(
			[]reject.AccountBlock{outcome.Block}, account, policy,
		)...)
	}
	return blocks, nil
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

// accountBlockCauseFrom restores the SDK account-block value represented by a
// persisted Officer block. The reverse lookup is derived from rejectCodeNames
// so capture and restore cannot drift onto separate code vocabularies.
func accountBlockCauseFrom(block domain.AccountBlock) (reject.AccountBlock, error) {
	code, ok := rejectCodesByName[block.Code]
	if !ok {
		return reject.AccountBlock{}, fmt.Errorf(
			"unknown persisted account block code %q", block.Code,
		)
	}
	return reject.AccountBlock{
		Policy:  block.Policy,
		Code:    code,
		Reason:  block.Reason,
		Details: block.Details,
	}, nil
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
	reject.CodeMissingRequiredField:            domain.RejectCodeMissingRequiredField,
	reject.CodeInvalidFieldFormat:              domain.RejectCodeInvalidFieldFormat,
	reject.CodeInvalidFieldValue:               domain.RejectCodeInvalidFieldValue,
	reject.CodeUnsupportedOrderType:            domain.RejectCodeUnsupportedOrderType,
	reject.CodeUnsupportedTimeInForce:          domain.RejectCodeUnsupportedTimeInForce,
	reject.CodeUnsupportedOrderAttribute:       domain.RejectCodeUnsupportedOrderAttribute,
	reject.CodeDuplicateClientOrderID:          domain.RejectCodeDuplicateClientOrderID,
	reject.CodeTooLateToEnter:                  domain.RejectCodeTooLateToEnter,
	reject.CodeExchangeClosed:                  domain.RejectCodeExchangeClosed,
	reject.CodeUnknownInstrument:               domain.RejectCodeUnknownInstrument,
	reject.CodeUnknownAccount:                  domain.RejectCodeUnknownAccount,
	reject.CodeUnknownVenue:                    domain.RejectCodeUnknownVenue,
	reject.CodeUnknownClearingAccount:          domain.RejectCodeUnknownClearingAccount,
	reject.CodeUnknownCollateralAsset:          domain.RejectCodeUnknownCollateralAsset,
	reject.CodeInsufficientFunds:               domain.RejectCodeInsufficientFunds,
	reject.CodeInsufficientMargin:              domain.RejectCodeInsufficientMargin,
	reject.CodeInsufficientPosition:            domain.RejectCodeInsufficientPosition,
	reject.CodeCreditLimitExceeded:             domain.RejectCodeCreditLimitExceeded,
	reject.CodeRiskLimitExceeded:               domain.RejectCodeRiskLimitExceeded,
	reject.CodeOrderExceedsLimit:               domain.RejectCodeOrderExceedsLimit,
	reject.CodeOrderQtyExceedsLimit:            domain.RejectCodeOrderQtyExceedsLimit,
	reject.CodeOrderNotionalExceedsLimit:       domain.RejectCodeOrderNotionalExceedsLimit,
	reject.CodePositionLimitExceeded:           domain.RejectCodePositionLimitExceeded,
	reject.CodeConcentrationLimitExceeded:      domain.RejectCodeConcentrationLimitExceeded,
	reject.CodeLeverageLimitExceeded:           domain.RejectCodeLeverageLimitExceeded,
	reject.CodeRateLimitExceeded:               domain.RejectCodeRateLimitExceeded,
	reject.CodePnlKillSwitchTriggered:          domain.RejectCodePnlKillSwitchTriggered,
	reject.CodeAccountBlocked:                  domain.RejectCodeAccountBlocked,
	reject.CodeAccountNotAuthorized:            domain.RejectCodeAccountNotAuthorized,
	reject.CodeComplianceRestriction:           domain.RejectCodeComplianceRestriction,
	reject.CodeInstrumentRestricted:            domain.RejectCodeInstrumentRestricted,
	reject.CodeJurisdictionRestriction:         domain.RejectCodeJurisdictionRestriction,
	reject.CodeWashTradePrevention:             domain.RejectCodeWashTradePrevention,
	reject.CodeSelfMatchPrevention:             domain.RejectCodeSelfMatchPrevention,
	reject.CodeShortSaleRestriction:            domain.RejectCodeShortSaleRestriction,
	reject.CodeRiskConfigurationMissing:        domain.RejectCodeRiskConfigurationMissing,
	reject.CodeReferenceDataUnavailable:        domain.RejectCodeReferenceDataUnavailable,
	reject.CodeOrderValueCalculationFailed:     domain.RejectCodeOrderValueCalculationFailed,
	reject.CodeSystemUnavailable:               domain.RejectCodeSystemUnavailable,
	reject.CodeMarkPriceUnavailable:            domain.RejectCodeMarkPriceUnavailable,
	reject.CodeAccountAdjustmentBoundsExceeded: domain.RejectCodeAccountAdjustmentBoundsExceeded,
	reject.CodeArithmeticOverflow:              domain.RejectCodeArithmeticOverflow,
	reject.CodeCustom:                          domain.RejectCodeCustom,
	reject.CodeOther:                           domain.RejectCodeOther,
}

var rejectCodesByName = func() map[string]reject.Code {
	reverse := make(map[string]reject.Code, len(rejectCodeNames))
	for code, name := range rejectCodeNames {
		reverse[name] = code
	}
	return reverse
}()
