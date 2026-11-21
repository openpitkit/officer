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

import {
  ClipboardCheck,
  LockKeyhole,
  NotebookPen,
  ShieldCheck,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import type { SpotFundsLimitMode } from "@/api/types";
import { ChoiceCards } from "@/components/ChoiceCards";
import { SPOT_FUNDS_LIMIT_MODES } from "@/framework/registries/vocabulary";

const MODE_ICONS = {
  enforce: ShieldCheck,
  review_with_hold: LockKeyhole,
  review_without_hold: ClipboardCheck,
  track_only: NotebookPen,
};

/** The Spot Funds mode choice shared by the policy dialog and first-run setup. */
export function SpotFundsModeCards({
  label,
  value,
  onChange,
  disabled = false,
}: {
  /** The accessible name of the group. */
  label: string;
  /** The selected mode, or null while none is chosen yet. */
  value: SpotFundsLimitMode | null;
  onChange: (mode: SpotFundsLimitMode) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation("policies");
  return (
    <ChoiceCards
      label={label}
      options={SPOT_FUNDS_LIMIT_MODES.map((mode) => ({
        value: mode,
        title: t(`spotFundsMode.modes.${mode}.title`),
        description: t(`spotFundsMode.modes.${mode}.description`),
        icon: MODE_ICONS[mode],
      }))}
      value={value}
      onChange={onChange}
      disabled={disabled}
    />
  );
}
