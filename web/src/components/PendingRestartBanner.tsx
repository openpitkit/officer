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

import { Info, RotateCcw } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { useMarketData } from "@/api/useMarketData";
import { CompactStaleState } from "@/components/PageStates";
import { Button } from "@/components/ui/button";
import { useOfficerApi } from "@/framework";

export function PendingRestartBanner() {
  const { t } = useTranslation("marketData");
  const { load, reload } = useMarketData();
  const { restartMarketData } = useOfficerApi();
  const [busy, setBusy] = useState(false);

  if (load.state !== "ready" || !load.data.restartRequired) {
    return null;
  }

  const restart = async () => {
    setBusy(true);
    try {
      await restartMarketData();
      reload();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-3 rounded-card border border-[var(--warn)] bg-[var(--warn-dim)] px-3 py-2 text-sm text-text">
      <Info className="h-4 w-4 shrink-0 text-[var(--warn)]" />
      <div className="min-w-0 flex-1">
        <p className="font-semibold text-text">{t("restart.requiredTitle")}</p>
        <p className="text-xs text-muted-lt">{t("restart.requiredDetail")}</p>
        <CompactStaleState load={load} />
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => {
          void restart();
        }}
        disabled={busy}
      >
        <RotateCcw />
        {t("restart.button")}
      </Button>
    </div>
  );
}
