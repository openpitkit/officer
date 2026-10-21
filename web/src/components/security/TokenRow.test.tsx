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
import { TokenRow } from "@/components/security/TokenRow";
import { ApiError } from "@/framework";

const AGENT_TOKEN = "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w";
const API_TOKEN = "pit_api_Lm4Ts9Qa2Xc7Vn1Bk6Hd3Wy8Pe5Rz0Gu2Jf7Os4Ni9w";
const DOTS = "•".repeat(14);

function renderRow(
  kind: AccessTokenKind,
  token: string,
  onRegenerate = vi.fn<() => Promise<void>>().mockResolvedValue(undefined),
) {
  render(<TokenRow kind={kind} token={token} onRegenerate={onRegenerate} />);
  return { onRegenerate };
}

function row(name: string) {
  return screen.getByRole("group", { name });
}

describe("TokenRow", () => {
  it.each([
    [
      "agent",
      AGENT_TOKEN,
      "Agent token · MCP",
      `pit_mcp_Q2x7${DOTS}Vr4w`,
      "The same token as in Connect your agent on MCP access. Regenerating stops the old one at once.",
    ],
    [
      "api",
      API_TOKEN,
      "API token · REST",
      `pit_api_Lm4T${DOTS}Ni9w`,
      "Full control over REST - the same access the panel holds through its session. Never give it to an agent.",
    ],
  ] as const)(
    "masks the %s token as its prefix and its first and last four characters",
    (kind, token, label, shown, note) => {
      renderRow(kind, token);

      const tokenRow = row(label);
      expect(within(tokenRow).getByText(shown)).toBeInTheDocument();
      expect(within(tokenRow).getByText(note)).toBeInTheDocument();
      expect(document.body.innerHTML).not.toContain(token);
    },
  );

  it("shows the whole token on Show and masks it again on Hide", async () => {
    const user = userEvent.setup();
    renderRow("agent", AGENT_TOKEN);

    await user.click(screen.getByRole("button", { name: "Show" }));
    expect(screen.getByText(AGENT_TOKEN)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Hide" }));
    expect(screen.queryByText(AGENT_TOKEN)).not.toBeInTheDocument();
    expect(screen.getByText(`pit_mcp_Q2x7${DOTS}Vr4w`)).toBeInTheDocument();
  });

  it("copies the whole token while it is masked", async () => {
    const user = userEvent.setup();
    renderRow("api", API_TOKEN);

    await user.click(screen.getByRole("button", { name: "Copy" }));

    expect(await navigator.clipboard.readText()).toBe(API_TOKEN);
    expect(
      await screen.findByRole("button", { name: "Copied" }),
    ).toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain(API_TOKEN);
  });

  it("regenerates only after the confirmation", async () => {
    const user = userEvent.setup();
    const { onRegenerate } = renderRow("agent", AGENT_TOKEN);

    await user.click(screen.getByRole("button", { name: "Regenerate" }));
    const dialog = screen.getByRole("alertdialog", {
      name: "Regenerate the agent token?",
    });
    expect(dialog).toHaveAccessibleDescription(
      "The current agent token stops working at once: an agent that uses it loses MCP until its configuration gets the new one.",
    );
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
  });

  it("keeps the confirmation open with the error of a failed regeneration", async () => {
    const user = userEvent.setup();
    renderRow(
      "api",
      API_TOKEN,
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
    expect(dialog).toBeInTheDocument();
  });
});
