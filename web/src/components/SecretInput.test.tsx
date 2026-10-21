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

import { SecretInput } from "@/components/SecretInput";

function renderSecretInput(
  props: Partial<Parameters<typeof SecretInput>[0]> = {},
) {
  const onChange = vi.fn();
  render(
    <SecretInput
      id="api-key"
      label="API key"
      value=""
      disabled={false}
      onChange={onChange}
      {...props}
    />,
  );
  return { onChange };
}

describe("SecretInput", () => {
  it("labels the input by its id", () => {
    renderSecretInput();
    const input = screen.getByLabelText("API key");
    expect(input.tagName).toBe("INPUT");
    expect(input).toHaveAttribute("id", "api-key");
  });

  it("masks what is typed", () => {
    renderSecretInput();
    expect(screen.getByLabelText("API key")).toHaveAttribute(
      "type",
      "password",
    );
  });

  it("renders the placeholder it is given", () => {
    renderSecretInput({ placeholder: "Keep stored secret" });
    expect(screen.getByLabelText("API key")).toHaveAttribute(
      "placeholder",
      "Keep stored secret",
    );
  });

  it("has no placeholder unless given one", () => {
    renderSecretInput();
    expect(screen.getByLabelText("API key")).not.toHaveAttribute("placeholder");
  });

  it("is enabled unless disabled", () => {
    renderSecretInput();
    expect(screen.getByLabelText("API key")).toBeEnabled();
  });

  it("disables the input", () => {
    renderSecretInput({ disabled: true });
    expect(screen.getByLabelText("API key")).toBeDisabled();
  });

  it("reports the whole typed value", async () => {
    const user = userEvent.setup();
    const { onChange } = renderSecretInput({ value: "ab" });
    await user.type(screen.getByLabelText("API key"), "c");
    expect(onChange).toHaveBeenCalledExactlyOnceWith("abc");
  });

  it("hands the error and the autofill hint to the field", () => {
    renderSecretInput({
      error: "Wrong password.",
      autoComplete: "new-password",
    });
    const input = screen.getByLabelText("API key");
    expect(input).toHaveAccessibleDescription("Wrong password.");
    expect(input).toHaveAttribute("autocomplete", "new-password");
  });
});
