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

import { ExternalLink, Loader2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import type { ServiceInfo } from "@/api/types";
import { useService } from "@/api/useService";
import { useMcpAccess } from "@/api/useMcpAccess";
import { ConnectAgent } from "@/components/ConnectAgent";
import { ErrorState } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { StatRow } from "@/components/StatRow";
import { StatusDot } from "@/components/StatusDot";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

// This page is the canonical i18n pattern (phase 1 pilot): user-facing text
// goes through useTranslation with a per-area "service" namespace; shared
// chrome (placeholders, generic verbs) comes from "common". Subsequent pages
// copy this shape. Domain identifiers and route paths stay literal.

/** One row in the build-profile parameter table. */
interface ProfileRow {
  param: string;
  value: string;
}

/**
 * Parse the engine build-profile string into parameter rows.
 *
 * The C ABI documents the value as a stable `key=value;`-delimited string
 * (keys: version, profile, opt_level, debug_assertions, target, target_cpu,
 * lto). Tolerant: any unexpected shape falls back to a single raw-value row.
 */
function parseProfileRows(raw: string): ProfileRow[] {
  if (!raw) {
    return [];
  }
  // Split on semicolons and parse each non-empty token as "key=value".
  const rows: ProfileRow[] = [];
  for (const token of raw.split(";")) {
    const trimmed = token.trim();
    if (!trimmed) {
      continue;
    }
    const eq = trimmed.indexOf("=");
    if (eq > 0) {
      rows.push({ param: trimmed.slice(0, eq), value: trimmed.slice(eq + 1) });
    } else {
      // Unexpected shape: surface the whole token as a raw value.
      rows.push({ param: trimmed, value: "" });
    }
  }
  // Fallback: if nothing parsed, treat the whole string as a single scalar.
  if (rows.length === 0) {
    return [{ param: raw, value: "" }];
  }
  return rows;
}

function ServiceCard({ info }: { info: ServiceInfo }) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { load: mcpLoad } = useMcpAccess();
  const mcpCommands = mcpLoad.state === "ready" ? mcpLoad.data : null;

  return (
    <div className="space-y-4 animate-fade-in">
      <Card>
        <CardHeader>
          <CardTitle>{t("application.title")}</CardTitle>
        </CardHeader>
        <CardContent>
          <StatRow
            label={t("application.name")}
            value={info.name || t("application.defaultName")}
            mono={false}
          />
          {!info.release && (
            <StatRow
              label={t("application.build")}
              value={
                <Badge variant="warn">{t("application.nonRelease")}</Badge>
              }
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("engine.title")}</CardTitle>
        </CardHeader>
        <CardContent>
          <StatRow
            label={t("engine.version")}
            value={info.engineVersion || tc("value.none")}
          />
          <StatRow
            label={t("engine.buildPosture")}
            mono={false}
            value={
              <span className="flex items-center gap-1.5">
                <Badge variant={info.release ? "ok" : "warn"}>
                  {info.release
                    ? t("engine.stableRelease")
                    : t("engine.developmentBuild")}
                </Badge>
                {/* buildClean is absent until the SDK exposes a precise
                    dirty-sources flag; when it arrives and is false, surface it. */}
                {info.buildClean === false && (
                  <Badge variant="warn">{t("engine.modifiedSources")}</Badge>
                )}
              </span>
            }
          />
          {info.engineBuildProfile ? (
            <>
              <div className="pb-1 pt-3">
                <p className="text-xs font-medium text-muted">
                  {t("engine.profile")}
                </p>
              </div>
              {parseProfileRows(info.engineBuildProfile).map((row) => (
                <StatRow
                  key={row.param}
                  label={
                    t(`engine.profileParams.${row.param}`, {
                      defaultValue: row.param,
                    })
                  }
                  value={row.value || tc("value.none")}
                  mono={false}
                />
              ))}
            </>
          ) : (
            <StatRow
              label={t("engine.profile")}
              value={tc("value.none")}
              mono={false}
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between gap-2">
          <CardTitle>{t("database.title")}</CardTitle>
          <Badge variant={info.database.reachable ? "ok" : "danger"}>
            <StatusDot
              tone={info.database.reachable ? "ok" : "danger"}
              pulse={info.database.reachable}
            />
            {info.database.reachable
              ? t("database.reachable")
              : t("database.unreachable")}
          </Badge>
        </CardHeader>
        <CardContent>
          <StatRow
            label={t("database.reachableLabel")}
            value={info.database.reachable ? tc("value.yes") : tc("value.no")}
          />
          <StatRow
            label={t("database.path")}
            value={info.database.path || tc("value.none")}
            mono={false}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("api.title")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* REST API */}
          <div>
            <p className="mb-1.5 text-xs font-medium text-muted">
              {t("api.rest")}
            </p>
            <div className="flex items-center justify-between gap-4 py-1">
              <span className="text-xs text-muted-lt">
                {t("api.openApiDocs")}
              </span>
              <a
                href="/docs"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 text-sm text-accent hover:underline"
              >
                /docs
                <ExternalLink className="h-3 w-3 opacity-70" />
              </a>
            </div>
          </div>

          {/* MCP API */}
          <div>
            <div className="mb-1.5 flex items-center justify-between gap-2">
              <p className="text-xs font-medium text-muted">{t("api.mcp")}</p>
              <div className="flex items-center gap-2">
                <span className="nums text-[0.6875rem] text-muted-lt">/mcp</span>
                <Link
                  to="/mcp-access"
                  className="text-[0.6875rem] text-accent hover:underline"
                >
                  {t("api.configureAccess")}
                </Link>
              </div>
            </div>
            {mcpCommands === null ? (
              <p className="text-xs text-muted-lt italic">
                {t("api.loadingCommands")}
              </p>
            ) : mcpCommands.length === 0 ? (
              <p className="text-xs text-muted-lt italic">
                {t("api.noCommands")}
              </p>
            ) : (
              <div className="space-y-3">
                <div className="space-y-1">
                  {mcpCommands.map((cmd) => (
                    <div
                      key={cmd.name}
                      className="flex items-start gap-2 text-xs"
                    >
                      <span
                        className={
                          cmd.protective
                            ? "nums shrink-0 font-medium text-[var(--danger)]"
                            : "nums shrink-0 font-medium text-text"
                        }
                      >
                        {cmd.name}
                      </span>
                      <span className="text-muted-lt">
                        {cmd.agentDescription || tc("value.none")}
                      </span>
                    </div>
                  ))}
                </div>
                <ConnectAgent />
              </div>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

export function Service() {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { load, reload } = useService();
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
      {load.state === "loading" && (
        <div className="space-y-4">
          {[0, 1, 2].map((i) => (
            <Card key={i} className="animate-pulse">
              <CardHeader>
                <div className="h-3 w-20 rounded bg-border" />
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="h-3 w-full rounded bg-border" />
                <div className="h-3 w-4/5 rounded bg-border" />
              </CardContent>
            </Card>
          ))}
        </div>
      )}
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" && <ServiceCard info={load.data} />}
    </Page>
  );
}
