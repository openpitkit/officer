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

import {
  ArrowLeft,
  ArrowRight,
  Info,
  Loader2,
  SlidersHorizontal,
} from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

/** What the footer of a setup step offers. */
export type StepFooterProps = {
  /** Where the decision of the step can be changed later. */
  where?: string;
  /** A note in place of `where`. */
  note?: string;
  back?: () => void;
  /** A control before Back and the primary button. */
  extra?: ReactNode;
  next: () => void;
  /** The primary button's label; Continue unless given. */
  nextLabel?: string;
  nextDisabled?: boolean;
};

/**
 * The footer of a setup step: where its decision changes later, or a note,
 * then Back and the primary button, both disabled while `busy`.
 */
export function StepFooter({
  where,
  note,
  back,
  extra,
  next,
  nextLabel,
  nextDisabled = false,
  busy,
}: StepFooterProps & { busy: boolean }) {
  const { t } = useTranslation("setup");
  const { t: tc } = useTranslation();
  return (
    <div className="border-t border-border bg-bg">
      <div className="mx-auto flex w-full max-w-3xl flex-wrap items-center gap-3 px-4 py-3 md:px-10">
        <p className="mr-auto flex min-w-0 items-center gap-1.5 text-xs text-muted">
          {where !== undefined && (
            <>
              <SlidersHorizontal
                aria-hidden="true"
                className="h-3.5 w-3.5 shrink-0"
              />
              <span>{t("changeLater", { where })}</span>
            </>
          )}
          {note !== undefined && (
            <>
              <Info aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
              <span>{note}</span>
            </>
          )}
        </p>
        {extra}
        {back !== undefined && (
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={back}
          >
            <ArrowLeft aria-hidden="true" />
            {tc("actions.back")}
          </Button>
        )}
        <Button type="button" disabled={busy || nextDisabled} onClick={next}>
          {nextLabel ?? t("continue")}
          {busy ? (
            <Loader2 aria-hidden="true" className="animate-spin" />
          ) : (
            <ArrowRight aria-hidden="true" />
          )}
        </Button>
      </div>
    </div>
  );
}
