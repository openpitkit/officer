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

import type { LucideIcon } from "lucide-react";
import { useId } from "react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

/** One option of `ChoiceCards`. */
export interface ChoiceCard<Choice extends string> {
  value: Choice;
  title: string;
  /** What choosing it means, under the title. */
  description: string;
  icon: LucideIcon;
  /** A mark under the description, such as "Recommended". */
  badge?: string;
}

/**
 * A radio group of cards, for a choice whose options carry consequences the
 * operator compares side by side; `Segmented` stays for short peer options.
 * Each card is a native radio input, so the arrow keys move the selection as
 * in any radio group.
 */
export function ChoiceCards<Choice extends string>({
  label,
  options,
  value,
  onChange,
  disabled = false,
}: {
  /** The accessible name of the group. */
  label: string;
  options: readonly ChoiceCard<Choice>[];
  /** The selected option. */
  value: Choice;
  onChange: (value: Choice) => void;
  disabled?: boolean;
}) {
  const name = useId();
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="grid gap-3 sm:grid-cols-2"
    >
      {options.map((option) => {
        const checked = option.value === value;
        const titleId = `${name}-${option.value}-title`;
        const descriptionId = `${name}-${option.value}-description`;
        const badgeId = `${name}-${option.value}-badge`;
        const Icon = option.icon;
        return (
          <label
            key={option.value}
            className={cn(
              "flex flex-col gap-1.5 rounded-card border px-4 py-3 transition-[background-color,border-color] duration-[180ms]",
              "has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring has-[:focus-visible]:ring-offset-2 has-[:focus-visible]:ring-offset-bg",
              checked
                ? "border-accent bg-accent-dim shadow-[inset_0_0_0_1px_var(--accent)]"
                : "border-border bg-surface",
              disabled
                ? "cursor-not-allowed opacity-50"
                : "cursor-pointer hover:border-border-hover",
              !checked && !disabled && "hover:bg-card-hover-bg",
            )}
          >
            <input
              type="radio"
              name={name}
              value={option.value}
              checked={checked}
              disabled={disabled}
              onChange={() => onChange(option.value)}
              aria-labelledby={titleId}
              aria-describedby={
                option.badge === undefined
                  ? descriptionId
                  : `${descriptionId} ${badgeId}`
              }
              className="sr-only"
            />
            <span className="flex items-center gap-2.5">
              <span
                aria-hidden="true"
                className={cn(
                  "grid h-[15px] w-[15px] shrink-0 place-items-center rounded-full border-[1.5px]",
                  checked ? "border-accent" : "border-muted",
                )}
              >
                {checked && (
                  <span className="h-[7px] w-[7px] rounded-full bg-accent" />
                )}
              </span>
              <span id={titleId} className="text-sm font-bold text-text">
                {option.title}
              </span>
              <Icon
                aria-hidden="true"
                className={cn(
                  "ml-auto h-[18px] w-[18px] shrink-0",
                  checked ? "text-accent" : "text-muted-lt",
                )}
              />
            </span>
            <span className="flex flex-col gap-1.5 pl-[25px]">
              <span id={descriptionId} className="text-xs text-muted-lt">
                {option.description}
              </span>
              {option.badge !== undefined && (
                <Badge id={badgeId} variant="ok" className="w-fit">
                  {option.badge}
                </Badge>
              )}
            </span>
          </label>
        );
      })}
    </div>
  );
}
