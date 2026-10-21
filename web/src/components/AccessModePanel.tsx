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
  AlertTriangle,
  Bot,
  KeyRound,
  RefreshCw,
  ShieldCheck,
  ShieldOff,
  Terminal,
  User,
} from "lucide-react";
import { useId, useState, type Ref } from "react";
import { Trans, useTranslation } from "react-i18next";

import {
  PASSWORD_MAX_LENGTH,
  PASSWORD_MIN_LENGTH,
  passwordLength,
  passwordLengthAllowed,
  type AccessMode,
} from "@/api/access";
import { ChoiceCards } from "@/components/ChoiceCards";
import { Notice } from "@/components/Notice";
import { PasswordPairFields } from "@/components/PasswordPairFields";
import { RevealButton } from "@/components/RevealButton";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

/** Crockford's base32 alphabet: no I, L, O or U to misread. */
const PASSWORD_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
/** Characters of a generated password, separators aside: 100 bits. */
const GENERATED_LENGTH = 20;
/** Characters between two separators of a generated password. */
const GROUP_LENGTH = 4;

/**
 * A password of 20 random base32 characters in groups of four, such as
 * `7KQ4-MZ2P-X9RD-8VNW-C3TJ`.
 */
function generatePassword(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(GENERATED_LENGTH));
  // 256 is a multiple of the alphabet's 32 characters, so the low five bits
  // of a random byte pick every character with the same chance.
  const characters = Array.from(bytes, (byte) => PASSWORD_ALPHABET[byte & 31]);
  const groups: string[] = [];
  for (let start = 0; start < characters.length; start += GROUP_LENGTH) {
    groups.push(characters.slice(start, start + GROUP_LENGTH).join(""));
  }
  return groups.join("-");
}

/** Commands named in the boundary sentence, kept out of the catalogs. */
const DOCKER_EXEC = "docker exec";
const DOCKER_INSPECT = "docker inspect";

/** What the token and the password keep out, and how an agent stays out. */
function AccessBoundary() {
  const { t } = useTranslation("service");
  return (
    <div className="grid gap-x-6 gap-y-3 text-xs text-muted-lt md:grid-cols-[3fr_2fr]">
      <p>
        <Trans
          t={t}
          i18nKey="access.boundary"
          components={{
            exec: <code className="font-mono text-text">{DOCKER_EXEC}</code>,
            inspect: (
              <code className="font-mono text-text">{DOCKER_INSPECT}</code>
            ),
          }}
        />
      </p>
      <div className="space-y-1">
        <p className="font-semibold text-text">{t("access.isolation.title")}</p>
        <ul className="list-disc space-y-0.5 pl-4">
          <li>{t("access.isolation.sandbox")}</li>
          <li>{t("access.isolation.user")}</li>
          <li>{t("access.isolation.computer")}</li>
        </ul>
      </div>
    </div>
  );
}

/**
 * A new panel password and its repeat, with Generate, which fills both and
 * shows the value so a password manager can keep it, and Show. The reasons a
 * password cannot be sent show under the fields as they change. The draft
 * lives only here: a caller drops it, as after a failed attempt, by mounting
 * the fields anew.
 */
export function NewPasswordFields({
  ref,
  onPasswordChange,
  error,
  disabled,
}: {
  /** The new password input, for a caller that moves the focus to it. */
  ref?: Ref<HTMLInputElement>;
  /**
   * Called with the password once both fields hold the same allowed
   * password, and with null otherwise.
   */
  onPasswordChange: (password: string | null) => void;
  /** The server's refusal of the last password, shown until it is typed in. */
  error?: string;
  disabled: boolean;
}) {
  const { t } = useTranslation("service");
  const id = useId();
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  const [revealed, setRevealed] = useState(false);

  const change = (nextPassword: string, nextRepeat: string) => {
    setPassword(nextPassword);
    setRepeat(nextRepeat);
    onPasswordChange(
      passwordLengthAllowed(nextPassword) && nextPassword === nextRepeat
        ? nextPassword
        : null,
    );
  };

  const length = passwordLength(password);
  const lengthAllowed = passwordLengthAllowed(password);
  const lengthReason =
    password === "" || lengthAllowed
      ? undefined
      : length < PASSWORD_MIN_LENGTH
        ? t("access.password.tooShort", { length, min: PASSWORD_MIN_LENGTH })
        : t("access.password.tooLong", { length, max: PASSWORD_MAX_LENGTH });
  const mismatch =
    lengthAllowed && repeat !== "" && repeat !== password
      ? t("validation:password.mismatch")
      : undefined;

  return (
    <div className="space-y-3">
      <PasswordPairFields
        password={{
          ref,
          id: `${id}-password`,
          label: t("access.password.new"),
          value: password,
          error: password === "" && repeat === "" ? error : lengthReason,
          onChange: (value) => change(value, repeat),
        }}
        repeat={{
          id: `${id}-repeat`,
          label: t("access.password.repeat"),
          value: repeat,
          error: mismatch,
          onChange: (value) => change(password, value),
        }}
        revealed={revealed}
        verbatim
        disabled={disabled}
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={disabled}
          onClick={() => {
            const generated = generatePassword();
            setRevealed(true);
            change(generated, generated);
          }}
        >
          <RefreshCw aria-hidden="true" />
          {t("access.password.generate")}
        </Button>
        <RevealButton
          shown={revealed}
          onToggle={() => setRevealed(!revealed)}
          disabled={disabled}
          variant="ghost"
        />
        <p className="text-xs text-muted">
          {t("access.password.hint", { min: PASSWORD_MIN_LENGTH })}
        </p>
      </div>
    </div>
  );
}

const FACTS = [
  { key: "session", icon: User },
  { key: "agent", icon: Bot },
  { key: "programs", icon: Terminal },
  { key: "recovery", icon: KeyRound },
] as const;

/**
 * The warning of `open` access: who can use Officer and what they can do,
 * that web pages stay locked out, and how to close it. With an
 * `acknowledgement` it ends in the checkbox a caller asks for before it
 * switches to `open`.
 */
export function OpenAccessWarning({
  acknowledgement,
}: {
  acknowledgement?: {
    acknowledged: boolean;
    onChange: (acknowledged: boolean) => void;
    disabled: boolean;
  };
}) {
  const { t } = useTranslation("service");
  return (
    <Notice
      tone="danger"
      icon={AlertTriangle}
      title={t("access.warning.title")}
    >
      <p>{t("access.warning.who")}</p>
      <ul className="list-disc space-y-0.5 pl-5">
        <li>{t("access.warning.programs")}</li>
        <li>{t("access.warning.containers")}</li>
        <li>{t("access.warning.agents")}</li>
      </ul>
      <p>{t("access.warning.what")}</p>
      <p className="text-muted-lt">{t("access.warning.pages")}</p>
      <p>{t("access.warning.close")}</p>
      {acknowledgement !== undefined && (
        <label className="flex items-center gap-2 pt-1 text-sm font-medium text-text">
          <Input
            type="checkbox"
            className="h-4 w-4"
            checked={acknowledgement.acknowledged}
            disabled={acknowledgement.disabled}
            onChange={(event) => acknowledgement.onChange(event.target.checked)}
          />
          {t("access.warning.acknowledge")}
        </label>
      )}
    </Notice>
  );
}

/**
 * The access mode choice, the one the first-run wizard and Service › Security
 * both show: the boundary no mode moves, the choice between "Token and
 * password" and "No token", and the body of the selected one - the new panel
 * password with what it means, or the warning of `open` access. It sends
 * nothing: the caller owns the action and the request.
 */
export function AccessModePanel({
  mode,
  onModeChange,
  onPasswordChange,
  passwordError,
  passwordRef,
  disabled,
}: {
  /** The selected choice. */
  mode: AccessMode;
  onModeChange: (mode: AccessMode) => void;
  /**
   * Called with the new panel password once both fields hold the same allowed
   * password, and with null otherwise, a change of the choice included.
   */
  onPasswordChange: (password: string | null) => void;
  /** The server's refusal of the last password, shown under the password. */
  passwordError?: string;
  /** The new password input, for a caller that moves the focus to it. */
  passwordRef?: Ref<HTMLInputElement>;
  disabled: boolean;
}) {
  const { t } = useTranslation("service");
  return (
    <div className="space-y-4">
      <AccessBoundary />
      <ChoiceCards
        label={t("access.choice")}
        options={[
          {
            value: "token",
            title: t("access.modes.token.title"),
            description: t("access.modes.token.description"),
            icon: ShieldCheck,
            badge: t("access.recommended"),
          },
          {
            value: "open",
            title: t("access.modes.open.title"),
            description: t("access.modes.open.description"),
            icon: ShieldOff,
          },
        ]}
        value={mode}
        onChange={(next) => {
          onPasswordChange(null);
          onModeChange(next);
        }}
        disabled={disabled}
      />
      {mode === "token" ? (
        <div className="space-y-3">
          <NewPasswordFields
            ref={passwordRef}
            onPasswordChange={onPasswordChange}
            error={passwordError}
            disabled={disabled}
          />
          <ul className="border-t border-border">
            {FACTS.map(({ key, icon: Icon }) => (
              <li
                key={key}
                className="flex gap-2.5 border-b border-border py-2 text-xs text-muted-lt"
              >
                <Icon
                  aria-hidden="true"
                  className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted"
                />
                <span>{t(`access.facts.${key}`)}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <OpenAccessWarning />
      )}
    </div>
  );
}
