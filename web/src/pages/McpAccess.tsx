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

import type { McpCommand } from "@/api/types";
import { useMcpAccess } from "@/api/useMcpAccess";
import { ConnectAgent } from "@/components/ConnectAgent";
import {
  ErrorBanner,
  ErrorState,
  StaleState,
  TableSkeleton,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ApiError, ColumnHeader, useOfficerApi } from "@/framework";

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
// Protective-enable confirmation dialog
// ---------------------------------------------------------------------------

function ProtectiveEnableDialog({
  command,
  open,
  onOpenChange,
  onConfirm,
}: {
  command: McpCommand | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
}) {
  const { t } = useTranslation("mcp");
  const { t: tc } = useTranslation();
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="text-[var(--danger)]">
            {t("dialog.title")}
          </AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="space-y-2 text-xs text-muted-lt">
              <p>
                {t("dialog.body1Pre")}{" "}
                <span className="nums font-medium text-text">
                  {command?.name}
                </span>{" "}
                {t("dialog.body1Mid")}{" "}
                <span className="font-medium text-[var(--danger)]">
                  {t("dialog.body1Flag")}
                </span>
                {t("dialog.body1Post")}
              </p>
              <p>{t("dialog.body2")}</p>
              <p>
                {t("dialog.body3Pre")} <strong>{tc("actions.cancel")}</strong>{" "}
                {t("dialog.body3Post")}
              </p>
            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{tc("actions.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              onConfirm();
            }}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            {t("dialog.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Commands table
// ---------------------------------------------------------------------------

function CommandsTable({
  commands,
  onToggle,
}: {
  commands: McpCommand[];
  onToggle: (cmd: McpCommand, next: boolean) => void;
}) {
  const { t } = useTranslation("mcp");
  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="w-8">
            <ColumnHeader description={t("table.columnDescriptions.on")}>
              {t("table.on")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.command")}>
              {t("table.command")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader
              description={t("table.columnDescriptions.description")}
            >
              {t("table.description")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.flags")}>
              {t("table.flags")}
            </ColumnHeader>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {commands.map((cmd) => (
          <TableRow key={cmd.name}>
            <TableCell>
              <input
                type="checkbox"
                checked={cmd.enabled}
                onChange={(e) => onToggle(cmd, e.target.checked)}
                className="accent-[var(--accent)] h-4 w-4 cursor-pointer"
                aria-label={t("table.enableAriaLabel", { name: cmd.name })}
              />
            </TableCell>

            <TableCell>
              <div className="space-y-0.5">
                <p
                  className={
                    cmd.protective
                      ? "text-sm font-semibold text-[var(--danger)]"
                      : "text-sm font-medium text-text"
                  }
                >
                  {cmd.title}
                </p>
                <p className="nums text-[0.6875rem] text-muted-lt">
                  {cmd.name}
                </p>
                {!cmd.enabled && (
                  <p className="text-[0.6875rem] text-muted-lt italic">
                    {t("table.disabledNotice")}
                  </p>
                )}
              </div>
            </TableCell>

            <TableCell className="max-w-xs text-xs text-muted-lt">
              {t("command." + cmd.name + ".description", {
                defaultValue: cmd.agentDescription || t("table.noDescription"),
              })}
            </TableCell>

            <TableCell>
              <div className="flex flex-wrap gap-1">
                {cmd.protective && (
                  <Badge variant="danger">{t("flags.protected")}</Badge>
                )}
                {cmd.mutating && (
                  <Badge variant="warn">{t("flags.mutating")}</Badge>
                )}
                {!cmd.implemented && (
                  <Badge variant="neutral">{t("flags.notImplemented")}</Badge>
                )}
              </div>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function McpAccess() {
  const { load, reload } = useMcpAccess();
  const { setMcpCommand } = useOfficerApi();

  // Local snapshot so toggling reflects immediately while the PUT is in flight.
  const [localCommands, setLocalCommands] = useState<McpCommand[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Pending protective enable confirmation.
  const [pendingProtective, setPendingProtective] = useState<McpCommand | null>(
    null,
  );

  const commands = localCommands ?? (load.state === "ready" ? load.data : null);

  function applyUpdate(updated: McpCommand) {
    setLocalCommands((prev) => {
      const base = prev ?? (load.state === "ready" ? load.data : []);
      return base.map((c) => (c.name === updated.name ? updated : c));
    });
  }

  async function doToggle(cmd: McpCommand, next: boolean) {
    // Optimistic update.
    applyUpdate({ ...cmd, enabled: next });
    setError(null);
    try {
      const updated = await setMcpCommand(cmd.name, next);
      applyUpdate(updated);
    } catch (err) {
      // Revert on failure.
      applyUpdate({ ...cmd, enabled: cmd.enabled });
      setError(errMessage(err));
    }
  }

  function handleToggle(cmd: McpCommand, next: boolean) {
    if (next && cmd.protective) {
      setPendingProtective(cmd);
      return;
    }
    void doToggle(cmd, next);
  }

  const { t } = useTranslation("mcp");

  const isLoading = load.state === "loading";

  return (
    <Page
      title={t("access.title")}
      actions={
        <RefreshButton
          onClick={() => {
            setLocalCommands(null);
            reload();
          }}
          busy={isLoading}
        />
      }
    >
      <ConnectAgent />

      <p className="text-xs text-muted-lt">
        {t("access.descriptionPre")}{" "}
        <span className="font-medium text-[var(--danger)]">
          {t("access.descriptionProtected")}
        </span>{" "}
        {t("access.descriptionPost")} <span className="nums">/mcp</span>
        {t("access.descriptionEnd")}
      </p>

      {error && (
        <ErrorBanner message={error} onDismiss={() => setError(null)} />
      )}

      {isLoading && commands === null && <TableSkeleton cols={4} />}

      <StaleState load={load} reload={reload} />
      {load.state === "error" && commands === null && (
        <ErrorState
          message={load.error}
          onRetry={() => {
            setLocalCommands(null);
            reload();
          }}
        />
      )}

      {commands !== null && (
        <CommandsTable commands={commands} onToggle={handleToggle} />
      )}

      <ProtectiveEnableDialog
        command={pendingProtective}
        open={pendingProtective !== null}
        onOpenChange={(next) => {
          if (!next) setPendingProtective(null);
        }}
        onConfirm={() => {
          if (pendingProtective) {
            void doToggle(pendingProtective, true);
          }
          setPendingProtective(null);
        }}
      />
    </Page>
  );
}
