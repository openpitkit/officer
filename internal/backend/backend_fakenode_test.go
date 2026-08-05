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
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/backend"
	appmarketdata "go.openpit.dev/officer/internal/marketdata"
)

// fakeNode records the commands routed to it and returns canned data. It never
// touches an engine or a store, so the backend validation/routing tests run in
// isolation.
type fakeNode struct {
	version      string
	accounts     []domain.Account
	assets       []domain.Asset
	assetClasses []domain.AssetClass
	limits       node.AccountLimits
	audit        []domain.AuditRow

	putRateLimitCalls               []domain.LimitRate
	putOrderSizeLimitCalls          []domain.LimitOrderSize
	putRateLimitHook                func()
	putOrderSizeLimitHook           func()
	putSpotFundsPnlBoundsLimitCalls []domain.LimitSpotFundsPnlBounds
	deleteLimitCalls                []node.LimitTarget
	createAssetCalls                []domain.Asset
	createAssetClassCalls           []domain.AssetClass
	createCalls                     []domain.Account
	blockCalls                      []blockCall
	adjustmentExternalIDs           []domain.ExternalID
	adjustmentErr                   error

	// missingAccountCalls records the missing-account choice each mutating
	// command received, so a routing test can assert the parameter is threaded.
	missingAccountCalls []domain.MissingAccountPolicy

	checkResult domain.CheckResult
	checkProbes []domain.OrderProbe

	mcpAccess        map[string]bool
	setMcpAccessCall []setMcpAccessCall
	userSettings     map[string]string
	mdInstances      []domain.MarketDataInstance
	mdInstruments    map[string][]domain.MarketDataInstrument
	mdQuotes         []domain.MarketDataQuote

	backupArchive           backup.Archive
	backupScope             backup.Scope
	backupCaller            domain.Caller
	backupErr               error
	restoreOpts             backup.RestoreOptions
	restoreSummary          backup.RestoreSummary
	restoreSink             marketdata.Sink
	restoreErr              error
	resetCaller             domain.Caller
	resetSink               marketdata.Sink
	resetErr                error
	currentSink             marketdata.Sink
	orders                  map[domain.ExternalID]domain.Order
	allOrders               []domain.Order
	orderEvents             map[domain.ExternalID][]domain.OrderEvent
	attestations            map[domain.ExternalID]domain.EventAttestation
	nextOrderSeq            byte
	nextEventSeq            int
	submitResult            *engine.OrderResult
	immediateResult         *engine.ImmediateResult
	execReports             []domain.ExecutionReportInput
	execReportCallers       []domain.Caller
	submitErr               error
	confirmErr              error
	cancelErr               error
	getOrderErr             error
	persistAttestationErr   error
	skipAttestNewEvents     bool
	cancelNoop              bool
	execReportNoop          bool
	confirmCalls            []string
	cancelCalls             []string
	persistAttestationCalls []domain.ExternalID
	auditCalls              []store.AuditEntry

	getAccountErr error

	// Canned list pages and the last filter each list method saw, so a multi-node
	// merge test can assert global ordering, summed totals and per-node paging.
	orderRowsPage     store.OrderListPage
	balanceRowsPage   store.BalanceListPage
	policyRowsPage    store.PolicyListPage
	lastOrderFilter   store.OrderListFilter
	lastBalanceFilter store.BalanceListFilter
	lastPolicyFilter  store.PolicyListFilter

	// getOrderCount counts GetOrder invocations; the order-resolving flows must
	// fetch the stored order at most once per operation.
	getOrderCount atomic.Int64
}

type fakeOrderTxnSnapshot struct {
	orders                  map[domain.ExternalID]domain.Order
	orderEvents             map[domain.ExternalID][]domain.OrderEvent
	attestations            map[domain.ExternalID]domain.EventAttestation
	execReports             []domain.ExecutionReportInput
	confirmCalls            []string
	cancelCalls             []string
	persistAttestationCalls []domain.ExternalID
	nextOrderSeq            byte
	nextEventSeq            int
}

// newOrderExternalID returns a deterministic distinct external id for a fake
// order. Machine records are addressed by an opaque external id, not an integer,
// so the fake mints a fresh string id per submit from a monotonic seed.
func (n *fakeNode) newOrderExternalID() domain.ExternalID {
	n.nextOrderSeq++
	return domain.ExternalID(fmt.Sprintf("order-%d", n.nextOrderSeq))
}

// newEventExternalID mints a fresh non-zero external id for an order-history
// event, mirroring the store's per-event id contract: the attestation flow binds
// its envelope to the event's external id, so every fake event needs a unique
// non-zero handle.
func (n *fakeNode) newEventExternalID() domain.ExternalID {
	n.nextEventSeq++
	return domain.ExternalID(fmt.Sprintf("event-%d", n.nextEventSeq))
}

// appendEvent records one order-history event with a fresh external id, links it
// to its order, appends it to the event stream, and returns it. Every event the
// fake produces must carry a non-zero external id so the backend's attestation
// path can bind its envelope to that event.
func (n *fakeNode) appendEvent(
	orderID domain.ExternalID,
	typ domain.OrderEventType,
	payload domain.OrderEventPayload,
) domain.OrderEvent {
	if n.orderEvents == nil {
		n.orderEvents = make(map[domain.ExternalID][]domain.OrderEvent)
	}
	event := domain.OrderEvent{
		ExternalID: n.newEventExternalID(),
		Order:      orderID,
		Type:       typ,
		Payload:    payload,
	}
	n.orderEvents[orderID] = append(n.orderEvents[orderID], event)
	return event
}

func (n *fakeNode) snapshotOrderTxn() fakeOrderTxnSnapshot {
	return fakeOrderTxnSnapshot{
		orders:                  cloneOrderMap(n.orders),
		orderEvents:             cloneOrderEventMap(n.orderEvents),
		attestations:            cloneAttestationMap(n.attestations),
		execReports:             slices.Clone(n.execReports),
		confirmCalls:            slices.Clone(n.confirmCalls),
		cancelCalls:             slices.Clone(n.cancelCalls),
		persistAttestationCalls: slices.Clone(n.persistAttestationCalls),
		nextOrderSeq:            n.nextOrderSeq,
		nextEventSeq:            n.nextEventSeq,
	}
}

func (n *fakeNode) restoreOrderTxn(snapshot fakeOrderTxnSnapshot) {
	n.orders = snapshot.orders
	n.orderEvents = snapshot.orderEvents
	n.attestations = snapshot.attestations
	n.execReports = snapshot.execReports
	n.confirmCalls = snapshot.confirmCalls
	n.cancelCalls = snapshot.cancelCalls
	n.persistAttestationCalls = snapshot.persistAttestationCalls
	n.nextOrderSeq = snapshot.nextOrderSeq
	n.nextEventSeq = snapshot.nextEventSeq
}

func cloneOrderMap(
	in map[domain.ExternalID]domain.Order,
) map[domain.ExternalID]domain.Order {
	if in == nil {
		return nil
	}
	out := make(map[domain.ExternalID]domain.Order, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneOrderEventMap(
	in map[domain.ExternalID][]domain.OrderEvent,
) map[domain.ExternalID][]domain.OrderEvent {
	if in == nil {
		return nil
	}
	out := make(map[domain.ExternalID][]domain.OrderEvent, len(in))
	for k, v := range in {
		out[k] = slices.Clone(v)
	}
	return out
}

func cloneAttestationMap(
	in map[domain.ExternalID]domain.EventAttestation,
) map[domain.ExternalID]domain.EventAttestation {
	if in == nil {
		return nil
	}
	out := make(map[domain.ExternalID]domain.EventAttestation, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (n *fakeNode) attestEvent(
	ctx context.Context, event domain.OrderEvent, attest store.EventAttestor,
) error {
	if attest == nil {
		return nil
	}
	att, ok, err := attest(ctx, event)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf(
			"fake node: event %s has no attestation payload: %w",
			event.Type, domain.ErrInvalid,
		)
	}
	n.persistAttestationCalls = append(n.persistAttestationCalls, event.ExternalID)
	if n.persistAttestationErr != nil {
		return n.persistAttestationErr
	}
	if n.attestations == nil {
		n.attestations = make(map[domain.ExternalID]domain.EventAttestation)
	}
	n.attestations[event.ExternalID] = att
	return nil
}

func (n *fakeNode) attestNewEvents(
	ctx context.Context,
	orderID domain.ExternalID,
	start int,
	attest store.EventAttestor,
) error {
	if n.skipAttestNewEvents {
		return nil
	}
	events := n.orderEvents[orderID]
	for _, event := range events[start:] {
		if err := n.attestEvent(ctx, event, attest); err != nil {
			return err
		}
	}
	return nil
}

// eventExists reports whether any order carries an event with the given external
// id, so PersistEventAttestation can honour the node's "only when the event
// exists" write contract.
func (n *fakeNode) eventExists(eventID domain.ExternalID) bool {
	for _, events := range n.orderEvents {
		for i := range events {
			if events[i].ExternalID == eventID {
				return true
			}
		}
	}
	return false
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

func (n *fakeNode) ListAccountRows(
	_ context.Context, _ store.AccountListFilter,
) (store.AccountListPage, error) {
	out := make([]store.AccountListRow, 0, len(n.accounts))
	for _, account := range n.accounts {
		out = append(out, store.AccountListRow{Account: account})
	}
	return store.AccountListPage{Rows: out, Total: len(out)}, nil
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

func (n *fakeNode) CurrentMarketDataSink() marketdata.Sink {
	return n.currentSink
}

func (n *fakeNode) ListAssets(context.Context) ([]domain.Asset, error) {
	return n.assets, nil
}

func (n *fakeNode) ListAssetRows(
	_ context.Context, _ store.AssetListFilter,
) (store.AssetListPage, error) {
	return store.AssetListPage{Rows: n.assets, Total: len(n.assets)}, nil
}

func (n *fakeNode) CreateAsset(
	_ context.Context, asset domain.Asset, _ domain.Caller,
) (domain.Asset, error) {
	n.createAssetCalls = append(n.createAssetCalls, asset)
	n.assets = append(n.assets, asset)
	return asset, nil
}

func (n *fakeNode) UpdateAsset(
	_ context.Context, oldCode string, asset domain.Asset, _ domain.Caller,
) (domain.Asset, error) {
	for i, a := range n.assets {
		if a.Code == oldCode {
			n.assets[i] = asset
			return asset, nil
		}
	}
	return domain.Asset{}, domain.ErrNotFound
}

func (n *fakeNode) ListAssetClasses(_ context.Context) ([]domain.AssetClass, error) {
	return n.assetClasses, nil
}

func (n *fakeNode) ListAssetClassRows(
	_ context.Context, _ store.AssetClassListFilter,
) (store.AssetClassListPage, error) {
	out := make([]store.AssetClassListRow, 0, len(n.assetClasses))
	for _, class := range n.assetClasses {
		out = append(out, store.AssetClassListRow{Class: class})
	}
	return store.AssetClassListPage{Rows: out, Total: len(out)}, nil
}

func (n *fakeNode) CreateAssetClass(
	_ context.Context, class domain.AssetClass, _ domain.Caller,
) (domain.AssetClass, error) {
	n.createAssetClassCalls = append(n.createAssetClassCalls, class)
	n.assetClasses = append(n.assetClasses, class)
	return class, nil
}

func (n *fakeNode) UpdateAssetClass(
	_ context.Context, oldCode string, class domain.AssetClass, _ domain.Caller,
) (domain.AssetClass, error) {
	for i, c := range n.assetClasses {
		if c.Code == oldCode {
			n.assetClasses[i] = class
			return class, nil
		}
	}
	return domain.AssetClass{}, domain.ErrNotFound
}

func (n *fakeNode) DeleteAssetClass(
	_ context.Context, code string, _ bool, _ domain.Caller,
) error {
	for i, c := range n.assetClasses {
		if c.Code == code {
			n.assetClasses = append(n.assetClasses[:i], n.assetClasses[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) DeleteAsset(
	_ context.Context, code string, _ bool, _ domain.Caller,
) error {
	for i, a := range n.assets {
		if a.Code == code {
			n.assets = append(n.assets[:i], n.assets[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) CreateAccount(
	_ context.Context, account domain.Account, _ domain.Caller,
) (domain.Account, error) {
	n.createCalls = append(n.createCalls, account)
	account.EngineAccountID = 1
	n.accounts = append(n.accounts, account)
	return account, nil
}

func (n *fakeNode) SetAccountBlocked(
	_ context.Context, key node.Key, blocked bool, reason string,
	missing domain.MissingAccountPolicy, _ domain.Caller,
) error {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
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

func (n *fakeNode) ListPolicyRows(
	_ context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	n.lastPolicyFilter = filter
	return n.policyRowsPage, nil
}

func (n *fakeNode) PutRateLimit(
	_ context.Context, limit domain.LimitRate,
	missing domain.MissingAccountPolicy, _ domain.Caller,
) (marketdata.Sink, error) {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
	if n.putRateLimitHook != nil {
		n.putRateLimitHook()
	}
	n.putRateLimitCalls = append(n.putRateLimitCalls, limit)
	return n.restoreSink, nil
}

func (n *fakeNode) PutOrderSizeLimit(
	_ context.Context, limit domain.LimitOrderSize,
	missing domain.MissingAccountPolicy, _ domain.Caller,
) (marketdata.Sink, error) {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
	if n.putOrderSizeLimitHook != nil {
		n.putOrderSizeLimitHook()
	}
	n.putOrderSizeLimitCalls = append(n.putOrderSizeLimitCalls, limit)
	return n.restoreSink, nil
}

func (n *fakeNode) PutSpotFundsPnlBoundsLimit(
	_ context.Context, limit domain.LimitSpotFundsPnlBounds,
	missing domain.MissingAccountPolicy, _ domain.Caller,
) (marketdata.Sink, error) {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
	n.putSpotFundsPnlBoundsLimitCalls = append(
		n.putSpotFundsPnlBoundsLimitCalls,
		limit,
	)
	return n.restoreSink, nil
}

func (n *fakeNode) DeleteLimit(
	_ context.Context, target node.LimitTarget, _ domain.Caller,
) (marketdata.Sink, error) {
	n.deleteLimitCalls = append(n.deleteLimitCalls, target)
	return n.restoreSink, nil
}

func (n *fakeNode) SetAccountGroup(
	_ context.Context, _ node.Key, _ string,
	missing domain.MissingAccountPolicy, _ domain.Caller,
) error {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
	return nil
}

func (n *fakeNode) SetAccountCurrency(
	context.Context, node.Key, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) SetAccountNotes(
	context.Context, node.Key, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) UpdateAccount(
	_ context.Context,
	_ node.Key,
	account domain.Account,
	_ domain.Caller,
) (domain.Account, error) {
	return account, nil
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

func (n *fakeNode) ListGroupRows(
	context.Context, store.GroupListFilter,
) (store.GroupListPage, error) {
	return store.GroupListPage{}, nil
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

func (n *fakeNode) SetGroupCurrency(
	context.Context, string, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) SetDefaultGroupCurrency(
	context.Context, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) UpdateGroup(
	_ context.Context,
	_ string,
	group domain.AccountGroup,
	_ domain.Caller,
) (domain.AccountGroup, error) {
	return group, nil
}

func (n *fakeNode) SetGroupBlocked(
	context.Context, string, bool, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) DeleteGroup(context.Context, string, domain.Caller) error {
	return nil
}

func (n *fakeNode) ApplyAdjustment(
	_ context.Context, key node.Key, externalID domain.ExternalID,
	req domain.AdjustmentRequest, missing domain.MissingAccountPolicy,
	caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
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

func (n *fakeNode) SetBalanceRealizedPnl(
	_ context.Context, key node.Key, asset string, realizedPnl string,
	missing domain.MissingAccountPolicy, _ domain.Caller,
) (domain.Balance, error) {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
	return domain.Balance{
		Account:     key.Account,
		Asset:       asset,
		RealizedPnl: realizedPnl,
	}, nil
}

func (n *fakeNode) ListBalances(
	context.Context, domain.AccountID, string,
) ([]domain.Balance, error) {
	return nil, nil
}

func (n *fakeNode) ListBalanceRows(
	_ context.Context, filter store.BalanceListFilter,
) (store.BalanceListPage, error) {
	n.lastBalanceFilter = filter
	return n.balanceRowsPage, nil
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
// is minted. This keeps the fake faithful to the node submission contract, so a
// backend test can prove the id is threaded create-once and the returned id
// matches.
func (n *fakeNode) recordedOrderExternalID(o domain.Order) domain.ExternalID {
	if !o.ExternalID.IsZero() {
		return o.ExternalID
	}
	return n.newOrderExternalID()
}

// recordRejected stores the rejected order and its pre_trade_rejected event,
// mirroring the real node's final persisted state so the reject verdict is
// durable and its first reject is readable by GetOrder.
func (n *fakeNode) recordRejected(order domain.Order, rejects []domain.OrderReject) {
	order.Status = domain.OrderStatusRejected
	n.orders[order.ExternalID] = order
	payload := domain.OrderEventPayload{}
	if len(rejects) > 0 {
		r := rejects[0]
		payload.RejectCode = r.Code
		payload.RejectScope = r.Scope
		payload.RejectPolicy = r.Policy
		payload.RejectReason = r.Reason
		payload.RejectDetails = r.Details
	}
	n.appendEvent(order.ExternalID, domain.OrderEventPreTradeRejected, payload)
}

func (n *fakeNode) SubmitOrder(
	_ context.Context, key node.Key, o domain.Order,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (domain.Order, error) {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
	if n.submitErr != nil {
		return domain.Order{}, n.submitErr
	}
	order := o
	order.ExternalID = n.recordedOrderExternalID(o)
	if _, exists := n.orders[order.ExternalID]; exists {
		return domain.Order{}, fmt.Errorf(
			"fake node: order %q already exists: %w",
			order.ExternalID,
			domain.ErrAlreadyExists,
		)
	}
	order.Account = key.Account
	order.Source = caller.Source
	order.Principal = caller.Principal
	n.appendEvent(order.ExternalID, domain.OrderEventSubmitted, domain.OrderEventPayload{})
	if n.submitResult != nil && !n.submitResult.Accepted {
		n.recordRejected(order, n.submitResult.Rejects)
		order = n.orders[order.ExternalID]
	} else {
		order.Status = domain.OrderStatusCommitted
		if n.submitResult != nil {
			order.Lock = slices.Clone(n.submitResult.Lock)
		}
		n.orders[order.ExternalID] = order
		n.appendEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, domain.OrderEventPayload{})
		n.appendEvent(order.ExternalID, domain.OrderEventCommitted, domain.OrderEventPayload{})
	}
	return order, nil
}

func (n *fakeNode) SubmitOrderWithAttestation(
	ctx context.Context,
	key node.Key,
	o domain.Order,
	missing domain.MissingAccountPolicy,
	caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	snapshot := n.snapshotOrderTxn()
	before := len(n.orderEvents[o.ExternalID])
	order, err := n.SubmitOrder(ctx, key, o, missing, caller)
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	if o.ExternalID.IsZero() {
		before = 0
	}
	result := engine.OrderResult{
		Accepted:            true,
		SettlementLockPrice: "100",
	}
	if n.submitResult != nil {
		result = *n.submitResult
	}
	var attest store.EventAttestor
	if attestFor != nil {
		attest = attestFor(order, result)
	}
	if err := n.attestNewEvents(ctx, order.ExternalID, before, attest); err != nil {
		n.restoreOrderTxn(snapshot)
		return domain.Order{}, engine.OrderResult{}, err
	}
	return order, result, nil
}

func (n *fakeNode) SubmitImmediate(
	_ context.Context, key node.Key, o domain.Order,
	missing domain.MissingAccountPolicy, _ domain.Caller,
) (domain.Order, engine.ImmediateResult, error) {
	n.missingAccountCalls = append(n.missingAccountCalls, missing)
	if n.submitErr != nil {
		return domain.Order{}, engine.ImmediateResult{}, n.submitErr
	}
	order := o
	order.ExternalID = n.recordedOrderExternalID(o)
	order.Account = key.Account
	n.appendEvent(order.ExternalID, domain.OrderEventSubmitted, domain.OrderEventPayload{})
	if n.immediateResult != nil {
		if !n.immediateResult.Accepted {
			n.recordRejected(order, n.immediateResult.Rejects)
			return n.orders[order.ExternalID], *n.immediateResult, nil
		}
		order.Status = domain.OrderStatusFilled
		order.Lock = n.immediateResult.Lock
		n.orders[order.ExternalID] = order
		n.appendEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, domain.OrderEventPayload{})
		return order, completeFakeImmediateResult(order, *n.immediateResult), nil
	}
	order.Status = domain.OrderStatusFilled
	n.orders[order.ExternalID] = order
	n.appendEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, domain.OrderEventPayload{})
	tradePrice := o.Price
	if tradePrice == "" {
		tradePrice = "100"
	}
	return order, completeFakeImmediateResult(order, engine.ImmediateResult{
		Accepted:            true,
		SettlementLockPrice: "100",
		TradePrice:          tradePrice,
		FillQuantity:        o.AmountValue,
	}), nil
}

func completeFakeImmediateResult(
	order domain.Order, result engine.ImmediateResult,
) engine.ImmediateResult {
	if !result.Accepted {
		return result
	}
	if result.FillQuantity == "" {
		result.FillQuantity = order.AmountValue
	}
	if result.TradePrice == "" {
		result.TradePrice = order.Price
		if result.TradePrice == "" {
			result.TradePrice = result.SettlementLockPrice
		}
	}
	if result.LeavesQuantity == "" {
		result.LeavesQuantity = "0"
	}
	if result.ExecutionReport == nil {
		result.ExecutionReport = domain.ExecutionReportRequestFromInput(
			domain.ExecutionReportInput{
				ExternalID:     mdID("immediate-report-" + order.ExternalID.String()),
				BaseAsset:      order.BaseAsset,
				QuoteAsset:     order.QuoteAsset,
				FillQuantity:   result.FillQuantity,
				FillPrice:      result.TradePrice,
				LeavesQuantity: result.LeavesQuantity,
				LockPrice:      result.SettlementLockPrice,
				Order:          order.ExternalID,
				Account:        order.Account,
				Side:           order.Side,
				OrderStatus:    domain.OrderStatusFilled,
			},
		)
	}
	if result.Persistence == nil {
		result.Persistence = &engine.ExecutionReportPersistence{
			Trade: &domain.Trade{
				Order:      order.ExternalID,
				Account:    order.Account,
				BaseAsset:  order.BaseAsset,
				QuoteAsset: order.QuoteAsset,
				Side:       order.Side,
				Quantity:   result.FillQuantity,
				Price:      result.TradePrice,
				LockPrice:  result.SettlementLockPrice,
			},
			OrderStatus: domain.OrderStatusFilled,
			Leaves:      result.LeavesQuantity,
			Blocks:      result.Blocks,
		}
	}
	return result
}

func (n *fakeNode) SubmitImmediateWithAttestation(
	ctx context.Context,
	key node.Key,
	o domain.Order,
	missing domain.MissingAccountPolicy,
	caller domain.Caller,
	attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
) (domain.Order, engine.ImmediateResult, error) {
	snapshot := n.snapshotOrderTxn()
	before := len(n.orderEvents[o.ExternalID])
	order, result, err := n.SubmitImmediate(ctx, key, o, missing, caller)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	if result.Accepted {
		n.appendEvent(order.ExternalID, domain.OrderEventCommitted, domain.OrderEventPayload{})
		n.appendEvent(order.ExternalID, domain.OrderEventFill, domain.OrderEventPayload{
			FillQuantity:    result.FillQuantity,
			FillPrice:       result.TradePrice,
			FillLockPrice:   result.SettlementLockPrice,
			LeavesQuantity:  result.LeavesQuantity,
			OrderStatus:     string(domain.OrderStatusFilled),
			ExecutionReport: result.ExecutionReport,
		})
	}
	if o.ExternalID.IsZero() {
		before = 0
	}
	var attest store.EventAttestor
	if attestFor != nil {
		attest = attestFor(order, result)
	}
	if err := n.attestNewEvents(ctx, order.ExternalID, before, attest); err != nil {
		n.restoreOrderTxn(snapshot)
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	return order, result, nil
}

func (n *fakeNode) ConfirmOrder(
	_ context.Context, orderID domain.ExternalID, _ domain.Caller,
) (domain.Order, error) {
	if n.confirmErr != nil {
		return domain.Order{}, n.confirmErr
	}
	order := n.orders[orderID]
	for _, event := range n.orderEvents[orderID] {
		if event.Payload.ExecutionReport != nil {
			return domain.Order{}, domain.ErrExecutionReportRequired
		}
		if event.Type == domain.OrderEventConfirmed {
			return order, nil
		}
	}
	n.confirmCalls = append(n.confirmCalls, orderID.String())
	n.appendEvent(orderID, domain.OrderEventConfirmed, domain.OrderEventPayload{})
	return order, nil
}

func (n *fakeNode) ConfirmOrderWithAttestation(
	ctx context.Context,
	orderID domain.ExternalID,
	caller domain.Caller,
	attest store.EventAttestor,
) (domain.Order, error) {
	snapshot := n.snapshotOrderTxn()
	before := len(n.orderEvents[orderID])
	order, err := n.ConfirmOrder(ctx, orderID, caller)
	if err != nil {
		return domain.Order{}, err
	}
	if err := n.attestNewEvents(ctx, orderID, before, attest); err != nil {
		n.restoreOrderTxn(snapshot)
		return domain.Order{}, err
	}
	return order, nil
}

func (n *fakeNode) CancelOrder(
	_ context.Context,
	orderID domain.ExternalID,
	leavesQuantity string,
	_ domain.Caller,
) (domain.Order, engine.ExecutionReportResult, error) {
	order := n.orders[orderID]
	if n.cancelNoop {
		return order, engine.ExecutionReportResult{}, nil
	}
	if n.cancelErr != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, n.cancelErr
	}
	for _, event := range n.orderEvents[orderID] {
		if event.Payload.ExecutionReport != nil {
			return domain.Order{}, engine.ExecutionReportResult{},
				domain.ErrExecutionReportRequired
		}
	}
	n.cancelCalls = append(n.cancelCalls, orderID.String())
	in := domain.ExecutionReportInput{
		Order:          orderID,
		Account:        order.Account,
		BaseAsset:      order.BaseAsset,
		QuoteAsset:     order.QuoteAsset,
		Side:           order.Side,
		LeavesQuantity: leavesQuantity,
		Lock:           slices.Clone(order.Lock),
		OrderStatus:    domain.OrderStatusCancelled,
	}
	if _, err := domain.ExecutionReportRequiresEngine(in); err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	n.execReports = append(n.execReports, in)
	request := domain.ExecutionReportRequestFromInput(in)
	order.Status = domain.OrderStatusCancelled
	order.Leaves = leavesQuantity
	n.orders[orderID] = order
	n.appendEvent(orderID, domain.OrderEventCancelled, domain.OrderEventPayload{
		LeavesQuantity:  in.LeavesQuantity,
		OrderStatus:     string(in.OrderStatus),
		ExecutionReport: request,
	})
	persistence := engine.ExecutionReportPersistence{
		OrderStatus: domain.OrderStatusCancelled,
		Leaves:      leavesQuantity,
	}
	return order, engine.ExecutionReportResult{Persistence: &persistence}, nil
}

func (n *fakeNode) CancelOrderWithAttestation(
	ctx context.Context,
	orderID domain.ExternalID,
	leavesQuantity string,
	caller domain.Caller,
	attest store.EventAttestor,
) (domain.Order, engine.ExecutionReportResult, error) {
	snapshot := n.snapshotOrderTxn()
	before := len(n.orderEvents[orderID])
	order, result, err := n.CancelOrder(ctx, orderID, leavesQuantity, caller)
	if err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	if err := n.attestNewEvents(ctx, orderID, before, attest); err != nil {
		n.restoreOrderTxn(snapshot)
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	return order, result, nil
}

func (n *fakeNode) ApplyExecutionReport(
	_ context.Context, _ node.Key, in domain.ExecutionReportInput, caller domain.Caller,
) (engine.ExecutionReportResult, error) {
	n.execReportCallers = append(n.execReportCallers, caller)
	if n.execReportNoop {
		n.execReports = append(n.execReports, in)
		return engine.ExecutionReportResult{
			Persistence: &engine.ExecutionReportPersistence{
				OrderStatus: in.OrderStatus,
				Leaves:      in.LeavesQuantity,
			},
		}, nil
	}
	if order, ok := n.orders[in.Order]; ok {
		if err := domain.RequireOrderModifiable(order.Status, in.Force); err != nil {
			return engine.ExecutionReportResult{}, err
		}
	}
	n.execReports = append(n.execReports, in)
	request := domain.ExecutionReportRequestFromInput(in)
	// Model the node's event emission so the backend's attestation path finds the
	// event it binds to: a fill event when the report carried a fill, else the
	// mapped status-change event for the report's target status.
	var events []domain.OrderEvent
	if in.FillQuantity != "" && in.FillPrice != "" {
		payload := domain.OrderEventPayload{
			FillQuantity:    in.FillQuantity,
			FillPrice:       in.FillPrice,
			FillLockPrice:   in.LockPrice,
			LeavesQuantity:  in.LeavesQuantity,
			OrderStatus:     string(in.OrderStatus),
			Commission:      in.Commission,
			ExecutionReport: request,
		}
		n.appendEvent(in.Order, domain.OrderEventFill, payload)
		events = append(events, domain.OrderEvent{
			Order:   in.Order,
			Type:    domain.OrderEventFill,
			Payload: payload,
		})
	}
	if typ, ok := domain.ExecutionReportStatusChangeEvent(in.OrderStatus); ok {
		payload := domain.OrderEventPayload{
			LeavesQuantity:  in.LeavesQuantity,
			OrderStatus:     string(in.OrderStatus),
			Commission:      in.Commission,
			ExecutionReport: request,
		}
		n.appendEvent(in.Order, typ, payload)
		events = append(events, domain.OrderEvent{
			Order: in.Order, Type: typ, Payload: payload,
		})
	}
	if order, ok := n.orders[in.Order]; ok {
		order.Status = in.OrderStatus
		if in.LeavesQuantity != "" {
			order.Leaves = in.LeavesQuantity
		}
		n.orders[in.Order] = order
	}
	return engine.ExecutionReportResult{
		Persistence: &engine.ExecutionReportPersistence{
			OrderStatus: in.OrderStatus,
			Commission:  in.Commission,
			Leaves:      in.LeavesQuantity,
			Events:      events,
		},
	}, nil
}

func (n *fakeNode) ApplyExecutionReportWithAttestation(
	ctx context.Context,
	key node.Key,
	in domain.ExecutionReportInput,
	caller domain.Caller,
	attest store.EventAttestor,
) (engine.ExecutionReportResult, error) {
	snapshot := n.snapshotOrderTxn()
	before := len(n.orderEvents[in.Order])
	result, err := n.ApplyExecutionReport(ctx, key, in, caller)
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	if attest != nil && len(n.orderEvents[in.Order]) == before {
		n.restoreOrderTxn(snapshot)
		return engine.ExecutionReportResult{}, fmt.Errorf(
			"fake node: settlement has no event to attest: %w", domain.ErrInvalid,
		)
	}
	if err := n.attestNewEvents(ctx, in.Order, before, attest); err != nil {
		n.restoreOrderTxn(snapshot)
		return engine.ExecutionReportResult{}, err
	}
	return result, nil
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
	// Fold each event's persisted attestation into the returned events, mirroring
	// the node's GetOrder read-back. Copy the slice so the stored stream is not
	// mutated with per-read attestation pointers.
	stored := n.orderEvents[id]
	events := make([]domain.OrderEvent, len(stored))
	copy(events, stored)
	for i := range events {
		if att, ok := n.attestations[events[i].ExternalID]; ok {
			stamped := att
			events[i].Attestation = &stamped
		}
	}
	return domain.OrderDetail{Order: order, Events: events}, nil
}

func (n *fakeNode) PersistEventAttestation(
	_ context.Context, _ node.Key, eventID domain.ExternalID, att domain.EventAttestation,
) error {
	n.persistAttestationCalls = append(n.persistAttestationCalls, eventID)
	if n.persistAttestationErr != nil {
		return n.persistAttestationErr
	}
	if n.attestations == nil {
		n.attestations = make(map[domain.ExternalID]domain.EventAttestation)
	}
	// Write-once, and only when the event exists: a retry or later write never
	// clobbers an already-issued envelope, and a missing event is a tolerated
	// no-op.
	if _, done := n.attestations[eventID]; !done && n.eventExists(eventID) {
		n.attestations[eventID] = att
	}
	return nil
}

func (n *fakeNode) ListOrders(
	context.Context, domain.AccountID, domain.Source, int,
) ([]domain.Order, error) {
	return nil, nil
}

func (n *fakeNode) ListOrderRows(
	_ context.Context, filter store.OrderListFilter,
) (store.OrderListPage, error) {
	n.lastOrderFilter = filter
	return n.orderRowsPage, nil
}

func (n *fakeNode) ListAllOrders(
	context.Context, domain.AccountID, domain.Source,
) ([]domain.Order, error) {
	return append([]domain.Order(nil), n.allOrders...), nil
}

func (n *fakeNode) CountOrders(context.Context) (int, error) {
	return 0, nil
}

func (n *fakeNode) CountActiveOrders(context.Context) (int, error) {
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
	_ context.Context, id domain.ExternalID, _ domain.Caller,
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
	pushErr    error
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

func (r *fakeMarketDataRuntime) Registry() *marketdata.Registry {
	return appmarketdata.DefaultRegistry()
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
	_ context.Context, _ string, instrument domain.MarketDataInstrument,
) error {
	r.pushed = append(r.pushed, instrument)
	return r.pushErr
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
func newTestServiceWithSigner(signer fwsigning.Service) (*backend.Service, *fakeNode) {
	fn := &fakeNode{orders: make(map[domain.ExternalID]domain.Order)}
	return backend.New(&fakeRouter{node: fn}, nil, signer), fn
}
