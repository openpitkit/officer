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

import { Check, Lock, ShieldCheck } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import { BrandLockup } from "@/components/BrandLockup";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { WIZARD_STEPS, type WizardStep } from "@/components/setup/steps";
import { cn } from "@/lib/utils";

/**
 * The rail of the setup wizard: the brand, the steps with their chosen
 * values, whether Officer answers yet, and the language and theme switches.
 * In the first run, before completion a step up to the current one can be
 * opened, and after it none can. In the review of a completed setup, under
 * the name of the guide, every step can be opened at any time.
 */
export function SetupRail({
  review,
  current,
  finished,
  values,
  busy,
  onJump,
}: {
  /** Whether the rail leads the review of a completed setup. */
  review: boolean;
  /** The index of the current step. */
  current: number;
  finished: boolean;
  /** The chosen value of each step, null where it has none to show. */
  values: (string | null)[];
  busy: boolean;
  onJump: (step: WizardStep) => void;
}) {
  const { t } = useTranslation("setup");
  const { t: ts } = useTranslation("service");
  const titleId = useId();
  return (
    <aside className="flex shrink-0 flex-col gap-5 border-b border-border bg-surface px-3 py-4 md:w-64 md:overflow-y-auto md:border-b-0 md:border-r">
      <div className="flex items-center gap-[var(--dens-nav-gap)] px-2">
        <BrandLockup />
      </div>
      <nav aria-labelledby={titleId} className="space-y-2">
        <p
          id={titleId}
          className="px-2 text-[0.6875rem] font-medium uppercase tracking-[0.07em] text-muted"
        >
          {review ? ts("security.guide.label") : t("firstStart")}
        </p>
        <ol className="space-y-0.5">
          {WIZARD_STEPS.map((id, index) => {
            const passed = index < current;
            const here = index === current;
            const open = review || (!finished && index <= current);
            const value = values[index];
            return (
              <li key={id}>
                <button
                  type="button"
                  disabled={!open || busy}
                  aria-current={here ? "step" : undefined}
                  onClick={() => onJump(id)}
                  className={cn(
                    "flex w-full items-center gap-2.5 rounded-[3px] px-2 py-1.5 text-left text-sm transition-colors",
                    here && "bg-accent-dim font-medium text-text",
                    passed && "text-text",
                    !here && !passed && "text-muted",
                    open && !here && "hover:bg-surface-hover hover:text-accent",
                  )}
                >
                  <span
                    aria-hidden="true"
                    className={cn(
                      "grid h-5 w-5 shrink-0 place-items-center rounded-full border text-[0.6875rem]",
                      passed &&
                        "border-[var(--ok)] bg-[var(--ok-dim)] text-[var(--ok)]",
                      here && "border-accent text-accent",
                      !here && !passed && "border-border text-muted",
                    )}
                  >
                    {passed ? <Check className="h-3 w-3" /> : index + 1}
                  </span>
                  <span className="min-w-0 flex-1 truncate">
                    {t(`rail.steps.${id}`)}
                  </span>
                  {value !== null && (
                    <span className="max-w-[8rem] shrink-0 truncate text-[0.6875rem] text-muted">
                      {value}
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ol>
      </nav>
      <div className="mt-auto space-y-3">
        <p className="flex items-start gap-2 border-t border-border px-2 pt-3 text-xs text-muted-lt">
          {finished ? (
            <ShieldCheck
              aria-hidden="true"
              className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--ok)]"
            />
          ) : (
            <Lock
              aria-hidden="true"
              className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--warn)]"
            />
          )}
          <span>{t(finished ? "rail.finished" : "rail.closed")}</span>
        </p>
        <div className="flex items-center gap-2 px-2">
          <LanguageSwitch />
          <ThemeSwitch />
        </div>
      </div>
    </aside>
  );
}
