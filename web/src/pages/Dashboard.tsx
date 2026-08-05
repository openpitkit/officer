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
// Please see https://openpit.dev and the OWNERS file for details.

import { Loader2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useOverview } from "@/api/useOverview";
import { Page } from "@/components/Page";
import { ErrorState, TableSkeleton } from "@/components/PageStates";
import { Button } from "@/components/ui/button";
import { DashboardWidgets } from "@/framework";
import type { DashboardReadyWidgetProps } from "@/pages/dashboard/widgets";

const READY_WIDGET_IDS = [
  "counts-row",
  "mcp-access-card",
  "market-data-card",
  "activity-columns",
];
const AUDIT_WIDGET_IDS = ["audit-strip"];

export function Dashboard() {
  const { t } = useTranslation("dashboard");
  const { t: tc } = useTranslation();
  const { load, reload } = useOverview();
  const refreshing = load.state === "loading";

  return (
    <Page
      title={t("title")}
      actions={
        <Button
          variant="ghost"
          size="sm"
          onClick={reload}
          disabled={refreshing}
          aria-label={t("refreshAriaLabel")}
        >
          {refreshing ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <RefreshCw className="h-3.5 w-3.5" />
          )}
          {tc("actions.refresh")}
        </Button>
      }
    >
      {load.state === "loading" && <TableSkeleton rows={3} cols={3} />}
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" && (
        <>
          <DashboardWidgets<DashboardReadyWidgetProps>
            ids={READY_WIDGET_IDS}
            props={{
              counts: load.data.counts,
              activity: load.data.activity,
            }}
          />
          <DashboardWidgets ids={AUDIT_WIDGET_IDS} />
        </>
      )}
    </Page>
  );
}
