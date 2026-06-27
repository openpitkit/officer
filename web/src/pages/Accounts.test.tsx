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

import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type {
  Account,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  BusinessCsvImportEntity,
  Group,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { useAccounts } from "@/api/useAccounts";
import { useGroups } from "@/api/useGroups";
import { SidebarProvider } from "@/components/SidebarContext";
import { ApiError } from "@/framework";
import i18n from "@/i18n";
import { Accounts, CreateAccountDialog } from "@/pages/Accounts";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useAccounts", () => ({ useAccounts: vi.fn() }));
vi.mock("@/api/useGroups", () => ({ useGroups: vi.fn() }));
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
      imports,
      onImported,
    }: {
      exports?: {
        entity: BusinessCsvEntity;
        filters?: BusinessCsvExportFilters;
        label: string;
      }[];
      imports?: {
        defaultEntity?: BusinessCsvImportEntity;
        entities: BusinessCsvImportEntity[];
        label: string;
      }[];
      onImported?: () => void;
    }) => (
      <>
        {imports?.map((item) => (
          <dialogs.BusinessCsvImportDialog
            key={`import-${item.label}`}
            defaultEntity={item.defaultEntity}
            entities={item.entities}
            onImported={onImported ?? (() => {})}
            trigger={(open) => (
              <button type="button" onClick={open}>
                {item.label}
              </button>
            )}
          />
        ))}
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
const importBusinessCsvMock = vi.fn();
const previewBusinessCsvImportMock = vi.fn();
const setAccountGroupMock = vi.fn();
const useAccountsMock = vi.mocked(useAccounts);
const useGroupsMock = vi.mocked(useGroups);

function ready<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

const accounts: Account[] = [
  {
    code: "desk-default",
    title: "Desk default",
    blocked: false,
    blockReason: "",
    group: "",
    notes: "",
  },
  {
    code: "desk-alpha",
    title: "Desk alpha",
    blocked: false,
    blockReason: "",
    group: "equity-desks",
    notes: "",
  },
];

const groups: Group[] = [
  {
    code: "equity-desks",
    title: "Equity desks",
    blocked: false,
    blockReason: "",
    notes: "",
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

function renderAccounts() {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <MemoryRouter>
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
        importBusinessCsv: importBusinessCsvMock,
        previewBusinessCsvImport: previewBusinessCsvImportMock,
        setAccountGroup: setAccountGroupMock,
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  // Pin the locale so assertions match the English catalog regardless of the
  // detector's navigator guess under jsdom.
  await i18n.changeLanguage("en");
  useAccountsMock.mockReturnValue(ready(accounts));
  useGroupsMock.mockReturnValue(ready(groups));
  exportBusinessCsvMock.mockResolvedValue({
    blob: new Blob(["csv"]),
    filename: "business.csv",
  });
  previewBusinessCsvImportMock.mockResolvedValue({
    file: { name: "accounts.csv", type: "csv" },
    counts: {
      rows: 2,
      applied: 0,
      skipped: 0,
      conflicts: 0,
      stopped: false,
    },
    conflicts: [],
  });
  importBusinessCsvMock.mockResolvedValue({
    file: { name: "accounts.csv", type: "csv" },
    counts: {
      rows: 2,
      applied: 2,
      skipped: 0,
      conflicts: 0,
      stopped: false,
    },
    conflicts: [],
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

describe("CreateAccountDialog", () => {
  it("creates an account, closes, and notifies on submit", async () => {
    const user = userEvent.setup();
    createAccountMock.mockResolvedValue({
      code: "acc-aapl-desk",
      title: "acc-aapl-desk",
      blocked: false,
      blockReason: "",
      group: "",
      notes: "",
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
    expect(createAccountMock).toHaveBeenCalledWith("acc-aapl-desk");
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
      notes: "",
    });
    setAccountGroupMock.mockResolvedValue({
      code: "acc-spx",
      title: "acc-spx",
      blocked: false,
      blockReason: "",
      group: "equity-desks",
      notes: "",
    });
    renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    await user.type(screen.getByLabelText(/account code/i), "acc-spx");
    await user.type(screen.getByLabelText(/group/i), "equity-desks");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() => {
      expect(createAccountMock).toHaveBeenCalledWith("acc-spx");
    });
    expect(setAccountGroupMock).toHaveBeenCalledTimes(1);
    expect(setAccountGroupMock).toHaveBeenCalledWith("acc-spx", "equity-desks");
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
    await user.click(screen.getByText("Default group"));
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

  it("previews pasted import data and applies the no-conflict result", async () => {
    const user = userEvent.setup();
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /import csv/i }));
    expect(screen.getByText("Entity")).toBeInTheDocument();
    expect(screen.getByText("Delimiter")).toBeInTheDocument();
    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
    expect(input?.getAttribute("accept")).toContain(".zip");

    await user.type(
      screen.getByPlaceholderText(/paste csv rows here/i),
      "account_id,group_id,notes\nacc-new,,New account\n",
    );
    await user.click(screen.getByRole("button", { name: /preview upload/i }));
    await waitFor(() =>
      expect(previewBusinessCsvImportMock).toHaveBeenCalledTimes(1),
    );
    expect(previewBusinessCsvImportMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "accounts",
        delimiter: "comma",
        filename: "pasted.csv",
      }),
    );
    await user.click(screen.getByRole("button", { name: /apply import/i }));
    await waitFor(() => expect(importBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(importBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        conflictPolicy: "skip",
      }),
    );
  });

  it("shows skip, replace, and stop choices when preview reports conflicts", async () => {
    const user = userEvent.setup();
    previewBusinessCsvImportMock.mockResolvedValueOnce({
      file: { name: "accounts.csv", type: "csv" },
      counts: {
        rows: 2,
        applied: 0,
        skipped: 0,
        conflicts: 1,
        stopped: false,
      },
      conflicts: [{ row: 2, key: "desk-alpha" }],
    });
    renderAccounts();

    await user.click(screen.getByRole("button", { name: /import csv/i }));
    await user.type(
      screen.getByPlaceholderText(/paste csv rows here/i),
      "account_id,group_id\nacc-new,\n",
    );
    await user.click(screen.getByRole("button", { name: /preview upload/i }));

    expect(await screen.findByText("Conflicts found")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /skip conflicts/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /replace conflicts/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /stop at first conflict/i }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /skip conflicts/i }));
    await waitFor(() => expect(importBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(importBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        conflictPolicy: "skip",
      }),
    );
  });

  it("resets account pagination when selecting a group filter", async () => {
    const user = userEvent.setup();
    const pagedAccounts: Account[] = [
      ...Array.from({ length: 51 }, (_, index) => ({
        code: `desk-${index.toString().padStart(2, "0")}`,
        title: `Desk ${index.toString().padStart(2, "0")}`,
        blocked: false,
        blockReason: "",
        group: "",
        notes: "",
      })),
      {
        code: "desk-alpha",
        title: "Desk alpha",
        blocked: false,
        blockReason: "",
        group: "equity-desks",
        notes: "",
      },
    ];
    useAccountsMock.mockReturnValue(ready(pagedAccounts));
    renderAccounts();

    await user.click(screen.getAllByRole("button", { name: /^next$/i })[0]);
    await user.click(screen.getByRole("button", { name: /^groups$/i }));
    await user.click(screen.getByText("equity-desks"));
    await user.click(screen.getByRole("button", { name: /^accounts$/i }));

    expect(screen.getByText("desk-alpha")).toBeInTheDocument();
  });
});
