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

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

describe("Tabs", () => {
  it("renders no selected tab or panel for an empty controlled value", () => {
    render(
      <Tabs value="">
        <TabsList>
          <TabsTrigger value="first">First</TabsTrigger>
          <TabsTrigger value="second">Second</TabsTrigger>
        </TabsList>
        <TabsContent value="first">First panel</TabsContent>
        <TabsContent value="second">Second panel</TabsContent>
      </Tabs>,
    );

    for (const tab of screen.getAllByRole("tab")) {
      expect(tab).toHaveAttribute("aria-selected", "false");
    }
    expect(screen.queryByRole("tabpanel")).not.toBeInTheDocument();
    expect(screen.queryByText("First panel")).not.toBeInTheDocument();
    expect(screen.queryByText("Second panel")).not.toBeInTheDocument();
  });

  it("reports the clicked trigger value", async () => {
    const onValueChange = vi.fn();
    const user = userEvent.setup();

    render(
      <Tabs value="" onValueChange={onValueChange}>
        <TabsList>
          <TabsTrigger value="first">First</TabsTrigger>
          <TabsTrigger value="second">Second</TabsTrigger>
        </TabsList>
      </Tabs>,
    );

    await user.click(screen.getByRole("tab", { name: "Second" }));

    expect(onValueChange).toHaveBeenCalledWith("second");
  });

  it("selects the controlled tab and renders only its panel", () => {
    render(
      <Tabs value="second">
        <TabsList>
          <TabsTrigger value="first">First</TabsTrigger>
          <TabsTrigger value="second">Second</TabsTrigger>
        </TabsList>
        <TabsContent value="first">First panel</TabsContent>
        <TabsContent value="second">Second panel</TabsContent>
      </Tabs>,
    );

    expect(screen.getByRole("tab", { name: "First" })).toHaveAttribute(
      "aria-selected",
      "false",
    );
    expect(screen.getByRole("tab", { name: "Second" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getAllByRole("tabpanel")).toHaveLength(1);
    expect(screen.getByRole("tabpanel", { name: "Second" })).toHaveTextContent(
      "Second panel",
    );
    expect(screen.queryByText("First panel")).not.toBeInTheDocument();
  });

  it("moves focus and selects tabs automatically by default", async () => {
    const user = userEvent.setup();

    render(
      <Tabs defaultValue="first">
        <TabsList>
          <TabsTrigger value="first">First</TabsTrigger>
          <TabsTrigger value="second">Second</TabsTrigger>
        </TabsList>
        <TabsContent value="first">First panel</TabsContent>
        <TabsContent value="second">Second panel</TabsContent>
      </Tabs>,
    );

    await user.tab();
    expect(screen.getByRole("tab", { name: "First" })).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    await waitFor(() => {
      expect(screen.getByRole("tab", { name: "Second" })).toHaveFocus();
      expect(screen.getByRole("tab", { name: "Second" })).toHaveAttribute(
        "aria-selected",
        "true",
      );
    });
    expect(screen.getByRole("tabpanel", { name: "Second" })).toHaveTextContent(
      "Second panel",
    );

    await user.keyboard("{Home}");
    await waitFor(() => {
      expect(screen.getByRole("tab", { name: "First" })).toHaveFocus();
      expect(screen.getByRole("tab", { name: "First" })).toHaveAttribute(
        "aria-selected",
        "true",
      );
    });
    expect(screen.getByRole("tabpanel", { name: "First" })).toHaveTextContent(
      "First panel",
    );
  });

  it.each(["{Enter}", " "])(
    "keeps keyboard focus separate from selection in manual mode until %s",
    async (key) => {
      const onValueChange = vi.fn();
      const user = userEvent.setup();

      render(
        <Tabs
          value="first"
          activationMode="manual"
          onValueChange={onValueChange}
        >
          <TabsList>
            <TabsTrigger value="first">First</TabsTrigger>
            <TabsTrigger value="second">Second</TabsTrigger>
          </TabsList>
        </Tabs>,
      );

      await user.tab();
      expect(screen.getByRole("tab", { name: "First" })).toHaveFocus();
      await user.keyboard("{ArrowRight}");
      await waitFor(() =>
        expect(screen.getByRole("tab", { name: "Second" })).toHaveFocus(),
      );
      expect(onValueChange).not.toHaveBeenCalled();

      await user.keyboard(key);

      expect(onValueChange).toHaveBeenCalledTimes(1);
      expect(onValueChange).toHaveBeenCalledWith("second");
    },
  );
});
