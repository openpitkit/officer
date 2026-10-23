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
  endAllSessions,
  fetchAccess,
  fetchAccessToken,
  fetchSession,
  isAccessModeConflict,
  logIn,
  logOut,
  passwordLength,
  passwordLengthAllowed,
  putAccessMode,
  putAccessPassword,
  regenerateAccessToken,
  requestRecoveryCode,
  resetPassword,
} from "@/api/access";
import { ApiError } from "@/framework";
import {
  clientAnswering,
  errorResponse,
  jsonResponse,
  sentRequest,
} from "@/test/apiClient";

const BASE_URL = "/app/api/v1";

const tokenSession = {
  accessMode: "token",
  setupComplete: true,
  authenticated: true,
};

describe("fetchSession", () => {
  it.each([
    [
      "open access",
      { ...tokenSession, accessMode: "open", authenticated: false },
    ],
    ["a token session", tokenSession],
    [
      "token access without a session",
      { ...tokenSession, authenticated: false },
    ],
    [
      "the setup state",
      { accessMode: "setup", setupComplete: false, authenticated: false },
    ],
  ])("decodes %s from GET /session", async (_case, body) => {
    const { client, fetchMock } = clientAnswering(jsonResponse(body));

    await expect(fetchSession(client)).resolves.toEqual({
      accessMode: body.accessMode,
      authenticated: body.authenticated,
    });

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/session`);
    expect(init.method).toBe("GET");
  });

  it.each([
    ["body", "a non-object body", ["token"]],
    [
      "accessMode",
      "an unknown access mode",
      { ...tokenSession, accessMode: "closed" },
    ],
    [
      "accessMode",
      "a missing access mode",
      { ...tokenSession, accessMode: undefined },
    ],
    [
      "setupComplete",
      "the setup state with a complete setup",
      { accessMode: "setup", setupComplete: true, authenticated: false },
    ],
    [
      "setupComplete",
      "token access with an incomplete setup",
      { ...tokenSession, setupComplete: false },
    ],
    [
      "setupComplete",
      "open access with an incomplete setup",
      { accessMode: "open", setupComplete: false, authenticated: false },
    ],
    [
      "setupComplete",
      "a missing setupComplete",
      { ...tokenSession, setupComplete: undefined },
    ],
    [
      "authenticated",
      "a non-boolean authenticated",
      { ...tokenSession, authenticated: "yes" },
    ],
    [
      "authenticated",
      "a missing authenticated",
      { ...tokenSession, authenticated: undefined },
    ],
  ])("rejects %s for %s", async (field, _case, body) => {
    const { client } = clientAnswering(jsonResponse(body));

    await expect(fetchSession(client)).rejects.toThrow(
      `session response: ${field} must`,
    );
  });
});

describe("logIn", () => {
  it("sends the password to POST /session", async () => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(logIn(client, "correct horse battery")).resolves.toBe(
      undefined,
    );

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/session`);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({
      password: "correct horse battery",
    });
  });

  it("rejects a wrong password with the delay before the next attempt", async () => {
    const { client } = clientAnswering(
      new Response(
        JSON.stringify({
          error: { code: "invalid_password", message: "wrong password" },
        }),
        { status: 401, headers: { "Retry-After": "4" } },
      ),
    );

    const err = await logIn(client, "wrong").catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      code: "invalid_password",
      retryAfterSeconds: 4,
    });
  });
});

describe("logOut", () => {
  it("ends the session with DELETE /session", async () => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(logOut(client)).resolves.toBe(undefined);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/session`);
    expect(init.method).toBe("DELETE");
  });
});

describe("requestRecoveryCode", () => {
  const issued = { issued: true, expiresAt: "2026-10-16T12:15:00Z" };

  it.each([
    ["a new code", issued],
    [
      "a live code with an offset and fractional seconds",
      { issued: false, expiresAt: "2026-10-16T15:15:00.123456+03:00" },
    ],
  ])("decodes %s from POST /session/recovery", async (_case, body) => {
    const { client, fetchMock } = clientAnswering(jsonResponse(body));

    await expect(requestRecoveryCode(client)).resolves.toEqual(body);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/session/recovery`);
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
  });

  it.each([
    ["body", "a non-object body", "issued"],
    ["issued", "a non-boolean issued", { ...issued, issued: "true" }],
    ["issued", "a missing issued", { ...issued, issued: undefined }],
    ["expiresAt", "a missing expiresAt", { ...issued, expiresAt: undefined }],
    ["expiresAt", "a numeric expiresAt", { ...issued, expiresAt: 1760616900 }],
    [
      "expiresAt",
      "a time without a zone",
      { ...issued, expiresAt: "2026-10-16T12:15:00" },
    ],
    ["expiresAt", "a date alone", { ...issued, expiresAt: "2026-10-16" }],
    [
      "expiresAt",
      "a day past the end of the month",
      { ...issued, expiresAt: "2026-02-30T12:00:00Z" },
    ],
    [
      "expiresAt",
      "an hour past the end of the day",
      { ...issued, expiresAt: "2026-10-16T24:00:00Z" },
    ],
    [
      "expiresAt",
      "an impossible date",
      { ...issued, expiresAt: "2026-13-40T12:15:00Z" },
    ],
  ])("rejects %s for %s", async (field, _case, body) => {
    const { client } = clientAnswering(jsonResponse(body));

    await expect(requestRecoveryCode(client)).rejects.toThrow(
      `recovery code response: ${field} must`,
    );
  });
});

describe("resetPassword", () => {
  it("sends the code and the new password to POST /session/recovery/reset", async () => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(
      resetPassword(client, {
        code: "7KQ4-MZ2P-X9RD",
        password: "a new panel password",
      }),
    ).resolves.toBe(undefined);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/session/recovery/reset`);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({
      code: "7KQ4-MZ2P-X9RD",
      password: "a new panel password",
    });
  });
});

describe("isAccessModeConflict", () => {
  it.each([
    ["a conflict", new ApiError("conflict", "conflict", 409), true],
    ["another refusal", new ApiError("internal", "internal", 500), false],
    ["an error from outside the client", new Error("conflict"), false],
  ])("answers %s with %s", (_case, err, conflict) => {
    expect(isAccessModeConflict(err)).toBe(conflict);
  });
});

describe("passwordLength", () => {
  it.each([
    ["ASCII characters", "a".repeat(15), 15],
    [
      "characters outside the Basic Multilingual Plane",
      "\u{1D538}".repeat(15),
      15,
    ],
    ["an empty password", "", 0],
  ])("counts %s as code points", (_case, password, length) => {
    expect(passwordLength(password)).toBe(length);
  });
});

describe("passwordLengthAllowed", () => {
  it.each([
    [14, false],
    [15, true],
    [256, true],
    [257, false],
  ])("answers %i characters with %s", (length, allowed) => {
    expect(passwordLengthAllowed("a".repeat(length))).toBe(allowed);
  });

  it("counts characters outside the Basic Multilingual Plane once", () => {
    // 14 of them take 28 UTF-16 code units, 129 of them take 258.
    expect(passwordLengthAllowed("\u{1D538}".repeat(14))).toBe(false);
    expect(passwordLengthAllowed("\u{1D538}".repeat(129))).toBe(true);
  });
});

describe("fetchAccess", () => {
  const tokenAccess = {
    mode: "token",
    passwordSetAt: "2026-10-15T14:05:00Z",
    sessions: 2,
  };
  const openAccess = { mode: "open", passwordSetAt: null, sessions: 0 };

  it.each([
    ["token access", tokenAccess],
    ["open access", openAccess],
  ])("decodes %s from GET /access", async (_case, body) => {
    const { client, fetchMock } = clientAnswering(jsonResponse(body));

    await expect(fetchAccess(client)).resolves.toEqual(body);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/access`);
    expect(init.method).toBe("GET");
  });

  it.each([
    ["body", "a non-object body", ["token"]],
    ["mode", "the setup state", { ...tokenAccess, mode: "setup" }],
    ["mode", "a missing mode", { ...tokenAccess, mode: undefined }],
    ["sessions", "a string count", { ...tokenAccess, sessions: "2" }],
    ["sessions", "a negative count", { ...tokenAccess, sessions: -1 }],
    ["sessions", "a fractional count", { ...tokenAccess, sessions: 1.5 }],
    ["sessions", "a missing count", { ...tokenAccess, sessions: undefined }],
    [
      "passwordSetAt",
      "token access without a password time",
      { ...tokenAccess, passwordSetAt: null },
    ],
    [
      "passwordSetAt",
      "a password date alone",
      { ...tokenAccess, passwordSetAt: "2026-10-15" },
    ],
    [
      "passwordSetAt",
      "a day past the end of the month",
      { ...tokenAccess, passwordSetAt: "2026-02-30T12:00:00Z" },
    ],
    [
      "passwordSetAt",
      "a day past the end of the month at an offset",
      { ...tokenAccess, passwordSetAt: "2026-04-31T01:30:00+03:00" },
    ],
    [
      "passwordSetAt",
      "open access with a password time",
      { ...openAccess, passwordSetAt: "2026-10-15T14:05:00Z" },
    ],
    [
      "passwordSetAt",
      "open access without passwordSetAt",
      { ...openAccess, passwordSetAt: undefined },
    ],
  ])("rejects %s for %s", async (field, _case, body) => {
    const { client } = clientAnswering(jsonResponse(body));

    await expect(fetchAccess(client)).rejects.toThrow(
      `access response: ${field} must`,
    );
  });
});

describe("putAccessMode", () => {
  it.each([
    [
      "open access with the current password and the acknowledgement",
      {
        mode: "open",
        currentPassword: "the panel password in force",
        acknowledged: true,
      } as const,
    ],
    [
      "token access with the new password",
      { mode: "token", password: "a new panel password" } as const,
    ],
  ])("sends %s to PUT /access/mode", async (_case, request) => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(putAccessMode(client, request)).resolves.toBe(undefined);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/access/mode`);
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body as string)).toEqual(request);
  });

  it("rejects a wrong current password without the unauthorized code", async () => {
    const { client } = clientAnswering(
      errorResponse(403, "current_password_invalid"),
    );

    const err = await putAccessMode(client, {
      mode: "open",
      currentPassword: "wrong",
      acknowledged: true,
    }).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ code: "current_password_invalid" });
  });
});

describe("putAccessPassword", () => {
  it("sends both passwords to PUT /access/password", async () => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(
      putAccessPassword(client, {
        currentPassword: "the panel password in force",
        password: "a new panel password",
      }),
    ).resolves.toBe(undefined);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/access/password`);
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body as string)).toEqual({
      currentPassword: "the panel password in force",
      password: "a new panel password",
    });
  });

  it("rejects a refused password with the problem detail", async () => {
    const { client } = clientAnswering(
      new Response(
        JSON.stringify({
          type: "about:blank",
          title: "Unprocessable Entity",
          status: 422,
          detail: "password must be at least 15 characters",
          errors: [{ pointer: "/password", detail: "too short" }],
        }),
        {
          status: 422,
          headers: { "Content-Type": "application/problem+json" },
        },
      ),
    );

    const err = await putAccessPassword(client, {
      currentPassword: "the panel password in force",
      password: "short",
    }).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      code: "precondition",
      message: "password must be at least 15 characters",
    });
  });
});

describe("endAllSessions", () => {
  it("ends every session with DELETE /sessions", async () => {
    const { client, fetchMock } = clientAnswering(
      new Response(null, { status: 204 }),
    );

    await expect(endAllSessions(client)).resolves.toBe(undefined);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/sessions`);
    expect(init.method).toBe("DELETE");
  });
});

// 32 bytes in unpadded base64url: 43 characters after the prefix.
const AGENT_TOKEN = "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w";
const API_TOKEN = "pit_api_Lm4Ts9Qa2Xc7Vn1Bk6Hd3Wy8Pe5Rz0Gu2Jf7Os4Ni9w";

describe("fetchAccessToken", () => {
  it.each([
    ["agent", AGENT_TOKEN],
    ["api", API_TOKEN],
  ] as const)(
    "decodes the %s token from GET /access/tokens/{kind}",
    async (kind, token) => {
      const { client, fetchMock } = clientAnswering(jsonResponse({ token }));

      await expect(fetchAccessToken(client, kind)).resolves.toBe(token);

      const { url, init } = sentRequest(fetchMock);
      expect(url).toBe(`${BASE_URL}/access/tokens/${kind}`);
      expect(init.method).toBe("GET");
    },
  );

  it.each([
    ["body", "a non-object body", [AGENT_TOKEN]],
    ["token", "a missing token", {}],
    ["token", "the API token", { token: API_TOKEN }],
    ["token", "a short body", { token: AGENT_TOKEN.slice(0, -1) }],
    ["token", "a padded body", { token: `${AGENT_TOKEN.slice(0, -1)}A=` }],
    [
      "token",
      "a body outside base64url",
      { token: `${AGENT_TOKEN.slice(0, -1)}+` },
    ],
  ])("rejects %s for %s", async (field, _case, body) => {
    const { client } = clientAnswering(jsonResponse(body));

    await expect(fetchAccessToken(client, "agent")).rejects.toThrow(
      `agent token response: ${field} must`,
    );
  });
});

describe("regenerateAccessToken", () => {
  it("decodes the new value from POST /access/tokens/{kind}/regenerate", async () => {
    const { client, fetchMock } = clientAnswering(
      jsonResponse({ token: API_TOKEN }),
    );

    await expect(regenerateAccessToken(client, "api")).resolves.toBe(API_TOKEN);

    const { url, init } = sentRequest(fetchMock);
    expect(url).toBe(`${BASE_URL}/access/tokens/api/regenerate`);
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
  });

  it("rejects a value of the other kind", async () => {
    const { client } = clientAnswering(jsonResponse({ token: AGENT_TOKEN }));

    await expect(regenerateAccessToken(client, "api")).rejects.toThrow(
      "api token response: token must be pit_api_",
    );
  });
});
