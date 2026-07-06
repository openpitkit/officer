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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { Autocomplete } from "@/components/Autocomplete";

function Harness({
  onClear,
  initial = "",
  suggestions = ["crypto", "credit", "equity"],
}: {
  onClear?: () => void;
  initial?: string;
  suggestions?: string[];
}) {
  const [value, setValue] = useState(initial);
  return (
    <Autocomplete
      aria-label="dictionary field"
      value={value}
      onChange={setValue}
      suggestions={suggestions}
      onClear={
        onClear
          ? () => {
              onClear();
              setValue("");
            }
          : undefined
      }
      clearLabel="Clear field"
    />
  );
}

describe("Autocomplete", () => {
  it("renders the inline reset glyph only while non-empty and clears in place", async () => {
    const user = userEvent.setup();
    const onClear = vi.fn();
    render(<Harness onClear={onClear} />);

    const input = screen.getByRole("combobox", { name: "dictionary field" });
    // No inline clear while empty.
    expect(
      screen.queryByRole("button", { name: "Clear field" }),
    ).not.toBeInTheDocument();

    await user.type(input, "cr");
    const clear = screen.getByRole("button", { name: "Clear field" });
    expect(clear).toBeInTheDocument();
    // The reset glyph stays out of the tab order and keeps focus on the input.
    expect(clear).toHaveAttribute("tabindex", "-1");

    await user.click(clear);
    expect(onClear).toHaveBeenCalledTimes(1);
    expect(input).toHaveValue("");
    expect(
      screen.queryByRole("button", { name: "Clear field" }),
    ).not.toBeInTheDocument();
  });

  it("suggests matching dictionary values from the known set", async () => {
    const user = userEvent.setup();
    render(<Harness onClear={vi.fn()} />);

    await user.type(
      screen.getByRole("combobox", { name: "dictionary field" }),
      "cr",
    );
    // Prefix match over the known set: "crypto" and "credit", not "equity".
    expect(screen.getByRole("option", { name: "crypto" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "credit" })).toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: "equity" }),
    ).not.toBeInTheDocument();
  });

  it("promotes a case-insensitive exact match to the top of the list", async () => {
    const user = userEvent.setup();
    // The exact match "USD" is not first in the source list, so a
    // case-sensitive exact test would fail to promote it for the lowercase
    // query and leave the longer prefix matches ahead of it.
    render(<Harness suggestions={["USDC", "USDT", "USD"]} />);

    await user.type(
      screen.getByRole("combobox", { name: "dictionary field" }),
      "usd",
    );

    expect(
      screen.getAllByRole("option").map((option) => option.textContent),
    ).toEqual(["USD", "USDC", "USDT"]);
  });

  it("de-duplicates case-variant suggestions", async () => {
    const user = userEvent.setup();
    render(<Harness suggestions={["USD", "usd", "USDC"]} />);

    await user.type(
      screen.getByRole("combobox", { name: "dictionary field" }),
      "us",
    );

    // "USD" and "usd" collapse to a single entry (the first canonical form),
    // so the list is not padded with a duplicate-looking case variant.
    expect(
      screen.getAllByRole("option").map((option) => option.textContent),
    ).toEqual(["USD", "USDC"]);
  });

  it("keeps an exact dictionary match ahead of longer prefix matches", async () => {
    const user = userEvent.setup();
    render(<Harness suggestions={["USD", "USDC", "USDT"]} />);

    await user.type(
      screen.getByRole("combobox", { name: "dictionary field" }),
      "USD",
    );

    expect(
      screen.getAllByRole("option").map((option) => option.textContent),
    ).toEqual(["USD", "USDC", "USDT"]);
  });

  it("omits the inline reset glyph when no onClear is provided", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(
      screen.getByRole("combobox", { name: "dictionary field" }),
      "cr",
    );
    expect(
      screen.queryByRole("button", { name: "Clear field" }),
    ).not.toBeInTheDocument();
  });
});
