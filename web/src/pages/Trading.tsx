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
import { useSearchParams } from "react-router-dom";
import { Plus } from "lucide-react";

import {
  ApiError,
  createOrder,
  fetchAccounts,
  fetchOrderDetail,
  submitExecutionReport,
} from "@/api/client";
import type { Balance, Order, OrderEvent, Source, Trade } from "@/api/types";
import { useBalances } from "@/api/useBalances";
import { useOrders } from "@/api/useOrders";
import { useTrades } from "@/api/useTrades";
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

/** Render an ISO timestamp as a compact UTC string. */
function formatUtc(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return iso;
  }
  return date.toISOString().replace("T", " ").replace("Z", " UTC");
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

/** Amount label: "100 qty" or "500 vol". */
function amountLabel(kind: string, value: string): string {
  const tag = kind === "base" ? "qty" : "vol";
  return `${value} ${tag}`;
}

/** Price display: empty string or "0" → "market". */
function priceLabel(price: string): string {
  if (!price || price === "0") {
    return "market";
  }
  return price;
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
// Submit Order dialog
// ---------------------------------------------------------------------------

interface SubmitOrderDialogProps {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
  accountSuggestions: string[];
  assetSuggestions: string[];
}

function SubmitOrderDialog({
  open,
  onClose,
  onCreated,
  accountSuggestions,
  assetSuggestions,
}: SubmitOrderDialogProps) {
  const [account, setAccount] = useState("");
  const [baseAsset, setBaseAsset] = useState("");
  const [quoteAsset, setQuoteAsset] = useState("");
  const [side, setSide] = useState<string>("buy");
  const [amountKind, setAmountKind] = useState<string>("base");
  const [amountValue, setAmountValue] = useState("");
  const [price, setPrice] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<Order | null>(null);

  function reset() {
    setAccount("");
    setBaseAsset("");
    setQuoteAsset("");
    setSide("buy");
    setAmountKind("base");
    setAmountValue("");
    setPrice("");
    setBusy(false);
    setError(null);
    setResult(null);
  }

  function handleClose() {
    reset();
    onClose();
  }

  async function submit() {
    if (!account.trim() || !baseAsset.trim() || !quoteAsset.trim() || !amountValue.trim()) {
      setError("Account, instrument, and amount are required.");
      return;
    }
    setBusy(true);
    setError(null);
    setResult(null);
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
      setResult(order);
      onCreated();
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
          <DialogTitle>Submit order</DialogTitle>
          <DialogDescription>
            Imitate an order from the panel source. Market orders (no price) are
            forwarded to the engine and may come back rejected.
          </DialogDescription>
        </DialogHeader>

        {result ? (
          <div className="space-y-3">
            <div className="rounded-card border border-border bg-bg p-3 space-y-2 text-xs">
              <div className="flex items-center gap-2">
                <span className="text-muted-lt">Order</span>
                <span className="nums font-medium text-text">#{result.id}</span>
                <Badge variant={statusVariant(result.status)}>{result.status}</Badge>
              </div>
              <div className="flex gap-4">
                <span className="text-muted-lt">Instrument</span>
                <span className="text-text">{instrument(result.baseAsset, result.quoteAsset)}</span>
              </div>
              <div className="flex gap-4">
                <span className="text-muted-lt">Side</span>
                <span className="text-text capitalize">{result.side}</span>
              </div>
              <div className="flex gap-4">
                <span className="text-muted-lt">Amount</span>
                <span className="text-text">{amountLabel(result.amountKind, result.amountValue)}</span>
              </div>
              <div className="flex gap-4">
                <span className="text-muted-lt">Price</span>
                <span className="text-text">{priceLabel(result.price)}</span>
              </div>
            </div>
            <DialogFooter>
              <Button size="sm" onClick={handleClose}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="so-account">Account</Label>
              <Autocomplete
                id="so-account"
                value={account}
                onChange={setAccount}
                suggestions={accountSuggestions}
                placeholder="e.g. desk-alpha"
                disabled={busy}
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="so-base">Base asset</Label>
                <Autocomplete
                  id="so-base"
                  value={baseAsset}
                  onChange={setBaseAsset}
                  suggestions={assetSuggestions}
                  placeholder="e.g. AAPL"
                  disabled={busy}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="so-quote">Quote asset</Label>
                <Autocomplete
                  id="so-quote"
                  value={quoteAsset}
                  onChange={setQuoteAsset}
                  suggestions={assetSuggestions}
                  placeholder="e.g. USD"
                  disabled={busy}
                />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="so-side">Side</Label>
                <Select value={side} onValueChange={setSide} disabled={busy}>
                  <SelectTrigger id="so-side" className="h-9 text-sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="buy">Buy</SelectItem>
                    <SelectItem value="sell">Sell</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="so-kind">Amount kind</Label>
                <Select value={amountKind} onValueChange={setAmountKind} disabled={busy}>
                  <SelectTrigger id="so-kind" className="h-9 text-sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="base">Quantity (base)</SelectItem>
                    <SelectItem value="quote">Volume (quote)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="so-amount">Amount</Label>
                <Input
                  id="so-amount"
                  value={amountValue}
                  onChange={(e) => setAmountValue(e.target.value)}
                  placeholder="e.g. 100"
                  disabled={busy}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="so-price">Limit price (optional)</Label>
                <Input
                  id="so-price"
                  value={price}
                  onChange={(e) => setPrice(e.target.value)}
                  placeholder="leave empty = market"
                  disabled={busy}
                />
              </div>
            </div>

            {error && (
              <ErrorBanner message={error} onDismiss={() => setError(null)} />
            )}

            <DialogFooter>
              <Button variant="outline" size="sm" onClick={handleClose} disabled={busy}>
                Cancel
              </Button>
              <Button size="sm" onClick={submit} disabled={busy}>
                {busy ? "Submitting…" : "Submit"}
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Execution Report dialog
// ---------------------------------------------------------------------------

interface ExecReportDialogProps {
  orderId: number | null;
  onClose: () => void;
  onSubmitted: () => void;
}

function ExecReportDialog({ orderId, onClose, onSubmitted }: ExecReportDialogProps) {
  const [quantity, setQuantity] = useState("");
  const [price, setPrice] = useState("");
  const [lockPrice, setLockPrice] = useState("");
  const [final, setFinal] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  function reset() {
    setQuantity("");
    setPrice("");
    setLockPrice("");
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
      setError("Quantity and price are required.");
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
          <DialogTitle>Submit execution report</DialogTitle>
          <DialogDescription>
            Order #{orderId} — record a fill from the venue.
          </DialogDescription>
        </DialogHeader>

        {done ? (
          <div className="space-y-3">
            <p className="text-xs text-[var(--ok)]">Execution report accepted.</p>
            <DialogFooter>
              <Button size="sm" onClick={handleClose}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="er-qty">Fill quantity</Label>
                <Input
                  id="er-qty"
                  value={quantity}
                  onChange={(e) => setQuantity(e.target.value)}
                  placeholder="e.g. 50"
                  disabled={busy}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="er-price">Fill price</Label>
                <Input
                  id="er-price"
                  value={price}
                  onChange={(e) => setPrice(e.target.value)}
                  placeholder="e.g. 192.40"
                  disabled={busy}
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="er-lock">Lock price (optional)</Label>
              <Input
                id="er-lock"
                value={lockPrice}
                onChange={(e) => setLockPrice(e.target.value)}
                placeholder="optional"
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
              Final fill (closes the order)
            </label>

            {error && (
              <ErrorBanner message={error} onDismiss={() => setError(null)} />
            )}

            <DialogFooter>
              <Button variant="outline" size="sm" onClick={handleClose} disabled={busy}>
                Cancel
              </Button>
              <Button size="sm" onClick={submit} disabled={busy}>
                {busy ? "Submitting…" : "Submit"}
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
}

type DetailState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; order: Order; events: OrderEvent[]; trades: Trade[] };

function OrderDetailDialog({ orderId, onClose, onExecReport }: OrderDetailDialogProps) {
  const [state, setState] = useState<DetailState>({ phase: "loading" });

  useEffect(() => {
    if (orderId === null) {
      return;
    }
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

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Order #{orderId}</DialogTitle>
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
            <div className="grid grid-cols-3 gap-2 rounded-card border border-border bg-bg p-3 text-xs">
              <div>
                <span className="text-muted-lt">Account</span>
                <div className="nums mt-0.5 text-text">{state.order.account}</div>
              </div>
              <div>
                <span className="text-muted-lt">Status</span>
                <div className="mt-0.5">
                  <Badge variant={statusVariant(state.order.status)}>
                    {state.order.status}
                  </Badge>
                </div>
              </div>
              <div>
                <span className="text-muted-lt">Source</span>
                <div className="mt-0.5">
                  <Badge variant={sourceVariant(state.order.source)}>
                    {state.order.source}
                  </Badge>
                </div>
              </div>
              <div className="col-span-3">
                <span className="text-muted-lt">Submitted</span>
                <div className="nums mt-0.5 text-muted-lt">{formatUtc(state.order.at)}</div>
              </div>
              {Object.keys(state.order.lockPrices).length > 0 && (
                <div className="col-span-3">
                  <span className="text-muted-lt">Lock prices</span>
                  <div className="mt-0.5 flex flex-wrap gap-2">
                    {Object.entries(state.order.lockPrices).map(([asset, lp]) => (
                      <span key={asset} className="nums text-text">
                        {asset}: {lp}
                      </span>
                    ))}
                  </div>
                </div>
              )}
            </div>

            {/* Event timeline */}
            <div>
              <p className="mb-2 text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
                Event timeline
              </p>
              {state.events.length === 0 ? (
                <p className="text-xs text-muted-lt">No events recorded.</p>
              ) : (
                <ol className="space-y-2">
                  {state.events.map((ev) => (
                    <li
                      key={ev.id}
                      className="flex gap-3 rounded-card border border-border bg-bg p-2.5 text-xs"
                    >
                      <div className="w-32 shrink-0">
                        <div className="nums text-muted-lt">{formatUtc(ev.at)}</div>
                      </div>
                      <div className="flex flex-1 flex-wrap items-start gap-x-3 gap-y-1">
                        <Badge variant={eventTypeVariant(ev.type)}>{ev.type}</Badge>
                        <Badge variant={sourceVariant(ev.source)}>{ev.source}</Badge>
                        {ev.principal && (
                          <span className="text-muted-lt">
                            by <span className="text-text">{ev.principal}</span>
                          </span>
                        )}
                        {/* Fill payload */}
                        {ev.fillQuantity !== undefined && (
                          <span className="text-text">
                            qty&nbsp;{ev.fillQuantity}
                            {ev.fillPrice !== undefined && <> @ {ev.fillPrice}</>}
                            {ev.fillLockPrice !== undefined && (
                              <span className="text-muted-lt">
                                {" "}(lock&nbsp;{ev.fillLockPrice})
                              </span>
                            )}
                          </span>
                        )}
                        {/* Reject payload */}
                        {ev.rejectCode !== undefined && (
                          <span className="text-[var(--danger)]">
                            code&nbsp;{ev.rejectCode}
                          </span>
                        )}
                        {ev.rejectScope !== undefined && (
                          <span className="text-muted-lt">scope&nbsp;{ev.rejectScope}</span>
                        )}
                        {ev.rejectPolicy !== undefined && (
                          <span className="text-muted-lt">policy&nbsp;{ev.rejectPolicy}</span>
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
                Trades for this order
              </p>
              {state.trades.length === 0 ? (
                <p className="text-xs text-muted-lt">
                  No fills recorded yet. Submit an execution report to create a trade.
                </p>
              ) : (
                <Card>
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead>ID</TableHead>
                        <TableHead>Qty</TableHead>
                        <TableHead>Price</TableHead>
                        <TableHead>Lock price</TableHead>
                        <TableHead>Source</TableHead>
                        <TableHead>Time</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {state.trades.map((t) => (
                        <TableRow key={t.id} className="hover:bg-transparent">
                          <TableCell className="nums text-xs text-muted-lt">
                            #{t.id}
                          </TableCell>
                          <TableCell className="nums text-xs">{t.quantity}</TableCell>
                          <TableCell className="nums text-xs">{t.price}</TableCell>
                          <TableCell className="nums text-xs text-muted-lt">
                            {t.lockPrice || "—"}
                          </TableCell>
                          <TableCell>
                            <Badge variant={sourceVariant(t.source)}>{t.source}</Badge>
                          </TableCell>
                          <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
                            {formatUtc(t.at)}
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
                Submit execution report
              </Button>
              <Button size="sm" onClick={onClose}>
                Close
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
}

function OrdersTable({ orders, onRowClick }: OrdersTableProps) {
  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>ID</TableHead>
            <TableHead>Account</TableHead>
            <TableHead>Instrument</TableHead>
            <TableHead>Side</TableHead>
            <TableHead>Amount</TableHead>
            <TableHead>Price</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Time</TableHead>
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
                <Badge variant={order.side === "buy" ? "ok" : "danger"}>
                  {order.side}
                </Badge>
              </TableCell>
              <TableCell className="nums text-xs">
                {amountLabel(order.amountKind, order.amountValue)}
              </TableCell>
              <TableCell className="nums text-xs">
                {priceLabel(order.price)}
              </TableCell>
              <TableCell>
                <Badge variant={statusVariant(order.status)}>{order.status}</Badge>
              </TableCell>
              <TableCell>
                <Badge variant={sourceVariant(order.source)}>{order.source}</Badge>
              </TableCell>
              <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
                {formatUtc(order.at)}
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
}

function TradesTable({ trades, onOrderClick }: TradesTableProps) {
  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>ID</TableHead>
            <TableHead>Order</TableHead>
            <TableHead>Account</TableHead>
            <TableHead>Instrument</TableHead>
            <TableHead>Side</TableHead>
            <TableHead>Qty</TableHead>
            <TableHead>Price</TableHead>
            <TableHead>Lock price</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Time</TableHead>
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
                <Badge variant={trade.side === "buy" ? "ok" : "danger"}>
                  {trade.side}
                </Badge>
              </TableCell>
              <TableCell className="nums text-xs">{trade.quantity}</TableCell>
              <TableCell className="nums text-xs">{trade.price}</TableCell>
              <TableCell className="nums text-xs text-muted-lt">
                {trade.lockPrice || "—"}
              </TableCell>
              <TableCell>
                <Badge variant={sourceVariant(trade.source)}>{trade.source}</Badge>
              </TableCell>
              <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
                {formatUtc(trade.at)}
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
  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="w-48">
        <Autocomplete
          value={account}
          onChange={onAccount}
          suggestions={accountSuggestions}
          placeholder="Filter by account…"
          className="h-8 text-xs"
        />
      </div>
      <Select value={source || "_all"} onValueChange={(v) => onSource(v === "_all" ? "" : v)}>
        <SelectTrigger className="h-8 w-32 text-xs" aria-label="Source">
          <SelectValue placeholder="All sources" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="_all">All sources</SelectItem>
          {SOURCES.filter(Boolean).map((s) => (
            <SelectItem key={s} value={s}>
              {s}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select value={String(size)} onValueChange={(v) => onSize(Number(v))}>
        <SelectTrigger className="h-8 w-28 text-xs" aria-label="Page size">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {PAGE_SIZES.map((n) => (
            <SelectItem key={n} value={String(n)}>
              {n} rows
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

export function Trading() {
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
      for (const t of tradesResult.load.data) {
        if (t.account) {
          set.add(t.account);
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
  const [detailOrderId, setDetailOrderId] = useState<number | null>(null);
  const [execReportOrderId, setExecReportOrderId] = useState<number | null>(null);

  function openDetail(id: number) {
    setDetailOrderId(id);
  }

  function closeDetail() {
    setDetailOrderId(null);
  }

  function openExecReport(id: number) {
    setDetailOrderId(null);
    setExecReportOrderId(id);
  }

  function closeExecReport() {
    setExecReportOrderId(null);
  }

  const activeLoad = tab === "orders" ? ordersResult : tradesResult;
  const activeReload = tab === "orders" ? ordersResult.reload : tradesResult.reload;

  return (
    <Page
      title="Trading"
      actions={
        <div className="flex items-center gap-2">
          <Button size="sm" onClick={() => setSubmitOpen(true)}>
            <Plus className="h-3.5 w-3.5" />
            Submit order
          </Button>
          <RefreshButton
            onClick={activeReload}
            busy={activeLoad.load.state === "loading"}
          />
        </div>
      }
    >
      <p className="text-xs text-muted-lt">
        Data-plane view: orders flowing through Officer, their lifecycle events,
        and the resulting trades. Select an order row to see its event timeline
        and fills.
      </p>

      {/* Tab toggle */}
      <div className="flex gap-1 rounded-card border border-border bg-bg p-1 w-fit">
        {(["orders", "trades"] as TabId[]).map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            className={[
              "rounded-[4px] px-3 py-1 text-xs font-medium capitalize transition-colors",
              tab === t
                ? "bg-surface text-text shadow-sm"
                : "text-muted-lt hover:text-text",
            ].join(" ")}
          >
            {t}
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
          {ordersResult.load.state === "loading" && <TableSkeleton cols={9} />}
          {ordersResult.load.state === "error" && (
            <ErrorState
              message={ordersResult.load.error}
              onRetry={ordersResult.reload}
            />
          )}
          {ordersResult.load.state === "ready" &&
            (ordersResult.load.data.length === 0 ? (
              <EmptyState
                title="No orders yet"
                hint="Orders submitted through the panel, API, or MCP will appear here."
                action={
                  <Button size="sm" onClick={() => setSubmitOpen(true)}>
                    <Plus className="h-3.5 w-3.5" />
                    Submit order
                  </Button>
                }
              />
            ) : (
              <OrdersTable
                orders={ordersResult.load.data}
                onRowClick={(o) => openDetail(o.id)}
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
                title="No trades yet"
                hint="Trades result from execution reports submitted against orders."
              />
            ) : (
              <TradesTable
                trades={tradesResult.load.data}
                onOrderClick={openDetail}
              />
            ))}
        </>
      )}

      {/* Dialogs */}
      <SubmitOrderDialog
        open={submitOpen}
        onClose={() => setSubmitOpen(false)}
        onCreated={ordersResult.reload}
        accountSuggestions={allAccountSuggestions}
        assetSuggestions={assetSuggestions}
      />

      <OrderDetailDialog
        orderId={detailOrderId}
        onClose={closeDetail}
        onExecReport={openExecReport}
      />

      <ExecReportDialog
        orderId={execReportOrderId}
        onClose={closeExecReport}
        onSubmitted={() => {
          ordersResult.reload();
          tradesResult.reload();
        }}
      />
    </Page>
  );
}
