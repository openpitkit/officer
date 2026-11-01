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
import { beforeEach, describe, expect, it } from "vitest";

import { VideoLink } from "@/components/video/VideoLink";
import i18n from "@/i18n";

describe("VideoLink", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("preserves the label as its accessible name and opens the same video safely", () => {
    render(<VideoLink video="intro" />);

    const link = screen.getByRole("link");
    expect(link).toHaveAccessibleName("Video: what Pit Officer is");
    expect(link).toHaveAttribute(
      "href",
      "https://www.youtube.com/watch?v=v34IdYhH5so",
    );
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("renders the official coloured YouTube glyph before the label", () => {
    render(<VideoLink video="intro" />);

    const link = screen.getByRole("link");
    const glyph = link.querySelector("svg[viewBox='0 0 28.57 20']");
    expect(glyph).not.toBeNull();
    expect(glyph).toHaveAttribute("aria-hidden", "true");
    expect(glyph?.querySelectorAll("path")).toHaveLength(2);
    expect(glyph?.querySelector("path:nth-child(1)")).toHaveAttribute(
      "fill",
      "#FF0000",
    );
    expect(glyph?.querySelector("path:nth-child(2)")).toHaveAttribute(
      "fill",
      "white",
    );
    expect(glyph?.nextSibling?.textContent).toBe("Video: what Pit Officer is");
  });
});
