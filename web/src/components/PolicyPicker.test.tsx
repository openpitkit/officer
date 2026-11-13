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

import { registerAppVocabulary } from "@/appDefaults";
import { PolicyPicker } from "@/components/PolicyPicker";
import i18n from "@/i18n";

beforeEach(async () => {
  await i18n.changeLanguage("en");
  registerAppVocabulary();
});

function renderPicker() {
  const onSelect = vi.fn();
  render(
    <I18nextProvider i18n={i18n}>
      <PolicyPicker onSelect={onSelect} />
    </I18nextProvider>,
  );
  return onSelect;
}

describe("PolicyPicker", () => {
  it("opens a group and selects a policy immediately while wiki links remain independent", async () => {
    const user = userEvent.setup();
    const onSelect = renderPicker();
    expect(screen.getAllByRole("button")).toHaveLength(3);
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Orders/ }));
    expect(
      screen.queryByRole("button", { name: /^Realized/ }),
    ).not.toBeInTheDocument();
    const button = screen.getByRole("button", { name: /^Order size limit/ });
    const tile = button.parentElement;
    if (!tile) throw new Error("Policy tile missing");
    const wiki = within(tile).getByRole("link", { name: "Details" });
    expect(wiki).toHaveAttribute(
      "href",
      "https://wiki.openpit.dev/Policies/?officer#ordersizelimitpolicy",
    );
    await user.click(wiki);
    expect(onSelect).not.toHaveBeenCalled();
    await user.click(button);
    expect(onSelect).toHaveBeenCalledExactlyOnceWith("order_size_limit");
  });

  it("shows an ordinary empty group and returns to all three groups", async () => {
    const user = userEvent.setup();
    const onSelect = renderPicker();
    await user.click(
      screen.getByRole("button", { name: /^Confirmation and review/ }),
    );
    expect(screen.getAllByRole("button")).toHaveLength(1);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(
      screen.queryByText(/0\.4|planned|coming soon/i),
    ).not.toBeInTheDocument();
    expect(onSelect).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "All groups" }));
    expect(screen.getAllByRole("button")).toHaveLength(3);
  });

  it("groups all P&L policies and excludes the permanent funds mode", async () => {
    const user = userEvent.setup();
    renderPicker();
    expect(
      screen.queryByRole("button", { name: /^Funds/ }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^P&L/ }));
    expect(
      screen.getByRole("button", { name: /^Realized/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /^Unrealized/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Total/ })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Details" })).toHaveLength(3);
    expect(screen.queryByText("Funds limit mode")).not.toBeInTheDocument();
  });
});
