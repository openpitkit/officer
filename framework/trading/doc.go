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

// Package trading sends pre-trade-held Officer orders through configured
// connections and feeds venue executions and outcomes into the core. Durable
// claims and send-attempt markers prevent resends; reconciliation recovers
// missed fills and unknown send outcomes after reconnect or restart. Each
// connection has one worker that writes its venue links and execution reports.
// Disabling a connection stops new sends only. After restart, a connection
// with any venue link keeps its worker for tracking and late fee intake, even
// when all its orders have finished.
//
// A destination is a connection, a venue account (empty where the venue has
// none), and connector-read route JSON. A connector resolves any commission
// currency to an Officer asset code and reports it as domain.Commission.
// PreCheck runs before Officer pre-trade so a venue-invalid draft never takes
// a reservation. The shared interface accommodates these planned venues;
// Alpaca is the only implementation, in its own package.
//
// Alpaca: one API key pair identifies one account; VenueAccount is empty. Paper
// mode selects the paper host and live mode the live host. Route JSON is
// {"timeInForce": ...}; symbols are names such as AAPL, verified through
// GET /v2/assets/{symbol}. Account check reads GET /v2/account for the account
// id (identity) and status. Symbol search requests GET /v2/assets twice, with
// status=active and asset_class=us_equity or asset_class=crypto, then searches
// their union by exact symbol, symbol prefix and name substring,
// case-insensitively, with each group sorted by symbol and at most 50 matches.
// Pre-check requires an active, tradable asset, fractionable assets and day TIF
// for fractional quantities, and valid limit price increments. Fills carry no
// commission and are identified by cumulative filled quantity. Reconciliation
// reads GET /v2/account/activities/FILL?order_id=. Crypto is allowed
// with gtc or ioc TIF, canonical BASE/QUOTE symbols and the venue's size and
// price increments.
// Alpaca posts FEE (USD equity regulatory fees) and CFEE (crypto fees, charged
// in the received asset) as
// separate account activities, typically at end of day. FeeReader takes them
// through AdjustmentSink with a deterministic activity id when the fee asset
// maps unambiguously. The adjustment is typed venue_fee, and its external id,
// trading:<connection>:fee:<fee id>, is the venue reference. A connection's
// fee account receives every unlinked fee
// whole, as posted, and may be any Officer account. Without a fee account, the
// single Officer account that can send through the connection, directly or
// through a granted group, receives fees. A group grant always needs the fee
// account, and granting a second distinct account is refused until it is set;
// clearing it is refused while a group grant exists or several accounts can
// send. Skipped fees are audited, including missing targets, unresolved
// assets, venue shapes needing attention and core rejections. Each fee id is
// successfully reported at most once per Runtime, including across Restart;
// a new process may report it again during replay. An audit sink error is
// logged and retried on a later pass.
// Force-deleting an Officer account removes its venue-fee adjustments, so its
// fees may be booked again to the connection's current fee account on the next
// restart; the operator is warned. A fee with no target is audited and skipped.
// Fees are read after reconnect reconciliation and hourly, starting at the
// connection's first venue-order link, in pages within a read time budget.
// An in-memory cursor consumes final outcomes and retries temporary failures;
// restart replays from the first link using deterministic adjustment ids.
// Fees are never estimated or attached to execution reports. Until a CFEE is
// posted and applied, the crypto position
// includes the not-yet-charged fee. Corrections, cancellations and unresolved
// account or asset mappings require operator attention and are never guessed.
// Venues whose fees come with executions (IB, Binance) book them on the fill to
// the order's Officer account, so the fee-account rule does not apply to them.
//
// Binance: one API key identifies one account or sub-account; VenueAccount is
// empty. Paper mode selects spot testnet. Route JSON is {"timeInForce": ...}.
// Exchange info verifies symbols; pre-check applies LOT_SIZE, PRICE_FILTER and
// NOTIONAL filters. Each trade has commission in commissionAsset (base, quote
// or BNB). BNB needs an asset-level mapping, not designed further here.
// Reconciliation reads myTrades by orderId; execution reports carry cumulative
// quantity. Account check would read signed GET /api/v3/account, using its uid
// as identity.
//
// Interactive Brokers: one TWS / IB Gateway login may contain multiple
// accounts, so the destination selects VenueAccount. Paper mode uses a paper
// login. Route JSON is {"exchange": ..., "tif": ...}. Instruments are contracts
// (conId), verified through contract details. Commission reports give
// per-execution commission in a currency; reconciliation reads executions by
// filter. Account check and a single connection identity are not designed
// further here.
//
// eToro: one API key identifies a user; paper mode selects the virtual (demo)
// portfolio and live mode the real one, with no VenueAccount. Instruments use
// instrument ids. Spread-based pricing has no separate commission;
// reconciliation reads trade history. Account check and its identity are not
// designed further here.
//
// Routing (design): an Officer account may have several destinations, including
// several connections of one provider with distinct venue identities. A send
// without a connection uses the account's only enabled destination in the send
// method's mode; several produce a routing error listing the candidates, and
// zero is also an error. A named connection must be granted to the Officer
// account or to its account group. An account has at most one effective grant
// per connection: its own grant, whole, before its group's, even when the own
// grant binds no venue account. This own-before-group precedence is the grant
// access rule; it is separate from the venue-account override model below.
//
// Venues with venue accounts, such as IB, need an explicit policy on the
// connection: no default venue account, or a default with one of three override
// modes: everyone may override; the Officer account may not override; the order
// may override (which implies the Officer account may too). An "only one" flag
// forbids overriding in both the order and the Officer account. The Officer
// account's binding to a connection may set a bound venue account and whether
// the order may override it. The exact permission matrix of these modes must
// be settled before venue-account routing is built.
//
// An order without a venue account takes the most specific configured default:
// the account binding before the connection default. With neither configured,
// it sends without a venue account. Where override is forbidden, naming the
// configured venue account is accepted; naming a different one is refused.
// Alpaca has no venue accounts, so this policy does not apply to it yet.
package trading
