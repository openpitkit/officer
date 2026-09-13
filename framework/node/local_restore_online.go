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

package node

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/model"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

type restoreBalanceChainState struct {
	ctx           context.Context
	account       domain.Account
	reqs          []domain.AdjustmentRequest
	adjustments   []model.AccountAdjustment
	results       []engine.AdjustmentResult
	reject        *domain.AdjustmentOutcomeRejected
	err           error
	engineApplied bool
}

// restoreRuntimeSnapshot is the persisted runtime shape on one side of a
// restore. The portable archive supplies fact rows while the store reads retain
// the stable numeric ids which deliberately do not travel in a backup.
type restoreRuntimeSnapshot struct {
	data                backup.Data
	marketDataInstances []domain.MarketDataInstance
	accounts            map[domain.AccountID]domain.Account
	assets              map[string]domain.Asset
	groups              map[string]domain.AccountGroup
}

type restoreRuntimePlan struct {
	rebuild              bool
	reason               string
	runtimeChanged       bool
	assetsChanged        bool
	rateChanged          bool
	orderSizeChanged     bool
	spotFundsChanged     bool
	marketDataChanged    bool
	marketDataNeedsClear bool
	forceAllAccountPnls  bool
}

func (n *localNode) captureRestoreRuntimeSnapshot(
	ctx context.Context,
) (restoreRuntimeSnapshot, backup.Archive, error) {
	archive, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		return restoreRuntimeSnapshot{}, backup.Archive{}, err
	}
	accounts, err := n.realm.ListAccounts(ctx)
	if err != nil {
		return restoreRuntimeSnapshot{}, backup.Archive{}, err
	}
	groups, err := n.realm.ListGroups(ctx)
	if err != nil {
		return restoreRuntimeSnapshot{}, backup.Archive{}, err
	}
	// The reserved default group is a runtime currency tier but ordinary group
	// listings may omit it. Capture it explicitly so a default-currency restore
	// cannot disappear from the before/after delta.
	if defaultGroup, ok, err := n.realm.GetGroup(ctx, ""); err != nil {
		return restoreRuntimeSnapshot{}, backup.Archive{}, err
	} else if ok {
		found := false
		for _, group := range groups {
			found = found || group.Code == ""
		}
		if !found {
			groups = append(groups, defaultGroup)
		}
	}
	assets, err := n.realm.ListAssets(ctx)
	if err != nil {
		return restoreRuntimeSnapshot{}, backup.Archive{}, err
	}
	marketDataInstances, err := n.realm.ListMarketDataInstances(ctx)
	if err != nil {
		return restoreRuntimeSnapshot{}, backup.Archive{}, err
	}
	snapshot := restoreRuntimeSnapshot{
		data:                archive.Data,
		marketDataInstances: marketDataInstances,
		accounts:            make(map[domain.AccountID]domain.Account, len(accounts)),
		assets:              make(map[string]domain.Asset, len(assets)),
		groups:              make(map[string]domain.AccountGroup, len(groups)),
	}
	for _, account := range accounts {
		snapshot.accounts[account.Code] = account
	}
	for _, asset := range assets {
		snapshot.assets[asset.Code] = asset
	}
	for _, group := range groups {
		snapshot.groups[group.Code] = group
	}
	return snapshot, archive, nil
}

func restoreAddsAssets(
	ctx context.Context,
	assets []backup.Asset,
	realm store.RealmStore,
) (bool, error) {
	if len(assets) == 0 {
		return false, nil
	}
	stored, err := realm.ListAssets(ctx)
	if err != nil {
		return false, fmt.Errorf("list stored assets: %w", err)
	}
	existing := make(map[string]struct{}, len(stored))
	for _, asset := range stored {
		existing[asset.Code] = struct{}{}
	}
	for _, asset := range assets {
		if _, ok := existing[asset.Code]; !ok {
			return true, nil
		}
	}
	return false, nil
}

func classifyRestoreRuntimeDelta(
	before restoreRuntimeSnapshot,
	after restoreRuntimeSnapshot,
) restoreRuntimePlan {
	plan := restoreRuntimePlan{}
	for code, account := range before.accounts {
		next, ok := after.accounts[code]
		if !ok {
			plan.rebuild = true
			plan.reason = "restore deletes an account"
			return plan
		}
		if account.EngineAccountID != next.EngineAccountID {
			plan.rebuild = true
			plan.reason = "restore changes a stable account engine id"
			return plan
		}
	}
	accountIDOwners := make(map[domain.EngineAccountID]domain.AccountID, len(before.accounts))
	for code, account := range before.accounts {
		accountIDOwners[account.EngineAccountID] = code
	}
	for code, account := range after.accounts {
		if owner, ok := accountIDOwners[account.EngineAccountID]; ok && owner != code {
			plan.rebuild = true
			plan.reason = "restore reassigns a live account engine id"
			return plan
		}
	}
	for code, group := range before.groups {
		if next, ok := after.groups[code]; ok &&
			group.EngineGroupID != next.EngineGroupID {
			plan.rebuild = true
			plan.reason = "restore changes a stable group engine id"
			return plan
		}
	}
	groupIDOwners := make(map[domain.EngineGroupID]string, len(before.groups))
	for code, group := range before.groups {
		groupIDOwners[group.EngineGroupID] = code
	}
	for code, group := range after.groups {
		if owner, ok := groupIDOwners[group.EngineGroupID]; ok && owner != code {
			plan.rebuild = true
			plan.reason = "restore reassigns a live group engine id"
			return plan
		}
	}

	plan.assetsChanged = len(
		sortedNewAssetCodes(before.data.Assets, after.data.Assets),
	) > 0
	plan.rateChanged = !reflect.DeepEqual(
		rateLimitMap(before.data.RateLimits), rateLimitMap(after.data.RateLimits),
	)
	plan.orderSizeChanged = !reflect.DeepEqual(
		orderSizeLimitMap(before.data.OrderSizeLimits),
		orderSizeLimitMap(after.data.OrderSizeLimits),
	)
	plan.spotFundsChanged = !reflect.DeepEqual(
		spotFundsLimitMap(before.data.SpotFundsPnlBoundsLimits),
		spotFundsLimitMap(after.data.SpotFundsPnlBoundsLimits),
	)
	if plan.rateChanged &&
		(len(before.data.RateLimits) == 0) != (len(after.data.RateLimits) == 0) {
		plan.rebuild = true
		plan.reason = "restore adds or removes the rate-limit policy"
		return plan
	}
	if plan.orderSizeChanged &&
		(len(before.data.OrderSizeLimits) == 0) != (len(after.data.OrderSizeLimits) == 0) {
		plan.rebuild = true
		plan.reason = "restore adds or removes the order-size policy"
		return plan
	}
	plan.marketDataChanged = !reflect.DeepEqual(
		marketDataRuntimeShape(before.marketDataInstances, before.data.MarketDataInstruments),
		marketDataRuntimeShape(after.marketDataInstances, after.data.MarketDataInstruments),
	)
	plan.marketDataNeedsClear = plan.marketDataChanged
	plan.forceAllAccountPnls = plan.spotFundsChanged
	plan.runtimeChanged = restoreRuntimeShapeChanged(before, after) ||
		plan.assetsChanged || plan.rateChanged || plan.orderSizeChanged ||
		plan.spotFundsChanged || plan.marketDataChanged
	return plan
}

func restoreRuntimeShapeChanged(
	before restoreRuntimeSnapshot, after restoreRuntimeSnapshot,
) bool {
	if !reflect.DeepEqual(accountRuntimeMap(before.accounts), accountRuntimeMap(after.accounts)) ||
		!reflect.DeepEqual(groupRuntimeMap(before.groups), groupRuntimeMap(after.groups)) {
		return true
	}
	return !reflect.DeepEqual(
		balanceRuntimeMap(before.data.Balances), balanceRuntimeMap(after.data.Balances),
	)
}

type restoreAccountRuntime struct {
	group        string
	currency     string
	pnl          string
	haltReason   domain.PnlHaltReason
	blockReason  string
	blockPolicy  string
	blockCode    string
	blockDetails string
	blocked      bool
}

func accountRuntimeMap(
	accounts map[domain.AccountID]domain.Account,
) map[domain.AccountID]restoreAccountRuntime {
	out := make(map[domain.AccountID]restoreAccountRuntime, len(accounts))
	for code, account := range accounts {
		out[code] = restoreAccountRuntime{
			group: account.GroupCode, currency: account.Currency,
			pnl: account.Pnl, haltReason: account.PnlHaltReason,
			blocked: account.Blocked, blockReason: account.BlockReason,
			blockPolicy: account.BlockPolicy, blockCode: account.BlockCode,
			blockDetails: account.BlockDetails,
		}
	}
	return out
}

type restoreGroupRuntime struct {
	currency    string
	blockReason string
	blocked     bool
}

func groupRuntimeMap(
	groups map[string]domain.AccountGroup,
) map[string]restoreGroupRuntime {
	out := make(map[string]restoreGroupRuntime, len(groups))
	for code, group := range groups {
		out[code] = restoreGroupRuntime{
			currency: group.Currency, blocked: group.Blocked,
			blockReason: group.BlockReason,
		}
	}
	return out
}

func balanceRuntimeMap(balances []backup.Balance) map[string]domain.Balance {
	out := make(map[string]domain.Balance, len(balances))
	for _, balance := range balances {
		out[restoreBalanceKey(balance.Account, balance.Asset)] = domain.Balance{
			UpdatedAt:             balance.UpdatedAt.UTC(),
			Available:             balance.Available,
			Held:                  balance.Held,
			Incoming:              balance.Incoming,
			RealizedPnl:           balance.RealizedPnl,
			RealizedPnlHaltReason: balance.RealizedPnlHaltReason,
			AverageEntryPrice:     balance.AverageEntryPrice,
			Asset:                 balance.Asset,
			Account:               balance.Account,
		}
	}
	return out
}

func rateLimitMap(limits []domain.LimitRate) map[string]domain.LimitRate {
	out := make(map[string]domain.LimitRate, len(limits))
	for _, limit := range limits {
		out[string(limit.Scope)+"\x00"+limit.Account.String()+"\x00"+limit.Asset] = limit
	}
	return out
}

func orderSizeLimitMap(limits []domain.LimitOrderSize) map[string]domain.LimitOrderSize {
	out := make(map[string]domain.LimitOrderSize, len(limits))
	for _, limit := range limits {
		out[string(limit.Scope)+"\x00"+limit.Account.String()+"\x00"+limit.Asset] = limit
	}
	return out
}

func spotFundsLimitMap(
	limits []domain.LimitSpotFundsPnlBounds,
) map[string]domain.LimitSpotFundsPnlBounds {
	out := make(map[string]domain.LimitSpotFundsPnlBounds, len(limits))
	for _, limit := range limits {
		out[string(limit.Scope)+"\x00"+limit.Account.String()+"\x00"+limit.AccountGroup] = limit
	}
	return out
}

type restoreMarketDataShape struct {
	instances   map[domain.ExternalID]domain.MarketDataInstance
	instruments map[string]backup.MarketDataInstrument
}

func marketDataRuntimeShape(
	instances []domain.MarketDataInstance,
	instruments []backup.MarketDataInstrument,
) restoreMarketDataShape {
	shape := restoreMarketDataShape{
		instances:   make(map[domain.ExternalID]domain.MarketDataInstance, len(instances)),
		instruments: make(map[string]backup.MarketDataInstrument, len(instruments)),
	}
	for _, instance := range instances {
		shape.instances[instance.ExternalID] = instance
	}
	for _, instrument := range instruments {
		shape.instruments[restoreMarketDataKey(instrument.Instance, instrument.ExternalSymbol)] =
			instrument
	}
	return shape
}

func restoreMarketDataKey(instance domain.ExternalID, external string) string {
	return instance.String() + "\x00" + external
}

func restoreBalanceKey(account domain.AccountID, asset string) string {
	return account.String() + "\x00" + asset
}

func (n *localNode) applyRestoreRuntimeDelta(
	ctx context.Context,
	before restoreRuntimeSnapshot,
	after restoreRuntimeSnapshot,
	plan restoreRuntimePlan,
) error {
	eng := n.currentEngine()
	resolver := eng

	for _, code := range sortedNewAssetCodes(before.data.Assets, after.data.Assets) {
		asset, ok, err := n.realm.GetAsset(ctx, code)
		if err != nil {
			return fmt.Errorf("read restored asset %q: %w", code, err)
		}
		if !ok {
			return fmt.Errorf("restored asset %q: %w", code, domain.ErrNotFound)
		}
		if err := resolver.AddAssetResolverEntry(asset); err != nil {
			return fmt.Errorf("publish restored asset %q: %w", code, err)
		}
	}
	for _, code := range sortedNewGroupCodes(before.groups, after.groups) {
		if err := resolver.AddGroupResolverEntry(after.groups[code]); err != nil {
			return fmt.Errorf("publish restored group %q: %w", code, err)
		}
	}
	for _, code := range sortedNewAccountCodes(before.accounts, after.accounts) {
		if err := resolver.AddAccountResolverEntry(after.accounts[code]); err != nil {
			return fmt.Errorf("publish restored account %q: %w", code, err)
		}
	}
	groupFallback := make(map[string]domain.AccountGroup, len(before.groups)+len(after.groups))
	for code, group := range before.groups {
		groupFallback[code] = group
	}
	for code, group := range after.groups {
		groupFallback[code] = group
	}

	for _, code := range sortedGroupCodes(after.groups) {
		previous, existed := before.groups[code]
		next := after.groups[code]
		if existed && previous.Currency == next.Currency {
			continue
		}
		if !existed && next.Currency == "" {
			continue
		}
		if _, err := n.runGroupRuntimeChain(
			ctx,
			eng,
			next,
			true,
			"apply restored group currency",
			&next.Currency,
			nil,
		); err != nil {
			return fmt.Errorf("apply restored group %q currency: %w", code, err)
		}
	}

	for _, code := range sortedAccountCodes(after.accounts) {
		previous, existed := before.accounts[code]
		next := after.accounts[code]
		oldGroup := ""
		if existed {
			oldGroup = previous.GroupCode
		}
		if oldGroup == next.GroupCode {
			continue
		}
		if _, err := n.applyGroupMove(
			ctx, eng, next, oldGroup, next.GroupCode, groupFallback,
		); err != nil {
			return fmt.Errorf("apply restored account %q group: %w", code, err)
		}
	}

	// Currency affects SpotFunds P&L interpretation, so explicit account
	// overrides must be live before restored SpotFunds policy state is applied.
	// Group currencies and membership were applied above, completing the same
	// cascade the cold builder sees.
	for _, code := range sortedAccountCodes(after.accounts) {
		previous, existed := before.accounts[code]
		next := after.accounts[code]
		if existed && previous.Currency == next.Currency {
			continue
		}
		if !existed && next.Currency == "" {
			continue
		}
		if _, _, err := n.runAccountRuntimeChain(
			ctx,
			eng,
			next,
			"apply restored account currency",
			&next.Currency,
			nil,
			nil,
		); err != nil {
			return fmt.Errorf("apply restored account %q currency: %w", code, err)
		}
	}

	var runtimeBlocks []domain.AccountBlock
	type policyBlockSet struct {
		policy string
		blocks []domain.AccountBlock
	}
	var policyBlocks []policyBlockSet
	var spotFundsRuntimeBlocks []domain.AccountBlock
	for _, policy := range []struct {
		name    string
		changed bool
	}{
		{name: domain.PolicyRateLimit, changed: plan.rateChanged},
		{name: domain.PolicyOrderSizeLimit, changed: plan.orderSizeChanged},
		{name: domain.PolicySpotFundsPnlBoundsKillSwitch, changed: plan.spotFundsChanged},
	} {
		if !policy.changed {
			continue
		}
		limits, err := n.policyLimitSet(ctx, policy.name)
		if err != nil {
			return err
		}
		result, err := eng.ConfigurePolicy(ctx, policy.name, limits)
		if err != nil {
			return fmt.Errorf("configure restored policy %q: %w", policy.name, err)
		}
		runtimeBlocks = append(runtimeBlocks, result.AccountBlocks...)
		policyBlocks = append(policyBlocks, policyBlockSet{
			policy: policy.name, blocks: result.AccountBlocks,
		})
	}

	for _, code := range sortedAccountCodes(after.accounts) {
		previous, existed := before.accounts[code]
		next := after.accounts[code]
		pnlChanged := !existed || previous.Pnl != next.Pnl ||
			previous.PnlHaltReason != next.PnlHaltReason || plan.forceAllAccountPnls
		if !pnlChanged {
			continue
		}
		pnl := next.Pnl
		if pnl == "" {
			pnl = "0"
		}
		if next.PnlHaltReason != "" {
			pnl = ""
		}
		blocks, _, err := n.runAccountRuntimeChain(
			ctx,
			eng,
			next,
			"apply restored account pnl state",
			nil,
			&accountRuntimePnl{
				value:       pnl,
				storedValue: next.Pnl,
				haltReason:  next.PnlHaltReason,
			},
			nil,
		)
		if err != nil {
			return fmt.Errorf("apply restored account %q state: %w", code, err)
		}
		runtimeBlocks = append(runtimeBlocks, blocks...)
		spotFundsRuntimeBlocks = append(spotFundsRuntimeBlocks, blocks...)
		// Reapplying P&L against changed SpotFunds bounds evaluates live blocks.
		// Keep the restored row exact; a halted row carries no numeric value.
		if plan.spotFundsChanged {
			if err := n.realm.SetAccountPnl(
				context.WithoutCancel(ctx), code, next.Pnl, next.PnlHaltReason,
			); err != nil {
				return fmt.Errorf("restore account %q pnl after policy configure: %w", code, err)
			}
		}
	}

	balanceBlocks, err := n.applyRestoredBalances(ctx, eng, before.data.Balances, after.data.Balances)
	if err != nil {
		return err
	}
	runtimeBlocks = append(runtimeBlocks, balanceBlocks...)
	spotFundsRuntimeBlocks = append(spotFundsRuntimeBlocks, balanceBlocks...)
	blockedByRuntime := make(map[domain.AccountID]struct{}, len(runtimeBlocks))
	for _, block := range runtimeBlocks {
		blockedByRuntime[block.Account] = struct{}{}
	}

	for _, code := range sortedAccountCodes(after.accounts) {
		previous, existed := before.accounts[code]
		next := after.accounts[code]
		if existed && previous.Blocked == next.Blocked &&
			previous.BlockReason == next.BlockReason &&
			previous.BlockPolicy == next.BlockPolicy &&
			previous.BlockCode == next.BlockCode &&
			previous.BlockDetails == next.BlockDetails {
			continue
		}
		if !existed && !next.Blocked {
			continue
		}
		if _, blocked := blockedByRuntime[code]; blocked && !next.Blocked {
			continue
		}
		if _, _, err := n.runAccountRuntimeChain(
			ctx,
			eng,
			next,
			"apply restored account block",
			nil,
			nil,
			&accountRuntimeBlock{
				blocked: next.Blocked,
				cause: domain.AccountBlock{
					Account: next.Code,
					Policy:  next.BlockPolicy,
					Code:    next.BlockCode,
					Reason:  next.BlockReason,
					Details: next.BlockDetails,
				},
			},
		); err != nil {
			return fmt.Errorf("apply restored account %q block: %w", code, err)
		}
	}

	for _, code := range sortedGroupCodes(after.groups) {
		previous, existed := before.groups[code]
		next := after.groups[code]
		if existed && previous.Blocked == next.Blocked &&
			previous.BlockReason == next.BlockReason {
			continue
		}
		if !existed && !next.Blocked {
			continue
		}
		if code == "" {
			continue
		}
		if _, err := n.runGroupRuntimeChain(
			ctx,
			eng,
			next,
			true,
			"apply restored group block",
			nil,
			&groupRuntimeBlock{
				blocked: next.Blocked,
				reason:  next.BlockReason,
			},
		); err != nil {
			return fmt.Errorf("apply restored group %q block: %w", code, err)
		}
	}

	for _, code := range sortedDeletedGroupCodes(before.groups, after.groups) {
		group := before.groups[code]
		var clearCurrency *string
		if group.Currency != "" {
			cleared := ""
			clearCurrency = &cleared
		}
		var unblock *groupRuntimeBlock
		if code != "" && group.Blocked {
			unblock = &groupRuntimeBlock{}
		}
		if _, err := n.runGroupRuntimeChain(
			ctx,
			eng,
			group,
			false,
			"clear deleted restored group",
			clearCurrency,
			unblock,
		); err != nil {
			return fmt.Errorf("clear deleted restored group %q: %w", code, err)
		}
		if code != "" {
			if err := resolver.RemoveGroupResolverEntry(group); err != nil {
				return fmt.Errorf("remove restored group resolver %q: %w", code, err)
			}
		}
	}

	for _, set := range policyBlocks {
		if err := n.mirrorPolicyConfigurationBlocks(ctx, set.policy, set.blocks); err != nil {
			return err
		}
	}
	if len(spotFundsRuntimeBlocks) > 0 {
		if err := n.mirrorPolicyConfigurationBlocks(
			ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, spotFundsRuntimeBlocks,
		); err != nil {
			return err
		}
	}
	if plan.marketDataChanged {
		if err := clearRestoredMarketData(eng.MarketDataSink(), before, after); err != nil {
			return fmt.Errorf("clear restored market data: %w", err)
		}
	}
	return nil
}

type marketDataPairKey struct {
	base  domain.EngineAssetID
	quote domain.EngineAssetID
}

// marketDataPublicationState contains only fields that determine whether an
// instrument publishes a live, valid quote. The pair selects the registry
// entries, provider selects publication behavior, effectiveEnabled combines
// both enable switches, manualPrice participates only for BYO publication, and
// pairResolved distinguishes an incomplete instrument from the zero-value key.
type marketDataPublicationState struct {
	pair             marketDataPairKey
	provider         string
	manualPrice      string
	effectiveEnabled bool
	pairResolved     bool
}

// effectiveMarketDataPublicationState derives the provider-aware comparison
// rule for one snapshot. A missing owner is an error because the store foreign
// key guarantees that every persisted instrument has an instance.
func effectiveMarketDataPublicationState(
	snapshot restoreRuntimeSnapshot,
	instances map[domain.ExternalID]domain.MarketDataInstance,
	instrument backup.MarketDataInstrument,
) (marketDataPublicationState, error) {
	instance, ok := instances[instrument.Instance]
	if !ok {
		return marketDataPublicationState{}, fmt.Errorf(
			"owning market-data instance %q: %w",
			instrument.Instance,
			domain.ErrNotFound,
		)
	}
	pair, pairResolved, err := marketDataPairFromAssets(
		snapshot.assets, instrument.BaseAsset, instrument.QuoteAsset,
	)
	if err != nil {
		return marketDataPublicationState{}, err
	}
	manualPrice := ""
	if instance.Provider == domain.MarketDataProviderBYO {
		manualPrice = instrument.ManualPrice
	}
	return marketDataPublicationState{
		pair:             pair,
		provider:         instance.Provider,
		manualPrice:      manualPrice,
		effectiveEnabled: instance.Enabled && instrument.Enabled,
		pairResolved:     pairResolved,
	}, nil
}

func restoreMarketDataPublicationStates(
	snapshot restoreRuntimeSnapshot,
) (map[string]marketDataPublicationState, error) {
	instances := make(map[domain.ExternalID]domain.MarketDataInstance,
		len(snapshot.marketDataInstances))
	for _, instance := range snapshot.marketDataInstances {
		instances[instance.ExternalID] = instance
	}
	states := make(map[string]marketDataPublicationState,
		len(snapshot.data.MarketDataInstruments))
	for _, instrument := range snapshot.data.MarketDataInstruments {
		key := restoreMarketDataKey(
			instrument.Instance, instrument.ExternalSymbol,
		)
		state, err := effectiveMarketDataPublicationState(
			snapshot, instances, instrument,
		)
		if err != nil {
			return nil, fmt.Errorf("resolve instrument %q publication state: %w", key, err)
		}
		states[key] = state
	}
	return states, nil
}

type marketDataPairSet map[marketDataPairKey]struct{}

func (pairs marketDataPairSet) add(pair marketDataPairKey) {
	pairs[pair] = struct{}{}
	pairs[marketDataPairKey{base: pair.quote, quote: pair.base}] = struct{}{}
}

func marketDataPairFromAssets(
	assets map[string]domain.Asset, base, quote string,
) (marketDataPairKey, bool, error) {
	if base == "" || quote == "" {
		return marketDataPairKey{}, false, nil
	}
	baseAsset, ok := assets[base]
	if !ok {
		return marketDataPairKey{}, false,
			fmt.Errorf("market-data base asset %q: %w", base, domain.ErrNotFound)
	}
	quoteAsset, ok := assets[quote]
	if !ok {
		return marketDataPairKey{}, false,
			fmt.Errorf("market-data quote asset %q: %w", quote, domain.ErrNotFound)
	}
	return marketDataPairKey{
		base: baseAsset.EngineAssetID, quote: quoteAsset.EngineAssetID,
	}, true, nil
}

func changedRestoreMarketDataPairs(
	before, after restoreRuntimeSnapshot,
) (marketDataPairSet, error) {
	beforeStates, err := restoreMarketDataPublicationStates(before)
	if err != nil {
		return nil, fmt.Errorf("resolve previous market data: %w", err)
	}
	afterStates, err := restoreMarketDataPublicationStates(after)
	if err != nil {
		return nil, fmt.Errorf("resolve restored market data: %w", err)
	}

	pairs := make(marketDataPairSet)
	for key, previous := range beforeStates {
		next, exists := afterStates[key]
		if !exists {
			if previous.pairResolved {
				pairs.add(previous.pair)
			}
			continue
		}
		if previous == next {
			continue
		}
		if previous.pairResolved {
			pairs.add(previous.pair)
		}
		if next.pairResolved {
			pairs.add(next.pair)
		}
	}
	for key, next := range afterStates {
		if _, exists := beforeStates[key]; exists {
			continue
		}
		if next.pairResolved {
			pairs.add(next.pair)
		}
	}
	return pairs, nil
}

func clearMarketDataPairs(
	sink marketdata.Sink, pairs marketDataPairSet,
) error {
	if len(pairs) == 0 {
		return nil
	}
	clearer, ok := sink.(marketdata.QuoteClearer)
	if !ok {
		return fmt.Errorf("live market-data sink does not support quote clear")
	}
	ordered := make([]marketDataPairKey, 0, len(pairs))
	for pair := range pairs {
		ordered = append(ordered, pair)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].base != ordered[j].base {
			return ordered[i].base < ordered[j].base
		}
		return ordered[i].quote < ordered[j].quote
	})
	var clearErrors []error
	for _, pair := range ordered {
		if err := clearer.Clear(pair.base, pair.quote); err != nil {
			clearErrors = append(clearErrors, fmt.Errorf(
				"clear quote %d/%d: %w", pair.base, pair.quote, err,
			))
		}
	}
	return errors.Join(clearErrors...)
}

func clearRestoredMarketData(
	sink marketdata.Sink, before, after restoreRuntimeSnapshot,
) error {
	pairs, err := changedRestoreMarketDataPairs(before, after)
	if err != nil {
		return err
	}
	return clearMarketDataPairs(sink, pairs)
}

func (n *localNode) applyRestoredBalances(
	ctx context.Context,
	eng engine.Engine,
	before []backup.Balance,
	after []backup.Balance,
) ([]domain.AccountBlock, error) {
	left := balanceRuntimeMap(before)
	right := balanceRuntimeMap(after)
	byAccount := make(map[domain.AccountID][]domain.Balance)
	for key, previous := range left {
		next, ok := right[key]
		if ok && reflect.DeepEqual(previous, next) {
			continue
		}
		if !ok {
			next = domain.Balance{
				Account: previous.Account, Asset: previous.Asset,
				Available: "0", Held: "0", Incoming: "0", RealizedPnl: "0",
			}
		}
		byAccount[next.Account] = append(byAccount[next.Account], next)
	}
	for key, next := range right {
		if previous, ok := left[key]; ok && reflect.DeepEqual(previous, next) {
			continue
		}
		if _, ok := left[key]; !ok {
			byAccount[next.Account] = append(byAccount[next.Account], next)
		}
	}

	accounts := make([]domain.AccountID, 0, len(byAccount))
	for account := range byAccount {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i] < accounts[j] })
	var blocks []domain.AccountBlock
	for _, account := range accounts {
		balances := byAccount[account]
		sort.Slice(balances, func(i, j int) bool { return balances[i].Asset < balances[j].Asset })
		reqs := make([]domain.AdjustmentRequest, 0, len(balances))
		for _, balance := range balances {
			reqs = append(reqs, snapshotAdjustmentRequest(balance))
		}
		storedAccount, found, err := n.realm.GetAccount(ctx, account)
		if err != nil {
			return nil, fmt.Errorf("read restored balance account %q: %w", account, err)
		}
		if !found {
			return nil, fmt.Errorf("account %q: %w", account, domain.ErrNotFound)
		}
		source, err := eng.AccountID(account)
		if err != nil {
			return nil, err
		}
		if err := validateAccountAdministrativeSource(storedAccount, source); err != nil {
			return nil, err
		}
		state := &restoreBalanceChainState{
			ctx:     ctx,
			account: storedAccount,
			reqs:    reqs,
		}
		begin := func(context.Context) (*restoreBalanceChainState, error) {
			if ctxErr := state.ctx.Err(); ctxErr != nil {
				state.err = fmt.Errorf(
					"engine: apply restored balances cancelled: %w", ctxErr,
				)
				return nil, state.err
			}
			current, found, readErr := n.realm.GetAccount(
				state.ctx, state.account.Code,
			)
			if readErr != nil {
				state.err = fmt.Errorf(
					"read account for restored balances: %w", readErr,
				)
				return nil, state.err
			}
			if !found {
				state.err = fmt.Errorf(
					"account %q: %w", state.account.Code, domain.ErrNotFound,
				)
				return nil, state.err
			}
			if validateErr := validateAccountAdministrativeSource(
				current, source,
			); validateErr != nil {
				state.err = fmt.Errorf(
					"read account for restored balances: %w", validateErr,
				)
				return nil, state.err
			}
			for _, req := range state.reqs {
				if _, assetErr := n.administrativeAsset(
					state.ctx, eng, req.Asset, "restored balance asset",
				); assetErr != nil {
					state.err = assetErr
					return nil, state.err
				}
			}
			adjustments, mapErr := eng.AccountAdjustmentModels(state.reqs)
			if mapErr != nil {
				state.err = mapErr
				return nil, state.err
			}
			state.adjustments = adjustments
			return state, nil
		}
		builder := asyncengine.Chain(source, begin)
		builder.ApplyAccountAdjustment(
			asyncengine.AccountAdjustmentHooks[*restoreBalanceChainState]{
				Adjustments: func(
					context.Context, *restoreBalanceChainState,
				) ([]model.AccountAdjustment, error) {
					return state.adjustments, nil
				},
				OnAdjusted: func(
					_ context.Context,
					state *restoreBalanceChainState,
					batch accountadjustment.BatchResult,
				) error {
					state.engineApplied = true
					results, reject, appliedErr :=
						eng.AppliedAccountAdjustmentBatch(
							state.account.Code, state.reqs, batch,
						)
					if appliedErr != nil {
						state.err = appliedErr
						return state.err
					}
					state.results = results
					state.reject = reject
					return nil
				},
			},
		)
		runner := builder.Finally(func(
			_ context.Context,
			state *restoreBalanceChainState,
			outcome asyncengine.ChainOutcome,
		) error {
			state.engineApplied, state.err = runtimeChainTerminalError(
				state.err, state.engineApplied, outcome,
			)
			return nil
		})
		_, chainErr := runner.Run(
			ctx, eng.AsyncEngine(),
		).Await(context.Background())
		if runErr := accountChainRunError(
			"apply restored balances", state.err, chainErr,
		); runErr != nil {
			return nil, fmt.Errorf(
				"apply restored balances for %q: %w", account, runErr,
			)
		}
		if state.reject != nil {
			return nil, fmt.Errorf(
				"restored balance batch for %q rejected: %s: %w",
				account, state.reject.Reason, domain.ErrInvalid,
			)
		}
		if len(state.results) != len(reqs) {
			return nil, fmt.Errorf(
				"restored balance batch for %q returned %d outcomes for %d requests: %w",
				account, len(state.results), len(reqs), domain.ErrInvalid,
			)
		}
		for _, result := range state.results {
			if result.Rejected != nil {
				return nil, fmt.Errorf(
					"restored balance for %q rejected: %s: %w",
					account, result.Rejected.Reason, domain.ErrInvalid,
				)
			}
			blocks = append(blocks, result.AccountBlocks...)
		}
	}
	return blocks, nil
}

func sortedAccountCodes(accounts map[domain.AccountID]domain.Account) []domain.AccountID {
	codes := make([]domain.AccountID, 0, len(accounts))
	for code := range accounts {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return codes
}

func sortedGroupCodes(groups map[string]domain.AccountGroup) []string {
	codes := make([]string, 0, len(groups))
	for code := range groups {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func sortedNewAccountCodes(
	before, after map[domain.AccountID]domain.Account,
) []domain.AccountID {
	codes := make([]domain.AccountID, 0)
	for code := range after {
		if _, ok := before[code]; !ok {
			codes = append(codes, code)
		}
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return codes
}

func sortedNewGroupCodes(
	before, after map[string]domain.AccountGroup,
) []string {
	codes := make([]string, 0)
	for code := range after {
		if _, ok := before[code]; !ok {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	return codes
}

func sortedNewAssetCodes(before, after []backup.Asset) []string {
	previous := make(map[string]struct{}, len(before))
	for _, asset := range before {
		previous[asset.Code] = struct{}{}
	}
	codes := make([]string, 0)
	for _, asset := range after {
		if _, ok := previous[asset.Code]; !ok {
			codes = append(codes, asset.Code)
		}
	}
	sort.Strings(codes)
	return codes
}

func sortedDeletedGroupCodes(
	before, after map[string]domain.AccountGroup,
) []string {
	codes := make([]string, 0)
	for code := range before {
		if _, ok := after[code]; !ok {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	return codes
}
