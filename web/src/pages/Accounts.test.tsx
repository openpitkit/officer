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
import { renderWithApi as render } from "@/test/apiClient";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  Account,
  Asset,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  Group,
  TradingSnapshot,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { useAccountsPage } from "@/api/useAccounts";
import { useGroupsPage } from "@/api/useGroups";
import { useTrading } from "@/api/useTrading";
import { SidebarProvider } from "@/components/SidebarContext";
import { ApiError, AUTOCOMPLETE_SUGGESTION_LIMIT } from "@/framework";
import i18n from "@/i18n";
import { Accounts, CreateAccountDialog } from "@/pages/Accounts";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useAccounts", async () => ({
  ...(await vi.importActual<typeof import("@/api/useAccounts")>(
    "@/api/useAccounts",
  )),
  useAccountsPage: vi.fn(),
}));
vi.mock("@/api/useGroups", () => ({ useGroupsPage: vi.fn() }));
vi.mock("@/api/useTrading", () => ({ useTrading: vi.fn() }));
// SensitiveDataBanner polls GET /credential-storage.
vi.mock("@/api/useCredentialStorage", () => ({
  useCredentialStorage: () => ({
    load: {
      state: "ready",
      data: {
        credentialsSealed: true,
        plaintextTradingKeys: false,
        plaintextMarketDataKeys: false,
      },
      error: null,
    },
    reload: () => {},
  }),
}));
// The venue select is the library widget, tested on its own.
vi.mock("@/framework", async () => {
  const actual =
    await vi.importActual<typeof import("@/framework")>("@/framework");
  const { VenueFilterStandIn } = await import("@/test/venueFilterStandIn");
  return { ...actual, VenueFilter: VenueFilterStandIn };
});
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
vi.mock("@/components/TableControls", async () => {
  const actual = await vi.importActual<
    typeof import("@/components/TableControls")
  >("@/components/TableControls");
  const dialogs = await vi.importActual<
    typeof import("@/components/BusinessCsvDialogs")
  >("@/components/BusinessCsvDialogs");
  return {
    ...actual,
    CsvTransferMenu: ({
      exports,
    }: {
      exports?: {
        entity: BusinessCsvEntity;
        filters?: BusinessCsvExportFilters;
        label: string;
      }[];
    }) => (
      <>
        {exports?.map((item) => (
          <dialogs.BusinessCsvExportDialog
            key={`export-${item.entity}-${item.label}`}
            entity={item.entity}
            filters={item.filters}
            trigger={(open) => (
              <button type="button" onClick={open}>
                {item.label}
              </button>
            )}
          />
        ))}
      </>
    ),
  };
});

const createAccountMock = vi.fn();
const exportBusinessCsvMock = vi.fn();
const setAccountGroupMock = vi.fn();
const setAccountCurrencyMock = vi.fn();
const createGroupMock = vi.fn();
const setGroupCurrencyMock = vi.fn();
const setDefaultGroupCurrencyMock = vi.fn();
const fetchAuditMock = vi.fn();
const fetchAssetsMock = vi.fn();
const fetchAccountsMock = vi.fn();
const fetchGroupsMock = vi.fn();
const blockAccountMock = vi.fn();
const unblockAccountMock = vi.fn();
const unblockGroupMock = vi.fn();
const useTradingMock = vi.mocked(useTrading);
const deleteAccountMock = vi.fn();
const deleteGroupMock = vi.fn();
const grantTradingAccessMock = vi.fn();
const revokeTradingAccessMock = vi.fn();
const fetchAccountStateMock = vi.fn();
const tradingSnapshot: TradingSnapshot = {
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
      diagnostics: [],
    },
  ],
};
const tradingWarning =
  "Trading connections exist. Deleting this account deletes all its data by cascade, including fees booked from trading connections. After Officer restarts, those venue fees may be booked again to the connection's current fee account.";
const useAccountsMock = vi.mocked(useAccountsPage);
const useGroupsMock = vi.mocked(useGroupsPage);

function accountCodeWasRequested(code: string): boolean {
  return useAccountsMock.mock.calls.some(([filters]) => filters?.code === code);
}

function groupCodeWasRequested(code: string): boolean {
  return useGroupsMock.mock.calls.some(([filters]) => filters?.code === code);
}

function lastAccountFilters() {
  return useAccountsMock.mock.calls[useAccountsMock.mock.calls.length - 1]?.[0];
}

function lastGroupFilters() {
  return useGroupsMock.mock.calls[useGroupsMock.mock.calls.length - 1]?.[0];
}

function ready<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

function readyPage<T>(
  items: T[],
): PollingResult<{ items: T[]; total: number }> {
  return ready({ items, total: items.length });
}

/** An account fixture without the derived effective-block fields. */
type AccountFixture = Omit<
  Account,
  | "blockSource"
  | "accountBlocked"
  | "accountBlockReason"
  | "groupBlocked"
  | "groupBlockReason"
>;

// Fixtures declare the account's own block; the effective state mirrors it,
// which is an account whose group is not blocked. A test that exercises a
// group block spells the group fields out instead.
function accountFixture(account: AccountFixture): Account {
  return {
    ...account,
    blockSource: account.blocked ? "account" : "none",
    accountBlocked: account.blocked,
    accountBlockReason: account.blockReason,
    groupBlocked: false,
    groupBlockReason: "",
  };
}

const accounts: Account[] = [
  {
    code: "desk-default",
    title: "Desk default",
    blocked: false,
    blockReason: "",
    group: "",
    notes: "",
    pnlHaltReason: "",
  },
  {
    code: "desk-alpha",
    title: "Desk alpha",
    blocked: false,
    blockReason: "",
    group: "equity-desks",
    notes: "",
    pnlHaltReason: "",
  },
].map(accountFixture);

const groups: Group[] = [
  {
    code: "",
    title: "",
    blocked: false,
    blockReason: "",
    notes: "",
    accountCount: 1,
    positionCount: 0,
  },
  {
    code: "equity-desks",
    title: "Equity desks",
    blocked: false,
    blockReason: "",
    notes: "",
    accountCount: 1,
    positionCount: 0,
  },
];

function renderDialog(onCreated = vi.fn()) {
  render(
    <I18nextProvider i18n={i18n}>
      <CreateAccountDialog
        groupSuggestions={["equity-desks"]}
        onCreated={onCreated}
      />
    </I18nextProvider>,
    {
      api: {
        createAccount: createAccountMock,
        setAccountGroup: setAccountGroupMock,
      },
    },
  );
  return onCreated;
}

function renderAccounts(initialEntry = "/accounts") {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-test-density"
          tradeStyleStorageKey="pit-test-style"
        >
          <MemoryRouter initialEntries={[initialEntry]}>
            <SidebarProvider>
              <Accounts />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        createAccount: createAccountMock,
        exportBusinessCsv: exportBusinessCsvMock,
        setAccountGroup: setAccountGroupMock,
        setAccountCurrency: setAccountCurrencyMock,
        createGroup: createGroupMock,
        setGroupCurrency: setGroupCurrencyMock,
        setDefaultGroupCurrency: setDefaultGroupCurrencyMock,
        fetchAudit: fetchAuditMock,
        fetchAssets: fetchAssetsMock,
        fetchAccounts: fetchAccountsMock,
        fetchGroups: fetchGroupsMock,
        blockAccount: blockAccountMock,
        unblockAccount: unblockAccountMock,
        unblockGroup: unblockGroupMock,
        deleteAccount: deleteAccountMock,
        deleteGroup: deleteGroupMock,
        grantTradingAccess: grantTradingAccessMock,
        revokeTradingAccess: revokeTradingAccessMock,
        fetchAccountState: fetchAccountStateMock,
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  useTradingMock.mockReturnValue(
    ready({ ...tradingSnapshot, connections: [] }),
  );
  // Pin the locale so assertions match the English catalog regardless of the
  // detector's navigator guess under jsdom.
  await i18n.changeLanguage("en");
  useAccountsMock.mockReturnValue(readyPage(accounts));
  useGroupsMock.mockReturnValue(readyPage(groups));
  exportBusinessCsvMock.mockResolvedValue({
    blob: new Blob(["csv"]),
    filename: "business.csv",
  });
  setAccountCurrencyMock.mockResolvedValue({
    code: "desk-alpha",
    title: "Desk alpha",
    blocked: false,
    blockReason: "",
    group: "equity-desks",
    currency: "EUR",
    effectiveCurrency: "EUR",
    currencyOrigin: "account",
    currencyCascade: { account: "EUR", group: "", default: "" },
    notes: "",
    pnlHaltReason: "",
  });
  setGroupCurrencyMock.mockResolvedValue({
    code: "equity-desks",
    title: "Equity desks",
    blocked: false,
    blockReason: "",
    notes: "",
    currency: "EUR",
    accountCount: 1,
    positionCount: 0,
  });
  setDefaultGroupCurrencyMock.mockResolvedValue({
    code: "",
    title: "",
    blocked: false,
    blockReason: "",
    notes: "",
    currency: "EUR",
    accountCount: 1,
    positionCount: 0,
  });
  fetchAuditMock.mockResolvedValue([]);
  fetchAssetsMock.mockResolvedValue([]);
  fetchAccountsMock.mockResolvedValue(accounts);
  fetchGroupsMock.mockResolvedValue(groups);
  fetchAccountStateMock.mockImplementation(async (code: string) => {
    const account = accounts.find((item) => item.code === code);
    if (account === undefined) {
      throw new ApiError("Account not found", "not_found", 404);
    }
    return { account, limits: [] };
  });
  blockAccountMock.mockResolvedValue({
    code: "desk-alpha",
    title: "Desk alpha",
    blocked: true,
    blockReason: "Desk freeze",
    group: "equity-desks",
    notes: "",
  });
  unblockAccountMock.mockResolvedValue({
    code: "algo-infinite-loop",
    title: "",
    blocked: false,
    blockReason: "",
    group: "",
    notes: "",
  });
  unblockGroupMock.mockResolvedValue({
    code: "equity-desks",
    title: "Equity desks",
    blocked: false,
    blockReason: "",
    notes: "",
  });
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: vi.fn(() => "blob:business-csv"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    value: vi.fn(),
  });
  HTMLAnchorElement.prototype.click = () => {};
});

afterEach(() => {
  vi.useRealTimers();
});

describe("DeleteAccountConfirm", () => {
  it("warns about trading fees even when credentials are sealed", async () => {
    useTradingMock.mockReturnValue(ready(tradingSnapshot));
    const user = userEvent.setup();
    renderAccounts();
    await user.click(screen.getByRole("button", { name: "Delete desk-alpha" }));
    const dialog = screen.getByRole("alertdialog", { name: "Delete account?" });
    expect(await within(dialog).findByText(tradingWarning)).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Delete" }),
    ).toBeEnabled();
  });

  it("does not warn when there are no trading connections", async () => {
    const user = userEvent.setup();
    renderAccounts();
    await user.click(screen.getByRole("button", { name: "Delete desk-alpha" }));
    expect(
      within(screen.getByRole("alertdialog")).queryByText(tradingWarning),
    ).not.toBeInTheDocument();
  });

  it.each([
    {
      state: "loading",
      load: { state: "loading", data: null, error: null },
      notice: "Trading connections are still being checked.",
      alert: false,
    },
    {
      state: "error",
      load: { state: "error", data: null, error: "trading unavailable" },
      notice: "Trading connections could not be checked: trading unavailable",
      alert: true,
    },
  ] as const)(
    "says trading connections are unknown while trading is $state",
    async ({ load, notice, alert }) => {
      useTradingMock.mockReturnValue({ load, reload: vi.fn() });
      const user = userEvent.setup();
      renderAccounts();
      await user.click(
        screen.getByRole("button", { name: "Delete desk-alpha" }),
      );
      const dialog = screen.getByRole("alertdialog");
      expect(within(dialog).getByText(notice)).toBeInTheDocument();
      if (alert) {
        expect(within(dialog).getByRole("alert")).toHaveTextContent(notice);
      } else {
        expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
      }
      // Unknown connections are never shown as no connections.
      expect(
        within(dialog).queryByText(tradingWarning),
      ).not.toBeInTheDocument();
      expect(
        within(dialog).getByRole("button", { name: "Delete" }),
      ).toBeEnabled();
    },
  );

  it("keeps the warning of a stale snapshot, says it is stale and reloads it", async () => {
    const trading: PollingResult<TradingSnapshot> = {
      load: { state: "ready", data: tradingSnapshot, error: "poll failed" },
      reload: vi.fn(),
    };
    useTradingMock.mockReturnValue(trading);
    const user = userEvent.setup();
    renderAccounts();
    await user.click(screen.getByRole("button", { name: "Delete desk-alpha" }));
    const dialog = screen.getByRole("alertdialog", { name: "Delete account?" });
    const stale = within(dialog).getByRole("status", { name: "Stale data" });
    expect(stale).toHaveTextContent("poll failed");
    expect(within(dialog).getByText(tradingWarning)).toBeInTheDocument();
    await user.click(within(stale).getByRole("button", { name: "Retry" }));
    expect(trading.reload).toHaveBeenCalledTimes(1);
    expect(
      within(dialog).getByRole("button", { name: "Delete" }),
    ).toBeEnabled();
  });

  it("labels trading access dependents after a refused delete", async () => {
    const user = userEvent.setup();
    const error = new ApiError(
      "account has dependents",
      "has_dependents",
      409,
      [{ kind: "trading_access", count: 2 }],
    );
    deleteAccountMock.mockRejectedValue(error);
    renderAccounts();
    await user.click(screen.getByRole("button", { name: "Delete desk-alpha" }));
    const dialog = screen.getByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    const label = await within(dialog).findByText("Trading connection access");
    expect(label.closest("li")).toHaveTextContent("2");
    expect(deleteAccountMock).toHaveBeenCalledWith("desk-alpha", false);
    expect(
      within(dialog).getByRole("button", { name: "Force delete" }),
    ).toBeEnabled();
  });
});

// Alpaca books unlinked fees and has no fee account; the mock venues do not.
// desk-alpha holds its own Alpaca grant; equity-desks, its group, holds the
// first mock venue.
const accessSnapshot: TradingSnapshot = {
  providers: [
    {
      type: "alpaca",
      title: "Alpaca",
      venueAccounts: true,
      verifiesSymbols: true,
      searchesSymbols: true,
      unlinkedFees: true,
    },
    {
      type: "mock",
      title: "Mock",
      venueAccounts: false,
      verifiesSymbols: false,
      searchesSymbols: false,
      unlinkedFees: false,
    },
  ],
  connections: [
    {
      ...tradingSnapshot.connections[0],
      access: [
        {
          account: "desk-alpha",
          accountGroup: "",
          venueAccount: "sub-1",
          group: "equity-desks",
        },
      ],
    },
    {
      ...tradingSnapshot.connections[0],
      id: "mock-1",
      provider: "mock",
      label: "Mock primary",
      access: [
        {
          account: "",
          accountGroup: "equity-desks",
          venueAccount: "",
          group: "",
        },
      ],
    },
    {
      ...tradingSnapshot.connections[0],
      id: "mock-2",
      provider: "mock",
      label: "Mock backup",
      access: [],
    },
  ],
};

const VENUE_ACCESS = "Trading connection access";

/** The list row holding the given delete button, e.g. "Delete desk-alpha". */
function listRow(deleteName: string) {
  return within(
    screen.getByRole("button", { name: deleteName }).closest("tr")!,
  );
}

/** The dialog row of the connection with the given label. */
function accessRow(dialog: HTMLElement, label: string) {
  return within(within(dialog).getByText(label).closest("tr")!);
}

async function openAccess(
  user: ReturnType<typeof userEvent.setup>,
  deleteName: string,
  dialogName: string,
) {
  await user.click(
    listRow(deleteName).getByRole("button", { name: VENUE_ACCESS }),
  );
  // Let an account's first membership poll land.
  await act(async () => {});
  return screen.getByRole("dialog", { name: dialogName });
}

/** Run the dialog's next membership poll; the test runs on fake timers. */
async function nextMembershipPoll() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
}

describe("Trading connection access", () => {
  it("sits before delete on every account row", () => {
    renderAccounts();
    for (const code of ["desk-default", "desk-alpha"]) {
      const remove = screen.getByRole("button", { name: `Delete ${code}` });
      expect(remove.previousElementSibling).toHaveAccessibleName(VENUE_ACCESS);
    }
  });

  it("sits before delete on real group rows, not on the default group", () => {
    renderAccounts("/accounts?tab=groups");
    const remove = screen.getByRole("button", { name: "Delete equity-desks" });
    expect(remove.previousElementSibling).toHaveAccessibleName(VENUE_ACCESS);
    expect(screen.getAllByRole("button", { name: VENUE_ACCESS })).toHaveLength(
      1,
    );
    const defaultRow = within(screen.getByText("Default group").closest("tr")!);
    expect(
      defaultRow.queryByRole("button", { name: VENUE_ACCESS }),
    ).not.toBeInTheDocument();
  });

  it("shows an account its own access and its group's", async () => {
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    const user = userEvent.setup();
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    const alpaca = accessRow(dialog, "Alpaca paper");
    expect(alpaca.getByText("Own grant")).toBeInTheDocument();
    expect(alpaca.getByText("Venue account: sub-1")).toBeInTheDocument();
    expect(alpaca.getByText("Paper")).toBeInTheDocument();
    // Revoking would drop the venue account; it is changed on its page.
    expect(alpaca.queryByRole("button")).not.toBeInTheDocument();
    expect(
      alpaca.getByRole("link", { name: "Open trading connections" }),
    ).toHaveAttribute("href", "/trading-connections");
    expect(
      accessRow(dialog, "Mock primary").getByText("Through group equity-desks"),
    ).toBeInTheDocument();
    expect(
      accessRow(dialog, "Mock backup").getByText("No access"),
    ).toBeInTheDocument();
  });

  it("shows a group its grants and why Alpaca refuses it", async () => {
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    const user = userEvent.setup();
    renderAccounts("/accounts?tab=groups");
    const dialog = await openAccess(
      user,
      "Delete equity-desks",
      "Trading connection access - group equity-desks",
    );
    const alpaca = accessRow(dialog, "Alpaca paper");
    expect(alpaca.getByText("No access")).toBeInTheDocument();
    expect(alpaca.getByRole("button", { name: "Grant access" })).toBeDisabled();
    expect(
      alpaca.getByText(
        "Set a fee account first. A group can hold several accounts, and Alpaca charges fees that are not tied to orders; Officer needs one account to book them on.",
      ),
    ).toBeInTheDocument();
    expect(
      alpaca.getByRole("link", { name: "Set fee account" }),
    ).toHaveAttribute("href", "/trading-connections");
    expect(
      accessRow(dialog, "Mock primary").getByText("Granted"),
    ).toBeInTheDocument();
    expect(
      accessRow(dialog, "Mock backup").getByRole("button", {
        name: "Grant access",
      }),
    ).toBeEnabled();
  });

  it("grants and revokes an account's own access and reloads", async () => {
    // An own grant without a venue account is revocable from the dialog.
    const trading = ready({
      ...accessSnapshot,
      connections: accessSnapshot.connections.map((connection) => ({
        ...connection,
        access: connection.access.map((grant) => ({
          ...grant,
          venueAccount: "",
        })),
      })),
    });
    useTradingMock.mockReturnValue(trading);
    grantTradingAccessMock.mockResolvedValue(accessSnapshot);
    revokeTradingAccessMock.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    await user.click(
      accessRow(dialog, "Mock backup").getByRole("button", {
        name: "Grant access",
      }),
    );
    expect(grantTradingAccessMock).toHaveBeenCalledExactlyOnceWith("mock-2", {
      account: "desk-alpha",
    });
    await waitFor(() => expect(trading.reload).toHaveBeenCalledTimes(1));
    const revoke = () =>
      user.click(
        accessRow(dialog, "Alpaca paper").getByRole("button", {
          name: "Revoke access",
        }),
      );
    // A revoke waits for its confirmation; cancelling it revokes nothing.
    await revoke();
    let confirm = screen.getByRole("alertdialog", {
      name: "Revoke account access?",
    });
    expect(
      within(confirm).getByText(
        "Revoke access of account desk-alpha to Alpaca paper?",
      ),
    ).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(revokeTradingAccessMock).not.toHaveBeenCalled();
    await revoke();
    confirm = screen.getByRole("alertdialog", {
      name: "Revoke account access?",
    });
    await user.click(
      within(confirm).getByRole("button", { name: "Revoke access" }),
    );
    expect(revokeTradingAccessMock).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { account: "desk-alpha" },
    );
    await waitFor(() => expect(trading.reload).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(
      screen.getByRole("dialog", {
        name: "Trading connection access - desk-alpha",
      }),
    ).toBeInTheDocument();
  });

  it("grants and revokes a group's access and reloads", async () => {
    const trading = ready(accessSnapshot);
    useTradingMock.mockReturnValue(trading);
    grantTradingAccessMock.mockResolvedValue(accessSnapshot);
    revokeTradingAccessMock.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderAccounts("/accounts?tab=groups");
    const dialog = await openAccess(
      user,
      "Delete equity-desks",
      "Trading connection access - group equity-desks",
    );
    await user.click(
      accessRow(dialog, "Mock backup").getByRole("button", {
        name: "Grant access",
      }),
    );
    expect(grantTradingAccessMock).toHaveBeenCalledExactlyOnceWith("mock-2", {
      accountGroup: "equity-desks",
    });
    await waitFor(() => expect(trading.reload).toHaveBeenCalledTimes(1));
    await user.click(
      accessRow(dialog, "Mock primary").getByRole("button", {
        name: "Revoke access",
      }),
    );
    const confirm = screen.getByRole("alertdialog", {
      name: "Revoke group access?",
    });
    expect(
      within(confirm).getByText(
        "Revoke access of group equity-desks to Mock primary? Accounts in the group keep access only if it is granted to them directly.",
      ),
    ).toBeInTheDocument();
    expect(revokeTradingAccessMock).not.toHaveBeenCalled();
    await user.click(
      within(confirm).getByRole("button", { name: "Revoke access" }),
    );
    expect(revokeTradingAccessMock).toHaveBeenCalledExactlyOnceWith("mock-1", {
      accountGroup: "equity-desks",
    });
    await waitFor(() => expect(trading.reload).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
  });

  it("drops a revoke confirmation once a poll binds the grant to a venue account", async () => {
    // desk-alpha's own grant on Alpaca paper, bound to sub-1 or not.
    const bound = accessSnapshot;
    const unbound: TradingSnapshot = {
      ...accessSnapshot,
      connections: accessSnapshot.connections.map((connection) => ({
        ...connection,
        access: connection.access.map((grant) => ({
          ...grant,
          venueAccount: "",
        })),
      })),
    };
    // The snapshot as polled by every reader: a later poll may change the
    // grant.
    const readers = new Set<(next: PollingResult<TradingSnapshot>) => void>();
    useTradingMock.mockImplementation(function useLiveTrading() {
      const [result, setResult] = useState(() => ready(unbound));
      readers.add(setResult);
      return result;
    });
    const publish = (snapshot: TradingSnapshot) => {
      const next = ready(snapshot);
      for (const read of readers) read(next);
    };
    const user = userEvent.setup();
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    await user.click(
      accessRow(dialog, "Alpaca paper").getByRole("button", {
        name: "Revoke access",
      }),
    );
    expect(
      screen.getByRole("alertdialog", { name: "Revoke account access?" }),
    ).toBeInTheDocument();
    act(() => publish(bound));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    // Closed for good: a poll that unbinds the grant again does not reopen it.
    act(() => publish(unbound));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(revokeTradingAccessMock).not.toHaveBeenCalled();
  });

  it("keeps a refused revoke in its confirmation without reloading", async () => {
    const trading = ready(accessSnapshot);
    useTradingMock.mockReturnValue(trading);
    revokeTradingAccessMock.mockRejectedValue(
      new ApiError("revoke refused", "forbidden", 403),
    );
    const user = userEvent.setup();
    renderAccounts("/accounts?tab=groups");
    const name = "Trading connection access - group equity-desks";
    const dialog = await openAccess(user, "Delete equity-desks", name);
    await user.click(
      accessRow(dialog, "Mock primary").getByRole("button", {
        name: "Revoke access",
      }),
    );
    const confirm = screen.getByRole("alertdialog", {
      name: "Revoke group access?",
    });
    await user.click(
      within(confirm).getByRole("button", { name: "Revoke access" }),
    );
    expect(await within(confirm).findByRole("alert")).toHaveTextContent(
      "revoke refused",
    );
    expect(trading.reload).not.toHaveBeenCalled();
    // Shown once: the dialog under the confirmation does not repeat it.
    expect(
      within(dialog).queryByText("revoke refused"),
    ).not.toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(
      within(screen.getByRole("dialog", { name })).getByRole("alert"),
    ).toHaveTextContent("revoke refused");
  });

  it("keeps a refused change in the dialog without reloading", async () => {
    const trading = ready(accessSnapshot);
    useTradingMock.mockReturnValue(trading);
    grantTradingAccessMock.mockRejectedValue(
      new ApiError("fee account required", "fee_account_required", 409),
    );
    const user = userEvent.setup();
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    await user.click(
      accessRow(dialog, "Mock backup").getByRole("button", {
        name: "Grant access",
      }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "fee account required",
    );
    expect(trading.reload).not.toHaveBeenCalled();
  });

  it("lists no connections when the trading load failed", async () => {
    useTradingMock.mockReturnValue({
      load: { state: "error", data: null, error: "trading unavailable" },
      reload: vi.fn(),
    });
    const user = userEvent.setup();
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "trading unavailable",
    );
    expect(within(dialog).queryByRole("table")).not.toBeInTheDocument();
    expect(
      within(dialog).queryByText("No trading connections"),
    ).not.toBeInTheDocument();
  });

  it("marks only the connection whose provider is missing", async () => {
    useTradingMock.mockReturnValue(
      ready({
        ...accessSnapshot,
        providers: accessSnapshot.providers.filter(
          (provider) => provider.type !== "alpaca",
        ),
      }),
    );
    const user = userEvent.setup();
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    const alpaca = accessRow(dialog, "Alpaca paper");
    expect(alpaca.getByRole("alert")).toHaveTextContent(
      "Provider alpaca is missing from the trading snapshot.",
    );
    expect(alpaca.queryByRole("button")).not.toBeInTheDocument();
    expect(
      accessRow(dialog, "Mock primary").getByText("Through group equity-desks"),
    ).toBeInTheDocument();
    expect(
      accessRow(dialog, "Mock backup").getByRole("button", {
        name: "Grant access",
      }),
    ).toBeEnabled();
  });

  it("keeps the rows of a stale snapshot and says they are stale", async () => {
    const trading: PollingResult<TradingSnapshot> = {
      load: { state: "ready", data: accessSnapshot, error: "poll failed" },
      reload: vi.fn(),
    };
    useTradingMock.mockReturnValue(trading);
    const user = userEvent.setup();
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    const stale = within(dialog).getByRole("status", { name: "Stale data" });
    expect(stale).toHaveTextContent(
      "Refresh failed. Showing the last received data.",
    );
    expect(stale).toHaveTextContent("poll failed");
    expect(
      accessRow(dialog, "Mock backup").getByText("No access"),
    ).toBeInTheDocument();
    await user.click(within(stale).getByRole("button", { name: "Retry" }));
    expect(trading.reload).toHaveBeenCalledTimes(1);
  });

  it("follows the account's group while open", async () => {
    // Real time still flows for user events; polls wait for nextMembershipPoll.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    expect(
      accessRow(dialog, "Mock primary").getByText("Through group equity-desks"),
    ).toBeInTheDocument();
    fetchAccountStateMock.mockResolvedValue({
      account: { ...accounts[1], group: "" },
      limits: [],
    });
    await nextMembershipPoll();
    expect(fetchAccountStateMock).toHaveBeenLastCalledWith(
      "desk-alpha",
      expect.any(AbortSignal),
    );
    expect(
      accessRow(dialog, "Mock primary").getByText("No access"),
    ).toBeInTheDocument();
  });

  it("stays open when the account leaves the listed page", async () => {
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    // The list as polled: a later poll pages, sorts or filters it away.
    const feed: { publish?: (items: Account[]) => void } = {};
    useAccountsMock.mockImplementation(function useLiveAccountsPage() {
      const [page, setPage] = useState(() => readyPage(accounts));
      feed.publish = (items) => setPage(readyPage(items));
      return page;
    });
    const user = userEvent.setup();
    renderAccounts();
    const name = "Trading connection access - desk-alpha";
    await openAccess(user, "Delete desk-alpha", name);
    act(() =>
      feed.publish!(
        accounts.filter((account) => account.code !== "desk-alpha"),
      ),
    );
    // The modal hides the list from the accessibility tree; look past it.
    expect(
      screen.queryByRole("button", { name: "Delete desk-alpha", hidden: true }),
    ).not.toBeInTheDocument();
    const dialog = screen.getByRole("dialog", { name });
    expect(
      accessRow(dialog, "Mock primary").getByText("Through group equity-desks"),
    ).toBeInTheDocument();
  });

  it("closes when the account is gone", async () => {
    // Real time still flows for user events; polls wait for nextMembershipPoll.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderAccounts();
    const name = "Trading connection access - desk-alpha";
    await openAccess(user, "Delete desk-alpha", name);
    fetchAccountStateMock.mockRejectedValue(
      new ApiError("Account not found", "not_found", 404),
    );
    await nextMembershipPoll();
    expect(screen.queryByRole("dialog", { name })).not.toBeInTheDocument();
  });

  it("lets a pending grant settle and show its error before closing for a gone account", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const trading = ready(accessSnapshot);
    useTradingMock.mockReturnValue(trading);
    let failGrant: (err: Error) => void = () => {};
    grantTradingAccessMock.mockReturnValue(
      new Promise((_, reject) => {
        failGrant = reject;
      }),
    );
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderAccounts();
    const name = "Trading connection access - desk-alpha";
    const dialog = await openAccess(user, "Delete desk-alpha", name);
    await user.click(
      accessRow(dialog, "Mock backup").getByRole("button", {
        name: "Grant access",
      }),
    );
    fetchAccountStateMock.mockRejectedValue(
      new ApiError("Account not found", "not_found", 404),
    );
    await nextMembershipPoll();
    expect(screen.getByRole("dialog", { name })).toBeInTheDocument();
    await act(async () => {
      failGrant(new ApiError("account desk-alpha is gone", "not_found", 404));
    });
    expect(
      within(screen.getByRole("dialog", { name })).getByRole("alert"),
    ).toHaveTextContent("account desk-alpha is gone");
    expect(trading.reload).not.toHaveBeenCalled();
    // Read, then closed by the operator.
    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog", { name })).not.toBeInTheDocument();
  });

  it("keeps the account's last group and says it is stale when its poll fails", async () => {
    // Real time still flows for user events; polls wait for nextMembershipPoll.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderAccounts();
    const dialog = await openAccess(
      user,
      "Delete desk-alpha",
      "Trading connection access - desk-alpha",
    );
    fetchAccountStateMock.mockRejectedValue(new Error("account poll failed"));
    await nextMembershipPoll();
    const stale = within(dialog).getByRole("status", { name: "Stale data" });
    expect(stale).toHaveTextContent("account poll failed");
    expect(
      accessRow(dialog, "Mock primary").getByText("Through group equity-desks"),
    ).toBeInTheDocument();
    const polls = fetchAccountStateMock.mock.calls.length;
    await user.click(within(stale).getByRole("button", { name: "Retry" }));
    await act(async () => {});
    expect(fetchAccountStateMock).toHaveBeenCalledTimes(polls + 1);
  });

  it.each([
    { state: "loading", alert: null },
    {
      state: "error",
      alert: "This account's group could not be checked: accounts unavailable",
    },
  ] as const)(
    "claims no group access while the account's group is $state",
    async ({ state, alert }) => {
      useTradingMock.mockReturnValue(ready(accessSnapshot));
      if (state === "loading") {
        fetchAccountStateMock.mockReturnValue(new Promise(() => {}));
      } else {
        fetchAccountStateMock.mockRejectedValue(
          new Error("accounts unavailable"),
        );
      }
      const user = userEvent.setup();
      renderAccounts();
      const dialog = await openAccess(
        user,
        "Delete desk-alpha",
        "Trading connection access - desk-alpha",
      );
      expect(
        accessRow(dialog, "Alpaca paper").getByText("Own grant"),
      ).toBeInTheDocument();
      expect(
        within(dialog).queryByText(/through group/i),
      ).not.toBeInTheDocument();
      expect(
        accessRow(dialog, "Mock primary").getByText("No own grant"),
      ).toBeInTheDocument();
      expect(
        accessRow(dialog, "Mock backup").getByRole("button", {
          name: "Grant access",
        }),
      ).toBeDisabled();
      if (alert === null) {
        expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
      } else {
        expect(within(dialog).getByRole("alert")).toHaveTextContent(alert);
      }
    },
  );

  it("closes on the close button and on Escape", async () => {
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    const user = userEvent.setup();
    renderAccounts();
    const name = "Trading connection access - desk-alpha";
    const dialog = await openAccess(user, "Delete desk-alpha", name);
    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name })).not.toBeInTheDocument(),
    );
    await openAccess(user, "Delete desk-alpha", name);
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name })).not.toBeInTheDocument(),
    );
  });
});

describe("Venue access column and filter", () => {
  const venueAccounts: Account[] = [
    { ...accounts[0], venueAccess: false },
    { ...accounts[1], venueAccess: true },
  ];
  const venueGroups: Group[] = [
    { ...groups[0], venueAccess: false },
    { ...groups[1], venueAccess: true },
  ];

  beforeEach(() => {
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    useAccountsMock.mockReturnValue(readyPage(venueAccounts));
    useGroupsMock.mockReturnValue(readyPage(venueGroups));
  });

  async function chooseVenue(
    user: ReturnType<typeof userEvent.setup>,
    option: string,
  ) {
    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /find by fields/i });
    const venue = within(within(dialog).getByRole("group", { name: "Venue" }));
    await user.click(venue.getByRole("button", { name: option }));
    await user.click(
      within(dialog).getByRole("button", { name: /apply advanced filter/i }),
    );
  }

  it("shows account venue access, an access through the group included", () => {
    renderAccounts();
    expect(
      screen.getByRole("columnheader", { name: /venues/i }),
    ).toBeInTheDocument();
    // desk-alpha has access only through equity-desks; the list row says so.
    expect(listRow("Delete desk-alpha").getByText("yes")).toBeInTheDocument();
    expect(listRow("Delete desk-default").getByText("no")).toBeInTheDocument();
  });

  it("shows group venue access, the default group without it", () => {
    renderAccounts("/accounts?tab=groups");
    expect(
      screen.getByRole("columnheader", { name: /venues/i }),
    ).toBeInTheDocument();
    expect(listRow("Delete equity-desks").getByText("yes")).toBeInTheDocument();
    const defaultRow = within(screen.getByText("Default group").closest("tr")!);
    expect(defaultRow.getByText("no")).toBeInTheDocument();
  });

  it.each([
    ["accounts", "/accounts", () => lastAccountFilters()],
    ["groups", "/accounts?tab=groups", () => lastGroupFilters()],
  ] as const)("sorts %s by venue access", async (_list, entry, lastFilters) => {
    const user = userEvent.setup();
    renderAccounts(entry);
    await user.click(screen.getByRole("button", { name: /sort by venues/i }));
    await waitFor(() =>
      expect(lastFilters()).toEqual(
        expect.objectContaining({ sort: "venueAccess", order: "asc" }),
      ),
    );
  });

  it.each([
    ["Any venue", "any"],
    ["Not through a venue", "none"],
    ["Mock · Paper · Mock primary", "connection:mock-1"],
  ])("filters accounts by %s", async (option, venue) => {
    const user = userEvent.setup();
    renderAccounts();
    await chooseVenue(user, option);
    expect(lastAccountFilters()).toEqual(expect.objectContaining({ venue }));
    expect(screen.getByText(`Venue: ${option}`)).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: /clear all filters/i }),
    );
    expect(lastAccountFilters()).not.toHaveProperty("venue");
  });

  it("filters groups by a connection", async () => {
    const user = userEvent.setup();
    renderAccounts("/accounts?tab=groups");
    await chooseVenue(user, "Alpaca · Paper · Alpaca paper");
    expect(lastGroupFilters()).toEqual(
      expect.objectContaining({ venue: "connection:alpaca-1" }),
    );
    expect(
      screen.getByText("Venue: Alpaca · Paper · Alpaca paper"),
    ).toBeInTheDocument();
  });

  it("seeds the venue filter from a deep link and shares it", async () => {
    // Stub the clipboard before rendering; the click uses fireEvent so no
    // user-event clipboard stub shadows it.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    renderAccounts("/accounts?tab=groups&venue=none");
    expect(lastGroupFilters()).toEqual(
      expect.objectContaining({ venue: "none" }),
    );
    fireEvent.click(
      screen.getByRole("button", {
        name: /copy a link to the current filter set/i,
      }),
    );
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const url = new URL(writeText.mock.calls[0][0]);
    expect(url.searchParams.get("tab")).toBe("groups");
    expect(url.searchParams.get("venue")).toBe("none");
  });

  it("hides the column and field without connections but keeps an applied filter and its chip", async () => {
    useTradingMock.mockReturnValue(
      ready({ ...tradingSnapshot, connections: [] }),
    );
    const user = userEvent.setup();
    renderAccounts("/accounts?venue=connection:deleted");
    expect(
      screen.queryByRole("columnheader", { name: /venues/i }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("yes")).not.toBeInTheDocument();
    expect(lastAccountFilters()).toEqual(
      expect.objectContaining({ venue: "connection:deleted" }),
    );
    expect(screen.getByText("Venue: deleted")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /more filters/i }));
    expect(
      within(
        screen.getByRole("dialog", { name: /find by fields/i }),
      ).queryByRole("group", { name: "Venue" }),
    ).not.toBeInTheDocument();
  });

  it("says why the column and field are missing when trading fails to load", async () => {
    useTradingMock.mockReturnValue({
      load: { state: "error", data: null, error: "trading unavailable" },
      reload: vi.fn(),
    });
    const user = userEvent.setup();
    renderAccounts();
    expect(
      screen.getByText(
        "Trading connections could not be checked: trading unavailable",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("columnheader", { name: /venues/i }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /more filters/i }));
    expect(
      within(
        screen.getByRole("dialog", { name: /find by fields/i }),
      ).queryByRole("group", { name: "Venue" }),
    ).not.toBeInTheDocument();
  });

  it.each([
    ["accounts", "/accounts?", () => lastAccountFilters()],
    ["groups", "/accounts?tab=groups&", () => lastGroupFilters()],
  ] as const)(
    "drops a hidden venue access sort from the %s request and share link",
    async (_list, entry, lastFilters) => {
      const writeText = vi.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: { writeText },
      });
      useTradingMock.mockReturnValue(
        ready({ ...tradingSnapshot, connections: [] }),
      );
      renderAccounts(`${entry}sort=venueAccess&order=desc`);
      expect(lastFilters()?.sort).toBeUndefined();
      expect(lastFilters()?.order).toBeUndefined();
      fireEvent.click(
        screen.getByRole("button", {
          name: /copy a link to the current filter set/i,
        }),
      );
      await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
      const url = new URL(writeText.mock.calls[0][0]);
      expect(url.searchParams.get("sort")).toBeNull();
      expect(url.searchParams.get("order")).toBeNull();
    },
  );

  it("reloads both lists once the access dialog closes", async () => {
    const accountsPage = readyPage(venueAccounts);
    const groupsPage = readyPage(venueGroups);
    useAccountsMock.mockReturnValue(accountsPage);
    useGroupsMock.mockReturnValue(groupsPage);
    const user = userEvent.setup();
    renderAccounts();
    const name = "Trading connection access - desk-alpha";
    await openAccess(user, "Delete desk-alpha", name);
    expect(accountsPage.reload).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name })).not.toBeInTheDocument(),
    );
    expect(accountsPage.reload).toHaveBeenCalledTimes(1);
    expect(groupsPage.reload).toHaveBeenCalledTimes(1);
  });
});

describe("DeleteGroupConfirm", () => {
  const groupWarning =
    "This group has access to 1 trading connection. Deleting the group revokes that access; accounts in the group keep access only if it is granted to them directly.";

  it("warns that the group's grants go with it", async () => {
    useTradingMock.mockReturnValue(ready(accessSnapshot));
    const user = userEvent.setup();
    renderAccounts("/accounts?tab=groups");
    await user.click(
      screen.getByRole("button", { name: "Delete equity-desks" }),
    );
    const dialog = screen.getByRole("alertdialog", { name: "Delete group?" });
    expect(within(dialog).getByText(groupWarning)).toBeInTheDocument();
  });

  it("does not warn when the group holds no grants", async () => {
    useTradingMock.mockReturnValue(ready(tradingSnapshot));
    const user = userEvent.setup();
    renderAccounts("/accounts?tab=groups");
    await user.click(
      screen.getByRole("button", { name: "Delete equity-desks" }),
    );
    const dialog = screen.getByRole("alertdialog", { name: "Delete group?" });
    expect(
      within(dialog).queryByText(/This group has access to/),
    ).not.toBeInTheDocument();
  });

  it.each([
    {
      state: "loading",
      load: { state: "loading", data: null, error: null },
      notice: "Trading connections are still being checked.",
      alert: false,
    },
    {
      state: "error",
      load: { state: "error", data: null, error: "trading unavailable" },
      notice: "Trading connections could not be checked: trading unavailable",
      alert: true,
    },
  ] as const)(
    "says the group's access is unknown while trading is $state",
    async ({ load, notice, alert }) => {
      useTradingMock.mockReturnValue({ load, reload: vi.fn() });
      const user = userEvent.setup();
      renderAccounts("/accounts?tab=groups");
      await user.click(
        screen.getByRole("button", { name: "Delete equity-desks" }),
      );
      const dialog = screen.getByRole("alertdialog", { name: "Delete group?" });
      expect(within(dialog).getByText(notice)).toBeInTheDocument();
      if (alert) {
        expect(within(dialog).getByRole("alert")).toHaveTextContent(notice);
      } else {
        expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
      }
      // An unknown grant count is never shown as no grants.
      expect(
        within(dialog).queryByText(/This group has access to/),
      ).not.toBeInTheDocument();
      expect(
        within(dialog).getByRole("button", { name: "Delete" }),
      ).toBeEnabled();
    },
  );

  it("keeps the warning of a stale snapshot, says it is stale and reloads it", async () => {
    const trading: PollingResult<TradingSnapshot> = {
      load: { state: "ready", data: accessSnapshot, error: "poll failed" },
      reload: vi.fn(),
    };
    useTradingMock.mockReturnValue(trading);
    const user = userEvent.setup();
    renderAccounts("/accounts?tab=groups");
    await user.click(
      screen.getByRole("button", { name: "Delete equity-desks" }),
    );
    const dialog = screen.getByRole("alertdialog", { name: "Delete group?" });
    const stale = within(dialog).getByRole("status", { name: "Stale data" });
    expect(stale).toHaveTextContent("poll failed");
    expect(within(dialog).getByText(groupWarning)).toBeInTheDocument();
    await user.click(within(stale).getByRole("button", { name: "Retry" }));
    expect(trading.reload).toHaveBeenCalledTimes(1);
    expect(
      within(dialog).getByRole("button", { name: "Delete" }),
    ).toBeEnabled();
  });

  it("reloads trading access after deleting the group", async () => {
    const trading = ready(accessSnapshot);
    useTradingMock.mockReturnValue(trading);
    deleteGroupMock.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderAccounts("/accounts?tab=groups");
    await user.click(
      screen.getByRole("button", { name: "Delete equity-desks" }),
    );
    const dialog = screen.getByRole("alertdialog", { name: "Delete group?" });
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(deleteGroupMock).toHaveBeenCalledExactlyOnceWith("equity-desks");
    await waitFor(() => expect(trading.reload).toHaveBeenCalledTimes(1));
  });
});

describe("CreateAccountDialog", () => {
  it("creates an account, closes, and notifies on submit", async () => {
    const user = userEvent.setup();
    createAccountMock.mockResolvedValue({
      code: "acc-aapl-desk",
      title: "acc-aapl-desk",
      blocked: false,
      blockReason: "",
      group: "",
      currency: "",
      effectiveCurrency: "",
      currencyOrigin: "",
      currencyCascade: { account: "", group: "", default: "" },
      notes: "",
      pnlHaltReason: "",
    });
    const onCreated = renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    expect(
      screen.getByRole("dialog", { name: /create account/i }),
    ).toBeInTheDocument();

    await user.type(screen.getByLabelText(/account code/i), "acc-aapl-desk");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() => {
      expect(createAccountMock).toHaveBeenCalledTimes(1);
    });
    expect(createAccountMock).toHaveBeenCalledWith("acc-aapl-desk", "", "");
    expect(setAccountGroupMock).not.toHaveBeenCalled();
    expect(onCreated).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("assigns the typed group after creating the account", async () => {
    const user = userEvent.setup();
    createAccountMock.mockResolvedValue({
      code: "acc-spx",
      title: "acc-spx",
      blocked: false,
      blockReason: "",
      group: "equity-desks",
      currency: "",
      effectiveCurrency: "",
      currencyOrigin: "",
      currencyCascade: { account: "", group: "", default: "" },
      notes: "",
      pnlHaltReason: "",
    });
    setAccountGroupMock.mockResolvedValue({
      code: "acc-spx",
      title: "acc-spx",
      blocked: false,
      blockReason: "",
      group: "equity-desks",
      currency: "",
      effectiveCurrency: "",
      currencyOrigin: "",
      currencyCascade: { account: "", group: "", default: "" },
      notes: "",
      pnlHaltReason: "",
    });
    renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    await user.type(screen.getByLabelText(/account code/i), "acc-spx");
    await user.type(screen.getByLabelText(/group/i), "equity-desks");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() => {
      expect(createAccountMock).toHaveBeenCalledWith("acc-spx", "", "");
    });
    expect(setAccountGroupMock).toHaveBeenCalledTimes(1);
    expect(setAccountGroupMock).toHaveBeenCalledWith(
      "acc-spx",
      "equity-desks",
      "reject",
    );
  });

  it("shows an inline validation message and does not submit an invalid id", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    // 65 chars exceeds the 64-char cap that validateAccountID enforces.
    await user.type(screen.getByLabelText(/account code/i), "a".repeat(65));

    expect(
      await screen.findByText(/at most 64 characters/i),
    ).toBeInTheDocument();
    // The submit button is disabled while the id is invalid, so a click is a
    // no-op; assert the client was never called regardless.
    await user.click(screen.getByRole("button", { name: /^create$/i }));
    expect(createAccountMock).not.toHaveBeenCalled();
  });

  it("surfaces a client error and keeps the dialog open", async () => {
    const user = userEvent.setup();
    createAccountMock.mockRejectedValue(
      new ApiError("account already exists", "conflict", 409),
    );
    renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    await user.type(screen.getByLabelText(/account code/i), "acc-dupe");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /account already exists/i,
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});

describe("Accounts business CSV", () => {
  it("exports groups without a group filter", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(
      screen.getByRole("button", { name: /export groups csv/i }),
    );
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(exportBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "account_groups",
        delimiter: "comma",
        filters: undefined,
        zip: false,
      }),
    );
  });

  it("exports accounts for the default no-group bucket", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    const defaultRow = screen.getByText("Default group").closest("tr");
    expect(defaultRow).not.toBeNull();
    await user.click(
      within(defaultRow as HTMLElement).getByRole("button", {
        name: /filter by default group/i,
      }),
    );
    await user.click(
      screen.getByRole("button", { name: /export accounts csv/i }),
    );
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(exportBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "accounts",
        filters: { groupCode: "" },
      }),
    );
  });

  it("resets account pagination and requests accounts with the selected group", async () => {
    const user = userEvent.setup();
    const pagedAccounts: Account[] = [
      ...Array.from({ length: 51 }, (_, index) => ({
        code: `desk-${index.toString().padStart(2, "0")}`,
        title: `Desk ${index.toString().padStart(2, "0")}`,
        blocked: false,
        blockReason: "",
        group: "",
        notes: "",
        pnlHaltReason: "",
      })),
      {
        code: "desk-alpha",
        title: "Desk alpha",
        blocked: false,
        blockReason: "",
        group: "equity-desks",
        notes: "",
        pnlHaltReason: "",
      },
    ].map(accountFixture);
    useAccountsMock.mockImplementation((filters) =>
      readyPage(
        filters?.group === "equity-desks" ? [pagedAccounts[51]] : pagedAccounts,
      ),
    );
    renderAccounts();

    await user.click(screen.getAllByRole("button", { name: /^next$/i })[0]);
    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    const groupRow = screen.getByText("Equity desks").closest("tr");
    expect(groupRow).not.toBeNull();
    await user.click(
      within(groupRow as HTMLElement).getByRole("button", {
        name: /filter by equity-desks/i,
      }),
    );

    expect(screen.getByText("Desk alpha")).toBeInTheDocument();
    expect(useAccountsMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ group: "equity-desks" }),
    );
  });

  it("filters accounts by group through the group-cell filter action", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(
      screen.getByRole("button", { name: /filter by equity-desks/i }),
    );

    await waitFor(() =>
      expect(useAccountsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ group: "equity-desks" }),
      ),
    );
  });

  it("creates a missing group before assigning it to an account", async () => {
    const user = userEvent.setup();
    createGroupMock.mockResolvedValue({
      code: "new-desk",
      title: "",
      notes: "",
      blocked: false,
      blockReason: "",
      accountCount: 1,
      positionCount: 0,
    });
    setAccountGroupMock.mockResolvedValue({
      code: "desk-default",
      title: "Desk default",
      blocked: false,
      blockReason: "",
      group: "new-desk",
      notes: "",
    });
    renderAccounts();

    const row = screen.getByText("Desk default").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /edit group assignment/i,
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: /assign group/i });
    await user.type(within(dialog).getByLabelText(/^group$/i), "new-desk");
    await user.click(within(dialog).getByRole("button", { name: /^assign$/i }));

    await waitFor(() =>
      expect(createGroupMock).toHaveBeenCalledWith("new-desk", "", ""),
    );
    expect(setAccountGroupMock).toHaveBeenCalledWith(
      "desk-default",
      "new-desk",
      "reject",
    );
  });

  it("sets an account currency from the account currency dialog", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "desk-alpha",
            title: "Desk alpha",
            blocked: false,
            blockReason: "",
            group: "equity-desks",
            currency: "USD",
            effectiveCurrency: "USD",
            currencyOrigin: "account",
            currencyCascade: { account: "USD", group: "", default: "" },
            notes: "",
            pnlHaltReason: "",
            positionCount: 0,
          },
        ].map(accountFixture),
      ),
    );
    renderAccounts();

    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /edit account currency/i,
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /edit account currency/i,
    });
    const currency = within(dialog).getByLabelText(/^account currency$/i);
    await user.clear(currency);
    await user.type(currency, "EUR");
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(setAccountCurrencyMock).toHaveBeenCalledWith("desk-alpha", "EUR"),
    );
  });

  it("searches currency assets while editing account currency", async () => {
    const user = userEvent.setup();
    const asset: Asset = { code: "EUR", title: "Euro", assetClass: "currency" };
    fetchAssetsMock.mockResolvedValue([asset]);
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "desk-alpha",
            title: "Desk alpha",
            blocked: false,
            blockReason: "",
            group: "equity-desks",
            currency: "USD",
            effectiveCurrency: "USD",
            currencyOrigin: "account",
            currencyCascade: { account: "USD", group: "", default: "" },
            notes: "",
            pnlHaltReason: "",
            positionCount: 0,
          },
        ].map(accountFixture),
      ),
    );
    renderAccounts();

    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /edit account currency/i,
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /edit account currency/i,
    });
    const currency = within(dialog).getByLabelText(/^account currency$/i);
    await user.clear(currency);
    await user.type(currency, "EU");

    await waitFor(() =>
      expect(fetchAssetsMock).toHaveBeenCalledWith(
        expect.objectContaining({
          code: "EU",
          codeMatch: "starts_with",
          limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
          sort: "code",
        }),
        expect.any(AbortSignal),
      ),
    );
    expect(
      within(dialog).getByRole("option", { name: "EUR" }),
    ).toBeInTheDocument();
  });

  it("reports a currency asset-suggestion lookup failure", async () => {
    const user = userEvent.setup();
    const error = new Error("asset lookup failed");
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => {});
    fetchAssetsMock.mockRejectedValueOnce(error);
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "desk-alpha",
            title: "Desk alpha",
            blocked: false,
            blockReason: "",
            group: "equity-desks",
            currency: "USD",
            effectiveCurrency: "USD",
            currencyOrigin: "account",
            currencyCascade: { account: "USD", group: "", default: "" },
            notes: "",
            pnlHaltReason: "",
            positionCount: 0,
          },
        ].map(accountFixture),
      ),
    );
    renderAccounts();

    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /edit account currency/i,
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /edit account currency/i,
    });
    const currency = within(dialog).getByLabelText(/^account currency$/i);
    await user.clear(currency);
    await user.type(currency, "EU");

    await waitFor(() => expect(consoleError).toHaveBeenCalledWith(error));
    expect(within(dialog).queryByRole("option")).not.toBeInTheDocument();
    expect(
      await within(dialog).findByText("Asset suggestions could not be loaded."),
    ).toBeInTheDocument();

    await user.type(currency, "R");
    await waitFor(() =>
      expect(
        within(dialog).queryByText("Asset suggestions could not be loaded."),
      ).not.toBeInTheDocument(),
    );
    consoleError.mockRestore();
  });

  it("clears an already-set account currency", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "desk-alpha",
            title: "Desk alpha",
            blocked: false,
            blockReason: "",
            group: "equity-desks",
            currency: "USD",
            effectiveCurrency: "USD",
            currencyOrigin: "account",
            currencyCascade: { account: "USD", group: "", default: "" },
            notes: "",
            pnlHaltReason: "",
            positionCount: 0,
          },
        ].map(accountFixture),
      ),
    );
    renderAccounts();

    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /edit account currency/i,
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /edit account currency/i,
    });
    const currency = within(dialog).getByLabelText(/^account currency$/i);
    await user.clear(currency);
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(setAccountCurrencyMock).toHaveBeenCalledWith("desk-alpha", ""),
    );
  });

  it("creates a group with an optional currency", async () => {
    const user = userEvent.setup();
    createGroupMock.mockResolvedValue({
      code: "fx-desks",
      title: "FX desks",
      notes: "",
      blocked: false,
      blockReason: "",
      currency: "EUR",
      accountCount: 0,
      positionCount: 0,
    });
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /new group/i }));
    const dialog = await screen.findByRole("dialog", {
      name: /create group/i,
    });
    await user.type(within(dialog).getByLabelText(/group code/i), "fx-desks");
    await user.type(
      within(dialog).getByLabelText(/display title/i),
      "FX desks",
    );
    await user.type(within(dialog).getByLabelText(/^currency/i), "EUR");
    await user.click(within(dialog).getByRole("button", { name: /^create$/i }));

    await waitFor(() =>
      expect(createGroupMock).toHaveBeenCalledWith(
        "fx-desks",
        "FX desks",
        "",
        "EUR",
      ),
    );
  });

  it("requests a status sort when the status header is toggled", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /sort by status/i }));

    await waitFor(() =>
      expect(useAccountsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort: "status", order: "asc" }),
      ),
    );
  });

  it("renders an account code once when the display title is empty", () => {
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "empty-title",
            title: "",
            blocked: false,
            blockReason: "",
            group: "",
            notes: "",
            pnlHaltReason: "",
          },
        ].map(accountFixture),
      ),
    );

    renderAccounts();

    const row = screen.getByText("empty-title").closest("tr");

    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getAllByText("empty-title")).toHaveLength(
      1,
    );
  });

  it("renders exact account PnL values without a currency suffix", () => {
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "pnl-positive",
            title: "Positive PnL",
            blocked: false,
            blockReason: "",
            group: "",
            effectiveCurrency: "USD",
            notes: "",
            pnl: "123.4500",
            pnlHaltReason: "",
          },
          {
            code: "pnl-negative",
            title: "Negative PnL",
            blocked: false,
            blockReason: "",
            group: "",
            effectiveCurrency: "USD",
            notes: "",
            pnl: "-0.1250",
            pnlHaltReason: "",
          },
          {
            code: "pnl-zero",
            title: "Zero PnL",
            blocked: false,
            blockReason: "",
            group: "",
            effectiveCurrency: "USD",
            notes: "",
            pnl: "0.0000",
            pnlHaltReason: "",
          },
          {
            code: "pnl-empty",
            title: "Empty PnL",
            blocked: false,
            blockReason: "",
            group: "",
            effectiveCurrency: "USD",
            notes: "",
            pnl: "",
            pnlHaltReason: "",
          },
          {
            code: "pnl-halted-known",
            title: "Known halt",
            blocked: false,
            blockReason: "",
            group: "",
            effectiveCurrency: "USD",
            notes: "",
            pnl: "",
            pnlHaltReason: "missing_cost_basis",
          },
          {
            code: "pnl-halted-unknown",
            title: "Unknown halt",
            blocked: false,
            blockReason: "",
            group: "",
            effectiveCurrency: "USD",
            notes: "",
            pnl: "",
            pnlHaltReason: "future_reason",
          },
          {
            code: "pnl-halted-arithmetic-overflow",
            title: "Arithmetic overflow",
            blocked: false,
            blockReason: "",
            group: "",
            effectiveCurrency: "USD",
            notes: "",
            pnl: "",
            pnlHaltReason: "arithmetic_overflow",
          },
        ].map(accountFixture),
      ),
    );
    renderAccounts();

    const row = (title: string): HTMLElement => {
      const result = screen.getByText(title).closest("tr");
      expect(result).not.toBeNull();
      return result as HTMLElement;
    };
    const cell = (title: string): HTMLElement =>
      within(row(title)).getAllByRole("cell")[3];

    expect(cell("Positive PnL")).toHaveTextContent("123.4500");
    expect(cell("Positive PnL")).toHaveClass("text-[var(--pnl-pos)]");
    expect(cell("Positive PnL")).not.toHaveTextContent("USD");
    expect(cell("Negative PnL")).toHaveTextContent("-0.1250");
    expect(cell("Negative PnL")).toHaveClass("text-[var(--pnl-neg)]");
    expect(cell("Zero PnL")).toHaveTextContent("0.0000");
    expect(cell("Zero PnL")).toHaveClass("text-[var(--pnl-flat)]");
    expect(cell("Empty PnL")).toHaveTextContent(/^-$/);
    expect(cell("Empty PnL")).toHaveClass("text-[var(--pnl-flat)]");
    expect(
      within(row("Empty PnL")).queryByRole("note"),
    ).not.toBeInTheDocument();

    const knownWarning = within(row("Known halt")).getByRole("note", {
      name: /position cost basis is unavailable/i,
    });
    expect(knownWarning).toHaveAttribute("tabindex", "0");
    expect(
      within(row("Arithmetic overflow")).getByRole("note", {
        name: /exact arithmetic exceeded the supported numeric range/i,
      }),
    ).toBeInTheDocument();
    expect(
      within(row("Unknown halt")).getByRole("note", {
        name: /stopped by the engine/i,
      }),
    ).toBeInTheDocument();
  });

  it("loads blocked-account audit with the account block action", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "algo-infinite-loop",
            title: "",
            blocked: true,
            blockReason: "Trading halt",
            group: "",
            notes: "",
            pnlHaltReason: "",
          },
        ].map(accountFixture),
      ),
    );
    fetchAuditMock.mockResolvedValue([
      {
        id: "audit-1",
        at: "2026-06-29T00:00:00Z",
        actor: "risk",
        actorTitle: "Risk",
        action: "block",
        account: "algo-infinite-loop",
        accountTitle: "",
        detail: "Trading halt",
        source: "panel",
      },
    ]);

    renderAccounts();
    await user.click(
      screen.getByRole("button", { name: /view block details/i }),
    );

    await waitFor(() =>
      expect(fetchAuditMock).toHaveBeenCalledWith({
        account: "algo-infinite-loop",
        actions: ["block"],
        limit: 1,
      }),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps blocked details open when unblock confirmation is cancelled", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "algo-infinite-loop",
            title: "",
            blocked: true,
            blockReason: "Trading halt",
            group: "",
            notes: "",
            pnlHaltReason: "",
          },
        ].map(accountFixture),
      ),
    );

    renderAccounts();
    await user.click(
      screen.getByRole("button", { name: /view block details/i }),
    );
    const details = await screen.findByRole("dialog", {
      name: /blocked account/i,
    });
    await user.click(
      within(details).getByRole("button", { name: /^unblock$/i }),
    );
    const confirmation = screen.getByRole("alertdialog", {
      name: /unblock account/i,
    });

    await user.click(
      within(confirmation).getByRole("button", { name: /cancel/i }),
    );

    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(
      screen.getByRole("dialog", { name: /blocked account/i }),
    ).toBeInTheDocument();
    expect(unblockAccountMock).not.toHaveBeenCalled();
  });

  it("closes blocked details after a successful unblock", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "algo-infinite-loop",
            title: "",
            blocked: true,
            blockReason: "Trading halt",
            group: "",
            notes: "",
            pnlHaltReason: "",
          },
        ].map(accountFixture),
      ),
    );

    renderAccounts();
    await user.click(
      screen.getByRole("button", { name: /view block details/i }),
    );
    const details = await screen.findByRole("dialog", {
      name: /blocked account/i,
    });
    await user.click(
      within(details).getByRole("button", { name: /^unblock$/i }),
    );
    const confirmation = screen.getByRole("alertdialog", {
      name: /unblock account/i,
    });
    await user.click(
      within(confirmation).getByRole("button", { name: /^unblock$/i }),
    );

    await waitFor(() =>
      expect(unblockAccountMock).toHaveBeenCalledWith(
        "algo-infinite-loop",
        "reject",
      ),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: /blocked account/i }),
      ).not.toBeInTheDocument(),
    );
  });

  // The tiers are independent: an account blocked only through its group holds
  // no block of its own, so the row offers Block - the operator has to be able
  // to hold this one account down past the group's unblock - and offers no
  // account-level unblock, which would have nothing to lift.
  it("offers block on an account blocked only through its group", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage([
        {
          code: "desk-alpha",
          title: "Desk alpha",
          blocked: true,
          blockReason: "Desk halt",
          blockSource: "group",
          accountBlocked: false,
          accountBlockReason: "",
          groupBlocked: true,
          groupBlockReason: "Desk halt",
          group: "equity-desks",
          notes: "",
          pnlHaltReason: "",
        },
      ]),
    );

    renderAccounts();
    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    expect(
      within(row as HTMLElement).getByText(/blocked/i),
    ).toBeInTheDocument();
    expect(
      within(row as HTMLElement).getByRole("button", { name: /^block$/i }),
    ).toBeEnabled();
    expect(
      within(row as HTMLElement).queryByRole("button", {
        name: /^unblock$/i,
      }),
    ).not.toBeInTheDocument();

    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /view block details/i,
      }),
    );
    const details = await screen.findByRole("dialog", {
      name: /blocked account/i,
    });
    expect(
      within(details).getByText(/blocked through group equity-desks/i),
    ).toBeInTheDocument();
    // The explanation stays; the dead button does not.
    expect(
      within(details).getByText(/unblock the group to let this account trade/i),
    ).toBeInTheDocument();
    expect(
      within(details).queryByRole("button", { name: /^unblock$/i }),
    ).not.toBeInTheDocument();
  });

  it("blocks an account that carries only its group's block", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage([
        {
          code: "desk-alpha",
          title: "Desk alpha",
          blocked: true,
          blockReason: "Desk halt",
          blockSource: "group",
          accountBlocked: false,
          accountBlockReason: "",
          groupBlocked: true,
          groupBlockReason: "Desk halt",
          group: "equity-desks",
          notes: "",
          pnlHaltReason: "",
        },
      ]),
    );

    renderAccounts();
    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", { name: /^block$/i }),
    );

    const dialog = await screen.findByRole("dialog", {
      name: /block account/i,
    });
    await user.type(within(dialog).getByLabelText(/^reason$/i), "Desk freeze");
    await user.click(within(dialog).getByRole("button", { name: /^block$/i }));

    await waitFor(() =>
      expect(blockAccountMock).toHaveBeenCalledWith(
        "desk-alpha",
        "Desk freeze",
        "reject",
      ),
    );
  });

  it("offers only block while neither tier is blocked", async () => {
    renderAccounts();
    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    expect(
      within(row as HTMLElement).getByRole("button", { name: /^block$/i }),
    ).toBeEnabled();
    expect(
      within(row as HTMLElement).queryByRole("button", {
        name: /^unblock$/i,
      }),
    ).not.toBeInTheDocument();
    expect(
      within(row as HTMLElement).queryByRole("button", {
        name: /view block details/i,
      }),
    ).not.toBeInTheDocument();
  });

  // The block never touched this account, so its own rows cannot record it;
  // the group's rows are the only truthful source.
  it("resolves a group-blocked account's audit through the group handle", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage([
        {
          code: "desk-alpha",
          title: "Desk alpha",
          blocked: true,
          blockReason: "Desk halt",
          blockSource: "group",
          accountBlocked: false,
          accountBlockReason: "",
          groupBlocked: true,
          groupBlockReason: "Desk halt",
          group: "equity-desks",
          notes: "",
          pnlHaltReason: "",
        },
      ]),
    );

    renderAccounts();
    await user.click(
      screen.getByRole("button", { name: /view block details/i }),
    );
    const details = await screen.findByRole("dialog", {
      name: /blocked account/i,
    });

    await waitFor(() =>
      expect(fetchAuditMock).toHaveBeenCalledWith({
        group: "equity-desks",
        actions: ["block_group"],
        limit: 3,
      }),
    );
    // Neither the account's unrelated rows nor the realm-wide tail may stand
    // in for this block's record.
    expect(fetchAuditMock).not.toHaveBeenCalledWith({
      account: "desk-alpha",
      limit: 3,
    });
    expect(fetchAuditMock).not.toHaveBeenCalledWith(3);
    expect(
      await within(details).findByText(/no audit records found/i),
    ).toBeInTheDocument();
  });

  // Both tiers latched: the account-level unblock is real work, so it stays
  // enabled, but it cannot make the account tradable on its own.
  it("keeps unblock enabled for an account blocked on both tiers", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage([
        {
          code: "desk-alpha",
          title: "Desk alpha",
          blocked: true,
          blockReason: "Account halt",
          blockSource: "account",
          accountBlocked: true,
          accountBlockReason: "Account halt",
          groupBlocked: true,
          groupBlockReason: "Desk halt",
          group: "equity-desks",
          notes: "",
          pnlHaltReason: "",
        },
      ]),
    );

    renderAccounts();
    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    expect(
      within(row as HTMLElement).getByRole("button", { name: /^unblock$/i }),
    ).toBeEnabled();
    expect(
      within(row as HTMLElement).queryByRole("button", { name: /^block$/i }),
    ).not.toBeInTheDocument();

    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /view block details/i,
      }),
    );
    const details = await screen.findByRole("dialog", {
      name: /blocked account/i,
    });
    expect(
      within(details).getByText(/group equity-desks is blocked as well/i),
    ).toBeInTheDocument();
    expect(
      within(details).getByRole("button", { name: /^unblock$/i }),
    ).toBeEnabled();
  });

  it("warns that the group block outlives an account unblock", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage([
        {
          code: "desk-alpha",
          title: "Desk alpha",
          blocked: true,
          blockReason: "Account halt",
          blockSource: "account",
          accountBlocked: true,
          accountBlockReason: "Account halt",
          groupBlocked: true,
          groupBlockReason: "Desk halt",
          group: "equity-desks",
          notes: "",
          pnlHaltReason: "",
        },
      ]),
    );

    renderAccounts();
    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", { name: /^unblock$/i }),
    );

    const confirmation = await screen.findByRole("alertdialog", {
      name: /unblock account/i,
    });
    expect(confirmation).toHaveTextContent(
      "Group equity-desks is blocked. This account will stay blocked until the group is unblocked.",
    );
  });

  it("switches to filtered accounts through a group row filter action", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    const row = screen.getByText("Equity desks").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /filter by equity-desks/i,
      }),
    );

    expect(screen.getByText("Desk alpha")).toBeInTheDocument();
    expect(useAccountsMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ group: "equity-desks" }),
    );
  });

  it("exposes mask help on the account code search field", async () => {
    const user = userEvent.setup();
    renderAccounts();

    const codeInput = screen.getByLabelText(/account search/i);

    expect(codeInput).toHaveAttribute(
      "title",
      expect.stringContaining("desk-*"),
    );
    expect(screen.queryByText(/wildcard/i)).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /more filters/i }));

    expect(screen.getByText(/wildcard/i)).toBeInTheDocument();
  });

  it("debounces account code filters without dropping focus", async () => {
    vi.useFakeTimers();
    renderAccounts();
    const codeInput = screen.getByLabelText(/account search/i);

    codeInput.focus();
    fireEvent.change(codeInput, {
      target: { value: "desk-alpha" },
    });

    expect(accountCodeWasRequested("desk-alpha")).toBe(false);

    act(() => {
      vi.advanceTimersByTime(299);
    });
    expect(accountCodeWasRequested("desk-alpha")).toBe(false);

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(accountCodeWasRequested("desk-alpha")).toBe(true);
    expect(document.activeElement).toBe(codeInput);
  });

  it("suggests known accounts while typing the account search filter", async () => {
    vi.useFakeTimers();
    fetchAccountsMock.mockResolvedValue([accounts[1]]);
    renderAccounts();

    const codeInput = screen.getByLabelText(/account search/i);
    fireEvent.change(codeInput, { target: { value: "desk-a" } });

    await act(async () => {
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    expect(fetchAccountsMock).toHaveBeenCalledWith(
      expect.objectContaining({
        code: "desk-a",
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      }),
      expect.any(AbortSignal),
    );
    expect(
      screen.getByRole("option", { name: "desk-alpha" }),
    ).toBeInTheDocument();
  });

  it("applies account advanced search only on apply and keeps dialog input", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /find by fields/i });

    expect(within(dialog).queryByLabelText(/^notes$/i)).not.toBeInTheDocument();
    await user.type(within(dialog).getAllByPlaceholderText("Value")[0], "1");
    await user.type(
      within(dialog).getByLabelText(/^block reason$/i, { selector: "input" }),
      "halt",
    );
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ blockReason: "halt" }),
    );
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ positionCountMin: "1" }),
    );

    await user.click(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    );
    expect(lastAccountFilters()).toEqual(
      expect.objectContaining({
        blockReason: "halt",
        positionCountMode: "eq",
        positionCountMin: "1",
      }),
    );
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ notes: expect.any(String) }),
    );
    expect(screen.getByText(/Active filters/i)).toBeInTheDocument();
    expect(screen.getByText(/Position count: Equals 1/i)).toBeInTheDocument();
    expect(
      screen.getByText(/Block reason: Contains halt/i),
    ).toBeInTheDocument();
    expect(
      screen.getAllByRole("button", { name: /Remove advanced filter/i }).length,
    ).toBeGreaterThan(0);
    await user.click(
      screen.getByRole("button", { name: /clear all filters/i }),
    );
    expect(screen.queryByText(/Active filters/i)).not.toBeInTheDocument();
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ blockReason: expect.any(String) }),
    );
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ positionCountMin: expect.any(String) }),
    );

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const reopened = screen.getByRole("dialog", { name: /find by fields/i });
    expect(
      within(reopened).queryByLabelText(/^notes$/i),
    ).not.toBeInTheDocument();
    expect(
      within(reopened).getByLabelText(/^block reason$/i, { selector: "input" }),
    ).toHaveValue("");
    expect(within(reopened).getAllByPlaceholderText("Value")[0]).toHaveValue(
      "",
    );
  });

  it("disables account advanced search while a numeric value is invalid", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /find by fields/i });
    const positionCount = within(dialog).getAllByPlaceholderText("Value")[0];
    await user.type(positionCount, "word");

    expect(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    ).toBeDisabled();

    expect(
      screen.getByRole("dialog", { name: /find by fields/i }),
    ).toBeInTheDocument();
    expect(positionCount).toBeInvalid();
    expect(positionCount).toHaveProperty(
      "validationMessage",
      "Enter a valid number.",
    );
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({
        positionCountMode: "eq",
        positionCountMin: expect.any(String),
      }),
    );
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();

    await user.clear(positionCount);
    await user.type(positionCount, "1.5");
    expect(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    ).toBeDisabled();

    await user.clear(positionCount);
    await user.type(positionCount, "-1");
    expect(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    ).toBeDisabled();
  });

  it("maps less-than count filters to the visible value", async () => {
    renderAccounts(
      "/accounts?positionCountMode=lt&positionCountMin=1&positionCountMax=9",
    );

    await waitFor(() =>
      expect(lastAccountFilters()).toEqual(
        expect.objectContaining({
          positionCountMode: "lt",
          positionCountMax: "1",
        }),
      ),
    );
  });

  it("debounces group code filters", async () => {
    vi.useFakeTimers();
    renderAccounts();

    fireEvent.click(screen.getByRole("button", { name: /^groups$/i }));
    fireEvent.change(screen.getByLabelText(/group search/i), {
      target: { value: "equity" },
    });

    expect(groupCodeWasRequested("equity")).toBe(false);

    act(() => {
      vi.advanceTimersByTime(299);
    });
    expect(groupCodeWasRequested("equity")).toBe(false);

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(groupCodeWasRequested("equity")).toBe(true);
  });

  it("suggests known groups while typing group filters", async () => {
    vi.useFakeTimers();
    fetchGroupsMock.mockResolvedValue([groups[1]]);
    renderAccounts();

    const accountGroupInput = screen.getByLabelText(/group search/i);
    fireEvent.change(accountGroupInput, {
      target: { value: "equity" },
    });

    await act(async () => {
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    expect(fetchGroupsMock).toHaveBeenCalledWith(
      expect.objectContaining({
        code: "equity",
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      }),
      expect.any(AbortSignal),
    );
    expect(
      screen.getByRole("option", { name: "equity-desks" }),
    ).toBeInTheDocument();
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ group: expect.any(String) }),
    );

    fireEvent.change(accountGroupInput, {
      target: { value: "equity-desks" },
    });
    expect(lastAccountFilters()).toEqual(
      expect.objectContaining({ group: "equity-desks" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /clear all filters/i }));
    expect(accountGroupInput).toHaveValue("");
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ group: expect.any(String) }),
    );

    fireEvent.click(screen.getByRole("button", { name: /^groups$/i }));
    fireEvent.change(screen.getByLabelText(/group search/i), {
      target: { value: "equity" },
    });

    await act(async () => {
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    expect(fetchGroupsMock).toHaveBeenCalledTimes(2);
    expect(
      screen.getByRole("option", { name: "equity-desks" }),
    ).toBeInTheDocument();
  });

  it("applies group advanced search only on apply", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /find by fields/i });

    await user.type(within(dialog).getByLabelText(/^notes$/i), "desk");
    await user.type(
      within(dialog).getByLabelText(/^block reason$/i, { selector: "input" }),
      "halt",
    );
    expect(lastGroupFilters()).not.toEqual(
      expect.objectContaining({ notes: "desk" }),
    );
    expect(lastGroupFilters()).not.toEqual(
      expect.objectContaining({ blockReason: "halt" }),
    );

    await user.click(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    );
    expect(lastGroupFilters()).toEqual(
      expect.objectContaining({ notes: "desk", blockReason: "halt" }),
    );
  });

  it("applies supported account-count inequality filters", async () => {
    const user = userEvent.setup();
    renderAccounts(
      "/accounts?tab=groups&accountCountMode=neq&accountCountMin=1",
    );

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /find by fields/i });

    expect(within(dialog).getAllByRole("combobox")[1]).toHaveTextContent(
      /not equal/i,
    );
    expect(within(dialog).getAllByPlaceholderText(/value/i)[1]).toHaveValue(
      "1",
    );
    await user.click(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    );

    expect(lastGroupFilters()).toEqual(
      expect.objectContaining({
        accountCountMode: "neq",
        accountCountMin: "1",
      }),
    );
    expect(screen.getByText(/Active filters/i)).toBeInTheDocument();
    expect(screen.getByText(/account count: not equal 1/i)).toBeInTheDocument();
  });

  it("does not synthesize the default group when the group response omits it", async () => {
    const user = userEvent.setup();
    useGroupsMock.mockReturnValue(
      readyPage(groups.filter((g) => g.code !== "")),
    );

    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));

    expect(screen.queryByText("Default group")).not.toBeInTheDocument();
    expect(screen.getByText("equity-desks")).toBeInTheDocument();
  });

  it("does not render a group code as a title when the title is empty", async () => {
    const user = userEvent.setup();
    useGroupsMock.mockReturnValue(
      readyPage([
        {
          code: "equity-desks",
          title: "",
          blocked: false,
          blockReason: "",
          notes: "",
          accountCount: 1,
          positionCount: 0,
        },
      ]),
    );

    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    const row = screen.getByText("equity-desks").closest("tr");

    expect(row).not.toBeNull();
    expect(
      within(row as HTMLElement).getAllByText("equity-desks"),
    ).toHaveLength(1);
  });

  it("sets a regular group currency from the group currency dialog", async () => {
    const user = userEvent.setup();
    useGroupsMock.mockReturnValue(
      readyPage([
        {
          code: "",
          title: "",
          blocked: false,
          blockReason: "",
          notes: "",
          accountCount: 1,
          positionCount: 0,
        },
        {
          code: "equity-desks",
          title: "Equity desks",
          blocked: false,
          blockReason: "",
          notes: "",
          currency: "USD",
          accountCount: 1,
          positionCount: 2,
        },
      ]),
    );
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    const row = screen.getByText("Equity desks").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /edit group currency/i,
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /edit group currency/i,
    });
    expect(
      within(dialog).getByText(
        /member accounts holding non-zero positions or P&L/i,
      ),
    ).toBeInTheDocument();
    const currency = within(dialog).getByLabelText(/^group currency$/i);
    await user.clear(currency);
    await user.type(currency, "EUR");
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(setGroupCurrencyMock).toHaveBeenCalledWith("equity-desks", "EUR"),
    );
  });

  it("sets the default group currency from the group currency dialog", async () => {
    const user = userEvent.setup();
    useGroupsMock.mockReturnValue(
      readyPage([
        {
          code: "",
          title: "",
          blocked: false,
          blockReason: "",
          notes: "",
          currency: "USD",
          accountCount: 1,
          positionCount: 1,
        },
      ]),
    );
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    const row = screen.getByText("Default group").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /edit group currency/i,
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /edit group currency/i,
    });
    const currency = within(dialog).getByLabelText(/^group currency$/i);
    await user.clear(currency);
    await user.type(currency, "EUR");
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(setDefaultGroupCurrencyMock).toHaveBeenCalledWith("EUR"),
    );
  });

  it("shows the source group name on inherited currency icons", () => {
    useAccountsMock.mockReturnValue(
      readyPage(
        [
          {
            code: "desk-alpha",
            title: "Desk alpha",
            blocked: false,
            blockReason: "",
            group: "equity-desks",
            currency: "",
            effectiveCurrency: "USD",
            currencyOrigin: "group",
            currencyCascade: { account: "", group: "USD", default: "" },
            notes: "",
            pnlHaltReason: "",
          },
        ].map(accountFixture),
      ),
    );

    renderAccounts();

    const row = screen.getByText("Desk alpha").closest("tr");
    expect(row).not.toBeNull();
    const titledNodes = Array.from(
      (row as HTMLElement).querySelectorAll("[title]"),
    );

    expect(
      titledNodes.some(
        (node) =>
          node.getAttribute("title") ===
          "Account currency is inherited from Equity desks",
      ),
    ).toBe(true);
  });

  it("renders default group counts from the group response, not the accounts response", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(readyPage([]));
    useGroupsMock.mockReturnValue(
      readyPage([
        {
          code: "",
          title: "",
          blocked: false,
          blockReason: "",
          notes: "",
          accountCount: 7,
          positionCount: 4,
        },
      ]),
    );

    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    const row = screen.getByText("Default group").closest("tr");

    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText("7")).toBeInTheDocument();
    expect(within(row as HTMLElement).getByText("4")).toBeInTheDocument();
  });
});

describe("Accounts share filter set", () => {
  it("copies a deep link that encodes the active accounts filters", async () => {
    const user = userEvent.setup();
    // Install the spy after userEvent.setup so its own clipboard stub does not
    // shadow it; the share button click uses fireEvent for the same reason.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    renderAccounts();

    await user.type(screen.getByLabelText(/account search/i), "acc-spx");
    // fireEvent (not userEvent) so the user-event clipboard stub does not
    // shadow the writeText spy asserted below.
    fireEvent.click(
      screen.getByRole("button", {
        name: /copy a link to the current filter set/i,
      }),
    );

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const url = new URL(writeText.mock.calls[0][0]);
    expect(url.pathname).toBe("/accounts");
    expect(url.searchParams.get("code")).toBe("acc-spx");
    // Default codeMatch / status stay out of the clean link, and the accounts
    // tab is implied (no tab param).
    expect(url.searchParams.get("codeMatch")).toBeNull();
    expect(url.searchParams.get("status")).toBeNull();
    expect(url.searchParams.get("tab")).toBeNull();
  });

  it("restores the accounts filter set from query params on mount", async () => {
    renderAccounts("/accounts?code=acc-spx&status=blocked");

    expect(screen.getByLabelText(/account search/i)).toHaveValue("acc-spx");
    await waitFor(() =>
      expect(lastAccountFilters()).toEqual(
        expect.objectContaining({ code: "acc-spx", status: "blocked" }),
      ),
    );
  });

  it("restores the groups tab and its filters from query params on mount", async () => {
    renderAccounts("/accounts?tab=groups&code=equity");

    expect(screen.getByLabelText(/group search/i)).toHaveValue("equity");
    await waitFor(() =>
      expect(lastGroupFilters()).toEqual(
        expect.objectContaining({ code: "equity" }),
      ),
    );
  });
});
