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
import { Link } from "react-router-dom";

import type { SpotFundsLimitModeState } from "@/api/spotFundsLimitMode";
import type { PollingResult } from "@/api/usePolling";
import { Notice } from "@/components/Notice";
import { ErrorState, StaleState } from "@/components/PageStates";
import { accountScope } from "@/lib/accountScope";

/**
 * Warns that P&L bounds block accounts that have no currency while the funds
 * limit mode allows orders with a funds shortfall to proceed. It renders
 * nothing while loading, in enforce mode or when every visible account
 * has a currency. A failed poll stays visible.
 */
export function SpotFundsCurrencyWarning({
  poll: { load, reload },
}: {
  poll: PollingResult<SpotFundsLimitModeState>;
}) {
  const { t } = useTranslation("policies");
  const { showsAccounts } = accountScope();

  if (load.state === "loading") {
    return null;
  }
  if (load.state === "error") {
    return (
      <ErrorState
        title={t("spotFundsMode.loadError")}
        message={load.error}
        onRetry={reload}
      />
    );
  }
  const { mode, accountsWithoutCurrency } = load.data;
  return (
    <>
      <StaleState load={load} reload={reload} />
      {mode !== "enforce" && accountsWithoutCurrency.length > 0 && (
        <Notice
          tone="warn"
          icon={AlertTriangle}
          title={t("spotFundsMode.withoutCurrency.title")}
        >
          <p>{t("spotFundsMode.withoutCurrency.detail")}</p>
          <ul className="list-disc pl-4">
            {accountsWithoutCurrency.map((code) => (
              <li key={code}>
                <Link
                  to={
                    showsAccounts
                      ? `/accounts?code=${encodeURIComponent(code)}&codeMatch=exact`
                      : "/accounts"
                  }
                  className="underline underline-offset-4"
                >
                  {code}
                </Link>
              </li>
            ))}
          </ul>
        </Notice>
      )}
    </>
  );
}
