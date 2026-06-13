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
import { Link } from "react-router-dom";

import type { ServiceInfo } from "@/api/types";
import { useService } from "@/api/useService";
import { useMcpAccess } from "@/api/useMcpAccess";
import { CopyableSnippet } from "@/components/CopyableSnippet";
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
import { buildMcpAgentPrompt } from "@/lib/mcpAgentPrompt";

function ServiceCard({ info }: { info: ServiceInfo }) {
  const { load: mcpLoad } = useMcpAccess();
  const mcpCommands = mcpLoad.state === "ready" ? mcpLoad.data : null;

  return (
    <div className="space-y-4 animate-fade-in">
      <Card>
        <CardHeader>
          <CardTitle>Application</CardTitle>
        </CardHeader>
        <CardContent>
          <StatRow label="Name" value={info.name || "Pit Officer"} mono={false} />
          {!info.release && (
            <StatRow
              label="Build"
              value={
                <Badge variant="warn">non-release</Badge>
              }
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Engine</CardTitle>
        </CardHeader>
        <CardContent>
          <StatRow
            label="Version"
            value={info.engineVersion || "—"}
          />
          <StatRow
            label="Profile"
            value={
              info.engineBuildProfile ? (
                <span className="flex items-center gap-1.5">
                  {info.engineBuildProfile}
                  {!info.release && (
                    <Badge variant="warn">non-release</Badge>
                  )}
                </span>
              ) : (
                "—"
              )
            }
            mono={false}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between gap-2">
          <CardTitle>Database</CardTitle>
          <Badge variant={info.database.reachable ? "ok" : "danger"}>
            <StatusDot
              tone={info.database.reachable ? "ok" : "danger"}
              pulse={info.database.reachable}
            />
            {info.database.reachable ? "reachable" : "unreachable"}
          </Badge>
        </CardHeader>
        <CardContent>
          <StatRow
            label="Reachable"
            value={info.database.reachable ? "yes" : "no"}
          />
          <StatRow
            label="Path"
            value={info.database.path || "—"}
            mono={false}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>API</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* REST API */}
          <div>
            <p className="mb-1.5 text-xs font-medium text-muted">REST API</p>
            <div className="flex items-center justify-between gap-4 py-1">
              <span className="text-xs text-muted-lt">
                OpenAPI / Swagger docs
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
              <p className="text-xs font-medium text-muted">MCP API</p>
              <div className="flex items-center gap-2">
                <span className="nums text-[0.6875rem] text-muted-lt">/mcp</span>
                <Link
                  to="/mcp-access"
                  className="text-[0.6875rem] text-accent hover:underline"
                >
                  Configure access
                </Link>
              </div>
            </div>
            {mcpCommands === null ? (
              <p className="text-xs text-muted-lt italic">Loading commands…</p>
            ) : mcpCommands.length === 0 ? (
              <p className="text-xs text-muted-lt italic">No commands defined.</p>
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
                        {cmd.agentDescription || "—"}
                      </span>
                    </div>
                  ))}
                </div>
                <CopyableSnippet
                  label="Agent instructions"
                  text={buildMcpAgentPrompt(mcpCommands)}
                  rows={4}
                />
              </div>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

export function Service() {
  const { load, reload } = useService();
  const refreshing = load.state === "loading";

  return (
    <Page
      title="Service"
      actions={
        <Button
          variant="ghost"
          size="sm"
          onClick={reload}
          disabled={refreshing}
          aria-label="Refresh service info"
        >
          {refreshing ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <RefreshCw className="h-3.5 w-3.5" />
          )}
          Refresh
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
