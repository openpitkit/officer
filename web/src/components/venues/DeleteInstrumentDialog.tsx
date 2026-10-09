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

import { Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
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

/** Controlled confirmation with caller-supplied description and error. */
export interface DeleteInstrumentDialogProps {
  open: boolean;
  description: string;
  busy: boolean;
  error?: string;
  onOpenChange: (open: boolean) => void;
  onSubmit: () => void;
}

/** Confirms instrument removal and optionally displays its mutation error. */
export function DeleteInstrumentDialog({
  open,
  description,
  busy,
  error,
  onOpenChange,
  onSubmit,
}: DeleteInstrumentDialogProps) {
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
          <AlertDialogTitle>{t("deleteInstrument.title")}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {t("common:actions.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="danger"
            onClick={(event) => {
              event.preventDefault();
              onSubmit();
            }}
            disabled={busy}
          >
            <Trash2 />
            {t("common:actions.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
