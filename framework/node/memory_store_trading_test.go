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
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func (m *memoryRealm) EarliestVenueOrderTime(
	context.Context, domain.ExternalID,
) (time.Time, bool, error) {
	return time.Time{}, false, fmt.Errorf(
		"memory store: EarliestVenueOrderTime: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) ListTradingAccessForConnection(
	context.Context, domain.ExternalID,
) ([]domain.TradingAccess, error) {
	return nil, fmt.Errorf(
		"memory store: ListTradingAccessForConnection: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) CreateTradingConnection(
	context.Context, domain.TradingConnection,
) (domain.TradingConnection, error) {
	return domain.TradingConnection{}, fmt.Errorf(
		"memory store: CreateTradingConnection: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) GetTradingConnection(
	context.Context, domain.ExternalID,
) (domain.TradingConnection, bool, error) {
	return domain.TradingConnection{}, false, fmt.Errorf(
		"memory store: GetTradingConnection: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) ListTradingConnections(
	context.Context,
) ([]domain.TradingConnection, error) {
	return nil, fmt.Errorf(
		"memory store: ListTradingConnections: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) SetTradingConnectionEnabled(
	context.Context, domain.ExternalID, bool,
) error {
	return fmt.Errorf(
		"memory store: SetTradingConnectionEnabled: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) UpsertTradingInstrument(
	context.Context, domain.TradingInstrument,
) error {
	return fmt.Errorf(
		"memory store: UpsertTradingInstrument: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) ListTradingInstruments(
	context.Context, domain.ExternalID,
) ([]domain.TradingInstrument, error) {
	return nil, fmt.Errorf(
		"memory store: ListTradingInstruments: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) FindTradingInstrument(
	context.Context, domain.ExternalID, string, string,
) (domain.TradingInstrument, bool, error) {
	return domain.TradingInstrument{}, false, fmt.Errorf(
		"memory store: FindTradingInstrument: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) AddTradingAccess(
	context.Context, domain.TradingAccess,
) error {
	return fmt.Errorf(
		"memory store: AddTradingAccess: %w", domain.ErrNotImplemented,
	)
}

func (m *memoryRealm) ListTradingAccess(
	context.Context, domain.AccountID,
) ([]domain.TradingAccess, error) {
	return nil, fmt.Errorf(
		"memory store: ListTradingAccess: %w", domain.ErrNotImplemented,
	)
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
	return domain.VenueOrder{}, false, fmt.Errorf(
		"memory store: GetVenueOrder: %w", domain.ErrNotImplemented,
	)
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
