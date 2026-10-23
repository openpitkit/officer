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

import {
  act,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AccountFeatures, AccountMode } from "@/api/accountMode";
import { LogoutNavEntry } from "@/components/SidebarExtras";
import { StartupGate, useStartupAdmit } from "@/components/StartupGate";
import { ApiClientProvider } from "@/framework";
import { accountScope } from "@/lib/accountScope";
import {
  checkSession,
  panelSession,
  setPanelSession,
} from "@/lib/panelSession";
import { errorResponse, jsonResponse, renderWithApi } from "@/test/apiClient";

const HIDDEN: AccountFeatures = { accounts: false, account: "main" };

function accountModeResponse(
  features: AccountFeatures,
  mode: AccountMode = features.accounts ? "multi" : "single",
): Response {
  return jsonResponse({
    mode,
    account: features.account,
    features,
    restartRequired: mode !== (features.accounts ? "multi" : "single"),
  });
}

// Reads the scope during its own first render, so it shows the scope the
// gate had set before rendering it.
function ScopeProbe() {
  return <p>{`scope:${accountScope().account}`}</p>;
}

// The gate asks GET /session before the account mode. The account-mode cases
// run in open access mode, so `fetchMock` sees only the account-mode requests.
function answeringOpenSession(
  fetchMock: ReturnType<typeof vi.fn>,
): typeof fetch {
  return (async (url: string, init: RequestInit) =>
    url === "/app/api/v1/session"
      ? jsonResponse({
          accessMode: "open",
          setupComplete: true,
          authenticated: false,
        })
      : fetchMock(url, init)) as unknown as typeof fetch;
}

function renderGate(fetchMock: ReturnType<typeof vi.fn>) {
  return renderWithApi(
    <StartupGate>
      <ScopeProbe />
    </StartupGate>,
    { fetch: answeringOpenSession(fetchMock) },
  );
}

describe("StartupGate", () => {
  it("hides the children while the first load is pending", async () => {
    const fetchMock = vi.fn(() => new Promise<Response>(() => {}));

    renderGate(fetchMock);

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/app/api/v1/account-mode",
        expect.objectContaining({ method: "GET" }),
      ),
    );
    const status = screen.getByRole("status", { name: "Loading" });
    expect(status).toHaveAttribute("aria-busy", "true");
    expect(screen.queryByText(/^scope:/)).not.toBeInTheDocument();
  });

  it("sets the scope from the response before the children render", async () => {
    const fetchMock = vi.fn(async () => accountModeResponse(HIDDEN));

    renderGate(fetchMock);

    expect(await screen.findByText("scope:main")).toBeInTheDocument();
    expect(accountScope().showsAccounts).toBe(false);
    expect(accountScope().accountFor("acc-7")).toBe("main");
  });

  it("shows the error when the first load fails and retries on demand", async () => {
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("connection refused"))
      .mockImplementation(async () => accountModeResponse(HIDDEN));

    renderGate(fetchMock);

    expect(
      await screen.findByText("Could not load the account mode"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/^scope:/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("scope:main")).toBeInTheDocument();
  });

  describe("on the poll clock", () => {
    const reload = vi.fn();

    beforeEach(() => {
      vi.useFakeTimers();
      vi.stubGlobal("location", { ...window.location, reload });
    });

    afterEach(() => {
      vi.unstubAllGlobals();
      vi.useRealTimers();
      reload.mockReset();
    });

    // Lets pending answers, decoding and React commits settle without moving
    // the clock to the next poll.
    async function settle() {
      await act(() => vi.advanceTimersByTimeAsync(0));
    }

    // Renders the gate and settles its first load and the first poll after it.
    async function renderLoaded(fetchMock: ReturnType<typeof vi.fn>) {
      await act(async () => {
        renderGate(fetchMock);
      });
      await settle();
      expect(screen.getByText("scope:main")).toBeInTheDocument();
      expect(fetchMock).toHaveBeenCalledTimes(2);
    }

    it("loads the shell when the first answer is slower than the poll interval", async () => {
      const fetchMock = vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            setTimeout(() => resolve(accountModeResponse(HIDDEN)), 6000);
          }),
      );

      await act(async () => {
        renderGate(fetchMock);
      });
      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(screen.getByRole("status", { name: "Loading" })).toBeVisible();

      await act(() => vi.advanceTimersByTimeAsync(1000));
      await settle();
      expect(screen.getByText("scope:main")).toBeInTheDocument();
    });

    it.each([
      ["accounts and the account", { accounts: true, account: "" }],
      ["only the account", { accounts: false, account: "other" }],
    ])(
      "reloads the page when a later poll changes %s",
      async (_change, changed) => {
        const fetchMock = vi
          .fn()
          .mockImplementationOnce(async () => accountModeResponse(HIDDEN))
          .mockImplementation(async () => accountModeResponse(changed));

        await renderLoaded(fetchMock);

        expect(reload).toHaveBeenCalledTimes(1);
      },
    );

    it("keeps its children when later polls store another mode with the same features", async () => {
      const fetchMock = vi
        .fn()
        .mockImplementationOnce(async () => accountModeResponse(HIDDEN))
        .mockImplementation(async () => accountModeResponse(HIDDEN, "multi"));

      await renderLoaded(fetchMock);
      await act(() => vi.advanceTimersByTimeAsync(5000));

      expect(fetchMock).toHaveBeenCalledTimes(3);
      expect(screen.getByText("scope:main")).toBeInTheDocument();
      expect(reload).not.toHaveBeenCalled();
    });

    it("keeps the page when later polls report the same features", async () => {
      const fetchMock = vi.fn(async () => accountModeResponse(HIDDEN));

      await renderLoaded(fetchMock);
      await act(() => vi.advanceTimersByTimeAsync(5000));

      expect(fetchMock).toHaveBeenCalledTimes(3);
      expect(reload).not.toHaveBeenCalled();
      expect(
        screen.queryByRole("status", { name: "Stale data" }),
      ).not.toBeInTheDocument();
      expect(accountScope().account).toBe("main");
    });

    it("marks the scope stale while polls fail and clears it on recovery", async () => {
      const fetchMock = vi
        .fn()
        .mockImplementationOnce(async () => accountModeResponse(HIDDEN))
        .mockRejectedValueOnce(new TypeError("connection refused"))
        .mockImplementationOnce(
          async () =>
            new Response(JSON.stringify({ mode: "solo" }), { status: 200 }),
        )
        .mockImplementation(async () => accountModeResponse(HIDDEN));

      await renderLoaded(fetchMock);
      expect(
        screen.getByRole("status", { name: "Stale data" }),
      ).toHaveTextContent("connection refused");

      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(
        screen.getByRole("status", { name: "Stale data" }),
      ).toHaveTextContent("account mode response: mode must be");
      expect(screen.getByText("scope:main")).toBeInTheDocument();

      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(
        screen.queryByRole("status", { name: "Stale data" }),
      ).not.toBeInTheDocument();
      expect(reload).not.toHaveBeenCalled();
    });
  });
});

describe("StartupGate session step", () => {
  type Answer = () => Response | Promise<Response>;

  function routeOf(url: string, init: RequestInit): string {
    return `${init.method} ${url.replace("/app/api/v1", "")}`;
  }

  // Answers each `METHOD path` from its own list, repeating the last answer;
  // a request with no list fails.
  function answering(table: Record<string, Answer[]>) {
    const answered = new Map<string, number>();
    return vi.fn(async (url: string, init: RequestInit) => {
      const route = routeOf(url, init);
      const answers = table[route];
      if (answers === undefined) {
        throw new Error(`unexpected request ${route}`);
      }
      const count = answered.get(route) ?? 0;
      answered.set(route, count + 1);
      return answers[Math.min(count, answers.length - 1)]();
    });
  }

  function requestsTo(fetchMock: ReturnType<typeof answering>, route: string) {
    return fetchMock.mock.calls.filter(
      ([url, init]) => routeOf(url, init) === route,
    ).length;
  }

  function session(accessMode: "open" | "token", authenticated: boolean) {
    return () =>
      jsonResponse({ accessMode, setupComplete: true, authenticated });
  }

  // The first-run wizard is not complete.
  const setupState = () =>
    jsonResponse({
      accessMode: "setup",
      setupComplete: false,
      authenticated: false,
    });

  // An answer the case releases when it chooses.
  function held() {
    let release: (response: Response) => void = () => {};
    const response = new Promise<Response>((resolve) => {
      release = resolve;
    });
    return { answer: () => response, release };
  }

  const hiddenAccounts = () => accountModeResponse(HIDDEN);
  const noContent = () => new Response(null, { status: 204 });
  const refused = () => Promise.reject(new TypeError("connection refused"));

  // Counts its mounts, so a case sees whether the gate kept the shell.
  const mounts = vi.fn();
  function ShellProbe() {
    useEffect(() => {
      mounts();
    }, []);
    return <p>{`scope:${accountScope().account}`}</p>;
  }

  // Admits the browser in token access mode on a click, as a screen that
  // switched the access mode itself does.
  function AdmitInTokenAccess() {
    const admit = useStartupAdmit();
    return (
      <button type="button" onClick={() => admit("token")}>
        Admit in token access
      </button>
    );
  }

  function renderSessionGate(fetchMock: ReturnType<typeof answering>) {
    return renderWithApi(
      <StartupGate>
        <ShellProbe />
      </StartupGate>,
      {
        fetch: fetchMock as unknown as typeof fetch,
        config: { onUnauthorized: checkSession },
      },
    );
  }

  // Lets pending answers and the renders they cause settle.
  async function settle() {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  const LOGIN_TITLE = "Log in to the panel";
  const RECOVERY_TITLE = "Reset the panel password";
  const SESSION_ERROR = "Could not check the panel session";

  beforeEach(() => {
    mounts.mockReset();
  });

  it.each([
    ["open access", session("open", false), "open"],
    ["a token session", session("token", true), "token"],
  ])("renders the shell for %s", async (_case, answer, accessMode) => {
    const fetchMock = answering({
      "GET /session": [answer],
      "GET /account-mode": [hiddenAccounts],
    });

    renderSessionGate(fetchMock);

    expect(await screen.findByText("scope:main")).toBeInTheDocument();
    expect(panelSession().accessMode).toBe(accessMode);
  });

  it("asks GET /session once and leaves it while the account mode polls", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = answering({
        "GET /session": [session("open", false)],
        "GET /account-mode": [hiddenAccounts],
      });
      await act(async () => {
        renderSessionGate(fetchMock);
      });
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(screen.getByText("scope:main")).toBeInTheDocument();

      await act(() => vi.advanceTimersByTimeAsync(15_000));

      expect(requestsTo(fetchMock, "GET /account-mode")).toBeGreaterThan(2);
      expect(requestsTo(fetchMock, "GET /session")).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows the login screen without a session, before any account-mode request", async () => {
    const fetchMock = answering({ "GET /session": [session("token", false)] });

    renderSessionGate(fetchMock);

    expect(
      await screen.findByRole("heading", { name: LOGIN_TITLE }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/^scope:/)).not.toBeInTheDocument();
    expect(requestsTo(fetchMock, "GET /account-mode")).toBe(0);
  });

  it("shows the error when the session check fails and retries on demand", async () => {
    const user = userEvent.setup();
    const fetchMock = answering({
      "GET /session": [refused, session("open", false)],
      "GET /account-mode": [hiddenAccounts],
    });

    renderSessionGate(fetchMock);

    expect(await screen.findByText(SESSION_ERROR)).toBeInTheDocument();
    expect(screen.getByText(/connection refused/)).toBeInTheDocument();
    expect(requestsTo(fetchMock, "GET /account-mode")).toBe(0);

    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("scope:main")).toBeInTheDocument();
  });

  it("admits the first-run setup state without the login screen", async () => {
    const fetchMock = answering({
      "GET /session": [setupState],
      "GET /account-mode": [hiddenAccounts],
    });

    renderSessionGate(fetchMock);

    expect(await screen.findByText("scope:main")).toBeInTheDocument();
    expect(panelSession().accessMode).toBe("setup");
    expect(
      screen.queryByRole("heading", { name: LOGIN_TITLE }),
    ).not.toBeInTheDocument();
    expect(requestsTo(fetchMock, "GET /account-mode")).toBeGreaterThan(0);
  });

  it("returns to the login screen when a request answers 401 unauthorized", async () => {
    const fetchMock = answering({
      "GET /session": [session("token", true), session("token", false)],
      "GET /account-mode": [
        hiddenAccounts,
        () => errorResponse(401, "unauthorized"),
      ],
    });

    renderSessionGate(fetchMock);

    expect(
      await screen.findByRole("heading", { name: LOGIN_TITLE }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/^scope:/)).not.toBeInTheDocument();
    expect(mounts).toHaveBeenCalledTimes(1);
    expect(requestsTo(fetchMock, "GET /session")).toBe(2);
  });

  it.each([
    [401, "invalid_password"],
    [401, "invalid_recovery_code"],
    [403, "current_password_invalid"],
    [403, "forbidden"],
  ])(
    "keeps the shell and checks no session when a request answers %i %s",
    async (status, code) => {
      const fetchMock = answering({
        "GET /session": [session("token", true)],
        "GET /account-mode": [
          hiddenAccounts,
          () => errorResponse(status, code),
        ],
      });

      renderSessionGate(fetchMock);

      // The failed poll marks the scope stale and nothing else.
      expect(
        await screen.findByRole("status", { name: "Stale data" }),
      ).toHaveTextContent(code);
      await settle();
      expect(screen.getByText("scope:main")).toBeInTheDocument();
      expect(requestsTo(fetchMock, "GET /session")).toBe(1);
    },
  );

  it("keeps the shell mounted when a check finds the session live", async () => {
    const fetchMock = answering({
      "GET /session": [session("token", true)],
      "GET /account-mode": [hiddenAccounts],
    });
    renderSessionGate(fetchMock);
    expect(await screen.findByText("scope:main")).toBeInTheDocument();

    act(() => checkSession());

    await waitFor(() => expect(requestsTo(fetchMock, "GET /session")).toBe(2));
    await settle();
    expect(screen.getByText("scope:main")).toBeInTheDocument();
    expect(mounts).toHaveBeenCalledTimes(1);
  });

  it("shows the error with a retry when a check fails", async () => {
    const user = userEvent.setup();
    const fetchMock = answering({
      "GET /session": [
        session("token", true),
        refused,
        session("token", false),
      ],
      "GET /account-mode": [hiddenAccounts],
    });
    renderSessionGate(fetchMock);
    expect(await screen.findByText("scope:main")).toBeInTheDocument();

    act(() => checkSession());

    expect(await screen.findByText(SESSION_ERROR)).toBeInTheDocument();
    expect(screen.queryByText(/^scope:/)).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(
      await screen.findByRole("heading", { name: LOGIN_TITLE }),
    ).toBeInTheDocument();
  });

  it("loads the account mode and the shell after a login", async () => {
    const user = userEvent.setup();
    const fetchMock = answering({
      "GET /session": [session("token", false)],
      "POST /session": [noContent],
      "GET /account-mode": [hiddenAccounts],
    });
    renderSessionGate(fetchMock);

    await user.type(
      await screen.findByLabelText("Password"),
      "a long panel password",
    );
    await user.click(screen.getByRole("button", { name: "Log in" }));

    expect(await screen.findByText("scope:main")).toBeInTheDocument();
    expect(panelSession().accessMode).toBe("token");
    expect(requestsTo(fetchMock, "GET /session")).toBe(1);
  });

  it("switches between the login and the recovery screens", async () => {
    const user = userEvent.setup();
    const fetchMock = answering({ "GET /session": [session("token", false)] });
    renderSessionGate(fetchMock);

    await user.click(
      await screen.findByRole("button", { name: "Forgot the password?" }),
    );
    expect(
      screen.getByRole("heading", { name: RECOVERY_TITLE }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(
      screen.getByRole("heading", { name: LOGIN_TITLE }),
    ).toBeInTheDocument();
  });

  it("loads the account mode and the shell after a password reset", async () => {
    const user = userEvent.setup();
    const fetchMock = answering({
      "GET /session": [session("token", false)],
      "POST /session/recovery/reset": [noContent],
      "GET /account-mode": [hiddenAccounts],
    });
    renderSessionGate(fetchMock);
    await user.click(
      await screen.findByRole("button", { name: "Forgot the password?" }),
    );

    await user.type(screen.getByLabelText("Recovery code"), "7KQ4-MZ2P-X9RD");
    await user.type(
      screen.getByLabelText("New password"),
      "a new panel password",
    );
    await user.type(
      screen.getByLabelText("Repeat the new password"),
      "a new panel password",
    );
    await user.click(
      screen.getByRole("button", { name: "Set the new password" }),
    );

    expect(await screen.findByText("scope:main")).toBeInTheDocument();
    expect(panelSession().accessMode).toBe("token");
  });

  it("keeps the recovery screen open when a check finds no session", async () => {
    const user = userEvent.setup();
    const fetchMock = answering({ "GET /session": [session("token", false)] });
    renderSessionGate(fetchMock);
    await user.click(
      await screen.findByRole("button", { name: "Forgot the password?" }),
    );

    act(() => checkSession());

    await waitFor(() => expect(requestsTo(fetchMock, "GET /session")).toBe(2));
    await settle();
    expect(
      screen.getByRole("heading", { name: RECOVERY_TITLE }),
    ).toBeInTheDocument();
  });

  it("runs exactly one more check after the one in flight, for the asks during it", async () => {
    const first = held();
    const fetchMock = answering({
      "GET /session": [
        session("token", true),
        first.answer,
        session("token", false),
      ],
      "GET /account-mode": [hiddenAccounts],
    });
    renderSessionGate(fetchMock);
    expect(await screen.findByText("scope:main")).toBeInTheDocument();

    act(() => checkSession());
    act(() => checkSession());
    act(() => checkSession());
    await settle();
    // Nothing more starts while that check is in flight.
    expect(requestsTo(fetchMock, "GET /session")).toBe(2);

    await act(async () => {
      first.release(session("token", true)());
    });
    await settle();

    expect(
      await screen.findByRole("heading", { name: LOGIN_TITLE }),
    ).toBeInTheDocument();
    expect(requestsTo(fetchMock, "GET /session")).toBe(3);
  });

  it("shows the login screen after a logout although an earlier check answers authenticated", async () => {
    const user = userEvent.setup();
    const before = held();
    const fetchMock = answering({
      "GET /session": [
        session("token", true),
        before.answer,
        session("token", false),
      ],
      "GET /account-mode": [hiddenAccounts],
      "DELETE /session": [noContent],
    });
    renderWithApi(
      <StartupGate>
        <ShellProbe />
        <LogoutNavEntry />
      </StartupGate>,
      {
        fetch: fetchMock as unknown as typeof fetch,
        config: { onUnauthorized: checkSession },
      },
    );
    expect(await screen.findByText("scope:main")).toBeInTheDocument();
    act(() => checkSession());
    await settle();
    expect(requestsTo(fetchMock, "GET /session")).toBe(2);

    await user.click(screen.getByRole("button", { name: "Log out" }));
    await waitFor(() =>
      expect(requestsTo(fetchMock, "DELETE /session")).toBe(1),
    );
    await settle();
    // The check started before the logout answers only now.
    await act(async () => {
      before.release(session("token", true)());
    });

    expect(
      await screen.findByRole("heading", { name: LOGIN_TITLE }),
    ).toBeInTheDocument();
    const routes = fetchMock.mock.calls.map(([url, init]) =>
      routeOf(url, init),
    );
    expect(routes.lastIndexOf("GET /session")).toBeGreaterThan(
      routes.indexOf("DELETE /session"),
    );
  });

  it("drops the answer of a check in flight when it unmounts", async () => {
    setPanelSession("open");
    const pending = held();
    const fetchMock = answering({ "GET /session": [pending.answer] });
    const { unmount } = renderSessionGate(fetchMock);
    await waitFor(() => expect(requestsTo(fetchMock, "GET /session")).toBe(1));

    unmount();
    await act(async () => {
      pending.release(session("token", true)());
    });
    await settle();

    expect(panelSession().accessMode).toBe("open");
    expect(fetchMock.mock.calls[0][1].signal?.aborted).toBe(true);
  });

  it("drops the answer of a check in flight when the API client changes", async () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload });
    try {
      const pending = held();
      const before = answering({ "GET /session": [pending.answer] });
      const after = answering({
        "GET /session": [session("open", false)],
        "GET /account-mode": [hiddenAccounts],
      });
      const gate = (
        <StartupGate>
          <ShellProbe />
        </StartupGate>
      );
      const { rerender } = render(
        <ApiClientProvider
          config={{
            baseUrl: "/app/api/v1",
            fetch: before as unknown as typeof fetch,
          }}
        >
          {gate}
        </ApiClientProvider>,
      );
      await waitFor(() => expect(requestsTo(before, "GET /session")).toBe(1));

      rerender(
        <ApiClientProvider
          config={{
            baseUrl: "/app/api/v1",
            fetch: after as unknown as typeof fetch,
          }}
        >
          {gate}
        </ApiClientProvider>,
      );
      expect(await screen.findByText("scope:main")).toBeInTheDocument();
      await act(async () => {
        pending.release(session("token", false)());
      });
      await settle();

      expect(before.mock.calls[0][1].signal?.aborted).toBe(true);
      expect(screen.getByText("scope:main")).toBeInTheDocument();
      expect(panelSession().accessMode).toBe("open");
      expect(reload).not.toHaveBeenCalled();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  describe("with a check in flight on the login screen", () => {
    async function renderCheckingLogin() {
      const check = held();
      const fetchMock = answering({
        "GET /session": [session("token", false), check.answer],
        "POST /session": [noContent],
        "GET /account-mode": [hiddenAccounts],
      });
      renderSessionGate(fetchMock);
      await screen.findByRole("heading", { name: LOGIN_TITLE });
      act(() => checkSession());
      await settle();
      expect(requestsTo(fetchMock, "GET /session")).toBe(2);
      return check;
    }

    async function logIn() {
      const user = userEvent.setup();
      await user.type(
        screen.getByLabelText("Password"),
        "a-long-panel-password",
      );
      await user.click(screen.getByRole("button", { name: "Log in" }));
    }

    it("keeps the shell when the stale answer arrives after the login", async () => {
      const check = await renderCheckingLogin();

      await logIn();
      expect(await screen.findByText("scope:main")).toBeInTheDocument();
      await act(async () => {
        check.release(session("token", false)());
      });
      await settle();

      expect(screen.getByText("scope:main")).toBeInTheDocument();
      expect(
        screen.queryByRole("heading", { name: LOGIN_TITLE }),
      ).not.toBeInTheDocument();
    });

    it("admits after the login when the answer arrives before it", async () => {
      const check = await renderCheckingLogin();

      await act(async () => {
        check.release(session("token", false)());
      });
      await settle();
      expect(
        screen.getByRole("heading", { name: LOGIN_TITLE }),
      ).toBeInTheDocument();
      await logIn();

      expect(await screen.findByText("scope:main")).toBeInTheDocument();
    });
  });

  describe("when the access mode switches while the page lives", () => {
    const reload = vi.fn();

    beforeEach(() => {
      vi.stubGlobal("location", { ...window.location, reload });
    });

    afterEach(() => {
      vi.unstubAllGlobals();
      reload.mockReset();
    });

    it.each([
      ["from token to open", session("token", true), session("open", false)],
      ["from open to token", session("open", false), session("token", true)],
      ["from setup to token", setupState, session("token", false)],
      ["from open to setup", session("open", false), setupState],
      ["from token to setup", session("token", true), setupState],
    ])(
      "reloads the page on a check that finds it %s",
      async (_case, admitted, later) => {
        const fetchMock = answering({
          "GET /session": [admitted, later],
          "GET /account-mode": [hiddenAccounts],
        });
        renderSessionGate(fetchMock);
        expect(await screen.findByText("scope:main")).toBeInTheDocument();
        expect(reload).not.toHaveBeenCalled();

        act(() => checkSession());

        await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
      },
    );

    it.each([
      ["open access", session("open", false), "open"],
      ["the setup state", setupState, "setup"],
    ])(
      "keeps the page on a check after a screen admits the browser from %s in token access mode",
      async (_case, admitted, accessMode) => {
        const user = userEvent.setup();
        const fetchMock = answering({
          "GET /session": [admitted, session("token", true)],
          "GET /account-mode": [hiddenAccounts],
        });
        renderWithApi(
          <StartupGate>
            <ShellProbe />
            <AdmitInTokenAccess />
          </StartupGate>,
          {
            fetch: fetchMock as unknown as typeof fetch,
            config: { onUnauthorized: checkSession },
          },
        );
        expect(await screen.findByText("scope:main")).toBeInTheDocument();
        expect(panelSession().accessMode).toBe(accessMode);

        await user.click(
          screen.getByRole("button", { name: "Admit in token access" }),
        );
        expect(panelSession().accessMode).toBe("token");

        act(() => checkSession());

        await waitFor(() =>
          expect(requestsTo(fetchMock, "GET /session")).toBe(2),
        );
        await settle();
        expect(reload).not.toHaveBeenCalled();
        expect(screen.getByText("scope:main")).toBeInTheDocument();
        expect(mounts).toHaveBeenCalledTimes(1);
      },
    );

    it("lets a browser on the login screen in once Officer answers it in open access mode", async () => {
      const user = userEvent.setup();
      const fetchMock = answering({
        "GET /session": [session("token", false), session("open", false)],
        "POST /session": [() => errorResponse(409, "conflict")],
        "GET /account-mode": [hiddenAccounts],
      });
      renderSessionGate(fetchMock);

      await user.type(
        await screen.findByLabelText("Password"),
        "a-long-panel-password",
      );
      await user.click(screen.getByRole("button", { name: "Log in" }));

      expect(await screen.findByText("scope:main")).toBeInTheDocument();
      expect(panelSession().accessMode).toBe("open");
      expect(reload).not.toHaveBeenCalled();
    });
  });
});

describe("useStartupAdmit", () => {
  it("throws outside StartupGate", () => {
    expect(() => renderHook(() => useStartupAdmit())).toThrow(
      "useStartupAdmit is used outside StartupGate",
    );
  });
});
