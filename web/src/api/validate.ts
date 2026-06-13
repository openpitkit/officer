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

// Client-side mirrors of the backend's domain.Validate* rules. The backend is
// authoritative; these only catch obvious mistakes before a request goes out
// and drive the form hints. They must not diverge from the contract.

import type { Limit } from "@/api/types";
import {
  ALLOWED_SCOPES,
  isPolicy,
  isScope,
  scopeHasAccount,
  scopeHasAsset,
  type Policy,
  type Scope,
} from "@/api/vocabulary";

const MAX_ACCOUNT_LEN = 64;
const MAX_ASSET_LEN = 32;
const MAX_ORDERS_CAP = 1e9;
const MAX_WINDOW_HOURS = 24;

/**
 * A localizable validation failure. `key` names a key in the `validation`
 * namespace; `values` carries the i18next interpolation values. Callers render
 * it with `t(err.key, err.values)`. Returning a key instead of an English
 * string keeps these client-side mirrors language-agnostic - the rules and
 * thresholds stay here, the wording lives in the catalog.
 */
export type FieldError = { key: string; values?: Record<string, string | number> };

/** Validate an account id: non-empty, <= 64 chars, printable, no
 *  leading/trailing whitespace. Returns a {@link FieldError} or null. */
export function validateAccountID(id: string): FieldError | null {
  if (id.length === 0) {
    return { key: "account.required" };
  }
  if (id.length > MAX_ACCOUNT_LEN) {
    return { key: "account.tooLong", values: { max: MAX_ACCOUNT_LEN } };
  }
  if (id !== id.trim()) {
    return { key: "account.whitespace" };
  }
  // Printable: reject ASCII control characters and DEL.
  for (const ch of id) {
    const code = ch.codePointAt(0) ?? 0;
    if (code < 0x20 || code === 0x7f) {
      return { key: "account.printable" };
    }
  }
  return null;
}

/** Validate an asset symbol: non-empty, no whitespace, <= 32 chars. */
export function validateAsset(asset: string): FieldError | null {
  if (asset.length === 0) {
    return { key: "asset.required" };
  }
  if (asset.length > MAX_ASSET_LEN) {
    return { key: "asset.tooLong", values: { max: MAX_ASSET_LEN } };
  }
  if (/\s/.test(asset)) {
    return { key: "asset.whitespace" };
  }
  return null;
}

/** Loose decimal check: optional sign, digits with an optional fraction. */
function isDecimal(value: string): boolean {
  return /^[+-]?(\d+\.?\d*|\.\d+)$/.test(value.trim());
}

/** True when a validated decimal string has at least one non-zero digit. The
 *  sign and decimal point carry no magnitude, so a value is zero iff every
 *  digit is "0". Works on the string directly to avoid float rounding on
 *  financial values. */
function isZeroDecimal(value: string): boolean {
  return !/[1-9]/.test(value);
}

function isPositiveDecimal(value: string): boolean {
  if (!isDecimal(value)) {
    return false;
  }
  const trimmed = value.trim();
  return !trimmed.startsWith("-") && !isZeroDecimal(trimmed);
}

function isPositiveInteger(value: string): boolean {
  return /^\d+$/.test(value.trim()) && /[1-9]/.test(value.trim());
}

/** Compare two validated decimal strings without parsing to float, so exact
 *  financial values keep full precision. Returns a negative number when
 *  `a < b`, zero when equal, and a positive number when `a > b`. Inputs must
 *  already satisfy {@link isDecimal}. */
function compareDecimal(a: string, b: string): number {
  const sign = (value: string): number => {
    const t = value.trim();
    if (isZeroDecimal(t)) {
      return 0;
    }
    return t.startsWith("-") ? -1 : 1;
  };
  const signA = sign(a);
  const signB = sign(b);
  if (signA !== signB) {
    return signA - signB;
  }
  if (signA === 0) {
    return 0;
  }
  return signA * compareMagnitude(a, b);
}

/** Compare the magnitudes of two validated decimal strings, ignoring sign.
 *  Returns -1, 0, or 1. */
function compareMagnitude(a: string, b: string): number {
  const split = (value: string): [string, string] => {
    const t = value.trim().replace(/^[+-]/, "");
    const dot = t.indexOf(".");
    if (dot < 0) {
      return [t, ""];
    }
    return [t.slice(0, dot), t.slice(dot + 1)];
  };
  const [intA, fracA] = split(a);
  const [intB, fracB] = split(b);
  const trimLeadZeros = (s: string): string => s.replace(/^0+/, "");
  const wholeA = trimLeadZeros(intA);
  const wholeB = trimLeadZeros(intB);
  if (wholeA.length !== wholeB.length) {
    return wholeA.length < wholeB.length ? -1 : 1;
  }
  if (wholeA !== wholeB) {
    return wholeA < wholeB ? -1 : 1;
  }
  const width = Math.max(fracA.length, fracB.length);
  const padA = fracA.padEnd(width, "0");
  const padB = fracB.padEnd(width, "0");
  if (padA === padB) {
    return 0;
  }
  return padA < padB ? -1 : 1;
}

/** Parse a Go duration string ("1s", "500ms", "2h30m") into seconds, or null
 *  when it is not a well-formed, strictly-positive duration. */
export function parseGoDurationSeconds(value: string): number | null {
  const text = value.trim();
  if (text.length === 0 || text === "0") {
    return null;
  }
  const unit: Record<string, number> = {
    ns: 1e-9,
    us: 1e-6,
    "µs": 1e-6,
    ms: 1e-3,
    s: 1,
    m: 60,
    h: 3600,
  };
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
  let total = 0;
  let consumed = 0;
  let match: RegExpExecArray | null;
  while ((match = re.exec(text)) !== null) {
    consumed += match[0].length;
    total += parseFloat(match[1]) * unit[match[2]];
  }
  if (consumed !== text.length || total <= 0) {
    return null;
  }
  return total;
}

/** Validate one kind/value pair for a policy. Returns a {@link FieldError} or
 *  null. */
export function validateKindValue(
  _policy: Policy,
  kind: string,
  value: string,
): FieldError | null {
  switch (kind) {
    case "max_orders": {
      if (!isPositiveInteger(value)) {
        return { key: "kind.maxOrders.integer" };
      }
      if (parseInt(value, 10) > MAX_ORDERS_CAP) {
        return { key: "kind.maxOrders.cap" };
      }
      return null;
    }
    case "window": {
      const secs = parseGoDurationSeconds(value);
      if (secs === null) {
        return { key: "kind.window.duration" };
      }
      if (secs > MAX_WINDOW_HOURS * 3600) {
        return { key: "kind.window.cap" };
      }
      return null;
    }
    case "max_quantity":
    case "max_notional": {
      if (!isPositiveDecimal(value)) {
        return { key: "kind.positiveDecimal", values: { kind } };
      }
      return null;
    }
    case "lower_bound":
    case "upper_bound":
    case "initial_pnl": {
      if (!isDecimal(value)) {
        return { key: "kind.decimal", values: { kind } };
      }
      return null;
    }
    default:
      return { key: "kind.unknown", values: { kind } };
  }
}

/**
 * Validate a full barrier against the cross-layer vocabulary: policy, scope,
 * the account/asset axes, and the per-policy kind set. Returns the first
 * {@link FieldError}, or null when the barrier is well-formed.
 */
export function validateLimit(limit: Limit): FieldError | null {
  if (!isPolicy(limit.policy)) {
    return { key: "limit.unknownPolicy", values: { policy: limit.policy } };
  }
  const policy: Policy = limit.policy;

  if (!isScope(limit.scope)) {
    return { key: "limit.unknownScope", values: { scope: limit.scope } };
  }
  const scope: Scope = limit.scope;

  if (!(ALLOWED_SCOPES[policy] as readonly Scope[]).includes(scope)) {
    return { key: "limit.scopeNotAllowed", values: { scope, policy } };
  }

  if (scopeHasAccount(scope)) {
    const err = validateAccountID(limit.account);
    if (err) {
      return err;
    }
  } else if (limit.account.length > 0) {
    return { key: "limit.scopeNoAccount", values: { scope } };
  }

  if (scopeHasAsset(scope)) {
    const err = validateAsset(limit.asset);
    if (err) {
      return err;
    }
  } else if (limit.asset.length > 0) {
    return { key: "limit.scopeNoAsset", values: { scope } };
  }

  const kinds = Object.keys(limit.values).filter(
    (k) => limit.values[k].trim().length > 0,
  );

  if (policy === "rate_limit") {
    if (!kinds.includes("max_orders") || !kinds.includes("window")) {
      return { key: "limit.rateLimitRequires" };
    }
  } else if (policy === "order_size_limit") {
    if (kinds.length === 0) {
      return { key: "limit.orderSizeRequires" };
    }
  } else if (policy === "pnl_bounds_kill_switch") {
    // initial_pnl alone is not sufficient; at least one bound is always required.
    const hasBound =
      kinds.includes("lower_bound") || kinds.includes("upper_bound");
    if (!hasBound) {
      return { key: "limit.pnlRequires" };
    }
  }

  for (const kind of kinds) {
    const err = validateKindValue(policy, kind, limit.values[kind]);
    if (err) {
      return err;
    }
  }

  // pnl: when both bounds are present, lower <= upper. Compared as exact
  // decimal strings so high-precision bounds keep full precision.
  if (
    policy === "pnl_bounds_kill_switch" &&
    kinds.includes("lower_bound") &&
    kinds.includes("upper_bound")
  ) {
    const lower = limit.values["lower_bound"];
    const upper = limit.values["upper_bound"];
    if (compareDecimal(lower, upper) > 0) {
      return { key: "limit.pnlBoundOrder" };
    }
  }

  return null;
}
