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
  code: string;
  title: string;
  blocked: boolean;
  blockReason: string;
  group: string;
  notes: string;
}

// --- Groups ---

/** An account group (mirrors the groupDTO wire shape). */
export interface Group {
  code: string;
  title: string;
  notes: string;
  blocked: boolean;
  blockReason: string;
}

// --- Limits / Policies ---

/** Typed wire shape for a rate-limit barrier. */
export interface RateLimit {
  scope: string;
  account: string;
  asset: string;
  windowMs: number;
  maxOrders: number;
}

/** Typed wire shape for an order-size barrier. */
export interface OrderSizeLimit {
  scope: string;
  account: string;
  asset: string;
  maxQuantity: string;
  maxNotional: string;
}

/** Typed wire shape for a P&L-bounds kill-switch barrier. */
export interface PnlBoundsLimit {
  scope: string;
  account: string;
  asset: string;
  lowerBound: string;
  upperBound: string;
  initialPnl: string;
}

/** Per-policy typed limits returned by the account-state and limit-list APIs. */
export interface AccountLimits {
  rateLimits: RateLimit[];
  orderSizeLimits: OrderSizeLimit[];
  pnlBoundsLimits: PnlBoundsLimit[];
}

/** One risk-limit barrier in the UI's flat policy/value model. Values is an
 *  ordered kind -> value map.
 */
export interface Limit {
  policy: string;
  scope: string;
  account: string;
  asset: string;
  values: Record<string, string>;
}

// --- Balances ---

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
  realizedPnl: string;
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
  externalId?: string;
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
  externalId: string;
  account: string;
  at: string;
  principal?: string;
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
export type AmountKind = "quantity" | "volume";

/** An order record. */
export interface Order {
  externalId: string;
  account: string;
  at: string;
  principal?: string;
  source: Source;
  baseAsset: string;
  quoteAsset: string;
  side: OrderSide;
  amountKind: AmountKind;
  amountValue: string;
  price: string;
  status: string;
  /** Human-readable reservation prices, as exact decimal strings. */
  displayPrices: string[];
}

/** An event on an order's lifecycle.
 * Reject fields are present on rejected events and account-blocking fill events.
 */
export interface OrderEvent {
  externalId: string;
  order: string;
  at: string;
  type: string;
  source: Source;
  principal?: string;
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
  externalId: string;
  order: string;
  account: string;
  at: string;
  principal?: string;
  source: Source;
  baseAsset: string;
  quoteAsset: string;
  side: OrderSide;
  quantity: string;
  price: string;
  lockPrice: string;
}

/** One pre-trade check reject entry (mirrors the checkRejectDTO wire shape). */
export interface CheckReject {
  code: string;
  scope: string;
  policy: string;
  reason: string;
  details: string;
}

/** Would-be account block from a pre-trade check. */
export interface CheckWouldBlock {
  account: string;
  code: string;
  reason: string;
  details: string;
}

/** Result of a POST /orders/check dry-run (mirrors the checkDTO wire shape). */
export interface CheckResult {
  passed: boolean;
  rejects: CheckReject[];
  wouldDisplayPrices: string[];
  wouldBlock: CheckWouldBlock | null;
}

/** One account block the engine recorded while settling an execution report. */
export interface ExecutionBlock {
  account: string;
  code: string;
  reason: string;
  details: string;
}

/** Result of POST /orders/{externalId}/execution-reports. */
export interface ExecutionReportResult {
  blocks: ExecutionBlock[];
}

export interface ApprovalToken {
  token: string;
  keyId: string;
  expiresAt: string;
  orderExternalId: string;
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
    accountsActive: number;
    groups: number;
    groupsActive: number;
    limits: number;
    ordersToday: number;
    ordersTotal: number;
  };
  activity: ActivityEntry[];
}

// --- Market data ---

export interface MarketDataProvider {
  type: string;
  title: string;
}

export interface MarketDataQuote {
  asOf: string;
  receivedAt: string;
  mark: string;
  bid: string;
  ask: string;
}

export interface MarketDataInstrument {
  instanceExternalId: string;
  externalSymbol: string;
  baseAsset: string;
  quoteAsset: string;
  /** Operator-set manual mark price (exact decimal string); empty when none.
   *  Applies only to bring-your-own (manual) instruments. */
  manualPrice: string;
  enabled: boolean;
  stale: boolean;
  /** Elapsed milliseconds between the two most recent ticks of this quote, as
   *  observed by the running manager. Undefined until a second tick has arrived
   *  since the last (re)subscribe. A fixed measurement, not an age. */
  updateIntervalMs?: number;
  quote?: MarketDataQuote;
}

export interface MarketDataDiagnosticAction {
  type: string;
  target?: string;
}

export interface MarketDataDiagnostic {
  level: string;
  code: string;
  kind: string;
  title: string;
  detail: string;
  remediation?: string;
  instrument?: string;
  actions: MarketDataDiagnosticAction[];
  at: string;
}

/** Provider help links exposed by a feed instance. */
export interface MarketDataReferences {
  docsUrl?: string;
  symbolsUrl?: string;
}

export interface MarketDataInstance {
  externalId: string;
  provider: string;
  label: string;
  credentials: string;
  settings?: Record<string, unknown>;
  secrets?: Record<string, boolean>;
  enabled: boolean;
  state: string;
  error?: string;
  /** Whether the provider can verify external-symbol existence (gates the
   *  verify button without making a call). */
  verifiesSymbols: boolean;
  /** Whether the provider can search its catalogue for matching symbols (gates
   *  the live-search control without making a call). */
  searchesSymbols: boolean;
  instruments: MarketDataInstrument[];
  diagnostics: MarketDataDiagnostic[];
  references?: MarketDataReferences;
}

/** Outcome of POST /market-data/instances/{externalId}/verify-symbol. */
export interface MarketDataSymbolVerification {
  /** False when the provider cannot verify symbols. */
  supported: boolean;
  /** Whether the external symbol exists in the provider's catalogue. */
  exists: boolean;
  /** Case-folded catalogue variant the operator likely meant, when present. */
  suggestion?: string;
  /** Optional provider metadata for the matched symbol. */
  details?: string;
}

/** One resolved contract from a symbol search (mirrors the wire DTO). */
export interface MarketDataSymbolMatch {
  symbol: string;
  name?: string;
  secType: string;
  exchange?: string;
  primaryExchange?: string;
  currency?: string;
  conId?: string;
  lastTradeDateOrContractMonth?: string;
  strike?: string;
  right?: string;
  multiplier?: string;
  localSymbol?: string;
  tradingClass?: string;
}

/** Outcome of POST /market-data/instances/{externalId}/search-symbols. */
export interface MarketDataSymbolSearch {
  /** False when the provider cannot search symbols. */
  supported: boolean;
  matches: MarketDataSymbolMatch[];
}

export interface MarketDataStatus {
  providers: MarketDataProvider[];
  instances: MarketDataInstance[];
  freshnessSeconds: number;
  restartRequired: boolean;
}

// --- Service info ---

/** Response from GET /service. */
export interface ServiceInfo {
  name: string;
  engineVersion: string;
  engineBuildProfile: string;
  release: boolean;
  /** Whether the engine was built from a clean source tree. Absent until the
   *  SDK exposes a precise dirty-sources flag; when present and false the build
   *  posture badge reflects "modified sources". */
  buildClean?: boolean;
  database: {
    path: string;
    reachable: boolean;
  };
}

/** Response from GET /service/logs: the captured log tail, oldest line first. */
export interface ServiceLogs {
  lines: string[];
  count: number;
}

// --- Backup / restore ---

export type BackupSection =
  | "accounts_groups"
  | "positions"
  | "risk_limits"
  | "market_data_settings"
  | "market_data_quotes"
  | "general_settings"
  | "user_settings"
  | "activity_history"
  | "audit_log";

export type RestoreMode = "replace_all" | "overwrite" | "insert_missing";

export interface BackupEntitySelector {
  all?: boolean;
  accounts?: string[];
  groups?: string[];
}

export interface BackupScope {
  all?: boolean;
  sections?: BackupSection[];
  accounts?: BackupEntitySelector;
  positions?: BackupEntitySelector;
}

export interface BackupManifest {
  format: "openpit.officer.backup";
  formatVersion: number;
  schemaVersion: number;
  createdAt: string;
  source?: string;
  sections: BackupSection[];
}

export interface BackupData {
  mcpAccess?: Record<string, boolean>;
  accounts?: Record<string, unknown>[];
  groups?: Record<string, unknown>[];
  limits?: Record<string, unknown>[];
  balances?: Record<string, unknown>[];
  adjustments?: Record<string, unknown>[];
  orders?: Record<string, unknown>[];
  orderEvents?: Record<string, unknown>[];
  trades?: Record<string, unknown>[];
  audit?: Record<string, unknown>[];
  marketDataInstances?: Record<string, unknown>[];
  marketDataInstruments?: Record<string, unknown>[];
  marketDataQuotes?: Record<string, unknown>[];
}

export interface BackupArchive {
  manifest: BackupManifest;
  data: BackupData;
}

export interface BackupRestoreSummary {
  applied: Partial<Record<BackupSection, number>>;
  skipped: Partial<Record<BackupSection, number>>;
  restartRequired: boolean;
}

// --- Business CSV import / export ---

export type BusinessCsvEntity =
  | "account_groups"
  | "accounts"
  | "positions"
  | "orders"
  | "trades";

export type BusinessCsvImportEntity =
  | "account_groups"
  | "accounts"
  | "positions";

export type BusinessCsvDelimiter = "comma" | "semicolon" | "tab" | "pipe";

export type BusinessCsvConflictPolicy = "skip" | "replace" | "stop";

export interface BusinessCsvExportFilters {
  groupCode?: string | null;
  account?: string;
  asset?: string;
  source?: Source;
}

export interface BusinessCsvImportFile {
  name: string;
  type: "csv" | "zip" | string;
}

export interface BusinessCsvImportCounts {
  rows: number;
  applied: number;
  skipped: number;
  conflicts: number;
  stopped: boolean;
}

export interface BusinessCsvConflict {
  row: number;
  key: string;
}

export interface BusinessCsvImportPreview {
  file: BusinessCsvImportFile;
  counts: BusinessCsvImportCounts;
  conflicts: BusinessCsvConflict[];
}

export type BusinessCsvImportResult = BusinessCsvImportPreview;

// --- MCP access ---

/** One entry from GET /mcp-access (mirrors the mcpCommandDTO wire shape). */
export interface McpCommand {
  name: string;
  title: string;
  agentDescription: string;
  mutating: boolean;
  protective: boolean;
  implemented: boolean;
  enabled: boolean;
}

// --- Signing keys ---

/** Public-key export format. */
export type SigningKeyFormat = "pem-pkcs8" | "openssh" | "raw-base64";

/** One signing key entry (mirrors the signingKeyDTO wire shape). PrivateKey is
 *  never present in any DTO. */
export interface SigningKey {
  keyId: string;
  fingerprint: string;
  createdAt: string;
  active: boolean;
}

/** GET /signing/keys response: list of known public keys + global config. */
export interface SigningKeysStatus {
  keys: SigningKey[];
  eSignEnabled: boolean;
}

/** Response for generate/import: the new key entry plus its exported public key
 *  in the default (pem-pkcs8) format. */
export interface SigningKeyResult {
  key: SigningKey;
  publicKey: string;
}

// --- Order approval / signed pre-trade verdict ---

/** Signing algorithm used in the approval envelope. */
export type ApprovalAlg = "ed25519" | "none";

/** The signed pre-trade verdict envelope attached to an order, or null when the
 *  order has no envelope (no signer configured / pre-existing order). */
export interface OrderApproval {
  /** Base64url-encoded envelope JSON: { approval, signature, keyId, alg }. */
  token: string;
  /** UUID of the signing key, or empty string when alg is "none". */
  keyId: string;
  alg: ApprovalAlg;
  /** Submission mode; currently always "immediate". */
  mode: string;
  /** RFC3339Nano timestamp at which the envelope was issued. May be empty. */
  issuedAt: string;
  /** RFC3339Nano expiry timestamp. May be empty. */
  expiresAt: string;
  /** Whether the envelope carries a real cryptographic signature. */
  signed: boolean;
}

// --- Audit ---

/** One audit-log entry (mirrors the auditDTO wire shape). */
export interface AuditEntry {
  externalId: string;
  at: string;
  actor: string;
  actorTitle: string;
  action: string;
  account: string;
  accountTitle: string;
  detail: string;
  source: Source;
}

/** One dependent row kind that blocks a destructive delete. */
export interface ApiErrorDependent {
  kind: string;
  count: number;
}

/** One category of the audit action catalogue (mirrors the wire shape from
 *  GET /audit/actions). Category is a stable key (e.g. "control", "trading"),
 *  control first in canonical order; labels stay in i18n. */
export interface AuditActionGroup {
  category: string;
  actions: string[];
}
