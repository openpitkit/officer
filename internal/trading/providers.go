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
	return []fwtrading.Provider{AlpacaProvider()}
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
