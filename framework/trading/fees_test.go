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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

type feeVenue struct {
	*fakeVenue
	fees     []trading.Fee
	since    []time.Time
	after    []string
	pageSize int
	err      error
	onFees   func(context.Context) error
	lookups  []int
}

type feeConnector struct {
	*fakeConnector
	fees *feeVenue
}

func (v *feeVenue) cursors() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.after...)
}

func (c *feeConnector) Fees(ctx context.Context, since time.Time, after string) ([]trading.Fee, string, error) {
	c.fees.mu.Lock()
	c.fees.since = append(c.fees.since, since)
	c.fees.after = append(c.fees.after, after)
	c.fees.lookups = append(c.fees.lookups, c.fees.fakeVenue.lookups)
	err, hook := c.fees.err, c.fees.onFees
	var fees []trading.Fee
	started := after == ""
	for _, fee := range c.fees.fees {
		if !started {
			started = fee.ID == after
			continue
		}
		if fee.At.After(since) {
			fees = append(fees, fee)
		}
	}
	next := ""
	if c.fees.pageSize > 0 && len(fees) > c.fees.pageSize {
		fees = fees[:c.fees.pageSize]
		next = fees[len(fees)-1].ID
	}
	c.fees.mu.Unlock()
	if hook != nil {
		err = hook(ctx)
	}
	if err != nil {
		return nil, "", err
	}
	return fees, next, err
}

func (c *feeConnector) InstrumentAssets(symbol string) (string, string, error) {
	if symbol == "bad-symbol" {
		return "", "", errors.New("malformed venue symbol")
	}
	if base, quote, found := strings.Cut(symbol, "/"); found {
		return base, quote, nil
	}
	return symbol, "USD", nil
}

func newFeeFixture(t *testing.T) (*fixture, *feeVenue) {
	t.Helper()
	f := newFixture(t)
	v := &feeVenue{fakeVenue: f.venue}
	f.registry = trading.NewRegistry()
	must(t, f.registry.Register(trading.Provider{Type: "fake", Title: "Fee venue",
		Build: func(connection domain.TradingConnection) (trading.Connector, error) {
			c, err := v.build(connection)
			if err != nil {
				return nil, err
			}
			return &feeConnector{fakeConnector: c.(*fakeConnector), fees: v}, nil
		}}))
	return f, v
}

func feeBalance(t *testing.T, f *fixture, asset, want string) {
	t.Helper()
	balance, found, err := f.n.GetBalance(testCtx, "desk", asset)
	must(t, err)
	if !found {
		t.Fatalf("fee balance missing for %s", asset)
	}
	assertDecimal(t, balance.Available, want)
}

func postedFee(id, asset, amount string) trading.Fee {
	return trading.Fee{ID: id, Asset: asset, Amount: amount,
		Status: trading.FeeExecuted, At: time.Now().Add(time.Hour)}
}

func TestFeeIntakeExactAssetsAndReplay(t *testing.T) {
	f, v := newFeeFixture(t)
	_, err := f.n.CreateAsset(testCtx, domain.Asset{Code: "ETHER"}, testCaller)
	must(t, err)
	must(t, f.st.UpsertTradingInstrument(testCtx, domain.TradingInstrument{
		Connection: f.connection.ExternalID, ExternalSymbol: "ETH/USD",
		BaseAsset: "ETHER", QuoteAsset: "USD", Enabled: false,
	}))
	must(t, f.adjustmentSink(testCtx, "desk", "seed-ether", domain.AdjustmentRequest{
		Asset: "ETHER", Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "1"},
	}))
	// Several venue accounts for the same Officer account are still unambiguous.
	must(t, f.st.AddTradingAccess(testCtx, domain.TradingAccess{
		Account: "desk", Connection: f.connection.ExternalID, VenueAccount: "second-grant",
	}))
	v.fees = []trading.Fee{postedFee("USD-fee", "USD", "-0.123456789"), postedFee("crypto-fee", "ETH", "-0.000195"),
		{ID: "pre-Officer", Asset: "USD", Amount: "-100", Status: trading.FeeExecuted, At: time.Now().Add(-time.Hour)}}
	f.start()
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	v.mu.Lock()
	beforeSendCalls := len(v.since)
	v.mu.Unlock()
	if beforeSendCalls != 0 {
		t.Fatal("fees requested before the connection's first venue link")
	}
	order := f.submit("1")
	link := f.send(order)
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	feeBalance(t, f, "USD", "997.876543211")
	feeBalance(t, f, "ETHER", "0.999805")
	v.mu.Lock()
	since, lookups := v.since[0], v.lookups[0]
	v.mu.Unlock()
	if !since.Equal(link.CreatedAt) {
		t.Fatalf("fee since=%v, want first send link time %v", since, link.CreatedAt)
	}
	if lookups == 0 {
		t.Fatal("fee intake preceded reconnect reconciliation")
	}
	for i := 0; i < 3; i++ {
		f.venue.push(trading.Event{Kind: trading.EventConnected})
	}
	f.flush()
	f.runtime.Stop()
	f.start()
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	feeBalance(t, f, "USD", "997.876543211")
	feeBalance(t, f, "ETHER", "0.999805")
	id := domain.ExternalID("trading:" + f.connection.ExternalID.String() + ":fee:USD-fee")
	if err := f.adjustmentSink(testCtx, "desk", id, domain.AdjustmentRequest{
		Asset: "USD", Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "-0.123456789"},
	}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("fee adjustment id was not deterministic: %v", err)
	}
	if strings.Contains(f.logs.text(), "already exists") {
		t.Fatal("fee replay was logged as an adjustment failure")
	}
	f.fill(link, makeFill("1", "1", "0", true))
	f.flush()
	f.venue.mu.Lock()
	defer f.venue.mu.Unlock()
	for _, report := range f.venue.reports {
		if report.Commission != nil {
			t.Fatal("separate venue fee was attached to an execution report")
		}
	}
}

type feeAccessStore struct {
	trading.Store
}

func (st feeAccessStore) ListTradingAccessForConnection(context.Context, domain.ExternalID) ([]domain.TradingAccess, error) {
	return nil, nil
}

func TestFeeIntakeRefusesAmbiguity(t *testing.T) {
	for _, scenario := range []string{"not applicable", "zero accounts", "two accounts", "unmapped asset", "ambiguous asset", "malformed symbol", "missing id", "unknown status"} {
		t.Run(scenario, func(t *testing.T) {
			f, v := newFeeFixture(t)
			fee := postedFee("attention", "USD", "-1")
			var st trading.Store = f.st
			wantLog := ""
			switch scenario {
			case "not applicable":
				fee.Status, fee.Detail = trading.FeeNotApplicable, "activity status correct"
				wantLog = fee.Detail
			case "zero accounts":
				st = feeAccessStore{Store: f.st}
				wantLog = "connection is used by 0 accounts"
			case "two accounts":
				f.fund("other", "1000")
				must(t, f.st.AddTradingAccess(testCtx, domain.TradingAccess{Account: "other", Connection: f.connection.ExternalID}))
				wantLog = "connection is used by 2 accounts"
			case "unmapped asset":
				fee.Asset, wantLog = "EUR", "maps to 0 Officer assets"
			case "ambiguous asset", "malformed symbol":
				for _, asset := range []string{"MSFT", "DOLLAR"} {
					_, err := f.n.CreateAsset(testCtx, domain.Asset{Code: asset}, testCaller)
					must(t, err)
				}
				symbol := "MSFT"
				wantLog = "maps to 2 Officer assets"
				if scenario == "malformed symbol" {
					symbol, wantLog = "bad-symbol", "malformed venue symbol"
				}
				must(t, f.st.UpsertTradingInstrument(testCtx, domain.TradingInstrument{
					Connection: f.connection.ExternalID, ExternalSymbol: symbol, BaseAsset: "MSFT", QuoteAsset: "DOLLAR",
				}))
			case "missing id":
				fee.ID, wantLog = "", "missing fee id"
			case "unknown status":
				fee.Status, wantLog = "invalid", "invalid fee status"
			}
			v.fees = []trading.Fee{fee}
			var applied atomic.Int32
			sink := func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
				applied.Add(1)
				return f.adjustmentSink(ctx, account, id, req)
			}
			r, err := trading.NewRuntime(f.registry, st, f.sink, sink,
				slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			must(t, err)
			f.runtime = r
			must(t, r.Start(testCtx))
			defer r.Stop()
			f.send(f.submit("1"))
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			if applied.Load() != 0 {
				t.Fatal("inapplicable or ambiguous fee reached the adjustment sink")
			}
			feeBalance(t, f, "USD", "998")
			logs := f.logs.text()
			if !strings.Contains(logs, wantLog) || !strings.Contains(logs, `"level":"ERROR"`) ||
				!strings.Contains(logs, `"fee_id":"`+fee.ID+`"`) || !strings.Contains(logs, f.connection.ExternalID.String()) {
				t.Fatalf("fee attention log lost reason/id/connection/error level: %s", logs)
			}
		})
	}
}

func TestFeeIntakePollRetries(t *testing.T) {
	for _, failure := range []string{"sink", "reader"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, v := newFeeFixture(t)
				v.fees = []trading.Fee{postedFee("retry", "USD", "-1")}
				var attempts int
				sink := func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
					attempts++
					if failure == "sink" && attempts == 1 {
						return errors.New("adjustment unavailable")
					}
					return f.adjustmentSink(ctx, account, id, req)
				}
				if failure == "reader" {
					v.err = errors.New("venue fee read failed")
				}
				r, err := trading.NewRuntime(f.registry, f.st, f.sink, sink,
					slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
				must(t, err)
				f.runtime = r
				must(t, r.Start(testCtx))
				defer r.Stop()
				f.send(f.submit("1"))
				f.venue.push(trading.Event{Kind: trading.EventConnected})
				synctest.Wait()
				feeBalance(t, f, "USD", "998")
				if !strings.Contains(f.logs.text(), map[string]string{"sink": "adjustment unavailable", "reader": "venue fee read failed"}[failure]) {
					t.Fatal("fee failure was not logged")
				}
				v.mu.Lock()
				v.err = nil
				v.mu.Unlock()
				time.Sleep(time.Hour - time.Nanosecond)
				synctest.Wait()
				feeBalance(t, f, "USD", "998")
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				feeBalance(t, f, "USD", "997")
				v.mu.Lock()
				calls := len(v.since)
				v.mu.Unlock()
				if calls != 2 {
					t.Fatalf("fee polls = %d, want exactly reconnect and hourly retry", calls)
				}
				time.Sleep(time.Hour)
				synctest.Wait()
				feeBalance(t, f, "USD", "997")
			})
		})
	}
}

func TestFeeReadIsBoundedAndSerialWithSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, v := newFeeFixture(t)
		var reading atomic.Bool
		v.onFees = func(ctx context.Context) error {
			reading.Store(true)
			defer reading.Store(false)
			<-ctx.Done()
			return ctx.Err()
		}
		v.onSend = func(context.Context, trading.Order) error {
			if reading.Load() {
				t.Error("fee reader and order writer ran concurrently")
			}
			return nil
		}
		f.start()
		defer f.runtime.Stop()
		f.send(f.submit("1"))
		f.venue.push(trading.Event{Kind: trading.EventConnected})
		synctest.Wait()
		if !reading.Load() {
			t.Fatal("fee read did not start")
		}
		started := time.Now()
		f.send(f.submit("1"))
		if elapsed := time.Since(started); elapsed != 30*time.Second {
			t.Fatalf("fee read blocked send for %v, want 30s timeout", elapsed)
		}
	})
}

type feeRetryStore struct {
	trading.Store
	failure                      string
	accessCalls, instrumentCalls int
}

func (s *feeRetryStore) ListTradingAccessForConnection(ctx context.Context, connection domain.ExternalID) ([]domain.TradingAccess, error) {
	s.accessCalls++
	if s.failure == "access store" && s.accessCalls == 2 {
		return nil, errors.New("access store unavailable")
	}
	return s.Store.ListTradingAccessForConnection(ctx, connection)
}

func (s *feeRetryStore) ListTradingInstruments(ctx context.Context, connection domain.ExternalID) ([]domain.TradingInstrument, error) {
	s.instrumentCalls++
	if s.failure == "instrument store" && s.instrumentCalls == 2 {
		return nil, fmt.Errorf("instrument store unavailable: %w", domain.ErrInvalid)
	}
	return s.Store.ListTradingInstruments(ctx, connection)
}

func TestFeeCursorResumesAfterFailure(t *testing.T) {
	for _, failure := range []string{"sink", "access store", "instrument store"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, v := newFeeFixture(t)
				refused := postedFee("refused", "USD", "-100")
				refused.Status, refused.Detail = trading.FeeNotApplicable, "activity status correct"
				v.fees = []trading.Fee{postedFee("first", "USD", "-1"), refused,
					postedFee("retry", "USD", "-1"), postedFee("last", "USD", "-1")}
				v.pageSize = 2
				attempts := make(map[string]int)
				sink := func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
					v.mu.Lock()
					attempts[id.String()]++
					attempt := attempts[id.String()]
					v.mu.Unlock()
					if failure == "sink" && strings.HasSuffix(id.String(), ":retry") && attempt == 1 {
						return errors.New("adjustment unavailable")
					}
					return f.adjustmentSink(ctx, account, id, req)
				}
				r, err := trading.NewRuntime(f.registry, &feeRetryStore{Store: f.st, failure: failure}, f.sink, sink,
					slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
				must(t, err)
				f.runtime = r
				must(t, r.Start(testCtx))
				defer r.Stop()
				f.send(f.submit("1"))
				f.venue.push(trading.Event{Kind: trading.EventConnected})
				synctest.Wait()
				feeBalance(t, f, "USD", "997")
				if after := v.cursors(); len(after) != 2 || after[0] != "" || after[1] != "refused" {
					t.Fatalf("first poll cursors = %q, want [empty refused]", after)
				}
				time.Sleep(time.Hour)
				synctest.Wait()
				feeBalance(t, f, "USD", "995")
				if after := v.cursors(); len(after) != 3 || after[2] != "refused" {
					t.Fatalf("retry cursor = %q, want retry after refused", after)
				}
				time.Sleep(time.Hour)
				synctest.Wait()
				if after := v.cursors(); len(after) != 4 || after[3] != "last" {
					t.Fatalf("finished poll cursors = %q, want last", after)
				}
				if strings.Count(f.logs.text(), "fee refused not applied:") != 1 {
					t.Fatal("refused fee was logged more than once in this process")
				}
				prefix := "trading:" + f.connection.ExternalID.String() + ":fee:"
				v.mu.Lock()
				defer v.mu.Unlock()
				if attempts[prefix+"first"] != 1 || attempts[prefix+"last"] != 1 {
					t.Fatalf("final fees replayed or later fee skipped: %v", attempts)
				}
			})
		})
	}
}

func TestFeeCursorResumesAfterReadBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, v := newFeeFixture(t)
		v.pageSize = 1
		v.fees = []trading.Fee{postedFee("first", "USD", "-1"), postedFee("second", "USD", "-1"), postedFee("third", "USD", "-1")}
		v.onFees = func(ctx context.Context) error {
			if len(v.cursors()) == 3 {
				<-ctx.Done()
				return ctx.Err()
			}
			time.Sleep(10 * time.Second)
			return nil
		}
		f.start()
		defer f.runtime.Stop()
		f.send(f.submit("1"))
		f.venue.push(trading.Event{Kind: trading.EventConnected})
		synctest.Wait()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		feeBalance(t, f, "USD", "996")
		if after := v.cursors(); len(after) != 3 || after[2] != "second" {
			t.Fatalf("bounded poll cursors = %q, want three page reads ending after second", after)
		}
		time.Sleep(time.Hour - 20*time.Second)
		synctest.Wait()
		feeBalance(t, f, "USD", "995")
		if after := v.cursors(); len(after) != 4 || after[3] != "second" {
			t.Fatalf("deadline retry cursors = %q, want resume after second", after)
		}
	})
}

func TestFeeCoreRejectionIsFinal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, v := newFeeFixture(t)
		v.fees = []trading.Fee{postedFee("rejected", "USD", "-1"), postedFee("accepted", "USD", "-1")}
		var rejects atomic.Int32
		sink := func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
			if strings.HasSuffix(id.String(), ":rejected") {
				rejects.Add(1)
				return fmt.Errorf("risk blocked: %w", trading.ErrAdjustmentRejected)
			}
			return f.adjustmentSink(ctx, account, id, req)
		}
		r, err := trading.NewRuntime(f.registry, f.st, f.sink, sink,
			slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		must(t, err)
		f.runtime = r
		must(t, r.Start(testCtx))
		defer r.Stop()
		f.send(f.submit("1"))
		f.venue.push(trading.Event{Kind: trading.EventConnected})
		synctest.Wait()
		feeBalance(t, f, "USD", "997")
		time.Sleep(time.Hour)
		synctest.Wait()
		if rejects.Load() != 1 || strings.Count(f.logs.text(), "fee rejected not applied: rejected by the core: risk blocked") != 1 ||
			!strings.Contains(f.logs.text(), `"level":"ERROR"`) {
			t.Fatalf("core rejection must be logged once and consumed: rejects=%d logs=%s", rejects.Load(), f.logs.text())
		}
	})
}

func TestFeeAdjustmentOutlivesReadDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, v := newFeeFixture(t)
		v.fees = []trading.Fee{postedFee("slow", "USD", "-1"), postedFee("later", "USD", "-1")}
		var calls atomic.Int32
		sink := func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
			call := calls.Add(1)
			if _, bounded := ctx.Deadline(); bounded {
				t.Error("adjustment sink inherited the venue read deadline")
			}
			if call == 1 {
				time.Sleep(31 * time.Second)
			}
			if ctx.Err() != nil {
				t.Errorf("adjustment canceled after engine work: %v", ctx.Err())
			}
			return f.adjustmentSink(ctx, account, id, req)
		}
		r, err := trading.NewRuntime(f.registry, f.st, f.sink, sink,
			slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		must(t, err)
		f.runtime = r
		must(t, r.Start(testCtx))
		defer r.Stop()
		f.send(f.submit("1"))
		f.venue.push(trading.Event{Kind: trading.EventConnected})
		synctest.Wait()
		time.Sleep(31 * time.Second)
		synctest.Wait()
		feeBalance(t, f, "USD", "997")
		if calls.Load() != 1 {
			t.Fatalf("sink calls after read budget expired = %d, want 1", calls.Load())
		}
		time.Sleep(time.Hour - 31*time.Second)
		synctest.Wait()
		feeBalance(t, f, "USD", "996")
		if after := v.cursors(); len(after) != 2 || after[1] != "slow" {
			t.Fatalf("completed slow adjustment was not consumed: %q", after)
		}
	})
}

func TestDisabledFinishedConnectionTakesLateFeesAfterRestart(t *testing.T) {
	f, v := newFeeFixture(t)
	f.start()
	order := f.submit("1")
	link := f.send(order)
	f.fill(link, makeFill("1", "1", "0", true))
	f.flush()
	open, err := f.st.ListOpenVenueOrders(testCtx, f.connection.ExternalID)
	must(t, err)
	if len(open) != 0 {
		t.Fatal("restart setup still has open venue orders")
	}
	must(t, f.st.SetTradingConnectionEnabled(testCtx, f.connection.ExternalID, false))
	f.runtime.Stop()
	v.fees = []trading.Fee{postedFee("late", "USD", "-1")}
	previous := f.venue.current
	f.start()
	f.venue.mu.Lock()
	trackingStarted := f.venue.current != previous
	f.venue.mu.Unlock()
	if !trackingStarted {
		t.Fatal("disabled connection with finished orders has no late-fee worker after restart")
	}
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	feeBalance(t, f, "USD", "997")
	if len(v.since) != 1 || !v.since[0].Equal(link.CreatedAt) || v.after[0] != "" {
		t.Fatal("restart did not replay fees from the first venue link with an empty cursor")
	}
	if err := f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("disabled precheck = %v", err)
	}
	if _, err := f.runtime.Send(testCtx, f.submit("1").ExternalID, f.dest); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("disabled send = %v", err)
	}
	sends, _ := f.venue.counts()
	if sends != 1 {
		t.Fatalf("disabled connection sent a new order: sends=%d", sends)
	}
}

func TestVenueEventRequiresClientOrderID(t *testing.T) {
	f := newFixture(t)
	f.start()
	order := f.submit("1")
	link := f.send(order)
	for _, kind := range []trading.EventKind{trading.EventFill, trading.EventOrder} {
		f.venue.push(trading.Event{Kind: kind, Ref: trading.OrderRef{VenueOrderID: link.VenueOrderID}, Fill: makeFill("1", "1", "0", true)})
		f.venue.push(trading.Event{Kind: kind, Ref: trading.OrderRef{ClientOrderID: "manual-venue-order", VenueOrderID: "manual"}, Fill: makeFill("1", "1", "0", true)})
	}
	f.flush()
	if detail := f.detail(order); detail.Order.Status != domain.OrderStatusCommitted || len(detail.Trades) != 0 {
		t.Fatal("unidentified venue event changed Officer order")
	}
	_, lookups := f.venue.counts()
	if lookups != 0 {
		t.Fatal("empty or unknown client order id triggered reconciliation")
	}
	warnings, unknown := 0, 0
	for _, line := range strings.Split(f.logs.text(), "\n") {
		if strings.Contains(line, "ignore venue event without client order id") {
			warnings++
			if !strings.Contains(line, `"level":"WARN"`) || !strings.Contains(line, link.VenueOrderID) || !strings.Contains(line, f.connection.ExternalID.String()) {
				t.Fatal("missing client order id log lost warn level, venue id or connection")
			}
		}
		if strings.Contains(line, "manual-venue-order") {
			unknown++
			if !strings.Contains(line, `"level":"DEBUG"`) {
				t.Fatal("unknown client order id was not logged at debug")
			}
		}
	}
	if warnings != 2 || unknown != 2 {
		t.Fatalf("event logs: warnings=%d unknown=%d, want two of each", warnings, unknown)
	}
}
