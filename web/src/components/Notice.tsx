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

import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

const TONES = {
  warn: {
    box: "border-[var(--warn)] bg-[var(--warn-dim)]",
    icon: "text-[var(--warn)]",
    title: "text-text",
    detail: "text-muted-lt",
  },
  danger: {
    box: "border-[var(--danger)] bg-[var(--danger-dim)]",
    icon: "text-[var(--danger)]",
    title: "text-[var(--danger)]",
    detail: "text-text",
  },
} as const;

export type NoticeProps = {
  /** Colours of the border, background, icon, title and detail. */
  tone: keyof typeof TONES;
  icon: LucideIcon;
  title: ReactNode;
  /** Detail under the title: paragraphs, links, a stale-state badge. */
  children: ReactNode;
  /** Control on the right, e.g. the button that resolves the notice. */
  action?: ReactNode;
  /** "alert" for a warning that must be announced when it appears. */
  role?: "alert";
};

/** Card that tells the operator about a condition: a tone-coloured icon, a
 *  title, its detail and an optional action. */
export function Notice({
  tone,
  icon: Icon,
  title,
  children,
  action,
  role,
}: NoticeProps) {
  return (
    <div
      role={role}
      className={cn(
        "flex flex-wrap items-start gap-3 rounded-card border px-3 py-2 text-sm text-text",
        TONES[tone].box,
      )}
    >
      <Icon
        aria-hidden="true"
        className={cn("mt-0.5 h-4 w-4 shrink-0", TONES[tone].icon)}
      />
      <div className="min-w-0 flex-1">
        <p className={cn("font-semibold", TONES[tone].title)}>{title}</p>
        <div className={cn("space-y-1 text-xs", TONES[tone].detail)}>
          {children}
        </div>
      </div>
      {action && <div className="self-center">{action}</div>}
    </div>
  );
}
