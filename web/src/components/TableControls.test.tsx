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

import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, it, vi } from "vitest";

import i18n from "@/i18n";
import { PageSizeSelect, TablePagination } from "@/components/TableControls";

function renderWithI18n(node: ReactNode) {
  return render(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>);
}

describe("TablePagination", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("shows exact page controls only when total pages are known", () => {
    renderWithI18n(
      <TablePagination
        page={0}
        canPrevious={false}
        canNext={true}
        knownTotalPages={3}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        onPage={vi.fn()}
      />,
    );

    expect(screen.getByText("Page 1 of 3")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Go to page 2" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Last page" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("spinbutton", { name: "Go to page" }),
    ).toBeInTheDocument();
  });

  it("does not invent page counts for limited server lists", () => {
    renderWithI18n(
      <TablePagination
        page={0}
        canPrevious={false}
        canNext={true}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        onPage={vi.fn()}
      />,
    );

    expect(screen.getByText("Page 1")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Go to page 2" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Last page" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("spinbutton", { name: "Go to page" }),
    ).not.toBeInTheDocument();
  });

  it("keeps thousands of pages compact", () => {
    renderWithI18n(
      <TablePagination
        page={2499}
        canPrevious={true}
        canNext={true}
        knownTotalPages={5000}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        onPage={vi.fn()}
      />,
    );

    expect(screen.getByText("Page 2500 of 5000")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Go to page 1" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Go to page 2500" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Go to page 5000" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Go to page 42" }),
    ).not.toBeInTheDocument();
  });
});

describe("PageSizeSelect", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("renders a compact numeric trigger", () => {
    renderWithI18n(
      <PageSizeSelect
        value={50}
        onChange={vi.fn()}
        ariaLabel="Rows per page"
        rowCountLabel={(count) => `${count} rows per page`}
      />,
    );

    const trigger = screen.getByRole("combobox", { name: "Rows per page" });
    expect(trigger).toHaveTextContent("50");
    expect(trigger).not.toHaveTextContent("rows per page");
    expect(trigger).toHaveClass("w-16");
  });
});
