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

import { forwardRef, type InputHTMLAttributes } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Input } from "@/components/ui/input";
import { stepValue } from "@/lib/numberStep";
import { cn } from "@/lib/utils";

export interface NumberStepperProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    "min" | "onChange" | "value" | "type"
  > {
  value: string;
  onChange: (value: string) => void;
  /** Lower clamp for the +/- controls; defaults to 0. Use null for signed values. */
  min?: string | null;
  /** Classes applied to the inner input; className applies to the wrapper. */
  inputClassName?: string;
}

/**
 * Decimal text input with smart +/- controls. The step size scales with the
 * current value's magnitude (see {@link stepValue}); ArrowUp/ArrowDown nudge it
 * too. Free-form typing is preserved — the controls only nudge.
 */
export const NumberStepper = forwardRef<HTMLInputElement, NumberStepperProps>(
  (
    {
      value,
      onChange,
      min = "0",
      disabled,
      className,
      inputClassName,
      onKeyDown,
      ...props
    },
    ref,
  ) => {
    const { t } = useTranslation();

    const nudge = (direction: 1 | -1) => {
      onChange(stepValue(value, direction, { min }));
    };

    return (
      <div className={cn("relative", className)}>
        <Input
          ref={ref}
          inputMode="decimal"
          {...props}
          value={value}
          disabled={disabled}
          className={cn("pr-7", inputClassName)}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "ArrowUp") {
              e.preventDefault();
              nudge(1);
              return;
            } else if (e.key === "ArrowDown") {
              e.preventDefault();
              nudge(-1);
              return;
            }
            onKeyDown?.(e);
          }}
        />
        <div className="absolute right-0 top-0 flex h-full w-6 flex-col overflow-hidden rounded-r-card border-l border-border">
          <button
            type="button"
            tabIndex={-1}
            aria-label={t("actions.increment")}
            disabled={disabled}
            onClick={() => nudge(1)}
            className="flex flex-1 items-center justify-center text-muted-lt transition-colors hover:bg-surface-hover hover:text-accent disabled:opacity-50"
          >
            <ChevronUp className="h-3 w-3" />
          </button>
          <button
            type="button"
            tabIndex={-1}
            aria-label={t("actions.decrement")}
            disabled={disabled}
            onClick={() => nudge(-1)}
            className="flex flex-1 items-center justify-center border-t border-border text-muted-lt transition-colors hover:bg-surface-hover hover:text-accent disabled:opacity-50"
          >
            <ChevronDown className="h-3 w-3" />
          </button>
        </div>
      </div>
    );
  },
);
NumberStepper.displayName = "NumberStepper";
