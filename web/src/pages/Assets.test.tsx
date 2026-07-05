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
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  Asset,
  AssetClass,
  AssetClassListFilters,
  AssetListFilters,
  PagedResult,
  TextMatchMode,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { useAssets } from "@/api/useAssets";
import { useAssetClasses } from "@/api/useAssetClasses";
import { SidebarProvider } from "@/components/SidebarContext";
import { ApiError } from "@/framework";
import i18n from "@/i18n";
import { Assets } from "@/pages/Assets";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

const navigateMock = vi.fn();

vi.mock("@/api/useAssets", () => ({ useAssets: vi.fn() }));
vi.mock("@/api/useAssetClasses", () => ({ useAssetClasses: vi.fn() }));
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
vi.mock("react-router-dom", async () => {
  const actual =
    await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return { ...actual, useNavigate: () => navigateMock };
});

const createAssetMock = vi.fn();
const updateAssetMock = vi.fn();
const deleteAssetMock = vi.fn();
const createAssetClassMock = vi.fn();
const updateAssetClassMock = vi.fn();
const deleteAssetClassMock = vi.fn();
const reloadMock = vi.fn();
const reloadClassesMock = vi.fn();
const useAssetsMock = vi.mocked(useAssets);
const useAssetClassesMock = vi.mocked(useAssetClasses);

function removeStoredPreference(key: string) {
  try {
    window.localStorage?.removeItem(key);
  } catch {
    // Test environments may expose no localStorage; the app falls back to cookies.
  }
  document.cookie = `${encodeURIComponent(key)}=; Max-Age=0; Path=/; SameSite=Lax`;
}

function setStoredPreference(key: string, value: string) {
  try {
    window.localStorage?.setItem(key, value);
  } catch {
    // Keep the cookie mirror in sync with the production storage fallback.
  }
  document.cookie = `${encodeURIComponent(key)}=${encodeURIComponent(value)}; Path=/; SameSite=Lax`;
}

function ready<T>(data: T, reload: () => void): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload };
}

const assets: Asset[] = [
  { code: "BTC", title: "Bitcoin", assetClass: "crypto" },
  { code: "AAPL", title: "Apple Inc.", assetClass: "equity" },
];

const assetClasses: AssetClass[] = [
  { code: "crypto", title: "Cryptocurrencies", notes: "Digital assets", assetCount: 1 },
  { code: "equity", title: "Equities", notes: "", assetCount: 1 },
];

function matchesText(value: string, filter: string | undefined, mode: TextMatchMode = "contains") {
  if (filter === undefined || filter.trim() === "") {
    return true;
  }
  const actual = value.toLowerCase();
  const expected = filter.trim().toLowerCase();
  switch (mode) {
    case "exact":
      return actual === expected;
    case "starts_with":
      return actual.startsWith(expected);
    case "ends_with":
      return actual.endsWith(expected);
    default:
      return actual.includes(expected);
  }
}

function filterAssets(filters: AssetListFilters = {}) {
  return assets.filter(
    (asset) =>
      (matchesText(asset.code, filters.code, filters.codeMatch) ||
        matchesText(asset.title, filters.code, filters.codeMatch)) &&
      matchesText(asset.assetClass, filters.class, filters.classMatch),
  );
}

function filterAssetClasses(filters: AssetClassListFilters = {}) {
  return assetClasses.filter((assetClass) =>
    matchesText(assetClass.code, filters.code, filters.codeMatch),
  );
}

function pageResult<T>(
  items: T[],
  filters: { limit?: number; offset?: number } = {},
): PagedResult<T> {
  const start = filters.offset ?? 0;
  const limit = filters.limit;
  return {
    items:
      limit === undefined ? items : items.slice(start, start + limit),
    total: items.length,
  };
}

function renderAssets(initialPath = "/assets") {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <MemoryRouter initialEntries={[initialPath]}>
            <SidebarProvider>
              <Assets />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        createAsset: createAssetMock,
        updateAsset: updateAssetMock,
        deleteAsset: deleteAssetMock,
        createAssetClass: createAssetClassMock,
        updateAssetClass: updateAssetClassMock,
        deleteAssetClass: deleteAssetClassMock,
        fetchAssets: async (filters?: AssetListFilters | AbortSignal) =>
          filterAssets(filters instanceof AbortSignal ? undefined : filters),
        fetchAssetClasses: async (filters?: AssetClassListFilters | AbortSignal) =>
          filterAssetClasses(filters instanceof AbortSignal ? undefined : filters),
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  removeStoredPreference("pit-officer-assets-page-size");
  removeStoredPreference("pit-officer-asset-classes-page-size");
  await i18n.changeLanguage("en");
  useAssetsMock.mockImplementation((filters) =>
    ready(pageResult(filterAssets(filters), filters), reloadMock),
  );
  useAssetClassesMock.mockImplementation((filters) =>
    ready(pageResult(filterAssetClasses(filters), filters), reloadClassesMock),
  );
});

afterEach(() => {
  vi.useRealTimers();
});

describe("Assets", () => {
  it("creates an asset and reloads on submit", async () => {
    const user = userEvent.setup();
    createAssetMock.mockResolvedValue({
      code: "ETH",
      title: "Ether",
      assetClass: "crypto",
    });
    renderAssets();

    await user.click(screen.getByRole("button", { name: /new asset/i }));
    const dialog = await screen.findByRole("dialog", { name: /create asset/i });
    await user.type(within(dialog).getByLabelText(/asset code/i), "ETH");
    await user.type(within(dialog).getByLabelText(/^title$/i), "Ether");
    await user.type(within(dialog).getByLabelText(/^class$/i), "crypto");
    await user.click(within(dialog).getByRole("button", { name: /^create$/i }));

    await waitFor(() =>
      expect(createAssetMock).toHaveBeenCalledWith("ETH", "Ether", "crypto"),
    );
    expect(reloadMock).toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("surfaces a client error and keeps the create dialog open", async () => {
    const user = userEvent.setup();
    createAssetMock.mockRejectedValue(
      new ApiError("asset already exists", "conflict", 409),
    );
    renderAssets();

    await user.click(screen.getByRole("button", { name: /new asset/i }));
    const dialog = await screen.findByRole("dialog", { name: /create asset/i });
    await user.type(within(dialog).getByLabelText(/asset code/i), "BTC");
    await user.click(within(dialog).getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /asset already exists/i,
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("edits an asset's code, title, and class", async () => {
    const user = userEvent.setup();
    updateAssetMock.mockResolvedValue({
      code: "XBT",
      title: "Bitcoin XBT",
      assetClass: "crypto",
    });
    renderAssets();

    const row = screen.getByText("Bitcoin").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", { name: /edit asset/i }),
    );
    const dialog = await screen.findByRole("dialog", { name: /edit asset/i });
    const codeInput = within(dialog).getByLabelText(/asset code/i);
    await user.clear(codeInput);
    await user.type(codeInput, "XBT");
    const titleInput = within(dialog).getByLabelText(/^title$/i);
    await user.clear(titleInput);
    await user.type(titleInput, "Bitcoin XBT");
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(updateAssetMock).toHaveBeenCalledWith(
        "BTC",
        "XBT",
        "Bitcoin XBT",
        "crypto",
      ),
    );
    expect(reloadMock).toHaveBeenCalled();
  });

  it("deletes an asset after confirmation", async () => {
    const user = userEvent.setup();
    deleteAssetMock.mockResolvedValue(undefined);
    renderAssets();

    const row = screen.getByText("Apple Inc.").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", { name: /delete aapl/i }),
    );
    const confirm = await screen.findByRole("alertdialog", {
      name: /delete asset/i,
    });
    await user.click(within(confirm).getByRole("button", { name: /^delete$/i }));

    await waitFor(() => expect(deleteAssetMock).toHaveBeenCalledWith("AAPL"));
    expect(reloadMock).toHaveBeenCalled();
  });

  it("sends asset search filters to the API", async () => {
    renderAssets();

    expect(screen.getByText("Bitcoin")).toBeInTheDocument();
    expect(screen.getByText("Apple Inc.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/asset search/i), {
      target: { value: "apple" },
    });

    await waitFor(() =>
      expect(useAssetsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ code: "apple", codeMatch: "contains" }),
      ),
    );
    expect(screen.queryByText("Bitcoin")).not.toBeInTheDocument();
    expect(screen.getByText("Apple Inc.")).toBeInTheDocument();
  });

  it("restores asset sort from the deep link", async () => {
    renderAssets("/assets?sort=title&order=desc");

    await waitFor(() =>
      expect(useAssetsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort: "title", order: "desc" }),
      ),
    );
  });

  it("pages the assets tab through server filters", async () => {
    const user = userEvent.setup();
    setStoredPreference("pit-officer-assets-page-size", "5");
    useAssetsMock.mockImplementation((filters) => {
      const page = pageResult(filterAssets(filters), filters);
      return ready({ ...page, total: 6 }, reloadMock);
    });
    renderAssets();

    await waitFor(() =>
      expect(useAssetsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ limit: 5, offset: 0 }),
      ),
    );
    expect(screen.getAllByText("Page 1 of 2")).toHaveLength(2);

    await user.click(screen.getAllByRole("button", { name: "Next" })[0]);

    await waitFor(() =>
      expect(useAssetsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ limit: 5, offset: 5 }),
      ),
    );
  });

  it("deep-links to filtered trades from the trades row action", async () => {
    const user = userEvent.setup();
    renderAssets();

    const row = screen.getByText("Bitcoin").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", { name: /^trades$/i }),
    );

    expect(navigateMock).toHaveBeenCalledWith("/orders?tab=trades&baseAsset=BTC");
  });

  it("seeds the class filter from the ?class= deep link", () => {
    renderAssets("/assets?class=crypto");

    expect(screen.getByText("Bitcoin")).toBeInTheDocument();
    expect(screen.queryByText("Apple Inc.")).not.toBeInTheDocument();
  });

  it("copies a deep link with the active asset filters", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    renderAssets("/assets?class=crypto");

    await user.type(screen.getByLabelText(/asset search/i), "btc");
    await waitFor(() =>
      expect(useAssetsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ code: "btc", codeMatch: "contains" }),
      ),
    );
    fireEvent.click(
      screen.getByRole("button", {
        name: /copy a link to the current filter set/i,
      }),
    );

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const url = new URL(writeText.mock.calls[0][0]);
    expect(url.pathname).toBe("/assets");
    expect(url.searchParams.get("class")).toBe("crypto");
    expect(url.searchParams.get("code")).toBe("btc");
    expect(url.searchParams.get("sort")).toBeNull();
  });
});

describe("Assets classes tab", () => {
  it("switches to the Classes tab and lists asset classes", async () => {
    const user = userEvent.setup();
    renderAssets();

    await user.click(screen.getByRole("button", { name: /^classes$/i }));

    expect(screen.getByText("Cryptocurrencies")).toBeInTheDocument();
    expect(screen.getByText("Equities")).toBeInTheDocument();
  });

  it("pages the classes tab through server filters", async () => {
    const user = userEvent.setup();
    setStoredPreference("pit-officer-asset-classes-page-size", "5");
    useAssetClassesMock.mockImplementation((filters) => {
      const page = pageResult(filterAssetClasses(filters), filters);
      return ready({ ...page, total: 6 }, reloadClassesMock);
    });
    renderAssets();

    await user.click(screen.getByRole("button", { name: /^classes$/i }));
    await waitFor(() =>
      expect(useAssetClassesMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ limit: 5, offset: 0 }),
      ),
    );
    expect(screen.getAllByText("Page 1 of 2")).toHaveLength(2);

    await user.click(screen.getAllByRole("button", { name: "Next" })[0]);

    await waitFor(() =>
      expect(useAssetClassesMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ limit: 5, offset: 5 }),
      ),
    );
  });

  it("restores class sort from the deep link", async () => {
    renderAssets("/assets?tab=classes&sort=assetCount&order=desc");

    await waitFor(() =>
      expect(useAssetClassesMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort: "assetCount", order: "desc" }),
      ),
    );
  });

  it("creates an asset class and reloads on submit", async () => {
    const user = userEvent.setup();
    createAssetClassMock.mockResolvedValue({
      code: "fx",
      title: "Foreign exchange",
      notes: "",
      assetCount: 0,
    });
    renderAssets();

    await user.click(screen.getByRole("button", { name: /new class/i }));
    const dialog = await screen.findByRole("dialog", {
      name: /create asset class/i,
    });
    await user.type(within(dialog).getByLabelText(/class code/i), "fx");
    await user.type(within(dialog).getByLabelText(/^title$/i), "Foreign exchange");
    await user.click(within(dialog).getByRole("button", { name: /^create$/i }));

    await waitFor(() =>
      expect(createAssetClassMock).toHaveBeenCalledWith(
        "fx",
        "Foreign exchange",
        "",
      ),
    );
    expect(reloadClassesMock).toHaveBeenCalled();
  });

  it("edits an asset class code, title, and notes", async () => {
    const user = userEvent.setup();
    updateAssetClassMock.mockResolvedValue({
      code: "digital",
      title: "Digital assets",
      notes: "Crypto",
      assetCount: 1,
    });
    renderAssets();

    await user.click(screen.getByRole("button", { name: /^classes$/i }));
    const row = screen.getByText("Cryptocurrencies").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", { name: /edit class/i }),
    );
    const dialog = await screen.findByRole("dialog", { name: /edit class/i });
    const codeInput = within(dialog).getByLabelText(/class code/i);
    await user.clear(codeInput);
    await user.type(codeInput, "digital");
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(updateAssetClassMock).toHaveBeenCalledWith(
        "crypto",
        "digital",
        "Cryptocurrencies",
        "Digital assets",
      ),
    );
    expect(reloadClassesMock).toHaveBeenCalled();
  });

  it("deletes an asset class after confirmation", async () => {
    const user = userEvent.setup();
    deleteAssetClassMock.mockResolvedValue(undefined);
    renderAssets();

    await user.click(screen.getByRole("button", { name: /^classes$/i }));
    const row = screen.getByText("Equities").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", { name: /delete equity/i }),
    );
    const confirm = await screen.findByRole("alertdialog", {
      name: /delete asset class/i,
    });
    await user.click(within(confirm).getByRole("button", { name: /^delete$/i }));

    // The equity class has one linked asset, so deletion forces detachment.
    await waitFor(() =>
      expect(deleteAssetClassMock).toHaveBeenCalledWith("equity", true),
    );
    expect(reloadClassesMock).toHaveBeenCalled();
  });

  it("filters assets to a class via the class filter action", async () => {
    const user = userEvent.setup();
    renderAssets();

    await user.click(screen.getByRole("button", { name: /^classes$/i }));
    const row = screen.getByText("Cryptocurrencies").closest("tr");
    expect(row).not.toBeNull();
    await user.click(
      within(row as HTMLElement).getByRole("button", {
        name: /filter by crypto/i,
      }),
    );

    // Filtering switches back to the Assets tab, narrowed to the crypto class.
    expect(screen.getByText("Bitcoin")).toBeInTheDocument();
    expect(screen.queryByText("Apple Inc.")).not.toBeInTheDocument();
  });
});
