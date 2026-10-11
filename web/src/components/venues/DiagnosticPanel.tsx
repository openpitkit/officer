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

import { ChevronDown, ChevronUp, ExternalLink, Logs } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import type {
  Diagnostic,
  DiagnosticAction,
  MarketDataReferences,
} from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatDateTime } from "@/i18n/format";

const DIAGNOSTICS_PANEL_CLASS = "max-h-96 overflow-y-auto pr-1";

/** Data and handlers for one venue's diagnostics. An action renders only when
 *  the caller can carry it out: `restart` needs `onRestart`,
 *  `remove_instrument` needs `onRemoveInstrument` and a target, `open_docs`
 *  and `open_symbols` need the matching reference URL. */
export interface DiagnosticPanelProps {
  /** The venue's diagnostics. An empty list renders nothing. */
  items: Diagnostic[];
  references?: MarketDataReferences;
  /** Disables the action buttons while the owner runs an operation. */
  busy?: boolean;
  onRestart?: () => void;
  onRemoveInstrument?: (externalSymbol: string) => void;
}

function diagnosticLevelVariant(level: string): "danger" | "warn" | "neutral" {
  if (level === "error") return "danger";
  if (level === "warn") return "warn";
  return "neutral";
}

function diagnosticActionKey(action: DiagnosticAction): string {
  return `${action.type}:${action.target ?? ""}`;
}

function diagnosticSummaryVariant(
  items: Diagnostic[],
): "danger" | "neutral" | "warn" {
  if (items.some((item) => item.level === "error")) {
    return "danger";
  }
  if (items.some((item) => item.level === "warn")) {
    return "warn";
  }
  return "neutral";
}

function diagnosticActions(items: Diagnostic[]): DiagnosticAction[] {
  const seen = new Set<string>();
  const actions: DiagnosticAction[] = [];
  for (const item of items) {
    for (const action of item.actions) {
      if (action.type === "remove_instrument" && action.target === undefined) {
        continue;
      }
      const key = diagnosticActionKey(action);
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      actions.push(action);
    }
  }
  return actions;
}

function DiagnosticGroup({
  title,
  items,
}: {
  title: string;
  items: Diagnostic[];
}) {
  const { t } = useTranslation("venues");
  if (items.length === 0) return null;
  return (
    <div>
      <p className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted">
        {title}
      </p>
      <div className="space-y-2">
        {items.map((d) => (
          <div
            key={[
              d.code,
              d.kind,
              d.level,
              d.instrument ?? "",
              d.title,
              d.detail,
              d.at,
            ].join("|")}
            className="rounded-card border border-border bg-surface p-3 text-sm"
          >
            <div className="flex flex-wrap items-start gap-2">
              <Badge
                variant={diagnosticLevelVariant(d.level)}
                className="mt-px shrink-0"
              >
                {d.level === "error"
                  ? t("diagnostics.levelError")
                  : d.level === "warn"
                    ? t("diagnostics.levelWarn")
                    : t("diagnostics.levelInfo")}
              </Badge>
              <span className="font-semibold text-text">{d.title}</span>
              {d.instrument && (
                <Badge variant="neutral" className="shrink-0 font-mono text-xs">
                  {d.instrument}
                </Badge>
              )}
              <span className="nums ml-auto shrink-0 text-xs text-muted">
                {formatDateTime(d.at)}
              </span>
            </div>
            <p className="mt-1.5 text-text">{d.detail}</p>
            {d.remediation && (
              <p className="mt-1.5 text-xs text-muted">
                <span className="font-medium">{t("diagnostics.howToFix")}</span>{" "}
                {d.remediation}
              </p>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function DiagnosticActions({
  actions,
  references,
  busy,
  onRestart,
  onRemoveInstrument,
}: {
  actions: DiagnosticAction[];
  references: DiagnosticPanelProps["references"];
  busy: DiagnosticPanelProps["busy"];
  onRestart: DiagnosticPanelProps["onRestart"];
  onRemoveInstrument: DiagnosticPanelProps["onRemoveInstrument"];
}) {
  const { t } = useTranslation("venues");
  return (
    <div
      role="toolbar"
      aria-label={t("diagnostics.actions")}
      className="flex flex-wrap gap-2 border-t border-border pt-2"
    >
      {actions.map((action) => {
        if (action.type === "restart" && onRestart) {
          return (
            <Button
              key={diagnosticActionKey(action)}
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={onRestart}
            >
              {t("common:actions.restartFeeds")}
            </Button>
          );
        }
        if (action.type === "open_docs" && references?.docsUrl) {
          return (
            <a
              key={diagnosticActionKey(action)}
              href={references.docsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex h-8 items-center rounded-card border border-border bg-transparent px-3 text-xs transition-colors hover:bg-surface-hover"
            >
              <ExternalLink className="mr-1 h-3 w-3" />
              {t("references.docs")}
            </a>
          );
        }
        if (action.type === "open_symbols" && references?.symbolsUrl) {
          return (
            <a
              key={diagnosticActionKey(action)}
              href={references.symbolsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex h-8 items-center rounded-card border border-border bg-transparent px-3 text-xs transition-colors hover:bg-surface-hover"
            >
              <ExternalLink className="mr-1 h-3 w-3" />
              {t("references.symbols")}
            </a>
          );
        }
        if (
          action.type === "remove_instrument" &&
          action.target !== undefined &&
          onRemoveInstrument
        ) {
          const target = action.target;
          return (
            <Button
              key={diagnosticActionKey(action)}
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => onRemoveInstrument(target)}
            >
              {t("diagnostics.actionRemoveTarget", { target })}
            </Button>
          );
        }
        return null;
      })}
      <Button asChild variant="outline" size="sm">
        <Link to="/service#logs">
          <Logs className="mr-1 h-3 w-3" />
          {t("diagnostics.actionLogs")}
        </Link>
      </Button>
    </div>
  );
}

/** Body of {@link DiagnosticPanel} for a non-empty list. It owns the
 *  collapse state, so that state ends when the list empties and the next
 *  problem set opens expanded. */
function DiagnosticPanelBody({
  items,
  references,
  busy,
  onRestart,
  onRemoveInstrument,
}: DiagnosticPanelProps) {
  const { t } = useTranslation("venues");
  const [collapsed, setCollapsed] = useState(false);
  const actions = diagnosticActions(items);
  const summaryVariant = diagnosticSummaryVariant(items);

  if (collapsed) {
    return (
      <button
        type="button"
        aria-expanded={false}
        onClick={() => setCollapsed(false)}
        className="flex w-full items-center justify-between gap-3 rounded-card border border-border bg-surface px-3 py-2 text-left text-sm transition-colors hover:bg-surface-hover"
      >
        <span className="flex min-w-0 items-center gap-2">
          <Badge variant={summaryVariant} className="shrink-0">
            {items.length}
          </Badge>
          <span className="truncate font-medium text-text">
            {t("diagnostics.collapsedSummary", { count: items.length })}
          </span>
        </span>
        <span className="inline-flex items-center gap-1 text-xs text-muted">
          {t("diagnostics.expand")}
          <ChevronDown className="h-3 w-3" />
        </span>
      </button>
    );
  }

  const configGroup = items.filter((d) => d.kind === "config");
  const providerGroup = items.filter(
    (d) => d.kind === "environment" || d.kind === "provider",
  );
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={summaryVariant}>{items.length}</Badge>
        <span className="text-sm font-medium text-text">
          {t("diagnostics.summary", { count: items.length })}
        </span>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="ml-auto"
          aria-expanded={true}
          onClick={() => setCollapsed(true)}
        >
          {t("diagnostics.collapse")}
          <ChevronUp />
        </Button>
      </div>
      <div
        role="region"
        aria-label={t("diagnostics.title")}
        className={DIAGNOSTICS_PANEL_CLASS}
      >
        <div className="space-y-4">
          <DiagnosticGroup
            title={t("diagnostics.groupActionNeeded")}
            items={configGroup}
          />
          <DiagnosticGroup
            title={t("diagnostics.groupProvider")}
            items={providerGroup}
          />
        </div>
      </div>
      <DiagnosticActions
        actions={actions}
        references={references}
        busy={busy}
        onRestart={onRestart}
        onRemoveInstrument={onRemoveInstrument}
      />
    </div>
  );
}

/** Collapsible list of a venue's diagnostics, grouped into configuration
 *  problems and connection or provider problems, with the actions the
 *  diagnostics offer and a link to the service logs. Renders nothing
 *  without diagnostics. */
export function DiagnosticPanel(props: DiagnosticPanelProps) {
  if (props.items.length === 0) {
    return null;
  }
  return <DiagnosticPanelBody {...props} />;
}
