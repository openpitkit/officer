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

import { beforeEach, describe, expect, it, vi } from "vitest";

import { normalizeSigningKeysStatus } from "@/api/client";
import type { BackupArchive } from "@/api/types";
import { createApiClient, createOfficerApi } from "@/framework";

function api() {
  return createOfficerApi(
    createApiClient({
      baseUrl: "/app/api/v1",
      fetch: globalThis.fetch,
    }),
  );
}

const createMarketDataInstance = (
  ...args: Parameters<ReturnType<typeof api>["createMarketDataInstance"]>
) => api().createMarketDataInstance(...args);
const checkOrder = (
  ...args: Parameters<ReturnType<typeof api>["checkOrder"]>
) => api().checkOrder(...args);
const exportBusinessCsv = (
  ...args: Parameters<ReturnType<typeof api>["exportBusinessCsv"]>
) => api().exportBusinessCsv(...args);
const fetchAccounts = (
  ...args: Parameters<ReturnType<typeof api>["fetchAccounts"]>
) => api().fetchAccounts(...args);
const fetchAssets = (
  ...args: Parameters<ReturnType<typeof api>["fetchAssets"]>
) => api().fetchAssets(...args);
const fetchBalancesPage = (
  ...args: Parameters<ReturnType<typeof api>["fetchBalancesPage"]>
) => api().fetchBalancesPage(...args);
const fetchGroups = (
  ...args: Parameters<ReturnType<typeof api>["fetchGroups"]>
) => api().fetchGroups(...args);
const exportBackup = (
  ...args: Parameters<ReturnType<typeof api>["exportBackup"]>
) => api().exportBackup(...args);
const fetchSigningKeys = (
  ...args: Parameters<ReturnType<typeof api>["fetchSigningKeys"]>
) => api().fetchSigningKeys(...args);
const fetchEventReproduction = (
  ...args: Parameters<ReturnType<typeof api>["fetchEventReproduction"]>
) => api().fetchEventReproduction(...args);
const fetchPublicKeyById = (
  ...args: Parameters<ReturnType<typeof api>["fetchPublicKeyById"]>
) => api().fetchPublicKeyById(...args);
const generateSigningKey = (
  ...args: Parameters<ReturnType<typeof api>["generateSigningKey"]>
) => api().generateSigningKey(...args);
const importSigningKey = (
  ...args: Parameters<ReturnType<typeof api>["importSigningKey"]>
) => api().importSigningKey(...args);
const restartService = (
  ...args: Parameters<ReturnType<typeof api>["restartService"]>
) => api().restartService(...args);
const resetDatabase = (
  ...args: Parameters<ReturnType<typeof api>["resetDatabase"]>
) => api().resetDatabase(...args);
const restoreBackup = (
  ...args: Parameters<ReturnType<typeof api>["restoreBackup"]>
) => api().restoreBackup(...args);
const searchMarketDataSymbols = (
  ...args: Parameters<ReturnType<typeof api>["searchMarketDataSymbols"]>
) => api().searchMarketDataSymbols(...args);
const setESignEnabled = (
  ...args: Parameters<ReturnType<typeof api>["setESignEnabled"]>
) => api().setESignEnabled(...args);
const stopService = (
  ...args: Parameters<ReturnType<typeof api>["stopService"]>
) => api().stopService(...args);
const submitExecutionReport = (
  ...args: Parameters<ReturnType<typeof api>["submitExecutionReport"]>
) => api().submitExecutionReport(...args);
const updateMarketDataInstanceSettings = (
  ...args: Parameters<
    ReturnType<typeof api>["updateMarketDataInstanceSettings"]
  >
) => api().updateMarketDataInstanceSettings(...args);
const verifyMarketDataSymbol = (
  ...args: Parameters<ReturnType<typeof api>["verifyMarketDataSymbol"]>
) => api().verifyMarketDataSymbol(...args);

function okMarketDataResponse(): Response {
  return new Response(
    JSON.stringify({
      marketData: {
        providers: [],
        instances: [],
        freshnessSeconds: 10,
        restartRequired: false,
      },
    }),
    {
      status: 200,
      headers: { "Content-Type": "application/json" },
    },
  );
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  vi.restoreAllMocks();
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(okMarketDataResponse()));
});

const backupArchive: BackupArchive = {
  manifest: {
    formatVersion: 1,
    createdAt: "2026-06-22T10:00:00Z",
    source: "unit-test-fixture",
    realm: { code: "test", title: "Test" },
    sections: ["accounts_groups"],
  },
  data: { accounts: [] },
};

describe("accounts and groups client", () => {
  it("builds account list filter query and normalizes position count", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        accounts: [
          {
            code: "acc-alpha",
            title: "Alpha",
            group: "",
            notes: "",
            blocked: false,
            blockReason: "",
            positionCount: 2,
          },
        ],
      }),
    );

    const result = await fetchAccounts({
      code: "acc*alpha",
      codeMatch: "starts_with",
      status: "blocked",
      positionCountMode: "between",
      positionCountMin: "1",
      positionCountMax: "3",
      blockReason: "halt",
      blockReasonMatch: "contains",
      group: "",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts?code=acc*alpha&codeMatch=starts_with&status=blocked&positionCountMode=between&positionCountMin=1&positionCountMax=3&blockReason=halt&group=",
      expect.anything(),
    );
    expect(result[0]).toMatchObject({ code: "acc-alpha", positionCount: 2 });
  });

  it("normalizes account currency fields across response casings", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        accounts: [
          {
            code: "acc-camel",
            title: "Camel",
            currency: "EUR",
            effectiveCurrency: "USD",
            currencyOrigin: "account",
            pnl: "123.4500",
            currencyCascade: {
              account: "USD",
              group: "EUR",
              default: "GBP",
            },
          },
          {
            Code: "acc-pascal",
            Title: "Pascal",
            Currency: "CHF",
            EffectiveCurrency: "JPY",
            CurrencyOrigin: "group",
            Pnl: "-0.1250",
            CurrencyCascade: {
              Account: "",
              Group: "JPY",
              Default: "USD",
            },
          },
          {
            code: "acc-snake",
            title: "Snake",
            currency: "GBP",
            effective_currency: "CAD",
            currency_origin: "default",
            pnl: "0",
            currencyCascade: {
              Account: "",
              Group: "",
              Default: "CAD",
            },
          },
        ],
      }),
    );

    const result = await fetchAccounts();

    expect(result).toEqual([
      expect.objectContaining({
        code: "acc-camel",
        currency: "EUR",
        effectiveCurrency: "USD",
        currencyOrigin: "account",
        pnl: "123.4500",
        currencyCascade: {
          account: "USD",
          group: "EUR",
          default: "GBP",
        },
      }),
      expect.objectContaining({
        code: "acc-pascal",
        currency: "CHF",
        effectiveCurrency: "JPY",
        currencyOrigin: "group",
        pnl: "-0.1250",
        currencyCascade: {
          account: "",
          group: "JPY",
          default: "USD",
        },
      }),
      expect.objectContaining({
        code: "acc-snake",
        currency: "GBP",
        effectiveCurrency: "CAD",
        currencyOrigin: "default",
        pnl: "0",
        currencyCascade: {
          account: "",
          group: "",
          default: "CAD",
        },
      }),
    ]);
  });

  it("normalizes an empty account PnL without coercing decimal values", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        accounts: [
          {
            code: "account-empty",
            pnl: "",
            PnlHaltReason: "missing_initial_pnl",
          },
          { code: "account-number", pnl: 12.5 },
        ],
      }),
    );

    const result = await fetchAccounts();

    expect(result).toEqual([
      expect.objectContaining({
        code: "account-empty",
        pnl: "",
        pnlHaltReason: "missing_initial_pnl",
      }),
      expect.objectContaining({ code: "account-number", pnl: "" }),
    ]);
  });

  it("builds group list filter query and normalizes aggregate counts", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        groups: [
          {
            code: "desk-alpha",
            title: "Desk alpha",
            notes: "",
            blocked: false,
            blockReason: "",
            accountCount: 3,
            positionCount: 4,
          },
        ],
      }),
    );

    const result = await fetchGroups({
      code: "desk",
      status: "active",
      positionCountMode: "less_than",
      positionCountMax: "2",
      accountCountMode: "between",
      accountCountMin: "1",
      accountCountMax: "5",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/groups?code=desk&status=active&positionCountMode=less_than&positionCountMax=2&accountCountMode=between&accountCountMin=1&accountCountMax=5",
      expect.anything(),
    );
    expect(result[0]).toMatchObject({
      code: "desk-alpha",
      accountCount: 3,
      positionCount: 4,
    });
  });

  it("preserves an empty group title from the API", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        groups: [
          {
            code: "the-code-of-group-withoit-title",
            title: "",
            notes: "",
            blocked: false,
            blockReason: "",
            accountCount: 0,
            positionCount: 0,
          },
        ],
      }),
    );

    const result = await fetchGroups();

    expect(result[0]).toMatchObject({
      code: "the-code-of-group-withoit-title",
      title: "",
    });
  });

  it("appends missingAccount when blocking, unblocking, and grouping an account", async () => {
    // A fresh Response per call: Response bodies are one-shot streams, and a
    // single shared mockResolvedValue would fail res.json() on the 2nd/3rd call.
    vi.mocked(fetch).mockImplementation(async () =>
      jsonResponse({ account: { code: "acc-1" } }),
    );

    const client = api();
    await client.blockAccount("acc-1", "risk breach", "reject");
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts/acc-1/block?missingAccount=reject",
      expect.objectContaining({ method: "POST" }),
    );

    await client.unblockAccount("acc-1", "reject");
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts/acc-1/unblock?missingAccount=reject",
      expect.objectContaining({ method: "POST" }),
    );

    await client.setAccountGroup("acc-1", "desk-a", "create");
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts/acc-1/group?missingAccount=create",
      expect.objectContaining({ method: "PUT" }),
    );
  });
});

describe("assets client", () => {
  it("builds server-side asset filter and sort query", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        assets: [{ code: "AAPL", title: "Apple Inc.", assetClass: "equity" }],
      }),
    );

    const result = await fetchAssets({
      code: "apple",
      codeMatch: "contains",
      class: "equity",
      classMatch: "exact",
      sort: "assetClass",
      order: "desc",
    });

    const url = new URL(
      String(vi.mocked(fetch).mock.calls[0][0]),
      "http://test",
    );
    expect(url.pathname).toBe("/app/api/v1/assets");
    expect(url.searchParams.get("code")).toBe("apple");
    expect(url.searchParams.get("codeMatch")).toBeNull();
    expect(url.searchParams.get("class")).toBe("equity");
    expect(url.searchParams.get("classMatch")).toBe("exact");
    expect(url.searchParams.get("sort")).toBe("assetClass");
    expect(url.searchParams.get("order")).toBe("desc");
    expect(result[0]).toMatchObject({ code: "AAPL", assetClass: "equity" });
  });

  it("normalizes paged assets", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        assets: [{ code: "AAPL", title: "Apple Inc.", assetClass: "equity" }],
        total: 12,
      }),
    );

    const result = await api().fetchAssetsPage({ limit: 5, offset: 10 });

    const url = new URL(
      String(vi.mocked(fetch).mock.calls[0][0]),
      "http://test",
    );
    expect(url.searchParams.get("limit")).toBe("5");
    expect(url.searchParams.get("offset")).toBe("10");
    expect(result).toMatchObject({
      total: 12,
      items: [{ code: "AAPL", assetClass: "equity" }],
    });
  });
});

describe("balances client", () => {
  it("builds exact identity filters without match-mode parameters", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        balances: [
          {
            account: "desk-alpha",
            asset: "AAPL",
            available: "10",
            held: "0",
            incoming: "0",
            averageEntryPrice: "100",
            realizedPnl: "0.000",
            realized_pnl_halt_reason: "missing_cost_basis",
            updatedAt: "2026-06-24T00:00:00Z",
          },
        ],
        total: 1,
      }),
    );

    const result = await fetchBalancesPage({
      account: "desk-alpha",
      groupCode: "equity-desks",
      asset: "AAPL",
      sort: "account",
      order: "asc",
    });

    const url = new URL(
      String(vi.mocked(fetch).mock.calls[0][0]),
      "http://test",
    );
    expect(url.pathname).toBe("/app/api/v1/balances");
    expect(url.searchParams.get("account")).toBe("desk-alpha");
    expect(url.searchParams.get("accountMatch")).toBeNull();
    expect(url.searchParams.get("groupCode")).toBe("equity-desks");
    expect(url.searchParams.get("asset")).toBe("AAPL");
    expect(url.searchParams.get("assetMatch")).toBeNull();
    expect(result).toMatchObject({
      total: 1,
      items: [
        {
          account: "desk-alpha",
          asset: "AAPL",
          realizedPnl: "0.000",
          realizedPnlHaltReason: "missing_cost_basis",
        },
      ],
    });
  });

  it("sets realized PnL through the balance record endpoint", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        balance: {
          account: "desk-alpha",
          asset: "USD",
          available: "1000",
          held: "0",
          incoming: "0",
          averageEntryPrice: "",
          realizedPnl: "-12.50",
          updatedAt: "2026-06-24T00:00:00Z",
        },
      }),
    );

    const result = await api().setBalanceRealizedPnl(
      "desk-alpha",
      {
        asset: "USD",
        realizedPnl: "-12.50",
      },
      "reject",
    );

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts/desk-alpha/balances/realized-pnl?missingAccount=reject",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          asset: "USD",
          realizedPnl: "-12.50",
        }),
      }),
    );
    expect(result).toMatchObject({
      account: "desk-alpha",
      asset: "USD",
      realizedPnl: "-12.50",
    });
  });
});

describe("business CSV client", () => {
  it("exports CSV with entity, delimiter, zip flag, filters, and filename", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response("account_id\nacc-default\n", {
        status: 200,
        headers: {
          "Content-Type": "text/csv",
          "Content-Disposition": 'attachment; filename="accounts.csv"',
        },
      }),
    );

    const result = await exportBusinessCsv({
      entity: "accounts",
      delimiter: "semicolon",
      zip: false,
      filters: { groupCode: "" },
    });
    const [, init] = vi.mocked(fetch).mock.calls[0];
    const body = JSON.parse(String((init as RequestInit).body));

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/business-csv/export",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({
          Accept: "text/csv",
        }),
      }),
    );
    expect(body).toEqual({
      entity: "accounts",
      delimiter: "semicolon",
      zip: false,
      filters: { groupCode: "" },
    });
    expect(result.filename).toBe("accounts.csv");
    expect(result.blob.size).toBe("account_id\nacc-default\n".length);
  });

  it("exports ZIP with the returned filename", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response("zip-bytes", {
        status: 200,
        headers: {
          "Content-Type": "application/zip",
          "Content-Disposition": "attachment; filename*=UTF-8''orders.zip",
        },
      }),
    );

    const result = await exportBusinessCsv({
      entity: "orders",
      delimiter: "comma",
      zip: true,
      filters: { account: "desk-alpha", source: "panel" },
    });

    expect(result.filename).toBe("orders.zip");
    const [, init] = vi.mocked(fetch).mock.calls[0];
    const body = JSON.parse(String((init as RequestInit).body));
    expect(body.zip).toBe(true);
    expect(body.filters).toEqual({
      account: "desk-alpha",
      source: "panel",
    });
  });

  it("omits undefined filters and never forwards display limits", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response("id\n", { status: 200 }));

    await exportBusinessCsv({
      entity: "trades",
      delimiter: "pipe",
      filters: {
        account: "desk-alpha",
        source: "api",
        limit: 50,
      } as never,
    });

    const [, init] = vi.mocked(fetch).mock.calls[0];
    const body = JSON.parse(String((init as RequestInit).body));
    expect(body.filters).toEqual({
      account: "desk-alpha",
      source: "api",
    });
    expect(body.filters).not.toHaveProperty("limit");
  });

  it("surfaces business CSV export network errors as ApiError", async () => {
    vi.mocked(fetch).mockRejectedValue(new Error("connection reset"));

    await expect(
      exportBusinessCsv({ entity: "accounts", delimiter: "comma" }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "network",
      message: expect.stringContaining("connection reset"),
    });
  });

  it("surfaces business CSV HTTP errors as ApiError", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "validation", message: "bad delimiter" },
        }),
        {
          status: 400,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );

    await expect(
      exportBusinessCsv({ entity: "accounts", delimiter: "comma" }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "validation",
      message: "bad delimiter",
    });
  });
});

describe("limits client", () => {
  it("serializes policy list filters", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ policies: [], total: 0 }),
    );

    const { fetchPoliciesPage } = api();
    await fetchPoliciesPage({
      account: "desk-alpha",
      accountGroup: "group-alpha",
      asset: "AAPL",
      policy: "spot_funds_pnl_bounds",
      limit: 25,
      offset: 50,
    });

    const calledUrl = new URL(
      String(vi.mocked(fetch).mock.calls[0][0]),
      "http://test",
    );
    expect(calledUrl.pathname).toBe("/app/api/v1/limits");
    expect(Object.fromEntries(calledUrl.searchParams.entries())).toEqual(
      expect.objectContaining({
        account: "desk-alpha",
        accountGroup: "group-alpha",
        asset: "AAPL",
        policy: "spot_funds_pnl_bounds",
        limit: "25",
        offset: "50",
      }),
    );
  });

  it("flattens typed limit-list responses for the dashboard view", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        policies: [
          {
            kind: "rate_limit",
            scope: "account",
            account: "desk-alpha",
            asset: "",
            values: { rate: { windowMs: 60000, maxOrders: 20 } },
          },
          {
            kind: "order_size_limit",
            scope: "account_asset",
            account: "desk-alpha",
            asset: "AAPL",
            values: { orderSize: { maxQuantity: "10", maxNotional: "1500" } },
          },
          {
            kind: "spot_funds_pnl_bounds_kill_switch",
            scope: "account_group",
            account: "",
            accountGroup: "desk-alpha",
            asset: "",
            values: {
              spotFundsPnlBounds: {
                currency: "USD",
                lowerBound: "-1000",
                upperBound: "",
              },
            },
          },
        ],
        total: 3,
      }),
    );

    const { fetchLimits } = api();
    const limits = await fetchLimits({ account: "desk-alpha" });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits?account=desk-alpha",
      expect.any(Object),
    );
    expect(limits).toEqual([
      {
        policy: "rate_limit",
        scope: "account",
        account: "desk-alpha",
        accountGroup: "",
        asset: "",
        values: { max_orders: "20", window: "1m" },
      },
      {
        policy: "order_size_limit",
        scope: "account_asset",
        account: "desk-alpha",
        accountGroup: "",
        asset: "AAPL",
        values: { max_quantity: "10", max_notional: "1500" },
      },
      {
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account_group",
        account: "",
        accountGroup: "desk-alpha",
        asset: "",
        values: { currency: "USD", lower_bound: "-1000", upper_bound: "" },
      },
    ]);
  });

  it("upserts rate limits through the typed policy endpoint", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        rateLimit: {
          scope: "account",
          account: "desk-alpha",
          asset: "",
          windowMs: 60000,
          maxOrders: 20,
        },
      }),
    );

    const { putLimit } = api();
    const limit = await putLimit(
      {
        policy: "rate_limit",
        scope: "account",
        account: "desk-alpha",
        asset: "",
        values: { max_orders: "20", window: "1m" },
      },
      "reject",
    );

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits/rate?missingAccount=reject",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          scope: "account",
          account: "desk-alpha",
          asset: "",
          windowMs: 60000,
          maxOrders: 20,
        }),
      }),
    );
    expect(limit.values.window).toBe("1m");
  });

  it("resends the rate limit with missingAccount=create after account creation is confirmed", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        rateLimit: {
          scope: "account",
          account: "desk-alpha",
          asset: "",
          windowMs: 60000,
          maxOrders: 20,
        },
      }),
    );

    const { putLimit } = api();
    await putLimit(
      {
        policy: "rate_limit",
        scope: "account",
        account: "desk-alpha",
        asset: "",
        values: { max_orders: "20", window: "1m" },
      },
      "create",
    );

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits/rate?missingAccount=create",
      expect.any(Object),
    );
  });

  it("omits missingAccount for a limit scope without an account axis", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        orderSizeLimit: {
          scope: "asset",
          account: "",
          asset: "AAPL",
          maxQuantity: "10",
          maxNotional: "1500",
        },
      }),
    );

    const { putLimit } = api();
    await putLimit(
      {
        policy: "order_size_limit",
        scope: "asset",
        account: "",
        asset: "AAPL",
        values: { max_quantity: "10", max_notional: "1500" },
      },
      "reject",
    );

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits/order-size",
      expect.any(Object),
    );
  });

  it("omits missingAccount for a self-computed PnL bound scoped to an account group", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        spotFundsPnlBoundsLimit: {
          scope: "account_group",
          account: "",
          accountGroup: "desk-alpha",
          currency: "USD",
          lowerBound: "-1000",
          upperBound: "",
        },
      }),
    );

    const { putLimit } = api();
    await putLimit(
      {
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account_group",
        account: "",
        accountGroup: "desk-alpha",
        asset: "",
        values: { currency: "USD", lower_bound: "-1000", upper_bound: "" },
      },
      "reject",
    );

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits/spot-funds-pnl-bounds",
      expect.any(Object),
    );
  });

  it("rejects invalid max_orders before sending rate limits", async () => {
    const { putLimit } = api();

    await expect(
      putLimit(
        {
          policy: "rate_limit",
          scope: "account",
          account: "desk-alpha",
          asset: "",
          values: { max_orders: "", window: "1m" },
        },
        "reject",
      ),
    ).rejects.toThrow(
      "rate limit max_orders must be an integer greater than 0",
    );
    expect(fetch).not.toHaveBeenCalled();
  });

  it("upserts self-computed PnL limits through the typed endpoint", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        spotFundsPnlBoundsLimit: {
          scope: "account",
          account: "desk-alpha",
          currency: "USD",
          lowerBound: "-1000",
          upperBound: "500",
        },
      }),
    );

    const { putLimit } = api();
    const limit = await putLimit(
      {
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account",
        account: "desk-alpha",
        asset: "",
        values: {
          currency: "USD",
          lower_bound: "-1000",
          upper_bound: "500",
        },
      },
      "reject",
    );

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits/spot-funds-pnl-bounds?missingAccount=reject",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          scope: "account",
          account: "desk-alpha",
          accountGroup: "",
          currency: "USD",
          lowerBound: "-1000",
          upperBound: "500",
        }),
      }),
    );
    expect(limit).toEqual(
      expect.objectContaining({
        policy: "spot_funds_pnl_bounds_kill_switch",
        account: "desk-alpha",
        values: {
          currency: "USD",
          lower_bound: "-1000",
          upper_bound: "500",
        },
      }),
    );
  });

  it("deletes self-computed PnL limits with the account-group axis", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

    const { deleteLimit } = api();
    await deleteLimit({
      policy: "spot_funds_pnl_bounds_kill_switch",
      scope: "account_group",
      account: "",
      accountGroup: "desk-alpha",
      asset: "",
    });

    const calledUrl = new URL(
      String(vi.mocked(fetch).mock.calls[0][0]),
      "http://test",
    );
    expect(calledUrl.pathname).toBe("/app/api/v1/limits");
    expect(Object.fromEntries(calledUrl.searchParams.entries())).toEqual({
      policy: "spot_funds_pnl_bounds_kill_switch",
      scope: "account_group",
      accountGroup: "desk-alpha",
    });
  });
});

describe("append-only list clients", () => {
  it("normalizes adjustments, trades, and audit keyset envelopes", async () => {
    const officerApi = api();
    vi.mocked(fetch)
      .mockResolvedValueOnce(
        jsonResponse({
          adjustments: [
            {
              id: "adj-1",
              account: "desk-alpha",
              at: "2026-06-24T00:00:00Z",
              source: "panel",
              asset: "USD",
              status: "accepted",
              request: { asset: "USD", realizedPnl: "-12.50" },
              outcome: {
                accepted: {
                  realizedPnlResult: { delta: "-12.50", result: "-12.50" },
                  RealizedPnlHaltReason: "arithmetic_overflow",
                },
              },
            },
          ],
          total: 3,
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          trades: [
            {
              id: "trd-1",
              order: "ord-1",
              account: "desk-alpha",
              at: "2026-06-24T00:00:00Z",
              source: "panel",
              baseAsset: "AAPL",
              quoteAsset: "USD",
              side: "buy",
              quantity: "10",
              price: "100",
              lockPrice: "99",
            },
            {
              id: "trd-legacy-order-alias",
              orderExternalId: "ord-legacy",
            },
          ],
          total: 4,
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          entries: [
            {
              id: "aud-1",
              at: "2026-06-24T00:00:00Z",
              actor: "operator",
              actorTitle: "Operator",
              action: "block",
              account: "desk-alpha",
              accountTitle: "Desk Alpha",
              detail: "blocked",
              source: "panel",
            },
          ],
          total: 5,
        }),
      );

    const adjustments = await officerApi.fetchAdjustmentsPage({
      id: "adj-1",
      account: "desk-alpha",
      accountMatch: "contains",
      asset: "USD",
      assetMatch: "exact",
      source: "panel",
      status: "accepted",
      atMode: "after",
      atMin: "2026-06-24T00:00:00Z",
      sort: "at",
      order: "desc",
      offset: 100,
      limit: 50,
    });
    const trades = await officerApi.fetchTradesPage({
      id: "trd-1",
      account: "desk-alpha",
      baseAsset: "AAP",
      quoteAsset: "USD",
      side: "buy",
      source: "panel",
      atMode: "between",
      atMin: "2026-06-24T00:00:00Z",
      atMax: "2026-06-25T00:00:00Z",
      quantityMode: "gte",
      quantityMin: "10",
      priceMode: "lt",
      priceMin: "150",
      lockPriceMode: "neq",
      lockPriceMin: "99",
      sort: "at",
      order: "desc",
      offset: 50,
      limit: 25,
    });
    const audit = await officerApi.fetchAuditPage({
      id: "aud-1",
      account: "desk-alpha",
      accountMatch: "contains",
      actor: "operator",
      actorMatch: "contains",
      source: "panel",
      category: "control",
      actions: ["block", "unblock"],
      atMode: "before",
      atMax: "2026-06-25T00:00:00Z",
      offset: 20,
      limit: 10,
    });

    expect(adjustments).toMatchObject({
      total: 3,
      items: [
        {
          id: "adj-1",
          accepted: {
            realizedPnlResult: { delta: "-12.50", result: "-12.50" },
            realizedPnlHaltReason: "arithmetic_overflow",
          },
        },
      ],
    });
    expect(trades).toMatchObject({
      total: 4,
      items: [{ id: "trd-1" }, { id: "trd-legacy-order-alias", order: "" }],
    });
    expect(audit).toMatchObject({
      total: 5,
      items: [{ id: "aud-1" }],
    });

    const adjustmentUrl = new URL(
      String(vi.mocked(fetch).mock.calls[0][0]),
      "http://test",
    );
    const tradeUrl = new URL(
      String(vi.mocked(fetch).mock.calls[1][0]),
      "http://test",
    );
    const auditUrl = new URL(
      String(vi.mocked(fetch).mock.calls[2][0]),
      "http://test",
    );
    expect(adjustmentUrl.searchParams.get("offset")).toBe("100");
    expect(adjustmentUrl.searchParams.get("id")).toBe("adj-1");
    expect(adjustmentUrl.searchParams.get("status")).toBe("accepted");
    expect(tradeUrl.searchParams.get("quantityMode")).toBe("gte");
    expect(tradeUrl.searchParams.get("offset")).toBe("50");
    expect(tradeUrl.searchParams.get("id")).toBe("trd-1");
    expect(tradeUrl.searchParams.get("lockPriceMode")).toBe("neq");
    expect(tradeUrl.searchParams.get("accountMatch")).toBeNull();
    expect(tradeUrl.searchParams.get("baseAssetMatch")).toBeNull();
    expect(tradeUrl.searchParams.get("quoteAssetMatch")).toBeNull();
    expect(auditUrl.searchParams.get("actions")).toBe("block,unblock");
    expect(auditUrl.searchParams.get("category")).toBe("control");
    expect(auditUrl.searchParams.get("id")).toBe("aud-1");
    expect(auditUrl.searchParams.get("offset")).toBe("20");
    expect(auditUrl.searchParams.get("sort")).toBeNull();
    expect(auditUrl.searchParams.get("order")).toBeNull();
  });

  it("returns null when an adjustment produces no engine-side change", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(null, { status: 204 }));

    const result = await api().createAdjustment(
      "desk-alpha",
      { asset: "USD" },
      "reject",
    );

    expect(result).toBeNull();
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts/desk-alpha/adjustments?missingAccount=reject",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("resends an adjustment with missingAccount=create after account creation is confirmed", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(null, { status: 204 }));

    await api().createAdjustment("desk-alpha", { asset: "USD" }, "create");

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts/desk-alpha/adjustments?missingAccount=create",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("parses an account_missing 404 into a typed ApiError carrying the account code", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          error: {
            code: "account_missing",
            message: 'block account: account "acc-1" does not exist',
            account: "acc-1",
          },
        }),
        { status: 404, headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(
      api().createAdjustment("acc-1", { asset: "USD" }, "reject"),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "account_missing",
      account: "acc-1",
      status: 404,
    });
  });
});

describe("backup client", () => {
  it("exports a backup and parses RFC 5987 filenames", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify(backupArchive), {
        status: 200,
        headers: {
          "Content-Type": "application/json",
          "Content-Disposition":
            "attachment; filename*=UTF-8''pit%20backup.json",
        },
      }),
    );

    const signal = new AbortController().signal;
    const result = await exportBackup({ all: true }, signal);

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/backup/export",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({
          Accept: "application/json",
        }),
        body: JSON.stringify({ scope: { all: true } }),
        signal,
      }),
    );
    expect(result.filename).toBe("pit backup.json");
    expect(result.blob.size).toBe(JSON.stringify(backupArchive).length);
  });

  it("requests a zipped backup when enabled", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response("zip-bytes", {
        status: 200,
        headers: {
          "Content-Type": "application/zip",
          "Content-Disposition": 'attachment; filename="pit backup.zip"',
        },
      }),
    );

    const result = await exportBackup({ all: true }, undefined, true);

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/backup/export",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({
          Accept: "application/zip",
        }),
        body: JSON.stringify({ scope: { all: true }, zip: true }),
      }),
    );
    expect(result.filename).toBe("pit backup.zip");
  });

  it("uses a json fallback filename for unzipped backups without a header", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify(backupArchive), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    const result = await exportBackup({ all: true });

    expect(result.filename).toBe("pit-officer-backup.json");
  });

  it("uses a zip fallback filename for zipped backups without a header", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response("zip-bytes", {
        status: 200,
        headers: { "Content-Type": "application/zip" },
      }),
    );

    const result = await exportBackup({ all: true }, undefined, true);

    expect(result.filename).toBe("pit-officer-backup.zip");
  });

  it("surfaces export HTTP errors", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "validation", message: "bad scope" },
        }),
        {
          status: 400,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );

    await expect(exportBackup({ all: false })).rejects.toThrow("bad scope");
  });

  it("surfaces export network errors", async () => {
    vi.mocked(fetch).mockRejectedValue(new Error("connection refused"));

    await expect(exportBackup({ all: true })).rejects.toThrow(
      "connection refused",
    );
  });

  it("restores with a typed mode and normalizes the summary", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        summary: {
          applied: { accounts_groups: 1 },
          skipped: {},
          restartRequired: true,
        },
      }),
    );

    const signal = new AbortController().signal;
    const result = await restoreBackup({
      archive: backupArchive,
      scope: { all: true },
      mode: "overwrite",
      signal,
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/backup/restore",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          scope: { all: true },
          mode: "overwrite",
          archive: backupArchive,
        }),
        signal,
      }),
    );
    expect(result.applied.accounts_groups).toBe(1);
    expect(result.restartRequired).toBe(true);
  });

  it("restores from an uploaded backup file payload", async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ summary: {} }));

    await restoreBackup({
      archiveFile: { base64: "UEsDBA==", filename: "pit backup.zip" },
      scope: { all: true },
      mode: "overwrite",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/backup/restore",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          scope: { all: true },
          mode: "overwrite",
          archiveFile: "UEsDBA==",
          archiveFilename: "pit backup.zip",
        }),
      }),
    );
  });

  it("defaults missing restore summary maps", async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ summary: {} }));

    const result = await restoreBackup({
      archive: backupArchive,
      scope: { all: true },
      mode: "insert_missing",
    });

    expect(result.applied).toEqual({});
    expect(result.skipped).toEqual({});
    expect(result.restartRequired).toBe(false);
  });

  it("rejects a non-object restore response distinctly from bad summaries", async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse(null));

    await expect(
      restoreBackup({
        archive: backupArchive,
        scope: { all: true },
        mode: "insert_missing",
      }),
    ).rejects.toThrow("Invalid backup restore response.");
  });

  it("resets the database with explicit confirmation", async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ ok: true }));

    await resetDatabase();

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/database/reset",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ confirm: true }),
      }),
    );
  });

  it("requests service restart", async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ accepted: true }));

    await restartService();

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/service/restart",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("requests service stop", async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ accepted: true }));

    await stopService();

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/service/stop",
      expect.objectContaining({ method: "POST" }),
    );
  });
});

describe("market-data client settings payloads", () => {
  it("normalizes the synthetic inverse flag to a required boolean", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        marketData: {
          providers: [],
          instances: [
            {
              id: "ib-1",
              provider: "ib",
              label: "IB Gateway",
              enabled: true,
              instruments: [
                {
                  externalSymbol: "EURUSD",
                  syntheticInverse: true,
                },
                { externalSymbol: "AAPL" },
              ],
            },
          ],
        },
      }),
    );

    const result = await api().fetchMarketData();

    expect(result.instances[0]?.instruments).toMatchObject([
      { externalSymbol: "EURUSD", syntheticInverse: true },
      { externalSymbol: "AAPL", syntheticInverse: false },
    ]);
  });

  it("sends provider credentials when creating an IB source", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        instance: {
          id: "md-created",
          provider: "ib",
          label: "IB Gateway",
          credentials: "",
          enabled: true,
          state: "",
          verifiesSymbols: false,
          searchesSymbols: false,
          instruments: [],
          diagnostics: [],
        },
      }),
    );

    const instance = await createMarketDataInstance({
      provider: "ib",
      label: "IB Gateway",
      credentials: JSON.stringify({
        host: "127.0.0.1",
        port: 7496,
        clientId: 7,
      }),
      enabled: true,
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          provider: "ib",
          label: "IB Gateway",
          credentials: '{"host":"127.0.0.1","port":7496,"clientId":7}',
          enabled: true,
        }),
      }),
    );
    expect(instance.id).toBe("md-created");
  });

  it("sends blank secret placeholders on settings update", async () => {
    await updateMarketDataInstanceSettings("alpaca-1", {
      label: "Alpaca",
      credentials: JSON.stringify({
        apiKey: "new-key",
        apiSecret: "",
      }),
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/alpaca-1/settings",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          label: "Alpaca",
          credentials: '{"apiKey":"new-key","apiSecret":""}',
        }),
      }),
    );
  });
});

describe("searchMarketDataSymbols", () => {
  it("posts the resolve criteria and normalizes the backend top-level response", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        supported: true,
        matches: [
          {
            symbol: "BTC",
            name: "Bitcoin",
            secType: "CRYPTO",
            exchange: "PAXOS",
            currency: "USD",
            conId: "12345",
            localSymbol: "BTC.USD",
            tradingClass: "BTC",
          },
        ],
      }),
    );

    const result = await searchMarketDataSymbols("ib-1", {
      query: "BTC",
      secType: "CRYPTO",
      exchange: "PAXOS",
      currency: "USD",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/ib-1/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          query: "BTC",
          secType: "CRYPTO",
          exchange: "PAXOS",
          currency: "USD",
        }),
      }),
    );
    expect(result.supported).toBe(true);
    expect(result.matches).toEqual([
      {
        symbol: "BTC",
        name: "Bitcoin",
        secType: "CRYPTO",
        exchange: "PAXOS",
        currency: "USD",
        conId: "12345",
        localSymbol: "BTC.USD",
        tradingClass: "BTC",
      },
    ]);
  });

  it("tolerates the older wrapped response shape", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        search: {
          supported: true,
          matches: [
            {
              symbol: "ES",
              secType: "FUT",
              exchange: "CME",
              currency: "USD",
              conId: "495512563",
              lastTradeDateOrContractMonth: "20251219",
              multiplier: "50",
              tradingClass: "ES",
            },
          ],
        },
      }),
    );

    const result = await searchMarketDataSymbols("ib-1", {
      query: "ES",
      secType: "FUT",
      exchange: "CME",
      currency: "USD",
      lastTradeDateOrContractMonth: "20251219",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/ib-1/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          query: "ES",
          secType: "FUT",
          exchange: "CME",
          currency: "USD",
          lastTradeDateOrContractMonth: "20251219",
        }),
      }),
    );
    expect(result.matches[0]).toMatchObject({
      symbol: "ES",
      secType: "FUT",
      exchange: "CME",
      conId: "495512563",
      lastTradeDateOrContractMonth: "20251219",
      multiplier: "50",
    });
  });

  it("posts a decimal-string strike and omits empty optionals", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ search: { supported: true, matches: [] } }),
    );

    await searchMarketDataSymbols("ib-1", {
      query: "SPX",
      secType: "OPT",
      exchange: "",
      currency: "USD",
      strike: "5000.00",
      right: "C",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/ib-1/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          query: "SPX",
          secType: "OPT",
          currency: "USD",
          right: "C",
          strike: "5000.00",
        }),
      }),
    );
  });

  it("omits all optionals when only the query is given", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ search: { supported: true, matches: [] } }),
    );

    await searchMarketDataSymbols("ib-1", { query: "msft" });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/ib-1/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ query: "msft" }),
      }),
    );
  });

  it("tolerates missing optional fields and an unsupported result", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        search: {
          supported: false,
          matches: [{ symbol: "X", secType: "STK" }],
        },
      }),
    );

    const result = await searchMarketDataSymbols("byo-1", { query: "x" });

    expect(result.supported).toBe(false);
    expect(result.matches).toEqual([{ symbol: "X", secType: "STK" }]);
  });

  it("carries a decimal-string strike through verbatim in a match", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        supported: true,
        matches: [
          {
            symbol: "SPX",
            secType: "OPT",
            exchange: "CBOE",
            currency: "USD",
            strike: "5000.00",
            right: "C",
          },
        ],
      }),
    );

    const result = await searchMarketDataSymbols("ib-1", {
      query: "SPX",
      secType: "OPT",
    });

    expect(result.matches[0].strike).toBe("5000.00");
  });

  it("omits strike from a match when the wire value is a number", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        supported: true,
        matches: [
          {
            symbol: "SPX",
            secType: "OPT",
            exchange: "CBOE",
            currency: "USD",
            strike: 5000,
            right: "C",
          },
        ],
      }),
    );

    const result = await searchMarketDataSymbols("ib-1", {
      query: "SPX",
      secType: "OPT",
    });

    // A numeric strike must never be coerced to string (lossy for decimals
    // like 5000.00 → "5000"). The field must be absent on the normalized
    // match instead.
    expect(result.matches[0].strike).toBeUndefined();
  });
});

describe("verifyMarketDataSymbol", () => {
  it("normalizes provider details for an existing symbol", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        verification: {
          supported: true,
          exists: true,
          details: "BTC DIGITAL LTD, Common Stock",
        },
      }),
    );

    const result = await verifyMarketDataSymbol("finnhub-1", "BTC");

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/finnhub-1/verify-symbol",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ externalSymbol: "BTC" }),
      }),
    );
    expect(result).toEqual({
      supported: true,
      exists: true,
      details: "BTC DIGITAL LTD, Common Stock",
    });
  });
});

// ---------------------------------------------------------------------------
// Signing keys — normalizer unit tests
// ---------------------------------------------------------------------------

describe("normalizeSigningKeysStatus", () => {
  it("normalizes camelCase wire shape", () => {
    const result = normalizeSigningKeysStatus({
      keys: [
        {
          keyId: "key-1",
          fingerprint: "aa:bb",
          createdAt: "2026-01-01T00:00:00Z",
          active: true,
        },
      ],
      config: { noESign: false },
    });
    expect(result.keys).toHaveLength(1);
    expect(result.keys[0].keyId).toBe("key-1");
    expect(result.keys[0].active).toBe(true);
    expect(result.eSignEnabled).toBe(true);
  });

  it("normalizes snake_case wire shape", () => {
    const result = normalizeSigningKeysStatus({
      keys: [
        {
          key_id: "key-2",
          fingerprint: "cc:dd",
          created_at: "2026-02-01T00:00:00Z",
          active: false,
        },
      ],
      config: { no_esign: "1" },
    });
    expect(result.keys[0].keyId).toBe("key-2");
    expect(result.keys[0].active).toBe(false);
    expect(result.eSignEnabled).toBe(false);
  });

  it("returns empty keys and eSignEnabled=true for empty input", () => {
    const result = normalizeSigningKeysStatus({});
    expect(result.keys).toHaveLength(0);
    expect(result.eSignEnabled).toBe(true);
  });

  it("tolerates PascalCase wire shape", () => {
    const result = normalizeSigningKeysStatus({
      keys: [
        {
          KeyId: "key-3",
          Fingerprint: "ee:ff",
          CreatedAt: "2026-03-01T00:00:00Z",
          Active: true,
        },
      ],
      config: { NoESign: false },
    });
    expect(result.keys[0].keyId).toBe("key-3");
    expect(result.eSignEnabled).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Signing keys — HTTP call tests
// ---------------------------------------------------------------------------

describe("signing-keys HTTP calls", () => {
  function signingKeysResponse(): Response {
    return new Response(
      JSON.stringify({
        keys: [
          {
            keyId: "k1",
            fingerprint: "11:22",
            createdAt: "2026-01-01T00:00:00Z",
            active: true,
          },
        ],
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }

  function signingConfigResponse(): Response {
    return new Response(JSON.stringify({ noESign: false }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }

  function generateResponse(): Response {
    return new Response(
      JSON.stringify({
        key: {
          keyId: "new-key",
          fingerprint: "33:44",
          createdAt: "2026-01-01T00:00:00Z",
          active: true,
        },
        publicKey: "-----BEGIN PUBLIC KEY-----\ntest\n-----END PUBLIC KEY-----",
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }

  it("fetchSigningKeys calls GET /signing/keys and GET /signing/config", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(signingKeysResponse())
      .mockResolvedValueOnce(signingConfigResponse());
    const result = await fetchSigningKeys();
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/signing/keys",
      expect.any(Object),
    );
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/signing/config",
      expect.any(Object),
    );
    expect(result.keys).toHaveLength(1);
    expect(result.eSignEnabled).toBe(true);
  });

  it("generateSigningKey calls POST /signing/keys/generate", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(generateResponse());
    const result = await generateSigningKey();
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/signing/keys/generate",
      expect.objectContaining({ method: "POST" }),
    );
    expect(result.key.keyId).toBe("new-key");
    expect(result.publicKey).toContain("BEGIN PUBLIC KEY");
  });

  it("importSigningKey calls POST /signing/keys/import with key and format", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(generateResponse());
    await importSigningKey("-----BEGIN PRIVATE KEY-----\ntest", "pem-pkcs8");
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/signing/keys/import",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          key: "-----BEGIN PRIVATE KEY-----\ntest",
          format: "pem-pkcs8",
        }),
      }),
    );
  });

  it("preserves signing error codes", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(
        JSON.stringify({ error: { code: "signing", message: "bad key" } }),
        { status: 400, headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(importSigningKey("bad", "pem-pkcs8")).rejects.toMatchObject({
      code: "signing",
      message: "bad key",
    });
  });

  it("setESignEnabled(true) sends noESign=false", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    await setESignEnabled(true);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/signing/config",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ noESign: false }),
      }),
    );
  });

  it("setESignEnabled(false) sends noESign=true", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    await setESignEnabled(false);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/signing/config",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ noESign: true }),
      }),
    );
  });
});

// ---------------------------------------------------------------------------
// Order reproduction + per-key public material
// ---------------------------------------------------------------------------

describe("event reproduction client", () => {
  it("fetchEventReproduction normalizes a signed submit bundle verbatim", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        requestType: "submit",
        event: {
          id: "evt-1",
          order: "ord-1",
          at: "2026-06-24T00:00:00Z",
          type: "submitted",
          source: "api",
          alg: "ed25519",
          signed: true,
        },
        attestation: {
          token: "tok-verbatim",
          keyId: "key-1",
          alg: "ed25519",
          requestType: "submit",
          mode: "immediate",
          issuedAt: "2026-06-24T00:00:00Z",
          signed: true,
        },
        request: {
          requestType: "submit",
          orderId: "ord-1",
          instrument: "AAPL/USD",
          side: "buy",
          quantity: "10",
          amountKind: "quantity",
          orderType: "limit",
          limitPrice: "150.25",
          accountId: "acc-1",
          verdict: "reject",
          result: null,
        },
        response: {
          submitResponse: {
            token: "tok-verbatim",
            keyId: "key-1",
            id: "ord-1",
            verdict: "reject",
            reasons: [
              {
                code: "max_order_size",
                scope: "order",
                policy: "order-size",
                reason: "order too large",
                details: "qty=100",
              },
            ],
          },
        },
        canonicalApproval: '{"version":1,"side":"buy"}',
        publicKey: {
          keyId: "key-1",
          alg: "ed25519",
          format: "pem-pkcs8",
          key: "PEM",
        },
        eSign: { alg: "ed25519", noESign: false, signed: true },
        signature: "sig-base64",
        reason: "",
      }),
    );

    const result = await fetchEventReproduction("ord-1", "evt-1");
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/ord-1/events/evt-1/reproduction",
      expect.any(Object),
    );
    // Signed artifacts are carried through verbatim.
    expect(result.requestType).toBe("submit");
    expect(result.attestation?.token).toBe("tok-verbatim");
    expect(result.response?.submitResponse?.token).toBe("tok-verbatim");
    expect(result.response?.submitResponse?.verdict).toBe("reject");
    expect(result.response?.submitResponse?.reasons).toEqual([
      {
        code: "max_order_size",
        scope: "order",
        policy: "order-size",
        reason: "order too large",
        details: "qty=100",
      },
    ]);
    expect(result.canonicalApproval).toBe('{"version":1,"side":"buy"}');
    expect(result.signature).toBe("sig-base64");
    expect(result.publicKey?.keyId).toBe("key-1");
    expect(result.event.id).toBe("evt-1");
    expect(result.event.order).toBe("ord-1");
    expect(result.event.signed).toBe(true);
    expect(result.eSign).toEqual({
      alg: "ed25519",
      noESign: false,
      signed: true,
    });
  });

  it("fetchEventReproduction ignores legacy order aliases", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        event: {
          id: "evt-legacy-order-alias",
          orderExternalId: "ord-legacy",
        },
      }),
    );

    const result = await fetchEventReproduction(
      "ord-1",
      "evt-legacy-order-alias",
    );

    expect(result.event.id).toBe("evt-legacy-order-alias");
    expect(result.event.order).toBe("");
  });

  it("fetchEventReproduction normalizes an execution-report facet with its verbatim token", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        requestType: "execution_report",
        event: {
          id: "evt-fill",
          order: "ord-1",
          at: "2026-06-24T00:01:00Z",
          type: "fill",
          source: "api",
          fillQuantity: "10",
          fillPrice: "150.25",
          leavesQuantity: "0",
          orderStatus: "filled",
          commission: { amount: "-0.50", currency: "USD" },
          alg: "ed25519",
          signed: true,
        },
        attestation: {
          token: "tok-fill",
          keyId: "key-1",
          alg: "ed25519",
          requestType: "execution_report",
          mode: "immediate",
          issuedAt: "2026-06-24T00:01:00Z",
          signed: true,
        },
        request: {
          requestType: "execution_report",
          orderId: "ord-1",
          eventId: "evt-fill",
          accountId: "acc-1",
          executionReport: {
            baseAsset: "AAPL",
            quoteAsset: "USD",
            fillQuantity: "10",
            fillPrice: "150.25",
            leavesQuantity: "0",
            lockPrice: "149.75",
            lock: "bG9jaw==",
            commission: { amount: "-0.50", currency: "USD" },
            order: "ord-1",
            account: "acc-1",
            side: "buy",
            orderStatus: "filled",
            force: false,
          },
          result: {
            outcome: "filled",
            fillQuantity: "10",
            fillPrice: "150.25",
            fillLockPrice: "149.75",
            commission: { amount: "-0.50", currency: "USD" },
            leavesQuantity: "0",
            orderStatus: "filled",
            blocks: [
              {
                account: "acc-1",
                policy: "spot_funds_pnl_bounds_kill_switch",
                code: "daily_loss",
                reason: "blocked",
                details: "limit=-1000",
              },
            ],
          },
        },
        response: {
          executionReport: {
            result: {
              blocks: [
                {
                  account: "acc-1",
                  policy: "spot_funds_pnl_bounds_kill_switch",
                  code: "daily_loss",
                  reason: "blocked",
                  details: "limit=-1000",
                },
              ],
              outcomes: [
                {
                  asset: "AAPL",
                  balanceDelta: "10",
                  balanceResult: "10",
                  heldDelta: "0",
                  heldResult: "0",
                  incomingDelta: "0",
                  incomingResult: "0",
                },
              ],
            },
            attestationToken: "tok-fill",
            attestationKeyId: "key-1",
            signed: true,
          },
        },
        canonicalApproval: '{"version":1,"requestType":"execution_report"}',
        publicKey: {
          keyId: "key-1",
          alg: "ed25519",
          format: "pem-pkcs8",
          key: "PEM",
        },
        eSign: { alg: "ed25519", noESign: false, signed: true },
        signature: "sig-fill",
        reason: "",
      }),
    );

    const result = await fetchEventReproduction("ord-1", "evt-fill");
    expect(result.requestType).toBe("execution_report");
    expect(result.event).toMatchObject({
      fillQuantity: "10",
      fillPrice: "150.25",
      leavesQuantity: "0",
      orderStatus: "filled",
      commission: { amount: "-0.50", currency: "USD" },
    });
    expect(result.response?.executionReport?.attestationToken).toBe("tok-fill");
    // The per-asset outcomes surface through the reproduction facet verbatim.
    expect(result.response?.executionReport?.outcomes).toEqual([
      {
        asset: "AAPL",
        balanceDelta: "10",
        balanceResult: "10",
        heldDelta: "0",
        heldResult: "0",
        incomingDelta: "0",
        incomingResult: "0",
        realizedPnlDelta: "",
        realizedPnlResult: "",
        averageEntryPrice: "",
      },
    ]);
    expect(result.request?.result?.orderStatus).toBe("filled");
    expect(result.request?.executionReport).toMatchObject({
      lockPrice: "149.75",
      commission: { amount: "-0.50", currency: "USD" },
      force: false,
    });
    expect(result.request?.executionReport).not.toHaveProperty("lock");
    expect(result.request?.result).toMatchObject({
      fillLockPrice: "149.75",
      commission: { amount: "-0.50", currency: "USD" },
      blocks: [
        expect.objectContaining({
          policy: "spot_funds_pnl_bounds_kill_switch",
          code: "daily_loss",
        }),
      ],
    });
    expect(result.response?.executionReport?.blocks).toEqual([
      {
        account: "acc-1",
        policy: "spot_funds_pnl_bounds_kill_switch",
        code: "daily_loss",
        reason: "blocked",
        details: "limit=-1000",
      },
    ]);
    expect(result.response?.submitResponse).toBeNull();
  });

  it("fetchEventReproduction keeps a null canonicalApproval distinct from empty", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        requestType: "",
        event: { id: "evt-bare", signed: false },
        attestation: null,
        request: null,
        response: null,
        canonicalApproval: null,
        publicKey: null,
        eSign: { alg: "", noESign: false, signed: false },
        signature: "",
        reason: "event has no persisted attestation envelope",
      }),
    );

    const result = await fetchEventReproduction("ord-bare", "evt-bare");
    expect(result.attestation).toBeNull();
    expect(result.request).toBeNull();
    expect(result.canonicalApproval).toBeNull();
    expect(result.publicKey).toBeNull();
    expect(result.reason).toBe("event has no persisted attestation envelope");
  });

  it("fetchPublicKeyById requests the per-key endpoint with format", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        keyId: "key-9",
        alg: "ed25519",
        format: "raw-base64",
        key: "RAW-BASE64",
      }),
    );

    const result = await fetchPublicKeyById("key-9", "raw-base64");
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/signing/keys/key-9/public?format=raw-base64",
      expect.any(Object),
    );
    expect(result).toEqual({
      keyId: "key-9",
      alg: "ed25519",
      format: "raw-base64",
      key: "RAW-BASE64",
    });
  });

  it("fetchPublicKeyById surfaces a 404 for an unknown key id", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          error: { code: "not_found", message: "unknown key" },
        }),
        { status: 404, headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(
      fetchPublicKeyById("missing", "raw-base64"),
    ).rejects.toMatchObject({ status: 404 });
  });
});

describe("order check client", () => {
  it("preserves the policy on a dry-run wouldBlock", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        check: {
          passed: false,
          rejects: [],
          wouldDisplayPrice: "",
          wouldBlock: {
            account: "acc-1",
            policy: "spot_funds_pnl_bounds_kill_switch",
            code: "pnl_bound_breached",
            reason: "lower bound breached",
            details: "realized=-1500,lower=-1000",
          },
        },
      }),
    );

    const result = await checkOrder({
      account: "acc-1",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "10",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/check",
      expect.objectContaining({ method: "POST" }),
    );
    expect(result.wouldBlock).toEqual({
      account: "acc-1",
      policy: "spot_funds_pnl_bounds_kill_switch",
      code: "pnl_bound_breached",
      reason: "lower bound breached",
      details: "realized=-1500,lower=-1000",
    });
  });
});

// ---------------------------------------------------------------------------
// Orders — submit creates a signed approval token and addresses by external id
// ---------------------------------------------------------------------------

describe("Orders createOrder submit lifecycle", () => {
  function approvalResponse(
    orderExternalId = "ord_alpha_0000000001",
  ): Response {
    return new Response(
      JSON.stringify({
        token: "approval-token",
        keyId: "key-1",
        id: orderExternalId,
        verdict: "accept",
      }),
      { status: 201, headers: { "Content-Type": "application/json" } },
    );
  }

  function wrappedApprovalResponse(
    orderExternalId = "ord_alpha_0000000001",
  ): Response {
    return new Response(
      JSON.stringify({
        approval: {
          token: "approval-token",
          keyId: "key-1",
          id: orderExternalId,
          verdict: "accept",
        },
      }),
      { status: 201, headers: { "Content-Type": "application/json" } },
    );
  }

  function orderResponse(
    orderExternalId = "ord_alpha_0000000001",
    dropCopy = false,
  ): Response {
    return new Response(
      JSON.stringify({
        order: {
          id: orderExternalId,
          account: "desk-alpha",
          at: "2026-01-01T00:00:00Z",
          source: "panel",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "100",
          leavesQuantity: "100",
          price: "0",
          status: "accepted",
          displayPrice: "",
          dropCopy,
        },
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }

  it("submits through /orders/submit and fetches the created external id", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(approvalResponse())
      .mockResolvedValueOnce(orderResponse());
    const { createOrder } = api();
    const result = await createOrder(
      {
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
      },
      "reject",
    );
    expect(result.approval).toMatchObject({
      token: "approval-token",
      keyId: "key-1",
      id: "ord_alpha_0000000001",
      verdict: "accept",
      reasons: [],
    });
    expect(fetch).toHaveBeenNthCalledWith(
      1,
      "/app/api/v1/orders/submit?missingAccount=reject",
      expect.objectContaining({
        body: JSON.stringify({
          account: "desk-alpha",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "100",
        }),
      }),
    );
    expect(fetch).toHaveBeenNthCalledWith(
      2,
      "/app/api/v1/orders/ord_alpha_0000000001",
      expect.any(Object),
    );
  });

  it("routes drop-copy orders through the distinct endpoint", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            id: "ord_drop_copy_000001",
            status: "committed",
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(orderResponse("ord_drop_copy_000001", true));
    const { createOrder } = api();

    const result = await createOrder(
      {
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
        id: "ord_drop_copy_000001",
        mode: "drop_copy",
      },
      "create",
    );

    expect(result.order.dropCopy).toBe(true);
    expect(fetch).toHaveBeenNthCalledWith(
      1,
      "/app/api/v1/orders/drop-copy/submit?missingAccount=create",
      expect.objectContaining({
        body: JSON.stringify({
          account: "desk-alpha",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "100",
          id: "ord_drop_copy_000001",
        }),
      }),
    );
    expect(result.approval).toBeUndefined();
  });

  it("keeps the drop-copy status when detail enrichment fails", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            id: "ord_drop_copy_fallback",
            status: "committed",
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { message: "store down" } }), {
          status: 500,
          headers: { "Content-Type": "application/json" },
        }),
      );
    const { createOrder } = api();

    const result = await createOrder(
      {
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
        price: "10",
        id: "ord_drop_copy_fallback",
        mode: "drop_copy",
      },
      "create",
    );

    expect(result.order).toMatchObject({
      id: "ord_drop_copy_fallback",
      status: "committed",
      source: "panel",
      leavesQuantity: "100",
      dropCopy: true,
      signed: false,
    });
    expect(result.approval).toBeUndefined();
    expect(result.warning).toContain(
      "Order was created, but detail enrichment failed",
    );
  });

  it("sends caller-supplied id and hold mode", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(approvalResponse("ord_supplied_000001"))
      .mockResolvedValueOnce(orderResponse("ord_supplied_000001"));
    const controller = new AbortController();
    const { createOrder } = api();
    await createOrder(
      {
        id: "ord_supplied_000001",
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
        mode: "hold",
      },
      "reject",
      controller.signal,
    );
    expect(fetch).toHaveBeenNthCalledWith(
      1,
      "/app/api/v1/orders/submit?missingAccount=reject",
      expect.objectContaining({
        body: JSON.stringify({
          id: "ord_supplied_000001",
          account: "desk-alpha",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "100",
          mode: "hold",
        }),
        signal: controller.signal,
      }),
    );
    expect(fetch).toHaveBeenNthCalledWith(
      2,
      "/app/api/v1/orders/ord_supplied_000001",
      expect.objectContaining({
        signal: controller.signal,
      }),
    );
  });

  it("normalizes wrapped approval tokens", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(wrappedApprovalResponse("ord_wrapped_0000001"))
      .mockResolvedValueOnce(orderResponse("ord_wrapped_0000001"));
    const { createOrder } = api();
    const result = await createOrder(
      {
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
      },
      "reject",
    );
    expect(result.order.id).toBe("ord_wrapped_0000001");
    expect(fetch).toHaveBeenNthCalledWith(
      2,
      "/app/api/v1/orders/ord_wrapped_0000001",
      expect.any(Object),
    );
  });

  it("normalizes submitResponse verdict and reasons", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            order: { id: "ord_rejected_0000001" },
            submitResponse: {
              token: "reject-token",
              keyId: "key-1",
              id: "ord_rejected_0000001",
              verdict: "reject",
              reasons: [
                {
                  code: "max_order_size",
                  scope: "order",
                  policy: "order-size",
                  reason: "order too large",
                  details: "qty=100",
                },
              ],
            },
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(orderResponse("ord_rejected_0000001"));
    const { createOrder } = api();
    const result = await createOrder(
      {
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
      },
      "reject",
    );
    expect(result.approval).toEqual({
      token: "reject-token",
      keyId: "key-1",
      id: "ord_rejected_0000001",
      verdict: "reject",
      reasons: [
        {
          code: "max_order_size",
          scope: "order",
          policy: "order-size",
          reason: "order too large",
          details: "qty=100",
        },
      ],
    });
  });

  it("keeps an empty submitResponse verdict", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            order: { id: "ord_legacy_0000001" },
            submitResponse: {
              token: "legacy-token",
              keyId: "key-1",
              id: "ord_legacy_0000001",
              verdict: "",
            },
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(orderResponse("ord_legacy_0000001"));
    const { createOrder } = api();
    const result = await createOrder(
      {
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
      },
      "reject",
    );
    expect(result.approval?.verdict).toBe("");
    expect(result.approval?.reasons).toEqual([]);
  });

  it("normalizes displayPrice from the fetched order", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(approvalResponse())
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            order: {
              id: "ord_alpha_0000000001",
              account: "desk-alpha",
              at: "2026-01-01T00:00:00Z",
              source: "panel",
              baseAsset: "AAPL",
              quoteAsset: "USD",
              side: "buy",
              amountKind: "quantity",
              amountValue: "100",
              leavesQuantity: "100",
              price: "0",
              status: "accepted",
              displayPrice: "101.20",
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    const { createOrder } = api();
    const result = await createOrder(
      {
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
      },
      "reject",
    );
    expect(result.order.displayPrice).toBe("101.20");
    expect(result.warning).toBeUndefined();
  });

  it("returns a created-order warning when detail enrichment fails", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(approvalResponse())
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { message: "store down" } }), {
          status: 500,
          headers: { "Content-Type": "application/json" },
        }),
      );
    const { createOrder } = api();
    const result = await createOrder(
      {
        account: "desk-alpha",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        side: "buy",
        amountKind: "quantity",
        amountValue: "100",
      },
      "reject",
    );

    expect(result.order).toMatchObject({
      id: "ord_alpha_0000000001",
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
      leavesQuantity: "100",
      price: "0",
      status: "submitted",
    });
    expect(result.warning).toContain(
      "Order was created, but detail enrichment failed",
    );
  });

  it("treats a fetched order without leavesQuantity as invalid", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        orders: [
          {
            id: "ord_alpha_0000000001",
            account: "desk-alpha",
            at: "2026-01-01T00:00:00Z",
            source: "panel",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            side: "buy",
            amountKind: "quantity",
            amountValue: "100",
            price: "0",
            status: "accepted",
            displayPrice: "",
          },
        ],
      }),
    );

    await expect(api().fetchOrders()).rejects.toMatchObject({
      code: "internal",
      message: "leavesQuantity is missing from the API response",
    });
  });
});

describe("Orders submitExecutionReport", () => {
  it("surfaces blocks, per-asset outcomes, and the attestation", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        id: "evt-report-1",
        result: {
          blocks: [
            {
              account: "desk-alpha",
              code: "spot_funds",
              policy: "spot_funds_pnl_bounds_kill_switch",
              reason: "insufficient funds",
              details: "USD",
            },
          ],
          outcomes: [
            {
              asset: "AAPL",
              balanceDelta: "10",
              balanceResult: "10",
              heldDelta: "0",
              heldResult: "0",
              incomingDelta: "0",
              incomingResult: "0",
              realized_pnl_halt_reason: "missing_cost_basis",
            },
            {
              asset: "USD",
              balanceDelta: "-1502.50",
              balanceResult: "8497.50",
              heldDelta: "0",
              heldResult: "0",
              incomingDelta: "0",
              incomingResult: "0",
              realizedPnlHaltReason: "missing_initial_pnl",
            },
          ],
        },
        attestationToken: "tok-report",
        attestationKeyId: "key-7",
        signed: true,
      }),
    );

    const result = await submitExecutionReport("ord_alpha_0000000001", {
      status: "filled",
      quantity: "10",
      price: "150.25",
      leavesQuantity: "0",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/ord_alpha_0000000001/execution-reports",
      expect.objectContaining({ method: "POST" }),
    );
    expect(result.blocks).toEqual([
      {
        account: "desk-alpha",
        code: "spot_funds",
        policy: "spot_funds_pnl_bounds_kill_switch",
        reason: "insufficient funds",
        details: "USD",
      },
    ]);
    expect(result.outcomes).toEqual([
      {
        asset: "AAPL",
        balanceDelta: "10",
        balanceResult: "10",
        heldDelta: "0",
        heldResult: "0",
        incomingDelta: "0",
        incomingResult: "0",
        realizedPnlDelta: "",
        realizedPnlResult: "",
        realizedPnlHaltReason: "missing_cost_basis",
        averageEntryPrice: "",
      },
      {
        asset: "USD",
        balanceDelta: "-1502.50",
        balanceResult: "8497.50",
        heldDelta: "0",
        heldResult: "0",
        incomingDelta: "0",
        incomingResult: "0",
        realizedPnlDelta: "",
        realizedPnlResult: "",
        realizedPnlHaltReason: "missing_initial_pnl",
        averageEntryPrice: "",
      },
    ]);
    expect(result.attestationToken).toBe("tok-report");
    expect(result.id).toBe("evt-report-1");
    expect(result.attestationKeyId).toBe("key-7");
    expect(result.signed).toBe(true);
  });
});

describe("Orders confirmOrder and cancelOrder", () => {
  function orderMutationResponse(): Response {
    return new Response(
      JSON.stringify({
        order: {
          id: "ord_alpha_0000000001",
          account: "desk-alpha",
          at: "2026-01-01T00:00:00Z",
          source: "panel",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "100",
          leavesQuantity: "0",
          price: "150.25",
          status: "filled",
          displayPrice: "",
        },
        attestationToken: "tok-confirm",
        attestationKeyId: "key-9",
        signed: true,
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }

  it("confirmOrder sends only the token and decodes the mutation response", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(orderMutationResponse());

    const result = await api().confirmOrder("ord_alpha_0000000001", {
      token: "approval-token",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/ord_alpha_0000000001/confirm",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ token: "approval-token" }),
      }),
    );
    expect(result.order).toMatchObject({
      id: "ord_alpha_0000000001",
      leavesQuantity: "0",
      price: "150.25",
      status: "filled",
    });
    expect(result.attestationToken).toBe("tok-confirm");
    expect(result.attestationKeyId).toBe("key-9");
    expect(result.signed).toBe(true);
  });

  it("cancelOrder sends token, leaves, and reason and decodes the mutation response", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(orderMutationResponse());

    const result = await api().cancelOrder("ord_alpha_0000000001", {
      token: "approval-token",
      leavesQuantity: "7.5",
      reason: "operator request",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/ord_alpha_0000000001/cancel",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          token: "approval-token",
          leavesQuantity: "7.5",
          reason: "operator request",
        }),
      }),
    );
    expect(result.order).toMatchObject({
      id: "ord_alpha_0000000001",
      leavesQuantity: "0",
      price: "150.25",
      status: "filled",
    });
    expect(result.attestationToken).toBe("tok-confirm");
    expect(result.attestationKeyId).toBe("key-9");
    expect(result.signed).toBe(true);
  });
});

describe("Orders conflict error decode", () => {
  function terminalOrderResponse(): Response {
    return new Response(
      JSON.stringify({
        error: {
          code: "terminal_order",
          message: "order is already in a terminal status",
        },
      }),
      { status: 409, headers: { "Content-Type": "application/json" } },
    );
  }

  function executionReportRequiredResponse(): Response {
    return new Response(
      JSON.stringify({
        error: {
          code: "execution_report_required",
          message:
            "order has execution-report activity; submit an explicit execution report",
        },
      }),
      { status: 409, headers: { "Content-Type": "application/json" } },
    );
  }

  it("confirmOrder surfaces execution_report_required on a 409", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(executionReportRequiredResponse());

    await expect(
      api().confirmOrder("ord_alpha_0000000001", {
        token: "approval-token",
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "execution_report_required",
      status: 409,
    });
  });

  it("cancelOrder surfaces execution_report_required on a 409", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(executionReportRequiredResponse());

    await expect(
      api().cancelOrder("ord_alpha_0000000001", {
        token: "approval-token",
        leavesQuantity: "7.5",
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "execution_report_required",
      status: 409,
    });
  });

  it("submitExecutionReport surfaces a typed terminal_order ApiError on a 409", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(terminalOrderResponse());

    await expect(
      submitExecutionReport("ord_alpha_0000000001", {
        status: "filled",
        quantity: "10",
        price: "150.25",
        leavesQuantity: "0",
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "terminal_order",
      status: 409,
    });
  });
});
