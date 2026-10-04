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

package trading_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

func TestSendFillsAndCoreBalances(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	link := f.send(order)
	stored, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if !found || stored.VenueOrderID != link.VenueOrderID || !stored.SendAttempted {
		t.Fatalf("acknowledged link = %+v", stored)
	}
	if len(link.ClientOrderID) != 22 {
		t.Fatalf("client id length = %d", len(link.ClientOrderID))
	}
	partial := makeFill("3", "3", "9.25", false)
	partial.Price = "1.5"
	final := makeFill("7", "10", "0.000", true)
	f.fill(link, partial)
	f.flush()
	detail := f.detail(order)
	if detail.Order.Status != domain.OrderStatusPartiallyFilled || detail.Order.Leaves != "9.25" {
		t.Fatalf("partial order = %+v", detail.Order)
	}
	f.fill(link, final)
	f.flush()
	detail = f.detail(order)
	if detail.Order.Status != domain.OrderStatusFilled || detail.Order.Leaves != "0.000" {
		t.Fatalf("final order = %+v", detail.Order)
	}
	assertTrades(t, detail, []trading.Fill{partial, final})
	usd, found, err := f.n.GetBalance(testCtx, "desk", "USD")
	must(t, err)
	if !found {
		t.Fatal("USD missing")
	}
	assertDecimal(t, usd.Available, "981.5")
	assertDecimal(t, usd.Held, "0")
	base, found, err := f.n.GetBalance(testCtx, "desk", "AAPL")
	must(t, err)
	if !found {
		t.Fatal("AAPL missing")
	}
	assertDecimal(t, base.Available, "10")
	assertDecimal(t, base.Held, "0")
}

func TestDuplicateFillIgnored(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	link := f.send(order)
	fill := makeFill("3", "3", "7", false)
	f.fill(link, fill)
	f.flush()
	f.venue.mu.Lock()
	reports := len(f.venue.reports)
	f.venue.mu.Unlock()
	fill.CumQuantity = "3.000"
	f.fill(link, fill)
	f.flush()
	f.venue.mu.Lock()
	after := len(f.venue.reports)
	f.venue.mu.Unlock()
	if after != reports {
		t.Fatalf("duplicate reached report sink: calls %d -> %d", reports, after)
	}
	assertTrades(t, f.detail(order), []trading.Fill{fill})
}

func TestDeterministicReportIDsAndRestartReplay(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	link := f.send(order)
	fill := makeFill("3", "3.000", "7", false)
	f.fill(link, fill)
	f.flush()
	id := domain.ExternalID("trading:" + link.ClientOrderID + ":fill:3")
	exists, err := f.st.ExecutionReportExists(testCtx, id)
	must(t, err)
	if !exists {
		t.Fatalf("deterministic canonical fill report %s missing", id)
	}
	f.venue.setSnapshot(link, trading.VenueStatusOpen, "3", fill)
	f.runtime.Stop()
	f.start()
	f.fill(link, fill)
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	assertTrades(t, f.detail(order), []trading.Fill{fill})
	detail := f.detail(order)
	reports := 0
	if detail.Order.Status != domain.OrderStatusPartiallyFilled {
		t.Fatalf("open snapshot changed status to %s", detail.Order.Status)
	}
	for _, event := range detail.Events {
		if event.Payload.ExecutionReport != nil && event.Payload.ExecutionReport.ExternalID == id {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("canonical report history entries = %d, want 1", reports)
	}
}

func TestGapReconcilesMissingFills(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	link := f.send(order)
	a, b := makeFill("3", "3", "7", false), makeFill("7", "10", "0", true)
	f.venue.setSnapshot(link, trading.VenueStatusFilled, "10", a, b)
	f.fill(link, b)
	f.flush()
	detail := f.detail(order)
	assertTrades(t, detail, []trading.Fill{a, b})
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %s", detail.Order.Status)
	}
}

func TestTerminalBarrier(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	link := f.send(order)
	a, b := makeFill("2", "2", "8", false), makeFill("3", "5", "5", false)
	// A prefix is insufficient even though it starts contiguously at zero.
	f.venue.setSnapshot(link, trading.VenueStatusCancelled, "5", a)
	f.orderEvent(link)
	f.flush()
	detail := f.detail(order)
	if len(detail.Trades) != 0 || detail.Order.Status != domain.OrderStatusCommitted {
		t.Fatalf("incomplete snapshot applied: trades=%d status=%s", len(detail.Trades), detail.Order.Status)
	}
	f.venue.setSnapshot(link, trading.VenueStatusCancelled, "5", a, b)
	f.orderEvent(link)
	f.flush()
	detail = f.detail(order)
	assertTrades(t, detail, []trading.Fill{a, b})
	if detail.Order.Status != domain.OrderStatusCancelled || detail.Order.Leaves != "0" {
		t.Fatalf("cancel barrier status=%s leaves=%s", detail.Order.Status, detail.Order.Leaves)
	}
	// History must put the cancellation after both executions.
	types := []domain.OrderEventType{}
	for _, event := range detail.Events {
		if event.Type == domain.OrderEventFill || event.Type == domain.OrderEventCancelled {
			types = append(types, event.Type)
		}
	}
	if fmt.Sprint(types) != "[fill fill cancelled]" {
		t.Fatalf("settlement history = %v", types)
	}
	f.assertReleased(order)
}

func TestDefinitiveSendRefusal(t *testing.T) {
	for _, refusal := range []error{trading.ErrRejected, trading.ErrUnsupported} {
		t.Run(refusal.Error(), func(t *testing.T) {
			f := newFixture(t)
			f.venue.sendErr = fmt.Errorf("venue rule: %w", refusal)
			f.start()
			order := f.submit("10")
			_, err := f.runtime.Send(testCtx, order.ExternalID, f.dest)
			if !errors.Is(err, refusal) {
				t.Fatalf("send error = %v", err)
			}
			detail := f.detail(order)
			if detail.Order.Status != domain.OrderStatusCancelled {
				t.Fatalf("status = %s", detail.Order.Status)
			}
			f.assertReleased(order)
			balance, _, err := f.n.GetBalance(testCtx, "desk", "USD")
			must(t, err)
			assertDecimal(t, balance.Available, "1000")
		})
	}
}

func TestUnknownSendOutcome(t *testing.T) {
	for _, found := range []bool{true, false} {
		t.Run(fmt.Sprint(found), func(t *testing.T) {
			f := newFixture(t)
			f.venue.sendErr = errors.New("network timeout")
			f.start()
			order := f.submit("10")
			link, err := f.runtime.Send(testCtx, order.ExternalID, f.dest)
			if !errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), "reconciliation") {
				t.Fatalf("send error = %v", err)
			}
			if !errors.Is(err, f.venue.sendErr) {
				t.Fatalf("send error lost venue cause: %v", err)
			}
			if link.VenueOrderID != "" || !link.SendAttempted {
				t.Fatalf("unknown link = %+v", link)
			}
			_, lookups := f.venue.counts()
			if lookups != 0 {
				t.Fatalf("unknown outcome looked up during send: %d", lookups)
			}
			if found {
				f.venue.setSnapshot(link, trading.VenueStatusFilled, "10", makeFill("10", "10", "0", true))
			}
			f.orderEvent(link)
			f.flush()
			detail := f.detail(order)
			want := domain.OrderStatusCommitted
			if found {
				want = domain.OrderStatusFilled
				stored, _, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
				must(t, err)
				if stored.VenueOrderID == "" {
					t.Fatal("reconcile did not acknowledge")
				}
				if len(detail.Trades) != 1 {
					t.Fatalf("trades = %d", len(detail.Trades))
				}
			}
			if detail.Order.Status != want {
				t.Fatalf("status = %s, want %s", detail.Order.Status, want)
			}
			if found {
				f.assertReleased(order)
			}
		})
	}
}

func TestRestartAndDisabledTracking(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	link := f.send(order)
	must(t, f.st.SetTradingConnectionEnabled(testCtx, f.connection.ExternalID, false))
	f.venue.mu.Lock()
	previousConnector := f.venue.current
	f.venue.mu.Unlock()
	f.runtime.Stop()
	f.start()
	f.venue.mu.Lock()
	trackingStarted := f.venue.current != previousConnector
	f.venue.mu.Unlock()
	if !trackingStarted {
		t.Fatal("disabled connection with open orders has no tracking worker")
	}
	draft := f.draft("1")
	if err := f.runtime.PreCheck(testCtx, draft, f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("disabled precheck = %v", err)
	}
	next := f.submit("1")
	if _, err := f.runtime.Send(testCtx, next.ExternalID, f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("disabled send = %v", err)
	}
	fill := makeFill("10", "10", "0", true)
	f.venue.setSnapshot(link, trading.VenueStatusFilled, "10", fill)
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	detail := f.detail(order)
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("disabled tracking status = %s", detail.Order.Status)
	}
	assertTrades(t, detail, []trading.Fill{fill})
	sends, _ := f.venue.counts()
	if sends != 1 {
		t.Fatalf("disabled connection sends = %d", sends)
	}
}

func TestUnattemptedLinks(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(fmt.Sprint(claimed), func(t *testing.T) {
			f := newFixture(t)
			order := f.submit("10")
			coid, err := domain.NewExternalID()
			must(t, err)
			link, err := f.st.CreateVenueOrder(testCtx, domain.VenueOrder{
				Order: order.ExternalID, Connection: f.dest.Connection, Route: "{}", ClientOrderID: string(coid),
			})
			must(t, err)
			if claimed {
				must(t, f.sink(testCtx, domain.ExecutionReportInput{
					ExternalID: domain.ExternalID("trading:" + link.ClientOrderID + ":claim"), Order: order.ExternalID, OrderStatus: domain.OrderStatusCommitted,
				}))
			}
			f.start()
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			_, exists, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
			must(t, err)
			if !claimed && exists {
				t.Fatal("unclaimed link was retained")
			}
			detail := f.detail(order)
			want := domain.OrderStatusCommitted
			if claimed {
				want = domain.OrderStatusCancelled
				f.assertReleased(order)
			}
			if detail.Order.Status != want {
				t.Fatalf("status=%s want=%s", detail.Order.Status, want)
			}
			sends, lookups := f.venue.counts()
			if sends != 0 || lookups != 0 {
				t.Fatalf("unattempted link contacted venue: sends=%d lookups=%d", sends, lookups)
			}
		})
	}
}

func TestClaimPrecedesSendAndCancelShortcutRefuses(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	var claimSeen bool
	f.venue.onSend = func(_ context.Context, mapped trading.Order) error {
		exists, err := f.st.ExecutionReportExists(testCtx, domain.ExternalID("trading:"+mapped.ClientOrderID+":claim"))
		claimSeen = exists && err == nil
		return nil
	}
	f.send(order)
	if !claimSeen {
		t.Fatal("venue send happened without a persisted claim")
	}
	_, _, err := f.n.CancelOrder(testCtx, order.ExternalID, "0", testCaller)
	if !errors.Is(err, domain.ErrExecutionReportRequired) {
		t.Fatalf("cancel shortcut = %v", err)
	}
}

func TestCancelWinsClaimRace(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	f.venue.onPreCheck = func(context.Context, trading.Order) error {
		_, _, err := f.n.CancelOrder(testCtx, order.ExternalID, "0", testCaller)
		return err
	}
	_, err := f.runtime.Send(testCtx, order.ExternalID, f.dest)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("claim error = %v", err)
	}
	_, exists, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if exists {
		t.Fatal("failed claim link retained")
	}
	sends, _ := f.venue.counts()
	if sends != 0 {
		t.Fatalf("failed claim sent %d times", sends)
	}
}

func TestOneSendPerOrder(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = f.runtime.Send(testCtx, order.ExternalID, f.dest) }()
	}
	wg.Wait()
	sends, _ := f.venue.counts()
	if sends != 1 {
		t.Fatalf("one Officer order sent %d times", sends)
	}
}

func TestEligibility(t *testing.T) {
	for _, kind := range []string{"rejected", "drop-copy", "confirmed", "already-sent", "immediate", "no-acceptance", "linked"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.start()
			var order domain.Order
			switch kind {
			case "rejected":
				draft := f.draft("10000")
				var err error
				order, err = f.n.SubmitOrder(testCtx, draft, domain.MissingAccountReject, testCaller)
				must(t, err)
				if order.Status != domain.OrderStatusRejected {
					t.Fatalf("expected rejection: %s", order.Status)
				}
			case "drop-copy":
				draft := f.draft("10")
				draft.DropCopy = true
				var err error
				order, err = f.n.SubmitOrder(testCtx, draft, domain.MissingAccountReject, testCaller)
				must(t, err)
			case "immediate":
				var err error
				order, _, err = f.n.SubmitImmediate(testCtx, f.draft("10"), domain.MissingAccountReject, testCaller)
				must(t, err)
			case "no-acceptance":
				var err error
				draft := f.draft("10")
				draft.Status = domain.OrderStatusCommitted
				draft.Source = domain.SourcePanel
				order, err = f.st.CreateOrder(testCtx, draft)
				must(t, err)
			default:
				order = f.submit("10")
			}
			if kind == "confirmed" {
				_, err := f.n.ConfirmOrder(testCtx, order.ExternalID, testCaller)
				must(t, err)
			}
			if kind == "already-sent" {
				f.send(order)
			}
			if kind == "linked" {
				coid, err := domain.NewExternalID()
				must(t, err)
				_, err = f.st.CreateVenueOrder(testCtx, domain.VenueOrder{Order: order.ExternalID, Connection: f.dest.Connection, ClientOrderID: string(coid), Route: "{}"})
				must(t, err)
			}
			_, err := f.runtime.Send(testCtx, order.ExternalID, f.dest)
			expected := domain.ErrConflict
			if kind == "linked" {
				expected = domain.ErrAlreadyExists
			}
			if !errors.Is(err, expected) {
				t.Fatalf("%s eligibility error=%v want=%v", kind, err, expected)
			}
			sends, _ := f.venue.counts()
			want := 0
			if kind == "already-sent" {
				want = 1
			}
			if sends != want {
				t.Fatalf("ineligible sends=%d want=%d", sends, want)
			}
		})
	}
}

func TestDestinationChecksAndPreCheckWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.fund("other", "1000")
	f.start()
	draft := f.draft("10")
	must(t, f.runtime.PreCheck(testCtx, draft, f.dest))
	links, err := f.st.ListOpenVenueOrders(testCtx, f.dest.Connection)
	must(t, err)
	if len(links) != 0 {
		t.Fatalf("draft precheck wrote %d links", len(links))
	}
	sends, _ := f.venue.counts()
	if sends != 0 {
		t.Fatal("precheck sent an order")
	}
	for _, tc := range []struct {
		name  string
		draft domain.Order
		dest  domain.TradingDestination
		want  error
	}{
		{"invalid-route", draft, domain.TradingDestination{Connection: f.dest.Connection, Route: "[]"}, domain.ErrInvalid},
		{"unknown-connection", draft, domain.TradingDestination{Connection: domain.ExternalID("unknown"), Route: "{}"}, domain.ErrNotFound},
		{"venue-account", draft, domain.TradingDestination{Connection: f.dest.Connection, VenueAccount: "account", Route: "{}"}, domain.ErrInvalid},
		{"no-access", func() domain.Order { d := draft; d.Account = "other"; return d }(), f.dest, domain.ErrForbidden},
		{"missing-instrument", func() domain.Order { d := draft; d.BaseAsset = "MSFT"; return d }(), f.dest, domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.runtime.PreCheck(testCtx, tc.draft, tc.dest); !errors.Is(err, tc.want) {
				t.Fatalf("precheck=%v want=%v", err, tc.want)
			}
			order, err := f.n.SubmitOrder(testCtx, tc.draft, domain.MissingAccountReject, testCaller)
			must(t, err)
			if _, err := f.runtime.Send(testCtx, order.ExternalID, tc.dest); !errors.Is(err, tc.want) {
				t.Fatalf("send=%v want=%v", err, tc.want)
			}
			_, linked, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
			must(t, err)
			if linked {
				t.Fatal("invalid destination created a venue link")
			}
		})
	}
	must(t, f.st.UpsertTradingInstrument(testCtx, domain.TradingInstrument{Connection: f.dest.Connection, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: false}))
	if err := f.runtime.PreCheck(testCtx, draft, f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("disabled instrument error=%v", err)
	}
	must(t, f.st.UpsertTradingInstrument(testCtx, domain.TradingInstrument{Connection: f.dest.Connection, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}))
	f.venue.mu.Lock()
	f.venue.preErr = fmt.Errorf("amount kind: %w", trading.ErrUnsupported)
	f.venue.mu.Unlock()
	if err := f.runtime.PreCheck(testCtx, draft, f.dest); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("unsupported precheck=%v", err)
	}
}

func TestVenueAccountAccessMustMatchExactly(t *testing.T) {
	f := newFixture(t)
	must(t, f.registry.Register(trading.Provider{Type: "accounts", VenueAccounts: true, Build: f.venue.build}))
	c := f.addConnection("accounts", "multi-account", false)
	must(t, f.st.AddTradingAccess(testCtx, domain.TradingAccess{Account: "desk", Connection: c.ExternalID, VenueAccount: "allowed"}))
	f.start()
	for _, tc := range []struct {
		account string
		want    error
	}{{"", domain.ErrInvalid}, {"other", domain.ErrForbidden}, {"allowed", nil}} {
		dest := domain.TradingDestination{Connection: c.ExternalID, VenueAccount: tc.account, Route: "{}"}
		err := f.runtime.PreCheck(testCtx, f.draft("1"), dest)
		if tc.want == nil {
			must(t, err)
		} else if !errors.Is(err, tc.want) {
			t.Fatalf("account %q precheck=%v", tc.account, err)
		}
	}
}

func TestUnavailableConnectionIsolation(t *testing.T) {
	for _, failure := range []string{"build", "subscribe"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			badVenue := newVenue()
			build := badVenue.build
			cause := errors.New("authentication failed")
			if failure == "build" {
				build = func(domain.TradingConnection) (trading.Connector, error) { return nil, cause }
			} else {
				badVenue.subscribeErr = cause
			}
			must(t, f.registry.Register(trading.Provider{Type: "broken", Build: build}))
			bad := f.addConnection("broken", "broken", true)
			f.start()
			order := f.submit("10")
			dest := domain.TradingDestination{Connection: bad.ExternalID, Route: "{}"}
			_, err := f.runtime.Send(testCtx, order.ExternalID, dest)
			if !errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), "authentication failed") {
				t.Fatalf("unavailable cause=%v", err)
			}
			if err := f.runtime.PreCheck(testCtx, f.draft("1"), dest); !errors.Is(err, domain.ErrUpstream) {
				t.Fatalf("unavailable precheck=%v", err)
			}
			f.send(order)
		})
	}
}

func TestUnknownClientIDIgnored(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	f.venue.push(trading.Event{Kind: trading.EventFill, Ref: trading.OrderRef{ClientOrderID: "manual-order"}, Fill: makeFill("10", "10", "0", true)})
	f.flush()
	if len(f.detail(order).Trades) != 0 {
		t.Fatal("manual venue order applied")
	}
	sends, lookups := f.venue.counts()
	if sends != 0 || lookups != 0 {
		t.Fatalf("unknown event sent=%d looked-up=%d", sends, lookups)
	}
}

func TestExactDecimalsAndOverfill(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("0.123456789012345678")
	link := f.send(order)
	fill := makeFill("0.223456789012345678", "0.223456789012345678", "0.000", true)
	fill.Price = "1.234567890123456789"
	f.fill(link, fill)
	f.flush()
	detail := f.detail(order)
	assertTrades(t, detail, []trading.Fill{fill})
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status=%s", detail.Order.Status)
	}
	f.venue.mu.Lock()
	mapped := f.venue.sends[0]
	f.venue.mu.Unlock()
	if mapped.Quantity != order.AmountValue || mapped.LimitPrice != order.Price {
		t.Fatalf("send rounded decimal: %+v", mapped)
	}
}

func TestCallerCancellationAndSingleWorker(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	entered, release := make(chan trading.Order, 1), make(chan struct{})
	var releaseOnce sync.Once
	releaseSend := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseSend()
	f.venue.onSend = func(_ context.Context, o trading.Order) error { entered <- o; <-release; return nil }
	ctx, cancel := context.WithCancel(testCtx)
	result := make(chan error, 1)
	go func() { _, err := f.runtime.Send(ctx, order.ExternalID, f.dest); result <- err }()
	mapped := <-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller=%v", err)
	}
	f.venue.push(trading.Event{Kind: trading.EventOrder, Ref: trading.OrderRef{ClientOrderID: mapped.ClientOrderID}})
	// PreCheck cannot enter the connector while the same worker is sending.
	checked := make(chan struct{})
	go func() { _ = f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); close(checked) }()
	select {
	case <-checked:
		t.Fatal("connection worker processed another task during send")
	case <-time.After(30 * time.Millisecond):
	}
	_, lookups := f.venue.counts()
	if lookups != 0 {
		t.Fatalf("worker reconciled during in-flight send: %d lookups", lookups)
	}
	releaseSend()
	<-checked
	f.flush()
	stored, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if !found || stored.VenueOrderID == "" {
		t.Fatal("caller cancellation tore send sequence")
	}
}

func TestOneSendAcrossConnections(t *testing.T) {
	f := newFixture(t)
	other := newVenue()
	must(t, f.registry.Register(trading.Provider{Type: "other", Build: other.build}))
	connection := f.addConnection("other", "other-connection", true)
	f.start()
	order := f.submit("10")
	results := make(chan error, 2)
	for _, dest := range []domain.TradingDestination{f.dest, {Connection: connection.ExternalID, Route: "{}"}} {
		go func() {
			_, err := f.runtime.Send(testCtx, order.ExternalID, dest)
			results <- err
		}()
	}
	successes := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("cross-connection send error = %v", err)
		}
	}
	first, _ := f.venue.counts()
	second, _ := other.counts()
	if successes != 1 || first+second != 1 {
		t.Fatalf("cross-connection successes=%d venue sends=%d", successes, first+second)
	}
}

func TestRuntimeLifecycle(t *testing.T) {
	f := newFixture(t)
	f.start()
	if err := f.runtime.Start(testCtx); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Start=%v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.runtime.Stop() }()
	}
	wg.Wait()
	if err := f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stopped PreCheck=%v", err)
	}
	if _, err := f.runtime.Send(testCtx, f.submit("1").ExternalID, f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stopped Send=%v", err)
	}
	f.venue.mu.Lock()
	closes := f.venue.closes
	f.venue.mu.Unlock()
	if closes != 1 {
		t.Fatalf("connector closes=%d", closes)
	}
}
