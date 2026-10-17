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

import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { CredentialStorage } from "@/api/types";
import type { LoadState } from "@/api/usePolling";
import { ErrorBanner } from "@/components/PageStates";

export type CredentialStorageCheckProps = {
  /** Installation credential storage load that decides what renders. */
  storage: LoadState<CredentialStorage>;
  /** The caller's notice, shown only once the storage is known unsealed. */
  children: ReactNode;
};

/** Shows the caller's notice when stored credentials are not sealed, and
 *  nothing when they are. Until the storage load settles, a quiet status says
 *  the check is running; a failed load is shown as the error, since the caller
 *  then cannot tell whether the keys will be sealed. */
export function CredentialStorageCheck({
  storage,
  children,
}: CredentialStorageCheckProps) {
  const { t } = useTranslation();
  if (storage.state === "error")
    return (
      <ErrorBanner
        message={t("sensitiveData.unknown", { error: storage.error })}
      />
    );
  if (storage.state === "loading")
    return (
      <p role="status" className="text-xs text-muted">
        {t("sensitiveData.checking")}
      </p>
    );
  if (storage.data.credentialsSealed) return null;
  return children;
}
