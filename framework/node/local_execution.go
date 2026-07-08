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
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// ApplyExecutionReport applies every report through the engine, then persists
// the execution-report persistence in one atomic RecordOrderSettlement transaction:
// report events, optional trade, per-asset balances, engine-block UPDATEs, and
// the reflected status/leaves. The node does not commit or roll back held
// reservations around the report; any account effects must come from the engine
// persistence. The observational block-audit row is written post-commit. A report
// against an account Officer does not know yet auto-creates it, so it is
// processed rather than rejected as invalid.
func (n *localNode) ApplyExecutionReport(
	ctx context.Context, key Key, in domain.ExecutionReportInput, caller domain.Caller,
) (engine.ExecutionReportResult, error) {
	return n.applyExecutionReport(ctx, key, in, caller, nil)
}

func (n *localNode) ApplyExecutionReportWithAttestation(
	ctx context.Context,
	key Key,
	in domain.ExecutionReportInput,
	caller domain.Caller,
	attest store.EventAttestor,
) (engine.ExecutionReportResult, error) {
	return n.applyExecutionReport(ctx, key, in, caller, attest)
}

func (n *localNode) applyExecutionReport(
	ctx context.Context,
	key Key,
	in domain.ExecutionReportInput,
	caller domain.Caller,
	attest store.EventAttestor,
) (engine.ExecutionReportResult, error) {
	status := domain.ExecutionReportTargetStatus(in)
	if !domain.OrderStatusSupported(status) {
		return engine.ExecutionReportResult{}, fmt.Errorf(
			"invalid execution report status %q: %w", status, domain.ErrInvalid)
	}
	// The report is a fact from the venue; caller cancellation must not cancel it.
	ctx = context.WithoutCancel(ctx)

	routeDetail, err := n.realm.GetOrder(ctx, in.Order)
	if err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("get execution report order: %w", err)
	}
	account := routeDetail.Order.Account

	// Register the order account and assets and rebuild pre-lane so the resolver
	// knows them before RunAccountSynchronized resolves the account.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, account, "execution report", caller,
		routeDetail.Order.BaseAsset, routeDetail.Order.QuoteAsset,
	); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, account)
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	defer done()

	var result engine.ExecutionReportResult
	if err := eng.RunAccountSynchronized(ctx, account, func(lane engine.AccountLane) error {
		detail, err := n.realm.GetOrder(ctx, in.Order)
		if err != nil {
			return fmt.Errorf("get execution report order: %w", err)
		}
		if err := domain.RequireOrderModifiable(detail.Order.Status, in.Force); err != nil {
			return err
		}
		forcedTerminalBypass := in.Force && domain.OrderStatusTerminal(detail.Order.Status)
		in.Account = detail.Order.Account
		in.BaseAsset = detail.Order.BaseAsset
		in.QuoteAsset = detail.Order.QuoteAsset
		in.Side = detail.Order.Side
		if len(in.Lock) == 0 && len(detail.Order.Lock) > 0 {
			in.Lock = detail.Order.Lock
		}

		reservationApprovalID := ""
		reservationIntentState := domain.ReservationIntentState("")
		if detail.Order.Status == domain.OrderStatusAccepted {
			intent, found, err := n.realm.GetOpenReservationIntentByOrder(ctx, in.Order)
			if err != nil {
				return fmt.Errorf("get open reservation intent: %w", err)
			}
			if found {
				reservationApprovalID = intent.ApprovalID
				if executionReportCarriesFill(in) {
					reservationIntentState = domain.ReservationIntentStateCommitted
				} else if domain.OrderStatusTerminal(status) {
					reservationIntentState = domain.ReservationIntentStateRolledBack
				}
			}
		}

		applied, err := lane.ApplyExecutionReport(ctx, in)
		if err != nil {
			return fmt.Errorf("apply execution report: %w", err)
		}
		result = applied

		if result.Persistence == nil {
			return fmt.Errorf("apply execution report returned no persistence write set: %w", domain.ErrInvalid)
		}
		persistence := stampExecutionReportPersistence(*result.Persistence, caller)
		settlement := domain.OrderSettlement{
			Account:     in.Account,
			Order:       in.Order,
			OrderStatus: persistence.OrderStatus,
			Leaves:      persistence.Leaves,
			Balances:    persistence.Balances,
			Events:      persistence.Events,
			Trade:       persistence.Trade,
			Blocks:      accountBlockSettlementsFrom(in.Order, persistence.Blocks),
		}
		if reservationApprovalID != "" && reservationIntentState != "" {
			settlement.ReservationApprovalID = reservationApprovalID
			settlement.ReservationIntentState = reservationIntentState
		}
		if err := recordOrderSettlementWithAttestation(
			ctx, n.realm, settlement, attest,
		); err != nil {
			return n.fatalPostEnginePersistence(
				"record execution report",
				accountID,
				fmt.Errorf("record execution report: %w", err),
			)
		}

		if err := n.mirrorEngineBlocksAudit(ctx, in.Order, result.Blocks); err != nil {
			return n.fatalPostEnginePersistence(
				"audit execution report engine blocks", accountID, err,
			)
		}

		detailText := executionReportDetail(in, status, len(result.Blocks))
		if forcedTerminalBypass {
			detailText += " forced=true"
		}
		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:  domain.AuditActionExecutionReport,
			Account: in.Account,
			Detail:  detailText,
		}); err != nil {
			return n.fatalPostEnginePersistence(
				"audit execution report",
				accountID,
				fmt.Errorf("audit execution report: %w", err),
			)
		}
		return nil
	}); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	return result, nil
}

func executionReportCarriesFill(in domain.ExecutionReportInput) bool {
	return in.FillQuantity != "" && in.FillPrice != ""
}

// PersistEventAttestation stamps the signed attestation envelope onto the
// order-history event named by its external id. It is write-once:
// store.PutEventAttestation sets the envelope only when none is present, so a
// retry never clobbers it. The event is already durable, so this mutation only
// adds the envelope; it never touches money or status.
func (n *localNode) PersistEventAttestation(
	ctx context.Context, _ Key, eventID domain.ExternalID, att domain.EventAttestation,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.PutEventAttestation(ctx, eventID, att); err != nil {
		return fmt.Errorf("persist event attestation: %w", err)
	}
	return nil
}

// GetOrder returns the order with its 1:1 signed approval, events and trades,
// addressed by the order's opaque external id.
func (n *localNode) GetOrder(
	ctx context.Context, order domain.ExternalID,
) (domain.OrderDetail, error) {
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.OrderDetail{}, fmt.Errorf("get order: %w", err)
	}
	return detail, nil
}

// ListOrders returns the most recent n orders for an account.
func (n *localNode) ListOrders(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.Order, error) {
	orders, err := n.realm.ListOrders(ctx, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	return orders, nil
}

// ListOrderRows returns orders matching filter.
func (n *localNode) ListOrderRows(
	ctx context.Context, filter store.OrderListFilter,
) (store.OrderListPage, error) {
	orders, err := n.realm.ListOrderRows(ctx, filter)
	if err != nil {
		return store.OrderListPage{}, fmt.Errorf("list order rows: %w", err)
	}
	return orders, nil
}

// ListAllOrders returns every matching order, newest first.
func (n *localNode) ListAllOrders(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Order, error) {
	orders, err := n.realm.ListAllOrders(ctx, account, source)
	if err != nil {
		return nil, fmt.Errorf("list all orders: %w", err)
	}
	return orders, nil
}

// CountOrders returns the total number of orders recorded in the realm.
func (n *localNode) CountOrders(ctx context.Context) (int, error) {
	count, err := n.realm.CountOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("count orders: %w", err)
	}
	return count, nil
}

// CountActiveOrders returns orders in the working lifecycle set.
func (n *localNode) CountActiveOrders(ctx context.Context) (int, error) {
	count, err := n.realm.CountActiveOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("count active orders: %w", err)
	}
	return count, nil
}

// CountOrdersSince returns the number of orders in the realm at or after since.
func (n *localNode) CountOrdersSince(
	ctx context.Context, since time.Time,
) (int, error) {
	count, err := n.realm.CountOrdersSince(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("count orders since: %w", err)
	}
	return count, nil
}

// ListOrderEvents returns all events for the identified order, oldest first.
func (n *localNode) ListOrderEvents(
	ctx context.Context, order domain.ExternalID,
) ([]domain.OrderEvent, error) {
	events, err := n.realm.ListOrderEvents(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("list order events: %w", err)
	}
	return events, nil
}

// ListTrades returns the most recent n trades for an account.
func (n *localNode) ListTrades(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.Trade, error) {
	trades, err := n.realm.ListTrades(ctx, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list trades: %w", err)
	}
	return trades, nil
}

// ListAllTrades returns every matching trade, newest first.
func (n *localNode) ListAllTrades(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Trade, error) {
	trades, err := n.realm.ListAllTrades(ctx, account, source)
	if err != nil {
		return nil, fmt.Errorf("list all trades: %w", err)
	}
	return trades, nil
}

// ListTradeRows returns trades matching filter.
func (n *localNode) ListTradeRows(
	ctx context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	page, err := n.realm.ListTradeRows(ctx, filter)
	if err != nil {
		return store.TradeListPage{}, fmt.Errorf("list trade rows: %w", err)
	}
	return page, nil
}

// CheckOrder delegates the non-mutating pre-trade dry-run to the engine. The
// check mutates no state and writes no audit row, but it still runs through the
// account lane so dry-runs cannot observe account state out of order with fills.
func (n *localNode) CheckOrder(
	ctx context.Context, _ Key, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	eng, done := n.beginLaneRead()
	defer done()
	var out domain.CheckResult
	if err := eng.RunAccountSynchronized(ctx, probe.Account, func(lane engine.AccountLane) error {
		var err error
		out, err = lane.CheckOrder(ctx, probe)
		return err
	}); err != nil {
		return domain.CheckResult{}, err
	}
	return out, nil
}

// Close stops the engine and closes the store. It is idempotent: the engine's
// Stop and the store's Close are both idempotent.
func (n *localNode) Close() error {
	n.mutate.Lock()
	defer n.mutate.Unlock()
	n.engineMu.Lock()
	n.engine.Stop()
	n.engineMu.Unlock()
	if err := n.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}
