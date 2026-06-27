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
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/domain"
)

const (
	alpacaIEXStreamURL = "wss://stream.data.alpaca.markets/v2/iex"
	alpacaSymbolLimit  = 30

	defaultAlpacaReconnectMin = 250 * time.Millisecond
	defaultAlpacaReconnectMax = 5 * time.Second
	defaultAlpacaReadTimeout  = 60 * time.Second
)

type alpacaConnector struct {
	dial         func(context.Context, string) (alpacaConn, error)
	sleep        func(context.Context, time.Duration) error
	report       StatusReporter
	diagReport   DiagnosticReporter
	reconnectMin time.Duration
	reconnectMax time.Duration
	readTimeout  time.Duration
	credentials  alpacaCredentials

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type alpacaConn interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close(websocket.StatusCode, string) error
}

type liveAlpacaConn struct {
	conn *websocket.Conn
}

type alpacaCredentials struct {
	APIKey    string
	APISecret string
}

type alpacaSubscription struct {
	Subscription
	symbol string
}

type alpacaFrame struct {
	Action string   `json:"action,omitempty"`
	Key    string   `json:"key,omitempty"`
	Secret string   `json:"secret,omitempty"`
	Quotes []string `json:"quotes,omitempty"`
	Trades []string `json:"trades,omitempty"`
}

type alpacaEvent struct {
	Timestamp time.Time
	Type      string
	Symbol    string
	Last      string
	Bid       string
	Ask       string
}

type alpacaControlEvent struct {
	Type string `json:"T"`
	Msg  string `json:"msg"`
	Code int    `json:"code"`
}

// NewAlpacaConnector builds an Alpaca IEX streaming connector from instance
// credentials.
func NewAlpacaConnector(
	instance domain.MarketDataInstance,
) (*alpacaConnector, error) {
	credentials, err := parseAlpacaCredentials(instance.Credentials)
	if err != nil {
		return nil, err
	}
	return &alpacaConnector{
		dial:         dialAlpaca,
		sleep:        sleepContext,
		reconnectMin: defaultAlpacaReconnectMin,
		reconnectMax: defaultAlpacaReconnectMax,
		readTimeout:  defaultAlpacaReadTimeout,
		credentials:  credentials,
	}, nil
}

func parseAlpacaCredentials(raw string) (alpacaCredentials, error) {
	var payload struct {
		APIKey    string `json:"apiKey"`
		APISecret string `json:"apiSecret"`
		Key       string `json:"key"`
		Secret    string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return alpacaCredentials{}, fmt.Errorf("alpaca credentials: %w", err)
	}
	credentials := alpacaCredentials{
		APIKey:    strings.TrimSpace(payload.APIKey),
		APISecret: strings.TrimSpace(payload.APISecret),
	}
	if credentials.APIKey == "" {
		credentials.APIKey = strings.TrimSpace(payload.Key)
	}
	if credentials.APISecret == "" {
		credentials.APISecret = strings.TrimSpace(payload.Secret)
	}
	if credentials.APIKey == "" || credentials.APISecret == "" {
		return alpacaCredentials{}, errors.New("alpaca credentials: apiKey and apiSecret are required")
	}
	return credentials, nil
}

func dialAlpaca(ctx context.Context, streamURL string) (alpacaConn, error) {
	conn, _, err := websocket.Dial(ctx, streamURL, nil)
	if err != nil {
		return nil, err
	}
	return liveAlpacaConn{conn: conn}, nil
}

func (c liveAlpacaConn) Read(ctx context.Context) ([]byte, error) {
	_, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func (c liveAlpacaConn) Write(ctx context.Context, payload []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func (c liveAlpacaConn) Close(code websocket.StatusCode, reason string) error {
	return c.conn.Close(code, reason)
}

// SetStatusReporter installs the manager's runtime reporter.
func (c *alpacaConnector) SetStatusReporter(report StatusReporter) {
	c.report = report
}

func (c *alpacaConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

// SetDiagnosticReporter installs the manager's structured diagnostic reporter.
func (c *alpacaConnector) SetDiagnosticReporter(report DiagnosticReporter) {
	c.diagReport = report
}

func (c *alpacaConnector) reportDiag(diag Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

// References returns the Alpaca API documentation and asset-list URLs.
func (c *alpacaConnector) References() (ProviderReferences, bool) {
	return ProviderReferences{
		DocsURL:    "https://docs.alpaca.markets/docs/real-time-stock-pricing-data",
		SymbolsURL: "https://docs.alpaca.markets/docs/assets-1",
	}, true
}

func (c *alpacaConnector) Subscribe(
	ctx context.Context, subs []Subscription,
) (<-chan QuoteUpdate, error) {
	normalized, err := normalizeAlpacaSubscriptions(subs)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	out := make(chan QuoteUpdate)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(out)
		c.run(runCtx, normalized, out)
	}()
	return out, nil
}

func (c *alpacaConnector) run(
	ctx context.Context, subs []alpacaSubscription, out chan<- QuoteUpdate,
) {
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

func (c *alpacaConnector) stream(
	ctx context.Context, subs []alpacaSubscription, out chan<- QuoteUpdate,
) (bool, error) {
	conn, err := c.dial(ctx, alpacaIEXStreamURL)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	if err := writeAlpacaAuth(ctx, conn, c.credentials); err != nil {
		return false, err
	}
	if err := c.waitForAuth(ctx, conn); err != nil {
		return false, err
	}
	if err := writeAlpacaSubscribe(ctx, conn, subs); err != nil {
		return false, err
	}
	c.reportStatus(true, "")

	var (
		unparsable unparsableTracker
		delivered  bool
	)
	for {
		payload, err := c.read(ctx, conn)
		if err != nil {
			return delivered, err
		}
		if msg, ok := alpacaControlError(payload); ok {
			c.reportStatus(false, "alpaca: "+msg)
			continue
		}
		if _, ok := alpacaControlFrames(payload); ok {
			continue
		}
		updates := parseAlpacaQuoteUpdates(payload, subs)
		if len(updates) == 0 {
			unparsable.recordUnparsed(c.reportDiag)
			continue
		}
		unparsable.recordParsed()
		delivered = true
		for _, update := range updates {
			select {
			case <-ctx.Done():
				return delivered, ctx.Err()
			case out <- update:
			}
		}
	}
}

func (c *alpacaConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}

func (c *alpacaConnector) read(
	ctx context.Context, conn alpacaConn,
) ([]byte, error) {
	if c.readTimeout <= 0 {
		return conn.Read(ctx)
	}
	readCtx, cancel := context.WithTimeout(ctx, c.readTimeout)
	defer cancel()
	payload, err := conn.Read(readCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("alpaca read timeout after %s", c.readTimeout)
	}
	return payload, err
}

func (c *alpacaConnector) waitForAuth(
	ctx context.Context, conn alpacaConn,
) error {
	for {
		payload, err := c.read(ctx, conn)
		if err != nil {
			return err
		}
		done, err, ok := alpacaAuthResult(payload)
		if err != nil {
			return err
		}
		if ok && done {
			return nil
		}
	}
}

func normalizeAlpacaSubscriptions(
	subs []Subscription,
) ([]alpacaSubscription, error) {
	if len(subs) > alpacaSymbolLimit {
		return nil, fmt.Errorf("alpaca subscription limit exceeded: %d > %d", len(subs), alpacaSymbolLimit)
	}
	normalized := make([]alpacaSubscription, 0, len(subs))
	for _, sub := range subs {
		symbol := strings.TrimSpace(sub.External)
		if symbol == "" {
			symbol = strings.TrimSpace(sub.Base)
		}
		symbol = strings.ToUpper(symbol)
		if symbol == "" {
			return nil, fmt.Errorf("alpaca subscription %s/%s: empty symbol", sub.Base, sub.Quote)
		}
		normalized = append(normalized, alpacaSubscription{
			Subscription: sub,
			symbol:       symbol,
		})
	}
	return normalized, nil
}

func writeAlpacaAuth(
	ctx context.Context, conn alpacaConn, credentials alpacaCredentials,
) error {
	payload, err := json.Marshal(alpacaFrame{
		Action: "auth",
		Key:    credentials.APIKey,
		Secret: credentials.APISecret,
	})
	if err != nil {
		return err
	}
	return conn.Write(ctx, payload)
}

func writeAlpacaSubscribe(
	ctx context.Context, conn alpacaConn, subs []alpacaSubscription,
) error {
	symbols := alpacaSymbols(subs)
	payload, err := json.Marshal(alpacaFrame{
		Action: "subscribe",
		Quotes: symbols,
		Trades: symbols,
	})
	if err != nil {
		return err
	}
	return conn.Write(ctx, payload)
}

func alpacaSymbols(subs []alpacaSubscription) []string {
	symbols := make([]string, 0, len(subs))
	for _, sub := range subs {
		symbols = append(symbols, sub.symbol)
	}
	return symbols
}

func parseAlpacaQuoteUpdates(
	payload []byte, subs []alpacaSubscription,
) []QuoteUpdate {
	var rawEvents []map[string]json.RawMessage
	if err := json.Unmarshal(payload, &rawEvents); err != nil {
		return nil
	}

	updates := make([]QuoteUpdate, 0, len(rawEvents))
	for _, rawEvent := range rawEvents {
		event := alpacaEvent{
			Type:      rawString(rawEvent["T"]),
			Symbol:    rawString(rawEvent["S"]),
			Timestamp: rawTime(rawEvent["t"]),
			Last:      rawDecimalString(rawEvent["p"]),
			Bid:       rawDecimalString(rawEvent["bp"]),
			Ask:       rawDecimalString(rawEvent["ap"]),
		}
		if update, ok := quoteUpdateFromAlpacaEvent(event, subs); ok {
			updates = append(updates, update)
		}
	}
	return updates
}

func alpacaControlFrames(payload []byte) ([]alpacaControlEvent, bool) {
	var frames []alpacaControlEvent
	if err := json.Unmarshal(payload, &frames); err != nil || len(frames) == 0 {
		return nil, false
	}
	for _, frame := range frames {
		if frame.Type == "success" || frame.Type == "error" || frame.Type == "subscription" {
			return frames, true
		}
	}
	return nil, false
}

func alpacaAuthResult(payload []byte) (done bool, err error, ok bool) {
	frames, ok := alpacaControlFrames(payload)
	if !ok {
		return false, nil, false
	}
	for _, frame := range frames {
		switch frame.Type {
		case "error":
			return false, fmt.Errorf("alpaca auth: %s", alpacaControlMessage(frame)), true
		case "success":
			msg := strings.ToLower(strings.TrimSpace(frame.Msg))
			if strings.Contains(msg, "auth") {
				return true, nil, true
			}
		}
	}
	return false, nil, true
}

func alpacaControlError(payload []byte) (string, bool) {
	frames, ok := alpacaControlFrames(payload)
	if !ok {
		return "", false
	}
	for _, frame := range frames {
		if frame.Type == "error" {
			return alpacaControlMessage(frame), true
		}
	}
	return "", false
}

func alpacaControlMessage(frame alpacaControlEvent) string {
	msg := strings.TrimSpace(frame.Msg)
	if msg == "" {
		msg = "request rejected"
	}
	if frame.Code != 0 {
		msg = fmt.Sprintf("%d: %s", frame.Code, msg)
	}
	return msg
}

func quoteUpdateFromAlpacaEvent(
	event alpacaEvent, subs []alpacaSubscription,
) (QuoteUpdate, bool) {
	if event.Timestamp.IsZero() {
		return QuoteUpdate{}, false
	}
	symbol := strings.ToUpper(strings.TrimSpace(event.Symbol))
	if symbol == "" {
		return QuoteUpdate{}, false
	}

	for _, sub := range subs {
		if sub.symbol != symbol {
			continue
		}
		switch event.Type {
		case "q":
			return QuoteUpdate{
				AsOf:  event.Timestamp.UTC(),
				Base:  sub.Base,
				Quote: sub.Quote,
				Bid:   event.Bid,
				Ask:   event.Ask,
			}, true
		case "t":
			return QuoteUpdate{
				AsOf:  event.Timestamp.UTC(),
				Base:  sub.Base,
				Quote: sub.Quote,
				Mark:  event.Last,
			}, true
		default:
			return QuoteUpdate{}, false
		}
	}
	return QuoteUpdate{}, false
}

func rawTime(raw json.RawMessage) time.Time {
	value := rawString(raw)
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
