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
import type { LoadState } from "@/api/usePolling";

/**
 * A loaded credential storage, sealed or not, with no plain-text keys; the
 * overrides set the refresh error it is stale under and the count of secrets
 * sealing re-encrypted from plain text.
 */
export function storage(
  credentialsSealed: boolean,
  {
    error = null,
    resealedSecrets = 0,
  }: { error?: string | null; resealedSecrets?: number } = {},
): LoadState<CredentialStorage> {
  return {
    state: "ready",
    data: {
      credentialsSealed,
      plaintextTradingKeys: false,
      plaintextMarketDataKeys: false,
      sealedAt: null,
      resealedSecrets,
    },
    error,
  };
}
