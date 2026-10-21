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

import { ArrowLeft, Info } from "lucide-react";
import {
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { flushSync } from "react-dom";
import { useTranslation } from "react-i18next";

import {
  isAccessModeConflict,
  PASSWORD_MAX_LENGTH,
  PASSWORD_MIN_LENGTH,
  passwordLengthAllowed,
  requestRecoveryCode,
  resetPassword,
  type RecoveryCodeState,
} from "@/api/access";
import { errMessage } from "@/components/AccountDialogs";
import { AuthScreen } from "@/components/AuthScreen";
import { CopyableSnippet } from "@/components/CopyableSnippet";
import { ErrorBanner } from "@/components/PageStates";
import { PasswordPairFields } from "@/components/PasswordPairFields";
import { TextField } from "@/components/TextField";
import { Button } from "@/components/ui/button";
import { ApiError, LinkButton, useApiClient } from "@/framework";
import { formatTime } from "@/i18n/format";
import { checkSession } from "@/lib/panelSession";
import { useRetryCountdown } from "@/lib/useRetryCountdown";

/** Where the code is printed, for an Officer that runs in Docker. */
const LOGS_COMMAND = "docker logs --tail 20 pit-officer";
/** The command whose terminal shows the code without Docker. */
const SERVE_COMMAND = "pit-officer serve";

/** One numbered step of the recovery. */
function RecoveryStep({
  number,
  title,
  children,
}: {
  number: number;
  title: string;
  children: ReactNode;
}) {
  return (
    <li className="flex gap-3 border-t border-border pt-4">
      <span
        aria-hidden
        className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-border text-[0.6875rem] text-muted-lt"
      >
        {number}
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <h2 className="text-sm font-bold text-text">{title}</h2>
        {children}
      </div>
    </li>
  );
}

/** The first step: ask Officer to print a recovery code to its log. */
function CodeRequestStep() {
  const { t } = useTranslation("auth");
  const client = useApiClient();
  const refusalLine = useRef<HTMLParagraphElement>(null);
  const [requesting, setRequesting] = useState(false);
  const [requested, setRequested] = useState<RecoveryCodeState | null>(null);
  const [refused, setRefused] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [secondsLeft, startCountdown] = useRetryCountdown();

  const requestCode = async () => {
    setRequesting(true);
    setRequested(null);
    setRefused(false);
    setError(null);
    try {
      setRequested(await requestRecoveryCode(client));
    } catch (err) {
      if (err instanceof ApiError && err.code === "too_many_attempts") {
        const { retryAfterSeconds } = err;
        flushSync(() => {
          setRefused(true);
          if (retryAfterSeconds !== undefined) {
            startCountdown(retryAfterSeconds);
          }
        });
        refusalLine.current?.focus();
      } else if (isAccessModeConflict(err)) {
        // Officer answers so in open access mode: the gate lets the browser
        // in once it checks the session.
        checkSession();
      } else {
        setError(errMessage(err));
      }
    } finally {
      setRequesting(false);
    }
  };

  return (
    <RecoveryStep number={1} title={t("recovery.request.title")}>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={requesting || secondsLeft > 0}
        onClick={() => {
          void requestCode();
        }}
      >
        {t("recovery.request.submit")}
      </Button>
      {requested !== null && (
        <p
          role="status"
          className={
            requested.issued
              ? "text-xs text-[var(--ok)]"
              : "text-xs text-muted-lt"
          }
        >
          {requested.issued
            ? t("recovery.request.issued")
            : t("recovery.request.alreadyIssued", {
                time: formatTime(requested.expiresAt),
              })}
        </p>
      )}
      {refused && (
        // Not a live region, as its count changes every second: the focus
        // moves here once instead, so the refusal is read when it appears.
        <p
          ref={refusalLine}
          tabIndex={-1}
          className="text-xs text-[var(--danger)] focus:outline-none"
        >
          {secondsLeft > 0
            ? t("tooManyAttemptsRetry", { seconds: secondsLeft })
            : t("tooManyAttempts")}
        </p>
      )}
      {error !== null && <ErrorBanner message={error} />}
    </RecoveryStep>
  );
}

/** The last step: set a new password with the code from the log. */
function PasswordResetStep({
  onPasswordReset,
}: {
  onPasswordReset: () => void;
}) {
  const { t } = useTranslation("auth");
  const client = useApiClient();
  const codeField = useRef<HTMLInputElement>(null);
  const passwordField = useRef<HTMLInputElement>(null);
  const repeatField = useRef<HTMLInputElement>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  // True once the input checks stopped a submit: their reasons then follow the
  // fields as they change, until an attempt reaches the server.
  const [checking, setChecking] = useState(false);
  const [codeError, setCodeError] = useState<string | undefined>(undefined);
  const [passwordError, setPasswordError] = useState<string | undefined>(
    undefined,
  );
  const [resetting, setResetting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const codeMissing = code === "";
  const lengthRefused = !passwordLengthAllowed(password);
  const mismatched = password !== repeat;

  // Commits a refusal, then moves the focus to the refused field, so the
  // field's description reads the refusal out.
  const refuse = (
    field: RefObject<HTMLInputElement | null>,
    show: () => void,
  ) => {
    flushSync(() => {
      setResetting(false);
      show();
    });
    field.current?.focus();
  };

  const reset = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (resetting) {
      return;
    }
    setCodeError(undefined);
    setPasswordError(undefined);
    setError(null);
    if (codeMissing || lengthRefused || mismatched) {
      const field = codeMissing
        ? codeField
        : lengthRefused
          ? passwordField
          : repeatField;
      refuse(field, () => setChecking(true));
      return;
    }
    setChecking(false);
    setResetting(true);
    try {
      await resetPassword(client, { code, password });
    } catch (err) {
      setPassword("");
      setRepeat("");
      if (err instanceof ApiError && err.code === "invalid_recovery_code") {
        refuse(codeField, () => setCodeError(t("recovery.reset.invalidCode")));
      } else if (err instanceof ApiError && err.code === "precondition") {
        const { message } = err;
        refuse(passwordField, () => setPasswordError(message));
      } else if (isAccessModeConflict(err)) {
        // Officer answers so in open access mode: the gate lets the browser
        // in once it checks the session.
        setResetting(false);
        checkSession();
      } else {
        setResetting(false);
        setError(errMessage(err));
      }
      return;
    }
    onPasswordReset();
  };

  return (
    <RecoveryStep number={3} title={t("recovery.reset.title")}>
      <form
        className="space-y-3"
        onSubmit={(event) => {
          void reset(event);
        }}
      >
        <TextField
          ref={codeField}
          id="recovery-code"
          type="text"
          label={t("recovery.reset.code")}
          value={code}
          autoComplete="one-time-code"
          error={
            checking && codeMissing
              ? t("recovery.reset.codeRequired")
              : codeError
          }
          disabled={resetting}
          onChange={setCode}
        />
        <PasswordPairFields
          password={{
            ref: passwordField,
            id: "recovery-password",
            label: t("recovery.reset.password"),
            value: password,
            error:
              checking && lengthRefused
                ? t("recovery.reset.length", {
                    min: PASSWORD_MIN_LENGTH,
                    max: PASSWORD_MAX_LENGTH,
                  })
                : passwordError,
            onChange: setPassword,
          }}
          repeat={{
            ref: repeatField,
            id: "recovery-repeat",
            label: t("recovery.reset.repeat"),
            value: repeat,
            error:
              checking && mismatched
                ? t("validation:password.mismatch")
                : undefined,
            onChange: setRepeat,
          }}
          disabled={resetting}
        />
        {error !== null && <ErrorBanner message={error} />}
        <Button type="submit" className="w-full" disabled={resetting}>
          {t("recovery.reset.submit")}
        </Button>
      </form>
    </RecoveryStep>
  );
}

/**
 * The forgotten-password screen of `token` access mode: ask Officer to print a
 * recovery code to its log, read it there, and set a new password with it.
 * The new password lives only in the reset step's state and is cleared after
 * every failed attempt.
 */
export function PasswordRecovery({
  onPasswordReset,
  onBack,
}: {
  /** Called once the new password is set and the browser has a session. */
  onPasswordReset: () => void;
  /** Returns to the login screen. */
  onBack: () => void;
}) {
  const { t } = useTranslation("auth");
  const { t: tc } = useTranslation();
  return (
    <AuthScreen
      title={t("recovery.title")}
      note={t("recovery.note")}
      noteIcon={Info}
      wide
    >
      <ol className="space-y-4">
        <CodeRequestStep />
        <RecoveryStep number={2} title={t("recovery.read.title")}>
          <p className="text-xs text-muted-lt">
            {t("recovery.read.withoutDocker", { command: SERVE_COMMAND })}
          </p>
          <CopyableSnippet text={LOGS_COMMAND} rows={1} />
        </RecoveryStep>
        <PasswordResetStep onPasswordReset={onPasswordReset} />
      </ol>
      <div>
        <LinkButton onClick={onBack}>
          <ArrowLeft aria-hidden />
          {tc("actions.back")}
        </LinkButton>
      </div>
    </AuthScreen>
  );
}
