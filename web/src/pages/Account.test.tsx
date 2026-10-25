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

import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchCredentialStorage } from "@/api/credentialStorage";
import type { Account as AccountRecord } from "@/api/types";
import { SidebarProvider } from "@/components/SidebarContext";
import { ApiError } from "@/framework";
import i18n from "@/i18n";
import { setAccountScope } from "@/lib/accountScope";
import { Account } from "@/pages/Account";
import { accountRow, renderWithApi } from "@/test/apiClient";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

const fetchAccountsMock = vi.fn();
const setAccountCurrencyMock = vi.fn();
const blockAccountMock = vi.fn();
const unblockAccountMock = vi.fn();
vi.mock("@/api/credentialStorage");
const fetchCredentialStorageMock = vi.mocked(fetchCredentialStorage);

const NOT_OWN_BLOCK =
  "The block does not come from the account itself. It can be lifted in the several-accounts mode.";

/** The default account as a GET /accounts row, once normalized. */
function mainAccount(overrides: Partial<AccountRecord> = {}): AccountRecord {
  return accountRow("main", {
    title: "",
    currency: "USD",
    effectiveCurrency: "USD",
    currencyOrigin: "account",
    currencyCascade: { account: "USD", group: "", default: "" },
    pnl: "",
    positionCount: 0,
    venueAccess: false,
    ...overrides,
  });
}

const blockedMain = mainAccount({
  blocked: true,
  blockReason: "Risk halt",
  blockSource: "account",
  accountBlocked: true,
  accountBlockReason: "Risk halt",
});

function answering(account: AccountRecord) {
  fetchAccountsMock.mockResolvedValue([account]);
}

function renderAccount() {
  renderWithApi(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <MemoryRouter initialEntries={["/accounts"]}>
            <SidebarProvider>
              <Account />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        fetchAccounts: fetchAccountsMock,
        setAccountCurrency: setAccountCurrencyMock,
        blockAccount: blockAccountMock,
        unblockAccount: unblockAccountMock,
        fetchAssets: vi.fn().mockResolvedValue([]),
        // The page frame's banners poll these.
        fetchMarketData: vi.fn().mockResolvedValue({
          providers: [],
          instances: [],
          freshnessSeconds: 10,
          restartRequired: false,
        }),
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  fetchCredentialStorageMock.mockResolvedValue({
    credentialsSealed: true,
    plaintextTradingKeys: false,
    plaintextMarketDataKeys: false,
    sealedAt: null,
    resealedSecrets: 0,
  });
  await i18n.changeLanguage("en");
  setAccountScope({ accounts: false, account: "main" });
});

describe("Account page", () => {
  it("shows the default account's currency and block state", async () => {
    answering(mainAccount());
    renderAccount();

    expect(await screen.findByText("USD · account")).toBeInTheDocument();
    expect(screen.getByText("active")).toBeInTheDocument();
    expect(screen.queryByText("Reason")).not.toBeInTheDocument();
    expect(fetchAccountsMock).toHaveBeenCalledWith(
      { code: "main", codeMatch: "exact" },
      expect.any(AbortSignal),
    );
  });

  it("shows the block reason of an account blocked on itself", async () => {
    answering(blockedMain);
    renderAccount();

    expect(await screen.findByText("blocked")).toBeInTheDocument();
    expect(screen.getByText("Risk halt")).not.toHaveClass("truncate");
    expect(screen.getByRole("button", { name: "Unblock" })).toBeVisible();
    expect(screen.queryByText(NOT_OWN_BLOCK)).not.toBeInTheDocument();
  });

  it("offers Block and explains a block that does not come from the account", async () => {
    answering(
      mainAccount({
        blocked: true,
        blockReason: "Desk halt",
        blockSource: "group",
        groupBlocked: true,
        groupBlockReason: "Desk halt",
        group: "equity-desks",
      }),
    );
    renderAccount();

    expect(await screen.findByText("blocked")).toBeInTheDocument();
    expect(screen.getByText("Desk halt")).toBeInTheDocument();
    expect(screen.getByText(NOT_OWN_BLOCK)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Block" })).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "Unblock" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/equity-desks/)).not.toBeInTheDocument();
  });

  it("sets the currency of the default account through the shared dialog", async () => {
    const user = userEvent.setup();
    answering(mainAccount());
    setAccountCurrencyMock.mockResolvedValue(
      mainAccount({ currency: "EUR", effectiveCurrency: "EUR" }),
    );
    renderAccount();

    await user.click(
      await screen.findByRole("button", { name: "Edit account currency" }),
    );
    const dialog = screen.getByRole("dialog", {
      name: "Edit account currency",
    });
    const field = within(dialog).getByLabelText("Account currency");
    await user.clear(field);
    await user.type(field, "EUR");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    expect(setAccountCurrencyMock).toHaveBeenCalledWith("main", "EUR");
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Edit account currency" }),
      ).not.toBeInTheDocument(),
    );
    await waitFor(() => expect(fetchAccountsMock).toHaveBeenCalledTimes(2));
  });

  it("warns in the currency dialog that the account holds positions", async () => {
    const user = userEvent.setup();
    answering(mainAccount({ positionCount: 3 }));
    renderAccount();

    await user.click(
      await screen.findByRole("button", { name: "Edit account currency" }),
    );

    expect(
      within(
        screen.getByRole("dialog", { name: "Edit account currency" }),
      ).getByText(
        "This account holds non-zero positions or P&L. The backend will reject a change that would alter its effective currency.",
      ),
    ).toBeInTheDocument();
  });

  it("blocks the default account through the shared dialog", async () => {
    const user = userEvent.setup();
    answering(mainAccount());
    blockAccountMock.mockResolvedValue(blockedMain);
    renderAccount();

    await user.click(await screen.findByRole("button", { name: "Block" }));
    const dialog = screen.getByRole("dialog");
    await user.type(within(dialog).getByLabelText("Reason"), "Risk halt");
    await user.click(within(dialog).getByRole("button", { name: "Block" }));

    expect(blockAccountMock).toHaveBeenCalledWith(
      "main",
      "Risk halt",
      "reject",
    );
    await waitFor(() => expect(fetchAccountsMock).toHaveBeenCalledTimes(2));
  });

  it("unblocks the default account through the shared confirmation", async () => {
    const user = userEvent.setup();
    answering(blockedMain);
    unblockAccountMock.mockResolvedValue(mainAccount());
    renderAccount();

    await user.click(await screen.findByRole("button", { name: "Unblock" }));
    const confirm = screen.getByRole("alertdialog", {
      name: "Unblock account?",
    });
    await user.click(within(confirm).getByRole("button", { name: "Unblock" }));

    expect(unblockAccountMock).toHaveBeenCalledWith("main", "reject");
    await waitFor(() => expect(fetchAccountsMock).toHaveBeenCalledTimes(2));
  });

  it("links to the account mode choice", async () => {
    answering(mainAccount());
    renderAccount();

    expect(
      await screen.findByRole("link", { name: "Switch to several accounts" }),
    ).toHaveAttribute("href", "/service#account-mode");
  });

  it("says the account is not in the database and the next start follows the saved mode", async () => {
    fetchAccountsMock.mockResolvedValue([]);
    renderAccount();

    expect(
      await screen.findByText("The account is not in the database"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Its settings cannot be shown or changed now. The next start of Officer follows the saved account mode.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit account currency" }),
    ).not.toBeInTheDocument();
  });

  it("offers a retry when the first load fails", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockRejectedValueOnce(
      new ApiError("database is locked", "internal", 500),
    );
    answering(mainAccount());
    renderAccount();

    expect(await screen.findByText("Could not load data")).toBeInTheDocument();
    expect(screen.getByText("database is locked")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("USD · account")).toBeInTheDocument();
  });

  it("marks the settings stale when a background poll fails", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      answering(mainAccount());
      renderAccount();
      await screen.findByText("USD · account");

      fetchAccountsMock.mockRejectedValueOnce(
        new ApiError("database is locked", "internal", 500),
      );
      act(() => {
        vi.advanceTimersByTime(5000);
      });

      const stale = await screen.findByRole("status", { name: "Stale data" });
      expect(stale).toHaveTextContent("database is locked");
      expect(screen.getByText("USD · account")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});
