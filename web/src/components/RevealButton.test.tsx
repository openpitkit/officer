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

import { RevealButton } from "@/components/RevealButton";

describe("RevealButton", () => {
  it("reads Show with the eye icon while the value is hidden", () => {
    render(<RevealButton shown={false} onToggle={() => {}} />);

    const button = screen.getByRole("button", { name: "Show" });
    expect(button).toHaveAttribute("type", "button");
    expect(button.querySelector("svg")).toHaveClass("lucide-eye");
    expect(button.querySelector("svg")).not.toHaveClass("lucide-eye-off");
  });

  it("reads Hide with the crossed eye icon while the value is shown", () => {
    render(<RevealButton shown onToggle={() => {}} />);

    const button = screen.getByRole("button", { name: "Hide" });
    expect(button.querySelector("svg")).toHaveClass("lucide-eye-off");
  });

  it("calls onToggle on click and leaves the state to the caller", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    render(<RevealButton shown={false} onToggle={onToggle} />);

    await user.click(screen.getByRole("button", { name: "Show" }));

    expect(onToggle).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Show" })).toBeInTheDocument();
  });

  it("keeps the password form's ghost control disabled during a request", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    render(
      <RevealButton
        shown={false}
        onToggle={onToggle}
        disabled
        variant="ghost"
      />,
    );

    const button = screen.getByRole("button", { name: "Show" });
    expect(button).toBeDisabled();
    expect(button).toHaveClass("border-transparent");
    await user.click(button);
    expect(onToggle).not.toHaveBeenCalled();
  });
});
