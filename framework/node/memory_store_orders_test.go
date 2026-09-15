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
	"sort"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (r *memoryRealm) CreateOrder(
	_ context.Context, order domain.Order,
) (domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if order.ExternalID.IsZero() {
		order.ExternalID = r.nextExternalID()
	} else if _, ok := r.orders[order.ExternalID]; ok {
		return domain.Order{}, domain.ErrAlreadyExists
	}
	if order.At.IsZero() {
		order.At = time.Now().UTC()
	}
	r.orders[order.ExternalID] = order
	return order, nil
}

func (r *memoryRealm) UpdateOrderStatus(
	_ context.Context, id domain.ExternalID, status domain.OrderStatus,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	order, ok := r.orders[id]
	if !ok {
		return domain.ErrNotFound
	}
	order.Status = status
	r.orders[id] = order
	return nil
}

func (r *memoryRealm) SetOrderLock(
	_ context.Context, id domain.ExternalID, lock []byte,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	order, ok := r.orders[id]
	if !ok {
		return domain.ErrNotFound
	}
	order.Lock = append([]byte(nil), lock...)
	r.orders[id] = order
	return nil
}

func (r *memoryRealm) PutEventAttestation(
	_ context.Context, eventID domain.ExternalID, att domain.EventAttestation,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for _, event := range r.events {
		if event.ExternalID == eventID {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if _, ok := r.attestations[eventID]; !ok {
		r.attestations[eventID] = att
	}
	return nil
}

func (r *memoryRealm) GetOrder(
	_ context.Context, id domain.ExternalID,
) (domain.OrderDetail, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	order, ok := r.orders[id]
	if !ok {
		return domain.OrderDetail{}, domain.ErrNotFound
	}
	detail := domain.OrderDetail{Order: order}
	for _, event := range r.events {
		if event.Order == id {
			if att, ok := r.attestations[event.ExternalID]; ok {
				a := att
				event.Attestation = &a
			}
			detail.Events = append(detail.Events, event)
		}
	}
	for _, trade := range r.trades {
		if trade.Order == id {
			detail.Trades = append(detail.Trades, trade)
		}
	}
	return detail, nil
}

func (r *memoryRealm) ListOrders(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Order, error) {
	if n <= 0 {
		return []domain.Order{}, nil
	}
	all, err := r.ListAllOrders(ctx, account, source)
	if err != nil || len(all) <= n {
		return all, err
	}
	return all[:n], nil
}

func (r *memoryRealm) ListOrderRows(
	ctx context.Context, filter store.OrderListFilter,
) (store.OrderListPage, error) {
	all, err := r.ListAllOrders(ctx, filter.Account, filter.Source)
	if err != nil {
		return store.OrderListPage{}, err
	}
	orders := make([]domain.Order, 0, len(all))
	for _, order := range all {
		if !textMatches(filter.BaseAsset, order.BaseAsset) ||
			!textMatches(filter.QuoteAsset, order.QuoteAsset) {
			continue
		}
		if len(filter.Status) > 0 && !slices.Contains(filter.Status, order.Status) {
			continue
		}
		orders = append(orders, order)
	}
	rows := make([]store.OrderListRow, 0, len(orders))
	for _, order := range orders {
		rows = append(rows, store.OrderListRow{Order: order})
	}
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(rows))
		end := min(start+filter.Page.Limit, len(rows))
		rows = rows[start:end]
	}
	return store.OrderListPage{Rows: rows, Total: len(orders)}, nil
}

func (r *memoryRealm) ListAllOrders(
	_ context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []domain.Order
	for _, order := range r.orders {
		if account != "" && order.Account != account {
			continue
		}
		if source != "" && order.Source != source {
			continue
		}
		out = append(out, order)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func (r *memoryRealm) CountOrders(context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.orders), nil
}

func (r *memoryRealm) CountActiveOrders(context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, order := range r.orders {
		switch order.Status {
		case domain.OrderStatusSubmitted,
			domain.OrderStatusAccepted,
			domain.OrderStatusCommitted,
			domain.OrderStatusPartiallyFilled:
			n++
		}
	}
	return n, nil
}

func (r *memoryRealm) CountOrdersSince(_ context.Context, since time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var n int
	for _, order := range r.orders {
		if !order.At.Before(since) {
			n++
		}
	}
	return n, nil
}

func (r *memoryRealm) ExecutionReportExists(
	_ context.Context, id domain.ExternalID,
) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.reports[id]
	return ok, nil
}

func (r *memoryRealm) RecordOrderSettlement(
	ctx context.Context, st domain.OrderSettlement,
) (domain.ExternalID, error) {
	return r.recordOrderSettlement(ctx, st, nil)
}

func (r *memoryRealm) RecordOrderSettlementWithAttestation(
	ctx context.Context, st domain.OrderSettlement, attest store.EventAttestor,
) (domain.ExternalID, error) {
	snapshot := r.exportData(ctx)
	reportID, err := r.recordOrderSettlement(ctx, st, attest)
	if err != nil {
		r.restoreData(snapshot, backup.RestoreModeOverwrite)
	}
	return reportID, err
}

func (r *memoryRealm) recordOrderSettlement(
	ctx context.Context, st domain.OrderSettlement, attest store.EventAttestor,
) (domain.ExternalID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if attest != nil && len(st.Events) == 0 {
		return "", fmt.Errorf(
			"store: settlement has no event to attest: %w", domain.ErrInvalid,
		)
	}
	var reportID domain.ExternalID
	if st.ReportID != nil {
		reportID = *st.ReportID
		if reportID.IsZero() {
			reportID = r.nextExternalID()
		}
		if _, exists := r.reports[reportID]; exists {
			return "", domain.ErrAlreadyExists
		}
	}
	if !st.Order.IsZero() {
		order, ok := r.orders[st.Order]
		if !ok {
			return "", domain.ErrNotFound
		}
		if len(st.AllowedFrom) > 0 && !slices.Contains(st.AllowedFrom, order.Status) {
			return "", domain.ErrConflict
		}
		order.Status = st.OrderStatus
		if st.SetLock {
			order.Lock = append([]byte(nil), st.Lock...)
		}
		if st.Leaves != "" {
			order.Leaves = st.Leaves
		}
		if st.ReservedQuantity != "" {
			order.ReservedQuantity = st.ReservedQuantity
		}
		r.orders[st.Order] = order
	}
	if st.AccountPnl != "" || st.AccountPnlHaltReason != "" {
		account, ok := r.accounts[st.Account]
		if !ok {
			return "", domain.ErrNotFound
		}
		if st.AccountPnl != "" {
			account.Pnl = st.AccountPnl
			account.PnlHaltReason = st.AccountPnlHaltReason
		} else {
			account.PnlHaltReason = st.AccountPnlHaltReason
		}
		r.accounts[st.Account] = account
	}
	for _, balance := range st.Balances {
		key := balanceKey(st.Account, balance.Asset)
		current := r.balances[key]
		current.Account = st.Account
		current.Asset = balance.Asset
		if balance.Outcome.BalanceResult != "" {
			current.Available = balance.Outcome.BalanceResult
		}
		if current.Available == "" {
			current.Available = "0"
		}
		if balance.Outcome.HeldResult != "" {
			current.Held = balance.Outcome.HeldResult
		}
		if current.Held == "" {
			current.Held = "0"
		}
		if balance.Outcome.IncomingResult != "" {
			current.Incoming = balance.Outcome.IncomingResult
		}
		if current.Incoming == "" {
			current.Incoming = "0"
		}
		if balance.Outcome.RealizedPnlResult != "" {
			current.RealizedPnl = balance.Outcome.RealizedPnlResult
			current.RealizedPnlHaltReason = ""
		} else if balance.Outcome.RealizedPnlHaltReason != "" {
			current.RealizedPnlHaltReason = balance.Outcome.RealizedPnlHaltReason
		}
		if balance.Outcome.AverageEntryPrice != "" {
			current.AverageEntryPrice = balance.Outcome.AverageEntryPrice
		}
		if decimalZeroOrEmpty(current.Available) &&
			decimalZeroOrEmpty(current.Held) &&
			decimalZeroOrEmpty(current.Incoming) {
			// A closed position has no cost basis; realised PnL remains
			// independent.
			current.AverageEntryPrice = ""
		}
		if balanceIsEmpty(current) {
			delete(r.balances, key)
			continue
		}
		r.balances[key] = current
	}
	for _, block := range st.Blocks {
		account, ok := r.accounts[block.Account]
		if !ok {
			continue
		}
		if account.Blocked {
			continue
		}
		account.Blocked = true
		account.BlockReason = domain.NormalizeReason(block.Reason)
		account.BlockPolicy = block.Policy
		account.BlockCode = block.Code
		account.BlockDetails = block.Details
		r.accounts[block.Account] = account
	}
	for _, event := range st.Events {
		if !reportID.IsZero() && event.Payload.ExecutionReport != nil {
			request := *event.Payload.ExecutionReport
			request.ExternalID = reportID
			event.Payload.ExecutionReport = &request
		}
		if event.Order.IsZero() {
			event.Order = st.Order
		}
		if event.ExternalID.IsZero() {
			event.ExternalID = r.nextExternalID()
		}
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		}
		if err := r.attestEventLocked(ctx, event, attest); err != nil {
			return "", err
		}
		r.events = append(r.events, event)
	}
	if st.Trade != nil {
		trade := *st.Trade
		if trade.ExternalID.IsZero() {
			trade.ExternalID = r.nextExternalID()
		}
		if trade.At.IsZero() {
			trade.At = time.Now().UTC()
		}
		r.trades = append(r.trades, trade)
	}
	if st.ReportID != nil {
		r.reports[reportID] = st.Order
		*st.ReportID = reportID
	}
	return reportID, nil
}

func (r *memoryRealm) RecordOrderSubmission(
	ctx context.Context,
	o domain.Order,
	submitted domain.OrderEvent,
	apply func(domain.Order) (domain.OrderSettlement, error),
) (domain.Order, error) {
	return r.recordOrderSubmission(ctx, o, submitted, apply, nil)
}

func (r *memoryRealm) RecordOrderSubmissionWithAttestation(
	ctx context.Context,
	o domain.Order,
	submitted domain.OrderEvent,
	apply func(domain.Order) (domain.OrderSettlement, error),
	attest store.EventAttestor,
) (domain.Order, error) {
	return r.recordOrderSubmission(ctx, o, submitted, apply, attest)
}

func (r *memoryRealm) recordOrderSubmission(
	ctx context.Context,
	o domain.Order,
	submitted domain.OrderEvent,
	apply func(domain.Order) (domain.OrderSettlement, error),
	attest store.EventAttestor,
) (domain.Order, error) {
	snapshot := r.exportData(ctx)
	order, err := r.CreateOrder(ctx, o)
	if err != nil {
		return domain.Order{}, err
	}
	if submitted.Order.IsZero() {
		submitted.Order = order.ExternalID
	}
	recordedSubmitted, err := r.AppendOrderEvent(ctx, submitted)
	if err != nil {
		r.restoreData(snapshot, backup.RestoreModeOverwrite)
		return domain.Order{}, err
	}

	settlement, err := apply(order)
	if err != nil {
		r.restoreData(snapshot, backup.RestoreModeOverwrite)
		return domain.Order{}, err
	}
	// The submitted event is offered to attest only after apply has returned,
	// as the SQLite store does: the attestor is chosen while apply runs.
	r.mu.Lock()
	err = r.attestEventLocked(ctx, recordedSubmitted, attest)
	r.mu.Unlock()
	if err != nil {
		r.restoreData(snapshot, backup.RestoreModeOverwrite)
		return domain.Order{}, err
	}
	if settlement.Order.IsZero() {
		settlement.Order = order.ExternalID
	}
	if settlement.Account == "" {
		settlement.Account = order.Account
	}
	if _, err := r.recordOrderSettlement(ctx, settlement, attest); err != nil {
		r.restoreData(snapshot, backup.RestoreModeOverwrite)
		return domain.Order{}, err
	}

	order.Status = settlement.OrderStatus
	if settlement.SetLock {
		order.Lock = append([]byte(nil), settlement.Lock...)
	}
	if settlement.Leaves != "" {
		order.Leaves = settlement.Leaves
	}
	if settlement.ReservedQuantity != "" {
		order.ReservedQuantity = settlement.ReservedQuantity
	}
	return order, nil
}

// attestEventLocked offers one recorded event to attest and stamps the returned
// attestation onto it, as the SQLite store does inside its transaction. The
// caller holds r.mu.
func (r *memoryRealm) attestEventLocked(
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
			"store: event %s has no attestation payload: %w",
			event.Type, domain.ErrInvalid,
		)
	}
	r.attestations[event.ExternalID] = att
	return nil
}

func (r *memoryRealm) AppendOrderEvent(
	_ context.Context, event domain.OrderEvent,
) (domain.OrderEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.ExternalID.IsZero() {
		event.ExternalID = r.nextExternalID()
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	r.events = append(r.events, event)
	return event, nil
}

func (r *memoryRealm) ListOrderEvents(
	_ context.Context, order domain.ExternalID,
) ([]domain.OrderEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []domain.OrderEvent
	for _, event := range r.events {
		if event.Order == order {
			if att, ok := r.attestations[event.ExternalID]; ok {
				a := att
				event.Attestation = &a
			}
			out = append(out, event)
		}
	}
	return out, nil
}

// exportEvents returns every event with its 1:1 attestation folded in, so the
// portable archive carries attestations per-event.

func (r *memoryRealm) exportEvents() []domain.OrderEvent {
	out := make([]domain.OrderEvent, 0, len(r.events))
	for _, event := range r.events {
		if att, ok := r.attestations[event.ExternalID]; ok {
			a := att
			event.Attestation = &a
		}
		out = append(out, event)
	}
	return out
}

func (r *memoryRealm) CreateTrade(
	_ context.Context, trade domain.Trade,
) (domain.Trade, error) {
	if trade.ExternalID.IsZero() {
		trade.ExternalID = r.nextExternalID()
	}
	if trade.At.IsZero() {
		trade.At = time.Now().UTC()
	}
	r.trades = append(r.trades, trade)
	return trade, nil
}

func (r *memoryRealm) ListTrades(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Trade, error) {
	if n <= 0 {
		return []domain.Trade{}, nil
	}
	all, err := r.ListAllTrades(ctx, account, source)
	if err != nil || len(all) <= n {
		return all, err
	}
	return all[:n], nil
}

func (r *memoryRealm) ListAllTrades(
	_ context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Trade, error) {
	var out []domain.Trade
	for _, trade := range r.trades {
		if account != "" && trade.Account != account {
			continue
		}
		if source != "" && trade.Source != source {
			continue
		}
		out = append(out, trade)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func (r *memoryRealm) ListTradeRows(
	ctx context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	trades, err := r.ListAllTrades(ctx, "", "")
	if err != nil {
		return store.TradeListPage{}, err
	}
	out := make([]domain.Trade, 0, len(trades))
	for _, trade := range trades {
		if !filter.ExternalID.IsZero() && trade.ExternalID != filter.ExternalID {
			continue
		}
		if !textMatches(filter.Account, trade.Account.String()) ||
			!textMatches(filter.BaseAsset, trade.BaseAsset) ||
			!textMatches(filter.QuoteAsset, trade.QuoteAsset) {
			continue
		}
		if filter.Side != nil && trade.Side != *filter.Side {
			continue
		}
		if filter.Source != "" && trade.Source != filter.Source {
			continue
		}
		out = append(out, trade)
	}
	total := len(out)
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(out))
		end := min(start+filter.Page.Limit, len(out))
		out = out[start:end]
	}
	return store.TradeListPage{Rows: out, Total: total}, nil
}

func TestMemoryRealmRecordOrderSubmissionWithAttestationStampsEveryEvent(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	realm := newMemoryStore("node.db").realm
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	var offered []domain.OrderEventType
	attest := func(
		_ context.Context, event domain.OrderEvent,
	) (domain.EventAttestation, bool, error) {
		if event.ExternalID.IsZero() {
			t.Fatalf("attestor offered %s without an external id", event.Type)
		}
		offered = append(offered, event.Type)
		return domain.EventAttestation{
			Token: "token-" + string(event.Type),
		}, true, nil
	}

	order, err := realm.RecordOrderSubmissionWithAttestation(
		ctx,
		domain.Order{Account: "acc-1", Status: domain.OrderStatusSubmitted},
		domain.OrderEvent{Type: domain.OrderEventSubmitted},
		func(persisted domain.Order) (domain.OrderSettlement, error) {
			return domain.OrderSettlement{
				Account:     "acc-1",
				Order:       persisted.ExternalID,
				OrderStatus: domain.OrderStatusCommitted,
				Events: []domain.OrderEvent{
					{Type: domain.OrderEventCommitted},
				},
			}, nil
		},
		attest,
	)
	if err != nil {
		t.Fatalf("RecordOrderSubmissionWithAttestation: %v", err)
	}
	if !slices.Equal(offered, []domain.OrderEventType{
		domain.OrderEventSubmitted, domain.OrderEventCommitted,
	}) {
		t.Fatalf("offered events = %v, want submitted then committed", offered)
	}
	detail, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 2 {
		t.Fatalf("events = %+v, want submitted and committed", detail.Events)
	}
	for _, event := range detail.Events {
		if event.Attestation == nil ||
			event.Attestation.Token != "token-"+string(event.Type) {
			t.Fatalf(
				"event %s attestation = %+v, want its stamped token",
				event.Type, event.Attestation,
			)
		}
	}
}

func TestMemoryRealmRecordOrderSettlementWithAttestationRollsBackOnAttestorError(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	realm := newMemoryStore("node.db").realm
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order, err := realm.CreateOrder(ctx, domain.Order{
		Account: "acc-1", Status: domain.OrderStatusCommitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	attestErr := errors.New("attestation failed")

	_, err = realm.RecordOrderSettlementWithAttestation(
		ctx,
		domain.OrderSettlement{
			Account:     "acc-1",
			Order:       order.ExternalID,
			OrderStatus: domain.OrderStatusCancelled,
			Events:      []domain.OrderEvent{{Type: domain.OrderEventCancelled}},
		},
		func(context.Context, domain.OrderEvent) (domain.EventAttestation, bool, error) {
			return domain.EventAttestation{}, false, attestErr
		},
	)
	if !errors.Is(err, attestErr) {
		t.Fatalf("RecordOrderSettlementWithAttestation = %v, want attestor error", err)
	}
	after, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if after.Order.Status != domain.OrderStatusCommitted || len(after.Events) != 0 {
		t.Fatalf("failed settlement mutated store: %+v", after)
	}
}
