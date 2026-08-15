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

package main

import (
	"context"
	"sync"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

const privateProviderID = "example-private"

func privateProvider() marketdata.Provider {
	return marketdata.Provider{
		Type:  privateProviderID,
		Title: "Example private provider",
		Build: func(domain.MarketDataInstance) (marketdata.Connector, error) {
			return newPrivateConnector(), nil
		},
	}
}

func newPrivateConnector() *privateConnector {
	return &privateConnector{stop: make(chan struct{})}
}

// privateConnector streams one quote and then stays subscribed. Its stop
// channel is what Close signals, so the constructor is mandatory.
type privateConnector struct {
	stop      chan struct{}
	closeOnce sync.Once
}

func (c *privateConnector) Subscribe(
	ctx context.Context,
	subs []marketdata.Subscription,
) (<-chan marketdata.QuoteUpdate, error) {
	ch := make(chan marketdata.QuoteUpdate, 1)
	if len(subs) == 0 {
		// An empty subscription has nothing to stream.
		close(ch)
		return ch, nil
	}
	update := marketdata.QuoteUpdate{
		AsOf:  time.Unix(1, 0).UTC(),
		Base:  subs[0].Base,
		Quote: subs[0].Quote,
		Mark:  "185.00",
	}
	go func() {
		defer close(ch)

		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case ch <- update:
		}

		select {
		case <-ctx.Done():
		case <-c.stop:
		}
	}()
	return ch, nil
}

func (c *privateConnector) Close() {
	c.closeOnce.Do(func() {
		close(c.stop)
	})
}

func (*privateConnector) References() (marketdata.ProviderReferences, bool) {
	return marketdata.ProviderReferences{
		DocsURL:    "https://openpit.dev/docs/examples/private-provider",
		SymbolsURL: "https://openpit.dev/docs/examples/private-symbols",
	}, true
}
