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
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// --- market-data control plane ---------------------------------------------

func (n *localNode) ListMarketDataInstances(
	ctx context.Context,
) ([]domain.MarketDataInstance, error) {
	instances, err := n.realm.ListMarketDataInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("list market-data instances: %w", err)
	}
	return instances, nil
}

func (n *localNode) GetMarketDataInstance(
	ctx context.Context, id domain.ExternalID,
) (domain.MarketDataInstance, bool, error) {
	instance, ok, err := n.realm.GetMarketDataInstance(ctx, id)
	if err != nil {
		return domain.MarketDataInstance{}, false, fmt.Errorf("get market-data instance: %w", err)
	}
	return instance, ok, nil
}

func (n *localNode) CreateMarketDataInstance(
	ctx context.Context, instance domain.MarketDataInstance, caller domain.Caller,
) (domain.MarketDataInstance, error) {
	if err := n.beginMutation(); err != nil {
		return domain.MarketDataInstance{}, err
	}
	defer n.endMutation()

	created, err := n.realm.CreateMarketDataInstance(ctx, instance)
	if err != nil {
		return domain.MarketDataInstance{}, fmt.Errorf("create market-data instance: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("create market-data instance %s", created.ExternalID),
	}); err != nil {
		return domain.MarketDataInstance{}, fmt.Errorf("audit create market-data instance: %w", err)
	}
	return created, nil
}

func (n *localNode) SetMarketDataInstanceEnabled(
	ctx context.Context, id domain.ExternalID, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetMarketDataInstanceEnabled(ctx, id, enabled); err != nil {
		return fmt.Errorf("set market-data instance enabled: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: marketDataToggleDetail("instance", id.String(), enabled),
	}); err != nil {
		return fmt.Errorf("audit set market-data instance enabled: %w", err)
	}
	return nil
}

func (n *localNode) UpdateMarketDataInstanceSettings(
	ctx context.Context, id domain.ExternalID, label, credentials string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.UpdateMarketDataInstanceSettings(ctx, id, label, credentials); err != nil {
		return fmt.Errorf("update market-data instance settings: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("update market-data instance settings %s", id),
	}); err != nil {
		return fmt.Errorf("audit update market-data instance settings: %w", err)
	}
	return nil
}

func (n *localNode) DeleteMarketDataInstance(
	ctx context.Context, id domain.ExternalID, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteMarketDataInstance(ctx, id); err != nil {
		return fmt.Errorf("delete market-data instance: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("delete market-data instance %s", id),
	}); err != nil {
		return fmt.Errorf("audit delete market-data instance: %w", err)
	}
	return nil
}

func (n *localNode) ListMarketDataInstruments(
	ctx context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	instruments, err := n.realm.ListMarketDataInstruments(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("list market-data instruments: %w", err)
	}
	return instruments, nil
}

func (n *localNode) UpsertMarketDataInstrument(
	ctx context.Context, instrument domain.MarketDataInstrument, caller domain.Caller,
) error {
	if err := n.ensureMarketDataAssetsRegisteredExclusive(ctx, instrument, caller); err != nil {
		return err
	}
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()
	if err := n.realm.UpsertMarketDataInstrument(ctx, instrument); err != nil {
		return fmt.Errorf("upsert market-data instrument: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("upsert market-data instrument %s/%s",
			instrument.Instance, instrument.ExternalSymbol),
	}); err != nil {
		return fmt.Errorf("audit upsert market-data instrument: %w", err)
	}
	return nil
}

func (n *localNode) ensureMarketDataAssetsRegisteredExclusive(
	ctx context.Context,
	instrument domain.MarketDataInstrument,
	caller domain.Caller,
) error {
	needed := false
	for _, code := range []string{instrument.BaseAsset, instrument.QuoteAsset} {
		if code == "" {
			continue
		}
		if _, ok, err := n.realm.GetAsset(ctx, code); err != nil {
			return fmt.Errorf("read asset for auto-create check: %w", err)
		} else if !ok {
			needed = true
		}
	}
	if !needed {
		return nil
	}
	if err := n.beginLiveIdentityPublication(); err != nil {
		return err
	}
	defer n.endLiveIdentityPublication()
	return n.ensureMarketDataAssets(ctx, instrument, caller)
}

func (n *localNode) ensureMarketDataAssets(
	ctx context.Context, instrument domain.MarketDataInstrument, caller domain.Caller,
) error {
	_, err := n.ensureAutoCreatedAssets(ctx, instrument.BaseAsset,
		instrument.QuoteAsset, "market-data instrument upsert", caller)
	return err
}

func (n *localNode) SetMarketDataInstrumentEnabled(
	ctx context.Context, instance domain.ExternalID, externalSymbol string, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetMarketDataInstrumentEnabled(
		ctx, instance, externalSymbol, enabled,
	); err != nil {
		return fmt.Errorf("set market-data instrument enabled: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: marketDataToggleDetail("instrument", instance.String()+"/"+externalSymbol, enabled),
	}); err != nil {
		return fmt.Errorf("audit set market-data instrument enabled: %w", err)
	}
	return nil
}

func (n *localNode) DeleteMarketDataInstrument(
	ctx context.Context, instance domain.ExternalID, externalSymbol string, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteMarketDataInstrument(ctx, instance, externalSymbol); err != nil {
		return fmt.Errorf("delete market-data instrument: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMarketData,
		Detail: fmt.Sprintf("delete market-data instrument %s/%s", instance, externalSymbol),
	}); err != nil {
		return fmt.Errorf("audit delete market-data instrument: %w", err)
	}
	return nil
}

func (n *localNode) ListMarketDataQuotes(
	ctx context.Context, instance domain.ExternalID,
) ([]domain.MarketDataQuote, error) {
	quotes, err := n.realm.ListMarketDataQuotes(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("list market-data quotes: %w", err)
	}
	return quotes, nil
}
