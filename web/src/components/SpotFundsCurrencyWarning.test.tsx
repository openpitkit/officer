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

import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SpotFundsLimitModeState } from "@/api/spotFundsLimitMode";
import type { SpotFundsLimitMode } from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { SpotFundsCurrencyWarning } from "@/components/SpotFundsCurrencyWarning";
import i18n from "@/i18n";
import { accountScope, setAccountScope } from "@/lib/accountScope";
import { renderWithApi } from "@/test/apiClient";

let previousScope: ReturnType<typeof accountScope>;

beforeEach(async () => {
  previousScope = accountScope();
  setAccountScope({ accounts: true, account: "" });
  await i18n.changeLanguage("en");
});

afterEach(() => {
  setAccountScope({
    accounts: previousScope.showsAccounts,
    account: previousScope.account,
  });
});

function renderWarning(
  mode: SpotFundsLimitMode,
  accountsWithoutCurrency: string[],
  loadError: string | null = null,
) {
  const reload = vi.fn();
  const poll: PollingResult<SpotFundsLimitModeState> = {
    load: {
      state: "ready",
      data: { mode, stored: true, accountsWithoutCurrency },
      error: loadError,
    },
    reload,
  };
  renderWithApi(
    <MemoryRouter>
      <SpotFundsCurrencyWarning poll={poll} />
    </MemoryRouter>,
  );
  return { reload };
}

const warningTitle = "P&L bounds block accounts without a currency";

describe("SpotFundsCurrencyWarning", () => {
  it("hides the warning in enforce even with affected accounts", () => {
    renderWarning("enforce", ["desk-a"]);

    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "desk-a" }),
    ).not.toBeInTheDocument();
  });

  it("hides the warning in track_only with no affected accounts", () => {
    renderWarning("track_only", []);

    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
  });

  it("links every affected account by exact code while accounts are shown", () => {
    renderWarning("track_only", ["desk-a", "desk &/b"]);

    expect(screen.getByText(warningTitle)).toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(2);
    expect(screen.getByRole("link", { name: "desk-a" })).toHaveAttribute(
      "href",
      "/accounts?code=desk-a&codeMatch=exact",
    );
    expect(screen.getByRole("link", { name: "desk &/b" })).toHaveAttribute(
      "href",
      "/accounts?code=desk%20%26%2Fb&codeMatch=exact",
    );
  });

  it("links to the account page while accounts are hidden", () => {
    setAccountScope({ accounts: false, account: "main" });
    renderWarning("track_only", ["main"]);

    expect(screen.getByText(warningTitle)).toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "main" })).toHaveAttribute(
      "href",
      "/accounts",
    );
  });

  it("renders nothing while loading", () => {
    const { container } = renderWithApi(
      <SpotFundsCurrencyWarning
        poll={{
          load: { state: "loading", data: null, error: null },
          reload: vi.fn(),
        }}
      />,
    );

    expect(container).toBeEmptyDOMElement();
  });

  it("shows a first-load error and retries with the supplied reload", async () => {
    const user = userEvent.setup();
    const reload = vi.fn();
    renderWithApi(
      <SpotFundsCurrencyWarning
        poll={{
          load: { state: "error", data: null, error: "mode unavailable" },
          reload,
        }}
      />,
    );

    expect(
      screen.getByText("Could not load Spot Funds limit mode"),
    ).toBeInTheDocument();
    expect(screen.getByText("mode unavailable")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("keeps the warning under the stale state and retries a failed background poll", async () => {
    const user = userEvent.setup();
    const { reload } = renderWarning(
      "track_only",
      ["desk-a"],
      "poll unavailable",
    );

    expect(screen.getByText("poll unavailable")).toBeInTheDocument();
    expect(screen.getByText(warningTitle)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });
});
