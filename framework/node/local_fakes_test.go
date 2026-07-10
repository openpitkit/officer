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
	"slices"
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

	enforceResolver   bool
	knownAccounts     map[domain.AccountID]struct{}
	knownGroups       map[string]struct{}
	accountCurrencies map[domain.AccountID]string

	configureCalls []configureCall
	blockCalls     []blockCall
	unblockCalls   []domain.AccountID

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
	submitOutcomes             []engine.BalanceOutcome
	holdOutcomes               []engine.BalanceOutcome
	submitReject               *domain.OrderReject
	execReportBlocks           []domain.ExecutionAccountBlock
	execReportOutcomes         []engine.BalanceOutcome
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
	reconcileCount             int

	// Canned dry-run outcome and recorded probes for the check path.
	checkResult domain.CheckResult
	checkProbes []domain.OrderProbe

	// configureErr, when set, is returned by ConfigurePolicy instead of the
	// generic failure; it lets a test assert a specific wrapped sentinel
	// propagates through the node.
	configureErr       error
	accountCurrencyErr error

	failConfigure     bool
	failBlock         bool
	failAdjustment    bool
	failSubmit        bool
	commitErr         error
	rollbackErr       error
	failExecReport    bool
	failGroup         bool
	failRegisterGroup string
	submitEntered     chan domain.AccountID
	submitRelease     <-chan struct{}
	blockGroupEntered chan string
	blockGroupRelease <-chan struct{}

	// resolveMu guards the held-resolution call logs so concurrent
	// ConfirmHeld/CancelHeld goroutines can record under the race detector.
	resolveMu         sync.Mutex
	commitHeldCalls   []string
	rollbackHeldCalls []string
}

type configureCall struct {
	policy string
	limits engine.LimitSet
}

type blockCall struct {
	id     domain.AccountID
	reason string
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

func groupCallEqual(left, right groupCall) bool {
	return left.groupID == right.groupID &&
		slices.Equal(left.accounts, right.accounts)
}

type blockGroupCall struct {
	groupID string
	reason  string
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
		running:      true,
		accountLanes: make(map[domain.AccountID]*sync.Mutex),
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
		eng.knownAccounts = map[domain.AccountID]struct{}{}
		for _, account := range snap.Accounts {
			eng.knownAccounts[account.Code] = struct{}{}
		}
		eng.knownGroups = map[string]struct{}{}
		for _, group := range snap.Groups {
			eng.knownGroups[group.Code] = struct{}{}
		}
		eng.accountCurrencies = map[domain.AccountID]string{}
		for _, account := range snap.Accounts {
			eng.accountCurrencies[account.Code] = account.EffectiveCurrency
		}
		return eng, nil
	}
}

func (e *fakeEngine) checkKnownAccount(account domain.AccountID) error {
	if !e.enforceResolver {
		return nil
	}
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
	if _, ok := e.knownGroups[groupID]; !ok {
		return fmt.Errorf("engine: unknown group %q: %w", groupID, domain.ErrInvalid)
	}
	return nil
}

func (e *fakeEngine) Version() string      { return "fake" }
func (e *fakeEngine) BuildProfile() string { return "test" }
func (e *fakeEngine) Running() bool        { return e.running }

func (e *fakeEngine) ConfigurePolicy(
	_ context.Context, policy string, limits engine.LimitSet,
) error {
	if e.configureErr != nil {
		return e.configureErr
	}
	if e.failConfigure {
		return errors.New("configure failed")
	}
	e.configureCalls = append(e.configureCalls, configureCall{policy, limits})
	return nil
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
		Accepted: true,
		Lock:     e.submitLock,
		Outcomes: e.submitOutcomes,
	}, nil
}

// ReserveHold, CommitHeld, RollbackHeld, SubmitImmediate, and ReconcileOrphans
// satisfy the held-reservation surface of the Engine interface.
func (e *fakeEngine) ReserveHold(
	_ context.Context, o domain.Order,
) (engine.HoldResult, error) {
	e.requireAccountSync()
	if err := e.checkKnownAccount(o.Account); err != nil {
		return engine.HoldResult{}, err
	}
	if e.failSubmit {
		return engine.HoldResult{}, errors.New("reserve hold failed")
	}
	e.stateMu.Lock()
	e.submitCalls = append(e.submitCalls, o)
	e.stateMu.Unlock()
	if e.submitReject != nil {
		return engine.HoldResult{Accepted: false, Rejects: []domain.OrderReject{*e.submitReject}}, nil
	}
	return engine.HoldResult{
		Accepted: true,
		Lock:     e.submitLock,
		Outcomes: e.holdOutcomes,
	}, nil
}

func (e *fakeEngine) CommitHeld(_ context.Context, approvalID string) error {
	e.requireAccountSync()
	e.resolveMu.Lock()
	e.commitHeldCalls = append(e.commitHeldCalls, approvalID)
	e.resolveMu.Unlock()
	return e.commitErr
}

func (e *fakeEngine) RollbackHeld(_ context.Context, approvalID string) error {
	e.requireAccountSync()
	e.resolveMu.Lock()
	e.rollbackHeldCalls = append(e.rollbackHeldCalls, approvalID)
	e.resolveMu.Unlock()
	return e.rollbackErr
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
	return engine.ImmediateResult{
		Accepted:     true,
		Lock:         e.submitLock,
		FillQuantity: o.AmountValue,
	}, nil
}

func (e *fakeEngine) SetReservationStore(_ engine.ReservationStore) {}

func (e *fakeEngine) ReconcileOrphans(_ context.Context) (int, error) {
	return e.reconcileCount, nil
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
		Trade:       trade,
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

// MarketDataSink returns a no-op sink: the node tests do not exercise quote
// ingestion, and the fake engine carries no market-data service.
func (e *fakeEngine) MarketDataSink() marketdata.Sink { return nopSink{} }

func (e *fakeEngine) Stop() { e.running = false }

// nopSink is a Sink that drops every quote; it stands in for the engine's sink
// in node tests that never push.
type nopSink struct{}

func (nopSink) Push(marketdata.QuoteUpdate) error { return nil }

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

type noFullScanReservationRealm struct {
	store.RealmStore
	mu              sync.Mutex
	targetedLookups int
	fullScans       int
	failFullScan    bool
}

func (s *noFullScanReservationRealm) GetOpenReservationIntentByOrder(
	ctx context.Context, order domain.ExternalID,
) (domain.ReservationIntent, bool, error) {
	s.mu.Lock()
	s.targetedLookups++
	s.mu.Unlock()
	return s.RealmStore.GetOpenReservationIntentByOrder(ctx, order)
}

func (s *noFullScanReservationRealm) ListOpenReservationIntents(
	ctx context.Context,
) ([]domain.ReservationIntent, error) {
	s.mu.Lock()
	s.fullScans++
	fail := s.failFullScan
	s.mu.Unlock()
	if fail {
		return nil, errors.New("full reservation scan forbidden")
	}
	return s.RealmStore.ListOpenReservationIntents(ctx)
}

func (s *noFullScanReservationRealm) forbidFullScans() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targetedLookups = 0
	s.fullScans = 0
	s.failFullScan = true
}

func (s *noFullScanReservationRealm) lookupCounts() (targeted, fullScans int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetedLookups, s.fullScans
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

// failResolveRealm fails ResolveOrderReservation with a canned error while
// delegating everything else. It lets a node test assert that a store failure on
// the atomic resolve leaves no partial persistence and does not re-resolve the
// native handle.
type failResolveRealm struct {
	store.RealmStore
	err error
}

func (s *failResolveRealm) ResolveOrderReservation(
	_ context.Context, _ domain.ReservationResolution,
) error {
	return s.err
}

// failBusinessCSVImportRealm fails the final transactional ApplyBusinessCSVImport
// store write while delegating everything else, so a node test can drive the
// engine/store reconcile-on-failure path.
type failBusinessCSVImportRealm struct {
	store.RealmStore
	err error
}

func (s *failBusinessCSVImportRealm) ApplyBusinessCSVImport(
	ctx context.Context, in store.BusinessCSVImport,
) error {
	if len(in.Balances) == 0 && len(in.Adjustments) == 0 {
		return s.RealmStore.ApplyBusinessCSVImport(ctx, in)
	}
	return s.err
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
) error {
	return s.err
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
