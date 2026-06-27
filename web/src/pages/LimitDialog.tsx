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
import { ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { Limit } from "@/api/types";
import { validateLimit } from "@/api/validate";
import {
  getAllowedScopes,
  getPolicies,
  getPolicyCatalogEntry,
  getPolicyKinds,
  kindHint,
  policyCatalogDescription,
  policyFieldHint,
  policyFieldLabel,
  policyLabel,
  scopeHasAccount,
  scopeHasAsset,
  scopeLabel,
  type Policy,
  type Scope,
} from "@/api/vocabulary";
import { ApiError, useOfficerApi } from "@/framework";
import { Autocomplete } from "@/components/Autocomplete";
import { ErrorBanner } from "@/components/PageStates";
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

// ---------------------------------------------------------------------------
// Duration picker helpers (for the rate_limit `window` field)
// ---------------------------------------------------------------------------

type DurationUnit = "ms" | "s" | "m" | "h";

interface DurationParts {
  amount: string; // numeric string as typed
  unit: DurationUnit;
}

const DURATION_UNITS: DurationUnit[] = ["ms", "s", "m", "h"];

/** Parse a Go-duration string (e.g. "500ms", "12s", "2m", "1h") into parts.
 *  Returns null when the string is not a recognised single-unit duration. */
function parseDuration(s: string): DurationParts | null {
  const trimmed = s.trim();
  const m = /^(\d+(?:\.\d+)?)(ms|s|m|h)$/.exec(trimmed);
  if (!m) {
    return null;
  }
  return { amount: m[1], unit: m[2] as DurationUnit };
}

/** Assemble a Go-duration string from {amount, unit}. Returns empty string
 *  when amount is blank or zero. */
function assembleDuration(parts: DurationParts): string {
  const n = Number(parts.amount);
  if (!parts.amount || !Number.isFinite(n) || n <= 0) {
    return "";
  }
  return `${parts.amount}${parts.unit}`;
}

/** Validate a fully assembled Go-duration string for the window field: must be
 *  > 0 and <= 24h (86400 seconds). Returns a key in the `policies.duration`
 *  group (rendered via `t`) or null. */
function validateDurationString(s: string): string | null {
  if (!s) return "duration.required";
  const parts = parseDuration(s);
  if (!parts) return "duration.invalid";
  const n = Number(parts.amount);
  if (!Number.isFinite(n) || n <= 0) return "duration.mustBePositive";
  // Convert to ms for the 24h ceiling check.
  const toMs: Record<DurationUnit, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 };
  if (n * toMs[parts.unit] > 86_400_000) return "duration.mustNotExceed24h";
  return null;
}

// ---------------------------------------------------------------------------
// Duration picker component
// ---------------------------------------------------------------------------

function DurationPicker({
  id,
  value,
  onChange,
}: {
  id: string;
  value: string; // Go-duration string (may be empty)
  onChange: (goStr: string) => void;
}) {
  const { t } = useTranslation("policies");
  const initial = parseDuration(value) ?? { amount: "", unit: "s" as DurationUnit };
  const [parts, setParts] = useState<DurationParts>(initial);

  // Sync inward when the value changes externally (dialog reopen).
  useEffect(() => {
    const parsed = parseDuration(value);
    if (parsed) {
      // Intentional inward sync from the external Go-duration string.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setParts(parsed);
    } else if (!value) {
      setParts({ amount: "", unit: "s" });
    }
  }, [value]);

  const update = (next: DurationParts) => {
    setParts(next);
    onChange(assembleDuration(next));
  };

  const durationError = parts.amount ? validateDurationString(assembleDuration(parts)) : null;

  return (
    <div className="space-y-1.5">
      <div className="flex gap-2">
        <Input
          id={id}
          type="number"
          min={1}
          step={1}
          value={parts.amount}
          spellCheck={false}
          className="w-28"
          placeholder={t("duration.amountPlaceholder")}
          onChange={(e) => update({ ...parts, amount: e.target.value })}
        />
        <Select
          value={parts.unit}
          onValueChange={(v) => update({ ...parts, unit: v as DurationUnit })}
        >
          <SelectTrigger className="flex-1">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {DURATION_UNITS.map((v) => (
              <SelectItem key={v} value={v}>
                {t(`duration.unit.${v}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {durationError && (
        <p className="text-[0.6875rem] text-[var(--danger)]">{t(durationError)}</p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------

interface FormState {
  policy: Policy;
  scope: Scope;
  account: string;
  asset: string;
  values: Record<string, string>;
}

function emptyForm(initialAccount = ""): FormState {
  return {
    policy: "rate_limit",
    scope: "broker",
    account: initialAccount,
    asset: "",
    values: {},
  };
}

function fromLimit(limit: Limit): FormState {
  return {
    policy: limit.policy as Policy,
    scope: limit.scope as Scope,
    account: limit.account,
    asset: limit.asset,
    values: { ...limit.values },
  };
}

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

/**
 * Create/edit dialog for one policy barrier. When `editing` is set the policy,
 * scope, account, and asset are locked (they form the barrier's identity) and
 * only the values are editable; otherwise all fields are editable. Validation
 * mirrors the cross-layer contract before the request is sent. Value fields are
 * labeled with human words from the policy catalog.
 */
export function LimitDialog({
  open,
  editing,
  initialAccount = "",
  assetSuggestions = [],
  accountSuggestions = [],
  policyCounts = {},
  onOpenChange,
  onSaved,
}: {
  open: boolean;
  editing: Limit | null;
  initialAccount?: string;
  assetSuggestions?: string[];
  accountSuggestions?: string[];
  policyCounts?: Partial<Record<Policy, number>>;
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation("policies");
  const { t: tc } = useTranslation();
  const { putLimit } = useOfficerApi();
  const [form, setForm] = useState<FormState>(() => emptyForm(initialAccount));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);

  // Reset the form whenever the dialog opens, seeding from the edited barrier.
  useEffect(() => {
    if (open) {
      // Reseed the form from the edited barrier each time the dialog opens.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setForm(editing ? fromLimit(editing) : emptyForm(initialAccount));
      setError(null);
      setBusy(false);
      setConfirmOpen(false);
    }
  }, [open, editing, initialAccount]);

  const isEdit = editing !== null;
  const allowedScopes = getAllowedScopes(form.policy);
  // Filter initial_pnl: only visible (and sendable) when creating an
  // account_asset pnl_bounds_kill_switch barrier.
  const kinds = getPolicyKinds(form.policy).filter(({ kind }) => {
    if (kind !== "initial_pnl") return true;
    return (
      !isEdit &&
      form.policy === "pnl_bounds_kill_switch" &&
      form.scope === "account_asset"
    );
  });

  // Pull the catalog entry for the current policy for descriptions + human labels.
  const catalogEntry = getPolicyCatalogEntry(form.policy);

  const candidate = useMemo<Limit>(() => {
    const values: Record<string, string> = {};
    for (const { kind } of kinds) {
      const v = form.values[kind];
      if (v !== undefined && v.trim().length > 0) {
        values[kind] = v.trim();
      }
    }
    return {
      policy: form.policy,
      scope: form.scope,
      account: scopeHasAccount(form.scope) ? form.account.trim() : "",
      asset: scopeHasAsset(form.scope) ? form.asset.trim() : "",
      values,
    };
  }, [form, kinds]);

  const validation = validateLimit(candidate);
  const requiresEngineRebuild =
    !isEdit && (policyCounts[form.policy] ?? 0) === 0;

  const setPolicy = (policy: Policy) => {
    // Switching policy resets scope to the first allowed one and clears values,
    // since the kind set differs per policy.
    setForm((prev) => ({
      ...prev,
      policy,
      scope: getAllowedScopes(policy)[0] ?? "",
      values: {},
    }));
  };

  const setScope = (scope: Scope) => {
    setForm((prev) => ({ ...prev, scope }));
  };

  const setValue = (kind: string, value: string) => {
    setForm((prev) => ({
      ...prev,
      values: { ...prev.values, [kind]: value },
    }));
  };

  const requestSubmit = () => {
    if (validation) {
      setError(t(validation.key, validation.values));
      return;
    }
    if (requiresEngineRebuild) {
      setConfirmOpen(true);
      return;
    }
    void submit();
  };

  const submit = async () => {
    if (validation) {
      setError(t(validation.key, validation.values));
      setConfirmOpen(false);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await putLimit(candidate);
      setConfirmOpen(false);
      onOpenChange(false);
      onSaved();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  return (
    <>
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {isEdit ? t("dialog.editTitle") : t("dialog.addTitle")}
          </DialogTitle>
          {catalogEntry && (
            <DialogDescription>
              {policyCatalogDescription(t, catalogEntry.id)}{" "}
              <a
                href={catalogEntry.wikiUrl}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-0.5 text-accent hover:underline"
              >
                {t("wikiDetails")}
                <ExternalLink className="h-3 w-3" />
              </a>
            </DialogDescription>
          )}
        </DialogHeader>

        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label>{t("dialog.policy")}</Label>
              <Select
                value={form.policy}
                onValueChange={setPolicy}
                disabled={isEdit}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {getPolicies().map((p) => (
                    <SelectItem key={p} value={p}>
                      {policyLabel(tc, p)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1.5">
              <Label>{t("dialog.scope")}</Label>
              <Select
                value={form.scope}
                onValueChange={setScope}
                disabled={isEdit}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {allowedScopes.map((s) => (
                    <SelectItem key={s} value={s}>
                      {scopeLabel(tc, s)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          {(scopeHasAccount(form.scope) || scopeHasAsset(form.scope)) && (
            <div className="grid grid-cols-2 gap-3">
              {scopeHasAccount(form.scope) && (
                <div className="space-y-1.5">
                  <Label htmlFor="limit-account">{t("dialog.account")}</Label>
                  <Autocomplete
                    id="limit-account"
                    value={form.account}
                    spellCheck={false}
                    placeholder={t("dialog.accountPlaceholder")}
                    disabled={isEdit}
                    suggestions={accountSuggestions}
                    onChange={(v) =>
                      setForm((prev) => ({ ...prev, account: v }))
                    }
                  />
                </div>
              )}
              {scopeHasAsset(form.scope) && (
                <div className="space-y-1.5">
                  <Label htmlFor="limit-asset">{t("dialog.asset")}</Label>
                  <Autocomplete
                    id="limit-asset"
                    value={form.asset}
                    spellCheck={false}
                    placeholder={t("dialog.assetPlaceholder")}
                    disabled={isEdit}
                    suggestions={assetSuggestions}
                    onChange={(v) =>
                      setForm((prev) => ({ ...prev, asset: v }))
                    }
                  />
                </div>
              )}
            </div>
          )}

          <div className="space-y-3 rounded-card border border-border p-3">
            <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
              {t("dialog.values")}
            </p>
            {kinds.map(({ kind }) => {
              // Human label/hint come from the policy catalog (domain ns), keyed
              // by policy id + field key; the field-label helper falls back to
              // the mnemonic key, and the hint falls back to the kind hint, so
              // unknown fields still render.
              const fieldLabel = policyFieldLabel(t, form.policy, kind);
              const catalogHint = policyFieldHint(t, form.policy, kind);
              const fieldHint = catalogHint || kindHint(t, form.policy, kind);
              const isDuration = kind === "window";
              return (
                <div key={kind} className="space-y-1.5">
                  <Label htmlFor={`kind-${kind}`}>{fieldLabel}</Label>
                  {isDuration ? (
                    <DurationPicker
                      id={`kind-${kind}`}
                      value={form.values[kind] ?? ""}
                      onChange={(v) => setValue(kind, v)}
                    />
                  ) : (
                    <>
                      <Input
                        id={`kind-${kind}`}
                        value={form.values[kind] ?? ""}
                        spellCheck={false}
                        onChange={(e) => setValue(kind, e.target.value)}
                      />
                      <p className="text-[0.6875rem] text-muted">{fieldHint}</p>
                    </>
                  )}
                </div>
              );
            })}
            <p className="text-[0.6875rem] text-muted">
              {form.policy === "rate_limit"
                ? t("dialog.rateLimitFootnote")
                : t("dialog.atLeastOneFootnote")}
            </p>
          </div>

          {validation && (
            <p className="text-[0.6875rem] text-[var(--danger)]">
              {t(validation.key, validation.values)}
            </p>
          )}
          {error && !confirmOpen && (
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
            {tc("actions.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={requestSubmit}
            disabled={busy || validation !== null}
          >
            {isEdit ? t("dialog.save") : t("dialog.addTitle")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
    <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("restartConfirm.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("restartConfirm.description")}
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
          >
            {t("restartConfirm.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
    </>
  );
}
