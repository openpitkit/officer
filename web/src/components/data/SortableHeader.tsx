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

import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import { useEffect, useRef } from "react";
import type { CSSProperties } from "react";

import { cn } from "@/lib/utils";

export type SortDirection = "none" | "asc" | "desc";

export interface SortableHeaderProps {
  /** Visible (and aria) label. */
  label: string;
  /** Tooltip sentence describing the column contents. */
  description?: string;
  /** The sort key sent back to `onSort` - your DB column / API sort param. */
  field: string;
  /** Current direction for this column. */
  direction?: SortDirection;
  /** Fired with the next direction on click / Enter / Space. */
  onSort?: (field: string, next: SortDirection) => void;
  align?: "left" | "right";
  style?: CSSProperties;
}

function nextDirection(direction: SortDirection): SortDirection {
  switch (direction) {
    case "none":
      return "asc";
    case "asc":
      return "desc";
    case "desc":
      return "none";
  }
}

/** Header label that cycles none -> asc -> desc -> none. */
export function SortableHeader({
  label,
  description,
  field,
  direction = "none",
  onSort,
  align = "left",
  style,
}: SortableHeaderProps) {
  const rootRef = useRef<HTMLSpanElement | null>(null);
  const active = direction !== "none";
  const Icon =
    direction === "asc"
      ? ArrowUp
      : direction === "desc"
        ? ArrowDown
        : ChevronsUpDown;

  const cycle = () => onSort?.(field, nextDirection(direction));

  useEffect(() => {
    const headerCell = rootRef.current?.closest("th");
    if (!headerCell) {
      return;
    }
    headerCell.setAttribute(
      "aria-sort",
      direction === "asc"
        ? "ascending"
        : direction === "desc"
          ? "descending"
          : "none",
    );
    return () => {
      headerCell.removeAttribute("aria-sort");
    };
  }, [direction]);

  return (
    <span
      ref={rootRef}
      role="button"
      tabIndex={0}
      title={description}
      aria-label={`Sort by ${label}`}
      aria-pressed={active}
      className={cn(
        "sortable-header inline-flex min-w-0 max-w-full cursor-pointer select-none items-center gap-1 text-[0.6875rem] font-bold uppercase tracking-[0.07em] transition-colors hover:text-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        align === "right" && "justify-end",
        active ? "text-accent" : "text-muted",
      )}
      style={style}
      onClick={cycle}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          cycle();
        }
      }}
    >
      <span className="min-w-0 truncate">{label}</span>
      <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
    </span>
  );
}
