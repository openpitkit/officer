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
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useState, type ComponentProps } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useAccountMode, type AccountModeState } from "@/api/accountMode";
import type { AccountListFilters } from "@/api/types";
import {
  AccountModeChoice,
  AccountModePanel,
} from "@/components/AccountModeChoice";
import i18n from "@/i18n";
import {
  accountRow,
  answeringAccountMode,
  jsonResponse,
  renderWithApi,
} from "@/test/apiClient";

const runningMulti: AccountModeState["features"] = {
  accounts: true,
  account: "",
};
const runningSingle: AccountModeState["features"] = {
  accounts: false,
  account: "main",
};
const storedMulti: AccountModeState = {
  mode: "multi",
  account: "",
  features: runningMulti,
  restartRequired: false,
};
const storedSingle: AccountModeState = {
  mode: "single",
  account: "main",
  features: runningSingle,
  restartRequired: false,
};

/** GET /accounts over `accounts`, filtered by code prefix and limit. */
function accountsLookup(accounts: string[]) {
  return vi.fn(async (input?: AccountListFilters | AbortSignal) => {
    const { code = "", limit } = input as AccountListFilters;
    return accounts
      .filter((account) => account.startsWith(code))
      .slice(0, limit)
      .map((account) => accountRow(account));
  });
}

/** Renders the chooser under a host whose button blocks it at any moment. */
function ChoiceHost({
  initialDisabled,
  ...props
}: Omit<ComponentProps<typeof AccountModeChoice>, "disabled"> & {
  initialDisabled: boolean;
}) {
  const [disabled, setDisabled] = useState(initialDisabled);
  return (
    <>
      <button type="button" onClick={() => setDisabled(true)}>
        Block the choice
      </button>
      <AccountModeChoice {...props} disabled={disabled} />
    </>
  );
}

function renderChoice(
  state: AccountModeState,
  {
    accounts = [],
    answer,
    loadError = null,
    disabled = false,
  }: {
    accounts?: string[];
    answer?: Response;
    loadError?: string | null;
    disabled?: boolean;
  } = {},
) {
  const fetchMock = vi.fn(async () => answer ?? jsonResponse(state));
  const fetchAccounts = accountsLookup(accounts);
  const restart = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
  const onSaved = vi.fn();
  renderWithApi(
    <ChoiceHost
      load={{ state: "ready", data: state, error: loadError }}
      restart={restart}
      initialDisabled={disabled}
      onSaved={onSaved}
    />,
    {
      fetch: fetchMock as unknown as typeof fetch,
      api: { fetchAccounts },
    },
  );
  return { fetchMock, fetchAccounts, restart, onSaved };
}

function saveButton() {
  return screen.getByRole("button", { name: "Save" });
}

async function choose(user: UserEvent, mode: string) {
  await user.click(screen.getByRole("button", { name: mode }));
}

async function confirmSave(user: UserEvent) {
  await user.click(saveButton());
  const dialog = await screen.findByRole("alertdialog");
  expect(
    within(dialog).getByText(/Officer restarts to apply the mode/),
  ).toBeInTheDocument();
  await user.click(within(dialog).getByRole("button", { name: "Save" }));
}

function putBodies(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls
    .map((call) => call as unknown as [string, RequestInit])
    .filter(([, init]) => init.method === "PUT")
    .map(([url, init]) => {
      expect(url).toBe("/app/api/v1/account-mode");
      return JSON.parse(init.body as string) as unknown;
    });
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("AccountModeChoice", () => {
  it("shows the stored mode and the running one while a restart is pending", async () => {
    renderChoice(
      { ...storedSingle, features: runningMulti, restartRequired: true },
      { accounts: ["main"] },
    );

    expect(
      await screen.findByText("Officer acts on the account main only."),
    ).toBeInTheDocument();
    // Saving the stored choice again restarts Officer to apply it.
    expect(saveButton()).toBeEnabled();
    expect(screen.getByText("Saved mode")).toBeInTheDocument();
    expect(screen.getByTitle("One account: main")).toBeInTheDocument();
    expect(screen.getByText("Running mode")).toBeInTheDocument();
    expect(screen.getByTitle("Several accounts")).toBeInTheDocument();
  });

  it("shows only the stored mode while the running one matches it", () => {
    renderChoice({ ...storedMulti, mode: null });

    expect(screen.getByTitle("Not chosen yet")).toBeInTheDocument();
    expect(screen.queryByText("Running mode")).not.toBeInTheDocument();
  });

  it("disables saving while the choice equals the stored one", async () => {
    const user = userEvent.setup();
    renderChoice(storedMulti, { accounts: ["main"] });

    expect(saveButton()).toBeDisabled();
    await choose(user, "One account");
    await screen.findByText("Officer acts on the account main only.");
    expect(saveButton()).toBeEnabled();
    await choose(user, "Several accounts");
    expect(saveButton()).toBeDisabled();
  });

  it("creates the default account when no account exists", async () => {
    const user = userEvent.setup();
    const { fetchMock, fetchAccounts } = renderChoice(storedMulti, {
      accounts: [],
      answer: jsonResponse({
        mode: "single",
        account: "default",
        features: runningMulti,
        restartRequired: true,
      }),
    });

    await choose(user, "One account");
    const note = await screen.findByText(
      "No account exists yet: Officer creates the account default and acts on it only.",
    );
    // The note goes under the save, so the save stays where the other mode
    // has it.
    expect(
      saveButton().compareDocumentPosition(note) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(fetchAccounts).toHaveBeenCalledWith(
      { limit: 2 },
      expect.any(AbortSignal),
    );
    await confirmSave(user);

    await waitFor(() =>
      expect(putBodies(fetchMock)).toEqual([{ mode: "single" }]),
    );
  });

  it("keeps the only account the server reports", async () => {
    const user = userEvent.setup();
    const { fetchMock } = renderChoice(storedMulti, {
      accounts: ["main"],
      answer: jsonResponse({
        mode: "single",
        account: "main",
        features: runningMulti,
        restartRequired: true,
      }),
    });

    await choose(user, "One account");
    expect(
      await screen.findByText("Officer acts on the account main only."),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("The other accounts stop working"),
    ).not.toBeInTheDocument();
    await confirmSave(user);

    await waitFor(() =>
      expect(putBodies(fetchMock)).toEqual([
        { mode: "single", account: "main" },
      ]),
    );
  });

  it("requires a chosen account among several and warns about the others", async () => {
    const user = userEvent.setup();
    const { fetchMock } = renderChoice(storedMulti, {
      accounts: ["alpha", "beta", "gamma"],
      answer: jsonResponse({
        mode: "single",
        account: "beta",
        features: runningMulti,
        restartRequired: true,
      }),
    });

    await choose(user, "One account");
    const picker = await screen.findByLabelText("Account");
    expect(
      screen.getByText("The other accounts stop working"),
    ).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();

    await user.type(picker, "beta");
    expect(saveButton()).toBeEnabled();
    await confirmSave(user);

    await waitFor(() =>
      expect(putBodies(fetchMock)).toEqual([
        { mode: "single", account: "beta" },
      ]),
    );
  });

  it("switches to several accounts and restarts when the server asks for it", async () => {
    const user = userEvent.setup();
    const { fetchMock, fetchAccounts, restart, onSaved } = renderChoice(
      storedSingle,
      {
        loadError: "Officer is unreachable",
        answer: jsonResponse({
          mode: "multi",
          account: "",
          features: runningSingle,
          restartRequired: true,
        }),
      },
    );
    expect(screen.queryByText("Stale data")).not.toBeInTheDocument();

    await choose(user, "Several accounts");
    await confirmSave(user);

    expect(
      await screen.findByText(
        "Officer is restarting; the panel reloads when it is back.",
      ),
    ).toBeInTheDocument();
    expect(putBodies(fetchMock)).toEqual([{ mode: "multi" }]);
    expect(restart).toHaveBeenCalledTimes(1);
    expect(onSaved).not.toHaveBeenCalled();
    expect(
      screen.getByRole("status", {
        name: "Stale data: Officer is unreachable",
      }),
    ).toBeInTheDocument();
    expect(fetchAccounts).not.toHaveBeenCalled();
  });

  it("keeps the running account and reports a save that needs no restart", async () => {
    const user = userEvent.setup();
    const saved: AccountModeState = { ...storedSingle };
    const { fetchMock, fetchAccounts, restart, onSaved } = renderChoice(
      { ...storedMulti, features: runningSingle, restartRequired: true },
      { answer: jsonResponse(saved) },
    );

    await choose(user, "One account");
    expect(
      screen.getByText("Officer acts on the account main only."),
    ).toBeInTheDocument();
    // The running process already enforces this mode: nothing to confirm.
    await user.click(saveButton());

    expect(
      await screen.findByText(
        "The account mode is saved. No restart is needed.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(putBodies(fetchMock)).toEqual([{ mode: "single", account: "main" }]);
    expect(onSaved).toHaveBeenCalledExactlyOnceWith(saved);
    expect(restart).not.toHaveBeenCalled();
    expect(fetchAccounts).not.toHaveBeenCalled();
  });

  it("saves several accounts on a fresh install without a restart warning", async () => {
    const user = userEvent.setup();
    const saved: AccountModeState = { ...storedMulti };
    const { fetchMock, restart, onSaved } = renderChoice(
      { ...storedMulti, mode: null },
      { answer: jsonResponse(saved) },
    );

    await choose(user, "Several accounts");
    await user.click(saveButton());

    expect(
      await screen.findByText(
        "The account mode is saved. No restart is needed.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(putBodies(fetchMock)).toEqual([{ mode: "multi" }]);
    expect(onSaved).toHaveBeenCalledExactlyOnceWith(saved);
    expect(restart).not.toHaveBeenCalled();
  });

  it("shows a refused save and does not restart", async () => {
    const user = userEvent.setup();
    const { restart, onSaved } = renderChoice(storedMulti, {
      accounts: ["alpha", "beta"],
      answer: jsonResponse(
        {
          error: {
            code: "account_missing",
            message: 'account "ghost" does not exist',
            account: "ghost",
          },
        },
        404,
      ),
    });

    await choose(user, "One account");
    await user.type(await screen.findByLabelText("Account"), "ghost");
    await confirmSave(user);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      'account "ghost" does not exist',
    );
    expect(restart).not.toHaveBeenCalled();
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("shows a failed account list read, does not save and retries it", async () => {
    const user = userEvent.setup();
    const { fetchMock, fetchAccounts } = renderChoice(storedMulti, {
      accounts: ["main"],
    });
    fetchAccounts.mockRejectedValueOnce(new Error("accounts are unavailable"));

    await choose(user, "One account");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "accounts are unavailable",
    );
    expect(saveButton()).toBeDisabled();
    expect(fetchMock).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(
      await screen.findByText("Officer acts on the account main only."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(saveButton()).toBeEnabled();
    expect(fetchAccounts).toHaveBeenCalledTimes(2);
  });

  it("does not save from an open confirmation once the host blocks the choice", async () => {
    const user = userEvent.setup();
    const { fetchMock } = renderChoice(storedSingle);
    await choose(user, "Several accounts");
    await user.click(saveButton());
    const dialog = await screen.findByRole("alertdialog");
    const confirm = within(dialog).getByRole("button", { name: "Save" });
    expect(confirm).toBeEnabled();

    // The open dialog makes the rest of the page inert to the pointer.
    fireEvent.click(
      screen.getByRole("button", { name: "Block the choice", hidden: true }),
    );

    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    // A save would reach fetch only after the client's own awaits.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows a refused restart and does not report it as under way", async () => {
    const user = userEvent.setup();
    const { restart } = renderChoice(storedSingle, {
      answer: jsonResponse({
        mode: "multi",
        account: "",
        features: runningSingle,
        restartRequired: true,
      }),
    });
    restart.mockRejectedValue(new Error("restart is not allowed"));

    await choose(user, "Several accounts");
    await confirmSave(user);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "restart is not allowed",
    );
    expect(
      screen.queryByText(
        "Officer is restarting; the panel reloads when it is back.",
      ),
    ).not.toBeInTheDocument();
  });

  it.each([true, false])(
    "blocks the choice only while the host disables it (%s)",
    async (disabled) => {
      renderChoice(
        { ...storedMulti, features: runningSingle, restartRequired: true },
        { disabled },
      );

      for (const mode of ["One account", "Several accounts"]) {
        expect(
          screen.getByRole("button", { name: mode }).hasAttribute("disabled"),
        ).toBe(disabled);
      }
      if (!disabled) {
        await userEvent.click(
          screen.getByRole("button", { name: "One account" }),
        );
        expect(saveButton()).toBeEnabled();
      }
    },
  );
});

/** Renders the panel over the account-mode poll, as its hosts do. */
function PanelHost(
  props: Omit<ComponentProps<typeof AccountModePanel>, "poll">,
) {
  return <AccountModePanel poll={useAccountMode()} {...props} />;
}

function renderPanel(
  fetchMock: ReturnType<typeof vi.fn>,
  accounts: string[] = [],
) {
  const restart = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
  renderWithApi(<PanelHost restart={restart} />, {
    fetch: fetchMock as unknown as typeof fetch,
    api: { fetchAccounts: accountsLookup(accounts) },
  });
  return { restart };
}

describe("AccountModePanel", () => {
  it("shows a failed first load and the choice once a retry loads it", async () => {
    const user = userEvent.setup();
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("connection refused"))
      .mockImplementation(answeringAccountMode(storedMulti));
    renderPanel(fetchMock);

    expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
    expect(
      await screen.findByText("Could not load the account mode"),
    ).toBeInTheDocument();
    expect(screen.getByText(/connection refused$/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByTitle("Several accounts")).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
  });

  describe("on the poll clock", () => {
    afterEach(() => {
      vi.useRealTimers();
    });

    it("saves the stored choice again to retry a refused restart", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const fetchMock = answeringAccountMode(storedMulti, {
        mode: "single",
        account: "main",
        features: runningMulti,
        restartRequired: true,
      });
      const { restart } = renderPanel(fetchMock, ["main"]);
      restart.mockRejectedValueOnce(new Error("restart is not allowed"));

      await screen.findByTitle("Several accounts");
      await choose(user, "One account");
      await screen.findByText("Officer acts on the account main only.");
      await confirmSave(user);
      expect(await screen.findByRole("alert")).toHaveTextContent(
        "restart is not allowed",
      );

      // The next poll reads the choice the refused restart left stored.
      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(screen.getByTitle("One account: main")).toBeInTheDocument();
      expect(saveButton()).toBeEnabled();

      await confirmSave(user);

      expect(
        await screen.findByText(
          "Officer is restarting; the panel reloads when it is back.",
        ),
      ).toBeInTheDocument();
      expect(restart).toHaveBeenCalledTimes(2);
      expect(putBodies(fetchMock)).toEqual([
        { mode: "single", account: "main" },
        { mode: "single", account: "main" },
      ]);
    });
  });
});
