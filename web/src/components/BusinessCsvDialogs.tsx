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

import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Download, FileArchive } from "lucide-react";

import type {
  BusinessCsvDelimiter,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
} from "@/api/types";
import { ErrorBanner } from "@/components/PageStates";
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
import { useOfficerApi } from "@/framework";

const DELIMITERS: BusinessCsvDelimiter[] = [
  "comma",
  "semicolon",
  "tab",
  "pipe",
];

function errMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
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

  const dialogOpen = open ?? localOpen;
  const setDialogOpen = (next: boolean) => {
    if (open === undefined) {
      setLocalOpen(next);
    }
    onOpenChange?.(next);
  };

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
            onValueChange={(value) =>
              setDelimiter(value as BusinessCsvDelimiter)
            }
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
