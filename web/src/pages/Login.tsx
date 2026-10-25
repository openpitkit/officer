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

import { Bot } from "lucide-react";
import { useRef, useState, type FormEvent } from "react";
import { flushSync } from "react-dom";
import { useTranslation } from "react-i18next";

import { logIn } from "@/api/access";
import { errMessage } from "@/components/AccountDialogs";
import { AuthScreen } from "@/components/AuthScreen";
import { ErrorBanner } from "@/components/PageStates";
import { SecretInput } from "@/components/SecretInput";
import { Button } from "@/components/ui/button";
import { ApiError, LinkButton, useApiClient } from "@/framework";
import { checkSession } from "@/lib/panelSession";
import { useRetryCountdown } from "@/lib/useRetryCountdown";

/** Why the server refused the last attempt. */
type Refusal = "invalid_password" | "too_many_attempts";

/**
 * The login screen of `token` access mode. The password lives only in this
 * component's state and is cleared after every failed attempt.
 */
export function Login({
  onLoggedIn,
  onForgotPassword,
}: {
  /** Called once the browser has a session. */
  onLoggedIn: () => void;
  onForgotPassword: () => void;
}) {
  const { t } = useTranslation("auth");
  const client = useApiClient();
  const passwordField = useRef<HTMLInputElement>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<Refusal | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [secondsLeft, startCountdown] = useRetryCountdown();
  const blocked = busy || secondsLeft > 0;

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    // Checked here rather than by disabling the button: a browser can keep an
    // autofilled value from scripts until the first gesture.
    if (blocked || password === "") {
      return;
    }
    setBusy(true);
    setRefusal(null);
    setError(null);
    try {
      await logIn(client, password);
    } catch (err) {
      if (
        err instanceof ApiError &&
        (err.code === "invalid_password" || err.code === "too_many_attempts")
      ) {
        const { code, retryAfterSeconds } = err;
        // Committed before the focus moves, so the field is enabled again and
        // its description already names the refusal the focus reads out.
        flushSync(() => {
          setPassword("");
          setBusy(false);
          setRefusal(code);
          if (retryAfterSeconds !== undefined) {
            startCountdown(retryAfterSeconds);
          }
        });
        passwordField.current?.focus();
        return;
      }
      setPassword("");
      setBusy(false);
      if (err instanceof ApiError && err.code === "conflict") {
        // Officer answers so in open access mode: the gate lets the browser
        // in once it checks the session.
        checkSession();
      } else {
        setError(errMessage(err));
      }
      return;
    }
    onLoggedIn();
  };

  let refusalText: string | undefined;
  if (refusal === "invalid_password") {
    refusalText =
      secondsLeft > 0
        ? t("login.wrongPasswordRetry", { seconds: secondsLeft })
        : t("login.wrongPassword");
  } else if (refusal === "too_many_attempts") {
    refusalText =
      secondsLeft > 0
        ? t("tooManyAttemptsRetry", { seconds: secondsLeft })
        : t("tooManyAttempts");
  }

  return (
    <AuthScreen title={t("login.title")} note={t("login.note")} noteIcon={Bot}>
      <form
        className="flex flex-col gap-y-4"
        onSubmit={(event) => {
          void submit(event);
        }}
      >
        <SecretInput
          ref={passwordField}
          id="login-password"
          label={t("login.password")}
          value={password}
          autoComplete="current-password"
          error={refusalText}
          disabled={busy}
          onChange={setPassword}
        />
        {error !== null && <ErrorBanner message={error} />}
        <Button type="submit" className="w-full" disabled={blocked}>
          {t("login.submit")}
        </Button>
      </form>
      <div>
        <LinkButton onClick={onForgotPassword}>
          {t("login.forgotPassword")}
        </LinkButton>
      </div>
    </AuthScreen>
  );
}
