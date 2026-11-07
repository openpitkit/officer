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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

type recordingNode struct {
	node.Node
	calls    *[]string
	closeErr error
}

type recordingStore struct {
	store.Store
	calls      *[]string
	migrateErr error
	closeErr   error
}

func (s *recordingStore) Migrate(context.Context) error {
	*s.calls = append(*s.calls, "store.Migrate")
	return s.migrateErr
}

func (s *recordingStore) Path() string { return "test store" }

func (s *recordingStore) Close() error {
	*s.calls = append(*s.calls, "store.Close")
	return s.closeErr
}

func (n *recordingNode) Close() error {
	*n.calls = append(*n.calls, "node.Close")
	return n.closeErr
}

type recordingMarketData struct {
	backend.MarketDataRuntime
	calls *[]string
}

func (m *recordingMarketData) Stop() {
	*m.calls = append(*m.calls, "marketData.Stop")
}

type recordingTrading struct {
	backend.TradingRuntime
	calls *[]string
}

func (r *recordingTrading) Stop() {
	*r.calls = append(*r.calls, "trading.Stop")
}

type testSigner struct{ signing.Service }
type testService struct{ backend.ControlPlane }
type testAuthorizer struct{ httpx.Authorizer }

func runningParts(calls *[]string) app.NodeRuntime {
	return app.NodeRuntime{
		Node:       &recordingNode{calls: calls},
		Signing:    &testSigner{},
		MarketData: &recordingMarketData{calls: calls},
		Trading:    &recordingTrading{calls: calls},
	}
}

func configuredBuilder(t *testing.T, b *app.Builder) *app.Builder {
	t.Helper()
	b.SetAuthorizer(testAuthorizer{})
	b.SetCallerResolver(func(*http.Request) (domain.Caller, error) {
		panic("unexpected caller resolution")
	})
	b.SetServiceFactory(func(context.Context, node.Node, backend.MarketDataRuntime,
		backend.TradingRuntime, signing.Service, *marketdata.Registry,
		*frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
		t.Fatal("unexpected service factory call")
		return nil, nil
	})
	b.SetRouteConfigBuilder(func(backend.ControlPlane, httpx.LogSource) app.RouteConfig {
		panic("unexpected route configuration")
	})
	b.AddToolRegistrar(func(*frameworkmcp.ToolRegistry, frameworkmcp.Source) {
		t.Fatal("unexpected tool registrar call")
	})
	return b
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func requireCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown calls = %v, want %v", got, want)
	}
}

func TestBuildOverSuccess(t *testing.T) {
	for _, registered := range []bool{false, true} {
		name := "zero_builder"
		if registered {
			name = "registered_providers"
		}
		t.Run(name, func(t *testing.T) {
			b := &app.Builder{}
			if registered {
				b = app.NewBuilder()
				if err := b.RegisterMarketDataProvider(marketdata.Provider{Type: "host-md"}); err != nil {
					t.Fatal(err)
				}
				if err := b.RegisterTradingProvider(trading.Provider{
					Type: "host-trading",
					Build: func(domain.TradingConnection) (trading.Connector, error) {
						panic("unexpected connector construction")
					},
				}); err != nil {
					t.Fatal(err)
				}
			}
			b.SetAuthorizer(testAuthorizer{})
			b.SetCallerResolver(func(*http.Request) (domain.Caller, error) {
				panic("unexpected caller resolution")
			})
			b.SetRouteConfigBuilder(func(backend.ControlPlane, httpx.LogSource) app.RouteConfig {
				panic("unexpected route configuration")
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []string
			parts := runningParts(&calls)
			service := &testService{}
			var md *marketdata.Registry
			var tr *trading.Registry
			var tools *frameworkmcp.ToolRegistry
			factoryCalls, serviceCalls := 0, 0
			b.SetServiceFactory(func(gotCtx context.Context, n node.Node, m backend.MarketDataRuntime,
				r backend.TradingRuntime, s signing.Service, registry *marketdata.Registry,
				toolRegistry *frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
				serviceCalls++
				if gotCtx != ctx || n != parts.Node || m != parts.MarketData || r != parts.Trading || s != parts.Signing {
					t.Fatal("service factory did not receive factory context and parts")
				}
				if md == nil || registry != md || toolRegistry == nil {
					t.Fatal("service factory did not receive builder registries")
				}
				tools = toolRegistry
				return service, nil
			})
			var registrars []int
			for _, id := range []int{1, 2} {
				b.AddToolRegistrar(func(registry *frameworkmcp.ToolRegistry, source frameworkmcp.Source) {
					if serviceCalls != 1 || registry != tools || !reflect.DeepEqual(source, frameworkmcp.SourceFor(service)) {
						t.Fatal("tool registrar did not receive composed service source and registry")
					}
					registrars = append(registrars, id)
				})
			}
			a, err := b.BuildOver(ctx, quietLogger(), func(gotCtx context.Context,
				mdRegistry *marketdata.Registry, tradingRegistry *trading.Registry) (app.NodeRuntime, error) {
				factoryCalls++
				if gotCtx != ctx {
					t.Fatal("runtime factory did not receive build context")
				}
				md, tr = mdRegistry, tradingRegistry
				if md == nil || tr == nil {
					t.Fatal("runtime factory received nil provider registries")
				}
				if registered {
					if _, ok := md.Lookup("host-md"); !ok {
						t.Fatal("runtime factory lost registered market-data provider")
					}
					if _, ok := tr.Lookup("host-trading"); !ok {
						t.Fatal("runtime factory lost registered trading provider")
					}
				}
				return parts, nil
			})
			if err != nil || a == nil {
				t.Fatalf("BuildOver = %v, %v; want app and no error", a, err)
			}
			if factoryCalls != 1 || serviceCalls != 1 {
				t.Fatalf("factory calls = %d, service calls = %d; want one each", factoryCalls, serviceCalls)
			}
			if a.Service() != service {
				t.Fatal("App did not retain composed service")
			}
			if !reflect.DeepEqual(registrars, []int{1, 2}) {
				t.Fatalf("tool registrars = %v, want [1 2]", registrars)
			}
			requireCalls(t, calls, nil)
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			requireCalls(t, calls, []string{"trading.Stop", "marketData.Stop", "node.Close"})
		})
	}
}

func TestBuildOverRefusesBeforeFactory(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*app.Builder)
		want      string
	}{
		{"store_factory", func(b *app.Builder) {
			b.SetStoreFactory(func() (store.Store, error) { panic("unexpected store factory") })
		}, "store factory"},
		{"node_builder", func(b *app.Builder) {
			b.SetNodeBuilder(func(context.Context, store.Store, app.FatalShutdownHook) (node.Node, engine.Engine, error) {
				panic("unexpected node builder")
			})
		}, "node builder"},
		{"node_hook", func(b *app.Builder) {
			b.AddNodeHook(func(context.Context, store.Store, node.Node) error { panic("unexpected node hook") })
		}, "node hooks"},
		{"signing_factory", func(b *app.Builder) {
			b.SetSigningFactory(func(store.RealmStore) (signing.Service, error) { panic("unexpected signing factory") })
		}, "signing factory"},
		{"missing_common_requirement", func(b *app.Builder) { b.SetAuthorizer(nil) }, "nil authorizer"},
		{"nil_caller_resolver", func(b *app.Builder) { b.SetCallerResolver(nil) }, "nil caller resolver"},
		{"nil_service_factory", func(b *app.Builder) { b.SetServiceFactory(nil) }, "nil service factory"},
		{"nil_route_config_builder", func(b *app.Builder) { b.SetRouteConfigBuilder(nil) }, "nil route config builder"},
		{"nil_factory", func(*app.Builder) {}, "nil node runtime factory"},
		{"nil_builder", nil, "nil builder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := configuredBuilder(t, app.NewBuilder())
			if tc.configure == nil {
				b = nil
			} else {
				tc.configure(b)
			}
			factoryCalls := 0
			factory := app.NodeRuntimeFactory(func(context.Context, *marketdata.Registry, *trading.Registry) (app.NodeRuntime, error) {
				factoryCalls++
				return app.NodeRuntime{}, errors.New("factory must not run")
			})
			if tc.name == "nil_factory" {
				factory = nil
			}
			a, err := b.BuildOver(context.Background(), quietLogger(), factory)
			if a != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v, %v; want error containing %q", a, err, tc.want)
			}
			if factoryCalls != 0 {
				t.Fatalf("runtime factory called %d times before refusal", factoryCalls)
			}
		})
	}
}

func TestBuildOverFactoryError(t *testing.T) {
	b := configuredBuilder(t, app.NewBuilder())
	var calls []string
	parts := runningParts(&calls)
	failure := errors.New("host factory failure")
	a, err := b.BuildOver(context.Background(), quietLogger(), func(context.Context,
		*marketdata.Registry, *trading.Registry) (app.NodeRuntime, error) {
		return parts, failure
	})
	if a != nil || !errors.Is(err, failure) || err == failure || !strings.Contains(err.Error(), "build node runtime") {
		t.Fatalf("factory error = %v, %v; want wrapped host error", a, err)
	}
	requireCalls(t, calls, nil)
}

func TestBuildOverMissingPart(t *testing.T) {
	cases := []struct {
		name      string
		remove    func(*app.NodeRuntime)
		wantCalls []string
	}{
		{"node", func(r *app.NodeRuntime) {
			r.Node = nil
		}, []string{"trading.Stop", "marketData.Stop"}},
		{"signing service", func(r *app.NodeRuntime) {
			r.Signing = nil
		}, []string{"trading.Stop", "marketData.Stop", "node.Close"}},
		{"market-data runtime", func(r *app.NodeRuntime) {
			r.MarketData = nil
		}, []string{"trading.Stop", "node.Close"}},
		{"trading runtime", func(r *app.NodeRuntime) {
			r.Trading = nil
		}, []string{"marketData.Stop", "node.Close"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := configuredBuilder(t, app.NewBuilder())
			var calls []string
			parts := runningParts(&calls)
			tc.remove(&parts)
			a, err := b.BuildOver(context.Background(), quietLogger(), func(context.Context,
				*marketdata.Registry, *trading.Registry) (app.NodeRuntime, error) {
				return parts, nil
			})
			if a != nil || err == nil || !strings.Contains(err.Error(), "returned no "+tc.name) {
				t.Fatalf("missing part = %v, %v; want explicit missing %s error", a, err, tc.name)
			}
			requireCalls(t, calls, tc.wantCalls)
		})
	}
}

func TestBuildJoinsCloseErrors(t *testing.T) {
	for _, name := range []string{"node-hook", "store-migrate"} {
		t.Run(name, func(t *testing.T) {
			b := configuredBuilder(t, app.NewBuilder())
			var calls []string
			cause := errors.New("build failure")
			closeErr := errors.New("close failure")
			st := &recordingStore{calls: &calls}
			n := &recordingNode{calls: &calls}
			var hookErr error
			wantCalls := []string{"store.Migrate", "node.Close"}
			if name == "node-hook" {
				hookErr, n.closeErr = cause, closeErr
			} else {
				st.migrateErr, st.closeErr = cause, closeErr
				wantCalls = []string{"store.Migrate", "store.Close"}
			}
			b.SetStoreFactory(func() (store.Store, error) { return st, nil })
			b.SetNodeBuilder(func(context.Context, store.Store,
				app.FatalShutdownHook) (node.Node, engine.Engine, error) {
				return n, struct{ engine.Engine }{}, nil
			})
			b.SetSigningFactory(func(store.RealmStore) (signing.Service, error) {
				t.Fatal("unexpected signing factory call")
				return nil, nil
			})
			b.AddNodeHook(func(context.Context, store.Store, node.Node) error {
				return hookErr
			})
			a, err := b.Build(context.Background(), quietLogger(), func(err error) {
				t.Fatalf("unexpected fatal shutdown: %v", err)
			})
			if a != nil {
				t.Fatal("Build returned an app after a failure")
			}
			if !errors.Is(err, cause) {
				t.Fatalf("Build error = %v; want build cause %v", err, cause)
			}
			if !errors.Is(err, closeErr) {
				t.Fatalf("Build error = %v; want joined close error %v", err, closeErr)
			}
			requireCalls(t, calls, wantCalls)
		})
	}
}

func TestBuildOverMissingPartCloseError(t *testing.T) {
	b := configuredBuilder(t, app.NewBuilder())
	var calls []string
	parts := runningParts(&calls)
	closeFailure := errors.New("host node close failure")
	parts.Node.(*recordingNode).closeErr = closeFailure
	parts.Signing = nil
	a, err := b.BuildOver(context.Background(), quietLogger(), func(context.Context,
		*marketdata.Registry, *trading.Registry) (app.NodeRuntime, error) {
		return parts, nil
	})
	if a != nil || err == nil || !strings.Contains(err.Error(), "returned no signing service") {
		t.Fatalf("missing part = %v, %v; want explicit missing signing service error", a, err)
	}
	if !errors.Is(err, closeFailure) {
		t.Fatalf("missing part error = %v; want joined node close error", err)
	}
	requireCalls(t, calls, []string{"trading.Stop", "marketData.Stop", "node.Close"})
}

func TestBuildOverServiceFactoryError(t *testing.T) {
	b := configuredBuilder(t, app.NewBuilder())
	var calls []string
	parts := runningParts(&calls)
	closeFailure := errors.New("host node close failure")
	parts.Node.(*recordingNode).closeErr = closeFailure
	failure := errors.New("host service failure")
	b.SetServiceFactory(func(context.Context, node.Node, backend.MarketDataRuntime,
		backend.TradingRuntime, signing.Service, *marketdata.Registry,
		*frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
		return nil, failure
	})
	a, err := b.BuildOver(context.Background(), quietLogger(), func(context.Context,
		*marketdata.Registry, *trading.Registry) (app.NodeRuntime, error) {
		return parts, nil
	})
	if a != nil || !errors.Is(err, failure) || err == failure || !strings.Contains(err.Error(), "build service") {
		t.Fatalf("service error = %v, %v; want wrapped host error", a, err)
	}
	if !errors.Is(err, closeFailure) {
		t.Fatalf("service error = %v; want joined node close error", err)
	}
	requireCalls(t, calls, []string{"trading.Stop", "marketData.Stop", "node.Close"})
}
