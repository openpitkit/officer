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

import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { AppRoutes } from "./AppRoutes";
import { registerRoute, unregisterRoute } from "./registries/routes";

function LocationProbe() {
  const { pathname, search, hash } = useLocation();
  return <output data-testid="location">{pathname + search + hash}</output>;
}

describe("AppRoutes", () => {
  it("keeps an unknown URL and renders a not-found page", () => {
    render(
      <MemoryRouter initialEntries={["/does-not-exist"]}>
        <AppRoutes />
        <LocationProbe />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("location")).toHaveTextContent("/does-not-exist");
    expect(
      screen.getByRole("heading", { name: /not found/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("main")).toBeInTheDocument();
  });

  it.each([
    ["/limits?account=acc-1#active", "/policies?account=acc-1#active"],
    ["/trading?status=open#desk", "/orders?status=open#desk"],
  ])("redirects %s to %s", async (source, target) => {
    const id = `redirect-${source}`;
    registerRoute({
      id,
      path: source.split("?", 1)[0]!,
      order: 1,
      redirectTo: target.split("?", 1)[0]!,
    });
    render(
      <MemoryRouter initialEntries={[source]}>
        <AppRoutes />
        <LocationProbe />
      </MemoryRouter>,
    );

    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(target),
    );
    unregisterRoute(id);
  });
});
