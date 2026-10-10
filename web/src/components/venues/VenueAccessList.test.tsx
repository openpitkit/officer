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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { TradingAccessGrant } from "@/api/types";
import {
  VenueAccessList,
  type VenueAccessListProps,
} from "@/components/venues";
import { AUTOCOMPLETE_SUGGESTION_LIMIT } from "@/framework";
import i18n from "@/i18n";
import {
  readStoredPreference,
  writeStoredPreference,
} from "@/lib/browserStorage";

const PAGE_SIZE_KEY = "pit-officer-venue-access-page-size";

function account(code: string, group = "", venueAccount = "") {
  return { account: code, accountGroup: "", venueAccount, group };
}

function group(code: string): TradingAccessGrant {
  return { account: "", accountGroup: code, venueAccount: "", group: "" };
}

/** Codes "<prefix>-01" to "<prefix>-<count>". */
function codes(prefix: string, count: number) {
  return Array.from(
    { length: count },
    (_, index) => `${prefix}-${String(index + 1).padStart(2, "0")}`,
  );
}

/** Six groups and 55 accounts: two pages or more under every filter the
 *  paging tests apply, at page size 5 and at 50. */
const PAGED_GROUPS = codes("desk", 6);
const PAGED_ACCOUNTS = codes("acct", 55);
const PAGED_CODES = [...PAGED_GROUPS, ...PAGED_ACCOUNTS];
const PAGED_GRANTS = [
  ...PAGED_GROUPS.map(group),
  ...PAGED_ACCOUNTS.map((code) => account(code)),
];

function renderList(
  props: Partial<VenueAccessListProps> & Pick<VenueAccessListProps, "grants">,
) {
  const onRevoke = vi.fn();
  const list = (next: Partial<VenueAccessListProps>) => (
    <I18nextProvider i18n={i18n}>
      <VenueAccessList
        showVenueAccount={false}
        busy={false}
        onRevoke={onRevoke}
        {...props}
        {...next}
      />
    </I18nextProvider>
  );
  const { rerender } = render(list({}));
  return {
    onRevoke,
    rerender: (next: Partial<VenueAccessListProps>) => rerender(list(next)),
  };
}

function table() {
  return within(screen.getByRole("table", { name: "Access grants" }));
}

const REVOKE_LABEL = /^Revoke access for (account|group) /;

/** Subject codes of the visible rows, in display order. */
function visibleSubjects() {
  return table()
    .queryAllByRole("button", { name: REVOKE_LABEL })
    .map((button) =>
      button.getAttribute("aria-label")!.replace(REVOKE_LABEL, ""),
    );
}

/** The row of a subject named by kind and code, e.g. "group desk". */
function row(subject: string) {
  const revoke = table().getByRole("button", {
    name: `Revoke access for ${subject}`,
  });
  return within(revoke.closest("tr")!);
}

async function nextPage(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getAllByRole("button", { name: "Next" })[0]);
}

beforeAll(() => {
  const proto = window.HTMLElement.prototype as HTMLElement & {
    hasPointerCapture?: (pointerId: number) => boolean;
    setPointerCapture?: (pointerId: number) => void;
    releasePointerCapture?: (pointerId: number) => void;
  };
  proto.hasPointerCapture = () => false;
  proto.setPointerCapture = () => {};
  proto.releasePointerCapture = () => {};
  proto.scrollIntoView = () => {};
});

beforeEach(async () => {
  await i18n.changeLanguage("en");
  // The environment may have no localStorage; the cookie mirror then holds it.
  window.localStorage?.removeItem(PAGE_SIZE_KEY);
  document.cookie = `${PAGE_SIZE_KEY}=; Max-Age=0; Path=/`;
});

describe("VenueAccessList", () => {
  it("counts accounts and groups over the whole list, whatever the filter", async () => {
    const user = userEvent.setup();
    renderList({
      grants: [
        group("desk"),
        group("ops"),
        account("alpha", "desk"),
        account("beta"),
        account("gamma"),
      ],
    });
    expect(screen.getByText("3 accounts · 2 groups")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Groups" }));
    expect(visibleSubjects()).toEqual(["desk", "ops"]);
    expect(screen.getByText("3 accounts · 2 groups")).toBeInTheDocument();
  });

  it("uses the singular counter forms", () => {
    renderList({ grants: [group("desk"), account("alpha")] });
    expect(screen.getByText("1 account · 1 group")).toBeInTheDocument();
  });

  it("marks group rows and shows an account's group, keeping the server order", () => {
    renderList({
      grants: [group("desk"), account("alpha", "desk"), account("beta")],
    });
    expect(
      table()
        .getAllByRole("columnheader")
        .map((header) => header.textContent),
    ).toEqual(["Account or group", "Account group", "Actions"]);
    expect(visibleSubjects()).toEqual(["desk", "alpha", "beta"]);
    const [deskSubject, deskGroup] = row("group desk").getAllByRole("cell");
    expect(within(deskSubject).getByText("Group")).toBeInTheDocument();
    expect(deskGroup).toBeEmptyDOMElement();
    const [alphaSubject, alphaGroup] =
      row("account alpha").getAllByRole("cell");
    expect(within(alphaSubject).queryByText("Group")).not.toBeInTheDocument();
    expect(alphaGroup).toHaveTextContent(/^desk$/);
    const [betaSubject, betaGroup] = row("account beta").getAllByRole("cell");
    expect(within(betaSubject).queryByText("Group")).not.toBeInTheDocument();
    expect(betaGroup).toBeEmptyDOMElement();
  });

  it("adds the venue-account column only when asked", () => {
    renderList({
      grants: [account("alpha", "", "venue-sub")],
      showVenueAccount: true,
    });
    expect(
      table()
        .getAllByRole("columnheader")
        .map((header) => header.textContent),
    ).toEqual([
      "Account or group",
      "Account group",
      "Venue account",
      "Actions",
    ]);
    expect(row("account alpha").getAllByRole("cell")[2]).toHaveTextContent(
      /^venue-sub$/,
    );
  });

  it("filters by kind and by a case-insensitive code fragment", async () => {
    const user = userEvent.setup();
    renderList({
      grants: [
        group("desk-a"),
        group("ops"),
        account("alpha", "desk-a"),
        account("desk-b"),
      ],
    });
    await user.click(screen.getByRole("button", { name: "Accounts" }));
    expect(visibleSubjects()).toEqual(["alpha", "desk-b"]);
    await user.click(screen.getByRole("button", { name: "Groups" }));
    expect(visibleSubjects()).toEqual(["desk-a", "ops"]);
    await user.click(screen.getByRole("button", { name: "All" }));
    await user.type(screen.getByRole("combobox", { name: "Code" }), "ESK");
    expect(visibleSubjects()).toEqual(["desk-a", "desk-b"]);
    await user.click(screen.getByRole("button", { name: "Accounts" }));
    expect(visibleSubjects()).toEqual(["desk-b"]);
    await user.click(screen.getByRole("button", { name: "Clear all filters" }));
    expect(visibleSubjects()).toEqual(["desk-a", "ops", "alpha", "desk-b"]);
    expect(screen.getByRole("combobox", { name: "Code" })).toHaveValue("");
  });

  it("suggests the codes of the chosen kind that start with the typed code", async () => {
    const user = userEvent.setup();
    renderList({
      grants: [
        group("desk-a"),
        group("ops"),
        account("Desk-b", "desk-a"),
        account("mid-desk"),
      ],
    });
    const field = screen.getByRole("combobox", { name: "Code" });
    const suggestions = () =>
      screen.queryAllByRole("option").map((option) => option.textContent);
    await user.type(field, " DE");
    expect(suggestions()).toEqual(["desk-a", "Desk-b"]);
    // The list itself keeps every code containing the typed one.
    expect(visibleSubjects()).toEqual(["desk-a", "Desk-b", "mid-desk"]);
    await user.click(screen.getByRole("button", { name: "Accounts" }));
    // Leaving the field closes its suggestions; focusing it reopens them.
    await waitFor(() => expect(suggestions()).toEqual([]));
    await user.click(field);
    expect(suggestions()).toEqual(["Desk-b"]);
  });

  it("keeps the highlighted suggestion when a poll brings the same grants", async () => {
    const user = userEvent.setup();
    const grants = () => [group("desk-a"), account("desk-b")];
    const { rerender } = renderList({ grants: grants() });
    await user.type(screen.getByRole("combobox", { name: "Code" }), "de");
    await user.keyboard("{ArrowDown}{ArrowDown}");
    const highlighted = () =>
      screen
        .getAllByRole("option")
        .filter((option) => option.getAttribute("aria-selected") === "true")
        .map((option) => option.textContent);
    expect(highlighted()).toEqual(["desk-b"]);
    rerender({ grants: grants() });
    expect(highlighted()).toEqual(["desk-b"]);
  });

  it("caps the code suggestions like every filter", async () => {
    const user = userEvent.setup();
    renderList({ grants: PAGED_GRANTS });
    await user.type(screen.getByRole("combobox", { name: "Code" }), "acct");
    expect(
      screen.getAllByRole("option").map((option) => option.textContent),
    ).toEqual(PAGED_ACCOUNTS.slice(0, AUTOCOMPLETE_SUGGESTION_LIMIT));
  });

  it("tells an empty list apart from one the filters empty", async () => {
    const user = userEvent.setup();
    const { rerender } = renderList({ grants: [] });
    expect(
      table().getByText("No accounts or groups have access"),
    ).toBeInTheDocument();
    expect(screen.getByText("0 accounts · 0 groups")).toBeInTheDocument();
    rerender({ grants: [account("alpha")] });
    await user.type(screen.getByRole("combobox", { name: "Code" }), "zz");
    expect(
      table().getByText("No grants match the filters"),
    ).toBeInTheDocument();
    expect(
      table().queryByText("No accounts or groups have access"),
    ).not.toBeInTheDocument();
  });

  it("pages with the stored page size and shows the last page left after a revoke", async () => {
    const user = userEvent.setup();
    writeStoredPreference(PAGE_SIZE_KEY, "5");
    const grants = [
      group("g1"),
      group("g2"),
      account("a1"),
      account("a2"),
      account("a3"),
      account("a4"),
      account("a5"),
    ];
    const { rerender } = renderList({ grants });
    expect(visibleSubjects()).toEqual(["g1", "g2", "a1", "a2", "a3"]);
    expect(screen.getAllByText("Page 1 of 2")).toHaveLength(2);
    await nextPage(user);
    expect(visibleSubjects()).toEqual(["a4", "a5"]);
    // Revokes shrink the list under the open page: the last page left shows.
    rerender({ grants: grants.slice(0, 5) });
    expect(visibleSubjects()).toEqual(["g1", "g2", "a1", "a2", "a3"]);
  });

  // Each change starts on page 2 and leaves two pages or more, so a list
  // that kept the open page would show page 2's rows.
  it.each<{
    name: string;
    setUp?: (user: ReturnType<typeof userEvent.setup>) => Promise<void>;
    change: (user: ReturnType<typeof userEvent.setup>) => Promise<void>;
    firstPage: string[];
  }>([
    {
      name: "typing a code",
      change: (user) =>
        user.type(screen.getByRole("combobox", { name: "Code" }), "ACCT"),
      firstPage: PAGED_ACCOUNTS.slice(0, 5),
    },
    {
      name: "clearing the code",
      setUp: (user) =>
        user.type(screen.getByRole("combobox", { name: "Code" }), "acct"),
      change: (user) =>
        user.click(screen.getByRole("button", { name: "Clear field" })),
      firstPage: PAGED_CODES.slice(0, 5),
    },
    {
      name: "choosing a kind",
      change: (user) =>
        user.click(screen.getByRole("button", { name: "Accounts" })),
      firstPage: PAGED_ACCOUNTS.slice(0, 5),
    },
    {
      name: "clearing all filters",
      setUp: async (user) => {
        await user.click(screen.getByRole("button", { name: "Accounts" }));
        await user.type(screen.getByRole("combobox", { name: "Code" }), "acct");
      },
      change: (user) =>
        user.click(screen.getByRole("button", { name: "Clear all filters" })),
      firstPage: PAGED_CODES.slice(0, 5),
    },
  ])(
    "returns to the first page on $name",
    async ({ setUp, change, firstPage }) => {
      const user = userEvent.setup();
      writeStoredPreference(PAGE_SIZE_KEY, "5");
      renderList({ grants: PAGED_GRANTS });
      await setUp?.(user);
      await nextPage(user);
      expect(screen.getAllByText(/^Page 2 of /)).toHaveLength(2);
      await change(user);
      expect(visibleSubjects()).toEqual(firstPage);
      expect(screen.getAllByText(/^Page 1 of /)).toHaveLength(2);
    },
  );

  it("stores a chosen page size and returns to the first page", async () => {
    const user = userEvent.setup();
    writeStoredPreference(PAGE_SIZE_KEY, "5");
    renderList({ grants: PAGED_GRANTS });
    await nextPage(user);
    expect(screen.getAllByText(/^Page 2 of /)).toHaveLength(2);
    await user.click(screen.getByRole("combobox", { name: "Page size" }));
    await user.click(await screen.findByRole("option", { name: "50" }));
    expect(visibleSubjects()).toEqual(PAGED_CODES.slice(0, 50));
    expect(screen.getAllByText("Page 1 of 2")).toHaveLength(2);
    expect(readStoredPreference(PAGE_SIZE_KEY)).toBe("50");
  });

  it("asks to revoke with the subject of the row and disables revoking while busy", async () => {
    const user = userEvent.setup();
    const { onRevoke, rerender } = renderList({
      grants: [group("desk"), account("alpha", "desk")],
    });
    await user.click(
      table().getByRole("button", { name: "Revoke access for group desk" }),
    );
    expect(onRevoke).toHaveBeenLastCalledWith({ accountGroup: "desk" });
    await user.click(
      table().getByRole("button", { name: "Revoke access for account alpha" }),
    );
    expect(onRevoke).toHaveBeenLastCalledWith({ account: "alpha" });
    expect(onRevoke).toHaveBeenCalledTimes(2);
    rerender({ busy: true });
    for (const button of table().getAllByRole("button", {
      name: REVOKE_LABEL,
    })) {
      expect(button).toBeDisabled();
    }
  });

  it("keeps an account and a group that share a code apart", async () => {
    const user = userEvent.setup();
    const errorLog = vi.spyOn(console, "error");
    try {
      const { onRevoke } = renderList({
        grants: [group("desk"), account("desk")],
      });
      expect(visibleSubjects()).toEqual(["desk", "desk"]);
      expect(
        within(row("group desk").getAllByRole("cell")[0]).getByText("Group"),
      ).toBeInTheDocument();
      expect(
        within(row("account desk").getAllByRole("cell")[0]).queryByText(
          "Group",
        ),
      ).not.toBeInTheDocument();
      await user.click(
        table().getByRole("button", { name: "Revoke access for group desk" }),
      );
      expect(onRevoke).toHaveBeenLastCalledWith({ accountGroup: "desk" });
      await user.click(
        table().getByRole("button", { name: "Revoke access for account desk" }),
      );
      expect(onRevoke).toHaveBeenLastCalledWith({ account: "desk" });
      await user.click(screen.getByRole("button", { name: "Groups" }));
      expect(visibleSubjects()).toEqual(["desk"]);
      expect(errorLog).not.toHaveBeenCalled();
    } finally {
      errorLog.mockRestore();
    }
  });
});
