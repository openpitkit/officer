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
  CredentialStorage,
  Group,
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
  type: "alpaca",
  title: "Alpaca",
  venueAccounts: false,
  verifiesSymbols: true,
  searchesSymbols: true,
  unlinkedFees: true,
};
const api = {
  fetchTrading: vi.fn(),
  fetchCredentialStorage: vi.fn(),
  fetchAccounts: vi.fn(),
  fetchGroups: vi.fn(),
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
  createAsset: vi.fn(),
};
let snapshot: TradingSnapshot;
let credentialStorage: CredentialStorage;
let accounts: string[];
let groups: string[];

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
    diagnostics: [],
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

async function card(label = "Alpaca paper") {
  return within(await screen.findByRole("region", { name: label }));
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

beforeEach(async () => {
  vi.resetAllMocks();
  await i18n.changeLanguage("en");
  snapshot = {
    providers: [provider],
    connections: [connection()],
  };
  credentialStorage = storage(true);
  accounts = ["alpha", "beta", "fees"];
  api.fetchTrading.mockImplementation(async () => snapshot);
  api.fetchCredentialStorage.mockImplementation(async () => credentialStorage);
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
      await user.type(form.getByPlaceholderText("Quote"), "USD");
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
    for (const badge of ["Alpaca", "Paper", "Unavailable", "Disabled"])
      expect(scope.getByText(badge, { exact: true })).toBeInTheDocument();
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
      screen.getAllByRole("button", { name: /^Add trading connection/ }),
    ).toHaveLength(1);
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

  it("wires the instrument blocks in MD order without a draft enabled control", async () => {
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
        draft: { externalSymbol: "", baseAsset: "", quoteAsset: "" },
        valid: false,
        busy: false,
        baseSuggestions: [],
        quoteSuggestions: [],
      }),
    );
    expect(venueWiring.draft.mock.lastCall![0]).not.toHaveProperty("enabled");
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
    "saves a trimmed symbol with existing=%s, preserving its state or enabling it as new",
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
          enabled: !existing,
        },
      );
      await waitFor(() => expect(symbol).toHaveValue(""));
      expect(base).toHaveValue("");
      expect(quote).toHaveValue("");
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
    await user.type(form.getByPlaceholderText("Quote"), "USD");
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

  it("shows a save refusal naming an unrelated asset as the server message", async () => {
    const user = userEvent.setup();
    api.upsertTradingInstrument.mockRejectedValue(
      new ApiError(
        "Asset EUR is missing",
        "asset_missing",
        404,
        undefined,
        undefined,
        "EUR",
      ),
    );
    renderTrading();
    const scope = await card();
    await user.type(scope.getByPlaceholderText("External symbol"), "BTC/USD");
    await user.type(scope.getByPlaceholderText("Base"), "BTC");
    await user.type(scope.getByPlaceholderText("Quote"), "USD");
    await user.click(scope.getByRole("button", { name: "Add instrument" }));
    expect(await scope.findByText("Asset EUR is missing")).toBeInTheDocument();
    expect(
      scope.queryByRole("button", { name: "Create asset EUR and save" }),
    ).not.toBeInTheDocument();
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

  it("offers to create the missing draft base asset BTC and save", async () => {
    const user = userEvent.setup();
    api.upsertTradingInstrument.mockRejectedValue(
      new ApiError(
        "BTC missing",
        "asset_missing",
        404,
        undefined,
        undefined,
        "BTC",
      ),
    );
    renderTrading();
    const scope = await card();
    await user.type(scope.getByPlaceholderText("External symbol"), "BTC/USD");
    await user.type(scope.getByPlaceholderText("Base"), "BTC");
    await user.type(scope.getByPlaceholderText("Quote"), "USD");
    await user.click(scope.getByRole("button", { name: "Add instrument" }));
    expect(
      await scope.findByRole("button", { name: "Create asset BTC and save" }),
    ).toBeInTheDocument();
    expect(api.createAsset).not.toHaveBeenCalled();
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
    await user.type(scope.getByPlaceholderText("Quote"), "USD");
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
    const second = await card("Second connection");
    await user.type(
      first.getByRole("textbox", { name: "Resolve symbol at this venue" }),
      "BTC",
    );
    await user.click(first.getByRole("button", { name: "Resolve" }));
    await user.click(await first.findByRole("button", { name: "BTC/USD" }));
    await user.click(first.getByRole("button", { name: "Verify symbol" }));
    expect(await first.findByText("First card refused")).toBeInTheDocument();
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
    expect(first.getByRole("button", { name: "BTC/USD" })).toBeInTheDocument();
    api.verifyTradingSymbol.mockRejectedValueOnce(
      new ApiError("Second card refused", "upstream", 502),
    );
    await user.type(second.getByPlaceholderText("External symbol"), "AAPL");
    await user.click(second.getByRole("button", { name: "Verify symbol" }));
    expect(await second.findByText("Second card refused")).toBeInTheDocument();
    expect(first.queryByText("Second card refused")).not.toBeInTheDocument();
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

  it("hides the missing-asset offer when the draft no longer uses that asset", async () => {
    const user = userEvent.setup();
    api.upsertTradingInstrument.mockRejectedValue(
      new ApiError(
        "USDT missing",
        "asset_missing",
        404,
        undefined,
        undefined,
        "USDT",
      ),
    );
    renderTrading();
    const scope = await card();
    await user.type(scope.getByPlaceholderText("External symbol"), "BTC/USDT");
    await user.type(scope.getByPlaceholderText("Base"), "BTC");
    const quote = scope.getByPlaceholderText("Quote");
    await user.type(quote, "USDT");
    await user.click(scope.getByRole("button", { name: "Add instrument" }));
    expect(
      await scope.findByRole("button", { name: "Create asset USDT and save" }),
    ).toBeInTheDocument();
    await user.type(quote, " ");
    expect(
      scope.getByRole("button", { name: "Create asset USDT and save" }),
    ).toBeInTheDocument();
    await user.clear(quote);
    await user.type(quote, "USD");
    expect(
      scope.queryByRole("button", { name: "Create asset USDT and save" }),
    ).not.toBeInTheDocument();
    expect(api.createAsset).not.toHaveBeenCalled();
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
    await user.type(form.getByPlaceholderText("Quote"), "USD");
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
    await user.type(form.getByPlaceholderText("Quote"), "USD");
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

  it("offers to create a missing asset by its code and repeats the save", async () => {
    const user = userEvent.setup();
    api.upsertTradingInstrument
      .mockRejectedValueOnce(
        new ApiError(
          'upsert trading instrument: asset "USDT" does not exist',
          "asset_missing",
          404,
          undefined,
          undefined,
          "USDT",
        ),
      )
      .mockResolvedValueOnce(snapshot);
    api.createAsset.mockResolvedValue({
      code: "USDT",
      title: "",
      assetClass: "",
    });
    renderTrading();
    const scope = await card();
    const form = within(scope.getByRole("region", { name: "Instruments" }));
    await user.type(form.getByPlaceholderText("External symbol"), "BTC/USDT");
    await user.type(form.getByPlaceholderText("Base"), "BTC");
    await user.type(form.getByPlaceholderText("Quote"), "USDT");
    await user.click(form.getByRole("button", { name: "Add instrument" }));
    const notice = await form.findByRole("alert");
    expect(notice).toHaveTextContent("Asset USDT does not exist in Officer.");
    expect(
      scope.queryByText(/upsert trading instrument/),
    ).not.toBeInTheDocument();
    expect(api.createAsset).not.toHaveBeenCalled();
    const symbol = form.getByPlaceholderText("External symbol");
    const createAndSave = within(notice).getByRole("button", {
      name: "Create asset USDT and save",
    });
    await user.clear(symbol);
    expect(createAndSave).toBeDisabled();
    await user.type(symbol, "BTC/USDT");
    await user.click(createAndSave);
    await waitFor(() =>
      expect(form.getByPlaceholderText("External symbol")).toHaveValue(""),
    );
    expect(api.createAsset).toHaveBeenCalledExactlyOnceWith("USDT", "", "");
    expect(api.upsertTradingInstrument).toHaveBeenCalledTimes(2);
    expect(api.upsertTradingInstrument).toHaveBeenLastCalledWith("alpaca-1", {
      externalSymbol: "BTC/USDT",
      baseAsset: "BTC",
      quoteAsset: "USDT",
      enabled: true,
    });
    expect(api.createAsset.mock.invocationCallOrder[0]).toBeLessThan(
      api.upsertTradingInstrument.mock.invocationCallOrder[1],
    );
    expect(form.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows a missing asset refused outside the draft as the server message", async () => {
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
      new ApiError(
        'asset "USDT" does not exist',
        "asset_missing",
        404,
        undefined,
        undefined,
        "USDT",
      ),
    );
    renderTrading();
    const scope = await card();
    await user.click(scope.getByRole("button", { name: "Disable" }));
    expect(
      await scope.findByText('asset "USDT" does not exist'),
    ).toBeInTheDocument();
    expect(api.createAsset).not.toHaveBeenCalled();
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
    const other = await card("Other paper");
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
      other.getByRole("combobox", { name: "Fee account" }),
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
    const other = await card("Other paper");
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
      other.getByRole("combobox", { name: "Fee account" }),
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
      "Generate paper API keys: with Paper Trading selected, find the API Keys panel on the dashboard and click Generate New Key. Copy the API key and the secret - Alpaca shows the secret only once. Paper keys work only for the paper account.",
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
    expect(mode.getByRole("button", { name: "Live" })).toBeDisabled();
    expect(
      dialog.getByText(
        "Live trading is not available yet: it arrives together with the live connection token.",
      ),
    ).toBeInTheDocument();
    for (const name of ["API key", "API secret"]) {
      expect(dialog.getByLabelText(name)).toHaveAttribute("type", "password");
      expect(dialog.getByLabelText(name)).toHaveAttribute(
        "autocomplete",
        "new-password",
      );
    }
    expect(api.createTradingConnection).not.toHaveBeenCalled();
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

  it("shows a failed credential storage load and keeps key entry enabled", async () => {
    const user = userEvent.setup();
    api.fetchCredentialStorage.mockRejectedValue(
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
