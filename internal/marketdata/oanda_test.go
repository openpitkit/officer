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
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func TestParseOANDACredentials(t *testing.T) {
	t.Parallel()

	connector, err := NewOANDAConnector(domain.MarketDataInstance{
		Credentials: `{"token":" token ","accountID":" acc ","environment":"practice"}`,
	})
	if err != nil {
		t.Fatalf("NewOANDAConnector: %v", err)
	}
	if connector.credentials.Token != "token" {
		t.Fatalf("token = %q, want token", connector.credentials.Token)
	}
	if connector.credentials.AccountID != "acc" {
		t.Fatalf("accountID = %q, want acc", connector.credentials.AccountID)
	}
	if connector.credentials.Environment != "practice" {
		t.Fatalf("environment = %q, want practice", connector.credentials.Environment)
	}
}

func TestParseOANDACredentialsDefaultsPractice(t *testing.T) {
	t.Parallel()

	connector, err := NewOANDAConnector(domain.MarketDataInstance{
		Credentials: `{"token":"token","accountID":"acc"}`,
	})
	if err != nil {
		t.Fatalf("NewOANDAConnector: %v", err)
	}
	if connector.credentials.Environment != "practice" {
		t.Fatalf("environment = %q, want practice", connector.credentials.Environment)
	}
}

func TestParseOANDACredentialsRejectsInvalidEnvironment(t *testing.T) {
	t.Parallel()

	if _, err := NewOANDAConnector(domain.MarketDataInstance{
		Credentials: `{"token":"token","accountID":"acc","environment":"paper"}`,
	}); err == nil {
		t.Fatal("NewOANDAConnector err = nil, want error")
	}
}

func TestOANDAPricingStreamURL(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
		{External: "GBP_USD", Base: "GBP", Quote: "USD"},
	})
	got := oandaPricingStreamURL(oandaCredentials{
		AccountID:   "101-001-123",
		Environment: "live",
	}, subs)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if parsed.Scheme+"://"+parsed.Host != oandaLiveStreamBaseURL {
		t.Fatalf("host = %s://%s", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/v3/accounts/101-001-123/pricing/stream" {
		t.Fatalf("path = %q", parsed.Path)
	}
	if parsed.Query().Get("instruments") != "EUR_USD,GBP_USD" {
		t.Fatalf("instruments = %q", parsed.Query().Get("instruments"))
	}
}

func TestParseOANDAQuoteUpdate(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	payload := []byte(`{
		"type":"PRICE",
		"instrument":"EUR_USD",
		"time":"2026-06-20T10:11:12.123456789Z",
		"bids":[{"price":"1.09123"}],
		"asks":[{"price":"1.09125"}]
	}`)

	update, ok := parseOANDAQuoteUpdate(payload, subs)
	if !ok {
		t.Fatal("parseOANDAQuoteUpdate returned ok=false")
	}
	if update.Bid != "1.09123" || update.Ask != "1.09125" {
		t.Fatalf("update = %+v", update)
	}
	if !update.AsOf.Equal(time.Date(2026, 6, 20, 10, 11, 12, 123456789, time.UTC)) {
		t.Fatalf("AsOf = %s", update.AsOf)
	}
}

func TestParseOANDAQuoteUpdateSkipsHeartbeat(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	if _, ok := parseOANDAQuoteUpdate([]byte(`{"type":"HEARTBEAT"}`), subs); ok {
		t.Fatal("parseOANDAQuoteUpdate heartbeat ok=true, want false")
	}
}

func TestParseOANDAQuoteUpdateSkipsEmptyPrice(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	payload := []byte(`{
		"type":"PRICE",
		"instrument":"EUR_USD",
		"time":"2026-06-20T10:11:12Z",
		"bids":[],
		"asks":[]
	}`)
	if _, ok := parseOANDAQuoteUpdate(payload, subs); ok {
		t.Fatal("parseOANDAQuoteUpdate empty PRICE ok=true, want false")
	}
}

func TestOANDAConnector_AllowsLongPriceLine(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	line := `{"type":"PRICE","instrument":"EUR_USD","time":"2026-06-20T10:11:12Z","bids":[{"price":"1"}],"asks":[{"price":"1.1"}],"extra":"` +
		strings.Repeat("x", oandaScannerInitialSize+1024) + `"}`
	connector := &oandaConnector{
		stream: func(context.Context, string, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(line + "\n")), nil
		},
		credentials: oandaCredentials{
			Token:       "token",
			AccountID:   "acc",
			Environment: "practice",
		},
	}

	out := make(chan QuoteUpdate, 1)
	delivered, err := connector.streamQuotes(context.Background(), subs, out)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("streamQuotes err = %v, want EOF", err)
	}
	if !delivered {
		t.Fatal("streamQuotes delivered = false, want true")
	}
	update := <-out
	if update.Bid != "1" || update.Ask != "1.1" {
		t.Fatalf("update = %+v", update)
	}
}

func TestOANDAConnector_SubscribeCloseLifecycle(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	var startOnce sync.Once
	var body *blockingOANDABody
	connector := &oandaConnector{
		stream: func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
			startOnce.Do(func() { close(started) })
			body = &blockingOANDABody{
				ctx: ctx,
				line: []byte(
					`{"type":"PRICE","instrument":"EUR_USD","time":"2026-06-20T10:11:12Z","bids":[{"price":"1"}],"asks":[{"price":"1.1"}]}` + "\n",
				),
				closed: make(chan struct{}),
			}
			return body, nil
		},
		credentials: oandaCredentials{
			Token:       "token",
			AccountID:   "acc",
			Environment: "practice",
		},
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream did not start")
	}
	var update QuoteUpdate
	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed before first quote")
		}
		update = got
	case <-time.After(time.Second):
		t.Fatal("subscription did not deliver first quote")
	}
	if update.Bid != "1" || update.Ask != "1.1" ||
		update.Base != "EUR" || update.Quote != "USD" {
		t.Fatalf("update = %+v", update)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				connector.Close()
			}()
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not stop Subscribe goroutine")
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscription channel is still open after Close")
	}
	if body == nil {
		t.Fatal("stream body was not created")
	}
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("stream body was not closed")
	}
}

func TestStreamOANDANonOKClosesBody(t *testing.T) {
	oldClient := oandaHTTPClient
	defer func() { oandaHTTPClient = oldClient }()

	const wantMsg = "Insufficient authorization to perform request."
	body := &trackingReadCloser{
		data: []byte(`{"errorMessage":"` + wantMsg + `"}`),
	}
	var gotAuth string
	oandaHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotAuth = req.Header.Get("Authorization")
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       body,
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	stream, err := streamOANDA(context.Background(), "https://example.test/stream", "token")
	if err == nil {
		if stream != nil {
			_ = stream.Close()
		}
		t.Fatal("streamOANDA err = nil, want non-OK error")
	}
	if !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("err = %v, want it to contain %q", err, wantMsg)
	}
	if gotAuth != "Bearer token" {
		t.Fatalf("Authorization = %q, want Bearer token", gotAuth)
	}
	if !body.closed {
		t.Fatal("non-OK response body was not closed")
	}
}

func TestOANDAConnector_ReconnectsWithBackoff(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	first := io.NopCloser(strings.NewReader(strings.Join([]string{
		`{"type":"PRICE","instrument":"EUR_USD","time":"2026-06-20T10:11:12Z","bids":[{"price":"1"}],"asks":[{"price":"1.1"}]}`,
		``,
	}, "\n")))
	second := io.NopCloser(strings.NewReader(strings.Join([]string{
		`{"type":"PRICE","instrument":"EUR_USD","time":"2026-06-20T10:11:13Z","bids":[{"price":"2"}],"asks":[{"price":"2.1"}]}`,
		``,
	}, "\n")))

	var (
		mu       sync.Mutex
		attempts int
		urls     []string
		tokens   []string
	)
	connector := &oandaConnector{
		stream: func(_ context.Context, streamURL, token string) (io.ReadCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			urls = append(urls, streamURL)
			tokens = append(tokens, token)
			if attempts == 1 {
				return first, nil
			}
			if attempts == 2 {
				return second, nil
			}
			return nil, context.Canceled
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		credentials: oandaCredentials{
			Token:       "token",
			AccountID:   "acc",
			Environment: "practice",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan QuoteUpdate)
	done := make(chan struct{})
	go func() {
		defer close(done)
		connector.run(ctx, subs, ch)
		close(ch)
	}()

	got := make([]QuoteUpdate, 0, 2)
	for update := range ch {
		got = append(got, update)
		if len(got) == 2 {
			cancel()
		}
	}
	<-done

	if len(got) != 2 || got[0].Bid != "1" || got[1].Bid != "2" {
		t.Fatalf("updates = %+v", got)
	}
	if len(urls) < 2 {
		t.Fatalf("len(urls) = %d, want at least 2", len(urls))
	}
	if urls[0] != urls[1] {
		t.Fatalf("urls = %v, want same resubscribe URL", urls)
	}
	if len(tokens) < 2 || tokens[0] != "token" || tokens[1] != "token" {
		t.Fatalf("tokens = %v", tokens)
	}
}

func TestOANDAConnector_RetriesStreamOpenError(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	wantErr := errors.New("temporary")

	var attempts int
	connector := &oandaConnector{
		stream: func(context.Context, string, string) (io.ReadCloser, error) {
			attempts++
			if attempts == 1 {
				return nil, wantErr
			}
			return nil, context.Canceled
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		credentials: oandaCredentials{
			Token:       "token",
			AccountID:   "acc",
			Environment: "practice",
		},
	}

	ch := make(chan QuoteUpdate)
	connector.run(context.Background(), subs, ch)
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestOANDAConnector_IdleTimeoutClosesStream(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	var body *blockingOANDABody
	connector := &oandaConnector{
		stream: func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
			body = &blockingOANDABody{
				ctx: ctx,
				line: []byte(
					`{"type":"PRICE","instrument":"EUR_USD","time":"2026-06-20T10:11:12Z","bids":[{"price":"1"}],"asks":[{"price":"1.1"}]}` + "\n",
				),
				closed: make(chan struct{}),
			}
			return body, nil
		},
		idleTimeout: 20 * time.Millisecond,
		credentials: oandaCredentials{
			Token:       "token",
			AccountID:   "acc",
			Environment: "practice",
		},
	}

	out := make(chan QuoteUpdate, 1)
	delivered, err := connector.streamQuotes(context.Background(), subs, out)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("streamQuotes err = %v, want non-nil non-EOF idle error", err)
	}
	if !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("err = %v, want idle timeout", err)
	}
	if !delivered {
		t.Fatal("streamQuotes delivered = false, want true")
	}
	if body == nil {
		t.Fatal("stream body was not created")
	}
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("idle watchdog did not close the body")
	}
}

func TestOANDAConnector_ResetsBackoffAfterRead(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOANDASubscriptions(t, []Subscription{
		{External: "EUR_USD", Base: "EUR", Quote: "USD"},
	})
	first := io.NopCloser(strings.NewReader(
		`{"type":"PRICE","instrument":"EUR_USD","time":"2026-06-20T10:11:12Z","bids":[{"price":"1"}],"asks":[{"price":"1.1"}]}`,
	))
	second := io.NopCloser(strings.NewReader(
		`{"type":"PRICE","instrument":"EUR_USD","time":"2026-06-20T10:11:13Z","bids":[{"price":"2"}],"asks":[{"price":"2.1"}]}`,
	))

	var (
		mu       sync.Mutex
		attempts int
		delays   []time.Duration
	)
	connector := &oandaConnector{
		stream: func(context.Context, string, string) (io.ReadCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			switch attempts {
			case 1:
				return first, nil
			case 2:
				return second, nil
			default:
				return nil, context.Canceled
			}
		},
		sleep: func(_ context.Context, delay time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			delays = append(delays, delay)
			return nil
		},
		reconnectMin: time.Millisecond,
		reconnectMax: 8 * time.Millisecond,
		credentials: oandaCredentials{
			Token:       "token",
			AccountID:   "acc",
			Environment: "practice",
		},
	}

	ch := make(chan QuoteUpdate)
	go func() {
		connector.run(context.Background(), subs, ch)
		close(ch)
	}()

	var got []QuoteUpdate
	for update := range ch {
		got = append(got, update)
	}

	if len(got) != 2 || got[0].Bid != "1" || got[1].Bid != "2" {
		t.Fatalf("updates = %+v", got)
	}
	// Each attempt delivered a quote, so backoff reset to min before every sleep.
	if len(delays) != 2 {
		t.Fatalf("delays = %v, want two reconnect sleeps", delays)
	}
	for i, delay := range delays {
		if delay != time.Millisecond {
			t.Fatalf("delay[%d] = %s, want %s", i, delay, time.Millisecond)
		}
	}
}

func mustNormalizeOANDASubscriptions(
	t *testing.T, subs []Subscription,
) []oandaSubscription {
	t.Helper()
	normalized, err := normalizeOANDASubscriptions(subs)
	if err != nil {
		t.Fatalf("normalizeOANDASubscriptions: %v", err)
	}
	return normalized
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadCloser struct {
	data   []byte
	offset int
	closed bool
}

func (b *trackingReadCloser) Read(p []byte) (int, error) {
	if b.offset >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.offset:])
	b.offset += n
	return n, nil
}

func (b *trackingReadCloser) Close() error {
	b.closed = true
	return nil
}

type blockingOANDABody struct {
	ctx       context.Context
	closeOnce sync.Once
	closed    chan struct{}
	line      []byte
	sent      bool
}

func (b *blockingOANDABody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.line), nil
	}
	// Block until the context is cancelled or the body is closed (e.g. by the
	// idle watchdog). Either unblocks the scanner.
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.closed:
		return 0, io.EOF
	}
}

func (b *blockingOANDABody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}
