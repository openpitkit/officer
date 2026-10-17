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

import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { setAccountScope } from "@/lib/accountScope";
import {
  setGlobalAccountFilter,
  useGlobalAccountFilter,
} from "@/lib/globalAccountFilter";

describe("useGlobalAccountFilter", () => {
  beforeEach(() => {
    setGlobalAccountFilter("");
  });

  it("reports the stored filter while accounts are shown", () => {
    setGlobalAccountFilter("acc-7");

    const { result } = renderHook(() => useGlobalAccountFilter());

    expect(result.current.account).toBe("acc-7");
    expect(result.current.enabled).toBe(true);
  });

  it("reports no filter while accounts are hidden and keeps it stored", () => {
    setGlobalAccountFilter("acc-7");
    setAccountScope({ accounts: false, account: "main" });

    const hidden = renderHook(() => useGlobalAccountFilter());

    expect(hidden.result.current.account).toBe("");
    expect(hidden.result.current.enabled).toBe(false);

    hidden.unmount();
    setAccountScope({ accounts: true, account: "" });
    const shown = renderHook(() => useGlobalAccountFilter());

    expect(shown.result.current.account).toBe("acc-7");
    expect(shown.result.current.enabled).toBe(true);
  });
});
