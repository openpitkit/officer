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
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AdjustmentTypeField } from "@/components/AdjustmentTypeField";
import i18n from "@/i18n";

const LABELS = ["Venue fee", "Deposit", "Withdrawal", "Manual correction"];

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("AdjustmentTypeField", () => {
  it("offers every type under its label with none chosen for an empty value", () => {
    render(<AdjustmentTypeField value="" onChange={vi.fn()} />);

    const group = screen.getByRole("group", { name: "Type" });
    const options = within(group).getAllByRole("button");
    expect(options.map((option) => option.textContent)).toEqual(LABELS);
    for (const option of options) {
      expect(option).toHaveAttribute("aria-pressed", "false");
    }
  });

  it("marks only the chosen type as pressed", () => {
    render(<AdjustmentTypeField value="withdrawal" onChange={vi.fn()} />);

    const group = screen.getByRole("group", { name: "Type" });
    expect(
      within(group).getByRole("button", { name: "Withdrawal" }),
    ).toHaveAttribute("aria-pressed", "true");
    expect(
      within(group).getAllByRole("button", { pressed: true }),
    ).toHaveLength(1);
  });

  it("reports the chosen type by its identifier", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<AdjustmentTypeField value="" onChange={onChange} />);

    await user.click(screen.getByRole("button", { name: "Venue fee" }));

    expect(onChange).toHaveBeenCalledExactlyOnceWith("venue_fee");
  });

  it("disables every option when disabled", () => {
    render(<AdjustmentTypeField value="deposit" onChange={vi.fn()} disabled />);

    for (const option of screen.getAllByRole("button")) {
      expect(option).toBeDisabled();
    }
  });
});
