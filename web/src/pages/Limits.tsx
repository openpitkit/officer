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

import {
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useState,
} from "react";
import type { KeyboardEvent } from "react";
import { ExternalLink, Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import type {
  Account,
  Limit,
  PolicyFilter,
  PolicyListFilters,
  SortOrder,
} from "@/api/types";
import { useLimitsPage } from "@/api/useLimits";
import {
  getPolicyCatalogEntry,
  policyCatalogDescription,
  policyLabel,
  scopeLabel,
} from "@/api/vocabulary";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  TableSkeleton,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import { PageSizeSelect, TablePagination } from "@/components/TableControls";
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
import {
  ApiError,
  AutocompleteFilterField,
  ColumnHeader,
  DeleteButton,
  EditButton,
  FieldLabel,
  FilterBar,
  FilterByButton,
  IdCell,
  AUTOCOMPLETE_SUGGESTION_LIMIT,
  MAX_LIST_LIMIT,
  RowActions,
  ShareLinkButton,
  SortableHeader,
  useOfficerApi,
} from "@/framework";
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
import { absoluteAppUrl } from "@/lib/shareLink";
import { sortDirection } from "@/lib/sortDirection";
import { knownPageCount } from "@/lib/tablePagination";
import { usePersistentPageSize } from "@/lib/tablePageSize";
import { useGlobalAccountFilter } from "@/lib/globalAccountFilter";
import { LimitDialog } from "@/pages/LimitDialog";

const ALL = "all";
const SCOPE_COLUMN_CLASS = "w-[var(--policies-scope-column-width)]";

// Wire policy-filter value -> the catalog kind id it selects, for labels and
// the description strip. The wire param is the short form; the catalog keys on
// the full kind id.
const POLICY_FILTER_KIND: Record<Exclude<PolicyFilter, "all">, string> = {
  rate: "rate_limit",
  order_size: "order_size_limit",
  spot_funds_pnl_bounds: "spot_funds_pnl_bounds_kill_switch",
};

const POLICY_FILTERS: Exclude<PolicyFilter, "all">[] = [
  "rate",
  "order_size",
  "spot_funds_pnl_bounds",
];

function policyFilterFromParams(params: URLSearchParams): PolicyFilter {
  const policy = params.get("policy");
  if (
    policy === "rate" ||
    policy === "order_size" ||
    policy === "spot_funds_pnl_bounds"
  ) {
    return policy;
  }
  return ALL;
}

function sortFromParams(params: URLSearchParams): {
  sort?: string;
  order?: SortOrder;
} {
  const sort = params.get("sort") ?? undefined;
  const order = params.get("order");
  return {
    sort,
    order: order === "asc" || order === "desc" ? order : undefined,
  };
}

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

function usePolicyCount(policy: Exclude<PolicyFilter, "all">) {
  const filters = useMemo<PolicyListFilters>(
    () => ({ policy, limit: 1, offset: 0 }),
    [policy],
  );
  return useLimitsPage(filters);
}

// Policy description strip shown near the filter when a single policy is selected.
function PolicyDescription({ policy }: { policy: PolicyFilter }) {
  const { t } = useTranslation("policies");
  if (policy === ALL) {
    return null;
  }
  const entry = getPolicyCatalogEntry(POLICY_FILTER_KIND[policy]);
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

function policyRowHref(limit: Limit): string {
  const query = new URLSearchParams();
  if (limit.account !== "") {
    query.set("account", limit.account);
  }
  if ((limit.accountGroup ?? "") !== "") {
    query.set("accountGroup", limit.accountGroup ?? "");
  }
  query.set("policy", limit.policy);
  return `/policies?${query.toString()}`;
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
  const { deleteLimit } = useOfficerApi();
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
        accountGroup: target.accountGroup,
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
                  : target?.accountGroup
                    ? t("delete.accountGroupFragment", {
                        accountGroup: target.accountGroup,
                      })
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
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
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
  activeSort,
  activeOrder,
  onSortChange,
  onFilterAccount,
  onFilterAsset,
  onEdit,
  onDelete,
}: {
  limits: Limit[];
  activeSort?: string;
  activeOrder?: SortOrder;
  onSortChange: (sort?: string, order?: SortOrder) => void;
  onFilterAccount: (account: string) => void;
  onFilterAsset: (asset: string) => void;
  onEdit: (limit: Limit) => void;
  onDelete: (limit: Limit) => void;
}) {
  const { t } = useTranslation("policies");
  const { t: tc } = useTranslation();
  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>
            <SortableHeader
              field="policy"
              label={t("table.policy")}
              description={t("table.columnDescriptions.policy")}
              direction={sortDirection(activeSort, activeOrder, "policy")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className={SCOPE_COLUMN_CLASS}>
            <SortableHeader
              field="scope"
              label={t("table.scope")}
              description={t("table.columnDescriptions.scope")}
              direction={sortDirection(activeSort, activeOrder, "scope")}
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
              field="account"
              label={t("table.account")}
              description={t("table.columnDescriptions.account")}
              direction={sortDirection(activeSort, activeOrder, "account")}
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
              field="asset"
              label={t("table.asset")}
              description={t("table.columnDescriptions.asset")}
              direction={sortDirection(activeSort, activeOrder, "asset")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.values")}>
              {t("table.values")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="text-right">
            <ColumnHeader
              align="right"
              description={t("table.columnDescriptions.actions")}
            >
              {t("table.actions")}
            </ColumnHeader>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {limits.map((limit) => {
          const accountAxis = limit.account || limit.accountGroup || "";
          const assetAxis = limit.asset;
          const isAccountFilterable = limit.account.length > 0;
          const isAssetFilterable = limit.asset.length > 0;
          return (
            <TableRow
              key={`${limit.policy}|${limit.scope}|${limit.account}|${limit.accountGroup ?? ""}|${limit.asset}`}
              className="hover:bg-transparent"
            >
              <TableCell>
                <Badge variant="accent">{policyLabel(tc, limit.policy)}</Badge>
              </TableCell>
              <TableCell
                className={`${SCOPE_COLUMN_CLASS} text-xs text-muted-lt`}
              >
                {scopeLabel(tc, limit.scope)}
              </TableCell>
              <TableCell className="nums text-xs">
                {accountAxis ? (
                  <div className="flex min-w-0 items-center gap-1">
                    <IdCell
                      value={accountAxis}
                      copyTitle={tc("rowActions.copyId")}
                      copiedTitle={tc("rowActions.copiedId")}
                    />
                    {isAccountFilterable && (
                      <span className="ml-auto flex shrink-0 items-center">
                        <FilterByButton
                          size={28}
                          title={tc("rowActions.filterByTitle", {
                            field: limit.account,
                          })}
                          href={absoluteAppUrl(
                            `/policies?account=${encodeURIComponent(limit.account)}`,
                          )}
                          onClick={() => onFilterAccount(limit.account)}
                        />
                      </span>
                    )}
                  </div>
                ) : (
                  tc("value.none")
                )}
              </TableCell>
              <TableCell className="nums text-xs">
                {assetAxis ? (
                  <div className="flex min-w-0 items-center gap-1">
                    <span className="min-w-0 truncate">{assetAxis}</span>
                    {isAssetFilterable && (
                      <span className="ml-auto flex shrink-0 items-center">
                        <FilterByButton
                          size={28}
                          title={tc("rowActions.filterByTitle", {
                            field: limit.asset,
                          })}
                          href={absoluteAppUrl(
                            `/policies?asset=${encodeURIComponent(limit.asset)}`,
                          )}
                          onClick={() => onFilterAsset(limit.asset)}
                        />
                      </span>
                    )}
                  </div>
                ) : (
                  tc("value.none")
                )}
              </TableCell>
              <TableCell>
                <ValueChips values={limit.values} />
              </TableCell>
              <TableCell className="text-right">
                <RowActions>
                  <ShareLinkButton
                    href={absoluteAppUrl(policyRowHref(limit))}
                    title={tc("rowActions.shareTitle", {
                      entity: policyLabel(tc, limit.policy),
                    })}
                    copiedTitle={tc("rowActions.copiedLink")}
                  />
                  <EditButton
                    title={tc("rowActions.editTitle", {
                      entity: policyLabel(tc, limit.policy),
                    })}
                    onClick={() => onEdit(limit)}
                  />
                  <DeleteButton
                    title={tc("rowActions.deleteTitle", {
                      entity: policyLabel(tc, limit.policy),
                    })}
                    onClick={() => onDelete(limit)}
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

export function Limits() {
  const { t } = useTranslation("policies");
  const { t: tc } = useTranslation();
  const officerApi = useOfficerApi();
  const { fetchAccounts, fetchAssets } = officerApi;
  const fetchGroups =
    "fetchGroups" in officerApi ? officerApi.fetchGroups : undefined;
  const [searchParams] = useSearchParams();
  const globalAccountFilter = useGlobalAccountFilter();
  const initialAccount =
    globalAccountFilter.account || searchParams.get("account") || "";
  const initialAccountGroup = searchParams.get("accountGroup") ?? "";
  const initialAsset = searchParams.get("asset") ?? "";

  const [accountFilter, setAccountFilter] = useState(initialAccount);
  const [accountGroupFilter, setAccountGroupFilter] =
    useState(initialAccountGroup);
  const [assetFilter, setAssetFilter] = useState(initialAsset);
  const [accountDraft, setAccountDraft] = useState(initialAccount);
  const [assetDraft, setAssetDraft] = useState(initialAsset);
  const [policyFilter, setPolicyFilter] = useState<PolicyFilter>(() =>
    policyFilterFromParams(searchParams),
  );
  const [sort, setSort] = useState<{ sort?: string; order?: SortOrder }>(() =>
    sortFromParams(searchParams),
  );
  const [page, setPage] = useState(0);
  const [size, setSize] = usePersistentPageSize(
    "pit-officer-policies-page-size",
  );

  const deferredAccountDraft = useDeferredValue(accountDraft.trim());
  const deferredAssetDraft = useDeferredValue(assetDraft.trim());
  const accountGlobalLocked =
    globalAccountFilter.account !== "" &&
    accountFilter.trim() === globalAccountFilter.account;
  const hasActiveFilters =
    accountFilter !== "" ||
    accountGroupFilter !== "" ||
    assetFilter !== "" ||
    policyFilter !== ALL;
  const clearFilters = () => {
    if (!accountGlobalLocked) {
      setAccountDraft("");
      setAccountFilter("");
    }
    setAccountGroupFilter("");
    setAssetDraft("");
    setAssetFilter("");
    setPolicyFilter(ALL);
    setPage(0);
  };
  const accountGlobalToggle = {
    active:
      accountDraft.trim() !== "" &&
      accountDraft.trim() === globalAccountFilter.account,
    disabled: accountDraft.trim() === "",
    activeLabel: tc("filters.globalAccount.active"),
    inactiveLabel: tc("filters.globalAccount.inactive"),
    disabledLabel: tc("filters.globalAccount.disabled"),
    onToggle: () => {
      const nextAccount = accountDraft.trim();
      if (nextAccount === "") {
        return;
      }
      if (globalAccountFilter.account === nextAccount) {
        globalAccountFilter.clear();
        return;
      }
      globalAccountFilter.setAccount(nextAccount);
      setAccountDraft(nextAccount);
      setAccountFilter(nextAccount);
      setPage(0);
    },
  };

  useEffect(() => {
    const nextAccount = globalAccountFilter.account;
    if (nextAccount === "") {
      return;
    }
    // Mirror the external global-account store into this page-local filter.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setAccountDraft(nextAccount);
    setAccountFilter(nextAccount);
    setPage(0);
  }, [globalAccountFilter.account]);

  const filters = useMemo<PolicyListFilters>(() => {
    const next: PolicyListFilters = {
      account: accountFilter || undefined,
      accountGroup: accountGroupFilter || undefined,
      asset: assetFilter || undefined,
      policy: policyFilter,
      sort: sort.sort,
      order: sort.order,
      limit: size,
      offset: page * size,
    };
    return next;
  }, [
    accountFilter,
    accountGroupFilter,
    assetFilter,
    page,
    policyFilter,
    size,
    sort.order,
    sort.sort,
  ]);
  const { load, reload } = useLimitsPage(filters);
  const rateCount = usePolicyCount("rate");
  const orderSizeCount = usePolicyCount("order_size");
  const spotFundsPnlBoundsCount = usePolicyCount("spot_funds_pnl_bounds");
  const policyCounts = useMemo(() => {
    const next: Record<string, number> = {};
    if (rateCount.load.state === "ready") {
      next[POLICY_FILTER_KIND.rate] = rateCount.load.data.total;
    }
    if (orderSizeCount.load.state === "ready") {
      next[POLICY_FILTER_KIND.order_size] = orderSizeCount.load.data.total;
    }
    if (spotFundsPnlBoundsCount.load.state === "ready") {
      next[POLICY_FILTER_KIND.spot_funds_pnl_bounds] =
        spotFundsPnlBoundsCount.load.data.total;
    }
    return next;
  }, [orderSizeCount.load, rateCount.load, spotFundsPnlBoundsCount.load]);
  const reloadPolicyCounts = useCallback(() => {
    rateCount.reload();
    orderSizeCount.reload();
    spotFundsPnlBoundsCount.reload();
  }, [orderSizeCount, rateCount, spotFundsPnlBoundsCount]);
  const reloadLimits = useCallback(() => {
    reload();
    reloadPolicyCounts();
  }, [reload, reloadPolicyCounts]);

  const [accountSuggestions, setAccountSuggestions] = useState<Account[]>([]);
  const [assetSuggestions, setAssetSuggestions] = useState<string[]>([]);
  const [accountGroupSuggestions, setAccountGroupSuggestions] = useState<
    string[]
  >([]);
  const visibleAccountSuggestions =
    deferredAccountDraft.trim() === ""
      ? []
      : accountSuggestions.map((account) => account.code);
  const visibleAssetSuggestions =
    deferredAssetDraft.trim() === "" ? [] : assetSuggestions;

  useEffect(() => {
    const query = deferredAccountDraft.trim();
    if (query === "") {
      return;
    }
    const controller = new AbortController();
    void fetchAccounts(
      {
        code: query,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then(setAccountSuggestions)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setAccountSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [deferredAccountDraft, fetchAccounts]);

  useEffect(() => {
    const query = deferredAssetDraft.trim();
    if (query === "") {
      return;
    }
    const controller = new AbortController();
    void fetchAssets(
      {
        code: query,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((assets) => setAssetSuggestions(assets.map((asset) => asset.code)))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setAssetSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [deferredAssetDraft, fetchAssets]);

  useEffect(() => {
    if (typeof fetchGroups !== "function") {
      return;
    }
    const controller = new AbortController();
    void fetchGroups({ limit: MAX_LIST_LIMIT, sort: "code" }, controller.signal)
      .then((groups) => {
        const groupCodes = groups
          .map((group) => group.code)
          .filter((code) => code !== "");
        setAccountGroupSuggestions(groupCodes);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setAccountGroupSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [fetchGroups]);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Limit | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Limit | null>(null);

  const rows = load.state === "ready" ? load.data.items : [];
  const total = load.state === "ready" ? load.data.total : 0;
  const hasMore = (page + 1) * size < total;
  const shownFrom = total === 0 ? 0 : page * size + 1;
  const shownTo = Math.min((page + 1) * size, total);
  const pager = (
    <TablePagination
      page={page}
      canPrevious={page > 0}
      canNext={hasMore}
      knownTotalPages={knownPageCount(total, size)}
      onPrevious={() => setPage((p) => Math.max(0, p - 1))}
      onNext={() => setPage((p) => p + 1)}
      onPage={setPage}
    />
  );

  const onSortChange = (nextSort?: string, nextOrder?: SortOrder) => {
    setSort({ sort: nextSort, order: nextOrder });
    setPage(0);
  };

  const identityDraftChanged =
    accountDraft.trim() !== accountFilter || assetDraft.trim() !== assetFilter;
  const applyIdentityFilters = () => {
    setAccountFilter(accountDraft.trim());
    setAssetFilter(assetDraft.trim());
    setPage(0);
  };
  const applyIdentityField = (field: "account" | "asset", value: string) => {
    const nextValue = value.trim();
    if (field === "account") {
      setAccountDraft(nextValue);
      setAccountFilter(nextValue);
    } else {
      setAssetDraft(nextValue);
      setAssetFilter(nextValue);
    }
    setPage(0);
  };
  const applyIdentityFiltersOnEnter = (
    event: KeyboardEvent<HTMLInputElement>,
  ) => {
    if (event.key === "Enter") {
      applyIdentityFilters();
    }
  };

  const openAdd = () => {
    setEditing(null);
    setDialogOpen(true);
  };

  const openEdit = (limit: Limit) => {
    setEditing(limit);
    setDialogOpen(true);
  };
  const accountIsSet = accountFilter.trim().length > 0;
  const assetIsSet = assetFilter.trim().length > 0;
  const policyIsSet = policyFilter !== ALL;
  const shareHref = useMemo(() => {
    const query = new URLSearchParams();
    const account = accountFilter.trim();
    const accountGroup = accountGroupFilter.trim();
    const asset = assetFilter.trim();
    if (account !== "") {
      query.set("account", account);
    }
    if (accountGroup !== "") {
      query.set("accountGroup", accountGroup);
    }
    if (asset !== "") {
      query.set("asset", asset);
    }
    if (policyFilter !== ALL) {
      query.set("policy", policyFilter);
    }
    if (sort.sort !== undefined) {
      query.set("sort", sort.sort);
      if (sort.order !== undefined) {
        query.set("order", sort.order);
      }
    }
    const text = query.toString();
    return absoluteAppUrl(`/policies${text === "" ? "" : `?${text}`}`);
  }, [
    accountFilter,
    accountGroupFilter,
    assetFilter,
    policyFilter,
    sort.order,
    sort.sort,
  ]);
  return (
    <Page
      title={t("title")}
      actions={
        <>
          <PageSizeSelect
            value={size}
            onChange={(value) => {
              setSize(value);
              setPage(0);
            }}
            ariaLabel={t("pagination.pageSize.ariaLabel")}
            rowCountLabel={(count) =>
              t("pagination.pageSize.rowCount", { count })
            }
          />
          <RefreshButton onClick={reload} busy={load.state === "loading"} />
          <Button size="sm" onClick={openAdd}>
            <Plus className="h-3.5 w-3.5" />
            {t("addPolicy")}
          </Button>
        </>
      }
    >
      <p className="text-xs text-muted-lt">{t("intro")}</p>

      <FilterBar
        active={hasActiveFilters}
        activeLabel={tc("filters.active")}
        onClearActive={clearFilters}
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
          label={t("filter.account")}
          value={accountDraft}
          placeholder={t("filter.accountPlaceholder")}
          suggestions={visibleAccountSuggestions}
          onChange={(value) => {
            if (accountGlobalLocked && value.trim() === "") {
              globalAccountFilter.clear();
            }
            setAccountDraft(value);
          }}
          onSuggestionSelect={(value) => applyIdentityField("account", value)}
          onKeyDown={applyIdentityFiltersOnEnter}
          onClear={() => {
            if (accountGlobalLocked) {
              globalAccountFilter.clear();
            }
            setAccountDraft("");
            setAccountFilter("");
            setPage(0);
          }}
          clearLabel={tc("filters.clearField")}
          globalToggle={accountGlobalToggle}
        />
        <AutocompleteFilterField
          label={t("filter.asset")}
          value={assetDraft}
          placeholder={t("filter.assetPlaceholder")}
          suggestions={visibleAssetSuggestions}
          onChange={setAssetDraft}
          onSuggestionSelect={(value) => applyIdentityField("asset", value)}
          onKeyDown={applyIdentityFiltersOnEnter}
          onClear={() => {
            setAssetDraft("");
            setAssetFilter("");
            setPage(0);
          }}
          clearLabel={tc("filters.clearField")}
        />
        <div className="flex items-end">
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={applyIdentityFilters}
            disabled={!identityDraftChanged}
          >
            {tc("filters.apply")}
          </Button>
        </div>
        <div className="grid gap-1">
          <FieldLabel>{t("filter.policy")}</FieldLabel>
          <Select
            value={policyFilter}
            onValueChange={(value) => {
              setPolicyFilter(value as PolicyFilter);
              setPage(0);
            }}
          >
            <SelectTrigger className="h-8 w-52 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t("filter.allPolicies")}</SelectItem>
              {POLICY_FILTERS.map((policy) => (
                <SelectItem key={policy} value={policy}>
                  {policyLabel(tc, POLICY_FILTER_KIND[policy])}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </FilterBar>

      <PolicyDescription policy={policyFilter} />

      {load.state === "loading" && <TableSkeleton cols={6} />}
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" &&
        (total === 0 ? (
          <EmptyState
            title={
              accountIsSet || policyIsSet || assetIsSet
                ? t("empty.noMatching")
                : t("empty.noPolicies")
            }
            hint={
              accountIsSet || policyIsSet || assetIsSet
                ? t("empty.noMatchingHint")
                : t("empty.noPoliciesHint")
            }
            action={
              accountIsSet || policyIsSet || assetIsSet ? undefined : (
                <Button size="sm" onClick={openAdd}>
                  <Plus className="h-3.5 w-3.5" />
                  {t("addPolicy")}
                </Button>
              )
            }
          />
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-xs text-muted-lt">
                {t("pagination.summary", {
                  from: shownFrom,
                  to: shownTo,
                  total,
                })}
              </span>
              {pager}
            </div>
            <PoliciesTable
              limits={rows}
              activeSort={sort.sort}
              activeOrder={sort.order}
              onSortChange={onSortChange}
              onFilterAccount={(account) => {
                setAccountDraft(account);
                setAccountFilter(account);
                setPage(0);
              }}
              onFilterAsset={(asset) => {
                setAssetDraft(asset);
                setAssetFilter(asset);
                setPage(0);
              }}
              onEdit={openEdit}
              onDelete={setDeleteTarget}
            />
            {pager}
          </>
        ))}

      <LimitDialog
        open={dialogOpen}
        editing={editing}
        initialAccount={initialAccount}
        assetSuggestions={assetSuggestions}
        accountSuggestions={accountSuggestions.map((account) => account.code)}
        accountGroupSuggestions={accountGroupSuggestions}
        policyCounts={policyCounts}
        onOpenChange={setDialogOpen}
        onSaved={reloadLimits}
      />
      <DeleteConfirm
        target={deleteTarget}
        open={deleteTarget !== null}
        requiresEngineRebuild={
          deleteTarget !== null &&
          deleteTarget.policy !== "spot_funds_pnl_bounds_kill_switch" &&
          policyCounts[deleteTarget.policy] !== undefined &&
          policyCounts[deleteTarget.policy] <= 1
        }
        onOpenChange={(next) => {
          if (!next) {
            setDeleteTarget(null);
          }
        }}
        onDone={reloadLimits}
      />
    </Page>
  );
}
