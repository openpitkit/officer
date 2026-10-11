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

import type { AdjustmentType } from "@/api/types";
import { ADJUSTMENT_TYPES, adjustmentTypeLabel } from "@/api/vocabulary";
import { Segmented } from "@/components/data";
import { Label } from "@/components/ui/label";

/** The labelled choice of an adjustment's type. Nothing is preselected: with
 *  `value` empty no option is active, and the form decides what an empty
 *  choice means on submit. */
export function AdjustmentTypeField({
  value,
  onChange,
  disabled,
}: {
  value: AdjustmentType | "";
  onChange: (value: AdjustmentType) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation("positions");

  return (
    <fieldset className="space-y-1.5">
      <Label asChild>
        <legend>{t("dialog.fields.type")}</legend>
      </Label>
      <Segmented
        value={value}
        disabled={disabled}
        options={ADJUSTMENT_TYPES.map((type) => ({
          value: type,
          label: adjustmentTypeLabel(t, type),
        }))}
        onChange={(next) => onChange(next as AdjustmentType)}
      />
    </fieldset>
  );
}
