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

import { invalid, isRecord } from "@/api/decode";
import { ApiError, type ApiClient } from "@/framework";
import { isRfc3339Time } from "@/framework/api/rfc3339";

/**
 * How Officer admits callers: `open` lets every caller in, `token` lets the
 * panel in by password and programs by their tokens.
 */
export type AccessMode = "open" | "token";

/** The answer of `GET /session`. */
export interface SessionState {
  /** The access mode the running process enforces. */
  accessMode: AccessMode;
  /**
   * Whether the request carried a live session cookie; always false in
   * `open`, so the access mode decides first.
   */
  authenticated: boolean;
}

/** The answer of `POST /session/recovery`. */
export interface RecoveryCodeState {
  /**
   * Whether this request printed a new code; false while an earlier code is
   * still live in the log.
   */
  issued: boolean;
  /** When the code in the log expires, an RFC 3339 timestamp. */
  expiresAt: string;
}

/**
 * Whether `err` is the `409 conflict` an access route answers when the access
 * mode in force is not the one the request was made for: the mode switched
 * since the page loaded, and the startup gate reloads the page under the new
 * one once it checks the session.
 */
export function isAccessModeConflict(err: unknown): boolean {
  return err instanceof ApiError && err.code === "conflict";
}

function decodeSession(value: unknown): SessionState {
  if (!isRecord(value)) {
    throw invalid("session", "body", "must be an object");
  }
  const { accessMode, setupComplete, authenticated } = value;
  // The first-run setup state is not handled by the panel yet: a process that
  // reports it is a load error, not a guessed mode.
  if (accessMode !== "open" && accessMode !== "token") {
    throw invalid("session", "accessMode", 'must be "open" or "token"');
  }
  if (setupComplete !== true) {
    throw invalid("session", "setupComplete", "must be true");
  }
  if (typeof authenticated !== "boolean") {
    throw invalid("session", "authenticated", "must be a boolean");
  }
  return { accessMode, authenticated };
}

function decodeRecoveryCode(value: unknown): RecoveryCodeState {
  if (!isRecord(value)) {
    throw invalid("recovery code", "body", "must be an object");
  }
  const { issued, expiresAt } = value;
  if (typeof issued !== "boolean") {
    throw invalid("recovery code", "issued", "must be a boolean");
  }
  if (!isRfc3339Time(expiresAt)) {
    throw invalid("recovery code", "expiresAt", "must be an RFC 3339 time");
  }
  return { issued, expiresAt };
}

/**
 * Fetch the access mode and whether this browser has a session from
 * `GET /session`.
 *
 * @throws Error naming the offending field when the response is malformed.
 */
export async function fetchSession(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<SessionState> {
  return decodeSession(
    await client.request(`${client.baseUrl}/session`, { signal }),
  );
}

/**
 * Log in with the panel password through `POST /session`; the answer sets
 * the session cookie.
 *
 * @throws ApiError `invalid_password` for a wrong password and
 * `too_many_attempts` during the delay after one, both with
 * `retryAfterSeconds`.
 */
export async function logIn(
  client: ApiClient,
  password: string,
): Promise<void> {
  await client.request(`${client.baseUrl}/session`, {
    method: "POST",
    body: { password },
  });
}

/** End this browser's session with `DELETE /session`. */
export async function logOut(client: ApiClient): Promise<void> {
  await client.request(`${client.baseUrl}/session`, { method: "DELETE" });
}

/**
 * Ask Officer to print a recovery code to its log with
 * `POST /session/recovery`.
 *
 * @throws ApiError `too_many_attempts` with `retryAfterSeconds` when asked
 * more often than once a minute.
 * @throws Error naming the offending field when the response is malformed.
 */
export async function requestRecoveryCode(
  client: ApiClient,
): Promise<RecoveryCodeState> {
  return decodeRecoveryCode(
    await client.request(`${client.baseUrl}/session/recovery`, {
      method: "POST",
    }),
  );
}

/** The body of `POST /session/recovery/reset`. */
export interface PasswordResetRequest {
  /** The recovery code from the log. */
  code: string;
  /** The new panel password. */
  password: string;
}

/**
 * Set a new panel password with a recovery code through
 * `POST /session/recovery/reset`. Every other session ends; the answer sets a
 * session cookie for this browser.
 *
 * @throws ApiError `invalid_recovery_code` for a wrong or expired code, and
 * `precondition` for a password the server rejects.
 */
export async function resetPassword(
  client: ApiClient,
  request: PasswordResetRequest,
): Promise<void> {
  await client.request(`${client.baseUrl}/session/recovery/reset`, {
    method: "POST",
    body: request,
  });
}

/** The fewest characters a panel password may have. */
export const PASSWORD_MIN_LENGTH = 15;
/** The most characters a panel password may have. */
export const PASSWORD_MAX_LENGTH = 256;

/**
 * The length of a panel password as Officer counts it: in Unicode code
 * points, so a character outside the Basic Multilingual Plane counts once.
 */
export function passwordLength(password: string): number {
  return Array.from(password).length;
}

/** Whether Officer accepts the length of `password` for a panel password. */
export function passwordLengthAllowed(password: string): boolean {
  const length = passwordLength(password);
  return length >= PASSWORD_MIN_LENGTH && length <= PASSWORD_MAX_LENGTH;
}

/** The answer of `GET /access`. */
export type AccessState =
  | {
      mode: "token";
      /** When the panel password was set, an RFC 3339 timestamp. */
      passwordSetAt: string;
      /** How many browser sessions are live. */
      sessions: number;
    }
  | {
      mode: "open";
      /** `open` access has no panel password. */
      passwordSetAt: null;
      /** How many browser sessions are live. */
      sessions: number;
    };

function decodeAccess(value: unknown): AccessState {
  if (!isRecord(value)) {
    throw invalid("access", "body", "must be an object");
  }
  const { mode, passwordSetAt, sessions } = value;
  if (mode !== "open" && mode !== "token") {
    throw invalid("access", "mode", 'must be "open" or "token"');
  }
  if (
    typeof sessions !== "number" ||
    !Number.isSafeInteger(sessions) ||
    sessions < 0
  ) {
    throw invalid("access", "sessions", "must be a non-negative integer");
  }
  if (mode === "open") {
    if (passwordSetAt !== null) {
      throw invalid("access", "passwordSetAt", "must be null in open access");
    }
    return { mode, passwordSetAt, sessions };
  }
  if (!isRfc3339Time(passwordSetAt)) {
    throw invalid("access", "passwordSetAt", "must be an RFC 3339 time");
  }
  return { mode, passwordSetAt, sessions };
}

/**
 * Fetch the access mode, when the panel password was set and how many
 * browser sessions are live from `GET /access`.
 *
 * @throws Error naming the offending field when the response is malformed.
 */
export async function fetchAccess(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<AccessState> {
  return decodeAccess(
    await client.request(`${client.baseUrl}/access`, { signal }),
  );
}

/** The body of `PUT /access/mode`. */
export type AccessModeRequest =
  | {
      mode: "open";
      /** The panel password in force. */
      currentPassword: string;
      /**
       * The operator confirmed that any software on the computer can control
       * Officer once it is open.
       */
      acknowledged: true;
    }
  | {
      mode: "token";
      /** The new panel password. */
      password: string;
    };

/**
 * Switch the access mode with `PUT /access/mode`. Switching to `open` deletes
 * the panel password, both tokens and every session, and the answer clears the
 * session cookie; switching to `token` issues both tokens, and the answer sets
 * a session cookie for this browser.
 *
 * @throws ApiError `current_password_invalid` for a wrong current password,
 * `precondition` for a password the server rejects, and `conflict` for the
 * mode already in force.
 */
export async function putAccessMode(
  client: ApiClient,
  request: AccessModeRequest,
): Promise<void> {
  await client.request(`${client.baseUrl}/access/mode`, {
    method: "PUT",
    body: request,
  });
}

/** The body of `PUT /access/password`. */
export interface PasswordChangeRequest {
  /** The panel password in force. */
  currentPassword: string;
  /** The new panel password. */
  password: string;
}

/**
 * Change the panel password with `PUT /access/password`; every other session
 * ends.
 *
 * @throws ApiError `current_password_invalid` for a wrong current password,
 * `precondition` for a password the server rejects, and `conflict` in `open`
 * access.
 */
export async function putAccessPassword(
  client: ApiClient,
  request: PasswordChangeRequest,
): Promise<void> {
  await client.request(`${client.baseUrl}/access/password`, {
    method: "PUT",
    body: request,
  });
}

/**
 * End every session, this browser's included, with `DELETE /sessions`; the
 * answer clears the session cookie.
 */
export async function endAllSessions(client: ApiClient): Promise<void> {
  await client.request(`${client.baseUrl}/sessions`, { method: "DELETE" });
}

/** One of the two tokens: `agent` works only for MCP, `api` for REST. */
export type AccessTokenKind = "agent" | "api";

/** The prefix each token starts with, which secret scanners recognise. */
export const ACCESS_TOKEN_PREFIXES: Readonly<Record<AccessTokenKind, string>> =
  {
    agent: "pit_mcp_",
    api: "pit_api_",
  };

/** A token body: 32 random bytes in unpadded base64url. */
const TOKEN_BODY = /^[A-Za-z0-9_-]{43}$/;

function decodeToken(kind: AccessTokenKind, value: unknown): string {
  const response = `${kind} token`;
  if (!isRecord(value)) {
    throw invalid(response, "body", "must be an object");
  }
  const { token } = value;
  const prefix = ACCESS_TOKEN_PREFIXES[kind];
  if (
    typeof token !== "string" ||
    !token.startsWith(prefix) ||
    !TOKEN_BODY.test(token.slice(prefix.length))
  ) {
    throw invalid(
      response,
      "token",
      `must be ${prefix} and 43 base64url characters`,
    );
  }
  return token;
}

/**
 * Fetch the value of a token from `GET /access/tokens/{kind}`.
 *
 * @throws ApiError `conflict` in `open` access, which has no tokens.
 * @throws Error naming the offending field when the response is malformed.
 */
export async function fetchAccessToken(
  client: ApiClient,
  kind: AccessTokenKind,
  signal?: AbortSignal,
): Promise<string> {
  return decodeToken(
    kind,
    await client.request(`${client.baseUrl}/access/tokens/${kind}`, {
      signal,
    }),
  );
}

/**
 * Issue a new value of a token with `POST /access/tokens/{kind}/regenerate`;
 * the old value stops working at once.
 *
 * @throws ApiError `conflict` in `open` access, which has no tokens.
 * @throws Error naming the offending field when the response is malformed.
 */
export async function regenerateAccessToken(
  client: ApiClient,
  kind: AccessTokenKind,
): Promise<string> {
  return decodeToken(
    kind,
    await client.request(`${client.baseUrl}/access/tokens/${kind}/regenerate`, {
      method: "POST",
    }),
  );
}
