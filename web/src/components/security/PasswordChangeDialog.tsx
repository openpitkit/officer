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

import { isAccessModeConflict, putAccessPassword } from "@/api/access";
import { NewPasswordFields } from "@/components/AccessModePanel";
import { errMessage } from "@/components/AccountDialogs";
import { ErrorBanner } from "@/components/PageStates";
import { SecretInput } from "@/components/SecretInput";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ApiError, useApiClient } from "@/framework";
import { checkSession } from "@/lib/panelSession";

/**
 * Changes the panel password with `PUT /access/password`: the current one,
 * then the new one; every other browser is logged out. Mounted only while
 * open, so the passwords live only in the dialog; a failed attempt drops them.
 */
export function PasswordChangeDialog({
  onClose,
  onChanged,
}: {
  onClose: () => void;
  /** Called once the password is changed. */
  onChanged: () => void;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const client = useApiClient();
  const id = useId();
  const currentField = useRef<HTMLInputElement>(null);
  const passwordField = useRef<HTMLInputElement>(null);
  const [currentPassword, setCurrentPassword] = useState("");
  const [password, setPassword] = useState<string | null>(null);
  // Bumped by a failed attempt: the new password fields mount anew, empty.
  const [attempt, setAttempt] = useState(0);
  const [changing, setChanging] = useState(false);
  const [currentError, setCurrentError] = useState<string | undefined>(
    undefined,
  );
  const [passwordError, setPasswordError] = useState<string | undefined>(
    undefined,
  );
  const [error, setError] = useState<string | null>(null);

  const change = async () => {
    if (password === null) {
      return;
    }
    setChanging(true);
    setCurrentError(undefined);
    setPasswordError(undefined);
    setError(null);
    try {
      await putAccessPassword(client, { currentPassword, password });
    } catch (err) {
      const code = err instanceof ApiError ? err.code : null;
      // Committed at once, the fields mounted anew and enabled, before the
      // focus moves to the refused one, so its description reads it out.
      flushSync(() => {
        setCurrentPassword("");
        setPassword(null);
        setAttempt((n) => n + 1);
        setChanging(false);
        if (code === "current_password_invalid") {
          setCurrentError(t("errors:code.current_password_invalid"));
        } else if (code === "precondition") {
          setPasswordError(errMessage(err));
        } else if (!isAccessModeConflict(err)) {
          setError(errMessage(err));
        }
      });
      if (code === "current_password_invalid") {
        currentField.current?.focus();
      } else if (code === "precondition") {
        passwordField.current?.focus();
      } else if (isAccessModeConflict(err)) {
        checkSession();
      }
      return;
    }
    onChanged();
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !changing) {
          onClose();
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("security.access.changeDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("security.access.changeDialog.description")}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            void change();
          }}
        >
          <SecretInput
            ref={currentField}
            id={`${id}-current`}
            label={t("security.access.currentPassword")}
            value={currentPassword}
            autoComplete="current-password"
            error={currentError}
            disabled={changing}
            onChange={setCurrentPassword}
          />
          <NewPasswordFields
            key={attempt}
            ref={passwordField}
            onPasswordChange={setPassword}
            error={passwordError}
            disabled={changing}
          />
          {error !== null && <ErrorBanner message={error} />}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={changing}
              onClick={onClose}
            >
              {tc("actions.cancel")}
            </Button>
            <Button
              type="submit"
              size="sm"
              disabled={changing || currentPassword === "" || password === null}
            >
              {changing && (
                <Loader2 aria-hidden="true" className="animate-spin" />
              )}
              {t("security.access.changeDialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
