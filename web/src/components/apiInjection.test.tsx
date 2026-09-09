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
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { BusinessCsvExportDialog } from "@/components/BusinessCsvDialogs";
import { PendingRestartBanner } from "@/components/PendingRestartBanner";
import { WelcomeDialog } from "@/components/WelcomeDialog";
import { renderWithApi } from "@/test/apiClient";
import { ThemeProvider } from "@/theme/ThemeProvider";

const injectedBaseUrl = "https://host.example/officer/api";

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function marketDataResponse(restartRequired: boolean): Response {
  return jsonResponse({
    marketData: {
      freshnessSeconds: 10,
      providers: [],
      instances: [],
      restartRequired,
    },
  });
}

function renderWithInjectedApi(
  ui: React.ReactElement,
  fetchMock: typeof fetch,
) {
  return renderWithApi(ui, {
    fetch: fetchMock,
    config: {
      baseUrl: injectedBaseUrl,
      headers: { "X-Static-Auth": "static-token" },
      getHeaders: () => ({ "X-Dynamic-Auth": "dynamic-token" }),
    },
  });
}

function expectInjectedRequest(call: unknown[], path: string) {
  const [url, init] = call as [string, RequestInit];
  expect(url).toBe(`${injectedBaseUrl}${path}`);
  expect(init.headers).toEqual(
    expect.objectContaining({
      "X-Static-Auth": "static-token",
      "X-Dynamic-Auth": "dynamic-token",
    }),
  );
}

describe("API provider injection", () => {
  it("routes welcome dismissal through the injected API client", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ welcomeSeen: true }));

    renderWithInjectedApi(
      <MemoryRouter>
        <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
          <WelcomeDialog open onOpenChange={vi.fn()} />
        </ThemeProvider>
      </MemoryRouter>,
      fetchMock as unknown as typeof fetch,
    );

    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(
      screen.getByRole("button", { name: /start working in pit officer/i }),
    );

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expectInjectedRequest(fetchMock.mock.calls[0], "/user-settings");
    expect((fetchMock.mock.calls[0] as [string, RequestInit])[1].method).toBe(
      "PUT",
    );
  });

  it("routes the shared restart banner through the injected API client", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(marketDataResponse(true))
      .mockResolvedValueOnce(marketDataResponse(false))
      .mockResolvedValue(marketDataResponse(false));

    renderWithInjectedApi(
      <PendingRestartBanner />,
      fetchMock as unknown as typeof fetch,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: /restart/i }),
    );

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([url]) => url === `${injectedBaseUrl}/market-data/restart`,
        ),
      ).toBe(true),
    );
    const restartCall = fetchMock.mock.calls.find(
      ([url]) => url === `${injectedBaseUrl}/market-data/restart`,
    );
    expect(restartCall).toBeDefined();
    expectInjectedRequest(restartCall ?? [], "/market-data/restart");
    expect((restartCall as [string, RequestInit])[1].method).toBe("POST");
  });

  it("routes business CSV export through the injected API client", async () => {
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: vi.fn(() => "blob:csv"),
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      value: vi.fn(),
    });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const fetchMock = vi.fn().mockResolvedValue(
      new Response("code,title\nacc-1,Main\n", {
        status: 200,
        headers: {
          "Content-Type": "text/csv",
          "Content-Disposition": 'attachment; filename="accounts.csv"',
        },
      }),
    );

    renderWithInjectedApi(
      <BusinessCsvExportDialog entity="accounts" triggerLabel="Open export" />,
      fetchMock as unknown as typeof fetch,
    );

    await userEvent.click(screen.getByRole("button", { name: "Open export" }));
    await userEvent.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expectInjectedRequest(fetchMock.mock.calls[0], "/business-csv/export");
  });
});
