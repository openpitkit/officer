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

import { useDeferredValue, useMemo, useState } from "react";
import { ExternalLink, Pencil, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { ApiError, deleteLimit } from "@/api/client";
import type { Limit } from "@/api/types";
import { useAccounts } from "@/api/useAccounts";
import { useBalances } from "@/api/useBalances";
import { useLimits } from "@/api/useLimits";
import {
  POLICIES,
  getPolicyCatalogEntry,
  policyCatalogDescription,
  policyLabel,
  scopeLabel,
  type Policy,
} from "@/api/vocabulary";
import { Autocomplete } from "@/components/Autocomplete";
import { EmptyState, ErrorBanner, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import { ValueChips } from "@/components/ValueChips";
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
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { LimitDialog } from "@/pages/LimitDialog";

const ALL = "__all__";

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

// Policy description strip shown near the filter when a single policy is selected.
function PolicyDescription({ policy }: { policy: Policy | typeof ALL }) {
  const { t } = useTranslation("policies");
  if (policy === ALL) {
    return null;
  }
  const entry = getPolicyCatalogEntry(policy);
  if (!entry) {
    return null;
  }
  return (
    <p className="text-xs text-muted-lt">
      {policyCatalogDescription(t, entry.id)}{" "}
      <a
        href={entry.wikiUrl}
        target="_blank"
        rel="noreferrer"
        className="inline-flex items-center gap-0.5 text-accent hover:underline"
      >
        {t("wikiDetails")}
        <ExternalLink className="h-3 w-3" />
      </a>
    </p>
  );
}

function DeleteConfirm({
  target,
  open,
  requiresEngineRebuild,
  onOpenChange,
  onDone,
}: {
  target: Limit | null;
  open: boolean;
  requiresEngineRebuild: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("policies");
  const { t: tc } = useTranslation();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!target) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await deleteLimit({
        policy: target.policy,
        scope: target.scope,
        account: target.account,
        asset: target.asset,
      });
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
          <AlertDialogTitle>{t("delete.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            <span className="block">
              {t("delete.description", {
                policy: policyLabel(tc, target?.policy ?? ""),
                scope: scopeLabel(tc, target?.scope ?? ""),
                account: target?.account
                  ? t("delete.accountFragment", { account: target.account })
                  : "",
                asset: target?.asset
                  ? t("delete.assetFragment", { asset: target.asset })
                  : "",
              })}
            </span>
            {requiresEngineRebuild && (
              <span className="mt-2 block">
                {t("restartConfirm.description")}
              </span>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {tc("actions.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
            className="bg-[var(--danger)] hover:opacity-90"
          >
            {tc("actions.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function PoliciesTable({
  limits,
  onEdit,
  onDelete,
}: {
  limits: Limit[];
  onEdit: (limit: Limit) => void;
  onDelete: (limit: Limit) => void;
}) {
  const { t } = useTranslation("policies");
  const { t: tc } = useTranslation();
  return (
    <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("table.policy")}</TableHead>
            <TableHead>{t("table.scope")}</TableHead>
            <TableHead>{t("table.account")}</TableHead>
            <TableHead>{t("table.asset")}</TableHead>
            <TableHead>{t("table.values")}</TableHead>
            <TableHead className="text-right">{t("table.actions")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {limits.map((limit) => (
            <TableRow
              key={`${limit.policy}|${limit.scope}|${limit.account}|${limit.asset}`}
              className="hover:bg-transparent"
            >
              <TableCell>
                <Badge variant="accent">{policyLabel(tc, limit.policy)}</Badge>
              </TableCell>
              <TableCell className="text-xs text-muted-lt">
                {scopeLabel(tc, limit.scope)}
              </TableCell>
              <TableCell className="nums text-xs">
                {limit.account || tc("value.none")}
              </TableCell>
              <TableCell className="nums text-xs">
                {limit.asset || tc("value.none")}
              </TableCell>
              <TableCell>
                <ValueChips values={limit.values} />
              </TableCell>
              <TableCell>
                <div className="flex justify-end gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onEdit(limit)}
                    aria-label={t("table.editAriaLabel")}
                  >
                    <Pencil className="h-3.5 w-3.5" />
                    {t("table.edit")}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onDelete(limit)}
                    aria-label={t("table.deleteAriaLabel")}
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

export function Limits() {
  const { t } = useTranslation("policies");
  const { t: tc } = useTranslation();
  const [searchParams] = useSearchParams();
  const initialAccount = searchParams.get("account") ?? "";

  const [accountFilter, setAccountFilter] = useState(initialAccount);
  const [policyFilter, setPolicyFilter] = useState<Policy | typeof ALL>(ALL);

  // Defer the account filter so server-side polling does not refire on every
  // keystroke; the input stays responsive while the fetch debounces.
  const deferredAccount = useDeferredValue(accountFilter.trim());
  const { load, reload } = useLimits(deferredAccount);

  // Account suggestions from the accounts hook.
  const accountsLoad = useAccounts();
  const accountSuggestions = useMemo(() => {
    if (accountsLoad.load.state !== "ready") {
      return [];
    }
    return accountsLoad.load.data.map((a) => a.id);
  }, [accountsLoad.load]);

  // Asset suggestions: union of assets from limits and balances.
  const balancesLoad = useBalances();
  const assetSuggestions = useMemo(() => {
    const set = new Set<string>();
    if (load.state === "ready") {
      for (const l of load.data) {
        if (l.asset) {
          set.add(l.asset);
        }
      }
    }
    if (balancesLoad.load.state === "ready") {
      for (const b of balancesLoad.load.data) {
        if (b.asset) {
          set.add(b.asset);
        }
      }
    }
    return Array.from(set).sort();
  }, [load, balancesLoad.load]);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Limit | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Limit | null>(null);

  const visible = useMemo(() => {
    if (load.state !== "ready") {
      return [];
    }
    if (policyFilter === ALL) {
      return load.data;
    }
    return load.data.filter((l) => l.policy === policyFilter);
  }, [load, policyFilter]);

  const policyCounts = useMemo<Partial<Record<Policy, number>>>(() => {
    const counts: Partial<Record<Policy, number>> = {};
    if (load.state !== "ready") {
      return counts;
    }
    for (const limit of load.data) {
      const policy = limit.policy as Policy;
      counts[policy] = (counts[policy] ?? 0) + 1;
    }
    return counts;
  }, [load]);

  const deleteRequiresEngineRebuild =
    deleteTarget !== null &&
    (policyCounts[deleteTarget.policy as Policy] ?? 0) === 1;

  const openAdd = () => {
    setEditing(null);
    setDialogOpen(true);
  };

  const openEdit = (limit: Limit) => {
    setEditing(limit);
    setDialogOpen(true);
  };

  return (
    <Page
      title={t("title")}
      actions={
        <>
          <RefreshButton onClick={reload} busy={load.state === "loading"} />
          <Button size="sm" onClick={openAdd}>
            <Plus className="h-3.5 w-3.5" />
            {t("addPolicy")}
          </Button>
        </>
      }
    >
      <p className="text-xs text-muted-lt">{t("intro")}</p>

      <Card className="flex flex-wrap items-end gap-4 p-4">
        <div className="space-y-1.5">
          <Label htmlFor="filter-account">{t("filter.account")}</Label>
          <Autocomplete
            id="filter-account"
            value={accountFilter}
            spellCheck={false}
            placeholder={t("filter.accountPlaceholder")}
            className="h-8 w-48 text-xs"
            suggestions={accountSuggestions}
            onChange={setAccountFilter}
          />
        </div>
        <div className="space-y-1.5">
          <Label>{t("filter.policy")}</Label>
          <Select
            value={policyFilter}
            onValueChange={(v) => setPolicyFilter(v as Policy | typeof ALL)}
          >
            <SelectTrigger className="h-8 w-52 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t("filter.allPolicies")}</SelectItem>
              {POLICIES.map((p) => (
                <SelectItem key={p} value={p}>
                  {policyLabel(tc, p)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </Card>

      <PolicyDescription policy={policyFilter} />

      {load.state === "loading" && <TableSkeleton cols={6} />}
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" &&
        (visible.length === 0 ? (
          <EmptyState
            title={
              load.data.length === 0
                ? t("empty.noPolicies")
                : t("empty.noMatching")
            }
            hint={
              load.data.length === 0
                ? t("empty.noPoliciesHint")
                : t("empty.noMatchingHint")
            }
            action={
              load.data.length === 0 ? (
                <Button size="sm" onClick={openAdd}>
                  <Plus className="h-3.5 w-3.5" />
                  {t("addPolicy")}
                </Button>
              ) : undefined
            }
          />
        ) : (
          <PoliciesTable
            limits={visible}
            onEdit={openEdit}
            onDelete={setDeleteTarget}
          />
        ))}

      <LimitDialog
        open={dialogOpen}
        editing={editing}
        initialAccount={initialAccount}
        assetSuggestions={assetSuggestions}
        accountSuggestions={accountSuggestions}
        policyCounts={policyCounts}
        onOpenChange={setDialogOpen}
        onSaved={reload}
      />
      <DeleteConfirm
        target={deleteTarget}
        open={deleteTarget !== null}
        requiresEngineRebuild={deleteRequiresEngineRebuild}
        onOpenChange={(next) => {
          if (!next) {
            setDeleteTarget(null);
          }
        }}
        onDone={reload}
      />
    </Page>
  );
}
