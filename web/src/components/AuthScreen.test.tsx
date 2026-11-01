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
import { Info } from "lucide-react";
import { describe, expect, it } from "vitest";

import { AuthScreen } from "@/components/AuthScreen";
import { ThemeProvider } from "@/theme/ThemeProvider";

function renderScreen(wide?: boolean) {
  return render(
    <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
      <AuthScreen
        title="Log in to the panel"
        note="Agents use their tokens."
        noteIcon={Info}
        wide={wide}
      >
        <button type="button">Log in</button>
      </AuthScreen>
    </ThemeProvider>,
  );
}

function card() {
  return screen.getByRole("heading", { level: 1 }).parentElement?.parentElement;
}

describe("AuthScreen", () => {
  it("shows the standalone bar above the card's title, content and note", () => {
    renderScreen();

    const banner = screen.getByRole("banner");
    const title = screen.getByRole("heading", {
      level: 1,
      name: "Log in to the panel",
    });
    const content = screen.getByRole("button", { name: "Log in" });
    const note = screen.getByText("Agents use their tokens.");
    expect(within(banner).getByText("Pit Officer")).toBeInTheDocument();
    expect(screen.getAllByText("Pit Officer")).toHaveLength(1);
    expect(
      within(banner).getByRole("button", { name: "Change language" }),
    ).toBeInTheDocument();
    expect(
      within(banner).getByRole("button", { name: /^Theme: / }),
    ).toBeInTheDocument();
    expect(card()).not.toContainElement(banner);
    const main = screen.getByRole("main");
    expect(main).toContainElement(card() ?? null);
    expect(main).not.toContainElement(banner);
    expect(card()).toContainElement(title);
    expect(card()).toContainElement(content);
    expect(card()).toContainElement(note);
    expect(banner.compareDocumentPosition(title)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    expect(title.compareDocumentPosition(content)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    expect(content.compareDocumentPosition(note)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
  });

  it.each([
    ["a narrow card by default", undefined, "max-w-md"],
    ["a wide card when asked", true, "max-w-xl"],
  ])("lays the screen out on %s", (_case, wide, width) => {
    renderScreen(wide);

    expect(card()).toHaveClass("w-full", width);
  });
});
