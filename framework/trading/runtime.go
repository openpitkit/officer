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
	"sync"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

const (
	connectorTimeout = 30 * time.Second
	retryInterval    = 15 * time.Second
	feePollInterval  = time.Hour
)

// ReportSink applies an execution report to the core. The application stamps
// the system caller into ctx before invoking its control-plane report method.
type ReportSink func(ctx context.Context, in domain.ExecutionReportInput) error

// AdjustmentSink applies a venue fee through the core adjustment path. The
// application stamps the system caller into ctx and rejects missing accounts.
// An existing external id returns domain.ErrAlreadyExists.
// A core rejection must return an error wrapping ErrAdjustmentRejected with
// the reject reason, even when the core persisted the rejected record.
type AdjustmentSink func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error

// Store is the runtime's subset of store.RealmStore. Its trading methods retain
// that interface's not-found, conflict and already-exists contracts.
type Store interface {
	GetTradingConnection(context.Context, domain.ExternalID) (domain.TradingConnection, bool, error)
	ListTradingConnections(context.Context) ([]domain.TradingConnection, error)
	FindTradingInstrument(context.Context, domain.ExternalID, string, string) (domain.TradingInstrument, bool, error)
	ListTradingAccess(context.Context, domain.AccountID) ([]domain.TradingAccess, error)
	ListTradingAccessForConnection(context.Context, domain.ExternalID) ([]domain.TradingAccess, error)
	ListTradingInstruments(context.Context, domain.ExternalID) ([]domain.TradingInstrument, error)
	EarliestVenueOrderTime(context.Context, domain.ExternalID) (time.Time, bool, error)
	CreateVenueOrder(context.Context, domain.VenueOrder) (domain.VenueOrder, error)
	MarkVenueOrderSendAttempted(context.Context, domain.ExternalID) error
	SetVenueOrderID(context.Context, domain.ExternalID, string) error
	DeleteVenueOrder(context.Context, domain.ExternalID) error
	FindVenueOrderByClientID(context.Context, domain.ExternalID, string) (domain.VenueOrder, bool, error)
	ListOpenVenueOrders(context.Context, domain.ExternalID) ([]domain.VenueOrder, error)
	GetOrder(context.Context, domain.ExternalID) (domain.OrderDetail, error)
	ExecutionReportExists(context.Context, domain.ExternalID) (bool, error)
}

// Runtime owns one serial worker per enabled connection or connection with
// outstanding orders or any venue link. Disabled connections keep a worker
// for tracking and fee intake only, without new sends. Connections must be
// configured before Start.
type Runtime struct {
	registry *Registry
	store    Store
	sink     ReportSink
	adjust   AdjustmentSink
	logger   *slog.Logger

	mu       sync.Mutex
	started  bool
	stopped  bool
	ctx      context.Context
	cancel   context.CancelFunc
	workers  map[domain.ExternalID]*worker
	wg       sync.WaitGroup
	stopDone chan struct{}
}

type worker struct {
	r                   *Runtime
	connection          domain.TradingConnection
	connector           Connector
	events              <-chan Event
	tasks               chan task
	pending             map[string]struct{}
	reconcileAllPending bool
	feeCursor           string
	unavailable         error // Protected by Runtime.mu.
}

type task struct {
	ctx      context.Context
	order    domain.ExternalID
	draft    domain.Order
	dest     domain.TradingDestination
	preCheck bool
	result   chan taskResult
}

type taskResult struct {
	link domain.VenueOrder
	err  error
}

// NewRuntime requires a registry, durable store, both core sinks and a logger.
func NewRuntime(registry *Registry, st Store, sink ReportSink, adjust AdjustmentSink, logger *slog.Logger) (*Runtime, error) {
	if registry == nil || st == nil || sink == nil || adjust == nil || logger == nil {
		return nil, fmt.Errorf("trading: registry, store, report sink, adjustment sink and logger required: %w", domain.ErrInvalid)
	}
	return &Runtime{
		registry: registry, store: st, sink: sink, adjust: adjust, logger: logger,
		workers: make(map[domain.ExternalID]*worker), stopDone: make(chan struct{}),
	}, nil
}

// Start starts workers for enabled connections and connections with outstanding
// orders or any venue link. A disabled connection's worker only tracks orders
// and takes in fees; it sends no new orders.
// Connector failures isolate one connection; only store failures abort startup.
// A runtime can be started once, and cannot be started after Stop.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return fmt.Errorf("trading: runtime already started or stopped: %w", domain.ErrConflict)
	}
	connections, err := r.store.ListTradingConnections(ctx)
	if err != nil {
		return err
	}
	// Finish store reads before starting any worker, so a failed Start has no
	// partially running connections.
	open := make(map[domain.ExternalID][]domain.VenueOrder)
	traded := make(map[domain.ExternalID]bool)
	for _, c := range connections {
		links, err := r.store.ListOpenVenueOrders(ctx, c.ExternalID)
		if err != nil {
			return err
		}
		open[c.ExternalID] = links
		if !c.Enabled && len(links) == 0 {
			_, traded[c.ExternalID], err = r.store.EarliestVenueOrderTime(ctx, c.ExternalID)
			if err != nil {
				return err
			}
		}
	}
	r.started = true
	r.ctx, r.cancel = context.WithCancel(ctx)
	for _, c := range connections {
		if !c.Enabled && len(open[c.ExternalID]) == 0 && !traded[c.ExternalID] {
			continue
		}
		w := &worker{
			r: r, connection: c, tasks: make(chan task),
			pending: make(map[string]struct{}),
		}
		r.workers[c.ExternalID] = w
		for _, link := range open[c.ExternalID] {
			w.pending[link.ClientOrderID] = struct{}{}
		}
		w.connector, err = r.registry.Build(c)
		if err == nil {
			w.events, err = w.connector.Subscribe(r.ctx)
			if err == nil && w.events == nil {
				err = errors.New("trading: Subscribe returned no event channel")
			}
		}
		if err != nil {
			w.unavailable = err
			r.logger.Error("trading connection unavailable",
				"connection", c.ExternalID, "error", w.unavailable)
			continue
		}
		r.wg.Add(1)
		go w.run()
	}
	return nil
}

// Stop cancels workers, waits for their current task, and closes every built
// connector. It is idempotent, including concurrent calls.
func (r *Runtime) Stop() {
	r.mu.Lock()
	if r.stopped {
		done := r.stopDone
		r.mu.Unlock()
		<-done
		return
	}
	r.stopped = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
	for _, w := range r.workers {
		if w.connector != nil {
			w.connector.Close()
		}
	}
	close(r.stopDone)
}

// PreCheck checks a draft's destination and optional venue rules, without
// writing an order, a venue link, or an execution report.
func (r *Runtime) PreCheck(ctx context.Context, order domain.Order, dest domain.TradingDestination) error {
	_, err := r.submit(ctx, task{ctx: ctx, draft: order, dest: dest, preCheck: true})
	return err
}

// Send sends one eligible held order. Once the worker accepts the task, caller
// cancellation only stops waiting; the worker completes the durable sequence.
func (r *Runtime) Send(ctx context.Context, order domain.ExternalID, dest domain.TradingDestination) (domain.VenueOrder, error) {
	return r.submit(ctx, task{order: order, dest: dest})
}

func (r *Runtime) submit(ctx context.Context, t task) (domain.VenueOrder, error) {
	if err := domain.ValidateTradingDestination(t.dest); err != nil {
		return domain.VenueOrder{}, err
	}
	connection, found, err := r.store.GetTradingConnection(ctx, t.dest.Connection)
	if err != nil {
		return domain.VenueOrder{}, err
	}
	if !found {
		return domain.VenueOrder{}, fmt.Errorf("trading: connection %s: %w", t.dest.Connection, domain.ErrNotFound)
	}
	r.mu.Lock()
	w, ok := r.workers[t.dest.Connection]
	if !r.started || r.stopped || r.ctx.Err() != nil {
		r.mu.Unlock()
		return domain.VenueOrder{}, fmt.Errorf("trading: runtime is not running: %w", domain.ErrConflict)
	}
	if !connection.Enabled {
		r.mu.Unlock()
		return domain.VenueOrder{}, fmt.Errorf("trading: connection %s disabled: %w", connection.ExternalID, domain.ErrConflict)
	}
	if _, registered := r.registry.Lookup(connection.Provider); !registered {
		r.mu.Unlock()
		return domain.VenueOrder{}, fmt.Errorf("trading: unknown provider %q: %w", connection.Provider, domain.ErrInvalid)
	}
	if !ok || w.unavailable != nil {
		cause := "worker is not running"
		if ok {
			cause = w.unavailable.Error()
		}
		r.mu.Unlock()
		return domain.VenueOrder{}, fmt.Errorf("trading: connection %s unavailable: %s: %w", connection.ExternalID, cause, domain.ErrUpstream)
	}
	runtimeCtx := r.ctx
	r.mu.Unlock()
	t.result = make(chan taskResult, 1)
	select {
	case <-ctx.Done():
		return domain.VenueOrder{}, ctx.Err()
	case <-runtimeCtx.Done():
		return domain.VenueOrder{}, fmt.Errorf("trading: runtime stopped: %w", domain.ErrConflict)
	case w.tasks <- t:
	}
	select {
	case <-ctx.Done():
		return domain.VenueOrder{}, ctx.Err()
	case <-runtimeCtx.Done():
		return domain.VenueOrder{}, fmt.Errorf("trading: runtime stopped: %w", domain.ErrConflict)
	case result := <-t.result:
		return result.link, result.err
	}
}

// VerifySymbol builds a fresh connector; unsupported verification returns
// ErrUnsupported. It does not depend on a running worker or enabled sends.
func (r *Runtime) VerifySymbol(ctx context.Context, connection domain.ExternalID, external string) (SymbolVerification, error) {
	c, found, err := r.store.GetTradingConnection(ctx, connection)
	if err != nil {
		return SymbolVerification{}, err
	}
	if !found {
		return SymbolVerification{}, fmt.Errorf("trading: connection %s: %w", connection, domain.ErrNotFound)
	}
	result, supported, err := r.registry.VerifySymbol(ctx, c, external)
	if err != nil {
		return SymbolVerification{}, err
	}
	if !supported {
		return SymbolVerification{}, ErrUnsupported
	}
	return result, nil
}

func (w *worker) run() {
	defer w.r.wg.Done()
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	feeTicker := time.NewTicker(feePollInterval)
	defer feeTicker.Stop()
	for {
		// Stop takes precedence over queued tasks after the current task.
		if w.r.ctx.Err() != nil {
			return
		}
		select {
		case <-w.r.ctx.Done():
			return
		case t := <-w.tasks:
			var result taskResult
			if t.preCheck {
				ctx, cancel := context.WithTimeout(t.ctx, connectorTimeout)
				stop := context.AfterFunc(w.r.ctx, cancel)
				_, result.err = w.checkDestination(ctx, t.draft, t.dest)
				stop()
				cancel()
			} else {
				result.link, result.err = w.send(t.order, t.dest)
			}
			t.result <- result
		case event, ok := <-w.events:
			if !ok {
				w.events = nil
				if w.r.ctx.Err() == nil {
					w.r.mu.Lock()
					w.unavailable = errors.New("trading: venue event stream closed")
					w.r.mu.Unlock()
					w.log(slog.LevelError, "", "venue event stream closed", nil)
				}
				continue
			}
			w.handleEvent(event)
		case <-feeTicker.C:
			w.intakeFees()
		case <-ticker.C:
			if w.reconcileAllPending {
				w.reconcileAll()
			}
			for coid := range w.pending {
				if w.r.ctx.Err() != nil {
					return
				}
				w.reconcileClientID(coid)
			}
		}
	}
}

func (w *worker) checkDestination(ctx context.Context, order domain.Order, dest domain.TradingDestination) (Order, error) {
	if err := domain.ValidateTradingDestination(dest); err != nil {
		return Order{}, err
	}
	c, found, err := w.r.store.GetTradingConnection(ctx, dest.Connection)
	if err != nil {
		return Order{}, err
	}
	if !found {
		return Order{}, fmt.Errorf("trading: connection %s: %w", dest.Connection, domain.ErrNotFound)
	}
	if !c.Enabled {
		return Order{}, fmt.Errorf("trading: connection %s disabled: %w", c.ExternalID, domain.ErrConflict)
	}
	p, registered := w.r.registry.Lookup(c.Provider)
	if !registered {
		return Order{}, fmt.Errorf("trading: unknown provider %q: %w", c.Provider, domain.ErrInvalid)
	}
	w.r.mu.Lock()
	unavailable := w.unavailable
	w.r.mu.Unlock()
	if unavailable != nil {
		return Order{}, fmt.Errorf("trading: connection %s unavailable: %s: %w", c.ExternalID, unavailable, domain.ErrUpstream)
	}
	if p.VenueAccounts != (dest.VenueAccount != "") {
		return Order{}, fmt.Errorf("trading: provider %q venue-account rule: %w", p.Type, domain.ErrInvalid)
	}
	access, err := w.r.store.ListTradingAccess(ctx, order.Account)
	if err != nil {
		return Order{}, err
	}
	allowed := false
	for _, grant := range access {
		if grant.Connection == dest.Connection && grant.VenueAccount == dest.VenueAccount {
			allowed = true
			break
		}
	}
	if !allowed {
		return Order{}, fmt.Errorf("trading: account %q has no access to connection %s and venue account %q: %w", order.Account, c.ExternalID, dest.VenueAccount, domain.ErrForbidden)
	}
	instrument, found, err := w.r.store.FindTradingInstrument(ctx, dest.Connection, order.BaseAsset, order.QuoteAsset)
	if err != nil {
		return Order{}, err
	}
	if !found {
		return Order{}, fmt.Errorf("trading: instrument %s/%s: %w", order.BaseAsset, order.QuoteAsset, domain.ErrNotFound)
	}
	if !instrument.Enabled {
		return Order{}, fmt.Errorf("trading: instrument %s/%s disabled: %w", order.BaseAsset, order.QuoteAsset, domain.ErrConflict)
	}
	mapped := Order{
		Symbol: instrument.ExternalSymbol, Side: order.Side,
		AmountKind: order.AmountKind, Quantity: order.AmountValue,
		LimitPrice: order.Price, VenueAccount: dest.VenueAccount, Route: dest.Route,
	}
	if checker, ok := w.connector.(PreChecker); ok {
		checkCtx, cancel := context.WithTimeout(ctx, connectorTimeout)
		defer cancel()
		if err := checker.PreCheck(checkCtx, mapped); err != nil {
			return Order{}, fmt.Errorf("trading: venue pre-check: %w", err)
		}
	}
	return mapped, nil
}

func eligible(detail domain.OrderDetail, claim domain.ExternalID) error {
	if detail.Order.Status != domain.OrderStatusCommitted {
		return fmt.Errorf("trading: order is not committed: %w", domain.ErrConflict)
	}
	if detail.Order.DropCopy {
		return fmt.Errorf("trading: drop-copy order: %w", domain.ErrConflict)
	}
	if len(detail.Trades) != 0 {
		return fmt.Errorf("trading: order has trades: %w", domain.ErrConflict)
	}
	accepted := false
	for _, event := range detail.Events {
		if event.Payload.ExecutionReport != nil &&
			(claim == "" || event.Payload.ExecutionReport.ExternalID != claim) {
			return fmt.Errorf("trading: order has execution-report activity: %w", domain.ErrConflict)
		}
		if event.Type == domain.OrderEventConfirmed {
			return fmt.Errorf("trading: order is confirmed: %w", domain.ErrConflict)
		}
		accepted = accepted || event.Type == domain.OrderEventPreTradeAccepted
	}
	if !accepted {
		return fmt.Errorf("trading: order has no pre-trade acceptance: %w", domain.ErrConflict)
	}
	return nil
}

func reportID(coid, suffix string) domain.ExternalID {
	return domain.ExternalID("trading:" + coid + ":" + suffix)
}

func (w *worker) send(id domain.ExternalID, dest domain.TradingDestination) (domain.VenueOrder, error) {
	// Only Stop cancels the worker. Accepted tasks do not use the caller ctx.
	ctx := context.WithoutCancel(w.r.ctx)
	detail, err := w.r.store.GetOrder(ctx, id)
	if err != nil {
		return domain.VenueOrder{}, err
	}
	mapped, err := w.checkDestination(w.r.ctx, detail.Order, dest)
	if err != nil {
		return domain.VenueOrder{}, err
	}
	detail, err = w.r.store.GetOrder(ctx, id)
	if err != nil {
		return domain.VenueOrder{}, err
	}
	if err := eligible(detail, ""); err != nil {
		return domain.VenueOrder{}, err
	}
	coid, err := domain.NewExternalID()
	if err != nil {
		return domain.VenueOrder{}, err
	}
	mapped.ClientOrderID = string(coid)
	link, err := w.r.store.CreateVenueOrder(ctx, domain.VenueOrder{
		Order: id, Connection: dest.Connection, VenueAccount: dest.VenueAccount,
		Route: dest.Route, ClientOrderID: string(coid),
	})
	if err != nil {
		return domain.VenueOrder{}, fmt.Errorf("trading: create venue link: %w", err)
	}
	if err := w.r.sink(ctx, domain.ExecutionReportInput{
		ExternalID: reportID(link.ClientOrderID, "claim"), Order: id,
		OrderStatus: domain.OrderStatusCommitted,
	}); err != nil {
		if deleteErr := w.r.store.DeleteVenueOrder(ctx, id); deleteErr != nil {
			w.pending[link.ClientOrderID] = struct{}{}
			w.log(slog.LevelError, link.ClientOrderID, "remove failed claim link", deleteErr)
		}
		return link, err
	}
	detail, err = w.r.store.GetOrder(ctx, id)
	if err != nil {
		w.pending[link.ClientOrderID] = struct{}{}
		return link, err
	}
	if err := eligible(detail, reportID(link.ClientOrderID, "claim")); err != nil {
		if deleteErr := w.r.store.DeleteVenueOrder(ctx, id); deleteErr != nil {
			w.pending[link.ClientOrderID] = struct{}{}
			w.log(slog.LevelError, link.ClientOrderID, "remove ineligible claim link", deleteErr)
		}
		return link, err
	}
	w.pending[link.ClientOrderID] = struct{}{}
	if err := w.r.store.MarkVenueOrderSendAttempted(ctx, id); err != nil {
		return link, err
	}
	link.SendAttempted = true
	sendCtx, cancel := context.WithTimeout(w.r.ctx, connectorTimeout)
	ack, err := w.connector.Send(sendCtx, mapped)
	cancel()
	if err == nil {
		if ack.VenueOrderID == "" {
			err = errors.New("trading: Send acknowledged without a venue order id")
		} else if err := w.r.store.SetVenueOrderID(ctx, id, ack.VenueOrderID); err != nil {
			return link, err
		} else {
			link.VenueOrderID = ack.VenueOrderID
			delete(w.pending, link.ClientOrderID)
			return link, nil
		}
	}
	if errors.Is(err, ErrRejected) || errors.Is(err, ErrUnsupported) {
		if doneErr := w.cancelOrder(link); doneErr != nil {
			w.log(slog.LevelError, link.ClientOrderID, "record definitive send refusal", doneErr)
		} else {
			delete(w.pending, link.ClientOrderID)
		}
		return link, fmt.Errorf("trading: venue send refusal: %w", err)
	}
	return link, fmt.Errorf("trading: send outcome unknown; reconciliation will resolve it: %w: %w", err, domain.ErrUpstream)
}

func (w *worker) cancelOrder(link domain.VenueOrder) error {
	err := w.r.sink(context.WithoutCancel(w.r.ctx), domain.ExecutionReportInput{
		ExternalID: reportID(link.ClientOrderID, "done"), Order: link.Order,
		OrderStatus: domain.OrderStatusCancelled, LeavesQuantity: "0",
	})
	if errors.Is(err, domain.ErrAlreadyExists) {
		return nil
	}
	return err
}

func (w *worker) log(level slog.Level, coid, message string, err error) {
	w.r.logger.Log(context.WithoutCancel(w.r.ctx), level, message,
		"connection", w.connection.ExternalID,
		"client_order_id", coid,
		"error", err)
}
