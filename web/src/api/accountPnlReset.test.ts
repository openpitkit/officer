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
import { createApiClient } from "@/framework";
import {
  decodeResetSchedule,
  fetchResetSchedule,
  saveResetSchedule,
  recoverResetSchedule,
  resetRealizedAccountPnl,
} from "@/api/accountPnlReset";
import { jsonResponse } from "@/test/apiClient";
describe("local P&L reset API", () => {
  it("keeps absent state explicit and refuses malformed progress", () => {
    expect(
      decodeResetSchedule({ job: null, runtimeError: "worker stopped" }),
    ).toEqual({ job: null, runtimeError: "worker stopped" });
    for (const value of [
      {},
      { job: null },
      { job: {}, runtimeError: "" },
      { job: null, runtimeError: 0 },
    ])
      expect(() => decodeResetSchedule(value)).toThrow();
  });
  it("uses the same operator endpoints and forwards explicit finite recurrence", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(async () =>
        jsonResponse({ job: null, runtimeError: "" }),
      );
    const client = createApiClient({
      baseUrl: "/app/api/v1",
      fetch: fetchMock as unknown as typeof fetch,
    });
    await fetchResetSchedule(client);
    const input = {
      enabled: false,
      time: "02:30",
      timezone: "America/New_York",
      termination: { mode: "count" as const, maxRuns: 100 },
      revision: 7,
    };
    await saveResetSchedule(client, input);
    expect(fetchMock.mock.calls[1][0]).toBe(
      "/app/api/v1/account-pnl-reset-schedule",
    );
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual(input);
    await recoverResetSchedule(client, 8, "retry");
    expect(JSON.parse(fetchMock.mock.calls[2][1].body)).toEqual({
      revision: 8,
      action: "retry",
    });
    await resetRealizedAccountPnl(client, "desk/one");
    expect(fetchMock.mock.calls[3][0]).toBe(
      "/app/api/v1/accounts/desk%2Fone/realized-pnl/reset",
    );
    expect(fetchMock.mock.calls[3][1].method).toBe("POST");
  });
});
