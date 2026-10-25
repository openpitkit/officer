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
import {
  accountRow,
  answeringAccountMode,
  jsonResponse,
  renderWithApi as render,
} from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { useState } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import i18n from "@/i18n";
import { setAccountScope } from "@/lib/accountScope";
import { checkSession, setPanelSession } from "@/lib/panelSession";
import { SidebarProvider } from "@/components/SidebarContext";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";
import type { AccountModeState } from "@/api/accountMode";
import { fetchCredentialStorage } from "@/api/credentialStorage";
import type {
  CredentialStorage,
  ServiceInfo,
  TradingSnapshot,
} from "@/api/types";
import type { OfficerApi } from "@/framework";
import {
  BackupCard,
  DatabaseCard,
  Service,
  ServiceCard,
  ServiceLifecycleCard,
} from "@/pages/Service";

// The startup gate that answers a session check is tested on its own.
vi.mock("@/lib/panelSession", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/panelSession")>(
      "@/lib/panelSession",
    );
  return { ...actual, checkSession: vi.fn() };
});

vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));

const exportBackupMock = vi.fn();
const resetDatabaseMock = vi.fn();
const restoreBackupMock = vi.fn();
const fetchTradingMock = vi.fn<() => Promise<TradingSnapshot>>();
vi.mock("@/api/credentialStorage");
const fetchCredentialStorageMock = vi.mocked(fetchCredentialStorage);
const serviceInfo: ServiceInfo = {
  name: "Pit Officer",
  engineVersion: "0.9.0",
  engineBuildProfile: "",
  release: true,
  database: { path: "/tmp/officer.db", reachable: true },
};
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

const warningTitle = "This backup will contain keys in plain text";
const tradingKeysLine =
  "API keys of trading connections go into the archive unencrypted.";
const marketDataKeysLine =
  "API keys and tokens of market-data sources go into the archive unencrypted.";
const warningAdvice = "Keep the file private, or set a master key first.";

const sealedStorage: CredentialStorage = {
  credentialsSealed: true,
  plaintextTradingKeys: false,
  plaintextMarketDataKeys: false,
  sealedAt: null,
  resealedSecrets: 0,
};

function unsealedStorage(
  plaintextTradingKeys: boolean,
  plaintextMarketDataKeys: boolean,
): CredentialStorage {
  return {
    credentialsSealed: false,
    plaintextTradingKeys,
    plaintextMarketDataKeys,
    sealedAt: null,
    resealedSecrets: 0,
  };
}

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
  fetchCredentialStorageMock.mockResolvedValue(sealedStorage);
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

const runningSingle = { accounts: false, account: "main" };
const runningMulti = { accounts: true, account: "" };
// Stored multi while the process runs single: a restart is pending.
const waitingMulti: AccountModeState = {
  mode: "multi",
  account: "",
  features: runningSingle,
  restartRequired: true,
};
// Stored single on an account deleted since, while the process runs multi.
const waitingOldAccount: AccountModeState = {
  mode: "single",
  account: "old",
  features: runningMulti,
  restartRequired: true,
};
const mainAccount = accountRow("main");
const noticeTitle = "Restart needed to apply the account mode";

describe("ServiceCard", () => {
  it("switches each service tab to its matching panel and reports its id", async () => {
    const user = userEvent.setup();
    const onTabChange = vi.fn();

    function ControlledServiceCard() {
      const [activeTab, setActiveTab] = useState<
        | "application"
        | "account-mode"
        | "api"
        | "database"
        | "logs"
        | "security"
      >("application");
      return (
        <ServiceCard
          info={serviceInfo}
          activeTab={activeTab}
          onTabChange={(tab) => {
            onTabChange(tab);
            setActiveTab(tab);
          }}
          onDatabaseReset={vi.fn()}
          lifecycleBusyAction={null}
          lifecycleError={null}
          onLifecycleAction={vi.fn().mockResolvedValue(undefined)}
          onAccountModeRestart={vi.fn().mockResolvedValue(undefined)}
        />
      );
    }

    render(
      <I18nextProvider i18n={i18n}>
        <MemoryRouter>
          <ControlledServiceCard />
        </MemoryRouter>
      </I18nextProvider>,
      {
        api: {
          getMcpCommands: vi.fn().mockResolvedValue([]),
          fetchServiceLogs: vi
            .fn()
            .mockResolvedValue({ lines: ["Service log"] }),
          fetchTrading: fetchTradingMock,
        },
        fetch: answeringAccountMode({
          ...waitingMulti,
          restartRequired: false,
        }) as unknown as typeof fetch,
      },
    );

    for (const [id, name] of [
      ["api", "API"],
      ["database", "Database"],
      ["logs", "Logs"],
      ["account-mode", "Account mode"],
      ["application", "Application"],
    ]) {
      onTabChange.mockClear();
      const tab = screen.getByRole("tab", { name });
      await user.click(tab);
      expect(onTabChange).toHaveBeenLastCalledWith(id);
      expect(onTabChange.mock.calls.every(([tab]) => tab === id)).toBe(true);
      expect(tab).toHaveAttribute("aria-selected", "true");
      const panel = screen.getByRole("tabpanel", { name });
      expect(tab).toHaveAttribute("aria-controls", panel.id);
      expect(
        within(panel).getByText(name, { exact: true }),
      ).toBeInTheDocument();
    }
  });
});

describe("Service", () => {
  function LocationProbe() {
    const location = useLocation();
    return (
      <output aria-label="Current location">
        {location.pathname + location.hash}
      </output>
    );
  }

  function renderService(
    entry: string,
    api: Partial<OfficerApi> = {},
    fetch?: (url: string, init: RequestInit) => Promise<Response>,
  ) {
    render(
      <I18nextProvider i18n={i18n}>
        <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
          <DisplayPreferencesProvider
            densityStorageKey="pit-test-density"
            tradeStyleStorageKey="pit-test-style"
          >
            <MemoryRouter initialEntries={[entry]}>
              <SidebarProvider>
                <Service />
              </SidebarProvider>
              <LocationProbe />
            </MemoryRouter>
          </DisplayPreferencesProvider>
        </ThemeProvider>
      </I18nextProvider>,
      {
        api: {
          fetchServiceInfo: vi.fn().mockResolvedValue(serviceInfo),
          fetchTrading: fetchTradingMock,
          fetchSigningKeys: vi
            .fn()
            .mockResolvedValue({ keys: [], eSignEnabled: false }),
          getMcpCommands: vi.fn().mockResolvedValue([]),
          fetchServiceLogs: vi
            .fn()
            .mockResolvedValue({ lines: ["Service log"] }),
          ...api,
        },
        fetch: fetch as unknown as typeof globalThis.fetch | undefined,
      },
    );
  }

  async function renderAtLogsHash() {
    vi.spyOn(window, "requestAnimationFrame").mockReturnValue(0);
    renderService("/service#logs");

    const logsTab = await screen.findByRole("tab", { name: "Logs" });
    expect(logsTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel", { name: "Logs" })).toBeInTheDocument();
    expect(await screen.findByText("Service log")).toBeInTheDocument();
    expect(screen.getByLabelText("Current location")).toHaveTextContent(
      "/service#logs",
    );
    return logsTab;
  }

  function expectHashClearedOn(name: string) {
    expect(screen.getByLabelText("Current location")).toHaveTextContent(
      /^\/service$/,
    );
    expect(screen.getByRole("tab", { name })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("tabpanel", { name })).toBeInTheDocument();
  }

  it.each(["Application", "Logs"])(
    "shows Logs from the hash and clears it when clicking %s",
    async (name) => {
      const user = userEvent.setup();
      await renderAtLogsHash();

      await user.click(screen.getByRole("tab", { name }));

      expectHashClearedOn(name);
    },
  );

  describe("logs download", () => {
    const DOWNLOAD_URL = "/app/api/v1/service/logs/download";

    // Answers the logs download with `answer`; nothing else is served.
    function servingDownload(answer: () => Response) {
      return vi.fn<(url: string, init: RequestInit) => Promise<Response>>(
        async (url) => {
          if (url !== DOWNLOAD_URL) {
            throw new TypeError("connection refused");
          }
          return answer();
        },
      );
    }

    async function clickDownload(fetch: ReturnType<typeof servingDownload>) {
      const user = userEvent.setup();
      vi.spyOn(window, "requestAnimationFrame").mockReturnValue(0);
      renderService("/service#logs", {}, fetch);
      expect(await screen.findByText("Service log")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Download" }));
    }

    it("fetches the logs through the API client and saves them", async () => {
      const fetch = servingDownload(
        () =>
          new Response("first line\nsecond line\n", {
            status: 200,
            headers: {
              "Content-Type": "text/plain; charset=utf-8",
              "Content-Disposition": 'attachment; filename="pit-officer.log"',
            },
          }),
      );

      await clickDownload(fetch);

      await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalledTimes(1));
      const download = fetch.mock.calls.find(([url]) => url === DOWNLOAD_URL);
      expect(download?.[1]).toMatchObject({
        method: "GET",
        headers: { Accept: "text/plain" },
      });
      const blob = vi.mocked(URL.createObjectURL).mock.calls[0][0] as Blob;
      expect(blob.size).toBe("first line\nsecond line\n".length);
      const click = vi.mocked(HTMLAnchorElement.prototype.click);
      expect(click).toHaveBeenCalledTimes(1);
      expect((click.mock.contexts[0] as HTMLAnchorElement).download).toBe(
        "pit-officer.log",
      );
    });

    it("shows a failed download in the logs card", async () => {
      const fetch = servingDownload(() =>
        jsonResponse(
          { error: { code: "internal", message: "log buffer unavailable" } },
          500,
        ),
      );

      await clickDownload(fetch);

      const panel = screen.getByRole("tabpanel", { name: "Logs" });
      expect(await within(panel).findByRole("alert")).toHaveTextContent(
        "log buffer unavailable",
      );
      expect(URL.createObjectURL).not.toHaveBeenCalled();
      expect(screen.getByRole("button", { name: "Download" })).toBeEnabled();
    });
  });

  it.each(["{Enter}", " "])(
    "clears the hash when %s activates the Logs tab selected by it",
    async (key) => {
      const user = userEvent.setup();
      const logsTab = await renderAtLogsHash();
      act(() => logsTab.focus());

      await user.keyboard(key);

      expectHashClearedOn("Logs");
    },
  );

  function noticeRestartButton() {
    return screen.getByRole("button", { name: "Restart service" });
  }

  async function confirmDialog(
    user: ReturnType<typeof userEvent.setup>,
    name: string,
  ) {
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name }));
  }

  it("shows the account mode tab from the hash and clears it on another tab", async () => {
    const user = userEvent.setup();
    renderService(
      "/service#account-mode",
      {},
      answeringAccountMode({ ...waitingMulti, restartRequired: false }),
    );

    const tab = await screen.findByRole("tab", { name: "Account mode" });
    expect(tab).toHaveAttribute("aria-selected", "true");
    const panel = screen.getByRole("tabpanel", { name: "Account mode" });
    expect(await within(panel).findByTitle("Several accounts")).toBeVisible();
    expect(
      within(panel).getByRole("group", { name: "Account mode" }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Application" }));

    expectHashClearedOn("Application");
  });

  it("shows the security tab from the hash and clears it on another tab", async () => {
    const user = userEvent.setup();
    setPanelSession("open");
    renderService("/service#security");

    const tab = await screen.findByRole("tab", { name: "Security" });
    expect(tab).toHaveAttribute("aria-selected", "true");
    const panel = screen.getByRole("tabpanel", { name: "Security" });
    const access = within(panel).getByRole("group", { name: "Access" });
    expect(
      within(access).getByRole("radio", { name: "No token" }),
    ).toBeChecked();

    await user.click(screen.getByRole("tab", { name: "Application" }));

    expectHashClearedOn("Application");
  });

  it("opens the Access section and the storage section under it from the security tab", async () => {
    const user = userEvent.setup();
    setPanelSession("open");
    renderService("/service");

    await user.click(await screen.findByRole("tab", { name: "Security" }));

    const panel = screen.getByRole("tabpanel", { name: "Security" });
    expect(
      within(panel).getByRole("radiogroup", { name: "Who may use Officer" }),
    ).toBeInTheDocument();
    expect(
      within(panel)
        .getAllByRole("heading")
        .map((heading) => heading.textContent),
    ).toEqual(["Access", "Storage, signing and accounts", "Guide"]);
    expect(
      await within(
        within(panel).getByRole("group", { name: "Encryption" }),
      ).findByText("On"),
    ).toBeInTheDocument();
  });

  it.each([true, false])(
    "shows the restart notice above every tab only when restartRequired is %s",
    async (restartRequired) => {
      const user = userEvent.setup();
      renderService(
        "/service#account-mode",
        {},
        answeringAccountMode({ ...waitingMulti, restartRequired }),
      );
      const panel = await screen.findByRole("tabpanel", {
        name: "Account mode",
      });
      await within(panel).findByTitle("Several accounts");

      expect(screen.queryByText(noticeTitle) !== null).toBe(restartRequired);
      await user.click(screen.getByRole("tab", { name: "Application" }));
      expect(screen.queryByText(noticeTitle) !== null).toBe(restartRequired);
    },
  );

  it("restarts from the notice only after confirming, then blocks the choice", async () => {
    const user = userEvent.setup();
    const restartService = vi.fn().mockResolvedValue(undefined);
    renderService(
      "/service#account-mode",
      { restartService },
      answeringAccountMode(waitingMulti),
    );
    await user.click(
      await screen.findByRole("button", { name: "One account" }),
    );
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();

    await user.click(noticeRestartButton());
    expect(
      await screen.findByRole("alertdialog", { name: "Restart service?" }),
    ).toBeInTheDocument();
    expect(restartService).not.toHaveBeenCalled();
    await confirmDialog(user, "Restart service");

    await waitFor(() => expect(restartService).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(noticeRestartButton()).toBeDisabled());
    expect(
      screen.getByRole("button", { name: "Several accounts" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("restarts a saved choice under the page's lifecycle busy state", async () => {
    const user = userEvent.setup();
    const restartService = vi.fn().mockResolvedValue(undefined);
    renderService(
      "/service#account-mode",
      {
        restartService,
        fetchAccounts: vi.fn().mockResolvedValue([mainAccount]),
      },
      answeringAccountMode(waitingOldAccount, {
        ...waitingOldAccount,
        account: "main",
      }),
    );
    await screen.findByText("Officer acts on the account main only.");
    expect(noticeRestartButton()).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Save" }));
    await confirmDialog(user, "Save");

    expect(
      await screen.findByText(
        "Officer is restarting; the panel reloads when it is back.",
      ),
    ).toBeInTheDocument();
    expect(restartService).toHaveBeenCalledTimes(1);
    expect(noticeRestartButton()).toBeDisabled();
  });

  it("shows a refused restart of a saved choice once and offers the restart again", async () => {
    const user = userEvent.setup();
    const restartService = vi
      .fn()
      .mockRejectedValue(new Error("restart is not allowed"));
    renderService(
      "/service#account-mode",
      {
        restartService,
        fetchAccounts: vi.fn().mockResolvedValue([mainAccount]),
      },
      answeringAccountMode(waitingOldAccount, {
        ...waitingOldAccount,
        account: "main",
      }),
    );
    await screen.findByText("Officer acts on the account main only.");

    await user.click(screen.getByRole("button", { name: "Save" }));
    await confirmDialog(user, "Save");

    const alerts = await screen.findAllByRole("alert");
    expect(alerts).toHaveLength(1);
    expect(alerts[0]).toHaveTextContent("restart is not allowed");
    expect(noticeRestartButton()).toBeEnabled();
  });

  it("reloads the account mode right after a save that needs no restart", async () => {
    const user = userEvent.setup();
    renderService(
      "/service#account-mode",
      {},
      answeringAccountMode(waitingMulti, {
        mode: "single",
        account: "main",
        features: runningSingle,
        restartRequired: false,
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "One account" }),
    );
    expect(screen.getByText(noticeTitle)).toBeInTheDocument();
    expect(screen.getByText("Running mode")).toBeInTheDocument();

    // The running process already enforces this mode: nothing to confirm.
    await user.click(screen.getByRole("button", { name: "Save" }));

    // The poll ticks every 5 s, beyond waitFor's 1 s: only the reload the
    // save triggers can clear these in time.
    await waitFor(() =>
      expect(screen.queryByText(noticeTitle)).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("Running mode")).not.toBeInTheDocument();
    expect(screen.getByTitle("One account: main")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("shows a refused restart from the notice in the notice only", async () => {
    const user = userEvent.setup();
    const restartService = vi
      .fn()
      .mockRejectedValue(new Error("restart is not allowed"));
    renderService(
      "/service#account-mode",
      { restartService },
      answeringAccountMode(waitingMulti),
    );

    await user.click(
      await screen.findByRole("button", { name: "Restart service" }),
    );
    await confirmDialog(user, "Restart service");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "restart is not allowed",
    );
    expect(noticeRestartButton()).toBeEnabled();
    await user.click(screen.getByRole("tab", { name: "Application" }));
    expect(screen.getAllByText("restart is not allowed")).toHaveLength(1);
  });

  const accountModeUnavailable = () =>
    jsonResponse(
      { error: { code: "internal", message: "account mode is unavailable" } },
      500,
    );

  it("shows a failed account mode load and retries it", async () => {
    const user = userEvent.setup();
    const fetchMock = answeringAccountMode({
      ...waitingMulti,
      restartRequired: false,
    });
    fetchMock.mockImplementationOnce(async () => accountModeUnavailable());
    renderService("/service#account-mode", {}, fetchMock);

    const panel = await screen.findByRole("tabpanel", {
      name: "Account mode",
    });
    expect(
      await within(panel).findByText("Could not load the account mode"),
    ).toBeInTheDocument();
    expect(
      within(panel).getByText("account mode is unavailable"),
    ).toBeInTheDocument();

    await user.click(within(panel).getByRole("button", { name: "Retry" }));

    expect(await within(panel).findByTitle("Several accounts")).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("marks the account mode stale when a background poll fails and retries it", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const user = userEvent.setup();
      const fetchMock = answeringAccountMode({
        ...waitingMulti,
        restartRequired: false,
      });
      renderService("/service#account-mode", {}, fetchMock);
      const panel = await screen.findByRole("tabpanel", {
        name: "Account mode",
      });
      await within(panel).findByTitle("Several accounts");

      fetchMock.mockImplementationOnce(async () => accountModeUnavailable());
      act(() => {
        vi.advanceTimersByTime(5000);
      });

      const stale = await within(panel).findByRole("status", {
        name: "Stale data",
      });
      expect(stale).toHaveTextContent("account mode is unavailable");
      expect(within(panel).getByTitle("Several accounts")).toBeVisible();

      await user.click(within(stale).getByRole("button", { name: "Retry" }));

      await waitFor(() =>
        expect(
          within(panel).queryByRole("status", { name: "Stale data" }),
        ).not.toBeInTheDocument(),
      );
      expect(fetchMock).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("BackupCard", () => {
  it("fetches trading once per poll and keeps the last snapshot on refresh failure", async () => {
    vi.useFakeTimers();
    fetchTradingMock.mockResolvedValue(tradingSnapshot);
    const { unmount } = renderBackupCard();
    try {
      await act(async () => {});
      expect(fetchTradingMock).toHaveBeenCalledTimes(1);
      const allAccounts = screen.getByRole("checkbox", {
        name: "All accounts and groups",
      });
      expect(allAccounts).toBeDisabled();

      fetchTradingMock.mockRejectedValue(new Error("trading unavailable"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchTradingMock).toHaveBeenCalledTimes(2);
      expect(allAccounts).toBeDisabled();

      fetchTradingMock.mockResolvedValue({
        ...tradingSnapshot,
        connections: [],
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchTradingMock).toHaveBeenCalledTimes(3);
      expect(allAccounts).toBeEnabled();
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("warns about plain-text trading keys without blocking export", async () => {
    fetchCredentialStorageMock.mockResolvedValue(unsealedStorage(true, false));
    exportBackupMock.mockResolvedValue({
      blob: new Blob(["backup"]),
      filename: "backup.zip",
    });
    const user = userEvent.setup();
    renderBackupCard();

    expect(await screen.findByText(warningTitle)).toBeInTheDocument();
    expect(screen.getByText(tradingKeysLine)).toBeInTheDocument();
    expect(screen.queryByText(marketDataKeysLine)).not.toBeInTheDocument();
    expect(screen.getByText(warningAdvice)).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "How to set a master key" }),
    ).toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Export" });
    expect(button).toBeEnabled();
    await user.click(button);
    await waitFor(() => expect(exportBackupMock).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole("tab", { name: "Restore" }));
    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
  });

  it("warns about plain-text market-data keys when their settings are exported", async () => {
    fetchCredentialStorageMock.mockResolvedValue(unsealedStorage(false, true));
    renderBackupCard();

    expect(await screen.findByText(warningTitle)).toBeInTheDocument();
    expect(screen.getByText(marketDataKeysLine)).toBeInTheDocument();
    expect(screen.queryByText(tradingKeysLine)).not.toBeInTheDocument();
    expect(screen.getByText(warningAdvice)).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "How to set a master key" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("names both kinds of plain-text keys", async () => {
    fetchCredentialStorageMock.mockResolvedValue(unsealedStorage(true, true));
    renderBackupCard();

    expect(await screen.findByText(warningTitle)).toBeInTheDocument();
    expect(screen.getByText(tradingKeysLine)).toBeInTheDocument();
    expect(screen.getByText(marketDataKeysLine)).toBeInTheDocument();
    expect(screen.getByText(warningAdvice)).toBeInTheDocument();
  });

  it("does not warn about market-data keys when their settings are not exported", async () => {
    fetchCredentialStorageMock.mockResolvedValue(unsealedStorage(false, true));
    const user = userEvent.setup();
    renderBackupCard();
    expect(await screen.findByText(marketDataKeysLine)).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    await user.click(
      screen.getByRole("checkbox", { name: "Market-data settings" }),
    );
    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
    expect(screen.queryByText(marketDataKeysLine)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("does not warn when credentials are sealed", async () => {
    fetchTradingMock.mockResolvedValue(tradingSnapshot);
    await act(async () => {
      renderBackupCard();
    });
    expect(fetchCredentialStorageMock).toHaveBeenCalled();
    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("does not warn while credential storage is loading", async () => {
    fetchCredentialStorageMock.mockReturnValue(new Promise(() => {}));
    await act(async () => {
      renderBackupCard();
    });
    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("explains a failed credential storage load without blocking export", async () => {
    fetchCredentialStorageMock.mockRejectedValue(
      new Error("credential storage unavailable"),
    );
    const user = userEvent.setup();
    await act(async () => {
      renderBackupCard();
    });
    const unknown =
      "Could not check whether stored keys are encrypted: credential storage unavailable";
    expect(screen.getByRole("alert")).toHaveTextContent(unknown);
    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();

    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    await user.click(
      screen.getByRole("checkbox", { name: "Trading connections" }),
    );
    await user.click(
      screen.getByRole("checkbox", { name: "Market-data settings" }),
    );
    expect(screen.queryByText(unknown)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("warns about trading keys only when the effective export scope includes trading", async () => {
    fetchCredentialStorageMock.mockResolvedValue(unsealedStorage(true, false));
    const user = userEvent.setup();
    renderBackupCard();
    expect(await screen.findByText(tradingKeysLine)).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    await user.click(
      screen.getByRole("checkbox", { name: "Trading connections" }),
    );
    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    expect(screen.getByText(tradingKeysLine)).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
    expect(screen.queryByText(warningTitle)).not.toBeInTheDocument();
    await user.click(
      screen.getByRole("checkbox", { name: "Orders, trades, adjustments" }),
    );
    expect(screen.getByText(tradingKeysLine)).toBeInTheDocument();
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
    fetchTradingMock.mockResolvedValue(tradingSnapshot);
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
    // Trading has loaded with a connection once the export tab locks the
    // account selector to all accounts.
    await waitFor(() =>
      expect(
        screen.getByRole("checkbox", { name: "All accounts and groups" }),
      ).toBeDisabled(),
    );
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

  it("links backup workflow tabs to their matching panels and switches workflows", async () => {
    const user = userEvent.setup();
    renderBackupCard();

    const exportTab = screen.getByRole("tab", { name: /^export$/i });
    const restoreTab = screen.getByRole("tab", { name: /^restore$/i });
    expect(exportTab).toHaveAttribute(
      "aria-controls",
      screen.getByRole("tabpanel", { name: /^export$/i }).id,
    );
    expect(
      screen.getByRole("tabpanel", { name: /^export$/i }),
    ).toBeInTheDocument();

    await user.click(restoreTab);

    await waitFor(() => {
      expect(restoreTab).toHaveAttribute("aria-selected", "true");
    });
    expect(
      screen.getByRole("tabpanel", { name: /^restore$/i }),
    ).toBeInTheDocument();
    expect(restoreTab).toHaveAttribute(
      "aria-controls",
      screen.getByRole("tabpanel", { name: /^restore$/i }).id,
    );
    expect(
      screen.queryByRole("tabpanel", { name: /^export$/i }),
    ).not.toBeInTheDocument();

    await user.click(exportTab);

    await waitFor(() => {
      expect(exportTab).toHaveAttribute("aria-selected", "true");
    });
    expect(
      screen.getByRole("tabpanel", { name: /^export$/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("tabpanel", { name: /^restore$/i }),
    ).not.toBeInTheDocument();
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

describe("BackupCard with accounts hidden", () => {
  beforeEach(() => {
    setAccountScope({ accounts: false, account: "main" });
  });

  it("shows no account or position selector and keeps the data sections", async () => {
    fetchTradingMock.mockResolvedValue(tradingSnapshot);
    const user = userEvent.setup();
    renderBackupCard();
    // Trading has loaded with a connection, which would lock the hidden
    // account selector to all accounts and show its hint.
    await act(async () => {});
    expect(fetchTradingMock).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("checkbox", { name: "All data" }));

    for (const name of ["All accounts and groups", "All position accounts"]) {
      expect(screen.queryByRole("checkbox", { name })).toBeNull();
    }
    for (const placeholder of [
      "accounts, comma-separated",
      "groups, comma-separated",
      "position accounts, comma-separated",
      "position groups, comma-separated",
    ]) {
      expect(screen.queryByPlaceholderText(placeholder)).toBeNull();
    }
    expect(
      screen.queryByText(
        "Trading data is exported and restored for all accounts.",
      ),
    ).toBeNull();
    for (const name of ["Accounts and groups", "Positions"]) {
      expect(screen.getByRole("checkbox", { name })).toBeChecked();
    }
  });

  it("exports the selected sections for every account", async () => {
    exportBackupMock.mockResolvedValue({
      blob: new Blob(["backup"]),
      filename: "backup.zip",
    });
    const user = userEvent.setup();
    await act(async () => {
      renderBackupCard();
    });
    await user.click(screen.getByRole("checkbox", { name: "All data" }));
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
        "activity_history",
        "trading",
        "audit_log",
      ],
    });
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
    expect(checkSession).not.toHaveBeenCalled();
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
    // A reset returns Officer to setup, which the gate finds on this check.
    expect(checkSession).toHaveBeenCalledTimes(1);
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
