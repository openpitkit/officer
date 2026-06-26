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

import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactElement } from "react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Copy, ExternalLink, Plus, ShieldCheck } from "lucide-react";

import {
  ApiError,
  checkOrder,
  createOrder,
  exportPublicKey,
  fetchAccounts,
  fetchOrderDetail,
  submitExecutionReport,
} from "@/api/client";
import type {
  Balance,
  CheckResult,
  ExecutionBlock,
  Order,
  OrderApproval,
  OrderEvent,
  Source,
  Trade,
} from "@/api/types";
import { useBalances } from "@/api/useBalances";
import { useOrders } from "@/api/useOrders";
import { useTrades } from "@/api/useTrades";
import { formatDateTime } from "@/i18n/format";
import { Autocomplete } from "@/components/Autocomplete";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
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
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { CopyableSnippet } from "@/components/CopyableSnippet";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
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
import {
  hasNextPage,
  pageFetchLimit,
  slicePage,
} from "@/lib/tablePagination";
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
    case "accepted":
    case "filled":
      return "ok";
    case "rejected":
    case "cancelled":
      return "danger";
    case "partial":
      return "warn";
    case "pending":
      return "accent";
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

// ---------------------------------------------------------------------------
// Asset pool — collect distinct base/quote assets from loaded data
// ---------------------------------------------------------------------------

function collectAssets(
  orders: Order[],
  trades: Trade[],
  balances: Balance[],
): string[] {
  const set = new Set<string>();
  for (const o of orders) {
    if (o.baseAsset) {
      set.add(o.baseAsset);
    }
    if (o.quoteAsset) {
      set.add(o.quoteAsset);
    }
  }
  for (const t of trades) {
    if (t.baseAsset) {
      set.add(t.baseAsset);
    }
    if (t.quoteAsset) {
      set.add(t.quoteAsset);
    }
  }
  // Supplement from balances so suggestions exist before any order is placed.
  for (const b of balances) {
    if (b.asset) {
      set.add(b.asset);
    }
  }
  return Array.from(set).sort();
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
      wouldBlockMessage = t("check.accountBlocked", { account: wouldBlock.account });
    } else if (wouldBlock.reason) {
      wouldBlockMessage = t("check.wouldBlockReason", {
        account: wouldBlock.account,
        reason: wouldBlock.reason,
      });
    } else {
      wouldBlockMessage = t("check.wouldBlock", { account: wouldBlock.account });
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
          className={result.passed ? "text-[var(--ok)] font-medium" : "text-[var(--danger)] font-medium"}
        >
          {result.passed ? t("check.wouldPass") : t("check.wouldReject")}
        </span>
      </div>
      {result.rejects.length > 0 && (
        <ul className="space-y-1">
          {result.rejects.map((r, i) => {
            const localizedReason = t(`check.rejectReasons.${r.code}`, { defaultValue: "" });
            const reason = localizedReason || r.reason || r.details || r.code;
            const details =
              r.details && r.details !== r.reason && r.details !== reason ? r.details : "";
            return (
              <li key={i} className="flex flex-wrap gap-x-2 gap-y-0.5 text-[var(--danger)]">
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
      {result.wouldDisplayPrices.length > 0 && (
        <div className="text-muted-lt">
          {t("check.displayPrices", { prices: result.wouldDisplayPrices.join(", ") })}
        </div>
      )}
      {wouldBlock && (
        <div className="text-[var(--danger)]">
          {wouldBlockMessage}
        </div>
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
  accountSuggestions,
  assetSuggestions,
  initialValues,
}: SubmitOrderDialogProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();

  const [externalId, setExternalId] = useState("");
  const [account, setAccount] = useState(initialValues?.account ?? "");
  const [baseAsset, setBaseAsset] = useState(initialValues?.baseAsset ?? "");
  const [quoteAsset, setQuoteAsset] = useState(initialValues?.quoteAsset ?? "");
  const [side, setSide] = useState<string>(initialValues?.side ?? "buy");
  const [amountKind, setAmountKind] = useState<string>(initialValues?.amountKind ?? "");
  const [amountValue, setAmountValue] = useState(initialValues?.amountValue ?? "");
  const [price, setPrice] = useState(initialValues?.price ?? "");
  const [submitMode, setSubmitMode] = useState<"immediate" | "hold" | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [checkState, setCheckState] = useState<CheckState>({ phase: "idle" });
  const checkAbortRef = useRef<AbortController | null>(null);
  const submitAbortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    return () => {
      submitAbortRef.current?.abort();
    };
  }, []);

  // Debounced live check: fires ~350ms after any form field changes.
  useEffect(() => {
    const accountT = account.trim();
    const baseT = baseAsset.trim();
    const quoteT = quoteAsset.trim();
    const amountT = amountValue.trim();
    // Skip when required fields are absent.
    if (!accountT || !baseT || !quoteT || !amountT || !amountKind) {
      // Reset to idle when the form is incomplete; intentional sync.
      // eslint-disable-next-line react-hooks/set-state-in-effect
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
        side,
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
    }, 350);
    return () => {
      window.clearTimeout(timer);
      checkAbortRef.current?.abort();
    };
  }, [account, baseAsset, quoteAsset, side, amountKind, amountValue, price]);

  // Reseed from initialValues whenever the dialog opens (clone path).
  useEffect(() => {
    if (open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setAccount(initialValues?.account ?? "");
      setBaseAsset(initialValues?.baseAsset ?? "");
      setQuoteAsset(initialValues?.quoteAsset ?? "");
      setExternalId("");
      setSide(initialValues?.side ?? "buy");
      setAmountKind(initialValues?.amountKind ?? "");
      setAmountValue(initialValues?.amountValue ?? "");
      setPrice(initialValues?.price ?? "");
      setSubmitMode(null);
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
    setSide(initialValues?.side ?? "buy");
    setAmountKind(initialValues?.amountKind ?? "");
    setAmountValue(initialValues?.amountValue ?? "");
    setPrice(initialValues?.price ?? "");
    setSubmitMode(null);
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

  async function submit() {
    if (!account.trim() || !baseAsset.trim() || !quoteAsset.trim() || !amountValue.trim()) {
      setError(t("addOrder.dialog.validationError"));
      return;
    }
    if (!amountKind) {
      setError(t("addOrder.dialog.amountKindRequired"));
      return;
    }
    if (submitMode === null) {
      setError(t("addOrder.dialog.submitModeRequired"));
      return;
    }
    setBusy(true);
    setError(null);
    submitAbortRef.current?.abort();
    const controller = new AbortController();
    submitAbortRef.current = controller;
    try {
      const body: Parameters<typeof createOrder>[0] = {
        account: account.trim(),
        baseAsset: baseAsset.trim(),
        quoteAsset: quoteAsset.trim(),
        side,
        amountKind,
        amountValue: amountValue.trim(),
      };
      if (price.trim()) {
        body.price = price.trim();
      }
      if (externalId.trim()) {
        body.externalId = externalId.trim();
      }
      body.mode = submitMode;
      const result = await createOrder(body, controller.signal);
      if (controller.signal.aborted) {
        return;
      }
      onCreated();
      reset();
      onClose();
      onOpenDetail(result.order.externalId, result.warning);
    } catch (err) {
      if (controller.signal.aborted) {
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

  // Every required choice must be made before the order can be submitted; the
  // button stays disabled until then so nothing slips through without a mode.
  const canSubmit =
    account.trim() !== "" &&
    baseAsset.trim() !== "" &&
    quoteAsset.trim() !== "" &&
    amountValue.trim() !== "" &&
    amountKind !== "" &&
    submitMode !== null;

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) handleClose(); }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("addOrder.dialog.title")}</DialogTitle>
          <DialogDescription>
            {t("addOrder.dialog.description")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="so-external-id">{t("addOrder.dialog.externalId")}</Label>
            <Input
              id="so-external-id"
              value={externalId}
              spellCheck={false}
              placeholder={t("addOrder.dialog.externalIdPlaceholder")}
              onChange={(e) => setExternalId(e.target.value)}
              disabled={busy}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("addOrder.dialog.externalIdHint")}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="so-account">{t("addOrder.dialog.account")}</Label>
            <Autocomplete
              id="so-account"
              value={account}
              onChange={setAccount}
              suggestions={accountSuggestions}
              placeholder={t("addOrder.dialog.accountPlaceholder")}
              disabled={busy}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="so-base">{t("addOrder.dialog.baseAsset")}</Label>
              <Autocomplete
                id="so-base"
                value={baseAsset}
                onChange={setBaseAsset}
                suggestions={assetSuggestions}
                placeholder={t("addOrder.dialog.baseAssetPlaceholder")}
                disabled={busy}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="so-quote">{t("addOrder.dialog.quoteAsset")}</Label>
              <Autocomplete
                id="so-quote"
                value={quoteAsset}
                onChange={setQuoteAsset}
                suggestions={assetSuggestions}
                placeholder={t("addOrder.dialog.quoteAssetPlaceholder")}
                disabled={busy}
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
              <Label id="so-mode-label">{t("addOrder.dialog.submitMode")}</Label>
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
              className="grid grid-cols-2 gap-2"
            >
              {(["immediate", "hold"] as const).map((mode) => {
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
                    <span className="block text-xs font-semibold text-text">
                      {mode === "immediate"
                        ? t("addOrder.dialog.submitModeImmediate")
                        : t("addOrder.dialog.submitModeHold")}
                    </span>
                    <span className="mt-1 block text-[0.6875rem] leading-snug">
                      {mode === "immediate"
                        ? t("addOrder.dialog.submitModeImmediateHelp")
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
                className="flex-1"
              />
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="so-price">{t("addOrder.dialog.limitPrice")}</Label>
            <NumberStepper
              id="so-price"
              value={price}
              onChange={setPrice}
              placeholder={t("addOrder.dialog.limitPricePlaceholder")}
              disabled={busy}
            />
          </div>

          <CheckPreview state={checkState} />

          {error && (
            <ErrorBanner message={error} onDismiss={() => setError(null)} />
          )}

          <DialogFooter>
            <Button variant="outline" size="sm" onClick={handleClose} disabled={busy}>
              {tc("actions.cancel")}
            </Button>
            <Button size="sm" onClick={submit} disabled={busy || !canSubmit}>
              {busy ? t("addOrder.dialog.submitBusy") : t("addOrder.dialog.submit")}
            </Button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Execution Report dialog
// ---------------------------------------------------------------------------

/** Input fields that can be preseeded when cloning an execution report. */
interface ExecReportInitialValues {
  quantity: string;
  price: string;
  lockPrice: string;
}

function execReportInitialValuesFromOrder(order: Order): ExecReportInitialValues {
  const lockPrice = order.displayPrices.length > 0
    ? order.displayPrices[order.displayPrices.length - 1]
    : "";
  return {
    quantity: "",
    price: lockPrice,
    lockPrice,
  };
}

interface ExecReportDialogProps {
  orderExternalId: string | null;
  onClose: () => void;
  onSubmitted: () => void;
  /** Pre-seed all input fields (clone path). */
  initialValues?: ExecReportInitialValues;
}

function ExecReportDialog({ orderExternalId, onClose, onSubmitted, initialValues }: ExecReportDialogProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();

  const [quantity, setQuantity] = useState(initialValues?.quantity ?? "");
  const [price, setPrice] = useState(initialValues?.price ?? "");
  const [lockPrice, setLockPrice] = useState(initialValues?.lockPrice ?? "");
  const [final, setFinal] = useState(true);
  const [force, setForce] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [blocks, setBlocks] = useState<ExecutionBlock[]>([]);

  // Reseed from initialValues whenever the dialog opens (clone path).
  useEffect(() => {
    if (orderExternalId !== null) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setQuantity(initialValues?.quantity ?? "");
      setPrice(initialValues?.price ?? "");
      setLockPrice(initialValues?.lockPrice ?? "");
      setFinal(true);
      setForce(false);
      setBusy(false);
      setError(null);
      setDone(false);
      setBlocks([]);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orderExternalId]);

  function reset() {
    setQuantity(initialValues?.quantity ?? "");
    setPrice(initialValues?.price ?? "");
    setLockPrice(initialValues?.lockPrice ?? "");
    setFinal(true);
    setForce(false);
    setBusy(false);
    setError(null);
    setDone(false);
    setBlocks([]);
  }

  function handleClose() {
    reset();
    onClose();
  }

  async function submit() {
    if (!quantity.trim() || !price.trim()) {
      setError(t("execReport.dialog.validationError"));
      return;
    }
    if (orderExternalId === null) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const body: {
        quantity: string;
        price: string;
        lockPrice?: string;
        force?: boolean;
        final: boolean;
      } = {
        quantity: quantity.trim(),
        price: price.trim(),
        final,
      };
      if (force) {
        body.force = true;
      }
      if (lockPrice.trim()) {
        body.lockPrice = lockPrice.trim();
      }
      const result = await submitExecutionReport(orderExternalId, body);
      setBlocks(result.blocks);
      setDone(true);
      onSubmitted();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={orderExternalId !== null} onOpenChange={(v) => { if (!v) handleClose(); }}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{t("execReport.dialog.title")}</DialogTitle>
          <DialogDescription>
            {t("execReport.dialog.description", { orderExternalId })}
          </DialogDescription>
        </DialogHeader>

        {done ? (
          <div className="space-y-3">
            <p className="text-xs text-[var(--ok)]">{t("execReport.dialog.accepted")}</p>
            {blocks.length > 0 && (
              <div className="space-y-1 rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] px-3 py-2 text-xs">
                {blocks.map((block, i) => (
                  <div key={i} className="text-[var(--danger)]">
                    <span className="font-medium">
                      {t("execReport.dialog.accountBlocked", { account: block.account })}
                    </span>{" "}
                    {block.reason || block.code}
                  </div>
                ))}
              </div>
            )}
            <DialogFooter>
              <Button size="sm" onClick={handleClose}>{t("execReport.dialog.done")}</Button>
            </DialogFooter>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="er-qty">{t("execReport.dialog.fillQty")}</Label>
                <NumberStepper
                  id="er-qty"
                  value={quantity}
                  onChange={setQuantity}
                  placeholder={t("execReport.dialog.fillQtyPlaceholder")}
                  disabled={busy}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="er-price">{t("execReport.dialog.fillPrice")}</Label>
                <NumberStepper
                  id="er-price"
                  value={price}
                  onChange={setPrice}
                  placeholder={t("execReport.dialog.fillPricePlaceholder")}
                  disabled={busy}
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="er-lock">{t("execReport.dialog.lockPrice")}</Label>
              <NumberStepper
                id="er-lock"
                value={lockPrice}
                onChange={setLockPrice}
                placeholder={t("execReport.dialog.lockPricePlaceholder")}
                disabled={busy}
              />
            </div>
            <label className="flex items-center gap-2 text-xs text-text cursor-pointer select-none">
              <input
                type="checkbox"
                checked={final}
                onChange={(e) => setFinal(e.target.checked)}
                disabled={busy}
                className="accent-[var(--accent)]"
              />
              {t("execReport.dialog.finalFill")}
            </label>
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
              <Button variant="outline" size="sm" onClick={handleClose} disabled={busy}>
                {tc("actions.cancel")}
              </Button>
              <Button size="sm" onClick={submit} disabled={busy}>
                {busy ? t("execReport.dialog.submitBusy") : t("execReport.dialog.submit")}
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Signed pre-trade verdict helpers + section component
// ---------------------------------------------------------------------------

/** Decode a base64url string to a Uint8Array (no padding required). */
function base64urlToBytes(s: string): Uint8Array {
  // base64url → base64: replace URL-safe chars and pad to 4-char boundary.
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/");
  const padded = b64 + "=".repeat((4 - (b64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/** Decode a standard base64 string (with padding) to a Uint8Array. */
function base64StdToBytes(s: string): Uint8Array {
  const binary = atob(s);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/** Extract the verbatim JSON value substring for "approval" from the envelope
 *  JSON string. The approval object is flat (no nested objects), so we scan
 *  forward from the opening '{' to the matching '}', honoring string literals
 *  and backslash escapes. Returns the canonical bytes that were signed. */
function approvalValueSubstring(envJson: string): string {
  // Match the "approval" KEY (preceded by { or , in compact Go JSON), never the
  // same text occurring inside a string value.
  let keyIdx = -1;
  for (let from = 0; ; ) {
    const idx = envJson.indexOf('"approval"', from);
    if (idx === -1) {
      break;
    }
    const prev = envJson[idx - 1];
    if (prev === "{" || prev === ",") {
      keyIdx = idx;
      break;
    }
    from = idx + 1;
  }
  if (keyIdx === -1) {
    throw new Error("approval key not found in envelope");
  }
  // Skip past the key and its colon.
  let i = keyIdx + '"approval"'.length;
  while (i < envJson.length && envJson[i] !== "{") {
    i++;
  }
  if (i >= envJson.length) {
    throw new Error("approval object not found");
  }
  const start = i;
  let depth = 0;
  while (i < envJson.length) {
    const ch = envJson[i];
    if (ch === "{") {
      depth++;
      i++;
    } else if (ch === "}") {
      depth--;
      i++;
      if (depth === 0) {
        break;
      }
    } else if (ch === '"') {
      // Skip over string literals, honoring backslash escapes.
      i++;
      while (i < envJson.length) {
        if (envJson[i] === "\\") {
          i += 2; // skip escaped char
        } else if (envJson[i] === '"') {
          i++;
          break;
        } else {
          i++;
        }
      }
    } else {
      i++;
    }
  }
  return envJson.slice(start, i);
}

type VerifyState =
  | { phase: "idle" }
  | { phase: "verifying" }
  | { phase: "verified" }
  | { phase: "invalid" }
  | { phase: "error"; message: string };

/** Dialog that shows the signed approval envelope for an order. */
function SignedPayloadDialog({
  approval,
  open,
  onOpenChange,
}: {
  approval: OrderApproval;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation("orders");

  const [verifyState, setVerifyState] = useState<VerifyState>({ phase: "idle" });

  // Decode the token to get the approval payload for display purposes.
  const decodedApproval = useMemo(() => {
    try {
      const envJson = new TextDecoder().decode(base64urlToBytes(approval.token));
      const env = JSON.parse(envJson) as { approval?: unknown };
      return JSON.stringify(env.approval, null, 2);
    } catch {
      return "";
    }
  }, [approval.token]);

  // RFC3339 timestamps for display.
  const issuedAtDisplay = approval.issuedAt
    ? formatDateTime(approval.issuedAt)
    : "";
  const expiresAtDisplay = approval.expiresAt
    ? formatDateTime(approval.expiresAt)
    : "";

  async function runVerify() {
    setVerifyState({ phase: "verifying" });
    try {
      const pubBase64 = await exportPublicKey("raw-base64");
      const envJson = new TextDecoder().decode(base64urlToBytes(approval.token));
      const env = JSON.parse(envJson) as { alg?: string; signature?: string };
      if (env.alg === "none") {
        // Should not reach here (the section hides the button for alg=none),
        // but guard defensively.
        setVerifyState({ phase: "idle" });
        return;
      }
      const canonical = new TextEncoder().encode(approvalValueSubstring(envJson));
      const sig = base64StdToBytes(env.signature ?? "");
      const keyBytes = base64StdToBytes(pubBase64);
      if (keyBytes.length !== 32) {
        throw new Error("active public key is not a 32-byte Ed25519 key");
      }
      const cryptoKey = await crypto.subtle.importKey(
        "raw",
        keyBytes,
        { name: "Ed25519" },
        false,
        ["verify"],
      );
      const ok = await crypto.subtle.verify(
        { name: "Ed25519" },
        cryptoKey,
        sig,
        canonical,
      );
      setVerifyState(ok ? { phase: "verified" } : { phase: "invalid" });
    } catch (err) {
      setVerifyState({
        phase: "error",
        message: err instanceof Error ? err.message : String(err),
      });
    }
  }

  // Badge for the current verification state.
  let badge: ReactElement;
  if (approval.alg === "none") {
    badge = <Badge variant="neutral">{t("detail.dialog.signedPayload.badgeUnsigned")}</Badge>;
  } else if (verifyState.phase === "verified") {
    badge = <Badge variant="ok">{t("detail.dialog.signedPayload.badgeVerified")}</Badge>;
  } else if (verifyState.phase === "invalid") {
    badge = <Badge variant="danger">{t("detail.dialog.signedPayload.badgeInvalid")}</Badge>;
  } else {
    badge = <Badge variant="neutral">{t("detail.dialog.signedPayload.badgeUnverified")}</Badge>;
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("detail.dialog.signedPayload.sectionTitle")}</DialogTitle>
          <DialogDescription>
            {t("detail.dialog.signedPayload.dialogDescription")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-3">
            {badge}
            {approval.alg === "ed25519" && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => { void runVerify(); }}
                disabled={verifyState.phase === "verifying"}
              >
                <ShieldCheck className="h-3.5 w-3.5" />
                {verifyState.phase === "verifying"
                  ? t("detail.dialog.signedPayload.verifying")
                  : t("detail.dialog.signedPayload.verifyButton")}
              </Button>
            )}
            {approval.alg === "none" && (
              <span className="text-xs text-muted-lt">
                {t("detail.dialog.signedPayload.unsignedNote")}
              </span>
            )}
          </div>

          {verifyState.phase === "verified" && (
            <p className="text-xs text-[var(--ok)]">
              {t("detail.dialog.signedPayload.verifiedNote")}
            </p>
          )}
          {verifyState.phase === "invalid" && (
            <p className="text-xs text-[var(--danger)]">
              {t("detail.dialog.signedPayload.invalidNote")}
            </p>
          )}
          {verifyState.phase === "error" && (
            <p className="text-xs text-[var(--danger)]">
              {t("detail.dialog.signedPayload.verifyError")}
            </p>
          )}

          {(issuedAtDisplay || expiresAtDisplay) && (
            <div className="flex flex-wrap gap-4 text-xs text-muted-lt">
              {issuedAtDisplay && (
                <span>
                  <span className="font-medium text-muted">
                    {t("detail.dialog.signedPayload.issuedAt")}
                  </span>
                  {" "}
                  <span className="nums">{issuedAtDisplay}</span>
                </span>
              )}
              {expiresAtDisplay && (
                <span>
                  <span className="font-medium text-muted">
                    {t("detail.dialog.signedPayload.expiresAt")}
                  </span>
                  {" "}
                  <span className="nums">{expiresAtDisplay}</span>
                </span>
              )}
            </div>
          )}

          <CopyableSnippet
            label={t("detail.dialog.signedPayload.tokenLabel")}
            text={approval.token}
            rows={3}
          />

          {decodedApproval && (
            <CopyableSnippet
              label={t("detail.dialog.signedPayload.decodedLabel")}
              text={decodedApproval}
              rows={10}
            />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Order Detail dialog — header + event timeline + trades
// ---------------------------------------------------------------------------

interface OrderDetailDialogProps {
  orderExternalId: string | null;
  onClose: () => void;
  onExecReport: (orderExternalId: string, values?: ExecReportInitialValues) => void;
  onCloneOrder: (values: OrderInitialValues) => void;
  onCloneExecReport: (orderExternalId: string, values: ExecReportInitialValues) => void;
  successBanner?: string;
}

type DetailState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; order: Order; events: OrderEvent[]; trades: Trade[]; approval: OrderApproval | null };

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

function OrderDetailDialog({ orderExternalId, onClose, onExecReport, onCloneOrder, onCloneExecReport, successBanner }: OrderDetailDialogProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();

  const [state, setState] = useState<DetailState>({ phase: "loading" });
  const [signatureOpen, setSignatureOpen] = useState(false);

  useEffect(() => {
    if (orderExternalId === null) {
      return;
    }
    // Show the loading state before the detail fetch starts; intentional.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setState({ phase: "loading" });
    setSignatureOpen(false);
    const controller = new AbortController();
    fetchOrderDetail(orderExternalId, controller.signal)
      .then(({ order, events, trades, approval }) => {
        if (controller.signal.aborted) {
          return;
        }
        setState({ phase: "ready", order, events, trades, approval });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ phase: "error", message: errMessage(err) });
        }
      });
    return () => controller.abort();
  }, [orderExternalId]);

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

  return (
    <>
      <Dialog
        open
        onOpenChange={(v) => {
          if (!v) {
            setSignatureOpen(false);
            onClose();
          }
        }}
      >
        <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("detail.dialog.title", { orderExternalId })}</DialogTitle>
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
              <div key={i} className="h-4 w-full animate-pulse rounded bg-border" />
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
                <span className="text-muted-lt">{t("detail.dialog.fieldAccount")}</span>
                <div className="nums mt-0.5 text-text">{state.order.account}</div>
              </div>
              <div>
                <span className="text-muted-lt">{t("detail.dialog.fieldStatus")}</span>
                <div className="mt-0.5">
                  <Badge variant={statusVariant(state.order.status)}>
                    {state.order.status}
                  </Badge>
                </div>
              </div>
              <div>
                <span className="text-muted-lt">{t("detail.dialog.fieldSource")}</span>
                <div className="mt-0.5">
                  <Badge variant={sourceVariant(state.order.source)}>
                    {state.order.source}
                  </Badge>
                </div>
              </div>
              <div className="col-span-3">
                <span className="text-muted-lt">{t("detail.dialog.fieldSubmitted")}</span>
                <div className="nums mt-0.5 text-muted-lt">{formatDateTime(state.order.at)}</div>
              </div>
              {state.order.displayPrices.length > 0 && (
                <div className="col-span-3">
                  <span className="text-muted-lt">{t("detail.dialog.fieldDisplayPrices")}</span>
                  <div className="nums mt-0.5 text-text">
                    {state.order.displayPrices.join(", ")}
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
                <p className="text-xs text-muted-lt">{t("detail.dialog.timeline.empty")}</p>
              ) : (
                <ol className="space-y-2">
                  {state.events.map((ev) => {
                    const blockReason = accountBlockReason(ev);
                    return (
                      <li
                        key={ev.externalId}
                        className="flex gap-3 rounded-card border border-border bg-surface-2 p-2.5 text-xs"
                      >
                      <div className="w-32 shrink-0">
                        <div className="nums text-muted-lt">{formatDateTime(ev.at)}</div>
                      </div>
                      <div className="flex flex-1 flex-wrap items-start gap-x-3 gap-y-1">
                        <Badge variant={eventTypeVariant(ev.type)}>{ev.type}</Badge>
                        <Badge variant={sourceVariant(ev.source)}>{ev.source}</Badge>
                        {ev.principal && (
                          <span className="text-muted-lt">
                            {t("detail.dialog.timeline.by", { principal: ev.principal })}
                          </span>
                        )}
                        {/* Fill payload */}
                        {ev.fillQuantity !== undefined && (
                          <span className="text-text">
                            {t("detail.dialog.timeline.fillQty", { qty: ev.fillQuantity })}
                            {ev.fillPrice !== undefined && (
                              <>{" "}{t("detail.dialog.timeline.fillAt", { price: ev.fillPrice })}</>
                            )}
                            {ev.fillLockPrice !== undefined && (
                              <span className="text-muted-lt">
                                {" "}{t("detail.dialog.timeline.fillLock", { price: ev.fillLockPrice })}
                              </span>
                            )}
                          </span>
                        )}
                        {/* Reject payload */}
                        {blockReason === null &&
                          ev.rejectCode !== undefined && (
                          <span className="text-[var(--danger)]">
                            {t("detail.dialog.timeline.rejectCode", { code: ev.rejectCode })}
                          </span>
                        )}
                        {blockReason === null &&
                          ev.rejectScope !== undefined && (
                          <span className="text-muted-lt">
                            {t("detail.dialog.timeline.rejectScope", { scope: ev.rejectScope })}
                          </span>
                        )}
                        {blockReason === null &&
                          ev.rejectPolicy !== undefined && (
                          <span className="text-muted-lt">
                            {t("detail.dialog.timeline.rejectPolicy", { policy: ev.rejectPolicy })}
                          </span>
                        )}
                        {blockReason === null &&
                          ev.rejectReason !== undefined && (
                          <span className="text-muted-lt">{ev.rejectReason}</span>
                        )}
                        {blockReason === null &&
                          ev.rejectDetails !== undefined && (
                          <span className="text-muted-lt">{ev.rejectDetails}</span>
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
                      {/* Re-issue the engine action this event recorded:
                          a submission clones the order, a fill clones the report. */}
                      {ev.type === "submitted" && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="shrink-0 self-start"
                          aria-label={t("clone.orderAriaLabel", { orderExternalId })}
                          onClick={() => {
                            onCloneOrder({
                              account: state.order.account,
                              baseAsset: state.order.baseAsset,
                              quoteAsset: state.order.quoteAsset,
                              side: state.order.side,
                              amountKind: state.order.amountKind,
                              amountValue: state.order.amountValue,
                              price: state.order.price === "0" ? "" : state.order.price,
                            });
                            onClose();
                          }}
                        >
                          <Copy className="h-3.5 w-3.5" />
                        </Button>
                      )}
                      {ev.type === "fill" && ev.fillQuantity !== undefined && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="shrink-0 self-start"
                          aria-label={t("clone.execReportEventAriaLabel", { orderExternalId })}
                          onClick={() =>
                            onCloneExecReport(orderExternalId, {
                              quantity: ev.fillQuantity ?? "",
                              price: ev.fillPrice ?? "",
                              lockPrice: ev.fillLockPrice ?? "",
                            })
                          }
                        >
                          <Copy className="h-3.5 w-3.5" />
                        </Button>
                      )}
                      </li>
                    );
                  })}
                </ol>
              )}
            </div>

            {/* Trades — always shown so the order→trades grouping is clear */}
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
                        <TableHead>{t("table.externalId")}</TableHead>
                        <TableHead>{t("table.qty")}</TableHead>
                        <TableHead>{t("table.price")}</TableHead>
                        <TableHead>{t("table.lockPrice")}</TableHead>
                        <TableHead>{t("table.source")}</TableHead>
                        <TableHead>{t("table.time")}</TableHead>
                        <TableHead />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {state.trades.map((trade) => (
                        <TableRow key={trade.externalId} className="hover:bg-transparent">
                          <TableCell className="nums text-xs text-muted-lt">
                            {trade.externalId}
                          </TableCell>
                          <TableCell className="nums text-xs">{trade.quantity}</TableCell>
                          <TableCell className="nums text-xs">{trade.price}</TableCell>
                          <TableCell className="nums text-xs text-muted-lt">
                            {trade.lockPrice || tc("value.none")}
                          </TableCell>
                          <TableCell>
                            <Badge variant={sourceVariant(trade.source)}>{trade.source}</Badge>
                          </TableCell>
                          <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
                            {formatDateTime(trade.at)}
                          </TableCell>
                          <TableCell>
                            <Button
                              variant="ghost"
                              size="sm"
                              aria-label={t("clone.execReportAriaLabel", { tradeId: trade.externalId })}
                              onClick={() =>
                                onCloneExecReport(orderExternalId, {
                                  quantity: trade.quantity,
                                  price: trade.price,
                                  lockPrice: trade.lockPrice,
                                })
                              }
                            >
                              <Copy className="h-3.5 w-3.5" />
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                </Table>
              )}
            </div>

            <DialogFooter>
              {state.approval !== null && (
                <Button
                  variant="outline"
                  size="sm"
                  className="sm:mr-auto"
                  onClick={() => setSignatureOpen(true)}
                >
                  <ShieldCheck className="h-3.5 w-3.5" />
                  {t("detail.dialog.signedPayload.openButton")}
                </Button>
              )}
              <Button
                variant="outline"
                size="sm"
                onClick={() => onExecReport(orderExternalId, execReportInitialValuesFromOrder(state.order))}
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
                      price: state.order.price === "0" ? "" : state.order.price,
                    });
                    onClose();
                  }}
                >
                  <Copy className="h-3.5 w-3.5" />
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

      {state.phase === "ready" && state.approval !== null && (
        <SignedPayloadDialog
          approval={state.approval}
          open={signatureOpen}
          onOpenChange={setSignatureOpen}
        />
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Orders table
// ---------------------------------------------------------------------------

interface OrdersTableProps {
  orders: Order[];
  onRowClick: (order: Order) => void;
  onClone: (values: OrderInitialValues) => void;
}

function OrdersTable({ orders, onRowClick, onClone }: OrdersTableProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();

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
            <TableHead>{t("table.externalId")}</TableHead>
            <TableHead>{t("table.account")}</TableHead>
            <TableHead>{t("table.instrument")}</TableHead>
            <TableHead>{t("table.side")}</TableHead>
            <TableHead>{t("table.amount")}</TableHead>
            <TableHead>{t("table.price")}</TableHead>
            <TableHead>{t("table.displayPrices")}</TableHead>
            <TableHead>{t("table.status")}</TableHead>
            <TableHead>{t("table.source")}</TableHead>
            <TableHead>{t("table.time")}</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {orders.map((order) => (
            <TableRow
              key={order.externalId}
              className="cursor-pointer"
              onClick={() => onRowClick(order)}
            >
              <TableCell className="nums text-xs text-muted-lt">
                {order.externalId}
              </TableCell>
              <TableCell className="nums text-xs">{order.account}</TableCell>
              <TableCell className="text-xs">
                {instrument(order.baseAsset, order.quoteAsset)}
              </TableCell>
              <TableCell>
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
                {order.displayPrices.length > 0
                  ? order.displayPrices.join(", ")
                  : tc("value.none")}
              </TableCell>
              <TableCell>
                <Badge variant={statusVariant(order.status)}>{order.status}</Badge>
              </TableCell>
              <TableCell>
                <Badge variant={sourceVariant(order.source)}>{order.source}</Badge>
              </TableCell>
              <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
                {formatDateTime(order.at)}
              </TableCell>
              <TableCell>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={t("clone.orderAriaLabel", { orderExternalId: order.externalId })}
                  onClick={(e) => {
                    e.stopPropagation();
                    onClone({
                      account: order.account,
                      baseAsset: order.baseAsset,
                      quoteAsset: order.quoteAsset,
                      side: order.side,
                      amountKind: order.amountKind,
                      amountValue: order.amountValue,
                      price: order.price === "0" ? "" : order.price,
                    });
                  }}
                >
                  <Copy className="h-3.5 w-3.5" />
                </Button>
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
  onOrderClick: (orderExternalId: string) => void;
  onCloneExecReport: (orderExternalId: string, values: ExecReportInitialValues) => void;
}

function TradesTable({ trades, onOrderClick, onCloneExecReport }: TradesTableProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();

  return (
    <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("table.externalId")}</TableHead>
            <TableHead>{t("table.order")}</TableHead>
            <TableHead>{t("table.account")}</TableHead>
            <TableHead>{t("table.instrument")}</TableHead>
            <TableHead>{t("table.side")}</TableHead>
            <TableHead>{t("table.qty")}</TableHead>
            <TableHead>{t("table.price")}</TableHead>
            <TableHead>{t("table.lockPrice")}</TableHead>
            <TableHead>{t("table.source")}</TableHead>
            <TableHead>{t("table.time")}</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {trades.map((trade) => (
            <TableRow key={trade.externalId} className="hover:bg-transparent">
              <TableCell className="nums text-xs text-muted-lt">
                {trade.externalId}
              </TableCell>
              <TableCell>
                <button
                  type="button"
                  className="nums text-xs text-accent underline-offset-2 hover:underline"
                  onClick={() => onOrderClick(trade.order)}
                >
                  {trade.order}
                </button>
              </TableCell>
              <TableCell className="nums text-xs">{trade.account}</TableCell>
              <TableCell className="text-xs">
                {instrument(trade.baseAsset, trade.quoteAsset)}
              </TableCell>
              <TableCell>
                <Badge variant={trade.side === "buy" ? "buy" : "sell"}>
                  {trade.side}
                </Badge>
              </TableCell>
              <TableCell className="nums text-xs">{trade.quantity}</TableCell>
              <TableCell className="nums text-xs">{trade.price}</TableCell>
              <TableCell className="nums text-xs text-muted-lt">
                {trade.lockPrice || tc("value.none")}
              </TableCell>
              <TableCell>
                <Badge variant={sourceVariant(trade.source)}>{trade.source}</Badge>
              </TableCell>
              <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
                {formatDateTime(trade.at)}
              </TableCell>
              <TableCell>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={t("clone.execReportAriaLabel", { tradeId: trade.externalId })}
                  onClick={() =>
                    onCloneExecReport(trade.order, {
                      quantity: trade.quantity,
                      price: trade.price,
                      lockPrice: trade.lockPrice,
                    })
                  }
                >
                  <Copy className="h-3.5 w-3.5" />
                </Button>
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

function normalizeSourceFilter(value: string): Source | undefined {
  const trimmed = value.trim();
  if (trimmed === "" || trimmed === "_all") {
    return undefined;
  }
  return trimmed as Source;
}

interface FilterBarProps {
  account: string;
  source: string;
  accountSuggestions: string[];
  onAccount: (v: string) => void;
  onSource: (v: string) => void;
}

function FilterBar({
  account,
  source,
  accountSuggestions,
  onAccount,
  onSource,
}: FilterBarProps) {
  const { t } = useTranslation("orders");

  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="w-48">
        <Autocomplete
          value={account}
          onChange={onAccount}
          suggestions={accountSuggestions}
          placeholder={t("filter.accountPlaceholder")}
          className="h-8 text-xs"
        />
      </div>
      <Select value={source || "_all"} onValueChange={(v) => onSource(v === "_all" ? "" : v)}>
        <SelectTrigger className="h-8 w-32 text-xs" aria-label={t("filter.sourceAriaLabel")}>
          <SelectValue placeholder={t("filter.sourceAll")} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="_all">{t("filter.sourceAll")}</SelectItem>
          {SOURCES.filter(Boolean).map((s) => (
            <SelectItem key={s} value={s}>
              {s}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

type TabId = "orders" | "trades";

export function Orders() {
  const { t } = useTranslation("orders");

  const [params] = useSearchParams();
  const [tab, setTab] = useState<TabId>("orders");

  // Filter state — seeded from URL on mount
  const [orderAccount, setOrderAccount] = useState(params.get("account") ?? "");
  const [orderSource, setOrderSource] = useState(params.get("source") ?? "");
  const [orderSize, setOrderSize] = usePersistentPageSize(
    "pit-officer-orders-page-size",
  );
  const [orderPage, setOrderPage] = useState(0);

  const [tradeAccount, setTradeAccount] = useState(params.get("account") ?? "");
  const [tradeSource, setTradeSource] = useState(params.get("source") ?? "");
  const [tradeSize, setTradeSize] = usePersistentPageSize(
    "pit-officer-trades-page-size",
  );
  const [tradePage, setTradePage] = useState(0);
  const normalizedOrderSource = normalizeSourceFilter(orderSource);
  const normalizedTradeSource = normalizeSourceFilter(tradeSource);

  // Data
  const ordersResult = useOrders(
    orderAccount || undefined,
    normalizedOrderSource,
    pageFetchLimit(orderPage, orderSize),
  );
  const tradesResult = useTrades(
    tradeAccount || undefined,
    normalizedTradeSource,
    pageFetchLimit(tradePage, tradeSize),
  );

  // Balances — unfiltered, for seeding asset suggestions before any orders exist.
  const balancesResult = useBalances();

  // Account suggestions — union of accounts seen in orders + trades
  const accountSuggestions = useMemo(() => {
    const set = new Set<string>();
    if (ordersResult.load.state === "ready") {
      for (const o of ordersResult.load.data) {
        if (o.account) {
          set.add(o.account);
        }
      }
    }
    if (tradesResult.load.state === "ready") {
      for (const tr of tradesResult.load.data) {
        if (tr.account) {
          set.add(tr.account);
        }
      }
    }
    return Array.from(set).sort();
  }, [ordersResult.load, tradesResult.load]);

  // Also fetch accounts for the submit dialog
  const [fetchedAccounts, setFetchedAccounts] = useState<string[]>([]);
  useEffect(() => {
    fetchAccounts()
      .then((accs) => setFetchedAccounts(accs.map((a) => a.code)))
      .catch(() => { /* ignore */ });
  }, []);

  const allAccountSuggestions = useMemo(
    () => Array.from(new Set([...accountSuggestions, ...fetchedAccounts])).sort(),
    [accountSuggestions, fetchedAccounts],
  );

  // Asset suggestions from orders + trades + balances (fallback when feeds empty).
  const assetSuggestions = useMemo(() => {
    const orders = ordersResult.load.state === "ready" ? ordersResult.load.data : [];
    const trades = tradesResult.load.state === "ready" ? tradesResult.load.data : [];
    const balances = balancesResult.load.state === "ready" ? balancesResult.load.data : [];
    return collectAssets(orders, trades, balances);
  }, [ordersResult.load, tradesResult.load, balancesResult.load]);

  // Dialog state
  const [submitOpen, setSubmitOpen] = useState(false);
  const [submitInitialValues, setSubmitInitialValues] = useState<OrderInitialValues | undefined>(undefined);
  const [detailOrderExternalId, setDetailOrderExternalId] = useState<string | null>(null);
  const [detailSuccessBanner, setDetailSuccessBanner] = useState<string | undefined>(undefined);
  const [execReportOrderExternalId, setExecReportOrderExternalId] = useState<string | null>(null);
  const [execReportInitialValues, setExecReportInitialValues] = useState<ExecReportInitialValues | undefined>(undefined);

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

  function openExecReport(externalId: string, values?: ExecReportInitialValues) {
    setDetailOrderExternalId(null);
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

  function openCloneExecReport(orderExternalId: string, values: ExecReportInitialValues) {
    setDetailOrderExternalId(null);
    setExecReportInitialValues(values);
    setExecReportOrderExternalId(orderExternalId);
  }

  const activeLoad = tab === "orders" ? ordersResult : tradesResult;
  const activeReload = tab === "orders" ? ordersResult.reload : tradesResult.reload;
  const orderRows =
    ordersResult.load.state === "ready" ? ordersResult.load.data : [];
  const tradeRows =
    tradesResult.load.state === "ready" ? tradesResult.load.data : [];
  const pagedOrders = slicePage(orderRows, orderPage, orderSize);
  const pagedTrades = slicePage(tradeRows, tradePage, tradeSize);
  const hasMoreOrders = hasNextPage(orderRows, orderPage, orderSize);
  const hasMoreTrades = hasNextPage(tradeRows, tradePage, tradeSize);
  const orderPager = (
    <TablePagination
      page={orderPage}
      canPrevious={orderPage > 0}
      canNext={hasMoreOrders}
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
      onPrevious={() => setTradePage((p) => Math.max(0, p - 1))}
      onNext={() => setTradePage((p) => p + 1)}
      onPage={setTradePage}
    />
  );

  // When the active tab is filtered to a single account, opening "Add order"
  // pre-fills that account; with no account filter, the form opens blank.
  const activeAccountFilter = (tab === "orders" ? orderAccount : tradeAccount).trim();
  const activeSourceFilter =
    tab === "orders" ? normalizedOrderSource : normalizedTradeSource;
  const activeExportFilters = {
    ...(activeAccountFilter ? { account: activeAccountFilter } : {}),
    ...(activeSourceFilter ? { source: activeSourceFilter } : {}),
  };

  function openAddOrder() {
    setSubmitInitialValues(
      activeAccountFilter
        ? {
            account: activeAccountFilter,
            baseAsset: "",
            quoteAsset: "",
            side: "buy",
            amountKind: "quantity",
            amountValue: "",
            price: "",
          }
        : undefined,
    );
    setSubmitOpen(true);
  }

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
                setTradePage(0);
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
          <Button size="sm" onClick={openAddOrder}>
            <Plus className="h-3.5 w-3.5" />
            {t("addOrder.button")}
          </Button>
        </div>
      }
    >
      <p className="text-xs text-muted-lt">
        {t("subtitle")}
      </p>

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
      {tab === "orders" ? (
        <FilterBar
          account={orderAccount}
          source={orderSource}
          accountSuggestions={allAccountSuggestions}
          onAccount={(value) => {
            setOrderAccount(value);
            setOrderPage(0);
          }}
          onSource={(value) => {
            setOrderSource(value);
            setOrderPage(0);
          }}
        />
      ) : (
        <FilterBar
          account={tradeAccount}
          source={tradeSource}
          accountSuggestions={allAccountSuggestions}
          onAccount={(value) => {
            setTradeAccount(value);
            setTradePage(0);
          }}
          onSource={(value) => {
            setTradeSource(value);
            setTradePage(0);
          }}
        />
      )}

      {/* Orders view */}
      {tab === "orders" && (
        <>
          {ordersResult.load.state === "loading" && <TableSkeleton cols={10} />}
          {ordersResult.load.state === "error" && (
            <ErrorState
              message={ordersResult.load.error}
              onRetry={ordersResult.reload}
            />
          )}
          {ordersResult.load.state === "ready" &&
            (ordersResult.load.data.length === 0 ? (
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
                  onRowClick={(o) => openDetail(o.externalId)}
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
          {tradesResult.load.state === "error" && (
            <ErrorState
              message={tradesResult.load.error}
              onRetry={tradesResult.reload}
            />
          )}
          {tradesResult.load.state === "ready" &&
            (tradesResult.load.data.length === 0 ? (
              <EmptyState
                title={t("empty.trades.title")}
                hint={t("empty.trades.hint")}
              />
            ) : (
              <>
                {tradePager}
                <TradesTable
                  trades={pagedTrades}
                  onOrderClick={openDetail}
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
        onClose={() => { setSubmitOpen(false); setSubmitInitialValues(undefined); }}
        onCreated={ordersResult.reload}
        onOpenDetail={(id, banner) => openDetailWithBanner(
          id,
          banner ?? t("addOrder.added"),
        )}
        accountSuggestions={allAccountSuggestions}
        assetSuggestions={assetSuggestions}
        initialValues={submitInitialValues}
      />

      <OrderDetailDialog
        orderExternalId={detailOrderExternalId}
        onClose={closeDetail}
        onExecReport={openExecReport}
        onCloneOrder={openCloneOrder}
        onCloneExecReport={openCloneExecReport}
        successBanner={detailSuccessBanner}
      />

      <ExecReportDialog
        orderExternalId={execReportOrderExternalId}
        onClose={closeExecReport}
        onSubmitted={() => {
          ordersResult.reload();
          tradesResult.reload();
        }}
        initialValues={execReportInitialValues}
      />
    </Page>
  );
}
