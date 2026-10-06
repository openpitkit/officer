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

import { useTrading } from "@/api/useTrading";

/** Report unsealed sensitive data and share the polled trading load. */
// eslint-disable-next-line react-refresh/only-export-components -- Shared hook.
export function useUnencryptedSensitiveData() {
  const { load: trading } = useTrading();
  const unencrypted =
    trading.state === "ready" &&
    !trading.data.credentialsSealed &&
    trading.data.connections.length > 0;
  return { unencrypted, trading };
}

export function SensitiveDataBanner() {
  const { t } = useTranslation();
  const { unencrypted } = useUnencryptedSensitiveData();

  if (!unencrypted) return null;

  return (
    <div className="flex flex-wrap items-start gap-3 rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] px-3 py-2 text-sm text-[var(--danger)]">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0 flex-1">
        <p className="font-semibold">{t("sensitiveData.title")}</p>
        <p className="text-xs">{t("sensitiveData.detail")}</p>
        <a
          href="https://github.com/openpitkit/officer/blob/main/docs/configuration.md#master-key"
          target="_blank"
          rel="noopener noreferrer"
          className="text-xs underline"
        >
          {t("sensitiveData.masterKeyLink")}
        </a>
      </div>
    </div>
  );
}
