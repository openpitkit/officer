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
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scmhub/ibapi"
	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/ib"
)

const (
	ibReconnectMin = 100 * time.Millisecond
	ibReconnectMax = 5 * time.Second
)

var errIBRequestNotIssued = errors.New("ib: request not issued")

type ibTradingOrder struct {
	ref              string
	permID           int64
	total            decimal.Decimal
	totalKnown       bool
	filled           decimal.Decimal
	status           string
	changed          bool
	acknowledged     bool
	rejected         error
	reason           string
	rejectedAfterAck bool
}

type ibTradingExecution struct {
	execution  ibapi.Execution
	commission *domain.Commission
	emitted    bool
	diagnosed  bool
}

type ibDetailsResult struct {
	done    chan struct{}
	details []*ibapi.ContractDetails
	err     error
}

type ibTradingSession struct {
	ibapi.Wrapper
	ctx               context.Context
	cancel            context.CancelFunc
	owner             *ibConnector
	client            ibTradingClient
	connectionTime    string
	readiness         *ib.Readiness
	collector         ib.ContractDetailsCollector
	mu                sync.Mutex
	changed           chan struct{}
	nextID            int64
	clientID          int64
	openRequests      int
	completedRequests int
	reconnected       bool
	managed           []string
	managedKnown      bool
	orders            map[int64]*ibTradingOrder
	executions        map[string]*ibTradingExecution
	commissions       map[string]*domain.Commission
	details           map[int64]*ibDetailsResult
	lookupToken       chan struct{}
	lookup            *ibLookupQuery
}

func newIBTradingSession(ctx context.Context, owner *ibConnector) *ibTradingSession {
	ctx, cancel := context.WithCancel(ctx)
	s := &ibTradingSession{
		ctx: ctx, cancel: cancel, owner: owner, readiness: ib.NewReadiness(),
		changed: make(chan struct{}), clientID: owner.clientID, nextID: 1,
		orders:      make(map[int64]*ibTradingOrder),
		executions:  make(map[string]*ibTradingExecution),
		commissions: make(map[string]*domain.Commission),
		details:     make(map[int64]*ibDetailsResult), lookupToken: make(chan struct{}, 1),
	}
	s.lookupToken <- struct{}{}
	return s
}

// signal broadcasts a state change to Send, Lookup and the event pump. All
// callers hold mu; a single-consumer notification would lose wakeups here.
func (s *ibTradingSession) signal() { close(s.changed); s.changed = make(chan struct{}) }

func (s *ibTradingSession) order(id int64) *ibTradingOrder {
	if id >= s.nextID {
		s.nextID = id + 1
	}
	o := s.orders[id]
	if o == nil {
		o = &ibTradingOrder{}
		s.orders[id] = o
	}
	return o
}

func (s *ibTradingSession) NextValidID(id int64) {
	s.mu.Lock()
	if id > s.nextID {
		s.nextID = id
	}
	s.signal()
	s.mu.Unlock()
	s.readiness.Confirm()
}

func (s *ibTradingSession) ConnectAck() {}

func (s *ibTradingSession) ConnectionClosed() { s.cancel() }

func (s *ibTradingSession) ManagedAccounts(accounts []string) {
	s.mu.Lock()
	s.managed = nil
	for _, account := range accounts {
		if account = strings.TrimSpace(account); account != "" {
			s.managed = append(s.managed, account)
		}
	}
	s.managedKnown = true
	s.signal()
	s.mu.Unlock()
}

func (s *ibTradingSession) request(ctx context.Context, call func()) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", errIBRequestNotIssued, err)
	}
	if err := s.ctx.Err(); err != nil {
		return fmt.Errorf("%w: session ended: %w", errIBRequestNotIssued, err)
	}
	done := make(chan struct{})
	// ibapi cannot interrupt a blocked enqueue, so its goroutine may survive cancellation.
	go func() { call(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	case <-s.ctx.Done():
		s.cancel()
		return fmt.Errorf("ib: request session ended: %w", s.ctx.Err())
	}
}

func (s *ibTradingSession) accounts(ctx context.Context) ([]string, error) {
	if err := s.request(ctx, s.client.ReqManagedAccts); err != nil {
		return nil, err
	}
	for {
		s.mu.Lock()
		known, accounts, changed := s.managedKnown, append([]string(nil), s.managed...), s.changed
		s.mu.Unlock()
		if known {
			return accounts, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.ctx.Done():
			return nil, errors.New("ib: session ended waiting for managed accounts")
		case <-changed:
		}
	}
}

func (s *ibTradingSession) contractDetails(ctx context.Context, contract *ibapi.Contract) ([]*ibapi.ContractDetails, error) {
	ctx, cancel := context.WithTimeout(ctx, s.owner.timeout)
	defer cancel()
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	result := &ibDetailsResult{done: make(chan struct{})}
	s.details[id] = result
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.details, id)
		s.collector.Finish(id)
		s.mu.Unlock()
	}()
	if err := s.request(ctx, func() { s.client.ReqContractDetails(id, contract) }); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, errors.New("ib: contract resolution session ended")
	case <-result.done:
		return result.details, result.err
	}
}

func (s *ibTradingSession) ContractDetails(id int64, detail *ibapi.ContractDetails) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.details[id] != nil {
		s.collector.Add(id, detail)
	}
}

func (s *ibTradingSession) ContractDetailsEnd(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	details, first := s.collector.Finish(id)
	if result := s.details[id]; result != nil && first {
		result.details = details
		close(result.done)
	}
}

// ibDefinitiveRejection deliberately has a narrow allowlist: 201 explicitly
// rejects an order, 200 has no security definition, 321 is validation failure.
// Processing error 322, duplicate id 103, cancellation 202 and transport errors
// remain unknown outcomes. Never infer rejection from arbitrary venue text.
func ibDefinitiveRejection(code int64) bool {
	switch code {
	case 200, 201, 321:
		return true
	default:
		return false
	}
}

func (s *ibTradingSession) Error(id ibapi.TickerID, _ int64, code int64, message, _ string) {
	s.mu.Lock()
	if result := s.details[id]; result != nil {
		_, first := s.collector.Finish(id)
		if first {
			if code != 200 {
				result.err = fmt.Errorf("ib: contract request error %d: %s", code, message)
			}
			close(result.done)
		}
	}
	if o := s.orders[id]; o != nil && ibDefinitiveRejection(code) {
		if o.acknowledged {
			o.reason, o.changed = message, true
			o.rejectedAfterAck = true
		} else {
			o.rejected = fmt.Errorf("%w: IB %d: %s", fwtrading.ErrRejected, code, message)
		}
	}
	if s.lookup != nil && id == s.lookup.reqID && !ib.InformationalError(code) {
		s.lookup.err = fmt.Errorf("ib: execution query error %d: %s", code, message)
	}
	if code == 1101 {
		s.reconnected = true
	}
	s.signal()
	s.mu.Unlock()
	if s.owner.reportDiag != nil {
		diag := health.Diagnostic{Level: health.DiagError, Kind: health.DiagKindProvider,
			Code: health.CodeConnectionError, Title: "Interactive Brokers API error",
			Detail: fmt.Sprintf("IB API error %d: %s", code, strings.TrimSpace(message))}
		if ib.InformationalError(code) {
			diag.Level, diag.Routine = health.DiagInfo, true
		}
		if ib.RecoverableConnectionError(code) {
			diag.Level = health.DiagWarn
		}
		s.owner.reportDiag(diag)
	}
	if ib.FatalError(code) || code == 326 || code == 503 {
		s.cancel()
	}
}

// PendingSubmit, PreSubmitted, Submitted, ApiPending and PendingCancel are
// open; Filled is filled; Cancelled/ApiCancelled are cancelled. Inactive stays
// open: IB also uses it for orders awaiting activation, not only rejections.
func ibVenueStatus(status string) (fwtrading.VenueStatus, bool) {
	switch status {
	// Inactive can mean a held order; only a definitive rejection makes it terminal.
	case "PendingSubmit", "PreSubmitted", "Submitted", "ApiPending", "PendingCancel", "Inactive":
		return fwtrading.VenueStatusOpen, true
	case "Filled":
		return fwtrading.VenueStatusFilled, true
	case "Cancelled", "ApiCancelled":
		return fwtrading.VenueStatusCancelled, true
	default:
		return "", false
	}
}

func (s *ibTradingSession) OpenOrder(id ibapi.OrderID, _ *ibapi.Contract, order *ibapi.Order, state *ibapi.OrderState) {
	if order.ClientID != s.clientID {
		return
	}
	s.recordOrder(id, order, state)
}

func (s *ibTradingSession) recordOrder(id int64, order *ibapi.Order, state *ibapi.OrderState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.order(id)
	if order.OrderRef != "" {
		o.ref = order.OrderRef
	}
	if order.PermID > 0 {
		o.permID = order.PermID
	}
	o.acknowledged = true
	if qty, err := domain.ParseOpenQuantity(order.TotalQuantity.String()); err == nil {
		o.total, o.totalKnown = qty, true
	}
	if filled, err := domain.ParseOpenQuantity(order.FilledQuantity.String()); err == nil && filled.GreaterThan(o.filled) {
		o.filled = filled
	}
	if state.Status == "Filled" && o.totalKnown && o.total.GreaterThan(o.filled) {
		o.filled = o.total
	}
	if o.status != state.Status {
		o.status, o.changed = state.Status, true
	}
	if s.lookup != nil {
		s.lookup.open[id] = true
	}
	s.signal()
}

func (s *ibTradingSession) OrderStatus(id ibapi.OrderID, status string, filled, remaining ibapi.Decimal, _ float64, permID, _ int64, _ float64, clientID int64, _ string, _ float64) {
	if clientID != s.clientID {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.order(id)
	o.acknowledged = true
	if permID > 0 {
		o.permID = permID
	}
	qty, qtyErr := domain.ParseOpenQuantity(filled.String())
	leaves, leavesErr := domain.ParseOpenQuantity(remaining.String())
	if qtyErr == nil && leavesErr == nil {
		if qty.GreaterThan(o.filled) {
			o.filled = qty
		}
		o.total, o.totalKnown = qty.Add(leaves), true
	}
	if o.status != status {
		o.status, o.changed = status, true
	}
	s.signal()
}

func (s *ibTradingSession) ExecDetails(reqID int64, _ *ibapi.Contract, execution *ibapi.Execution) {
	if execution.ClientID != s.clientID {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if execution.ExecID == "" {
		return
	}
	o := s.order(execution.OrderID)
	if execution.OrderRef != "" {
		o.ref = execution.OrderRef
	}
	if execution.PermID > 0 {
		o.permID = execution.PermID
	}
	if s.executions[execution.ExecID] == nil {
		s.executions[execution.ExecID] = &ibTradingExecution{execution: *execution, commission: s.commissions[execution.ExecID]}
		delete(s.commissions, execution.ExecID)
	}
	if s.lookup != nil && reqID == s.lookup.reqID {
		s.lookup.executions[execution.ExecID] = true
	}
	s.signal()
}

func (s *ibTradingSession) CommissionAndFeesReport(report ibapi.CommissionAndFeesReport) {
	// UNSET_FLOAT (max float) is an incomplete report, not a huge charge.
	if report.ExecID == "" || report.Currency == "" || math.IsNaN(report.CommissionAndFees) || math.IsInf(report.CommissionAndFees, 0) || report.CommissionAndFees == ibapi.UNSET_FLOAT {
		return
	}
	commission := &domain.Commission{Amount: strconv.FormatFloat(report.CommissionAndFees, 'f', -1, 64), Currency: report.Currency}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.executions[report.ExecID]; e != nil {
		e.commission = commission
	} else {
		s.commissions[report.ExecID] = commission
	}
	s.signal()
}

func ibExecutionFill(e *ibTradingExecution, o *ibTradingOrder, connectionTime string) (fwtrading.Fill, error) {
	if !o.totalKnown || e.commission == nil {
		return fwtrading.Fill{}, errors.New("ib: execution awaits total quantity or commission")
	}
	qty, err := domain.ParseOpenQuantity(e.execution.Shares.String())
	if err != nil || !qty.IsPositive() {
		return fwtrading.Fill{}, errors.New("ib: invalid execution shares")
	}
	cum, err := domain.ParseOpenQuantity(e.execution.CumQty.String())
	if err != nil || cum.LessThan(qty) || cum.GreaterThan(o.total) {
		return fwtrading.Fill{}, errors.New("ib: inconsistent execution cumulative quantity")
	}
	if e.execution.Price <= 0 || math.IsNaN(e.execution.Price) || math.IsInf(e.execution.Price, 0) || e.execution.Price == ibapi.UNSET_FLOAT {
		return fwtrading.Fill{}, errors.New("ib: invalid execution price")
	}
	at, err := ibExecutionTime(e.execution.Time, connectionTime)
	if err != nil {
		return fwtrading.Fill{}, err
	}
	commission := *e.commission
	return fwtrading.Fill{Quantity: qty.String(), CumQuantity: cum.String(),
		Price:          strconv.FormatFloat(e.execution.Price, 'f', -1, 64),
		LeavesQuantity: o.total.Sub(cum).String(), Final: cum.Equal(o.total),
		Commission: &commission, At: at}, nil
}

func ibExecutionTime(raw, connectionTime string) (time.Time, error) {
	// TWS may return UTC with a hyphen, or an explicitly named timezone.
	if at, err := time.Parse("20060102-15:04:05", raw); err == nil {
		return at, nil
	}
	parts := strings.Fields(raw)
	if len(parts) == 2 {
		// Legacy executions use the TWS timezone advertised by its handshake.
		// An absent/unknown timezone is an error, never the Officer host's zone.
		connectionParts := strings.Fields(connectionTime)
		if len(connectionParts) != 3 {
			return time.Time{}, errors.New("ib: TWS handshake has no execution timezone")
		}
		parts = append(parts, connectionParts[2])
	}
	if len(parts) == 3 {
		zone, err := time.LoadLocation(parts[2])
		if err != nil {
			return time.Time{}, errors.New("ib: unknown execution timezone")
		}
		return time.ParseInLocation("20060102 15:04:05", parts[0]+" "+parts[1], zone)
	}
	return time.Time{}, errors.New("ib: execution time requires an explicit timezone")
}

func (s *ibTradingSession) events() ([]fwtrading.Event, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []fwtrading.Event
	if s.reconnected {
		events = append(events, fwtrading.Event{Kind: fwtrading.EventConnected})
		s.reconnected = false
	}
	for _, o := range s.orders {
		if o.ref != "" && o.changed {
			events = append(events, fwtrading.Event{Kind: fwtrading.EventOrder, Ref: fwtrading.OrderRef{ClientOrderID: o.ref, VenueOrderID: ibPermID(o.permID)}})
			o.changed = false
		}
	}
	var pending []*ibTradingExecution
	for _, e := range s.executions {
		if !e.emitted {
			pending = append(pending, e)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		left, _ := decimal.NewFromString(pending[i].execution.CumQty.String())
		right, _ := decimal.NewFromString(pending[j].execution.CumQty.String())
		return left.LessThan(right)
	})
	blocked := make(map[int64]bool)
	for _, e := range pending {
		id := e.execution.OrderID
		o := s.orders[e.execution.OrderID]
		if blocked[id] || o.ref == "" {
			blocked[id] = true
			continue
		}
		fill, err := ibExecutionFill(e, o, s.connectionTime)
		if err != nil {
			blocked[id] = true
			if e.commission != nil && o.totalKnown && !e.diagnosed {
				s.owner.diagnostic(health.CodeUnparsableData, err.Error())
				e.diagnosed = true
			}
			continue
		}
		e.emitted = true
		events = append(events, fwtrading.Event{Kind: fwtrading.EventFill, Ref: fwtrading.OrderRef{ClientOrderID: o.ref, VenueOrderID: ibPermID(o.permID)}, Fill: fill})
	}
	return events, s.changed
}

func ibPermID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// Send assigns the API order id and waits for an acknowledgement with permId.
// Once PlaceOrder is called, timeout and disconnection leave outcome unknown.
func (c *ibConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	s, err := c.liveSession()
	if err != nil {
		return fwtrading.Ack{}, fmt.Errorf("%w: %v", fwtrading.ErrNotSent, err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	contract, err := s.orderContract(ctx, order)
	if err != nil {
		if errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) {
			return fwtrading.Ack{}, err
		}
		return fwtrading.Ack{}, fmt.Errorf("%w: %v", fwtrading.ErrNotSent, err)
	}
	venueOrder, err := ibOrder(order, contract)
	if err != nil {
		if errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) {
			return fwtrading.Ack{}, err
		}
		return fwtrading.Ack{}, fmt.Errorf("%w: %v", fwtrading.ErrNotSent, err)
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return fwtrading.Ack{}, fmt.Errorf("%w: %v", fwtrading.ErrNotSent, err)
	}
	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return fwtrading.Ack{}, fmt.Errorf("%w: session ended: %v", fwtrading.ErrNotSent, err)
	}
	id := s.nextID
	s.nextID++
	o := s.order(id)
	o.ref = order.ClientOrderID
	s.mu.Unlock()
	if err := s.request(ctx, func() { s.client.PlaceOrder(id, contract, venueOrder) }); err != nil {
		if errors.Is(err, errIBRequestNotIssued) {
			s.mu.Lock()
			delete(s.orders, id)
			s.mu.Unlock()
			return fwtrading.Ack{}, fmt.Errorf("%w: %w", fwtrading.ErrNotSent, err)
		}
		return fwtrading.Ack{}, fmt.Errorf("ib: send outcome unknown: %w", err)
	}
	for {
		s.mu.Lock()
		permID, acknowledged, rejected, changed := o.permID, o.acknowledged, o.rejected, s.changed
		s.mu.Unlock()
		if rejected != nil {
			return fwtrading.Ack{}, rejected
		}
		if acknowledged && permID > 0 {
			return fwtrading.Ack{VenueOrderID: ibPermID(permID)}, nil
		}
		select {
		case <-ctx.Done():
			return fwtrading.Ack{}, fmt.Errorf("ib: send outcome unknown: %w", ctx.Err())
		case <-s.ctx.Done():
			return fwtrading.Ack{}, errors.New("ib: send outcome unknown: session ended")
		case <-changed:
		}
	}
}

// Subscribe owns one reconnecting trading session and requires both reporters.
func (c *ibConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	return c.lifecycle.start(ctx, streamRefusals{
		closed:     "ib: connector closed or already subscribed",
		subscribed: "ib: connector closed or already subscribed",
		reporters:  "ib: stream requires status and diagnostic reporters",
	}, ibReconnectMin, ibReconnectMax, nil, c.streamIB, c.reportStatus, c.reportDiag,
		func(string) string { return "IB trading session disconnected; reconnecting" })
}

func (c *ibConnector) streamIB(ctx context.Context, out chan<- fwtrading.Event) (bool, error) {
	s := newIBTradingSession(ctx, c)
	s.client = c.newClient(s)
	defer func() {
		c.mu.Lock()
		c.session = nil
		c.mu.Unlock()
		s.cancel()
		if err := ib.Disconnect(s.client); err != nil {
			c.diagnostic(health.CodeConnectionError, "TWS trading session teardown failed")
		}
	}()
	if err := ib.Connect(s.ctx, s.client, c.credentials.Host, c.credentials.Port, c.clientID, c.timeout); err != nil {
		return false, err
	}
	if err := s.readiness.Wait(s.ctx, c.timeout); err != nil {
		return false, err
	}
	s.connectionTime = s.client.TWSConnectionTime()
	c.mu.Lock()
	c.session = s
	c.mu.Unlock()
	session, err := connectStream(s.ctx, out, ibReconnectMax, c.reportStatus)
	if err != nil {
		return false, err
	}
	for {
		events, changed := s.events()
		for _, event := range events {
			if err := session.deliver(event); err != nil {
				return session.healthy(), err
			}
		}
		select {
		case <-changed:
		case <-s.ctx.Done():
			return session.healthy(), s.ctx.Err()
		}
	}
}

// Close refuses new work at once, then cancels and waits for the stream loop
// and its output, then cancels and waits for every probe.
func (c *ibConnector) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.lifecycle.close()
	c.mu.Lock()
	c.cancel()
	c.mu.Unlock()
	c.probes.Wait()
}
