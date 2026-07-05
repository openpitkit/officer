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
import type { ReactNode } from "react";

import type { SortOrder } from "@/api/types";
import { TableHead } from "@/components/ui/table";
import { cn } from "@/lib/utils";

export function SortableTableHead({
  sortKey,
  activeSort,
  activeOrder,
  className,
  children,
  onSortChange,
}: {
  sortKey: string;
  activeSort?: string;
  activeOrder?: SortOrder;
  className?: string;
  children: ReactNode;
  onSortChange: (sort?: string, order?: SortOrder) => void;
}) {
  const active = activeSort === sortKey;
  const Icon = active
    ? activeOrder === "desc"
      ? ArrowDown
      : ArrowUp
    : ChevronsUpDown;
  return (
    <TableHead
      className={className}
      aria-sort={
        active
          ? activeOrder === "desc"
            ? "descending"
            : "ascending"
          : "none"
      }
    >
      <button
        type="button"
        className={cn(
          "inline-flex items-center gap-1 text-left text-inherit transition-colors hover:text-text",
          active && "text-accent",
        )}
        onClick={() => {
          if (!active) {
            onSortChange(sortKey, "asc");
            return;
          }
          if (activeOrder === "asc") {
            onSortChange(sortKey, "desc");
            return;
          }
          onSortChange(undefined, undefined);
        }}
      >
        <span>{children}</span>
        <Icon className="h-3 w-3" aria-hidden="true" />
      </button>
    </TableHead>
  );
}
