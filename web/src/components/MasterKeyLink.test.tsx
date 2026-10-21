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
import { I18nextProvider } from "react-i18next";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it } from "vitest";

import { MasterKeyLink } from "@/components/MasterKeyLink";
import i18n from "@/i18n";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

function LocationProbe() {
  const location = useLocation();
  return (
    <output aria-label="Current location">
      {location.pathname + location.hash}
    </output>
  );
}

describe("MasterKeyLink", () => {
  it("leads in the app to the Security tab of Service", async () => {
    const user = userEvent.setup();
    render(
      <I18nextProvider i18n={i18n}>
        <MemoryRouter initialEntries={["/trading-connections"]}>
          <Routes>
            <Route path="/trading-connections" element={<MasterKeyLink />} />
            <Route path="/service" element={<LocationProbe />} />
          </Routes>
        </MemoryRouter>
      </I18nextProvider>,
    );

    const link = screen.getByRole("link", { name: "How to set a master key" });
    expect(link).toHaveAttribute("href", "/service#security");
    expect(link).not.toHaveAttribute("target");
    await user.click(link);

    expect(screen.getByLabelText("Current location")).toHaveTextContent(
      "/service#security",
    );
  });
});
