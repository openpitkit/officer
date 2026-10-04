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
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

type failingStore struct {
	trading.Store
	markErr error
	listErr error
}

type postClaimStore struct {
	trading.Store
	readFail   atomic.Bool
	deleteFail atomic.Bool
	readErr    error
	deleteErr  error
	deleted    chan struct{}
}

func (s *postClaimStore) GetOrder(ctx context.Context, id domain.ExternalID) (domain.OrderDetail, error) {
	if s.readFail.Load() {
		return domain.OrderDetail{}, s.readErr
	}
	return s.Store.GetOrder(ctx, id)
}

func (s *postClaimStore) DeleteVenueOrder(ctx context.Context, id domain.ExternalID) error {
	if s.deleteFail.Load() {
		return s.deleteErr
	}
	if err := s.Store.DeleteVenueOrder(ctx, id); err != nil {
		return err
	}
	s.deleted <- struct{}{}
	return nil
}

func TestPostClaimRecovery(t *testing.T) {
	for _, recovery := range []string{"retry", "restart"} {
		for _, activity := range []string{"read-failure", "confirm", "client-report", "client-fill"} {
			t.Run(recovery+"/"+activity, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t)
				order := f.submit("10")
				st := &postClaimStore{Store: f.st,
					readErr:   errors.New("post-claim order read failed"),
					deleteErr: errors.New("post-claim link delete failed"),
					deleted:   make(chan struct{}, 1)}
				cancelled := make(chan struct{}, 1)
				st.deleteFail.Store(activity != "read-failure")
				sink := func(ctx context.Context, in domain.ExecutionReportInput) error {
					claim := strings.HasSuffix(string(in.ExternalID), ":claim")
					if claim && activity == "confirm" {
						if _, err := f.n.ConfirmOrder(ctx, order.ExternalID, testCaller); err != nil {
							return err
						}
					} else if claim && (activity == "client-report" || activity == "client-fill") {
						report := domain.ExecutionReportInput{ExternalID: "client-report",
							Order: order.ExternalID, OrderStatus: domain.OrderStatusCommitted}
						if activity == "client-fill" {
							report.OrderStatus = domain.OrderStatusPartiallyFilled
							report.FillQuantity, report.FillPrice, report.LeavesQuantity = "3", "2", "7"
						}
						if err := f.sink(ctx, report); err != nil {
							return err
						}
					}
					if err := f.sink(ctx, in); err != nil {
						return err
					}
					if claim && activity == "read-failure" {
						st.readFail.Store(true)
					}
					if strings.HasSuffix(string(in.ExternalID), ":done") {
						cancelled <- struct{}{}
					}
					return nil
				}
				r, err := trading.NewRuntime(f.registry, st, sink, f.adjustmentSink,
					slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
				must(t, err)
				must(t, r.Start(testCtx))
				f.runtime = r
				t.Cleanup(r.Stop)
				link, err := r.Send(testCtx, order.ExternalID, f.dest)
				wantErr := domain.ErrConflict
				if activity == "read-failure" {
					wantErr = st.readErr
				}
				if !errors.Is(err, wantErr) || link.SendAttempted {
					t.Fatalf("post-claim failure: link=%+v error=%v, want %v", link, err, wantErr)
				}
				stored, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
				must(t, err)
				if !found || stored.SendAttempted || stored.VenueOrderID != "" {
					t.Fatalf("post-claim failure did not retain unattempted link: found=%t link=%+v", found, stored)
				}
				claimID := domain.ExternalID("trading:" + link.ClientOrderID + ":claim")
				claimed, err := f.st.ExecutionReportExists(testCtx, claimID)
				must(t, err)
				if !claimed {
					t.Fatal("recovery scenario did not persist the claim")
				}
				st.readFail.Store(false)
				st.deleteFail.Store(false)
				if recovery == "restart" {
					r.Stop()
					f.start()
					f.venue.push(trading.Event{Kind: trading.EventConnected})
					f.flush()
				} else {
					resolved := st.deleted
					if activity == "read-failure" {
						resolved = cancelled
					}
					select {
					case <-resolved:
					case <-time.After(20 * time.Second):
						t.Fatal("post-claim order was not pending for automatic reconciliation")
					}
				}
				detail := f.detail(order)
				doneID := domain.ExternalID("trading:" + link.ClientOrderID + ":done")
				done, err := f.st.ExecutionReportExists(testCtx, doneID)
				must(t, err)
				if activity == "read-failure" {
					if detail.Order.Status != domain.OrderStatusCancelled || !done {
						t.Fatalf("Officer-owned claim was not cancelled: status=%s done=%t", detail.Order.Status, done)
					}
					f.assertReleased(order)
				} else {
					_, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
					must(t, err)
					if found || detail.Order.Status == domain.OrderStatusCancelled || done {
						t.Fatalf("conflicting claim was cancelled or retained: found=%t status=%s done=%t", found, detail.Order.Status, done)
					}
					if activity == "client-fill" {
						assertTrades(t, detail, []trading.Fill{makeFill("3", "3", "7", false)})
					}
				}
				if sends, lookups := f.venue.counts(); sends != 0 || lookups != 0 {
					t.Fatalf("unattempted claim contacted venue: sends=%d lookups=%d", sends, lookups)
				}
			})
		}
	}
}

func TestClaimReportFailure(t *testing.T) {
	for _, failure := range []string{"sink-error", "cancel-shortcut"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			order := f.submit("10")
			claimErr := errors.New("claim report sink failed")
			wantStatus := domain.OrderStatusCommitted
			if failure == "cancel-shortcut" {
				claimErr = domain.ErrTerminalOrder
				wantStatus = domain.OrderStatusCancelled
			}
			claimAttempted := false
			sink := func(ctx context.Context, in domain.ExecutionReportInput) error {
				if strings.HasSuffix(string(in.ExternalID), ":claim") {
					claimAttempted = true
					if failure == "sink-error" {
						return claimErr
					}
					if _, _, err := f.n.CancelOrder(ctx, order.ExternalID, "0", testCaller); err != nil {
						return err
					}
				}
				return f.sink(ctx, in)
			}
			r, err := trading.NewRuntime(f.registry, f.st, sink, f.adjustmentSink,
				slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			must(t, err)
			must(t, r.Start(testCtx))
			t.Cleanup(r.Stop)
			link, err := r.Send(testCtx, order.ExternalID, f.dest)
			if !claimAttempted || !errors.Is(err, claimErr) {
				t.Fatalf("claim failure not returned: attempted=%t error=%v, want %v", claimAttempted, err, claimErr)
			}
			_, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
			must(t, err)
			if found {
				t.Fatal("failed claim retained a venue link")
			}
			claimed, err := f.st.ExecutionReportExists(testCtx,
				domain.ExternalID("trading:"+link.ClientOrderID+":claim"))
			must(t, err)
			if claimed || f.detail(order).Order.Status != wantStatus {
				t.Fatalf("failed claim changed order: claimed=%t status=%s", claimed, f.detail(order).Order.Status)
			}
			if sends, lookups := f.venue.counts(); sends != 0 || lookups != 0 || link.SendAttempted {
				t.Fatalf("failed claim contacted venue: sends=%d lookups=%d link=%+v", sends, lookups, link)
			}
		})
	}
}

func (s failingStore) MarkVenueOrderSendAttempted(ctx context.Context, id domain.ExternalID) error {
	if s.markErr != nil {
		return s.markErr
	}
	return s.Store.MarkVenueOrderSendAttempted(ctx, id)
}
func (s failingStore) ListTradingConnections(ctx context.Context) ([]domain.TradingConnection, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.Store.ListTradingConnections(ctx)
}

func TestMarkAttemptFailureIsRecoveredWithoutVenueCall(t *testing.T) {
	f := newFixture(t)
	errMark := errors.New("send marker storage failed")
	r, err := trading.NewRuntime(f.registry, failingStore{Store: f.st, markErr: errMark}, f.sink, f.adjustmentSink,
		slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	must(t, err)
	must(t, r.Start(testCtx))
	f.runtime = r
	t.Cleanup(r.Stop)
	order := f.submit("10")
	link, err := r.Send(testCtx, order.ExternalID, f.dest)
	if !errors.Is(err, errMark) || link.SendAttempted {
		t.Fatalf("mark failure link=%+v error=%v", link, err)
	}
	f.orderEvent(link)
	f.flush()
	if f.detail(order).Order.Status != domain.OrderStatusCancelled {
		t.Fatal("claimed unattempted order not cancelled")
	}
	sends, lookups := f.venue.counts()
	if sends != 0 || lookups != 0 {
		t.Fatalf("marker failure contacted venue: sends=%d lookups=%d", sends, lookups)
	}
	f.assertReleased(order)
}

func TestStartStoreFailure(t *testing.T) {
	f := newFixture(t)
	failed := errors.New("connection store unavailable")
	r, err := trading.NewRuntime(f.registry, failingStore{Store: f.st, listErr: failed}, f.sink, f.adjustmentSink, slog.Default())
	must(t, err)
	t.Cleanup(r.Stop)
	if err := r.Start(testCtx); !errors.Is(err, failed) {
		t.Fatalf("Start store error=%v", err)
	}
	sends, lookups := f.venue.counts()
	if sends != 0 || lookups != 0 {
		t.Fatal("failed Start contacted venue")
	}
}

func TestPendingRetryRetainsUnknownSendAfterPartialEvent(t *testing.T) {
	f := newFixture(t)
	f.venue.sendErr = errors.New("send timed out")
	f.start()
	order := f.submit("10")
	link, err := f.runtime.Send(testCtx, order.ExternalID, f.dest)
	if !errors.Is(err, domain.ErrUpstream) {
		t.Fatalf("unknown send error=%v", err)
	}
	a, b := makeFill("2", "2", "8", false), makeFill("3", "5", "5", false)
	f.fill(link, a)
	f.flush()
	if len(f.detail(order).Trades) != 1 {
		t.Fatal("partial stream fill was not applied")
	}
	// No further event: the pending unknown send must still be reconciled.
	f.venue.setSnapshot(link, trading.VenueStatusCancelled, "5", a, b)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && f.detail(order).Order.Status != domain.OrderStatusCancelled {
		time.Sleep(25 * time.Millisecond)
	}
	detail := f.detail(order)
	if detail.Order.Status != domain.OrderStatusCancelled {
		t.Fatalf("retry status=%s", detail.Order.Status)
	}
	assertTrades(t, detail, []trading.Fill{a, b})
	stored, _, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if stored.VenueOrderID == "" {
		t.Fatal("retry did not record venue acknowledgement")
	}
	f.assertReleased(order)
}

func TestStopInFlightLeavesRecoverableLink(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("10")
	entered := make(chan struct{})
	f.venue.onSend = func(ctx context.Context, _ trading.Order) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	result := make(chan struct{})
	go func() { _, _ = f.runtime.Send(testCtx, order.ExternalID, f.dest); close(result) }()
	<-entered
	f.runtime.Stop()
	<-result
	link, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if !found || !link.SendAttempted || link.VenueOrderID != "" {
		t.Fatalf("stopped in-flight link=%+v", link)
	}
	f.venue.setSnapshot(link, trading.VenueStatusFilled, "10", makeFill("10", "10", "0", true))
	f.start()
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	if f.detail(order).Order.Status != domain.OrderStatusFilled {
		t.Fatal("restart did not recover interrupted send")
	}
	sends, _ := f.venue.counts()
	if sends != 1 {
		t.Fatalf("interrupted send resent: %d", sends)
	}
}

func TestAcknowledgedMissingOrderAndFilledWithoutFinalStayOpen(t *testing.T) {
	for _, kind := range []string{"missing", "nonfinal", "replaced", "mismatched-cumulative", "negative-leaves", "final-before-last"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.start()
			order := f.submit("10")
			link := f.send(order)
			fill := makeFill("3", "3", "7", false)
			switch kind {
			case "missing":
				f.venue.mu.Lock()
				delete(f.venue.snapshots, link.ClientOrderID)
				f.venue.mu.Unlock()
			case "nonfinal":
				f.venue.setSnapshot(link, trading.VenueStatusFilled, "3", fill)
			case "replaced":
				f.venue.setSnapshot(link, trading.VenueStatusReplaced, "3", fill)
			case "mismatched-cumulative":
				fill.CumQuantity = "4"
				f.venue.setSnapshot(link, trading.VenueStatusCancelled, "4", fill)
			case "negative-leaves":
				fill.LeavesQuantity = "-1"
				f.venue.setSnapshot(link, trading.VenueStatusCancelled, "3", fill)
			case "final-before-last":
				fill.Final = true
				f.venue.setSnapshot(link, trading.VenueStatusFilled, "10", fill, makeFill("7", "10", "0", true))
			}
			f.orderEvent(link)
			f.flush()
			detail := f.detail(order)
			if domain.OrderStatusTerminal(detail.Order.Status) {
				t.Fatalf("anomaly %s terminalized order", kind)
			}
			want := 0
			if kind == "nonfinal" || kind == "replaced" {
				want = 1
			}
			if len(detail.Trades) != want {
				t.Fatalf("anomaly %s trades=%d want=%d", kind, len(detail.Trades), want)
			}
			if kind == "replaced" && !strings.Contains(f.logs.text(), "operator attention") {
				t.Fatal("replacement not logged")
			}
		})
	}
}
