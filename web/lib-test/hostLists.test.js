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

import * as officer from "@openpit/officer-web";
import { cleanup, fireEvent, render } from "@testing-library/react";
import React, { useState } from "react";
import { afterEach, expect, it } from "vitest";

afterEach(() => {
  cleanup();
  localStorage.clear();
});

it("composes a host list using the built package exports", async () => {
  await officer.initOfficerI18n();
  const rows = ["AAPL", "MSFT", "NVDA", "IBM", "ORCL", "AMD"];
  expect(officer.pageFetchLimit(0, 5)).toBe(6);
  expect(officer.knownPageCount(rows.length, 5)).toBe(2);

  function HostList() {
    const [page, setPage] = useState(0);
    const [size, setSize] = officer.usePersistentPageSize("host-list-size", 5);
    return React.createElement(
      React.Fragment,
      null,
      React.createElement(
        "p",
        null,
        officer.slicePage(rows, page, size).join(","),
      ),
      React.createElement(officer.PageSizeSelect, {
        ariaLabel: "Rows per page",
        value: size,
        onChange: setSize,
        rowCountLabel: (count) => String(count),
      }),
      React.createElement(officer.TablePagination, {
        page,
        canPrevious: page > 0,
        canNext: officer.hasNextPage(rows, page, size),
        knownTotalPages: officer.knownPageCount(rows.length, size),
        onPrevious: () => setPage(page - 1),
        onNext: () => setPage(page + 1),
        onPage: setPage,
      }),
      React.createElement(officer.TableSkeleton, { rows: 2, cols: 3 }),
      React.createElement(officer.EmptyState, {
        title: "No matching instruments",
        hint: "Change the filter",
      }),
    );
  }

  const view = render(React.createElement(HostList));
  expect(view.getByText("AAPL,MSFT,NVDA,IBM,ORCL")).toBeTruthy();
  expect(view.getByRole("combobox", { name: "Rows per page" })).toBeTruthy();
  expect(view.getByRole("status", { name: "Loading" })).toBeTruthy();
  expect(view.getByText("No matching instruments")).toBeTruthy();
  fireEvent.click(view.getByRole("button", { name: "Next" }));
  expect(view.getByText("AMD")).toBeTruthy();
  expect(view.getByRole("button", { name: "Next" }).disabled).toBe(true);
});
