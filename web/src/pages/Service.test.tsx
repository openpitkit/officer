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
import { beforeEach, describe, expect, it, vi } from "vitest";

import i18n from "@/i18n";
import { BackupCard, DatabaseCard } from "@/pages/Service";

const exportBackupMock = vi.fn();
const resetDatabaseMock = vi.fn();
const restoreBackupMock = vi.fn();

function renderBackupCard() {
  render(
    <I18nextProvider i18n={i18n}>
      <BackupCard />
    </I18nextProvider>,
    {
      api: {
        exportBackup: exportBackupMock,
        restoreBackup: restoreBackupMock,
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
  it("links backup workflow tabs to panels and supports arrow navigation", async () => {
    const user = userEvent.setup();
    renderBackupCard();

    const exportTab = screen.getByRole("tab", { name: /^export$/i });
    const restoreTab = screen.getByRole("tab", { name: /^restore$/i });
    expect(exportTab).toHaveAttribute("aria-controls", "backup-workflow-panel-export");
    expect(restoreTab).toHaveAttribute("aria-controls", "backup-workflow-panel-restore");
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

    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
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
      restartRequired: false,
    });
    renderBackupCard();

    await user.click(screen.getByRole("tab", { name: /^restore$/i }));
    await user.click(screen.getByRole("radio", { name: /overwrite/i }));
    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
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
  });

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
    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
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
    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
    if (input == null) {
      throw new Error("backup file input not found");
    }
    await user.upload(
      input,
      new File(
        [
          JSON.stringify({
            manifest: {
              format: "openpit.officer.backup",
              formatVersion: 1,
              schemaVersion: 2,
              createdAt: "2026-06-22T10:00:00Z",
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
