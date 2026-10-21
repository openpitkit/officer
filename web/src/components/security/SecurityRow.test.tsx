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
import { describe, expect, it } from "vitest";

import { SecurityRow } from "@/components/security/SecurityRow";

describe("SecurityRow", () => {
  it("groups the value and the actions under the label", () => {
    render(
      <SecurityRow
        label="Panel password"
        actions={<button type="button">Change</button>}
      >
        Set today
      </SecurityRow>,
    );

    const group = screen.getByRole("group", { name: "Panel password" });
    expect(within(group).getByText("Set today")).toBeInTheDocument();
    expect(
      within(group).getByRole("button", { name: "Change" }),
    ).toBeInTheDocument();
  });

  it("holds a value with no actions", () => {
    render(<SecurityRow label="Access">The choice</SecurityRow>);

    const group = screen.getByRole("group", { name: "Access" });
    expect(within(group).getByText("The choice")).toBeInTheDocument();
    expect(within(group).queryByRole("button")).not.toBeInTheDocument();
  });
});
