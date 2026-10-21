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
import { describe, expect, it, vi } from "vitest";

import type { AccessTokenKind } from "@/api/access";
import { RegenerateTokenButton } from "@/components/security/RegenerateTokenButton";
import { ApiError } from "@/framework";

function renderButton(
  kind: AccessTokenKind,
  onRegenerate = vi.fn<() => Promise<void>>().mockResolvedValue(undefined),
) {
  render(<RegenerateTokenButton kind={kind} onRegenerate={onRegenerate} />);
  return { onRegenerate };
}

describe("RegenerateTokenButton", () => {
  it.each([
    [
      "agent",
      "Regenerate the agent token?",
      "The current agent token stops working at once: an agent that uses it loses MCP until its configuration gets the new one.",
    ],
    [
      "api",
      "Regenerate the API token?",
      "The current API token stops working at once: a program that uses it loses REST until it gets the new one.",
    ],
  ] as const)(
    "asks to confirm what stops working for the %s token before regenerating",
    async (kind, title, description) => {
      const user = userEvent.setup();
      const { onRegenerate } = renderButton(kind);

      await user.click(screen.getByRole("button", { name: "Regenerate" }));
      const dialog = screen.getByRole("alertdialog", { name: title });
      expect(dialog).toHaveAccessibleDescription(description);
      await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
      expect(onRegenerate).not.toHaveBeenCalled();

      await user.click(screen.getByRole("button", { name: "Regenerate" }));
      await user.click(
        within(screen.getByRole("alertdialog")).getByRole("button", {
          name: "Regenerate",
        }),
      );

      expect(onRegenerate).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    },
  );

  it("keeps the confirmation open with the error, and drops it on Cancel", async () => {
    const user = userEvent.setup();
    renderButton(
      "api",
      vi
        .fn<() => Promise<void>>()
        .mockRejectedValue(
          new ApiError("Officer runs in open access.", "conflict", 409),
        ),
    );

    await user.click(screen.getByRole("button", { name: "Regenerate" }));
    const dialog = screen.getByRole("alertdialog", {
      name: "Regenerate the API token?",
    });
    await user.click(
      within(dialog).getByRole("button", { name: "Regenerate" }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "Officer runs in open access.",
    );

    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "Regenerate" }));
    expect(
      within(screen.getByRole("alertdialog")).queryByRole("alert"),
    ).not.toBeInTheDocument();
  });
});
