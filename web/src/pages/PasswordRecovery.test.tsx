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

import { formatTime } from "@/i18n/format";
import { onCheckSession } from "@/lib/panelSession";
import { PasswordRecovery } from "@/pages/PasswordRecovery";
import { errorResponse, jsonResponse, renderWithApi } from "@/test/apiClient";

// Letters, digits and hyphens only: a URL never percent-encodes them, so a
// secret that reached the URL would show there as written.
const CODE = "7KQ4-MZ2P-X9RD";
const PASSWORD = "a-new-panel-password";

function renderRecovery(answer: () => Response | Promise<Response>) {
  const fetchMock = vi.fn<
    (url: string, init: RequestInit) => Promise<Response>
  >(async () => answer());
  const onPasswordReset = vi.fn();
  const onBack = vi.fn();
  renderWithApi(
    <PasswordRecovery onPasswordReset={onPasswordReset} onBack={onBack} />,
    { fetch: fetchMock as unknown as typeof fetch },
  );
  return { fetchMock, onPasswordReset, onBack };
}

function requestButton() {
  return screen.getByRole("button", { name: "Print a code to the log" });
}

function codeField() {
  return screen.getByLabelText("Recovery code");
}

function passwordField() {
  return screen.getByLabelText("New password");
}

function repeatField() {
  return screen.getByLabelText("Repeat the new password");
}

async function fillReset(
  user: ReturnType<typeof userEvent.setup>,
  password: string,
  repeat = password,
  code = CODE,
) {
  if (code !== "") {
    await user.type(codeField(), code);
  }
  // Paste: typing a 257-character password key by key is slow and adds
  // nothing to what is checked.
  await user.click(passwordField());
  await user.paste(password);
  await user.click(repeatField());
  await user.paste(repeat);
  await user.click(
    screen.getByRole("button", { name: "Set the new password" }),
  );
}

// Subscribes to session checks for one case.
function watchSessionChecks() {
  const check = vi.fn();
  const unsubscribe = onCheckSession(check);
  return { check, unsubscribe };
}

describe("PasswordRecovery", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  describe("asking for a code", () => {
    it("asks POST /session/recovery and says the code is in the log", async () => {
      const user = userEvent.setup();
      const { fetchMock } = renderRecovery(() =>
        jsonResponse({ issued: true, expiresAt: "2026-10-16T12:15:00Z" }),
      );

      await user.click(requestButton());

      expect(
        await screen.findByText("Officer printed a code to its log."),
      ).toBeInTheDocument();
      const [url, init] = fetchMock.mock.calls[0];
      expect(url).toBe("/app/api/v1/session/recovery");
      expect(init.method).toBe("POST");
      expect(init.body).toBeUndefined();
    });

    it("says until when the code already in the log is valid", async () => {
      const user = userEvent.setup();
      const expiresAt = "2026-10-16T12:15:00Z";
      renderRecovery(() => jsonResponse({ issued: false, expiresAt }));

      await user.click(requestButton());

      expect(
        await screen.findByText(
          `A code is already in the log and is valid until ${formatTime(expiresAt)}.`,
        ),
      ).toBeInTheDocument();
      expect(
        screen.queryByText("Officer printed a code to its log."),
      ).not.toBeInTheDocument();
    });

    it("refuses new requests until the delay runs out", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderRecovery(() =>
        errorResponse(429, "too_many_attempts", { "Retry-After": "30" }),
      );

      await user.click(requestButton());

      const line = await screen.findByText(
        "Too many attempts. Next try in 30 s.",
      );
      // Read once through the focus; the counting text is no live region.
      expect(line).toHaveFocus();
      expect(line).not.toHaveAttribute("role");
      expect(line).not.toHaveAttribute("aria-live");
      expect(requestButton()).toBeDisabled();

      for (let i = 0; i < 30; i++) {
        act(() => {
          vi.advanceTimersByTime(1000);
        });
      }
      expect(screen.getByText("Too many attempts.")).toBeInTheDocument();
      expect(requestButton()).toBeEnabled();
    });

    it("asks for a session check when Officer answers in open access mode", async () => {
      const { check, unsubscribe } = watchSessionChecks();
      try {
        const user = userEvent.setup();
        renderRecovery(() => errorResponse(409, "conflict"));

        await user.click(requestButton());

        await waitFor(() => expect(check).toHaveBeenCalledTimes(1));
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      } finally {
        unsubscribe();
      }
    });

    it("shows any other failure in a banner", async () => {
      const user = userEvent.setup();
      renderRecovery(() =>
        jsonResponse({ error: { code: "internal", message: "no log" } }, 500),
      );

      await user.click(requestButton());

      expect(await screen.findByRole("alert")).toHaveTextContent("no log");
    });
  });

  it("shows where to read the code, with and without Docker", () => {
    renderRecovery(() => new Response(null, { status: 204 }));

    expect(
      screen.getByRole("textbox", { name: "Copyable snippet" }),
    ).toHaveValue("docker logs --tail 20 pit-officer");
    expect(
      screen.getByText(
        "Without Docker it is in the terminal where pit-officer serve runs.",
      ),
    ).toBeInTheDocument();
  });

  describe("setting the new password", () => {
    it("marks the fields for autofill as a one-time code and new passwords", () => {
      renderRecovery(() => new Response(null, { status: 204 }));

      expect(codeField()).toHaveAttribute("autocomplete", "one-time-code");
      expect(passwordField()).toHaveAttribute("autocomplete", "new-password");
      expect(repeatField()).toHaveAttribute("autocomplete", "new-password");
    });

    it.each([
      ["8 characters", "x".repeat(8)],
      ["256 characters", "x".repeat(256)],
      ["8 characters outside the basic plane", "\u{1F511}".repeat(8)],
    ])("sends a password of %s", async (_case, password) => {
      const user = userEvent.setup();
      const { fetchMock, onPasswordReset } = renderRecovery(
        () => new Response(null, { status: 204 }),
      );

      await fillReset(user, password);

      expect(onPasswordReset).toHaveBeenCalledTimes(1);
      const [url, init] = fetchMock.mock.calls[0];
      expect(url).toBe("/app/api/v1/session/recovery/reset");
      expect(init.method).toBe("POST");
      expect(JSON.parse(init.body as string)).toEqual({
        code: CODE,
        password,
      });
    });

    it.each([
      ["7 characters", "x".repeat(7)],
      ["257 characters", "x".repeat(257)],
      ["7 characters outside the basic plane", "\u{1F511}".repeat(7)],
    ])(
      "refuses a password of %s before sending it",
      async (_case, password) => {
        const user = userEvent.setup();
        const { fetchMock } = renderRecovery(
          () => new Response(null, { status: 204 }),
        );

        await fillReset(user, password);

        expect(passwordField()).toHaveAccessibleDescription(
          "The password needs 8 to 256 characters.",
        );
        expect(passwordField()).toHaveAttribute("aria-invalid", "true");
        expect(passwordField()).toHaveFocus();
        expect(passwordField()).toHaveValue(password);
        expect(fetchMock).not.toHaveBeenCalled();
      },
    );

    it("refuses passwords that differ until the repeat matches", async () => {
      const user = userEvent.setup();
      const { fetchMock } = renderRecovery(
        () => new Response(null, { status: 204 }),
      );

      await fillReset(user, PASSWORD, `${PASSWORD}!`);

      expect(repeatField()).toHaveAccessibleDescription(
        "The passwords do not match.",
      );
      expect(repeatField()).toHaveFocus();
      expect(fetchMock).not.toHaveBeenCalled();

      await user.type(repeatField(), "{Backspace}");
      expect(repeatField()).not.toHaveAttribute("aria-invalid");
    });

    it("refuses an empty code before sending, until one is typed", async () => {
      const user = userEvent.setup();
      const { fetchMock } = renderRecovery(
        () => new Response(null, { status: 204 }),
      );

      await fillReset(user, PASSWORD, PASSWORD, "");

      expect(codeField()).toHaveAccessibleDescription(
        "Enter the code from the log.",
      );
      expect(codeField()).toHaveAttribute("aria-invalid", "true");
      expect(codeField()).toHaveFocus();
      expect(fetchMock).not.toHaveBeenCalled();

      await user.type(codeField(), CODE);
      expect(codeField()).not.toHaveAttribute("aria-invalid");
    });

    it("marks the code wrong and clears the passwords", async () => {
      const user = userEvent.setup();
      const { onPasswordReset } = renderRecovery(() =>
        errorResponse(401, "invalid_recovery_code"),
      );

      await fillReset(user, PASSWORD);

      expect(
        await screen.findByText("The code is wrong or has expired."),
      ).toBeInTheDocument();
      expect(codeField()).toHaveAttribute("aria-invalid", "true");
      expect(codeField()).toHaveAccessibleDescription(
        "The code is wrong or has expired.",
      );
      expect(codeField()).toHaveFocus();
      expect(codeField()).toHaveValue(CODE);
      expect(passwordField()).toHaveValue("");
      expect(repeatField()).toHaveValue("");
      expect(onPasswordReset).not.toHaveBeenCalled();
    });

    it("shows the server's reason under the password it rejects", async () => {
      const user = userEvent.setup();
      renderRecovery(
        () =>
          new Response(
            JSON.stringify({
              type: "about:blank",
              title: "Unprocessable Entity",
              status: 422,
              detail: "password must have 8 to 256 characters",
              errors: [{ pointer: "/password", detail: "too short" }],
            }),
            {
              status: 422,
              headers: { "Content-Type": "application/problem+json" },
            },
          ),
      );

      await fillReset(user, PASSWORD);

      expect(
        await screen.findByText("password must have 8 to 256 characters"),
      ).toBeInTheDocument();
      expect(passwordField()).toHaveAccessibleDescription(
        "password must have 8 to 256 characters",
      );
      expect(passwordField()).toHaveFocus();
      expect(passwordField()).toHaveValue("");
      expect(repeatField()).toHaveValue("");
    });

    it("asks for a session check when Officer answers in open access mode", async () => {
      const { check, unsubscribe } = watchSessionChecks();
      try {
        const user = userEvent.setup();
        const { onPasswordReset } = renderRecovery(() =>
          errorResponse(409, "conflict"),
        );

        await fillReset(user, PASSWORD);

        await waitFor(() => expect(check).toHaveBeenCalledTimes(1));
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
        expect(passwordField()).toHaveValue("");
        expect(onPasswordReset).not.toHaveBeenCalled();
      } finally {
        unsubscribe();
      }
    });

    it("shows any other failure in a banner and clears the passwords", async () => {
      const user = userEvent.setup();
      renderRecovery(() =>
        jsonResponse(
          { error: { code: "internal", message: "database is locked" } },
          500,
        ),
      );

      await fillReset(user, PASSWORD);

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "database is locked",
      );
      expect(passwordField()).toHaveValue("");
      expect(repeatField()).toHaveValue("");
      expect(codeField()).not.toHaveAttribute("aria-invalid");
    });

    it("keeps the code and the password out of storage, the URL and the console", async () => {
      const consoleSpies = (
        ["debug", "error", "info", "log", "warn"] as const
      ).map((method) => vi.spyOn(console, method));
      const user = userEvent.setup();
      renderRecovery(() => errorResponse(401, "invalid_recovery_code"));

      await fillReset(user, PASSWORD);
      await screen.findByText("The code is wrong or has expired.");

      // The environment may have no Web Storage; the cookie jar is checked
      // too.
      const storages = [window.localStorage, window.sessionStorage].filter(
        (storage): storage is Storage => storage !== undefined,
      );
      for (const secret of [CODE, PASSWORD]) {
        for (const storage of storages) {
          for (let i = 0; i < storage.length; i++) {
            const key = storage.key(i) as string;
            expect(key).not.toContain(secret);
            expect(storage.getItem(key)).not.toContain(secret);
          }
        }
        expect(document.cookie).not.toContain(secret);
        expect(window.location.href).not.toContain(secret);
        for (const spy of consoleSpies) {
          for (const args of spy.mock.calls) {
            expect(args.map(String).join(" ")).not.toContain(secret);
          }
        }
      }
    });
  });

  it("returns to the login screen", async () => {
    const user = userEvent.setup();
    const { fetchMock, onBack } = renderRecovery(
      () => new Response(null, { status: 204 }),
    );

    await user.click(screen.getByRole("button", { name: "Back" }));

    expect(onBack).toHaveBeenCalledTimes(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
