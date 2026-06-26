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

import {
  createMarketDataInstance,
  exportBusinessCsv,
  exportBackup,
  fetchSigningKeys,
  generateSigningKey,
  importBusinessCsv,
  importSigningKey,
  normalizeSigningKeysStatus,
  previewBusinessCsvImport,
  resetDatabase,
  restoreBackup,
  searchMarketDataSymbols,
  setESignEnabled,
  updateMarketDataInstanceSettings,
  verifyMarketDataSymbol,
} from "@/api/client";
import type { BackupArchive } from "@/api/types";

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
    format: "openpit.officer.backup",
    formatVersion: 1,
    schemaVersion: 2,
    createdAt: "2026-06-22T10:00:00Z",
    sections: ["accounts_groups"],
  },
  data: { accounts: [] },
};

describe("business CSV client", () => {
  it("exports CSV with entity, delimiter, zip flag, filters, and filename", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response("account_id\nacc-default\n", {
        status: 200,
        headers: {
          "Content-Type": "text/csv",
          "Content-Disposition": "attachment; filename=\"accounts.csv\"",
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

  it("previews and applies imports with payloadBase64 and conflictPolicy", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(
        jsonResponse({
          preview: {
            file: { name: "accounts.csv", type: "csv" },
            counts: {
              rows: 2,
              applied: 0,
              skipped: 0,
              conflicts: 1,
              stopped: false,
            },
            conflicts: [{ row: 2, key: "desk-alpha" }],
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          result: {
            file: { name: "accounts.csv", type: "csv" },
            counts: {
              rows: 2,
              applied: 1,
              skipped: 1,
              conflicts: 1,
              stopped: false,
            },
            conflicts: [{ row: 2, key: "desk-alpha" }],
          },
        }),
      );

    const preview = await previewBusinessCsvImport({
      entity: "accounts",
      delimiter: "comma",
      filename: "accounts.csv",
      payloadBase64: "YWNjb3VudF9pZAo=",
    });
    const result = await importBusinessCsv({
      entity: "accounts",
      delimiter: "comma",
      filename: "accounts.csv",
      payloadBase64: "YWNjb3VudF9pZAo=",
      conflictPolicy: "replace",
    });

    const previewBody = JSON.parse(
      String((vi.mocked(fetch).mock.calls[0][1] as RequestInit).body),
    );
    const importBody = JSON.parse(
      String((vi.mocked(fetch).mock.calls[1][1] as RequestInit).body),
    );
    expect(previewBody).toEqual({
      entity: "accounts",
      delimiter: "comma",
      filename: "accounts.csv",
      payloadBase64: "YWNjb3VudF9pZAo=",
    });
    expect(importBody).toEqual({
      ...previewBody,
      conflictPolicy: "replace",
    });
    expect(preview.conflicts[0]).toEqual({ row: 2, key: "desk-alpha" });
    expect(result.counts.applied).toBe(1);
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

  it("surfaces business CSV preview HTTP errors as ApiError", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "validation", message: "invalid CSV header" },
        }),
        {
          status: 400,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );

    await expect(
      previewBusinessCsvImport({
        entity: "accounts",
        delimiter: "comma",
        filename: "accounts.csv",
        payloadBase64: "YWNjb3VudF9pZAo=",
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "validation",
      message: "invalid CSV header",
    });
  });

  it("surfaces business CSV import HTTP errors as ApiError", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "conflict", message: "account already exists" },
        }),
        {
          status: 409,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );

    await expect(
      importBusinessCsv({
        entity: "accounts",
        delimiter: "comma",
        filename: "accounts.csv",
        payloadBase64: "YWNjb3VudF9pZAo=",
        conflictPolicy: "stop",
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "conflict",
      message: "account already exists",
    });
  });

  it("localizes known too_large API errors instead of backend prose", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "too_large", message: "server-side English detail" },
        }),
        {
          status: 413,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );

    await expect(
      previewBusinessCsvImport({
        entity: "accounts",
        delimiter: "comma",
        filename: "accounts.csv",
        payloadBase64: "YWNjb3VudF9pZAo=",
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "too_large",
      message:
        "The import exceeds the maximum size of 128 MiB. Split the export into smaller files or use the API for bulk loading.",
    });
  });

  it("falls back to backend prose for unknown API error codes", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "new_backend_code", message: "backend detail" },
        }),
        {
          status: 418,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );

    await expect(
      previewBusinessCsvImport({
        entity: "accounts",
        delimiter: "comma",
        filename: "accounts.csv",
        payloadBase64: "YWNjb3VudF9pZAo=",
      }),
    ).rejects.toMatchObject({
      name: "ApiError",
      code: "internal",
      message: "backend detail",
    });
  });
});

describe("limits client", () => {
  it("flattens typed limit-list responses for the dashboard view", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        limits: {
          rateLimits: [
            {
              scope: "account",
              account: "desk-alpha",
              asset: "",
              windowMs: 60000,
              maxOrders: 20,
            },
          ],
          orderSizeLimits: [
            {
              scope: "account_asset",
              account: "desk-alpha",
              asset: "AAPL",
              maxQuantity: "10",
              maxNotional: "1500",
            },
          ],
          pnlBoundsLimits: [],
        },
      }),
    );

    const { fetchLimits } = await import("@/api/client");
    const limits = await fetchLimits("desk-alpha");

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits?account=desk-alpha",
      expect.any(Object),
    );
    expect(limits).toEqual([
      {
        policy: "rate_limit",
        scope: "account",
        account: "desk-alpha",
        asset: "",
        values: { max_orders: "20", window: "1m" },
      },
      {
        policy: "order_size_limit",
        scope: "account_asset",
        account: "desk-alpha",
        asset: "AAPL",
        values: { max_quantity: "10", max_notional: "1500" },
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

    const { putLimit } = await import("@/api/client");
    const limit = await putLimit({
      policy: "rate_limit",
      scope: "account",
      account: "desk-alpha",
      asset: "",
      values: { max_orders: "20", window: "1m" },
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/limits/rate",
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

  it("rejects invalid max_orders before sending rate limits", async () => {
    const { putLimit } = await import("@/api/client");

    await expect(
      putLimit({
        policy: "rate_limit",
        scope: "account",
        account: "desk-alpha",
        asset: "",
        values: { max_orders: "", window: "1m" },
      }),
    ).rejects.toThrow("rate limit max_orders must be an integer greater than 0");
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe("backup client", () => {
  it("exports a backup and parses RFC 5987 filenames", async () => {
    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify(backupArchive), {
        status: 200,
        headers: {
          "Content-Type": "application/json",
          "Content-Disposition": "attachment; filename*=UTF-8''pit%20backup.json",
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
          "Content-Disposition": "attachment; filename=\"pit backup.zip\"",
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
});

describe("market-data client settings payloads", () => {
  it("sends provider credentials when creating an IB source", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        instance: {
          externalId: "md-created",
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
    expect(instance.externalId).toBe("md-created");
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
        search: { supported: false, matches: [{ symbol: "X", secType: "STK" }] },
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
    return new Response(
      JSON.stringify({ noESign: false }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
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
// Orders — submit creates a signed approval token and addresses by external id
// ---------------------------------------------------------------------------

describe("Orders createOrder submit lifecycle", () => {
  function approvalResponse(orderExternalId = "ord_alpha_0000000001"): Response {
    return new Response(
      JSON.stringify({
        token: "approval-token",
        keyId: "key-1",
        expiresAt: "2026-01-01T00:05:00Z",
        orderExternalId,
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
          expiresAt: "2026-01-01T00:05:00Z",
          orderExternalId,
        },
      }),
      { status: 201, headers: { "Content-Type": "application/json" } },
    );
  }

  function orderResponse(orderExternalId = "ord_alpha_0000000001"): Response {
    return new Response(
      JSON.stringify({
        order: {
          externalId: orderExternalId,
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
          displayPrices: [],
        },
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }

  it("submits through /orders/submit and fetches the created external id", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(approvalResponse())
      .mockResolvedValueOnce(orderResponse());
    const { createOrder } = await import("@/api/client");
    await createOrder({
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
    });
    expect(fetch).toHaveBeenNthCalledWith(
      1,
      "/app/api/v1/orders/submit",
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

  it("sends caller-supplied externalId and hold mode", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(approvalResponse("ord_supplied_000001"))
      .mockResolvedValueOnce(orderResponse("ord_supplied_000001"));
    const controller = new AbortController();
    const { createOrder } = await import("@/api/client");
    await createOrder({
      externalId: "ord_supplied_000001",
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
      mode: "hold",
    }, controller.signal);
    expect(fetch).toHaveBeenNthCalledWith(
      1,
      "/app/api/v1/orders/submit",
      expect.objectContaining({
        body: JSON.stringify({
          externalId: "ord_supplied_000001",
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
    const { createOrder } = await import("@/api/client");
    const result = await createOrder({
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
    });
    expect(result.order.externalId).toBe("ord_wrapped_0000001");
    expect(fetch).toHaveBeenNthCalledWith(
      2,
      "/app/api/v1/orders/ord_wrapped_0000001",
      expect.any(Object),
    );
  });

  it("normalizes displayPrices from the fetched order", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(approvalResponse())
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            order: {
              externalId: "ord_alpha_0000000001",
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
              displayPrices: ["101.20"],
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    const { createOrder } = await import("@/api/client");
    const result = await createOrder({
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
    });
    expect(result.order.displayPrices).toEqual(["101.20"]);
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
    const { createOrder } = await import("@/api/client");
    const result = await createOrder({
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
    });

    expect(result.order).toMatchObject({
      externalId: "ord_alpha_0000000001",
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
      price: "0",
      status: "submitted",
    });
    expect(result.warning).toContain(
      "Order was created, but detail enrichment failed",
    );
  });
});
