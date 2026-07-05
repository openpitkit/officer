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

// Consumer-side localized labels for the edition-agnostic data-control
// widgets under components/data. The widgets keep English defaults; pages
// build these arrays from the shared `common` catalog and inject them, so
// the widgets never import i18n themselves.

import type { TFunction } from "i18next";

import type {
  FilterValueType,
  OperatorOption,
  SegmentedOption,
} from "@/components/data";

// Operator value sets per type, paired with their `common:operators.*` key
// suffix and the optional glyph the canonical operator set carried.
const OPERATOR_KEYS: Record<
  FilterValueType,
  ReadonlyArray<{ value: string; sign?: string }>
> = {
  text: [
    { value: "contains" },
    { value: "starts_with" },
    { value: "ends_with" },
    { value: "exact" },
  ],
  number: [
    { value: "eq", sign: "=" },
    { value: "neq", sign: "≠" },
    { value: "gt", sign: ">" },
    { value: "lt", sign: "<" },
    { value: "gte", sign: "≥" },
    { value: "lte", sign: "≤" },
    { value: "between" },
  ],
  time: [{ value: "after" }, { value: "before" }, { value: "between" }],
};

// Time quick-preset values paired with their `common:operators.time.*` key.
const TIME_PRESET_KEYS: ReadonlyArray<{ value: string; key: string }> = [
  { value: "today", key: "today" },
  { value: "24h", key: "last24h" },
  { value: "7d", key: "last7d" },
  { value: "custom", key: "custom" },
];

/**
 * Localized operator options for a filter type.
 *
 * Built from `common:operators.<type>.<value>`; preserves the glyph `sign`
 * the canonical English operator set carried for numeric comparators.
 */
export function operatorOptions(
  t: TFunction,
  type: FilterValueType,
): OperatorOption[] {
  return OPERATOR_KEYS[type].map(({ value, sign }) => ({
    value,
    label: t(`common:operators.${type}.${value}`),
    ...(sign !== undefined && { sign }),
  }));
}

/**
 * Localized time quick-preset options for the time-range filter.
 *
 * Values match the presets the widget emits (`today`/`24h`/`7d`/`custom`);
 * labels come from `common:operators.time.*`.
 */
export function timePresetOptions(t: TFunction): SegmentedOption[] {
  return TIME_PRESET_KEYS.map(({ value, key }) => ({
    value,
    label: t(`common:operators.time.${key}`),
  }));
}
