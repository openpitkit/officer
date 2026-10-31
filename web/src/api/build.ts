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
import type { ApiClient } from "@/framework";

/** The build gate's status before and after its database is ready. */
export type BuildStatus = {
  release: boolean;
  releaseDatabaseExists: boolean;
} & (
  | { phase: "choice" | "preparing" | "ready" }
  | { phase: "failed"; error: string }
);

function decodeBuild(value: unknown): BuildStatus {
  if (!isRecord(value)) {
    throw invalid("build", "body", "must be an object");
  }
  const { release, phase, releaseDatabaseExists } = value;
  if (typeof release !== "boolean") {
    throw invalid("build", "release", "must be a boolean");
  }
  if (typeof releaseDatabaseExists !== "boolean") {
    throw invalid("build", "releaseDatabaseExists", "must be a boolean");
  }
  if (
    phase !== "choice" &&
    phase !== "preparing" &&
    phase !== "ready" &&
    phase !== "failed"
  ) {
    throw invalid(
      "build",
      "phase",
      'must be "choice", "preparing", "failed" or "ready"',
    );
  }
  if (phase === "failed") {
    if (typeof value.error !== "string" || value.error.length === 0) {
      throw invalid("build", "error", "must be a non-empty string in failed");
    }
    return { release, phase, releaseDatabaseExists, error: value.error };
  }
  if ("error" in value) {
    throw invalid("build", "error", "must be absent outside failed");
  }
  return { release, phase, releaseDatabaseExists };
}

/** Fetch and strictly decode `GET /build`. */
export async function fetchBuild(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<BuildStatus> {
  return decodeBuild(
    await client.request(`${client.baseUrl}/build`, { signal }),
  );
}

/** Confirm a database choice and decode the accepted build status. */
export async function chooseDatabase(
  client: ApiClient,
  fromRelease: boolean,
): Promise<BuildStatus> {
  return decodeBuild(
    await client.request(`${client.baseUrl}/build/database`, {
      method: "POST",
      body: { fromRelease },
    }),
  );
}
