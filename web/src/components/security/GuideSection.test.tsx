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
import { MemoryRouter, useLocation } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { GuideSection } from "@/components/security/GuideSection";

function LocationProbe() {
  const location = useLocation();
  return <output aria-label="Current location">{location.pathname}</output>;
}

describe("GuideSection", () => {
  it("shows the first-run guide and opens it again at /setup", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/service"]}>
        <GuideSection />
        <LocationProbe />
      </MemoryRouter>,
    );

    const row = screen.getByRole("group", { name: "First-run guide" });
    expect(
      within(row).getByText("Every step, with your current choices"),
    ).toBeInTheDocument();
    await user.click(within(row).getByRole("link", { name: "Open again" }));

    expect(screen.getByLabelText("Current location")).toHaveTextContent(
      "/setup",
    );
  });
});
