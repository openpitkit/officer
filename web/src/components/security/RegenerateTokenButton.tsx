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

import { Loader2, RefreshCw } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { AccessTokenKind } from "@/api/access";
import { errMessage } from "@/components/AccountDialogs";
import { ErrorBanner } from "@/components/PageStates";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";

/**
 * Regenerate for one token, after a confirmation that says what stops working.
 * A refused request stays in the dialog until it is retried or cancelled.
 */
export function RegenerateTokenButton({
  kind,
  onRegenerate,
}: {
  kind: AccessTokenKind;
  /** Issues a new value; rejects with the request's error. */
  onRegenerate: () => Promise<void>;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const [confirming, setConfirming] = useState(false);
  const [regenerating, setRegenerating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const regenerate = async () => {
    setRegenerating(true);
    setError(null);
    try {
      await onRegenerate();
      setConfirming(false);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setRegenerating(false);
    }
  };

  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="border-[var(--danger)] text-[var(--danger)] hover:border-[var(--danger)] hover:bg-[var(--danger-dim)] hover:text-[var(--danger)]"
        onClick={() => setConfirming(true)}
      >
        <RefreshCw aria-hidden="true" />
        {t("security.tokens.regenerate")}
      </Button>
      <AlertDialog
        open={confirming}
        onOpenChange={(open) => {
          if (!open && !regenerating) {
            setConfirming(false);
            setError(null);
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(`security.tokens.${kind}.confirmTitle`)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(`security.tokens.${kind}.confirmDescription`)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {error !== null && <ErrorBanner message={error} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={regenerating}>
              {tc("actions.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="danger"
              disabled={regenerating}
              onClick={(event) => {
                event.preventDefault();
                void regenerate();
              }}
            >
              {regenerating && (
                <Loader2 aria-hidden="true" className="animate-spin" />
              )}
              {t("security.tokens.regenerate")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
