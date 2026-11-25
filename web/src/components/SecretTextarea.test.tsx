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

import { SecretTextarea } from "@/components/SecretTextarea";

describe("SecretTextarea", () => {
  it("preserves multiline key text and disables autofill and spell checking", () => {
    const pem =
      "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----";
    render(
      <>
        <label htmlFor="key">Private key</label>
        <SecretTextarea id="key" value={pem} onChange={() => {}} />
      </>,
    );
    const input = screen.getByLabelText("Private key");
    expect(input.tagName).toBe("TEXTAREA");
    expect(input).toHaveValue(pem);
    expect(input).toHaveAttribute("rows", "5");
    expect(input).toHaveClass("resize-none", "font-mono", "text-xs");
    expect(input).toHaveAttribute("autocomplete", "off");
    expect(input).toHaveAttribute("autocorrect", "off");
    expect(input).toHaveAttribute("spellcheck", "false");
  });

  it("forwards input changes and field attributes", async () => {
    const onChange = vi.fn();
    render(
      <SecretTextarea
        placeholder="Keep stored key"
        required
        onChange={(event) => onChange(event.target.value)}
      />,
    );
    const input = screen.getByPlaceholderText("Keep stored key");
    expect(input).toBeRequired();
    await userEvent.setup().type(input, "first{Enter}second");
    expect(onChange).toHaveBeenLastCalledWith("first\nsecond");
  });

  it("prevents editing while disabled", async () => {
    const onChange = vi.fn();
    render(<SecretTextarea disabled value="stored" onChange={onChange} />);
    const input = screen.getByRole("textbox");
    expect(input).toBeDisabled();
    await userEvent.setup().type(input, "new");
    expect(onChange).not.toHaveBeenCalled();
  });
});
