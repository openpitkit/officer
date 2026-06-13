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
// Please see https://openpit.dev and the OWNERS file for details.

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, createAccount, setAccountGroup } from "@/api/client";
import i18n from "@/i18n";
import { CreateAccountDialog } from "@/pages/Accounts";

// Keep the real client (ApiError, normalizers) and replace only the two
// mutating calls the dialog makes so no network request is issued.
vi.mock("@/api/client", async () => {
  const actual =
    await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return {
    ...actual,
    createAccount: vi.fn(),
    setAccountGroup: vi.fn(),
  };
});

const createAccountMock = vi.mocked(createAccount);
const setAccountGroupMock = vi.mocked(setAccountGroup);

function renderDialog(onCreated = vi.fn()) {
  render(
    <I18nextProvider i18n={i18n}>
      <CreateAccountDialog groupSuggestions={["equity-desks"]} onCreated={onCreated} />
    </I18nextProvider>,
  );
  return onCreated;
}

beforeEach(async () => {
  vi.clearAllMocks();
  // Pin the locale so assertions match the English catalog regardless of the
  // detector's navigator guess under jsdom.
  await i18n.changeLanguage("en");
});

describe("CreateAccountDialog", () => {
  it("creates an account, closes, and notifies on submit", async () => {
    const user = userEvent.setup();
    createAccountMock.mockResolvedValue({
      id: "acc-aapl-desk",
      blocked: false,
      blockReason: "",
      group: "",
      notes: "",
    });
    const onCreated = renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    expect(
      screen.getByRole("dialog", { name: /create account/i }),
    ).toBeInTheDocument();

    await user.type(screen.getByLabelText(/account id/i), "acc-aapl-desk");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() => {
      expect(createAccountMock).toHaveBeenCalledTimes(1);
    });
    expect(createAccountMock).toHaveBeenCalledWith("acc-aapl-desk");
    expect(setAccountGroupMock).not.toHaveBeenCalled();
    expect(onCreated).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("assigns the typed group after creating the account", async () => {
    const user = userEvent.setup();
    createAccountMock.mockResolvedValue({
      id: "acc-spx",
      blocked: false,
      blockReason: "",
      group: "equity-desks",
      notes: "",
    });
    setAccountGroupMock.mockResolvedValue({
      id: "acc-spx",
      blocked: false,
      blockReason: "",
      group: "equity-desks",
      notes: "",
    });
    renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    await user.type(screen.getByLabelText(/account id/i), "acc-spx");
    await user.type(screen.getByLabelText(/group/i), "equity-desks");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    await waitFor(() => {
      expect(createAccountMock).toHaveBeenCalledWith("acc-spx");
    });
    expect(setAccountGroupMock).toHaveBeenCalledTimes(1);
    expect(setAccountGroupMock).toHaveBeenCalledWith("acc-spx", "equity-desks");
  });

  it("shows an inline validation message and does not submit an invalid id", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    // 65 chars exceeds the 64-char cap that validateAccountID enforces.
    await user.type(screen.getByLabelText(/account id/i), "a".repeat(65));

    expect(
      await screen.findByText(/at most 64 characters/i),
    ).toBeInTheDocument();
    // The submit button is disabled while the id is invalid, so a click is a
    // no-op; assert the client was never called regardless.
    await user.click(screen.getByRole("button", { name: /^create$/i }));
    expect(createAccountMock).not.toHaveBeenCalled();
  });

  it("surfaces a client error and keeps the dialog open", async () => {
    const user = userEvent.setup();
    createAccountMock.mockRejectedValue(
      new ApiError("account already exists", "conflict", 409),
    );
    renderDialog();

    await user.click(screen.getByRole("button", { name: /new account/i }));
    await user.type(screen.getByLabelText(/account id/i), "acc-dupe");
    await user.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /account already exists/i,
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
