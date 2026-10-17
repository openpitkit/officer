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

import { accountScope, setAccountScope } from "@/lib/accountScope";

describe("accountScope", () => {
  it("throws when read before a scope is set", async () => {
    // The test setup sets a scope before every test; a fresh module instance
    // is the page before its first load.
    vi.resetModules();
    const fresh = await import("@/lib/accountScope");

    expect(() => fresh.accountScope()).toThrow("account scope is not loaded");
  });

  it("passes the chosen account through while accounts are shown", () => {
    setAccountScope({ accounts: true, account: "" });

    const scope = accountScope();
    expect(scope.showsAccounts).toBe(true);
    expect(scope.account).toBe("");
    expect(scope.accountFor("acc-7")).toBe("acc-7");
    expect(scope.accountFor("")).toBe("");
  });

  it("carries the default account while accounts are hidden", () => {
    setAccountScope({ accounts: false, account: "main" });

    const scope = accountScope();
    expect(scope.showsAccounts).toBe(false);
    expect(scope.account).toBe("main");
    expect(scope.accountFor("acc-7")).toBe("main");
    expect(scope.accountFor("")).toBe("main");
  });

  it("keeps a snapshot that later changes to the input do not reach", () => {
    const features = { accounts: false, account: "main" };
    setAccountScope(features);
    features.account = "other";

    expect(accountScope().account).toBe("main");
    expect(Object.isFrozen(accountScope())).toBe(true);
  });
});
