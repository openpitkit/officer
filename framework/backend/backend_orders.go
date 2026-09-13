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

package backend

import (
	"context"
	"fmt"
	"sort"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// OrderListRow is an order row enriched with backend-owned presentation data.
type OrderListRow struct {
	Order        domain.Order
	DisplayPrice string
	Signed       bool
}

// OrderListPage is a page of backend-enriched order rows.
type OrderListPage struct {
	Rows  []OrderListRow
	Total int
}

func (s *Service) orderDisplayPrice(order domain.Order) (string, error) {
	if len(order.Lock) == 0 {
		return "", nil
	}
	price, err := s.lockSettlement(order.Lock, order)
	if err != nil {
		return "", fmt.Errorf(
			"backend: restore order %s display price: %w",
			order.ExternalID,
			err,
		)
	}
	return price, nil
}

func (s *Service) enrichOrderListPage(page store.OrderListPage) (OrderListPage, error) {
	rows := make([]OrderListRow, 0, len(page.Rows))
	for _, row := range page.Rows {
		price, err := s.orderDisplayPrice(row.Order)
		if err != nil {
			return OrderListPage{}, err
		}
		rows = append(rows, OrderListRow{
			Order:        row.Order,
			DisplayPrice: price,
			Signed:       row.Signed,
		})
	}
	return OrderListPage{Rows: rows, Total: page.Total}, nil
}

// CheckOrder validates the probe's account and assets and runs the engine
// pre-trade as a non-mutating dry-run. It mutates no state and writes no audit
// row; an engine reject is a successful call carrying the reasons, not an error.
func (s *Service) CheckOrder(
	ctx context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	// Officer applies no boundary id/asset format checks; the engine seam parses
	// the account and assets and enforces the real trading rules. An asset missing
	// from the live engine dictionary is invalid input and is never auto-created.
	return s.node.CheckOrder(ctx, probe)
}

// ApplyExecutionReport validates the target status and hands the report to the
// node for account-synchronized application. Ordinary order reports are
// signed and persisted fail-closed; drop-copy reports remain unattested.
func (s *Service) ApplyExecutionReport(
	ctx context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, Attestation, error) {
	targetStatus := in.OrderStatus
	if !domain.OrderStatusSupported(targetStatus) {
		return engine.ExecutionReportResult{}, Attestation{}, fmt.Errorf(
			"backend: invalid execution report status %q: %w", targetStatus, domain.ErrInvalid)
	}
	n := s.node
	stored, err := n.GetOrder(ctx, in.Order)
	if err != nil {
		return engine.ExecutionReportResult{}, Attestation{}, err
	}
	caller := auth.CallerFromContext(ctx)
	if isDropCopyOrder(stored.Order) {
		// Drop-copy never enforced a pre-trade verdict, so signing its lifecycle
		// would falsely represent risk approval.
		result, err := n.ApplyExecutionReport(ctx, in, caller)
		return result, Attestation{}, err
	}
	signer, err := s.signerOrErr()
	if err != nil {
		return engine.ExecutionReportResult{}, Attestation{}, err
	}
	off, signingKeyID, err := signingMode(ctx, signer)
	if err != nil {
		return engine.ExecutionReportResult{}, Attestation{}, err
	}
	attesting, ok := n.(executionReportAttestingNode)
	if !ok {
		return engine.ExecutionReportResult{}, Attestation{}, fmt.Errorf(
			"backend: execution-report attestation unsupported: %w",
			domain.ErrNotImplemented,
		)
	}
	var att Attestation
	attPriority := 0
	attest := eventAttestor(
		signer, off, signingKeyID, domain.AttestationRequestExecutionReport,
		func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
			if event.Type != domain.OrderEventFill &&
				event.Type != domain.OrderEventCommission {
				eventType, ok := domain.ExecutionReportStatusChangeEvent(
					domain.OrderStatus(event.Payload.OrderStatus))
				if !ok || event.Type != eventType {
					eventType, ok = domain.ExecutionReportStatusChangeEvent(
						targetStatus)
				}
				if !ok || event.Type != eventType {
					return domain.ApprovalPayload{}, false, nil
				}
			}
			if event.Payload.OrderStatus == "" &&
				event.Type != domain.OrderEventFill &&
				event.Type != domain.OrderEventCommission {
				return domain.ApprovalPayload{}, false, nil
			}
			persistence := engine.ExecutionReportPersistence{
				OrderStatus: domain.OrderStatus(event.Payload.OrderStatus),
				Commission:  event.Payload.Commission,
				Leaves:      event.Payload.LeavesQuantity,
			}
			if persistence.OrderStatus == "" {
				persistence.OrderStatus = targetStatus
			}
			if event.Payload.FillQuantity != "" || event.Payload.FillPrice != "" {
				persistence.Trade = &domain.Trade{
					Order:      event.Order,
					Account:    stored.Order.Account,
					Source:     caller.Source,
					Principal:  caller.Principal,
					BaseAsset:  stored.Order.BaseAsset,
					QuoteAsset: stored.Order.QuoteAsset,
					Side:       stored.Order.Side,
					Quantity:   event.Payload.FillQuantity,
					Price:      event.Payload.FillPrice,
					LockPrice:  event.Payload.FillLockPrice,
					Commission: event.Payload.Commission,
				}
			}
			if event.Payload.RejectCode != "" || event.Payload.RejectReason != "" {
				persistence.Blocks = []domain.ExecutionAccountBlock{{
					Account: stored.Order.Account,
					Policy:  event.Payload.RejectPolicy,
					Code:    event.Payload.RejectCode,
					Reason:  event.Payload.RejectReason,
					Details: event.Payload.RejectDetails,
				}}
			}
			order := stored.Order
			order.Status = persistence.OrderStatus
			order.Source = caller.Source
			order.Principal = caller.Principal
			p, err := s.buildExecutionReportEventPayload(
				order, event, persistence,
			)
			return p, err == nil, err
		},
		func(got Attestation, p domain.ApprovalPayload) {
			priority := 1
			if p.Result != nil && p.Result.FillQuantity != "" {
				priority = 2
			}
			if priority > attPriority {
				att = got
				attPriority = priority
			}
		},
	)
	result, err := attesting.ApplyExecutionReportWithAttestation(
		ctx, in, caller, attest)
	if err != nil {
		return engine.ExecutionReportResult{}, Attestation{}, err
	}
	if att.Token == "" {
		return engine.ExecutionReportResult{}, Attestation{},
			missingAttestationError(domain.AttestationRequestExecutionReport)
	}
	return result, att, nil
}

func (s *Service) buildExecutionReportEventPayload(
	order domain.Order,
	event domain.OrderEvent,
	persistence engine.ExecutionReportPersistence,
) (domain.ApprovalPayload, error) {
	if event.Payload.ExecutionReport == nil {
		return domain.ApprovalPayload{}, fmt.Errorf(
			"backend: execution-report event has no request snapshot",
		)
	}
	payload, err := s.buildExecutionReportPayload(order, persistence)
	if err != nil {
		return domain.ApprovalPayload{}, err
	}
	payload.ExecutionReport = event.Payload.ExecutionReport
	return payload, nil
}

// GetOrder returns the order with its events and trades, addressed by the
// order's opaque external id. Each event carries its own 1:1 EventAttestation
// (when issued); there is no order-level approval envelope. It maps a missing
// order onto the node's domain.ErrNotFound.
func (s *Service) GetOrder(
	ctx context.Context, id string,
) (domain.OrderDetail, error) {
	order, err := domain.ParseExternalID(id)
	if err != nil {
		return domain.OrderDetail{}, err
	}
	n := s.node
	detail, err := n.GetOrder(ctx, order)
	if err != nil {
		return domain.OrderDetail{}, err
	}
	detail.DisplayPrice, err = s.orderDisplayPrice(detail.Order)
	if err != nil {
		return domain.OrderDetail{}, err
	}
	return detail, nil
}

// ListOrders returns the most recent n orders, optionally narrowed to a
// non-empty account and/or source, newest first.
func (s *Service) ListOrders(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Order, error) {
	page, err := s.listOrderRows(ctx, store.OrderListFilter{
		Account: account,
		Source:  source,
		Sort:    store.SortSpec{Column: "at", Descending: true},
		Page:    store.PageSpec{Limit: n},
	})
	if err != nil {
		return nil, err
	}
	orders := make([]domain.Order, 0, len(page.Rows))
	for _, row := range page.Rows {
		orders = append(orders, row.Order)
	}
	return orders, nil
}

// ListOrderRows returns order rows matching filter.
func (s *Service) ListOrderRows(
	ctx context.Context, filter store.OrderListFilter,
) (OrderListPage, error) {
	page, err := s.listOrderRows(ctx, filter)
	if err != nil {
		return OrderListPage{}, err
	}
	return s.enrichOrderListPage(page)
}

func (s *Service) listOrderRows(
	ctx context.Context, filter store.OrderListFilter,
) (store.OrderListPage, error) {
	if filter.Account != "" {
		if err := domain.ValidateAccountID(filter.Account); err != nil {
			return store.OrderListPage{}, err
		}
	}
	return s.node.ListOrderRows(ctx, filter)
}

// ListTrades returns the most recent n trades, optionally narrowed to a
// non-empty account and/or source, newest first.
func (s *Service) ListTrades(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Trade, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	trades, err := s.node.ListTrades(ctx, account, source, n)
	if err != nil {
		return nil, err
	}
	sortTradesNewestFirst(trades)
	return trades, nil
}

// sortOrdersNewestFirst orders orders newest first by timestamp, breaking ties
// on the opaque external id (descending). The store orders by the RFC3339Nano
// text, which is not chronological within one second when the fractional
// precision differs.
func sortOrdersNewestFirst(orders []domain.Order) {
	sort.Slice(orders, func(i, j int) bool {
		if !orders[i].At.Equal(orders[j].At) {
			return orders[i].At.After(orders[j].At)
		}
		return orders[i].ExternalID.String() > orders[j].ExternalID.String()
	})
}

// sortTradesNewestFirst orders trades newest first, as sortOrdersNewestFirst
// orders orders.
func sortTradesNewestFirst(trades []domain.Trade) {
	sort.Slice(trades, func(i, j int) bool {
		if !trades[i].At.Equal(trades[j].At) {
			return trades[i].At.After(trades[j].At)
		}
		return trades[i].ExternalID.String() > trades[j].ExternalID.String()
	})
}

// ListTradeRows returns trades with DB-side filters and counts.
func (s *Service) ListTradeRows(
	ctx context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	target, ok := s.node.(tradeRowNode)
	if !ok {
		return store.TradeListPage{}, fmt.Errorf(
			"backend: node list trade rows: %w", domain.ErrNotImplemented,
		)
	}
	return target.ListTradeRows(ctx, filter)
}
