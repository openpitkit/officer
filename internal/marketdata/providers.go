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
	"go.openpit.dev/officer/framework/domain"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
)

// FirstPartyProviders returns the providers the default composition registers,
// in registration order.
func FirstPartyProviders() []fwmarketdata.Provider {
	return []fwmarketdata.Provider{
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
	}
}

// IBProvider describes the Interactive Brokers connector.
func IBProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:            domain.MarketDataProviderIB,
		Title:           "Interactive Brokers",
		SearchesSymbols: true,
		Build: func(instance domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			connector := NewIBConnector(instance.ExternalID.String(), instance.Credentials)
			if connector.configErr != nil {
				return nil, connector.configErr
			}
			return connector, nil
		},
	}
}

// BinanceProvider describes the Binance connector.
func BinanceProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:            domain.MarketDataProviderBinance,
		Title:           "Binance",
		VerifiesSymbols: true,
		SearchesSymbols: true,
		Build: func(domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewBinanceConnector(), nil
		},
	}
}

// KrakenProvider describes the Kraken connector.
func KrakenProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:            domain.MarketDataProviderKraken,
		Title:           "Kraken",
		VerifiesSymbols: true,
		SearchesSymbols: true,
		Build: func(domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewKrakenConnector(), nil
		},
	}
}

// CoinbaseProvider describes the Coinbase connector.
func CoinbaseProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:            domain.MarketDataProviderCoinbase,
		Title:           "Coinbase",
		VerifiesSymbols: true,
		SearchesSymbols: true,
		Build: func(domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewCoinbaseConnector(), nil
		},
	}
}

// AlpacaProvider describes the Alpaca connector.
func AlpacaProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:  domain.MarketDataProviderAlpaca,
		Title: "Alpaca",
		Build: func(instance domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewAlpacaConnector(instance)
		},
	}
}

// OKXProvider describes the OKX connector.
func OKXProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:            domain.MarketDataProviderOKX,
		Title:           "OKX",
		VerifiesSymbols: true,
		SearchesSymbols: true,
		Build: func(domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewOKXConnector(), nil
		},
	}
}

// BybitProvider describes the Bybit connector.
func BybitProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:            domain.MarketDataProviderBybit,
		Title:           "Bybit",
		VerifiesSymbols: true,
		SearchesSymbols: true,
		Build: func(instance domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewBybitConnector(instance.Credentials)
		},
	}
}

// OANDAProvider describes the OANDA connector.
func OANDAProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:  domain.MarketDataProviderOANDA,
		Title: "OANDA",
		Build: func(instance domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewOANDAConnector(instance)
		},
	}
}

// FinnhubProvider describes the Finnhub connector.
func FinnhubProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:            domain.MarketDataProviderFinnhub,
		Title:           "Finnhub",
		VerifiesSymbols: true,
		SearchesSymbols: true,
		Build: func(instance domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewFinnhubConnector(instance)
		},
	}
}

// BYOProvider describes the bring-your-own quote connector.
func BYOProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:  domain.MarketDataProviderBYO,
		Title: "BYO",
		Build: func(domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewBYOConnector(0), nil
		},
	}
}

// MockProvider describes the mock connector.
func MockProvider() fwmarketdata.Provider {
	return fwmarketdata.Provider{
		Type:  domain.MarketDataProviderMock,
		Title: "Mock",
		Build: func(domain.MarketDataInstance) (fwmarketdata.Connector, error) {
			return NewMockConnector(0), nil
		},
	}
}
