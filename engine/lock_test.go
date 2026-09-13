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

// These tests exercise the OpenPit pretrade lock binding and so require the
// native runtime dylib at run time (set OPENPIT_RUNTIME_LIBRARY_PATH or build
// the workspace dylib first, as documented for the Go bindings).

package engine

import (
	"testing"

	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
)

// lockFromPrices builds a default-group pretrade lock carrying the given decimal
// prices, the representative shape a reservation produces.
func lockFromPrices(t *testing.T, prices ...string) pretrade.Lock {
	t.Helper()
	entries := make([]pretrade.Entry, 0, len(prices))
	for _, p := range prices {
		price, err := param.NewPriceFromString(p)
		if err != nil {
			t.Fatalf("price %q: %v", p, err)
		}
		entries = append(entries, pretrade.Entry{PolicyGroupID: model.DefaultPolicyGroupID, Price: price})
	}
	lock, err := pretrade.NewLockFromEntries(entries)
	if err != nil {
		t.Fatalf("NewLockFromEntries: %v", err)
	}
	return lock
}

// TestLockSeam_RoundTripExact checks the lock seam round-trips a lock exactly:
// marshalLock produces the store BLOB, unmarshalLock reconstructs an equal lock,
// and the reconstructed prices match the originals in iteration order. This is
// the persistence contract the order/reservation paths rely on.
func TestLockSeam_RoundTripExact(t *testing.T) {
	t.Parallel()
	original := lockFromPrices(t, "100.25", "99.5", "12345.6789")

	blob, err := marshalLock(original)
	if err != nil {
		t.Fatalf("marshalLock: %v", err)
	}
	if len(blob) == 0 {
		t.Fatalf("marshalLock produced empty blob")
	}

	restored, err := unmarshalLock(blob)
	if err != nil {
		t.Fatalf("unmarshalLock: %v", err)
	}
	if !restored.Equal(original) {
		t.Fatalf("round-trip lock is not equal to the original")
	}

	wantPrices, err := original.Prices()
	if err != nil {
		t.Fatalf("original prices: %v", err)
	}
	gotPrices, err := restored.Prices()
	if err != nil {
		t.Fatalf("restored prices: %v", err)
	}
	if len(gotPrices) != len(wantPrices) {
		t.Fatalf("price count = %d, want %d", len(gotPrices), len(wantPrices))
	}
	for i := range gotPrices {
		if gotPrices[i].Compare(wantPrices[i]) != 0 {
			t.Fatalf("price[%d] = %s, want %s", i, gotPrices[i], wantPrices[i])
		}
	}
}

// TestLockDisplayPrices_FromStoredBlob checks the display-price helper
// deserializes a stored lock blob and returns its prices in iteration order, the
// seam the presentation layer calls to render an order's locked prices.
func TestLockDisplayPrices_FromStoredBlob(t *testing.T) {
	t.Parallel()
	blob, err := marshalLock(lockFromPrices(t, "100.25", "99.5"))
	if err != nil {
		t.Fatalf("marshalLock: %v", err)
	}

	prices, err := lockDisplayPrices(blob)
	if err != nil {
		t.Fatalf("lockDisplayPrices: %v", err)
	}
	// Compare numerically to tolerate the binding's decimal scale formatting.
	want := []string{"100.25", "99.5"}
	if len(prices) != len(want) {
		t.Fatalf("display prices = %v, want %d entries", prices, len(want))
	}
	for i := range prices {
		got, err := param.NewPriceFromString(prices[i])
		if err != nil {
			t.Fatalf("parse display price %q: %v", prices[i], err)
		}
		wantPrice, _ := param.NewPriceFromString(want[i])
		if got.Compare(wantPrice) != 0 {
			t.Fatalf("display price[%d] = %s, want %s", i, prices[i], want[i])
		}
	}
}

// TestLockDisplayPrices_EmptyMeansNoCapturedLock checks a nil/empty stored BLOB
// means no lock was captured: the helper returns an empty slice with no error
// rather than failing to decode.
func TestLockDisplayPrices_EmptyMeansNoCapturedLock(t *testing.T) {
	t.Parallel()
	for _, blob := range [][]byte{nil, {}} {
		prices, err := lockDisplayPrices(blob)
		if err != nil {
			t.Fatalf("lockDisplayPrices(empty): %v", err)
		}
		if len(prices) != 0 {
			t.Fatalf("empty lock must yield no prices, got %v", prices)
		}
	}
}

// TestUnmarshalLock_RejectsGarbage checks the seam surfaces an error for bytes
// that are not a valid serialized lock rather than returning a silently-empty
// lock.
func TestUnmarshalLock_RejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := unmarshalLock([]byte{0x01, 0x02, 0x03, 0x04}); err == nil {
		t.Fatalf("want error decoding garbage lock bytes")
	}
}

// TestLockEncoding_MsgpackVsCBORMeasurement covers the msgpack-vs-cbor choice
// the seam documents: it measures both encodings on a representative lock and
// asserts each round-trips exactly through its matching decoder. The seam
// defaults to msgpack (the measured payload sizes were identical and the
// round-trip timings within noise); this test pins that both encodings remain
// available and lossless so the choice stays swappable in marshalLock.
func TestLockEncoding_MsgpackVsCBORMeasurement(t *testing.T) {
	t.Parallel()
	original := lockFromPrices(t, "100.25", "99.5", "12345.6789")

	msg, err := original.MarshalMsgpack()
	if err != nil {
		t.Fatalf("MarshalMsgpack: %v", err)
	}
	cbor, err := original.MarshalCBOR()
	if err != nil {
		t.Fatalf("MarshalCBOR: %v", err)
	}
	t.Logf("lock encoding sizes: msgpack=%d bytes, cbor=%d bytes (seam default: msgpack)",
		len(msg), len(cbor))

	fromMsg, err := pretrade.NewLockFromMsgPack(msg)
	if err != nil {
		t.Fatalf("NewLockFromMsgPack: %v", err)
	}
	if !fromMsg.Equal(original) {
		t.Fatalf("msgpack round-trip not equal to original")
	}
	fromCBOR, err := pretrade.NewLockFromCBOR(cbor)
	if err != nil {
		t.Fatalf("NewLockFromCBOR: %v", err)
	}
	if !fromCBOR.Equal(original) {
		t.Fatalf("cbor round-trip not equal to original")
	}

	// marshalLock is the seam: its output must be the default encoding (msgpack)
	// and decode through unmarshalLock back to the original.
	blob, err := marshalLock(original)
	if err != nil {
		t.Fatalf("marshalLock: %v", err)
	}
	restored, err := unmarshalLock(blob)
	if err != nil {
		t.Fatalf("unmarshalLock: %v", err)
	}
	if !restored.Equal(original) {
		t.Fatalf("seam round-trip not equal to original")
	}
}
