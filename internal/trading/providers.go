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
func AlpacaProvider() fwtrading.Provider {
	return fwtrading.Provider{
		Type: domain.TradingProviderAlpaca, Title: "Alpaca",
		VenueAccounts: false, VerifiesSymbols: true,
		Build: NewAlpacaConnector,
	}
}
