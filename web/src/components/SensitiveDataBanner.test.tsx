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

import { act, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { TradingSnapshot } from "@/api/types";
import { SensitiveDataBanner } from "@/components/SensitiveDataBanner";
import i18n from "@/i18n";
import { renderWithApi } from "@/test/apiClient";

const fetchTrading = vi.fn<() => Promise<TradingSnapshot>>();
const title = "Sensitive data is stored unencrypted";
const snapshot: TradingSnapshot = {
  credentialsSealed: false,
  providers: [],
  connections: [
    {
      id: "alpaca-1",
      provider: "alpaca",
      label: "Alpaca paper",
      mode: "paper",
      enabled: false,
      venueIdentity: "venue-123",
      state: "stopped",
      secrets: { apiKey: true, apiSecret: true },
      feeAccount: "",
      instruments: [],
      access: [],
    },
  ],
};

function renderBanner() {
  return renderWithApi(
    <I18nextProvider i18n={i18n}>
      <SensitiveDataBanner />
    </I18nextProvider>,
    { api: { fetchTrading } },
  );
}

beforeEach(async () => {
  vi.resetAllMocks();
  await i18n.changeLanguage("en");
});

describe("SensitiveDataBanner", () => {
  it("keeps warning from the last loaded snapshot on background refresh failure", async () => {
    vi.useFakeTimers();
    fetchTrading.mockResolvedValue(snapshot);
    const { unmount } = renderBanner();
    try {
      await act(async () => {});
      expect(fetchTrading).toHaveBeenCalledTimes(1);
      expect(screen.getByText(title)).toBeInTheDocument();

      fetchTrading.mockRejectedValue(new Error("trading unavailable"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchTrading).toHaveBeenCalledTimes(2);
      expect(screen.getByText(title)).toBeInTheDocument();
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("warns about unsealed trading keys and links to master key setup", async () => {
    fetchTrading.mockResolvedValue(snapshot);
    const { container } = renderBanner();
    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(
      screen.getByText(
        "Officer runs without a master key: API keys of trading connections are kept in plain text in its database and in backups. Set a master key to store them sealed.",
      ),
    ).toBeInTheDocument();
    const link = screen.getByRole("link", { name: "How to set a master key" });
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/openpitkit/officer/blob/main/docs/configuration.md#master-key",
    );
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(
      container.querySelector('[role="alert"], [role="status"], [aria-live]'),
    ).toBeNull();
  });

  it.each([
    ["sealed", { ...snapshot, credentialsSealed: true }],
    ["no connections", { ...snapshot, connections: [] }],
  ])("hides when %s", async (_, data) => {
    fetchTrading.mockResolvedValue(data);
    await act(async () => {
      renderBanner();
    });
    expect(fetchTrading).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(title)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("hides while loading", () => {
    fetchTrading.mockReturnValue(new Promise(() => {}));
    const { container } = renderBanner();
    expect(container).toBeEmptyDOMElement();
  });

  it("hides when loading fails", async () => {
    fetchTrading.mockRejectedValue(new Error("trading unavailable"));
    await act(async () => {
      renderBanner();
    });
    expect(fetchTrading).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(title)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
