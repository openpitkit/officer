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
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
)

func TestFirstPartyRegistryOrderTitlesAndCapabilities(t *testing.T) {
	t.Parallel()

	registry := firstPartyRegistry(t)
	want := []struct {
		typ      string
		title    string
		verifies bool
		searches bool
	}{
		{domain.MarketDataProviderIB, "Interactive Brokers", false, true},
		{domain.MarketDataProviderBinance, "Binance", true, true},
		{domain.MarketDataProviderKraken, "Kraken", true, true},
		{domain.MarketDataProviderCoinbase, "Coinbase", true, true},
		{domain.MarketDataProviderAlpaca, "Alpaca", false, false},
		{domain.MarketDataProviderOKX, "OKX", true, true},
		{domain.MarketDataProviderBybit, "Bybit", true, true},
		{domain.MarketDataProviderOANDA, "OANDA", false, false},
		{domain.MarketDataProviderFinnhub, "Finnhub", true, true},
		{domain.MarketDataProviderBYO, "BYO", false, false},
		{domain.MarketDataProviderMock, "Mock", false, false},
	}
	got := registry.List()
	if len(got) != len(want) {
		t.Fatalf("providers = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, wantProvider := range want {
		gotProvider := got[i]
		if gotProvider.Type != wantProvider.typ || gotProvider.Title != wantProvider.title {
			t.Fatalf("provider[%d] = %s/%s, want %s/%s",
				i, gotProvider.Type, gotProvider.Title,
				wantProvider.typ, wantProvider.title)
		}
		if registry.VerifiesSymbols(wantProvider.typ) != wantProvider.verifies {
			t.Fatalf("VerifiesSymbols(%s) = %v, want %v",
				wantProvider.typ, registry.VerifiesSymbols(wantProvider.typ),
				wantProvider.verifies)
		}
		if registry.SearchesSymbols(wantProvider.typ) != wantProvider.searches {
			t.Fatalf("SearchesSymbols(%s) = %v, want %v",
				wantProvider.typ, registry.SearchesSymbols(wantProvider.typ),
				wantProvider.searches)
		}
		if title, ok := registry.Title(wantProvider.typ); !ok || title != wantProvider.title {
			t.Fatalf("Title(%s) = %q/%v, want %q/true",
				wantProvider.typ, title, ok, wantProvider.title)
		}
		if !registry.Known(wantProvider.typ) {
			t.Fatalf("Known(%s) = false", wantProvider.typ)
		}
	}
	if registry.Known("nope") || registry.VerifiesSymbols("nope") ||
		registry.SearchesSymbols("nope") {
		t.Fatalf("unknown provider reported known/capable")
	}
}

func TestFirstPartyRegistryBuildsFirstPartyConnectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		instance domain.MarketDataInstance
		wantType any
	}{
		{
			name:     "ib",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderIB, Credentials: `{"clientId":109}`},
			wantType: (*ibConnector)(nil),
		},
		{name: "binance", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderBinance}, wantType: (*binanceConnector)(nil)},
		{name: "kraken", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderKraken}, wantType: (*krakenConnector)(nil)},
		{name: "coinbase", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderCoinbase}, wantType: (*coinbaseConnector)(nil)},
		{
			name:     "alpaca",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderAlpaca, Credentials: `{"apiKey":"key","apiSecret":"secret"}`},
			wantType: (*alpacaConnector)(nil),
		},
		{name: "okx", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderOKX}, wantType: (*okxConnector)(nil)},
		{
			name:     "bybit",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderBybit, Credentials: `{"category":"linear"}`},
			wantType: (*bybitConnector)(nil),
		},
		{
			name:     "oanda",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderOANDA, Credentials: `{"token":"token","accountID":"account","environment":"practice"}`},
			wantType: (*oandaConnector)(nil),
		},
		{
			name:     "finnhub",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderFinnhub, Credentials: `{"token":"token"}`},
			wantType: (*finnhubConnector)(nil),
		},
		{name: "byo", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderBYO}, wantType: (*byoConnector)(nil)},
		{name: "mock", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderMock}, wantType: (*mockConnector)(nil)},
	}
	registry := firstPartyRegistry(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.instance.ExternalID = testProviderExternalID(tt.name + "-1")
			connector, err := registry.Build(tt.instance)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer connector.Close()
			if reflect.TypeOf(connector) != reflect.TypeOf(tt.wantType) {
				t.Fatalf("connector type = %T, want %T", connector, tt.wantType)
			}
		})
	}
}

func TestFirstPartyRegistryCapabilitiesMatchConnectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		instance domain.MarketDataInstance
	}{
		{name: "ib", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderIB, Credentials: `{"clientId":109}`}},
		{name: "binance", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderBinance}},
		{name: "kraken", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderKraken}},
		{name: "coinbase", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderCoinbase}},
		{name: "alpaca", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderAlpaca, Credentials: `{"apiKey":"key","apiSecret":"secret"}`}},
		{name: "okx", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderOKX}},
		{name: "bybit", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderBybit, Credentials: `{"category":"linear"}`}},
		{name: "oanda", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderOANDA, Credentials: `{"token":"token","accountID":"account","environment":"practice"}`}},
		{name: "finnhub", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderFinnhub, Credentials: `{"token":"token"}`}},
		{name: "byo", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderBYO}},
		{name: "mock", instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderMock}},
	}
	registry := firstPartyRegistry(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.instance.ExternalID = testProviderExternalID(tt.name + "-1")
			provider, ok := registry.Lookup(tt.instance.Provider)
			if !ok {
				t.Fatalf("Lookup(%s) = false", tt.instance.Provider)
			}
			connector, err := registry.Build(tt.instance)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer connector.Close()

			_, verifies := connector.(fwmarketdata.SymbolVerifier)
			if provider.VerifiesSymbols != verifies {
				t.Fatalf("%s VerifiesSymbols = %v, connector implements = %v",
					provider.Type, provider.VerifiesSymbols, verifies)
			}
			_, searches := connector.(fwmarketdata.SymbolSearcher)
			if provider.SearchesSymbols != searches {
				t.Fatalf("%s SearchesSymbols = %v, connector implements = %v",
					provider.Type, provider.SearchesSymbols, searches)
			}
		})
	}
}

func testProviderExternalID(label string) domain.ExternalID {
	return domain.ExternalID(label)
}

// firstPartyRegistry registers the first-party providers explicitly, in the
// order the Officer composition registers them.
func firstPartyRegistry(t *testing.T) *fwmarketdata.Registry {
	t.Helper()
	registry := fwmarketdata.NewRegistry()
	for _, provider := range []fwmarketdata.Provider{
		IBProvider(),
		BinanceProvider(),
		KrakenProvider(),
		CoinbaseProvider(),
		AlpacaProvider(),
		OKXProvider(),
		BybitProvider(),
		OANDAProvider(),
		FinnhubProvider(),
		BYOProvider(),
		MockProvider(),
	} {
		if err := registry.Register(provider); err != nil {
			t.Fatalf("register provider %s: %v", provider.Type, err)
		}
	}
	return registry
}
