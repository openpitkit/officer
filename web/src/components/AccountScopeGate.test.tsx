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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AccountFeatures, AccountMode } from "@/api/accountMode";
import {
  AccountScopeGate,
  useStartupAccountMode,
} from "@/components/AccountScopeGate";
import { accountScope } from "@/lib/accountScope";
import { jsonResponse, renderWithApi } from "@/test/apiClient";

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

function StartupModeProbe() {
  return <p>{`startup mode:${useStartupAccountMode().mode}`}</p>;
}

function renderGate(fetchMock: ReturnType<typeof vi.fn>) {
  return renderWithApi(
    <AccountScopeGate>
      <ScopeProbe />
      <StartupModeProbe />
    </AccountScopeGate>,
    { fetch: fetchMock as unknown as typeof fetch },
  );
}

describe("AccountScopeGate", () => {
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

    it("keeps the first loaded mode for its children when later polls store another", async () => {
      const fetchMock = vi
        .fn()
        .mockImplementationOnce(async () => accountModeResponse(HIDDEN))
        .mockImplementation(async () => accountModeResponse(HIDDEN, "multi"));

      await renderLoaded(fetchMock);
      await act(() => vi.advanceTimersByTimeAsync(5000));

      expect(fetchMock).toHaveBeenCalledTimes(3);
      expect(screen.getByText("startup mode:single")).toBeInTheDocument();
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
