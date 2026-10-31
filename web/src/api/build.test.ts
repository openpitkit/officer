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

import { describe, expect, it } from "vitest";

import { chooseDatabase, fetchBuild } from "@/api/build";
import { ApiError } from "@/framework";
import {
  clientAnswering,
  errorResponse,
  jsonResponse,
  sentRequest,
} from "@/test/apiClient";

const CHOICE = { release: false, phase: "choice", releaseDatabaseExists: true };

describe("fetchBuild", () => {
  it.each([
    CHOICE,
    { ...CHOICE, phase: "preparing" },
    { ...CHOICE, phase: "failed", error: "migration failed" },
    { ...CHOICE, phase: "ready" },
    { release: true, phase: "ready", releaseDatabaseExists: false },
  ])("decodes the C5 status %j", async (body) => {
    const { client, fetchMock } = clientAnswering(jsonResponse(body));
    const controller = new AbortController();

    await expect(fetchBuild(client, controller.signal)).resolves.toEqual(body);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe("/app/api/v1/build");
    expect(init.method).toBe("GET");
    expect(init.signal).toBe(controller.signal);
  });

  it.each<[string, unknown]>([
    ["body", null],
    ["body", []],
    ["phase", { ...CHOICE, phase: "unknown" }],
    ["phase", { ...CHOICE, phase: undefined }],
    ["phase", { ...CHOICE, phase: 1 }],
    ["release", { ...CHOICE, release: undefined }],
    ["release", { ...CHOICE, release: "false" }],
    ["releaseDatabaseExists", { ...CHOICE, releaseDatabaseExists: undefined }],
    ["releaseDatabaseExists", { ...CHOICE, releaseDatabaseExists: 1 }],
    ["error", { ...CHOICE, phase: "failed" }],
    ["error", { ...CHOICE, phase: "failed", error: 1 }],
    ["error", { ...CHOICE, phase: "failed", error: "" }],
    ...["choice", "preparing", "ready"].flatMap<[string, unknown]>((phase) => [
      ["error", { ...CHOICE, phase, error: "unexpected" }],
      ["error", { ...CHOICE, phase, error: null }],
      ["error", { ...CHOICE, phase, error: "" }],
    ]),
  ])("rejects invalid %s in %j", async (field, body) => {
    const { client } = clientAnswering(jsonResponse(body));

    await expect(fetchBuild(client)).rejects.toThrow(
      `build response: ${field} must`,
    );
  });
});

describe("chooseDatabase", () => {
  it.each([true, false])(
    "sends fromRelease: %s and decodes the 202",
    async (fromRelease) => {
      const body = { ...CHOICE, phase: "preparing" };
      const { client, fetchMock } = clientAnswering(jsonResponse(body, 202));

      await expect(chooseDatabase(client, fromRelease)).resolves.toEqual(body);

      const { url, init } = sentRequest(fetchMock);
      expect(url).toBe("/app/api/v1/build/database");
      expect(init.method).toBe("POST");
      expect(JSON.parse(init.body as string)).toEqual({ fromRelease });
    },
  );

  it("preserves the client's conflict error", async () => {
    const { client } = clientAnswering(errorResponse(409, "conflict"));
    await expect(chooseDatabase(client, true)).rejects.toEqual(
      new ApiError("conflict", "conflict", 409),
    );
  });

  it("rejects an invalid accepted body", async () => {
    const { client } = clientAnswering(
      jsonResponse({ ...CHOICE, phase: "unknown" }, 202),
    );
    await expect(chooseDatabase(client, false)).rejects.toThrow(
      "build response: phase must",
    );
  });
});
