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
	"sync"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

type fakeEngine struct {
	running bool
	sink    marketdata.Sink

	enforceResolver    bool
	knownAccounts      map[domain.AccountID]struct{}
	knownGroups        map[string]struct{}
	accountResolverIDs map[domain.AccountID]domain.EngineAccountID
	groupResolverIDs   map[string]domain.EngineGroupID
	accountCurrencies  map[domain.AccountID]string
	groupCurrencies    map[string]string
	accountGroups      map[domain.AccountID]string
	resolverMu         sync.RWMutex

	// accountPnlCalls records every engine P&L assignment in order, and
	// accountPnlBlocks are the blocks the engine reports back for an account.
	accountPnlCalls      []accountPnlCall
	accountPnlStateCalls []accountPnlStateCall
	accountPnlBlocks     map[domain.AccountID][]domain.AccountBlock

	configureCalls  []configureCall
	configureBlocks []domain.AccountBlock
	blockCalls      []blockCall
	unblockCalls    []domain.AccountID

	adjustmentCalls        []adjustmentCall
	adjustmentBatchCalls   []adjustmentBatchCall
	adjustmentBatchResults []engine.AdjustmentResult
	submitCalls            []domain.Order
	execReportCalls        []domain.ExecutionReportInput
	registerGroupCalls     []groupCall
	unregisterGroupCalls   []groupCall
	blockGroupCalls        []blockGroupCall
	unblockGroupCalls      []string

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
	submitReservedQuantity     string
	submitReject               *domain.OrderReject
	execReportBlocks           []domain.ExecutionAccountBlock
	execReportOutcomes         []engine.BalanceOutcome
	execReportReservedQuantity string
	emptyExecReportPersistence bool
	stateMu                    sync.Mutex
	accountLanesMu             sync.Mutex
	accountLanes               map[domain.AccountID]*sync.Mutex
	accountSyncCalls           []domain.AccountID
	groupSyncCalls             []string
	inAccountSync              int
	laneDepth                  int
	operationOutsideSync       bool
	execReportOutsideSync      bool

	// Canned dry-run outcome and recorded probes for the check path.
	checkResult domain.CheckResult
	checkProbes []domain.OrderProbe

	// configureErr, when set, is returned by ConfigurePolicy instead of the
	// generic failure; it lets a test assert a specific wrapped sentinel
	// propagates through the node.
	configureErr       error
	accountCurrencyErr error
	groupCurrencyErr   error
	groupCurrencyFails map[int]error
	groupCurrencyCalls int
	accountPnlErr      error

	failConfigure     bool
	failBlock         bool
	failAdjustment    bool
	failSubmit        bool
	failExecReport    bool
	failGroup         bool
	failRegisterGroup string
	submitEntered     chan domain.AccountID
	submitRelease     <-chan struct{}
	blockGroupEntered chan string
	blockGroupRelease <-chan struct{}
}

type configureCall struct {
	policy string
	limits engine.LimitSet
}

type blockCall struct {
	id     domain.AccountID
	reason string
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

type groupCall struct {
	accounts []domain.AccountID
	groupID  string
}

type blockGroupCall struct {
	groupID string
	reason  string
}

var _ engine.DictionaryResolver = (*fakeEngine)(nil)

// fakeExecutionReportCarriesFill deliberately mirrors the engine's fill
// predicate: the fake emits a fill event and trade exactly when the real engine
// would, so a report input with both a fill quantity and price drives the fill
// legs of the persistence the node persists.
func fakeExecutionReportCarriesFill(in domain.ExecutionReportInput) bool {
	return in.FillQuantity != "" && in.FillPrice != ""
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		running:      true,
		sink:         nopSink{},
		accountLanes: make(map[domain.AccountID]*sync.Mutex),
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
	if err := eng.RenameGroupResolverEntry("group-old", domain.AccountGroup{
		Code: "group-new", EngineGroupID: 9,
	}); err != nil {
		t.Fatalf("RenameGroupResolverEntry: %v", err)
	}

	if err := eng.RunAccountSynchronized(
		context.Background(), "account-new", func(engine.AccountLane) error { return nil },
	); err != nil {
		t.Fatalf("RunAccountSynchronized renamed alias: %v", err)
	}
	if err := eng.RunGroupSynchronized(
		context.Background(), "group-new", func(engine.GroupLane) error { return nil },
	); err != nil {
		t.Fatalf("RunGroupSynchronized renamed alias: %v", err)
	}
	if err := eng.RenameAccountResolverEntry("account-new", domain.Account{
		Code: "account-bad", EngineAccountID: 99,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched account id error = %v, want ErrInvalid", err)
	}
	if err := eng.RunAccountSynchronized(
		context.Background(), "account-new", func(engine.AccountLane) error { return nil },
	); err != nil {
		t.Fatalf("failed rename changed existing alias: %v", err)
	}
	if err := eng.RemoveGroupResolverEntry(domain.AccountGroup{
		Code: "group-new", EngineGroupID: 11,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched group removal error = %v, want ErrInvalid", err)
	}
	if err := eng.RunGroupSynchronized(
		context.Background(), "group-new", func(engine.GroupLane) error { return nil },
	); err != nil {
		t.Fatalf("failed removal changed existing group alias: %v", err)
	}
	if err := eng.RemoveGroupResolverEntry(domain.AccountGroup{
		Code: "group-new", EngineGroupID: 9,
	}); err != nil {
		t.Fatalf("RemoveGroupResolverEntry: %v", err)
	}
	if err := eng.RunGroupSynchronized(
		context.Background(), "group-new", func(engine.GroupLane) error { return nil },
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("removed group alias error = %v, want ErrInvalid", err)
	}
	if got := eng.MarketDataSink(); got != sinkBefore {
		t.Fatal("fake resolver mutation replaced MarketDataSink")
	}
}

func TestFakeEngineSetAccountPnlState(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.accountPnlBlocks = map[domain.AccountID][]domain.AccountBlock{
		"account": {{Account: "account", Reason: "halted"}},
	}
	sinkBefore := eng.MarketDataSink()

	if _, err := eng.SetAccountPnlState(
		context.Background(), "account", "2.5", "",
	); err != nil {
		t.Fatalf("numeric SetAccountPnlState: %v", err)
	}
	blocks, err := eng.SetAccountPnlState(
		context.Background(), "account", "", domain.PnlHaltReasonMissingFx,
	)
	if err != nil {
		t.Fatalf("halted SetAccountPnlState: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Account != "account" {
		t.Fatalf("halted blocks = %+v, want account block", blocks)
	}
	want := []accountPnlStateCall{
		{id: "account", pnl: "2.5"},
		{id: "account", haltReason: domain.PnlHaltReasonMissingFx},
	}
	if !reflect.DeepEqual(eng.accountPnlStateCalls, want) {
		t.Fatalf("account pnl state calls = %+v, want %+v", eng.accountPnlStateCalls, want)
	}

	for _, test := range []struct {
		name       string
		pnl        string
		haltReason domain.PnlHaltReason
	}{
		{name: "both", pnl: "1", haltReason: domain.PnlHaltReasonMissingFx},
		{name: "empty"},
		{name: "unknown reason", haltReason: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := eng.SetAccountPnlState(
				context.Background(), "account", test.pnl, test.haltReason,
			)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("SetAccountPnlState error = %v, want ErrInvalid", err)
			}
		})
	}
	if len(eng.accountPnlStateCalls) != 2 {
		t.Fatalf("invalid states were recorded: %+v", eng.accountPnlStateCalls)
	}
	if eng.MarketDataSink() != sinkBefore {
		t.Fatal("SetAccountPnlState replaced fake MarketDataSink")
	}
}

func (e *fakeEngine) requireAccountSync() {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.inAccountSync == 0 {
		e.operationOutsideSync = true
	}
}

// insideLane reports whether an account or group lane closure is currently
// executing on this engine. A store decorator reads it to prove an admin
// method's store write runs inside the lane, not before or after it.
func (e *fakeEngine) insideLane() bool {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.laneDepth > 0
}

func (e *fakeEngine) accountLane(account domain.AccountID) *sync.Mutex {
	e.accountLanesMu.Lock()
	defer e.accountLanesMu.Unlock()
	if e.accountLanes == nil {
		e.accountLanes = make(map[domain.AccountID]*sync.Mutex)
	}
	lane := e.accountLanes[account]
	if lane == nil {
		lane = &sync.Mutex{}
		e.accountLanes[account] = lane
	}
	return lane
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

func (e *fakeEngine) checkKnownAccounts(accounts []domain.AccountID) error {
	for _, account := range accounts {
		if err := e.checkKnownAccount(account); err != nil {
			return err
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

func (e *fakeEngine) BlockAccount(
	_ context.Context, id domain.AccountID, reason string,
) error {
	if err := e.checkKnownAccount(id); err != nil {
		return err
	}
	if e.failBlock {
		return errors.New("block failed")
	}
	e.blockCalls = append(e.blockCalls, blockCall{id, reason})
	return nil
}

func (e *fakeEngine) UnblockAccount(_ context.Context, id domain.AccountID) error {
	if err := e.checkKnownAccount(id); err != nil {
		return err
	}
	if e.failBlock {
		return errors.New("unblock failed")
	}
	e.unblockCalls = append(e.unblockCalls, id)
	return nil
}

func (e *fakeEngine) SetAccountCurrency(
	_ context.Context, id domain.AccountID, currency string,
) error {
	if err := e.checkKnownAccount(id); err != nil {
		return err
	}
	if e.accountCurrencyErr != nil {
		return e.accountCurrencyErr
	}
	if e.accountCurrencies == nil {
		e.accountCurrencies = map[domain.AccountID]string{}
	}
	e.accountCurrencies[id] = currency
	return nil
}

func (e *fakeEngine) ClearAccountCurrency(
	_ context.Context, id domain.AccountID,
) error {
	if err := e.checkKnownAccount(id); err != nil {
		return err
	}
	delete(e.accountCurrencies, id)
	return nil
}

func (e *fakeEngine) SetGroupCurrency(
	_ context.Context, groupID, currency string,
) error {
	if err := e.checkKnownGroup(groupID); err != nil {
		return err
	}
	e.groupCurrencyCalls++
	if err := e.groupCurrencyFails[e.groupCurrencyCalls]; err != nil {
		return err
	}
	if e.groupCurrencyErr != nil {
		return e.groupCurrencyErr
	}
	if e.groupCurrencies == nil {
		e.groupCurrencies = map[string]string{}
	}
	e.groupCurrencies[groupID] = currency
	return nil
}

func (e *fakeEngine) ClearGroupCurrency(
	_ context.Context, groupID string,
) error {
	if err := e.checkKnownGroup(groupID); err != nil {
		return err
	}
	e.groupCurrencyCalls++
	if err := e.groupCurrencyFails[e.groupCurrencyCalls]; err != nil {
		return err
	}
	if e.groupCurrencyErr != nil {
		return e.groupCurrencyErr
	}
	delete(e.groupCurrencies, groupID)
	return nil
}

func (e *fakeEngine) effectiveAccountCurrency(account domain.AccountID) string {
	if currency := e.accountCurrencies[account]; currency != "" {
		return currency
	}
	if currency := e.groupCurrencies[e.accountGroups[account]]; currency != "" {
		return currency
	}
	return e.groupCurrencies[""]
}

func (e *fakeEngine) SetAccountPnl(
	ctx context.Context, id domain.AccountID, pnl string,
) ([]domain.AccountBlock, error) {
	return e.SetAccountPnlState(ctx, id, pnl, "")
}

func (e *fakeEngine) SetAccountPnlState(
	_ context.Context,
	id domain.AccountID,
	pnl string,
	haltReason domain.PnlHaltReason,
) ([]domain.AccountBlock, error) {
	if err := e.checkKnownAccount(id); err != nil {
		return nil, err
	}
	if e.accountPnlErr != nil {
		return nil, e.accountPnlErr
	}
	if pnl != "" && haltReason != "" {
		return nil, fmt.Errorf(
			"engine: account pnl state has both value %q and halt reason %q: %w",
			pnl, haltReason, domain.ErrInvalid,
		)
	}
	if err := domain.ValidatePnlHaltReason(haltReason); err != nil {
		return nil, err
	}
	if pnl == "" && haltReason == "" {
		return nil, fmt.Errorf("engine: account pnl state is empty: %w", domain.ErrInvalid)
	}
	e.accountPnlStateCalls = append(e.accountPnlStateCalls, accountPnlStateCall{
		id: id, pnl: pnl, haltReason: haltReason,
	})
	if haltReason == "" {
		e.accountPnlCalls = append(e.accountPnlCalls, accountPnlCall{id, pnl})
	}
	if e.accountPnlBlocks == nil {
		return nil, nil
	}
	return e.accountPnlBlocks[id], nil
}

func (e *fakeEngine) ApplyAccountAdjustment(
	ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	e.requireAccountSync()
	results, batchReject, err := e.ApplyAccountAdjustmentBatch(ctx, account,
		[]domain.AdjustmentRequest{req})
	if err != nil {
		return engine.AdjustmentResult{}, err
	}
	if batchReject != nil {
		return engine.AdjustmentResult{Rejected: batchReject}, nil
	}
	if len(results) == 0 {
		return engine.AdjustmentResult{}, nil
	}
	if len(results) != 1 {
		return engine.AdjustmentResult{}, errors.New("unexpected adjustment result count")
	}
	return results[0], nil
}

func (e *fakeEngine) ApplyAccountAdjustmentBatch(
	_ context.Context, account domain.AccountID, reqs []domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	e.requireAccountSync()
	if err := e.checkKnownAccount(account); err != nil {
		return nil, nil, err
	}
	if e.failAdjustment {
		return nil, nil, errors.New("adjustment failed")
	}
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

func (e *fakeEngine) SubmitOrder(
	_ context.Context, o domain.Order,
) (engine.OrderResult, error) {
	e.requireAccountSync()
	if err := e.checkKnownAccount(o.Account); err != nil {
		return engine.OrderResult{}, err
	}
	if e.failSubmit {
		return engine.OrderResult{}, errors.New("submit failed")
	}
	e.stateMu.Lock()
	e.submitCalls = append(e.submitCalls, o)
	e.stateMu.Unlock()
	if e.submitEntered != nil {
		e.submitEntered <- o.Account
	}
	if e.submitRelease != nil {
		<-e.submitRelease
	}
	if e.submitReject != nil {
		return engine.OrderResult{Accepted: false, Rejects: []domain.OrderReject{*e.submitReject}}, nil
	}
	return engine.OrderResult{
		Accepted:            true,
		Lock:                e.submitLock,
		Blocks:              e.submitBlocks,
		Outcomes:            e.submitOutcomes,
		SettlementLockPrice: o.Price,
	}, nil
}

func (e *fakeEngine) SubmitImmediate(
	_ context.Context, o domain.Order,
) (engine.ImmediateResult, error) {
	e.requireAccountSync()
	if err := e.checkKnownAccount(o.Account); err != nil {
		return engine.ImmediateResult{}, err
	}
	if e.failSubmit {
		return engine.ImmediateResult{}, errors.New("submit immediate failed")
	}
	e.stateMu.Lock()
	e.submitCalls = append(e.submitCalls, o)
	e.stateMu.Unlock()
	if e.submitReject != nil {
		return engine.ImmediateResult{Accepted: false, Rejects: []domain.OrderReject{*e.submitReject}}, nil
	}
	settlementPrice := e.submitSettlementLockPrice
	if settlementPrice == "" {
		settlementPrice = o.Price
	}
	tradePrice := e.submitTradePrice
	if tradePrice == "" {
		tradePrice = o.Price
		if tradePrice == "" {
			tradePrice = settlementPrice
		}
	}
	reportInput := domain.ExecutionReportInput{
		BaseAsset:      o.BaseAsset,
		QuoteAsset:     o.QuoteAsset,
		FillQuantity:   o.AmountValue,
		FillPrice:      tradePrice,
		LeavesQuantity: "0",
		LockPrice:      settlementPrice,
		Lock:           append([]byte(nil), e.submitLock...),
		Order:          o.ExternalID,
		Account:        o.Account,
		Side:           o.Side,
		OrderStatus:    domain.OrderStatusFilled,
	}
	request := domain.ExecutionReportRequestFromInput(reportInput)
	persistence := engine.ExecutionReportPersistence{
		Trade: &domain.Trade{
			Order:      o.ExternalID,
			Account:    o.Account,
			BaseAsset:  o.BaseAsset,
			QuoteAsset: o.QuoteAsset,
			Side:       o.Side,
			Quantity:   reportInput.FillQuantity,
			Price:      reportInput.FillPrice,
			LockPrice:  reportInput.LockPrice,
		},
		OrderStatus:          domain.OrderStatusFilled,
		AccountPnl:           e.submitAccountPnl,
		AccountPnlHaltReason: e.submitAccountPnlHaltReason,
		Leaves:               reportInput.LeavesQuantity,
		ReservedQuantity:     e.submitReservedQuantity,
		Balances:             balanceSettlementsFrom(e.submitOutcomes),
		Events: []domain.OrderEvent{{
			Order: o.ExternalID,
			Type:  domain.OrderEventFill,
			Payload: domain.OrderEventPayload{
				FillQuantity:   reportInput.FillQuantity,
				FillPrice:      reportInput.FillPrice,
				FillLockPrice:  reportInput.LockPrice,
				LeavesQuantity: reportInput.LeavesQuantity,
				OrderStatus:    string(reportInput.OrderStatus),
			},
		}},
	}
	return engine.ImmediateResult{
		Accepted:             true,
		Persistence:          &persistence,
		ExecutionReport:      request,
		Lock:                 e.submitLock,
		Outcomes:             e.submitOutcomes,
		AccountPnl:           e.submitAccountPnl,
		AccountPnlHaltReason: e.submitAccountPnlHaltReason,
		SettlementLockPrice:  settlementPrice,
		FillQuantity:         o.AmountValue,
		LeavesQuantity:       "0",
		TradePrice:           tradePrice,
	}, nil
}

func (e *fakeEngine) RunAccountSynchronized(
	_ context.Context, account domain.AccountID, fn func(engine.AccountLane) error,
) error {
	// Mirror the real adapter: resolve the account before entering the lane, so a
	// brand-new account rejects here (and its callback never runs) unless a
	// pre-lane rebuild has already registered it.
	if err := e.checkKnownAccount(account); err != nil {
		return err
	}
	lane := e.accountLane(account)
	lane.Lock()
	defer lane.Unlock()
	e.stateMu.Lock()
	e.accountSyncCalls = append(e.accountSyncCalls, account)
	e.inAccountSync++
	e.laneDepth++
	e.stateMu.Unlock()
	defer func() {
		e.stateMu.Lock()
		e.inAccountSync--
		e.laneDepth--
		e.stateMu.Unlock()
	}()
	return fn(e)
}

func (e *fakeEngine) RunGroupSynchronized(
	_ context.Context, groupID string, fn func(engine.GroupLane) error,
) error {
	if err := e.checkKnownGroup(groupID); err != nil {
		return err
	}
	e.stateMu.Lock()
	e.groupSyncCalls = append(e.groupSyncCalls, groupID)
	e.laneDepth++
	e.stateMu.Unlock()
	defer func() {
		e.stateMu.Lock()
		e.laneDepth--
		e.stateMu.Unlock()
	}()
	return fn(e)
}

func (e *fakeEngine) ApplyExecutionReport(
	_ context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	e.stateMu.Lock()
	outsideSync := e.inAccountSync == 0
	e.stateMu.Unlock()
	if outsideSync {
		e.stateMu.Lock()
		e.operationOutsideSync = true
		e.execReportOutsideSync = true
		e.stateMu.Unlock()
	}
	if err := e.checkKnownAccount(in.Account); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	if e.failExecReport {
		return engine.ExecutionReportResult{}, errors.New("exec report failed")
	}
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
		Trade:            trade,
		Commission:       in.Commission,
		OrderStatus:      in.OrderStatus,
		Leaves:           in.LeavesQuantity,
		ReservedQuantity: e.execReportReservedQuantity,
		Balances:         balanceSettlementsFrom(e.execReportOutcomes),
		Events:           events,
		Blocks:           e.execReportBlocks,
	}
	return engine.ExecutionReportResult{
		Persistence: &persistence,
		Blocks:      e.execReportBlocks,
		Outcomes:    e.execReportOutcomes,
	}, nil
}

func (e *fakeEngine) RegisterGroup(
	_ context.Context, accounts []domain.AccountID, groupID string,
) error {
	if err := e.checkKnownAccounts(accounts); err != nil {
		return err
	}
	if err := e.checkKnownGroup(groupID); err != nil {
		return err
	}
	if e.failGroup {
		return errors.New("register group failed")
	}
	if e.failRegisterGroup == groupID {
		return errors.New("register group failed")
	}
	e.registerGroupCalls = append(e.registerGroupCalls, groupCall{accounts, groupID})
	if e.accountGroups == nil {
		e.accountGroups = map[domain.AccountID]string{}
	}
	for _, account := range accounts {
		e.accountGroups[account] = groupID
	}
	return nil
}

func (e *fakeEngine) UnregisterGroup(
	_ context.Context, accounts []domain.AccountID, groupID string,
) error {
	if err := e.checkKnownAccounts(accounts); err != nil {
		return err
	}
	if err := e.checkKnownGroup(groupID); err != nil {
		return err
	}
	if e.failGroup {
		return errors.New("unregister group failed")
	}
	e.unregisterGroupCalls = append(e.unregisterGroupCalls, groupCall{accounts, groupID})
	for _, account := range accounts {
		if e.accountGroups[account] == groupID {
			delete(e.accountGroups, account)
		}
	}
	return nil
}

func (e *fakeEngine) BlockGroup(_ context.Context, groupID, reason string) error {
	if err := e.checkKnownGroup(groupID); err != nil {
		return err
	}
	if e.failGroup {
		return errors.New("block group failed")
	}
	if e.blockGroupEntered != nil {
		e.blockGroupEntered <- groupID
	}
	if e.blockGroupRelease != nil {
		<-e.blockGroupRelease
	}
	e.stateMu.Lock()
	e.blockGroupCalls = append(e.blockGroupCalls, blockGroupCall{groupID, reason})
	e.stateMu.Unlock()
	return nil
}

func (e *fakeEngine) UnblockGroup(_ context.Context, groupID string) error {
	if err := e.checkKnownGroup(groupID); err != nil {
		return err
	}
	if e.failGroup {
		return errors.New("unblock group failed")
	}
	e.unblockGroupCalls = append(e.unblockGroupCalls, groupID)
	return nil
}

func (e *fakeEngine) CheckOrder(
	_ context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	e.requireAccountSync()
	e.checkProbes = append(e.checkProbes, probe)
	return e.checkResult, nil
}

// MarketDataSink returns the stable no-op sink owned by this fake handle.
func (e *fakeEngine) MarketDataSink() marketdata.Sink { return e.sink }

func (e *fakeEngine) Stop() { e.running = false }

// nopSink is a Sink that drops every quote; it stands in for the engine's sink
// in node tests that never push.
type nopSink struct{}

func (nopSink) Push(marketdata.QuoteUpdate) error { return nil }
func (nopSink) Clear(string, string) error        { return nil }

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

// laneProbeRealm records, for each block/group store write, whether a lane
// closure was executing on eng at the moment of the write. It lets a node test
// prove the store write is serialized inside the RunAccountSynchronized/
// RunGroupSynchronized closure rather than before or after it. When failErr is
// set the corresponding store write returns it, driving the in-lane revert path.
type laneProbeRealm struct {
	store.RealmStore
	eng     *fakeEngine
	inLane  []bool
	failErr error
}

func (s *laneProbeRealm) SetAccountBlocked(
	ctx context.Context, code domain.AccountID, blocked bool, reason string,
) error {
	s.inLane = append(s.inLane, s.eng.insideLane())
	if s.failErr != nil {
		return s.failErr
	}
	return s.RealmStore.SetAccountBlocked(ctx, code, blocked, reason)
}

func (s *laneProbeRealm) SetAccountGroup(
	ctx context.Context, code domain.AccountID, groupCode string,
) error {
	s.inLane = append(s.inLane, s.eng.insideLane())
	if s.failErr != nil {
		return s.failErr
	}
	return s.RealmStore.SetAccountGroup(ctx, code, groupCode)
}

func (s *laneProbeRealm) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string,
) error {
	s.inLane = append(s.inLane, s.eng.insideLane())
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
	seedTestPrincipal(t, local.realm)
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
	seedTestPrincipal(t, local.realm)
	return local
}

// seedTestPrincipal registers the operator principal a node mutation stamps as
// the audit actor and the assets every machine record links to. The store
// enforces referential integrity: an audit whose Actor names a principal that
// does not exist, or an order/balance/limit linking an unknown asset code, is
// rejected. The dictionary rows must therefore be present before any audited
// mutation or machine-record write runs. ErrAlreadyExists is tolerated so the
// helper is idempotent across re-seeds (e.g. after a ResetDatabase rebind).
func seedTestPrincipal(t *testing.T, realm store.RealmStore) {
	t.Helper()
	ctx := context.Background()
	err := realm.CreatePrincipal(ctx, domain.Principal{Code: testCaller.Principal})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreatePrincipal(%s): %v", testCaller.Principal, err)
	}
	for _, code := range []string{"USD", "EUR", "AAPL"} {
		if err := realm.CreateAsset(ctx, domain.Asset{Code: code}); err != nil &&
			!errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("CreateAsset(%s): %v", code, err)
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
