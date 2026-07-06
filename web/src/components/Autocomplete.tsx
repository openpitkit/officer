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

// Combobox/autocomplete input. Suggests from a provided list matching the typed
// prefix but allows any free-typed value (not restricted to the list). Built on
// the existing ui/Input styling; keyboard-navigable with Enter/ArrowUp/ArrowDown
// and closeable with Escape.

import {
  forwardRef,
  useEffect,
  useId,
  useRef,
  useState,
  type InputHTMLAttributes,
  type KeyboardEvent,
} from "react";

import { ClearInlineButton } from "@/components/ClearableInput";
import { cn } from "@/lib/utils";

export interface AutocompleteProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "onChange"> {
  /** The current typed value (controlled). */
  value: string;
  /** Called on every keystroke and when a suggestion is selected. */
  onChange: (value: string) => void;
  /** Called only when a suggestion is explicitly selected. */
  onSuggestionSelect?: (value: string) => void;
  /** Candidate list to filter suggestions from. May be empty. */
  suggestions: string[];
  /** Maximum suggestions to show at once (default 8). */
  maxSuggestions?: number;
  /** Clears the field; when set, the shared inline reset glyph shows while non-empty. */
  onClear?: () => void;
  /** Accessible name + tooltip for the inline reset glyph. */
  clearLabel?: string;
}

/**
 * Prefix-match filter - case-insensitive, empty query shows nothing. A
 * case-insensitive exact match is promoted to the top of the list, and the
 * remaining prefix matches are de-duplicated case-insensitively so a list with
 * case-variants (e.g. "usd" and "USD") never shows a duplicate-looking entry.
 */
function filterSuggestions(
  query: string,
  suggestions: string[],
  max: number,
): string[] {
  const q = query.trim().toLowerCase();
  if (q.length === 0) {
    return [];
  }
  const exact = suggestions.find((s) => s.toLowerCase() === q);
  const out: string[] = exact === undefined ? [] : [exact];
  const seen = new Set(out.map((s) => s.toLowerCase()));
  if (out.length >= max) {
    return out;
  }
  for (const s of suggestions) {
    const lower = s.toLowerCase();
    if (lower.startsWith(q) && !seen.has(lower)) {
      out.push(s);
      seen.add(lower);
      if (out.length >= max) {
        break;
      }
    }
  }
  return out;
}

const Autocomplete = forwardRef<HTMLInputElement, AutocompleteProps>(
  (
    {
      value,
      onChange,
      onSuggestionSelect,
      suggestions,
      maxSuggestions = 8,
      className,
      onBlur,
      onFocus,
      onKeyDown,
      onClear,
      clearLabel = "Clear",
      ...props
    },
    ref,
  ) => {
    const clearable =
      onClear !== undefined && value !== "" && !props.disabled;
    const listId = useId();
    const [open, setOpen] = useState(false);
    const [activeIndex, setActiveIndex] = useState(-1);
    const listRef = useRef<HTMLUListElement>(null);

    const filtered = filterSuggestions(value, suggestions, maxSuggestions);
    const visible = open && filtered.length > 0;

    // Reset active index whenever the query or candidate set changes. Tracking
    // only filtered.length would miss cases where the length is stable but the
    // items themselves change (e.g. "fo"→[foo,food] then "ba"→[bar,baz]):
    // the persisted index would silently point at a different item.
    useEffect(() => {
      setActiveIndex(-1);
    }, [value, suggestions]);

    function select(suggestion: string) {
      onChange(suggestion);
      onSuggestionSelect?.(suggestion);
      setOpen(false);
      setActiveIndex(-1);
    }

    function handleKeyDown(e: KeyboardEvent<HTMLInputElement>) {
      if (!visible) {
        onKeyDown?.(e);
        return;
      }
      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          setActiveIndex((i) => Math.min(i + 1, filtered.length - 1));
          break;
        case "ArrowUp":
          e.preventDefault();
          setActiveIndex((i) => Math.max(i - 1, -1));
          break;
        case "Enter":
          if (activeIndex >= 0 && activeIndex < filtered.length) {
            e.preventDefault();
            select(filtered[activeIndex]);
          }
          break;
        case "Escape":
          setOpen(false);
          setActiveIndex(-1);
          break;
      }
      if (!e.defaultPrevented) {
        onKeyDown?.(e);
      }
    }

    return (
      <div className="relative">
        <input
          ref={ref}
          role="combobox"
          aria-autocomplete="list"
          aria-expanded={visible}
          aria-controls={visible ? listId : undefined}
          aria-activedescendant={
            visible && activeIndex >= 0
              ? `${listId}-${activeIndex}`
              : undefined
          }
          value={value}
          onChange={(e) => {
            onChange(e.target.value);
            setOpen(true);
          }}
          onFocus={(e) => {
            setOpen(true);
            onFocus?.(e);
          }}
          onBlur={(e) => {
            // Delay so a click on a list item fires before the blur closes it.
            setTimeout(() => setOpen(false), 120);
            onBlur?.(e);
          }}
          onKeyDown={handleKeyDown}
          className={cn(
            "flex h-9 w-full rounded-card border border-border bg-surface-2 px-3 py-1 text-sm text-text",
            "placeholder:text-muted focus-visible:border-border-hover focus-visible:outline-none",
            "focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-bg",
            "disabled:cursor-not-allowed disabled:opacity-50",
            clearable && "pr-9",
            className,
          )}
          {...props}
        />
        {clearable && (
          <ClearInlineButton label={clearLabel} onClick={() => onClear?.()} />
        )}
        {visible && (
          <ul
            ref={listRef}
            id={listId}
            role="listbox"
            className={cn(
              "absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-card border border-border",
              "bg-surface py-1 text-sm shadow-card",
            )}
          >
            {filtered.map((s, i) => (
              <li
                key={s}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === activeIndex}
                onMouseDown={(e) => {
                  e.preventDefault();
                  select(s);
                }}
                onMouseEnter={() => setActiveIndex(i)}
                className={cn(
                  "cursor-pointer px-3 py-1.5 text-xs transition-colors duration-[180ms]",
                  i === activeIndex
                    ? "bg-accent-dim text-accent"
                    : "text-text hover:bg-surface-hover hover:text-accent",
                )}
              >
                {s}
              </li>
            ))}
          </ul>
        )}
      </div>
    );
  },
);
Autocomplete.displayName = "Autocomplete";

export { Autocomplete };
