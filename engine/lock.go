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

// The lock seam: the single place that (de)serializes a pretrade.Lock to and
// from the opaque BLOB the store persists, plus the helper that derives display
// prices from a stored lock. Every order/reservation lock-persistence path
// routes through here so the serialization choice lives in one spot.

package engine

import (
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
)

// marshalLock serializes a pretrade.Lock into the bytes the store persists in
// the order/reservation lock BLOB. It uses the SDK's versioned MessagePack
// encoding (Lock.MarshalMsgpack), never Lock.Bytes(): Lock.Bytes() is an
// in-process, build-specific layout that must not outlive the current library
// build, so it is unfit for durable storage. A measurement on a representative
// multi-entry lock showed msgpack and CBOR produce identical-size payloads with
// round-trip timings within noise of each other; with no compactness or speed
// advantage either way, the encoding follows the documented default (msgpack).
// To switch to CBOR, swap MarshalMsgpack for MarshalCBOR here and
// NewLockFromMsgPack for NewLockFromCBOR in unmarshalLock - the two functions
// are the only place the encoding is named.
func marshalLock(lock pretrade.Lock) ([]byte, error) {
	payload, err := lock.MarshalMsgpack()
	if err != nil {
		return nil, fmt.Errorf("engine: marshal lock: %w", err)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("engine: marshal lock returned an empty payload")
	}
	return payload, nil
}

// unmarshalLock reconstructs a pretrade.Lock from stored BLOB bytes via the
// matching SDK decoder (NewLockFromMsgPack). It is the inverse of marshalLock;
// the two must always name the same encoding. Empty/nil bytes are not a valid
// lock payload and yield an error, so callers must guard the no-lock case
// before reaching here.
func unmarshalLock(payload []byte) (pretrade.Lock, error) {
	lock, err := pretrade.NewLockFromMsgPack(payload)
	if err != nil {
		return pretrade.Lock{}, fmt.Errorf("engine: unmarshal lock: %w", err)
	}
	return lock, nil
}

// lockDisplayPrices deserializes a stored lock BLOB and returns its prices as
// exact decimal strings, in the lock's iteration order: default-group records
// first, then each non-default group in insertion order. An empty BLOB means
// no lock was captured, as on a rejected order, and yields an empty slice;
// settlementPrice owns the cardinality check.
func lockDisplayPrices(lock []byte) ([]string, error) {
	if len(lock) == 0 {
		return []string{}, nil
	}
	decoded, err := unmarshalLock(lock)
	if err != nil {
		return nil, err
	}
	prices, err := decoded.Prices()
	if err != nil {
		return nil, fmt.Errorf("engine: read lock prices: %w", err)
	}
	return pricesToStrings(prices), nil
}

// LockSettlementPrice restores the SDK lock and returns its single settlement
// price using the same rule as live reservations.
func LockSettlementPrice(lock []byte, order domain.Order) (string, error) {
	prices, err := lockDisplayPrices(lock)
	if err != nil {
		return "", err
	}
	return settlementPrice(prices, orderExternalIDForError(order.ExternalID)), nil
}

// pricesToStrings renders a binding price slice as exact decimal strings.
func pricesToStrings(prices []param.Price) []string {
	out := make([]string, 0, len(prices))
	for _, price := range prices {
		out = append(out, price.String())
	}
	return out
}

// serializePreTradeLock serializes a pre-trade output's lock into the BLOB
// bytes the store persists, via the lock seam (marshalLock).
func serializePreTradeLock(lock pretrade.Lock) ([]byte, error) {
	return marshalLock(lock)
}
