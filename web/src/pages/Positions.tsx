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
  useDeferredValue,
  useEffect,
  useMemo,
  useState,
} from "react";
import { Coins, Plus, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { ApiError, createAdjustment } from "@/api/client";
import { formatDate, formatTime } from "@/i18n/format";
import type {
  Adjustment,
  AdjustmentMode,
  Balance,
  BoundsPair,
  Source,
} from "@/api/types";
import { useAccounts } from "@/api/useAccounts";
import { useAdjustments } from "@/api/useAdjustments";
import { useBalances } from "@/api/useBalances";
import { Autocomplete } from "@/components/Autocomplete";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  TableSkeleton,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
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

/** Split a localized timestamp so the date and time are rendered as
 *  separate unbreakable units that wrap as a whole, never split mid-value. */
function SplitTime({ iso }: { iso: string }) {
  if (!iso) {
    return <span className="text-muted-lt">—</span>;
  }
  let date: string;
  let time: string;
  try {
    date = formatDate(iso);
    time = formatTime(iso);
  } catch {
    return <span>{iso}</span>;
  }
  return (
    <span>
      <span className="inline-block whitespace-nowrap">{date}</span>{" "}
      <span className="inline-block whitespace-nowrap">{time}</span>
    </span>
  );
}

function dash(v: string | undefined): string {
  return v && v !== "" ? v : "—";
}

// ---------------------------------------------------------------------------
// Balances table
// ---------------------------------------------------------------------------

function BalancesTable({
  balances,
  onAdjust,
}: {
  balances: Balance[];
  onAdjust: (b: Balance) => void;
}) {
  const { t } = useTranslation("positions");
  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("balances.columns.account")}</TableHead>
            <TableHead>{t("balances.columns.asset")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.available")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.held")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.incoming")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.avgEntryPrice")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.realizedPnl")}</TableHead>
            <TableHead>{t("balances.columns.updated")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.actions")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {balances.map((b) => (
            <TableRow
              key={`${b.account}|${b.asset}`}
              className="hover:bg-transparent"
            >
              <TableCell className="nums text-xs">{b.account}</TableCell>
              <TableCell className="nums text-xs">{b.asset}</TableCell>
              <TableCell className="nums text-right text-xs">{b.available}</TableCell>
              <TableCell className="nums text-right text-xs">{b.held}</TableCell>
              <TableCell className="nums text-right text-xs">{b.incoming}</TableCell>
              <TableCell className="nums text-right text-xs">
                {dash(b.averageEntryPrice)}
              </TableCell>
              <TableCell className="nums text-right text-xs">
                {dash(b.realizedPnl)}
              </TableCell>
              <TableCell className="text-xs text-muted-lt">
                <SplitTime iso={b.updatedAt} />
              </TableCell>
              <TableCell>
                <div className="flex justify-end">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onAdjust(b)}
                    aria-label={t("actions.adjustAriaLabel", { account: b.account, asset: b.asset })}
                  >
                    {t("actions.adjust")}
                  </Button>
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
// Adjustment outcome display (used in dialog after submit)
// ---------------------------------------------------------------------------

interface AdjustOutcome {
  adjustment: Adjustment;
}

function AdjustOutcomeView({ adjustment }: AdjustOutcome) {
  const { t } = useTranslation("positions");
  const { accepted, rejected } = adjustment;
  if (rejected) {
    return (
      <div className="rounded-card border border-[var(--danger)] bg-accent-dim p-3 text-xs text-[var(--danger)]">
        <p className="font-medium">{t("dialog.outcome.rejectedTitle")}</p>
        <p className="mt-1 text-[var(--danger)]">{rejected.reason}</p>
      </div>
    );
  }
  if (accepted) {
    const rows: { label: string; delta: string; result: string }[] = [];
    if (accepted.balanceDelta || accepted.balanceResult) {
      rows.push({ label: t("dialog.outcome.fieldBalance"), delta: accepted.balanceDelta, result: accepted.balanceResult });
    }
    if (accepted.heldDelta || accepted.heldResult) {
      rows.push({ label: t("dialog.outcome.fieldHeld"), delta: accepted.heldDelta, result: accepted.heldResult });
    }
    if (accepted.incomingDelta || accepted.incomingResult) {
      rows.push({ label: t("dialog.outcome.fieldIncoming"), delta: accepted.incomingDelta, result: accepted.incomingResult });
    }
    return (
      <div className="rounded-card border border-[var(--ok)] bg-accent-dim p-3 text-xs">
        <p className="font-medium text-[var(--ok)]">{t("dialog.outcome.acceptedTitle")}</p>
        {rows.length > 0 && (
          <div className="mt-2 space-y-1">
            {rows.map((r) => (
              <div key={r.label} className="flex gap-2">
                <span className="w-28 text-muted">{r.label}</span>
                <span className="nums text-text">
                  Δ{r.delta} → {r.result}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    );
  }
  return null;
}

// ---------------------------------------------------------------------------
// Amount field row — mode toggle + value input
// ---------------------------------------------------------------------------

interface AmountFieldState {
  enabled: boolean;
  mode: AdjustmentMode;
  value: string;
}

function AmountField({
  id,
  label,
  field,
  onChange,
}: {
  id: string;
  label: string;
  field: AmountFieldState;
  onChange: (next: AmountFieldState) => void;
}) {
  const { t } = useTranslation("positions");
  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <input
          type="checkbox"
          id={`chk-${id}`}
          checked={field.enabled}
          onChange={(e) => onChange({ ...field, enabled: e.target.checked })}
          className="h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
        />
        <Label htmlFor={`chk-${id}`} className="cursor-pointer capitalize">
          {label}
        </Label>
      </div>
      {field.enabled && (
        <div className="ml-5 flex items-center gap-2">
          <Select
            value={field.mode}
            onValueChange={(v) =>
              onChange({ ...field, mode: v as AdjustmentMode })
            }
          >
            <SelectTrigger className="h-7 w-24 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="absolute">{t("dialog.mode.absolute")}</SelectItem>
              <SelectItem value="delta">{t("dialog.mode.delta")}</SelectItem>
            </SelectContent>
          </Select>
          <Input
            value={field.value}
            spellCheck={false}
            placeholder="0"
            className="h-7 text-xs"
            onChange={(e) => onChange({ ...field, value: e.target.value })}
          />
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Bounds field row — optional lower / upper
// ---------------------------------------------------------------------------

interface BoundsFieldState {
  enabled: boolean;
  lower: string;
  upper: string;
}

function BoundsField({
  label,
  field,
  onChange,
}: {
  label: string;
  field: BoundsFieldState;
  onChange: (next: BoundsFieldState) => void;
}) {
  const { t } = useTranslation("positions");
  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <input
          type="checkbox"
          id={`chkb-${label}`}
          checked={field.enabled}
          onChange={(e) => onChange({ ...field, enabled: e.target.checked })}
          className="h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
        />
        <Label htmlFor={`chkb-${label}`} className="cursor-pointer capitalize">
          {label}
        </Label>
      </div>
      {field.enabled && (
        <div className="ml-5 grid grid-cols-2 gap-2">
          <div className="space-y-1">
            <Label className="text-[0.6875rem] text-muted">{t("dialog.bounds.lower")}</Label>
            <Input
              value={field.lower}
              spellCheck={false}
              placeholder="—"
              className="h-7 text-xs"
              onChange={(e) => onChange({ ...field, lower: e.target.value })}
            />
          </div>
          <div className="space-y-1">
            <Label className="text-[0.6875rem] text-muted">{t("dialog.bounds.upper")}</Label>
            <Input
              value={field.upper}
              spellCheck={false}
              placeholder="—"
              className="h-7 text-xs"
              onChange={(e) => onChange({ ...field, upper: e.target.value })}
            />
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Adjust dialog
// ---------------------------------------------------------------------------

function emptyAmount(): AmountFieldState {
  return { enabled: false, mode: "absolute", value: "" };
}

function emptyBounds(): BoundsFieldState {
  return { enabled: false, lower: "", upper: "" };
}

interface AdjustDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initialAccount: string;
  initialAsset: string;
  assetSuggestions: string[];
  accountSuggestions: string[];
  onDone: () => void;
}

function AdjustDialog({
  open,
  onOpenChange,
  initialAccount,
  initialAsset,
  assetSuggestions,
  accountSuggestions,
  onDone,
}: AdjustDialogProps) {
  const [account, setAccount] = useState(initialAccount);
  const [asset, setAsset] = useState(initialAsset);
  const [avgPrice, setAvgPrice] = useState("");
  const [balance, setBalance] = useState<AmountFieldState>(emptyAmount);
  const [held, setHeld] = useState<AmountFieldState>(emptyAmount);
  const [incoming, setIncoming] = useState<AmountFieldState>(emptyAmount);
  const [balanceBounds, setBalanceBounds] = useState<BoundsFieldState>(emptyBounds);
  const [heldBounds, setHeldBounds] = useState<BoundsFieldState>(emptyBounds);
  const [incomingBounds, setIncomingBounds] = useState<BoundsFieldState>(emptyBounds);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<Adjustment | null>(null);

  // Reseed whenever the dialog opens.
  useEffect(() => {
    if (open) {
      // Reset all form fields from props each time the dialog opens.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setAccount(initialAccount);
      setAsset(initialAsset);
      setAvgPrice("");
      setBalance(emptyAmount());
      setHeld(emptyAmount());
      setIncoming(emptyAmount());
      setBalanceBounds(emptyBounds());
      setHeldBounds(emptyBounds());
      setIncomingBounds(emptyBounds());
      setBusy(false);
      setError(null);
      setOutcome(null);
    }
  }, [open, initialAccount, initialAsset]);

  function buildBounds(f: BoundsFieldState): BoundsPair | undefined {
    if (!f.enabled) {
      return undefined;
    }
    const pair: BoundsPair = {};
    if (f.lower.trim()) {
      pair.lower = f.lower.trim();
    }
    if (f.upper.trim()) {
      pair.upper = f.upper.trim();
    }
    if (!pair.lower && !pair.upper) {
      return undefined;
    }
    return pair;
  }

  const { t } = useTranslation("positions");

  const submit = async () => {
    const trimAccount = account.trim();
    const trimAsset = asset.trim();
    if (!trimAccount) {
      setError(t("dialog.error.accountRequired"));
      return;
    }
    if (!trimAsset) {
      setError(t("dialog.error.assetRequired"));
      return;
    }
    setBusy(true);
    setError(null);
    setOutcome(null);
    try {
      const body: Parameters<typeof createAdjustment>[1] = { asset: trimAsset };
      if (avgPrice.trim()) {
        body.averageEntryPrice = avgPrice.trim();
      }
      if (balance.enabled && balance.value.trim()) {
        body.balance = { mode: balance.mode, value: balance.value.trim() };
      }
      if (held.enabled && held.value.trim()) {
        body.held = { mode: held.mode, value: held.value.trim() };
      }
      if (incoming.enabled && incoming.value.trim()) {
        body.incoming = { mode: incoming.mode, value: incoming.value.trim() };
      }
      const bb = buildBounds(balanceBounds);
      if (bb) {
        body.balanceBounds = bb;
      }
      const hb = buildBounds(heldBounds);
      if (hb) {
        body.heldBounds = hb;
      }
      const ib = buildBounds(incomingBounds);
      if (ib) {
        body.incomingBounds = ib;
      }
      const result = await createAdjustment(trimAccount, body);
      setOutcome(result);
      onDone();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("dialog.title")}</DialogTitle>
          <DialogDescription>
            {t("dialog.description")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {/* Account + asset */}
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="adj-account">{t("dialog.fields.account")}</Label>
              <Autocomplete
                id="adj-account"
                value={account}
                spellCheck={false}
                placeholder="acc-1"
                suggestions={accountSuggestions}
                onChange={setAccount}
                disabled={busy || outcome !== null}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="adj-asset">{t("dialog.fields.asset")}</Label>
              <Autocomplete
                id="adj-asset"
                value={asset}
                spellCheck={false}
                placeholder="AAPL"
                suggestions={assetSuggestions}
                onChange={setAsset}
                disabled={busy || outcome !== null}
              />
            </div>
          </div>

          {/* Average entry price */}
          <div className="space-y-1.5">
            <Label htmlFor="adj-aep">{t("dialog.fields.avgEntryPrice")}</Label>
            <Input
              id="adj-aep"
              value={avgPrice}
              spellCheck={false}
              placeholder="e.g. 142.50"
              className="text-xs"
              disabled={busy || outcome !== null}
              onChange={(e) => setAvgPrice(e.target.value)}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("dialog.fields.avgEntryPriceHint")}
            </p>
          </div>

          {/* Amount fields */}
          <div className="space-y-3 rounded-card border border-border p-3">
            <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
              {t("dialog.amounts.sectionLabel")}
            </p>
            <AmountField id="balance" label={t("dialog.amounts.balance")} field={balance} onChange={setBalance} />
            <AmountField id="held" label={t("dialog.amounts.held")} field={held} onChange={setHeld} />
            <AmountField id="incoming" label={t("dialog.amounts.incoming")} field={incoming} onChange={setIncoming} />
          </div>

          {/* Bounds fields */}
          <div className="space-y-3 rounded-card border border-border p-3">
            <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
              {t("dialog.bounds.sectionLabel")}
            </p>
            <BoundsField
              label={t("dialog.bounds.balanceLabel")}
              field={balanceBounds}
              onChange={setBalanceBounds}
            />
            <BoundsField
              label={t("dialog.bounds.heldLabel")}
              field={heldBounds}
              onChange={setHeldBounds}
            />
            <BoundsField
              label={t("dialog.bounds.incomingLabel")}
              field={incomingBounds}
              onChange={setIncomingBounds}
            />
          </div>

          {/* Outcome */}
          {outcome && <AdjustOutcomeView adjustment={outcome} />}

          {error && (
            <ErrorBanner message={error} onDismiss={() => setError(null)} />
          )}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {outcome ? t("dialog.footer.close") : t("actions.cancel", { ns: "common" })}
          </Button>
          {!outcome && (
            <Button
              size="sm"
              onClick={() => void submit()}
              disabled={busy}
            >
              {t("dialog.footer.submit")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Adjustment history row — inline outcome
// ---------------------------------------------------------------------------

function HistoryRowOutcome({ adj }: { adj: Adjustment }) {
  const { accepted, rejected } = adj;
  if (rejected) {
    return (
      <span className="text-[var(--danger)]">
        {rejected.reason || "—"}
      </span>
    );
  }
  if (accepted) {
    const parts: string[] = [];
    if (accepted.balanceDelta || accepted.balanceResult) {
      parts.push(`balance Δ${accepted.balanceDelta} → ${accepted.balanceResult}`);
    }
    if (accepted.heldDelta || accepted.heldResult) {
      parts.push(`held Δ${accepted.heldDelta} → ${accepted.heldResult}`);
    }
    if (accepted.incomingDelta || accepted.incomingResult) {
      parts.push(`incoming Δ${accepted.incomingDelta} → ${accepted.incomingResult}`);
    }
    return (
      <span className="nums text-text">
        {parts.length > 0 ? parts.join(" · ") : "—"}
      </span>
    );
  }
  return <span className="text-muted-lt">—</span>;
}

function HistoryRow({ adj }: { adj: Adjustment }) {
  const { t } = useTranslation("positions");
  const isRejected = !!adj.rejected;
  const isAccepted = !!adj.accepted && !isRejected;

  const req = adj.request;
  const reqParts: string[] = [];
  if (req.balance) {
    reqParts.push(
      req.balance.mode === "delta"
        ? t("history.request.balanceDelta", { value: req.balance.value })
        : t("history.request.balanceAbsolute", { value: req.balance.value }),
    );
  }
  if (req.held) {
    reqParts.push(
      req.held.mode === "delta"
        ? t("history.request.heldDelta", { value: req.held.value })
        : t("history.request.heldAbsolute", { value: req.held.value }),
    );
  }
  if (req.incoming) {
    reqParts.push(
      req.incoming.mode === "delta"
        ? t("history.request.incomingDelta", { value: req.incoming.value })
        : t("history.request.incomingAbsolute", { value: req.incoming.value }),
    );
  }
  if (req.averageEntryPrice) {
    reqParts.push(t("history.request.avgPrice", { value: req.averageEntryPrice }));
  }

  return (
    <TableRow className="hover:bg-transparent">
      <TableCell className="text-xs text-muted-lt"><SplitTime iso={adj.at} /></TableCell>
      <TableCell className="nums text-xs">{adj.account}</TableCell>
      <TableCell className="nums text-xs">{adj.asset}</TableCell>
      <TableCell>
        <Badge variant="neutral" className="text-[0.6875rem]">
          {adj.source}
        </Badge>
      </TableCell>
      <TableCell className="nums text-xs text-muted-lt">
        {reqParts.length > 0 ? reqParts.join(" · ") : "—"}
      </TableCell>
      <TableCell>
        {isRejected ? (
          <Badge variant="danger">{t("history.status.rejected")}</Badge>
        ) : isAccepted ? (
          <Badge variant="ok">{t("history.status.accepted")}</Badge>
        ) : (
          <Badge variant="neutral">{adj.status}</Badge>
        )}
      </TableCell>
      <TableCell className="text-xs">
        <HistoryRowOutcome adj={adj} />
      </TableCell>
    </TableRow>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

const SOURCES: Source[] = ["panel", "api", "mcp", "system"];

export function Positions() {
  const { t } = useTranslation("positions");
  const [searchParams] = useSearchParams();
  const initialAccount = searchParams.get("account") ?? "";

  const [accountFilter, setAccountFilter] = useState(initialAccount);
  const [assetFilter, setAssetFilter] = useState("");
  const [sourceFilter, setSourceFilter] = useState<Source | "__all__">("__all__");

  const [adjustOpen, setAdjustOpen] = useState(false);
  const [adjustAccount, setAdjustAccount] = useState("");
  const [adjustAsset, setAdjustAsset] = useState("");

  const deferredAccount = useDeferredValue(accountFilter.trim());
  const deferredAsset = useDeferredValue(assetFilter.trim());

  const balancesLoad = useBalances(
    deferredAccount || undefined,
    deferredAsset || undefined,
  );

  const deferredSource =
    sourceFilter === "__all__" ? undefined : sourceFilter;
  const adjustmentsLoad = useAdjustments(
    deferredAccount || undefined,
    deferredSource,
    100,
  );

  // Account suggestions from the accounts hook.
  const accountsLoad = useAccounts();
  const accountSuggestions = useMemo(() => {
    if (accountsLoad.load.state !== "ready") {
      return [];
    }
    return accountsLoad.load.data.map((a) => a.id);
  }, [accountsLoad.load]);

  // Asset suggestions: union from balances + adjustments history.
  const assetSuggestions = useMemo(() => {
    const set = new Set<string>();
    if (balancesLoad.load.state === "ready") {
      for (const b of balancesLoad.load.data) {
        if (b.asset) {
          set.add(b.asset);
        }
      }
    }
    if (adjustmentsLoad.load.state === "ready") {
      for (const a of adjustmentsLoad.load.data) {
        if (a.asset) {
          set.add(a.asset);
        }
      }
    }
    return Array.from(set).sort();
  }, [balancesLoad.load, adjustmentsLoad.load]);

  const openAdjust = (b: Balance) => {
    setAdjustAccount(b.account);
    setAdjustAsset(b.asset);
    setAdjustOpen(true);
  };

  const openNewAdjust = () => {
    setAdjustAccount(accountFilter.trim());
    setAdjustAsset(assetFilter.trim());
    setAdjustOpen(true);
  };

  const handleAdjustDone = () => {
    balancesLoad.reload();
    adjustmentsLoad.reload();
  };

  return (
    <Page
      title={t("title")}
      actions={
        <>
          <RefreshButton
            onClick={() => {
              balancesLoad.reload();
              adjustmentsLoad.reload();
            }}
            busy={
              balancesLoad.load.state === "loading" ||
              adjustmentsLoad.load.state === "loading"
            }
          />
          <Button size="sm" onClick={openNewAdjust}>
            <Plus className="h-3.5 w-3.5" />
            {t("actions.adjust")}
          </Button>
        </>
      }
    >
      {/* Filters */}
      <Card className="flex flex-wrap items-end gap-4 p-4">
        <div className="space-y-1.5">
          <Label htmlFor="pos-account">{t("filters.byAccount")}</Label>
          <Autocomplete
            id="pos-account"
            value={accountFilter}
            spellCheck={false}
            placeholder="acc-1"
            className="h-8 w-48 text-xs"
            suggestions={accountSuggestions}
            onChange={setAccountFilter}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="pos-asset">{t("filters.byAsset")}</Label>
          <Autocomplete
            id="pos-asset"
            value={assetFilter}
            spellCheck={false}
            placeholder="AAPL"
            className="h-8 w-32 text-xs"
            suggestions={assetSuggestions}
            onChange={setAssetFilter}
          />
        </div>
      </Card>

      {/* Balances */}
      <div className="flex items-center gap-2">
        <Coins className="h-3.5 w-3.5 text-muted" />
        <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
          {t("balances.sectionLabel")}
        </p>
      </div>

      {balancesLoad.load.state === "loading" && <TableSkeleton cols={9} />}
      {balancesLoad.load.state === "error" && (
        <ErrorState
          message={balancesLoad.load.error}
          onRetry={balancesLoad.reload}
        />
      )}
      {balancesLoad.load.state === "ready" &&
        (balancesLoad.load.data.length === 0 ? (
          <EmptyState
            title={t("balances.empty.title")}
            hint={t("balances.empty.hint")}
            action={
              <Button size="sm" onClick={openNewAdjust}>
                <Plus className="h-3.5 w-3.5" />
                {t("actions.adjust")}
              </Button>
            }
          />
        ) : (
          <BalancesTable
            balances={balancesLoad.load.data}
            onAdjust={openAdjust}
          />
        ))}

      {/* Adjustment history */}
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
          {t("history.sectionLabel")}
        </p>
        <div className="ml-auto flex items-center gap-2">
          <Label className="text-xs">{t("history.sourceLabel")}</Label>
          <Select
            value={sourceFilter}
            onValueChange={(v) =>
              setSourceFilter(v as Source | "__all__")
            }
          >
            <SelectTrigger className="h-7 w-28 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all__">{t("history.sourceAll")}</SelectItem>
              {SOURCES.map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="ghost"
            size="sm"
            onClick={adjustmentsLoad.reload}
            disabled={adjustmentsLoad.load.state === "loading"}
            aria-label={t("actions.refreshHistoryAriaLabel")}
          >
            <RefreshCw className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>

      {adjustmentsLoad.load.state === "loading" && (
        <TableSkeleton cols={7} />
      )}
      {adjustmentsLoad.load.state === "error" && (
        <ErrorState
          message={adjustmentsLoad.load.error}
          onRetry={adjustmentsLoad.reload}
        />
      )}
      {adjustmentsLoad.load.state === "ready" &&
        (adjustmentsLoad.load.data.length === 0 ? (
          <EmptyState
            title={t("history.empty.title")}
            hint={t("history.empty.hint")}
          />
        ) : (
          <Card>
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>{t("history.columns.time")}</TableHead>
                  <TableHead>{t("history.columns.account")}</TableHead>
                  <TableHead>{t("history.columns.asset")}</TableHead>
                  <TableHead>{t("history.columns.source")}</TableHead>
                  <TableHead>{t("history.columns.request")}</TableHead>
                  <TableHead>{t("history.columns.status")}</TableHead>
                  <TableHead>{t("history.columns.outcome")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {adjustmentsLoad.load.data.map((adj) => (
                  <HistoryRow key={adj.id} adj={adj} />
                ))}
              </TableBody>
            </Table>
          </Card>
        ))}

      <AdjustDialog
        open={adjustOpen}
        onOpenChange={setAdjustOpen}
        initialAccount={adjustAccount}
        initialAsset={adjustAsset}
        assetSuggestions={assetSuggestions}
        accountSuggestions={accountSuggestions}
        onDone={handleAdjustDone}
      />
    </Page>
  );
}
