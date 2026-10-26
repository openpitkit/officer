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

import i18n from "i18next";

import type { AuditDetail } from "@/api/types";
import {
  codeLabel,
  policyLabel,
  rejectLabel,
  scopeLabel,
  sideLabel,
  statusLabel,
  warnMissingOnce,
} from "@/i18n/codeLabel";

const booleanLabel = (v: string) => codeLabel(`audit:detailValue.${v}`, v);

/** The label of each detail value that is a code, by placeholder or field
 *  name. Every other value - an identifier, a number, operator text, an engine
 *  message - is shown as written. */
const VALUE_LABELS: Record<string, (value: string) => string> = {
  zip: booleanLabel,
  overfill: booleanLabel,
  forced: booleanLabel,
  policy: (v) => policyLabel(i18n.t, v),
  scope: (v) => scopeLabel(i18n.t, v),
  side: sideLabel,
  status: statusLabel,
  rejectCode: rejectLabel,
  adjustmentType: (v) => codeLabel(`domain:adjustmentType.${v}`, v),
  connectionMode: (v) => codeLabel(`trading:mode.${v}`, v),
  restoreMode: (v) => codeLabel(`service:backup.modes.${v}`, v),
  entity: (v) => codeLabel(`common:businessCsv.entities.${v}`, v),
  delimiter: (v) => codeLabel(`common:businessCsv.delimiters.${v}`, v),
};

/** The placeholders that may carry a sentinel for an absent side of a
 *  transition (framework/domain/audit_detail.go), and the catalog entry of
 *  each sentinel. */
const SENTINEL_PARAMS = new Set(["from", "to", "currency"]);
const SENTINEL_KEYS: Record<string, string> = {
  "<none>": "audit:detailValue.none",
  "<unset>": "audit:detailValue.unset",
};

function paramValue(name: string, value: string): string {
  const sentinel = SENTINEL_PARAMS.has(name) ? SENTINEL_KEYS[value] : undefined;
  if (sentinel !== undefined) {
    return codeLabel(sentinel, value);
  }
  const label = VALUE_LABELS[name];
  return label === undefined ? value : label(value);
}

/** An audit detail line in the current language: the catalog template
 *  `audit:detail.<code>` filled with the values the line was written with,
 *  code values shown by their labels, then the ` name=value` tail with each
 *  name labelled by `audit:detailField.<name>` and each value as a template
 *  value of that name.
 *
 *  A detail without a code is its stored text: an action recorded without a
 *  detail. A code with no catalog template, such as one from a server newer
 *  than this panel, shows the stored English text and is reported once per
 *  code with `console.warn`. */
export function auditDetailText(detail: AuditDetail): string {
  if (detail.code === "") {
    return detail.text;
  }
  const key = `audit:detail.${detail.code}`;
  if (!i18n.exists(key)) {
    warnMissingOnce(
      key,
      `No catalog template for audit detail "${detail.code}" (${key}); showing the stored text.`,
    );
    return detail.text;
  }
  const values = Object.fromEntries(
    Object.entries(detail.params).map(([name, value]) => [
      name,
      paramValue(name, value),
    ]),
  );
  // `replace` keeps placeholder names such as `count` or `context` out of
  // the i18next options they would otherwise collide with.
  const line = i18n.t(key, { replace: values });
  const tail = detail.fields
    .map(
      (field) =>
        ` ${codeLabel(`audit:detailField.${field.name}`, field.name)}=${paramValue(field.name, field.value)}`,
    )
    .join("");
  return line + tail;
}
