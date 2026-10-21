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

import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useMarketData } from "@/api/useMarketData";
import { useService } from "@/api/useService";
import {
  LogoutNavEntry,
  NonReleaseNavEntry,
  RestartRequiredNavEntry,
} from "@/components/SidebarExtras";
import { getNav } from "@/framework";
import { onCheckSession, setPanelSession } from "@/lib/panelSession";
// Side-effect import: the application's own registration.
import "@/register";
import { jsonResponse, renderWithApi } from "@/test/apiClient";

vi.mock("@/api/useMarketData", () => ({ useMarketData: vi.fn() }));
vi.mock("@/api/useService", () => ({ useService: vi.fn() }));
vi.mock("@/components/sidebar-context", () => ({
  useSidebar: () => ({ close: vi.fn() }),
}));

const useMarketDataMock = vi.mocked(useMarketData);
const useServiceMock = vi.mocked(useService);

beforeEach(() => {
  vi.clearAllMocks();
  useMarketDataMock.mockReturnValue({
    load: {
      state: "ready",
      data: {
        providers: [],
        instances: [],
        freshnessSeconds: 30,
        restartRequired: true,
      },
      error: null,
    },
    reload: vi.fn(),
  });
  useServiceMock.mockReturnValue({
    load: {
      state: "ready",
      data: {
        name: "Pit Officer",
        engineVersion: "dev",
        engineBuildProfile: "debug",
        release: false,
        database: { path: "officer.db", reachable: true },
      },
      error: null,
    },
    reload: vi.fn(),
  });
});

describe("SidebarExtras page request ownership", () => {
  it("leaves the market-data request to its destination page", () => {
    render(
      <MemoryRouter initialEntries={["/market-data"]}>
        <RestartRequiredNavEntry />
      </MemoryRouter>,
    );

    expect(useMarketDataMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("polls market data and shows the warning away from its page", () => {
    render(
      <MemoryRouter initialEntries={["/accounts"]}>
        <RestartRequiredNavEntry />
      </MemoryRouter>,
    );

    expect(useMarketDataMock).toHaveBeenCalledOnce();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/market-data");
  });

  it("leaves the service request to its destination page", () => {
    render(
      <MemoryRouter initialEntries={["/service"]}>
        <NonReleaseNavEntry />
      </MemoryRouter>,
    );

    expect(useServiceMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("polls service state and shows the warning away from its page", () => {
    render(
      <MemoryRouter initialEntries={["/accounts"]}>
        <NonReleaseNavEntry />
      </MemoryRouter>,
    );

    expect(useServiceMock).toHaveBeenCalledOnce();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/service");
  });
});

describe("LogoutNavEntry", () => {
  const check = vi.fn();
  let unsubscribe = () => {};

  beforeEach(() => {
    unsubscribe = onCheckSession(check);
  });

  afterEach(() => {
    unsubscribe();
  });

  function renderLogout(answer: () => Response | Promise<Response>) {
    const fetchMock = vi.fn<
      (url: string, init: RequestInit) => Promise<Response>
    >(async () => answer());
    renderWithApi(<LogoutNavEntry />, {
      fetch: fetchMock as unknown as typeof fetch,
    });
    return { fetchMock };
  }

  function logoutButton() {
    return screen.getByRole("button", { name: "Log out" });
  }

  it("sits between Service and About and shows only in token access mode", () => {
    const footer = getNav("footer");
    const ids = footer.map((entry) => entry.id);
    expect(ids.indexOf("logout")).toBeGreaterThan(ids.indexOf("service"));
    expect(ids.indexOf("logout")).toBeLessThan(ids.indexOf("about"));
    const entry = footer.find((candidate) => candidate.id === "logout");

    setPanelSession("token");
    expect(entry?.when?.()).toBe(true);

    setPanelSession("open");
    expect(entry?.when?.()).toBe(false);
  });

  it("ends the session with DELETE /session, then asks for a session check", async () => {
    const user = userEvent.setup();
    let answer: (response: Response) => void = () => {};
    const { fetchMock } = renderLogout(
      () =>
        new Promise<Response>((resolve) => {
          answer = resolve;
        }),
    );

    await user.click(logoutButton());

    expect(logoutButton()).toBeDisabled();
    expect(check).not.toHaveBeenCalled();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/app/api/v1/session");
    expect(init.method).toBe("DELETE");

    await act(async () => {
      answer(new Response(null, { status: 204 }));
    });

    expect(check).toHaveBeenCalledTimes(1);
  });

  it("shows a failure in the entry and keeps the session", async () => {
    const user = userEvent.setup();
    renderLogout(() =>
      jsonResponse(
        { error: { code: "internal", message: "database is locked" } },
        500,
      ),
    );

    await user.click(logoutButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "database is locked",
    );
    expect(check).not.toHaveBeenCalled();
    expect(logoutButton()).toBeEnabled();
  });
});
