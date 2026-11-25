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

import { ConfirmDialog } from "@/components/ConfirmDialog";
import i18n from "@/i18n";

const title = "Reset realized P&L for desk-alpha?";
const confirmLabel = "Reset to 0";

function renderConfirmation({
  busy = false,
  error,
}: { busy?: boolean; error?: string } = {}) {
  const onOpenChange = vi.fn();
  const onConfirm = vi.fn();
  render(
    <I18nextProvider i18n={i18n}>
      <ConfirmDialog
        open
        onOpenChange={onOpenChange}
        title={title}
        description="Sets the account's realized P&L to 0."
        confirmLabel={confirmLabel}
        busy={busy}
        error={error}
        onConfirm={onConfirm}
      />
    </I18nextProvider>,
  );
  const dialog = within(screen.getByRole("alertdialog", { name: title }));
  return { dialog, onOpenChange, onConfirm };
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("ConfirmDialog", () => {
  it("ignores Escape while the action is pending", async () => {
    const user = userEvent.setup();
    const { onOpenChange } = renderConfirmation({ busy: true });

    await user.keyboard("{Escape}");

    expect(onOpenChange).not.toHaveBeenCalled();
  });

  it.each(["Cancel", confirmLabel])(
    "disables %s while the action is pending",
    (name) => {
      const { dialog } = renderConfirmation({ busy: true });

      expect(dialog.getByRole("button", { name })).toBeDisabled();
    },
  );

  it("requests close when the operator cancels", async () => {
    const user = userEvent.setup();
    const { dialog, onOpenChange } = renderConfirmation();

    await user.click(dialog.getByRole("button", { name: "Cancel" }));

    expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false);
  });

  it("confirms the action and leaves closing to the caller", async () => {
    const user = userEvent.setup();
    const { dialog, onConfirm, onOpenChange } = renderConfirmation();

    await user.click(dialog.getByRole("button", { name: confirmLabel }));

    expect(onConfirm).toHaveBeenCalledOnce();
    expect(onOpenChange).not.toHaveBeenCalled();
  });

  it("shows the caller's error inside the confirmation", () => {
    const error = "The service could not reset the account's realized P&L.";
    const { dialog } = renderConfirmation({ error });

    expect(dialog.getByRole("alert")).toHaveTextContent(error);
  });

  it("uses the danger tone for the confirmation action", () => {
    const { dialog } = renderConfirmation();

    expect(dialog.getByRole("button", { name: confirmLabel })).toHaveClass(
      "bg-[var(--danger)]",
    );
  });
});
