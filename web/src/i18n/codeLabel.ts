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
// Please see https://officer.openpit.dev and the OWNERS file for details.

import i18n, { type TFunction } from "i18next";

import { isPolicy, isScope } from "@/framework/registries/vocabulary";

const reported = new Set<string>();

/** Report a catalog entry the panel lacks with `console.warn`, once per `key`
 *  for the panel's lifetime. */
export function warnMissingOnce(key: string, message: string): void {
  if (!reported.has(key)) {
    reported.add(key);
    console.warn(message);
  }
}

/** The display label of a wire code - an order status, a source, a reject
 *  code, an audit action - read from its code-keyed catalog entry `key`
 *  (`"orders:filter.status.rejected"`). The code itself never changes; only
 *  its label is translated.
 *
 *  A code with no catalog entry, such as one from a server newer than this
 *  panel, or a `null` key for a code a mapping does not know, renders as the
 *  code itself and is reported once per key with `console.warn`: never as a
 *  blank, never as the catalog key. */
export function codeLabel(key: string | null, code: string): string {
  if (key !== null && i18n.exists(key)) {
    return i18n.t(key);
  }
  warnMissingOnce(
    key ?? code,
    `No catalog label for code "${code}"${key === null ? "" : ` (${key})`}; showing the code.`,
  );
  return code;
}

/** Order status → display label. */
export function statusLabel(status: string): string {
  return codeLabel(`orders:filter.status.${status}`, status);
}

/** Order side → display label. */
export function sideLabel(side: string): string {
  return codeLabel(`orders:filter.side.${side}`, side);
}

/** Source → display label. */
export function sourceLabel(source: string): string {
  return codeLabel(`common:source.${source}`, source);
}

/** Order event type → display label. */
export function eventTypeLabel(type: string): string {
  return codeLabel(`orders:detail.dialog.timeline.type.${type}`, type);
}

/** Reject code → display label. */
export function rejectLabel(code: string): string {
  return codeLabel(`orders:check.rejectReasons.${code}`, code);
}

/** Reject scope → display label. */
export function rejectScopeLabel(scope: string): string {
  return codeLabel(`orders:detail.dialog.timeline.scopes.${scope}`, scope);
}

/** Registered policy → display label. */
export function policyLabel(t: TFunction, id: string): string {
  return isPolicy(id) ? t(`domain:policyLabel.${id}`) : codeLabel(null, id);
}

/** Registered limit scope → display label. */
export function scopeLabel(t: TFunction, id: string): string {
  return isScope(id) ? t(`domain:scope.${id}`) : codeLabel(null, id);
}

/** Policy settings field → display label. */
export function policyFieldLabel(
  t: TFunction,
  policyId: string,
  fieldKey: string,
): string {
  const key = `domain:policyField.${policyId}.${fieldKey}.label`;
  return i18n.exists(key) ? t(key) : codeLabel(key, fieldKey);
}
