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

import {
  completeSetup,
  declineEncryption,
  fetchSetup,
  isSetupComplete,
  putSetupProgress,
  SETUP_STEPS,
} from "@/api/setup";
import { ApiError } from "@/framework";
import {
  clientAnswering,
  errorResponse,
  jsonResponse,
  sentRequest,
} from "@/test/apiClient";

const BASE_URL = "/app/api/v1";

// The setup steps the server contract fixes, in the order the wizard walks.
const CONTRACT_STEPS = [
  "welcome",
  "accounts",
  "encryption",
  "signing",
  "access",
];

describe("SETUP_STEPS", () => {
  it("lists the contract steps in the order the wizard walks", () => {
    expect(SETUP_STEPS).toEqual(CONTRACT_STEPS);
  });
});

describe("fetchSetup", () => {
  const declined = {
    step: "signing",
    encryptionDeclinedAt: "2026-10-17T09:30:00Z",
  };

  it.each(CONTRACT_STEPS)(
    "decodes the %s step from GET /setup",
    async (step) => {
      const body = { step, encryptionDeclinedAt: null };
      const { client, fetchMock } = clientAnswering(jsonResponse(body));

      await expect(fetchSetup(client)).resolves.toEqual(body);

      const { url, init } = sentRequest(fetchMock);
      expect(url).toBe(`${BASE_URL}/setup`);
      expect(init.method).toBe("GET");
    },
  );

  it.each([
    ["a declined encryption", declined],
    [
      "a decline time with an offset and fractional seconds",
      {
        step: "access",
        encryptionDeclinedAt: "2026-10-17T12:30:00.123456+03:00",
      },
    ],
  ])("decodes %s from GET /setup", async (_case, body) => {
    const { client } = clientAnswering(jsonResponse(body));

    await expect(fetchSetup(client)).resolves.toEqual(body);
  });

  it.each([
    ["body", "a non-object body", ["signing"]],
    ["step", "a step stored after the setup", { ...declined, step: "agent" }],
    ["step", "a missing step", { ...declined, step: undefined }],
    ["step", "a numeric step", { ...declined, step: 3 }],
    [
      "encryptionDeclinedAt",
      "a missing encryptionDeclinedAt",
      { ...declined, encryptionDeclinedAt: undefined },
    ],
    [
      "encryptionDeclinedAt",
      "a numeric decline time",
      { ...declined, encryptionDeclinedAt: 1760693400 },
    ],
    [
      "encryptionDeclinedAt",
      "a decline date alone",
      { ...declined, encryptionDeclinedAt: "2026-10-17" },
    ],
    [
      "encryptionDeclinedAt",
      "a day past the end of the month",
      { ...declined, encryptionDeclinedAt: "2026-02-30T12:00:00Z" },
    ],
  ])("rejects %s for %s", async (field, _case, body) => {
    const { client } = clientAnswering(jsonResponse(body));

    await expect(fetchSetup(client)).rejects.toThrow(
      `setup response: ${field} must`,
    );
  });

  it("rejects a finished setup with the setup_complete code", async () => {
    const { client } = clientAnswering(errorResponse(409, "setup_complete"));

    const err = await fetchSetup(client).catch((e: unknown) => e);

    expect(isSetupComplete(err)).toBe(true);
  });
});

describe("putSetupProgress", () => {
  it("sends the step to PUT /setup/progress", async () => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(putSetupProgress(client, "encryption")).resolves.toBe(
      undefined,
    );

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/setup/progress`);
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body as string)).toEqual({ step: "encryption" });
  });
});

describe("declineEncryption", () => {
  it("sends the acknowledged decline to PUT /setup/encryption", async () => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(declineEncryption(client)).resolves.toBe(undefined);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/setup/encryption`);
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body as string)).toEqual({
      encryption: "none",
      acknowledged: true,
    });
  });
});

describe("completeSetup", () => {
  it.each([
    [
      "token access with the panel password",
      { access: "token", password: "a new panel password" } as const,
    ],
    [
      "open access with the acknowledgement",
      { access: "open", acknowledged: true } as const,
    ],
  ])("sends %s to POST /setup/complete", async (_case, request) => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(completeSetup(client, request)).resolves.toBe(undefined);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/setup/complete`);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual(request);
  });
});

describe("isSetupComplete", () => {
  it.each([
    [
      "a finished setup",
      new ApiError("setup_complete", "setup_complete", 409),
      true,
    ],
    ["another refusal", new ApiError("conflict", "conflict", 409), false],
    ["an error from outside the client", new Error("setup_complete"), false],
  ])("answers %s with %s", (_case, err, complete) => {
    expect(isSetupComplete(err)).toBe(complete);
  });
});
