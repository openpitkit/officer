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

import type { TFunction } from "i18next";

export type Policy = string;
export type Scope = string;

export interface KindSpec {
  kind: string;
}

export interface PolicyField {
  key: string;
}

export interface PolicyCatalogEntry {
  id: string;
  wikiUrl: string;
  fields: PolicyField[];
}

export interface PolicyRegistration {
  id: string;
  allowedScopes: string[];
  kinds: KindSpec[];
  catalog: {
    wikiUrl: string;
    fields: PolicyField[];
  };
}

const scopes = new Map<string, number>();
const policies = new Map<
  string,
  { policy: PolicyRegistration; sequence: number }
>();
let nextScopeSequence = 0;
let nextPolicySequence = 0;

export function registerScope(id: string): void {
  const existing = scopes.get(id);
  scopes.set(id, existing ?? nextScopeSequence++);
}

export function unregisterScope(id: string): void {
  scopes.delete(id);
}

export function registerPolicy(policy: PolicyRegistration): void {
  const existing = policies.get(policy.id);
  policies.set(policy.id, {
    policy,
    sequence: existing?.sequence ?? nextPolicySequence++,
  });
}

export function unregisterPolicy(id: string): void {
  policies.delete(id);
}

export function getScopes(): string[] {
  return Array.from(scopes.entries())
    .sort((left, right) => left[1] - right[1])
    .map(([id]) => id);
}

export function getPolicies(): string[] {
  return Array.from(policies.values())
    .sort((left, right) => left.sequence - right.sequence)
    .map(({ policy }) => policy.id);
}

export function getAllowedScopes(policyId: string): string[] {
  return [...(policies.get(policyId)?.policy.allowedScopes ?? [])];
}

export function getPolicyKinds(policyId: string): KindSpec[] {
  return [...(policies.get(policyId)?.policy.kinds ?? [])];
}

export function getPolicyCatalogEntry(
  id: string,
): PolicyCatalogEntry | undefined {
  const entry = policies.get(id)?.policy.catalog;
  if (!entry) {
    return undefined;
  }
  return {
    id,
    wikiUrl: entry.wikiUrl,
    fields: [...entry.fields],
  };
}

export function isPolicy(value: string): value is Policy {
  return policies.has(value);
}

export function isScope(value: string): value is Scope {
  return scopes.has(value);
}

export function scopeHasAccount(scope: string): boolean {
  return scope === "account" || scope === "account_asset";
}

export function scopeHasAsset(scope: string): boolean {
  return scope === "asset" || scope === "account_asset";
}

export function policyLabel(t: TFunction, id: string): string {
  return isPolicy(id) ? t(`domain:policyLabel.${id}`) : id;
}

export function scopeLabel(t: TFunction, id: string): string {
  return isScope(id) ? t(`domain:scope.${id}`) : id;
}

export function kindHint(t: TFunction, policy: string, kind: string): string {
  return t(`domain:kindHint.${policy}.${kind}`);
}

export function policyCatalogLabel(t: TFunction, id: string): string {
  return t(`domain:policy.${id}.label`);
}

export function policyCatalogDescription(t: TFunction, id: string): string {
  return t(`domain:policy.${id}.description`);
}

export function policyFieldLabel(
  t: TFunction,
  policyId: string,
  fieldKey: string,
): string {
  return t(`domain:policyField.${policyId}.${fieldKey}.label`, {
    defaultValue: fieldKey,
  });
}

export function policyFieldHint(
  t: TFunction,
  policyId: string,
  fieldKey: string,
): string {
  return t(`domain:policyField.${policyId}.${fieldKey}.hint`, {
    defaultValue: "",
  });
}

export function resetVocabulary(): void {
  scopes.clear();
  policies.clear();
  nextScopeSequence = 0;
  nextPolicySequence = 0;
}
