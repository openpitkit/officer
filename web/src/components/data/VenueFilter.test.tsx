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

import {
  normalizeVenueFilterValue,
  VenueFilter,
  venueConnectionFilterValue,
  venueConnectionLabel,
  venueFilterConnectionId,
  venueFilterValueLabel,
} from "@/components/data";

const labels = {
  label: "Trading connection",
  allLabel: "All connections",
  anyLabel: "Linked",
  noneLabel: "Unlinked",
};
const connections = [
  { id: "desk:primary", label: "Primary desk" },
  { id: "desk-secondary", label: "Secondary desk" },
];

describe("VenueFilter", () => {
  it("renders the options in display order with caller labels", async () => {
    const user = userEvent.setup();
    render(
      <VenueFilter
        {...labels}
        connections={connections}
        value=""
        onChange={vi.fn()}
      />,
    );

    const select = screen.getByRole("combobox", { name: labels.label });
    expect(select).toHaveTextContent(labels.allLabel);
    await user.click(select);
    expect(
      screen.getAllByRole("option").map((option) => option.textContent),
    ).toEqual([
      "All connections",
      "Linked",
      "Unlinked",
      "Primary desk",
      "Secondary desk",
    ]);
  });

  it.each(connections)(
    "shows the caller label for listed connection $id without a raw-ID option",
    async (connection) => {
      const user = userEvent.setup();
      render(
        <VenueFilter
          {...labels}
          connections={connections}
          value={venueConnectionFilterValue(connection.id)}
          onChange={vi.fn()}
        />,
      );

      const select = screen.getByRole("combobox", { name: labels.label });
      expect(select.textContent).toBe(connection.label);
      await user.click(select);
      expect(
        screen.getAllByRole("option").map((option) => option.textContent),
      ).toEqual([
        labels.allLabel,
        labels.anyLabel,
        labels.noneLabel,
        ...connections.map((item) => item.label),
      ]);
    },
  );

  it.each([
    ["none", labels.noneLabel],
    ["all", labels.allLabel],
  ])("shows the caller label for %s", (value, expectedLabel) => {
    render(
      <VenueFilter
        {...labels}
        connections={connections}
        value={value}
        onChange={vi.fn()}
      />,
    );

    expect(
      screen.getByRole("combobox", { name: labels.label }).textContent,
    ).toBe(expectedLabel);
  });

  it.each([
    ["All connections", ""],
    ["Linked", "any"],
    ["Unlinked", "none"],
    ["Primary desk", "connection:desk:primary"],
    ["Secondary desk", "connection:desk-secondary"],
  ])("emits the REST value for %s", async (optionLabel, expectedValue) => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <VenueFilter
        {...labels}
        connections={connections}
        value={expectedValue === "" ? "any" : ""}
        onChange={onChange}
      />,
    );

    await user.click(screen.getByRole("combobox", { name: labels.label }));
    await user.click(screen.getByRole("option", { name: optionLabel }));
    expect(onChange).toHaveBeenCalledExactlyOnceWith(expectedValue);
  });

  it("keeps an unknown connection selected under its raw ID", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <VenueFilter
        {...labels}
        connections={connections}
        value="connection:deleted:desk"
        onChange={onChange}
        width={240}
      />,
    );

    const select = screen.getByRole("combobox", { name: labels.label });
    expect(select).toHaveTextContent("deleted:desk");
    expect(select).toHaveStyle({ width: "240px" });
    expect(onChange).not.toHaveBeenCalled();
    await user.click(select);
    expect(
      screen.getByRole("option", { name: "deleted:desk" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(screen.getAllByRole("option").at(-1)).toHaveTextContent(
      "deleted:desk",
    );
  });
});

describe("venue filter encoding", () => {
  it("normalizes all to the unrestricted value", () => {
    expect(normalizeVenueFilterValue("all")).toBe("");
  });

  it.each(["desk-primary", "desk:primary:live"])("round-trips %s", (id) => {
    expect(venueConnectionFilterValue(id)).toBe(`connection:${id}`);
    expect(venueFilterConnectionId(`connection:${id}`)).toBe(id);
  });

  it.each(["", "any", "none", "all", "desk-primary"])(
    "does not decode %s as a connection",
    (value) => {
      expect(venueFilterConnectionId(value)).toBeUndefined();
    },
  );
});

describe("venue filter labels", () => {
  const venueLabels = new Map([["desk:primary", "Alpaca · Paper · Primary"]]);
  const options = {
    labels: venueLabels,
    anyLabel: "Any venue",
    noneLabel: "No venue",
  };

  it.each([
    ["any", "Any venue"],
    ["none", "No venue"],
    ["connection:desk:primary", "Alpaca · Paper · Primary"],
    ["connection:deleted:desk", "deleted:desk"],
    ["bogus", "bogus"],
  ])("labels %s as %s", (value, label) => {
    expect(venueFilterValueLabel(value, options)).toBe(label);
  });

  it("labels a connection by its raw ID without known labels", () => {
    expect(venueConnectionLabel(undefined, "desk:primary")).toBe(
      "desk:primary",
    );
    expect(
      venueFilterValueLabel("connection:desk:primary", {
        ...options,
        labels: undefined,
      }),
    ).toBe("desk:primary");
  });
});
