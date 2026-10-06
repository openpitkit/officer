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
// Please see https://openpit.dev and the OWNERS file for details.

import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import i18n from "@/i18n";
import type { TradingSnapshot } from "@/api/types";
import {
  BackupCard,
  DatabaseCard,
  ServiceLifecycleCard,
} from "@/pages/Service";

const exportBackupMock = vi.fn();
const resetDatabaseMock = vi.fn();
const restoreBackupMock = vi.fn();
const fetchTradingMock = vi.fn<() => Promise<TradingSnapshot>>();
const tradingSnapshot: TradingSnapshot = {
  providers: [],
  credentialsSealed: false,
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

const exportWarning =
  "This backup will contain sensitive data unencrypted: API keys of trading connections are stored without a master key and go into the archive in plain text. Keep the file private, or set a master key first.";

function renderBackupCard() {
  return render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <BackupCard />
      </MemoryRouter>
    </I18nextProvider>,
    {
      api: {
        exportBackup: exportBackupMock,
        restoreBackup: restoreBackupMock,
        fetchTrading: fetchTradingMock,
      },
    },
  );
}

function renderDatabaseCard(onReset = vi.fn()) {
  render(
    <I18nextProvider i18n={i18n}>
      <DatabaseCard
        database={{ path: "/tmp/officer.db", reachable: true }}
        onReset={onReset}
      />
    </I18nextProvider>,
    {
      api: {
        resetDatabase: resetDatabaseMock,
      },
    },
  );
  return onReset;
}

function renderServiceLifecycleCard(
  onAction = vi.fn<() => Promise<void>>().mockResolvedValue(undefined),
) {
  render(
    <I18nextProvider i18n={i18n}>
      <ServiceLifecycleCard
        busyAction={null}
        error={null}
        onAction={onAction}
      />
    </I18nextProvider>,
  );
  return onAction;
}

function backupFile(name: string, body: string, type = "application/json") {
  const file = new File([body], name, { type });
  const bytes = new TextEncoder().encode(body);
  Object.defineProperty(file, "arrayBuffer", {
    value: () => Promise.resolve(bytes.buffer),
  });
  return file;
}

beforeEach(async () => {
  vi.clearAllMocks();
  vi.restoreAllMocks();
  fetchTradingMock.mockResolvedValue({ ...tradingSnapshot, connections: [] });
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: vi.fn(() => "blob:backup"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    value: vi.fn(),
  });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  await i18n.changeLanguage("en");
});

describe("BackupCard", () => {
  it("fetches trading once per poll and keeps the last snapshot on refresh failure", async () => {
    vi.useFakeTimers();
    fetchTradingMock.mockResolvedValue(tradingSnapshot);
    const { unmount } = renderBackupCard();
    try {
      await act(async () => {});
      expect(fetchTradingMock).toHaveBeenCalledTimes(1);
      expect(screen.getByText(exportWarning)).toBeInTheDocument();
      const allAccounts = screen.getByRole("checkbox", {
        name: "All accounts and groups",
      });
      expect(allAccounts).toBeDisabled();

      fetchTradingMock.mockRejectedValue(new Error("trading unavailable"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchTradingMock).toHaveBeenCalledTimes(2);
      expect(screen.getByText(exportWarning)).toBeInTheDocument();
      expect(allAccounts).toBeDisabled();

      fetchTradingMock.mockResolvedValue({
        ...tradingSnapshot,
        connections: [],
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchTradingMock).toHaveBeenCalledTimes(3);
      expect(screen.queryByText(exportWarning)).not.toBeInTheDocument();
      expect(allAccounts).toBeEnabled();
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("warns about unencrypted trading keys without blocking export", async () => {
    fetchTradingMock.mockResolvedValue(tradingSnapshot);
    exportBackupMock.mockResolvedValue({
      blob: new Blob(["backup"]),
      filename: "backup.zip",
    });
    const user = userEvent.setup();
    renderBackupCard();

    expect(await screen.findByText(exportWarning)).toBeInTheDocument();
    const link = screen.getByRole("link", { name: "How to set a master key" });
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/openpitkit/officer/blob/main/docs/configuration.md#master-key",
    );
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    const button = screen.getByRole("button", { name: "Export" });
    expect(button).toBeEnabled();
    await user.click(button);
    await waitFor(() => expect(exportBackupMock).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole("tab", { name: "Restore" }));
    expect(screen.queryByText(exportWarning)).not.toBeInTheDocument();
  });

  it.each([
    ["sealed", { ...tradingSnapshot, credentialsSealed: true }],
    ["no connections", { ...tradingSnapshot, connections: [] }],
  ])("does not warn when %s", async (_, snapshot) => {
    fetchTradingMock.mockResolvedValue(snapshot);
    await act(async () => {
      renderBackupCard();
    });
    expect(fetchTradingMock).toHaveBeenCalled();
    expect(screen.queryByText(exportWarning)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("does not warn while trading is loading", () => {
    fetchTradingMock.mockReturnValue(new Promise(() => {}));
    renderBackupCard();
    expect(screen.queryByText(exportWarning)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("does not warn when trading fails to load", async () => {
    fetchTradingMock.mockRejectedValue(new Error("trading unavailable"));
    await act(async () => {
      renderBackupCard();
    });
    expect(fetchTradingMock).toHaveBeenCalled();
    expect(screen.queryByText(exportWarning)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("warns only when the effective export scope includes trading", async () => {
    fetchTradingMock.mockResolvedValue(tradingSnapshot);
    const user = userEvent.setup();
    renderBackupCard();
    expect(await screen.findByText(exportWarning)).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    await user.click(
      screen.getByRole("checkbox", { name: "Trading connections" }),
    );
    expect(screen.queryByText(exportWarning)).not.toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    expect(screen.getByText(exportWarning)).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    expect(screen.queryByText(exportWarning)).not.toBeInTheDocument();
    await user.click(
      screen.getByRole("checkbox", { name: "Orders, trades, adjustments" }),
    );
    expect(screen.getByText(exportWarning)).toBeInTheDocument();
  });

  it.each(["Export", "Restore"])(
    "keeps section selection closed under the server pulls in %s",
    async (workflow) => {
      const user = userEvent.setup();
      renderBackupCard();
      await user.click(screen.getByRole("tab", { name: workflow }));
      await user.click(screen.getByRole("checkbox", { name: "All data" }));
      const accounts = screen.getByRole("checkbox", {
        name: "Accounts and groups",
      });
      const history = screen.getByRole("checkbox", {
        name: "Orders, trades, adjustments",
      });
      const trading = screen.getByRole("checkbox", {
        name: "Trading connections",
      });
      expect(
        screen.getByText(
          "Trading connections include accounts and groups, and orders and trades; orders and trades include trading connections.",
        ),
      ).toBeInTheDocument();

      await user.click(accounts);
      [accounts, history, trading].forEach((item) =>
        expect(item).not.toBeChecked(),
      );
      await user.click(accounts);
      expect(accounts).toBeChecked();
      expect(history).not.toBeChecked();
      expect(trading).not.toBeChecked();
      await user.click(accounts);
      await user.click(trading);
      [accounts, history, trading].forEach((item) =>
        expect(item).toBeChecked(),
      );
      await user.click(history);
      expect(accounts).toBeChecked();
      expect(history).not.toBeChecked();
      expect(trading).not.toBeChecked();
      await user.click(history);
      [accounts, history, trading].forEach((item) =>
        expect(item).toBeChecked(),
      );
      await user.click(trading);
      expect(accounts).toBeChecked();
      expect(history).not.toBeChecked();
      expect(trading).not.toBeChecked();
      await user.click(history);
      await user.click(accounts);
      [accounts, history, trading].forEach((item) =>
        expect(item).not.toBeChecked(),
      );
      expect(screen.getByRole("checkbox", { name: "Audit log" })).toBeChecked();
    },
  );

  it("exports one account without connections when only accounts are selected", async () => {
    const user = userEvent.setup();
    exportBackupMock.mockResolvedValue({
      blob: new Blob(["backup"]),
      filename: "backup.zip",
    });
    await act(async () => {
      renderBackupCard();
    });
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    await user.click(
      screen.getByRole("checkbox", { name: "Trading connections" }),
    );
    for (const name of [
      "Positions",
      "Risk limits",
      "Market-data settings",
      "General settings",
      "User settings",
      "Audit log",
    ]) {
      await user.click(screen.getByRole("checkbox", { name }));
    }
    await user.click(
      screen.getByRole("checkbox", { name: "All accounts and groups" }),
    );
    await user.type(
      screen.getByPlaceholderText("accounts, comma-separated"),
      "desk-a",
    );
    await user.click(screen.getByRole("button", { name: "Export" }));
    await waitFor(() => expect(exportBackupMock).toHaveBeenCalledTimes(1));
    expect(exportBackupMock).toHaveBeenCalledWith(
      {
        all: false,
        sections: ["accounts_groups"],
        accounts: { all: false, accounts: ["desk-a"] },
      },
      undefined,
      true,
    );
  });

  it("allows narrowed history exports without trading connections", async () => {
    const user = userEvent.setup();
    exportBackupMock.mockResolvedValue({
      blob: new Blob(["backup"]),
      filename: "backup.zip",
    });
    await act(async () => {
      renderBackupCard();
    });
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    const allAccounts = screen.getByRole("checkbox", {
      name: "All accounts and groups",
    });
    expect(allAccounts).toBeEnabled();
    await user.click(allAccounts);
    await user.type(
      screen.getByPlaceholderText("accounts, comma-separated"),
      "desk-a",
    );
    await user.click(screen.getByRole("button", { name: "Export" }));
    await waitFor(() => expect(exportBackupMock).toHaveBeenCalledTimes(1));
    expect(exportBackupMock.mock.calls[0][0]).toMatchObject({
      accounts: { all: false, accounts: ["desk-a"] },
      sections: expect.arrayContaining([
        "accounts_groups",
        "activity_history",
        "trading",
      ]),
    });
  });

  it("exports all accounts with trading connections and restores narrowing when deselected", async () => {
    fetchTradingMock.mockResolvedValue({
      ...tradingSnapshot,
      credentialsSealed: true,
    });
    exportBackupMock.mockResolvedValue({
      blob: new Blob(["backup"]),
      filename: "backup.zip",
    });
    const user = userEvent.setup();
    renderBackupCard();
    const allAccounts = screen.getByRole("checkbox", {
      name: "All accounts and groups",
    });
    await waitFor(() => expect(allAccounts).toBeDisabled());
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    const trading = screen.getByRole("checkbox", {
      name: "Trading connections",
    });
    const accountsInput = screen.getByPlaceholderText(
      "accounts, comma-separated",
    );
    const groupsInput = screen.getByPlaceholderText("groups, comma-separated");
    await user.click(trading);
    expect(allAccounts).toBeEnabled();
    await user.click(allAccounts);
    await user.type(accountsInput, "desk-a");
    await user.type(groupsInput, "equity-desks");
    await user.click(trading);
    expect(allAccounts).toBeChecked();
    expect(allAccounts).toBeDisabled();
    expect(accountsInput).toBeDisabled();
    expect(groupsInput).toBeDisabled();
    expect(accountsInput).toHaveValue("");
    expect(groupsInput).toHaveValue("");
    expect(
      screen.getByText(
        "Trading data is exported and restored for all accounts.",
      ),
    ).toBeInTheDocument();
    await user.click(trading);
    expect(allAccounts).not.toBeChecked();
    expect(accountsInput).toHaveValue("desk-a");
    expect(groupsInput).toHaveValue("equity-desks");
    await user.click(trading);
    await user.click(screen.getByRole("button", { name: "Export" }));
    await waitFor(() => expect(exportBackupMock).toHaveBeenCalledTimes(1));
    expect(exportBackupMock.mock.calls[0][0]).toEqual({
      all: false,
      accounts: { all: true },
      positions: { all: true },
      sections: [
        "accounts_groups",
        "positions",
        "risk_limits",
        "market_data_settings",
        "general_settings",
        "user_settings",
        "activity_history",
        "trading",
        "audit_log",
      ],
    });
  });

  it("sends narrowed restore scope with trading and displays the server's 422 message", async () => {
    fetchTradingMock.mockResolvedValue(tradingSnapshot);
    const message =
      "Trading data cannot be restored for selected accounts: the archive contains trading rows.";
    restoreBackupMock.mockRejectedValue(
      Object.assign(new Error(message), { status: 422 }),
    );
    const user = userEvent.setup();
    renderBackupCard();
    await screen.findByText(exportWarning);
    await user.click(screen.getByRole("tab", { name: "Restore" }));
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    const allAccounts = screen.getByRole("checkbox", {
      name: "All accounts and groups",
    });
    expect(allAccounts).toBeEnabled();
    expect(
      screen.queryByText(
        "Trading data is exported and restored for all accounts.",
      ),
    ).not.toBeInTheDocument();
    await user.click(allAccounts);
    await user.type(
      screen.getByPlaceholderText("accounts, comma-separated"),
      "desk-a",
    );
    await user.click(
      screen.getByRole("radio", { name: "Restore and overwrite matches" }),
    );
    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    if (!input) throw new Error("backup file input not found");
    await user.upload(input, backupFile("backup.json", "{}"));
    await user.click(screen.getByRole("button", { name: "Restore" }));
    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(restoreBackupMock).toHaveBeenCalledWith({
      archiveFile: { base64: "e30=", filename: "backup.json" },
      mode: "overwrite",
      scope: {
        all: false,
        accounts: { all: false, accounts: ["desk-a"] },
        positions: { all: true },
        sections: [
          "accounts_groups",
          "positions",
          "risk_limits",
          "market_data_settings",
          "general_settings",
          "user_settings",
          "activity_history",
          "trading",
          "audit_log",
        ],
      },
    });
  });

  it("lists unavailable trading credentials and links to connections", async () => {
    const user = userEvent.setup();
    restoreBackupMock.mockResolvedValue({
      applied: { trading: 2 },
      skipped: {},
      restartRequired: false,
      tradingCredentialsUnavailable: ["Alpaca paper", "Second connection"],
    });
    renderBackupCard();
    await user.click(screen.getByRole("tab", { name: "Restore" }));
    await user.click(
      screen.getByRole("radio", { name: "Restore and overwrite matches" }),
    );
    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    if (!input) throw new Error("backup file input not found");
    await user.upload(input, backupFile("backup.json", "{}"));
    await user.click(screen.getByRole("button", { name: "Restore" }));
    const link = await screen.findByRole("link", {
      name: "Trading connections",
    });
    expect(link).toHaveAttribute("href", "/trading-connections");
    expect(link.closest("p")).toHaveTextContent(
      "API keys could not be restored for Alpaca paper, Second connection. Those trading connections were restored disabled: enter their keys again in Trading connections.",
    );
  });

  it.each([
    ["en", "Trading connections"],
    ["ru", "Торговые подключения"],
    ["zh-CN", "交易连接"],
  ])(
    "renders restored connection labels as plain text in %s",
    async (locale, linkName) => {
      const label = "<tradingLink>x</tradingLink><b>y</b>";
      restoreBackupMock.mockResolvedValue({
        applied: { trading: 1 },
        skipped: {},
        restartRequired: false,
        tradingCredentialsUnavailable: [label],
      });
      const user = userEvent.setup();
      renderBackupCard();
      await user.click(screen.getByRole("tab", { name: "Restore" }));
      await user.click(
        screen.getByRole("radio", { name: "Restore and overwrite matches" }),
      );
      const input =
        document.querySelector<HTMLInputElement>('input[type="file"]');
      if (!input) throw new Error("backup file input not found");
      await user.upload(input, backupFile("backup.json", "{}"));
      await user.click(screen.getByRole("button", { name: "Restore" }));
      await screen.findByText(/Applied 1, skipped 0/);
      await act(async () => {
        await i18n.changeLanguage(locale);
      });
      expect(screen.getByText(label)).toBeInTheDocument();
      const links = screen.getAllByRole("link");
      expect(links).toHaveLength(1);
      expect(links[0]).toHaveAccessibleName(linkName);
      expect(links[0]).toHaveAttribute("href", "/trading-connections");
      expect(links[0].closest("p")).toHaveTextContent(label);
      expect(links[0].closest("p")?.querySelector("b")).toBeNull();
    },
  );

  it("links backup workflow tabs to panels and supports arrow navigation", async () => {
    const user = userEvent.setup();
    renderBackupCard();

    const exportTab = screen.getByRole("tab", { name: /^export$/i });
    const restoreTab = screen.getByRole("tab", { name: /^restore$/i });
    expect(exportTab).toHaveAttribute(
      "aria-controls",
      "backup-workflow-panel-export",
    );
    expect(restoreTab).toHaveAttribute(
      "aria-controls",
      "backup-workflow-panel-restore",
    );
    expect(
      screen.getByRole("tabpanel", { name: /^export$/i }),
    ).toBeInTheDocument();

    exportTab.focus();
    await user.keyboard("{ArrowRight}");

    await waitFor(() => {
      expect(restoreTab).toHaveAttribute("aria-selected", "true");
    });
    expect(
      screen.getByRole("tabpanel", { name: /^restore$/i }),
    ).toBeInTheDocument();

    await user.keyboard("{Home}");

    await waitFor(() => {
      expect(exportTab).toHaveAttribute("aria-selected", "true");
    });
  });

  it("keeps restore disabled until a mode and file are selected", async () => {
    const user = userEvent.setup();
    renderBackupCard();

    await user.click(screen.getByRole("tab", { name: /^restore$/i }));
    expect(
      screen.getByText("Only selected sections are affected."),
    ).toBeInTheDocument();
    const button = screen.getByRole("button", { name: /^restore$/i });
    expect(button).toBeDisabled();

    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    if (input == null) {
      throw new Error("backup file input not found");
    }
    await user.upload(input, new File(["{}"], "backup.json"));
    expect(button).toBeDisabled();

    await user.click(screen.getByRole("radio", { name: /overwrite/i }));
    expect(button).toBeEnabled();
  });

  it("exports zipped by default and resets backup controls after success", async () => {
    const user = userEvent.setup();
    exportBackupMock.mockResolvedValue({
      blob: new Blob(["zip-bytes"], { type: "application/zip" }),
      filename: "backup.zip",
    });
    renderBackupCard();

    const allData = screen.getByRole("checkbox", { name: /all data/i });
    const zip = screen.getByRole("checkbox", { name: /archive as zip/i });
    expect(allData).toBeChecked();
    expect(zip).toBeChecked();

    await user.click(allData);
    await user.click(zip);
    expect(allData).not.toBeChecked();
    expect(zip).not.toBeChecked();

    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => {
      expect(exportBackupMock).toHaveBeenCalledTimes(1);
    });
    expect(exportBackupMock).toHaveBeenCalledWith(
      expect.objectContaining({ all: false }),
      undefined,
      false,
    );
    await waitFor(() => {
      expect(allData).toBeChecked();
      expect(zip).toBeChecked();
    });
    expect(screen.getByText("backup.zip")).toBeInTheDocument();
  });

  it("restores from the selected backup file payload", async () => {
    const user = userEvent.setup();
    restoreBackupMock.mockResolvedValue({
      applied: { accounts_groups: 1 },
      skipped: {},
      marketDataCredentialsUnavailable: ["alpaca-primary", "oanda-fx"],
      restartRequired: false,
    });
    renderBackupCard();

    await user.click(screen.getByRole("tab", { name: /^restore$/i }));
    await user.click(screen.getByRole("radio", { name: /overwrite/i }));
    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    if (input == null) {
      throw new Error("backup file input not found");
    }
    await user.upload(
      input,
      backupFile("backup.zip", "zip-bytes", "application/zip"),
    );
    await user.click(screen.getByRole("button", { name: /^restore$/i }));

    await waitFor(() => {
      expect(restoreBackupMock).toHaveBeenCalledTimes(1);
    });
    expect(restoreBackupMock).toHaveBeenCalledWith({
      archiveFile: {
        base64: "emlwLWJ5dGVz",
        filename: "backup.zip",
      },
      scope: { all: true },
      mode: "overwrite",
    });
    await waitFor(() => {
      expect(screen.getByText(/Applied 1, skipped 0/)).toBeInTheDocument();
    });
    expect(
      screen.getByText(
        "Credentials could not be opened for alpaca-primary, oanda-fx. Those market-data sources were restored disabled and need their credentials entered again.",
      ),
    ).toBeInTheDocument();
  });

  it.each([
    ["empty", []],
    ["absent", undefined],
  ])(
    "does not show a credentials notice when the unavailable list is %s",
    async (_, unavailable) => {
      const user = userEvent.setup();
      restoreBackupMock.mockResolvedValue({
        applied: {},
        skipped: {},
        ...(unavailable === undefined
          ? {}
          : { marketDataCredentialsUnavailable: unavailable }),
        ...(unavailable === undefined
          ? {}
          : { tradingCredentialsUnavailable: unavailable }),
        restartRequired: false,
      });
      renderBackupCard();

      await user.click(screen.getByRole("tab", { name: /^restore$/i }));
      await user.click(screen.getByRole("radio", { name: /overwrite/i }));
      const input =
        document.querySelector<HTMLInputElement>('input[type="file"]');
      if (input == null) {
        throw new Error("backup file input not found");
      }
      await user.upload(input, backupFile("backup.json", "{}"));
      await user.click(screen.getByRole("button", { name: /^restore$/i }));

      await waitFor(() => {
        expect(screen.getByText(/Applied 0, skipped 0/)).toBeInTheDocument();
      });
      expect(
        screen.queryByText(/Credentials could not be opened for/),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByRole("link", { name: "Trading connections" }),
      ).not.toBeInTheDocument();
    },
  );

  it("confirms and runs replace-all restore", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    restoreBackupMock.mockResolvedValue({
      applied: {},
      skipped: {},
      restartRequired: true,
    });
    renderBackupCard();

    await user.click(screen.getByRole("tab", { name: /^restore$/i }));
    await user.click(screen.getByRole("radio", { name: /delete selected/i }));
    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    if (input == null) {
      throw new Error("backup file input not found");
    }
    await user.upload(input, backupFile("backup.json", "{}"));
    await user.click(screen.getByRole("button", { name: /^restore$/i }));

    await waitFor(() => {
      expect(restoreBackupMock).toHaveBeenCalledTimes(1);
    });
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(restoreBackupMock).toHaveBeenCalledWith({
      archiveFile: {
        base64: "e30=",
        filename: "backup.json",
      },
      scope: { all: true },
      mode: "replace_all",
    });
  });

  it("keeps export controls unchanged when export fails", async () => {
    const user = userEvent.setup();
    exportBackupMock.mockRejectedValue(new Error("export failed"));
    renderBackupCard();

    const allData = screen.getByRole("checkbox", { name: /all data/i });
    const zip = screen.getByRole("checkbox", { name: /archive as zip/i });
    await user.click(allData);
    await user.click(zip);
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => {
      expect(screen.getByText("export failed")).toBeInTheDocument();
    });
    expect(allData).not.toBeChecked();
    expect(zip).not.toBeChecked();
  });

  it("requires confirmation before replace-all restore", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    renderBackupCard();

    await user.click(screen.getByRole("tab", { name: /^restore$/i }));
    await user.click(screen.getByRole("radio", { name: /delete selected/i }));
    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    if (input == null) {
      throw new Error("backup file input not found");
    }
    await user.upload(
      input,
      new File(
        [
          JSON.stringify({
            manifest: {
              formatVersion: 1,
              createdAt: "2026-06-22T10:00:00Z",
              realm: { code: "test", title: "Test" },
              sections: ["accounts_groups"],
            },
            data: {},
          }),
        ],
        "backup.json",
        { type: "application/json" },
      ),
    );

    await user.click(screen.getByRole("button", { name: /^restore$/i }));

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(restoreBackupMock).not.toHaveBeenCalled();
  });
});

describe("DatabaseCard", () => {
  it("requires confirmation before resetting the database", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const onReset = renderDatabaseCard();

    await user.click(screen.getByRole("button", { name: /delete all data/i }));

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(resetDatabaseMock).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
  });

  it("resets the database and reloads service info after confirmation", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    resetDatabaseMock.mockResolvedValue(undefined);
    const onReset = renderDatabaseCard();

    await user.click(screen.getByRole("button", { name: /delete all data/i }));

    await waitFor(() => {
      expect(resetDatabaseMock).toHaveBeenCalledTimes(1);
    });
    expect(onReset).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Database reset.")).toBeInTheDocument();
  });
});

describe("ServiceLifecycleCard", () => {
  it("confirms before requesting a service restart", async () => {
    const user = userEvent.setup();
    const onAction = renderServiceLifecycleCard();

    await user.click(screen.getByRole("button", { name: "Restart service" }));
    expect(onAction).not.toHaveBeenCalled();

    await user.click(
      within(screen.getByRole("alertdialog")).getByRole("button", {
        name: "Restart service",
      }),
    );

    await waitFor(() => {
      expect(onAction).toHaveBeenCalledWith("restart");
    });
  });

  it("cancels service stop without calling the action", async () => {
    const user = userEvent.setup();
    const onAction = renderServiceLifecycleCard();

    await user.click(screen.getByRole("button", { name: "Stop service" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(onAction).not.toHaveBeenCalled();
  });
});
