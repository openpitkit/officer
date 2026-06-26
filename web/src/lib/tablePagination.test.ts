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

import { describe, expect, it } from "vitest";

import {
  hasNextPage,
  knownPageCount,
  pageFetchLimit,
  slicePage,
} from "@/lib/tablePagination";

describe("tablePagination", () => {
  it("counts exact pages for loaded client-side rows", () => {
    expect(knownPageCount(0, 50)).toBe(1);
    expect(knownPageCount(50, 50)).toBe(1);
    expect(knownPageCount(51, 50)).toBe(2);
    expect(knownPageCount(101, 50)).toBe(3);
  });

  it("fetches one sentinel row past the requested page window", () => {
    expect(pageFetchLimit(0, 50)).toBe(51);
    expect(pageFetchLimit(1, 50)).toBe(101);
  });

  it("slices empty and first-page rows", () => {
    expect(slicePage([], 0, 50)).toEqual([]);
    expect(slicePage(["a", "b", "c"], 0, 2)).toEqual(["a", "b"]);
  });

  it("returns an empty slice for pages beyond available data", () => {
    expect(slicePage(["a", "b", "c"], 2, 2)).toEqual([]);
  });

  it("detects exact sentinel page boundaries", () => {
    expect(hasNextPage(["a", "b"], 0, 2)).toBe(false);
    expect(hasNextPage(["a", "b", "c"], 0, 2)).toBe(true);
    expect(hasNextPage(["a", "b", "c"], 1, 2)).toBe(false);
  });
});
