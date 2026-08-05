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

import { describe, expect, it } from "vitest";

import { createAppRouteManifest } from "./openRoutes";

describe("app route manifest", () => {
  it("lists canonical operator pages and compatibility redirects", () => {
    expect(createAppRouteManifest()).toEqual({
      routes: [
        { id: "dashboard", path: "/", kind: "canonical" },
        { id: "accounts", path: "/accounts", kind: "canonical" },
        { id: "policies", path: "/policies", kind: "canonical" },
        {
          id: "limits-redirect",
          path: "/limits",
          kind: "redirect",
          target: "/policies",
        },
        { id: "assets", path: "/assets", kind: "canonical" },
        { id: "positions", path: "/positions", kind: "canonical" },
        { id: "orders", path: "/orders", kind: "canonical" },
        {
          id: "trading-redirect",
          path: "/trading",
          kind: "redirect",
          target: "/orders",
        },
        { id: "market-data", path: "/market-data", kind: "canonical" },
        { id: "audit", path: "/audit", kind: "canonical" },
        { id: "mcp-access", path: "/mcp-access", kind: "canonical" },
        { id: "signing-keys", path: "/signing-keys", kind: "canonical" },
        { id: "service", path: "/service", kind: "canonical" },
      ],
    });
  });
});
