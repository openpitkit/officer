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

import type { ComponentProps } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { ErrorBanner } from "@/components/PageStates";
import type { ActionableAlert, AlertRenderer } from "./registry";

function accountName(alert: ActionableAlert): string | null {
  const payload = alert.payload;
  if (
    typeof payload !== "object" ||
    payload === null ||
    !("account" in payload) ||
    typeof payload.account !== "string" ||
    !payload.account ||
    !("title" in payload) ||
    typeof payload.title !== "string"
  )
    return null;
  return payload.title
    ? `${payload.title} (${payload.account})`
    : payload.account;
}

export function Summary({ alert }: { alert: ActionableAlert }) {
  const { t } = useTranslation("alerts");
  const account = accountName(alert);
  return account === null
    ? t("mixedTradingAccess.invalid")
    : t("mixedTradingAccess.title", { account });
}

export function Detail({
  alert,
  pending,
  resolve,
}: ComponentProps<AlertRenderer["Detail"]>) {
  const { t } = useTranslation("alerts");
  const account = accountName(alert);
  if (account === null)
    return <ErrorBanner message={t("mixedTradingAccess.invalid")} />;
  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm">{t("mixedTradingAccess.detail", { account })}</p>
      {alert.actions.includes("acknowledge") && (
        <Button
          size="sm"
          disabled={pending}
          onClick={() => void resolve("acknowledge", {})}
        >
          {t("mixedTradingAccess.acknowledge")}
        </Button>
      )}
    </div>
  );
}
