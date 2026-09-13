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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
)

const (
	okxPublicFeedURL   = "wss://ws.okx.com:8443/ws/v5/public"
	okxInstrumentsURL  = "https://www.okx.com/api/v5/public/instruments?instType=SPOT"
	okxDiagnoseTimeout = 10 * time.Second

	defaultOKXReconnectMin = 250 * time.Millisecond
	defaultOKXReconnectMax = 5 * time.Second
	defaultOKXPingInterval = 25 * time.Second
)

type okxConnector struct {
	dial         func(context.Context, string) (okxConn, error)
	sleep        func(context.Context, time.Duration) error
	fetchSymbols func(context.Context) (map[string]struct{}, error)
	report       fwmarketdata.StatusReporter
	diagReport   fwmarketdata.DiagnosticReporter
	reconnectMin time.Duration
	reconnectMax time.Duration
	pingInterval time.Duration
	readTimeout  time.Duration

	subs []okxSubscription

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type okxConn interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close(websocket.StatusCode, string) error
}

type liveOKXConn struct {
	conn *websocket.Conn
}

type okxSubscription struct {
	fwmarketdata.Subscription
	instID string
}

type okxSubscribeRequest struct {
	Op   string       `json:"op"`
	Args []okxChannel `json:"args"`
}

type okxChannel struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

type okxTickerData struct {
	InstID string `json:"instId"`
	Last   string `json:"last"`
	BidPx  string `json:"bidPx"`
	AskPx  string `json:"askPx"`
	TS     string `json:"ts"`
}

var _ fwmarketdata.Connector = (*okxConnector)(nil)
var _ fwmarketdata.SymbolVerifier = (*okxConnector)(nil)
var _ fwmarketdata.Diagnosable = (*okxConnector)(nil)

// NewOKXConnector builds an OKX public ticker-stream connector.
func NewOKXConnector() *okxConnector {
	return &okxConnector{
		dial:         dialOKX,
		sleep:        sleepContext,
		fetchSymbols: fetchOKXSymbols,
		reconnectMin: defaultOKXReconnectMin,
		reconnectMax: defaultOKXReconnectMax,
		pingInterval: defaultOKXPingInterval,
		readTimeout:  defaultWebsocketReadTimeout,
	}
}

func dialOKX(ctx context.Context, feedURL string) (okxConn, error) {
	conn, _, err := websocket.Dial(ctx, feedURL, nil)
	if err != nil {
		return nil, err
	}
	return liveOKXConn{conn: conn}, nil
}

func (c liveOKXConn) Read(ctx context.Context) ([]byte, error) {
	_, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func (c liveOKXConn) Write(ctx context.Context, payload []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func (c liveOKXConn) Close(code websocket.StatusCode, reason string) error {
	return c.conn.Close(code, reason)
}

// SetStatusReporter installs the manager's runtime reporter.
func (c *okxConnector) SetStatusReporter(report fwmarketdata.StatusReporter) {
	c.report = report
}

func (c *okxConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

// SetDiagnosticReporter installs the manager's structured diagnostic reporter.
func (c *okxConnector) SetDiagnosticReporter(report fwmarketdata.DiagnosticReporter) {
	c.diagReport = report
}

func (c *okxConnector) reportDiag(diag fwmarketdata.Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

// References returns the OKX API documentation and instrument-list URLs.
func (c *okxConnector) References() (fwmarketdata.ProviderReferences, bool) {
	return fwmarketdata.ProviderReferences{
		DocsURL:    "https://www.okx.com/docs-v5/en/#websocket-api-public-channel-tickers-channel",
		SymbolsURL: okxInstrumentsURL,
	}, true
}

// VerifySymbol checks external against the OKX spot-instrument catalogue.
func (c *okxConnector) VerifySymbol(
	ctx context.Context, external string,
) (fwmarketdata.SymbolVerification, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, okxDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(verifyCtx)
	if err != nil {
		return fwmarketdata.SymbolVerification{}, err
	}
	return verifySymbolFromSet(known, external), nil
}

func (c *okxConnector) SearchSymbols(
	ctx context.Context, query fwmarketdata.SymbolSearchQuery,
) ([]fwmarketdata.SymbolMatch, error) {
	searchCtx, cancel := context.WithTimeout(ctx, okxDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(searchCtx)
	if err != nil {
		return nil, err
	}
	return searchSymbolsFromSet(known, query, "SPOT"), nil
}

// Diagnose returns a diagnostic for each stored OKX subscription whose
// instrument id is not listed by the provider catalogue.
func (c *okxConnector) Diagnose(ctx context.Context) ([]fwmarketdata.Diagnostic, error) {
	diagCtx, cancel := context.WithTimeout(ctx, okxDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(diagCtx)
	if err != nil {
		return nil, err
	}
	var findings []fwmarketdata.Diagnostic
	for _, sub := range c.subs {
		if _, ok := known[sub.instID]; ok {
			continue
		}
		findings = append(findings, providerUnknownSymbolDiag(
			"OKX",
			sub.External,
		))
	}
	return findings, nil
}

func (c *okxConnector) Subscribe(
	ctx context.Context, subs []fwmarketdata.Subscription,
) (<-chan fwmarketdata.QuoteUpdate, error) {
	normalized, err := normalizeOKXSubscriptions(subs)
	if err != nil {
		return nil, err
	}
	c.subs = normalized

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

func (c *okxConnector) run(
	ctx context.Context, subs []okxSubscription, out chan<- fwmarketdata.QuoteUpdate,
) {
	subs = c.validateSymbols(ctx, subs)
	if len(subs) == 0 {
		c.reportStatus(false, "okx: no valid symbols")
		return
	}
	attempt := 0
	for {
		delivered, err := c.stream(ctx, subs, out)
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

func (c *okxConnector) validateSymbols(
	ctx context.Context, subs []okxSubscription,
) []okxSubscription {
	if c.fetchSymbols == nil {
		return subs
	}
	validateCtx, cancel := context.WithTimeout(ctx, okxDiagnoseTimeout)
	defer cancel()

	known, err := c.fetchSymbols(validateCtx)
	if err != nil {
		c.reportDiag(fwmarketdata.Diagnostic{
			Level:       DiagWarn,
			Code:        CodeSelfDiagnosisFailed,
			Kind:        DiagKindProvider,
			Title:       "Self-diagnosis failed",
			Detail:      "symbol validation unavailable: " + err.Error(),
			Remediation: "Couldn't validate against the provider; try Restart feeds.",
			Actions:     []fwmarketdata.DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
		})
		return subs
	}

	valid := subs[:0:0]
	for _, sub := range subs {
		if _, ok := known[sub.instID]; ok {
			valid = append(valid, sub)
			continue
		}
		c.reportDiag(providerUnknownSymbolDiag(
			"OKX",
			sub.External,
		))
	}
	return valid
}

func (c *okxConnector) stream(
	ctx context.Context, subs []okxSubscription, out chan<- fwmarketdata.QuoteUpdate,
) (bool, error) {
	conn, err := c.dial(ctx, okxPublicFeedURL)
	if err != nil {
		return false, err
	}
	c.reportStatus(true, "")
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	payload, err := okxSubscribePayload(subs)
	if err != nil {
		return false, err
	}
	if err := conn.Write(ctx, payload); err != nil {
		return false, err
	}

	pingCtx, cancelPing := context.WithCancel(ctx)
	defer cancelPing()
	pingErr := make(chan error, 1)
	go okxPingLoop(pingCtx, conn, c.pingInterval, pingErr)

	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	reads := websocketReadLoop(readCtx, conn.Read)
	var (
		unparsable unparsableTracker
		delivered  bool
	)

	for {
		payload, err := nextWebsocketRead(ctx, reads, c.readTimeout, pingErr)
		if err != nil {
			return delivered, err
		}
		if string(payload) == "pong" {
			continue
		}
		if okxControlAck(payload) {
			continue
		}
		if msg, ok := okxControlError(payload); ok {
			c.reportStatus(false, "okx: "+msg)
			continue
		}
		update, ok := parseOKXQuoteUpdate(payload, subs)
		if !ok {
			unparsable.recordUnparsed(c.reportDiag)
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
}

func (c *okxConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}

func normalizeOKXSubscriptions(subs []fwmarketdata.Subscription) ([]okxSubscription, error) {
	normalized := make([]okxSubscription, 0, len(subs))
	for _, sub := range subs {
		instID := strings.TrimSpace(sub.External)
		if instID == "" {
			return nil, missingExternalSymbolError("okx", sub)
		}
		instID = strings.ToUpper(instID)
		if strings.Trim(instID, "-") == "" {
			return nil, invalidExternalSymbolError("okx", sub.External)
		}
		normalized = append(normalized, okxSubscription{
			Subscription: sub,
			instID:       instID,
		})
	}
	return normalized, nil
}

func okxSubscribePayload(subs []okxSubscription) ([]byte, error) {
	args := make([]okxChannel, 0, len(subs))
	for _, sub := range subs {
		args = append(args, okxChannel{Channel: "tickers", InstID: sub.instID})
	}
	return json.Marshal(okxSubscribeRequest{Op: "subscribe", Args: args})
}

func okxPingLoop(
	ctx context.Context,
	conn okxConn,
	interval time.Duration,
	errs chan<- error,
) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.Write(ctx, []byte("ping")); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}
}

type okxInstrumentsResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		InstID string `json:"instId"`
	} `json:"data"`
}

func fetchOKXSymbols(ctx context.Context) (map[string]struct{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, okxInstrumentsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("okx instruments: unexpected status %d", resp.StatusCode)
	}
	var payload okxInstrumentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("okx instruments: decode: %w", err)
	}
	if payload.Code != "" && payload.Code != "0" {
		msg := strings.TrimSpace(payload.Msg)
		if msg == "" {
			msg = "request failed"
		}
		return nil, fmt.Errorf("okx instruments: %s: %s", payload.Code, msg)
	}
	set := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		if instID := strings.TrimSpace(item.InstID); instID != "" {
			set[instID] = struct{}{}
		}
	}
	return set, nil
}

func parseOKXQuoteUpdate(
	payload []byte, subs []okxSubscription,
) (fwmarketdata.QuoteUpdate, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return fwmarketdata.QuoteUpdate{}, false
	}
	var arg map[string]json.RawMessage
	if err := json.Unmarshal(fields["arg"], &arg); err != nil {
		return fwmarketdata.QuoteUpdate{}, false
	}
	if rawString(arg["channel"]) != "tickers" {
		return fwmarketdata.QuoteUpdate{}, false
	}
	var data []json.RawMessage
	if err := json.Unmarshal(fields["data"], &data); err != nil || len(data) == 0 {
		return fwmarketdata.QuoteUpdate{}, false
	}
	for _, rawEvent := range data {
		var eventFields map[string]json.RawMessage
		if err := json.Unmarshal(rawEvent, &eventFields); err != nil {
			continue
		}
		event := okxTickerData{
			InstID: rawString(eventFields["instId"]),
			Last:   rawString(eventFields["last"]),
			BidPx:  rawString(eventFields["bidPx"]),
			AskPx:  rawString(eventFields["askPx"]),
			TS:     rawString(eventFields["ts"]),
		}
		if event.InstID == "" {
			event.InstID = rawString(arg["instId"])
		}
		update, ok := quoteUpdateFromOKXEvent(event, subs)
		if ok {
			return update, true
		}
	}
	return fwmarketdata.QuoteUpdate{}, false
}

func okxControlError(payload []byte) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return "", false
	}
	if rawString(fields["event"]) != "error" {
		return "", false
	}
	msg := rawString(fields["msg"])
	if msg == "" {
		msg = "request rejected"
	}
	if code := rawString(fields["code"]); code != "" {
		msg = code + ": " + msg
	}
	return msg, true
}

func okxControlAck(payload []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return false
	}
	switch rawString(fields["event"]) {
	case "subscribe", "unsubscribe":
		return true
	default:
		return false
	}
}

func quoteUpdateFromOKXEvent(
	event okxTickerData, subs []okxSubscription,
) (fwmarketdata.QuoteUpdate, bool) {
	ts, err := parseMillis(event.TS)
	if err != nil {
		return fwmarketdata.QuoteUpdate{}, false
	}
	instID := strings.ToUpper(strings.TrimSpace(event.InstID))
	if instID == "" {
		return fwmarketdata.QuoteUpdate{}, false
	}
	if event.Last == "" && event.BidPx == "" && event.AskPx == "" {
		return fwmarketdata.QuoteUpdate{}, false
	}
	for _, sub := range subs {
		if sub.instID != instID {
			continue
		}
		return fwmarketdata.QuoteUpdate{
			AsOf:  time.UnixMilli(ts).UTC(),
			Base:  sub.Base,
			Quote: sub.Quote,
			Mark:  event.Last,
			Bid:   event.BidPx,
			Ask:   event.AskPx,
		}, true
	}
	return fwmarketdata.QuoteUpdate{}, false
}

func parseMillis(value string) (int64, error) {
	ts, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, err
	}
	if ts <= 0 {
		return 0, fmt.Errorf("invalid timestamp %q", value)
	}
	return ts, nil
}
