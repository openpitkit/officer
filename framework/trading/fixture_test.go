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
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/signing"
)

var testCtx = context.Background()
var testCaller = domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}

type safeBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *safeBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.String()
}

type fakeVenue struct {
	mu           sync.Mutex
	snapshots    map[string]trading.Snapshot
	sends        []trading.Order
	lookups      int
	closes       int
	reports      []domain.ExecutionReportInput
	sendErr      error
	preErr       error
	subscribeErr error
	onSend       func(context.Context, trading.Order) error
	onPreCheck   func(context.Context, trading.Order) error
	current      *fakeConnector
}
type fakeConnector struct {
	venue     *fakeVenue
	events    chan trading.Event
	closeOnce sync.Once
}

func newVenue() *fakeVenue { return &fakeVenue{snapshots: make(map[string]trading.Snapshot)} }
func (v *fakeVenue) build(domain.TradingConnection) (trading.Connector, error) {
	c := &fakeConnector{venue: v, events: make(chan trading.Event, 256)}
	v.mu.Lock()
	v.current = c
	v.mu.Unlock()
	return c, nil
}
func (c *fakeConnector) Subscribe(ctx context.Context) (<-chan trading.Event, error) {
	c.venue.mu.Lock()
	err := c.venue.subscribeErr
	c.venue.mu.Unlock()
	if err != nil {
		return nil, err
	}
	go func() { <-ctx.Done(); c.closeEvents() }()
	return c.events, nil
}
func (c *fakeConnector) closeEvents() { c.closeOnce.Do(func() { close(c.events) }) }
func (c *fakeConnector) Close() {
	c.closeEvents()
	c.venue.mu.Lock()
	c.venue.closes++
	c.venue.mu.Unlock()
}
func (c *fakeConnector) PreCheck(ctx context.Context, order trading.Order) error {
	c.venue.mu.Lock()
	err, hook := c.venue.preErr, c.venue.onPreCheck
	c.venue.mu.Unlock()
	if hook != nil {
		return hook(ctx, order)
	}
	return err
}
func (c *fakeConnector) Send(ctx context.Context, order trading.Order) (trading.Ack, error) {
	v := c.venue
	v.mu.Lock()
	v.sends = append(v.sends, order)
	err, hook := v.sendErr, v.onSend
	v.mu.Unlock()
	if hook != nil {
		if hookErr := hook(ctx, order); hookErr != nil {
			return trading.Ack{}, hookErr
		}
	}
	if err != nil {
		return trading.Ack{}, err
	}
	id := "venue:" + order.ClientOrderID
	v.mu.Lock()
	v.snapshots[order.ClientOrderID] = trading.Snapshot{VenueOrderID: id,
		Status: trading.VenueStatusOpen, FilledQuantity: "0"}
	v.mu.Unlock()
	return trading.Ack{VenueOrderID: id}, nil
}
func (c *fakeConnector) Lookup(_ context.Context, ref trading.OrderRef) (trading.Snapshot, bool, error) {
	v := c.venue
	v.mu.Lock()
	defer v.mu.Unlock()
	v.lookups++
	snapshot, found := v.snapshots[ref.ClientOrderID]
	snapshot.Fills = append([]trading.Fill(nil), snapshot.Fills...)
	return snapshot, found, nil
}
func (v *fakeVenue) push(event trading.Event) {
	v.mu.Lock()
	c := v.current
	v.mu.Unlock()
	c.events <- event
}
func (v *fakeVenue) setSnapshot(link domain.VenueOrder, status trading.VenueStatus, filled string, fills ...trading.Fill) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.snapshots[link.ClientOrderID] = trading.Snapshot{
		VenueOrderID: "venue:" + link.ClientOrderID, Status: status,
		FilledQuantity: filled, Fills: append([]trading.Fill(nil), fills...),
	}
}
func (v *fakeVenue) counts() (sends, lookups int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.sends), v.lookups
}

type fixture struct {
	t          *testing.T
	st         store.RealmStore
	n          node.Node
	svc        *backend.Service
	registry   *trading.Registry
	venue      *fakeVenue
	connection domain.TradingConnection
	dest       domain.TradingDestination
	runtime    *trading.Runtime
	logs       *safeBuffer
	quiet      int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := sqlite.New(t.TempDir()+"/officer.db", domain.DefaultRealm)
	must(t, err)
	t.Cleanup(func() { must(t, st.Close()) })
	must(t, st.Migrate(testCtx))
	n, _, err := node.NewLocalNode(testCtx, domain.DefaultRealm, st,
		engine.NewOpenPitEngineBuildFunc(), func(err error) { t.Errorf("fatal node shutdown: %v", err) })
	must(t, err)
	t.Cleanup(func() { must(t, n.Close()) })
	realm, err := st.ForRealm(testCtx, domain.DefaultRealm)
	must(t, err)
	signer, err := signing.New(realm)
	must(t, err)
	svc, err := backend.New(n, nil, signer, engine.LockSettlementPrice)
	must(t, err)
	f := &fixture{t: t, st: realm, n: n, svc: svc, registry: trading.NewRegistry(),
		venue: newVenue(), logs: &safeBuffer{}}
	f.fund("desk", "1000")
	// Register the base asset through the node, keeping its engine registry live.
	_, err = n.CreateAsset(testCtx, domain.Asset{Code: "AAPL"}, testCaller)
	must(t, err)
	must(t, f.registry.Register(trading.Provider{Type: "fake", Title: "Test venue", Build: f.venue.build}))
	f.connection = f.addConnection("fake", "primary", true)
	f.dest = domain.TradingDestination{Connection: f.connection.ExternalID, Route: "{}"}
	return f
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) fund(account domain.AccountID, amount string) {
	f.t.Helper()
	_, err := f.n.CreateAccount(testCtx, domain.Account{Code: account, Currency: "USD"}, testCaller)
	must(f.t, err)
	xid, err := domain.NewExternalID()
	must(f.t, err)
	_, err = f.n.ApplyAdjustment(testCtx, account, xid, domain.AdjustmentRequest{
		Asset: "USD", AverageEntryPrice: "1", RealizedPnl: "0",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: amount},
	}, domain.MissingAccountCreate, testCaller)
	must(f.t, err)
}
func (f *fixture) addConnection(provider, label string, access bool) domain.TradingConnection {
	f.t.Helper()
	c, err := f.st.CreateTradingConnection(testCtx, domain.TradingConnection{
		Provider: provider, Label: label, Mode: domain.TradingModeTest,
		Credentials: "{\"key\":\"do-not-log-key\",\"secret\":\"do-not-log-secret\"}", Enabled: true,
	})
	must(f.t, err)
	must(f.t, f.st.UpsertTradingInstrument(testCtx, domain.TradingInstrument{
		Connection: c.ExternalID, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}))
	if access {
		must(f.t, f.st.AddTradingAccess(testCtx, domain.TradingAccess{Account: "desk", Connection: c.ExternalID}))
	}
	return c
}
func (f *fixture) sink(ctx context.Context, in domain.ExecutionReportInput) error {
	f.venue.mu.Lock()
	f.venue.reports = append(f.venue.reports, in)
	f.venue.mu.Unlock()
	_, _, err := f.svc.ApplyExecutionReport(auth.ContextWithCaller(ctx, auth.SystemCaller()), in)
	return err
}
func (f *fixture) adjustmentSink(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
	record, err := f.svc.ApplyAdjustment(auth.ContextWithCaller(ctx, auth.SystemCaller()), account, id, req, domain.MissingAccountReject)
	if err == nil && record.Rejected != nil {
		return fmt.Errorf("%s: %w", record.Rejected.Reason, trading.ErrAdjustmentRejected)
	}
	return err
}

func (f *fixture) start() {
	f.t.Helper()
	r, err := trading.NewRuntime(f.registry, f.st, f.sink, f.adjustmentSink,
		slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	must(f.t, err)
	f.runtime = r
	must(f.t, r.Start(testCtx))
	f.t.Cleanup(r.Stop)
}
func (f *fixture) draft(quantity string) domain.Order {
	return domain.Order{Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: quantity, Price: "2"}
}
func (f *fixture) submit(quantity string) domain.Order {
	f.t.Helper()
	order, err := f.n.SubmitOrder(testCtx, f.draft(quantity), domain.MissingAccountReject, testCaller)
	must(f.t, err)
	if order.Status != domain.OrderStatusCommitted {
		f.t.Fatalf("hold status = %s", order.Status)
	}
	return order
}
func (f *fixture) send(order domain.Order) domain.VenueOrder {
	f.t.Helper()
	link, err := f.runtime.Send(testCtx, order.ExternalID, f.dest)
	must(f.t, err)
	return link
}
func (f *fixture) detail(order domain.Order) domain.OrderDetail {
	f.t.Helper()
	detail, err := f.st.GetOrder(testCtx, order.ExternalID)
	must(f.t, err)
	return detail
}
func waitFor(t *testing.T, check func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
func (f *fixture) flush() {
	f.t.Helper()
	f.quiet++
	marker := fmt.Sprintf("quiet-%d", f.quiet)
	f.venue.push(trading.Event{Kind: trading.EventOrder, Ref: trading.OrderRef{ClientOrderID: marker}})
	waitFor(f.t, func() bool { return strings.Contains(f.logs.text(), "\"client_order_id\":\""+marker+"\"") }, "worker queue barrier")
}
func (f *fixture) fill(link domain.VenueOrder, fill trading.Fill) {
	f.venue.push(trading.Event{Kind: trading.EventFill,
		Ref: trading.OrderRef{ClientOrderID: link.ClientOrderID}, Fill: fill})
}
func (f *fixture) orderEvent(link domain.VenueOrder) {
	f.venue.push(trading.Event{Kind: trading.EventOrder, Ref: trading.OrderRef{ClientOrderID: link.ClientOrderID}})
}
func makeFill(qty, cum, leaves string, final bool) trading.Fill {
	return trading.Fill{Quantity: qty, CumQuantity: cum, Price: "2", LeavesQuantity: leaves, Final: final, At: time.Now()}
}
func assertDecimal(t *testing.T, got, want string) {
	t.Helper()
	d, err := decimal.NewFromString(got)
	must(t, err)
	expected, err := decimal.NewFromString(want)
	must(t, err)
	if !d.Equal(expected) {
		t.Fatalf("decimal = %s, want %s", got, want)
	}
}
func assertTrades(t *testing.T, detail domain.OrderDetail, fills []trading.Fill) {
	t.Helper()
	if len(detail.Trades) != len(fills) {
		t.Fatalf("trades = %d, want %d", len(detail.Trades), len(fills))
	}
	total := decimal.Zero
	for i, trade := range detail.Trades {
		assertDecimal(t, trade.Quantity, fills[i].Quantity)
		assertDecimal(t, trade.Price, fills[i].Price)
		q, err := decimal.NewFromString(trade.Quantity)
		must(t, err)
		total = total.Add(q)
	}
	if len(fills) > 0 {
		assertDecimal(t, total.String(), fills[len(fills)-1].CumQuantity)
	}
}
func (f *fixture) assertReleased(order domain.Order) {
	f.t.Helper()
	b, found, err := f.n.GetBalance(testCtx, order.Account, "USD")
	must(f.t, err)
	if !found {
		f.t.Fatal("USD balance missing")
	}
	assertDecimal(f.t, b.Held, "0")
}

var _ io.Writer = (*safeBuffer)(nil)
var _ trading.Connector = (*fakeConnector)(nil)
var _ trading.PreChecker = (*fakeConnector)(nil)
