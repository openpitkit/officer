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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
)

const (
	oandaPracticeStreamBaseURL = "https://stream-fxpractice.oanda.com"
	oandaLiveStreamBaseURL     = "https://stream-fxtrade.oanda.com"

	defaultOANDAReconnectMin = 250 * time.Millisecond
	defaultOANDAReconnectMax = 5 * time.Second
	oandaScannerInitialSize  = 64 * 1024
	oandaScannerMaxSize      = 1024 * 1024

	// OANDA emits a HEARTBEAT every ~5s; treat ~3 missed beats as a dead stream.
	defaultOANDAIdleTimeout = 15 * time.Second

	// Bound on the OANDA error body read on non-200 responses.
	oandaErrorBodyLimit = 2 * 1024
)

var oandaHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: time.Second,
	},
}

type oandaConnector struct {
	stream       func(context.Context, string, string) (io.ReadCloser, error)
	sleep        func(context.Context, time.Duration) error
	report       fwmarketdata.StatusReporter
	diagReport   fwmarketdata.DiagnosticReporter
	reconnectMin time.Duration
	reconnectMax time.Duration
	idleTimeout  time.Duration
	credentials  oandaCredentials

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type oandaCredentials struct {
	Token       string
	AccountID   string
	Environment string
}

type oandaSubscription struct {
	fwmarketdata.Subscription
	instrument string
}

// oandaPriceEvent holds price fields extracted by exact JSON keys. That keeps
// the adapter aligned with Binance's case-twin defense instead of relying on
// Go's case-insensitive struct matching.
type oandaPriceEvent struct {
	Time       string
	Instrument string
	Bid        string
	Ask        string
}

// NewOANDAConnector builds an OANDA v20 pricing-stream connector from instance
// credentials.
func NewOANDAConnector(
	instance domain.MarketDataInstance,
) (*oandaConnector, error) {
	credentials, err := parseOANDACredentials(instance.Credentials)
	if err != nil {
		return nil, err
	}
	return &oandaConnector{
		stream:       streamOANDA,
		sleep:        sleepContext,
		reconnectMin: defaultOANDAReconnectMin,
		reconnectMax: defaultOANDAReconnectMax,
		idleTimeout:  defaultOANDAIdleTimeout,
		credentials:  credentials,
	}, nil
}

func parseOANDACredentials(raw string) (oandaCredentials, error) {
	var payload struct {
		Token       string `json:"token"`
		AccountID   string `json:"accountID"`
		Environment string `json:"environment"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return oandaCredentials{}, fmt.Errorf("oanda credentials: %w", err)
	}
	credentials := oandaCredentials{
		Token:       strings.TrimSpace(payload.Token),
		AccountID:   strings.TrimSpace(payload.AccountID),
		Environment: strings.ToLower(strings.TrimSpace(payload.Environment)),
	}
	if credentials.Environment == "" {
		credentials.Environment = "practice"
	}
	if credentials.Environment != "practice" && credentials.Environment != "live" {
		return oandaCredentials{}, errors.New("oanda credentials: environment must be practice or live")
	}
	if credentials.Token == "" || credentials.AccountID == "" {
		return oandaCredentials{}, errors.New("oanda credentials: token and accountID are required")
	}
	return credentials, nil
}

func streamOANDA(
	ctx context.Context, streamURL, token string,
) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := oandaHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// Surface OANDA's JSON errorMessage (bad token/accountID/environment).
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, oandaErrorBodyLimit))
		_ = resp.Body.Close()
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			return nil, fmt.Errorf("oanda pricing stream: unexpected status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("oanda pricing stream: unexpected status %d: %s", resp.StatusCode, msg)
	}
	return resp.Body, nil
}

// SetStatusReporter installs the manager's runtime reporter.
func (c *oandaConnector) SetStatusReporter(report fwmarketdata.StatusReporter) {
	c.report = report
}

func (c *oandaConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

// SetDiagnosticReporter installs the manager's structured diagnostic reporter.
func (c *oandaConnector) SetDiagnosticReporter(report fwmarketdata.DiagnosticReporter) {
	c.diagReport = report
}

func (c *oandaConnector) reportDiag(diag fwmarketdata.Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

// References returns the OANDA API documentation and instrument-list URLs.
func (c *oandaConnector) References() (fwmarketdata.ProviderReferences, bool) {
	return fwmarketdata.ProviderReferences{
		DocsURL:    "https://developer.oanda.com/rest-live-v20/pricing-ep/",
		SymbolsURL: "https://developer.oanda.com/rest-live-v20/instrument-df/",
	}, true
}

func (c *oandaConnector) Subscribe(
	ctx context.Context, subs []fwmarketdata.Subscription,
) (<-chan fwmarketdata.QuoteUpdate, error) {
	normalized, err := normalizeOANDASubscriptions(subs)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	out := make(chan fwmarketdata.QuoteUpdate)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(out)
		c.run(runCtx, normalized, out)
	}()
	return out, nil
}

func (c *oandaConnector) run(
	ctx context.Context, subs []oandaSubscription, out chan<- fwmarketdata.QuoteUpdate,
) {
	attempt := 0
	for {
		delivered, err := c.streamQuotes(ctx, subs, out)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		if delivered {
			attempt = 0
		}
		c.reportStatus(false, "stream error: "+err.Error())
		attempt++
		if err := c.sleep(ctx, backoff(attempt, c.reconnectMin, c.reconnectMax)); err != nil {
			return
		}
	}
}

// streamQuotes reads the pricing stream until error/EOF. The bool reports
// whether at least one QuoteUpdate was delivered this attempt, so run can reset
// reconnect backoff after a healthy connection.
func (c *oandaConnector) streamQuotes(
	ctx context.Context, subs []oandaSubscription, out chan<- fwmarketdata.QuoteUpdate,
) (bool, error) {
	delivered := false
	streamURL := oandaPricingStreamURL(c.credentials, subs)
	body, err := c.stream(ctx, streamURL, c.credentials.Token)
	if err != nil {
		return delivered, err
	}
	c.reportStatus(true, "")
	defer func() { _ = body.Close() }()

	// Liveness watchdog: OANDA's ~5s heartbeats keep the stream alive, so a
	// half-open TCP connection is detected when no line (quote OR heartbeat)
	// arrives within idleTimeout. The timer closes body to unblock the scanner;
	// idled distinguishes that from a real EOF/ctx-cancel afterwards. Disabled
	// when idleTimeout <= 0 (behavior then identical to no watchdog).
	var idled atomic.Bool
	var timer *time.Timer
	if c.idleTimeout > 0 {
		timer = time.AfterFunc(c.idleTimeout, func() {
			idled.Store(true)
			_ = body.Close()
		})
		defer timer.Stop()
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, oandaScannerInitialSize), oandaScannerMaxSize)
	var unparsable unparsableTracker
	for scanner.Scan() {
		if c.idleTimeout > 0 {
			// Any scanned line (heartbeat included) proves liveness.
			timer.Reset(c.idleTimeout)
		}
		select {
		case <-ctx.Done():
			return delivered, ctx.Err()
		default:
		}
		payload := scanner.Bytes()
		update, ok := parseOANDAQuoteUpdate(payload, subs)
		if !ok {
			if !isOANDAHeartbeat(payload) {
				unparsable.recordUnparsed(c.reportDiag)
			}
			continue
		}
		unparsable.recordParsed()
		delivered = true
		select {
		case <-ctx.Done():
			return delivered, ctx.Err()
		case out <- update:
		}
	}
	if idled.Load() {
		return delivered, errors.New("oanda: pricing stream idle timeout")
	}
	if err := scanner.Err(); err != nil {
		return delivered, err
	}
	return delivered, io.EOF
}

func (c *oandaConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}

func normalizeOANDASubscriptions(
	subs []fwmarketdata.Subscription,
) ([]oandaSubscription, error) {
	normalized := make([]oandaSubscription, 0, len(subs))
	for _, sub := range subs {
		instrument := strings.TrimSpace(sub.External)
		if instrument == "" {
			return nil, missingExternalSymbolError("oanda", sub)
		}
		instrument = strings.ToUpper(instrument)
		if instrument == "" || instrument == "_" {
			return nil, invalidExternalSymbolError("oanda", sub.External)
		}
		normalized = append(normalized, oandaSubscription{
			Subscription: sub,
			instrument:   instrument,
		})
	}
	return normalized, nil
}

func oandaPricingStreamURL(
	credentials oandaCredentials, subs []oandaSubscription,
) string {
	baseURL := oandaPracticeStreamBaseURL
	if credentials.Environment == "live" {
		baseURL = oandaLiveStreamBaseURL
	}
	instruments := make([]string, 0, len(subs))
	for _, sub := range subs {
		instruments = append(instruments, sub.instrument)
	}
	values := url.Values{}
	values.Set("instruments", strings.Join(instruments, ","))
	return fmt.Sprintf(
		"%s/v3/accounts/%s/pricing/stream?%s",
		baseURL,
		url.PathEscape(credentials.AccountID),
		values.Encode(),
	)
}

func parseOANDAQuoteUpdate(
	payload []byte, subs []oandaSubscription,
) (fwmarketdata.QuoteUpdate, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return fwmarketdata.QuoteUpdate{}, false
	}
	if rawString(fields["type"]) != "PRICE" {
		return fwmarketdata.QuoteUpdate{}, false
	}
	event := oandaPriceEvent{
		Time:       rawString(fields["time"]),
		Instrument: rawString(fields["instrument"]),
		Bid:        firstOANDAPrice(fields["bids"]),
		Ask:        firstOANDAPrice(fields["asks"]),
	}
	if event.Bid == "" && event.Ask == "" {
		return fwmarketdata.QuoteUpdate{}, false
	}
	asOf, err := time.Parse(time.RFC3339Nano, event.Time)
	if err != nil {
		return fwmarketdata.QuoteUpdate{}, false
	}
	instrument := strings.ToUpper(strings.TrimSpace(event.Instrument))
	if instrument == "" {
		return fwmarketdata.QuoteUpdate{}, false
	}

	for _, sub := range subs {
		if sub.instrument != instrument {
			continue
		}
		return fwmarketdata.QuoteUpdate{
			AsOf:  asOf.UTC(),
			Base:  sub.Base,
			Quote: sub.Quote,
			Bid:   event.Bid,
			Ask:   event.Ask,
		}, true
	}
	return fwmarketdata.QuoteUpdate{}, false
}

func firstOANDAPrice(raw json.RawMessage) string {
	var levels []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &levels); err != nil {
		return ""
	}
	if len(levels) == 0 {
		return ""
	}
	return rawString(levels[0]["price"])
}

func isOANDAHeartbeat(payload []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return false
	}
	return rawString(fields["type"]) == "HEARTBEAT"
}
