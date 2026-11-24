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
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";

import { fetchCredentialStorage } from "@/api/credentialStorage";
import type {
  Account,
  CredentialStorage,
  Group,
  TradingConnection,
  TradingProvider,
  TradingSnapshot,
  TradingSymbolVerification,
  TestExecutionPolicy,
  TestExecutionPolicyState,
} from "@/api/types";
import { SidebarProvider } from "@/components/SidebarContext";
import {
  ApiError,
  AUTOCOMPLETE_SUGGESTION_LIMIT,
  createApiClient,
  createOfficerApi,
} from "@/framework";
import i18n from "@/i18n";
import enTrading from "@/i18n/locales/en/trading.json";
import esTrading from "@/i18n/locales/es/trading.json";
import ptTrading from "@/i18n/locales/pt-BR/trading.json";
import ruTrading from "@/i18n/locales/ru/trading.json";
import zhTrading from "@/i18n/locales/zh-CN/trading.json";
import { setAccountScope } from "@/lib/accountScope";
import { TradingConnections } from "@/pages/TradingConnections";
import { accountRow, jsonResponse, renderWithApi } from "@/test/apiClient";
import { holdPolling } from "@/test/polling";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

const venueWiring = vi.hoisted(() => ({
  add: vi.fn(),
  picker: vi.fn(),
  header: vi.fn(),
  search: vi.fn(),
  draft: vi.fn(),
  verdict: vi.fn(),
  table: vi.fn(),
  deletion: vi.fn(),
  access: vi.fn(),
}));

vi.mock("@/components/venues", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/components/venues")>();
  return {
    ...actual,
    SymbolSearchPanel(
      props: import("@/components/venues").SymbolSearchPanelProps<
        import("@/api/types").TradingSymbolSearch["matches"][number]
      >,
    ) {
      venueWiring.search(props);
      return <actual.SymbolSearchPanel {...props} />;
    },
    InstrumentDraftRow(
      props: import("@/components/venues").InstrumentDraftRowProps,
    ) {
      venueWiring.draft(props);
      return <actual.InstrumentDraftRow {...props} />;
    },
    VerifyResult(props: import("@/components/venues").VerifyResultProps) {
      venueWiring.verdict(props);
      return <actual.VerifyResult {...props} />;
    },
    InstrumentsTable(
      props: import("@/components/venues").InstrumentsTableProps<
        import("@/api/types").TradingInstrument
      >,
    ) {
      venueWiring.table(props);
      return <actual.InstrumentsTable {...props} />;
    },
    DeleteInstrumentDialog(
      props: import("@/components/venues").DeleteInstrumentDialogProps,
    ) {
      venueWiring.deletion(props);
      return <actual.DeleteInstrumentDialog {...props} />;
    },
    AddProviderButton(props: Parameters<typeof actual.AddProviderButton>[0]) {
      venueWiring.add(props);
      return <actual.AddProviderButton {...props} />;
    },
    ProviderGuideDialog(
      props: Parameters<typeof actual.ProviderGuideDialog>[0],
    ) {
      venueWiring.picker(props);
      return <actual.ProviderGuideDialog {...props} />;
    },
    VenueCardHeader(props: Parameters<typeof actual.VenueCardHeader>[0]) {
      venueWiring.header(props);
      return <actual.VenueCardHeader {...props} />;
    },
    VenueAccessList(props: import("@/components/venues").VenueAccessListProps) {
      venueWiring.access(props);
      return <actual.VenueAccessList {...props} />;
    },
  };
});

const provider: TradingProvider = {
  executionPolicy: false,
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
  fetchGroups: vi.fn(),
  fetchAccountState: vi.fn(),
  fetchAssets: vi.fn(),
  fetchMarketData: vi.fn(),
  createTradingConnection: vi.fn(),
  updateTradingConnectionSettings: vi.fn(),
  fetchTestExecutionPolicy: vi.fn(),
  updateTestExecutionPolicy: vi.fn(),
  setTradingConnectionEnabled: vi.fn(),
  checkTradingConnection: vi.fn(),
  upsertTradingInstrument: vi.fn(),
  deleteTradingInstrument: vi.fn(),
  deleteTradingConnection: vi.fn(),
  verifyTradingSymbol: vi.fn(),
  searchTradingSymbols: vi.fn(),
  grantTradingAccess: vi.fn(),
  revokeTradingAccess: vi.fn(),
  setTradingFeeAccount: vi.fn(),
  clearTradingFeeAccount: vi.fn(),
  createAccount: vi.fn(),
  createAsset: vi.fn(),
};
// PreMigrationBackupBanner polls GET /database/pre-migration-backups.
vi.mock("@/api/usePreMigrationBackups", () => ({
  usePreMigrationBackups: () => ({
    load: { state: "ready", data: [], error: null },
    reload: () => {},
  }),
}));
vi.mock("@/api/credentialStorage");
const fetchCredentialStorageMock = vi.mocked(fetchCredentialStorage);
let snapshot: TradingSnapshot;
let credentialStorage: CredentialStorage;
let accounts: string[];
let groups: string[];

function connection(
  overrides: Partial<TradingConnection> = {},
): TradingConnection {
  return {
    executionPolicy: false,
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
    diagnostics: [],
    ...overrides,
  };
}

function account(code: string): Account {
  return accountRow(code, { title: "" });
}

function group(code: string): Group {
  return { code, title: "", notes: "", blocked: false, blockReason: "" };
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

function storage(sealed: boolean): CredentialStorage {
  return {
    credentialsSealed: sealed,
    plaintextTradingKeys: !sealed,
    plaintextMarketDataKeys: false,
    sealedAt: null,
    resealedSecrets: 0,
  };
}

function renderTrading() {
  return renderWithApi(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
          <DisplayPreferencesProvider
            densityStorageKey="pit-test-density"
            tradeStyleStorageKey="pit-test-style"
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

async function card(label = "Alpaca paper", expand = true) {
  const region = within(await screen.findByRole("region", { name: label }));
  const toggle = region.getByRole("button", { name: label });
  if (expand && toggle.getAttribute("aria-expanded") === "false")
    await userEvent.click(toggle);
  return region;
}

async function openCreateConnection(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    await screen.findByRole("button", { name: /^Add trading connection/ }),
  );
  const picker = within(
    screen.getByRole("dialog", { name: "Add trading connection" }),
  );
  await user.click(picker.getByRole("button", { name: "Alpaca" }));
  return within(
    await screen.findByRole("dialog", { name: "Add Alpaca connection" }),
  );
}

beforeAll(() => {
  window.HTMLElement.prototype.scrollIntoView = vi.fn();
});

// The cases count the snapshot reads an action causes, so the 5 s background
// poll is held: on a slow machine it would otherwise add reads mid-case.
let polling: ReturnType<typeof holdPolling>;

afterEach(() => {
  polling.restore();
});

beforeEach(async () => {
  vi.resetAllMocks();
  polling = holdPolling();
  await i18n.changeLanguage("en");
  snapshot = {
    providers: [provider],
    connections: [connection()],
  };
  credentialStorage = storage(true);
  accounts = ["alpha", "beta", "fees"];
  api.fetchTrading.mockImplementation(async () => snapshot);
  fetchCredentialStorageMock.mockImplementation(async () => credentialStorage);
  api.fetchMarketData.mockResolvedValue({
    providers: [],
    instances: [],
    freshnessSeconds: 10,
    restartRequired: false,
  });
  api.fetchAccounts.mockImplementation(async ({ code }: { code: string }) =>
    accounts.filter((item) => item.startsWith(code)).map(account),
  );
  groups = ["desk", "ops"];
  api.fetchGroups.mockImplementation(async ({ code }: { code: string }) =>
    groups.filter((item) => item.startsWith(code)).map(group),
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
  it("starts collapsed, opens one connection and preserves a hidden draft", async () => {
    snapshot.connections.push(
      connection({ id: "alpaca-2", label: "Other paper" }),
    );
    const user = userEvent.setup();
    renderTrading();
    const first = within(
      await screen.findByRole("region", { name: "Alpaca paper" }),
    );
    const second = within(screen.getByRole("region", { name: "Other paper" }));
    const toggle = first.getByRole("button", { name: "Alpaca paper" });
    const otherToggle = second.getByRole("button", { name: "Other paper" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(otherToggle).toHaveAttribute("aria-expanded", "false");
    await user.click(toggle);
    const draft = first.getByPlaceholderText("External symbol");
    await user.type(draft, "BTC/USD");
    await user.click(otherToggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(draft).not.toBeVisible();
    expect(otherToggle).toHaveAttribute("aria-expanded", "true");
    await user.click(toggle);
    expect(draft).toBeVisible();
    expect(draft).toHaveValue("BTC/USD");
    expect(otherToggle).toHaveAttribute("aria-expanded", "false");
  });

  it("deletes a connection after confirmation and keeps a refusal in the dialog", async () => {
    const user = userEvent.setup();
    api.deleteTradingConnection.mockRejectedValueOnce(
      new ApiError("Disable the connection first", "conflict", 409),
    );
    api.deleteTradingConnection.mockImplementationOnce(async (id: string) => {
      snapshot = {
        ...snapshot,
        connections: snapshot.connections.filter((item) => item.id !== id),
      };
    });
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: "Delete trading connection" }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Delete trading connection?" }),
    );
    expect(api.deleteTradingConnection).not.toHaveBeenCalled();
    await user.click(dialog.getByRole("button", { name: "Delete" }));
    expect(await dialog.findByRole("alert")).toHaveTextContent(
      "Disable the connection first",
    );
    await user.click(dialog.getByRole("button", { name: "Delete" }));
    await waitFor(() =>
      expect(
        screen.queryByRole("region", { name: "Alpaca paper" }),
      ).not.toBeInTheDocument(),
    );
    expect(api.deleteTradingConnection.mock.calls).toEqual([
      ["alpaca-1"],
      ["alpaca-1"],
    ]);
  });
  it("confirms non-release trading only after the server refusal", async () => {
    const user = userEvent.setup();
    api.setTradingConnectionEnabled.mockRejectedValueOnce(
      new ApiError(
        "Panel confirmation required",
        "non_release_confirmation_required",
        403,
      ),
    );
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Enable" }));
    const dialog = within(
      await screen.findByRole("alertdialog", {
        name: "Enable trading in a non-release build?",
      }),
    );
    expect(
      dialog.getByText(
        "This non-release build is for quick tests only. Enabling this connection lets it send real orders to the venue. The release build will not see the orders this build sends. This build may corrupt its data.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Panel confirmation required"),
    ).not.toBeInTheDocument();
    expect(api.setTradingConnectionEnabled.mock.calls).toEqual([
      ["alpaca-1", true],
    ]);
    await user.click(dialog.getByRole("button", { name: "Enable trading" }));
    expect(api.setTradingConnectionEnabled.mock.calls).toEqual([
      ["alpaca-1", true],
      ["alpaca-1", true, true],
    ]);
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(
      await scope.findByRole("button", { name: "Disable" }),
    ).toBeInTheDocument();
  });

  it("cancels non-release trading without another request", async () => {
    const user = userEvent.setup();
    api.setTradingConnectionEnabled.mockRejectedValueOnce(
      new ApiError(
        "Panel confirmation required",
        "non_release_confirmation_required",
        403,
      ),
    );
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Enable" }));
    const dialog = within(await screen.findByRole("alertdialog"));
    await user.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(api.setTradingConnectionEnabled.mock.calls).toEqual([
      ["alpaca-1", true],
    ]);
    expect(scope.getByRole("button", { name: "Enable" })).toBeEnabled();
  });

  it("enables immediately without confirmation when the server accepts", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Enable" }));
    expect(
      await scope.findByRole("button", { name: "Disable" }),
    ).toBeInTheDocument();
    expect(api.setTradingConnectionEnabled.mock.calls).toEqual([
      ["alpaca-1", true],
    ]);
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("keeps the fee-account refusal on enable without confirmation", async () => {
    const user = userEvent.setup();
    api.setTradingConnectionEnabled.mockRejectedValueOnce(
      new ApiError("Choose a fee ledger", "fee_account_required", 409),
    );
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Enable" }));
    expect(await scope.findByText("Choose a fee ledger")).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    await user.click(scope.getByRole("button", { name: "Set fee account" }));
    expect(scope.getByRole("combobox", { name: "Fee account" })).toHaveFocus();
    expect(api.setTradingConnectionEnabled.mock.calls).toEqual([
      ["alpaca-1", true],
    ]);
  });

  it("disables without non-release confirmation", async () => {
    const user = userEvent.setup();
    snapshot.connections = [connection({ enabled: true })];
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Disable" }));
    expect(
      await scope.findByRole("button", { name: "Enable" }),
    ).toBeInTheDocument();
    expect(api.setTradingConnectionEnabled.mock.calls).toEqual([
      ["alpaca-1", false],
    ]);
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("shows a confirmed non-release enable failure through the usual error line", async () => {
    const user = userEvent.setup();
    api.setTradingConnectionEnabled
      .mockRejectedValueOnce(
        new ApiError("Confirm first", "non_release_confirmation_required", 403),
      )
      .mockRejectedValueOnce(new ApiError("Venue refused", "upstream", 502));
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Enable" }));
    const dialog = within(await screen.findByRole("alertdialog"));
    await user.click(dialog.getByRole("button", { name: "Enable trading" }));
    expect(await scope.findByText("Venue refused")).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(api.setTradingConnectionEnabled.mock.calls).toEqual([
      ["alpaca-1", true],
      ["alpaca-1", true, true],
    ]);
  });

  it.each(["failed", "success", "pending"])(
    "forgets a %s row verdict when the deleted symbol is re-added",
    async (verification) => {
      const user = userEvent.setup();
      const instrument = {
        externalSymbol: "AAPL",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        enabled: true,
      };
      snapshot.connections = [connection({ instruments: [instrument] })];
      let failVerify!: (error: Error) => void;
      if (verification === "failed") {
        api.verifyTradingSymbol.mockRejectedValueOnce(
          new Error("Venue offline"),
        );
      } else if (verification === "pending") {
        api.verifyTradingSymbol.mockImplementationOnce(
          () =>
            new Promise<TradingSymbolVerification>((_resolve, reject) => {
              failVerify = reject;
            }),
        );
      }
      api.deleteTradingInstrument.mockImplementationOnce(async () => {
        updateConnection("alpaca-1", { instruments: [] });
      });
      api.upsertTradingInstrument.mockImplementationOnce(async () => {
        updateConnection("alpaca-1", { instruments: [instrument] });
        return snapshot;
      });
      renderTrading();
      const scope = await card();
      const table = within(scope.getByRole("table", { name: "Instruments" }));
      const form = within(scope.getByRole("region", { name: "Instruments" }));
      await user.click(table.getByRole("button", { name: "Verify symbol" }));
      if (verification !== "pending") {
        expect(
          await table.findByText(
            verification === "failed" ? "Verification failed" : "Symbol exists",
          ),
        ).toBeInTheDocument();
      }
      await user.click(
        table.getByRole("button", { name: "Delete instrument" }),
      );
      await user.click(
        within(
          screen.getByRole("alertdialog", { name: "Delete instrument?" }),
        ).getByRole("button", { name: "Delete" }),
      );
      await waitFor(() =>
        expect(
          table.queryByText("AAPL", { exact: true }),
        ).not.toBeInTheDocument(),
      );
      if (verification === "pending") {
        await act(async () => failVerify(new Error("Venue offline")));
      }
      await user.type(form.getByPlaceholderText("External symbol"), "AAPL");
      await user.type(form.getByPlaceholderText("Base"), "AAPL");
      await user.click(form.getByRole("button", { name: "Add instrument" }));
      expect(
        await table.findByText("AAPL", { exact: true }),
      ).toBeInTheDocument();
      expect(scope.queryByRole("alert")).not.toBeInTheDocument();
      expect(table.queryByText("Verification failed")).not.toBeInTheDocument();
      expect(table.queryByText("Symbol exists")).not.toBeInTheDocument();
    },
  );

  it.each(["delete", "poll"])(
    "removes a row verify banner when its instrument disappears through %s",
    async (removal) => {
      const user = userEvent.setup();
      snapshot.connections = [
        connection({
          instruments: [
            {
              externalSymbol: "AAPL",
              baseAsset: "AAPL",
              quoteAsset: "USD",
              enabled: true,
            },
          ],
        }),
      ];
      api.verifyTradingSymbol.mockRejectedValueOnce(new Error("Venue offline"));
      renderTrading();
      const scope = await card();
      const row = within(scope.getByRole("table", { name: "Instruments" }));
      await user.click(row.getByRole("button", { name: "Verify symbol" }));
      expect(await scope.findByText("AAPL: Venue offline")).toBeInTheDocument();
      if (removal === "delete") {
        api.deleteTradingInstrument.mockImplementationOnce(async () => {
          updateConnection("alpaca-1", { instruments: [] });
        });
        await user.click(
          row.getByRole("button", { name: "Delete instrument" }),
        );
        await user.click(
          within(
            screen.getByRole("alertdialog", { name: "Delete instrument?" }),
          ).getByRole("button", { name: "Delete" }),
        );
        expect(api.deleteTradingInstrument).toHaveBeenCalledExactlyOnceWith(
          "alpaca-1",
          "AAPL",
        );
      } else {
        updateConnection("alpaca-1", { instruments: [] });
        await user.click(screen.getByRole("button", { name: "Refresh" }));
      }
      await waitFor(() =>
        expect(venueWiring.table.mock.lastCall![0].instruments).toEqual([]),
      );
      expect(scope.queryByText("AAPL: Venue offline")).not.toBeInTheDocument();
    },
  );
  it("wires the shared card header to provider, badges, error, links and settings", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({ state: "unavailable", error: "Venue is offline" }),
    ];
    renderTrading();
    const region = await screen.findByRole("region", { name: "Alpaca paper" });
    const scope = within(region);
    const title = scope.getByRole("heading", { name: "Alpaca paper" });
    expect(region).toHaveAttribute("aria-labelledby", title.id);
    expect(region).toHaveAttribute("tabindex", "-1");
    expect(venueWiring.header).toHaveBeenLastCalledWith(
      expect.objectContaining({
        providerType: "alpaca",
        title: "Alpaca paper",
        titleId: title.id,
        error: "Venue is offline",
        errorTone: "danger",
        links: expect.objectContaining({
          siteUrl: "https://alpaca.markets",
          docsUrl: "https://docs.alpaca.markets/us/docs/paper-trading",
        }),
      }),
    );
    for (const badge of ["Alpaca", "Paper", "Disabled"])
      expect(scope.getByText(badge, { exact: true })).toBeInTheDocument();
    // As a feed's header: a disabled connection reads as disabled, not as its
    // worker's runtime state; the worker's error still shows.
    expect(
      scope.queryByText("Unavailable", { exact: true }),
    ).not.toBeInTheDocument();
    expect(scope.getByText("Venue is offline")).toBeInTheDocument();
    expect(scope.getByRole("link", { name: "Provider site" })).toHaveAttribute(
      "href",
      "https://alpaca.markets",
    );
    expect(scope.getByRole("link", { name: "Provider docs" })).toHaveAttribute(
      "href",
      "https://docs.alpaca.markets/us/docs/paper-trading",
    );
    const settings = scope.getByRole("button", { name: "Edit settings" });
    expect(settings).toHaveAttribute("title", "Edit settings");
    expect(settings.querySelector("svg.lucide-settings")).toBeInTheDocument();
    expect(settings.textContent).toBe("");
    expect(
      scope.queryByRole("button", { name: "Delete connection" }),
    ).not.toBeInTheDocument();
    await user.click(settings);
    expect(
      screen.getByRole("dialog", { name: "Edit settings" }),
    ).toBeInTheDocument();
  });

  it("shows a degraded connection's warn state, error and diagnostics", async () => {
    snapshot.connections = [
      connection({
        enabled: true,
        state: "degraded",
        error: "Trade updates refused",
        diagnostics: [
          {
            level: "error",
            code: "connection_error",
            kind: "provider",
            title: "Trade-updates stream unavailable",
            detail: "The venue closed the stream.",
            actions: [],
            at: "2026-10-13T08:00:00Z",
          },
        ],
      }),
    ];
    renderTrading();
    const scope = await card();
    expect(scope.getByText("Degraded", { exact: true })).toHaveClass(
      "text-[var(--warn)]",
    );
    expect(
      scope.queryByText("Running", { exact: true }),
    ).not.toBeInTheDocument();
    expect(venueWiring.header).toHaveBeenLastCalledWith(
      expect.objectContaining({
        error: "Trade updates refused",
        errorTone: "warn",
      }),
    );
    expect(scope.getByText("Trade updates refused")).toBeInTheDocument();
    expect(
      within(scope.getByRole("region", { name: "Problems" })).getByText(
        "Trade-updates stream unavailable",
      ),
    ).toBeInTheDocument();
  });

  it("shows a running connection's diagnostics in its panel", async () => {
    snapshot.connections = [
      connection({
        enabled: true,
        state: "running",
        diagnostics: [
          {
            level: "warn",
            code: "connection_error",
            kind: "provider",
            title: "Trade-updates stream reconnected",
            detail: "The venue closed the stream once.",
            actions: [],
            at: "2026-10-13T08:00:00Z",
          },
        ],
      }),
    ];
    renderTrading();
    const scope = await card();
    expect(scope.getByText("Running", { exact: true })).toHaveClass(
      "text-[var(--ok)]",
    );
    expect(
      within(scope.getByRole("region", { name: "Problems" })).getByText(
        "Trade-updates stream reconnected",
      ),
    ).toBeInTheDocument();
  });

  it.each([
    { field: "Account", form: "Grant access" },
    { field: "Fee account", form: "Fee account" },
  ])(
    "clears the $field draft through its standard clear button",
    async ({ field, form }) => {
      const user = userEvent.setup();
      renderTrading();
      const scope = within((await card()).getByRole("form", { name: form }));
      const input = scope.getByRole("combobox", { name: field });
      await user.type(input, "fees");
      await user.click(scope.getByRole("button", { name: "Clear field" }));
      expect(input).toHaveValue("");
      expect(
        scope.queryByRole("button", { name: "Clear field" }),
      ).not.toBeInTheDocument();
      expect(
        scope.getByRole("button", {
          name: form === "Grant access" ? "Grant access" : "Save",
        }),
      ).toBeDisabled();
      expect(api.grantTradingAccess).not.toHaveBeenCalled();
      expect(api.setTradingFeeAccount).not.toHaveBeenCalled();
      expect(api.clearTradingFeeAccount).not.toHaveBeenCalled();
    },
  );

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
        enabled: true,
        state: "unavailable",
        error: "Venue is offline",
      }),
    ];
    renderTrading();
    const paper = await card();
    const live = await card("Alpaca live", false);
    const unavailable = await card("Alpaca unavailable", false);
    // Alpaca's paper docs cover paper trading only: a live card links the
    // docs for live credentials instead.
    expect(live.getByRole("link", { name: "Provider site" })).toHaveAttribute(
      "href",
      "https://alpaca.markets",
    );
    expect(live.getByRole("link", { name: "Provider docs" })).toHaveAttribute(
      "href",
      "https://docs.alpaca.markets/us/docs/authentication",
    );
    expect(paper.getByRole("link", { name: "Provider docs" })).toHaveAttribute(
      "href",
      "https://docs.alpaca.markets/us/docs/paper-trading",
    );
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
      paper.getByRole("heading", { name: "Alpaca paper", level: 3 }),
    ).toBeInTheDocument();
    for (const name of [
      "Setup checklist",
      "Account access",
      "Fee account",
      "Instruments",
    ]) {
      expect(
        paper.getByRole("heading", { name, level: 4 }),
      ).toBeInTheDocument();
    }
    expect(
      paper.getByRole("heading", { name: "Grant access", level: 5 }),
    ).toBeInTheDocument();
    expect(
      paper.queryByRole("heading", { name: "Add instrument" }),
    ).not.toBeInTheDocument();
    expect(paper.getByText("Paper", { exact: true })).toBeInTheDocument();
    expect(
      paper.queryByText("Stopped", { exact: true }),
    ).not.toBeInTheDocument();
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
      screen.getAllByRole("button", { name: /^Add trading connection/ }),
    ).toHaveLength(1);
  });

  it("orders collapsed connection actions as toggle, settings, delete", async () => {
    renderTrading();
    const scope = await card("Alpaca paper", false);
    expect(
      scope
        .getAllByRole("button")
        .map(
          (button) =>
            button.getAttribute("aria-label") ?? button.textContent?.trim(),
        ),
    ).toEqual([
      "Alpaca paper",
      "Enable",
      "Edit settings",
      "Delete trading connection",
    ]);
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

  it.each(["missing-venue"])(
    "isolates a restored connection without source metadata for %s",
    async (missingProvider) => {
      snapshot.connections.push(
        connection({
          id: "restored-custom",
          provider: missingProvider,
          label: "Restored custom connection",
          executionPolicy: undefined,
          state: "unavailable",
          error: "Custom provider is unavailable",
        }),
      );
      renderTrading();

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "Custom provider is unavailable",
      );
      expect(
        await screen.findByRole("region", { name: "Alpaca paper" }),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("region", { name: "Restored custom connection" }),
      ).not.toBeInTheDocument();
      expect(api.fetchTestExecutionPolicy).not.toHaveBeenCalled();
      expect(
        screen.queryByText("executionPolicy.undefined"),
      ).not.toBeInTheDocument();
    },
  );

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

  it("checks a test connection without showing the fresh identity or venue status", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({ executionPolicy: true, venueIdentity: "test:stored" }),
    ];
    api.fetchTestExecutionPolicy.mockResolvedValue({
      connection: "alpaca-1",
      revision: "1",
      rules: [],
    });
    api.checkTradingConnection.mockResolvedValue({
      venueIdentity: "test:fresh-check",
      venueStatus: "",
    });
    renderTrading();
    const scope = await card();
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
      "Check passed: test execution reaches no venue and needs no keys.",
    );
    expect(screen.queryByText(/test:fresh-check/)).not.toBeInTheDocument();
    expect(screen.queryByText(/· status/)).not.toBeInTheDocument();
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
      action: "Resolve",
      method: "searchTradingSymbols" as const,
      field: "Resolve symbol at this venue",
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
          access: [
            { account: "alpha", accountGroup: "", venueAccount: "", group: "" },
          ],
        }),
      ];
      api[method].mockReturnValue(new Promise(() => {}));
      renderTrading();
      const scope = await card();
      if (field !== "")
        await user.type(
          field === "External symbol"
            ? scope.getByPlaceholderText(field)
            : scope.getByRole("textbox", { name: field }),
          "ETH",
        );
      const venueButton = scope.getByRole("button", { name: action });
      await user.click(venueButton);
      expect(venueButton).toBeDisabled();
      await user.click(venueButton);
      expect(api[method]).toHaveBeenCalledTimes(1);
      expect(scope.getByRole("button", { name: "Disable" })).toBeEnabled();
      expect(
        scope.getByRole("button", { name: "Revoke access for account alpha" }),
      ).toBeEnabled();
      await user.click(scope.getByRole("button", { name: "Disable" }));
      expect(api.setTradingConnectionEnabled).toHaveBeenCalledExactlyOnceWith(
        "alpaca-1",
        false,
      );
      expect(
        await scope.findByRole("button", { name: "Enable" }),
      ).toBeEnabled();
      await user.click(
        scope.getByRole("button", { name: "Revoke access for account alpha" }),
      );
      expect(api.revokeTradingAccess).not.toHaveBeenCalled();
      await user.click(
        within(
          screen.getByRole("alertdialog", { name: "Revoke account access?" }),
        ).getByRole("button", { name: "Revoke access" }),
      );
      expect(api.revokeTradingAccess).toHaveBeenCalledExactlyOnceWith(
        "alpaca-1",
        { account: "alpha" },
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
    "shows the unsealed warning only when credential storage is unsealed (sealed %s)",
    async (sealed) => {
      const user = userEvent.setup();
      credentialStorage = storage(sealed);
      renderTrading();
      await user.click(
        (await card()).getByRole("button", { name: "Edit settings" }),
      );
      const dialog = within(
        screen.getByRole("dialog", { name: "Edit settings" }),
      );
      const warningText = "Keys will be stored unencrypted";
      if (sealed) {
        expect(dialog.queryByText(warningText)).not.toBeInTheDocument();
      } else {
        expect(dialog.getByRole("alert")).toHaveTextContent(warningText);
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

  it("wires the instrument blocks in MD order with the feed's draft enabled control", async () => {
    renderTrading();
    const scope = await card();
    const section = scope.getByRole("region", { name: "Instruments" });
    expect(section).toHaveAttribute("tabindex", "-1");
    expect(venueWiring.search).toHaveBeenLastCalledWith(
      expect.objectContaining({
        label: "Resolve symbol at this venue",
        placeholder: "Type BTC, BTC/USD or AAPL",
        noResultsText: "No matching symbols at this venue",
        searched: false,
        results: [],
        busy: false,
      }),
    );
    const props = venueWiring.search.mock.lastCall![0];
    const match = {
      symbol: "AAPL",
      name: "Apple",
      exchange: "NASDAQ",
      class: "US_EQUITY",
      tradable: false,
    };
    expect(props.getKey(match)).toBe("AAPL");
    expect(props.getLabel(match)).toBe("AAPL");
    expect(props.getName(match)).toBe("Apple");
    expect(props.getMetadata(match)).toBe("NASDAQ · US_EQUITY");
    expect(props.getWarning(match)).toBe("Not tradable");
    expect(props.getSearchText(match)).toBe("aapl apple nasdaq us_equity");
    expect(venueWiring.draft).toHaveBeenLastCalledWith(
      expect.objectContaining({
        draft: {
          externalSymbol: "",
          baseAsset: "",
          quoteAsset: "USD",
          enabled: true,
        },
        valid: false,
        busy: false,
        baseSuggestions: [],
        enabled: expect.objectContaining({ value: true }),
      }),
    );
    expect(venueWiring.verdict).toHaveBeenLastCalledWith({
      result: null,
      unsupportedLabel: "Verification unavailable",
    });
    expect(venueWiring.table).toHaveBeenLastCalledWith(
      expect.objectContaining({
        instruments: [],
        ariaLabel: "Instruments",
        parentEnabled: false,
        parentDisabledLabel: "Connection disabled",
        busy: false,
      }),
    );
    const search = within(section).getByRole("textbox", {
      name: "Resolve symbol at this venue",
    });
    const draft = within(section).getByPlaceholderText("External symbol");
    const table = within(section).getByRole("table", { name: "Instruments" });
    expect(
      search.compareDocumentPosition(draft) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      draft.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("passes all venue matches to the search panel while the add row remains rendered", async () => {
    const user = userEvent.setup();
    api.searchTradingSymbols.mockResolvedValue({
      supported: true,
      matches: Array.from({ length: 80 }, (_, index) => ({
        symbol: `SYM${index}`,
        name: `Company ${index}`,
        tradable: true,
      })),
    });
    renderTrading();
    const section = within(
      (await card()).getByRole("region", { name: "Instruments" }),
    );
    await user.type(
      section.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "SYM",
    );
    await user.click(section.getByRole("button", { name: "Resolve" }));
    await waitFor(() =>
      expect(venueWiring.search.mock.lastCall![0].results).toHaveLength(80),
    );
    expect(api.searchTradingSymbols).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { query: "SYM" },
    );
    expect(section.getByPlaceholderText("External symbol")).toBeInTheDocument();
    expect(
      section.getByRole("button", { name: "Add instrument" }),
    ).toBeInTheDocument();
  });

  it.each([
    { match: { symbol: "BTC/USD" }, base: "BTC", quote: "USD", add: true },
    {
      match: { symbol: "ETHUSDT", baseAsset: "ETH", quoteAsset: "USDT" },
      base: "ETH",
      quote: "USDT",
      add: true,
    },
    { match: { symbol: "GBTC" }, base: "GBTC", quote: "", add: false },
  ])(
    "fills the draft pair from a picked $match.symbol",
    async ({ match, base, quote, add }) => {
      const user = userEvent.setup();
      api.searchTradingSymbols.mockResolvedValue({
        supported: true,
        matches: [{ ...match, tradable: true }],
      });
      renderTrading();
      const section = within(
        (await card()).getByRole("region", { name: "Instruments" }),
      );
      await user.type(
        section.getByRole("textbox", { name: "Resolve symbol at this venue" }),
        match.symbol,
      );
      await user.click(section.getByRole("button", { name: "Resolve" }));
      await user.click(
        await section.findByRole("button", {
          name: new RegExp(match.symbol, "u"),
        }),
      );
      expect(section.getByPlaceholderText("External symbol")).toHaveValue(
        match.symbol,
      );
      expect(section.getByPlaceholderText("Base")).toHaveValue(base);
      expect(section.getByPlaceholderText("Quote")).toHaveValue(quote);
      const submit = section.getByRole("button", { name: "Add instrument" });
      if (add) {
        expect(submit).toBeEnabled();
      } else {
        expect(submit).toBeDisabled();
      }
    },
  );

  it.each(["AAPL", "Apple", "NASDAQ", "US_EQUITY"])(
    "filters found symbols by %s and resets the filter on a new query",
    async (filter) => {
      const user = userEvent.setup();
      api.searchTradingSymbols.mockResolvedValue({
        supported: true,
        matches: [
          {
            symbol: "AAPL",
            name: "Apple",
            exchange: "NASDAQ",
            class: "US_EQUITY",
            tradable: true,
          },
          {
            symbol: "BTC/USD",
            name: "Bitcoin",
            exchange: "CRYPTO",
            class: "crypto",
            tradable: true,
          },
        ],
      });
      renderTrading();
      const section = within(
        (await card()).getByRole("region", { name: "Instruments" }),
      );
      const query = section.getByRole("textbox", {
        name: "Resolve symbol at this venue",
      });
      await user.type(query, "  all  ");
      await user.click(section.getByRole("button", { name: "Resolve" }));
      expect(api.searchTradingSymbols).toHaveBeenCalledExactlyOnceWith(
        "alpaca-1",
        { query: "all" },
      );
      await user.type(
        await section.findByRole("textbox", {
          name: "Filter resolved symbols",
        }),
        filter,
      );
      expect(venueWiring.search.mock.lastCall![0].filterQuery).toBe(filter);
      await user.type(query, "2");
      expect(venueWiring.search.mock.lastCall![0].results).toEqual([]);
      expect(venueWiring.search.mock.lastCall![0].filterQuery).toBe("");
    },
  );

  it.each([false, true])(
    "saves a trimmed symbol with existing=%s in the add row's enabled state, as a feed does",
    async (existing) => {
      const user = userEvent.setup();
      if (existing)
        snapshot.connections = [
          connection({
            instruments: [
              {
                externalSymbol: "BTC/USD",
                baseAsset: "BTC",
                quoteAsset: "USD",
                enabled: false,
              },
            ],
          }),
        ];
      renderTrading();
      const section = within(
        (await card()).getByRole("region", { name: "Instruments" }),
      );
      const symbol = section.getByPlaceholderText("External symbol");
      const base = section.getByPlaceholderText("Base");
      const quote = section.getByPlaceholderText("Quote");
      await user.type(symbol, "  BTC/USD  ");
      await user.type(base, "  BTC  ");
      await user.clear(quote);
      await user.type(quote, "  ");
      expect(
        section.getByRole("button", { name: "Add instrument" }),
      ).toBeDisabled();
      await user.type(quote, "USD  ");
      await user.click(section.getByRole("button", { name: "Add instrument" }));
      expect(api.upsertTradingInstrument).toHaveBeenCalledExactlyOnceWith(
        "alpaca-1",
        {
          externalSymbol: "BTC/USD",
          baseAsset: "BTC",
          quoteAsset: "USD",
          enabled: true,
        },
      );
      await waitFor(() => expect(symbol).toHaveValue(""));
      expect(base).toHaveValue("");
      expect(quote).toHaveValue("USD");
    },
  );

  it.each([false, true])(
    "shows the connection enabled=%s state for an enabled instrument",
    async (enabled) => {
      snapshot.connections = [
        connection({
          enabled,
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
      renderTrading();
      const table = within(
        (await card()).getByRole("table", { name: "Instruments" }),
      );
      expect(
        table.getByText(enabled ? "Enabled" : "Connection disabled", {
          exact: true,
        }),
      ).toBeInTheDocument();
      expect(
        table.queryByText(enabled ? "Connection disabled" : "Enabled", {
          exact: true,
        }),
      ).not.toBeInTheDocument();
    },
  );

  it("does not show a row verification banner for constructor before verification", async () => {
    snapshot.connections = [
      connection({
        instruments: [
          {
            externalSymbol: "constructor",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            enabled: true,
          },
        ],
      }),
    ];
    renderTrading();
    const scope = await card();
    expect(scope.getByText("constructor", { exact: true })).toBeInTheDocument();
    expect(scope.queryByRole("alert")).not.toBeInTheDocument();
    expect(api.verifyTradingSymbol).not.toHaveBeenCalled();
  });

  it("clears a deleted symbol's row verification banner before it is added again", async () => {
    const user = userEvent.setup();
    const instrument = {
      externalSymbol: "AAPL",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      enabled: true,
    };
    snapshot.connections = [connection({ instruments: [instrument] })];
    api.verifyTradingSymbol.mockRejectedValueOnce(
      new ApiError("Venue offline", "upstream", 502),
    );
    api.deleteTradingInstrument.mockImplementationOnce(async (id: string) => {
      updateConnection(id, { instruments: [] });
    });
    api.upsertTradingInstrument.mockImplementationOnce(async (id: string) =>
      updateConnection(id, { instruments: [instrument] }),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    const table = within(form.getByRole("table", { name: "Instruments" }));
    await user.click(table.getByRole("button", { name: "Verify symbol" }));
    expect(await form.findByRole("alert")).toHaveTextContent(
      "AAPL: Venue offline",
    );
    await user.click(table.getByRole("button", { name: "Delete instrument" }));
    const dialog = within(
      screen.getByRole("alertdialog", { name: "Delete instrument?" }),
    );
    await user.click(dialog.getByRole("button", { name: "Delete" }));
    await waitFor(() =>
      expect(
        table.queryByText("AAPL", { exact: true }),
      ).not.toBeInTheDocument(),
    );
    await user.type(form.getByPlaceholderText("External symbol"), "AAPL");
    await user.type(form.getByPlaceholderText("Base"), "AAPL");
    await user.click(form.getByRole("button", { name: "Add instrument" }));
    expect(await table.findByText("AAPL", { exact: true })).toBeInTheDocument();
    expect(form.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("verifies rows independently and shows a failed verdict after a refusal", async () => {
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
          {
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            enabled: false,
          },
        ],
      }),
    ];
    api.verifyTradingSymbol
      .mockResolvedValueOnce({
        supported: true,
        exists: true,
        tradable: false,
        details: "Trading halted",
      })
      .mockRejectedValueOnce(new ApiError("Venue offline", "upstream", 502));
    renderTrading();
    const table = within(
      (await card()).getByRole("table", { name: "Instruments" }),
    );
    const rows = table
      .getAllByRole("row")
      .slice(1)
      .map((row) => within(row));
    await user.click(rows[0].getByRole("button", { name: "Verify symbol" }));
    expect(
      await rows[0].findByText("Symbol exists but is not tradable"),
    ).toBeInTheDocument();
    expect(
      rows[1].queryByText("Symbol exists but is not tradable"),
    ).not.toBeInTheDocument();
    await user.click(rows[1].getByRole("button", { name: "Verify symbol" }));
    expect(await rows[1].findByText("Verification failed")).toBeInTheDocument();
    expect((await card()).getByRole("alert")).toHaveTextContent(
      "Venue offline",
    );
    expect(rows[0].queryByText("Verification failed")).not.toBeInTheDocument();
    expect(api.verifyTradingSymbol.mock.calls).toEqual([
      ["alpaca-1", "BTC/USD"],
      ["alpaca-1", "AAPL"],
    ]);
    expect(venueWiring.verdict).toHaveBeenCalledWith({
      result: { failed: true, message: "Venue offline" },
      unsupportedLabel: "Verification unavailable",
    });
  });

  it("keeps a named row verification error when another row is verified", async () => {
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
          {
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            enabled: true,
          },
        ],
      }),
    ];
    api.verifyTradingSymbol.mockRejectedValueOnce(
      new ApiError("Venue offline", "upstream", 502),
    );
    renderTrading();
    const scope = await card();
    const rows = within(scope.getByRole("table", { name: "Instruments" }))
      .getAllByRole("row")
      .slice(1)
      .map((row) => within(row));
    await user.click(rows[0].getByRole("button", { name: "Verify symbol" }));
    expect(await rows[0].findByText("Verification failed")).toBeInTheDocument();
    await user.click(rows[1].getByRole("button", { name: "Verify symbol" }));
    expect(await rows[1].findByText("Symbol exists")).toBeInTheDocument();
    expect(scope.getByRole("alert")).toHaveTextContent(
      "BTC/USD: Venue offline",
    );
    expect(rows[0].getByText("Verification failed")).toBeInTheDocument();
    await user.click(rows[0].getByRole("button", { name: "Verify symbol" }));
    expect(await rows[0].findByText("Symbol exists")).toBeInTheDocument();
    expect(scope.queryByRole("alert")).not.toBeInTheDocument();
    expect(api.verifyTradingSymbol.mock.calls).toEqual([
      ["alpaca-1", "BTC/USD"],
      ["alpaca-1", "AAPL"],
      ["alpaca-1", "BTC/USD"],
    ]);
  });

  it("hides a row verification error during retry and shows the same failure again", async () => {
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
    let failRetry!: (error: Error) => void;
    api.verifyTradingSymbol
      .mockRejectedValueOnce(new ApiError("Venue offline", "upstream", 502))
      .mockImplementationOnce(
        () =>
          new Promise<TradingSymbolVerification>((_resolve, reject) => {
            failRetry = reject;
          }),
      );
    renderTrading();
    const scope = await card();
    const row = within(
      within(scope.getByRole("table", { name: "Instruments" })).getAllByRole(
        "row",
      )[1],
    );
    await user.click(row.getByRole("button", { name: "Verify symbol" }));
    const firstBanner = await scope.findByRole("alert");
    expect(firstBanner).toHaveTextContent("BTC/USD: Venue offline");

    await user.click(row.getByRole("button", { name: "Verify symbol" }));
    expect(api.verifyTradingSymbol.mock.calls).toEqual([
      ["alpaca-1", "BTC/USD"],
      ["alpaca-1", "BTC/USD"],
    ]);
    expect(row.getByRole("button", { name: "Verify symbol" })).toBeDisabled();
    expect(scope.queryByRole("alert")).not.toBeInTheDocument();
    expect(scope.queryByText("BTC/USD: Venue offline")).not.toBeInTheDocument();

    await act(async () => {
      failRetry(new ApiError("Venue offline", "upstream", 502));
    });
    const retryBanner = await scope.findByRole("alert");
    expect(retryBanner).toHaveTextContent("BTC/USD: Venue offline");
    expect(retryBanner).not.toBe(firstBanner);
  });

  it("keeps both named row verification errors after parallel failures", async () => {
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
          {
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            enabled: true,
          },
        ],
      }),
    ];
    let failFirst!: (error: Error) => void;
    let failSecond!: (error: Error) => void;
    api.verifyTradingSymbol
      .mockImplementationOnce(
        () =>
          new Promise<TradingSymbolVerification>((_resolve, reject) => {
            failFirst = reject;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise<TradingSymbolVerification>((_resolve, reject) => {
            failSecond = reject;
          }),
      );
    renderTrading();
    const scope = await card();
    const rows = within(scope.getByRole("table", { name: "Instruments" }))
      .getAllByRole("row")
      .slice(1)
      .map((row) => within(row));
    await user.click(rows[0].getByRole("button", { name: "Verify symbol" }));
    await user.click(rows[1].getByRole("button", { name: "Verify symbol" }));
    expect(api.verifyTradingSymbol.mock.calls).toEqual([
      ["alpaca-1", "BTC/USD"],
      ["alpaca-1", "AAPL"],
    ]);
    await act(async () => {
      failFirst(new ApiError("First venue refusal", "upstream", 502));
      failSecond(new ApiError("Second venue refusal", "upstream", 502));
    });
    expect(rows[0].getByText("Verification failed")).toBeInTheDocument();
    expect(rows[1].getByText("Verification failed")).toBeInTheDocument();
    expect(
      scope.getAllByRole("alert").map((alert) => alert.textContent),
    ).toEqual(["BTC/USD: First venue refusal", "AAPL: Second venue refusal"]);
  });

  it("keeps row A disabled while row B verifies and finishes", async () => {
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
          {
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            enabled: true,
          },
        ],
      }),
    ];
    let finishA!: (result: TradingSymbolVerification) => void;
    let finishB!: (result: TradingSymbolVerification) => void;
    api.verifyTradingSymbol
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finishA = resolve;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finishB = resolve;
          }),
      );
    renderTrading();
    const rows = within(
      (await card()).getByRole("table", { name: "Instruments" }),
    )
      .getAllByRole("row")
      .slice(1)
      .map((row) => within(row));
    const a = rows[0].getByRole("button", { name: "Verify symbol" });
    const b = rows[1].getByRole("button", { name: "Verify symbol" });
    await user.click(a);
    await user.click(b);
    expect(a).toBeDisabled();
    expect(b).toBeDisabled();
    await act(async () => {
      finishB({ supported: false, exists: false, tradable: false });
    });
    expect(rows[1].getByText("Verification unavailable")).toBeInTheDocument();
    expect(a).toBeDisabled();
    expect(b).toBeEnabled();
    await act(async () => {
      finishA({ supported: true, exists: true, tradable: true });
    });
    expect(a).toBeEnabled();
    expect(rows[0].getByText("Symbol exists")).toBeInTheDocument();
  });

  it("hides row verification for a provider that cannot verify symbols", async () => {
    snapshot.providers = [{ ...provider, verifiesSymbols: false }];
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
    renderTrading();
    const table = within(
      (await card()).getByRole("table", { name: "Instruments" }),
    );
    expect(table.getAllByText("BTC/USD")).toHaveLength(2);
    expect(
      table.queryByRole("button", { name: "Verify symbol" }),
    ).not.toBeInTheDocument();
  });

  it.each(["Base", "Quote"])(
    "clears the %s asset with the shared row control",
    async (field) => {
      const user = userEvent.setup();
      renderTrading();
      const section = within(
        (await card()).getByRole("region", { name: "Instruments" }),
      );
      const input = section.getByPlaceholderText(field);
      // The add row starts quoted in USD; empty it so one field is clearable.
      await user.clear(section.getByPlaceholderText("Quote"));
      await user.type(input, "USD");
      await user.click(section.getByRole("button", { name: "Clear field" }));
      expect(input).toHaveValue("");
      expect(api.upsertTradingInstrument).not.toHaveBeenCalled();
    },
  );

  it("passes card mutation busy state to every instrument block", async () => {
    const user = userEvent.setup();
    let finishSave!: (snapshot: TradingSnapshot) => void;
    api.upsertTradingInstrument.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishSave = resolve;
        }),
    );
    renderTrading();
    const scope = await card();
    await user.type(scope.getByPlaceholderText("External symbol"), "BTC/USD");
    await user.type(scope.getByPlaceholderText("Base"), "BTC");
    await user.click(scope.getByRole("button", { name: "Add instrument" }));
    for (const block of [
      venueWiring.search,
      venueWiring.draft,
      venueWiring.table,
      venueWiring.deletion,
    ])
      expect(block.mock.lastCall![0].busy).toBe(true);
    expect(scope.getByRole("button", { name: "Enable" })).toBeDisabled();
    await act(async () => {
      finishSave(snapshot);
    });
    for (const block of [
      venueWiring.search,
      venueWiring.draft,
      venueWiring.table,
      venueWiring.deletion,
    ])
      expect(block.mock.lastCall![0].busy).toBe(false);
  });

  it("keeps search, draft and verification errors local to their connection", async () => {
    const user = userEvent.setup();
    snapshot.connections.push(
      connection({ id: "alpaca-2", label: "Second connection" }),
    );
    api.searchTradingSymbols.mockResolvedValue({
      supported: true,
      matches: [{ symbol: "BTC/USD", tradable: true }],
    });
    api.verifyTradingSymbol.mockRejectedValue(
      new ApiError("First card refused", "upstream", 502),
    );
    renderTrading();
    const first = await card();
    const second = await card("Second connection", false);
    await user.type(
      first.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "BTC",
    );
    await user.click(first.getByRole("button", { name: "Resolve" }));
    await user.click(await first.findByRole("button", { name: "BTC/USD" }));
    await user.click(first.getByRole("button", { name: "Verify symbol" }));
    expect(await first.findByText("First card refused")).toBeInTheDocument();
    await card("Second connection");
    expect(
      second.getByRole("textbox", { name: "Resolve symbol at this venue" }),
    ).toHaveValue("");
    expect(second.getByPlaceholderText("External symbol")).toHaveValue("");
    expect(second.queryByText("First card refused")).not.toBeInTheDocument();
    api.searchTradingSymbols.mockRejectedValueOnce(
      new ApiError("Second search refused", "upstream", 502),
    );
    await user.type(
      second.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "AAPL",
    );
    await user.click(second.getByRole("button", { name: "Resolve" }));
    expect(
      await second.findByText("Second search refused"),
    ).toBeInTheDocument();
    expect(
      second.queryByText("No matching symbols at this venue"),
    ).not.toBeInTheDocument();
    expect(first.queryByText("Second search refused")).not.toBeInTheDocument();
    await card();
    expect(first.getByRole("button", { name: "BTC/USD" })).toBeInTheDocument();
    await card("Second connection");
    api.verifyTradingSymbol.mockRejectedValueOnce(
      new ApiError("Second card refused", "upstream", 502),
    );
    await user.type(second.getByPlaceholderText("External symbol"), "AAPL");
    await user.click(second.getByRole("button", { name: "Verify symbol" }));
    expect(await second.findByText("Second card refused")).toBeInTheDocument();
    expect(first.queryByText("Second card refused")).not.toBeInTheDocument();
    await card();
    await user.type(first.getByPlaceholderText("External symbol"), "2");
    expect(first.queryByText("First card refused")).not.toBeInTheDocument();
    expect(api.searchTradingSymbols.mock.calls).toEqual([
      ["alpaca-1", { query: "BTC" }],
      ["alpaca-2", { query: "AAPL" }],
    ]);
    expect(api.verifyTradingSymbol.mock.calls).toEqual([
      ["alpaca-1", "BTC/USD"],
      ["alpaca-2", "AAPL"],
    ]);
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
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    expect(form.getByRole("button", { name: "Resolve" })).toBeDisabled();
    await user.type(
      form.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "   ",
    );
    expect(form.getByRole("button", { name: "Resolve" })).toBeDisabled();
    await user.clear(
      form.getByRole("textbox", { name: "Resolve symbol at this venue" }),
    );
    await user.type(
      form.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "BTC",
    );
    await user.click(form.getByRole("button", { name: "Resolve" }));
    expect(api.searchTradingSymbols).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { query: "BTC" },
    );
    expect(await form.findByText("Bitcoin USD")).toBeInTheDocument();
    expect(form.getByText(/CRYPTO/)).toBeInTheDocument();
    expect(form.getByText("Apple", { exact: true })).toBeInTheDocument();
    expect(form.getByText(/NASDAQ/)).toBeInTheDocument();
    await user.click(form.getByRole("button", { name: /^BTC\/USD(?: ·|$)/ }));
    expect(form.getByPlaceholderText("External symbol")).toHaveValue("BTC/USD");
    expect(form.getByPlaceholderText("Base")).toHaveValue("BTC");
    expect(form.getByPlaceholderText("Quote")).toHaveValue("USD");
    const callsBeforeSave = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Add instrument" }));
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
    await user.click(form.getByRole("button", { name: /^AAPL ·/ }));
    expect(form.getByPlaceholderText("External symbol")).toHaveValue("AAPL");
    expect(form.getByPlaceholderText("Base")).toHaveValue("AAPL");
    expect(form.getByPlaceholderText("Quote")).toHaveValue("");
    expect(form.getByRole("button", { name: "Add instrument" })).toBeDisabled();
    await user.clear(form.getByPlaceholderText("External symbol"));
    await user.type(form.getByPlaceholderText("External symbol"), "AAPL2");
    await user.clear(form.getByPlaceholderText("Base"));
    await user.type(form.getByPlaceholderText("Base"), "AAPL2");
    await user.type(form.getByPlaceholderText("Quote"), "USD");
    expect(form.queryByRole("checkbox")).not.toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "Add instrument" }));
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
          enabled: true,
        },
      ],
    ]);
  });

  it.each([
    {
      supported: false,
    },
    {
      supported: true,
    },
  ])(
    "wires an empty resolve result with supported=$supported",
    async ({ supported }) => {
      const user = userEvent.setup();
      api.searchTradingSymbols.mockResolvedValue({ supported, matches: [] });
      renderTrading();
      const scope = await card();
      await user.type(
        scope.getByRole("textbox", { name: "Resolve symbol at this venue" }),
        "ETH",
      );
      await user.click(scope.getByRole("button", { name: "Resolve" }));
      await waitFor(() =>
        expect(venueWiring.search.mock.lastCall![0]).toEqual(
          expect.objectContaining({
            searching: false,
            searched: supported,
            notice: supported ? undefined : "This venue cannot resolve symbols",
          }),
        ),
      );
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
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    await user.type(form.getByPlaceholderText("External symbol"), "BTC/USD");
    await user.type(form.getByPlaceholderText("Base"), "BTC");
    expect(form.getByRole("button", { name: "Add instrument" })).toBeEnabled();
    await user.type(
      form.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "ETH{Enter}",
    );
    expect(api.searchTradingSymbols).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { query: "ETH" },
    );
    expect(
      await form.findByText("No matching symbols at this venue"),
    ).toBeInTheDocument();
    expect(api.upsertTradingInstrument).not.toHaveBeenCalled();
    expect(form.getByPlaceholderText("External symbol")).toHaveValue("BTC/USD");
  });

  it.each([
    {
      supported: true,
      exists: true,
      tradable: true,
      label: "Symbol exists",
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
  ])("passes verify-symbol outcome: $label", async ({ label, ...result }) => {
    const user = userEvent.setup();
    api.verifyTradingSymbol.mockResolvedValue({
      ...result,
      details: "Venue catalogue details",
    });
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    await user.type(form.getByPlaceholderText("External symbol"), "AAPL");
    const callsBeforeVerify = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Verify symbol" }));
    expect(
      within(form.getByRole("status")).getByText(label),
    ).toBeInTheDocument();
    expect(venueWiring.verdict, label).toHaveBeenCalledWith({
      result: { ...result, details: "Venue catalogue details" },
      unsupportedLabel: "Verification unavailable",
    });
    expect(api.verifyTradingSymbol).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "AAPL",
    );
    expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeVerify);
    await user.type(form.getByPlaceholderText("External symbol"), "2");
    expect(form.queryByRole("status")).toBeNull();
    expect(venueWiring.verdict).toHaveBeenLastCalledWith({
      result: null,
      unsupportedLabel: "Verification unavailable",
    });
    await user.keyboard("{Backspace}");
    expect(form.getByPlaceholderText("External symbol")).toHaveValue("AAPL");
    expect(venueWiring.verdict).toHaveBeenLastCalledWith({
      result: null,
      unsupportedLabel: "Verification unavailable",
    });
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
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    await user.type(
      form.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "BTC",
    );
    await user.click(form.getByRole("button", { name: "Resolve" }));
    await user.click(
      await form.findByRole("button", { name: /^BTC\/USD(?: ·|$)/ }),
    );
    await user.click(form.getByRole("button", { name: "Verify symbol" }));
    expect(api.verifyTradingSymbol).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      "BTC/USD",
    );
    await user.click(form.getByRole("button", { name: /^BTC\/USDT(?: ·|$)/ }));
    expect(form.getByPlaceholderText("External symbol")).toHaveValue(
      "BTC/USDT",
    );
    await act(async () => {
      finishVerify({ supported: true, exists: true, tradable: true });
    });
    await waitFor(() =>
      expect(form.getByRole("button", { name: "Verify symbol" })).toBeEnabled(),
    );
    expect(venueWiring.verdict).toHaveBeenLastCalledWith({
      result: null,
      unsupportedLabel: "Verification unavailable",
    });
    expect(form.queryByText("Symbol exists")).not.toBeInTheDocument();

    let failVerify!: (error: Error) => void;
    api.verifyTradingSymbol.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          failVerify = reject;
        }),
    );
    await user.click(form.getByRole("button", { name: /^BTC\/USD(?: ·|$)/ }));
    await user.click(form.getByRole("button", { name: "Verify symbol" }));
    await user.click(form.getByRole("button", { name: /^BTC\/USDT(?: ·|$)/ }));
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
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    await user.type(
      form.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "BTC",
    );
    await user.click(form.getByRole("button", { name: "Resolve" }));
    await user.click(
      await form.findByRole("button", { name: /^BTC\/USD(?: ·|$)/ }),
    );
    await user.click(form.getByRole("button", { name: "Verify symbol" }));
    expect(await form.findByText("BTC/USD unavailable")).toBeInTheDocument();
    expect(venueWiring.verdict).toHaveBeenLastCalledWith({
      result: { failed: true },
      unsupportedLabel: "Verification unavailable",
    });
    await user.click(form.getByRole("button", { name: /^BTC\/USDT(?: ·|$)/ }));
    expect(form.queryByText("BTC/USD unavailable")).not.toBeInTheDocument();
    await user.click(form.getByRole("button", { name: /^BTC\/USD(?: ·|$)/ }));
    expect(form.queryByText("BTC/USD unavailable")).not.toBeInTheDocument();
  });

  it("shows instrument save errors and keeps the draft for correction", async () => {
    const user = userEvent.setup();
    api.upsertTradingInstrument.mockRejectedValue(
      new ApiError("This pair is already mapped", "conflict", 409),
    );
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    await user.type(form.getByPlaceholderText("External symbol"), "AAPL");
    await user.type(form.getByPlaceholderText("Base"), "AAPL");
    const callsBeforeSave = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Add instrument" }));
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
    expect(form.getByPlaceholderText("External symbol")).toHaveValue("AAPL");
    expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeSave);
  });

  // As on a market-data feed, the server creates an asset Officer does not
  // know yet, so the panel saves once and creates nothing itself.
  it("adds an instrument with an unknown asset in one save, as a feed does", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    const quote = form.getByPlaceholderText("Quote");
    expect(quote).toHaveValue("USD");
    await user.type(form.getByPlaceholderText("External symbol"), "BTC/USDT");
    await user.type(form.getByPlaceholderText("Base"), "BTC");
    await user.clear(quote);
    await user.type(quote, "USDT");
    await user.click(form.getByRole("button", { name: "Add instrument" }));
    await waitFor(() =>
      expect(form.getByPlaceholderText("External symbol")).toHaveValue(""),
    );
    expect(api.upsertTradingInstrument).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      {
        externalSymbol: "BTC/USDT",
        baseAsset: "BTC",
        quoteAsset: "USDT",
        enabled: true,
      },
    );
    expect(api.createAsset).not.toHaveBeenCalled();
    expect(quote).toHaveValue("USD");
    expect(form.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the add button disabled for an asset code Officer cannot accept, as a feed does", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    await user.type(form.getByPlaceholderText("External symbol"), "BAD");
    await user.type(form.getByPlaceholderText("Base"), "BAD ASSET");
    const add = form.getByRole("button", { name: "Add instrument" });
    expect(add).toBeDisabled();
    await user.clear(form.getByPlaceholderText("Base"));
    await user.type(form.getByPlaceholderText("Base"), "B".repeat(33));
    expect(add).toBeDisabled();
    await user.clear(form.getByPlaceholderText("Base"));
    await user.type(form.getByPlaceholderText("Base"), "BAD");
    expect(add).toBeEnabled();
    expect(api.upsertTradingInstrument).not.toHaveBeenCalled();
  });

  it("adds a disabled instrument when the add row is switched off, as a feed does", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    await user.type(form.getByPlaceholderText("External symbol"), "AAPL");
    await user.type(form.getByPlaceholderText("Base"), "AAPL");
    await user.click(form.getByRole("button", { name: "Enabled" }));
    expect(form.getByRole("button", { name: "Disabled" })).toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "Add instrument" }));
    expect(api.upsertTradingInstrument).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      {
        externalSymbol: "AAPL",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        enabled: false,
      },
    );
    await waitFor(() =>
      expect(form.getByRole("button", { name: "Enabled" })).toBeInTheDocument(),
    );
  });

  it("shows a refused instrument toggle as the server message", async () => {
    const user = userEvent.setup();
    snapshot = {
      ...snapshot,
      connections: [
        connection({
          instruments: [
            {
              externalSymbol: "BTC/USDT",
              baseAsset: "BTC",
              quoteAsset: "USDT",
              enabled: true,
            },
          ],
        }),
      ],
    };
    api.upsertTradingInstrument.mockRejectedValue(
      new ApiError("Connection is busy", "conflict", 409),
    );
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Disable" }));
    expect(await scope.findByText("Connection is busy")).toBeInTheDocument();
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
    await user.click(table.getByRole("button", { name: "Delete instrument" }));
    expect(api.deleteTradingInstrument).not.toHaveBeenCalled();
    let dialog = within(
      screen.getByRole("alertdialog", { name: "Delete instrument?" }),
    );
    expect(
      dialog.getByText("BTC/USD will be removed from Alpaca paper."),
    ).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(api.deleteTradingInstrument).not.toHaveBeenCalled();
    await user.click(table.getByRole("button", { name: "Delete instrument" }));
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
      expect(
        screen.queryByRole("alertdialog", { name: "Delete instrument?" }),
      ).not.toBeInTheDocument(),
    );
    expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
      callsBeforeDelete,
    );
  });

  it.each(["Escape", "Cancel", "outside click"])(
    "keeps the delete dialog open on %s while pending",
    async (close) => {
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
      let finishDelete!: (result: TradingSnapshot) => void;
      api.deleteTradingInstrument.mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finishDelete = resolve;
          }),
      );
      renderTrading();
      await user.click(
        (await card()).getByRole("button", { name: "Delete instrument" }),
      );
      const dialog = screen.getByRole("alertdialog", {
        name: "Delete instrument?",
      });
      await user.click(within(dialog).getByRole("button", { name: "Delete" }));
      expect(
        within(dialog).getByRole("button", { name: "Delete" }),
      ).toBeDisabled();
      if (close === "Escape") await user.keyboard("{Escape}");
      else if (close === "Cancel")
        await user.click(
          within(dialog).getByRole("button", { name: "Cancel" }),
        );
      else
        fireEvent.pointerDown(document.body, {
          button: 0,
          pointerType: "mouse",
        });
      expect(
        screen.getByRole("alertdialog", { name: "Delete instrument?" }),
      ).toBe(dialog);
      await act(async () => {
        finishDelete(snapshot);
      });
      await waitFor(() =>
        expect(
          screen.queryByRole("alertdialog", { name: "Delete instrument?" }),
        ).not.toBeInTheDocument(),
      );
    },
  );

  it("uses asset and account code suggestions with starts_with filters", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    await user.type(scope.getByPlaceholderText("Base"), "BT");
    await user.click(await scope.findByRole("option", { name: "BTC" }));
    expect(scope.getByPlaceholderText("Base")).toHaveValue("BTC");
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
      if (code === "USD") return [{ code: "USD", title: "", assetClass: "" }];
      throw new Error(`Unexpected asset query: ${code}`);
    });
    renderTrading();
    const scope = await card();
    const base = scope.getByPlaceholderText("Base");
    const quote = scope.getByPlaceholderText("Quote");
    await user.type(base, "U");
    expect(
      await scope.findByRole("option", { name: "USD" }),
    ).toBeInTheDocument();
    await user.clear(quote);
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

  it.each(["Base", "Quote"])(
    "shows an asset lookup failure and clears it when %s empties",
    async (field) => {
      const user = userEvent.setup();
      const errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
      try {
        api.fetchAssets.mockRejectedValue(new Error("Asset lookup offline"));
        renderTrading();
        const scope = await card();
        const input = scope.getByPlaceholderText(field);
        // The add row starts quoted in USD; empty it so one field looks up.
        await user.clear(scope.getByPlaceholderText("Quote"));
        await user.type(input, "BT");
        await waitFor(() =>
          expect(venueWiring.draft.mock.lastCall![0]).toEqual(
            expect.objectContaining({
              baseSuggestionsFailed: field === "Base",
              quoteSuggestionsFailed: field === "Quote",
            }),
          ),
        );
        expect(scope.queryByRole("alert")).not.toBeInTheDocument();
        await user.clear(input);
        await waitFor(() =>
          expect(venueWiring.draft.mock.lastCall![0]).toEqual(
            expect.objectContaining({
              baseSuggestionsFailed: false,
              quoteSuggestionsFailed: false,
            }),
          ),
        );
      } finally {
        errorLog.mockRestore();
      }
    },
  );

  it.each([
    ["Account", "status", "Account suggestions could not be loaded."],
    ["Fee account", "alert", "Account suggestions offline"],
  ] as const)(
    "shows an account suggestion failure and clears it when %s empties",
    async (field, role, message) => {
      const user = userEvent.setup();
      const errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
      try {
        api.fetchAccounts.mockRejectedValue(
          new ApiError("Account suggestions offline", "upstream", 502),
        );
        renderTrading();
        const scope = await card();
        const input = scope.getByRole("combobox", { name: field });
        await user.type(input, "al");
        expect(await scope.findByRole(role)).toHaveTextContent(message);
        await user.type(input, " ");
        expect(input).toHaveValue("al ");
        expect(scope.getByRole(role)).toHaveTextContent(message);
        await user.clear(input);
        await waitFor(() =>
          expect(scope.queryByRole(role)).not.toBeInTheDocument(),
        );
      } finally {
        errorLog.mockRestore();
      }
    },
  );

  it("shows a group suggestion failure and clears it when the field empties", async () => {
    const user = userEvent.setup();
    const errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      api.fetchGroups.mockRejectedValue(
        new ApiError("Group suggestions offline", "upstream", 502),
      );
      renderTrading();
      const scope = await card();
      const form = within(scope.getByRole("form", { name: "Grant access" }));
      await user.click(form.getByRole("button", { name: "Group" }));
      const input = form.getByRole("combobox", { name: "Group" });
      await user.type(input, "de");
      expect(await form.findByRole("status")).toHaveTextContent(
        "Group suggestions could not be loaded.",
      );
      expect(api.fetchAccounts).not.toHaveBeenCalled();
      await user.clear(input);
      await waitFor(() =>
        expect(scope.queryByRole("status")).not.toBeInTheDocument(),
      );
    } finally {
      errorLog.mockRestore();
    }
  });

  it("takes access suggestions from accounts or groups by the chosen kind", async () => {
    const user = userEvent.setup();
    accounts = ["desk-trader"];
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    expect(form.getByRole("button", { name: "Account" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await user.type(form.getByRole("combobox", { name: "Account" }), "de");
    expect(
      await form.findByRole("option", { name: "desk-trader" }),
    ).toBeInTheDocument();
    expect(api.fetchGroups).not.toHaveBeenCalled();
    await user.click(form.getByRole("button", { name: "Group" }));
    const input = form.getByRole("combobox", { name: "Group" });
    expect(input).toHaveValue("de");
    await user.click(input);
    expect(
      await form.findByRole("option", { name: "desk" }),
    ).toBeInTheDocument();
    expect(
      form.queryByRole("option", { name: "desk-trader" }),
    ).not.toBeInTheDocument();
    expect(api.fetchGroups).toHaveBeenCalledExactlyOnceWith(
      {
        code: "de",
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      expect.any(AbortSignal),
    );
    expect(api.fetchAccounts).toHaveBeenCalledExactlyOnceWith(
      {
        code: "de",
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      expect.any(AbortSignal),
    );
  });

  it("passes the connection's grants and capabilities to the access list", async () => {
    const access = [
      { account: "", accountGroup: "desk", venueAccount: "", group: "" },
      { account: "alpha", accountGroup: "", venueAccount: "", group: "desk" },
    ];
    snapshot.connections = [connection({ access })];
    renderTrading();
    await card();
    expect(venueWiring.access.mock.lastCall![0]).toEqual(
      expect.objectContaining({
        grants: access,
        showVenueAccount: false,
        busy: false,
      }),
    );
  });

  it("grants access to a group by its code", async () => {
    const user = userEvent.setup();
    snapshot.connections = [connection({ feeAccount: "fees" })];
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    await user.click(form.getByRole("button", { name: "Group" }));
    expect(
      form.queryByRole("button", { name: "Set fee account" }),
    ).not.toBeInTheDocument();
    expect(form.getByRole("button", { name: "Grant access" })).toBeDisabled();
    await user.type(form.getByRole("combobox", { name: "Group" }), " desk ");
    const callsBeforeGrant = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Grant access" }));
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      accountGroup: "desk",
    });
    await waitFor(() =>
      expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
        callsBeforeGrant,
      ),
    );
    expect(form.getByRole("combobox", { name: "Group" })).toHaveValue("");
  });

  it("states the group reason before a code is typed and focuses that card's fee field", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection(),
      connection({ id: "alpaca-2", label: "Other paper" }),
    ];
    renderTrading();
    const scope = await card();
    const other = await card("Other paper", false);
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    const reason =
      "Set a fee account first. A group can hold several accounts, and Alpaca charges fees that are not tied to orders; Officer needs one account to book them on.";
    expect(form.queryByText(reason)).not.toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "Group" }));
    expect(form.getByRole("combobox", { name: "Group" })).toHaveValue("");
    expect(form.getByText(reason)).toBeInTheDocument();
    await user.type(form.getByRole("combobox", { name: "Group" }), "desk");
    expect(form.getByRole("button", { name: "Grant access" })).toBeDisabled();
    await user.click(form.getByRole("button", { name: "Set fee account" }));
    expect(scope.getByRole("combobox", { name: "Fee account" })).toHaveFocus();
    expect(
      other.getByLabelText("Fee account", { selector: "input" }),
    ).not.toHaveFocus();
    await user.click(form.getByRole("button", { name: "Account" }));
    expect(form.queryByText(reason)).not.toBeInTheDocument();
    expect(api.grantTradingAccess).not.toHaveBeenCalled();
  });

  it("grants access without an Alpaca venue-account field and reloads", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    expect(
      form.queryByRole("textbox", { name: "Venue account" }),
    ).not.toBeInTheDocument();
    await user.type(form.getByRole("combobox", { name: "Account" }), "alpha");
    const callsBeforeGrant = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Grant access" }));
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
      connection({
        access: [
          { account: "alpha", accountGroup: "", venueAccount: "", group: "" },
        ],
      }),
      connection({ id: "alpaca-2", label: "Other paper" }),
    ];
    renderTrading();
    const scope = await card();
    const other = await card("Other paper", false);
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "beta");
    expect(form.getByRole("button", { name: "Grant access" })).toBeDisabled();
    expect(
      form.getByText(
        "Set a fee account first. Alpaca charges fees that are not tied to orders; with several accounts on one connection Officer needs one account to book them on.",
      ),
    ).toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "Grant access" }));
    expect(api.grantTradingAccess).not.toHaveBeenCalled();
    await user.click(form.getByRole("button", { name: "Set fee account" }));
    expect(scope.getByRole("combobox", { name: "Fee account" })).toHaveFocus();
    expect(
      other.getByLabelText("Fee account", { selector: "input" }),
    ).not.toHaveFocus();
    expect(window.HTMLElement.prototype.scrollIntoView).toHaveBeenCalled();
    const fee = within(scope.getByRole("form", { name: "Fee account" }));
    await user.type(fee.getByRole("combobox", { name: "Fee account" }), "fees");
    await user.click(fee.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(form.getByRole("button", { name: "Grant access" })).toBeEnabled(),
    );
    await user.click(form.getByRole("button", { name: "Grant access" }));
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
      connection({
        access: [
          { account: "alpha", accountGroup: "", venueAccount: "", group: "" },
        ],
      }),
    ];
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "alpha");
    expect(
      form.queryByRole("button", { name: "Set fee account" }),
    ).not.toBeInTheDocument();
    await user.click(form.getByRole("button", { name: "Grant access" }));
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
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "beta");
    const callsBeforeGrant = api.fetchTrading.mock.calls.length;
    await user.click(form.getByRole("button", { name: "Grant access" }));
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
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "alpha");
    await user.click(form.getByRole("button", { name: "Grant access" }));
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
          { account: "alpha", accountGroup: "", venueAccount: "", group: "" },
          { account: "beta", accountGroup: "", venueAccount: "", group: "" },
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
      connection({
        access: [
          { account: "alpha", accountGroup: "", venueAccount: "", group: "" },
        ],
      }),
    ];
    renderTrading();
    const scope = await card();
    const table = within(scope.getByRole("table", { name: "Access grants" }));
    await user.click(
      table.getByRole("button", { name: "Revoke access for account alpha" }),
    );
    expect(api.revokeTradingAccess).not.toHaveBeenCalled();
    let dialog = within(
      screen.getByRole("alertdialog", { name: "Revoke account access?" }),
    );
    expect(
      dialog.getByText("Revoke access of account alpha to Alpaca paper?"),
    ).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(api.revokeTradingAccess).not.toHaveBeenCalled();
    await user.click(
      table.getByRole("button", { name: "Revoke access for account alpha" }),
    );
    dialog = within(
      screen.getByRole("alertdialog", { name: "Revoke account access?" }),
    );
    const callsBeforeRevoke = api.fetchTrading.mock.calls.length;
    await user.click(dialog.getByRole("button", { name: "Revoke access" }));
    expect(api.revokeTradingAccess).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { account: "alpha" },
    );
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(
      callsBeforeRevoke,
    );
  });

  it("confirms a group revoke and revokes the group, not a same-coded account", async () => {
    const user = userEvent.setup();
    snapshot.connections = [
      connection({
        feeAccount: "fees",
        access: [
          { account: "", accountGroup: "desk", venueAccount: "", group: "" },
          { account: "desk", accountGroup: "", venueAccount: "", group: "" },
        ],
      }),
    ];
    renderTrading();
    const scope = await card();
    const table = within(scope.getByRole("table", { name: "Access grants" }));
    await user.click(
      table.getByRole("button", { name: "Revoke access for group desk" }),
    );
    const dialog = within(
      screen.getByRole("alertdialog", { name: "Revoke group access?" }),
    );
    expect(
      dialog.getByText(
        "Revoke access of group desk to Alpaca paper? Accounts in the group keep access only if it is granted to them directly.",
      ),
    ).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "Revoke access" }));
    expect(api.revokeTradingAccess).toHaveBeenCalledExactlyOnceWith(
      "alpaca-1",
      { accountGroup: "desk" },
    );
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
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
        { name: "Delete instrument" },
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
      connection({
        access: [
          { account: "alpha", accountGroup: "", venueAccount: "", group: "" },
        ],
      }),
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
    await user.click(
      scope.getByRole("button", { name: "Revoke access for account alpha" }),
    );
    const dialog = within(
      screen.getByRole("alertdialog", { name: "Revoke account access?" }),
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
      scope.getByRole("form", { name: "Grant access" }),
    );
    await user.type(
      accountForm.getByRole("combobox", { name: "Account" }),
      "beta",
    );
    await user.click(accountForm.getByRole("button", { name: "Grant access" }));
    expect(await scope.findByText("Choose a fee ledger")).toBeInTheDocument();
    const instrumentForm = within(
      scope.getByRole("region", { name: "Instruments" }),
    );
    await user.type(
      instrumentForm.getByRole("textbox", {
        name: "Resolve symbol at this venue",
      }),
      "ETH",
    );
    await user.click(instrumentForm.getByRole("button", { name: "Resolve" }));
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
        access: [
          { account: "alpha", accountGroup: "", venueAccount: "", group: "" },
        ],
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
    const second = await card("Other paper", false);
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
      second.getByRole("button", { name: "Check connection", hidden: true }),
    ).toBeEnabled();
    expect(second.getByRole("button", { name: "Edit settings" })).toBeEnabled();
    await act(async () => {
      finishCheck({ venueIdentity: "first-checked", venueStatus: "ACTIVE" });
    });
    expect(await first.findByRole("status")).toHaveTextContent(
      "Venue account first-checked · status ACTIVE",
    );
    expect(second.queryByRole("status")).not.toBeInTheDocument();
    expect(
      within(first.getByRole("list", { name: "Setup checklist" })).getAllByText(
        "Done",
        { exact: true },
      ),
    ).toHaveLength(1);
    expect(
      within(
        second.getByRole("list", { name: "Setup checklist", hidden: true }),
      ).queryByText("Done", { exact: true }),
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
        access: [
          {
            account: "alpha",
            accountGroup: "",
            venueAccount: "venue-subaccount",
            group: "",
          },
        ],
      }),
    ];
    renderTrading();
    const scope = await card();
    expect(venueWiring.access.mock.lastCall![0]).toEqual(
      expect.objectContaining({
        grants: snapshot.connections[0].access,
        showVenueAccount: true,
      }),
    );
    expect(
      scope.queryByRole("heading", { name: "Fee account" }),
    ).not.toBeInTheDocument();
    expect(
      scope.queryByRole("textbox", { name: "Resolve symbol at this venue" }),
    ).not.toBeInTheDocument();
    expect(
      scope.queryByRole("button", { name: "Verify symbol" }),
    ).not.toBeInTheDocument();
    const form = within(scope.getByRole("form", { name: "Grant access" }));
    await user.type(form.getByRole("combobox", { name: "Account" }), "beta");
    await user.type(
      form.getByRole("textbox", { name: "Venue account" }),
      "discard",
    );
    await user.click(form.getAllByRole("button", { name: "Clear field" })[1]);
    expect(form.getByRole("textbox", { name: "Venue account" })).toHaveValue(
      "",
    );
    await user.type(
      form.getByRole("textbox", { name: "Venue account" }),
      "venue-target",
    );
    await user.click(form.getByRole("button", { name: "Grant access" }));
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      account: "beta",
      venueAccount: "venue-target",
    });
  });
});

describe("Trading connections while accounts are hidden", () => {
  const ownGrant = {
    account: "main",
    accountGroup: "",
    venueAccount: "",
    group: "desk",
  };
  const deskGrant = {
    account: "",
    accountGroup: "desk",
    venueAccount: "",
    group: "",
  };
  const inheritedNote =
    "Inherited from the account's group. Group access is managed in multi-account mode.";
  const venueAccounts = { ...provider, venueAccounts: true };

  function venueAccountOf(scope: ReturnType<typeof within>) {
    return within(scope.getByRole("region", { name: "Venue account" }));
  }

  function checklistOf(scope: ReturnType<typeof within>) {
    return within(scope.getByRole("list", { name: "Setup checklist" }))
      .getAllByRole("listitem")
      .map((item) => item.querySelector("button")?.textContent);
  }

  beforeEach(() => {
    setAccountScope({
      accounts: false,
      account: "main",
      accountPnlReset: false,
    });
  });

  it.each([[], [ownGrant], [deskGrant]])(
    "shows no access section, grant or revoke and no access step: the server grants the default account (%j)",
    async (...access) => {
      snapshot.connections = [connection({ access })];
      renderTrading();
      const scope = await card();
      expect(
        scope.queryByRole("region", { name: "Account access" }),
      ).not.toBeInTheDocument();
      expect(
        scope.queryByRole("region", { name: "Venue account" }),
      ).not.toBeInTheDocument();
      expect(
        scope.queryByRole("button", { name: "Grant access" }),
      ).not.toBeInTheDocument();
      expect(
        scope.queryByRole("button", { name: "Revoke access" }),
      ).not.toBeInTheDocument();
      expect(checklistOf(scope)).toEqual([
        "Check connection",
        "Instruments",
        "Enable",
      ]);
      expect(venueWiring.access).not.toHaveBeenCalled();
      expect(
        scope.queryByRole("heading", { name: "Fee account" }),
      ).not.toBeInTheDocument();
      expect(api.fetchAccounts).not.toHaveBeenCalled();
      expect(api.fetchGroups).not.toHaveBeenCalled();
      expect(api.fetchAccountState).not.toHaveBeenCalled();
    },
  );

  it("enables and disables the connection without any access setup", async () => {
    const user = userEvent.setup();
    api.setTradingConnectionEnabled.mockImplementation(
      async (id: string, enabled: boolean) => {
        updateConnection(id, { enabled });
        return enabled;
      },
    );
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Enable" }));
    expect(api.setTradingConnectionEnabled).toHaveBeenLastCalledWith(
      "alpaca-1",
      true,
    );
    await user.click(await scope.findByRole("button", { name: "Disable" }));
    expect(api.setTradingConnectionEnabled).toHaveBeenLastCalledWith(
      "alpaca-1",
      false,
    );
    expect(await scope.findByRole("button", { name: "Enable" })).toBeVisible();
    expect(api.grantTradingAccess).not.toHaveBeenCalled();
  });

  it("shows a fee_account_required refusal of the server's grant without a fee-account action", async () => {
    const user = userEvent.setup();
    api.setTradingConnectionEnabled.mockRejectedValue(
      new ApiError("Choose a fee ledger", "fee_account_required", 409),
    );
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Enable" }));
    expect(await scope.findByText("Choose a fee ledger")).toBeInTheDocument();
    expect(
      scope.queryByRole("button", { name: "Set fee account" }),
    ).not.toBeInTheDocument();
  });

  it("saves the trimmed venue account the provider binds as the default account's grant and shows it", async () => {
    const user = userEvent.setup();
    snapshot.providers = [venueAccounts];
    api.grantTradingAccess.mockImplementation(async () =>
      updateConnection("alpaca-1", {
        access: [{ ...ownGrant, venueAccount: "venue-target" }],
      }),
    );
    renderTrading();
    const scope = await card();
    const section = venueAccountOf(scope);
    expect(checklistOf(scope)).toEqual([
      "Check connection",
      "Instruments",
      "Venue account",
      "Enable",
    ]);
    expect(
      scope.queryByRole("button", { name: "Grant access" }),
    ).not.toBeInTheDocument();
    const save = section.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    await user.type(
      section.getByRole("textbox", { name: "Venue account" }),
      " venue-target ",
    );
    await user.click(save);
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      account: "main",
      venueAccount: "venue-target",
    });
    expect(
      await section.findByText("Venue account: venue-target"),
    ).toBeInTheDocument();
    expect(
      scope.queryByRole("button", { name: "Revoke access" }),
    ).not.toBeInTheDocument();
  });

  it("replaces the saved venue account of the default account's own grant, which wins over its group's", async () => {
    const user = userEvent.setup();
    snapshot.providers = [venueAccounts];
    snapshot.connections = [
      connection({
        access: [
          { ...deskGrant, venueAccount: "venue-77" },
          { ...ownGrant, venueAccount: "venue-1" },
        ],
      }),
    ];
    renderTrading();
    const section = venueAccountOf(await card());
    expect(section.getByText("Venue account: venue-1")).toBeInTheDocument();
    expect(section.queryByText(inheritedNote)).not.toBeInTheDocument();
    await user.type(
      section.getByRole("textbox", { name: "Venue account" }),
      "venue-2",
    );
    await user.click(section.getByRole("button", { name: "Save" }));
    expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("alpaca-1", {
      account: "main",
      venueAccount: "venue-2",
    });
  });

  it.each([
    ["no venue account", [ownGrant], "Not done"],
    ["a group grant without one", [deskGrant], "Not done"],
    [
      "the venue account named",
      [{ ...ownGrant, venueAccount: "venue-1" }],
      "Done",
    ],
  ])(
    "marks the venue-account step by the default account's grant: %s",
    async (_name, access, state) => {
      snapshot.providers = [venueAccounts];
      snapshot.connections = [connection({ access })];
      renderTrading();
      const scope = await card();
      const step = within(scope.getByRole("list", { name: "Setup checklist" }))
        .getAllByRole("listitem")
        .find(
          (item) =>
            item.querySelector("button")?.textContent === "Venue account",
        );
      expect(step).toBeDefined();
      expect(
        within(step!).getByText(state, { exact: true }),
      ).toBeInTheDocument();
    },
  );

  const missingAccess =
    "The account has no access to this connection, so orders through it are refused. Disable and enable the connection to restore access.";

  it.each([
    ["enabled without a grant", true, [], true],
    ["enabled with its own grant", true, [ownGrant], false],
    ["enabled with its group's grant", true, [deskGrant], false],
    ["disabled without a grant", false, [], false],
  ])(
    "warns of missing access only on an enabled connection without one: %s",
    async (_name, enabled, access, warned) => {
      snapshot.connections = [connection({ enabled, access })];
      renderTrading();
      const scope = await card();
      if (warned) {
        expect(scope.getByRole("alert")).toHaveTextContent(missingAccess);
      } else {
        expect(scope.queryByText(missingAccess)).not.toBeInTheDocument();
      }
      expect(
        scope.queryByRole("button", { name: "Grant access" }),
      ).not.toBeInTheDocument();
    },
  );

  it("shows a listed group grant's venue account read-only", async () => {
    snapshot.providers = [venueAccounts];
    snapshot.connections = [
      connection({ access: [{ ...deskGrant, venueAccount: "venue-77" }] }),
    ];
    renderTrading();
    const section = venueAccountOf(await card());
    expect(section.getByText(inheritedNote)).toBeInTheDocument();
    expect(section.getByText("Venue account: venue-77")).toBeInTheDocument();
    expect(section.queryByRole("button")).not.toBeInTheDocument();
    expect(section.queryByRole("textbox")).not.toBeInTheDocument();
    expect(section.queryByText(/desk/)).not.toBeInTheDocument();
    expect(api.fetchAccountState).not.toHaveBeenCalled();
  });
});

describe("Test execution policy", () => {
  const connectionId = "test/desk #1";
  const transport = vi.fn<typeof fetch>();
  let state: TestExecutionPolicyState;
  let putProblem: object | null;

  beforeEach(() => {
    snapshot = {
      providers: [
        provider,
        {
          ...provider,
          type: "test",
          title: "Test",
          executionPolicy: true,
          unlinkedFees: false,
        },
      ],
      connections: [
        connection(),
        connection({
          id: connectionId,
          label: "Test desk",
          provider: "test",
          executionPolicy: true,
          secrets: {},
        }),
      ],
    };
    state = {
      connection: connectionId,
      revision: "1",
      rules: [
        {
          mask: { baseAsset: "A*", quoteAsset: "USD" },
          mode: "execute",
          priceVariant: "normal",
        },
        {
          mask: { baseAsset: "MSFT", quoteAsset: "EUR" },
          mode: "rest",
          priceVariant: "best",
          wait: { kind: "finite", seconds: 10 },
        },
      ],
    };
    putProblem = null;
    transport.mockImplementation(async (_input, init) => {
      if (init?.method === "PUT") {
        if (putProblem !== null) return jsonResponse(putProblem, 422);
        const policy = JSON.parse(init.body as string) as TestExecutionPolicy;
        state = { connection: connectionId, revision: "2", ...policy };
      }
      return jsonResponse(state);
    });
    const realApi = createOfficerApi(
      createApiClient({ baseUrl: "/app/api/v1", fetch: transport }),
    );
    api.fetchTestExecutionPolicy.mockImplementation(
      realApi.fetchTestExecutionPolicy,
    );
    api.updateTestExecutionPolicy.mockImplementation(
      realApi.updateTestExecutionPolicy,
    );
  });

  async function loadedPolicy() {
    renderTrading();
    await card("Test desk");
    const section = within(
      await screen.findByRole("region", { name: "Test execution policy" }),
    );
    await section.findByRole("group", { name: "Rule 1" });
    for (const expand of section.getAllByRole("button", { name: "Expand" }))
      await userEvent.click(expand);
    return section;
  }

  function putRequests() {
    return transport.mock.calls.filter(([, init]) => init?.method === "PUT");
  }

  it.each(["0", "9223372037"])(
    "marks invalid execution delay %s and clears it through the standard control",
    async (value) => {
      const user = userEvent.setup();
      const section = await loadedPolicy();
      const rule = within(section.getByRole("group", { name: "Rule 2" }));
      const seconds = rule.getByRole("textbox", { name: "Wait in seconds" });
      await user.clear(seconds);
      await user.type(seconds, value);
      expect(seconds).toBeInvalid();
      await user.click(section.getByRole("button", { name: "Save" }));
      expect(section.getByRole("alert")).toHaveTextContent(
        "Rule 2: complete the field “Wait in seconds”.",
      );
      expect(putRequests()).toHaveLength(0);
      await user.click(
        within(seconds.parentElement!).getByRole("button", {
          name: "Clear field",
        }),
      );
      expect(seconds).toHaveValue("");
      expect(seconds).toBeValid();
    },
  );

  it("keeps finite wait seconds integer-only through paste and stepping", async () => {
    const user = userEvent.setup();
    const section = await loadedPolicy();
    const rule = within(section.getByRole("group", { name: "Rule 2" }));
    const seconds = rule.getByRole("textbox", { name: "Wait in seconds" });
    expect(seconds).toHaveAttribute("inputmode", "numeric");
    await user.clear(seconds);
    await user.type(seconds, "1");
    for (const invalid of ["1,25", "1.25", "-2"]) {
      await user.tripleClick(seconds);
      await user.paste(invalid);
      expect(seconds).toHaveValue("1");
    }
    await user.keyboard("{ArrowDown}");
    expect(seconds).toHaveValue("1");
    await user.keyboard("{ArrowUp}");
    expect(seconds).toHaveValue("2");
    await user.click(section.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(putRequests()).toHaveLength(1));
    expect(
      JSON.parse(putRequests()[0][1]!.body as string).rules[1].wait,
    ).toEqual({ kind: "finite", seconds: 2 });
  });

  it("shows execution rules only for a provider that supports them", async () => {
    const section = await loadedPolicy();
    const venue = await card("Alpaca paper", false);
    expect(
      venue.queryByRole("region", { name: "Test execution policy" }),
    ).not.toBeInTheDocument();
    expect(api.fetchTestExecutionPolicy).toHaveBeenCalledExactlyOnceWith(
      connectionId,
      expect.any(AbortSignal),
    );
    expect(
      section.getByText(/Rules are checked from top to bottom/),
    ).toHaveTextContent("test_policy_no_match");
    expect(
      section.getByText(/Keep exactly one enabled market-data instrument/),
    ).toHaveTextContent("test_quote_ambiguous");
    expect(
      section.getByText(/Keep exactly one enabled market-data instrument/),
    ).toHaveTextContent("Restart feeds");
    expect(section.getByRole("link", { name: "Market Data" })).toHaveAttribute(
      "href",
      "/market-data",
    );
    const testCard = await card("Test desk");
    expect(testCard.getByText("Paper")).toBeInTheDocument();
    expect(
      testCard.getByText("Test", { selector: "span" }),
    ).toBeInTheDocument();
    expect(
      testCard.queryByLabelText("External symbol"),
    ).not.toBeInTheDocument();
  });

  it("starts saved rules collapsed and preserves expansion when the connection is closed", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card("Test desk");
    const section = within(
      await scope.findByRole("region", { name: "Test execution policy" }),
    );
    const first = within(await section.findByRole("group", { name: "Rule 1" }));
    const second = within(section.getByRole("group", { name: "Rule 2" }));
    expect(first.getByRole("button", { name: "Expand" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(first.getByLabelText("Base asset mask")).not.toBeVisible();
    expect(
      first.getByRole("button", { name: "Move rule 1 down" }),
    ).toBeVisible();
    expect(
      second.getByRole("button", { name: "Move rule 2 up" }),
    ).toBeVisible();
    await user.click(first.getByRole("button", { name: "Expand" }));
    const toggle = scope.getByRole("button", { name: "Test desk" });
    await user.click(toggle);
    await user.click(toggle);
    expect(first.getByLabelText("Base asset mask")).toBeVisible();
    expect(second.getByLabelText("Base asset mask")).not.toBeVisible();
    await user.type(first.getByLabelText("Base asset mask"), "X");
    expect(
      first.queryByRole("button", { name: "Collapse" }),
    ).not.toBeInTheDocument();
    await user.click(section.getByRole("button", { name: "Add rule" }));
    const added = within(section.getByRole("group", { name: "Rule 3" }));
    expect(added.getByLabelText("Base asset mask")).toBeVisible();
    expect(
      added.queryByRole("button", { name: "Collapse" }),
    ).not.toBeInTheDocument();
  });

  it("moves rules by priority and persists their new order", async () => {
    const user = userEvent.setup();
    const section = await loadedPolicy();
    const original = structuredClone(state.rules);
    expect(
      section.getByRole("button", { name: "Move rule 1 up" }),
    ).toBeDisabled();
    expect(
      section.getByRole("button", { name: "Move rule 2 down" }),
    ).toBeDisabled();
    await user.click(section.getByRole("button", { name: "Move rule 2 up" }));
    expect(
      within(section.getByRole("group", { name: "Rule 1" })).getByLabelText(
        "Base asset mask",
      ),
    ).toHaveValue("MSFT");
    await user.click(section.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(state.rules).toEqual([original[1], original[0]]),
    );
    await user.click(section.getByRole("button", { name: "Move rule 1 down" }));
    await user.click(section.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(state.rules).toEqual(original));
  });

  it("saves exactly one PUT with the whole edited rule list in order", async () => {
    const user = userEvent.setup();
    const section = await loadedPolicy();
    const first = within(section.getByRole("group", { name: "Rule 1" }));
    await user.clear(first.getByLabelText("Base asset mask"));
    await user.type(first.getByLabelText("Base asset mask"), "B*");
    await user.clear(first.getByLabelText("Quote asset mask"));
    await user.type(first.getByLabelText("Quote asset mask"), "GBP");
    await user.click(first.getByRole("radio", { name: "Split execution" }));
    await user.click(
      within(first.getByRole("group", { name: "Number of fills" })).getByRole(
        "button",
        { name: "3" },
      ),
    );
    await user.click(first.getByRole("button", { name: "Worst" }));
    const second = within(section.getByRole("group", { name: "Rule 2" }));
    await user.clear(second.getByLabelText("Base asset mask"));
    await user.type(second.getByLabelText("Base asset mask"), "IBM");
    await user.clear(second.getByLabelText("Wait in seconds"));
    await user.type(second.getByLabelText("Wait in seconds"), "25");
    await user.click(second.getByRole("button", { name: "Normal" }));
    await user.click(section.getByRole("button", { name: "Add rule" }));
    const third = within(section.getByRole("group", { name: "Rule 3" }));
    await user.type(third.getByLabelText("Base asset mask"), "GOOG");
    await user.type(third.getByLabelText("Quote asset mask"), "USD");
    await user.click(
      third.getByRole("radio", { name: "Reject every second order" }),
    );
    await user.click(third.getByRole("button", { name: "Best" }));
    await user.click(section.getByRole("button", { name: "Save" }));
    await section.findByText("Saved revision 2");
    expect(putRequests()).toHaveLength(1);
    expect(putRequests()[0][0]).toBe(
      "/app/api/v1/trading/connections/test%2Fdesk%20%231/test-policy",
    );
    expect(JSON.parse(putRequests()[0][1]!.body as string)).toEqual({
      rules: [
        {
          mask: { baseAsset: "B*", quoteAsset: "GBP" },
          mode: "split",
          fills: 3,
          priceVariant: "worst",
        },
        {
          mask: { baseAsset: "IBM", quoteAsset: "EUR" },
          mode: "rest",
          wait: { kind: "finite", seconds: 25 },
          priceVariant: "normal",
        },
        {
          mask: { baseAsset: "GOOG", quoteAsset: "USD" },
          mode: "reject_even",
          priceVariant: "best",
        },
      ],
    });
    expect(section.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("points to another incomplete rule before saving infinite rest", async () => {
    const user = userEvent.setup();
    const section = await loadedPolicy();
    await user.click(section.getByRole("button", { name: "Add rule" }));
    const third = within(section.getByRole("group", { name: "Rule 3" }));
    await user.type(third.getByLabelText("Base asset mask"), "BTC");
    await user.type(third.getByLabelText("Quote asset mask"), "USD");
    await user.click(section.getByRole("button", { name: "Add rule" }));
    const fourth = within(section.getByRole("group", { name: "Rule 4" }));
    await user.type(fourth.getByLabelText("Base asset mask"), "AAA");
    await user.type(fourth.getByLabelText("Quote asset mask"), "XXX");
    await user.click(fourth.getByRole("radio", { name: "Rest the order" }));
    await user.click(
      fourth.getByRole("radio", { name: "Without automatic execution" }),
    );
    await user.click(section.getByRole("button", { name: "Save" }));
    expect(await section.findByRole("alert")).toHaveTextContent(
      "Rule 3: complete the field “Market-order price variant”.",
    );
    expect(putRequests()).toHaveLength(0);
    await user.click(section.getByRole("button", { name: "Go to rule 3" }));
    await user.click(third.getByRole("button", { name: "Normal" }));
    await user.click(section.getByRole("button", { name: "Save" }));
    await section.findByText("Saved revision 2");
    expect(state.rules[3]).toEqual({
      mask: { baseAsset: "AAA", quoteAsset: "XXX" },
      mode: "rest",
      wait: { kind: "infinite" },
    });
  });

  it("infinite rest neither shows nor sends priceVariant", async () => {
    state.rules = [state.rules[1]];
    const user = userEvent.setup();
    const section = await loadedPolicy();
    await user.click(
      section.getByRole("radio", { name: "Without automatic execution" }),
    );
    expect(
      section.queryByRole("group", { name: "Market-order price variant" }),
    ).not.toBeInTheDocument();
    expect(section.queryByLabelText("Wait in seconds")).not.toBeInTheDocument();
    expect(
      section.getByText(
        "There is no individual cancel. Any runtime restart loses the book.",
      ),
    ).toBeInTheDocument();
    await user.click(section.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(putRequests()).toHaveLength(1));
    expect(JSON.parse(putRequests()[0][1]!.body as string)).toEqual({
      rules: [
        {
          mask: { baseAsset: "MSFT", quoteAsset: "EUR" },
          mode: "rest",
          wait: { kind: "infinite" },
        },
      ],
    });
    await section.findByText("Saved revision 2");
    expect(
      section.queryByRole("group", { name: "Market-order price variant" }),
    ).not.toBeInTheDocument();
  });

  it("blocks an incomplete mask before PUT, scrolls to the error and keeps the operator input", async () => {
    putProblem = {
      type: "about:blank",
      title: "Unprocessable Content",
      status: 422,
      detail: "base asset mask: asset is required",
      errors: [
        {
          code: "validation",
          constraint: "required",
          pointer: "/rules/0/mask/baseAsset",
        },
      ],
    };
    state.rules.push({
      mask: { baseAsset: "IBM", quoteAsset: "GBP" },
      mode: "split",
      fills: 3,
      priceVariant: "normal",
    });
    const user = userEvent.setup();
    const section = await loadedPolicy();
    const first = within(section.getByRole("group", { name: "Rule 1" }));
    await user.clear(first.getByLabelText("Base asset mask"));
    await user.clear(first.getByLabelText("Quote asset mask"));
    await user.type(first.getByLabelText("Quote asset mask"), "JPY");
    await user.click(first.getByRole("button", { name: "Worst" }));
    const scrollIntoView = vi.mocked(HTMLElement.prototype.scrollIntoView);
    scrollIntoView.mockClear();
    await user.click(section.getByRole("button", { name: "Save" }));
    const alert = await section.findByRole("alert");
    expect(alert).toHaveTextContent(
      "Rule 1: complete the field “Base asset mask”.",
    );
    const actionRow = section.getByRole("button", {
      name: "Save",
    }).parentElement;
    expect(actionRow).toContainElement(
      section.getByRole("button", { name: "Add rule" }),
    );
    expect(alert.parentElement?.nextElementSibling).toBe(actionRow);
    expect(scrollIntoView).toHaveBeenCalledExactlyOnceWith({
      block: "nearest",
    });
    expect(scrollIntoView.mock.contexts[0]).toBe(alert.parentElement);
    expect(first.getByLabelText("Base asset mask")).toHaveValue("");
    expect(first.getByLabelText("Quote asset mask")).toHaveValue("JPY");
    expect(first.getByRole("button", { name: "Worst" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(section.getAllByLabelText("Base asset mask")).toHaveLength(3);
    expect(section.getByText("Saved revision 1")).toBeInTheDocument();
    expect(putRequests()).toHaveLength(0);
    expect(api.fetchTestExecutionPolicy).toHaveBeenCalledTimes(1);
  });

  it.each([true, false])(
    "reconciles an ambiguous policy error without another PUT (committed=%s)",
    async (committed) => {
      const user = userEvent.setup();
      const section = await loadedPolicy();
      const first = within(section.getByRole("group", { name: "Rule 1" }));
      await user.clear(first.getByLabelText("Base asset mask"));
      await user.type(first.getByLabelText("Base asset mask"), "IBM");
      transport.mockImplementation(async (_input, init) => {
        if (init?.method === "PUT") {
          if (committed) {
            state = {
              connection: connectionId,
              revision: "2",
              ...(JSON.parse(init.body as string) as TestExecutionPolicy),
            };
          }
          return jsonResponse(
            { error: { code: "internal", message: "Audit write failed" } },
            500,
          );
        }
        return jsonResponse(state);
      });
      await user.click(section.getByRole("button", { name: "Save" }));
      await waitFor(() =>
        expect(api.fetchTestExecutionPolicy).toHaveBeenCalledTimes(2),
      );
      expect(
        await section.findByText(`Saved revision ${committed ? "2" : "1"}`),
      ).toBeVisible();
      expect(await section.findByRole("alert")).toHaveTextContent(
        "Audit write failed",
      );
      expect(first.getByLabelText("Base asset mask")).toHaveValue("IBM");
      expect(state.rules[0].mask.baseAsset).toBe(committed ? "IBM" : "A*");
      expect(putRequests()).toHaveLength(1);
    },
  );

  it("retries only the policy read after an ambiguous write and failed reconciliation", async () => {
    const user = userEvent.setup();
    const section = await loadedPolicy();
    let failRead = true;
    transport.mockImplementation(async (_input, init) => {
      if (init?.method === "PUT") {
        state = { ...state, revision: "2", rules: [] };
        return jsonResponse(
          { error: { code: "internal", message: "Audit write failed" } },
          500,
        );
      }
      if (failRead) throw new Error("Policy read failed");
      return jsonResponse(state);
    });
    await user.click(section.getByRole("button", { name: "Save" }));
    const retry = await section.findByRole("button", { name: "Retry" });
    expect(section.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(
      section
        .getAllByRole("alert")
        .map((alert) => alert.textContent)
        .join(" "),
    ).toContain("Audit write failed");
    expect(
      section
        .getAllByRole("alert")
        .map((alert) => alert.textContent)
        .join(" "),
    ).toContain("Policy read failed");
    expect(section.getByText("Saved revision 1")).toBeVisible();
    expect(section.getAllByLabelText("Base asset mask")).toHaveLength(2);
    failRead = false;
    await user.click(retry);
    await section.findByText("Saved revision 2");
    expect(section.queryByLabelText("Base asset mask")).not.toBeInTheDocument();
    expect(await section.findByRole("alert")).toHaveTextContent(
      "Audit write failed",
    );
    expect(section.getByRole("button", { name: "Save" })).toBeEnabled();
    expect(putRequests()).toHaveLength(1);
    expect(api.fetchTestExecutionPolicy).toHaveBeenCalledTimes(3);
  });

  it("fills both masks with the all-instruments action and saves them", async () => {
    const user = userEvent.setup();
    const section = await loadedPolicy();
    await user.click(section.getByRole("button", { name: "Add rule" }));
    const third = within(section.getByRole("group", { name: "Rule 3" }));
    expect(third.getByLabelText("Base asset mask")).toHaveValue("");
    expect(third.getByLabelText("Quote asset mask")).toHaveValue("");
    await user.click(
      third.getByRole("button", { name: "For all instruments" }),
    );
    expect(third.getByLabelText("Base asset mask")).toHaveValue("*");
    expect(third.getByLabelText("Quote asset mask")).toHaveValue("*");
    await user.click(third.getByRole("button", { name: "Normal" }));
    await user.click(section.getByRole("button", { name: "Save" }));
    await section.findByText("Saved revision 2");
    expect(state.rules[2]).toEqual({
      mask: { baseAsset: "*", quoteAsset: "*" },
      mode: "execute",
      priceVariant: "normal",
    });
  });

  it.each([
    "/rules/3/priceVariant",
    "/rules/3/unknownField",
    "/rules/3/toString",
    "/rules/30/priceVariant",
  ])(
    "retains the exact later-row pointer %s without guessing its field",
    async (pointer) => {
      state.rules.push(
        structuredClone(state.rules[0]),
        structuredClone(state.rules[0]),
      );
      const saved = structuredClone(state);
      putProblem = {
        detail: "price variant is required",
        errors: [{ code: "validation", constraint: "required", pointer }],
      };
      const user = userEvent.setup();
      const section = await loadedPolicy();
      const fourth = within(section.getByRole("group", { name: "Rule 4" }));
      await user.click(section.getByRole("button", { name: "Save" }));
      expect(await section.findByRole("alert")).toHaveTextContent(
        "price variant is required",
      );
      expect(section.getByText(`${pointer} (required)`)).toBeVisible();
      const fieldMessage =
        "Market-order price variant: price variant is required";
      if (pointer === "/rules/3/priceVariant") {
        expect(fourth.getByText(fieldMessage)).toBeVisible();
        for (let index = 1; index <= 3; index++) {
          expect(
            within(
              section.getByRole("group", { name: `Rule ${index}` }),
            ).queryByText(fieldMessage),
          ).not.toBeInTheDocument();
        }
      } else {
        expect(section.queryByText(fieldMessage)).not.toBeInTheDocument();
      }
      expect(state).toEqual(saved);
      expect(section.getByText("Saved revision 1")).toBeVisible();
      if (pointer === "/rules/3/priceVariant") {
        await user.click(
          section.getByRole("button", { name: "Delete rule 1" }),
        );
      } else if (pointer === "/rules/3/unknownField") {
        await user.click(section.getByRole("button", { name: "Add rule" }));
      } else {
        await user.click(fourth.getByRole("button", { name: "Worst" }));
      }
      expect(section.queryByRole("alert")).not.toBeInTheDocument();
      expect(
        section.queryByText(`${pointer} (required)`),
      ).not.toBeInTheDocument();
      expect(section.queryByText(fieldMessage)).not.toBeInTheDocument();
    },
  );

  it("shows visible mode, fills and wait labels beside their controls", async () => {
    const user = userEvent.setup();
    const section = await loadedPolicy();
    const first = within(section.getByRole("group", { name: "Rule 1" }));
    expect(first.getByText("Execution mode", { exact: true })).toBeVisible();
    await user.click(first.getByRole("radio", { name: "Split execution" }));
    expect(first.getByText("Number of fills", { exact: true })).toBeVisible();
    const second = within(section.getByRole("group", { name: "Rule 2" }));
    expect(
      second.getByText("Automatic execution", { exact: true }),
    ).toBeVisible();
  });

  it("shows an empty rule set without a default rule and saves it empty", async () => {
    state.rules = [];
    const user = userEvent.setup();
    renderTrading();
    await card("Test desk");
    const section = within(
      await screen.findByRole("region", { name: "Test execution policy" }),
    );
    await section.findByText("No execution rules");
    expect(
      section.getByText(
        "This policy is empty. Orders have no matching rule and are refused.",
      ),
    ).toBeInTheDocument();
    expect(section.queryByLabelText("Base asset mask")).not.toBeInTheDocument();
    await user.click(section.getByRole("button", { name: "Save" }));
    await section.findByText("Saved revision 2");
    expect(JSON.parse(putRequests()[0][1]!.body as string)).toEqual({
      rules: [],
    });
  });

  it("removes a rule and saves the remaining rules in their original order", async () => {
    const user = userEvent.setup();
    const section = await loadedPolicy();
    const remaining = state.rules[1];
    await user.click(section.getByRole("button", { name: "Delete rule 1" }));
    expect(section.getAllByLabelText("Base asset mask")).toHaveLength(1);
    expect(section.getByLabelText("Base asset mask")).toHaveValue("MSFT");
    await user.click(section.getByRole("button", { name: "Save" }));
    await section.findByText("Saved revision 2");
    expect(JSON.parse(putRequests()[0][1]!.body as string)).toEqual({
      rules: [remaining],
    });
  });

  it("shows a GET decode error without inventing a policy and can retry", async () => {
    transport.mockResolvedValueOnce(
      jsonResponse({ connection: connectionId, revision: "1" }),
    );
    const user = userEvent.setup();
    renderTrading();
    await card("Test desk");
    const section = within(
      await screen.findByRole("region", { name: "Test execution policy" }),
    );
    expect(await section.findByRole("alert")).toHaveTextContent(
      "Invalid test execution policy",
    );
    expect(
      section.queryByRole("button", { name: "Save" }),
    ).not.toBeInTheDocument();
    expect(section.queryByLabelText("Base asset mask")).not.toBeInTheDocument();
    await user.click(section.getByRole("button", { name: "Retry" }));
    await section.findByRole("group", { name: "Rule 1" });
    expect(section.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("has every policy and simulator guide locale key in all five catalogs", () => {
    function leaves(value: unknown, prefix: string): string[] {
      if (typeof value === "string") return [prefix];
      if (value === null || typeof value !== "object")
        throw new Error(`Invalid locale value: ${prefix}`);
      return Object.entries(value).flatMap(([key, child]) =>
        leaves(child, `${prefix}.${key}`),
      );
    }
    const keys = (catalog: typeof enTrading) =>
      [
        ...leaves(catalog.testPolicy, "testPolicy"),
        ...leaves(catalog.create.test, "create.test"),
      ].sort();
    for (const catalog of [esTrading, ptTrading, ruTrading, zhTrading]) {
      expect(keys(catalog)).toEqual(keys(enTrading));
    }
  });
});

describe("OpenAlgo trading connections", () => {
  const openalgoProvider: TradingProvider = {
    ...provider,
    type: "openalgo",
    title: "OpenAlgo",
    venueAccounts: false,
    unlinkedFees: false,
  };

  beforeEach(() => {
    snapshot = {
      providers: [openalgoProvider],
      connections: [
        connection({
          id: "openalgo-1",
          provider: "openalgo",
          label: "OpenAlgo paper",
          settings: {
            url: "http://openalgo.local:5000",
            wsUrl: "ws://openalgo.local:8765",
          },
          secrets: { apiKey: true },
        }),
      ],
    };
  });

  async function openCreate(user: ReturnType<typeof userEvent.setup>) {
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    const picker = within(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    );
    expect(
      picker.queryByRole("link", { name: "Site" }),
    ).not.toBeInTheDocument();
    await user.click(picker.getByRole("button", { name: "OpenAlgo" }));
    return within(
      await screen.findByRole("dialog", { name: "Add OpenAlgo connection" }),
    );
  }

  it.each(["paper", "live"] as const)(
    "guides and creates a disabled %s connection with explicit trimmed settings and key",
    async (mode) => {
      const user = userEvent.setup();
      renderTrading();
      const dialog = await openCreate(user);
      const modeControl = within(dialog.getByRole("group", { name: "Mode" }));
      if (mode === "live")
        await user.click(modeControl.getByRole("button", { name: "Live" }));
      expect(dialog.getAllByRole("listitem")).toHaveLength(5);
      for (const text of [
        "Install OpenAlgo on a machine Officer can reach and start it. In OpenAlgo, open Select Broker, save your broker's API keys and log in to the broker with Login with.",
        "Open API Key from OpenAlgo's profile menu, generate the API key and copy it. On the same page keep Order Execution Mode on Auto: in Semi-Auto OpenAlgo queues orders for approval in its Action Center instead of sending them, and Officer reports each such order as an error with an unknown outcome.",
        "Enter OpenAlgo's REST address (its HOST_SERVER setting) and WebSocket address (WEBSOCKET_URL) below, with the API key. The REST address identifies this connection and cannot be changed later: to use another address, create a new connection.",
        mode === "paper"
          ? "Switch OpenAlgo to Analyzer mode and keep it there while this connection is enabled. Officer refuses a paper connection while OpenAlgo is in live mode. An order OpenAlgo places in live mode is reported as an error, and Officer does not track it further: if it closes, it stays pending for you to check at the broker."
          : "Switch OpenAlgo to live mode and keep it there while this connection is enabled: live mode sends real orders to your broker. Officer refuses a live connection while OpenAlgo is in Analyzer mode. An order OpenAlgo places in Analyzer mode is reported as an error, and Officer does not track it further: if it closes, it stays pending for you to check in OpenAlgo.",
      ])
        expect(dialog.getByText(text, { exact: true })).toBeInTheDocument();
      const tradingDayTexts = {
        paper:
          "OpenAlgo's order and trade books cover the current trading day only, and OpenAlgo confirms the filled quantity of a cancelled, rejected or expired order only through its live order stream. An order Officer cannot reconcile within its trading day, or one that closed while Officer was stopped, stays pending with an error: check it in OpenAlgo while it is in Analyzer mode.",
        live: "OpenAlgo's order and trade books cover the current trading day only, and OpenAlgo confirms the filled quantity of a cancelled, rejected or expired order only through its live order stream. An order Officer cannot reconcile within its trading day, or one that closed while Officer was stopped, stays pending with an error: check it at the broker.",
      };
      expect(
        dialog.getByText(tradingDayTexts[mode], { exact: true }),
      ).toBeInTheDocument();
      expect(
        dialog.queryByText(
          tradingDayTexts[mode === "paper" ? "live" : "paper"],
          {
            exact: true,
          },
        ),
      ).not.toBeInTheDocument();
      expect(
        dialog.queryByText(
          mode === "paper"
            ? "Switch OpenAlgo to live mode and keep it there while this connection is enabled: live mode sends real orders to your broker. Officer refuses a live connection while OpenAlgo is in Analyzer mode. An order OpenAlgo places in Analyzer mode is reported as an error, and Officer does not track it further: if it closes, it stays pending for you to check in OpenAlgo."
            : "Switch OpenAlgo to Analyzer mode and keep it there while this connection is enabled. Officer refuses a paper connection while OpenAlgo is in live mode. An order OpenAlgo places in live mode is reported as an error, and Officer does not track it further: if it closes, it stays pending for you to check at the broker.",
          { exact: true },
        ),
      ).not.toBeInTheDocument();
      for (const [name, href] of [
        [
          "Installation guide",
          "https://docs.openalgo.in/installation-guidelines/getting-started",
        ],
        [
          "Broker configuration",
          "https://docs.openalgo.in/connect-brokers/broker-configuration",
        ],
        [
          "About Analyzer mode",
          "https://docs.openalgo.in/new-features/api-analyzer",
        ],
        [
          "About order execution modes",
          "https://docs.openalgo.in/new-features/action-center",
        ],
      ])
        expect(dialog.getByRole("link", { name })).toHaveAttribute(
          "href",
          href,
        );
      const url = dialog.getByLabelText("REST address");
      const wsUrl = dialog.getByLabelText("WebSocket address");
      const key = dialog.getByLabelText("API key");
      expect(url).toHaveValue("");
      expect(wsUrl).toHaveValue("");
      expect(url).toHaveAttribute("placeholder", "http://127.0.0.1:5000");
      expect(wsUrl).toHaveAttribute("placeholder", "ws://127.0.0.1:8765");
      expect(url).not.toHaveAttribute("readonly");
      expect(url).toHaveAccessibleDescription(
        "The REST address identifies this connection and cannot be changed later. To use another address, create a new connection.",
      );
      expect(key).toHaveAttribute("type", "password");
      expect(
        dialog.queryByText(i18n.t("trading:settings.openalgo.addressKeyHint")),
      ).not.toBeInTheDocument();
      const create = dialog.getByRole("button", { name: "Create" });
      const entries = [
        [url, " http://openalgo.local:5000 "],
        [wsUrl, " ws://openalgo.local:8765 "],
        [key, " openalgo-private-key "],
      ] as const;
      for (const [field, value] of entries) {
        expect(create).toBeDisabled();
        fireEvent.change(field, { target: { value } });
      }
      expect(create).toBeEnabled();
      for (const [field, value] of entries) {
        fireEvent.change(field, { target: { value: "   " } });
        expect(create).toBeDisabled();
        fireEvent.change(field, { target: { value } });
      }
      await user.click(create);
      expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
        provider: "openalgo",
        label: "OpenAlgo",
        mode,
        enabled: false,
        credentials: {
          url: "http://openalgo.local:5000",
          wsUrl: "ws://openalgo.local:8765",
          apiKey: "openalgo-private-key",
        },
      });
      expect(
        screen.queryByDisplayValue(/openalgo-private-key/),
      ).not.toBeInTheDocument();
    },
  );

  it("shows public addresses and key presence and requires a key for an address change", async () => {
    const user = userEvent.setup();
    renderTrading();
    const form = await card("OpenAlgo paper");
    expect(
      form.getByText("REST address: http://openalgo.local:5000"),
    ).toBeInTheDocument();
    expect(
      form.getByText("WebSocket address: ws://openalgo.local:8765"),
    ).toBeInTheDocument();
    expect(form.getByText("API key: stored")).toBeInTheDocument();
    expect(
      form.queryByRole("link", { name: "Provider site" }),
    ).not.toBeInTheDocument();
    expect(form.getByRole("link", { name: "Provider docs" })).toHaveAttribute(
      "href",
      "https://docs.openalgo.in/new-features/api-analyzer",
    );
    await user.click(form.getByRole("button", { name: "Edit settings" }));
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    const url = dialog.getByLabelText("REST address");
    expect(url).toHaveValue("http://openalgo.local:5000");
    expect(url).toHaveAttribute("readonly");
    expect(
      dialog.getByText(i18n.t("trading:settings.openalgo.urlHint")),
    ).toBeVisible();
    expect(url).toHaveAccessibleDescription(
      "The REST address identifies this connection. To use another address, create a new connection.",
    );
    expect(dialog.getByLabelText("API key")).toHaveValue("");
    expect(dialog.getByLabelText("API key")).toHaveAttribute(
      "placeholder",
      i18n.t("venues:settings.keepStoredSecret"),
    );
    const save = dialog.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    fireEvent.change(dialog.getByLabelText("WebSocket address"), {
      target: { value: "   " },
    });
    expect(save).toBeDisabled();
    fireEvent.change(dialog.getByLabelText("WebSocket address"), {
      target: { value: " ws://other.local:8765 " },
    });
    expect(save).toBeDisabled();
    expect(
      dialog.getByText(i18n.t("trading:settings.openalgo.addressKeyHint")),
    ).toBeVisible();
    expect(
      dialog.getByLabelText("WebSocket address"),
    ).toHaveAccessibleDescription(
      i18n.t("trading:settings.openalgo.addressKeyHint"),
    );
    await user.click(save);
    expect(api.updateTradingConnectionSettings).not.toHaveBeenCalled();
    fireEvent.change(dialog.getByLabelText("API key"), {
      target: { value: "   " },
    });
    expect(save).toBeDisabled();
    fireEvent.change(dialog.getByLabelText("API key"), {
      target: { value: " fake-new-key " },
    });
    expect(save).toBeEnabled();
    await user.click(save);
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "openalgo-1",
      {
        credentials: {
          wsUrl: "ws://other.local:8765",
          apiKey: "fake-new-key",
        },
      },
    );
    expect(screen.queryByDisplayValue(/fake-new-key/)).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("fake-new-key");
  });

  it("keeps the stored key when the trimmed address is unchanged", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      (await card("OpenAlgo paper")).getByRole("button", {
        name: "Edit settings",
      }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    fireEvent.change(dialog.getByLabelText("WebSocket address"), {
      target: { value: " ws://openalgo.local:8765 " },
    });
    const save = dialog.getByRole("button", { name: "Save" });
    expect(save).toBeEnabled();
    await user.click(save);
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "openalgo-1",
      { credentials: { wsUrl: "ws://openalgo.local:8765" } },
    );
  });

  it("updates a non-empty key and label without sending either unchanged address", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      (await card("OpenAlgo paper")).getByRole("button", {
        name: "Edit settings",
      }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    fireEvent.change(dialog.getByLabelText("API key"), {
      target: { value: " replacement-key " },
    });
    fireEvent.change(dialog.getByLabelText("Label"), {
      target: { value: " OpenAlgo renamed " },
    });
    await user.click(dialog.getByRole("button", { name: "Save" }));
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "openalgo-1",
      { label: "OpenAlgo renamed", credentials: { apiKey: "replacement-key" } },
    );
    expect(
      screen.queryByDisplayValue(/replacement-key/),
    ).not.toBeInTheDocument();
  });

  it("saves a label-only edit without a key when stored addresses have surrounding spaces", async () => {
    snapshot.connections[0].settings = {
      url: "  http://openalgo.local:5000  ",
      wsUrl: "  ws://openalgo.local:8765  ",
    };
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      (await card("OpenAlgo paper")).getByRole("button", {
        name: "Edit settings",
      }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    fireEvent.change(dialog.getByLabelText("Label"), {
      target: { value: "OpenAlgo renamed" },
    });
    expect(dialog.getByLabelText("API key")).toHaveValue("");
    const save = dialog.getByRole("button", { name: "Save" });
    expect(save).toBeEnabled();
    await user.click(save);
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "openalgo-1",
      { label: "OpenAlgo renamed" },
    );
  });

  it("shows the backend URL error without imposing client URL validation", async () => {
    api.createTradingConnection.mockRejectedValue(
      new Error("OpenAlgo REST URL must use http or https"),
    );
    const user = userEvent.setup();
    renderTrading();
    const dialog = await openCreate(user);
    for (const [label, value] of [
      ["REST address", "invalid"],
      ["WebSocket address", "ws://openalgo.local:8765"],
      ["API key", "key"],
    ]) {
      fireEvent.change(dialog.getByLabelText(label), { target: { value } });
    }
    await user.click(dialog.getByRole("button", { name: "Create" }));
    expect(
      await dialog.findByText("OpenAlgo REST URL must use http or https"),
    ).toBeInTheDocument();
  });
});

describe("IB trading connections", () => {
  const ibProvider: TradingProvider = {
    ...provider,
    type: "ib",
    title: "Interactive Brokers",
    venueAccounts: true,
    unlinkedFees: false,
  };

  beforeEach(() => {
    snapshot = {
      providers: [ibProvider],
      connections: [
        connection({
          id: "ib-1",
          provider: "ib",
          label: "IB paper",
          settings: { host: "gateway.local", port: 7497, clientId: 12 },
        }),
      ],
    };
  });

  async function openIBCreate(user: ReturnType<typeof userEvent.setup>) {
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    const picker = within(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    );
    await user.click(
      picker.getByRole("button", { name: "Interactive Brokers" }),
    );
    return within(
      await screen.findByRole("dialog", {
        name: "Add Interactive Brokers connection",
      }),
    );
  }

  it.each(["", "0", "27"])(
    "creates IB with numeric credentials and optional client ID %s",
    async (clientId) => {
      const user = userEvent.setup();
      renderTrading();
      const dialog = await openIBCreate(user);
      expect(dialog.getByLabelText("Host")).toHaveValue("127.0.0.1");
      expect(dialog.getByLabelText("Port")).toHaveValue("4002");
      expect(dialog.getByLabelText("Client ID")).toHaveAttribute(
        "placeholder",
        i18n.t("marketData:settings.autoClientId"),
      );
      expect(dialog.queryByLabelText("API key")).not.toBeInTheDocument();
      expect(dialog.queryByLabelText("API secret")).not.toBeInTheDocument();
      expect(dialog.getAllByRole("listitem")).toHaveLength(4);
      expect(
        dialog.getByRole("link", { name: "Download IB Gateway" }),
      ).toHaveAttribute(
        "href",
        "https://www.interactivebrokers.com/en/trading/ibgateway-stable.php",
      );
      expect(
        dialog.getByRole("link", { name: "TWS API documentation" }),
      ).toHaveAttribute(
        "href",
        "https://www.interactivebrokers.com/docs/tws-api/doc/",
      );
      if (clientId !== "")
        await user.type(dialog.getByLabelText("Client ID"), clientId);
      await user.click(dialog.getByRole("button", { name: "Create" }));
      expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
        provider: "ib",
        label: "Interactive Brokers",
        mode: "paper",
        enabled: false,
        credentials: {
          host: "127.0.0.1",
          port: 4002,
          ...(clientId === "" ? {} : { clientId: Number(clientId) }),
        },
      });
    },
  );

  it("prefills the gateway port of the mode until the port is edited", async () => {
    const user = userEvent.setup();
    renderTrading();
    const dialog = await openIBCreate(user);
    const port = dialog.getByLabelText("Port");
    await user.click(dialog.getByRole("button", { name: "Live" }));
    expect(port).toHaveValue("4001");
    await user.click(dialog.getByRole("button", { name: "Paper" }));
    expect(port).toHaveValue("4002");
    await user.clear(port);
    await user.type(port, "7496");
    await user.click(dialog.getByRole("button", { name: "Live" }));
    expect(port).toHaveValue("7496");
    await user.click(dialog.getByRole("button", { name: "Create" }));
    expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
      provider: "ib",
      label: "Interactive Brokers",
      mode: "live",
      enabled: false,
      credentials: { host: "127.0.0.1", port: 7496 },
    });
  });

  it("creates IB with an edited host and port", async () => {
    const user = userEvent.setup();
    renderTrading();
    const dialog = await openIBCreate(user);
    await user.clear(dialog.getByLabelText("Host"));
    await user.type(dialog.getByLabelText("Host"), " tws.local ");
    await user.clear(dialog.getByLabelText("Port"));
    await user.type(dialog.getByLabelText("Port"), "7497");
    await user.click(dialog.getByRole("button", { name: "Create" }));
    expect(api.createTradingConnection).toHaveBeenCalledWith(
      expect.objectContaining({
        credentials: { host: "tws.local", port: 7497 },
      }),
    );
  });

  it.each([
    ["Port", ""],
    ["Port", "0"],
    ["Port", "65536"],
    ["Client ID", "9007199254740992"],
    ["Host", "   "],
    ["Label", ""],
  ])("holds IB creation for invalid %s %s", async (field, value) => {
    const user = userEvent.setup();
    renderTrading();
    const dialog = await openIBCreate(user);
    fireEvent.change(dialog.getByLabelText(field), { target: { value } });
    expect(dialog.getByRole("button", { name: "Create" })).toBeDisabled();
    fireEvent.submit(
      dialog.getByRole("button", { name: "Create" }).closest("form")!,
    );
    expect(api.createTradingConnection).not.toHaveBeenCalled();
  });

  it("rejects IB port and client ID edits as whole values", async () => {
    const user = userEvent.setup();
    renderTrading();
    const dialog = await openIBCreate(user);
    for (const field of ["Port", "Client ID"]) {
      const input = dialog.getByLabelText(field);
      const before = (input as HTMLInputElement).value;
      for (const value of ["-1", "1.5", "1,5", "1e2"]) {
        fireEvent.change(input, { target: { value } });
        expect(input).toHaveValue(before);
      }
    }
    expect(dialog.getByRole("button", { name: "Create" })).toBeEnabled();
  });

  it("shows Auto for an IB connection without a stored client ID", async () => {
    updateConnection("ib-1", {
      settings: { host: "gateway.local", port: 7497 },
    });
    renderTrading();
    const scope = await card("IB paper");
    expect(scope.getByText("Client ID: Auto")).toBeInTheDocument();
  });

  it("shows non-secret IB details and prefills settings without API-key fields", async () => {
    const user = userEvent.setup();
    renderTrading();
    const scope = await card("IB paper");
    expect(scope.getByText("gateway.local:7497")).toBeInTheDocument();
    expect(scope.getByText("Client ID: 12")).toBeInTheDocument();
    expect(scope.queryByText(/API key:/)).not.toBeInTheDocument();
    expect(scope.queryByText(/API secret:/)).not.toBeInTheDocument();
    await user.click(scope.getByRole("button", { name: "Edit settings" }));
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    expect(dialog.getByLabelText("Host")).toHaveValue("gateway.local");
    expect(dialog.getByLabelText("Port")).toHaveValue("7497");
    expect(dialog.getByLabelText("Client ID")).toHaveValue("12");
    expect(dialog.queryByLabelText("API key")).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.change(dialog.getByLabelText("Port"), {
      target: { value: "4002" },
    });
    fireEvent.change(dialog.getByLabelText("Label"), {
      target: { value: "IB renamed" },
    });
    await user.click(dialog.getByRole("button", { name: "Save" }));
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "ib-1",
      { label: "IB renamed", credentials: { port: 4002 } },
    );
  });

  it.each([
    ["Host", "new.local", { host: "new.local" }],
    ["Client ID", "15", { clientId: 15 }],
    ["Client ID", "", undefined],
    ["Label", "IB renamed", undefined],
  ])(
    "sends only changed IB keys for %s %s",
    async (field, value, credentials) => {
      const user = userEvent.setup();
      renderTrading();
      await user.click(
        (await card("IB paper")).getByRole("button", { name: "Edit settings" }),
      );
      const dialog = within(
        screen.getByRole("dialog", { name: "Edit settings" }),
      );
      fireEvent.change(dialog.getByLabelText(field), { target: { value } });
      expect(dialog.getByRole("button", { name: "Save" })).toBeEnabled();
      await user.click(dialog.getByRole("button", { name: "Save" }));
      expect(
        api.updateTradingConnectionSettings,
      ).toHaveBeenCalledExactlyOnceWith("ib-1", {
        ...(field === "Label" ? { label: value } : {}),
        ...(credentials === undefined ? {} : { credentials }),
      });
    },
  );

  it.each([
    ["Host", ""],
    ["Port", "65536"],
    ["Label", ""],
  ])("holds invalid IB settings %s", async (field, value) => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      (await card("IB paper")).getByRole("button", { name: "Edit settings" }),
    );
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit settings" }),
    );
    fireEvent.change(dialog.getByLabelText(field), { target: { value } });
    expect(dialog.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.submit(
      dialog.getByRole("button", { name: "Save" }).closest("form")!,
    );
    expect(api.updateTradingConnectionSettings).not.toHaveBeenCalled();
  });

  it.each(["single-account", "multi-account"])(
    "guides IB's venue account in %s mode without naming a grant only when accounts are hidden",
    async (mode) => {
      if (mode === "single-account")
        setAccountScope({
          accounts: false,
          account: "main",
          accountPnlReset: false,
        });
      const user = userEvent.setup();
      renderTrading();
      const dialog = await openIBCreate(user);
      expect(dialog.getAllByRole("listitem")[3]).toHaveTextContent(
        mode === "single-account"
          ? "After creating the connection, enter your IB account ID (starts with DU for paper trading) as its venue account."
          : "After creating the connection, grant access with your IB account ID (starts with DU for paper trading) as the venue account.",
      );
      await user.click(dialog.getByRole("button", { name: "Live" }));
      expect(dialog.getAllByRole("listitem")[3]).toHaveTextContent(
        mode === "single-account"
          ? "After creating the connection, enter the account ID of your live IB account as its venue account."
          : "After creating the connection, grant access with the account ID of your live IB account as the venue account.",
      );
    },
  );

  it.each(["account", "group"])(
    "requires the typed venue account for an IB %s grant and focuses it",
    async (kind) => {
      const user = userEvent.setup();
      renderTrading();
      const scope = await card("IB paper");
      const form = within(scope.getByRole("form", { name: "Grant access" }));
      if (kind === "group")
        await user.click(form.getByRole("button", { name: "Group" }));
      await user.type(
        form.getByLabelText(kind === "group" ? "Group" : "Account"),
        kind === "group" ? "desk" : "alpha",
      );
      expect(form.getByRole("button", { name: "Grant access" })).toBeDisabled();
      await user.click(form.getByRole("button", { name: "Set venue account" }));
      expect(form.getByLabelText("Venue account")).toHaveFocus();
      await user.type(form.getByLabelText("Venue account"), "  ");
      expect(form.getByRole("button", { name: "Grant access" })).toBeDisabled();
      await user.type(form.getByLabelText("Venue account"), "DU123 ");
      expect(form.getByRole("button", { name: "Grant access" })).toBeEnabled();
      await user.click(form.getByRole("button", { name: "Grant access" }));
      expect(api.grantTradingAccess).toHaveBeenCalledExactlyOnceWith("ib-1", {
        ...(kind === "group" ? { accountGroup: "desk" } : { account: "alpha" }),
        venueAccount: "DU123",
      });
    },
  );
});

describe("Add trading connection dialog", () => {
  it("opens from the add button in page content", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    expect(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    ).toBeInTheDocument();
  });

  it("opens the picker from the add button when the page has no connections", async () => {
    const user = userEvent.setup();
    snapshot.connections = [];
    renderTrading();
    await screen.findByText("No trading connections", { exact: true });
    const buttons = screen.getAllByRole("button", {
      name: /^Add trading connection/,
    });
    expect(buttons).toHaveLength(1);
    await user.click(buttons[0]);
    expect(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    ).toBeInTheDocument();
  });

  it("wires provider tiles and opens a single create dialog with the exact Alpaca guidance", async () => {
    const user = userEvent.setup();
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    const picker = within(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    );
    expect(
      picker.getByText("Choose venue", { exact: true }),
    ).toBeInTheDocument();
    expect(venueWiring.add).toHaveBeenLastCalledWith(
      expect.objectContaining({
        label: "Add trading connection",
        providerTypes: ["alpaca"],
      }),
    );
    expect(venueWiring.picker).toHaveBeenLastCalledWith(
      expect.objectContaining({
        title: "Add trading connection",
        description: "Choose venue",
        providers: [
          {
            type: "alpaca",
            title: "Alpaca",
            siteUrl: "https://alpaca.markets",
            docsUrl: "https://docs.alpaca.markets/us/docs/paper-trading",
          },
        ],
      }),
    );
    const tile = picker.getByRole("button", { name: "Alpaca" });
    expect(tile.querySelector("svg")).toBeInTheDocument();
    for (const [name, href] of [
      ["Site", "https://alpaca.markets"],
      ["Docs", "https://docs.alpaca.markets/us/docs/paper-trading"],
    ])
      expect(picker.getByRole("link", { name })).toHaveAttribute("href", href);
    await user.click(tile);
    expect(venueWiring.picker).toHaveBeenLastCalledWith(
      expect.objectContaining({ open: false }),
    );
    expect(
      screen.queryByRole("dialog", { name: "Add trading connection" }),
    ).not.toBeInTheDocument();
    const dialog = within(
      screen.getByRole("dialog", { name: "Add Alpaca connection" }),
    );
    expect(
      dialog.getByRole("heading", { name: "Prepare your Alpaca account" }),
    ).toBeInTheDocument();
    for (const text of [
      "Create an Alpaca account.",
      "Open your paper trading account: in the Alpaca dashboard, switch the account selector at the top to Paper Trading. Alpaca gives every account a paper account with simulated funds.",
      "Generate paper API keys: with Paper Trading selected, find the API Keys panel on the dashboard and generate a key; if a key already exists there, the button is Regenerate. Copy the API key and the secret - Alpaca shows the secret only once. Paper keys work only for the paper account.",
      "Keep the dashboard open - the fields below ask for both values.",
    ])
      expect(dialog.getByText(text, { exact: true })).toBeInTheDocument();
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
    expect(dialog.getByLabelText("Label")).toHaveValue("Alpaca");
    const mode = within(dialog.getByRole("group", { name: "Mode" }));
    expect(dialog.getByText("Mode", { exact: true })).toHaveAttribute(
      "class",
      dialog.getByText("Label", { exact: true }).getAttribute("class"),
    );
    expect(mode.getByRole("button", { name: "Paper" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(mode.getByRole("button", { name: "Paper" })).toBeEnabled();
    expect(mode.getByRole("button", { name: "Live" })).toBeEnabled();
    for (const name of ["API key", "API secret"]) {
      expect(dialog.getByLabelText(name)).toHaveAttribute("type", "password");
      expect(dialog.getByLabelText(name)).toHaveAttribute(
        "autocomplete",
        "new-password",
      );
    }
    expect(api.createTradingConnection).not.toHaveBeenCalled();
  });

  it("shows the three Binance setup steps and the Spot Testnet link", async () => {
    const user = userEvent.setup();
    snapshot.providers.push({ ...provider, type: "binance", title: "Binance" });
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    const picker = within(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    );
    const binanceTile = picker.getByRole("button", { name: "Binance" });
    const tile = within(binanceTile.parentElement!);
    expect(tile.getByRole("link", { name: "Site" })).toHaveAttribute(
      "href",
      "https://www.binance.com",
    );
    expect(tile.getByRole("link", { name: "Docs" })).toHaveAttribute(
      "href",
      "https://testnet.binance.vision/",
    );
    await user.click(binanceTile);
    const dialog = within(
      await screen.findByRole("dialog", { name: "Add Binance connection" }),
    );
    expect(
      dialog.getByRole("heading", { name: "Prepare your Binance account" }),
    ).toBeInTheDocument();
    expect(dialog.getAllByRole("listitem")).toHaveLength(3);
    for (const text of [
      "Open the Binance Spot Testnet and log in with a GitHub account.",
      "Generate an HMAC-SHA256 API key there and copy the API key and the secret key. Paper connections use the Spot Testnet; testnet keys work only there.",
      "Keep the page open - the fields below ask for both values.",
    ]) {
      expect(dialog.getByText(text, { exact: true })).toBeInTheDocument();
    }
    const link = dialog.getByRole("link", { name: "Open Spot Testnet" });
    expect(link).toHaveAttribute("href", "https://testnet.binance.vision/");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it.each([
    {
      venue: {
        ...provider,
        type: "ib",
        title: "Interactive Brokers",
        venueAccounts: true,
      },
      paperFirst:
        "Install IB Gateway (or TWS) and log in with your paper trading username.",
      live: [
        "Install IB Gateway (or TWS) and log in with your live trading username.",
        'In Configure > Settings > API > Settings, enable socket clients (TWS: "Enable ActiveX and Socket Clients"), clear "Read-Only API" so orders are accepted, and note the socket port (IB Gateway live: 4001; TWS live: 7496). Allow 127.0.0.1 in Trusted IPs, or accept the connection prompt.',
        "Keep IB Gateway running and logged in. Enter its host and port below; the client ID is optional.",
        "After creating the connection, grant access with the account ID of your live IB account as the venue account. Orders reach IB only through a grant bound to that account.",
      ],
      docs: {
        name: "TWS API documentation",
        href: "https://www.interactivebrokers.com/docs/tws-api/doc/",
      },
    },
    {
      venue: { ...provider, type: "binance", title: "Binance" },
      paperFirst:
        "Open the Binance Spot Testnet and log in with a GitHub account.",
      live: [
        "Meet Binance's account requirements for API keys: identity verification, two-factor authentication and a deposit of any amount to the Spot Wallet.",
        "Create an API key: click the profile icon, then Account, go to API Management and click Create API. Choose System generated - Officer signs requests with HMAC-SHA256 - enter a label and complete the two-factor verification. Copy the API key and the secret key.",
        "Click Edit restrictions, select Restrict access to trusted IPs only and enter the public IPv4 address of the machine running Officer: without that restriction, a system-generated key can only read. Then check Enable Spot & Margin Trading so Officer can place orders, and enable no other permission.",
        "Keep the page open - the fields below ask for both values.",
      ],
      docs: {
        name: "How to create API keys",
        href: "https://www.binance.com/en/support/faq/how-to-create-api-keys-on-binance-360002502072",
      },
    },
    {
      venue: { ...provider, type: "etoro", title: "eToro" },
      paperFirst: "Log in to your eToro account.",
      live: [
        "Log in to your eToro account.",
        "Create a user key: in eToro, open Settings, then Trading, scroll to API Key Management and click Create New Key. Choose the Real environment - each key works for one environment only, and Officer connects the real account in live mode. Choose Write permission - Read only shows portfolio data and cannot place orders. Click Generate Key and enter the verification code eToro sends to your phone. API Key Management appears only once your eToro account is verified.",
        "Copy the new key from the Generated Keys list into the User key field.",
        "The Public API key identifies the application. Enter it in the Public API key field.",
        "Keep this dialog open - the fields below ask for both values.",
      ],
      docs: {
        name: "eToro authentication guide",
        href: "https://api-portal.etoro.com/core/getting-started/authentication",
      },
    },
    {
      venue: provider,
      paperFirst: "Create an Alpaca account.",
      live: [
        "Open a live brokerage account: apply in the Alpaca dashboard with a government-issued ID and, once Alpaca approves the application, fund the account. Live orders need an approved, funded account.",
        "Generate live API keys: in the top left corner of the dashboard, select Live Trading, find the API Keys panel and generate new keys. Copy the API key and the secret - Alpaca shows the secret only once. Live keys differ from paper keys and do not work with the paper account.",
        "Keep the dashboard open - the fields below ask for both values.",
      ],
      docs: {
        name: "About live credentials",
        href: "https://docs.alpaca.markets/us/docs/authentication",
      },
    },
  ])(
    "switches $venue.title between the paper and the live account steps",
    async ({ venue, paperFirst, live, docs }) => {
      const user = userEvent.setup();
      snapshot = { providers: [venue], connections: [] };
      renderTrading();
      await user.click(
        await screen.findByRole("button", { name: /^Add trading connection/ }),
      );
      await user.click(
        within(
          screen.getByRole("dialog", { name: "Add trading connection" }),
        ).getByRole("button", { name: venue.title }),
      );
      const dialog = within(
        screen.getByRole("dialog", { name: `Add ${venue.title} connection` }),
      );
      const mode = within(dialog.getByRole("group", { name: "Mode" }));
      const paperSteps = dialog
        .getAllByRole("listitem")
        .map((step) => step.textContent);
      expect(paperSteps[0]).toContain(paperFirst);
      await user.click(mode.getByRole("button", { name: "Live" }));
      expect(
        dialog.getByRole("heading", {
          name: `Prepare your ${venue.title} account`,
        }),
      ).toBeInTheDocument();
      const steps = dialog.getAllByRole("listitem");
      expect(steps).toHaveLength(live.length);
      live.forEach((text, index) =>
        expect(
          within(steps[index]).getByText(text, { exact: true }),
        ).toBeInTheDocument(),
      );
      const link = dialog.getByRole("link", { name: docs.name });
      expect(link).toHaveAttribute("href", docs.href);
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
      await user.click(mode.getByRole("button", { name: "Paper" }));
      expect(
        dialog.getAllByRole("listitem").map((step) => step.textContent),
      ).toEqual(paperSteps);
    },
  );

  it("shows a provider without instructions or external links", async () => {
    const user = userEvent.setup();
    snapshot.providers.push({
      ...provider,
      type: "other",
      title: "Other venue",
    });
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    const picker = within(
      screen.getByRole("dialog", { name: "Add trading connection" }),
    );
    const tile = picker.getByRole("button", { name: "Other venue" });
    expect(
      within(tile.parentElement!).queryByRole("link"),
    ).not.toBeInTheDocument();
    await user.click(tile);
    const dialog = within(
      screen.getByRole("dialog", { name: "Add Other venue connection" }),
    );
    expect(
      dialog.getByRole("heading", { name: "Prepare your Other venue account" }),
    ).toBeInTheDocument();
    expect(dialog.queryByRole("list")).not.toBeInTheDocument();
    expect(dialog.queryByRole("link")).not.toBeInTheDocument();
    expect(dialog.getByLabelText("Label")).toHaveValue("Other venue");
  });

  it.each([
    { sealed: false, warning: true },
    { sealed: true, warning: false },
  ])(
    "keeps key inputs enabled when credential storage sealed is $sealed",
    async ({ sealed, warning }) => {
      const user = userEvent.setup();
      credentialStorage = storage(sealed);
      renderTrading();
      const dialog = await openCreateConnection(user);
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

  it.each([
    { sealed: false, warning: true },
    { sealed: true, warning: false },
  ])(
    "creates live with the live account steps when credential storage sealed is $sealed",
    async ({ sealed, warning }) => {
      const user = userEvent.setup();
      credentialStorage = storage(sealed);
      renderTrading();
      const dialog = await openCreateConnection(user);
      const mode = within(dialog.getByRole("group", { name: "Mode" }));
      await user.click(mode.getByRole("button", { name: "Live" }));
      expect(mode.getByRole("button", { name: "Live" })).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(
        dialog.getByRole("heading", { name: "Prepare your Alpaca account" }),
      ).toBeInTheDocument();
      expect(
        dialog.getAllByRole("listitem").map((step) => step.textContent),
      ).toEqual([
        "Open a live brokerage account: apply in the Alpaca dashboard with a government-issued ID and, once Alpaca approves the application, fund the account. Live orders need an approved, funded account.Sign up at AlpacaLog in",
        "Generate live API keys: in the top left corner of the dashboard, select Live Trading, find the API Keys panel and generate new keys. Copy the API key and the secret - Alpaca shows the secret only once. Live keys differ from paper keys and do not work with the paper account.Open the Alpaca dashboardAbout live credentials",
        "Keep the dashboard open - the fields below ask for both values.",
      ]);
      // The account links stay; the live docs replace the paper docs.
      expect(
        dialog.queryAllByRole("link").map((link) => link.textContent),
      ).toEqual([
        ...(warning ? ["How to set a master key"] : []),
        "Sign up at Alpaca",
        "Log in",
        "Open the Alpaca dashboard",
        "About live credentials",
      ]);
      expect(
        dialog.getByRole("link", { name: "About live credentials" }),
      ).toHaveAttribute(
        "href",
        "https://docs.alpaca.markets/us/docs/authentication",
      );
      const warningText = "Keys will be stored unencrypted";
      if (warning) {
        expect(dialog.getByRole("alert")).toHaveTextContent(warningText);
      } else {
        expect(dialog.queryByText(warningText)).not.toBeInTheDocument();
      }
      await user.type(dialog.getByLabelText("API key"), "live-key");
      await user.type(dialog.getByLabelText("API secret"), "live-secret");
      await user.click(dialog.getByRole("button", { name: "Create" }));
      expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
        provider: "alpaca",
        label: "Alpaca",
        mode: "live",
        credentials: { apiKey: "live-key", apiSecret: "live-secret" },
        enabled: false,
      });
    },
  );

  it("shows a failed credential storage load and keeps key entry enabled", async () => {
    const user = userEvent.setup();
    fetchCredentialStorageMock.mockRejectedValue(
      new ApiError("Credential storage unavailable", "internal"),
    );
    renderTrading();
    const dialog = await openCreateConnection(user);
    expect(dialog.getByRole("alert")).toHaveTextContent(
      "Could not check whether stored keys are encrypted: Credential storage unavailable",
    );
    expect(
      dialog.queryByText("Keys will be stored unencrypted"),
    ).not.toBeInTheDocument();
    await user.type(dialog.getByLabelText("API key"), "paper-key");
    await user.type(dialog.getByLabelText("API secret"), "paper-secret");
    expect(dialog.getByRole("button", { name: "Create" })).toBeEnabled();
  });

  it("requires the label and both keys before creating", async () => {
    const user = userEvent.setup();
    renderTrading();
    const dialog = await openCreateConnection(user);
    const create = dialog.getByRole("button", { name: "Create" });
    const label = dialog.getByLabelText("Label");
    expect(label).toHaveValue("Alpaca");
    for (const name of ["Label", "API key", "API secret"])
      expect(dialog.getByLabelText(name)).toBeRequired();
    expect(create).toBeDisabled();
    await user.type(dialog.getByLabelText("API key"), "paper-key");
    expect(create).toBeDisabled();
    await user.type(dialog.getByLabelText("API secret"), "paper-secret");
    expect(create).toBeEnabled();
    await user.clear(label);
    expect(create).toBeDisabled();
    await user.type(label, "   ");
    expect(create).toBeDisabled();
    await user.type(label, "Desk paper");
    expect(create).toBeEnabled();
    for (const name of ["API key", "API secret"]) {
      await user.clear(dialog.getByLabelText(name));
      await user.type(dialog.getByLabelText(name), "   ");
      expect(create).toBeDisabled();
      await user.type(dialog.getByLabelText(name), "key");
    }
    expect(api.createTradingConnection).not.toHaveBeenCalled();
  });

  it("creates paper disabled with trimmed values, reloads, and focuses the new card", async () => {
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
    const dialog = await openCreateConnection(user);
    await user.clear(dialog.getByLabelText("Label"));
    await user.type(dialog.getByLabelText("Label"), "  Desk paper  ");
    await user.type(dialog.getByLabelText("API key"), "  paper-key  ");
    await user.type(dialog.getByLabelText("API secret"), "  paper-secret  ");
    const callsBeforeCreate = api.fetchTrading.mock.calls.length;
    await user.click(dialog.getByRole("button", { name: "Create" }));
    expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
      provider: "alpaca",
      label: "Desk paper",
      mode: "paper",
      credentials: { apiKey: "paper-key", apiSecret: "paper-secret" },
      enabled: false,
    });
    expect(dialog.getByRole("button", { name: "Create" })).toBeDisabled();
    expect(dialog.getByLabelText("Label")).toBeDisabled();
    expect(dialog.getByLabelText("API key")).toBeDisabled();
    expect(dialog.getByLabelText("API secret")).toBeDisabled();
    await act(async () => {
      finishCreate();
    });
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Add Alpaca connection" }),
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
      const dialog = await openCreateConnection(user);
      const dialogElement = screen.getByRole("dialog", {
        name: "Add Alpaca connection",
      });
      await user.clear(dialog.getByLabelText("Label"));
      await user.type(dialog.getByLabelText("Label"), "Desk paper");
      await user.type(dialog.getByLabelText("API key"), "paper-key");
      await user.type(dialog.getByLabelText("API secret"), "paper-secret");
      await user.click(dialog.getByRole("button", { name: "Create" }));
      expect(api.createTradingConnection).toHaveBeenCalledTimes(1);
      if (close === "Escape") await user.keyboard("{Escape}");
      else
        fireEvent.pointerDown(document.body, {
          button: 0,
          pointerType: "mouse",
        });
      expect(
        screen.getByRole("dialog", { name: "Add Alpaca connection" }),
      ).toBe(dialogElement);
      await act(async () => finishCreate(connection()));
      await waitFor(() =>
        expect(
          screen.queryByRole("dialog", { name: "Add Alpaca connection" }),
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
    "keeps the create dialog and its fields after a $status $code error",
    async ({ status, code, message }) => {
      const user = userEvent.setup();
      api.createTradingConnection.mockRejectedValue(
        new ApiError(message, code, status),
      );
      renderTrading();
      const dialog = await openCreateConnection(user);
      await user.clear(dialog.getByLabelText("Label"));
      await user.type(dialog.getByLabelText("Label"), "Desk paper");
      await user.type(dialog.getByLabelText("API key"), "paper-key");
      await user.type(dialog.getByLabelText("API secret"), "paper-secret");
      const callsBeforeCreate = api.fetchTrading.mock.calls.length;
      await user.click(dialog.getByRole("button", { name: "Create" }));
      const alert = await dialog.findByRole("alert");
      expect(alert).toHaveTextContent(message);
      const errorWrapper = alert.parentElement;
      const scrollIntoView = vi.mocked(
        window.HTMLElement.prototype.scrollIntoView,
      );
      expect(scrollIntoView).toHaveBeenCalledExactlyOnceWith({
        block: "nearest",
      });
      expect(scrollIntoView.mock.contexts[0]).toBe(errorWrapper);
      const submit = dialog.getByRole("button", { name: "Create" });
      const cancel = dialog.getByRole("button", { name: "Cancel" });
      const form = submit.closest("form");
      expect(form).toContainElement(alert);
      expect(submit.parentElement).toBe(cancel.parentElement);
      expect(submit.parentElement?.previousElementSibling).toBe(errorWrapper);
      for (const button of [cancel, submit]) {
        expect(
          alert.compareDocumentPosition(button) &
            Node.DOCUMENT_POSITION_FOLLOWING,
        ).toBeTruthy();
      }
      expect(
        dialog.getByRole("heading", { name: "Add Alpaca connection" }),
      ).toBeInTheDocument();
      expect(dialog.getByLabelText("Label")).toHaveValue("Desk paper");
      expect(dialog.getByLabelText("API key")).toHaveValue("paper-key");
      expect(dialog.getByLabelText("API secret")).toHaveValue("paper-secret");
      expect(dialog.getByRole("button", { name: "Paper" })).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(api.fetchTrading.mock.calls.length).toBe(callsBeforeCreate);
      scrollIntoView.mockClear();
      let failCreate!: () => void;
      api.createTradingConnection.mockImplementationOnce(
        () =>
          new Promise<TradingConnection>((_, reject) => {
            failCreate = () => reject(new ApiError(message, code, status));
          }),
      );
      await user.type(dialog.getByLabelText("Label"), " retry{Enter}");
      expect(api.createTradingConnection).toHaveBeenCalledTimes(2);
      await act(async () => failCreate());
      const retryAlert = await dialog.findByRole("alert");
      expect(retryAlert).toHaveTextContent(message);
      expect(scrollIntoView).toHaveBeenCalledExactlyOnceWith({
        block: "nearest",
      });
      expect(scrollIntoView.mock.contexts[0]).toBe(retryAlert.parentElement);
    },
  );

  it.each(["Cancel", "Close"])(
    "%s restores the provider label and clears keys when reopened",
    async (closeName) => {
      const user = userEvent.setup();
      renderTrading();
      let dialog = await openCreateConnection(user);
      await user.clear(dialog.getByLabelText("Label"));
      await user.type(dialog.getByLabelText("Label"), "Desk paper");
      await user.type(dialog.getByLabelText("API key"), "paper-key");
      await user.type(dialog.getByLabelText("API secret"), "paper-secret");
      await user.click(dialog.getByRole("button", { name: closeName }));
      await waitFor(() =>
        expect(
          screen.queryByRole("dialog", { name: "Add Alpaca connection" }),
        ).not.toBeInTheDocument(),
      );
      dialog = await openCreateConnection(user);
      expect(dialog.getByLabelText("Label")).toHaveValue("Alpaca");
      expect(dialog.getByLabelText("API key")).toHaveValue("");
      expect(dialog.getByLabelText("API secret")).toHaveValue("");
    },
  );
});

describe("Trading provider credential fields", () => {
  const etoro: TradingProvider = { ...provider, type: "etoro", title: "eToro" };
  const okx: TradingProvider = { ...provider, type: "okx", title: "OKX" };
  const unknownCredentials =
    "This panel does not know the credential fields of this provider.";

  async function chooseOKXSite(
    user: ReturnType<typeof userEvent.setup>,
    trigger: HTMLElement,
    name: string,
    inspect?: (options: HTMLElement[]) => void,
  ) {
    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    try {
      await user.click(trigger);
      const options = await screen.findAllByRole("option");
      inspect?.(options);
      await user.click(screen.getByRole("option", { name }));
    } finally {
      focusSpy.mockRestore();
    }
  }

  async function openProviderCreate(
    user: ReturnType<typeof userEvent.setup>,
    title: string,
  ) {
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    await user.click(
      within(
        screen.getByRole("dialog", { name: "Add trading connection" }),
      ).getByRole("button", { name: title }),
    );
    return within(
      screen.getByRole("dialog", { name: `Add ${title} connection` }),
    );
  }

  it("requires the OKX label, three secrets and an explicitly chosen site", async () => {
    const user = userEvent.setup();
    snapshot = { providers: [okx], connections: [] };
    renderTrading();
    const dialog = await openProviderCreate(user, "OKX");
    const create = dialog.getByRole("button", { name: "Create" });
    const label = dialog.getByLabelText("Label");
    expect(label).toBeRequired();
    expect(create).toBeDisabled();
    const site = dialog.getByRole("combobox", { name: "OKX site" });
    expect(site).toBeRequired();
    expect(site).toHaveTextContent("Choose the site of your account");
    for (const name of ["API key", "API secret", "Passphrase"]) {
      const input = dialog.getByLabelText(name, { exact: true });
      expect(input).toBeRequired();
      expect(input).toHaveAttribute("type", "password");
      expect(input).toHaveAttribute("autocomplete", "new-password");
      await user.type(input, `  fixture-${name}  `);
      expect(create).toBeDisabled();
    }
    await chooseOKXSite(user, site, "EEA account (my.okx.com)", (options) => {
      expect(options.map((option) => option.textContent)).toEqual([
        "Global account (www.okx.com)",
        "EEA account (my.okx.com)",
        "US or Australian account (app.okx.com)",
      ]);
      expect(
        options.every(
          (option) => option.getAttribute("aria-selected") === "false",
        ),
      ).toBe(true);
    });
    expect(create).toBeEnabled();
    for (const name of ["API key", "API secret", "Passphrase"]) {
      const input = dialog.getByLabelText(name, { exact: true });
      await user.clear(input);
      await user.type(input, "   ");
      expect(create).toBeDisabled();
      await user.type(input, `fixture-${name}  `);
      expect(create).toBeEnabled();
    }
    await user.clear(label);
    await user.type(label, "   ");
    expect(create).toBeDisabled();
    await user.type(label, "OKX desk  ");
    await user.click(create);
    expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
      provider: "okx",
      label: "OKX desk",
      mode: "paper",
      credentials: {
        apiKey: "fixture-API key",
        apiSecret: "fixture-API secret",
        passphrase: "fixture-Passphrase",
        site: "my.okx.com",
      },
      enabled: false,
    });
  });

  it("shows the OKX paper guide and switches to the live guide", async () => {
    const user = userEvent.setup();
    snapshot = { providers: [okx], connections: [] };
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    const picker = within(screen.getByRole("dialog"));
    expect(picker.getByRole("link", { name: "Site" })).toHaveAttribute(
      "href",
      "https://www.okx.com",
    );
    expect(picker.getByRole("link", { name: "Docs" })).toHaveAttribute(
      "href",
      "https://www.okx.com/docs-v5/en/#overview-demo-trading-services",
    );
    await user.click(picker.getByRole("button", { name: "OKX" }));
    const dialog = within(screen.getByRole("dialog"));
    const keepPageOpen =
      "Keep the page open - the fields below ask for the three values and the site your account is registered on.";
    const paper = [
      "Open your OKX account dashboard. In its context menu, select Trade -> Demo Trading to open the demo trading terminal.",
      "In the terminal, open the profile menu and select Demo Trading API. Click Create Demo Trading API Key, grant the Trade permission, and set a passphrase. Save the API key, secret key, and passphrase.",
      keepPageOpen,
    ];
    expect(dialog.getAllByRole("listitem")).toHaveLength(3);
    paper.forEach((text, index) =>
      expect(
        within(dialog.getAllByRole("listitem")[index]).getByText(text),
      ).toBeInTheDocument(),
    );
    expect(
      dialog.getByRole("link", { name: "About demo trading" }),
    ).toHaveAttribute(
      "href",
      "https://www.okx.com/docs-v5/en/#overview-demo-trading-services",
    );
    await user.click(dialog.getByRole("button", { name: "Live" }));
    const live = [
      "Create an API key in your OKX account with the Trade permission and no withdrawal permission, and set a passphrase. Copy the API key, the secret key and the passphrase - OKX cannot recover a lost passphrase.",
      "Bind the public IPv4 address of the machine running Officer to the key: OKX expires a key with the trading permission and no bound IP address after 14 days of inactivity.",
      keepPageOpen,
    ];
    expect(dialog.getAllByRole("listitem")).toHaveLength(3);
    live.forEach((text, index) =>
      expect(
        within(dialog.getAllByRole("listitem")[index]).getByText(text),
      ).toBeInTheDocument(),
    );
    expect(dialog.queryByText(paper[0])).not.toBeInTheDocument();
    expect(
      dialog.queryByRole("link", { name: "About demo trading" }),
    ).not.toBeInTheDocument();
    expect(
      dialog.getByRole("link", { name: "How to create API keys" }),
    ).toHaveAttribute(
      "href",
      "https://www.okx.com/docs-v5/en/#overview-api-key-creation",
    );
  });

  it.each(["site", "passphrase"])(
    "edits only the changed OKX %s",
    async (field) => {
      const user = userEvent.setup();
      snapshot = {
        providers: [okx],
        connections: [
          connection({
            id: "okx-1",
            provider: "okx",
            label: "OKX paper",
            settings: { site: "my.okx.com" },
            secrets: { apiKey: true, apiSecret: true, passphrase: true },
          }),
        ],
      };
      renderTrading();
      const scope = await card("OKX paper");
      await user.click(scope.getByRole("button", { name: "Edit settings" }));
      const dialog = within(screen.getByRole("dialog"));
      const save = dialog.getByRole("button", { name: "Save" });
      expect(save).toBeDisabled();
      const site = dialog.getByRole("combobox", { name: "OKX site" });
      expect(site).toHaveTextContent("EEA account (my.okx.com)");
      for (const name of ["API key", "API secret", "Passphrase"]) {
        const input = dialog.getByLabelText(name, { exact: true });
        expect(input).toHaveValue("");
        expect(input).toHaveAttribute("type", "password");
        expect(input).toHaveAttribute("autocomplete", "new-password");
        expect(input).toHaveAttribute("placeholder", "Keep stored secret");
        await user.type(input, "   ");
      }
      expect(save).toBeDisabled();
      if (field === "site") {
        await chooseOKXSite(
          user,
          site,
          "US or Australian account (app.okx.com)",
        );
        expect(save).toBeEnabled();
        await chooseOKXSite(user, site, "EEA account (my.okx.com)");
        expect(save).toBeDisabled();
        await chooseOKXSite(
          user,
          site,
          "US or Australian account (app.okx.com)",
        );
      } else {
        await user.type(
          dialog.getByLabelText("Passphrase"),
          "fixture-replacement  ",
        );
      }
      expect(save).toBeEnabled();
      await user.click(save);
      expect(
        api.updateTradingConnectionSettings,
      ).toHaveBeenCalledExactlyOnceWith("okx-1", {
        credentials:
          field === "site"
            ? { site: "app.okx.com" }
            : { passphrase: "fixture-replacement" },
      });
      await user.click(scope.getByRole("button", { name: "Edit settings" }));
      const reopened = within(screen.getByRole("dialog"));
      expect(reopened.getByLabelText("Passphrase")).toHaveValue("");
      expect(reopened.getByRole("button", { name: "Save" })).toBeDisabled();
    },
  );

  it.each([true, false])(
    "shows the OKX site and only secret presence flags (%s)",
    async (stored) => {
      snapshot = {
        providers: [okx],
        connections: [
          connection({
            provider: "okx",
            label: "OKX paper",
            settings: {
              site: "app.okx.com",
              apiKey: "fixture-hidden-key",
              apiSecret: "fixture-hidden-secret",
              passphrase: "fixture-hidden-passphrase",
            },
            secrets: { apiKey: stored, apiSecret: !stored, passphrase: stored },
          }),
        ],
      };
      renderTrading();
      const scope = await card("OKX paper");
      expect(
        scope.getByText("Site: app.okx.com", { exact: true }),
      ).toBeInTheDocument();
      for (const [name, flag] of [
        ["API key", stored],
        ["API secret", !stored],
        ["Passphrase", stored],
      ] as const) {
        expect(
          scope.getByText(`${name}: ${flag ? "stored" : "not set"}`, {
            exact: true,
          }),
        ).toBeInTheDocument();
      }
      expect(
        screen
          .getByRole("region", { name: "OKX paper" })
          .textContent?.includes("fixture-hidden-"),
      ).toBe(false);
    },
  );

  it.each([
    ["trading_access_notification_failed", 409, false],
    ["trading_access_notification_failed", 409, true],
    ["trading_access_completion_failed", 500, false],
    ["trading_access_completion_failed", 500, true],
  ] as const)(
    "re-reads a saved Test connection after %s (%i) and prevents another create (reload fails=%s)",
    async (code, status, reloadFails) => {
      setAccountScope({
        accounts: false,
        account: "main",
        accountPnlReset: false,
      });
      snapshot = {
        providers: [
          { ...provider, type: "test", title: "Test", executionPolicy: true },
        ],
        connections: [],
      };
      const message =
        code === "trading_access_notification_failed"
          ? "access change was saved, but its mixed paper/live warning could not be published or delivered; review current access and alerts before retrying"
          : "access change was saved, but operation completion failed; review current access, alerts and service status before retrying";
      const transport = vi.fn<typeof fetch>(async (_input, init) => {
        const body = JSON.parse(init!.body as string) as { label: string };
        snapshot = {
          ...snapshot,
          connections: [
            ...snapshot.connections,
            connection({
              id: "test-created",
              label: body.label,
              provider: "test",
              executionPolicy: true,
              secrets: {},
            }),
          ],
        };
        return jsonResponse({ error: { code, message } }, status);
      });
      const realApi = createOfficerApi(
        createApiClient({ baseUrl: "/app/api/v1", fetch: transport }),
      );
      api.createTradingConnection.mockImplementation(
        realApi.createTradingConnection,
      );
      let failReload = reloadFails;
      api.fetchTrading.mockImplementation(async () => {
        if (failReload && transport.mock.calls.length !== 0) {
          throw new Error("Saved connection read failed");
        }
        return snapshot;
      });
      api.fetchTestExecutionPolicy.mockResolvedValue({
        connection: "test-created",
        revision: "1",
        rules: [],
      });
      const user = userEvent.setup();
      renderTrading();
      const dialog = await openProviderCreate(user, "Test");
      await user.clear(dialog.getByLabelText("Label"));
      await user.type(dialog.getByLabelText("Label"), "Test saved");
      const reads = api.fetchTrading.mock.calls.length;
      await user.click(dialog.getByRole("button", { name: "Create" }));
      expect(await dialog.findByRole("alert")).toHaveTextContent(message);
      await waitFor(() =>
        expect(api.fetchTrading.mock.calls.length).toBeGreaterThan(reads),
      );
      expect(dialog.getByRole("button", { name: "Create" })).toBeDisabled();
      await user.type(dialog.getByLabelText("Label"), "{Enter}");
      expect(transport).toHaveBeenCalledTimes(1);
      expect(dialog.getAllByText(message)).toHaveLength(1);
      await user.click(dialog.getByRole("button", { name: "Cancel" }));
      if (reloadFails) {
        expect(
          await screen.findByText("Saved connection read failed"),
        ).toBeVisible();
        failReload = false;
        await user.click(screen.getByRole("button", { name: "Retry" }));
      }
      expect(
        await screen.findByRole("region", { name: "Test saved" }),
      ).toBeVisible();
      expect(snapshot.connections).toHaveLength(1);
    },
  );

  it("creates a test connection with only a label, no keys or trading mode", async () => {
    const user = userEvent.setup();
    credentialStorage = storage(false);
    snapshot = {
      providers: [
        { ...provider, type: "test", title: "Test", executionPolicy: true },
      ],
      connections: [],
    };
    api.createTradingConnection.mockResolvedValue(
      connection({
        id: "test-new",
        provider: "test",
        executionPolicy: true,
        label: "Test desk",
        secrets: {},
      }),
    );
    renderTrading();
    const dialog = await openProviderCreate(user, "Test");
    expect(dialog.queryByText(unknownCredentials)).not.toBeInTheDocument();
    expect(
      dialog.queryByText("Keys will be stored unencrypted"),
    ).not.toBeInTheDocument();
    expect(dialog.queryByRole("alert")).not.toBeInTheDocument();
    expect(
      dialog.queryByRole("button", { name: "Live" }),
    ).not.toBeInTheDocument();
    expect(
      dialog.queryByRole("button", { name: "Paper" }),
    ).not.toBeInTheDocument();
    expect(dialog.getAllByRole("listitem")).toHaveLength(1);
    expect(
      dialog.getByText(
        "This connection executes test orders locally and needs no keys. After creating it, grant access to a test account, configure symbol masks and their execution rules, then enable the connection.",
      ),
    ).toBeVisible();
    expect(dialog.queryByRole("link")).not.toBeInTheDocument();
    const create = dialog.getByRole("button", { name: "Create" });
    expect(create).toBeEnabled();
    await user.clear(dialog.getByLabelText("Label"));
    expect(create).toBeDisabled();
    await user.type(dialog.getByLabelText("Label"), "Test desk");
    expect(create).toBeEnabled();
    await user.click(create);
    expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
      provider: "test",
      label: "Test desk",
      mode: "paper",
      credentials: {},
      enabled: false,
    });
  });

  it("keeps the unsealed-keys warning and Live option for a venue provider", async () => {
    const user = userEvent.setup();
    credentialStorage = storage(false);
    renderTrading();
    const dialog = await openProviderCreate(user, "Alpaca");
    expect(dialog.getByRole("alert")).toHaveTextContent(
      "Keys will be stored unencrypted",
    );
    await user.click(dialog.getByRole("button", { name: "Live" }));
    expect(dialog.getByRole("button", { name: "Live" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("hides credential warnings and the keep-values hint in test settings", async () => {
    const user = userEvent.setup();
    credentialStorage = storage(false);
    snapshot = {
      providers: [
        { ...provider, type: "test", title: "Test", executionPolicy: true },
      ],
      connections: [
        connection({
          id: "test-1",
          provider: "test",
          executionPolicy: true,
          label: "Test desk",
          secrets: {},
        }),
      ],
    };
    api.fetchTestExecutionPolicy.mockResolvedValue({
      connection: "test-1",
      revision: "1",
      rules: [],
    });
    renderTrading();
    const scope = await card("Test desk");
    await user.click(scope.getByRole("button", { name: "Edit settings" }));
    const dialog = within(screen.getByRole("dialog"));
    expect(dialog.queryByText(unknownCredentials)).not.toBeInTheDocument();
    expect(
      dialog.queryByText("Keys will be stored unencrypted"),
    ).not.toBeInTheDocument();
    expect(
      dialog.queryByText("Leave fields blank to keep the stored values."),
    ).not.toBeInTheDocument();
    expect(dialog.queryByRole("alert")).not.toBeInTheDocument();
    await user.clear(dialog.getByLabelText("Label"));
    await user.type(dialog.getByLabelText("Label"), "Renamed test desk");
    await user.click(dialog.getByRole("button", { name: "Save" }));
    expect(api.updateTradingConnectionSettings).toHaveBeenCalledExactlyOnceWith(
      "test-1",
      { label: "Renamed test desk" },
    );
  });

  it.each([
    {
      venue: etoro,
      labels: ["Public API key", "User key"],
      credentials: { apiKey: "public-key", userKey: "user-key" },
    },
    {
      venue: provider,
      labels: ["API key", "API secret"],
      credentials: { apiKey: "public-key", apiSecret: "user-key" },
    },
  ])(
    "requires every $venue.title credential and the label, then creates with exactly its trimmed fields",
    async ({ venue, labels, credentials }) => {
      const user = userEvent.setup();
      snapshot = { providers: [venue], connections: [] };
      renderTrading();
      const dialog = await openProviderCreate(user, venue.title);
      const create = dialog.getByRole("button", { name: "Create" });
      const label = dialog.getByLabelText("Label");
      expect(create).toBeDisabled();
      expect(label).toBeRequired();
      if (venue.type === "etoro") {
        expect(dialog.queryByLabelText("API secret")).not.toBeInTheDocument();
      }
      for (const name of labels) {
        const input = dialog.getByLabelText(name, { exact: true });
        expect(input).toBeRequired();
        expect(input).toHaveAttribute("type", "password");
        expect(input).toHaveAttribute("autocomplete", "new-password");
      }
      await user.type(dialog.getByLabelText(labels[0]), "  public-key  ");
      expect(create).toBeDisabled();
      await user.type(dialog.getByLabelText(labels[1]), "   ");
      expect(create).toBeDisabled();
      await user.type(dialog.getByLabelText(labels[1]), "user-key  ");
      expect(create).toBeEnabled();
      await user.clear(label);
      await user.type(label, "   ");
      expect(create).toBeDisabled();
      await user.type(label, "Desk paper  ");
      expect(create).toBeEnabled();
      await user.clear(dialog.getByLabelText(labels[0]));
      expect(create).toBeDisabled();
      await user.type(dialog.getByLabelText(labels[0]), "  public-key  ");
      expect(dialog.queryByText("public-key")).not.toBeInTheDocument();
      expect(dialog.queryByText("user-key")).not.toBeInTheDocument();
      await user.click(create);
      expect(api.createTradingConnection).toHaveBeenCalledExactlyOnceWith({
        provider: venue.type,
        label: "Desk paper",
        mode: "paper",
        credentials,
        enabled: false,
      });
    },
  );

  it.each([
    {
      field: "Public API key",
      key: "apiKey",
      secrets: { apiKey: true, userKey: true },
    },
    {
      field: "User key",
      key: "userKey",
      secrets: { apiKey: false, userKey: true },
    },
    {
      field: "User key",
      key: "userKey",
      secrets: { apiKey: true, userKey: false },
    },
  ])(
    "edits only the filled eToro $field with stored flags $secrets",
    async ({ field, key, secrets }) => {
      const user = userEvent.setup();
      snapshot = {
        providers: [etoro],
        connections: [
          connection({
            id: "etoro-1",
            provider: "etoro",
            label: "eToro paper",
            secrets,
          }),
        ],
      };
      renderTrading();
      const scope = await card("eToro paper");
      await user.click(scope.getByRole("button", { name: "Edit settings" }));
      const dialog = within(screen.getByRole("dialog"));
      const save = dialog.getByRole("button", { name: "Save" });
      expect(save).toBeDisabled();
      expect(dialog.queryByLabelText("API secret")).not.toBeInTheDocument();
      for (const [name, stored] of [
        ["Public API key", secrets.apiKey],
        ["User key", secrets.userKey],
      ] as const) {
        const input = dialog.getByLabelText(name);
        expect(input).toHaveValue("");
        expect(input).toHaveAttribute("type", "password");
        expect(input).toHaveAttribute("autocomplete", "new-password");
        expect(input).toHaveAttribute(
          "placeholder",
          stored ? "Keep stored secret" : "",
        );
        await user.type(input, "   ");
      }
      expect(save).toBeDisabled();
      await user.type(dialog.getByLabelText(field), "replacement-key  ");
      await user.click(save);
      expect(
        api.updateTradingConnectionSettings,
      ).toHaveBeenCalledExactlyOnceWith("etoro-1", {
        credentials: { [key]: "replacement-key" },
      });
      await user.click(scope.getByRole("button", { name: "Edit settings" }));
      const reopened = within(screen.getByRole("dialog"));
      expect(reopened.getByLabelText("Public API key")).toHaveValue("");
      expect(reopened.getByLabelText("User key")).toHaveValue("");
      expect(screen.queryByText("replacement-key")).not.toBeInTheDocument();
    },
  );

  it.each([
    { apiKey: true, userKey: false },
    { apiKey: false, userKey: true },
  ])("shows only eToro credential presence with flags %j", async (secrets) => {
    snapshot = {
      providers: [etoro],
      connections: [
        connection({
          provider: "etoro",
          label: "eToro paper",
          secrets: { ...secrets, apiSecret: true },
        }),
      ],
    };
    renderTrading();
    const scope = await card("eToro paper");
    expect(
      scope.getByText(
        `Public API key: ${secrets.apiKey ? "stored" : "not set"}`,
        { exact: true },
      ),
    ).toBeInTheDocument();
    expect(
      scope.getByText(`User key: ${secrets.userKey ? "stored" : "not set"}`, {
        exact: true,
      }),
    ).toBeInTheDocument();
    expect(scope.queryByText(/^API (key|secret):/)).not.toBeInTheDocument();
  });

  it("shows the eToro mark, setup steps and authentication links", async () => {
    const user = userEvent.setup();
    snapshot = { providers: [etoro], connections: [] };
    renderTrading();
    await user.click(
      await screen.findByRole("button", { name: /^Add trading connection/ }),
    );
    const picker = within(screen.getByRole("dialog"));
    const tile = picker.getByRole("button", { name: "eToro" });
    const icon = tile.querySelector("svg");
    expect(icon).toHaveAttribute("viewBox", "0 0 24 24");
    expect(icon).toHaveAttribute("aria-hidden", "true");
    expect(icon?.querySelector("rect")).toHaveAttribute("fill", "#13C636");
    expect(icon?.querySelector("path")).toHaveAttribute("fill", "#fff");
    expect(picker.getByRole("link", { name: "Site" })).toHaveAttribute(
      "href",
      "https://www.etoro.com",
    );
    expect(picker.getByRole("link", { name: "Docs" })).toHaveAttribute(
      "href",
      "https://api-portal.etoro.com/core/getting-started/authentication",
    );
    await user.click(tile);
    const dialog = within(screen.getByRole("dialog"));
    const steps = dialog.getAllByRole("listitem");
    const texts = [
      "Log in to your eToro account.",
      "Create a user key: in eToro, open Settings, then Trading, scroll to API Key Management and click Create New Key. Choose the Demo environment - each key works for one environment only, and Officer connects the demo account in paper mode. Choose Write permission - Read only shows portfolio data and cannot place orders. Click Generate Key and enter the verification code eToro sends to your phone. API Key Management appears only once your eToro account is verified.",
      "Copy the new key from the Generated Keys list into the User key field.",
      "The Public API key identifies the application. Enter it in the Public API key field.",
      "Keep this dialog open - the fields below ask for both values.",
    ];
    expect(steps).toHaveLength(texts.length);
    texts.forEach((text, index) => {
      expect(within(steps[index]).getByText(text)).toBeInTheDocument();
    });
    for (const [name, href] of [
      ["Log in at eToro", "https://www.etoro.com/login"],
      [
        "eToro authentication guide",
        "https://api-portal.etoro.com/core/getting-started/authentication",
      ],
    ]) {
      const link = dialog.getByRole("link", { name });
      expect(link).toHaveAttribute("href", href);
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
    }
    expect(api.createTradingConnection).not.toHaveBeenCalled();
  });

  it.each(["other", "toString"])(
    "blocks creation for unknown provider %s without guessing credential fields",
    async (type) => {
      const user = userEvent.setup();
      snapshot = {
        providers: [{ ...provider, type, title: "Other venue" }],
        connections: [],
      };
      renderTrading();
      const dialog = await openProviderCreate(user, "Other venue");
      expect(dialog.getByRole("alert")).toHaveTextContent(unknownCredentials);
      expect(dialog.queryByLabelText(/key|secret/i)).not.toBeInTheDocument();
      const label = dialog.getByLabelText("Label");
      await user.type(label, " renamed");
      const create = dialog.getByRole("button", { name: "Create" });
      expect(create).toBeDisabled();
      await user.click(create);
      fireEvent.submit(label.closest("form")!);
      expect(api.createTradingConnection).not.toHaveBeenCalled();
    },
  );

  it.each(["other", "toString"])(
    "omits credential card lines and blocks settings for unknown provider %s",
    async (type) => {
      const user = userEvent.setup();
      snapshot = {
        providers: [{ ...provider, type, title: "Other venue" }],
        connections: [
          connection({
            provider: type,
            label: "Other paper",
            secrets: { apiKey: true, apiSecret: true, userKey: true },
          }),
        ],
      };
      renderTrading();
      const scope = await card("Other paper");
      expect(
        scope.queryByText(/^(Public API key|API key|API secret|User key):/),
      ).not.toBeInTheDocument();
      await user.click(scope.getByRole("button", { name: "Edit settings" }));
      const dialog = within(screen.getByRole("dialog"));
      expect(dialog.getByRole("alert")).toHaveTextContent(unknownCredentials);
      expect(dialog.queryByLabelText(/key|secret/i)).not.toBeInTheDocument();
      const label = dialog.getByLabelText("Label");
      await user.type(label, " renamed");
      const save = dialog.getByRole("button", { name: "Save" });
      expect(save).toBeDisabled();
      await user.click(save);
      fireEvent.submit(label.closest("form")!);
      expect(api.updateTradingConnectionSettings).not.toHaveBeenCalled();
    },
  );
});
