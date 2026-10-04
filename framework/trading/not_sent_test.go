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
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

type notSentStore struct {
	trading.Store
	cause                                 error
	connectionFailure, orderFailure       int
	connectionReads, orderReads           int
	ineligible, createFailure, detachRead bool
}

func (s *notSentStore) GetTradingConnection(ctx context.Context, id domain.ExternalID) (domain.TradingConnection, bool, error) {
	s.connectionReads++
	if s.connectionReads == s.connectionFailure {
		return domain.TradingConnection{}, false, s.cause
	}
	if s.detachRead {
		ctx = context.WithoutCancel(ctx)
	}
	return s.Store.GetTradingConnection(ctx, id)
}

func (s *notSentStore) GetOrder(ctx context.Context, id domain.ExternalID) (domain.OrderDetail, error) {
	s.orderReads++
	if s.orderReads == s.orderFailure {
		return domain.OrderDetail{}, s.cause
	}
	detail, err := s.Store.GetOrder(ctx, id)
	if s.ineligible && s.orderReads == 2 {
		detail.Order.Status = domain.OrderStatusCancelled
	}
	return detail, err
}

func (s *notSentStore) CreateVenueOrder(ctx context.Context, link domain.VenueOrder) (domain.VenueOrder, error) {
	if s.createFailure {
		return domain.VenueOrder{}, s.cause
	}
	return s.Store.CreateVenueOrder(ctx, link)
}

func TestSendNotSentBeforeLink(t *testing.T) {
	for _, stage := range []string{
		"destination", "connection_read", "missing_connection", "not_running",
		"disabled_connection", "unknown_provider", "worker_missing", "worker_unavailable",
		"order_read_before_check", "destination_read", "order_read_after_check",
		"ineligible", "create_link", "recheck_rejected", "recheck_unsupported",
	} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			order := f.submit("1")
			cause := errors.New("store failure")
			st := &notSentStore{Store: f.st, cause: cause}
			r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink, f.feeSkipSink,
				slog.New(slog.NewJSONHandler(f.logs, nil)))
			must(t, err)
			t.Cleanup(r.Stop)
			dest := f.dest
			want := cause
			switch stage {
			case "destination":
				dest.Route, want = "[]", domain.ErrInvalid
			case "connection_read":
				st.connectionFailure = 1
			case "missing_connection":
				dest.Connection, want = "missing", domain.ErrNotFound
			case "not_running":
				want = domain.ErrConflict
			case "disabled_connection":
				must(t, f.st.SetTradingConnectionEnabled(testCtx, dest.Connection, false))
				want = domain.ErrConflict
			case "unknown_provider":
				c := f.addConnection("unknown", "unknown", true)
				dest.Connection, want = c.ExternalID, domain.ErrInvalid
			case "worker_unavailable":
				f.venue.subscribeErr, want = cause, domain.ErrUpstream
			case "order_read_before_check":
				st.orderFailure = 1
			case "destination_read":
				st.connectionFailure = 2
			case "order_read_after_check":
				st.orderFailure = 2
			case "ineligible":
				st.ineligible, want = true, domain.ErrConflict
			case "create_link":
				st.createFailure = true
			case "recheck_rejected":
				f.venue.preErr, want = trading.ErrRejected, trading.ErrRejected
			case "recheck_unsupported":
				f.venue.preErr, want = trading.ErrUnsupported, trading.ErrUnsupported
			}
			if stage != "not_running" {
				must(t, r.Start(testCtx))
			}
			if stage == "worker_missing" {
				c := f.addConnection("fake", "late", true)
				dest.Connection, want = c.ExternalID, domain.ErrUpstream
			}
			_, err = r.Send(testCtx, order.ExternalID, dest)
			if !errors.Is(err, trading.ErrNotSent) || !errors.Is(err, want) {
				t.Fatalf("send=%v want ErrNotSent and %v", err, want)
			}
			_, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
			must(t, err)
			sends, _ := f.venue.counts()
			if found || sends != 0 {
				t.Fatalf("not sent linked=%t sends=%d", found, sends)
			}
			st.connectionReads, st.orderReads = 0, 0
			if err := r.PreCheck(testCtx, f.draft("1"), dest); errors.Is(err, trading.ErrNotSent) {
				t.Fatalf("PreCheck got Send-only sentinel: %v", err)
			}
		})
	}
}

func TestSendNotSentAfterClaimCleanup(t *testing.T) {
	for _, stage := range []string{"claim_report", "post_claim_eligibility"} {
		for _, outcome := range []string{"deleted", "delete_failed"} {
			t.Run(stage+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t)
				order := f.submit("10")
				deleteFails := outcome == "delete_failed"
				st := &postClaimStore{
					Store: f.st, deleteErr: errors.New("claim link delete failed"),
					deleted: make(chan struct{}, 1),
				}
				st.deleteFail.Store(deleteFails)
				claimErr := errors.New("claim report failed")
				wantErr := error(domain.ErrConflict)
				if stage == "claim_report" {
					wantErr = claimErr
				}
				sink := func(ctx context.Context, in domain.ExecutionReportInput) error {
					claim := strings.HasSuffix(string(in.ExternalID), ":claim")
					if claim && stage == "claim_report" {
						return claimErr
					}
					if err := f.sink(ctx, in); err != nil {
						return err
					}
					if claim {
						return f.sink(ctx, domain.ExecutionReportInput{
							ExternalID: "client-report", Order: order.ExternalID,
							OrderStatus: domain.OrderStatusCommitted,
						})
					}
					return nil
				}
				r, err := trading.NewRuntime(f.registry, st, sink, f.adjustmentSink, f.feeSkipSink,
					slog.New(slog.NewJSONHandler(f.logs, nil)))
				must(t, err)
				must(t, r.Start(testCtx))
				t.Cleanup(r.Stop)
				link, err := r.Send(testCtx, order.ExternalID, f.dest)
				if !errors.Is(err, wantErr) || errors.Is(err, trading.ErrNotSent) == deleteFails {
					t.Fatalf("cleanup error=%v want cause=%v notSent=%t", err, wantErr, !deleteFails)
				}
				stored, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
				must(t, err)
				if found != deleteFails || link.ClientOrderID == "" || link.SendAttempted || stored.SendAttempted {
					t.Fatalf("cleanup link: found=%t returned=%+v stored=%+v", found, link, stored)
				}
				claimed, err := f.st.ExecutionReportExists(testCtx,
					domain.ExternalID("trading:"+link.ClientOrderID+":claim"))
				must(t, err)
				if claimed != (stage == "post_claim_eligibility") {
					t.Fatalf("wrong cleanup branch: claimed=%t stage=%s", claimed, stage)
				}
				if sends, lookups := f.venue.counts(); sends != 0 || lookups != 0 {
					t.Fatalf("cleanup contacted venue: sends=%d lookups=%d", sends, lookups)
				}
				if deleteFails {
					st.deleteFail.Store(false)
					select {
					case <-st.deleted:
					case <-time.After(20 * time.Second):
						t.Fatal("failed delete did not leave the link pending for reconciliation")
					}
					_, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
					must(t, err)
					if found {
						t.Fatal("pending cleanup retained the venue link")
					}
				}
			})
		}
	}
}

type handoffContext struct {
	context.Context
	onDone func()
	once   sync.Once
}

func (c *handoffContext) Done() <-chan struct{} {
	c.once.Do(c.onDone)
	return c.Context.Done()
}

func TestSendNotSentBeforeHandoff(t *testing.T) {
	for _, runtimeStopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "runtime"}[runtimeStopped], func(t *testing.T) {
			f := newFixture(t)
			order := f.submit("1")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer close(release)
			f.venue.onPreCheck = func(context.Context, trading.Order) error {
				once.Do(func() { close(entered) })
				<-release
				return nil
			}
			st := &notSentStore{Store: f.st, detachRead: true}
			r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink, f.feeSkipSink,
				slog.New(slog.NewJSONHandler(f.logs, nil)))
			must(t, err)
			t.Cleanup(r.Stop)
			base, stop := context.WithCancel(testCtx)
			defer stop()
			must(t, r.Start(base))
			checked := make(chan struct{})
			go func() { _ = r.PreCheck(testCtx, f.draft("1"), f.dest); close(checked) }()
			<-entered
			caller, cancel := context.WithCancel(testCtx)
			defer cancel()
			ctx := &handoffContext{Context: caller, onDone: cancel}
			want := error(context.Canceled)
			if runtimeStopped {
				ctx.onDone, want = stop, domain.ErrConflict
			}
			_, err = r.Send(ctx, order.ExternalID, f.dest)
			if !errors.Is(err, trading.ErrNotSent) || !errors.Is(err, want) {
				t.Fatalf("pre-handoff send=%v want ErrNotSent and %v", err, want)
			}
			sends, _ := f.venue.counts()
			_, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
			must(t, err)
			if found || sends != 0 {
				t.Fatalf("pre-handoff linked=%t sends=%d", found, sends)
			}
		})
	}
}

func TestSendStoppedAfterHandoff(t *testing.T) {
	f := newFixture(t)
	order := f.submit("1")
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	f.venue.onSend = func(context.Context, trading.Order) error {
		close(entered)
		<-release
		return nil
	}
	f.newRuntime()
	base, stop := context.WithCancel(testCtx)
	defer stop()
	must(t, f.runtime.Start(base))
	result := make(chan error, 1)
	go func() { _, err := f.runtime.Send(testCtx, order.ExternalID, f.dest); result <- err }()
	<-entered
	stop()
	if err := <-result; !errors.Is(err, domain.ErrConflict) || errors.Is(err, trading.ErrNotSent) {
		t.Fatalf("post-handoff runtime stop=%v", err)
	}
	link, found, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if !found || !link.SendAttempted {
		t.Fatalf("accepted send link=%+v found=%t", link, found)
	}
}
