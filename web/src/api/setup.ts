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
 * The steps of the first-run setup that Officer stores, in the order the
 * wizard walks them.
 */
export const SETUP_STEPS = [
  "welcome",
  "accounts",
  "encryption",
  "signing",
  "access",
] as const;

/** A step of the first-run setup that Officer stores. */
export type SetupStep = (typeof SETUP_STEPS)[number];

/** The answer of `GET /setup`. */
export interface SetupState {
  /** The step the wizard stands on; `welcome` on a fresh database. */
  step: SetupStep;
  /**
   * When the operator chose to run without encryption, an RFC 3339
   * timestamp; null until then.
   */
  encryptionDeclinedAt: string | null;
}

/**
 * Whether `err` is the `409 setup_complete` a setup route answers once the
 * first-run setup is complete: it was finished elsewhere, and the wizard has
 * nothing left to store.
 */
export function isSetupComplete(err: unknown): boolean {
  return err instanceof ApiError && err.code === "setup_complete";
}

function isSetupStep(value: unknown): value is SetupStep {
  return (SETUP_STEPS as readonly unknown[]).includes(value);
}

function decodeSetup(value: unknown): SetupState {
  if (!isRecord(value)) {
    throw invalid("setup", "body", "must be an object");
  }
  const { step, encryptionDeclinedAt } = value;
  if (!isSetupStep(step)) {
    throw invalid(
      "setup",
      "step",
      'must be "welcome", "accounts", "encryption", "signing" or "access"',
    );
  }
  if (encryptionDeclinedAt !== null && !isRfc3339Time(encryptionDeclinedAt)) {
    throw invalid(
      "setup",
      "encryptionDeclinedAt",
      "must be null or an RFC 3339 time",
    );
  }
  return { step, encryptionDeclinedAt };
}

/**
 * Fetch the step the wizard stands on and when encryption was declined from
 * `GET /setup`.
 *
 * @throws ApiError `setup_complete` once the first-run setup is complete.
 * @throws Error naming the offending field when the response is malformed.
 */
export async function fetchSetup(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<SetupState> {
  return decodeSetup(
    await client.request(`${client.baseUrl}/setup`, { signal }),
  );
}

/**
 * Store the step the wizard stands on with `PUT /setup/progress`, so a reload
 * resumes there.
 *
 * @throws ApiError `precondition` for a step the server rejects, and
 * `setup_complete` once the first-run setup is complete.
 */
export async function putSetupProgress(
  client: ApiClient,
  step: SetupStep,
): Promise<void> {
  await client.request(`${client.baseUrl}/setup/progress`, {
    method: "PUT",
    body: { step },
  });
}

/**
 * Record that the operator chose to run without encryption with
 * `PUT /setup/encryption`; a repeat keeps the time of the first decline.
 *
 * @throws ApiError `conflict` when the database is already sealed, and
 * `setup_complete` once the first-run setup is complete.
 */
export async function declineEncryption(client: ApiClient): Promise<void> {
  await client.request(`${client.baseUrl}/setup/encryption`, {
    method: "PUT",
    body: { encryption: "none", acknowledged: true },
  });
}

/** The body of `POST /setup/complete`. */
export type SetupCompleteRequest =
  | {
      access: "token";
      /** The panel password. */
      password: string;
    }
  | {
      access: "open";
      /**
       * The operator confirmed that any software on the computer can control
       * Officer once it is open.
       */
      acknowledged: true;
    };

/**
 * Finish the first-run setup under the chosen access mode with
 * `POST /setup/complete`. In `token` access it issues both tokens, and the
 * answer sets a session cookie for this browser.
 *
 * @throws ApiError `precondition` for a password the server rejects,
 * `conflict` with a message naming the decision still missing, the account
 * mode or the encryption, and `setup_complete` once the first-run setup is
 * complete.
 */
export async function completeSetup(
  client: ApiClient,
  request: SetupCompleteRequest,
): Promise<void> {
  await client.request(`${client.baseUrl}/setup/complete`, {
    method: "POST",
    body: request,
  });
}
