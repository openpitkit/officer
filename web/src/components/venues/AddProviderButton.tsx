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

import { ChevronRight, Plus } from "lucide-react";
import { useTranslation } from "react-i18next";

import { ProviderIcon } from "./ProviderIcon";

/** Caller-supplied action label, available provider types, and click handler. */
export interface AddProviderButtonProps {
  label: string;
  providerTypes: string[];
  onClick: () => void;
}

/** Full-width picker trigger with provider icons and a localized count. */
export function AddProviderButton({
  label,
  providerTypes,
  onClick,
}: AddProviderButtonProps) {
  const { t } = useTranslation("venues");
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex w-full items-center justify-between gap-3 rounded-card border border-border bg-surface px-4 py-3 text-left transition-colors hover:bg-surface-hover"
    >
      <span className="flex items-center gap-2 text-sm font-medium text-text">
        <Plus className="h-4 w-4 shrink-0 text-accent" />
        {label}
        <span className="ml-1 flex items-center gap-1.5">
          {providerTypes.map((type) => (
            <ProviderIcon key={type} type={type} className="h-4 w-4 shrink-0" />
          ))}
        </span>
      </span>
      <span className="flex items-center gap-1.5 text-xs text-muted">
        {t("providers.count", { count: providerTypes.length })}
        <ChevronRight className="h-3.5 w-3.5" />
      </span>
    </button>
  );
}
