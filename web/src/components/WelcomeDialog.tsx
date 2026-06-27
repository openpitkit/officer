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
  BookOpen,
  CheckCircle2,
  Database,
  ExternalLink,
  Landmark,
  ListChecks,
  RadioTower,
  ShieldCheck,
  TerminalSquare,
  WalletCards,
  X,
} from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import type { Limit, MarketDataInstance } from "@/api/types";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ApiError, useOfficerApi } from "@/framework";
import { cn } from "@/lib/utils";

const DEMO_ACCOUNT_ID = "demo-main";
const BINANCE_MARKET_DATA_LABEL = "Binance spot";
const STATIC_MARKET_DATA_LABEL = "FX (static)";
const BINANCE_PROVIDER = "binance";
const MANUAL_PROVIDER = "byo";

type ActionId = "account" | "policies" | "marketData" | "binance";

interface WelcomeDialogProps {
  onOpenChange: (open: boolean) => void;
  open: boolean;
}

interface PresetInstrument {
  baseAsset: string;
  externalSymbol: string;
  manualPrice: string;
  quoteAsset: string;
}

interface StatusMessage {
  text: string;
  tone: "ok" | "warn";
}

const DEMO_LIMITS: Limit[] = [
  {
    policy: "rate_limit",
    scope: "account",
    account: DEMO_ACCOUNT_ID,
    asset: "",
    values: { max_orders: "20", window: "1m" },
  },
  {
    policy: "order_size_limit",
    scope: "account_asset",
    account: DEMO_ACCOUNT_ID,
    asset: "BTC",
    values: { max_quantity: "2", max_notional: "150000" },
  },
  {
    policy: "pnl_bounds_kill_switch",
    scope: "account_asset",
    account: DEMO_ACCOUNT_ID,
    asset: "BTC",
    values: { lower_bound: "-5000", upper_bound: "10000" },
  },
];

const DEMO_POSITIONS = [
  { asset: "USD", balance: "100000" },
  { asset: "BTC", balance: "1", averageEntryPrice: "65000" },
  { asset: "ETH", balance: "10", averageEntryPrice: "3500" },
  { asset: "USDT", balance: "25000", averageEntryPrice: "1" },
];

const STATIC_MARKET_DATA_PRESET: PresetInstrument[] = [
  {
    externalSymbol: "USDT/USD",
    baseAsset: "USDT",
    quoteAsset: "USD",
    manualPrice: "0.9988",
  },
  {
    externalSymbol: "USDC/USD",
    baseAsset: "USDC",
    quoteAsset: "USD",
    manualPrice: "0.9999",
  },
  {
    externalSymbol: "USDT/USDC",
    baseAsset: "USDT",
    quoteAsset: "USDC",
    manualPrice: "0.9989",
  },
  {
    externalSymbol: "EURI/EUR",
    baseAsset: "EURI",
    quoteAsset: "EUR",
    manualPrice: "0.9995",
  },
  {
    externalSymbol: "AEUR/EUR",
    baseAsset: "AEUR",
    quoteAsset: "EUR",
    manualPrice: "0.9992",
  },
  {
    externalSymbol: "EURI/AEUR",
    baseAsset: "EURI",
    quoteAsset: "AEUR",
    manualPrice: "1.0002",
  },
];

const BINANCE_MARKET_DATA_PRESET: PresetInstrument[] = [
  {
    externalSymbol: "EURUSDT",
    baseAsset: "EUR",
    quoteAsset: "USDT",
    manualPrice: "",
  },
  {
    externalSymbol: "EURUSDC",
    baseAsset: "EUR",
    quoteAsset: "USDC",
    manualPrice: "",
  },
  {
    externalSymbol: "EUREURI",
    baseAsset: "EUR",
    quoteAsset: "EURI",
    manualPrice: "",
  },
  {
    externalSymbol: "USDTUSD",
    baseAsset: "USDT",
    quoteAsset: "USD",
    manualPrice: "",
  },
  {
    externalSymbol: "USDCUSD",
    baseAsset: "USDC",
    quoteAsset: "USD",
    manualPrice: "",
  },
  {
    externalSymbol: "USDCUSDT",
    baseAsset: "USDC",
    quoteAsset: "USDT",
    manualPrice: "",
  },
  {
    externalSymbol: "EURIUSDT",
    baseAsset: "EURI",
    quoteAsset: "USDT",
    manualPrice: "",
  },
];

function apiErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return error.message;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

class PresetApplyError extends Error {
  readonly appliedCount: number;

  constructor(error: unknown, appliedCount: number) {
    super(apiErrorMessage(error));
    this.name = "PresetApplyError";
    this.appliedCount = appliedCount;
  }
}

function findStaticMarketDataInstance(
  instances: MarketDataInstance[],
): MarketDataInstance | undefined {
  return findMarketDataInstance(
    instances,
    MANUAL_PROVIDER,
    STATIC_MARKET_DATA_LABEL,
  );
}

function findBinanceMarketDataInstance(
  instances: MarketDataInstance[],
): MarketDataInstance | undefined {
  return findMarketDataInstance(
    instances,
    BINANCE_PROVIDER,
    BINANCE_MARKET_DATA_LABEL,
  );
}

function findMarketDataInstance(
  instances: MarketDataInstance[],
  provider: string,
  label: string,
): MarketDataInstance | undefined {
  return instances.find(
    (instance) => instance.provider === provider && instance.label === label,
  );
}

function Section({
  children,
  icon: Icon,
  label,
}: {
  children: ReactNode;
  icon: typeof ShieldCheck;
  label: string;
}) {
  return (
    <section className="border-t border-border pt-4">
      <div className="mb-2 flex items-center gap-2">
        <Icon className="h-4 w-4 text-accent" />
        <h3 className="text-sm font-bold text-text">{label}</h3>
      </div>
      {children}
    </section>
  );
}

function ActionButton({
  action,
  busy,
  children,
  onClick,
}: {
  action: ActionId;
  busy: ActionId | null;
  children: ReactNode;
  onClick: () => void;
}) {
  return (
    <Button
      type="button"
      variant={busy === action ? "default" : "outline"}
      onClick={onClick}
      disabled={busy !== null}
      className="justify-start"
    >
      {busy === action ? (
        <RadioTower className="animate-pulse" />
      ) : (
        <CheckCircle2 />
      )}
      {children}
    </Button>
  );
}

export function WelcomeDialog({ onOpenChange, open }: WelcomeDialogProps) {
  const { t } = useTranslation();
  const {
    createAccount,
    createAdjustment,
    createMarketDataInstance,
    fetchAccounts,
    fetchMarketData,
    putLimit,
    restartMarketData,
    setAccountNotes,
    setMarketDataInstanceEnabled,
    setWelcomeSeen,
    upsertMarketDataInstrument,
  } = useOfficerApi();
  const [busy, setBusy] = useState<ActionId | null>(null);
  const [status, setStatus] = useState<StatusMessage | null>(null);
  // The first-run dialog reappears on every load until the operator opts out;
  // a first-time reader rarely takes in everything, so dismissal is explicit.
  const [dontShowAgain, setDontShowAgain] = useState(false);

  const links = useMemo(
    () => [
      {
        label: t("about.links.website"),
        href: "https://officer.openpit.dev?officer",
      },
      {
        label: t("about.links.issues"),
        href: "https://github.com/openpitkit/officer/issues?officer",
      },
      {
        label: t("about.links.discussions"),
        href: "https://github.com/openpitkit/officer/discussions?officer",
      },
      {
        label: t("about.links.openpit"),
        href: "https://openpit.dev?officer",
      },
    ],
    [t],
  );

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen && dontShowAgain) {
      // Persist the opt-out; a transient failure just means it shows again.
      void setWelcomeSeen(true).catch(() => {});
    }
    onOpenChange(nextOpen);
  };

  const runAction = async (
    action: ActionId,
    label: string,
    fn: () => Promise<number>,
  ) => {
    setBusy(action);
    setStatus(null);
    try {
      const count = await fn();
      setStatus({
        tone: "ok",
        text: t("welcome.status.applied", { count, label }),
      });
    } catch (error) {
      const partialCount =
        error instanceof PresetApplyError ? error.appliedCount : 0;
      setStatus({
        tone: "warn",
        text:
          partialCount > 0
            ? t("welcome.status.partialFailed", {
                count: partialCount,
                label,
                message: apiErrorMessage(error),
              })
            : t("welcome.status.failed", {
                label,
                message: apiErrorMessage(error),
              }),
      });
    } finally {
      setBusy(null);
    }
  };

  const ensureDemoAccount = async () => {
    let applied = 0;
    try {
      const accounts = await fetchAccounts();
      if (!accounts.some((account) => account.code === DEMO_ACCOUNT_ID)) {
        await createAccount(DEMO_ACCOUNT_ID);
        applied += 1;
      }
      await setAccountNotes(DEMO_ACCOUNT_ID, t("welcome.presets.accountNote"));
      applied += 1;
      for (const position of DEMO_POSITIONS) {
        await createAdjustment(DEMO_ACCOUNT_ID, {
          asset: position.asset,
          balance: { mode: "absolute", value: position.balance },
          averageEntryPrice: position.averageEntryPrice,
        });
        applied += 1;
      }
      return applied;
    } catch (error) {
      throw new PresetApplyError(error, applied);
    }
  };

  const applyPolicyPreset = async () => {
    let applied = 0;
    try {
      for (const limit of DEMO_LIMITS) {
        await putLimit(limit);
        applied += 1;
      }
      return applied;
    } catch (error) {
      throw new PresetApplyError(error, applied);
    }
  };

  const applyMarketDataPreset = async () => {
    let applied = 0;
    try {
      const status = await fetchMarketData();
      let instance = findStaticMarketDataInstance(status.instances);
      if (instance === undefined) {
        instance = await createMarketDataInstance({
          provider: MANUAL_PROVIDER,
          label: STATIC_MARKET_DATA_LABEL,
          credentials: "",
          enabled: true,
        });
        applied += 1;
      }
      if (instance === undefined) {
        throw new Error(t("welcome.status.marketDataInstanceMissing"));
      }
      if (!instance.enabled) {
        await setMarketDataInstanceEnabled(instance.externalId, true);
        applied += 1;
      }
      for (const instrument of STATIC_MARKET_DATA_PRESET) {
        await upsertMarketDataInstrument(instance.externalId, {
          ...instrument,
          enabled: true,
        });
        applied += 1;
      }
      await restartMarketData();
      return applied;
    } catch (error) {
      throw new PresetApplyError(error, applied);
    }
  };

  const applyBinancePreset = async () => {
    let applied = 0;
    try {
      const status = await fetchMarketData();
      let instance = findBinanceMarketDataInstance(status.instances);
      if (instance === undefined) {
        instance = await createMarketDataInstance({
          provider: BINANCE_PROVIDER,
          label: BINANCE_MARKET_DATA_LABEL,
          credentials: "",
          enabled: true,
        });
        applied += 1;
      }
      if (instance === undefined) {
        throw new Error(t("welcome.status.binanceInstanceMissing"));
      }
      if (!instance.enabled) {
        await setMarketDataInstanceEnabled(instance.externalId, true);
        applied += 1;
      }
      for (const instrument of BINANCE_MARKET_DATA_PRESET) {
        await upsertMarketDataInstrument(instance.externalId, {
          ...instrument,
          enabled: true,
        });
        applied += 1;
      }
      await restartMarketData();
      return applied;
    } catch (error) {
      throw new PresetApplyError(error, applied);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent hideClose className="max-w-3xl gap-0 overflow-y-auto p-0">
        <div className="sticky top-0 z-10 border-b border-border bg-surface px-6 pb-4 pt-5">
          <div className="mb-4 flex items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="accent">{t("brand.controlPlane")}</Badge>
              <Badge variant="neutral">{t("welcome.badgeLocal")}</Badge>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <LanguageSwitch />
              <ThemeSwitch />
              <DialogClose asChild>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t("actions.close")}
                >
                  <X />
                </Button>
              </DialogClose>
            </div>
          </div>
          <DialogHeader>
            <DialogTitle className="text-lg">{t("welcome.title")}</DialogTitle>
            <DialogDescription>{t("welcome.description")}</DialogDescription>
          </DialogHeader>
        </div>

        <div className="space-y-4 px-6 pb-6 pt-5 text-sm text-muted-lt">
          <Section icon={ShieldCheck} label={t("welcome.sections.product")}>
            <p>{t("welcome.product.copy")}</p>
            <div className="mt-3 grid gap-2 sm:grid-cols-2">
              {links.map((link) => (
                <a
                  key={link.href}
                  href={link.href}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center justify-between gap-3 rounded-card border border-border bg-surface-2 px-3 py-2 text-xs text-muted-lt transition-colors hover:border-border-hover hover:bg-card-hover-bg hover:text-accent"
                >
                  <span>{link.label}</span>
                  <ExternalLink className="h-3.5 w-3.5 shrink-0" />
                </a>
              ))}
            </div>
          </Section>

          <Section icon={TerminalSquare} label={t("welcome.sections.uses")}>
            <div className="grid gap-2 sm:grid-cols-2">
              {[
                "dropCopy",
                "preTrade",
                "surfaces",
                "operations",
              ].map((item) => (
                <div
                  key={item}
                  className="rounded-card border border-border bg-surface-2 p-3"
                >
                  <div className="text-xs font-bold text-text">
                    {t(`welcome.useCases.${item}.title`)}
                  </div>
                  <p className="mt-1 text-xs">
                    {t(`welcome.useCases.${item}.copy`)}
                  </p>
                </div>
              ))}
            </div>
          </Section>

          <Section icon={WalletCards} label={t("welcome.sections.preTrade")}>
            <p>{t("welcome.preTrade.copy")}</p>
            <div className="mt-3 flex flex-wrap gap-2">
              <ActionButton
                action="account"
                busy={busy}
                onClick={() => {
                  void runAction(
                    "account",
                    t("welcome.actions.demoAccount"),
                    ensureDemoAccount,
                  );
                }}
              >
                {t("welcome.actions.demoAccount")}
              </ActionButton>
              <ActionButton
                action="policies"
                busy={busy}
                onClick={() => {
                  void runAction(
                    "policies",
                    t("welcome.actions.policyPreset"),
                    applyPolicyPreset,
                  );
                }}
              >
                {t("welcome.actions.policyPreset")}
              </ActionButton>
              <Button asChild variant="ghost">
                <Link to="/accounts">
                  <Landmark />
                  {t("welcome.actions.openAccounts")}
                </Link>
              </Button>
              <Button asChild variant="ghost">
                <Link to="/policies">
                  <ListChecks />
                  {t("welcome.actions.openPolicies")}
                </Link>
              </Button>
            </div>
          </Section>

          <Section icon={Database} label={t("welcome.sections.marketData")}>
            <p>{t("welcome.marketData.copy")}</p>
            <div className="mt-3 flex flex-wrap gap-2">
              <ActionButton
                action="binance"
                busy={busy}
                onClick={() => {
                  void runAction(
                    "binance",
                    t("welcome.actions.binancePreset"),
                    applyBinancePreset,
                  );
                }}
              >
                {t("welcome.actions.binancePreset")}
              </ActionButton>
              <span className="flex h-[var(--dens-button-h)] items-center px-1 text-xs font-bold uppercase tracking-[0.07em] text-muted">
                {t("welcome.actions.or")}
              </span>
              <ActionButton
                action="marketData"
                busy={busy}
                onClick={() => {
                  void runAction(
                    "marketData",
                    t("welcome.actions.marketDataPreset"),
                    applyMarketDataPreset,
                  );
                }}
              >
                {t("welcome.actions.marketDataPreset")}
              </ActionButton>
              <Button asChild variant="ghost">
                <Link to="/market-data">
                  <RadioTower />
                  {t("welcome.actions.openMarketData")}
                </Link>
              </Button>
            </div>
          </Section>

          <Section icon={BookOpen} label={t("welcome.sections.optional")}>
            <p>{t("welcome.optional.copy")}</p>
          </Section>

          {status && (
            <div
              className={cn(
                "rounded-card border px-3 py-2 text-xs",
                status.tone === "ok"
                  ? "border-[var(--ok)] bg-[var(--ok-dim)] text-[var(--ok)]"
                  : "border-[var(--warn)] bg-[var(--warn-dim)] text-[var(--warn)]",
              )}
            >
              {status.text}
            </div>
          )}

          <div className="flex flex-col gap-3 border-t border-border pt-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <label className="flex items-center gap-2 text-xs text-text">
                <input
                  type="checkbox"
                  checked={dontShowAgain}
                  onChange={(event) => setDontShowAgain(event.target.checked)}
                  className="accent-[var(--accent)]"
                />
                {t("welcome.dontShowAgain")}
              </label>
              <p className="text-xs text-muted-lt">{t("welcome.returnHint")}</p>
            </div>
            <DialogClose asChild>
              <Button type="button">
                <CheckCircle2 />
                {t("welcome.actions.start")}
              </Button>
            </DialogClose>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
