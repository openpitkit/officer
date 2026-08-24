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

package marketdata

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scmhub/ibapi"

	"go.openpit.dev/officer/framework/domain"
)

func TestParseIBConfigAndContractOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := parseIBConfig("ib-primary", `{
		"host": "localhost",
		"port": 7496,
		"clientId": 42,
		"marketDataType": "delayed",
		"contracts": {
			"EUR.USD": {"symbol": "EUR", "secType": "CASH", "exchange": "IDEALPRO", "currency": "USD"},
			"AAPL": {"secType": "STK", "exchange": "SMART", "primaryExchange": "NASDAQ", "currency": "USD"}
		}
	}`)
	if err != nil {
		t.Fatalf("parseIBConfig: %v", err)
	}
	if cfg.Host != "localhost" || cfg.Port != 7496 || cfg.ClientID != 42 {
		t.Fatalf("connection config = %+v", cfg)
	}
	if cfg.MarketDataType != int64(ibapi.DELAYED) {
		t.Fatalf("MarketDataType = %d, want delayed", cfg.MarketDataType)
	}

	subs, err := normalizeIBSubscriptions(cfg, []Subscription{
		{External: "EUR.USD", Base: testMarketDataAssetID("EUR"), Quote: testMarketDataAssetID("USD")},
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("normalizeIBSubscriptions: %v", err)
	}
	eurusd := subs[0].contract
	if eurusd.Symbol != "EUR" || eurusd.SecType != "CASH" ||
		eurusd.Exchange != "IDEALPRO" || eurusd.Currency != "USD" {
		t.Fatalf("EUR.USD contract = %+v", eurusd)
	}
	aapl := subs[1].contract
	if aapl.Symbol != "AAPL" || aapl.SecType != "STK" ||
		aapl.Exchange != "SMART" || aapl.PrimaryExchange != "NASDAQ" ||
		aapl.Currency != "USD" {
		t.Fatalf("AAPL contract = %+v", aapl)
	}
}

func TestNormalizeIBSubscriptions_PreservesAssetKeys(t *testing.T) {
	t.Parallel()

	subs, err := normalizeIBSubscriptions(ibConfig{ContractDefaults: ibContractConfig{
		Currency: "provider.currency",
	}}, []Subscription{{
		External: "provider.symbol",
		Base:     testMarketDataAssetID("asset.base"),
		Quote:    testMarketDataAssetID("asset.quote"),
	}})
	if err != nil {
		t.Fatalf("normalizeIBSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subscription count = %d, want 1", len(subs))
	}
	if got := subs[0].Base; got != testMarketDataAssetID("asset.base") {
		t.Fatalf("subscription Base = %d, want caller key", got)
	}
	if got := subs[0].Quote; got != testMarketDataAssetID("asset.quote") {
		t.Fatalf("subscription Quote = %d, want caller key", got)
	}
	if got := subs[0].contract.Symbol; got != "PROVIDER.SYMBOL" {
		t.Fatalf("contract Symbol = %q, want uppercased provider value", got)
	}
	if got := subs[0].contract.Currency; got != "PROVIDER.CURRENCY" {
		t.Fatalf("contract Currency = %q, want uppercased provider value", got)
	}
}

func TestNormalizeIBSubscriptions_PerContractCurrencyOverrideWins(t *testing.T) {
	t.Parallel()

	subs, err := normalizeIBSubscriptions(ibConfig{
		ContractDefaults: ibContractConfig{Currency: "default.currency"},
		Contracts: map[string]ibContractConfig{
			"provider.symbol": {Currency: "contract.currency"},
		},
	}, []Subscription{{
		External: "provider.symbol",
		Base:     testMarketDataAssetID("asset.base"),
		Quote:    testMarketDataAssetID("asset.quote"),
	}})
	if err != nil {
		t.Fatalf("normalizeIBSubscriptions: %v", err)
	}
	if got := subs[0].contract.Currency; got != "CONTRACT.CURRENCY" {
		t.Fatalf("contract Currency = %q, want per-contract override", got)
	}
}

func TestNormalizeIBSubscriptions_PreservesSyntheticInverse(t *testing.T) {
	t.Parallel()

	subs, err := normalizeIBSubscriptions(ibConfig{
		ContractDefaults: ibContractConfig{Currency: "USD"},
	}, []Subscription{{
		External:         "AAPL",
		Base:             testMarketDataAssetID("asset.base"),
		Quote:            testMarketDataAssetID("asset.quote"),
		SyntheticInverse: true,
	}})
	if err != nil {
		t.Fatalf("normalizeIBSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subscription count = %d, want 1", len(subs))
	}
	if !subs[0].SyntheticInverse {
		t.Fatal("subscription SyntheticInverse = false, want true")
	}
}

func TestNormalizeIBSubscriptions_RequiresContractCurrency(t *testing.T) {
	t.Parallel()

	_, err := normalizeIBSubscriptions(ibConfig{}, []Subscription{{
		External: "AAPL",
		Base:     testMarketDataAssetID("opaque-base-key"),
		Quote:    testMarketDataAssetID("opaque-quote-key"),
	}})
	if err == nil {
		t.Fatal("normalizeIBSubscriptions error = nil, want missing currency error")
	}
	if !strings.Contains(err.Error(), "AAPL") ||
		!strings.Contains(err.Error(), "IB contract currency is not configured") {
		t.Fatalf("normalizeIBSubscriptions error = %q", err)
	}
}

func TestParseIBMarketDataType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  any
		want int64
	}{
		{name: "numeric", raw: float64(3), want: int64(ibapi.DELAYED)},
		{name: "string", raw: "delayed_frozen", want: int64(ibapi.DELAYED_FROZEN)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseIBMarketDataType(tt.raw)
			if err != nil {
				t.Fatalf("parseIBMarketDataType(%v): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("parseIBMarketDataType(%v) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}

	if _, err := parseIBMarketDataType(float64(1.5)); err == nil {
		t.Fatal("parseIBMarketDataType(1.5) err = nil, want error")
	}
	if _, err := parseIBMarketDataType("stale"); err == nil {
		t.Fatal("parseIBMarketDataType(stale) err = nil, want error")
	}
}

func TestDefaultIBClientIDReservesFallback(t *testing.T) {
	t.Parallel()

	if got := defaultIBClientID(""); got != defaultIBFallbackClientID {
		t.Fatalf("defaultIBClientID(empty) = %d, want fallback", got)
	}
	if got := defaultIBClientID("ib-primary"); got <= defaultIBFallbackClientID {
		t.Fatalf("defaultIBClientID(non-empty) = %d, want above fallback", got)
	}
}

func TestIBWrapperTickPriceEmitsNormalizedUpdates(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan QuoteUpdate, 6)
	at := time.Unix(1710000000, 0).UTC()
	sourceAt := time.Unix(1710000100, 0).UTC()
	subs := []ibSubscription{{
		Subscription: Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		reqID:        1,
	}}
	wrapper := newIBWrapper(
		ctx, subs, out, func() time.Time { return at }, nil, nil,
	)

	wrapper.TickPrice(1, ibapi.BID, 181.11, ibapi.TickAttrib{})
	wrapper.TickPrice(1, ibapi.ASK, 181.13, ibapi.TickAttrib{})
	wrapper.TickString(1, ibapi.LAST_TIMESTAMP, strconv.FormatInt(sourceAt.Unix(), 10))
	wrapper.TickPrice(1, ibapi.LAST, 181.12, ibapi.TickAttrib{})
	wrapper.TickPrice(1, ibapi.MARK_PRICE, 181.10, ibapi.TickAttrib{})
	wrapper.TickPrice(1, ibapi.LAST, 181.15, ibapi.TickAttrib{})

	first := <-out
	if first.Bid != "181.11" || first.Ask != "" || first.Mark != "" {
		t.Fatalf("first update = %+v", first)
	}
	second := <-out
	if second.Bid != "181.11" || second.Ask != "181.13" || second.Mark != "" {
		t.Fatalf("second update = %+v", second)
	}
	third := <-out
	if !third.AsOf.Equal(sourceAt) || third.Base != testMarketDataAssetID("AAPL") || third.Quote != testMarketDataAssetID("USD") ||
		third.Bid != "181.11" || third.Ask != "181.13" || third.Mark != "181.12" {
		t.Fatalf("third update = %+v", third)
	}
	fourth := <-out
	if fourth.Mark != "181.1" {
		t.Fatalf("fourth mark = %q, want computed mark", fourth.Mark)
	}
	fifth := <-out
	if fifth.Mark != "181.1" {
		t.Fatalf("fifth mark = %q, want computed mark priority over last", fifth.Mark)
	}
	if !first.AsOf.Equal(at) || !second.AsOf.Equal(at) {
		t.Fatalf("fallback AsOf = %s/%s, want %s", first.AsOf, second.AsOf, at)
	}
}

func TestIBWrapperTickPriceClearsNoQuoteAndReportsNoData(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan QuoteUpdate, 4)
	diags := make(chan Diagnostic, 2)
	subs := []ibSubscription{{
		Subscription: Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		reqID:        1,
	}}
	wrapper := newIBWrapper(
		ctx, subs, out, time.Now, nil, func(diag Diagnostic) { diags <- diag },
	)

	wrapper.TickPrice(1, ibapi.BID, 181.11, ibapi.TickAttrib{})
	if update := <-out; update.Bid != "181.11" {
		t.Fatalf("initial bid update = %+v", update)
	}
	wrapper.TickPrice(1, ibapi.BID, 0, ibapi.TickAttrib{})
	if update := <-out; update.Bid != "" {
		t.Fatalf("zero bid update = %+v, want cleared bid", update)
	}

	wrapper.TickPrice(1, ibapi.ASK, 181.13, ibapi.TickAttrib{})
	if update := <-out; update.Ask != "181.13" {
		t.Fatalf("initial ask update = %+v", update)
	}
	wrapper.TickPrice(1, ibapi.ASK, -1, ibapi.TickAttrib{})
	if update := <-out; update.Ask != "" {
		t.Fatalf("no-data ask update = %+v, want cleared ask", update)
	}
	select {
	case diag := <-diags:
		if diag.Code != CodeNoData || diag.Instrument != "AAPL" {
			t.Fatalf("diag = %+v", diag)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for no-data diagnostic")
	}
	wrapper.TickPrice(1, ibapi.ASK, -1, ibapi.TickAttrib{})
	select {
	case diag := <-diags:
		t.Fatalf("duplicate no-data diagnostic = %+v", diag)
	default:
	}
}

func TestIBWrapperIgnoresInvalidTickPrice(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan QuoteUpdate, 1)
	subs := []ibSubscription{{
		Subscription: Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		reqID:        1,
	}}
	wrapper := newIBWrapper(ctx, subs, out, time.Now, nil, nil)

	wrapper.TickPrice(2, ibapi.BID, 181.11, ibapi.TickAttrib{})
	wrapper.TickPrice(1, ibapi.BID, math.NaN(), ibapi.TickAttrib{})
	wrapper.TickPrice(1, ibapi.BID, math.Inf(1), ibapi.TickAttrib{})
	wrapper.TickPrice(1, ibapi.BID_SIZE, 100, ibapi.TickAttrib{})

	select {
	case update := <-out:
		t.Fatalf("unexpected update = %+v", update)
	default:
	}
}

func TestIBWrapperIgnoresInvalidTickStringTimestamp(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan QuoteUpdate, 1)
	at := time.Unix(1710000000, 0).UTC()
	subs := []ibSubscription{{
		Subscription: Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		reqID:        1,
	}}
	wrapper := newIBWrapper(
		ctx, subs, out, func() time.Time { return at }, nil, nil,
	)

	wrapper.TickString(1, ibapi.LAST_TIMESTAMP, "not-a-timestamp")
	wrapper.TickString(1, ibapi.DELAYED_LAST_TIMESTAMP, "-1")
	wrapper.TickPrice(1, ibapi.LAST, 181.12, ibapi.TickAttrib{})

	update := <-out
	if !update.AsOf.Equal(at) {
		t.Fatalf("AsOf = %s, want fallback %s", update.AsOf, at)
	}
}

func TestIBConnectorSubscribesThroughClient(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-test", `{
		"host": "127.0.0.1",
		"port": 7496,
		"clientId": 109,
		"marketDataType": "delayed",
		"contracts": {"AAPL": {"currency": "USD"}}
	}`)
	connector.now = func() time.Time { return time.Unix(1710000001, 0).UTC() }
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		client.asyncReady = true
		client.readyDelay = time.Millisecond
		clients <- client
		return client
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	client := <-clients
	client.waitRequests(t, 1)
	if client.host != "127.0.0.1" || client.port != 7496 || client.clientID != 109 {
		t.Fatalf("connect args = %s:%d/%d", client.host, client.port, client.clientID)
	}
	if client.marketDataType != int64(ibapi.DELAYED) {
		t.Fatalf("market data type = %d", client.marketDataType)
	}
	if got := client.callsBeforeReadyCount(); got != 0 {
		t.Fatalf("IB API calls before NextValidID = %d", got)
	}
	request := client.requests[0]
	if request.contract.Symbol != "AAPL" || request.contract.SecType != "STK" ||
		request.contract.Exchange != "SMART" || request.contract.Currency != "USD" {
		t.Fatalf("request contract = %+v", request.contract)
	}

	go client.wrapper.TickPrice(1, ibapi.LAST, 181.12, ibapi.TickAttrib{})
	select {
	case update := <-ch:
		if update.Mark != "181.12" || update.Base != testMarketDataAssetID("AAPL") || update.Quote != testMarketDataAssetID("USD") {
			t.Fatalf("update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for quote update")
	}
	connector.Close()
}

func TestIBConnectorDiagnoseReportsActiveSubscriptions(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-test", `{"clientId":109,"contracts":{"AAPL":{"currency":"USD"}}}`)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		return newFakeIBClient(wrapper)
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer connector.Close()
	_ = ch

	findings, err := connector.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(findings) != 1 || findings[0].Code != CodeNoData ||
		findings[0].Instrument != "AAPL" {
		t.Fatalf("findings = %+v", findings)
	}
	connector.Close()
	findings, err = connector.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose after Close: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings after Close = %+v, want none", findings)
	}
}

func TestIBConnectorReconnectsAndResubscribes(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-test", `{"clientId":109,"contracts":{"AAPL":{"currency":"USD"}}}`)
	connector.sleep = func(context.Context, time.Duration) error { return nil }
	clients := make(chan *fakeIBClient, 2)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		clients <- client
		return client
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	first := <-clients
	first.waitRequests(t, 1)
	first.wrapper.ConnectionClosed()
	second := <-clients
	second.waitRequests(t, 1)

	if first.disconnectCount() == 0 {
		t.Fatal("first client disconnect count = 0, want cleanup after reconnect")
	}
	go second.wrapper.TickPrice(1, ibapi.LAST, 181.12, ibapi.TickAttrib{})
	select {
	case update := <-ch:
		if update.Mark != "181.12" {
			t.Fatalf("update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for quote after reconnect")
	}
	connector.Close()
}

// A connection that delivered a quote then dropped must reconnect from the
// minimum delay, not a grown backoff (mirrors binance/kraken).
func TestIBConnectorResetsBackoffAfterDelivery(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		delays []time.Duration
	)
	connector := NewIBConnector("ib-test", `{"clientId":109,"contracts":{"AAPL":{"currency":"USD"}}}`)
	connector.sleep = func(_ context.Context, delay time.Duration) error {
		mu.Lock()
		delays = append(delays, delay)
		mu.Unlock()
		return nil
	}
	clients := make(chan *fakeIBClient, 3)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		clients <- client
		return client
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Two healthy connections in a row: each delivers one quote, then drops.
	// Without the reset, the second sleep would be the grown backoff.
	for i, price := range []float64{181.12, 182.34} {
		client := <-clients
		client.waitRequests(t, 1)
		go client.wrapper.TickPrice(1, ibapi.LAST, price, ibapi.TickAttrib{})
		select {
		case update := <-ch:
			if want := strconv.FormatFloat(price, 'f', -1, 64); update.Mark != want {
				t.Fatalf("update[%d] = %+v, want mark %s", i, update, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for quote on connection %d", i+1)
		}
		client.wrapper.ConnectionClosed()
	}

	// Drain the third connection's request so it is parked on its select, then
	// cancel: the canceled error ends run cleanly without another sleep.
	third := <-clients
	third.waitRequests(t, 1)
	connector.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(delays) != 2 {
		t.Fatalf("delays = %v, want two reconnect sleeps", delays)
	}
	for i, delay := range delays {
		if delay != connector.reconnectMin {
			t.Fatalf("delay[%d] = %s, want %s", i, delay, connector.reconnectMin)
		}
	}
}

func TestIBConnectorCloseStopsSubscription(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-test", `{"clientId":109,"contracts":{"AAPL":{"currency":"USD"}}}`)
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		clients <- client
		return client
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	client := <-clients
	client.waitRequests(t, 1)
	connector.Close()

	if client.disconnectCount() == 0 {
		t.Fatal("disconnect count = 0, want disconnect on Close")
	}
	if client.cancelCount() == 0 {
		t.Fatal("cancel count = 0, want market-data cancel on Close")
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("subscription channel still open after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}

func TestIBConnectorConnectErrorDisconnectsAndRetries(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-test", `{"clientId":109,"contracts":{"AAPL":{"currency":"USD"}}}`)
	connector.sleep = func(context.Context, time.Duration) error { return nil }
	clients := make(chan *fakeIBClient, 2)
	created := 0
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		created++
		client := newFakeIBClient(wrapper)
		if created == 1 {
			client.connectErr = errors.New("startAPI failed")
		}
		clients <- client
		return client
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	first := <-clients
	second := <-clients
	second.waitRequests(t, 1)
	if first.disconnectCount() == 0 {
		t.Fatal("failed connect disconnect count = 0, want cleanup")
	}

	go second.wrapper.TickPrice(1, ibapi.LAST, 181.12, ibapi.TickAttrib{})
	select {
	case update := <-ch:
		if update.Mark != "181.12" {
			t.Fatalf("update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for quote after connect retry")
	}
	connector.Close()
}

func TestIBConnectorConnectTimeoutIsInterruptible(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-test", `{"clientId":109,"contracts":{"AAPL":{"currency":"USD"}}}`)
	connector.connectTimeout = time.Millisecond
	var client *fakeIBClient
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client = newFakeIBClient(wrapper)
		client.connectBlock = make(chan struct{})
		client.disconnectNoop = true
		return client
	}
	subs, err := normalizeIBSubscriptions(connector.cfg, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("normalizeIBSubscriptions: %v", err)
	}

	_, err = connector.stream(context.Background(), subs, make(chan QuoteUpdate))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream err = %v, want context deadline exceeded", err)
	}
	if client.forceDisconnectCount() == 0 {
		t.Fatal("force disconnect count = 0, want forced teardown on connect timeout")
	}
}

func TestIBConnectorConnectTimeoutDrainsResultWhenDisconnectFails(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-test", `{"clientId":109}`)
	connector.connectTimeout = time.Millisecond
	client := newFakeIBClient(nil)
	client.connectBlock = make(chan struct{})
	client.connectReturnBlock = make(chan struct{})
	client.disconnectErr = errors.New("disconnect failed")

	var releaseOnce sync.Once
	releaseConnect := func() {
		releaseOnce.Do(func() { close(client.connectReturnBlock) })
	}
	t.Cleanup(releaseConnect)

	result := make(chan error, 1)
	go func() {
		result <- connector.connectClient(
			context.Background(), client, connector.cfg.ClientID,
		)
	}()

	select {
	case <-client.connectBlock:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for disconnect after connect timeout")
	}
	select {
	case err := <-result:
		t.Fatalf("connectClient returned before Connect completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseConnect()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("connectClient error = %v, want context deadline exceeded", err)
		}
		if !errors.Is(err, client.disconnectErr) {
			t.Fatalf("connectClient error = %v, want disconnect failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("connectClient did not return after Connect completed")
	}
}

func TestIBConnectorInvalidCredentials(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-test", `{"port":70000}`)
	_, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err == nil {
		t.Fatal("Subscribe err = nil, want invalid credentials error")
	}
}

func TestIBWrapperReportsContractErrorDiagnostic(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	diags := make(chan Diagnostic, 1)
	subs := []ibSubscription{{
		Subscription: Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		reqID:        1,
	}}
	wrapper := newIBWrapper(ctx, subs, nil, time.Now, nil, func(diag Diagnostic) {
		diags <- diag
	})

	wrapper.Error(1, 0, 200, "No security definition has been found", "")

	select {
	case diag := <-diags:
		if diag.Code != CodeUnknownSymbol || diag.Kind != DiagKindConfig ||
			diag.Instrument != "AAPL" {
			t.Fatalf("diag = %+v", diag)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for diagnostic")
	}
}

func TestIBWrapperReportsMarketDataTypeFreshness(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	diags := make(chan Diagnostic, 2)
	subs := []ibSubscription{{
		Subscription: Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		reqID:        1,
	}}
	wrapper := newIBWrapper(ctx, subs, nil, time.Now, nil, func(diag Diagnostic) {
		diags <- diag
	})

	wrapper.MarketDataType(1, int64(ibapi.REALTIME))
	wrapper.MarketDataType(1, int64(ibapi.DELAYED_FROZEN))

	realtime := <-diags
	if realtime.Level != DiagInfo || realtime.Code != CodeDataFreshness ||
		realtime.Instrument != "AAPL" {
		t.Fatalf("realtime diag = %+v", realtime)
	}
	delayed := <-diags
	if delayed.Level != DiagWarn || delayed.Code != CodeDataFreshness ||
		delayed.Instrument != "AAPL" {
		t.Fatalf("delayed diag = %+v", delayed)
	}
}

func TestIBWrapperConnectAckDoesNotMarkReady(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapper := newIBWrapper(ctx, nil, nil, time.Now, nil, nil)

	wrapper.ConnectAck()
	if err := wrapper.waitReady(ctx, time.Millisecond); err == nil {
		t.Fatal("waitReady after ConnectAck err = nil, want timeout")
	}
	wrapper.NextValidID(1)
	if err := wrapper.waitReady(ctx, time.Second); err != nil {
		t.Fatalf("waitReady after NextValidID: %v", err)
	}
}

func TestIBWrapperClassifiesIBErrorDiagnostics(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	diags := make(chan Diagnostic, 4)
	subs := []ibSubscription{{
		Subscription: Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		reqID:        1,
	}}
	wrapper := newIBWrapper(ctx, subs, nil, time.Now, nil, func(diag Diagnostic) {
		diags <- diag
	})

	wrapper.Error(-1, 0, 2104, "Market data farm connection is OK", "")
	wrapper.Error(-1, 0, 1100, "Connectivity between IB and TWS has been lost", "")
	wrapper.Error(1, 0, 354, "Requested market data is not subscribed", "")
	wrapper.Error(-1, 0, 326, "Client ID already in use", "")

	info := <-diags
	if info.Level != DiagInfo || info.Code != CodeConnectionError {
		t.Fatalf("info diag = %+v", info)
	}
	recoverable := <-diags
	if recoverable.Level != DiagWarn || recoverable.Code != CodeConnectionError {
		t.Fatalf("recoverable diag = %+v", recoverable)
	}
	noData := <-diags
	if noData.Level != DiagWarn || noData.Code != CodeNoData ||
		noData.Kind != DiagKindConfig || noData.Instrument != "AAPL" {
		t.Fatalf("no-data diag = %+v", noData)
	}
	clientID := <-diags
	if clientID.Level != DiagWarn || clientID.Kind != DiagKindConfig {
		t.Fatalf("client-id diag = %+v", clientID)
	}

	wrapper.Error(-1, 0, 502, "Couldn't connect to TWS", "")
	select {
	case err := <-wrapper.lost:
		if err == nil {
			t.Fatal("fatal error lost err = nil")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fatal lost signal")
	}
}

func TestIBWrapperSignalsLostOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapper := newIBWrapper(ctx, nil, nil, time.Now, nil, nil)

	wrapper.ConnectionClosed()
	wrapper.Error(-1, 0, 502, "Couldn't connect to TWS", "")

	select {
	case err := <-wrapper.lost:
		if err == nil {
			t.Fatal("lost err = nil")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first lost signal")
	}
	select {
	case err := <-wrapper.lost:
		t.Fatalf("second lost signal = %v", err)
	default:
	}
}

func TestNewConnectorIBRegistered(t *testing.T) {
	t.Parallel()

	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry: %v", err)
	}
	connector, err := registry.Build(domain.MarketDataInstance{
		ExternalID: testProviderExternalID("ib-1"),
		Provider:   domain.MarketDataProviderIB,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := connector.(*ibConnector); !ok {
		t.Fatalf("connector type = %T, want *ibConnector", connector)
	}
}

func TestProviderVerifiesSymbolsIBUnsupported(t *testing.T) {
	t.Parallel()

	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry: %v", err)
	}
	if registry.VerifiesSymbols(domain.MarketDataProviderIB) {
		t.Fatal("VerifiesSymbols(ib) = true, want false")
	}
	if !registry.SearchesSymbols(domain.MarketDataProviderIB) {
		t.Fatal("SearchesSymbols(ib) = false, want true")
	}
}

func ibSearchDetails(
	symbol, secType, exchange, currency, longName string,
	conID int64,
) *ibapi.ContractDetails {
	cd := &ibapi.ContractDetails{LongName: longName}
	cd.Contract.Symbol = symbol
	cd.Contract.SecType = secType
	cd.Contract.Exchange = exchange
	cd.Contract.Currency = currency
	cd.Contract.ConID = conID
	return cd
}

func TestIBConnectorSearchSymbolsTranslatesDetails(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		crypto := ibSearchDetails("BTC", "CRYPTO", "PAXOS", "USD", "Bitcoin", 12345)
		crypto.Contract.LocalSymbol = "BTC.USD"
		crypto.Contract.TradingClass = "BTC"
		client.details = []*ibapi.ContractDetails{crypto}
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "BTC", SecType: "CRYPTO"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want 1", matches)
	}
	got := matches[0]
	if got.Symbol != "BTC" || got.Name != "Bitcoin" || got.SecType != "CRYPTO" ||
		got.Exchange != "PAXOS" || got.Currency != "USD" || got.ConID != "12345" {
		t.Fatalf("match = %+v", got)
	}
	if got.LocalSymbol != "BTC.USD" || got.TradingClass != "BTC" {
		t.Fatalf("match contract specifics = %+v", got)
	}
}

func TestIBConnectorSearchSymbolsTranslatesDerivativeDetails(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		fut := ibSearchDetails("ES", "FUT", "CME", "USD", "E-mini S&P 500", 2)
		fut.Contract.LastTradeDateOrContractMonth = "20250919"
		fut.Contract.Multiplier = "50"
		client.details = []*ibapi.ContractDetails{fut}
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(),
		SymbolSearchQuery{Query: "ES", SecType: "FUT", Exchange: "CME"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want 1", matches)
	}
	got := matches[0]
	if got.LastTradeDateOrContractMonth != "20250919" || got.Multiplier != "50" {
		t.Fatalf("derivative specifics = %+v", got)
	}
}

func TestIBSymbolMatchFormatsContractNumbersAsStrings(t *testing.T) {
	t.Parallel()

	const maxConID int64 = 9223372036854775807
	cd := ibSearchDetails("OPT", "OPT", "CME", "USD", "Option", maxConID)
	cd.Contract.Strike = 0.00000001

	got := symbolMatchFromDetails(cd)
	if got.ConID != "9223372036854775807" {
		t.Fatalf("ConID = %q, want max int64 string", got.ConID)
	}
	if got.Strike != "0.00000001" {
		t.Fatalf("Strike = %q, want exact decimal string", got.Strike)
	}
}

func TestIBConnectorSearchSymbolsBuildsRequestContract(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		clients <- client
		return client
	}

	if _, err := connector.SearchSymbols(context.Background(), SymbolSearchQuery{
		Query:    "BTC",
		SecType:  "CRYPTO",
		Exchange: "PAXOS",
		Currency: "USD",
	}); err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	client := <-clients
	contract := client.searchContract()
	if contract == nil {
		t.Fatal("request contract = nil")
	}
	if contract.Symbol != "BTC" || contract.SecType != "CRYPTO" ||
		contract.Exchange != "PAXOS" || contract.Currency != "USD" {
		t.Fatalf("request contract = %+v", contract)
	}
}

func TestIBConnectorSearchSymbolsParsesDecimalStringStrike(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		clients <- client
		return client
	}

	if _, err := connector.SearchSymbols(context.Background(), SymbolSearchQuery{
		Query:    "ES",
		SecType:  "FUT",
		Exchange: "CME",
		Strike:   "0.00000001",
	}); err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	client := <-clients
	contract := client.searchContract()
	if contract == nil {
		t.Fatal("request contract = nil")
	}
	if contract.Strike != 0.00000001 {
		t.Fatalf("request strike = %.12f, want 0.00000001", contract.Strike)
	}
}

func TestIBConnectorSearchSymbolsInfersPairQuery(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		clients <- client
		return client
	}

	if _, err := connector.SearchSymbols(context.Background(), SymbolSearchQuery{
		Query: "EUR/USD",
	}); err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	client := <-clients
	contracts := client.searchContracts()
	if len(contracts) != 2 {
		t.Fatalf("request count = %d, want 2", len(contracts))
	}
	if contracts[0].Symbol != "EUR" || contracts[0].SecType != "CASH" ||
		contracts[0].Exchange != "IDEALPRO" || contracts[0].Currency != "USD" {
		t.Fatalf("first request contract = %+v", contracts[0])
	}
	if contracts[1].Symbol != "EUR" || contracts[1].SecType != "CRYPTO" ||
		contracts[1].Exchange != "PAXOS" || contracts[1].Currency != "USD" {
		t.Fatalf("second request contract = %+v", contracts[1])
	}
}

func TestIBConnectorSearchSymbolsFallbacksToStock(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		client.detailsBySecType = map[string][]*ibapi.ContractDetails{
			"STK": []*ibapi.ContractDetails{
				ibSearchDetails("AAPL", "STK", "SMART", "USD", "Apple Inc", 265598),
			},
		}
		clients <- client
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "AAPL"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(matches) != 1 || matches[0].Symbol != "AAPL" ||
		matches[0].Currency != "USD" {
		t.Fatalf("matches = %+v, want AAPL/USD", matches)
	}
	client := <-clients
	contracts := client.searchContracts()
	if len(contracts) != 3 {
		t.Fatalf("request count = %d, want 3", len(contracts))
	}
	if contracts[0].SecType != "CASH" || contracts[1].SecType != "CRYPTO" ||
		contracts[2].SecType != "STK" || contracts[2].Exchange != "SMART" {
		t.Fatalf("request contracts = %+v", contracts)
	}
}

func TestIBConnectorSearchSymbolsDefaultsToForex(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		client.detailsBySecType = map[string][]*ibapi.ContractDetails{
			"CASH": []*ibapi.ContractDetails{
				ibSearchDetails("EUR", "CASH", "IDEALPRO", "USD", "Euro", 12087792),
			},
		}
		clients <- client
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "EUR"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(matches) != 1 || matches[0].Symbol != "EUR" ||
		matches[0].SecType != "CASH" || matches[0].Currency != "USD" {
		t.Fatalf("matches = %+v, want EUR forex/USD", matches)
	}
	client := <-clients
	contracts := client.searchContracts()
	if len(contracts) != 1 {
		t.Fatalf("request count = %d, want 1", len(contracts))
	}
	if contracts[0].Symbol != "EUR" || contracts[0].SecType != "CASH" ||
		contracts[0].Exchange != "IDEALPRO" || contracts[0].Currency != "USD" {
		t.Fatalf("request contract = %+v", contracts[0])
	}
}

func TestIBConnectorSearchSymbolsFallbacksToCrypto(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		client.detailsBySecType = map[string][]*ibapi.ContractDetails{
			"CRYPTO": []*ibapi.ContractDetails{
				ibSearchDetails("BTC", "CRYPTO", "PAXOS", "USD", "Bitcoin", 12345),
			},
		}
		clients <- client
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "BTC"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(matches) != 1 || matches[0].Symbol != "BTC" ||
		matches[0].SecType != "CRYPTO" || matches[0].Currency != "USD" {
		t.Fatalf("matches = %+v, want BTC crypto/USD", matches)
	}
	client := <-clients
	contracts := client.searchContracts()
	if len(contracts) != 2 {
		t.Fatalf("request count = %d, want 2", len(contracts))
	}
	if contracts[0].SecType != "CASH" || contracts[1].SecType != "CRYPTO" ||
		contracts[1].Exchange != "PAXOS" {
		t.Fatalf("request contracts = %+v", contracts)
	}
}

func TestIBConnectorSearchSymbolsNoSecurityDefinitionReturnsEmpty(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		// IB code 200 ("No security definition has been found") must resolve to an
		// empty result, never a transport error.
		client.searchErrorCode = 200
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "NOPE", SecType: "STK"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols err = %v, want nil", err)
	}
	if matches != nil {
		t.Fatalf("matches = %+v, want nil", matches)
	}
}

func TestIBConnectorSearchSymbolsNoDetailsReturnsEmpty(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		// No details, but ContractDetailsEnd still fires: the resolve completes
		// with an empty slice rather than waiting for the timeout.
		return newFakeIBClient(wrapper)
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "AAPL", SecType: "STK"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols err = %v, want nil", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want empty", matches)
	}
}

func TestIBConnectorSearchSymbolsTimeoutReturnsEmpty(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.searchTimeout = 10 * time.Millisecond
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		// A no-op ReqContractDetails: the wrapper never receives ContractDetailsEnd,
		// so the search must fall through to its timeout.
		return &silentSearchClient{wrapper: wrapper}
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "AAPL"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols err = %v, want nil", err)
	}
	if matches != nil {
		t.Fatalf("matches = %+v, want nil", matches)
	}
}

func TestIBConnectorSearchSymbolsConnectErrorReturnsError(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		client.connectErr = errors.New("dial tcp: connection refused")
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "AAPL"},
	)
	if err == nil {
		t.Fatal("SearchSymbols err = nil, want connect error")
	}
	if matches != nil {
		t.Fatalf("matches = %+v, want nil", matches)
	}
}

func TestIBConnectorSearchSymbolsDisconnectFailurePreservesMatches(t *testing.T) {
	t.Parallel()

	disconnectErr := errors.New("disconnect failed")
	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		client.details = []*ibapi.ContractDetails{
			ibSearchDetails("AAPL", "STK", "SMART", "USD", "Apple Inc", 265598),
		}
		client.disconnectErr = disconnectErr
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "AAPL", SecType: "STK"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols error = %v, want nil", err)
	}
	if len(matches) != 1 || matches[0].Symbol != "AAPL" {
		t.Fatalf("matches = %+v, want AAPL", matches)
	}
}

func TestIBConnectorSearchSymbolsFailureJoinsDisconnectFailure(t *testing.T) {
	t.Parallel()

	searchErr := errors.New("search connect failed")
	disconnectErr := errors.New("disconnect failed")
	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		client.connectErr = searchErr
		client.disconnectErr = disconnectErr
		return client
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "AAPL", SecType: "STK"},
	)
	if matches != nil {
		t.Fatalf("matches = %+v, want nil", matches)
	}
	if !errors.Is(err, searchErr) {
		t.Fatalf("SearchSymbols error = %v, want search failure", err)
	}
	if !errors.Is(err, disconnectErr) {
		t.Fatalf("SearchSymbols error = %v, want disconnect failure", err)
	}
}

func TestIBConnectorSearchUsesDistinctClientID(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	clients := make(chan *fakeIBClient, 1)
	connector.newClient = func(wrapper ibapi.EWrapper) ibClient {
		client := newFakeIBClient(wrapper)
		clients <- client
		return client
	}

	if _, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "AAPL"},
	); err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	client := <-clients
	want := int64(7) + ibSearchClientIDOffset
	if client.clientID != want {
		t.Fatalf("search clientID = %d, want %d", client.clientID, want)
	}
	if contract := client.searchContract(); contract == nil || contract.Symbol != "AAPL" {
		t.Fatalf("search contract = %+v, want symbol AAPL", contract)
	}
}

func TestIBConnectorSearchSymbolsEmptyQuery(t *testing.T) {
	t.Parallel()

	connector := NewIBConnector("ib-search", `{"clientId":7}`)
	connector.newClient = func(ibapi.EWrapper) ibClient {
		t.Fatal("empty query must not open a connection")
		return nil
	}

	matches, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "  "},
	)
	if err != nil || matches != nil {
		t.Fatalf("SearchSymbols(empty) = (%+v, %v), want (nil, nil)", matches, err)
	}
}

// silentSearchClient is a minimal ibClient that connects and signals readiness
// but never delivers ContractDetailsEnd, exercising the search timeout path.
type silentSearchClient struct {
	wrapper ibapi.EWrapper
}

func (c *silentSearchClient) Connect(string, int, int64) error {
	c.wrapper.NextValidID(1)
	return nil
}
func (c *silentSearchClient) Disconnect() error { return nil }
func (c *silentSearchClient) ReqMktData(
	ibapi.TickerID, *ibapi.Contract, string, bool, bool, []ibapi.TagValue,
) {
}
func (c *silentSearchClient) CancelMktData(ibapi.TickerID)              {}
func (c *silentSearchClient) ReqMarketDataType(int64)                   {}
func (c *silentSearchClient) ReqContractDetails(int64, *ibapi.Contract) {}

func TestLiveIBClientForceDisconnectZeroClient(t *testing.T) {
	t.Parallel()

	client := liveIBClient{client: ibapi.NewEClient(nil)}
	if err := client.ForceDisconnect(); err != nil {
		t.Fatalf("ForceDisconnect: %v", err)
	}
}

func TestLiveIBClientConnectTimeoutClosesPartialHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	addr := listener.Addr().(*net.TCPAddr)
	connector := NewIBConnector(
		"ib-test",
		fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"clientId":109}`, addr.Port),
	)
	connector.connectTimeout = time.Millisecond
	client := liveIBClient{client: ibapi.NewEClient(nil)}

	err = connector.connect(context.Background(), client)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connect err = %v, want context deadline exceeded", err)
	}
	if client.client.IsConnected() {
		t.Fatal("client remains connected after partial-handshake timeout")
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	case err := <-acceptErr:
		t.Fatalf("Accept: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for accepted connection")
	}
}

func TestIBConnectorLiveConnect(t *testing.T) {
	if os.Getenv("OFFICER_IB_LIVE_TEST") != "1" {
		t.Skip("set OFFICER_IB_LIVE_TEST=1 to connect to a running TWS/IB Gateway")
	}
	host := envOr("OFFICER_IB_HOST", "127.0.0.1")
	port := intFromEnv(t, "OFFICER_IB_PORT", defaultIBPort)
	symbol := envOr("OFFICER_IB_SYMBOL", "AAPL")
	quote := envOr("OFFICER_IB_QUOTE", "USD")
	creds := fmt.Sprintf(
		`{"host":%q,"port":%d,"clientId":109,"marketDataType":"realtime"}`,
		host, port,
	)
	connector := NewIBConnector("ib-live", creds)
	statuses := make(chan string, 4)
	connector.SetStatusReporter(func(ok bool, errMsg string) {
		if ok {
			statuses <- "ok"
			return
		}
		statuses <- errMsg
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := connector.Subscribe(ctx, []Subscription{
		{
			External: symbol,
			Base:     testMarketDataAssetID(symbol),
			Quote:    testMarketDataAssetID(quote),
		},
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer connector.Close()

	select {
	case status := <-statuses:
		if status != "ok" {
			t.Fatalf("live connection status = %q", status)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for live IB connection: %v", ctx.Err())
	}
}

type fakeIBClient struct {
	wrapper ibapi.EWrapper
	reqCh   chan struct{}

	mu                 sync.Mutex
	disconnectOnce     sync.Once
	connectBlock       chan struct{}
	connectReturnBlock chan struct{}
	connectErr         error
	disconnectErr      error
	readyDelay         time.Duration
	requests           []fakeIBRequest
	searchReqContracts []*ibapi.Contract
	details            []*ibapi.ContractDetails
	detailsBySecType   map[string][]*ibapi.ContractDetails
	searchReqContract  *ibapi.Contract
	host               string
	marketDataType     int64
	clientID           int64
	searchErrorCode    int64
	port               int
	callsBeforeReady   int
	disconnects        int
	forceDisconnects   int
	cancels            int
	asyncReady         bool
	disconnectNoop     bool
	ready              bool
}

type fakeIBRequest struct {
	contract *ibapi.Contract
	reqID    ibapi.TickerID
}

func newFakeIBClient(wrapper ibapi.EWrapper) *fakeIBClient {
	return &fakeIBClient{wrapper: wrapper, reqCh: make(chan struct{}, 8)}
}

func (c *fakeIBClient) Connect(host string, port int, clientID int64) error {
	c.mu.Lock()
	c.host = host
	c.port = port
	c.clientID = clientID
	err := c.connectErr
	block := c.connectBlock
	returnBlock := c.connectReturnBlock
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if block != nil {
		<-block
		if returnBlock != nil {
			<-returnBlock
		}
		return context.Canceled
	}
	if c.asyncReady {
		go func() {
			if c.readyDelay > 0 {
				time.Sleep(c.readyDelay)
			}
			c.markReady()
			c.wrapper.NextValidID(1)
		}()
		return nil
	}
	c.markReady()
	c.wrapper.NextValidID(1)
	return nil
}

func (c *fakeIBClient) Disconnect() error {
	c.mu.Lock()
	c.disconnects++
	block := c.connectBlock
	noop := c.disconnectNoop
	err := c.disconnectErr
	c.mu.Unlock()
	if !noop {
		c.closeConnectBlock(block)
	}
	return err
}

func (c *fakeIBClient) ForceDisconnect() error {
	c.mu.Lock()
	c.forceDisconnects++
	block := c.connectBlock
	err := c.disconnectErr
	c.mu.Unlock()
	c.closeConnectBlock(block)
	return err
}

func (c *fakeIBClient) ReqMktData(
	reqID ibapi.TickerID,
	contract *ibapi.Contract,
	_ string,
	_ bool,
	_ bool,
	_ []ibapi.TagValue,
) {
	c.mu.Lock()
	if !c.ready {
		c.callsBeforeReady++
	}
	c.requests = append(c.requests, fakeIBRequest{reqID: reqID, contract: contract})
	c.mu.Unlock()
	c.reqCh <- struct{}{}
}

func (c *fakeIBClient) CancelMktData(ibapi.TickerID) {
	c.mu.Lock()
	c.cancels++
	c.mu.Unlock()
}

func (c *fakeIBClient) ReqMarketDataType(marketDataType int64) {
	c.mu.Lock()
	if !c.ready {
		c.callsBeforeReady++
	}
	c.marketDataType = marketDataType
	c.mu.Unlock()
}

func (c *fakeIBClient) ReqContractDetails(reqID int64, contract *ibapi.Contract) {
	c.mu.Lock()
	c.searchReqContract = contract
	c.searchReqContracts = append(c.searchReqContracts, contract)
	details := c.details
	if c.detailsBySecType != nil {
		details = c.detailsBySecType[contract.SecType]
	}
	errorCode := c.searchErrorCode
	c.mu.Unlock()
	if errorCode != 0 {
		c.wrapper.Error(ibapi.TickerID(reqID), 0, errorCode, "", "")
		return
	}
	for _, cd := range details {
		c.wrapper.ContractDetails(reqID, cd)
	}
	c.wrapper.ContractDetailsEnd(reqID)
}

func (c *fakeIBClient) searchContract() *ibapi.Contract {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.searchReqContract
}

func (c *fakeIBClient) searchContracts() []*ibapi.Contract {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*ibapi.Contract(nil), c.searchReqContracts...)
}

func (c *fakeIBClient) markReady() {
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
}

func (c *fakeIBClient) closeConnectBlock(block chan struct{}) {
	if block != nil {
		c.disconnectOnce.Do(func() { close(block) })
	}
}

func (c *fakeIBClient) waitRequests(t *testing.T, want int) {
	t.Helper()
	for {
		c.mu.Lock()
		got := len(c.requests)
		c.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-c.reqCh:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %d requests", want)
		}
	}
}

func (c *fakeIBClient) disconnectCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disconnects + c.forceDisconnects
}

func (c *fakeIBClient) forceDisconnectCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.forceDisconnects
}

func (c *fakeIBClient) cancelCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancels
}

func (c *fakeIBClient) callsBeforeReadyCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callsBeforeReady
}

func envOr(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func intFromEnv(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s=%q: %v", name, value, err)
	}
	return n
}
