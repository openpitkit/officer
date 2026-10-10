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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { TradingAccessGrant } from "@/api/types";
import {
  VenueAccessDialog,
  type VenueAccessDialogEditableRow,
  type VenueAccessDialogProps,
} from "@/components/venues";
import i18n from "@/i18n";

/** Account alpha in group desk. */
const ACCOUNT = { subject: { account: "alpha" }, group: "desk" };
const GROUP = { subject: { accountGroup: "desk" }, group: null };

function accountGrant(venueAccount = ""): TradingAccessGrant {
  return { account: "alpha", accountGroup: "", venueAccount, group: "desk" };
}

function groupGrant(venueAccount = ""): TradingAccessGrant {
  return { account: "", accountGroup: "desk", venueAccount, group: "" };
}

function row(
  id: string,
  fields: Partial<VenueAccessDialogEditableRow> = {},
): VenueAccessDialogEditableRow {
  return {
    id,
    label: `${id} label`,
    providerType: "alpaca",
    providerTitle: "Alpaca",
    modeLabel: "Paper",
    ownGrant: null,
    groupGrant: null,
    grantBlock: null,
    ...fields,
  };
}

function renderDialog(
  props: Partial<VenueAccessDialogProps> &
    Pick<VenueAccessDialogProps, "subject" | "group" | "connections">,
) {
  const handlers = {
    onOpenChange: vi.fn(),
    onGrant: vi.fn(),
    onRevoke: vi.fn(),
  };
  const dialog = (next: Partial<VenueAccessDialogProps>) => (
    <I18nextProvider i18n={i18n}>
      <MemoryRouter initialEntries={["/accounts"]}>
        <Routes>
          <Route
            path="/accounts"
            element={
              <VenueAccessDialog
                open
                connectionsHref="/trading-connections"
                busy={false}
                {...handlers}
                {...props}
                {...next}
              />
            }
          />
          <Route
            path="/trading-connections"
            element={<p>connections page</p>}
          />
        </Routes>
      </MemoryRouter>
    </I18nextProvider>
  );
  const { rerender } = render(dialog({}));
  return {
    ...handlers,
    rerender: (next: Partial<VenueAccessDialogProps>) => rerender(dialog(next)),
  };
}

/** The table row of the connection with the given label. */
function connectionRow(label: string) {
  return within(screen.getByText(label).closest("tr")!);
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("VenueAccessDialog", () => {
  it("names the account and tells its own, group and missing access apart", () => {
    renderDialog({
      ...ACCOUNT,
      connections: [
        row("own", { ownGrant: accountGrant() }),
        row("group", { groupGrant: groupGrant() }),
        row("both", { ownGrant: accountGrant(), groupGrant: groupGrant() }),
        row("none"),
      ],
    });
    expect(
      screen.getByRole("dialog", {
        name: "Trading connection access - alpha",
      }),
    ).toBeInTheDocument();
    const own = connectionRow("own label");
    expect(own.getByText("Own grant")).toBeInTheDocument();
    expect(own.queryByText(/^Venue account:/)).not.toBeInTheDocument();
    expect(own.getByText("Paper")).toBeInTheDocument();
    expect(own.getByRole("button", { name: "Revoke access" })).toBeEnabled();
    const group = connectionRow("group label");
    expect(group.getByText("Through group desk")).toBeInTheDocument();
    expect(group.getByRole("button", { name: "Grant access" })).toBeEnabled();
    const both = connectionRow("both label");
    expect(
      both.getByText("Own grant, also through group desk"),
    ).toBeInTheDocument();
    expect(both.queryByText(/^Venue account:/)).not.toBeInTheDocument();
    expect(both.getByRole("button", { name: "Revoke access" })).toBeEnabled();
    const none = connectionRow("none label");
    expect(none.getByText("No access")).toBeInTheDocument();
    expect(none.getByRole("button", { name: "Grant access" })).toBeEnabled();
  });

  it("names the group and reads only its own grant", () => {
    renderDialog({
      ...GROUP,
      connections: [
        row("granted", { ownGrant: groupGrant() }),
        // A group subject has no group of its own; the grant is not read.
        row("none", { groupGrant: groupGrant("sub-9") }),
      ],
    });
    expect(
      screen.getByRole("dialog", {
        name: "Trading connection access - group desk",
      }),
    ).toBeInTheDocument();
    const granted = connectionRow("granted label");
    expect(granted.getByText("Granted")).toBeInTheDocument();
    expect(
      granted.getByRole("button", { name: "Revoke access" }),
    ).toBeInTheDocument();
    const none = connectionRow("none label");
    expect(none.getByText("No access")).toBeInTheDocument();
    expect(none.queryByText(/through group/i)).not.toBeInTheDocument();
    // Nor does the group-venue-account guard apply to a group.
    expect(none.getByRole("button", { name: "Grant access" })).toBeEnabled();
    expect(
      none.queryByText(/would replace the group's venue account/),
    ).not.toBeInTheDocument();
    expect(none.queryByRole("link")).not.toBeInTheDocument();
  });

  it("claims no group access and holds granting while the account's group is unknown", () => {
    renderDialog({
      subject: { account: "alpha" },
      group: null,
      connections: [
        row("own", { ownGrant: accountGrant() }),
        // A group grant is not read while the account's group is unknown.
        row("none", { groupGrant: groupGrant("sub-9") }),
      ],
    });
    const own = connectionRow("own label");
    expect(own.getByText("Own grant")).toBeInTheDocument();
    expect(own.getByRole("button", { name: "Revoke access" })).toBeEnabled();
    const none = connectionRow("none label");
    expect(none.getByText("No own grant")).toBeInTheDocument();
    expect(none.queryByText("No access")).not.toBeInTheDocument();
    expect(none.queryByText(/through group/i)).not.toBeInTheDocument();
    expect(
      none.queryByText(/would replace the group's venue account/),
    ).not.toBeInTheDocument();
    expect(none.queryByRole("link")).not.toBeInTheDocument();
    // An own grant may replace a group's venue account nobody can see yet.
    expect(none.getByRole("button", { name: "Grant access" })).toBeDisabled();
  });

  it("grants and revokes with the connection id", async () => {
    const user = userEvent.setup();
    const { onGrant, onRevoke } = renderDialog({
      ...ACCOUNT,
      connections: [
        row("conn-own", { ownGrant: accountGrant(), groupGrant: groupGrant() }),
        row("conn-none"),
      ],
    });
    await user.click(
      connectionRow("conn-none label").getByRole("button", {
        name: "Grant access",
      }),
    );
    expect(onGrant).toHaveBeenCalledExactlyOnceWith("conn-none");
    await user.click(
      connectionRow("conn-own label").getByRole("button", {
        name: "Revoke access",
      }),
    );
    expect(onRevoke).toHaveBeenCalledExactlyOnceWith("conn-own");
  });

  it("disables a refused grant and links its reason to the fee account page", async () => {
    const user = userEvent.setup();
    const { onGrant } = renderDialog({
      ...GROUP,
      connections: [
        row("blocked", { grantBlock: "groupNeedsFeeAccount" }),
        row("open"),
        // A held grant can only be revoked; a stale reason is not shown.
        row("held", {
          ownGrant: groupGrant(),
          grantBlock: "groupNeedsFeeAccount",
        }),
      ],
    });
    const blocked = connectionRow("blocked label");
    const grant = blocked.getByRole("button", { name: "Grant access" });
    expect(grant).toBeDisabled();
    await user.click(grant);
    expect(onGrant).not.toHaveBeenCalled();
    expect(
      blocked.getByText(
        "Set a fee account first. A group can hold several accounts, and Alpaca charges fees that are not tied to orders; Officer needs one account to book them on.",
      ),
    ).toBeInTheDocument();
    expect(
      connectionRow("open label").queryByRole("link"),
    ).not.toBeInTheDocument();
    expect(
      connectionRow("held label").queryByRole("link"),
    ).not.toBeInTheDocument();
    const link = blocked.getByRole("link", { name: "Set fee account" });
    expect(link).toHaveAttribute("href", "/trading-connections");
    await user.click(link);
    expect(screen.getByText("connections page")).toBeInTheDocument();
  });

  it("leaves an own grant bound to a venue account to the connection page", async () => {
    const user = userEvent.setup();
    renderDialog({
      ...ACCOUNT,
      connections: [row("bound", { ownGrant: accountGrant("sub-1") })],
    });
    const bound = connectionRow("bound label");
    expect(bound.getByText("Own grant")).toBeInTheDocument();
    expect(bound.getByText("Venue account: sub-1")).toBeInTheDocument();
    expect(
      bound.getByText(
        "Revoking here would drop the venue account. Change this grant on the trading connections page.",
      ),
    ).toBeInTheDocument();
    expect(bound.queryByRole("button")).not.toBeInTheDocument();
    const link = bound.getByRole("link", { name: "Open trading connections" });
    expect(link).toHaveAttribute("href", "/trading-connections");
    await user.click(link);
    expect(screen.getByText("connections page")).toBeInTheDocument();
  });

  it("refuses an own grant over the group's venue account", async () => {
    const user = userEvent.setup();
    const { onGrant } = renderDialog({
      ...ACCOUNT,
      connections: [row("bound", { groupGrant: groupGrant("sub-9") })],
    });
    const bound = connectionRow("bound label");
    expect(bound.getByText("Through group desk")).toBeInTheDocument();
    expect(
      bound.getByText(
        "An own grant would replace the group's venue account sub-9. Grant it on the trading connections page.",
      ),
    ).toBeInTheDocument();
    const grant = bound.getByRole("button", { name: "Grant access" });
    expect(grant).toBeDisabled();
    await user.click(grant);
    expect(onGrant).not.toHaveBeenCalled();
    const link = bound.getByRole("link", { name: "Open trading connections" });
    expect(link).toHaveAttribute("href", "/trading-connections");
    await user.click(link);
    expect(screen.getByText("connections page")).toBeInTheDocument();
  });

  it("shows why a connection cannot be edited and keeps the other rows", () => {
    renderDialog({
      ...ACCOUNT,
      connections: [
        {
          id: "lost",
          label: "lost label",
          providerType: "gone",
          modeLabel: "Paper",
          unavailable: "Provider gone is missing from the trading snapshot.",
        },
        row("none"),
      ],
    });
    const lost = connectionRow("lost label");
    expect(lost.getByRole("alert")).toHaveTextContent(
      "Provider gone is missing from the trading snapshot.",
    );
    expect(lost.queryByRole("button")).not.toBeInTheDocument();
    expect(
      connectionRow("none label").getByRole("button", { name: "Grant access" }),
    ).toBeEnabled();
  });

  it("links an empty list to trading connection setup", async () => {
    const user = userEvent.setup();
    renderDialog({ ...ACCOUNT, connections: [] });
    expect(screen.getByText("No trading connections")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    const link = screen.getByRole("link", {
      name: "Set up a trading connection",
    });
    expect(link).toHaveAttribute("href", "/trading-connections");
    await user.click(link);
    expect(screen.getByText("connections page")).toBeInTheDocument();
  });

  it("shows loading, not an empty list, while connections are unknown", () => {
    const { rerender } = renderDialog({ ...ACCOUNT, connections: null });
    expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
    expect(
      screen.queryByText("No trading connections"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    rerender({ error: "trading unavailable" });
    expect(screen.getByRole("alert")).toHaveTextContent("trading unavailable");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(
      screen.queryByText("No trading connections"),
    ).not.toBeInTheDocument();
  });

  it("shows the caller's error and notices next to the rows", () => {
    renderDialog({
      ...ACCOUNT,
      connections: [row("none")],
      error: "fee account required",
      children: <p>rows are stale</p>,
    });
    expect(screen.getByRole("alert")).toHaveTextContent("fee account required");
    expect(screen.getByText("rows are stale")).toBeVisible();
    expect(connectionRow("none label").getByText("No access")).toBeVisible();
  });

  it("locks every change and closing while busy", async () => {
    const user = userEvent.setup();
    const { onOpenChange, rerender } = renderDialog({
      ...ACCOUNT,
      connections: [row("own", { ownGrant: accountGrant() }), row("none")],
    });
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false);
    onOpenChange.mockClear();
    rerender({ busy: true });
    expect(
      connectionRow("own label").getByRole("button", { name: "Revoke access" }),
    ).toBeDisabled();
    expect(
      connectionRow("none label").getByRole("button", { name: "Grant access" }),
    ).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Close" }));
    await user.keyboard("{Escape}");
    expect(onOpenChange).not.toHaveBeenCalled();
  });
});
