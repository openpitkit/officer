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

import { beforeEach, describe, expect, it, vi } from "vitest";
import { createInstance } from "i18next";

import { normalizeSigningKeysStatus } from "@/api/client";
import type {
  BackupArchive,
  FrameworkCredentialStorage,
  LimitTarget,
  TradingAccessSubject,
  TradingSnapshot,
  TestExecutionPolicy,
  VenueOrder,
} from "@/api/types";
import {
  ApiError,
  createApiClient,
  createOfficerApi,
  type ApiClient,
} from "@/framework";
import { appLocaleResources } from "@/i18n";
import { errorResponse, jsonResponse } from "@/test/apiClient";

describe("currency change diagnostics", () => {
  it.each(["en", "ru", "zh-CN", "es", "pt-BR"])(
    "localizes every reason in %s and preserves exact values",
    async (locale) => {
      const i18n = createInstance();
      await i18n.init({
        lng: locale,
        resources: appLocaleResources,
        interpolation: { escapeValue: false },
      });
      const reasons = [
        ...[
          "available",
          "held",
          "incoming",
          "average_entry_price",
          "realized_pnl",
          "balance_pnl_halt",
        ].map((kind) => ({
          account: "default",
          kind,
          asset: "USD",
          value:
            kind === "balance_pnl_halt"
              ? "missing_fx"
              : "1000.000000000000000001",
        })),
        {
          account: "default",
          kind: "account_pnl",
          value: "-123.456789123456789",
        },
        {
          account: "default",
          kind: "account_pnl_halt",
          value: "missing_account_currency",
        },
        {
          account: "default",
          kind: "active_order",
          order: "order-1",
          status: "submitted",
        },
        ...["account", "account_group", "global"].map((scope) => ({
          account: "default",
          kind: "pnl_limit",
          scope,
          group: "desk",
          axis: "realized",
          currency: "USD",
          lowerBound: "-10.000000000000000001",
        })),
      ];
      const fetch = vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: {
              code: "currency_change_blocked",
              message:
                "1 account(s) carry state that prevents a currency change: conflict",
              reasons,
            },
          }),
          { status: 409 },
        ),
      );
      const client = createOfficerApi(
        createApiClient({
          baseUrl: "/api/v1",
          fetch,
          translate: (key, params) => i18n.t(key, params),
        }),
      );
      const error = await client
        .setAccountCurrency("default", "USD")
        .catch((err: unknown) => err);
      expect(error).toBeInstanceOf(ApiError);
      const decoded = error as ApiError;
      expect(decoded.code).toBe("currency_change_blocked");
      expect(decoded.currencyChangeReasons).toEqual(reasons);
      expect(decoded.message).not.toContain("carry state");
      expect(decoded.message).not.toContain("{{");
      expect(decoded.message).not.toContain("undefined");
      expect(decoded.message).toContain("1000.000000000000000001");
      expect(decoded.message).toContain("order-1");
      expect(decoded.message).toContain("-10.000000000000000001");
      expect(decoded.message.split("\n")).toHaveLength(reasons.length + 1);
      if (locale === "ru") {
        expect(decoded.message).toContain(
          "доступный остаток USD = 1000.000000000000000001",
        );
        expect(decoded.message).toContain("лимит P&L группы desk");
        expect(decoded.message).toContain("верхняя граница не задана");
        expect(decoded.message).toContain("не задана валюта счёта");
        expect(decoded.message).toContain(
          "необходимая валютная котировка недоступна",
        );
        expect(decoded.message).not.toContain("missing_account_currency");
      }
    },
  );

  it("formats framework diagnostics in English without a supplied translator", async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "currency_change_blocked",
            message: "raw server text",
            reasons: [
              {
                account: "default",
                kind: "available",
                asset: "USD",
                value: "1.25",
              },
            ],
          },
        }),
        { status: 409 },
      ),
    );
    const client = createOfficerApi(
      createApiClient({ baseUrl: "/api/v1", fetch }),
    );
    await expect(client.setAccountCurrency("default", "USD")).rejects.toThrow(
      "Account default: available USD balance = 1.25.",
    );
  });

  it.each([
    [{ account: "default", kind: "available", value: "1" }],
    [{ account: "default", kind: "unknown_kind" }],
    [{ account: "default", kind: "available", asset: "USD", value: 1000 }],
  ])(
    "rejects malformed diagnostic facts instead of guessing their meaning",
    async (reason) => {
      const fetch = vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: { code: "currency_change_blocked", reasons: [reason] },
          }),
          { status: 409 },
        ),
      );
      const client = createOfficerApi(
        createApiClient({ baseUrl: "/api/v1", fetch }),
      );
      await expect(client.setAccountCurrency("default", "USD")).rejects.toThrow(
        "invalid currency-change diagnostics",
      );
    },
  );
});

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
const listPreMigrationBackups = (
  ...args: Parameters<ReturnType<typeof api>["listPreMigrationBackups"]>
) => api().listPreMigrationBackups(...args);
const downloadPreMigrationBackup = (
  ...args: Parameters<ReturnType<typeof api>["downloadPreMigrationBackup"]>
) => api().downloadPreMigrationBackup(...args);
const deletePreMigrationBackup = (
  ...args: Parameters<ReturnType<typeof api>["deletePreMigrationBackup"]>
) => api().deletePreMigrationBackup(...args);
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

describe("test execution policy client", () => {
  const id = "test/desk #1";
  const policy: TestExecutionPolicy = {
    rules: [
      {
        mask: { baseAsset: "A*", quoteAsset: "USD" },
        mode: "split",
        priceVariant: "worst",
        fills: 3,
      },
      {
        mask: { baseAsset: "MSFT", quoteAsset: "EUR" },
        mode: "rest",
        wait: { kind: "infinite" },
      },
    ],
  };
  const state = { connection: id, revision: "2", ...policy };

  it("GETs the policy on the encoded connection path without a body", async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse(state));
    const controller = new AbortController();
    expect(await api().fetchTestExecutionPolicy(id, controller.signal)).toEqual(
      state,
    );
    expect(fetch).toHaveBeenCalledExactlyOnceWith(
      "/app/api/v1/trading/connections/test%2Fdesk%20%231/test-policy",
      expect.objectContaining({
        method: "GET",
        body: undefined,
        signal: controller.signal,
      }),
    );
  });

  it("PUTs the whole policy once on the encoded connection path", async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse(state));
    expect(await api().updateTestExecutionPolicy(id, policy)).toEqual(state);
    expect(fetch).toHaveBeenCalledExactlyOnceWith(
      "/app/api/v1/trading/connections/test%2Fdesk%20%231/test-policy",
      expect.objectContaining({ method: "PUT", body: JSON.stringify(policy) }),
    );
  });

  it.each([
    {},
    { ...state, connection: undefined },
    { ...state, revision: 2 },
    { ...state, rules: null },
    { ...state, rules: [{}] },
    { ...state, rules: [{ ...policy.rules[0], mask: { baseAsset: "A*" } }] },
    { ...state, rules: [{ ...policy.rules[0], mode: "unknown" }] },
    { ...state, rules: [{ ...policy.rules[0], priceVariant: undefined }] },
    { ...state, rules: [{ ...policy.rules[0], fills: 4 }] },
    { ...state, rules: [{ ...policy.rules[1], priceVariant: "normal" }] },
    { ...state, rules: [{ ...policy.rules[1], wait: { kind: "finite" } }] },
  ])(
    "rejects malformed policy state instead of supplying defaults: %j",
    async (body) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse(body));
      await expect(api().fetchTestExecutionPolicy(id)).rejects.toBeInstanceOf(
        ApiError,
      );
    },
  );

  it("decodes an explicitly empty rule list", async () => {
    const empty = { connection: id, revision: "1", rules: [] };
    vi.mocked(fetch).mockResolvedValue(jsonResponse(empty));
    expect(await api().fetchTestExecutionPolicy(id)).toEqual(empty);
  });

  it("copies structured server errors when constructing an API error", () => {
    const details = [
      {
        code: "validation",
        pointer: "/rules/3/priceVariant",
        constraint: "required",
      },
    ];
    const error = new ApiError(
      "Invalid policy",
      "validation",
      422,
      undefined,
      undefined,
      undefined,
      undefined,
      details,
    );
    details[0].pointer = "/rules/0/mode";
    expect(error.errors).toEqual([
      {
        code: "validation",
        pointer: "/rules/3/priceVariant",
        constraint: "required",
      },
    ]);
    expect(new ApiError("Network failure", "network").errors).toBeUndefined();
  });

  it("preserves a PUT validation problem from the server", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse(
        {
          type: "about:blank",
          title: "Unprocessable Content",
          status: 422,
          detail: "fills must be 2 or 3",
          errors: [
            {
              code: "validation",
              constraint: "enum",
              pointer: "/rules/0/fills",
            },
          ],
        },
        422,
      ),
    );
    await expect(
      api().updateTestExecutionPolicy(id, policy),
    ).rejects.toMatchObject({
      status: 422,
      message: "fills must be 2 or 3",
      errors: [
        { code: "validation", constraint: "enum", pointer: "/rules/0/fills" },
      ],
    });
  });
});

describe("trading client", () => {
  it.each([
    ["trading_access_notification_failed", 409],
    ["trading_access_completion_failed", 500],
  ] as const)(
    "preserves committed-access code %s and status %i",
    async (code, status) => {
      const message =
        code === "trading_access_notification_failed"
          ? "access change was saved, but its mixed paper/live warning could not be published or delivered; review current access and alerts before retrying"
          : "access change was saved, but operation completion failed; review current access, alerts and service status before retrying";
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ error: { code, message } }, status),
      );
      await expect(
        api().createTradingConnection({
          provider: "test",
          label: "Test",
          mode: "paper",
          credentials: {},
          enabled: false,
        }),
      ).rejects.toMatchObject({
        code,
        status,
        message,
      });
    },
  );

  it("types an access subject as exactly one grantee", () => {
    // @ts-expect-error -- a subject cannot name both an account and a group
    const both: TradingAccessSubject = { account: "a", accountGroup: "g" };
    // @ts-expect-error -- a subject must name a grantee
    const neither: TradingAccessSubject = {};
    // Reading both bindings keeps noUnusedLocals from satisfying the two
    // directives above; only the subject type error may satisfy them.
    expect([both, neither]).toHaveLength(2);
  });

  const connection = {
    executionPolicy: false as const,
    id: "paper/desk #1",
    provider: "alpaca",
    label: "Paper desk",
    mode: "paper" as const,
    enabled: true,
    venueIdentity: "paper-account",
    state: "running" as const,
    secrets: { apiKey: true, apiSecret: false },
    instruments: [
      {
        externalSymbol: "AAPL",
        baseAsset: "AAPL",
        quoteAsset: "USD",
        enabled: true,
      },
    ],
    access: [
      {
        account: "desk/1 #paper",
        accountGroup: "",
        venueAccount: "",
        group: "",
      },
    ],
    diagnostics: [],
    feeAccount: "",
  };
  const trading: TradingSnapshot = {
    providers: [
      {
        executionPolicy: false,
        type: "alpaca",
        title: "Alpaca",
        venueAccounts: false,
        verifiesSymbols: true,
        searchesSymbols: true,
        unlinkedFees: true,
        liveOnly: false,
      },
    ],
    connections: [connection],
  };

  it.each([undefined, null, "", "unknown"])(
    "rejects missing or unknown provider executionPolicy: %j",
    async (executionPolicy) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({
          trading: {
            ...trading,
            providers: [{ ...trading.providers[0], executionPolicy }],
          },
        }),
      );
      await expect(api().fetchTrading()).rejects.toMatchObject({
        code: "internal",
        message: expect.stringContaining("executionPolicy"),
      });
    },
  );

  it.each([null, "", "unknown", "test", "venue", 1])(
    "rejects invalid present connection executionPolicy: %j",
    async (executionPolicy) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({
          trading: {
            ...trading,
            connections: [{ ...connection, executionPolicy }],
          },
        }),
      );
      await expect(api().fetchTrading()).rejects.toMatchObject({
        code: "internal",
        message: expect.stringContaining("executionPolicy"),
      });
    },
  );

  it("preserves available rows and restored connections without provider metadata", async () => {
    const unavailable: TradingSnapshot["connections"][number] = {
      ...connection,
      id: "restored-custom",
      provider: "custom",
      state: "unavailable",
      error: "Custom provider is unavailable",
    };
    delete unavailable.executionPolicy;
    const mixed = {
      ...trading,
      connections: [connection, unavailable],
    };
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ trading: mixed }));

    const result = await api().fetchTrading();
    expect(result).toEqual(mixed);
    expect(result.connections[1]).not.toHaveProperty("executionPolicy");
  });
  const venueOrder: VenueOrder = {
    connection: connection.id,
    venueAccount: "",
    route: { timeInForce: "day", providerField: "value" },
    clientOrderId: "client-1",
    venueOrderId: "venue-1",
    sendAttempted: true,
    createdAt: "2026-10-04T12:30:00Z",
  };
  const paperBody = {
    id: "order-1",
    account: "desk/1 #paper",
    baseAsset: "AAPL",
    quoteAsset: "USD",
    side: "buy",
    amountKind: "quantity",
    amountValue: "2.5",
    price: "100.25",
    destination: {
      connection: connection.id,
      venueAccount: "",
      route: { timeInForce: "day" },
    },
  };
  const approval = {
    signedApproval: "signed-envelope",
    keyId: "key-1",
    id: "order-1",
    verdict: "accept",
  };

  it.each([
    [true, "desk/1 #paper", true],
    [false, "", false],
  ] as const)(
    "normalizes the trading snapshot with unlinkedFees %s, feeAccount %s and liveOnly %s",
    async (unlinkedFees, feeAccount, liveOnly) => {
      const snapshot = {
        ...trading,
        providers: [{ ...trading.providers[0], unlinkedFees, liveOnly }],
        connections: [{ ...connection, feeAccount }],
      };
      const signal = new AbortController().signal;
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          trading: {
            ...snapshot,
            providers: [{ ...snapshot.providers[0], extra: "ignored" }],
            connections: [{ ...snapshot.connections[0], extra: "ignored" }],
          },
        }),
      );

      expect(await api().fetchTrading(signal)).toEqual(snapshot);
      expect(fetch).toHaveBeenCalledWith(
        "/app/api/v1/trading",
        expect.objectContaining({ method: "GET", body: undefined, signal }),
      );
    },
  );

  it.each([undefined, "unknown"])(
    "rejects missing or unknown connection mode %s",
    async (mode) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          trading: { ...trading, connections: [{ ...connection, mode }] },
        }),
      );

      const result = api().fetchTrading();

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({ code: "internal" });
    },
  );

  it("rejects an unknown connection state", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        trading: {
          ...trading,
          connections: [{ ...connection, state: "unknown" }],
        },
      }),
    );

    const result = api().fetchTrading();

    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({ code: "internal" });
  });

  it.each([undefined, { host: "127.0.0.1", port: 4002, clientId: 23 }])(
    "preserves non-secret trading settings %j without inventing absent settings",
    async (settings) => {
      const ib = {
        ...connection,
        provider: "ib",
        ...(settings === undefined ? {} : { settings }),
      };
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ trading: { ...trading, connections: [ib] } }),
      );
      const result = (await api().fetchTrading()).connections[0];
      if (settings === undefined) {
        expect(result).not.toHaveProperty("settings");
      } else {
        expect(result.settings).toEqual(settings);
      }
    },
  );

  describe.each(["providers", "connections"] as const)(
    "snapshot %s",
    (field) => {
      it.each([undefined, null, {}, "invalid"])(
        "rejects a missing or non-array collection %s",
        async (value) => {
          vi.mocked(fetch).mockResolvedValue(
            jsonResponse({ trading: { ...trading, [field]: value } }),
          );

          const result = api().fetchTrading();

          await expect(result).rejects.toBeInstanceOf(ApiError);
          await expect(result).rejects.toMatchObject({ code: "internal" });
        },
      );
    },
  );

  describe.each(["instruments", "access", "diagnostics"] as const)(
    "connection %s",
    (field) => {
      it.each([undefined, null, {}, "invalid"])(
        "rejects a missing or non-array collection %s",
        async (value) => {
          vi.mocked(fetch).mockResolvedValue(
            jsonResponse({
              trading: {
                ...trading,
                connections: [{ ...connection, [field]: value }],
              },
            }),
          );

          const result = api().fetchTrading();

          await expect(result).rejects.toBeInstanceOf(ApiError);
          await expect(result).rejects.toMatchObject({ code: "internal" });
        },
      );
    },
  );

  it.each([null, [], "invalid", false])(
    "rejects a non-object trading envelope %s",
    async (snapshot) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse({ trading: snapshot }));

      const result = api().fetchTrading();

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({ code: "internal" });
    },
  );

  it.each([undefined, null, "true"])(
    "rejects missing or invalid provider unlinkedFees %s",
    async (unlinkedFees) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          trading: {
            ...trading,
            providers: [{ ...trading.providers[0], unlinkedFees }],
          },
        }),
      );

      const result = api().fetchTrading();

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "internal",
        message:
          "Invalid or missing trading provider unlinkedFees in the API response",
      });
    },
  );

  it.each([undefined, null, "true"])(
    "rejects missing or invalid provider liveOnly %s",
    async (liveOnly) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          trading: {
            ...trading,
            providers: [{ ...trading.providers[0], liveOnly }],
          },
        }),
      );

      const result = api().fetchTrading();

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "internal",
        message:
          "Invalid or missing trading provider liveOnly in the API response",
      });
    },
  );

  it.each([undefined, null, false])(
    "rejects missing or invalid connection feeAccount %s",
    async (feeAccount) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          trading: {
            ...trading,
            connections: [{ ...connection, feeAccount }],
          },
        }),
      );

      const result = api().fetchTrading();

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "internal",
        message:
          "Trading connection feeAccount is missing from the API response",
      });
    },
  );

  it("normalizes account and group access grants with all four fields", async () => {
    const access = [
      {
        account: "desk/1 #paper",
        accountGroup: "",
        venueAccount: "venue-1",
        group: "desks",
      },
      { account: "", accountGroup: "desks", venueAccount: "", group: "" },
    ];
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        trading: {
          ...trading,
          connections: [
            {
              ...connection,
              access: access.map((grant) => ({ ...grant, extra: "ignored" })),
            },
          ],
        },
      }),
    );

    const snapshot = await api().fetchTrading();

    expect(snapshot.connections[0].access).toEqual(access);
  });

  it.each(["account", "accountGroup", "venueAccount", "group"] as const)(
    "rejects an access grant without %s",
    async (field) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          trading: {
            ...trading,
            connections: [
              {
                ...connection,
                access: [{ ...connection.access[0], [field]: undefined }],
              },
            ],
          },
        }),
      );

      const result = api().fetchTrading();

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "internal",
        message: `Trading access ${field} is missing from the API response`,
      });
    },
  );

  it("creates a connection with credentials as an object", async () => {
    const body = {
      id: connection.id,
      provider: "alpaca",
      label: "Paper desk",
      mode: "paper" as const,
      credentials: { apiKey: "test-key", apiSecret: "test-secret" },
      enabled: true,
    };
    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify({ connection }), {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }),
    );

    expect(await api().createTradingConnection(body)).toEqual(connection);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections",
      expect.objectContaining({ method: "POST", body: JSON.stringify(body) }),
    );
  });

  it.each<{
    label?: string;
    credentials?: Record<string, string>;
  }>([
    { label: "Renamed", credentials: { apiKey: "new-key", apiSecret: "" } },
    { label: "", credentials: {} },
    {},
  ])(
    "updates settings without changing blank or omitted fields",
    async (body) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse({ trading }));

      expect(
        await api().updateTradingConnectionSettings(connection.id, body),
      ).toEqual(trading);
      expect(fetch).toHaveBeenCalledWith(
        "/app/api/v1/trading/connections/paper%2Fdesk%20%231/settings",
        expect.objectContaining({ method: "PUT", body: JSON.stringify(body) }),
      );
    },
  );

  it.each([true, false])("sets connection enabled to %s", async (enabled) => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ enabled }));

    expect(
      await api().setTradingConnectionEnabled(connection.id, enabled),
    ).toBe(enabled);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/enabled",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ enabled }),
      }),
    );
  });

  it.each([true, false])(
    "sends explicit non-release confirmation %s when enabling",
    async (nonReleaseConfirmed) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse({ enabled: true }));
      expect(
        await api().setTradingConnectionEnabled(
          connection.id,
          true,
          nonReleaseConfirmed,
        ),
      ).toBe(true);
      expect(fetch).toHaveBeenCalledExactlyOnceWith(
        "/app/api/v1/trading/connections/paper%2Fdesk%20%231/enabled",
        expect.objectContaining({
          method: "PUT",
          body: JSON.stringify({ enabled: true, nonReleaseConfirmed }),
        }),
      );
    },
  );

  it("passes non-release confirmation through when creating a connection", async () => {
    const body = {
      provider: "alpaca",
      label: "Paper desk",
      mode: "paper" as const,
      credentials: { apiKey: "test-key", apiSecret: "test-secret" },
      enabled: true,
      nonReleaseConfirmed: true,
    };
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ connection }, 201));
    expect(await api().createTradingConnection(body)).toEqual(connection);
    expect(fetch).toHaveBeenCalledExactlyOnceWith(
      "/app/api/v1/trading/connections",
      expect.objectContaining({ method: "POST", body: JSON.stringify(body) }),
    );
  });

  it.each([
    [
      422,
      "non_release_archive",
      "This archive was made by a non-release build whose data is disposable. A release build cannot restore it.",
      "this archive was exported by a non-release (edge, dev or local) build of Officer; a release build does not restore it; its data is disposable and restores only into a non-release build",
    ],
    [
      403,
      "non_release_confirmation_required",
      "Enabling trading in a non-release build requires your confirmation in the panel.",
      "this is a non-release (edge, dev or local) build of Officer; it enables a trading connection only after the operator confirms it in the panel, because its data is disposable and the build may corrupt it",
    ],
  ])(
    "decodes HTTP %s with %s and its English explanation",
    async (status, code, message, serverMessage) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse(
          { error: { code, message: serverMessage } },
          status as number,
        ),
      );
      await expect(
        api().setTradingConnectionEnabled(connection.id, true),
      ).rejects.toMatchObject({
        code,
        status,
        message,
      });
    },
  );

  it("checks a connection without a request body", async () => {
    const check = { venueIdentity: "paper-account", venueStatus: "ACTIVE" };
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ check }));

    expect(await api().checkTradingConnection(connection.id)).toEqual(check);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/check",
      expect.objectContaining({ method: "POST", body: undefined }),
    );
  });

  it("upserts an instrument and unwraps the snapshot", async () => {
    const body = connection.instruments[0];
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ trading }));

    expect(await api().upsertTradingInstrument(connection.id, body)).toEqual(
      trading,
    );
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/instruments",
      expect.objectContaining({ method: "PUT", body: JSON.stringify(body) }),
    );
  });

  it("deletes an instrument with an encoded symbol and accepts 204", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

    expect(
      await api().deleteTradingInstrument(connection.id, "BRK/B #1"),
    ).toBeUndefined();
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/instruments?externalSymbol=BRK%2FB+%231",
      expect.objectContaining({ method: "DELETE", body: undefined }),
    );
  });

  it("verifies a symbol and unwraps venue metadata", async () => {
    const verification = {
      supported: true,
      exists: true,
      tradable: false,
      details: "Trading is suspended",
    };
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ verification }));

    expect(await api().verifyTradingSymbol(connection.id, "AAPL")).toEqual(
      verification,
    );
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/verify-symbol",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ externalSymbol: "AAPL" }),
      }),
    );
  });

  it("searches symbols and normalizes optional match fields", async () => {
    const search = {
      supported: true,
      matches: [
        {
          symbol: "AAPL",
          name: "Apple Inc.",
          class: "us_equity",
          exchange: "NASDAQ",
          tradable: true,
        },
        { symbol: "AAPL.TEST", tradable: false },
        {
          symbol: "ETHUSDT",
          baseAsset: "ETH",
          quoteAsset: "USDT",
          tradable: true,
        },
      ],
    };
    vi.mocked(fetch).mockResolvedValue(jsonResponse(search));

    expect(
      await api().searchTradingSymbols(connection.id, { query: "Apple" }),
    ).toEqual(search);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ query: "Apple" }),
      }),
    );
  });

  it.each([
    [
      { account: "desk/1 #paper", venueAccount: "" },
      { account: "desk/1 #paper", venueAccount: "" },
    ],
    [{ account: "desk/1 #paper" }, { account: "desk/1 #paper" }],
    [
      { accountGroup: "desks/1 #paper", venueAccount: "venue-1" },
      { accountGroup: "desks/1 #paper", venueAccount: "venue-1" },
    ],
    [{ accountGroup: "desks/1 #paper" }, { accountGroup: "desks/1 #paper" }],
    [
      { account: "desk/1 #paper", venueAccount: "", group: "desks" },
      { account: "desk/1 #paper", venueAccount: "" },
    ],
  ])("grants access with %j as the body %j", async (input, body) => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ trading }));

    expect(await api().grantTradingAccess(connection.id, input)).toEqual(
      trading,
    );
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/access",
      expect.objectContaining({ method: "PUT", body: JSON.stringify(body) }),
    );
  });

  it.each([
    [{ account: "desk/1 #paper" }, "account=desk%2F1+%23paper"],
    [{ accountGroup: "desks/1 #paper" }, "accountGroup=desks%2F1+%23paper"],
    [
      { account: "desk/1 #paper", venueAccount: "venue-1" },
      "account=desk%2F1+%23paper",
    ],
  ])(
    "revokes access for %j with only the query %s and accepts 204",
    async (subject, query) => {
      vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

      expect(
        await api().revokeTradingAccess(connection.id, subject),
      ).toBeUndefined();
      expect(fetch).toHaveBeenCalledWith(
        `/app/api/v1/trading/connections/paper%2Fdesk%20%231/access?${query}`,
        expect.objectContaining({ method: "DELETE", body: undefined }),
      );
    },
  );

  it("sets the fee account and unwraps the trading snapshot", async () => {
    const account = "desk/1 #paper";
    const snapshot = {
      ...trading,
      connections: [{ ...connection, feeAccount: account }],
    };
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ trading: snapshot }));

    expect(await api().setTradingFeeAccount(connection.id, account)).toEqual(
      snapshot,
    );
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/fee-account",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ account }),
      }),
    );
  });

  it.each([
    ["fetchTrading", () => api().fetchTrading()],
    [
      "updateTradingConnectionSettings",
      () => api().updateTradingConnectionSettings(connection.id, {}),
    ],
    [
      "upsertTradingInstrument",
      () =>
        api().upsertTradingInstrument(connection.id, connection.instruments[0]),
    ],
    [
      "grantTradingAccess",
      () => api().grantTradingAccess(connection.id, { account: "desk" }),
    ],
    [
      "setTradingFeeAccount",
      () => api().setTradingFeeAccount(connection.id, "desk"),
    ],
  ] as const)(
    "rejects a %s response without a trading snapshot",
    async (_method, request) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse({}));

      const result = request();

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "internal",
        message: "Trading snapshot is missing from the API response",
      });
    },
  );

  it.each([undefined, null, "false", 0])(
    "rejects a missing or non-boolean enabled echo %s",
    async (enabled) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse({ enabled }));

      const result = api().setTradingConnectionEnabled(connection.id, false);

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({ code: "internal" });
    },
  );

  it("clears the fee account on the encoded path and accepts 204", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

    expect(await api().clearTradingFeeAccount(connection.id)).toBeUndefined();
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/trading/connections/paper%2Fdesk%20%231/fee-account",
      expect.objectContaining({ method: "DELETE", body: undefined }),
    );
  });

  it.each(["setTradingFeeAccount", "revokeTradingAccess"] as const)(
    "preserves the not_found response from %s",
    async (method) => {
      vi.mocked(fetch).mockResolvedValue(
        new Response(
          JSON.stringify({
            error: {
              code: "not_found",
              message: "Account or binding not found",
            },
          }),
          { status: 404, headers: { "Content-Type": "application/json" } },
        ),
      );

      const result =
        method === "revokeTradingAccess"
          ? api().revokeTradingAccess(connection.id, { account: "missing" })
          : api().setTradingFeeAccount(connection.id, "missing");

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "not_found",
        status: 404,
        message: "Account or binding not found",
      });
    },
  );

  it.each([
    [undefined, ""],
    ["paper", "?mode=paper"],
    ["live", "?mode=live"],
  ] as const)("fetches destinations with mode %s", async (mode, query) => {
    const destinations = [
      {
        connection: connection.id,
        label: connection.label,
        provider: "alpaca",
        mode: "paper",
        enabled: false,
        venueAccount: "",
        state: "unavailable",
        error: "Venue unavailable",
      },
    ];
    const signal = new AbortController().signal;
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ destinations }));

    expect(
      await api().fetchTradingDestinations("desk/1 #paper", mode, signal),
    ).toEqual(destinations);
    expect(fetch).toHaveBeenCalledWith(
      `/app/api/v1/accounts/desk%2F1%20%23paper/trading-destinations${query}`,
      expect.objectContaining({ method: "GET", body: undefined, signal }),
    );
  });

  it("rejects an unknown destination mode", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        destinations: [
          { ...connection, connection: connection.id, mode: "unknown" },
        ],
      }),
    );

    const result = api().fetchTradingDestinations("desk");

    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({ code: "internal" });
  });

  it("accepts a degraded destination with its error", async () => {
    const destination = {
      connection: connection.id,
      label: connection.label,
      provider: "alpaca",
      mode: "paper",
      enabled: true,
      venueAccount: "",
      state: "degraded",
      error: "trade updates refused",
    };
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ destinations: [destination] }),
    );

    expect(await api().fetchTradingDestinations("desk")).toEqual([destination]);
  });

  it("preserves empty connection collections and unavailable errors", async () => {
    const unavailable = {
      ...connection,
      mode: "live",
      enabled: false,
      state: "unavailable",
      error: "Invalid API key",
      instruments: [],
      access: [],
    };
    const stopped = { ...unavailable, state: "stopped", error: undefined };
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        trading: {
          providers: [],
          connections: [unavailable, stopped],
        },
      }),
    );

    expect(await api().fetchTrading()).toEqual({
      providers: [],
      connections: [unavailable, stopped],
    });
  });

  it("keeps every provider credential presence flag", async () => {
    const etoro = {
      ...connection,
      provider: "etoro",
      secrets: { apiKey: true, userKey: false, malformed: "yes" },
    };
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ trading: { providers: [], connections: [etoro] } }),
    );

    expect((await api().fetchTrading()).connections[0].secrets).toEqual({
      apiKey: true,
      userKey: false,
      malformed: false,
    });
  });

  it("normalizes a degraded connection with its error and diagnostics", async () => {
    const diagnostic = {
      level: "error",
      code: "connection_error",
      kind: "provider",
      title: "Trade updates refused",
      detail: "The venue closed the trade-updates stream.",
      remediation: "Check the API key permissions.",
      actions: [{ type: "open_docs" }],
      at: "2026-10-13T08:00:00Z",
    };
    const degraded = {
      ...connection,
      state: "degraded",
      error: "trade updates refused",
      diagnostics: [diagnostic],
    };
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ trading: { providers: [], connections: [degraded] } }),
    );

    expect(await api().fetchTrading()).toEqual({
      providers: [],
      connections: [degraded],
    });
  });

  it.each([true, false])(
    "fetches order detail with a normalized venue order and sendAttempted %s",
    async (sendAttempted) => {
      const linkedOrder = { ...venueOrder, sendAttempted };
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          order: { id: "order/1", leavesQuantity: "2.5" },
          events: [],
          trades: [],
          venueOrder: linkedOrder,
        }),
      );

      const detail = await api().fetchOrderDetail("order/1");

      expect(detail.order.id).toBe("order/1");
      expect(detail.events).toEqual([]);
      expect(detail.trades).toEqual([]);
      expect(detail.venueOrder).toEqual(linkedOrder);
      expect(fetch).toHaveBeenCalledWith(
        "/app/api/v1/orders/order%2F1",
        expect.objectContaining({ method: "GET", body: undefined }),
      );
    },
  );

  it.each([undefined, null, { timeInForce: 1 }])(
    "rejects a missing or invalid venue order route %s",
    async (route) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          order: { id: "order-1", leavesQuantity: "0" },
          events: [],
          trades: [],
          venueOrder: { ...venueOrder, route },
        }),
      );

      const result = api().fetchOrderDetail("order-1");

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({ code: "internal" });
    },
  );

  it.each([undefined, null, "false", 0])(
    "rejects missing or non-boolean venue order sendAttempted %s",
    async (sendAttempted) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          order: { id: "order-1", leavesQuantity: "0" },
          events: [],
          trades: [],
          venueOrder: { ...venueOrder, sendAttempted },
        }),
      );

      const result = api().fetchOrderDetail("order-1");

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({ code: "internal" });
    },
  );

  it("keeps venueOrder absent for an ordinary order detail", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        order: { id: "order-1", leavesQuantity: "0" },
        events: [],
        trades: [],
      }),
    );

    expect(await api().fetchOrderDetail("order-1")).not.toHaveProperty(
      "venueOrder",
    );
  });

  it("sends a paper order and returns approval and venue acknowledgment", async () => {
    const venueSend = { status: "acknowledged", venueOrder };
    const signal = new AbortController().signal;
    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify({ ...approval, venueSend }), {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }),
    );

    expect(await api().sendPaperOrder(paperBody, signal)).toEqual({
      ...approval,
      reasons: [],
      reviews: [],
      venueSend,
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/send-paper",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify(paperBody),
        signal,
      }),
    );
  });

  it("sends a paper order with only the required destination route", async () => {
    const body = {
      ...paperBody,
      destination: { route: paperBody.destination.route },
    };
    vi.mocked(fetch).mockResolvedValue(jsonResponse(approval));

    await api().sendPaperOrder(body);

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/send-paper",
      expect.objectContaining({ method: "POST", body: JSON.stringify(body) }),
    );
  });

  it("sends a live order to the live endpoint with the paper body and result", async () => {
    const venueSend = { status: "acknowledged", venueOrder };
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ ...approval, venueSend }),
    );

    expect(await api().sendLiveOrder(paperBody)).toEqual({
      ...approval,
      reasons: [],
      reviews: [],
      venueSend,
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/send-live",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify(paperBody),
      }),
    );
  });

  it.each(["refused", "unknown"])(
    "returns a %s venue send without venue acknowledgment",
    async (status) => {
      const pending = { ...venueOrder };
      delete pending.venueOrderId;
      const venueSend = { status, error: "Venue reason", venueOrder: pending };
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ ...approval, venueSend }),
      );

      const result = await api().sendPaperOrder(paperBody);

      expect(result.venueSend).toEqual(venueSend);
      expect(result.venueSend?.venueOrder).not.toHaveProperty("venueOrderId");
    },
  );

  it("rejects an unknown venueSend status", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ ...approval, venueSend: { status: "pending" } }),
    );

    const result = api().sendPaperOrder(paperBody);

    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({ code: "internal" });
  });

  it("returns a pre-trade reject without venueSend or a follow-up request", async () => {
    const rejected = {
      ...approval,
      verdict: "reject",
      reasons: [
        {
          code: "limit",
          scope: "account",
          policy: "position",
          reason: "Too large",
        },
      ],
    };
    const body = {
      account: paperBody.account,
      baseAsset: paperBody.baseAsset,
      quoteAsset: paperBody.quoteAsset,
      side: paperBody.side,
      amountKind: paperBody.amountKind,
      amountValue: paperBody.amountValue,
      destination: paperBody.destination,
    };
    vi.mocked(fetch).mockResolvedValue(jsonResponse(rejected));

    const result = await api().sendPaperOrder(body);

    expect(result).toMatchObject(rejected);
    expect(result).not.toHaveProperty("venueSend");
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/send-paper",
      expect.objectContaining({ method: "POST", body: JSON.stringify(body) }),
    );
  });

  it("uses the venue reason from a 422 problem response", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          type: "about:blank",
          title: "Validation failed",
          status: 422,
          detail: "AAPL is not tradable at this venue",
          errors: [
            {
              code: "validation",
              constraint: "tradable",
              pointer: "/destination",
            },
          ],
        }),
        {
          status: 422,
          headers: { "Content-Type": "application/problem+json" },
        },
      ),
    );

    const result = api().sendPaperOrder(paperBody);

    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({
      code: "precondition",
      status: 422,
      message: "AAPL is not tradable at this venue",
    });
  });

  it("uses a 400 problem detail with the existing status code", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          type: "about:blank",
          title: "Bad request",
          status: 400,
          detail: "The request body contains invalid JSON",
          errors: [{ code: "bad_request" }],
        }),
        {
          status: 400,
          headers: { "Content-Type": "application/problem+json" },
        },
      ),
    );

    const result = api().sendPaperOrder(paperBody);

    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({
      code: "validation",
      status: 400,
      message: "The request body contains invalid JSON",
    });
  });

  it.each([
    ["fee_account_required", 409, "A fee account is required."],
    [
      "trading_mode_mismatch",
      409,
      "The connection uses a different trading mode.",
    ],
    [
      "destination_ambiguous",
      409,
      "Multiple trading destinations match the request.",
    ],
    ["no_destination", 409, "No trading destination is available."],
    ["forbidden", 403, "Access is denied."],
    [
      "upstream",
      502,
      "An upstream service is unreachable or refused the request.",
    ],
  ] as const)(
    "preserves the %s contract code and its default message",
    async (code, status, message) => {
      vi.mocked(fetch).mockResolvedValue(
        new Response(JSON.stringify({ error: { code } }), {
          status,
          headers: { "Content-Type": "application/json" },
        }),
      );

      const result = api().sendPaperOrder(paperBody);

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({ code, status, message });
    },
  );

  it("preserves the trading_mode_mismatch server message", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "trading_mode_mismatch",
            message: "Connection is live, paper mode is required",
          },
        }),
        { status: 409, headers: { "Content-Type": "application/json" } },
      ),
    );

    const result = api().sendPaperOrder(paperBody);

    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({
      code: "trading_mode_mismatch",
      status: 409,
      message: "Connection is live, paper mode is required",
    });
  });

  it.each([
    [
      "grantTradingAccess",
      "A fee account is required before binding another Officer account",
    ],
    [
      "clearTradingFeeAccount",
      "A fee account is required while multiple Officer accounts are bound",
    ],
  ] as const)(
    "preserves the fee_account_required server message from %s",
    async (method, message) => {
      vi.mocked(fetch).mockResolvedValue(
        new Response(
          JSON.stringify({ error: { code: "fee_account_required", message } }),
          { status: 409, headers: { "Content-Type": "application/json" } },
        ),
      );

      const result =
        method === "grantTradingAccess"
          ? api().grantTradingAccess(connection.id, { account: "other-desk" })
          : api().clearTradingFeeAccount(connection.id);

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "fee_account_required",
        status: 409,
        message,
      });
    },
  );
});

describe("credential storage client", () => {
  const storage: FrameworkCredentialStorage = {
    credentialsSealed: false,
    plaintextTradingKeys: true,
    plaintextMarketDataKeys: false,
  };

  it("accepts the three framework fields without a seal report", async () => {
    const signal = new AbortController().signal;
    vi.mocked(fetch).mockResolvedValue(jsonResponse(storage));

    expect(await api().fetchCredentialStorage(signal)).toEqual(storage);
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/credential-storage",
      expect.objectContaining({ method: "GET", body: undefined, signal }),
    );
  });

  describe.each([
    "credentialsSealed",
    "plaintextTradingKeys",
    "plaintextMarketDataKeys",
  ] as const)("field %s", (field) => {
    it.each([undefined, null, "false", 0])(
      "rejects a missing or non-boolean value %s",
      async (value) => {
        vi.mocked(fetch).mockResolvedValue(
          jsonResponse({ ...storage, [field]: value }),
        );

        const result = api().fetchCredentialStorage();

        await expect(result).rejects.toBeInstanceOf(ApiError);
        await expect(result).rejects.toMatchObject({
          code: "internal",
          message: `Invalid or missing credential storage ${field} in the API response`,
        });
      },
    );
  });

  it.each([null, [], "invalid", false])(
    "rejects a non-object body %s",
    async (body) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse(body));

      const result = api().fetchCredentialStorage();

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "internal",
        message: "Credential storage is missing from the API response",
      });
    },
  );

  it("returns only framework fields when the server adds a seal report", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ ...storage, sealedAt: "invalid", resealedSecrets: -1 }),
    );

    expect(await api().fetchCredentialStorage()).toEqual(storage);
  });
});

beforeEach(() => {
  vi.restoreAllMocks();
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(okMarketDataResponse()));
});

const backupArchive: BackupArchive = {
  credentialForm: "plaintext",
  manifest: {
    formatVersion: 2,
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

  describe.each(["accounts", "groups"] as const)("%s venue access", (list) => {
    const fetchList = (filters?: { venue?: string }) =>
      list === "accounts" ? fetchAccounts(filters) : fetchGroups(filters);

    it.each(["any", "none", "connection:desk:primary"])(
      "sends the venue filter %s",
      async (venue) => {
        vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ [list]: [] }));
        await fetchList({ venue });

        const url = new URL(
          String(vi.mocked(fetch).mock.calls[0][0]),
          "http://test",
        );
        expect(url.pathname).toBe(`/app/api/v1/${list}`);
        expect(url.searchParams.get("venue")).toBe(venue);
      },
    );

    it("sends no venue filter when it is empty", async () => {
      vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ [list]: [] }));
      await fetchList({ venue: "" });

      expect(String(vi.mocked(fetch).mock.calls[0][0])).toBe(
        `/app/api/v1/${list}`,
      );
    });

    it("keeps venueAccess of list rows and omits it when absent", async () => {
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({
          [list]: [
            { code: "granted", venueAccess: true },
            { code: "plain", venueAccess: false },
            { code: "single" },
          ],
        }),
      );
      const result = await fetchList();

      expect(result[0]).toHaveProperty("venueAccess", true);
      expect(result[1]).toHaveProperty("venueAccess", false);
      expect(result[2]).not.toHaveProperty("venueAccess");
    });

    it.each(["yes", null, 1])("rejects a venueAccess of %s", async (value) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({ [list]: [{ code: "malformed", venueAccess: value }] }),
      );

      await expect(fetchList()).rejects.toMatchObject({
        code: "internal",
        message: `Invalid ${list === "accounts" ? "account" : "group"} venueAccess in the API response`,
      });
    });
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
  const orderSizeBarrier = {
    scope: "broker",
    account: "",
    asset: "",
    maxQuantity: "10",
    maxNotional: "",
  };

  it("reports an unknown policy kind without treating it as a rate limit", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        policies: [
          {
            kind: "exposure_limit",
            mode: "reject",
            scope: "broker",
            account: "",
            asset: "",
            values: { exposure: { maxNotional: "10000" } },
          },
        ],
        total: 1,
      }),
    );
    const result = api().fetchPoliciesPage();
    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({
      code: "internal",
      message: expect.stringContaining('"exposure_limit"'),
    });
  });

  it.each(["addLimit", "putLimit"] as const)(
    "refuses an order-size write with null mode on %s without fetching",
    async (method) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          orderSizeLimit: { ...orderSizeBarrier, mode: "reject" },
        }),
      );
      const result = api()[method](
        {
          policy: "order_size_limit",
          mode: null,
          scope: "broker",
          account: "",
          accountGroup: "",
          asset: "",
          values: { max_quantity: "10" },
        },
        "reject",
      );
      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "validation",
        message: expect.stringContaining("mode"),
      });
      expect(fetch).not.toHaveBeenCalled();
    },
  );

  it.each([undefined, "unknown", null])(
    "rejects invalid account order-size mode %s",
    async (mode) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          account: { code: "desk-alpha" },
          limits: {
            rateLimits: [],
            orderSizeLimits: [{ ...orderSizeBarrier, mode }],
            positionLimits: [],
            spotFundsPnlBoundsLimits: [],
            spotFundsUnrealizedPnlBoundsLimits: [],
            spotFundsTotalPnlBoundsLimits: [],
          },
        }),
      );
      const result = api().fetchAccountState("desk-alpha");
      await expect(result).rejects.toThrow(ApiError);
      await expect(result).rejects.toThrow(/mode/);
    },
  );

  it.each([undefined, "unknown", null])(
    "rejects invalid PUT order-size mode %s",
    async (mode) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ orderSizeLimit: { ...orderSizeBarrier, mode } }),
      );
      const result = api().putLimit(
        {
          policy: "order_size_limit",
          mode: "reject",
          scope: "broker",
          account: "",
          accountGroup: "",
          asset: "",
          values: { max_quantity: "10" },
        },
        "reject",
      );
      await expect(result).rejects.toThrow(ApiError);
      await expect(result).rejects.toThrow(/mode/);
    },
  );

  it.each([
    ["order_size_limit", undefined],
    ["order_size_limit", "unknown"],
    ["order_size_limit", null],
    ["rate_limit", undefined],
    ["rate_limit", "unknown"],
    ["spot_funds_pnl_bounds_kill_switch", null],
    ["position_limit", undefined],
  ])("rejects policy %s with invalid top-level mode %s", async (kind, mode) => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        policies: [
          {
            kind,
            mode,
            scope: "broker",
            account: "",
            asset: "",
            values: {},
          },
        ],
        total: 1,
      }),
    );
    const result = api().fetchPoliciesPage();
    await expect(result).rejects.toThrow(ApiError);
    await expect(result).rejects.toThrow(/mode/);
  });

  it.each(["reject", "review_without_hold", "review_with_hold"] as const)(
    "carries mode %s through account, PUT, POST and policy responses and requests",
    async (mode) => {
      const row = { ...orderSizeBarrier, mode };
      const expected = {
        policy: "order_size_limit",
        mode,
        scope: "broker",
        account: "",
        accountGroup: "",
        asset: "",
        values: { max_quantity: "10", max_notional: "" },
      };
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({
          account: { code: "desk-alpha" },
          limits: {
            rateLimits: [],
            orderSizeLimits: [row],
            positionLimits: [],
            spotFundsPnlBoundsLimits: [],
            spotFundsUnrealizedPnlBoundsLimits: [],
            spotFundsTotalPnlBoundsLimits: [],
          },
        }),
      );
      expect((await api().fetchAccountState("desk-alpha")).limits).toEqual([
        expected,
      ]);
      for (const method of ["putLimit", "addLimit"] as const) {
        vi.mocked(fetch).mockResolvedValueOnce(
          jsonResponse({ orderSizeLimit: row }),
        );
        expect(await api()[method](expected, "reject")).toEqual(expected);
        const request = vi.mocked(fetch).mock.lastCall![1] as RequestInit;
        expect(request.method).toBe(method === "putLimit" ? "PUT" : "POST");
        expect(JSON.parse(String(request.body))).toEqual(row);
      }
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({
          policies: [
            {
              kind: "order_size_limit",
              mode,
              scope: "broker",
              account: "",
              accountGroup: "",
              asset: "",
              values: { orderSize: { maxQuantity: "10", maxNotional: "" } },
            },
          ],
          total: 1,
        }),
      );
      expect(
        (await api().fetchPoliciesPage({ policy: "order_size", mode })).items,
      ).toEqual([{ ...expected, accountGroup: "" }]);
      const url = new URL(
        String(vi.mocked(fetch).mock.lastCall![0]),
        "http://test",
      );
      expect(url.searchParams.get("mode")).toBe(mode);
      expect(url.searchParams.get("policy")).toBe("order_size");
    },
  );

  it.each(["order_size_limit", "rate_limit"])(
    "deletes a %s review barrier by its exact mode and refuses a missing mode",
    async (policy) => {
      vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));
      const target = {
        policy,
        mode: "review_without_hold" as const,
        scope: "broker",
        account: "",
        accountGroup: "",
        asset: "",
      };
      await api().deleteLimit(target);
      const url = new URL(
        String(vi.mocked(fetch).mock.lastCall![0]),
        "http://test",
      );
      expect(url.searchParams.get("policy")).toBe(policy);
      expect(url.searchParams.get("mode")).toBe("review_without_hold");
      const calls = vi.mocked(fetch).mock.calls.length;
      await expect(
        api().deleteLimit({ ...target, mode: null }),
      ).rejects.toThrow(/mode/);
      expect(fetch).toHaveBeenCalledTimes(calls);
    },
  );

  it.each<[keyof LimitTarget, LimitTarget, LimitTarget]>([
    [
      "policy",
      {
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
      },
      {
        policy: "spot_funds_unrealized_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
      },
    ],
    [
      "mode",
      {
        policy: "order_size_limit",
        mode: "reject",
        scope: "broker",
        account: "",
        accountGroup: "",
        asset: "",
      },
      {
        policy: "order_size_limit",
        mode: "review_without_hold",
        scope: "broker",
        account: "",
        accountGroup: "",
        asset: "",
      },
    ],
    [
      "scope",
      {
        policy: "order_size_limit",
        mode: "reject",
        scope: "underlying_asset",
        account: "",
        accountGroup: "",
        asset: "AAPL",
      },
      {
        policy: "order_size_limit",
        mode: "reject",
        scope: "settlement_asset",
        account: "",
        accountGroup: "",
        asset: "AAPL",
      },
    ],
    [
      "account",
      {
        policy: "rate_limit",
        mode: "reject",
        scope: "account",
        account: "desk-alpha",
        accountGroup: "",
        asset: "",
      },
      {
        policy: "rate_limit",
        mode: "reject",
        scope: "account",
        account: "desk-beta",
        accountGroup: "",
        asset: "",
      },
    ],
    [
      "accountGroup",
      {
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "account_group",
        account: "",
        accountGroup: "group-alpha",
        asset: "",
      },
      {
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "account_group",
        account: "",
        accountGroup: "group-beta",
        asset: "",
      },
    ],
    [
      "asset",
      {
        policy: "rate_limit",
        mode: "reject",
        scope: "asset",
        account: "",
        accountGroup: "",
        asset: "AAPL",
      },
      {
        policy: "rate_limit",
        mode: "reject",
        scope: "asset",
        account: "",
        accountGroup: "",
        asset: "MSFT",
      },
    ],
  ])(
    "uses distinct DELETE URLs for the %s axis",
    async (_axis, left, right) => {
      vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));
      const client = api();
      await client.deleteLimit(left);
      await client.deleteLimit(right);
      const [first, second] = vi.mocked(fetch).mock.calls;
      expect(first[0]).not.toBe(second[0]);
      expect(first[1]).toEqual(expect.objectContaining({ method: "DELETE" }));
      expect(second[1]).toEqual(expect.objectContaining({ method: "DELETE" }));
    },
  );

  it.each([
    ["positionLimit", {}, "Position limit is missing from the API response"],
    [
      "maxPosition",
      {
        positionLimit: {
          mode: "reject",
          scope: "account_asset",
          account: "desk-alpha",
          asset: "AAPL",
        },
      },
      "Position ceiling is missing from the API response",
    ],
  ])(
    "rejects a position PUT answer without %s",
    async (_field, body, message) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse(body));
      const { putLimit } = api();
      const result = putLimit(
        {
          policy: "position_limit",
          mode: "reject",
          scope: "account_asset",
          account: "desk-alpha",
          accountGroup: "",
          asset: "AAPL",
          values: { max_position: "10" },
        },
        "reject",
      );
      await expect(result).rejects.toThrow(ApiError);
      await expect(result).rejects.toMatchObject({ message, code: "internal" });
    },
  );

  it.each([
    ["values.position", {}, "Position limit is missing from the API response"],
    [
      "maxPosition",
      { position: {} },
      "Position ceiling is missing from the API response",
    ],
  ])(
    "rejects a position policy row without %s",
    async (_field, values, message) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          policies: [
            {
              kind: "position_limit",
              mode: "reject",
              scope: "account_asset",
              account: "desk-alpha",
              asset: "AAPL",
              values,
            },
          ],
          total: 1,
        }),
      );
      const { fetchPoliciesPage } = api();
      const result = fetchPoliciesPage({ policy: "position" });
      await expect(result).rejects.toThrow(ApiError);
      await expect(result).rejects.toMatchObject({ message, code: "internal" });
    },
  );

  it.each([
    ["missing", {}],
    ["null", { positionLimits: null }],
    ["non-array", { positionLimits: {} }],
  ])("rejects account state with %s positionLimits", async (_case, limits) => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ account: { code: "desk-alpha" }, limits }),
    );
    const { fetchAccountState } = api();
    const result = fetchAccountState("desk-alpha");
    await expect(result).rejects.toThrow(ApiError);
    await expect(result).rejects.toMatchObject({
      message:
        "Position limits array is invalid or missing from the API response",
      code: "internal",
    });
  });

  it("upserts a position ceiling with the exact string and review mode", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        positionLimit: {
          mode: "review_with_hold",
          scope: "account_asset",
          account: "desk-alpha",
          asset: "AAPL",
          maxPosition: "10.00000001",
        },
      }),
    );
    const { putLimit } = api();
    const limit = await putLimit(
      {
        policy: "position_limit",
        mode: "review_with_hold",
        scope: "account_asset",
        account: "desk-alpha",
        accountGroup: "",
        asset: "AAPL",
        values: { max_position: "+10.000000010" },
      },
      "reject",
    );

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits/position?missingAccount=reject",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          scope: "account_asset",
          account: "desk-alpha",
          asset: "AAPL",
          maxPosition: "+10.000000010",
          mode: "review_with_hold",
        }),
      }),
    );
    expect(limit).toEqual({
      policy: "position_limit",
      mode: "review_with_hold",
      scope: "account_asset",
      account: "desk-alpha",
      accountGroup: "",
      asset: "AAPL",
      values: { max_position: "10.00000001" },
    });
  });

  it("reads a position review row and serializes its policy, mode and sort filters", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        policies: [
          {
            kind: "position_limit",
            mode: "review_with_hold",
            scope: "account_asset",
            account: "desk-alpha",
            asset: "AAPL",
            values: { position: { maxPosition: "10.00000001" } },
          },
        ],
        total: 1,
      }),
    );
    const { fetchPoliciesPage } = api();
    const page = await fetchPoliciesPage({
      policy: "position",
      mode: "review_with_hold",
      sort: "maxPosition",
      order: "desc",
    });
    const url = new URL(
      String(vi.mocked(fetch).mock.calls[0][0]),
      "http://test",
    );
    expect(Object.fromEntries(url.searchParams.entries())).toEqual({
      policy: "position",
      mode: "review_with_hold",
      sort: "maxPosition",
      order: "desc",
    });
    expect(page.items).toEqual([
      {
        policy: "position_limit",
        mode: "review_with_hold",
        scope: "account_asset",
        account: "desk-alpha",
        accountGroup: "",
        asset: "AAPL",
        values: { max_position: "10.00000001" },
      },
    ]);
  });

  it("keeps review modes on typed rate, position and P&L rows from account state", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        account: { code: "desk-alpha" },
        limits: {
          rateLimits: [
            {
              mode: "review_without_hold",
              scope: "account",
              account: "desk-alpha",
              asset: "",
              maxOrders: 20,
              windowMs: 60000,
            },
          ],
          positionLimits: [
            {
              mode: "review_with_hold",
              scope: "account_asset",
              account: "desk-alpha",
              asset: "AAPL",
              maxPosition: "0",
            },
          ],
          spotFundsPnlBoundsLimits: [
            {
              mode: "review_without_hold",
              scope: "account",
              account: "desk-alpha",
              currency: "USD",
              lowerBound: "-1000",
            },
          ],
        },
      }),
    );
    const { fetchAccountState } = api();
    const state = await fetchAccountState("desk-alpha");
    expect(state.limits).toEqual([
      expect.objectContaining({
        policy: "rate_limit",
        mode: "review_without_hold",
        values: { max_orders: "20", window: "1m" },
      }),
      {
        policy: "position_limit",
        mode: "review_with_hold",
        scope: "account_asset",
        account: "desk-alpha",
        accountGroup: "",
        asset: "AAPL",
        values: { max_position: "0" },
      },
      expect.objectContaining({
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "review_without_hold",
        values: { currency: "USD", lower_bound: "-1000", upper_bound: "" },
      }),
    ]);
  });

  it("deletes a position review barrier through the generic endpoint", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));
    const { deleteLimit } = api();
    await deleteLimit({
      policy: "position_limit",
      mode: "review_with_hold",
      scope: "account_asset",
      account: "desk-alpha",
      accountGroup: "",
      asset: "AAPL",
    });
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits?policy=position_limit&scope=account_asset&mode=review_with_hold&account=desk-alpha&asset=AAPL",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

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

  it("flattens typed limit-list responses with rate and P&L review modes", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        policies: [
          {
            kind: "rate_limit",
            mode: "review_with_hold",
            scope: "account",
            account: "desk-alpha",
            asset: "",
            values: { rate: { windowMs: 60000, maxOrders: 20 } },
          },
          {
            kind: "order_size_limit",
            mode: "reject",
            scope: "account_underlying_asset",
            account: "desk-alpha",
            asset: "AAPL",
            values: { orderSize: { maxQuantity: "10", maxNotional: "" } },
          },
          {
            kind: "spot_funds_pnl_bounds_kill_switch",
            mode: "review_with_hold",
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
          {
            kind: "spot_funds_unrealized_pnl_bounds_kill_switch",
            mode: "reject",
            scope: "account",
            account: "desk-alpha",
            asset: "",
            values: {
              spotFundsPnlBounds: {
                currency: "USD",
                lowerBound: "-200",
                upperBound: "",
              },
            },
          },
          {
            kind: "spot_funds_total_pnl_bounds_kill_switch",
            mode: "reject",
            scope: "global",
            account: "",
            asset: "",
            values: {
              spotFundsPnlBounds: {
                currency: "USD",
                lowerBound: "",
                upperBound: "900",
              },
            },
          },
        ],
        total: 5,
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
        mode: "review_with_hold",
        scope: "account",
        account: "desk-alpha",
        accountGroup: "",
        asset: "",
        values: { max_orders: "20", window: "1m" },
      },
      {
        policy: "order_size_limit",
        mode: "reject",
        scope: "account_underlying_asset",
        account: "desk-alpha",
        accountGroup: "",
        asset: "AAPL",
        values: { max_quantity: "10", max_notional: "" },
      },
      {
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "review_with_hold",
        scope: "account_group",
        account: "",
        accountGroup: "desk-alpha",
        asset: "",
        values: { currency: "USD", lower_bound: "-1000", upper_bound: "" },
      },
      {
        policy: "spot_funds_unrealized_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "account",
        account: "desk-alpha",
        accountGroup: "",
        asset: "",
        values: { currency: "USD", lower_bound: "-200", upper_bound: "" },
      },
      {
        policy: "spot_funds_total_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { currency: "USD", lower_bound: "", upper_bound: "900" },
      },
    ]);
  });

  it.each([
    ["addLimit", "POST", 201],
    ["putLimit", "PUT", 200],
  ] as const)(
    "writes rate limits with a review mode through %s (%s, %i)",
    async (operation, method, status) => {
      const stored = {
        scope: "account",
        account: "desk-alpha",
        asset: "",
        windowMs: 60000,
        maxOrders: 20,
        mode: "review_with_hold",
      };
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({ rateLimit: stored }, status),
      );

      const limit = await api()[operation](
        {
          policy: "rate_limit",
          mode: "review_with_hold",
          scope: "account",
          account: "desk-alpha",
          accountGroup: "",
          asset: "",
          values: { max_orders: "20", window: "1m" },
        },
        "reject",
      );

      expect(fetch).toHaveBeenCalledExactlyOnceWith(
        "/app/api/v1/limits/rate?missingAccount=reject",
        expect.objectContaining({ method, body: JSON.stringify(stored) }),
      );
      expect(limit).toEqual({
        policy: "rate_limit",
        mode: "review_with_hold",
        scope: "account",
        account: "desk-alpha",
        accountGroup: "",
        asset: "",
        values: { max_orders: "20", window: "1m" },
      });
    },
  );

  it("decodes and localizes limit_exists without retrying the request", async () => {
    const i18n = createInstance();
    await i18n.init({
      lng: "ru",
      resources: appLocaleResources,
      interpolation: { escapeValue: false },
    });
    vi.mocked(fetch).mockResolvedValueOnce(
      errorResponse(409, "limit_exists", {}, "the rate limit already exists"),
    );
    const client = createOfficerApi(
      createApiClient({
        baseUrl: "/app/api/v1",
        fetch: globalThis.fetch,
        translate: (key, params) => i18n.t(key, params),
      }),
    );

    await expect(
      client.addLimit(
        {
          policy: "rate_limit",
          mode: "reject",
          scope: "account",
          account: "desk-alpha",
          accountGroup: "",
          asset: "",
          values: { max_orders: "20", window: "1m" },
        },
        "reject",
      ),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "limit_exists",
      status: 409,
      message: "Такая политика уже есть. Измените существующую политику.",
    });
    expect(fetch).toHaveBeenCalledExactlyOnceWith(
      "/app/api/v1/limits/rate?missingAccount=reject",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("resends the rate limit with missingAccount=create after account creation is confirmed", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        rateLimit: {
          mode: "reject",
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
        mode: "reject",
        scope: "account",
        account: "desk-alpha",
        accountGroup: "",
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
          mode: "reject",
          scope: "underlying_asset",
          account: "",
          asset: "AAPL",
          maxQuantity: "10",
          maxNotional: "",
        },
      }),
    );

    const { putLimit } = api();
    await putLimit(
      {
        policy: "order_size_limit",
        mode: "reject",
        scope: "underlying_asset",
        account: "",
        accountGroup: "",
        asset: "AAPL",
        values: { max_quantity: "10" },
      },
      "reject",
    );

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits/order-size",
      expect.any(Object),
    );
    const request = vi.mocked(fetch).mock.calls[0][1] as RequestInit;
    expect(JSON.parse(String(request.body))).toEqual({
      scope: "underlying_asset",
      mode: "reject",
      account: "",
      asset: "AAPL",
      maxQuantity: "10",
      maxNotional: "",
    });
  });

  it("omits missingAccount for a self-computed PnL bound scoped to an account group", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        spotFundsPnlBoundsLimit: {
          mode: "reject",
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
        mode: "reject",
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
          mode: "reject",
          scope: "account",
          account: "desk-alpha",
          accountGroup: "",
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

  it("upserts a self-computed P&L limit with a review mode", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        spotFundsPnlBoundsLimit: {
          mode: "review_with_hold",
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
        mode: "review_with_hold",
        scope: "account",
        account: "desk-alpha",
        accountGroup: "",
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
          mode: "review_with_hold",
          currency: "USD",
          lowerBound: "-1000",
          upperBound: "500",
        }),
      }),
    );
    expect(limit).toEqual(
      expect.objectContaining({
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "review_with_hold",
        account: "desk-alpha",
        values: {
          currency: "USD",
          lower_bound: "-1000",
          upper_bound: "500",
        },
      }),
    );
  });

  it.each([
    [
      "spot_funds_unrealized_pnl_bounds_kill_switch",
      "spot-funds-unrealized-pnl-bounds",
      "spotFundsUnrealizedPnlBoundsLimit",
    ],
    [
      "spot_funds_total_pnl_bounds_kill_switch",
      "spot-funds-total-pnl-bounds",
      "spotFundsTotalPnlBoundsLimit",
    ],
  ])(
    "upserts the %s bound through its own endpoint",
    async (policy, path, putKey) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          [putKey]: {
            mode: "reject",
            scope: "account",
            account: "desk-alpha",
            accountGroup: "",
            currency: "USD",
            lowerBound: "-1000",
            upperBound: "",
          },
        }),
      );

      const { putLimit } = api();
      const limit = await putLimit(
        {
          policy,
          mode: "reject",
          scope: "account",
          account: "desk-alpha",
          accountGroup: "",
          asset: "",
          values: { currency: "USD", lower_bound: "-1000", upper_bound: "" },
        },
        "reject",
      );

      expect(fetch).toHaveBeenCalledWith(
        `/app/api/v1/limits/${path}?missingAccount=reject`,
        expect.objectContaining({ method: "PUT" }),
      );
      expect(limit).toEqual(
        expect.objectContaining({
          policy,
          account: "desk-alpha",
          values: { currency: "USD", lower_bound: "-1000", upper_bound: "" },
        }),
      );
    },
  );

  it("deletes a self-computed P&L review barrier with the account-group axis", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

    const { deleteLimit } = api();
    await deleteLimit({
      policy: "spot_funds_pnl_bounds_kill_switch",
      mode: "review_with_hold",
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
      mode: "review_with_hold",
      accountGroup: "desk-alpha",
    });
  });

  describe("funds limit mode policy", () => {
    const modePolicy = {
      kind: "spot_funds_limit_mode",
      mode: null,
      scope: "global",
      account: "",
      accountGroup: "",
      asset: "",
      values: { spotFundsLimitMode: { mode: "track_only" } },
    };

    it("serializes the funds limit mode policy filter", async () => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ policies: [], total: 0 }),
      );

      const { fetchPoliciesPage } = api();
      await fetchPoliciesPage({ policy: "spot_funds_limit_mode" });

      const calledUrl = new URL(
        String(vi.mocked(fetch).mock.calls[0][0]),
        "http://test",
      );
      expect(calledUrl.pathname).toBe("/app/api/v1/limits");
      expect(Object.fromEntries(calledUrl.searchParams.entries())).toEqual({
        policy: "spot_funds_limit_mode",
      });
    });

    it("flattens the global mode row of the typed list", async () => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ policies: [modePolicy], total: 1 }),
      );

      const { fetchLimits } = api();

      await expect(fetchLimits()).resolves.toEqual([
        {
          policy: "spot_funds_limit_mode",
          mode: null,
          scope: "global",
          account: "",
          accountGroup: "",
          asset: "",
          values: { mode: "track_only" },
        },
      ]);
    });

    it.each([
      [
        "a non-null rule mode",
        { ...modePolicy, mode: "review_without_hold" },
        "Limit mode field for spot_funds_limit_mode in the API response must be null",
      ],
      [
        "a missing value",
        { ...modePolicy, values: {} },
        "Spot Funds limit mode is missing from the API response",
      ],
      [
        "a value without a mode",
        { ...modePolicy, values: { spotFundsLimitMode: {} } },
        "Spot Funds limit mode is missing from the API response",
      ],
      [
        "an unknown mode",
        {
          ...modePolicy,
          values: { spotFundsLimitMode: { mode: "always_allow" } },
        },
        'Spot Funds limit mode "always_allow" in the API response is unknown',
      ],
    ])("rejects a list row with %s", async (_case, policy, message) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ policies: [policy], total: 1 }),
      );

      const { fetchLimits } = api();

      await expect(fetchLimits()).rejects.toThrow(message);
    });

    it("refuses to add a mode before sending a request", async () => {
      await expect(
        api().addLimit(
          {
            policy: "spot_funds_limit_mode",
            mode: null,
            scope: "global",
            account: "",
            accountGroup: "",
            asset: "",
            values: { mode: "track_only" },
          },
          "reject",
        ),
      ).rejects.toThrow(
        "Spot Funds limit mode has no add operation; change it with putLimit.",
      );
      expect(fetch).not.toHaveBeenCalled();
    });

    it("puts the mode to its own endpoint with no missingAccount query", async () => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          spotFundsLimitMode: { scope: "global", mode: "track_only" },
        }),
      );

      const { putLimit } = api();
      const limit = await putLimit(
        {
          policy: "spot_funds_limit_mode",
          mode: null,
          scope: "global",
          account: "",
          accountGroup: "",
          asset: "",
          values: { mode: "track_only" },
        },
        "create",
      );

      const [url, init] = vi.mocked(fetch).mock.calls[0] as [
        string,
        RequestInit,
      ];
      expect(url).toBe("/app/api/v1/limits/spot-funds-limit-mode");
      expect(init.method).toBe("PUT");
      expect(JSON.parse(String(init.body))).toEqual({
        scope: "global",
        mode: "track_only",
      });
      expect(limit).toEqual({
        policy: "spot_funds_limit_mode",
        mode: null,
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { mode: "track_only" },
      });
    });

    it.each([
      ["no mode", {}],
      ["an unknown mode", { mode: "always_allow" }],
    ])("refuses to send %s", async (_case, values) => {
      const { putLimit } = api();

      await expect(
        putLimit(
          {
            policy: "spot_funds_limit_mode",
            mode: null,
            scope: "global",
            account: "",
            accountGroup: "",
            asset: "",
            values,
          },
          "reject",
        ),
      ).rejects.toThrow(
        "Spot Funds limit mode must be one of: enforce, review_without_hold, review_with_hold, track_only",
      );
      expect(fetch).not.toHaveBeenCalled();
    });

    it.each([
      [
        "no mode row",
        {},
        "Spot Funds limit mode is missing from the API response",
      ],
      [
        "no mode",
        { spotFundsLimitMode: { scope: "global" } },
        "Spot Funds limit mode is missing from the API response",
      ],
      [
        "no scope",
        { spotFundsLimitMode: { mode: "enforce" } },
        "Spot Funds limit mode scope is missing from the API response",
      ],
    ])("rejects a put answer with %s", async (_case, body, message) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse(body));

      const { putLimit } = api();

      await expect(
        putLimit(
          {
            policy: "spot_funds_limit_mode",
            mode: null,
            scope: "global",
            account: "",
            accountGroup: "",
            asset: "",
            values: { mode: "enforce" },
          },
          "reject",
        ),
      ).rejects.toThrow(message);
    });

    it("deletes the global row with no empty axis parameter", async () => {
      vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

      const { deleteLimit } = api();
      await deleteLimit({
        policy: "spot_funds_limit_mode",
        mode: null,
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
      });

      const [url, init] = vi.mocked(fetch).mock.calls[0] as [
        string,
        RequestInit,
      ];
      expect(url).toBe(
        "/app/api/v1/limits?policy=spot_funds_limit_mode&scope=global",
      );
      expect(init.method).toBe("DELETE");
    });
  });
});

describe("append-only list clients", () => {
  describe.each(["orders", "trades"])("%s venue filtering", (list) => {
    it.each(["any", "none", "connection:desk:primary"])(
      "sends the venue filter %s",
      async (venue) => {
        vi.mocked(fetch).mockResolvedValueOnce(
          jsonResponse({ [list]: [], total: 0 }),
        );
        const officerApi = api();
        if (list === "orders") {
          await officerApi.fetchOrdersPage({ venue });
        } else {
          await officerApi.fetchTradesPage({ venue });
        }

        const url = new URL(
          String(vi.mocked(fetch).mock.calls[0][0]),
          "http://test",
        );
        expect(url.pathname).toBe(`/app/api/v1/${list}`);
        expect(url.searchParams.get("venue")).toBe(venue);
      },
    );

    it.each([undefined, ""])(
      "keeps the request unchanged with venue %s",
      async (venue) => {
        vi.mocked(fetch).mockResolvedValueOnce(
          jsonResponse({ [list]: [], total: 0 }),
        );
        const officerApi = api();
        const filter = {
          account: "desk-alpha",
          source: "panel",
          limit: 25,
          offset: 50,
          ...(venue === undefined ? {} : { venue }),
        };
        if (list === "orders") {
          await officerApi.fetchOrdersPage(filter);
        } else {
          await officerApi.fetchTradesPage(filter);
        }

        expect(String(vi.mocked(fetch).mock.calls[0][0])).toBe(
          `/app/api/v1/${list}?account=desk-alpha&source=panel&limit=25&offset=50`,
        );
      },
    );

    it("preserves venueConnection on linked list rows", async () => {
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({
          [list]: [
            {
              id: "row-1",
              leavesQuantity: "0",
              venueConnection: "desk:primary",
            },
          ],
          total: 1,
        }),
      );
      const officerApi = api();
      const result =
        list === "orders"
          ? await officerApi.fetchOrdersPage()
          : await officerApi.fetchTradesPage();

      expect(result.items[0]).toHaveProperty("venueConnection", "desk:primary");
    });

    it.each([undefined, "", null, 42])(
      "omits venueConnection when the wire value is %s",
      async (venueConnection) => {
        vi.mocked(fetch).mockResolvedValueOnce(
          jsonResponse({
            [list]: [
              {
                id: "row-1",
                leavesQuantity: "0",
                ...(venueConnection === undefined ? {} : { venueConnection }),
              },
            ],
            total: 1,
          }),
        );
        const officerApi = api();
        const result =
          list === "orders"
            ? await officerApi.fetchOrdersPage()
            : await officerApi.fetchTradesPage();

        expect(result.items[0]).not.toHaveProperty("venueConnection");
      },
    );
  });

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
              request: {
                type: "correction",
                asset: "USD",
                realizedPnl: "-12.50",
              },
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
              detail:
                "Limit set: rate_limit (account) account=desk-alpha max_orders=5",
              detailCode: "set_limit",
              detailParams: { policy: "rate_limit", scope: "account" },
              detailFields: [
                { name: "account", value: "desk-alpha" },
                { name: "max_orders", value: "5" },
              ],
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
      type: "venue_fee",
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
          request: { type: "correction", asset: "USD" },
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
      items: [
        {
          id: "aud-1",
          detail: {
            text: "Limit set: rate_limit (account) account=desk-alpha max_orders=5",
            code: "set_limit",
            params: { policy: "rate_limit", scope: "account" },
            fields: [
              { name: "account", value: "desk-alpha" },
              { name: "max_orders", value: "5" },
            ],
          },
        },
      ],
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
    expect(adjustmentUrl.searchParams.get("type")).toBe("venue_fee");
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
      { type: "deposit", asset: "USD" },
      "reject",
    );

    expect(result).toBeNull();
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts/desk-alpha/adjustments?missingAccount=reject",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("posts the adjustment type and reads it back from the record", async () => {
    const body = {
      type: "withdrawal",
      asset: "USD",
      balance: { mode: "delta", value: "-25" },
    } as const;
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        adjustment: {
          id: "adj-2",
          account: "desk-alpha",
          at: "2026-06-24T00:00:00Z",
          source: "panel",
          status: "accepted",
          request: body,
          outcome: { accepted: { balanceDelta: "-25", balanceResult: "75" } },
        },
      }),
    );

    const result = await api().createAdjustment("desk-alpha", body, "reject");

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/accounts/desk-alpha/adjustments?missingAccount=reject",
      expect.objectContaining({ method: "POST", body: JSON.stringify(body) }),
    );
    expect(result?.request).toMatchObject({ type: "withdrawal", asset: "USD" });
  });

  it.each([
    ["missing", { asset: "USD" }],
    ["unknown", { type: "fee", asset: "USD" }],
    ["empty", { type: "", asset: "USD" }],
    ["only under the PascalCase key", { Type: "deposit", asset: "USD" }],
  ])("rejects an adjustment record whose type is %s", async (_, request) => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        adjustments: [
          {
            id: "adj-3",
            account: "desk-alpha",
            at: "2026-06-24T00:00:00Z",
            source: "panel",
            status: "accepted",
            request,
          },
        ],
        total: 1,
      }),
    );

    const result = api().fetchAdjustmentsPage();

    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({
      code: "internal",
      message: "Invalid or missing adjustment type in the API response",
    });
  });

  it("resends an adjustment with missingAccount=create after account creation is confirmed", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(null, { status: 204 }));

    await api().createAdjustment(
      "desk-alpha",
      { type: "deposit", asset: "USD" },
      "create",
    );

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
      api().createAdjustment(
        "acc-1",
        { type: "deposit", asset: "USD" },
        "reject",
      ),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "account_missing",
      account: "acc-1",
      status: 404,
    });
  });

  it("parses an asset_missing 404 into a typed ApiError carrying the asset code", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          error: {
            code: "asset_missing",
            message: 'upsert trading instrument: asset "USDT" does not exist',
            asset: "USDT",
          },
        }),
        { status: 404, headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(
      api().upsertTradingInstrument("alpaca-1", {
        externalSymbol: "BTC/USDT",
        baseAsset: "BTC",
        quoteAsset: "USDT",
        enabled: true,
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "asset_missing",
      asset: "USDT",
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
          marketDataCredentialsUnavailable: ["alpaca-primary", "oanda-fx"],
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
    expect(result.marketDataCredentialsUnavailable).toEqual([
      "alpaca-primary",
      "oanda-fx",
    ]);
    expect(result.restartRequired).toBe(true);
  });

  it("restores from an uploaded backup file payload", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        summary: {
          applied: {},
          skipped: {},
          restartRequired: false,
        },
      }),
    );

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

  it("normalizes an empty restore summary", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        summary: {
          applied: {},
          skipped: {},
          restartRequired: false,
        },
      }),
    );

    const result = await restoreBackup({
      archive: backupArchive,
      scope: { all: true },
      mode: "insert_missing",
    });

    expect(result.applied).toEqual({});
    expect(result.skipped).toEqual({});
    expect(result.marketDataCredentialsUnavailable).toBeUndefined();
    expect(result.tradingCredentialsUnavailable).toBeUndefined();
    expect(result.tradingDisabledNonRelease).toBeUndefined();
    expect(result.restartRequired).toBe(false);
  });

  it.each(["applied", "skipped", "restartRequired"])(
    "rejects a restore summary with an absent %s field",
    async (field) => {
      const summary = {
        applied: {},
        skipped: {},
        restartRequired: false,
      };
      delete summary[field as keyof typeof summary];
      vi.mocked(fetch).mockResolvedValue(jsonResponse({ summary }));

      await expect(
        restoreBackup({
          archive: backupArchive,
          scope: { all: true },
          mode: "insert_missing",
        }),
      ).rejects.toThrow("The service encountered an internal error.");
    },
  );

  it.each([
    ["applied", null],
    ["applied", "bad"],
    ["applied", 1],
    ["skipped", null],
    ["skipped", "bad"],
    ["skipped", 1],
    ["restartRequired", "true"],
  ])(
    "rejects a restore summary with a malformed %s field",
    async (field, value) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          summary: {
            applied: {},
            skipped: {},
            restartRequired: false,
            [field]: value,
          },
        }),
      );

      await expect(
        restoreBackup({
          archive: backupArchive,
          scope: { all: true },
          mode: "insert_missing",
        }),
      ).rejects.toThrow("The service encountered an internal error.");
    },
  );

  it.each([
    "tradingCredentialsUnavailable",
    "TradingCredentialsUnavailable",
    "trading_credentials_unavailable",
  ])("normalizes unavailable trading credentials from %s", async (field) => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        summary: {
          applied: { trading: 2 },
          skipped: {},
          restartRequired: false,
          [field]: ["Alpaca paper", "Second connection"],
        },
      }),
    );
    const result = await restoreBackup({
      archive: backupArchive,
      scope: { all: true },
      mode: "overwrite",
    });
    expect(result.tradingCredentialsUnavailable).toEqual([
      "Alpaca paper",
      "Second connection",
    ]);
    expect(result.applied.trading).toBe(2);
  });

  it.each([null, "Alpaca paper", 1, {}, ["Alpaca paper", 1]])(
    "rejects malformed unavailable trading credentials: %j",
    async (unavailable) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          summary: {
            applied: {},
            skipped: {},
            restartRequired: false,
            tradingCredentialsUnavailable: unavailable,
          },
        }),
      );
      await expect(
        restoreBackup({
          archive: backupArchive,
          scope: { all: true },
          mode: "overwrite",
        }),
      ).rejects.toThrow("The service encountered an internal error.");
    },
  );

  it.each([
    "tradingDisabledNonRelease",
    "TradingDisabledNonRelease",
    "trading_disabled_non_release",
  ])(
    "normalizes non-release disabled trading connections from %s",
    async (field) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          summary: {
            applied: { trading: 2 },
            skipped: {},
            restartRequired: false,
            [field]: ["Alpaca paper", "Second connection"],
          },
        }),
      );
      const result = await restoreBackup({
        archive: backupArchive,
        scope: { all: true },
        mode: "overwrite",
      });
      expect(result.tradingDisabledNonRelease).toEqual([
        "Alpaca paper",
        "Second connection",
      ]);
    },
  );

  it.each([null, "Alpaca paper", 1, {}, ["Alpaca paper", 1]])(
    "rejects malformed non-release disabled trading connections: %j",
    async (disabled) => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({
          summary: {
            applied: {},
            skipped: {},
            restartRequired: false,
            tradingDisabledNonRelease: disabled,
          },
        }),
      );
      await expect(
        restoreBackup({
          archive: backupArchive,
          scope: { all: true },
          mode: "overwrite",
        }),
      ).rejects.toMatchObject({
        code: "internal",
        message: "The service encountered an internal error.",
      });
    },
  );

  it("rejects malformed unavailable market-data credentials", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        summary: {
          applied: {},
          skipped: {},
          restartRequired: false,
          marketDataCredentialsUnavailable: ["alpaca-primary", 1],
        },
      }),
    );

    await expect(
      restoreBackup({
        archive: backupArchive,
        scope: { all: true },
        mode: "insert_missing",
      }),
    ).rejects.toThrow("The service encountered an internal error.");
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

  describe("pre-migration backups", () => {
    const copy = {
      name: "officer db/pre#1.bak",
      createdAt: "2026-01-02T03:04:05Z",
      sizeBytes: 2048,
    };
    const encodedName = "officer%20db%2Fpre%231.bak";

    it("lists the copies in the order the server sent them", async () => {
      const older = {
        name: "officer.db.pre-migration-older",
        createdAt: "2025-12-01T01:01:01.5+02:00",
        sizeBytes: 0,
      };
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ backups: [copy, older] }),
      );
      const signal = new AbortController().signal;

      const result = await listPreMigrationBackups(signal);

      expect(fetch).toHaveBeenCalledWith(
        "/app/api/v1/database/pre-migration-backups",
        expect.objectContaining({ method: "GET", signal }),
      );
      expect(result).toEqual([copy, older]);
    });

    it("returns an empty list the server sent as empty", async () => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse({ backups: [] }));

      await expect(listPreMigrationBackups()).resolves.toEqual([]);
    });

    it.each([
      ["a body without backups", {}],
      ["a backups value that is not an array", { backups: "none" }],
      ["a copy with an empty name", { backups: [{ ...copy, name: "" }] }],
      [
        "a copy with a createdAt that is not RFC 3339",
        { backups: [{ ...copy, createdAt: "2026-01-02 03:04:05" }] },
      ],
      [
        "a copy with a negative size",
        { backups: [{ ...copy, sizeBytes: -1 }] },
      ],
      [
        "a copy with a fractional size",
        { backups: [{ ...copy, sizeBytes: 1.5 }] },
      ],
    ])("rejects %s as an internal error", async (_, body) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse(body));

      const failure = await listPreMigrationBackups().catch(
        (err: unknown) => err,
      );

      expect(failure).toBeInstanceOf(ApiError);
      expect(failure).toMatchObject({ code: "internal" });
    });

    it("downloads a copy with a bodyless POST to its encoded name", async () => {
      vi.mocked(fetch).mockResolvedValue(
        new Response("sqlite-bytes", {
          status: 200,
          headers: {
            "Content-Type": "application/octet-stream",
            "Content-Disposition": 'attachment; filename="served-copy.bak"',
          },
        }),
      );

      const result = await downloadPreMigrationBackup(copy.name);

      expect(fetch).toHaveBeenCalledWith(
        `/app/api/v1/database/pre-migration-backups/${encodedName}/download`,
        expect.objectContaining({
          method: "POST",
          headers: expect.objectContaining({
            Accept: "application/octet-stream",
          }),
          body: undefined,
        }),
      );
      expect(result.filename).toBe("served-copy.bak");
      expect(result.blob.size).toBe("sqlite-bytes".length);
    });

    it("deletes a copy with a DELETE to its encoded name", async () => {
      vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

      await expect(
        deletePreMigrationBackup(copy.name),
      ).resolves.toBeUndefined();

      expect(fetch).toHaveBeenCalledWith(
        `/app/api/v1/database/pre-migration-backups/${encodedName}`,
        expect.objectContaining({ method: "DELETE" }),
      );
    });
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

  it("keeps the pair a match states and drops empty assets", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        supported: true,
        matches: [
          {
            symbol: "ETHUSDT",
            secType: "SPOT",
            baseAsset: "ETH",
            quoteAsset: "USDT",
          },
          { symbol: "GBTC", secType: "SPOT", baseAsset: "", quoteAsset: "" },
        ],
      }),
    );

    const result = await searchMarketDataSymbols("binance-1", {
      query: "ETH",
    });

    expect(result.matches).toEqual([
      {
        symbol: "ETHUSDT",
        secType: "SPOT",
        baseAsset: "ETH",
        quoteAsset: "USDT",
      },
      { symbol: "GBTC", secType: "SPOT" },
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
// Signing keys - normalizer unit tests
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
// Signing keys - HTTP call tests
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
          signedEnvelope: "env-verbatim",
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
            signedApproval: "env-verbatim",
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
    expect(result.attestation?.signedEnvelope).toBe("env-verbatim");
    expect(result.attestation?.mode).toBe("immediate");
    expect(result.response?.submitResponse?.signedApproval).toBe(
      "env-verbatim",
    );
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

  it("decodes a snake_case signed envelope", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        attestation: {
          signed_envelope: "env-snake-envelope",
          mode: "hold",
        },
      }),
    );

    const result = await fetchEventReproduction("ord-1", "evt-1");

    expect(result.attestation?.signedEnvelope).toBe("env-snake-envelope");
  });

  it("rejects an attestation with an unknown submit mode", async () => {
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
          signedEnvelope: "env-verbatim",
          keyId: "key-1",
          alg: "ed25519",
          requestType: "submit",
          mode: "unexpected",
          issuedAt: "2026-06-24T00:00:00Z",
          signed: true,
        },
      }),
    );

    const result = fetchEventReproduction("ord-1", "evt-1");
    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({
      code: "internal",
      message: "Invalid submit mode in the API response",
    });
  });

  it("decodes a snake_case signed approval response", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        response: {
          submitResponse: {
            signed_approval: "env-snake-response",
            keyId: "key-1",
            id: "ord-1",
            verdict: "accept",
          },
        },
      }),
    );

    const result = await fetchEventReproduction("ord-1", "evt-1");

    expect(result.response?.submitResponse?.signedApproval).toBe(
      "env-snake-response",
    );
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

  it("fetchEventReproduction normalizes an execution-report facet with its verbatim envelope", async () => {
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
          signedEnvelope: "env-fill",
          keyId: "key-1",
          alg: "ed25519",
          requestType: "execution_report",
          mode: "hold",
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
            signedAttestation: "env-fill",
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
    expect(result.attestation?.mode).toBe("hold");
    expect(result.event).toMatchObject({
      fillQuantity: "10",
      fillPrice: "150.25",
      leavesQuantity: "0",
      orderStatus: "filled",
      commission: { amount: "-0.50", currency: "USD" },
    });
    expect(result.response?.executionReport?.signedAttestation).toBe(
      "env-fill",
    );
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
// Orders - submit creates a signed approval envelope and addresses by external id
// ---------------------------------------------------------------------------

describe("Orders createOrder submit lifecycle", () => {
  function approvalResponse(
    orderExternalId = "ord_alpha_0000000001",
  ): Response {
    return new Response(
      JSON.stringify({
        signedApproval: "approval-envelope",
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
          signedApproval: "approval-envelope",
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
      signedApproval: "approval-envelope",
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

  it("decodes a snake_case signed approval", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            signed_approval: "approval-envelope-snake",
            keyId: "key-1",
            id: "ord_alpha_0000000001",
            verdict: "accept",
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      )
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

    expect(result.approval?.signedApproval).toBe("approval-envelope-snake");
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

  it("normalizes wrapped approval envelopes", async () => {
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

  it.each(["reject", "review"])(
    "normalizes submitResponse %s verdict and reasons",
    async (verdict) => {
      vi.mocked(fetch)
        .mockResolvedValueOnce(
          new Response(
            JSON.stringify({
              order: { id: "ord_rejected_0000001" },
              submitResponse: {
                signedApproval: "reject-envelope",
                keyId: "key-1",
                id: "ord_rejected_0000001",
                verdict,
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
        signedApproval: "reject-envelope",
        reviews: [],
        keyId: "key-1",
        id: "ord_rejected_0000001",
        verdict,
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
    },
  );

  it.each([
    ["defer", '"defer"'],
    [undefined, "undefined"],
    ["", '""'],
  ])(
    "reports an invalid submitResponse verdict %j",
    async (verdict, received) => {
      vi.mocked(fetch)
        .mockResolvedValueOnce(
          new Response(
            JSON.stringify({
              order: { id: "ord_invalid_0000001" },
              submitResponse: {
                signedApproval: "order-envelope",
                keyId: "key-1",
                id: "ord_invalid_0000001",
                verdict,
              },
            }),
            { status: 201, headers: { "Content-Type": "application/json" } },
          ),
        )
        .mockResolvedValueOnce(orderResponse("ord_invalid_0000001"));
      const { createOrder } = api();
      const result = createOrder(
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
      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "internal",
        message: expect.stringContaining(received),
      });
    },
  );

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
        signedAttestation: "env-report",
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
    expect(result.signedAttestation).toBe("env-report");
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
        signedAttestation: "env-confirm",
        attestationKeyId: "key-9",
        signed: true,
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }

  it("confirmOrder sends a body-less POST and decodes the mutation response", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(orderMutationResponse());

    const result = await api().confirmOrder("ord_alpha_0000000001");

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/ord_alpha_0000000001/confirm",
      expect.objectContaining({
        method: "POST",
        body: undefined,
      }),
    );
    expect(result.order).toMatchObject({
      id: "ord_alpha_0000000001",
      leavesQuantity: "0",
      price: "150.25",
      status: "filled",
    });
    expect(result.signedAttestation).toBe("env-confirm");
    expect(result.attestationKeyId).toBe("key-9");
    expect(result.signed).toBe(true);
  });

  it("cancelOrder sends leaves and reason and decodes the mutation response", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(orderMutationResponse());

    const result = await api().cancelOrder("ord_alpha_0000000001", {
      leavesQuantity: "7.5",
      reason: "operator request",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/ord_alpha_0000000001/cancel",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
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
    expect(result.signedAttestation).toBe("env-confirm");
    expect(result.attestationKeyId).toBe("key-9");
    expect(result.signed).toBe(true);
  });
});

describe("Order review client", () => {
  it.each([
    "submitOrder",
    "sendPaperOrder",
    "sendLiveOrder",
    "confirmOrderReview",
    "forceOrderReview",
    "cancelOrderReview",
  ] as const)(
    "strictly decodes optional alertFailure on %s",
    async (operation) => {
      const request = () => {
        if (operation === "submitOrder") {
          return api()
            .submitOrder({ ...input, mode: "hold" }, "reject")
            .then((result) => result.approval);
        }
        if (operation === "sendPaperOrder" || operation === "sendLiveOrder") {
          return api()[operation]({
            ...input,
            destination: {
              connection: "paper",
              venueAccount: "desk",
              route: {},
            },
          });
        }
        return api()[operation](order.id);
      };
      const response = {
        id: order.id,
        verdict: "review",
        reviews: [mark],
        order,
        decision: {
          verdict: "accept",
          reasons: [],
          signedApproval: "approval",
          keyId: "key",
          signed: true,
        },
        signed: true,
      };
      for (const step of [undefined, "publish", "close"] as const) {
        const alertFailure = step
          ? { step, message: "alert service unavailable" }
          : undefined;
        vi.mocked(fetch).mockResolvedValueOnce(
          jsonResponse({
            ...response,
            ...(alertFailure ? { alertFailure } : {}),
          }),
        );
        const result = await request();
        if (alertFailure) {
          expect(result).toHaveProperty("alertFailure", alertFailure);
        } else {
          expect(result).not.toHaveProperty("alertFailure");
        }
      }
      for (const alertFailure of [
        null,
        [],
        "failure",
        {},
        { step: "retry", message: "unavailable" },
        { step: "publish" },
        { step: "close", message: 42 },
      ]) {
        vi.mocked(fetch).mockResolvedValueOnce(
          jsonResponse({ ...response, alertFailure }),
        );
        await expect(request()).rejects.toMatchObject({
          code: "internal",
          message: "Invalid alertFailure in the API response",
        });
      }
    },
  );

  it("preserves alertFailure when enriching a created order fails", async () => {
    const alertFailure = {
      step: "publish",
      message: "alert service unavailable",
    };
    vi.mocked(fetch)
      .mockResolvedValueOnce(
        jsonResponse({ id: order.id, verdict: "review", alertFailure }),
      )
      .mockRejectedValueOnce(new Error("detail unavailable"));
    const result = await api().createOrder(
      { ...input, mode: "hold" },
      "reject",
    );
    expect(result.approval?.alertFailure).toEqual(alertFailure);
    expect(result.warning).toContain("detail unavailable");
  });

  const order = {
    id: "order/1",
    leavesQuantity: "10",
    status: "awaiting_review",
  };
  const mark = {
    code: "policy_reject",
    scope: "account",
    policy: "order_size",
    reason: "Size needs review",
    details: "100 > 50",
    mode: "review_without_hold",
  };
  const review = {
    held: true,
    marks: [mark],
    lastConfirm: {
      verdict: "reject",
      reasons: [{ ...mark, mode: undefined }],
      at: "2026-11-19T10:00:00Z",
    },
    destination: {
      connection: "paper-1",
      venueAccount: "PA-1",
      route: { timeInForce: "day" },
    },
  };
  const venueOrder = {
    connection: "paper-1",
    venueAccount: "PA-1",
    route: {},
    clientOrderId: "order/1",
    venueOrderId: "venue-1",
    sendAttempted: true,
    createdAt: "2026-11-19T10:01:00Z",
  };

  const input = {
    account: "desk-alpha",
    baseAsset: "AAPL",
    quoteAsset: "USD",
    side: "buy" as const,
    amountKind: "quantity" as const,
    amountValue: "10",
  };

  it.each([undefined, [mark]])(
    "normalizes check reviews %j",
    async (reviews) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({
          check: {
            passed: true,
            rejects: [],
            ...(reviews === undefined ? {} : { reviews }),
          },
        }),
      );
      expect((await api().checkOrder(input)).reviews).toEqual(reviews ?? []);
    },
  );

  it.each([
    null,
    {},
    "marks",
    [{ ...mark, mode: "unknown" }],
    [{ ...mark, mode: "reject" }],
  ])("refuses invalid check reviews %j", async (reviews) => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ check: { passed: true, reviews } }),
    );
    await expect(api().checkOrder(input)).rejects.toMatchObject({
      code: "internal",
    });
  });

  it("preserves review marks on a submit", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ id: order.id, verdict: "review", reviews: [mark] }),
    );
    const result = await api().submitOrder(
      { ...input, mode: "hold" },
      "reject",
    );
    expect(result.approval).toMatchObject({
      verdict: "review",
      reviews: [mark],
    });
  });

  it.each(["paper", "live"] as const)(
    "preserves review marks on a manual %s send",
    async (mode) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({ id: order.id, verdict: "review", reviews: [mark] }),
      );
      const send =
        mode === "paper" ? api().sendPaperOrder : api().sendLiveOrder;
      const result = await send({
        ...input,
        destination: { connection: "paper-1", venueAccount: "PA-1", route: {} },
      });
      expect(result).toMatchObject({ verdict: "review", reviews: [mark] });
      expect(result).not.toHaveProperty("venueSend");
    },
  );

  it.each(["reviewConfirm", "reviewCancel", "reviewForce"] as const)(
    "normalizes the %s reproduction facet",
    async (facet) => {
      const decision = {
        verdict: "accept",
        reasons: [],
        signedApproval: "review-envelope",
        keyId: "key-1",
        signed: true,
      };
      const cancel = {
        order: { ...order, status: "cancelled" },
        signedAttestation: "cancel-envelope",
        attestationKeyId: "key-1",
        signed: true,
      };
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({
          event: { id: "event-1" },
          response: { [facet]: facet !== "reviewCancel" ? decision : cancel },
        }),
      );
      const result = await api().fetchEventReproduction(order.id, "event-1");
      if (facet !== "reviewCancel") {
        expect(result.response?.[facet]).toEqual(decision);
        expect(result.response?.[facet]).not.toHaveProperty("order");
      } else {
        expect(result.response?.reviewCancel).toMatchObject(cancel);
      }
    },
  );

  it("rejects an invalid reviewForce reproduction verdict", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        event: { id: "event-1" },
        response: { reviewForce: { verdict: "bogus" } },
      }),
    );
    const result = api().fetchEventReproduction(order.id, "event-1");
    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({
      code: "internal",
      message: "Invalid review decision verdict in the API response",
    });
  });

  it.each([
    review,
    { held: false, marks: [mark], lastConfirm: null, destination: null },
  ])(
    "fetches review marks, last confirmation and destination: %j",
    async (payload) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        jsonResponse({ order, events: [], trades: [], review: payload }),
      );
      const result = await api().fetchOrderDetail(order.id);
      expect(result.review).toEqual(payload);
      expect(fetch).toHaveBeenCalledWith(
        "/app/api/v1/orders/order%2F1",
        expect.objectContaining({ method: "GET" }),
      );
    },
  );

  it("leaves review absent for a non-waiting order", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        order: { ...order, status: "committed" },
        events: [],
        trades: [],
      }),
    );
    expect(await api().fetchOrderDetail(order.id)).not.toHaveProperty("review");
  });

  it("refuses a waiting order whose review is missing", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ order, events: [], trades: [] }),
    );
    await expect(api().fetchOrderDetail(order.id)).rejects.toMatchObject({
      code: "internal",
      message: "waiting order has no review in the API response",
    });
  });

  it.each([
    { verdict: "accept", venueSend: { status: "acknowledged", venueOrder } },
    { verdict: "accept" },
    { verdict: "reject" },
  ])(
    "confirms review with %j using a body-less encoded POST",
    async ({ verdict, venueSend }) => {
      const decision = {
        verdict,
        reasons: verdict === "reject" ? [mark] : [],
        signedApproval: "approval",
        keyId: "key-1",
        signed: true,
      };
      const response = { order, decision, ...(venueSend ? { venueSend } : {}) };
      vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(response));
      const signal = new AbortController().signal;
      const result = await api().confirmOrderReview(order.id, signal);
      expect(result).toEqual({
        ...response,
        order: expect.objectContaining(order),
        decision: {
          ...decision,
          reasons: decision.reasons.map(
            ({ code, scope, policy, reason, details }) => ({
              code,
              scope,
              policy,
              reason,
              details,
            }),
          ),
        },
      });
      expect(fetch).toHaveBeenCalledWith(
        "/app/api/v1/orders/order%2F1/review/confirm",
        expect.objectContaining({ method: "POST", body: undefined, signal }),
      );
      if (!venueSend) expect(result).not.toHaveProperty("venueSend");
    },
  );

  it.each([true, false])(
    "forces review with venue send=%s using a body-less encoded POST",
    async (trading) => {
      const decision = {
        verdict: "accept",
        reasons: [],
        signedApproval: "force-approval",
        keyId: "key-1",
        signed: true,
      };
      const response = {
        order,
        decision,
        ...(trading
          ? { venueSend: { status: "acknowledged", venueOrder } }
          : {}),
      };
      vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(response));
      const signal = new AbortController().signal;
      expect(await api().forceOrderReview(order.id, signal)).toEqual({
        ...response,
        order: expect.objectContaining(order),
      });
      expect(fetch).toHaveBeenCalledWith(
        "/app/api/v1/orders/order%2F1/review/force",
        expect.objectContaining({ method: "POST", body: undefined, signal }),
      );
    },
  );

  it("cancels review using a body-less encoded POST", async () => {
    const response = {
      order: { ...order, status: "cancelled" },
      signedAttestation: "cancel",
      attestationKeyId: "key-1",
      signed: true,
    };
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(response));
    const signal = new AbortController().signal;
    expect(await api().cancelOrderReview(order.id, signal)).toMatchObject(
      response,
    );
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders/order%2F1/review/cancel",
      expect.objectContaining({ method: "POST", body: undefined, signal }),
    );
  });

  it.each([
    "confirmOrderReview",
    "cancelOrderReview",
    "forceOrderReview",
  ] as const)(
    "%s refuses a non-object 2xx instead of inventing a result",
    async (operation) => {
      vi.mocked(fetch).mockResolvedValueOnce(jsonResponse([]));
      await expect(api()[operation](order.id)).rejects.toMatchObject({
        code: "internal",
        message: expect.stringContaining("not a JSON object"),
      });
    },
  );

  it("refuses an invalid venue send outcome on review confirmation", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        order,
        decision: { verdict: "accept", reasons: [] },
        venueSend: { status: "made_up" },
      }),
    );
    await expect(api().confirmOrderReview(order.id)).rejects.toMatchObject({
      code: "internal",
      message: "Invalid venue send status in the API response",
    });
  });

  it.each([
    [
      "missing held",
      { ...review, held: undefined },
      "Invalid order review in the API response",
    ],
    [
      "non-boolean held",
      { ...review, held: "false" },
      "Invalid order review in the API response",
    ],
    [
      "unknown mark mode",
      { ...review, marks: [{ ...mark, mode: "unknown" }] },
      "Invalid review rule mode in the API response",
    ],
    [
      "non-reject lastConfirm verdict",
      { ...review, lastConfirm: { ...review.lastConfirm, verdict: "accept" } },
      "Invalid order review in the API response",
    ],
    [
      "missing lastConfirm.at",
      { ...review, lastConfirm: { ...review.lastConfirm, at: undefined } },
      "review lastConfirm at is missing from the API response",
    ],
    [
      "non-string lastConfirm.at",
      { ...review, lastConfirm: { ...review.lastConfirm, at: 123 } },
      "review lastConfirm at is missing from the API response",
    ],
    [
      "non-array lastConfirm.reasons",
      { ...review, lastConfirm: { ...review.lastConfirm, reasons: {} } },
      "Invalid order review in the API response",
    ],
    [
      "missing destination.connection",
      {
        ...review,
        destination: { ...review.destination, connection: undefined },
      },
      "review destination connection is missing from the API response",
    ],
    [
      "missing destination.venueAccount",
      {
        ...review,
        destination: { ...review.destination, venueAccount: undefined },
      },
      "review destination venueAccount is missing from the API response",
    ],
  ])("refuses %s in order review detail", async (_, payload, message) => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ order, events: [], trades: [], review: payload }),
    );
    await expect(api().fetchOrderDetail(order.id)).rejects.toMatchObject({
      code: "internal",
      message,
    });
  });

  it("refuses decision verdict review on review confirmation", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        order,
        decision: { verdict: "review", reasons: [] },
      }),
    );
    await expect(api().confirmOrderReview(order.id)).rejects.toMatchObject({
      code: "internal",
      message: "Invalid review decision verdict in the API response",
    });
  });

  it("refuses venueSend on a rejected review confirmation", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        order,
        decision: { verdict: "reject", reasons: [] },
        venueSend: { status: "acknowledged", venueOrder },
      }),
    );
    await expect(api().confirmOrderReview(order.id)).rejects.toMatchObject({
      code: "internal",
      message:
        "venueSend requires an accepted review decision in the API response",
    });
  });

  it.each([undefined, null, [], "accept"])(
    "refuses missing or non-object decision %j with a decision-specific error",
    async (decision) => {
      vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ order, decision }));
      await expect(api().confirmOrderReview(order.id)).rejects.toMatchObject({
        code: "internal",
        message:
          "review confirmation decision is missing or not an object in the API response",
      });
    },
  );
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

  function approvalRequiredResponse(): Response {
    return new Response(
      JSON.stringify({
        error: {
          code: "approval_required",
          message: "backend detail is not client-owned",
        },
      }),
      { status: 409, headers: { "Content-Type": "application/json" } },
    );
  }

  it("confirmOrder surfaces execution_report_required on a 409", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(executionReportRequiredResponse());

    await expect(
      api().confirmOrder("ord_alpha_0000000001"),
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
        leavesQuantity: "7.5",
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "execution_report_required",
      status: 409,
    });
  });

  it("confirmOrder surfaces approval_required on a 409", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(approvalRequiredResponse());

    await expect(
      api().confirmOrder("ord_alpha_0000000001"),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "approval_required",
      status: 409,
      message:
        "This order has no verifiable pre-trade approval recorded by Officer at submit, so the confirm/cancel shortcut is unavailable. Submit a complete execution report through the order workflow.",
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

describe("access error decode", () => {
  const accessCodes = [
    [401, "unauthorized"],
    [401, "invalid_password"],
    [401, "invalid_recovery_code"],
    [403, "current_password_invalid"],
    [403, "wrong_token"],
    [403, "host_not_allowed"],
    [403, "origin_not_allowed"],
    [409, "setup_complete"],
    [415, "unsupported_media_type"],
    [429, "too_many_attempts"],
    [503, "setup_required"],
  ] as const;

  const channels = {
    request: (client: ApiClient) => client.request("/app/api/v1/session"),
    requestBlob: (client: ApiClient) =>
      client.requestBlob("/app/api/v1/session", {
        accept: "text/csv",
        fallbackFilename: "session.csv",
      }),
  };

  function client(onUnauthorized?: () => void): ApiClient {
    return createApiClient({
      baseUrl: "/app/api/v1",
      fetch: globalThis.fetch,
      onUnauthorized,
    });
  }

  it.each(accessCodes)(
    "passes %i %s through with the server message",
    async (status, code) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        errorResponse(status, code, {}, "server detail"),
      );

      await expect(channels.request(client())).rejects.toMatchObject({
        name: "ApiError",
        code,
        status,
        message: "server detail",
      });
    },
  );

  it.each(accessCodes)(
    "uses the catalog text for %i %s without a server message",
    async (status, code) => {
      const catalogs = ["en", "ru", "zh-CN", "es", "pt-BR"].map(
        (locale) =>
          (
            appLocaleResources[locale]?.errors as {
              code: Record<string, string>;
            }
          ).code[code],
      );
      vi.mocked(fetch).mockResolvedValueOnce(
        errorResponse(status, code, {}, ""),
      );

      await expect(channels.request(client())).rejects.toMatchObject({
        code,
        message: catalogs[0],
      });
      for (const text of catalogs) {
        expect(text).toEqual(expect.stringMatching(/\S/));
      }
    },
  );

  it.each([
    ["an unknown code", errorResponse(401, "session_gone", {}, "")],
    ["a code-less 401", new Response("", { status: 401 })],
    ["a code-less 429", new Response("", { status: 429 })],
  ])("decodes %s as internal", async (_name, response) => {
    vi.mocked(fetch).mockResolvedValueOnce(response);

    await expect(channels.request(client())).rejects.toMatchObject({
      code: "internal",
    });
  });

  it("keeps a code-less 503 as engine_restarting", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response("", { status: 503 }));

    await expect(channels.request(client())).rejects.toMatchObject({
      code: "engine_restarting",
      status: 503,
    });
  });

  it("decodes a 503 carrying setup_required as setup_required", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      errorResponse(503, "setup_required", {}, ""),
    );

    await expect(channels.request(client())).rejects.toMatchObject({
      code: "setup_required",
      status: 503,
    });
  });

  it.each([
    [401, "invalid_password", "120", 120],
    [429, "too_many_attempts", "0", 0],
  ])(
    "reads delta-seconds Retry-After on %i %s",
    async (status, code, header, seconds) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        errorResponse(status, code, { "Retry-After": header }, ""),
      );

      await expect(channels.request(client())).rejects.toMatchObject({
        code,
        retryAfterSeconds: seconds,
      });
    },
  );

  it.each([
    ["rounded up", Date.UTC(2026, 9, 16, 12, 0, 0, 250), 30],
    ["never below zero", Date.UTC(2026, 9, 16, 12, 5, 0), 0],
  ])(
    "reads an HTTP-date Retry-After as whole seconds from now, %s",
    async (_name, now, seconds) => {
      vi.spyOn(Date, "now").mockReturnValue(now);
      vi.mocked(fetch).mockResolvedValueOnce(
        errorResponse(
          429,
          "too_many_attempts",
          {
            "Retry-After": "Fri, 16 Oct 2026 12:00:30 GMT",
          },
          "",
        ),
      );

      await expect(channels.request(client())).rejects.toMatchObject({
        retryAfterSeconds: seconds,
      });
    },
  );

  it("leaves retryAfterSeconds undefined without a Retry-After header", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      errorResponse(429, "too_many_attempts", {}, ""),
    );

    await expect(channels.request(client())).rejects.toMatchObject({
      code: "too_many_attempts",
      retryAfterSeconds: undefined,
    });
  });

  it.each(["soon", "1.5", "-5", "abc 12", "2026-10-16T12:00:30Z"])(
    "leaves retryAfterSeconds undefined for an unparseable Retry-After %j",
    async (header) => {
      vi.mocked(fetch).mockResolvedValueOnce(
        errorResponse(429, "too_many_attempts", { "Retry-After": header }, ""),
      );

      await expect(channels.request(client())).rejects.toMatchObject({
        code: "too_many_attempts",
        retryAfterSeconds: undefined,
      });
    },
  );

  it.each(["request", "requestBlob"] as const)(
    "calls onUnauthorized once, before throwing, for a 401 unauthorized through %s",
    async (channel) => {
      const seen: string[] = [];
      vi.mocked(fetch).mockResolvedValueOnce(
        errorResponse(401, "unauthorized", {}, ""),
      );

      await channels[channel](client(() => seen.push("hook"))).catch(
        (err: unknown) => {
          expect(err).toMatchObject({ code: "unauthorized", status: 401 });
          seen.push("thrown");
        },
      );

      expect(seen).toEqual(["hook", "thrown"]);
    },
  );

  it.each([
    [401, "invalid_password"],
    [401, "invalid_recovery_code"],
    [403, "wrong_token"],
  ])("does not call onUnauthorized for %i %s", async (status, code) => {
    const onUnauthorized = vi.fn();
    vi.mocked(fetch).mockResolvedValueOnce(errorResponse(status, code, {}, ""));

    await expect(
      channels.request(client(onUnauthorized)),
    ).rejects.toMatchObject({ code, status });
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("does not call onUnauthorized for a network failure", async () => {
    const onUnauthorized = vi.fn();
    vi.mocked(fetch).mockRejectedValueOnce(new Error("connection reset"));

    await expect(
      channels.request(client(onUnauthorized)),
    ).rejects.toMatchObject({ code: "network" });
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("does not call onUnauthorized for an aborted request", async () => {
    const onUnauthorized = vi.fn();
    vi.mocked(fetch).mockRejectedValueOnce(
      new DOMException("The operation was aborted.", "AbortError"),
    );

    await expect(
      channels.request(client(onUnauthorized)),
    ).rejects.toMatchObject({ name: "AbortError" });
    expect(onUnauthorized).not.toHaveBeenCalled();
  });
});
