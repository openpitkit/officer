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
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { MarketDataStatus } from "@/api/types";
import { PendingRestartBanner } from "@/components/PendingRestartBanner";
import i18n from "@/i18n";
import { renderWithApi } from "@/test/apiClient";

const fetchMarketData = vi.fn<() => Promise<MarketDataStatus>>();
const restartMarketData = vi.fn<() => Promise<MarketDataStatus>>();
const title = "Restart needed to apply market-data changes";
const detail =
  "Saved source or instrument settings differ from the running feeds. Restart feeds to apply them.";

function status(restartRequired: boolean): MarketDataStatus {
  return { providers: [], instances: [], freshnessSeconds: 5, restartRequired };
}

function renderBanner() {
  return renderWithApi(
    <I18nextProvider i18n={i18n}>
      <PendingRestartBanner />
    </I18nextProvider>,
    { api: { fetchMarketData, restartMarketData } },
  );
}

beforeEach(async () => {
  vi.resetAllMocks();
  await i18n.changeLanguage("en");
});

describe("PendingRestartBanner", () => {
  it.each([
    [
      "the load is pending",
      () => fetchMarketData.mockReturnValue(new Promise(() => {})),
    ],
    [
      "the load fails",
      () =>
        fetchMarketData.mockRejectedValue(new Error("market data unavailable")),
    ],
    [
      "no restart is required",
      () => fetchMarketData.mockResolvedValue(status(false)),
    ],
  ])("hides when %s", async (_, arrange) => {
    arrange();
    const { container } = renderBanner();
    await act(async () => {});
    expect(fetchMarketData).toHaveBeenCalledTimes(1);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the title, the detail and the restart button when a restart is required", async () => {
    fetchMarketData.mockResolvedValue(status(true));
    renderBanner();
    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(screen.getByText(detail)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restart feeds" })).toBeEnabled();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("marks the notice stale when a refresh fails on loaded data", async () => {
    vi.useFakeTimers();
    fetchMarketData.mockResolvedValue(status(true));
    const { unmount } = renderBanner();
    try {
      await act(async () => {});
      expect(screen.getByText(title)).toBeInTheDocument();
      expect(screen.queryByRole("status")).not.toBeInTheDocument();

      fetchMarketData.mockRejectedValue(new Error("market data unavailable"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchMarketData).toHaveBeenCalledTimes(2);
      expect(screen.getByText(title)).toBeInTheDocument();
      expect(
        screen.getByRole("status", {
          name: "Stale data: market data unavailable",
        }),
      ).toBeInTheDocument();
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("restarts the feeds, then reloads, with the button disabled while busy", async () => {
    const user = userEvent.setup();
    fetchMarketData.mockResolvedValue(status(true));
    let finishRestart: (value: MarketDataStatus) => void = () => {};
    restartMarketData.mockReturnValue(
      new Promise((resolve) => {
        finishRestart = resolve;
      }),
    );
    renderBanner();
    const button = await screen.findByRole("button", { name: "Restart feeds" });

    await user.click(button);
    expect(restartMarketData).toHaveBeenCalledOnce();
    expect(button).toBeDisabled();
    expect(fetchMarketData).toHaveBeenCalledTimes(1);

    await act(async () => {
      finishRestart(status(true));
    });
    await waitFor(() => expect(fetchMarketData).toHaveBeenCalledTimes(2));
    expect(button).toBeEnabled();
  });
});
