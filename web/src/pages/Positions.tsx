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
import { Coins, Copy, Plus, RefreshCw } from "lucide-react";
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

function pnlClass(v: string | undefined): string {
  const value = (v ?? "").trim();
  if (value === "" || /^0+(\.0+)?$/.test(value)) {
    return "text-[var(--pnl-flat)]";
  }
  if (value.startsWith("-") || value.startsWith("−")) {
    return "text-[var(--pnl-neg)]";
  }
  return "text-[var(--pnl-pos)]";
}

// ---------------------------------------------------------------------------
// Balances table — inline editing
// ---------------------------------------------------------------------------

/** A seed for the dialog's "apply with limits" path, carried from a row. */
interface AdjustSeed {
  account: string;
  asset: string;
  mode: AdjustmentMode;
  value: string;
}

/** The simple, no-extra-limits apply path: a balance amount on (account, asset).
 *  Returns the settled record so the row can show its outcome. */
type InlineApply = (
  account: string,
  asset: string,
  mode: AdjustmentMode,
  value: string,
) => Promise<Adjustment>;

/** Shared inline editor: balance mode + amount, then Apply (simple path) and
 *  Apply with limits (opens the dialog prefilled). Used by both existing
 *  balance rows and the draft add-row. The row supplies account + asset. */
function InlineAdjustEditor({
  account,
  asset,
  onInlineApply,
  onApplyWithLimits,
  onApplied,
  applyLabel,
}: {
  account: string;
  asset: string;
  onInlineApply: InlineApply;
  onApplyWithLimits: (seed: AdjustSeed) => void;
  /** Called after a successful inline apply (e.g. to reload + reset a draft). */
  onApplied?: () => void;
  /** Label for the simple Apply button (add-row reuses this slot). */
  applyLabel?: string;
}) {
  const { t } = useTranslation("positions");
  const [mode, setMode] = useState<AdjustmentMode>("absolute");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<Adjustment | null>(null);

  const canApply = account.trim() !== "" && asset.trim() !== "" && value.trim() !== "";

  const apply = async () => {
    if (!canApply) {
      return;
    }
    setBusy(true);
    setError(null);
    setOutcome(null);
    try {
      const result = await onInlineApply(
        account.trim(),
        asset.trim(),
        mode,
        value.trim(),
      );
      setOutcome(result);
      onApplied?.();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-end gap-2">
        <Select value={mode} onValueChange={(v) => setMode(v as AdjustmentMode)}>
          <SelectTrigger className="h-7 w-24 text-xs" disabled={busy}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="absolute">{t("dialog.mode.absolute")}</SelectItem>
            <SelectItem value="delta">{t("dialog.mode.delta")}</SelectItem>
          </SelectContent>
        </Select>
        <Input
          value={value}
          spellCheck={false}
          placeholder={t("inline.amountPlaceholder")}
          className="h-7 w-28 text-right text-xs"
          disabled={busy}
          aria-label={t("inline.amountAriaLabel", { account, asset })}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && canApply && !busy) {
              void apply();
            }
          }}
        />
        <Button
          size="sm"
          onClick={() => void apply()}
          disabled={busy || !canApply}
          aria-label={t("inline.applyAriaLabel", { account, asset })}
        >
          {applyLabel ?? t("inline.apply")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() =>
            onApplyWithLimits({
              account: account.trim(),
              asset: asset.trim(),
              mode,
              value: value.trim(),
            })
          }
          disabled={busy}
          aria-label={t("inline.applyWithLimitsAriaLabel", { account, asset })}
        >
          {t("inline.applyWithLimits")}
        </Button>
      </div>
      {outcome && <AdjustOutcomeView adjustment={outcome} />}
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
    </div>
  );
}

function BalanceEditRow({
  balance,
  onInlineApply,
  onApplyWithLimits,
  onApplied,
}: {
  balance: Balance;
  onInlineApply: InlineApply;
  onApplyWithLimits: (seed: AdjustSeed) => void;
  onApplied: () => void;
}) {
  const b = balance;
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell className="nums text-xs">{b.account}</TableCell>
      <TableCell className="nums text-xs">{b.asset}</TableCell>
      <TableCell className="nums text-right text-xs">{b.available}</TableCell>
      <TableCell className="nums text-right text-xs">{b.held}</TableCell>
      <TableCell className="nums text-right text-xs">{b.incoming}</TableCell>
      <TableCell className="nums text-right text-xs">
        {dash(b.averageEntryPrice)}
      </TableCell>
      <TableCell className={`nums text-right text-xs ${pnlClass(b.realizedPnl)}`}>
        {dash(b.realizedPnl)}
      </TableCell>
      <TableCell className="text-xs text-muted-lt">
        <SplitTime iso={b.updatedAt} />
      </TableCell>
      <TableCell>
        <InlineAdjustEditor
          account={b.account}
          asset={b.asset}
          onInlineApply={onInlineApply}
          onApplyWithLimits={onApplyWithLimits}
          onApplied={onApplied}
        />
      </TableCell>
    </TableRow>
  );
}

/** A draft row to seed a balance for an account/asset not yet listed. */
function BalanceDraftRow({
  accountSuggestions,
  assetSuggestions,
  onInlineApply,
  onApplyWithLimits,
  onApplied,
}: {
  accountSuggestions: string[];
  assetSuggestions: string[];
  onInlineApply: InlineApply;
  onApplyWithLimits: (seed: AdjustSeed) => void;
  onApplied: () => void;
}) {
  const { t } = useTranslation("positions");
  const [account, setAccount] = useState("");
  const [asset, setAsset] = useState("");

  return (
    <TableRow className="hover:bg-transparent">
      <TableCell className="text-xs">
        <Autocomplete
          value={account}
          spellCheck={false}
          placeholder="acc-1"
          className="h-7 text-xs"
          suggestions={accountSuggestions}
          aria-label={t("inline.accountAriaLabel")}
          onChange={setAccount}
        />
      </TableCell>
      <TableCell className="text-xs">
        <Autocomplete
          value={asset}
          spellCheck={false}
          placeholder="AAPL"
          className="h-7 text-xs"
          suggestions={assetSuggestions}
          aria-label={t("inline.assetAriaLabel")}
          onChange={setAsset}
        />
      </TableCell>
      <TableCell className="text-right text-xs text-muted-lt" colSpan={5}>
        {t("inline.newRowHint")}
      </TableCell>
      <TableCell />
      <TableCell>
        <InlineAdjustEditor
          account={account}
          asset={asset}
          onInlineApply={onInlineApply}
          onApplyWithLimits={onApplyWithLimits}
          onApplied={() => {
            setAccount("");
            setAsset("");
            onApplied();
          }}
        />
      </TableCell>
    </TableRow>
  );
}

function BalancesTable({
  balances,
  accountSuggestions,
  assetSuggestions,
  onInlineApply,
  onApplyWithLimits,
  onApplied,
}: {
  balances: Balance[];
  accountSuggestions: string[];
  assetSuggestions: string[];
  onInlineApply: InlineApply;
  onApplyWithLimits: (seed: AdjustSeed) => void;
  onApplied: () => void;
}) {
  const { t } = useTranslation("positions");
  return (
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
            <TableHead className="text-right">{t("balances.columns.adjust")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {balances.map((b) => (
            <BalanceEditRow
              key={`${b.account}|${b.asset}`}
              balance={b}
              onInlineApply={onInlineApply}
              onApplyWithLimits={onApplyWithLimits}
              onApplied={onApplied}
            />
          ))}
          <BalanceDraftRow
            accountSuggestions={accountSuggestions}
            assetSuggestions={assetSuggestions}
            onInlineApply={onInlineApply}
            onApplyWithLimits={onApplyWithLimits}
            onApplied={onApplied}
          />
        </TableBody>
    </Table>
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
      <div className="rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] p-3 text-xs text-[var(--danger)]">
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
      <div className="rounded-card border border-[var(--ok)] bg-[var(--ok-dim)] p-3 text-xs">
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
  id,
  label,
  field,
  onChange,
}: {
  id: string;
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
          id={`chkb-${id}`}
          checked={field.enabled}
          onChange={(e) => onChange({ ...field, enabled: e.target.checked })}
          className="h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
        />
        <Label htmlFor={`chkb-${id}`} className="cursor-pointer capitalize">
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
  /** Balance amount to preseed from a row (empty leaves the field disabled). */
  initialBalanceMode: AdjustmentMode;
  initialBalanceValue: string;
  /** Full-clone prefill for held, incoming, avgPrice, and bounds. */
  initialHeldMode?: AdjustmentMode;
  initialHeldValue?: string;
  initialIncomingMode?: AdjustmentMode;
  initialIncomingValue?: string;
  initialAvgPrice?: string;
  initialBalanceBoundsLower?: string;
  initialBalanceBoundsUpper?: string;
  initialHeldBoundsLower?: string;
  initialHeldBoundsUpper?: string;
  initialIncomingBoundsLower?: string;
  initialIncomingBoundsUpper?: string;
  assetSuggestions: string[];
  accountSuggestions: string[];
  onDone: () => void;
}

function AdjustDialog({
  open,
  onOpenChange,
  initialAccount,
  initialAsset,
  initialBalanceMode,
  initialBalanceValue,
  initialHeldMode,
  initialHeldValue,
  initialIncomingMode,
  initialIncomingValue,
  initialAvgPrice,
  initialBalanceBoundsLower,
  initialBalanceBoundsUpper,
  initialHeldBoundsLower,
  initialHeldBoundsUpper,
  initialIncomingBoundsLower,
  initialIncomingBoundsUpper,
  assetSuggestions,
  accountSuggestions,
  onDone,
}: AdjustDialogProps) {
  // A preseeded amount (e.g. from an inline row or clone) opens the field
  // already enabled so the operator only adjusts what they need.
  const seedAmount = (mode: AdjustmentMode | undefined, value: string | undefined): AmountFieldState =>
    value
      ? { enabled: true, mode: mode ?? "absolute", value }
      : emptyAmount();

  const seedBounds = (lower: string | undefined, upper: string | undefined): BoundsFieldState =>
    lower || upper
      ? { enabled: true, lower: lower ?? "", upper: upper ?? "" }
      : emptyBounds();

  const [account, setAccount] = useState(initialAccount);
  const [asset, setAsset] = useState(initialAsset);
  const [avgPrice, setAvgPrice] = useState(initialAvgPrice ?? "");
  const [balance, setBalance] = useState<AmountFieldState>(() => seedAmount(initialBalanceMode, initialBalanceValue));
  const [held, setHeld] = useState<AmountFieldState>(() => seedAmount(initialHeldMode, initialHeldValue));
  const [incoming, setIncoming] = useState<AmountFieldState>(() => seedAmount(initialIncomingMode, initialIncomingValue));
  const [balanceBounds, setBalanceBounds] = useState<BoundsFieldState>(() => seedBounds(initialBalanceBoundsLower, initialBalanceBoundsUpper));
  const [heldBounds, setHeldBounds] = useState<BoundsFieldState>(() => seedBounds(initialHeldBoundsLower, initialHeldBoundsUpper));
  const [incomingBounds, setIncomingBounds] = useState<BoundsFieldState>(() => seedBounds(initialIncomingBoundsLower, initialIncomingBoundsUpper));
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
      setAvgPrice(initialAvgPrice ?? "");
      setBalance(seedAmount(initialBalanceMode, initialBalanceValue));
      setHeld(seedAmount(initialHeldMode, initialHeldValue));
      setIncoming(seedAmount(initialIncomingMode, initialIncomingValue));
      setBalanceBounds(seedBounds(initialBalanceBoundsLower, initialBalanceBoundsUpper));
      setHeldBounds(seedBounds(initialHeldBoundsLower, initialHeldBoundsUpper));
      setIncomingBounds(seedBounds(initialIncomingBoundsLower, initialIncomingBoundsUpper));
      setBusy(false);
      setError(null);
      setOutcome(null);
    }
  }, [
    open,
    initialAccount, initialAsset,
    initialBalanceMode, initialBalanceValue,
    initialHeldMode, initialHeldValue,
    initialIncomingMode, initialIncomingValue,
    initialAvgPrice,
    initialBalanceBoundsLower, initialBalanceBoundsUpper,
    initialHeldBoundsLower, initialHeldBoundsUpper,
    initialIncomingBoundsLower, initialIncomingBoundsUpper,
  ]);

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
              id="balance"
              label={t("dialog.bounds.balanceLabel")}
              field={balanceBounds}
              onChange={setBalanceBounds}
            />
            <BoundsField
              id="held"
              label={t("dialog.bounds.heldLabel")}
              field={heldBounds}
              onChange={setHeldBounds}
            />
            <BoundsField
              id="incoming"
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
  const { t } = useTranslation("positions");
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
      parts.push(
        `${t("dialog.outcome.fieldBalance")} Δ${accepted.balanceDelta} → ${accepted.balanceResult}`,
      );
    }
    if (accepted.heldDelta || accepted.heldResult) {
      parts.push(
        `${t("dialog.outcome.fieldHeld")} Δ${accepted.heldDelta} → ${accepted.heldResult}`,
      );
    }
    if (accepted.incomingDelta || accepted.incomingResult) {
      parts.push(
        `${t("dialog.outcome.fieldIncoming")} Δ${accepted.incomingDelta} → ${accepted.incomingResult}`,
      );
    }
    return (
      <span className="nums text-text">
        {parts.length > 0 ? parts.join(" · ") : "—"}
      </span>
    );
  }
  return <span className="text-muted-lt">—</span>;
}

function HistoryRow({ adj, onClone }: { adj: Adjustment; onClone: (adj: Adjustment) => void }) {
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
      <TableCell>
        <Button
          variant="ghost"
          size="sm"
          aria-label={t("history.clone.ariaLabel", { id: adj.id })}
          onClick={() => onClone(adj)}
        >
          <Copy className="h-3.5 w-3.5" />
        </Button>
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
  const [adjustBalanceMode, setAdjustBalanceMode] =
    useState<AdjustmentMode>("absolute");
  const [adjustBalanceValue, setAdjustBalanceValue] = useState("");
  // Extended clone-prefill props (undefined = leave empty / disabled).
  const [adjustHeldMode, setAdjustHeldMode] = useState<AdjustmentMode | undefined>(undefined);
  const [adjustHeldValue, setAdjustHeldValue] = useState<string | undefined>(undefined);
  const [adjustIncomingMode, setAdjustIncomingMode] = useState<AdjustmentMode | undefined>(undefined);
  const [adjustIncomingValue, setAdjustIncomingValue] = useState<string | undefined>(undefined);
  const [adjustAvgPrice, setAdjustAvgPrice] = useState<string | undefined>(undefined);
  const [adjustBalanceBoundsLower, setAdjustBalanceBoundsLower] = useState<string | undefined>(undefined);
  const [adjustBalanceBoundsUpper, setAdjustBalanceBoundsUpper] = useState<string | undefined>(undefined);
  const [adjustHeldBoundsLower, setAdjustHeldBoundsLower] = useState<string | undefined>(undefined);
  const [adjustHeldBoundsUpper, setAdjustHeldBoundsUpper] = useState<string | undefined>(undefined);
  const [adjustIncomingBoundsLower, setAdjustIncomingBoundsLower] = useState<string | undefined>(undefined);
  const [adjustIncomingBoundsUpper, setAdjustIncomingBoundsUpper] = useState<string | undefined>(undefined);

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

  /** Clear the extended clone-only props so they don't bleed into a fresh dialog. */
  const clearCloneExtras = () => {
    setAdjustHeldMode(undefined);
    setAdjustHeldValue(undefined);
    setAdjustIncomingMode(undefined);
    setAdjustIncomingValue(undefined);
    setAdjustAvgPrice(undefined);
    setAdjustBalanceBoundsLower(undefined);
    setAdjustBalanceBoundsUpper(undefined);
    setAdjustHeldBoundsLower(undefined);
    setAdjustHeldBoundsUpper(undefined);
    setAdjustIncomingBoundsLower(undefined);
    setAdjustIncomingBoundsUpper(undefined);
  };

  // "Apply with limits" — open the dialog prefilled from the row's draft.
  const openAdjustWithLimits = (seed: AdjustSeed) => {
    setAdjustAccount(seed.account);
    setAdjustAsset(seed.asset);
    setAdjustBalanceMode(seed.mode);
    setAdjustBalanceValue(seed.value);
    clearCloneExtras();
    setAdjustOpen(true);
  };

  const openNewAdjust = () => {
    setAdjustAccount(accountFilter.trim());
    setAdjustAsset(assetFilter.trim());
    setAdjustBalanceMode("absolute");
    setAdjustBalanceValue("");
    clearCloneExtras();
    setAdjustOpen(true);
  };

  // Clone an adjustment record — open dialog prefilled with all request fields.
  const openCloneAdjust = (adj: Adjustment) => {
    const req = adj.request;
    setAdjustAccount(adj.account);
    setAdjustAsset(adj.asset);
    setAdjustBalanceMode(req.balance?.mode ?? "absolute");
    setAdjustBalanceValue(req.balance?.value ?? "");
    setAdjustHeldMode(req.held?.mode);
    setAdjustHeldValue(req.held?.value);
    setAdjustIncomingMode(req.incoming?.mode);
    setAdjustIncomingValue(req.incoming?.value);
    setAdjustAvgPrice(req.averageEntryPrice);
    setAdjustBalanceBoundsLower(req.balanceBounds?.lower);
    setAdjustBalanceBoundsUpper(req.balanceBounds?.upper);
    setAdjustHeldBoundsLower(req.heldBounds?.lower);
    setAdjustHeldBoundsUpper(req.heldBounds?.upper);
    setAdjustIncomingBoundsLower(req.incomingBounds?.lower);
    setAdjustIncomingBoundsUpper(req.incomingBounds?.upper);
    setAdjustOpen(true);
  };

  // The simple, no-extra-limits inline apply: just a balance amount.
  const inlineApply: InlineApply = (account, asset, mode, value) =>
    createAdjustment(account, { asset, balance: { mode, value } });

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
          // No balances yet: keep the guidance hint, but still offer the inline
          // add-row table so the first balance can be seeded right here.
          <>
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
            <BalancesTable
              balances={[]}
              accountSuggestions={accountSuggestions}
              assetSuggestions={assetSuggestions}
              onInlineApply={inlineApply}
              onApplyWithLimits={openAdjustWithLimits}
              onApplied={handleAdjustDone}
            />
          </>
        ) : (
          <BalancesTable
            balances={balancesLoad.load.data}
            accountSuggestions={accountSuggestions}
            assetSuggestions={assetSuggestions}
            onInlineApply={inlineApply}
            onApplyWithLimits={openAdjustWithLimits}
            onApplied={handleAdjustDone}
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
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {adjustmentsLoad.load.data.map((adj) => (
                  <HistoryRow key={adj.id} adj={adj} onClone={openCloneAdjust} />
                ))}
              </TableBody>
          </Table>
        ))}

      <AdjustDialog
        open={adjustOpen}
        onOpenChange={setAdjustOpen}
        initialAccount={adjustAccount}
        initialAsset={adjustAsset}
        initialBalanceMode={adjustBalanceMode}
        initialBalanceValue={adjustBalanceValue}
        initialHeldMode={adjustHeldMode}
        initialHeldValue={adjustHeldValue}
        initialIncomingMode={adjustIncomingMode}
        initialIncomingValue={adjustIncomingValue}
        initialAvgPrice={adjustAvgPrice}
        initialBalanceBoundsLower={adjustBalanceBoundsLower}
        initialBalanceBoundsUpper={adjustBalanceBoundsUpper}
        initialHeldBoundsLower={adjustHeldBoundsLower}
        initialHeldBoundsUpper={adjustHeldBoundsUpper}
        initialIncomingBoundsLower={adjustIncomingBoundsLower}
        initialIncomingBoundsUpper={adjustIncomingBoundsUpper}
        assetSuggestions={assetSuggestions}
        accountSuggestions={accountSuggestions}
        onDone={handleAdjustDone}
      />
    </Page>
  );
}
