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

import { Loader2 } from "lucide-react";
import { useId, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { useTranslation } from "react-i18next";

import { isAccessModeConflict, putAccessMode } from "@/api/access";
import { OpenAccessWarning } from "@/components/AccessModePanel";
import { errMessage } from "@/components/AccountDialogs";
import { ErrorBanner } from "@/components/PageStates";
import { SecretInput } from "@/components/SecretInput";
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
import { ApiError, useApiClient } from "@/framework";
import { checkSession } from "@/lib/panelSession";

/**
 * Switches Officer to `open` access with `PUT /access/mode` once the operator
 * has read the warning, acknowledged it and entered the current password.
 * Mounted only while open, so the password lives only in the dialog; a failed
 * attempt drops it.
 */
export function SwitchToOpenDialog({
  onClose,
  onSwitched,
}: {
  onClose: () => void;
  /** Called once Officer runs in `open` access: the caller reloads the page. */
  onSwitched: () => void;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const client = useApiClient();
  const id = useId();
  const currentField = useRef<HTMLInputElement>(null);
  const [currentPassword, setCurrentPassword] = useState("");
  const [acknowledged, setAcknowledged] = useState(false);
  const [switching, setSwitching] = useState(false);
  const [currentError, setCurrentError] = useState<string | undefined>(
    undefined,
  );
  const [error, setError] = useState<string | null>(null);

  const switchToOpen = async () => {
    setSwitching(true);
    setCurrentError(undefined);
    setError(null);
    try {
      await putAccessMode(client, {
        mode: "open",
        currentPassword,
        acknowledged: true,
      });
    } catch (err) {
      const refused =
        err instanceof ApiError && err.code === "current_password_invalid";
      // Enabled again before the focus moves, so the field's description
      // reads the refusal out.
      flushSync(() => {
        setCurrentPassword("");
        setSwitching(false);
        if (refused) {
          setCurrentError(t("errors:code.current_password_invalid"));
        } else if (!isAccessModeConflict(err)) {
          setError(errMessage(err));
        }
      });
      if (refused) {
        currentField.current?.focus();
      } else if (isAccessModeConflict(err)) {
        checkSession();
      }
      return;
    }
    onSwitched();
  };

  return (
    <AlertDialog
      open
      onOpenChange={(open) => {
        if (!open && !switching) {
          onClose();
        }
      }}
    >
      <AlertDialogContent className="max-h-[90vh] max-w-xl overflow-y-auto">
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("security.access.switchDialog.title")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("security.access.switchDialog.description")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <OpenAccessWarning
          acknowledgement={{
            acknowledged,
            onChange: setAcknowledged,
            disabled: switching,
          }}
        />
        <SecretInput
          ref={currentField}
          id={`${id}-current`}
          label={t("security.access.currentPassword")}
          value={currentPassword}
          autoComplete="current-password"
          error={currentError}
          disabled={switching}
          onChange={setCurrentPassword}
        />
        {error !== null && <ErrorBanner message={error} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={switching}>
            {tc("actions.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="danger"
            disabled={switching || !acknowledged || currentPassword === ""}
            onClick={(event) => {
              event.preventDefault();
              void switchToOpen();
            }}
          >
            {switching && (
              <Loader2 aria-hidden="true" className="animate-spin" />
            )}
            {t("security.access.switchDialog.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
