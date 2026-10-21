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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AccessMode } from "@/api/access";
import {
  AccessModePanel,
  OpenAccessWarning,
} from "@/components/AccessModePanel";

const PASSWORD = "correct-horse-battery-staple";
const WARNING_TITLE =
  "Without a token, Officer is open to everything on this computer";
const ACKNOWLEDGEMENT =
  "I understand: any software on this computer can control Officer.";
// Crockford's base32 in groups of four: no I, L, O or U.
const GENERATED =
  /^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$/;

function renderPanel({
  mode = "token",
  passwordError,
  disabled = false,
}: {
  mode?: AccessMode;
  passwordError?: string;
  disabled?: boolean;
} = {}) {
  const onModeChange = vi.fn();
  const onPasswordChange = vi.fn();
  function Controlled() {
    const [current, setCurrent] = useState<AccessMode>(mode);
    return (
      <AccessModePanel
        mode={current}
        onModeChange={(next) => {
          onModeChange(next);
          setCurrent(next);
        }}
        onPasswordChange={onPasswordChange}
        passwordError={passwordError}
        disabled={disabled}
      />
    );
  }
  render(<Controlled />);
  return { onModeChange, onPasswordChange };
}

function passwordField() {
  return screen.getByLabelText("New panel password");
}

function repeatField() {
  return screen.getByLabelText("Repeat the new password");
}

// Answers getRandomValues with `bytes`, once.
function randomBytes(bytes: number[]) {
  return vi.spyOn(crypto, "getRandomValues").mockImplementationOnce((array) => {
    (array as Uint8Array).set(bytes);
    return array;
  });
}

describe("AccessModePanel", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("names the boundary and how to run an agent past it above the choice", () => {
    renderPanel();

    const boundary = screen.getByText(/controls Officer fully, past the token/);
    expect(boundary).toHaveTextContent(
      "with Docker Desktop through docker exec, a copy of the data volume and docker inspect; without Docker through Officer's data folder.",
    );
    expect(boundary).toHaveTextContent(
      "The token and the password keep out web pages, other containers, and agents in a sandbox that cannot reach Docker or Officer's files.",
    );
    expect(screen.getByText("docker exec").tagName).toBe("CODE");
    expect(screen.getByText("docker inspect").tagName).toBe("CODE");
    expect(
      screen.getByText("Run an agent that cannot touch Officer"),
    ).toBeInTheDocument();
    for (const item of [
      "In a sandbox without the Docker socket and without access to Officer's data.",
      "Under a separate user account on this computer.",
      "On another computer.",
    ]) {
      expect(screen.getByText(item)).toBeInTheDocument();
    }
    const choice = screen.getByRole("radiogroup", {
      name: "Who may use Officer",
    });
    expect(
      boundary.compareDocumentPosition(choice) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("recommends token and password over no token", () => {
    renderPanel();

    expect(
      screen.getByRole("radio", { name: "Token and password" }),
    ).toHaveAccessibleDescription(
      "Programs and agents show a token. The panel asks for a password once per browser. Recommended",
    );
    expect(
      screen.getByRole("radio", { name: "No token" }),
    ).toHaveAccessibleDescription(
      "Any software on this computer uses Officer without being asked.",
    );
  });

  it("asks for a new password and says what it means under token and password", () => {
    renderPanel({ mode: "token" });

    expect(
      screen.getByRole("radio", { name: "Token and password" }),
    ).toBeChecked();
    expect(passwordField()).toHaveAttribute("type", "password");
    expect(passwordField()).toHaveAttribute("autocomplete", "new-password");
    expect(repeatField()).toHaveAttribute("type", "password");
    expect(repeatField()).toHaveAttribute("autocomplete", "new-password");
    expect(
      screen.getByText(
        "At least 15 characters. Keep it in your password manager.",
      ),
    ).toBeInTheDocument();
    for (const fact of [
      "The panel asks for it once per browser and keeps you logged in for up to 30 days. A week without visits, or logging out, ends the session.",
      "Your agent gets its own token. It works only over MCP, within the commands you allow.",
      "Your own programs get an API token on Service › Security.",
      "Forgot the password? Officer prints a one-time recovery code to its log.",
    ]) {
      expect(screen.getByText(fact)).toBeInTheDocument();
    }
    expect(screen.queryByText(WARNING_TITLE)).not.toBeInTheDocument();
  });

  it("warns who can use Officer under no token, with no acknowledgement", () => {
    renderPanel({ mode: "open" });

    expect(screen.getByRole("radio", { name: "No token" })).toBeChecked();
    expect(screen.getByText(WARNING_TITLE)).toBeInTheDocument();
    for (const line of [
      "Who can use it:",
      "any program on this computer;",
      "any container on Docker's default network;",
      "any AI agent with a terminal here - it calls the REST API directly and steps around the limits you set for it in MCP.",
      "What they can do: read everything, change and remove limits, unblock accounts, turn on agent commands, delete all data, send orders through your trading connections.",
      "Web pages open in your browser stay locked out in both modes.",
      "To close it, choose Token and password and set a panel password. Officer switches at once, without a restart.",
    ]) {
      expect(screen.getByText(line)).toBeInTheDocument();
    }
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(
      screen.queryByLabelText("New panel password"),
    ).not.toBeInTheDocument();
  });

  it("reports the choice and drops the password when the choice changes", async () => {
    const user = userEvent.setup();
    const { onModeChange, onPasswordChange } = renderPanel({ mode: "token" });
    await user.type(passwordField(), PASSWORD);
    await user.type(repeatField(), PASSWORD);
    expect(onPasswordChange).toHaveBeenLastCalledWith(PASSWORD);

    await user.click(screen.getByRole("radio", { name: "No token" }));

    expect(onModeChange).toHaveBeenLastCalledWith("open");
    expect(onPasswordChange).toHaveBeenLastCalledWith(null);
    expect(screen.getByText(WARNING_TITLE)).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "Token and password" }));

    expect(onModeChange).toHaveBeenLastCalledWith("token");
    expect(passwordField()).toHaveValue("");
    expect(repeatField()).toHaveValue("");
  });

  it("gives the reasons a password cannot be sent as they change", async () => {
    const user = userEvent.setup();
    const { onPasswordChange } = renderPanel();

    await user.type(passwordField(), "short");
    expect(passwordField()).toHaveAccessibleDescription(
      "Too short: 5 of 15 characters.",
    );
    expect(passwordField()).toHaveAttribute("aria-invalid", "true");

    await user.type(passwordField(), "-but-long-now");
    expect(passwordField()).not.toHaveAttribute("aria-invalid");

    await user.type(repeatField(), "short");
    expect(repeatField()).toHaveAccessibleDescription(
      "The passwords do not match.",
    );
    expect(onPasswordChange).toHaveBeenLastCalledWith(null);

    await user.type(repeatField(), "-but-long-now");
    expect(repeatField()).not.toHaveAttribute("aria-invalid");
    expect(onPasswordChange).toHaveBeenLastCalledWith("short-but-long-now");

    await user.type(passwordField(), "!");
    expect(repeatField()).toHaveAccessibleDescription(
      "The passwords do not match.",
    );
    expect(onPasswordChange).toHaveBeenLastCalledWith(null);
  });

  it("refuses a password longer than 256 characters", async () => {
    const user = userEvent.setup();
    const { onPasswordChange } = renderPanel();
    const long = "a".repeat(257);

    await user.click(passwordField());
    await user.paste(long);
    await user.click(repeatField());
    await user.paste(long);

    expect(passwordField()).toHaveAccessibleDescription(
      "Too long: 257 characters, at most 256.",
    );
    expect(onPasswordChange).toHaveBeenLastCalledWith(null);
  });

  it("generates 20 base32 characters in groups of four into both fields and shows them", async () => {
    const user = userEvent.setup();
    const { onPasswordChange } = renderPanel();
    const spy = randomBytes(Array.from({ length: 20 }, (_, i) => i));

    await user.click(screen.getByRole("button", { name: "Generate" }));

    expect(spy).toHaveBeenCalledTimes(1);
    const array = spy.mock.calls[0][0] as Uint8Array;
    expect(array).toBeInstanceOf(Uint8Array);
    expect(array).toHaveLength(20);
    expect(passwordField()).toHaveValue("0123-4567-89AB-CDEF-GHJK");
    expect(repeatField()).toHaveValue("0123-4567-89AB-CDEF-GHJK");
    expect(passwordField()).toHaveAttribute("type", "text");
    expect(repeatField()).toHaveAttribute("type", "text");
    expect(onPasswordChange).toHaveBeenLastCalledWith(
      "0123-4567-89AB-CDEF-GHJK",
    );

    // The top three bits of a byte are dropped: 236 picks the 13th character.
    randomBytes(Array.from({ length: 20 }, (_, i) => 236 + i));
    await user.click(screen.getByRole("button", { name: "Generate" }));

    expect(passwordField()).toHaveValue("CDEF-GHJK-MNPQ-RSTV-WXYZ");
  });

  it("generates a new random password on every click", async () => {
    const user = userEvent.setup();
    renderPanel();
    const spy = vi.spyOn(crypto, "getRandomValues");

    await user.click(screen.getByRole("button", { name: "Generate" }));
    const first = (passwordField() as HTMLInputElement).value;
    await user.click(screen.getByRole("button", { name: "Generate" }));
    const second = (passwordField() as HTMLInputElement).value;

    expect(spy).toHaveBeenCalledTimes(2);
    expect(first).toMatch(GENERATED);
    expect(second).toMatch(GENERATED);
    expect(second).not.toBe(first);
  });

  it("keeps both fields away from spell checking, shown or hidden", async () => {
    const user = userEvent.setup();
    renderPanel();
    const expectVerbatim = () => {
      for (const field of [passwordField(), repeatField()]) {
        expect(field).toHaveAttribute("spellcheck", "false");
        expect(field).toHaveAttribute("autocorrect", "off");
        expect(field).toHaveAttribute("autocapitalize", "off");
      }
    };

    expectVerbatim();
    await user.click(screen.getByRole("button", { name: "Show" }));

    expect(passwordField()).toHaveAttribute("type", "text");
    expectVerbatim();
  });

  it("shows and hides both fields", async () => {
    const user = userEvent.setup();
    renderPanel();

    await user.click(screen.getByRole("button", { name: "Show" }));
    expect(passwordField()).toHaveAttribute("type", "text");
    expect(repeatField()).toHaveAttribute("type", "text");

    await user.click(screen.getByRole("button", { name: "Hide" }));
    expect(passwordField()).toHaveAttribute("type", "password");
    expect(repeatField()).toHaveAttribute("type", "password");
  });

  it("shows the server's refusal on the empty password until it is typed in", async () => {
    const user = userEvent.setup();
    renderPanel({ passwordError: "password must be at least 15 characters" });

    expect(passwordField()).toHaveAccessibleDescription(
      "password must be at least 15 characters",
    );

    await user.type(passwordField(), "a");

    expect(passwordField()).toHaveAccessibleDescription(
      "Too short: 1 of 15 characters.",
    );
  });

  it("disables every control while disabled", () => {
    renderPanel({ disabled: true });

    for (const control of [
      screen.getByRole("radio", { name: "Token and password" }),
      screen.getByRole("radio", { name: "No token" }),
      passwordField(),
      repeatField(),
      screen.getByRole("button", { name: "Generate" }),
      screen.getByRole("button", { name: "Show" }),
    ]) {
      expect(control).toBeDisabled();
    }
  });
});

describe("OpenAccessWarning", () => {
  function renderWarning(disabled = false) {
    const onChange = vi.fn();
    function Controlled() {
      const [acknowledged, setAcknowledged] = useState(false);
      return (
        <OpenAccessWarning
          acknowledgement={{
            acknowledged,
            disabled,
            onChange: (next) => {
              onChange(next);
              setAcknowledged(next);
            },
          }}
        />
      );
    }
    render(<Controlled />);
    return { onChange };
  }

  it("ends in the acknowledgement a caller asks for", async () => {
    const user = userEvent.setup();
    const { onChange } = renderWarning();
    const checkbox = screen.getByRole("checkbox", { name: ACKNOWLEDGEMENT });
    expect(screen.getByText(WARNING_TITLE)).toBeInTheDocument();
    expect(checkbox).not.toBeChecked();

    await user.click(checkbox);
    expect(checkbox).toBeChecked();
    expect(onChange).toHaveBeenLastCalledWith(true);

    await user.click(screen.getByText(ACKNOWLEDGEMENT));
    expect(checkbox).not.toBeChecked();
    expect(onChange).toHaveBeenLastCalledWith(false);
  });

  it("disables the acknowledgement while disabled", () => {
    renderWarning(true);

    expect(
      screen.getByRole("checkbox", { name: ACKNOWLEDGEMENT }),
    ).toBeDisabled();
  });
});
