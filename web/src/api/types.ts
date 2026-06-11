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

// Normalized view of the control-plane types. The raw wire shape is normalized
// in api/client.ts so the UI does not depend on the JSON casing the Go handler
// emits.

// --- Legacy types (status/health endpoint, kept for backward compatibility) ---

/** Health of a single node's engine adapter (mirrors engine.Health in Go). */
export interface EngineHealth {
  version: string;
  buildProfile: string;
  running: boolean;
}

/** Health of a single node's store (mirrors store.StoreHealth in Go). */
export interface StoreHealth {
  path: string;
  schemaVersion: number;
  reachable: boolean;
}

/** Aggregate health of one node (mirrors node.Health in Go). */
export interface NodeHealth {
  engine: EngineHealth;
  store: StoreHealth;
}

/** Deployment-wide status (mirrors backend.Status in Go). */
export interface Status {
  nodes: NodeHealth[];
  healthy: boolean;
}

/** Liveness response from GET /health. */
export interface Health {
  ok: boolean;
}

// --- Accounts ---

/** An operator account (mirrors the accountDTO wire shape). */
export interface Account {
  id: string;
  blocked: boolean;
  blockReason: string;
  group: string;
  notes: string;
}

// --- Groups ---

/** An account group (mirrors the groupDTO wire shape). */
export interface Group {
  id: string;
  notes: string;
  blocked: boolean;
  blockReason: string;
}

// --- Limits / Policies ---

/** One risk-limit barrier: the full set of kind/value pairs for a target
 *  (mirrors the limitDTO wire shape). Values is an ordered kind -> value map.
 */
export interface Limit {
  policy: string;
  scope: string;
  account: string;
  asset: string;
  values: Record<string, string>;
}

// --- Balances / Spot Funds ---

/** Adjustment amount mode. */
export type AdjustmentMode = "absolute" | "delta";

/** An amount field in an adjustment request/response. */
export interface AdjustmentAmount {
  mode: AdjustmentMode;
  value: string;
}

/** Bound pair (lower/upper, each optional). */
export interface BoundsPair {
  lower?: string;
  upper?: string;
}

/** Spot-funds balance for one (account, asset) pair. */
export interface Balance {
  account: string;
  asset: string;
  available: string;
  held: string;
  incoming: string;
  averageEntryPrice: string;
  updatedAt: string;
}

/** The accepted side of a settled adjustment (mirrors adjustmentAcceptedDTO). */
export interface AdjustmentAccepted {
  balanceDelta: string;
  balanceResult: string;
  heldDelta: string;
  heldResult: string;
  incomingDelta: string;
  incomingResult: string;
}

/** The rejected side of an adjustment (mirrors adjustmentRejectedDTO). */
export interface AdjustmentRejected {
  code: string;
  scope: string;
  policy: string;
  reason: string;
  details: string;
}

/** The request body captured on an adjustment record. */
export interface AdjustmentRequest {
  asset: string;
  balance?: AdjustmentAmount;
  held?: AdjustmentAmount;
  incoming?: AdjustmentAmount;
  balanceBounds?: BoundsPair;
  heldBounds?: BoundsPair;
  incomingBounds?: BoundsPair;
  averageEntryPrice?: string;
}

/** One balance adjustment record (mirrors adjustmentDTO). */
export interface Adjustment {
  id: number;
  account: string;
  at: string;
  source: Source;
  asset: string;
  status: "accepted" | "rejected";
  request: AdjustmentRequest;
  accepted?: AdjustmentAccepted;
  rejected?: AdjustmentRejected;
}

// --- Orders / Trading ---

/** Source of an action: panel, REST API, MCP, or internal system. */
export type Source = "panel" | "api" | "mcp" | "system";

/** Side of an order. */
export type OrderSide = "buy" | "sell";

/** How the order amount is denominated. */
export type AmountKind = "base" | "quote";

/** Lock prices captured on order submission. */
export interface LockPrices {
  [asset: string]: string;
}

/** An order record. */
export interface Order {
  id: number;
  account: string;
  at: string;
  source: Source;
  baseAsset: string;
  quoteAsset: string;
  side: OrderSide;
  amountKind: AmountKind;
  amountValue: string;
  price: string;
  status: string;
  lockPrices: LockPrices;
}

/** An event on an order's lifecycle. */
export interface OrderEvent {
  id: number;
  orderId: number;
  at: string;
  type: string;
  source: Source;
  principal: string;
  rejectCode?: string;
  rejectScope?: string;
  rejectPolicy?: string;
  rejectReason?: string;
  rejectDetails?: string;
  fillQuantity?: string;
  fillPrice?: string;
  fillLockPrice?: string;
}

/** A completed trade derived from a fill. */
export interface Trade {
  id: number;
  orderId: number;
  account: string;
  at: string;
  source: Source;
  baseAsset: string;
  quoteAsset: string;
  side: OrderSide;
  quantity: string;
  price: string;
  lockPrice: string;
}

// --- Dashboard / Overview ---

/** One activity entry from GET /overview. */
export interface ActivityEntry {
  source: Source;
  at: string;
  kind: string;
  ref: string;
  summary: string;
}

/** Response from GET /overview. */
export interface Overview {
  counts: {
    accounts: number;
    groups: number;
    limits: number;
  };
  activity: ActivityEntry[];
}

// --- Service info ---

/** Response from GET /service. */
export interface ServiceInfo {
  name: string;
  engineVersion: string;
  engineBuildProfile: string;
  release: boolean;
  database: {
    path: string;
    reachable: boolean;
  };
}

// --- Audit ---

/** One audit-log entry (mirrors the auditDTO wire shape). */
export interface AuditEntry {
  id: number;
  at: string;
  actor: string;
  action: string;
  account: string;
  detail: string;
  source: Source;
}
