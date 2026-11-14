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
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

type testBookConnector struct{ *fakeConnector }

func (v *fakeVenue) buildTestBook(ctx trading.BuildContext) (trading.Connector, error) {
	c, err := v.build(ctx)
	if err != nil {
		return nil, err
	}
	return &testBookConnector{c.(*fakeConnector)}, nil
}

func (c *testBookConnector) Lookup(ctx context.Context, ref trading.OrderRef) (trading.Snapshot, bool, error) {
	snapshot, found, err := c.fakeConnector.Lookup(ctx, ref)
	if err == nil && !found {
		return snapshot, false, trading.ErrOrderGone
	}
	return snapshot, found, err
}

func (c *testBookConnector) ReleaseOrder(clientOrderID string) {
	c.venue.mu.Lock()
	defer c.venue.mu.Unlock()
	delete(c.venue.snapshots, clientOrderID)
}

func TestLostBookCancelsRemainder(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		name := "enabled"
		if disabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			// Cancellation follows the connector's lost-order response for any provider.
			f := newFixture(t, true)
			f.start()
			order := f.submit("10")
			link := f.send(order)
			partial := makeFill("3", "3", "7", false)
			f.fill(link, partial)
			f.flush()
			before := f.detail(order)
			if before.Order.Status != domain.OrderStatusPartiallyFilled {
				t.Fatal("partial fill was not recorded")
			}
			f.runtime.Stop()
			f.venue.mu.Lock()
			clear(f.venue.snapshots)
			f.venue.mu.Unlock()
			if disabled {
				must(t, f.st.SetTradingConnectionEnabled(testCtx, f.connection.ExternalID, false))
			}
			f.start()
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			after := f.detail(order)
			if after.Order.Status != domain.OrderStatusCancelled || after.Order.Leaves != "0" {
				t.Fatalf("lost book order = %+v", after.Order)
			}
			assertTrades(t, after, []trading.Fill{partial})
			if after.Trades[0].ExternalID != before.Trades[0].ExternalID {
				t.Fatal("restart changed a historical trade")
			}
			f.assertReleased(order)
			links, err := f.st.ListOpenVenueOrders(testCtx, f.connection.ExternalID)
			must(t, err)
			if len(links) != 0 {
				t.Fatal("lost link remained open")
			}
			reportsBefore := len(after.Events)
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			if len(f.detail(order).Events) != reportsBefore {
				t.Fatal("completed pending generated another report")
			}
		})
	}
}

type terminalReadStore struct {
	trading.Store
	fail atomic.Bool
}

func (s *terminalReadStore) GetOrder(ctx context.Context, id domain.ExternalID) (domain.OrderDetail, error) {
	detail, err := s.Store.GetOrder(ctx, id)
	if err == nil && domain.OrderStatusTerminal(detail.Order.Status) && s.fail.Load() {
		return domain.OrderDetail{}, errors.New("terminal confirmation read failed")
	}
	return detail, err
}

func TestTestBookReleaseAfterConfirmedTerminal(t *testing.T) {
	for _, terminal := range []string{"fill", "cancel"} {
		for _, failure := range []string{"none", "sink", "read", "already exists"} {
			t.Run(terminal+"/"+failure, func(t *testing.T) {
				f := newFixture(t, true)
				st := &terminalReadStore{Store: f.st}
				var failSink atomic.Bool
				sink := func(ctx context.Context, report domain.ExecutionReportInput) error {
					if report.OrderStatus == domain.OrderStatusCommitted {
						return f.sink(ctx, report)
					}
					if failSink.Load() {
						return errors.New("sink unavailable")
					}
					err := f.sink(ctx, report)
					if err == nil && failure == "already exists" {
						return domain.ErrAlreadyExists
					}
					return err
				}
				var err error
				f.runtime, err = trading.NewRuntime(f.registry, st, nil, sink,
					f.adjustmentSink, f.feeSkipSink, slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
				must(t, err)
				t.Cleanup(f.runtime.Stop)
				must(t, f.runtime.Start(testCtx))
				order := f.submit("10")
				link := f.send(order)
				if terminal == "fill" {
					fill := makeFill("10", "10", "0", true)
					f.venue.setSnapshot(link, trading.VenueStatusFilled, "10", fill)
				} else {
					f.venue.setSnapshot(link, trading.VenueStatusCancelled, "0")
				}
				failSink.Store(failure == "sink")
				st.fail.Store(failure == "read")
				f.orderEvent(link)
				f.flush()
				hasEntry := func() bool {
					f.venue.mu.Lock()
					defer f.venue.mu.Unlock()
					_, ok := f.venue.snapshots[link.ClientOrderID]
					return ok
				}
				if failure == "sink" || failure == "read" {
					if !hasEntry() {
						t.Fatal("book released before sink/terminal confirmation succeeded")
					}
					if failure == "sink" && domain.OrderStatusTerminal(f.detail(order).Order.Status) {
						t.Fatal("failed sink terminalized order")
					}
					failSink.Store(false)
					st.fail.Store(false)
					f.orderEvent(link)
					f.flush()
				}
				if hasEntry() {
					t.Fatal("confirmed terminal retained book entry")
				}
				if !domain.OrderStatusTerminal(f.detail(order).Order.Status) {
					t.Fatal("order did not reach terminal")
				}
				f.assertReleased(order)
			})
		}
	}
}

func TestTestClassReconcileWaitsForSend(t *testing.T) {
	f := newFixture(t, true)
	f.start()
	order := f.submit("10")
	entered, release := make(chan trading.Order, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.venue.onSend = func(_ context.Context, order trading.Order) error {
		entered <- order
		<-release
		return nil
	}
	result := make(chan error, 1)
	go func() { _, err := f.runtime.Send(testCtx, order.ExternalID, f.dest); result <- err }()
	mapped := <-entered
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	barrier := make(chan struct{})
	go func() { _ = f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); close(barrier) }()
	select {
	case <-barrier:
		t.Fatal("worker processed a task during Send")
	case <-time.After(30 * time.Millisecond):
	}
	_, lookups := f.venue.counts()
	if lookups != 0 || domain.OrderStatusTerminal(f.detail(order).Order.Status) {
		t.Fatal("reconcile overtook Send and cancelled the unaccepted order")
	}
	unblock()
	must(t, <-result)
	<-barrier
	f.flush()
	f.venue.mu.Lock()
	_, found := f.venue.snapshots[mapped.ClientOrderID]
	f.venue.mu.Unlock()
	if !found || domain.OrderStatusTerminal(f.detail(order).Order.Status) {
		t.Fatal("serialized Send lost its open entry")
	}
}
