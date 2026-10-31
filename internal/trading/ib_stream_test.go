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
	"strings"
	"testing"
	"time"

	"github.com/scmhub/ibapi"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

func TestIBStreamReconnectHealthAndCancellation(t *testing.T) {
	v := newFakeIBVenue()
	v.connectHook = func(client *fakeIBTradingClient) {
		v.mu.Lock()
		first := v.connects == 1
		v.mu.Unlock()
		if first {
			v.connectError = errors.New("test-host:7497 secret internal error")
		} else {
			v.connectError = nil
		}
	}
	c := testIBConnector(t, v)
	type status struct {
		ok      bool
		message string
	}
	statuses := make(chan status, 20)
	c.SetStatusReporter(func(ok bool, message string) { statuses <- status{ok, message} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if event := nextIBEvent(t, events); event.Kind != fwtrading.EventConnected {
		t.Fatalf("event=%+v", event)
	}
	failed, connected := <-statuses, <-statuses
	if failed.ok || failed.message == "" || strings.Contains(failed.message, "test-host") || strings.Contains(failed.message, "secret") || !connected.ok {
		t.Fatalf("health failure=%+v connected=%+v", failed, connected)
	}
	v.mu.Lock()
	live := v.clients[len(v.clients)-1]
	v.mu.Unlock()
	live.w.ConnectionClosed()
	if event := nextIBEvent(t, events); event.Kind != fwtrading.EventConnected {
		t.Fatalf("reconnect=%+v", event)
	}
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("cancellation left the stream open")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close stream")
	}
	c.Close()
	if v.connects != 3 || v.disconnects != 3 {
		t.Fatalf("sessions leaked: %d/%d", v.connects, v.disconnects)
	}
}

func TestIBBlockedRequestsCancelSession(t *testing.T) {
	for _, name := range []string{"accounts", "details", "open", "completed", "executions", "place"} {
		t.Run(name, func(t *testing.T) {
			v := newFakeIBVenue()
			release := make(chan struct{})
			defer close(release)
			v.request = func(_ *fakeIBTradingClient, request string, _ int64) bool {
				if request != name {
					return false
				}
				<-release
				return true
			}
			c := testIBConnector(t, v)
			subscribeIB(t, c)
			s, err := c.liveSession()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				var err error
				switch name {
				case "accounts":
					_, err = s.accounts(ctx)
				case "details", "place":
					_, err = c.Send(ctx, ibTestOrder())
				default:
					_, _, err = c.Lookup(ctx, fwtrading.OrderRef{ClientOrderID: "officer-order"})
				}
				result <- err
			}()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("blocked request succeeded")
				}
				if name == "place" && (errors.Is(err, fwtrading.ErrNotSent) || !strings.Contains(err.Error(), "unknown")) {
					t.Fatalf("PlaceOrder outcome=%v", err)
				}
				if name == "details" && !errors.Is(err, fwtrading.ErrNotSent) {
					t.Fatalf("pre-send outcome=%v", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("request ignored context deadline")
			}
			if s.ctx.Err() == nil {
				t.Fatal("stalled request left session usable")
			}
		})
	}
}

func TestIBRequestNotIssued(t *testing.T) {
	for _, ended := range []string{"request", "session"} {
		t.Run(ended, func(t *testing.T) {
			v := newFakeIBVenue()
			c := testIBConnector(t, v)
			s := ibTestSession(t, c, v)
			ctx := context.Background()
			cause := context.Canceled
			if ended == "request" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				cause = context.DeadlineExceeded
			} else {
				s.cancel()
			}
			called := make(chan struct{}, 1)
			err := s.request(ctx, func() { called <- struct{}{} })
			if !errors.Is(err, errIBRequestNotIssued) || !errors.Is(err, cause) {
				t.Fatalf("request error=%v", err)
			}
			select {
			case <-called:
				t.Fatal("request was issued")
			default:
			}
			if ended == "request" && s.ctx.Err() != nil {
				t.Fatalf("unissued request ended session: %v", s.ctx.Err())
			}
		})
	}
}

type ibCancelOnErrContext struct {
	context.Context
	onErr func()
}

func (c ibCancelOnErrContext) Err() error {
	c.onErr()
	return c.Context.Err()
}

func TestIBSendPlaceOrderNotIssued(t *testing.T) {
	v := newFakeIBVenue()
	c := testIBConnector(t, v)
	s := ibTestSession(t, c, v)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks := 0
	s.ctx = ibCancelOnErrContext{Context: s.ctx, onErr: func() {
		checks++
		// The second check is Send's guard after contract resolution.
		if checks == 2 {
			cancel()
		}
	}}
	_, err := c.Send(ctx, ibTestOrder())
	if !errors.Is(err, fwtrading.ErrNotSent) || !errors.Is(err, errIBRequestNotIssued) || !errors.Is(err, context.Canceled) {
		t.Fatalf("unissued PlaceOrder outcome=%v", err)
	}
	v.mu.Lock()
	places := v.places
	v.mu.Unlock()
	if places != 0 {
		t.Fatalf("unissued PlaceOrder reached venue: places=%d", places)
	}
	s.mu.Lock()
	orders, nextID := len(s.orders), s.nextID
	s.mu.Unlock()
	if orders != 0 || nextID != 102 {
		t.Fatalf("unissued order retained: orders=%d nextID=%d", orders, nextID)
	}
	if s.ctx.Err() != nil {
		t.Fatal("unissued PlaceOrder ended session")
	}
}

func TestIBSendContractTimeoutNotSent(t *testing.T) {
	v := newFakeIBVenue()
	v.request = func(_ *fakeIBTradingClient, name string, _ int64) bool { return name == "details" }
	c := testIBConnector(t, v)
	subscribeIB(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Send(ctx, ibTestOrder())
	if !errors.Is(err, fwtrading.ErrNotSent) || !strings.Contains(err.Error(), "deadline") || v.places != 0 {
		t.Fatalf("contract timeout=%v places=%d", err, v.places)
	}
}

func TestIBRecoverableNotices(t *testing.T) {
	v := newFakeIBVenue()
	c := testIBConnector(t, v)
	diagnostics := make(chan health.Diagnostic, 20)
	c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics <- d })
	events := subscribeIB(t, c)
	s, err := c.liveSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int64{1100, 2103, 2105, 2110} {
		s.Error(-1, 0, code, "farm unavailable", "")
		if s.ctx.Err() != nil {
			t.Fatalf("notice %d cancelled live session", code)
		}
		if d := <-diagnostics; d.Level != health.DiagWarn {
			t.Fatalf("diagnostic=%+v", d)
		}
	}
	s.Error(-1, 0, 1101, "connectivity restored", "")
	if event := nextIBEvent(t, events); event.Kind != fwtrading.EventConnected {
		t.Fatalf("restored=%+v", event)
	}
	select {
	case event := <-events:
		t.Fatalf("extra event=%+v", event)
	case <-time.After(30 * time.Millisecond):
	}
	if s.ctx.Err() != nil {
		t.Fatal("restored notice cancelled session")
	}
	v.request = func(client *fakeIBTradingClient, name string, _ int64) bool {
		if name == "accounts" {
			probe := client.w.(*ibTradingSession)
			if probe.clientID != c.clientID+ibProbeClientIDOffset || probe.clientID != client.id {
				t.Error("probe uses stream client id")
			}
			for _, code := range []int64{2103, 2105} {
				client.w.Error(-1, 0, code, "farm unavailable", "")
				if probe.ctx.Err() != nil {
					t.Errorf("notice %d cancelled probe", code)
				}
			}
		}
		return false
	}
	if _, err := c.CheckAccount(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIBImmediateDeathsGrowBackoff(t *testing.T) {
	v := newFakeIBVenue()
	c := testIBConnector(t, v)
	events := subscribeIB(t, c)
	var delays []time.Duration
	for i := 0; i < 3; i++ {
		v.mu.Lock()
		client := v.clients[len(v.clients)-1]
		v.mu.Unlock()
		start := time.Now()
		client.w.ConnectionClosed()
		if event := nextIBEvent(t, events); event.Kind != fwtrading.EventConnected {
			t.Fatalf("reconnect=%+v", event)
		}
		delays = append(delays, time.Since(start))
	}
	if delays[1] < 150*time.Millisecond || delays[2] < 300*time.Millisecond {
		t.Fatalf("immediate sessions reset backoff: %v", delays)
	}
}

func TestIBOrdersAndRequestsSharePositiveIDs(t *testing.T) {
	v := newFakeIBVenue()
	var ids []int64
	v.request = func(_ *fakeIBTradingClient, name string, id int64) bool {
		if name == "details" || name == "executions" || name == "place" {
			ids = append(ids, id)
		}
		return false
	}
	c := testIBConnector(t, v)
	ibTestSession(t, c, v)
	if err := c.PreCheck(context.Background(), ibTestOrder()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), ibTestOrder()); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 4 {
		t.Fatalf("ids=%v", ids)
	}
	for i, id := range ids {
		if id != int64(100+i) {
			t.Fatalf("request/order IDs are not one positive sequence: %v", ids)
		}
	}
}

func TestIBZeroNextValidIDUsesPositiveRequests(t *testing.T) {
	v := newFakeIBVenue()
	v.request = func(_ *fakeIBTradingClient, name string, id int64) bool {
		if name == "details" && id != 1 {
			t.Errorf("nextValidId zero produced request ID %d", id)
		}
		return false
	}
	c := testIBConnector(t, v)
	s := newIBTradingSession(context.Background(), c)
	defer s.cancel()
	s.client = v.client(s)
	s.NextValidID(0)
	if _, err := s.contractDetails(context.Background(), ibapi.NewContract()); err != nil {
		t.Fatal(err)
	}
}

func TestIBStatusTable(t *testing.T) {
	for _, raw := range []string{"PendingSubmit", "PreSubmitted", "Submitted", "ApiPending", "PendingCancel", "Inactive"} {
		if status, known := ibVenueStatus(raw); !known || status != fwtrading.VenueStatusOpen {
			t.Fatalf("%s=%s", raw, status)
		}
	}
	for _, raw := range []string{"Cancelled", "ApiCancelled"} {
		if status, known := ibVenueStatus(raw); !known || status != fwtrading.VenueStatusCancelled {
			t.Fatalf("%s=%s", raw, status)
		}
	}
	if status, known := ibVenueStatus("Filled"); !known || status != fwtrading.VenueStatusFilled {
		t.Fatal("filled mapping")
	}
	if _, known := ibVenueStatus("future-status"); known {
		t.Fatal("unknown status accepted")
	}
}

func TestIBExecutionTime(t *testing.T) {
	for _, raw := range []string{"20261027-12:00:00", "20261027 12:00:00 UTC", "20261027 08:00:00 America/New_York", "20261027 12:00:00"} {
		at, err := ibExecutionTime(raw, "20261027 12:00:00 UTC")
		if err != nil || !at.Equal(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("time %s=%s: %v", raw, at, err)
		}
	}
	if _, err := ibExecutionTime("20261027 12:00:00", ""); err == nil {
		t.Fatal("timezone silently guessed")
	}
	if _, err := ibExecutionTime("invalid", ""); err == nil {
		t.Fatal("invalid execution time accepted")
	}
}

func TestIBContractRequestErrorAndTimeout(t *testing.T) {
	for _, kind := range []string{"no-definition", "provider-error", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			v := newFakeIBVenue()
			c := testIBConnector(t, v)
			v.details = func(*ibapi.Contract) []*ibapi.ContractDetails {
				v.mu.Lock()
				client := v.clients[0]
				v.mu.Unlock()
				if kind == "no-definition" {
					client.w.Error(100, 0, 200, "not found", "")
				}
				if kind == "provider-error" {
					client.w.Error(100, 0, 321, "bad request", "")
				}
				return nil
			}
			if kind == "timeout" {
				v.noReady = true
				c.timeout = 20 * time.Millisecond
				v.connectHook = func(client *fakeIBTradingClient) {
					// Confirming ConnectAck alone must not release the barrier.
					client.w.ConnectAck()
				}
				_, err := c.VerifySymbol(context.Background(), "AAPL")
				if err == nil {
					t.Fatal("ConnectAck released nextValidId readiness barrier")
				}
				return
			}
			result, err := c.VerifySymbol(context.Background(), "AAPL")
			if kind == "no-definition" && (err != nil || result.Exists) {
				t.Fatalf("code 200=%+v %v", result, err)
			}
			if kind == "provider-error" && (err == nil || !strings.Contains(err.Error(), "bad request")) {
				t.Fatalf("provider error=%v", err)
			}
		})
	}
}

func TestIBSubscribeRefusals(t *testing.T) {
	for _, state := range []string{
		"closed", "subscribed", "missing_status", "missing_diagnostic", "missing_both",
	} {
		t.Run(state, func(t *testing.T) {
			c := testIBConnector(t, newFakeIBVenue())
			want := "ib: connector closed or already subscribed"
			switch state {
			case "closed":
				c.Close()
				c.SetStatusReporter(nil)
				c.SetDiagnosticReporter(nil)
			case "subscribed":
				subscribeIB(t, c)
			case "missing_status":
				c.SetStatusReporter(nil)
				want = "ib: stream requires status and diagnostic reporters"
			case "missing_diagnostic":
				c.SetDiagnosticReporter(nil)
				want = "ib: stream requires status and diagnostic reporters"
			case "missing_both":
				c.SetStatusReporter(nil)
				c.SetDiagnosticReporter(nil)
				want = "ib: stream requires status and diagnostic reporters"
			}
			out, err := c.Subscribe(context.Background())
			if out != nil || err == nil || err.Error() != want {
				t.Fatalf("Subscribe = (%v, %v), want (nil, %q)", out, err, want)
			}
		})
	}
}

func TestIBReconnectStatusText(t *testing.T) {
	v := newFakeIBVenue()
	v.connectHook = func(*fakeIBTradingClient) {
		v.mu.Lock()
		first := v.connects == 1
		v.mu.Unlock()
		if first {
			v.connectError = errors.New("private connection error")
		} else {
			v.connectError = nil
		}
	}
	c := testIBConnector(t, v)
	type status struct {
		ok      bool
		message string
	}
	statuses := make(chan status, 8)
	c.SetStatusReporter(func(ok bool, message string) {
		statuses <- status{ok, message}
	})
	events := subscribeIB(t, c)
	v.mu.Lock()
	client := v.clients[len(v.clients)-1]
	v.mu.Unlock()
	client.w.ConnectionClosed()
	if event := nextIBEvent(t, events); event.Kind != fwtrading.EventConnected {
		t.Fatalf("reconnect = %+v", event)
	}
	for i := 0; i < 4; i++ {
		wantOK, wantMessage := false, "IB trading session disconnected; reconnecting"
		if i%2 == 1 {
			wantOK, wantMessage = true, ""
		}
		select {
		case got := <-statuses:
			if got.ok != wantOK || got.message != wantMessage {
				t.Fatalf("status %d = (%v, %q), want (%v, %q)",
					i, got.ok, got.message, wantOK, wantMessage)
			}
		case <-time.After(time.Second):
			t.Fatalf("status %d was not reported", i)
		}
	}
}

func TestIBCloseRefusesProbesDuringStreamTeardown(t *testing.T) {
	v := newFakeIBVenue()
	probeVenue := newFakeIBVenue()
	c := testIBConnector(t, v)
	c.newClient = func(w ibapi.EWrapper) ibTradingClient {
		if w.(*ibTradingSession).clientID == c.clientID {
			return v.client(w)
		}
		return probeVenue.client(w)
	}
	subscribeIB(t, c)
	s, err := c.liveSession()
	if err != nil {
		t.Fatal(err)
	}

	// Block the stream's Disconnect without blocking probe clients.
	v.mu.Lock()
	closed := make(chan struct{})
	defer func() {
		v.mu.Unlock()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Error("Close did not finish after stream teardown was released")
		}
	}()
	go func() {
		c.Close()
		close(closed)
	}()
	select {
	case <-s.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the stream")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before stream teardown finished")
	default:
	}
	if _, err := c.CheckAccount(context.Background()); err == nil || err.Error() != "ib: connector is closed" {
		t.Fatalf("CheckAccount during stream teardown = %v, want ib: connector is closed", err)
	}
}
