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

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import {
  ArrowLeftRight,
  Ban,
  CircleCheck,
  Coins,
  Folder,
  History,
  Plus,
  ShieldCheck,
  Trash2,
  Upload,
} from "lucide-react";

import {
  ApiError,
  blockAccount,
  blockGroup,
  createAccount,
  createGroup,
  deleteGroup,
  setAccountGroup,
  setAccountNotes,
  setGroupNotes,
  unblockAccount,
  unblockGroup,
} from "@/api/client";
import type { Account, Group } from "@/api/types";
import { useAccounts } from "@/api/useAccounts";
import { useGroups } from "@/api/useGroups";
import { validateAccountID } from "@/api/validate";
import { Autocomplete } from "@/components/Autocomplete";
import { EmptyState, ErrorBanner, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import { StatusDot } from "@/components/StatusDot";
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

// ---------------------------------------------------------------------------
// CSV parse helpers
// ---------------------------------------------------------------------------

/** Parse one raw CSV line into up to 3 fields, handling basic double-quote
 *  escaping and trimming whitespace from each field. */
function parseCsvRow(line: string): string[] {
  const fields: string[] = [];
  let cur = "";
  let inQuote = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (inQuote) {
      if (ch === '"' && line[i + 1] === '"') {
        cur += '"';
        i++;
      } else if (ch === '"') {
        inQuote = false;
      } else {
        cur += ch;
      }
    } else if (ch === '"') {
      inQuote = true;
    } else if (ch === ",") {
      fields.push(cur.trim());
      cur = "";
    } else {
      cur += ch;
    }
  }
  fields.push(cur.trim());
  return fields;
}

interface CsvRow {
  id: string;
  group: string;
  notes: string;
}

/** Parse CSV text into rows. Skips blank lines and the header row
 *  (detected when the first field is literally "account_id"). */
function parseCsv(text: string): CsvRow[] {
  const rows: CsvRow[] = [];
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) {
      continue;
    }
    const [id = "", group = "", notes = ""] = parseCsvRow(line);
    // Skip header line.
    if (id.toLowerCase() === "account_id") {
      continue;
    }
    if (!id) {
      continue;
    }
    rows.push({ id, group, notes });
  }
  return rows;
}

// ---------------------------------------------------------------------------
// Load accounts dialog
// ---------------------------------------------------------------------------

type RowStatus = "created" | "skipped" | "error";

interface RowResult {
  id: string;
  status: RowStatus;
  detail: string;
}

function LoadAccountsDialog({ onLoaded }: { onLoaded: () => void }) {
  const { t } = useTranslation("accounts");
  const [open, setOpen] = useState(false);
  const [csvText, setCsvText] = useState("");
  const [results, setResults] = useState<RowResult[] | null>(null);
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const reset = () => {
    setCsvText("");
    setResults(null);
    setBusy(false);
  };

  const handleFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (ev) => {
      setCsvText(typeof ev.target?.result === "string" ? ev.target.result : "");
    };
    reader.readAsText(file);
  };

  const run = async () => {
    const rows = parseCsv(csvText);
    if (rows.length === 0) return;
    setBusy(true);
    setResults(null);
    const out: RowResult[] = [];
    for (const row of rows) {
      try {
        await createAccount(row.id);
      } catch (err) {
        if (err instanceof ApiError && err.code === "conflict") {
          out.push({ id: row.id, status: "skipped", detail: t("loadAccounts.alreadyExists") });
          // Still apply group/notes updates on an existing account.
        } else {
          out.push({ id: row.id, status: "error", detail: errMessage(err) });
          continue;
        }
      }
      // If we get here the account exists (just created or already existed).
      const isNew = !out.some((r) => r.id === row.id);
      try {
        if (row.group) {
          await setAccountGroup(row.id, row.group);
        }
        if (row.notes) {
          await setAccountNotes(row.id, row.notes);
        }
        if (isNew) {
          out.push({ id: row.id, status: "created", detail: "" });
        }
      } catch (err) {
        // Replace any earlier entry for this id.
        const idx = out.findIndex((r) => r.id === row.id);
        const r: RowResult = {
          id: row.id,
          status: "error",
          detail: errMessage(err),
        };
        if (idx >= 0) {
          out[idx] = r;
        } else {
          out.push(r);
        }
      }
    }
    setResults(out);
    setBusy(false);
  };

  const parsedCount = parseCsv(csvText).length;

  const statusColor: Record<RowStatus, string> = {
    created: "text-[color:var(--ok)]",
    skipped: "text-muted-lt",
    error: "text-[var(--danger)]",
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          reset();
          if (results?.some((r) => r.status === "created")) {
            onLoaded();
          }
        }
      }}
    >
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        <Upload className="h-3.5 w-3.5" />
        {t("loadAccounts.trigger")}
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("loadAccounts.title")}</DialogTitle>
        </DialogHeader>

        <div className="space-y-1 rounded-card border border-border bg-surface-2 p-3 text-[0.6875rem] text-muted">
          <p className="font-medium text-text">{t("loadAccounts.formatHeading")}</p>
          <p>{t("loadAccounts.formatLine1")} <code>{t("loadAccounts.formatCode1")}</code></p>
          {/* account_id/group/notes are literal CSV column identifiers, not UI copy. */}
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <p><code>account_id</code> {t("loadAccounts.formatLine2prefix")} <code>group</code> {t("loadAccounts.formatLine2middle")} <code>notes</code> {t("loadAccounts.formatLine2suffix")}</p>
          <p>{t("loadAccounts.formatLine3prefix")}<code>{t("loadAccounts.formatLine3code")}</code>{t("loadAccounts.formatLine3suffix")}</p>
          <p className="pt-1 font-medium text-text">{t("loadAccounts.examplesHeading")}</p>
          <pre className="overflow-x-auto whitespace-pre">
{`desk-alpha,equity-desks,Primary cash desk
desk-beta,equity-desks
spx-arb,,SPX arb desk
account_id,group,notes`}
          </pre>
        </div>

        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <input
              ref={fileRef}
              type="file"
              accept=".csv,text/csv"
              className="hidden"
              onChange={handleFile}
            />
            <Button
              size="sm"
              variant="outline"
              onClick={() => fileRef.current?.click()}
              disabled={busy}
            >
              {t("loadAccounts.chooseFile")}
            </Button>
            <span className="text-[0.6875rem] text-muted-lt">{t("loadAccounts.orPasteBelow")}</span>
          </div>
          <textarea
            className="min-h-[7rem] w-full rounded-card border border-border bg-surface-2 p-2 font-mono text-[0.75rem] text-text placeholder:text-muted-lt focus:outline-none focus:ring-1 focus:ring-ring"
            placeholder={"desk-alpha,equity-desks,Primary cash desk\ndesk-beta,equity-desks\nspx-arb,,SPX arb desk"}
            value={csvText}
            spellCheck={false}
            disabled={busy}
            onChange={(e) => setCsvText(e.target.value)}
          />
          {parsedCount > 0 && !results && (
            <p className="text-[0.6875rem] text-muted-lt">
              {t("loadAccounts.parsedRows", { count: parsedCount })}
            </p>
          )}
        </div>

        {results && (
          <div className="space-y-1 rounded-card border border-border p-3">
            <p className="text-[0.6875rem] font-medium text-muted">
              {t("loadAccounts.results.summary", {
                created: results.filter((r) => r.status === "created").length,
                skipped: results.filter((r) => r.status === "skipped").length,
                errors: results.filter((r) => r.status === "error").length,
              })}
            </p>
            <ul className="max-h-40 overflow-y-auto space-y-0.5">
              {results.map((r, i) => (
                <li key={i} className={`flex items-baseline gap-1.5 text-[0.6875rem] ${statusColor[r.status]}`}>
                  <span className="shrink-0">
                    {r.status === "created" ? t("loadAccounts.results.created") : r.status === "skipped" ? t("loadAccounts.results.skipped") : t("loadAccounts.results.error")}
                  </span>
                  <span className="nums">{r.id}</span>
                  {r.detail && <span className="text-muted-lt">— {r.detail}</span>}
                </li>
              ))}
            </ul>
          </div>
        )}

        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setOpen(false)}
            disabled={busy}
          >
            {results ? t("loadAccounts.close") : t("loadAccounts.cancel")}
          </Button>
          {!results && (
            <Button
              size="sm"
              onClick={() => void run()}
              disabled={busy || parsedCount === 0}
            >
              <Upload className="h-3.5 w-3.5" />
              {parsedCount > 0 ? t("loadAccounts.import", { count: parsedCount }) : t("loadAccounts.importEmpty")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Create account dialog
// ---------------------------------------------------------------------------

// Exported for unit tests; rendered standalone inside Accounts otherwise.
export function CreateAccountDialog({
  groupSuggestions,
  onCreated,
}: {
  groupSuggestions: string[];
  onCreated: () => void;
}) {
  const { t } = useTranslation("validation");
  const { t: ta } = useTranslation("accounts");
  const [open, setOpen] = useState(false);
  const [id, setId] = useState("");
  const [group, setGroup] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const validation = id.length > 0 ? validateAccountID(id) : null;

  const reset = () => {
    setId("");
    setGroup("");
    setError(null);
    setBusy(false);
  };

  const submit = async () => {
    const v = validateAccountID(id);
    if (v) {
      setError(t(v.key, v.values));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await createAccount(id);
      if (group.trim().length > 0) {
        await setAccountGroup(id, group.trim());
      }
      setOpen(false);
      reset();
      onCreated();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <Button size="sm" onClick={() => setOpen(true)}>
        <Plus className="h-3.5 w-3.5" />
        {ta("createAccount.trigger")}
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{ta("createAccount.title")}</DialogTitle>
          <DialogDescription>
            {ta("createAccount.description")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="account-id">{ta("createAccount.idLabel")}</Label>
            <Input
              id="account-id"
              value={id}
              autoFocus
              spellCheck={false}
              placeholder="acc-aapl-desk"
              onChange={(e) => setId(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !validation) {
                  void submit();
                }
              }}
            />
            <p className="text-[0.6875rem] text-muted">
              {ta("createAccount.idHint")}
            </p>
            {validation && (
              <p className="text-[0.6875rem] text-[var(--danger)]">
                {t(validation.key, validation.values)}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="create-account-group">{ta("createAccount.groupLabel")}</Label>
            <Autocomplete
              id="create-account-group"
              value={group}
              onChange={setGroup}
              suggestions={groupSuggestions}
              placeholder="equity-desks"
              spellCheck={false}
            />
            <p className="text-[0.6875rem] text-muted">
              {ta("createAccount.groupHint")}
            </p>
          </div>
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setOpen(false)}
            disabled={busy}
          >
            {ta("createAccount.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || id.length === 0 || validation !== null}
          >
            {ta("createAccount.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Create group dialog
// ---------------------------------------------------------------------------

function CreateGroupDialog({ onCreated }: { onCreated: () => void }) {
  const { t } = useTranslation("accounts");
  const [open, setOpen] = useState(false);
  const [id, setId] = useState("");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const idTrimmed = id.trim();

  const reset = () => {
    setId("");
    setNotes("");
    setError(null);
    setBusy(false);
  };

  const submit = async () => {
    if (idTrimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      await createGroup(idTrimmed, notes.trim().length > 0 ? notes.trim() : undefined);
      setOpen(false);
      reset();
      onCreated();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        <Plus className="h-3.5 w-3.5" />
        {t("createGroup.trigger")}
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("createGroup.title")}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="group-id">{t("createGroup.idLabel")}</Label>
            <Input
              id="group-id"
              value={id}
              autoFocus
              spellCheck={false}
              placeholder="equity-desks"
              onChange={(e) => setId(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && idTrimmed.length > 0) {
                  void submit();
                }
              }}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="group-notes">{t("createGroup.notesLabel")}</Label>
            <Textarea
              id="group-notes"
              value={notes}
              placeholder={t("createGroup.notesPlaceholder")}
              onChange={(e) => setNotes(e.target.value)}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("createGroup.notesHint")}
            </p>
          </div>
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setOpen(false)}
            disabled={busy}
          >
            {t("createGroup.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || idTrimmed.length === 0}
          >
            {t("createGroup.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Block / unblock account dialogs
// ---------------------------------------------------------------------------

function BlockAccountDialog({
  account,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const trimmed = reason.trim();

  const submit = async () => {
    if (!account || trimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await blockAccount(account.id, trimmed);
      onOpenChange(false);
      setReason("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        if (!next) {
          setReason("");
          setError(null);
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("blockAccount.titlePrefix")}{" "}
            <span className="nums text-accent">{account?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          {t("blockAccount.description")}
        </p>
        <div className="space-y-2">
          <Label htmlFor="block-account-reason">{t("blockAccount.reasonLabel")}</Label>
          <Textarea
            id="block-account-reason"
            value={reason}
            autoFocus
            placeholder={t("blockAccount.reasonPlaceholder")}
            onChange={(e) => setReason(e.target.value)}
          />
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("blockAccount.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || trimmed.length === 0}
          >
            <Ban className="h-3.5 w-3.5" />
            {t("blockAccount.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function UnblockAccountConfirm({
  account,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!account) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await unblockAccount(account.id);
      onOpenChange(false);
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("unblockAccount.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("unblockAccount.descriptionPrefix")}{" "}
            <span className="nums text-accent">{account?.id}</span>{" "}
            {t("unblockAccount.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t("unblockAccount.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
          >
            {t("unblockAccount.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Block / unblock group dialogs
// ---------------------------------------------------------------------------

function BlockGroupDialog({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const trimmed = reason.trim();

  const submit = async () => {
    if (!group || trimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await blockGroup(group.id, trimmed);
      onOpenChange(false);
      setReason("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        if (!next) {
          setReason("");
          setError(null);
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("blockGroup.titlePrefix")}{" "}
            <span className="nums text-accent">{group?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          {t("blockGroup.description")}
        </p>
        <div className="space-y-2">
          <Label htmlFor="block-group-reason">{t("blockGroup.reasonLabel")}</Label>
          <Textarea
            id="block-group-reason"
            value={reason}
            autoFocus
            placeholder={t("blockGroup.reasonPlaceholder")}
            onChange={(e) => setReason(e.target.value)}
          />
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("blockGroup.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || trimmed.length === 0}
          >
            <Ban className="h-3.5 w-3.5" />
            {t("blockGroup.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function UnblockGroupConfirm({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!group) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await unblockGroup(group.id);
      onOpenChange(false);
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("unblockGroup.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("unblockGroup.descriptionPrefix")}{" "}
            <span className="nums text-accent">{group?.id}</span>{" "}
            {t("unblockGroup.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t("unblockGroup.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
          >
            {t("unblockGroup.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Delete group confirm
// ---------------------------------------------------------------------------

function DeleteGroupConfirm({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("accounts");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!group) return;
    setBusy(true);
    setError(null);
    try {
      await deleteGroup(group.id);
      onOpenChange(false);
      onDone();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("deleteGroup.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("deleteGroup.descriptionPrefix")}{" "}
            <span className="nums text-accent">{group?.id}</span>{" "}
            {t("deleteGroup.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t("deleteGroup.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            <Trash2 className="h-3.5 w-3.5" />
            {t("deleteGroup.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Edit group notes dialog
// ---------------------------------------------------------------------------

function EditGroupNotesDialog({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    // Reseed the notes field from the edited group when the dialog opens.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (open) setNotes(group?.notes ?? "");
  }, [open, group]);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setNotes("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!group) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await setGroupNotes(group.id, notes);
      onOpenChange(false);
      setNotes("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("editGroupNotes.titlePrefix")}{" "}
            <span className="nums text-accent">{group?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          {t("editGroupNotes.description")}
        </p>
        <div className="space-y-2">
          <Label htmlFor="group-notes-edit">{t("editGroupNotes.notesLabel")}</Label>
          <Textarea
            id="group-notes-edit"
            value={notes}
            autoFocus
            rows={4}
            onChange={(e) => setNotes(e.target.value)}
          />
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("editGroupNotes.cancel")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            {t("editGroupNotes.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Assign group dialog (for accounts)
// ---------------------------------------------------------------------------

function AssignGroupDialog({
  account,
  groupSuggestions,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  groupSuggestions: string[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  const [group, setGroup] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const handleOpenChange = (next: boolean) => {
    if (next && account) {
      setGroup(account.group ?? "");
    }
    if (!next) {
      setGroup("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!account) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await setAccountGroup(account.id, group.trim());
      onOpenChange(false);
      setGroup("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("assignGroup.titlePrefix")}{" "}
            <span className="nums text-accent">{account?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          {t("assignGroup.description")}
        </p>
        <div className="space-y-2">
          <Label htmlFor="assign-group">{t("assignGroup.groupLabel")}</Label>
          <Autocomplete
            id="assign-group"
            value={group}
            onChange={setGroup}
            suggestions={groupSuggestions}
            placeholder="equity-desks"
            autoFocus
            spellCheck={false}
          />
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("assignGroup.cancel")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            <Folder className="h-3.5 w-3.5" />
            {group.trim().length > 0 ? t("assignGroup.assign") : t("assignGroup.clear")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Edit account notes dialog
// ---------------------------------------------------------------------------

function EditAccountNotesDialog({
  account,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    // Reseed the notes field from the edited account when the dialog opens.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (open) setNotes(account?.notes ?? "");
  }, [open, account]);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setNotes("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!account) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await setAccountNotes(account.id, notes);
      onOpenChange(false);
      setNotes("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("editAccountNotes.titlePrefix")}{" "}
            <span className="nums text-accent">{account?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          {t("editAccountNotes.description")}
        </p>
        <div className="space-y-2">
          <Label htmlFor="account-notes">{t("editAccountNotes.notesLabel")}</Label>
          <Textarea
            id="account-notes"
            value={notes}
            autoFocus
            rows={4}
            onChange={(e) => setNotes(e.target.value)}
          />
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("editAccountNotes.cancel")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            {t("editAccountNotes.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Groups panel
// ---------------------------------------------------------------------------

// Sentinel id used internally to represent the "Default group" row.
const DEFAULT_GROUP_ID = "";

type GroupRow =
  | { kind: "default"; memberCount: number }
  | { kind: "real"; group: Group; memberCount: number };

function GroupsPanel({
  groupRows,
  selectedGroupId,
  onSelect,
  onEditNotes,
  onBlock,
  onUnblock,
  onDelete,
}: {
  groupRows: GroupRow[];
  selectedGroupId: string | null;
  onSelect: (id: string | null) => void;
  onEditNotes: (group: Group) => void;
  onBlock: (group: Group) => void;
  onUnblock: (group: Group) => void;
  onDelete: (group: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("groups.columns.group")}</TableHead>
            <TableHead>{t("groups.columns.accounts")}</TableHead>
            <TableHead>{t("groups.columns.status")}</TableHead>
            <TableHead>{t("groups.columns.notes")}</TableHead>
            <TableHead className="text-right">{t("groups.columns.actions")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {groupRows.map((row) => {
            const id = row.kind === "default" ? DEFAULT_GROUP_ID : row.group.id;
            const isSelected = selectedGroupId === id;
            const isBlocked = row.kind === "real" && row.group.blocked;
            return (
              <TableRow
                key={id === DEFAULT_GROUP_ID ? "__default__" : id}
                className={cn(
                  isBlocked && "bg-accent-dim",
                  isSelected && "ring-1 ring-inset ring-ring",
                  "cursor-pointer",
                )}
                onClick={() => onSelect(isSelected ? null : id)}
              >
                <TableCell className="font-medium">
                  {row.kind === "default" ? (
                    <span className="text-muted-lt italic">{t("groups.defaultGroup")}</span>
                  ) : (
                    <span className="nums text-accent">{row.group.id}</span>
                  )}
                </TableCell>

                <TableCell className="text-xs text-muted-lt">
                  {row.memberCount}
                </TableCell>

                <TableCell>
                  {row.kind === "real" && row.group.blocked ? (
                    <Badge variant="danger">
                      <StatusDot tone="danger" />
                      {t("groups.status.blocked")}
                    </Badge>
                  ) : (
                    <Badge variant="ok">
                      <StatusDot tone="ok" />
                      {t("groups.status.active")}
                    </Badge>
                  )}
                </TableCell>

                <TableCell
                  className="max-w-[16rem] truncate text-xs text-muted-lt"
                  title={
                    row.kind === "real" ? (row.group.notes || undefined) : undefined
                  }
                >
                  {row.kind === "real" ? (
                    row.group.notes || "—"
                  ) : (
                    <span className="italic">
                      {t("groups.defaultGroupNote")}
                    </span>
                  )}
                </TableCell>

                <TableCell className="text-right">
                  {row.kind === "real" && (
                    <div
                      className="flex items-center justify-end gap-1"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onEditNotes(row.group)}
                        title={t("groups.actions.editNotesTitle")}
                      >
                        {t("groups.actions.notes")}
                      </Button>
                      {row.group.blocked ? (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => onUnblock(row.group)}
                        >
                          <CircleCheck className="h-3.5 w-3.5" />
                          {t("groups.actions.unblock")}
                        </Button>
                      ) : (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => onBlock(row.group)}
                        >
                          <Ban className="h-3.5 w-3.5" />
                          {t("groups.actions.block")}
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onDelete(row.group)}
                        title={t("groups.actions.deleteTitle")}
                        className="text-[var(--danger)] hover:text-[var(--danger)]"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        {t("groups.actions.delete")}
                      </Button>
                    </div>
                  )}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Accounts table
// ---------------------------------------------------------------------------

function AccountsTable({
  accounts,
  groupSuggestions,
  onBlock,
  onUnblock,
  onAssignGroup,
  onEditNotes,
}: {
  accounts: Account[];
  groupSuggestions: string[];
  onBlock: (account: Account) => void;
  onUnblock: (account: Account) => void;
  onAssignGroup: (account: Account) => void;
  onEditNotes: (account: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("accounts.columns.account")}</TableHead>
            <TableHead>{t("accounts.columns.group")}</TableHead>
            <TableHead>{t("accounts.columns.status")}</TableHead>
            <TableHead>{t("accounts.columns.notes")}</TableHead>
            <TableHead className="text-right">{t("accounts.columns.actions")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {accounts.map((account) => (
            <TableRow
              key={account.id}
              className={cn(account.blocked && "bg-accent-dim")}
            >
              <TableCell className="nums font-medium">{account.id}</TableCell>

              {/* Group cell — button with single-membership Folder icon */}
              <TableCell>
                <button
                  type="button"
                  className="inline-flex items-center gap-1 text-xs text-muted-lt hover:text-accent"
                  onClick={() => onAssignGroup(account)}
                  title={
                    groupSuggestions.length > 0
                      ? t("accounts.groupCell.title")
                      : t("accounts.groupCell.titleNoGroups")
                  }
                >
                  <Folder className="h-3.5 w-3.5 shrink-0" />
                  <span className="nums">
                    {account.group || <span className="italic">{t("accounts.groupCell.noGroup")}</span>}
                  </span>
                </button>
              </TableCell>

              <TableCell>
                {account.blocked ? (
                  <Badge variant="danger">
                    <StatusDot tone="danger" />
                    {t("accounts.status.blocked")}
                  </Badge>
                ) : (
                  <Badge variant="ok">
                    <StatusDot tone="ok" />
                    {t("accounts.status.active")}
                  </Badge>
                )}
              </TableCell>

              <TableCell
                className="max-w-[14rem] truncate text-xs text-muted-lt"
                title={account.notes || undefined}
              >
                {account.notes || "—"}
              </TableCell>

              <TableCell className="text-right">
                <div className="flex items-center justify-end gap-1">
                  {/* Quick-links: Positions → Trading → Policies → Audit */}
                  <Link
                    to={`/positions?account=${encodeURIComponent(account.id)}`}
                    title={t("accounts.links.positions")}
                    aria-label={t("accounts.links.positions")}
                    className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
                  >
                    <Coins className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/trading?account=${encodeURIComponent(account.id)}`}
                    title={t("accounts.links.trading")}
                    aria-label={t("accounts.links.trading")}
                    className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
                  >
                    <ArrowLeftRight className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/policies?account=${encodeURIComponent(account.id)}`}
                    title={t("accounts.links.policies")}
                    aria-label={t("accounts.links.policies")}
                    className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
                  >
                    <ShieldCheck className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/audit?account=${encodeURIComponent(account.id)}`}
                    title={t("accounts.links.audit")}
                    aria-label={t("accounts.links.audit")}
                    className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
                  >
                    <History className="h-3.5 w-3.5 text-muted" />
                  </Link>

                  {/* Notes */}
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onEditNotes(account)}
                    title={t("accounts.actions.editNotesTitle")}
                  >
                    {t("accounts.actions.notes")}
                  </Button>

                  {/* Block / Unblock */}
                  {account.blocked ? (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => onUnblock(account)}
                    >
                      <CircleCheck className="h-3.5 w-3.5" />
                      {t("accounts.actions.unblock")}
                    </Button>
                  ) : (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => onBlock(account)}
                    >
                      <Ban className="h-3.5 w-3.5" />
                      {t("accounts.actions.block")}
                    </Button>
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
// Helpers
// ---------------------------------------------------------------------------

function replaceAccount(list: Account[], updated: Account): Account[] {
  return list.map((a) => (a.id === updated.id ? updated : a));
}

function replaceGroup(list: Group[], updated: Group): Group[] {
  return list.map((g) => (g.id === updated.id ? updated : g));
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function Accounts() {
  const { t } = useTranslation("accounts");
  const { load: accountsLoad, reload: reloadAccounts } = useAccounts();
  const { load: groupsLoad, reload: reloadGroups } = useGroups();

  // Local snapshots so individual rows update immediately from server responses.
  const [localAccounts, setLocalAccounts] = useState<Account[] | null>(null);
  const [localGroups, setLocalGroups] = useState<Group[] | null>(null);

  const accounts =
    localAccounts ?? (accountsLoad.state === "ready" ? accountsLoad.data : null);
  const groups =
    localGroups ?? (groupsLoad.state === "ready" ? groupsLoad.data : null);

  // Union of group record ids and distinct non-empty account.group values.
  const memberOnlyIds: string[] = accounts
    ? Array.from(new Set(accounts.map((a) => a.group).filter((g) => g !== "")))
        .filter((id) => !groups?.some((g) => g.id === id))
    : [];

  const groupSuggestions: string[] = [
    ...(groups?.map((g) => g.id) ?? []),
    ...memberOnlyIds,
  ];

  // Which group row is highlighted; null = show all accounts.
  const [selectedGroupId, setSelectedGroupId] = useState<string | null>(null);

  // Account dialog targets.
  const [blockAccountTarget, setBlockAccountTarget] = useState<Account | null>(null);
  const [unblockAccountTarget, setUnblockAccountTarget] = useState<Account | null>(null);
  const [groupTarget, setGroupTarget] = useState<Account | null>(null);
  const [notesAccountTarget, setNotesAccountTarget] = useState<Account | null>(null);

  // Group dialog targets.
  const [groupNotesTarget, setGroupNotesTarget] = useState<Group | null>(null);
  const [blockGroupTarget, setBlockGroupTarget] = useState<Group | null>(null);
  const [unblockGroupTarget, setUnblockGroupTarget] = useState<Group | null>(null);
  const [deleteGroupTarget, setDeleteGroupTarget] = useState<Group | null>(null);

  function applyAccountUpdate(updated: Account) {
    setLocalAccounts((prev) => {
      const base = prev ?? (accountsLoad.state === "ready" ? accountsLoad.data : []);
      return replaceAccount(base, updated);
    });
  }

  function applyGroupUpdate(updated: Group) {
    setLocalGroups((prev) => {
      const base = prev ?? (groupsLoad.state === "ready" ? groupsLoad.data : []);
      const exists = base.some((g) => g.id === updated.id);
      return exists ? replaceGroup(base, updated) : [...base, updated];
    });
  }

  // Build group rows: Default first, then real groups (records + membership-only), sorted by id.
  const defaultCount =
    accounts?.filter((a) => a.group === "").length ?? 0;

  // Membership-only groups get a synthetic Group object; actions still call the
  // existing client funcs which create-if-missing on the backend.
  const memberOnlyRows: GroupRow[] = memberOnlyIds.map((id) => ({
    kind: "real" as const,
    group: { id, notes: "", blocked: false, blockReason: "" } satisfies Group,
    memberCount: accounts?.filter((a) => a.group === id).length ?? 0,
  }));

  const recordRows: GroupRow[] = (groups ?? []).map((g) => ({
    kind: "real" as const,
    group: g,
    memberCount: accounts?.filter((a) => a.group === g.id).length ?? 0,
  }));

  const allRealRows = [...recordRows, ...memberOnlyRows].sort((a, b) => {
    const ai = a.kind === "real" ? a.group.id : "";
    const bi = b.kind === "real" ? b.group.id : "";
    return ai.localeCompare(bi);
  });

  const groupRows: GroupRow[] = [
    { kind: "default", memberCount: defaultCount },
    ...allRealRows,
  ];

  // Filter accounts by selected group (null = all).
  const visibleAccounts = accounts
    ? selectedGroupId === null
      ? accounts
      : accounts.filter((a) => a.group === selectedGroupId)
    : null;

  const reloadAll = () => {
    setLocalAccounts(null);
    setLocalGroups(null);
    reloadAccounts();
    reloadGroups();
  };

  const isLoading =
    accountsLoad.state === "loading" || groupsLoad.state === "loading";
  const loadError =
    accountsLoad.state === "error"
      ? accountsLoad.error
      : groupsLoad.state === "error"
        ? groupsLoad.error
        : null;

  return (
    <Page
      title={t("page.title")}
      actions={
        <>
          <RefreshButton onClick={reloadAll} busy={isLoading} />
          <CreateGroupDialog
            onCreated={() => {
              setLocalGroups(null);
              reloadGroups();
            }}
          />
          <LoadAccountsDialog
            onLoaded={() => {
              setLocalAccounts(null);
              reloadAccounts();
            }}
          />
          <CreateAccountDialog
            groupSuggestions={groupSuggestions}
            onCreated={() => {
              setLocalAccounts(null);
              reloadAccounts();
            }}
          />
        </>
      }
    >
      <p className="text-xs text-muted-lt">
        {t("page.description")}
      </p>

      {/* Groups panel */}
      {isLoading && accounts === null && groups === null && (
        <TableSkeleton cols={5} />
      )}
      {loadError && accounts === null && (
        <ErrorState message={loadError} onRetry={reloadAll} />
      )}
      {(accounts !== null || groups !== null) && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">
            {t("groups.heading")}{" "}
            {selectedGroupId !== null && (
              <button
                type="button"
                className="ml-1 text-accent hover:underline"
                onClick={() => setSelectedGroupId(null)}
              >
                {t("groups.clearFilter")}
              </button>
            )}
          </p>
          <GroupsPanel
            groupRows={groupRows}
            selectedGroupId={selectedGroupId}
            onSelect={setSelectedGroupId}
            onEditNotes={setGroupNotesTarget}
            onBlock={setBlockGroupTarget}
            onUnblock={setUnblockGroupTarget}
            onDelete={setDeleteGroupTarget}
          />
        </div>
      )}

      {/* Accounts panel */}
      {visibleAccounts !== null && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">
            {t("accounts.heading")}
            {selectedGroupId !== null && (
              <span className="ml-1 text-muted-lt">
                {t("accounts.filteredTo")}{" "}
                {selectedGroupId === DEFAULT_GROUP_ID
                  ? t("accounts.filteredToDefault")
                  : <span className="nums">{selectedGroupId}</span>}
              </span>
            )}
          </p>
          {visibleAccounts.length === 0 ? (
            <EmptyState
              title={t("accounts.empty.title")}
              hint={
                selectedGroupId !== null
                  ? t("accounts.empty.hintFiltered")
                  : t("accounts.empty.hintEmpty")
              }
              action={
                selectedGroupId === null ? (
                  <CreateAccountDialog
                    groupSuggestions={groupSuggestions}
                    onCreated={() => {
                      setLocalAccounts(null);
                      reloadAccounts();
                    }}
                  />
                ) : undefined
              }
            />
          ) : (
            <AccountsTable
              accounts={visibleAccounts}
              groupSuggestions={groupSuggestions}
              onBlock={setBlockAccountTarget}
              onUnblock={setUnblockAccountTarget}
              onAssignGroup={setGroupTarget}
              onEditNotes={setNotesAccountTarget}
            />
          )}
        </div>
      )}

      {/* Account dialogs */}
      <BlockAccountDialog
        account={blockAccountTarget}
        open={blockAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setBlockAccountTarget(null);
        }}
        onDone={(updated) => {
          setBlockAccountTarget(null);
          applyAccountUpdate(updated);
        }}
      />
      <UnblockAccountConfirm
        account={unblockAccountTarget}
        open={unblockAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setUnblockAccountTarget(null);
        }}
        onDone={(updated) => {
          setUnblockAccountTarget(null);
          applyAccountUpdate(updated);
        }}
      />
      <AssignGroupDialog
        account={groupTarget}
        groupSuggestions={groupSuggestions}
        open={groupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setGroupTarget(null);
        }}
        onDone={(updated) => {
          setGroupTarget(null);
          applyAccountUpdate(updated);
        }}
      />
      <EditAccountNotesDialog
        account={notesAccountTarget}
        open={notesAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setNotesAccountTarget(null);
        }}
        onDone={(updated) => {
          setNotesAccountTarget(null);
          applyAccountUpdate(updated);
        }}
      />
      {/* Group dialogs */}
      <EditGroupNotesDialog
        group={groupNotesTarget}
        open={groupNotesTarget !== null}
        onOpenChange={(next) => {
          if (!next) setGroupNotesTarget(null);
        }}
        onDone={(updated) => {
          setGroupNotesTarget(null);
          applyGroupUpdate(updated);
        }}
      />
      <BlockGroupDialog
        group={blockGroupTarget}
        open={blockGroupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setBlockGroupTarget(null);
        }}
        onDone={(updated) => {
          setBlockGroupTarget(null);
          applyGroupUpdate(updated);
        }}
      />
      <UnblockGroupConfirm
        group={unblockGroupTarget}
        open={unblockGroupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setUnblockGroupTarget(null);
        }}
        onDone={(updated) => {
          setUnblockGroupTarget(null);
          applyGroupUpdate(updated);
        }}
      />
      <DeleteGroupConfirm
        group={deleteGroupTarget}
        open={deleteGroupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setDeleteGroupTarget(null);
        }}
        onDone={() => {
          setDeleteGroupTarget(null);
          // If the deleted group was selected, clear the filter.
          if (
            deleteGroupTarget !== null &&
            selectedGroupId === deleteGroupTarget.id
          ) {
            setSelectedGroupId(null);
          }
          setLocalGroups(null);
          reloadGroups();
        }}
      />
    </Page>
  );
}
