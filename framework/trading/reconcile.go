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

package trading

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
)

const (
	missingOrderGrace     = 2 * time.Minute
	maxVenueDecimalLength = 40
)

func parseVenueDecimal(value, field string, positive, signed bool) (decimal.Decimal, error) {
	if len(value) == 0 || len(value) > maxVenueDecimalLength {
		return decimal.Zero, fmt.Errorf("trading: invalid %s length: %w", field, domain.ErrInvalid)
	}
	point := false
	start := 0
	if signed && value[0] == '-' {
		start = 1
	}
	for i, digit := range value {
		if i < start {
			continue
		}
		if digit == '.' && !point && i > start && i < len(value)-1 {
			point = true
			continue
		}
		if digit < '0' || digit > '9' {
			return decimal.Zero, fmt.Errorf("trading: %s must be a plain decimal with an allowed sign: %w", field, domain.ErrInvalid)
		}
	}
	d, err := decimal.NewFromString(value)
	if err != nil || (positive && !d.IsPositive()) {
		return decimal.Zero, fmt.Errorf("trading: invalid %s: %w", field, domain.ErrInvalid)
	}
	return d, nil
}

func cumulative(detail domain.OrderDetail) (decimal.Decimal, error) {
	total := decimal.Zero
	for _, trade := range detail.Trades {
		qty, err := positiveDecimal(trade.Quantity, "stored trade quantity")
		if err != nil {
			return decimal.Zero, err
		}
		total = total.Add(qty)
	}
	return total, nil
}

func positiveDecimal(value, field string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(value)
	if err != nil || !d.IsPositive() {
		return decimal.Zero, fmt.Errorf("trading: invalid %s: %w", field, domain.ErrInvalid)
	}
	return d, nil
}

func fillQuantities(fill Fill) (qty, cum decimal.Decimal, err error) {
	qty, err = parseVenueDecimal(fill.Quantity, "fill quantity", false, false)
	if err != nil {
		return
	}
	cum, err = parseVenueDecimal(fill.CumQuantity, "cumulative fill quantity", false, false)
	if err != nil {
		return
	}
	if _, err = parseVenueDecimal(fill.Price, "fill price", true, false); err != nil {
		return
	}
	if _, err = parseVenueDecimal(fill.LeavesQuantity, "fill leaves quantity", false, false); err != nil {
		return
	}
	if fill.Commission != nil {
		if _, err = parseVenueDecimal(fill.Commission.Amount, "fill commission amount", false, true); err != nil {
			return
		}
		if fill.Commission.Currency == "" {
			err = fmt.Errorf("trading: fill commission currency is empty: %w", domain.ErrInvalid)
		}
	}
	return
}

func (w *worker) applyFill(link domain.VenueOrder, fill Fill) error {
	_, cum, err := fillQuantities(fill)
	if err != nil {
		return err
	}
	status := domain.OrderStatusPartiallyFilled
	if fill.Final {
		status = domain.OrderStatusFilled
	}
	err = w.r.sink(context.WithoutCancel(w.r.ctx), domain.ExecutionReportInput{
		ExternalID: reportID(link.ClientOrderID, "fill:"+cum.String()),
		Order:      link.Order, OrderStatus: status,
		FillQuantity: fill.Quantity, FillPrice: fill.Price,
		LeavesQuantity: fill.LeavesQuantity, Commission: fill.Commission,
	})
	if errors.Is(err, domain.ErrAlreadyExists) {
		return nil
	}
	return err
}

func (w *worker) handleEvent(event Event) {
	if event.Kind == EventConnected {
		w.reconcileAll()
		w.intakeFees()
		return
	}
	if event.Kind != EventFill && event.Kind != EventOrder {
		w.log(slog.LevelError, event.Ref.ClientOrderID, "unknown venue event kind", domain.ErrInvalid)
		return
	}
	coid := event.Ref.ClientOrderID
	if coid == "" {
		w.r.logger.WarnContext(context.WithoutCancel(w.r.ctx),
			"ignore venue event without client order id",
			"connection", w.connection.ExternalID,
			"venue_order_id", event.Ref.VenueOrderID)
		return
	}
	link, found, err := w.r.store.FindVenueOrderByClientID(w.r.ctx, w.connection.ExternalID, coid)
	if err != nil {
		w.pending[coid] = struct{}{}
		w.log(slog.LevelError, coid, "find stream venue order", err)
		return
	}
	if !found {
		w.log(slog.LevelDebug, coid, "ignore unknown client order id", nil)
		return
	}
	if event.Kind == EventOrder {
		w.trackReconcile(link)
		return
	}
	err = w.handleFill(link, event.Fill)
	if err != nil {
		w.finishAttempt(link.ClientOrderID, err)
	}
}

func (w *worker) handleFill(link domain.VenueOrder, fill Fill) error {
	detail, err := w.r.store.GetOrder(w.r.ctx, link.Order)
	if err != nil {
		return err
	}
	officerCum, err := cumulative(detail)
	if err != nil {
		return err
	}
	qty, venueCum, err := fillQuantities(fill)
	if err != nil {
		return err
	}
	if venueCum.LessThanOrEqual(officerCum) {
		return nil
	}
	if domain.OrderStatusTerminal(detail.Order.Status) {
		return fmt.Errorf("trading: venue cumulative qty %s exceeds Officer cumulative qty %s: %w",
			venueCum, officerCum, domain.ErrTerminalOrder)
	}
	if !venueCum.Sub(qty).Equal(officerCum) {
		w.trackReconcile(link)
		return nil
	}
	return w.applyFill(link, fill)
}

func (w *worker) reconcileAll() {
	links, err := w.r.store.ListOpenVenueOrders(w.r.ctx, w.connection.ExternalID)
	w.reconcileAllPending = err != nil
	if err != nil {
		w.log(slog.LevelError, "", "list open venue orders", err)
		return
	}
	for _, link := range links {
		if w.r.ctx.Err() != nil {
			return
		}
		w.trackReconcile(link)
	}
}

func (w *worker) reconcileClientID(coid string) {
	link, found, err := w.r.store.FindVenueOrderByClientID(w.r.ctx, w.connection.ExternalID, coid)
	if err != nil {
		w.log(slog.LevelError, coid, "find pending venue order", err)
		return
	}
	if !found {
		delete(w.pending, coid)
		return
	}
	w.trackReconcile(link)
}

func (w *worker) trackReconcile(link domain.VenueOrder) {
	w.finishAttempt(link.ClientOrderID, w.reconcile(link))
}

func (w *worker) finishAttempt(coid string, err error) {
	if errors.Is(err, domain.ErrTerminalOrder) {
		w.log(slog.LevelError, coid, "venue execution after Officer order finished", err)
		delete(w.pending, coid)
	} else if err != nil {
		w.pending[coid] = struct{}{}
		w.log(slog.LevelError, coid, "venue order remains pending", err)
	} else {
		delete(w.pending, coid)
	}
}

func validateSnapshot(snapshot Snapshot) (decimal.Decimal, error) {
	switch snapshot.Status {
	case VenueStatusOpen, VenueStatusFilled, VenueStatusCancelled, VenueStatusReplaced:
	default:
		return decimal.Zero, fmt.Errorf("trading: unknown snapshot status: %w", domain.ErrInvalid)
	}
	filled, err := parseVenueDecimal(snapshot.FilledQuantity, "snapshot filled quantity", false, false)
	if err != nil {
		return decimal.Zero, err
	}
	previous := decimal.Zero
	for i, fill := range snapshot.Fills {
		qty, cum, err := fillQuantities(fill)
		if err != nil {
			return decimal.Zero, err
		}
		if !cum.Equal(previous.Add(qty)) {
			return decimal.Zero, fmt.Errorf("trading: incomplete snapshot at fill %d: %w", i, domain.ErrUpstream)
		}
		if fill.Final && (i != len(snapshot.Fills)-1 || snapshot.Status != VenueStatusFilled) {
			return decimal.Zero, fmt.Errorf("trading: premature final fill in snapshot: %w", domain.ErrUpstream)
		}
		previous = cum
	}
	if !previous.Equal(filled) {
		return decimal.Zero, fmt.Errorf("trading: incomplete snapshot cumulative quantity: %w", domain.ErrUpstream)
	}
	return filled, nil
}

func (w *worker) reconcile(link domain.VenueOrder) error {
	ctx := w.r.ctx
	detail, err := w.r.store.GetOrder(ctx, link.Order)
	if err != nil {
		return err
	}
	if !link.SendAttempted {
		claimed, err := w.r.store.ExecutionReportExists(ctx, reportID(link.ClientOrderID, "claim"))
		if err != nil {
			return err
		}
		if !claimed {
			return w.r.store.DeleteVenueOrder(ctx, link.Order)
		}
		if err := eligible(detail, reportID(link.ClientOrderID, "claim")); err != nil {
			return w.r.store.DeleteVenueOrder(ctx, link.Order)
		}
		return w.cancelOrder(link)
	}
	if domain.OrderStatusTerminal(detail.Order.Status) {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, connectorTimeout)
	snapshot, found, err := w.connector.Lookup(lookupCtx, OrderRef{
		ClientOrderID: link.ClientOrderID, VenueOrderID: link.VenueOrderID,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("trading: venue lookup: %w", err)
	}
	if !found {
		if link.VenueOrderID != "" {
			return fmt.Errorf("trading: acknowledged order missing at venue: %w", domain.ErrUpstream)
		}
		if time.Since(link.CreatedAt) <= missingOrderGrace {
			return fmt.Errorf("trading: unacknowledged order missing within send grace: %w", domain.ErrUpstream)
		}
		return w.cancelOrder(link)
	}
	if snapshot.VenueOrderID == "" {
		return fmt.Errorf("trading: snapshot has no venue order id: %w", domain.ErrUpstream)
	}
	if link.VenueOrderID == "" {
		if err := w.r.store.SetVenueOrderID(ctx, link.Order, snapshot.VenueOrderID); err != nil {
			return err
		}
		link.VenueOrderID = snapshot.VenueOrderID
	} else if link.VenueOrderID != snapshot.VenueOrderID {
		return fmt.Errorf("trading: snapshot venue order id changed: %w", domain.ErrUpstream)
	}
	venueCum, err := validateSnapshot(snapshot)
	if err != nil {
		return err
	}
	officerCum, err := cumulative(detail)
	if err != nil {
		return err
	}
	for _, fill := range snapshot.Fills {
		qty, cum, err := fillQuantities(fill)
		if err != nil {
			return err
		}
		if cum.LessThanOrEqual(officerCum) {
			continue
		}
		if !cum.Sub(qty).Equal(officerCum) {
			return fmt.Errorf("trading: Officer cumulative does not match next venue fill: %w", domain.ErrUpstream)
		}
		if err := w.applyFill(link, fill); err != nil {
			return err
		}
		// Re-read even for ErrAlreadyExists: only persisted core trades count.
		detail, err = w.r.store.GetOrder(ctx, link.Order)
		if err != nil {
			return err
		}
		officerCum, err = cumulative(detail)
		if err != nil {
			return err
		}
		if !officerCum.Equal(cum) {
			return fmt.Errorf("trading: fill report did not advance Officer cumulative: %w", domain.ErrUpstream)
		}
	}
	if !officerCum.Equal(venueCum) {
		return fmt.Errorf("trading: terminal barrier cumulative mismatch: %w", domain.ErrUpstream)
	}
	switch snapshot.Status {
	case VenueStatusCancelled:
		return w.cancelOrder(link)
	case VenueStatusFilled:
		detail, err = w.r.store.GetOrder(ctx, link.Order)
		if err != nil {
			return err
		}
		if !domain.OrderStatusTerminal(detail.Order.Status) {
			return fmt.Errorf("trading: venue filled but Officer order is still open: %w", domain.ErrUpstream)
		}
	case VenueStatusReplaced:
		w.log(slog.LevelError, link.ClientOrderID,
			"venue replaced order; Officer does not follow replacements; operator attention required",
			errors.New(snapshot.Reason))
	}
	return nil
}
