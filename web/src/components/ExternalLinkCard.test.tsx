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
import { describe, expect, it } from "vitest";

import { ExternalLinkCard } from "@/components/ExternalLinkCard";

describe("ExternalLinkCard", () => {
  it("renders a supplied icon before the label", () => {
    render(
      <ExternalLinkCard
        href="https://openpit.dev?officer"
        label="OpenPit"
        icon={<svg aria-hidden="true" />}
      />,
    );

    const link = screen.getByRole("link", { name: "OpenPit" });
    const icon = link.querySelector("svg[aria-hidden='true']");
    expect(icon).not.toBeNull();
    expect(icon?.nextSibling?.textContent).toBe("OpenPit");
    expect(link).toHaveAccessibleName("OpenPit");
    expect(link).toHaveAttribute("href", "https://openpit.dev?officer");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("opens its page in a new tab without handing over the panel", () => {
    render(
      <ExternalLinkCard href="https://openpit.dev?officer" label="OpenPit" />,
    );

    const link = screen.getByRole("link", { name: "OpenPit" });
    expect(link).toHaveAttribute("href", "https://openpit.dev?officer");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link.outerHTML).toMatchInlineSnapshot(
      `"<a href="https://openpit.dev?officer" target="_blank" rel="noopener noreferrer" class="inline-flex items-center justify-between gap-3 rounded-card border border-border bg-surface-2 px-3 py-2 text-xs text-muted-lt transition-colors hover:border-border-hover hover:bg-card-hover-bg hover:text-accent"><span>OpenPit</span><svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-external-link h-3.5 w-3.5 shrink-0"><path d="M15 3h6v6"></path><path d="M10 14 21 3"></path><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path></svg></a>"`,
    );
  });
});
