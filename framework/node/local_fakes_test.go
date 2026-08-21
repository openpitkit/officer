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
	"strconv"
	"sync"
	"testing"

	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

type fakeEngine struct {
	running                     bool
	sink                        marketdata.Sink
	marketDataServiceCloseCalls int
	enforceResolver             bool
	knownAccounts               map[domain.AccountID]struct{}
	knownAssets                 map[string]struct{}
	knownGroups                 map[string]struct{}
	accountResolverIDs          map[domain.AccountID]domain.EngineAccountID
	assetResolverIDs            map[string]domain.EngineAssetID
	groupResolverIDs            map[string]domain.EngineGroupID
	accountCurrencies           map[domain.AccountID]string
	groupCurrencies             map[string]string
	accountGroups               map[domain.AccountID]string
	resolverMu                  sync.RWMutex
	// Callbacks run under resolverMu; they must not re-enter the fake's resolver.
	afterAssetResolverAdd    func()
	afterAssetResolverRemove func()

	// accountPnlCalls records every engine P&L assignment in order, and
	// accountPnlBlocks are the blocks the engine reports back for an account.
	accountPnlCalls      []accountPnlCall
	accountPnlStateCalls []accountPnlStateCall
	accountPnlBlocks     map[domain.AccountID][]domain.AccountBlock
	accountPnlStates     map[domain.AccountID]model.PnlState

	configureCalls         []configureCall
	configureBlocks        []domain.AccountBlock
	adjustmentCalls        []adjustmentCall
	adjustmentBatchCalls   []adjustmentBatchCall
	adjustmentBatchResults []engine.AdjustmentResult
	submitCalls            []domain.Order
	execReportCalls        []domain.ExecutionReportInput
	execReportLeaves       []string
	// Canned engine outcomes for the trading/spot-funds/group paths.
	adjustmentAccepted         *domain.AdjustmentOutcomeAccepted
	adjustmentReject           *domain.AdjustmentOutcomeRejected
	adjustmentNoop             bool
	submitLock                 []byte
	submitSettlementLockPrice  string
	submitTradePrice           string
	submitOutcomes             []engine.BalanceOutcome
	submitBlocks               []domain.ExecutionAccountBlock
	submitAccountPnl           string
	submitAccountPnlHaltReason domain.PnlHaltReason
	submitReject               *domain.OrderReject
	emptyImmediatePersistence  bool
	execReportBlocks           []domain.ExecutionAccountBlock
	execReportOutcomes         []engine.BalanceOutcome
	emptyExecReportPersistence bool
	execReportAccountMismatch  bool
	stateMu                    sync.Mutex

	// Canned dry-run outcome and recorded probes for the check path.
	checkResult domain.CheckResult
	checkProbes []domain.OrderProbe

	// configureErr, when set, is returned by ConfigurePolicy instead of the
	// generic failure; it lets a test assert a specific wrapped sentinel
	// propagates through the node.
	configureErr  error
	accountPnlErr error

	failConfigure          bool
	failAdjustment         bool
	failSubmit             bool
	failExecReport         bool
	renameAssetResolverErr error
	submitEntered          chan domain.AccountID
	submitRelease          <-chan struct{}
}

type configureCall struct {
	policy string
	limits engine.LimitSet
}

type accountPnlCall struct {
	id  domain.AccountID
	pnl string
}

type accountPnlStateCall struct {
	id         domain.AccountID
	pnl        string
	haltReason domain.PnlHaltReason
}

type adjustmentCall struct {
	account domain.AccountID
	req     domain.AdjustmentRequest
}

type adjustmentBatchCall struct {
	account domain.AccountID
	reqs    []domain.AdjustmentRequest
}

var _ engine.DictionaryResolver = (*fakeEngine)(nil)
var _ engine.AccountChainAdapter = (*fakeEngine)(nil)

func (d *fakeOrderChainDriver) Configure() configure.Configurator {
	return d.admin.Configure()
}

// fakeExecutionReportCarriesFill deliberately mirrors the engine's fill
// predicate: the fake emits a fill event and trade exactly when the real engine
// would, so a report input with both a fill quantity and price drives the fill
// legs of the persistence the node persists.
func fakeExecutionReportCarriesFill(in domain.ExecutionReportInput) bool {
	return in.FillQuantity != "" && in.FillPrice != ""
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		running: true,
		sink:    nopSink{},
	}
}

func TestFakeEngineDictionaryResolverMutations(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	var captured engine.Snapshot
	if _, err := fakeBuild(eng, &captured)(engine.Snapshot{
		Accounts: []domain.Account{{Code: "account-old", EngineAccountID: 7}},
		Groups:   []domain.AccountGroup{{Code: "group-old", EngineGroupID: 9}},
	}); err != nil {
		t.Fatalf("fakeBuild: %v", err)
	}
	sinkBefore := eng.MarketDataSink()

	if err := eng.AddAccountResolverEntry(domain.Account{
		Code: "account-added", EngineAccountID: 8,
	}); err != nil {
		t.Fatalf("AddAccountResolverEntry: %v", err)
	}
	if err := eng.AddGroupResolverEntry(domain.AccountGroup{
		Code: "group-added", EngineGroupID: 10,
	}); err != nil {
		t.Fatalf("AddGroupResolverEntry: %v", err)
	}
	if err := eng.RenameAccountResolverEntry("account-old", domain.Account{
		Code: "account-new", EngineAccountID: 7,
	}); err != nil {
		t.Fatalf("RenameAccountResolverEntry: %v", err)
	}
	if err := eng.AddAssetResolverEntry(domain.Asset{
		Code: "asset-old", EngineAssetID: 11,
	}); err != nil {
		t.Fatalf("AddAssetResolverEntry: %v", err)
	}
	if err := eng.RenameAssetResolverEntry("asset-old", domain.Asset{
		Code: "asset-new", EngineAssetID: 11,
	}); err != nil {
		t.Fatalf("RenameAssetResolverEntry: %v", err)
	}
	if err := eng.RenameGroupResolverEntry("group-old", domain.AccountGroup{
		Code: "group-new", EngineGroupID: 9,
	}); err != nil {
		t.Fatalf("RenameGroupResolverEntry: %v", err)
	}

	if _, err := eng.AccountID("account-new"); err != nil {
		t.Fatalf("resolve renamed account alias: %v", err)
	}
	if _, err := eng.ResolveGroup("group-new"); err != nil {
		t.Fatalf("resolve renamed group alias: %v", err)
	}
	if _, ok := eng.knownAssets["asset-old"]; ok ||
		eng.assetResolverIDs["asset-new"] != 11 {
		t.Fatalf("renamed asset resolver = %+v", eng.assetResolverIDs)
	}
	if err := eng.RemoveAssetResolverEntry(domain.Asset{
		Code: "asset-new", EngineAssetID: 12,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched asset removal error = %v, want ErrInvalid", err)
	}
	if err := eng.RemoveAssetResolverEntry(domain.Asset{
		Code: "asset-new", EngineAssetID: 11,
	}); err != nil {
		t.Fatalf("RemoveAssetResolverEntry: %v", err)
	}
	if _, ok := eng.knownAssets["asset-new"]; ok {
		t.Fatalf("removed asset resolver alias survived: %+v", eng.knownAssets)
	}
	if _, ok := eng.assetResolverIDs["asset-new"]; ok {
		t.Fatalf("removed asset resolver id survived: %+v", eng.assetResolverIDs)
	}
	if err := eng.RenameAccountResolverEntry("account-new", domain.Account{
		Code: "account-bad", EngineAccountID: 99,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched account id error = %v, want ErrInvalid", err)
	}
	if _, err := eng.AccountID("account-new"); err != nil {
		t.Fatalf("failed rename changed existing alias: %v", err)
	}
	if err := eng.RemoveGroupResolverEntry(domain.AccountGroup{
		Code: "group-new", EngineGroupID: 11,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched group removal error = %v, want ErrInvalid", err)
	}
	if _, err := eng.ResolveGroup("group-new"); err != nil {
		t.Fatalf("failed removal changed existing group alias: %v", err)
	}
	if err := eng.RemoveGroupResolverEntry(domain.AccountGroup{
		Code: "group-new", EngineGroupID: 9,
	}); err != nil {
		t.Fatalf("RemoveGroupResolverEntry: %v", err)
	}
	if _, err := eng.ResolveGroup("group-new"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("removed group alias error = %v, want ErrInvalid", err)
	}
	if got := eng.MarketDataSink(); got != sinkBefore {
		t.Fatal("fake resolver mutation replaced MarketDataSink")
	}
}

func TestFakeEngineRejectsUnpublishedAssetOnEveryResolverPath(t *testing.T) {
	t.Parallel()

	const (
		account     = domain.AccountID("account")
		group       = "group"
		unpublished = "unpublished"
	)
	ctx := context.Background()
	newEngine := func(t *testing.T) *fakeEngine {
		t.Helper()
		eng := newFakeEngine()
		eng.enforceResolver = true
		var captured engine.Snapshot
		if _, err := fakeBuild(eng, &captured)(engine.Snapshot{
			Accounts: []domain.Account{{Code: account, EngineAccountID: 1}},
			Groups:   []domain.AccountGroup{{Code: group, EngineGroupID: 1}},
		}); err != nil {
			t.Fatalf("fakeBuild: %v", err)
		}
		return eng
	}
	tests := []struct {
		name string
		run  func(*fakeEngine) error
	}{
		{
			name: "rate limit policy",
			run: func(eng *fakeEngine) error {
				_, err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, engine.LimitSet{
					RateLimits: []domain.LimitRate{{
						Scope: domain.ScopeAsset,
						Asset: unpublished,
					}},
				})
				return err
			},
		},
		{
			name: "order size policy",
			run: func(eng *fakeEngine) error {
				_, err := eng.ConfigurePolicy(ctx, domain.PolicyOrderSizeLimit,
					engine.LimitSet{OrderSizeLimits: []domain.LimitOrderSize{{
						Scope: domain.ScopeAsset,
						Asset: unpublished,
					}}})
				return err
			},
		},
		{
			name: "spot funds PnL policy",
			run: func(eng *fakeEngine) error {
				_, err := eng.ConfigurePolicy(ctx,
					domain.PolicySpotFundsPnlBoundsKillSwitch,
					engine.LimitSet{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{{
						Currency: unpublished,
					}}})
				return err
			},
		},
		{
			name: "account currency",
			run: func(eng *fakeEngine) error {
				_, err := eng.ResolveAsset(unpublished)
				return err
			},
		},
		{
			name: "group currency",
			run: func(eng *fakeEngine) error {
				_, err := eng.ResolveAsset(unpublished)
				return err
			},
		},
		{
			name: "account adjustment",
			run: func(eng *fakeEngine) error {
				_, err := eng.AccountAdjustmentModels(
					[]domain.AdjustmentRequest{{Asset: unpublished}},
				)
				return err
			},
		},
		{
			name: "submit order",
			run: func(eng *fakeEngine) error {
				_, err := eng.OrderModel(domain.Order{
					Account:    account,
					BaseAsset:  unpublished,
					QuoteAsset: unpublished,
				})
				return err
			},
		},
		{
			name: "submit immediate",
			run: func(eng *fakeEngine) error {
				_, err := eng.OrderModel(domain.Order{
					Account:    account,
					BaseAsset:  unpublished,
					QuoteAsset: unpublished,
				})
				return err
			},
		},
		{
			name: "execution report instrument",
			run: func(eng *fakeEngine) error {
				_, err := eng.ExecutionReportModel(
					domain.ExecutionReportInput{
						Account:    account,
						BaseAsset:  unpublished,
						QuoteAsset: unpublished,
					}, "",
				)
				return err
			},
		},
		{
			name: "execution report commission",
			run: func(eng *fakeEngine) error {
				for _, asset := range []domain.Asset{
					{Code: "published-base", EngineAssetID: 2},
					{Code: "published-quote", EngineAssetID: 3},
				} {
					if err := eng.AddAssetResolverEntry(asset); err != nil {
						return err
					}
				}
				_, err := eng.ExecutionReportModel(
					domain.ExecutionReportInput{
						Account:    account,
						BaseAsset:  "published-base",
						QuoteAsset: "published-quote",
						Commission: &domain.Commission{Currency: unpublished},
					}, "",
				)
				return err
			},
		},
		{
			name: "dry run",
			run: func(eng *fakeEngine) error {
				_, err := eng.CheckOrderModel(domain.OrderProbe{
					Account:    account,
					BaseAsset:  unpublished,
					QuoteAsset: unpublished,
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.run(newEngine(t)); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// fakeBuild returns a BuildFunc that records the seed snapshot it was given and
// hands back eng. The captured snapshot lets a test assert the build was seeded
// from the store.
func fakeBuild(eng *fakeEngine, captured *engine.Snapshot) engine.BuildFunc {
	return func(snap engine.Snapshot) (engine.Engine, error) {
		*captured = snap
		eng.resolverMu.Lock()
		defer eng.resolverMu.Unlock()
		eng.knownAccounts = map[domain.AccountID]struct{}{}
		eng.accountResolverIDs = map[domain.AccountID]domain.EngineAccountID{}
		for _, account := range snap.Accounts {
			eng.knownAccounts[account.Code] = struct{}{}
			eng.accountResolverIDs[account.Code] = account.EngineAccountID
		}
		eng.knownAssets = map[string]struct{}{}
		eng.assetResolverIDs = map[string]domain.EngineAssetID{}
		for _, asset := range snap.Assets {
			eng.knownAssets[asset.Code] = struct{}{}
			eng.assetResolverIDs[asset.Code] = asset.EngineAssetID
		}
		eng.knownGroups = map[string]struct{}{}
		eng.groupResolverIDs = map[string]domain.EngineGroupID{}
		for _, group := range snap.Groups {
			eng.knownGroups[group.Code] = struct{}{}
			eng.groupResolverIDs[group.Code] = group.EngineGroupID
		}
		eng.accountCurrencies = map[domain.AccountID]string{}
		eng.groupCurrencies = map[string]string{}
		eng.accountGroups = map[domain.AccountID]string{}
		for _, account := range snap.Accounts {
			if account.Currency != "" {
				eng.accountCurrencies[account.Code] = account.Currency
			}
			eng.accountGroups[account.Code] = account.GroupCode
		}
		for _, group := range snap.Groups {
			if group.Currency != "" {
				eng.groupCurrencies[group.Code] = group.Currency
			}
		}
		return eng, nil
	}
}

func (e *fakeEngine) checkKnownAccount(account domain.AccountID) error {
	if !e.enforceResolver {
		return nil
	}
	e.resolverMu.RLock()
	defer e.resolverMu.RUnlock()
	if _, ok := e.knownAccounts[account]; !ok {
		return fmt.Errorf("engine: unknown account %q: %w", account, domain.ErrInvalid)
	}
	return nil
}

func (e *fakeEngine) checkKnownAsset(asset string) error {
	if !e.enforceResolver {
		return nil
	}
	e.resolverMu.RLock()
	defer e.resolverMu.RUnlock()
	if _, ok := e.knownAssets[asset]; !ok {
		return fmt.Errorf("engine: unknown asset %q: %w", asset, domain.ErrInvalid)
	}
	return nil
}

func (e *fakeEngine) checkKnownOrderAssets(base, quote string) error {
	if err := e.checkKnownAsset(base); err != nil {
		return err
	}
	return e.checkKnownAsset(quote)
}

func (e *fakeEngine) checkKnownPolicyAssets(
	policy string, limits engine.LimitSet,
) error {
	switch policy {
	case domain.PolicyRateLimit:
		for _, limit := range limits.RateLimits {
			if limit.Scope != domain.ScopeAsset &&
				limit.Scope != domain.ScopeAccountAsset {
				continue
			}
			if err := e.checkKnownAsset(limit.Asset); err != nil {
				return err
			}
		}
	case domain.PolicyOrderSizeLimit:
		for _, limit := range limits.OrderSizeLimits {
			if limit.Scope != domain.ScopeAsset &&
				limit.Scope != domain.ScopeAccountAsset {
				continue
			}
			if err := e.checkKnownAsset(limit.Asset); err != nil {
				return err
			}
		}
	case domain.PolicySpotFundsPnlBoundsKillSwitch:
		for _, limit := range limits.SpotFundsPnlBoundsLimits {
			if err := e.checkKnownAsset(limit.Currency); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *fakeEngine) checkKnownGroup(groupID string) error {
	if !e.enforceResolver || groupID == "" {
		return nil
	}
	e.resolverMu.RLock()
	defer e.resolverMu.RUnlock()
	if _, ok := e.knownGroups[groupID]; !ok {
		return fmt.Errorf("engine: unknown group %q: %w", groupID, domain.ErrInvalid)
	}
	return nil
}

func (e *fakeEngine) Version() string      { return "fake" }
func (e *fakeEngine) BuildProfile() string { return "test" }
func (e *fakeEngine) Running() bool        { return e.running }

func (e *fakeEngine) AddAccountResolverEntry(account domain.Account) error {
	if err := domain.ValidateEngineAccountID(account.EngineAccountID); err != nil {
		return fmt.Errorf("engine: account %q engine id: %w", account.Code, err)
	}
	e.resolverMu.Lock()
	defer e.resolverMu.Unlock()
	if _, exists := e.knownAccounts[account.Code]; exists {
		return fmt.Errorf(
			"engine: account resolver alias %q already exists: %w",
			account.Code, domain.ErrAlreadyExists,
		)
	}
	for alias, id := range e.accountResolverIDs {
		if id == account.EngineAccountID {
			return fmt.Errorf(
				"engine: account engine id %d already belongs to resolver alias %q: %w",
				id, alias, domain.ErrInvalid,
			)
		}
	}
	if e.knownAccounts == nil {
		e.knownAccounts = map[domain.AccountID]struct{}{}
	}
	if e.accountResolverIDs == nil {
		e.accountResolverIDs = map[domain.AccountID]domain.EngineAccountID{}
	}
	e.knownAccounts[account.Code] = struct{}{}
	e.accountResolverIDs[account.Code] = account.EngineAccountID
	return nil
}

func (e *fakeEngine) AddAssetResolverEntry(asset domain.Asset) error {
	if err := domain.ValidateEngineAssetID(asset.EngineAssetID); err != nil {
		return fmt.Errorf("engine: asset %q engine id: %w", asset.Code, err)
	}
	e.resolverMu.Lock()
	defer e.resolverMu.Unlock()
	if _, exists := e.knownAssets[asset.Code]; exists {
		return fmt.Errorf(
			"engine: asset resolver alias %q already exists: %w",
			asset.Code, domain.ErrAlreadyExists,
		)
	}
	for alias, id := range e.assetResolverIDs {
		if id == asset.EngineAssetID {
			return fmt.Errorf(
				"engine: asset engine id %d already belongs to resolver alias %q: %w",
				id, alias, domain.ErrInvalid,
			)
		}
	}
	if e.knownAssets == nil {
		e.knownAssets = map[string]struct{}{}
	}
	if e.assetResolverIDs == nil {
		e.assetResolverIDs = map[string]domain.EngineAssetID{}
	}
	e.knownAssets[asset.Code] = struct{}{}
	e.assetResolverIDs[asset.Code] = asset.EngineAssetID
	if e.afterAssetResolverAdd != nil {
		e.afterAssetResolverAdd()
	}
	return nil
}

func (e *fakeEngine) ResolveAsset(code string) (param.Asset, error) {
	if err := e.checkKnownAsset(code); err != nil {
		return param.Asset{}, err
	}
	e.resolverMu.RLock()
	id, ok := e.assetResolverIDs[code]
	e.resolverMu.RUnlock()
	if !ok {
		return param.Asset{}, fmt.Errorf(
			"engine: unknown asset resolver alias %q: %w",
			code,
			domain.ErrInvalid,
		)
	}
	return param.NewAsset(strconv.FormatUint(id.Uint64(), 10))
}

func (e *fakeEngine) RenameAccountResolverEntry(
	oldCode domain.AccountID, account domain.Account,
) error {
	if err := domain.ValidateEngineAccountID(account.EngineAccountID); err != nil {
		return fmt.Errorf("engine: account %q engine id: %w", account.Code, err)
	}
	e.resolverMu.Lock()
	defer e.resolverMu.Unlock()
	currentID, exists := e.accountResolverIDs[oldCode]
	if !exists {
		return fmt.Errorf(
			"engine: unknown account resolver alias %q: %w",
			oldCode, domain.ErrInvalid,
		)
	}
	if currentID != account.EngineAccountID {
		return fmt.Errorf(
			"engine: account resolver alias %q has engine id %d, target %q has %d: %w",
			oldCode, currentID, account.Code, account.EngineAccountID, domain.ErrInvalid,
		)
	}
	if oldCode == account.Code {
		return nil
	}
	if _, exists := e.knownAccounts[account.Code]; exists {
		return fmt.Errorf(
			"engine: account resolver alias %q already exists: %w",
			account.Code, domain.ErrAlreadyExists,
		)
	}
	delete(e.knownAccounts, oldCode)
	delete(e.accountResolverIDs, oldCode)
	e.knownAccounts[account.Code] = struct{}{}
	e.accountResolverIDs[account.Code] = currentID
	if currency, ok := e.accountCurrencies[oldCode]; ok {
		delete(e.accountCurrencies, oldCode)
		e.accountCurrencies[account.Code] = currency
	}
	if group, ok := e.accountGroups[oldCode]; ok {
		delete(e.accountGroups, oldCode)
		e.accountGroups[account.Code] = group
	}
	return nil
}

func (e *fakeEngine) RenameAssetResolverEntry(oldCode string, asset domain.Asset) error {
	if e.renameAssetResolverErr != nil {
		return e.renameAssetResolverErr
	}
	if err := domain.ValidateEngineAssetID(asset.EngineAssetID); err != nil {
		return fmt.Errorf("engine: asset %q engine id: %w", asset.Code, err)
	}
	e.resolverMu.Lock()
	defer e.resolverMu.Unlock()
	currentID, exists := e.assetResolverIDs[oldCode]
	if !exists {
		return fmt.Errorf(
			"engine: unknown asset resolver alias %q: %w",
			oldCode, domain.ErrInvalid,
		)
	}
	if currentID != asset.EngineAssetID {
		return fmt.Errorf(
			"engine: asset resolver alias %q has engine id %d, target %q has %d: %w",
			oldCode, currentID, asset.Code, asset.EngineAssetID, domain.ErrInvalid,
		)
	}
	if oldCode == asset.Code {
		return nil
	}
	if _, exists := e.knownAssets[asset.Code]; exists {
		return fmt.Errorf(
			"engine: asset resolver alias %q already exists: %w",
			asset.Code, domain.ErrAlreadyExists,
		)
	}
	delete(e.knownAssets, oldCode)
	delete(e.assetResolverIDs, oldCode)
	e.knownAssets[asset.Code] = struct{}{}
	e.assetResolverIDs[asset.Code] = currentID
	return nil
}

func (e *fakeEngine) RemoveAssetResolverEntry(asset domain.Asset) error {
	if err := domain.ValidateEngineAssetID(asset.EngineAssetID); err != nil {
		return fmt.Errorf("engine: asset %q engine id: %w", asset.Code, err)
	}
	e.resolverMu.Lock()
	defer e.resolverMu.Unlock()
	currentID, exists := e.assetResolverIDs[asset.Code]
	if !exists {
		return fmt.Errorf(
			"engine: unknown asset resolver alias %q: %w",
			asset.Code, domain.ErrInvalid,
		)
	}
	if currentID != asset.EngineAssetID {
		return fmt.Errorf(
			"engine: asset resolver alias %q has engine id %d, removal target has %d: %w",
			asset.Code, currentID, asset.EngineAssetID, domain.ErrInvalid,
		)
	}
	delete(e.knownAssets, asset.Code)
	delete(e.assetResolverIDs, asset.Code)
	if e.afterAssetResolverRemove != nil {
		e.afterAssetResolverRemove()
	}
	return nil
}

func (e *fakeEngine) AddGroupResolverEntry(group domain.AccountGroup) error {
	if err := validateFakeResolverGroup(group); err != nil {
		return fmt.Errorf("engine: group %q engine id: %w", group.Code, err)
	}
	e.resolverMu.Lock()
	defer e.resolverMu.Unlock()
	if _, exists := e.knownGroups[group.Code]; exists {
		return fmt.Errorf(
			"engine: group resolver alias %q already exists: %w",
			group.Code, domain.ErrAlreadyExists,
		)
	}
	for alias, id := range e.groupResolverIDs {
		if id == group.EngineGroupID {
			return fmt.Errorf(
				"engine: group engine id %d already belongs to resolver alias %q: %w",
				id, alias, domain.ErrInvalid,
			)
		}
	}
	if e.knownGroups == nil {
		e.knownGroups = map[string]struct{}{}
	}
	if e.groupResolverIDs == nil {
		e.groupResolverIDs = map[string]domain.EngineGroupID{}
	}
	e.knownGroups[group.Code] = struct{}{}
	e.groupResolverIDs[group.Code] = group.EngineGroupID
	return nil
}

func (e *fakeEngine) ResolveGroup(
	code string,
) (param.AccountGroupID, error) {
	if err := e.checkKnownGroup(code); err != nil {
		return param.AccountGroupID{}, err
	}
	if code == "" {
		return param.DefaultAccountGroup, nil
	}
	e.resolverMu.RLock()
	id, ok := e.groupResolverIDs[code]
	e.resolverMu.RUnlock()
	if !ok {
		return param.AccountGroupID{}, fmt.Errorf(
			"engine: unknown group resolver alias %q: %w",
			code,
			domain.ErrInvalid,
		)
	}
	return param.NewAccountGroupIDFromUint32(id.Uint32())
}

func (e *fakeEngine) RenameGroupResolverEntry(
	oldCode string, group domain.AccountGroup,
) error {
	if err := validateFakeResolverGroup(group); err != nil {
		return fmt.Errorf("engine: group %q engine id: %w", group.Code, err)
	}
	e.resolverMu.Lock()
	defer e.resolverMu.Unlock()
	currentID, exists := e.groupResolverIDs[oldCode]
	if !exists {
		return fmt.Errorf(
			"engine: unknown group resolver alias %q: %w",
			oldCode, domain.ErrInvalid,
		)
	}
	if currentID != group.EngineGroupID {
		return fmt.Errorf(
			"engine: group resolver alias %q has engine id %d, target %q has %d: %w",
			oldCode, currentID, group.Code, group.EngineGroupID, domain.ErrInvalid,
		)
	}
	if oldCode == group.Code {
		return nil
	}
	if _, exists := e.knownGroups[group.Code]; exists {
		return fmt.Errorf(
			"engine: group resolver alias %q already exists: %w",
			group.Code, domain.ErrAlreadyExists,
		)
	}
	delete(e.knownGroups, oldCode)
	delete(e.groupResolverIDs, oldCode)
	e.knownGroups[group.Code] = struct{}{}
	e.groupResolverIDs[group.Code] = currentID
	if currency, ok := e.groupCurrencies[oldCode]; ok {
		delete(e.groupCurrencies, oldCode)
		e.groupCurrencies[group.Code] = currency
	}
	for account, memberGroup := range e.accountGroups {
		if memberGroup == oldCode {
			e.accountGroups[account] = group.Code
		}
	}
	return nil
}

func (e *fakeEngine) RemoveGroupResolverEntry(group domain.AccountGroup) error {
	if err := validateFakeResolverGroup(group); err != nil {
		return fmt.Errorf("engine: group %q engine id: %w", group.Code, err)
	}
	e.resolverMu.Lock()
	defer e.resolverMu.Unlock()
	currentID, exists := e.groupResolverIDs[group.Code]
	if !exists {
		return fmt.Errorf(
			"engine: unknown group resolver alias %q: %w",
			group.Code, domain.ErrInvalid,
		)
	}
	if currentID != group.EngineGroupID {
		return fmt.Errorf(
			"engine: group resolver alias %q has engine id %d, removal target has %d: %w",
			group.Code, currentID, group.EngineGroupID, domain.ErrInvalid,
		)
	}
	delete(e.knownGroups, group.Code)
	delete(e.groupResolverIDs, group.Code)
	return nil
}

func validateFakeResolverGroup(group domain.AccountGroup) error {
	if group.Code == "" {
		if group.EngineGroupID != 0 {
			return domain.ErrInvalid
		}
		return nil
	}
	return domain.ValidateEngineGroupID(group.EngineGroupID)
}

func (e *fakeEngine) ConfigurePolicy(
	_ context.Context, policy string, limits engine.LimitSet,
) (engine.PolicyConfigurationResult, error) {
	if err := e.checkKnownPolicyAssets(policy, limits); err != nil {
		return engine.PolicyConfigurationResult{}, err
	}
	if e.configureErr != nil {
		return engine.PolicyConfigurationResult{}, e.configureErr
	}
	if e.failConfigure {
		return engine.PolicyConfigurationResult{}, errors.New("configure failed")
	}
	e.configureCalls = append(e.configureCalls, configureCall{policy, limits})
	return engine.PolicyConfigurationResult{
		AccountBlocks: e.configureBlocks,
	}, nil
}

func (e *fakeEngine) materializeFakeAccountAdjustmentBatch(
	account domain.AccountID,
	reqs []domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	copied := append([]domain.AdjustmentRequest(nil), reqs...)
	e.stateMu.Lock()
	e.adjustmentBatchCalls = append(e.adjustmentBatchCalls,
		adjustmentBatchCall{account, copied})
	for _, req := range reqs {
		e.adjustmentCalls = append(e.adjustmentCalls, adjustmentCall{account, req})
	}
	e.stateMu.Unlock()
	if e.adjustmentReject != nil {
		return nil, e.adjustmentReject, nil
	}
	if e.adjustmentNoop {
		return nil, nil, nil
	}
	if e.adjustmentBatchResults != nil {
		return append([]engine.AdjustmentResult(nil), e.adjustmentBatchResults...), nil, nil
	}
	accepted := e.adjustmentAccepted
	if accepted == nil {
		accepted = &domain.AdjustmentOutcomeAccepted{}
	}
	results := make([]engine.AdjustmentResult, 0, len(reqs))
	for range reqs {
		results = append(results, engine.AdjustmentResult{Accepted: accepted})
	}
	return results, nil, nil
}

func (e *fakeEngine) SettledExecutionReport(
	in domain.ExecutionReportInput,
	_ param.AccountID,
	_ pretrade.PostTradeResult,
) (engine.ExecutionReportResult, error) {
	return e.materializeFakeExecutionReport(in)
}

func (e *fakeEngine) materializeFakeExecutionReport(
	in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	e.stateMu.Lock()
	e.execReportCalls = append(e.execReportCalls, in)
	e.stateMu.Unlock()
	if e.emptyExecReportPersistence {
		return engine.ExecutionReportResult{
			Blocks:   e.execReportBlocks,
			Outcomes: e.execReportOutcomes,
		}, nil
	}
	payload := accountBlockPayload(e.execReportBlocks)
	payload.FillQuantity = in.FillQuantity
	payload.FillPrice = in.FillPrice
	payload.FillLockPrice = in.LockPrice
	payload.LeavesQuantity = in.LeavesQuantity
	payload.OrderStatus = string(in.OrderStatus)
	payload.Commission = in.Commission
	events := []domain.OrderEvent{}
	if fakeExecutionReportCarriesFill(in) {
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
	if fakeExecutionReportCarriesFill(in) {
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
	persistence := engine.ExecutionReportPersistence{
		Trade:       trade,
		Commission:  in.Commission,
		OrderStatus: in.OrderStatus,
		Leaves:      in.LeavesQuantity,
		Balances:    balanceSettlementsFrom(e.execReportOutcomes),
		Events:      events,
		Blocks:      e.execReportBlocks,
	}
	return engine.ExecutionReportResult{
		Persistence: &persistence,
		Blocks:      e.execReportBlocks,
		Outcomes:    e.execReportOutcomes,
	}, nil
}

// MarketDataSink returns the stable no-op sink owned by this fake handle.
func (e *fakeEngine) MarketDataSink() marketdata.Sink { return e.sink }

func (e *fakeEngine) Stop() {
	if runtime, ok := fakeOrderAsyncEngines.LoadAndDelete(e); ok {
		_ = runtime.(*fakeOrderAsyncRuntime).async.StopGraceful(context.Background())
	}
	e.running = false
}

func (e *fakeEngine) CloseMarketDataService() {
	e.marketDataServiceCloseCalls++
}

// nopSink is a Sink that drops every quote; it stands in for the engine's sink
// in node tests that never push.
type nopSink struct{}

func (nopSink) Push(marketdata.QuoteUpdate) error { return nil }
func (nopSink) Clear(domain.EngineAssetID, domain.EngineAssetID) error {
	return nil
}

// realmWrapStore decorates a store.Store so its ForRealm returns a realm handle
// produced by wrap. It lets a test inject a failing/overriding RealmStore while
// keeping the connection-lifecycle methods of the real store. The realm handle
// is resolved once and cached so repeated ForRealm calls (e.g. ResetDatabase
// re-binding) see the same decorator.
type realmWrapStore struct {
	store.Store
	wrap  func(store.RealmStore) store.RealmStore
	mu    sync.Mutex
	realm store.RealmStore
}

func newRealmWrapStore(
	st store.Store, wrap func(store.RealmStore) store.RealmStore,
) *realmWrapStore {
	return &realmWrapStore{Store: st, wrap: wrap}
}

func (s *realmWrapStore) ForRealm(
	ctx context.Context, realm domain.RealmID,
) (store.RealmStore, error) {
	inner, err := s.Store.ForRealm(ctx, realm)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realm = s.wrap(inner)
	return s.realm, nil
}

// failRestoreAuditRealm fails the restore_backup audit append while delegating
// everything else to the wrapped realm store.
type failRestoreAuditRealm struct {
	store.RealmStore
}

var errRestoreAuditFailed = errors.New("restore audit failed")

func (s *failRestoreAuditRealm) AppendAudit(
	ctx context.Context, entry store.AuditEntry,
) error {
	if entry.Action == domain.AuditActionRestoreBackup {
		return errRestoreAuditFailed
	}
	return s.RealmStore.AppendAudit(ctx, entry)
}

// restoreScopeProbeRealm records the exact export and restore scopes used by
// LocalNode recovery. It also fails the final restore audit so tests can drive
// the rollback path without changing the wrapped store's backup semantics.
type restoreScopeProbeRealm struct {
	store.RealmStore
	exportScopes  []backup.Scope
	restoreScopes []backup.Scope
}

func (s *restoreScopeProbeRealm) ExportBackup(
	ctx context.Context, scope backup.Scope,
) (backup.Archive, error) {
	s.exportScopes = append(s.exportScopes, scope)
	return s.RealmStore.ExportBackup(ctx, scope)
}

func (s *restoreScopeProbeRealm) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	s.restoreScopes = append(s.restoreScopes, opts.Scope)
	return s.RealmStore.RestoreBackup(ctx, archive, opts)
}

func (s *restoreScopeProbeRealm) AppendAudit(
	ctx context.Context, entry store.AuditEntry,
) error {
	if entry.Action == domain.AuditActionRestoreBackup {
		return errRestoreAuditFailed
	}
	return s.RealmStore.AppendAudit(ctx, entry)
}

// failActionAuditRealm fails the AppendAudit write for one specific audit action
// while delegating every other action (startup hydrate, create_account,
// create_group) so setup succeeds. It drives the admin block/group/set-group
// post-engine audit fail-stop: the engine mutation and the account/group store
// write already committed inside the lane, so the failed audit is a post-engine
// persistence failure that must route to the fatal hook.
type failActionAuditRealm struct {
	store.RealmStore
	action domain.AuditAction
	err    error
}

func (s *failActionAuditRealm) AppendAudit(
	ctx context.Context, entry store.AuditEntry,
) error {
	if entry.Action == s.action {
		return s.err
	}
	return s.RealmStore.AppendAudit(ctx, entry)
}

func (s *failActionAuditRealm) AppendAuditBatch(
	ctx context.Context, entries []store.AuditEntry,
) error {
	for _, entry := range entries {
		if entry.Action == s.action {
			return s.err
		}
	}
	return s.RealmStore.AppendAuditBatch(ctx, entries)
}

// failNthActionAuditRealm fails the Nth (1-based) AppendAudit write for one
// specific audit action, delegating every earlier write for that action - and
// every write for any other action - to the wrapped realm store. Unlike
// failActionAuditRealm, which rejects every write for the action, this lets an
// earlier row for the action commit so a later corrective or follow-up row for
// the same action can be attempted and observed to fail on its own.
type failNthActionAuditRealm struct {
	store.RealmStore
	action domain.AuditAction
	n      int
	err    error

	mu    sync.Mutex
	calls int
}

func (s *failNthActionAuditRealm) AppendAudit(
	ctx context.Context, entry store.AuditEntry,
) error {
	if entry.Action == s.action {
		s.mu.Lock()
		s.calls++
		fail := s.calls == s.n
		s.mu.Unlock()
		if fail {
			return s.err
		}
	}
	return s.RealmStore.AppendAudit(ctx, entry)
}

// failRollbackRestoreRealm fails the rollback RestoreBackup (the second restore
// call) while delegating the first to the wrapped realm store.
type failRollbackRestoreRealm struct {
	store.RealmStore
	rollbackErr  error
	restoreCalls int
}

func (s *failRollbackRestoreRealm) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	s.restoreCalls++
	if s.restoreCalls > 1 {
		return backup.RestoreSummary{}, s.rollbackErr
	}
	return s.RealmStore.RestoreBackup(ctx, archive, opts)
}

type failAccountAdjustmentRecordRealm struct {
	store.RealmStore
	err error
}

func (s *failAccountAdjustmentRecordRealm) RecordAccountAdjustment(
	_ context.Context, _ store.AccountAdjustmentPersistence,
) (domain.AccountAdjustmentRecord, error) {
	return domain.AccountAdjustmentRecord{}, s.err
}

type accountAdjustmentRecordProbeRealm struct {
	store.RealmStore
	records []store.AccountAdjustmentPersistence
}

func (s *accountAdjustmentRecordProbeRealm) RecordAccountAdjustment(
	ctx context.Context, in store.AccountAdjustmentPersistence,
) (domain.AccountAdjustmentRecord, error) {
	s.records = append(s.records, in)
	return s.RealmStore.RecordAccountAdjustment(ctx, in)
}

type accountReadGuardRealm struct {
	store.RealmStore
	inOrderApply           bool
	accountReadDuringApply bool
}

func (s *accountReadGuardRealm) RecordOrderSubmission(
	ctx context.Context,
	o domain.Order,
	submitted domain.OrderEvent,
	apply func(domain.Order) (domain.OrderSettlement, error),
) (domain.Order, error) {
	return s.RealmStore.RecordOrderSubmission(
		ctx,
		o,
		submitted,
		func(persisted domain.Order) (domain.OrderSettlement, error) {
			s.inOrderApply = true
			defer func() {
				s.inOrderApply = false
			}()
			return apply(persisted)
		},
	)
}

func (s *accountReadGuardRealm) GetAccount(
	ctx context.Context, code domain.AccountID,
) (domain.Account, bool, error) {
	if s.inOrderApply {
		s.accountReadDuringApply = true
		return domain.Account{}, false, errors.New(
			"GetAccount called during order submission apply",
		)
	}
	return s.RealmStore.GetAccount(ctx, code)
}

type failOrderSubmissionAfterApplyRealm struct {
	store.RealmStore
	err error
}

func (s *failOrderSubmissionAfterApplyRealm) RecordOrderSubmission(
	_ context.Context,
	o domain.Order,
	_ domain.OrderEvent,
	apply func(domain.Order) (domain.OrderSettlement, error),
) (domain.Order, error) {
	if o.ExternalID.IsZero() {
		o.ExternalID = domain.ExternalID("post-engine-failed-order")
	}
	if _, err := apply(o); err != nil {
		return domain.Order{}, err
	}
	return domain.Order{}, s.err
}

// failOrderSettlementRealm fails the atomic RecordOrderSettlement store write
// while delegating everything else. The execution-report path applies the fill
// to the engine first, so a settlement-write failure is a post-engine
// persistence failure that must fail-stop.
type failOrderSettlementRealm struct {
	store.RealmStore
	err error
}

func (s *failOrderSettlementRealm) RecordOrderSettlement(
	_ context.Context, _ domain.OrderSettlement,
) (domain.ExternalID, error) {
	return "", s.err
}

// laneProbeRealm can hold a block/group store write open. A node test submits a
// second task on the same SDK lane while the write is held to prove the caller's
// persistence remains inside the administrative chain.
type laneProbeRealm struct {
	store.RealmStore
	failErr error
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *laneProbeRealm) holdWrite() {
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	if s.release != nil {
		<-s.release
	}
}

func (s *laneProbeRealm) SetAccountBlocked(
	ctx context.Context, code domain.AccountID, blocked bool, reason string,
) error {
	s.holdWrite()
	if s.failErr != nil {
		return s.failErr
	}
	return s.RealmStore.SetAccountBlocked(ctx, code, blocked, reason)
}

func (s *laneProbeRealm) SetAccountGroup(
	ctx context.Context, code domain.AccountID, groupCode string,
) error {
	s.holdWrite()
	if s.failErr != nil {
		return s.failErr
	}
	return s.RealmStore.SetAccountGroup(ctx, code, groupCode)
}

func (s *laneProbeRealm) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string,
) error {
	s.holdWrite()
	if s.failErr != nil {
		return s.failErr
	}
	return s.RealmStore.SetGroupBlocked(ctx, code, blocked, reason)
}

// newTestNode builds a localNode over a real temp SQLite store and the fake
// engine. NewLocalNode binds the realm, seeds the build from the (freshly
// migrated, empty) store and writes one startup hydrate audit row, so a fresh
// node already has exactly one audit row. It returns the node and the bound
// realm handle for direct store assertions.
func newTestNode(t *testing.T, eng *fakeEngine) (*localNode, store.RealmStore) {
	t.Helper()
	st := newMemoryStore("node.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var seed engine.Snapshot
	n, _, err := NewLocalNode(ctx, st, fakeBuild(eng, &seed))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	local := n.(*localNode)
	seedTestPrincipal(t, local)
	return local, local.realm
}

// newTestNodeWithStore builds a localNode over the supplied store (typically a
// realmWrapStore around a real temp SQLite store) and the fake engine. It mirrors
// newTestNode but lets a test inject a failing realm decorator.
func newTestNodeWithStore(
	t *testing.T, st store.Store, eng *fakeEngine, opts ...LocalOption,
) *localNode {
	t.Helper()
	var seed engine.Snapshot
	n, _, err := NewLocalNode(context.Background(), st, fakeBuild(eng, &seed), opts...)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	local := n.(*localNode)
	seedTestPrincipal(t, local)
	return local
}

// seedTestPrincipal registers the operator principal a node mutation stamps as
// the audit actor and the assets every machine record links to. The store
// enforces referential integrity: an audit whose Actor names a principal that
// does not exist, or an order/balance/limit linking an unknown asset code, is
// rejected. The dictionary rows must therefore be present before any audited
// mutation or machine-record write runs. ErrAlreadyExists is tolerated so the
// helper is idempotent across re-seeds (e.g. after a ResetDatabase rebind).
func seedTestPrincipal(t *testing.T, n *localNode) {
	t.Helper()
	ctx := context.Background()
	err := n.realm.CreatePrincipal(ctx, domain.Principal{Code: testCaller.Principal})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreatePrincipal(%s): %v", testCaller.Principal, err)
	}
	if eng, ok := n.currentEngine().(*fakeEngine); ok && eng.enforceResolver {
		for _, code := range []string{"USD", "EUR", "AAPL"} {
			if _, err := n.CreateAsset(ctx, domain.Asset{Code: code}, testCaller); err != nil &&
				!errors.Is(err, domain.ErrAlreadyExists) {
				t.Fatalf("CreateAsset(%s): %v", code, err)
			}
		}
		return
	}
	for _, code := range []string{"USD", "EUR", "AAPL"} {
		asset, err := n.realm.CreateAsset(ctx, domain.Asset{Code: code})
		if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
		if errors.Is(err, domain.ErrAlreadyExists) {
			var ok bool
			asset, ok, err = n.realm.GetAsset(ctx, code)
			if err != nil || !ok {
				t.Fatalf("GetAsset(%s): ok=%v err=%v", code, ok, err)
			}
		}
		eng, ok := n.currentEngine().(*fakeEngine)
		if !ok {
			continue
		}
		eng.resolverMu.RLock()
		_, published := eng.assetResolverIDs[code]
		eng.resolverMu.RUnlock()
		if !published {
			if err := eng.AddAssetResolverEntry(asset); err != nil {
				t.Fatalf("AddAssetResolverEntry(%s): %v", code, err)
			}
		}
	}
}

// seedTestAccount registers an account dictionary row so the machine records a
// test attaches to it (orders, balances, account-scope limits) resolve their
// foreign key. ErrAlreadyExists is tolerated.
func seedTestAccount(t *testing.T, realm store.RealmStore, id domain.AccountID) {
	t.Helper()
	if _, err := realm.CreateAccount(context.Background(), domain.Account{Code: id}); err != nil &&
		!errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateAccount(%s): %v", id, err)
	}
}

func testKey(id domain.AccountID) Key {
	return Key{Account: id}
}

func testAccount(id domain.AccountID) domain.Account {
	return domain.Account{Code: id}
}
