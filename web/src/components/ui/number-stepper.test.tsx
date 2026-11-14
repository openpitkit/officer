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

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { NumberStepper } from "@/components/ui/number-stepper";

describe("NumberStepper", () => {
  it("rejects non-decimal edits without changing the value sent to its owner", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    render(<NumberStepper aria-label="Amount" value="" onChange={onChange} />);

    const input = screen.getByRole("textbox", { name: "Amount" });
    await user.type(input, "word12a.3b");

    expect(input).toHaveValue("12.3");
    expect(onChange.mock.calls.map(([value]) => value)).toEqual([
      "1",
      "12",
      "12.",
      "12.3",
    ]);
    await user.click(input);
    await user.paste("4x5");
    expect(input).toHaveValue("12.3");
    expect(onChange).toHaveBeenCalledTimes(4);
    expect(input).toBeValid();
  });

  it.each(["-", "+", ".", "-.", "+."])(
    "allows editing the incomplete decimal prefix %j without accepting it as a number",
    async (prefix) => {
      const onChange = vi.fn();
      const user = userEvent.setup();
      render(
        <NumberStepper
          aria-label="Amount"
          value=""
          valueType="quantity"
          onChange={onChange}
        />,
      );
      const input = screen.getByRole("textbox", { name: "Amount" });
      await user.type(input, prefix);
      expect(input).toHaveValue(prefix);
      expect(input).toBeInvalid();
      await user.type(input, "5");
      expect(input).toHaveValue(`${prefix}5`);
      expect(input).toBeValid();
    },
  );

  it("keeps invalid externally supplied data visible and invalid", async () => {
    render(
      <NumberStepper aria-label="Amount" value="word" onChange={vi.fn()} />,
    );
    const input = screen.getByRole("textbox", { name: "Amount" });
    expect(input).toHaveValue("word");
    await waitFor(() => expect(input).toBeInvalid());
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveProperty("validationMessage", "Enter a valid number.");
  });

  it("preserves pasted decimal precision and rejects misplaced separators or exponents", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<NumberStepper aria-label="Amount" value="" onChange={onChange} />);
    const input = screen.getByRole("textbox", { name: "Amount" });
    const exact = "0.0000000000000123456789012345";
    await user.click(input);
    await user.paste(exact);
    expect(input).toHaveValue(exact);
    expect(onChange).toHaveBeenLastCalledWith(exact);
    onChange.mockClear();
    for (const invalid of ["12.3.4", "1e3", "1,2.5", "2-3", " 12 "]) {
      await user.tripleClick(input);
      await user.paste(invalid);
      expect(input).toHaveValue(exact);
      expect(onChange).not.toHaveBeenCalled();
    }
  });

  it.each([",", "."])(
    "keeps sequential %s decimal entry exact",
    async (separator) => {
      const onChange = vi.fn();
      const user = userEvent.setup();
      render(
        <NumberStepper aria-label="Amount" value="" onChange={onChange} />,
      );
      const input = screen.getByRole("textbox", { name: "Amount" });
      await user.type(input, `1${separator}25`);
      expect(input).toHaveValue("1.25");
      expect(onChange.mock.calls.map(([value]) => value)).toEqual([
        "1",
        "1.",
        "1.2",
        "1.25",
      ]);
      expect(input).toBeValid();
    },
  );

  it.each(["price", "quantity"] as const)(
    "normalizes pasted signed %s decimals without losing precision",
    async (valueType) => {
      const onChange = vi.fn();
      const user = userEvent.setup();
      render(
        <NumberStepper
          aria-label="Amount"
          value=""
          valueType={valueType}
          onChange={onChange}
        />,
      );
      const input = screen.getByRole("textbox", { name: "Amount" });
      for (const sign of ["-", "+"]) {
        for (const separator of [",", "."]) {
          await user.clear(input);
          await user.click(input);
          await user.paste(
            `${sign}12345678901234567890${separator}000000000000012345`,
          );
          const exact = `${sign}12345678901234567890.000000000000012345`;
          expect(input).toHaveValue(exact);
          expect(onChange).toHaveBeenLastCalledWith(exact);
          expect(input).toBeValid();
        }
      }
    },
  );

  it("admits only ASCII digits for whole-number edits and rejects whole invalid pastes", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <NumberStepper
        aria-label="Count"
        value=""
        integerOnly
        onChange={onChange}
      />,
    );
    const input = screen.getByRole("textbox", { name: "Count" });
    expect(input).toHaveAttribute("inputmode", "numeric");
    await user.type(input, "0123456789");
    expect(input).toHaveValue("0123456789");
    onChange.mockClear();
    for (const invalid of [",", ".", "+", "-", "e", "١"]) {
      await user.type(input, invalid);
      expect(input).toHaveValue("0123456789");
      expect(onChange).not.toHaveBeenCalled();
    }
    for (const invalid of ["1,25", "1.25", "-2", "+2", " 2 ", "2x3"]) {
      await user.tripleClick(input);
      await user.paste(invalid);
      expect(input).toHaveValue("0123456789");
      expect(onChange).not.toHaveBeenCalled();
    }
  });

  it("steps whole numbers without fractions or negative counts", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <NumberStepper
        aria-label="Count"
        value=""
        integerOnly
        min={null}
        onChange={onChange}
      />,
    );
    const input = screen.getByRole("textbox", { name: "Count" });
    await user.click(input);
    await user.keyboard("{ArrowUp}");
    expect(input).toHaveValue("1");
    await user.click(screen.getByRole("button", { name: "Increase" }));
    expect(input).toHaveValue("2");
    await user.click(input);
    await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}");
    expect(input).toHaveValue("0");
    expect(onChange).toHaveBeenLastCalledWith("0");
  });

  it("syncs external values and validity after a comma edit and clear", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const onClear = vi.fn();
    const { rerender } = render(
      <NumberStepper
        aria-label="Amount"
        value=""
        onChange={onChange}
        onClear={onClear}
      />,
    );
    const input = screen.getByRole("textbox", { name: "Amount" });
    await user.type(input, "1,25");
    rerender(
      <NumberStepper
        aria-label="Amount"
        value="2.50"
        onChange={onChange}
        onClear={onClear}
        customValidity="Range error"
      />,
    );
    await waitFor(() => expect(input).toHaveValue("2.50"));
    expect(input).toHaveProperty("validationMessage", "Range error");
    await user.keyboard("{ArrowUp}");
    expect(input).toHaveValue("2.50");
    await user.click(screen.getByRole("button", { name: "Clear field" }));
    expect(onClear).toHaveBeenCalledOnce();
    expect(input).toHaveValue("");
    rerender(
      <NumberStepper
        aria-label="Amount"
        value="3"
        onChange={onChange}
        integerOnly
      />,
    );
    await waitFor(() => expect(input).toHaveValue("3"));
    expect(input).toBeValid();
    rerender(
      <NumberStepper
        aria-label="Amount"
        value="3.5"
        onChange={onChange}
        integerOnly
      />,
    );
    await waitFor(() => expect(input).toBeInvalid());
    await user.keyboard("{ArrowUp}");
    expect(input).toHaveValue("3.5");
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

  it("rejects signs for a generic number with a nonnegative lower bound", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    render(<NumberStepper aria-label="Amount" value="" onChange={onChange} />);

    const input = screen.getByRole("textbox", { name: "Amount" });
    await user.type(input, "+0.50");

    expect(input).toHaveValue("0.50");
    expect(onChange).not.toHaveBeenCalledWith(expect.stringMatching(/^[+-]/));
  });

  it.each(["price", "quantity"] as const)(
    "accepts a negative %s and steps below zero without a separate sign flag",
    async (valueType) => {
      const onChange = vi.fn();
      const user = userEvent.setup();
      render(
        <NumberStepper
          aria-label="Value"
          value=""
          valueType={valueType}
          onChange={onChange}
        />,
      );
      const input = screen.getByRole("textbox", { name: "Value" });
      await user.type(input, "-0.50");
      expect(input).toHaveValue("-0.50");
      expect(input).toBeValid();
      await user.clear(input);
      await user.type(input, "0");
      await user.keyboard("{ArrowDown}");
      expect(input).toHaveValue("-0.1");
      expect(onChange).toHaveBeenLastCalledWith("-0.1");
      await user.tripleClick(input);
      await user.paste("-12.500000000000001");
      expect(input).toHaveValue("-12.500000000000001");
    },
  );
});
