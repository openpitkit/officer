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

package store

import (
	"context"
	"errors"
	"fmt"

	"go.openpit.dev/officer/internal/domain"
)

// SeedFXStablecoinsInstanceID is the id of the predefined bring-your-own source
// that carries the default stablecoin cross-rates. It is also the idempotency
// guard: seeding is skipped once an instance with this id exists.
const SeedFXStablecoinsInstanceID = "fx-stablecoins"

// seedFXStablecoinsLabel is the operator-facing label of the predefined source.
const seedFXStablecoinsLabel = "FX (stablecoins)"

// seedStablecoinPairs are the predefined cross-rates, all at 1:1. They are
// ordinary editable bring-your-own instruments: the operator can change the
// mark, disable, or remove them. The external symbol mirrors the base/quote
// pair so the manual source carries a sensible non-empty symbol.
var seedStablecoinPairs = []struct {
	base  string
	quote string
}{
	{"USDT", "USD"},
	{"USDC", "USD"},
	{"USDT", "USDC"},
	{"EURC", "EUR"},
	{"EURT", "EUR"},
	{"EURC", "EURT"},
}

// SeedMarketDataDefaults seeds the predefined stablecoin cross-rates the first
// time it runs, then never again. It creates one enabled bring-your-own source
// labelled "FX (stablecoins)" carrying the stablecoin pairs at a manual mark of
// "1". The presence of the source's id is the guard, so re-running is a no-op
// and an operator who edits or deletes the seeded rows is never overridden.
//
// It runs in the bootstrap path after Migrate and before the connector manager
// starts, so the enabled source and its 1:1 marks are applied by the manager's
// normal startup push - the seed adds no separate engine path.
func SeedMarketDataDefaults(ctx context.Context, s Store) error {
	if _, ok, err := s.GetMarketDataInstance(ctx, SeedFXStablecoinsInstanceID); err != nil {
		return fmt.Errorf("store: seed market-data defaults: check instance: %w", err)
	} else if ok {
		return nil
	}

	instance := domain.MarketDataInstance{
		ID:      SeedFXStablecoinsInstanceID,
		Type:    domain.MarketDataProviderBYO,
		Label:   seedFXStablecoinsLabel,
		Enabled: true,
	}
	if err := s.CreateMarketDataInstance(ctx, instance); err != nil {
		// A concurrent first run that lost the create race already seeded the
		// source; treat it as done rather than an error.
		if errors.Is(err, domain.ErrAlreadyExists) {
			return nil
		}
		return fmt.Errorf("store: seed market-data defaults: create instance: %w", err)
	}

	for _, pair := range seedStablecoinPairs {
		symbol := pair.base + "/" + pair.quote
		instrument := domain.MarketDataInstrument{
			InstanceID:     SeedFXStablecoinsInstanceID,
			ExternalSymbol: symbol,
			BaseAsset:      pair.base,
			QuoteAsset:     pair.quote,
			ManualPrice:    "1",
			Enabled:        true,
		}
		if err := s.UpsertMarketDataInstrument(ctx, instrument); err != nil {
			return fmt.Errorf(
				"store: seed market-data defaults: upsert instrument %s: %w", symbol, err)
		}
	}
	return nil
}
