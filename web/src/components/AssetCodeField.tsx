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

import { useTranslation } from "react-i18next";
import { Autocomplete } from "@/components/Autocomplete";
import { CodeSuggestionFailure } from "@/components/CodeSuggestionFailure";
import { Label } from "@/components/ui/label";
import { useAssetCodeSuggestions } from "@/lib/useAssetCodeSuggestions";

/**
 * A labelled asset-code picker that also accepts a freely typed mask.
 * Uses the framework's ApiClientProvider for asset suggestions.
 */
export function AssetCodeField({
  id,
  label,
  value,
  onChange,
  disabled,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  const { codes, failed } = useAssetCodeSuggestions(value, !disabled);
  return (
    <div className="flex flex-col gap-y-2">
      <Label htmlFor={id}>{label}</Label>
      <Autocomplete
        id={id}
        value={value}
        onChange={onChange}
        disabled={disabled}
        suggestions={codes}
        onClear={() => onChange("")}
        clearLabel={t("filters.clearField")}
      />
      <CodeSuggestionFailure kind="asset" failed={failed} />
    </div>
  );
}
