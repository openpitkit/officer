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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  RevokeAccessDialog,
  type RevokeAccessDialogProps,
} from "@/components/venues";
import i18n from "@/i18n";

function renderConfirm(
  props: Partial<RevokeAccessDialogProps> &
    Pick<RevokeAccessDialogProps, "subject">,
) {
  const handlers = { onOpenChange: vi.fn(), onConfirm: vi.fn() };
  render(
    <I18nextProvider i18n={i18n}>
      <RevokeAccessDialog
        open
        connectionLabel="Alpaca paper"
        busy={false}
        {...handlers}
        {...props}
      />
    </I18nextProvider>,
  );
  return handlers;
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("RevokeAccessDialog", () => {
  it("names the account and the connection", () => {
    renderConfirm({ subject: { account: "alpha" } });
    const dialog = screen.getByRole("alertdialog", {
      name: "Revoke account access?",
    });
    expect(dialog).toHaveAccessibleDescription(
      "Revoke access of account alpha to Alpaca paper?",
    );
  });

  it("names the group and says its accounts keep only direct access", () => {
    renderConfirm({ subject: { accountGroup: "desk" } });
    const dialog = screen.getByRole("alertdialog", {
      name: "Revoke group access?",
    });
    expect(dialog).toHaveAccessibleDescription(
      "Revoke access of group desk to Alpaca paper? Accounts in the group keep access only if it is granted to them directly.",
    );
  });

  it("confirms, cancels and closes on Escape while idle", async () => {
    const user = userEvent.setup();
    const { onOpenChange, onConfirm } = renderConfirm({
      subject: { account: "alpha" },
    });
    const dialog = within(screen.getByRole("alertdialog"));
    await user.click(dialog.getByRole("button", { name: "Revoke access" }));
    expect(onConfirm).toHaveBeenCalledOnce();
    // Confirming leaves closing to the caller.
    expect(onOpenChange).not.toHaveBeenCalled();
    await user.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(onOpenChange).toHaveBeenLastCalledWith(false);
    onOpenChange.mockClear();
    await user.keyboard("{Escape}");
    expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false);
  });

  it("can be neither cancelled, closed nor confirmed while busy", async () => {
    const user = userEvent.setup();
    const { onOpenChange, onConfirm } = renderConfirm({
      subject: { account: "alpha" },
      busy: true,
    });
    const dialog = within(screen.getByRole("alertdialog"));
    const cancel = dialog.getByRole("button", { name: "Cancel" });
    const revoke = dialog.getByRole("button", { name: "Revoke access" });
    expect(cancel).toBeDisabled();
    expect(revoke).toBeDisabled();
    await user.click(cancel);
    await user.click(revoke);
    await user.keyboard("{Escape}");
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("shows the caller's error inside", () => {
    renderConfirm({
      subject: { accountGroup: "desk" },
      error: "revoke refused",
    });
    expect(
      within(screen.getByRole("alertdialog")).getByRole("alert"),
    ).toHaveTextContent("revoke refused");
  });
});
