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
// Please see https://openpit.dev and the OWNERS file for details.

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { NumberStepper } from "@/components/ui/number-stepper";

describe("NumberStepper", () => {
  it("reports invalid numeric drafts to its owner and marks the input invalid", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    render(<NumberStepper aria-label="Amount" value="" onChange={onChange} />);

    const input = screen.getByRole("textbox", { name: "Amount" });
    await user.type(input, "word");

    expect(input).toHaveValue("word");
    expect(onChange).toHaveBeenLastCalledWith("word");
    await waitFor(() => expect(input).toBeInvalid());
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveProperty("validationMessage", "Enter a valid number.");
  });

  it("commits empty and decimal values", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    render(<NumberStepper aria-label="Amount" value="" onChange={onChange} />);

    const input = screen.getByRole("textbox", { name: "Amount" });
    await user.type(input, "12.5");
    await user.clear(input);

    expect(onChange).toHaveBeenNthCalledWith(1, "1");
    expect(onChange).toHaveBeenNthCalledWith(2, "12");
    expect(onChange).toHaveBeenNthCalledWith(3, "12.");
    expect(onChange).toHaveBeenNthCalledWith(4, "12.5");
    expect(onChange).toHaveBeenLastCalledWith("");
    expect(input).toBeValid();
  });

  it("rejects explicit signs when signed input is disabled", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    render(
      <NumberStepper
        aria-label="Amount"
        value=""
        onChange={onChange}
        allowSignedInput={false}
      />,
    );

    const input = screen.getByRole("textbox", { name: "Amount" });
    await user.type(input, "+0.50");

    expect(input).toHaveValue("0.50");
    expect(onChange).not.toHaveBeenCalledWith(expect.stringMatching(/^[+-]/));
  });
});
