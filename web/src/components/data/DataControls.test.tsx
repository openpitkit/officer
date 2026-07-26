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
// Please see https://openpit.dev and the OWNERS file for details.

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter, useLocation, useSearchParams } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  ActionButton,
  CloneButton,
  CopyIdButton,
  FilterBar,
  FilterChip,
  FilterByButton,
  AutocompleteFilterField,
  OnlineFilterField,
  OPERATORS,
  NumberRangeFilter,
  ShareLinkButton,
  SortableHeader,
  TimeRangeFilter,
  type SortDirection,
} from "@/components/data";

function UrlFilterHarness() {
  const [params, setParams] = useSearchParams();
  const account = params.get("account") ?? "";
  const group = params.get("group") ?? "";
  const location = useLocation();

  function update(name: string, value: string) {
    const next = new URLSearchParams(params);
    if (value.trim().length === 0) {
      next.delete(name);
    } else {
      next.set(name, value);
    }
    setParams(next, { replace: true });
  }

  return (
    <>
      <FilterBar
        active={account !== "" || group !== ""}
        onClearActive={() => setParams({})}
        chips={
          <>
            {account !== "" && (
              <FilterChip
                field="account"
                value={account}
                onRemove={() => update("account", "")}
              />
            )}
            {group !== "" && (
              <FilterChip
                field="group"
                value={group}
                onRemove={() => update("group", "")}
              />
            )}
            {(account !== "" || group !== "") && (
              <button type="button" onClick={() => setParams({})}>
                Clear all
              </button>
            )}
          </>
        }
      >
        <OnlineFilterField
          label="Account"
          value={account}
          onChange={(value) => update("account", value)}
        />
        <OnlineFilterField
          label="Group"
          value={group}
          onChange={(value) => update("group", value)}
        />
      </FilterBar>
      <output aria-label="query">{location.search}</output>
    </>
  );
}

describe("data controls", () => {
  beforeEach(() => {
    document.documentElement.removeAttribute("data-density");
  });

  it("exposes the canonical operator sets", () => {
    expect(OPERATORS.text.map((operator) => operator.value)).toEqual([
      "contains",
      "starts_with",
      "ends_with",
      "exact",
    ]);
    expect(OPERATORS.number.map((operator) => operator.value)).toEqual([
      "eq",
      "neq",
      "gt",
      "lt",
      "gte",
      "lte",
      "between",
    ]);
    expect(OPERATORS.time.map((operator) => operator.value)).toEqual([
      "after",
      "before",
      "between",
    ]);
  });

  it("rehydrates filters from URL params and removes chips or all filters", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/accounts?account=ACC-1&group=G-2"]}>
        <UrlFilterHarness />
      </MemoryRouter>,
    );

    expect(screen.getByLabelText("query")).toHaveTextContent(
      "?account=ACC-1&group=G-2",
    );
    expect(screen.getByDisplayValue("ACC-1")).toBeInTheDocument();
    expect(screen.getByDisplayValue("G-2")).toBeInTheDocument();
    expect(screen.getByText("Active filters")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "Remove filter" })[0]);
    expect(screen.getByLabelText("query")).toHaveTextContent("?group=G-2");

    await user.clear(screen.getByLabelText("Group"));
    await user.type(screen.getByLabelText("Group"), "G-3");
    expect(screen.getByLabelText("query")).toHaveTextContent("?group=G-3");

    await user.click(screen.getByRole("button", { name: "Clear all filters" }));
    expect(screen.getByLabelText("query")).toHaveTextContent("");
    expect(screen.queryByText("Active filters")).not.toBeInTheDocument();
  });

  it("renders account-global filter toggles with pressed and disabled states", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();

    const { rerender } = render(
      <OnlineFilterField
        label="Account"
        value=""
        globalToggle={{
          disabled: true,
          active: false,
          onToggle,
          activeLabel: "Stop using this account globally",
          inactiveLabel: "Use this account globally",
          disabledLabel: "Enter an account first",
        }}
      />,
    );

    expect(
      screen.getByRole("button", { name: "Enter an account first" }),
    ).toBeDisabled();

    rerender(
      <AutocompleteFilterField
        ariaLabel="Account"
        value="ACC-1"
        globalToggle={{
          disabled: false,
          active: true,
          onToggle,
          activeLabel: "Stop using this account globally",
          inactiveLabel: "Use this account globally",
          disabledLabel: "Enter an account first",
        }}
      />,
    );

    const toggle = screen.getByRole("button", {
      name: "Stop using this account globally",
    });
    expect(toggle).toHaveAttribute("aria-pressed", "true");

    await user.click(toggle);
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it("copies raw IDs and deep links", async () => {
    const onIdCopy = vi.fn();
    const onLinkCopy = vi.fn();
    const user = userEvent.setup();

    render(
      <>
        <CopyIdButton
          value="ACC-001"
          title="Copy account ID"
          onCopy={onIdCopy}
        />
        <ShareLinkButton
          href="https://officer.example/accounts?account=ACC-001"
          title="Copy account link"
          onCopy={onLinkCopy}
        />
      </>,
    );

    await user.click(screen.getByRole("button", { name: "Copy account ID" }));
    await user.click(screen.getByRole("button", { name: "Copy account link" }));

    await waitFor(() => expect(onIdCopy).toHaveBeenCalledWith("ACC-001"));
    expect(onLinkCopy).toHaveBeenCalledWith(
      "https://officer.example/accounts?account=ACC-001",
    );
  });

  it("opens share and filter actions in a new tab on modifier click", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const onCopy = vi.fn();
    const onFilter = vi.fn();

    render(
      <>
        <ShareLinkButton
          href="https://officer.example/accounts?account=ACC-001"
          title="Copy account link"
          onCopy={onCopy}
        />
        <FilterByButton
          href="https://officer.example/orders?account=ACC-001"
          title="Filter by account"
          onClick={onFilter}
        />
      </>,
    );

    const share = screen.getByRole("button", { name: "Copy account link" });
    expect(share).toHaveAttribute("title", expect.stringContaining("Cmd-click"));
    fireEvent.click(share, { ctrlKey: true });
    fireEvent.click(screen.getByRole("button", { name: "Filter by account" }), {
      metaKey: true,
    });

    expect(open).toHaveBeenNthCalledWith(
      1,
      "https://officer.example/accounts?account=ACC-001",
      "_blank",
      "noopener,noreferrer",
    );
    expect(open).toHaveBeenNthCalledWith(
      2,
      "https://officer.example/orders?account=ACC-001",
      "_blank",
      "noopener,noreferrer",
    );
    expect(onCopy).not.toHaveBeenCalled();
    expect(onFilter).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("keeps clone and copy on distinct glyphs", () => {
    const { container } = render(
      <>
        <CopyIdButton value="ORD-1" title="Copy order ID" />
        <CloneButton title="Clone order" />
      </>,
    );
    const [copySvg, cloneSvg] = Array.from(container.querySelectorAll("svg"));

    expect(copySvg.innerHTML).not.toEqual(cloneSvg.innerHTML);
    expect(screen.getByRole("button", { name: "Copy order ID" })).toHaveAttribute(
      "title",
      "Copy order ID",
    );
    expect(screen.getByRole("button", { name: "Clone order" })).toHaveAttribute(
      "title",
      "Clone order",
    );
  });

  it("cycles sortable headers by click and keyboard", async () => {
    const user = userEvent.setup();

    function Harness() {
      const [direction, setDirection] = useState<SortDirection>("none");
      return (
        <SortableHeader
          label="Account"
          field="account"
          direction={direction}
          onSort={(_field, next) => setDirection(next)}
        />
      );
    }

    render(<Harness />);
    const header = screen.getByRole("button", { name: "Sort by Account" });
    expect(header).toHaveAttribute("aria-pressed", "false");

    await user.click(header);
    expect(header).toHaveAttribute("aria-pressed", "true");

    await user.keyboard("{Enter}");
    expect(header).toHaveAttribute("aria-pressed", "true");

    await user.keyboard(" ");
    expect(header).toHaveAttribute("aria-pressed", "false");
  });

  it("exposes sortable header direction on the table header cell", async () => {
    render(
      <table>
        <thead>
          <tr>
            <th>
              <SortableHeader
                label="Account"
                field="account"
                direction="desc"
              />
            </th>
          </tr>
        </thead>
      </table>,
    );

    await waitFor(() => {
      expect(screen.getByRole("columnheader")).toHaveAttribute(
        "aria-sort",
        "descending",
      );
    });
  });

  it("marks inverted number ranges invalid", async () => {
    render(<NumberRangeFilter min="10" max="5" />);

    const inputs = screen.getAllByRole("textbox") as HTMLInputElement[];
    await waitFor(() => {
      expect(inputs[0]).toBeInvalid();
      expect(inputs[0].validationMessage).toBe(
        "Enter a range with the lower bound at or below the upper bound.",
      );
      expect(inputs[1]).toBeInvalid();
    });
  });

  it("marks inverted time ranges invalid", async () => {
    const { container } = render(
      <TimeRangeFilter
        from="2026-07-03T12:00"
        to="2026-07-03T10:00"
        showPresets={false}
      />,
    );

    const inputs = Array.from(
      container.querySelectorAll("input"),
    ) as HTMLInputElement[];
    await waitFor(() => {
      expect(inputs[0]).toBeInvalid();
      expect(inputs[0].validationMessage).toBe(
        "Enter a range with the lower bound at or below the upper bound.",
      );
      expect(inputs[1]).toBeInvalid();
    });
  });

  it("keeps accessible names and density-safe hit sizes", () => {
    document.documentElement.dataset.density = "terminal";
    render(
      <>
        <ActionButton icon="filter-by" title="Filter by account" />
        <ActionButton icon="view" title="Open account" size={44} />
      </>,
    );

    expect(
      screen.getByRole("button", { name: "Filter by account" }),
    ).toHaveStyle({
      width: "min(30px, var(--dens-row-action-size, 30px))",
      height: "min(30px, var(--dens-row-action-size, 30px))",
    });
    expect(screen.getByRole("button", { name: "Open account" })).toHaveStyle({
      width: "min(44px, var(--dens-row-action-size, 44px))",
      height: "min(44px, var(--dens-row-action-size, 44px))",
    });
  });
});
