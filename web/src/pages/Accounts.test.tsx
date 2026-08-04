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

import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  Account,
  Asset,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  Group,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { useAccountsPage } from "@/api/useAccounts";
import { useGroupsPage } from "@/api/useGroups";
import { SidebarProvider } from "@/components/SidebarContext";
import { ApiError, AUTOCOMPLETE_SUGGESTION_LIMIT } from "@/framework";
import i18n from "@/i18n";
import { Accounts, CreateAccountDialog } from "@/pages/Accounts";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useAccounts", () => ({ useAccountsPage: vi.fn() }));
vi.mock("@/api/useGroups", () => ({ useGroupsPage: vi.fn() }));
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
vi.mock("@/components/TableControls", async () => {
  const actual =
    await vi.importActual<typeof import("@/components/TableControls")>(
      "@/components/TableControls",
    );
  const dialogs =
    await vi.importActual<typeof import("@/components/BusinessCsvDialogs")>(
      "@/components/BusinessCsvDialogs",
    );
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

function readyPage<T>(items: T[]): PollingResult<{ items: T[]; total: number }> {
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
      <CreateAccountDialog groupSuggestions={["equity-desks"]} onCreated={onCreated} />
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
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
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
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
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

    await user.click(screen.getByRole("button", { name: /export groups csv/i }));
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
      readyPage(filters?.group === "equity-desks" ? [pagedAccounts[51]] : pagedAccounts),
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
    useAccountsMock.mockReturnValue(readyPage([
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
    ].map(accountFixture)));
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
      expect(setAccountCurrencyMock).toHaveBeenCalledWith(
        "desk-alpha",
        "EUR",
      ),
    );
  });

  it("searches currency assets while editing account currency", async () => {
    const user = userEvent.setup();
    const asset: Asset = { code: "EUR", title: "Euro", assetClass: "currency" };
    fetchAssetsMock.mockResolvedValue([asset]);
    useAccountsMock.mockReturnValue(readyPage([
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
    ].map(accountFixture)));
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

  it("clears an already-set account currency", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(readyPage([
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
    ].map(accountFixture)));
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
      readyPage([
        {
          code: "empty-title",
          title: "",
          blocked: false,
          blockReason: "",
          group: "",
          notes: "",
          pnlHaltReason: "",
        },
      ].map(accountFixture)),
    );

    renderAccounts();

    const row = screen.getByText("empty-title").closest("tr");

    expect(row).not.toBeNull();
    expect(
      within(row as HTMLElement).getAllByText("empty-title"),
    ).toHaveLength(1);
  });

  it("renders exact account PnL values without a currency suffix", () => {
    useAccountsMock.mockReturnValue(
      readyPage([
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
      ].map(accountFixture)),
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
    expect(cell("Empty PnL")).toHaveTextContent("—");
    expect(cell("Empty PnL")).toHaveClass("text-[var(--pnl-flat)]");
    expect(within(row("Empty PnL")).queryByRole("note")).not.toBeInTheDocument();

    const knownWarning = within(row("Known halt")).getByRole("note", {
      name: /position cost basis is unavailable/i,
    });
    expect(knownWarning).toHaveAttribute("tabindex", "0");
    expect(
      within(row("Unknown halt")).getByRole("note", {
        name: /stopped by the engine/i,
      }),
    ).toBeInTheDocument();
  });

  it("loads blocked-account audit with the account block action", async () => {
    const user = userEvent.setup();
    useAccountsMock.mockReturnValue(
      readyPage([
        {
          code: "algo-infinite-loop",
          title: "",
          blocked: true,
          blockReason: "Trading halt",
          group: "",
          notes: "",
          pnlHaltReason: "",
        },
      ].map(accountFixture)),
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
      readyPage([
        {
          code: "algo-infinite-loop",
          title: "",
          blocked: true,
          blockReason: "Trading halt",
          group: "",
          notes: "",
          pnlHaltReason: "",
        },
      ].map(accountFixture)),
    );

    renderAccounts();
    await user.click(
      screen.getByRole("button", { name: /view block details/i }),
    );
    const details = await screen.findByRole("dialog", {
      name: /blocked account/i,
    });
    await user.click(within(details).getByRole("button", { name: /^unblock$/i }));
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
      readyPage([
        {
          code: "algo-infinite-loop",
          title: "",
          blocked: true,
          blockReason: "Trading halt",
          group: "",
          notes: "",
          pnlHaltReason: "",
        },
      ].map(accountFixture)),
    );

    renderAccounts();
    await user.click(
      screen.getByRole("button", { name: /view block details/i }),
    );
    const details = await screen.findByRole("dialog", {
      name: /blocked account/i,
    });
    await user.click(within(details).getByRole("button", { name: /^unblock$/i }));
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
    expect(within(row as HTMLElement).getByText(/blocked/i)).toBeInTheDocument();
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
      within(row as HTMLElement).getByRole("button", { name: /filter by equity-desks/i }),
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
    expect(screen.getByText(/Block reason: Contains halt/i)).toBeInTheDocument();
    expect(
      screen.getAllByRole("button", { name: /Remove advanced filter/i })
        .length,
    ).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: /clear all filters/i }));
    expect(screen.queryByText(/Active filters/i)).not.toBeInTheDocument();
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ blockReason: expect.any(String) }),
    );
    expect(lastAccountFilters()).not.toEqual(
      expect.objectContaining({ positionCountMin: expect.any(String) }),
    );

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const reopened = screen.getByRole("dialog", { name: /find by fields/i });
    expect(within(reopened).queryByLabelText(/^notes$/i)).not.toBeInTheDocument();
    expect(
      within(reopened).getByLabelText(/^block reason$/i, { selector: "input" }),
    ).toHaveValue("");
    expect(within(reopened).getAllByPlaceholderText("Value")[0]).toHaveValue("");
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
    expect(
      screen.getByText(/account count: not equal 1/i),
    ).toBeInTheDocument();
  });

  it("does not synthesize the default group when the group response omits it", async () => {
    const user = userEvent.setup();
    useGroupsMock.mockReturnValue(readyPage(groups.filter((g) => g.code !== "")));

    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));

    expect(screen.queryByText("Default group")).not.toBeInTheDocument();
    expect(screen.getByText("equity-desks")).toBeInTheDocument();
  });

  it("does not render a group code as a title when the title is empty", async () => {
    const user = userEvent.setup();
    useGroupsMock.mockReturnValue(readyPage([
      {
        code: "equity-desks",
        title: "",
        blocked: false,
        blockReason: "",
        notes: "",
        accountCount: 1,
        positionCount: 0,
      },
    ]));

    renderAccounts();

    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    const row = screen.getByText("equity-desks").closest("tr");

    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getAllByText("equity-desks")).toHaveLength(1);
  });

  it("sets a regular group currency from the group currency dialog", async () => {
    const user = userEvent.setup();
    useGroupsMock.mockReturnValue(readyPage([
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
    ]));
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
      expect(setGroupCurrencyMock).toHaveBeenCalledWith(
        "equity-desks",
        "EUR",
      ),
    );
  });

  it("sets the default group currency from the group currency dialog", async () => {
    const user = userEvent.setup();
    useGroupsMock.mockReturnValue(readyPage([
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
    ]));
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
    useAccountsMock.mockReturnValue(readyPage([
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
    ].map(accountFixture)));

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
    useGroupsMock.mockReturnValue(readyPage([
      {
        code: "",
        title: "",
        blocked: false,
        blockReason: "",
        notes: "",
        accountCount: 7,
        positionCount: 4,
      },
    ]));

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
