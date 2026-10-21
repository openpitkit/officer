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

import { useId, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * One setting of Service › Security, a group named by its label: the label,
 * the value, and the actions on it. Without actions the value takes the rest
 * of the row.
 */
export function SecurityRow({
  label,
  actions,
  children,
}: {
  label: string;
  actions?: ReactNode;
  /** The value. */
  children: ReactNode;
}) {
  const labelId = useId();
  return (
    <div
      role="group"
      aria-labelledby={labelId}
      className={cn(
        "grid gap-x-4 gap-y-2 border-b border-border py-3 last:border-0",
        actions === undefined
          ? "sm:grid-cols-[13rem_minmax(0,1fr)]"
          : "sm:grid-cols-[13rem_minmax(0,1fr)_auto] sm:items-center",
      )}
    >
      <p id={labelId} className="text-sm font-medium text-text">
        {label}
      </p>
      <div className="min-w-0 space-y-1 text-xs text-muted-lt">{children}</div>
      {actions !== undefined && (
        <div className="flex flex-wrap items-center gap-1.5 sm:justify-end">
          {actions}
        </div>
      )}
    </div>
  );
}
