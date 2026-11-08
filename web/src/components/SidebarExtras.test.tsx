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
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Fragment } from "react";

import type { CredentialStorage } from "@/api/types";
import { useCredentialStorage } from "@/api/useCredentialStorage";
import { useMarketData } from "@/api/useMarketData";
import { NonReleaseContext } from "@/components/non-release-context";
import {
  AboutNavEntry,
  LogoutNavEntry,
  RestartRequiredNavEntry,
  SensitiveDataNavEntry,
} from "@/components/SidebarExtras";
import { getNav } from "@/framework";
import { onCheckSession, setPanelSession } from "@/lib/panelSession";
import { registerOfficerDefaults } from "@/register";
import { jsonResponse, renderWithApi } from "@/test/apiClient";

registerOfficerDefaults();

vi.mock("@/api/useMarketData", () => ({ useMarketData: vi.fn() }));
vi.mock("@/api/useCredentialStorage", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/useCredentialStorage")>()),
  useCredentialStorage: vi.fn(),
}));
vi.mock("@/components/sidebar-context", () => ({
  useSidebar: () => ({ close: vi.fn() }),
}));

const useMarketDataMock = vi.mocked(useMarketData);
const useCredentialStorageMock = vi.mocked(useCredentialStorage);

const plaqueTitle = "Sensitive data is stored unencrypted";

function storage(flags: Partial<CredentialStorage>) {
  useCredentialStorageMock.mockReturnValue({
    load: {
      state: "ready",
      data: {
        credentialsSealed: false,
        plaintextTradingKeys: false,
        plaintextMarketDataKeys: false,
        sealedAt: null,
        resealedSecrets: 0,
        ...flags,
      },
      error: null,
    },
    reload: vi.fn(),
  });
}

function LocationProbe() {
  const location = useLocation();
  return (
    <output aria-label="Current location">
      {location.pathname + location.hash}
    </output>
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  storage({ plaintextTradingKeys: true });
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
});

describe("SensitiveDataNavEntry", () => {
  it.each([
    ["trading keys", { plaintextTradingKeys: true }],
    ["market-data keys", { plaintextMarketDataKeys: true }],
  ])(
    "shows the yellow plaque for plain-text %s and leads to Service › Security",
    async (_, flags) => {
      storage(flags);
      const user = userEvent.setup();
      render(
        <MemoryRouter initialEntries={["/accounts"]}>
          <SensitiveDataNavEntry />
          <LocationProbe />
        </MemoryRouter>,
      );

      const plaque = screen.getByRole("link", { name: /^Sensitive data/ });
      expect(plaque).toHaveAttribute("href", "/service#security");
      expect(plaque).toHaveTextContent(plaqueTitle);
      expect(plaque).toHaveTextContent("How to set a master key");
      expect(plaque.firstElementChild).toHaveClass("border-[var(--warn)]");

      await user.click(plaque);

      expect(screen.getByLabelText("Current location")).toHaveTextContent(
        "/service#security",
      );
    },
  );

  it.each([
    ["sealed", { credentialsSealed: true }],
    ["unsealed with no stored keys", {}],
  ])("shows no plaque while storage is %s", (_, flags) => {
    storage(flags);
    render(
      <MemoryRouter initialEntries={["/accounts"]}>
        <SensitiveDataNavEntry />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("shows no plaque before the storage is loaded", () => {
    useCredentialStorageMock.mockReturnValue({
      load: { state: "loading", data: null, error: null },
      reload: vi.fn(),
    });
    render(
      <MemoryRouter initialEntries={["/accounts"]}>
        <SensitiveDataNavEntry />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("leaves the warning and its request to the Service page", () => {
    render(
      <MemoryRouter initialEntries={["/service"]}>
        <SensitiveDataNavEntry />
      </MemoryRouter>,
    );

    expect(useCredentialStorageMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it.each([
    ["a release build shows it alone", false, [plaqueTitle]],
    [
      "a non-release build shows it above the non-release plaque",
      true,
      [plaqueTitle, "non-release"],
    ],
  ])("sits in the footer where %s", (_, nonRelease, titles) => {
    const ids = ["sensitive-data", "non-release"];
    const footer = getNav("footer");
    const order = footer.map((entry) => entry.id);
    expect(order.indexOf("sensitive-data")).toBe(
      order.indexOf("non-release") - 1,
    );
    render(
      <NonReleaseContext.Provider
        value={{ nonRelease, showInSidebar: vi.fn() }}
      >
        <MemoryRouter initialEntries={["/accounts"]}>
          {footer
            .filter((entry) => ids.includes(entry.id))
            .map((entry) => (
              <Fragment key={entry.id}>{entry.render?.()}</Fragment>
            ))}
        </MemoryRouter>
      </NonReleaseContext.Provider>,
    );

    const plaques = screen.getAllByRole("link");
    expect(plaques).toHaveLength(titles.length);
    titles.forEach((title, index) => {
      expect(plaques[index]).toHaveTextContent(new RegExp(title, "i"));
    });
  });
});

describe("AboutNavEntry", () => {
  it("leads to the first-run guide at /setup", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/accounts"]}>
        <AboutNavEntry />
        <LocationProbe />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole("button", { name: "About" }));
    await user.click(
      await screen.findByRole("link", { name: "Open first-run guide" }),
    );

    expect(screen.getByLabelText("Current location")).toHaveTextContent(
      "/setup",
    );
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
