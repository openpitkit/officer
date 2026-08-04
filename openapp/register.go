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

// Package openapp registers the open Pit Officer concrete implementations.
package openapp

import (
	"context"

	"go.openpit.dev/officer"
	enginenative "go.openpit.dev/officer/app/engine/native"
	frameworkapp "go.openpit.dev/officer/framework/app"
	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/engine"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/httproutes"
	appmarketdata "go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/mcp/tools"
	appsigning "go.openpit.dev/officer/internal/signing"
	"go.openpit.dev/officer/internal/store/sqlite"
)

// Register populates b with the open Officer concrete implementations.
func Register(b *frameworkapp.Builder) {
	if b == nil {
		return
	}
	b.SetStoreFactory(func(path string) (store.Store, error) {
		return sqlite.New(path)
	})
	b.SetEngineBuildFactory(func(cfg frameworkapp.Config) engine.BuildFunc {
		return func(snap engine.Snapshot) (engine.Engine, error) {
			return enginenative.BuildOpenPitEngine(cfg.RuntimeLibraryPath, snap)
		}
	})
	b.SetNodeBuilder(func(
		ctx context.Context,
		st store.Store,
		build engine.BuildFunc,
		fatalHook frameworkapp.FatalShutdownHook,
	) (node.Node, engine.Engine, error) {
		return node.NewLocalNode(ctx, st, build, node.WithFatalShutdownHook(fatalHook))
	})
	b.SetNodeRouterBuilder(node.NewLocalRouter)
	b.SetSigningFactory(func(st store.RealmStore) (fwsigning.Service, error) {
		return appsigning.New(st)
	})
	b.SetAuthorizer(httpx.AllowAll{})
	for _, provider := range []fwmarketdata.Provider{
		appmarketdata.IBProvider(),
		appmarketdata.BinanceProvider(),
		appmarketdata.KrakenProvider(),
		appmarketdata.CoinbaseProvider(),
		appmarketdata.AlpacaProvider(),
		appmarketdata.OKXProvider(),
		appmarketdata.BybitProvider(),
		appmarketdata.OANDAProvider(),
		appmarketdata.FinnhubProvider(),
		appmarketdata.BYOProvider(),
		appmarketdata.MockProvider(),
	} {
		_ = b.RegisterMarketDataProvider(provider)
	}
	b.SetServiceFactory(func(
		router node.NodeRouter,
		md fwbackend.MarketDataRuntime,
		signer fwsigning.Service,
		registry *fwmarketdata.Registry,
		mcpRegistry *frameworkmcp.ToolRegistry,
	) (frameworkapp.ControlPlane, error) {
		return fwbackend.New(
			router,
			md,
			signer,
			fwbackend.WithMarketDataRegistry(registry),
			fwbackend.WithMCPCatalogProvider(mcpRegistry),
			fwbackend.WithLockSettlementPrice(
				enginenative.LockSettlementPrice,
			),
		), nil
	})
	b.SetRouteConfigBuilder(func(
		svc fwbackend.ControlPlane,
		logs httpx.LogSource,
	) frameworkapp.RouteConfig {
		cfg := httproutes.Build(svc, logs)
		return frameworkapp.RouteConfig{
			Routes:      cfg.Routes,
			Authorizer:  cfg.Authorizer,
			BodyLimit:   cfg.BodyLimit,
			ExtraMounts: cfg.ExtraMounts,
		}
	})
	b.AddToolRegistrar(tools.RegisterTools)
	b.SetSPAFactory(officer.WebDist)
}
