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

/** The kinds accepted for one policy, with a flag for whether all are
 *  required. rate_limit needs both kinds; the others need at least one. */
export interface KindSpec {
  kind: string;
  /** A short unit/format hint shown next to the value input. */
  hint: string;
}

export const POLICY_KINDS: Record<Policy, KindSpec[]> = {
  rate_limit: [
    { kind: "max_orders", hint: "integer > 0, <= 1e9" },
    { kind: "window", hint: "Go duration, > 0, <= 24h (e.g. 1s, 500ms)" },
  ],
  order_size_limit: [
    { kind: "max_quantity", hint: "positive decimal" },
    { kind: "max_notional", hint: "positive decimal" },
  ],
  pnl_bounds_kill_switch: [
    { kind: "lower_bound", hint: "decimal" },
    { kind: "upper_bound", hint: "decimal" },
    { kind: "initial_pnl", hint: "decimal, sets the starting realized PnL; only on creation" },
  ],
};

/** Human-readable policy labels for selects and chips. */
export const POLICY_LABELS: Record<Policy, string> = {
  rate_limit: "Rate limit",
  order_size_limit: "Order size limit",
  pnl_bounds_kill_switch: "PnL bounds kill-switch",
};

/** Human-readable scope labels for selects and chips. */
export const SCOPE_LABELS: Record<Scope, string> = {
  broker: "Broker",
  asset: "Asset",
  account: "Account",
  account_asset: "Account + asset",
};

export function isPolicy(value: string): value is Policy {
  return (POLICIES as readonly string[]).includes(value);
}

export function isScope(value: string): value is Scope {
  return (SCOPES as readonly string[]).includes(value);
}

// --- Policy catalog ---
// Rich metadata for the Policies page. One entry per policy.

/** One field descriptor inside a policy catalog entry. */
export interface PolicyField {
  key: string;
  /** Human words, not the mnemonic key. */
  label: string;
  hint: string;
}

/** Full catalog entry for a policy or pseudo-policy. */
export interface PolicyCatalogEntry {
  id: string;
  label: string;
  description: string;
  /** Canonical wiki URL. */
  wikiUrl: string;
  fields: PolicyField[];
}

export const POLICY_CATALOG: PolicyCatalogEntry[] = [
  {
    id: "rate_limit",
    label: "Rate limit",
    description:
      "Counts order attempts within a time window and rejects bursts beyond the cap. Rejected attempts still count, so retries cannot bypass it.",
    wikiUrl: "https://github.com/openpitkit/pit/wiki/Policies#ratelimitpolicy",
    fields: [
      {
        key: "max_orders",
        label: "max orders",
        hint: "integer greater than 0",
      },
      {
        key: "window",
        label: "window",
        hint: "duration up to 24h, e.g. 1s, 500ms",
      },
    ],
  },
  {
    id: "order_size_limit",
    label: "Order size limit",
    description:
      "Caps the size of a single order by quantity and notional value to prevent fat-finger errors.",
    wikiUrl:
      "https://github.com/openpitkit/pit/wiki/Policies#ordersizelimitpolicy",
    fields: [
      {
        key: "max_quantity",
        label: "max quantity",
        hint: "positive decimal",
      },
      {
        key: "max_notional",
        label: "max notional",
        hint: "positive decimal",
      },
    ],
  },
  {
    id: "pnl_bounds_kill_switch",
    label: "P&L kill switch",
    description:
      "Tracks accumulated realized P&L and blocks the account when it crosses a configured lower or upper bound.",
    wikiUrl:
      "https://github.com/openpitkit/pit/wiki/Policies#pnlboundskillswitchpolicy",
    fields: [
      {
        key: "lower_bound",
        label: "lower bound",
        hint: "decimal, e.g. -1000",
      },
      {
        key: "upper_bound",
        label: "upper bound",
        hint: "decimal, e.g. 500",
      },
      {
        key: "initial_pnl",
        label: "initial PnL",
        hint: "decimal — seeds the starting realized PnL; settable only when creating an account+asset barrier",
      },
    ],
  },
];

/** Look up a catalog entry by policy id. */
export function getPolicyCatalogEntry(
  id: string,
): PolicyCatalogEntry | undefined {
  return POLICY_CATALOG.find((e) => e.id === id);
}

