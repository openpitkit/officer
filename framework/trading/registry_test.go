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

package trading_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

type verifyingConnector struct{ *fakeConnector }

func (c verifyingConnector) VerifySymbol(_ context.Context, external string) (trading.SymbolVerification, error) {
	return trading.SymbolVerification{Exists: external == "AAPL", Tradable: external == "AAPL", Details: "asset active"}, nil
}

func TestRegistryAndFreshSymbolVerification(t *testing.T) {
	registry := trading.NewRegistry()
	if err := registry.Register(trading.Provider{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid registration=%v", err)
	}
	if _, err := registry.Build(domain.TradingConnection{Provider: "unknown"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown provider=%v", err)
	}
	venue := newVenue()
	connection := domain.TradingConnection{Provider: "verify", Credentials: "{}"}
	must(t, registry.Register(trading.Provider{Type: "verify", VerifiesSymbols: true,
		Build: func(c domain.TradingConnection) (trading.Connector, error) {
			raw, err := venue.build(c)
			if err != nil {
				return nil, err
			}
			return verifyingConnector{raw.(*fakeConnector)}, nil
		}}))
	result, supported, err := registry.VerifySymbol(testCtx, connection, "AAPL")
	must(t, err)
	if !supported || !result.Exists || !result.Tradable {
		t.Fatalf("verification=%+v supported=%v", result, supported)
	}
	venue.mu.Lock()
	closes := venue.closes
	venue.mu.Unlock()
	if closes != 1 {
		t.Fatalf("verification connector closes=%d", closes)
	}
	must(t, registry.Register(trading.Provider{Type: "verify", Build: venue.build}))
	_, supported, err = registry.VerifySymbol(testCtx, connection, "AAPL")
	must(t, err)
	if supported {
		t.Fatal("unsupported verifier reported supported")
	}
}

func TestRuntimeSymbolVerification(t *testing.T) {
	f := newFixture(t)
	r, err := trading.NewRuntime(f.registry, f.st, f.sink, f.adjustmentSink, slog.Default())
	must(t, err)
	t.Cleanup(r.Stop)
	if _, err := r.VerifySymbol(testCtx, f.connection.ExternalID, "AAPL"); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("unsupported runtime verification=%v", err)
	}
	if _, err := r.VerifySymbol(testCtx, domain.ExternalID("missing"), "AAPL"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing runtime verification=%v", err)
	}
	must(t, f.registry.Register(trading.Provider{Type: "fake", VerifiesSymbols: true,
		Build: func(c domain.TradingConnection) (trading.Connector, error) {
			raw, err := f.venue.build(c)
			if err != nil {
				return nil, err
			}
			return verifyingConnector{raw.(*fakeConnector)}, nil
		}}))
	must(t, f.st.SetTradingConnectionEnabled(testCtx, f.connection.ExternalID, false))
	result, err := r.VerifySymbol(testCtx, f.connection.ExternalID, "AAPL")
	must(t, err)
	if !result.Exists {
		t.Fatal("disabled connection could not verify symbol")
	}
}

func TestPreCheckMappingAndUnsupportedBeforeClaim(t *testing.T) {
	f := newFixture(t)
	f.start()
	var mapped trading.Order
	f.venue.onPreCheck = func(_ context.Context, order trading.Order) error { mapped = order; return trading.ErrUnsupported }
	draft := f.draft("1.125")
	draft.Price = ""
	dest := f.dest
	dest.Route = "{\"timeInForce\":\"day\"}"
	if err := f.runtime.PreCheck(testCtx, draft, dest); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("precheck error=%v", err)
	}
	if mapped.Symbol != "AAPL" || mapped.Quantity != draft.AmountValue || mapped.LimitPrice != "" || mapped.Route != dest.Route || mapped.ClientOrderID != "" {
		t.Fatalf("mapped draft=%+v", mapped)
	}
	count, err := f.st.CountOrders(testCtx)
	must(t, err)
	if count != 0 {
		t.Fatal("draft precheck wrote an order")
	}
	order := f.submit("1")
	if _, err := f.runtime.Send(testCtx, order.ExternalID, dest); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("send precheck error=%v", err)
	}
	_, exists, err := f.st.GetVenueOrder(testCtx, order.ExternalID)
	must(t, err)
	if exists {
		t.Fatal("unsupported precheck created a venue link")
	}
	if f.detail(order).Order.Status != domain.OrderStatusCommitted {
		t.Fatal("precheck took ownership of held order")
	}
}

func TestAlreadyAppliedSinkResultAndLateTerminalFill(t *testing.T) {
	f := newFixture(t)
	sink := func(ctx context.Context, in domain.ExecutionReportInput) error {
		err := f.sink(ctx, in)
		if err == nil && in.FillQuantity != "" {
			return domain.ErrAlreadyExists
		}
		return err
	}
	r, err := trading.NewRuntime(f.registry, f.st, sink, f.adjustmentSink,
		slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	must(t, err)
	must(t, r.Start(testCtx))
	f.runtime = r
	t.Cleanup(r.Stop)
	order := f.submit("10")
	link := f.send(order)
	a, b := makeFill("3", "3", "7", false), makeFill("7", "10", "0", true)
	f.venue.setSnapshot(link, trading.VenueStatusFilled, "10", a, b)
	f.orderEvent(link)
	f.flush()
	assertTrades(t, f.detail(order), []trading.Fill{a, b})
	f.fill(link, makeFill("1", "12", "0", true))
	f.flush()
	assertTrades(t, f.detail(order), []trading.Fill{a, b})
	if !strings.Contains(f.logs.text(), "venue execution after Officer order finished") {
		t.Fatal("late terminal execution was not logged")
	}
	if !strings.Contains(f.logs.text(), "venue cumulative qty 12") {
		t.Fatal("late terminal execution cumulative quantity was not logged")
	}
}
