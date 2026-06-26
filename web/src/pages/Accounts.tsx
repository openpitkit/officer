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

import { useEffect, useState } from "react";
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
} from "lucide-react";

import {
  ApiError,
  blockAccount,
  blockGroup,
  createAccount,
  createGroup,
  deleteAccount,
  deleteGroup,
  setAccountGroup,
  setAccountNotes,
  setGroupNotes,
  unblockAccount,
  unblockGroup,
} from "@/api/client";
import type { Account, ApiErrorDependent, Group } from "@/api/types";
import { useAccounts } from "@/api/useAccounts";
import { useGroups } from "@/api/useGroups";
import { validateAccountID } from "@/api/validate";
import { Autocomplete } from "@/components/Autocomplete";
import { EmptyState, ErrorBanner, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import { StatusDot } from "@/components/StatusDot";
import {
  CsvTransferMenu,
  PageSizeSelect,
  TablePagination,
} from "@/components/TableControls";
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
import {
  hasNextPage,
  knownPageCount,
  slicePage,
} from "@/lib/tablePagination";
import { usePersistentPageSize } from "@/lib/tablePageSize";
import { cn } from "@/lib/utils";

type AccountsTab = "accounts" | "groups";

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
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
  const [code, setCode] = useState("");
  const [group, setGroup] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const validation = code.length > 0 ? validateAccountID(code) : null;

  const reset = () => {
    setCode("");
    setGroup("");
    setError(null);
    setBusy(false);
  };

  const submit = async () => {
    const v = validateAccountID(code);
    if (v) {
      setError(t(v.key, v.values));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await createAccount(code);
      if (group.trim().length > 0) {
        await setAccountGroup(code, group.trim());
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
            <Label htmlFor="account-code">{ta("createAccount.codeLabel")}</Label>
            <Input
              id="account-code"
              value={code}
              autoFocus
              spellCheck={false}
              placeholder="acc-aapl-desk"
              onChange={(e) => setCode(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !validation) {
                  void submit();
                }
              }}
            />
            <p className="text-[0.6875rem] text-muted">
              {ta("createAccount.codeHint")}
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
            disabled={busy || code.length === 0 || validation !== null}
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
  const [code, setCode] = useState("");
  const [title, setTitle] = useState("");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const codeTrimmed = code.trim();
  const titleTrimmed = title.trim();

  const reset = () => {
    setCode("");
    setTitle("");
    setNotes("");
    setError(null);
    setBusy(false);
  };

  const submit = async () => {
    if (codeTrimmed.length === 0 || titleTrimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      await createGroup(codeTrimmed, titleTrimmed, notes.trim());
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
            <Label htmlFor="group-code">{t("createGroup.codeLabel")}</Label>
            <Input
              id="group-code"
              value={code}
              autoFocus
              spellCheck={false}
              placeholder="equity-desks"
              onChange={(e) => setCode(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && codeTrimmed.length > 0 && titleTrimmed.length > 0) {
                  void submit();
                }
              }}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="group-title">{t("createGroup.titleLabel")}</Label>
            <Input
              id="group-title"
              value={title}
              spellCheck={false}
              placeholder={t("createGroup.titlePlaceholder")}
              onChange={(e) => setTitle(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && codeTrimmed.length > 0 && titleTrimmed.length > 0) {
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
            disabled={busy || codeTrimmed.length === 0 || titleTrimmed.length === 0}
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
      const updated = await blockAccount(account.code, trimmed);
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
            <span className="nums text-accent">{account?.code}</span>
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
      const updated = await unblockAccount(account.code);
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
            <span className="nums text-accent">{account?.code}</span>{" "}
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
      const updated = await blockGroup(group.code, trimmed);
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
            <span className="nums text-accent">{group?.code}</span>
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
      const updated = await unblockGroup(group.code);
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
            <span className="nums text-accent">{group?.code}</span>{" "}
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
      await deleteGroup(group.code);
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
            <span className="nums text-accent">{group?.code}</span>{" "}
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
// Delete account confirm
// ---------------------------------------------------------------------------

function DeleteAccountConfirm({
  account,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("accounts");
  const [error, setError] = useState<string | null>(null);
  const [dependents, setDependents] = useState<ApiErrorDependent[]>([]);
  const [busy, setBusy] = useState(false);

  const changeOpen = (next: boolean) => {
    if (!next) {
      setError(null);
      setDependents([]);
    }
    onOpenChange(next);
  };

  const submit = async (force: boolean) => {
    if (!account) return;
    setBusy(true);
    setError(null);
    try {
      await deleteAccount(account.code, force);
      changeOpen(false);
      onDone();
    } catch (err) {
      if (err instanceof ApiError && err.code === "has_dependents") {
        setDependents(err.dependents ?? []);
        setError(null);
      } else {
        setError(errMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={changeOpen}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("deleteAccount.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("deleteAccount.descriptionPrefix")}{" "}
            <span className="nums text-accent">{account?.code}</span>{" "}
            {t("deleteAccount.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {dependents.length > 0 && (
          <div className="rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] p-3 text-xs">
            <p className="font-medium text-[var(--danger)]">
              {t("deleteAccount.dependentsTitle")}
            </p>
            <ul className="mt-2 space-y-1">
              {dependents.map((dep) => (
                <li key={dep.kind} className="flex justify-between gap-4">
                  <span>
                    {t(`deleteAccount.dependentKinds.${dep.kind}`, {
                      defaultValue: dep.kind,
                    })}
                  </span>
                  <span className="nums">{dep.count}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t("deleteAccount.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit(dependents.length > 0);
            }}
            disabled={busy}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            <Trash2 className="h-3.5 w-3.5" />
            {dependents.length > 0
              ? t("deleteAccount.forceSubmit")
              : t("deleteAccount.submit")}
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
      const updated = await setGroupNotes(group.code, notes);
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
            <span className="nums text-accent">{group?.code}</span>
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
      const updated = await setAccountGroup(account.code, group.trim());
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
            <span className="nums text-accent">{account?.code}</span>
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
      const updated = await setAccountNotes(account.code, notes);
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
            <span className="nums text-accent">{account?.code}</span>
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

// Sentinel code used internally to represent the "Default group" row.
const DEFAULT_GROUP_CODE = "";

type GroupRow =
  | { kind: "default"; memberCount: number }
  | { kind: "real"; group: Group; memberCount: number };
type RealGroupRow = Extract<GroupRow, { kind: "real" }>;

function groupDisplayTitle(group: Group): string {
  return group.title !== "" ? group.title : group.code;
}

function GroupsPanel({
  groupRows,
  selectedGroupCode,
  onSelect,
  onEditNotes,
  onBlock,
  onUnblock,
  onDelete,
}: {
  groupRows: GroupRow[];
  selectedGroupCode: string | null;
  onSelect: (code: string | null) => void;
  onEditNotes: (group: Group) => void;
  onBlock: (group: Group) => void;
  onUnblock: (group: Group) => void;
  onDelete: (group: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  return (
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
            const code = row.kind === "default" ? DEFAULT_GROUP_CODE : row.group.code;
            const isSelected = selectedGroupCode === code;
            const isBlocked = row.kind === "real" && row.group.blocked;
            const title = row.kind === "real" ? groupDisplayTitle(row.group) : "";
            return (
              <TableRow
                key={code === DEFAULT_GROUP_CODE ? "__default__" : code}
                className={cn(
                  isBlocked && "bg-accent-dim",
                  isSelected && "ring-1 ring-inset ring-ring",
                  "cursor-pointer",
                )}
                onClick={() => onSelect(isSelected ? null : code)}
              >
                <TableCell className="font-medium">
                  {row.kind === "default" ? (
                    <span className="text-muted-lt italic">{t("groups.defaultGroup")}</span>
                  ) : (
                    <span className="flex flex-col">
                      <span>{title}</span>
                      {title !== row.group.code && (
                        <span className="nums text-[0.6875rem] text-accent">
                          {row.group.code}
                        </span>
                      )}
                    </span>
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
  onDelete,
}: {
  accounts: Account[];
  groupSuggestions: string[];
  onBlock: (account: Account) => void;
  onUnblock: (account: Account) => void;
  onAssignGroup: (account: Account) => void;
  onEditNotes: (account: Account) => void;
  onDelete: (account: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  return (
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
              key={account.code}
              className={cn(account.blocked && "bg-accent-dim")}
            >
              <TableCell className="font-medium">
                <span className="flex flex-col">
                  <span>{account.title}</span>
                  <span className="nums text-[0.6875rem] text-accent">
                    {account.code}
                  </span>
                </span>
              </TableCell>

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
                  <div className="flex flex-col items-start gap-1">
                    <Badge variant="danger">
                      <StatusDot tone="danger" />
                      {t("accounts.status.blocked")}
                    </Badge>
                    {account.blockReason && (
                      <span
                        className="max-w-[18rem] truncate text-[0.6875rem] text-muted-lt"
                        title={account.blockReason}
                      >
                        {account.blockReason}
                      </span>
                    )}
                  </div>
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
                    to={`/positions?account=${encodeURIComponent(account.code)}`}
                    title={t("accounts.links.positions")}
                    aria-label={t("accounts.links.positions")}
                    className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
                  >
                    <Coins className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/trading?account=${encodeURIComponent(account.code)}`}
                    title={t("accounts.links.trading")}
                    aria-label={t("accounts.links.trading")}
                    className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
                  >
                    <ArrowLeftRight className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/policies?account=${encodeURIComponent(account.code)}`}
                    title={t("accounts.links.policies")}
                    aria-label={t("accounts.links.policies")}
                    className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
                  >
                    <ShieldCheck className="h-3.5 w-3.5 text-muted" />
                  </Link>
                  <Link
                    to={`/audit?account=${encodeURIComponent(account.code)}`}
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
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onDelete(account)}
                    title={t("accounts.actions.deleteTitle")}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function replaceAccount(list: Account[], updated: Account): Account[] {
  return list.map((a) => (a.code === updated.code ? updated : a));
}

function replaceGroup(list: Group[], updated: Group): Group[] {
  return list.map((g) => (g.code === updated.code ? updated : g));
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

  // Union of group record codes and distinct non-empty account.group values.
  const memberOnlyCodes: string[] = accounts
    ? Array.from(new Set(accounts.map((a) => a.group).filter((g) => g !== "")))
        .filter((code) => !groups?.some((g) => g.code === code))
    : [];

  const groupSuggestions: string[] = [
    ...(groups?.map((g) => g.code) ?? []),
    ...memberOnlyCodes,
  ];

  // Which group row is highlighted; null = show all accounts.
  const [selectedGroupCode, setSelectedGroupCode] = useState<string | null>(null);
  const [tab, setTab] = useState<AccountsTab>("accounts");
  const [accountPage, setAccountPage] = useState(0);
  const [accountSize, setAccountSize] = usePersistentPageSize(
    "pit-officer-accounts-page-size",
  );
  const [groupPage, setGroupPage] = useState(0);
  const [groupSize, setGroupSize] = usePersistentPageSize(
    "pit-officer-groups-page-size",
  );

  // Account dialog targets.
  const [blockAccountTarget, setBlockAccountTarget] = useState<Account | null>(null);
  const [unblockAccountTarget, setUnblockAccountTarget] = useState<Account | null>(null);
  const [groupTarget, setGroupTarget] = useState<Account | null>(null);
  const [notesAccountTarget, setNotesAccountTarget] = useState<Account | null>(null);
  const [deleteAccountTarget, setDeleteAccountTarget] = useState<Account | null>(null);

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

  function removeAccount(code: string) {
    setLocalAccounts((prev) => {
      const base = prev ?? (accountsLoad.state === "ready" ? accountsLoad.data : []);
      return base.filter((a) => a.code !== code);
    });
  }

  function applyGroupUpdate(updated: Group) {
    setLocalGroups((prev) => {
      const base = prev ?? (groupsLoad.state === "ready" ? groupsLoad.data : []);
      const exists = base.some((g) => g.code === updated.code);
      return exists ? replaceGroup(base, updated) : [...base, updated];
    });
  }

  // Build group rows: Default first, then real groups (records + membership-only), sorted by code.
  const defaultCount =
    accounts?.filter((a) => a.group === "").length ?? 0;

  // Membership-only groups get a synthetic Group object; actions still call the
  // existing client funcs which create-if-missing on the backend.
  const memberOnlyRows: RealGroupRow[] = memberOnlyCodes.map((code) => ({
    kind: "real" as const,
    group: {
      code,
      title: code,
      notes: "",
      blocked: false,
      blockReason: "",
    } satisfies Group,
    memberCount: accounts?.filter((a) => a.group === code).length ?? 0,
  }));

  const recordRows: RealGroupRow[] = (groups ?? []).map((g) => ({
    kind: "real" as const,
    group: g,
    memberCount: accounts?.filter((a) => a.group === g.code).length ?? 0,
  }));

  const allRealRows = [...recordRows, ...memberOnlyRows].sort((a, b) =>
    a.group.code.localeCompare(b.group.code),
  );
  const selectedGroup =
    selectedGroupCode === null
      ? null
      : allRealRows.find((row) => row.group.code === selectedGroupCode)?.group ??
        null;
  const selectedGroupTitle =
    selectedGroup === null ? "" : groupDisplayTitle(selectedGroup);

  const groupRows: GroupRow[] = [
    { kind: "default", memberCount: defaultCount },
    ...allRealRows,
  ];

  // Filter accounts by selected group (null = all).
  const visibleAccounts = accounts
    ? selectedGroupCode === null
      ? accounts
      : accounts.filter((a) => a.group === selectedGroupCode)
    : null;
  const pagedAccounts =
    visibleAccounts === null
      ? null
      : slicePage(visibleAccounts, accountPage, accountSize);
  const visibleAccountCount = visibleAccounts?.length ?? 0;
  const pagedGroups = slicePage(groupRows, groupPage, groupSize);
  const hasMoreAccounts =
    visibleAccounts !== null &&
    hasNextPage(visibleAccounts, accountPage, accountSize);
  const hasMoreGroups = hasNextPage(groupRows, groupPage, groupSize);
  const accountPager = (
    <TablePagination
      page={accountPage}
      canPrevious={accountPage > 0}
      canNext={hasMoreAccounts}
      knownTotalPages={
        visibleAccounts === null
          ? undefined
          : knownPageCount(visibleAccounts.length, accountSize)
      }
      onPrevious={() => setAccountPage((p) => Math.max(0, p - 1))}
      onNext={() => setAccountPage((p) => p + 1)}
      onPage={setAccountPage}
    />
  );
  const groupPager = (
    <TablePagination
      page={groupPage}
      canPrevious={groupPage > 0}
      canNext={hasMoreGroups}
      knownTotalPages={knownPageCount(groupRows.length, groupSize)}
      onPrevious={() => setGroupPage((p) => Math.max(0, p - 1))}
      onNext={() => setGroupPage((p) => p + 1)}
      onPage={setGroupPage}
    />
  );

  const reloadAll = () => {
    setLocalAccounts(null);
    setLocalGroups(null);
    reloadAccounts();
    reloadGroups();
  };
  const accountCsvFilters =
    selectedGroupCode === null ? undefined : { groupCode: selectedGroupCode };

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
          <PageSizeSelect
            value={tab === "accounts" ? accountSize : groupSize}
            onChange={(value) => {
              if (tab === "accounts") {
                setAccountSize(value);
                setAccountPage(0);
              } else {
                setGroupSize(value);
                setGroupPage(0);
              }
            }}
            ariaLabel={t("pagination.pageSize.ariaLabel")}
            rowCountLabel={(count) =>
              t("pagination.pageSize.rowCount", { count })
            }
          />
          <RefreshButton onClick={reloadAll} busy={isLoading} />
          <CsvTransferMenu
            imports={[
              {
                defaultEntity: "accounts",
                entities: ["account_groups", "accounts"],
                label: t("businessCsv.importCsv"),
              },
            ]}
            exports={[
              {
                entity: "account_groups",
                label: t("businessCsv.exportGroupsCsv"),
              },
              {
                entity: "accounts",
                filters: accountCsvFilters,
                label: t("businessCsv.exportAccountsCsv"),
              },
            ]}
            onImported={reloadAll}
          />
          <CreateGroupDialog
            onCreated={() => {
              setLocalGroups(null);
              reloadGroups();
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

      <div className="flex w-fit gap-1 rounded-card border border-border bg-surface-2 p-1">
        {(["accounts", "groups"] as AccountsTab[]).map((tabId) => (
          <button
            key={tabId}
            type="button"
            onClick={() => setTab(tabId)}
            className={[
              "rounded-badge px-3 py-1 text-xs font-medium transition-colors duration-[180ms]",
              tab === tabId
                ? "bg-accent-dim text-accent"
                : "text-muted-lt hover:bg-surface-hover hover:text-text",
            ].join(" ")}
          >
            {t(`tabs.${tabId}`)}
          </button>
        ))}
      </div>

      {/* Groups panel */}
      {isLoading && accounts === null && groups === null && (
        <TableSkeleton cols={5} />
      )}
      {loadError && accounts === null && (
        <ErrorState message={loadError} onRetry={reloadAll} />
      )}
      {tab === "groups" && (accounts !== null || groups !== null) && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">
            {t("groups.heading")}{" "}
            {selectedGroupCode !== null && (
              <button
                type="button"
                className="ml-1 text-accent hover:underline"
                onClick={() => {
                  setSelectedGroupCode(null);
                  setAccountPage(0);
                }}
              >
                {t("groups.clearFilter")}
              </button>
            )}
          </p>
          {groupPager}
          <GroupsPanel
            groupRows={pagedGroups}
            selectedGroupCode={selectedGroupCode}
            onSelect={(code) => {
              setSelectedGroupCode(code);
              setAccountPage(0);
            }}
            onEditNotes={setGroupNotesTarget}
            onBlock={setBlockGroupTarget}
            onUnblock={setUnblockGroupTarget}
            onDelete={setDeleteGroupTarget}
          />
          {groupPager}
        </div>
      )}

      {/* Accounts panel */}
      {tab === "accounts" && pagedAccounts !== null && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">
            {t("accounts.heading")}
            {selectedGroupCode !== null && (
              <span className="ml-1 text-muted-lt">
                {t("accounts.filteredTo")}{" "}
                {selectedGroupCode === DEFAULT_GROUP_CODE
                  ? t("accounts.filteredToDefault")
                  : selectedGroup === null
                    ? <span className="nums">{selectedGroupCode}</span>
                    : (
                      <>
                        <span>{selectedGroupTitle}</span>
                        {selectedGroupTitle !== selectedGroup.code && (
                          <>
                            {" "}
                            <span className="nums">({selectedGroup.code})</span>
                          </>
                        )}
                      </>
                    )}
              </span>
            )}
          </p>
          {visibleAccountCount === 0 ? (
            <EmptyState
              title={t("accounts.empty.title")}
              hint={
                selectedGroupCode !== null
                  ? t("accounts.empty.hintFiltered")
                  : t("accounts.empty.hintEmpty")
              }
              action={
                selectedGroupCode === null ? (
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
            <>
              {accountPager}
              <AccountsTable
                accounts={pagedAccounts}
                groupSuggestions={groupSuggestions}
                onBlock={setBlockAccountTarget}
                onUnblock={setUnblockAccountTarget}
                onAssignGroup={setGroupTarget}
                onEditNotes={setNotesAccountTarget}
                onDelete={setDeleteAccountTarget}
              />
              {accountPager}
            </>
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
      <DeleteAccountConfirm
        account={deleteAccountTarget}
        open={deleteAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setDeleteAccountTarget(null);
        }}
        onDone={() => {
          if (deleteAccountTarget !== null) {
            removeAccount(deleteAccountTarget.code);
          }
          setDeleteAccountTarget(null);
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
            selectedGroupCode === deleteGroupTarget.code
          ) {
            setSelectedGroupCode(null);
            setAccountPage(0);
          }
          setLocalGroups(null);
          reloadGroups();
        }}
      />
    </Page>
  );
}
