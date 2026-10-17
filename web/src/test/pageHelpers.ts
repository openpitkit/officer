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

import { fireEvent, screen } from "@testing-library/react";
import { vi } from "vitest";

/**
 * Query of a page's share link. A link is read from its href; a share button
 * is clicked with a modifier and the URL it hands to window.open is read.
 */
export function shareLinkParams(share: HTMLElement): URLSearchParams {
  let href = share.getAttribute("href");
  if (href === null) {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    fireEvent.click(share, { ctrlKey: true });
    const opened = open.mock.calls[0]?.[0];
    open.mockRestore();
    if (typeof opened !== "string") {
      throw new Error("share link did not open");
    }
    href = opened;
  }
  return new URL(href, window.location.origin).searchParams;
}

/** The share control of the current filter set, a button or a link. */
export function filterSetShare(role: "button" | "link"): HTMLElement {
  return screen.getByRole(role, {
    name: /copy a link to the current filter set/i,
  });
}

/** Column count of the loading TableSkeleton currently on screen. */
export function skeletonColumns(): number {
  const bar = screen.getByRole("status", { busy: true }).querySelector(".h-3");
  if (!bar?.parentElement) {
    throw new Error("loading skeleton row not found");
  }
  return bar.parentElement.children.length;
}
