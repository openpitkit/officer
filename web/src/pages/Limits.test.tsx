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
import { shareLinkParams, skeletonColumns } from "@/test/pageHelpers";
import { ApiError } from "@/framework";
import i18n from "@/i18n";
import { setAccountScope } from "@/lib/accountScope";
import { LimitDialog } from "@/pages/LimitDialog";
import { Limits } from "@/pages/Limits";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useLimits", () => ({ useLimitsPage: vi.fn() }));
// SensitiveDataBanner polls GET /credential-storage.
vi.mock("@/api/useCredentialStorage", () => ({
  useCredentialStorage: () => ({
    load: {
      state: "ready",
      data: {
        credentialsSealed: true,
        plaintextTradingKeys: false,
        plaintextMarketDataKeys: false,
      },
      error: null,
    },
    reload: () => {},
  }),
}));
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
    disabled: boolean;
    open: boolean;
    toggleOpen: () => void;
    close: () => void;
  }

  const SelectContext = React.createContext<SelectContextValue>({
    value: "",
    onValueChange: (value) => {
      void value;
    },
    disabled: false,
    open: false,
    toggleOpen: () => {},
    close: () => {},
  });

  function Select({
    value,
    onValueChange,
    disabled = false,
    children,
  }: {
    value: string;
    onValueChange: (value: string) => void;
    disabled?: boolean;
    children?: ReactNode;
  }) {
    const [open, setOpen] = React.useState(false);
    return (
      <SelectContext.Provider
        value={{
          value,
          onValueChange,
          disabled,
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
    const { disabled, toggleOpen } = React.useContext(SelectContext);
    return (
      <button
        type="button"
        role="combobox"
        {...props}
        disabled={disabled || props.disabled}
        onClick={(event) => {
          onClick?.(event);
          if (!disabled) {
            toggleOpen();
          }
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
const deleteLimitMock = vi.fn();

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
      <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-test-density"
          tradeStyleStorageKey="pit-test-style"
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
        deleteLimit: deleteLimitMock,
        ...(options.includeGroupsApi ? { fetchGroups: fetchGroupsMock } : {}),
      },
    },
  );
}

function renderLimitDialog(editing: Limit | null) {
  render(
    <I18nextProvider i18n={i18n}>
      <LimitDialog
        open
        editing={editing}
        policyCounts={{ order_size_limit: 1 }}
        onOpenChange={() => {}}
        onSaved={() => {}}
      />
    </I18nextProvider>,
    {
      api: {
        fetchAccounts: fetchAccountsMock,
        fetchAssets: fetchAssetsMock,
        putLimit: putLimitMock,
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
  deleteLimitMock.mockResolvedValue(undefined);
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
          values: { currency: "USD", lower_bound: "-1000" },
        },
      ]),
    );

    renderLimits();

    expect(screen.getByText("PnL Kill Switch")).toBeInTheDocument();
    expect(screen.getByText("desk-a")).toBeInTheDocument();
    expect(screen.getByText("USD")).toBeInTheDocument();
  });

  it("accepts the self-computed PnL policy filter from the URL", () => {
    renderLimits("/policies?policy=spot_funds_pnl_bounds");

    expect(
      latestPolicyFiltersMatching(
        (filters) => filters?.policy === "spot_funds_pnl_bounds",
      ),
    ).toEqual(expect.objectContaining({ policy: "spot_funds_pnl_bounds" }));
  });

  it("accepts the SpotFunds account-group filter from the URL", () => {
    renderLimits("/policies?policy=spot_funds_pnl_bounds&accountGroup=desk-a");

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
  it("renders and submits only the quantity cap for an underlying-asset scope", async () => {
    const user = userEvent.setup();
    renderLimitDialog(null);

    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "Order size limit" }),
    );

    const maxQuantity = within(dialog).getByLabelText("max quantity");
    const maxNotional = within(dialog).getByLabelText("max notional");
    await user.type(maxQuantity, "5");
    await user.type(maxNotional, "1000");

    await user.click(within(dialog).getAllByRole("combobox")[1]);
    await user.click(
      within(dialog).getByRole("option", { name: "Underlying asset" }),
    );

    expect(within(dialog).getByLabelText("max quantity")).toHaveValue("5");
    expect(within(dialog).queryByLabelText("max notional")).toBeNull();
    await user.type(within(dialog).getByLabelText("Asset"), "AAPL");
    await user.click(
      within(dialog).getByRole("button", { name: "Add policy" }),
    );

    await waitFor(() =>
      expect(putLimitMock).toHaveBeenCalledWith(
        {
          policy: "order_size_limit",
          scope: "underlying_asset",
          account: "",
          accountGroup: "",
          asset: "AAPL",
          values: { max_quantity: "5" },
        },
        "reject",
      ),
    );
  });

  it("restores a hidden order-size cap after returning to broker scope", async () => {
    const user = userEvent.setup();
    renderLimitDialog(null);

    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "Order size limit" }),
    );
    await user.type(within(dialog).getByLabelText("max quantity"), "5");
    await user.type(within(dialog).getByLabelText("max notional"), "1000");

    await user.click(within(dialog).getAllByRole("combobox")[1]);
    await user.click(
      within(dialog).getByRole("option", { name: "Underlying asset" }),
    );

    expect(within(dialog).queryByLabelText("max notional")).toBeNull();

    await user.click(within(dialog).getAllByRole("combobox")[1]);
    await user.click(within(dialog).getByRole("option", { name: "Broker" }));

    expect(within(dialog).getByLabelText("max notional")).toHaveValue("1000");
  });

  it("renders and submits only the notional cap for a settlement-asset scope", async () => {
    const user = userEvent.setup();
    renderLimitDialog(null);

    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "Order size limit" }),
    );
    await user.type(within(dialog).getByLabelText("max quantity"), "5");
    await user.type(within(dialog).getByLabelText("max notional"), "1000");

    await user.click(within(dialog).getAllByRole("combobox")[1]);
    await user.click(
      within(dialog).getByRole("option", { name: "Settlement asset" }),
    );

    expect(within(dialog).queryByLabelText("max quantity")).toBeNull();
    expect(within(dialog).getByLabelText("max notional")).toHaveValue("1000");
    await user.type(within(dialog).getByLabelText("Asset"), "USD");
    await user.click(
      within(dialog).getByRole("button", { name: "Add policy" }),
    );

    await waitFor(() =>
      expect(putLimitMock).toHaveBeenCalledWith(
        {
          policy: "order_size_limit",
          scope: "settlement_asset",
          account: "",
          accountGroup: "",
          asset: "USD",
          values: { max_notional: "1000" },
        },
        "reject",
      ),
    );
  });

  it("uses clearable numeric steppers and pre-searches account and asset fields", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValue([
      { code: "acc-main", effectiveCurrency: "USD" },
    ]);
    fetchAssetsMock.mockResolvedValue([{ code: "AAPL" }]);
    renderLimits();

    await user.click(screen.getAllByRole("button", { name: "Add policy" })[0]);
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
      within(dialog).getByRole("option", {
        name: "Account + underlying asset",
      }),
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

    await user.click(screen.getAllByRole("button", { name: "Add policy" })[0]);
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

  it("requires an explicit PnL barrier currency without prefilling the account currency", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValue([
      { code: "acc-usd", effectiveCurrency: "USD" },
    ]);
    renderLimits();

    await user.click(screen.getAllByRole("button", { name: "Add policy" })[0]);
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "PnL Kill Switch" }),
    );
    const currencyValidationMessage = within(dialog).getByText(
      "spot_funds_pnl_bounds_kill_switch requires currency",
    );
    const currencyWithValidation = within(dialog).getByLabelText("currency");
    expect(currencyValidationMessage).toHaveAttribute(
      "id",
      "kind-currency-error",
    );
    expect(currencyWithValidation).toHaveAttribute("aria-invalid", "true");
    expect(currencyWithValidation).toHaveAttribute(
      "aria-errormessage",
      currencyValidationMessage.id,
    );
    expect(
      within(dialog).getByText(
        "Currency and at least one PnL bound are required.",
      ),
    ).toBeInTheDocument();
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

    const currency = within(dialog).getByLabelText("currency");
    expect(currency).toHaveValue("");
    await user.type(currency, "EUR");
    await user.type(within(dialog).getByLabelText("lower bound"), "-100");

    await user.click(
      within(dialog).getByRole("button", { name: "Add policy" }),
    );
    expect(
      screen.queryByRole("button", { name: "Rebuild and apply" }),
    ).not.toBeInTheDocument();
    await waitFor(() =>
      expect(putLimitMock).toHaveBeenCalledWith(
        {
          policy: "spot_funds_pnl_bounds_kill_switch",
          scope: "account",
          account: "acc-usd",
          accountGroup: "",
          asset: "",
          values: { currency: "EUR", lower_bound: "-100" },
        },
        "reject",
      ),
    );
  });

  it("blocks a malformed PnL currency before submitting the form", async () => {
    const user = userEvent.setup();
    renderLimits();

    await user.click(screen.getAllByRole("button", { name: "Add policy" })[0]);
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "PnL Kill Switch" }),
    );
    await user.type(within(dialog).getByLabelText("currency"), "US D");
    await user.type(within(dialog).getByLabelText("lower bound"), "-100");

    expect(
      within(dialog).getByText("Asset must not contain whitespace"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Add policy" }),
    ).toBeDisabled();
    expect(putLimitMock).not.toHaveBeenCalled();
  });

  it("offers to create a missing account and resubmits with missingAccount=create", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValue([]);
    putLimitMock.mockRejectedValueOnce(
      new ApiError(
        'block account: account "acc-new" does not exist',
        "account_missing",
        404,
        undefined,
        "acc-new",
      ),
    );
    putLimitMock.mockResolvedValueOnce({
      policy: "spot_funds_pnl_bounds_kill_switch",
      scope: "account",
      account: "acc-new",
      accountGroup: "",
      asset: "",
      values: { currency: "USD", lower_bound: "-100" },
    });
    renderLimits();

    await user.click(screen.getAllByRole("button", { name: "Add policy" })[0]);
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "PnL Kill Switch" }),
    );
    await user.click(within(dialog).getAllByRole("combobox")[1]);
    await user.click(within(dialog).getByRole("option", { name: "Account" }));

    const account = within(dialog).getByLabelText("Account");
    await user.type(account, "acc-new");
    await user.type(within(dialog).getByLabelText("currency"), "USD");
    await user.type(within(dialog).getByLabelText("lower bound"), "-100");

    await user.click(
      within(dialog).getByRole("button", { name: "Add policy" }),
    );
    // The PnL policy never requires an engine rebuild confirm, so this is the
    // account_missing confirm and only it.
    const confirmDialog = await screen.findByRole("alertdialog", {
      name: "Account does not exist",
    });
    expect(
      within(confirmDialog).getByText(/does not exist yet/),
    ).toBeInTheDocument();
    expect(putLimitMock).toHaveBeenCalledTimes(1);

    await user.click(
      within(confirmDialog).getByRole("button", {
        name: "Create account and continue",
      }),
    );

    await waitFor(() => expect(putLimitMock).toHaveBeenCalledTimes(2));
    expect(putLimitMock).toHaveBeenNthCalledWith(
      2,
      {
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account",
        account: "acc-new",
        accountGroup: "",
        asset: "",
        values: { currency: "USD", lower_bound: "-100" },
      },
      "create",
    );
  });

  it("keeps the PnL currency editable while account barrier identity stays locked", async () => {
    renderLimitDialog({
      policy: "spot_funds_pnl_bounds_kill_switch",
      scope: "account",
      account: "acc-usd",
      accountGroup: "",
      asset: "",
      values: { currency: "USD", lower_bound: "-100" },
    });
    const dialog = screen.getByRole("dialog");

    expect(within(dialog).getAllByRole("combobox")[1]).toBeDisabled();
    expect(within(dialog).getByLabelText("Account")).toBeDisabled();
    expect(within(dialog).getByLabelText("currency")).toBeEnabled();
  });

  it("keeps the PnL currency editable while account-group identity stays locked", async () => {
    renderLimitDialog({
      policy: "spot_funds_pnl_bounds_kill_switch",
      scope: "account_group",
      account: "",
      accountGroup: "desk-a",
      asset: "",
      values: { currency: "USD", lower_bound: "-100" },
    });
    const dialog = screen.getByRole("dialog");

    expect(within(dialog).getAllByRole("combobox")[1]).toBeDisabled();
    expect(within(dialog).getByLabelText("Account group")).toBeDisabled();
    expect(within(dialog).getByLabelText("currency")).toBeEnabled();
  });

  it("shows the server validation error for an unknown PnL currency", async () => {
    const user = userEvent.setup();
    putLimitMock.mockRejectedValueOnce(
      new ApiError("currency XYZ is unknown", "validation", 422),
    );
    renderLimits();

    await user.click(screen.getAllByRole("button", { name: "Add policy" })[0]);
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getAllByRole("combobox")[0]);
    await user.click(
      within(dialog).getByRole("option", { name: "PnL Kill Switch" }),
    );
    await user.type(within(dialog).getByLabelText("currency"), "XYZ");
    await user.type(within(dialog).getByLabelText("lower bound"), "-100");
    await user.click(
      within(dialog).getByRole("button", { name: "Add policy" }),
    );

    expect(
      await screen.findByText("currency XYZ is unknown"),
    ).toBeInTheDocument();
  });
});

/** An account-scope rate limit as GET /limits lists it. */
function accountRateLimit(account: string): Limit {
  return {
    policy: "rate_limit",
    scope: "account",
    account,
    accountGroup: "",
    asset: "",
    values: { max_orders: "100", window: "1s" },
  };
}

/** A group-scope PnL barrier as GET /limits lists it. */
const groupPnlLimit: Limit = {
  policy: "spot_funds_pnl_bounds_kill_switch",
  scope: "account_group",
  account: "",
  accountGroup: "desk-a",
  asset: "",
  values: { currency: "USD", lower_bound: "-1000" },
};

// The account surfaces of the policies list, by name: the sortable column, the
// row's account cell with its filter-by button, and the filter bar's account
// field with the global toggle.
function accountSurfaces(code: string) {
  const table = screen.getByRole("table");
  return {
    column: within(table).queryByRole("button", {
      name: "Sort by Account / group",
    }),
    cell: within(table).queryByText(code),
    "row filter": within(table).queryByRole("button", {
      name: `Filter by ${code}`,
    }),
    "account field": screen.queryByLabelText("Filter by account"),
    "global toggle": screen.queryByRole("button", {
      name: /global account filter/i,
    }),
  };
}

/** The table row of the barrier whose policy badge reads `policy`. */
function policyRow(policy: string): HTMLElement {
  const row = screen.getByText(policy).closest("tr");
  if (!row) {
    throw new Error(`row of ${policy} not found`);
  }
  return row;
}

/** Column count of the policies list's loading skeleton. */
function loadingSkeletonColumns(): number {
  useLimitsMock.mockReturnValue({
    load: { state: "loading", data: null, error: null },
    reload: vi.fn(),
  });
  renderLimits();
  return skeletonColumns();
}

async function openAddDialog(
  user: ReturnType<typeof userEvent.setup>,
): Promise<HTMLElement> {
  await user.click(screen.getAllByRole("button", { name: "Add policy" })[0]);
  return screen.getByRole("dialog");
}

/** Pick `option` in the dialog's policy (0) or scope (1) select. */
async function chooseInDialog(
  user: ReturnType<typeof userEvent.setup>,
  dialog: HTMLElement,
  select: 0 | 1,
  option: string,
) {
  await user.click(within(dialog).getAllByRole("combobox")[select]);
  await user.click(within(dialog).getByRole("option", { name: option }));
}

/** The grid holding the dialog's account, group and asset fields. */
function identityGrid(dialog: HTMLElement): HTMLElement {
  const label = within(dialog).getByText("Asset", { selector: "label" });
  const grid = label.parentElement?.parentElement;
  if (!grid) {
    throw new Error("identity grid not found");
  }
  return grid;
}

/** Open the delete confirm of the barrier whose policy badge reads `policy`. */
async function openDeleteConfirm(
  user: ReturnType<typeof userEvent.setup>,
  policy: string,
): Promise<HTMLElement> {
  await user.click(
    within(policyRow(policy)).getByRole("button", {
      name: `Delete ${policy}`,
    }),
  );
  return screen.findByRole("alertdialog", { name: "Delete policy barrier?" });
}

describe("Limits with accounts shown", () => {
  it("shows the account column, filter and global toggle", () => {
    useLimitsMock.mockReturnValue(readyPage([accountRateLimit("acc-1")]));
    renderLimits();

    for (const [name, surface] of Object.entries(accountSurfaces("acc-1"))) {
      expect(surface, name).not.toBeNull();
    }
  });

  it("keeps the account and group in row links and every row action", () => {
    useLimitsMock.mockReturnValue(
      readyPage([accountRateLimit("acc-1"), groupPnlLimit]),
    );
    renderLimits();

    const accountRow = within(policyRow("Rate limit"));
    expect(
      shareLinkParams(
        accountRow.getByRole("button", { name: "Copy link (Rate limit)" }),
      ).get("account"),
    ).toBe("acc-1");
    const groupRow = within(policyRow("PnL Kill Switch"));
    expect(
      shareLinkParams(
        groupRow.getByRole("button", { name: "Copy link (PnL Kill Switch)" }),
      ).get("accountGroup"),
    ).toBe("desk-a");
    expect(
      groupRow.getByRole("button", { name: "Edit PnL Kill Switch" }),
    ).toBeInTheDocument();
    expect(
      groupRow.getByRole("button", { name: "Delete PnL Kill Switch" }),
    ).toBeInTheDocument();
  });

  it("loads with a skeleton column per visible column", () => {
    expect(loadingSkeletonColumns()).toBe(6);
  });

  it("names the account filter in the no-match hint", () => {
    renderLimits("/policies?policy=rate");

    expect(
      screen.getByText(
        "No barrier matches the current account or policy filter.",
      ),
    ).toBeInTheDocument();
  });

  it("names the account when deleting an account barrier", async () => {
    const user = userEvent.setup();
    useLimitsMock.mockReturnValue(readyPage([accountRateLimit("acc-1")]));
    renderLimits();

    const confirm = await openDeleteConfirm(user, "Rate limit");
    expect(
      within(confirm).getByText(
        "This removes the Rate limit barrier on Account account acc-1.",
      ),
    ).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(deleteLimitMock).toHaveBeenCalledWith({
        policy: "rate_limit",
        scope: "account",
        account: "acc-1",
        accountGroup: "",
        asset: "",
      }),
    );
  });

  it("seeds the dialog account from the filter and lays fields in two columns", async () => {
    const user = userEvent.setup();
    renderLimits("/policies?account=acc-1");

    const dialog = await openAddDialog(user);
    await chooseInDialog(user, dialog, 1, "Account + asset");

    expect(within(dialog).getByLabelText("Account")).toHaveValue("acc-1");
    expect(identityGrid(dialog)).toHaveClass("grid-cols-2");
  });
});

describe("Limits with accounts hidden", () => {
  beforeEach(() => {
    setAccountScope({ accounts: false, account: "main" });
  });

  it("shows no account column, filter or global toggle", () => {
    useLimitsMock.mockReturnValue(readyPage([accountRateLimit("main")]));
    renderLimits();

    for (const [name, surface] of Object.entries(accountSurfaces("main"))) {
      expect(surface, name).toBeNull();
    }
  });

  it("keeps URL account and group filters out of requests, suggestions and the share link", () => {
    renderLimits("/policies?account=desk-alpha&accountGroup=desk-a", {
      includeGroupsApi: true,
    });

    expect(useLimitsMock).toHaveBeenCalled();
    for (const [filters] of useLimitsMock.mock.calls) {
      expect(filters?.account).toBeUndefined();
      expect(filters?.accountGroup).toBeUndefined();
    }
    expect(screen.queryByText("Active filters")).not.toBeInTheDocument();
    const shared = shareLinkParams(
      screen.getByRole("button", {
        name: "Copy a link to the current filter set",
      }),
    );
    expect(shared.has("account")).toBe(false);
    expect(shared.has("accountGroup")).toBe(false);
    expect(fetchAccountsMock).not.toHaveBeenCalled();
    expect(fetchGroupsMock).not.toHaveBeenCalled();
  });

  it("links an account row without the account and leaves a group row read-only", () => {
    useLimitsMock.mockReturnValue(
      readyPage([accountRateLimit("main"), groupPnlLimit]),
    );
    renderLimits();

    const accountRow = within(policyRow("Rate limit"));
    const shared = shareLinkParams(
      accountRow.getByRole("button", { name: "Copy link (Rate limit)" }),
    );
    expect(shared.has("account")).toBe(false);
    expect(
      accountRow.getByRole("button", { name: "Edit Rate limit" }),
    ).toBeInTheDocument();

    const groupRow = within(policyRow("PnL Kill Switch"));
    expect(groupRow.getByText("Account group")).toBeInTheDocument();
    expect(groupRow.queryAllByRole("button")).toEqual([]);
  });

  it("loads with a skeleton column per visible column", () => {
    expect(loadingSkeletonColumns()).toBe(5);
  });

  it("names no account filter in the no-match hint", () => {
    renderLimits("/policies?policy=rate");

    expect(
      screen.getByText("No barrier matches the current filters."),
    ).toBeInTheDocument();
  });

  it("deletes an account barrier on the default account without naming it", async () => {
    const user = userEvent.setup();
    useLimitsMock.mockReturnValue(readyPage([accountRateLimit("main")]));
    renderLimits();

    const confirm = await openDeleteConfirm(user, "Rate limit");
    expect(
      within(confirm).getByText(
        "This removes the Rate limit barrier on Account.",
      ),
    ).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(deleteLimitMock).toHaveBeenCalledWith({
        policy: "rate_limit",
        scope: "account",
        account: "main",
        accountGroup: "",
        asset: "",
      }),
    );
  });

  it("offers no group scope and no account or group field in the dialog", async () => {
    const user = userEvent.setup();
    renderLimits("/policies", { includeGroupsApi: true });

    const dialog = await openAddDialog(user);
    await chooseInDialog(user, dialog, 0, "PnL Kill Switch");
    await user.click(within(dialog).getAllByRole("combobox")[1]);
    expect(
      within(dialog)
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual(["Global", "Account"]);
    await user.click(within(dialog).getByRole("option", { name: "Account" }));

    expect(within(dialog).queryByLabelText("Account")).toBeNull();
    expect(within(dialog).queryByLabelText("Account group")).toBeNull();
  });

  it("lays the asset field alone across the dialog row", async () => {
    const user = userEvent.setup();
    renderLimits();

    const dialog = await openAddDialog(user);
    await chooseInDialog(user, dialog, 1, "Account + asset");

    expect(within(dialog).queryByLabelText("Account")).toBeNull();
    expect(identityGrid(dialog)).not.toHaveClass("grid-cols-2");
  });

  it("writes an account-scope rate limit on the default account", async () => {
    const user = userEvent.setup();
    useLimitsMock.mockReturnValue(readyPage([accountRateLimit("main")]));
    renderLimits();

    const dialog = await openAddDialog(user);
    await chooseInDialog(user, dialog, 1, "Account");
    await user.type(within(dialog).getByLabelText("max orders"), "5");
    await user.type(within(dialog).getByLabelText("window"), "1");
    await user.click(
      within(dialog).getByRole("button", { name: "Add policy" }),
    );

    await waitFor(() =>
      expect(putLimitMock).toHaveBeenCalledWith(
        {
          policy: "rate_limit",
          scope: "account",
          account: "main",
          accountGroup: "",
          asset: "",
          values: { max_orders: "5", window: "1s" },
        },
        "reject",
      ),
    );
    expect(fetchAccountsMock).not.toHaveBeenCalled();
  });

  it("writes a broker rate limit with no account", async () => {
    const user = userEvent.setup();
    useLimitsMock.mockReturnValue(readyPage([accountRateLimit("main")]));
    renderLimits();

    const dialog = await openAddDialog(user);
    await user.type(within(dialog).getByLabelText("max orders"), "5");
    await user.type(within(dialog).getByLabelText("window"), "1");
    await user.click(
      within(dialog).getByRole("button", { name: "Add policy" }),
    );

    await waitFor(() =>
      expect(putLimitMock).toHaveBeenCalledWith(
        {
          policy: "rate_limit",
          scope: "broker",
          account: "",
          accountGroup: "",
          asset: "",
          values: { max_orders: "5", window: "1s" },
        },
        "reject",
      ),
    );
  });
});
