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

import type { CredentialStorage } from "@/api/types";
import { ApiError, type ApiClient } from "@/framework";
import { normalizeCredentialStorage } from "@/framework/api/officerApi";
import { isRfc3339Time } from "@/framework/api/rfc3339";

/** Read Officer's credential storage flags and seal report. */
export async function fetchCredentialStorage(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<CredentialStorage> {
  const value = await client.request(`${client.baseUrl}/credential-storage`, {
    signal,
  });
  const base = normalizeCredentialStorage(value);
  const invalid = (field: "sealedAt" | "resealedSecrets") =>
    new ApiError(
      `Invalid or missing credential storage ${field} in the API response`,
      "internal",
    );
  const { sealedAt, resealedSecrets } = value as Record<string, unknown>;
  if (sealedAt !== null && !isRfc3339Time(sealedAt)) {
    throw invalid("sealedAt");
  }
  if (
    typeof resealedSecrets !== "number" ||
    !Number.isSafeInteger(resealedSecrets) ||
    resealedSecrets < 0
  ) {
    throw invalid("resealedSecrets");
  }
  return { ...base, sealedAt, resealedSecrets };
}
