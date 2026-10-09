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
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DeleteInstrumentDialog,
  InstrumentDraftRow,
  InstrumentsTable,
  SymbolSearchPanel,
  VerifyResult,
  type SymbolVerificationOutcome,
} from "@/components/venues";
import i18n from "@/i18n";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

const matches = [
  { symbol: "AAPL", name: "Apple", exchange: "NASDAQ" },
  { symbol: "MSFT", name: "", exchange: "" },
];
const searchProps = {
  id: "symbol-search",
  label: "Find a contract",
  placeholder: "Enter a symbol",
  noResultsText: "No contracts found",
  busy: false,
  searching: false,
  searched: true,
  query: "AA",
  results: matches,
  filterQuery: "",
  getKey: (match: (typeof matches)[number]) => match.symbol,
  getLabel: (match: (typeof matches)[number]) => match.symbol,
  getMetadata: (match: (typeof matches)[number]) => match.exchange,
  getName: (match: (typeof matches)[number]) => match.name,
  getSearchText: (match: (typeof matches)[number]) =>
    [match.symbol, match.name, match.exchange].join(" ").toLowerCase(),
};

describe("SymbolSearchPanel", () => {
  it("renders a muted notice inside the panel and clears query and filter through callbacks", async () => {
    const user = userEvent.setup();
    const onQueryChange = vi.fn();
    const onFilterChange = vi.fn();
    const { rerender } = render(
      <SymbolSearchPanel
        {...searchProps}
        filterQuery="Apple"
        notice="Search unavailable"
        onQueryChange={onQueryChange}
        onFilterChange={onFilterChange}
        onSearch={vi.fn()}
        onPick={vi.fn()}
      />,
    );
    const notice = screen.getByText("Search unavailable");
    expect(notice).toHaveClass("text-muted");
    expect(notice.parentElement).toContainElement(
      screen.getByLabelText("Find a contract"),
    );
    const clears = screen.getAllByRole("button", { name: "Clear field" });
    await user.click(clears[0]);
    expect(onQueryChange).toHaveBeenCalledExactlyOnceWith("");
    await user.click(clears[1]);
    expect(onFilterChange).toHaveBeenCalledExactlyOnceWith("");
    rerender(
      <SymbolSearchPanel
        {...searchProps}
        onQueryChange={onQueryChange}
        onFilterChange={onFilterChange}
        onSearch={vi.fn()}
        onPick={vi.fn()}
      />,
    );
    expect(screen.queryByText("Search unavailable")).not.toBeInTheDocument();
  });
  it("renders warnings in the warning colour outside the truncated text", () => {
    render(
      <SymbolSearchPanel
        {...searchProps}
        onQueryChange={vi.fn()}
        onFilterChange={vi.fn()}
        onSearch={vi.fn()}
        onPick={vi.fn()}
        getWarning={(match) =>
          match.symbol === "AAPL" ? "Not tradable" : undefined
        }
      />,
    );
    const warning = within(
      screen.getByRole("button", { name: /AAPL/ }),
    ).getByText("Not tradable");
    expect(warning).toHaveClass("text-[var(--warn)]", "shrink-0");
    expect(warning.closest(".truncate")).toBeNull();
    expect(
      within(screen.getByRole("button", { name: /MSFT/ })).queryByText(
        "Not tradable",
      ),
    ).not.toBeInTheDocument();
  });

  it("searches on Enter and the button, filters and picks the original match", async () => {
    const user = userEvent.setup();
    const onSearch = vi.fn();
    const onPick = vi.fn();
    function Search() {
      const [query, setQuery] = useState(searchProps.query);
      const [filterQuery, setFilterQuery] = useState("");
      return (
        <SymbolSearchPanel
          {...searchProps}
          query={query}
          filterQuery={filterQuery}
          onQueryChange={setQuery}
          onFilterChange={setFilterQuery}
          onSearch={onSearch}
          onPick={onPick}
        />
      );
    }
    render(<Search />);
    const input = screen.getByLabelText("Find a contract");
    await user.type(input, "PL{Enter}");
    expect(input).toHaveValue("AAPL");
    expect(onSearch).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    expect(onSearch).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("list")).toHaveClass("max-h-52", "overflow-y-auto");
    expect(screen.getByText("Apple")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "MSFT" })).toBeInTheDocument();
    const filter = screen.getByLabelText("Filter resolved symbols");
    await user.type(filter, " nasdaq ");
    expect(
      screen.queryByRole("button", { name: "MSFT" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /AAPL/ }));
    expect(onPick).toHaveBeenCalledWith(matches[0]);
    await user.clear(filter);
    await user.type(filter, "missing");
    expect(screen.getByText("No results match the filter")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  it("keeps empty, searching and disabled states separate", () => {
    const props = {
      ...searchProps,
      onQueryChange: vi.fn(),
      onFilterChange: vi.fn(),
      onSearch: vi.fn(),
      onPick: vi.fn(),
      results: [],
    };
    const { rerender } = render(<SymbolSearchPanel {...props} />);
    expect(screen.getByText("No contracts found")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
    expect(
      screen.queryByLabelText("Filter resolved symbols"),
    ).not.toBeInTheDocument();
    rerender(<SymbolSearchPanel {...props} searched={false} query=" " />);
    expect(screen.queryByText("No contracts found")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Resolve" })).toBeDisabled();
    rerender(<SymbolSearchPanel {...props} searching />);
    expect(screen.getByText("Resolving…")).toBeInTheDocument();
    expect(screen.queryByText("No contracts found")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Find a contract")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Resolve" })).toBeDisabled();
    rerender(<SymbolSearchPanel {...props} results={matches} busy />);
    expect(screen.getByLabelText("Filter resolved symbols")).toBeDisabled();
    expect(screen.getByRole("button", { name: /AAPL/ })).toBeDisabled();
  });
});

describe("VerifyResult", () => {
  it("renders every verdict with its tone and context-specific unavailability", () => {
    const { container, rerender } = render(<VerifyResult result={null} />);
    expect(container).toBeEmptyDOMElement();
    const verdict = (
      result: SymbolVerificationOutcome,
      unavailableLabel?: string,
    ) =>
      rerender(
        <VerifyResult result={result} unavailableLabel={unavailableLabel} />,
      );
    verdict({ supported: false, exists: false });
    expect(container).toBeEmptyDOMElement();
    rerender(
      <VerifyResult
        result={{ supported: false, exists: false }}
        unsupportedLabel="Verification unavailable"
      />,
    );
    expect(screen.getByText("Verification unavailable")).toHaveClass(
      "text-muted",
    );
    verdict({ failed: true });
    expect(screen.getByText("Verification failed")).toHaveClass(
      "text-[var(--danger)]",
    );
    verdict({ failed: true, message: "Venue offline" });
    expect(container.textContent).toBe("Verification failed");
    verdict({ supported: true, exists: true, details: "NASDAQ" });
    expect(screen.getByText("Symbol exists")).toHaveClass("text-[var(--ok)]");
    expect(screen.getByText("(NASDAQ)")).toBeInTheDocument();
    verdict({ supported: true, exists: true, tradable: true });
    expect(screen.getByText("Symbol exists")).toBeInTheDocument();
    verdict({
      supported: true,
      exists: true,
      tradable: false,
      details: "Restricted",
    });
    expect(screen.getByText("Symbol exists but is not tradable")).toHaveClass(
      "text-[var(--warn)]",
    );
    expect(screen.queryByText("Symbol exists")).not.toBeInTheDocument();
    expect(screen.getByText("(Restricted)")).toBeInTheDocument();
    verdict({ supported: true, exists: false, suggestion: "AAPL" });
    expect(screen.getByText("Symbol not found")).toBeInTheDocument();
    expect(screen.getByText("Did you mean AAPL?")).toBeInTheDocument();
    verdict({ supported: true, exists: false, details: "No quote" });
    expect(screen.getByText("Symbol not found")).toBeInTheDocument();
    expect(screen.getByText("(No quote)")).toBeInTheDocument();
    verdict(
      { supported: true, exists: false, details: "No quote" },
      "No fresh price for symbol",
    );
    expect(screen.getByText("No fresh price for symbol")).toBeInTheDocument();
    verdict({ supported: true, exists: false }, "No fresh price for symbol");
    expect(screen.getByText("Symbol not found")).toBeInTheDocument();
  });
});

describe("InstrumentDraftRow", () => {
  it.each(["base", "quote"])(
    "shows the %s lookup failure at its field",
    (field) => {
      render(
        <InstrumentDraftRow
          draft={{
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
          }}
          busy={false}
          valid
          onDraftChange={vi.fn()}
          onSubmit={vi.fn()}
          baseSuggestions={[]}
          quoteSuggestions={[]}
          baseSuggestionsFailed={field === "base"}
          quoteSuggestionsFailed={field === "quote"}
        />,
      );
      const failure = screen.getByRole("status");
      expect(failure).toHaveTextContent(
        "Asset suggestions could not be loaded.",
      );
      expect(failure.parentElement).toContainElement(
        screen.getByPlaceholderText(field === "base" ? "Base" : "Quote"),
      );
      expect(failure.parentElement).not.toContainElement(
        screen.getByPlaceholderText(field === "base" ? "Quote" : "Base"),
      );
    },
  );
  it("clears all plain draft fields through field-specific patches", async () => {
    const user = userEvent.setup();
    const onDraftChange = vi.fn();
    render(
      <InstrumentDraftRow
        draft={{ externalSymbol: "AAPL", baseAsset: "AAPL", quoteAsset: "USD" }}
        busy={false}
        valid
        onDraftChange={onDraftChange}
        onSubmit={vi.fn()}
      />,
    );
    const clears = screen.getAllByRole("button", { name: "Clear field" });
    expect(clears).toHaveLength(3);
    for (const [index, field] of [
      "externalSymbol",
      "baseAsset",
      "quoteAsset",
    ].entries()) {
      await user.click(clears[index]);
      expect(onDraftChange).toHaveBeenNthCalledWith(index + 1, { [field]: "" });
    }
  });
  const draft = {
    externalSymbol: "AAPL",
    baseAsset: "AAPL",
    quoteAsset: "USD",
  };
  it("edits and submits, and only renders supplied optional controls", async () => {
    const user = userEvent.setup();
    const onDraftChange = vi.fn();
    const onSubmit = vi.fn();
    const onVerify = vi.fn();
    const onToggle = vi.fn();
    const props = { draft, busy: false, valid: true, onDraftChange, onSubmit };
    const { container, rerender } = render(<InstrumentDraftRow {...props} />);
    expect(container.firstChild).toHaveClass(
      "md:grid-cols-[1.1fr_0.8fr_0.8fr_auto]",
    );
    expect(
      screen.queryByRole("button", { name: "Verify symbol" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Enabled" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByPlaceholderText("Manual price"),
    ).not.toBeInTheDocument();
    await user.type(screen.getByPlaceholderText("External symbol"), "X");
    expect(onDraftChange).toHaveBeenLastCalledWith({ externalSymbol: "AAPLX" });
    await user.type(screen.getByPlaceholderText("Base"), "X");
    expect(onDraftChange).toHaveBeenLastCalledWith({ baseAsset: "AAPLX" });
    await user.type(screen.getByPlaceholderText("Quote"), "X");
    expect(onDraftChange).toHaveBeenLastCalledWith({ quoteAsset: "USDX" });
    await user.click(screen.getByRole("button", { name: "Add instrument" }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
    rerender(
      <InstrumentDraftRow
        {...props}
        extraField={<input placeholder="Manual price" />}
      />,
    );
    expect(container.firstChild).toHaveClass(
      "md:grid-cols-[1.1fr_0.8fr_0.8fr_0.8fr_auto]",
    );
    rerender(
      <InstrumentDraftRow
        {...props}
        verify={{ verifying: false, onVerify }}
        enabled={{ value: true, onToggle }}
        quoteAdornment={<span>Pair warning</span>}
        extraField={<input placeholder="Manual price" />}
      />,
    );
    expect(container.firstChild).toHaveClass(
      "md:grid-cols-[1.1fr_0.8fr_0.8fr_0.8fr_auto_auto]",
    );
    expect(screen.getByText("Pair warning")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Manual price")).toBeInTheDocument();
    const children = Array.from(container.firstElementChild!.children);
    expect(children).toEqual([
      screen.getByPlaceholderText("External symbol").parentElement!
        .parentElement!.parentElement,
      screen.getByPlaceholderText("Base").parentElement!.parentElement,
      screen.getByPlaceholderText("Quote").parentElement!.parentElement!
        .parentElement!.parentElement,
      screen.getByPlaceholderText("Manual price"),
      screen.getByRole("button", { name: "Enabled" }),
      screen.getByRole("button", { name: "Add instrument" }),
    ]);
    expect(
      within(children[0] as HTMLElement).getByRole("button", {
        name: "Verify symbol",
      }),
    ).toBeInTheDocument();
    expect(
      within(children[2] as HTMLElement).getByText("Pair warning"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Verify symbol" }));
    expect(onVerify).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "Enabled" }));
    expect(onToggle).toHaveBeenCalledTimes(1);
    rerender(
      <InstrumentDraftRow
        {...props}
        valid={false}
        verify={{ verifying: true, onVerify }}
        enabled={{ value: false, onToggle }}
      />,
    );
    expect(container.firstChild).toHaveClass(
      "md:grid-cols-[1.1fr_0.8fr_0.8fr_auto_auto]",
    );
    expect(
      screen.getByRole("button", { name: "Add instrument" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Verify symbol" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Disabled" }),
    ).toBeInTheDocument();
    rerender(<InstrumentDraftRow {...props} busy />);
    for (const control of screen.getAllByRole("textbox"))
      expect(control).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Add instrument" }),
    ).toBeDisabled();
    rerender(
      <InstrumentDraftRow
        {...props}
        draft={{ ...draft, externalSymbol: " " }}
        verify={{ verifying: false, onVerify }}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Verify symbol" }),
    ).toBeDisabled();
  });

  it("uses autocomplete suggestions and clears both asset fields through patches", async () => {
    const user = userEvent.setup();
    const onDraftChange = vi.fn();
    render(
      <InstrumentDraftRow
        draft={draft}
        busy={false}
        valid
        onDraftChange={onDraftChange}
        onSubmit={vi.fn()}
        baseSuggestions={["AAPL", "AAPL2"]}
        quoteSuggestions={["USD", "USDC"]}
      />,
    );
    expect(screen.getAllByRole("combobox")).toHaveLength(2);
    const quoteCombobox = screen.getByPlaceholderText("Quote");
    expect(quoteCombobox).toHaveAttribute("role", "combobox");
    expect(quoteCombobox.parentElement!.parentElement).toHaveClass(
      "min-w-0",
      "flex-1",
    );
    const clear = screen.getAllByRole("button", { name: "Clear field" });
    expect(clear).toHaveLength(3);
    await user.click(clear[1]);
    expect(onDraftChange).toHaveBeenLastCalledWith({ baseAsset: "" });
    await user.click(clear[2]);
    expect(onDraftChange).toHaveBeenLastCalledWith({ quoteAsset: "" });
    await user.click(screen.getByPlaceholderText("Base"));
    await user.click(screen.getByRole("option", { name: "AAPL2" }));
    expect(onDraftChange).toHaveBeenLastCalledWith({ baseAsset: "AAPL2" });
    await user.click(screen.getByPlaceholderText("Quote"));
    await user.click(screen.getByRole("option", { name: "USDC" }));
    expect(onDraftChange).toHaveBeenLastCalledWith({ quoteAsset: "USDC" });
  });
});

describe("InstrumentsTable", () => {
  const row = {
    externalSymbol: "AAPL",
    baseAsset: "AAPL",
    quoteAsset: "USD",
    enabled: true,
  };
  it("renders empty rows, context columns and states, and calls row actions", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    const onDelete = vi.fn();
    const onVerify = vi.fn();
    const props = {
      instruments: [row],
      busy: false,
      parentEnabled: false,
      parentDisabledLabel: "Connection disabled",
      onToggle,
      onDelete,
    };
    const { rerender } = render(
      <InstrumentsTable {...props} instruments={[]} />,
    );
    expect(screen.getByText("No instruments configured")).toHaveAttribute(
      "colspan",
      "4",
    );
    rerender(
      <InstrumentsTable
        {...props}
        ariaLabel="Connection instruments"
        displaySymbol={() => "Apple contract"}
        metadata={() => "NASDAQ"}
        pairAdornment={() => <span>Duplicate pair</span>}
        stateAdornment={() => <span>Stale marker</span>}
        extraColumns={[
          {
            id: "price",
            header: "Price",
            cellClassName: "nums py-2 pr-3 text-text",
            renderCell: () => "123",
          },
        ]}
      />,
    );
    const table = screen.getByRole("table", { name: "Connection instruments" });
    expect(table).toHaveClass(
      "ledger-table",
      "text-[length:var(--dens-table-fz)]",
    );
    expect(table).not.toHaveClass("min-w-[840px]");
    expect(table.parentElement).toHaveClass("overflow-x-auto");
    expect(within(table).getAllByRole("columnheader")[0]).toHaveClass(
      "h-[var(--dens-head-h)]",
    );
    expect(
      within(table)
        .getAllByRole("columnheader")
        .map((header) => header.textContent),
    ).toEqual(["External", "Instrument", "Price", "State", "Actions"]);
    expect(screen.getByText("123")).toHaveClass("nums", "text-text");
    expect(screen.getByText("Apple contract")).toBeInTheDocument();
    expect(screen.getByText("NASDAQ")).toBeInTheDocument();
    expect(screen.getByText("Duplicate pair")).toBeInTheDocument();
    expect(screen.getByText("Stale marker")).toBeInTheDocument();
    expect(screen.getByText("Connection disabled")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Verify symbol" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Disable" }));
    expect(onToggle).toHaveBeenCalledWith(row);
    await user.click(screen.getByRole("button", { name: "Delete instrument" }));
    expect(onDelete).toHaveBeenCalledWith(row);
    rerender(<InstrumentsTable {...props} parentEnabled onVerify={onVerify} />);
    expect(screen.getByText("AAPL")).toBeInTheDocument();
    expect(screen.getByText("Enabled")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Verify symbol" }));
    expect(onVerify).toHaveBeenCalledWith(row);
    rerender(
      <InstrumentsTable
        {...props}
        onVerify={onVerify}
        isVerifying={(symbol) => symbol === "AAPL"}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Verify symbol" }),
    ).toBeDisabled();
    rerender(
      <InstrumentsTable
        {...props}
        instruments={[{ ...row, enabled: false }]}
        busy
        onVerify={onVerify}
      />,
    );
    expect(screen.getByText("Disabled")).toBeInTheDocument();
    for (const button of screen.getAllByRole("button"))
      expect(button).toBeDisabled();
    expect(screen.getByRole("button", { name: "Enable" })).toBeInTheDocument();
    rerender(
      <InstrumentsTable
        {...props}
        instruments={[]}
        extraColumns={[
          { id: "price", header: "Price", renderCell: () => "123" },
        ]}
      />,
    );
    expect(screen.getByText("No instruments configured")).toHaveAttribute(
      "colspan",
      "5",
    );
  });
  it("preserves uncontrolled cell values under their headers when extra columns reorder", async () => {
    const user = userEvent.setup();
    const columns = [
      {
        id: "first",
        header: "First editor",
        renderCell: () => <input defaultValue="first" />,
      },
      {
        id: "second",
        header: "Second editor",
        renderCell: () => <input defaultValue="second" />,
      },
    ];
    const props = {
      instruments: [row],
      busy: false,
      parentEnabled: true,
      parentDisabledLabel: "Connection disabled",
      onToggle: vi.fn(),
      onDelete: vi.fn(),
    };
    const { rerender } = render(
      <InstrumentsTable {...props} extraColumns={columns} />,
    );
    const inputUnderHeader = (name: string) => {
      const headers = screen.getAllByRole("columnheader");
      const index = headers.indexOf(screen.getByRole("columnheader", { name }));
      const cells = within(screen.getAllByRole("row")[1]).getAllByRole("cell");
      return within(cells[index]).getByRole("textbox");
    };
    await user.clear(inputUnderHeader("First editor"));
    await user.type(inputUnderHeader("First editor"), "typed value");
    rerender(
      <InstrumentsTable {...props} extraColumns={[columns[1], columns[0]]} />,
    );
    expect(
      screen.getAllByRole("columnheader").map((header) => header.textContent),
    ).toEqual([
      "External",
      "Instrument",
      "Second editor",
      "First editor",
      "State",
      "Actions",
    ]);
    expect(inputUnderHeader("First editor")).toHaveValue("typed value");
    expect(inputUnderHeader("Second editor")).toHaveValue("second");
  });
});

describe("DeleteInstrumentDialog", () => {
  it("does not request closing on Escape while busy", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    render(
      <DeleteInstrumentDialog
        open
        description="Remove AAPL from this connection."
        busy
        onSubmit={vi.fn()}
        onOpenChange={onOpenChange}
      />,
    );
    await user.keyboard("{Escape}");
    expect(onOpenChange).not.toHaveBeenCalled();
  });

  it("shows the description and error inside, then submits and cancels", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    const onOpenChange = vi.fn();
    const props = {
      open: true,
      description: "Remove AAPL from this connection.",
      busy: false,
      onSubmit,
      onOpenChange,
    };
    const { rerender } = render(
      <DeleteInstrumentDialog {...props} error="Deletion failed" />,
    );
    const dialog = screen.getByRole("alertdialog", {
      name: "Delete instrument?",
    });
    expect(within(dialog).getByRole("button", { name: "Delete" })).toHaveClass(
      "bg-[var(--danger)]",
    );
    expect(dialog).toHaveAccessibleDescription(props.description);
    expect(within(dialog).getByText("Deletion failed")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(onOpenChange).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
    rerender(<DeleteInstrumentDialog {...props} busy />);
    expect(screen.queryByText("Deletion failed")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
    rerender(<DeleteInstrumentDialog {...props} open={false} />);
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });
});
