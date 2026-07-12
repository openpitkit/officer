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

import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Limit, PolicyListFilters } from "@/api/types";
import { useLimitsPage } from "@/api/useLimits";
import type { PollingResult } from "@/api/usePolling";
import { SidebarProvider } from "@/components/SidebarContext";
import { renderWithApi as render } from "@/test/apiClient";
import i18n from "@/i18n";
import { Limits } from "@/pages/Limits";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useLimits", () => ({ useLimitsPage: vi.fn() }));
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
vi.mock("@/components/ui/select", async () => {
  const React = await import("react");
  interface SelectContextValue {
    value: string;
    onValueChange: (value: string) => void;
    open: boolean;
    toggleOpen: () => void;
    close: () => void;
  }

  const SelectContext = React.createContext<SelectContextValue>({
    value: "",
    onValueChange: (value) => {
      void value;
    },
    open: false,
    toggleOpen: () => {},
    close: () => {},
  });

  function Select({
    value,
    onValueChange,
    children,
  }: {
    value: string;
    onValueChange: (value: string) => void;
    children?: ReactNode;
  }) {
    const [open, setOpen] = React.useState(false);
    return (
      <SelectContext.Provider
        value={{
          value,
          onValueChange,
          open,
          toggleOpen: () => setOpen((current) => !current),
          close: () => setOpen(false),
        }}
      >
        {children}
      </SelectContext.Provider>
    );
  }

  function SelectTrigger({
    children,
    onClick,
    ...props
  }: ButtonHTMLAttributes<HTMLButtonElement>) {
    const { toggleOpen } = React.useContext(SelectContext);
    return (
      <button
        type="button"
        role="combobox"
        {...props}
        onClick={(event) => {
          onClick?.(event);
          toggleOpen();
        }}
      >
        {children}
      </button>
    );
  }

  function SelectValue() {
    return <span>{React.useContext(SelectContext).value}</span>;
  }

  function SelectContent({ children }: { children?: ReactNode }) {
    return React.useContext(SelectContext).open ? (
      <div role="listbox">{children}</div>
    ) : null;
  }

  function SelectItem({
    value,
    children,
    ...props
  }: ButtonHTMLAttributes<HTMLButtonElement> & { value: string }) {
    const { close, onValueChange } = React.useContext(SelectContext);
    return (
      <button
        type="button"
        role="option"
        {...props}
        onClick={() => {
          onValueChange(value);
          close();
        }}
      >
        {children}
      </button>
    );
  }

  return { Select, SelectContent, SelectItem, SelectTrigger, SelectValue };
});

const useLimitsMock = vi.mocked(useLimitsPage);
const fetchAccountsMock = vi.fn();
const fetchAssetsMock = vi.fn();
const fetchGroupsMock = vi.fn();
const putLimitMock = vi.fn();

function readyPage(items: Limit[] = []): PollingResult<{
  items: Limit[];
  total: number;
}> {
  return {
    load: { state: "ready", data: { items, total: items.length }, error: null },
    reload: vi.fn(),
  };
}

function lastPolicyFilters(): PolicyListFilters | undefined {
  const calls = useLimitsMock.mock.calls;
  return calls[calls.length - 1]?.[0];
}

function latestPolicyFiltersMatching(
  predicate: (filters: PolicyListFilters | undefined) => boolean,
): PolicyListFilters | undefined {
  for (const [filters] of [...useLimitsMock.mock.calls].reverse()) {
    if (predicate(filters)) {
      return filters;
    }
  }
  return undefined;
}

function renderLimits(
  initialEntry = "/policies",
  options: { includeGroupsApi?: boolean; putLimit?: typeof putLimitMock } = {},
) {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <MemoryRouter initialEntries={[initialEntry]}>
            <SidebarProvider>
              <Limits />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        fetchAccounts: fetchAccountsMock,
        fetchAssets: fetchAssetsMock,
        putLimit: options.putLimit ?? putLimitMock,
        ...(options.includeGroupsApi ? { fetchGroups: fetchGroupsMock } : {}),
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  useLimitsMock.mockReturnValue(readyPage());
  fetchAccountsMock.mockResolvedValue([]);
  fetchAssetsMock.mockResolvedValue([]);
  fetchGroupsMock.mockResolvedValue([]);
  putLimitMock.mockResolvedValue(undefined);
});

describe("Limits identity filters", () => {
  it("renders the self-computed PnL policy", () => {
    useLimitsMock.mockReturnValue(
      readyPage([
        {
          policy: "spot_funds_pnl_bounds_kill_switch",
          scope: "account_group",
          account: "",
          accountGroup: "desk-a",
          asset: "",
          values: { lower_bound: "-1000" },
        },
      ]),
    );

    renderLimits();

    expect(screen.getByText("PnL Kill Switch")).toBeInTheDocument();
    expect(screen.getByText("desk-a")).toBeInTheDocument();
  });

  it("accepts the self-computed PnL policy filter from the URL", () => {
    renderLimits("/policies?policy=spot_funds_pnl_bounds");

    expect(
      latestPolicyFiltersMatching(
        (filters) => filters?.policy === "spot_funds_pnl_bounds",
      ),
    ).toEqual(
      expect.objectContaining({ policy: "spot_funds_pnl_bounds" }),
    );
  });

  it("accepts the SpotFunds account-group filter from the URL", () => {
    renderLimits(
      "/policies?policy=spot_funds_pnl_bounds&accountGroup=desk-a",
    );

    expect(
      latestPolicyFiltersMatching(
        (filters) =>
          filters?.policy === "spot_funds_pnl_bounds" &&
          filters.accountGroup === "desk-a",
      ),
    ).toEqual(
      expect.objectContaining({
        policy: "spot_funds_pnl_bounds",
        accountGroup: "desk-a",
      }),
    );
  });

  it("loads group suggestions", async () => {
    renderLimits("/policies", { includeGroupsApi: true });

    await waitFor(() => expect(fetchGroupsMock).toHaveBeenCalled());
  });

  it("applies account and asset filters when Enter is pressed", async () => {
    const user = userEvent.setup();
    renderLimits();

    await user.type(screen.getByLabelText("Filter by account"), "desk-alpha");
    await user.type(screen.getByLabelText("Filter by asset"), "AAPL");
    expect(lastPolicyFilters()).not.toEqual(
      expect.objectContaining({ account: "desk-alpha", asset: "AAPL" }),
    );

    await user.keyboard("{Enter}");

    await waitFor(() =>
      expect(
        latestPolicyFiltersMatching(
          (filters) =>
            filters?.account === "desk-alpha" && filters.asset === "AAPL",
        ),
      ).toEqual(
        expect.objectContaining({
          account: "desk-alpha",
          asset: "AAPL",
        }),
      ),
    );
  });

  it("applies an asset suggestion when it is selected", async () => {
    const user = userEvent.setup();
    fetchAssetsMock.mockResolvedValue([{ code: "AAPL" }]);
    renderLimits();

    await user.type(screen.getByLabelText("Filter by asset"), "AA");
    await waitFor(() => expect(fetchAssetsMock).toHaveBeenCalled());
    await user.click(await screen.findByRole("option", { name: "AAPL" }));

    await waitFor(() =>
      expect(
        latestPolicyFiltersMatching((filters) => filters?.asset === "AAPL"),
      ).toEqual(
        expect.objectContaining({
          asset: "AAPL",
        }),
      ),
    );
  });

  it("keeps the account group in the shared filter link", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    renderLimits(
      "/policies?policy=spot_funds_pnl_bounds&account=acc-1" +
        "&accountGroup=desk-a&asset=BTC",
    );

    fireEvent.click(
      screen.getByRole("button", {
        name: "Copy a link to the current filter set",
      }),
      { ctrlKey: true },
    );

    expect(open).toHaveBeenCalledTimes(1);
    const shared = new URL(open.mock.calls[0][0] as string);
    expect(shared.pathname).toBe("/policies");
    expect(shared.searchParams.get("account")).toBe("acc-1");
    expect(shared.searchParams.get("accountGroup")).toBe("desk-a");
    expect(shared.searchParams.get("asset")).toBe("BTC");
    expect(shared.searchParams.get("policy")).toBe("spot_funds_pnl_bounds");
  });
});

describe("LimitDialog framework controls", () => {
  it("uses clearable numeric steppers and pre-searches account and asset fields", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValue([
      { code: "acc-main", effectiveCurrency: "USD" },
    ]);
    fetchAssetsMock.mockResolvedValue([{ code: "AAPL" }]);
    renderLimits();

    await user.click(
      screen.getAllByRole("button", { name: "Add policy" })[0],
    );
    const dialog = screen.getByRole("dialog");
    const maxOrders = within(dialog).getByLabelText("max orders");
    const maxOrdersControls = within(maxOrders.parentElement as HTMLElement);

    expect(maxOrders).toHaveAttribute("inputmode", "decimal");
    expect(
      maxOrdersControls.getByRole("button", { name: "Increase" }),
    ).toBeInTheDocument();
    expect(
      maxOrdersControls.getByRole("button", { name: "Decrease" }),
    ).toBeInTheDocument();

    await user.type(maxOrders, "5");
    await user.click(
      maxOrdersControls.getByRole("button", { name: "Clear field" }),
    );
    expect(maxOrders).toHaveValue("");

    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "Order size limit" }),
    );
    await user.click(within(dialog).getAllByRole("combobox")[1]);
    await user.click(
      within(dialog).getByRole("option", { name: "Account + asset" }),
    );

    const account = within(dialog).getByLabelText("Account");
    await user.type(account, "acc");
    await waitFor(() =>
      expect(fetchAccountsMock).toHaveBeenCalledWith(
        expect.objectContaining({ code: "acc" }),
        expect.any(AbortSignal),
      ),
    );
    await user.click(await screen.findByRole("option", { name: "acc-main" }));
    expect(
      within(account.parentElement as HTMLElement).getByRole("button", {
        name: "Clear field",
      }),
    ).toBeInTheDocument();

    const asset = within(dialog).getByLabelText("Asset");
    await user.type(asset, "AA");
    await waitFor(() =>
      expect(fetchAssetsMock).toHaveBeenCalledWith(
        expect.objectContaining({ code: "AA" }),
        expect.any(AbortSignal),
      ),
    );
    await user.click(await screen.findByRole("option", { name: "AAPL" }));
    expect(
      within(asset.parentElement as HTMLElement).getByRole("button", {
        name: "Clear field",
      }),
    ).toBeInTheDocument();
  });

  it("pre-searches and clears account groups", async () => {
    const user = userEvent.setup();
    fetchGroupsMock.mockResolvedValue([{ code: "desk-alpha" }]);
    renderLimits("/policies", { includeGroupsApi: true });

    await user.click(
      screen.getAllByRole("button", { name: "Add policy" })[0],
    );
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "PnL Kill Switch" }),
    );
    await user.click(within(dialog).getAllByRole("combobox")[1]);
    await user.click(
      within(dialog).getByRole("option", { name: "Account group" }),
    );

    const accountGroup = within(dialog).getByLabelText("Account group");
    await user.type(accountGroup, "desk");
    await waitFor(() =>
      expect(fetchGroupsMock).toHaveBeenCalledWith(
        expect.objectContaining({ code: "desk" }),
        expect.any(AbortSignal),
      ),
    );
    await user.click(await screen.findByRole("option", { name: "desk-alpha" }));
    await user.click(
      within(accountGroup.parentElement as HTMLElement).getByRole("button", {
        name: "Clear field",
      }),
    );
    expect(accountGroup).toHaveValue("");
  });

  it("omits the account currency from the PnL payload", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValue([
      { code: "acc-usd", effectiveCurrency: "USD" },
    ]);
    renderLimits();

    await user.click(
      screen.getAllByRole("button", { name: "Add policy" })[0],
    );
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "PnL Kill Switch" }),
    );
    await user.click(within(dialog).getAllByRole("combobox")[1]);
    await user.click(within(dialog).getByRole("option", { name: "Account" }));

    const account = within(dialog).getByLabelText("Account");
    await user.type(account, "acc");
    await waitFor(() =>
      expect(fetchAccountsMock).toHaveBeenCalledWith(
        expect.objectContaining({ code: "acc" }),
        expect.any(AbortSignal),
      ),
    );
    await user.click(await screen.findByRole("option", { name: "acc-usd" }));

    await user.type(within(dialog).getByLabelText("lower bound"), "-100");

    await user.click(within(dialog).getByRole("button", { name: "Add policy" }));
    expect(
      screen.queryByRole("button", { name: "Rebuild and apply" }),
    ).not.toBeInTheDocument();
    await waitFor(() =>
      expect(putLimitMock).toHaveBeenCalledWith({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account",
        account: "acc-usd",
        accountGroup: "",
        asset: "",
        values: { lower_bound: "-100" },
      }),
    );
  });
});
