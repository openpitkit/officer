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

import { codeLabel, sourceLabel, statusLabel } from "@/i18n/codeLabel";

function RawSource({ entry }) {
  // ruleid: panel-code-label-raw-field
  return <Badge variant="neutral">{entry.source}</Badge>;
}

function RawStatusTitle({ order }) {
  // ruleid: panel-code-label-raw-field
  return <Chip title={order.status} />;
}

function RawBreakdownValue({ fields }) {
  // ruleid: panel-code-label-raw-field
  return <BreakdownRow label="Verdict" value={fields.verdict} />;
}

function LabelledSource({ entry }) {
  // ok: panel-code-label-raw-field
  return <Badge variant="neutral">{sourceLabel(entry.source)}</Badge>;
}

function LabelledBreakdownValue({ fields }) {
  // ok: panel-code-label-raw-field
  return <BreakdownRow label="Status" value={statusLabel(fields.orderStatus)} />;
}

function SelectValue({ form }) {
  // ok: panel-code-label-raw-field
  return <Select value={form.side} onValueChange={setSide} />;
}

function Identifier({ asset }) {
  // ok: panel-code-label-raw-field
  return <span>{asset.code}</span>;
}

function rawOptions() {
  // ruleid: panel-code-label-raw-text
  return SOURCES.map((source) => ({
    value: source,
    label: source,
  }));
}

function labelledOptions() {
  // ok: panel-code-label-raw-text
  return SOURCES.map((source) => ({
    value: source,
    label: sourceLabel(source),
  }));
}

function rawChip(t, sourceFilter) {
  // ruleid: panel-code-label-raw-text
  return { key: "source", label: t("history.sourceLabel"), value: sourceFilter };
}

function labelledChip(t, sourceFilter) {
  // ok: panel-code-label-raw-text
  return { key: "source", label: t("history.sourceLabel"), value: sourceLabel(sourceFilter) };
}

function rawHeading(t, normalizedOrderSource) {
  // ruleid: panel-code-label-raw-text
  return `${t("table.source")}: ${normalizedOrderSource}`;
}

function labelledHeading(t, normalizedOrderSource) {
  // ok: panel-code-label-raw-text
  return `${t("table.source")}: ${sourceLabel(normalizedOrderSource)}`;
}

function rawParenthesised(usage) {
  // ruleid: panel-code-label-raw-text
  return `${usage.instanceLabel} (${usage.providerType}: ${usage.externalSymbol})`;
}

function identifierHeading(t, account) {
  // ok: panel-code-label-raw-text
  return `${t("table.account")}: ${account}`;
}

function silentDefault(t, row) {
  // ruleid: panel-code-label-silent-fallback
  return t(`engine.profileParams.${row.param}`, { defaultValue: row.param });
}

function silentGuard(t, id) {
  // ruleid: panel-code-label-silent-fallback
  return isPolicy(id) ? t(`domain:policyLabel.${id}`) : id;
}

function reportedDefault(row) {
  // ok: panel-code-label-silent-fallback
  return codeLabel(`service:engine.profileParams.${row.param}`, row.param);
}

function textDefault(t, provider) {
  // ok: panel-code-label-silent-fallback
  return t(`providers.${provider.type}.title`, { defaultValue: provider.title });
}

function reportedGuard(t, id) {
  // ok: panel-code-label-silent-fallback
  return isPolicy(id) ? t(`domain:policyLabel.${id}`) : codeLabel(null, id);
}
