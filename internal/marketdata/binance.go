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
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	binanceStreamBaseURL = "wss://data-stream.binance.vision/stream"

	defaultBinanceReconnectMin = 250 * time.Millisecond
	defaultBinanceReconnectMax = 5 * time.Second
)

type binanceConnector struct {
	dial         func(context.Context, string) (binanceConn, error)
	sleep        func(context.Context, time.Duration) error
	reconnectMin time.Duration
	reconnectMax time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type binanceConn interface {
	Read(context.Context) ([]byte, error)
	Close(websocket.StatusCode, string) error
}

type liveBinanceConn struct {
	conn *websocket.Conn
}

type binanceSubscription struct {
	Subscription
	symbol string
	stream string
}

type binanceCombinedMessage struct {
	Data binanceTickerEvent `json:"data"`
}

type binanceTickerEvent struct {
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
	Last      string `json:"c"`
	Bid       string `json:"b"`
	Ask       string `json:"a"`
}

func NewBinanceConnector() *binanceConnector {
	return &binanceConnector{
		dial:         dialBinance,
		sleep:        sleepContext,
		reconnectMin: defaultBinanceReconnectMin,
		reconnectMax: defaultBinanceReconnectMax,
	}
}

func dialBinance(ctx context.Context, streamURL string) (binanceConn, error) {
	conn, _, err := websocket.Dial(ctx, streamURL, nil)
	if err != nil {
		return nil, err
	}
	return liveBinanceConn{conn: conn}, nil
}

func (c liveBinanceConn) Read(ctx context.Context) ([]byte, error) {
	_, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func (c liveBinanceConn) Close(code websocket.StatusCode, reason string) error {
	return c.conn.Close(code, reason)
}

func (c *binanceConnector) Subscribe(
	ctx context.Context, subs []Subscription,
) (<-chan QuoteUpdate, error) {
	normalized, err := normalizeBinanceSubscriptions(subs)
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

func (c *binanceConnector) run(
	ctx context.Context, subs []binanceSubscription, out chan<- QuoteUpdate,
) {
	attempt := 0
	for {
		err := c.stream(ctx, subs, out)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}

		attempt++
		if err := c.sleep(ctx, backoff(attempt, c.reconnectMin, c.reconnectMax)); err != nil {
			return
		}
	}
}

func (c *binanceConnector) stream(
	ctx context.Context, subs []binanceSubscription, out chan<- QuoteUpdate,
) error {
	conn, err := c.dial(ctx, binanceStreamURL(subs))
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	for {
		payload, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		update, ok := parseBinanceQuoteUpdate(payload, subs)
		if !ok {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- update:
		}
	}
}

func (c *binanceConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}

func normalizeBinanceSubscriptions(
	subs []Subscription,
) ([]binanceSubscription, error) {
	normalized := make([]binanceSubscription, 0, len(subs))
	for _, sub := range subs {
		symbol := strings.TrimSpace(sub.External)
		if symbol == "" {
			symbol = strings.ToUpper(strings.TrimSpace(sub.Base) + strings.TrimSpace(sub.Quote))
		} else {
			symbol = strings.ToUpper(symbol)
		}
		if symbol == "" {
			return nil, fmt.Errorf("binance subscription %s/%s: empty symbol", sub.Base, sub.Quote)
		}
		normalized = append(normalized, binanceSubscription{
			Subscription: sub,
			symbol:       symbol,
			stream:       strings.ToLower(symbol) + "@ticker",
		})
	}
	return normalized, nil
}

func binanceStreamURL(subs []binanceSubscription) string {
	streams := make([]string, 0, len(subs))
	for _, sub := range subs {
		streams = append(streams, sub.stream)
	}
	slices.Sort(streams)
	query := url.Values{}
	query.Set("streams", strings.Join(streams, "/"))
	return binanceStreamBaseURL + "?" + query.Encode()
}

func parseBinanceQuoteUpdate(
	payload []byte, subs []binanceSubscription,
) (QuoteUpdate, bool) {
	var message binanceCombinedMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return QuoteUpdate{}, false
	}
	return quoteUpdateFromBinanceEvent(message.Data, subs)
}

func quoteUpdateFromBinanceEvent(
	event binanceTickerEvent, subs []binanceSubscription,
) (QuoteUpdate, bool) {
	if event.EventTime <= 0 {
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
		return QuoteUpdate{
			AsOf:  time.UnixMilli(event.EventTime).UTC(),
			Base:  sub.Base,
			Quote: sub.Quote,
			Mark:  event.Last,
			Bid:   event.Bid,
			Ask:   event.Ask,
		}, true
	}
	return QuoteUpdate{}, false
}

func backoff(attempt int, minDelay, maxDelay time.Duration) time.Duration {
	if attempt <= 0 {
		return minDelay
	}
	delay := minDelay
	for range attempt - 1 {
		if delay >= maxDelay/2 {
			return maxDelay
		}
		delay *= 2
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
