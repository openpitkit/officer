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
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Copy, Plus } from "lucide-react";

import {
  ApiError,
  checkOrder,
  createOrder,
  fetchAccounts,
  fetchOrderDetail,
  submitExecutionReport,
} from "@/api/client";
import type { Balance, CheckResult, Order, OrderEvent, Source, Trade } from "@/api/types";
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
import { Badge, type BadgeProps } from "@/components/ui/badge";
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
      {result.wouldLockPrices.length > 0 && (
        <div className="text-muted-lt">
          {t("check.lockPrices", { prices: result.wouldLockPrices.join(", ") })}
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
  onOpenDetail: (id: number) => void;
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

  const [account, setAccount] = useState(initialValues?.account ?? "");
  const [baseAsset, setBaseAsset] = useState(initialValues?.baseAsset ?? "");
  const [quoteAsset, setQuoteAsset] = useState(initialValues?.quoteAsset ?? "");
  const [side, setSide] = useState<string>(initialValues?.side ?? "buy");
  const [amountKind, setAmountKind] = useState<string>(initialValues?.amountKind ?? "quantity");
  const [amountValue, setAmountValue] = useState(initialValues?.amountValue ?? "");
  const [price, setPrice] = useState(initialValues?.price ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [checkState, setCheckState] = useState<CheckState>({ phase: "idle" });
  const checkAbortRef = useRef<AbortController | null>(null);

  // Debounced live check: fires ~350ms after any form field changes.
  useEffect(() => {
    const accountT = account.trim();
    const baseT = baseAsset.trim();
    const quoteT = quoteAsset.trim();
    const amountT = amountValue.trim();
    // Skip when required fields are absent.
    if (!accountT || !baseT || !quoteT || !amountT) {
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
      setSide(initialValues?.side ?? "buy");
      setAmountKind(initialValues?.amountKind ?? "quantity");
      setAmountValue(initialValues?.amountValue ?? "");
      setPrice(initialValues?.price ?? "");
      setBusy(false);
      setError(null);
      setCheckState({ phase: "idle" });
      checkAbortRef.current?.abort();
      checkAbortRef.current = null;
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  function reset() {
    setAccount(initialValues?.account ?? "");
    setBaseAsset(initialValues?.baseAsset ?? "");
    setQuoteAsset(initialValues?.quoteAsset ?? "");
    setSide(initialValues?.side ?? "buy");
    setAmountKind(initialValues?.amountKind ?? "quantity");
    setAmountValue(initialValues?.amountValue ?? "");
    setPrice(initialValues?.price ?? "");
    setBusy(false);
    setError(null);
    setCheckState({ phase: "idle" });
    checkAbortRef.current?.abort();
    checkAbortRef.current = null;
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
    setBusy(true);
    setError(null);
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
      const order = await createOrder(body);
      onCreated();
      reset();
      onClose();
      onOpenDetail(order.id);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  }

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
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="so-side">{t("addOrder.dialog.side")}</Label>
              <Select value={side} onValueChange={setSide} disabled={busy}>
                <SelectTrigger id="so-side" className="h-9 text-sm">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="buy">{t("addOrder.dialog.sideOptions.buy")}</SelectItem>
                  <SelectItem value="sell">{t("addOrder.dialog.sideOptions.sell")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="so-kind">{t("addOrder.dialog.amountKind")}</Label>
              <Select value={amountKind} onValueChange={setAmountKind} disabled={busy}>
                <SelectTrigger id="so-kind" className="h-9 text-sm">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="quantity">{t("addOrder.dialog.amountKindOptions.quantity")}</SelectItem>
                  <SelectItem value="volume">{t("addOrder.dialog.amountKindOptions.volume")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="so-amount">{t("addOrder.dialog.amount")}</Label>
              <Input
                id="so-amount"
                value={amountValue}
                onChange={(e) => setAmountValue(e.target.value)}
                placeholder={t("addOrder.dialog.amountPlaceholder")}
                disabled={busy}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="so-price">{t("addOrder.dialog.limitPrice")}</Label>
              <Input
                id="so-price"
                value={price}
                onChange={(e) => setPrice(e.target.value)}
                placeholder={t("addOrder.dialog.limitPricePlaceholder")}
                disabled={busy}
              />
            </div>
          </div>

          <CheckPreview state={checkState} />

          {error && (
            <ErrorBanner message={error} onDismiss={() => setError(null)} />
          )}

          <DialogFooter>
            <Button variant="outline" size="sm" onClick={handleClose} disabled={busy}>
              {tc("actions.cancel")}
            </Button>
            <Button size="sm" onClick={submit} disabled={busy}>
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

interface ExecReportDialogProps {
  orderId: number | null;
  onClose: () => void;
  onSubmitted: () => void;
  /** Pre-seed all input fields (clone path). */
  initialValues?: ExecReportInitialValues;
}

function ExecReportDialog({ orderId, onClose, onSubmitted, initialValues }: ExecReportDialogProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();

  const [quantity, setQuantity] = useState(initialValues?.quantity ?? "");
  const [price, setPrice] = useState(initialValues?.price ?? "");
  const [lockPrice, setLockPrice] = useState(initialValues?.lockPrice ?? "");
  const [final, setFinal] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  // Reseed from initialValues whenever the dialog opens (clone path).
  useEffect(() => {
    if (orderId !== null) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setQuantity(initialValues?.quantity ?? "");
      setPrice(initialValues?.price ?? "");
      setLockPrice(initialValues?.lockPrice ?? "");
      setFinal(true);
      setBusy(false);
      setError(null);
      setDone(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orderId]);

  function reset() {
    setQuantity(initialValues?.quantity ?? "");
    setPrice(initialValues?.price ?? "");
    setLockPrice(initialValues?.lockPrice ?? "");
    setFinal(true);
    setBusy(false);
    setError(null);
    setDone(false);
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
    if (orderId === null) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const body: {
        quantity: string;
        price: string;
        lockPrice?: string;
        final: boolean;
      } = {
        quantity: quantity.trim(),
        price: price.trim(),
        final,
      };
      if (lockPrice.trim()) {
        body.lockPrice = lockPrice.trim();
      }
      await submitExecutionReport(orderId, body);
      setDone(true);
      onSubmitted();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={orderId !== null} onOpenChange={(v) => { if (!v) handleClose(); }}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{t("execReport.dialog.title")}</DialogTitle>
          <DialogDescription>
            {t("execReport.dialog.description", { orderId })}
          </DialogDescription>
        </DialogHeader>

        {done ? (
          <div className="space-y-3">
            <p className="text-xs text-[var(--ok)]">{t("execReport.dialog.accepted")}</p>
            <DialogFooter>
              <Button size="sm" onClick={handleClose}>{t("execReport.dialog.done")}</Button>
            </DialogFooter>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="er-qty">{t("execReport.dialog.fillQty")}</Label>
                <Input
                  id="er-qty"
                  value={quantity}
                  onChange={(e) => setQuantity(e.target.value)}
                  placeholder={t("execReport.dialog.fillQtyPlaceholder")}
                  disabled={busy}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="er-price">{t("execReport.dialog.fillPrice")}</Label>
                <Input
                  id="er-price"
                  value={price}
                  onChange={(e) => setPrice(e.target.value)}
                  placeholder={t("execReport.dialog.fillPricePlaceholder")}
                  disabled={busy}
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="er-lock">{t("execReport.dialog.lockPrice")}</Label>
              <Input
                id="er-lock"
                value={lockPrice}
                onChange={(e) => setLockPrice(e.target.value)}
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
// Order Detail dialog — header + event timeline + trades
// ---------------------------------------------------------------------------

interface OrderDetailDialogProps {
  orderId: number | null;
  onClose: () => void;
  onExecReport: (orderId: number) => void;
  onCloneOrder: (values: OrderInitialValues) => void;
  onCloneExecReport: (orderId: number, values: ExecReportInitialValues) => void;
  successBanner?: string;
}

type DetailState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; order: Order; events: OrderEvent[]; trades: Trade[] };

function OrderDetailDialog({ orderId, onClose, onExecReport, onCloneOrder, onCloneExecReport, successBanner }: OrderDetailDialogProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();

  const [state, setState] = useState<DetailState>({ phase: "loading" });

  useEffect(() => {
    if (orderId === null) {
      return;
    }
    // Show the loading state before the detail fetch starts; intentional.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setState({ phase: "loading" });
    const controller = new AbortController();
    fetchOrderDetail(orderId, controller.signal)
      .then(({ order, events, trades }) => {
        if (!controller.signal.aborted) {
          setState({ phase: "ready", order, events, trades });
        }
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ phase: "error", message: errMessage(err) });
        }
      });
    return () => controller.abort();
  }, [orderId]);

  if (orderId === null) {
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
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("detail.dialog.title", { orderId })}</DialogTitle>
          {state.phase === "ready" && (
            <DialogDescription>
              {instrument(state.order.baseAsset, state.order.quoteAsset)}
              {" · "}
              <span className="capitalize">{state.order.side}</span>
              {" · "}
              {amountLabel(state.order.amountKind, state.order.amountValue)}
              {" · "}
              {priceLabel(state.order.price)}
            </DialogDescription>
          )}
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
              {state.order.lockPrices.length > 0 && (
                <div className="col-span-3">
                  <span className="text-muted-lt">{t("detail.dialog.fieldLockPrices")}</span>
                  <div className="nums mt-0.5 text-text">
                    {state.order.lockPrices.join(", ")}
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
                  {state.events.map((ev) => (
                    <li
                      key={ev.id}
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
                        {ev.rejectCode !== undefined && (
                          <span className="text-[var(--danger)]">
                            {t("detail.dialog.timeline.rejectCode", { code: ev.rejectCode })}
                          </span>
                        )}
                        {ev.rejectScope !== undefined && (
                          <span className="text-muted-lt">
                            {t("detail.dialog.timeline.rejectScope", { scope: ev.rejectScope })}
                          </span>
                        )}
                        {ev.rejectPolicy !== undefined && (
                          <span className="text-muted-lt">
                            {t("detail.dialog.timeline.rejectPolicy", { policy: ev.rejectPolicy })}
                          </span>
                        )}
                        {ev.rejectReason !== undefined && (
                          <span className="text-muted-lt">{ev.rejectReason}</span>
                        )}
                        {ev.rejectDetails !== undefined && (
                          <span className="text-muted-lt">{ev.rejectDetails}</span>
                        )}
                      </div>
                    </li>
                  ))}
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
                <Card>
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead>{t("table.id")}</TableHead>
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
                        <TableRow key={trade.id} className="hover:bg-transparent">
                          <TableCell className="nums text-xs text-muted-lt">
                            #{trade.id}
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
                              aria-label={t("clone.execReportAriaLabel", { tradeId: trade.id })}
                              onClick={() =>
                                onCloneExecReport(orderId, {
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
                </Card>
              )}
            </div>

            <DialogFooter>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onExecReport(orderId)}
              >
                {t("detail.dialog.trades.submitExecReport")}
              </Button>
              {state.phase === "ready" && (
                <Button
                  variant="outline"
                  size="sm"
                  aria-label={t("clone.orderAriaLabel", { orderId })}
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
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("table.id")}</TableHead>
            <TableHead>{t("table.account")}</TableHead>
            <TableHead>{t("table.instrument")}</TableHead>
            <TableHead>{t("table.side")}</TableHead>
            <TableHead>{t("table.amount")}</TableHead>
            <TableHead>{t("table.price")}</TableHead>
            <TableHead>{t("table.lockPrices")}</TableHead>
            <TableHead>{t("table.status")}</TableHead>
            <TableHead>{t("table.source")}</TableHead>
            <TableHead>{t("table.time")}</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {orders.map((order) => (
            <TableRow
              key={order.id}
              className="cursor-pointer"
              onClick={() => onRowClick(order)}
            >
              <TableCell className="nums text-xs text-muted-lt">
                #{order.id}
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
                {order.lockPrices.length > 0
                  ? order.lockPrices.join(", ")
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
                  aria-label={t("clone.orderAriaLabel", { orderId: order.id })}
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
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Trades table
// ---------------------------------------------------------------------------

interface TradesTableProps {
  trades: Trade[];
  onOrderClick: (orderId: number) => void;
  onCloneExecReport: (orderId: number, values: ExecReportInitialValues) => void;
}

function TradesTable({ trades, onOrderClick, onCloneExecReport }: TradesTableProps) {
  const { t } = useTranslation("orders");
  const { t: tc } = useTranslation();

  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("table.id")}</TableHead>
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
            <TableRow key={trade.id} className="hover:bg-transparent">
              <TableCell className="nums text-xs text-muted-lt">
                #{trade.id}
              </TableCell>
              <TableCell>
                <button
                  type="button"
                  className="nums text-xs text-accent underline-offset-2 hover:underline"
                  onClick={() => onOrderClick(trade.orderId)}
                >
                  #{trade.orderId}
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
                  aria-label={t("clone.execReportAriaLabel", { tradeId: trade.id })}
                  onClick={() =>
                    onCloneExecReport(trade.orderId, {
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
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Filter bar
// ---------------------------------------------------------------------------

const SOURCES = ["", "panel", "api", "mcp", "system"] as const;
const PAGE_SIZES = [50, 100, 500] as const;

interface FilterBarProps {
  account: string;
  source: string;
  size: number;
  accountSuggestions: string[];
  onAccount: (v: string) => void;
  onSource: (v: string) => void;
  onSize: (v: number) => void;
}

function FilterBar({
  account,
  source,
  size,
  accountSuggestions,
  onAccount,
  onSource,
  onSize,
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
      <Select value={String(size)} onValueChange={(v) => onSize(Number(v))}>
        <SelectTrigger className="h-8 w-28 text-xs" aria-label={t("filter.sizeAriaLabel")}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {PAGE_SIZES.map((n) => (
            <SelectItem key={n} value={String(n)}>
              {t("filter.sizeRows", { count: n })}
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
  const [orderSize, setOrderSize] = useState(50);

  const [tradeAccount, setTradeAccount] = useState(params.get("account") ?? "");
  const [tradeSource, setTradeSource] = useState(params.get("source") ?? "");
  const [tradeSize, setTradeSize] = useState(50);

  // Data
  const ordersResult = useOrders(
    orderAccount || undefined,
    orderSource || undefined,
    orderSize,
  );
  const tradesResult = useTrades(
    tradeAccount || undefined,
    tradeSource || undefined,
    tradeSize,
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
      .then((accs) => setFetchedAccounts(accs.map((a) => a.id)))
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
  const [detailOrderId, setDetailOrderId] = useState<number | null>(null);
  const [detailSuccessBanner, setDetailSuccessBanner] = useState<string | undefined>(undefined);
  const [execReportOrderId, setExecReportOrderId] = useState<number | null>(null);
  const [execReportInitialValues, setExecReportInitialValues] = useState<ExecReportInitialValues | undefined>(undefined);

  function openDetail(id: number) {
    setDetailSuccessBanner(undefined);
    setDetailOrderId(id);
  }

  function openDetailWithBanner(id: number, banner: string) {
    setDetailSuccessBanner(banner);
    setDetailOrderId(id);
  }

  function closeDetail() {
    setDetailOrderId(null);
    setDetailSuccessBanner(undefined);
  }

  function openExecReport(id: number) {
    setDetailOrderId(null);
    setExecReportOrderId(id);
    setExecReportInitialValues(undefined);
  }

  function closeExecReport() {
    setExecReportOrderId(null);
    setExecReportInitialValues(undefined);
  }

  function openCloneOrder(values: OrderInitialValues) {
    setSubmitInitialValues(values);
    setSubmitOpen(true);
  }

  function openCloneExecReport(orderId: number, values: ExecReportInitialValues) {
    setDetailOrderId(null);
    setExecReportInitialValues(values);
    setExecReportOrderId(orderId);
  }

  const activeLoad = tab === "orders" ? ordersResult : tradesResult;
  const activeReload = tab === "orders" ? ordersResult.reload : tradesResult.reload;

  return (
    <Page
      title={t("title")}
      actions={
        <div className="flex items-center gap-2">
          <Button size="sm" onClick={() => { setSubmitInitialValues(undefined); setSubmitOpen(true); }}>
            <Plus className="h-3.5 w-3.5" />
            {t("addOrder.button")}
          </Button>
          <RefreshButton
            onClick={activeReload}
            busy={activeLoad.load.state === "loading"}
          />
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
          size={orderSize}
          accountSuggestions={allAccountSuggestions}
          onAccount={setOrderAccount}
          onSource={setOrderSource}
          onSize={setOrderSize}
        />
      ) : (
        <FilterBar
          account={tradeAccount}
          source={tradeSource}
          size={tradeSize}
          accountSuggestions={allAccountSuggestions}
          onAccount={setTradeAccount}
          onSource={setTradeSource}
          onSize={setTradeSize}
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
                  <Button size="sm" onClick={() => { setSubmitInitialValues(undefined); setSubmitOpen(true); }}>
                    <Plus className="h-3.5 w-3.5" />
                    {t("addOrder.button")}
                  </Button>
                }
              />
            ) : (
              <OrdersTable
                orders={ordersResult.load.data}
                onRowClick={(o) => openDetail(o.id)}
                onClone={openCloneOrder}
              />
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
              <TradesTable
                trades={tradesResult.load.data}
                onOrderClick={openDetail}
                onCloneExecReport={openCloneExecReport}
              />
            ))}
        </>
      )}

      {/* Dialogs */}
      <SubmitOrderDialog
        open={submitOpen}
        onClose={() => { setSubmitOpen(false); setSubmitInitialValues(undefined); }}
        onCreated={ordersResult.reload}
        onOpenDetail={(id) => openDetailWithBanner(id, t("addOrder.added"))}
        accountSuggestions={allAccountSuggestions}
        assetSuggestions={assetSuggestions}
        initialValues={submitInitialValues}
      />

      <OrderDetailDialog
        orderId={detailOrderId}
        onClose={closeDetail}
        onExecReport={openExecReport}
        onCloneOrder={openCloneOrder}
        onCloneExecReport={openCloneExecReport}
        successBanner={detailSuccessBanner}
      />

      <ExecReportDialog
        orderId={execReportOrderId}
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
