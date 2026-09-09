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

package main

import (
	"fmt"
	"log"
	"net/http"

	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/marketdata"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/officerapp"
)

const hiddenRoutePermission = "customhost.hidden.route"

type customHostComposition struct {
	Builder    *frameworkapp.Builder
	Authorizer httpx.Authorizer
}

func main() {
	if _, err := newCustomHostComposition(); err != nil {
		log.Fatal(err)
	}
}

func newCustomHostComposition() (customHostComposition, error) {
	authorizer := newDenyOneAuthorizer(hiddenRoutePermission, hiddenToolID)

	builder := frameworkapp.NewBuilder()
	if err := officerapp.Register(builder); err != nil {
		return customHostComposition{}, err
	}
	builder.SetAuthorizer(authorizer)
	if err := builder.RegisterMarketDataProvider(hostProvider()); err != nil {
		return customHostComposition{},
			fmt.Errorf("register custom host market data provider: %w", err)
	}
	builder.AddToolRegistrar(registerCustomHostTools)
	if err := builder.WrapRouteConfigBuilder(func(
		base frameworkapp.RouteConfigBuilder,
	) frameworkapp.RouteConfigBuilder {
		return func(
			svc backend.ControlPlane,
			logs httpx.LogSource,
		) frameworkapp.RouteConfig {
			cfg := base(svc, logs)
			composeCustomHostRoutes(cfg.Routes)
			cfg.Authorizer = authorizer
			return cfg
		}
	}); err != nil {
		return customHostComposition{},
			fmt.Errorf("wrap custom host route config builder: %w", err)
	}

	return customHostComposition{
		Builder:    builder,
		Authorizer: authorizer,
	}, nil
}

var _ marketdata.Connector = (*hostConnector)(nil)
var _ http.HandlerFunc = hostRoute
