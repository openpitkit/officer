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

import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Plus, Trash2 } from "lucide-react";

import type {
  Asset,
  AssetClass,
  AssetClassListFilters,
  AssetListFilters,
  SortOrder,
} from "@/api/types";
import { useAssets } from "@/api/useAssets";
import { useAssetClasses } from "@/api/useAssetClasses";
import { Autocomplete } from "@/components/Autocomplete";
import { EmptyState, ErrorBanner, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import {
  PageSizeSelect,
  TablePagination,
} from "@/components/TableControls";
import {
  ApiError,
  AutocompleteFilterField,
  ColumnHeader,
  DeleteButton,
  EditButton,
  FilterBar,
  FilterByButton,
  HistoryButton,
  IdCell,
  PositionsButton,
  RowActions,
  ShareLinkButton,
  SortableHeader,
  TradesButton,
  TradingButton,
  useOfficerApi,
} from "@/framework";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
import { ClearableInput } from "@/components/ClearableInput";
import { ClearFieldButton } from "@/components/ClearFieldButton";
import {
  DEFAULT_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/lib/useDebounce";
import { absoluteAppUrl, shareUrl } from "@/lib/shareLink";
import { sortDirection } from "@/lib/sortDirection";
import { knownPageCount } from "@/lib/tablePagination";
import { usePersistentPageSize } from "@/lib/tablePageSize";
import { cn } from "@/lib/utils";

type AssetsTab = "assets" | "classes";

const ASSET_SORT_KEYS = new Set(["assetClass", "code", "title"]);
const ASSET_CLASS_SORT_KEYS = new Set(["assetCount", "code", "title"]);

function sortFromParams(
  params: URLSearchParams,
  allowed: Set<string>,
): { sort?: string; order?: SortOrder } {
  const sort = params.get("sort");
  const order = params.get("order");
  if (sort !== null && allowed.has(sort) && (order === "asc" || order === "desc")) {
    return { sort, order };
  }
  return {};
}

function sameStrings(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((value, i) => value === right[i]);
}

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

// ---------------------------------------------------------------------------
// Create asset dialog
// ---------------------------------------------------------------------------

// Exported for unit tests; rendered standalone inside Assets otherwise.
export function CreateAssetDialog({
  classSuggestions,
  onCreated,
}: {
  classSuggestions: string[];
  onCreated: () => void;
}) {
  const { t } = useTranslation("assets");
  const { createAsset } = useOfficerApi();
  const [open, setOpen] = useState(false);
  const [code, setCode] = useState("");
  const [title, setTitle] = useState("");
  const [assetClass, setAssetClass] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const codeTrimmed = code.trim();

  const reset = () => {
    setCode("");
    setTitle("");
    setAssetClass("");
    setError(null);
    setBusy(false);
  };

  const submit = async () => {
    if (codeTrimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      await createAsset(codeTrimmed, title.trim(), assetClass.trim());
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
        {t("createAsset.trigger")}
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("createAsset.title")}</DialogTitle>
          <DialogDescription>{t("createAsset.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="asset-code">{t("createAsset.codeLabel")}</Label>
            <ClearableInput
              id="asset-code"
              value={code}
              autoFocus
              spellCheck={false}
              placeholder="BTC"
              onChange={(e) => setCode(e.target.value)}
              onClear={() => setCode("")}
              clearLabel={t("filters.clearField")}
              onKeyDown={(e) => {
                if (e.key === "Enter" && codeTrimmed.length > 0) {
                  void submit();
                }
              }}
              disabled={busy}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("createAsset.codeHint")}
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="asset-title">{t("createAsset.titleLabel")}</Label>
            <ClearableInput
              id="asset-title"
              value={title}
              spellCheck={false}
              onChange={(e) => setTitle(e.target.value)}
              onClear={() => setTitle("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="asset-class">{t("createAsset.classLabel")}</Label>
            <Autocomplete
              id="asset-class"
              value={assetClass}
              onChange={setAssetClass}
              suggestions={classSuggestions}
              placeholder="crypto"
              spellCheck={false}
              disabled={busy}
              onClear={() => setAssetClass("")}
              clearLabel={t("filters.clearField")}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("createAsset.classHint")}
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
            {t("createAsset.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || codeTrimmed.length === 0}
          >
            {t("createAsset.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Create class dialog
// ---------------------------------------------------------------------------

function CreateClassDialog({ onCreated }: { onCreated: () => void }) {
  const { t } = useTranslation("assets");
  const { createAssetClass } = useOfficerApi();
  const [open, setOpen] = useState(false);
  const [code, setCode] = useState("");
  const [title, setTitle] = useState("");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const codeTrimmed = code.trim();

  const reset = () => {
    setCode("");
    setTitle("");
    setNotes("");
    setError(null);
    setBusy(false);
  };

  const submit = async () => {
    if (codeTrimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      await createAssetClass(codeTrimmed, title.trim(), notes.trim());
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
        {t("createClass.trigger")}
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("createClass.title")}</DialogTitle>
          <DialogDescription>{t("createClass.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="class-code">{t("createClass.codeLabel")}</Label>
            <ClearableInput
              id="class-code"
              value={code}
              autoFocus
              spellCheck={false}
              placeholder="crypto"
              onChange={(e) => setCode(e.target.value)}
              onClear={() => setCode("")}
              clearLabel={t("filters.clearField")}
              onKeyDown={(e) => {
                if (e.key === "Enter" && codeTrimmed.length > 0) {
                  void submit();
                }
              }}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="class-title">{t("createClass.titleLabel")}</Label>
            <ClearableInput
              id="class-title"
              value={title}
              spellCheck={false}
              placeholder={t("createClass.titlePlaceholder")}
              onChange={(e) => setTitle(e.target.value)}
              onClear={() => setTitle("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="class-notes">{t("createClass.notesLabel")}</Label>
            <Textarea
              id="class-notes"
              value={notes}
              placeholder={t("createClass.notesPlaceholder")}
              onChange={(e) => setNotes(e.target.value)}
              disabled={busy}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("createClass.notesHint")}
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
            {t("createClass.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || codeTrimmed.length === 0}
          >
            {t("createClass.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Edit asset dialog (code is now editable)
// ---------------------------------------------------------------------------

function EditAssetDialog({
  asset,
  classSuggestions,
  open,
  onOpenChange,
  onDone,
}: {
  asset: Asset | null;
  classSuggestions: string[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Asset) => void;
}) {
  const { t } = useTranslation("assets");
  const { updateAsset } = useOfficerApi();
  const [code, setCode] = useState("");
  const [title, setTitle] = useState("");
  const [assetClass, setAssetClass] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      /* eslint-disable react-hooks/set-state-in-effect */
      setCode(asset?.code ?? "");
      setTitle(asset?.title ?? "");
      setAssetClass(asset?.assetClass ?? "");
      /* eslint-enable react-hooks/set-state-in-effect */
    }
  }, [open, asset]);

  const codeTrimmed = code.trim();

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setCode("");
      setTitle("");
      setAssetClass("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!asset || codeTrimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await updateAsset(
        asset.code,
        codeTrimmed,
        title.trim(),
        assetClass.trim(),
      );
      onOpenChange(false);
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
            {t("editAsset.titlePrefix")}{" "}
            <span className="nums text-accent">{asset?.code}</span>
          </DialogTitle>
          <DialogDescription>{t("editAsset.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="asset-edit-code">{t("editAsset.codeLabel")}</Label>
            <ClearableInput
              id="asset-edit-code"
              value={code}
              autoFocus
              spellCheck={false}
              onChange={(e) => setCode(e.target.value)}
              onClear={() => setCode("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="asset-edit-title">{t("editAsset.titleLabel")}</Label>
            <ClearableInput
              id="asset-edit-title"
              value={title}
              spellCheck={false}
              onChange={(e) => setTitle(e.target.value)}
              onClear={() => setTitle("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="asset-edit-class">{t("editAsset.classLabel")}</Label>
            <Autocomplete
              id="asset-edit-class"
              value={assetClass}
              onChange={setAssetClass}
              suggestions={classSuggestions}
              placeholder="crypto"
              spellCheck={false}
              disabled={busy}
              onClear={() => setAssetClass("")}
              clearLabel={t("filters.clearField")}
            />
          </div>
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("editAsset.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || codeTrimmed.length === 0}
          >
            {t("editAsset.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Edit class dialog (code editable + title + notes)
// ---------------------------------------------------------------------------

function EditClassDialog({
  assetClass,
  open,
  onOpenChange,
  onDone,
}: {
  assetClass: AssetClass | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: AssetClass) => void;
}) {
  const { t } = useTranslation("assets");
  const { updateAssetClass } = useOfficerApi();
  const [code, setCode] = useState("");
  const [title, setTitle] = useState("");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      /* eslint-disable react-hooks/set-state-in-effect */
      setCode(assetClass?.code ?? "");
      setTitle(assetClass?.title ?? "");
      setNotes(assetClass?.notes ?? "");
      /* eslint-enable react-hooks/set-state-in-effect */
    }
  }, [open, assetClass]);

  const codeTrimmed = code.trim();

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setCode("");
      setTitle("");
      setNotes("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!assetClass || codeTrimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await updateAssetClass(
        assetClass.code,
        codeTrimmed,
        title.trim(),
        notes.trim(),
      );
      onOpenChange(false);
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
            {t("editClass.titlePrefix")}{" "}
            <span className="nums text-accent">{assetClass?.code}</span>
          </DialogTitle>
          <DialogDescription>{t("editClass.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="class-edit-code">{t("editClass.codeLabel")}</Label>
            <ClearableInput
              id="class-edit-code"
              value={code}
              autoFocus
              spellCheck={false}
              onChange={(e) => setCode(e.target.value)}
              onClear={() => setCode("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="class-edit-title">{t("editClass.titleLabel")}</Label>
            <ClearableInput
              id="class-edit-title"
              value={title}
              spellCheck={false}
              onChange={(e) => setTitle(e.target.value)}
              onClear={() => setTitle("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="class-edit-notes">{t("editClass.notesLabel")}</Label>
            <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_2rem]">
              <Textarea
                id="class-edit-notes"
                value={notes}
                rows={4}
                onChange={(e) => setNotes(e.target.value)}
                disabled={busy}
              />
              <ClearFieldButton
                label={t("filters.clearField")}
                onClick={() => setNotes("")}
                disabled={busy || notes.length === 0}
              />
            </div>
          </div>
        </div>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("editClass.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || codeTrimmed.length === 0}
          >
            {t("editClass.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Delete asset confirm
// ---------------------------------------------------------------------------

function DeleteAssetConfirm({
  asset,
  open,
  onOpenChange,
  onDone,
}: {
  asset: Asset | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("assets");
  const { deleteAsset } = useOfficerApi();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!asset) return;
    setBusy(true);
    setError(null);
    try {
      await deleteAsset(asset.code);
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
          <AlertDialogTitle>{t("deleteAsset.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("deleteAsset.descriptionPrefix")}{" "}
            <span className="nums text-accent">{asset?.code}</span>
            {t("deleteAsset.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t("deleteAsset.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            <Trash2 className="h-3.5 w-3.5" />
            {t("deleteAsset.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Delete class confirm
// ---------------------------------------------------------------------------

function DeleteClassConfirm({
  assetClass,
  open,
  onOpenChange,
  onDone,
}: {
  assetClass: AssetClass | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("assets");
  const { deleteAssetClass } = useOfficerApi();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const changeOpen = (next: boolean) => {
    if (!next) {
      setError(null);
    }
    onOpenChange(next);
  };

  // Delete straight away; a class with linked assets is force-detached (the
  // backend clears the assets' class link) so the operator is never blocked.
  const submit = async () => {
    if (!assetClass) return;
    setBusy(true);
    setError(null);
    try {
      await deleteAssetClass(assetClass.code, assetClass.assetCount > 0);
      changeOpen(false);
      onDone();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const hasAssets = (assetClass?.assetCount ?? 0) > 0;

  return (
    <AlertDialog open={open} onOpenChange={changeOpen}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("deleteClass.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("deleteClass.descriptionPrefix")}{" "}
            <span className="nums text-accent">{assetClass?.code}</span>
            {hasAssets
              ? t("deleteClass.descriptionSuffixLinked", {
                  count: assetClass?.assetCount ?? 0,
                })
              : t("deleteClass.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t("deleteClass.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            <Trash2 className="h-3.5 w-3.5" />
            {t("deleteClass.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Assets table
// ---------------------------------------------------------------------------

function AssetsTable({
  assets,
  selectedAsset,
  selectedClass,
  activeSort,
  activeOrder,
  onSortChange,
  onEditAsset,
  onFilterAsset,
  onFilterClass,
  onOpenPositions,
  onOpenOrders,
  onOpenTrades,
  onOpenHistory,
  onDelete,
}: {
  assets: Asset[];
  selectedAsset: string | null;
  selectedClass: string | null;
  activeSort?: string;
  activeOrder?: SortOrder;
  onSortChange: (sort?: string, order?: SortOrder) => void;
  onEditAsset: (asset: Asset) => void;
  onFilterAsset: (asset: Asset) => void;
  onFilterClass: (assetClass: string) => void;
  onOpenPositions: (asset: Asset) => void;
  onOpenOrders: (asset: Asset) => void;
  onOpenTrades: (asset: Asset) => void;
  onOpenHistory: (asset: Asset) => void;
  onDelete: (asset: Asset) => void;
}) {
  const { t } = useTranslation("assets");
  const { t: tc } = useTranslation("common");
  return (
    <Table className="table-fixed min-w-[44rem]">
      <colgroup>
        <col className="w-[16rem]" />
        <col />
        <col className="w-[12.5rem]" />
      </colgroup>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="w-[16rem]">
            <SortableHeader
              field="code"
              label={t("columns.asset")}
              description={t("columnDescriptions.asset")}
              direction={sortDirection(activeSort, activeOrder, "code")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="assetClass"
              label={t("columns.class")}
              description={t("columnDescriptions.class")}
              direction={sortDirection(activeSort, activeOrder, "assetClass")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="w-[12.5rem] text-right">
            <ColumnHeader
              align="right"
              description={t("columnDescriptions.actions")}
            >
              {t("columns.actions")}
            </ColumnHeader>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {assets.map((asset) => {
          const title = asset.title;
          return (
            <TableRow key={asset.code}>
              <TableCell className="w-[16rem] font-medium">
                <div className="flex items-start gap-1">
                  <span className="flex min-w-0 flex-col">
                    {title !== "" && title !== asset.code ? (
                      <>
                        <span className="nums truncate">{title}</span>
                        <IdCell
                          value={asset.code}
                          copyTitle={tc("rowActions.copyIdTitle", {
                            entity: asset.code,
                          })}
                          copiedTitle={tc("rowActions.copiedId")}
                          gap={4}
                        >
                          <span className="text-[0.6875rem] text-accent">
                            {asset.code}
                          </span>
                        </IdCell>
                      </>
                    ) : (
                      <IdCell
                        value={asset.code}
                        copyTitle={tc("rowActions.copyIdTitle", {
                          entity: asset.code,
                        })}
                        copiedTitle={tc("rowActions.copiedId")}
                        gap={4}
                      >
                        {title || asset.code}
                      </IdCell>
                    )}
                  </span>
                  <span className="ml-auto flex shrink-0 items-center">
                    {selectedAsset !== asset.code && (
                      <FilterByButton
                        size={28}
                        title={tc("rowActions.filterByTitle", {
                          field: asset.code,
                        })}
                        href={absoluteAppUrl(
                          `/assets?asset=${encodeURIComponent(asset.code)}`,
                        )}
                        onClick={() => onFilterAsset(asset)}
                      />
                    )}
                    <EditButton
                      size={28}
                      onClick={() => onEditAsset(asset)}
                      title={t("actions.editTitle")}
                    />
                  </span>
                </div>
              </TableCell>

              <TableCell className="text-xs text-muted-lt">
                <div className="flex min-w-0 items-center gap-1">
                  <span
                    className={cn(
                      "nums min-w-0 truncate",
                      asset.assetClass === "" && "italic",
                      selectedClass !== null && selectedClass === asset.assetClass
                        ? "text-accent"
                        : "text-muted-lt",
                    )}
                  >
                    {asset.assetClass || t("noClass")}
                  </span>
                  {asset.assetClass !== "" &&
                    selectedClass !== asset.assetClass && (
                      <span className="ml-auto flex shrink-0 items-center">
                        <FilterByButton
                          size={28}
                          title={tc("rowActions.filterByTitle", {
                            field: asset.assetClass,
                          })}
                          href={absoluteAppUrl(
                            `/assets?class=${encodeURIComponent(asset.assetClass)}`,
                          )}
                          onClick={() => onFilterClass(asset.assetClass)}
                        />
                      </span>
                    )}
                </div>
              </TableCell>

              <TableCell className="w-[12.5rem] text-right">
                <RowActions>
                  <PositionsButton
                    title={t("links.positions")}
                    href={absoluteAppUrl(
                      `/positions?asset=${encodeURIComponent(asset.code)}`,
                    )}
                    onClick={() => onOpenPositions(asset)}
                  />
                  <TradingButton
                    title={t("links.orders")}
                    href={absoluteAppUrl(
                      `/orders?baseAsset=${encodeURIComponent(asset.code)}`,
                    )}
                    onClick={() => onOpenOrders(asset)}
                  />
                  <TradesButton
                    title={t("links.trades")}
                    href={absoluteAppUrl(
                      `/orders?tab=trades&baseAsset=${encodeURIComponent(
                        asset.code,
                      )}`,
                    )}
                    onClick={() => onOpenTrades(asset)}
                  />
                  <HistoryButton
                    title={t("links.audit")}
                    href={absoluteAppUrl(
                      `/audit?asset=${encodeURIComponent(asset.code)}`,
                    )}
                    onClick={() => onOpenHistory(asset)}
                  />
                  <DeleteButton
                    title={tc("rowActions.deleteTitle", { entity: asset.code })}
                    onClick={() => onDelete(asset)}
                  />
                </RowActions>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Classes panel
// ---------------------------------------------------------------------------

function classDisplayTitle(assetClass: AssetClass): string {
  return assetClass.title !== "" ? assetClass.title : assetClass.code;
}

function ClassesPanel({
  classes,
  selectedClass,
  activeSort,
  activeOrder,
  onSortChange,
  onSelect,
  onEdit,
  onDelete,
}: {
  classes: AssetClass[];
  selectedClass: string | null;
  activeSort?: string;
  activeOrder?: SortOrder;
  onSortChange: (sort?: string, order?: SortOrder) => void;
  onSelect: (code: string) => void;
  onEdit: (assetClass: AssetClass) => void;
  onDelete: (assetClass: AssetClass) => void;
}) {
  const { t } = useTranslation("assets");
  const { t: tc } = useTranslation("common");
  return (
    <Table className="table-fixed min-w-[44rem]">
      <colgroup>
        <col className="w-[16rem]" />
        <col className="w-[5rem]" />
        <col />
        <col className="w-[8.5rem]" />
      </colgroup>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="w-[16rem]">
            <SortableHeader
              field="code"
              label={t("classes.columns.class")}
              description={t("classes.columnDescriptions.class")}
              direction={sortDirection(activeSort, activeOrder, "code")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="w-[5rem]">
            <SortableHeader
              field="assetCount"
              label={t("classes.columns.assets")}
              description={t("classes.columnDescriptions.assets")}
              direction={sortDirection(activeSort, activeOrder, "assetCount")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("classes.columnDescriptions.notes")}>
              {t("classes.columns.notes")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="w-[8.5rem] text-right">
            <ColumnHeader
              align="right"
              description={t("classes.columnDescriptions.actions")}
            >
              {t("classes.columns.actions")}
            </ColumnHeader>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {classes.map((assetClass) => {
          const title = classDisplayTitle(assetClass);
          const isSelected = selectedClass === assetClass.code;
          return (
            <TableRow
              key={assetClass.code}
              className={cn(isSelected && "ring-1 ring-inset ring-ring")}
            >
              <TableCell className="w-[16rem] font-medium">
                <div className="flex items-start gap-1">
                  <span className="flex min-w-0 flex-col">
                    {title !== assetClass.code ? (
                      <>
                        <span className="nums truncate">{title}</span>
                        <IdCell
                          value={assetClass.code}
                          copyTitle={tc("rowActions.copyIdTitle", {
                            entity: assetClass.code,
                          })}
                          copiedTitle={tc("rowActions.copiedId")}
                          gap={4}
                        >
                          <span className="text-[0.6875rem] text-accent">
                            {assetClass.code}
                          </span>
                        </IdCell>
                      </>
                    ) : (
                      <IdCell
                        value={assetClass.code}
                        copyTitle={tc("rowActions.copyIdTitle", {
                          entity: assetClass.code,
                        })}
                        copiedTitle={tc("rowActions.copiedId")}
                        gap={4}
                      >
                        {title}
                      </IdCell>
                    )}
                  </span>
                  <span className="ml-auto flex shrink-0 items-center">
                    <EditButton
                      size={28}
                      onClick={() => onEdit(assetClass)}
                      title={t("classes.actions.editTitle")}
                    />
                  </span>
                </div>
              </TableCell>

              <TableCell className="w-[5rem] text-xs text-muted-lt">
                <span className="nums">{assetClass.assetCount}</span>
              </TableCell>

              <TableCell
                className="text-xs text-muted-lt"
                title={assetClass.notes || undefined}
              >
                <span className={cn("min-w-0 truncate", assetClass.notes === "" && "italic")}>
                  {assetClass.notes || "—"}
                </span>
              </TableCell>

              <TableCell className="w-[8.5rem] text-right">
                <RowActions>
                  <FilterByButton
                    title={tc("rowActions.filterByTitle", {
                      field: assetClass.code,
                    })}
                    href={absoluteAppUrl(
                      `/assets?class=${encodeURIComponent(assetClass.code)}`,
                    )}
                    onClick={() => onSelect(assetClass.code)}
                  />
                  <ShareLinkButton
                    href={absoluteAppUrl(
                      `/assets?class=${encodeURIComponent(assetClass.code)}`,
                    )}
                    title={tc("rowActions.shareTitle", {
                      entity: assetClass.code,
                    })}
                    copiedTitle={tc("rowActions.copiedLink")}
                  />
                  <DeleteButton
                    title={tc("rowActions.deleteTitle", {
                      entity: assetClass.code,
                    })}
                    onClick={() => onDelete(assetClass)}
                  />
                </RowActions>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function Assets() {
  const { t } = useTranslation("assets");
  const { t: tc } = useTranslation("common");
  const { fetchAssetClasses, fetchAssets } = useOfficerApi();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  const initialTab: AssetsTab =
    searchParams.get("tab") === "classes" ? "classes" : "assets";
  const seedAssets = initialTab === "assets";
  const seedClasses = initialTab === "classes";
  const [tab, setTab] = useState<AssetsTab>(initialTab);
  const [assetSort, setAssetSort] = useState<{
    sort?: string;
    order?: SortOrder;
  }>(() => (seedAssets ? sortFromParams(searchParams, ASSET_SORT_KEYS) : {}));
  const [classSort, setClassSort] = useState<{
    sort?: string;
    order?: SortOrder;
  }>(() =>
    seedClasses ? sortFromParams(searchParams, ASSET_CLASS_SORT_KEYS) : {},
  );
  const [assetPage, setAssetPage] = useState(0);
  const [classPage, setClassPage] = useState(0);
  const [assetSize, setAssetSize] = usePersistentPageSize(
    "pit-officer-assets-page-size",
  );
  const [classSize, setClassSize] = usePersistentPageSize(
    "pit-officer-asset-classes-page-size",
  );

  // A shared deep link (?class=<code>) seeds the initial exact-class filter,
  // matching the "filter by class" action; the operator owns it thereafter.
  const [selectedClass, setSelectedClass] = useState<string | null>(
    () => (seedAssets ? searchParams.get("class") : null),
  );
  const [selectedAsset, setSelectedAsset] = useState<string | null>(
    () => (seedAssets ? searchParams.get("asset") : null),
  );
  const [search, setSearch] = useState(
    seedAssets ? (searchParams.get("code") ?? "") : "",
  );
  const debouncedSearch = useDebouncedValue(search, DEFAULT_SEARCH_DEBOUNCE_MS);
  const [classSearch, setClassSearch] = useState(
    seedClasses
      ? (searchParams.get("code") ?? "")
      : (searchParams.get("classSearch") ?? ""),
  );
  const debouncedClassSearch = useDebouncedValue(
    classSearch,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );

  const [editTarget, setEditTarget] = useState<Asset | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Asset | null>(null);
  const [editClassTarget, setEditClassTarget] = useState<AssetClass | null>(null);
  const [deleteClassTarget, setDeleteClassTarget] = useState<AssetClass | null>(null);

  const assetListFilters = useMemo<AssetListFilters>(() => {
    const filters: AssetListFilters = {
      sort: assetSort.sort,
      order: assetSort.order,
      limit: assetSize,
      offset: assetPage * assetSize,
    };
    const code = selectedAsset ?? debouncedSearch.trim();
    const assetClass = (selectedClass ?? debouncedClassSearch.trim()) || undefined;
    if (code !== "") {
      return {
        ...filters,
        code,
        codeMatch: selectedAsset === null ? "contains" : "exact",
        class: assetClass,
        classMatch: selectedClass === null ? "contains" : "exact",
      };
    }
    return {
      ...filters,
      class: assetClass,
      classMatch: selectedClass === null ? "contains" : "exact",
    };
  }, [
    assetSort.order,
    assetSort.sort,
    assetPage,
    assetSize,
    debouncedClassSearch,
    debouncedSearch,
    selectedAsset,
    selectedClass,
  ]);
  const assetClassListFilters = useMemo<AssetClassListFilters>(
    () => ({
      code: debouncedClassSearch.trim() || undefined,
      codeMatch: debouncedClassSearch.trim() === "" ? undefined : "contains",
      sort: classSort.sort,
      order: classSort.order,
      limit: classSize,
      offset: classPage * classSize,
    }),
    [classPage, classSize, classSort.order, classSort.sort, debouncedClassSearch],
  );
  const { load, reload } = useAssets(assetListFilters);
  const { load: classesLoad, reload: reloadClasses } = useAssetClasses(
    assetClassListFilters,
  );

  const assetPageData = load.state === "ready" ? load.data : null;
  const classPageData = classesLoad.state === "ready" ? classesLoad.data : null;
  const assets = assetPageData?.items ?? null;
  const classes = classPageData?.items ?? null;
  const classFilterValue =
    selectedClass !== null ? selectedClass : classSearch;
  const assetFilterValue =
    selectedAsset !== null ? selectedAsset : search;

  const [classSuggestions, setClassSuggestions] = useState<string[]>([]);
  const [assetSuggestions, setAssetSuggestions] = useState<string[]>([]);
  const visibleClassSuggestions =
    debouncedClassSearch.trim() === "" && selectedClass === null
      ? []
      : classSuggestions;
  const visibleAssetSuggestions =
    debouncedSearch.trim() === "" && selectedAsset === null ? [] : assetSuggestions;

  useEffect(() => {
    const query = (selectedClass ?? debouncedClassSearch).trim();
    if (query === "") {
      return;
    }
    const controller = new AbortController();
    void fetchAssetClasses(
      { code: query, codeMatch: "starts_with", limit: 8, sort: "code" },
      controller.signal,
    )
      .then((rows) => {
        const next = rows.map((row) => row.code);
        setClassSuggestions((prev) => (sameStrings(prev, next) ? prev : next));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setClassSuggestions((prev) => (prev.length === 0 ? prev : []));
        }
      });
    return () => controller.abort();
  }, [debouncedClassSearch, fetchAssetClasses, selectedClass]);

  useEffect(() => {
    const query = (selectedAsset ?? debouncedSearch).trim();
    if (query === "") {
      return;
    }
    const controller = new AbortController();
    void fetchAssets(
      { code: query, codeMatch: "starts_with", limit: 8, sort: "code" },
      controller.signal,
    )
      .then((rows) => {
        const next = rows.map((row) => row.code);
        setAssetSuggestions((prev) => (sameStrings(prev, next) ? prev : next));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setAssetSuggestions((prev) => (prev.length === 0 ? prev : []));
        }
      });
    return () => controller.abort();
  }, [debouncedSearch, fetchAssets, selectedAsset]);

  const loadError = load.state === "error" ? load.error : null;
  const classesError = classesLoad.state === "error" ? classesLoad.error : null;
  const isFiltered =
    debouncedSearch.trim() !== "" ||
    selectedAsset !== null ||
    selectedClass !== null ||
    debouncedClassSearch.trim() !== "";
  const isClassFiltered = debouncedClassSearch.trim() !== "";

  const reloadAll = () => {
    reload();
    reloadClasses();
  };
  const clearAssetFilters = () => {
    setSelectedAsset(null);
    setSelectedClass(null);
    setSearch("");
    setClassSearch("");
    setAssetPage(0);
  };
  const clearClassFilters = () => {
    setClassSearch("");
    setClassPage(0);
  };

  // The class filter control mirrors the Accounts group field: an exact class
  // selection (from a "filter by class" action) shows the selected code; typing
  // switches to a substring search over class codes.
  const shareHref = useMemo(() => {
    const query = new URLSearchParams();
    if (tab === "classes") {
      query.set("tab", "classes");
      if (classSearch.trim() !== "") {
        query.set("code", classSearch.trim());
      }
      if (classSort.sort !== undefined) {
        query.set("sort", classSort.sort);
        if (classSort.order !== undefined) {
          query.set("order", classSort.order);
        }
      }
      return shareUrl("/assets", query);
    }
    if (selectedClass !== null) {
      query.set("class", selectedClass);
    } else if (classSearch.trim() !== "") {
      query.set("classSearch", classSearch.trim());
    }
    if (selectedAsset !== null) {
      query.set("asset", selectedAsset);
    } else if (search.trim() !== "") {
      query.set("code", search.trim());
    }
    if (assetSort.sort !== undefined) {
      query.set("sort", assetSort.sort);
      if (assetSort.order !== undefined) {
        query.set("order", assetSort.order);
      }
    }
    return shareUrl("/assets", query);
  }, [
    assetSort.order,
    assetSort.sort,
    classSearch,
    classSort.order,
    classSort.sort,
    search,
    selectedAsset,
    selectedClass,
    tab,
  ]);
  const assetPager = (
    <TablePagination
      page={assetPage}
      canPrevious={assetPage > 0}
      canNext={
        assetPageData !== null && (assetPage + 1) * assetSize < assetPageData.total
      }
      knownTotalPages={
        assetPageData !== null
          ? knownPageCount(assetPageData.total, assetSize)
          : undefined
      }
      onPrevious={() => setAssetPage((p) => Math.max(0, p - 1))}
      onNext={() => setAssetPage((p) => p + 1)}
      onPage={setAssetPage}
    />
  );
  const classPager = (
    <TablePagination
      page={classPage}
      canPrevious={classPage > 0}
      canNext={
        classPageData !== null &&
        (classPage + 1) * classSize < classPageData.total
      }
      knownTotalPages={
        classPageData !== null
          ? knownPageCount(classPageData.total, classSize)
          : undefined
      }
      onPrevious={() => setClassPage((p) => Math.max(0, p - 1))}
      onNext={() => setClassPage((p) => p + 1)}
      onPage={setClassPage}
    />
  );

  return (
    <Page
      title={t("page.title")}
      actions={
        <>
          <PageSizeSelect
            value={tab === "assets" ? assetSize : classSize}
            onChange={(value) => {
              if (tab === "assets") {
                setAssetSize(value);
                setAssetPage(0);
              } else {
                setClassSize(value);
                setClassPage(0);
              }
            }}
            ariaLabel={t("pagination.pageSize.ariaLabel")}
            rowCountLabel={(count) =>
              t("pagination.pageSize.rowCount", { count })
            }
          />
          <RefreshButton
            onClick={reloadAll}
            busy={load.state === "loading" || classesLoad.state === "loading"}
          />
          <CreateClassDialog onCreated={reloadClasses} />
          <CreateAssetDialog
            classSuggestions={classSuggestions}
            onCreated={reloadAll}
          />
        </>
      }
    >
      <p className="text-xs text-muted-lt">{t("page.description")}</p>

      <div className="flex w-fit gap-1 rounded-card border border-border bg-surface-2 p-1">
        {(["assets", "classes"] as AssetsTab[]).map((tabId) => (
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

      {/* Assets panel */}
      {tab === "assets" && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">{t("assets.heading")}</p>

          <FilterBar
            active={isFiltered}
            activeLabel={tc("filters.active")}
            onClearActive={clearAssetFilters}
            clearActiveLabel={tc("filters.clearAll")}
            trailing={
              <div className="flex items-end">
                <ShareLinkButton
                  href={shareHref}
                  title={tc("rowActions.shareFilters")}
                  copiedTitle={tc("rowActions.copiedLink")}
                  size={32}
                />
              </div>
            }
          >
            <AutocompleteFilterField
              label={t("filters.classLabel")}
              value={classFilterValue}
              placeholder={t("filters.classPlaceholder")}
              suggestions={visibleClassSuggestions}
              onChange={(value) => {
                // Typing switches from an exact class selection to a substring
                // search, so only one class filter is ever active.
                setSelectedClass(null);
                setClassSearch(value);
                setAssetPage(0);
                setClassPage(0);
              }}
              onClear={() => {
                setSelectedClass(null);
                setClassSearch("");
                setAssetPage(0);
                setClassPage(0);
              }}
              clearLabel={tc("filters.clearField")}
              width={180}
            />
            <AutocompleteFilterField
              label={t("filters.searchLabel")}
              value={assetFilterValue}
              placeholder={t("filters.searchPlaceholder")}
              suggestions={visibleAssetSuggestions}
              onChange={(value) => {
                setSelectedAsset(null);
                setSearch(value);
                setAssetPage(0);
              }}
              onClear={() => {
                setSelectedAsset(null);
                setSearch("");
                setAssetPage(0);
              }}
              clearLabel={tc("filters.clearField")}
              width={260}
            />
          </FilterBar>

          {loadError !== null && assets === null ? (
            <ErrorState message={loadError} onRetry={reload} />
          ) : assets === null ? (
            <TableSkeleton cols={3} />
          ) : assets.length === 0 ? (
            <EmptyState
              title={t("empty.title")}
              hint={isFiltered ? t("empty.hintFiltered") : t("empty.hintEmpty")}
              action={
                isFiltered ? undefined : (
                  <CreateAssetDialog
                    classSuggestions={classSuggestions}
                    onCreated={reloadAll}
                  />
                )
              }
            />
          ) : (
            <>
              {assetPager}
              <AssetsTable
                assets={assets}
                selectedAsset={selectedAsset}
                selectedClass={selectedClass}
                activeSort={assetSort.sort}
                activeOrder={assetSort.order}
                onSortChange={(sort, order) => {
                  setAssetSort({ sort, order });
                  setAssetPage(0);
                }}
                onEditAsset={setEditTarget}
                onFilterAsset={(asset) => {
                  setSelectedAsset(asset.code);
                  setSearch("");
                  setAssetPage(0);
                }}
                onFilterClass={(assetClass) => {
                  setSelectedClass(assetClass);
                  setClassSearch("");
                  setAssetPage(0);
                }}
                onOpenPositions={(asset) =>
                  navigate(`/positions?asset=${encodeURIComponent(asset.code)}`)
                }
                onOpenOrders={(asset) =>
                  navigate(`/orders?baseAsset=${encodeURIComponent(asset.code)}`)
                }
                onOpenTrades={(asset) =>
                  navigate(
                    `/orders?tab=trades&baseAsset=${encodeURIComponent(asset.code)}`,
                  )
                }
                onOpenHistory={(asset) =>
                  navigate(`/audit?asset=${encodeURIComponent(asset.code)}`)
                }
                onDelete={setDeleteTarget}
              />
              {assetPager}
            </>
          )}
        </div>
      )}

      {/* Classes panel */}
      {tab === "classes" && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">{t("classes.heading")}</p>

          <FilterBar
            active={isClassFiltered}
            activeLabel={tc("filters.active")}
            onClearActive={clearClassFilters}
            clearActiveLabel={tc("filters.clearAll")}
            trailing={
              <div className="flex items-end">
                <ShareLinkButton
                  href={shareHref}
                  title={tc("rowActions.shareFilters")}
                  copiedTitle={tc("rowActions.copiedLink")}
                  size={32}
                />
              </div>
            }
          >
            <AutocompleteFilterField
              label={t("classes.filters.searchLabel")}
              value={classSearch}
              placeholder={t("classes.filters.searchPlaceholder")}
              suggestions={visibleClassSuggestions}
              onChange={(value) => {
                setClassSearch(value);
                setClassPage(0);
              }}
              onClear={() => {
                setClassSearch("");
                setClassPage(0);
              }}
              clearLabel={tc("filters.clearField")}
              width={260}
            />
          </FilterBar>

          {classesError !== null && classes === null ? (
            <ErrorState message={classesError} onRetry={reloadClasses} />
          ) : classes === null ? (
            <TableSkeleton cols={4} />
          ) : classes.length === 0 ? (
            <EmptyState
              title={t("classes.empty.title")}
              hint={
                isClassFiltered
                  ? t("classes.empty.hintFiltered")
                  : t("classes.empty.hintEmpty")
              }
              action={
                isClassFiltered ? undefined : (
                  <CreateClassDialog onCreated={reloadClasses} />
                )
              }
            />
          ) : (
            <>
              {classPager}
              <ClassesPanel
                classes={classes}
                selectedClass={selectedClass}
                activeSort={classSort.sort}
                activeOrder={classSort.order}
                onSortChange={(sort, order) => {
                  setClassSort({ sort, order });
                  setClassPage(0);
                }}
                onSelect={(code) => {
                  setSelectedClass(code);
                  setClassSearch("");
                  setAssetPage(0);
                  setTab("assets");
                }}
                onEdit={setEditClassTarget}
                onDelete={setDeleteClassTarget}
              />
              {classPager}
            </>
          )}
        </div>
      )}

      <EditAssetDialog
        asset={editTarget}
        classSuggestions={classSuggestions}
        open={editTarget !== null}
        onOpenChange={(next) => {
          if (!next) setEditTarget(null);
        }}
        onDone={(updated) => {
          if (editTarget !== null && selectedAsset === editTarget.code) {
            setSelectedAsset(updated.code);
          }
          setEditTarget(null);
          reloadAll();
        }}
      />
      <DeleteAssetConfirm
        asset={deleteTarget}
        open={deleteTarget !== null}
        onOpenChange={(next) => {
          if (!next) setDeleteTarget(null);
        }}
        onDone={() => {
          setDeleteTarget(null);
          reloadAll();
        }}
      />
      <EditClassDialog
        assetClass={editClassTarget}
        open={editClassTarget !== null}
        onOpenChange={(next) => {
          if (!next) setEditClassTarget(null);
        }}
        onDone={(updated) => {
          if (
            editClassTarget !== null &&
            selectedClass === editClassTarget.code
          ) {
            setSelectedClass(updated.code);
          }
          setEditClassTarget(null);
          reloadAll();
        }}
      />
      <DeleteClassConfirm
        assetClass={deleteClassTarget}
        open={deleteClassTarget !== null}
        onOpenChange={(next) => {
          if (!next) setDeleteClassTarget(null);
        }}
        onDone={() => {
          if (
            deleteClassTarget !== null &&
            selectedClass === deleteClassTarget.code
          ) {
            setSelectedClass(null);
          }
          setDeleteClassTarget(null);
          reloadAll();
        }}
      />
    </Page>
  );
}
