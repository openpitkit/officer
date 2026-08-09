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
  type HTMLAttributes,
  type TdHTMLAttributes,
  type ThHTMLAttributes,
} from "react";

import { cn } from "@/lib/utils";

// Column visibility by breakpoint: pass hideBelow="md" on secondary columns to
// drop them on mobile; primary columns omit the prop.
const HIDE_BELOW = {
  sm: "hidden sm:table-cell",
  md: "hidden md:table-cell",
  lg: "hidden lg:table-cell",
} as const;

const Table = forwardRef<HTMLTableElement, HTMLAttributes<HTMLTableElement>>(
  ({ className, ...props }, ref) => (
    <div className="ledger-table-wrap w-full overflow-x-auto">
      <table
        ref={ref}
        className={cn(
          "ledger-table w-full caption-bottom border-collapse text-[length:var(--dens-table-fz)]",
          className,
        )}
        {...props}
      />
    </div>
  ),
);
Table.displayName = "Table";

const TableHeader = forwardRef<
  HTMLTableSectionElement,
  HTMLAttributes<HTMLTableSectionElement>
>(({ className, ...props }, ref) => (
  <thead
    ref={ref}
    className={cn("bg-bg [&_tr]:border-b [&_tr]:border-border", className)}
    {...props}
  />
));
TableHeader.displayName = "TableHeader";

const TableBody = forwardRef<
  HTMLTableSectionElement,
  HTMLAttributes<HTMLTableSectionElement>
>(({ className, ...props }, ref) => (
  <tbody
    ref={ref}
    className={cn("[&_tr:last-child]:border-0", className)}
    {...props}
  />
));
TableBody.displayName = "TableBody";

const TableRow = forwardRef<
  HTMLTableRowElement,
  HTMLAttributes<HTMLTableRowElement>
>(({ className, ...props }, ref) => (
  <tr
    ref={ref}
    className={cn(
      "border-b border-border transition-colors duration-[180ms] hover:bg-accent-dim",
      className,
    )}
    {...props}
  />
));
TableRow.displayName = "TableRow";

const TableHead = forwardRef<
  HTMLTableCellElement,
  ThHTMLAttributes<HTMLTableCellElement> & { hideBelow?: "sm" | "md" | "lg" }
>(({ className, hideBelow, ...props }, ref) => (
  <th
    ref={ref}
    className={cn(
      "h-[var(--dens-head-h)] bg-bg px-[var(--dens-cell-px)] text-left align-middle text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted",
      hideBelow && HIDE_BELOW[hideBelow],
      className,
    )}
    {...props}
  />
));
TableHead.displayName = "TableHead";

const TableCell = forwardRef<
  HTMLTableCellElement,
  TdHTMLAttributes<HTMLTableCellElement> & { hideBelow?: "sm" | "md" | "lg" }
>(({ className, hideBelow, ...props }, ref) => (
  <td
    ref={ref}
    className={cn(
      "px-[var(--dens-cell-px)] py-[var(--dens-cell-py)] align-middle text-text",
      hideBelow && HIDE_BELOW[hideBelow],
      className,
    )}
    {...props}
  />
));
TableCell.displayName = "TableCell";

export { Table, TableHeader, TableBody, TableRow, TableHead, TableCell };
