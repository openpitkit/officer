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
import { AlertTriangle, Info } from "lucide-react";
import { describe, expect, it } from "vitest";

import { Notice } from "@/components/Notice";

describe("Notice", () => {
  it("shows the title and every detail child", () => {
    render(
      <Notice tone="warn" icon={Info} title="Restart required">
        <p>Changes apply after a restart.</p>
        <a href="https://example.test/guide">Read the guide</a>
      </Notice>,
    );
    expect(screen.getByText("Restart required")).toBeInTheDocument();
    expect(
      screen.getByText("Changes apply after a restart."),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Read the guide" }),
    ).toHaveAttribute("href", "https://example.test/guide");
  });

  it("renders the action", () => {
    render(
      <Notice
        tone="warn"
        icon={Info}
        title="Restart required"
        action={<button type="button">Restart</button>}
      >
        <p>Changes apply after a restart.</p>
      </Notice>,
    );
    expect(screen.getByRole("button", { name: "Restart" })).toBeInTheDocument();
  });

  it("has neither an action nor a role unless given", () => {
    const { container } = render(
      <Notice tone="danger" icon={AlertTriangle} title="Data is unsealed">
        <p>Set a master key.</p>
      </Notice>,
    );
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(container.querySelector("[role]")).toBeNull();
  });

  it("announces itself as an alert when asked", () => {
    render(
      <Notice
        tone="danger"
        icon={AlertTriangle}
        title="Data is unsealed"
        role="alert"
      >
        <p>Set a master key.</p>
      </Notice>,
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Data is unsealed");
    expect(alert).toHaveTextContent("Set a master key.");
  });

  it.each([
    {
      tone: "warn",
      box: ["border-[var(--warn)]", "bg-[var(--warn-dim)]"],
      icon: "text-[var(--warn)]",
      title: "text-text",
      detail: "text-muted-lt",
    },
    {
      tone: "danger",
      box: ["border-[var(--danger)]", "bg-[var(--danger-dim)]"],
      icon: "text-[var(--danger)]",
      title: "text-[var(--danger)]",
      detail: "text-text",
    },
  ] as const)(
    "paints the $tone tone on the box, icon, title and detail",
    ({ tone, box, icon, title, detail }) => {
      const { container } = render(
        <Notice tone={tone} icon={Info} title="Title">
          <p>Detail</p>
        </Notice>,
      );
      const root = container.firstElementChild;
      expect(root).toHaveClass(...box);
      expect(root?.querySelector("svg")).toHaveClass(icon);
      expect(screen.getByText("Title")).toHaveClass(title);
      expect(screen.getByText("Detail").parentElement).toHaveClass(detail);
    },
  );
});
