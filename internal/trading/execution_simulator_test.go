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
	"math/big"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/marketdata"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/openpit/param"
)

type testRestTimer struct {
	ticks    chan time.Time
	duration time.Duration
	stops    int
}

type testRestClock struct {
	mu    sync.Mutex
	at    time.Time
	armed chan *testRestTimer
}

func newTestRestClock() *testRestClock {
	return &testRestClock{at: testNow, armed: make(chan *testRestTimer, 256)}
}

func (c *testRestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testRestClock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}

func (c *testRestClock) newTimer(d time.Duration) (<-chan time.Time, func() bool) {
	timer := &testRestTimer{ticks: make(chan time.Time, 4), duration: d}
	c.armed <- timer
	return timer.ticks, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		timer.stops++
		return timer.stops == 1
	}
}

func (c *testRestClock) schedule(ctx context.Context, at time.Time, run func(time.Time)) (func() bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	var guard sync.Mutex
	ticks, stop := c.newTimer(at.Sub(c.now()))
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
			}
			guard.Lock()
			if ctx.Err() != nil {
				guard.Unlock()
				return
			}
			now := c.now()
			if now.Before(at) {
				stop()
				ticks, stop = c.newTimer(at.Sub(now))
				guard.Unlock()
				continue
			}
			guard.Unlock()
			run(now)
			return
		}
	}()
	return func() bool {
		guard.Lock()
		defer guard.Unlock()
		cancel()
		return stop()
	}, nil
}

func (c *testRestClock) next(t *testing.T) *testRestTimer {
	t.Helper()
	select {
	case timer := <-c.armed:
		return timer
	case <-time.After(3 * time.Second):
		t.Fatal("rest timer was not armed")
		return nil
	}
}

func testRestEvent(t *testing.T, events <-chan fwtrading.Event) fwtrading.Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("rest events closed unexpectedly")
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("rest event was not delivered")
		return fwtrading.Event{}
	}
}

func restPolicy(t *testing.T, infinite bool) domain.TestExecutionPolicyState {
	t.Helper()
	state := executePolicy(t)
	rule := &state.Policy.Rules[0]
	rule.Mode = "rest"
	seconds := int64(10)
	rule.Wait = &domain.TestExecutionWait{Kind: "finite", Seconds: &seconds}
	if infinite {
		rule.Wait = &domain.TestExecutionWait{Kind: "infinite"}
		rule.PriceVariant = ""
	}
	return state
}

func buildRestConnector(t *testing.T, build fwtrading.BuildContext, clock *testRestClock) (*testConnector, <-chan fwtrading.Event, context.CancelFunc) {
	t.Helper()
	build.Schedule = clock.schedule
	connector, err := newTestConnector(build, clock.now)
	tradingE2EMust(t, err)
	c := connector.(*testConnector)
	ctx, cancel := context.WithCancel(context.Background())
	events, err := c.Subscribe(ctx)
	tradingE2EMust(t, err)
	t.Cleanup(func() { cancel(); c.Close() })
	if event := testRestEvent(t, events); event.Kind != fwtrading.EventConnected {
		t.Fatalf("first event = %+v", event)
	}
	return c, events, cancel
}

func restSnapshot(t *testing.T, c *testConnector, id string) fwtrading.Snapshot {
	t.Helper()
	snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: id})
	requireTestBookLookup(t, found, err)
	if !found {
		t.Fatalf("rest entry %s missing", id)
	}
	return snapshot
}

func TestTestConnectorRestNoEarlyFill(t *testing.T) {
	build := testBuildContext(t)
	build.Store.(*testPolicyStore).state = restPolicy(t, false)
	quote := directTestQuote()
	quote.AsOf = testNow.Add(10 * time.Second)
	build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{quote}
	clock := newTestRestClock()
	c, events, _ := buildRestConnector(t, build, clock)
	order := testExecutionOrder()
	_, err := c.Send(context.Background(), order)
	tradingE2EMust(t, err)
	first := clock.next(t)
	if first.duration != 10*time.Second {
		t.Fatalf("duration = %v", first.duration)
	}
	assertOpen := func() {
		s := restSnapshot(t, c, order.ClientOrderID)
		if s.Status != fwtrading.VenueStatusOpen || s.FilledQuantity != "0" || len(s.Fills) != 0 {
			t.Fatalf("fill before deadline: %+v", s)
		}
	}
	assertOpen()
	clock.set(testNow.Add(10*time.Second - time.Nanosecond))
	assertOpen()
	first.ticks <- clock.now()
	second := clock.next(t)
	if second.duration != time.Nanosecond {
		t.Fatalf("early tick rearmed for %v, want 1ns", second.duration)
	}
	assertOpen()
	clock.set(testNow.Add(10 * time.Second))
	second.ticks <- clock.now()
	if event := testRestEvent(t, events); event.Kind != fwtrading.EventOrder {
		t.Fatalf("terminal event = %+v", event)
	}
	c.handlers.Wait()
	s := restSnapshot(t, c, order.ClientOrderID)
	if s.Status != fwtrading.VenueStatusFilled || len(s.Fills) != 1 || s.Fills[0].Price != "101" || !s.Fills[0].At.Equal(clock.now()) {
		t.Fatalf("deadline did not fill exactly once: %+v", s)
	}
	second.ticks <- clock.now().Add(time.Hour)
	first.ticks <- clock.now().Add(time.Hour)
	if got := restSnapshot(t, c, order.ClientOrderID); !reflect.DeepEqual(got, s) {
		t.Fatalf("duplicate tick changed terminal: %+v", got)
	}
	clock.mu.Lock()
	stops := second.stops
	clock.mu.Unlock()
	if stops != 1 {
		t.Fatalf("terminal timer stops = %d, want 1", stops)
	}
}

func TestTestConnectorRestSnapshot(t *testing.T) {
	build := testBuildContext(t)
	store := build.Store.(*testPolicyStore)
	store.state = restPolicy(t, false)
	q := directTestQuote()
	q.AsOf = testNow.Add(10 * time.Second)
	build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{q}
	clock := newTestRestClock()
	c, events, _ := buildRestConnector(t, build, clock)
	order := testExecutionOrder()
	ack, err := c.Send(context.Background(), order)
	tradingE2EMust(t, err)
	timer := clock.next(t)
	// Mutate the source pointers as well as replacing the policy, to prove ownership.
	*store.state.Policy.Rules[0].Wait.Seconds = 1
	store.state = executePolicy(t)
	store.state.Revision++
	store.state.Policy.Rules[0].PriceVariant = "best"
	retry, err := c.Send(context.Background(), order)
	tradingE2EMust(t, err)
	if retry != ack {
		t.Fatal("retry changed accepted Ack")
	}
	clock.set(testNow.Add(10 * time.Second))
	timer.ticks <- clock.now()
	testRestEvent(t, events)
	c.handlers.Wait()
	old := restSnapshot(t, c, order.ClientOrderID)
	if len(old.Fills) != 1 || old.Fills[0].Price != "101" {
		t.Fatalf("accepted rule was replaced: %+v", old)
	}
	order.ClientOrderID = "new"
	_, err = c.Send(context.Background(), order)
	tradingE2EMust(t, err)
	newOrder := restSnapshot(t, c, order.ClientOrderID)
	if len(newOrder.Fills) != 1 || newOrder.Fills[0].Price != "99" || store.reads != 2 {
		t.Fatalf("new policy not applied: %+v reads=%d", newOrder, store.reads)
	}
}

func TestTestConnectorRestLifecycle(t *testing.T) {
	for _, action := range []string{"stop", "close", "release"} {
		t.Run(action, func(t *testing.T) {
			build := testBuildContext(t)
			build.Store.(*testPolicyStore).state = restPolicy(t, false)
			entered, proceed := make(chan struct{}), make(chan struct{})
			build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{directTestQuote()}
			build.Quotes.(*testQuotes).onRead = func() { close(entered); <-proceed }
			clock := newTestRestClock()
			c, _, cancel := buildRestConnector(t, build, clock)
			_, err := c.Send(context.Background(), testExecutionOrder())
			tradingE2EMust(t, err)
			timer := clock.next(t)
			c.mu.Lock()
			entry := c.book["client"]
			c.mu.Unlock()
			clock.set(testNow.Add(10 * time.Second))
			timer.ticks <- clock.now()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("quote read not reached")
			}
			closed := make(chan struct{})
			switch action {
			case "stop":
				cancel()
			case "release":
				c.ReleaseOrder("client")
			case "close":
				go func() { c.Close(); close(closed) }()
				<-c.ctx.Done()
				select {
				case <-closed:
					t.Fatal("Close did not wait for quote handler")
				default:
				}
			}
			close(proceed)
			c.handlers.Wait()
			if action == "close" {
				<-closed
			}
			c.mu.Lock()
			snapshot := entry.snapshot
			c.mu.Unlock()
			if len(snapshot.Fills) != 0 || snapshot.Status != fwtrading.VenueStatusOpen {
				t.Fatalf("late commit after %s: %+v", action, snapshot)
			}
			timer.ticks <- clock.now()
			c.Close()
			c.Close()
		})
	}
}

func TestTestConnectorRestStoppedTick(t *testing.T) {
	build := testBuildContext(t)
	build.Store.(*testPolicyStore).state = restPolicy(t, false)
	build.Quotes.(*testQuotes).onRead = func() { t.Error("stopped timer read a quote") }
	clock := newTestRestClock()
	c, events, cancel := buildRestConnector(t, build, clock)
	_, err := c.Send(context.Background(), testExecutionOrder())
	tradingE2EMust(t, err)
	timer := clock.next(t)
	cancel()
	clock.set(testNow.Add(time.Hour))
	timer.ticks <- clock.now()
	c.handlers.Wait()
	if s := restSnapshot(t, c, "client"); len(s.Fills) != 0 {
		t.Fatalf("tick after Stop filled: %+v", s)
	}
	c.Close()
	if _, ok := <-events; ok {
		t.Fatal("Close left events open")
	}
	clock.mu.Lock()
	stops := timer.stops
	clock.mu.Unlock()
	if stops != 1 {
		t.Fatalf("Close timer stops = %d", stops)
	}
}

func TestTestConnectorRestAcceptance(t *testing.T) {
	for _, when := range []string{"before", "asset read", "infinite", "overflow"} {
		t.Run(when, func(t *testing.T) {
			build := testBuildContext(t)
			build.Store.(*testPolicyStore).state = restPolicy(t, when == "infinite")
			build.Quotes.(*testQuotes).onRead = func() { t.Error("rest read price at acceptance") }
			clock := newTestRestClock()
			if when == "overflow" {
				clock.set(time.Unix(math.MaxInt64-62135596800, 0))
			}
			c, _, _ := buildRestConnector(t, build, clock)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if when == "before" {
				cancel()
			}
			if when == "asset read" {
				build.Store.(*testPolicyStore).onAsset = cancel
			}
			ack, err := c.Send(ctx, testExecutionOrder())
			s, found, lookupErr := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "client"})
			requireTestBookLookup(t, found, lookupErr)
			if when == "infinite" {
				tradingE2EMust(t, err)
				clock.set(testNow.Add(1000 * time.Hour))
				if !found || s.Status != fwtrading.VenueStatusOpen || len(s.Fills) != 0 || ack.VenueOrderID == "" {
					t.Fatalf("infinite = %+v %v", s, err)
				}
			} else if when == "overflow" {
				assertTestRefusal(t, err, "test_deadline_overflow", []int{0})
				if !found || s.Status != fwtrading.VenueStatusCancelled {
					t.Fatalf("overflow = %+v", s)
				}
			} else if !errors.Is(err, context.Canceled) || found {
				t.Fatalf("ctx error left entry: %v %+v", err, s)
			}
			select {
			case <-clock.armed:
				t.Fatal("unexpected timer after refusal/infinite/cancellation")
			default:
			}
		})
	}
}

func requireTestBookLookup(t *testing.T, found bool, err error) {
	t.Helper()
	if found && err != nil || !found && !errors.Is(err, fwtrading.ErrOrderGone) {
		t.Fatalf("book lookup found=%t: %v", found, err)
	}
}

func TestTestConnectorRestMissingAsk(t *testing.T) {
	build := testBuildContext(t)
	build.Store.(*testPolicyStore).state = restPolicy(t, false)
	q := directTestQuote()
	q.AsOf, q.Ask = testNow.Add(10*time.Second), ""
	build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{q}
	clock := newTestRestClock()
	c, events, _ := buildRestConnector(t, build, clock)
	diagnostics := make(chan health.Diagnostic, 2)
	c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics <- d })
	_, err := c.Send(context.Background(), testExecutionOrder())
	tradingE2EMust(t, err)
	timer := clock.next(t)
	deadline := testNow.Add(10 * time.Second)
	// The injected clock reports a local offset; the diagnostic must still be UTC.
	clock.set(deadline.In(time.FixedZone("EET", 2*60*60)))
	timer.ticks <- clock.now()
	testRestEvent(t, events)
	c.handlers.Wait()
	s := restSnapshot(t, c, "client")
	if s.Status != fwtrading.VenueStatusCancelled || len(s.Fills) != 0 || !strings.Contains(s.Reason, "has no ask") {
		t.Fatalf("missing ask = %+v", s)
	}
	select {
	case d := <-diagnostics:
		if d.Code != "test_quote_side_missing" || d.Instrument != "AAPL/USD" || d.Detail != s.Reason {
			t.Fatalf("diagnostic = %+v", d)
		}
		if d.At.Location() != time.UTC || !d.At.Equal(deadline) {
			t.Fatalf("diagnostic At = %v, want %v in UTC", d.At, deadline)
		}
	default:
		t.Fatal("delayed refusal diagnostic missing")
	}
	select {
	case d := <-diagnostics:
		t.Fatalf("duplicate diagnostic: %+v", d)
	default:
	}
}

func FuzzTestSequence(f *testing.F) {
	for _, seed := range [][]byte{
		{0, 5, 5, 5, 1, 2, 3},
		{0, 164, 64, 5, 5, 1},
		{0, 8, 5, 13, 5, 13, 6, 14, 7, 15, 3, 11},
		{20, 0, 5, 6, 7, 3},
		{52, 0, 1, 2, 5, 6, 7, 3},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sequence []byte) {
		if len(sequence) > 128 {
			sequence = sequence[:128]
		}
		type expected struct {
			order    fwtrading.Order
			mode     domain.TestExecutionMode
			price    string
			deadline time.Time
			terminal bool
		}
		type connection struct {
			c      *testConnector
			events <-chan fwtrading.Event
			cancel context.CancelFunc
			clock  *testRestClock
			build  fwtrading.BuildContext
			orders map[string]expected
			timers map[string]*testRestTimer
			active bool
		}
		var connections [2]*connection
		for i := range connections {
			build := testBuildContext(t)
			build.Connection.ExternalID = domain.ExternalID(fmt.Sprintf("connection-%d", i))
			build.Store.(*testPolicyStore).state = restPolicy(t, false)
			build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{directTestQuote()}
			x := &connection{build: build, clock: newTestRestClock(), orders: map[string]expected{}, timers: map[string]*testRestTimer{}, active: true}
			x.c, x.events, x.cancel = buildRestConnector(t, build, x.clock)
			connections[i] = x
		}
		check := func(x *connection) {
			for id, want := range x.orders {
				s := restSnapshot(t, x.c, id)
				if s.VenueOrderID != "test:"+x.build.Connection.ExternalID.String()+":"+id {
					t.Fatalf("connection identity lost: %+v", s)
				}
				if !want.terminal {
					if s.Status != fwtrading.VenueStatusOpen || len(s.Fills) != 0 || s.FilledQuantity != "0" {
						t.Fatalf("reference open, got %+v", s)
					}
					continue
				}
				count := 1
				if want.mode == "split" {
					count = 3
				}
				if s.Status != fwtrading.VenueStatusFilled || len(s.Fills) != count || s.FilledQuantity != want.order.Quantity {
					t.Fatalf("reference terminal count=%d, got %+v", count, s)
				}
				sum := decimal.Zero
				for j, fill := range s.Fills {
					q := decimal.RequireFromString(fill.Quantity)
					sum = sum.Add(q)
					leaves := decimal.RequireFromString(want.order.Quantity).Sub(sum)
					if !q.IsPositive() || !decimal.RequireFromString(fill.CumQuantity).Equal(sum) || !decimal.RequireFromString(fill.LeavesQuantity).Equal(leaves) || fill.Final != (j == count-1) || fill.Price != want.price {
						t.Fatalf("sequence fill invariant: %+v want price=%s", fill, want.price)
					}
					if want.mode == "rest" && fill.At.Before(want.deadline) {
						t.Fatalf("sequence early fill: %+v", fill)
					}
				}
				if !sum.Equal(decimal.RequireFromString(want.order.Quantity)) {
					t.Fatalf("sum = %s", sum)
				}
			}
		}
		for _, b := range sequence {
			x := connections[(b/8)%2]
			id := fmt.Sprintf("order-%d", (b/64)%2)
			op := b % 8
			if !x.active && op != 7 {
				_, found, err := x.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: id})
				requireTestBookLookup(t, found, err)
				if found {
					t.Fatal("stopped book survived")
				}
				continue
			}
			switch op {
			case 0, 1:
				order := testExecutionOrder()
				order.ClientOrderID, order.Quantity = id, "1.00"
				want, exists := x.orders[id]
				var before fwtrading.Snapshot
				if exists {
					before = restSnapshot(t, x.c, id)
				}
				x.build.Quotes.(*testQuotes).snapshots[0].AsOf = x.clock.now()
				ack, err := x.c.Send(context.Background(), order)
				tradingE2EMust(t, err)
				if ack.VenueOrderID == "" {
					t.Fatal("missing Ack")
				}
				if exists {
					if after := restSnapshot(t, x.c, id); !reflect.DeepEqual(before, after) {
						t.Fatalf("retry changed result: %+v -> %+v", before, after)
					}
					break
				}
				rule := x.build.Store.(*testPolicyStore).state.Policy.Rules[0]
				want = expected{order: order, mode: rule.Mode, price: "101", terminal: rule.Mode != "rest"}
				if rule.PriceVariant == "best" {
					want.price = "99"
				}
				if rule.Mode == "rest" && rule.Wait.Kind == "finite" {
					want.deadline = x.clock.now().Add(10 * time.Second)
					x.timers[id] = x.clock.next(t)
				} else if want.terminal {
					testRestEvent(t, x.events)
				}
				x.orders[id] = want
			case 2:
				if want, ok := x.orders[id]; ok {
					order := want.order
					order.Quantity = "2.00"
					_, err := x.c.Send(context.Background(), order)
					if !errors.Is(err, domain.ErrConflict) {
						t.Fatalf("conflict = %v", err)
					}
				}
			case 3:
				_, found, err := x.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: id})
				requireTestBookLookup(t, found, err)
				_, exists := x.orders[id]
				if found != exists {
					t.Fatalf("reference existence=%v found=%v", exists, found)
				}
			case 4:
				store := x.build.Store.(*testPolicyStore)
				next := restPolicy(t, false)
				next.Revision = store.state.Revision + 1
				switch (b / 16) % 4 {
				case 1:
					next = restPolicy(t, true)
					next.Revision = store.state.Revision + 1
				case 2:
					next.Policy.Rules[0].Mode, next.Policy.Rules[0].Wait = "execute", nil
				case 3:
					fills := 3
					next.Policy.Rules[0].Mode, next.Policy.Rules[0].Wait, next.Policy.Rules[0].Fills = "split", nil, &fills
				}
				if next.Policy.Rules[0].PriceVariant != "" && b&128 != 0 {
					next.Policy.Rules[0].PriceVariant = "best"
				}
				store.state = next
			case 5:
				x.clock.set(x.clock.now().Add(5 * time.Second))
				x.build.Quotes.(*testQuotes).snapshots[0].AsOf = x.clock.now()
				for id, timer := range x.timers {
					want := x.orders[id]
					if want.terminal {
						select {
						case timer.ticks <- x.clock.now():
						default:
						}
						continue
					}
					timer.ticks <- x.clock.now()
					if x.clock.now().Before(want.deadline) {
						x.timers[id] = x.clock.next(t)
					} else {
						testRestEvent(t, x.events)
						want.terminal = true
						x.orders[id] = want
					}
				}
			case 6:
				x.cancel()
				x.c.Close()
				for _, timer := range x.timers {
					select {
					case timer.ticks <- x.clock.now():
					default:
					}
				}
				x.active = false
				x.orders = map[string]expected{}
			case 7:
				if !x.active {
					x.c, x.events, x.cancel = buildRestConnector(t, x.build, x.clock)
					x.active = true
					x.timers = map[string]*testRestTimer{}
				}
			}
			for _, other := range connections {
				check(other)
			}
		}
	})
}

type testQuotes struct {
	snapshots []marketdata.QuoteSnapshot
	onRead    func()
}

func (q *testQuotes) QuoteSnapshots() []marketdata.QuoteSnapshot {
	if q.onRead != nil {
		q.onRead()
	}
	return q.snapshots
}

type testPolicyStore struct {
	state   domain.TestExecutionPolicyState
	reads   int
	err     error
	onAsset func()
}

func (s *testPolicyStore) GetTestExecutionPolicy(context.Context, domain.ExternalID) (domain.TestExecutionPolicyState, error) {
	s.reads++
	return s.state, s.err
}

func (s *testPolicyStore) GetAsset(_ context.Context, code string) (domain.Asset, bool, error) {
	if s.onAsset != nil {
		s.onAsset()
	}
	ids := map[string]domain.EngineAssetID{"AAPL": 1, "USD": 2}
	id, found := ids[code]
	return domain.Asset{Code: code, EngineAssetID: id}, found, nil
}

var testNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func executePolicy(t *testing.T) domain.TestExecutionPolicyState {
	t.Helper()
	mask, err := domain.ParseInstrumentMask("AAPL", "USD")
	tradingE2EMust(t, err)
	return domain.TestExecutionPolicyState{Revision: 1, Policy: domain.TestExecutionPolicy{
		Rules: []domain.TestExecutionRule{{Mask: mask, Mode: "execute", PriceVariant: "normal"}},
	}}
}

func testBuildContext(t *testing.T) fwtrading.BuildContext {
	t.Helper()
	return fwtrading.BuildContext{Connection: domain.TradingConnection{
		ExternalID: "connection", Provider: "test", Mode: domain.TradingModePaper,
		Credentials: "{}", Enabled: true}, Quotes: &testQuotes{},
		Store: &testPolicyStore{state: executePolicy(t)}}
}

func buildTestConnector(t *testing.T, build fwtrading.BuildContext) fwtrading.Connector {
	t.Helper()
	build.Schedule = func(context.Context, time.Time, func(time.Time)) (func() bool, error) {
		t.Fatal("execute scheduled a timer")
		return nil, nil
	}
	c, err := newTestConnector(build, func() time.Time { return testNow })
	tradingE2EMust(t, err)
	t.Cleanup(c.Close)
	return c
}

func testExecutionOrder() fwtrading.Order {
	return fwtrading.Order{ClientOrderID: "client", Symbol: "opaque-route-id",
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, Quantity: "2", Route: "{}"}
}

func directTestQuote() marketdata.QuoteSnapshot {
	return marketdata.QuoteSnapshot{BaseAssetID: 1, QuoteAssetID: 2,
		MarketDataQuote: domain.MarketDataQuote{Instance: "feed", ExternalSymbol: "symbol",
			AsOf: testNow, ReceivedAt: testNow, Bid: "99", Ask: "101"}}
}

func TestTestConnectorPrices(t *testing.T) {
	for _, side := range []domain.OrderSide{domain.OrderSideBuy, domain.OrderSideSell} {
		for _, variant := range []domain.TestPriceVariant{"normal", "best", "worst"} {
			t.Run(string(side)+"/"+string(variant), func(t *testing.T) {
				build := testBuildContext(t)
				build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{directTestQuote()}
				build.Store.(*testPolicyStore).state.Policy.Rules[0].PriceVariant = variant
				c := buildTestConnector(t, build)
				order := testExecutionOrder()
				order.Side = side
				_, err := c.Send(context.Background(), order)
				tradingE2EMust(t, err)
				snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID})
				requireTestBookLookup(t, found, err)
				want := "101"
				if (side == domain.OrderSideSell) != (variant == "best") {
					want = "99"
				}
				if !found || len(snapshot.Fills) != 1 || snapshot.Fills[0].Price != want ||
					snapshot.Fills[0].Quantity != "2" || !snapshot.Fills[0].Final || snapshot.Fills[0].LeavesQuantity != "0" {
					t.Fatalf("execution = %+v, want price %s", snapshot, want)
				}
			})
		}
	}
	t.Run("limit without quotes", func(t *testing.T) {
		build := testBuildContext(t)
		build.Quotes.(*testQuotes).onRead = func() { t.Fatal("limit read quotes") }
		c := buildTestConnector(t, build)
		order := testExecutionOrder()
		order.LimitPrice = "105"
		_, err := c.Send(context.Background(), order)
		tradingE2EMust(t, err)
		snapshot, _, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID})
		tradingE2EMust(t, err)
		if len(snapshot.Fills) != 1 || snapshot.Fills[0].Price != "105" {
			t.Fatalf("limit execution = %+v", snapshot)
		}
	})
}

func TestTestConnectorQuoteSelection(t *testing.T) {
	direct := directTestQuote()
	inverse := direct
	inverse.BaseAssetID, inverse.QuoteAssetID = 2, 1
	inverse.Bid, inverse.Ask = "0.01", "0.02"
	mark := direct
	mark.Bid, mark.Ask, mark.Mark = "", "", "100"
	second := direct
	second.ExternalSymbol, second.ReceivedAt = "second", testNow.Add(time.Hour)
	for _, tc := range []struct {
		name   string
		quotes []marketdata.QuoteSnapshot
		side   domain.OrderSide
		price  string
		code   string
	}{
		{"missing", nil, domain.OrderSideBuy, "", "test_quote_missing"},
		{"mark only", []marketdata.QuoteSnapshot{mark}, domain.OrderSideBuy, "", "test_quote_side_missing"},
		{"direct beats inverse", []marketdata.QuoteSnapshot{inverse, direct}, domain.OrderSideBuy, "101", ""},
		{"inverse buy", []marketdata.QuoteSnapshot{inverse}, domain.OrderSideBuy, "100", ""},
		{"inverse sell", []marketdata.QuoteSnapshot{inverse}, domain.OrderSideSell, "50", ""},
		{"ambiguous", []marketdata.QuoteSnapshot{direct, second}, domain.OrderSideBuy, "", "test_quote_ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := testBuildContext(t)
			build.Quotes.(*testQuotes).snapshots = tc.quotes
			c := buildTestConnector(t, build)
			order := testExecutionOrder()
			order.Side = tc.side
			_, err := c.Send(context.Background(), order)
			snapshot, found, lookupErr := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID})
			requireTestBookLookup(t, found, lookupErr)
			if !found || snapshot.VenueOrderID != "test:connection:client" {
				t.Fatalf("snapshot = %+v, found=%v", snapshot, found)
			}
			if tc.code != "" {
				refusal := requireTestRefusal(t, err, tc.code, []int{0})
				if tc.code == "test_quote_ambiguous" && (!strings.Contains(refusal.Message, "feed/symbol") || !strings.Contains(refusal.Message, "feed/second")) {
					t.Fatal("ambiguity omitted its sources")
				}
				if len(snapshot.Fills) != 0 || snapshot.FilledQuantity != "0" || snapshot.Status != fwtrading.VenueStatusCancelled {
					t.Fatalf("refusal invented execution: %+v", snapshot)
				}
			} else {
				tradingE2EMust(t, err)
				if len(snapshot.Fills) != 1 || snapshot.Fills[0].Price != tc.price {
					t.Fatalf("execution = %+v, want price %s", snapshot, tc.price)
				}
			}
		})
	}
}

func TestTestConnectorFreshness(t *testing.T) {
	for _, inverse := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			asOf time.Time
			code string
		}{
			{"within", testNow.Add(-marketdata.FreshnessTTL + time.Nanosecond), ""},
			{"boundary", testNow.Add(-marketdata.FreshnessTTL), "test_quote_stale"},
			{"past", testNow.Add(-marketdata.FreshnessTTL - time.Nanosecond), "test_quote_stale"},
			{"future", testNow.Add(time.Second), ""},
			{"zero", time.Time{}, "test_quote_invalid"},
		} {
			name := tc.name
			if inverse {
				name += "/inverse"
			}
			t.Run(name, func(t *testing.T) {
				quote := directTestQuote()
				quote.AsOf = tc.asOf
				if inverse {
					quote.BaseAssetID, quote.QuoteAssetID = 2, 1
					quote.Bid, quote.Ask = "0.01", "0.02"
				}
				build := testBuildContext(t)
				build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{quote}
				c := buildTestConnector(t, build)
				_, err := c.Send(context.Background(), testExecutionOrder())
				if tc.code != "" {
					assertTestRefusal(t, err, tc.code, []int{0})
				} else {
					tradingE2EMust(t, err)
				}
			})
		}
	}
}

func assertTestRefusal(t *testing.T, err error, code string, indexes []int) {
	t.Helper()
	var refusal *fwtrading.TestExecutionRefusal
	if !errors.As(err, &refusal) || !errors.Is(err, fwtrading.ErrRejected) {
		t.Fatalf("refusal = %v, want %s", err, code)
	}
	if refusal.Code != code || refusal.BaseAsset != "AAPL" || refusal.QuoteAsset != "USD" || !reflect.DeepEqual(refusal.RuleIndexes, indexes) {
		t.Fatalf("refusal = %+v, want %s indexes %v", refusal, code, indexes)
	}
}

// requireTestRefusal asserts the refusal like assertTestRefusal and returns it
// for a test that inspects or mutates it.
func requireTestRefusal(t *testing.T, err error, code string, indexes []int) *fwtrading.TestExecutionRefusal {
	t.Helper()
	assertTestRefusal(t, err, code, indexes)
	var refusal *fwtrading.TestExecutionRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal = %v, want %s", err, code)
	}
	return refusal
}

func TestTestConnectorFirstMatchingSymbol(t *testing.T) {
	for _, masks := range [][]string{{"*", "AAPL"}, {"AAPL", "*"}, {"MSFT", "A*", "*"}} {
		t.Run(strings.Join(masks, ","), func(t *testing.T) {
			build := testBuildContext(t)
			store := build.Store.(*testPolicyStore)
			store.state.Policy.Rules = nil
			first := -1
			for i, mask := range masks {
				mode := domain.TestExecutionMode("execute")
				variant := domain.TestPriceVariant("normal")
				var wait *domain.TestExecutionWait
				if first >= 0 {
					mode = "rest"
					variant = ""
					wait = &domain.TestExecutionWait{Kind: "infinite"}
				} else if mask != "MSFT" {
					first = i
				}
				parsed, err := domain.ParseInstrumentMask(mask, "USD")
				tradingE2EMust(t, err)
				store.state.Policy.Rules = append(store.state.Policy.Rules, domain.TestExecutionRule{
					Mask: parsed,
					Mode: mode, PriceVariant: variant, Wait: wait,
				})
			}
			c := buildTestConnector(t, build)
			order := testExecutionOrder()
			order.LimitPrice = "105"
			_, err := c.Send(context.Background(), order)
			tradingE2EMust(t, err)
			snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID})
			requireTestBookLookup(t, found, err)
			if !found || snapshot.Status != fwtrading.VenueStatusFilled || c.(*testConnector).book[order.ClientOrderID].ruleIndex != first {
				t.Fatalf("first matching rule %d was not executed: %+v", first, snapshot)
			}
		})
	}
}

func TestTestConnectorRetriesAndLookup(t *testing.T) {
	for _, outcome := range []string{"execute", "no_match", "overlap"} {
		t.Run(outcome, func(t *testing.T) {
			build := testBuildContext(t)
			st := build.Store.(*testPolicyStore)
			switch outcome {
			case "no_match":
				st.state.Policy.Rules = []domain.TestExecutionRule{}
			case "overlap":
				st.state.Policy.Rules = append(st.state.Policy.Rules, st.state.Policy.Rules[0])
			}
			c := buildTestConnector(t, build)
			order := testExecutionOrder()
			order.LimitPrice = "105"
			ack, err := c.Send(context.Background(), order)
			switch outcome {
			case "execute", "overlap":
				tradingE2EMust(t, err)
			case "no_match":
				assertTestRefusal(t, err, "test_policy_no_match", []int{})
			}
			st.state.Policy.Rules = nil
			st.err = errors.New("a retry must not re-read policy")
			retryAck, retryErr := c.Send(context.Background(), order)
			if retryAck != ack || (err == nil) != (retryErr == nil) || st.reads != 1 {
				t.Fatalf("retry = %+v %v, policy reads %d", retryAck, retryErr, st.reads)
			}
			snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID})
			requireTestBookLookup(t, found, err)
			if !found || (outcome != "no_match" && len(snapshot.Fills) != 1) {
				t.Fatalf("retry snapshot = %+v", snapshot)
			}
			if len(snapshot.Fills) != 0 {
				snapshot.Fills[0].Price = "999"
				copy, _, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID})
				tradingE2EMust(t, err)
				if copy.Fills[0].Price != "105" {
					t.Fatal("Lookup shared its fills")
				}
			}
			order.Quantity = "3"
			if _, err := c.Send(context.Background(), order); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("conflicting retry = %v", err)
			}
			c.(interface{ ReleaseOrder(string) }).ReleaseOrder(order.ClientOrderID)
			c.(interface{ ReleaseOrder(string) }).ReleaseOrder(order.ClientOrderID)
			snapshot, found, err = c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID})
			requireTestBookLookup(t, found, err)
			if found || !reflect.DeepEqual(snapshot, fwtrading.Snapshot{}) {
				t.Fatalf("missing Lookup = %+v, %v", snapshot, found)
			}
		})
	}
}

func TestTestConnectorAcceptanceContext(t *testing.T) {
	for _, when := range []string{"before", "asset read", "transient", "after acceptance"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			build := testBuildContext(t)
			build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{directTestQuote()}
			switch when {
			case "before":
				cancel()
			case "asset read":
				build.Store.(*testPolicyStore).onAsset = cancel
			case "transient":
				build.Store.(*testPolicyStore).err = errors.New("temporary store failure")
			case "after acceptance":
				build.Quotes.(*testQuotes).onRead = cancel
			}
			c := buildTestConnector(t, build)
			ack, err := c.Send(ctx, testExecutionOrder())
			_, found, lookupErr := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "client"})
			requireTestBookLookup(t, found, lookupErr)
			if when == "after acceptance" {
				if err != nil || !found || ack.VenueOrderID == "" {
					t.Fatalf("accepted outcome lost to cancellation: %+v %v found=%v", ack, err, found)
				}
			} else if err == nil || found {
				t.Fatalf("pre-acceptance error left an entry: %v found=%v", err, found)
			}
		})
	}
}

func TestTestProviderValidationAndDisabledSubscribe(t *testing.T) {
	provider := TestProvider()
	if provider.Type != "test" || provider.Title != "Execution Simulator" || provider.ExecutionPolicy != true ||
		provider.VenueAccounts || provider.UnlinkedFees || provider.VerifiesSymbols || provider.SearchesSymbols {
		t.Fatalf("test provider capabilities = %+v", provider)
	}
	for _, invalid := range []string{"Quotes", "Store", "mode", "credentials"} {
		t.Run(invalid, func(t *testing.T) {
			build := testBuildContext(t)
			switch invalid {
			case "Quotes":
				build.Quotes = nil
			case "Store":
				build.Store = nil
			case "mode":
				build.Connection.Mode = domain.TradingModeLive
			case "credentials":
				build.Connection.Credentials = `{"token":"secret"}`
			}
			if c, err := TestProvider().Build(build); err == nil {
				c.Close()
				t.Fatal("invalid Build accepted")
			} else if invalid == "credentials" && (domain.ValidationPointer(err) != "/credentials/token" || domain.ValidationConstraint(err) != "forbidden") {
				t.Fatalf("credential error = %v", err)
			}
		})
	}
	build := testBuildContext(t)
	build.Connection.Enabled = false
	c := buildTestConnector(t, build)
	checker := c.(fwtrading.AccountChecker)
	first, err := checker.CheckAccount(context.Background())
	tradingE2EMust(t, err)
	second, err := checker.CheckAccount(context.Background())
	tradingE2EMust(t, err)
	if !strings.HasPrefix(first.Identity, "test:") || first.Identity == second.Identity {
		t.Fatalf("identities = %+v, %+v", first, second)
	}
	for _, kind := range []string{"volume", "route"} {
		order := testExecutionOrder()
		if kind == "volume" {
			order.AmountKind = domain.OrderAmountKindVolume
		} else {
			order.Route = `{"timeInForce":"day"}`
		}
		err := c.(fwtrading.PreChecker).PreCheck(context.Background(), order)
		if err == nil || !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s pre-check = %v", kind, err)
		}
		if kind == "volume" && (!errors.Is(err, fwtrading.ErrUnsupported) || domain.ValidationPointer(err) != "/amountKind" || domain.ValidationConstraint(err) != "enum") {
			t.Fatalf("volume pre-check = %v", err)
		}
		if kind == "route" && domain.ValidationConstraint(err) != "forbidden" {
			t.Fatalf("route pre-check = %v", err)
		}
	}
	if build.Store.(*testPolicyStore).reads != 0 {
		t.Fatal("Build/CheckAccount/PreCheck read the policy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events, err := c.Subscribe(ctx)
	tradingE2EMust(t, err)
	select {
	case event := <-events:
		if event.Kind != fwtrading.EventConnected {
			t.Fatalf("first event = %+v", event)
		}
	case <-ctx.Done():
		t.Fatal("disabled connection did not connect for reconcile")
	}
}

func testSplitOracle(t testing.TB, raw string, n int) ([]string, bool) {
	t.Helper()
	value, err := decimal.NewFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	exponent := value.Exponent()
	units := value.Coefficient()
	if exponent > 0 {
		units.Mul(units, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil))
		exponent = 0
	}
	if units.Cmp(big.NewInt(int64(n))) < 0 {
		return nil, false
	}
	// The ith chunk is ceil((U-i)/n), independently of QuoRem.
	want := make([]string, n)
	for i := range want {
		numerator := new(big.Int).Add(units, big.NewInt(int64(n-1-i)))
		coefficient := new(big.Int).Quo(numerator, big.NewInt(int64(n)))
		want[i] = decimal.NewFromBigInt(coefficient, exponent).StringFixed(-exponent)
	}
	return want, true
}

func checkTestSplit(t *testing.T, raw string, n int) {
	t.Helper()
	build := testBuildContext(t)
	rule := &build.Store.(*testPolicyStore).state.Policy.Rules[0]
	rule.Mode, rule.Fills = "split", &n
	reads := 0
	build.Quotes.(*testQuotes).snapshots = []marketdata.QuoteSnapshot{directTestQuote()}
	build.Quotes.(*testQuotes).onRead = func() { reads++ }
	c := buildTestConnector(t, build)
	order := testExecutionOrder()
	order.Quantity = raw
	_, err := c.Send(context.Background(), order)
	snapshot, found, lookupErr := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID})
	requireTestBookLookup(t, found, lookupErr)
	if !found {
		t.Fatal("split result missing from book")
	}
	want, representable := testSplitOracle(t, raw, n)
	if !representable {
		assertTestRefusal(t, err, "test_split_unrepresentable", []int{0})
		if len(snapshot.Fills) != 0 || snapshot.FilledQuantity != "0" || snapshot.Status != fwtrading.VenueStatusCancelled {
			t.Fatalf("refused split emitted fills: %+v", snapshot)
		}
		return
	}
	tradingE2EMust(t, err)
	if snapshot.Status != fwtrading.VenueStatusFilled || len(snapshot.Fills) != n || reads != 1 {
		t.Fatalf("split result = %+v, quote reads = %d", snapshot, reads)
	}
	total := decimal.RequireFromString(raw)
	sum := decimal.Zero
	for i, fill := range snapshot.Fills {
		sum = sum.Add(decimal.RequireFromString(want[i]))
		leaves := total.Sub(sum)
		if fill.Quantity != want[i] || !decimal.RequireFromString(fill.CumQuantity).Equal(sum) ||
			!decimal.RequireFromString(fill.LeavesQuantity).Equal(leaves) ||
			fill.Final != (i == n-1) || fill.Price != "101" || fill.Commission != nil ||
			!fill.At.Equal(testNow) {
			t.Fatalf("fill %d = %+v, want %s cum %s leaves %s", i, fill, want[i], sum, leaves)
		}
		if i < n-1 && !leaves.IsPositive() {
			t.Fatal("partial report has no positive leaves")
		}
		if fill.Final && fill.LeavesQuantity != "0" {
			t.Fatal("final report leaves is not zero")
		}
	}
	if !sum.Equal(total) {
		t.Fatalf("split sum = %s, want %s", sum, total)
	}
}

func TestTestConnectorSplit(t *testing.T) {
	for _, raw := range []string{
		"1.00", "1", "0.02", "0.03", "1.0000", "20",
		"100000000000000000000", "79228162514264337593543950335",
		"0.0000000000000000000000000003", "1.0000000000000000000000000000",
	} {
		for _, n := range []int{2, 3} {
			t.Run(fmt.Sprintf("%s/%d", raw, n), func(t *testing.T) {
				if _, err := param.NewQuantityFromString(raw); err != nil {
					t.Fatalf("SDK boundary fixture is invalid: %v", err)
				}
				checkTestSplit(t, raw, n)
			})
		}
	}
}

func FuzzTestSplit(f *testing.F) {
	for _, raw := range []string{"1.00", "1", "0.02", "0.03", "1.0000",
		"100000000000000000000", "79228162514264337593543950335",
		"0.0000000000000000000000000003"} {
		for _, n := range []uint8{2, 3} {
			f.Add(raw, n)
		}
	}
	f.Fuzz(func(t *testing.T, raw string, n uint8) {
		if len(raw) > 128 || (n != 2 && n != 3) {
			t.Skip()
		}
		value, err := decimal.NewFromString(raw)
		if err != nil || !value.IsPositive() || value.Exponent() < -28 || value.Exponent() > 28 {
			t.Skip()
		}
		quantity, err := param.NewQuantityFromString(raw)
		if err != nil || !quantity.Decimal().Equal(value) {
			t.Skip()
		}
		checkTestSplit(t, raw, int(n))
	})
}

func TestTestConnectorRejectEven(t *testing.T) {
	build := testBuildContext(t)
	store := build.Store.(*testPolicyStore)
	store.state.Policy.Rules[0].Mode = "reject_even"
	c := buildTestConnector(t, build)
	send := func(key, base, quote string, rejected bool) {
		t.Helper()
		order := testExecutionOrder()
		order.ClientOrderID, order.BaseAsset, order.QuoteAsset, order.LimitPrice = key, base, quote, "100"
		ack, err := c.Send(context.Background(), order)
		if rejected {
			var refusal *fwtrading.TestExecutionRefusal
			if !errors.As(err, &refusal) || refusal.Code != "test_policy_even_rejected" ||
				refusal.Message != "Test policy rejected every second order" {
				t.Fatalf("key %s refusal = %v", key, err)
			}
		} else if err != nil || ack.VenueOrderID == "" {
			t.Fatalf("key %s ack = %+v, error = %v", key, ack, err)
		}
	}
	order := testExecutionOrder()
	for range 3 {
		tradingE2EMust(t, c.(*testConnector).PreCheck(context.Background(), order))
		_, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "missing"})
		requireTestBookLookup(t, found, err)
	}
	for _, tc := range []struct {
		key      string
		rejected bool
	}{{"a", false}, {"b", true}, {"a", false}, {"c", false}, {"d", true}, {"b", true}} {
		send(tc.key, "AAPL", "USD", tc.rejected)
		_, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: tc.key})
		requireTestBookLookup(t, found, err)
		if !found {
			t.Fatal("counter result missing")
		}
	}
	otherBuild := build
	otherBuild.Connection.ExternalID = "other-connection"
	other := buildTestConnector(t, otherBuild)
	_, err := other.Send(context.Background(), fwtrading.Order{
		ClientOrderID: "other", BaseAsset: "AAPL", QuoteAsset: "USD",
		AmountKind: domain.OrderAmountKindQuantity, Quantity: "1", LimitPrice: "100", Route: "{}",
	})
	tradingE2EMust(t, err)
	restarted := buildTestConnector(t, build)
	restartOrder := testExecutionOrder()
	restartOrder.ClientOrderID, restartOrder.LimitPrice = "restart", "100"
	_, err = restarted.Send(context.Background(), restartOrder)
	tradingE2EMust(t, err)

	// Separate rules and instruments still share the connection's ordinal.
	mask, err := domain.ParseInstrumentMask("USD", "AAPL")
	tradingE2EMust(t, err)
	store.state.Policy.Rules = append(store.state.Policy.Rules, domain.TestExecutionRule{
		Mask: mask, Mode: "reject_even", PriceVariant: "normal",
	})
	send("e", "USD", "AAPL", false)
	send("f", "AAPL", "USD", true)
	store.state.Revision++
	send("g", "AAPL", "USD", false)
	send("a", "AAPL", "USD", false)
	send("b", "AAPL", "USD", true)
	send("h", "USD", "AAPL", true)
	for range 100 {
		store.state.Revision++
		send(fmt.Sprintf("revision-%d", store.state.Revision), "AAPL", "USD", false)
	}
	connector := c.(*testConnector)
	if connector.currentRevision != store.state.Revision || connector.ordinal != 1 {
		t.Fatalf("current counter = %d/%d", connector.currentRevision, connector.ordinal)
	}
	// The book is the only map; a revision history would grow for no purpose.
	shape := reflect.TypeOf(connector).Elem()
	for i := 0; i < shape.NumField(); i++ {
		field := shape.Field(i)
		if field.Type.Kind() == reflect.Map && field.Name != "book" {
			t.Fatalf("connector retains counter history in %s", field.Name)
		}
	}
}

func TestTestConnectorRejectEvenInvalidRequest(t *testing.T) {
	for _, tc := range []struct {
		name, base, quote, quantity, code, message string
	}{
		{"invalid_quantity", "AAPL", "USD", "invalid", "test_quantity_invalid", ""},
		{"missing_base_identity", "MISSING", "USD", "2", "test_quote_invalid", "Test asset identity is missing: MISSING"},
		{"missing_quote_identity", "AAPL", "MISSING", "2", "test_quote_invalid", "Test asset identity is missing: MISSING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for validBefore := range 2 {
				t.Run(fmt.Sprintf("after_%d_valid", validBefore), func(t *testing.T) {
					build := testBuildContext(t)
					store := build.Store.(*testPolicyStore)
					store.state.Policy.Rules[0].Mode = "reject_even"
					if tc.code == "test_quote_invalid" {
						mask, err := domain.ParseInstrumentMask(tc.base, tc.quote)
						tradingE2EMust(t, err)
						store.state.Policy.Rules = append(store.state.Policy.Rules, domain.TestExecutionRule{
							Mask: mask, Mode: "reject_even", PriceVariant: "normal",
						})
					}
					c := buildTestConnector(t, build)
					sendValid := func(key string, rejected bool) {
						t.Helper()
						order := testExecutionOrder()
						order.ClientOrderID, order.LimitPrice = key, "100"
						ack, err := c.Send(context.Background(), order)
						if rejected {
							assertTestRefusal(t, err, "test_policy_even_rejected", []int{0})
							return
						}
						tradingE2EMust(t, err)
						if ack.VenueOrderID == "" {
							t.Fatal("valid request has no venue order ID")
						}
						snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: key})
						requireTestBookLookup(t, found, err)
						if !found || snapshot.Status != fwtrading.VenueStatusFilled || snapshot.FilledQuantity != order.Quantity {
							t.Fatalf("valid request did not execute: found=%v snapshot=%+v", found, snapshot)
						}
					}
					if validBefore == 1 {
						sendValid("first", false)
					}
					order := testExecutionOrder()
					order.ClientOrderID, order.LimitPrice = "invalid", "100"
					order.BaseAsset, order.QuoteAsset, order.Quantity = tc.base, tc.quote, tc.quantity
					_, err := c.Send(context.Background(), order)
					var refusal *fwtrading.TestExecutionRefusal
					if !errors.As(err, &refusal) || !errors.Is(err, fwtrading.ErrRejected) {
						t.Fatalf("invalid request refusal = %v, want %s", err, tc.code)
					}
					if refusal.Code != tc.code {
						t.Errorf("invalid request refusal code = %s, want %s", refusal.Code, tc.code)
					}
					if tc.code == "test_quote_invalid" && refusal.Message != tc.message {
						t.Errorf("invalid request refusal message = %q, want %q", refusal.Message, tc.message)
					}
					sendValid("next", validBefore == 1)
					sendValid("following", validBefore == 0)
				})
			}
		})
	}
}

func TestTestConnectorRejectEvenSelectionAndPrice(t *testing.T) {
	build := testBuildContext(t)
	store := build.Store.(*testPolicyStore)
	rules := store.state.Policy.Rules
	rules[0].Mode = "reject_even"
	c := buildTestConnector(t, build)
	order := testExecutionOrder()
	store.state.Policy.Rules = nil
	_, err := c.Send(context.Background(), order)
	assertTestRefusal(t, err, "test_policy_no_match", []int{})
	if c.(*testConnector).ordinal != 0 {
		t.Fatal("no-match moved ordinal")
	}
	store.state.Policy.Rules = rules
	order.ClientOrderID = "odd-no-price"
	_, err = c.Send(context.Background(), order)
	assertTestRefusal(t, err, "test_quote_missing", []int{0})
	build.Quotes.(*testQuotes).onRead = func() { t.Fatal("even request read a price") }
	order.ClientOrderID = "even-no-price"
	_, err = c.Send(context.Background(), order)
	assertTestRefusal(t, err, "test_policy_even_rejected", []int{0})
	connector := c.(*testConnector)
	connector.ordinal = math.MaxUint64
	order.ClientOrderID = "overflow"
	_, err = c.Send(context.Background(), order)
	assertTestRefusal(t, err, "test_policy_ordinal_overflow", []int{0})
	if connector.ordinal != math.MaxUint64 {
		t.Fatal("overflow moved ordinal")
	}
}

func TestTestConnectorRejectEvenAcceptance(t *testing.T) {
	for _, failure := range []string{"ctx", "transient"} {
		t.Run(failure, func(t *testing.T) {
			build := testBuildContext(t)
			store := build.Store.(*testPolicyStore)
			store.state.Policy.Rules[0].Mode = "reject_even"
			c := buildTestConnector(t, build).(*testConnector)
			order := testExecutionOrder()
			order.LimitPrice = "100"
			_, err := c.Send(context.Background(), order)
			tradingE2EMust(t, err)
			store.state.Revision++
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "ctx" {
				store.onAsset = cancel
			} else {
				store.err = errors.New("temporary policy read failure")
			}
			order.ClientOrderID = "new-revision"
			if _, err := c.Send(ctx, order); err == nil {
				t.Fatal("pre-acceptance failure was lost")
			}
			if c.currentRevision != 1 || c.ordinal != 1 {
				t.Fatalf("failed request committed counter: %d/%d", c.currentRevision, c.ordinal)
			}
			if _, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: order.ClientOrderID}); !errors.Is(err, fwtrading.ErrOrderGone) || found {
				t.Fatalf("failed request entered book: found=%v err=%v", found, err)
			}
			store.onAsset, store.err = nil, nil
			_, err = c.Send(context.Background(), order)
			tradingE2EMust(t, err)
			if c.currentRevision != 2 || c.ordinal != 1 {
				t.Fatalf("retry did not start revision: %d/%d", c.currentRevision, c.ordinal)
			}
		})
	}
}
