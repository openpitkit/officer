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

import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/** A label/value pair in a status card. Values default to monospace tabular
 *  numerics so columns line up; pass `mono={false}` for free-form text. */
export function StatRow({
  label,
  value,
  mono = true,
  className,
}: {
  label: string;
  value: ReactNode;
  mono?: boolean;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex items-center justify-between gap-4 border-b border-border py-2 last:border-0",
        className,
      )}
    >
      <span className="text-xs text-muted">{label}</span>
      <span
        className={cn(
          "truncate text-right text-sm text-text",
          mono && "nums",
        )}
        title={typeof value === "string" ? value : undefined}
      >
        {value}
      </span>
    </div>
  );
}
