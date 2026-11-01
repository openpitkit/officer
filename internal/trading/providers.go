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

// Package trading implements first-party trading venue connectors.
package trading

import (
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

// FirstPartyProviders returns the built-in trading factories.
func FirstPartyProviders() []fwtrading.Provider {
	return []fwtrading.Provider{AlpacaProvider(), BinanceProvider(), IBProvider(), EtoroProvider()}
}

// IBProvider trades stocks, spot FX and crypto through TWS/IB Gateway.
// Settings require host and port, with an optional clientId. Route JSON is
// {"timeInForce":"day"|"gtc"|"ioc"}. Symbols are plain SMART stock symbols or
// BASE.QUOTE on IDEALPRO/PAXOS. Account checks read managed accounts and reject
// a paper/live mismatch. Each order selects a managed VenueAccount. Pre-check
// resolves exactly one contract using the mapping's assets and refuses volume
// orders and crypto market buys by quantity. Executions wait for their
// commission-and-fees reports and carry the reported fee currency. Lookup
// reads open/completed orders and executions, which IB limits to since local
// midnight; missing acknowledged orders or earlier executions stay pending
// with a diagnostic. Fees travel on fills, never through a separate FeeReader.
func IBProvider() fwtrading.Provider {
	return fwtrading.Provider{
		Type: domain.TradingProviderIB, Title: "Interactive Brokers",
		VenueAccounts: true, VerifiesSymbols: true, SearchesSymbols: true,
		UnlinkedFees: false, Build: NewIBConnector,
	}
}

// AlpacaProvider trades one account per key pair and verifies venue symbols.
//
// One API key pair identifies one account; VenueAccount is empty. Paper mode
// selects the paper host and live mode the live host. Route JSON is
// {"timeInForce": ...}; symbols are names such as AAPL, verified through
// GET /v2/assets/{symbol}. Account check reads GET /v2/account for the account
// id (identity) and status. Symbol search requests GET /v2/assets twice, with
// status=active and asset_class=us_equity or asset_class=crypto, then searches
// their union by exact symbol, symbol prefix and name substring,
// case-insensitively, with each group sorted by symbol and at most 50 matches.
// Pre-check requires an active, tradable asset, fractionable assets and day TIF
// for fractional quantities, and valid limit price increments. Fills carry no
// commission and are identified by cumulative filled quantity. Reconciliation
// reads GET /v2/account/activities/FILL?order_id=. Crypto is allowed with gtc
// or ioc TIF, canonical BASE/QUOTE symbols and the venue's size and price
// increments.
//
// Alpaca posts FEE (USD equity regulatory fees) and CFEE (crypto fees, charged
// in the received asset) as separate account activities, typically at end of
// day, so the provider declares UnlinkedFees and the connector reads them with
// a deterministic activity id. Until a CFEE is posted and applied, the crypto
// position includes the not-yet-charged fee.
func AlpacaProvider() fwtrading.Provider {
	return fwtrading.Provider{
		Type: domain.TradingProviderAlpaca, Title: "Alpaca",
		VenueAccounts: false, VerifiesSymbols: true, SearchesSymbols: true,
		UnlinkedFees: true,
		Build:        NewAlpacaConnector,
	}
}

// BinanceProvider trades one account per HMAC key pair and verifies symbols.
//
// One HMAC API key pair identifies one account; VenueAccount is empty. Paper
// mode selects the Spot Testnet REST and WebSocket API hosts; live mode uses
// api.binance.com and ws-api.binance.com. Route JSON for a limit order is
// {"timeInForce": "GTC"|"IOC"|"FOK"}; a market order requires {}. Only quantity
// orders are supported. Symbols are Binance names such as ETHUSDT, verified
// through GET /api/v3/exchangeInfo?symbol=. Symbol search uses exchangeInfo
// from the connection's host, includes spot-allowed symbols, and takes their
// base and quote assets from exchangeInfo. Account check reads GET
// /api/v3/account for uid (identity), account type and trading availability.
//
// Pre-check requires a TRADING, spot-allowed symbol supporting the order type,
// positive quantity and, for a limit order, a positive price. Limit orders
// apply PRICE_FILTER, LOT_SIZE, MIN_NOTIONAL and NOTIONAL bounds and increments
// where applicable; market orders apply MARKET_LOT_SIZE when its step is
// nonzero. The venue's own refusal is definitive for all other restrictions.
// Fills are identified by cumulative filled quantity. Commission is retained
// in the reported asset and booked in the order's base or quote when it is
// one of them.
//
// User data uses the WebSocket API userDataStream.subscribe.signature;
// Binance removed listenKey REST on 2026-02-20. Reconciliation reads GET
// /api/v3/order and GET /api/v3/myTrades, both requiring the order's symbol.
// There are no unlinked fees.
func BinanceProvider() fwtrading.Provider {
	return fwtrading.Provider{
		Type: domain.TradingProviderBinance, Title: "Binance",
		VenueAccounts: false, VerifiesSymbols: true, SearchesSymbols: true,
		UnlinkedFees: false,
		Build:        NewBinanceConnector,
	}
}

// EtoroProvider trades one account per key pair and verifies venue instruments.
//
// Credentials are {apiKey, userKey}; one pair identifies one account, so
// VenueAccounts is false and VenueAccount is empty. Paper mode selects demo
// trading paths and live mode selects real paths; unknown modes are refused.
// Route JSON is {} for market orders
// or {"timeInForce":"ioc"} for an IOC limit buy. Symbols are positive decimal
// instrument ids. Pre-check requires an available real, long, leverage-1
// configuration for buys; sells close an existing eligible long position.
//
// Account check reads GET /api/v1/me. Identity is demoCid for paper or realCid
// for live as a decimal string, stable across key rotation; a missing or
// non-positive CID is an upstream error. eToro reports no account status;
// Status is empty.
//
// VerifiesSymbols and SearchesSymbols are true. Verification reads
// GET /api/v2/market-data/instruments?instrumentsIds= and reports existence,
// tradability and "<type> <symbol> <displayName>". Search reads
// GET /api/v2/market-data/instruments/search with the trimmed query and limit=50,
// preserving venue order. Empty queries return an empty non-nil slice without
// a request; queries over 100 characters are refused rather than truncated.
// Matches use the instrument id as Symbol, "<symbol> - <displayName>" as Name
// and type as Class. Exchange is empty because eToro supplies only exchangeId;
// BaseAsset and QuoteAsset are empty because its catalogue states neither.
// Both operations derive Tradable from allowOpenPosition, an available real,
// long, leverage-1 configuration with isPotential=false, and an allowed
// quantity type of unitsOnly or all. Search makes one eligibility request for
// all result ids; an id eToro lists as not found is untradable (demo probe P10),
// while other eligibility failures fail the search.
// Market-data paths have no demo variant.
//
// UnlinkedFees is false: open-side fees arrive on fills, while close-side fees
// are not reported by eToro (demo probe P7).
func EtoroProvider() fwtrading.Provider {
	return fwtrading.Provider{
		Type: domain.TradingProviderEtoro, Title: "eToro",
		VenueAccounts: false, VerifiesSymbols: true, SearchesSymbols: true,
		UnlinkedFees: false,
		Build:        NewEtoroConnector,
	}
}
