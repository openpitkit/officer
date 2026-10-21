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

import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Cloud, HardDrive, Server } from "lucide-react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { ChoiceCards, type ChoiceCard } from "@/components/ChoiceCards";

type Place = "local" | "server" | "cloud";

const OPTIONS: readonly ChoiceCard<Place>[] = [
  {
    value: "local",
    title: "This computer",
    description: "Keeps the files on this disk.",
    icon: HardDrive,
    badge: "Recommended",
  },
  {
    value: "server",
    title: "A server",
    description: "Keeps the files on a server you run.",
    icon: Server,
  },
  {
    value: "cloud",
    title: "The cloud",
    description: "Keeps the files with a provider.",
    icon: Cloud,
  },
];

function renderChoice({
  options = OPTIONS,
  initial = "local",
  disabled,
}: {
  options?: readonly ChoiceCard<Place>[];
  initial?: Place;
  disabled?: boolean;
} = {}) {
  const onChange = vi.fn();
  function Controlled() {
    const [value, setValue] = useState<Place>(initial);
    return (
      <ChoiceCards
        label="Where to keep the files"
        options={options}
        value={value}
        disabled={disabled}
        onChange={(next) => {
          onChange(next);
          setValue(next);
        }}
      />
    );
  }
  render(<Controlled />);
  return { onChange };
}

function radio(name: string) {
  return screen.getByRole("radio", { name });
}

describe("ChoiceCards", () => {
  it("is a radio group named by its label, one radio per card", () => {
    renderChoice();

    const group = screen.getByRole("radiogroup", {
      name: "Where to keep the files",
    });
    expect(group).toContainElement(radio("This computer"));
    expect(screen.getAllByRole("radio")).toHaveLength(3);
  });

  it("describes each card by its description and its badge", () => {
    renderChoice();

    expect(radio("This computer")).toHaveAccessibleDescription(
      "Keeps the files on this disk. Recommended",
    );
    expect(radio("A server")).toHaveAccessibleDescription(
      "Keeps the files on a server you run.",
    );
  });

  it("exposes the selected card as the checked radio", () => {
    renderChoice({ initial: "server" });

    expect(radio("A server")).toBeChecked();
    expect(radio("This computer")).not.toBeChecked();
    expect(radio("The cloud")).not.toBeChecked();
  });

  it("selects a card clicked anywhere on it", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChoice();

    await user.click(screen.getByText("Keeps the files with a provider."));

    expect(onChange).toHaveBeenCalledExactlyOnceWith("cloud");
    expect(radio("The cloud")).toBeChecked();
    expect(radio("This computer")).not.toBeChecked();
  });

  it("reports nothing when the selected card is clicked again", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChoice();

    await user.click(screen.getByText("This computer"));

    expect(onChange).not.toHaveBeenCalled();
    expect(radio("This computer")).toBeChecked();
  });

  // Two cards: user-event 14.5 walks a longer group past the next radio.
  it("moves and selects with the arrow keys, wrapping at the ends", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChoice({ options: OPTIONS.slice(0, 2) });
    act(() => radio("This computer").focus());

    await user.keyboard("{ArrowRight}");
    expect(radio("A server")).toBeChecked();
    expect(radio("A server")).toHaveFocus();

    await user.keyboard("{ArrowRight}");
    expect(radio("This computer")).toBeChecked();
    expect(radio("This computer")).toHaveFocus();

    await user.keyboard("{ArrowLeft}");
    expect(radio("A server")).toBeChecked();
    expect(onChange.mock.calls).toEqual([["server"], ["local"], ["server"]]);
  });

  it("keeps a disabled choice as it is", async () => {
    const user = userEvent.setup();
    const { onChange } = renderChoice({ disabled: true });

    for (const name of ["This computer", "A server", "The cloud"]) {
      expect(radio(name)).toBeDisabled();
    }
    await user.click(radio("The cloud"));

    expect(onChange).not.toHaveBeenCalled();
    expect(radio("This computer")).toBeChecked();
  });
});
