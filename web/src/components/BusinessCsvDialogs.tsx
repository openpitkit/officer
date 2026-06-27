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

import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Download, FileArchive, Upload } from "lucide-react";

import type {
  BusinessCsvConflictPolicy,
  BusinessCsvDelimiter,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  BusinessCsvImportEntity,
  BusinessCsvImportPreview,
  BusinessCsvImportResult,
} from "@/api/types";
import { ErrorBanner } from "@/components/PageStates";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { useOfficerApi } from "@/framework";

const DELIMITERS: BusinessCsvDelimiter[] = [
  "comma",
  "semicolon",
  "tab",
  "pipe",
];

const CONFLICT_POLICIES: BusinessCsvConflictPolicy[] = [
  "skip",
  "replace",
  "stop",
];

const MAX_IMPORT_FILE_BYTES = 40 * 1024 * 1024;

function errMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunkSize = 0x8000;
  for (let i = 0; i < bytes.length; i += chunkSize) {
    const chunk = bytes.slice(i, i + chunkSize);
    binary += String.fromCharCode(...chunk);
  }
  return btoa(binary);
}

async function fileToBase64(file: File): Promise<string> {
  return bytesToBase64(new Uint8Array(await file.arrayBuffer()));
}

function textToBase64(text: string): string {
  return bytesToBase64(new TextEncoder().encode(text));
}

function downloadBlob(blob: Blob, filename: string) {
  const href =
    typeof URL.createObjectURL === "function"
      ? URL.createObjectURL(blob)
      : "";
  const anchor = document.createElement("a");
  anchor.href = href;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  if (href && typeof URL.revokeObjectURL === "function") {
    URL.revokeObjectURL(href);
  }
}

interface PendingImport {
  delimiter: BusinessCsvDelimiter;
  entity: BusinessCsvImportEntity;
  filename: string;
  payloadBase64: string;
}

function CountsLine({
  value,
}: {
  value: BusinessCsvImportPreview | BusinessCsvImportResult;
}) {
  const { t } = useTranslation("common");
  return (
    <p className="text-[0.6875rem] text-muted-lt">
      {t("businessCsv.import.counts", {
        rows: value.counts.rows,
        applied: value.counts.applied,
        skipped: value.counts.skipped,
        conflicts: value.counts.conflicts,
      })}
    </p>
  );
}

export function BusinessCsvImportDialog({
  defaultEntity,
  entities,
  onImported,
  open,
  onOpenChange,
  trigger,
  triggerLabel,
}: {
  defaultEntity?: BusinessCsvImportEntity;
  entities: BusinessCsvImportEntity[];
  onImported: () => void;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  trigger?: (open: () => void) => ReactNode;
  triggerLabel?: string;
}) {
  const { t } = useTranslation("common");
  const { importBusinessCsv, previewBusinessCsvImport } = useOfficerApi();
  const initialEntity =
    defaultEntity !== undefined && entities.includes(defaultEntity)
      ? defaultEntity
      : entities[0];
  const [localOpen, setLocalOpen] = useState(false);
  const [entity, setEntity] = useState<BusinessCsvImportEntity>(initialEntity);
  const [delimiter, setDelimiter] =
    useState<BusinessCsvDelimiter>("comma");
  const [file, setFile] = useState<File | null>(null);
  const [text, setText] = useState("");
  const [pending, setPending] = useState<PendingImport | null>(null);
  const [preview, setPreview] = useState<BusinessCsvImportPreview | null>(null);
  const [result, setResult] = useState<BusinessCsvImportResult | null>(null);
  const [conflictOpen, setConflictOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  function reset() {
    setEntity(initialEntity);
    setDelimiter("comma");
    setFile(null);
    setText("");
    setPending(null);
    setPreview(null);
    setResult(null);
    setConflictOpen(false);
    setBusy(false);
    setError(null);
    if (fileRef.current) {
      fileRef.current.value = "";
    }
  }

  const dialogOpen = open ?? localOpen;
  const setDialogOpen = (next: boolean) => {
    if (open === undefined) {
      setLocalOpen(next);
    }
    onOpenChange?.(next);
  };

  function clearOutcome() {
    setPending(null);
    setPreview(null);
    setResult(null);
    setError(null);
  }

  async function buildImport(): Promise<PendingImport> {
    if (file) {
      return {
        entity,
        delimiter,
        filename: file.name,
        payloadBase64: await fileToBase64(file),
      };
    }
    return {
      entity,
      delimiter,
      filename: "pasted.csv",
      payloadBase64: textToBase64(text),
    };
  }

  async function runPreview() {
    if (file && file.size > MAX_IMPORT_FILE_BYTES) {
      setError(
        t("businessCsv.import.fileTooLarge", {
          limit: MAX_IMPORT_FILE_BYTES / 1024 / 1024,
        }),
      );
      setResult(null);
      return;
    }
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const next = await buildImport();
      const nextPreview = await previewBusinessCsvImport(next);
      setPending(next);
      setPreview(nextPreview);
      if (nextPreview.conflicts.length > 0) {
        setConflictOpen(true);
      }
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function apply(policy: BusinessCsvConflictPolicy) {
    if (!pending) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const next = await importBusinessCsv({ ...pending, conflictPolicy: policy });
      setResult(next);
      setConflictOpen(false);
      onImported();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const hasInput = file !== null || text.trim().length > 0;
  const canApplyDirect =
    pending !== null &&
    preview !== null &&
    preview.conflicts.length === 0 &&
    result === null;

  return (
    <>
      <Dialog
        open={dialogOpen}
        onOpenChange={(next) => {
          setDialogOpen(next);
          if (!next) {
            reset();
          }
        }}
      >
        {trigger ? (
          trigger(() => setDialogOpen(true))
        ) : (
          <Button size="sm" variant="outline" onClick={() => setDialogOpen(true)}>
            <Upload className="h-3.5 w-3.5" />
            {triggerLabel ?? t("businessCsv.import.trigger")}
          </Button>
        )}
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("businessCsv.import.title")}</DialogTitle>
            <DialogDescription>
              {t("businessCsv.import.description")}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-1 rounded-card border border-border bg-surface-2 p-3">
            <p className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
              {t("businessCsv.format.heading")}
            </p>
            <p className="text-xs text-muted-lt">
              {t(`businessCsv.format.${entity}.description`)}
            </p>
            <code className="block whitespace-pre-wrap break-words rounded-card bg-bg p-2 text-[0.6875rem] text-text">
              {t(`businessCsv.format.${entity}.columns`)}
            </code>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label>{t("businessCsv.entityLabel")}</Label>
              <Select
                value={entity}
                onValueChange={(v) => {
                  setEntity(v as BusinessCsvImportEntity);
                  clearOutcome();
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {entities.map((value) => (
                    <SelectItem key={value} value={value}>
                      {t(`businessCsv.entities.${value}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>{t("businessCsv.delimiterLabel")}</Label>
              <Select
                value={delimiter}
                onValueChange={(v) => {
                  setDelimiter(v as BusinessCsvDelimiter);
                  clearOutcome();
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {DELIMITERS.map((value) => (
                    <SelectItem key={value} value={value}>
                      {t(`businessCsv.delimiters.${value}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="space-y-2">
            <input
              ref={fileRef}
              type="file"
              accept=".csv,.zip,text/csv,application/zip"
              className="hidden"
              onChange={(event) => {
                setFile(event.target.files?.[0] ?? null);
                clearOutcome();
              }}
            />
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => fileRef.current?.click()}
                disabled={busy}
              >
                <FileArchive className="h-3.5 w-3.5" />
                {t("businessCsv.import.chooseFile")}
              </Button>
              <span className="text-[0.6875rem] text-muted-lt">
                {file?.name ?? t("businessCsv.import.noFile")}
              </span>
            </div>
            <Textarea
              className="min-h-28 font-mono text-xs"
              placeholder={t("businessCsv.import.pastePlaceholder")}
              value={text}
              spellCheck={false}
              disabled={busy || file !== null}
              onChange={(event) => {
                setText(event.target.value);
                clearOutcome();
              }}
            />
            {file !== null && (
              <p className="text-[0.6875rem] text-muted-lt">
                {t("businessCsv.import.fileTakesPriority")}
              </p>
            )}
          </div>

          {error && <ErrorBanner message={error} />}
          {preview && (
            <div className="space-y-1 rounded-card border border-border p-3">
              <p className="text-xs font-medium text-text">
                {t("businessCsv.import.previewReady", {
                  file: preview.file.name,
                })}
              </p>
              <CountsLine value={preview} />
              {preview.conflicts.length > 0 && (
                <p className="text-[0.6875rem] text-[var(--warn)]">
                  {t("businessCsv.import.conflictsFound", {
                    count: preview.conflicts.length,
                  })}
                </p>
              )}
            </div>
          )}
          {result && (
            <div className="space-y-1 rounded-card border border-[var(--ok)] p-3">
              <p className="text-xs font-medium text-[var(--ok)]">
                {t("businessCsv.import.applied")}
              </p>
              <CountsLine value={result} />
            </div>
          )}

          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setDialogOpen(false)}
              disabled={busy}
            >
              {t("actions.close")}
            </Button>
            {canApplyDirect ? (
              <Button size="sm" onClick={() => void apply("skip")} disabled={busy}>
                <Upload className="h-3.5 w-3.5" />
                {t("businessCsv.import.apply")}
              </Button>
            ) : (
              <Button
                size="sm"
                onClick={() => void runPreview()}
                disabled={busy || !hasInput}
              >
                <Upload className="h-3.5 w-3.5" />
                {busy
                  ? t("businessCsv.import.previewing")
                  : t("businessCsv.import.preview")}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog open={conflictOpen} onOpenChange={setConflictOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("businessCsv.import.conflictTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("businessCsv.import.conflictDescription", {
                count: preview?.conflicts.length ?? 0,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {error && <ErrorBanner message={error} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {t("actions.cancel")}
            </AlertDialogCancel>
            {CONFLICT_POLICIES.map((policy) => (
              <Button
                key={policy}
                disabled={busy}
                onClick={() => void apply(policy)}
              >
                {t(`businessCsv.conflictPolicy.${policy}`)}
              </Button>
            ))}
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

export function BusinessCsvExportDialog({
  entity,
  filters,
  open,
  onOpenChange,
  trigger,
  triggerLabel,
}: {
  entity: BusinessCsvEntity;
  filters?: BusinessCsvExportFilters;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  trigger?: (open: () => void) => ReactNode;
  triggerLabel?: string;
}) {
  const { t } = useTranslation("common");
  const { exportBusinessCsv } = useOfficerApi();
  const [localOpen, setLocalOpen] = useState(false);
  const [delimiter, setDelimiter] =
    useState<BusinessCsvDelimiter>("comma");
  const [zip, setZip] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function runExport() {
    setBusy(true);
    setError(null);
    try {
      const result = await exportBusinessCsv({
        entity,
        delimiter,
        filters,
        zip,
      });
      downloadBlob(result.blob, result.filename);
      setDialogOpen(false);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const dialogOpen = open ?? localOpen;
  const setDialogOpen = (next: boolean) => {
    if (open === undefined) {
      setLocalOpen(next);
    }
    onOpenChange?.(next);
  };

  return (
    <Dialog
      open={dialogOpen}
      onOpenChange={(next) => {
        setDialogOpen(next);
        if (!next) {
          setDelimiter("comma");
          setZip(false);
          setBusy(false);
          setError(null);
        }
      }}
    >
      {trigger ? (
        trigger(() => setDialogOpen(true))
      ) : (
        <Button size="sm" variant="outline" onClick={() => setDialogOpen(true)}>
          <Download className="h-3.5 w-3.5" />
          {triggerLabel ?? t("businessCsv.export.trigger")}
        </Button>
      )}
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{t("businessCsv.export.title")}</DialogTitle>
          <DialogDescription>
            {t("businessCsv.export.description", {
              entity: t(`businessCsv.entities.${entity}`),
            })}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-1 rounded-card border border-border bg-surface-2 p-3">
          <p className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
            {t("businessCsv.format.heading")}
          </p>
          <p className="text-xs text-muted-lt">
            {t(`businessCsv.format.${entity}.description`)}
          </p>
          <code className="block whitespace-pre-wrap break-words rounded-card bg-bg p-2 text-[0.6875rem] text-text">
            {t(`businessCsv.format.${entity}.columns`)}
          </code>
        </div>

        <div className="space-y-1.5">
          <Label>{t("businessCsv.delimiterLabel")}</Label>
          <Select
            value={delimiter}
            onValueChange={(v) => setDelimiter(v as BusinessCsvDelimiter)}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {DELIMITERS.map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`businessCsv.delimiters.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <label className="flex items-center gap-2 text-xs text-text">
          <Input
            type="checkbox"
            className="h-4 w-4"
            checked={zip}
            onChange={(event) => setZip(event.target.checked)}
          />
          <span>{t("businessCsv.export.zip")}</span>
        </label>

        {error && <ErrorBanner message={error} />}

        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setDialogOpen(false)}
            disabled={busy}
          >
            {t("actions.cancel")}
          </Button>
          <Button size="sm" onClick={() => void runExport()} disabled={busy}>
            {zip ? (
              <FileArchive className="h-3.5 w-3.5" />
            ) : (
              <Download className="h-3.5 w-3.5" />
            )}
            {busy
              ? t("businessCsv.export.exporting")
              : t("businessCsv.export.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
