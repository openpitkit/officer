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
  Check,
  Database,
  ExternalLink,
  Plus,
  RefreshCw,
  RotateCcw,
  SearchCheck,
  Trash2,
  X,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createMarketDataInstance,
  deleteMarketDataInstance,
  deleteMarketDataInstrument,
  restartMarketData,
  setMarketDataInstanceEnabled,
  setMarketDataInstrumentEnabled,
  upsertMarketDataInstrument,
  verifyMarketDataSymbol,
} from "@/api/client";
import type {
  MarketDataDiagnostic,
  MarketDataInstance,
  MarketDataSymbolVerification,
} from "@/api/types";
import { useMarketData } from "@/api/useMarketData";
import { Page } from "@/components/Page";
import { ErrorBanner, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { formatDateTime } from "@/i18n/format";

interface InstanceForm {
  id: string;
  type: string;
  label: string;
  credentials: string;
  enabled: boolean;
}

interface InstrumentDraft {
  externalSymbol: string;
  baseAsset: string;
  quoteAsset: string;
  manualPrice: string;
  enabled: boolean;
}

const emptyInstrument: InstrumentDraft = {
  externalSymbol: "",
  baseAsset: "",
  quoteAsset: "USD",
  manualPrice: "",
  enabled: true,
};

// Provider type whose instruments carry an operator-set manual mark price. Only
// this (bring-your-own / manual) provider exposes the price input; streaming
// providers get their marks from the source.
const MANUAL_PROVIDER = "byo";

function providerTitle(instance: MarketDataInstance): string {
  return instance.label || instance.id;
}

function bestPriceLabel(
  quote: MarketDataInstance["instruments"][number]["quote"],
): string {
  if (!quote) return "";
  return quote.mark || quote.bid || quote.ask;
}

function diagnosticLevelVariant(
  level: string,
): "danger" | "warn" | "neutral" {
  if (level === "error") return "danger";
  if (level === "warn") return "warn";
  return "neutral";
}

function DiagnosticGroup({
  title,
  items,
  instance,
  busy,
  onRestart,
  onDeleteInstrument,
}: {
  title: string;
  items: MarketDataDiagnostic[];
  instance: MarketDataInstance;
  busy: boolean;
  onRestart: () => void;
  onDeleteInstrument: (
    instance: MarketDataInstance,
    externalSymbol: string,
  ) => void;
}) {
  const { t } = useTranslation("marketData");
  if (items.length === 0) return null;
  return (
    <div>
      <p className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted">
        {title}
      </p>
      <div className="space-y-2">
        {items.map((d, i) => (
          <div
            key={i}
            className="rounded-card border border-border bg-surface p-3 text-sm"
          >
            <div className="flex flex-wrap items-start gap-2">
              <Badge
                variant={diagnosticLevelVariant(d.level)}
                className="mt-px shrink-0"
              >
                {d.level === "error"
                  ? t("diagnostics.levelError")
                  : d.level === "warn"
                    ? t("diagnostics.levelWarn")
                    : t("diagnostics.levelInfo")}
              </Badge>
              <span className="font-semibold text-text">{d.title}</span>
              {d.instrument && (
                <Badge variant="neutral" className="shrink-0 font-mono text-xs">
                  {d.instrument}
                </Badge>
              )}
              <span className="nums ml-auto shrink-0 text-xs text-muted">
                {formatDateTime(d.at)}
              </span>
            </div>
            <p className="mt-1.5 text-text">{d.detail}</p>
            {d.remediation && (
              <p className="mt-1.5 text-xs text-muted">
                <span className="font-medium">{t("diagnostics.howToFix")}</span>{" "}
                {d.remediation}
              </p>
            )}
            {d.actions.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-2">
                {d.actions.map((action, ai) => {
                  if (action.type === "restart") {
                    return (
                      <Button
                        key={ai}
                        type="button"
                        variant="outline"
                        size="sm"
                        disabled={busy}
                        onClick={onRestart}
                      >
                        {t("diagnostics.actionRestart")}
                      </Button>
                    );
                  }
                  if (
                    action.type === "open_docs" &&
                    instance.references?.docsUrl
                  ) {
                    return (
                      <a
                        key={ai}
                        href={instance.references.docsUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex h-8 items-center rounded-card border border-border bg-transparent px-3 text-xs transition-colors hover:bg-surface-hover"
                      >
                        <ExternalLink className="mr-1 h-3 w-3" />
                        {t("diagnostics.actionDocs")}
                      </a>
                    );
                  }
                  if (
                    action.type === "open_symbols" &&
                    instance.references?.symbolsUrl
                  ) {
                    return (
                      <a
                        key={ai}
                        href={instance.references.symbolsUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex h-8 items-center rounded-card border border-border bg-transparent px-3 text-xs transition-colors hover:bg-surface-hover"
                      >
                        <ExternalLink className="mr-1 h-3 w-3" />
                        {t("diagnostics.actionSymbols")}
                      </a>
                    );
                  }
                  if (
                    action.type === "remove_instrument" &&
                    action.target !== undefined
                  ) {
                    const target = action.target;
                    return (
                      <Button
                        key={ai}
                        type="button"
                        variant="outline"
                        size="sm"
                        disabled={busy}
                        onClick={() => onDeleteInstrument(instance, target)}
                      >
                        {t("diagnostics.actionRemove")}
                      </Button>
                    );
                  }
                  return null;
                })}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

/** Inline outcome of a symbol verification: success, not-found, or not-found
 *  with a case-folded suggestion. Renders nothing until a result is present. */
function VerifyResult({
  result,
}: {
  result: MarketDataSymbolVerification | null;
}) {
  const { t } = useTranslation("marketData");
  if (!result || !result.supported) return null;
  if (result.exists) {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-[var(--ok)]">
        <Check className="h-3 w-3" />
        {t("verify.exists")}
      </span>
    );
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1 text-xs text-[var(--danger)]">
      <X className="h-3 w-3" />
      {t("verify.notFound")}
      {result.suggestion && (
        <span className="text-muted">
          {t("verify.didYouMean", { suggestion: result.suggestion })}
        </span>
      )}
    </span>
  );
}

function InstanceCard({
  instance,
  busy,
  onVerifySymbol,
  onToggleInstance,
  onDeleteInstance,
  onUpsertInstrument,
  onToggleInstrument,
  onDeleteInstrument,
  onRestart,
}: {
  instance: MarketDataInstance;
  busy: boolean;
  onVerifySymbol: (
    instance: MarketDataInstance,
    externalSymbol: string,
  ) => Promise<MarketDataSymbolVerification | null>;
  onToggleInstance: (instance: MarketDataInstance) => void;
  onDeleteInstance: (instance: MarketDataInstance) => void;
  onUpsertInstrument: (
    instance: MarketDataInstance,
    draft: InstrumentDraft,
  ) => Promise<boolean>;
  onToggleInstrument: (
    instance: MarketDataInstance,
    externalSymbol: string,
    enabled: boolean,
  ) => void;
  onDeleteInstrument: (instance: MarketDataInstance, externalSymbol: string) => void;
  onRestart: () => void;
}) {
  const { t } = useTranslation("marketData");
  const [draft, setDraft] = useState<InstrumentDraft>(emptyInstrument);
  // Verification is presentation-only state local to this card: the draft-row
  // result, the row currently verifying, and per-row results keyed by symbol.
  const [draftVerify, setDraftVerify] =
    useState<MarketDataSymbolVerification | null>(null);
  const [verifying, setVerifying] = useState<string | null>(null);
  const [rowVerify, setRowVerify] = useState<
    Record<string, MarketDataSymbolVerification>
  >({});

  // The provider gates the button regardless of runtime state; an empty draft
  // symbol also disables it (nothing to check).
  const canVerify = instance.verifiesSymbols;
  const disabledTitle = canVerify ? undefined : t("verify.unsupported");

  // Only the manual (bring-your-own) provider exposes the operator-set mark; for
  // streaming providers the price comes from the source, so the input and column
  // are hidden.
  const isManual = instance.type === MANUAL_PROVIDER;

  const submitInstrument = async () => {
    if (await onUpsertInstrument(instance, draft)) {
      setDraft(emptyInstrument);
      setDraftVerify(null);
    }
  };

  // Draft-row verify: marker key "" tracks the draft slot in `verifying`.
  const verifyDraft = async () => {
    setVerifying("");
    setDraftVerify(null);
    try {
      setDraftVerify(await onVerifySymbol(instance, draft.externalSymbol));
    } finally {
      setVerifying(null);
    }
  };

  const verifyRow = async (externalSymbol: string) => {
    setVerifying(externalSymbol);
    try {
      const result = await onVerifySymbol(instance, externalSymbol);
      setRowVerify((prev) => {
        const next = { ...prev };
        if (result) {
          next[externalSymbol] = result;
        } else {
          delete next[externalSymbol];
        }
        return next;
      });
    } finally {
      setVerifying(null);
    }
  };

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-3">
        <div className="min-w-0">
          <CardTitle className="truncate">{providerTitle(instance)}</CardTitle>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <Badge variant="neutral">{instance.type}</Badge>
            <Badge variant={instance.enabled ? "ok" : "neutral"}>
              {instance.enabled ? t("state.enabled") : t("state.disabled")}
            </Badge>
            {instance.state === "ok" && (
              <Badge variant="ok">{t("state.live")}</Badge>
            )}
            {instance.state === "pending" && (
              <Badge variant="warn">{t("state.notApplied")}</Badge>
            )}
            {instance.state === "error" && (
              <Badge variant="danger">{t("state.error")}</Badge>
            )}
          </div>
          {instance.state === "error" && instance.error && (
            <p className="mt-1.5 text-xs text-[var(--danger)]">
              {t("instanceError", { message: instance.error })}
            </p>
          )}
          {instance.references &&
            (instance.references.docsUrl || instance.references.symbolsUrl) && (
              <div className="mt-1.5 flex flex-wrap gap-3">
                {instance.references.docsUrl && (
                  <a
                    href={instance.references.docsUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 text-xs text-muted hover:text-text"
                  >
                    <ExternalLink className="h-3 w-3" />
                    {t("references.docs")}
                  </a>
                )}
                {instance.references.symbolsUrl && (
                  <a
                    href={instance.references.symbolsUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 text-xs text-muted hover:text-text"
                  >
                    <ExternalLink className="h-3 w-3" />
                    {t("references.symbols")}
                  </a>
                )}
              </div>
            )}
        </div>
        <div className="flex shrink-0 gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => onToggleInstance(instance)}
            disabled={busy}
          >
            {instance.enabled ? t("actions.disable") : t("actions.enable")}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={t("actions.deleteInstance")}
            onClick={() => onDeleteInstance(instance)}
            disabled={busy}
          >
            <Trash2 />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div
          className={
            isManual
              ? "grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_0.8fr_auto_auto]"
              : "grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_auto_auto]"
          }
        >
          <div className="flex gap-2">
            <Input
              value={draft.externalSymbol}
              onChange={(e) => {
                const externalSymbol = e.target.value;
                setDraft((d) => ({ ...d, externalSymbol }));
                setDraftVerify(null);
              }}
              placeholder={t("instrument.externalSymbol")}
              disabled={busy}
            />
            <Button
              type="button"
              variant="outline"
              size="icon"
              aria-label={t("verify.action")}
              title={disabledTitle}
              disabled={
                busy ||
                !canVerify ||
                draft.externalSymbol.trim() === "" ||
                verifying === ""
              }
              onClick={() => {
                void verifyDraft();
              }}
            >
              <SearchCheck />
            </Button>
          </div>
          <Input
            value={draft.baseAsset}
            onChange={(e) => setDraft((d) => ({ ...d, baseAsset: e.target.value }))}
            placeholder={t("instrument.baseAsset")}
            disabled={busy}
          />
          <Input
            value={draft.quoteAsset}
            onChange={(e) =>
              setDraft((d) => ({ ...d, quoteAsset: e.target.value }))
            }
            placeholder={t("instrument.quoteAsset")}
            disabled={busy}
          />
          {isManual && (
            <Input
              value={draft.manualPrice}
              inputMode="decimal"
              onChange={(e) =>
                setDraft((d) => ({ ...d, manualPrice: e.target.value }))
              }
              placeholder={t("instrument.manualPrice")}
              disabled={busy}
            />
          )}
          <Button
            type="button"
            variant={draft.enabled ? "default" : "outline"}
            onClick={() => setDraft((d) => ({ ...d, enabled: !d.enabled }))}
            disabled={busy}
          >
            {draft.enabled ? t("state.enabled") : t("state.disabled")}
          </Button>
          <Button
            type="button"
            onClick={() => {
              void submitInstrument();
            }}
            disabled={busy}
          >
            <Plus />
            {t("actions.addInstrument")}
          </Button>
        </div>

        {draftVerify && (
          <div className="-mt-2">
            <VerifyResult result={draftVerify} />
          </div>
        )}

        <div className="overflow-x-auto">
          <table className="w-full min-w-[760px] text-left text-sm">
            <thead className="border-b border-border text-xs uppercase text-muted">
              <tr>
                <th className="py-2 pr-3">{t("table.external")}</th>
                <th className="py-2 pr-3">{t("table.instrument")}</th>
                {isManual && (
                  <th className="py-2 pr-3">{t("table.manualPrice")}</th>
                )}
                <th className="py-2 pr-3">{t("table.price")}</th>
                <th className="py-2 pr-3">{t("table.asOf")}</th>
                <th className="py-2 pr-3">{t("table.state")}</th>
                <th className="py-2 pr-0 text-right">{t("table.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {instance.instruments.length === 0 && (
                <tr>
                  <td
                    className="py-5 text-center text-muted"
                    colSpan={isManual ? 7 : 6}
                  >
                    {t("emptyInstruments")}
                  </td>
                </tr>
              )}
              {instance.instruments.map((instrument) => (
                <tr key={instrument.externalSymbol} className="border-b border-border">
                  <td className="py-2 pr-3 font-medium text-text">
                    {instrument.externalSymbol}
                  </td>
                  <td className="py-2 pr-3 text-muted">
                    {instrument.baseAsset}/{instrument.quoteAsset}
                  </td>
                  {isManual && (
                    <td className="nums py-2 pr-3 text-text">
                      {instrument.manualPrice || "—"}
                    </td>
                  )}
                  <td className="nums py-2 pr-3 text-text">
                    {bestPriceLabel(instrument.quote) || "—"}
                  </td>
                  <td className="nums py-2 pr-3 text-muted">
                    {instrument.quote ? formatDateTime(instrument.quote.asOf) : "—"}
                  </td>
                  <td className="py-2 pr-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant={instrument.enabled ? "ok" : "neutral"}>
                        {instrument.enabled
                          ? t("state.enabled")
                          : t("state.disabled")}
                      </Badge>
                      {instrument.stale && (
                        <Badge variant="warn">{t("state.stale")}</Badge>
                      )}
                      <VerifyResult
                        result={rowVerify[instrument.externalSymbol] ?? null}
                      />
                    </div>
                  </td>
                  <td className="py-2 pr-0">
                    <div className="flex justify-end gap-2">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() =>
                          onToggleInstrument(
                            instance,
                            instrument.externalSymbol,
                            !instrument.enabled,
                          )
                        }
                        disabled={busy}
                      >
                        {instrument.enabled
                          ? t("actions.disable")
                          : t("actions.enable")}
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label={t("verify.action")}
                        title={disabledTitle}
                        onClick={() => {
                          void verifyRow(instrument.externalSymbol);
                        }}
                        disabled={
                          busy ||
                          !canVerify ||
                          verifying === instrument.externalSymbol
                        }
                      >
                        <SearchCheck />
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label={t("actions.deleteInstrument")}
                        onClick={() =>
                          onDeleteInstrument(instance, instrument.externalSymbol)
                        }
                        disabled={busy}
                      >
                        <Trash2 />
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        {instance.diagnostics.length > 0 && (() => {
          const configGroup = instance.diagnostics.filter(
            (d) => d.kind === "config",
          );
          const providerGroup = instance.diagnostics.filter(
            (d) => d.kind === "environment" || d.kind === "provider",
          );
          return (
            <div className="space-y-4">
              <DiagnosticGroup
                title={t("diagnostics.groupActionNeeded")}
                items={configGroup}
                instance={instance}
                busy={busy}
                onRestart={onRestart}
                onDeleteInstrument={onDeleteInstrument}
              />
              <DiagnosticGroup
                title={t("diagnostics.groupProvider")}
                items={providerGroup}
                instance={instance}
                busy={busy}
                onRestart={onRestart}
                onDeleteInstrument={onDeleteInstrument}
              />
            </div>
          );
        })()}
      </CardContent>
    </Card>
  );
}

/** Market-data control-plane page. */
export function MarketData() {
  const { t } = useTranslation("marketData");
  const { t: tc } = useTranslation();
  const { load, reload } = useMarketData();
  const [busy, setBusy] = useState(false);
  const [mutationError, setMutationError] = useState("");
  const [form, setForm] = useState<InstanceForm>({
    id: "",
    type: "byo",
    label: "",
    credentials: "",
    enabled: false,
  });

  const providers = load.state === "ready" ? load.data.providers : [];
  const providerOptions =
    providers.length > 0 ? providers : [{ type: "byo", title: "BYO" }];

  const run = async (fn: () => Promise<void>): Promise<boolean> => {
    setBusy(true);
    setMutationError("");
    try {
      await fn();
      reload();
      return true;
    } catch (err) {
      setMutationError(err instanceof Error ? err.message : String(err));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const createInstance = () =>
    run(async () => {
      await createMarketDataInstance(form);
      setForm({ id: "", type: form.type, label: "", credentials: "", enabled: false });
    });

  const restart = () =>
    run(async () => {
      await restartMarketData();
    });

  // Verify is non-mutating: it neither toggles page busy nor reloads, so the
  // operator can keep editing. A transport/catalogue failure surfaces in the
  // shared error banner and yields no result.
  const verifySymbol = async (
    instance: MarketDataInstance,
    externalSymbol: string,
  ): Promise<MarketDataSymbolVerification | null> => {
    setMutationError("");
    try {
      return await verifyMarketDataSymbol(instance.id, externalSymbol);
    } catch (err) {
      setMutationError(err instanceof Error ? err.message : String(err));
      return null;
    }
  };

  return (
    <Page
      title={t("title")}
      actions={
        <>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={restart}
            disabled={busy}
          >
            <RotateCcw />
            {t("actions.restart")}
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={reload}>
            <RefreshCw />
            {tc("actions.refresh")}
          </Button>
        </>
      }
    >
      <Card>
        <CardHeader className="flex-row items-center gap-2">
          <Database className="h-4 w-4 text-muted" />
          <CardTitle>{t("instances.title")}</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-3 lg:grid-cols-[0.8fr_0.9fr_1.2fr_auto_auto]">
          <div className="space-y-1">
            <Label htmlFor="md-id">{t("instance.id")}</Label>
            <Input
              id="md-id"
              value={form.id}
              onChange={(e) => setForm((f) => ({ ...f, id: e.target.value }))}
              disabled={busy}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="md-type">{t("instance.type")}</Label>
            <Select
              value={form.type}
              onValueChange={(type) => setForm((f) => ({ ...f, type }))}
              disabled={busy}
            >
              <SelectTrigger id="md-type">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {providerOptions.map((provider) => (
                  <SelectItem key={provider.type} value={provider.type}>
                    {provider.title}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1">
            <Label htmlFor="md-label">{t("instance.label")}</Label>
            <Input
              id="md-label"
              value={form.label}
              onChange={(e) => setForm((f) => ({ ...f, label: e.target.value }))}
              disabled={busy}
            />
          </div>
          <div className="flex items-end">
            <Button
              type="button"
              variant={form.enabled ? "default" : "outline"}
              onClick={() => setForm((f) => ({ ...f, enabled: !f.enabled }))}
              disabled={busy}
              className="w-full"
            >
              {form.enabled ? t("state.enabled") : t("state.disabled")}
            </Button>
          </div>
          <div className="flex items-end">
            <Button type="button" onClick={createInstance} disabled={busy}>
              <Plus />
              {t("actions.addInstance")}
            </Button>
          </div>
          <div className="lg:col-span-5 space-y-1">
            <Label htmlFor="md-credentials">{t("instance.credentials")}</Label>
            <Textarea
              id="md-credentials"
              value={form.credentials}
              onChange={(e) =>
                setForm((f) => ({ ...f, credentials: e.target.value }))
              }
              disabled={busy}
              className="min-h-20 font-mono text-xs"
            />
          </div>
        </CardContent>
      </Card>

      {mutationError && <ErrorBanner message={mutationError} />}

      {load.state === "loading" && <TableSkeleton rows={3} cols={5} />}
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" && (
        <div className="space-y-4">
          <p className="text-xs text-muted">{t("instances.restartHint")}</p>
          {load.data.instances.length === 0 && (
            <Card>
              <CardContent className="py-8 text-center text-sm text-muted">
                {t("emptyInstances")}
              </CardContent>
            </Card>
          )}
          {load.data.instances.map((instance) => (
            <InstanceCard
              key={instance.id}
              instance={instance}
              busy={busy}
              onVerifySymbol={verifySymbol}
              onToggleInstance={(target) =>
                run(() =>
                  setMarketDataInstanceEnabled(target.id, !target.enabled),
                )
              }
              onDeleteInstance={(target) =>
                run(() => deleteMarketDataInstance(target.id))
              }
              onUpsertInstrument={(target, draft) =>
                run(() => upsertMarketDataInstrument(target.id, draft).then(() => {}))
              }
              onToggleInstrument={(target, externalSymbol, enabled) =>
                run(() =>
                  setMarketDataInstrumentEnabled(
                    target.id,
                    externalSymbol,
                    enabled,
                  ),
                )
              }
              onDeleteInstrument={(target, externalSymbol) =>
                run(() => deleteMarketDataInstrument(target.id, externalSymbol))
              }
              onRestart={restart}
            />
          ))}
        </div>
      )}
    </Page>
  );
}
