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

import { vi } from "vitest";

/** The interval the panel's background polls run on. */
const POLL_INTERVAL_MS = 5000;

/**
 * Holds the panel's background polls: every window.setInterval at the polling
 * interval is captured instead of scheduled, so a poll runs only when the case
 * calls tick(), however slow the machine is. Other intervals, testing-library's
 * waitFor among them, run for real. restore() puts both timer functions back.
 */
export function holdPolling(): {
  /** Runs every held poll that is still scheduled, once. */
  tick: () => void;
  /** How many held polls are scheduled and not yet cleared. */
  scheduled: () => number;
  restore: () => void;
} {
  // Held handles are negative so they never collide with a real timer id.
  const polls = new Map<number, () => void>();
  let lastHandle = 0;
  const realSetInterval = window.setInterval.bind(window);
  const realClearInterval = window.clearInterval.bind(window);
  const setIntervalSpy = vi
    .spyOn(window, "setInterval")
    .mockImplementation((handler, delay, ...args) => {
      if (delay === POLL_INTERVAL_MS && typeof handler === "function") {
        lastHandle -= 1;
        polls.set(lastHandle, () => handler(...args));
        return lastHandle as unknown as ReturnType<typeof setInterval>;
      }
      return realSetInterval(handler, delay, ...args) as unknown as ReturnType<
        typeof setInterval
      >;
    });
  const clearIntervalSpy = vi
    .spyOn(window, "clearInterval")
    .mockImplementation((handle) => {
      if (typeof handle === "number" && polls.delete(handle)) {
        return;
      }
      realClearInterval(handle);
    });
  return {
    tick: () => [...polls.values()].forEach((poll) => poll()),
    scheduled: () => polls.size,
    restore: () => {
      setIntervalSpy.mockRestore();
      clearIntervalSpy.mockRestore();
    },
  };
}
