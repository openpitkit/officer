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

package marketdata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/internal/backoff"
	"go.openpit.dev/officer/internal/openalgo"
	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	openAlgoHTTPTimeout      = 10 * time.Second
	openAlgoHandshakeTimeout = 10 * time.Second
	openAlgoReadTimeout      = 90 * time.Second
	openAlgoPingInterval     = 20 * time.Second
	openAlgoReconnectMin     = time.Second
	openAlgoReconnectMax     = 30 * time.Second
)

type openAlgoConnector struct {
	credentials  openalgo.Credentials
	client       *openalgo.Client
	redact       *strings.Replacer
	report       health.StatusReporter
	diagReport   health.DiagnosticReporter
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	once         sync.Once
	pingInterval time.Duration
	readTimeout  time.Duration
	sleep        func(context.Context, time.Duration) error
	subsMu       sync.Mutex
	subs         []fwmarketdata.Subscription
}

// NewOpenAlgoConnector builds an NSE/BSE Quote-mode source.
func NewOpenAlgoConnector(instance domain.MarketDataInstance) (fwmarketdata.Connector, error) {
	credentials, err := openalgo.ParseCredentials(instance.Credentials)
	if err != nil {
		return nil, err
	}
	_, redact, err := secret.CredentialRedactor(instance.Credentials, "url", "wsUrl")
	if err != nil {
		return nil, fmt.Errorf("openalgo: %w", err)
	}
	return &openAlgoConnector{
		credentials: credentials, redact: redact,
		pingInterval: openAlgoPingInterval, readTimeout: openAlgoReadTimeout,
		sleep: backoff.Sleep,
		client: openalgo.NewClient(credentials.URL, credentials.APIKey,
			&http.Client{Timeout: openAlgoHTTPTimeout}, redact),
	}, nil
}

func (c *openAlgoConnector) SetStatusReporter(report health.StatusReporter) {
	c.report = report
}

func (c *openAlgoConnector) reportStatus(ok bool, message string) {
	if c.report != nil {
		c.report(ok, c.redact.Replace(message))
	}
}

func (c *openAlgoConnector) SetDiagnosticReporter(report health.DiagnosticReporter) {
	c.diagReport = report
}

func (c *openAlgoConnector) reportDiag(diag health.Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

func (c *openAlgoConnector) References() (fwmarketdata.ProviderReferences, bool) {
	return fwmarketdata.ProviderReferences{
		DocsURL:    "https://docs.openalgo.in/api-documentation/v1/websockets",
		SymbolsURL: "https://docs.openalgo.in/symbol-format",
	}, true
}

func (c *openAlgoConnector) VerifySymbol(ctx context.Context, external string) (fwmarketdata.SymbolVerification, error) {
	exists, details, err := openalgo.VerifySpotSymbol(ctx, c.client, external)
	if err != nil {
		return fwmarketdata.SymbolVerification{}, err
	}
	return fwmarketdata.SymbolVerification{Exists: exists, Details: details}, nil
}

func (c *openAlgoConnector) SearchSymbols(ctx context.Context, query fwmarketdata.SymbolSearchQuery) ([]fwmarketdata.SymbolMatch, error) {
	rows, err := openalgo.SearchSpotSymbols(ctx, c.client, query.Query)
	if err != nil {
		return nil, err
	}
	matches := make([]fwmarketdata.SymbolMatch, 0, len(rows))
	for _, row := range rows {
		matches = append(matches, fwmarketdata.SymbolMatch{
			Symbol: row.Exchange + ":" + row.Symbol, Name: row.Name,
			SecType: row.InstrumentType, Exchange: row.Exchange,
		})
	}
	return matches, nil
}

func (c *openAlgoConnector) Subscribe(ctx context.Context, subs []fwmarketdata.Subscription) (<-chan fwmarketdata.QuoteUpdate, error) {
	byInstrument := make(map[openalgo.Instrument][]fwmarketdata.Subscription)
	var instruments []openalgo.Instrument
	for _, sub := range subs {
		instrument, err := openalgo.ParseSpotSymbol(sub.External)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", invalidExternalSymbolError("openalgo", c.redact.Replace(sub.External)), openalgo.SymbolForm)
		}
		if _, exists := byInstrument[instrument]; !exists {
			instruments = append(instruments, instrument)
		}
		byInstrument[instrument] = append(byInstrument[instrument], sub)
	}
	if len(instruments) == 0 {
		return nil, errors.New("openalgo: at least one instrument is required")
	}
	c.subsMu.Lock()
	c.subs = append([]fwmarketdata.Subscription(nil), subs...)
	c.subsMu.Unlock()
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	out := make(chan fwmarketdata.QuoteUpdate)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(out)
		c.run(runCtx, instruments, byInstrument, out)
	}()
	return out, nil
}

func (c *openAlgoConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		c.wg.Wait()
	})
}

func (c *openAlgoConnector) run(ctx context.Context, instruments []openalgo.Instrument, subs map[openalgo.Instrument][]fwmarketdata.Subscription, out chan<- fwmarketdata.QuoteUpdate) {
	attempt := 0
	for ctx.Err() == nil {
		delivered, err := c.stream(ctx, instruments, subs, out)
		if err == nil || ctx.Err() != nil {
			return
		}
		if delivered {
			attempt = 0
		}
		c.reportStatus(false, "stream error: "+err.Error())
		attempt++
		if c.sleep(ctx, backoff.Delay(attempt, openAlgoReconnectMin, openAlgoReconnectMax)) != nil {
			return
		}
	}
}

func (c *openAlgoConnector) stream(ctx context.Context, instruments []openalgo.Instrument, subs map[openalgo.Instrument][]fwmarketdata.Subscription, out chan<- fwmarketdata.QuoteUpdate) (delivered bool, err error) {
	defer func() {
		if err != nil {
			err = errors.New(c.redact.Replace(err.Error()))
		}
	}()
	handshakeCtx, cancel := context.WithTimeout(ctx, openAlgoHandshakeTimeout)
	defer cancel()
	conn, err := vendorhttp.DialStream(handshakeCtx, "openalgo", c.credentials.WSURL)
	if err != nil {
		return false, err
	}
	defer closeStream("openalgo", conn, func(err error) error {
		return errors.New(c.redact.Replace(err.Error()))
	})
	if err := openalgo.Authenticate(handshakeCtx, conn, c.credentials.APIKey, c.redact); err != nil {
		return false, err
	}
	id := uuid.NewString()
	if err := openalgo.WriteSubscribeQuotes(handshakeCtx, conn, id, instruments); err != nil {
		return false, err
	}
	payload, err := openalgo.ReadTextReply(handshakeCtx, conn, "subscription")
	if err != nil {
		return false, err
	}
	refused, err := openalgo.ParseSubscribeQuotesReply(payload, id, instruments, c.redact)
	if err != nil {
		return false, err
	}
	active := make(map[openalgo.Instrument][]fwmarketdata.Subscription, len(subs))
	for instrument, subscriptions := range subs {
		active[instrument] = subscriptions
	}
	for _, refusal := range refused {
		delete(active, refusal.Instrument)
		diag := health.Diagnostic{
			Level:       health.DiagError,
			Code:        health.CodeNoData,
			Kind:        health.DiagKindProvider,
			Instrument:  c.redact.Replace(refusal.String()),
			Title:       "OpenAlgo refused quotes for this instrument",
			Detail:      "OpenAlgo refused the quote subscription, so Officer receives no quotes for this instrument until the stream reconnects or feeds restart.",
			Remediation: "Check the symbol and the broker's market-data access in OpenAlgo, use Verify symbol on this instrument, then Restart feeds.",
			Actions:     []health.DiagnosticAction{{Type: health.ActionRestart}, {Type: health.ActionOpenDocs}},
		}
		if strings.TrimSpace(refusal.Message) != "" {
			diag.Detail += " OpenAlgo: " + refusal.Message
		}
		c.reportDiag(diag)
	}
	if len(active) == 0 {
		return false, errors.New("openalgo: all quote subscriptions were refused")
	}
	cancel()
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	pingErr := make(chan error, 1)
	go pingLoop(sessionCtx, c.pingInterval, func(ctx context.Context) error {
		return openalgo.WriteStreamFrame(ctx, conn, struct {
			Action string `json:"action"`
		}{"ping"})
	}, pingErr)
	reads := websocketReadLoop(sessionCtx, func(ctx context.Context) ([]byte, error) {
		typ, payload, err := conn.Read(ctx)
		if typ != websocket.MessageText && err == nil {
			return nil, nil
		}
		return payload, err
	})
	var unparsable unparsableTracker
	c.reportStatus(true, "")
	for {
		payload, err := nextWebsocketRead(ctx, reads, c.readTimeout, pingErr)
		if err != nil {
			return delivered, err
		}
		if payload == nil {
			unparsable.recordUnparsed(c.reportDiag)
			continue
		}
		quote, err := openalgo.ParseMarketData(payload, c.redact)
		if errors.Is(err, openalgo.ErrStreamPong) {
			continue
		}
		if err != nil {
			var streamErr *openalgo.StreamError
			if errors.As(err, &streamErr) {
				return delivered, err
			}
			unparsable.recordUnparsed(c.reportDiag)
			continue
		}
		unparsable.recordParsed()
		subscriptions, exists := active[quote.Instrument]
		if !exists {
			c.reportDiag(health.Diagnostic{
				Level:       health.DiagWarn,
				Code:        health.CodeInconsistentData,
				Kind:        health.DiagKindProvider,
				Instrument:  c.redact.Replace(quote.String()),
				Title:       "OpenAlgo sent quotes for an instrument it was not asked for",
				Detail:      "OpenAlgo's quote stream sent market data for an instrument this source did not subscribe to; Officer ignored it.",
				Remediation: "This is an issue in OpenAlgo or its broker adapter, not your configuration. Restart feeds, and report it if it repeats.",
				Actions:     []health.DiagnosticAction{{Type: health.ActionRestart}},
			})
			continue
		}
		for _, sub := range subscriptions {
			update := fwmarketdata.QuoteUpdate{
				AsOf: quote.Timestamp, Base: sub.Base, Quote: sub.Quote, Mark: string(quote.LTP),
			}
			select {
			case <-ctx.Done():
				return delivered, ctx.Err()
			case out <- update:
				delivered = true
			}
		}
	}
}

// ToleratesSilence reports that NSE/BSE trade only in session hours.
func (c *openAlgoConnector) ToleratesSilence() bool { return true }

// Diagnose checks whether OpenAlgo knows the subscribed spot instruments.
func (c *openAlgoConnector) Diagnose(ctx context.Context) ([]health.Diagnostic, error) {
	c.subsMu.Lock()
	subs := append([]fwmarketdata.Subscription(nil), c.subs...)
	c.subsMu.Unlock()
	var findings []health.Diagnostic
	for _, sub := range subs {
		exists, _, err := openalgo.VerifySpotSymbol(ctx, c.client, sub.External)
		if err != nil {
			return nil, errors.New(c.redact.Replace(err.Error()))
		}
		if !exists {
			findings = append(findings, providerUnknownSymbolDiag("OpenAlgo", c.redact.Replace(sub.External)))
		}
	}
	return findings, nil
}
