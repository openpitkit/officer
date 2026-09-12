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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, MouseEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  AlertTriangle,
  Calculator,
  ExternalLink,
  Plus,
  GitFork,
  Info,
  KeyRound,
  ShieldCheck,
} from "lucide-react";

import {
  AUTOCOMPLETE_SUGGESTION_LIMIT,
  ApiError,
  CloneButton,
  ColumnHeader,
  AutocompleteFilterField,
  ExactIdField,
  FieldLabel,
  FilterBar,
  FilterByButton,
  FilterChip,
  IdCell,
  MoreFiltersButton,
  NumberRangeFilter,
  reportInvalidFilterControls,
  RowActions,
  Segmented,
  SortableHeader,
  ShareLinkButton,
  TimeRangeFilter,
  ViewEntityButton,
  useOpenInNewTabHint,
  useOfficerApi,
  type ExecutionReportBody,
  type MissingAccountPolicy,
  type SortDirection,
  type TradesFilter,
} from "@/framework";
import type {
  ApprovalToken,
  CheckResult,
  Commission,
  ExecutionBlock,
  Order,
  OrderEvent,
  OrderListFilters,
  OrderSide,
  RangeFilterMode,
  Source,
  SortOrder,
  Trade,
} from "@/api/types";
import { useOrdersPage } from "@/api/useOrders";
import { useTradesPage } from "@/api/useTrades";
import { operatorOptions } from "@/lib/dataControlLabels";
import {
  ACTIVE_ORDER_STATUSES,
  ACTIVE_STATUS_SET,
  ALL_ORDER_STATUSES,
  parseStatusSet,
  sameStatusSet,
  TERMINAL_ORDER_STATUSES,
  type OrderStatusValue,
} from "@/lib/orderStatus";
import { formatDateTime } from "@/i18n/format";
import {
  DEFAULT_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/lib/useDebounce";
import { useGlobalAccountFilter } from "@/lib/globalAccountFilter";
import {
  isDecimalRangeValid,
  isNonNegativeDecimalString,
  isOpenQuantityString,
  isOptionalPositiveDecimalString,
  isPositiveDecimalString,
  subtractDecimalStrings,
} from "@/lib/numberStep";
import { shareUrl } from "@/lib/shareLink";
import { sortDirection } from "@/lib/sortDirection";
import { cn } from "@/lib/utils";
import { OrderVerificationPanel } from "@/pages/OrderVerificationPanel";
import { Autocomplete } from "@/components/Autocomplete";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  StaleState,
  TableSkeleton,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import {
  CsvTransferMenu,
  PageSizeSelect,
  TablePagination,
} from "@/components/TableControls";
import { Badge, type BadgeProps } from "@/components/ui/badge";
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
import { ClearableInput } from "@/components/ClearableInput";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { NumberStepper } from "@/components/ui/number-stepper";
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
import { knownPageCount } from "@/lib/tablePagination";
import { usePersistentPageSize } from "@/lib/tablePageSize";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

/** Order status → badge variant. */
function statusVariant(status: string): BadgeProps["variant"] {
  switch (status) {
    case "submitted":
      return "accent";
    case "accepted":
    case "committed":
    case "filled":
      return "ok";
    case "rejected":
    case "rolled_back":
    case "cancelled":
      return "danger";
    case "partially_filled":
      return "warn";
    default:
      return "neutral";
  }
}

/** Source → badge variant. */
function sourceVariant(source: Source): BadgeProps["variant"] {
  switch (source) {
    case "panel":
      return "accent";
    case "api":
      return "neutral";
    case "mcp":
      return "warn";
    case "system":
      return "neutral";
  }
}

/** Event type → badge variant. */
function eventTypeVariant(type: string): BadgeProps["variant"] {
  if (type.startsWith("reject")) {
    return "danger";
  }
  if (type === "fill" || type === "filled") {
    return "ok";
  }
  if (type === "accepted" || type === "created") {
    return "accent";
  }
  return "neutral";
}

/** Instrument label: "AAPL / USD". */
function instrument(baseAsset: string, quoteAsset: string): string {
  return `${baseAsset} / ${quoteAsset}`;
}

function commissionLabel(commissions: Commission[]): string {
  return commissions
    .filter(
      (commission) => commission.amount !== "" && commission.currency !== "",
    )
    .map((commission) => `${commission.amount} ${commission.currency}`)
    .join(", ");
}

function tradeCommissionLabel(trade: Trade): string {
  return trade.commission === undefined
    ? ""
    : commissionLabel([trade.commission]);
}

function ordersFilterHref({
  tab,
  account,
  baseAsset,
  quoteAsset,
  order,
}: {
  tab?: "orders" | "trades";
  account?: string;
  baseAsset?: string;
  quoteAsset?: string;
  order?: string;
}): string {
  const query = new URLSearchParams();
  if (tab === "trades") {
    query.set("tab", "trades");
  }
  if (order !== undefined && order !== "") {
    query.set("order", order);
  }
  if (account !== undefined && account !== "") {
    query.set("account", account);
  }
  if (baseAsset !== undefined && baseAsset !== "") {
    query.set("baseAsset", baseAsset);
  }
  if (quoteAsset !== undefined && quoteAsset !== "") {
    query.set("quoteAsset", quoteAsset);
  }
  return shareUrl("/orders", query);
}

// ---------------------------------------------------------------------------
// Order check preview
// ---------------------------------------------------------------------------

type CheckState =
  | { phase: "idle" }
  | { phase: "checking" }
  | { phase: "done"; result: CheckResult }
  | { phase: "error"; message: string };

function CheckPreview({ state }: { state: CheckState }) {
  const { t } = useTranslation("orders");

  if (state.phase === "idle") {
    return null;
  }
  if (state.phase === "checking") {
    return (
      <div className="animate-pulse rounded-card border border-border bg-surface-2 px-3 py-2 text-xs text-muted-lt">
        {t("check.checking")}
      </div>
    );
  }
  if (state.phase === "error") {
    return (
      <div className="rounded-card border border-border bg-surface-2 px-3 py-2 text-xs text-muted-lt">
        {t("check.previewUnavailable")}
      </div>
    );
  }
  const { result } = state;
  const wouldBlock = result.wouldBlock;
  let wouldBlockMessage = "";
  if (wouldBlock) {
    if (wouldBlock.code === "account_blocked") {
      wouldBlockMessage = t("check.accountBlocked", {
        account: wouldBlock.account,
      });
    } else if (wouldBlock.reason) {
      wouldBlockMessage = t("check.wouldBlockReason", {
        account: wouldBlock.account,
        reason: wouldBlock.reason,
      });
    } else {
      wouldBlockMessage = t("check.wouldBlock", {
        account: wouldBlock.account,
      });
    }
  }

  return (
    <div
      className={[
        "rounded-card border px-3 py-2 space-y-2 text-xs",
        result.passed
          ? "border-[var(--ok)] bg-[var(--ok-dim)]"
          : "border-[var(--danger)] bg-[var(--danger-dim)]",
      ].join(" ")}
    >
      <div className="flex items-center gap-2">
        <span
          className={
            result.passed
              ? "text-[var(--ok)] font-medium"
              : "text-[var(--danger)] font-medium"
          }
        >
          {result.passed ? t("check.wouldPass") : t("check.wouldReject")}
        </span>
      </div>
      {result.rejects.length > 0 && (
        <ul className="space-y-1">
          {result.rejects.map((r, i) => {
            const localizedReason = t(`check.rejectReasons.${r.code}`, {
              defaultValue: "",
            });
            const reason = localizedReason || r.reason || r.details || r.code;
            const details =
              r.details && r.details !== r.reason && r.details !== reason
                ? r.details
                : "";
            return (
              <li
                key={i}
                className="flex flex-wrap gap-x-2 gap-y-0.5 text-[var(--danger)]"
              >
                <span className="font-medium">{reason}</span>
                {details && <span>{details}</span>}
                {r.code && r.code !== reason && (
                  <span className="text-muted-lt text-[11px]">{r.code}</span>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {result.wouldDisplayPrice !== "" && (
        <div className="text-muted-lt">
          {t("check.displayPrice", { price: result.wouldDisplayPrice })}
        </div>
      )}
      {wouldBlock && (
        <div className="text-[var(--danger)]">{wouldBlockMessage}</div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Submit Order dialog
// ---------------------------------------------------------------------------

/** Input fields that can be preseeded when cloning an order. */
interface OrderInitialValues {
  account: string;
  baseAsset: string;
  quoteAsset: string;
  side: string;
  amountKind: string;
  amountValue: string;
  price: string;
}

interface SubmitOrderDialogProps {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
  onOpenDetail: (externalId: string, banner?: string) => void;
  /** Retain the accepted approval token of a workflow order so the operator can
   *  use the confirm/cancel shortcuts. Called only for accepted `hold` submits. */
  onWorkflowTokenIssued: (token: ApprovalToken) => void;
  accountSuggestions: string[];
  assetSuggestions: string[];
  /** Pre-seed all input fields (clone path). */
  initialValues?: OrderInitialValues;
}

function SubmitOrderDialog({
  open,
  onClose,
  onCreated,
  onOpenDetail,
  onWorkflowTokenIssued,
  accountSuggestions,
  assetSuggestions,
  initialValues,
}: SubmitOrderDialogProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();
  const { checkOrder, createOrder, fetchAccounts, fetchAssets } =
    useOfficerApi();

  const [externalId, setExternalId] = useState("");
  const [account, setAccount] = useState(initialValues?.account ?? "");
  const [baseAsset, setBaseAsset] = useState(initialValues?.baseAsset ?? "");
  const [quoteAsset, setQuoteAsset] = useState(initialValues?.quoteAsset ?? "");
  const [side, setSide] = useState<string>(initialValues?.side ?? "");
  const [amountKind, setAmountKind] = useState<string>(
    initialValues?.amountKind ?? "",
  );
  const [amountValue, setAmountValue] = useState(
    initialValues?.amountValue ?? "",
  );
  const [price, setPrice] = useState(initialValues?.price ?? "");
  const [submitMode, setSubmitMode] = useState<
    "immediate" | "drop_copy" | "hold" | null
  >(null);
  const [dropCopyConfirmOpen, setDropCopyConfirmOpen] = useState(false);
  const [missingAccountConfirmOpen, setMissingAccountConfirmOpen] =
    useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [checkState, setCheckState] = useState<CheckState>({ phase: "idle" });
  const debouncedAccount = useDebouncedValue(
    account.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedBaseAsset = useDebouncedValue(
    baseAsset.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedQuoteAsset = useDebouncedValue(
    quoteAsset.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const [dialogAccountSuggestions, setDialogAccountSuggestions] = useState<
    string[]
  >([]);
  const [dialogAssetSuggestions, setDialogAssetSuggestions] = useState<
    string[]
  >([]);
  const checkAbortRef = useRef<AbortController | null>(null);
  const submitAbortRef = useRef<AbortController | null>(null);
  const accountFieldRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    return () => {
      submitAbortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (debouncedAccount === "") {
      return;
    }
    const controller = new AbortController();
    fetchAccounts(
      {
        code: debouncedAccount,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((accounts) => {
        if (!controller.signal.aborted) {
          setDialogAccountSuggestions(accounts.map((item) => item.code));
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setDialogAccountSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [debouncedAccount, fetchAccounts]);

  useEffect(() => {
    const queries = Array.from(
      new Set(
        [debouncedBaseAsset, debouncedQuoteAsset].filter(
          (value) => value !== "",
        ),
      ),
    );
    if (queries.length === 0) {
      return;
    }
    const controller = new AbortController();
    Promise.all(
      queries.map((query) =>
        fetchAssets(
          {
            code: query,
            codeMatch: "starts_with",
            limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
            sort: "code",
          },
          controller.signal,
        ),
      ),
    )
      .then((pages) => {
        if (controller.signal.aborted) {
          return;
        }
        const next = new Set<string>();
        for (const page of pages) {
          for (const asset of page) {
            next.add(asset.code);
          }
        }
        setDialogAssetSuggestions(Array.from(next).sort().slice(0, 12));
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setDialogAssetSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [debouncedBaseAsset, debouncedQuoteAsset, fetchAssets]);

  // Debounced live check: fires after the shared search interval.
  useEffect(() => {
    if (submitMode === "drop_copy") {
      checkAbortRef.current?.abort();
      checkAbortRef.current = null;
      // Drop-copy still runs policies, but their rejects are intentionally not
      // an operator-facing verdict for this operation.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setCheckState({ phase: "idle" });
      return;
    }
    const accountT = account.trim();
    const baseT = baseAsset.trim();
    const quoteT = quoteAsset.trim();
    const amountT = amountValue.trim();
    // Skip when required fields are absent.
    if (
      !accountT ||
      !baseT ||
      !quoteT ||
      !amountT ||
      !isPositiveDecimalString(amountT) ||
      !isOptionalPositiveDecimalString(price) ||
      !amountKind ||
      !side
    ) {
      // Reset to idle when the form is incomplete; intentional sync.
      setCheckState({ phase: "idle" });
      return;
    }
    const timer = window.setTimeout(() => {
      checkAbortRef.current?.abort();
      const controller = new AbortController();
      checkAbortRef.current = controller;
      setCheckState({ phase: "checking" });
      const body: Parameters<typeof checkOrder>[0] = {
        account: accountT,
        baseAsset: baseT,
        quoteAsset: quoteT,
        side: side as OrderSide,
        amountKind,
        amountValue: amountT,
      };
      if (price.trim()) {
        body.price = price.trim();
      }
      checkOrder(body, controller.signal)
        .then((res) => {
          if (!controller.signal.aborted) {
            setCheckState({ phase: "done", result: res });
          }
        })
        .catch((err: unknown) => {
          if (controller.signal.aborted) {
            return;
          }
          if (err instanceof DOMException && err.name === "AbortError") {
            return;
          }
          setCheckState({ phase: "error", message: errMessage(err) });
        });
    }, DEFAULT_SEARCH_DEBOUNCE_MS);
    return () => {
      window.clearTimeout(timer);
      checkAbortRef.current?.abort();
    };
  }, [
    checkOrder,
    account,
    baseAsset,
    quoteAsset,
    side,
    amountKind,
    amountValue,
    price,
    submitMode,
  ]);

  // Reseed from initialValues whenever the dialog opens (clone path).
  useEffect(() => {
    if (open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setAccount(initialValues?.account ?? "");
      setBaseAsset(initialValues?.baseAsset ?? "");
      setQuoteAsset(initialValues?.quoteAsset ?? "");
      setExternalId("");
      setSide(initialValues?.side ?? "");
      setAmountKind(initialValues?.amountKind ?? "");
      setAmountValue(initialValues?.amountValue ?? "");
      setPrice(initialValues?.price ?? "");
      setSubmitMode(null);
      setDropCopyConfirmOpen(false);
      setBusy(false);
      setError(null);
      setCheckState({ phase: "idle" });
      checkAbortRef.current?.abort();
      checkAbortRef.current = null;
      submitAbortRef.current?.abort();
      submitAbortRef.current = null;
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  function reset() {
    setAccount(initialValues?.account ?? "");
    setBaseAsset(initialValues?.baseAsset ?? "");
    setQuoteAsset(initialValues?.quoteAsset ?? "");
    setExternalId("");
    setSide(initialValues?.side ?? "");
    setAmountKind(initialValues?.amountKind ?? "");
    setAmountValue(initialValues?.amountValue ?? "");
    setPrice(initialValues?.price ?? "");
    setSubmitMode(null);
    setDropCopyConfirmOpen(false);
    setMissingAccountConfirmOpen(false);
    setBusy(false);
    setError(null);
    setCheckState({ phase: "idle" });
    checkAbortRef.current?.abort();
    checkAbortRef.current = null;
    submitAbortRef.current?.abort();
    submitAbortRef.current = null;
  }

  function handleClose() {
    reset();
    onClose();
  }

  async function submit(missingAccount: MissingAccountPolicy = "reject") {
    if (
      !account.trim() ||
      !baseAsset.trim() ||
      !quoteAsset.trim() ||
      !amountValue.trim() ||
      !isPositiveDecimalString(amountValue)
    ) {
      setError(t("addOrder.dialog.validationError"));
      return;
    }
    if (!amountKind) {
      setError(t("addOrder.dialog.amountKindRequired"));
      return;
    }
    if (!side) {
      setError(t("addOrder.dialog.sideRequired"));
      return;
    }
    if (submitMode === null) {
      setError(t("addOrder.dialog.submitModeRequired"));
      return;
    }
    if (
      !isOptionalPositiveDecimalString(price) ||
      (submitMode === "drop_copy" && !isPositiveDecimalString(price))
    ) {
      setError(
        t(
          submitMode === "drop_copy"
            ? "addOrder.dialog.dropCopyPriceRequired"
            : "addOrder.dialog.validationError",
        ),
      );
      return;
    }
    setBusy(true);
    setError(null);
    submitAbortRef.current?.abort();
    const controller = new AbortController();
    submitAbortRef.current = controller;
    try {
      const submissionID = externalId.trim();
      const commonBody = {
        account: account.trim(),
        baseAsset: baseAsset.trim(),
        quoteAsset: quoteAsset.trim(),
        side: side as OrderSide,
        amountKind,
        amountValue: amountValue.trim(),
        ...(price.trim() ? { price: price.trim() } : {}),
      };
      const body: Parameters<typeof createOrder>[0] = {
        ...commonBody,
        ...(submissionID ? { id: submissionID } : {}),
        mode: submitMode,
      };
      // Drop-copy reports a fact that already happened, so its endpoint only
      // accepts "create" for an unknown account; "reject" is a 400 there.
      const effectiveMissingAccount: MissingAccountPolicy =
        submitMode === "drop_copy" ? "create" : missingAccount;
      const result = await createOrder(
        body,
        effectiveMissingAccount,
        controller.signal,
      );
      if (controller.signal.aborted) {
        return;
      }
      // A rejected pre-trade verdict has no accepted workflow to confirm or
      // cancel. Retain only accepted `hold` tokens for the history shortcuts.
      if (submitMode === "hold" && result.approval?.verdict === "accept") {
        onWorkflowTokenIssued(result.approval);
      }
      onCreated();
      reset();
      onClose();
      onOpenDetail(result.order.id, result.warning);
    } catch (err) {
      if (controller.signal.aborted) {
        return;
      }
      if (
        missingAccount === "reject" &&
        submitMode !== "drop_copy" &&
        err instanceof ApiError &&
        err.code === "account_missing"
      ) {
        setMissingAccountConfirmOpen(true);
        return;
      }
      setError(errMessage(err));
    } finally {
      if (submitAbortRef.current === controller) {
        submitAbortRef.current = null;
      }
      setBusy(false);
    }
  }

  function requestSubmit() {
    if (submitMode === "drop_copy") {
      setDropCopyConfirmOpen(true);
      return;
    }
    void submit();
  }

  // Every required choice must be made before the order can be submitted; the
  // button stays disabled until then so nothing slips through without a mode.
  const canSubmit =
    account.trim() !== "" &&
    baseAsset.trim() !== "" &&
    quoteAsset.trim() !== "" &&
    amountValue.trim() !== "" &&
    isPositiveDecimalString(amountValue) &&
    isOptionalPositiveDecimalString(price) &&
    (submitMode !== "drop_copy" || isPositiveDecimalString(price)) &&
    side !== "" &&
    amountKind !== "" &&
    submitMode !== null;

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) handleClose();
      }}
    >
      <DialogContent
        className="max-w-md"
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          accountFieldRef.current?.focus();
        }}
      >
        <DialogHeader>
          <DialogTitle>{t("addOrder.dialog.title")}</DialogTitle>
          <DialogDescription>
            {t("addOrder.dialog.description")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="so-external-id">
              {t("addOrder.dialog.externalId")}
            </Label>
            <ClearableInput
              id="so-external-id"
              value={externalId}
              spellCheck={false}
              placeholder={t("addOrder.dialog.externalIdPlaceholder")}
              onChange={(e) => setExternalId(e.target.value)}
              disabled={busy}
              onClear={() => setExternalId("")}
              clearLabel={tc("filters.clearField")}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("addOrder.dialog.externalIdHint")}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="so-account">{t("addOrder.dialog.account")}</Label>
            <Autocomplete
              ref={accountFieldRef}
              id="so-account"
              value={account}
              onChange={setAccount}
              suggestions={
                debouncedAccount === ""
                  ? []
                  : dialogAccountSuggestions.length > 0
                    ? dialogAccountSuggestions
                    : accountSuggestions
              }
              placeholder={t("addOrder.dialog.accountPlaceholder")}
              disabled={busy}
              onClear={() => setAccount("")}
              clearLabel={tc("filters.clearField")}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="so-base">{t("addOrder.dialog.baseAsset")}</Label>
              <Autocomplete
                id="so-base"
                value={baseAsset}
                onChange={setBaseAsset}
                suggestions={
                  debouncedBaseAsset === ""
                    ? []
                    : dialogAssetSuggestions.length > 0
                      ? dialogAssetSuggestions
                      : assetSuggestions
                }
                placeholder={t("addOrder.dialog.baseAssetPlaceholder")}
                disabled={busy}
                onClear={() => setBaseAsset("")}
                clearLabel={tc("filters.clearField")}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="so-quote">
                {t("addOrder.dialog.quoteAsset")}
              </Label>
              <Autocomplete
                id="so-quote"
                value={quoteAsset}
                onChange={setQuoteAsset}
                suggestions={
                  debouncedQuoteAsset === ""
                    ? []
                    : dialogAssetSuggestions.length > 0
                      ? dialogAssetSuggestions
                      : assetSuggestions
                }
                placeholder={t("addOrder.dialog.quoteAssetPlaceholder")}
                disabled={busy}
                onClear={() => setQuoteAsset("")}
                clearLabel={tc("filters.clearField")}
              />
            </div>
          </div>
          <div className="space-y-1.5">
            <Label id="so-side-label">{t("addOrder.dialog.side")}</Label>
            <div
              role="radiogroup"
              aria-labelledby="so-side-label"
              className="grid grid-cols-2 gap-2"
            >
              {(["buy", "sell"] as const).map((value) => {
                const selected = side === value;
                const selectedClass =
                  value === "buy"
                    ? "border-[var(--buy)] bg-[var(--buy-dim)] text-[var(--buy)]"
                    : "border-[var(--sell)] bg-[var(--sell-dim)] text-[var(--sell)]";
                return (
                  <button
                    key={value}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    onClick={() => setSide(value)}
                    disabled={busy}
                    className={[
                      "h-9 rounded-card border px-3 text-sm font-semibold transition-colors",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                      selected
                        ? selectedClass
                        : "border-border bg-surface-2 text-muted-lt hover:bg-surface-hover hover:text-text",
                    ].join(" ")}
                  >
                    {t(`addOrder.dialog.sideOptions.${value}`)}
                  </button>
                );
              })}
            </div>
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <Label id="so-mode-label">
                {t("addOrder.dialog.submitMode")}
              </Label>
              <a
                href="/docs"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 text-[0.6875rem] font-medium text-muted-lt underline-offset-2 hover:text-accent hover:underline"
              >
                {t("addOrder.dialog.openApi")}
                <ExternalLink className="h-3 w-3" />
              </a>
            </div>
            <div
              role="radiogroup"
              aria-labelledby="so-mode-label"
              className="grid grid-cols-3 gap-2"
            >
              {(["immediate", "drop_copy", "hold"] as const).map((mode) => {
                const selected = submitMode === mode;
                return (
                  <button
                    key={mode}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    onClick={() => setSubmitMode(mode)}
                    disabled={busy}
                    className={[
                      "min-h-[4.5rem] rounded-card border px-3 py-2 text-left transition-colors",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                      selected
                        ? "border-accent bg-accent-dim text-text"
                        : "border-border bg-surface-2 text-muted-lt hover:bg-surface-hover hover:text-text",
                    ].join(" ")}
                  >
                    <span className="flex items-center gap-1.5 text-xs font-semibold text-text">
                      {mode === "drop_copy" && (
                        <AlertTriangle className="h-3.5 w-3.5 text-[var(--danger)]" />
                      )}
                      {mode === "immediate"
                        ? t("addOrder.dialog.submitModeImmediate")
                        : mode === "drop_copy"
                          ? t("addOrder.dialog.submitModeDropCopy")
                          : t("addOrder.dialog.submitModeHold")}
                    </span>
                    <span className="mt-1 block text-[0.6875rem] leading-snug">
                      {mode === "immediate"
                        ? t("addOrder.dialog.submitModeImmediateHelp")
                        : mode === "drop_copy"
                          ? t("addOrder.dialog.submitModeDropCopyHelp")
                          : t("addOrder.dialog.submitModeHoldHelp")}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="so-amount">{t("addOrder.dialog.amount")}</Label>
            <div className="flex gap-2">
              <div
                role="radiogroup"
                aria-label={t("addOrder.dialog.amountKind")}
                className="flex shrink-0 gap-1"
              >
                {(["quantity", "volume"] as const).map((value) => {
                  const selected = amountKind === value;
                  return (
                    <button
                      key={value}
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      onClick={() => setAmountKind(value)}
                      disabled={busy}
                      className={[
                        "h-9 rounded-card border px-2.5 text-xs font-medium transition-colors",
                        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                        selected
                          ? "border-accent bg-accent-dim text-text"
                          : "border-border bg-surface-2 text-muted-lt hover:bg-surface-hover hover:text-text",
                      ].join(" ")}
                    >
                      {t(`addOrder.dialog.amountKindOptions.${value}`)}
                    </button>
                  );
                })}
              </div>
              <NumberStepper
                id="so-amount"
                value={amountValue}
                onChange={setAmountValue}
                placeholder={t("addOrder.dialog.amountPlaceholder")}
                disabled={busy}
                allowSignedInput={false}
                className="flex-1"
                onClear={() => setAmountValue("")}
                clearLabel={tc("filters.clearField")}
              />
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="so-price">
              {t(
                submitMode === "drop_copy"
                  ? "addOrder.dialog.limitPriceDropCopy"
                  : "addOrder.dialog.limitPrice",
              )}
            </Label>
            <NumberStepper
              id="so-price"
              value={price}
              onChange={setPrice}
              placeholder={t(
                submitMode === "drop_copy"
                  ? "addOrder.dialog.limitPriceDropCopyPlaceholder"
                  : "addOrder.dialog.limitPricePlaceholder",
              )}
              disabled={busy}
              allowSignedInput={false}
              onClear={() => setPrice("")}
              clearLabel={tc("filters.clearField")}
            />
            {submitMode === "drop_copy" && (
              <p className="text-xs text-muted">
                {t("addOrder.dialog.limitPriceDropCopyHint")}
              </p>
            )}
          </div>

          {submitMode !== "drop_copy" && <CheckPreview state={checkState} />}

          {error && (
            <ErrorBanner message={error} onDismiss={() => setError(null)} />
          )}

          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={handleClose}
              disabled={busy}
            >
              {tc("actions.cancel")}
            </Button>
            <Button
              size="sm"
              onClick={requestSubmit}
              disabled={busy || !canSubmit}
            >
              {busy
                ? t("addOrder.dialog.submitBusy")
                : submitMode === "drop_copy"
                  ? t("addOrder.dialog.submitDropCopy")
                  : t("addOrder.dialog.submit")}
            </Button>
          </DialogFooter>
        </div>
      </DialogContent>
      <AlertDialog
        open={dropCopyConfirmOpen}
        onOpenChange={setDropCopyConfirmOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-[var(--danger)]">
              {t("addOrder.dialog.dropCopyConfirmTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("addOrder.dialog.dropCopyConfirmDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {tc("actions.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="danger"
              disabled={busy}
              onClick={() => {
                setDropCopyConfirmOpen(false);
                void submit();
              }}
            >
              {t("addOrder.dialog.submitDropCopy")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog
        open={missingAccountConfirmOpen}
        onOpenChange={setMissingAccountConfirmOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("addOrder.dialog.missingAccountConfirmTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("addOrder.dialog.missingAccountConfirmDescription", {
                account: account.trim(),
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {tc("actions.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={busy}
              onClick={() => {
                setMissingAccountConfirmOpen(false);
                void submit("create");
              }}
            >
              {t("addOrder.dialog.missingAccountConfirmAction")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Execution Report dialog
// ---------------------------------------------------------------------------

/** Input fields that can be preseeded when cloning an execution report. */
interface ExecReportInitialValues {
  status?: ExecReportOrderStatus;
  quantity: string;
  price: string;
  lockPrice: string;
  commission?: Commission;
  /** Order's current remaining-open quantity. Absent for a trades-table clone
   * that carries no order detail. */
  leaves?: string;
  leavesQuantity?: string;
}

type CommissionMode = "none" | "fee" | "rebate";

const EXEC_REPORT_STATUS_OPTIONS = [
  "submitted",
  "accepted",
  "rejected",
  "committed",
  "rolled_back",
  "filled",
  "partially_filled",
  "cancelled",
] as const;

type ExecReportOrderStatus = (typeof EXEC_REPORT_STATUS_OPTIONS)[number];

/** Statuses that settle a fill through the engine and need fill fields. */
const EXEC_REPORT_FILL_STATUSES = new Set<ExecReportOrderStatus>([
  "filled",
  "partially_filled",
]);

// Only a fill status accepts a fill; every other status refuses the pair, so
// showing the fields there would build a report the API rejects.
function execReportShowsFillFields(status: ExecReportOrderStatus): boolean {
  return EXEC_REPORT_FILL_STATUSES.has(status);
}

function asExecReportOrderStatus(
  value: string | undefined,
): ExecReportOrderStatus | undefined {
  return EXEC_REPORT_STATUS_OPTIONS.includes(value as ExecReportOrderStatus)
    ? (value as ExecReportOrderStatus)
    : undefined;
}

function remainingLeavesFromHistory(
  order: Order,
  events: OrderEvent[],
): string {
  if (order.amountKind !== "quantity") {
    return "";
  }
  let remaining = order.amountValue;
  for (const event of events) {
    if (event.type !== "fill" || !event.fillQuantity) {
      continue;
    }
    const next = subtractDecimalStrings(remaining, event.fillQuantity);
    if (next === null) {
      return "";
    }
    remaining = next;
  }
  return remaining;
}

function execReportInitialValuesFromOrder(
  order: Order,
  events: OrderEvent[] = [],
): ExecReportInitialValues {
  const lockPrice = order.displayPrice;
  const leaves =
    order.leavesQuantity || remainingLeavesFromHistory(order, events);
  return {
    quantity: leaves,
    price: lockPrice,
    lockPrice,
    leaves,
  };
}

function commissionModeFor(amount: string | undefined): CommissionMode {
  if (amount === undefined || amount.trim() === "") {
    return "none";
  }
  return amount.trim().startsWith("-") ? "rebate" : "fee";
}

function commissionMagnitude(amount: string | undefined): string {
  return amount?.trim().replace(/^[+-]/, "") ?? "";
}

interface ExecReportDialogProps {
  orderExternalId: string | null;
  onClose: () => void;
  onSubmitted: (update: ExecReportSubmittedUpdate) => void;
  /** Pre-seed all input fields (clone path). */
  initialValues?: ExecReportInitialValues;
}

interface ExecReportSubmittedUpdate {
  orderExternalId: string;
}

/** Field values for a given status. Leaves is never auto-filled on status change. */
function execReportFieldsForStatus(
  status: ExecReportOrderStatus,
  initialValues: ExecReportInitialValues | undefined,
): {
  quantity: string;
  price: string;
  leavesQuantity: string;
  lockPrice: string;
} {
  // Leaves are recorded for every status, so a cloned value survives a status
  // the fill fields cannot follow.
  const leavesQuantity = initialValues?.leavesQuantity ?? "";
  if (execReportShowsFillFields(status)) {
    return {
      quantity: initialValues?.quantity ?? "",
      price: initialValues?.price ?? "",
      leavesQuantity,
      lockPrice: initialValues?.lockPrice ?? "",
    };
  }
  return { quantity: "", price: "", leavesQuantity, lockPrice: "" };
}

function ExecReportInfoButton({
  ariaLabel,
  title,
  body,
}: {
  ariaLabel: string;
  title: string;
  body: string;
}) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={ariaLabel}
          title={ariaLabel}
          className="h-6 w-6 shrink-0"
        >
          <Info className="h-3.5 w-3.5" aria-hidden="true" />
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{body}</DialogDescription>
        </DialogHeader>
      </DialogContent>
    </Dialog>
  );
}

function ExecReportDialog({
  orderExternalId,
  onClose,
  onSubmitted,
  initialValues,
}: ExecReportDialogProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();
  const { fetchAssets, submitExecutionReport } = useOfficerApi();
  const statusTriggerRef = useRef<HTMLButtonElement | null>(null);

  const initialStatus = initialValues?.status ?? "filled";
  const initialFields = execReportFieldsForStatus(initialStatus, initialValues);
  const [quantity, setQuantity] = useState(initialFields.quantity);
  const [price, setPrice] = useState(initialFields.price);
  const [leavesQuantity, setLeavesQuantity] = useState(
    initialFields.leavesQuantity,
  );
  const [lockPrice, setLockPrice] = useState(initialFields.lockPrice);
  const [commissionAmount, setCommissionAmount] = useState(
    commissionMagnitude(initialValues?.commission?.amount),
  );
  const [commissionCurrency, setCommissionCurrency] = useState(
    initialValues?.commission?.currency ?? "",
  );
  const [commissionMode, setCommissionMode] = useState<CommissionMode>(() =>
    commissionModeFor(initialValues?.commission?.amount),
  );
  const [status, setStatus] = useState<ExecReportOrderStatus>(initialStatus);
  const [force, setForce] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [reportId, setReportId] = useState("");
  const [blocks, setBlocks] = useState<ExecutionBlock[]>([]);
  const [commissionCurrencySuggestions, setCommissionCurrencySuggestions] =
    useState<string[]>([]);
  const debouncedCommissionCurrency = useDebouncedValue(
    commissionCurrency.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );

  const showFillFields = execReportShowsFillFields(status);
  const requiresLeaves = showFillFields;

  function resetEconomicsFields(nextInitialValues?: ExecReportInitialValues) {
    setCommissionMode(commissionModeFor(nextInitialValues?.commission?.amount));
    setCommissionAmount(
      commissionMagnitude(nextInitialValues?.commission?.amount),
    );
    setCommissionCurrency(nextInitialValues?.commission?.currency ?? "");
    setCommissionCurrencySuggestions([]);
  }

  useEffect(() => {
    if (commissionMode === "none" || debouncedCommissionCurrency === "") {
      return;
    }
    const controller = new AbortController();
    fetchAssets(
      {
        code: debouncedCommissionCurrency,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((assets) => {
        if (!controller.signal.aborted) {
          setCommissionCurrencySuggestions(assets.map((asset) => asset.code));
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setCommissionCurrencySuggestions([]);
        }
      });
    return () => controller.abort();
  }, [commissionMode, debouncedCommissionCurrency, fetchAssets]);

  function applyStatus(next: ExecReportOrderStatus) {
    const wasShowingFillFields = showFillFields;
    const nextShowsFillFields = execReportShowsFillFields(next);
    setStatus(next);
    if (wasShowingFillFields !== nextShowsFillFields) {
      const fields = nextShowsFillFields
        ? execReportFieldsForStatus(next, initialValues)
        : execReportFieldsForStatus(next, undefined);
      setQuantity(fields.quantity);
      setPrice(fields.price);
      setLockPrice(fields.lockPrice);
    }
  }

  // Reseed from initialValues whenever the dialog opens (clone path).
  useEffect(() => {
    if (orderExternalId !== null) {
      const nextStatus = initialValues?.status ?? "filled";
      const fields = execReportFieldsForStatus(nextStatus, initialValues);
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setStatus(nextStatus);
      setQuantity(fields.quantity);
      setPrice(fields.price);
      setLeavesQuantity(fields.leavesQuantity);
      setLockPrice(fields.lockPrice);
      resetEconomicsFields(initialValues);
      setForce(false);
      setBusy(false);
      setError(null);
      setDone(false);
      setReportId("");
      setBlocks([]);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orderExternalId]);

  function reset() {
    const nextStatus = initialValues?.status ?? "filled";
    const fields = execReportFieldsForStatus(nextStatus, initialValues);
    setStatus(nextStatus);
    setQuantity(fields.quantity);
    setPrice(fields.price);
    setLeavesQuantity(fields.leavesQuantity);
    setLockPrice(fields.lockPrice);
    resetEconomicsFields(initialValues);
    setForce(false);
    setBusy(false);
    setError(null);
    setDone(false);
    setReportId("");
    setBlocks([]);
  }

  function handleClose() {
    reset();
    onClose();
  }

  function calculateLeavesQuantity() {
    const result = subtractDecimalStrings(
      initialValues?.leaves ?? "",
      showFillFields ? quantity : "0",
    );
    if (result !== null) {
      setLeavesQuantity(result);
    }
  }

  const canCalculateLeaves =
    !busy &&
    (initialValues?.leaves ?? "").trim() !== "" &&
    (!showFillFields ||
      (quantity !== "" &&
        quantity === quantity.trim() &&
        isPositiveDecimalString(quantity)));
  // A fill status carries the pair and its leaves or the API refuses the whole
  // report; a commission never stands in for the trade. Values travel verbatim,
  // so the checks accept exactly what the report contract accepts.
  const fillFieldsValid =
    !showFillFields ||
    (quantity === quantity.trim() &&
      price === price.trim() &&
      lockPrice === lockPrice.trim() &&
      isPositiveDecimalString(quantity) &&
      isPositiveDecimalString(price) &&
      isOptionalPositiveDecimalString(lockPrice));
  const leavesValid =
    (!requiresLeaves && leavesQuantity === "") ||
    isOpenQuantityString(leavesQuantity);
  const commissionValid =
    commissionMode === "none" ||
    (commissionAmount.trim() !== "" &&
      isNonNegativeDecimalString(commissionAmount) &&
      commissionCurrency.trim() !== "");
  const canSubmit =
    orderExternalId !== null &&
    fillFieldsValid &&
    leavesValid &&
    commissionValid;

  function selectCommissionMode(next: CommissionMode) {
    setCommissionMode(next);
    if (next === "none") {
      setCommissionAmount("");
      setCommissionCurrency("");
      setCommissionCurrencySuggestions([]);
    }
  }

  async function submit() {
    const commissionAmountValue = commissionAmount.trim();
    const commissionCurrencyValue = commissionCurrency.trim();
    if (orderExternalId === null) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const body: ExecutionReportBody = { status };
      if (showFillFields) {
        body.quantity = quantity;
        body.price = price;
        if (lockPrice !== "") {
          body.lockPrice = lockPrice;
        }
      }
      if (leavesQuantity !== "") {
        body.leavesQuantity = leavesQuantity;
      }
      if (commissionMode !== "none") {
        body.commission = {
          amount:
            commissionMode === "fee"
              ? commissionAmountValue
              : `-${commissionAmountValue}`,
          currency: commissionCurrencyValue,
        };
      }
      if (force) {
        body.force = true;
      }
      const result = await submitExecutionReport(orderExternalId, body);
      setBlocks(result.blocks);
      setReportId(result.id);
      setDone(true);
      onSubmitted({ orderExternalId });
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={orderExternalId !== null}
      onOpenChange={(v) => {
        if (!v) handleClose();
      }}
    >
      <DialogContent
        className="max-w-sm"
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          statusTriggerRef.current?.focus();
        }}
      >
        <DialogHeader>
          <DialogTitle>{t("execReport.dialog.title")}</DialogTitle>
          <DialogDescription>
            {t("execReport.dialog.description", { orderExternalId })}
          </DialogDescription>
        </DialogHeader>

        {done ? (
          <div className="space-y-3">
            <p className="text-xs text-[var(--ok)]">
              {t("execReport.dialog.accepted")}
            </p>
            <div className="rounded-card border border-border bg-surface-2 px-3 py-2 text-xs">
              <span className="text-muted-lt">
                {t("execReport.dialog.reportId")}
              </span>
              <div className="mt-0.5">
                <IdCell
                  value={reportId}
                  copyTitle={t("common:rowActions.copyId")}
                  copiedTitle={t("common:rowActions.copiedId")}
                />
              </div>
            </div>
            {blocks.length > 0 && (
              <div className="space-y-1 rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] px-3 py-2 text-xs">
                {blocks.map((block, i) => (
                  <div key={i} className="text-[var(--danger)]">
                    <span className="font-medium">
                      {t("execReport.dialog.accountBlocked", {
                        account: block.account,
                      })}
                    </span>{" "}
                    {block.reason || block.code}
                  </div>
                ))}
              </div>
            )}
            <DialogFooter>
              <Button size="sm" onClick={handleClose}>
                {t("execReport.dialog.done")}
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="er-status">{t("execReport.dialog.status")}</Label>
              <Select
                value={status}
                onValueChange={(next) =>
                  applyStatus(next as ExecReportOrderStatus)
                }
                disabled={busy}
              >
                <SelectTrigger
                  id="er-status"
                  className="h-8 text-xs"
                  ref={statusTriggerRef}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EXEC_REPORT_STATUS_OPTIONS.map((option) => (
                    <SelectItem key={option} value={option}>
                      {t(`filter.status.${option}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-[0.6875rem] text-muted">
                {t(`execReport.dialog.statusHelp.${status}`)}
              </p>
            </div>

            {showFillFields && (
              <>
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-1.5">
                    <Label htmlFor="er-qty">
                      {t("execReport.dialog.fillQty")}
                    </Label>
                    <NumberStepper
                      id="er-qty"
                      value={quantity}
                      onChange={setQuantity}
                      placeholder={t("execReport.dialog.fillQtyPlaceholder")}
                      disabled={busy}
                      allowSignedInput={false}
                      onClear={() => setQuantity("")}
                      clearLabel={tc("filters.clearField")}
                      customValidity={
                        quantity === quantity.trim()
                          ? ""
                          : tc("filters.invalidNumber")
                      }
                    />
                    {quantity !== quantity.trim() && (
                      <p className="text-[0.6875rem] text-muted">
                        {tc("filters.invalidNumber")}
                      </p>
                    )}
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="er-price">
                      {t("execReport.dialog.fillPrice")}
                    </Label>
                    <NumberStepper
                      id="er-price"
                      value={price}
                      onChange={setPrice}
                      placeholder={t("execReport.dialog.fillPricePlaceholder")}
                      disabled={busy}
                      allowSignedInput={false}
                      onClear={() => setPrice("")}
                      clearLabel={tc("filters.clearField")}
                      customValidity={
                        price === price.trim()
                          ? ""
                          : tc("filters.invalidNumber")
                      }
                    />
                    {price !== price.trim() && (
                      <p className="text-[0.6875rem] text-muted">
                        {tc("filters.invalidNumber")}
                      </p>
                    )}
                  </div>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="er-lock">
                    {t("execReport.dialog.lockPrice")}
                  </Label>
                  <NumberStepper
                    id="er-lock"
                    value={lockPrice}
                    onChange={setLockPrice}
                    placeholder={t("execReport.dialog.lockPricePlaceholder")}
                    disabled={busy}
                    allowSignedInput={false}
                    onClear={() => setLockPrice("")}
                    clearLabel={tc("filters.clearField")}
                    customValidity={
                      lockPrice === lockPrice.trim()
                        ? ""
                        : tc("filters.invalidNumber")
                    }
                  />
                  {lockPrice !== lockPrice.trim() && (
                    <p className="text-[0.6875rem] text-muted">
                      {tc("filters.invalidNumber")}
                    </p>
                  )}
                </div>
              </>
            )}

            <div className="space-y-3">
              <section className="space-y-3 rounded-card border border-border bg-surface-2 p-3">
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <h3 className="text-xs font-semibold text-text">
                      {t("execReport.dialog.economics.commission.title")}
                    </h3>
                    <p className="text-[0.6875rem] text-muted">
                      {t("execReport.dialog.economics.commission.summary")}
                    </p>
                  </div>
                  <ExecReportInfoButton
                    ariaLabel={t(
                      "execReport.dialog.economics.commission.infoAriaLabel",
                    )}
                    title={t(
                      "execReport.dialog.economics.commission.infoTitle",
                    )}
                    body={t("execReport.dialog.economics.commission.infoBody")}
                  />
                </div>
                <div
                  className="flex flex-wrap gap-2"
                  role="group"
                  aria-label={t("execReport.dialog.economics.commission.title")}
                >
                  {(["none", "fee", "rebate"] as const).map((mode) => (
                    <Button
                      key={mode}
                      type="button"
                      variant="outline"
                      size="sm"
                      aria-pressed={commissionMode === mode}
                      disabled={busy}
                      onClick={() => selectCommissionMode(mode)}
                      className={cn(
                        mode === "none" && "text-muted hover:text-text",
                        mode === "fee" &&
                          "border-[var(--danger)] text-[var(--danger)] hover:border-[var(--danger)] hover:bg-[var(--danger-dim)] hover:text-[var(--danger)]",
                        mode === "rebate" &&
                          "border-[var(--ok)] text-[var(--ok)] hover:border-[var(--ok)] hover:bg-[var(--ok-dim)] hover:text-[var(--ok)]",
                        commissionMode === mode &&
                          mode === "none" &&
                          "bg-surface-hover text-text",
                        commissionMode === mode &&
                          mode === "fee" &&
                          "bg-[var(--danger-dim)]",
                        commissionMode === mode &&
                          mode === "rebate" &&
                          "bg-[var(--ok-dim)]",
                      )}
                    >
                      {t(`execReport.dialog.economics.commission.mode.${mode}`)}
                    </Button>
                  ))}
                </div>
                {commissionMode !== "none" && (
                  <div className="grid grid-cols-2 items-end gap-3">
                    <div className="space-y-1.5">
                      <Label htmlFor="er-commission-amount">
                        {t("execReport.dialog.commissionAmount")}
                      </Label>
                      <NumberStepper
                        id="er-commission-amount"
                        value={commissionAmount}
                        onChange={setCommissionAmount}
                        placeholder={t(
                          "execReport.dialog.commissionAmountPlaceholder",
                        )}
                        disabled={busy}
                        allowSignedInput={false}
                        onClear={() => setCommissionAmount("")}
                        clearLabel={tc("filters.clearField")}
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="er-commission-currency">
                        {t("execReport.dialog.commissionCurrency")}
                      </Label>
                      <Autocomplete
                        id="er-commission-currency"
                        value={commissionCurrency}
                        onChange={setCommissionCurrency}
                        suggestions={commissionCurrencySuggestions}
                        placeholder={t(
                          "execReport.dialog.commissionCurrencyPlaceholder",
                        )}
                        disabled={busy}
                        onClear={() => setCommissionCurrency("")}
                        clearLabel={tc("filters.clearField")}
                        className="h-8 text-xs"
                      />
                    </div>
                  </div>
                )}
              </section>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="er-leaves">
                {t("execReport.dialog.leavesQty")}
              </Label>
              <div className="flex gap-2">
                <ClearableInput
                  id="er-leaves"
                  value={leavesQuantity}
                  onChange={(event) => setLeavesQuantity(event.target.value)}
                  placeholder={t("execReport.dialog.leavesQtyPlaceholder")}
                  disabled={busy}
                  onClear={() => setLeavesQuantity("")}
                  clearLabel={tc("filters.clearField")}
                  className="min-w-0 flex-1"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  title={t("execReport.dialog.calculateLeaves")}
                  aria-label={t("execReport.dialog.calculateLeaves")}
                  disabled={!canCalculateLeaves}
                  onClick={calculateLeavesQuantity}
                >
                  <Calculator />
                </Button>
              </div>
              {leavesQuantity !== "" && !leavesValid && (
                <p className="text-[0.6875rem] text-muted">
                  {t("execReport.dialog.leavesInvalid")}
                </p>
              )}
            </div>

            <label className="flex items-center gap-2 text-xs text-text cursor-pointer select-none">
              <input
                type="checkbox"
                checked={force}
                onChange={(e) => setForce(e.target.checked)}
                disabled={busy}
                className="accent-[var(--accent)]"
              />
              {t("execReport.dialog.force")}
            </label>

            {error && (
              <ErrorBanner message={error} onDismiss={() => setError(null)} />
            )}

            <DialogFooter>
              <Button
                variant="outline"
                size="sm"
                onClick={handleClose}
                disabled={busy}
              >
                {tc("actions.cancel")}
              </Button>
              <Button size="sm" onClick={submit} disabled={busy || !canSubmit}>
                {busy
                  ? t("execReport.dialog.submitBusy")
                  : t("execReport.dialog.submit")}
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Order Detail dialog - header + event timeline + trades
// ---------------------------------------------------------------------------

interface OrderDetailDialogProps {
  orderExternalId: string | null;
  refreshKey: number;
  onClose: () => void;
  onExecReport: (
    orderExternalId: string,
    values?: ExecReportInitialValues,
  ) => void;
  onCloneOrder: (values: OrderInitialValues) => void;
  onCloneExecReport: (
    orderExternalId: string,
    values: ExecReportInitialValues,
  ) => void;
  /** Approval token for a workflow order, or null when none is retained
   *  (immediate order, or the token was lost on reload). */
  workflowToken: ApprovalToken | null;
  /** Called after a workflow shortcut succeeds so the page refetches server
   *  state. Confirmation keeps the token because it is history-only; a
   *  successful cancellation consumes it. */
  onWorkflowShortcutCompleted: (
    orderExternalId: string,
    action: "confirm" | "cancel",
  ) => void;
  successBanner?: string;
}

type DetailState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; order: Order; events: OrderEvent[]; trades: Trade[] };

/** Whether a timeline event carries a persisted attestation the operator can
 *  reproduce and verify. A signed event has a real Ed25519 signature; an
 *  eSign-off event (alg "none") is still attested and openable. Events with no
 *  attestation carry no alg and show no key. */
function eventHasAttestation(ev: OrderEvent): boolean {
  return ev.signed || ev.alg === "none";
}

function accountBlockReason(ev: OrderEvent): string | null {
  if (ev.rejectScope !== "account") {
    return null;
  }
  const reason = ev.rejectReason || ev.rejectCode;
  if (!reason) {
    return null;
  }
  const details: string[] = [];
  if (ev.rejectCode) {
    details.push(`code=${ev.rejectCode}`);
  }
  if (ev.rejectDetails) {
    details.push(ev.rejectDetails);
  }
  return details.length > 0 ? `${reason} [${details.join(", ")}]` : reason;
}

function OrderDetailDialog({
  orderExternalId,
  refreshKey,
  onClose,
  onExecReport,
  onCloneOrder,
  onCloneExecReport,
  workflowToken,
  onWorkflowShortcutCompleted,
  successBanner,
}: OrderDetailDialogProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();
  const { fetchOrderDetail, confirmOrder, cancelOrder } = useOfficerApi();

  const [state, setState] = useState<DetailState>({ phase: "loading" });
  // The event whose per-event reproduction/verification panel is open, or null
  // when the panel is closed. Officer signs each engine-processed request 1:1
  // with the event it produced, so verification is per timeline event.
  const [verifyEventId, setVerifyEventId] = useState<string | null>(null);
  // Workflow-shortcut UI state: the in-flight action and the last error.
  const [shortcutAction, setShortcutAction] = useState<
    "confirm" | "cancel" | null
  >(null);
  const [shortcutError, setShortcutError] = useState<string | null>(null);
  const [cancelFormOpen, setCancelFormOpen] = useState(false);
  const [cancelLeavesQuantity, setCancelLeavesQuantity] = useState("");

  useEffect(() => {
    if (orderExternalId === null) {
      return;
    }
    // Show the loading state before the detail fetch starts; intentional.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setState({ phase: "loading" });
    setVerifyEventId(null);
    setShortcutAction(null);
    setShortcutError(null);
    setCancelFormOpen(false);
    setCancelLeavesQuantity("");
    const controller = new AbortController();
    fetchOrderDetail(orderExternalId, controller.signal)
      .then(({ order, events, trades }) => {
        if (controller.signal.aborted) {
          return;
        }
        setState({ phase: "ready", order, events, trades });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ phase: "error", message: errMessage(err) });
        }
      });
    return () => controller.abort();
  }, [fetchOrderDetail, orderExternalId, refreshKey]);

  if (orderExternalId === null) {
    return null;
  }

  // Amount label: "100 qty" or "500 vol".
  function amountLabel(kind: string, value: string): string {
    return kind === "quantity"
      ? t("amount.qty", { value })
      : t("amount.vol", { value });
  }

  // Price display: empty string or "0" → "market".
  function priceLabel(price: string): string {
    if (!price || price === "0") {
      return t("price.market");
    }
    return price;
  }

  // The backend owns activity validation; the UI only checks whether this
  // session still carries a token it can submit.
  const canUseWorkflowShortcut =
    state.phase === "ready" && workflowToken !== null;

  // Cancellation leaves are optional and recorded verbatim: an absent value is
  // the request's own shape, a supplied one must survive the report contract.
  const cancelLeavesValid =
    cancelLeavesQuantity === "" || isOpenQuantityString(cancelLeavesQuantity);

  async function runWorkflowShortcut(action: "confirm" | "cancel") {
    if (
      orderExternalId === null ||
      workflowToken === null ||
      shortcutAction !== null
    ) {
      return;
    }
    if (action === "cancel" && !cancelLeavesValid) {
      return;
    }
    setShortcutAction(action);
    setShortcutError(null);
    try {
      if (action === "confirm") {
        await confirmOrder(orderExternalId, {
          token: workflowToken.token,
        });
      } else {
        await cancelOrder(orderExternalId, {
          token: workflowToken.token,
          ...(cancelLeavesQuantity === ""
            ? {}
            : { leavesQuantity: cancelLeavesQuantity }),
        });
      }
      onWorkflowShortcutCompleted(orderExternalId, action);
    } catch (err) {
      setShortcutError(errMessage(err));
    } finally {
      setShortcutAction(null);
    }
  }

  return (
    <>
      <Dialog
        open
        onOpenChange={(v) => {
          if (!v) {
            setVerifyEventId(null);
            onClose();
          }
        }}
      >
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {t("detail.dialog.title", { orderExternalId })}
            </DialogTitle>
            <DialogDescription>
              {state.phase === "ready" ? (
                <>
                  {instrument(state.order.baseAsset, state.order.quoteAsset)}
                  {" · "}
                  <span className="capitalize">{state.order.side}</span>
                  {" · "}
                  {amountLabel(state.order.amountKind, state.order.amountValue)}
                  {" · "}
                  {priceLabel(state.order.price)}
                </>
              ) : (
                t("detail.dialog.loading")
              )}
            </DialogDescription>
          </DialogHeader>

          {successBanner && (
            <div className="rounded-card border border-[var(--ok)] bg-[var(--ok-dim)] px-3 py-2 text-xs font-medium text-[var(--ok)]">
              {successBanner}
            </div>
          )}

          {state.phase === "loading" && (
            <div className="space-y-2 py-4">
              {[1, 2, 3].map((i) => (
                <div
                  key={i}
                  className="h-4 w-full animate-pulse rounded bg-border"
                />
              ))}
            </div>
          )}

          {state.phase === "error" && (
            <p className="text-xs text-[var(--danger)]">{state.message}</p>
          )}

          {state.phase === "ready" && (
            <div className="space-y-5">
              {/* Order header fields */}
              <div className="grid grid-cols-3 gap-2 rounded-card border border-border bg-surface-2 p-3 text-xs">
                <div>
                  <span className="text-muted-lt">{t("table.externalId")}</span>
                  <div className="mt-0.5">
                    <IdCell
                      value={state.order.id}
                      copyTitle={t("common:rowActions.copyId")}
                      copiedTitle={t("common:rowActions.copiedId")}
                    />
                    {state.order.dropCopy && (
                      <Badge variant="danger">{t("dropCopy.badge")}</Badge>
                    )}
                  </div>
                </div>
                <div>
                  <span className="text-muted-lt">
                    {t("detail.dialog.fieldAccount")}
                  </span>
                  <div className="mt-0.5">
                    <IdCell
                      value={state.order.account}
                      copyTitle={t("common:rowActions.copyId")}
                      copiedTitle={t("common:rowActions.copiedId")}
                    />
                  </div>
                </div>
                <div>
                  <span className="text-muted-lt">
                    {t("detail.dialog.fieldStatus")}
                  </span>
                  <div className="mt-0.5">
                    <Badge variant={statusVariant(state.order.status)}>
                      {state.order.status}
                    </Badge>
                  </div>
                </div>
                <div>
                  <span className="text-muted-lt">
                    {t("detail.dialog.fieldSource")}
                  </span>
                  <div className="mt-0.5">
                    <Badge variant={sourceVariant(state.order.source)}>
                      {state.order.source}
                    </Badge>
                  </div>
                </div>
                <div className="col-span-3">
                  <span className="text-muted-lt">
                    {t("detail.dialog.fieldSubmitted")}
                  </span>
                  <div className="nums mt-0.5 text-muted-lt">
                    {formatDateTime(state.order.at)}
                  </div>
                </div>
                {state.order.displayPrice !== "" && (
                  <div className="col-span-3">
                    <span className="text-muted-lt">
                      {t("detail.dialog.fieldDisplayPrice")}
                    </span>
                    <div className="nums mt-0.5 text-text">
                      {state.order.displayPrice}
                    </div>
                  </div>
                )}
                {state.order.commissionSubtotals.length > 0 && (
                  <div className="col-span-3">
                    <span className="text-muted-lt">
                      {t("detail.dialog.fieldCommission")}
                    </span>
                    <div className="nums mt-0.5 text-text">
                      {commissionLabel(state.order.commissionSubtotals)}
                    </div>
                  </div>
                )}
              </div>

              {/* Event timeline */}
              <div>
                <p className="mb-2 text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
                  {t("detail.dialog.timeline.sectionTitle")}
                </p>
                {state.events.length === 0 ? (
                  <p className="text-xs text-muted-lt">
                    {t("detail.dialog.timeline.empty")}
                  </p>
                ) : (
                  <ol className="space-y-2">
                    {state.events.map((ev) => {
                      const blockReason = accountBlockReason(ev);
                      return (
                        <li
                          key={ev.id}
                          className="flex gap-3 rounded-card border border-border bg-surface-2 p-2.5 text-xs"
                        >
                          <div className="w-32 shrink-0">
                            <div className="nums text-muted-lt">
                              {formatDateTime(ev.at)}
                            </div>
                          </div>
                          <div className="flex flex-1 flex-wrap items-start gap-x-3 gap-y-1">
                            <Badge variant={eventTypeVariant(ev.type)}>
                              {ev.type}
                            </Badge>
                            <Badge variant={sourceVariant(ev.source)}>
                              {ev.source}
                            </Badge>
                            {ev.principal && (
                              <span className="text-muted-lt">
                                {t("detail.dialog.timeline.by", {
                                  principal: ev.principal,
                                })}
                              </span>
                            )}
                            {/* Fill payload */}
                            {ev.fillQuantity !== undefined && (
                              <span className="text-text">
                                {t("detail.dialog.timeline.fillQty", {
                                  qty: ev.fillQuantity,
                                })}
                                {ev.fillPrice !== undefined && (
                                  <>
                                    {" "}
                                    {t("detail.dialog.timeline.fillAt", {
                                      price: ev.fillPrice,
                                    })}
                                  </>
                                )}
                                {ev.fillLockPrice !== undefined && (
                                  <span className="text-muted-lt">
                                    {" "}
                                    {t("detail.dialog.timeline.fillLock", {
                                      price: ev.fillLockPrice,
                                    })}
                                  </span>
                                )}
                              </span>
                            )}
                            {ev.commission !== undefined && (
                              <span className="nums text-muted-lt">
                                {t("detail.dialog.timeline.commission", {
                                  value: commissionLabel([ev.commission]),
                                })}
                              </span>
                            )}
                            {/* Reject payload */}
                            {blockReason === null &&
                              ev.rejectCode !== undefined && (
                                <span className="text-[var(--danger)]">
                                  {t("detail.dialog.timeline.rejectCode", {
                                    code: ev.rejectCode,
                                  })}
                                </span>
                              )}
                            {blockReason === null &&
                              ev.rejectScope !== undefined && (
                                <span className="text-muted-lt">
                                  {t("detail.dialog.timeline.rejectScope", {
                                    scope: ev.rejectScope,
                                  })}
                                </span>
                              )}
                            {blockReason === null &&
                              ev.rejectPolicy !== undefined && (
                                <span className="text-muted-lt">
                                  {t("detail.dialog.timeline.rejectPolicy", {
                                    policy: ev.rejectPolicy,
                                  })}
                                </span>
                              )}
                            {blockReason === null &&
                              ev.rejectReason !== undefined && (
                                <span className="text-muted-lt">
                                  {ev.rejectReason}
                                </span>
                              )}
                            {blockReason === null &&
                              ev.rejectDetails !== undefined && (
                                <span className="text-muted-lt">
                                  {ev.rejectDetails}
                                </span>
                              )}
                            {blockReason !== null && (
                              <div className="w-full rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] px-3 py-2 text-[var(--danger)]">
                                <span className="font-medium">
                                  {t("detail.dialog.accountBlocked", {
                                    account: state.order.account,
                                  })}
                                </span>
                                <> {blockReason}</>
                              </div>
                            )}
                          </div>
                          {/* Per-event attestation: open the reproduction /
                          verification panel for this event's signature. */}
                          {eventHasAttestation(ev) && (
                            <button
                              type="button"
                              className={cn(
                                "shrink-0 self-start rounded-badge p-1 text-muted-lt transition-colors duration-[180ms] hover:text-accent focus-visible:text-accent",
                                verifyEventId === ev.id && "text-accent",
                              )}
                              aria-label={
                                ev.signed
                                  ? t("detail.dialog.timeline.verifySigned")
                                  : t("detail.dialog.timeline.verifyUnsigned")
                              }
                              title={
                                ev.signed
                                  ? t("detail.dialog.timeline.verifySigned")
                                  : t("detail.dialog.timeline.verifyUnsigned")
                              }
                              onClick={() => setVerifyEventId(ev.id)}
                            >
                              <KeyRound
                                className={cn(
                                  "h-3.5 w-3.5",
                                  !ev.signed && "opacity-60",
                                )}
                              />
                            </button>
                          )}
                          {/* Re-issue the engine action this event recorded:
                          a submission clones the order, a fill clones the report. */}
                          {ev.type === "submitted" && (
                            <Button
                              variant="ghost"
                              size="sm"
                              className="shrink-0 self-start"
                              aria-label={t("clone.orderAriaLabel", {
                                orderExternalId,
                              })}
                              onClick={() => {
                                onCloneOrder({
                                  account: state.order.account,
                                  baseAsset: state.order.baseAsset,
                                  quoteAsset: state.order.quoteAsset,
                                  side: state.order.side,
                                  amountKind: state.order.amountKind,
                                  amountValue: state.order.amountValue,
                                  price:
                                    state.order.price === "0"
                                      ? ""
                                      : state.order.price,
                                });
                                onClose();
                              }}
                            >
                              <GitFork className="h-3.5 w-3.5" />
                            </Button>
                          )}
                          {ev.type === "fill" &&
                            ev.fillQuantity !== undefined && (
                              <Button
                                variant="ghost"
                                size="sm"
                                className="shrink-0 self-start"
                                aria-label={t(
                                  "clone.execReportEventAriaLabel",
                                  { orderExternalId },
                                )}
                                onClick={() =>
                                  onCloneExecReport(orderExternalId, {
                                    status: asExecReportOrderStatus(
                                      ev.orderStatus,
                                    ),
                                    quantity: ev.fillQuantity ?? "",
                                    price: ev.fillPrice ?? "",
                                    lockPrice: ev.fillLockPrice ?? "",
                                    leavesQuantity: ev.leavesQuantity ?? "",
                                    commission: ev.commission,
                                  })
                                }
                              >
                                <GitFork className="h-3.5 w-3.5" />
                              </Button>
                            )}
                        </li>
                      );
                    })}
                  </ol>
                )}
              </div>

              {/* Trades - always shown so the order→trades grouping is clear */}
              <div>
                <p className="mb-2 text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
                  {t("detail.dialog.trades.sectionTitle")}
                </p>
                {state.trades.length === 0 ? (
                  <p className="text-xs text-muted-lt">
                    {t("detail.dialog.trades.empty")}
                  </p>
                ) : (
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead>
                          <ColumnHeader
                            description={t(
                              "table.columnDescriptions.externalId",
                            )}
                          >
                            {t("table.externalId")}
                          </ColumnHeader>
                        </TableHead>
                        <TableHead>
                          <ColumnHeader
                            description={t("table.columnDescriptions.qty")}
                          >
                            {t("table.qty")}
                          </ColumnHeader>
                        </TableHead>
                        <TableHead>
                          <ColumnHeader
                            description={t("table.columnDescriptions.price")}
                          >
                            {t("table.price")}
                          </ColumnHeader>
                        </TableHead>
                        <TableHead>
                          <ColumnHeader
                            description={t(
                              "table.columnDescriptions.lockPrice",
                            )}
                          >
                            {t("table.lockPrice")}
                          </ColumnHeader>
                        </TableHead>
                        <TableHead>
                          <ColumnHeader
                            description={t(
                              "table.columnDescriptions.commission",
                            )}
                          >
                            {t("table.commission")}
                          </ColumnHeader>
                        </TableHead>
                        <TableHead>
                          <ColumnHeader
                            description={t("table.columnDescriptions.source")}
                          >
                            {t("table.source")}
                          </ColumnHeader>
                        </TableHead>
                        <TableHead>
                          <ColumnHeader
                            description={t("table.columnDescriptions.time")}
                          >
                            {t("table.time")}
                          </ColumnHeader>
                        </TableHead>
                        <TableHead className="text-right" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {state.trades.map((trade) => (
                        <TableRow
                          key={trade.id}
                          className="hover:bg-transparent"
                        >
                          <TableCell className="nums text-xs text-muted-lt">
                            <IdCell
                              value={trade.id}
                              copyTitle={t("common:rowActions.copyId")}
                              copiedTitle={t("common:rowActions.copiedId")}
                            />
                          </TableCell>
                          <TableCell className="nums text-xs">
                            {trade.quantity}
                          </TableCell>
                          <TableCell className="nums text-xs">
                            {trade.price}
                          </TableCell>
                          <TableCell className="nums text-xs text-muted-lt">
                            {trade.lockPrice || tc("value.none")}
                          </TableCell>
                          <TableCell className="nums text-xs text-muted-lt">
                            {tradeCommissionLabel(trade) || tc("value.none")}
                          </TableCell>
                          <TableCell>
                            <Badge variant={sourceVariant(trade.source)}>
                              {trade.source}
                            </Badge>
                          </TableCell>
                          <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
                            {formatDateTime(trade.at)}
                          </TableCell>
                          <TableCell className="text-right">
                            <RowActions>
                              <CloneButton
                                title={t("clone.execReportAriaLabel", {
                                  tradeId: trade.id,
                                })}
                                onClick={() =>
                                  onCloneExecReport(orderExternalId, {
                                    quantity: trade.quantity,
                                    price: trade.price,
                                    lockPrice: trade.lockPrice,
                                    commission: trade.commission,
                                    leaves: state.order.leavesQuantity,
                                  })
                                }
                              />
                            </RowActions>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                )}
              </div>

              {canUseWorkflowShortcut && (
                <div className="space-y-3 rounded-card border border-border bg-surface-2 p-3">
                  <div>
                    <h4 className="text-xs font-bold text-text">
                      {t("detail.dialog.workflow.title")}
                    </h4>
                    <p className="mt-0.5 text-[0.6875rem] text-muted">
                      {t("detail.dialog.workflow.description")}
                    </p>
                  </div>
                  {shortcutError && (
                    <ErrorBanner
                      message={shortcutError}
                      onDismiss={() => setShortcutError(null)}
                    />
                  )}
                  <div className="flex gap-2">
                    <Button
                      size="sm"
                      onClick={() => void runWorkflowShortcut("confirm")}
                      disabled={shortcutAction !== null}
                    >
                      {shortcutAction === "confirm"
                        ? t("detail.dialog.workflow.confirmBusy")
                        : t("detail.dialog.workflow.confirm")}
                    </Button>
                    {!cancelFormOpen && (
                      <Button
                        variant="outline"
                        size="sm"
                        className="text-[var(--danger)] hover:border-[var(--danger)] hover:text-[var(--danger)]"
                        onClick={() => setCancelFormOpen(true)}
                        disabled={shortcutAction !== null}
                      >
                        {t("detail.dialog.workflow.cancel")}
                      </Button>
                    )}
                  </div>
                  {cancelFormOpen && (
                    <div className="space-y-2 border-t border-border pt-3">
                      <div className="space-y-1.5">
                        <Label htmlFor="workflow-cancel-leaves">
                          {t("execReport.dialog.leavesQty")}
                        </Label>
                        <ClearableInput
                          id="workflow-cancel-leaves"
                          value={cancelLeavesQuantity}
                          onChange={(event) =>
                            setCancelLeavesQuantity(event.target.value)
                          }
                          placeholder={t(
                            "execReport.dialog.leavesQtyPlaceholder",
                          )}
                          disabled={shortcutAction !== null}
                          onClear={() => setCancelLeavesQuantity("")}
                          clearLabel={tc("filters.clearField")}
                        />
                        {!cancelLeavesValid && (
                          <p className="text-[0.6875rem] text-muted">
                            {t("execReport.dialog.leavesInvalid")}
                          </p>
                        )}
                      </div>
                      <Button
                        variant="outline"
                        size="sm"
                        className="text-[var(--danger)] hover:border-[var(--danger)] hover:text-[var(--danger)]"
                        onClick={() => void runWorkflowShortcut("cancel")}
                        disabled={shortcutAction !== null || !cancelLeavesValid}
                      >
                        {shortcutAction === "cancel"
                          ? t("detail.dialog.workflow.cancelBusy")
                          : t("detail.dialog.workflow.cancel")}
                      </Button>
                    </div>
                  )}
                </div>
              )}

              <DialogFooter>
                <Button
                  variant="outline"
                  size="sm"
                  className="sm:mr-auto"
                  onClick={() =>
                    onExecReport(
                      orderExternalId,
                      execReportInitialValuesFromOrder(
                        state.order,
                        state.events,
                      ),
                    )
                  }
                >
                  {t("detail.dialog.trades.submitExecReport")}
                </Button>
                {state.phase === "ready" && (
                  <Button
                    variant="outline"
                    size="sm"
                    aria-label={t("clone.orderAriaLabel", { orderExternalId })}
                    onClick={() => {
                      onCloneOrder({
                        account: state.order.account,
                        baseAsset: state.order.baseAsset,
                        quoteAsset: state.order.quoteAsset,
                        side: state.order.side,
                        amountKind: state.order.amountKind,
                        amountValue: state.order.amountValue,
                        price:
                          state.order.price === "0" ? "" : state.order.price,
                      });
                      onClose();
                    }}
                  >
                    <GitFork className="h-3.5 w-3.5" />
                    {t("clone.orderButton")}
                  </Button>
                )}
                <Button size="sm" onClick={onClose}>
                  {tc("actions.close")}
                </Button>
              </DialogFooter>
            </div>
          )}
        </DialogContent>
      </Dialog>

      <OrderVerificationPanel
        orderExternalId={
          state.phase === "ready" && verifyEventId !== null
            ? orderExternalId
            : null
        }
        eventId={verifyEventId}
        open={verifyEventId !== null}
        onOpenChange={(next) => {
          if (!next) {
            setVerifyEventId(null);
          }
        }}
      />
    </>
  );
}

// ---------------------------------------------------------------------------
// Orders table
// ---------------------------------------------------------------------------

interface OrdersTableProps {
  orders: Order[];
  activeSort?: string;
  activeOrder?: SortOrder;
  onSortChange: (sort: string, order: SortDirection) => void;
  onRowClick: (order: Order) => void;
  onFilterAccount: (account: string) => void;
  onFilterInstrument: (baseAsset: string, quoteAsset: string) => void;
  onClone: (values: OrderInitialValues) => void;
}

function OrdersTable({
  orders,
  activeSort,
  activeOrder,
  onSortChange,
  onRowClick,
  onFilterAccount,
  onFilterInstrument,
  onClone,
}: OrdersTableProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();
  const rowRefs = useRef<Array<HTMLTableRowElement | null>>([]);
  const [selectedIndex, setSelectedIndex] = useState(0);

  const focusRow = (index: number) => {
    rowRefs.current[index]?.focus();
  };

  const onRowKeyDown = (
    event: KeyboardEvent<HTMLTableRowElement>,
    index: number,
    order: Order,
  ) => {
    if (event.key === "Enter") {
      event.preventDefault();
      onRowClick(order);
      return;
    }
    if (event.key === "ArrowDown" || event.key === "j") {
      event.preventDefault();
      const next = Math.min(index + 1, orders.length - 1);
      setSelectedIndex(next);
      focusRow(next);
      return;
    }
    if (event.key === "ArrowUp" || event.key === "k") {
      event.preventDefault();
      const next = Math.max(index - 1, 0);
      setSelectedIndex(next);
      focusRow(next);
    }
  };

  // Amount label: "100 qty" or "500 vol".
  function amountLabel(kind: string, value: string): string {
    return kind === "quantity"
      ? t("amount.qty", { value })
      : t("amount.vol", { value });
  }

  // Price display: empty string or "0" → "market".
  function priceLabel(price: string): string {
    if (!price || price === "0") {
      return t("price.market");
    }
    return price;
  }

  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>
            <ColumnHeader
              description={t("table.columnDescriptions.externalId")}
            >
              {t("table.externalId")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <SortableHeader
              field="account"
              label={t("table.account")}
              description={t("table.columnDescriptions.account")}
              direction={sortDirection(activeSort, activeOrder, "account")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="baseAsset"
              label={t("table.instrument")}
              description={t("table.columnDescriptions.instrument")}
              direction={sortDirection(activeSort, activeOrder, "baseAsset")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead className="orders-side-cell">
            <SortableHeader
              field="side"
              label={t("table.side")}
              description={t("table.columnDescriptions.side")}
              direction={sortDirection(activeSort, activeOrder, "side")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="amountValue"
              label={t("table.amount")}
              description={t("table.columnDescriptions.amount")}
              direction={sortDirection(activeSort, activeOrder, "amountValue")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="price"
              label={t("table.price")}
              description={t("table.columnDescriptions.price")}
              direction={sortDirection(activeSort, activeOrder, "price")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <ColumnHeader
              description={t("table.columnDescriptions.displayPrice")}
            >
              {t("table.displayPrice")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader
              description={t("table.columnDescriptions.commission")}
            >
              {t("table.commission")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="w-[var(--orders-status-column-width)]">
            <SortableHeader
              field="status"
              label={t("table.status")}
              description={t("table.columnDescriptions.status")}
              direction={sortDirection(activeSort, activeOrder, "status")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="source"
              label={t("table.source")}
              description={t("table.columnDescriptions.source")}
              direction={sortDirection(activeSort, activeOrder, "source")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="at"
              label={t("table.time")}
              description={t("table.columnDescriptions.time")}
              direction={sortDirection(activeSort, activeOrder, "at")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead className="text-right" />
        </TableRow>
      </TableHeader>
      <TableBody>
        {orders.map((order, index) => (
          <TableRow
            key={order.id}
            ref={(node) => {
              rowRefs.current[index] = node;
            }}
            tabIndex={0}
            className={cn(
              "cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              selectedIndex === index && "ring-1 ring-inset ring-ring",
            )}
            onClick={() => onRowClick(order)}
            onFocus={() => setSelectedIndex(index)}
            onKeyDown={(event) => onRowKeyDown(event, index, order)}
          >
            <TableCell className="text-muted-lt">
              <div className="flex min-w-0 items-center gap-1">
                <IdCell
                  value={order.id}
                  copyTitle={t("common:rowActions.copyId")}
                  copiedTitle={t("common:rowActions.copiedId")}
                />
                {order.signed && (
                  <span
                    className="shrink-0"
                    role="img"
                    aria-label={t("table.signedIndicator")}
                    title={t("table.signedIndicator")}
                  >
                    <KeyRound className="h-3.5 w-3.5 text-muted-lt" />
                  </span>
                )}
                {order.dropCopy && (
                  <Badge variant="danger" className="shrink-0">
                    {t("dropCopy.badge")}
                  </Badge>
                )}
              </div>
            </TableCell>
            <TableCell className="nums text-xs">
              <div className="flex min-w-0 items-center gap-1">
                <IdCell
                  value={order.account}
                  copyTitle={t("common:rowActions.copyId")}
                  copiedTitle={t("common:rowActions.copiedId")}
                />
                <span className="ml-auto flex shrink-0 items-center">
                  <FilterByButton
                    size={28}
                    title={tc("rowActions.filterByTitle", {
                      field: order.account,
                    })}
                    href={ordersFilterHref({ account: order.account })}
                    onClick={() => onFilterAccount(order.account)}
                  />
                </span>
              </div>
            </TableCell>
            <TableCell className="text-xs">
              <div className="flex min-w-0 items-center gap-1">
                <span className="min-w-0 truncate">
                  {instrument(order.baseAsset, order.quoteAsset)}
                </span>
                <span className="ml-auto flex shrink-0 items-center">
                  <FilterByButton
                    size={28}
                    title={tc("rowActions.filterByTitle", {
                      field: instrument(order.baseAsset, order.quoteAsset),
                    })}
                    href={ordersFilterHref({
                      baseAsset: order.baseAsset,
                      quoteAsset: order.quoteAsset,
                    })}
                    onClick={() =>
                      onFilterInstrument(order.baseAsset, order.quoteAsset)
                    }
                  />
                </span>
              </div>
            </TableCell>
            <TableCell className="orders-side-cell">
              <Badge variant={order.side === "buy" ? "buy" : "sell"}>
                {order.side}
              </Badge>
            </TableCell>
            <TableCell className="nums text-xs">
              {amountLabel(order.amountKind, order.amountValue)}
            </TableCell>
            <TableCell className="nums text-xs">
              {priceLabel(order.price)}
            </TableCell>
            <TableCell className="nums text-xs text-muted-lt">
              {order.displayPrice !== ""
                ? order.displayPrice
                : tc("value.none")}
            </TableCell>
            <TableCell className="nums text-xs text-muted-lt">
              {commissionLabel(order.commissionSubtotals) || tc("value.none")}
            </TableCell>
            <TableCell className="w-[var(--orders-status-column-width)]">
              <Badge variant={statusVariant(order.status)}>
                {order.status}
              </Badge>
            </TableCell>
            <TableCell>
              <Badge variant={sourceVariant(order.source)}>
                {order.source}
              </Badge>
            </TableCell>
            <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
              {formatDateTime(order.at)}
            </TableCell>
            <TableCell
              className="text-right"
              onClick={(event) => event.stopPropagation()}
            >
              <RowActions>
                <ViewEntityButton
                  title={tc("rowActions.viewTitle", {
                    entity: order.id,
                  })}
                  href={ordersFilterHref({ order: order.id })}
                  onClick={() => onRowClick(order)}
                />
                <CloneButton
                  title={t("clone.orderAriaLabel", {
                    orderExternalId: order.id,
                  })}
                  onClick={() =>
                    onClone({
                      account: order.account,
                      baseAsset: order.baseAsset,
                      quoteAsset: order.quoteAsset,
                      side: order.side,
                      amountKind: order.amountKind,
                      amountValue: order.amountValue,
                      price: order.price === "0" ? "" : order.price,
                    })
                  }
                />
              </RowActions>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Trades table
// ---------------------------------------------------------------------------

interface TradesTableProps {
  trades: Trade[];
  activeSort?: string;
  activeOrder?: SortOrder;
  onSortChange: (sort: string, order: SortDirection) => void;
  onOrderClick: (orderExternalId: string) => void;
  onFilterAccount: (account: string) => void;
  onFilterInstrument: (baseAsset: string, quoteAsset: string) => void;
  onCloneExecReport: (
    orderExternalId: string,
    values: ExecReportInitialValues,
  ) => void;
}

function TradesTable({
  trades,
  activeSort,
  activeOrder,
  onSortChange,
  onOrderClick,
  onFilterAccount,
  onFilterInstrument,
  onCloneExecReport,
}: TradesTableProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();
  const openInNewTabHint = useOpenInNewTabHint();
  const rowRefs = useRef<Array<HTMLTableRowElement | null>>([]);
  const [selectedIndex, setSelectedIndex] = useState(0);

  const focusRow = (index: number) => {
    rowRefs.current[index]?.focus();
  };

  const onRowKeyDown = (
    event: KeyboardEvent<HTMLTableRowElement>,
    index: number,
    trade: Trade,
  ) => {
    if (event.key === "Enter") {
      event.preventDefault();
      onOrderClick(trade.order);
      return;
    }
    if (event.key === "ArrowDown" || event.key === "j") {
      event.preventDefault();
      const next = Math.min(index + 1, trades.length - 1);
      setSelectedIndex(next);
      focusRow(next);
      return;
    }
    if (event.key === "ArrowUp" || event.key === "k") {
      event.preventDefault();
      const next = Math.max(index - 1, 0);
      setSelectedIndex(next);
      focusRow(next);
    }
  };

  const onOrderLinkClick = (
    event: MouseEvent<HTMLAnchorElement>,
    orderExternalId: string,
  ) => {
    if (event.metaKey || event.ctrlKey) {
      return;
    }
    event.preventDefault();
    onOrderClick(orderExternalId);
  };

  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>
            <ColumnHeader
              description={t("table.columnDescriptions.externalId")}
            >
              {t("table.externalId")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.order")}>
              {t("table.order")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <SortableHeader
              field="account"
              label={t("table.account")}
              description={t("table.columnDescriptions.account")}
              direction={sortDirection(activeSort, activeOrder, "account")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="baseAsset"
              label={t("table.instrument")}
              description={t("table.columnDescriptions.instrument")}
              direction={sortDirection(activeSort, activeOrder, "baseAsset")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead className="trades-side-cell">
            <SortableHeader
              field="side"
              label={t("table.side")}
              description={t("table.columnDescriptions.side")}
              direction={sortDirection(activeSort, activeOrder, "side")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="quantity"
              label={t("table.qty")}
              description={t("table.columnDescriptions.qty")}
              direction={sortDirection(activeSort, activeOrder, "quantity")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="price"
              label={t("table.price")}
              description={t("table.columnDescriptions.price")}
              direction={sortDirection(activeSort, activeOrder, "price")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="lockPrice"
              label={t("table.lockPrice")}
              description={t("table.columnDescriptions.lockPrice")}
              direction={sortDirection(activeSort, activeOrder, "lockPrice")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <ColumnHeader
              description={t("table.columnDescriptions.commission")}
            >
              {t("table.commission")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <SortableHeader
              field="source"
              label={t("table.source")}
              description={t("table.columnDescriptions.source")}
              direction={sortDirection(activeSort, activeOrder, "source")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="at"
              label={t("table.time")}
              description={t("table.columnDescriptions.time")}
              direction={sortDirection(activeSort, activeOrder, "at")}
              onSort={onSortChange}
            />
          </TableHead>
          <TableHead className="text-right" />
        </TableRow>
      </TableHeader>
      <TableBody>
        {trades.map((trade, index) => (
          <TableRow
            key={trade.id}
            ref={(node) => {
              rowRefs.current[index] = node;
            }}
            tabIndex={0}
            className={cn(
              "hover:bg-transparent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              selectedIndex === index && "ring-1 ring-inset ring-ring",
            )}
            onFocus={() => setSelectedIndex(index)}
            onKeyDown={(event) => onRowKeyDown(event, index, trade)}
          >
            <TableCell className="text-muted-lt">
              <IdCell
                value={trade.id}
                copyTitle={t("common:rowActions.copyId")}
                copiedTitle={t("common:rowActions.copiedId")}
              />
            </TableCell>
            <TableCell>
              <IdCell
                value={trade.order}
                copyTitle={t("common:rowActions.copyId")}
                copiedTitle={t("common:rowActions.copiedId")}
              >
                <a
                  className="nums text-xs text-accent underline-offset-2 hover:underline"
                  href={ordersFilterHref({ order: trade.order })}
                  title={`${tc("rowActions.viewTitle", {
                    entity: trade.order,
                  })}\n${openInNewTabHint}`}
                  onClick={(event) => onOrderLinkClick(event, trade.order)}
                >
                  {trade.order}
                </a>
              </IdCell>
            </TableCell>
            <TableCell className="nums text-xs">
              <div className="flex min-w-0 items-center gap-1">
                <IdCell
                  value={trade.account}
                  copyTitle={t("common:rowActions.copyId")}
                  copiedTitle={t("common:rowActions.copiedId")}
                />
                <span className="ml-auto flex shrink-0 items-center">
                  <FilterByButton
                    size={28}
                    title={tc("rowActions.filterByTitle", {
                      field: trade.account,
                    })}
                    href={ordersFilterHref({
                      tab: "trades",
                      account: trade.account,
                    })}
                    onClick={() => onFilterAccount(trade.account)}
                  />
                </span>
              </div>
            </TableCell>
            <TableCell className="text-xs">
              <div className="flex min-w-0 items-center gap-1">
                <IdCell
                  value={instrument(trade.baseAsset, trade.quoteAsset)}
                  copyTitle={t("common:rowActions.copyId")}
                  copiedTitle={t("common:rowActions.copiedId")}
                />
                <span className="ml-auto flex shrink-0 items-center">
                  <FilterByButton
                    size={28}
                    title={tc("rowActions.filterByTitle", {
                      field: instrument(trade.baseAsset, trade.quoteAsset),
                    })}
                    href={ordersFilterHref({
                      tab: "trades",
                      baseAsset: trade.baseAsset,
                      quoteAsset: trade.quoteAsset,
                    })}
                    onClick={() =>
                      onFilterInstrument(trade.baseAsset, trade.quoteAsset)
                    }
                  />
                </span>
              </div>
            </TableCell>
            <TableCell className="trades-side-cell">
              <Badge variant={trade.side === "buy" ? "buy" : "sell"}>
                {trade.side}
              </Badge>
            </TableCell>
            <TableCell className="nums text-xs">{trade.quantity}</TableCell>
            <TableCell className="nums text-xs">{trade.price}</TableCell>
            <TableCell className="nums text-xs text-muted-lt">
              {trade.lockPrice || tc("value.none")}
            </TableCell>
            <TableCell className="nums text-xs text-muted-lt">
              {tradeCommissionLabel(trade) || tc("value.none")}
            </TableCell>
            <TableCell>
              <Badge variant={sourceVariant(trade.source)}>
                {trade.source}
              </Badge>
            </TableCell>
            <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
              {formatDateTime(trade.at)}
            </TableCell>
            <TableCell className="text-right">
              <RowActions>
                <CloneButton
                  title={t("clone.execReportAriaLabel", {
                    tradeId: trade.id,
                  })}
                  onClick={() =>
                    onCloneExecReport(trade.order, {
                      quantity: trade.quantity,
                      price: trade.price,
                      lockPrice: trade.lockPrice,
                      commission: trade.commission,
                    })
                  }
                />
              </RowActions>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Filter bar
// ---------------------------------------------------------------------------

const SOURCES = ["", "panel", "api", "mcp", "system"] as const;
const RANGE_MODES: RangeFilterMode[] = [
  "all",
  "greater_than",
  "less_than",
  "between",
];
const NUMBER_FILTER_MODES: RangeFilterMode[] = [
  "eq",
  "neq",
  "gt",
  "lt",
  "gte",
  "lte",
  "between",
];
const TIME_FILTER_MODES: RangeFilterMode[] = ["after", "before", "between"];
function normalizeSourceFilter(value: string): Source | undefined {
  const trimmed = value.trim();
  if (trimmed === "" || trimmed === "_all") {
    return undefined;
  }
  return trimmed as Source;
}

function rangeModeFromParams(
  params: URLSearchParams,
  key: string,
  modes: RangeFilterMode[] = RANGE_MODES,
  fallback: RangeFilterMode = "all",
): RangeFilterMode {
  const value = params.get(key);
  return modes.includes(value as RangeFilterMode)
    ? (value as RangeFilterMode)
    : fallback;
}

function trimmedOrUndefined(value: string): string | undefined {
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

function appendShareParam(
  query: URLSearchParams,
  key: string,
  value: string | undefined,
  defaultValue = "",
) {
  if (value !== undefined && value !== "" && value !== defaultValue) {
    query.set(key, value);
  }
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  const tag = target.tagName.toLowerCase();
  return (
    target.isContentEditable ||
    tag === "input" ||
    tag === "select" ||
    tag === "textarea" ||
    tag === "button"
  );
}

function dateTimeFilter(value: string): string | undefined {
  const trimmed = value.trim();
  if (trimmed === "") {
    return undefined;
  }
  const date = new Date(trimmed);
  if (Number.isNaN(date.getTime())) {
    return undefined;
  }
  return date.toISOString();
}

function addAmountRange(
  filter: OrderListFilters,
  mode: RangeFilterMode,
  min: string,
  max: string,
) {
  if (mode === "greater_than" && min !== "") {
    filter.amountMode = mode;
    filter.amountMin = min;
  } else if (
    (mode === "lt" || mode === "lte" || mode === "less_than") &&
    min !== ""
  ) {
    filter.amountMode = mode;
    filter.amountMax = min;
  } else if (mode === "between" && min !== "" && max !== "") {
    filter.amountMode = mode;
    filter.amountMin = min;
    filter.amountMax = max;
  }
}

function addPriceRange(
  filter: OrderListFilters,
  mode: RangeFilterMode,
  min: string,
  max: string,
) {
  if (mode === "greater_than" && min !== "") {
    filter.priceMode = mode;
    filter.priceMin = min;
  } else if (
    (mode === "lt" || mode === "lte" || mode === "less_than") &&
    min !== ""
  ) {
    filter.priceMode = mode;
    filter.priceMax = min;
  } else if (mode === "between" && min !== "" && max !== "") {
    filter.priceMode = mode;
    filter.priceMin = min;
    filter.priceMax = max;
  }
}

function addAtRange(
  filter: OrderListFilters,
  mode: RangeFilterMode,
  min: string | undefined,
  max: string | undefined,
) {
  if (mode === "greater_than" && min !== undefined) {
    filter.atMode = mode;
    filter.atMin = min;
  } else if (
    (mode === "lt" ||
      mode === "lte" ||
      mode === "less_than" ||
      mode === "before") &&
    min !== undefined
  ) {
    filter.atMode = mode;
    filter.atMax = min;
  } else if (mode === "between" && min !== undefined && max !== undefined) {
    filter.atMode = mode;
    filter.atMin = min;
    filter.atMax = max;
  }
}

function RangeFilterControls({
  label,
  mode,
  min,
  max,
  inputType = "text",
  onMode,
  onMin,
  onMax,
}: {
  label: string;
  mode: RangeFilterMode;
  min: string;
  max: string;
  inputType?: "text" | "datetime-local";
  onMode: (value: RangeFilterMode) => void;
  onMin: (value: string) => void;
  onMax: (value: string) => void;
}) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation("common");
  const activeMode: RangeFilterMode = mode === "all" ? "greater_than" : mode;
  const rangeOperators = [
    {
      value: "greater_than",
      label: t("filter.range.greater_than"),
      sign: ">",
    },
    { value: "less_than", label: t("filter.range.less_than"), sign: "<" },
    { value: "between", label: t("filter.range.between") },
  ];
  const firstValue = activeMode === "less_than" ? max : min;
  const secondValue = activeMode === "between" ? max : "";
  const updateMode = (next: string) => {
    const nextMode = next as RangeFilterMode;
    onMode(nextMode);
    if (nextMode === "greater_than") {
      onMax("");
    } else if (nextMode === "less_than") {
      onMin("");
    }
  };
  const updateFirst = (value: string) => {
    if (mode === "all") {
      onMode(activeMode);
    }
    if (activeMode === "less_than") {
      onMax(value);
    } else {
      onMin(value);
    }
  };
  const updateSecond = (value: string) => {
    if (mode === "all") {
      onMode(activeMode);
    }
    onMax(value);
  };
  return (
    <div className="grid gap-1">
      <FieldLabel>{label}</FieldLabel>
      {inputType === "datetime-local" ? (
        <TimeRangeFilter
          operator={activeMode}
          operators={rangeOperators}
          operatorAriaLabel={label}
          from={firstValue}
          to={secondValue}
          fluid
          showPresets={false}
          clearLabel={tc("filters.clearField")}
          onOperatorChange={updateMode}
          onFromChange={updateFirst}
          onToChange={updateSecond}
        />
      ) : (
        <NumberRangeFilter
          operator={activeMode}
          operators={rangeOperators}
          operatorAriaLabel={label}
          min={firstValue}
          max={secondValue}
          fluid
          clearLabel={tc("filters.clearField")}
          onOperatorChange={updateMode}
          onMinChange={updateFirst}
          onMaxChange={updateSecond}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

type TabId = "orders" | "trades";

type OrderAdvancedFilterDraft = {
  amountMode: RangeFilterMode;
  amountMin: string;
  amountMax: string;
  priceMode: RangeFilterMode;
  priceMin: string;
  priceMax: string;
  atMode: RangeFilterMode;
  atMin: string;
  atMax: string;
};

type TradeAdvancedFilterDraft = {
  atMode: RangeFilterMode;
  atMin: string;
  atMax: string;
  quantityMode: RangeFilterMode;
  quantityMin: string;
  quantityMax: string;
  priceMode: RangeFilterMode;
  priceMin: string;
  priceMax: string;
  lockPriceMode: RangeFilterMode;
  lockPriceMin: string;
  lockPriceMax: string;
};

function rangeFilterControlsValid(
  mode: RangeFilterMode,
  min: string,
  max: string,
): boolean {
  const activeMode = mode === "all" ? "greater_than" : mode;
  const firstValue = activeMode === "less_than" ? max : min;
  const secondValue = activeMode === "between" ? max : "";
  return isDecimalRangeValid(activeMode, firstValue, secondValue);
}

/** The advanced-dialog status filter: checkboxes grouped by lifecycle phase,
 *  plus per-group quick selects and a reset. Multi-select over a status set;
 *  an empty selection means no status filter. */
function OrderStatusFilterGroup({
  selected,
  onChange,
}: {
  selected: OrderStatusValue[];
  onChange: (next: OrderStatusValue[]) => void;
}) {
  const { t } = useTranslation("orders");

  const toggle = (status: OrderStatusValue, checked: boolean) => {
    const next = new Set(selected);
    if (checked) {
      next.add(status);
    } else {
      next.delete(status);
    }
    onChange(ALL_ORDER_STATUSES.filter((value) => next.has(value)));
  };

  const renderGroup = (title: string, group: readonly OrderStatusValue[]) => {
    const nextWithoutGroup = selected.filter(
      (status) => !group.includes(status),
    );
    const nextWithGroup = ALL_ORDER_STATUSES.filter(
      (status) => selected.includes(status) || group.includes(status),
    );
    return (
      <div className="grid gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <FieldLabel>{title}</FieldLabel>
          <div className="flex flex-wrap gap-1">
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-5 px-1.5 text-[0.625rem]"
              onClick={() => onChange(nextWithGroup)}
            >
              {t("filter.status.selectAll")}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-5 px-1.5 text-[0.625rem]"
              onClick={() => onChange(nextWithoutGroup)}
            >
              {t("filter.status.clearAll")}
            </Button>
          </div>
        </div>
        <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-3">
          {group.map((status) => (
            <label
              key={status}
              className="flex cursor-pointer select-none items-center gap-2 text-xs text-text"
            >
              <input
                type="checkbox"
                checked={selected.includes(status)}
                onChange={(e) => toggle(status, e.target.checked)}
                className="accent-[var(--accent)]"
              />
              {t(`filter.status.${status}`)}
            </label>
          ))}
        </div>
      </div>
    );
  };

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <FieldLabel>{t("filter.statusLabel")}</FieldLabel>
        <div className="flex flex-wrap gap-1">
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-5 px-1.5 text-[0.625rem]"
            onClick={() => onChange([...ALL_ORDER_STATUSES])}
          >
            {t("filter.status.selectAll")}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-5 px-1.5 text-[0.625rem]"
            onClick={() => onChange([])}
          >
            {t("filter.status.clearAll")}
          </Button>
        </div>
      </div>
      {renderGroup(t("filter.status.groupActive"), ACTIVE_ORDER_STATUSES)}
      {renderGroup(t("filter.status.groupFinalized"), TERMINAL_ORDER_STATUSES)}
    </div>
  );
}

export function Orders() {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation("common");
  const { fetchAccounts, fetchAssets, fetchOrderDetail } = useOfficerApi();
  const filterRegionRef = useRef<HTMLDivElement | null>(null);
  const advancedFilterDialogRef = useRef<HTMLDivElement | null>(null);
  const openedTradeExternalIdRef = useRef<string | null>(null);

  const [params] = useSearchParams();
  const globalAccountFilter = useGlobalAccountFilter();
  // A deep link from the Assets screen lands on the trades tab when asked
  // (?tab=trades) and seeds the matching base-asset filter on both tabs.
  const [tab, setTab] = useState<TabId>(
    params.get("tab") === "trades" ? "trades" : "orders",
  );
  const initialBaseAsset = params.get("baseAsset") ?? "";
  const initialAccount =
    globalAccountFilter.account || params.get("account") || "";

  // Filter state - seeded from URL on mount
  const [orderAccount, setOrderAccount] = useState(initialAccount);
  const [orderAccountDraft, setOrderAccountDraft] = useState(initialAccount);
  const [orderSource, setOrderSource] = useState(params.get("source") ?? "");
  const [orderSide, setOrderSide] = useState<"all" | OrderSide>(
    params.get("side") === "buy" || params.get("side") === "sell"
      ? (params.get("side") as OrderSide)
      : "all",
  );
  // The applied status filter is a set; empty means "all statuses". The quick
  // buttons and the advanced checkbox group are shorthands over this one set.
  const [orderStatuses, setOrderStatuses] = useState<OrderStatusValue[]>(() =>
    parseStatusSet(params.get("status")),
  );
  const [orderStatusesDraft, setOrderStatusesDraft] = useState<
    OrderStatusValue[]
  >(() => parseStatusSet(params.get("status")));
  const [orderBaseAsset, setOrderBaseAsset] = useState(initialBaseAsset);
  const [orderBaseAssetDraft, setOrderBaseAssetDraft] =
    useState(initialBaseAsset);
  const [orderQuoteAsset, setOrderQuoteAsset] = useState(
    params.get("quoteAsset") ?? "",
  );
  const [orderQuoteAssetDraft, setOrderQuoteAssetDraft] = useState(
    params.get("quoteAsset") ?? "",
  );
  const [orderAmountMode, setOrderAmountMode] = useState<RangeFilterMode>(
    rangeModeFromParams(params, "amountMode"),
  );
  const [orderAmountMin, setOrderAmountMin] = useState(
    params.get("amountMin") ?? "",
  );
  const [orderAmountMax, setOrderAmountMax] = useState(
    params.get("amountMax") ?? "",
  );
  const [orderPriceMode, setOrderPriceMode] = useState<RangeFilterMode>(
    rangeModeFromParams(params, "priceMode"),
  );
  const [orderPriceMin, setOrderPriceMin] = useState(
    params.get("priceMin") ?? "",
  );
  const [orderPriceMax, setOrderPriceMax] = useState(
    params.get("priceMax") ?? "",
  );
  const [orderAtMode, setOrderAtMode] = useState<RangeFilterMode>(
    rangeModeFromParams(params, "atMode"),
  );
  const [orderAtMin, setOrderAtMin] = useState(params.get("atMin") ?? "");
  const [orderAtMax, setOrderAtMax] = useState(params.get("atMax") ?? "");
  const [orderSize, setOrderSize] = usePersistentPageSize(
    "pit-officer-orders-page-size",
  );
  const [orderPage, setOrderPage] = useState(0);
  const [orderSort, setOrderSort] = useState<{
    sort?: string;
    order?: SortOrder;
  }>({ sort: "at", order: "desc" });

  const [tradeAccount, setTradeAccount] = useState(initialAccount);
  const [tradeAccountDraft, setTradeAccountDraft] = useState(initialAccount);
  const [tradeExternalId, setTradeExternalId] = useState(
    params.get("id") ?? "",
  );
  const [appliedTradeExternalId, setAppliedTradeExternalId] = useState(
    params.get("id") ?? "",
  );
  const [tradeSource, setTradeSource] = useState(params.get("source") ?? "");
  const [tradeSide, setTradeSide] = useState<"all" | OrderSide>(
    params.get("side") === "buy" || params.get("side") === "sell"
      ? (params.get("side") as OrderSide)
      : "all",
  );
  const [tradeBaseAsset, setTradeBaseAsset] = useState(initialBaseAsset);
  const [tradeBaseAssetDraft, setTradeBaseAssetDraft] =
    useState(initialBaseAsset);
  const [tradeQuoteAsset, setTradeQuoteAsset] = useState(
    params.get("quoteAsset") ?? "",
  );
  const [tradeQuoteAssetDraft, setTradeQuoteAssetDraft] = useState(
    params.get("quoteAsset") ?? "",
  );
  const [tradeAtMode, setTradeAtMode] = useState<RangeFilterMode>(
    rangeModeFromParams(params, "atMode", TIME_FILTER_MODES, "after"),
  );
  const [tradeAtMin, setTradeAtMin] = useState(params.get("atMin") ?? "");
  const [tradeAtMax, setTradeAtMax] = useState(params.get("atMax") ?? "");
  const [tradeQuantityMode, setTradeQuantityMode] = useState<RangeFilterMode>(
    rangeModeFromParams(params, "quantityMode", NUMBER_FILTER_MODES, "eq"),
  );
  const [tradeQuantityMin, setTradeQuantityMin] = useState(
    params.get("quantityMin") ?? "",
  );
  const [tradeQuantityMax, setTradeQuantityMax] = useState(
    params.get("quantityMax") ?? "",
  );
  const [tradePriceMode, setTradePriceMode] = useState<RangeFilterMode>(
    rangeModeFromParams(params, "priceMode", NUMBER_FILTER_MODES, "eq"),
  );
  const [tradePriceMin, setTradePriceMin] = useState(
    params.get("priceMin") ?? "",
  );
  const [tradePriceMax, setTradePriceMax] = useState(
    params.get("priceMax") ?? "",
  );
  const [tradeLockPriceMode, setTradeLockPriceMode] = useState<RangeFilterMode>(
    rangeModeFromParams(params, "lockPriceMode", NUMBER_FILTER_MODES, "eq"),
  );
  const [tradeLockPriceMin, setTradeLockPriceMin] = useState(
    params.get("lockPriceMin") ?? "",
  );
  const [tradeLockPriceMax, setTradeLockPriceMax] = useState(
    params.get("lockPriceMax") ?? "",
  );
  const [tradeSize, setTradeSize] = usePersistentPageSize(
    "pit-officer-trades-page-size",
  );
  const [tradePage, setTradePage] = useState(0);
  const [tradeSort, setTradeSort] = useState<{
    sort?: string;
    order?: SortOrder;
  }>({ sort: "at", order: "desc" });
  const [moreFiltersOpen, setMoreFiltersOpen] = useState(false);
  const [orderAdvancedDraft, setOrderAdvancedDraft] =
    useState<OrderAdvancedFilterDraft>({
      amountMode: orderAmountMode,
      amountMin: orderAmountMin,
      amountMax: orderAmountMax,
      priceMode: orderPriceMode,
      priceMin: orderPriceMin,
      priceMax: orderPriceMax,
      atMode: orderAtMode,
      atMin: orderAtMin,
      atMax: orderAtMax,
    });
  const [tradeAdvancedDraft, setTradeAdvancedDraft] =
    useState<TradeAdvancedFilterDraft>({
      atMode: tradeAtMode,
      atMin: tradeAtMin,
      atMax: tradeAtMax,
      quantityMode: tradeQuantityMode,
      quantityMin: tradeQuantityMin,
      quantityMax: tradeQuantityMax,
      priceMode: tradePriceMode,
      priceMin: tradePriceMin,
      priceMax: tradePriceMax,
      lockPriceMode: tradeLockPriceMode,
      lockPriceMin: tradeLockPriceMin,
      lockPriceMax: tradeLockPriceMax,
    });
  const debouncedOrderAccountDraft = useDebouncedValue(
    orderAccountDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeAccountDraft = useDebouncedValue(
    tradeAccountDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeBaseAssetDraft = useDebouncedValue(
    tradeBaseAssetDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeQuoteAssetDraft = useDebouncedValue(
    tradeQuoteAssetDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeAtMin = useDebouncedValue(
    tradeAtMin,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeAtMax = useDebouncedValue(
    tradeAtMax,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeQuantityMin = useDebouncedValue(
    tradeQuantityMin.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeQuantityMax = useDebouncedValue(
    tradeQuantityMax.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradePriceMin = useDebouncedValue(
    tradePriceMin.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradePriceMax = useDebouncedValue(
    tradePriceMax.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeLockPriceMin = useDebouncedValue(
    tradeLockPriceMin.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedTradeLockPriceMax = useDebouncedValue(
    tradeLockPriceMax.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedOrderBaseAssetDraft = useDebouncedValue(
    orderBaseAssetDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedOrderQuoteAssetDraft = useDebouncedValue(
    orderQuoteAssetDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedOrderAmountMin = useDebouncedValue(
    orderAmountMin.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedOrderAmountMax = useDebouncedValue(
    orderAmountMax.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedOrderPriceMin = useDebouncedValue(
    orderPriceMin.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedOrderPriceMax = useDebouncedValue(
    orderPriceMax.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedOrderAtMin = useDebouncedValue(
    orderAtMin,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedOrderAtMax = useDebouncedValue(
    orderAtMax,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const normalizedOrderSource = normalizeSourceFilter(orderSource);
  const normalizedTradeSource = normalizeSourceFilter(tradeSource);

  function resetTradePage() {
    setTradePage(0);
  }

  useEffect(() => {
    const account = globalAccountFilter.account;
    if (account === "") {
      return;
    }
    // Mirror the external global-account store into both page-local account
    // filters so tab switches keep the global account applied.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setOrderAccount(account);
    setOrderAccountDraft(account);
    setTradeAccount(account);
    setTradeAccountDraft(account);
    setOrderPage(0);
    resetTradePage();
  }, [globalAccountFilter.account]);

  function syncAdvancedDrafts() {
    setOrderStatusesDraft(
      orderStatuses.length === 0 ? [...ALL_ORDER_STATUSES] : orderStatuses,
    );
    setOrderAdvancedDraft({
      amountMode: orderAmountMode,
      amountMin: orderAmountMin,
      amountMax: orderAmountMax,
      priceMode: orderPriceMode,
      priceMin: orderPriceMin,
      priceMax: orderPriceMax,
      atMode: orderAtMode,
      atMin: orderAtMin,
      atMax: orderAtMax,
    });
    setTradeAdvancedDraft({
      atMode: tradeAtMode,
      atMin: tradeAtMin,
      atMax: tradeAtMax,
      quantityMode: tradeQuantityMode,
      quantityMin: tradeQuantityMin,
      quantityMax: tradeQuantityMax,
      priceMode: tradePriceMode,
      priceMin: tradePriceMin,
      priceMax: tradePriceMax,
      lockPriceMode: tradeLockPriceMode,
      lockPriceMin: tradeLockPriceMin,
      lockPriceMax: tradeLockPriceMax,
    });
  }

  function applyAdvancedFilters() {
    if (!reportInvalidFilterControls(advancedFilterDialogRef.current)) {
      return;
    }
    if (tab === "orders") {
      if (orderStatusesDraft.length === 0) {
        return;
      }
      const nextStatuses = normalizeOrderStatusSet(orderStatusesDraft);
      setOrderStatuses(nextStatuses);
      setOrderStatusesDraft(nextStatuses);
      setOrderAmountMode(orderAdvancedDraft.amountMode);
      setOrderAmountMin(orderAdvancedDraft.amountMin);
      setOrderAmountMax(orderAdvancedDraft.amountMax);
      setOrderPriceMode(orderAdvancedDraft.priceMode);
      setOrderPriceMin(orderAdvancedDraft.priceMin);
      setOrderPriceMax(orderAdvancedDraft.priceMax);
      setOrderAtMode(orderAdvancedDraft.atMode);
      setOrderAtMin(orderAdvancedDraft.atMin);
      setOrderAtMax(orderAdvancedDraft.atMax);
      setOrderPage(0);
    } else {
      setTradeAtMode(tradeAdvancedDraft.atMode);
      setTradeAtMin(tradeAdvancedDraft.atMin);
      setTradeAtMax(tradeAdvancedDraft.atMax);
      setTradeQuantityMode(tradeAdvancedDraft.quantityMode);
      setTradeQuantityMin(tradeAdvancedDraft.quantityMin);
      setTradeQuantityMax(tradeAdvancedDraft.quantityMax);
      setTradePriceMode(tradeAdvancedDraft.priceMode);
      setTradePriceMin(tradeAdvancedDraft.priceMin);
      setTradePriceMax(tradeAdvancedDraft.priceMax);
      setTradeLockPriceMode(tradeAdvancedDraft.lockPriceMode);
      setTradeLockPriceMin(tradeAdvancedDraft.lockPriceMin);
      setTradeLockPriceMax(tradeAdvancedDraft.lockPriceMax);
      resetTradePage();
    }
    setMoreFiltersOpen(false);
  }

  // Quick status filters are online shorthands over the one status set: "All"
  // clears it, "Active" sets it to exactly the active-orders group. They win
  // over any advanced selection, overwriting it to match.
  const statusIsAll = orderStatuses.length === 0;
  const statusIsActive = sameStatusSet(orderStatuses, ACTIVE_STATUS_SET);
  const orderAdvancedNumericValid =
    rangeFilterControlsValid(
      orderAdvancedDraft.amountMode,
      orderAdvancedDraft.amountMin,
      orderAdvancedDraft.amountMax,
    ) &&
    rangeFilterControlsValid(
      orderAdvancedDraft.priceMode,
      orderAdvancedDraft.priceMin,
      orderAdvancedDraft.priceMax,
    );
  const tradeAdvancedNumericValid =
    isDecimalRangeValid(
      tradeAdvancedDraft.quantityMode,
      tradeAdvancedDraft.quantityMin,
      tradeAdvancedDraft.quantityMax,
    ) &&
    isDecimalRangeValid(
      tradeAdvancedDraft.priceMode,
      tradeAdvancedDraft.priceMin,
      tradeAdvancedDraft.priceMax,
    ) &&
    isDecimalRangeValid(
      tradeAdvancedDraft.lockPriceMode,
      tradeAdvancedDraft.lockPriceMin,
      tradeAdvancedDraft.lockPriceMax,
    );

  function normalizeOrderStatusSet(
    statuses: readonly OrderStatusValue[],
  ): OrderStatusValue[] {
    if (sameStatusSet(statuses, ALL_ORDER_STATUSES)) {
      return [];
    }
    if (sameStatusSet(statuses, ACTIVE_STATUS_SET)) {
      return [...ACTIVE_STATUS_SET];
    }
    return ALL_ORDER_STATUSES.filter((status) => statuses.includes(status));
  }

  function applyAllStatuses() {
    setOrderStatuses([]);
    setOrderStatusesDraft([]);
    setOrderPage(0);
  }

  function applyActiveStatuses() {
    setOrderStatuses(ACTIVE_STATUS_SET);
    setOrderStatusesDraft(ACTIVE_STATUS_SET);
    setOrderPage(0);
  }

  const orderIdentityDraftChanged =
    orderAccountDraft.trim() !== orderAccount ||
    orderBaseAssetDraft.trim() !== orderBaseAsset ||
    orderQuoteAssetDraft.trim() !== orderQuoteAsset;
  const tradeIdentityDraftChanged =
    tradeAccountDraft.trim() !== tradeAccount ||
    tradeBaseAssetDraft.trim() !== tradeBaseAsset ||
    tradeQuoteAssetDraft.trim() !== tradeQuoteAsset;

  function applyOrderIdentityFilters() {
    setOrderAccount(orderAccountDraft.trim());
    setOrderBaseAsset(orderBaseAssetDraft.trim());
    setOrderQuoteAsset(orderQuoteAssetDraft.trim());
    setOrderPage(0);
  }

  function applyTradeIdentityFilters() {
    setTradeAccount(tradeAccountDraft.trim());
    setTradeBaseAsset(tradeBaseAssetDraft.trim());
    setTradeQuoteAsset(tradeQuoteAssetDraft.trim());
    resetTradePage();
  }

  function applyOrderIdentityField(
    field: "account" | "baseAsset" | "quoteAsset",
    value: string,
  ) {
    const nextValue = value.trim();
    if (field === "account") {
      setOrderAccountDraft(nextValue);
      setOrderAccount(nextValue);
    } else if (field === "baseAsset") {
      setOrderBaseAssetDraft(nextValue);
      setOrderBaseAsset(nextValue);
    } else {
      setOrderQuoteAssetDraft(nextValue);
      setOrderQuoteAsset(nextValue);
    }
    setOrderPage(0);
  }

  function applyTradeIdentityField(
    field: "account" | "baseAsset" | "quoteAsset",
    value: string,
  ) {
    const nextValue = value.trim();
    if (field === "account") {
      setTradeAccountDraft(nextValue);
      setTradeAccount(nextValue);
    } else if (field === "baseAsset") {
      setTradeBaseAssetDraft(nextValue);
      setTradeBaseAsset(nextValue);
    } else {
      setTradeQuoteAssetDraft(nextValue);
      setTradeQuoteAsset(nextValue);
    }
    resetTradePage();
  }

  function accountGlobalToggle(
    value: string,
    applyAccount: (account: string) => void,
  ) {
    const nextAccount = value.trim();
    return {
      active: nextAccount !== "" && nextAccount === globalAccountFilter.account,
      disabled: nextAccount === "",
      activeLabel: tc("filters.globalAccount.active"),
      inactiveLabel: tc("filters.globalAccount.inactive"),
      disabledLabel: tc("filters.globalAccount.disabled"),
      onToggle: () => {
        if (nextAccount === "") {
          return;
        }
        if (globalAccountFilter.account === nextAccount) {
          globalAccountFilter.clear();
          return;
        }
        globalAccountFilter.setAccount(nextAccount);
        applyAccount(nextAccount);
      },
    };
  }

  function applyOrderIdentityFiltersOnEnter(
    event: KeyboardEvent<HTMLInputElement>,
  ) {
    if (event.key === "Enter") {
      applyOrderIdentityFilters();
    }
  }

  function applyTradeIdentityFiltersOnEnter(
    event: KeyboardEvent<HTMLInputElement>,
  ) {
    if (event.key === "Enter") {
      applyTradeIdentityFilters();
    }
  }

  // Data
  const orderListFilters = useMemo<OrderListFilters>(() => {
    const filter: OrderListFilters = {
      account: orderAccount.trim() || undefined,
      source: normalizedOrderSource,
      limit: orderSize,
      offset: orderPage * orderSize,
      sort: orderSort.sort,
      order: orderSort.order,
    };
    if (orderSide !== "all") {
      filter.side = orderSide;
    }
    if (orderStatuses.length > 0) {
      filter.status = orderStatuses.join(",");
    }
    const baseAsset = trimmedOrUndefined(orderBaseAsset);
    if (baseAsset !== undefined) {
      filter.baseAsset = baseAsset;
    }
    const quoteAsset = trimmedOrUndefined(orderQuoteAsset);
    if (quoteAsset !== undefined) {
      filter.quoteAsset = quoteAsset;
    }
    addAmountRange(
      filter,
      orderAmountMode,
      debouncedOrderAmountMin,
      debouncedOrderAmountMax,
    );
    addPriceRange(
      filter,
      orderPriceMode,
      debouncedOrderPriceMin,
      debouncedOrderPriceMax,
    );
    addAtRange(
      filter,
      orderAtMode,
      dateTimeFilter(debouncedOrderAtMin),
      dateTimeFilter(debouncedOrderAtMax),
    );
    return filter;
  }, [
    debouncedOrderAmountMax,
    debouncedOrderAmountMin,
    debouncedOrderAtMax,
    debouncedOrderAtMin,
    debouncedOrderPriceMax,
    debouncedOrderPriceMin,
    normalizedOrderSource,
    orderAccount,
    orderAmountMode,
    orderAtMode,
    orderBaseAsset,
    orderPage,
    orderPriceMode,
    orderQuoteAsset,
    orderSide,
    orderSize,
    orderSort.order,
    orderSort.sort,
    orderStatuses,
  ]);
  const ordersResult = useOrdersPage(orderListFilters);
  const tradeListFilters = useMemo<TradesFilter>(() => {
    const filter: TradesFilter = {
      id: appliedTradeExternalId.trim() || undefined,
      account: tradeAccount.trim() || undefined,
      source: normalizedTradeSource,
      limit: tradeSize,
      offset: tradePage * tradeSize,
      sort: tradeSort.sort,
      order: tradeSort.order,
    };
    if (tradeSide !== "all") {
      filter.side = tradeSide;
    }
    const baseAsset = trimmedOrUndefined(tradeBaseAsset);
    if (baseAsset !== undefined) {
      filter.baseAsset = baseAsset;
    }
    const quoteAsset = trimmedOrUndefined(tradeQuoteAsset);
    if (quoteAsset !== undefined) {
      filter.quoteAsset = quoteAsset;
    }
    const tradeAtFrom = dateTimeFilter(debouncedTradeAtMin);
    const tradeAtTo = dateTimeFilter(debouncedTradeAtMax);
    if (tradeAtMode === "after" && tradeAtFrom !== undefined) {
      filter.atMode = tradeAtMode;
      filter.atMin = tradeAtFrom;
    } else if (tradeAtMode === "before" && tradeAtFrom !== undefined) {
      filter.atMode = tradeAtMode;
      filter.atMax = tradeAtFrom;
    } else if (
      tradeAtMode === "between" &&
      tradeAtFrom !== undefined &&
      tradeAtTo !== undefined
    ) {
      filter.atMode = tradeAtMode;
      filter.atMin = tradeAtFrom;
      filter.atMax = tradeAtTo;
    }
    if (tradeQuantityMode === "between") {
      if (
        debouncedTradeQuantityMin !== "" &&
        debouncedTradeQuantityMax !== ""
      ) {
        filter.quantityMode = tradeQuantityMode;
        filter.quantityMin = debouncedTradeQuantityMin;
        filter.quantityMax = debouncedTradeQuantityMax;
      }
    } else if (debouncedTradeQuantityMin !== "") {
      filter.quantityMode = tradeQuantityMode as RangeFilterMode;
      filter.quantityMin = debouncedTradeQuantityMin;
    }
    if (tradePriceMode === "between") {
      if (debouncedTradePriceMin !== "" && debouncedTradePriceMax !== "") {
        filter.priceMode = tradePriceMode;
        filter.priceMin = debouncedTradePriceMin;
        filter.priceMax = debouncedTradePriceMax;
      }
    } else if (debouncedTradePriceMin !== "") {
      filter.priceMode = tradePriceMode as RangeFilterMode;
      filter.priceMin = debouncedTradePriceMin;
    }
    if (tradeLockPriceMode === "between") {
      if (
        debouncedTradeLockPriceMin !== "" &&
        debouncedTradeLockPriceMax !== ""
      ) {
        filter.lockPriceMode = tradeLockPriceMode;
        filter.lockPriceMin = debouncedTradeLockPriceMin;
        filter.lockPriceMax = debouncedTradeLockPriceMax;
      }
    } else if (debouncedTradeLockPriceMin !== "") {
      filter.lockPriceMode = tradeLockPriceMode as RangeFilterMode;
      filter.lockPriceMin = debouncedTradeLockPriceMin;
    }
    return filter;
  }, [
    appliedTradeExternalId,
    debouncedTradeAtMax,
    debouncedTradeAtMin,
    debouncedTradeLockPriceMax,
    debouncedTradeLockPriceMin,
    debouncedTradePriceMax,
    debouncedTradePriceMin,
    debouncedTradeQuantityMax,
    debouncedTradeQuantityMin,
    normalizedTradeSource,
    tradeAtMode,
    tradeAccount,
    tradeBaseAsset,
    tradeLockPriceMode,
    tradePage,
    tradePriceMode,
    tradeQuantityMode,
    tradeQuoteAsset,
    tradeSide,
    tradeSize,
    tradeSort.order,
    tradeSort.sort,
  ]);
  const tradesResult = useTradesPage(tradeListFilters);

  const accountSuggestionQuery =
    tab === "orders" ? debouncedOrderAccountDraft : debouncedTradeAccountDraft;
  const [allAccountSuggestions, setAllAccountSuggestions] = useState<string[]>(
    [],
  );
  const visibleAccountSuggestions =
    accountSuggestionQuery.trim() === "" ? [] : allAccountSuggestions;
  useEffect(() => {
    const query = accountSuggestionQuery.trim();
    if (query === "") {
      return;
    }
    const controller = new AbortController();
    fetchAccounts(
      {
        code: query,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((accounts) => setAllAccountSuggestions(accounts.map((a) => a.code)))
      .catch(() => {
        if (!controller.signal.aborted) {
          setAllAccountSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [accountSuggestionQuery, fetchAccounts]);

  const assetSuggestionQuery = useMemo(
    () =>
      Array.from(
        new Set(
          [
            debouncedOrderBaseAssetDraft,
            debouncedOrderQuoteAssetDraft,
            debouncedTradeBaseAssetDraft,
            debouncedTradeQuoteAssetDraft,
          ]
            .map((value) => value.trim())
            .filter(Boolean),
        ),
      ),
    [
      debouncedOrderBaseAssetDraft,
      debouncedOrderQuoteAssetDraft,
      debouncedTradeBaseAssetDraft,
      debouncedTradeQuoteAssetDraft,
    ],
  );
  const [assetSuggestions, setAssetSuggestions] = useState<string[]>([]);
  const visibleAssetSuggestions =
    assetSuggestionQuery.length === 0 ? [] : assetSuggestions;
  useEffect(() => {
    if (assetSuggestionQuery.length === 0) {
      return;
    }
    const controller = new AbortController();
    Promise.all(
      assetSuggestionQuery.map((query) =>
        fetchAssets(
          {
            code: query,
            codeMatch: "starts_with",
            limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
            sort: "code",
          },
          controller.signal,
        ),
      ),
    )
      .then((pages) => {
        const next = new Set<string>();
        for (const assets of pages) {
          for (const asset of assets) {
            next.add(asset.code);
          }
        }
        setAssetSuggestions(Array.from(next).sort().slice(0, 12));
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setAssetSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [assetSuggestionQuery, fetchAssets]);

  // Dialog state
  const [submitOpen, setSubmitOpen] = useState(false);
  const [submitInitialValues, setSubmitInitialValues] = useState<
    OrderInitialValues | undefined
  >(undefined);
  const [detailOrderExternalId, setDetailOrderExternalId] = useState<
    string | null
  >(params.get("order"));
  const [detailRefreshKey, setDetailRefreshKey] = useState(0);
  const [detailSuccessBanner, setDetailSuccessBanner] = useState<
    string | undefined
  >(undefined);
  const [execReportOrderExternalId, setExecReportOrderExternalId] = useState<
    string | null
  >(null);
  const [execReportInitialValues, setExecReportInitialValues] = useState<
    ExecReportInitialValues | undefined
  >(undefined);
  const [lookupId, setLookupId] = useState("");
  const [lookupBusy, setLookupBusy] = useState(false);
  const [lookupNotFoundOpen, setLookupNotFoundOpen] = useState(false);
  const [verifyTokenOpen, setVerifyTokenOpen] = useState(false);
  // Workflow approval tokens created in this session, keyed by order external
  // id. The token is returned only by submit, so it remains in page state until
  // cancellation, an explicit report, or reload; confirmation is history-only.
  const [workflowOrderTokens, setWorkflowOrderTokens] = useState<
    Record<string, ApprovalToken>
  >({});

  const rememberWorkflowToken = useCallback((token: ApprovalToken) => {
    if (token.token === "" || token.id === "") {
      return;
    }
    setWorkflowOrderTokens((prev) => ({ ...prev, [token.id]: token }));
  }, []);

  const forgetWorkflowToken = useCallback((orderExternalId: string) => {
    setWorkflowOrderTokens((prev) => {
      if (!(orderExternalId in prev)) {
        return prev;
      }
      const next = { ...prev };
      delete next[orderExternalId];
      return next;
    });
  }, []);

  function openDetail(externalId: string) {
    setDetailSuccessBanner(undefined);
    setDetailOrderExternalId(externalId);
  }

  function openDetailWithBanner(externalId: string, banner: string) {
    setDetailSuccessBanner(banner);
    setDetailOrderExternalId(externalId);
  }

  function closeDetail() {
    setDetailOrderExternalId(null);
    setDetailSuccessBanner(undefined);
  }

  function openExecReport(
    externalId: string,
    values?: ExecReportInitialValues,
  ) {
    setExecReportOrderExternalId(externalId);
    setExecReportInitialValues(values);
  }

  function closeExecReport() {
    setExecReportOrderExternalId(null);
    setExecReportInitialValues(undefined);
  }

  function openCloneOrder(values: OrderInitialValues) {
    setSubmitInitialValues(values);
    setSubmitOpen(true);
  }

  function openCloneExecReport(
    orderExternalId: string,
    values: ExecReportInitialValues,
  ) {
    setDetailOrderExternalId(null);
    setExecReportInitialValues(values);
    setExecReportOrderExternalId(orderExternalId);
  }

  const activeLoad = tab === "orders" ? ordersResult : tradesResult;
  const activeReload =
    tab === "orders" ? ordersResult.reload : tradesResult.reload;
  // Render the server's rows as-is. A mutation refetches (reload) so the table
  // reflects the engine's resulting state, never the operator's requested one.
  const orderRows =
    ordersResult.load.state === "ready" ? ordersResult.load.data.items : [];
  const tradeRows = useMemo(
    () =>
      tradesResult.load.state === "ready" ? tradesResult.load.data.items : [],
    [tradesResult.load],
  );
  // Deep-link: open the trade's order detail when the applied trade id changes.
  useEffect(() => {
    const externalId = appliedTradeExternalId.trim();
    if (externalId === "") {
      openedTradeExternalIdRef.current = null;
      return;
    }
    if (openedTradeExternalIdRef.current === externalId) {
      return;
    }
    const trade = tradeRows.find((row) => row.id === externalId);
    if (trade === undefined) {
      return;
    }
    openedTradeExternalIdRef.current = externalId;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setDetailSuccessBanner(undefined);
    setDetailOrderExternalId(trade.order);
  }, [appliedTradeExternalId, tradeRows]);
  const pagedOrders = orderRows;
  const pagedTrades = tradeRows;
  const hasMoreOrders =
    ordersResult.load.state === "ready" &&
    (orderPage + 1) * orderSize < ordersResult.load.data.total;
  const tradesPage =
    tradesResult.load.state === "ready" ? tradesResult.load.data : null;
  const hasMoreTrades =
    tradesPage !== null && (tradePage + 1) * tradeSize < tradesPage.total;
  const orderPager = (
    <TablePagination
      page={orderPage}
      canPrevious={orderPage > 0}
      canNext={hasMoreOrders}
      knownTotalPages={
        ordersResult.load.state === "ready"
          ? knownPageCount(ordersResult.load.data.total, orderSize)
          : undefined
      }
      onPrevious={() => setOrderPage((p) => Math.max(0, p - 1))}
      onNext={() => setOrderPage((p) => p + 1)}
      onPage={setOrderPage}
    />
  );
  const tradePager = (
    <TablePagination
      page={tradePage}
      canPrevious={tradePage > 0}
      canNext={hasMoreTrades}
      knownTotalPages={
        tradesPage !== null
          ? knownPageCount(tradesPage.total, tradeSize)
          : undefined
      }
      onPrevious={() => setTradePage((p) => Math.max(0, p - 1))}
      onNext={() => setTradePage((p) => p + 1)}
      onPage={setTradePage}
    />
  );

  // When the active tab is filtered to a single account, opening "Add order"
  // pre-fills that account; with no account filter, the form opens blank.
  const activeAccountFilter = (
    tab === "orders" ? orderAccount : tradeAccount
  ).trim();
  const activeAccountGlobalLocked =
    globalAccountFilter.account !== "" &&
    activeAccountFilter === globalAccountFilter.account;
  const activeSourceFilter =
    tab === "orders" ? normalizedOrderSource : normalizedTradeSource;
  const activeExportFilters = {
    ...(activeAccountFilter ? { account: activeAccountFilter } : {}),
    ...(activeSourceFilter ? { source: activeSourceFilter } : {}),
    ...(tab === "trades" && appliedTradeExternalId.trim()
      ? { id: appliedTradeExternalId.trim() }
      : {}),
  };

  const shareHref = useMemo(() => {
    const query = new URLSearchParams();
    if (tab === "trades") {
      query.set("tab", "trades");
      appendShareParam(query, "id", appliedTradeExternalId.trim());
      appendShareParam(query, "account", tradeAccount.trim());
      appendShareParam(query, "source", normalizedTradeSource);
      appendShareParam(query, "side", tradeSide, "all");
      appendShareParam(query, "baseAsset", tradeBaseAsset.trim());
      appendShareParam(query, "quoteAsset", tradeQuoteAsset.trim());
      appendShareParam(query, "atMode", tradeAtMode, "after");
      appendShareParam(query, "atMin", tradeAtMin.trim());
      appendShareParam(query, "atMax", tradeAtMax.trim());
      appendShareParam(query, "quantityMode", tradeQuantityMode, "eq");
      appendShareParam(query, "quantityMin", tradeQuantityMin.trim());
      appendShareParam(query, "quantityMax", tradeQuantityMax.trim());
      appendShareParam(query, "priceMode", tradePriceMode, "eq");
      appendShareParam(query, "priceMin", tradePriceMin.trim());
      appendShareParam(query, "priceMax", tradePriceMax.trim());
      appendShareParam(query, "lockPriceMode", tradeLockPriceMode, "eq");
      appendShareParam(query, "lockPriceMin", tradeLockPriceMin.trim());
      appendShareParam(query, "lockPriceMax", tradeLockPriceMax.trim());
    } else {
      appendShareParam(query, "account", orderAccount.trim());
      appendShareParam(query, "source", normalizedOrderSource);
      appendShareParam(query, "side", orderSide, "all");
      appendShareParam(query, "status", orderStatuses.join(","));
      appendShareParam(query, "baseAsset", orderBaseAsset.trim());
      appendShareParam(query, "quoteAsset", orderQuoteAsset.trim());
      appendShareParam(query, "amountMode", orderAmountMode, "all");
      appendShareParam(query, "amountMin", orderAmountMin.trim());
      appendShareParam(query, "amountMax", orderAmountMax.trim());
      appendShareParam(query, "priceMode", orderPriceMode, "all");
      appendShareParam(query, "priceMin", orderPriceMin.trim());
      appendShareParam(query, "priceMax", orderPriceMax.trim());
      appendShareParam(query, "atMode", orderAtMode, "all");
      appendShareParam(query, "atMin", orderAtMin.trim());
      appendShareParam(query, "atMax", orderAtMax.trim());
    }
    return shareUrl("/orders", query);
  }, [
    appliedTradeExternalId,
    normalizedOrderSource,
    normalizedTradeSource,
    orderAccount,
    orderAmountMax,
    orderAmountMin,
    orderAmountMode,
    orderAtMax,
    orderAtMin,
    orderAtMode,
    orderBaseAsset,
    orderPriceMax,
    orderPriceMin,
    orderPriceMode,
    orderQuoteAsset,
    orderSide,
    orderStatuses,
    tab,
    tradeAccount,
    tradeAtMax,
    tradeAtMin,
    tradeAtMode,
    tradeBaseAsset,
    tradeLockPriceMax,
    tradeLockPriceMin,
    tradeLockPriceMode,
    tradePriceMax,
    tradePriceMin,
    tradePriceMode,
    tradeQuantityMax,
    tradeQuantityMin,
    tradeQuantityMode,
    tradeQuoteAsset,
    tradeSide,
  ]);

  const orderRangeValue = useCallback(
    (mode: RangeFilterMode, min: string, max: string) => {
      const operator = t(`filter.range.${mode}`);
      const from = min.trim();
      const to = max.trim();
      if (mode === "between") {
        return [operator, from, to].filter(Boolean).join(" ");
      }
      if (mode === "less_than") {
        return [operator, to || from].filter(Boolean).join(" ");
      }
      return [operator, from || to].filter(Boolean).join(" ");
    },
    [t],
  );
  const numberRangeValue = useCallback(
    (mode: RangeFilterMode, min: string, max: string) => {
      const operator = tc(`operators.number.${mode}`);
      const from = min.trim();
      const to = max.trim();
      if (mode === "between") {
        return [operator, from, to].filter(Boolean).join(" ");
      }
      if (mode === "lt" || mode === "lte") {
        return [operator, to || from].filter(Boolean).join(" ");
      }
      return [operator, from || to].filter(Boolean).join(" ");
    },
    [tc],
  );
  const timeRangeValue = useCallback(
    (mode: RangeFilterMode, min: string, max: string) => {
      const operator = tc(`operators.time.${mode}`);
      const from = min.trim();
      const to = max.trim();
      if (mode === "between") {
        return [operator, from, to].filter(Boolean).join(" ");
      }
      if (mode === "before") {
        return [operator, to || from].filter(Boolean).join(" ");
      }
      return [operator, from || to].filter(Boolean).join(" ");
    },
    [tc],
  );

  const activeFilterChips = useMemo(() => {
    const entries: Array<{ key: string; label: string; onRemove: () => void }> =
      [];
    const add = (key: string, label: string, onRemove: () => void) => {
      entries.push({ key, label, onRemove });
    };
    if (tab === "orders") {
      if (orderAccount.trim() !== "") {
        add("account", `${t("table.account")}: ${orderAccount.trim()}`, () => {
          if (orderAccount.trim() === globalAccountFilter.account) {
            globalAccountFilter.clear();
          }
          setOrderAccountDraft("");
          setOrderAccount("");
          setOrderPage(0);
        });
      }
      if (normalizedOrderSource !== undefined) {
        add("source", `${t("table.source")}: ${normalizedOrderSource}`, () => {
          setOrderSource("");
          setOrderPage(0);
        });
      }
      if (orderSide !== "all") {
        add(
          "side",
          `${t("filter.sideLabel")}: ${t(`filter.side.${orderSide}`)}`,
          () => {
            setOrderSide("all");
            setOrderPage(0);
          },
        );
      }
      if (orderStatuses.length > 0) {
        const value = statusIsActive
          ? t("filter.quickStatus.active")
          : orderStatuses
              .map((status) => t(`filter.status.${status}`))
              .join(", ");
        add("status", `${t("filter.statusLabel")}: ${value}`, () => {
          setOrderStatuses([]);
          setOrderStatusesDraft([]);
          setOrderPage(0);
        });
      }
      if (orderBaseAsset.trim() !== "") {
        add(
          "baseAsset",
          `${t("filter.baseAssetLabel")}: ${orderBaseAsset.trim()}`,
          () => {
            setOrderBaseAssetDraft("");
            setOrderBaseAsset("");
            setOrderPage(0);
          },
        );
      }
      if (orderQuoteAsset.trim() !== "") {
        add(
          "quoteAsset",
          `${t("filter.quoteAssetLabel")}: ${orderQuoteAsset.trim()}`,
          () => {
            setOrderQuoteAssetDraft("");
            setOrderQuoteAsset("");
            setOrderPage(0);
          },
        );
      }
      if (orderAmountMode !== "all") {
        add(
          "amount",
          `${t("filter.amountLabel")}: ${orderRangeValue(orderAmountMode, orderAmountMin, orderAmountMax)}`,
          () => {
            setOrderAmountMode("all");
            setOrderAmountMin("");
            setOrderAmountMax("");
            setOrderPage(0);
          },
        );
      }
      if (orderPriceMode !== "all") {
        add(
          "price",
          `${t("filter.priceLabel")}: ${orderRangeValue(orderPriceMode, orderPriceMin, orderPriceMax)}`,
          () => {
            setOrderPriceMode("all");
            setOrderPriceMin("");
            setOrderPriceMax("");
            setOrderPage(0);
          },
        );
      }
      if (orderAtMode !== "all") {
        add(
          "at",
          `${t("filter.timeLabel")}: ${orderRangeValue(orderAtMode, orderAtMin, orderAtMax)}`,
          () => {
            setOrderAtMode("all");
            setOrderAtMin("");
            setOrderAtMax("");
            setOrderPage(0);
          },
        );
      }
      return entries;
    }
    if (appliedTradeExternalId.trim() !== "") {
      add(
        "externalId",
        `${t("table.externalId")}: ${appliedTradeExternalId.trim()}`,
        () => {
          setTradeExternalId("");
          setAppliedTradeExternalId("");
          resetTradePage();
        },
      );
    }
    if (tradeAccount.trim() !== "") {
      add("account", `${t("table.account")}: ${tradeAccount.trim()}`, () => {
        if (tradeAccount.trim() === globalAccountFilter.account) {
          globalAccountFilter.clear();
        }
        setTradeAccountDraft("");
        setTradeAccount("");
        resetTradePage();
      });
    }
    if (normalizedTradeSource !== undefined) {
      add("source", `${t("table.source")}: ${normalizedTradeSource}`, () => {
        setTradeSource("");
        resetTradePage();
      });
    }
    if (tradeSide !== "all") {
      add(
        "side",
        `${t("filter.sideLabel")}: ${t(`filter.side.${tradeSide}`)}`,
        () => {
          setTradeSide("all");
          resetTradePage();
        },
      );
    }
    if (tradeBaseAsset.trim() !== "") {
      add(
        "baseAsset",
        `${t("filter.baseAssetLabel")}: ${tradeBaseAsset.trim()}`,
        () => {
          setTradeBaseAssetDraft("");
          setTradeBaseAsset("");
          resetTradePage();
        },
      );
    }
    if (tradeQuoteAsset.trim() !== "") {
      add(
        "quoteAsset",
        `${t("filter.quoteAssetLabel")}: ${tradeQuoteAsset.trim()}`,
        () => {
          setTradeQuoteAssetDraft("");
          setTradeQuoteAsset("");
          resetTradePage();
        },
      );
    }
    if (
      tradeAtMode !== "after" ||
      tradeAtMin.trim() !== "" ||
      tradeAtMax.trim() !== ""
    ) {
      add(
        "at",
        `${t("filter.timeLabel")}: ${timeRangeValue(tradeAtMode, tradeAtMin, tradeAtMax)}`,
        () => {
          setTradeAtMode("after");
          setTradeAtMin("");
          setTradeAtMax("");
          resetTradePage();
        },
      );
    }
    if (
      tradeQuantityMode !== "eq" ||
      tradeQuantityMin.trim() !== "" ||
      tradeQuantityMax.trim() !== ""
    ) {
      add(
        "quantity",
        `${t("table.qty")}: ${numberRangeValue(tradeQuantityMode, tradeQuantityMin, tradeQuantityMax)}`,
        () => {
          setTradeQuantityMode("eq");
          setTradeQuantityMin("");
          setTradeQuantityMax("");
          resetTradePage();
        },
      );
    }
    if (
      tradePriceMode !== "eq" ||
      tradePriceMin.trim() !== "" ||
      tradePriceMax.trim() !== ""
    ) {
      add(
        "price",
        `${t("table.price")}: ${numberRangeValue(tradePriceMode, tradePriceMin, tradePriceMax)}`,
        () => {
          setTradePriceMode("eq");
          setTradePriceMin("");
          setTradePriceMax("");
          resetTradePage();
        },
      );
    }
    if (
      tradeLockPriceMode !== "eq" ||
      tradeLockPriceMin.trim() !== "" ||
      tradeLockPriceMax.trim() !== ""
    ) {
      add(
        "lockPrice",
        `${t("table.lockPrice")}: ${numberRangeValue(tradeLockPriceMode, tradeLockPriceMin, tradeLockPriceMax)}`,
        () => {
          setTradeLockPriceMode("eq");
          setTradeLockPriceMin("");
          setTradeLockPriceMax("");
          resetTradePage();
        },
      );
    }
    return entries;
  }, [
    appliedTradeExternalId,
    normalizedOrderSource,
    normalizedTradeSource,
    numberRangeValue,
    orderAccount,
    orderAmountMax,
    orderAmountMin,
    orderAmountMode,
    orderAtMax,
    orderAtMin,
    orderAtMode,
    orderBaseAsset,
    orderRangeValue,
    orderPriceMax,
    orderPriceMin,
    orderPriceMode,
    orderQuoteAsset,
    orderSide,
    orderStatuses,
    statusIsActive,
    t,
    tab,
    tradeAccount,
    tradeAtMax,
    tradeAtMin,
    tradeAtMode,
    tradeBaseAsset,
    tradeLockPriceMax,
    tradeLockPriceMin,
    tradeLockPriceMode,
    tradePriceMax,
    tradePriceMin,
    tradePriceMode,
    tradeQuantityMax,
    tradeQuantityMin,
    tradeQuantityMode,
    tradeQuoteAsset,
    tradeSide,
    timeRangeValue,
    globalAccountFilter,
  ]);

  const visibleFilterChips = useMemo(
    () =>
      tab === "orders"
        ? activeFilterChips.filter(
            (entry) =>
              ["amount", "price", "at"].includes(entry.key) ||
              (entry.key === "status" && !statusIsActive),
          )
        : activeFilterChips.filter((entry) =>
            ["at", "quantity", "price", "lockPrice"].includes(entry.key),
          ),
    [activeFilterChips, statusIsActive, tab],
  );
  const advancedFilterCount = visibleFilterChips.length;
  const clearActiveFilters = () => {
    for (const entry of activeFilterChips) {
      if (entry.key === "account" && activeAccountGlobalLocked) {
        continue;
      }
      entry.onRemove();
    }
  };

  function filterOrdersAccount(account: string) {
    setTab("orders");
    setOrderAccountDraft(account);
    setOrderAccount(account);
    setOrderPage(0);
  }

  function filterOrdersInstrument(baseAsset: string, quoteAsset: string) {
    setTab("orders");
    setOrderBaseAssetDraft(baseAsset);
    setOrderQuoteAssetDraft(quoteAsset);
    setOrderBaseAsset(baseAsset);
    setOrderQuoteAsset(quoteAsset);
    setOrderPage(0);
  }

  function filterTradesAccount(account: string) {
    setTab("trades");
    setTradeAccountDraft(account);
    setTradeAccount(account);
    resetTradePage();
  }

  function filterTradesInstrument(baseAsset: string, quoteAsset: string) {
    setTab("trades");
    setTradeBaseAssetDraft(baseAsset);
    setTradeQuoteAssetDraft(quoteAsset);
    setTradeBaseAsset(baseAsset);
    setTradeQuoteAsset(quoteAsset);
    resetTradePage();
  }

  function openAddOrder() {
    setSubmitInitialValues(
      activeAccountFilter
        ? {
            account: activeAccountFilter,
            baseAsset: "",
            quoteAsset: "",
            side: "",
            amountKind: "",
            amountValue: "",
            price: "",
          }
        : undefined,
    );
    setSubmitOpen(true);
  }

  async function openOrderById(value: string) {
    if (lookupBusy) {
      return;
    }
    const externalId = value.trim();
    if (!externalId) {
      return;
    }
    setLookupBusy(true);
    try {
      const detail = await fetchOrderDetail(externalId);
      setLookupNotFoundOpen(false);
      openDetail(detail.order.id);
    } catch {
      setLookupNotFoundOpen(true);
    } finally {
      setLookupBusy(false);
    }
  }

  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.defaultPrevented || isEditableTarget(event.target)) {
        return;
      }
      if (event.key === "/") {
        event.preventDefault();
        filterRegionRef.current
          ?.querySelector<HTMLInputElement>("input:not([disabled])")
          ?.focus();
        return;
      }
      if (event.key !== "Escape") {
        return;
      }
      if (detailOrderExternalId !== null) {
        setDetailOrderExternalId(null);
        setDetailSuccessBanner(undefined);
        return;
      }
      if (execReportOrderExternalId !== null) {
        setExecReportOrderExternalId(null);
        setExecReportInitialValues(undefined);
        return;
      }
      if (submitOpen) {
        setSubmitInitialValues(undefined);
        setSubmitOpen(false);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [detailOrderExternalId, execReportOrderExternalId, submitOpen]);

  return (
    <Page
      title={t("title")}
      actions={
        <div className="flex items-center gap-2">
          <PageSizeSelect
            value={tab === "orders" ? orderSize : tradeSize}
            onChange={(value) => {
              if (tab === "orders") {
                setOrderSize(value);
                setOrderPage(0);
              } else {
                setTradeSize(value);
                resetTradePage();
              }
            }}
            ariaLabel={t("filter.sizeAriaLabel")}
            rowCountLabel={(count) => t("filter.sizeRows", { count })}
          />
          <RefreshButton
            onClick={activeReload}
            busy={activeLoad.load.state === "loading"}
          />
          <CsvTransferMenu
            exports={[
              {
                entity: tab,
                filters: activeExportFilters,
                label:
                  tab === "orders"
                    ? t("businessCsv.exportOrdersCsv")
                    : t("businessCsv.exportTradesCsv"),
              },
            ]}
          />
          <Button
            variant="outline"
            size="sm"
            onClick={() => setVerifyTokenOpen(true)}
          >
            <ShieldCheck className="h-3.5 w-3.5" />
            {t("panel.verifyTokenButton")}
          </Button>
          <Button size="sm" onClick={openAddOrder}>
            <Plus className="h-3.5 w-3.5" />
            {t("addOrder.button")}
          </Button>
        </div>
      }
    >
      <p className="text-xs text-muted-lt">{t("subtitle")}</p>

      {/* Tab toggle */}
      <div className="flex w-fit gap-1 rounded-card border border-border bg-surface-2 p-1">
        {(["orders", "trades"] as TabId[]).map((tabId) => (
          <button
            key={tabId}
            type="button"
            onClick={() => setTab(tabId)}
            className={[
              "rounded-badge px-3 py-1 text-xs font-medium capitalize transition-colors duration-[180ms]",
              tab === tabId
                ? "bg-accent-dim text-accent"
                : "text-muted-lt hover:bg-surface-hover hover:text-text",
            ].join(" ")}
          >
            {t(`tab.${tabId}`)}
          </button>
        ))}
      </div>

      {/* Filters */}
      <div ref={filterRegionRef}>
        <FilterBar
          active={activeFilterChips.length > 0}
          activeLabel={tc("filters.active")}
          onClearActive={clearActiveFilters}
          clearActiveLabel={tc("filters.clearAll")}
          chips={
            visibleFilterChips.length > 0 ? (
              <>
                {visibleFilterChips.map((entry) => (
                  <FilterChip
                    key={entry.key}
                    label={entry.label}
                    removeLabel={tc("filters.removeAdvanced")}
                    onRemove={entry.onRemove}
                  />
                ))}
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-6 text-[0.6875rem]"
                  onClick={() => {
                    for (const entry of visibleFilterChips) {
                      entry.onRemove();
                    }
                  }}
                >
                  {tc("filters.removeAdvanced")}
                </Button>
              </>
            ) : undefined
          }
          trailing={
            <>
              <MoreFiltersButton
                count={advancedFilterCount}
                label={tc("filters.more")}
                onClick={() => {
                  syncAdvancedDrafts();
                  setMoreFiltersOpen(true);
                }}
              />
              <div className="flex items-end">
                <ShareLinkButton
                  href={shareHref}
                  title={tc("rowActions.shareFilters")}
                  copiedTitle={tc("rowActions.copiedLink")}
                  size={32}
                />
              </div>
            </>
          }
          bottom={
            tab === "orders" ? (
              <ExactIdField
                label={tc("exactLookup.label")}
                value={lookupId}
                placeholder={t("lookup.placeholder")}
                openLabel={
                  lookupBusy ? t("lookup.searching") : tc("exactLookup.open")
                }
                onChange={setLookupId}
                onOpen={(value) => void openOrderById(value)}
                style={{ width: "100%" }}
                width={360}
              />
            ) : (
              <ExactIdField
                label={tc("exactLookup.label")}
                value={tradeExternalId}
                placeholder={tc("exactLookup.placeholder", {
                  entity: tc("entities.trade"),
                })}
                openLabel={tc("exactLookup.open")}
                onChange={setTradeExternalId}
                onOpen={(value) => {
                  setAppliedTradeExternalId(value.trim());
                  resetTradePage();
                }}
                style={{ width: "100%" }}
                width={360}
              />
            )
          }
        >
          {tab === "orders" ? (
            <>
              <AutocompleteFilterField
                label={t("table.account")}
                value={orderAccountDraft}
                placeholder={t("filter.accountPlaceholder")}
                suggestions={visibleAccountSuggestions}
                onChange={(value) => {
                  if (
                    orderAccount.trim() === globalAccountFilter.account &&
                    value.trim() === ""
                  ) {
                    globalAccountFilter.clear();
                  }
                  setOrderAccountDraft(value);
                }}
                onSuggestionSelect={(value) =>
                  applyOrderIdentityField("account", value)
                }
                onKeyDown={applyOrderIdentityFiltersOnEnter}
                onClear={() => {
                  if (orderAccount.trim() === globalAccountFilter.account) {
                    globalAccountFilter.clear();
                  }
                  setOrderAccountDraft("");
                  setOrderAccount("");
                  setOrderPage(0);
                }}
                clearLabel={tc("filters.clearField")}
                globalToggle={accountGlobalToggle(
                  orderAccountDraft,
                  (account) => {
                    setOrderAccountDraft(account);
                    setOrderAccount(account);
                    setOrderPage(0);
                  },
                )}
              />
              <AutocompleteFilterField
                label={t("filter.baseAssetLabel")}
                value={orderBaseAssetDraft}
                placeholder={t("filter.baseAssetPlaceholder")}
                suggestions={visibleAssetSuggestions}
                onChange={setOrderBaseAssetDraft}
                onSuggestionSelect={(value) =>
                  applyOrderIdentityField("baseAsset", value)
                }
                onKeyDown={applyOrderIdentityFiltersOnEnter}
                onClear={() => {
                  setOrderBaseAssetDraft("");
                  setOrderBaseAsset("");
                  setOrderPage(0);
                }}
                clearLabel={tc("filters.clearField")}
              />
              <AutocompleteFilterField
                label={t("filter.quoteAssetLabel")}
                value={orderQuoteAssetDraft}
                placeholder={t("filter.quoteAssetPlaceholder")}
                suggestions={visibleAssetSuggestions}
                onChange={setOrderQuoteAssetDraft}
                onSuggestionSelect={(value) =>
                  applyOrderIdentityField("quoteAsset", value)
                }
                onKeyDown={applyOrderIdentityFiltersOnEnter}
                onClear={() => {
                  setOrderQuoteAssetDraft("");
                  setOrderQuoteAsset("");
                  setOrderPage(0);
                }}
                clearLabel={tc("filters.clearField")}
              />
              <div className="flex items-end">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={applyOrderIdentityFilters}
                  disabled={!orderIdentityDraftChanged}
                >
                  {tc("filters.apply")}
                </Button>
              </div>
              <div className="grid gap-1">
                <FieldLabel>{t("filter.quickStatusLabel")}</FieldLabel>
                <Segmented
                  value={statusIsAll ? "all" : statusIsActive ? "active" : ""}
                  options={[
                    { value: "all", label: t("filter.quickStatus.all") },
                    { value: "active", label: t("filter.quickStatus.active") },
                  ]}
                  onChange={(next) => {
                    if (next === "active") {
                      applyActiveStatuses();
                    } else {
                      applyAllStatuses();
                    }
                  }}
                />
              </div>
              <div className="grid gap-1">
                <FieldLabel>{t("filter.sideLabel")}</FieldLabel>
                <Segmented
                  value={orderSide}
                  options={[
                    { value: "all", label: t("filter.side.all") },
                    { value: "buy", label: t("filter.side.buy") },
                    { value: "sell", label: t("filter.side.sell") },
                  ]}
                  onChange={(next) => {
                    setOrderSide(next as "all" | OrderSide);
                    setOrderPage(0);
                  }}
                />
              </div>
              <div className="grid gap-1">
                <FieldLabel>{t("filter.sourceAriaLabel")}</FieldLabel>
                <Select
                  value={orderSource || "_all"}
                  onValueChange={(value) => {
                    setOrderSource(value === "_all" ? "" : value);
                    setOrderPage(0);
                  }}
                >
                  <SelectTrigger
                    className="h-8 w-44 text-xs"
                    aria-label={t("filter.sourceAriaLabel")}
                  >
                    <SelectValue placeholder={t("filter.sourceAll")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="_all">
                      {t("filter.sourceAll")}
                    </SelectItem>
                    {SOURCES.filter(Boolean).map((source) => (
                      <SelectItem key={source} value={source}>
                        {source}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </>
          ) : (
            <>
              <AutocompleteFilterField
                label={t("table.account")}
                value={tradeAccountDraft}
                placeholder={t("filter.accountPlaceholder")}
                suggestions={visibleAccountSuggestions}
                onChange={(value) => {
                  if (
                    tradeAccount.trim() === globalAccountFilter.account &&
                    value.trim() === ""
                  ) {
                    globalAccountFilter.clear();
                  }
                  setTradeAccountDraft(value);
                }}
                onSuggestionSelect={(value) =>
                  applyTradeIdentityField("account", value)
                }
                onKeyDown={applyTradeIdentityFiltersOnEnter}
                onClear={() => {
                  if (tradeAccount.trim() === globalAccountFilter.account) {
                    globalAccountFilter.clear();
                  }
                  setTradeAccountDraft("");
                  setTradeAccount("");
                  resetTradePage();
                }}
                clearLabel={tc("filters.clearField")}
                globalToggle={accountGlobalToggle(
                  tradeAccountDraft,
                  (account) => {
                    setTradeAccountDraft(account);
                    setTradeAccount(account);
                    resetTradePage();
                  },
                )}
              />
              <AutocompleteFilterField
                label={t("filter.baseAssetLabel")}
                value={tradeBaseAssetDraft}
                placeholder={t("filter.baseAssetPlaceholder")}
                suggestions={visibleAssetSuggestions}
                onChange={setTradeBaseAssetDraft}
                onSuggestionSelect={(value) =>
                  applyTradeIdentityField("baseAsset", value)
                }
                onKeyDown={applyTradeIdentityFiltersOnEnter}
                onClear={() => {
                  setTradeBaseAssetDraft("");
                  setTradeBaseAsset("");
                  resetTradePage();
                }}
                clearLabel={tc("filters.clearField")}
              />
              <AutocompleteFilterField
                label={t("filter.quoteAssetLabel")}
                value={tradeQuoteAssetDraft}
                placeholder={t("filter.quoteAssetPlaceholder")}
                suggestions={visibleAssetSuggestions}
                onChange={setTradeQuoteAssetDraft}
                onSuggestionSelect={(value) =>
                  applyTradeIdentityField("quoteAsset", value)
                }
                onKeyDown={applyTradeIdentityFiltersOnEnter}
                onClear={() => {
                  setTradeQuoteAssetDraft("");
                  setTradeQuoteAsset("");
                  resetTradePage();
                }}
                clearLabel={tc("filters.clearField")}
              />
              <div className="flex items-end">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={applyTradeIdentityFilters}
                  disabled={!tradeIdentityDraftChanged}
                >
                  {tc("filters.apply")}
                </Button>
              </div>
              <div className="grid gap-1">
                <FieldLabel>{t("filter.sideLabel")}</FieldLabel>
                <Segmented
                  value={tradeSide}
                  options={[
                    { value: "all", label: t("filter.side.all") },
                    { value: "buy", label: t("filter.side.buy") },
                    { value: "sell", label: t("filter.side.sell") },
                  ]}
                  onChange={(next) => {
                    setTradeSide(next as "all" | OrderSide);
                    resetTradePage();
                  }}
                />
              </div>
              <div className="grid gap-1">
                <FieldLabel>{t("filter.sourceAriaLabel")}</FieldLabel>
                <Select
                  value={tradeSource || "_all"}
                  onValueChange={(value) => {
                    setTradeSource(value === "_all" ? "" : value);
                    resetTradePage();
                  }}
                >
                  <SelectTrigger
                    className="h-8 w-44 text-xs"
                    aria-label={t("filter.sourceAriaLabel")}
                  >
                    <SelectValue placeholder={t("filter.sourceAll")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="_all">
                      {t("filter.sourceAll")}
                    </SelectItem>
                    {SOURCES.filter(Boolean).map((source) => (
                      <SelectItem key={source} value={source}>
                        {source}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </>
          )}
        </FilterBar>
      </div>

      <Dialog
        open={moreFiltersOpen}
        onOpenChange={(next) => {
          if (next) {
            syncAdvancedDrafts();
          }
          setMoreFiltersOpen(next);
        }}
      >
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{tc("filters.more")}</DialogTitle>
            <DialogDescription>
              {t("filter.advancedDescription")}
            </DialogDescription>
          </DialogHeader>
          {tab === "orders" ? (
            <div ref={advancedFilterDialogRef} className="grid gap-3">
              <OrderStatusFilterGroup
                selected={orderStatusesDraft}
                onChange={setOrderStatusesDraft}
              />
              <div className="mt-2 grid gap-3 border-t border-border pt-3">
                <RangeFilterControls
                  label={t("filter.amountLabel")}
                  mode={orderAdvancedDraft.amountMode}
                  min={orderAdvancedDraft.amountMin}
                  max={orderAdvancedDraft.amountMax}
                  onMode={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      amountMode: value,
                    }))
                  }
                  onMin={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      amountMin: value,
                    }))
                  }
                  onMax={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      amountMax: value,
                    }))
                  }
                />
                <RangeFilterControls
                  label={t("filter.priceLabel")}
                  mode={orderAdvancedDraft.priceMode}
                  min={orderAdvancedDraft.priceMin}
                  max={orderAdvancedDraft.priceMax}
                  onMode={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      priceMode: value,
                    }))
                  }
                  onMin={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      priceMin: value,
                    }))
                  }
                  onMax={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      priceMax: value,
                    }))
                  }
                />
                <RangeFilterControls
                  label={t("filter.timeLabel")}
                  mode={orderAdvancedDraft.atMode}
                  min={orderAdvancedDraft.atMin}
                  max={orderAdvancedDraft.atMax}
                  inputType="datetime-local"
                  onMode={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      atMode: value,
                    }))
                  }
                  onMin={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      atMin: value,
                    }))
                  }
                  onMax={(value) =>
                    setOrderAdvancedDraft((current) => ({
                      ...current,
                      atMax: value,
                    }))
                  }
                />
              </div>
            </div>
          ) : (
            <div ref={advancedFilterDialogRef} className="grid gap-3">
              <div className="grid gap-3">
                <div className="grid gap-1">
                  <FieldLabel>{t("filter.timeLabel")}</FieldLabel>
                  <TimeRangeFilter
                    operator={tradeAdvancedDraft.atMode}
                    operators={operatorOptions(t, "time")}
                    operatorAriaLabel={t("table.time")}
                    from={tradeAdvancedDraft.atMin}
                    to={tradeAdvancedDraft.atMax}
                    showPresets={false}
                    clearLabel={tc("filters.clearField")}
                    onOperatorChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        atMode: value as RangeFilterMode,
                      }))
                    }
                    onFromChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        atMin: value,
                      }))
                    }
                    onToChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        atMax: value,
                      }))
                    }
                  />
                </div>
                <div className="grid gap-1">
                  <FieldLabel>{t("table.qty")}</FieldLabel>
                  <NumberRangeFilter
                    operator={tradeAdvancedDraft.quantityMode}
                    operators={operatorOptions(t, "number")}
                    operatorAriaLabel={t("table.qty")}
                    min={tradeAdvancedDraft.quantityMin}
                    max={tradeAdvancedDraft.quantityMax}
                    clearLabel={tc("filters.clearField")}
                    onOperatorChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        quantityMode: value as RangeFilterMode,
                      }))
                    }
                    onMinChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        quantityMin: value,
                      }))
                    }
                    onMaxChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        quantityMax: value,
                      }))
                    }
                  />
                </div>
                <div className="grid gap-1">
                  <FieldLabel>{t("table.price")}</FieldLabel>
                  <NumberRangeFilter
                    operator={tradeAdvancedDraft.priceMode}
                    operators={operatorOptions(t, "number")}
                    operatorAriaLabel={t("table.price")}
                    min={tradeAdvancedDraft.priceMin}
                    max={tradeAdvancedDraft.priceMax}
                    clearLabel={tc("filters.clearField")}
                    onOperatorChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        priceMode: value as RangeFilterMode,
                      }))
                    }
                    onMinChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        priceMin: value,
                      }))
                    }
                    onMaxChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        priceMax: value,
                      }))
                    }
                  />
                </div>
                <div className="grid gap-1">
                  <FieldLabel>{t("table.lockPrice")}</FieldLabel>
                  <NumberRangeFilter
                    operator={tradeAdvancedDraft.lockPriceMode}
                    operators={operatorOptions(t, "number")}
                    operatorAriaLabel={t("table.lockPrice")}
                    min={tradeAdvancedDraft.lockPriceMin}
                    max={tradeAdvancedDraft.lockPriceMax}
                    clearLabel={tc("filters.clearField")}
                    onOperatorChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        lockPriceMode: value as RangeFilterMode,
                      }))
                    }
                    onMinChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        lockPriceMin: value,
                      }))
                    }
                    onMaxChange={(value) =>
                      setTradeAdvancedDraft((current) => ({
                        ...current,
                        lockPriceMax: value,
                      }))
                    }
                  />
                </div>
              </div>
            </div>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                if (tab === "orders") {
                  setOrderStatusesDraft([]);
                  setOrderAdvancedDraft({
                    amountMode: "all",
                    amountMin: "",
                    amountMax: "",
                    priceMode: "all",
                    priceMin: "",
                    priceMax: "",
                    atMode: "all",
                    atMin: "",
                    atMax: "",
                  });
                } else {
                  setTradeAdvancedDraft({
                    atMode: "after",
                    atMin: "",
                    atMax: "",
                    quantityMode: "eq",
                    quantityMin: "",
                    quantityMax: "",
                    priceMode: "eq",
                    priceMin: "",
                    priceMax: "",
                    lockPriceMode: "eq",
                    lockPriceMin: "",
                    lockPriceMax: "",
                  });
                }
              }}
            >
              {tc("filters.removeAdvanced")}
            </Button>
            <Button
              type="button"
              onClick={applyAdvancedFilters}
              disabled={
                (tab === "orders" &&
                  (orderStatusesDraft.length === 0 ||
                    !orderAdvancedNumericValid)) ||
                (tab === "trades" && !tradeAdvancedNumericValid)
              }
            >
              {tc("filters.applyAdvanced")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Orders view */}
      {tab === "orders" && (
        <>
          {ordersResult.load.state === "loading" && <TableSkeleton cols={10} />}
          <StaleState {...ordersResult} />
          {ordersResult.load.state === "error" && (
            <ErrorState
              message={ordersResult.load.error}
              onRetry={ordersResult.reload}
            />
          )}
          {ordersResult.load.state === "ready" &&
            (ordersResult.load.data.items.length === 0 ? (
              <EmptyState
                title={t("empty.orders.title")}
                hint={t("empty.orders.hint")}
                action={
                  <Button size="sm" onClick={openAddOrder}>
                    <Plus className="h-3.5 w-3.5" />
                    {t("addOrder.button")}
                  </Button>
                }
              />
            ) : (
              <>
                {orderPager}
                <OrdersTable
                  orders={pagedOrders}
                  activeSort={orderSort.sort}
                  activeOrder={orderSort.order}
                  onSortChange={(sort, order) => {
                    setOrderSort(order === "none" ? {} : { sort, order });
                    setOrderPage(0);
                  }}
                  onRowClick={(o) => openDetail(o.id)}
                  onFilterAccount={filterOrdersAccount}
                  onFilterInstrument={filterOrdersInstrument}
                  onClone={openCloneOrder}
                />
                {orderPager}
              </>
            ))}
        </>
      )}

      {/* Trades view */}
      {tab === "trades" && (
        <>
          {tradesResult.load.state === "loading" && <TableSkeleton cols={10} />}
          <StaleState {...tradesResult} />
          {tradesResult.load.state === "error" && (
            <ErrorState
              message={tradesResult.load.error}
              onRetry={tradesResult.reload}
            />
          )}
          {tradesResult.load.state === "ready" &&
            (tradesResult.load.data.total === 0 ? (
              <EmptyState
                title={t("empty.trades.title")}
                hint={t("empty.trades.hint")}
              />
            ) : (
              <>
                {tradePager}
                <TradesTable
                  trades={pagedTrades}
                  activeSort={tradeSort.sort}
                  activeOrder={tradeSort.order}
                  onSortChange={(sort, order) => {
                    setTradeSort(order === "none" ? {} : { sort, order });
                    resetTradePage();
                  }}
                  onOrderClick={openDetail}
                  onFilterAccount={filterTradesAccount}
                  onFilterInstrument={filterTradesInstrument}
                  onCloneExecReport={openCloneExecReport}
                />
                {tradePager}
              </>
            ))}
        </>
      )}

      {/* Dialogs */}
      <SubmitOrderDialog
        open={submitOpen}
        onClose={() => {
          setSubmitOpen(false);
          setSubmitInitialValues(undefined);
        }}
        onCreated={ordersResult.reload}
        onOpenDetail={(id, banner) =>
          openDetailWithBanner(id, banner ?? t("addOrder.added"))
        }
        onWorkflowTokenIssued={rememberWorkflowToken}
        accountSuggestions={allAccountSuggestions}
        assetSuggestions={assetSuggestions}
        initialValues={submitInitialValues}
      />

      <OrderDetailDialog
        orderExternalId={detailOrderExternalId}
        refreshKey={detailRefreshKey}
        onClose={closeDetail}
        onExecReport={openExecReport}
        onCloneOrder={openCloneOrder}
        onCloneExecReport={openCloneExecReport}
        workflowToken={
          detailOrderExternalId !== null
            ? (workflowOrderTokens[detailOrderExternalId] ?? null)
            : null
        }
        onWorkflowShortcutCompleted={(id, action) => {
          if (action === "cancel") {
            forgetWorkflowToken(id);
          }
          setDetailRefreshKey((value) => value + 1);
          ordersResult.reload();
        }}
        successBanner={detailSuccessBanner}
      />

      <ExecReportDialog
        orderExternalId={execReportOrderExternalId}
        onClose={closeExecReport}
        onSubmitted={(update) => {
          // Any explicit report permanently moves this order out of the safe
          // shortcut case: later lifecycle changes require another full report.
          forgetWorkflowToken(update.orderExternalId);
          // Reflect only server truth: refetch the table and the open detail so
          // the rendered status/leaves come from the engine, not the request.
          if (detailOrderExternalId === update.orderExternalId) {
            setDetailRefreshKey((value) => value + 1);
          }
          ordersResult.reload();
          tradesResult.reload();
        }}
        initialValues={execReportInitialValues}
      />
      <Dialog open={lookupNotFoundOpen} onOpenChange={setLookupNotFoundOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{t("lookup.notFound.title")}</DialogTitle>
            <DialogDescription>
              {t("lookup.notFound.description", {
                externalId: lookupId.trim(),
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" onClick={() => setLookupNotFoundOpen(false)}>
              {t("lookup.notFound.close")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <OrderVerificationPanel
        orderExternalId={null}
        initialMode="verify"
        open={verifyTokenOpen}
        onOpenChange={setVerifyTokenOpen}
      />
    </Page>
  );
}
