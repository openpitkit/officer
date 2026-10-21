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

import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { CopyableSnippet } from "@/components/CopyableSnippet";

const SNIPPET = '{"mcpServers": {}}';

describe("CopyableSnippet", () => {
  it("shows the text read-only under its label and copies it", async () => {
    const user = userEvent.setup();
    render(<CopyableSnippet text={SNIPPET} label="Claude Desktop config" />);

    const block = screen.getByRole("textbox", {
      name: "Claude Desktop config",
    });
    expect(block).toHaveValue(SNIPPET);
    expect(block).toHaveAttribute("readonly");
    await user.click(screen.getByRole("button", { name: "Copy" }));

    expect(await navigator.clipboard.readText()).toBe(SNIPPET);
    expect(
      await screen.findByRole("button", { name: "Copied" }),
    ).toBeInTheDocument();
  });

  it("shows the text and copies the separate copy text when given", async () => {
    const user = userEvent.setup();
    render(
      <CopyableSnippet
        text="Bearer pit_mcp_Q2x7••••Vr4w"
        copyText="Bearer pit_mcp_real"
        label="Command"
      />,
    );

    expect(screen.getByRole("textbox", { name: "Command" })).toHaveValue(
      "Bearer pit_mcp_Q2x7••••Vr4w",
    );
    await user.click(screen.getByRole("button", { name: "Copy" }));

    expect(await navigator.clipboard.readText()).toBe("Bearer pit_mcp_real");
  });

  it("puts the copy text on the clipboard when the text area itself is copied", () => {
    render(
      <CopyableSnippet
        text="Bearer pit_mcp_Q2x7••••Vr4w"
        copyText="Bearer pit_mcp_real"
        label="Command"
      />,
    );
    const setData = vi.fn();

    const notPrevented = fireEvent.copy(
      screen.getByRole("textbox", { name: "Command" }),
      { clipboardData: { setData } },
    );

    expect(notPrevented).toBe(false);
    expect(setData).toHaveBeenCalledExactlyOnceWith(
      "text/plain",
      "Bearer pit_mcp_real",
    );
  });

  it("leaves a copy of the text area to the browser without copy text", () => {
    render(<CopyableSnippet text={SNIPPET} label="Claude Desktop config" />);
    const setData = vi.fn();

    const notPrevented = fireEvent.copy(
      screen.getByRole("textbox", { name: "Claude Desktop config" }),
      { clipboardData: { setData } },
    );

    expect(notPrevented).toBe(true);
    expect(setData).not.toHaveBeenCalled();
  });

  it("names an unlabelled snippet for assistive technology", () => {
    render(<CopyableSnippet text={SNIPPET} />);

    expect(
      screen.getByRole("textbox", { name: "Copyable snippet" }),
    ).toHaveValue(SNIPPET);
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
  });
});
