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
          out.push({ id: row.id, status: "skipped", detail: "already exists" });
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
        Load accounts
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Load accounts from CSV</DialogTitle>
        </DialogHeader>

        <div className="rounded-card border border-border bg-muted/30 p-3 text-[0.6875rem] text-muted space-y-1">
          <p className="font-medium text-text">CSV format</p>
          <p>One account per line: <code>account_id[,group[,notes]]</code></p>
          <p><code>account_id</code> is required; <code>group</code> and <code>notes</code> are optional.</p>
          <p>A header row (<code>account_id,group,notes</code>) is allowed and skipped automatically.</p>
          <p className="pt-1 font-medium text-text">Examples</p>
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
              Choose file…
            </Button>
            <span className="text-[0.6875rem] text-muted-lt">or paste below</span>
          </div>
          <textarea
            className="min-h-[7rem] w-full rounded-card border border-border bg-background p-2 font-mono text-[0.75rem] text-text placeholder:text-muted-lt focus:outline-none focus:ring-1 focus:ring-accent/40"
            placeholder={"desk-alpha,equity-desks,Primary cash desk\ndesk-beta,equity-desks\nspx-arb,,SPX arb desk"}
            value={csvText}
            spellCheck={false}
            disabled={busy}
            onChange={(e) => setCsvText(e.target.value)}
          />
          {parsedCount > 0 && !results && (
            <p className="text-[0.6875rem] text-muted-lt">
              {parsedCount} row{parsedCount !== 1 ? "s" : ""} parsed.
            </p>
          )}
        </div>

        {results && (
          <div className="space-y-1 rounded-card border border-border p-3">
            <p className="text-[0.6875rem] font-medium text-muted">
              Results — {results.filter((r) => r.status === "created").length} created,{" "}
              {results.filter((r) => r.status === "skipped").length} skipped,{" "}
              {results.filter((r) => r.status === "error").length} errors
            </p>
            <ul className="max-h-40 overflow-y-auto space-y-0.5">
              {results.map((r, i) => (
                <li key={i} className={`flex items-baseline gap-1.5 text-[0.6875rem] ${statusColor[r.status]}`}>
                  <span className="shrink-0">
                    {r.status === "created" ? "✓" : r.status === "skipped" ? "–" : "✗"}
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
            {results ? "Close" : "Cancel"}
          </Button>
          {!results && (
            <Button
              size="sm"
              onClick={() => void run()}
              disabled={busy || parsedCount === 0}
            >
              <Upload className="h-3.5 w-3.5" />
              Import {parsedCount > 0 ? `${parsedCount} row${parsedCount !== 1 ? "s" : ""}` : ""}
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

function CreateAccountDialog({
  groupSuggestions,
  onCreated,
}: {
  groupSuggestions: string[];
  onCreated: () => void;
}) {
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
      setError(v);
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
        New account
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create account</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="account-id">Account id</Label>
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
              Up to 64 printable characters, no leading or trailing whitespace.
            </p>
            {validation && (
              <p className="text-[0.6875rem] text-[var(--danger)]">{validation}</p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="create-account-group">Group (optional)</Label>
            <Autocomplete
              id="create-account-group"
              value={group}
              onChange={setGroup}
              suggestions={groupSuggestions}
              placeholder="equity-desks"
              spellCheck={false}
            />
            <p className="text-[0.6875rem] text-muted">
              Choose an existing group or type a new id. Leave blank to create
              an ungrouped account.
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
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || id.length === 0 || validation !== null}
          >
            Create
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
        New group
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create group</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="group-id">Group id</Label>
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
            <Label htmlFor="group-notes">Notes (optional)</Label>
            <Textarea
              id="group-notes"
              value={notes}
              placeholder="Accounts belonging to the US equity trading desks."
              onChange={(e) => setNotes(e.target.value)}
            />
            <p className="text-[0.6875rem] text-muted">
              Reference notes — stored in the control plane only, not sent to
              the engine.
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
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || idTrimmed.length === 0}
          >
            Create
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
            Block account{" "}
            <span className="nums text-accent">{account?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          Blocking halts new orders for this account. A reason is required and
          is recorded in the audit log.
        </p>
        <div className="space-y-2">
          <Label htmlFor="block-account-reason">Reason</Label>
          <Textarea
            id="block-account-reason"
            value={reason}
            autoFocus
            placeholder="Kill-switch: risk limit breach during incident #42"
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
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || trimmed.length === 0}
          >
            <Ban className="h-3.5 w-3.5" />
            Block
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
          <AlertDialogTitle>Unblock account?</AlertDialogTitle>
          <AlertDialogDescription>
            Account{" "}
            <span className="nums text-accent">{account?.id}</span> will accept
            new orders again. This is recorded in the audit log.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
          >
            Unblock
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
            Block group{" "}
            <span className="nums text-accent">{group?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          Blocking halts new orders for all accounts in this group. A reason is
          required and is recorded in the audit log.
        </p>
        <div className="space-y-2">
          <Label htmlFor="block-group-reason">Reason</Label>
          <Textarea
            id="block-group-reason"
            value={reason}
            autoFocus
            placeholder="Kill-switch: risk limit breach during incident #42"
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
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || trimmed.length === 0}
          >
            <Ban className="h-3.5 w-3.5" />
            Block
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
          <AlertDialogTitle>Unblock group?</AlertDialogTitle>
          <AlertDialogDescription>
            Group{" "}
            <span className="nums text-accent">{group?.id}</span> accounts will
            accept new orders again. This is recorded in the audit log.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
          >
            Unblock
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
          <AlertDialogTitle>Delete group?</AlertDialogTitle>
          <AlertDialogDescription>
            Group <span className="nums text-accent">{group?.id}</span> will be
            permanently removed. Accounts currently assigned to this group will
            have their group assignment cleared.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
            className="bg-[var(--danger)] text-white hover:bg-[var(--danger)]/90"
          >
            <Trash2 className="h-3.5 w-3.5" />
            Delete
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
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => { if (open) setNotes(group?.notes ?? ""); }, [open, group]);

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
            Notes — group{" "}
            <span className="nums text-accent">{group?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          Reference notes stored in the control plane. Not sent to the engine
          and have no effect on risk evaluation.
        </p>
        <div className="space-y-2">
          <Label htmlFor="group-notes-edit">Notes</Label>
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
            Cancel
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            Save
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
            Assign group —{" "}
            <span className="nums text-accent">{account?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          Choose an existing group or type a new id. Leave blank to clear the
          group assignment.
        </p>
        <div className="space-y-2">
          <Label htmlFor="assign-group">Group</Label>
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
            Cancel
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            <Folder className="h-3.5 w-3.5" />
            {group.trim().length > 0 ? "Assign" : "Clear"}
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
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => { if (open) setNotes(account?.notes ?? ""); }, [open, account]);

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
            Notes —{" "}
            <span className="nums text-accent">{account?.id}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          Reference notes stored in the control plane. Not sent to the engine
          and have no effect on risk evaluation.
        </p>
        <div className="space-y-2">
          <Label htmlFor="account-notes">Notes</Label>
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
            Cancel
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            Save
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
  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Group</TableHead>
            <TableHead>Accounts</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Notes</TableHead>
            <TableHead className="text-right">Actions</TableHead>
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
                  isSelected && "ring-1 ring-inset ring-accent/40",
                  "cursor-pointer",
                )}
                onClick={() => onSelect(isSelected ? null : id)}
              >
                <TableCell className="font-medium">
                  {row.kind === "default" ? (
                    <span className="text-muted-lt italic">Default group</span>
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
                      blocked
                    </Badge>
                  ) : (
                    <Badge variant="ok">
                      <StatusDot tone="ok" />
                      active
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
                      All accounts without an assigned group.
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
                        title="Edit notes"
                      >
                        Notes
                      </Button>
                      {row.group.blocked ? (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => onUnblock(row.group)}
                        >
                          <CircleCheck className="h-3.5 w-3.5" />
                          Unblock
                        </Button>
                      ) : (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => onBlock(row.group)}
                        >
                          <Ban className="h-3.5 w-3.5" />
                          Block
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onDelete(row.group)}
                        title="Delete group"
                        className="text-[var(--danger)] hover:text-[var(--danger)]"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        Delete
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
  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Account</TableHead>
            <TableHead>Group</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Notes</TableHead>
            <TableHead className="text-right">Actions</TableHead>
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
                      ? "Group — click to change"
                      : "Group — click to change (no groups yet)"
                  }
                >
                  <Folder className="h-3.5 w-3.5 shrink-0" />
                  <span className="nums">
                    {account.group || <span className="italic">none</span>}
                  </span>
                </button>
              </TableCell>

              <TableCell>
                {account.blocked ? (
                  <Badge variant="danger">
                    <StatusDot tone="danger" />
                    blocked
                  </Badge>
                ) : (
                  <Badge variant="ok">
                    <StatusDot tone="ok" />
                    active
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
                    title="Positions — spot limits for this account"
                    aria-label="Positions — spot limits for this account"
                    className="inline-flex h-7 w-7 items-center justify-center rounded hover:bg-accent-dim"
                  >
                    <Coins className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/trading?account=${encodeURIComponent(account.id)}`}
                    title="Trading — orders & trades"
                    aria-label="Trading — orders & trades"
                    className="inline-flex h-7 w-7 items-center justify-center rounded hover:bg-accent-dim"
                  >
                    <ArrowLeftRight className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/policies?account=${encodeURIComponent(account.id)}`}
                    title="Policies — risk barriers"
                    aria-label="Policies — risk barriers"
                    className="inline-flex h-7 w-7 items-center justify-center rounded hover:bg-accent-dim"
                  >
                    <ShieldCheck className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/audit?account=${encodeURIComponent(account.id)}`}
                    title="Audit — log for this account"
                    aria-label="Audit — log for this account"
                    className="inline-flex h-7 w-7 items-center justify-center rounded hover:bg-accent-dim"
                  >
                    <History className="h-3.5 w-3.5 text-muted" />
                  </Link>

                  {/* Notes */}
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onEditNotes(account)}
                    title="Edit reference notes"
                  >
                    Notes
                  </Button>

                  {/* Block / Unblock */}
                  {account.blocked ? (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => onUnblock(account)}
                    >
                      <CircleCheck className="h-3.5 w-3.5" />
                      Unblock
                    </Button>
                  ) : (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => onBlock(account)}
                    >
                      <Ban className="h-3.5 w-3.5" />
                      Block
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
      title="Accounts"
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
        Operator accounts persisted in the control plane and applied to the
        engine. Block an account or group to halt orders via the kill switch.
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
            Groups{" "}
            {selectedGroupId !== null && (
              <button
                type="button"
                className="ml-1 text-accent hover:underline"
                onClick={() => setSelectedGroupId(null)}
              >
                (clear filter)
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
            Accounts
            {selectedGroupId !== null && (
              <span className="ml-1 text-muted-lt">
                — filtered to{" "}
                {selectedGroupId === DEFAULT_GROUP_ID
                  ? "default group"
                  : <span className="nums">{selectedGroupId}</span>}
              </span>
            )}
          </p>
          {visibleAccounts.length === 0 ? (
            <EmptyState
              title="No accounts"
              hint={
                selectedGroupId !== null
                  ? "No accounts are assigned to this group."
                  : "Create the first account to start applying risk limits to it."
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
