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
  AuditActionGroup,
  AuditEntry,
  Balance,
  BackupArchive,
  BackupRestoreSummary,
  BackupScope,
  BoundsPair,
  CheckReject,
  CheckResult,
  CheckWouldBlock,
  EngineHealth,
  ExecutionBlock,
  ExecutionReportResult,
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
  MarketDataSymbolMatch,
  MarketDataSymbolSearch,
  MarketDataSymbolVerification,
  McpCommand,
  NodeHealth,
  Order,
  OrderEvent,
  SigningKey,
  SigningKeyFormat,
  SigningKeysStatus,
  SigningKeyResult,
  Overview,
  RestoreMode,
  ServiceInfo,
  ServiceLogs,
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
  | "engine_restarting"
  | "not_implemented"
  | "internal"
  | "network"
  | "signing";

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
    case "engine_restarting":
    case "not_implemented":
    case "internal":
    case "signing":
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
  const order: Order = {
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
  const submitMode = asString(pick(o, "submitMode", "SubmitMode", "submit_mode"));
  if (submitMode === "hold" || submitMode === "immediate") {
    order.submitMode = submitMode;
  }
  return order;
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
  const accountsTotal = asInt(pick(counts, "accounts", "Accounts"));
  const groupsTotal = asInt(pick(counts, "groups", "Groups"));
  const accountsActiveRaw = pick(counts, "accountsActive", "AccountsActive");
  const groupsActiveRaw = pick(counts, "groupsActive", "GroupsActive");
  return {
    counts: {
      accounts: accountsTotal,
      accountsActive: typeof accountsActiveRaw === "number" ? asInt(accountsActiveRaw) : accountsTotal,
      groups: groupsTotal,
      groupsActive: typeof groupsActiveRaw === "number" ? asInt(groupsActiveRaw) : groupsTotal,
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
  // Omitted (omitempty) on the wire when unknown; keep it undefined then so the
  // UI can tell "no interval yet" from a real zero.
  const intervalMs = asInt(
    pick(o, "updateIntervalMs", "UpdateIntervalMs", "update_interval_ms"),
  );
  if (intervalMs > 0) {
    instrument.updateIntervalMs = intervalMs;
  }
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
    searchesSymbols: asBool(
      pick(o, "searchesSymbols", "SearchesSymbols", "searches_symbols"),
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
  const settings = pick(o, "settings", "Settings");
  if (isObject(settings)) {
    instance.settings = settings;
  }
  const secrets = pick(o, "secrets", "Secrets");
  if (isObject(secrets)) {
    const normalized: Record<string, boolean> = {};
    for (const [key, value] of Object.entries(secrets)) {
      normalized[key] = value === true;
    }
    instance.secrets = normalized;
  }
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
    restartRequired: asBool(
      pick(o, "restartRequired", "RestartRequired", "restart_required"),
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
  const details = pick(o, "details", "Details");
  if (typeof details === "string" && details.length > 0) {
    result.details = details;
  }
  return result;
}

function normalizeMarketDataSymbolMatch(v: unknown): MarketDataSymbolMatch {
  const o = isObject(v) ? v : {};
  const match: MarketDataSymbolMatch = {
    symbol: asString(pick(o, "symbol", "Symbol")),
    secType: asString(pick(o, "secType", "SecType", "sec_type")),
  };
  const name = pick(o, "name", "Name");
  if (typeof name === "string" && name.length > 0) {
    match.name = name;
  }
  const exchange = pick(o, "exchange", "Exchange");
  if (typeof exchange === "string" && exchange.length > 0) {
    match.exchange = exchange;
  }
  const primaryExchange = pick(
    o,
    "primaryExchange",
    "PrimaryExchange",
    "primary_exchange",
  );
  if (typeof primaryExchange === "string" && primaryExchange.length > 0) {
    match.primaryExchange = primaryExchange;
  }
  const currency = pick(o, "currency", "Currency");
  if (typeof currency === "string" && currency.length > 0) {
    match.currency = currency;
  }
  const conId = pick(o, "conId", "ConId", "ConID", "con_id");
  if (typeof conId === "string" && conId.trim() !== "") {
    match.conId = conId.trim();
  } else if (typeof conId === "number" && Number.isFinite(conId)) {
    match.conId = String(conId);
  }
  const expiry = pick(
    o,
    "lastTradeDateOrContractMonth",
    "LastTradeDateOrContractMonth",
    "last_trade_date_or_contract_month",
  );
  if (typeof expiry === "string" && expiry.length > 0) {
    match.lastTradeDateOrContractMonth = expiry;
  }
  const strike = pick(o, "strike", "Strike");
  if (typeof strike === "string" && strike.trim() !== "") {
    match.strike = strike.trim();
  }
  // A numeric strike is intentionally ignored: financial decimals must never
  // be represented as JS numbers (lossless string is the only safe wire
  // format). The backend always sends strike as a decimal string, so a number
  // here would be an unexpected wire-format bug — omit rather than coerce.
  const right = pick(o, "right", "Right");
  if (typeof right === "string" && right.length > 0) {
    match.right = right;
  }
  const multiplier = pick(o, "multiplier", "Multiplier");
  if (typeof multiplier === "string" && multiplier.length > 0) {
    match.multiplier = multiplier;
  }
  const localSymbol = pick(o, "localSymbol", "LocalSymbol", "local_symbol");
  if (typeof localSymbol === "string" && localSymbol.length > 0) {
    match.localSymbol = localSymbol;
  }
  const tradingClass = pick(o, "tradingClass", "TradingClass", "trading_class");
  if (typeof tradingClass === "string" && tradingClass.length > 0) {
    match.tradingClass = tradingClass;
  }
  return match;
}

function normalizeMarketDataSymbolSearch(
  v: unknown,
): MarketDataSymbolSearch {
  const o = isObject(v) ? v : {};
  return {
    supported: asBool(pick(o, "supported", "Supported")),
    matches: normalizeArray(
      pick(o, "matches", "Matches"),
      normalizeMarketDataSymbolMatch,
    ),
  };
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

function normalizeServiceLogs(v: unknown): ServiceLogs {
  const o = isObject(v) ? v : {};
  const rawLines = pick(o, "lines", "Lines");
  const lines = Array.isArray(rawLines)
    ? rawLines.map((x) => asString(x))
    : [];
  const rawCount = pick(o, "count", "Count");
  // Trust the server count when present; otherwise fall back to the line total.
  const count = typeof rawCount === "number" ? asInt(rawCount) : lines.length;
  return { lines, count };
}

function normalizeBackupSummary(v: unknown): BackupRestoreSummary {
  if (!isObject(v)) {
    throw new ApiError(i18n.t("errors:code.internal"), "internal");
  }
  const o = v;
  const applied = pick(o, "applied", "Applied");
  const skipped = pick(o, "skipped", "Skipped");
  return {
    applied: isObject(applied)
      ? (applied as BackupRestoreSummary["applied"])
      : {},
    skipped: isObject(skipped)
      ? (skipped as BackupRestoreSummary["skipped"])
      : {},
    restartRequired: asBool(
      pick(o, "restartRequired", "RestartRequired", "restart_required"),
    ),
  };
}

function normalizeArray<T>(v: unknown, one: (x: unknown) => T): T[] {
  return Array.isArray(v) ? v.map(one) : [];
}

function filenameFromDisposition(value: string | null): string {
  if (!value) {
    return "";
  }
  const encoded = /(?:^|;)\s*filename\*=UTF-8''([^;]+)/i.exec(value);
  if (encoded) {
    try {
      return decodeURIComponent(encoded[1]);
    } catch {
      return encoded[1];
    }
  }
  const quoted = /(?:^|;)\s*filename="([^"]+)"/i.exec(value);
  if (quoted) {
    return quoted[1];
  }
  const bare = /(?:^|;)\s*filename=([^;]+)/i.exec(value);
  return bare ? bare[1].trim() : "";
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
    } else if (res.status === 503) {
      code = "engine_restarting";
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
  type: string;
  label: string;
  credentials?: string;
  enabled: boolean;
}): Promise<MarketDataStatus> {
  const v = await request(`${BASE}/market-data/instances`, {
    method: "POST",
    body,
  });
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** PUT /market-data/instances/{id}/settings. */
export async function updateMarketDataInstanceSettings(
  id: string,
  body: {
    label: string;
    credentials: string;
  },
): Promise<MarketDataStatus> {
  const v = await request(`${BASE}/market-data/instances/${encode(id)}/settings`, {
    method: "PUT",
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

/** Resolve inputs for a symbol search: the typed symbol plus the contract
 *  criteria (secType/exchange/currency and, for derivatives, expiry/strike/
 *  right) the backend feeds to reqContractDetails. */
export interface MarketDataSymbolSearchInput {
  query: string;
  secType?: string;
  exchange?: string;
  currency?: string;
  lastTradeDateOrContractMonth?: string;
  strike?: string;
  right?: string;
}

/** POST /market-data/instances/{id}/search-symbols - stateless contract
 *  resolve. Never disturbs live feeds; supported=false when the provider can't
 *  search. The secType/exchange/currency criteria scope the resolve so crypto,
 *  forex, and futures resolve to an exact contract. */
export async function searchMarketDataSymbols(
  instanceId: string,
  input: MarketDataSymbolSearchInput,
): Promise<MarketDataSymbolSearch> {
  const body: Record<string, string> = { query: input.query };
  const putStr = (key: string, value: string | undefined) => {
    if (value !== undefined && value.trim() !== "") {
      body[key] = value.trim();
    }
  };
  putStr("secType", input.secType);
  putStr("exchange", input.exchange);
  putStr("currency", input.currency);
  putStr("lastTradeDateOrContractMonth", input.lastTradeDateOrContractMonth);
  putStr("right", input.right);
  putStr("strike", input.strike);
  const v = await request(
    `${BASE}/market-data/instances/${encode(instanceId)}/search-symbols`,
    { method: "POST", body },
  );
  const o = isObject(v) ? v : {};
  const wrapped = pick(o, "search", "Search");
  return normalizeMarketDataSymbolSearch(wrapped === undefined ? o : wrapped);
}

// --- Service info ---

/** GET /service. */
export async function fetchServiceInfo(signal?: AbortSignal): Promise<ServiceInfo> {
  return normalizeServiceInfo(await request(`${BASE}/service`, { signal }));
}

/** GET /service/logs - the captured log tail, oldest line first. */
export async function fetchServiceLogs(
  signal?: AbortSignal,
): Promise<ServiceLogs> {
  return normalizeServiceLogs(await request(`${BASE}/service/logs`, { signal }));
}

/** POST /backup/export - returns a portable backup archive and filename. */
export async function exportBackup(
  scope: BackupScope,
  signal?: AbortSignal,
  zip?: boolean,
): Promise<{ blob: Blob; filename: string }> {
  const headers: Record<string, string> = {
    Accept: zip ? "application/zip" : "application/json",
    "Content-Type": "application/json",
  };
  const body: { scope: BackupScope; zip?: boolean } = { scope };
  if (zip) {
    body.zip = true;
  }
  const path = `${BASE}/backup/export`;
  let res: Response;
  try {
    res = await fetch(path, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
      signal,
    });
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
  const filename = filenameFromDisposition(
    res.headers.get("Content-Disposition"),
  );
  const blob = await res.blob();
  return {
    blob,
    filename: filename || (zip ? "pit-officer-backup.zip" : "pit-officer-backup.json"),
  };
}

/** POST /backup/restore - imports a portable backup archive. */
export async function restoreBackup(input: {
  archive?: BackupArchive;
  archiveFile?: {
    base64: string;
    filename: string;
  };
  scope: BackupScope;
  mode: RestoreMode;
  signal?: AbortSignal;
}): Promise<BackupRestoreSummary> {
  const body: Record<string, unknown> = {
    scope: input.scope,
    mode: input.mode,
  };
  if (input.archive !== undefined) {
    body.archive = input.archive;
  }
  if (input.archiveFile !== undefined) {
    body.archiveFile = input.archiveFile.base64;
    body.archiveFilename = input.archiveFile.filename;
  }
  const v = await request(`${BASE}/backup/restore`, {
    method: "POST",
    body,
    signal: input.signal,
  });
  if (!isObject(v)) {
    throw new ApiError("Invalid backup restore response.", "internal");
  }
  const o = v;
  return normalizeBackupSummary(pick(o, "summary", "Summary"));
}

/** POST /database/reset - recreates the database from scratch. */
export async function resetDatabase(): Promise<void> {
  await request(`${BASE}/database/reset`, {
    method: "POST",
    body: { confirm: true },
  });
}

/** Same-origin URL for the full-buffer log download, used as an anchor href so
 *  the browser downloads it under the route's shared middleware. */
export const serviceLogsDownloadUrl = `${BASE}/service/logs/download`;

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

export interface CreateOrderBody {
  account: string;
  baseAsset: string;
  quoteAsset: string;
  side: string;
  amountKind: string;
  amountValue: string;
  price?: string;
  /** Only sent when "hold"; omitted for "immediate" to stay backward-compatible. */
  submitMode?: "hold";
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

function normalizeExecutionBlock(v: unknown): ExecutionBlock {
  const o = isObject(v) ? v : {};
  return {
    account: asString(pick(o, "account", "Account")),
    code: asString(pick(o, "code", "Code")),
    reason: asString(pick(o, "reason", "Reason")),
    details: asString(pick(o, "details", "Details")),
  };
}

/** POST /orders/{id}/execution-reports. Returns the account blocks the fill caused. */
export async function submitExecutionReport(
  orderId: number,
  body: ExecutionReportBody,
): Promise<ExecutionReportResult> {
  const v = await request(`${BASE}/orders/${orderId}/execution-reports`, {
    method: "POST",
    body,
  });
  const o = isObject(v) ? v : {};
  const result = pick(o, "result", "Result");
  const ro = isObject(result) ? result : {};
  return {
    blocks: normalizeArray(pick(ro, "blocks", "Blocks"), normalizeExecutionBlock),
  };
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
  /** Action types to include. Undefined sends no action filter (server
   *  default: control only); an empty array selects nothing. */
  actions?: string[];
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

// --- User settings ---

/** GET /user-settings - the current operator's UI preferences. */
export async function fetchWelcomeSeen(signal?: AbortSignal): Promise<boolean> {
  const v = await request(`${BASE}/user-settings`, { signal });
  const o = isObject(v) ? v : {};
  return asBool(pick(o, "welcomeSeen", "WelcomeSeen", "welcome_seen"));
}

/** PUT /user-settings - persist the "don't show the welcome again" choice. */
export async function setWelcomeSeen(seen: boolean): Promise<boolean> {
  const v = await request(`${BASE}/user-settings`, {
    method: "PUT",
    body: { welcomeSeen: seen },
  });
  const o = isObject(v) ? v : {};
  return asBool(pick(o, "welcomeSeen", "WelcomeSeen", "welcome_seen"));
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
    // An empty action selection matches nothing; skip the round-trip.
    if (limitOrFilter.actions && limitOrFilter.actions.length === 0) {
      return [];
    }
    if (limitOrFilter.account) {
      params.set("account", limitOrFilter.account);
    }
    if (limitOrFilter.source) {
      params.set("source", limitOrFilter.source);
    }
    if (limitOrFilter.actions && limitOrFilter.actions.length > 0) {
      params.set("actions", limitOrFilter.actions.join(","));
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

function normalizeAuditActionGroup(v: unknown): AuditActionGroup {
  const o = isObject(v) ? v : {};
  return {
    category: asString(pick(o, "category", "Category")),
    actions: normalizeArray(pick(o, "actions", "Actions"), asString),
  };
}

/** GET /audit/actions - the audit action catalogue, grouped by category
 *  (control first, canonical order). */
export async function fetchAuditActions(
  signal?: AbortSignal,
): Promise<AuditActionGroup[]> {
  const v = await request(`${BASE}/audit/actions`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "groups", "Groups"), normalizeAuditActionGroup);
}

// --- Signing keys ---

function normalizeSigningKey(v: unknown): SigningKey {
  const o = isObject(v) ? v : {};
  return {
    keyId: asString(pick(o, "keyId", "KeyId", "key_id")),
    fingerprint: asString(pick(o, "fingerprint", "Fingerprint")),
    createdAt: asString(pick(o, "createdAt", "CreatedAt", "created_at")),
    active: asBool(pick(o, "active", "Active")),
  };
}

// asFlag reads a flag the Go handler emits as a boolean but the store persists as
// a "0"/"1" text column, so both wire forms (boolean true, string "1"/"true")
// read truthy.
function asFlag(v: unknown): boolean {
  return v === true || v === "1" || v === "true";
}

export function normalizeSigningKeysStatus(v: unknown): SigningKeysStatus {
  const o = isObject(v) ? v : {};
  const cfg = isObject(pick(o, "config", "Config")) ? pick(o, "config", "Config") : {};
  return {
    keys: normalizeArray(pick(o, "keys", "Keys"), normalizeSigningKey),
    eSignEnabled: !asFlag(
      pick(cfg as Record<string, unknown>, "noESign", "NoESign", "no_esign"),
    ),
  };
}

function normalizeSigningKeyResult(v: unknown): SigningKeyResult {
  const o = isObject(v) ? v : {};
  return {
    key: normalizeSigningKey(pick(o, "key", "Key")),
    publicKey: asString(pick(o, "publicKey", "PublicKey", "public_key")),
  };
}

/** GET /signing/keys — list all keys (including inactive) + global config. */
export async function fetchSigningKeys(
  signal?: AbortSignal,
): Promise<SigningKeysStatus> {
  const [keysV, cfgV] = await Promise.all([
    request(`${BASE}/signing/keys`, { signal }),
    request(`${BASE}/signing/config`, { signal }),
  ]);
  const keysO = isObject(keysV) ? keysV : {};
  const cfgO = isObject(cfgV) ? cfgV : {};
  const combined = {
    keys: pick(keysO, "keys", "Keys"),
    config: cfgO,
  };
  return normalizeSigningKeysStatus(combined);
}

/** POST /signing/keys/generate — create a new Ed25519 key pair. */
export async function generateSigningKey(): Promise<SigningKeyResult> {
  const v = await request(`${BASE}/signing/keys/generate`, { method: "POST" });
  return normalizeSigningKeyResult(v);
}

/** POST /signing/keys/import — import a BYOK private key. */
export async function importSigningKey(
  key: string,
  format: SigningKeyFormat,
): Promise<SigningKeyResult> {
  const v = await request(`${BASE}/signing/keys/import`, {
    method: "POST",
    body: { key, format },
  });
  return normalizeSigningKeyResult(v);
}

/** GET /signing/keys/active/public?format= — export the active public key. */
export async function exportPublicKey(
  format: SigningKeyFormat,
  signal?: AbortSignal,
): Promise<string> {
  const v = await request(
    `${BASE}/signing/keys/active/public?format=${encode(format)}`,
    { signal },
  );
  const o = isObject(v) ? v : {};
  return asString(pick(o, "publicKey", "PublicKey", "public_key"));
}

/** PUT /signing/config — toggle the global eSign flag. */
export async function setESignEnabled(enabled: boolean): Promise<void> {
  await request(`${BASE}/signing/config`, {
    method: "PUT",
    body: { noESign: !enabled },
  });
}
