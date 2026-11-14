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
import { Home, Settings } from "lucide-react";
import { beforeEach, describe, expect, it } from "vitest";
import { MemoryRouter } from "react-router-dom";

import { SidebarProvider } from "@/components/SidebarContext";
import { registerNav, resetNav } from "@/framework/registries/nav";
import { Sidebar } from "./Sidebar";
import { AuthProvider } from "./auth/AuthProvider";

beforeEach(() => {
  resetNav();
  registerNav({
    id: "orders",
    to: "/orders",
    labelKey: "nav.orders",
    icon: Home,
    section: "primary",
    order: 1,
  });
  registerNav({
    id: "service",
    to: "/service",
    labelKey: "nav.service",
    icon: Settings,
    section: "footer",
    order: 1,
  });
});

function mount() {
  render(
    <MemoryRouter initialEntries={["/orders"]}>
      <AuthProvider>
        <SidebarProvider>
          <Sidebar />
        </SidebarProvider>
      </AuthProvider>
    </MemoryRouter>,
  );
  return screen.getByRole("complementary");
}

describe("Sidebar", () => {
  it("keeps navigation groups in separate scroll containers and bounds the footer", () => {
    const sidebar = mount();
    const [primary, footer] =
      sidebar.querySelectorAll<HTMLElement>(".sidebar-scroll");
    expect(primary).toContainElement(
      within(sidebar).getByRole("link", { name: "Orders" }),
    );
    expect(footer).toContainElement(
      within(sidebar).getByRole("link", { name: "Service" }),
    );
    expect(primary).not.toContainElement(footer);
    expect(primary).toHaveClass("overflow-y-auto");
    expect(footer).toHaveClass("overflow-y-auto", "max-h-[66.666667%]");
  });

  it("collapses every navigation group and retains the choice after navigation", async () => {
    const user = userEvent.setup();
    const sidebar = mount();
    await user.click(
      within(sidebar).getByRole("button", { name: "Collapse sidebar" }),
    );
    expect(sidebar).toHaveAttribute("data-collapsed", "true");
    expect(within(sidebar).queryByText("Pit Officer")).not.toBeInTheDocument();
    expect(
      sidebar.querySelector(".ledger-masthead")!.querySelectorAll("svg"),
    ).toHaveLength(1);
    for (const name of ["Orders", "Service"]) {
      const link = within(sidebar).getByRole("link", { name });
      expect(link).toHaveAttribute("title", name);
      expect(within(link).getByText(name)).toHaveClass("sr-only");
    }
    await user.click(within(sidebar).getByRole("link", { name: "Service" }));
    expect(sidebar).toHaveAttribute("data-collapsed", "true");
    await user.click(
      within(sidebar).getByRole("button", { name: "Expand sidebar" }),
    );
    expect(sidebar).toHaveAttribute("data-collapsed", "false");
    expect(within(sidebar).getByText("Orders")).not.toHaveClass("sr-only");
  });
});
