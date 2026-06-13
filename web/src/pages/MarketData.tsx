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

import { Database, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createMarketDataInstance,
  deleteMarketDataInstance,
  deleteMarketDataInstrument,
  setMarketDataInstanceEnabled,
  setMarketDataInstrumentEnabled,
  upsertMarketDataInstrument,
} from "@/api/client";
import type { MarketDataInstance } from "@/api/types";
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
  enabled: boolean;
}

const emptyInstrument: InstrumentDraft = {
  externalSymbol: "",
  baseAsset: "",
  quoteAsset: "USD",
  enabled: true,
};

function providerTitle(instance: MarketDataInstance): string {
  return instance.label || instance.id;
}

function bestPriceLabel(
  quote: MarketDataInstance["instruments"][number]["quote"],
): string {
  if (!quote) return "";
  return quote.mark || quote.bid || quote.ask;
}

function InstanceCard({
  instance,
  busy,
  onToggleInstance,
  onDeleteInstance,
  onUpsertInstrument,
  onToggleInstrument,
  onDeleteInstrument,
}: {
  instance: MarketDataInstance;
  busy: boolean;
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
}) {
  const { t } = useTranslation("marketData");
  const [draft, setDraft] = useState<InstrumentDraft>(emptyInstrument);

  const submitInstrument = async () => {
    if (await onUpsertInstrument(instance, draft)) {
      setDraft(emptyInstrument);
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
          </div>
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
        <div className="grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_auto_auto]">
          <Input
            value={draft.externalSymbol}
            onChange={(e) =>
              setDraft((d) => ({ ...d, externalSymbol: e.target.value }))
            }
            placeholder={t("instrument.externalSymbol")}
            disabled={busy}
          />
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

        <div className="overflow-x-auto">
          <table className="w-full min-w-[760px] text-left text-sm">
            <thead className="border-b border-border text-xs uppercase text-muted">
              <tr>
                <th className="py-2 pr-3">{t("table.external")}</th>
                <th className="py-2 pr-3">{t("table.instrument")}</th>
                <th className="py-2 pr-3">{t("table.price")}</th>
                <th className="py-2 pr-3">{t("table.asOf")}</th>
                <th className="py-2 pr-3">{t("table.state")}</th>
                <th className="py-2 pr-0 text-right">{t("table.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {instance.instruments.length === 0 && (
                <tr>
                  <td className="py-5 text-center text-muted" colSpan={6}>
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
                  <td className="nums py-2 pr-3 text-text">
                    {bestPriceLabel(instrument.quote) || "—"}
                  </td>
                  <td className="nums py-2 pr-3 text-muted">
                    {instrument.quote ? formatDateTime(instrument.quote.asOf) : "—"}
                  </td>
                  <td className="py-2 pr-3">
                    <div className="flex gap-2">
                      <Badge variant={instrument.enabled ? "ok" : "neutral"}>
                        {instrument.enabled
                          ? t("state.enabled")
                          : t("state.disabled")}
                      </Badge>
                      {instrument.stale && (
                        <Badge variant="warn">{t("state.stale")}</Badge>
                      )}
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

  return (
    <Page
      title={t("title")}
      actions={
        <Button type="button" variant="ghost" size="sm" onClick={reload}>
          <RefreshCw />
          {tc("actions.refresh")}
        </Button>
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
            />
          ))}
        </div>
      )}
    </Page>
  );
}
