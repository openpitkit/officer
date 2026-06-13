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

import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1.5 rounded-[3px] border px-2 py-0.5 text-[0.6875rem] font-medium uppercase tracking-[0.07em]",
  {
    variants: {
      // Tones use solid borders + the accent-dim wash rather than Tailwind
      // alpha modifiers, because the palette colors are plain CSS vars without
      // an <alpha-value> channel, so `/opacity` would not apply to them.
      variant: {
        neutral: "border-[var(--tag-border)] text-muted",
        accent: "border-[var(--border-hover)] bg-accent-dim text-accent",
        ok: "border-[var(--ok)] bg-accent-dim text-[var(--ok)]",
        warn: "border-[var(--warn)] bg-accent-dim text-[var(--warn)]",
        danger: "border-[var(--danger)] bg-accent-dim text-[var(--danger)]",
      },
    },
    defaultVariants: {
      variant: "neutral",
    },
  },
);

export interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <span className={cn(badgeVariants({ variant }), className)} {...props} />
  );
}

// `badgeVariants` is a cva() result, not a literal constant, so it is not
// covered by `allowConstantExport`; shadcn/ui co-locates it with the component.
// eslint-disable-next-line react-refresh/only-export-components
export { Badge, badgeVariants };
