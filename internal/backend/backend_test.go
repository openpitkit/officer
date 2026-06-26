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

package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/auth"
	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/businesscsv"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/node"
	"go.openpit.dev/officer/internal/store"
)

// fakeNode records the commands routed to it and returns canned data. It never
// touches an engine or a store, so the backend validation/routing tests run in
// isolation.
type fakeNode struct {
	version  string
	accounts []domain.Account
	limits   node.AccountLimits
	audit    []domain.AuditRow

	putRateLimitCalls      []domain.LimitRate
	putOrderSizeLimitCalls []domain.LimitOrderSize
	putPnlBoundsLimitCalls []domain.LimitPnlBounds
	deleteLimitCalls       []node.LimitTarget
	createCalls            []node.Key
	blockCalls             []blockCall
	positionSnapshots      []domain.Balance
	adjustmentExternalIDs  []domain.ExternalID
	adjustmentErr          error

	checkResult domain.CheckResult
	checkProbes []domain.OrderProbe

	mcpAccess        map[string]bool
	setMcpAccessCall []setMcpAccessCall
	userSettings     map[string]string
	mdInstances      []domain.MarketDataInstance
	mdInstruments    map[string][]domain.MarketDataInstrument
	mdQuotes         []domain.MarketDataQuote

	backupArchive        backup.Archive
	backupScope          backup.Scope
	backupCaller         domain.Caller
	backupErr            error
	restoreOpts          backup.RestoreOptions
	restoreSummary       backup.RestoreSummary
	restoreSink          marketdata.Sink
	restoreErr           error
	resetCaller          domain.Caller
	resetSink            marketdata.Sink
	resetErr             error
	orders               map[domain.ExternalID]domain.Order
	approvals            map[domain.ExternalID]domain.OrderApproval
	nextOrderSeq         byte
	holdResult           *engine.HoldResult
	immediateResult      *engine.ImmediateResult
	execReports          []domain.ExecutionReportInput
	submitErr            error
	confirmErr           error
	cancelErr            error
	getOrderErr          error
	reconciled           int
	holdCalls            []string
	confirmCalls         []string
	cancelCalls          []string
	persistApprovalCalls []domain.ExternalID
	auditCalls           []store.AuditEntry

	getAccountErr error

	// getOrderCount counts GetOrder invocations; the order-resolving flows must
	// fetch the stored order at most once per operation.
	getOrderCount atomic.Int64
}

// newOrderExternalID returns a deterministic distinct external id for a fake
// order. Machine records are addressed by an opaque external id, not an integer,
// so the fake mints a fresh 16-byte id per submit from a monotonic seed.
func (n *fakeNode) newOrderExternalID() domain.ExternalID {
	n.nextOrderSeq++
	var id domain.ExternalID
	id[0] = n.nextOrderSeq
	return id
}

type setMcpAccessCall struct {
	command string
	enabled bool
}

type blockCall struct {
	key     node.Key
	blocked bool
	reason  string
}

func (n *fakeNode) Health(context.Context) (node.Health, error) {
	return node.Health{}, nil
}
func (n *fakeNode) EngineVersion() string { return n.version }
func (n *fakeNode) Owns(node.Key) bool    { return true }

func (n *fakeNode) ListAccounts(context.Context) ([]domain.Account, error) {
	return n.accounts, nil
}

func (n *fakeNode) ExportBackup(
	_ context.Context,
	scope backup.Scope,
	caller domain.Caller,
) (backup.Archive, error) {
	n.backupScope = scope
	n.backupCaller = caller
	return n.backupArchive, n.backupErr
}

func (n *fakeNode) RestoreBackup(
	_ context.Context,
	_ backup.Archive,
	opts backup.RestoreOptions,
	_ domain.Caller,
) (backup.RestoreSummary, marketdata.Sink, error) {
	n.restoreOpts = opts
	summary := n.restoreSummary
	if summary.Applied == nil {
		summary = backup.NewSummary()
	}
	return summary, n.restoreSink, n.restoreErr
}

func (n *fakeNode) ResetDatabase(
	_ context.Context,
	caller domain.Caller,
) (marketdata.Sink, error) {
	n.resetCaller = caller
	return n.resetSink, n.resetErr
}

func (n *fakeNode) CreateAccount(
	_ context.Context, key node.Key, _ domain.Caller,
) (domain.Account, error) {
	n.createCalls = append(n.createCalls, key)
	account := domain.Account{Code: key.Account, EngineAccountID: 1}
	n.accounts = append(n.accounts, account)
	return account, nil
}

func (n *fakeNode) SetAccountBlocked(
	_ context.Context, key node.Key, blocked bool, reason string, _ domain.Caller,
) error {
	n.blockCalls = append(n.blockCalls, blockCall{key, blocked, reason})
	return nil
}

func (n *fakeNode) GetAccountState(
	_ context.Context, key node.Key,
) (domain.Account, node.AccountLimits, error) {
	if n.getAccountErr != nil {
		return domain.Account{}, node.AccountLimits{}, n.getAccountErr
	}
	return domain.Account{Code: key.Account}, n.limits, nil
}

func (n *fakeNode) ListLimits(
	context.Context, domain.AccountID,
) (node.AccountLimits, error) {
	return n.limits, nil
}

func (n *fakeNode) PutRateLimit(
	_ context.Context, limit domain.LimitRate, _ domain.Caller,
) (marketdata.Sink, error) {
	n.putRateLimitCalls = append(n.putRateLimitCalls, limit)
	return n.restoreSink, nil
}

func (n *fakeNode) PutOrderSizeLimit(
	_ context.Context, limit domain.LimitOrderSize, _ domain.Caller,
) (marketdata.Sink, error) {
	n.putOrderSizeLimitCalls = append(n.putOrderSizeLimitCalls, limit)
	return n.restoreSink, nil
}

func (n *fakeNode) PutPnlBoundsLimit(
	_ context.Context, limit domain.LimitPnlBounds, _ domain.Caller,
) (marketdata.Sink, error) {
	n.putPnlBoundsLimitCalls = append(n.putPnlBoundsLimitCalls, limit)
	return n.restoreSink, nil
}

func (n *fakeNode) DeleteLimit(
	_ context.Context, target node.LimitTarget, _ domain.Caller,
) (marketdata.Sink, error) {
	n.deleteLimitCalls = append(n.deleteLimitCalls, target)
	return n.restoreSink, nil
}

func (n *fakeNode) SetAccountGroup(
	context.Context, node.Key, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) SetAccountNotes(
	context.Context, node.Key, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) DeleteAccount(
	context.Context, node.Key, bool, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) CreateGroup(
	_ context.Context, group domain.AccountGroup, _ domain.Caller,
) (domain.AccountGroup, error) {
	group.EngineGroupID = 1
	return group, nil
}

func (n *fakeNode) ListGroups(context.Context) ([]domain.AccountGroup, error) {
	return nil, nil
}

func (n *fakeNode) GetGroup(
	context.Context, string,
) (domain.AccountGroup, []domain.Account, bool, error) {
	return domain.AccountGroup{}, nil, false, nil
}

func (n *fakeNode) SetGroupNotes(
	context.Context, string, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) SetGroupBlocked(
	context.Context, string, bool, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) DeleteGroup(context.Context, string, domain.Caller) error {
	return nil
}

func (n *fakeNode) ApplyBusinessCSVImport(
	_ context.Context,
	in store.BusinessCSVImport,
	_ domain.Caller,
) error {
	for _, row := range in.Accounts {
		if !row.Exists {
			n.createCalls = append(n.createCalls, node.Key{
				Account: row.Account.Code,
			})
		}
		found := false
		for i, account := range n.accounts {
			if account.Code == row.Account.Code {
				n.accounts[i] = row.Account
				found = true
				break
			}
		}
		if !found {
			n.accounts = append(n.accounts, row.Account)
		}
	}
	n.positionSnapshots = append(n.positionSnapshots, in.Balances...)
	return nil
}

func (n *fakeNode) ApplyAdjustment(
	_ context.Context, key node.Key, externalID domain.ExternalID,
	req domain.AdjustmentRequest, caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	n.adjustmentExternalIDs = append(n.adjustmentExternalIDs, externalID)
	if n.adjustmentErr != nil {
		return domain.AccountAdjustmentRecord{}, n.adjustmentErr
	}
	id := externalID
	if id.IsZero() {
		id = n.newOrderExternalID()
	}
	return domain.AccountAdjustmentRecord{
		ExternalID: id,
		Account:    key.Account,
		Source:     caller.Source,
		Principal:  caller.Principal,
		Request:    req,
		Asset:      req.Asset,
		Accepted:   &domain.AdjustmentOutcomeAccepted{},
	}, nil
}

func (n *fakeNode) ImportPositionSnapshot(
	_ context.Context, _ node.Key, snapshot domain.Balance, _ domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	n.positionSnapshots = append(n.positionSnapshots, snapshot)
	return domain.AccountAdjustmentRecord{
		Accepted: &domain.AdjustmentOutcomeAccepted{},
	}, nil
}

func (n *fakeNode) ListBalances(
	context.Context, domain.AccountID, string,
) ([]domain.Balance, error) {
	return nil, nil
}

func (n *fakeNode) GetBalance(
	context.Context, domain.AccountID, string,
) (domain.Balance, bool, error) {
	return domain.Balance{}, false, nil
}

func (n *fakeNode) ListAdjustments(
	context.Context, domain.AccountID, domain.Source, int,
) ([]domain.AccountAdjustmentRecord, error) {
	return nil, nil
}

// recordedOrderExternalID models the store's supplied-or-generated contract: a
// caller-supplied (non-zero) order external id is used verbatim, else a fresh id
// is minted. This keeps the fake faithful to recordSubmittedOrder, so a backend
// test can prove the id is threaded create-once and the returned id matches.
func (n *fakeNode) recordedOrderExternalID(o domain.Order) domain.ExternalID {
	if !o.ExternalID.IsZero() {
		return o.ExternalID
	}
	return n.newOrderExternalID()
}

func (n *fakeNode) SubmitOrder(
	_ context.Context, key node.Key, o domain.Order, _ domain.Caller,
) (domain.Order, error) {
	if n.submitErr != nil {
		return domain.Order{}, n.submitErr
	}
	order := o
	order.ExternalID = n.recordedOrderExternalID(o)
	order.Account = key.Account
	order.Status = domain.OrderStatusAccepted
	n.orders[order.ExternalID] = order
	return order, nil
}

func (n *fakeNode) SubmitHold(
	_ context.Context, key node.Key, o domain.Order, _ domain.Caller,
) (domain.Order, engine.HoldResult, error) {
	if n.submitErr != nil {
		return domain.Order{}, engine.HoldResult{}, n.submitErr
	}
	order := o
	order.ExternalID = n.recordedOrderExternalID(o)
	order.Account = key.Account
	if n.holdResult != nil {
		if !n.holdResult.Accepted {
			order.Status = domain.OrderStatusRejected
			return order, *n.holdResult, nil
		}
		order.Status = domain.OrderStatusAccepted
		order.Lock = n.holdResult.Lock
		n.orders[order.ExternalID] = order
		return order, *n.holdResult, nil
	}
	order.Status = domain.OrderStatusAccepted
	n.orders[order.ExternalID] = order
	result := engine.HoldResult{
		Accepted:            true,
		ApprovalID:          "approval-1",
		SettlementLockPrice: "100",
		EstimateSource:      domain.EstimateSourceLimit,
		ExpiresAt:           time.Now().UTC().Add(2 * time.Minute),
	}
	n.holdCalls = append(n.holdCalls, result.ApprovalID)
	return order, result, nil
}

func (n *fakeNode) SubmitImmediate(
	_ context.Context, key node.Key, o domain.Order, _ domain.Caller,
) (domain.Order, engine.ImmediateResult, error) {
	if n.submitErr != nil {
		return domain.Order{}, engine.ImmediateResult{}, n.submitErr
	}
	order := o
	order.ExternalID = n.recordedOrderExternalID(o)
	order.Account = key.Account
	if n.immediateResult != nil {
		if !n.immediateResult.Accepted {
			order.Status = domain.OrderStatusRejected
			return order, *n.immediateResult, nil
		}
		order.Status = domain.OrderStatusFilled
		order.Lock = n.immediateResult.Lock
		n.orders[order.ExternalID] = order
		return order, *n.immediateResult, nil
	}
	order.Status = domain.OrderStatusFilled
	n.orders[order.ExternalID] = order
	return order, engine.ImmediateResult{
		Accepted:            true,
		SettlementLockPrice: "100",
		FillQuantity:        o.AmountValue,
		EstimateSource:      domain.EstimateSourceLimit,
	}, nil
}

func (n *fakeNode) ConfirmHeld(
	_ context.Context, orderID domain.ExternalID,
	approvalID string, _ domain.Caller, _ bool,
) (domain.Order, error) {
	n.confirmCalls = append(n.confirmCalls, approvalID)
	if n.confirmErr != nil {
		return domain.Order{}, n.confirmErr
	}
	order := n.orders[orderID]
	order.Status = domain.OrderStatusCommitted
	n.orders[orderID] = order
	return order, nil
}

func (n *fakeNode) CancelHeld(
	_ context.Context, orderID domain.ExternalID,
	approvalID string, _ domain.Caller, _ bool,
) (domain.Order, error) {
	n.cancelCalls = append(n.cancelCalls, approvalID)
	if n.cancelErr != nil {
		return domain.Order{}, n.cancelErr
	}
	order := n.orders[orderID]
	order.Status = domain.OrderStatusCancelled
	n.orders[orderID] = order
	return order, nil
}

func (n *fakeNode) ReconcileOrphans(context.Context) (int, error) {
	return n.reconciled, nil
}

func (n *fakeNode) ApplyExecutionReport(
	_ context.Context, _ node.Key, in domain.ExecutionReportInput, _ domain.Caller,
) (engine.ExecutionReportResult, error) {
	n.execReports = append(n.execReports, in)
	return engine.ExecutionReportResult{}, nil
}

func (n *fakeNode) GetOrder(
	_ context.Context, id domain.ExternalID,
) (domain.OrderDetail, error) {
	n.getOrderCount.Add(1)
	if n.getOrderErr != nil {
		return domain.OrderDetail{}, n.getOrderErr
	}
	order, ok := n.orders[id]
	if !ok {
		return domain.OrderDetail{}, domain.ErrNotFound
	}
	detail := domain.OrderDetail{Order: order}
	if env, ok := n.approvals[id]; ok {
		stamped := env
		detail.Approval = &stamped
	}
	return detail, nil
}

func (n *fakeNode) PersistOrderApproval(
	_ context.Context, _ node.Key, orderID domain.ExternalID, env domain.OrderApproval,
) error {
	n.persistApprovalCalls = append(n.persistApprovalCalls, orderID)
	if n.approvals == nil {
		n.approvals = make(map[domain.ExternalID]domain.OrderApproval)
	}
	// Write-once: a retry or later write never clobbers an already-issued envelope.
	if _, ok := n.approvals[orderID]; !ok {
		if _, exists := n.orders[orderID]; exists {
			n.approvals[orderID] = env
		}
	}
	return nil
}

func (n *fakeNode) ListOrders(
	context.Context, domain.AccountID, domain.Source, int,
) ([]domain.Order, error) {
	return nil, nil
}

func (n *fakeNode) ListAllOrders(
	context.Context, domain.AccountID, domain.Source,
) ([]domain.Order, error) {
	return nil, nil
}

func (n *fakeNode) CountOrders(context.Context) (int, error) {
	return 0, nil
}

func (n *fakeNode) CountOrdersSince(
	context.Context, time.Time,
) (int, error) {
	return 0, nil
}

func (n *fakeNode) ListOrderEvents(
	context.Context, domain.ExternalID,
) ([]domain.OrderEvent, error) {
	return nil, nil
}

func (n *fakeNode) ListTrades(
	context.Context, domain.AccountID, domain.Source, int,
) ([]domain.Trade, error) {
	return nil, nil
}

func (n *fakeNode) ListAllTrades(
	context.Context, domain.AccountID, domain.Source,
) ([]domain.Trade, error) {
	return nil, nil
}

func (n *fakeNode) ListAudit(context.Context, int) ([]domain.AuditRow, error) {
	return n.audit, nil
}

func (n *fakeNode) ListAuditFiltered(
	_ context.Context, filter domain.AuditFilter, _ int,
) ([]domain.AuditRow, error) {
	actions := make(map[domain.AuditAction]struct{}, len(filter.Actions))
	for _, action := range filter.Actions {
		actions[action] = struct{}{}
	}
	out := make([]domain.AuditRow, 0, len(n.audit))
	for _, row := range n.audit {
		if filter.Account != "" && row.Account != filter.Account {
			continue
		}
		if filter.Source != "" && row.Source != filter.Source {
			continue
		}
		if len(actions) > 0 {
			if _, ok := actions[row.Action]; !ok {
				continue
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (n *fakeNode) AppendAudit(
	_ context.Context, entry store.AuditEntry, _ domain.Caller,
) error {
	n.auditCalls = append(n.auditCalls, entry)
	return nil
}

func (n *fakeNode) CheckOrder(
	_ context.Context, _ node.Key, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	n.checkProbes = append(n.checkProbes, probe)
	return n.checkResult, nil
}

func (n *fakeNode) ListMcpAccess(context.Context) (map[string]bool, error) {
	return n.mcpAccess, nil
}

func (n *fakeNode) SetMcpAccess(
	_ context.Context, command string, enabled bool, _ domain.Caller,
) error {
	n.setMcpAccessCall = append(n.setMcpAccessCall,
		setMcpAccessCall{command: command, enabled: enabled})
	if n.mcpAccess == nil {
		n.mcpAccess = make(map[string]bool)
	}
	n.mcpAccess[command] = enabled
	return nil
}

func (n *fakeNode) GetUserSetting(
	_ context.Context, _, key string,
) (string, bool, error) {
	if n.userSettings == nil {
		return "", false, nil
	}
	value, ok := n.userSettings[key]
	return value, ok, nil
}

func (n *fakeNode) SetUserSetting(
	_ context.Context, _, key, value string,
) error {
	if n.userSettings == nil {
		n.userSettings = make(map[string]string)
	}
	n.userSettings[key] = value
	return nil
}

func (n *fakeNode) ListMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	return n.mdInstances, nil
}

func (n *fakeNode) GetMarketDataInstance(
	_ context.Context, id domain.ExternalID,
) (domain.MarketDataInstance, bool, error) {
	for _, instance := range n.mdInstances {
		if instance.ExternalID == id {
			return instance, true, nil
		}
	}
	return domain.MarketDataInstance{}, false, nil
}

func (n *fakeNode) CreateMarketDataInstance(
	_ context.Context, instance domain.MarketDataInstance, _ domain.Caller,
) (domain.MarketDataInstance, error) {
	if instance.ExternalID.IsZero() {
		instance.ExternalID = n.newOrderExternalID()
	}
	n.mdInstances = append(n.mdInstances, instance)
	return instance, nil
}

func (n *fakeNode) SetMarketDataInstanceEnabled(
	_ context.Context, id domain.ExternalID, enabled bool, _ domain.Caller,
) error {
	for i := range n.mdInstances {
		if n.mdInstances[i].ExternalID == id {
			n.mdInstances[i].Enabled = enabled
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) UpdateMarketDataInstanceSettings(
	_ context.Context, id domain.ExternalID, label, credentials string, _ domain.Caller,
) error {
	for i := range n.mdInstances {
		if n.mdInstances[i].ExternalID == id {
			n.mdInstances[i].Label = label
			n.mdInstances[i].Credentials = credentials
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) DeleteMarketDataInstance(
	_ context.Context, id domain.ExternalID, _ bool, _ domain.Caller,
) error {
	for i := range n.mdInstances {
		if n.mdInstances[i].ExternalID == id {
			n.mdInstances = append(n.mdInstances[:i], n.mdInstances[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) ListMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	return n.mdInstruments[instance.String()], nil
}

func (n *fakeNode) UpsertMarketDataInstrument(
	_ context.Context, instrument domain.MarketDataInstrument, _ domain.Caller,
) error {
	if n.mdInstruments == nil {
		n.mdInstruments = make(map[string][]domain.MarketDataInstrument)
	}
	key := instrument.Instance.String()
	n.mdInstruments[key] = append(n.mdInstruments[key], instrument)
	return nil
}

func (n *fakeNode) SetMarketDataInstrumentEnabled(
	_ context.Context, instance domain.ExternalID, externalSymbol string, enabled bool, _ domain.Caller,
) error {
	key := instance.String()
	for i := range n.mdInstruments[key] {
		if n.mdInstruments[key][i].ExternalSymbol == externalSymbol {
			n.mdInstruments[key][i].Enabled = enabled
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) DeleteMarketDataInstrument(
	_ context.Context, instance domain.ExternalID, externalSymbol string, _ domain.Caller,
) error {
	key := instance.String()
	instruments := n.mdInstruments[key]
	for i := range instruments {
		if instruments[i].ExternalSymbol == externalSymbol {
			n.mdInstruments[key] = append(instruments[:i], instruments[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) ListMarketDataQuotes(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataQuote, error) {
	if instance.IsZero() {
		return n.mdQuotes, nil
	}
	out := make([]domain.MarketDataQuote, 0)
	for _, quote := range n.mdQuotes {
		if quote.Instance == instance {
			out = append(out, quote)
		}
	}
	return out, nil
}

func (n *fakeNode) Close() error { return nil }

// fakeRouter routes every key to the single fake node.
type fakeRouter struct {
	node     *fakeNode
	routeErr error

	// routeCount counts Route invocations; the order-resolving flows must route
	// exactly once per operation.
	routeCount atomic.Int64
}

func (r *fakeRouter) Route(node.Key) (node.Node, error) {
	r.routeCount.Add(1)
	return r.node, r.routeErr
}
func (r *fakeRouter) All() []node.Node { return []node.Node{r.node} }

type fakeMarketDataRuntime struct {
	statuses   map[string]marketdata.InstanceRuntimeStatus
	applied    map[string]marketdata.AppliedInstanceConfig
	intervals  map[string]time.Duration
	pushed     []domain.MarketDataInstrument
	restarts   int
	stops      int
	sink       marketdata.Sink
	restartErr error
	useSinkErr error
}

type backendTestSink struct{}

func (*backendTestSink) Push(marketdata.QuoteUpdate) error { return nil }

func (r *fakeMarketDataRuntime) InstanceStatuses() map[string]marketdata.InstanceRuntimeStatus {
	return r.statuses
}

func (r *fakeMarketDataRuntime) AppliedConfig() map[string]marketdata.AppliedInstanceConfig {
	return r.applied
}

func (r *fakeMarketDataRuntime) QuoteUpdateInterval(
	instanceID, external string,
) (time.Duration, bool) {
	d, ok := r.intervals[instanceID+"\x00"+external]
	return d, ok
}

func (r *fakeMarketDataRuntime) Restart() error {
	r.restarts++
	return r.restartErr
}

func (r *fakeMarketDataRuntime) Stop() {
	r.stops++
}

func (r *fakeMarketDataRuntime) UseSink(sink marketdata.Sink) error {
	if r.useSinkErr != nil {
		return r.useSinkErr
	}
	r.sink = sink
	return nil
}

func (r *fakeMarketDataRuntime) PushManual(
	_ string, instrument domain.MarketDataInstrument,
) {
	r.pushed = append(r.pushed, instrument)
}

func newTestService() (*backend.Service, *fakeNode) {
	fn := &fakeNode{orders: make(map[domain.ExternalID]domain.Order)}
	return backend.New(&fakeRouter{node: fn}, nil, nil), fn
}

func newTestServiceWithMarketDataRuntime(
	md backend.MarketDataRuntime,
) (*backend.Service, *fakeNode) {
	fn := &fakeNode{orders: make(map[domain.ExternalID]domain.Order)}
	return backend.New(&fakeRouter{node: fn}, md, nil), fn
}

// newTestServiceWithSigner builds a service with a fake signer for the approval
// flow tests.
func newTestServiceWithSigner(signer backend.SigningService) (*backend.Service, *fakeNode) {
	fn := &fakeNode{orders: make(map[domain.ExternalID]domain.Order)}
	return backend.New(&fakeRouter{node: fn}, nil, signer), fn
}

func TestService_ApplyExecutionReportTerminalRequiresForce(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	orderID := mdID("order-4")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusFilled,
	}
	report := domain.ExecutionReportInput{
		Order:        orderID,
		Account:      "acc-1",
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "1",
		FillPrice:    "100",
		Final:        true,
	}

	_, err := svc.ApplyExecutionReport(context.Background(), report)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("ApplyExecutionReport without force = %v, want terminal order", err)
	}
	if len(fn.execReports) != 0 {
		t.Fatalf("terminal preflight reached node: %+v", fn.execReports)
	}

	report.Force = true
	if _, err := svc.ApplyExecutionReport(context.Background(), report); err != nil {
		t.Fatalf("ApplyExecutionReport with force: %v", err)
	}
	if len(fn.execReports) != 1 || !fn.execReports[0].Force {
		t.Fatalf("forced report not forwarded: %+v", fn.execReports)
	}
}

func TestService_ExportBackupRoutesScopeCallerAndFilename(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	createdAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	fn.backupArchive = backup.NewArchive(
		createdAt,
		"test",
		backup.RealmLabel{Code: "default"},
		backup.Scope{All: true},
		backup.Data{},
	)
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionAccountsGroups},
	}
	caller := domain.Caller{
		Principal: "operator",
		Source:    domain.SourceAPI,
	}

	archive, filename, err := svc.ExportBackup(
		auth.ContextWithCaller(context.Background(), caller),
		scope,
	)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.Manifest.Source != "test" {
		t.Fatalf("archive source = %q, want test", archive.Manifest.Source)
	}
	if filename != backup.Filename(createdAt) {
		t.Fatalf("filename = %q, want %q", filename, backup.Filename(createdAt))
	}
	if !reflect.DeepEqual(fn.backupScope, scope) {
		t.Fatalf("backup scope = %+v, want %+v", fn.backupScope, scope)
	}
	if fn.backupCaller != caller {
		t.Fatalf("backup caller = %+v, want %+v", fn.backupCaller, caller)
	}
}

func TestService_ExportBackupRouteError(t *testing.T) {
	t.Parallel()
	routeErr := errors.New("route failed")
	svc := backend.New(&fakeRouter{routeErr: routeErr}, nil, nil)

	if _, _, err := svc.ExportBackup(
		context.Background(),
		backup.Scope{All: true},
	); !errors.Is(err, routeErr) {
		t.Fatalf("ExportBackup error = %v, want route error", err)
	}
}

func TestService_ExportBackupNodeError(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	exportErr := errors.New("export failed")
	fn.backupErr = exportErr

	if _, _, err := svc.ExportBackup(
		context.Background(),
		backup.Scope{All: true},
	); !errors.Is(err, exportErr) {
		t.Fatalf("ExportBackup error = %v, want export error", err)
	}
}

func TestService_ResetDatabaseRoutesCallerAndRestartsMarketData(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.resetSink = sink
	caller := domain.Caller{
		Principal: "operator",
		Source:    domain.SourcePanel,
	}

	if err := svc.ResetDatabase(
		auth.ContextWithCaller(context.Background(), caller),
	); err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if fn.resetCaller != caller {
		t.Fatalf("reset caller = %+v, want %+v", fn.resetCaller, caller)
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("market-data stops/restarts = %d/%d, want 1/1",
			md.stops, md.restarts)
	}
	if md.sink != sink {
		t.Fatalf("market-data sink = %T, want reset sink", md.sink)
	}
}

func TestService_ResetDatabaseRouteError(t *testing.T) {
	t.Parallel()
	routeErr := errors.New("route failed")
	svc := backend.New(&fakeRouter{routeErr: routeErr}, nil, nil)

	if err := svc.ResetDatabase(context.Background()); !errors.Is(err, routeErr) {
		t.Fatalf("ResetDatabase error = %v, want route error", err)
	}
}

func TestService_ResetDatabaseNodeError(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	resetErr := errors.New("reset failed")
	fn.resetErr = resetErr

	if err := svc.ResetDatabase(context.Background()); !errors.Is(err, resetErr) {
		t.Fatalf("ResetDatabase error = %v, want reset error", err)
	}
}

func TestService_RestoreBackupGeneralSettingsDoesNotStopMarketData(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, _ := newTestServiceWithMarketDataRuntime(md)
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 0 || md.restarts != 0 || md.sink != nil {
		t.Fatalf("market-data touched for non-runtime restore: %+v", md)
	}
}

func TestService_RestoreBackupRuntimeReconnectsMarketData(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	fn.restoreSummary = backup.RestoreSummary{
		Applied:         map[backup.Section]int{},
		Skipped:         map[backup.Section]int{},
		RestartRequired: true,
	}
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data lifecycle stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupRuntimeErrorRestartsReturnedSink(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	fn.restoreErr = errors.New("restore failed")
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want error")
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data recovery stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupRuntimeUseSinkErrorStillRestarts(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{useSinkErr: errors.New("use sink failed")}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.restoreSink = &backendTestSink{}
	fn.restoreSummary = backup.RestoreSummary{
		Applied:         map[backup.Section]int{},
		Skipped:         map[backup.Section]int{},
		RestartRequired: true,
	}
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want UseSink error")
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("market-data recovery stops=%d restarts=%d",
			md.stops, md.restarts)
	}
}

func TestService_RestoreBackupRuntimeRestartErrorIsReturned(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{restartErr: errors.New("restart failed")}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	fn.restoreSummary = backup.RestoreSummary{
		Applied:         map[backup.Section]int{},
		Skipped:         map[backup.Section]int{},
		RestartRequired: true,
	}
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want Restart error")
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data recovery stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_CreateAccountValidates(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	if _, err := svc.CreateAccount(ctx, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
	if len(fn.createCalls) != 0 {
		t.Fatalf("invalid input must not reach the node")
	}

	if _, err := svc.CreateAccount(ctx, "acc-1"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if len(fn.createCalls) != 1 {
		t.Fatalf("valid create must route to node")
	}
}

func TestService_CheckOrderForwardsWithoutFormatValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Officer no longer pre-validates the probe account/asset format; the engine
	// seam parses and rejects bad values downstream. Every probe now reaches the
	// node, including ones Officer used to reject (empty/interior-space asset).
	probes := []domain.OrderProbe{
		{Account: "", BaseAsset: "AAPL", QuoteAsset: "USD"},
		{Account: "acc-1", BaseAsset: "", QuoteAsset: "USD"},
		{Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "bad asset"},
	}
	for _, probe := range probes {
		svc, fn := newTestService()
		if _, err := svc.CheckOrder(ctx, probe); err != nil {
			t.Fatalf("CheckOrder %+v: unexpected error: %v", probe, err)
		}
		if len(fn.checkProbes) != 1 {
			t.Fatalf("probe must reach the node, got %d calls", len(fn.checkProbes))
		}
	}
}

func TestService_CheckOrderRoutesAndReturnsPass(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.checkResult = domain.CheckResult{Passed: true, WouldLockPrices: []string{"100"}}
	ctx := context.Background()

	probe := domain.OrderProbe{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
	}
	out, err := svc.CheckOrder(ctx, probe)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed || len(out.WouldLockPrices) != 1 || out.WouldLockPrices[0] != "100" {
		t.Fatalf("pass result not propagated: %+v", out)
	}
	if len(fn.checkProbes) != 1 || fn.checkProbes[0].Account != "acc-1" {
		t.Fatalf("valid probe must route to node once with the account preserved")
	}
}

func TestService_CheckOrderReturnsRejectAndBlock(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.checkResult = domain.CheckResult{
		Passed: false,
		Rejects: []domain.OrderReject{
			{Code: "rate_limit_exceeded", Scope: "account", Policy: "rate_limit"},
		},
		WouldBlock: &domain.ExecutionAccountBlock{
			Account: "acc-1", Code: "account_blocked", Reason: "kill switch",
		},
	}
	ctx := context.Background()

	out, err := svc.CheckOrder(ctx, domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	})
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed {
		t.Fatalf("want passed=false")
	}
	if len(out.Rejects) != 1 || out.Rejects[0].Code != "rate_limit_exceeded" {
		t.Fatalf("reject not propagated: %+v", out.Rejects)
	}
	if out.WouldBlock == nil || out.WouldBlock.Account != "acc-1" {
		t.Fatalf("would-block not propagated: %+v", out.WouldBlock)
	}
}

func TestService_PutLimitValidatesBeforeRouting(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// account scope on order_size_limit is not allowed - validation must reject
	// before the node is touched.
	bad := domain.LimitOrderSize{
		Scope:       domain.ScopeAccount,
		Account:     "acc-1",
		MaxQuantity: "1",
	}
	if err := svc.PutOrderSizeLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.putOrderSizeLimitCalls) != 0 {
		t.Fatalf("invalid limit must not reach the node")
	}
}

func TestService_PutLimitAcceptsNonExistentAccount(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// Account "acc-new" does not exist in the fake node (getAccountErr is not set,
	// but no account record exists either). PutRateLimit must succeed regardless: a
	// policy rule may be created before the account is ever registered.
	limit := domain.LimitRate{
		Scope:     domain.ScopeAccountAsset,
		Account:   "acc-new",
		Asset:     "AAPL",
		MaxOrders: 100,
		Window:    time.Second,
	}
	if err := svc.PutRateLimit(ctx, limit); err != nil {
		t.Fatalf("PutRateLimit for non-existent account: %v", err)
	}
	if len(fn.putRateLimitCalls) != 1 {
		t.Fatalf("want 1 PutRateLimit call, got %d", len(fn.putRateLimitCalls))
	}
}

func TestService_PutLimitForwardsTypedBarrier(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	limit := domain.LimitRate{
		Scope:     domain.ScopeBroker,
		MaxOrders: 100,
		Window:    time.Second,
	}
	if err := svc.PutRateLimit(ctx, limit); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if len(fn.putRateLimitCalls) != 1 {
		t.Fatalf("want one PutRateLimit call")
	}
	if fn.putRateLimitCalls[0].Scope != domain.ScopeBroker ||
		fn.putRateLimitCalls[0].MaxOrders != 100 {
		t.Fatalf("barrier not forwarded: %+v", fn.putRateLimitCalls[0])
	}
}

func TestService_PutLimitReconnectsMarketDataOnRebuild(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	ctx := context.Background()

	limit := domain.LimitRate{
		Scope:     domain.ScopeBroker,
		MaxOrders: 100,
		Window:    time.Second,
	}
	if err := svc.PutRateLimit(ctx, limit); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data reconnect = stops:%d restarts:%d sink:%T",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_DeleteLimitValidatesTarget(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// broker scope is not allowed for pnl_bounds; target validation must reject.
	bad := node.LimitTarget{
		Policy: domain.PolicyPnlBoundsKillSwitch,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.deleteLimitCalls) != 0 {
		t.Fatalf("invalid target must not reach the node")
	}

	good := node.LimitTarget{
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, good); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if len(fn.deleteLimitCalls) != 1 {
		t.Fatalf("valid delete must route to node")
	}
	if fn.deleteLimitCalls[0].Policy != domain.PolicyRateLimit {
		t.Fatalf("delete target not forwarded: %+v", fn.deleteLimitCalls[0])
	}
}

func TestService_DeleteLimitReconnectsMarketDataOnRebuild(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	ctx := context.Background()

	target := node.LimitTarget{
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, target); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data reconnect = stops:%d restarts:%d sink:%T",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_BlockAccountValidates(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	if err := svc.BlockAccount(ctx, "", "risk"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
	if len(fn.blockCalls) != 0 {
		t.Fatalf("invalid id must not reach the node")
	}

	if err := svc.BlockAccount(ctx, "acc-1", "risk"); err != nil {
		t.Fatalf("BlockAccount: %v", err)
	}
	if len(fn.blockCalls) != 1 || !fn.blockCalls[0].blocked ||
		fn.blockCalls[0].reason != "risk" {
		t.Fatalf("block not routed correctly: %+v", fn.blockCalls)
	}
}

func TestService_AggregatesReads(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.accounts = []domain.Account{{Code: "a"}, {Code: "b"}}
	fn.limits = node.AccountLimits{
		RateLimits: []domain.LimitRate{{Scope: domain.ScopeBroker}},
	}
	fn.audit = []domain.AuditRow{{ExternalID: domain.ExternalID{1}}}
	ctx := context.Background()

	accounts, err := svc.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("ListAccounts: %v len=%d", err, len(accounts))
	}
	limits, err := svc.ListLimits(ctx, "")
	if err != nil || len(limits.RateLimits) != 1 {
		t.Fatalf("ListLimits: %v rate=%d", err, len(limits.RateLimits))
	}
	rows, err := svc.ListAudit(ctx, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListAudit: %v len=%d", err, len(rows))
	}
}

func TestService_ListMcpAccessMergesDefaults(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	// Override one default-on command off and one default-off command on.
	fn.mcpAccess = map[string]bool{"health": false, "set_limit": true}
	ctx := context.Background()

	commands, err := svc.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	got := make(map[string]bool, len(commands))
	for _, c := range commands {
		got[c.Command.Name] = c.Enabled
	}
	if got["health"] != false {
		t.Errorf("health override not applied: %v", got["health"])
	}
	if got["set_limit"] != true {
		t.Errorf("set_limit override not applied: %v", got["set_limit"])
	}
	if got["get_limits"] != true {
		t.Errorf("get_limits should default on, got %v", got["get_limits"])
	}
	if got["arm_killswitch"] != false {
		t.Errorf("arm_killswitch should default off, got %v", got["arm_killswitch"])
	}
}

func TestService_SetMcpAccessValidatesCommand(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	if err := svc.SetMcpAccess(ctx, "not_a_command", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown command, got %v", err)
	}
	if len(fn.setMcpAccessCall) != 0 {
		t.Fatalf("unknown command must not reach the node")
	}

	if err := svc.SetMcpAccess(ctx, "health", false); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	if len(fn.setMcpAccessCall) != 1 || fn.setMcpAccessCall[0].command != "health" ||
		fn.setMcpAccessCall[0].enabled != false {
		t.Fatalf("valid set must route to node: %+v", fn.setMcpAccessCall)
	}
}

func TestService_ListMarketDataBuildsStatus(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	now := time.Now().UTC()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
		{ExternalID: mdID("mock-2"), Provider: domain.MarketDataProviderMock, Enabled: false},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
		mdID("mock-2").String(): {
			{
				Instance:       mdID("mock-2"),
				ExternalSymbol: "TSLA",
				BaseAsset:      "TSLA",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}
	fn.mdQuotes = []domain.MarketDataQuote{
		{
			Instance:       mdID("mock-1"),
			ExternalSymbol: "AAPL",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
			Mark:           "100",
			AsOf:           now,
			ReceivedAt:     now,
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if status.FreshnessSeconds != int(backend.MarketDataFreshnessTTL.Seconds()) {
		t.Fatalf("freshness seconds mismatch: %+v", status)
	}
	for _, want := range []string{
		domain.MarketDataProviderIB,
		domain.MarketDataProviderBinance,
		domain.MarketDataProviderKraken,
		domain.MarketDataProviderCoinbase,
		domain.MarketDataProviderAlpaca,
		domain.MarketDataProviderOKX,
		domain.MarketDataProviderBybit,
		domain.MarketDataProviderOANDA,
		domain.MarketDataProviderFinnhub,
		domain.MarketDataProviderBYO,
		domain.MarketDataProviderMock,
	} {
		if !containsProviderType(status.Providers, want) {
			t.Fatalf("providers = %+v, want %s", status.Providers, want)
		}
	}
	if len(status.Instances) != 2 || len(status.Instances[0].Instruments) != 2 ||
		len(status.Instances[1].Instruments) != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
	first := status.Instances[0].Instruments[0]
	second := status.Instances[0].Instruments[1]
	disabledSource := status.Instances[1].Instruments[0]
	if first.Quote == nil || first.Stale {
		t.Fatalf("fresh quoted instrument should not be stale: %+v", first)
	}
	if !second.Stale {
		t.Fatalf("enabled instrument without quote should be stale: %+v", second)
	}
	if disabledSource.Stale {
		t.Fatalf("disabled source instrument should not be stale: %+v", disabledSource)
	}
}

func TestMarketDataFreshnessTTLContract(t *testing.T) {
	t.Parallel()

	if marketdata.FreshnessTTL != 70*time.Second {
		t.Fatalf("marketdata.FreshnessTTL = %s, want 70s", marketdata.FreshnessTTL)
	}
	if engine.MarketDataFreshnessTTL != marketdata.FreshnessTTL {
		t.Fatalf(
			"engine.MarketDataFreshnessTTL = %s, want %s",
			engine.MarketDataFreshnessTTL,
			marketdata.FreshnessTTL,
		)
	}
	if backend.MarketDataFreshnessTTL != marketdata.FreshnessTTL {
		t.Fatalf(
			"backend.MarketDataFreshnessTTL = %s, want %s",
			backend.MarketDataFreshnessTTL,
			marketdata.FreshnessTTL,
		)
	}
}

func TestService_ListMarketDataFlagsStaleQuote(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	now := time.Now().UTC()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}
	fn.mdQuotes = []domain.MarketDataQuote{
		{
			Instance:       mdID("mock-1"),
			ExternalSymbol: "AAPL",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
			Mark:           "298.01",
			AsOf:           now.Add(-backend.MarketDataFreshnessTTL - time.Second),
			ReceivedAt:     now,
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	got := status.Instances[0].Instruments[0]
	if !got.Stale {
		t.Fatalf("instrument should be stale: %+v", got)
	}
	// Quote is kept even when stale so the last known price remains visible.
	if got.Quote == nil {
		t.Fatalf("stale quote must still be present: %+v", got)
	}
	if got.Quote.Mark != "298.01" {
		t.Fatalf("stale quote has unexpected mark: %+v", got.Quote)
	}
}

func TestService_ListMarketDataDetectsRestartRequired(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			mdID("mock-1").String(): {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			mdID("mock-1").String(): {
				Provider: domain.MarketDataProviderMock,
				Subscriptions: []marketdata.Subscription{
					{External: "AAPL", Base: "AAPL", Quote: "USD"},
				},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if !status.RestartRequired {
		t.Fatal("RestartRequired = false, want true for unapplied instrument")
	}
}

func TestService_ListMarketDataSurfacesUpdateInterval(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			mdID("mock-1").String(): {State: marketdata.StateOK},
		},
		intervals: map[string]time.Duration{
			mdID("mock-1").String() + "\x00AAPL": 12 * time.Second,
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	instruments := status.Instances[0].Instruments
	withInterval := instruments[0]
	if withInterval.UpdateInterval == nil {
		t.Fatalf("AAPL interval should be known: %+v", withInterval)
	}
	if *withInterval.UpdateInterval != 12*time.Second {
		t.Fatalf("AAPL interval = %v, want 12s", *withInterval.UpdateInterval)
	}
	withoutInterval := instruments[1]
	if withoutInterval.UpdateInterval != nil {
		t.Fatalf("MSFT interval should be unknown: %+v", withoutInterval)
	}
}

func TestService_CreateMarketDataInstanceGeneratesExternalIDAndDefaultLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	// With no supplied id the store mints one and returns it.
	created, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBinance,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("created instances = %d, want 1", len(fn.mdInstances))
	}
	got := fn.mdInstances[0]
	if got.ExternalID.IsZero() {
		t.Fatalf("instance external id is zero, want a generated id")
	}
	if created.ExternalID != got.ExternalID {
		t.Fatalf("returned external id = %q, want stored %q", created.ExternalID, got.ExternalID)
	}
	if got.Provider != domain.MarketDataProviderBinance || got.Label != "Binance" || !got.Enabled {
		t.Fatalf("created instance = %+v, want Binance default label and enabled", got)
	}
}

func TestService_CreateMarketDataInstanceHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	supplied := mdID("operator-supplied")
	created, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		ExternalID: supplied,
		Provider:   domain.MarketDataProviderBinance,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("created instances = %d, want 1", len(fn.mdInstances))
	}
	if fn.mdInstances[0].ExternalID != supplied {
		t.Fatalf("stored external id = %q, want supplied %q", fn.mdInstances[0].ExternalID, supplied)
	}
	if created.ExternalID != supplied {
		t.Fatalf("returned external id = %q, want supplied %q", created.ExternalID, supplied)
	}
}

func TestService_CreateMarketDataInstancePassesCredentials(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	_, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Provider: domain.MarketDataProviderAlpaca,
		Credentials: `{
			"apiKey": "key",
			"apiSecret": "secret"
		}`,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("created instances = %d, want 1", len(fn.mdInstances))
	}
	if fn.mdInstances[0].Credentials == "" {
		t.Fatal("Credentials not persisted")
	}
}

func TestService_CreateMarketDataInstanceRejectsInvalidCredentials(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	_, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Provider: domain.MarketDataProviderAlpaca,
		Credentials: `{
			"apiKey": "key"
		}`,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateMarketDataInstance error = %v, want ErrInvalid", err)
	}
	if len(fn.mdInstances) != 0 {
		t.Fatalf("invalid create changed instances: %+v", fn.mdInstances)
	}
}

func TestService_CreateMarketDataInstanceRejectsDuplicateLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("bn-1"), Provider: domain.MarketDataProviderBinance, Label: "Binance"},
	}

	_, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Provider: domain.MarketDataProviderMock,
		Label:    " binance ",
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateMarketDataInstance error = %v, want ErrAlreadyExists", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("duplicate create changed instances: %+v", fn.mdInstances)
	}
}

func TestService_UpdateMarketDataInstanceSettingsMergesBlankSecret(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{
			ExternalID:  mdID("alpaca-1"),
			Provider:    domain.MarketDataProviderAlpaca,
			Label:       "Alpaca",
			Credentials: `{"apiKey":"old-key","apiSecret":"old-secret"}`,
		},
	}

	err := svc.UpdateMarketDataInstanceSettings(
		context.Background(),
		mdID("alpaca-1").String(),
		"Alpaca live",
		`{"apiKey":"new-key","apiSecret":""}`,
	)
	if err != nil {
		t.Fatalf("UpdateMarketDataInstanceSettings: %v", err)
	}
	got := fn.mdInstances[0]
	if got.Label != "Alpaca live" {
		t.Fatalf("Label = %q, want updated label", got.Label)
	}
	var credentials map[string]string
	if err := json.Unmarshal([]byte(got.Credentials), &credentials); err != nil {
		t.Fatalf("credentials JSON: %v", err)
	}
	if credentials["apiKey"] != "new-key" || credentials["apiSecret"] != "old-secret" {
		t.Fatalf("credentials = %+v, want new key and preserved secret", credentials)
	}
}

func TestService_UpdateMarketDataInstanceSettingsRejectsDuplicateLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("a"), Provider: domain.MarketDataProviderBinance, Label: "Primary"},
		{ExternalID: mdID("b"), Provider: domain.MarketDataProviderBinance, Label: "Backup"},
	}

	err := svc.UpdateMarketDataInstanceSettings(
		context.Background(), mdID("b").String(), "primary", "",
	)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("UpdateMarketDataInstanceSettings error = %v, want ErrAlreadyExists", err)
	}
	if fn.mdInstances[1].Label != "Backup" {
		t.Fatalf("duplicate update changed instance: %+v", fn.mdInstances[1])
	}
}

func TestService_ListMarketDataManualPriceDoesNotRequireRestart(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			mdID("byo-1").String(): {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			mdID("byo-1").String(): {
				Provider: domain.MarketDataProviderBYO,
				Subscriptions: []marketdata.Subscription{
					{External: "USDT/USD", Base: "USDT", Quote: "USD"},
				},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("byo-1"), Provider: domain.MarketDataProviderBYO, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("byo-1").String(): {
			{
				Instance:       mdID("byo-1"),
				ExternalSymbol: "USDT/USD",
				BaseAsset:      "USDT",
				QuoteAsset:     "USD",
				ManualPrice:    "0.9998",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if status.RestartRequired {
		t.Fatal("RestartRequired = true, want false for manual price change")
	}
}

func TestService_ListMarketDataSurfacesVerifyCapability(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
		{ExternalID: mdID("bn-1"), Provider: domain.MarketDataProviderBinance, Enabled: false},
		{ExternalID: mdID("ib-1"), Provider: domain.MarketDataProviderIB, Enabled: false},
		{ExternalID: mdID("kraken-1"), Provider: domain.MarketDataProviderKraken, Enabled: false},
		{ExternalID: mdID("coinbase-1"), Provider: domain.MarketDataProviderCoinbase, Enabled: false},
		{ExternalID: mdID("okx-1"), Provider: domain.MarketDataProviderOKX, Enabled: false},
		{ExternalID: mdID("bybit-1"), Provider: domain.MarketDataProviderBybit, Enabled: false},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	byID := make(map[string]backend.MarketDataInstanceStatus, len(status.Instances))
	for _, instance := range status.Instances {
		byID[instance.Instance.ExternalID.String()] = instance
	}
	if byID[mdID("mock-1").String()].VerifiesSymbols {
		t.Fatalf("mock instance VerifiesSymbols = true, want false")
	}
	if byID[mdID("ib-1").String()].VerifiesSymbols {
		t.Fatalf("ib instance VerifiesSymbols = true, want false")
	}
	// Binance is verify-capable even though the instance is disabled: the flag is
	// provider-derived, not runtime-derived.
	if !byID[mdID("bn-1").String()].VerifiesSymbols {
		t.Fatalf("binance instance VerifiesSymbols = false, want true")
	}
	for _, label := range []string{"kraken-1", "coinbase-1", "okx-1", "bybit-1"} {
		if !byID[mdID(label).String()].VerifiesSymbols {
			t.Fatalf("%s VerifiesSymbols = false, want true", label)
		}
	}
}

func TestService_VerifyMarketDataSymbolUnsupported(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}

	got, err := svc.VerifyMarketDataSymbol(context.Background(), mdID("mock-1").String(), "AAPL")
	if err != nil {
		t.Fatalf("VerifyMarketDataSymbol: %v", err)
	}
	if got.Supported || got.Exists || got.Suggestion != "" {
		t.Fatalf("verification = %+v, want unsupported zero result", got)
	}
}

func TestService_VerifyMarketDataSymbolUnknownInstance(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService()

	_, err := svc.VerifyMarketDataSymbol(context.Background(), mdID("missing").String(), "AAPL")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("VerifyMarketDataSymbol(missing) err = %v, want ErrNotFound", err)
	}
}

func TestService_SearchMarketDataSymbolsUnsupported(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}

	got, err := svc.SearchMarketDataSymbols(
		context.Background(), mdID("mock-1").String(),
		backend.MarketDataSymbolSearchInput{Query: "AAPL"},
	)
	if err != nil {
		t.Fatalf("SearchMarketDataSymbols: %v", err)
	}
	if got.Supported || len(got.Matches) != 0 {
		t.Fatalf("search = %+v, want unsupported empty result", got)
	}
}

func TestService_SearchMarketDataSymbolsNotFound(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService()

	_, err := svc.SearchMarketDataSymbols(
		context.Background(), mdID("missing").String(),
		backend.MarketDataSymbolSearchInput{Query: "AAPL"},
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SearchMarketDataSymbols(missing) err = %v, want ErrNotFound", err)
	}
}

func sampleAdjustmentRequest() domain.AdjustmentRequest {
	return domain.AdjustmentRequest{
		Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "100",
		},
	}
}

// TestService_ApplyAdjustmentHonorsSuppliedExternalID covers the user-create
// adjustment path: a caller-supplied external id is threaded onto the record and
// returned verbatim.
func TestService_ApplyAdjustmentHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	supplied := mdID("supplied-adj-id")
	rec, err := svc.ApplyAdjustment(
		context.Background(), "acc-1", supplied, sampleAdjustmentRequest())
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if len(fn.adjustmentExternalIDs) != 1 || fn.adjustmentExternalIDs[0] != supplied {
		t.Fatalf("threaded ids = %+v, want [%q]", fn.adjustmentExternalIDs, supplied)
	}
	if rec.ExternalID != supplied {
		t.Fatalf("returned record id = %q, want supplied %q", rec.ExternalID, supplied)
	}
}

// TestService_ApplyAdjustmentGeneratesExternalIDWhenAbsent covers the absent-id
// path: a zero id is forwarded so the store mints one.
func TestService_ApplyAdjustmentGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	rec, err := svc.ApplyAdjustment(
		context.Background(), "acc-1", domain.ExternalID{}, sampleAdjustmentRequest())
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if len(fn.adjustmentExternalIDs) != 1 || !fn.adjustmentExternalIDs[0].IsZero() {
		t.Fatalf("threaded ids = %+v, want one zero id", fn.adjustmentExternalIDs)
	}
	if rec.ExternalID.IsZero() {
		t.Fatalf("returned record id is zero, want a generated id")
	}
}

// TestService_ApplyAdjustmentDuplicateSuppliedIDConflicts covers the conflict
// propagation: a duplicate supplied id surfaces domain.ErrAlreadyExists
// unchanged from the store.
func TestService_ApplyAdjustmentDuplicateSuppliedIDConflicts(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.adjustmentErr = fmt.Errorf("append adjustment: %w", domain.ErrAlreadyExists)

	_, err := svc.ApplyAdjustment(
		context.Background(), "acc-1", mdID("dup-adj-id"), sampleAdjustmentRequest())
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

// mdID derives a deterministic, distinct external id from a short label so a
// test can address a market-data instance by a stable handle. Market-data
// instances are dictionary rows addressed by their opaque external id, not a
// human string, so fixtures mint a real ExternalID rather than a synthetic id.
func mdID(label string) domain.ExternalID {
	var id domain.ExternalID
	copy(id[:], label)
	return id
}

func containsProviderType(providers []backend.MarketDataProvider, want string) bool {
	for _, provider := range providers {
		if provider.Type == want {
			return true
		}
	}
	return false
}

// TestService_OrderFlowsRouteOnceFetchAtMostOnce locks the resolve-once
// invariant: every order-resolving flow must route exactly once and fetch the
// stored order at most once per operation. It instruments the fake router/node
// call counters so a regression to double routing or double fetching fails here.
func TestService_OrderFlowsRouteOnceFetchAtMostOnce(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// run drives one operation. mustHold and any other multi-step setup happen
		// before the measured operation; the case calls reset() to zero the counters
		// just before the operation under test so the assertion covers only it. It
		// returns the number of order fetches expected (submit fetches none,
		// confirm/cancel fetch once).
		run func(t *testing.T, svc *backend.Service, fn *fakeNode, reset func()) int
	}{
		{
			name: "submit hold",
			run: func(t *testing.T, svc *backend.Service, _ *fakeNode, reset func()) int {
				t.Helper()
				reset()
				if _, err := svc.SubmitOrderToken(
					context.Background(), sampleOrder(), backend.SubmitModeHold,
				); err != nil {
					t.Fatalf("SubmitOrderToken hold: %v", err)
				}
				return 0
			},
		},
		{
			name: "submit immediate",
			run: func(t *testing.T, svc *backend.Service, _ *fakeNode, reset func()) int {
				t.Helper()
				reset()
				if _, err := svc.SubmitOrderToken(
					context.Background(), sampleOrder(), backend.SubmitModeImmediate,
				); err != nil {
					t.Fatalf("SubmitOrderToken immediate: %v", err)
				}
				return 0
			},
		},
		{
			name: "confirm accepted",
			run: func(t *testing.T, svc *backend.Service, _ *fakeNode, reset func()) int {
				t.Helper()
				tok := mustHold(t, svc)
				reset()
				if _, err := svc.ConfirmExecution(
					context.Background(), tok.OrderExternalID, tok.Token, false,
				); err != nil {
					t.Fatalf("ConfirmExecution: %v", err)
				}
				return 1
			},
		},
		{
			name: "confirm already committed idempotent",
			run: func(t *testing.T, svc *backend.Service, fn *fakeNode, reset func()) int {
				t.Helper()
				tok := mustHold(t, svc)
				if _, err := svc.ConfirmExecution(
					context.Background(), tok.OrderExternalID, tok.Token, false,
				); err != nil {
					t.Fatalf("first confirm: %v", err)
				}
				reset()
				fn.confirmErr = domain.ErrConflict
				if _, err := svc.ConfirmExecution(
					context.Background(), tok.OrderExternalID, tok.Token, false,
				); err != nil {
					t.Fatalf("idempotent confirm: %v", err)
				}
				return 1
			},
		},
		{
			name: "confirm conflict",
			run: func(t *testing.T, svc *backend.Service, fn *fakeNode, reset func()) int {
				t.Helper()
				tok := mustHold(t, svc)
				if _, err := svc.CancelOrder(
					context.Background(), tok.OrderExternalID, tok.Token, "operator", false,
				); err != nil {
					t.Fatalf("cancel setup: %v", err)
				}
				reset()
				fn.confirmErr = domain.ErrConflict
				if _, err := svc.ConfirmExecution(
					context.Background(), tok.OrderExternalID, tok.Token, false,
				); !errors.Is(err, domain.ErrTerminalOrder) {
					t.Fatalf("confirm after cancel = %v, want terminal order", err)
				}
				return 1
			},
		},
		{
			name: "cancel accepted",
			run: func(t *testing.T, svc *backend.Service, _ *fakeNode, reset func()) int {
				t.Helper()
				tok := mustHold(t, svc)
				reset()
				if _, err := svc.CancelOrder(
					context.Background(), tok.OrderExternalID, tok.Token, "stale price", false,
				); err != nil {
					t.Fatalf("CancelOrder: %v", err)
				}
				return 1
			},
		},
		{
			name: "cancel conflict",
			run: func(t *testing.T, svc *backend.Service, fn *fakeNode, reset func()) int {
				t.Helper()
				tok := mustHold(t, svc)
				if _, err := svc.ConfirmExecution(
					context.Background(), tok.OrderExternalID, tok.Token, false,
				); err != nil {
					t.Fatalf("confirm setup: %v", err)
				}
				reset()
				if _, err := svc.CancelOrder(
					context.Background(), tok.OrderExternalID, tok.Token, "too late", false,
				); !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("cancel after confirm = %v, want conflict", err)
				}
				return 1
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			router := &fakeRouter{node: &fakeNode{orders: make(map[domain.ExternalID]domain.Order)}}
			svc := backend.New(router, nil, &fakeSigner{})
			fn := router.node
			reset := func() {
				router.routeCount.Store(0)
				fn.getOrderCount.Store(0)
			}

			wantFetches := tc.run(t, svc, fn, reset)

			if got := router.routeCount.Load(); got != 1 {
				t.Fatalf("Route calls = %d, want exactly 1", got)
			}
			if got := fn.getOrderCount.Load(); got > int64(wantFetches) {
				t.Fatalf("GetOrder calls = %d, want at most %d", got, wantFetches)
			}
		})
	}
}

func TestService_BusinessCSVImportStopKeepsPreviousRowsAndAudits(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.accounts = []domain.Account{{
		Code: "acc-existing",
	}}

	body := []byte(
		"code,title,group_code,notes,blocked,block_reason\n" +
			"acc-new,,desk-a,new note,false,\n" +
			"acc-existing,,desk-a,old note,false,\n" +
			"acc-after,,desk-a,after,false,\n",
	)
	result, err := svc.ImportBusinessCSV(context.Background(),
		backend.BusinessCSVImportRequest{
			Entity:         businesscsv.EntityAccounts,
			Delimiter:      businesscsv.DelimiterComma,
			Filename:       "accounts.csv",
			Payload:        body,
			ConflictPolicy: businesscsv.ConflictStop,
		})
	if err != nil {
		t.Fatalf("ImportBusinessCSV: %v", err)
	}
	if result.Counts.Applied != 1 || result.Counts.Conflicts != 1 ||
		!result.Counts.Stopped || len(fn.createCalls) != 1 ||
		fn.createCalls[0].Account != "acc-new" {
		t.Fatalf("result=%+v createCalls=%+v", result.Counts, fn.createCalls)
	}
	if len(fn.auditCalls) != 1 ||
		fn.auditCalls[0].Action != domain.AuditActionImportBusinessCSV ||
		!strings.Contains(fn.auditCalls[0].Detail, "policy=stop") {
		t.Fatalf("auditCalls = %+v", fn.auditCalls)
	}
}

func TestService_BusinessCSVPartialImportFailureAuditsFileAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, eng := newBusinessCSVRealService(t)

	body := []byte(
		"code,title,group_code,notes,blocked,block_reason\n" +
			"acc-good,,,first note,false,\n" +
			",,,bad note,false,\n",
	)
	_, err := svc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccounts,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       "accounts.csv",
		Payload:        body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ImportBusinessCSV error = %v, want invalid", err)
	}

	if _, ok, err := st.GetAccount(ctx, "acc-good"); err != nil || ok {
		t.Fatalf("GetAccount acc-good after failed import: %v ok=%v, want absent", err, ok)
	}

	operationAudits, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{
			domain.AuditActionCreateAccount,
			domain.AuditActionSetNotes,
			domain.AuditActionSetGroup,
			domain.AuditActionBlock,
			domain.AuditActionUnblock,
		},
		Account: "acc-good",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered operations: %v", err)
	}
	if len(operationAudits) != 0 {
		t.Fatalf("operation audits = %+v, want none after rollback", operationAudits)
	}
	if len(eng.adjustmentCalls) != 0 {
		t.Fatalf("engine adjustment calls = %d, want none", len(eng.adjustmentCalls))
	}

	importAudits, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionImportBusinessCSV},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered import: %v", err)
	}
	if len(importAudits) != 1 ||
		!strings.Contains(importAudits[0].Detail, "entity=accounts") ||
		!strings.Contains(importAudits[0].Detail, "delimiter=comma") ||
		!strings.Contains(importAudits[0].Detail, "file=accounts.csv") ||
		!strings.Contains(importAudits[0].Detail, "policy=replace") ||
		!strings.Contains(importAudits[0].Detail, "rows=2") ||
		!strings.Contains(importAudits[0].Detail, "applied=0") ||
		!strings.Contains(importAudits[0].Detail, "error=") {
		t.Fatalf("import audit rows = %+v", importAudits)
	}
}

func TestService_BusinessCSVImportRejectsInvalidTitle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newBusinessCSVRealService(t)
	longTitle := strings.Repeat("x", 257)

	body := []byte(
		"code,title,group_code,notes,blocked,block_reason\n" +
			"acc-bad," + longTitle + ",,,false,\n",
	)
	_, err := svc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccounts,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       "accounts.csv",
		Payload:        body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ImportBusinessCSV error = %v, want invalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, "acc-bad"); err != nil || ok {
		t.Fatalf("GetAccount acc-bad after failed import: %v ok=%v, want absent", err, ok)
	}
}

func TestService_BusinessCSVPositionsRoundTripPreservesRealizedPnl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sourceSvc, sourceStore, _ := newBusinessCSVRealService(t)
	if err := sourceStore.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset source: %v", err)
	}
	if _, err := sourceStore.CreateAccount(ctx, domain.Account{
		Code: "acc-1",
	}); err != nil {
		t.Fatalf("CreateAccount source: %v", err)
	}
	want := domain.Balance{
		Account:           "acc-1",
		Asset:             "USD",
		Available:         "100.25",
		Held:              "10.5",
		Incoming:          "2.75",
		RealizedPnl:       "7.125",
		AverageEntryPrice: "99.5",
	}
	if err := sourceStore.UpsertBalance(ctx, want); err != nil {
		t.Fatalf("UpsertBalance source: %v", err)
	}

	file, err := sourceSvc.ExportBusinessCSV(ctx, backend.BusinessCSVExportRequest{
		Entity:    businesscsv.EntityPositions,
		Delimiter: businesscsv.DelimiterSemicolon,
	})
	if err != nil {
		t.Fatalf("ExportBusinessCSV: %v", err)
	}
	if !strings.Contains(string(file.Body), "7.125") {
		t.Fatalf("exported body %q does not contain realized_pnl", file.Body)
	}

	targetSvc, targetStore, targetEngine := newBusinessCSVRealService(t)
	// Positions reference an existing account and asset by code; the relational
	// store enforces those foreign keys, so seed the dictionary rows before import.
	if err := targetStore.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset target: %v", err)
	}
	if _, err := targetStore.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount target: %v", err)
	}
	result, err := targetSvc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityPositions,
		Delimiter:      businesscsv.DelimiterSemicolon,
		Filename:       file.Name,
		Payload:        file.Body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if err != nil {
		t.Fatalf("ImportBusinessCSV: %v", err)
	}
	if result.Counts.Applied != 1 || result.Counts.Conflicts != 0 {
		t.Fatalf("import counts = %+v", result.Counts)
	}
	if len(targetEngine.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %d, want 1", len(targetEngine.adjustmentCalls))
	}
	got, ok, err := targetStore.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance target: %v ok=%v", err, ok)
	}
	if got.Available != want.Available || got.Held != want.Held ||
		got.Incoming != want.Incoming || got.RealizedPnl != want.RealizedPnl ||
		got.AverageEntryPrice != want.AverageEntryPrice {
		t.Fatalf("target balance = %+v, want %+v", got, want)
	}
	audits, err := targetStore.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered adjustment: %v", err)
	}
	if len(audits) != 1 ||
		!strings.Contains(audits[0].Detail, "import position snapshot account acc-1 asset=USD") ||
		!strings.Contains(audits[0].Detail, "realized_pnl=7.125") {
		t.Fatalf("adjustment audit rows = %+v", audits)
	}
	importAudits, err := targetStore.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionImportBusinessCSV},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered import: %v", err)
	}
	if len(importAudits) != 1 || !strings.Contains(importAudits[0].Detail, "applied=1") {
		t.Fatalf("import audit rows = %+v", importAudits)
	}
}

func TestService_BusinessCSVExportAuditsAndDoesNotReuseBackupAction(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.accounts = []domain.Account{{
		Code:      "acc-1",
		GroupCode: "desk-a",
	}}

	file, err := svc.ExportBusinessCSV(context.Background(),
		backend.BusinessCSVExportRequest{
			Entity:    businesscsv.EntityAccounts,
			Delimiter: businesscsv.DelimiterPipe,
			Filter: businesscsv.ExportFilter{
				GroupCode:    "desk-a",
				GroupCodeSet: true,
			},
		})
	if err != nil {
		t.Fatalf("ExportBusinessCSV: %v", err)
	}
	if !strings.Contains(string(file.Body), "acc-1||desk-a") {
		t.Fatalf("body = %q", file.Body)
	}
	if len(fn.auditCalls) != 1 ||
		fn.auditCalls[0].Action != domain.AuditActionExportBusinessCSV ||
		strings.Contains(string(fn.auditCalls[0].Action), "backup") {
		t.Fatalf("auditCalls = %+v", fn.auditCalls)
	}
}

func newBusinessCSVRealService(
	t *testing.T,
) (*backend.Service, store.RealmStore, *businessCSVRoundTripEngine) {
	t.Helper()
	ctx := context.Background()
	st, err := store.NewSQLiteStore(t.TempDir() + "/business-csv.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// All data access hangs off the realm handle; the single-realm SQLite store
	// serves domain.DefaultRealm.
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	eng := &businessCSVRoundTripEngine{running: true}
	n, _, err := node.NewLocalNode(ctx, st, func(engine.Snapshot) (engine.Engine, error) {
		return eng, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	router, err := node.NewLocalRouter(n)
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	return backend.New(router, nil, nil), realm, eng
}

type businessCSVRoundTripEngine struct {
	running              bool
	adjustmentCalls      []domain.AdjustmentRequest
	adjustmentBatchCalls [][]domain.AdjustmentRequest
}

func (e *businessCSVRoundTripEngine) Version() string      { return "fake" }
func (e *businessCSVRoundTripEngine) BuildProfile() string { return "test" }
func (e *businessCSVRoundTripEngine) Running() bool        { return e.running }
func (e *businessCSVRoundTripEngine) ConfigurePolicy(
	context.Context, string, engine.LimitSet,
) error {
	return nil
}
func (e *businessCSVRoundTripEngine) BlockAccount(context.Context, domain.AccountID, string) error {
	return nil
}
func (e *businessCSVRoundTripEngine) UnblockAccount(context.Context, domain.AccountID) error {
	return nil
}
func (e *businessCSVRoundTripEngine) ApplyAccountAdjustment(
	ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	results, batchReject, err := e.ApplyAccountAdjustmentBatch(ctx, account,
		[]domain.AdjustmentRequest{req})
	if err != nil {
		return engine.AdjustmentResult{}, err
	}
	if batchReject != nil {
		return engine.AdjustmentResult{Rejected: batchReject}, nil
	}
	return results[0], nil
}
func (e *businessCSVRoundTripEngine) ApplyAccountAdjustmentBatch(
	_ context.Context, _ domain.AccountID, reqs []domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	e.adjustmentBatchCalls = append(e.adjustmentBatchCalls,
		append([]domain.AdjustmentRequest(nil), reqs...))
	results := make([]engine.AdjustmentResult, 0, len(reqs))
	for _, req := range reqs {
		e.adjustmentCalls = append(e.adjustmentCalls, req)
		accepted := &domain.AdjustmentOutcomeAccepted{}
		if req.Balance != nil {
			accepted.BalanceResult = req.Balance.Value
		}
		if req.Held != nil {
			accepted.HeldResult = req.Held.Value
		}
		if req.Incoming != nil {
			accepted.IncomingResult = req.Incoming.Value
		}
		results = append(results, engine.AdjustmentResult{Accepted: accepted})
	}
	return results, nil, nil
}
func (e *businessCSVRoundTripEngine) SubmitOrder(
	context.Context, domain.Order,
) (engine.OrderResult, error) {
	return engine.OrderResult{Accepted: true}, nil
}
func (e *businessCSVRoundTripEngine) ReserveHold(
	context.Context, domain.Order,
) (engine.HoldResult, error) {
	return engine.HoldResult{Accepted: true}, nil
}
func (e *businessCSVRoundTripEngine) CommitHeld(context.Context, string) error {
	return nil
}
func (e *businessCSVRoundTripEngine) RollbackHeld(context.Context, string) error {
	return nil
}
func (e *businessCSVRoundTripEngine) SubmitImmediate(
	context.Context, domain.Order,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{Accepted: true}, nil
}
func (e *businessCSVRoundTripEngine) SetReservationStore(engine.ReservationStore) {}
func (e *businessCSVRoundTripEngine) ReconcileOrphans(context.Context) (int, error) {
	return 0, nil
}
func (e *businessCSVRoundTripEngine) ApplyExecutionReport(
	context.Context, domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, nil
}
func (e *businessCSVRoundTripEngine) RegisterGroup(
	context.Context, []domain.AccountID, string,
) error {
	return nil
}
func (e *businessCSVRoundTripEngine) UnregisterGroup(
	context.Context, []domain.AccountID, string,
) error {
	return nil
}
func (e *businessCSVRoundTripEngine) BlockGroup(context.Context, string, string) error {
	return nil
}
func (e *businessCSVRoundTripEngine) UnblockGroup(context.Context, string) error {
	return nil
}
func (e *businessCSVRoundTripEngine) CheckOrder(
	context.Context, domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}
func (e *businessCSVRoundTripEngine) MarketDataSink() marketdata.Sink {
	return &backendTestSink{}
}
func (e *businessCSVRoundTripEngine) Stop() { e.running = false }

func TestService_BusinessCSVExportAccountGroupFilterPresence(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.accounts = []domain.Account{
		{Code: "acc-none"},
		{Code: "acc-desk-a", GroupCode: "desk-a"},
		{Code: "acc-desk-b", GroupCode: "desk-b"},
	}

	cases := []struct {
		name       string
		filter     businesscsv.ExportFilter
		want       []string
		wantDetail string
	}{
		{
			name: "omitted group exports all",
			want: []string{"acc-none", "acc-desk-a", "acc-desk-b"},
		},
		{
			name:       "explicit empty group exports no-group bucket",
			filter:     businesscsv.ExportFilter{GroupCodeSet: true},
			want:       []string{"acc-none"},
			wantDetail: "filters=group=<none>",
		},
		{
			name: "explicit group exports matching group",
			filter: businesscsv.ExportFilter{
				GroupCode: "desk-a", GroupCodeSet: true,
			},
			want:       []string{"acc-desk-a"},
			wantDetail: "filters=group=desk-a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn.auditCalls = nil
			file, err := svc.ExportBusinessCSV(context.Background(),
				backend.BusinessCSVExportRequest{
					Entity:    businesscsv.EntityAccounts,
					Delimiter: businesscsv.DelimiterComma,
					Filter:    tc.filter,
				})
			if err != nil {
				t.Fatalf("ExportBusinessCSV: %v", err)
			}
			body := string(file.Body)
			for _, id := range tc.want {
				if !strings.Contains(body, id) {
					t.Fatalf("body %q missing %s", body, id)
				}
			}
			for _, account := range fn.accounts {
				if slices.Contains(tc.want, string(account.Code)) {
					continue
				}
				if strings.Contains(body, string(account.Code)) {
					t.Fatalf("body %q unexpectedly contains %s", body, account.Code)
				}
			}
			if tc.wantDetail != "" && (len(fn.auditCalls) != 1 ||
				!strings.Contains(fn.auditCalls[0].Detail, tc.wantDetail)) {
				t.Fatalf("auditCalls = %+v, want detail %q", fn.auditCalls, tc.wantDetail)
			}
		})
	}
}

func TestService_CommandEnabledResolves(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mcpAccess = map[string]bool{"check_order": false}
	ctx := context.Background()

	if enabled, err := svc.CommandEnabled(ctx, "check_order"); err != nil || enabled {
		t.Fatalf("check_order override should disable: enabled=%v err=%v", enabled, err)
	}
	if enabled, err := svc.CommandEnabled(ctx, "health"); err != nil || !enabled {
		t.Fatalf("health should default enabled: enabled=%v err=%v", enabled, err)
	}
	if enabled, err := svc.CommandEnabled(ctx, "submit_order"); err != nil || enabled {
		t.Fatalf("submit_order should default disabled: enabled=%v err=%v", enabled, err)
	}
}
