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

import { Acknowledgement } from "@/components/Acknowledgement";

const LABEL = "I understand what this does.";

function renderAcknowledgement({
  acknowledged = false,
  disabled = false,
}: {
  acknowledged?: boolean;
  disabled?: boolean;
} = {}) {
  const onChange = vi.fn();
  render(
    <Acknowledgement
      acknowledged={acknowledged}
      onChange={onChange}
      disabled={disabled}
    >
      {LABEL}
    </Acknowledgement>,
  );
  return { onChange };
}

function checkbox() {
  return screen.getByRole("checkbox", { name: LABEL });
}

describe("Acknowledgement", () => {
  it("is a checkbox named by its label", () => {
    renderAcknowledgement();

    expect(checkbox()).toBeInTheDocument();
  });

  it("shows the acknowledged state it is given", () => {
    renderAcknowledgement({ acknowledged: true });

    expect(checkbox()).toBeChecked();
  });

  it("shows an unacknowledged state as unchecked", () => {
    renderAcknowledgement({ acknowledged: false });

    expect(checkbox()).not.toBeChecked();
  });

  it("reports true when an unchecked box is clicked", async () => {
    const user = userEvent.setup();
    const { onChange } = renderAcknowledgement({ acknowledged: false });

    await user.click(checkbox());

    expect(onChange).toHaveBeenCalledExactlyOnceWith(true);
  });

  it("reports false when a checked box is clicked", async () => {
    const user = userEvent.setup();
    const { onChange } = renderAcknowledgement({ acknowledged: true });

    await user.click(checkbox());

    expect(onChange).toHaveBeenCalledExactlyOnceWith(false);
  });

  it("keeps a disabled box as it is", async () => {
    const user = userEvent.setup();
    const { onChange } = renderAcknowledgement({ disabled: true });

    expect(checkbox()).toBeDisabled();
    await user.click(checkbox());

    expect(onChange).not.toHaveBeenCalled();
    expect(checkbox()).not.toBeChecked();
  });
});
