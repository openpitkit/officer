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

import { ApiError, type ApiClient } from "./createApiClient";
import type {
  Account,
  AccountListFilters,
  AccountLimits,
  Adjustment,
  Asset,
  AdjustmentAccepted,
  AdjustmentAmount,
  AdjustmentRejected,
  AdjustmentRequest,
  ApprovalToken,
  AssetClass,
  AssetClassListFilters,
  AssetListFilters,
  AuditActionGroup,
  AuditEntry,
  Balance,
  BalanceListFilters,
  BackupArchive,
  BackupRestoreSummary,
  BackupScope,
  BoundsPair,
  BusinessCsvConflict,
  BusinessCsvConflictPolicy,
  BusinessCsvDelimiter,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  BusinessCsvImportEntity,
  BusinessCsvImportPreview,
  BusinessCsvImportResult,
  CheckReject,
  CheckResult,
  CheckWouldBlock,
  Commission,
  EngineHealth,
  ExecutionBlock,
  ExecutionOutcome,
  ExecutionReportResult,
  Group,
  GroupListFilters,
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
  ApprovalTokenResponse,
  AttestationBlock,
  AttestationResult,
  EventAttestation,
  EventReproduction,
  EventReproductionESign,
  EventReproductionRequest,
  EventReproductionResponse,
  ExecutionReportResponse,
  Order,
  OrderEvent,
  OrderListFilters,
  OrderMutationResponse,
  OrderSizeLimit,
  PublicKeyMaterial,
  PageRequest,
  PagedResult,
  Policy,
  PolicyListFilters,
  RateLimit,
  RangeFilterMode,
  SigningKey,
  SigningKeyFormat,
  SigningKeysStatus,
  SigningKeyResult,
  Overview,
  RestoreMode,
  ServiceInfo,
  ServiceLogs,
  Source,
  SortOrder,
  SpotFundsPnlBoundsLimit,
  Status,
  StoreHealth,
  TextMatchMode,
  Trade,
} from "../../api/types";

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

function requireStringField(obj: Json, label: string, ...keys: string[]): string {
  const value = pick(obj, ...keys);
  if (typeof value !== "string") {
    throw new ApiError(`${label} is missing from the API response`, "internal");
  }
  return value;
}

function parseGoDurationSeconds(value: string): number | null {
  const text = value.trim();
  if (text.length === 0 || text === "0") {
    return null;
  }
  const unit: Record<string, number> = {
    ns: 1e-9,
    us: 1e-6,
    "\u00b5s": 1e-6,
    ms: 1e-3,
    s: 1,
    m: 60,
    h: 3600,
  };
  const re = /(\d+(?:\.\d+)?)(ns|us|\u00b5s|ms|s|m|h)/g;
  let total = 0;
  let consumed = 0;
  let match: RegExpExecArray | null;
  while ((match = re.exec(text)) !== null) {
    consumed += match[0].length;
    total += parseFloat(match[1]) * unit[match[2]];
  }
  if (consumed !== text.length || total <= 0) {
    return null;
  }
  return total;
}

function asBool(v: unknown): boolean {
  return v === true;
}

function asInt(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
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
  const code = asString(pick(o, "code", "Code", "id", "Id", "ID"));
  const title = asString(pick(o, "title", "Title"));
  const cascadeValue = pick(o, "currencyCascade", "CurrencyCascade");
  const cascade = isObject(cascadeValue) ? cascadeValue : {};
  return {
    code,
    title,
    currency: asString(pick(o, "currency", "Currency")),
    effectiveCurrency: asString(
      pick(o, "effectiveCurrency", "EffectiveCurrency", "effective_currency"),
    ),
    currencyOrigin: asString(
      pick(o, "currencyOrigin", "CurrencyOrigin", "currency_origin"),
    ),
    currencyCascade: {
      account: asString(pick(cascade, "account", "Account")),
      group: asString(pick(cascade, "group", "Group")),
      default: asString(pick(cascade, "default", "Default")),
    },
    blocked: asBool(pick(o, "blocked", "Blocked")),
    blockReason: asString(pick(o, "blockReason", "BlockReason", "block_reason")),
    group: asString(pick(o, "group", "Group")),
    notes: asString(pick(o, "notes", "Notes")),
    positionCount: asInt(pick(o, "positionCount", "PositionCount")),
  };
}

function normalizeGroup(v: unknown): Group {
  const o = isObject(v) ? v : {};
  const code = asString(pick(o, "code", "Code", "id", "Id", "ID"));
  const title = asString(pick(o, "title", "Title"));
  return {
    code,
    title,
    currency: asString(pick(o, "currency", "Currency")),
    notes: asString(pick(o, "notes", "Notes")),
    blocked: asBool(pick(o, "blocked", "Blocked")),
    blockReason: asString(pick(o, "blockReason", "BlockReason", "block_reason")),
    accountCount: asInt(pick(o, "accountCount", "AccountCount")),
    positionCount: asInt(pick(o, "positionCount", "PositionCount")),
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
  const externalId = pick(o, "externalId", "ExternalId", "external_id", "id", "Id", "ID");
  if (externalId !== undefined) {
    req.externalId = asString(externalId);
  }
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
  const realizedPnl = pick(o, "realizedPnl", "RealizedPnl", "realized_pnl");
  if (realizedPnl !== undefined) {
    req.realizedPnl = asString(realizedPnl);
  }
  return req;
}

function normalizeAdjustmentAccepted(v: unknown): AdjustmentAccepted | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  const realizedPnlResult = normalizeAdjustmentResultOptional(
    pick(v, "realizedPnlResult", "RealizedPnlResult", "realized_pnl_result"),
  );
  return {
    balanceDelta: asString(pick(v, "balanceDelta", "BalanceDelta", "balance_delta")),
    balanceResult: asString(pick(v, "balanceResult", "BalanceResult", "balance_result")),
    heldDelta: asString(pick(v, "heldDelta", "HeldDelta", "held_delta")),
    heldResult: asString(pick(v, "heldResult", "HeldResult", "held_result")),
    incomingDelta: asString(pick(v, "incomingDelta", "IncomingDelta", "incoming_delta")),
    incomingResult: asString(pick(v, "incomingResult", "IncomingResult", "incoming_result")),
    realizedPnlResult,
  };
}

function normalizeAdjustmentResultOptional(
  v: unknown,
): AdjustmentAccepted["realizedPnlResult"] {
  // Legacy scalar shape: some payloads carry the result as a bare decimal
  // string rather than the { delta, result } object. Map it to the object
  // form so the type union is fully honored instead of silently dropped.
  if (typeof v === "string") {
    return v !== "" ? { delta: "", result: v } : undefined;
  }
  if (!isObject(v)) {
    return undefined;
  }
  const delta = asString(pick(v, "delta", "Delta"));
  const result = asString(pick(v, "result", "Result"));
  return delta !== "" || result !== "" ? { delta, result } : undefined;
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
    externalId: asString(pick(o, "externalId", "ExternalId", "external_id", "id", "Id", "ID")),
    account: asString(pick(o, "account", "Account")),
    at: asString(pick(o, "at", "At")),
    principal: asString(pick(o, "principal", "Principal")) || undefined,
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
  const rawDisplay = pick(
    o,
    "displayPrices",
    "DisplayPrices",
    "display_prices",
    "lockPrices",
    "LockPrices",
    "lock_prices",
  );
  const amountValue = asString(pick(o, "amountValue", "AmountValue", "amount_value"));
  const leavesQuantity = requireStringField(
    o,
    "leavesQuantity",
    "leavesQuantity",
    "LeavesQuantity",
    "leaves_quantity",
  );
  return {
    externalId: asString(pick(o, "externalId", "ExternalId", "external_id", "id", "Id", "ID")),
    account: asString(pick(o, "account", "Account")),
    at: asString(pick(o, "at", "At")),
    principal: asString(pick(o, "principal", "Principal")) || undefined,
    source: asSource(pick(o, "source", "Source")),
    baseAsset: asString(pick(o, "baseAsset", "BaseAsset", "base_asset")),
    quoteAsset: asString(pick(o, "quoteAsset", "QuoteAsset", "quote_asset")),
    side: asString(pick(o, "side", "Side")) as Order["side"],
    amountKind: asString(pick(o, "amountKind", "AmountKind", "amount_kind")) as Order["amountKind"],
    amountValue,
    commissionSubtotals: normalizeArray(
      pick(o, "commissionSubtotals", "CommissionSubtotals", "commission_subtotals"),
      normalizeCommission,
    ),
    leavesQuantity,
    price: asString(pick(o, "price", "Price")),
    status: asString(pick(o, "status", "Status")),
    displayPrices: Array.isArray(rawDisplay) ? rawDisplay.map(asString) : [],
    signed: asBool(pick(o, "signed", "Signed")),
  };
}

function normalizeOrderEvent(v: unknown): OrderEvent {
  const o = isObject(v) ? v : {};
  const event: OrderEvent = {
    externalId: asString(pick(o, "externalId", "ExternalId", "external_id", "id", "Id", "ID")),
    order: asString(pick(o, "order", "Order", "orderExternalId", "OrderExternalId", "order_external_id", "orderId", "OrderId", "order_id")),
    at: asString(pick(o, "at", "At")),
    type: asString(pick(o, "type", "Type")),
    source: asSource(pick(o, "source", "Source")),
    principal: asString(pick(o, "principal", "Principal")) || undefined,
    signed: asBool(pick(o, "signed", "Signed")),
  };
  const alg = pick(o, "alg", "Alg");
  if (typeof alg === "string" && alg.length > 0) {
    event.alg = alg;
  }
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
  const leavesQuantity = pick(o, "leavesQuantity", "LeavesQuantity", "leaves_quantity");
  if (leavesQuantity !== undefined) {
    event.leavesQuantity = asString(leavesQuantity);
  }
  const orderStatus = pick(o, "orderStatus", "OrderStatus", "order_status");
  if (orderStatus !== undefined) {
    event.orderStatus = asString(orderStatus);
  }
  const executionReport = normalizeExecutionReportRequestRecord(
    pick(o, "executionReport", "ExecutionReport", "execution_report"),
  );
  if (executionReport !== undefined) {
    event.executionReport = executionReport;
  }
  event.commission = normalizeCommissionOptional(
    pick(o, "commission", "Commission"),
  );
  return event;
}

function normalizeExecutionReportRequestRecord(
  v: unknown,
): OrderEvent["executionReport"] {
  if (!isObject(v)) {
    return undefined;
  }
  return {
    baseAsset: asString(pick(v, "baseAsset", "BaseAsset", "base_asset")),
    quoteAsset: asString(pick(v, "quoteAsset", "QuoteAsset", "quote_asset")),
    fillQuantity: asString(
      pick(v, "fillQuantity", "FillQuantity", "fill_quantity"),
    ),
    fillPrice: asString(pick(v, "fillPrice", "FillPrice", "fill_price")),
    leavesQuantity: asString(
      pick(v, "leavesQuantity", "LeavesQuantity", "leaves_quantity"),
    ),
    lockPrice: asString(pick(v, "lockPrice", "LockPrice", "lock_price")),
    commission: normalizeCommissionOptional(
      pick(v, "commission", "Commission"),
    ) ?? null,
    order: asString(pick(v, "order", "Order")),
    account: asString(pick(v, "account", "Account")),
    side: asString(pick(v, "side", "Side")),
    orderStatus: asString(
      pick(v, "orderStatus", "OrderStatus", "order_status"),
    ),
    force: asBool(pick(v, "force", "Force")),
  };
}

function normalizeTrade(v: unknown): Trade {
  const o = isObject(v) ? v : {};
  return {
    externalId: asString(pick(o, "externalId", "ExternalId", "external_id", "id", "Id", "ID")),
    order: asString(pick(o, "order", "Order", "orderExternalId", "OrderExternalId", "order_external_id", "orderId", "OrderId", "order_id")),
    account: asString(pick(o, "account", "Account")),
    at: asString(pick(o, "at", "At")),
    principal: asString(pick(o, "principal", "Principal")) || undefined,
    source: asSource(pick(o, "source", "Source")),
    baseAsset: asString(pick(o, "baseAsset", "BaseAsset", "base_asset")),
    quoteAsset: asString(pick(o, "quoteAsset", "QuoteAsset", "quote_asset")),
    side: asString(pick(o, "side", "Side")) as Trade["side"],
    quantity: asString(pick(o, "quantity", "Quantity")),
    price: asString(pick(o, "price", "Price")),
    lockPrice: asString(pick(o, "lockPrice", "LockPrice", "lock_price")),
    commission: normalizeCommissionOptional(
      pick(o, "commission", "Commission"),
    ),
  };
}

function normalizeCommission(v: unknown): Commission {
  const o = isObject(v) ? v : {};
  return {
    amount: asString(pick(o, "amount", "Amount")),
    currency: asString(pick(o, "currency", "Currency")),
  };
}

function normalizeCommissionOptional(v: unknown): Commission | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  const commission = normalizeCommission(v);
  return commission.amount !== "" && commission.currency !== ""
    ? commission
    : undefined;
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

function windowMsToDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) {
    return "";
  }
  if (ms % 3_600_000 === 0) {
    return `${ms / 3_600_000}h`;
  }
  if (ms % 60_000 === 0) {
    return `${ms / 60_000}m`;
  }
  if (ms % 1_000 === 0) {
    return `${ms / 1_000}s`;
  }
  return `${ms}ms`;
}

function normalizeLimit(v: unknown): Limit {
  const o = isObject(v) ? v : {};
  return {
    policy: asString(pick(o, "policy", "Policy")),
    scope: asString(pick(o, "scope", "Scope")),
    account: asString(pick(o, "account", "Account")),
    accountGroup: asString(pick(o, "accountGroup", "AccountGroup")),
    asset: asString(pick(o, "asset", "Asset")),
    accountCurrency: asString(pick(o, "accountCurrency", "AccountCurrency")),
    values: normalizeValues(pick(o, "values", "Values")),
  };
}

function normalizeRateLimit(v: unknown): RateLimit {
  const o = isObject(v) ? v : {};
  return {
    scope: asString(pick(o, "scope", "Scope")),
    account: asString(pick(o, "account", "Account")),
    asset: asString(pick(o, "asset", "Asset")),
    windowMs: asInt(pick(o, "windowMs", "WindowMs", "window_ms")),
    maxOrders: asInt(pick(o, "maxOrders", "MaxOrders", "max_orders")),
  };
}

function normalizeOrderSizeLimit(v: unknown): OrderSizeLimit {
  const o = isObject(v) ? v : {};
  return {
    scope: asString(pick(o, "scope", "Scope")),
    account: asString(pick(o, "account", "Account")),
    asset: asString(pick(o, "asset", "Asset")),
    maxQuantity: asString(pick(o, "maxQuantity", "MaxQuantity", "max_quantity")),
    maxNotional: asString(pick(o, "maxNotional", "MaxNotional", "max_notional")),
  };
}

function normalizeSpotFundsPnlBoundsLimit(
  v: unknown,
): SpotFundsPnlBoundsLimit {
  const o = isObject(v) ? v : {};
  return {
    scope: asString(pick(o, "scope", "Scope")),
    account: asString(pick(o, "account", "Account")),
    accountGroup: asString(pick(o, "accountGroup", "AccountGroup")),
    accountCurrency: asString(pick(o, "accountCurrency", "AccountCurrency")),
    lowerBound: asString(pick(o, "lowerBound", "LowerBound", "lower_bound")),
    upperBound: asString(pick(o, "upperBound", "UpperBound", "upper_bound")),
    initialPnl: asString(pick(o, "initialPnl", "InitialPnl", "initial_pnl")),
  };
}

function normalizeAccountLimits(v: unknown): AccountLimits {
  const o = isObject(v) ? v : {};
  return {
    rateLimits: normalizeArray(
      pick(o, "rateLimits", "RateLimits", "rate_limits"),
      normalizeRateLimit,
    ),
    orderSizeLimits: normalizeArray(
      pick(o, "orderSizeLimits", "OrderSizeLimits", "order_size_limits"),
      normalizeOrderSizeLimit,
    ),
    spotFundsPnlBoundsLimits: normalizeArray(
      pick(
        o,
        "spotFundsPnlBoundsLimits",
        "SpotFundsPnlBoundsLimits",
        "spot_funds_pnl_bounds_limits",
      ),
      normalizeSpotFundsPnlBoundsLimit,
    ),
  };
}

function flattenAccountLimits(v: AccountLimits): Limit[] {
  return [
    ...v.rateLimits.map((limit) => ({
      policy: "rate_limit",
      scope: limit.scope,
      account: limit.account,
      asset: limit.asset,
      values: {
        max_orders: String(limit.maxOrders),
        window: windowMsToDuration(limit.windowMs),
      },
    })),
    ...v.orderSizeLimits.map((limit) => ({
      policy: "order_size_limit",
      scope: limit.scope,
      account: limit.account,
      asset: limit.asset,
      values: {
        max_quantity: limit.maxQuantity,
        max_notional: limit.maxNotional,
      },
    })),
    ...v.spotFundsPnlBoundsLimits.map((limit) => ({
      policy: "spot_funds_pnl_bounds_kill_switch",
      scope: limit.scope,
      account: limit.account,
      accountGroup: limit.accountGroup,
      asset: "",
      accountCurrency: limit.accountCurrency,
      values: {
        lower_bound: limit.lowerBound,
        upper_bound: limit.upperBound,
        initial_pnl: limit.initialPnl,
      },
    })),
  ];
}

function normalizeLimitCollection(v: unknown): Limit[] {
  if (Array.isArray(v)) {
    return v.map(normalizeLimit);
  }
  return flattenAccountLimits(normalizeAccountLimits(v));
}

function normalizePolicyKind(v: unknown): Policy["kind"] {
  switch (v) {
    case "rate_limit":
    case "order_size_limit":
    case "spot_funds_pnl_bounds_kill_switch":
      return v;
    default:
      return "rate_limit";
  }
}

/** Normalize one policyDTO from the unified GET /limits list. */
function normalizePolicy(v: unknown): Policy {
  const o = isObject(v) ? v : {};
  const values = isObject(pick(o, "values", "Values"))
    ? (pick(o, "values", "Values") as Json)
    : {};
  const policy: Policy = {
    kind: normalizePolicyKind(pick(o, "kind", "Kind")),
    scope: asString(pick(o, "scope", "Scope")),
    account: asString(pick(o, "account", "Account")),
    accountGroup: asString(pick(o, "accountGroup", "AccountGroup")),
    asset: asString(pick(o, "asset", "Asset")),
    accountCurrency: asString(pick(o, "accountCurrency", "AccountCurrency")),
    values: {},
  };
  const rate = pick(values, "rate", "Rate");
  if (isObject(rate)) {
    policy.values.rate = {
      windowMs: asInt(pick(rate, "windowMs", "WindowMs", "window_ms")),
      maxOrders: asInt(pick(rate, "maxOrders", "MaxOrders", "max_orders")),
    };
  }
  const orderSize = pick(values, "orderSize", "OrderSize", "order_size");
  if (isObject(orderSize)) {
    policy.values.orderSize = {
      maxQuantity: asString(
        pick(orderSize, "maxQuantity", "MaxQuantity", "max_quantity"),
      ),
      maxNotional: asString(
        pick(orderSize, "maxNotional", "MaxNotional", "max_notional"),
      ),
    };
  }
  const spotFundsPnlBounds = pick(
    values,
    "spotFundsPnlBounds",
    "SpotFundsPnlBounds",
    "spot_funds_pnl_bounds",
  );
  if (isObject(spotFundsPnlBounds)) {
    policy.values.spotFundsPnlBounds = {
      lowerBound: asString(
        pick(spotFundsPnlBounds, "lowerBound", "LowerBound", "lower_bound"),
      ),
      upperBound: asString(
        pick(spotFundsPnlBounds, "upperBound", "UpperBound", "upper_bound"),
      ),
      initialPnl: asString(
        pick(spotFundsPnlBounds, "initialPnl", "InitialPnl", "initial_pnl"),
      ),
    };
  }
  return policy;
}

/** Flatten a typed policy row into the UI's flat policy/value `Limit` model so
 *  the policy table, value chips, and the edit/delete dialogs keep one shape
 *  across the list endpoint and the account-detail endpoint. */
function policyToLimit(policy: Policy): Limit {
  const base = {
    policy: policy.kind,
    scope: policy.scope,
    account: policy.account,
    accountGroup: policy.accountGroup,
    asset: policy.asset,
    accountCurrency: policy.accountCurrency,
  };
  switch (policy.kind) {
    case "rate_limit": {
      const rate = policy.values.rate;
      return {
        ...base,
        values: {
          max_orders: String(rate?.maxOrders ?? 0),
          window: windowMsToDuration(rate?.windowMs ?? 0),
        },
      };
    }
    case "order_size_limit": {
      const orderSize = policy.values.orderSize;
      return {
        ...base,
        values: {
          max_quantity: orderSize?.maxQuantity ?? "",
          max_notional: orderSize?.maxNotional ?? "",
        },
      };
    }
    case "spot_funds_pnl_bounds_kill_switch": {
      const pnlBounds = policy.values.spotFundsPnlBounds;
      return {
        ...base,
        asset: "",
        values: {
          lower_bound: pnlBounds?.lowerBound ?? "",
          upper_bound: pnlBounds?.upperBound ?? "",
          initial_pnl: pnlBounds?.initialPnl ?? "",
        },
      };
    }
  }
}

function limitWindowMs(limit: Limit): number {
  const seconds = parseGoDurationSeconds(limit.values.window ?? "");
  const ms = seconds === null ? NaN : seconds * 1_000;
  if (!Number.isFinite(ms) || ms <= 0 || !Number.isInteger(ms)) {
    throw new ApiError(
      "rate limit window must resolve to whole milliseconds",
      "validation",
      400,
    );
  }
  return ms;
}

function limitMaxOrders(limit: Limit): number {
  const raw = limit.values.max_orders ?? "";
  const value = Number(raw);
  if (!Number.isFinite(value) || value <= 0 || !Number.isInteger(value)) {
    throw new ApiError(
      "rate limit max_orders must be an integer greater than 0",
      "validation",
      400,
    );
  }
  return value;
}

function limitEndpointBody(client: ApiClient, limit: Limit): { path: string; body: unknown } {
  switch (limit.policy) {
    case "rate_limit":
      return {
        path: `${client.baseUrl}/limits/rate`,
        body: {
          scope: limit.scope,
          account: limit.account,
          asset: limit.asset,
          windowMs: limitWindowMs(limit),
          maxOrders: limitMaxOrders(limit),
        },
      };
    case "order_size_limit":
      return {
        path: `${client.baseUrl}/limits/order-size`,
        body: {
          scope: limit.scope,
          account: limit.account,
          asset: limit.asset,
          maxQuantity: limit.values.max_quantity ?? "",
          maxNotional: limit.values.max_notional ?? "",
        },
      };
    case "spot_funds_pnl_bounds_kill_switch":
      return {
        path: `${client.baseUrl}/limits/spot-funds-pnl-bounds`,
        body: {
          scope: limit.scope,
          account: limit.account,
          accountGroup: limit.accountGroup ?? "",
          accountCurrency: limit.accountCurrency ?? "",
          lowerBound: limit.values.lower_bound ?? "",
          upperBound: limit.values.upper_bound ?? "",
          initialPnl: limit.values.initial_pnl ?? "",
        },
      };
    default:
      throw new ApiError(`unknown policy ${limit.policy}`, "validation", 400);
  }
}

function singleFlattenedLimit(policy: string, limits: AccountLimits): Limit {
  const flattened = flattenAccountLimits(limits);
  const first = flattened[0];
  if (first === undefined) {
    throw new ApiError(
      `empty ${policy} limit response`,
      "internal",
      500,
    );
  }
  return first;
}

function normalizePutLimitResponse(policy: string, v: unknown): Limit {
  const o = isObject(v) ? v : {};
  switch (policy) {
    case "rate_limit":
      return singleFlattenedLimit(policy, {
        rateLimits: [normalizeRateLimit(pick(o, "rateLimit", "RateLimit"))],
        orderSizeLimits: [],
        spotFundsPnlBoundsLimits: [],
      });
    case "order_size_limit":
      return singleFlattenedLimit(policy, {
        rateLimits: [],
        orderSizeLimits: [
          normalizeOrderSizeLimit(pick(o, "orderSizeLimit", "OrderSizeLimit")),
        ],
        spotFundsPnlBoundsLimits: [],
      });
    case "spot_funds_pnl_bounds_kill_switch":
      return singleFlattenedLimit(policy, {
        rateLimits: [],
        orderSizeLimits: [],
        spotFundsPnlBoundsLimits: [
          normalizeSpotFundsPnlBoundsLimit(
            pick(o, "spotFundsPnlBoundsLimit", "SpotFundsPnlBoundsLimit"),
          ),
        ],
      });
    default:
      return normalizeLimit(pick(o, "limit", "Limit"));
  }
}

function normalizeAudit(v: unknown): AuditEntry {
  const o = isObject(v) ? v : {};
  return {
    externalId: asString(pick(o, "externalId", "ExternalId", "external_id", "id", "Id", "ID")),
    at: asString(pick(o, "at", "At")),
    actor: asString(pick(o, "actor", "Actor")),
    actorTitle: asString(pick(o, "actorTitle", "ActorTitle", "actor_title")),
    action: asString(pick(o, "action", "Action")),
    account: asString(pick(o, "account", "Account")),
    accountTitle: asString(pick(o, "accountTitle", "AccountTitle", "account_title")),
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
      ordersActive: asInt(pick(counts, "ordersActive", "OrdersActive", "orders_active")),
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
    instanceExternalId: asString(
      pick(
        o,
        "instanceExternalId",
        "InstanceExternalId",
        "instance_external_id",
        "instanceId",
        "InstanceID",
        "instance_id",
      ),
    ),
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
    externalId: asString(pick(o, "externalId", "ExternalId", "external_id", "id", "ID")),
    provider: asString(pick(o, "provider", "Provider", "type", "Type")),
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
    throw new ApiError("The service encountered an internal error.", "internal");
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

function encode(id: string): string {
  return encodeURIComponent(id);
}

// --- Status / health ---

/** Fetch and normalize the deployment status from GET /status. */
async function fetchStatus(client: ApiClient, signal?: AbortSignal): Promise<Status> {
  return normalizeStatus(await client.request(`${client.baseUrl}/status`, { signal }));
}

/** Fetch the liveness signal from GET /health. */
async function fetchHealth(client: ApiClient, signal?: AbortSignal): Promise<Health> {
  const v = await client.request(`${client.baseUrl}/health`, { signal });
  const o = isObject(v) ? v : {};
  return { ok: asBool(pick(o, "ok", "Ok", "healthy", "Healthy")) };
}

// --- Overview / Dashboard ---

/** GET /overview. */
async function fetchOverview(client: ApiClient, signal?: AbortSignal): Promise<Overview> {
  const d = new Date();
  d.setHours(0, 0, 0, 0);
  const since = d.toISOString();
  return normalizeOverview(
    await client.request(`${client.baseUrl}/overview?since=${encodeURIComponent(since)}`, { signal }),
  );
}

// --- Market data ---

/** GET /market-data. */
async function fetchMarketData(client: ApiClient, 
  signal?: AbortSignal,
): Promise<MarketDataStatus> {
  const v = await client.request(`${client.baseUrl}/market-data`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** POST /market-data/instances. */
async function createMarketDataInstance(client: ApiClient, body: {
  externalId?: string;
  provider: string;
  label: string;
  credentials?: string;
  enabled: boolean;
}): Promise<MarketDataInstance> {
  const v = await client.request(`${client.baseUrl}/market-data/instances`, {
    method: "POST",
    body,
  });
  const o = isObject(v) ? v : {};
  return normalizeMarketDataInstance(pick(o, "instance", "Instance"));
}

/** PUT /market-data/instances/{externalId}/settings. */
async function updateMarketDataInstanceSettings(client: ApiClient, 
  externalId: string,
  body: {
    label: string;
    credentials: string;
  },
): Promise<MarketDataStatus> {
  const v = await client.request(
    `${client.baseUrl}/market-data/instances/${encode(externalId)}/settings`,
    {
      method: "PUT",
      body,
    },
  );
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** PUT /market-data/instances/{externalId}/enabled. */
async function setMarketDataInstanceEnabled(client: ApiClient, 
  externalId: string,
  enabled: boolean,
): Promise<void> {
  await client.request(`${client.baseUrl}/market-data/instances/${encode(externalId)}/enabled`, {
    method: "PUT",
    body: { enabled },
  });
}

/** DELETE /market-data/instances/{externalId}. */
async function deleteMarketDataInstance(client: ApiClient, 
  externalId: string,
  force = false,
): Promise<void> {
  const query = force ? "?force=true" : "";
  await client.request(`${client.baseUrl}/market-data/instances/${encode(externalId)}${query}`, {
    method: "DELETE",
  });
}

/** PUT /market-data/instances/{externalId}/instruments. */
async function upsertMarketDataInstrument(client: ApiClient, 
  instanceExternalId: string,
  body: {
    externalSymbol: string;
    baseAsset: string;
    quoteAsset: string;
    manualPrice: string;
    enabled: boolean;
  },
): Promise<MarketDataStatus> {
  const v = await client.request(
    `${client.baseUrl}/market-data/instances/${encode(instanceExternalId)}/instruments`,
    { method: "PUT", body },
  );
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** PUT /market-data/instances/{externalId}/instruments/enabled. */
async function setMarketDataInstrumentEnabled(client: ApiClient, 
  instanceExternalId: string,
  externalSymbol: string,
  enabled: boolean,
): Promise<void> {
  await client.request(
    `${client.baseUrl}/market-data/instances/${encode(instanceExternalId)}/instruments/enabled`,
    { method: "PUT", body: { externalSymbol, enabled } },
  );
}

/** DELETE /market-data/instances/{externalId}/instruments?externalSymbol=. */
async function deleteMarketDataInstrument(client: ApiClient, 
  instanceExternalId: string,
  externalSymbol: string,
): Promise<void> {
  const params = new URLSearchParams({ externalSymbol });
  await client.request(
    `${client.baseUrl}/market-data/instances/${encode(instanceExternalId)}/instruments?${params.toString()}`,
    { method: "DELETE" },
  );
}

/** POST /market-data/restart - stop and restart all feed subscriptions. */
async function restartMarketData(client: ApiClient, ): Promise<MarketDataStatus> {
  const v = await client.request(`${client.baseUrl}/market-data/restart`, { method: "POST" });
  const o = isObject(v) ? v : {};
  return normalizeMarketDataStatus(pick(o, "marketData", "MarketData"));
}

/** POST /market-data/instances/{externalId}/verify-symbol - stateless symbol check.
 *  Never disturbs live feeds; supported=false when the provider can't verify. */
async function verifyMarketDataSymbol(client: ApiClient, 
  instanceExternalId: string,
  externalSymbol: string,
): Promise<MarketDataSymbolVerification> {
  const v = await client.request(
    `${client.baseUrl}/market-data/instances/${encode(instanceExternalId)}/verify-symbol`,
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

/** POST /market-data/instances/{externalId}/search-symbols - stateless contract
 *  resolve. Never disturbs live feeds; supported=false when the provider can't
 *  search. The secType/exchange/currency criteria scope the resolve so crypto,
 *  forex, and futures resolve to an exact contract. */
async function searchMarketDataSymbols(client: ApiClient, 
  instanceExternalId: string,
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
  const v = await client.request(
    `${client.baseUrl}/market-data/instances/${encode(instanceExternalId)}/search-symbols`,
    { method: "POST", body },
  );
  const o = isObject(v) ? v : {};
  const wrapped = pick(o, "search", "Search");
  return normalizeMarketDataSymbolSearch(wrapped === undefined ? o : wrapped);
}

// --- Service info ---

/** GET /service. */
async function fetchServiceInfo(client: ApiClient, signal?: AbortSignal): Promise<ServiceInfo> {
  return normalizeServiceInfo(await client.request(`${client.baseUrl}/service`, { signal }));
}

/** GET /service/logs - the captured log tail, oldest line first. */
async function fetchServiceLogs(client: ApiClient, 
  signal?: AbortSignal,
): Promise<ServiceLogs> {
  return normalizeServiceLogs(await client.request(`${client.baseUrl}/service/logs`, { signal }));
}

/** POST /service/restart - request service process restart. */
async function restartService(client: ApiClient): Promise<void> {
  await client.request(`${client.baseUrl}/service/restart`, { method: "POST" });
}

/** POST /service/stop - request graceful process shutdown. */
async function stopService(client: ApiClient): Promise<void> {
  await client.request(`${client.baseUrl}/service/stop`, { method: "POST" });
}

function businessCsvFilters(
  filters?: BusinessCsvExportFilters,
): BusinessCsvExportFilters {
  if (!filters) {
    return {};
  }
  const out: BusinessCsvExportFilters = {};
  if (filters.groupCode !== undefined) {
    out.groupCode = filters.groupCode;
  }
  if (filters.account) {
    out.account = filters.account;
  }
  if (filters.asset) {
    out.asset = filters.asset;
  }
  if (filters.source) {
    out.source = filters.source;
  }
  return out;
}

function normalizeBusinessCsvConflict(v: unknown): BusinessCsvConflict {
  const o = isObject(v) ? v : {};
  return {
    row: asInt(pick(o, "row", "Row")),
    key: asString(pick(o, "key", "Key")),
  };
}

function normalizeBusinessCsvImport(v: unknown): BusinessCsvImportPreview {
  const o = isObject(v) ? v : {};
  const file = pick(o, "file", "File");
  const fo = isObject(file) ? file : {};
  const counts = pick(o, "counts", "Counts");
  const co = isObject(counts) ? counts : {};
  return {
    file: {
      name: asString(pick(fo, "name", "Name")),
      type: asString(pick(fo, "type", "Type")),
    },
    counts: {
      rows: asInt(pick(co, "rows", "Rows")),
      applied: asInt(pick(co, "applied", "Applied")),
      skipped: asInt(pick(co, "skipped", "Skipped")),
      conflicts: asInt(pick(co, "conflicts", "Conflicts")),
      stopped: asBool(pick(co, "stopped", "Stopped")),
    },
    conflicts: normalizeArray(
      pick(o, "conflicts", "Conflicts"),
      normalizeBusinessCsvConflict,
    ),
  };
}

/** POST /business-csv/export - returns a business CSV or ZIP blob. */
async function exportBusinessCsv(client: ApiClient, input: {
  entity: BusinessCsvEntity;
  delimiter: BusinessCsvDelimiter;
  filters?: BusinessCsvExportFilters;
  signal?: AbortSignal;
  zip?: boolean;
}): Promise<{ blob: Blob; filename: string }> {
  return client.requestBlob(`${client.baseUrl}/business-csv/export`, {
    method: "POST",
    body: {
      entity: input.entity,
      delimiter: input.delimiter,
      zip: input.zip ?? false,
      filters: businessCsvFilters(input.filters),
    },
    signal: input.signal,
    accept: input.zip ? "application/zip" : "text/csv",
    fallbackFilename: `pit-officer-${input.entity}.${input.zip ? "zip" : "csv"}`,
  });
}

/** POST /business-csv/import/preview - validates and reports conflicts. */
async function previewBusinessCsvImport(client: ApiClient, input: {
  entity: BusinessCsvImportEntity;
  delimiter: BusinessCsvDelimiter;
  filename: string;
  payloadBase64: string;
  signal?: AbortSignal;
}): Promise<BusinessCsvImportPreview> {
  const v = await client.request(`${client.baseUrl}/business-csv/import/preview`, {
    method: "POST",
    body: {
      entity: input.entity,
      delimiter: input.delimiter,
      filename: input.filename,
      payloadBase64: input.payloadBase64,
    },
    signal: input.signal,
  });
  const o = isObject(v) ? v : {};
  return normalizeBusinessCsvImport(pick(o, "preview", "Preview"));
}

/** POST /business-csv/import - applies a validated business CSV import. */
async function importBusinessCsv(client: ApiClient, input: {
  entity: BusinessCsvImportEntity;
  delimiter: BusinessCsvDelimiter;
  filename: string;
  payloadBase64: string;
  conflictPolicy: BusinessCsvConflictPolicy;
  signal?: AbortSignal;
}): Promise<BusinessCsvImportResult> {
  const v = await client.request(`${client.baseUrl}/business-csv/import`, {
    method: "POST",
    body: {
      entity: input.entity,
      delimiter: input.delimiter,
      filename: input.filename,
      payloadBase64: input.payloadBase64,
      conflictPolicy: input.conflictPolicy,
    },
    signal: input.signal,
  });
  const o = isObject(v) ? v : {};
  return normalizeBusinessCsvImport(pick(o, "result", "Result"));
}

/** POST /backup/export - returns a portable backup archive and filename. */
async function exportBackup(client: ApiClient, 
  scope: BackupScope,
  signal?: AbortSignal,
  zip?: boolean,
): Promise<{ blob: Blob; filename: string }> {
  const body: { scope: BackupScope; zip?: boolean } = { scope };
  if (zip) {
    body.zip = true;
  }
  return client.requestBlob(`${client.baseUrl}/backup/export`, {
    method: "POST",
    body,
    signal,
    accept: zip ? "application/zip" : "application/json",
    fallbackFilename: zip ? "pit-officer-backup.zip" : "pit-officer-backup.json",
  });
}

/** POST /backup/restore - imports a portable backup archive. */
async function restoreBackup(client: ApiClient, input: {
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
  const v = await client.request(`${client.baseUrl}/backup/restore`, {
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
async function resetDatabase(client: ApiClient, ): Promise<void> {
  await client.request(`${client.baseUrl}/database/reset`, {
    method: "POST",
    body: { confirm: true },
  });
}

/** Same-origin URL for the full-buffer log download, used as an anchor href so
 *  the browser downloads it under the route's shared middleware. */
// --- Accounts ---

function signalFromListArg<T>(
  input?: T | AbortSignal,
  signal?: AbortSignal,
): AbortSignal | undefined {
  return input instanceof AbortSignal ? input : signal;
}

function listFiltersFromArg<T>(input?: T | AbortSignal): T | undefined {
  return input instanceof AbortSignal ? undefined : input;
}

function appendListFilter(
  params: URLSearchParams,
  key: string,
  value: number | string | undefined,
  defaultValue?: string,
) {
  const text = value === undefined ? undefined : String(value);
  if (text !== undefined && text !== "" && text !== defaultValue) {
    params.set(key, text);
  }
}

function accountListQuery(filters?: AccountListFilters): string {
  if (filters === undefined) return "";
  const params = new URLSearchParams();
  appendListFilter(params, "code", filters.code);
  appendListFilter(params, "codeMatch", filters.codeMatch, "contains");
  appendListFilter(params, "status", filters.status, "all");
  appendListFilter(params, "positionCountMode", filters.positionCountMode, "all");
  appendListFilter(params, "positionCountMin", filters.positionCountMin);
  appendListFilter(params, "positionCountMax", filters.positionCountMax);
  appendListFilter(params, "blockReason", filters.blockReason);
  appendListFilter(params, "blockReasonMatch", filters.blockReasonMatch, "contains");
  if (filters.group !== undefined) {
    params.set("group", filters.group);
  }
  appendListFilter(params, "sort", filters.sort);
  appendListFilter(params, "order", filters.order);
  appendListFilter(params, "limit", filters.limit);
  appendListFilter(params, "offset", filters.offset);
  const query = params.toString();
  return query === "" ? "" : `?${query}`;
}

function groupListQuery(filters?: GroupListFilters): string {
  if (filters === undefined) return "";
  const params = new URLSearchParams();
  appendListFilter(params, "code", filters.code);
  appendListFilter(params, "codeMatch", filters.codeMatch, "contains");
  appendListFilter(params, "status", filters.status, "all");
  appendListFilter(params, "positionCountMode", filters.positionCountMode, "all");
  appendListFilter(params, "positionCountMin", filters.positionCountMin);
  appendListFilter(params, "positionCountMax", filters.positionCountMax);
  appendListFilter(params, "accountCountMode", filters.accountCountMode, "all");
  appendListFilter(params, "accountCountMin", filters.accountCountMin);
  appendListFilter(params, "accountCountMax", filters.accountCountMax);
  appendListFilter(params, "notes", filters.notes);
  appendListFilter(params, "notesMatch", filters.notesMatch, "contains");
  appendListFilter(params, "blockReason", filters.blockReason);
  appendListFilter(params, "blockReasonMatch", filters.blockReasonMatch, "contains");
  appendListFilter(params, "sort", filters.sort);
  appendListFilter(params, "order", filters.order);
  appendListFilter(params, "limit", filters.limit);
  appendListFilter(params, "offset", filters.offset);
  const query = params.toString();
  return query === "" ? "" : `?${query}`;
}

function policyListQuery(filters?: PolicyListFilters): string {
  if (filters === undefined) return "";
  const params = new URLSearchParams();
  appendListFilter(params, "account", filters.account);
  appendListFilter(params, "accountGroup", filters.accountGroup);
  appendListFilter(params, "asset", filters.asset);
  appendListFilter(params, "accountCurrency", filters.accountCurrency);
  appendListFilter(params, "policy", filters.policy, "all");
  appendListFilter(params, "sort", filters.sort);
  appendListFilter(params, "order", filters.order);
  appendListFilter(params, "limit", filters.limit);
  appendListFilter(params, "offset", filters.offset);
  const query = params.toString();
  return query === "" ? "" : `?${query}`;
}

/** GET /accounts with server-side total. */
async function fetchAccountsPage(
  client: ApiClient,
  input?: AccountListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<PagedResult<Account>> {
  const filters = listFiltersFromArg(input);
  const v = await client.request(`${client.baseUrl}/accounts${accountListQuery(filters)}`, {
    signal: signalFromListArg(input, signal),
  });
  const o = isObject(v) ? v : {};
  return {
    items: normalizeArray(pick(o, "accounts", "Accounts"), normalizeAccount),
    total: asInt(pick(o, "total", "Total")),
  };
}

/** GET /accounts. */
async function fetchAccounts(
  client: ApiClient,
  input?: AccountListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<Account[]> {
  return (await fetchAccountsPage(client, input, signal)).items;
}

/** POST /accounts. */
async function createAccount(
  client: ApiClient,
  code: string,
  title = "",
  currency = "",
): Promise<Account> {
  const v = await client.request(`${client.baseUrl}/accounts`, {
    method: "POST",
    body: { code, title, currency },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** PUT /accounts/{code}. Returns the updated account. */
async function updateAccount(
  client: ApiClient,
  oldCode: string,
  code: string,
  title: string,
): Promise<Account> {
  const v = await client.request(`${client.baseUrl}/accounts/${encode(oldCode)}`, {
    method: "PUT",
    body: { code, title },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** DELETE /accounts/{code}. Returns 204; throws ApiError on failure. */
async function deleteAccount(client: ApiClient, code: string, force = false): Promise<void> {
  const query = force ? "?force=true" : "";
  await client.request(`${client.baseUrl}/accounts/${encode(code)}${query}`, { method: "DELETE" });
}

/** GET /accounts/{code}: the account plus its account-scoped limits. */
async function fetchAccountState(client: ApiClient, 
  code: string,
  signal?: AbortSignal,
): Promise<{ account: Account; limits: Limit[] }> {
  const v = await client.request(`${client.baseUrl}/accounts/${encode(code)}`, { signal });
  const o = isObject(v) ? v : {};
  return {
    account: normalizeAccount(pick(o, "account", "Account")),
    limits: normalizeLimitCollection(pick(o, "limits", "Limits")),
  };
}

/** POST /accounts/{code}/block. Returns the updated account. */
async function blockAccount(client: ApiClient, 
  code: string,
  reason: string,
): Promise<Account> {
  const v = await client.request(`${client.baseUrl}/accounts/${encode(code)}/block`, {
    method: "POST",
    body: { reason },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** POST /accounts/{code}/unblock. Returns the updated account. */
async function unblockAccount(client: ApiClient, code: string): Promise<Account> {
  const v = await client.request(`${client.baseUrl}/accounts/${encode(code)}/unblock`, {
    method: "POST",
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** PUT /accounts/{code}/group. Returns the updated account. */
async function setAccountGroup(client: ApiClient, 
  code: string,
  group: string,
): Promise<Account> {
  const v = await client.request(`${client.baseUrl}/accounts/${encode(code)}/group`, {
    method: "PUT",
    body: { group },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** PUT /accounts/{code}/currency. Returns the updated account. */
async function setAccountCurrency(
  client: ApiClient,
  code: string,
  currency: string,
): Promise<Account> {
  const v = await client.request(`${client.baseUrl}/accounts/${encode(code)}/currency`, {
    method: "PUT",
    body: { currency },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

/** PUT /accounts/{code}/notes. Returns the updated account. */
async function setAccountNotes(client: ApiClient, 
  code: string,
  notes: string,
): Promise<Account> {
  const v = await client.request(`${client.baseUrl}/accounts/${encode(code)}/notes`, {
    method: "PUT",
    body: { notes },
  });
  const o = isObject(v) ? v : {};
  return normalizeAccount(pick(o, "account", "Account"));
}

// --- Groups ---

/** GET /groups with server-side total. The synthetic default group is pinned
 *  first by the server on the first page and is excluded from `total`. */
async function fetchGroupsPage(
  client: ApiClient,
  input?: GroupListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<PagedResult<Group>> {
  const filters = listFiltersFromArg(input);
  const v = await client.request(`${client.baseUrl}/groups${groupListQuery(filters)}`, {
    signal: signalFromListArg(input, signal),
  });
  const o = isObject(v) ? v : {};
  return {
    items: normalizeArray(pick(o, "groups", "Groups"), normalizeGroup),
    total: asInt(pick(o, "total", "Total")),
  };
}

/** GET /groups. */
async function fetchGroups(
  client: ApiClient,
  input?: GroupListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<Group[]> {
  return (await fetchGroupsPage(client, input, signal)).items;
}

/** POST /groups. */
async function createGroup(client: ApiClient, 
  code: string,
  title: string,
  notes: string,
  currency = "",
): Promise<Group> {
  const v = await client.request(`${client.baseUrl}/groups`, {
    method: "POST",
    body: { code, title, notes, currency },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** PUT /groups/{code}. Returns the updated group. */
async function updateGroup(
  client: ApiClient,
  oldCode: string,
  code: string,
  title: string,
): Promise<Group> {
  const v = await client.request(`${client.baseUrl}/groups/${encode(oldCode)}`, {
    method: "PUT",
    body: { code, title },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** GET /groups/{code}: the group plus its member accounts. */
async function fetchGroupState(client: ApiClient, 
  code: string,
  signal?: AbortSignal,
): Promise<{ group: Group; accounts: Account[] }> {
  const v = await client.request(`${client.baseUrl}/groups/${encode(code)}`, { signal });
  const o = isObject(v) ? v : {};
  return {
    group: normalizeGroup(pick(o, "group", "Group")),
    accounts: normalizeArray(pick(o, "accounts", "Accounts"), normalizeAccount),
  };
}

/** PUT /groups/{code}/currency. Returns the updated group. */
async function setGroupCurrency(
  client: ApiClient,
  code: string,
  currency: string,
): Promise<Group> {
  const v = await client.request(`${client.baseUrl}/groups/${encode(code)}/currency`, {
    method: "PUT",
    body: { currency },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** PUT /groups/-/default/currency. Returns the default group row. */
async function setDefaultGroupCurrency(
  client: ApiClient,
  currency: string,
): Promise<Group> {
  const v = await client.request(`${client.baseUrl}/groups/-/default/currency`, {
    method: "PUT",
    body: { currency },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** PUT /groups/{code}/notes. Returns the updated group. */
async function setGroupNotes(client: ApiClient, 
  code: string,
  notes: string,
): Promise<Group> {
  const v = await client.request(`${client.baseUrl}/groups/${encode(code)}/notes`, {
    method: "PUT",
    body: { notes },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** POST /groups/{code}/block. Returns the updated group. */
async function blockGroup(client: ApiClient, 
  code: string,
  reason: string,
): Promise<Group> {
  const v = await client.request(`${client.baseUrl}/groups/${encode(code)}/block`, {
    method: "POST",
    body: { reason },
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** POST /groups/{code}/unblock. Returns the updated group. */
async function unblockGroup(client: ApiClient, code: string): Promise<Group> {
  const v = await client.request(`${client.baseUrl}/groups/${encode(code)}/unblock`, {
    method: "POST",
  });
  const o = isObject(v) ? v : {};
  return normalizeGroup(pick(o, "group", "Group"));
}

/** DELETE /groups/{code}. Returns 204; throws ApiError on failure. */
async function deleteGroup(client: ApiClient, code: string): Promise<void> {
  await client.request(`${client.baseUrl}/groups/${encode(code)}`, { method: "DELETE" });
}

// --- Assets ---

function normalizeAsset(v: unknown): Asset {
  const o = isObject(v) ? v : {};
  return {
    code: asString(pick(o, "code", "Code", "id", "Id", "ID")),
    title: asString(pick(o, "title", "Title")),
    assetClass: asString(pick(o, "assetClass", "AssetClass", "asset_class")),
  };
}

/** GET /assets with server-side filtering and sorting. */
async function fetchAssetsPage(
  client: ApiClient,
  filtersOrSignal?: AssetListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<PagedResult<Asset>> {
  const filters = listFiltersFromArg(filtersOrSignal);
  const params = new URLSearchParams();
  appendListFilter(params, "code", filters?.code);
  appendListFilter(params, "codeMatch", filters?.codeMatch, "contains");
  appendListFilter(params, "class", filters?.class);
  appendListFilter(params, "classMatch", filters?.classMatch, "contains");
  appendListFilter(params, "sort", filters?.sort);
  appendListFilter(params, "order", filters?.order);
  appendListFilter(params, "limit", filters?.limit);
  appendListFilter(params, "offset", filters?.offset);
  const query = params.toString();
  const v = await client.request(
    `${client.baseUrl}/assets${query ? `?${query}` : ""}`,
    { signal: signalFromListArg(filtersOrSignal, signal) },
  );
  const o = isObject(v) ? v : {};
  return normalizePagedResult(o, "assets", "Assets", normalizeAsset);
}

async function fetchAssets(
  client: ApiClient,
  filtersOrSignal?: AssetListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<Asset[]> {
  const page = await fetchAssetsPage(client, filtersOrSignal, signal);
  return page.items;
}

/** POST /assets. */
async function createAsset(
  client: ApiClient,
  code: string,
  title: string,
  assetClass: string,
): Promise<Asset> {
  const v = await client.request(`${client.baseUrl}/assets`, {
    method: "POST",
    body: { code, title, assetClass },
  });
  const o = isObject(v) ? v : {};
  return normalizeAsset(pick(o, "asset", "Asset"));
}

/** PUT /assets/{code}. Renames the asset code and updates its title and class;
 *  returns the updated asset. */
async function updateAsset(
  client: ApiClient,
  oldCode: string,
  code: string,
  title: string,
  assetClass: string,
): Promise<Asset> {
  const v = await client.request(`${client.baseUrl}/assets/${encode(oldCode)}`, {
    method: "PUT",
    body: { code, title, assetClass },
  });
  const o = isObject(v) ? v : {};
  return normalizeAsset(pick(o, "asset", "Asset"));
}

/** DELETE /assets/{code}. Returns 204; throws ApiError on failure. */
async function deleteAsset(client: ApiClient, code: string): Promise<void> {
  await client.request(`${client.baseUrl}/assets/${encode(code)}`, { method: "DELETE" });
}

// --- Asset classes ---

function normalizeAssetClass(v: unknown): AssetClass {
  const o = isObject(v) ? v : {};
  return {
    code: asString(pick(o, "code", "Code", "id", "Id", "ID")),
    title: asString(pick(o, "title", "Title")),
    notes: asString(pick(o, "notes", "Notes")),
    assetCount: asInt(pick(o, "assetCount", "AssetCount", "asset_count")),
  };
}

/** GET /asset-classes with server-side filtering and sorting. */
async function fetchAssetClassesPage(
  client: ApiClient,
  filtersOrSignal?: AssetClassListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<PagedResult<AssetClass>> {
  const filters = listFiltersFromArg(filtersOrSignal);
  const params = new URLSearchParams();
  appendListFilter(params, "code", filters?.code);
  appendListFilter(params, "codeMatch", filters?.codeMatch, "contains");
  appendListFilter(params, "notes", filters?.notes);
  appendListFilter(params, "notesMatch", filters?.notesMatch, "contains");
  appendListFilter(params, "sort", filters?.sort);
  appendListFilter(params, "order", filters?.order);
  appendListFilter(params, "limit", filters?.limit);
  appendListFilter(params, "offset", filters?.offset);
  const query = params.toString();
  const v = await client.request(
    `${client.baseUrl}/asset-classes${query ? `?${query}` : ""}`,
    { signal: signalFromListArg(filtersOrSignal, signal) },
  );
  const o = isObject(v) ? v : {};
  return normalizePagedResult(
    o,
    "assetClasses",
    "AssetClasses",
    normalizeAssetClass,
  );
}

async function fetchAssetClasses(
  client: ApiClient,
  filtersOrSignal?: AssetClassListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<AssetClass[]> {
  const page = await fetchAssetClassesPage(client, filtersOrSignal, signal);
  return page.items;
}

/** POST /asset-classes. */
async function createAssetClass(
  client: ApiClient,
  code: string,
  title: string,
  notes: string,
): Promise<AssetClass> {
  const v = await client.request(`${client.baseUrl}/asset-classes`, {
    method: "POST",
    body: { code, title, notes },
  });
  const o = isObject(v) ? v : {};
  return normalizeAssetClass(pick(o, "assetClass", "AssetClass"));
}

/** PUT /asset-classes/{code}. Renames the class code (cascading to the assets'
 *  class link) and updates its title and notes; returns the updated class. */
async function updateAssetClass(
  client: ApiClient,
  oldCode: string,
  code: string,
  title: string,
  notes: string,
): Promise<AssetClass> {
  const v = await client.request(
    `${client.baseUrl}/asset-classes/${encode(oldCode)}`,
    {
      method: "PUT",
      body: { code, title, notes },
    },
  );
  const o = isObject(v) ? v : {};
  return normalizeAssetClass(pick(o, "assetClass", "AssetClass"));
}

/** DELETE /asset-classes/{code}. Returns 204; throws ApiError on failure. */
async function deleteAssetClass(
  client: ApiClient,
  code: string,
  force = false,
): Promise<void> {
  const query = force ? "?force=true" : "";
  await client.request(
    `${client.baseUrl}/asset-classes/${encode(code)}${query}`,
    { method: "DELETE" },
  );
}

// --- Balances (Spot Funds) ---

export interface BalancesFilter {
  account?: string;
  asset?: string;
}

function balanceListQuery(filter: BalanceListFilters = {}): string {
  const params = new URLSearchParams();
  appendListFilter(params, "account", filter.account);
  appendListFilter(params, "groupCode", filter.groupCode);
  appendListFilter(params, "asset", filter.asset);
  appendListFilter(params, "availableMode", filter.availableMode, "all");
  appendListFilter(params, "availableMin", filter.availableMin);
  appendListFilter(params, "availableMax", filter.availableMax);
  appendListFilter(params, "heldMode", filter.heldMode, "all");
  appendListFilter(params, "heldMin", filter.heldMin);
  appendListFilter(params, "heldMax", filter.heldMax);
  appendListFilter(params, "incomingMode", filter.incomingMode, "all");
  appendListFilter(params, "incomingMin", filter.incomingMin);
  appendListFilter(params, "incomingMax", filter.incomingMax);
  appendListFilter(params, "averageEntryPriceMode", filter.averageEntryPriceMode, "all");
  appendListFilter(params, "averageEntryPriceMin", filter.averageEntryPriceMin);
  appendListFilter(params, "averageEntryPriceMax", filter.averageEntryPriceMax);
  appendListFilter(params, "realizedPnlMode", filter.realizedPnlMode, "all");
  appendListFilter(params, "realizedPnlMin", filter.realizedPnlMin);
  appendListFilter(params, "realizedPnlMax", filter.realizedPnlMax);
  appendListFilter(params, "updatedAtMode", filter.updatedAtMode, "all");
  appendListFilter(params, "updatedAfter", filter.updatedAfter);
  appendListFilter(params, "updatedBefore", filter.updatedBefore);
  appendListFilter(params, "sort", filter.sort);
  appendListFilter(params, "order", filter.order);
  appendListFilter(params, "limit", filter.limit);
  appendListFilter(params, "offset", filter.offset);
  const query = params.toString();
  return query === "" ? "" : `?${query}`;
}

/** GET /balances with server-side total. */
async function fetchBalancesPage(
  client: ApiClient,
  filter: BalanceListFilters = {},
  signal?: AbortSignal,
): Promise<PagedResult<Balance>> {
  const v = await client.request(`${client.baseUrl}/balances${balanceListQuery(filter)}`, { signal });
  const o = isObject(v) ? v : {};
  return {
    items: normalizeArray(pick(o, "balances", "Balances"), normalizeBalance),
    total: asInt(pick(o, "total", "Total")),
  };
}

/** GET /balances, optionally filtered by account and/or asset. */
async function fetchBalances(
  client: ApiClient,
  filter: BalancesFilter = {},
  signal?: AbortSignal,
): Promise<Balance[]> {
  return (await fetchBalancesPage(client, filter, signal)).items;
}

export interface AdjustmentBody {
  id?: string;
  externalId?: string;
  asset: string;
  realizedPnl?: string;
  averageEntryPrice?: string;
  balance?: { mode: string; value: string };
  held?: { mode: string; value: string };
  incoming?: { mode: string; value: string };
  balanceBounds?: { lower?: string; upper?: string };
  heldBounds?: { lower?: string; upper?: string };
  incomingBounds?: { lower?: string; upper?: string };
}

export interface BalanceRealizedPnlBody {
  asset: string;
  realizedPnl: string;
}

/** PUT /accounts/{code}/balances/realized-pnl. */
async function setBalanceRealizedPnl(
  client: ApiClient,
  accountCode: string,
  body: BalanceRealizedPnlBody,
): Promise<Balance> {
  const v = await client.request(
    `${client.baseUrl}/accounts/${encode(accountCode)}/balances/realized-pnl`,
    {
      method: "PUT",
      body,
    },
  );
  const o = isObject(v) ? v : {};
  return normalizeBalance(pick(o, "balance", "Balance"));
}

/** POST /accounts/{code}/adjustments. */
async function createAdjustment(client: ApiClient, 
  accountCode: string,
  body: AdjustmentBody,
): Promise<Adjustment | null> {
  const v = await client.request(`${client.baseUrl}/accounts/${encode(accountCode)}/adjustments`, {
    method: "POST",
    body,
  });
  if (v === undefined) {
    return null;
  }
  const o = isObject(v) ? v : {};
  return normalizeAdjustment(pick(o, "adjustment", "Adjustment"));
}

export interface AdjustmentsFilter {
  source?: string;
  limit?: number;
}

/** GET /accounts/{code}/adjustments, newest first. */
async function fetchAccountAdjustments(client: ApiClient, 
  accountCode: string,
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
  const v = await client.request(
    `${client.baseUrl}/accounts/${encode(accountCode)}/adjustments${query}`,
    { signal },
  );
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "adjustments", "Adjustments"), normalizeAdjustment);
}

export interface GlobalAdjustmentsFilter extends PageRequest {
  externalId?: string;
  account?: string;
  accountMatch?: TextMatchMode;
  asset?: string;
  assetMatch?: TextMatchMode;
  source?: string;
  status?: string;
  atMode?: string;
  atMin?: string;
  atMax?: string;
  sort?: string;
  order?: SortOrder;
}

function appendPagedListParams(
  params: URLSearchParams,
  filter: PageRequest & { sort?: string; order?: SortOrder },
) {
  appendListFilter(params, "sort", filter.sort);
  appendListFilter(params, "order", filter.order);
  appendListFilter(params, "limit", filter.limit);
  appendListFilter(params, "offset", filter.offset);
}

function normalizePagedResult<T>(
  source: Json,
  key: string,
  fallbackKey: string,
  normalize: (value: unknown) => T,
): PagedResult<T> {
  return {
    items: normalizeArray(pick(source, key, fallbackKey), normalize),
    total: asInt(pick(source, "total", "Total")),
  };
}

function adjustmentListQuery(filter: GlobalAdjustmentsFilter = {}): string {
  const params = new URLSearchParams();
  appendListFilter(params, "id", filter.externalId);
  appendListFilter(params, "account", filter.account);
  appendListFilter(params, "asset", filter.asset);
  appendListFilter(params, "source", filter.source);
  appendListFilter(params, "status", filter.status, "all");
  appendListFilter(params, "atMode", filter.atMode, "all");
  appendListFilter(params, "atMin", filter.atMin);
  appendListFilter(params, "atMax", filter.atMax);
  appendPagedListParams(params, filter);
  return params.size > 0 ? `?${params.toString()}` : "";
}

/** GET /adjustments with server-side total and offset paging. */
async function fetchAdjustmentsPage(
  client: ApiClient,
  filter: GlobalAdjustmentsFilter = {},
  signal?: AbortSignal,
): Promise<PagedResult<Adjustment>> {
  const v = await client.request(
    `${client.baseUrl}/adjustments${adjustmentListQuery(filter)}`,
    { signal },
  );
  const o = isObject(v) ? v : {};
  return normalizePagedResult(
    o,
    "adjustments",
    "Adjustments",
    normalizeAdjustment,
  );
}

/** GET /adjustments, newest first. */
async function fetchAdjustments(
  client: ApiClient,
  filter: GlobalAdjustmentsFilter = {},
  signal?: AbortSignal,
): Promise<Adjustment[]> {
  const page = await fetchAdjustmentsPage(client, filter, signal);
  return page.items;
}

// --- Orders / Trading ---

export interface CreateOrderBody {
  id?: string;
  externalId?: string;
  account: string;
  baseAsset: string;
  quoteAsset: string;
  side: string;
  amountKind: string;
  amountValue: string;
  price?: string;
  mode?: "immediate" | "hold";
}

export interface CreateOrderResult {
  order: Order;
  warning?: string;
  /** The approval token issued by submit. For the `hold` compatibility mode,
   *  the UI retains it for the workflow confirm/cancel shortcuts; for an
   *  immediate order it is already resolved and unused. */
  approval: ApprovalToken;
}

function normalizeApprovalToken(v: unknown): ApprovalToken {
  const response = isObject(v) ? v : {};
  const wrapped = pick(
    response,
    "approval",
    "Approval",
    "submitResponse",
    "SubmitResponse",
  );
  const o = isObject(wrapped) ? wrapped : response;
  return {
    token: asString(pick(o, "token", "Token")),
    keyId: asString(pick(o, "keyId", "KeyId", "key_id")),
    orderExternalId: asString(
      pick(o, "orderExternalId", "OrderExternalId", "order_external_id"),
    ),
    verdict: normalizeSubmitVerdict(pick(o, "verdict", "Verdict")),
    reasons: normalizeArray(pick(o, "reasons", "Reasons"), normalizeCheckReject),
  };
}

function normalizeSubmitVerdict(v: unknown): ApprovalToken["verdict"] {
  return v === "accept" || v === "reject" ? v : "";
}

function minimalCreatedOrder(
  body: CreateOrderBody,
  token: ApprovalToken,
): Order {
  return {
    externalId: token.orderExternalId,
    account: body.account,
    at: "",
    source: "panel",
    baseAsset: body.baseAsset,
    quoteAsset: body.quoteAsset,
    side: body.side as Order["side"],
    amountKind: body.amountKind as Order["amountKind"],
    amountValue: body.amountValue,
    commissionSubtotals: [],
    leavesQuantity: body.amountValue,
    price: body.price ?? "0",
    status: "submitted",
    displayPrices: [],
    // The submit token is signed but not yet reflected in this placeholder, so
    // it reports unsigned until a real fetch replaces it.
    signed: false,
  };
}

/** POST /orders/submit. Submit creates the order and returns an approval token. */
async function submitOrder(client: ApiClient, 
  body: CreateOrderBody,
  signal?: AbortSignal,
): Promise<ApprovalToken> {
  const requestBody =
    body.id !== undefined || body.externalId === undefined
      ? body
      : { ...body, id: body.externalId, externalId: undefined };
  const v = await client.request(`${client.baseUrl}/orders/submit`, {
    method: "POST",
    body: requestBody,
    signal,
  });
  return normalizeApprovalToken(v);
}

/** POST /orders/submit, then fetch the created order for the dashboard table. */
async function createOrder(client: ApiClient, 
  body: CreateOrderBody,
  signal?: AbortSignal,
): Promise<CreateOrderResult> {
  const token = await submitOrder(client, body, signal);
  try {
    return {
      order: (await fetchOrderDetail(client, token.orderExternalId, signal)).order,
      approval: token,
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return {
      order: minimalCreatedOrder(body, token),
      warning: `Order was created, but detail enrichment failed: ${message}`,
      approval: token,
    };
  }
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
  const rawDisplay = pick(
    o,
    "wouldDisplayPrices",
    "WouldDisplayPrices",
    "would_display_prices",
    "wouldLockPrices",
    "WouldLockPrices",
  );
  return {
    passed: asBool(pick(o, "passed", "Passed")),
    rejects: normalizeArray(pick(o, "rejects", "Rejects"), normalizeCheckReject),
    wouldDisplayPrices: Array.isArray(rawDisplay) ? rawDisplay.map(asString) : [],
    wouldBlock: normalizeCheckWouldBlock(
      pick(o, "wouldBlock", "WouldBlock", "would_block"),
    ),
  };
}

export interface CheckOrderBody {
  account: string;
  baseAsset: string;
  quoteAsset: string;
  side: string;
  amountKind: string;
  amountValue: string;
  price?: string;
}

/** POST /orders/check - pre-trade dry-run; never mutates state. */
async function checkOrder(client: ApiClient, 
  body: CheckOrderBody,
  signal?: AbortSignal,
): Promise<CheckResult> {
  const v = await client.request(`${client.baseUrl}/orders/check`, { method: "POST", body, signal });
  const o = isObject(v) ? v : {};
  return normalizeCheckResult(pick(o, "check", "Check"));
}

export interface OrdersFilter {
  account?: string;
  source?: string;
  limit?: number;
}

function orderListQuery(filter: OrderListFilters = {}): string {
  const params = new URLSearchParams();
  appendListFilter(params, "account", filter.account);
  appendListFilter(params, "source", filter.source);
  appendListFilter(params, "side", filter.side, "all");
  appendListFilter(params, "status", filter.status, "all");
  appendListFilter(params, "baseAsset", filter.baseAsset);
  appendListFilter(params, "quoteAsset", filter.quoteAsset);
  appendListFilter(params, "amountMode", filter.amountMode, "all");
  appendListFilter(params, "amountMin", filter.amountMin);
  appendListFilter(params, "amountMax", filter.amountMax);
  appendListFilter(params, "priceMode", filter.priceMode, "all");
  appendListFilter(params, "priceMin", filter.priceMin);
  appendListFilter(params, "priceMax", filter.priceMax);
  appendListFilter(params, "atMode", filter.atMode, "all");
  appendListFilter(params, "atMin", filter.atMin);
  appendListFilter(params, "atMax", filter.atMax);
  appendListFilter(params, "sort", filter.sort);
  appendListFilter(params, "order", filter.order);
  appendListFilter(params, "limit", filter.limit);
  appendListFilter(params, "offset", filter.offset);
  const query = params.toString();
  return query === "" ? "" : `?${query}`;
}

/** GET /orders with server-side total. */
async function fetchOrdersPage(
  client: ApiClient,
  filter: OrderListFilters = {},
  signal?: AbortSignal,
): Promise<PagedResult<Order>> {
  const v = await client.request(`${client.baseUrl}/orders${orderListQuery(filter)}`, { signal });
  const o = isObject(v) ? v : {};
  return {
    items: normalizeArray(pick(o, "orders", "Orders"), normalizeOrder),
    total: asInt(pick(o, "total", "Total")),
  };
}

/** GET /orders, newest first. */
async function fetchOrders(
  client: ApiClient,
  filter: OrdersFilter = {},
  signal?: AbortSignal,
): Promise<Order[]> {
  return (await fetchOrdersPage(client, filter, signal)).items;
}

/** GET /orders/{externalId}: the order plus its per-event signed timeline and
 *  trades. Signatures are sourced per event (each event carries its own signed
 *  flag and alg); there is no order-level approval envelope. */
async function fetchOrderDetail(client: ApiClient,
  externalId: string,
  signal?: AbortSignal,
): Promise<{ order: Order; events: OrderEvent[]; trades: Trade[] }> {
  const v = await client.request(`${client.baseUrl}/orders/${encode(externalId)}`, { signal });
  const o = isObject(v) ? v : {};
  return {
    order: normalizeOrder(pick(o, "order", "Order")),
    events: normalizeArray(pick(o, "events", "Events"), normalizeOrderEvent),
    trades: normalizeArray(pick(o, "trades", "Trades"), normalizeTrade),
  };
}

// asNullableString maps an absent/null wire value to null and any string to
// itself, so a null canonicalApproval stays distinct from an empty-string one.
function asNullableString(v: unknown): string | null {
  return typeof v === "string" ? v : null;
}

function normalizeSigningKeyFormat(v: unknown): SigningKeyFormat {
  switch (v) {
    case "openssh":
    case "raw-base64":
      return v;
    default:
      return "pem-pkcs8";
  }
}

function normalizePublicKeyMaterial(v: unknown): PublicKeyMaterial | null {
  if (!isObject(v)) {
    return null;
  }
  return {
    keyId: asString(pick(v, "keyId", "KeyId", "key_id")),
    alg: pick(v, "alg", "Alg") === "none" ? "none" : "ed25519",
    format: normalizeSigningKeyFormat(pick(v, "format", "Format")),
    key: asString(pick(v, "key", "Key")),
  };
}

function normalizeApprovalTokenResponse(v: unknown): ApprovalTokenResponse | null {
  if (!isObject(v)) {
    return null;
  }
  return {
    token: asString(pick(v, "token", "Token")),
    keyId: asString(pick(v, "keyId", "KeyId", "key_id")),
    orderExternalId: asString(
      pick(v, "orderExternalId", "OrderExternalId", "order_external_id"),
    ),
    verdict: normalizeSubmitVerdict(pick(v, "verdict", "Verdict")),
    reasons: normalizeArray(pick(v, "reasons", "Reasons"), normalizeCheckReject),
  };
}

function normalizeEventReproductionESign(v: unknown): EventReproductionESign {
  const o = isObject(v) ? v : {};
  return {
    alg: asString(pick(o, "alg", "Alg")),
    noESign: asFlag(pick(o, "noESign", "NoESign", "no_esign")),
    signed: asBool(pick(o, "signed", "Signed")),
  };
}

function normalizeEventAttestation(v: unknown): EventAttestation | null {
  if (!isObject(v)) {
    return null;
  }
  const rawAlg = asString(pick(v, "alg", "Alg"));
  const alg: EventAttestation["alg"] = rawAlg === "none" ? "none" : "ed25519";
  return {
    token: asString(pick(v, "token", "Token")),
    keyId: asString(pick(v, "keyId", "KeyId", "key_id")),
    alg,
    requestType: asString(
      pick(v, "requestType", "RequestType", "request_type"),
    ),
    mode: asString(pick(v, "mode", "Mode")),
    issuedAt: asString(pick(v, "issuedAt", "IssuedAt", "issued_at")),
    signed: asBool(pick(v, "signed", "Signed")),
  };
}

function normalizeAttestationBlock(v: unknown): AttestationBlock {
  const o = isObject(v) ? v : {};
  return {
    account: asString(pick(o, "account", "Account")),
    code: asString(pick(o, "code", "Code")),
    reason: asString(pick(o, "reason", "Reason")),
    details: asString(pick(o, "details", "Details")),
  };
}

function normalizeAttestationResult(v: unknown): AttestationResult | null {
  if (!isObject(v)) {
    return null;
  }
  return {
    outcome: asString(pick(v, "outcome", "Outcome")),
    fillQuantity: asString(pick(v, "fillQuantity", "FillQuantity", "fill_quantity")),
    fillPrice: asString(pick(v, "fillPrice", "FillPrice", "fill_price")),
    fillLockPrice: asString(
      pick(v, "fillLockPrice", "FillLockPrice", "fill_lock_price"),
    ),
    commission: normalizeCommissionOptional(
      pick(v, "commission", "Commission"),
    ),
    leavesQuantity: asString(
      pick(v, "leavesQuantity", "LeavesQuantity", "leaves_quantity"),
    ),
    orderStatus: asString(pick(v, "orderStatus", "OrderStatus", "order_status")),
    blocks: normalizeArray(pick(v, "blocks", "Blocks"), normalizeAttestationBlock),
  };
}

function normalizeEventReproductionRequest(
  v: unknown,
): EventReproductionRequest | null {
  if (!isObject(v)) {
    return null;
  }
  return {
    requestType: asString(pick(v, "requestType", "RequestType", "request_type")),
    orderExternalId: asString(
      pick(v, "orderExternalId", "OrderExternalId", "order_external_id"),
    ),
    eventExternalId: asString(
      pick(v, "eventExternalId", "EventExternalId", "event_external_id"),
    ),
    instrument: asString(pick(v, "instrument", "Instrument")),
    side: asString(pick(v, "side", "Side")),
    quantity: asString(pick(v, "quantity", "Quantity")),
    amountKind: asString(pick(v, "amountKind", "AmountKind", "amount_kind")),
    orderType: asString(pick(v, "orderType", "OrderType", "order_type")),
    limitPrice: asString(pick(v, "limitPrice", "LimitPrice", "limit_price")),
    priceCurrency: asString(
      pick(v, "priceCurrency", "PriceCurrency", "price_currency"),
    ),
    accountId: asString(pick(v, "accountId", "AccountId", "account_id")),
    verdict: asString(pick(v, "verdict", "Verdict")),
    executionReport: normalizeExecutionReportRequestRecord(
      pick(v, "executionReport", "ExecutionReport", "execution_report"),
    ),
    result: normalizeAttestationResult(pick(v, "result", "Result")),
  };
}

function normalizeExecutionReportResponse(
  v: unknown,
): ExecutionReportResponse | null {
  if (!isObject(v)) {
    return null;
  }
  const result = pick(v, "result", "Result");
  const resultObj = isObject(result) ? result : {};
  return {
    blocks: normalizeArray(
      pick(resultObj, "blocks", "Blocks"),
      normalizeAttestationBlock,
    ),
    outcomes: normalizeArray(
      pick(resultObj, "outcomes", "Outcomes"),
      normalizeExecutionOutcome,
    ),
    attestationToken: asString(
      pick(v, "attestationToken", "AttestationToken", "attestation_token"),
    ),
    attestationKeyId: asString(
      pick(v, "attestationKeyId", "AttestationKeyId", "attestation_key_id"),
    ),
    signed: asBool(pick(v, "signed", "Signed")),
  };
}

function normalizeOrderMutationResponse(v: unknown): OrderMutationResponse | null {
  if (!isObject(v)) {
    return null;
  }
  return {
    order: normalizeOrder(pick(v, "order", "Order")),
    attestationToken: asString(
      pick(v, "attestationToken", "AttestationToken", "attestation_token"),
    ),
    attestationKeyId: asString(
      pick(v, "attestationKeyId", "AttestationKeyId", "attestation_key_id"),
    ),
    signed: asBool(pick(v, "signed", "Signed")),
  };
}

function normalizeEventReproductionResponse(
  v: unknown,
): EventReproductionResponse | null {
  if (!isObject(v)) {
    return null;
  }
  return {
    submitResponse: normalizeApprovalTokenResponse(
      pick(v, "submitResponse", "SubmitResponse", "submit_response"),
    ),
    executionReport: normalizeExecutionReportResponse(
      pick(v, "executionReport", "ExecutionReport", "execution_report"),
    ),
    confirm: normalizeOrderMutationResponse(pick(v, "confirm", "Confirm")),
    cancel: normalizeOrderMutationResponse(pick(v, "cancel", "Cancel")),
  };
}

/** GET /orders/{orderExternalId}/events/{eventId}/reproduction: the
 *  controller-facing per-event reproduction bundle. Officer signs every
 *  engine-processed request, so each attested event has its own bundle. Signed
 *  artifacts (token, canonicalApproval, signature, publicKey.key) are returned
 *  verbatim and are never re-serialized here. */
async function fetchEventReproduction(client: ApiClient,
  orderExternalId: string,
  eventId: string,
  signal?: AbortSignal,
): Promise<EventReproduction> {
  const v = await client.request(
    `${client.baseUrl}/orders/${encode(orderExternalId)}/events/${encode(eventId)}/reproduction`,
    { signal },
  );
  const o = isObject(v) ? v : {};
  return {
    requestType: asString(pick(o, "requestType", "RequestType", "request_type")),
    event: normalizeOrderEvent(pick(o, "event", "Event")),
    attestation: normalizeEventAttestation(
      pick(o, "attestation", "Attestation"),
    ),
    request: normalizeEventReproductionRequest(pick(o, "request", "Request")),
    response: normalizeEventReproductionResponse(pick(o, "response", "Response")),
    canonicalApproval: asNullableString(
      pick(o, "canonicalApproval", "CanonicalApproval", "canonical_approval"),
    ),
    publicKey: normalizePublicKeyMaterial(
      pick(o, "publicKey", "PublicKey", "public_key"),
    ),
    eSign: normalizeEventReproductionESign(pick(o, "eSign", "ESign", "e_sign")),
    signature: asString(pick(o, "signature", "Signature")),
    reason: asString(pick(o, "reason", "Reason")),
  };
}

/** GET /signing/keys/{keyId}/public?format= — resolve one key's public material
 *  by id (rotation-safe: not the active key). Only public material is returned. */
async function fetchPublicKeyById(client: ApiClient,
  keyId: string,
  format: SigningKeyFormat,
  signal?: AbortSignal,
): Promise<PublicKeyMaterial> {
  const v = await client.request(
    `${client.baseUrl}/signing/keys/${encode(keyId)}/public?format=${encode(format)}`,
    { signal },
  );
  const material = normalizePublicKeyMaterial(v);
  if (material !== null) {
    return material;
  }
  return { keyId, alg: "ed25519", format, key: "" };
}

export interface ExecutionReportBody {
  /** Fill fields route the report through engine settlement. */
  quantity?: string;
  price?: string;
  /** Required for fills, terminal statuses, and any report carrying commission.
   *  Terminal reports use the remaining quantity the engine must release. */
  leavesQuantity?: string;
  lockPrice?: string;
  commission?: Commission;
  status: string;
  force?: boolean;
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

function normalizeExecutionOutcome(v: unknown): ExecutionOutcome {
  const o = isObject(v) ? v : {};
  return {
    asset: asString(pick(o, "asset", "Asset")),
    balanceDelta: asString(pick(o, "balanceDelta", "BalanceDelta")),
    balanceResult: asString(pick(o, "balanceResult", "BalanceResult")),
    heldDelta: asString(pick(o, "heldDelta", "HeldDelta")),
    heldResult: asString(pick(o, "heldResult", "HeldResult")),
    incomingDelta: asString(pick(o, "incomingDelta", "IncomingDelta")),
    incomingResult: asString(pick(o, "incomingResult", "IncomingResult")),
  };
}

/** POST /orders/{externalId}/execution-reports. Returns the recorded result,
 *  including any engine blocks and balance outcomes, plus the attestation that
 *  proves what Officer recorded for this report. */
async function submitExecutionReport(client: ApiClient,
  orderExternalId: string,
  body: ExecutionReportBody,
): Promise<ExecutionReportResult> {
  const v = await client.request(`${client.baseUrl}/orders/${encode(orderExternalId)}/execution-reports`, {
    method: "POST",
    body,
  });
  const o = isObject(v) ? v : {};
  const result = pick(o, "result", "Result");
  const ro = isObject(result) ? result : {};
  return {
    blocks: normalizeArray(pick(ro, "blocks", "Blocks"), normalizeExecutionBlock),
    outcomes: normalizeArray(
      pick(ro, "outcomes", "Outcomes"),
      normalizeExecutionOutcome,
    ),
    attestationToken: asString(
      pick(o, "attestationToken", "AttestationToken", "attestation_token"),
    ),
    attestationKeyId: asString(
      pick(o, "attestationKeyId", "AttestationKeyId", "attestation_key_id"),
    ),
    signed: asBool(pick(o, "signed", "Signed")),
  };
}

/** POST /orders/{externalId}/confirm body: the approval token from the
 *  workflow (`hold` wire mode) submit. */
export interface ConfirmOrderBody {
  token: string;
}

/** POST /orders/{externalId}/cancel body: the approval token from the workflow
 *  (`hold` wire mode) submit and an optional reason. */
export interface CancelOrderBody {
  token: string;
  reason?: string;
}

function orderMutationResponseOrThrow(v: unknown): OrderMutationResponse {
  const mutation = normalizeOrderMutationResponse(v);
  if (mutation !== null) {
    return mutation;
  }
  // A 2xx with a non-object body is a contract violation; fail loud rather than
  // fabricate a resolved order the caller would render as truth.
  throw new ApiError(
    "order mutation response was not a JSON object",
    "internal",
  );
}

/** POST /orders/{externalId}/confirm. Verifies the approval token and records
 *  order-confirmation history, returning the order plus its attestation. */
async function confirmOrder(client: ApiClient,
  orderExternalId: string,
  body: ConfirmOrderBody,
  signal?: AbortSignal,
): Promise<OrderMutationResponse> {
  const v = await client.request(
    `${client.baseUrl}/orders/${encode(orderExternalId)}/confirm`,
    { method: "POST", body, signal },
  );
  return orderMutationResponseOrThrow(v);
}

/** POST /orders/{externalId}/cancel. Verifies the approval token and applies
 *  the untouched-order cancellation shortcut. */
async function cancelOrder(client: ApiClient,
  orderExternalId: string,
  body: CancelOrderBody,
  signal?: AbortSignal,
): Promise<OrderMutationResponse> {
  const v = await client.request(
    `${client.baseUrl}/orders/${encode(orderExternalId)}/cancel`,
    { method: "POST", body, signal },
  );
  return orderMutationResponseOrThrow(v);
}

export interface TradesFilter extends PageRequest {
  externalId?: string;
  account?: string;
  baseAsset?: string;
  quoteAsset?: string;
  side?: "all" | Order["side"];
  source?: string;
  atMode?: string;
  atMin?: string;
  atMax?: string;
  quantityMode?: RangeFilterMode;
  quantityMin?: string;
  quantityMax?: string;
  priceMode?: RangeFilterMode;
  priceMin?: string;
  priceMax?: string;
  lockPriceMode?: RangeFilterMode;
  lockPriceMin?: string;
  lockPriceMax?: string;
  sort?: string;
  order?: SortOrder;
}

function tradeListQuery(filter: TradesFilter = {}): string {
  const params = new URLSearchParams();
  appendListFilter(params, "id", filter.externalId);
  appendListFilter(params, "account", filter.account);
  appendListFilter(params, "baseAsset", filter.baseAsset);
  appendListFilter(params, "quoteAsset", filter.quoteAsset);
  appendListFilter(params, "side", filter.side, "all");
  appendListFilter(params, "source", filter.source);
  appendListFilter(params, "atMode", filter.atMode, "all");
  appendListFilter(params, "atMin", filter.atMin);
  appendListFilter(params, "atMax", filter.atMax);
  appendListFilter(params, "quantityMode", filter.quantityMode, "all");
  appendListFilter(params, "quantityMin", filter.quantityMin);
  appendListFilter(params, "quantityMax", filter.quantityMax);
  appendListFilter(params, "priceMode", filter.priceMode, "all");
  appendListFilter(params, "priceMin", filter.priceMin);
  appendListFilter(params, "priceMax", filter.priceMax);
  appendListFilter(params, "lockPriceMode", filter.lockPriceMode, "all");
  appendListFilter(params, "lockPriceMin", filter.lockPriceMin);
  appendListFilter(params, "lockPriceMax", filter.lockPriceMax);
  appendPagedListParams(params, filter);
  return params.size > 0 ? `?${params.toString()}` : "";
}

/** GET /trades with server-side total and offset paging. */
async function fetchTradesPage(
  client: ApiClient,
  filter: TradesFilter = {},
  signal?: AbortSignal,
): Promise<PagedResult<Trade>> {
  const v = await client.request(
    `${client.baseUrl}/trades${tradeListQuery(filter)}`,
    { signal },
  );
  const o = isObject(v) ? v : {};
  return normalizePagedResult(o, "trades", "Trades", normalizeTrade);
}

/** GET /trades, newest first. */
async function fetchTrades(
  client: ApiClient,
  filter: TradesFilter = {},
  signal?: AbortSignal,
): Promise<Trade[]> {
  const page = await fetchTradesPage(client, filter, signal);
  return page.items;
}

// --- Limits / Policies ---

/** GET /limits: the unified, paged policy list, with server-side total. Rows
 *  arrive as typed policyDTOs and are flattened into the UI's `Limit` model. */
async function fetchPoliciesPage(
  client: ApiClient,
  input?: PolicyListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<PagedResult<Limit>> {
  const filters = listFiltersFromArg(input);
  const v = await client.request(`${client.baseUrl}/limits${policyListQuery(filters)}`, {
    signal: signalFromListArg(input, signal),
  });
  const o = isObject(v) ? v : {};
  return {
    items: normalizeArray(pick(o, "policies", "Policies"), (row) =>
      policyToLimit(normalizePolicy(row)),
    ),
    total: asInt(pick(o, "total", "Total")),
  };
}

/** GET /limits: the unified, paged policy list. */
async function fetchLimits(
  client: ApiClient,
  input?: PolicyListFilters | AbortSignal,
  signal?: AbortSignal,
): Promise<Limit[]> {
  return (await fetchPoliciesPage(client, input, signal)).items;
}

/** PUT /limits/{policy}: upsert a typed barrier. */
async function putLimit(client: ApiClient, limit: Limit): Promise<Limit> {
  const { path, body } = limitEndpointBody(client, limit);
  const v = await client.request(path, { method: "PUT", body });
  return normalizePutLimitResponse(limit.policy, v);
}

/** DELETE /limits identified by its target tuple. */
async function deleteLimit(client: ApiClient, target: {
  policy: string;
  scope: string;
  account: string;
  accountGroup?: string;
  asset: string;
  accountCurrency?: string;
}): Promise<void> {
  const params = new URLSearchParams({
    policy: target.policy,
    scope: target.scope,
  });
  if (target.account) {
    params.set("account", target.account);
  }
  if (target.accountGroup) {
    params.set("accountGroup", target.accountGroup);
  }
  if (target.asset) {
    params.set("asset", target.asset);
  }
  if (target.accountCurrency) {
    params.set("accountCurrency", target.accountCurrency);
  }
  await client.request(`${client.baseUrl}/limits?${params.toString()}`, { method: "DELETE" });
}

// --- Audit ---

export interface AuditFilter extends PageRequest {
  externalId?: string;
  account?: string;
  accountMatch?: TextMatchMode;
  asset?: string;
  actor?: string;
  actorMatch?: TextMatchMode;
  source?: string;
  /** Action types to include. Undefined lets `category` select the action
   *  predicate; an empty array selects nothing. */
  actions?: string[];
  category?: string;
  atMode?: string;
  atMin?: string;
  atMax?: string;
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
async function getMcpCommands(client: ApiClient, 
  signal?: AbortSignal,
): Promise<McpCommand[]> {
  const v = await client.request(`${client.baseUrl}/mcp-access`, { signal });
  const o = isObject(v) ? v : {};
  return normalizeArray(pick(o, "commands", "Commands"), normalizeMcpCommand);
}

/** PUT /mcp-access/{command} - enable or disable one MCP command. */
async function setMcpCommand(client: ApiClient, 
  name: string,
  enabled: boolean,
): Promise<McpCommand> {
  const v = await client.request(`${client.baseUrl}/mcp-access/${encode(name)}`, {
    method: "PUT",
    body: { enabled },
  });
  const o = isObject(v) ? v : {};
  return normalizeMcpCommand(pick(o, "command", "Command"));
}

// --- User settings ---

/** GET /user-settings - the current operator's UI preferences. */
async function fetchWelcomeSeen(client: ApiClient, signal?: AbortSignal): Promise<boolean> {
  const v = await client.request(`${client.baseUrl}/user-settings`, { signal });
  const o = isObject(v) ? v : {};
  return asBool(pick(o, "welcomeSeen", "WelcomeSeen", "welcome_seen"));
}

/** PUT /user-settings - persist the "don't show the welcome again" choice. */
async function setWelcomeSeen(client: ApiClient, seen: boolean): Promise<boolean> {
  const v = await client.request(`${client.baseUrl}/user-settings`, {
    method: "PUT",
    body: { welcomeSeen: seen },
  });
  const o = isObject(v) ? v : {};
  return asBool(pick(o, "welcomeSeen", "WelcomeSeen", "welcome_seen"));
}

// --- Audit ---

function auditListQuery(filter: AuditFilter = {}): string {
  const params = new URLSearchParams();
  appendListFilter(params, "id", filter.externalId);
  appendListFilter(params, "account", filter.account);
  appendListFilter(params, "asset", filter.asset);
  appendListFilter(params, "actor", filter.actor);
  appendListFilter(params, "actorMatch", filter.actorMatch, "contains");
  appendListFilter(params, "source", filter.source);
  appendListFilter(params, "category", filter.category);
  appendListFilter(params, "atMode", filter.atMode, "all");
  appendListFilter(params, "atMin", filter.atMin);
  appendListFilter(params, "atMax", filter.atMax);
  if (filter.actions && filter.actions.length > 0) {
    params.set("actions", filter.actions.join(","));
  }
  appendPagedListParams(params, filter);
  return params.size > 0 ? `?${params.toString()}` : "";
}

/** GET /audit with server-side total and offset paging. */
async function fetchAuditPage(
  client: ApiClient,
  filter: AuditFilter = {},
  signal?: AbortSignal,
): Promise<PagedResult<AuditEntry>> {
  // An empty action selection matches nothing; skip the round-trip.
  if (filter.actions && filter.actions.length === 0) {
    return { items: [], total: 0 };
  }
  const v = await client.request(
    `${client.baseUrl}/audit${auditListQuery(filter)}`,
    { signal },
  );
  const o = isObject(v) ? v : {};
  return normalizePagedResult(o, "entries", "Entries", normalizeAudit);
}

/** GET /audit, newest first; the backend caps the limit at 1000. */
async function fetchAudit(
  client: ApiClient,
  limitOrFilter: number | AuditFilter,
  signal?: AbortSignal,
): Promise<AuditEntry[]> {
  const page = await fetchAuditPage(
    client,
    typeof limitOrFilter === "number" ? { limit: limitOrFilter } : limitOrFilter,
    signal,
  );
  return page.items;
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
async function fetchAuditActions(client: ApiClient, 
  signal?: AbortSignal,
): Promise<AuditActionGroup[]> {
  const v = await client.request(`${client.baseUrl}/audit/actions`, { signal });
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
async function fetchSigningKeys(client: ApiClient, 
  signal?: AbortSignal,
): Promise<SigningKeysStatus> {
  const [keysV, cfgV] = await Promise.all([
    client.request(`${client.baseUrl}/signing/keys`, { signal }),
    client.request(`${client.baseUrl}/signing/config`, { signal }),
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
async function generateSigningKey(client: ApiClient, ): Promise<SigningKeyResult> {
  const v = await client.request(`${client.baseUrl}/signing/keys/generate`, { method: "POST" });
  return normalizeSigningKeyResult(v);
}

/** POST /signing/keys/import — import a BYOK private key. */
async function importSigningKey(client: ApiClient, 
  key: string,
  format: SigningKeyFormat,
): Promise<SigningKeyResult> {
  const v = await client.request(`${client.baseUrl}/signing/keys/import`, {
    method: "POST",
    body: { key, format },
  });
  return normalizeSigningKeyResult(v);
}

/** GET /signing/keys/active/public?format= — export the active public key. */
async function exportPublicKey(client: ApiClient, 
  format: SigningKeyFormat,
  signal?: AbortSignal,
): Promise<string> {
  const v = await client.request(
    `${client.baseUrl}/signing/keys/active/public?format=${encode(format)}`,
    { signal },
  );
  const o = isObject(v) ? v : {};
  return asString(pick(o, "publicKey", "PublicKey", "public_key"));
}

/** PUT /signing/config — toggle the global eSign flag. */
async function setESignEnabled(client: ApiClient, enabled: boolean): Promise<void> {
  await client.request(`${client.baseUrl}/signing/config`, {
    method: "PUT",
    body: { noESign: !enabled },
  });
}

/** Build the Pit Officer endpoint surface over an injected transport client. */
export function createOfficerApi(client: ApiClient) {
  const bind = <Args extends unknown[], Result>(
    fn: (client: ApiClient, ...args: Args) => Result,
  ) => {
    return (...args: Args) => fn(client, ...args);
  };

  return {
    fetchStatus: bind(fetchStatus),
    fetchHealth: bind(fetchHealth),
    fetchOverview: bind(fetchOverview),
    fetchMarketData: bind(fetchMarketData),
    createMarketDataInstance: bind(createMarketDataInstance),
    updateMarketDataInstanceSettings: bind(updateMarketDataInstanceSettings),
    setMarketDataInstanceEnabled: bind(setMarketDataInstanceEnabled),
    deleteMarketDataInstance: bind(deleteMarketDataInstance),
    upsertMarketDataInstrument: bind(upsertMarketDataInstrument),
    setMarketDataInstrumentEnabled: bind(setMarketDataInstrumentEnabled),
    deleteMarketDataInstrument: bind(deleteMarketDataInstrument),
    restartMarketData: bind(restartMarketData),
    verifyMarketDataSymbol: bind(verifyMarketDataSymbol),
    searchMarketDataSymbols: bind(searchMarketDataSymbols),
    fetchServiceInfo: bind(fetchServiceInfo),
    fetchServiceLogs: bind(fetchServiceLogs),
    restartService: bind(restartService),
    stopService: bind(stopService),
    exportBusinessCsv: bind(exportBusinessCsv),
    previewBusinessCsvImport: bind(previewBusinessCsvImport),
    importBusinessCsv: bind(importBusinessCsv),
    exportBackup: bind(exportBackup),
    restoreBackup: bind(restoreBackup),
    resetDatabase: bind(resetDatabase),
    fetchAccountsPage: bind(fetchAccountsPage),
    fetchAccounts: bind(fetchAccounts),
    createAccount: bind(createAccount),
    updateAccount: bind(updateAccount),
    deleteAccount: bind(deleteAccount),
    fetchAccountState: bind(fetchAccountState),
    blockAccount: bind(blockAccount),
    unblockAccount: bind(unblockAccount),
    setAccountGroup: bind(setAccountGroup),
    setAccountCurrency: bind(setAccountCurrency),
    setAccountNotes: bind(setAccountNotes),
    fetchGroupsPage: bind(fetchGroupsPage),
    fetchGroups: bind(fetchGroups),
    createGroup: bind(createGroup),
    updateGroup: bind(updateGroup),
    fetchGroupState: bind(fetchGroupState),
    setGroupCurrency: bind(setGroupCurrency),
    setDefaultGroupCurrency: bind(setDefaultGroupCurrency),
    setGroupNotes: bind(setGroupNotes),
    blockGroup: bind(blockGroup),
    unblockGroup: bind(unblockGroup),
    deleteGroup: bind(deleteGroup),
    fetchAssetsPage: bind(fetchAssetsPage),
    fetchAssets: bind(fetchAssets),
    createAsset: bind(createAsset),
    updateAsset: bind(updateAsset),
    deleteAsset: bind(deleteAsset),
    fetchAssetClassesPage: bind(fetchAssetClassesPage),
    fetchAssetClasses: bind(fetchAssetClasses),
    createAssetClass: bind(createAssetClass),
    updateAssetClass: bind(updateAssetClass),
    deleteAssetClass: bind(deleteAssetClass),
    fetchBalancesPage: bind(fetchBalancesPage),
    fetchBalances: bind(fetchBalances),
    setBalanceRealizedPnl: bind(setBalanceRealizedPnl),
    createAdjustment: bind(createAdjustment),
    fetchAccountAdjustments: bind(fetchAccountAdjustments),
    fetchAdjustmentsPage: bind(fetchAdjustmentsPage),
    fetchAdjustments: bind(fetchAdjustments),
    submitOrder: bind(submitOrder),
    createOrder: bind(createOrder),
    checkOrder: bind(checkOrder),
    fetchOrdersPage: bind(fetchOrdersPage),
    fetchOrders: bind(fetchOrders),
    fetchOrderDetail: bind(fetchOrderDetail),
    fetchEventReproduction: bind(fetchEventReproduction),
    submitExecutionReport: bind(submitExecutionReport),
    confirmOrder: bind(confirmOrder),
    cancelOrder: bind(cancelOrder),
    fetchTradesPage: bind(fetchTradesPage),
    fetchTrades: bind(fetchTrades),
    fetchPoliciesPage: bind(fetchPoliciesPage),
    fetchLimits: bind(fetchLimits),
    putLimit: bind(putLimit),
    deleteLimit: bind(deleteLimit),
    getMcpCommands: bind(getMcpCommands),
    setMcpCommand: bind(setMcpCommand),
    fetchWelcomeSeen: bind(fetchWelcomeSeen),
    setWelcomeSeen: bind(setWelcomeSeen),
    fetchAuditPage: bind(fetchAuditPage),
    fetchAudit: bind(fetchAudit),
    fetchAuditActions: bind(fetchAuditActions),
    fetchSigningKeys: bind(fetchSigningKeys),
    generateSigningKey: bind(generateSigningKey),
    importSigningKey: bind(importSigningKey),
    exportPublicKey: bind(exportPublicKey),
    fetchPublicKeyById: bind(fetchPublicKeyById),
    setESignEnabled: bind(setESignEnabled),
  } as const;
}

/** Same-origin log download URL for a host-provided API base URL. */
export function serviceLogsDownloadUrl(baseUrl: string): string {
  return `${baseUrl}/service/logs/download`;
}

export type OfficerApi = ReturnType<typeof createOfficerApi>;
