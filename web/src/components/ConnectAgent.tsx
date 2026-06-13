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

import { Download } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { CopyableSnippet } from "@/components/CopyableSnippet";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  chatgptInstructions,
  claudeDesktopConfig,
  claudeMcpAddCommand,
  claudeSkillMarkdown,
} from "@/lib/agentArtifacts";
import { buildMcpAgentPrompt } from "@/lib/mcpAgentPrompt";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Live MCP endpoint URL from the running origin; guards SSR-less env. */
function mcpUrl(): string {
  const origin =
    typeof window !== "undefined" && window.location
      ? window.location.origin
      : "";
  return `${origin}/mcp`;
}

/** Trigger a client-side download of `text` as `filename` (no new deps). */
function downloadText(filename: string, text: string, mime: string) {
  const blob = new Blob([text], { type: mime });
  const href = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = href;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(href);
}

// ---------------------------------------------------------------------------
// Connect-your-agent block
// ---------------------------------------------------------------------------

type TabId = "claude" | "chatgpt" | "prompt";

/** Tabbed connect block shared by the MCP-access and Service pages. */
export function ConnectAgent() {
  const { t } = useTranslation("mcp");
  const [tab, setTab] = useState<TabId>("claude");
  const url = mcpUrl();

  return (
    <Card className="space-y-3 p-4">
      <div className="space-y-1">
        <p className="text-sm font-medium text-text">{t("connect.title")}</p>
        <p className="text-xs text-muted-lt">{t("connect.subtitle")}</p>
      </div>

      {/* Tab toggle — mirrors the Orders page pattern. */}
      <div className="flex w-fit gap-1 rounded-card border border-border bg-bg p-1">
        {(["claude", "chatgpt", "prompt"] as TabId[]).map((tabId) => (
          <button
            key={tabId}
            type="button"
            onClick={() => setTab(tabId)}
            className={[
              "rounded-[4px] px-3 py-1 text-xs font-medium transition-colors",
              tab === tabId
                ? "bg-surface text-text shadow-sm"
                : "text-muted-lt hover:text-text",
            ].join(" ")}
          >
            {t(`connect.tab.${tabId}`)}
          </button>
        ))}
      </div>

      {tab === "claude" && (
        <div className="space-y-3">
          <CopyableSnippet
            label={t("connect.claude.cliLabel")}
            text={claudeMcpAddCommand(url)}
            rows={2}
          />
          <CopyableSnippet
            label={t("connect.claude.desktopLabel")}
            text={claudeDesktopConfig(url)}
            rows={8}
          />
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs text-muted-lt">{t("connect.claude.skillHint")}</p>
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                downloadText(
                  "SKILL.md",
                  claudeSkillMarkdown(url),
                  "text/markdown",
                )
              }
            >
              <Download className="h-3.5 w-3.5" />
              {t("connect.claude.skillButton")}
            </Button>
          </div>
        </div>
      )}

      {tab === "chatgpt" && (
        <div className="space-y-3">
          <CopyableSnippet
            label={t("connect.chatgpt.urlLabel")}
            text={url}
            rows={2}
          />
          <ol className="list-decimal space-y-1 pl-5 text-xs text-muted-lt">
            <li>{t("connect.chatgpt.step1")}</li>
            <li>{t("connect.chatgpt.step2")}</li>
            <li>{t("connect.chatgpt.step3")}</li>
          </ol>
          <p className="rounded-card border border-[var(--warn)] bg-accent-dim px-3 py-2 text-xs text-muted-lt">
            {t("connect.chatgpt.publicUrlCaveat")}
          </p>
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs text-muted-lt">
              {t("connect.chatgpt.instructionsHint")}
            </p>
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                downloadText(
                  "pit-officer-gpt-instructions.md",
                  chatgptInstructions(url),
                  "text/markdown",
                )
              }
            >
              <Download className="h-3.5 w-3.5" />
              {t("connect.chatgpt.instructionsButton")}
            </Button>
          </div>
        </div>
      )}

      {tab === "prompt" && (
        <CopyableSnippet
          label={t("connect.prompt.label")}
          text={buildMcpAgentPrompt()}
          rows={10}
        />
      )}
    </Card>
  );
}
