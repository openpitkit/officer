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

import { describe, expect, it, vi } from "vitest";

import {
  checkSession,
  onCheckSession,
  panelSession,
  setPanelSession,
} from "@/lib/panelSession";

describe("panelSession", () => {
  it("throws when read before the gate sets it", async () => {
    // A fresh module instance is the page before the gate's first load.
    vi.resetModules();
    const fresh = await import("@/lib/panelSession");

    expect(() => fresh.panelSession()).toThrow("panel session is not loaded");
  });

  it("returns the access mode the gate set, as a frozen snapshot", () => {
    setPanelSession("token");
    expect(panelSession().accessMode).toBe("token");
    expect(Object.isFrozen(panelSession())).toBe(true);

    setPanelSession("open");
    expect(panelSession().accessMode).toBe("open");
  });
});

describe("checkSession", () => {
  it("calls every subscribed check until it unsubscribes", () => {
    const first = vi.fn();
    const second = vi.fn();
    const unsubscribeFirst = onCheckSession(first);
    const unsubscribeSecond = onCheckSession(second);

    checkSession();
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);

    unsubscribeFirst();
    checkSession();
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(2);

    unsubscribeSecond();
    checkSession();
    expect(second).toHaveBeenCalledTimes(2);
  });
});
