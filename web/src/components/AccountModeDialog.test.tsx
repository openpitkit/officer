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

import { screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { AccountModeState } from "@/api/accountMode";
import { AccountModeDialog } from "@/components/AccountModeDialog";
import { answeringAccountMode, renderWithApi } from "@/test/apiClient";
import { ThemeProvider } from "@/theme/ThemeProvider";

const TITLE = "How many accounts will you use?";
const RESTARTING = "Officer is restarting; the panel reloads when it is back.";

const notChosen: AccountModeState = {
  mode: null,
  account: "",
  features: { accounts: true, account: "" },
  restartRequired: false,
};

function renderDialog(fetchMock: ReturnType<typeof vi.fn>) {
  const onDone = vi.fn();
  const restartService = vi
    .fn<() => Promise<void>>()
    .mockResolvedValue(undefined);
  // A first start has no account yet.
  const fetchAccounts = vi.fn().mockResolvedValue([]);
  renderWithApi(
    <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
      <AccountModeDialog onDone={onDone} />
    </ThemeProvider>,
    {
      fetch: fetchMock as unknown as typeof fetch,
      api: { restartService, fetchAccounts },
    },
  );
  return { onDone, restartService };
}

function puts(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter(
    (call) => (call as [string, RequestInit])[1].method === "PUT",
  );
}

async function saveChoice(user: UserEvent, dialog: HTMLElement, mode: string) {
  await user.click(await within(dialog).findByRole("button", { name: mode }));
  const save = within(dialog).getByRole("button", { name: "Save" });
  await waitFor(() => expect(save).toBeEnabled());
  await user.click(save);
  const confirm = await screen.findByRole("alertdialog");
  await user.click(within(confirm).getByRole("button", { name: "Save" }));
}

describe("AccountModeDialog", () => {
  it("asks for the mode with the shared chooser", async () => {
    const { onDone } = renderDialog(answeringAccountMode(notChosen));

    const dialog = screen.getByRole("dialog", { name: TITLE });
    expect(
      await within(dialog).findByTitle("Not chosen yet"),
    ).toBeInTheDocument();
    for (const mode of ["One account", "Several accounts"]) {
      expect(
        within(dialog).getByRole("button", { name: mode }),
      ).toBeInTheDocument();
    }
    expect(onDone).not.toHaveBeenCalled();
  });

  it("offers the language switch", () => {
    renderDialog(answeringAccountMode(notChosen));

    const dialog = screen.getByRole("dialog", { name: TITLE });
    expect(
      within(dialog).getByRole("button", { name: "Change language" }),
    ).toBeInTheDocument();
  });

  it("closes and hands over once a save needs no restart", async () => {
    const user = userEvent.setup();
    const fetchMock = answeringAccountMode(notChosen, {
      ...notChosen,
      mode: "multi",
    });
    const { onDone, restartService } = renderDialog(fetchMock);

    await saveChoice(
      user,
      screen.getByRole("dialog", { name: TITLE }),
      "Several accounts",
    );

    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
    expect(screen.queryByText(TITLE)).not.toBeInTheDocument();
    expect(puts(fetchMock)).toHaveLength(1);
    expect(restartService).not.toHaveBeenCalled();
  });

  it("leaves the mode unset when closed without a choice", async () => {
    const user = userEvent.setup();
    const fetchMock = answeringAccountMode(notChosen);
    const { onDone } = renderDialog(fetchMock);

    const dialog = screen.getByRole("dialog", { name: TITLE });
    await within(dialog).findByTitle("Not chosen yet");
    await user.click(within(dialog).getByRole("button", { name: "Close" }));

    expect(screen.queryByText(TITLE)).not.toBeInTheDocument();
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(puts(fetchMock)).toHaveLength(0);
  });

  it("does not hand over when closed while Officer restarts", async () => {
    const user = userEvent.setup();
    const fetchMock = answeringAccountMode(notChosen, {
      ...notChosen,
      mode: "single",
      account: "default",
      restartRequired: true,
    });
    const { onDone, restartService } = renderDialog(fetchMock);

    const dialog = screen.getByRole("dialog", { name: TITLE });
    await saveChoice(user, dialog, "One account");
    expect(await within(dialog).findByText(RESTARTING)).toBeInTheDocument();
    expect(restartService).toHaveBeenCalledTimes(1);

    await user.click(within(dialog).getByRole("button", { name: "Close" }));

    expect(screen.queryByText(TITLE)).not.toBeInTheDocument();
    expect(onDone).not.toHaveBeenCalled();
  });

  it("hands over when closed after Officer refused the restart", async () => {
    const user = userEvent.setup();
    const fetchMock = answeringAccountMode(notChosen, {
      ...notChosen,
      mode: "single",
      account: "default",
      restartRequired: true,
    });
    const { onDone, restartService } = renderDialog(fetchMock);
    restartService.mockRejectedValue(new Error("restart is not allowed"));

    const dialog = screen.getByRole("dialog", { name: TITLE });
    await saveChoice(user, dialog, "One account");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "restart is not allowed",
    );

    await user.click(within(dialog).getByRole("button", { name: "Close" }));

    expect(onDone).toHaveBeenCalledTimes(1);
  });
});
