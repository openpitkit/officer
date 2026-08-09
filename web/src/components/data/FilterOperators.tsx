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

import { useEffect, useRef } from "react";
import type { CSSProperties } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NumberStepper } from "@/components/ui/number-stepper";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { compareDecimalStrings } from "@/lib/numberStep";
import { ClearInlineButton } from "./FilterBar";
import {
  OPERATORS,
  type FilterValueType,
  type OperatorOption,
} from "./filterOperatorOptions";

export interface FilterOperatorSelectProps {
  /** Picks the operator set. */
  type?: FilterValueType;
  /** Localized operator options; falls back to the English {@link OPERATORS}. */
  options?: OperatorOption[];
  /** Accessible name for the operator menu. */
  ariaLabel?: string;
  value?: string;
  onChange?: (operator: string) => void;
  width?: number;
  style?: CSSProperties;
}

/** The shared operator menu, reused for every field. */
export function FilterOperatorSelect({
  type = "text",
  options,
  ariaLabel = "Filter operator",
  value,
  onChange,
  width = 130,
  style,
}: FilterOperatorSelectProps) {
  const operators = options ?? OPERATORS[type] ?? OPERATORS.text;

  return (
    <Select
      value={value ?? operators[0]?.value}
      onValueChange={(next) => onChange?.(next)}
    >
      <SelectTrigger
        className="h-8 text-xs"
        style={{ width, ...style }}
        aria-label={ariaLabel}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {operators.map((operator) => (
          <SelectItem key={operator.value} value={operator.value}>
            <span className="flex items-center gap-2">
              {operator.sign !== undefined && (
                <span className="w-4 text-center font-bold">
                  {operator.sign}
                </span>
              )}
              {operator.label}
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export interface SegmentedOption {
  value: string;
  label: string;
}

export interface SegmentedProps {
  options: SegmentedOption[];
  value?: string;
  onChange?: (value: string) => void;
  style?: CSSProperties;
}

/** Inline segmented control for short facets. */
export function Segmented({ options, value, onChange, style }: SegmentedProps) {
  return (
    <div
      className="inline-flex h-8 overflow-hidden rounded-card border border-border bg-bg"
      style={style}
    >
      {options.map((option, index) => {
        const active = option.value === value;
        return (
          <Button
            key={option.value}
            type="button"
            variant="ghost"
            size="sm"
            className={cn(
              "h-full rounded-none border-r border-border px-3 text-xs last:border-r-0",
              index === 0 && "rounded-l-card",
              index === options.length - 1 && "rounded-r-card",
              active
                ? "bg-accent-dim text-accent hover:bg-accent-dim hover:text-accent"
                : "text-muted-lt",
            )}
            onClick={() => onChange?.(option.value)}
            aria-pressed={active}
          >
            {option.label}
          </Button>
        );
      })}
    </div>
  );
}

export interface TextFilterProps {
  operator?: string;
  /** Localized operator options forwarded to the inner select. */
  operators?: OperatorOption[];
  value?: string;
  onOperatorChange?: (op: string) => void;
  onValueChange?: (value: string) => void;
  /** Clears the value; when set, an inline reset icon shows while non-empty. */
  onClear?: () => void;
  /** Accessible name + tooltip for the inline reset icon. */
  clearLabel?: string;
  placeholder?: string;
  /** Tooltip text forwarded to the value input (e.g. mask help). */
  title?: string;
  operatorAriaLabel?: string;
  inputWidth?: number;
  /** Stretch to fill the row: the value input grows, operator stays fixed. */
  fluid?: boolean;
  style?: CSSProperties;
}

/** Operator + value for a text column. */
export function TextFilter({
  operator = "contains",
  operators,
  value = "",
  onOperatorChange,
  onValueChange,
  onClear,
  clearLabel = "Clear",
  placeholder,
  title,
  operatorAriaLabel,
  inputWidth = 200,
  fluid = false,
  style,
}: TextFilterProps) {
  const clearable = onClear !== undefined && value !== "";
  const fallbackOperatorLabel =
    title !== undefined
      ? `${title} operator`
      : placeholder !== undefined
        ? `${placeholder} operator`
        : undefined;
  return (
    <div
      className={cn("flex items-center gap-2", fluid && "w-full")}
      style={style}
    >
      <span
        className={cn("relative block", fluid ? "min-w-0 flex-1" : undefined)}
        style={fluid ? undefined : { width: inputWidth }}
      >
        <Input
          value={value}
          placeholder={placeholder}
          title={title}
          spellCheck={false}
          className={cn("h-8 text-xs", clearable && "pr-7")}
          onChange={(event) => onValueChange?.(event.target.value)}
        />
        {clearable && (
          <ClearInlineButton label={clearLabel} onClick={() => onClear?.()} />
        )}
      </span>
      <FilterOperatorSelect
        type="text"
        options={operators}
        ariaLabel={operatorAriaLabel ?? fallbackOperatorLabel}
        value={operator}
        onChange={onOperatorChange}
        width={140}
      />
    </div>
  );
}

export interface NumberRangeFilterProps {
  operator?: string;
  /** Localized operator options forwarded to the inner select. */
  operators?: OperatorOption[];
  operatorAriaLabel?: string;
  min?: string | number;
  max?: string | number;
  onOperatorChange?: (op: string) => void;
  onMinChange?: (v: string) => void;
  onMaxChange?: (v: string) => void;
  /** Accessible name + tooltip for each inline reset icon. */
  clearLabel?: string;
  /** Stretch to fill the row: operator stays fixed, value inputs grow. */
  fluid?: boolean;
  style?: CSSProperties;
}

/** Operator + min/max for a numeric column. */
export function NumberRangeFilter({
  operator = "between",
  operators,
  operatorAriaLabel,
  min = "",
  max = "",
  onOperatorChange,
  onMinChange,
  onMaxChange,
  clearLabel = "Clear",
  fluid = false,
  style,
}: NumberRangeFilterProps) {
  const { t } = useTranslation();
  const single = operator !== "between";
  const inputClass = "h-8 text-xs";
  const wrapperClass = cn(fluid && "min-w-0 flex-1");
  const minValue = String(min);
  const maxValue = String(max);
  const rangeInvalid =
    !single &&
    minValue.trim() !== "" &&
    maxValue.trim() !== "" &&
    compareDecimalStrings(minValue, maxValue) === 1;
  const rangeValidity = rangeInvalid ? t("filters.invalidRange") : "";

  return (
    <div
      className={cn("flex items-center gap-2", fluid && "w-full")}
      style={style}
    >
      <FilterOperatorSelect
        type="number"
        options={operators}
        ariaLabel={operatorAriaLabel}
        value={operator}
        onChange={onOperatorChange}
        width={122}
      />
      <NumberStepper
        value={minValue}
        placeholder={single ? "Value" : "Min"}
        min={null}
        className={wrapperClass}
        inputClassName={inputClass}
        style={fluid ? undefined : { width: single ? 130 : 90 }}
        onChange={(value) => onMinChange?.(value)}
        onClear={onMinChange === undefined ? undefined : () => onMinChange("")}
        clearLabel={clearLabel}
        customValidity={rangeValidity}
      />
      {!single && (
        <NumberStepper
          value={maxValue}
          placeholder="Max"
          min={null}
          className={wrapperClass}
          inputClassName={inputClass}
          style={fluid ? undefined : { width: 90 }}
          onChange={(value) => onMaxChange?.(value)}
          onClear={
            onMaxChange === undefined ? undefined : () => onMaxChange("")
          }
          clearLabel={clearLabel}
          customValidity={rangeValidity}
        />
      )}
    </div>
  );
}

const TIME_PRESETS: SegmentedOption[] = [
  { value: "today", label: "Today" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "custom", label: "Custom" },
];

export interface TimeRangeFilterProps {
  operator?: string;
  /** Localized operator options forwarded to the inner select. */
  operators?: OperatorOption[];
  operatorAriaLabel?: string;
  /** Localized quick-preset options; falls back to the English presets. */
  presets?: SegmentedOption[];
  preset?: string;
  from?: string;
  to?: string;
  onOperatorChange?: (op: string) => void;
  onPresetChange?: (preset: string) => void;
  onFromChange?: (v: string) => void;
  onToChange?: (v: string) => void;
  clearLabel?: string;
  showPresets?: boolean;
  fluid?: boolean;
  style?: CSSProperties;
}

export interface DateTimeFieldProps {
  value?: string;
  onChange?: (value: string) => void;
  onClear?: () => void;
  clearLabel?: string;
  customValidity?: string;
  className?: string;
  style?: CSSProperties;
}

/** Shared datetime-local field with inline reset. */
export function DateTimeField({
  value = "",
  onChange,
  onClear,
  clearLabel = "Clear",
  customValidity = "",
  className,
  style,
}: DateTimeFieldProps) {
  const inputRef = useRef<HTMLInputElement | null>(null);
  const clearable = onClear !== undefined && value !== "";
  useEffect(() => {
    inputRef.current?.setCustomValidity(customValidity);
  }, [customValidity]);
  return (
    <span className={cn("relative block min-w-0", className)} style={style}>
      <Input
        ref={inputRef}
        type="datetime-local"
        value={value}
        aria-invalid={customValidity === "" ? undefined : "true"}
        className={cn("h-8 text-xs", clearable && "pr-7")}
        onChange={(event) => {
          event.target.setCustomValidity(customValidity);
          onChange?.(event.target.value);
        }}
      />
      {clearable && (
        <ClearInlineButton label={clearLabel} onClick={() => onClear?.()} />
      )}
    </span>
  );
}

/** Operator + datetime input(s) + quick presets. */
export function TimeRangeFilter({
  operator = "between",
  operators,
  operatorAriaLabel,
  presets,
  preset = "custom",
  from = "",
  to = "",
  onOperatorChange,
  onPresetChange,
  onFromChange,
  onToChange,
  clearLabel = "Clear",
  showPresets = true,
  fluid = false,
  style,
}: TimeRangeFilterProps) {
  const { t } = useTranslation();
  const single = operator !== "between";
  const fromTime = from === "" ? Number.NaN : Date.parse(from);
  const toTime = to === "" ? Number.NaN : Date.parse(to);
  const rangeInvalid =
    !single &&
    Number.isFinite(fromTime) &&
    Number.isFinite(toTime) &&
    fromTime > toTime;
  const rangeValidity = rangeInvalid ? t("filters.invalidRange") : "";

  return (
    <div className="grid gap-2" style={style}>
      <div className={cn("flex items-center gap-2", fluid && "w-full")}>
        <FilterOperatorSelect
          type="time"
          options={operators}
          ariaLabel={operatorAriaLabel}
          value={operator}
          onChange={onOperatorChange}
          width={fluid ? undefined : 122}
        />
        <DateTimeField
          value={from}
          className={fluid ? "flex-1" : undefined}
          style={fluid ? undefined : { width: 172 }}
          onChange={onFromChange}
          onClear={
            onFromChange === undefined ? undefined : () => onFromChange("")
          }
          clearLabel={clearLabel}
          customValidity={rangeValidity}
        />
        {!single && (
          <DateTimeField
            value={to}
            className={fluid ? "flex-1" : undefined}
            style={fluid ? undefined : { width: 172 }}
            onChange={onToChange}
            onClear={
              onToChange === undefined ? undefined : () => onToChange("")
            }
            clearLabel={clearLabel}
            customValidity={rangeValidity}
          />
        )}
      </div>
      {showPresets && (
        <Segmented
          options={presets ?? TIME_PRESETS}
          value={preset}
          onChange={onPresetChange}
        />
      )}
    </div>
  );
}
