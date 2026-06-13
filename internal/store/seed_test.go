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

package store_test

import (
	"context"
	"testing"

	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/store"
)

// wantSeedPairs are the external symbols the seed must create, each at mark "1".
var wantSeedPairs = map[string]struct{ base, quote string }{
	"USDT/USD":  {"USDT", "USD"},
	"USDC/USD":  {"USDC", "USD"},
	"USDT/USDC": {"USDT", "USDC"},
	"EURC/EUR":  {"EURC", "EUR"},
	"EURT/EUR":  {"EURT", "EUR"},
	"EURC/EURT": {"EURC", "EURT"},
}

// TestSeedMarketDataDefaults_SeedsStablecoinCrossRates verifies the first seed
// creates one enabled BYO source with the predefined stablecoin pairs at a
// manual mark of "1".
func TestSeedMarketDataDefaults_SeedsStablecoinCrossRates(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	if err := store.SeedMarketDataDefaults(ctx, s); err != nil {
		t.Fatalf("SeedMarketDataDefaults: %v", err)
	}

	instance, ok, err := s.GetMarketDataInstance(ctx, store.SeedFXStablecoinsInstanceID)
	if err != nil {
		t.Fatalf("GetMarketDataInstance: %v", err)
	}
	if !ok {
		t.Fatal("seed instance not created")
	}
	if instance.Type != domain.MarketDataProviderBYO || !instance.Enabled {
		t.Fatalf("seed instance = %+v, want BYO and enabled", instance)
	}

	instruments, err := s.ListMarketDataInstruments(ctx, store.SeedFXStablecoinsInstanceID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != len(wantSeedPairs) {
		t.Fatalf("want %d seeded instruments, got %d", len(wantSeedPairs), len(instruments))
	}
	for _, inst := range instruments {
		want, ok := wantSeedPairs[inst.ExternalSymbol]
		if !ok {
			t.Fatalf("unexpected seeded instrument %q", inst.ExternalSymbol)
		}
		if inst.BaseAsset != want.base || inst.QuoteAsset != want.quote {
			t.Fatalf("instrument %q = %s/%s, want %s/%s",
				inst.ExternalSymbol, inst.BaseAsset, inst.QuoteAsset, want.base, want.quote)
		}
		if inst.ManualPrice != "1" {
			t.Fatalf("instrument %q manual price = %q, want \"1\"",
				inst.ExternalSymbol, inst.ManualPrice)
		}
		if !inst.Enabled {
			t.Fatalf("instrument %q not enabled", inst.ExternalSymbol)
		}
	}
}

// TestSeedMarketDataDefaults_Idempotent verifies a second seed is a no-op: it
// neither duplicates rows nor overrides an operator edit made between runs.
func TestSeedMarketDataDefaults_Idempotent(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	if err := store.SeedMarketDataDefaults(ctx, s); err != nil {
		t.Fatalf("first SeedMarketDataDefaults: %v", err)
	}

	// Operator edits one seeded row: changes the mark and disables it.
	edited := domain.MarketDataInstrument{
		InstanceID:     store.SeedFXStablecoinsInstanceID,
		ExternalSymbol: "USDT/USD",
		BaseAsset:      "USDT",
		QuoteAsset:     "USD",
		ManualPrice:    "1.01",
		Enabled:        false,
	}
	if err := s.UpsertMarketDataInstrument(ctx, edited); err != nil {
		t.Fatalf("operator edit: %v", err)
	}

	// Second seed must be a clean no-op.
	if err := store.SeedMarketDataDefaults(ctx, s); err != nil {
		t.Fatalf("second SeedMarketDataDefaults: %v", err)
	}

	instruments, err := s.ListMarketDataInstruments(ctx, store.SeedFXStablecoinsInstanceID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != len(wantSeedPairs) {
		t.Fatalf("re-seed changed instrument count: got %d, want %d",
			len(instruments), len(wantSeedPairs))
	}
	for _, inst := range instruments {
		if inst.ExternalSymbol == "USDT/USD" {
			if inst.ManualPrice != "1.01" || inst.Enabled {
				t.Fatalf("re-seed overrode operator edit: %+v", inst)
			}
		}
	}
}
