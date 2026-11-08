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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scmhub/ibapi"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

func ibTestExecution(id, qty, cum string) *ibapi.Execution {
	return &ibapi.Execution{ExecID: id, OrderID: 42, OrderRef: "officer-order", PermID: 4242,
		Time: "20261027-12:00:00", Shares: ibapi.StringToDecimal(qty), CumQty: ibapi.StringToDecimal(cum), Price: 100}
}

func ibTestSession(t *testing.T, c *ibConnector, v *fakeIBVenue) *ibTradingSession {
	t.Helper()
	s := newIBTradingSession(context.Background(), c)
	t.Cleanup(s.cancel)
	s.client = v.client(s)
	s.client.(*fakeIBTradingClient).id = c.clientID
	s.NextValidID(100)
	c.session = s
	return s
}

func TestIBClientIsolation(t *testing.T) {
	for _, ownID := range []int64{0, 17} {
		for _, callback := range []string{"open", "status", "execution", "completed"} {
			t.Run(fmt.Sprintf("client-%d/%s", ownID, callback), func(t *testing.T) {
				v := newFakeIBVenue()
				c := testIBConnector(t, v)
				c.clientID = ownID
				s := ibTestSession(t, c, v)
				v.executionFilter = func(filter *ibapi.ExecutionFilter) {
					if filter.ClientID != ownID {
						t.Errorf("execution filter client=%d want=%d", filter.ClientID, ownID)
					}
				}
				v.open = func(client *fakeIBTradingClient) {
					order := ibTestVenueOrder()
					order.ClientID = ownID
					client.w.OpenOrder(42, ibapi.NewContract(), order, &ibapi.OrderState{Status: "Submitted"})
					order.ClientID, order.PermID = ownID+7, 9999
					order.TotalQuantity = ibapi.StringToDecimal("1")
					switch callback {
					case "open":
						client.w.OpenOrder(42, ibapi.NewContract(), order, &ibapi.OrderState{Status: "Filled"})
					case "status":
						client.w.OrderStatus(42, "Filled", ibapi.StringToDecimal("1"), ibapi.ZERO, 100, 9999, 0, 0, ownID+7, "", 0)
					case "execution":
						e := ibTestExecution("foreign", "1", "1")
						e.ClientID, e.PermID = ownID+7, 9999
						s.mu.Lock()
						reqID := s.lookup.reqID
						s.mu.Unlock()
						client.w.ExecDetails(reqID, ibapi.NewContract(), e)
					case "completed":
						client.w.CompletedOrder(ibapi.NewContract(), order, &ibapi.OrderState{Status: "Filled"})
					}
				}
				snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "officer-order", VenueOrderID: "4242"})
				if err != nil || !found || snapshot.VenueOrderID != "4242" || snapshot.Status != fwtrading.VenueStatusOpen || snapshot.FilledQuantity != "0" || len(snapshot.Fills) != 0 {
					t.Fatalf("foreign %s changed snapshot=%+v found=%v err=%v", callback, snapshot, found, err)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				o := s.orders[42]
				if o.ref != "officer-order" || o.permID != 4242 || o.total.String() != "10" || o.filled.String() != "0" || o.status != "Submitted" || len(s.executions) != 0 {
					t.Fatalf("foreign %s changed session order=%+v executions=%v", callback, o, s.executions)
				}
			})
		}
	}
}

func TestIBLookupOpenRequestNotIssued(t *testing.T) {
	v := newFakeIBVenue()
	openCalls := 0
	v.open = func(client *fakeIBTradingClient) {
		openCalls++
		client.w.OpenOrder(42, ibapi.NewContract(), ibTestVenueOrder(), &ibapi.OrderState{Status: "Submitted"})
	}
	c := testIBConnector(t, v)
	s := ibTestSession(t, c, v)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	s.lookupToken = make(chan struct{})
	s.mu.Lock()
	go func() {
		_, _, err := c.Lookup(ctx, fwtrading.OrderRef{ClientOrderID: "officer-order"})
		first <- err
	}()
	select {
	case s.lookupToken <- struct{}{}:
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("lookup did not acquire token")
	}
	// Lookup owns the token but cannot register or issue its request yet.
	cancel()
	s.mu.Unlock()
	select {
	case <-s.lookupToken:
	case <-time.After(time.Second):
		t.Fatal("lookup did not return token")
	}
	if err := <-first; !errors.Is(err, errIBRequestNotIssued) || !errors.Is(err, context.Canceled) {
		t.Fatalf("first lookup error=%v", err)
	}
	s.lookupToken = make(chan struct{}, 1)
	s.lookupToken <- struct{}{}
	snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "officer-order"})
	if err != nil || !found || snapshot.VenueOrderID != "4242" {
		t.Fatalf("following lookup could not complete: snapshot=%+v found=%v err=%v", snapshot, found, err)
	}
	if openCalls != 1 {
		t.Fatalf("open requests issued=%d, want 1", openCalls)
	}
	s.mu.Lock()
	openRequests, completedRequests := s.openRequests, s.completedRequests
	s.mu.Unlock()
	if openRequests != 0 || completedRequests != 0 {
		t.Fatalf("request counters leaked: open=%d completed=%d", openRequests, completedRequests)
	}
	if s.ctx.Err() != nil {
		t.Fatal("unissued lookup ended session")
	}
}

func TestIBLookupAbandonedEndMarkers(t *testing.T) {
	v := newFakeIBVenue()
	type request struct {
		name string
		id   int64
	}
	issued := make(chan request, 10)
	v.request = func(_ *fakeIBTradingClient, name string, id int64) bool { issued <- request{name, id}; return true }
	c := testIBConnector(t, v)
	s := ibTestSession(t, c, v)
	first := make(chan error, 1)
	go func() {
		_, _, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "officer-order"})
		first <- err
	}()
	for i := 0; i < 3; i++ {
		<-issued
	}
	s.mu.Lock()
	firstQuery := s.lookup
	s.mu.Unlock()
	s.Error(firstQuery.reqID, 0, 321, "abandon first lookup", "")
	if err := <-first; err == nil {
		t.Fatal("abandoned lookup succeeded")
	}
	result := make(chan error, 1)
	go func() {
		snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "officer-order"})
		if err == nil && (!found || snapshot.VenueOrderID != "4242") {
			err = fmt.Errorf("second lookup completed without its order: found=%v snapshot=%+v", found, snapshot)
		}
		result <- err
	}()
	var reqID int64
	for i := 0; i < 3; i++ {
		r := <-issued
		if r.name == "executions" {
			reqID = r.id
		}
	}
	s.ExecDetailsEnd(reqID)
	s.OpenOrderEnd()
	s.CompletedOrdersEnd()
	select {
	case err := <-result:
		t.Fatalf("abandoned end markers completed next lookup: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	s.OpenOrder(42, ibapi.NewContract(), ibTestVenueOrder(), &ibapi.OrderState{Status: "Submitted"})
	s.OpenOrderEnd()
	s.CompletedOrdersEnd()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second lookup did not complete on its own markers")
	}
}

func TestIBAcknowledgedRejection(t *testing.T) {
	for _, tc := range []struct {
		code    int64
		message string
	}{{200, "rejected after ack"}, {201, "rejected after ack"}, {321, "rejected after ack"}, {201, ""}} {
		t.Run(fmt.Sprintf("%d/%q", tc.code, tc.message), func(t *testing.T) {
			v := newFakeIBVenue()
			c := testIBConnector(t, v)
			s := ibTestSession(t, c, v)
			s.OpenOrder(42, ibapi.NewContract(), ibTestVenueOrder(), &ibapi.OrderState{Status: "Inactive"})
			s.events()
			s.Error(42, 0, tc.code, tc.message, "")
			events, _ := s.events()
			if len(events) != 1 || events[0].Kind != fwtrading.EventOrder || events[0].Ref.ClientOrderID != "officer-order" {
				t.Fatalf("rejection events=%+v", events)
			}
			snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "officer-order", VenueOrderID: "4242"})
			if err != nil || !found || snapshot.Status != fwtrading.VenueStatusCancelled || snapshot.Reason != tc.message {
				t.Fatalf("rejection snapshot=%+v found=%v err=%v", snapshot, found, err)
			}
		})
	}
}

func TestIBCompletedOrdersAreTerminal(t *testing.T) {
	for _, status := range []string{"Inactive", "Submitted", "ApiCancelled", "unknown", "Filled"} {
		t.Run(status, func(t *testing.T) {
			v := newFakeIBVenue()
			v.completed = func(client *fakeIBTradingClient) {
				order := ibTestVenueOrder()
				order.FilledQuantity = ibapi.ZERO
				if status == "Filled" {
					order.FilledQuantity = ibapi.StringToDecimal("10")
				}
				client.w.CompletedOrder(ibapi.NewContract(), order, &ibapi.OrderState{Status: status, CompletedStatus: "venue terminal text"})
			}
			if status == "Filled" {
				v.executions = func(client *fakeIBTradingClient, id int64) {
					client.w.ExecDetails(id, ibapi.NewContract(), ibTestExecution("done", "10", "10"))
					client.w.CommissionAndFeesReport(ibapi.CommissionAndFeesReport{ExecID: "done", CommissionAndFees: 0.5, Currency: "USD"})
				}
			}
			c := testIBConnector(t, v)
			ibTestSession(t, c, v)
			snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "officer-order"})
			want := fwtrading.VenueStatusCancelled
			if status == "Filled" {
				want = fwtrading.VenueStatusFilled
			}
			if err != nil || !found || snapshot.Status != want || snapshot.Reason != "venue terminal text" {
				t.Fatalf("completed snapshot=%+v found=%v err=%v", snapshot, found, err)
			}
		})
	}
}

func ibTestVenueOrder() *ibapi.Order {
	order := ibapi.NewOrder()
	order.OrderID, order.OrderRef, order.PermID = 42, "officer-order", 4242
	order.TotalQuantity = ibapi.StringToDecimal("10")
	return order
}

func TestIBLookupNotFoundRule(t *testing.T) {
	v := newFakeIBVenue()
	c := testIBConnector(t, v)
	var mu sync.Mutex
	var diagnostics []health.Diagnostic
	c.SetDiagnosticReporter(func(d health.Diagnostic) { mu.Lock(); diagnostics = append(diagnostics, d); mu.Unlock() })
	subscribeIB(t, c)
	_, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "unknown"})
	if err != nil || found {
		t.Fatalf("unacknowledged unknown: found=%v err=%v", found, err)
	}
	_, found, err = c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "unknown", VenueOrderID: "99"})
	if !errors.Is(err, errIBHistoryLimit) || !found {
		t.Fatalf("acknowledged unknown: found=%v, got %v, want errIBHistoryLimit", found, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Detail, "since local midnight") {
		t.Fatalf("history limit is invisible: %+v", diagnostics)
	}
}

func TestIBLookupHistory(t *testing.T) {
	for _, kind := range []string{"complete", "partial", "commission-first", "completed-no-order-id", "completed-beats-open"} {
		t.Run(kind, func(t *testing.T) {
			v := newFakeIBVenue()
			v.open = func(client *fakeIBTradingClient) {
				if kind == "completed-no-order-id" {
					return
				}
				order := ibTestVenueOrder()
				if kind == "completed-beats-open" {
					client.w.OpenOrder(42, ibapi.NewContract(), order, &ibapi.OrderState{Status: "Submitted"})
					client.w.OrderStatus(42, "Submitted", ibapi.StringToDecimal("4"), ibapi.StringToDecimal("6"), 100, 4242, 0, 0, 0, "", 0)
					return
				}
				client.w.OpenOrder(42, ibapi.NewContract(), order, &ibapi.OrderState{Status: "Filled"})
				client.w.OrderStatus(42, "Filled", ibapi.StringToDecimal("10"), ibapi.ZERO, 100, 4242, 0, 0, 0, "", 0)
			}
			v.completed = func(client *fakeIBTradingClient) {
				if kind != "completed-no-order-id" && kind != "completed-beats-open" {
					return
				}
				order := ibTestVenueOrder()
				order.OrderID, order.FilledQuantity = 0, ibapi.StringToDecimal("10")
				client.w.CompletedOrder(ibapi.NewContract(), order, &ibapi.OrderState{Status: "Filled"})
			}
			v.executions = func(client *fakeIBTradingClient, reqID int64) {
				for i, cum := range []string{"4", "10"} {
					if kind == "partial" && i == 0 {
						continue
					}
					execution := ibTestExecution(cum, []string{"4", "6"}[i], cum)
					commission := ibapi.CommissionAndFeesReport{ExecID: cum, CommissionAndFees: 0.5, Currency: "USD"}
					if kind == "commission-first" {
						client.w.CommissionAndFeesReport(commission)
					}
					client.w.ExecDetails(reqID, ibapi.NewContract(), execution)
					client.w.CommissionAndFeesReport(commission)
				}
			}
			c := testIBConnector(t, v)
			diagnostics := make(chan health.Diagnostic, 10)
			c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics <- d })
			subscribeIB(t, c)
			snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "officer-order", VenueOrderID: "4242"})
			if kind == "partial" {
				if !errors.Is(err, errIBHistoryLimit) || !strings.Contains(err.Error(), "since local midnight") {
					t.Fatalf("partial history: found=%v err=%v", found, err)
				}
				select {
				case <-diagnostics:
				default:
					t.Fatal("partial history diagnostic missing")
				}
				return
			}
			if err != nil || !found || snapshot.VenueOrderID != "4242" || snapshot.Status != fwtrading.VenueStatusFilled || snapshot.FilledQuantity != "10" || len(snapshot.Fills) != 2 {
				t.Fatalf("snapshot=%+v found=%v err=%v", snapshot, found, err)
			}
			if snapshot.Fills[0].CumQuantity != "4" || snapshot.Fills[1].CumQuantity != "10" || !snapshot.Fills[1].Final || snapshot.Fills[0].Commission.Amount != "0.5" {
				t.Fatalf("fills=%+v", snapshot.Fills)
			}
		})
	}
}
