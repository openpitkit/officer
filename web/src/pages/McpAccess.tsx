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

import { ApiError, setMcpCommand } from "@/api/client";
import type { McpCommand } from "@/api/types";
import { useMcpAccess } from "@/api/useMcpAccess";
import { CopyableSnippet } from "@/components/CopyableSnippet";
import { ErrorBanner, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import { Badge } from "@/components/ui/badge";
import { buildMcpAgentPrompt } from "@/lib/mcpAgentPrompt";
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
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

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
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="text-[var(--danger)]">
            Enable protected command?
          </AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="space-y-2 text-xs text-muted-lt">
              <p>
                The command{" "}
                <span className="nums font-medium text-text">
                  {command?.name}
                </span>{" "}
                is marked <span className="font-medium text-[var(--danger)]">
                  protective
                </span>. Enabling it grants the AI agent the ability to change
                the database in ways the operator cannot supervise or control in
                real time.
              </p>
              <p>
                This is a protective barrier against autonomous AI actions. Only
                enable it if you intend to grant the agent that power and accept
                the associated risk.
              </p>
              <p>
                If you are unsure, press <strong>Cancel</strong> — the command
                will remain disabled.
              </p>
            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              onConfirm();
            }}
            className="bg-[var(--danger)] text-white hover:bg-[var(--danger)]/90"
          >
            Enable anyway
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
  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="w-8">On</TableHead>
            <TableHead>Command</TableHead>
            <TableHead>Description</TableHead>
            <TableHead>Flags</TableHead>
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
                  aria-label={`Enable ${cmd.name}`}
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
                      Disabled — returns a "disabled in panel" notice to the
                      agent.
                    </p>
                  )}
                </div>
              </TableCell>

              <TableCell className="max-w-xs text-xs text-muted-lt">
                {cmd.agentDescription || "—"}
              </TableCell>

              <TableCell>
                <div className="flex flex-wrap gap-1">
                  {cmd.protective && (
                    <Badge variant="danger">protected</Badge>
                  )}
                  {cmd.mutating && (
                    <Badge variant="warn">mutating</Badge>
                  )}
                  {!cmd.implemented && (
                    <Badge variant="neutral">not implemented</Badge>
                  )}
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function McpAccess() {
  const { load, reload } = useMcpAccess();

  // Local snapshot so toggling reflects immediately while the PUT is in flight.
  const [localCommands, setLocalCommands] = useState<McpCommand[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Pending protective enable confirmation.
  const [pendingProtective, setPendingProtective] =
    useState<McpCommand | null>(null);

  const commands =
    localCommands ?? (load.state === "ready" ? load.data : null);

  function applyUpdate(updated: McpCommand) {
    setLocalCommands((prev) => {
      const base =
        prev ?? (load.state === "ready" ? load.data : []);
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

  const isLoading = load.state === "loading";

  return (
    <Page
      title="MCP access"
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
      {commands !== null && (
        <div className="space-y-1.5">
          <p className="text-xs font-medium text-muted">Agent instructions</p>
          <CopyableSnippet
            label="Paste into your agent's prompt"
            text={buildMcpAgentPrompt(commands)}
            rows={8}
          />
        </div>
      )}

      <p className="text-xs text-muted-lt">
        Controls which MCP commands the AI agent may invoke. Disabled commands
        return a "disabled in panel" notice to the agent and have no effect on
        the engine. Commands marked{" "}
        <span className="font-medium text-[var(--danger)]">protected</span>{" "}
        grant the agent write access and require explicit confirmation to enable.
        The MCP endpoint is served at <span className="nums">/mcp</span>.
      </p>

      {error && (
        <ErrorBanner message={error} onDismiss={() => setError(null)} />
      )}

      {isLoading && commands === null && <TableSkeleton cols={4} />}

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
