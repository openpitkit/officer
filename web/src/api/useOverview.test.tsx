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

import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { useOverview } from "@/api/useOverview";
import { renderWithApi } from "@/test/apiClient";

function OverviewProbe() {
  const { load } = useOverview();
  return <span>{load.state}</span>;
}

function overviewResponse(): Response {
  return new Response(
    JSON.stringify({
      counts: {
        accounts: 0,
        accountsActive: 0,
        groups: 0,
        groupsActive: 0,
        limits: 0,
        ordersActive: 0,
        ordersToday: 0,
        ordersTotal: 0,
      },
      activity: [],
    }),
    {
      status: 200,
      headers: { "Content-Type": "application/json" },
    },
  );
}

describe("useOverview", () => {
  it("uses the injected API base URL and headers", async () => {
    const fetchMock = vi.fn().mockResolvedValue(overviewResponse());

    renderWithApi(<OverviewProbe />, {
      fetch: fetchMock as unknown as typeof fetch,
      config: {
        baseUrl: "https://host.example/officer/api",
        headers: { "X-Static-Auth": "static-token" },
        getHeaders: () => ({ "X-Dynamic-Auth": "dynamic-token" }),
      },
    });

    await waitFor(() => {
      expect(screen.getByText("ready")).toBeInTheDocument();
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/^https:\/\/host\.example\/officer\/api\/overview\?/);
    expect(init.headers).toEqual(
      expect.objectContaining({
        "X-Static-Auth": "static-token",
        "X-Dynamic-Auth": "dynamic-token",
      }),
    );
  });
});
