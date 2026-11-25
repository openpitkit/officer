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

import { VideoGuideNotice } from "@/components/video/VideoGuideNotice";
import i18n from "@/i18n";

describe("VideoGuideNotice", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("renders the title and one VideoLink per requested guide", () => {
    render(<VideoGuideNotice videos={["size", "rate", "pnl"]} />);

    expect(
      screen.getByText("Learn how this section works"),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(3);
    for (const [label, id] of [
      ["Video: order size limit", "aXXWLLw3Jm0"],
      ["Video: order rate limit", "BpQoS5UUEsU"],
      ["Video: stopping on P&L", "kkcir-NpZBA"],
    ]) {
      expect(screen.getByRole("link", { name: label })).toHaveAttribute(
        "href",
        `https://www.youtube.com/watch?v=${id}`,
      );
    }
  });

  it("says under each guide what it shows", () => {
    render(<VideoGuideNotice videos={["whyBalances", "balances"]} />);

    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("Video: why Officer needs balances");
    expect(items[0]).toHaveTextContent(
      "How balances cap the funds an agent can trade with.",
    );
    expect(items[1]).toHaveTextContent("Video: balances and cash movements");
    expect(items[1]).toHaveTextContent(
      "How to enter balances and record deposits, withdrawals and fees.",
    );
  });
});
