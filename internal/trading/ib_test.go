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
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/ib"
)

type fakeIBVenue struct {
	mu                            sync.Mutex
	clients                       []*fakeIBTradingClient
	active                        map[int64]int
	maxActive                     int
	connects, disconnects, places int
	accounts                      []string
	connectError                  error
	noReady                       bool
	connectHook                   func(*fakeIBTradingClient)
	details                       func(*ibapi.Contract) []*ibapi.ContractDetails
	place                         func(*fakeIBTradingClient, int64, *ibapi.Contract, *ibapi.Order)
	open                          func(*fakeIBTradingClient)
	completed                     func(*fakeIBTradingClient)
	executions                    func(*fakeIBTradingClient, int64)
	request                       func(*fakeIBTradingClient, string, int64) bool
	executionFilter               func(*ibapi.ExecutionFilter)
}

type fakeIBTradingClient struct {
	venue  *fakeIBVenue
	w      ibapi.EWrapper
	id     int64
	closed bool
}

func newFakeIBVenue() *fakeIBVenue {
	return &fakeIBVenue{active: make(map[int64]int), accounts: []string{"DU123"},
		details: func(c *ibapi.Contract) []*ibapi.ContractDetails {
			if c.SecType != "STK" {
				return nil
			}
			copy := *c
			copy.ConID, copy.Currency, copy.PrimaryExchange = 1, "USD", "NASDAQ"
			return []*ibapi.ContractDetails{{Contract: copy, OrderTypes: "MKT,LMT", LongName: "Apple"}}
		},
		place: func(c *fakeIBTradingClient, id int64, contract *ibapi.Contract, order *ibapi.Order) {
			copy := *order
			copy.PermID = 999
			copy.ClientID = c.id
			c.w.OpenOrder(id, contract, &copy, &ibapi.OrderState{Status: "Submitted"})
		},
	}
}

func (v *fakeIBVenue) client(w ibapi.EWrapper) ibTradingClient {
	c := &fakeIBTradingClient{venue: v, w: w}
	v.mu.Lock()
	v.clients = append(v.clients, c)
	v.mu.Unlock()
	return c
}

func (c *fakeIBTradingClient) Connect(_ string, _ int, id int64) error {
	v := c.venue
	v.mu.Lock()
	c.id = id
	v.connects++
	v.active[id]++
	if v.active[id] > v.maxActive {
		v.maxActive = v.active[id]
	}
	v.mu.Unlock()
	if v.connectHook != nil {
		v.connectHook(c)
	}
	if v.connectError != nil {
		return v.connectError
	}
	if !v.noReady {
		c.w.NextValidID(100)
	}
	return nil
}

func (c *fakeIBTradingClient) Disconnect() error {
	v := c.venue
	v.mu.Lock()
	defer v.mu.Unlock()
	if !c.closed {
		c.closed = true
		v.active[c.id]--
		v.disconnects++
	}
	return nil
}

func (c *fakeIBTradingClient) TWSConnectionTime() string { return "20261027 12:00:00 UTC" }
func (c *fakeIBTradingClient) ReqManagedAccts() {
	if c.venue.request != nil && c.venue.request(c, "accounts", 0) {
		return
	}
	c.w.ManagedAccounts(c.venue.accounts)
}
func (c *fakeIBTradingClient) ReqContractDetails(id int64, contract *ibapi.Contract) {
	if c.venue.request != nil && c.venue.request(c, "details", id) {
		return
	}
	for _, d := range c.venue.details(contract) {
		c.w.ContractDetails(id, d)
	}
	c.w.ContractDetailsEnd(id)
}
func (c *fakeIBTradingClient) PlaceOrder(id int64, contract *ibapi.Contract, order *ibapi.Order) {
	c.venue.mu.Lock()
	c.venue.places++
	c.venue.mu.Unlock()
	if c.venue.request != nil && c.venue.request(c, "place", id) {
		return
	}
	c.venue.place(c, id, contract, order)
}
func (c *fakeIBTradingClient) ReqOpenOrders() {
	if c.venue.request != nil && c.venue.request(c, "open", 0) {
		return
	}
	if c.venue.open != nil {
		c.venue.open(c)
	}
	c.w.OpenOrderEnd()
}
func (c *fakeIBTradingClient) ReqCompletedOrders(bool) {
	if c.venue.request != nil && c.venue.request(c, "completed", 0) {
		return
	}
	if c.venue.completed != nil {
		c.venue.completed(c)
	}
	c.w.CompletedOrdersEnd()
}
func (c *fakeIBTradingClient) ReqExecutions(id int64, filter *ibapi.ExecutionFilter) {
	if c.venue.executionFilter != nil {
		c.venue.executionFilter(filter)
	}
	if c.venue.request != nil && c.venue.request(c, "executions", id) {
		return
	}
	if c.venue.executions != nil {
		c.venue.executions(c, id)
	}
	c.w.ExecDetailsEnd(id)
}

func testIBConnector(t *testing.T, venue *fakeIBVenue) *ibConnector {
	t.Helper()
	connector, err := NewIBConnector(domain.TradingConnection{ExternalID: "ib-test", Mode: domain.TradingModePaper, Credentials: `{"host":"test-host","port":7497,"clientId":0}`})
	if err != nil {
		t.Fatal(err)
	}
	c := connector.(*ibConnector)
	c.newClient, c.timeout = venue.client, time.Second
	c.SetStatusReporter(func(bool, string) {})
	c.SetDiagnosticReporter(func(health.Diagnostic) {})
	t.Cleanup(c.Close)
	return c
}

func subscribeIB(t *testing.T, c *ibConnector) <-chan fwtrading.Event {
	t.Helper()
	events, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event := nextIBEvent(t, events); event.Kind != fwtrading.EventConnected {
		t.Fatalf("first event = %v", event)
	}
	return events
}

func nextIBEvent(t *testing.T, events <-chan fwtrading.Event) fwtrading.Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event stream closed")
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for IB event")
		return fwtrading.Event{}
	}
}

func ibTestOrder() fwtrading.Order {
	return fwtrading.Order{ClientOrderID: "officer-order", Symbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", VenueAccount: "DU123", Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, Quantity: "10", Route: `{"timeInForce":"day"}`}
}

func TestIBCredentials(t *testing.T) {
	for _, tc := range []struct{ raw, field string }{
		{`{"port":7497}`, "host"}, {`{"host":"x"}`, "port"},
		{`{"host":"x","port":0}`, "port"}, {`{"host":"x","port":65536}`, "port"},
		{`{"host":"x","port":1,"extra":"secret"}`, "unknown"},
		{`{"host":"x","port":1,"clientId":null}`, "clientId"},
		{`{"host":"x","port":1,"clientId":-1}`, "clientId"},
		{`{"host":"x","port":1,"clientId":2147483647}`, "clientId"},
		{`{"host":"x","port":"secret"}`, "port"},
	} {
		_, err := NewIBConnector(domain.TradingConnection{Mode: domain.TradingModePaper, Credentials: tc.raw})
		if err == nil || !strings.Contains(err.Error(), tc.field) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s: %v", tc.raw, err)
		}
	}
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("connection-%d", i)
		connector, err := NewIBConnector(domain.TradingConnection{ExternalID: domain.ExternalID(id), Mode: domain.TradingModePaper, Credentials: `{"host":"x","port":1}`})
		if err != nil {
			t.Fatal(err)
		}
		c := connector.(*ibConnector)
		if c.clientID != ib.ClientID(id)+ibTradingClientIDOffset || c.clientID <= 10_900_999 || c.clientID+ibProbeClientIDOffset <= 20_900_999 {
			t.Fatalf("client id ranges overlap: %d", c.clientID)
		}
		c.Close()
	}
	connector, err := NewIBConnector(domain.TradingConnection{Mode: domain.TradingModeLive, Credentials: `{"host":"x","port":1,"clientId":77}`})
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	if connector.(*ibConnector).clientID != 77 {
		t.Fatal("configured clientId ignored")
	}
}

func TestIBModeGuard(t *testing.T) {
	for _, tc := range []struct {
		mode      domain.TradingMode
		accounts  []string
		wantError string
	}{
		{domain.TradingModePaper, []string{"DU2", "DU1"}, ""},
		{domain.TradingModeLive, []string{"U1"}, ""},
		{domain.TradingModePaper, []string{"U1"}, "paper mode found a live"},
		{domain.TradingModeLive, []string{"DU1"}, "live mode found a paper"},
		{domain.TradingModePaper, nil, "no managed accounts"},
	} {
		v := newFakeIBVenue()
		v.accounts = tc.accounts
		c := testIBConnector(t, v)
		c.mode = tc.mode
		info, err := c.CheckAccount(context.Background())
		if tc.wantError != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("guard: %v", err)
			}
		} else if err != nil || info.Status != "active" || info.Identity == "" {
			t.Fatalf("account = %+v: %v", info, err)
		}
		if len(tc.accounts) == 2 && info.Identity != "DU1,DU2" {
			t.Fatal("identity is not sorted")
		}
		if v.connects != v.disconnects {
			t.Fatal("probe session leaked")
		}
	}
}

func TestIBSend(t *testing.T) {
	for _, kind := range []string{"ack", "reject", "timeout", "ambiguous", "ack-before-reject", "not-ready"} {
		t.Run(kind, func(t *testing.T) {
			v := newFakeIBVenue()
			v.place = func(c *fakeIBTradingClient, id int64, contract *ibapi.Contract, order *ibapi.Order) {
				if order.OrderRef != ibTestOrder().ClientOrderID || order.Account != "DU123" || id != 101 {
					t.Errorf("wrong venue order: %+v id=%d", order, id)
				}
				switch kind {
				case "ack", "ack-before-reject":
					c.w.OrderStatus(id, "Submitted", ibapi.ZERO, ibapi.StringToDecimal("10"), 0, 12345, 0, 0, 0, "", 0)
					if kind == "ack-before-reject" {
						c.w.Error(id, 0, 201, "late rejection", "")
					}
				case "reject":
					c.w.Error(id, 0, 201, "order refused by venue", "")
				case "ambiguous":
					c.w.Error(id, 0, 103, "duplicate order id", "")
				}
			}
			c := testIBConnector(t, v)
			if kind != "not-ready" {
				subscribeIB(t, c)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			ack, err := c.Send(ctx, ibTestOrder())
			switch kind {
			case "ack", "ack-before-reject":
				if err != nil || ack.VenueOrderID != "12345" {
					t.Fatalf("ack=%+v err=%v", ack, err)
				}
			case "reject":
				if !errors.Is(err, fwtrading.ErrRejected) || !strings.Contains(err.Error(), "order refused by venue") {
					t.Fatalf("rejection=%v", err)
				}
			case "not-ready":
				if !errors.Is(err, fwtrading.ErrNotSent) || v.places != 0 {
					t.Fatalf("not sent=%v places=%d", err, v.places)
				}
			default:
				if err == nil || errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrNotSent) || !strings.Contains(err.Error(), "unknown") {
					t.Fatalf("unknown outcome=%v", err)
				}
			}
		})
	}
	for _, code := range []int64{200, 201, 321} {
		if !ibDefinitiveRejection(code) {
			t.Fatalf("missing rejection code %d", code)
		}
	}
	for _, code := range []int64{103, 202, 322, 2109, 1100, 504, 9999} {
		if ibDefinitiveRejection(code) {
			t.Fatalf("unsafe rejection code %d", code)
		}
	}
}
