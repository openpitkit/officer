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
import type { ComponentProps } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { ErrorBanner } from "@/components/PageStates";
import type { ActionableAlert, AlertRenderer } from "./registry";

function reviewPayload(alert: ActionableAlert) {
  const payload = alert.payload;
  if (
    typeof payload !== "object" ||
    payload === null ||
    !("order" in payload) ||
    typeof payload.order !== "string" ||
    !payload.order ||
    !("account" in payload) ||
    typeof payload.account !== "string" ||
    !payload.account ||
    !("markCode" in payload) ||
    typeof payload.markCode !== "string" ||
    !payload.markCode
  ) {
    return null;
  }
  return {
    order: payload.order,
    account: payload.account,
    markCode: payload.markCode,
  };
}

// eslint-disable-next-line react-refresh/only-export-components -- The summary shares payload validation with the detail.
export function summary(alert: ActionableAlert, t: TFunction): string {
  const payload = reviewPayload(alert);
  return payload === null
    ? t("alerts:orderAwaitingReview.invalid")
    : t("alerts:orderAwaitingReview.title", payload);
}

export function Detail({
  alert,
  pending,
  resolve,
}: ComponentProps<AlertRenderer["Detail"]>) {
  const { t } = useTranslation("alerts");
  const navigate = useNavigate();
  const payload = reviewPayload(alert);
  if (payload === null) {
    return <ErrorBanner message={t("orderAwaitingReview.invalid")} />;
  }
  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm">{t("orderAwaitingReview.detail", payload)}</p>
      <div className="flex gap-2">
        <Button
          size="sm"
          onClick={() =>
            navigate(`/orders?order=${encodeURIComponent(payload.order)}`)
          }
        >
          {t("orderAwaitingReview.openOrder")}
        </Button>
        {alert.actions.includes("close") && (
          <Button
            size="sm"
            disabled={pending}
            onClick={() => void resolve("close", {})}
          >
            {t("common:actions.close")}
          </Button>
        )}
      </div>
    </div>
  );
}
