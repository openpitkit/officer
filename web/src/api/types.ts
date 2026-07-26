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
  currency?: string;
  effectiveCurrency?: string;
  currencyOrigin?: string;
  currencyCascade?: {
    account: string;
    group: string;
    default: string;
  };
  blocked: boolean;
  blockReason: string;
  group: string;
  notes: string;
  pnl?: string;
  pnlHaltReason: string;
  positionCount?: number;
}

/** Text pattern anchoring used by account/group list filters. */
export type TextMatchMode = "contains" | "starts_with" | "ends_with" | "exact";

/** Blocked-state account/group list filter. */
export type StatusListFilter = "all" | "active" | "blocked";

/** Aggregate count list filter. */
export type CountListFilter = "all" | "has" | "none";

/** Numeric position-count filter mode. */
export type PositionCountFilterMode =
  | "all"
  | "eq"
  | "neq"
  | "gt"
  | "gte"
  | "lt"
  | "lte"
  | "greater_than"
  | "less_than"
  | "between";

/** Sort direction accepted by list endpoints. */
export type SortOrder = "asc" | "desc";

/** Server-side page request. */
export interface PageRequest {
  limit?: number;
  offset?: number;
}

/** Server-side sort request. */
export interface SortSpec {
  sort?: string;
  order?: SortOrder;
}

/** Paged list response with total count before limit/offset. */
export interface PagedResult<T> {
  items: T[];
  total: number;
}

/** Filter options accepted by GET /accounts. */
export interface AccountListFilters extends PageRequest, SortSpec {
  code?: string;
  codeMatch?: TextMatchMode;
  status?: StatusListFilter;
  positionCountMode?: PositionCountFilterMode;
  positionCountMin?: string;
  positionCountMax?: string;
  blockReason?: string;
  blockReasonMatch?: TextMatchMode;
  /** Exact group selection (empty string = accounts with no group). */
  group?: string;
}

// --- Assets ---

/** A tradable asset registered in the control plane (mirrors the assetDTO wire
 *  shape). */
export interface Asset {
  code: string;
  title: string;
  assetClass: string;
}

/** Filter options accepted by GET /assets. */
export interface AssetListFilters extends PageRequest, SortSpec {
  code?: string;
  codeMatch?: TextMatchMode;
  class?: string;
  classMatch?: TextMatchMode;
}

/** An asset class entry from the class dictionary (mirrors the assetClassDTO
 *  wire shape). */
export interface AssetClass {
  code: string;
  title: string;
  notes: string;
  assetCount: number;
}

/** Filter options accepted by GET /asset-classes. */
export interface AssetClassListFilters extends PageRequest, SortSpec {
  code?: string;
  codeMatch?: TextMatchMode;
  notes?: string;
  notesMatch?: TextMatchMode;
}

// --- Groups ---

/** An account group (mirrors the groupDTO wire shape). */
export interface Group {
  code: string;
  title: string;
  currency?: string;
  notes: string;
  blocked: boolean;
  blockReason: string;
  accountCount?: number;
  positionCount?: number;
}

/** Filter options accepted by GET /groups. */
export interface GroupListFilters extends PageRequest, SortSpec {
  code?: string;
  codeMatch?: TextMatchMode;
  status?: StatusListFilter;
  positionCountMode?: PositionCountFilterMode;
  positionCountMin?: string;
  positionCountMax?: string;
  accountCountMode?: PositionCountFilterMode;
  accountCountMin?: string;
  accountCountMax?: string;
  notes?: string;
  notesMatch?: TextMatchMode;
  blockReason?: string;
  blockReasonMatch?: TextMatchMode;
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

/** Typed wire shape for a self-computed SpotFunds P&L-bounds barrier. */
export interface SpotFundsPnlBoundsLimit {
  scope: string;
  account: string;
  accountGroup: string;
  lowerBound: string;
  upperBound: string;
}

/** Per-policy typed limits returned by the account-state and limit-list APIs. */
export interface AccountLimits {
  rateLimits: RateLimit[];
  orderSizeLimits: OrderSizeLimit[];
  spotFundsPnlBoundsLimits: SpotFundsPnlBoundsLimit[];
}

/** One risk-limit barrier in the UI's flat policy/value model. Values is an
 *  ordered kind -> value map.
 */
export interface Limit {
  policy: string;
  scope: string;
  account: string;
  accountGroup?: string;
  asset: string;
  values: Record<string, string>;
}

/** Discriminant of a policy row returned by the unified GET /limits list. */
export type PolicyKind =
  | "rate_limit"
  | "order_size_limit"
  | "spot_funds_pnl_bounds_kill_switch";

/** Rate-limit barrier values (window in ms, integer order cap). */
export interface PolicyRateValues {
  windowMs: number;
  maxOrders: number;
}

/** Order-size barrier values as exact decimal strings (empty when unset). */
export interface PolicyOrderSizeValues {
  maxQuantity: string;
  maxNotional: string;
}

/** P&L-bounds barrier values as exact decimal strings (empty when unset). */
export interface PolicyPnlBoundsValues {
  lowerBound: string;
  upperBound: string;
}

/** Discriminated value carrier: exactly one member is present per `kind`. */
export interface PolicyValues {
  rate?: PolicyRateValues;
  orderSize?: PolicyOrderSizeValues;
  spotFundsPnlBounds?: PolicyPnlBoundsValues;
}

/** One policy row from the unified, paged GET /limits list (the policyDTO wire
 *  shape). The `kind` selects which member of `values` is populated. */
export interface Policy {
  kind: PolicyKind;
  scope: string;
  account: string;
  accountGroup: string;
  asset: string;
  values: PolicyValues;
}

/** Policy-kind selector accepted by the GET /limits `policy` query param. */
export type PolicyFilter =
  | "all"
  | "rate"
  | "order_size"
  | "spot_funds_pnl_bounds";

/** Filter options accepted by the unified, paged GET /limits list. */
export interface PolicyListFilters extends PageRequest, SortSpec {
  account?: string;
  accountGroup?: string;
  asset?: string;
  policy?: PolicyFilter;
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

/** Spot-funds balance for one (account, asset) pair. `available`, `held` and
 *  `incoming` are quantities of `asset`; `averageEntryPrice` and `realizedPnl`
 *  are denominated in `accountCurrency` instead. */
export interface Balance {
  account: string;
  asset: string;
  available: string;
  held: string;
  incoming: string;
  averageEntryPrice: string;
  realizedPnl: string;
  realizedPnlHaltReason: string;
  /** Asset code denominating `averageEntryPrice` and `realizedPnl`: the
   *  account's effective currency. One (account, asset) slot merges fills
   *  against every quote asset the asset trades against, so a single fill's
   *  quote asset never denominates these values. Empty when the account's
   *  currency cascade sets no tier, leaving both values without a unit — which
   *  is also what the engine reports as the `missing_account_currency` halt. */
  accountCurrency: string;
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
  averageEntryPrice?: string;
  realizedPnlResult?:
    | string
    | {
        delta: string;
        result: string;
      };
  realizedPnlHaltReason?: string;
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
  id?: string;
  asset: string;
  balance?: AdjustmentAmount;
  held?: AdjustmentAmount;
  incoming?: AdjustmentAmount;
  balanceBounds?: BoundsPair;
  heldBounds?: BoundsPair;
  incomingBounds?: BoundsPair;
  averageEntryPrice?: string;
  realizedPnl?: string;
}

/** One balance adjustment record (mirrors adjustmentDTO). */
export interface Adjustment {
  id: string;
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

/** Numeric range filter mode for exact decimal and time fields. */
export type RangeFilterMode =
  | "all"
  | "eq"
  | "neq"
  | "gt"
  | "gte"
  | "lt"
  | "lte"
  | "after"
  | "before"
  | "greater_than"
  | "less_than"
  | "between";

/** Filter options accepted by GET /orders. */
export interface OrderListFilters extends PageRequest, SortSpec {
  account?: string;
  source?: string;
  side?: "all" | OrderSide;
  status?: string;
  baseAsset?: string;
  quoteAsset?: string;
  amountMode?: RangeFilterMode;
  amountMin?: string;
  amountMax?: string;
  priceMode?: RangeFilterMode;
  priceMin?: string;
  priceMax?: string;
  atMode?: RangeFilterMode;
  atMin?: string;
  atMax?: string;
}

/** Filter options accepted by GET /balances. */
export interface BalanceListFilters extends PageRequest, SortSpec {
  account?: string;
  groupCode?: string;
  asset?: string;
  availableMode?: RangeFilterMode;
  availableMin?: string;
  availableMax?: string;
  heldMode?: RangeFilterMode;
  heldMin?: string;
  heldMax?: string;
  incomingMode?: RangeFilterMode;
  incomingMin?: string;
  incomingMax?: string;
  averageEntryPriceMode?: RangeFilterMode;
  averageEntryPriceMin?: string;
  averageEntryPriceMax?: string;
  /** Asset the average-entry-price bounds are expressed in. Required whenever
   *  the mode is not `all`: the column is denominated per row, so only rows in
   *  this currency are compared and the rest are excluded, never converted. */
  averageEntryPriceCurrency?: string;
  realizedPnlMode?: RangeFilterMode;
  realizedPnlMin?: string;
  realizedPnlMax?: string;
  /** Asset the realized-P&L bounds are expressed in. Required whenever the
   *  mode is not `all`; see `averageEntryPriceCurrency`. */
  realizedPnlCurrency?: string;
  updatedAtMode?: RangeFilterMode;
  updatedAfter?: string;
  updatedBefore?: string;
}

/** An order record. */
export interface Commission {
  amount: string;
  currency: string;
}

/** Audit-safe execution-report input captured before Officer enriches it with
 *  order-derived values or normalizes the persisted result. The opaque engine
 *  lock is deliberately excluded. */
export interface ExecutionReportRequestRecord {
  id: string;
  baseAsset: string;
  quoteAsset: string;
  fillQuantity: string;
  fillPrice: string;
  leavesQuantity: string;
  lockPrice: string;
  commission: Commission | null;
  order: string;
  account: string;
  side: string;
  orderStatus: string;
  force: boolean;
}

export interface Order {
  id: string;
  account: string;
  at: string;
  principal?: string;
  source: Source;
  baseAsset: string;
  quoteAsset: string;
  side: OrderSide;
  amountKind: AmountKind;
  amountValue: string;
  commissionSubtotals: Commission[];
  /** Remaining open base quantity persisted from stored order/report data. */
  leavesQuantity: string;
  price: string;
  status: string;
  /** Human-readable pre-trade lock prices, as exact decimal strings. */
  displayPrices: string[];
  /** Whether the order carries a persisted Ed25519-signed approval envelope. */
  signed: boolean;
}

/** An event on an order's lifecycle.
 * Reject fields are present on rejected events and account-blocking fill events.
 */
export interface OrderEvent {
  id: string;
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
  leavesQuantity?: string;
  orderStatus?: string;
  executionReport?: ExecutionReportRequestRecord;
  commission?: Commission;
  /** Whether this event carries a persisted Ed25519-signed attestation. */
  signed: boolean;
  /** The attestation's algorithm ("ed25519" | "none"); empty when unattested. */
  alg?: string;
}

/** A completed trade derived from a fill. */
export interface Trade {
  id: string;
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
  commission?: Commission;
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
  policy: string;
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
  policy: string;
  reason: string;
  details: string;
}

/** One per-asset balance effect of a fill, tagged with its asset so the base
 *  and quote legs of a spot fill can be told apart. All values are exact
 *  decimal strings passed through verbatim. */
export interface ExecutionOutcome {
  asset: string;
  balanceDelta: string;
  balanceResult: string;
  heldDelta: string;
  heldResult: string;
  incomingDelta: string;
  incomingResult: string;
  realizedPnlDelta: string;
  realizedPnlResult: string;
  realizedPnlHaltReason?: string;
  averageEntryPrice?: string;
}

/** Result of POST /orders/{id}/execution-reports: the recorded result,
 *  including any account blocks and per-asset outcomes, plus the attestation
 *  proving what Officer recorded. The attestation fields are empty when
 *  attestation was skipped or ran under eSign-off. */
export interface ExecutionReportResult {
  id: string;
  blocks: ExecutionBlock[];
  outcomes: ExecutionOutcome[];
  attestationToken: string;
  attestationKeyId: string;
  signed: boolean;
}

export interface ApprovalToken {
  token: string;
  keyId: string;
  id: string;
  verdict: "accept" | "reject" | "";
  reasons: CheckReject[];
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
    ordersActive: number;
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
  instanceId: string;
  externalSymbol: string;
  baseAsset: string;
  quoteAsset: string;
  /** Operator-set manual mark price (exact decimal string); empty when none.
   *  Applies only to bring-your-own (manual) instruments. */
  manualPrice: string;
  enabled: boolean;
  /** Whether the currently applied runtime emits the synthetic reverse pair. */
  syntheticInverse: boolean;
  stale: boolean;
  /** Elapsed milliseconds between the two most recent ticks of this quote, as
   *  observed by the running manager. Undefined until a second tick has arrived
   *  since the last (re)subscribe. A fixed measurement, not an age. */
  updateIntervalMs?: number;
  quote?: MarketDataQuote;
  inverseQuote?: MarketDataQuote;
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
  id: string;
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

/** Outcome of POST /market-data/instances/{id}/verify-symbol. */
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

/** Outcome of POST /market-data/instances/{id}/search-symbols. */
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

// --- Per-event attestation / reproduction ---

/** Signing algorithm used in the attestation envelope. */
export type ApprovalAlg = "ed25519" | "none";

/** The trading request an attestation binds. Every attested event carries
 *  exactly one of these. */
export type AttestationRequestType =
  | "submit"
  | "execution_report"
  | "confirm"
  | "cancel";

/** Identified public-key material (never private): the key resolved by a keyId,
 *  paired with its export format. Mirrors the reproduction bundle's publicKey and
 *  the GET /signing/keys/{keyId}/public body. */
export interface PublicKeyMaterial {
  /** UUID of the signing key this material was resolved by. */
  keyId: string;
  alg: ApprovalAlg;
  /** Export format of `key` (pem-pkcs8 | openssh | raw-base64). */
  format: SigningKeyFormat;
  /** The public key encoded in `format`. */
  key: string;
}

/** The persisted signed-attestation envelope metadata for one order event: the
 *  exact base64url token plus the envelope's own fields. Bound 1:1 to the event
 *  it attested. */
export interface EventAttestation {
  /** Base64url-encoded envelope JSON: { approval, signature, keyId, alg }. */
  token: string;
  /** UUID of the signing key, or empty string when alg is "none". */
  keyId: string;
  alg: ApprovalAlg;
  /** The trading request this attestation binds. */
  requestType: string;
  /** Wire submit mode carried by the attestation ("hold" | "immediate"). */
  mode: string;
  /** RFC3339Nano timestamp at which the envelope was issued. May be empty. */
  issuedAt: string;
  /** Whether the envelope carries a real cryptographic signature. */
  signed: boolean;
}

/** The exact POST /orders/submit response a robot / agent received (submit). */
export interface ApprovalTokenResponse {
  /** Base64url envelope token, verbatim. */
  token: string;
  /** Signing key UUID, or empty string under eSign-off. */
  keyId: string;
  /** The order's opaque public handle. */
  id: string;
  /** Signed pre-trade verdict. */
  verdict: "accept" | "reject" | "";
  /** Structured reject reasons; empty on accept. */
  reasons: CheckReject[];
}

/** One engine-recorded account block bound in the attestation payload result. */
export interface AttestationBlock {
  account: string;
  policy: string;
  code: string;
  reason: string;
  details: string;
}

/** The recorded result section bound in the attestation payload, present for
 *  request types carrying a result beyond the submit verdict. */
export interface AttestationResult {
  outcome: string;
  fillQuantity: string;
  fillPrice: string;
  fillLockPrice: string;
  commission?: Commission;
  leavesQuantity: string;
  orderStatus: string;
  blocks: AttestationBlock[];
}

/** The request bound in the attestation payload, reconstructed for reproduction
 *  from the decoded token: the request type, its material params, and the
 *  recorded result section when present. Carries no private material. */
export interface EventReproductionRequest {
  requestType: string;
  orderId: string;
  eventId: string;
  instrument: string;
  side: string;
  quantity: string;
  amountKind: string;
  orderType: string;
  limitPrice: string;
  priceCurrency: string;
  accountId: string;
  verdict: string;
  executionReport?: ExecutionReportRequestRecord;
  result: AttestationResult | null;
}

/** The execution-report facet of a reproduction response: the recorded result
 *  plus the attestation token the robot received verbatim. */
export interface ExecutionReportResponse {
  id: string;
  blocks: AttestationBlock[];
  outcomes: ExecutionOutcome[];
  attestationToken: string;
  attestationKeyId: string;
  signed: boolean;
}

/** The confirm / cancel facet of a reproduction response: the resolved order plus
 *  the attestation token the robot received verbatim. */
export interface OrderMutationResponse {
  order: Order;
  attestationToken: string;
  attestationKeyId: string;
  signed: boolean;
}

/** The exact type-specific API response the robot received for the attested
 *  request. Exactly one facet is populated, matching the request type. The token
 *  inside each facet is carried verbatim. */
export interface EventReproductionResponse {
  submitResponse: ApprovalTokenResponse | null;
  executionReport: ExecutionReportResponse | null;
  confirm: OrderMutationResponse | null;
  cancel: OrderMutationResponse | null;
}

/** The signing-mode facet of a reproduction bundle: the global eSign-off flag
 *  plus this event's own envelope alg and whether it carries a real signature. */
export interface EventReproductionESign {
  /** Envelope alg for this event ("ed25519" | "none"); empty when unattested. */
  alg: string;
  /** Whether the global eSign-off flag is set. */
  noESign: boolean;
  /** Whether this event carries a real Ed25519 signature. */
  signed: boolean;
}

/** The controller-facing reproduction bundle for one order-history event's
 *  attestation: byte-for-byte what a robot / AI agent received from the live APIs
 *  for the request that produced this event. Signed artifacts (token,
 *  canonicalApproval, signature, publicKey.key) are verbatim server output and
 *  must never be re-serialized client-side. */
export interface EventReproduction {
  /** The trading request the attestation binds; empty when unattested. */
  requestType: string;
  /** The event body, identical to GET /orders/{id}. */
  event: OrderEvent;
  /** Persisted attestation metadata (token verbatim), or null when unattested. */
  attestation: EventAttestation | null;
  /** The request bound in the attestation payload, or null when unattested. */
  request: EventReproductionRequest | null;
  /** The exact type-specific API response, or null when unattested. */
  response: EventReproductionResponse | null;
  /** The exact signed bytes (canonical JSON), or null when absent/undecodable. */
  canonicalApproval: string | null;
  /** Public key resolved rotation-safe by the attestation's keyId; null under
   *  "none". */
  publicKey: PublicKeyMaterial | null;
  /** Signing mode facet. */
  eSign: EventReproductionESign;
  /** Base64 envelope signature; empty under alg "none". */
  signature: string;
  /** Explains a null attestation; empty otherwise. */
  reason: string;
}

// --- Audit ---

/** One audit-log entry (mirrors the auditDTO wire shape). */
export interface AuditEntry {
  id: string;
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
