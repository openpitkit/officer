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

package node

import (
	"context"

	"go.openpit.dev/officer/framework/domain"
)

func (r *memoryRealm) CreateMarketDataInstance(
	_ context.Context, instance domain.MarketDataInstance,
) (domain.MarketDataInstance, error) {
	for _, existing := range r.instances {
		if existing.Label == instance.Label {
			return domain.MarketDataInstance{}, domain.ErrAlreadyExists
		}
	}
	if instance.ExternalID.IsZero() {
		instance.ExternalID = r.nextExternalID()
	}
	r.instances[instance.ExternalID] = instance
	return instance, nil
}

func (r *memoryRealm) GetMarketDataInstance(
	_ context.Context, id domain.ExternalID,
) (domain.MarketDataInstance, bool, error) {
	instance, ok := r.instances[id]
	return instance, ok, nil
}

func (r *memoryRealm) ListMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	out := make([]domain.MarketDataInstance, 0, len(r.instances))
	for _, instance := range r.instances {
		out = append(out, instance)
	}
	return out, nil
}

func (r *memoryRealm) ListEnabledMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	var out []domain.MarketDataInstance
	for _, instance := range r.instances {
		if instance.Enabled {
			out = append(out, instance)
		}
	}
	return out, nil
}

func (r *memoryRealm) SetMarketDataInstanceEnabled(
	_ context.Context, id domain.ExternalID, enabled bool,
) error {
	instance, ok := r.instances[id]
	if !ok {
		return domain.ErrNotFound
	}
	instance.Enabled = enabled
	r.instances[id] = instance
	return nil
}

func (r *memoryRealm) UpdateMarketDataInstanceSettings(
	_ context.Context, id domain.ExternalID, label, credentials string,
) error {
	instance, ok := r.instances[id]
	if !ok {
		return domain.ErrNotFound
	}
	instance.Label = label
	instance.Credentials = credentials
	r.instances[id] = instance
	return nil
}

func (r *memoryRealm) DeleteMarketDataInstance(
	_ context.Context, id domain.ExternalID,
) error {
	if _, ok := r.instances[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.instances, id)
	return nil
}

func (r *memoryRealm) UpsertMarketDataInstrument(
	_ context.Context, instrument domain.MarketDataInstrument,
) error {
	if _, ok := r.instances[instrument.Instance]; !ok {
		return domain.ErrNotFound
	}
	base, ok := r.assets[instrument.BaseAsset]
	if !ok {
		return domain.ErrInvalid
	}
	quote, ok := r.assets[instrument.QuoteAsset]
	if !ok {
		return domain.ErrInvalid
	}
	instrument.BaseAssetID = base.EngineAssetID
	instrument.QuoteAssetID = quote.EngineAssetID
	r.instruments[instrumentKey(instrument.Instance, instrument.ExternalSymbol)] = instrument
	return nil
}

func (r *memoryRealm) ListMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	var out []domain.MarketDataInstrument
	for _, instrument := range r.instruments {
		if instrument.Instance == instance {
			out = append(out, instrument)
		}
	}
	return out, nil
}

func (r *memoryRealm) ListEnabledMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	var out []domain.MarketDataInstrument
	for _, instrument := range r.instruments {
		if instrument.Instance == instance && instrument.Enabled {
			out = append(out, instrument)
		}
	}
	return out, nil
}

func (r *memoryRealm) SetMarketDataInstrumentEnabled(
	_ context.Context, instance domain.ExternalID, externalSymbol string, enabled bool,
) error {
	key := instrumentKey(instance, externalSymbol)
	instrument, ok := r.instruments[key]
	if !ok {
		return domain.ErrNotFound
	}
	instrument.Enabled = enabled
	r.instruments[key] = instrument
	return nil
}

func (r *memoryRealm) DeleteMarketDataInstrument(
	_ context.Context, instance domain.ExternalID, externalSymbol string,
) error {
	key := instrumentKey(instance, externalSymbol)
	if _, ok := r.instruments[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.instruments, key)
	return nil
}
