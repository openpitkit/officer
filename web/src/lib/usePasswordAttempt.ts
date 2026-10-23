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

import { useCallback, useRef, useState } from "react";
import { flushSync } from "react-dom";

import { errMessage } from "@/components/AccountDialogs";

/**
 * A panel password entered through `AccessModePanel` and sent by the caller,
 * with what a refused attempt leaves. `attempt` keys the panel, so a refusal
 * mounts it anew without the password; `setPassword` is its
 * `onPasswordChange`; `passwordError` and `passwordRef` go to its props of
 * the same names.
 */
export function usePasswordAttempt() {
  const passwordRef = useRef<HTMLInputElement>(null);
  const [password, setPassword] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [passwordError, setPasswordError] = useState<string | undefined>(
    undefined,
  );

  /**
   * Follows the panel's password. Once the password is dropped, as when the
   * choice changes or the panel is left and the fields come back empty, the
   * refusal goes too.
   */
  const changePassword = useCallback((next: string | null) => {
    setPassword(next);
    if (next === null) {
      setPasswordError(undefined);
    }
  }, []);

  /** Clears the refusal under the password as a new attempt starts. */
  const clearPasswordError = useCallback(
    () => setPasswordError(undefined),
    [],
  );

  /**
   * Ends a refused attempt: drops the password, mounts the panel anew and
   * runs `settle`, the caller's own updates such as enabling the panel again,
   * all in one commit. A password refusal puts the message of `err` under the
   * password and moves the focus there.
   */
  const refuse = useCallback(
    (err: unknown, passwordRefused: boolean, settle: () => void) => {
      // Committed at once, the fields mounted anew and enabled, before the
      // focus moves to the refused one, so its description reads it out.
      flushSync(() => {
        setPassword(null);
        setAttempt((n) => n + 1);
        settle();
        if (passwordRefused) {
          setPasswordError(errMessage(err));
        }
      });
      if (passwordRefused) {
        passwordRef.current?.focus();
      }
    },
    [],
  );

  return {
    attempt,
    password,
    setPassword: changePassword,
    passwordError,
    passwordRef,
    clearPasswordError,
    refuse,
  };
}
