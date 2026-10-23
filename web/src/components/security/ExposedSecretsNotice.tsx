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

import { Notice } from "@/components/Notice";
import { ErrorState, LoadingStatus } from "@/components/PageStates";
import { useAccessTokens } from "@/components/security/AccessSection";
import { RegenerateTokenButton } from "@/components/security/RegenerateTokenButton";
import { Button } from "@/components/ui/button";
import { panelSession } from "@/lib/panelSession";

/**
 * Values that sealing re-encrypted from plain text must be considered
 * exposed: new keys go to the connection pages, and in `token` access both
 * tokens are to be regenerated, each here with the Access section's action
 * once the tokens have loaded.
 */
export function ExposedSecretsNotice({
  count,
  showTokenStatus = false,
}: {
  count: number;
  /** Show loading and retry here when AccessSection is not on the screen. */
  showTokenStatus?: boolean;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { accessMode } = panelSession();
  const { tokens, reload, regenerate } = useAccessTokens();
  return (
    <Notice
      tone="danger"
      icon={AlertTriangle}
      title={t("security.exposed.title")}
    >
      <p>{t("security.exposed.detail", { count })}</p>
      <p>{t("security.exposed.keys")}</p>
      <div className="flex flex-wrap gap-1.5 py-1">
        <Button asChild variant="outline" size="sm">
          <Link to="/trading-connections">{tc("nav.tradingConnections")}</Link>
        </Button>
        <Button asChild variant="outline" size="sm">
          <Link to="/market-data">{tc("nav.marketData")}</Link>
        </Button>
      </div>
      {accessMode === "token" && (
        <>
          <p>{t("security.exposed.tokens")}</p>
          {showTokenStatus && tokens.state === "loading" && <LoadingStatus />}
          {showTokenStatus && tokens.state === "error" && (
            <ErrorState message={tokens.error} onRetry={reload} />
          )}
          {tokens.state === "ready" && (
            <div className="flex flex-wrap gap-x-4 gap-y-1.5 py-1">
              {(["agent", "api"] as const).map((kind) => (
                <div key={kind} className="flex items-center gap-2">
                  <span>{t(`security.tokens.${kind}.label`)}</span>
                  <RegenerateTokenButton
                    kind={kind}
                    onRegenerate={() => regenerate(kind)}
                  />
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </Notice>
  );
}
