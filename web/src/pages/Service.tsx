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

import {
  AlertTriangle,
  Database,
  Download,
  ExternalLink,
  FileArchive,
  Loader2,
  Logs,
  Plug,
  RefreshCw,
  RotateCcw,
  Server,
  Trash2,
  Upload,
  type LucideIcon,
} from "lucide-react";
import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useLocation, useNavigate } from "react-router-dom";

import {
  exportBackup,
  resetDatabase as resetDatabaseRequest,
  restartMarketData,
  restoreBackup,
  serviceLogsDownloadUrl,
} from "@/api/client";
import type {
  BackupEntitySelector,
  BackupRestoreSummary,
  BackupScope,
  BackupSection,
  RestoreMode,
  ServiceInfo,
} from "@/api/types";
import { useService } from "@/api/useService";
import { useServiceLogs } from "@/api/useServiceLogs";
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
import { cn } from "@/lib/utils";

// This page is the canonical i18n pattern (phase 1 pilot): user-facing text
// goes through useTranslation with a per-area "service" namespace; shared
// chrome (placeholders, generic verbs) comes from "common". Subsequent pages
// copy this shape. Domain identifiers and route paths stay literal.

/** One row in the build-profile parameter table. */
interface ProfileRow {
  param: string;
  value: string;
}

type ServiceTab = "application" | "api" | "database" | "logs";
type BackupWorkflow = "export" | "restore";

const serviceTabs: {
  id: ServiceTab;
  labelKey: string;
  icon: LucideIcon;
}[] = [
  { id: "application", labelKey: "tabs.application", icon: Server },
  { id: "api", labelKey: "tabs.api", icon: Plug },
  { id: "database", labelKey: "tabs.database", icon: Database },
  { id: "logs", labelKey: "tabs.logs", icon: Logs },
];

const backupWorkflowTabs: {
  id: BackupWorkflow;
  labelKey: string;
  icon: LucideIcon;
}[] = [
  { id: "export", labelKey: "backup.tabs.export", icon: Download },
  { id: "restore", labelKey: "backup.tabs.restore", icon: Upload },
];

const backupSections: BackupSection[] = [
  "accounts_groups",
  "positions",
  "risk_limits",
  "market_data_settings",
  "market_data_quotes",
  "general_settings",
  "activity_history",
  "audit_log",
];

const restoreModes: RestoreMode[] = [
  "replace_all",
  "overwrite",
  "insert_missing",
];

const accountScopedSections = new Set<BackupSection>([
  "accounts_groups",
  "risk_limits",
  "activity_history",
  "audit_log",
]);

const serviceTabIDs = serviceTabs.map(({ id }) => id);
const backupWorkflowTabIDs = backupWorkflowTabs.map(({ id }) => id);

function serviceTabID(tab: ServiceTab) {
  return `service-tab-${tab}`;
}

function servicePanelID(tab: ServiceTab) {
  return `service-panel-${tab}`;
}

function backupWorkflowTabID(tab: BackupWorkflow) {
  return `backup-workflow-tab-${tab}`;
}

function backupWorkflowPanelID(tab: BackupWorkflow) {
  return `backup-workflow-panel-${tab}`;
}

function moveTabFocus<T extends string>(
  event: KeyboardEvent<HTMLButtonElement>,
  tabs: readonly T[],
  current: T,
  select: (tab: T) => void,
  tabID: (tab: T) => string,
) {
  const index = tabs.indexOf(current);
  if (index < 0) {
    return;
  }
  let next: T | null = null;
  switch (event.key) {
    case "ArrowRight":
    case "ArrowDown":
      next = tabs[(index + 1) % tabs.length];
      break;
    case "ArrowLeft":
    case "ArrowUp":
      next = tabs[(index + tabs.length - 1) % tabs.length];
      break;
    case "Home":
      next = tabs[0];
      break;
    case "End":
      next = tabs[tabs.length - 1];
      break;
    default:
      return;
  }
  event.preventDefault();
  select(next);
  window.requestAnimationFrame(() => {
    document.getElementById(tabID(next))?.focus();
  });
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

/**
 * The captured service log tail with a download affordance. The recent lines
 * render in a height-capped monospace block (oldest first); the Download button
 * fetches the full buffer over the same-origin download route.
 */
function LogsCard() {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { load, reload } = useServiceLogs();
  const refreshing = load.state === "loading";
  const lines = load.state === "ready" ? load.data.lines : [];

  return (
    <Card id="logs" className="scroll-mt-4">
      <CardHeader className="flex-row items-center justify-between gap-2">
        <CardTitle>{t("logs.title")}</CardTitle>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={reload}
            disabled={refreshing}
            aria-label={t("logs.refreshAriaLabel")}
          >
            {refreshing ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <RefreshCw className="h-3.5 w-3.5" />
            )}
            {tc("actions.refresh")}
          </Button>
          <Button asChild variant="ghost" size="sm">
            <a href={serviceLogsDownloadUrl} download>
              <Download className="h-3.5 w-3.5" />
              {t("logs.download")}
            </a>
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {load.state === "error" ? (
          <p className="text-xs text-muted-lt italic">{load.error}</p>
        ) : lines.length === 0 ? (
          <p className="text-xs text-muted-lt italic">{t("logs.empty")}</p>
        ) : (
          <pre className="max-h-[calc(100vh-18rem)] min-h-[24rem] overflow-auto whitespace-pre-wrap break-all rounded bg-surface-2 p-3 font-mono text-xs text-muted">
            {lines.join("\n")}
          </pre>
        )}
      </CardContent>
    </Card>
  );
}

function splitList(value: string): string[] {
  return value
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
}

function selectorScope(
  all: boolean,
  accounts: string,
  groups: string,
): BackupEntitySelector {
  const out: BackupEntitySelector = { all };
  if (all) {
    return out;
  }
  const accountList = splitList(accounts);
  if (accountList.length > 0) {
    out.accounts = accountList;
  }
  const groupList = splitList(groups);
  if (groupList.length > 0) {
    out.groups = groupList;
  }
  return out;
}

function downloadArchive(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

function arrayBufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  const chunkSize = 0x8000;
  let binary = "";
  for (let i = 0; i < bytes.length; i += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunkSize));
  }
  return window.btoa(binary);
}

export function BackupCard() {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const [workflow, setWorkflow] = useState<BackupWorkflow>("export");
  const [all, setAll] = useState(true);
  const [sections, setSections] = useState<BackupSection[]>(backupSections);
  const [accountAll, setAccountAll] = useState(true);
  const [accountIDs, setAccountIDs] = useState("");
  const [accountGroups, setAccountGroups] = useState("");
  const [positionAll, setPositionAll] = useState(true);
  const [positionAccounts, setPositionAccounts] = useState("");
  const [positionGroups, setPositionGroups] = useState("");
  const [zipArchive, setZipArchive] = useState(true);
  const [restoreMode, setRestoreMode] = useState<RestoreMode | "">("");
  const [restoreFile, setRestoreFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [summary, setSummary] = useState<BackupRestoreSummary | null>(null);
  const [downloaded, setDownloaded] = useState("");

  const resetScopeControls = () => {
    setAll(true);
    setSections(backupSections);
    setAccountAll(true);
    setAccountIDs("");
    setAccountGroups("");
    setPositionAll(true);
    setPositionAccounts("");
    setPositionGroups("");
    setZipArchive(true);
  };

  const toggleSection = (section: BackupSection) => {
    setSections((current) =>
      current.includes(section)
        ? current.filter((item) => item !== section)
        : [...current, section],
    );
  };

  const scope = (): BackupScope => {
    const out: BackupScope = { all };
    if (all) {
      return out;
    }
    out.sections = sections;
    if (sections.some((section) => accountScopedSections.has(section))) {
      out.accounts = selectorScope(accountAll, accountIDs, accountGroups);
    }
    if (sections.includes("positions")) {
      out.positions = selectorScope(
        positionAll,
        positionAccounts,
        positionGroups,
      );
    }
    return out;
  };

  const runExport = async () => {
    setBusy(true);
    setError("");
    setSummary(null);
    setDownloaded("");
    try {
      const out = await exportBackup(scope(), undefined, zipArchive);
      downloadArchive(out.blob, out.filename);
      setDownloaded(out.filename);
      resetScopeControls();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const runRestore = async () => {
    if (!restoreMode || !restoreFile) {
      return;
    }
    if (
      restoreMode === "replace_all" &&
      !window.confirm(t("backup.confirmReplaceAll"))
    ) {
      return;
    }
    setBusy(true);
    setError("");
    setSummary(null);
    try {
      const base64 = arrayBufferToBase64(await restoreFile.arrayBuffer());
      const result = await restoreBackup({
        archiveFile: {
          base64,
          filename: restoreFile.name,
        },
        scope: scope(),
        mode: restoreMode,
      });
      setSummary(result);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const scopeControls = (
    <BackupScopeControls
      all={all}
      setAll={setAll}
      sections={sections}
      toggleSection={toggleSection}
      accountAll={accountAll}
      setAccountAll={setAccountAll}
      accountIDs={accountIDs}
      setAccountIDs={setAccountIDs}
      accountGroups={accountGroups}
      setAccountGroups={setAccountGroups}
      positionAll={positionAll}
      setPositionAll={setPositionAll}
      positionAccounts={positionAccounts}
      setPositionAccounts={setPositionAccounts}
      positionGroups={positionGroups}
      setPositionGroups={setPositionGroups}
    />
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("backup.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div
          role="tablist"
          aria-label={t("backup.tabs.ariaLabel")}
          className="inline-flex w-fit overflow-hidden rounded-card border border-border bg-bg/70"
        >
          {backupWorkflowTabs.map(({ id, labelKey, icon: Icon }) => (
            <button
              id={backupWorkflowTabID(id)}
              key={id}
              type="button"
              role="tab"
              aria-selected={workflow === id}
              aria-controls={backupWorkflowPanelID(id)}
              tabIndex={workflow === id ? 0 : -1}
              className={cn(
                "inline-flex h-8 items-center gap-2 border-l border-border px-3 text-xs font-medium transition-colors first:border-l-0",
                workflow === id
                  ? "bg-accent text-bg"
                  : "text-muted-lt hover:bg-surface-hover hover:text-accent",
              )}
              onClick={() => setWorkflow(id)}
              onKeyDown={(event) =>
                moveTabFocus(
                  event,
                  backupWorkflowTabIDs,
                  id,
                  setWorkflow,
                  backupWorkflowTabID,
                )
              }
            >
              <Icon className="h-3.5 w-3.5" />
              {t(labelKey)}
            </button>
          ))}
        </div>

        {workflow === "export" ? (
          <div
            id={backupWorkflowPanelID("export")}
            role="tabpanel"
            aria-labelledby={backupWorkflowTabID("export")}
            className="space-y-4"
          >
            {scopeControls}
            <div className="space-y-2">
              <label className="flex w-fit items-center gap-2 text-xs text-muted">
                <input
                  type="checkbox"
                  checked={zipArchive}
                  onChange={(event) => setZipArchive(event.target.checked)}
                />
                {t("backup.zip")}
              </label>
              <Button
                type="button"
                size="sm"
                onClick={() => {
                  void runExport();
                }}
                disabled={busy}
              >
                {busy ? <Loader2 className="animate-spin" /> : <Download />}
                {t("backup.export")}
              </Button>
              {downloaded && (
                <p className="break-all text-xs text-muted-lt">{downloaded}</p>
              )}
            </div>
          </div>
        ) : (
          <div
            id={backupWorkflowPanelID("restore")}
            role="tabpanel"
            aria-labelledby={backupWorkflowTabID("restore")}
            className="space-y-4"
          >
            {scopeControls}
            <div className="rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] px-3 py-2 text-xs text-[var(--danger)]">
              <div className="flex items-start gap-2">
                <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                <p>
                  <span className="font-bold">
                    {t("backup.restoreScopeWarningTitle")}
                  </span>{" "}
                  {t("backup.restoreScopeWarning")}
                </p>
              </div>
            </div>
            <div className="space-y-3">
              <div className="grid gap-2">
                {restoreModes.map((mode) => (
                  <label key={mode} className="flex items-center gap-2 text-xs">
                    <input
                      type="radio"
                      name="restore-mode"
                      checked={restoreMode === mode}
                      onChange={() => setRestoreMode(mode)}
                    />
                    {t(`backup.modes.${mode}`)}
                  </label>
                ))}
              </div>
              <label className="flex items-center gap-2 text-xs text-muted">
                <FileArchive className="h-3.5 w-3.5" />
                <input
                  type="file"
                  accept="application/json,application/zip,.json,.zip"
                  onChange={(event) =>
                    setRestoreFile(event.target.files?.[0] ?? null)
                  }
                />
              </label>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  void runRestore();
                }}
                disabled={busy || !restoreMode || !restoreFile}
              >
                {busy ? <Loader2 className="animate-spin" /> : <Upload />}
                {t("backup.restore")}
              </Button>
            </div>
          </div>
        )}

        {error && <p className="text-xs text-[var(--danger)]">{error}</p>}
        {summary && (
          <p className="text-xs text-muted-lt">
            {t("backup.summary", {
              applied: Object.values(summary.applied).reduce(
                (a, b) => a + (b ?? 0),
                0,
              ),
              skipped: Object.values(summary.skipped).reduce(
                (a, b) => a + (b ?? 0),
                0,
              ),
              restart: summary.restartRequired
                ? t("backup.restartRequired")
                : tc("value.no"),
            })}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function BackupScopeControls(props: {
  all: boolean;
  setAll: (value: boolean) => void;
  sections: BackupSection[];
  toggleSection: (section: BackupSection) => void;
  accountAll: boolean;
  setAccountAll: (value: boolean) => void;
  accountIDs: string;
  setAccountIDs: (value: string) => void;
  accountGroups: string;
  setAccountGroups: (value: string) => void;
  positionAll: boolean;
  setPositionAll: (value: boolean) => void;
  positionAccounts: string;
  setPositionAccounts: (value: string) => void;
  positionGroups: string;
  setPositionGroups: (value: string) => void;
}) {
  const { t } = useTranslation("service");

  return (
    <div className="space-y-4">
      <label className="inline-flex w-fit items-center gap-2 text-xs text-text">
        <input
          type="checkbox"
          checked={props.all}
          onChange={(event) => props.setAll(event.target.checked)}
        />
        {t("backup.all")}
      </label>

      <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
        {backupSections.map((section) => (
          <label
            key={section}
            className="inline-flex w-fit items-center gap-2 whitespace-nowrap text-xs text-muted"
          >
            <input
              type="checkbox"
              checked={props.all || props.sections.includes(section)}
              disabled={props.all}
              onChange={() => props.toggleSection(section)}
            />
            {t(`backup.sections.${section}`)}
          </label>
        ))}
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <SelectorBox
          title={t("backup.accounts.title")}
          allLabel={t("backup.accounts.all")}
          all={props.accountAll}
          setAll={props.setAccountAll}
          accounts={props.accountIDs}
          setAccounts={props.setAccountIDs}
          groups={props.accountGroups}
          setGroups={props.setAccountGroups}
          accountsPlaceholder={t("backup.accountsPlaceholder")}
          groupsPlaceholder={t("backup.groupsPlaceholder")}
        />
        <SelectorBox
          title={t("backup.positions.title")}
          allLabel={t("backup.positions.all")}
          all={props.positionAll}
          setAll={props.setPositionAll}
          accounts={props.positionAccounts}
          setAccounts={props.setPositionAccounts}
          groups={props.positionGroups}
          setGroups={props.setPositionGroups}
          accountsPlaceholder={t("backup.positionsAccountsPlaceholder")}
          groupsPlaceholder={t("backup.positionsGroupsPlaceholder")}
        />
      </div>
    </div>
  );
}

function SelectorBox(props: {
  title: string;
  allLabel: string;
  all: boolean;
  setAll: (value: boolean) => void;
  accounts: string;
  setAccounts: (value: string) => void;
  groups: string;
  setGroups: (value: string) => void;
  accountsPlaceholder: string;
  groupsPlaceholder: string;
}) {
  return (
    <div className="space-y-2 rounded-card border border-border p-3">
      <p className="text-xs font-medium text-muted">{props.title}</p>
      <label className="flex items-center gap-2 text-xs text-text">
        <input
          type="checkbox"
          checked={props.all}
          onChange={(event) => props.setAll(event.target.checked)}
        />
        {props.allLabel}
      </label>
      <input
        className="h-8 w-full rounded-card border border-border bg-surface-2 px-2 text-xs"
        value={props.accounts}
        onChange={(event) => props.setAccounts(event.target.value)}
        placeholder={props.accountsPlaceholder}
        disabled={props.all}
      />
      <input
        className="h-8 w-full rounded-card border border-border bg-surface-2 px-2 text-xs"
        value={props.groups}
        onChange={(event) => props.setGroups(event.target.value)}
        placeholder={props.groupsPlaceholder}
        disabled={props.all}
      />
    </div>
  );
}

export function DatabaseCard({
  database,
  onReset,
}: {
  database: ServiceInfo["database"];
  onReset: () => void;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const [resetting, setResetting] = useState(false);
  const [resetDone, setResetDone] = useState(false);
  const [resetError, setResetError] = useState("");

  const runReset = async () => {
    if (!window.confirm(t("database.resetConfirm"))) {
      return;
    }
    setResetting(true);
    setResetDone(false);
    setResetError("");
    try {
      await resetDatabaseRequest();
      setResetDone(true);
      onReset();
    } catch (err) {
      setResetError(err instanceof Error ? err.message : String(err));
    } finally {
      setResetting(false);
    }
  };

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2">
        <CardTitle>{t("database.title")}</CardTitle>
        <Badge variant={database.reachable ? "ok" : "danger"}>
          <StatusDot
            tone={database.reachable ? "ok" : "danger"}
            pulse={database.reachable}
          />
          {database.reachable
            ? t("database.reachable")
            : t("database.unreachable")}
        </Badge>
      </CardHeader>
      <CardContent>
        <StatRow
          label={t("database.reachableLabel")}
          value={database.reachable ? tc("value.yes") : tc("value.no")}
        />
        <StatRow
          label={t("database.path")}
          value={database.path || tc("value.none")}
          mono={false}
        />
        <div className="mt-4 border-t border-[var(--danger)]/50 pt-3">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div>
              <p className="text-xs font-bold text-[var(--danger)]">
                {t("database.resetTitle")}
              </p>
              <p className="mt-1 max-w-2xl text-xs text-muted-lt">
                {t("database.resetDescription")}
              </p>
            </div>
            <Button
              type="button"
              size="sm"
              className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
              onClick={() => {
                void runReset();
              }}
              disabled={resetting}
            >
              {resetting ? <Loader2 className="animate-spin" /> : <Trash2 />}
              {t("database.resetButton")}
            </Button>
          </div>
          {resetDone && (
            <p className="mt-2 text-xs font-medium text-[var(--ok)]">
              {t("database.resetDone")}
            </p>
          )}
          {resetError && (
            <p className="mt-2 text-xs text-[var(--danger)]">{resetError}</p>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

export function ServiceCard({
  info,
  activeTab,
  onTabChange,
  onDatabaseReset,
}: {
  info: ServiceInfo;
  activeTab: ServiceTab;
  onTabChange: (tab: ServiceTab) => void;
  onDatabaseReset: () => void;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { t: tmcp } = useTranslation("mcp");
  const { load: mcpLoad } = useMcpAccess();
  const mcpCommands = mcpLoad.state === "ready" ? mcpLoad.data : null;

  return (
    <div className="space-y-4 animate-fade-in">
      <div
        role="tablist"
        aria-label={t("tabs.ariaLabel")}
        className="flex w-fit flex-wrap overflow-hidden rounded-card border border-border bg-bg/70"
      >
        {serviceTabs.map(({ id, labelKey, icon: Icon }) => (
          <button
            id={serviceTabID(id)}
            key={id}
            type="button"
            role="tab"
            aria-selected={activeTab === id}
            aria-controls={servicePanelID(id)}
            tabIndex={activeTab === id ? 0 : -1}
            className={cn(
              "inline-flex h-8 items-center gap-2 border-l border-border px-3 text-xs font-medium transition-colors first:border-l-0",
              activeTab === id
                ? "bg-accent text-bg"
                : "text-muted-lt hover:bg-surface-hover hover:text-accent",
            )}
            onClick={() => onTabChange(id)}
            onKeyDown={(event) =>
              moveTabFocus(
                event,
                serviceTabIDs,
                id,
                onTabChange,
                serviceTabID,
              )
            }
          >
            <Icon className="h-3.5 w-3.5" />
            {t(labelKey)}
          </button>
        ))}
      </div>

      {activeTab === "application" && (
        <div
          id={servicePanelID("application")}
          role="tabpanel"
          aria-labelledby={serviceTabID("application")}
          className="space-y-4"
        >
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
                    <Badge variant="warn">
                      {t("application.nonRelease")}
                    </Badge>
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
                mono={false}
                value={
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="nums font-mono">
                      {info.engineVersion || tc("value.none")}
                    </span>
                    <a
                      href="https://github.com/openpitkit/pit?officer"
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
                    >
                      {t("engine.sdk")}
                      <ExternalLink className="h-3 w-3 opacity-70" />
                    </a>
                  </span>
                }
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
                      <Badge variant="warn">
                        {t("engine.modifiedSources")}
                      </Badge>
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
        </div>
      )}

      {activeTab === "database" && (
        <div
          id={servicePanelID("database")}
          role="tabpanel"
          aria-labelledby={serviceTabID("database")}
          className="space-y-4"
        >
          <DatabaseCard
            database={info.database}
            onReset={onDatabaseReset}
          />
          <BackupCard />
        </div>
      )}

      {activeTab === "api" && (
        <div
          id={servicePanelID("api")}
          role="tabpanel"
          aria-labelledby={serviceTabID("api")}
        >
          <Card>
            <CardHeader>
              <CardTitle>{t("api.title")}</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
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

              <div>
                <div className="mb-1.5 flex items-center justify-between gap-2">
                  <p className="text-xs font-medium text-muted">
                    {t("api.mcp")}
                  </p>
                  <div className="flex items-center gap-2">
                    <span className="nums text-[0.6875rem] text-muted-lt">
                      /mcp
                    </span>
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
                            {tmcp(`command.${cmd.name}.description`, {
                              defaultValue:
                                cmd.agentDescription || tc("value.none"),
                            })}
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
      )}

      {activeTab === "logs" && (
        <div
          id={servicePanelID("logs")}
          role="tabpanel"
          aria-labelledby={serviceTabID("logs")}
        >
          <LogsCard />
        </div>
      )}
    </div>
  );
}

export function Service() {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { t: tm } = useTranslation("marketData");
  const { load, reload } = useService();
  const location = useLocation();
  const navigate = useNavigate();
  const [restarting, setRestarting] = useState(false);
  const [activeTab, setActiveTab] = useState<ServiceTab>("application");
  const scrolledToLogs = useRef(false);
  const refreshing = load.state === "loading";
  const displayedTab: ServiceTab =
    location.hash === "#logs" ? "logs" : activeTab;

  useEffect(() => {
    if (location.hash !== "#logs") {
      scrolledToLogs.current = false;
      return;
    }
    if (scrolledToLogs.current || load.state !== "ready") {
      return;
    }
    scrolledToLogs.current = true;
    window.requestAnimationFrame(() => {
      document.getElementById("logs")?.scrollIntoView({ block: "start" });
    });
  }, [load.state, location.hash]);

  const selectTab = (tab: ServiceTab) => {
    setActiveTab(tab);
    if (location.hash !== "") {
      navigate(
        { pathname: location.pathname, search: location.search },
        { replace: true },
      );
    }
  };
  const restartFeeds = async () => {
    setRestarting(true);
    try {
      await restartMarketData();
    } finally {
      setRestarting(false);
    }
  };

  return (
    <Page
      title={t("title")}
      actions={
        <>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              void restartFeeds();
            }}
            disabled={restarting}
            aria-label={t("restartFeedsAriaLabel")}
          >
            {restarting ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <RotateCcw className="h-3.5 w-3.5" />
            )}
            {tm("actions.restart")}
          </Button>
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
        </>
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
      {load.state === "ready" && (
        <ServiceCard
          info={load.data}
          activeTab={displayedTab}
          onTabChange={selectTab}
          onDatabaseReset={reload}
        />
      )}
    </Page>
  );
}
