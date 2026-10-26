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

import {
  AlertTriangle,
  Database,
  Download,
  ExternalLink,
  FileArchive,
  Info,
  Loader2,
  Logs,
  Plug,
  Power,
  RefreshCw,
  RotateCcw,
  Server,
  ShieldCheck,
  Trash2,
  Upload,
  Users,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { Link, useLocation, useNavigate } from "react-router-dom";

import { useAccountMode, type AccountModeState } from "@/api/accountMode";
import type {
  BackupEntitySelector,
  BackupRestoreSummary,
  BackupScope,
  BackupSection,
  RestoreMode,
  ServiceInfo,
} from "@/api/types";
import { useCredentialStorage } from "@/api/useCredentialStorage";
import type { LoadState, PollingResult } from "@/api/usePolling";
import { useService } from "@/api/useService";
import { useServiceLogs } from "@/api/useServiceLogs";
import { useMcpAccess } from "@/api/useMcpAccess";
import { useTrading } from "@/api/useTrading";
import { AccountModePanel } from "@/components/AccountModeChoice";
import { ConnectAgent } from "@/components/ConnectAgent";
import { MasterKeyLink } from "@/components/MasterKeyLink";
import { Notice } from "@/components/Notice";
import {
  CompactStaleState,
  ErrorBanner,
  ErrorState,
  StaleState,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import {
  AccessSection,
  AccessTokensProvider,
} from "@/components/security/AccessSection";
import { GuideSection } from "@/components/security/GuideSection";
import { StorageSection } from "@/components/security/StorageSection";
import { StatRow } from "@/components/StatRow";
import { StatusDot } from "@/components/StatusDot";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  serviceLogsDownloadUrl,
  useApiClient,
  useOfficerApi,
} from "@/framework";
import { accountScope } from "@/lib/accountScope";
import { checkSession } from "@/lib/panelSession";

// This page is the canonical i18n pattern: user-facing text
// goes through useTranslation with a per-area "service" namespace; shared
// chrome (placeholders, generic verbs) comes from "common". Subsequent pages
// copy this shape. Domain identifiers and route paths stay literal.

/** One row in the build-profile parameter table. */
interface ProfileRow {
  param: string;
  value: string;
}

type ServiceTab =
  | "application"
  | "engine"
  | "account-mode"
  | "api"
  | "database"
  | "logs"
  | "security";
type BackupWorkflow = "export" | "restore";
type ServiceLifecycleAction = "restart" | "stop";

const serviceTabs: {
  id: ServiceTab;
  labelKey: string;
  icon: LucideIcon;
}[] = [
  { id: "application", labelKey: "tabs.application", icon: Server },
  { id: "engine", labelKey: "engine.title", icon: Info },
  { id: "account-mode", labelKey: "tabs.accountMode", icon: Users },
  { id: "api", labelKey: "tabs.api", icon: Plug },
  { id: "database", labelKey: "tabs.database", icon: Database },
  { id: "logs", labelKey: "tabs.logs", icon: Logs },
  { id: "security", labelKey: "tabs.security", icon: ShieldCheck },
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
  "general_settings",
  "activity_history",
  "trading",
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

const tradingSections: BackupSection[] = [
  "accounts_groups",
  "activity_history",
  "trading",
];

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
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
 * fetches the full buffer through the API client and saves it as a file.
 */
function LogsCard() {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const apiClient = useApiClient();
  const { load, reload } = useServiceLogs();
  const [downloading, setDownloading] = useState(false);
  const [downloadError, setDownloadError] = useState<string | null>(null);
  const refreshing = load.state === "loading";
  const lines = load.state === "ready" ? load.data.lines : [];

  const download = async () => {
    setDownloading(true);
    setDownloadError(null);
    try {
      const { blob, filename } = await apiClient.requestBlob(
        serviceLogsDownloadUrl(apiClient.baseUrl),
        { accept: "text/plain", fallbackFilename: "pit-officer.log" },
      );
      downloadArchive(blob, filename);
    } catch (err) {
      setDownloadError(errorText(err));
    } finally {
      setDownloading(false);
    }
  };

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
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              void download();
            }}
            disabled={downloading}
          >
            <Download className="h-3.5 w-3.5" />
            {t("logs.download")}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {downloadError !== null && (
          <ErrorBanner className="mb-3" message={downloadError} />
        )}
        <StaleState load={load} reload={reload} />
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
  const { exportBackup, restoreBackup } = useOfficerApi();
  const { load: tradingLoad } = useTrading();
  const { load: credentialStorage } = useCredentialStorage();
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
  const tradingSelected = all || sections.includes("trading");
  const marketDataSelected = all || sections.includes("market_data_settings");
  const exportAllAccounts =
    workflow === "export" &&
    tradingSelected &&
    tradingLoad.state === "ready" &&
    tradingLoad.data.connections.length > 0;
  const plaintextTradingExport =
    tradingSelected &&
    credentialStorage.state === "ready" &&
    credentialStorage.data.plaintextTradingKeys;
  const plaintextMarketDataExport =
    marketDataSelected &&
    credentialStorage.state === "ready" &&
    credentialStorage.data.plaintextMarketDataKeys;

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
    const coupled = section === "trading" || section === "activity_history";
    const added = coupled ? tradingSections : [section];
    const removed =
      section === "accounts_groups"
        ? tradingSections
        : coupled
          ? ["trading", "activity_history"]
          : [section];
    setSections((current) =>
      current.includes(section)
        ? current.filter((item) => !removed.includes(item))
        : backupSections.filter(
            (item) => current.includes(item) || added.includes(item),
          ),
    );
  };

  const scope = (): BackupScope => {
    const out: BackupScope = { all };
    if (all) {
      return out;
    }
    out.sections = sections;
    if (sections.some((section) => accountScopedSections.has(section))) {
      out.accounts = exportAllAccounts
        ? { all: true }
        : selectorScope(accountAll, accountIDs, accountGroups);
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
      exportAllAccounts={exportAllAccounts}
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
      <CardContent className="flex flex-col gap-y-4">
        <Tabs
          value={workflow}
          onValueChange={(value) => setWorkflow(value as BackupWorkflow)}
          className="flex flex-col gap-y-4"
        >
          <TabsList aria-label={t("backup.tabs.ariaLabel")}>
            {backupWorkflowTabs.map(({ id, labelKey, icon: Icon }) => (
              <TabsTrigger key={id} value={id}>
                <Icon className="h-3.5 w-3.5" />
                {t(labelKey)}
              </TabsTrigger>
            ))}
          </TabsList>

          <TabsContent value="export" className="flex flex-col gap-y-4">
            {scopeControls}
            <div className="flex flex-col gap-y-2">
              <label className="flex w-fit items-center gap-2 text-xs text-muted">
                <input
                  type="checkbox"
                  checked={zipArchive}
                  onChange={(event) => setZipArchive(event.target.checked)}
                />
                {t("backup.zip")}
              </label>
              {(plaintextTradingExport || plaintextMarketDataExport) && (
                <Notice
                  tone="danger"
                  icon={AlertTriangle}
                  title={t("backup.plaintextKeys.title")}
                >
                  {plaintextTradingExport && (
                    <p>{t("backup.plaintextKeys.trading")}</p>
                  )}
                  {plaintextMarketDataExport && (
                    <p>{t("backup.plaintextKeys.marketData")}</p>
                  )}
                  <p>{t("backup.plaintextKeys.advice")}</p>
                  <p>
                    <MasterKeyLink />
                  </p>
                </Notice>
              )}
              {credentialStorage.state === "error" &&
                (tradingSelected || marketDataSelected) && (
                  <ErrorBanner
                    message={tc("sensitiveData.unknown", {
                      error: credentialStorage.error,
                    })}
                  />
                )}
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
          </TabsContent>
          <TabsContent value="restore" className="flex flex-col gap-y-4">
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
            <div className="flex flex-col gap-y-3">
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
          </TabsContent>
        </Tabs>

        {error && <p className="text-xs text-[var(--danger)]">{error}</p>}
        {summary && (
          <>
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
            {summary.marketDataCredentialsUnavailable?.length ? (
              <p className="text-xs text-[var(--danger)]">
                {t("backup.marketDataCredentialsUnavailable", {
                  instances:
                    summary.marketDataCredentialsUnavailable.join(", "),
                })}
              </p>
            ) : null}
            {summary.tradingCredentialsUnavailable?.length ? (
              <p className="text-xs text-[var(--danger)]">
                <Trans
                  t={t}
                  i18nKey="backup.tradingCredentialsUnavailable"
                  components={{
                    connections: (
                      <span>
                        {summary.tradingCredentialsUnavailable.join(", ")}
                      </span>
                    ),
                    tradingLink: (
                      <Link to="/trading-connections" className="underline" />
                    ),
                  }}
                />
              </p>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  );
}

function BackupScopeControls(props: {
  all: boolean;
  setAll: (value: boolean) => void;
  sections: BackupSection[];
  exportAllAccounts: boolean;
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
  const { showsAccounts } = accountScope();

  return (
    <div className="flex flex-col gap-y-4">
      <label className="inline-flex w-fit items-center gap-2 text-xs text-text">
        <input
          type="checkbox"
          checked={props.all}
          onChange={(event) => props.setAll(event.target.checked)}
        />
        {t("backup.all")}
      </label>

      {/* Two adjacent columns: fill the first, then the second sits right
          beside it (content-width, not stretched across the row). */}
      <div className="flex gap-x-10">
        {[
          backupSections.slice(0, Math.ceil(backupSections.length / 2)),
          backupSections.slice(Math.ceil(backupSections.length / 2)),
        ].map((column) => (
          <div key={column[0]} className="flex flex-col gap-y-2">
            {column.map((section) => (
              <label
                key={section}
                className="flex w-fit items-center gap-2 whitespace-nowrap text-xs text-muted"
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
        ))}
      </div>

      <p className="text-xs text-muted-lt">{t("backup.tradingSectionsHint")}</p>

      {/* While accounts are hidden there is no account or group to narrow
          by: the selectors keep their all-accounts default. */}
      {showsAccounts && (
        <>
          <div className="grid gap-3 md:grid-cols-2">
            <SelectorBox
              title={t("backup.accounts.title")}
              allLabel={t("backup.accounts.all")}
              all={props.exportAllAccounts || props.accountAll}
              disabled={props.exportAllAccounts}
              setAll={props.setAccountAll}
              accounts={props.exportAllAccounts ? "" : props.accountIDs}
              setAccounts={props.setAccountIDs}
              groups={props.exportAllAccounts ? "" : props.accountGroups}
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
          {props.exportAllAccounts && (
            <p className="text-xs text-muted-lt">
              {t("backup.tradingAccountsHint")}
            </p>
          )}
        </>
      )}
    </div>
  );
}

function SelectorBox(props: {
  title: string;
  allLabel: string;
  all: boolean;
  disabled?: boolean;
  setAll: (value: boolean) => void;
  accounts: string;
  setAccounts: (value: string) => void;
  groups: string;
  setGroups: (value: string) => void;
  accountsPlaceholder: string;
  groupsPlaceholder: string;
}) {
  return (
    <div className="flex flex-col gap-y-2 rounded-card border border-border p-3">
      <p className="text-xs font-medium text-muted">{props.title}</p>
      <label className="flex items-center gap-2 text-xs text-text">
        <input
          type="checkbox"
          checked={props.all}
          disabled={props.disabled}
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
  const { resetDatabase } = useOfficerApi();
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
      await resetDatabase();
      setResetDone(true);
      onReset();
      // A reset returns Officer to setup: the gate reloads into the wizard.
      checkSession();
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

export function ServiceLifecycleCard({
  busyAction,
  error,
  onAction,
}: {
  busyAction: ServiceLifecycleAction | null;
  error: string | null;
  onAction: (action: ServiceLifecycleAction) => Promise<void>;
}) {
  const { t } = useTranslation("service");
  const [confirmAction, setConfirmAction] =
    useState<ServiceLifecycleAction | null>(null);
  const busy = busyAction !== null;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("lifecycle.title")}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-y-3">
        <div className="flex items-start gap-2 rounded-card border border-[var(--danger)]/35 bg-[var(--danger)]/10 p-3 text-xs text-text">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--danger)]" />
          <p>{t("lifecycle.warning")}</p>
        </div>
        {error !== null && (
          <p className="rounded-card border border-[var(--danger)]/40 bg-[var(--danger)]/10 px-3 py-2 text-xs text-[var(--danger)]">
            {error}
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setConfirmAction("restart")}
            disabled={busy}
          >
            {busyAction === "restart" ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <RotateCcw className="h-3.5 w-3.5" />
            )}
            {t("lifecycle.restart")}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="border-[var(--danger)]/60 text-[var(--danger)] hover:border-[var(--danger)] hover:text-[var(--danger)]"
            onClick={() => setConfirmAction("stop")}
            disabled={busy}
          >
            {busyAction === "stop" ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Power className="h-3.5 w-3.5" />
            )}
            {t("lifecycle.stop")}
          </Button>
        </div>
      </CardContent>
      <LifecycleConfirmDialog
        action={confirmAction}
        busyAction={busyAction}
        onClose={() => setConfirmAction(null)}
        onAction={onAction}
      />
    </Card>
  );
}

/** Confirms a lifecycle action, runs it and closes; open while `action` is
 *  set. */
function LifecycleConfirmDialog({
  action,
  busyAction,
  onClose,
  onAction,
}: {
  action: ServiceLifecycleAction | null;
  busyAction: ServiceLifecycleAction | null;
  onClose: () => void;
  onAction: (action: ServiceLifecycleAction) => Promise<void>;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const busy = busyAction !== null;
  const currentAction = action ?? "restart";
  const confirmBusy = busyAction === action;

  const run = async () => {
    if (action === null) {
      return;
    }
    await onAction(action);
    onClose();
  };

  return (
    <AlertDialog
      open={action !== null}
      onOpenChange={(open) => {
        if (!open && !busy) {
          onClose();
        }
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t(`lifecycle.confirm.${currentAction}.title`)}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t(`lifecycle.confirm.${currentAction}.description`)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={confirmBusy}>
            {tc("actions.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            disabled={confirmBusy}
            className="border border-[var(--danger)] bg-[var(--danger)] text-bg hover:bg-[var(--danger)]/90"
            onClick={(event) => {
              event.preventDefault();
              void run();
            }}
          >
            {confirmBusy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            {t(`lifecycle.confirm.${currentAction}.confirm`)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Says that the stored account mode waits for a restart, and restarts the
 *  service through the lifecycle action after its confirmation. */
function AccountModeRestartNotice({
  load,
  busyAction,
  onRestart,
}: {
  load: LoadState<AccountModeState>;
  busyAction: ServiceLifecycleAction | null;
  /** Restarts the service; rejects when the restart is refused. */
  onRestart: () => Promise<void>;
}) {
  const { t } = useTranslation("service");
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (load.state !== "ready" || !load.data.restartRequired) {
    return null;
  }

  const restart = async () => {
    setError(null);
    try {
      await onRestart();
    } catch (err) {
      setError(errorText(err));
    }
  };

  return (
    <>
      <Notice
        tone="warn"
        icon={Info}
        title={t("accountMode.restartNotice.title")}
        action={
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setConfirming(true)}
            disabled={busyAction !== null}
          >
            {busyAction === "restart" ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <RotateCcw className="h-3.5 w-3.5" />
            )}
            {t("lifecycle.restart")}
          </Button>
        }
      >
        <p>{t("accountMode.restartNotice.detail")}</p>
        <CompactStaleState load={load} />
        {error !== null && <ErrorBanner message={error} />}
      </Notice>
      <LifecycleConfirmDialog
        action={confirming ? "restart" : null}
        busyAction={busyAction}
        onClose={() => setConfirming(false)}
        onAction={restart}
      />
    </>
  );
}

/** The account-mode tab: the polled mode, its load states and its choice. */
function AccountModeCard({
  poll,
  restart,
  disabled,
}: {
  poll: PollingResult<AccountModeState>;
  restart: () => Promise<void>;
  disabled: boolean;
}) {
  const { t } = useTranslation("service");

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("accountMode.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <AccountModePanel
          poll={poll}
          restart={restart}
          disabled={disabled}
          onSaved={poll.reload}
        />
      </CardContent>
    </Card>
  );
}

function EngineInfoCard({ info }: { info: ServiceInfo }) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  return (
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
                href="https://openpit.dev?officer"
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
                label={t(`engine.profileParams.${row.param}`, {
                  defaultValue: row.param,
                })}
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
  );
}

export function ServiceCard({
  info,
  activeTab,
  onTabChange,
  onDatabaseReset,
  lifecycleBusyAction,
  lifecycleError,
  onLifecycleAction,
  onAccountModeRestart,
}: {
  info: ServiceInfo;
  activeTab: ServiceTab;
  onTabChange: (tab: ServiceTab) => void;
  onDatabaseReset: () => void;
  lifecycleBusyAction: ServiceLifecycleAction | null;
  lifecycleError: string | null;
  onLifecycleAction: (action: ServiceLifecycleAction) => Promise<void>;
  /** Restarts the service under the lifecycle busy state; rejects when the
   *  restart is refused. */
  onAccountModeRestart: () => Promise<void>;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { t: tmcp } = useTranslation("mcp");
  const { load: mcpLoad } = useMcpAccess();
  const accountMode = useAccountMode();
  const mcpCommands = mcpLoad.state === "ready" ? mcpLoad.data : null;
  return (
    <Tabs
      value={activeTab}
      onValueChange={(value) => onTabChange(value as ServiceTab)}
      className="flex flex-col gap-y-4 animate-fade-in"
    >
      <AccountModeRestartNotice
        load={accountMode.load}
        busyAction={lifecycleBusyAction}
        onRestart={onAccountModeRestart}
      />
      <TabsList aria-label={t("tabs.ariaLabel")}>
        {serviceTabs.map(({ id, labelKey, icon: Icon }) => (
          <TabsTrigger
            key={id}
            value={id}
            // Radix reports only a changed value; the click also re-commits
            // the selected tab, which drops a #logs hash.
            onClick={() => onTabChange(id)}
          >
            <Icon className="h-3.5 w-3.5" />
            {t(labelKey)}
          </TabsTrigger>
        ))}
      </TabsList>

      <TabsContent value="application" className="flex flex-col gap-y-4">
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

        <ServiceLifecycleCard
          busyAction={lifecycleBusyAction}
          error={lifecycleError}
          onAction={onLifecycleAction}
        />
      </TabsContent>

      <TabsContent value="engine">
        <EngineInfoCard info={info} />
      </TabsContent>

      <TabsContent value="account-mode" className="flex flex-col gap-y-4">
        <AccountModeCard
          poll={accountMode}
          restart={onAccountModeRestart}
          disabled={lifecycleBusyAction !== null}
        />
      </TabsContent>

      <TabsContent value="database" className="flex flex-col gap-y-4">
        <DatabaseCard database={info.database} onReset={onDatabaseReset} />
        <BackupCard />
      </TabsContent>

      <TabsContent value="api">
        <Card>
          <CardHeader>
            <CardTitle>{t("api.title")}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-y-4">
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
                <div className="flex items-center gap-2">
                  <p className="text-xs font-medium text-muted">
                    {t("api.mcp")}
                  </p>
                  <CompactStaleState load={mcpLoad} />
                </div>
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
                <div className="flex flex-col gap-y-3">
                  <div className="flex flex-col gap-y-1">
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
      </TabsContent>

      <TabsContent value="logs">
        <LogsCard />
      </TabsContent>

      <TabsContent value="security" className="flex flex-col gap-y-4">
        <AccessTokensProvider>
          <AccessSection />
          <StorageSection />
        </AccessTokensProvider>
        <GuideSection />
      </TabsContent>
    </Tabs>
  );
}

export function Service() {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { load, reload } = useService();
  const { restartMarketData, restartService, stopService } = useOfficerApi();
  const location = useLocation();
  const navigate = useNavigate();
  const [restarting, setRestarting] = useState(false);
  const [lifecycleBusyAction, setLifecycleBusyAction] =
    useState<ServiceLifecycleAction | null>(null);
  const [lifecycleError, setLifecycleError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<ServiceTab>("application");
  const scrolledToLogs = useRef(false);
  const refreshing = load.state === "loading";
  const displayedTab: ServiceTab =
    location.hash === "#logs"
      ? "logs"
      : location.hash === "#account-mode"
        ? "account-mode"
        : location.hash === "#security"
          ? "security"
          : activeTab;

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
  // Every restart or stop of this page runs under the one busy state, so
  // while one is in flight no surface offers another.
  const requestLifecycleAction = async (action: ServiceLifecycleAction) => {
    setLifecycleBusyAction(action);
    setLifecycleError(null);
    try {
      if (action === "restart") {
        await restartService();
      } else {
        await stopService();
      }
    } catch (err) {
      setLifecycleBusyAction(null);
      throw err;
    }
  };
  const runLifecycleAction = async (action: ServiceLifecycleAction) => {
    try {
      await requestLifecycleAction(action);
    } catch (err) {
      setLifecycleError(errorText(err));
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
            {tc("actions.restartFeeds")}
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
        <div
          role="status"
          aria-busy="true"
          aria-label={tc("states.loading")}
          className="flex flex-col gap-y-4"
        >
          {[0, 1, 2].map((i) => (
            <Card key={i} className="animate-pulse">
              <CardHeader>
                <div className="h-3 w-20 rounded bg-border" />
              </CardHeader>
              <CardContent className="flex flex-col gap-y-3">
                <div className="h-3 w-full rounded bg-border" />
                <div className="h-3 w-4/5 rounded bg-border" />
              </CardContent>
            </Card>
          ))}
        </div>
      )}
      <StaleState load={load} reload={reload} />
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" && (
        <ServiceCard
          info={load.data}
          activeTab={displayedTab}
          onTabChange={selectTab}
          onDatabaseReset={reload}
          lifecycleBusyAction={lifecycleBusyAction}
          lifecycleError={lifecycleError}
          onLifecycleAction={runLifecycleAction}
          onAccountModeRestart={() => requestLifecycleAction("restart")}
        />
      )}
    </Page>
  );
}
