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
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  Account,
  TradingConnection,
  TradingProvider,
  TradingSnapshot,
  TradingSymbolVerification,
} from "@/api/types";
import { SidebarProvider } from "@/components/SidebarContext";
import { ApiError, AUTOCOMPLETE_SUGGESTION_LIMIT } from "@/framework";
import i18n from "@/i18n";
import { TradingConnections } from "@/pages/TradingConnections";
import { renderWithApi } from "@/test/apiClient";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

const provider: TradingProvider = {
  type: "alpaca",
  title: "Alpaca",
  venueAccounts: false,
  verifiesSymbols: true,
  searchesSymbols: true,
  unlinkedFees: true,
};
const api = {
  fetchTrading: vi.fn(),
  fetchAccounts: vi.fn(),
  fetchAccountState: vi.fn(),
  fetchAssets: vi.fn(),
  fetchMarketData: vi.fn(),
  createTradingConnection: vi.fn(),
  updateTradingConnectionSettings: vi.fn(),
  setTradingConnectionEnabled: vi.fn(),
  checkTradingConnection: vi.fn(),
  upsertTradingInstrument: vi.fn(),
  deleteTradingInstrument: vi.fn(),
  verifyTradingSymbol: vi.fn(),
  searchTradingSymbols: vi.fn(),
  grantTradingAccess: vi.fn(),
  revokeTradingAccess: vi.fn(),
  setTradingFeeAccount: vi.fn(),
  clearTradingFeeAccount: vi.fn(),
  createAccount: vi.fn(),
};
let snapshot: TradingSnapshot;
let accounts: string[];

function connection(
  overrides: Partial<TradingConnection> = {},
): TradingConnection {
  return {
    id: "alpaca-1",
    provider: "alpaca",
    label: "Alpaca paper",
    mode: "paper",
    enabled: false,
    venueIdentity: "venue-123",
    state: "stopped",
    secrets: { apiKey: true, apiSecret: false },
    feeAccount: "",
    instruments: [],
    access: [],
    ...overrides,
  };
}

function account(code: string): Account {
  return {
    code,
    title: "",
    blocked: false,
    blockReason: "",
    blockSource: "none",
    accountBlocked: false,
    accountBlockReason: "",
    groupBlocked: false,
    groupBlockReason: "",
    group: "",
    notes: "",
    pnlHaltReason: "",
  };
}

function updateConnection(id: string, patch: Partial<TradingConnection>) {
  snapshot = {
    ...snapshot,
    connections: snapshot.connections.map((item) =>
      item.id === id ? { ...item, ...patch } : item,
    ),
  };
  return snapshot;
}

function renderTrading() {
  return renderWithApi(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
          <DisplayPreferencesProvider
            densityStorageKey="pit-officer-test-density"
            tradeStyleStorageKey="pit-officer-test-trade-style"
          >
            <SidebarProvider>
              <TradingConnections />
            </SidebarProvider>
          </DisplayPreferencesProvider>
        </ThemeProvider>
      </MemoryRouter>
    </I18nextProvider>,
    { api },
  );
}

async function card(label = "Alpaca paper") {
  return within(await screen.findByRole("region", { name: label }));
}

async function openConnectionDetails(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    screen.getByRole("button", { name: "Add trading connection" }),
  );
  const dialog = within(
    screen.getByRole("dialog", { name: "Add trading connection" }),
  );
  await user.click(dialog.getByRole("button", { name: "Alpaca" }));
  await dialog.findByRole("heading", {
    name: "Prepare your Alpaca account",
  });
  await user.click(dialog.getByRole("button", { name: "Next" }));
  await dialog.findByRole("heading", { name: "Connection details" });
  return dialog;
}

beforeAll(() => {
  window.HTMLElement.prototype.scrollIntoView = vi.fn();
});

beforeEach(async () => {
  vi.resetAllMocks();
  await i18n.changeLanguage("en");
  snapshot = {
    providers: [provider],
    connections: [connection()],
    credentialsSealed: true,
  };
  accounts = ["alpha", "beta", "fees"];
  api.fetchTrading.mockImplementation(async () => snapshot);
  api.fetchMarketData.mockResolvedValue({
    providers: [],
    instances: [],
    freshnessSeconds: 10,
    restartRequired: false,
  });
  api.fetchAccounts.mockImplementation(async ({ code }: { code: string }) =>
    accounts.filter((item) => item.startsWith(code)).map(account),
  );
  api.fetchAccountState.mockImplementation(async (code: string) => {
    if (!accounts.includes(code))
      throw new ApiError("Account not found", "not_found", 404);
    return { account: account(code), limits: [] };
  });
  api.fetchAssets.mockImplementation(async ({ code }: { code: string }) =>
    ["BTC", "USD", "AAPL"]
      .filter((item) => item.startsWith(code))
      .map((item) => ({ code: item, title: "", assetClass: "" })),
  );
  api.createTradingConnection.mockImplementation(
    async (
      body: Pick<TradingConnection, "provider" | "label" | "mode" | "enabled">,
    ) => {
      const created = connection({
        id: "alpaca-new",
        provider: body.provider,
        label: body.label,
        mode: body.mode,
        enabled: body.enabled,
        venueIdentity: "venue-new",
        secrets: { apiKey: true, apiSecret: true },
      });
      snapshot = {
        ...snapshot,
        connections: [...snapshot.connections, created],
      };
      return created;
    },
  );
  api.updateTradingConnectionSettings.mockResolvedValue(snapshot);
  api.setTradingConnectionEnabled.mockImplementation(
    async (id: string, enabled: boolean) => {
      updateConnection(id, { enabled });
      return enabled;
    },
  );
  api.checkTradingConnection.mockResolvedValue({
    venueIdentity: "checked-123",
    venueStatus: "ACTIVE",
  });
  api.upsertTradingInstrument.mockResolvedValue(snapshot);
  api.deleteTradingInstrument.mockResolvedValue(undefined);
  api.verifyTradingSymbol.mockResolvedValue({
    supported: true,
    exists: true,
    tradable: true,
  });
  api.searchTradingSymbols.mockResolvedValue({ supported: true, matches: [] });
  api.grantTradingAccess.mockResolvedValue(snapshot);
  api.revokeTradingAccess.mockResolvedValue(undefined);
  api.setTradingFeeAccount.mockImplementation(
    async (id: string, code: string) =>
      updateConnection(id, { feeAccount: code }),
  );
  api.clearTradingFeeAccount.mockImplementation(async (id: string) => {
    updateConnection(id, { feeAccount: "" });
  });
  api.createAccount.mockImplementation(async (code: string) => {
    accounts.push(code);
    return account(code);
  });
});

describe("Trading connections", () => {
  it("renders independent connections of one provider and only secret presence flags", async () => {
    snapshot.connections = [
      Object.assign(connection(), {
        credentials: {
          apiKey: "server-key-must-not-render",
          apiSecret: "server-secret-must-not-render",
        },
      }),
      connection({
        id: "alpaca-2",
        label: "Alpaca live",
        mode: "live",
        enabled: true,
        state: "running",
        venueIdentity: "venue-456",
        secrets: { apiKey: false, apiSecret: true },
      }),
      connection({
        id: "alpaca-3",
        label: "Alpaca unavailable",
        state: "unavailable",
        error: "Venue is offline",
      }),
    ];
    renderTrading();
    const paper = await card();
    const live = await card("Alpaca live");
    const unavailable = await card("Alpaca unavailable");
    expect(
      within(paper.getByRole("list", { name: "Setup checklist" })).getAllByText(
        "Not done",
        { exact: true },
      ),
    ).toHaveLength(4);
    expect(
      screen.getByRole("heading", { name: "Trading connections" }),
    ).toBeInTheDocument();
    for (const scope of [paper, live, unavailable])
      expect(scope.getByText("Alpaca", { exact: true })).toBeInTheDocument();
    expect(
      paper.getByRole("heading", { name: "Alpaca paper" }),
    ).toBeInTheDocument();
    expect(paper.getByText("Paper", { exact: true })).toBeInTheDocument();
    expect(paper.getByText("Stopped", { exact: true })).toBeInTheDocument();
    expect(paper.getByText("Disabled", { exact: true })).toBeInTheDocument();
    expect(
      paper.getByText("Venue account", { exact: true }),
    ).toBeInTheDocument();
    expect(paper.getByText("venue-123", { exact: true })).toBeInTheDocument();
    expect(
      paper.getByText("API key: stored", { exact: true }),
    ).toBeInTheDocument();
    expect(
      paper.getByText("API secret: not set", { exact: true }),
    ).toBeInTheDocument();
    expect(live.getByText("Live", { exact: true })).toBeInTheDocument();
    expect(live.getByText("Running", { exact: true })).toBeInTheDocument();
    expect(live.getByText("venue-456", { exact: true })).toBeInTheDocument();
    expect(
      live.getByText("API key: not set", { exact: true }),
    ).toBeInTheDocument();
    expect(
      live.getByText("API secret: stored", { exact: true }),
    ).toBeInTheDocument();
    expect(live.getByRole("button", { name: "Disable" })).toBeEnabled();
    expect(
      unavailable.getByText("Unavailable", { exact: true }),
    ).toBeInTheDocument();
    expect(
      unavailable.getByText("Venue is offline", { exact: true }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/server-(key|secret)-must-not-render/),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/\*{3,}|•{3,}/)).not.toBeInTheDocument();
    expect(
      document.querySelector('input[type="password"]'),
    ).not.toBeInTheDocument();
  });

  it("shows the empty state without creating a connection", async () => {
    snapshot.connections = [];
    renderTrading();
    expect(
      await screen.findByText("No trading connections", { exact: true }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Configure a trading connection to send orders to a venue.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getAllByRole("button", { name: "Add trading connection" }),
    ).toHaveLength(2);
  });

  it("shows a missing provider error with its name and omits the connection card", async () => {
    snapshot.connections = [connection({ provider: "missing-venue" })];
    renderTrading();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Provider missing-venue is missing from the trading snapshot.",
    );
    expect(
      screen.queryByRole("region", { name: "Alpaca paper" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit settings" }),
    ).not.toBeInTheDocument();
  });

  it("checks a disabled connection and shows its venue identity and status", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const callsBeforeCheck = api.fetchTrading.mock.calls.length;
    await user.click(
      within(scope.getByRole("region", { name: "Check connection" })).getByRole(
        "button",
        { name: "Check connection" },
      ),
    );
    expect(api.checkTradingConnection).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
    );
    expect(await scope.findByRole("status")).toHaveTextContent(
      "Venue account checked-123 · status ACTIVE",
    );
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeCheck,
      ),
    );
  });

  it("shows the server error when a connection check fails", async () => {
    const user = userEvent.setup();
    api.checkTradingConnection.mockRejectedValue(
      new ApiError("Credentials rejected by venue", "upstream", 502),
    );
    renderTrading();
    const scope = await card();
    await user.click(
      within(scope.getByRole("region", { name: "Check connection" })).getByRole(
        "button",
        { name: "Check connection" },
      ),
    );
    expect(
      await scope.findByText("Credentials rejected by venue"),
    ).toBeInTheDocument();
    expect(scope.queryByRole("status")).not.toBeInTheDocument();
    expect(api.checkTradingConnection).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
    );
  });

  it("marks the check step not done after a successful check followed by a failure", async () => {
    const user = userEvent.setup();
    api.checkTradingConnection
      .mockResolvedValueOnce({
        venueIdentity: "checked-123",
        venueStatus: "ACTIVE",
      })
      .mockRejectedValueOnce(new ApiError("Venue is offline", "upstream", 502));
    renderTrading();
    const scope = await card();
    const checklist = within(
      scope.getByRole("list", { name: "Setup checklist" }),
    );
    await user.click(scope.getByRole("button", { name: "Check connection" }));
    expect(await scope.findByRole("status")).toHaveTextContent("checked-123");
    expect(checklist.getAllByText("Done", { exact: true })).toHaveLength(1);
    await user.click(scope.getByRole("button", { name: "Check connection" }));
    expect(await scope.findByText("Venue is offline")).toBeInTheDocument();
    expect(
      checklist.queryByText("Done", { exact: true }),
    ).not.toBeInTheDocument();
    expect(checklist.getAllByText("Not done", { exact: true })).toHaveLength(4);
    expect(scope.queryByRole("status")).not.toBeInTheDocument();
  });

  it.each([
    {
      action: "Check connection",
      method: "checkTradingConnection" as const,
      field: "",
    },
    {
      action: "Search",
      method: "searchTradingSymbols" as const,
      field: "Search symbol",
    },
    {
      action: "Verify symbol",
      method: "verifyTradingSymbol" as const,
      field: "External symbol",
    },
  ])(
    "keeps Disable and Revoke usable while $action never returns",
    async ({ action, method, field }) => {
      const user = userEvent.setup();
      snapshot.connections = [
        connection({
          enabled: true,
          state: "running",
          access: [{ account: "alpha", venueAccount: "" }],
        }),
      ];
      api[method].mockReturnValue(new Promise(() => {}));
      renderTrading();
      const scope = await card();
      if (field !== "")
        await user.type(scope.getByRole("textbox", { name: field }), "ETH");
      const venueButton = scope.getByRole("button", { name: action });
      await user.click(venueButton);
      expect(venueButton).toBeDisabled();
      await user.click(venueButton);
      expect(api[method]).toHaveBeenCalledTimes(1);
      expect(scope.getByRole("button", { name: "Disable" })).toBeEnabled();
      expect(scope.getByRole("button", { name: "Revoke" })).toBeEnabled();
      await user.click(scope.getByRole("button", { name: "Disable" }));
      expect(api.setTradingConnectionEnabled).toHaveBeenCalledExactlyOnceWith(
        "alpaca-1",
        false,
      );
      expect(
        await scope.findByRole("button", { name: "Enable" }),
      ).toBeEnabled();
      await user.click(scope.getByRole("button", { name: "Revoke" }));
      expect(api.revokeTradingAccess).not.toHaveBeenCalled();
      await user.click(
        within(
          screen.getByRole("alertdialog", { name: "Revoke access?" }),
        ).getByRole("button", { name: "Revoke" }),
      );
      expect(api.revokeTradingAccess).toHaveBeenCalledExactlyOnceWith(
        "alpaca-1",
        "alpha",
      );
      await waitFor(() =>
        expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
      );
      expect(venueButton).toBeDisabled();
    },
  );

  it("sends only a changed label and omits both empty secrets", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      (await card()).getByRole("button", { name: "Edit settings" }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    expect(dialog.getByLabelText("API key", { exact: true })).toHaveValue("");
    expect(dialog.getByLabelText("API secret", { exact: true })).toHaveValue(
      "",
    );
    await user.clear(dialog.getByRole("textbox", { name: "Label" }));
    await user.type(
      dialog.getByRole("textbox", { name: "Label" }),
      "Renamed paper",
    );
    const callsBeforeSave = api.fetchTrading.mock.calls.length;
    await user.click(dialog.getByRole("button", { name: "Save" }));
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { label: "Renamed paper" },
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(callsBeforeSave);
  });

  it.each([
    { field: "API key", credentials: { apiKey: "typed-new-key" } },
    { field: "API secret", credentials: { apiSecret: "typed-new-secret" } },
  ])(
    "sends only the typed $field and never the unchanged label or other secret",
    async ({ field, credentials }) => {
      const user = userEvent.setup();
      renderTrading();
      const scope = await card();
      await user.click(scope.getByRole("button", { name: "Edit settings" }));
      const dialog = within(
        screen.getByRole("dialog", { name: "Edit settings" }),
      );
      expect(dialog.getByLabelText("API key", { exact: true })).toHaveAttribute(
        "placeholder",
        "Keep stored secret",
      );
      for (const name of ["API key", "API secret"]) {
        expect(dialog.getByLabelText(name, { exact: true })).toHaveAttribute(
          "type",
          "password",
        );
        expect(dialog.getByLabelText(name, { exact: true })).toHaveAttribute(
          "autocomplete",
          "new-password",
        );
        expect(dialog.getByLabelText(name, { exact: true })).toHaveValue("");
      }
      const storage = vi.spyOn(Storage.prototype, "setItem");
      const value = Object.values(credentials)[0];
      await user.type(dialog.getByLabelText(field, { exact: true }), value);
      await user.click(dialog.getByRole("button", { name: "Save" }));
      expect(
        api.updateTradingConnectionSettings,
      ).toHaveBeenCalledExactlyOnceWith("alpaca-1", { credentials });
      await waitFor(() =>
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
      );
      expect(
        storage.mock.calls.some((call) =>
          call.some((argument) => argument.includes(value)),
        ),
      ).toBe(false);
      storage.mockRestore();
      await user.click(scope.getByRole("button", { name: "Edit settings" }));
      expect(
        within(screen.getByRole("dialog")).getByLabelText(field, {
          exact: true,
        }),
      ).toHaveValue("");
    },
  );

  it("keeps a label renamed by polling when the dialog only replaces a secret", async () => {
    vi.useFakeTimers();
    try {
      renderTrading();
      await act(async () => {});
      fireEvent.click(screen.getByRole("button", { name: "Edit settings" }));
      const dialog = within(
        screen.getByRole("dialog", { name: "Edit settings" }),
      );
      fireEvent.change(dialog.getByLabelText("API secret", { exact: true }), {
        target: { value: "new-secret" },
      });
      updateConnection("alpaca-1", { label: "Renamed elsewhere" });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(
        screen.getByRole("heading", {
          name: "Renamed elsewhere",
          hidden: true,
        }),
      ).toBeInTheDocument();
      expect(dialog.getByRole("textbox", { name: "Label" })).toHaveValue(
        "Alpaca paper",
      );
      fireEvent.click(dialog.getByRole("button", { name: "Save" }));
      await act(async () => {});
      expect(
        api.updateTradingConnectionSettings,
      ).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
        credentials: { apiSecret: "new-secret" },
      });
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("trims both secrets before replacing them", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      (await card()).getByRole("button", { name: "Edit settings" }),
    );
    const dialog = within(screen.getByRole("dialog"));
    await user.type(
      dialog.getByLabelText("API key", { exact: true }),
      "  padded-key  ",
    );
    await user.type(
      dialog.getByLabelText("API secret", { exact: true }),
      "  padded-secret  ",
    );
    await user.click(dialog.getByRole("button", { name: "Save" }));
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      {
        credentials: { apiKey: "padded-key", apiSecret: "padded-secret" },
      },
    );
  });

  it("ignores whitespace-only secrets when deciding changes and saving a label", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      (await card()).getByRole("button", { name: "Edit settings" }),
    );
    const dialog = within(screen.getByRole("dialog"));
    await user.type(dialog.getByLabelText("API key", { exact: true }), "   ");
    await user.type(
      dialog.getByLabelText("API secret", { exact: true }),
      "   ",
    );
    expect(dialog.getByRole("button", { name: "Save" })).toBeDisabled();
    await user.clear(dialog.getByRole("textbox", { name: "Label" }));
    await user.type(
      dialog.getByRole("textbox", { name: "Label" }),
      "Renamed paper",
    );
    await user.click(dialog.getByRole("button", { name: "Save" }));
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { label: "Renamed paper" },
    );
  });

  it.each(["Escape", "Close", "outside click"])(
    "ignores %s while settings save is pending",
    async (close) => {
      const user = userEvent.setup();
      let finishSave!: (result: TradingSnapshot) => void;
      api.updateTradingConnectionSettings.mockImplementationOnce(
        () =>
          new Promise<TradingSnapshot>((resolve) => {
            finishSave = resolve;
          }),
      );
      renderTrading();
      await user.click(
        (await card()).getByRole("button", { name: "Edit settings" }),
      );
      const dialogElement = screen.getByRole("dialog", {
        name: "Edit settings",
      });
      const dialog = within(dialogElement);
      const requestClose = async () => {
        if (close === "Escape") await user.keyboard("{Escape}");
        else if (close === "Close")
          await user.click(
            within(screen.getByRole("dialog")).getByRole("button", {
              name: "Close",
            }),
          );
        else
          fireEvent.pointerDown(document.body, {
            button: 0,
            pointerType: "mouse",
          });
      };
      await user.type(
        dialog.getByLabelText("API secret", { exact: true }),
        "new-secret",
      );
      await user.click(dialog.getByRole("button", { name: "Save" }));
      expect(api.updateTradingConnectionSettings).toHaveBeenCalledTimes(1);
      expect(dialog.getByRole("button", { name: "Cancel" })).toBeDisabled();
      await requestClose();
      expect(screen.getByRole("dialog", { name: "Edit settings" })).toBe(
        dialogElement,
      );
      await act(async () => finishSave(snapshot));
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      await user.click(
        (await card()).getByRole("button", { name: "Edit settings" }),
      );
      expect(
        screen.getByRole("dialog", { name: "Edit settings" }),
      ).toBeInTheDocument();
      expect(
        within(screen.getByRole("dialog")).getByLabelText("API secret", {
          exact: true,
        }),
      ).toHaveValue("");
      await requestClose();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    },
  );

  it("keeps an empty label and unchanged fields out of settings requests", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      (await card()).getByRole("button", { name: "Edit settings" }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    expect(dialog.getByRole("button", { name: "Save" })).toBeDisabled();
    await user.clear(dialog.getByRole("textbox", { name: "Label" }));
    expect(dialog.getByRole("button", { name: "Save" })).toBeDisabled();
    await user.type(
      dialog.getByLabelText("API secret", { exact: true }),
      "typed-secret",
    );
    await user.click(dialog.getByRole("button", { name: "Save" }));
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { credentials: { apiSecret: "typed-secret" } },
    );
  });

  it.each([false, true])(
    "shows the unsealed warning only when credentialsSealed is false (%s)",
    async (sealed) => {
      const user = userEvent.setup();
      snapshot.credentialsSealed = sealed;
      renderTrading();
      await user.click(
        (await card()).getByRole("button", { name: "Edit settings" }),
      );
      const dialog = within(
        screen.getByRole("dialog", { name: "Edit settings" }),
      );
      const warning = dialog.queryByRole("heading", {
        name: "Keys will be stored unencrypted",
      });
      if (sealed) {
        expect(warning).not.toBeInTheDocument();
        expect(
          dialog.queryByRole("link", { name: "How to set a master key" }),
        ).not.toBeInTheDocument();
      } else {
        expect(warning).toBeInTheDocument();
        expect(dialog.getByRole("alert")).toHaveTextContent(
          "Anyone who can read the database or a backup can trade on this account.",
        );
        expect(
          dialog.getByText(
            "A lost master key makes the database unopenable - back the key up before you set it.",
          ),
        ).toBeInTheDocument();
        const link = dialog.getByRole("link", {
          name: "How to set a master key",
        });
        expect(link).toHaveAttribute(
          "href",
          "https://github.com/openpitkit/officer/blob/main/docs/configuration.md#master-key",
        );
        expect(link).toHaveAttribute("target", "_blank");
        expect(link).toHaveAttribute("rel", "noopener noreferrer");
      }
      expect(dialog.getByLabelText("API key", { exact: true })).toBeEnabled();
      expect(
        dialog.getByLabelText("API secret", { exact: true }),
      ).toBeEnabled();
      await user.type(
        dialog.getByLabelText("API key", { exact: true }),
        "usable-key",
      );
      await user.click(dialog.getByRole("button", { name: "Save" }));
      expect(
        api.updateTradingConnectionSettings,
      ).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
        credentials: { apiKey: "usable-key" },
      });
    },
  );

  it("keeps the settings dialog open and shows a server error", async () => {
    const user = userEvent.setup();
    api.updateTradingConnectionSettings.mockRejectedValue(
      new ApiError("Keys belong to another venue account", "conflict", 409),
    );
    renderTrading();
    await user.click(
      (await card()).getByRole("button", { name: "Edit settings" }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    await user.type(
      dialog.getByLabelText("API key", { exact: true }),
      "wrong-venue-key",
    );
    const callsBeforeSave = api.fetchTrading.mock.calls.length;
    await user.click(dialog.getByRole("button", { name: "Save" }));
    expect(
      await dialog.findByText("Keys belong to another venue account"),
    ).toBeInTheDocument();
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { credentials: { apiKey: "wrong-venue-key" } },
    );
    expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeSave);
  });

  it("enables and disables a connection and reloads after each mutation", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const callsBeforeEnable = api.fetchTrading.mock.calls.length;
    await user.click(scope.getByRole("button", { name: "Enable" }));
    const disable = await scope.findByRole("button", { name: "Disable" });
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeEnable,
      ),
    );
    const callsBeforeDisable = api.fetchTrading.mock.calls.length;
    await user.click(disable);
    expect(api.setTradingConnectionEnabled.mock.calls).toEqual([
      ["alpaca-1", true],
      ["alpaca-1", false],
    ]);
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeDisable,
      ),
    );
  });

  it("searches symbols, splits BASE/QUOTE and leaves all instrument fields editable", async () => {
    const user = userEvent.setup();
    api.searchTradingSymbols.mockResolvedValue({
      supported: true,
      matches: [
        {
          symbol: "BTC/USD",
          name: "Bitcoin USD",
          exchange: "CRYPTO",
          tradable: true,
        },
        { symbol: "AAPL", name: "Apple", exchange: "NASDAQ", tradable: false },
      ],
    });
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add instrument" }));
    expect(form.getByRole("button", { name: "Search" })).toBeDisabled();
    await user.type(
      form.getByRole("textbox", { name: "Search symbol" }),
      "   ",
    );
    expect(form.getByRole("button", { name: "Search" })).toBeDisabled();
    await user.clear(form.getByRole("textbox", { name: "Search symbol" }));
    await user.type(
      form.getByRole("textbox", { name: "Search symbol" }),
      "BTC",
    );
    await user.click(form.getByRole("button", { name: "Search" }));
    expect(api.searchTradingSymbols).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { query: "BTC" },
    );
    expect(await form.findByText("Bitcoin USD")).toBeInTheDocument();
    expect(form.getByText("CRYPTO", { exact: true })).toBeInTheDocument();
    expect(form.getByText("Apple", { exact: true })).toBeInTheDocument();
    expect(form.getByText("NASDAQ", { exact: true })).toBeInTheDocument();
    expect(form.getByText("Not tradable", { exact: true })).toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "BTC/USD" }));
    expect(form.getByRole("textbox", { name: "External symbol" })).toHaveValue(
      "BTC/USD",
    );
    expect(form.getByRole("combobox", { name: "Base asset" })).toHaveValue(
      "BTC",
    );
    expect(form.getByRole("combobox", { name: "Quote asset" })).toHaveValue(
      "USD",
    );
    const callsBeforeSave = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Save" }));
    expect(api.upsertTradingInstrument).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      {
        externalSymbol: "BTC/USD",
        baseAsset: "BTC",
        quoteAsset: "USD",
        enabled: true,
      },
    );
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeSave,
      ),
    );
    await user.click(form.getByRole("button", { name: "AAPL" }));
    expect(form.getByRole("textbox", { name: "External symbol" })).toHaveValue(
      "AAPL",
    );
    expect(form.getByRole("combobox", { name: "Base asset" })).toHaveValue(
      "AAPL",
    );
    expect(form.getByRole("combobox", { name: "Quote asset" })).toHaveValue("");
    expect(form.getByRole("button", { name: "Save" })).toBeDisabled();
    await user.clear(form.getByRole("textbox", { name: "External symbol" }));
    await user.type(
      form.getByRole("textbox", { name: "External symbol" }),
      "AAPL2",
    );
    await user.clear(form.getByRole("combobox", { name: "Base asset" }));
    await user.type(
      form.getByRole("combobox", { name: "Base asset" }),
      "AAPL2",
    );
    await user.type(form.getByRole("combobox", { name: "Quote asset" }), "USD");
    await user.click(form.getByRole("checkbox", { name: "Enabled" }));
    await user.click(form.getByRole("button", { name: "Save" }));
    expect(api.upsertTradingInstrument.mock.calls).toEqual([
      [
        "alpaca-1",
        {
          externalSymbol: "BTC/USD",
          baseAsset: "BTC",
          quoteAsset: "USD",
          enabled: true,
        },
      ],
      [
        "alpaca-1",
        {
          externalSymbol: "AAPL2",
          baseAsset: "AAPL2",
          quoteAsset: "USD",
          enabled: false,
        },
      ],
    ]);
  });

  it.each([
    {
      supported: false,
      message: "Symbol search unavailable",
      absent: "No matching symbols",
    },
    {
      supported: true,
      message: "No matching symbols",
      absent: "Symbol search unavailable",
    },
  ])(
    "shows $message when search returns no matches",
    async ({ supported, message, absent }) => {
      const user = userEvent.setup();
      api.searchTradingSymbols.mockResolvedValue({ supported, matches: [] });
      renderTrading();
      const scope = await card();
      await user.type(
        scope.getByRole("textbox", { name: "Search symbol" }),
        "ETH",
      );
      await user.click(scope.getByRole("button", { name: "Search" }));
      expect(
        await scope.findByText(message, { exact: true }),
      ).toBeInTheDocument();
      expect(
        scope.queryByText(absent, { exact: true }),
      ).not.toBeInTheDocument();
      expect(
        scope.queryByRole("list", { name: "Symbol matches" }),
      ).not.toBeInTheDocument();
      expect(api.searchTradingSymbols).toHaveBeenCalledExactlyOnceWith(
        "alpaca-1",
        { query: "ETH" },
      );
    },
  );

  it("searches on Enter without saving a complete instrument draft", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add instrument" }));
    await user.type(
      form.getByRole("textbox", { name: "External symbol" }),
      "BTC/USD",
    );
    await user.type(form.getByRole("combobox", { name: "Base asset" }), "BTC");
    await user.type(form.getByRole("combobox", { name: "Quote asset" }), "USD");
    expect(form.getByRole("button", { name: "Save" })).toBeEnabled();
    await user.type(
      form.getByRole("textbox", { name: "Search symbol" }),
      "ETH{Enter}",
    );
    expect(api.searchTradingSymbols).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { query: "ETH" },
    );
    expect(await form.findByText("No matching symbols")).toBeInTheDocument();
    expect(api.upsertTradingInstrument).not.toHaveBeenCalled();
    expect(form.getByRole("textbox", { name: "External symbol" })).toHaveValue(
      "BTC/USD",
    );
  });

  it.each([
    {
      supported: true,
      exists: true,
      tradable: true,
      label: "Symbol exists and is tradable",
    },
    {
      supported: true,
      exists: true,
      tradable: false,
      label: "Symbol exists but is not tradable",
    },
    {
      supported: true,
      exists: false,
      tradable: false,
      label: "Symbol not found",
    },
    {
      supported: false,
      exists: false,
      tradable: false,
      label: "Verification unavailable",
    },
  ])("shows verify-symbol outcome: $label", async ({ label, ...result }) => {
    const user = userEvent.setup();
    api.verifyTradingSymbol.mockResolvedValue({
      ...result,
      details: "Venue catalogue details",
    });
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add instrument" }));
    await user.type(
      form.getByRole("textbox", { name: "External symbol" }),
      "AAPL",
    );
    const callsBeforeVerify = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Verify symbol" }));
    expect(await form.findByRole("status")).toHaveTextContent(label);
    expect(form.getByText("Venue catalogue details")).toBeInTheDocument();
    expect(api.verifyTradingSymbol).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "AAPL",
    );
    expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeVerify);
    await user.type(
      form.getByRole("textbox", { name: "External symbol" }),
      "2",
    );
    expect(form.queryByRole("status")).not.toBeInTheDocument();
  });

  it("does not show a verdict under a symbol picked while verify was pending", async () => {
    const user = userEvent.setup();
    api.searchTradingSymbols.mockResolvedValue({
      supported: true,
      matches: [
        { symbol: "BTC/USD", tradable: true },
        { symbol: "BTC/USDT", tradable: true },
      ],
    });
    let finishVerify!: (result: TradingSymbolVerification) => void;
    api.verifyTradingSymbol.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishVerify = resolve;
        }),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add instrument" }));
    await user.type(
      form.getByRole("textbox", { name: "Search symbol" }),
      "BTC",
    );
    await user.click(form.getByRole("button", { name: "Search" }));
    await user.click(await form.findByRole("button", { name: "BTC/USD" }));
    await user.click(form.getByRole("button", { name: "Verify symbol" }));
    expect(api.verifyTradingSymbol).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "BTC/USD",
    );
    await user.click(form.getByRole("button", { name: "BTC/USDT" }));
    expect(form.getByRole("textbox", { name: "External symbol" })).toHaveValue(
      "BTC/USDT",
    );
    await act(async () => {
      finishVerify({ supported: true, exists: true, tradable: true });
    });
    await waitFor(() =>
      expect(form.getByRole("button", { name: "Verify symbol" })).toBeEnabled(),
    );
    expect(form.queryByRole("status")).not.toBeInTheDocument();
    expect(
      form.queryByText("Symbol exists and is tradable"),
    ).not.toBeInTheDocument();

    let failVerify!: (error: Error) => void;
    api.verifyTradingSymbol.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          failVerify = reject;
        }),
    );
    await user.click(form.getByRole("button", { name: "BTC/USD" }));
    await user.click(form.getByRole("button", { name: "Verify symbol" }));
    await user.click(form.getByRole("button", { name: "BTC/USDT" }));
    await act(async () => {
      failVerify(new ApiError("BTC/USD unavailable", "upstream", 502));
    });
    await waitFor(() =>
      expect(form.getByRole("button", { name: "Verify symbol" })).toBeEnabled(),
    );
    expect(form.queryByText("BTC/USD unavailable")).not.toBeInTheDocument();
    expect(api.verifyTradingSymbol).toHaveBeenNthCalledWith(
      2,
      "alpaca-1",
      "BTC/USD",
    );
  });

  it("hides a verify error after another symbol match is picked", async () => {
    const user = userEvent.setup();
    api.searchTradingSymbols.mockResolvedValue({
      supported: true,
      matches: [
        { symbol: "BTC/USD", tradable: true },
        { symbol: "BTC/USDT", tradable: true },
      ],
    });
    api.verifyTradingSymbol.mockRejectedValue(
      new ApiError("BTC/USD unavailable", "upstream", 502),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add instrument" }));
    await user.type(
      form.getByRole("textbox", { name: "Search symbol" }),
      "BTC",
    );
    await user.click(form.getByRole("button", { name: "Search" }));
    await user.click(await form.findByRole("button", { name: "BTC/USD" }));
    await user.click(form.getByRole("button", { name: "Verify symbol" }));
    expect(await form.findByText("BTC/USD unavailable")).toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "BTC/USDT" }));
    expect(form.queryByText("BTC/USD unavailable")).not.toBeInTheDocument();
  });

  it("shows instrument save errors and keeps the draft for correction", async () => {
    const user = userEvent.setup();
    api.upsertTradingInstrument.mockRejectedValue(
      new ApiError("This pair is already mapped", "conflict", 409),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add instrument" }));
    await user.type(
      form.getByRole("textbox", { name: "External symbol" }),
      "AAPL",
    );
    await user.type(form.getByRole("combobox", { name: "Base asset" }), "AAPL");
    await user.type(form.getByRole("combobox", { name: "Quote asset" }), "USD");
    const callsBeforeSave = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Save" }));
    expect(
      await scope.findByText("This pair is already mapped"),
    ).toBeInTheDocument();
    expect(api.upsertTradingInstrument).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      {
        externalSymbol: "AAPL",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        enabled: true,
      },
    );
    expect(form.getByRole("textbox", { name: "External symbol" })).toHaveValue(
      "AAPL",
    );
    expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeSave);
  });

  it("toggles an instrument with the same mapping and confirms deletion", async () => {
    const user = userEvent.setup();
    const instrument = {
      externalSymbol: "BTC/USD",
      baseAsset: "BTC",
      quoteAsset: "USD",
      enabled: true,
    };
    snapshot.connections = [connection({ instruments: [instrument] })];
    renderTrading();
    const scope = await card();
    const table = within(scope.getByRole("table", { name: "Instruments" }));
    const callsBeforeDisable = api.fetchTrading.mock.calls.length;
    await user.click(table.getByRole("button", { name: "Disable" }));
    expect(api.upsertTradingInstrument).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { ...instrument, enabled: false },
    );
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeDisable,
      ),
    );
    await user.click(table.getByRole("button", { name: "Delete" }));
    expect(api.deleteTradingInstrument).not.toHaveBeenCalled();
    let dialog = within(
      screen.getByRole("alertdialog", { name: "Delete instrument?" }),
    );
    expect(
      dialog.getByText("BTC/USD will be removed from Alpaca paper."),
    ).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(api.deleteTradingInstrument).not.toHaveBeenCalled();
    await user.click(table.getByRole("button", { name: "Delete" }));
    dialog = within(
      screen.getByRole("alertdialog", { name: "Delete instrument?" }),
    );
    const callsBeforeDelete = api.fetchTrading.mock.calls.length;
    await user.click(dialog.getByRole("button", { name: "Delete" }));
    expect(api.deleteTradingInstrument).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "BTC/USD",
    );
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
      callsBeforeDelete,
    );
  });

  it("uses asset and account code suggestions with starts_with filters", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    await user.type(scope.getByRole("combobox", { name: "Base asset" }), "BT");
    await user.click(await scope.findByRole("option", { name: "BTC" }));
    expect(scope.getByRole("combobox", { name: "Base asset" })).toHaveValue(
      "BTC",
    );
    expect(api.fetchAssets).toHaveBeenCalledWith(
      {
        code: "BT",
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      expect.any(AbortSignal),
    );
    await user.type(scope.getByRole("combobox", { name: "Account" }), "al");
    await user.click(await scope.findByRole("option", { name: "alpha" }));
    expect(scope.getByRole("combobox", { name: "Account" })).toHaveValue(
      "alpha",
    );
    expect(api.fetchAccounts).toHaveBeenCalledWith(
      {
        code: "al",
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      expect.any(AbortSignal),
    );
  });

  it.each(["Account", "Fee account"])(
    "keeps %s suggestions when an edit preserves the trimmed query",
    async (field) => {
      const user = userEvent.setup();
      renderTrading();
      const scope = await card();
      const input = scope.getByRole("combobox", { name: field });
      await user.type(input, "al");
      expect(
        await scope.findByRole("option", { name: "alpha" }),
      ).toBeInTheDocument();
      await user.type(input, " ");
      expect(input).toHaveValue("al ");
      expect(scope.getByRole("option", { name: "alpha" })).toBeInTheDocument();
      expect(api.fetchAccounts).toHaveBeenCalledWith(
        {
          code: "al",
          codeMatch: "starts_with",
          limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
          sort: "code",
        },
        expect.any(AbortSignal),
      );
    },
  );

  it.each([
    ["constructor", "Account"],
    ["__proto__", "Account"],
    ["constructor", "Fee account"],
    ["__proto__", "Fee account"],
  ])(
    "renders prototype-like account code %s in the %s field",
    async (code, field) => {
      const user = userEvent.setup();
      renderTrading();
      const scope = await card();
      const input = scope.getByRole("combobox", { name: field });
      await user.type(input, code);
      expect(input).toHaveValue(code);
    },
  );

  it("keeps base and quote asset suggestions independent", async () => {
    const user = userEvent.setup();
    api.fetchAssets.mockImplementation(async ({ code }: { code: string }) => {
      if (code === "U") return [{ code: "USD", title: "", assetClass: "" }];
      if (code === "US") return [{ code: "USDT", title: "", assetClass: "" }];
      throw new Error(`Unexpected asset query: ${code}`);
    });
    renderTrading();
    const scope = await card();
    const base = scope.getByRole("combobox", { name: "Base asset" });
    const quote = scope.getByRole("combobox", { name: "Quote asset" });
    await user.type(base, "U");
    expect(
      await scope.findByRole("option", { name: "USD" }),
    ).toBeInTheDocument();
    await user.type(quote, "US");
    expect(
      await scope.findByRole("option", { name: "USDT" }),
    ).toBeInTheDocument();
    const quoteList = within(
      document.getElementById(quote.getAttribute("aria-controls")!)!,
    );
    expect(
      quoteList.queryByRole("option", { name: "USD" }),
    ).not.toBeInTheDocument();
    await user.click(base);
    const baseList = within(
      document.getElementById(base.getAttribute("aria-controls")!)!,
    );
    expect(baseList.getByRole("option", { name: "USD" })).toBeInTheDocument();
    expect(
      baseList.queryByRole("option", { name: "USDT" }),
    ).not.toBeInTheDocument();
  });

  it.each(["Base asset", "Quote asset"])(
    "shows an asset lookup failure and clears it when %s empties",
    async (field) => {
      const user = userEvent.setup();
      const errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
      try {
        api.fetchAssets.mockRejectedValue(new Error("Asset lookup offline"));
        renderTrading();
        const scope = await card();
        const input = scope.getByRole("combobox", { name: field });
        await user.type(input, "BT");
        expect(await scope.findByRole("alert")).toHaveTextContent(
          "Asset suggestions could not be loaded.",
        );
        expect(scope.queryByRole("option")).not.toBeInTheDocument();
        await user.clear(input);
        await waitFor(() =>
          expect(scope.queryByRole("alert")).not.toBeInTheDocument(),
        );
      } finally {
        errorLog.mockRestore();
      }
    },
  );

  it.each(["Account", "Fee account"])(
    "shows an account suggestion failure and clears it when %s empties",
    async (field) => {
      const user = userEvent.setup();
      api.fetchAccounts.mockRejectedValue(
        new ApiError("Account suggestions offline", "upstream", 502),
      );
      renderTrading();
      const scope = await card();
      const input = scope.getByRole("combobox", { name: field });
      await user.type(input, "al");
      expect(await scope.findByRole("alert")).toHaveTextContent(
        "Account suggestions offline",
      );
      await user.type(input, " ");
      expect(input).toHaveValue("al ");
      expect(scope.getByRole("alert")).toHaveTextContent(
        "Account suggestions offline",
      );
      await user.clear(input);
      await waitFor(() =>
        expect(scope.queryByRole("alert")).not.toBeInTheDocument(),
      );
    },
  );

  it("grants access without an Alpaca venue-account field and reloads", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add account" }));
    const table = within(scope.getByRole("table", { name: "Account access" }));
    expect(
      table.getAllByRole("columnheader").map((header) => header.textContent),
    ).toEqual(["Account", "Actions"]);
    expect(
      form.queryByRole("textbox", { name: "Venue account" }),
    ).not.toBeInTheDocument();
    await user.type(form.getByRole("combobox", { name: "Account" }), "alpha");
    const callsBeforeGrant = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Add account" }));
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      account: "alpha",
    });
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeGrant,
      ),
    );
  });

  it("blocks a second distinct account until a fee account is set and focuses that card's field", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({ access: [{ account: "alpha", venueAccount: "" }] }),
      connection({ id: "alpaca-2", label: "Other paper" }),
    ];
    renderTrading();
    const scope = await card();
    const other = await card("Other paper");
    const form = within(scope.getByRole("form", { name: "Add account" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "beta");
    expect(form.getByRole("button", { name: "Add account" })).toBeDisabled();
    expect(
      form.getByText(
        "Set a fee account first. Alpaca charges fees that are not tied to orders; with several accounts on one connection Officer needs one account to book them on.",
      ),
    ).toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "Add account" }));
    expect(api.grantTradingAccess).not.toHaveBeenCalled();
    await user.click(form.getByRole("button", { name: "Set fee account" }));
    expect(scope.getByRole("combobox", { name: "Fee account" })).toHaveFocus();
    expect(
      other.getByRole("combobox", { name: "Fee account" }),
    ).not.toHaveFocus();
    expect(window.HTMLElement.prototype.scrollIntoView).toHaveBeenCalled();
    const fee = within(scope.getByRole("form", { name: "Fee account" }));
    await user.type(fee.getByRole("combobox", { name: "Fee account" }), "fees");
    await user.click(fee.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(form.getByRole("button", { name: "Add account" })).toBeEnabled(),
    );
    await user.click(form.getByRole("button", { name: "Add account" }));
    expect(api.setTradingFeeAccount).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "fees",
    );
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      account: "beta",
    });
    expect(api.createAccount).not.toHaveBeenCalled();
  });

  it("allows replacing an existing binding without a fee account", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({ access: [{ account: "alpha", venueAccount: "" }] }),
    ];
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add account" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "alpha");
    expect(
      form.queryByRole("button", { name: "Set fee account" }),
    ).not.toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "Add account" }));
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      account: "alpha",
    });
  });

  it("recognizes fee_account_required by code and shows the server message with a focus link", async () => {
    const user = userEvent.setup();
    api.grantTradingAccess.mockRejectedValue(
      new ApiError("Choose a fee ledger", "fee_account_required", 409),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add account" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "beta");
    const callsBeforeGrant = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Add account" }));
    expect(await scope.findByText("Choose a fee ledger")).toBeInTheDocument();
    await user.click(scope.getByRole("button", { name: "Set fee account" }));
    expect(scope.getByRole("combobox", { name: "Fee account" })).toHaveFocus();
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      account: "beta",
    });
    expect(api.setTradingFeeAccount).not.toHaveBeenCalled();
    expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeGrant);
  });

  it("does not infer a fee-account error from message text", async () => {
    const user = userEvent.setup();
    api.grantTradingAccess.mockRejectedValue(
      new ApiError("fee_account_required", "forbidden", 403),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Add account" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "alpha");
    await user.click(form.getByRole("button", { name: "Add account" }));
    expect(await scope.findByText("fee_account_required")).toBeInTheDocument();
    expect(
      scope.queryByRole("button", { name: "Set fee account" }),
    ).not.toBeInTheDocument();
  });

  it("sets and clears an existing fee account without creating or granting access", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Fee account" }));
    expect(scope.getByText("Not set", { exact: true })).toBeInTheDocument();
    expect(
      scope.getByText(
        "Alpaca fees are not tied to orders. On a connection shared by several accounts, they are all booked on this account.",
      ),
    ).toBeInTheDocument();
    await user.type(
      form.getByRole("combobox", { name: "Fee account" }),
      "fees",
    );
    await user.click(await form.findByRole("option", { name: "fees" }));
    expect(
      form.queryByRole("button", { name: "Create account fees and use it" }),
    ).not.toBeInTheDocument();
    const callsBeforeSave = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Save" }));
    expect(api.setTradingFeeAccount).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "fees",
    );
    const clear = await form.findByRole("button", { name: "Clear" });
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeSave,
      ),
    );
    const callsBeforeClear = api.fetchTrading.mock.calls.length;
    await user.click(clear);
    expect(api.clearTradingFeeAccount).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
    );
    await waitFor(() =>
      expect(scope.getByText("Not set", { exact: true })).toBeInTheDocument(),
    );
    expect(
      form.queryByRole("button", { name: "Clear" }),
    ).not.toBeInTheDocument();
    expect(api.createAccount).not.toHaveBeenCalled();
    expect(api.grantTradingAccess).not.toHaveBeenCalled();
    expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
      callsBeforeClear,
    );
  });

  it.each(["found", "not_found", "upstream"])(
    "uses an exact fee account lookup with outcome %s",
    async (outcome) => {
      const user = userEvent.setup();
      api.fetchAccounts.mockResolvedValue(
        Array.from({ length: AUTOCOMPLETE_SUGGESTION_LIMIT }, (_, index) =>
          account(`title-match-${index}`),
        ),
      );
      if (outcome === "found")
        api.fetchAccountState.mockResolvedValue({
          account: account("fees"),
          limits: [],
        });
      else if (outcome === "not_found")
        api.fetchAccountState.mockRejectedValue(
          new ApiError("Account not found", "not_found", 404),
        );
      else
        api.fetchAccountState.mockRejectedValue(
          new ApiError("Exact lookup offline", "upstream", 502),
        );
      renderTrading();
      const scope = await card();
      const form = within(scope.getByRole("form", { name: "Fee account" }));
      await user.type(
        form.getByRole("combobox", { name: "Fee account" }),
        "fees",
      );
      await waitFor(() =>
        expect(api.fetchAccountState).toHaveBeenCalledWith(
          "fees",
          expect.any(AbortSignal),
        ),
      );
      await act(async () => {});
      if (outcome === "not_found") {
        expect(
          await form.findByRole("button", {
            name: "Create account fees and use it",
          }),
        ).toBeEnabled();
        expect(scope.queryByRole("alert")).not.toBeInTheDocument();
      } else {
        expect(
          form.queryByRole("button", {
            name: "Create account fees and use it",
          }),
        ).not.toBeInTheDocument();
        if (outcome === "upstream")
          expect(await scope.findByRole("alert")).toHaveTextContent(
            "Exact lookup offline",
          );
        else expect(scope.queryByRole("alert")).not.toBeInTheDocument();
      }
      expect(api.createAccount).not.toHaveBeenCalled();
      expect(api.setTradingFeeAccount).not.toHaveBeenCalled();
    },
  );

  it("keeps the create offer when only trailing whitespace is added to the fee account", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Fee account" }));
    const input = form.getByRole("combobox", { name: "Fee account" });
    await user.type(input, "fees2");
    expect(
      await form.findByRole("button", {
        name: "Create account fees2 and use it",
      }),
    ).toBeEnabled();
    await user.type(input, " ");
    expect(input).toHaveValue("fees2 ");
    expect(
      form.getByRole("button", { name: "Create account fees2 and use it" }),
    ).toBeEnabled();
  });

  it("creates a missing account only after the explicit create-and-use click", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Fee account" }));
    await user.type(
      form.getByRole("combobox", { name: "Fee account" }),
      "new-fees",
    );
    const create = await form.findByRole("button", {
      name: "Create account new-fees and use it",
    });
    expect(api.createAccount).not.toHaveBeenCalled();
    expect(api.setTradingFeeAccount).not.toHaveBeenCalled();
    const callsBeforeCreate = api.fetchTrading.mock.calls.length;
    await user.click(create);
    expect(api.createAccount).toHaveBeenCalledExactlyOnceWith("new-fees");
    expect(api.setTradingFeeAccount).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "new-fees",
    );
    expect(api.createAccount.mock.invocationCallOrder[0]).toBeLessThan(
      api.setTradingFeeAccount.mock.invocationCallOrder[0],
    );
    expect(api.grantTradingAccess).not.toHaveBeenCalled();
    expect(
      await scope.findByText("new-fees", { exact: true }),
    ).toBeInTheDocument();
    expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
      callsBeforeCreate,
    );
  });

  it("does not set a fee account when account creation fails", async () => {
    const user = userEvent.setup();
    api.createAccount.mockRejectedValue(
      new ApiError("Account creation forbidden", "forbidden", 403),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Fee account" }));
    await user.type(
      form.getByRole("combobox", { name: "Fee account" }),
      "new-fees",
    );
    const callsBeforeCreate = api.fetchTrading.mock.calls.length;
    await user.click(
      await form.findByRole("button", {
        name: "Create account new-fees and use it",
      }),
    );
    expect(
      await scope.findByText("Account creation forbidden"),
    ).toBeInTheDocument();
    expect(api.createAccount).toHaveBeenCalledExactlyOnceWith("new-fees");
    expect(api.setTradingFeeAccount).not.toHaveBeenCalled();
    expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeCreate);
  });

  it("shows the server's fee-account clear conflict and keeps the fee account", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({
        feeAccount: "fees",
        access: [
          { account: "alpha", venueAccount: "" },
          { account: "beta", venueAccount: "" },
        ],
      }),
    ];
    api.clearTradingFeeAccount.mockRejectedValue(
      new ApiError(
        "Several accounts still use this connection",
        "fee_account_required",
        409,
      ),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Fee account" }));
    await user.click(form.getByRole("button", { name: "Clear" }));
    expect(
      await scope.findByText("Several accounts still use this connection"),
    ).toBeInTheDocument();
    expect(api.clearTradingFeeAccount).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
    );
    expect(scope.getByText("fees", { exact: true })).toBeInTheDocument();
    await user.click(scope.getByRole("button", { name: "Set fee account" }));
    expect(form.getByRole("combobox", { name: "Fee account" })).toHaveFocus();
  });

  it("confirms revoke and does not revoke on cancel", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({ access: [{ account: "alpha", venueAccount: "" }] }),
    ];
    renderTrading();
    const scope = await card();
    const table = within(scope.getByRole("table", { name: "Account access" }));
    await user.click(table.getByRole("button", { name: "Revoke" }));
    expect(api.revokeTradingAccess).not.toHaveBeenCalled();
    let dialog = within(
      screen.getByRole("alertdialog", { name: "Revoke access?" }),
    );
    expect(
      dialog.getByText("Revoke access of alpha to Alpaca paper?"),
    ).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(api.revokeTradingAccess).not.toHaveBeenCalled();
    await user.click(table.getByRole("button", { name: "Revoke" }));
    dialog = within(
      screen.getByRole("alertdialog", { name: "Revoke access?" }),
    );
    const callsBeforeRevoke = api.fetchTrading.mock.calls.length;
    await user.click(dialog.getByRole("button", { name: "Revoke" }));
    expect(api.revokeTradingAccess).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "alpha",
    );
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
      callsBeforeRevoke,
    );
  });

  it("shows destructive action errors inside the confirmation dialog", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({
        instruments: [
          {
            externalSymbol: "BTC/USD",
            baseAsset: "BTC",
            quoteAsset: "USD",
            enabled: true,
          },
        ],
      }),
    ];
    api.deleteTradingInstrument.mockRejectedValue(
      new ApiError("Instrument deletion refused", "forbidden", 403),
    );
    renderTrading();
    const scope = await card();
    await user.click(
      within(scope.getByRole("table", { name: "Instruments" })).getByRole(
        "button",
        { name: "Delete" },
      ),
    );
    const dialog = within(
      screen.getByRole("alertdialog", { name: "Delete instrument?" }),
    );
    const callsBeforeDelete = api.fetchTrading.mock.calls.length;
    await user.click(dialog.getByRole("button", { name: "Delete" }));
    expect(
      await dialog.findByText("Instrument deletion refused"),
    ).toBeInTheDocument();
    expect(api.deleteTradingInstrument).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "BTC/USD",
    );
    expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeDelete);
  });

  it("keeps a check failure out of an open revoke confirmation", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({ access: [{ account: "alpha", venueAccount: "" }] }),
    ];
    let failCheck!: (error: Error) => void;
    api.checkTradingConnection.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          failCheck = reject;
        }),
    );
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Check connection" }));
    await user.click(scope.getByRole("button", { name: "Revoke" }));
    const dialog = within(
      screen.getByRole("alertdialog", { name: "Revoke access?" }),
    );
    await act(async () => {
      failCheck(new ApiError("Venue is offline", "upstream", 502));
    });
    expect(dialog.queryByText("Venue is offline")).not.toBeInTheDocument();
    expect(dialog.queryByRole("alert")).not.toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(
      within(scope.getByRole("region", { name: "Check connection" })).getByText(
        "Venue is offline",
      ),
    ).toBeInTheDocument();
    expect(api.revokeTradingAccess).not.toHaveBeenCalled();
  });

  it("keeps the fee-account link when a symbol search fails after a fee_account_required failure", async () => {
    const user = userEvent.setup();
    api.grantTradingAccess.mockRejectedValue(
      new ApiError("Choose a fee ledger", "fee_account_required", 409),
    );
    api.searchTradingSymbols.mockRejectedValue(
      new ApiError("Symbol search offline", "upstream", 502),
    );
    renderTrading();
    const scope = await card();
    const accountForm = within(
      scope.getByRole("form", { name: "Add account" }),
    );
    await user.type(
      accountForm.getByRole("combobox", { name: "Account" }),
      "beta",
    );
    await user.click(accountForm.getByRole("button", { name: "Add account" }));
    expect(await scope.findByText("Choose a fee ledger")).toBeInTheDocument();
    const instrumentForm = within(
      scope.getByRole("form", { name: "Add instrument" }),
    );
    await user.type(
      instrumentForm.getByRole("textbox", { name: "Search symbol" }),
      "ETH",
    );
    await user.click(instrumentForm.getByRole("button", { name: "Search" }));
    expect(
      await instrumentForm.findByText("Symbol search offline"),
    ).toBeInTheDocument();
    expect(scope.getByText("Choose a fee ledger")).toBeInTheDocument();
    await user.click(scope.getByRole("button", { name: "Set fee account" }));
    expect(scope.getByRole("combobox", { name: "Fee account" })).toHaveFocus();
  });

  it("marks checklist steps from the snapshot and session check and only navigates on step clicks", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({
        instruments: [
          {
            externalSymbol: "BTC/USD",
            baseAsset: "BTC",
            quoteAsset: "USD",
            enabled: true,
          },
        ],
        access: [{ account: "alpha", venueAccount: "" }],
      }),
    ];
    renderTrading();
    const scope = await card();
    const checklist = within(
      scope.getByRole("list", { name: "Setup checklist" }),
    );
    const items = checklist.getAllByRole("listitem");
    expect(items).toHaveLength(4);
    expect(
      within(items[0]).getByText("Not done", { exact: true }),
    ).toBeInTheDocument();
    expect(
      within(items[1]).getByText("Done", { exact: true }),
    ).toBeInTheDocument();
    expect(
      within(items[2]).getByText("Done", { exact: true }),
    ).toBeInTheDocument();
    expect(
      within(items[3]).getByText("Not done", { exact: true }),
    ).toBeInTheDocument();
    for (const label of ["Check connection", "Instruments", "Account access"]) {
      await user.click(
        checklist.getByRole("button", { name: `Go to step: ${label}` }),
      );
      expect(scope.getByRole("region", { name: label })).toHaveFocus();
    }
    await user.click(
      checklist.getByRole("button", { name: "Go to step: Enable" }),
    );
    expect(scope.getByRole("button", { name: "Enable" })).toHaveFocus();
    expect(api.checkTradingConnection).not.toHaveBeenCalled();
    expect(api.setTradingConnectionEnabled).not.toHaveBeenCalled();
    await user.click(
      within(scope.getByRole("region", { name: "Check connection" })).getByRole(
        "button",
        { name: "Check connection" },
      ),
    );
    await waitFor(() =>
      expect(
        within(items[0]).getByText("Done", { exact: true }),
      ).toBeInTheDocument(),
    );
    expect(api.checkTradingConnection).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
    );
    await user.click(scope.getByRole("button", { name: "Enable" }));
    await waitFor(() =>
      expect(
        scope.queryByRole("list", { name: "Setup checklist" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("keeps session checks and pending actions independent between connections", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection(),
      connection({ id: "alpaca-2", label: "Other paper" }),
    ];
    let finishCheck!: (result: {
      venueIdentity: string;
      venueStatus: string;
    }) => void;
    api.checkTradingConnection.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishCheck = resolve;
        }),
    );
    renderTrading();
    const first = await card();
    const second = await card("Other paper");
    await user.click(
      within(first.getByRole("region", { name: "Check connection" })).getByRole(
        "button",
        { name: "Check connection" },
      ),
    );
    expect(
      first.getByRole("button", { name: "Check connection" }),
    ).toBeDisabled();
    expect(first.getByRole("button", { name: "Edit settings" })).toBeEnabled();
    expect(
      second.getByRole("button", { name: "Check connection" }),
    ).toBeEnabled();
    expect(second.getByRole("button", { name: "Edit settings" })).toBeEnabled();
    await act(async () => {
      finishCheck({ venueIdentity: "first-checked", venueStatus: "ACTIVE" });
    });
    expect(await first.findByRole("status")).toHaveTextContent("first-checked");
    expect(second.queryByRole("status")).not.toBeInTheDocument();
    expect(
      within(first.getByRole("list", { name: "Setup checklist" })).getAllByText(
        "Done",
        { exact: true },
      ),
    ).toHaveLength(1);
    expect(
      within(second.getByRole("list", { name: "Setup checklist" })).queryByText(
        "Done",
        { exact: true },
      ),
    ).not.toBeInTheDocument();
  });

  it("uses provider capabilities for the venue-account column, symbol tools and fee section", async () => {
    const user = userEvent.setup();
    snapshot.providers = [
      {
        ...provider,
        venueAccounts: true,
        searchesSymbols: false,
        verifiesSymbols: false,
        unlinkedFees: false,
      },
    ];
    snapshot.connections = [
      connection({
        access: [{ account: "alpha", venueAccount: "venue-subaccount" }],
      }),
    ];
    renderTrading();
    const scope = await card();
    const table = within(scope.getByRole("table", { name: "Account access" }));
    expect(
      table.getAllByRole("columnheader").map((header) => header.textContent),
    ).toEqual(["Account", "Venue account", "Actions"]);
    expect(
      table.getByText("venue-subaccount", { exact: true }),
    ).toBeInTheDocument();
    expect(
      scope.queryByRole("heading", { name: "Fee account" }),
    ).not.toBeInTheDocument();
    expect(
      scope.queryByRole("textbox", { name: "Search symbol" }),
    ).not.toBeInTheDocument();
    expect(
      scope.queryByRole("button", { name: "Verify symbol" }),
    ).not.toBeInTheDocument();
    const form = within(scope.getByRole("form", { name: "Add account" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "beta");
    await user.type(
      form.getByRole("textbox", { name: "Venue account" }),
      "venue-target",
    );
    await user.click(form.getByRole("button", { name: "Add account" }));
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      account: "beta",
      venueAccount: "venue-target",
    });
  });
});

describe("Add trading connection dialog", () => {
  it("opens from the page header", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: "Add trading connection" }),
    );
    expect(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    ).toBeInTheDocument();
  });

  it("opens from the empty state", async () => {
    const user = userEvent.setup();
    snapshot.connections = [];
    renderTrading();
    await screen.findByText("No trading connections", { exact: true });
    const buttons = screen.getAllByRole("button", {
      name: "Add trading connection",
    });
    expect(buttons).toHaveLength(2);
    await user.click(buttons[1]);
    expect(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    ).toBeInTheDocument();
  });

  it("navigates all steps and shows the exact Alpaca guidance", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: "Add trading connection" }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    );
    expect(
      dialog.getByText("Step 1 of 3", { exact: true }),
    ).toBeInTheDocument();
    expect(
      dialog.getByRole("heading", { name: "Choose venue" }),
    ).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Back" })).toBeDisabled();
    expect(dialog.getByRole("button", { name: "Next" })).toBeDisabled();

    for (const [name, href] of [
      ["Site", "https://alpaca.markets"],
      ["Docs", "https://docs.alpaca.markets/us/docs/paper-trading"],
    ]) {
      const link = dialog.getByRole("link", { name });
      expect(link).toHaveAttribute("href", href);
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
    }

    await user.click(dialog.getByRole("button", { name: "Alpaca" }));
    expect(
      dialog.getByText("Step 2 of 3", { exact: true }),
    ).toBeInTheDocument();
    expect(
      dialog.getByRole("heading", {
        name: "Prepare your Alpaca account",
      }),
    ).toBeInTheDocument();
    for (const text of [
      "Create an Alpaca account.",
      "Open your paper trading account: in the Alpaca dashboard, switch the account selector at the top to Paper Trading. Alpaca gives every account a paper account with simulated funds.",
      "Generate paper API keys: with Paper Trading selected, find the API Keys panel on the dashboard and click Generate New Key. Copy the API key and the secret - Alpaca shows the secret only once. Paper keys work only for the paper account.",
      "Keep the dashboard open - the next step asks for both values.",
    ]) {
      expect(dialog.getByText(text, { exact: true })).toBeInTheDocument();
    }
    for (const [name, href] of [
      ["Sign up at Alpaca", "https://app.alpaca.markets/signup"],
      ["Log in", "https://app.alpaca.markets/account/login"],
      [
        "Open the Alpaca dashboard",
        "https://app.alpaca.markets/dashboard/overview",
      ],
      [
        "About paper trading",
        "https://docs.alpaca.markets/us/docs/paper-trading",
      ],
    ]) {
      const link = dialog.getByRole("link", { name });
      expect(link).toHaveAttribute("href", href);
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
    }

    await user.click(dialog.getByRole("button", { name: "Back" }));
    expect(
      dialog.getByText("Step 1 of 3", { exact: true }),
    ).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Next" })).toBeEnabled();
    await user.click(dialog.getByRole("button", { name: "Next" }));
    expect(
      dialog.getByText("Step 2 of 3", { exact: true }),
    ).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "Next" }));
    expect(
      dialog.getByText("Step 3 of 3", { exact: true }),
    ).toBeInTheDocument();
    expect(
      dialog.getByRole("heading", { name: "Connection details" }),
    ).toBeInTheDocument();
    const mode = within(dialog.getByRole("group", { name: "Mode" }));
    expect(mode.getByRole("radio", { name: "Paper" })).toBeChecked();
    expect(mode.getByRole("radio", { name: "Paper" })).toBeEnabled();
    expect(mode.getByRole("radio", { name: "Live" })).toBeDisabled();
    expect(
      dialog.getByText(
        "Live trading is not available yet: it arrives together with the live connection token.",
      ),
    ).toBeInTheDocument();
  });

  it("shows a provider without instructions or external links", async () => {
    const user = userEvent.setup();
    snapshot.providers.push({
      ...provider,
      type: "other",
      title: "Other venue",
    });
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: "Add trading connection" }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    );
    const providerButton = dialog.getByRole("button", { name: "Other venue" });
    const providerTile = within(providerButton.parentElement!);
    expect(providerTile.queryByRole("link", { name: "Site" })).toBeNull();
    expect(providerTile.queryByRole("link", { name: "Docs" })).toBeNull();
    await user.click(providerButton);
    expect(
      dialog.getByRole("heading", {
        name: "Prepare your Other venue account",
      }),
    ).toBeInTheDocument();
    expect(dialog.queryByRole("list")).not.toBeInTheDocument();
    expect(dialog.queryByRole("link")).not.toBeInTheDocument();
  });

  it("replaces Next with a distinct Create connection button", async () => {
    const user = userEvent.setup();
    renderTrading();
    const dialog = await openConnectionDetails(user);
    await user.type(dialog.getByLabelText("Label"), "Desk paper");
    await user.type(dialog.getByLabelText("API key"), "paper-key");
    await user.type(dialog.getByLabelText("API secret"), "paper-secret");
    await user.click(dialog.getByRole("button", { name: "Back" }));
    const next = dialog.getByRole("button", { name: "Next" });
    await user.click(next);
    expect(dialog.getByRole("button", { name: "Create connection" })).not.toBe(
      next,
    );
    expect(api.createTradingConnection).not.toHaveBeenCalled();
  });

  it.each([
    { sealed: false, warning: true },
    { sealed: true, warning: false },
  ])(
    "keeps key inputs enabled when credentialsSealed is $sealed",
    async ({ sealed, warning }) => {
      const user = userEvent.setup();
      snapshot.credentialsSealed = sealed;
      renderTrading();
      const dialog = await openConnectionDetails(user);
      const warningText = "Keys will be stored unencrypted";
      if (warning) {
        expect(dialog.getByRole("alert")).toHaveTextContent(warningText);
      } else {
        expect(dialog.queryByText(warningText)).not.toBeInTheDocument();
      }
      expect(dialog.getByLabelText("API key")).toBeEnabled();
      expect(dialog.getByLabelText("API secret")).toBeEnabled();
    },
  );

  it("requires the label and both keys before creating", async () => {
    const user = userEvent.setup();
    renderTrading();
    const dialog = await openConnectionDetails(user);
    const create = dialog.getByRole("button", { name: "Create connection" });
    expect(create).toBeDisabled();
    await user.type(dialog.getByLabelText("Label"), "Desk paper");
    expect(create).toBeDisabled();
    await user.type(dialog.getByLabelText("API key"), "paper-key");
    expect(create).toBeDisabled();
    await user.type(dialog.getByLabelText("API secret"), "paper-secret");
    expect(create).toBeEnabled();
  });

  it("creates paper disabled, reloads, and focuses the new card", async () => {
    const user = userEvent.setup();
    const created = connection({
      id: "alpaca-new",
      label: "Desk paper",
      secrets: { apiKey: true, apiSecret: true },
      venueIdentity: "venue-new",
    });
    let finishCreate!: () => void;
    api.createTradingConnection.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishCreate = () => {
            snapshot = {
              ...snapshot,
              connections: [...snapshot.connections, created],
            };
            resolve(created);
          };
        }),
    );
    renderTrading();
    const dialog = await openConnectionDetails(user);
    await user.type(dialog.getByLabelText("Label"), "Desk paper");
    await user.type(dialog.getByLabelText("API key"), "paper-key");
    await user.type(dialog.getByLabelText("API secret"), "paper-secret");
    const callsBeforeCreate = api.fetchTrading.mock.calls.length;
    await user.click(dialog.getByRole("button", { name: "Create connection" }));
    expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
      provider: "alpaca",
      label: "Desk paper",
      mode: "paper",
      credentials: { apiKey: "paper-key", apiSecret: "paper-secret" },
      enabled: false,
    });
    expect(dialog.getByRole("button", { name: "Creating…" })).toBeDisabled();
    expect(dialog.getByLabelText("Label")).toBeDisabled();
    expect(dialog.getByLabelText("API key")).toBeDisabled();
    expect(dialog.getByLabelText("API secret")).toBeDisabled();
    await act(async () => {
      finishCreate();
    });
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Add trading connection" }),
      ).not.toBeInTheDocument(),
    );
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeCreate,
      ),
    );
    const createdCard = await screen.findByRole("region", {
      name: "Desk paper",
    });
    await waitFor(() => expect(createdCard).toHaveFocus());
    expect(window.HTMLElement.prototype.scrollIntoView).toHaveBeenCalled();
  });

  it.each(["Escape", "outside click"])(
    "ignores %s while connection creation is pending",
    async (close) => {
      const user = userEvent.setup();
      let finishCreate!: (result: TradingConnection) => void;
      api.createTradingConnection.mockImplementationOnce(
        () =>
          new Promise<TradingConnection>((resolve) => {
            finishCreate = resolve;
          }),
      );
      renderTrading();
      const dialog = await openConnectionDetails(user);
      const dialogElement = screen.getByRole("dialog", {
        name: "Add trading connection",
      });
      await user.type(dialog.getByLabelText("Label"), "Desk paper");
      await user.type(dialog.getByLabelText("API key"), "paper-key");
      await user.type(dialog.getByLabelText("API secret"), "paper-secret");
      await user.click(
        dialog.getByRole("button", { name: "Create connection" }),
      );
      expect(api.createTradingConnection).toHaveBeenCalledTimes(1);
      if (close === "Escape") await user.keyboard("{Escape}");
      else
        fireEvent.pointerDown(document.body, {
          button: 0,
          pointerType: "mouse",
        });
      expect(
        screen.getByRole("dialog", { name: "Add trading connection" }),
      ).toBe(dialogElement);
      await act(async () => finishCreate(connection()));
      await waitFor(() =>
        expect(
          screen.queryByRole("dialog", { name: "Add trading connection" }),
        ).not.toBeInTheDocument(),
      );
    },
  );

  it.each([
    {
      status: 502,
      code: "upstream",
      message: "Alpaca rejected the API key",
    },
    {
      status: 409,
      code: "conflict",
      message: "This label is already used",
    },
  ] as const)(
    "keeps step 3 and its fields after a $status $code error",
    async ({ status, code, message }) => {
      const user = userEvent.setup();
      api.createTradingConnection.mockRejectedValue(
        new ApiError(message, code, status),
      );
      renderTrading();
      const dialog = await openConnectionDetails(user);
      await user.type(dialog.getByLabelText("Label"), "Desk paper");
      await user.type(dialog.getByLabelText("API key"), "paper-key");
      await user.type(dialog.getByLabelText("API secret"), "paper-secret");
      const callsBeforeCreate = api.fetchTrading.mock.calls.length;
      await user.click(
        dialog.getByRole("button", { name: "Create connection" }),
      );
      expect(await dialog.findByRole("alert")).toHaveTextContent(message);
      expect(
        dialog.getByRole("heading", { name: "Connection details" }),
      ).toBeInTheDocument();
      expect(dialog.getByLabelText("Label")).toHaveValue("Desk paper");
      expect(dialog.getByLabelText("API key")).toHaveValue("paper-key");
      expect(dialog.getByLabelText("API secret")).toHaveValue("paper-secret");
      expect(dialog.getByRole("radio", { name: "Paper" })).toBeChecked();
      expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeCreate);
    },
  );

  it.each(["Cancel", "Close"])(
    "%s clears every field before the dialog is reopened",
    async (closeName) => {
      const user = userEvent.setup();
      renderTrading();
      let dialog = await openConnectionDetails(user);
      await user.type(dialog.getByLabelText("Label"), "Desk paper");
      await user.type(dialog.getByLabelText("API key"), "paper-key");
      await user.type(dialog.getByLabelText("API secret"), "paper-secret");
      await user.click(dialog.getByRole("button", { name: closeName }));
      await waitFor(() =>
        expect(
          screen.queryByRole("dialog", { name: "Add trading connection" }),
        ).not.toBeInTheDocument(),
      );
      dialog = await openConnectionDetails(user);
      expect(dialog.getByLabelText("Label")).toHaveValue("");
      expect(dialog.getByLabelText("API key")).toHaveValue("");
      expect(dialog.getByLabelText("API secret")).toHaveValue("");
    },
  );
});
