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
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func TestMemoryUpdateTradingConnectionSettingsRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	_, realm := newTestNode(t, newFakeEngine())
	original, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "paper", Mode: domain.TradingModePaper,
		VenueIdentity: "paper-account", Credentials: `{"key":"original-secret"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, label, credentials string
	}{
		{"empty label", "", "{}"},
		{"label leading space", " paper", "{}"},
		{"label trailing space", "paper ", "{}"},
		{"credentials leading space", "renamed", " {}"},
		{"credentials trailing space", "renamed", "{} "},
		{"empty credentials", "renamed", ""},
		{"invalid credentials", "renamed", "{"},
		{"array credentials", "renamed", "[]"},
		{"string credentials", "renamed", `"secret"`},
		{"number credentials", "renamed", "1"},
		{"boolean credentials", "renamed", "true"},
		{"null credentials", "renamed", "null"},
		{"trailing JSON", "renamed", "{} {}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := realm.UpdateTradingConnectionSettings(ctx, original.ExternalID, test.label, test.credentials); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("invalid settings error = %v, want ErrInvalid", err)
			}
			got, found, err := realm.GetTradingConnection(ctx, original.ExternalID)
			if err != nil || !found || got != original {
				t.Errorf("invalid settings changed connection: found=%v err=%v", found, err)
			}
		})
	}
}

func (m *memoryRealm) CredentialsSealed() bool {
	return m.credentialsSealed
}

func (m *memoryRealm) UpdateTradingConnectionSettings(_ context.Context, id domain.ExternalID, label, credentials string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := domain.ValidateTradingConnectionSettings(label, credentials); err != nil {
		return err
	}
	connection, ok := m.tradingConnections[id]
	if !ok {
		return domain.ErrNotFound
	}
	for existingID, existing := range m.tradingConnections {
		if existingID != id && strings.EqualFold(existing.Label, label) {
			return domain.ErrAlreadyExists
		}
	}
	connection.Label, connection.Credentials = label, credentials
	m.tradingConnections[id] = connection
	return nil
}

func (m *memoryRealm) DeleteTradingInstrument(_ context.Context, connection domain.ExternalID, externalSymbol string) error {
	key := fmt.Sprintf("%s/%s", connection, externalSymbol)
	if _, ok := m.tradingInstruments[key]; !ok {
		return domain.ErrNotFound
	}
	delete(m.tradingInstruments, key)
	return nil
}

func (m *memoryRealm) RemoveTradingAccess(_ context.Context, account domain.AccountID, connection domain.ExternalID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.tradingAccess {
		if existing.Account == account && existing.Connection == connection {
			m.tradingAccess = append(m.tradingAccess[:i], m.tradingAccess[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (m *memoryRealm) EarliestVenueOrderTime(
	context.Context, domain.ExternalID,
) (time.Time, bool, error) {
	return time.Time{}, false, fmt.Errorf(
		"memory store: EarliestVenueOrderTime: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) ListTradingAccessForConnection(_ context.Context, connection domain.ExternalID) ([]domain.TradingAccess, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var grants []domain.TradingAccess
	for _, access := range m.tradingAccess {
		if access.Connection == connection {
			grants = append(grants, access)
		}
	}
	sort.Slice(grants, func(i, j int) bool {
		if grants[i].Account == grants[j].Account {
			return grants[i].VenueAccount < grants[j].VenueAccount
		}
		return grants[i].Account < grants[j].Account
	})
	return grants, nil
}

func (m *memoryRealm) GetTradingFeeAccounts(_ context.Context, id domain.ExternalID) (domain.AccountID, []domain.AccountID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	connection, found := m.tradingConnections[id]
	if !found {
		return "", nil, domain.ErrNotFound
	}
	bound := make(map[domain.AccountID]struct{})
	for _, access := range m.tradingAccess {
		if access.Connection == id {
			bound[access.Account] = struct{}{}
		}
	}
	accounts := make([]domain.AccountID, 0, len(bound))
	for account := range bound {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i] < accounts[j] })
	return connection.FeeAccount, accounts, nil
}

func (m *memoryRealm) CreateTradingConnection(_ context.Context, connection domain.TradingConnection) (domain.TradingConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := domain.ValidateTradingConnection(connection); err != nil {
		return domain.TradingConnection{}, err
	}
	if connection.FeeAccount != "" {
		return domain.TradingConnection{}, domain.ErrInvalid
	}
	for _, existing := range m.tradingConnections {
		if existing.ExternalID == connection.ExternalID || strings.EqualFold(existing.Label, connection.Label) || (existing.Provider == connection.Provider && existing.VenueIdentity == connection.VenueIdentity) {
			return domain.TradingConnection{}, domain.ErrAlreadyExists
		}
	}
	if connection.ExternalID.IsZero() {
		connection.ExternalID = m.nextExternalID()
	}
	m.tradingConnections[connection.ExternalID] = connection
	return connection, nil
}

func (m *memoryRealm) GetTradingConnection(_ context.Context, id domain.ExternalID) (domain.TradingConnection, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	connection, ok := m.tradingConnections[id]
	return connection, ok, nil
}

func (m *memoryRealm) ListTradingConnections(context.Context) ([]domain.TradingConnection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var connections []domain.TradingConnection
	for _, connection := range m.tradingConnections {
		connections = append(connections, connection)
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].ExternalID < connections[j].ExternalID })
	return connections, nil
}

func (m *memoryRealm) SetTradingConnectionFeeAccount(_ context.Context, id domain.ExternalID, account domain.AccountID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return err
		}
		if _, ok := m.accounts[account]; !ok {
			return domain.ErrNotFound
		}
	}
	connection, ok := m.tradingConnections[id]
	if !ok {
		return domain.ErrNotFound
	}
	connection.FeeAccount = account
	m.tradingConnections[id] = connection
	return nil
}

func (m *memoryRealm) SetTradingConnectionEnabled(_ context.Context, id domain.ExternalID, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	connection, ok := m.tradingConnections[id]
	if !ok {
		return domain.ErrNotFound
	}
	connection.Enabled = enabled
	m.tradingConnections[id] = connection
	return nil
}

func (m *memoryRealm) UpsertTradingInstrument(_ context.Context, instrument domain.TradingInstrument) error {
	if err := domain.ValidateTradingInstrument(instrument); err != nil {
		return err
	}
	if _, ok := m.tradingConnections[instrument.Connection]; !ok {
		return domain.ErrNotFound
	}
	if _, ok := m.assets[instrument.BaseAsset]; !ok {
		return domain.NewAssetMissingError(instrument.BaseAsset)
	}
	if _, ok := m.assets[instrument.QuoteAsset]; !ok {
		return domain.NewAssetMissingError(instrument.QuoteAsset)
	}
	key := fmt.Sprintf("%s/%s", instrument.Connection, instrument.ExternalSymbol)
	for existingKey, existing := range m.tradingInstruments {
		if existingKey != key && existing.Connection == instrument.Connection && existing.BaseAsset == instrument.BaseAsset && existing.QuoteAsset == instrument.QuoteAsset {
			return domain.ErrAlreadyExists
		}
	}
	m.tradingInstruments[key] = instrument
	return nil
}

func (m *memoryRealm) ListTradingInstruments(_ context.Context, connection domain.ExternalID) ([]domain.TradingInstrument, error) {
	var instruments []domain.TradingInstrument
	for _, instrument := range m.tradingInstruments {
		if instrument.Connection == connection {
			instruments = append(instruments, instrument)
		}
	}
	sort.Slice(instruments, func(i, j int) bool { return instruments[i].ExternalSymbol < instruments[j].ExternalSymbol })
	return instruments, nil
}

func (m *memoryRealm) FindTradingInstrument(
	context.Context, domain.ExternalID, string, string,
) (domain.TradingInstrument, bool, error) {
	return domain.TradingInstrument{}, false, fmt.Errorf(
		"memory store: FindTradingInstrument: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) PutTradingAccess(_ context.Context, access domain.TradingAccess) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := domain.ValidateTradingAccess(access); err != nil {
		return err
	}
	if _, ok := m.tradingConnections[access.Connection]; !ok {
		return domain.ErrNotFound
	}
	if _, ok := m.accounts[access.Account]; !ok {
		return domain.ErrNotFound
	}
	for i, existing := range m.tradingAccess {
		if existing.Account == access.Account && existing.Connection == access.Connection {
			m.tradingAccess[i] = access
			return nil
		}
	}
	m.tradingAccess = append(m.tradingAccess, access)
	return nil
}

func (m *memoryRealm) ListTradingAccess(_ context.Context, account domain.AccountID) ([]domain.TradingAccess, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var grants []domain.TradingAccess
	for _, access := range m.tradingAccess {
		if access.Account == account {
			grants = append(grants, access)
		}
	}
	sort.Slice(grants, func(i, j int) bool {
		if grants[i].Connection == grants[j].Connection {
			return grants[i].VenueAccount < grants[j].VenueAccount
		}
		return grants[i].Connection < grants[j].Connection
	})
	return grants, nil
}

func (m *memoryRealm) CreateVenueOrder(
	context.Context, domain.VenueOrder,
) (domain.VenueOrder, error) {
	return domain.VenueOrder{}, fmt.Errorf(
		"memory store: CreateVenueOrder: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) SetVenueOrderID(
	context.Context, domain.ExternalID, string,
) error {
	return fmt.Errorf(
		"memory store: SetVenueOrderID: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) MarkVenueOrderSendAttempted(
	context.Context, domain.ExternalID,
) error {
	return fmt.Errorf(
		"memory store: MarkVenueOrderSendAttempted: %w",
		domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) DeleteVenueOrder(
	context.Context, domain.ExternalID,
) error {
	return fmt.Errorf(
		"memory store: DeleteVenueOrder: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) GetVenueOrder(
	context.Context, domain.ExternalID,
) (domain.VenueOrder, bool, error) {
	return domain.VenueOrder{}, false, nil
}

func (m *memoryRealm) FindVenueOrderByClientID(
	context.Context, domain.ExternalID, string,
) (domain.VenueOrder, bool, error) {
	return domain.VenueOrder{}, false, fmt.Errorf(
		"memory store: FindVenueOrderByClientID: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) ListOpenVenueOrders(
	context.Context, domain.ExternalID,
) ([]domain.VenueOrder, error) {
	return nil, fmt.Errorf(
		"memory store: ListOpenVenueOrders: %w", domain.ErrNotImplemented,
	)
}
