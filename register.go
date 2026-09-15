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

// Package officer registers the Pit Officer concrete implementations.
package officer

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"go.openpit.dev/officer/engine"
	frameworkapp "go.openpit.dev/officer/framework/app"
	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
	appmarketdata "go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/mcptools"
	"go.openpit.dev/officer/signing"
	"go.openpit.dev/officer/web"
)

// Config carries the default composition's runtime inputs: the SQLite database
// path and the master key that seals stored secrets (nil when none is
// configured). The native OpenPit runtime is not one of them: the SDK loads it
// at process start (see engine.NewOpenPitEngineBuildFunc).
type Config struct {
	SQLitePath string
	MasterKey  *secret.MasterKey
}

// Register populates b with the Pit Officer concrete implementations built
// from cfg. The composition serves domain.DefaultRealm through one local node.
func Register(b *frameworkapp.Builder, cfg Config) error {
	if b == nil {
		return errors.New("officer: nil builder")
	}
	b.SetStoreFactory(func() (store.Store, error) {
		var opts []sqlite.Option
		if cfg.MasterKey != nil {
			opts = append(opts, sqlite.WithMasterKey(*cfg.MasterKey))
		}
		return sqlite.New(cfg.SQLitePath, domain.DefaultRealm, opts...)
	})
	b.SetNodeBuilder(func(
		ctx context.Context,
		st store.Store,
		fatalHook frameworkapp.FatalShutdownHook,
	) (node.Node, fwengine.Engine, error) {
		return node.NewLocalNode(
			ctx,
			domain.DefaultRealm,
			st,
			engine.NewOpenPitEngineBuildFunc(),
			fatalHook,
		)
	})
	b.SetSigningFactory(func(st store.RealmStore) (fwsigning.Service, error) {
		return signing.New(st)
	})
	b.SetAuthorizer(httpx.AllowAll{})
	b.SetCallerResolver(func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	})
	for _, provider := range appmarketdata.FirstPartyProviders() {
		if err := b.RegisterMarketDataProvider(provider); err != nil {
			return fmt.Errorf("officer: register market data provider: %w", err)
		}
	}
	b.SetServiceFactory(func(
		n node.Node,
		md fwbackend.MarketDataRuntime,
		signer fwsigning.Service,
		registry *fwmarketdata.Registry,
		mcpRegistry *frameworkmcp.ToolRegistry,
	) (fwbackend.ControlPlane, error) {
		service, err := fwbackend.New(
			n,
			md,
			signer,
			engine.LockSettlementPrice,
			fwbackend.WithMarketDataRegistry(registry),
			fwbackend.WithMCPCatalogProvider(mcpRegistry),
		)
		if err != nil {
			return nil, err
		}
		return service, nil
	})
	b.SetRouteConfigBuilder(httpapi.RouteConfig)
	b.AddToolRegistrar(mcptools.RegisterTools)
	b.SetSPAFactory(web.Dist)
	return nil
}
