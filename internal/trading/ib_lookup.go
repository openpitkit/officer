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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package trading

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/scmhub/ibapi"
	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

type ibLookupQuery struct {
	reqID                                int64
	ref                                  fwtrading.OrderRef
	open                                 map[int64]bool
	completedOrder                       *ibTradingOrder
	executions                           map[string]bool
	openEnd, completedEnd, executionsEnd bool
	openRequested, completedRequested    bool
	err                                  error
}

func (s *ibTradingSession) OpenOrderEnd() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openRequests > 0 {
		s.openRequests--
	}
	if s.lookup != nil && s.lookup.openRequested && s.openRequests == 0 {
		s.lookup.openEnd = true
		s.signal()
	}
}

func (s *ibTradingSession) CompletedOrder(_ *ibapi.Contract, order *ibapi.Order, state *ibapi.OrderState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The completed-order decoder does not supply an API order id. Match
	// orderRef instead of inserting all completed orders under zero/unset.
	if order.ClientID != 0 && order.ClientID != ibapi.UNSET_INT && order.ClientID != s.clientID {
		return
	}
	if s.lookup == nil || order.OrderRef != s.lookup.ref.ClientOrderID {
		return
	}
	status := "Cancelled"
	if state.Status == "Filled" {
		status = "Filled"
	}
	o := &ibTradingOrder{ref: order.OrderRef, permID: order.PermID, status: status, reason: state.CompletedStatus}
	qty, err := domain.ParseOpenQuantity(order.TotalQuantity.String())
	if err != nil {
		s.lookup.err = errors.New("ib: invalid completed order quantity")
		s.signal()
		return
	}
	o.total, o.totalKnown = qty, true
	filled, err := domain.ParseOpenQuantity(order.FilledQuantity.String())
	if err != nil {
		s.lookup.err = errors.New("ib: invalid completed filled quantity")
		s.signal()
		return
	}
	o.filled = filled
	s.lookup.completedOrder = o
	s.signal()
}

func (s *ibTradingSession) CompletedOrdersEnd() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completedRequests > 0 {
		s.completedRequests--
	}
	if s.lookup != nil && s.lookup.completedRequested && s.completedRequests == 0 {
		s.lookup.completedEnd = true
		s.signal()
	}
}

func (s *ibTradingSession) ExecDetailsEnd(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookup != nil && id == s.lookup.reqID {
		s.lookup.executionsEnd = true
		s.signal()
	}
}

var errIBHistoryLimit = errors.New("ib: execution history is limited to since local midnight; acknowledged order or earlier fills are missing")

// lookupSnapshot requires mu. ready=false means commissions or query end
// markers are still outstanding; a history gap never becomes found=false.
func (s *ibTradingSession) lookupSnapshot(q *ibLookupQuery) (snapshot fwtrading.Snapshot, found, ready bool, err error) {
	if q.err != nil {
		return snapshot, false, true, q.err
	}
	if !q.openEnd || !q.completedEnd || !q.executionsEnd {
		return snapshot, false, false, nil
	}
	var o *ibTradingOrder
	for _, candidate := range s.orders {
		if candidate.ref == q.ref.ClientOrderID && candidate.rejectedAfterAck {
			o = candidate
		}
	}
	for id := range q.open {
		if candidate := s.orders[id]; candidate.ref == q.ref.ClientOrderID {
			o = candidate
		}
	}
	if q.completedOrder != nil {
		o = q.completedOrder
	}
	var executions []*ibTradingExecution
	for id := range q.executions {
		e := s.executions[id]
		ref := e.execution.OrderRef
		if ref == "" {
			ref = s.orders[e.execution.OrderID].ref
		}
		if ref == q.ref.ClientOrderID {
			executions = append(executions, e)
		}
	}
	if o == nil && len(executions) == 0 {
		if q.ref.VenueOrderID != "" {
			return snapshot, true, true, errIBHistoryLimit
		}
		return snapshot, false, true, nil
	}
	if o == nil {
		return snapshot, true, true, errors.New("ib: executions found without order total and status")
	}
	if o.permID <= 0 || !o.totalKnown {
		return snapshot, true, true, errors.New("ib: lookup order has no permId or total quantity")
	}
	status, known := ibVenueStatus(o.status)
	if o.rejectedAfterAck {
		status, known = fwtrading.VenueStatusCancelled, true
	}
	if !known {
		return snapshot, true, true, errors.New("ib: unknown venue order status")
	}
	snapshot = fwtrading.Snapshot{VenueOrderID: ibPermID(o.permID), Status: status, FilledQuantity: o.filled.String(), Reason: o.reason}
	sort.Slice(executions, func(i, j int) bool {
		left, _ := decimal.NewFromString(executions[i].execution.CumQty.String())
		right, _ := decimal.NewFromString(executions[j].execution.CumQty.String())
		return left.LessThan(right)
	})
	cum := decimal.Zero
	for _, execution := range executions {
		qty, qtyErr := domain.ParseOpenQuantity(execution.execution.Shares.String())
		next, nextErr := domain.ParseOpenQuantity(execution.execution.CumQty.String())
		if qtyErr != nil || nextErr != nil {
			return snapshot, true, true, errors.New("ib: invalid execution quantity")
		}
		if !cum.Add(qty).Equal(next) {
			return snapshot, true, true, errIBHistoryLimit
		}
		cum = next
	}
	if o.filled.GreaterThan(cum) {
		return snapshot, true, true, errIBHistoryLimit
	}
	for _, execution := range executions {
		if execution.commission == nil {
			return snapshot, true, false, nil
		}
		fill, fillErr := ibExecutionFill(execution, o, s.connectionTime)
		if fillErr != nil {
			return snapshot, true, true, fillErr
		}
		snapshot.Fills = append(snapshot.Fills, fill)
	}
	if cum.GreaterThan(o.filled) {
		// An execution can beat OrderStatus. Its own cumulative quantity is
		// authoritative, but the lifecycle still comes from the venue order.
		snapshot.FilledQuantity = cum.String()
	}
	return snapshot, true, true, nil
}

// Lookup queries live open/completed orders and today's executions. Missing
// acknowledged orders or incomplete history fail visibly and stay pending.
func (c *ibConnector) Lookup(ctx context.Context, ref fwtrading.OrderRef) (fwtrading.Snapshot, bool, error) {
	if ref.ClientOrderID == "" {
		return fwtrading.Snapshot{}, false, errors.New("ib: ClientOrderID is required for lookup")
	}
	if c.reportDiag == nil {
		return fwtrading.Snapshot{}, false, errors.New("ib: lookup requires a diagnostic reporter")
	}
	s, err := c.liveSession()
	if err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return fwtrading.Snapshot{}, false, ctx.Err()
	case <-s.ctx.Done():
		return fwtrading.Snapshot{}, false, errors.New("ib: lookup session ended")
	case <-s.lookupToken:
	}
	defer func() { s.lookupToken <- struct{}{} }()
	s.mu.Lock()
	q := &ibLookupQuery{reqID: s.nextID, ref: ref, open: make(map[int64]bool), executions: make(map[string]bool)}
	s.nextID++
	s.lookup = q
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.lookup = nil; s.mu.Unlock() }()
	s.mu.Lock()
	s.openRequests++
	q.openRequested = true
	s.mu.Unlock()
	if err := s.request(ctx, s.client.ReqOpenOrders); err != nil {
		if errors.Is(err, errIBRequestNotIssued) {
			s.mu.Lock()
			s.openRequests--
			q.openRequested = false
			s.mu.Unlock()
		}
		return fwtrading.Snapshot{}, false, err
	}
	s.mu.Lock()
	s.completedRequests++
	q.completedRequested = true
	s.mu.Unlock()
	if err := s.request(ctx, func() { s.client.ReqCompletedOrders(false) }); err != nil {
		if errors.Is(err, errIBRequestNotIssued) {
			s.mu.Lock()
			s.completedRequests--
			q.completedRequested = false
			s.mu.Unlock()
		}
		return fwtrading.Snapshot{}, false, err
	}
	filter := ibapi.NewExecutionFilter()
	filter.ClientID = s.clientID
	if err := s.request(ctx, func() { s.client.ReqExecutions(q.reqID, filter) }); err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	for {
		s.mu.Lock()
		snapshot, found, ready, err := s.lookupSnapshot(q)
		changed := s.changed
		s.mu.Unlock()
		if err != nil {
			if errors.Is(err, errIBHistoryLimit) {
				c.diagnostic(health.CodeInconsistentData, "IB returns executions only since local midnight. An acknowledged order or part of its execution history is missing; Officer keeps the order pending.")
			} else {
				c.diagnostic(health.CodeUnparsableData, err.Error())
			}
			return fwtrading.Snapshot{}, found, err
		}
		if ready {
			return snapshot, found, nil
		}
		select {
		case <-ctx.Done():
			return fwtrading.Snapshot{}, false, fmt.Errorf("ib: lookup incomplete: %w", ctx.Err())
		case <-s.ctx.Done():
			return fwtrading.Snapshot{}, false, errors.New("ib: lookup session ended")
		case <-changed:
		}
	}
}
