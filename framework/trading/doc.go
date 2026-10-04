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
// Alpaca: one API key pair identifies one account; VenueAccount is empty. Test
// mode selects the paper host and real mode the live host. Route JSON is
// {"timeInForce": ...}; symbols are names such as AAPL, verified through
// GET /v2/assets/{symbol}. Pre-check requires an active, tradable asset,
// fractionable assets and day TIF for fractional quantities, and valid limit
// price increments. Fills carry no commission and are identified by cumulative
// filled quantity. Reconciliation reads
// GET /v2/account/activities/FILL?order_id=. Crypto is allowed with gtc or ioc
// TIF, canonical BASE/QUOTE symbols and the venue's size and price increments.
// Alpaca posts FEE (USD equity regulatory fees) and CFEE (crypto fees, charged
// in the received asset) as
// separate account activities, typically at end of day. FeeReader takes them
// through AdjustmentSink with a deterministic activity id, only when the
// connection has one Officer account and the fee asset maps unambiguously.
// Fees are read after reconnect reconciliation and hourly, starting at the
// connection's first venue-order link, in pages within a read time budget.
// An in-memory cursor consumes final outcomes and retries temporary failures;
// restart replays from the first link using deterministic adjustment ids.
// Fees are never estimated or attached to execution reports. Until a CFEE is
// posted and applied, the crypto position
// includes the not-yet-charged fee. Corrections, cancellations and unresolved
// account or asset mappings require operator attention and are never guessed.
//
// Binance: one API key identifies one account or sub-account; VenueAccount is
// empty. Test mode selects spot testnet. Route JSON is {"timeInForce": ...}.
// Exchange info verifies symbols; pre-check applies LOT_SIZE, PRICE_FILTER and
// NOTIONAL filters. Each trade has commission in commissionAsset (base, quote
// or BNB). BNB needs an asset-level mapping, not designed further here.
// Reconciliation reads myTrades by orderId; execution reports carry cumulative
// quantity.
//
// Interactive Brokers: one TWS / IB Gateway login may contain multiple
// accounts, so the destination selects VenueAccount. Test mode uses a paper
// login. Route JSON is {"exchange": ..., "tif": ...}. Instruments are contracts
// (conId), verified through contract details. Commission reports give
// per-execution commission in a currency; reconciliation reads executions by
// filter.
//
// eToro: one API key identifies a user; mode selects a virtual (demo) or real
// portfolio, with no VenueAccount. Instruments use instrument ids. Spread-based
// pricing has no separate commission; reconciliation reads trade history.
package trading
