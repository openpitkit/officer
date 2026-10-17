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

import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Account } from "@/api/types";
import {
  AccountCurrencyDialog,
  BlockAccountDialog,
  UnblockAccountConfirm,
} from "@/components/AccountDialogs";
import { ApiError } from "@/framework";
import i18n from "@/i18n";
import { accountRow, renderWithApi } from "@/test/apiClient";

const blockAccountMock = vi.fn();
const unblockAccountMock = vi.fn();
const setAccountCurrencyMock = vi.fn();

/** An account as GET /accounts/{code} answers it once normalized. */
function deskAccount(overrides: Partial<Account> = {}): Account {
  return accountRow("desk-alpha", {
    title: "Desk alpha",
    currency: "",
    effectiveCurrency: "USD",
    currencyOrigin: "group",
    currencyCascade: { account: "", group: "USD", default: "" },
    group: "equity-desks",
    pnl: "",
    positionCount: 0,
    ...overrides,
  });
}

const blockedDesk = deskAccount({
  blocked: true,
  blockReason: "Risk halt",
  blockSource: "account",
  accountBlocked: true,
  accountBlockReason: "Risk halt",
});

function renderDialog(dialog: ReactElement) {
  renderWithApi(dialog, {
    api: {
      blockAccount: blockAccountMock,
      unblockAccount: unblockAccountMock,
      setAccountCurrency: setAccountCurrencyMock,
      fetchAssets: vi
        .fn()
        .mockResolvedValue([{ code: "EUR", title: "", assetClass: "" }]),
    },
  });
}

/** Types the currency and saves it. The field closes its suggestion list on a
 *  timer after the blur; waiting for the list to close keeps that timer from
 *  outliving the test. */
async function enterCurrencyAndSave(
  user: ReturnType<typeof userEvent.setup>,
  currency: string,
) {
  await user.type(screen.getByLabelText("Account currency"), currency);
  expect(await screen.findByRole("listbox")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() =>
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument(),
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
});

describe("BlockAccountDialog", () => {
  it("needs a reason and blocks the account with it", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    const onDone = vi.fn();
    blockAccountMock.mockResolvedValue(blockedDesk);
    renderDialog(
      <BlockAccountDialog
        account={deskAccount()}
        open
        onOpenChange={onOpenChange}
        onDone={onDone}
      />,
    );

    expect(
      screen.getByRole("dialog", { name: "Block account desk-alpha" }),
    ).toBeInTheDocument();
    const submit = screen.getByRole("button", { name: "Block" });
    expect(submit).toBeDisabled();
    const reason = screen.getByLabelText("Reason");
    await user.type(reason, "   ");
    expect(submit).toBeDisabled();

    await user.type(reason, "Risk halt ");
    await user.click(submit);

    expect(blockAccountMock).toHaveBeenCalledWith(
      "desk-alpha",
      "Risk halt",
      "reject",
    );
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(blockedDesk));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});

describe("UnblockAccountConfirm", () => {
  it("lifts the account's own block", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    const onDone = vi.fn();
    const unblocked = deskAccount();
    unblockAccountMock.mockResolvedValue(unblocked);
    renderDialog(
      <UnblockAccountConfirm
        account={blockedDesk}
        open
        onOpenChange={onOpenChange}
        onDone={onDone}
      />,
    );

    expect(screen.queryByText(/is blocked\./)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Unblock" }));

    expect(unblockAccountMock).toHaveBeenCalledWith("desk-alpha", "reject");
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(unblocked));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("warns that the group's block stays in force", () => {
    renderDialog(
      <UnblockAccountConfirm
        account={{
          ...blockedDesk,
          groupBlocked: true,
          groupBlockReason: "Desk halt",
        }}
        open
        onOpenChange={vi.fn()}
        onDone={vi.fn()}
      />,
    );

    expect(
      screen.getByText(
        "Group equity-desks is blocked. This account will stay blocked until the group is unblocked.",
      ),
    ).toBeInTheDocument();
  });
});

describe("AccountCurrencyDialog", () => {
  it("shows the effective currency with the tier it comes from", () => {
    renderDialog(
      <AccountCurrencyDialog
        account={deskAccount()}
        open
        onOpenChange={vi.fn()}
        onDone={vi.fn()}
      />,
    );

    expect(screen.getByText("USD · group")).toBeInTheDocument();
  });

  it("sets the account's own currency", async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    setAccountCurrencyMock.mockResolvedValue(
      deskAccount({ currency: "EUR", effectiveCurrency: "EUR" }),
    );
    renderDialog(
      <AccountCurrencyDialog
        account={deskAccount()}
        open
        onOpenChange={vi.fn()}
        onDone={onDone}
      />,
    );

    await enterCurrencyAndSave(user, " EUR ");

    expect(setAccountCurrencyMock).toHaveBeenCalledWith("desk-alpha", "EUR");
    await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
  });

  it("shows a refused change and stays open", async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    setAccountCurrencyMock.mockRejectedValue(
      new ApiError("the account holds open positions", "conflict", 409),
    );
    renderDialog(
      <AccountCurrencyDialog
        account={deskAccount()}
        open
        onOpenChange={vi.fn()}
        onDone={onDone}
      />,
    );

    await enterCurrencyAndSave(user, "EUR");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "the account holds open positions",
    );
    expect(onDone).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });
});
