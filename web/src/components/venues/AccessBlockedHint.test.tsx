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
import { I18nextProvider } from "react-i18next";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { TradingAccessGrant } from "@/api/types";
import {
  AccessBlockedHint,
  accessGrantBlock,
  type AccessBlockedHintProps,
} from "@/components/venues";
import i18n from "@/i18n";

const feeProvider = { unlinkedFees: true };
const plainProvider = { unlinkedFees: false };

function grant(account: string, accountGroup = ""): TradingAccessGrant {
  return { account, accountGroup, venueAccount: "", group: "" };
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("accessGrantBlock", () => {
  it("never blocks a provider without unlinked fees", () => {
    const connection = { feeAccount: "", access: [grant("alpha")] };
    expect(
      accessGrantBlock(plainProvider, connection, { account: "beta" }),
    ).toBeNull();
    expect(
      accessGrantBlock(plainProvider, connection, { accountGroup: "desk" }),
    ).toBeNull();
  });

  it("never blocks a connection that has a fee account", () => {
    const connection = { feeAccount: "fees", access: [grant("alpha")] };
    expect(
      accessGrantBlock(feeProvider, connection, { account: "beta" }),
    ).toBeNull();
    expect(
      accessGrantBlock(feeProvider, connection, { accountGroup: "desk" }),
    ).toBeNull();
  });

  it("always blocks a group while the connection has no fee account", () => {
    expect(
      accessGrantBlock(
        feeProvider,
        { feeAccount: "", access: [] },
        { accountGroup: "desk" },
      ),
    ).toBe("groupNeedsFeeAccount");
    expect(
      accessGrantBlock(
        feeProvider,
        { feeAccount: "", access: [grant("alpha")] },
        { accountGroup: "desk" },
      ),
    ).toBe("groupNeedsFeeAccount");
  });

  it("allows the first account on an empty connection", () => {
    expect(
      accessGrantBlock(
        feeProvider,
        { feeAccount: "", access: [] },
        { account: "alpha" },
      ),
    ).toBeNull();
  });

  it("blocks a second distinct account", () => {
    expect(
      accessGrantBlock(
        feeProvider,
        { feeAccount: "", access: [grant("alpha")] },
        { account: "beta" },
      ),
    ).toBe("secondAccountNeedsFeeAccount");
  });

  it("does not block the same account granted again", () => {
    expect(
      accessGrantBlock(
        feeProvider,
        { feeAccount: "", access: [grant("alpha")] },
        { account: "alpha" },
      ),
    ).toBeNull();
  });

  it("blocks a new account when a group grant is on the connection", () => {
    expect(
      accessGrantBlock(
        feeProvider,
        { feeAccount: "", access: [grant("", "desk")] },
        { account: "alpha" },
      ),
    ).toBe("secondAccountNeedsFeeAccount");
  });
});

function renderHint(props: AccessBlockedHintProps) {
  return render(
    <I18nextProvider i18n={i18n}>
      <AccessBlockedHint {...props} />
    </I18nextProvider>,
  );
}

describe("AccessBlockedHint", () => {
  it("states the second-account reason with the provider title and focuses through the button", async () => {
    const user = userEvent.setup();
    const onSetFeeAccount = vi.fn();
    renderHint({
      reason: "secondAccountNeedsFeeAccount",
      providerTitle: "Alpaca",
      onSetFeeAccount,
    });
    expect(
      screen.getByText(
        "Set a fee account first. Alpaca charges fees that are not tied to orders; with several accounts on one connection Officer needs one account to book them on.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Set fee account" }));
    expect(onSetFeeAccount).toHaveBeenCalledOnce();
  });

  it("states the group reason with the provider title", () => {
    renderHint({
      reason: "groupNeedsFeeAccount",
      providerTitle: "Alpaca",
      onSetFeeAccount: vi.fn(),
    });
    expect(
      screen.getByText(
        "Set a fee account first. A group can hold several accounts, and Alpaca charges fees that are not tied to orders; Officer needs one account to book them on.",
      ),
    ).toBeInTheDocument();
  });

  it("navigates client-side to the fee account page when given an href", async () => {
    render(
      <I18nextProvider i18n={i18n}>
        <MemoryRouter initialEntries={["/accounts"]}>
          <Routes>
            <Route
              path="/accounts"
              element={
                <AccessBlockedHint
                  reason="groupNeedsFeeAccount"
                  providerTitle="Alpaca"
                  feeAccountHref="/trading-connections"
                />
              }
            />
            <Route
              path="/trading-connections"
              element={<p>connections page</p>}
            />
          </Routes>
        </MemoryRouter>
      </I18nextProvider>,
    );
    const link = screen.getByRole("link", { name: "Set fee account" });
    expect(link).toHaveAttribute("href", "/trading-connections");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    // A plain anchor would leave the memory router on /accounts; only a router
    // link reaches the connections route.
    await userEvent.click(link);
    expect(screen.getByText("connections page")).toBeInTheDocument();
  });

  it("takes exactly one action", () => {
    const base = {
      reason: "groupNeedsFeeAccount",
      providerTitle: "Alpaca",
    } as const;
    // @ts-expect-error -- the action is a button or a link, never both
    const both: AccessBlockedHintProps = {
      ...base,
      onSetFeeAccount: () => undefined,
      feeAccountHref: "/trading-connections",
    };
    // @ts-expect-error -- the action is required
    const neither: AccessBlockedHintProps = { ...base };
    // Reading both bindings keeps noUnusedLocals from satisfying the two
    // directives above; only the props type error may satisfy them.
    expect([both, neither]).toHaveLength(2);
  });
});
