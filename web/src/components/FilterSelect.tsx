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

import type { LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";

/**
 * A selectable filter option. `labelKey` is the i18n key for the option word
 * (the source of truth for the tooltip and the screen-reader name). When
 * `icon` is set the collapsed trigger shows only that glyph, with the word
 * still available as `title` + `aria-label`; the dropdown always shows the
 * icon next to the word.
 */
export type FilterSelectItem<Value extends string> = {
  value: Value;
  labelKey: string;
  icon?: LucideIcon;
};

/**
 * Compact filter dropdown shared across ledger pages. With icon-bearing items
 * the collapsed trigger contracts to the icon (the word stays accessible);
 * with plain items it behaves exactly like a word-only select. Reusable for
 * text-match and range-mode item sets alike.
 */
export function FilterSelect<Value extends string>({
  ariaLabel,
  value,
  items,
  onChange,
  namespace = "accounts",
  className = "w-36",
}: {
  ariaLabel: string;
  value: Value;
  items: FilterSelectItem<Value>[];
  onChange: (value: Value) => void;
  /** i18n namespace the item `labelKey`s resolve against. */
  namespace?: string;
  className?: string;
}) {
  const { t } = useTranslation(namespace);
  const selected = items.find((item) => item.value === value);
  const selectedLabel = selected ? t(selected.labelKey) : undefined;
  const SelectedIcon = selected?.icon;
  return (
    <Select value={value} onValueChange={(next) => onChange(next as Value)}>
      <SelectTrigger
        // With an icon the trigger contracts to a compact glyph + chevron; the
        // word stays as title/aria-label. Word-only mode keeps the given width.
        className={cn("h-8 text-xs", SelectedIcon ? "w-12 shrink-0" : className)}
        aria-label={ariaLabel}
        title={selectedLabel}
      >
        {SelectedIcon ? (
          <span
            className="flex min-w-0 items-center"
            aria-label={selectedLabel}
          >
            <SelectedIcon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          </span>
        ) : (
          <span className="min-w-0 truncate">{selectedLabel}</span>
        )}
      </SelectTrigger>
      <SelectContent>
        {items.map((item) => {
          const Icon = item.icon;
          return (
            <SelectItem key={item.value} value={item.value}>
              <span className="flex items-center gap-2">
                {Icon && (
                  <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                )}
                {t(item.labelKey)}
              </span>
            </SelectItem>
          );
        })}
      </SelectContent>
    </Select>
  );
}

/**
 * Text-match filter dropdown (contains / starts with / ends with / exact).
 * A thin preset over {@link FilterSelect} that fixes the item set; callers pass
 * the match items (optionally icon-bearing) so each page controls its glyphs.
 */
export function MatchSelect<Value extends string>({
  ariaLabel,
  value,
  items,
  onChange,
  namespace = "accounts",
}: {
  ariaLabel: string;
  value: Value;
  items: FilterSelectItem<Value>[];
  onChange: (value: Value) => void;
  namespace?: string;
}) {
  return (
    <FilterSelect
      ariaLabel={ariaLabel}
      value={value}
      items={items}
      onChange={onChange}
      namespace={namespace}
      className="w-32"
    />
  );
}
