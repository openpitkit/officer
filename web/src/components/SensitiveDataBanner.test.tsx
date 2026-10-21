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
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { CredentialStorage } from "@/api/types";
import { SensitiveDataBanner } from "@/components/SensitiveDataBanner";
import i18n from "@/i18n";
import { renderWithApi } from "@/test/apiClient";

const fetchCredentialStorage = vi.fn<() => Promise<CredentialStorage>>();
const title = "Sensitive data is stored unencrypted";
const unsealed: CredentialStorage = {
  credentialsSealed: false,
  plaintextTradingKeys: true,
  plaintextMarketDataKeys: false,
  sealedAt: null,
  resealedSecrets: 0,
};

function renderBanner() {
  return renderWithApi(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <SensitiveDataBanner />
      </MemoryRouter>
    </I18nextProvider>,
    { api: { fetchCredentialStorage } },
  );
}

beforeEach(async () => {
  vi.resetAllMocks();
  await i18n.changeLanguage("en");
});

describe("SensitiveDataBanner", () => {
  it("keeps warning from the last loaded storage on background refresh failure", async () => {
    vi.useFakeTimers();
    fetchCredentialStorage.mockResolvedValue(unsealed);
    const { unmount } = renderBanner();
    try {
      await act(async () => {});
      expect(fetchCredentialStorage).toHaveBeenCalledTimes(1);
      expect(screen.getByText(title)).toBeInTheDocument();

      fetchCredentialStorage.mockRejectedValue(
        new Error("credential storage unavailable"),
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchCredentialStorage).toHaveBeenCalledTimes(2);
      expect(screen.getByText(title)).toBeInTheDocument();
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it.each([
    ["trading keys", { plaintextTradingKeys: true }],
    ["market-data keys", { plaintextMarketDataKeys: true }],
    [
      "both kinds of keys",
      { plaintextTradingKeys: true, plaintextMarketDataKeys: true },
    ],
  ])("warns about plain-text %s", async (_, flags) => {
    fetchCredentialStorage.mockResolvedValue({
      credentialsSealed: false,
      plaintextTradingKeys: false,
      plaintextMarketDataKeys: false,
      sealedAt: null,
      resealedSecrets: 0,
      ...flags,
    });
    const { container } = renderBanner();
    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(
      screen.getByText(
        "Officer runs without a master key: stored API keys and tokens are kept in plain text in its database and in backups. Set a master key to store them sealed.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "How to set a master key" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(
      container.querySelector('[role="alert"], [role="status"], [aria-live]'),
    ).toBeNull();
  });

  it.each([
    [
      "sealed",
      {
        credentialsSealed: true,
        plaintextTradingKeys: false,
        plaintextMarketDataKeys: false,
        sealedAt: null,
        resealedSecrets: 0,
      },
    ],
    [
      "no keys are stored",
      {
        credentialsSealed: false,
        plaintextTradingKeys: false,
        plaintextMarketDataKeys: false,
        sealedAt: null,
        resealedSecrets: 0,
      },
    ],
  ])("hides when %s", async (_, data) => {
    fetchCredentialStorage.mockResolvedValue(data);
    await act(async () => {
      renderBanner();
    });
    expect(fetchCredentialStorage).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(title)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("hides while loading", () => {
    fetchCredentialStorage.mockReturnValue(new Promise(() => {}));
    const { container } = renderBanner();
    expect(container).toBeEmptyDOMElement();
  });

  it("hides when loading fails", async () => {
    fetchCredentialStorage.mockRejectedValue(
      new Error("credential storage unavailable"),
    );
    await act(async () => {
      renderBanner();
    });
    expect(fetchCredentialStorage).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(title)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
