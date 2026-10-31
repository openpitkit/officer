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

import type {
  TradingAccessSubject,
  TradingConnection,
  TradingProvider,
} from "@/api/types";

/** Why a grant is blocked in the panel. Each value is also its sentence key
 *  under `accessBlocked` in the `venues` namespace. */
export type AccessGrantBlock =
  | "needsVenueAccount"
  | "groupNeedsFeeAccount"
  | "secondAccountNeedsFeeAccount";

/** Reports why granting `subject` access to `connection` would be refused for
 *  a missing venue or fee account, or null when nothing blocks the grant.
 *
 *  The venue-account reason mirrors the runtime send rule: PutTradingAccess
 *  accepts an unbound grant, but it cannot carry an order for such a provider.
 *
 *  Mirrors the server rule (PutTradingAccess): only a provider with unlinked
 *  fees needs a fee account, and only while the connection has none. Then a
 *  group is always refused, since it can hold several accounts, and an account
 *  is refused when the connection already has grants and none of them is that
 *  account's own, so that re-granting the same account stays allowed.
 *
 *  An account subject carries a non-empty code; a group subject's code is not
 *  read, so a caller may ask before the group is named. */
export function accessGrantBlock(
  provider: Pick<TradingProvider, "unlinkedFees" | "venueAccounts">,
  connection: Pick<TradingConnection, "feeAccount" | "access">,
  subject: TradingAccessSubject & { venueAccount?: string },
): AccessGrantBlock | null {
  if (provider.venueAccounts && !subject.venueAccount?.trim()) {
    return "needsVenueAccount";
  }
  if (!provider.unlinkedFees || connection.feeAccount !== "") {
    return null;
  }
  if (subject.account === undefined) {
    return "groupNeedsFeeAccount";
  }
  const account = subject.account;
  if (
    connection.access.length > 0 &&
    !connection.access.some((grant) => grant.account === account)
  ) {
    return "secondAccountNeedsFeeAccount";
  }
  return null;
}
