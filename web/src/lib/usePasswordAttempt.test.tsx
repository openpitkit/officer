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

import { act, render, screen } from "@testing-library/react";
import { useLayoutEffect, useState } from "react";
import { describe, expect, it } from "vitest";

import { usePasswordAttempt } from "@/lib/usePasswordAttempt";

const PASSWORD = "a-new-panel-password";
const REFUSAL = "password must be at least 8 characters";
const FIELD = "New panel password";

type Caller = ReturnType<typeof usePasswordAttempt> & {
  busy: boolean;
  setBusy: (busy: boolean) => void;
};

/**
 * A caller that sends the password: its field is keyed on the attempt, holds
 * the hook's ref and is disabled while the caller is busy, as the access mode
 * panel is while a request runs. Each commit hands the caller out.
 */
function PasswordCaller({ onCommit }: { onCommit: (caller: Caller) => void }) {
  const {
    attempt,
    password,
    setPassword,
    passwordError,
    passwordRef,
    clearPasswordError,
    refuse,
  } = usePasswordAttempt();
  const [busy, setBusy] = useState(true);
  useLayoutEffect(() => {
    onCommit({
      attempt,
      password,
      setPassword,
      passwordError,
      passwordRef,
      clearPasswordError,
      refuse,
      busy,
      setBusy,
    });
  }, [
    onCommit,
    attempt,
    password,
    setPassword,
    passwordError,
    passwordRef,
    clearPasswordError,
    refuse,
    busy,
  ]);
  return (
    <input
      key={attempt}
      ref={passwordRef}
      aria-label={FIELD}
      disabled={busy}
    />
  );
}

/** The caller busy with a password it sent, with every commit it went through. */
function renderCaller() {
  const commits: Caller[] = [];
  render(
    <PasswordCaller
      onCommit={(caller) => {
        commits.push(caller);
      }}
    />,
  );
  const latest = () => commits[commits.length - 1];
  act(() => {
    latest().setPassword(PASSWORD);
  });
  return { commits, latest };
}

describe("usePasswordAttempt", () => {
  it("puts a password refusal under the field in one commit and focuses it", () => {
    const { commits, latest } = renderCaller();

    act(() => {
      latest().refuse(new Error(REFUSAL), true, () => latest().setBusy(false));
    });

    expect(latest().password).toBeNull();
    expect(latest().attempt).toBe(1);
    expect(latest().passwordError).toBe(REFUSAL);
    expect(latest().busy).toBe(false);
    // No commit shows the new attempt while the caller is still busy.
    expect(
      commits.filter((caller) => caller.attempt === 1 && caller.busy),
    ).toEqual([]);
    // The focus lands only on a field mounted anew and enabled before it moves.
    expect(screen.getByLabelText(FIELD)).toBeEnabled();
    expect(screen.getByLabelText(FIELD)).toHaveFocus();
  });

  it("leaves another refusal to the caller and keeps the focus", () => {
    const { latest } = renderCaller();

    act(() => {
      latest().refuse(new Error("database is locked"), false, () =>
        latest().setBusy(false),
      );
    });

    expect(latest().password).toBeNull();
    expect(latest().attempt).toBe(1);
    expect(latest().passwordError).toBeUndefined();
    expect(latest().busy).toBe(false);
    expect(screen.getByLabelText(FIELD)).not.toHaveFocus();
  });

  it("drops the refusal once the panel drops the password", () => {
    const { latest } = renderCaller();
    act(() => {
      latest().refuse(new Error(REFUSAL), true, () => latest().setBusy(false));
    });

    act(() => {
      latest().setPassword(null);
    });

    expect(latest().password).toBeNull();
    expect(latest().passwordError).toBeUndefined();
  });

  it("clears the refusal for a new attempt and keeps the password", () => {
    const { latest } = renderCaller();
    act(() => {
      latest().refuse(new Error(REFUSAL), true, () => latest().setBusy(false));
    });
    act(() => {
      latest().setPassword(PASSWORD);
    });

    act(() => {
      latest().clearPasswordError();
    });

    expect(latest().passwordError).toBeUndefined();
    expect(latest().password).toBe(PASSWORD);
  });
});
