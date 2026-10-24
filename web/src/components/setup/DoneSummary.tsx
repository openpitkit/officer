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

import { useTranslation } from "react-i18next";

import type { AccessMode } from "@/api/access";
import type { AccountModeState } from "@/api/accountMode";
import type { CredentialStorage } from "@/api/types";
import type { LoadState } from "@/api/usePolling";
import {
  CompactStaleState,
  ErrorBanner,
  LoadingStatus,
} from "@/components/PageStates";
import type { useSigningKeysPanel } from "@/components/SigningKeysPanel";
import {
  accountModeValue,
  encryptionValue,
  signingValue,
} from "@/components/security/storageValues";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { DecisionStep } from "@/components/setup/steps";
import { cn } from "@/lib/utils";

/**
 * The decisions setup stored and where each one lives now, each value as
 * Service › Security shows it. The rows that leave Officer less protected
 * carry the warning tint.
 */
export function DoneSummary({
  accounts,
  storage,
  signing,
  accessMode,
  places,
}: {
  /** The polled account mode. */
  accounts: LoadState<AccountModeState>;
  /** The polled credential storage. */
  storage: LoadState<CredentialStorage>;
  signing: ReturnType<typeof useSigningKeysPanel>;
  accessMode: AccessMode;
  /** Where each decision lives in the panel. */
  places: Record<DecisionStep, string>;
}) {
  const { t } = useTranslation("setup");
  const { t: ts } = useTranslation("service");
  // The polls retry by themselves, so a failed one shows its error until
  // the next answer.
  for (const load of [accounts, signing.load, storage]) {
    if (load.state === "error") {
      return <ErrorBanner message={load.error} />;
    }
  }
  if (
    accounts.state !== "ready" ||
    signing.load.state !== "ready" ||
    storage.state !== "ready"
  ) {
    return <LoadingStatus />;
  }

  const { mode, account } = accounts.data;
  const sealed = storage.data.credentialsSealed;
  const { eSignEnabled, activeKey } = signing;
  const rows: {
    id: DecisionStep;
    choice: string;
    /** The poll the value comes from, marked when it is stale. */
    load?: LoadState<unknown>;
    flagged: boolean;
  }[] = [
    {
      id: "accounts",
      choice:
        mode === null
          ? ts("accountMode.notChosen")
          : accountModeValue(ts, mode === "multi", account),
      load: accounts,
      flagged: false,
    },
    {
      id: "encryption",
      choice: encryptionValue(ts, storage.data),
      load: storage,
      flagged: !sealed,
    },
    {
      id: "signing",
      choice: signingValue(ts, eSignEnabled, activeKey),
      load: signing.load,
      flagged: !eSignEnabled || activeKey === null,
    },
    {
      id: "access",
      choice: ts(`access.modes.${accessMode}.title`),
      flagged: accessMode === "open",
    },
    {
      id: "agent",
      choice: t(
        accessMode === "token" ? "done.values.agent" : "done.values.agentOpen",
      ),
      flagged: false,
    },
  ];

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t("done.columns.decision")}</TableHead>
          <TableHead>{t("done.columns.choice")}</TableHead>
          <TableHead>{t("done.columns.where")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row) => (
          <TableRow
            key={row.id}
            className={cn(row.flagged && "bg-[var(--warn-dim)]")}
          >
            <TableCell>{t(`rail.steps.${row.id}`)}</TableCell>
            <TableCell>
              <div className="flex flex-wrap items-center gap-2">
                <span>{row.choice}</span>
                {row.load !== undefined && (
                  <CompactStaleState load={row.load} />
                )}
              </div>
            </TableCell>
            <TableCell className="text-muted-lt">{places[row.id]}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
