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
import { describe, expect, it } from "vitest";

import { BrandLockup } from "@/components/BrandLockup";

describe("BrandLockup", () => {
  it("shows the mark beside the product name and the control-plane label", () => {
    const { container } = render(<BrandLockup />);

    const mark = screen.getByRole("img", { name: "Pit logo" });
    const name = screen.getByText("Pit Officer");
    const label = screen.getByText("Control plane");
    // Siblings for the parent's row: the mark, then the two lines.
    expect(container.firstElementChild).toBe(mark);
    expect(mark.nextElementSibling).toContainElement(name);
    expect(name.nextElementSibling).toBe(label);
  });
});
