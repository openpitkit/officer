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

package backend

import (
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

func TestMarketDataInstrumentStaleMatchesEngineTTLBoundary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	instrument := domain.MarketDataInstrument{Enabled: true}

	for _, test := range []struct {
		name  string
		asOf  time.Time
		stale bool
	}{
		{
			name: "inside freshness window",
			asOf: now.Add(-MarketDataFreshnessTTL + time.Nanosecond),
		},
		{
			name:  "at ttl",
			asOf:  now.Add(-MarketDataFreshnessTTL),
			stale: true,
		},
		{
			name:  "past ttl",
			asOf:  now.Add(-MarketDataFreshnessTTL - time.Nanosecond),
			stale: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			quote := &domain.MarketDataQuote{AsOf: test.asOf}
			if got := marketDataInstrumentStale(
				true, instrument, quote, now,
			); got != test.stale {
				t.Fatalf("stale = %t, want %t", got, test.stale)
			}
		})
	}
}

func TestMarketDataInverseQuoteUsesCurrentInstrumentCodesAfterRename(t *testing.T) {
	t.Parallel()
	quote := &domain.MarketDataQuote{
		BaseAsset: "AAPL.OLD", QuoteAsset: "USD", Mark: "2",
	}
	instrument := domain.MarketDataInstrument{
		BaseAsset: "AAPL.NEW", QuoteAsset: "USD",
		BaseAssetID: 41, QuoteAssetID: 42,
	}
	inverted := marketDataInverseQuote(quote, instrument, true)
	if inverted == nil || inverted.BaseAsset != "USD" ||
		inverted.QuoteAsset != "AAPL.NEW" || inverted.Mark != "0.5" {
		t.Fatalf("panel inverse quote after rename = %+v", inverted)
	}
}

// TestAssetRenameNeedsNoRestart keeps asset codes out of restart identity.
func TestAssetRenameNeedsNoRestart(t *testing.T) {
	t.Parallel()
	const instanceID = "ib-primary"
	instance := domain.MarketDataInstance{
		ExternalID: domain.ExternalID(instanceID),
		Provider:   domain.MarketDataProviderIB,
		Enabled:    true,
	}
	before := []domain.MarketDataInstrument{{
		Instance:       instance.ExternalID,
		ExternalSymbol: "EURUSD",
		BaseAsset:      "EUR.OLD",
		QuoteAsset:     "USD.OLD",
		BaseAssetID:    41,
		QuoteAssetID:   42,
		Enabled:        true,
	}}
	renamed := []domain.MarketDataInstrument{{
		Instance:       instance.ExternalID,
		ExternalSymbol: "EURUSD",
		BaseAsset:      "EUR.NEW",
		QuoteAsset:     "USD.NEW",
		BaseAssetID:    41,
		QuoteAssetID:   42,
		Enabled:        true,
	}}
	current := map[string]marketdata.AppliedInstanceConfig{
		instanceID: marketDataAppliedConfig(instance, before, nil),
	}
	applied := map[string]marketdata.AppliedInstanceConfig{
		instanceID: marketDataAppliedConfig(instance, renamed, nil),
	}
	if marketDataRestartRequired(&marketdata.Manager{}, current, applied) {
		t.Fatal("market-data restart required after asset code rename")
	}
}
