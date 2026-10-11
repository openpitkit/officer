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
	"slices"
	"sync"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/store"
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

// FeeSkipSink records that a venue fee was skipped and why. The application
// writes an audit record.
type FeeSkipSink func(ctx context.Context, connection domain.ExternalID, feeID, reason string) error

// Store is the runtime's subset of store.RealmStore. Its trading methods retain
// that interface's not-found, conflict and already-exists contracts.
type Store interface {
	GetTradingConnection(context.Context, domain.ExternalID) (domain.TradingConnection, bool, error)
	// GetTradingFeeAccounts atomically reads the fee account and the distinct
	// accounts that can send through the connection: direct grantees and the
	// current members of granted groups.
	GetTradingFeeAccounts(context.Context, domain.ExternalID) (domain.AccountID, []domain.AccountID, error)
	ListTradingConnections(context.Context) ([]domain.TradingConnection, error)
	FindTradingInstrument(context.Context, domain.ExternalID, string, string) (domain.TradingInstrument, bool, error)
	ListTradingAccess(context.Context, domain.AccountID) ([]domain.TradingAccess, error)
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
	ListAdjustmentRows(context.Context, store.AdjustmentListFilter) (store.AdjustmentListPage, error)
}

// Runtime owns one serial worker per enabled connection or connection with
// outstanding orders or any venue link. Disabled connections keep a worker
// for tracking and fee intake only, without new sends. Start reads the current
// store state; Restart applies connection changes after stopping all workers.
// Lifecycle calls are serialized, and Stop leaves the runtime restartable.
type Runtime struct {
	registry *Registry
	store    Store
	sink     ReportSink
	adjust   AdjustmentSink
	feeSkip  FeeSkipSink
	logger   *slog.Logger

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	started     bool
	baseCtx     context.Context
	ctx         context.Context
	cancel      context.CancelFunc
	workers     map[domain.ExternalID]*worker
	feeSkips    map[domain.ExternalID]map[string]struct{}
	wg          sync.WaitGroup
}

// ConnectionState is the health of a held worker's venue connection.
type ConnectionState string

const (
	// ConnectionRunning means the worker runs and its connector reports no
	// failure.
	ConnectionRunning ConnectionState = "running"
	// ConnectionDegraded means the worker runs and still sends, but its
	// connector reports a stream failure; Error is the latest one.
	ConnectionDegraded ConnectionState = "degraded"
	// ConnectionUnavailable means the worker cannot reach its venue and
	// refuses sends; Error says why.
	ConnectionUnavailable ConnectionState = "unavailable"
)

// ConnectionStatus is a held worker's state with its error and diagnostics.
// Error is empty while State is ConnectionRunning; otherwise it carries the
// unavailable error or the connector's latest failure message, empty only if
// a connector breaks its contract to send a non-empty one. Diagnostics are
// oldest first, at most 20 (the cap health.Record applies).
type ConnectionStatus struct {
	State       ConnectionState
	Error       string
	Diagnostics []health.Diagnostic
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
	// streamFailed is set while the connector's latest status report is a
	// failure, and streamErr is that report's message. Both protected by
	// Runtime.mu.
	streamFailed bool
	streamErr    string
	diagnostics  []health.Diagnostic // Protected by Runtime.mu.
	// retired is set by stop when it removes the worker from
	// Runtime.workers; a retired worker drops its connector's reports.
	// Protected by Runtime.mu.
	retired bool
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

// NewRuntime requires a registry, durable store, both core sinks, a fee skip
// sink and a logger.
func NewRuntime(registry *Registry, st Store, sink ReportSink, adjust AdjustmentSink, feeSkip FeeSkipSink, logger *slog.Logger) (*Runtime, error) {
	if registry == nil || st == nil || sink == nil || adjust == nil || feeSkip == nil || logger == nil {
		return nil, fmt.Errorf("trading: registry, store, report sink, adjustment sink, fee skip sink and logger required: %w", domain.ErrInvalid)
	}
	return &Runtime{
		registry: registry, store: st, sink: sink, adjust: adjust, feeSkip: feeSkip, logger: logger,
		feeSkips: make(map[domain.ExternalID]map[string]struct{}),
	}, nil
}

// Start starts workers for enabled connections and connections with outstanding
// orders or any venue link. A disabled connection's worker only tracks orders
// and takes in fees; it sends no new orders.
// Connector failures isolate one connection; only store failures abort startup.
// Start is valid again after Stop; starting a running runtime wraps
// ErrConflict. ctx is retained for Restart and must be process-scoped, not
// request-scoped.
func (r *Runtime) Start(ctx context.Context) error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	return r.start(ctx)
}

func (r *Runtime) start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return fmt.Errorf("trading: runtime already started: %w", domain.ErrConflict)
	}
	r.mu.Unlock()
	r.baseCtx = ctx
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
	runCtx, cancel := context.WithCancel(ctx)
	workers := make(map[domain.ExternalID]*worker)
	for _, c := range connections {
		if !c.Enabled && len(open[c.ExternalID]) == 0 && !traded[c.ExternalID] {
			continue
		}
		w := &worker{
			r: r, connection: c, tasks: make(chan task),
			pending: make(map[string]struct{}),
		}
		workers[c.ExternalID] = w
		for _, link := range open[c.ExternalID] {
			w.pending[link.ClientOrderID] = struct{}{}
		}
		w.connector, err = r.registry.Build(c)
		if err != nil {
			// Every rebuild, including the one that saving credentials
			// makes, fails for an unregistered provider, so its
			// remediation promises no retry.
			if _, registered := r.registry.Lookup(c.Provider); !registered {
				w.setUnavailable(err, health.Diagnostic{
					Code:  health.CodeUnsupportedProvider,
					Kind:  health.DiagKindConfig,
					Title: "Unsupported trading provider",
					Remediation: "Officer refuses sends through this " +
						"connection. This Officer build has no such trading " +
						"provider, so the connection cannot run until one " +
						"is registered.",
				})
				continue
			}
			w.setUnavailable(err, health.Diagnostic{
				Code:  health.CodeInvalidProviderConfig,
				Kind:  health.DiagKindConfig,
				Title: "Invalid connection configuration",
				Remediation: "Officer refuses sends through this connection. " +
					"Save corrected credentials, or disable and re-enable " +
					"the connection, to retry. If the error names no " +
					"configuration problem, this is an internal issue - " +
					"please report it.",
			})
			continue
		}
		// Wire the reporters before Subscribe, as market data does. Both
		// take Runtime.mu themselves, and no lock is held while Subscribe
		// runs, so a connector that reports synchronously cannot deadlock.
		// Status lives on the worker, not in a map keyed by connection, so
		// a report that arrives before the worker is published is kept.
		// Stop retires the worker, so a report from a previous
		// generation's connector is dropped, neither recorded nor logged.
		if sr, ok := w.connector.(health.StatusReporting); ok {
			sr.SetStatusReporter(w.reportStatus)
		}
		if dr, ok := w.connector.(health.DiagnosticReporting); ok {
			dr.SetDiagnosticReporter(w.reportDiagnostic)
		}
		w.events, err = w.connector.Subscribe(runCtx)
		switch {
		case err != nil:
			w.setUnavailable(err, health.Diagnostic{
				Code:  health.CodeConnectionError,
				Kind:  health.DiagKindEnvironment,
				Title: "Venue stream failed to start",
				Remediation: "Officer refuses sends through this connection. " +
					"Check that the venue is reachable and that the " +
					"connection's credentials are valid, then disable and " +
					"re-enable the connection to retry.",
			})
		case w.events == nil:
			// A connector defect, not the venue or the environment.
			w.setUnavailable(errors.New("trading: Subscribe returned no event channel"), health.Diagnostic{
				Code:  health.CodeInternalError,
				Kind:  health.DiagKindProvider,
				Title: "Internal error",
				Remediation: "Officer refuses sends through this connection. " +
					"This is an internal issue in the venue connector; " +
					"please report it.",
			})
		}
	}
	r.mu.Lock()
	r.ctx, r.cancel, r.workers = runCtx, cancel, workers
	r.started = true
	r.mu.Unlock()
	for _, w := range workers {
		if w.unavailable == nil {
			r.wg.Add(1)
			go w.run()
		}
	}
	return nil
}

// Stop cancels connector calls, waits for current tasks and their durable
// writes, then closes every built connector. It is idempotent, including
// concurrent calls and calls before Start. A later Start or Restart creates
// fresh workers.
func (r *Runtime) Stop() {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.stop()
}

func (r *Runtime) stop() {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return
	}
	r.started = false
	r.cancel()
	workers := r.workers
	r.workers = nil
	for _, w := range workers {
		w.retired = true
	}
	r.mu.Unlock()
	r.wg.Wait()
	for _, w := range workers {
		if w.connector != nil {
			w.connector.Close()
		}
	}
}

// Restart stops all workers, then starts from the current store state using the
// last Start context. Calling it before Start returns domain.ErrConflict.
// A failed Restart leaves the runtime stopped.
func (r *Runtime) Restart() error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.baseCtx == nil {
		return fmt.Errorf("trading: runtime has never started: %w", domain.ErrConflict)
	}
	r.stop()
	if err := r.start(r.baseCtx); err != nil {
		return fmt.Errorf("trading: restart: %w", err)
	}
	return nil
}

// ConnectionStatuses snapshots every worker held by the running generation.
// Connections without workers and stopped runtimes are absent from the map.
// An unavailable worker is ConnectionUnavailable even while its connector
// also reports a stream failure. Each status owns its Diagnostics and their
// Actions.
func (r *Runtime) ConnectionStatuses() map[domain.ExternalID]ConnectionStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.ctx.Err() != nil {
		return map[domain.ExternalID]ConnectionStatus{}
	}
	statuses := make(map[domain.ExternalID]ConnectionStatus, len(r.workers))
	for id, w := range r.workers {
		// A copy down to the Actions, so a later in-place At refresh by
		// health.Record cannot race the caller and a caller's write
		// cannot reach the worker's diagnostics.
		status := ConnectionStatus{
			State:       ConnectionRunning,
			Diagnostics: slices.Clone(w.diagnostics),
		}
		for i := range status.Diagnostics {
			status.Diagnostics[i].Actions = slices.Clone(status.Diagnostics[i].Actions)
		}
		switch {
		case w.unavailable != nil:
			status.State, status.Error = ConnectionUnavailable, w.unavailable.Error()
		case w.streamFailed:
			status.State, status.Error = ConnectionDegraded, w.streamErr
		}
		statuses[id] = status
	}
	return statuses
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

func (r *Runtime) submit(ctx context.Context, t task) (link domain.VenueOrder, err error) {
	handedOff := false
	defer func() {
		if err != nil && !t.preCheck && !handedOff {
			err = fmt.Errorf("%w: %w", ErrNotSent, err)
		}
	}()
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
	if !r.started || r.ctx.Err() != nil {
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
	handedOff = true
	select {
	case <-ctx.Done():
		return domain.VenueOrder{}, ctx.Err()
	case <-runtimeCtx.Done():
		return domain.VenueOrder{}, fmt.Errorf("trading: runtime stopped: %w", domain.ErrConflict)
	case result := <-t.result:
		return result.link, result.err
	}
}

// Providers returns the registered connector providers sorted by Type.
func (r *Runtime) Providers() []Provider { return r.registry.Providers() }

// CheckAccount checks a stored or draft connection without a running worker.
func (r *Runtime) CheckAccount(ctx context.Context, connection domain.TradingConnection) (AccountInfo, error) {
	result, supported, err := r.registry.CheckAccount(ctx, connection)
	if err != nil {
		return AccountInfo{}, err
	}
	if !supported {
		return AccountInfo{}, ErrUnsupported
	}
	return result, nil
}

// SearchSymbols searches the supplied connection snapshot without a worker.
func (r *Runtime) SearchSymbols(ctx context.Context, connection domain.TradingConnection, query string) ([]SymbolMatch, error) {
	result, supported, err := r.registry.SearchSymbols(ctx, connection, query)
	if err != nil {
		return nil, err
	}
	if !supported {
		return nil, ErrUnsupported
	}
	return result, nil
}

// VerifySymbol builds a fresh connector; unsupported verification returns
// ErrUnsupported. It does not depend on a running worker or enabled sends.
func (r *Runtime) VerifySymbol(ctx context.Context, connection domain.TradingConnection, external string) (SymbolVerification, error) {
	result, supported, err := r.registry.VerifySymbol(ctx, connection, external)
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
					w.setUnavailable(errors.New("trading: venue event stream closed"), health.Diagnostic{
						Code:  health.CodeConnectionError,
						Kind:  health.DiagKindProvider,
						Title: "Venue stream closed",
						Remediation: "Officer refuses sends through this " +
							"connection until it restarts. Disable and " +
							"re-enable the connection to restart it.",
					})
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

func (w *worker) send(id domain.ExternalID, dest domain.TradingDestination) (link domain.VenueOrder, err error) {
	linked := false
	defer func() {
		if err != nil && !linked {
			err = fmt.Errorf("%w: %w", ErrNotSent, err)
		}
	}()
	// Only Stop cancels the worker. Accepted tasks do not use the caller ctx.
	ctx := context.WithoutCancel(w.r.ctx)
	detail, err := w.r.store.GetOrder(ctx, id)
	if err != nil {
		return domain.VenueOrder{}, err
	}
	mapped, err := w.checkDestination(w.r.ctx, detail.Order, dest)
	if err != nil {
		if runErr := w.r.ctx.Err(); runErr != nil && errors.Is(err, runErr) {
			return domain.VenueOrder{}, fmt.Errorf("trading: runtime stopped: %w: %w", err, domain.ErrConflict)
		}
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
	link, err = w.r.store.CreateVenueOrder(ctx, domain.VenueOrder{
		Order: id, Connection: dest.Connection, VenueAccount: dest.VenueAccount,
		Route: dest.Route, ClientOrderID: string(coid),
	})
	if err != nil {
		return domain.VenueOrder{}, fmt.Errorf("trading: create venue link: %w", err)
	}
	linked = true
	if err := w.r.sink(ctx, domain.ExecutionReportInput{
		ExternalID: reportID(link.ClientOrderID, "claim"), Order: id,
		OrderStatus: domain.OrderStatusCommitted,
	}); err != nil {
		if deleteErr := w.r.store.DeleteVenueOrder(ctx, id); deleteErr != nil {
			w.pending[link.ClientOrderID] = struct{}{}
			w.log(slog.LevelError, link.ClientOrderID, "remove failed claim link", deleteErr)
		} else {
			linked = false
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
		} else {
			linked = false
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

// setUnavailable makes the worker refuse sends with err and records diag at
// level DiagError with err's text as its Detail.
func (w *worker) setUnavailable(err error, diag health.Diagnostic) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	w.unavailable = err
	diag.Level, diag.Detail = health.DiagError, err.Error()
	w.recordLocked(diag)
}

// reportStatus is the worker's health.StatusReporter. A failure degrades the
// worker without gating sends, whatever its message; ok clears it and keeps
// the diagnostics. A retired worker drops the report.
func (w *worker) reportStatus(ok bool, errMsg string) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if w.retired {
		return
	}
	if ok {
		w.streamFailed, w.streamErr = false, ""
		return
	}
	w.streamFailed, w.streamErr = true, errMsg
	w.recordLocked(health.Diagnostic{
		Level:  health.DiagError,
		Code:   health.CodeConnectionError,
		Kind:   health.DiagKindEnvironment,
		Title:  "Venue stream error",
		Detail: errMsg,
		Remediation: "Officer keeps reconnecting. Check that the venue is " +
			"reachable and that the connection's credentials are valid.",
	})
}

// reportDiagnostic is the worker's health.DiagnosticReporter. A retired
// worker drops the report.
func (w *worker) reportDiagnostic(diag health.Diagnostic) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if w.retired {
		return
	}
	w.recordLocked(diag)
}

// recordLocked records diag in the worker's diagnostics. The caller holds
// Runtime.mu.
func (w *worker) recordLocked(diag health.Diagnostic) {
	w.diagnostics = health.Record(w.r.logger, w.diagnostics, diag, "connection", w.connection.ExternalID)
}
