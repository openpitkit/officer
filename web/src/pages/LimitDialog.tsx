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

// ---------------------------------------------------------------------------
// Duration picker helpers (for the rate_limit `window` field)
// ---------------------------------------------------------------------------

type DurationUnit = "ms" | "s" | "m" | "h";

interface DurationParts {
  amount: string; // numeric string as typed
  unit: DurationUnit;
}

const DURATION_UNITS: { value: DurationUnit; label: string }[] = [
  { value: "ms", label: "milliseconds" },
  { value: "s", label: "seconds" },
  { value: "m", label: "minutes" },
  { value: "h", label: "hours" },
];

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

/** Validate a fully assembled Go-duration string for the window field:
 *  must be > 0 and <= 24h (86400 seconds). Returns an error string or null. */
function validateDurationString(s: string): string | null {
  if (!s) return "Required.";
  const parts = parseDuration(s);
  if (!parts) return "Invalid duration.";
  const n = Number(parts.amount);
  if (!Number.isFinite(n) || n <= 0) return "Must be greater than 0.";
  // Convert to ms for the 24h ceiling check.
  const toMs: Record<DurationUnit, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 };
  if (n * toMs[parts.unit] > 86_400_000) return "Must not exceed 24 hours.";
  return null;
}

import { ApiError, putLimit } from "@/api/client";
import type { Limit } from "@/api/types";
import { validateLimit } from "@/api/validate";
import {
  ALLOWED_SCOPES,
  POLICIES,
  POLICY_KINDS,
  POLICY_LABELS,
  SCOPE_LABELS,
  getPolicyCatalogEntry,
  scopeHasAccount,
  scopeHasAsset,
  type Policy,
  type Scope,
} from "@/api/vocabulary";
import { Autocomplete } from "@/components/Autocomplete";
import { ErrorBanner } from "@/components/PageStates";
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
  const initial = parseDuration(value) ?? { amount: "", unit: "s" as DurationUnit };
  const [parts, setParts] = useState<DurationParts>(initial);

  // Sync inward when the value changes externally (dialog reopen).
  useEffect(() => {
    const parsed = parseDuration(value);
    if (parsed) {
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
          placeholder="e.g. 30"
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
            {DURATION_UNITS.map(({ value: v, label }) => (
              <SelectItem key={v} value={v}>
                {label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {durationError && (
        <p className="text-[0.6875rem] text-[var(--danger)]">{durationError}</p>
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
  onOpenChange,
  onSaved,
}: {
  open: boolean;
  editing: Limit | null;
  initialAccount?: string;
  assetSuggestions?: string[];
  accountSuggestions?: string[];
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState<FormState>(() => emptyForm(initialAccount));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Reset the form whenever the dialog opens, seeding from the edited barrier.
  useEffect(() => {
    if (open) {
      setForm(editing ? fromLimit(editing) : emptyForm(initialAccount));
      setError(null);
      setBusy(false);
    }
  }, [open, editing, initialAccount]);

  const isEdit = editing !== null;
  const allowedScopes = ALLOWED_SCOPES[form.policy];
  const kinds = POLICY_KINDS[form.policy];

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

  const setPolicy = (policy: Policy) => {
    // Switching policy resets scope to the first allowed one and clears values,
    // since the kind set differs per policy.
    setForm((prev) => ({
      ...prev,
      policy,
      scope: ALLOWED_SCOPES[policy][0],
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

  const submit = async () => {
    if (validation) {
      setError(validation);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await putLimit(candidate);
      onOpenChange(false);
      onSaved();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{isEdit ? "Edit policy" : "Add policy"}</DialogTitle>
          {catalogEntry && (
            <DialogDescription>
              {catalogEntry.description}{" "}
              <a
                href={catalogEntry.wikiUrl}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-0.5 text-accent hover:underline"
              >
                Details
                <ExternalLink className="h-3 w-3" />
              </a>
            </DialogDescription>
          )}
        </DialogHeader>

        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label>Policy</Label>
              <Select
                value={form.policy}
                onValueChange={(v) => setPolicy(v as Policy)}
                disabled={isEdit}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {POLICIES.map((p) => (
                    <SelectItem key={p} value={p}>
                      {POLICY_LABELS[p]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1.5">
              <Label>Scope</Label>
              <Select
                value={form.scope}
                onValueChange={(v) => setScope(v as Scope)}
                disabled={isEdit}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {allowedScopes.map((s) => (
                    <SelectItem key={s} value={s}>
                      {SCOPE_LABELS[s]}
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
                  <Label htmlFor="limit-account">Account</Label>
                  <Autocomplete
                    id="limit-account"
                    value={form.account}
                    spellCheck={false}
                    placeholder="acc-1"
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
                  <Label htmlFor="limit-asset">Asset</Label>
                  <Autocomplete
                    id="limit-asset"
                    value={form.asset}
                    spellCheck={false}
                    placeholder="AAPL"
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
              Values
            </p>
            {kinds.map(({ kind, hint }) => {
              // Use the human label from the catalog if available; fall back to
              // the mnemonic key so unknown policies still render.
              const fieldLabel =
                catalogEntry?.fields.find((f) => f.key === kind)?.label ?? kind;
              const fieldHint =
                catalogEntry?.fields.find((f) => f.key === kind)?.hint ?? hint;
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
                ? "Both max orders and window are required."
                : "At least one value is required."}
            </p>
          </div>

          {validation && (
            <p className="text-[0.6875rem] text-[var(--danger)]">{validation}</p>
          )}
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
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || validation !== null}
          >
            {isEdit ? "Save" : "Add policy"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
