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
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PreMigrationBackup } from "@/api/types";
import { PreMigrationBackupBanner } from "@/components/PreMigrationBackupBanner";
import i18n from "@/i18n";
import { formatDateTime } from "@/i18n/format";
import { renderWithApi } from "@/test/apiClient";

const listPreMigrationBackups =
  vi.fn<(signal?: AbortSignal) => Promise<PreMigrationBackup[]>>();
const downloadPreMigrationBackup =
  vi.fn<(name: string) => Promise<{ blob: Blob; filename: string }>>();
const deletePreMigrationBackup = vi.fn<(name: string) => Promise<void>>();
const title = "A copy of your database from before the migration is available";
const newest: PreMigrationBackup = {
  name: "officer.db.pre-migration-20260102T030405Z",
  createdAt: "2026-01-02T03:04:05Z",
  sizeBytes: 2_500_000,
};
const oldest: PreMigrationBackup = {
  name: "officer.db.pre-migration-20251201T010101Z",
  createdAt: "2025-12-01T01:01:01Z",
  sizeBytes: 1_200_000,
};

function renderBanner() {
  return renderWithApi(
    <I18nextProvider i18n={i18n}>
      <PreMigrationBackupBanner />
    </I18nextProvider>,
    {
      api: {
        listPreMigrationBackups,
        downloadPreMigrationBackup,
        deletePreMigrationBackup,
      },
    },
  );
}

function entryOf(backup: PreMigrationBackup): HTMLElement {
  const time = formatDateTime(backup.createdAt);
  const entry = screen
    .getAllByRole("listitem")
    .find((item) => item.textContent?.includes(time));
  if (entry === undefined) {
    throw new Error(`no list entry for ${backup.name}`);
  }
  return entry;
}

beforeEach(async () => {
  vi.resetAllMocks();
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: vi.fn(() => "blob:pre-migration"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    value: vi.fn(),
  });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  await i18n.changeLanguage("en");
});

describe("PreMigrationBackupBanner", () => {
  it("hides while loading", () => {
    listPreMigrationBackups.mockReturnValue(new Promise(() => {}));
    const { container } = renderBanner();
    expect(container).toBeEmptyDOMElement();
  });

  it("hides when no copy exists", async () => {
    listPreMigrationBackups.mockResolvedValue([]);
    const { container } = renderBanner();
    await act(async () => {});
    expect(listPreMigrationBackups).toHaveBeenCalledTimes(1);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows an error banner when the first read fails", async () => {
    listPreMigrationBackups.mockRejectedValue(new Error("backups unavailable"));
    renderBanner();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not list the database copies made before a migration: backups unavailable",
    );
    expect(screen.queryByText(title)).not.toBeInTheDocument();
  });

  it("shows the error banner when a poll fails after an empty list", async () => {
    vi.useFakeTimers();
    listPreMigrationBackups.mockResolvedValue([]);
    const { unmount } = renderBanner();
    try {
      await act(async () => {});
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();

      listPreMigrationBackups.mockRejectedValue(new Error("refresh failed"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(listPreMigrationBackups).toHaveBeenCalledTimes(2);
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Could not list the database copies made before a migration: refresh failed",
      );
      expect(screen.queryByText(title)).not.toBeInTheDocument();
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("lists every copy, newest first, with its buttons", async () => {
    listPreMigrationBackups.mockResolvedValue([newest, oldest]);
    renderBanner();

    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(screen.getByText(/keep it/)).toBeInTheDocument();
    expect(
      screen.getByText("Officer never deletes the copy itself."),
    ).toBeInTheDocument();
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent(formatDateTime(newest.createdAt));
    expect(items[0]).toHaveTextContent("2.5 MB");
    expect(items[1]).toHaveTextContent(formatDateTime(oldest.createdAt));
    expect(items[1]).toHaveTextContent("1.2 MB");
    for (const item of items) {
      expect(
        within(item).getByRole("button", { name: "Download" }),
      ).toBeEnabled();
      expect(
        within(item).getByRole("button", { name: "Delete" }),
      ).toBeEnabled();
    }
  });

  it("keeps the offer and marks it stale when a background refresh fails", async () => {
    vi.useFakeTimers();
    listPreMigrationBackups.mockResolvedValue([newest]);
    const { unmount } = renderBanner();
    try {
      await act(async () => {});
      expect(screen.getByText(title)).toBeInTheDocument();

      listPreMigrationBackups.mockRejectedValue(new Error("refresh failed"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(listPreMigrationBackups).toHaveBeenCalledTimes(2);
      expect(screen.getByText(title)).toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveAttribute(
        "aria-label",
        "Stale data: refresh failed",
      );
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("downloads the copy the button belongs to under the name the server sent", async () => {
    const user = userEvent.setup();
    listPreMigrationBackups.mockResolvedValue([newest, oldest]);
    const blob = new Blob(["sqlite"]);
    downloadPreMigrationBackup.mockResolvedValue({
      blob,
      filename: "served-name.db",
    });
    renderBanner();
    await screen.findByText(title);

    await user.click(
      within(entryOf(oldest)).getByRole("button", { name: "Download" }),
    );

    await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalledTimes(1));
    expect(downloadPreMigrationBackup).toHaveBeenCalledWith(oldest.name);
    expect(vi.mocked(URL.createObjectURL).mock.calls[0][0]).toBe(blob);
    const click = vi.mocked(HTMLAnchorElement.prototype.click);
    expect(click).toHaveBeenCalledTimes(1);
    expect((click.mock.contexts[0] as HTMLAnchorElement).download).toBe(
      "served-name.db",
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows a failed download inside the banner and re-enables the buttons", async () => {
    const user = userEvent.setup();
    listPreMigrationBackups.mockResolvedValue([newest]);
    downloadPreMigrationBackup.mockRejectedValue(new Error("copy is gone"));
    renderBanner();
    await screen.findByText(title);

    await user.click(screen.getByRole("button", { name: "Download" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("copy is gone");
    expect(URL.createObjectURL).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Download" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled();
  });

  it("disables the buttons while a request runs", async () => {
    const user = userEvent.setup();
    listPreMigrationBackups.mockResolvedValue([newest]);
    downloadPreMigrationBackup.mockReturnValue(new Promise(() => {}));
    renderBanner();
    await screen.findByText(title);

    await user.click(screen.getByRole("button", { name: "Download" }));

    expect(screen.getByRole("button", { name: "Download" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("sends nothing when the delete confirmation is declined", async () => {
    const user = userEvent.setup();
    listPreMigrationBackups.mockResolvedValue([newest]);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    renderBanner();
    await screen.findByText(title);

    await user.click(screen.getByRole("button", { name: "Delete" }));

    expect(confirm).toHaveBeenCalledWith(
      `Delete the copy ${newest.name}? It cannot be restored.`,
    );
    expect(deletePreMigrationBackup).not.toHaveBeenCalled();
    expect(listPreMigrationBackups).toHaveBeenCalledTimes(1);
    expect(screen.getByText(title)).toBeInTheDocument();
  });

  it("deletes the confirmed copy and reloads the list", async () => {
    const user = userEvent.setup();
    listPreMigrationBackups
      .mockResolvedValueOnce([newest, oldest])
      .mockResolvedValue([newest]);
    deletePreMigrationBackup.mockResolvedValue(undefined);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderBanner();
    await screen.findByText(title);

    await user.click(
      within(entryOf(oldest)).getByRole("button", { name: "Delete" }),
    );

    await waitFor(() =>
      expect(screen.getAllByRole("listitem")).toHaveLength(1),
    );
    expect(deletePreMigrationBackup).toHaveBeenCalledTimes(1);
    expect(deletePreMigrationBackup).toHaveBeenCalledWith(oldest.name);
    expect(listPreMigrationBackups).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("listitem")).toHaveTextContent(
      formatDateTime(newest.createdAt),
    );
  });

  it("shows a failed delete inside the banner and keeps the copy", async () => {
    const user = userEvent.setup();
    listPreMigrationBackups.mockResolvedValue([newest]);
    deletePreMigrationBackup.mockRejectedValue(new Error("removal failed"));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderBanner();
    await screen.findByText(title);

    await user.click(screen.getByRole("button", { name: "Delete" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "removal failed",
    );
    expect(listPreMigrationBackups).toHaveBeenCalledTimes(1);
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled();
  });
});
