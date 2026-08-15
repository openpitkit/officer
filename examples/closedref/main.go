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
	"net/http"

	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/marketdata"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/openapp"
)

const hiddenRoutePermission = "closedref.hidden.route"

type referenceComposition struct {
	Builder    *frameworkapp.Builder
	Authorizer httpx.Authorizer
}

func main() {
	_ = newReferenceComposition()
}

func newReferenceComposition() referenceComposition {
	authorizer := newDenyOneAuthorizer(hiddenRoutePermission, hiddenToolID)

	builder := frameworkapp.NewBuilder()
	openapp.Register(builder)
	builder.SetAuthorizer(authorizer)
	_ = builder.RegisterMarketDataProvider(privateProvider())
	builder.AddToolRegistrar(registerReferenceTools)
	_ = builder.WrapRouteConfigBuilder(func(
		open frameworkapp.RouteConfigBuilder,
	) frameworkapp.RouteConfigBuilder {
		return func(
			svc backend.ControlPlane,
			logs httpx.LogSource,
		) frameworkapp.RouteConfig {
			cfg := open(svc, logs)
			composeReferenceRoutes(cfg.Routes)
			cfg.Authorizer = authorizer
			return cfg
		}
	})

	return referenceComposition{
		Builder:    builder,
		Authorizer: authorizer,
	}
}

var _ marketdata.Connector = (*privateConnector)(nil)
var _ http.HandlerFunc = privateRoute
