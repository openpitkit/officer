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
import { afterEach, describe, expect, it, vi } from "vitest";

import { onCheckSession } from "@/lib/panelSession";
import { Login } from "@/pages/Login";
import { errorResponse, jsonResponse, renderWithApi } from "@/test/apiClient";

// Letters and hyphens only: a URL never percent-encodes them, so a password
// that reached the URL would show there as written.
const PASSWORD = "correct-horse-battery-staple";

function renderLogin(answer: () => Response | Promise<Response>) {
  const fetchMock = vi.fn<
    (url: string, init: RequestInit) => Promise<Response>
  >(async () => answer());
  const onLoggedIn = vi.fn();
  const onForgotPassword = vi.fn();
  renderWithApi(
    <Login onLoggedIn={onLoggedIn} onForgotPassword={onForgotPassword} />,
    { fetch: fetchMock as unknown as typeof fetch },
  );
  return { fetchMock, onLoggedIn, onForgotPassword };
}

function passwordField() {
  return screen.getByLabelText("Password");
}

function submitButton() {
  return screen.getByRole("button", { name: "Log in" });
}

async function attempt(user: ReturnType<typeof userEvent.setup>) {
  await user.type(passwordField(), PASSWORD);
  await user.click(submitButton());
}

describe("Login", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // Runs the countdown out, one second per step, letting each tick render.
  function runOut(seconds: number) {
    for (let i = 0; i < seconds; i++) {
      act(() => {
        vi.advanceTimersByTime(1000);
      });
    }
  }

  function countdownUser() {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  }

  it("sends the password to POST /session and reports the login", async () => {
    const user = userEvent.setup();
    const { fetchMock, onLoggedIn } = renderLogin(
      () => new Response(null, { status: 204 }),
    );

    await attempt(user);

    expect(onLoggedIn).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/app/api/v1/session");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ password: PASSWORD });
    expect(passwordField()).toHaveAttribute("autocomplete", "current-password");
  });

  it("sends no empty password", async () => {
    const user = userEvent.setup();
    const { fetchMock } = renderLogin(
      () => new Response(null, { status: 204 }),
    );

    expect(submitButton()).toBeEnabled();
    await user.click(submitButton());

    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("refuses until the delay after a wrong password runs out", async () => {
    const user = countdownUser();
    const { onLoggedIn } = renderLogin(() =>
      errorResponse(401, "invalid_password", { "Retry-After": "4" }),
    );

    await attempt(user);

    expect(
      await screen.findByText("Wrong password. Next try in 4 s."),
    ).toBeInTheDocument();
    expect(passwordField()).toHaveValue("");
    expect(passwordField()).toHaveAttribute("aria-invalid", "true");
    expect(passwordField()).toHaveAccessibleDescription(
      "Wrong password. Next try in 4 s.",
    );
    expect(passwordField()).toHaveFocus();
    expect(submitButton()).toBeDisabled();

    runOut(4);
    expect(screen.getByText("Wrong password.")).toBeInTheDocument();
    expect(passwordField()).toHaveAttribute("aria-invalid", "true");
    expect(submitButton()).toBeEnabled();
    expect(onLoggedIn).not.toHaveBeenCalled();
  });

  it("says only that the password is wrong when no delay is given", async () => {
    const user = userEvent.setup();
    renderLogin(() => errorResponse(401, "invalid_password"));

    await attempt(user);

    expect(await screen.findByText("Wrong password.")).toBeInTheDocument();
    expect(passwordField()).toHaveValue("");
    expect(passwordField()).toHaveAccessibleDescription("Wrong password.");
    expect(passwordField()).toHaveFocus();
    expect(submitButton()).toBeEnabled();
  });

  it("refuses until the delay runs out while attempts are refused", async () => {
    const user = countdownUser();
    renderLogin(() =>
      errorResponse(429, "too_many_attempts", { "Retry-After": "8" }),
    );

    await attempt(user);

    expect(
      await screen.findByText("Too many attempts. Next try in 8 s."),
    ).toBeInTheDocument();
    expect(passwordField()).toHaveValue("");
    expect(passwordField()).toHaveFocus();
    expect(submitButton()).toBeDisabled();

    runOut(8);
    expect(screen.getByText("Too many attempts.")).toBeInTheDocument();
    expect(submitButton()).toBeEnabled();
  });

  it("asks for a session check when Officer answers in open access mode", async () => {
    const check = vi.fn();
    const unsubscribe = onCheckSession(check);
    try {
      const user = userEvent.setup();
      const { onLoggedIn } = renderLogin(() => errorResponse(409, "conflict"));

      await attempt(user);

      await waitFor(() => expect(check).toHaveBeenCalledTimes(1));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(passwordField()).toHaveValue("");
      expect(onLoggedIn).not.toHaveBeenCalled();
    } finally {
      unsubscribe();
    }
  });

  it("shows any other failure in a banner and clears the password", async () => {
    const user = userEvent.setup();
    renderLogin(() =>
      jsonResponse(
        { error: { code: "internal", message: "database is locked" } },
        500,
      ),
    );

    await attempt(user);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "database is locked",
    );
    expect(passwordField()).toHaveValue("");
    expect(passwordField()).not.toHaveAttribute("aria-invalid");
  });

  it("opens the recovery screen from the forgotten-password link", async () => {
    const user = userEvent.setup();
    const { fetchMock, onForgotPassword } = renderLogin(
      () => new Response(null, { status: 204 }),
    );

    await user.click(
      screen.getByRole("button", { name: "Forgot the password?" }),
    );

    expect(onForgotPassword).toHaveBeenCalledTimes(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("keeps the password out of storage, the URL and the console", async () => {
    const consoleSpies = (
      ["debug", "error", "info", "log", "warn"] as const
    ).map((method) => vi.spyOn(console, method));
    const user = userEvent.setup();
    renderLogin(() => errorResponse(401, "invalid_password"));

    await attempt(user);
    await screen.findByText("Wrong password.");

    // The environment may have no Web Storage; the cookie jar is checked too.
    const storages = [window.localStorage, window.sessionStorage].filter(
      (storage): storage is Storage => storage !== undefined,
    );
    for (const storage of storages) {
      for (let i = 0; i < storage.length; i++) {
        const key = storage.key(i) as string;
        expect(key).not.toContain(PASSWORD);
        expect(storage.getItem(key)).not.toContain(PASSWORD);
      }
    }
    expect(document.cookie).not.toContain(PASSWORD);
    expect(window.location.href).not.toContain(PASSWORD);
    for (const spy of consoleSpies) {
      for (const args of spy.mock.calls) {
        expect(args.map(String).join(" ")).not.toContain(PASSWORD);
      }
    }
  });
});
