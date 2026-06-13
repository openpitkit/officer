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

// The policy / scope / kind vocabulary. These strings are a single
// cross-layer vocabulary shared byte-identical with the Go domain constants,
// the SQL data, the REST/MCP JSON, and this SPA. No layer renames or re-cases
// them.
//
// Identifiers and structure (ids, allowed scopes, field keys, wiki URLs) live
// here. Their human display text lives in the `domain` namespace keyed by the
// same identifiers; read it through the helper functions below, which take a
// `t` and an identifier and return the localized label/description/hint.

import type { TFunction } from "i18next";

export const POLICIES = [
  "rate_limit",
  "order_size_limit",
  "pnl_bounds_kill_switch",
] as const;

export type Policy = (typeof POLICIES)[number];

export const SCOPES = [
  "broker",
  "asset",
  "account",
  "account_asset",
] as const;

export type Scope = (typeof SCOPES)[number];

/** Scopes permitted for each policy, in declaration order. */
export const ALLOWED_SCOPES: Record<Policy, readonly Scope[]> = {
  rate_limit: ["broker", "asset", "account", "account_asset"],
  order_size_limit: ["broker", "asset", "account_asset"],
  pnl_bounds_kill_switch: ["asset", "account_asset"],
};

/** A scope carries an account axis when the account field is required. */
export function scopeHasAccount(scope: Scope): boolean {
  return scope === "account" || scope === "account_asset";
}

/** A scope carries an asset axis when the asset field is required. */
export function scopeHasAsset(scope: Scope): boolean {
  return scope === "asset" || scope === "account_asset";
}

/** The kinds accepted for one policy. rate_limit needs both kinds; the others
 *  need at least one. The per-kind unit/format hint is domain text - read it
 *  with {@link kindHint}. */
export interface KindSpec {
  kind: string;
}

export const POLICY_KINDS: Record<Policy, KindSpec[]> = {
  rate_limit: [{ kind: "max_orders" }, { kind: "window" }],
  order_size_limit: [{ kind: "max_quantity" }, { kind: "max_notional" }],
  pnl_bounds_kill_switch: [
    { kind: "lower_bound" },
    { kind: "upper_bound" },
    { kind: "initial_pnl" },
  ],
};

/** Localized policy label for selects and chips. Falls back to the raw
 *  identifier for unknown ids so they still render. */
export function policyLabel(t: TFunction, id: string): string {
  return isPolicy(id) ? t(`domain:policyLabel.${id}`) : id;
}

/** Localized scope label for selects and chips. Falls back to the raw
 *  identifier for unknown ids. */
export function scopeLabel(t: TFunction, id: string): string {
  return isScope(id) ? t(`domain:scope.${id}`) : id;
}

/** Localized unit/format hint shown next to a policy's `kind` value input. */
export function kindHint(t: TFunction, policy: Policy, kind: string): string {
  return t(`domain:kindHint.${policy}.${kind}`);
}

export function isPolicy(value: string): value is Policy {
  return (POLICIES as readonly string[]).includes(value);
}

export function isScope(value: string): value is Scope {
  return (SCOPES as readonly string[]).includes(value);
}

// --- Policy catalog ---
// Structural metadata for the Policies page. One entry per policy. The human
// text (entry label/description, field label/hint) is domain text in the
// `domain` namespace, keyed by these ids; read it with the helpers below.

/** One field descriptor inside a policy catalog entry. Only the identifier
 *  `key` lives here; the human label/hint are read via {@link policyFieldLabel}
 *  / {@link policyFieldHint}. */
export interface PolicyField {
  key: string;
}

/** Full catalog entry for a policy or pseudo-policy. */
export interface PolicyCatalogEntry {
  id: string;
  /** Canonical wiki URL. */
  wikiUrl: string;
  fields: PolicyField[];
}

export const POLICY_CATALOG: PolicyCatalogEntry[] = [
  {
    id: "rate_limit",
    wikiUrl: "https://github.com/openpitkit/pit/wiki/Policies#ratelimitpolicy",
    fields: [{ key: "max_orders" }, { key: "window" }],
  },
  {
    id: "order_size_limit",
    wikiUrl:
      "https://github.com/openpitkit/pit/wiki/Policies#ordersizelimitpolicy",
    fields: [{ key: "max_quantity" }, { key: "max_notional" }],
  },
  {
    id: "pnl_bounds_kill_switch",
    wikiUrl:
      "https://github.com/openpitkit/pit/wiki/Policies#pnlboundskillswitchpolicy",
    fields: [
      { key: "lower_bound" },
      { key: "upper_bound" },
      { key: "initial_pnl" },
    ],
  },
];

/** Look up a catalog entry by policy id. */
export function getPolicyCatalogEntry(
  id: string,
): PolicyCatalogEntry | undefined {
  return POLICY_CATALOG.find((e) => e.id === id);
}

/** Localized rich label for a catalog policy (e.g. the dialog heading text).
 *  Distinct from {@link policyLabel}, which is the terse select/chip label. */
export function policyCatalogLabel(t: TFunction, id: string): string {
  return t(`domain:policy.${id}.label`);
}

/** Localized prose description for a catalog policy. */
export function policyCatalogDescription(t: TFunction, id: string): string {
  return t(`domain:policy.${id}.description`);
}

/** Localized human label for a policy catalog field, by policy id + field key.
 *  Falls back to the raw field key for unknown keys. */
export function policyFieldLabel(
  t: TFunction,
  policyId: string,
  fieldKey: string,
): string {
  return t(`domain:policyField.${policyId}.${fieldKey}.label`, {
    defaultValue: fieldKey,
  });
}

/** Localized format hint for a policy catalog field, by policy id + field key.
 *  Returns the empty string for unknown keys (no hint to show). */
export function policyFieldHint(
  t: TFunction,
  policyId: string,
  fieldKey: string,
): string {
  return t(`domain:policyField.${policyId}.${fieldKey}.hint`, {
    defaultValue: "",
  });
}

