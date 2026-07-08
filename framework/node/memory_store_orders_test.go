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
	"slices"
	"sort"
	"time"

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
	orders, err := r.ListAllOrders(ctx, filter.Account, filter.Source)
	if err != nil {
		return store.OrderListPage{}, err
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

func (r *memoryRealm) RecordOrderSettlement(
	_ context.Context, st domain.OrderSettlement,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st.ReservationApprovalID != "" {
		if intent, ok := r.reservations[st.ReservationApprovalID]; ok {
			intent.State = st.ReservationIntentState
			r.reservations[st.ReservationApprovalID] = intent
		}
	}
	if !st.Order.IsZero() {
		order, ok := r.orders[st.Order]
		if !ok {
			return domain.ErrNotFound
		}
		if len(st.AllowedFrom) > 0 && !slices.Contains(st.AllowedFrom, order.Status) {
			return domain.ErrConflict
		}
		order.Status = st.OrderStatus
		if st.SetLock {
			order.Lock = append([]byte(nil), st.Lock...)
		}
		if st.Leaves != "" {
			order.Leaves = st.Leaves
		}
		r.orders[st.Order] = order
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
		next, err := domain.AddDecimals(current.RealizedPnl, balance.Outcome.RealizedPnlDelta)
		if err != nil {
			return err
		}
		current.RealizedPnl = next
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
		account.Blocked = true
		account.BlockReason = block.Reason
		r.accounts[block.Account] = account
	}
	for _, event := range st.Events {
		if event.Order.IsZero() {
			event.Order = st.Order
		}
		if event.ExternalID.IsZero() {
			event.ExternalID = r.nextExternalID()
		}
		if event.At.IsZero() {
			event.At = time.Now().UTC()
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
	return nil
}

func (r *memoryRealm) RecordOrderSubmission(
	ctx context.Context,
	o domain.Order,
	submitted domain.OrderEvent,
	apply func(domain.Order) (domain.OrderSettlement, error),
) (domain.Order, error) {
	snapshot := r.exportData(ctx)
	order, err := r.CreateOrder(ctx, o)
	if err != nil {
		return domain.Order{}, err
	}
	if submitted.Order.IsZero() {
		submitted.Order = order.ExternalID
	}
	if _, err := r.AppendOrderEvent(ctx, submitted); err != nil {
		r.restoreData(snapshot)
		return domain.Order{}, err
	}

	settlement, err := apply(order)
	if err != nil {
		r.restoreData(snapshot)
		return domain.Order{}, err
	}
	if settlement.Order.IsZero() {
		settlement.Order = order.ExternalID
	}
	if settlement.Account == "" {
		settlement.Account = order.Account
	}
	if err := r.RecordOrderSettlement(ctx, settlement); err != nil {
		r.restoreData(snapshot)
		return domain.Order{}, err
	}

	order.Status = settlement.OrderStatus
	if settlement.SetLock {
		order.Lock = append([]byte(nil), settlement.Lock...)
	}
	if settlement.Leaves != "" {
		order.Leaves = settlement.Leaves
	}
	return order, nil
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
