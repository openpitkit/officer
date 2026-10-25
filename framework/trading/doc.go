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
// A venue plugs in as a Provider: a factory that builds a Connector for one
// connection and declares what the venue offers - venue accounts, symbol
// verification and search, and fees posted apart from executions. A Connector
// sends orders, looks them up and streams their events; optional interfaces
// add a pre-check, an account check, symbol search and verification, and fee
// reading. Venue connectors live outside this package.
//
// A destination is a connection, a venue account (empty where the venue has
// none), and connector-read route JSON. A connector resolves any commission
// currency to an Officer asset code and reports it as domain.Commission.
// PreCheck runs before Officer pre-trade so a venue-invalid draft never takes
// a reservation.
//
// A venue whose fees come with executions books them on the fill to the
// order's Officer account. A venue that posts fees apart from executions
// declares UnlinkedFees, and its FeeReader reports them; each goes through
// AdjustmentSink when the fee asset maps unambiguously. The adjustment is typed
// venue_fee, and its external id, trading:<connection>:fee:<fee id>, is the
// venue reference. A connection's fee account receives every unlinked fee
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
// Unlinked fees are never estimated or attached to execution reports.
// Corrections, cancellations and unresolved account or asset mappings require
// operator attention and are never guessed.
//
// Routing: an Officer account may have several destinations, including several
// connections of one provider with distinct venue identities. A send without a
// connection uses the account's only enabled destination in the send method's
// mode; several produce a routing error listing the candidates, and zero is
// also an error. A named connection must be granted to the Officer account or
// to its account group. An account has at most one effective grant per
// connection: its own grant, whole, before its group's, even when the own grant
// binds no venue account.
//
// The venue account comes from the effective grant: an order sends with the
// grant's bound venue account, or without one when the grant binds none. An
// order may name only that venue account; naming a different one is refused.
package trading
