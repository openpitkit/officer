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

import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  AuditActionGroup,
  AuditEntry,
  PagedResult,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import type { AuditFilter } from "@/framework";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { Audit } from "@/pages/Audit";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useAudit", () => ({
  useAuditPage: vi.fn(),
  useAuditActions: vi.fn(),
}));
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
import { useAuditActions, useAuditPage } from "@/api/useAudit";

const useAuditPageMock = vi.mocked(useAuditPage);
const useAuditActionsMock = vi.mocked(useAuditActions);

const proto = window.HTMLElement.prototype as HTMLElement & {
  hasPointerCapture?: (pointerId: number) => boolean;
  setPointerCapture?: (pointerId: number) => void;
};
proto.hasPointerCapture = () => false;
proto.setPointerCapture = () => {};
proto.scrollIntoView = () => {};

function ready<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

const ACTION_GROUPS: AuditActionGroup[] = [
  { category: "control", actions: ["block", "unblock", "set_limit"] },
  { category: "trading", actions: ["submit_order", "execution_report"] },
];

function auditPage(
  items: AuditEntry[],
  total = items.length,
): PollingResult<PagedResult<AuditEntry>> {
  return ready({ items, total });
}

const sampleEntry: AuditEntry = {
  id: "aud-alpha-000000000001",
  at: "2026-06-24T00:00:00Z",
  actor: "operator",
  actorTitle: "Operator",
  action: "block",
  account: "desk-alpha",
  accountTitle: "Desk Alpha",
  detail: "blocked desk-alpha",
  source: "panel",
};

function lastAuditFilter(): AuditFilter | undefined {
  const calls = useAuditPageMock.mock.calls;
  return calls[calls.length - 1]?.[0];
}

function renderAudit(initialEntry = "/audit") {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <MemoryRouter initialEntries={[initialEntry]}>
            <SidebarProvider>
              <Audit />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  useAuditActionsMock.mockReturnValue(ready<AuditActionGroup[]>(ACTION_GROUPS));
  useAuditPageMock.mockReturnValue(auditPage([sampleEntry]));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("Audit default action filter", () => {
  it("sends the control category on first load", async () => {
    renderAudit();
    await waitFor(() =>
      expect(lastAuditFilter()).toMatchObject({
        category: "control",
        actions: undefined,
      }),
    );
    expect(lastAuditFilter()?.category).toBe("control");
    expect(lastAuditFilter()?.actions).toBeUndefined();
  });

  it("sends an explicit IN once the operator narrows to a subset", async () => {
    renderAudit("/audit?actions=block,unblock");

    await waitFor(() => {
      const actions = lastAuditFilter()?.actions;
      expect(actions).toBeDefined();
      expect(actions).not.toContain("submit_order");
      expect(actions).toContain("block");
    });
  });

  it("restores the all-actions category from the deep link", async () => {
    renderAudit("/audit?category=all");

    await waitFor(() =>
      expect(lastAuditFilter()).toMatchObject({
        category: "all",
        actions: undefined,
      }),
    );
    expect(lastAuditFilter()?.actions).toBeUndefined();
  });

  it("marks the default control category as an active action filter", async () => {
    renderAudit();

    expect(await screen.findByText("Active filters")).toBeInTheDocument();
  });

  it("does not mark show-all event types as an active filter", async () => {
    renderAudit("/audit?category=all");

    await waitFor(() =>
      expect(lastAuditFilter()).toMatchObject({ category: "all" }),
    );
    expect(screen.queryByText("Active filters")).not.toBeInTheDocument();
  });

  it("lets explicit action filters override the category deep link", async () => {
    renderAudit("/audit?category=all&actions=block");

    await waitFor(() => expect(lastAuditFilter()?.actions).toEqual(["block"]));
    expect(lastAuditFilter()?.category).toBeUndefined();
  });
});

describe("Audit pager", () => {
  it("shows total pages and navigates by offset", async () => {
    const user = userEvent.setup();
    useAuditPageMock.mockReturnValue(auditPage([sampleEntry], 100));
    renderAudit();

    await waitFor(() =>
      expect(screen.getAllByText("Page 1 of 2").length).toBeGreaterThan(0),
    );

    const next = screen.getAllByRole("button", { name: "Next" })[0];
    expect(next).toBeEnabled();
    await user.click(next);

    await waitFor(() => expect(lastAuditFilter()?.offset).toBe(50));
  });

  it("hides the pager on a single page", async () => {
    useAuditPageMock.mockReturnValue(auditPage([sampleEntry]));
    renderAudit();

    expect(await screen.findByText("blocked desk-alpha")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Next" }),
    ).not.toBeInTheDocument();
  });
});

describe("Audit share filter set", () => {
  it("copies a deep link that encodes the active filters", async () => {
    const user = userEvent.setup();
    // Install the spy after userEvent.setup so its own clipboard stub does not
    // shadow it; the share button click uses fireEvent for the same reason.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    renderAudit();

    await user.type(
      screen.getByPlaceholderText(/filter by account/i),
      "desk-alpha",
    );
    // fireEvent (not userEvent) so the user-event clipboard stub does not
    // shadow the writeText spy asserted below.
    fireEvent.click(
      screen.getByRole("button", {
        name: /copy a link to the current filter set/i,
      }),
    );

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const url = new URL(writeText.mock.calls[0][0]);
    expect(url.pathname).toBe("/audit");
    expect(url.searchParams.get("account")).toBe("desk-alpha");
    // The default control category stays out of the link.
    expect(url.searchParams.get("sort")).toBeNull();
    expect(url.searchParams.get("order")).toBeNull();
    expect(url.searchParams.get("category")).toBeNull();
    expect(url.searchParams.get("actions")).toBeNull();
  });

  it("restores the filter set from query params on mount", async () => {
    renderAudit("/audit?account=desk-alpha&actor=operator&source=mcp");

    await waitFor(() =>
      expect(lastAuditFilter()).toMatchObject({
        account: "desk-alpha",
        actor: "operator",
        source: "mcp",
      }),
    );
  });

  it("round-trips the all-actions category in share links", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    renderAudit("/audit?category=all");

    await waitFor(() =>
      expect(lastAuditFilter()).toMatchObject({ category: "all" }),
    );
    fireEvent.click(
      screen.getByRole("button", {
        name: /copy a link to the current filter set/i,
      }),
    );

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const url = new URL(writeText.mock.calls[0][0]);
    expect(url.pathname).toBe("/audit");
    expect(url.searchParams.get("category")).toBe("all");
    expect(url.searchParams.get("actions")).toBeNull();
  });
});

describe("Audit empty state", () => {
  it("shows the empty state from an empty first page regardless of total", async () => {
    // Total is 0 for every keyset page now; the empty state must key off the
    // first page having no rows, not the total.
    useAuditPageMock.mockReturnValue(auditPage([]));
    renderAudit();

    expect(await screen.findByText("No audit entries")).toBeInTheDocument();
  });

  it("renders the table when the first page has rows", async () => {
    useAuditPageMock.mockReturnValue(auditPage([sampleEntry]));
    renderAudit();

    expect(await screen.findByText("blocked desk-alpha")).toBeInTheDocument();
    expect(screen.queryByText("No audit entries")).not.toBeInTheDocument();
  });
});
