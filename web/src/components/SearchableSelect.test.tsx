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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SearchableSelect } from "@/components/SearchableSelect";

describe("SearchableSelect", () => {
  const options = ["UTC", "Europe/London", "Europe/Paris", "Asia/Tokyo"];
  it("opens all choices, filters without changing the value and selects using the keyboard", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <SearchableSelect
        id="zone"
        value="UTC"
        onChange={onChange}
        options={options}
        disabled={false}
        placeholder="Choose a zone"
        searchLabel="Search zones"
        emptyLabel="No matches"
      />,
    );
    await user.click(screen.getByRole("button", { name: "UTC" }));
    expect(screen.getAllByRole("menuitemradio")).toHaveLength(4);
    const search = screen.getByRole("searchbox");
    await user.click(search);
    await user.type(search, "not a zone");
    expect(screen.getByRole("status")).toHaveTextContent("No matches");
    expect(screen.queryByRole("menuitemradio")).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
    await user.clear(search);
    await user.type(search, "london");
    expect(screen.getAllByRole("menuitemradio")).toHaveLength(1);
    await user.keyboard("{ArrowDown}{Enter}");
    expect(onChange).toHaveBeenCalledExactlyOnceWith("Europe/London");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "UTC" })).toHaveFocus();
  });
  it("does not open when disabled", async () => {
    const user = userEvent.setup();
    render(
      <SearchableSelect
        id="zone"
        value="UTC"
        onChange={vi.fn()}
        options={options}
        disabled
        placeholder="Choose a zone"
        searchLabel="Search zones"
        emptyLabel="No matches"
      />,
    );
    await user.click(screen.getByRole("button", { name: "UTC" }));
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
  });
});
