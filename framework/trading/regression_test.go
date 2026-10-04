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
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

func TestSendRechecksEligibility(t *testing.T) {
	for _, window := range []string{"pre-check", "claim"} {
		for _, activity := range []string{"confirm", "client-report", "client-fill"} {
			t.Run(window+"/"+activity, func(t *testing.T) {
				f := newFixture(t)
				order := f.submit("10")
				changeOrder := func() error {
					if activity == "confirm" {
						_, err := f.n.ConfirmOrder(testCtx, order.ExternalID, testCaller)
						return err
					}
					in := domain.ExecutionReportInput{
						ExternalID: "client-report", Order: order.ExternalID,
						OrderStatus: domain.OrderStatusCommitted,
					}
					if activity == "client-fill" {
						in.OrderStatus = domain.OrderStatusPartiallyFilled
						in.FillQuantity, in.FillPrice, in.LeavesQuantity = "3", "2", "7"
					}
					return f.sink(testCtx, in)
				}
				if window == "pre-check" {
					f.venue.onPreCheck = func(context.Context, trading.Order) error {
						return changeOrder()
					}
					f.start()
				} else {
					sink := func(ctx context.Context, in domain.ExecutionReportInput) error {
						if strings.HasSuffix(string(in.ExternalID), ":claim") {
							if err := changeOrder(); err != nil {
								return err
							}
						}
						return f.sink(ctx, in)
					}
					r, err := trading.NewRuntime(f.registry, f.st, sink, f.adjustmentSink,
						slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
					must(t, err)
					must(t, r.Start(testCtx))
					f.runtime = r
					t.Cleanup(r.Stop)
				}
				link, err := f.runtime.Send(testCtx, order.ExternalID, f.dest)
				if !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("changed order send error = %v, want conflict", err)
				}
				condition := "execution-report activity"
				switch activity {
				case "confirm":
					condition = "confirmed"
				case "client-fill":
					condition = "not committed"
					if window == "claim" {
						condition = "trades"
					}
				}
				if !strings.Contains(err.Error(), condition) {
					t.Fatalf("send error %v does not name %q", err, condition)
				}
				if sends, _ := f.venue.counts(); sends != 0 || link.SendAttempted {
					t.Fatalf("changed order was sent: sends=%d link=%+v", sends, link)
				}
				_, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
				must(t, err)
				if found {
					t.Fatal("ineligible order retained an unattempted venue link")
				}
				if window == "pre-check" && activity == "client-fill" &&
					f.detail(order).Order.Status != domain.OrderStatusPartiallyFilled {
					t.Fatal("client partial fill was rewritten by the claim")
				}
			})
		}
	}
}

type datedLinkStore struct {
	trading.Store
	createdAt time.Time
}

func (s datedLinkStore) FindVenueOrderByClientID(ctx context.Context, connection domain.ExternalID, coid string) (domain.VenueOrder, bool, error) {
	link, found, err := s.Store.FindVenueOrderByClientID(ctx, connection, coid)
	link.CreatedAt = s.createdAt
	return link, found, err
}

func TestMissingVenueOrderGrace(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
		want domain.OrderStatus
	}{
		{"within-grace", time.Minute, domain.OrderStatusCommitted},
		{"after-grace", 3 * time.Minute, domain.OrderStatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.venue.sendErr = errors.New("venue request timed out")
			st := datedLinkStore{Store: f.st, createdAt: time.Now().Add(-tc.age)}
			r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink,
				slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			must(t, err)
			must(t, r.Start(testCtx))
			f.runtime = r
			t.Cleanup(r.Stop)
			order := f.submit("10")
			link, err := r.Send(testCtx, order.ExternalID, f.dest)
			if !errors.Is(err, domain.ErrUpstream) {
				t.Fatalf("unknown send = %v", err)
			}
			f.orderEvent(link)
			f.flush()
			if status := f.detail(order).Order.Status; status != tc.want {
				t.Fatalf("missing venue order at age %s: status=%s, want %s", tc.age, status, tc.want)
			}
			if tc.want == domain.OrderStatusCancelled {
				f.assertReleased(order)
			} else {
				if !strings.Contains(f.logs.text(), "within send grace") {
					t.Fatal("missing venue order did not remain pending during grace")
				}
				f.venue.setSnapshot(link, trading.VenueStatusFilled, "10", makeFill("10", "10", "0", true))
				f.orderEvent(link)
				f.flush()
				if f.detail(order).Order.Status != domain.OrderStatusFilled {
					t.Fatal("delayed venue request was not recovered during grace")
				}
			}
		})
	}
}

func TestInvalidVenueDecimals(t *testing.T) {
	for _, source := range []string{"event", "snapshot"} {
		for _, field := range []string{"quantity", "price", "cumulative", "leaves", "filled"} {
			if source == "event" && field == "filled" {
				continue
			}
			for _, tc := range []struct{ name, value string }{
				{"exponent", "1e2147483647"},
				{"overlong", strings.Repeat("0", 40) + "3"},
				{"negative", "-3"},
				{"space", " 3"},
				{"trailing-space", "3 "},
				{"empty", ""},
				{"zero-price", "0"},
			} {
				if tc.name == "zero-price" && field != "price" {
					continue
				}
				t.Run(source+"/"+field+"/"+tc.name, func(t *testing.T) {
					f := newFixture(t)
					f.start()
					order := f.submit("10")
					link := f.send(order)
					fill := makeFill("3", "3", "7", false)
					filled := "3"
					switch field {
					case "quantity":
						fill.Quantity = tc.value
					case "price":
						fill.Price = tc.value
					case "cumulative":
						fill.CumQuantity = tc.value
					case "leaves":
						fill.LeavesQuantity = tc.value
					case "filled":
						filled = tc.value
					}
					if source == "event" {
						f.fill(link, fill)
					} else {
						f.venue.setSnapshot(link, trading.VenueStatusOpen, filled, fill)
						f.orderEvent(link)
					}
					f.flush()
					detail := f.detail(order)
					if len(detail.Trades) != 0 || detail.Order.Status != domain.OrderStatusCommitted {
						t.Fatalf("invalid venue decimal was applied: status=%s trades=%d", detail.Order.Status, len(detail.Trades))
					}
					logs := f.logs.text()
					if !strings.Contains(logs, "venue order remains pending") ||
						!strings.Contains(logs, domain.ErrInvalid.Error()) {
						t.Fatalf("invalid venue decimal did not produce an explicit pending error: %s", logs)
					}
					f.venue.mu.Lock()
					reports := len(f.venue.reports)
					f.venue.mu.Unlock()
					if reports != 1 {
						t.Fatalf("invalid venue decimal reached report sink: %d reports, want only claim", reports)
					}
				})
			}
		}
	}
}

func TestVenueCommissionValidation(t *testing.T) {
	for _, source := range []string{"event", "snapshot"} {
		for _, tc := range []struct {
			name     string
			amount   string
			currency string
			valid    bool
		}{
			{"fee", "0.5", "USD", true},
			{"rebate", "-0.5", "USD", true},
			{"zero", "0", "USD", true},
			{"signed-boundary", "-" + strings.Repeat("0", 36) + "0.1", "USD", true},
			{"exponent", "1e0", "USD", false},
			{"huge-exponent", "1e2147483647", "USD", false},
			{"negative-exponent", "-1e0", "USD", false},
			{"overlong", strings.Repeat("0", 40) + "1", "USD", false},
			{"signed-overlong", "-" + strings.Repeat("0", 39) + "1", "USD", false},
			{"double-minus", "--1", "USD", false},
			{"plus", "+1", "USD", false},
			{"space", " 1", "USD", false},
			{"trailing-space", "1 ", "USD", false},
			{"empty", "", "USD", false},
			{"minus-only", "-", "USD", false},
			{"missing-integer", "-.1", "USD", false},
			{"currency", "0.5", "", false},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				f := newFixture(t)
				f.start()
				order := f.submit("10")
				link := f.send(order)
				fill := makeFill("3", "3", "7", false)
				fill.Commission = &domain.Commission{Amount: tc.amount, Currency: tc.currency}
				fills := []trading.Fill{fill}
				if source == "event" {
					f.fill(link, fill)
				} else {
					fill.CumQuantity, fill.LeavesQuantity = "5", "5"
					fills = []trading.Fill{makeFill("2", "2", "8", false), fill}
					f.venue.setSnapshot(link, trading.VenueStatusOpen, "5", fills...)
					f.orderEvent(link)
				}
				f.flush()
				detail := f.detail(order)
				if tc.valid {
					assertTrades(t, detail, fills)
					commission := detail.Trades[len(detail.Trades)-1].Commission
					if commission == nil || commission.Currency != tc.currency {
						t.Fatalf("valid venue commission was lost: %+v", commission)
					}
					assertDecimal(t, commission.Amount, tc.amount)
					return
				}
				if len(detail.Trades) != 0 || detail.Order.Status != domain.OrderStatusCommitted {
					t.Fatalf("invalid venue commission was applied: status=%s trades=%d", detail.Order.Status, len(detail.Trades))
				}
				logs := f.logs.text()
				if !strings.Contains(logs, "venue order remains pending") ||
					!strings.Contains(logs, "commission") || !strings.Contains(logs, domain.ErrInvalid.Error()) {
					t.Fatalf("invalid venue commission did not produce an explicit pending error: %s", logs)
				}
				f.venue.mu.Lock()
				reports := len(f.venue.reports)
				f.venue.mu.Unlock()
				if reports != 1 {
					t.Fatalf("invalid venue commission reached report sink: %d reports, want only claim", reports)
				}
			})
		}
	}
}

func TestInvalidCommissionPendingRetry(t *testing.T) {
	for _, source := range []string{"event", "snapshot"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			applied := make(chan struct{}, 1)
			sink := func(ctx context.Context, in domain.ExecutionReportInput) error {
				if err := f.sink(ctx, in); err != nil {
					return err
				}
				if strings.Contains(string(in.ExternalID), ":fill:") {
					applied <- struct{}{}
				}
				return nil
			}
			r, err := trading.NewRuntime(f.registry, f.st, sink, f.adjustmentSink,
				slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			must(t, err)
			must(t, r.Start(testCtx))
			f.runtime = r
			t.Cleanup(r.Stop)
			order := f.submit("10")
			link := f.send(order)
			fill := makeFill("3", "3", "7", false)
			fill.Commission = &domain.Commission{Amount: "1e0", Currency: "USD"}
			if source == "event" {
				f.fill(link, fill)
			} else {
				f.venue.setSnapshot(link, trading.VenueStatusOpen, "3", fill)
				f.orderEvent(link)
			}
			f.flush()
			if len(f.detail(order).Trades) != 0 {
				t.Fatal("invalid commission reached the core before retry")
			}
			fill.Commission = &domain.Commission{Amount: "-0.5", Currency: "USD"}
			f.venue.setSnapshot(link, trading.VenueStatusOpen, "3", fill)
			select {
			case <-applied:
			case <-time.After(20 * time.Second):
				t.Fatal("invalid commission order was not pending for automatic reconciliation")
			}
			detail := f.detail(order)
			assertTrades(t, detail, []trading.Fill{fill})
			if detail.Order.Status != domain.OrderStatusPartiallyFilled {
				t.Fatalf("valid retry status = %s, want partially_filled", detail.Order.Status)
			}
		})
	}
}

func TestPreCheckUsesCallerContext(t *testing.T) {
	for _, cancelRuntime := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller-cancel", true: "runtime-stop"}[cancelRuntime], func(t *testing.T) {
			f := newFixture(t)
			f.start()
			entered, abandoned := make(chan struct{}), make(chan struct{})
			holdCtx, release := context.WithCancel(testCtx)
			defer release()
			type contextKey struct{}
			f.venue.onPreCheck = func(ctx context.Context, _ trading.Order) error {
				if ctx.Value(contextKey{}) != "caller" {
					return errors.New("pre-check lost caller context value")
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
					return errors.New("pre-check lacks connector timeout")
				}
				close(entered)
				<-ctx.Done()
				close(abandoned)
				if cancelRuntime {
					<-holdCtx.Done()
				}
				return ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.WithValue(testCtx, contextKey{}, "caller"))
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- f.runtime.PreCheck(ctx, f.draft("1"), f.dest) }()
			select {
			case <-entered:
			case err := <-result:
				t.Fatalf("pre-check failed before cancellation: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("pre-check did not reach connector")
			}
			stopDone := make(chan struct{})
			if cancelRuntime {
				go func() {
					f.runtime.Stop()
					close(stopDone)
				}()
			} else {
				cancel()
			}
			select {
			case <-abandoned:
			case <-time.After(2 * time.Second):
				t.Fatal("pre-check connector did not observe cancellation")
			}
			if cancelRuntime {
				select {
				case <-stopDone:
					t.Fatal("Stop returned before the in-flight pre-check finished")
				default:
				}
				release()
				select {
				case <-stopDone:
				case <-time.After(2 * time.Second):
					t.Fatal("Stop did not return promptly after pre-check abandonment")
				}
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("cancelled pre-check succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled pre-check did not return promptly")
			}
		})
	}
}

type preCheckReadStore struct {
	trading.Store
	readContext chan context.Context
}

func (s preCheckReadStore) ListTradingAccess(ctx context.Context, _ domain.AccountID) ([]domain.TradingAccess, error) {
	s.readContext <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPreCheckStoreReadsUseCallerContext(t *testing.T) {
	f := newFixture(t)
	st := preCheckReadStore{Store: f.st, readContext: make(chan context.Context, 1)}
	r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink,
		slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	must(t, err)
	must(t, r.Start(testCtx))
	t.Cleanup(r.Stop)
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(testCtx, contextKey{}, "caller"))
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- r.PreCheck(ctx, f.draft("1"), f.dest) }()
	select {
	case readCtx := <-st.readContext:
		if readCtx.Value(contextKey{}) != "caller" {
			t.Fatal("pre-check store read lost caller context value")
		}
		if deadline, ok := readCtx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("pre-check store read lacks connector timeout")
		}
		cancel()
		select {
		case <-readCtx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("pre-check store read did not observe caller cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pre-check did not reach store read")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pre-check = %v", err)
	}
}
