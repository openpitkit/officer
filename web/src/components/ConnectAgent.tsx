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

import { Download, ShieldOff } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { fetchAccessToken, isAccessModeConflict } from "@/api/access";
import type { LoadState } from "@/api/usePolling";
import { errMessage } from "@/components/AccountDialogs";
import { CopyButton } from "@/components/CopyButton";
import { CopyableSnippet } from "@/components/CopyableSnippet";
import { ErrorBanner, LoadingStatus } from "@/components/PageStates";
import { RevealButton } from "@/components/RevealButton";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useApiClient } from "@/framework";
import { maskAccessToken } from "@/lib/accessTokenMask";
import {
  chatgptInstructions,
  claudeDesktopConfig,
  claudeMcpAddCommand,
  claudeSkillMarkdown,
  codexConfig,
  cursorConfig,
} from "@/lib/agentArtifacts";
import { buildMcpAgentPrompt } from "@/lib/mcpAgentPrompt";
import { checkSession, panelSession } from "@/lib/panelSession";

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

/** Textarea rows that fit `text`'s lines with one to spare for wrapping. */
function rowsFor(text: string): number {
  return text.split("\n").length + 1;
}

const LOADING = { state: "loading", data: null, error: null } as const;

/**
 * The agent token in `token` access, fetched once per mount and kept only in
 * this state; `open` access has no token and makes no request.
 */
function useAgentToken(withToken: boolean): LoadState<string> {
  const client = useApiClient();
  const [token, setToken] = useState<LoadState<string>>(LOADING);

  useEffect(() => {
    if (!withToken) {
      return undefined;
    }
    const controller = new AbortController();
    fetchAccessToken(client, "agent", controller.signal).then(
      (value) => {
        if (!controller.signal.aborted) {
          setToken({ state: "ready", data: value, error: null });
        }
      },
      (err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        if (isAccessModeConflict(err)) {
          checkSession();
          return;
        }
        setToken({ state: "error", data: null, error: errMessage(err) });
      },
    );
    return () => controller.abort();
  }, [client, withToken]);

  return token;
}

/**
 * The token the connect snippets carry: `shown` goes on screen, masked until
 * Show, and `copied` is the real value Copy writes; both null in `open`.
 */
interface SnippetToken {
  shown: string | null;
  copied: string | null;
}

// ---------------------------------------------------------------------------
// Connect-your-agent block
// ---------------------------------------------------------------------------

type TabId = "claude" | "codex" | "cursor" | "chatgpt" | "prompt";

const TABS: readonly TabId[] = [
  "claude",
  "codex",
  "cursor",
  "chatgpt",
  "prompt",
];

/** One labelled value of the address and token row, with its actions. */
function RowField({
  label,
  value,
  actions,
}: {
  label: string;
  value: string;
  actions: ReactNode;
}) {
  return (
    <div role="group" aria-label={label} className="min-w-0 space-y-1.5">
      <p className="text-xs text-muted">{label}</p>
      <div className="flex items-center gap-2">
        <p className="min-w-0 flex-1 break-all rounded-card border border-border bg-surface-2 px-2 py-1.5 font-mono text-[0.6875rem] text-text">
          {value}
        </p>
        {actions}
      </div>
    </div>
  );
}

/**
 * Tabbed connect block shared by the MCP-access and Service pages. In `token`
 * access it shows the MCP address and the agent token and puts the token in
 * every connect command and configuration; in `open` access it says, in one
 * line, that no token is needed.
 */
export function ConnectAgent() {
  const { t } = useTranslation("mcp");
  const [tab, setTab] = useState<TabId>("claude");
  const [shown, setShown] = useState(false);
  const withToken = panelSession().accessMode === "token";
  const token = useAgentToken(withToken);
  const url = mcpUrl();

  // In `token` access nothing that carries the token renders before it has
  // loaded: `snippetToken` stays null until then.
  let snippetToken: SnippetToken | null = null;
  let access: ReactNode;
  if (!withToken) {
    snippetToken = { shown: null, copied: null };
    access = (
      <p className="flex items-start gap-2 text-xs text-muted-lt">
        <ShieldOff aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <span>
          {t("connect.openNote")}{" "}
          <Link to="/service#security" className="underline underline-offset-4">
            {t("connect.openLink")}
          </Link>
        </span>
      </p>
    );
  } else if (token.state === "loading") {
    access = (
      <div className="py-3">
        <LoadingStatus />
      </div>
    );
  } else if (token.state === "error") {
    access = <ErrorBanner message={token.error} />;
  } else {
    const shownToken = shown
      ? token.data
      : maskAccessToken("agent", token.data);
    snippetToken = { shown: shownToken, copied: token.data };
    access = (
      <div className="space-y-1.5">
        <div className="grid gap-3 sm:grid-cols-2">
          <RowField
            label={t("connect.address")}
            value={url}
            actions={<CopyButton text={url} />}
          />
          <RowField
            label={t("connect.token")}
            value={shownToken}
            actions={
              <>
                <RevealButton shown={shown} onToggle={() => setShown(!shown)} />
                <CopyButton text={token.data} />
              </>
            }
          />
        </div>
        <p className="text-xs text-muted-lt">{t("connect.tokenNote")}</p>
      </div>
    );
  }

  /** A connect snippet that shows `build(shown)` and copies `build(copied)`. */
  const tokenSnippet = (
    label: string,
    build: (url: string, token: string | null) => string,
    snippet: SnippetToken,
  ) => {
    const text = build(url, snippet.shown);
    return (
      <CopyableSnippet
        label={label}
        text={text}
        copyText={build(url, snippet.copied)}
        rows={rowsFor(text)}
      />
    );
  };

  return (
    <Card className="space-y-3 p-4">
      <div className="space-y-1">
        <p className="text-sm font-medium text-text">{t("connect.title")}</p>
        <p className="text-xs text-muted-lt">{t("connect.subtitle")}</p>
      </div>

      {access}

      {/* Tab toggle - mirrors the Orders page pattern. */}
      <div className="flex w-fit flex-wrap gap-1 rounded-card border border-border bg-surface-2 p-1">
        {TABS.map((tabId) => (
          <button
            key={tabId}
            type="button"
            onClick={() => setTab(tabId)}
            className={[
              "rounded-badge px-3 py-1 text-xs font-medium transition-colors duration-[180ms]",
              tab === tabId
                ? "bg-accent-dim text-accent"
                : "text-muted-lt hover:bg-surface-hover hover:text-text",
            ].join(" ")}
          >
            {t(`connect.tab.${tabId}`)}
          </button>
        ))}
      </div>

      {/* While the token loads or after it failed, the token-carrying
          snippets stay hidden: the status above says which. */}
      {tab === "claude" && (
        <div className="space-y-3">
          {snippetToken !== null && (
            <>
              {tokenSnippet(
                t("connect.claude.cliLabel"),
                claudeMcpAddCommand,
                snippetToken,
              )}
              <div className="space-y-1.5">
                {tokenSnippet(
                  t("connect.claude.desktopLabel"),
                  claudeDesktopConfig,
                  snippetToken,
                )}
                <p className="text-xs text-muted-lt">
                  {t("connect.claude.bridgeNote", {
                    bridge: "mcp-remote",
                    runtime: "Node.js",
                  })}
                </p>
              </div>
            </>
          )}
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs text-muted-lt">
              {t("connect.claude.skillHint")}
            </p>
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

      {tab === "codex" &&
        snippetToken !== null &&
        tokenSnippet(
          t("connect.codex.label", { path: "~/.codex/config.toml" }),
          codexConfig,
          snippetToken,
        )}

      {tab === "cursor" &&
        snippetToken !== null &&
        tokenSnippet(
          t("connect.cursor.label", { path: "~/.cursor/mcp.json" }),
          cursorConfig,
          snippetToken,
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
          <p className="rounded-card border border-[var(--warn)] bg-[var(--warn-dim)] px-3 py-2 text-xs text-muted-lt">
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
