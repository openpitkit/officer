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
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

type verifyingConnector struct{ *fakeConnector }

func (c verifyingConnector) VerifySymbol(_ context.Context, external string) (trading.SymbolVerification, error) {
	return trading.SymbolVerification{Exists: external == "AAPL", Tradable: external == "AAPL", Details: "asset active"}, nil
}

type capabilityConnector struct {
	*fakeConnector
	check  func(context.Context) (trading.AccountInfo, error)
	search func(context.Context, string) ([]trading.SymbolMatch, error)
}

func (c capabilityConnector) CheckAccount(ctx context.Context) (trading.AccountInfo, error) {
	return c.check(ctx)
}

func (c capabilityConnector) SearchSymbols(ctx context.Context, query string) ([]trading.SymbolMatch, error) {
	return c.search(ctx, query)
}

type connectionErrorStore struct {
	trading.Store
	err error
}

func (s connectionErrorStore) GetTradingConnection(context.Context, domain.ExternalID) (domain.TradingConnection, bool, error) {
	return domain.TradingConnection{}, false, s.err
}

func TestProvidersSorted(t *testing.T) {
	r := trading.NewRegistry()
	if len(r.Providers()) != 0 {
		t.Fatal("empty registry has providers")
	}
	v := newVenue()
	for _, typ := range []string{"z", "b", "a", "c"} {
		must(t, r.Register(trading.Provider{Type: typ, Title: "Provider " + typ,
			VenueAccounts: true, VerifiesSymbols: true, SearchesSymbols: true, Build: v.build}))
	}
	providers := r.Providers()
	if len(providers) != 4 {
		t.Fatalf("providers=%d want=4", len(providers))
	}
	for i, typ := range []string{"a", "b", "c", "z"} {
		p := providers[i]
		if p.Type != typ || p.Title != "Provider "+typ || !p.VenueAccounts || !p.VerifiesSymbols || !p.SearchesSymbols || p.Build == nil {
			t.Fatalf("provider %d differs: %+v", i, p)
		}
	}
}

func TestRegistryCapabilities(t *testing.T) {
	venueErr, buildErr := errors.New("venue failure"), errors.New("build failure")
	for _, capability := range []string{"account", "search"} {
		for _, tc := range []struct {
			name                       string
			supported, unknown         bool
			callErr, buildErr, wantErr error
		}{
			{name: "success", supported: true},
			{name: "unsupported"},
			{name: "venue error", supported: true, callErr: venueErr, wantErr: venueErr},
			{name: "build error", buildErr: buildErr, wantErr: buildErr},
			{name: "unknown provider", unknown: true, wantErr: domain.ErrInvalid},
		} {
			t.Run(capability+"/"+tc.name, func(t *testing.T) {
				r, v := trading.NewRegistry(), newVenue()
				connection := domain.TradingConnection{Provider: "capability", Mode: domain.TradingModePaper, Credentials: "draft credentials"}
				ctx, cancel := context.WithCancel(testCtx)
				defer cancel()
				info := trading.AccountInfo{Identity: "venue-account", Status: "ACTIVE"}
				matches := []trading.SymbolMatch{{Symbol: "BTC/USD", Name: "Bitcoin", Class: "crypto", Exchange: "ALPACA", Tradable: true}}
				query := " bTc/ "
				if !tc.unknown {
					must(t, r.Register(trading.Provider{Type: connection.Provider, SearchesSymbols: true,
						Build: func(got domain.TradingConnection) (trading.Connector, error) {
							if !reflect.DeepEqual(got, connection) {
								t.Error("connection was not passed to builder intact")
							}
							if tc.buildErr != nil {
								return nil, tc.buildErr
							}
							raw, err := v.build(got)
							must(t, err)
							if !tc.supported {
								return raw, nil
							}
							return capabilityConnector{fakeConnector: raw.(*fakeConnector),
								check: func(got context.Context) (trading.AccountInfo, error) {
									if got != ctx || capability != "account" {
										t.Error("wrong account capability invocation")
									}
									return info, tc.callErr
								},
								search: func(got context.Context, gotQuery string) ([]trading.SymbolMatch, error) {
									if got != ctx || gotQuery != query || capability != "search" {
										t.Error("wrong search capability invocation")
									}
									return matches, tc.callErr
								}}, nil
						}}))
				}
				for i := 0; i < 2; i++ {
					var supported bool
					var err error
					if capability == "account" {
						var result trading.AccountInfo
						result, supported, err = r.CheckAccount(ctx, connection)
						if tc.supported && result != info || !tc.supported && result != (trading.AccountInfo{}) {
							t.Fatalf("account result=%+v", result)
						}
					} else {
						var result []trading.SymbolMatch
						result, supported, err = r.SearchSymbols(ctx, connection, query)
						if tc.supported && !reflect.DeepEqual(result, matches) || !tc.supported && result != nil {
							t.Fatalf("search result=%+v", result)
						}
					}
					if supported != tc.supported || !errors.Is(err, tc.wantErr) {
						t.Fatalf("supported=%v error=%v want=%v", supported, err, tc.wantErr)
					}
				}
				wantBuilds := 2
				if tc.unknown || tc.buildErr != nil {
					wantBuilds = 0
				}
				if v.builds != wantBuilds || v.closes != wantBuilds || v.active != 0 {
					t.Fatalf("fresh connector counts: builds=%d closes=%d active=%d", v.builds, v.closes, v.active)
				}
			})
		}
	}
}

func TestRuntimeProviders(t *testing.T) {
	f := newFixture(t)
	f.newRuntime()
	must(t, f.registry.Register(trading.Provider{Type: "a", Title: "First", SearchesSymbols: true, Build: f.venue.build}))
	providers := f.runtime.Providers()
	if len(providers) != 2 || providers[0].Type != "a" || providers[1].Type != "fake" || !providers[0].SearchesSymbols {
		t.Fatalf("runtime providers=%+v", providers)
	}
	if f.venue.builds != 0 {
		t.Fatal("listing providers built a connector")
	}
}

func TestRuntimeCheckAccount(t *testing.T) {
	f := newFixture(t)
	readErr, buildErr, venueErr := errors.New("store failure"), errors.New("build failure"), errors.New("venue failure")
	r, err := trading.NewRuntime(f.registry, connectionErrorStore{Store: f.st, err: readErr}, f.sink, f.adjustmentSink, f.feeSkipSink, slog.Default())
	must(t, err)
	t.Cleanup(r.Stop)
	if _, err := r.CheckAccount(testCtx, f.connection); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("unsupported account check=%v", err)
	}
	if _, err := r.CheckAccount(testCtx, domain.TradingConnection{Provider: "unknown"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown provider account check=%v", err)
	}
	must(t, f.registry.Register(trading.Provider{Type: "broken", Build: func(domain.TradingConnection) (trading.Connector, error) { return nil, buildErr }}))
	if _, err := r.CheckAccount(testCtx, domain.TradingConnection{Provider: "broken"}); !errors.Is(err, buildErr) {
		t.Fatalf("build error account check=%v", err)
	}
	info := trading.AccountInfo{Identity: "venue-account", Status: "ACTIVE"}
	var expected domain.TradingConnection
	var callErr error
	must(t, f.registry.Register(trading.Provider{Type: "fake", Build: func(c domain.TradingConnection) (trading.Connector, error) {
		if !reflect.DeepEqual(c, expected) {
			t.Error("account connection was not passed intact")
		}
		raw, err := f.venue.build(c)
		must(t, err)
		return capabilityConnector{fakeConnector: raw.(*fakeConnector), check: func(ctx context.Context) (trading.AccountInfo, error) {
			if ctx != testCtx {
				t.Error("account context was not passed intact")
			}
			return info, callErr
		}}, nil
	}}))
	draft := domain.TradingConnection{ExternalID: "not-stored", Provider: "fake", Mode: domain.TradingModeLive, Credentials: "draft credentials"}
	for _, connection := range []domain.TradingConnection{f.connection, draft} {
		expected = connection
		result, err := r.CheckAccount(testCtx, connection)
		must(t, err)
		if result != info {
			t.Fatalf("account=%+v want=%+v", result, info)
		}
	}
	expected, callErr = draft, venueErr
	if result, err := r.CheckAccount(testCtx, draft); !errors.Is(err, venueErr) || result != (trading.AccountInfo{}) {
		t.Fatalf("venue error account check: result=%+v err=%v", result, err)
	}
	if f.venue.builds != 4 || f.venue.closes != 4 || f.venue.active != 0 {
		t.Fatal("account check relied on a worker or left a connector open")
	}
}

func TestRuntimeSearchSymbols(t *testing.T) {
	f := newFixture(t)
	f.newRuntime()
	if _, err := f.runtime.SearchSymbols(testCtx, f.connection, "AAPL"); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("unsupported symbol search=%v", err)
	}
	matches := []trading.SymbolMatch{{Symbol: "BTC/USD", Tradable: true}}
	query := " bTc/ "
	var callErr error
	must(t, f.registry.Register(trading.Provider{Type: "fake", Build: func(c domain.TradingConnection) (trading.Connector, error) {
		if !reflect.DeepEqual(c, f.connection) {
			t.Error("supplied search connection snapshot was not passed intact")
		}
		raw, err := f.venue.build(c)
		must(t, err)
		return capabilityConnector{fakeConnector: raw.(*fakeConnector), search: func(ctx context.Context, gotQuery string) ([]trading.SymbolMatch, error) {
			if ctx != testCtx || gotQuery != query {
				t.Error("search arguments were not passed intact")
			}
			return matches, callErr
		}}, nil
	}}))
	for _, enabled := range []bool{true, false} {
		must(t, f.st.SetTradingConnectionEnabled(testCtx, f.connection.ExternalID, enabled))
		f.connection.Enabled = enabled
		result, err := f.runtime.SearchSymbols(testCtx, f.connection, query)
		must(t, err)
		if !reflect.DeepEqual(result, matches) {
			t.Fatalf("matches=%+v want=%+v", result, matches)
		}
	}
	callErr = errors.New("venue failure")
	if result, err := f.runtime.SearchSymbols(testCtx, f.connection, query); !errors.Is(err, callErr) || result != nil {
		t.Fatalf("venue error symbol search: result=%+v err=%v", result, err)
	}
	buildErr := errors.New("build failure")
	must(t, f.registry.Register(trading.Provider{Type: "broken", Build: func(domain.TradingConnection) (trading.Connector, error) { return nil, buildErr }}))
	broken := f.addConnection("broken", "broken", false)
	if _, err := f.runtime.SearchSymbols(testCtx, broken, query); !errors.Is(err, buildErr) {
		t.Fatalf("build error symbol search=%v", err)
	}
	readErr := errors.New("store failure")
	r, err := trading.NewRuntime(f.registry, connectionErrorStore{Store: f.st, err: readErr}, f.sink, f.adjustmentSink, f.feeSkipSink, slog.Default())
	must(t, err)
	t.Cleanup(r.Stop)
	callErr = nil
	result, err := r.SearchSymbols(testCtx, f.connection, query)
	must(t, err)
	if !reflect.DeepEqual(result, matches) {
		t.Fatalf("snapshot search with unavailable store: matches=%+v want=%+v", result, matches)
	}
	if f.venue.builds != 5 || f.venue.closes != 5 || f.venue.active != 0 {
		t.Fatal("symbol search relied on a worker or left a connector open")
	}
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
	readErr := errors.New("store failure")
	r, err := trading.NewRuntime(f.registry, connectionErrorStore{Store: f.st, err: readErr}, f.sink, f.adjustmentSink, f.feeSkipSink, slog.Default())
	must(t, err)
	t.Cleanup(r.Stop)
	if _, err := r.VerifySymbol(testCtx, f.connection, "AAPL"); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("unsupported runtime verification=%v", err)
	}
	must(t, f.registry.Register(trading.Provider{Type: "fake", VerifiesSymbols: true,
		Build: func(c domain.TradingConnection) (trading.Connector, error) {
			if !reflect.DeepEqual(c, f.connection) {
				t.Error("supplied verification connection snapshot was not passed intact")
			}
			raw, err := f.venue.build(c)
			if err != nil {
				return nil, err
			}
			return verifyingConnector{raw.(*fakeConnector)}, nil
		}}))
	must(t, f.st.SetTradingConnectionEnabled(testCtx, f.connection.ExternalID, false))
	f.connection.Enabled = false
	result, err := r.VerifySymbol(testCtx, f.connection, "AAPL")
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
	r, err := trading.NewRuntime(f.registry, f.st, sink, f.adjustmentSink, f.feeSkipSink,
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
