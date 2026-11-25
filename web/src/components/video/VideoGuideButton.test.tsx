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
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { VideoGuideButton } from "@/components/video/VideoGuideButton";
import i18n from "@/i18n";

describe("VideoGuideButton", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  afterEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("is one glyph-only link that names its guide and opens it safely", () => {
    render(<VideoGuideButton video="stop" />);

    const link = screen.getByRole("link", {
      name: "Video guide on YouTube: blocking an account",
    });
    expect(link).toHaveAttribute("title", link.getAttribute("aria-label"));
    expect(link).toHaveAttribute(
      "href",
      "https://www.youtube.com/watch?v=ateQ-i5k-o0",
    );
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link).toHaveTextContent("");
    expect(link.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  it("takes the height of small controls by default, of a field or of a text link on request", () => {
    const { rerender } = render(<VideoGuideButton video="groups" />);
    expect(screen.getByRole("link")).toHaveClass(
      "h-[var(--dens-button-sm-h)]",
      "w-[var(--dens-button-sm-h)]",
    );

    rerender(<VideoGuideButton video="groups" size="field" />);
    expect(screen.getByRole("link")).toHaveClass(
      "h-[var(--dens-field-h)]",
      "w-[var(--dens-field-h)]",
    );
    expect(screen.getByRole("link")).not.toHaveClass(
      "h-[var(--dens-button-sm-h)]",
    );

    rerender(<VideoGuideButton video="groups" size="link" />);
    expect(screen.getByRole("link")).toHaveClass("h-4", "w-6");
    expect(screen.getByRole("link")).not.toHaveClass(
      "h-[var(--dens-button-sm-h)]",
    );
  });

  it("opens the guide in the panel language", async () => {
    await i18n.changeLanguage("ru");
    render(<VideoGuideButton video="groups" />);

    expect(
      screen.getByRole("link", {
        name: "Видеоинструкция на YouTube: группы счетов",
      }),
    ).toHaveAttribute("href", "https://www.youtube.com/watch?v=p885xVaJQkU");
  });
});
