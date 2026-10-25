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

import { BrandLockup } from "@/components/BrandLockup";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

/**
 * A screen the startup gate shows in place of the shell: the brand, a title
 * and the screen's content on a centered card, with a note at the bottom.
 */
export function AuthScreen({
  title,
  note,
  noteIcon: NoteIcon,
  wide = false,
  children,
}: {
  title: string;
  note: string;
  noteIcon: LucideIcon;
  /** A wider card for a screen with several steps. */
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="h-dvh w-full overflow-y-auto bg-bg text-text">
      <div className="flex min-h-full items-center justify-center p-6">
        <Card className={cn("w-full", wide ? "max-w-xl" : "max-w-md")}>
          <CardContent className="flex flex-col gap-y-5 pt-[var(--dens-card-pad)]">
            <div className="flex items-center gap-[var(--dens-nav-gap)]">
              <BrandLockup />
            </div>
            <h1 className="ledger-title text-2xl">{title}</h1>
            {children}
            <div className="flex gap-2 border-t border-border pt-4 text-xs text-muted">
              <NoteIcon className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />
              <p>{note}</p>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
