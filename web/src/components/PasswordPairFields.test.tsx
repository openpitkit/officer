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

import { PasswordPairFields } from "@/components/PasswordPairFields";

describe("PasswordPairFields", () => {
  it("keeps each field's focus, refusal and edits separate", async () => {
    const user = userEvent.setup();
    const passwordRef = createRef<HTMLInputElement>();
    const repeatRef = createRef<HTMLInputElement>();
    const onPassword = vi.fn();
    const onRepeat = vi.fn();
    render(
      <PasswordPairFields
        password={{
          ref: passwordRef,
          id: "password",
          label: "New password",
          value: "",
          error: "The server refused the password.",
          onChange: onPassword,
        }}
        repeat={{
          ref: repeatRef,
          id: "repeat",
          label: "Repeat password",
          value: "",
          error: "The passwords differ.",
          onChange: onRepeat,
        }}
        disabled={false}
      />,
    );

    const password = screen.getByLabelText("New password");
    const repeat = screen.getByLabelText("Repeat password");
    passwordRef.current?.focus();
    expect(password).toHaveFocus();
    expect(password).toHaveAccessibleDescription(
      "The server refused the password.",
    );
    repeatRef.current?.focus();
    expect(repeat).toHaveFocus();
    expect(repeat).toHaveAccessibleDescription("The passwords differ.");

    await user.type(password, "x");
    await user.type(repeat, "y");
    expect(onPassword).toHaveBeenCalledExactlyOnceWith("x");
    expect(onRepeat).toHaveBeenCalledExactlyOnceWith("y");
  });

  it("keeps the inputs mounted when revealing or clearing a refused attempt", () => {
    const ref = createRef<HTMLInputElement>();
    const fields = {
      password: {
        ref,
        id: "password",
        label: "New password",
        value: "a long password",
        onChange: vi.fn(),
      },
      repeat: {
        id: "repeat",
        label: "Repeat password",
        value: "a long password",
        onChange: vi.fn(),
      },
    };
    const { rerender } = render(
      <PasswordPairFields {...fields} disabled={false} />,
    );
    const password = screen.getByLabelText("New password");
    ref.current?.focus();

    rerender(
      <PasswordPairFields {...fields} disabled={false} revealed verbatim />,
    );
    expect(screen.getByLabelText("New password")).toBe(password);
    expect(password).toHaveFocus();
    expect(password).toHaveAttribute("type", "text");
    expect(password).toHaveAttribute("spellcheck", "false");
    expect(screen.getByLabelText("Repeat password")).toHaveAttribute(
      "type",
      "text",
    );

    rerender(
      <PasswordPairFields
        password={{ ...fields.password, value: "" }}
        repeat={{ ...fields.repeat, value: "" }}
        disabled
      />,
    );
    expect(ref.current).toBe(password);
    for (const field of [password, screen.getByLabelText("Repeat password")]) {
      expect(field).toHaveValue("");
      expect(field).toBeDisabled();
      expect(field).toHaveAttribute("type", "password");
      expect(field).toHaveAttribute("autocomplete", "new-password");
      expect(field).not.toHaveAttribute("spellcheck");
    }
  });
});
