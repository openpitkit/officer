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

import { Hash, Search, SlidersHorizontal, X } from "lucide-react";
import type { CSSProperties, KeyboardEventHandler, ReactNode } from "react";

import { ClearInlineButton } from "@/components/ClearableInput";
import { Autocomplete } from "@/components/Autocomplete";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { AUTOCOMPLETE_SUGGESTION_LIMIT } from "@/framework/constants";

export { ClearInlineButton };

export interface FieldLabelProps {
  children?: ReactNode;
  style?: CSSProperties;
}

/** Tracked-uppercase micro-label used above every field. */
export function FieldLabel({ children, style }: FieldLabelProps) {
  return (
    <span
      className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted"
      style={style}
    >
      {children}
    </span>
  );
}

export interface FilterBarProps {
  /** The leading row of controls (online fields, exact-ID, segmented). */
  children?: ReactNode;
  /**
   * Controls pinned to the right edge (status segmented, MoreFiltersButton).
   * Keeping the shared utility controls here anchors them to the right so they
   * stay put across pages regardless of how many leading fields precede them.
   */
  trailing?: ReactNode;
  /** Optional full-width controls rendered below the primary filter row. */
  bottom?: ReactNode;
  /** Optional chip row rendered below (FilterChip elements + a Clear-all). */
  chips?: ReactNode;
  /** Shows that at least one filter is active, including inline fields. */
  active?: boolean;
  /** Label for the active-filter marker. */
  activeLabel?: string;
  /** Clears every active filter represented by this bar. */
  onClearActive?: () => void;
  /** Accessible name + tooltip for the active-filter clear action. */
  clearActiveLabel?: string;
  style?: CSSProperties;
}

/** The bar shell: a wrapping control row + an optional chip row below. */
export function FilterBar({
  children,
  trailing,
  bottom,
  chips,
  active = false,
  activeLabel = "Active filters",
  onClearActive,
  clearActiveLabel = "Clear all filters",
  style,
}: FilterBarProps) {
  return (
    <div className="w-full border-y border-border py-3" style={style}>
      <div className="flex flex-wrap items-end gap-3">
        {children}
        {trailing !== undefined && trailing !== null && (
          <div className="ml-auto flex flex-wrap items-end gap-3">{trailing}</div>
        )}
      </div>
      {bottom !== undefined && bottom !== null && (
        <div className="mt-3 flex flex-wrap items-end gap-3">{bottom}</div>
      )}
      {(active || (chips !== undefined && chips !== null)) && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {active && (
            <span className="inline-flex min-h-[1.5rem] items-center rounded-badge border border-accent/50 bg-accent-dim px-2 text-[0.6875rem] font-bold text-accent">
              {activeLabel}
              {onClearActive !== undefined && (
                <button
                  type="button"
                  className="-mr-1 ml-1 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-badge text-accent transition-colors hover:bg-accent-dim hover:text-accent-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  onClick={onClearActive}
                  title={clearActiveLabel}
                  aria-label={clearActiveLabel}
                >
                  <X className="h-3 w-3" aria-hidden="true" />
                </button>
              )}
            </span>
          )}
          {chips}
        </div>
      )}
    </div>
  );
}

export interface OnlineFilterFieldProps {
  label?: string;
  value?: string;
  onChange?: (value: string) => void;
  /** Clears the field; when set, an inline reset icon shows while non-empty. */
  onClear?: () => void;
  /** Accessible name + tooltip for the inline reset icon. */
  clearLabel?: string;
  placeholder?: string;
  /** Show the live-query dot while a debounced request is in flight. */
  loading?: boolean;
  /** Accessible name for the live-query dot. */
  searchingLabel?: string;
  width?: number;
  icon?: boolean;
  style?: CSSProperties;
}

/** As-you-type filter. Server-side; consumer owns the debounce + fetch. */
export function OnlineFilterField({
  label,
  value = "",
  onChange,
  onClear,
  clearLabel = "Clear",
  placeholder,
  loading = false,
  searchingLabel = "searching",
  width = 200,
  icon = true,
  style,
}: OnlineFilterFieldProps) {
  const clearable = onClear !== undefined && value !== "";
  return (
    <label className="grid gap-1.5" style={style}>
      {label !== undefined && <FieldLabel>{label}</FieldLabel>}
      <span className="relative block" style={{ width }}>
        {icon && (
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted"
            aria-hidden="true"
          />
        )}
        <Input
          value={value}
          placeholder={placeholder}
          spellCheck={false}
          className={cn(
            "h-8 text-xs",
            icon && "pl-8",
            (loading || clearable) && "pr-7",
          )}
          onChange={(event) => onChange?.(event.target.value)}
        />
        {loading ? (
          <span
            aria-label={searchingLabel}
            className="absolute right-2.5 top-1/2 h-1.5 w-1.5 -translate-y-1/2 animate-pulse rounded-full bg-accent"
          />
        ) : (
          clearable && (
            <ClearInlineButton label={clearLabel} onClick={() => onClear?.()} />
          )
        )}
      </span>
    </label>
  );
}

export interface AutocompleteFilterFieldProps
  extends Omit<OnlineFilterFieldProps, "icon" | "onChange"> {
  onChange?: (value: string) => void;
  onSuggestionSelect?: (value: string) => void;
  onKeyDown?: KeyboardEventHandler<HTMLInputElement>;
  /** Tooltip text forwarded to the value input. */
  title?: string;
  /** Accessible name forwarded when no visible label wraps the value input. */
  ariaLabel?: string;
  suggestions?: string[];
  maxSuggestions?: number;
}

/** Online filter backed by the shared autocomplete input. */
export function AutocompleteFilterField({
  label,
  value = "",
  onChange,
  onSuggestionSelect,
  onKeyDown,
  onClear,
  clearLabel = "Clear",
  placeholder,
  title,
  ariaLabel,
  loading = false,
  searchingLabel = "searching",
  width = 200,
  suggestions = [],
  maxSuggestions = AUTOCOMPLETE_SUGGESTION_LIMIT,
  style,
}: AutocompleteFilterFieldProps) {
  return (
    <label className="grid gap-1.5" style={style}>
      {label !== undefined && <FieldLabel>{label}</FieldLabel>}
      <span className="relative block" style={{ width }}>
        <Autocomplete
          value={value}
          placeholder={placeholder}
          title={title}
          aria-label={ariaLabel}
          suggestions={suggestions}
          maxSuggestions={maxSuggestions}
          clearLabel={clearLabel}
          onChange={(next) => onChange?.(next)}
          onSuggestionSelect={onSuggestionSelect}
          onKeyDown={onKeyDown}
          onClear={onClear}
          className={cn(
            "h-8 text-xs",
            loading && "pr-7",
          )}
        />
        {loading && (
          <span
            aria-label={searchingLabel}
            className="absolute right-2.5 top-1/2 h-1.5 w-1.5 -translate-y-1/2 animate-pulse rounded-full bg-accent"
          />
        )}
      </span>
    </label>
  );
}

export interface ExactIdFieldProps {
  label?: string;
  value?: string;
  onChange?: (value: string) => void;
  /** Fired on Enter or the Open button - perform the indexed single-row lookup. */
  onOpen?: (value: string) => void;
  placeholder?: string;
  openLabel?: string;
  width?: number;
  style?: CSSProperties;
}

/** Indexed single-row lookup with an Open action. */
export function ExactIdField({
  label,
  value = "",
  onChange,
  onOpen,
  placeholder,
  openLabel = "Open",
  width = 200,
  style,
}: ExactIdFieldProps) {
  const submit = () => onOpen?.(value);

  return (
    <div className="grid gap-1.5" style={style}>
      {label !== undefined && <FieldLabel>{label}</FieldLabel>}
      <div className="flex" style={{ width }}>
        <span className="relative min-w-0 flex-1">
          <Hash
            className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted"
            aria-hidden="true"
          />
          <Input
            value={value}
            placeholder={placeholder}
            spellCheck={false}
            className="h-8 rounded-r-none pl-8 text-xs"
            onChange={(event) => onChange?.(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                submit();
              }
            }}
          />
        </span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="rounded-l-none border-l-0"
          onClick={submit}
        >
          {openLabel}
        </Button>
      </div>
    </div>
  );
}

export interface MoreFiltersButtonProps {
  /** Active advanced-filter count shown in the badge. */
  count?: number;
  onClick?: () => void;
  label?: string;
  style?: CSSProperties;
}

/** Opens the advanced-filter panel; the badge counts active advanced filters. */
export function MoreFiltersButton({
  count = 0,
  onClick,
  label = "More filters",
  style,
}: MoreFiltersButtonProps) {
  return (
    <Button type="button" variant="outline" size="sm" onClick={onClick} style={style}>
      <SlidersHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
      {label}
      {count > 0 && (
        <span className="ml-0.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-accent px-1 text-[0.625rem] font-bold text-bg">
          {count}
        </span>
      )}
    </Button>
  );
}

export interface FilterChipProps {
  field?: string;
  /** Key text shown muted (e.g. "account contains"). Falls back to `field`. */
  label?: string;
  /** The value text / node. */
  value?: ReactNode;
  /** Accessible name + tooltip for the remove button. */
  removeLabel?: string;
  onRemove?: () => void;
  style?: CSSProperties;
}

/** A removable summary of one applied filter. */
export function FilterChip({
  field,
  label,
  value,
  removeLabel = "Remove filter",
  onRemove,
  style,
}: FilterChipProps) {
  const title = label ?? field;

  return (
    <span
      className="inline-flex min-h-[1.5rem] items-center gap-1.5 rounded-badge border border-border bg-surface-2 py-0.5 pl-2 pr-1 text-[0.6875rem] text-text"
      style={style}
    >
      {title !== undefined && <span className="text-muted">{title}</span>}
      {value}
      {onRemove !== undefined && (
        <button
          type="button"
          className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-badge text-muted transition-colors hover:bg-accent-dim hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          onClick={onRemove}
          title={removeLabel}
          aria-label={removeLabel}
        >
          <X className="h-3 w-3" aria-hidden="true" />
        </button>
      )}
    </span>
  );
}
