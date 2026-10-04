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

package sqlite

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func seedTradingFixtures(
	t *testing.T,
) (context.Context, store.RealmStore, domain.TradingConnection) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	for _, asset := range []string{"AAPL", "MSFT", "USD", "EUR"} {
		if _, err := rs.CreateAsset(ctx, domain.Asset{Code: asset}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	connection, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider:    domain.TradingProviderAlpaca,
		Label:       "Alpaca paper",
		Mode:        domain.TradingModeTest,
		Credentials: `{"key":"fixture"}`,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateTradingConnection: %v", err)
	}
	if connection.ExternalID.IsZero() {
		t.Fatal("CreateTradingConnection did not generate an external id")
	}
	return ctx, rs, connection
}

func createTradingOrder(
	t *testing.T, ctx context.Context, rs store.RealmStore,
) domain.Order {
	t.Helper()
	order, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	return order
}

func TestEarliestVenueOrderTime(t *testing.T) {
	ctx, rs, first := seedTradingFixtures(t)
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "Other", Mode: domain.TradingModeTest,
		Credentials: `{}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.ExternalID{first.ExternalID, second.ExternalID, "unknown"} {
		at, found, err := rs.EarliestVenueOrderTime(ctx, id)
		if err != nil || found || !at.IsZero() {
			t.Fatalf("empty connection: at=%v found=%v err=%v", at, found, err)
		}
	}
	links := make([]domain.VenueOrder, 0)
	for i, connection := range []domain.ExternalID{second.ExternalID, first.ExternalID, first.ExternalID} {
		order := createTradingOrder(t, ctx, rs)
		link, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
			Order: order.ExternalID, Connection: connection, Route: `{}`,
			ClientOrderID: order.ExternalID.String(),
		})
		if err != nil {
			t.Fatal(err)
		}
		links = append(links, link)
		if i == 1 {
			if err := rs.UpdateOrderStatus(ctx, order.ExternalID, domain.OrderStatusCancelled); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, connection := range []domain.ExternalID{second.ExternalID, first.ExternalID} {
		at, found, err := rs.EarliestVenueOrderTime(ctx, connection)
		if err != nil || !found || !at.Equal(links[i].CreatedAt) {
			t.Fatalf("earliest link for connection %s: at=%v want=%v found=%v err=%v",
				connection, at, links[i].CreatedAt, found, err)
		}
	}
}

func TestListTradingAccessForConnection(t *testing.T) {
	ctx, rs, first := seedTradingFixtures(t)
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: domain.TradingProviderAlpaca, Label: "Other", Mode: domain.TradingModeTest,
		Credentials: `{}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.ExternalID{first.ExternalID, second.ExternalID, "unknown"} {
		rows, err := rs.ListTradingAccessForConnection(ctx, id)
		if err != nil || len(rows) != 0 {
			t.Fatalf("empty connection: rows=%v err=%v", rows, err)
		}
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatal(err)
	}
	want := []domain.TradingAccess{
		{Account: "acc-1", Connection: first.ExternalID},
		{Account: "acc-1", Connection: first.ExternalID, VenueAccount: "z"},
		{Account: "acc-2", Connection: first.ExternalID, VenueAccount: "a"},
	}
	other := domain.TradingAccess{Account: "acc-2", Connection: second.ExternalID}
	for _, row := range []domain.TradingAccess{want[2], other, want[1], want[0]} {
		if err := rs.AddTradingAccess(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := rs.ListTradingAccessForConnection(ctx, first.ExternalID)
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("connection access ordering/isolation: got=%v want=%v err=%v", rows, want, err)
	}
	rows, err = rs.ListTradingAccessForConnection(ctx, second.ExternalID)
	if err != nil || !reflect.DeepEqual(rows, []domain.TradingAccess{other}) {
		t.Fatalf("other connection access: got=%v err=%v", rows, err)
	}
}

func TestTradingConnectionCreateGetListEnable(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	first, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		ExternalID:  "connection-b",
		Provider:    domain.TradingProviderAlpaca,
		Label:       "Second",
		Mode:        domain.TradingModeTest,
		Credentials: `{"key":"second"}`,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateTradingConnection(first): %v", err)
	}
	if first.ExternalID != "connection-b" {
		t.Fatal("CreateTradingConnection did not preserve the supplied external id")
	}
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		ExternalID:  "connection-a",
		Provider:    domain.TradingProviderAlpaca,
		Label:       "First",
		Mode:        domain.TradingModeReal,
		Credentials: `{"key":"first"}`,
	})
	if err != nil {
		t.Fatalf("CreateTradingConnection(second): %v", err)
	}

	got, ok, err := rs.GetTradingConnection(ctx, first.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetTradingConnection: ok=%v err=%v", ok, err)
	}
	if got.ExternalID != first.ExternalID || got.Provider != first.Provider ||
		got.Label != first.Label || got.Mode != first.Mode ||
		got.Enabled != first.Enabled {
		t.Fatal("GetTradingConnection did not preserve non-secret fields")
	}
	if got.Credentials != first.Credentials {
		t.Fatal("GetTradingConnection did not preserve credentials")
	}
	if _, ok, err := rs.GetTradingConnection(ctx, "missing"); err != nil || ok {
		t.Fatalf("GetTradingConnection(missing): ok=%v err=%v", ok, err)
	}

	connections, err := rs.ListTradingConnections(ctx)
	if err != nil {
		t.Fatalf("ListTradingConnections: %v", err)
	}
	if len(connections) != 2 ||
		connections[0].ExternalID != second.ExternalID ||
		connections[1].ExternalID != first.ExternalID {
		t.Fatal("ListTradingConnections is not ordered by external id")
	}

	if err := rs.SetTradingConnectionEnabled(ctx, second.ExternalID, true); err != nil {
		t.Fatalf("SetTradingConnectionEnabled: %v", err)
	}
	got, ok, err = rs.GetTradingConnection(ctx, second.ExternalID)
	if err != nil || !ok || !got.Enabled {
		t.Fatalf("enabled connection read: ok=%v enabled=%v err=%v", ok, got.Enabled, err)
	}
	if err := rs.SetTradingConnectionEnabled(ctx, "missing", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetTradingConnectionEnabled(missing) error = %v", err)
	}
}

func TestTradingConnectionValidationAndUniqueness(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	connection := domain.TradingConnection{
		ExternalID:  "connection-id",
		Provider:    domain.TradingProviderAlpaca,
		Label:       "Alpaca",
		Mode:        domain.TradingModeTest,
		Credentials: `{}`,
	}
	if _, err := rs.CreateTradingConnection(ctx, connection); err != nil {
		t.Fatalf("CreateTradingConnection: %v", err)
	}
	duplicateID := connection
	duplicateID.Label = "Other"
	if _, err := rs.CreateTradingConnection(ctx, duplicateID); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate external id error = %v", err)
	}
	duplicateLabel := connection
	duplicateLabel.ExternalID = "other-id"
	duplicateLabel.Label = "ALPACA"
	if _, err := rs.CreateTradingConnection(ctx, duplicateLabel); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("case-insensitive duplicate label error = %v", err)
	}
	invalidMode := connection
	invalidMode.ExternalID = "invalid-mode"
	invalidMode.Label = "Invalid mode"
	invalidMode.Mode = "paper"
	if _, err := rs.CreateTradingConnection(ctx, invalidMode); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid mode error = %v", err)
	}
	invalidCredentials := connection
	invalidCredentials.ExternalID = "invalid-credentials"
	invalidCredentials.Label = "Invalid credentials"
	invalidCredentials.Credentials = "[]"
	if _, err := rs.CreateTradingConnection(ctx, invalidCredentials); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid credentials error does not wrap ErrInvalid")
	}
}

func TestTradingInstrumentUpsertFindListAndUniqueness(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	instrument := domain.TradingInstrument{
		Connection:     connection.ExternalID,
		ExternalSymbol: "AAPL",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Enabled:        true,
	}
	if err := rs.UpsertTradingInstrument(ctx, instrument); err != nil {
		t.Fatalf("UpsertTradingInstrument: %v", err)
	}
	got, ok, err := rs.FindTradingInstrument(
		ctx, connection.ExternalID, "AAPL", "USD",
	)
	if err != nil || !ok {
		t.Fatalf("FindTradingInstrument: ok=%v err=%v", ok, err)
	}
	if got.ExternalSymbol != instrument.ExternalSymbol ||
		got.BaseAsset != instrument.BaseAsset ||
		got.QuoteAsset != instrument.QuoteAsset || !got.Enabled {
		t.Fatal("FindTradingInstrument did not populate the stored mapping")
	}

	updated := instrument
	updated.BaseAsset = "MSFT"
	updated.Enabled = false
	if err := rs.UpsertTradingInstrument(ctx, updated); err != nil {
		t.Fatalf("UpsertTradingInstrument(update by symbol): %v", err)
	}
	if _, ok, err := rs.FindTradingInstrument(
		ctx, connection.ExternalID, "AAPL", "USD",
	); err != nil || ok {
		t.Fatalf("old instrument pair after update: ok=%v err=%v", ok, err)
	}
	got, ok, err = rs.FindTradingInstrument(
		ctx, connection.ExternalID, "MSFT", "USD",
	)
	if err != nil || !ok || got.Enabled {
		t.Fatalf("updated instrument: ok=%v enabled=%v err=%v", ok, got.Enabled, err)
	}

	if err := rs.UpsertTradingInstrument(ctx, domain.TradingInstrument{
		Connection:     connection.ExternalID,
		ExternalSymbol: "MSFT-OTHER",
		BaseAsset:      "MSFT",
		QuoteAsset:     "USD",
	}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate asset pair error = %v", err)
	}
	if err := rs.UpsertTradingInstrument(ctx, domain.TradingInstrument{
		Connection:     connection.ExternalID,
		ExternalSymbol: "EURUSD",
		BaseAsset:      "EUR",
		QuoteAsset:     "USD",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertTradingInstrument(second): %v", err)
	}
	instruments, err := rs.ListTradingInstruments(ctx, connection.ExternalID)
	if err != nil {
		t.Fatalf("ListTradingInstruments: %v", err)
	}
	if len(instruments) != 2 || instruments[0].ExternalSymbol != "AAPL" ||
		instruments[1].ExternalSymbol != "EURUSD" {
		t.Fatal("ListTradingInstruments is not ordered by external symbol")
	}
}

func TestTradingInstrumentUnknownReferences(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	base := domain.TradingInstrument{
		Connection:     connection.ExternalID,
		ExternalSymbol: "AAPL",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
	}
	unknownConnection := base
	unknownConnection.Connection = "missing"
	if err := rs.UpsertTradingInstrument(ctx, unknownConnection); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown connection error = %v", err)
	}
	unknownBase := base
	unknownBase.BaseAsset = "UNKNOWN"
	if err := rs.UpsertTradingInstrument(ctx, unknownBase); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown base asset error = %v", err)
	}
	unknownQuote := base
	unknownQuote.QuoteAsset = "UNKNOWN"
	if err := rs.UpsertTradingInstrument(ctx, unknownQuote); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown quote asset error = %v", err)
	}
}

func TestTradingAccessAddListDuplicateAndUnknown(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	second, err := rs.CreateTradingConnection(ctx, domain.TradingConnection{
		ExternalID:  "connection-a",
		Provider:    domain.TradingProviderAlpaca,
		Label:       "Alpaca real",
		Mode:        domain.TradingModeReal,
		Credentials: `{}`,
	})
	if err != nil {
		t.Fatalf("CreateTradingConnection(second): %v", err)
	}
	for _, access := range []domain.TradingAccess{
		{Account: "acc-1", Connection: connection.ExternalID, VenueAccount: "z"},
		{Account: "acc-1", Connection: second.ExternalID, VenueAccount: "b"},
		{Account: "acc-1", Connection: second.ExternalID, VenueAccount: "a"},
	} {
		if err := rs.AddTradingAccess(ctx, access); err != nil {
			t.Fatalf("AddTradingAccess: %v", err)
		}
	}
	if err := rs.AddTradingAccess(ctx, domain.TradingAccess{
		Account: "acc-1", Connection: second.ExternalID, VenueAccount: "a",
	}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate trading access error = %v", err)
	}
	accesses, err := rs.ListTradingAccess(ctx, "acc-1")
	if err != nil {
		t.Fatalf("ListTradingAccess: %v", err)
	}
	if len(accesses) != 3 {
		t.Fatalf("ListTradingAccess count = %d, want 3", len(accesses))
	}
	for i := 1; i < len(accesses); i++ {
		previous := accesses[i-1]
		current := accesses[i]
		if previous.Connection > current.Connection ||
			(previous.Connection == current.Connection &&
				previous.VenueAccount > current.VenueAccount) {
			t.Fatal("ListTradingAccess is not ordered by connection and venue account")
		}
	}
	if err := rs.AddTradingAccess(ctx, domain.TradingAccess{
		Account: "missing", Connection: connection.ExternalID,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown access account error = %v", err)
	}
	if err := rs.AddTradingAccess(ctx, domain.TradingAccess{
		Account: "acc-1", Connection: "missing",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown access connection error = %v", err)
	}
}

func TestVenueOrderCreateGetFindAndUniqueness(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	firstOrder := createTradingOrder(t, ctx, rs)
	secondOrder := createTradingOrder(t, ctx, rs)
	link := domain.VenueOrder{
		Order:         firstOrder.ExternalID,
		Connection:    connection.ExternalID,
		Route:         `{"time_in_force":"day"}`,
		ClientOrderID: "client-1",
	}
	created, err := rs.CreateVenueOrder(ctx, link)
	if err != nil {
		t.Fatalf("CreateVenueOrder: %v", err)
	}
	if created.CreatedAt.IsZero() || created.CreatedAt.Location() != time.UTC {
		t.Fatal("CreateVenueOrder did not stamp a UTC creation time")
	}
	got, ok, err := rs.GetVenueOrder(ctx, firstOrder.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetVenueOrder: ok=%v err=%v", ok, err)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Fatal("GetVenueOrder did not return a UTC creation time")
	}
	assertVenueOrderEqual(t, got, created)
	got, ok, err = rs.FindVenueOrderByClientID(
		ctx, connection.ExternalID, link.ClientOrderID,
	)
	if err != nil || !ok {
		t.Fatalf("FindVenueOrderByClientID: ok=%v err=%v", ok, err)
	}
	assertVenueOrderEqual(t, got, created)
	if _, ok, err := rs.GetVenueOrder(ctx, secondOrder.ExternalID); err != nil || ok {
		t.Fatalf("GetVenueOrder(missing): ok=%v err=%v", ok, err)
	}
	if _, ok, err := rs.FindVenueOrderByClientID(
		ctx, connection.ExternalID, "missing",
	); err != nil || ok {
		t.Fatalf("FindVenueOrderByClientID(missing): ok=%v err=%v", ok, err)
	}

	if _, err := rs.CreateVenueOrder(ctx, link); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate Officer order link error = %v", err)
	}
	duplicateClient := link
	duplicateClient.Order = secondOrder.ExternalID
	if _, err := rs.CreateVenueOrder(ctx, duplicateClient); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate client order id error = %v", err)
	}
	acknowledgedAtCreate := link
	acknowledgedAtCreate.Order = secondOrder.ExternalID
	acknowledgedAtCreate.ClientOrderID = "client-2"
	acknowledgedAtCreate.SendAttempted = true
	acknowledgedAtCreate.VenueOrderID = "venue-2"
	if _, err := rs.CreateVenueOrder(
		ctx, acknowledgedAtCreate,
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("pre-acknowledged create error = %v", err)
	}
	sendAttemptedAtCreate := link
	sendAttemptedAtCreate.Order = secondOrder.ExternalID
	sendAttemptedAtCreate.ClientOrderID = "client-3"
	sendAttemptedAtCreate.SendAttempted = true
	if _, err := rs.CreateVenueOrder(
		ctx, sendAttemptedAtCreate,
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("send-attempted create error = %v", err)
	}
	unknownOrder := link
	unknownOrder.Order = "missing"
	unknownOrder.ClientOrderID = "missing-order"
	if _, err := rs.CreateVenueOrder(ctx, unknownOrder); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown Officer order error = %v", err)
	}
	unknownConnection := link
	unknownConnection.Order = secondOrder.ExternalID
	unknownConnection.Connection = "missing"
	unknownConnection.ClientOrderID = "missing-connection"
	if _, err := rs.CreateVenueOrder(ctx, unknownConnection); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown venue connection error = %v", err)
	}
}

func TestVenueOrderAcknowledgementAndDelete(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	firstOrder := createTradingOrder(t, ctx, rs)
	secondOrder := createTradingOrder(t, ctx, rs)
	thirdOrder := createTradingOrder(t, ctx, rs)
	for i, order := range []domain.Order{firstOrder, secondOrder, thirdOrder} {
		if _, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
			Order:         order.ExternalID,
			Connection:    connection.ExternalID,
			Route:         `{}`,
			ClientOrderID: []string{"client-1", "client-2", "client-3"}[i],
		}); err != nil {
			t.Fatalf("CreateVenueOrder(%d): %v", i, err)
		}
	}
	if err := rs.SetVenueOrderID(ctx, firstOrder.ExternalID, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty venue order id error = %v", err)
	}
	if err := rs.SetVenueOrderID(ctx, "missing", "venue-missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing venue order link error = %v", err)
	}
	if err := rs.MarkVenueOrderSendAttempted(
		ctx, "missing",
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing send-attempted link error = %v", err)
	}
	if err := rs.MarkVenueOrderSendAttempted(
		ctx, thirdOrder.ExternalID,
	); err != nil {
		t.Fatalf("MarkVenueOrderSendAttempted: %v", err)
	}
	if err := rs.MarkVenueOrderSendAttempted(
		ctx, thirdOrder.ExternalID,
	); err != nil {
		t.Fatalf("MarkVenueOrderSendAttempted(idempotent): %v", err)
	}
	marked, ok, err := rs.GetVenueOrder(ctx, thirdOrder.ExternalID)
	if err != nil || !ok || !marked.SendAttempted || marked.VenueOrderID != "" {
		t.Fatalf("marked venue order: order=%+v ok=%v err=%v", marked, ok, err)
	}
	if err := rs.DeleteVenueOrder(
		ctx, thirdOrder.ExternalID,
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("delete send-attempted venue order error = %v", err)
	}
	if err := rs.SetVenueOrderID(ctx, firstOrder.ExternalID, "venue-1"); err != nil {
		t.Fatalf("SetVenueOrderID: %v", err)
	}
	acknowledged, ok, err := rs.GetVenueOrder(ctx, firstOrder.ExternalID)
	if err != nil || !ok || !acknowledged.SendAttempted {
		t.Fatalf(
			"acknowledged venue order: order=%+v ok=%v err=%v",
			acknowledged, ok, err,
		)
	}
	if err := rs.SetVenueOrderID(ctx, firstOrder.ExternalID, "venue-1"); err != nil {
		t.Fatalf("SetVenueOrderID(same): %v", err)
	}
	if err := rs.SetVenueOrderID(ctx, firstOrder.ExternalID, "venue-other"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("replace venue order id error = %v", err)
	}
	if err := rs.DeleteVenueOrder(ctx, firstOrder.ExternalID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("delete acknowledged venue order error = %v", err)
	}
	if err := rs.DeleteVenueOrder(ctx, secondOrder.ExternalID); err != nil {
		t.Fatalf("DeleteVenueOrder(unacknowledged): %v", err)
	}
	if err := rs.DeleteVenueOrder(ctx, secondOrder.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteVenueOrder(missing) error = %v", err)
	}
}

func TestListOpenVenueOrdersUsesDomainTerminalStatuses(t *testing.T) {
	ctx, rs, connection := seedTradingFixtures(t)
	tests := []struct {
		status domain.OrderStatus
		order  domain.ExternalID
	}{
		{status: domain.OrderStatusSubmitted},
		{status: domain.OrderStatusAccepted},
		{status: domain.OrderStatusRejected},
		{status: domain.OrderStatusCommitted},
		{status: domain.OrderStatusRolledBack},
		{status: domain.OrderStatusFilled},
		{status: domain.OrderStatusPartiallyFilled},
		{status: domain.OrderStatusCancelled},
	}
	for i := range tests {
		test := &tests[i]
		t.Run(string(test.status), func(t *testing.T) {
			order := createTradingOrder(t, ctx, rs)
			if err := rs.UpdateOrderStatus(
				ctx, order.ExternalID, test.status,
			); err != nil {
				t.Fatalf("UpdateOrderStatus: %v", err)
			}
			if _, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
				Order:         order.ExternalID,
				Connection:    connection.ExternalID,
				Route:         `{}`,
				ClientOrderID: string(test.status),
			}); err != nil {
				t.Fatalf("CreateVenueOrder: %v", err)
			}
			test.order = order.ExternalID
		})
	}
	sharedClientID := "shared-client"
	firstSharedOrder := createTradingOrder(t, ctx, rs)
	if _, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
		Order:         firstSharedOrder.ExternalID,
		Connection:    connection.ExternalID,
		Route:         `{}`,
		ClientOrderID: sharedClientID,
	}); err != nil {
		t.Fatalf("CreateVenueOrder(first shared client id): %v", err)
	}
	secondConnection, err := rs.CreateTradingConnection(
		ctx,
		domain.TradingConnection{
			Provider:    domain.TradingProviderAlpaca,
			Label:       "Alpaca second paper",
			Mode:        domain.TradingModeTest,
			Credentials: `{"key":"second"}`,
			Enabled:     true,
		},
	)
	if err != nil {
		t.Fatalf("CreateTradingConnection(second): %v", err)
	}
	secondOrder := createTradingOrder(t, ctx, rs)
	if _, err := rs.CreateVenueOrder(ctx, domain.VenueOrder{
		Order:         secondOrder.ExternalID,
		Connection:    secondConnection.ExternalID,
		Route:         `{}`,
		ClientOrderID: sharedClientID,
	}); err != nil {
		t.Fatalf("CreateVenueOrder(second shared client id): %v", err)
	}

	open, err := rs.ListOpenVenueOrders(ctx, connection.ExternalID)
	if err != nil {
		t.Fatalf("ListOpenVenueOrders: %v", err)
	}
	openByOrder := make(map[domain.ExternalID]bool, len(open))
	for _, order := range open {
		if order.Connection != connection.ExternalID ||
			order.Order == secondOrder.ExternalID {
			t.Fatal("ListOpenVenueOrders returned another connection's row")
		}
		openByOrder[order.Order] = true
	}
	wantOpen := 1 // the requested connection's shared client id row
	for _, test := range tests {
		if !domain.OrderStatusTerminal(test.status) {
			wantOpen++
		}
	}
	if len(open) != wantOpen {
		t.Errorf("ListOpenVenueOrders returned %d rows, want %d", len(open), wantOpen)
	}
	if !sort.SliceIsSorted(open, func(i, j int) bool {
		if !open[i].CreatedAt.Equal(open[j].CreatedAt) {
			return open[i].CreatedAt.Before(open[j].CreatedAt)
		}
		return open[i].Order < open[j].Order
	}) {
		t.Error("ListOpenVenueOrders rows are not ordered by creation time, then order id")
	}
	for _, test := range tests {
		got := openByOrder[test.order]
		want := !domain.OrderStatusTerminal(test.status)
		if got != want {
			t.Errorf("status %s membership = %v, want %v", test.status, got, want)
		}
	}
	if !openByOrder[firstSharedOrder.ExternalID] {
		t.Error(
			"ListOpenVenueOrders omitted requested connection's shared client id row",
		)
	}
	secondOpen, err := rs.ListOpenVenueOrders(ctx, secondConnection.ExternalID)
	if err != nil {
		t.Fatalf("ListOpenVenueOrders(second): %v", err)
	}
	if len(secondOpen) != 1 || secondOpen[0].Order != secondOrder.ExternalID ||
		secondOpen[0].Connection != secondConnection.ExternalID {
		t.Fatal("ListOpenVenueOrders(second) did not isolate the connection")
	}
	firstFound, ok, err := rs.FindVenueOrderByClientID(
		ctx, connection.ExternalID, sharedClientID,
	)
	if err != nil || !ok || firstFound.Order != firstSharedOrder.ExternalID {
		t.Fatalf(
			"FindVenueOrderByClientID(first): order=%+v ok=%v err=%v",
			firstFound, ok, err,
		)
	}
	secondFound, ok, err := rs.FindVenueOrderByClientID(
		ctx, secondConnection.ExternalID, sharedClientID,
	)
	if err != nil || !ok || secondFound.Order != secondOrder.ExternalID {
		t.Fatalf(
			"FindVenueOrderByClientID(second): order=%+v ok=%v err=%v",
			secondFound, ok, err,
		)
	}
}

func assertVenueOrderEqual(
	t *testing.T, got, want domain.VenueOrder,
) {
	t.Helper()
	if got.Order != want.Order || got.Connection != want.Connection ||
		got.VenueAccount != want.VenueAccount || got.Route != want.Route ||
		got.ClientOrderID != want.ClientOrderID ||
		got.SendAttempted != want.SendAttempted ||
		got.VenueOrderID != want.VenueOrderID ||
		!got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatal("venue order round trip changed a field")
	}
}
