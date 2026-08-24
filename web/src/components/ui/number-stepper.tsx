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

import {
  forwardRef,
  useEffect,
  useRef,
  useState,
  type ForwardedRef,
  type InputHTMLAttributes,
} from "react";
import { ChevronDown, ChevronUp, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Input } from "@/components/ui/input";
import { isDecimalString, stepValue } from "@/lib/numberStep";
import { cn } from "@/lib/utils";

export interface NumberStepperProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "min" | "onChange" | "value" | "type"
> {
  value: string;
  onChange: (value: string) => void;
  /** Lower clamp for the +/- controls; defaults to 0. Use null for signed values. */
  min?: string | null;
  /** Whether typed values may include an explicit sign. */
  allowSignedInput?: boolean;
  /** Clears the field; when set, an inline reset icon shows while non-empty. */
  onClear?: () => void;
  /** Accessible name + tooltip for the inline reset icon. */
  clearLabel?: string;
  /** Classes applied to the inner input; className applies to the wrapper. */
  inputClassName?: string;
  /** Extra validity message owned by a parent control, such as range ordering. */
  customValidity?: string;
}

function assignRef<T>(ref: ForwardedRef<T>, value: T) {
  if (typeof ref === "function") {
    ref(value);
  } else if (ref !== null) {
    ref.current = value;
  }
}

/**
 * Decimal text input with smart +/- controls. The step size scales with the
 * current value's magnitude (see {@link stepValue}); ArrowUp/ArrowDown nudge it
 * too. Free-form typing is preserved - the controls only nudge.
 */
export const NumberStepper = forwardRef<HTMLInputElement, NumberStepperProps>(
  (
    {
      value,
      onChange,
      min = "0",
      allowSignedInput = true,
      onClear,
      clearLabel,
      customValidity = "",
      disabled,
      className,
      inputClassName,
      onKeyDown,
      ...props
    },
    ref,
  ) => {
    const { t } = useTranslation();
    const inputRef = useRef<HTMLInputElement | null>(null);
    const previousValueRef = useRef(value);
    const [draftValue, setDraftValue] = useState(value);
    const invalidMessage = t("filters.invalidNumber");
    const validityMessage = !isDecimalString(draftValue)
      ? invalidMessage
      : customValidity;
    const invalid = validityMessage !== "";

    useEffect(() => {
      if (value !== previousValueRef.current) {
        previousValueRef.current = value;
        setDraftValue(value);
      }
    }, [value]);

    useEffect(() => {
      inputRef.current?.setCustomValidity(validityMessage);
    }, [validityMessage]);

    const nudge = (direction: 1 | -1) => {
      if (invalid) {
        inputRef.current?.reportValidity();
        return;
      }
      const next = stepValue(draftValue, direction, { min });
      setDraftValue(next);
      onChange(next);
    };
    const clearable = onClear !== undefined && draftValue !== "" && !disabled;

    return (
      <div className={cn("relative", className)}>
        <Input
          ref={(node) => {
            inputRef.current = node;
            assignRef(ref, node);
          }}
          inputMode="decimal"
          {...props}
          value={draftValue}
          disabled={disabled}
          aria-invalid={invalid ? "true" : undefined}
          className={cn(
            "pr-7 invalid:border-danger",
            clearable && "!pr-12",
            inputClassName,
          )}
          onChange={(e) => {
            const next = e.target.value;
            const hasExplicitSign = /^[+-]/.test(next.trim());
            const nextValid =
              isDecimalString(next) && (allowSignedInput || !hasExplicitSign);
            if (!allowSignedInput && hasExplicitSign) {
              e.currentTarget.value = draftValue;
              return;
            }
            setDraftValue(next);
            e.target.setCustomValidity(
              nextValid ? customValidity : invalidMessage,
            );
            onChange(next);
          }}
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
        {clearable && (
          <button
            type="button"
            title={clearLabel ?? t("filters.clearField")}
            aria-label={clearLabel ?? t("filters.clearField")}
            tabIndex={-1}
            className="absolute right-7 top-1/2 inline-flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded-badge text-muted transition-colors hover:bg-accent-dim hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => {
              setDraftValue("");
              inputRef.current?.setCustomValidity("");
              onClear();
            }}
          >
            <X className="h-3 w-3" aria-hidden="true" />
          </button>
        )}
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
