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

import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { fetchSession, type AccessMode } from "@/api/access";
import type { AccountMode } from "@/api/accountMode";
import type { BuildStatus } from "@/api/build";
import App from "@/App";
import { useStartupAdmit } from "@/components/StartupGate";
import { panelSession } from "@/lib/panelSession";
import { jsonResponse, renderWithApi } from "@/test/apiClient";
import { ThemeProvider } from "@/theme/ThemeProvider";

// The shell's navigation and pages are tested on their own; this test covers
// the routes to the wizard and the shell.
vi.mock("@/framework", async () => {
  const actual =
    await vi.importActual<typeof import("@/framework")>("@/framework");
  return { ...actual, AppRoutes: () => null, Sidebar: () => null };
});

// The startup gate asks GET /session before the account mode; it is tested on
// its own, and these cases run in open access mode unless one answers the
// setup state.
vi.mock("@/api/access", async () => {
  const actual =
    await vi.importActual<typeof import("@/api/access")>("@/api/access");
  return {
    ...actual,
    fetchSession: vi
      .fn()
      .mockResolvedValue({ accessMode: "open", authenticated: false }),
  };
});

// The wizard is tested on its own. Its stub admits the browser in token access
// mode and moves on within /setup, as the wizard's last steps do; the review's
// stub names the access mode it was given.
vi.mock("@/pages/Setup", () => ({
  SetupReview: function SetupReviewStub({
    accessMode,
  }: {
    accessMode: AccessMode;
  }) {
    return <p>The review stub in {accessMode}</p>;
  },
  Setup: function SetupStub() {
    const admit = useStartupAdmit();
    const navigate = useNavigate();
    return (
      <button
        type="button"
        onClick={() => {
          admit("token");
          void navigate("/setup?x=1");
        }}
      >
        Finish the setup stub
      </button>
    );
  },
}));

const SETUP_STUB = "Finish the setup stub";

function LocationProbe() {
  const location = useLocation();
  return (
    <output aria-label="Current location">
      {location.pathname + location.search}
    </output>
  );
}

function currentLocation() {
  return screen.getByLabelText("Current location").textContent;
}

// Renders the app again on a click, as any update above it does; a navigation
// alone does not render a route element again.
function RerenderingApp() {
  const [, setRenders] = useState(0);
  return (
    <>
      <App />
      <button type="button" onClick={() => setRenders((n) => n + 1)}>
        Render the app again
      </button>
    </>
  );
}

function renderApp(
  mode: AccountMode | null,
  path = "/",
  build: BuildStatus = {
    release: true,
    phase: "ready",
    releaseDatabaseExists: false,
  },
) {
  const fetchMock = vi.fn(async (url: string) =>
    jsonResponse(
      url === "/app/api/v1/build"
        ? build
        : {
            mode,
            account: "",
            features: { accounts: true, account: "" },
            restartRequired: false,
            live: false,
          },
    ),
  );
  renderWithApi(
    <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
      <MemoryRouter initialEntries={[path]}>
        <RerenderingApp />
        <LocationProbe />
      </MemoryRouter>
    </ThemeProvider>,
    { fetch: fetchMock as unknown as typeof fetch },
  );
  return { fetchMock };
}

// The next GET /session finds Officer in the first-run setup.
function answerSetupState() {
  vi.mocked(fetchSession).mockResolvedValueOnce({
    accessMode: "setup",
    authenticated: false,
  });
}

// Lets pending answers and the renders they cause settle.
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

describe("App", () => {
  it("keeps the non-release banner on /service", async () => {
    renderApp("multi", "/service", {
      release: false,
      phase: "ready",
      releaseDatabaseExists: false,
    });
    expect(await screen.findByText("non-release")).toBeVisible();
    await settle();
    expect(currentLocation()).toBe("/service");
    expect(screen.getByText("non-release")).toBeVisible();
  });

  it("keeps the non-release banner above login", async () => {
    vi.mocked(fetchSession).mockResolvedValueOnce({
      accessMode: "token",
      authenticated: false,
    });
    renderApp("multi", "/", {
      release: false,
      phase: "ready",
      releaseDatabaseExists: false,
    });
    expect(
      await screen.findByRole("heading", { name: "Log in to the panel" }),
    ).toBeInTheDocument();
    expect(screen.getByText("non-release")).toBeVisible();
  });

  it("does not ask for a session before the build is ready", async () => {
    const sessionCalls = vi.mocked(fetchSession).mock.calls.length;
    const { fetchMock } = renderApp("multi", "/", {
      release: false,
      phase: "choice",
      releaseDatabaseExists: false,
    });
    expect(
      await screen.findByRole("heading", {
        name: "Choose this build's database",
      }),
    ).toBeInTheDocument();
    expect(vi.mocked(fetchSession).mock.calls).toHaveLength(sessionCalls);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it.each([null, "multi"] as const)(
    "opens no dialog over the shell with the account mode %s",
    async (mode) => {
      const { fetchMock } = renderApp(mode);

      // The build check, scope load and its first poll.
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
      await settle();

      expect(currentLocation()).toBe("/");
      expect(screen.queryAllByRole("dialog", { hidden: true })).toHaveLength(0);
    },
  );
});

describe("App routes", () => {
  it.each(["/", "/accounts", "/setup"])(
    "renders the wizard at /setup for %s in the setup state",
    async (path) => {
      answerSetupState();
      renderApp(null, path);

      expect(
        await screen.findByRole("button", { name: SETUP_STUB }),
      ).toBeInTheDocument();
      expect(currentLocation()).toBe("/setup");
    },
  );

  it.each(["open", "token"] as const)(
    "renders the review at /setup once setup is complete, in %s access",
    async (accessMode) => {
      vi.mocked(fetchSession).mockResolvedValueOnce({
        accessMode,
        authenticated: true,
      });
      renderApp("multi", "/setup");

      expect(
        await screen.findByText(`The review stub in ${accessMode}`),
      ).toBeInTheDocument();
      expect(currentLocation()).toBe("/setup");
      await settle();
      expect(
        screen.queryByRole("button", { name: SETUP_STUB }),
      ).not.toBeInTheDocument();
    },
  );

  it("keeps the wizard after it admits the browser and moves on within /setup", async () => {
    const user = userEvent.setup();
    answerSetupState();
    renderApp(null, "/setup");

    await user.click(await screen.findByRole("button", { name: SETUP_STUB }));
    await user.click(
      screen.getByRole("button", { name: "Render the app again" }),
    );
    await settle();

    expect(panelSession().accessMode).toBe("token");
    expect(currentLocation()).toBe("/setup?x=1");
    expect(
      screen.getByRole("button", { name: SETUP_STUB }),
    ).toBeInTheDocument();
  });
});
