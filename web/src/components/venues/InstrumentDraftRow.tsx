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

import { Plus, SearchCheck } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Autocomplete } from "@/components/Autocomplete";
import { Button } from "@/components/ui/button";
import { ClearableInput } from "@/components/ClearableInput";
import { CodeSuggestionFailure } from "@/components/CodeSuggestionFailure";

/** Instrument identity fields edited by the add row. */
export interface InstrumentDraftFields {
  externalSymbol: string;
  baseAsset: string;
  quoteAsset: string;
}

/** The caller owns validation, verification, and optional enabled state. */
export interface InstrumentDraftRowProps {
  draft: InstrumentDraftFields;
  onDraftChange: (patch: Partial<InstrumentDraftFields>) => void;
  busy: boolean;
  valid: boolean;
  onSubmit: () => void;
  verify?: { verifying: boolean; onVerify: () => void };
  enabled?: { value: boolean; onToggle: () => void };
  quoteAdornment?: ReactNode;
  extraField?: ReactNode;
  baseSuggestions?: string[];
  quoteSuggestions?: string[];
  /** Failed asset-code lookup, shown beside the corresponding field. */
  baseSuggestionsFailed?: boolean;
  /** Failed asset-code lookup, shown beside the corresponding field. */
  quoteSuggestionsFailed?: boolean;
}

/** Adds an instrument using standard text fields or asset autocomplete. */
export function InstrumentDraftRow({
  draft,
  onDraftChange,
  busy,
  valid,
  onSubmit,
  verify,
  enabled,
  quoteAdornment,
  extraField,
  baseSuggestions,
  quoteSuggestions,
  baseSuggestionsFailed,
  quoteSuggestionsFailed,
}: InstrumentDraftRowProps) {
  const { t } = useTranslation("venues");
  const columns = enabled
    ? extraField
      ? "grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_0.8fr_auto_auto]"
      : "grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_auto_auto]"
    : extraField
      ? "grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_0.8fr_auto]"
      : "grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_auto]";
  return (
    <div className={columns}>
      <div className="flex gap-2">
        <div className="min-w-0 flex-1">
          <ClearableInput
            value={draft.externalSymbol}
            onClear={() => onDraftChange({ externalSymbol: "" })}
            clearLabel={t("common:filters.clearField")}
            onChange={(e) => onDraftChange({ externalSymbol: e.target.value })}
            placeholder={t("instrument.externalSymbol")}
            disabled={busy}
          />
        </div>
        {verify && (
          <Button
            type="button"
            variant="outline"
            size="icon"
            aria-label={t("verify.action")}
            disabled={
              busy || draft.externalSymbol.trim() === "" || verify.verifying
            }
            onClick={verify.onVerify}
          >
            <SearchCheck />
          </Button>
        )}
      </div>
      <div className="space-y-1">
        {baseSuggestions ? (
          <Autocomplete
            value={draft.baseAsset}
            onChange={(baseAsset) => onDraftChange({ baseAsset })}
            suggestions={baseSuggestions}
            onClear={() => onDraftChange({ baseAsset: "" })}
            clearLabel={t("common:filters.clearField")}
            placeholder={t("instrument.baseAsset")}
            disabled={busy}
          />
        ) : (
          <ClearableInput
            value={draft.baseAsset}
            onClear={() => onDraftChange({ baseAsset: "" })}
            clearLabel={t("common:filters.clearField")}
            onChange={(e) => onDraftChange({ baseAsset: e.target.value })}
            placeholder={t("instrument.baseAsset")}
            disabled={busy}
          />
        )}
        <CodeSuggestionFailure
          kind="asset"
          failed={baseSuggestionsFailed === true}
        />
      </div>
      <div className="space-y-1">
        <div className="flex items-center gap-2">
          <div className="min-w-0 flex-1">
            {quoteSuggestions ? (
              <Autocomplete
                value={draft.quoteAsset}
                onChange={(quoteAsset) => onDraftChange({ quoteAsset })}
                suggestions={quoteSuggestions}
                onClear={() => onDraftChange({ quoteAsset: "" })}
                clearLabel={t("common:filters.clearField")}
                placeholder={t("instrument.quoteAsset")}
                disabled={busy}
              />
            ) : (
              <ClearableInput
                value={draft.quoteAsset}
                onClear={() => onDraftChange({ quoteAsset: "" })}
                clearLabel={t("common:filters.clearField")}
                onChange={(e) => onDraftChange({ quoteAsset: e.target.value })}
                placeholder={t("instrument.quoteAsset")}
                disabled={busy}
              />
            )}
          </div>
          {quoteAdornment}
        </div>
        <CodeSuggestionFailure
          kind="asset"
          failed={quoteSuggestionsFailed === true}
        />
      </div>
      {extraField}
      {enabled && (
        <Button
          type="button"
          variant={enabled.value ? "default" : "outline"}
          onClick={enabled.onToggle}
          disabled={busy}
        >
          {enabled.value ? t("state.enabled") : t("state.disabled")}
        </Button>
      )}
      <Button type="button" onClick={onSubmit} disabled={busy || !valid}>
        <Plus />
        {t("actions.addInstrument")}
      </Button>
    </div>
  );
}
