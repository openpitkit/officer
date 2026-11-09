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

/** Fetch through the zero-based page plus one row to detect a next page. */
export function pageFetchLimit(page: number, pageSize: number): number {
  return (page + 1) * pageSize + 1;
}

/** Select a zero-based page from rows fetched from the start of a list. */
export function slicePage<Row>(rows: Row[], page: number, pageSize: number): Row[] {
  return rows.slice(page * pageSize, (page + 1) * pageSize);
}

/** Whether the fetched rows extend beyond the zero-based current page. */
export function hasNextPage<Row>(
  rows: Row[],
  page: number,
  pageSize: number,
): boolean {
  return rows.length > (page + 1) * pageSize;
}

/** Count pages in a complete list, with one page for an empty list. */
export function knownPageCount(rowCount: number, pageSize: number): number {
  return Math.max(1, Math.ceil(rowCount / pageSize));
}
