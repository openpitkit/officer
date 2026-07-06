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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

// fakeEngine is a stand-in Engine that records calls and can be configured to
// fail the next mutating call. It is not the openpit adapter, so the node tests
// run without the native runtime.
type fakeEngine struct {
	running bool

	enforceResolver bool
	knownAccounts   map[domain.AccountID]struct{}
	knownGroups     map[string]struct{}

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
	configureErr error

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

func TestUpsertMarketDataInstrumentCreatesAssets(t *testing.T) {
	t.Parallel()
	n, realm := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	instance, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBinance,
		Label:    "Binance",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	instrument := domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "BTCUSDT",
		BaseAsset:      "BTC",
		QuoteAsset:     "USDT",
		Enabled:        true,
	}
	if err := n.UpsertMarketDataInstrument(ctx, instrument, testCaller); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	for _, code := range []string{"BTC", "USDT"} {
		asset, ok, err := realm.GetAsset(ctx, code)
		if err != nil || !ok {
			t.Fatalf("GetAsset(%s) = ok %v, err %v; want created asset", code, ok, err)
		}
		if asset.AssetClass != autoCreatedAssetClassCode {
			t.Fatalf("asset %s class = %q, want %q", code, asset.AssetClass,
				autoCreatedAssetClassCode)
		}
	}
	if class, ok, err := realm.GetAssetClass(ctx, autoCreatedAssetClassCode); err != nil || !ok {
		t.Fatalf("GetAssetClass(%s) = ok %v, err %v; want created class",
			autoCreatedAssetClassCode, ok, err)
	} else if class.Title != autoCreatedAssetClassTitle {
		t.Fatalf("auto-created class title = %q, want %q", class.Title,
			autoCreatedAssetClassTitle)
	} else if class.Notes != autoCreatedAssetClassNotes {
		t.Fatalf("auto-created class notes = %q, want %q", class.Notes,
			autoCreatedAssetClassNotes)
	}
	instruments, err := realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != 1 || instruments[0].ExternalSymbol != "BTCUSDT" {
		t.Fatalf("instruments = %+v, want BTCUSDT", instruments)
	}
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("create-asset audit rows = %+v, want BTC and USDT", rows)
	}
	for _, code := range []string{"BTC", "USDT"} {
		found := false
		for _, row := range rows {
			if row.Asset == code && strings.Contains(row.Detail,
				"by market-data instrument upsert") {
				found = true
			}
		}
		if !found {
			t.Fatalf("create-asset audit rows = %+v, want %s auto-created detail",
				rows, code)
		}
	}
}

// testCaller is the attribution the node tests stamp on mutations.
var testCaller = domain.Caller{Source: domain.SourceAPI, Principal: domain.PrincipalOperator}

// rateLimit builds a broker-scope rate-limit barrier with the given count/window
// for the tests that exercise the limit paths.
func rateLimit(scope domain.LimitScope, account domain.AccountID, asset string, max uint64, window time.Duration) domain.LimitRate {
	return domain.LimitRate{
		Scope:     scope,
		Account:   account,
		Asset:     asset,
		MaxOrders: max,
		Window:    window,
	}
}

func TestApplyBusinessCSVImport_BatchesAdjustmentsByAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{
			{Account: "acc-1", Asset: "USD", Available: "10"},
			{Account: "acc-1", Asset: "EUR", Available: "20"},
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	if len(eng.adjustmentBatchCalls) != 1 {
		t.Fatalf("adjustment batch calls = %+v, want one", eng.adjustmentBatchCalls)
	}
	call := eng.adjustmentBatchCalls[0]
	if call.account != "acc-1" || len(call.reqs) != 2 ||
		call.reqs[0].Asset != "USD" || call.reqs[1].Asset != "EUR" {
		t.Fatalf("adjustment batch call = %+v", call)
	}
	// The node applies both position snapshots as one per-account engine batch
	// (covered above) and hands the resulting adjustment records to the store as
	// part of the import. The store persists those adjustments transactionally
	// alongside the import's account/balances, so both snapshots leave a durable
	// adjustment row keyed to acc-1, newest first by asset.
	if _, ok, err := st.GetAccount(ctx, "acc-1"); err != nil || !ok {
		t.Fatalf("GetAccount after import: ok=%v err=%v, want present", ok, err)
	}
	adj, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(adj) != 2 {
		t.Fatalf("persisted adjustments = %d, want 2 (one per imported position)", len(adj))
	}
	gotAssets := []string{adj[0].Asset, adj[1].Asset}
	wantAssets := []string{"EUR", "USD"} // newest first; EUR was applied last.
	if !slices.Equal(gotAssets, wantAssets) {
		t.Fatalf("persisted adjustment assets = %v, want %v", gotAssets, wantAssets)
	}
	for _, rec := range adj {
		if rec.Accepted == nil || rec.Rejected != nil {
			t.Fatalf("adjustment %s outcome = %+v, want accepted", rec.Asset, rec)
		}
		if rec.ExternalID.IsZero() {
			t.Fatalf("adjustment %s has no external id", rec.Asset)
		}
	}
}

func TestApplyBusinessCSVImport_AppliesSparseAdjustmentBatch(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentBatchResults = []engine.AdjustmentResult{{
		Accepted: &domain.AdjustmentOutcomeAccepted{
			BalanceResult: "10",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{
			{Account: "acc-1", Asset: "USD", Available: "10"},
			{Account: "acc-1", Asset: "EUR", Available: "20"},
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	adj, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(adj) != 1 {
		t.Fatalf("persisted adjustments = %d, want only returned outcome", len(adj))
	}
	if adj[0].Asset != "USD" || adj[0].Accepted == nil {
		t.Fatalf("adjustment = %+v, want accepted USD only", adj[0])
	}
}

func TestApplyBusinessCSVImport_RoutesGroupMembershipByGroupLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{
			{Account: domain.Account{Code: "acc-1", GroupCode: "desk-a"}},
			{Account: domain.Account{Code: "acc-2", GroupCode: "desk-a"}},
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	want := []groupCall{
		{accounts: []domain.AccountID{"acc-1"}, groupID: "desk-a"},
		{accounts: []domain.AccountID{"acc-2"}, groupID: "desk-a"},
	}
	if !slices.EqualFunc(eng.registerGroupCalls, want, groupCallEqual) {
		t.Fatalf("register group calls = %+v, want %+v", eng.registerGroupCalls, want)
	}
	membershipSyncs := eng.groupSyncCalls[len(eng.groupSyncCalls)-2:]
	if !slices.Equal(membershipSyncs, []string{"desk-a", "desk-a"}) {
		t.Fatalf("group sync calls = %+v, want membership syncs on desk-a", eng.groupSyncCalls)
	}
}

// TestApplyBusinessCSVImport_CreatesBlocksAndMovesNewEntitiesWithResolver drives
// the import with the fake engine's resolver enforced, so BlockAccount/BlockGroup/
// RegisterGroup reject any account or group unknown to the engine. Importing a
// brand-new blocked account and group plus a group move for a new account only
// succeeds if the store write and engine rebuild register the entities before
// the engine effects run. It locks in that create+register+rebuild precedes the
// engine effect, guarding against a future reorder of an engine effect ahead of
// the rebuild.
func TestApplyBusinessCSVImport_CreatesBlocksAndMovesNewEntitiesWithResolver(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{
			{Account: domain.Account{
				Code: "acc-1", GroupCode: "desk-a", Blocked: true, BlockReason: "risk",
			}},
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	if len(eng.blockCalls) != 1 || eng.blockCalls[0].id != "acc-1" {
		t.Fatalf("block account calls = %+v, want one for acc-1", eng.blockCalls)
	}
	wantRegister := []groupCall{{accounts: []domain.AccountID{"acc-1"}, groupID: "desk-a"}}
	if !slices.EqualFunc(eng.registerGroupCalls, wantRegister, groupCallEqual) {
		t.Fatalf("register group calls = %+v, want %+v", eng.registerGroupCalls, wantRegister)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount acc-1: ok=%v err=%v, want present", ok, err)
	}
	if !account.Blocked || account.GroupCode != "desk-a" {
		t.Fatalf("account after import = %+v, want blocked in desk-a", account)
	}
}

func TestApplyBusinessCSVImport_RollsBackRegisteredGroupsOnGroupError(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.failRegisterGroup = "desk-b"
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	for _, group := range []string{"old-a", "old-b"} {
		if _, err := st.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
			t.Fatalf("CreateGroup(%s): %v", group, err)
		}
	}
	if _, err := st.CreateAccount(ctx, domain.Account{
		Code: "acc-1", GroupCode: "old-a",
	}); err != nil {
		t.Fatalf("CreateAccount(acc-1): %v", err)
	}
	if _, err := st.CreateAccount(ctx, domain.Account{
		Code: "acc-2", GroupCode: "old-b",
	}); err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{
			{
				Exists:  true,
				Account: domain.Account{Code: "acc-1", GroupCode: "desk-a"},
			},
			{
				Exists:  true,
				Account: domain.Account{Code: "acc-2", GroupCode: "desk-b"},
			},
		},
	}, testCaller)
	if err == nil {
		t.Fatal("ApplyBusinessCSVImport error = nil, want group error")
	}
	wantUnregister := []groupCall{
		{accounts: []domain.AccountID{"acc-1"}, groupID: "old-a"},
		{accounts: []domain.AccountID{"acc-2"}, groupID: "old-b"},
	}
	if !slices.EqualFunc(eng.unregisterGroupCalls, wantUnregister, groupCallEqual) {
		t.Fatalf("unregister group calls = %+v, want %+v",
			eng.unregisterGroupCalls, wantUnregister)
	}
	wantRegister := []groupCall{
		{accounts: []domain.AccountID{"acc-1"}, groupID: "desk-a"},
		{accounts: []domain.AccountID{"acc-2"}, groupID: "old-b"},
	}
	if !slices.EqualFunc(eng.registerGroupCalls, wantRegister, groupCallEqual) {
		t.Fatalf("register group calls = %+v, want %+v",
			eng.registerGroupCalls, wantRegister)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok || account.GroupCode != "old-a" {
		t.Fatalf("GetAccount acc-1 after rollback: account=%+v ok=%v err=%v",
			account, ok, err)
	}
}

// TestApplyBusinessCSVImport_ReconcilesEngineOnStoreFailure proves the atomicity
// seam: when the engine adjustments succeeded but the final transactional store
// write fails, the node rebuilds the engine from the persisted (rolled-back)
// store state so the engine and store do not diverge.
func TestApplyBusinessCSVImport_ReconcilesEngineOnStoreFailure(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "10"}
	errStoreWrite := errors.New("business csv store write failed")

	real := newMemoryStore("csv-reconcile.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failBusinessCSVImportRealm{RealmStore: r, err: errStoreWrite}
	})

	var buildCount int
	build := func(snap engine.Snapshot) (engine.Engine, error) {
		buildCount++
		eng.knownAccounts = map[domain.AccountID]struct{}{}
		for _, account := range snap.Accounts {
			eng.knownAccounts[account.Code] = struct{}{}
		}
		eng.knownGroups = map[string]struct{}{}
		for _, group := range snap.Groups {
			eng.knownGroups[group.Code] = struct{}{}
		}
		return eng, nil
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.realm.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil &&
		!errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateAsset: %v", err)
	}
	buildsBefore := buildCount

	// The balance for acc-1 drives a successful engine adjustment on the lane; the
	// decorated realm then fails the final transactional store write, after the
	// engine already applied the adjustment, so the node must reconcile.
	err = n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{
			{Exists: true, Account: domain.Account{Code: "acc-1"}},
		},
		Balances: []domain.Balance{{Account: "acc-1", Asset: "USD", Available: "10"}},
	}, testCaller)
	if !errors.Is(err, errStoreWrite) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want store write failure", err)
	}
	if buildCount <= buildsBefore {
		t.Fatalf("engine was not rebuilt to reconcile after store failure: builds %d -> %d",
			buildsBefore, buildCount)
	}
}

func TestApplyBusinessCSVImport_BatchRejectIsRecoverable(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "bounds",
		Reason: "outside bounds",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{{
			Account: "acc-1", Asset: "USD", Available: "10",
		}},
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want invalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, "acc-1"); err != nil || ok {
		t.Fatalf("GetAccount after reject: ok=%v err=%v, want absent", ok, err)
	}
}

func TestApplyBusinessCSVImport_DuplicatePositionSnapshotIsInvalid(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{
			{Account: "acc-1", Asset: "USD", Available: "10"},
			{Account: "acc-1", Asset: "USD", Available: "20"},
		},
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want invalid", err)
	}
	if len(eng.adjustmentBatchCalls) != 0 {
		t.Fatalf("adjustment batch calls = %+v, want none", eng.adjustmentBatchCalls)
	}
	if _, ok, err := st.GetAccount(ctx, "acc-1"); err != nil || ok {
		t.Fatalf("GetAccount after duplicate snapshot: ok=%v err=%v, want absent",
			ok, err)
	}
}

func TestLocalNode_ReconcileOrphansPreservesOrders(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.reconcileCount = 1
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	seedTestAccount(t, st, "acc-1")
	order, err := st.CreateOrder(ctx, domain.Order{
		Account:     "acc-1",
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "100",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := st.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "approval-1",
		Order:      order.ExternalID,
		Account:    order.Account,
		ParamsJSON: `{"id":1}`,
		IssuedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(2 * time.Minute),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	count, err := n.ReconcileOrphans(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if count != 1 {
		t.Fatalf("held intents = %d, want 1", count)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusAccepted {
		t.Fatalf("order status = %q, want accepted", detail.Order.Status)
	}
	events, err := st.ListOrderEvents(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("ListOrderEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want no rollback events", events)
	}
}

func TestLocalNode_SubmitHoldPersistsHeldBalances(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = []byte("lock")
	eng.holdOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "8000",
			HeldResult:    "2000",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10000",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	order, result, err := n.SubmitHold(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitHold: %v", err)
	}
	if !result.Accepted || order.Status != domain.OrderStatusAccepted {
		t.Fatalf("hold not accepted: order=%+v result=%+v", order, result)
	}
	balance, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if balance.Available != "8000" || balance.Held != "2000" {
		t.Fatalf("balance = %+v, want available=8000 held=2000", balance)
	}
}

// TestLocalNode_SubmitOrderPersistsReservationBalances verifies the direct
// submit path mirrors the reservation's balance effects into the snapshot, so
// held funds and incoming quantity show up before any fill settles.
func TestLocalNode_SubmitOrderPersistsReservationBalances(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = []byte("lock")
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "8000",
				HeldResult:    "2000",
			},
		},
		{
			Asset: "AAPL",
			Outcome: domain.AdjustmentOutcomeAccepted{
				IncomingResult: "20",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10000",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}

	quote, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance USD: %v ok=%v", err, ok)
	}
	if quote.Held != "2000" {
		t.Fatalf("USD held = %q, want 2000", quote.Held)
	}
	base, ok, err := st.GetBalance(ctx, "acc-1", "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance AAPL: %v ok=%v", err, ok)
	}
	if base.Incoming != "20" {
		t.Fatalf("AAPL incoming = %q, want 20", base.Incoming)
	}
}

func TestLocalNode_SubmitOrderPostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record order submission failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failOrderSubmissionAfterApplyRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("SubmitOrder error = %v, want store failure", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine order persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record order submission"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record order submission failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestLocalNode_SubmitImmediatePostEngineStoreFailureFatals mirrors the
// SubmitOrder fatal test for the immediate path: the engine applies the order
// inside RecordOrderSubmission's apply callback, then the store write fails, so
// the settlement can never be persisted and the node must fail-stop with the
// "record immediate submission" operation label.
func TestLocalNode_SubmitImmediatePostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record immediate submission failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failOrderSubmissionAfterApplyRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("SubmitImmediate error = %v, want store failure", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine immediate persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record immediate submission"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record immediate submission failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

func TestLocalNode_SubmitOrderAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "GOLD",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Asset != "GOLD" ||
		!strings.Contains(rows[0].Detail, "auto-created asset GOLD by submit order") {
		t.Fatalf("create-asset audit rows = %+v, want submit-order auto-create", rows)
	}
}

// TestLocalNode_SubmitOrderAutoCreatesUnknownAccount checks that an order
// submitted for an account Officer does not know yet auto-creates it (like a
// fresh adjustment target), so the engine resolves the account and the order
// is processed rather than rejected as invalid, and the creation is audited.
func TestLocalNode_SubmitOrderAutoCreatesUnknownAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	// Enforce the resolver so a submit against an unknown account would error
	// unless the auto-create runs first and rebuilds the engine.
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order, err := n.SubmitOrder(ctx, testKey("fresh"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}

	if _, ok, err := st.GetAccount(ctx, "fresh"); err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAccount},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create account): %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "auto-created account fresh by submit order") {
		t.Fatalf("create-account audit rows = %+v, want submit-order auto-create", rows)
	}
}

// TestLocalNode_SubmitOrderHonorsSuppliedExternalID covers the create-once
// id-honoring path end-to-end through the real store: a caller-supplied order
// external id is used verbatim and the returned order carries it, and exactly
// one order row is created under it.
func TestLocalNode_SubmitOrderHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "supplied-order-id")
	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		ExternalID:  supplied,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.ExternalID != supplied {
		t.Fatalf("returned order id = %q, want supplied %q", order.ExternalID, supplied)
	}
	detail, err := st.GetOrder(ctx, supplied)
	if err != nil {
		t.Fatalf("GetOrder by supplied id: %v", err)
	}
	if detail.Order.ExternalID != supplied {
		t.Fatalf("stored order id = %q, want supplied %q", detail.Order.ExternalID, supplied)
	}
}

// TestLocalNode_SubmitOrderGeneratesExternalIDWhenAbsent covers the absent-id
// path: a zero id leaves the store to mint a canonical one.
func TestLocalNode_SubmitOrderGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.ExternalID.IsZero() {
		t.Fatalf("order id is zero, want a generated id")
	}
	if err := domain.ValidateExternalID(order.ExternalID.String()); err != nil {
		t.Fatalf("generated order id not canonical: %v", err)
	}
}

// TestLocalNode_SubmitOrderDuplicateSuppliedIDConflicts covers the conflict
// path: a second submit reusing a supplied id surfaces domain.ErrAlreadyExists
// from the store, propagated unchanged.
func TestLocalNode_SubmitOrderDuplicateSuppliedIDConflicts(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "dup-order-id")
	mk := func() domain.Order {
		return domain.Order{
			ExternalID:  supplied,
			BaseAsset:   "AAPL",
			QuoteAsset:  "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "20",
			Price:       "100",
		}
	}
	if _, err := n.SubmitOrder(ctx, testKey("acc-1"), mk(), testCaller); err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	_, err := n.SubmitOrder(ctx, testKey("acc-1"), mk(), testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

func TestLocalNode_SubmitOrderDifferentAccountsProceedConcurrently(t *testing.T) {
	t.Parallel()
	entered := make(chan domain.AccountID, 2)
	release := make(chan struct{})
	eng := newFakeEngine()
	eng.submitEntered = entered
	eng.submitRelease = release
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")
	seedTestAccount(t, st, "acc-2")

	submit := func(account domain.AccountID, errs chan<- error) {
		_, err := n.SubmitOrder(ctx, testKey(account), domain.Order{
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "10",
		}, testCaller)
		errs <- err
	}
	errs := make(chan error, 2)
	go submit("acc-1", errs)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("first entered account = %s, want acc-1", got)
	}
	go submit("acc-2", errs)
	select {
	case got := <-entered:
		if got != "acc-2" {
			t.Fatalf("second entered account = %s, want acc-2", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second account did not enter SubmitOrder while first account was in-flight")
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("SubmitOrder #%d: %v", i, err)
		}
	}
}

func TestLocalNode_SubmitOrderSameAccountSerializes(t *testing.T) {
	t.Parallel()
	entered := make(chan domain.AccountID, 2)
	release := make(chan struct{})
	eng := newFakeEngine()
	eng.submitEntered = entered
	eng.submitRelease = release
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	submit := func(errs chan<- error) {
		_, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "10",
		}, testCaller)
		errs <- err
	}
	errs := make(chan error, 2)
	go submit(errs)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("first entered account = %s, want acc-1", got)
	}
	go submit(errs)
	select {
	case got := <-entered:
		t.Fatalf("second same-account SubmitOrder entered early as %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("second entered account = %s, want acc-1", got)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("SubmitOrder #%d: %v", i, err)
		}
	}
}

// TestLocalNode_SubmitHoldHonorsSuppliedExternalID covers the hold submit path:
// the supplied id is recorded once and confirm/cancel can resolve the same order
// by it.
func TestLocalNode_SubmitHoldHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "supplied-hold-id")
	order, _, err := n.SubmitHold(ctx, testKey("acc-1"), domain.Order{
		ExternalID:  supplied,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitHold: %v", err)
	}
	if order.ExternalID != supplied {
		t.Fatalf("returned order id = %q, want supplied %q", order.ExternalID, supplied)
	}
	if _, err := st.GetOrder(ctx, supplied); err != nil {
		t.Fatalf("GetOrder by supplied id: %v", err)
	}
}

// TestLocalNode_ApplyAdjustmentHonorsSuppliedExternalID covers the user-create
// adjustment path end-to-end: a supplied id is carried onto the record and
// persisted verbatim; a duplicate surfaces domain.ErrAlreadyExists.
func TestLocalNode_ApplyAdjustmentHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "100",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "supplied-adj-id")
	req := domain.AdjustmentRequest{
		Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "100",
		},
	}
	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), supplied, req, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.ExternalID != supplied {
		t.Fatalf("record id = %q, want supplied %q", rec.ExternalID, supplied)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].ExternalID != supplied {
		t.Fatalf("stored adjustments = %+v, want one under supplied id", records)
	}

	// A second adjustment reusing the supplied id conflicts.
	_, err = n.ApplyAdjustment(ctx, testKey("acc-1"), supplied, req, testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

func TestLocalNode_ApplyAdjustmentNoChangeDoesNotPersist(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentNoop = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "0"},
		}, testCaller)
	if !errors.Is(err, domain.ErrNoChange) {
		t.Fatalf("ApplyAdjustment error = %v, want ErrNoChange", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want one", eng.adjustmentCalls)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("stored adjustments = %+v, want none", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("audit rows = %+v, want none", rows)
	}
}

func TestLocalNode_ApplyAdjustmentStoreFailureFatalsWithoutCompensation(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record adjustment failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failAccountAdjustmentRecordRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceDelta:  "5",
		BalanceResult: "15",
	}
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	seedTestAccount(t, n.realm, "acc-1")
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10", AverageEntryPrice: "42",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:             "USD",
			AverageEntryPrice: "99",
			Balance:           &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "5"},
		}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("ApplyAdjustment error = %v, want store failure", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want only the requested apply", eng.adjustmentCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine adjustment persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record account adjustment"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record adjustment failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestLocalNode_ApplyAdjustmentGeneratesExternalIDWhenAbsent covers the
// absent-id path: a zero id lets the store mint one.
func TestLocalNode_ApplyAdjustmentGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""), domain.AdjustmentRequest{
		Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "100",
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.ExternalID.IsZero() {
		t.Fatalf("record id is zero, want a generated id")
	}
}

// TestLocalNode_ApplyAdjustmentRejectedIsRecorded checks that a policy reject
// (e.g. a P&L kill-switch) on an existing account persists the rejected attempt
// to the adjustment history and the audit log, leaving balances untouched.
func TestLocalNode_ApplyAdjustmentRejectedIsRecorded(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "pnl_bounds_kill_switch",
		Policy: "pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}

	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance after reject = ok %v err %v, want no balance row", ok, err)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAccount checks that an
// adjustment to a non-existent account auto-creates it in the default group (no
// group assigned), so the engine resolves it and applies the adjustment, and
// the creation is audited.
func TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	// Enforce the resolver so an adjustment to an unknown account would error
	// unless the auto-create runs first and rebuilds the engine.
	eng.enforceResolver = true
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	rec, err := n.ApplyAdjustment(ctx, testKey("fresh"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}

	account, ok, err := st.GetAccount(ctx, "fresh")
	if err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	if account.GroupCode != "" {
		t.Fatalf("auto-created account group = %q, want default (empty)", account.GroupCode)
	}

	records, err := st.ListAdjustments(ctx, "fresh", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Accepted == nil {
		t.Fatalf("stored adjustments = %+v, want one accepted record", records)
	}

	createRows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAccount},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create): %v", err)
	}
	if len(createRows) != 1 {
		t.Fatalf("create-account audit rows = %+v, want one", createRows)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreateThenReject checks that a rejected
// adjustment to a non-existent account still auto-creates the account and
// records the rejected attempt in both history and audit.
func TestLocalNode_ApplyAdjustmentAutoCreateThenReject(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "pnl_bounds_kill_switch",
		Policy: "pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	rec, err := n.ApplyAdjustment(ctx, testKey("fresh"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	if _, ok, err := st.GetAccount(ctx, "fresh"); err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	records, err := st.ListAdjustments(ctx, "fresh", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}
}

// TestLocalNode_ApplyAdjustmentRejectsMalformedAccountID checks that a malformed
// account id is rejected with domain.ErrInvalid and no account is auto-created.
func TestLocalNode_ApplyAdjustmentRejectsMalformedAccountID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	bad := domain.AccountID("bad-id ")
	_, err := n.ApplyAdjustment(ctx, testKey(bad), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(malformed) = %v, want ErrInvalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, bad); err != nil || ok {
		t.Fatalf("GetAccount(malformed) = ok %v err %v, want absent", ok, err)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAsset checks that an adjustment
// referencing a non-existent asset auto-creates it in the auto-created class, so
// the engine resolves it and applies the adjustment, and the creation is
// audited with the originating operation.
func TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "GOLD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.Title != "" || asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset = %+v, want empty title and %q class", asset,
			autoCreatedAssetClassCode)
	}

	createRows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(createRows) != 1 ||
		createRows[0].Asset != "GOLD" ||
		!strings.Contains(createRows[0].Detail, "auto-created asset GOLD by adjustment") {
		t.Fatalf("create-asset audit rows = %+v, want one for GOLD", createRows)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreateAssetThenReject checks that a rejected
// adjustment to a non-existent asset still auto-creates the asset and records
// the rejected attempt in both history and audit.
func TestLocalNode_ApplyAdjustmentAutoCreateAssetThenReject(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "pnl_bounds_kill_switch",
		Policy: "pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "GOLD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}
}

// TestLocalNode_ApplyAdjustmentRejectsMalformedAsset checks that a malformed
// asset id is rejected with domain.ErrInvalid and no asset is auto-created.
func TestLocalNode_ApplyAdjustmentRejectsMalformedAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	bad := "US D"
	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   bad,
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(malformed asset) = %v, want ErrInvalid", err)
	}
	if _, ok, err := st.GetAsset(ctx, bad); err != nil || ok {
		t.Fatalf("GetAsset(malformed) = ok %v err %v, want absent", ok, err)
	}
}

func TestLocalNode_CancelHeldFallbackReleasesPersistedHold(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.rollbackErr = fmt.Errorf("engine: reservation %q: %w", "approval-1", domain.ErrNotFound)
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "10000",
		HeldResult:    "0",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")
	order, err := st.CreateOrder(ctx, domain.Order{
		Account:     "acc-1",
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "8000", Held: "2000",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	outcomes := []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceDelta: "-2000",
			HeldDelta:    "2000",
		},
	}}
	payload, err := json.Marshal(reservationIntentPayload{
		Order:    order,
		Outcomes: outcomes,
	})
	if err != nil {
		t.Fatalf("Marshal payload: %v", err)
	}
	if err := st.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "approval-1",
		Order:      order.ExternalID,
		Account:    order.Account,
		ParamsJSON: string(payload),
		IssuedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(2 * time.Minute),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	cancelled, _, err := n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if err != nil {
		t.Fatalf("CancelHeld: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("order status = %q, want cancelled", cancelled.Status)
	}
	balance, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if balance.Available != "10000" || balance.Held != "0" {
		t.Fatalf("balance = %+v, want available=10000 held=0", balance)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %d, want 1", len(eng.adjustmentCalls))
	}
	req := eng.adjustmentCalls[0].req
	if req.Balance == nil || req.Balance.Value != "2000" ||
		req.Held == nil || req.Held.Value != "-2000" {
		t.Fatalf("release request = %+v, want balance +2000 held -2000", req)
	}
	open, err := st.ListOpenReservationIntents(ctx)
	if err != nil {
		t.Fatalf("ListOpenReservationIntents: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open intents = %+v, want none", open)
	}
}

func TestDecodeReservationIntentPayloadWrapperWorks(t *testing.T) {
	t.Parallel()
	order := domain.Order{
		ExternalID: externalID(t, "held-order-id"),
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
	}
	outcomes := []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceDelta: "-100",
			HeldDelta:    "100",
		},
	}}
	raw, err := json.Marshal(reservationIntentPayload{Order: order, Outcomes: outcomes})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	gotOrder, gotOutcomes, err := decodeReservationIntentPayload(string(raw))
	if err != nil {
		t.Fatalf("decodeReservationIntentPayload: %v", err)
	}
	if gotOrder.ExternalID != order.ExternalID || gotOrder.Account != order.Account {
		t.Fatalf("order = %+v, want %+v", gotOrder, order)
	}
	if len(gotOutcomes) != 1 || gotOutcomes[0].Asset != "USD" {
		t.Fatalf("outcomes = %+v, want one USD outcome", gotOutcomes)
	}
}

func TestDecodeReservationIntentPayloadRejectsBareOrder(t *testing.T) {
	t.Parallel()
	order := domain.Order{
		ExternalID: externalID(t, "bare-order-id"),
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
	}
	raw, err := json.Marshal(order)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	_, _, err = decodeReservationIntentPayload(string(raw))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("decode bare order error = %v, want ErrInvalid", err)
	}
	if err == nil || !strings.Contains(err.Error(), "malformed reservation intent payload") {
		t.Fatalf("decode bare order error = %v, want malformed payload message", err)
	}
}

func TestLocalNode_ImportPositionSnapshotPersistsRealizedPnlAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	snapshot := domain.Balance{
		Account:           "acc-1",
		Asset:             "USD",
		Available:         "100.25",
		Held:              "10.5",
		Incoming:          "2.75",
		RealizedPnl:       "7.125",
		AverageEntryPrice: "99.5",
	}
	rec, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), domain.ExternalID(""), snapshot, testCaller)
	if err != nil {
		t.Fatalf("ImportPositionSnapshot: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %d, want 1", len(eng.adjustmentCalls))
	}
	req := eng.adjustmentCalls[0].req
	if req.Asset != "USD" ||
		req.Balance == nil || req.Balance.Mode != domain.AdjustmentModeAbsolute ||
		req.Balance.Value != "100.25" ||
		req.Held == nil || req.Held.Mode != domain.AdjustmentModeAbsolute ||
		req.Held.Value != "10.5" ||
		req.Incoming == nil || req.Incoming.Mode != domain.AdjustmentModeAbsolute ||
		req.Incoming.Value != "2.75" ||
		req.AverageEntryPrice != "99.5" {
		t.Fatalf("adjustment request = %+v", req)
	}
	got, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if got.Available != "100.25" || got.Held != "10.5" ||
		got.Incoming != "2.75" || got.RealizedPnl != "7.125" ||
		got.AverageEntryPrice != "99.5" {
		t.Fatalf("balance = %+v", got)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 ||
		!strings.Contains(rows[0].Detail, "import position snapshot account acc-1 asset=USD") ||
		!strings.Contains(rows[0].Detail, "realized_pnl=7.125") {
		t.Fatalf("audit rows = %+v", rows)
	}
}

func TestLocalNode_ImportPositionSnapshotDuplicateExternalIDDoesNotApplyEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, n.realm, "acc-1")

	supplied := externalID(t, "snapshot-adj-id")
	snapshot := domain.Balance{Account: "acc-1", Asset: "USD", Available: "100"}
	if _, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), supplied, snapshot, testCaller); err != nil {
		t.Fatalf("first ImportPositionSnapshot: %v", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine calls after first import = %d, want 1", len(eng.adjustmentCalls))
	}
	_, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), supplied, snapshot, testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate snapshot id error = %v, want ErrAlreadyExists", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine calls after duplicate = %d, want no second apply", len(eng.adjustmentCalls))
	}
}

func TestLocalNode_ImportPositionSnapshotStoreFailureFatalsWithoutCompensation(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record snapshot adjustment failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failAccountAdjustmentRecordRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceDelta:  "5",
		BalanceResult: "15",
	}
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	seedTestAccount(t, n.realm, "acc-1")
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10", AverageEntryPrice: "42",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	_, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.Balance{
			Account: "acc-1", Asset: "USD", Available: "15", AverageEntryPrice: "99",
		}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("ImportPositionSnapshot error = %v, want store failure", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want only the requested apply", eng.adjustmentCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine snapshot persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record position snapshot adjustment"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record snapshot adjustment failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

func TestLocalNode_ImportPositionSnapshotDeletesEmptyPosition(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD",
		Available: "100", AverageEntryPrice: "50",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	snapshot := domain.Balance{
		Account: "acc-1", Asset: "USD",
		Available: "0.00", Held: "0", Incoming: "0",
		RealizedPnl: "0", AverageEntryPrice: "0.000",
	}
	if _, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), domain.ExternalID(""), snapshot, testCaller); err != nil {
		t.Fatalf("ImportPositionSnapshot: %v", err)
	}
	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance after empty snapshot: ok=%v err=%v, want missing", ok, err)
	}
}

func TestLocalNode_ImportPositionSnapshotAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	snapshot := domain.Balance{
		Account:   "acc-1",
		Asset:     "GOLD",
		Available: "100.25",
	}
	if _, err := n.ImportPositionSnapshot(ctx, testKey("acc-1"), domain.ExternalID(""), snapshot, testCaller); err != nil {
		t.Fatalf("ImportPositionSnapshot: %v", err)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Asset != "GOLD" ||
		!strings.Contains(rows[0].Detail, "auto-created asset GOLD by position snapshot import") {
		t.Fatalf("create-asset audit rows = %+v, want snapshot auto-create", rows)
	}
}

// TestLocalNode_ConcurrentResolveNoDeadlockSingleResolution drives ConfirmHeld
// and a sweeper-style CancelHeld concurrently on sibling orders. Both paths take
// n.beginMutation, so they serialize on the node's mutate lock rather than
// deadlock: if a rollback path ever re-entered that lock (e.g. the engine
// regaining a node reference and calling back through CancelHeld), the two
// goroutines would hang. The test bounds the fan-out with a timeout and asserts
// each order's reservation resolves exactly once through the engine.
func TestLocalNode_ConcurrentResolveNoDeadlockSingleResolution(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// Two committed-buy orders, each with its own held reservation intent. One is
	// confirmed, the sibling is cancelled, concurrently.
	type held struct {
		order      domain.Order
		approvalID string
	}
	mk := func(i int) held {
		account := domain.AccountID(fmt.Sprintf("acc-%d", i))
		seedTestAccount(t, st, account)
		order, err := st.CreateOrder(ctx, domain.Order{
			Account:     account,
			Source:      domain.SourceAPI,
			Principal:   "operator",
			BaseAsset:   "AAPL",
			QuoteAsset:  "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "1",
			Price:       "100",
			Status:      domain.OrderStatusAccepted,
		})
		if err != nil {
			t.Fatalf("CreateOrder %d: %v", i, err)
		}
		approval := fmt.Sprintf("approval-%d", i)
		if err := st.UpsertReservationIntent(ctx, domain.ReservationIntent{
			ApprovalID: approval,
			Order:      order.ExternalID,
			Account:    order.Account,
			ParamsJSON: `{"id":1}`,
			IssuedAt:   time.Now().UTC(),
			ExpiresAt:  time.Now().UTC().Add(2 * time.Minute),
			State:      domain.ReservationIntentStateHeld,
		}); err != nil {
			t.Fatalf("UpsertReservationIntent %d: %v", i, err)
		}
		return held{order: order, approvalID: approval}
	}
	confirmTarget := mk(1)
	cancelTarget := mk(2)

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)
	go func() {
		defer wg.Done()
		_, _, errs[0] = n.ConfirmHeld(
			ctx, confirmTarget.order.ExternalID, confirmTarget.approvalID, testCaller, false)
	}()
	go func() {
		defer wg.Done()
		_, _, errs[1] = n.CancelHeld(
			ctx, cancelTarget.order.ExternalID, cancelTarget.approvalID, testCaller, false)
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent ConfirmHeld/CancelHeld deadlocked")
	}

	if errs[0] != nil {
		t.Fatalf("ConfirmHeld: %v", errs[0])
	}
	if errs[1] != nil {
		t.Fatalf("CancelHeld: %v", errs[1])
	}

	// Each reservation resolved exactly once through the engine, on the right path.
	eng.resolveMu.Lock()
	commits := append([]string(nil), eng.commitHeldCalls...)
	rollbacks := append([]string(nil), eng.rollbackHeldCalls...)
	eng.resolveMu.Unlock()
	if len(commits) != 1 || commits[0] != confirmTarget.approvalID {
		t.Fatalf("commit calls = %+v, want exactly [%s]", commits, confirmTarget.approvalID)
	}
	if len(rollbacks) != 1 || rollbacks[0] != cancelTarget.approvalID {
		t.Fatalf("rollback calls = %+v, want exactly [%s]", rollbacks, cancelTarget.approvalID)
	}

	// Durable resolution: committed and cancelled statuses landed in the store.
	confirmed, err := st.GetOrder(ctx, confirmTarget.order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder confirmed: %v", err)
	}
	if confirmed.Order.Status != domain.OrderStatusCommitted {
		t.Fatalf("confirmed status = %q, want committed", confirmed.Order.Status)
	}
	cancelled, err := st.GetOrder(ctx, cancelTarget.order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder cancelled: %v", err)
	}
	if cancelled.Order.Status != domain.OrderStatusCancelled {
		t.Fatalf("cancelled status = %q, want cancelled", cancelled.Order.Status)
	}
}

func TestLocalNode_PutRateLimitAppliesAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("want 1 stored barrier, got %d", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Startup hydrate row plus the set_limit row, newest first.
	if len(rows) != 2 || rows[0].Action != domain.AuditActionSetLimit {
		t.Fatalf("want newest set_limit over startup hydrate, got %+v", rows)
	}
}

func TestLocalNode_PutAssetRateLimitAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	limit := rateLimit(domain.ScopeAsset, "", "GOLD", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Asset != "GOLD" ||
		!strings.Contains(rows[0].Detail, "auto-created asset GOLD by rate limit") {
		t.Fatalf("create-asset audit rows = %+v, want limit auto-create", rows)
	}
}

func TestLocalNode_PutRateLimitEngineFailureRevertsStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.failConfigure = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller); err == nil {
		t.Fatalf("PutRateLimit: want error on engine failure")
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("want store reverted to empty, got %d barriers", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Only the startup hydrate row: the failed mutation audits nothing.
	if len(rows) != 1 || rows[0].Action != domain.AuditActionHydrate {
		t.Fatalf("want only the startup hydrate row, got %+v", rows)
	}
}

func TestLocalNode_PutRateLimitConfiguresPolicyFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	limit := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	if len(eng.configureCalls) != 1 {
		t.Fatalf("configure calls = %d, want 1", len(eng.configureCalls))
	}
	if eng.configureCalls[0].policy != domain.PolicyRateLimit {
		t.Fatalf("configured policy = %q, want rate_limit", eng.configureCalls[0].policy)
	}
	if len(eng.configureCalls[0].limits.RateLimits) != 1 {
		t.Fatalf("configured limits = %d, want 1", len(eng.configureCalls[0].limits.RateLimits))
	}
	if eng.configureCalls[0].limits.RateLimits[0] != limit {
		t.Fatalf("configured wrong barrier: %+v", eng.configureCalls[0].limits.RateLimits[0])
	}
}

func TestLocalNode_PutRateLimitNotImplementedRebuildsFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.configureErr = fmt.Errorf("engine: unregistered policy: %w",
		domain.ErrNotImplemented)
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

	limit := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	sink, err := n.PutRateLimit(ctx, limit, testCaller)
	if err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if sink == nil {
		t.Fatal("PutRateLimit returned nil sink after rebuild")
	}
	if eng.running {
		t.Fatal("old engine still running after rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after rebuild")
	}
	if len(rebuilt.RateLimits) != 1 || rebuilt.RateLimits[0] != limit {
		t.Fatalf("rebuilt limits = %+v, want the new barrier", rebuilt.RateLimits)
	}
}

// TestLocalNode_CreateAccountRebuildsEngineWithNewAccount verifies a freshly
// created account is wired into the live engine. The resolver has no incremental
// account registration, so the node rebuilds from the store on create; without
// the rebuild the account would persist but stay unknown to the engine
// (adjustments, group moves, and orders would reject as "unknown account") until
// a restart.
func TestLocalNode_CreateAccountRebuildsEngineWithNewAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

	if _, err := n.CreateAccount(ctx, testAccount("fresh"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if eng.running {
		t.Fatal("old engine still running after create rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after create rebuild")
	}
	found := false
	for _, a := range rebuilt.Accounts {
		if a.Code == "fresh" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rebuilt accounts = %+v, want the new account", rebuilt.Accounts)
	}
}

// TestLocalNode_CreateGroupRebuildsEngineWithNewGroup verifies a freshly created
// group is wired into the live engine for the same reason: a runtime-created
// group is unknown to the resolver until the engine is rebuilt from the store.
func TestLocalNode_CreateGroupRebuildsEngineWithNewGroup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "vips"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if eng.running {
		t.Fatal("old engine still running after create rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after create rebuild")
	}
	found := false
	for _, g := range rebuilt.Groups {
		if g.Code == "vips" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rebuilt groups = %+v, want the new group", rebuilt.Groups)
	}
}

func TestLocalNode_PutRateLimitSamePolicyDifferentAccountsRetunesOnePolicy(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	buildCalls := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		buildCalls++
		return newFakeEngine(), nil
	}

	seedTestAccount(t, n.realm, "acc-1")
	seedTestAccount(t, n.realm, "acc-2")
	first := rateLimit(domain.ScopeAccount, "acc-1", "", 100, time.Second)
	second := rateLimit(domain.ScopeAccount, "acc-2", "", 10, time.Second)
	if _, err := n.PutRateLimit(ctx, first, testCaller); err != nil {
		t.Fatalf("PutRateLimit first: %v", err)
	}
	if _, err := n.PutRateLimit(ctx, second, testCaller); err != nil {
		t.Fatalf("PutRateLimit second: %v", err)
	}

	if buildCalls != 0 {
		t.Fatalf("build calls = %d, want 0", buildCalls)
	}
	if len(eng.configureCalls) != 2 {
		t.Fatalf("configure calls = %d, want 2", len(eng.configureCalls))
	}
	last := eng.configureCalls[1]
	if last.policy != domain.PolicyRateLimit {
		t.Fatalf("configured policy = %q, want rate_limit", last.policy)
	}
	if len(last.limits.RateLimits) != 2 {
		t.Fatalf("configured barriers = %d, want 2", len(last.limits.RateLimits))
	}
	got := map[domain.AccountID]bool{}
	for _, limit := range last.limits.RateLimits {
		got[limit.Account] = true
	}
	if !got["acc-1"] || !got["acc-2"] {
		t.Fatalf("configured accounts = %+v, want acc-1 and acc-2", got)
	}
}

func TestLocalNode_PutRateLimitEngineFailureRestoresPrevious(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	first := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, first, testCaller); err != nil {
		t.Fatalf("PutRateLimit first: %v", err)
	}

	eng.failConfigure = true
	second := rateLimit(domain.ScopeBroker, "", "", 5, 2*time.Second)
	if _, err := n.PutRateLimit(ctx, second, testCaller); err == nil {
		t.Fatalf("PutRateLimit second: want error")
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("want 1 barrier after revert, got %d", len(stored))
	}
	if stored[0].MaxOrders != 100 || stored[0].Window != time.Second {
		t.Fatalf("barrier not restored to previous: %+v", stored[0])
	}
}

func TestLocalNode_DeleteLimitAppliesAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	target := LimitTarget{Policy: domain.PolicyRateLimit, Scope: domain.ScopeBroker}
	if _, err := n.DeleteLimit(ctx, target, testCaller); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("want 0 barriers after delete, got %d", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Startup hydrate, set_limit, delete_limit = 3 rows, newest first.
	if len(rows) != 3 || rows[0].Action != domain.AuditActionDeleteLimit {
		t.Fatalf("want newest row delete_limit, got %+v", rows)
	}
}

func TestLocalNode_DeleteLastLimitRebuildsFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	eng.configureErr = fmt.Errorf("engine: empty policy settings: %w",
		domain.ErrNotImplemented)
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)
	target := LimitTarget{Policy: domain.PolicyRateLimit, Scope: domain.ScopeBroker}
	sink, err := n.DeleteLimit(ctx, target, testCaller)
	if err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if sink == nil {
		t.Fatal("DeleteLimit returned nil sink after rebuild")
	}
	if eng.running {
		t.Fatal("old engine still running after rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after rebuild")
	}
	if len(rebuilt.RateLimits) != 0 {
		t.Fatalf("rebuilt limits = %+v, want no barriers", rebuilt.RateLimits)
	}
	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored limits = %+v, want none", stored)
	}
}

func TestLocalNode_BlockUnblockAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", testCaller); err != nil {
		t.Fatalf("block: %v", err)
	}
	if len(eng.blockCalls) != 1 || eng.blockCalls[0].reason != "risk" {
		t.Fatalf("engine block not applied: %+v", eng.blockCalls)
	}

	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if !account.Blocked || account.BlockReason != "risk" {
		t.Fatalf("account not blocked in store: %+v", account)
	}

	if err := n.SetAccountBlocked(ctx, testKey(id), false, "", testCaller); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if len(eng.unblockCalls) != 1 {
		t.Fatalf("engine unblock not applied: %+v", eng.unblockCalls)
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// startup hydrate, create_account, block, unblock = 4 rows, newest first.
	if len(rows) != 4 || rows[0].Action != domain.AuditActionUnblock {
		t.Fatalf("unexpected audit trail: %+v", rows)
	}
}

// TestLocalNode_SetAccountBlockedUsesAccountLane proves the engine block/unblock
// runs through the account lane (so it serializes against fills and the
// execution-report kill-switch on the same account), not just under the global
// mutation lock.
func TestLocalNode_SetAccountBlockedUsesAccountLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", testCaller); err != nil {
		t.Fatalf("block: %v", err)
	}
	if len(eng.blockCalls) != 1 {
		t.Fatalf("engine block not applied: %+v", eng.blockCalls)
	}
	if len(eng.accountSyncCalls) == 0 || eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != id {
		t.Fatalf("account sync calls = %+v, want last %s (block routed through lane)",
			eng.accountSyncCalls, id)
	}
}

// TestLocalNode_SetAccountGroupUsesGroupLane proves the engine group move runs
// on the target group's synchronized lane while the store write remains inside
// the account lane.
func TestLocalNode_SetAccountGroupUsesGroupLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := n.SetAccountGroup(ctx, testKey(id), "desk-a", testCaller); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}
	if len(eng.registerGroupCalls) != 1 {
		t.Fatalf("register group calls = %+v, want one", eng.registerGroupCalls)
	}
	if len(eng.accountSyncCalls) == 0 || eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != id {
		t.Fatalf("account sync calls = %+v, want last %s", eng.accountSyncCalls, id)
	}
	if len(eng.groupSyncCalls) == 0 || eng.groupSyncCalls[len(eng.groupSyncCalls)-1] != "desk-a" {
		t.Fatalf("group sync calls = %+v, want last desk-a", eng.groupSyncCalls)
	}
}

// TestLocalNode_SetAccountGroupAutoCreatesUnknownGroup proves the auto-create
// contract against a strict resolver: moving an account into a group with no
// account_groups record succeeds because the missing record is created and the
// engine rebuilt from the store before the lane, so the in-lane RegisterGroup
// resolves the now-known group. Against the old in-lane-create-no-rebuild
// implementation the account lane's RegisterGroup resolved the brand-new group
// before it was registered and rejected it with ErrInvalid, so the move failed.
func TestLocalNode_SetAccountGroupAutoCreatesUnknownGroup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// The strict resolver rejects any group it has not seen in a build snapshot.
	// "new-desk" has never been created, so it is absent from the resolver.
	eng.enforceResolver = true

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetAccountGroup(ctx, testKey(id), "new-desk", testCaller); err != nil {
		t.Fatalf("SetAccountGroup into unknown group: %v", err)
	}

	if len(eng.registerGroupCalls) != 1 ||
		eng.registerGroupCalls[0].groupID != "new-desk" ||
		!slices.Equal(eng.registerGroupCalls[0].accounts, []domain.AccountID{id}) {
		t.Fatalf("register group calls = %+v, want one for new-desk/%s",
			eng.registerGroupCalls, id)
	}

	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if account.GroupCode != "new-desk" {
		t.Fatalf("account group = %q, want new-desk", account.GroupCode)
	}
	group, ok, err := st.GetGroup(ctx, "new-desk")
	if err != nil || !ok {
		t.Fatalf("GetGroup: %v ok=%v", err, ok)
	}
	if group.Code != "new-desk" {
		t.Fatalf("group code = %q, want new-desk", group.Code)
	}
}

// TestLocalNode_SetGroupBlockedRunsUnderRestartGate proves the group block runs
// under the exclusive engine-restart gate rather than on the group lane: it
// applies the engine block directly (no RunGroupSynchronized call) and, while it
// holds the gate, a concurrent engine-restart request is rejected with
// ErrEngineRestarting.
func TestLocalNode_SetGroupBlockedRunsUnderRestartGate(t *testing.T) {
	t.Parallel()
	entered := make(chan string, 1)
	release := make(chan struct{})
	eng := newFakeEngine()
	eng.blockGroupEntered = entered
	eng.blockGroupRelease = release
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	errs := make(chan error, 1)
	go func() { errs <- n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller) }()

	select {
	case got := <-entered:
		if got != "desk-a" {
			t.Fatalf("entered group = %s, want desk-a", got)
		}
	case <-time.After(time.Second):
		t.Fatal("SetGroupBlocked did not reach the engine block")
	}

	// The exclusive gate is held: a concurrent engine-restart request is rejected
	// rather than interleaving with the in-flight group block.
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-b"}, testCaller); !errors.Is(err, domain.ErrEngineRestarting) {
		t.Fatalf("concurrent CreateGroup error = %v, want ErrEngineRestarting", err)
	}

	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	if len(eng.blockGroupCalls) != 1 {
		t.Fatalf("block group calls = %+v, want one", eng.blockGroupCalls)
	}
	if !slices.Equal(eng.groupSyncCalls, []string{"desk-a"}) {
		t.Fatalf("group sync calls = %+v, want desk-a", eng.groupSyncCalls)
	}
}

// TestLocalNode_SetGroupBlockedAutoCreatesUnknownGroup proves the auto-create
// contract against a strict resolver: a group with no account_groups record is
// unknown to the engine resolver, yet SetGroupBlocked succeeds because the
// missing record is created and the engine rebuilt from the store before the
// block runs. Against the old in-lane implementation the group-synchronized
// call resolved the group before the callback and rejected the unknown group
// with ErrInvalid, so the store record was never created and the block failed.
func TestLocalNode_SetGroupBlockedAutoCreatesUnknownGroup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// The strict resolver rejects any group it has not seen in a build snapshot.
	// "new-desk" has never been created, so it is absent from the resolver.
	eng.enforceResolver = true

	if err := n.SetGroupBlocked(ctx, "new-desk", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked on unknown group: %v", err)
	}

	group, ok, err := st.GetGroup(ctx, "new-desk")
	if err != nil || !ok {
		t.Fatalf("GetGroup: %v ok=%v", err, ok)
	}
	if !group.Blocked {
		t.Fatalf("store not blocked after auto-create block")
	}
	if len(eng.blockGroupCalls) != 1 || eng.blockGroupCalls[0].groupID != "new-desk" {
		t.Fatalf("block group calls = %+v, want one for new-desk", eng.blockGroupCalls)
	}
}

// TestLocalNode_SetAccountBlockedAuditFailureFatals proves the account-block
// path joins the post-engine fail-stop: once the engine block and the store
// blocked-state write committed inside the account lane, a failing audit write
// routes to the fatal hook. The diagnostic must name the account CODE, never the
// engine surrogate (account_id), so operators reading the fatal log see the same
// identifier the audit row stores.
func TestLocalNode_SetAccountBlockedAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("account block audit failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r, action: domain.AuditActionBlock, err: auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("SetAccountBlocked error = %v, want audit failure", err)
	}
	// Engine block applied before the audit write failed.
	if len(eng.blockCalls) != 1 {
		t.Fatalf("engine block calls = %+v, want one", eng.blockCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine account-block audit failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="audit account block"`) ||
		!strings.Contains(msg, "account=acc-1") ||
		!strings.Contains(msg, "account block audit failed") {
		t.Fatalf("fatal error = %q, want operation, account code, and cause", msg)
	}
	if strings.Contains(msg, "account_id=") {
		t.Fatalf("fatal error = %q, must not leak the engine surrogate", msg)
	}
}

// TestLocalNode_SetAccountGroupAuditFailureFatals proves the account set-group
// path joins the post-engine fail-stop with the same code-not-surrogate
// diagnostic: the engine group move and the store link write committed inside
// the lane, so a failing audit write routes to the fatal hook naming the account
// code.
func TestLocalNode_SetAccountGroupAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("set group audit failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r, action: domain.AuditActionSetGroup, err: auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	err := n.SetAccountGroup(ctx, testKey(id), "desk-a", testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("SetAccountGroup error = %v, want audit failure", err)
	}
	// Engine group move applied before the audit write failed.
	if len(eng.registerGroupCalls) != 1 {
		t.Fatalf("register group calls = %+v, want one", eng.registerGroupCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine set-group audit failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="audit set account group"`) ||
		!strings.Contains(msg, "account=acc-1") ||
		!strings.Contains(msg, "set group audit failed") {
		t.Fatalf("fatal error = %q, want operation, account code, and cause", msg)
	}
	if strings.Contains(msg, "account_id=") {
		t.Fatalf("fatal error = %q, must not leak the engine surrogate", msg)
	}
}

// TestLocalNode_SetGroupBlockedAuditFailureFatals proves the group-block path
// joins the post-engine fail-stop. A group has no account, so the diagnostic
// names the group CODE. The engine group block and the store blocked-state write
// committed under the restart gate, so a failing audit write routes to the fatal
// hook.
func TestLocalNode_SetGroupBlockedAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("group block audit failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r, action: domain.AuditActionBlockGroup, err: auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	err := n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("SetGroupBlocked error = %v, want audit failure", err)
	}
	// Engine group block applied before the audit write failed.
	if len(eng.blockGroupCalls) != 1 || eng.blockGroupCalls[0].groupID != "desk-a" {
		t.Fatalf("block group calls = %+v, want one for desk-a", eng.blockGroupCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine group-block audit failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="audit group block"`) ||
		!strings.Contains(msg, "group=desk-a") ||
		!strings.Contains(msg, "group block audit failed") {
		t.Fatalf("fatal error = %q, want operation, group code, and cause", msg)
	}
	if strings.Contains(msg, "account_id=") || strings.Contains(msg, "account=") {
		t.Fatalf("fatal error = %q, must not leak a surrogate or account id", msg)
	}
}

// TestLocalNode_SetGroupNotesAutoCreatesRegisteredGroup proves that setting
// notes on a brand-new group registers it in the engine, not just the store:
// after SetGroupNotes auto-creates "new-desk" a later SetAccountGroup moving an
// account into it succeeds because the strict resolver knows the group.
// SetGroupNotes rebuilt the engine from the store when it created the record.
// Against the old no-rebuild SetGroupNotes the store row existed but the live
// resolver never learned the group, so the in-lane RegisterGroup rejected it
// with ErrInvalid and the account move failed.
func TestLocalNode_SetGroupNotesAutoCreatesRegisteredGroup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// The strict resolver rejects any group it has not seen in a build snapshot.
	// "new-desk" has never been created, so it is absent from the resolver.
	eng.enforceResolver = true

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetGroupNotes(ctx, "new-desk", "vip desk", testCaller); err != nil {
		t.Fatalf("SetGroupNotes on unknown group: %v", err)
	}

	group, ok, err := st.GetGroup(ctx, "new-desk")
	if err != nil || !ok {
		t.Fatalf("GetGroup: %v ok=%v", err, ok)
	}
	if group.Notes != "vip desk" {
		t.Fatalf("group notes = %q, want vip desk", group.Notes)
	}

	// The group is now engine-registered, so moving an account into it succeeds
	// against the strict resolver.
	if err := n.SetAccountGroup(ctx, testKey(id), "new-desk", testCaller); err != nil {
		t.Fatalf("SetAccountGroup into notes-created group: %v", err)
	}
	if len(eng.registerGroupCalls) != 1 ||
		eng.registerGroupCalls[0].groupID != "new-desk" ||
		!slices.Equal(eng.registerGroupCalls[0].accounts, []domain.AccountID{id}) {
		t.Fatalf("register group calls = %+v, want one for new-desk/%s",
			eng.registerGroupCalls, id)
	}

	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if account.GroupCode != "new-desk" {
		t.Fatalf("account group = %q, want new-desk", account.GroupCode)
	}
}

func TestLocalNode_BlockEngineFailureRevertsStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	eng.failBlock = true
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", testCaller); err == nil {
		t.Fatalf("block: want error on engine failure")
	}

	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if account.Blocked {
		t.Fatalf("store not reverted: account still blocked")
	}
}

// newLaneProbeNode builds a node whose realm is a laneProbeRealm around a real
// temp SQLite store, so a test can assert block/group store writes execute
// inside the engine lane closure.
func newLaneProbeNode(
	t *testing.T, eng *fakeEngine,
) (*localNode, *laneProbeRealm) {
	t.Helper()
	probe := &laneProbeRealm{eng: eng}
	st := newRealmWrapStore(newMemoryStore("lane-probe.db"), func(inner store.RealmStore) store.RealmStore {
		probe.RealmStore = inner
		return probe
	})
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	n := newTestNodeWithStore(t, st, eng)
	return n, probe
}

// TestLocalNode_SetAccountBlockedStoreWriteInsideLane proves the store write is
// serialized inside the account lane closure, so a concurrent same-account admin
// op cannot interleave the store write with the engine block.
func TestLocalNode_SetAccountBlockedStoreWriteInsideLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", testCaller); err != nil {
		t.Fatalf("block: %v", err)
	}
	if len(probe.inLane) != 1 || !probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one true (write inside lane)", probe.inLane)
	}
}

// TestLocalNode_SetAccountBlockedStoreFailureInLaneLeavesEngineUntouched proves a
// store-write failure inside the lane returns before the engine block runs, so
// the engine is never mutated when the store write cannot commit.
func TestLocalNode_SetAccountBlockedStoreFailureInLaneLeavesEngineUntouched(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	probe.failErr = errors.New("store write failed")
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", testCaller); err == nil {
		t.Fatalf("block: want error on store write failure")
	}
	if len(probe.inLane) != 1 || !probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one true", probe.inLane)
	}
	if len(eng.blockCalls) != 0 {
		t.Fatalf("engine block calls = %+v, want none on store failure", eng.blockCalls)
	}
	if len(eng.accountSyncCalls) == 0 || eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != id {
		t.Fatalf("account sync calls = %+v, want last %s (write attempted inside lane)",
			eng.accountSyncCalls, id)
	}
}

// TestLocalNode_SetAccountGroupStoreWriteInsideLane proves the group-move store
// write is serialized inside the account lane closure.
func TestLocalNode_SetAccountGroupStoreWriteInsideLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountGroup(ctx, testKey(id), "desk-a", testCaller); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}
	if len(probe.inLane) != 1 || !probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one true (write inside lane)", probe.inLane)
	}
}

// TestLocalNode_SetAccountGroupStoreFailureInLaneLeavesEngineUntouched proves a
// store-write failure inside the account lane returns before the engine move
// runs.
func TestLocalNode_SetAccountGroupStoreFailureInLaneLeavesEngineUntouched(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	probe.failErr = errors.New("store write failed")
	if err := n.SetAccountGroup(ctx, testKey(id), "desk-a", testCaller); err == nil {
		t.Fatalf("SetAccountGroup: want error on store write failure")
	}
	if len(probe.inLane) != 1 || !probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one true", probe.inLane)
	}
	if len(eng.registerGroupCalls) != 0 || len(eng.unregisterGroupCalls) != 0 {
		t.Fatalf("engine group moves = register %+v unregister %+v, want none on store failure",
			eng.registerGroupCalls, eng.unregisterGroupCalls)
	}
}

// TestLocalNode_SetGroupBlockedStoreWriteUnderGate proves the group-block store
// write runs under the exclusive restart gate, then the engine block follows on
// the group lane.
func TestLocalNode_SetGroupBlockedStoreWriteUnderGate(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	if len(probe.inLane) != 1 || probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one false (write under restart gate)", probe.inLane)
	}
	if len(eng.blockGroupCalls) != 1 {
		t.Fatalf("engine block group calls = %+v, want one after store write", eng.blockGroupCalls)
	}
	if !slices.Equal(eng.groupSyncCalls, []string{"desk-a"}) {
		t.Fatalf("group sync calls = %+v, want desk-a", eng.groupSyncCalls)
	}
}

// TestLocalNode_SetGroupBlockedStoreFailureLeavesEngineUntouched proves a
// store-write failure under the gate returns before the engine block runs, so
// the engine is never mutated when the store write cannot commit.
func TestLocalNode_SetGroupBlockedStoreFailureLeavesEngineUntouched(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	probe.failErr = errors.New("store write failed")
	if err := n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller); err == nil {
		t.Fatalf("SetGroupBlocked: want error on store write failure")
	}
	if len(probe.inLane) != 1 || probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one false (write under restart gate)", probe.inLane)
	}
	if len(eng.blockGroupCalls) != 0 {
		t.Fatalf("engine block group calls = %+v, want none on store failure", eng.blockGroupCalls)
	}
}

// testOrder records a committed buy order so an execution report has a parent
// order row to attach its event and trade to.
func testOrder(t *testing.T, st store.RealmStore, id domain.AccountID) domain.Order {
	t.Helper()
	seedTestAccount(t, st, id)
	order, err := st.CreateOrder(context.Background(), domain.Order{
		Account:     id,
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "400",
		Status:      domain.OrderStatusCommitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	return order
}

// TestLocalNode_ApplyExecutionReportPersistsBothLegs verifies a spot fill
// settles both the base (bought) and quote (cash) legs into their own balance
// rows, not collapsed onto the base asset.
func TestLocalNode_ApplyExecutionReportPersistsBothLegs(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportOutcomes = []engine.BalanceOutcome{
		{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "2"}},
		{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "800"}},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	base, ok, err := st.GetBalance(ctx, id, "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance base: %v ok=%v", err, ok)
	}
	if base.Available != "2" {
		t.Fatalf("base available = %q, want 2", base.Available)
	}
	quote, ok, err := st.GetBalance(ctx, id, "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance quote: %v ok=%v", err, ok)
	}
	if quote.Available != "800" {
		t.Fatalf("quote available = %q, want 800", quote.Available)
	}
}

// TestLocalNode_ApplyExecutionReportPostEngineStoreFailureFatals proves the
// execution-report settlement write is a post-engine persistence step: the
// engine applies the report first (execReportCalls==1), then the atomic
// RecordOrderSettlement fails, so the node must fail-stop with the "record
// execution report" operation label, the real account id, and the wrapped cause.
func TestLocalNode_ApplyExecutionReportPostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record execution report failed")
	real := newMemoryStore("node.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failOrderSettlementRealm{RealmStore: r, err: storeErr}
	})

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))

	// Seed the order through the underlying real store; the failing wrapper only
	// rejects the settlement write, so routing reads and the order create succeed.
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	order := testOrder(t, realm, "acc-1")

	_, err = n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("ApplyExecutionReport error = %v, want store failure", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine report calls = %+v, want one engine apply", eng.execReportCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine execution-report persistence failure")
	}
	account, ok, err := realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record execution report"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record execution report failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

func TestLocalNode_ApplyExecutionReportDeletesEmptySettledPosition(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceDelta:  "-100.00",
			BalanceResult: "0",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order := testOrder(t, st, "acc-1")
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD",
		Available: "100",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	_, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusRejected,
		LeavesQuantity: "0",
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance after empty settlement: ok=%v err=%v, want missing", ok, err)
	}
}

func TestLocalNode_ApplyExecutionReportIgnoresReportAssetFields(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "GOLD",
		QuoteAsset:   "EUR",
		Side:         domain.OrderSideSell,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	if _, ok, err := st.GetAsset(ctx, "GOLD"); err != nil || ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want no auto-create", ok, err)
	}
	if len(eng.execReportCalls) != 1 ||
		eng.execReportCalls[0].BaseAsset != order.BaseAsset ||
		eng.execReportCalls[0].QuoteAsset != order.QuoteAsset ||
		eng.execReportCalls[0].Side != order.Side {
		t.Fatalf("engine report calls = %+v, want order instrument", eng.execReportCalls)
	}
}

// TestLocalNode_ApplyExecutionReportUsesOrderAccountOverRouteKey checks that
// the in-lane order read owns the report account even when the caller passed a
// different route key.
func TestLocalNode_ApplyExecutionReportUsesOrderAccountOverRouteKey(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey("fresh-report"), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		FillQuantity: "2",
		FillPrice:    "100",
		LockPrice:    "100",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	if _, ok, err := st.GetAccount(ctx, "fresh-report"); err != nil || ok {
		t.Fatalf("GetAccount(fresh-report) = ok %v err %v, want no account", ok, err)
	}
	if len(eng.execReportCalls) != 1 || eng.execReportCalls[0].Account != "acc-1" {
		t.Fatalf("engine report calls = %+v, want order account acc-1", eng.execReportCalls)
	}
}

// TestLocalNode_ApplyExecutionReportAuditsEngineBlock verifies an engine-
// initiated block during settlement is mirrored to the account and recorded as
// a system-sourced block audit row carrying the engine reason.
func TestLocalNode_ApplyExecutionReportAuditsEngineBlock(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportBlocks = []domain.ExecutionAccountBlock{
		{Account: "acc-1", Code: "pnl_kill_switch", Reason: "loss limit breached"},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	acc, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	wantReason := fmt.Sprintf("loss limit breached [code=pnl_kill_switch, order %s]", order.ExternalID)
	if !acc.Blocked || acc.BlockReason != wantReason {
		t.Fatalf("account block reason = %q, want %q", acc.BlockReason, wantReason)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 1 {
		t.Fatalf("events = %d, want 1 fill event", len(detail.Events))
	}
	payload := detail.Events[0].Payload
	if payload.RejectCode != "pnl_kill_switch" ||
		payload.RejectScope != "account" ||
		payload.RejectReason != "loss limit breached" {
		t.Fatalf("fill event reject payload = %+v", payload)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 block audit row, got %d: %+v", len(rows), rows)
	}
	// A kill-switch block is engine-initiated: SourceSystem marks the channel and
	// the actor is empty (system origin carries no principal dictionary code).
	if rows[0].Source != domain.SourceSystem || rows[0].Actor != "" {
		t.Fatalf("block not attributed to system with empty actor: %+v", rows[0])
	}
	if !strings.Contains(rows[0].Detail, "loss limit breached") {
		t.Fatalf("block detail missing reason: %q", rows[0].Detail)
	}
}

func TestLocalNode_ApplyExecutionReportForce(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)
	// A terminal order is the guard force must bypass; forcing another fill still
	// settles a trade and re-advances the status past the AllowedFrom net.
	if err := st.UpdateOrderStatus(ctx, order.ExternalID, domain.OrderStatusFilled); err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		LockPrice:      "400",
		Force:          true,
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport force: %v", err)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("order status = %q, want filled", detail.Order.Status)
	}

	trades, err := st.ListTrades(ctx, id, domain.SourceAPI, 10)
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("trade count = %d, want 1", len(trades))
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "forced=true") {
		t.Fatalf("audit rows = %+v, want forced=true execution report", rows)
	}
}

func TestLocalNode_ApplyExecutionReportForceOnOpenOrderDoesNotAuditForced(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "1",
		Force:          true,
		OrderStatus:    domain.OrderStatusPartiallyFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport force open: %v", err)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || strings.Contains(rows[0].Detail, "forced=true") {
		t.Fatalf("audit rows = %+v, want no forced=true", rows)
	}
}

// TestLocalNode_ApplyExecutionReportStatusOnlyNoAccountWrites drives a
// status-only report through the fake's real persistence mapper (no emptyExecReportPersistence
// shortcut): the report carries no fill, no engine outcomes, and no blocks, so the
// mapper emits nil Balances and empty Blocks. The node must then write zero
// balance rows and zero account-block state/audit rows while still recording the
// status change.
func TestLocalNode_ApplyExecutionReportStatusOnlyNoAccountWrites(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:       order.ExternalID,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusCancelled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport status-only: %v", err)
	}

	if _, ok, err := st.GetBalance(ctx, id, "AAPL"); err != nil || ok {
		t.Fatalf("base balance ok=%v err=%v, want no balance row written", ok, err)
	}
	if _, ok, err := st.GetBalance(ctx, id, "USD"); err != nil || ok {
		t.Fatalf("quote balance ok=%v err=%v, want no balance row written", ok, err)
	}
	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount ok=%v err=%v", ok, err)
	}
	if account.Blocked {
		t.Fatal("account is blocked, want no account-block write on a status-only report")
	}
	blockRows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(blockRows) != 0 {
		t.Fatalf("block audit rows = %+v, want none", blockRows)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusCancelled {
		t.Fatalf("status = %q, want cancelled (the venue-owned status change is recorded)",
			detail.Order.Status)
	}
}

func TestLocalNode_ApplyExecutionReportUsesOrderFields(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		Account:        "wrong-account",
		BaseAsset:      "WRONG",
		QuoteAsset:     "NOPE",
		Side:           domain.OrderSideSell,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "1",
		OrderStatus:    domain.OrderStatusPartiallyFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	call := eng.execReportCalls[0]
	if call.Account != id ||
		call.BaseAsset != order.BaseAsset ||
		call.QuoteAsset != order.QuoteAsset ||
		call.Side != order.Side {
		t.Fatalf("engine call = %+v, want authoritative order fields from %+v",
			call, order)
	}
}

func TestLocalNode_ApplyExecutionReportIgnoresCanceledContext(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	reportCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := n.ApplyExecutionReport(reportCtx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport with canceled context: %v", err)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled", detail.Order.Status)
	}
}

func TestLocalNode_ApplyExecutionReportUsesAccountSyncLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if eng.execReportOutsideSync {
		t.Fatal("execution report applied outside the account sync lane")
	}
	if len(eng.accountSyncCalls) == 0 || eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != id {
		t.Fatalf("account sync calls = %+v, want last %s", eng.accountSyncCalls, id)
	}
}

func TestLocalNode_ApplyExecutionReportAlwaysUsesEngine(t *testing.T) {
	t.Parallel()
	persistedLock := []byte{0x01, 0x02, 0x03}
	cases := []struct {
		name            string
		input           domain.ExecutionReportInput
		orderStatus     domain.OrderStatus
		heldIntent      bool
		outcomes        []engine.BalanceOutcome
		blocks          []domain.ExecutionAccountBlock
		wantStatus      domain.OrderStatus
		wantLeaves      string
		wantEvent       domain.OrderEventType
		wantTrade       bool
		wantBlocked     bool
		wantNoQuote     bool
		wantCommit      int
		wantRollback    int
		wantIntentState domain.ReservationIntentState
	}{
		{
			name: "committed reject releases funds",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusRejected,
				LeavesQuantity: "0",
			},
			orderStatus: domain.OrderStatusCommitted,
			outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "10000",
					HeldResult:    "0",
				},
			}},
			wantStatus: domain.OrderStatusRejected,
			wantLeaves: "0",
			wantEvent:  domain.OrderEventPreTradeRejected,
		},
		{
			name: "committed cancel releases funds",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusCancelled,
				LeavesQuantity: "0",
			},
			orderStatus: domain.OrderStatusCommitted,
			outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "10000",
					HeldResult:    "0",
				},
			}},
			wantStatus: domain.OrderStatusCancelled,
			wantLeaves: "0",
			wantEvent:  domain.OrderEventCancelled,
		},
		{
			name: "fill stays fill settlement",
			input: domain.ExecutionReportInput{
				FillQuantity:   "2",
				FillPrice:      "400",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusFilled,
			},
			orderStatus: domain.OrderStatusCommitted,
			outcomes: []engine.BalanceOutcome{
				{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "2"}},
				{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "0"}},
			},
			wantStatus:  domain.OrderStatusFilled,
			wantLeaves:  "0",
			wantEvent:   domain.OrderEventFill,
			wantTrade:   true,
			wantNoQuote: true,
		},
		{
			name: "engine block still applies balances",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusRejected,
				LeavesQuantity: "0",
			},
			orderStatus: domain.OrderStatusCommitted,
			outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "10000",
					HeldResult:    "0",
				},
			}},
			blocks: []domain.ExecutionAccountBlock{{
				Account: "acc-1",
				Code:    "post_trade_reject",
				Reason:  "engine rejected report",
			}},
			wantStatus:  domain.OrderStatusRejected,
			wantLeaves:  "0",
			wantEvent:   domain.OrderEventPreTradeRejected,
			wantBlocked: true,
		},
		{
			name: "accepted held fill marks reservation committed",
			input: domain.ExecutionReportInput{
				FillQuantity:   "2",
				FillPrice:      "400",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusFilled,
			},
			orderStatus: domain.OrderStatusAccepted,
			heldIntent:  true,
			outcomes: []engine.BalanceOutcome{
				{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "2"}},
				{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "10000", HeldResult: "0"}},
			},
			wantStatus:      domain.OrderStatusFilled,
			wantLeaves:      "0",
			wantEvent:       domain.OrderEventFill,
			wantTrade:       true,
			wantIntentState: domain.ReservationIntentStateCommitted,
		},
		{
			name: "accepted held reject applies only report engine adjustments",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusRejected,
				LeavesQuantity: "0",
			},
			orderStatus: domain.OrderStatusAccepted,
			heldIntent:  true,
			outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "10000",
					HeldResult:    "0",
				},
			}},
			wantStatus:      domain.OrderStatusRejected,
			wantLeaves:      "0",
			wantEvent:       domain.OrderEventPreTradeRejected,
			wantIntentState: domain.ReservationIntentStateRolledBack,
		},
		{
			name: "empty engine response changes no balances",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusRejected,
				LeavesQuantity: "0",
			},
			orderStatus:     domain.OrderStatusAccepted,
			heldIntent:      true,
			wantStatus:      domain.OrderStatusRejected,
			wantLeaves:      "0",
			wantEvent:       domain.OrderEventPreTradeRejected,
			wantIntentState: domain.ReservationIntentStateRolledBack,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			eng.execReportOutcomes = tc.outcomes
			eng.execReportBlocks = tc.blocks
			n, st := newTestNode(t, eng)
			ctx := context.Background()

			const id domain.AccountID = "acc-1"
			if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			order, err := st.CreateOrder(ctx, domain.Order{
				Account:     id,
				Source:      domain.SourceAPI,
				Principal:   "operator",
				BaseAsset:   "AAPL",
				QuoteAsset:  "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "2",
				Price:       "400",
				Lock:        persistedLock,
				Status:      tc.orderStatus,
			})
			if err != nil {
				t.Fatalf("CreateOrder: %v", err)
			}
			if tc.heldIntent {
				if err := st.UpsertBalance(ctx, domain.Balance{
					Account:   id,
					Asset:     "USD",
					Available: "8000",
					Held:      "2000",
				}); err != nil {
					t.Fatalf("UpsertBalance: %v", err)
				}
				if err := st.UpsertReservationIntent(ctx, domain.ReservationIntent{
					ApprovalID: "approval-1",
					Order:      order.ExternalID,
					Account:    order.Account,
					ParamsJSON: `{"id":1}`,
					IssuedAt:   time.Now().UTC(),
					ExpiresAt:  time.Now().UTC().Add(2 * time.Minute),
					State:      domain.ReservationIntentStateHeld,
				}); err != nil {
					t.Fatalf("UpsertReservationIntent: %v", err)
				}
			}

			in := tc.input
			in.Order = order.ExternalID
			in.BaseAsset = "AAPL"
			in.QuoteAsset = "USD"
			in.Side = domain.OrderSideBuy
			result, err := n.ApplyExecutionReport(ctx, testKey(id), in, testCaller)
			if err != nil {
				t.Fatalf("ApplyExecutionReport: %v", err)
			}
			if len(result.Blocks) != len(tc.blocks) || len(result.Outcomes) != len(tc.outcomes) {
				t.Fatalf("result = %+v, want blocks=%d outcomes=%d",
					result, len(tc.blocks), len(tc.outcomes))
			}
			if len(eng.execReportCalls) != 1 {
				t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
			}
			if len(eng.rollbackHeldCalls) != tc.wantRollback {
				t.Fatalf("rollback calls = %+v, want %d", eng.rollbackHeldCalls, tc.wantRollback)
			}
			if len(eng.commitHeldCalls) != tc.wantCommit {
				t.Fatalf("commit calls = %+v, want %d", eng.commitHeldCalls, tc.wantCommit)
			}
			call := eng.execReportCalls[0]
			if call.Order != order.ExternalID ||
				call.Account != id ||
				!slices.Equal(call.Lock, persistedLock) {
				t.Fatalf("engine call = %+v, want order/account/persisted lock", call)
			}
			if call.LeavesQuantity != in.LeavesQuantity {
				t.Fatalf("engine leaves = %q, want request leaves %q",
					call.LeavesQuantity, in.LeavesQuantity)
			}

			detail, err := st.GetOrder(ctx, order.ExternalID)
			if err != nil {
				t.Fatalf("GetOrder: %v", err)
			}
			if detail.Order.Status != tc.wantStatus {
				t.Fatalf("order status = %q, want %q", detail.Order.Status, tc.wantStatus)
			}
			if detail.Order.Leaves != tc.wantLeaves {
				t.Fatalf("leaves = %q, want %q", detail.Order.Leaves, tc.wantLeaves)
			}
			if len(detail.Events) == 0 || detail.Events[0].Type != tc.wantEvent {
				t.Fatalf("events = %+v, want first %s", detail.Events, tc.wantEvent)
			}
			if gotTrade := len(detail.Trades) == 1; gotTrade != tc.wantTrade {
				t.Fatalf("trade present = %v, want %v: %+v", gotTrade, tc.wantTrade, detail.Trades)
			}
			if tc.heldIntent {
				intent, ok, err := st.GetReservationIntent(ctx, "approval-1")
				if err != nil || !ok {
					t.Fatalf("GetReservationIntent: %v ok=%v", err, ok)
				}
				if intent.State != tc.wantIntentState {
					t.Fatalf("intent state = %q, want %q", intent.State, tc.wantIntentState)
				}
				types := eventTypes(t, st, order.ExternalID)
				if len(types) != 1 || types[0] != tc.wantEvent {
					t.Fatalf("events = %+v, want [%s]", types, tc.wantEvent)
				}
			}

			quote, ok, err := st.GetBalance(ctx, id, "USD")
			if err != nil {
				t.Fatalf("GetBalance quote: %v", err)
			}
			if tc.wantNoQuote {
				if ok {
					t.Fatalf("quote balance = %+v, want missing", quote)
				}
				return
			}
			if !ok {
				t.Fatalf("GetBalance quote ok=false")
			}
			if tc.heldIntent {
				wantAvailable := "10000"
				if len(tc.outcomes) == 0 {
					wantAvailable = "8000"
				}
				if quote.Available != wantAvailable {
					t.Fatalf("quote available = %q, want %s", quote.Available, wantAvailable)
				}
			} else if len(tc.outcomes) > 0 && tc.outcomes[0].Asset == "USD" &&
				quote.Available != "10000" {
				t.Fatalf("quote available = %q, want 10000", quote.Available)
			}
			if tc.heldIntent {
				wantHeld := "0"
				if len(tc.outcomes) == 0 {
					wantHeld = "2000"
				}
				if quote.Held != wantHeld {
					t.Fatalf("quote held = %q, want %s", quote.Held, wantHeld)
				}
			}
			if tc.wantBlocked {
				acc, ok, err := st.GetAccount(ctx, id)
				if err != nil || !ok {
					t.Fatalf("GetAccount: %v ok=%v", err, ok)
				}
				if !acc.Blocked || !strings.Contains(acc.BlockReason, "engine rejected report") {
					t.Fatalf("account block = %+v, want engine reason", acc)
				}
			}
		})
	}
}

func TestLocalNode_ApplyExecutionReportUsesTargetedReservationLookup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	baseStore := newMemoryStore("node.db")
	ctx := context.Background()
	if err := baseStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = baseStore.Close() })

	wrapped := newRealmWrapStore(baseStore, func(inner store.RealmStore) store.RealmStore {
		return &noFullScanReservationRealm{RealmStore: inner}
	})
	n := newTestNodeWithStore(t, wrapped, eng)
	realm := wrapped.realm.(*noFullScanReservationRealm)

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order, err := realm.CreateOrder(ctx, domain.Order{
		Account:     id,
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "400",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := realm.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "approval-1",
		Order:      order.ExternalID,
		Account:    order.Account,
		ParamsJSON: `{"id":1}`,
		IssuedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(2 * time.Minute),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	realm.forbidFullScans()
	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		OrderStatus:    domain.OrderStatusCancelled,
		LeavesQuantity: "0",
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	targeted, fullScans := realm.lookupCounts()
	if targeted != 1 || fullScans != 0 {
		t.Fatalf("reservation lookups targeted=%d fullScans=%d, want 1/0", targeted, fullScans)
	}
	if len(eng.rollbackHeldCalls) != 0 || len(eng.commitHeldCalls) != 0 {
		t.Fatalf("commit=%+v rollback=%+v, want no explicit resolve calls",
			eng.commitHeldCalls, eng.rollbackHeldCalls)
	}
}

func TestLocalNode_ApplyExecutionReportNilPersistenceErrors(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.emptyExecReportPersistence = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	_, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport nil persistence = %v, want ErrInvalid", err)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != order.Status || detail.Order.Leaves != order.Leaves {
		t.Fatalf("order = %+v, want status/leaves unchanged from %+v",
			detail.Order, order)
	}
	if len(detail.Events) != 0 || len(detail.Trades) != 0 {
		t.Fatalf("detail = %+v, want no events or trades", detail)
	}
}

// TestLocalNode_ApplyExecutionReportLeavesFollowsFills verifies the persisted
// remaining open quantity starts from the stored order request value, follows a
// partial fill's reported leaves, and reaches "0" on the final fill.
func TestLocalNode_ApplyExecutionReportLeavesFollowsFills(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)
	if order.Leaves != "" {
		t.Fatalf("initial leaves = %q, want request value", order.Leaves)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "1",
		OrderStatus:    domain.OrderStatusPartiallyFilled,
	}, testCaller); err != nil {
		t.Fatalf("partial fill: %v", err)
	}
	partial, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder partial: %v", err)
	}
	if partial.Order.Status != domain.OrderStatusPartiallyFilled {
		t.Fatalf("status = %q, want partially_filled", partial.Order.Status)
	}
	if partial.Order.Leaves != "1" {
		t.Fatalf("leaves after partial = %q, want 1", partial.Order.Leaves)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("final fill: %v", err)
	}
	filled, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder final: %v", err)
	}
	if filled.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled", filled.Order.Status)
	}
	if filled.Order.Leaves != "0" {
		t.Fatalf("leaves after final = %q, want 0", filled.Order.Leaves)
	}
}

func TestLocalNode_ApplyExecutionReportRejectsInvalidStatusBeforeEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	_, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "1",
		FillPrice:    "400",
		LockPrice:    "400",
		Force:        true,
		OrderStatus:  domain.OrderStatus("bogus"),
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport invalid status = %v, want invalid", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("invalid status reached engine: %+v", eng.execReportCalls)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != order.Status {
		t.Fatalf("order status = %q, want unchanged %q", detail.Order.Status, order.Status)
	}
}

// TestEngineBlockReason verifies the account block_reason composed for an
// engine-initiated block carries the engine reason plus the cause (code and
// triggering order), falling back to the code when no reason is given and
// appending details when present.
func TestEngineBlockReason(t *testing.T) {
	t.Parallel()
	order := mustExternalID(t)
	cases := []struct {
		name  string
		block domain.ExecutionAccountBlock
		want  string
	}{
		{
			name:  "reason and code",
			block: domain.ExecutionAccountBlock{Account: "acc-1", Code: "pnl_kill_switch", Reason: "loss limit breached"},
			want:  fmt.Sprintf("loss limit breached [code=pnl_kill_switch, order %s]", order),
		},
		{
			name:  "empty reason falls back to code",
			block: domain.ExecutionAccountBlock{Account: "acc-1", Code: "pnl_kill_switch"},
			want:  fmt.Sprintf("pnl_kill_switch [code=pnl_kill_switch, order %s]", order),
		},
		{
			name:  "details appended",
			block: domain.ExecutionAccountBlock{Account: "acc-1", Code: "pnl_kill_switch", Reason: "loss limit breached", Details: "upper bound 1000"},
			want:  fmt.Sprintf("loss limit breached [code=pnl_kill_switch, order %s, upper bound 1000]", order),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := engineBlockReason(order, tc.block); got != tc.want {
				t.Fatalf("engineBlockReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// mustExternalID returns a fixed generated external id for the audit-detail
// tests that need a stable order handle to render.
func mustExternalID(t *testing.T) domain.ExternalID {
	t.Helper()
	id, err := domain.ParseExternalID("AAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatalf("ParseExternalID: %v", err)
	}
	return id
}

// externalID derives a deterministic, canonical external id from a short label
// so a test can supply a stable handle and reuse it (e.g. to provoke a
// duplicate).
func externalID(t *testing.T, label string) domain.ExternalID {
	t.Helper()
	id := domain.ExternalID(label)
	if err := domain.ValidateExternalID(id.String()); err != nil {
		t.Fatalf("externalID(%q) not canonical: %v", label, err)
	}
	return id
}

// TestNewLocalNode_SeedsBuildAndAudits verifies the constructor seeds the
// engine build from the store snapshot and writes one startup hydrate audit
// row. The store is pre-populated before the node is built so the seed is
// non-empty.
func TestNewLocalNode_SeedsBuildAndAudits(t *testing.T) {
	t.Parallel()
	st := newMemoryStore("node.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}

	// Pre-seed the store: one account and one rate-limit barrier.
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := realm.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second)); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	eng := newFakeEngine()
	var seed engine.Snapshot
	n, got, err := NewLocalNode(ctx, st, fakeBuild(eng, &seed))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	if got != eng {
		t.Fatalf("NewLocalNode returned a different engine handle")
	}

	// The build must be seeded from the store snapshot.
	if len(seed.Accounts) != 1 || len(seed.RateLimits) != 1 {
		t.Fatalf("build not seeded from store: %+v", seed)
	}
	if seed.Accounts[0].Code != "acc-1" {
		t.Fatalf("seeded wrong account: %+v", seed.Accounts)
	}

	rows, err := realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// The startup hydrate is system-initiated: SourceSystem with an empty actor
	// (system origin carries no principal dictionary code).
	if len(rows) != 1 ||
		rows[0].Action != domain.AuditActionHydrate ||
		rows[0].Actor != "" || rows[0].Source != domain.SourceSystem {
		t.Fatalf("want one system hydrate row, got %+v", rows)
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestNewLocalNode_BuildFailure verifies a build failure is surfaced and no node
// is returned.
func TestNewLocalNode_BuildFailure(t *testing.T) {
	t.Parallel()
	st := newMemoryStore("node.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	failBuild := func(engine.Snapshot) (engine.Engine, error) {
		return nil, errors.New("build boom")
	}
	if _, _, err := NewLocalNode(ctx, st, failBuild); err == nil {
		t.Fatalf("NewLocalNode: want error on build failure")
	}

	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	// A failed build writes no audit row.
	rows, err := realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want no audit row on build failure, got %+v", rows)
	}
}

// TestNewLocalNode_NilArgs verifies the constructor rejects nil dependencies.
func TestNewLocalNode_NilArgs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	build := func(engine.Snapshot) (engine.Engine, error) {
		return newFakeEngine(), nil
	}
	if _, _, err := NewLocalNode(ctx, nil, build); err == nil {
		t.Fatalf("want error for nil store")
	}

	st := newMemoryStore("node.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := NewLocalNode(ctx, st, nil); err == nil {
		t.Fatalf("want error for nil build func")
	}
}

func TestLocalNode_CheckOrderDelegatesToEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.checkResult = domain.CheckResult{Passed: true, WouldLockPrices: []string{"100"}}
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100",
	}
	out, err := n.CheckOrder(ctx, testKey("acc-1"), probe)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed || len(out.WouldLockPrices) != 1 {
		t.Fatalf("engine result not propagated: %+v", out)
	}
	if len(eng.checkProbes) != 1 || eng.checkProbes[0].Account != "acc-1" {
		t.Fatalf("probe must be forwarded to the engine once")
	}
	if eng.operationOutsideSync {
		t.Fatal("check order ran outside the account sync lane")
	}
	if len(eng.accountSyncCalls) == 0 ||
		eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != "acc-1" {
		t.Fatalf("account sync calls = %+v, want last acc-1", eng.accountSyncCalls)
	}
}

// TestLocalNode_CheckOrderWritesNoAudit asserts the non-mutating check writes
// no audit row and is side-effect-free across repeated calls: the audit count
// is unchanged from before the first check, and the engine is only ever asked
// to dry-run (the node never calls SubmitOrder/commit for a check).
func TestLocalNode_CheckOrderWritesNoAudit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.checkResult = domain.CheckResult{
		Passed:  false,
		Rejects: []domain.OrderReject{{Code: "rate_limit_exceeded", Scope: "account"}},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	before, err := st.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit before: %v", err)
	}

	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}
	for i := 0; i < 3; i++ {
		if _, err := n.CheckOrder(ctx, testKey("acc-1"), probe); err != nil {
			t.Fatalf("CheckOrder #%d: %v", i, err)
		}
	}

	after, err := st.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("check must write no audit row: before=%d after=%d", len(before), len(after))
	}
	if len(eng.submitCalls) != 0 || len(eng.execReportCalls) != 0 {
		t.Fatalf("check must not submit or settle: submit=%d exec=%d",
			len(eng.submitCalls), len(eng.execReportCalls))
	}
	if len(eng.checkProbes) != 3 {
		t.Fatalf("want 3 dry-run calls, got %d", len(eng.checkProbes))
	}
}

func TestLocalNode_PutAccountAssetRateLimit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// An account-asset scope barrier links a real account and asset; the store
	// enforces that both dictionary rows exist before the barrier is keyed to them.
	seedTestAccount(t, st, "acc-1")
	limit := rateLimit(domain.ScopeAccountAsset, "acc-1", "AAPL", 50, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutRateLimit account-asset: %v", err)
	}
	if len(eng.configureCalls) != 1 {
		t.Fatalf("configure calls = %d, want 1", len(eng.configureCalls))
	}
}

// TestLocalNode_PutOrderSizeAndPnlBoundsLimits verifies the order-size and
// P&L-bounds put paths persist their typed barrier and configure the matching
// policy from the store, exercising the typed limit surface beyond rate limits.
func TestLocalNode_PutOrderSizeAndPnlBoundsLimits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	size := domain.LimitOrderSize{Scope: domain.ScopeBroker, MaxQuantity: "100"}
	if _, err := n.PutOrderSizeLimit(ctx, size, testCaller); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
	pnl := domain.LimitPnlBounds{Scope: domain.ScopeAsset, Asset: "USD", LowerBound: "-1000"}
	if _, err := n.PutPnlBoundsLimit(ctx, pnl, testCaller); err != nil {
		t.Fatalf("PutPnlBoundsLimit: %v", err)
	}

	storedSize, err := st.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(storedSize) != 1 || storedSize[0] != size {
		t.Fatalf("order-size barriers = %+v, want the one barrier", storedSize)
	}
	storedPnl, err := st.ListPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListPnlBoundsLimits: %v", err)
	}
	if len(storedPnl) != 1 || storedPnl[0] != pnl {
		t.Fatalf("pnl-bounds barriers = %+v, want the one barrier", storedPnl)
	}

	// Two configure calls, one per policy, each carrying only its own slice.
	if len(eng.configureCalls) != 2 {
		t.Fatalf("configure calls = %d, want 2", len(eng.configureCalls))
	}
	if eng.configureCalls[0].policy != domain.PolicyOrderSizeLimit ||
		len(eng.configureCalls[0].limits.OrderSizeLimits) != 1 {
		t.Fatalf("first configure = %+v, want order-size policy", eng.configureCalls[0])
	}
	if eng.configureCalls[1].policy != domain.PolicyPnlBoundsKillSwitch ||
		len(eng.configureCalls[1].limits.PnlBoundsLimits) != 1 {
		t.Fatalf("second configure = %+v, want pnl-bounds policy", eng.configureCalls[1])
	}

	// ListLimits bundles the matching barriers per policy into AccountLimits.
	limits, err := n.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListLimits: %v", err)
	}
	if len(limits.OrderSizeLimits) != 1 || len(limits.PnlBoundsLimits) != 1 ||
		len(limits.RateLimits) != 0 {
		t.Fatalf("listed limits = %+v, want one order-size and one pnl-bounds", limits)
	}
}

func TestLocalNode_ExportBackupAudits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, st := newTestNode(t, newFakeEngine())

	if _, err := n.ExportBackup(ctx, backup.Scope{All: true}, testCaller); err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if rows[0].Action != domain.AuditActionExportBackup {
		t.Fatalf("latest action = %q, want export_backup", rows[0].Action)
	}
	if rows[0].Actor != testCaller.Principal ||
		rows[0].Source != testCaller.Source {
		t.Fatalf("audit attribution = %+v, want %+v", rows[0], testCaller)
	}
}

// testArchive builds a portable archive carrying data, labelled with the default
// realm, for the restore tests.
func testArchive(scope backup.Scope, data backup.Data) backup.Archive {
	return backup.NewArchive(
		backupTestTime(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		data,
	)
}

func TestLocalNode_RestoreBackupRebuildsEngineFromRestoredStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	nextEngine := newFakeEngine()
	var captured engine.Snapshot
	n.build = fakeBuild(nextEngine, &captured)
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "restored"}}},
	)

	summary, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatalf("RestartRequired = false, want true")
	}
	if oldEngine.running {
		t.Fatalf("old engine still running after restore swap")
	}
	if !nextEngine.running {
		t.Fatalf("next engine not running after restore")
	}
	if len(captured.Accounts) != 1 || captured.Accounts[0].Code != "restored" {
		t.Fatalf("build snapshot accounts = %+v", captured.Accounts)
	}
	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if rows[0].Action != domain.AuditActionRestoreBackup {
		t.Fatalf("latest action = %q, want restore_backup", rows[0].Action)
	}
}

// TestLocalNode_RestoreBackupNonRuntimeScopeRebuildsForForceIncludedGroup proves
// a restore whose REQUESTED scope names only a non-runtime section (the audit
// log) still rebuilds the engine when the archive carries a group the local
// resolver has not seen. Normalize force-includes the accounts+groups dictionary
// so the archived group lands, and the store now raises RestartRequired from the
// rows actually written, so the node takes the exclusive restart gate and
// rebuilds. Against the old TouchesRuntime(opts.Scope) code the audit-only scope
// read as observational: no rebuild ran, the store held the new group but the
// live resolver did not, and the SetGroupBlocked below rejected the group it
// lists with ErrInvalid.
func TestLocalNode_RestoreBackupNonRuntimeScopeRebuildsForForceIncludedGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, _ := newTestNode(t, oldEngine)

	// A strict resolver rejects any group it has not seen in a build snapshot.
	// The rebuilt engine adopts the snapshot, so it learns the restored group.
	nextEngine := newFakeEngine()
	nextEngine.enforceResolver = true
	var captured engine.Snapshot
	n.build = fakeBuild(nextEngine, &captured)

	// The archive carries a new group (and its member account) but the caller
	// requests ONLY the audit log: Normalize force-includes accounts+groups so the
	// group travels and restores, even though audit_log is observational.
	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}},
		backup.Data{
			Groups:   []backup.AccountGroup{{Code: "restored-desk"}},
			Accounts: []backup.Account{{Code: "restored-acc", GroupCode: "restored-desk"}},
		},
	)

	summary, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}},
		Mode:  backup.RestoreModeInsertMissing,
	}, testCaller)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatalf("RestartRequired = false, want true for a force-included group write")
	}
	if oldEngine.running {
		t.Fatalf("old engine still running: rebuild did not swap the engine")
	}
	if _, ok := nextEngine.knownGroups["restored-desk"]; !ok {
		t.Fatalf("rebuilt resolver missing restored-desk: %+v", nextEngine.knownGroups)
	}

	// The store lists the group and, because the rebuild reconciled the resolver,
	// a group block into it succeeds instead of failing with ErrInvalid.
	if err := n.SetGroupBlocked(ctx, "restored-desk", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked into restored group: %v", err)
	}
	if len(nextEngine.blockGroupCalls) != 1 ||
		nextEngine.blockGroupCalls[0].groupID != "restored-desk" {
		t.Fatalf("block group calls = %+v, want one for restored-desk",
			nextEngine.blockGroupCalls)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreOnRebuildFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	if _, err := n.CreateAccount(ctx, testAccount("keep"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, errors.New("build failed")
	}
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "new"}}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want rebuild failure")
	}
	if !oldEngine.running {
		t.Fatalf("old engine stopped after failed rebuild")
	}
	// The pre-restore state is restored exactly: the rollback re-applies the
	// captured archive with replace-all, so the account that existed before the
	// failed restore is present and the account the failed forward restore
	// inserted is gone (replace-all deletes rows the rollback archive omits).
	if _, ok, err := st.GetAccount(ctx, "keep"); err != nil || !ok {
		t.Fatalf("keep account ok=%v err=%v, want present", ok, err)
	}
	if _, ok, err := st.GetAccount(ctx, "new"); err != nil || ok {
		t.Fatalf("new account ok=%v err=%v, want removed by rollback", ok, err)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreAndEngineOnAuditFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()

	real := newMemoryStore("node.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failRestoreAuditRealm{RealmStore: r}
	})

	nextEngine := newFakeEngine()
	rollbackEngine := newFakeEngine()
	var buildCount int
	var rollbackSnapshot engine.Snapshot
	build := func(snap engine.Snapshot) (engine.Engine, error) {
		buildCount++
		switch buildCount {
		case 1:
			return oldEngine, nil
		case 2:
			return nextEngine, nil
		default:
			rollbackSnapshot = snap
			return rollbackEngine, nil
		}
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)
	if _, err := n.CreateAccount(ctx, testAccount("keep"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "new"}}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want audit failure")
	}
	if nextEngine.running {
		t.Fatalf("restored engine still running after rollback")
	}
	if !rollbackEngine.running {
		t.Fatalf("rollback engine not running")
	}
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if _, ok, err := realm.GetAccount(ctx, "keep"); err != nil || !ok {
		t.Fatalf("keep account ok=%v err=%v, want present", ok, err)
	}
	// The replace-all rollback deletes the account the failed forward restore
	// inserted: it is not in the captured pre-restore archive.
	if _, ok, err := realm.GetAccount(ctx, "new"); err != nil || ok {
		t.Fatalf("new account ok=%v err=%v, want removed by rollback", ok, err)
	}
	// The rollback engine is rebuilt from the restored pre-restore snapshot, which
	// carries the account that existed before the failed restore.
	if len(rollbackSnapshot.Accounts) == 0 {
		t.Fatalf("rollback snapshot accounts = %+v, want the pre-restore account", rollbackSnapshot.Accounts)
	}
	found := false
	for _, a := range rollbackSnapshot.Accounts {
		if a.Code == "keep" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rollback snapshot accounts = %+v, want to include keep", rollbackSnapshot.Accounts)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreOnAuditFailureWithoutRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()

	real := newMemoryStore("node.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	realm0, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if err := realm0.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failRestoreAuditRealm{RealmStore: r}
	})
	n := newTestNodeWithStore(t, st, oldEngine)
	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{McpAccess: map[string]bool{"submit_order": false}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want audit failure")
	}
	if !oldEngine.running {
		t.Fatalf("engine stopped for non-runtime rollback")
	}
	access, err := realm0.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if access["submit_order"] != true {
		t.Fatalf("mcp access = %+v, want rollback to true", access)
	}
}

func TestLocalNode_RestoreBackupJoinsRollbackRestoreFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	real := newMemoryStore("node.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	realm0, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if err := realm0.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	rollbackErr := errors.New("rollback restore failed")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failRollbackRestoreRealm{
			RealmStore:  &failRestoreAuditRealm{RealmStore: r},
			rollbackErr: rollbackErr,
		}
	})
	n := newTestNodeWithStore(t, st, newFakeEngine())
	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{McpAccess: map[string]bool{"submit_order": false}},
	)

	_, _, err = n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want audit and rollback failure")
	}
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("RestoreBackup error = %v, want rollback restore error", err)
	}
	if !errors.Is(err, errRestoreAuditFailed) {
		t.Fatalf("RestoreBackup error = %v, want audit failure", err)
	}
}

func TestLocalNode_ResetDatabaseRecreatesStoreAndAudits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("reset.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	oldEngine := newFakeEngine()
	nextEngine := newFakeEngine()
	builds := 0
	build := func(engine.Snapshot) (engine.Engine, error) {
		builds++
		if builds == 1 {
			return oldEngine, nil
		}
		return nextEngine, nil
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)
	if _, err := n.CreateAccount(ctx, testAccount("reset-me"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	sink, err := n.ResetDatabase(ctx, testCaller)
	if err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if sink == nil {
		t.Fatalf("ResetDatabase returned nil sink")
	}
	// Three builds: the initial seed, the rebuild CreateAccount triggers so the
	// new account enters the resolver, and the rebuild ResetDatabase performs.
	if builds != 3 {
		t.Fatalf("build calls = %d, want 3", builds)
	}
	if oldEngine.running {
		t.Fatalf("old engine still running after reset")
	}
	if !nextEngine.running {
		t.Fatalf("next engine is not running after reset")
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	accounts, err := realm.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts after reset = %+v, want none", accounts)
	}
	rows, err := realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 || rows[0].Action != domain.AuditActionResetDatabase {
		t.Fatalf("audit after reset = %+v, want reset row only", rows)
	}
	// The reset audit lands in the freshly-emptied dictionary, so it carries the
	// caller's source channel but no actor principal.
	if rows[0].Actor != "" || rows[0].Source != testCaller.Source {
		t.Fatalf("audit attribution = %+v, want empty actor and source %s", rows[0], testCaller.Source)
	}
}

func TestLocalNode_ConcurrentRestoreSwapAndReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, _ := newTestNode(t, newFakeEngine())
	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := n.CheckOrder(ctx, testKey("acc-1"), probe); err != nil {
				errs <- err
				return
			}
			if _, err := n.Health(ctx); err != nil {
				errs <- err
				return
			}
			_ = n.EngineVersion()
		}
	}()
	for i := 0; i < 25; i++ {
		n.build = func(engine.Snapshot) (engine.Engine, error) {
			return newFakeEngine(), nil
		}
		archive := backup.NewArchive(
			backupTestTime().Add(time.Duration(i)*time.Second),
			"test",
			backup.RealmLabel{Code: string(domain.DefaultRealm)},
			backup.Scope{All: true},
			backup.Data{Accounts: []backup.Account{{
				Code: fmt.Sprintf("acc-%d", i),
			}}},
		)
		if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
			Scope: backup.Scope{All: true},
			Mode:  backup.RestoreModeReplaceAll,
		}, testCaller); err != nil {
			close(stop)
			t.Fatalf("RestoreBackup #%d: %v", i, err)
		}
	}
	close(stop)
	<-done
	select {
	case err := <-errs:
		t.Fatalf("concurrent read error: %v", err)
	default:
	}
}

func backupTestTime() time.Time {
	return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
}

func TestLocalNode_ErrorMessagesNoNodePrefix(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	// GetAccountState on a missing account must surface a not-found error whose
	// message does not start with "node: ".
	_, _, err := n.GetAccountState(ctx, testKey("no-such-account"))
	if err == nil {
		t.Fatal("want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	msg := err.Error()
	if len(msg) >= 6 && msg[:6] == "node: " {
		t.Fatalf("error message must not start with \"node: \", got: %s", msg)
	}

	// SetAccountBlocked on a missing account must also not carry "node: ".
	err = n.SetAccountBlocked(ctx, testKey("no-such-account"), true, "test", testCaller)
	if err == nil {
		t.Fatal("want error for missing account on block, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	msg = err.Error()
	if len(msg) >= 6 && msg[:6] == "node: " {
		t.Fatalf("error message must not start with \"node: \", got: %s", msg)
	}
}

// TestLocalNode_MissingAccountAdminNotFoundWithResolver guards the pre-lane
// existence check in SetAccountBlocked and SetAccountGroup. The fake engine runs
// with enforceResolver=true, so its RunAccountSynchronized rejects an unknown
// account with domain.ErrInvalid before the closure runs, mirroring the real
// adapter. The only way these methods can still surface domain.ErrNotFound is
// the pre-lane realm existence check: remove it and the missing account would
// fall through to the resolver, regressing 404 (ErrNotFound) to 400 (ErrInvalid).
func TestLocalNode_MissingAccountAdminNotFoundWithResolver(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	// SetAccountBlocked on a missing account must be ErrNotFound (404), not the
	// resolver's ErrInvalid (400).
	err := n.SetAccountBlocked(ctx, testKey("no-such-account"), true, "risk", testCaller)
	if err == nil {
		t.Fatal("SetAccountBlocked: want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetAccountBlocked: want ErrNotFound, got %v", err)
	}

	// SetAccountGroup on a missing account must likewise be ErrNotFound (404).
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	err = n.SetAccountGroup(ctx, testKey("no-such-account"), "desk-a", testCaller)
	if err == nil {
		t.Fatal("SetAccountGroup: want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetAccountGroup: want ErrNotFound, got %v", err)
	}
}

// --- Atomic held-resolution / settlement at the node layer -------------------

// seedHeldOrder creates an accepted order plus a held reservation intent bound to
// it, returning the order. It mirrors the seeding the backend would have done at
// hold time so the node's confirm/cancel paths have a durable intent to resolve.
func seedHeldOrder(t *testing.T, st store.RealmStore, account domain.AccountID, approvalID string) domain.Order {
	t.Helper()
	ctx := context.Background()
	seedTestAccount(t, st, account)
	order, err := st.CreateOrder(ctx, domain.Order{
		Account:     account,
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := st.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: approvalID,
		Order:      order.ExternalID,
		Account:    order.Account,
		ParamsJSON: "{}",
		IssuedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(2 * time.Minute),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}
	return order
}

func eventTypes(t *testing.T, st store.RealmStore, order domain.ExternalID) []domain.OrderEventType {
	t.Helper()
	evs, err := st.ListOrderEvents(context.Background(), order)
	if err != nil {
		t.Fatalf("ListOrderEvents: %v", err)
	}
	types := make([]domain.OrderEventType, len(evs))
	for i, ev := range evs {
		types[i] = ev.Type
	}
	return types
}

func intentStillHeld(t *testing.T, st store.RealmStore, approvalID string) bool {
	t.Helper()
	open, err := st.ListOpenReservationIntents(context.Background())
	if err != nil {
		t.Fatalf("ListOpenReservationIntents: %v", err)
	}
	for _, i := range open {
		if i.ApprovalID == approvalID {
			return true
		}
	}
	return false
}

// TestConfirmHeld_AtomicSingleResolve proves the confirm path advances the order
// to committed with exactly one reservation_committed event, flips the intent,
// and resolves the native handle exactly once.
func TestConfirmHeld_AtomicSingleResolve(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")

	confirmed, _, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if err != nil {
		t.Fatalf("ConfirmHeld: %v", err)
	}
	if confirmed.Status != domain.OrderStatusCommitted {
		t.Fatalf("status = %q, want committed", confirmed.Status)
	}
	if intentStillHeld(t, st, "approval-1") {
		t.Fatal("intent still held, want committed")
	}
	types := eventTypes(t, st, order.ExternalID)
	if len(types) != 1 || types[0] != domain.OrderEventReservationCommitted {
		t.Fatalf("events = %+v, want exactly [reservation_committed]", types)
	}
	eng.resolveMu.Lock()
	commits := append([]string(nil), eng.commitHeldCalls...)
	eng.resolveMu.Unlock()
	if len(commits) != 1 || commits[0] != "approval-1" {
		t.Fatalf("commit calls = %+v, want exactly [approval-1]", commits)
	}
}

// TestConfirmHeld_StoreFailureNoPartialAndSingleNativeResolve proves that when
// the atomic ResolveOrderReservation fails after the native CommitHeld already
// succeeded, ConfirmHeld returns an error, leaves the order accepted and the
// intent held (nothing persisted), and does NOT re-resolve the native handle -
// finishResolve already ran inside the single CommitHeld.
func TestConfirmHeld_StoreFailureNoPartialAndSingleNativeResolve(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()

	real := newMemoryStore("node.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	resolveErr := errors.New("resolve boom")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failResolveRealm{RealmStore: r, err: resolveErr}
	})
	n := newTestNodeWithStore(t, st, eng)

	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	order := seedHeldOrder(t, realm, "acc-1", "approval-1")

	_, _, err = n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if err == nil {
		t.Fatal("ConfirmHeld: want error from failing ResolveOrderReservation")
	}
	if !errors.Is(err, resolveErr) {
		t.Fatalf("ConfirmHeld error = %v, want wrapping resolve boom", err)
	}

	// No partial persistence: status accepted, intent held, no events (read the
	// underlying real store, not the failing wrapper).
	detail, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusAccepted {
		t.Fatalf("status = %q, want accepted (nothing persisted)", detail.Order.Status)
	}
	if !intentStillHeld(t, realm, "approval-1") {
		t.Fatal("intent flipped despite store failure, want still held")
	}
	if len(detail.Events) != 0 {
		t.Fatalf("events = %+v, want none", detail.Events)
	}
	// Native handle resolved exactly once: the store failure must not trigger a
	// second CommitHeld.
	eng.resolveMu.Lock()
	commits := append([]string(nil), eng.commitHeldCalls...)
	eng.resolveMu.Unlock()
	if len(commits) != 1 {
		t.Fatalf("commit calls = %+v, want exactly one native resolve", commits)
	}
}

// TestConfirmHeld_PostEngineStoreFailureFatals proves the reservation-resolve
// write is a post-engine persistence step: the native CommitHeld already
// succeeded, so a failing atomic ResolveOrderReservation must fail-stop with the
// "resolve held confirmation" operation label, the real account id, and the
// wrapped cause.
func TestConfirmHeld_PostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()

	real := newMemoryStore("node.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	resolveErr := errors.New("resolve confirm boom")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failResolveRealm{RealmStore: r, err: resolveErr}
	})
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))

	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	order := seedHeldOrder(t, realm, "acc-1", "approval-1")

	_, _, err = n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("ConfirmHeld error = %v, want store failure", err)
	}
	// Native handle resolved before the store write failed.
	eng.resolveMu.Lock()
	commits := append([]string(nil), eng.commitHeldCalls...)
	eng.resolveMu.Unlock()
	if len(commits) != 1 {
		t.Fatalf("commit calls = %+v, want exactly one native resolve", commits)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine confirm persistence failure")
	}
	account, ok, err := realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="resolve held confirmation"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "resolve confirm boom") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestCancelHeld_PostEngineStoreFailureFatals mirrors the confirm fatal test for
// the cancel path: the native RollbackHeld already succeeded, so a failing
// atomic ResolveOrderReservation must fail-stop with the "resolve held
// cancellation" operation label, the real account id, and the wrapped cause.
func TestCancelHeld_PostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()

	real := newMemoryStore("node.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	resolveErr := errors.New("resolve cancel boom")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failResolveRealm{RealmStore: r, err: resolveErr}
	})
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))

	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	order := seedHeldOrder(t, realm, "acc-1", "approval-1")

	_, _, err = n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("CancelHeld error = %v, want store failure", err)
	}
	// Native handle rolled back before the store write failed.
	eng.resolveMu.Lock()
	rollbacks := append([]string(nil), eng.rollbackHeldCalls...)
	eng.resolveMu.Unlock()
	if len(rollbacks) != 1 {
		t.Fatalf("rollback calls = %+v, want exactly one native resolve", rollbacks)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine cancel persistence failure")
	}
	account, ok, err := realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="resolve held cancellation"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "resolve cancel boom") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestCancelHeld_AtomicConsistency proves the cancel path advances the order to
// cancelled with both reservation_rolled_back and cancelled events and flips the
// intent - all together.
func TestCancelHeld_AtomicConsistency(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")

	cancelled, _, err := n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if err != nil {
		t.Fatalf("CancelHeld: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
	if intentStillHeld(t, st, "approval-1") {
		t.Fatal("intent still held, want rolled_back")
	}
	types := eventTypes(t, st, order.ExternalID)
	if len(types) != 2 ||
		types[0] != domain.OrderEventReservationRolledBack ||
		types[1] != domain.OrderEventCancelled {
		t.Fatalf("events = %+v, want [reservation_rolled_back cancelled]", types)
	}
}

// TestConfirmHeld_AfterSweepReturnsConflict proves that a ConfirmHeld whose
// native handle is gone (post-restart) and whose intent has been swept to
// rolled_back by the TTL sweeper returns ErrConflict (409), not ErrNotFound
// (404). A genuinely unknown approval id still returns ErrNotFound.
func TestConfirmHeld_AfterSweepReturnsConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	// Native handle is gone: CommitHeld reports ErrNotFound.
	eng.commitErr = fmt.Errorf("engine: reservation %q: %w", "approval-swept", domain.ErrNotFound)

	n, st := newTestNode(t, eng)
	order := seedHeldOrder(t, st, "acc-1", "approval-swept")

	// Simulate TTL sweeper: flip intent to rolled_back without resolving the order.
	if err := st.SetReservationIntentState(
		ctx, "approval-swept", domain.ReservationIntentStateRolledBack,
	); err != nil {
		t.Fatalf("SetReservationIntentState: %v", err)
	}

	// Confirm after sweep must return ErrConflict.
	_, _, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-swept", testCaller, false)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ConfirmHeld after sweep: want ErrConflict, got %v", err)
	}

	// A genuinely unknown approval id must still return ErrNotFound.
	_, _, err = n.ConfirmHeld(ctx, order.ExternalID, "approval-unknown", testCaller, false)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ConfirmHeld unknown: want ErrNotFound, got %v", err)
	}
}

// TestCancelHeld_AfterSweepReturnsConflict proves that a CancelHeld whose
// native handle is gone (post-restart) and whose intent has been swept to
// rolled_back returns ErrConflict (409), not ErrNotFound (404).
func TestCancelHeld_AfterSweepReturnsConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	// Native handle is gone: RollbackHeld reports ErrNotFound.
	eng.rollbackErr = fmt.Errorf("engine: reservation %q: %w", "approval-swept", domain.ErrNotFound)

	n, st := newTestNode(t, eng)
	order := seedHeldOrder(t, st, "acc-1", "approval-swept")

	// Simulate TTL sweeper: flip intent to rolled_back without resolving the order.
	if err := st.SetReservationIntentState(
		ctx, "approval-swept", domain.ReservationIntentStateRolledBack,
	); err != nil {
		t.Fatalf("SetReservationIntentState: %v", err)
	}

	// Cancel after sweep must return ErrConflict.
	_, _, err := n.CancelHeld(ctx, order.ExternalID, "approval-swept", testCaller, false)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CancelHeld after sweep: want ErrConflict, got %v", err)
	}

	// A genuinely unknown approval id must still return ErrNotFound.
	_, _, err = n.CancelHeld(ctx, order.ExternalID, "approval-unknown", testCaller, false)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("CancelHeld unknown: want ErrNotFound, got %v", err)
	}
}

// TestCancelHeld_AfterFillConflictPreservesFilled proves a cancel that races a
// fill is rejected: the order was flipped to filled (via a direct settlement),
// so the cancel's AllowedFrom={accepted} guard returns ErrConflict, the filled
// status stands, and no cancel events are written.
func TestCancelHeld_AfterFillConflictPreservesFilled(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")

	// Simulate a fill landing before the cancel: settle the order to filled
	// directly through the store (the same atomic method the fill path uses).
	if err := st.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement (simulated fill): %v", err)
	}

	_, _, err := n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("CancelHeld after fill: want ErrTerminalOrder, got %v", err)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled preserved", detail.Order.Status)
	}
	types := eventTypes(t, st, order.ExternalID)
	if len(types) != 1 || types[0] != domain.OrderEventFill {
		t.Fatalf("events = %+v, want only the fill (no cancel events)", types)
	}
}

// TestConfirmHeld_AfterFillConflictPreservesFilled proves a confirm without
// force on an order a fill already moved to filled is rejected with
// ErrTerminalOrder, the filled status stands, and no confirm events are written.
// It mirrors TestCancelHeld_AfterFillConflictPreservesFilled for the confirm path.
func TestConfirmHeld_AfterFillConflictPreservesFilled(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")

	if err := st.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement (simulated fill): %v", err)
	}

	_, forcedBypass, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("ConfirmHeld after fill without force: want ErrTerminalOrder, got %v", err)
	}
	if forcedBypass {
		t.Fatal("forcedBypass = true on a rejected confirm, want false")
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled preserved", detail.Order.Status)
	}
	types := eventTypes(t, st, order.ExternalID)
	if len(types) != 1 || types[0] != domain.OrderEventFill {
		t.Fatalf("events = %+v, want only the fill (no confirm events)", types)
	}
}

func TestConfirmHeld_AfterFillForceCommits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")
	if err := st.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement (simulated fill): %v", err)
	}

	committed, forcedBypass, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, true)
	if err != nil {
		t.Fatalf("ConfirmHeld force: %v", err)
	}
	if !forcedBypass {
		t.Fatal("ConfirmHeld force over a filled order: forcedBypass = false, want true")
	}
	if committed.Status != domain.OrderStatusCommitted {
		t.Fatalf("status = %q, want committed", committed.Status)
	}
}

func TestCancelHeld_AfterFillForceCancels(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")
	if err := st.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement (simulated fill): %v", err)
	}

	cancelled, forcedBypass, err := n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, true)
	if err != nil {
		t.Fatalf("CancelHeld force: %v", err)
	}
	if !forcedBypass {
		t.Fatal("CancelHeld force over a filled order: forcedBypass = false, want true")
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
}

func TestConfirmHeld_ForcePartiallyFilledRoutesEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")
	if err := st.UpdateOrderStatus(
		ctx, order.ExternalID, domain.OrderStatusPartiallyFilled,
	); err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}

	confirmed, forcedBypass, err := n.ConfirmHeld(
		ctx, order.ExternalID, "approval-1", testCaller, true,
	)
	if err != nil {
		t.Fatalf("ConfirmHeld force partially_filled: %v", err)
	}
	if forcedBypass {
		t.Fatal("forcedBypass = true, want false for non-terminal status")
	}
	if confirmed.Status != domain.OrderStatusCommitted {
		t.Fatalf("status = %q, want committed", confirmed.Status)
	}
	if len(eng.commitHeldCalls) != 1 {
		t.Fatalf("commit calls = %+v, want one", eng.commitHeldCalls)
	}
}

func TestCancelHeld_ForceCommittedRoutesEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")
	if err := st.UpdateOrderStatus(
		ctx, order.ExternalID, domain.OrderStatusCommitted,
	); err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}

	cancelled, forcedBypass, err := n.CancelHeld(
		ctx, order.ExternalID, "approval-1", testCaller, true,
	)
	if err != nil {
		t.Fatalf("CancelHeld force committed: %v", err)
	}
	if forcedBypass {
		t.Fatal("forcedBypass = true, want false for non-terminal status")
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
	if len(eng.rollbackHeldCalls) != 1 {
		t.Fatalf("rollback calls = %+v, want one", eng.rollbackHeldCalls)
	}
}

// TestReconcileAfterCrash_LeavesFullyHeldOrFullyResolved proves the reconcile/
// retry contract across a simulated crash. A crash that aborts the resolve tx
// leaves intent=held + status=accepted (a clean retry point); a fresh ConfirmHeld
// (driven through the post-restart intent fallback, native handle gone) then ends
// fully committed. A crash AFTER commit leaves intent=committed + status=committed
// with no half state.
func TestReconcileAfterCrash_LeavesFullyHeldOrFullyResolved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// --- Crash BEFORE commit: nothing persisted, then retry completes. ---
	t.Run("crash_before_commit_then_retry_completes", func(t *testing.T) {
		eng := newFakeEngine()
		// The native handle is gone post-restart: CommitHeld reports NotFound so the
		// node drives the intent fallback + the same atomic resolve.
		eng.commitErr = fmt.Errorf("engine: reservation %q: %w", "approval-1", domain.ErrNotFound)

		n, st := newTestNode(t, eng)

		// Simulate the crash: the resolve tx never committed, so the durable state
		// is exactly intent=held + status=accepted (what seedHeldOrder leaves).
		order := seedHeldOrder(t, st, "acc-1", "approval-1")

		// Retry: a fresh confirm drives the fallback and the atomic resolve, ending
		// fully committed.
		confirmed, _, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
		if err != nil {
			t.Fatalf("retry ConfirmHeld: %v", err)
		}
		if confirmed.Status != domain.OrderStatusCommitted {
			t.Fatalf("status = %q, want committed after retry", confirmed.Status)
		}
		if intentStillHeld(t, st, "approval-1") {
			t.Fatal("intent still held after retry, want committed")
		}
		types := eventTypes(t, st, order.ExternalID)
		if len(types) != 1 || types[0] != domain.OrderEventReservationCommitted {
			t.Fatalf("events = %+v, want [reservation_committed]", types)
		}
	})

	// --- Crash AFTER commit: fully resolved, no half state. ---
	t.Run("crash_after_commit_is_fully_resolved", func(t *testing.T) {
		eng := newFakeEngine()
		n, st := newTestNode(t, eng)
		order := seedHeldOrder(t, st, "acc-1", "approval-1")

		if _, _, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false); err != nil {
			t.Fatalf("ConfirmHeld: %v", err)
		}
		// The committed state is durable: status committed, intent committed (absent
		// from the open/held set), one event. No accepted+held half-state remains.
		detail, err := st.GetOrder(ctx, order.ExternalID)
		if err != nil {
			t.Fatalf("GetOrder: %v", err)
		}
		if detail.Order.Status != domain.OrderStatusCommitted {
			t.Fatalf("status = %q, want committed", detail.Order.Status)
		}
		if intentStillHeld(t, st, "approval-1") {
			t.Fatal("intent still held after commit, want committed")
		}
		types := eventTypes(t, st, order.ExternalID)
		if len(types) != 1 || types[0] != domain.OrderEventReservationCommitted {
			t.Fatalf("events = %+v, want [reservation_committed]", types)
		}
	})
}
