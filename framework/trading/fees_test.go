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
	must(t, f.registry.Register(trading.Provider{Type: "fake", Title: "Fee venue", UnlinkedFees: true,
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
	feeAccountBalance(t, f, "desk", asset, want)
}

func feeAccountBalance(t *testing.T, f *fixture, account domain.AccountID, asset, want string) {
	t.Helper()
	balance, found, err := f.n.GetBalance(testCtx, account, asset)
	must(t, err)
	if !found {
		t.Fatalf("fee balance missing for %s", asset)
	}
	assertDecimal(t, balance.Available, want)
}

func feeSkipRows(t *testing.T, f *fixture, want int) []domain.AuditRow {
	t.Helper()
	rows, err := f.n.ListAuditFiltered(testCtx, domain.AuditFilter{Actions: []domain.AuditAction{domain.AuditActionVenueFeeSkipped}}, 100)
	must(t, err)
	if len(rows) != want {
		t.Fatalf("venue_fee_skipped rows = %d, want %d: %+v", len(rows), want, rows)
	}
	for _, row := range rows {
		if row.Source != domain.SourceSystem || row.Actor != "" || row.Account != "" || !strings.Contains(row.Detail, f.connection.ExternalID.String()) {
			t.Fatalf("venue fee skip caller/connection = %+v", row)
		}
		if strings.Contains(row.Detail, "do-not-log-key") || strings.Contains(row.Detail, "do-not-log-secret") {
			t.Fatalf("venue fee skip contains credentials: %+v", row)
		}
	}
	return rows
}

func postedFee(id, asset, amount string) trading.Fee {
	return trading.Fee{ID: id, Asset: asset, Amount: amount,
		Status: trading.FeeExecuted, At: time.Now().Add(time.Hour)}
}

const defaultFeeAccountLog = "venue fee applied to the only account that can send through the connection because the connection has no fee account"

func defaultFeeAccountLogs(f *fixture) []string {
	var lines []string
	for _, line := range strings.Split(f.logs.text(), "\n") {
		if strings.Contains(line, defaultFeeAccountLog) {
			lines = append(lines, line)
		}
	}
	return lines
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
		Type:  domain.AdjustmentTypeDeposit,
		Asset: "ETHER", Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "1"},
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
	logs := defaultFeeAccountLogs(f)
	if len(logs) != 2 {
		t.Fatalf("default fee account logs = %q, want one per applied fee", logs)
	}
	for i, feeID := range []string{"USD-fee", "crypto-fee"} {
		if !strings.Contains(logs[i], `"level":"INFO"`) || !strings.Contains(logs[i], `"fee_id":"`+feeID+`"`) ||
			!strings.Contains(logs[i], `"account":"desk"`) || !strings.Contains(logs[i], f.connection.ExternalID.String()) {
			t.Fatalf("default fee account log lost level/fee id/account/connection: %s", logs[i])
		}
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
	if logs := defaultFeeAccountLogs(f); len(logs) != 2 {
		t.Fatalf("replayed fees logged as applied to the default account again: %q", logs)
	}
	id := domain.ExternalID("trading:" + f.connection.ExternalID.String() + ":fee:USD-fee")
	records, err := f.st.ListAdjustments(testCtx, "desk", "", 10)
	must(t, err)
	posted := 0
	for _, record := range records {
		if record.ExternalID != id {
			continue
		}
		posted++
		if record.Request.Type != domain.AdjustmentTypeVenueFee {
			t.Fatalf("fee adjustment type = %q, want venue_fee", record.Request.Type)
		}
	}
	if posted != 1 {
		t.Fatalf("fee adjustments under %q = %d, want 1: %+v", id, posted, records)
	}
	if err := f.adjustmentSink(testCtx, "desk", id, domain.AdjustmentRequest{
		Type:  domain.AdjustmentTypeVenueFee,
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

func TestFeeAccountTargets(t *testing.T) {
	for _, scenario := range []string{"no bindings", "one binding", "two bindings", "bound fee account"} {
		t.Run(scenario, func(t *testing.T) {
			f, v := newFeeFixture(t)
			f.fund("fees", "1000")
			f.fund("other", "1000")
			feeAccount := domain.AccountID("fees")
			if scenario == "bound fee account" {
				feeAccount = "desk"
			}
			must(t, f.n.SetTradingConnectionFeeAccount(testCtx, f.connection.ExternalID, feeAccount, testCaller))
			f.start()
			f.send(f.submit("1"))
			if scenario == "no bindings" {
				must(t, f.n.RemoveTradingAccess(testCtx, f.connection.ExternalID, "desk", "", testCaller))
			}
			if scenario == "two bindings" || scenario == "bound fee account" {
				must(t, f.n.PutTradingAccess(testCtx, domain.TradingAccess{Connection: f.connection.ExternalID, Account: "other"}, true, testCaller))
			}
			v.mu.Lock()
			v.fees = []trading.Fee{postedFee("whole-fee", "USD", "-0.125")}
			v.mu.Unlock()
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			wantDesk, wantFees := "998", "999.875"
			if feeAccount == "desk" {
				wantDesk, wantFees = "997.875", "1000"
			}
			feeAccountBalance(t, f, "desk", "USD", wantDesk)
			feeAccountBalance(t, f, "fees", "USD", wantFees)
			feeAccountBalance(t, f, "other", "USD", "1000")
			feeSkipRows(t, f, 0)
			if logs := defaultFeeAccountLogs(f); len(logs) != 0 {
				t.Fatalf("fee to the connection's fee account logged as the default target: %q", logs)
			}
			must(t, f.runtime.Restart())
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			feeAccountBalance(t, f, "desk", "USD", wantDesk)
			feeAccountBalance(t, f, "fees", "USD", wantFees)
			feeAccountBalance(t, f, "other", "USD", "1000")
		})
	}
}

// TestFeeAccountThroughGroup sends through a group grant alone: an unlinked fee
// is booked to the connection's fee account whatever the group's size. A group
// grant on an unlinked-fees provider needs that fee account, so this is the
// only reachable group setup there.
func TestFeeAccountThroughGroup(t *testing.T) {
	for _, members := range [][]domain.AccountID{{"desk"}, {"desk", "other"}} {
		t.Run(fmt.Sprintf("%d members", len(members)), func(t *testing.T) {
			f, v := newFeeFixture(t)
			f.fund("fees", "1000")
			f.fund("other", "1000")
			_, err := f.n.CreateGroup(testCtx, domain.AccountGroup{Code: "team"}, testCaller)
			must(t, err)
			for _, account := range members {
				must(t, f.n.SetAccountGroup(testCtx, account, "team", domain.MissingAccountReject, testCaller))
			}
			must(t, f.n.SetTradingConnectionFeeAccount(testCtx, f.connection.ExternalID, "fees", testCaller))
			must(t, f.n.PutTradingAccess(testCtx, domain.TradingAccess{AccountGroup: "team", Connection: f.connection.ExternalID}, true, testCaller))
			must(t, f.n.RemoveTradingAccess(testCtx, f.connection.ExternalID, "desk", "", testCaller))
			f.start()
			f.send(f.submit("1"))
			v.mu.Lock()
			v.fees = []trading.Fee{postedFee("group-fee", "USD", "-0.125")}
			v.mu.Unlock()
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			feeAccountBalance(t, f, "fees", "USD", "999.875")
			feeAccountBalance(t, f, "desk", "USD", "998")
			feeAccountBalance(t, f, "other", "USD", "1000")
			feeSkipRows(t, f, 0)
		})
	}
}

func TestFeeAccountChangeWithoutRestart(t *testing.T) {
	f, v := newFeeFixture(t)
	f.fund("fees", "1000")
	f.fund("other", "1000")
	must(t, f.n.SetTradingConnectionFeeAccount(testCtx, f.connection.ExternalID, "fees", testCaller))
	f.start()
	f.send(f.submit("1"))
	v.mu.Lock()
	v.fees = []trading.Fee{postedFee("first-page", "USD", "-1"), postedFee("second-page", "USD", "-1")}
	v.pageSize = 1
	var reads atomic.Int32
	v.onFees = func(ctx context.Context) error {
		if reads.Add(1) == 1 {
			return f.n.SetTradingConnectionFeeAccount(ctx, f.connection.ExternalID, "other", testCaller)
		}
		return nil
	}
	v.mu.Unlock()
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	feeAccountBalance(t, f, "fees", "USD", "998")
	feeAccountBalance(t, f, "other", "USD", "1000")
	v.mu.Lock()
	v.fees = append(v.fees, postedFee("next-pass", "USD", "-1"))
	v.mu.Unlock()
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	feeAccountBalance(t, f, "fees", "USD", "998")
	feeAccountBalance(t, f, "other", "USD", "999")
	must(t, f.runtime.Restart())
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	feeAccountBalance(t, f, "fees", "USD", "998")
	feeAccountBalance(t, f, "other", "USD", "999")
	feeAccountBalance(t, f, "desk", "USD", "998")
	feeSkipRows(t, f, 0)
}

type feeAccountChangeStore struct {
	trading.Store
	change func()
	armed  atomic.Bool
}

func (s *feeAccountChangeStore) GetTradingConnection(ctx context.Context, id domain.ExternalID) (domain.TradingConnection, bool, error) {
	connection, found, err := s.Store.GetTradingConnection(ctx, id)
	if err == nil && found && s.armed.CompareAndSwap(true, false) {
		s.change()
	}
	return connection, found, err
}

func (s *feeAccountChangeStore) GetTradingFeeAccounts(ctx context.Context, id domain.ExternalID) (domain.AccountID, []domain.AccountID, error) {
	account, bound, err := s.Store.GetTradingFeeAccounts(ctx, id)
	if err == nil && s.armed.CompareAndSwap(true, false) {
		s.change()
	}
	return account, bound, err
}

func TestFeeAccountSnapshotPreventsMixedTarget(t *testing.T) {
	f, v := newFeeFixture(t)
	f.fund("fees", "1000")
	f.fund("other", "1000")
	st := &feeAccountChangeStore{Store: f.st}
	st.change = func() {
		must(t, f.n.SetTradingConnectionFeeAccount(testCtx, f.connection.ExternalID, "fees", testCaller))
		must(t, f.n.RemoveTradingAccess(testCtx, f.connection.ExternalID, "desk", "", testCaller))
		must(t, f.n.PutTradingAccess(testCtx, domain.TradingAccess{Account: "other", Connection: f.connection.ExternalID}, true, testCaller))
	}
	r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink, f.feeSkipSink,
		slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	must(t, err)
	f.runtime = r
	must(t, r.Start(testCtx))
	defer r.Stop()
	f.send(f.submit("1"))
	v.mu.Lock()
	v.fees = []trading.Fee{postedFee("snapshot", "USD", "-1")}
	v.mu.Unlock()
	st.armed.Store(true)
	f.venue.push(trading.Event{Kind: trading.EventConnected})
	f.flush()
	if st.armed.Load() {
		t.Fatal("operator-change hook did not run")
	}
	feeAccountBalance(t, f, "desk", "USD", "997")
	feeAccountBalance(t, f, "fees", "USD", "1000")
	feeAccountBalance(t, f, "other", "USD", "1000")
	feeSkipRows(t, f, 0)
}

func TestAppliedFeeReplayAfterTargetOrMappingRemoval(t *testing.T) {
	for _, change := range []string{"revoke binding", "remove mapping"} {
		t.Run(change, func(t *testing.T) {
			f, v := newFeeFixture(t)
			v.fees = []trading.Fee{postedFee("applied", "USD", "-1")}
			var applied atomic.Int32
			sink := func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
				applied.Add(1)
				return f.adjustmentSink(ctx, account, id, req)
			}
			r, err := trading.NewRuntime(f.registry, f.st, f.sink, sink, f.feeSkipSink,
				slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			must(t, err)
			f.runtime = r
			must(t, r.Start(testCtx))
			defer r.Stop()
			f.send(f.submit("1"))
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			feeBalance(t, f, "USD", "997")
			r.Stop()
			if change == "revoke binding" {
				must(t, f.n.RemoveTradingAccess(testCtx, f.connection.ExternalID, "desk", "", testCaller))
			} else {
				must(t, f.n.DeleteTradingInstrument(testCtx, f.connection.ExternalID, "AAPL", testCaller))
			}
			must(t, r.Start(testCtx))
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			feeSkipRows(t, f, 0)
			feeBalance(t, f, "USD", "997")
			if applied.Load() != 1 {
				t.Fatalf("fee applied %d times, want once", applied.Load())
			}
			if len(v.cursors()) != 2 {
				t.Fatalf("fee reads=%v, want replay after restart", v.cursors())
			}
		})
	}
}

func TestRuntimeRequiresFeeSkipSink(t *testing.T) {
	f := newFixture(t)
	r, err := trading.NewRuntime(f.registry, f.st, f.sink, f.adjustmentSink, nil, slog.Default())
	if r != nil || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("runtime without fee skip sink = %v, err=%v", r, err)
	}
}

func TestFeeSkipSinkFailureRetries(t *testing.T) {
	for _, failure := range []error{errors.New("audit unavailable"), domain.ErrEngineRestarting} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, v := newFeeFixture(t)
				v.fees = []trading.Fee{
					{ID: "skipped", Status: trading.FeeNotApplicable, Detail: "activity needs attention", At: time.Now().Add(time.Second)},
					postedFee("accepted", "USD", "-1"),
				}
				var reports atomic.Int32
				sink := func(ctx context.Context, connection domain.ExternalID, feeID, reason string) error {
					if reports.Add(1) == 1 {
						return failure
					}
					return f.feeSkipSink(ctx, connection, feeID, reason)
				}
				r, err := trading.NewRuntime(f.registry, f.st, f.sink, f.adjustmentSink, sink,
					slog.New(slog.NewJSONHandler(f.logs, nil)))
				must(t, err)
				f.runtime = r
				must(t, r.Start(testCtx))
				defer r.Stop()
				f.send(f.submit("1"))
				f.venue.push(trading.Event{Kind: trading.EventConnected})
				synctest.Wait()
				feeBalance(t, f, "USD", "998")
				feeSkipRows(t, f, 0)
				if reports.Load() != 1 || !strings.Contains(f.logs.text(), "report skipped venue fee") || !strings.Contains(f.logs.text(), failure.Error()) {
					t.Fatalf("failed fee skip sink reports=%d logs=%s", reports.Load(), f.logs.text())
				}
				time.Sleep(time.Hour)
				synctest.Wait()
				if reports.Load() != 2 {
					t.Fatalf("fee skip report attempts=%d, want retry without restart", reports.Load())
				}
				feeSkipRows(t, f, 1)
				feeBalance(t, f, "USD", "997")
				must(t, r.Restart())
				f.venue.push(trading.Event{Kind: trading.EventConnected})
				synctest.Wait()
				if reports.Load() != 2 {
					t.Fatalf("successful fee skip repeated after restart: %d", reports.Load())
				}
				feeSkipRows(t, f, 1)
				feeBalance(t, f, "USD", "997")
			})
		})
	}
}

type feeAccountReadFailureStore struct {
	trading.Store
	fail atomic.Bool
}

func (s *feeAccountReadFailureStore) GetTradingFeeAccounts(ctx context.Context, connection domain.ExternalID) (domain.AccountID, []domain.AccountID, error) {
	if s.fail.Load() {
		return "", nil, errors.New("fee accounts read unavailable")
	}
	return s.Store.GetTradingFeeAccounts(ctx, connection)
}

func TestFeeAccountReadFailuresRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, v := newFeeFixture(t)
		v.fees = []trading.Fee{postedFee("retry", "USD", "-1")}
		st := &feeAccountReadFailureStore{Store: f.st}
		r, err := trading.NewRuntime(f.registry, st, f.sink, f.adjustmentSink, f.feeSkipSink,
			slog.New(slog.NewJSONHandler(f.logs, nil)))
		must(t, err)
		f.runtime = r
		must(t, r.Start(testCtx))
		defer r.Stop()
		f.send(f.submit("1"))
		st.fail.Store(true)
		f.venue.push(trading.Event{Kind: trading.EventConnected})
		synctest.Wait()
		feeBalance(t, f, "USD", "998")
		feeSkipRows(t, f, 0)
		if !strings.Contains(f.logs.text(), "read unavailable") || len(v.cursors()) != 0 {
			t.Fatal("fee intake did not stop and log the snapshot read failure")
		}
		st.fail.Store(false)
		time.Sleep(time.Hour)
		synctest.Wait()
		feeBalance(t, f, "USD", "997")
		feeSkipRows(t, f, 0)
	})
}

// feeAccessStore reports no fee account and accounts as the connection's users.
type feeAccessStore struct {
	trading.Store
	accounts []domain.AccountID
}

func (st feeAccessStore) GetTradingFeeAccounts(context.Context, domain.ExternalID) (domain.AccountID, []domain.AccountID, error) {
	return "", st.accounts, nil
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
				// The node refuses a second grant on a connection without a fee
				// account, so the store reports one the runtime must still refuse.
				st = feeAccessStore{Store: f.st, accounts: []domain.AccountID{"desk", "other"}}
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
			r, err := trading.NewRuntime(f.registry, st, f.sink, sink, f.feeSkipSink,
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
			rows := feeSkipRows(t, f, 1)
			if !strings.Contains(rows[0].Detail, fee.ID) || !strings.Contains(rows[0].Detail, wantLog) {
				t.Fatalf("fee skip lost id or reason: %+v", rows)
			}
			must(t, r.Restart())
			f.venue.push(trading.Event{Kind: trading.EventConnected})
			f.flush()
			feeSkipRows(t, f, 1)
			feeBalance(t, f, "USD", "998")
			logs := f.logs.text()
			if !strings.Contains(logs, wantLog) || !strings.Contains(logs, `"level":"ERROR"`) ||
				!strings.Contains(logs, `"fee_id":"`+fee.ID+`"`) || !strings.Contains(logs, f.connection.ExternalID.String()) {
				t.Fatalf("fee attention log lost reason/id/connection/error level: %s", logs)
			}
			if lines := defaultFeeAccountLogs(f); len(lines) != 0 {
				t.Fatalf("skipped fee logged as applied to the default account: %q", lines)
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
				r, err := trading.NewRuntime(f.registry, f.st, f.sink, sink, f.feeSkipSink,
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
				feeSkipRows(t, f, 0)
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
				feeSkipRows(t, f, 0)
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
	failure         string
	instrumentCalls int
}

func (s *feeRetryStore) ListTradingInstruments(ctx context.Context, connection domain.ExternalID) ([]domain.TradingInstrument, error) {
	s.instrumentCalls++
	if s.failure == "instrument store" && s.instrumentCalls == 2 {
		return nil, fmt.Errorf("instrument store unavailable: %w", domain.ErrInvalid)
	}
	return s.Store.ListTradingInstruments(ctx, connection)
}

func TestFeeCursorResumesAfterFailure(t *testing.T) {
	for _, failure := range []string{"sink", "instrument store"} {
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
				r, err := trading.NewRuntime(f.registry, &feeRetryStore{Store: f.st, failure: failure}, f.sink, sink, f.feeSkipSink,
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
		v.fees = []trading.Fee{postedFee("rejected", "USD", "-1"), postedFee("duplicate", "USD", "-1"), postedFee("accepted", "USD", "-1")}
		var rejects atomic.Int32
		sink := func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
			if strings.HasSuffix(id.String(), ":rejected") {
				rejects.Add(1)
				return fmt.Errorf("risk blocked: %w", trading.ErrAdjustmentRejected)
			}
			if strings.HasSuffix(id.String(), ":duplicate") {
				return domain.ErrAlreadyExists
			}
			return f.adjustmentSink(ctx, account, id, req)
		}
		r, err := trading.NewRuntime(f.registry, f.st, f.sink, sink, f.feeSkipSink,
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
		rows := feeSkipRows(t, f, 1)
		if !strings.Contains(rows[0].Detail, "rejected") || !strings.Contains(rows[0].Detail, "rejected by the core") {
			t.Fatalf("core rejection skip audit = %+v", rows)
		}
		if rejects.Load() != 1 || strings.Count(f.logs.text(), "fee rejected not applied: rejected by the core: risk blocked") != 1 ||
			!strings.Contains(f.logs.text(), `"level":"ERROR"`) {
			t.Fatalf("core rejection must be logged once and consumed: rejects=%d logs=%s", rejects.Load(), f.logs.text())
		}
		if lines := defaultFeeAccountLogs(f); len(lines) != 1 || !strings.Contains(lines[0], `"fee_id":"accepted"`) {
			t.Fatalf("default fee account logs = %q, want the accepted fee alone", lines)
		}
	})
}

func TestPersistedFeeCoreRejectionRetriesSkipReport(t *testing.T) {
	for _, failure := range []error{errors.New("audit unavailable"), domain.ErrEngineRestarting} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, v := newFeeFixture(t)
				v.fees = []trading.Fee{postedFee("rejected", "USD", "79228162514264337593543950335")}
				var adjustments, reports atomic.Int32
				adjust := func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
					adjustments.Add(1)
					return f.adjustmentSink(ctx, account, id, req)
				}
				skip := func(ctx context.Context, connection domain.ExternalID, feeID, reason string) error {
					if reports.Add(1) == 1 {
						return failure
					}
					return f.feeSkipSink(ctx, connection, feeID, reason)
				}
				r, err := trading.NewRuntime(f.registry, f.st, f.sink, adjust, skip,
					slog.New(slog.NewJSONHandler(f.logs, nil)))
				must(t, err)
				f.runtime = r
				must(t, r.Start(testCtx))
				defer r.Stop()
				f.send(f.submit("1"))
				f.venue.push(trading.Event{Kind: trading.EventConnected})
				synctest.Wait()
				feeID := domain.ExternalID("trading:" + f.connection.ExternalID.String() + ":fee:rejected")
				records, err := f.st.ListAdjustments(testCtx, "desk", domain.SourceSystem, 10)
				must(t, err)
				if len(records) != 1 || records[0].ExternalID != feeID || records[0].Accepted != nil || records[0].Rejected == nil {
					t.Fatalf("core rejection was not persisted: %+v", records)
				}
				feeSkipRows(t, f, 0)
				feeBalance(t, f, "USD", "998")
				if adjustments.Load() != 1 || reports.Load() != 1 || !strings.Contains(f.logs.text(), failure.Error()) {
					t.Fatalf("first pass adjustments=%d reports=%d logs=%s", adjustments.Load(), reports.Load(), f.logs.text())
				}
				time.Sleep(time.Hour)
				synctest.Wait()
				rows := feeSkipRows(t, f, 1)
				if !strings.Contains(rows[0].Detail, "fee rejected: rejected by the core") {
					t.Fatalf("core rejection skip audit = %+v", rows)
				}
				if adjustments.Load() != 1 || reports.Load() != 2 {
					t.Fatalf("retry adjustments=%d reports=%d, want 1 and 2", adjustments.Load(), reports.Load())
				}
				must(t, r.Restart())
				f.venue.push(trading.Event{Kind: trading.EventConnected})
				synctest.Wait()
				feeSkipRows(t, f, 1)
				feeBalance(t, f, "USD", "998")
				if adjustments.Load() != 1 || reports.Load() != 2 {
					t.Fatalf("replay adjustments=%d reports=%d, want 1 and 2", adjustments.Load(), reports.Load())
				}
			})
		})
	}
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
		r, err := trading.NewRuntime(f.registry, f.st, f.sink, sink, f.feeSkipSink,
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
