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
import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";

import { TextField } from "@/components/TextField";

function renderTextField(props: Partial<Parameters<typeof TextField>[0]> = {}) {
  const onChange = vi.fn();
  render(
    <TextField
      id="recovery-code"
      label="Recovery code"
      type="text"
      value=""
      disabled={false}
      onChange={onChange}
      {...props}
    />,
  );
  return { onChange };
}

function field() {
  return screen.getByLabelText("Recovery code");
}

describe("TextField", () => {
  it("labels the input by its id", () => {
    renderTextField();
    expect(field().tagName).toBe("INPUT");
    expect(field()).toHaveAttribute("id", "recovery-code");
  });

  it("hands its input to the ref it is given", () => {
    const ref = createRef<HTMLInputElement>();
    renderTextField({ ref });
    expect(ref.current).toBe(field());
  });

  it.each(["text", "password"] as const)("renders a %s input", (type) => {
    renderTextField({ type });
    expect(field()).toHaveAttribute("type", type);
  });

  it("renders the placeholder it is given", () => {
    renderTextField({ placeholder: "XXXX-XXXX-XXXX" });
    expect(field()).toHaveAttribute("placeholder", "XXXX-XXXX-XXXX");
  });

  it("disables the input", () => {
    renderTextField({ disabled: true });
    expect(field()).toBeDisabled();
  });

  it("reports the whole typed value", async () => {
    const user = userEvent.setup();
    const { onChange } = renderTextField({ value: "7KQ" });
    await user.type(field(), "4");
    expect(onChange).toHaveBeenCalledExactlyOnceWith("7KQ4");
  });

  it("shows the error under the input and marks the input invalid by it", () => {
    renderTextField({ error: "The code is wrong or has expired." });
    expect(field()).toHaveAttribute("aria-invalid", "true");
    expect(field()).toHaveAccessibleDescription(
      "The code is wrong or has expired.",
    );
    expect(
      screen.getByText("The code is wrong or has expired."),
    ).toHaveAttribute("id", "recovery-code-error");
  });

  it("is neither invalid nor described without an error", () => {
    renderTextField();
    expect(field()).not.toHaveAttribute("aria-invalid");
    expect(field()).not.toHaveAttribute("aria-describedby");
  });

  it("passes the autofill hint it is given", () => {
    renderTextField({ autoComplete: "one-time-code" });
    expect(field()).toHaveAttribute("autocomplete", "one-time-code");
  });

  it("has no autofill hint unless given one", () => {
    renderTextField();
    expect(field()).not.toHaveAttribute("autocomplete");
  });

  it("turns off spell check, autocorrection and autocapitalisation when verbatim", () => {
    renderTextField({ verbatim: true });
    expect(field()).toHaveAttribute("spellcheck", "false");
    expect(field()).toHaveAttribute("autocorrect", "off");
    expect(field()).toHaveAttribute("autocapitalize", "off");
  });

  it("leaves spell check, autocorrection and autocapitalisation to the browser by default", () => {
    renderTextField();
    expect(field()).not.toHaveAttribute("spellcheck");
    expect(field()).not.toHaveAttribute("autocorrect");
    expect(field()).not.toHaveAttribute("autocapitalize");
  });
});
