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

import type { TFunction } from "i18next";

import type { CredentialStorage, SigningKey } from "@/api/types";
import { formatDateTime } from "@/i18n/format";

// The values Service › Security names its storage facts with, for every place
// that shows one of those facts.

/**
 * Whether stored credentials are sealed under a master key, and since when
 * once sealing re-encrypted them.
 */
export function encryptionValue(
  t: TFunction<"service">,
  storage: CredentialStorage,
): string {
  if (!storage.credentialsSealed) {
    return t("security.storage.plainText");
  }
  if (storage.sealedAt === null) {
    return t("security.storage.encryptionOn");
  }
  return t("security.storage.sealed", {
    date: formatDateTime(storage.sealedAt),
  });
}

/**
 * Whether Officer signs its decisions: off by the setting, or on with the
 * active key that signs them, or on with no key to sign with.
 */
export function signingValue(
  t: TFunction<"service">,
  eSignEnabled: boolean,
  activeKey: SigningKey | null,
): string {
  if (!eSignEnabled) {
    return t("security.storage.signingOff");
  }
  if (activeKey === null) {
    return t("security.storage.signingWithoutKey");
  }
  return t("security.storage.signingOn", { key: activeKey.fingerprint });
}

/** The account mode: several accounts, or the one account Officer acts on. */
export function accountModeValue(
  t: TFunction<"service">,
  multi: boolean,
  account: string,
): string {
  return multi
    ? t("accountMode.modes.multi")
    : t("security.storage.oneAccount", { account });
}
