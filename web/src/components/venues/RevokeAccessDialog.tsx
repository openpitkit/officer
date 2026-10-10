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

import { useTranslation } from "react-i18next";

import type { TradingAccessSubject } from "@/api/types";
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

/** Controlled confirmation with caller-supplied busy state and error; the
 *  caller performs the revoke and closes it. */
export interface RevokeAccessDialogProps {
  open: boolean;
  /** Whose grant is revoked; null only while closed. */
  subject: TradingAccessSubject | null;
  /** Label of the trading connection the grant is on. */
  connectionLabel: string;
  /** Disables cancelling, closing and confirming while the revoke is in
   *  flight. */
  busy: boolean;
  error?: string;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
}

/** Confirms revoking an account's or a group's grant on a trading
 *  connection; for a group, warns that its accounts keep access only through
 *  their own grants. */
export function RevokeAccessDialog({
  open,
  subject,
  connectionLabel,
  busy,
  error,
  onOpenChange,
  onConfirm,
}: RevokeAccessDialogProps) {
  const { t } = useTranslation("venues");
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!busy) onOpenChange(next);
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {subject?.accountGroup !== undefined
              ? t("revokeAccess.groupTitle")
              : t("revokeAccess.accountTitle")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {subject?.accountGroup !== undefined
              ? t("revokeAccess.groupDescription", {
                  group: subject.accountGroup,
                  label: connectionLabel,
                })
              : t("revokeAccess.accountDescription", {
                  account: subject?.account,
                  label: connectionLabel,
                })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {t("common:actions.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="danger"
            disabled={busy}
            onClick={(event) => {
              event.preventDefault();
              onConfirm();
            }}
          >
            {t("actions.revokeAccess")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
