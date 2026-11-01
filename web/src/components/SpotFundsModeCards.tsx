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

import { NotebookPen, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { SpotFundsLimitMode } from "@/api/types";
import { ChoiceCards } from "@/components/ChoiceCards";

/**
 * The choice between the two Spot Funds limit modes, as the policy dialog and
 * the first-run setup offer it: checking funds, or accounting without
 * blocking.
 */
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
      options={[
        {
          value: "enforce",
          title: t("spotFundsMode.modes.enforce.title"),
          description: t("spotFundsMode.modes.enforce.description"),
          icon: ShieldCheck,
        },
        {
          value: "track_only",
          title: t("spotFundsMode.modes.track_only.title"),
          description: t("spotFundsMode.modes.track_only.description"),
          icon: NotebookPen,
        },
      ]}
      value={value}
      onChange={onChange}
      disabled={disabled}
    />
  );
}
