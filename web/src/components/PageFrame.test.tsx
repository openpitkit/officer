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

import type { ReactNode } from "react";

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { PageFrame } from "@/components/PageFrame";
import { SidebarContext } from "@/components/sidebar-context";

function renderFrame(ui: ReactNode, toggle = vi.fn()) {
  render(
    <SidebarContext.Provider value={{ open: false, toggle, close: vi.fn() }}>
      {ui}
    </SidebarContext.Provider>,
  );
  return toggle;
}

describe("PageFrame", () => {
  it("shows the title and toggles the navigation from the menu button", async () => {
    const toggle = renderFrame(<PageFrame title="Orders">content</PageFrame>);

    const topBar = screen.getByRole("banner");
    expect(
      within(topBar).getByRole("heading", { level: 1, name: "Orders" }),
    ).toBeInTheDocument();

    await userEvent.click(
      within(topBar).getByRole("button", { name: "Toggle navigation" }),
    );
    expect(toggle).toHaveBeenCalledTimes(1);
  });

  it("places the controls in the top bar and the children in the content column", () => {
    renderFrame(
      <PageFrame
        title="Orders"
        controls={<button type="button">Refresh</button>}
      >
        <p>Order list</p>
      </PageFrame>,
    );

    const topBar = screen.getByRole("banner");
    const main = screen.getByRole("main");
    expect(
      within(topBar).getByRole("button", { name: "Refresh" }),
    ).toBeInTheDocument();
    expect(within(main).queryByRole("button", { name: "Refresh" })).toBeNull();
    expect(within(main).getByText("Order list")).toBeInTheDocument();
    expect(within(topBar).queryByText("Order list")).toBeNull();
  });

  it("renders no control area when no controls are given", () => {
    renderFrame(<PageFrame title="Orders">content</PageFrame>);

    const topBar = screen.getByRole("banner");
    expect(topBar.querySelector(".topbar-actions")).toBeNull();
    expect(within(topBar).getAllByRole("button")).toHaveLength(1);
  });
});
