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

import i18n from "@/i18n";
import type {
  Account,
  Adjustment,
  AdjustmentAccepted,
  AdjustmentAmount,
  AdjustmentRejected,
  AdjustmentRequest,
  AuditEntry,
  Balance,
  BoundsPair,
  CheckReject,
  CheckResult,
  CheckWouldBlock,
  EngineHealth,
  Group,
  Health,
  Limit,
  MarketDataDiagnostic,
  MarketDataDiagnosticAction,
  MarketDataInstrument,
  MarketDataInstance,
  MarketDataProvider,
  MarketDataQuote,
  MarketDataReferences,
  MarketDataStatus,
  MarketDataSymbolVerification,
  McpCommand,
  NodeHealth,
  Order,
  OrderEvent,
  Overview,
  ServiceInfo,
  Source,
  Status,
  StoreHealth,
  Trade,
} from "@/api/types";

const BASE = "/app/api/v1";

/** Stable codes the backend maps from its domain sentinels. */
export type ApiErrorCode =
  | "validation"
  | "not_found"
  | "conflict"
  | "precondition"
  | "not_implemented"
  | "internal"
  | "network";

/** A failed API call, carrying the HTTP status and decoded error code. */
export class ApiError extends Error {
  readonly status?: number;
  readonly code: ApiErrorCode;
  constructor(message: string, code: ApiErrorCode, status?: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
  }
}

type Json = Record<string, unknown>;

function isObject(v: unknown): v is Json {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * Read a property trying several candidate keys. The Go handler may emit keys
 * as PascalCase, camelCase, or snake_case depending on its JSON tags;
 * tolerating all three keeps the dashboard robust against the exact handler
 * implementation.
 */
function pick(obj: Json, ...keys: string[]): unknown {
  for (const key of keys) {
    if (key in obj) {
      return obj[key];
    }
  }
  return undefined;
}

function asString(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function asBool(v: unknown): boolean {
  return v === true;
}

function asInt(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

function asCode(v: unknown): ApiErrorCode {
  switch (v) {
    case "validation":
    case "not_found":
    case "conflict":
    case "precondition":
    case "not_implemented":
    case "internal":
      return v;
    default:
      return "internal";
  }
}

function asSource(v: unknown): Source {
  switch (v) {
    case "panel":
    case "api":
    case "mcp":
    case "system":
      return v;
    default:
      return "system";
  }
}

function normalizeEngine(v: unknown): EngineHealth {
  const o = isObject(v) ? v : {};
  return {
    version: asString(pick(o, "version", "Version")),
    buildProfile: asString(
      pick(o, "buildProfile", "BuildProfile", "build_profile"),
    ),
    running: asBool(pick(o, "running", "Running")),
  };
}

function normalizeStore(v: unknown): StoreHealth {
  const o = isObject(v) ? v : {};
  return {
    path: asString(pick(o, "path", "Path")),
    schemaVersion: asInt(
      pick(o, "schemaVersion", "SchemaVersion", "schema_version"),
    ),
    reachable: asBool(pick(o, "reachable", "Reachable")),
  };
}

function normalizeNode(v: unknown): NodeHealth {
  const o = isObject(v) ? v : {};
  return {
    engine: normalizeEngine(pick(o, "engine", "Engine")),
    store: normalizeStore(pick(o, "store", "Store")),
  };
}

function normalizeStatus(v: unknown): Status {
  const o = isObject(v) ? v : {};
  const rawNodes = pick(o, "nodes", "Nodes");
  const nodes = Array.isArray(rawNodes) ? rawNodes.map(normalizeNode) : [];
  return {
    nodes,
    healthy: asBool(pick(o, "healthy", "Healthy")),
  };
}

function normalizeAccount(v: unknown): Account {
  const o = isObject(v) ? v : {};
  return {
    id: asString(pick(o, "id", "Id", "ID")),
    blocked: asBool(pick(o, "blocked", "Blocked")),
    blockReason: asString(pick(o, "blockReason", "BlockReason", "block_reason")),
    group: asString(pick(o, "group", "Group")),
    notes: asString(pick(o, "notes", "Notes")),
  };
}

function normalizeGroup(v: unknown): Group {
  const o = isObject(v) ? v : {};
  return {
    id: asString(pick(o, "id", "Id", "ID")),
    notes: asString(pick(o, "notes", "Notes")),
    blocked: asBool(pick(o, "blocked", "Blocked")),
    blockReason: asString(pick(o, "blockReason", "BlockReason", "block_reason")),
  };
}

function normalizeBalance(v: unknown): Balance {
  const o = isObject(v) ? v : {};
  return {
    account: asString(pick(o, "account", "Account")),
    asset: asString(pick(o, "asset", "Asset")),
    available: asString(pick(o, "available", "Available")),
    held: asString(pick(o, "held", "Held")),
    incoming: asString(pick(o, "incoming", "Incoming")),
    averageEntryPrice: asString(
      pick(o, "averageEntryPrice", "AverageEntryPrice", "average_entry_price"),
    ),
    realizedPnl: asString(
      pick(o, "realizedPnl", "RealizedPnl", "realized_pnl"),
    ),
    updatedAt: asString(pick(o, "updatedAt", "UpdatedAt", "updated_at")),
  };
}

function normalizeAdjustmentAmount(v: unknown): AdjustmentAmount | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  return {
    mode: asString(pick(v, "mode", "Mode")) as AdjustmentAmount["mode"],
    value: asString(pick(v, "value", "Value")),
  };
}

function normalizeBoundsPair(v: unknown): BoundsPair | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  const lower = pick(v, "lower", "Lower");
  const upper = pick(v, "upper", "Upper");
  const result: BoundsPair = {};
  if (lower !== undefined) {
    result.lower = asString(lower);
  }
  if (upper !== undefined) {
    result.upper = asString(upper);
  }
  return result;
}

function normalizeAdjustmentRequest(v: unknown): AdjustmentRequest {
  const o = isObject(v) ? v : {};
  const req: AdjustmentRequest = {
    asset: asString(pick(o, "asset", "Asset")),
  };
  const balance = normalizeAdjustmentAmount(pick(o, "balance", "Balance"));
  if (balance) {
    req.balance = balance;
  }
  const held = normalizeAdjustmentAmount(pick(o, "held", "Held"));
  if (held) {
    req.held = held;
  }
  const incoming = normalizeAdjustmentAmount(pick(o, "incoming", "Incoming"));
  if (incoming) {
    req.incoming = incoming;
  }
  const balanceBounds = normalizeBoundsPair(
    pick(o, "balanceBounds", "BalanceBounds", "balance_bounds"),
  );
  if (balanceBounds) {
    req.balanceBounds = balanceBounds;
  }
  const heldBounds = normalizeBoundsPair(
    pick(o, "heldBounds", "HeldBounds", "held_bounds"),
  );
  if (heldBounds) {
    req.heldBounds = heldBounds;
  }
  const incomingBounds = normalizeBoundsPair(
    pick(o, "incomingBounds", "IncomingBounds", "incoming_bounds"),
  );
  if (incomingBounds) {
    req.incomingBounds = incomingBounds;
  }
  const aep = pick(o, "averageEntryPrice", "AverageEntryPrice", "average_entry_price");
  if (aep !== undefined) {
    req.averageEntryPrice = asString(aep);
  }
  return req;
}

function normalizeAdjustmentAccepted(v: unknown): AdjustmentAccepted | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  return {
    balanceDelta: asString(pick(v, "balanceDelta", "BalanceDelta", "balance_delta")),
    balanceResult: asString(pick(v, "balanceResult", "BalanceResult", "balance_result")),
    heldDelta: asString(pick(v, "heldDelta", "HeldDelta", "held_delta")),
    heldResult: asString(pick(v, "heldResult", "HeldResult", "held_result")),
    incomingDelta: asString(pick(v, "incomingDelta", "IncomingDelta", "incoming_delta")),
    incomingResult: asString(pick(v, "incomingResult", "IncomingResult", "incoming_result")),
  };
}

function normalizeAdjustmentRejected(v: unknown): AdjustmentRejected | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  return {
    code: asString(pick(v, "code", "Code")),
    scope: asString(pick(v, "scope", "Scope")),
    policy: asString(pick(v, "policy", "Policy")),
    reason: asString(pick(v, "reason", "Reason")),
    details: asString(pick(v, "details", "Details")),
  };
}

function asAdjustmentStatus(v: unknown): Adjustment["status"] {
  return v === "rejected" ? "rejected" : "accepted";
}

function normalizeAdjustment(v: unknown): Adjustment {
  const o = isObject(v) ? v : {};
  const outcome = isObject(pick(o, "outcome", "Outcome"))
    ? (pick(o, "outcome", "Outcome") as Record<string, unknown>)
    : {};
  return {
    id: asInt(pick(o, "id", "Id", "ID")),
    account: asString(pick(o, "account", "Account")),
    at: asString(pick(o, "at", "At")),
    source: asSource(pick(o, "source", "Source")),
    asset: asString(
      pick(
        isObject(pick(o, "request", "Request"))
          ? (pick(o, "request", "Request") as Record<string, unknown>)
          : {},
        "asset",
        "Asset",
      ),
    ),
    status: asAdjustmentStatus(pick(o, "status", "Status")),
    request: normalizeAdjustmentRequest(pick(o, "request", "Request")),
    accepted: normalizeAdjustmentAccepted(pick(outcome, "accepted", "Accepted")),
    rejected: normalizeAdjustmentRejected(pick(outcome, "rejected", "Rejected")),
  };
}

function normalizeOrder(v: unknown): Order {
  const o = isObject(v) ? v : {};
  const rawLock = pick(o, "lockPrices", "LockPrices", "lock_prices");
  return {
    id: asInt(pick(o, "id", "Id", "ID")),
    account: asString(pick(o, "account", "Account")),
    at: asString(pick(o, "at", "At")),
    source: asSource(pick(o, "source", "Source")),
    baseAsset: asString(pick(o, "baseAsset", "BaseAsset", "base_asset")),
    quoteAsset: asString(pick(o, "quoteAsset", "QuoteAsset", "quote_asset")),
    side: asString(pick(o, "side", "Side")) as Order["side"],
    amountKind: asString(pick(o, "amountKind", "AmountKind", "amount_kind")) as Order["amountKind"],
    amountValue: asString(pick(o, "amountValue", "AmountValue", "amount_value")),
    price: asString(pick(o, "price", "Price")),
    status: asString(pick(o, "status", "Status")),
    lockPrices: Array.isArray(rawLock) ? rawLock.map(asString) : [],
  };
}

function normalizeOrderEvent(v: unknown): OrderEvent {
  const o = isObject(v) ? v : {};
  const event: OrderEvent = {
    id: asInt(pick(o, "id", "Id", "ID")),
    orderId: asInt(pick(o, "orderId", "OrderId", "order_id")),
    at: asString(pick(o, "at", "At")),
    type: asString(pick(o, "type", "Type")),
    source: asSource(pick(o, "source", "Source")),
    principal: asString(pick(o, "principal", "Principal")),
  };
  const rejectCode = pick(o, "rejectCode", "RejectCode", "reject_code");
  if (rejectCode !== undefined) {
    event.rejectCode = asString(rejectCode);
  }
  const rejectScope = pick(o, "rejectScope", "RejectScope", "reject_scope");
  if (rejectScope !== undefined) {
    event.rejectScope = asString(rejectScope);
  }
  const rejectPolicy = pick(o, "rejectPolicy", "RejectPolicy", "reject_policy");
  if (rejectPolicy !== undefined) {
    event.rejectPolicy = asString(rejectPolicy);
  }
  const rejectReason = pick(o, "rejectReason", "RejectReason", "reject_reason");
  if (rejectReason !== undefined) {
    event.rejectReason = asString(rejectReason);
  }
  const rejectDetails = pick(o, "rejectDetails", "RejectDetails", "reject_details");
  if (rejectDetails !== undefined) {
    event.rejectDetails = asString(rejectDetails);
  }
  const fillQuantity = pick(o, "fillQuantity", "FillQuantity", "fill_quantity");
  if (fillQuantity !== undefined) {
    event.fillQuantity = asString(fillQuantity);
  }
  const fillPrice = pick(o, "fillPrice", "FillPrice", "fill_price");
  if (fillPrice !== undefined) {
    event.fillPrice = asString(fillPrice);
  }
  const fillLockPrice = pick(o, "fillLockPrice", "FillLockPrice", "fill_lock_price");
  if (fillLockPrice !== undefined) {
    event.fillLockPrice = asString(fillLockPrice);
  }
  return event;
}

function normalizeTrade(v: unknown): Trade {
  const o = isObject(v) ? v : {};
  return {
    id: asInt(pick(o, "id", "Id", "ID")),
    orderId: asInt(pick(o, "orderId", "OrderId", "order_id")),
    account: asString(pick(o, "account", "Account")),
    at: asString(pick(o, "at", "At")),
    source: asSource(pick(o, "source", "Source")),
    baseAsset: asString(pick(o, "baseAsset", "BaseAsset", "base_asset")),
    quoteAsset: asString(pick(o, "quoteAsset", "QuoteAsset", "quote_asset")),
    side: asString(pick(o, "side", "Side")) as Trade["side"],
    quantity: asString(pick(o, "quantity", "Quantity")),
    price: asString(pick(o, "price", "Price")),
    lockPrice: asString(pick(o, "lockPrice", "LockPrice", "lock_price")),
  };
}

function normalizeValues(v: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (isObject(v)) {
    for (const [key, raw] of Object.entries(v)) {
      out[key] = asString(raw);
    }
  }
  return out;
}

function normalizeLimit(v: unknown): Limit {
  const o = isObject(v) ? v : {};
  return {
    policy: asString(pick(o, "policy", "Policy")),
    scope: asString(pick(o, "scope", "Scope")),
    account: asString(pick(o, "account", "Account")),
    asset: asString(pick(o, "asset", "Asset")),
    values: normalizeValues(pick(o, "values", "Values")),
  };
}

function normalizeAudit(v: unknown): AuditEntry {
  const o = isObject(v) ? v : {};
  return {
    id: asInt(pick(o, "id", "Id", "ID")),
    at: asString(pick(o, "at", "At")),
    actor: asString(pick(o, "actor", "Actor")),
    action: asString(pick(o, "action", "Action")),
    account: asString(pick(o, "account", "Account")),
    detail: asString(pick(o, "detail", "Detail")),
    source: asSource(pick(o, "source", "Source")),
  };
}

function normalizeOverview(v: unknown): Overview {
  const o = isObject(v) ? v : {};
  const counts = isObject(pick(o, "counts", "Counts"))
    ? (pick(o, "counts", "Counts") as Json)
    : {};
  const rawActivity = pick(o, "activity", "Activity");
  const activity = Array.isArray(rawActivity)
    ? rawActivity.map((a: unknown) => {
        const ao = isObject(a) ? a : {};
        return {
          source: asSource(pick(ao, "source", "Source")),
          at: asString(pick(ao, "at", "At")),
          kind: asString(pick(ao, "kind", "Kind")),
          ref: asString(pick(ao, "ref", "Ref")),
          summary: asString(pick(ao, "summary", "Summary")),
        };
      })
    : [];
  return {
    counts: {
      accounts: asInt(pick(counts, "accounts", "Accounts")),
      groups: asInt(pick(counts, "groups", "Groups")),
      limits: asInt(pick(counts, "limits", "Limits")),
      ordersToday: asInt(pick(counts, "ordersToday", "OrdersToday", "orders_today")),
      ordersTotal: asInt(pick(counts, "ordersTotal", "OrdersTotal", "orders_total")),
    },
    activity,
  };
}

function normalizeMarketDataProvider(v: unknown): MarketDataProvider {
  const o = isObject(v) ? v : {};
  return {
    type: asString(pick(o, "type", "Type")),
    title: asString(pick(o, "title", "Title")),
  };
}

function normalizeMarketDataQuote(v: unknown): MarketDataQuote | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  return {
    asOf: asString(pick(v, "asOf", "AsOf", "as_of")),
    receivedAt: asString(pick(v, "receivedAt", "ReceivedAt", "received_at")),
    mark: asString(pick(v, "mark", "Mark")),
    bid: asString(pick(v, "bid", "Bid")),
    ask: asString(pick(v, "ask", "Ask")),
  };
}

function normalizeMarketDataInstrument(v: unknown): MarketDataInstrument {
  const o = isObject(v) ? v : {};
  const quote = normalizeMarketDataQuote(pick(o, "quote", "Quote"));
  const instrument: MarketDataInstrument = {
    instanceId: asString(pick(o, "instanceId", "InstanceID", "instance_id")),
    externalSymbol: asString(
      pick(o, "externalSymbol", "ExternalSymbol", "external_symbol"),
    ),
    baseAsset: asString(pick(o, "baseAsset", "BaseAsset", "base_asset")),
    quoteAsset: asString(pick(o, "quoteAsset", "QuoteAsset", "quote_asset")),
    manualPrice: asString(pick(o, "manualPrice", "ManualPrice", "manual_price")),
    enabled: asBool(pick(o, "enabled", "Enabled")),
    stale: asBool(pick(o, "stale", "Stale")),
  };
  if (quote) {
    instrument.quote = quote;
  }
  return instrument;
}

function normalizeMarketDataDiagnosticAction(
  v: unknown,
): MarketDataDiagnosticAction {
  const o = isObject(v) ? v : {};
  const action: MarketDataDiagnosticAction = {
    type: asString(pick(o, "type", "Type")),
  };
  const target = pick(o, "target", "Target");
  if (typeof target === "string" && target.length > 0) {
    action.target = target;
  }
  return action;
}

function normalizeMarketDataDiagnostic(v: unknown): MarketDataDiagnostic {
  const o = isObject(v) ? v : {};
  const diag: MarketDataDiagnostic = {
    level: asString(pick(o, "level", "Level")),
    code: asString(pick(o, "code", "Code")),
    kind: asString(pick(o, "kind", "Kind")),
    title: asString(pick(o, "title", "Title")),
    detail: asString(pick(o, "detail", "Detail")),
    actions: normalizeArray(
      pick(o, "actions", "Actions"),
      normalizeMarketDataDiagnosticAction,
    ),
    at: asString(pick(o, "at", "At")),
  };
  const remediation = pick(o, "remediation", "Remediation");
  if (typeof remediation === "string" && remediation.length > 0) {
    diag.remediation = remediation;
  }
  const instrument = pick(o, "instrument", "Instrument");
  if (typeof instrument === "string" && instrument.length > 0) {
    diag.instrument = instrument;
  }
  return diag;
}

function normalizeMarketDataReferences(
  v: unknown,
): MarketDataReferences | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  const docsUrl = pick(v, "docsUrl", "DocsUrl", "docs_url");
  const symbolsUrl = pick(v, "symbolsUrl", "SymbolsUrl", "symbols_url");
  if (docsUrl === undefined && symbolsUrl === undefined) {
    return undefined;
  }
  const refs: MarketDataReferences = {};
  if (typeof docsUrl === "string" && docsUrl.length > 0) {
    refs.docsUrl = docsUrl;
  }
  if (typeof symbolsUrl === "string" && symbolsUrl.length > 0) {
    refs.symbolsUrl = symbolsUrl;
  }
  return refs;
}

function normalizeMarketDataInstance(v: unknown): MarketDataInstance {
  const o = isObject(v) ? v : {};
  const instance: MarketDataInstance = {
    id: asString(pick(o, "id", "ID")),
    type: asString(pick(o, "type", "Type")),
    label: asString(pick(o, "label", "Label")),
    credentials: asString(pick(o, "credentials", "Credentials")),
    enabled: asBool(pick(o, "enabled", "Enabled")),
    state: asString(pick(o, "state", "State")),
    verifiesSymbols: asBool(
      pick(o, "verifiesSymbols", "VerifiesSymbols", "verifies_symbols"),
    ),
    instruments: normalizeArray(
      pick(o, "instruments", "Instruments"),
      normalizeMarketDataInstrument,
    ),
    diagnostics: normalizeArray(
      pick(o, "diagnostics", "Diagnostics"),
      normalizeMarketDataDiagnostic,
    ),
  };
  const err = pick(o, "error", "Error");
  if (err !== undefined) {
    instance.error = asString(err);
  }
  const refs = normalizeMarketDataReferences(
    pick(o, "references", "References"),
  );
  if (refs !== undefined) {
    instance.references = refs;
  }
  return instance;
}

function normalizeMarketDataStatus(v: unknown): MarketDataStatus {
  const o = isObject(v) ? v : {};
  return {
    providers: normalizeArray(
      pick(o, "providers", "Providers"),
      normalizeMarketDataProvider,
    ),
    instances: normalizeArray(
      pick(o, "instances", "Instances"),
      normalizeMarketDataInstance,
    ),
    freshnessSeconds: asInt(
      pick(o, "freshnessSeconds", "FreshnessSeconds", "freshness_seconds"),
    ),
  };
}

function normalizeMarketDataSymbolVerification(
  v: unknown,
): MarketDataSymbolVerification {
  const o = isObject(v) ? v : {};
  const result: MarketDataSymbolVerification = {
    supported: asBool(pick(o, "supported", "Supported")),
    exists: asBool(pick(o, "exists", "Exists")),
  };
  const suggestion = pick(o, "suggestion", "Suggestion");
  if (typeof suggestion === "string" && suggestion.length > 0) {
    result.suggestion = suggestion;
  }
  return result;
}

function normalizeServiceInfo(v: unknown): ServiceInfo {
  const o = isObject(v) ? v : {};
  const db = isObject(pick(o, "database", "Database"))
    ? (pick(o, "database", "Database") as Json)
    : {};
  const info: ServiceInfo = {
    name: asString(pick(o, "name", "Name")),
    engineVersion: asString(
      pick(o, "engineVersion", "EngineVersion", "engine_version"),
    ),
    engineBuildProfile: asString(
      pick(o, "engineBuildProfile", "EngineBuildProfile", "engine_build_profile"),
    ),
    release: asBool(pick(o, "release", "Release")),
    database: {
      path: asString(pick(db, "path", "Path")),
      reachable: asBool(pick(db, "reachable", "Reachable")),
    },
  };
  // Tolerate the optional SDK-provided precise dirty-sources flag when present.
  const rawClean = pick(o, "buildClean", "BuildClean", "build_clean");
  if (typeof rawClean === "boolean") {
    info.buildClean = rawClean;
  }
  return info;
}

function normalizeArray<T>(v: unknown, one: (x: unknown) => T): T[] {
  return Array.isArray(v) ? v.map(one) : [];
}

/** The localized fallback message for a stable error code. The backend's own
 *  message, when present, is preferred; this is what the UI shows otherwise. */
function defaultMessage(code: ApiErrorCode): string {
  return i18n.t(`errors:code.${code}`);
}

/** Turn a non-2xx response into a typed ApiError, decoding the JSON error body
 *  shape {"error":{"code","message"}} when present. */
async function toApiError(res: Response, path: string): Promise<ApiError> {
  let code = asCode(undefined);
  let bodyMessage = "";
  try {
    const body = (await res.json()) as unknown;
    if (isObject(body)) {
      const err = pick(body, "error", "Error");
      if (isObject(err)) {
        code = asCode(pick(err, "code", "Code"));
        bodyMessage = asString(pick(err, "message", "Message"));
      }
    }
  } catch {
    /* No JSON body; infer the code from the status and use a localized message. */
  }
  // Fall back to the HTTP status when the body carried no code.
  if (code === "internal") {
    if (res.status === 400) {
      code = "validation";
    } else if (res.status === 404) {
      code = "not_found";
    } else if (res.status === 409) {
      code = "conflict";
    } else if (res.status === 422) {
      code = "precondition";
    } else if (res.status === 501) {
      code = "not_implemented";
    }
  }
  // Prefer the backend's human message; otherwise a localized per-code default,
  // falling back to the generic HTTP line when the code is still unspecific.
  const message =
    bodyMessage.length > 0
      ? bodyMessage
      : code === "internal"
        ? i18n.t("errors:http", { path, status: res.status })
        : defaultMessage(code);
  return new ApiError(message, code, res.status);
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

/** Issue a request and return the parsed JSON, or undefined for 204. Throws
 *  ApiError on transport failure or a non-2xx status. */
async function request(
  path: string,
  opts: RequestOptions = {},
): Promise<unknown> {
  const { method = "GET", body, signal } = opts;
  const headers: Record<string, string> = { Accept: "application/json" };
  let payload: string | undefined;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }

  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: payload, signal });
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") {
      throw err;
    }
    throw new ApiError(
      i18n.t("errors:network", {
        path,
        detail: err instanceof Error ? err.message : String(err),
      }),
      "network",
    );
  }

  if (!res.ok) {
    throw await toApiError(res, path);
  }

  if (res.status === 204) {
    return undefined;
  }

  try {
    return (await res.json()) as unknown;
  } catch {
    throw new ApiError(
      i18n.t("errors:invalidJson", { path }),
      "internal",
      res.status,
    );
  }
}

function encode(id: string): string {
  return encodeURIComponent(id);
}

// --- Status / health ---

/** Fetch and normalize the deployment status from GET /status. */
export async function fetchStatus(signal?: AbortSignal): Promise<Status> {
  return normalizeStatus(await request(`${BASE}/status`, { signal }));
}

/** Fetch the liveness signal from GET /health. */
export async function fetchHealth(signal?: AbortSignal): Promise<Health> {
  const v = await request(`${BASE}/health`, { signal });
  const o = isObject(v) ? v : {};
  return { ok: asBool(pick(o, "ok", "Ok", "healthy", "Healthy")) };
}

// --- Overview / Dashboard ---

/** GET /overview. */
export async function fetchOverview(signal?: AbortSignal): Promise<Overview> {
  const d = new Date();
  d.setHours(0, 0, 0, 0);
  const since = d.toISOString();
  return normalizeOverview(
    await request(`${BASE}/overview?since=${encodeURIComponent(since)}`, { signal }),
  );
}

// --- Market data ---

/** GET /market-data. */
export async function fetchMarketData(
  signal?: AbortSignal,
): Promise<MarketDataStatus> {
  const v = await request(`${BASE}/market-data`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** POST /market-data/instances. */
export async function createMarketDataInstance(body: {
  id: string;
  type: string;
  label: string;
  credentials: string;
  enabled: boolean;
}): Promise<MarketDataStatus> {
  const v = await request(`${BASE}/market-data/instances`, {
    method: "POST",
    body,
  });
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** PUT /market-data/instances/{id}/enabled. */
export async function setMarketDataInstanceEnabled(
  id: string,
  enabled: boolean,
): Promise<void> {
  await request(`${BASE}/market-data/instances/${encode(id)}/enabled`, {
    method: "PUT",
    body: { enabled },
  });
}

/** DELETE /market-data/instances/{id}. */
export async function deleteMarketDataInstance(id: string): Promise<void> {
  await request(`${BASE}/market-data/instances/${encode(id)}`, {
    method: "DELETE",
  });
}

/** PUT /market-data/instances/{id}/instruments. */
export async function upsertMarketDataInstrument(
  instanceId: string,
  body: {
    externalSymbol: string;
    baseAsset: string;
    quoteAsset: string;
    manualPrice: string;
    enabled: boolean;
  },
): Promise<MarketDataStatus> {
  const v = await request(
    `${BASE}/market-data/instances/${encode(instanceId)}/instruments`,
    { method: "PUT", body },
  );
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** PUT /market-data/instances/{id}/instruments/enabled. */
export async function setMarketDataInstrumentEnabled(
  instanceId: string,
  externalSymbol: string,
  enabled: boolean,
): Promise<void> {
  await request(
    `${BASE}/market-data/instances/${encode(instanceId)}/instruments/enabled`,
    { method: "PUT", body: { externalSymbol, enabled } },
  );
}

/** DELETE /market-data/instances/{id}/instruments?externalSymbol=. */
export async function deleteMarketDataInstrument(
  instanceId: string,
  externalSymbol: string,
): Promise<void> {
  const params = new URLSearchParams({ externalSymbol });
  await request(
    `${BASE}/market-data/instances/${encode(instanceId)}/instruments?${params.toString()}`,
    { method: "DELETE" },
  );
}

/** POST /market-data/restart - stop and restart all feed subscriptions. */
export async function restartMarketData(): Promise<MarketDataStatus> {
  const v = await request(`${BASE}/market-data/restart`, { method: "POST" });
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** POST /market-data/instances/{id}/verify-symbol - stateless symbol check.
 *  Never disturbs live feeds; supported=false when the provider can't verify. */
export async function verifyMarketDataSymbol(
  instanceId: string,
  externalSymbol: string,
): Promise<MarketDataSymbolVerification> {
  const v = await request(
    `${BASE}/market-data/instances/${encode(instanceId)}/verify-symbol`,
    { method: "POST", body: { externalSymbol } },
  );
  const o = isObject(v) ? v : {};
  return normalizeMarketDataSymbolVerification(
    pick(o, "verification", "Verification"),
  );
}

// --- Service info ---

/** GET /service. */
export async function fetchServiceInfo(signal?: AbortSignal): Promise<ServiceInfo> {
  return normalizeServiceInfo(await request(`${BASE}/service`, { signal }));
}

// --- Accounts ---

/** GET /accounts. */
export async function fetchAccounts(signal?: AbortSignal): Promise<Account[]> {
  const v = await request(`${BASE}/accounts`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "accounts", "Accounts"), normalizeAccount);
}

/** POST /accounts. */
export async function createAccount(id: string): Promise<Account> {
  const v = await request(`${BASE}/accounts`, {
    method: "POST",
    body: { id },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** GET /accounts/{id}: the account plus its account-scoped limits. */
export async function fetchAccountState(
  id: string,
  signal?: AbortSignal,
): Promise<{ account: Account; limits: Limit[] }> {
  const v = await request(`${BASE}/accounts/${encode(id)}`, { signal });
  const o = isObject(v) ? v : {};
  return {
    account: normalizeAccount(pick(o, "account", "Account")),
    limits: normalizeArray(pick(o, "limits", "Limits"), normalizeLimit),
  };
}

/** POST /accounts/{id}/block. Returns the updated account. */
export async function blockAccount(
  id: string,
  reason: string,
): Promise<Account> {
  const v = await request(`${BASE}/accounts/${encode(id)}/block`, {
    method: "POST",
    body: { reason },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** POST /accounts/{id}/unblock. Returns the updated account. */
export async function unblockAccount(id: string): Promise<Account> {
  const v = await request(`${BASE}/accounts/${encode(id)}/unblock`, {
    method: "POST",
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** PUT /accounts/{id}/group. Returns the updated account. */
export async function setAccountGroup(
  id: string,
  group: string,
): Promise<Account> {
  const v = await request(`${BASE}/accounts/${encode(id)}/group`, {
    method: "PUT",
    body: { group },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** PUT /accounts/{id}/notes. Returns the updated account. */
export async function setAccountNotes(
  id: string,
  notes: string,
): Promise<Account> {
  const v = await request(`${BASE}/accounts/${encode(id)}/notes`, {
    method: "PUT",
    body: { notes },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

// --- Groups ---

/** GET /groups. */
export async function fetchGroups(signal?: AbortSignal): Promise<Group[]> {
  const v = await request(`${BASE}/groups`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "groups", "Groups"), normalizeGroup);
}

/** POST /groups. */
export async function createGroup(
  id: string,
  notes?: string,
): Promise<Group> {
  const v = await request(`${BASE}/groups`, {
    method: "POST",
    body: notes !== undefined ? { id, notes } : { id },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** GET /groups/{id}: the group plus its member accounts. */
export async function fetchGroupState(
  id: string,
  signal?: AbortSignal,
): Promise<{ group: Group; accounts: Account[] }> {
  const v = await request(`${BASE}/groups/${encode(id)}`, { signal });
  const o = isObject(v) ? v : {};
  return {
    group: normalizeGroup(pick(o, "group", "Group")),
    accounts: normalizeArray(pick(o, "accounts", "Accounts"), normalizeAccount),
  };
}

/** PUT /groups/{id}/notes. Returns the updated group. */
export async function setGroupNotes(
  id: string,
  notes: string,
): Promise<Group> {
  const v = await request(`${BASE}/groups/${encode(id)}/notes`, {
    method: "PUT",
    body: { notes },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** POST /groups/{id}/block. Returns the updated group. */
export async function blockGroup(
  id: string,
  reason: string,
): Promise<Group> {
  const v = await request(`${BASE}/groups/${encode(id)}/block`, {
    method: "POST",
    body: { reason },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** POST /groups/{id}/unblock. Returns the updated group. */
export async function unblockGroup(id: string): Promise<Group> {
  const v = await request(`${BASE}/groups/${encode(id)}/unblock`, {
    method: "POST",
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** DELETE /groups/{id}. Returns 204; throws ApiError on failure. */
export async function deleteGroup(id: string): Promise<void> {
  await request(`${BASE}/groups/${encode(id)}`, { method: "DELETE" });
}

// --- Balances (Spot Funds) ---

interface BalancesFilter {
  account?: string;
  asset?: string;
}

/** GET /balances, optionally filtered by account and/or asset. */
export async function fetchBalances(
  filter: BalancesFilter = {},
  signal?: AbortSignal,
): Promise<Balance[]> {
  const params = new URLSearchParams();
  if (filter.account) {
    params.set("account", filter.account);
  }
  if (filter.asset) {
    params.set("asset", filter.asset);
  }
  const query = params.size > 0 ? `?${params.toString()}` : "";
  const v = await request(`${BASE}/balances${query}`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "balances", "Balances"), normalizeBalance);
}

interface AdjustmentBody {
  asset: string;
  averageEntryPrice?: string;
  balance?: { mode: string; value: string };
  held?: { mode: string; value: string };
  incoming?: { mode: string; value: string };
  balanceBounds?: { lower?: string; upper?: string };
  heldBounds?: { lower?: string; upper?: string };
  incomingBounds?: { lower?: string; upper?: string };
}

/** POST /accounts/{id}/adjustments. */
export async function createAdjustment(
  accountId: string,
  body: AdjustmentBody,
): Promise<Adjustment> {
  const v = await request(`${BASE}/accounts/${encode(accountId)}/adjustments`, {
    method: "POST",
    body,
  });
  const o = isObject(v) ? v : {};
  return normalizeAdjustment(pick(o, "adjustment", "Adjustment"));
}

interface AdjustmentsFilter {
  source?: string;
  limit?: number;
}

/** GET /accounts/{id}/adjustments, newest first. */
export async function fetchAccountAdjustments(
  accountId: string,
  filter: AdjustmentsFilter = {},
  signal?: AbortSignal,
): Promise<Adjustment[]> {
  const params = new URLSearchParams();
  if (filter.source) {
    params.set("source", filter.source);
  }
  if (filter.limit !== undefined) {
    params.set("limit", String(filter.limit));
  }
  const query = params.size > 0 ? `?${params.toString()}` : "";
  const v = await request(
    `${BASE}/accounts/${encode(accountId)}/adjustments${query}`,
    { signal },
  );
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "adjustments", "Adjustments"), normalizeAdjustment);
}

interface GlobalAdjustmentsFilter {
  account?: string;
  source?: string;
  limit?: number;
}

/** GET /adjustments, newest first. */
export async function fetchAdjustments(
  filter: GlobalAdjustmentsFilter = {},
  signal?: AbortSignal,
): Promise<Adjustment[]> {
  const params = new URLSearchParams();
  if (filter.account) {
    params.set("account", filter.account);
  }
  if (filter.source) {
    params.set("source", filter.source);
  }
  if (filter.limit !== undefined) {
    params.set("limit", String(filter.limit));
  }
  const query = params.size > 0 ? `?${params.toString()}` : "";
  const v = await request(`${BASE}/adjustments${query}`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "adjustments", "Adjustments"), normalizeAdjustment);
}

// --- Orders / Trading ---

interface CreateOrderBody {
  account: string;
  baseAsset: string;
  quoteAsset: string;
  side: string;
  amountKind: string;
  amountValue: string;
  price?: string;
}

/** POST /orders. */
export async function createOrder(body: CreateOrderBody): Promise<Order> {
  const v = await request(`${BASE}/orders`, { method: "POST", body });
  const o = isObject(v) ? v : {};
  return normalizeOrder(pick(o, "order", "Order"));
}

function normalizeCheckReject(v: unknown): CheckReject {
  const o = isObject(v) ? v : {};
  return {
    code: asString(pick(o, "code", "Code")),
    scope: asString(pick(o, "scope", "Scope")),
    policy: asString(pick(o, "policy", "Policy")),
    reason: asString(pick(o, "reason", "Reason")),
    details: asString(pick(o, "details", "Details")),
  };
}

function normalizeCheckWouldBlock(v: unknown): CheckWouldBlock | null {
  if (!isObject(v)) {
    return null;
  }
  return {
    account: asString(pick(v, "account", "Account")),
    code: asString(pick(v, "code", "Code")),
    reason: asString(pick(v, "reason", "Reason")),
    details: asString(pick(v, "details", "Details")),
  };
}

function normalizeCheckResult(v: unknown): CheckResult {
  const o = isObject(v) ? v : {};
  return {
    passed: asBool(pick(o, "passed", "Passed")),
    rejects: normalizeArray(pick(o, "rejects", "Rejects"), normalizeCheckReject),
    wouldLockPrices: Array.isArray(pick(o, "wouldLockPrices", "WouldLockPrices"))
      ? (pick(o, "wouldLockPrices", "WouldLockPrices") as unknown[]).map(asString)
      : [],
    wouldBlock: normalizeCheckWouldBlock(
      pick(o, "wouldBlock", "WouldBlock", "would_block"),
    ),
  };
}

interface CheckOrderBody {
  account: string;
  baseAsset: string;
  quoteAsset: string;
  side: string;
  amountKind: string;
  amountValue: string;
  price?: string;
}

/** POST /orders/check - pre-trade dry-run; never mutates state. */
export async function checkOrder(
  body: CheckOrderBody,
  signal?: AbortSignal,
): Promise<CheckResult> {
  const v = await request(`${BASE}/orders/check`, { method: "POST", body, signal });
  const o = isObject(v) ? v : {};
  return normalizeCheckResult(pick(o, "check", "Check"));
}

interface OrdersFilter {
  account?: string;
  source?: string;
  limit?: number;
}

/** GET /orders, newest first. */
export async function fetchOrders(
  filter: OrdersFilter = {},
  signal?: AbortSignal,
): Promise<Order[]> {
  const params = new URLSearchParams();
  if (filter.account) {
    params.set("account", filter.account);
  }
  if (filter.source) {
    params.set("source", filter.source);
  }
  if (filter.limit !== undefined) {
    params.set("limit", String(filter.limit));
  }
  const query = params.size > 0 ? `?${params.toString()}` : "";
  const v = await request(`${BASE}/orders${query}`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "orders", "Orders"), normalizeOrder);
}

/** GET /orders/{id}: the order plus its events and trades. */
export async function fetchOrderDetail(
  id: number,
  signal?: AbortSignal,
): Promise<{ order: Order; events: OrderEvent[]; trades: Trade[] }> {
  const v = await request(`${BASE}/orders/${id}`, { signal });
  const o = isObject(v) ? v : {};
  return {
    order: normalizeOrder(pick(o, "order", "Order")),
    events: normalizeArray(pick(o, "events", "Events"), normalizeOrderEvent),
    trades: normalizeArray(pick(o, "trades", "Trades"), normalizeTrade),
  };
}

interface ExecutionReportBody {
  quantity: string;
  price: string;
  lockPrice?: string;
  final: boolean;
}

/** POST /orders/{id}/execution-reports. */
export async function submitExecutionReport(
  orderId: number,
  body: ExecutionReportBody,
): Promise<unknown> {
  const v = await request(`${BASE}/orders/${orderId}/execution-reports`, {
    method: "POST",
    body,
  });
  const o = isObject(v) ? v : {};
  return pick(o, "result", "Result");
}

interface TradesFilter {
  account?: string;
  source?: string;
  limit?: number;
}

/** GET /trades, newest first. */
export async function fetchTrades(
  filter: TradesFilter = {},
  signal?: AbortSignal,
): Promise<Trade[]> {
  const params = new URLSearchParams();
  if (filter.account) {
    params.set("account", filter.account);
  }
  if (filter.source) {
    params.set("source", filter.source);
  }
  if (filter.limit !== undefined) {
    params.set("limit", String(filter.limit));
  }
  const query = params.size > 0 ? `?${params.toString()}` : "";
  const v = await request(`${BASE}/trades${query}`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "trades", "Trades"), normalizeTrade);
}

// --- Limits / Policies ---

/** GET /limits, optionally filtered to one account server-side. */
export async function fetchLimits(
  account?: string,
  signal?: AbortSignal,
): Promise<Limit[]> {
  const query = account ? `?account=${encode(account)}` : "";
  const v = await request(`${BASE}/limits${query}`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "limits", "Limits"), normalizeLimit);
}

/** PUT /limits: upsert the whole barrier. */
export async function putLimit(limit: Limit): Promise<Limit> {
  const v = await request(`${BASE}/limits`, { method: "PUT", body: limit });
  const o = isObject(v) ? v : {};
  return normalizeLimit(pick(o, "limit", "Limit"));
}

/** DELETE /limits identified by its target tuple. */
export async function deleteLimit(target: {
  policy: string;
  scope: string;
  account: string;
  asset: string;
}): Promise<void> {
  const params = new URLSearchParams({
    policy: target.policy,
    scope: target.scope,
  });
  if (target.account) {
    params.set("account", target.account);
  }
  if (target.asset) {
    params.set("asset", target.asset);
  }
  await request(`${BASE}/limits?${params.toString()}`, { method: "DELETE" });
}

// --- Audit ---

interface AuditFilter {
  account?: string;
  source?: string;
  limit?: number;
}

// --- MCP access ---

function normalizeMcpCommand(v: unknown): McpCommand {
  const o = isObject(v) ? v : {};
  return {
    name: asString(pick(o, "name", "Name")),
    title: asString(pick(o, "title", "Title")),
    agentDescription: asString(
      pick(o, "agentDescription", "AgentDescription", "agent_description"),
    ),
    mutating: asBool(pick(o, "mutating", "Mutating")),
    protective: asBool(pick(o, "protective", "Protective")),
    implemented: asBool(pick(o, "implemented", "Implemented")),
    enabled: asBool(pick(o, "enabled", "Enabled")),
  };
}

/** GET /mcp-access - returns the full MCP command catalogue. */
export async function getMcpCommands(
  signal?: AbortSignal,
): Promise<McpCommand[]> {
  const v = await request(`${BASE}/mcp-access`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "commands", "Commands"), normalizeMcpCommand);
}

/** PUT /mcp-access/{command} - enable or disable one MCP command. */
export async function setMcpCommand(
  name: string,
  enabled: boolean,
): Promise<McpCommand> {
  const v = await request(`${BASE}/mcp-access/${encode(name)}`, {
    method: "PUT",
    body: { enabled },
  });
  const o = isObject(v) ? v : {};
  return normalizeMcpCommand(pick(o, "command", "Command"));
}

// --- Audit ---

/** GET /audit, newest first; the backend caps the limit at 1000. */
export async function fetchAudit(
  limitOrFilter: number | AuditFilter,
  signal?: AbortSignal,
): Promise<AuditEntry[]> {
  const params = new URLSearchParams();
  if (typeof limitOrFilter === "number") {
    params.set("limit", String(limitOrFilter));
  } else {
    if (limitOrFilter.account) {
      params.set("account", limitOrFilter.account);
    }
    if (limitOrFilter.source) {
      params.set("source", limitOrFilter.source);
    }
    if (limitOrFilter.limit !== undefined) {
      params.set("limit", String(limitOrFilter.limit));
    }
  }
  const query = params.size > 0 ? `?${params.toString()}` : "";
  const v = await request(`${BASE}/audit${query}`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "entries", "Entries"), normalizeAudit);
}
