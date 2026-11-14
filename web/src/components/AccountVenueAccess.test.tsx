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
import { beforeEach, expect, it, vi } from "vitest";
import type { TradingConnection, TradingSnapshot } from "@/api/types";
import i18n from "@/i18n";
import {
  AccountVenueAccess,
  AccountTradingAccessWarning,
} from "./AccountVenueAccess";

function connection(
  id: string,
  mode: TradingConnection["mode"],
  access: TradingConnection["access"],
): TradingConnection {
  return {
    id,
    mode,
    access,
    enabled: false,
    provider: "alpaca",
    label: id,
    venueIdentity: id,
    state: "stopped",
    diagnostics: [],
    secrets: {},
    instruments: [],
    feeAccount: "",
  };
}
const snapshot: TradingSnapshot = {
  providers: [],
  connections: [
    connection("paper", "paper", [
      { account: "desk", accountGroup: "", venueAccount: "", group: "" },
      { account: "", accountGroup: "group", venueAccount: "", group: "" },
    ]),
    connection("live", "live", [
      { account: "", accountGroup: "group", venueAccount: "", group: "" },
    ]),
    connection("unrelated", "live", [
      { account: "other", accountGroup: "", venueAccount: "", group: "" },
    ]),
  ],
};

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

it("deduplicates own plus inherited access and warns for disabled mixed venues", async () => {
  const onOpen = vi.fn();
  render(
    <I18nextProvider i18n={i18n}>
      <AccountVenueAccess
        account="desk"
        group="group"
        snapshot={snapshot}
        onOpen={onOpen}
      />
    </I18nextProvider>,
  );
  expect(screen.getByText("2")).toBeVisible();
  const warning = screen.getByRole("note");
  expect(warning).toHaveAttribute("tabindex", "0");
  expect(warning).toHaveAttribute("title", warning.getAttribute("aria-label"));
  expect(warning).toHaveAccessibleName(/both paper and live/);
  expect(
    screen.getByLabelText("Includes access inherited from the account group."),
  ).toBeVisible();
  await userEvent
    .setup()
    .click(screen.getByRole("button", { name: "Trading connection access" }));
  expect(onOpen).toHaveBeenCalledOnce();
});

it("keeps unknown access explicit and its dialog action reachable", () => {
  render(
    <I18nextProvider i18n={i18n}>
      <AccountVenueAccess
        account="desk"
        group="group"
        snapshot={null}
        onOpen={vi.fn()}
      />
    </I18nextProvider>,
  );
  expect(screen.getByText("-")).toBeVisible();
  expect(screen.queryByRole("note")).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Trading connection access" }),
  ).toBeEnabled();
});

it("does not classify an account dialog until group membership is known", () => {
  const { rerender } = render(
    <I18nextProvider i18n={i18n}>
      <AccountTradingAccessWarning
        subject={{ account: "desk" }}
        group={null}
        snapshot={snapshot}
      />
    </I18nextProvider>,
  );
  expect(screen.queryByRole("note")).not.toBeInTheDocument();
  rerender(
    <I18nextProvider i18n={i18n}>
      <AccountTradingAccessWarning
        subject={{ account: "desk" }}
        group="group"
        snapshot={snapshot}
      />
    </I18nextProvider>,
  );
  expect(screen.getByRole("note")).toHaveAccessibleName(/both paper and live/);
});
