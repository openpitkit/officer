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

import { AlertTriangle } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { CredentialStorage } from "@/api/types";
import type { LoadState } from "@/api/usePolling";
import { MasterKeyLink } from "@/components/MasterKeyLink";
import { Notice } from "@/components/Notice";
import { ErrorBanner } from "@/components/PageStates";

export type UnsealedKeysWarningProps = {
  /** Installation credential storage load that decides the warning. */
  storage: LoadState<CredentialStorage>;
};

/** Warns about unsealed credentials without restricting key entry. Until the
 *  storage load settles, a quiet status says the check is running; a failed
 *  load is shown as the error, since the dialog then cannot tell whether the
 *  keys will be sealed. */
export function UnsealedKeysWarning({ storage }: UnsealedKeysWarningProps) {
  const { t } = useTranslation("venues");
  const { t: tc } = useTranslation();
  if (storage.state === "error")
    return (
      <ErrorBanner
        message={tc("sensitiveData.unknown", { error: storage.error })}
      />
    );
  if (storage.state === "loading")
    return (
      <p role="status" className="text-xs text-muted">
        {tc("sensitiveData.checking")}
      </p>
    );
  if (storage.data.credentialsSealed) return null;
  return (
    <Notice
      tone="danger"
      icon={AlertTriangle}
      title={t("unsealed.title")}
      role="alert"
    >
      <p>{t("unsealed.description")}</p>
      <p>
        <MasterKeyLink />
      </p>
      <p>{t("unsealed.caution")}</p>
    </Notice>
  );
}
