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

import { DatabaseBackup, Download, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { usePreMigrationBackups } from "@/api/usePreMigrationBackups";
import { Notice } from "@/components/Notice";
import { CompactStaleState, ErrorBanner } from "@/components/PageStates";
import { Button } from "@/components/ui/button";
import { useOfficerApi } from "@/framework";
import { formatDateTime, formatNumber } from "@/i18n/format";
import { downloadBlob } from "@/lib/download";

const BYTES_PER_MEGABYTE = 1_000_000;

/** Offers the raw database copies Officer wrote before a migration for
 *  download and deletion on every page; Officer never deletes them itself. */
export function PreMigrationBackupBanner() {
  const { t } = useTranslation();
  const { load, reload } = usePreMigrationBackups();
  const { downloadPreMigrationBackup, deletePreMigrationBackup } =
    useOfficerApi();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (load.state === "loading") {
    return null;
  }
  // No offer is on screen: a failed read must say what it could not list.
  if (load.state === "error" || load.data.length === 0) {
    return load.error === null ? null : (
      <ErrorBanner
        message={t("preMigrationBackup.unknown", { error: load.error })}
      />
    );
  }

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const download = (name: string) =>
    run(async () => {
      const { blob, filename } = await downloadPreMigrationBackup(name);
      downloadBlob(blob, filename);
    });

  const remove = async (name: string) => {
    if (!window.confirm(t("preMigrationBackup.deleteConfirm", { name }))) {
      return;
    }
    await run(async () => {
      await deletePreMigrationBackup(name);
      reload();
    });
  };

  return (
    <Notice
      tone="info"
      icon={DatabaseBackup}
      title={t("preMigrationBackup.title")}
    >
      <p>{t("preMigrationBackup.detail")}</p>
      <p>{t("preMigrationBackup.neverDeleted")}</p>
      <ul className="flex flex-col gap-y-2 pt-1">
        {load.data.map((copy) => (
          <li
            key={copy.name}
            className="flex flex-wrap items-center justify-between gap-2"
          >
            <span>
              {t("preMigrationBackup.entry", {
                time: formatDateTime(copy.createdAt),
                size: formatNumber(copy.sizeBytes / BYTES_PER_MEGABYTE, {
                  style: "unit",
                  unit: "megabyte",
                  maximumSignificantDigits: 3,
                }),
              })}
            </span>
            <span className="flex gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => {
                  void download(copy.name);
                }}
              >
                <Download />
                {t("actions.download")}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => {
                  void remove(copy.name);
                }}
              >
                <Trash2 />
                {t("actions.delete")}
              </Button>
            </span>
          </li>
        ))}
      </ul>
      {error !== null && <ErrorBanner message={error} />}
      <CompactStaleState load={load} />
    </Notice>
  );
}
