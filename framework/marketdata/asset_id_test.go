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
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// Test-only helpers are package-local, so each package keeps its own copy.
func testMarketDataAssetID(code string) domain.EngineAssetID {
	var id domain.EngineAssetID = 100
	for _, char := range code {
		id = id*131 + domain.EngineAssetID(char)
	}
	return id
}

func testMarketDataInstrumentWithAssetIDs(
	instrument domain.MarketDataInstrument,
) domain.MarketDataInstrument {
	if instrument.BaseAssetID == 0 {
		instrument.BaseAssetID = testMarketDataAssetID(instrument.BaseAsset)
	}
	if instrument.QuoteAssetID == 0 {
		instrument.QuoteAssetID = testMarketDataAssetID(instrument.QuoteAsset)
	}
	return instrument
}

func testMarketDataInstrumentsWithAssetIDs(
	instruments []domain.MarketDataInstrument,
) []domain.MarketDataInstrument {
	withIDs := append([]domain.MarketDataInstrument(nil), instruments...)
	for index := range withIDs {
		withIDs[index] = testMarketDataInstrumentWithAssetIDs(withIDs[index])
	}
	return withIDs
}

func TestMarketDataRuntimeIdentityUsesEngineAssetIDs(t *testing.T) {
	instrument := domain.MarketDataInstrument{
		ExternalSymbol: "provider-symbol",
		BaseAssetID:    domain.EngineAssetID(41),
		QuoteAssetID:   domain.EngineAssetID(42),
	}
	subs := subscriptionsFor([]domain.MarketDataInstrument{instrument}, nil)
	if len(subs) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(subs))
	}
	if subs[0].Base != instrument.BaseAssetID || subs[0].Quote != instrument.QuoteAssetID {
		t.Fatalf("subscription identity = %d/%d", subs[0].Base, subs[0].Quote)
	}
	symbols := externalSymbolsFor(subs)
	key := quoteInstrumentKey{base: instrument.BaseAssetID, quote: instrument.QuoteAssetID}
	if got := symbols[key]; got != instrument.ExternalSymbol {
		t.Fatalf("external symbol = %q, want %q", got, instrument.ExternalSymbol)
	}
}
