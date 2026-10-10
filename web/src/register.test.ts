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

import { describe, expect, it } from "vitest";

import { getNav, getWidgets } from "@/framework";
// Side-effect import: the application's own registration.
import "@/register";

function indexOf(ids: string[], id: string): number {
  const index = ids.indexOf(id);
  expect(index, `${id} is registered`).toBeGreaterThanOrEqual(0);
  return index;
}

describe("officer default registration", () => {
  it("lists trading connections above market data in the menu", () => {
    const ids = getNav("footer").map((entry) => entry.id);

    expect(indexOf(ids, "trading-connections")).toBeLessThan(
      indexOf(ids, "market-data"),
    );
  });

  it("places the trading connections widget above the market data widget", () => {
    const ids = getWidgets().map((entry) => entry.id);

    expect(indexOf(ids, "trading-connections-card")).toBeLessThan(
      indexOf(ids, "market-data-card"),
    );
  });
});
