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

package trading_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

func TestRuntimeStartAfterStop(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.send(f.submit("1"))
	f.venue.mu.Lock()
	first := f.venue.current
	f.venue.mu.Unlock()
	f.runtime.Stop()
	must(t, f.runtime.Start(testCtx))
	f.venue.mu.Lock()
	second, builds, closes := f.venue.current, f.venue.builds, f.venue.closes
	f.venue.mu.Unlock()
	if first == second || builds != 2 || closes != 1 {
		t.Fatalf("second generation: same connector=%v builds=%d closes=%d", first == second, builds, closes)
	}
	order := f.submit("1")
	link := f.send(order)
	stored, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if !found || link.VenueOrderID == "" || stored.VenueOrderID != link.VenueOrderID {
		t.Fatal("second generation did not acknowledge Send")
	}
}

func TestRuntimeRestartPicksUpConnection(t *testing.T) {
	f := newFixture(t)
	v := newVenue()
	must(t, f.registry.Register(trading.Provider{Type: "later", Build: v.build}))
	f.start()
	c := f.addConnection("later", "created after start", true)
	dest := domain.TradingDestination{Connection: c.ExternalID, Route: "{}"}
	if _, held := f.runtime.ConnectionStatuses()[c.ExternalID]; held {
		t.Fatal("new connection got a worker before Restart")
	}
	order := f.submit("1")
	if _, err := f.runtime.Send(testCtx, order.ExternalID, dest); !errors.Is(err, domain.ErrUpstream) {
		t.Fatalf("Send before Restart=%v", err)
	}
	must(t, f.runtime.Restart())
	if status, held := f.runtime.ConnectionStatuses()[c.ExternalID]; !held || status.State != trading.ConnectionRunning {
		t.Fatalf("new connection has no available worker after Restart: held=%v status=%+v", held, status)
	}
	must(t, f.runtime.PreCheck(testCtx, f.draft("1"), dest))
	link, err := f.runtime.Send(testCtx, order.ExternalID, dest)
	must(t, err)
	if link.VenueOrderID == "" {
		t.Fatal("new connection did not acknowledge Send")
	}
}

func TestRuntimeRestartAppliesEnabled(t *testing.T) {
	f := newFixture(t)
	id := f.connection.ExternalID
	must(t, f.st.SetTradingConnectionEnabled(testCtx, id, false))
	f.start()
	if _, held := f.runtime.ConnectionStatuses()[id]; held {
		t.Fatal("disabled connection without links has a worker")
	}
	must(t, f.st.SetTradingConnectionEnabled(testCtx, id, true))
	if err := f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); !errors.Is(err, domain.ErrUpstream) {
		t.Fatalf("enabled before Restart=%v", err)
	}
	must(t, f.runtime.Restart())
	must(t, f.runtime.PreCheck(testCtx, f.draft("1"), f.dest))
	must(t, f.st.SetTradingConnectionEnabled(testCtx, id, false))
	if _, held := f.runtime.ConnectionStatuses()[id]; !held {
		t.Fatal("disabling removed the worker before Restart")
	}
	if err := f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("disabled worker allowed PreCheck=%v", err)
	}
	must(t, f.runtime.Restart())
	if _, held := f.runtime.ConnectionStatuses()[id]; held {
		t.Fatal("disabled connection without links kept a worker after Restart")
	}
	must(t, f.st.SetTradingConnectionEnabled(testCtx, id, true))
	must(t, f.runtime.Restart())
	order := f.submit("1")
	link := f.send(order)
	must(t, f.st.SetTradingConnectionEnabled(testCtx, id, false))
	must(t, f.runtime.Restart())
	if _, held := f.runtime.ConnectionStatuses()[id]; !held {
		t.Fatal("disabled connection with open venue order lost its worker")
	}
	if _, err := f.runtime.Send(testCtx, f.submit("1").ExternalID, f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("disabled tracking worker allowed Send=%v", err)
	}
	fill := makeFill("1", "1", "0", true)
	f.venue.setSnapshot(link, trading.VenueStatusFilled, "1", fill)
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	if f.detail(order).Order.Status != domain.OrderStatusFilled {
		t.Fatal("disabled worker did not track its open order")
	}
	open, err := f.st.ListOpenVenueOrders(testCtx, id)
	must(t, err)
	if len(open) != 0 {
		t.Fatal("filled venue order is still open")
	}
	must(t, f.runtime.Restart())
	if _, held := f.runtime.ConnectionStatuses()[id]; !held {
		t.Fatal("disabled connection with a finished venue link lost its worker")
	}
}

func TestRuntimeLifecycleGuards(t *testing.T) {
	f := newFixture(t)
	f.newRuntime()
	if err := f.runtime.Restart(); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Restart before Start=%v", err)
	}
	f.runtime.Stop()
	f.runtime.Stop()
	must(t, f.runtime.Start(testCtx))
	if err := f.runtime.Start(testCtx); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Start while running=%v", err)
	}
	f.venue.mu.Lock()
	builds, closes := f.venue.builds, f.venue.closes
	f.venue.mu.Unlock()
	if builds != 1 || closes != 0 {
		t.Fatalf("invalid lifecycle calls changed workers: builds=%d closes=%d", builds, closes)
	}
}

type startupStore struct {
	trading.Store
	fail     string
	contexts []context.Context
}

var errStartupRead = errors.New("startup store read failed")

func (s *startupStore) ListTradingConnections(ctx context.Context) ([]domain.TradingConnection, error) {
	s.contexts = append(s.contexts, ctx)
	if s.fail == "connections" {
		return nil, errStartupRead
	}
	return s.Store.ListTradingConnections(ctx)
}

func (s *startupStore) ListOpenVenueOrders(ctx context.Context, id domain.ExternalID) ([]domain.VenueOrder, error) {
	if s.fail == "open" {
		return nil, errStartupRead
	}
	return s.Store.ListOpenVenueOrders(ctx, id)
}

func (s *startupStore) EarliestVenueOrderTime(ctx context.Context, id domain.ExternalID) (time.Time, bool, error) {
	if s.fail == "links" {
		return time.Time{}, false, errStartupRead
	}
	return s.Store.EarliestVenueOrderTime(ctx, id)
}

func TestRuntimeRestartUsesBaseContext(t *testing.T) {
	f := newFixture(t)
	st := &startupStore{Store: f.st}
	r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink, f.feeSkipSink, slog.New(slog.NewJSONHandler(f.logs, nil)))
	must(t, err)
	t.Cleanup(r.Stop)
	type contextKey struct{}
	requestCtx, cancel := context.WithCancel(context.WithValue(testCtx, contextKey{}, "process"))
	baseCtx := context.WithoutCancel(requestCtx)
	must(t, r.Start(baseCtx))
	cancel()
	must(t, r.Restart())
	if len(st.contexts) != 2 || st.contexts[1] != baseCtx || st.contexts[1].Value(contextKey{}) != "process" {
		t.Fatal("Restart did not retain the last Start base context")
	}
	must(t, r.PreCheck(testCtx, f.draft("1"), f.dest))
	r.Stop()
	nextCtx := context.WithValue(testCtx, contextKey{}, "next process")
	must(t, r.Start(nextCtx))
	must(t, r.Restart())
	if st.contexts[3] != nextCtx {
		t.Fatal("Restart retained an earlier Start context")
	}
}

func TestRuntimeStartReadFailures(t *testing.T) {
	for _, fail := range []string{"connections", "open", "links"} {
		t.Run(fail, func(t *testing.T) {
			f := newFixture(t)
			c := f.addConnection("fake", "disabled", false)
			must(t, f.st.SetTradingConnectionEnabled(testCtx, c.ExternalID, false))
			st := &startupStore{Store: f.st, fail: fail}
			r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink, f.feeSkipSink, slog.New(slog.NewJSONHandler(f.logs, nil)))
			must(t, err)
			t.Cleanup(r.Stop)
			if err := r.Start(testCtx); !errors.Is(err, errStartupRead) {
				t.Fatalf("Start read failure=%v", err)
			}
			f.venue.mu.Lock()
			builds := f.venue.builds
			f.venue.mu.Unlock()
			if builds != 0 || len(r.ConnectionStatuses()) != 0 {
				t.Fatal("failed store read started a partial generation")
			}
			st.fail = ""
			must(t, r.Start(testCtx))
			must(t, r.PreCheck(testCtx, f.draft("1"), f.dest))
		})
	}
}

func TestRuntimeRestartReadFailure(t *testing.T) {
	f := newFixture(t)
	f.addConnection("fake", "secondary", false)
	st := &startupStore{Store: f.st}
	r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink, f.feeSkipSink,
		slog.New(slog.NewJSONHandler(f.logs, nil)))
	must(t, err)
	t.Cleanup(r.Stop)
	must(t, r.Start(testCtx))
	must(t, r.PreCheck(testCtx, f.draft("1"), f.dest))
	st.fail = "connections"
	err = r.Restart()
	if !errors.Is(err, errStartupRead) || err == errStartupRead {
		t.Fatalf("Restart did not wrap its store read failure: %v", err)
	}
	if statuses := r.ConnectionStatuses(); len(statuses) != 0 {
		t.Fatalf("failed Restart still exposes connection statuses: %+v", statuses)
	}
	f.venue.mu.Lock()
	builds, closes := f.venue.builds, f.venue.closes
	f.venue.mu.Unlock()
	if builds != 2 || closes != builds {
		t.Fatalf("failed Restart leaked connectors: builds=%d closes=%d", builds, closes)
	}
	if err := r.PreCheck(testCtx, f.draft("1"), f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("PreCheck after failed Restart=%v", err)
	}
	st.fail = ""
	must(t, r.Restart())
	must(t, r.PreCheck(testCtx, f.draft("1"), f.dest))
}

func TestRuntimeConnectionStatusesAfterContextCancellation(t *testing.T) {
	f := newFixture(t)
	f.newRuntime()
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	must(t, f.runtime.Start(ctx))
	if statuses := f.runtime.ConnectionStatuses(); len(statuses) != 1 {
		t.Fatalf("running connection statuses: %+v", statuses)
	}
	order := f.submit("1")
	cancel()
	if statuses := f.runtime.ConnectionStatuses(); len(statuses) != 0 {
		t.Fatalf("cancelled generation still exposes connection statuses: %+v", statuses)
	}
	if err := f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("PreCheck after context cancellation=%v", err)
	}
	if _, err := f.runtime.Send(testCtx, order.ExternalID, f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Send after context cancellation=%v", err)
	}
}

type stoppedSendContext struct {
	context.Context
	calls   atomic.Int32
	release <-chan struct{}
}

func (c *stoppedSendContext) Done() <-chan struct{} {
	if c.calls.Add(1) > 1 {
		<-c.release
	}
	return nil
}

type stoppedSendStore struct {
	trading.Store
	caller  *stoppedSendContext
	entered chan struct{}
}

func (s *stoppedSendStore) GetTradingConnection(ctx context.Context, id domain.ExternalID) (domain.TradingConnection, bool, error) {
	if ctx == s.caller {
		return s.Store.GetTradingConnection(s.caller.Context, id)
	}
	close(s.entered)
	<-ctx.Done()
	return s.Store.GetTradingConnection(ctx, id)
}

func TestRuntimeSendStoppedDuringDestinationRead(t *testing.T) {
	f := newFixture(t)
	order := f.submit("1")
	workerResults := 0
	for i := 0; i < 32; i++ {
		release := make(chan struct{})
		var once sync.Once
		releaseCaller := func() { once.Do(func() { close(release) }) }
		defer releaseCaller()
		caller := &stoppedSendContext{Context: testCtx, release: release}
		st := &stoppedSendStore{Store: f.st, caller: caller, entered: make(chan struct{})}
		r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink, f.feeSkipSink,
			slog.New(slog.NewJSONHandler(f.logs, nil)))
		must(t, err)
		t.Cleanup(r.Stop)
		must(t, r.Start(testCtx))
		sent := make(chan error, 1)
		go func() { _, err := r.Send(caller, order.ExternalID, f.dest); sent <- err }()
		select {
		case <-st.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("Send did not reach the worker's destination read")
		}
		r.Stop()
		// Both select arms are ready, so exercise result delivery as well as Stop.
		releaseCaller()
		select {
		case err := <-sent:
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("accepted Send stopped before any write (attempt %d)=%v", i, err)
			}
			if errors.Is(err, trading.ErrNotSent) {
				workerResults++
			}
		case <-time.After(3 * time.Second):
			t.Fatal("accepted Send hung after Stop")
		}
		_, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
		must(t, err)
		if found {
			t.Fatal("cancelled destination read created a venue link")
		}
	}
	if workerResults == 0 {
		t.Fatal("no stopped worker result carried ErrNotSent")
	}
}

func TestRuntimeConnectionStatuses(t *testing.T) {
	for _, failure := range []string{"build", "subscribe"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.newRuntime()
			if len(f.runtime.ConnectionStatuses()) != 0 {
				t.Fatal("unstarted runtime has connection statuses")
			}
			v := newVenue()
			cause := errors.New("authentication failed")
			build := v.build
			if failure == "build" {
				build = func(domain.TradingConnection) (trading.Connector, error) { return nil, cause }
			} else {
				v.subscribeErr = cause
			}
			must(t, f.registry.Register(trading.Provider{Type: "broken", Build: build}))
			bad := f.addConnection("broken", "unavailable", true)
			disabled := f.addConnection("fake", "disabled without links", false)
			must(t, f.st.SetTradingConnectionEnabled(testCtx, disabled.ExternalID, false))
			must(t, f.runtime.Start(testCtx))
			statuses := f.runtime.ConnectionStatuses()
			if status, held := statuses[f.connection.ExternalID]; !held || status.State != trading.ConnectionRunning {
				t.Fatalf("running worker status: held=%v status=%+v", held, status)
			}
			if status, held := statuses[bad.ExternalID]; !held || status.State != trading.ConnectionUnavailable || !strings.Contains(status.Error, cause.Error()) {
				t.Fatalf("unavailable worker status: held=%v status=%+v", held, status)
			}
			if _, held := statuses[disabled.ExternalID]; held || len(statuses) != 2 {
				t.Fatalf("workers without links in status map: %+v", statuses)
			}
			for _, status := range statuses {
				if strings.Contains(fmt.Sprintf("%+v", status), "do-not-log-") {
					t.Fatalf("status leaked credentials: %+v", status)
				}
			}
			delete(statuses, f.connection.ExternalID)
			if _, held := f.runtime.ConnectionStatuses()[f.connection.ExternalID]; !held {
				t.Fatal("caller changed the runtime's status map")
			}
			f.venue.mu.Lock()
			connector := f.venue.current
			f.venue.mu.Unlock()
			connector.closeEvents()
			waitFor(t, func() bool {
				status := f.runtime.ConnectionStatuses()[f.connection.ExternalID]
				return status.State == trading.ConnectionUnavailable && strings.Contains(status.Error, "venue event stream closed")
			}, "closed event stream status")
			f.runtime.Stop()
			if len(f.runtime.ConnectionStatuses()) != 0 {
				t.Fatal("stopped runtime still exposes connection statuses")
			}
			v.mu.Lock()
			closes := v.closes
			v.mu.Unlock()
			if failure == "subscribe" && closes != 1 {
				t.Fatalf("failed Subscribe connector closes=%d", closes)
			}
			if strings.Contains(f.logs.text(), "do-not-log-") {
				t.Fatal("lifecycle logged credentials")
			}
		})
	}
}

func TestRuntimeCallsDuringStart(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.runtime.Stop()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseBuild := func() { once.Do(func() { close(release) }) }
	defer releaseBuild()
	v := newVenue()
	must(t, f.registry.Register(trading.Provider{Type: "slow", Build: func(c domain.TradingConnection) (trading.Connector, error) {
		close(entered)
		<-release
		return v.build(c)
	}}))
	f.addConnection("slow", "slow construction", false)
	started := make(chan error, 1)
	go func() { started <- f.runtime.Start(testCtx) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not reach connector construction")
	}
	orderID := f.submit("1").ExternalID
	calls := make(chan error, 1)
	go func() {
		if len(f.runtime.ConnectionStatuses()) != 0 {
			calls <- errors.New("partially built generation was published")
			return
		}
		if err := f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); !errors.Is(err, domain.ErrConflict) {
			calls <- errors.New("PreCheck during Start did not return ErrConflict")
			return
		}
		if _, err := f.runtime.Send(testCtx, orderID, f.dest); !errors.Is(err, domain.ErrConflict) {
			calls <- errors.New("Send during Start did not return ErrConflict")
			return
		}
		_, err := f.runtime.VerifySymbol(testCtx, f.connection, "AAPL")
		if !errors.Is(err, trading.ErrUnsupported) {
			calls <- errors.New("VerifySymbol during Start did not finish independently")
			return
		}
		calls <- nil
	}()
	blocked := false
	select {
	case err := <-calls:
		must(t, err)
	case <-time.After(500 * time.Millisecond):
		blocked = true
	}
	releaseBuild()
	must(t, <-started)
	if blocked {
		<-calls
		t.Fatal("calls blocked on connector construction")
	}
	must(t, f.runtime.PreCheck(testCtx, f.draft("1"), f.dest))
}

func TestRuntimeConcurrentLifecycleAndSend(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("1")
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseSend := func() { once.Do(func() { close(release) }) }
	defer releaseSend()
	f.venue.onSend = func(ctx context.Context, _ trading.Order) error {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return nil
	}
	sent := make(chan error, 1)
	go func() { _, err := f.runtime.Send(testCtx, order.ExternalID, f.dest); sent <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Send was not accepted")
	}
	queued := make(chan error, 16)
	for i := 0; i < cap(queued); i++ {
		id := f.submit("1").ExternalID
		go func() { _, err := f.runtime.Send(testCtx, id, f.dest); queued <- err }()
	}
	stopped := make(chan struct{})
	go func() { f.runtime.Stop(); close(stopped) }()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not cancel the accepted venue call")
	}
	for i := 0; i < cap(queued); i++ {
		select {
		case err := <-queued:
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("queued Send during Stop=%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("queued Send hung during Stop")
		}
	}
	select {
	case <-stopped:
		releaseSend()
		waitFor(t, func() bool {
			link, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
			return err == nil && found && link.VenueOrderID != ""
		}, "accepted Send acknowledgement after early Stop")
		t.Fatal("Stop returned before the accepted Send completed")
	default:
	}
	restarted := make(chan error, 1)
	go func() { restarted <- f.runtime.Restart() }()
	secondStop := make(chan struct{})
	go func() { f.runtime.Stop(); close(secondStop) }()
	if len(f.runtime.ConnectionStatuses()) != 0 {
		t.Fatal("stopping generation still exposes workers")
	}
	if err := f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("PreCheck during Stop=%v", err)
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before the accepted Send completed")
	case <-secondStop:
		t.Fatal("concurrent Stop returned before the accepted Send completed")
	case err := <-restarted:
		t.Fatalf("Restart overtook the accepted Send: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	releaseSend()
	for _, done := range []<-chan struct{}{stopped, secondStop} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent Stop hung")
		}
	}
	select {
	case err := <-restarted:
		must(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent Restart hung")
	}
	if err := <-sent; err != nil && !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("accepted Send during Stop=%v", err)
	}
	stored, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if !found || !stored.SendAttempted || stored.VenueOrderID == "" {
		t.Fatal("Stop tore the accepted Send's durable acknowledgement")
	}
	f.venue.mu.Lock()
	f.venue.onSend = nil
	f.venue.mu.Unlock()
	orders := make([]domain.ExternalID, 16)
	for i := range orders {
		orders[i] = f.submit("1").ExternalID
	}
	results := make(chan error, len(orders)+8)
	wave := make(chan struct{})
	for _, id := range orders {
		go func() {
			<-wave
			_, err := f.runtime.Send(testCtx, id, f.dest)
			results <- err
		}()
	}
	for i := 0; i < 8; i++ {
		go func() {
			<-wave
			f.runtime.Stop()
			results <- f.runtime.Restart()
		}()
	}
	close(wave)
	for i := 0; i < cap(results); i++ {
		select {
		case err := <-results:
			if err != nil && !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("concurrent lifecycle/Send=%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent lifecycle/Send hung")
		}
	}
	must(t, f.runtime.Restart())
	f.send(f.submit("1"))
	f.runtime.Stop()
	f.venue.mu.Lock()
	builds, closes, active, maxActive := f.venue.builds, f.venue.closes, f.venue.active, f.venue.maxActive
	f.venue.mu.Unlock()
	if maxActive != 1 || active != 0 || builds != closes {
		t.Fatalf("overlapping or leaked generations: max active=%d active=%d builds=%d closes=%d", maxActive, active, builds, closes)
	}
}
