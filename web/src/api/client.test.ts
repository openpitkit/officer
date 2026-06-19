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
  exportBackup,
  fetchSigningKeys,
  generateSigningKey,
  importSigningKey,
  normalizeSigningKeysStatus,
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
    await createMarketDataInstance({
      type: "ib",
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
          type: "ib",
          label: "IB Gateway",
          credentials: '{"host":"127.0.0.1","port":7496,"clientId":7}',
          enabled: true,
        }),
      }),
    );
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
// Orders — submit mode tests
// ---------------------------------------------------------------------------

describe("Orders createOrder submit-mode", () => {
  function orderResponse(submitMode?: "hold" | "immediate"): Response {
    return new Response(
      JSON.stringify({
        order: {
          id: 1,
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
          lockPrices: [],
          ...(submitMode ? { submitMode } : {}),
        },
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }

  it("does not send submitMode when immediate (default)", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(orderResponse());
    const { createOrder } = await import("@/api/client");
    await createOrder({
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
    });
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders",
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
  });

  it("sends submitMode=hold when hold mode is specified", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(orderResponse());
    const { createOrder } = await import("@/api/client");
    await createOrder({
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
      submitMode: "hold",
    });
    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/orders",
      expect.objectContaining({
        body: JSON.stringify({
          account: "desk-alpha",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "100",
          submitMode: "hold",
        }),
      }),
    );
  });

  it("normalizes submitMode from order responses", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(orderResponse("hold"));
    const { createOrder } = await import("@/api/client");
    const order = await createOrder({
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
    });
    expect(order.submitMode).toBe("hold");
  });
});
