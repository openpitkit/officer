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

export interface OpenRouteDefinition {
  id: string;
  path: string;
  order: number;
  redirectTo?: string;
}

export interface AppRouteManifestEntry {
  id: string;
  path: string;
  kind: "canonical" | "redirect";
  target?: string;
}

// This data-only registry is shared by the React runtime and Vite's generated
// manifest asset. Keeping components out lets the build config import it
// without pulling browser-only modules into Node.
export const openRoutes = [
  { id: "dashboard", path: "/", order: 10 },
  { id: "accounts", path: "/accounts", order: 20 },
  { id: "policies", path: "/policies", order: 30 },
  {
    id: "limits-redirect",
    path: "/limits",
    order: 40,
    redirectTo: "/policies",
  },
  { id: "assets", path: "/assets", order: 45 },
  { id: "positions", path: "/positions", order: 50 },
  { id: "orders", path: "/orders", order: 60 },
  {
    id: "trading-redirect",
    path: "/trading",
    order: 70,
    redirectTo: "/orders",
  },
  { id: "market-data", path: "/market-data", order: 80 },
  { id: "audit", path: "/audit", order: 90 },
  { id: "mcp-access", path: "/mcp-access", order: 100 },
  { id: "signing-keys", path: "/signing-keys", order: 110 },
  { id: "service", path: "/service", order: 120 },
] as const satisfies readonly OpenRouteDefinition[];

// This shape is a cross-repository contract: the officer-test Playwright
// harness fetches it from /app-route-manifest.json. Its consumer is outside
// this repository, so it must not be removed as an apparently orphaned asset.
export function createAppRouteManifest(): {
  routes: AppRouteManifestEntry[];
} {
  return {
    routes: openRoutes.map((route) =>
      "redirectTo" in route
        ? {
            id: route.id,
            path: route.path,
            kind: "redirect" as const,
            target: route.redirectTo,
          }
        : {
            id: route.id,
            path: route.path,
            kind: "canonical" as const,
          },
    ),
  };
}
