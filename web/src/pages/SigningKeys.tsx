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

import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { SigningKey, SigningKeyFormat } from "@/api/types";
import { useSigningKeys } from "@/api/useSigningKeys";
import { CopyableSnippet } from "@/components/CopyableSnippet";
import {
  ErrorBanner,
  ErrorState,
  StaleState,
  TableSkeleton,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import { StatRow } from "@/components/StatRow";
import { Badge } from "@/components/ui/badge";
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
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { ApiError, useOfficerApi } from "@/framework";
import { formatDateTime } from "@/i18n/format";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

// ---------------------------------------------------------------------------
// Card A - Active key
// ---------------------------------------------------------------------------

function ActiveKeyCard({ activeKey }: { activeKey: SigningKey | null }) {
  const { t } = useTranslation("approvalKeys");
  const { exportPublicKey } = useOfficerApi();

  const [format, setFormat] = useState<SigningKeyFormat>("pem-pkcs8");
  const [exportedKey, setExportedKey] = useState<string | null>(null);
  const [exportError, setExportError] = useState<string | null>(null);
  const [exporting, setExporting] = useState(false);

  async function handleExport(fmt: SigningKeyFormat) {
    setExporting(true);
    setExportError(null);
    try {
      const pub = await exportPublicKey(fmt);
      setExportedKey(pub);
    } catch (err) {
      setExportError(errMessage(err));
    } finally {
      setExporting(false);
    }
  }

  function handleFormatChange(v: string) {
    const fmt = v as SigningKeyFormat;
    setFormat(fmt);
    setExportedKey(null);
    setExportError(null);
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("currentKey.sectionTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {activeKey === null ? (
          <p className="text-xs text-muted-lt">{t("currentKey.empty")}</p>
        ) : (
          <>
            <StatRow
              label={t("currentKey.keyId")}
              value={
                <span className="font-mono text-xs">{activeKey.keyId}</span>
              }
              mono={false}
            />
            <StatRow
              label={t("currentKey.fingerprint")}
              value={
                <span className="font-mono text-xs">
                  {activeKey.fingerprint}
                </span>
              }
              mono={false}
            />
            <StatRow
              label={t("currentKey.createdAt")}
              value={formatDateTime(activeKey.createdAt)}
            />
            <StatRow
              label={t("currentKey.status")}
              value={<Badge variant="ok">{t("currentKey.statusActive")}</Badge>}
              mono={false}
            />

            <div className="space-y-2 pt-1">
              <Label className="text-xs text-muted">
                {t("currentKey.export.label")}
              </Label>
              <div className="flex items-center gap-2">
                <Select
                  value={format}
                  onValueChange={handleFormatChange}
                  disabled={exporting}
                >
                  <SelectTrigger className="h-8 w-48 text-xs">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="pem-pkcs8">
                      {t("currentKey.export.formats.pem-pkcs8")}
                    </SelectItem>
                    <SelectItem value="openssh">
                      {t("currentKey.export.formats.openssh")}
                    </SelectItem>
                    <SelectItem value="raw-base64">
                      {t("currentKey.export.formats.raw-base64")}
                    </SelectItem>
                  </SelectContent>
                </Select>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => void handleExport(format)}
                  disabled={exporting}
                >
                  {t("currentKey.export.formatLabel")}
                </Button>
              </div>
              {exportError && (
                <p className="text-xs text-[var(--danger)]">{exportError}</p>
              )}
              {exportedKey !== null && (
                <CopyableSnippet text={exportedKey} rows={4} />
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Card B - Generate / Import
// ---------------------------------------------------------------------------

function ManageKeyCard({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation("approvalKeys");
  const { t: tc } = useTranslation();
  const { generateSigningKey, importSigningKey } = useOfficerApi();

  const [mode, setMode] = useState<"generate" | "import">("generate");
  const [importFormat, setImportFormat] =
    useState<SigningKeyFormat>("pem-pkcs8");
  const [importKey, setImportKey] = useState("");
  const [importBusy, setImportBusy] = useState(false);
  const [importError, setImportError] = useState<string | null>(null);
  const [generateDialogOpen, setGenerateDialogOpen] = useState(false);
  const [generateBusy, setGenerateBusy] = useState(false);
  const [generateError, setGenerateError] = useState<string | null>(null);

  async function handleGenerate() {
    setGenerateBusy(true);
    setGenerateError(null);
    try {
      await generateSigningKey();
      setGenerateDialogOpen(false);
      onDone();
    } catch (err) {
      setGenerateError(errMessage(err) || t("manageKey.errors.generateFailed"));
    } finally {
      setGenerateBusy(false);
    }
  }

  async function handleImport() {
    const key = importKey.trim();
    if (!key) {
      setImportError(t("manageKey.errors.importEmpty"));
      return;
    }
    setImportBusy(true);
    setImportError(null);
    try {
      await importSigningKey(key, importFormat);
      setImportKey("");
      onDone();
    } catch (err) {
      setImportError(errMessage(err) || t("manageKey.errors.importFailed"));
    } finally {
      setImportBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("manageKey.sectionTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* Mode toggle */}
        <div className="flex w-fit gap-1 rounded-card border border-border bg-surface-2 p-1">
          {(["generate", "import"] as const).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => {
                setMode(m);
                setImportError(null);
                setGenerateError(null);
              }}
              className={[
                "rounded-badge px-3 py-1 text-xs font-medium transition-colors duration-[180ms]",
                mode === m
                  ? "bg-accent-dim text-accent"
                  : "text-muted-lt hover:bg-surface-hover hover:text-text",
              ].join(" ")}
            >
              {m === "generate"
                ? t("manageKey.modeGenerate")
                : t("manageKey.modeImport")}
            </button>
          ))}
        </div>

        {mode === "generate" && (
          <div>
            <p className="mb-3 text-xs text-muted-lt">
              {t("manageKey.generateDialog.description")}
            </p>
            <Button
              size="sm"
              onClick={() => setGenerateDialogOpen(true)}
              disabled={generateBusy}
            >
              {t("manageKey.generateButton")}
            </Button>
            {generateError && (
              <p className="mt-2 text-xs text-[var(--danger)]">
                {generateError}
              </p>
            )}
          </div>
        )}

        {mode === "import" && (
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label className="text-xs">{t("manageKey.importFormat")}</Label>
              <Select
                value={importFormat}
                onValueChange={(v) => setImportFormat(v as SigningKeyFormat)}
                disabled={importBusy}
              >
                <SelectTrigger className="h-8 w-56 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="pem-pkcs8">
                    {t("currentKey.export.formats.pem-pkcs8")}
                  </SelectItem>
                  <SelectItem value="openssh">
                    {t("currentKey.export.formats.openssh")}
                  </SelectItem>
                  <SelectItem value="raw-base64">
                    {t("currentKey.export.formats.raw-base64")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">{t("manageKey.importKeyLabel")}</Label>
              <Textarea
                value={importKey}
                onChange={(e) => setImportKey(e.target.value)}
                placeholder={t("manageKey.importKeyPlaceholder")}
                disabled={importBusy}
                rows={5}
                className="resize-none font-mono text-xs"
                autoComplete="off"
                autoCorrect="off"
                spellCheck={false}
              />
            </div>
            {importError && (
              <p className="text-xs text-[var(--danger)]">{importError}</p>
            )}
            <Button
              size="sm"
              onClick={() => void handleImport()}
              disabled={importBusy}
            >
              {importBusy
                ? t("manageKey.importBusy")
                : t("manageKey.importButton")}
            </Button>
          </div>
        )}

        {/* Generate confirm dialog */}
        <AlertDialog
          open={generateDialogOpen}
          onOpenChange={(open) => {
            setGenerateDialogOpen(open);
            if (!open) setGenerateError(null);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t("manageKey.generateDialog.title")}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t("manageKey.generateDialog.description")}
              </AlertDialogDescription>
            </AlertDialogHeader>
            {generateError && (
              <p className="text-xs text-[var(--danger)]">{generateError}</p>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={generateBusy}>
                {tc("actions.cancel")}
              </AlertDialogCancel>
              <AlertDialogAction
                onClick={(e) => {
                  e.preventDefault();
                  void handleGenerate();
                }}
                disabled={generateBusy}
              >
                {t("manageKey.generateDialog.confirm")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Card C - eSign toggle
// ---------------------------------------------------------------------------

function ESignCard({
  enabled,
  busy,
  onToggle,
}: {
  enabled: boolean;
  busy: boolean;
  onToggle: (next: boolean) => void;
}) {
  const { t } = useTranslation("approvalKeys");

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("eSign.sectionTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <label className="flex cursor-pointer items-center gap-3 select-none">
          <input
            type="checkbox"
            checked={enabled}
            disabled={busy}
            onChange={(e) => onToggle(e.target.checked)}
            className="accent-[var(--accent)] h-4 w-4"
          />
          <span className="text-sm text-text">{t("eSign.label")}</span>
        </label>
        <p className="text-xs text-muted-lt">{t("eSign.description")}</p>
        {busy && <p className="text-xs text-muted">{t("eSign.busy")}</p>}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function SigningKeys() {
  const { t } = useTranslation("approvalKeys");
  const { setESignEnabled } = useOfficerApi();
  const { load, reload } = useSigningKeys();

  const [eSignBusy, setESignBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Local eSign override so the toggle reflects immediately.
  const [localESign, setLocalESign] = useState<boolean | null>(null);

  const status = load.state === "ready" ? load.data : null;
  const activeKey = status?.keys.find((k) => k.active) ?? null;
  const eSignEnabled = localESign ?? status?.eSignEnabled ?? false;

  async function handleESignToggle(next: boolean) {
    if (next && activeKey === null) {
      setLocalESign(false);
      setError(t("eSign.missingKeyError"));
      return;
    }
    setLocalESign(next);
    setError(null);
    setESignBusy(true);
    try {
      await setESignEnabled(next);
    } catch (err) {
      setLocalESign(!next);
      setError(errMessage(err));
    } finally {
      setESignBusy(false);
    }
  }

  const isLoading = load.state === "loading";

  return (
    <Page
      title={t("title")}
      actions={
        <RefreshButton
          onClick={() => {
            setLocalESign(null);
            reload();
          }}
          busy={isLoading}
        />
      }
    >
      {error && (
        <ErrorBanner message={error} onDismiss={() => setError(null)} />
      )}

      <StaleState load={load} reload={reload} />
      {load.state === "error" && (
        <ErrorState
          message={load.error}
          onRetry={() => {
            setLocalESign(null);
            reload();
          }}
        />
      )}

      {load.state === "loading" && <TableSkeleton rows={3} cols={1} />}

      {load.state === "ready" && (
        <div className="space-y-4">
          <ActiveKeyCard activeKey={activeKey} />

          <ManageKeyCard
            onDone={() => {
              setLocalESign(null);
              reload();
            }}
          />

          <ESignCard
            enabled={eSignEnabled}
            busy={eSignBusy}
            onToggle={(next) => {
              if (!eSignBusy) {
                void handleESignToggle(next);
              }
            }}
          />
        </div>
      )}
    </Page>
  );
}
