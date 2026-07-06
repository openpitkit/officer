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

// Signing-group tests: key upsert/get/list round-trips addressed by the UUID
// key_id, byte-exact private/public BLOB round-trip, active switching and
// DeactivateAll, the listing's private-material redaction, and the
// signing_config enum key/value round-trip.

package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func TestSigningKeyUpsertGetList(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	priv := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	pub := []byte{0xaa, 0xbb, 0xcc}
	key := domain.SigningKey{
		CreatedAt:  time.Now().UTC().Truncate(time.Nanosecond),
		KeyID:      "key-uuid-1",
		Alg:        "ed25519",
		PublicKey:  pub,
		PrivateKey: priv,
		Active:     true,
	}
	if err := rs.UpsertSigningKey(ctx, key); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}

	// GetSigningKey returns the key with both BLOBs byte-exact.
	got, err := rs.GetSigningKey(ctx, "key-uuid-1")
	if err != nil {
		t.Fatalf("GetSigningKey: %v", err)
	}
	if !bytes.Equal(got.PrivateKey, priv) {
		t.Fatalf("private key round-trip = %v, want %v", got.PrivateKey, priv)
	}
	if !bytes.Equal(got.PublicKey, pub) {
		t.Fatalf("public key round-trip = %v, want %v", got.PublicKey, pub)
	}
	if got.KeyID != "key-uuid-1" || got.Alg != "ed25519" || !got.Active {
		t.Fatalf("GetSigningKey metadata = %+v", got)
	}
	if !got.CreatedAt.Equal(key.CreatedAt) {
		t.Fatalf("created_at round-trip = %v, want %v", got.CreatedAt, key.CreatedAt)
	}

	// GetActiveSigningKey returns the same active key.
	active, ok, err := rs.GetActiveSigningKey(ctx)
	if err != nil || !ok {
		t.Fatalf("GetActiveSigningKey: ok=%v err=%v", ok, err)
	}
	if active.KeyID != "key-uuid-1" || !bytes.Equal(active.PrivateKey, priv) {
		t.Fatalf("GetActiveSigningKey = %+v", active)
	}

	// ListSigningKeys surfaces the key but never the private material.
	list, err := rs.ListSigningKeys(ctx)
	if err != nil {
		t.Fatalf("ListSigningKeys: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListSigningKeys len = %d, want 1", len(list))
	}
	if list[0].PrivateKey != nil {
		t.Fatalf("ListSigningKeys leaked private key: %v", list[0].PrivateKey)
	}
	if !bytes.Equal(list[0].PublicKey, pub) {
		t.Fatalf("ListSigningKeys public key = %v, want %v", list[0].PublicKey, pub)
	}

	// Missing key is ErrNotFound.
	if _, err := rs.GetSigningKey(ctx, "ghost"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSigningKey(missing) error = %v, want ErrNotFound", err)
	}
}

func TestSigningKeyActiveSwitchingAndDeactivateAll(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	// First key is active.
	k1 := domain.SigningKey{
		KeyID: "k1", Alg: "ed25519", PublicKey: []byte{1}, PrivateKey: []byte{1}, Active: true,
	}
	if err := rs.UpsertSigningKey(ctx, k1); err != nil {
		t.Fatalf("UpsertSigningKey(k1): %v", err)
	}

	// Caller-driven rotation: deactivate all, then insert the new active key. This
	// mirrors the signing service's storeNewActive discipline.
	if err := rs.DeactivateAllSigningKeys(ctx); err != nil {
		t.Fatalf("DeactivateAllSigningKeys: %v", err)
	}
	k2 := domain.SigningKey{
		KeyID: "k2", Alg: "ed25519", PublicKey: []byte{2}, PrivateKey: []byte{2}, Active: true,
	}
	if err := rs.UpsertSigningKey(ctx, k2); err != nil {
		t.Fatalf("UpsertSigningKey(k2): %v", err)
	}

	// Exactly k2 is active now.
	active, ok, err := rs.GetActiveSigningKey(ctx)
	if err != nil || !ok {
		t.Fatalf("GetActiveSigningKey: ok=%v err=%v", ok, err)
	}
	if active.KeyID != "k2" {
		t.Fatalf("active key = %q, want k2", active.KeyID)
	}

	// k1 survives as an inactive key (connectors verify in-flight tokens).
	got1, err := rs.GetSigningKey(ctx, "k1")
	if err != nil {
		t.Fatalf("GetSigningKey(k1): %v", err)
	}
	if got1.Active {
		t.Fatal("k1 should be inactive after rotation")
	}

	// DeactivateAll leaves no active key.
	if err := rs.DeactivateAllSigningKeys(ctx); err != nil {
		t.Fatalf("DeactivateAllSigningKeys (second): %v", err)
	}
	if _, ok, err := rs.GetActiveSigningKey(ctx); err != nil || ok {
		t.Fatalf("GetActiveSigningKey after DeactivateAll: ok=%v err=%v, want ok=false", ok, err)
	}

	// An upsert by an existing key_id replaces in place (no duplicate row).
	k2.PrivateKey = []byte{9, 9}
	if err := rs.UpsertSigningKey(ctx, k2); err != nil {
		t.Fatalf("UpsertSigningKey(k2 replace): %v", err)
	}
	list, err := rs.ListSigningKeys(ctx)
	if err != nil {
		t.Fatalf("ListSigningKeys: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListSigningKeys len = %d, want 2 (replace, not insert)", len(list))
	}
	got2, _ := rs.GetSigningKey(ctx, "k2")
	if !bytes.Equal(got2.PrivateKey, []byte{9, 9}) {
		t.Fatalf("k2 private key after replace = %v, want [9 9]", got2.PrivateKey)
	}
}

func TestSigningConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	// The migration seeds no_esign=1, so fresh realms do not require signatures
	// until an operator configures a signing key and enables the mode.
	v, ok, err := rs.GetSigningConfig(ctx, "no_esign")
	if err != nil || !ok {
		t.Fatalf("GetSigningConfig(no_esign): ok=%v err=%v", ok, err)
	}
	if v != "1" {
		t.Fatalf("seeded no_esign = %q, want 1", v)
	}

	// Set then read back (upsert over the seeded row).
	if err := rs.SetSigningConfig(ctx, "no_esign", "0"); err != nil {
		t.Fatalf("SetSigningConfig: %v", err)
	}
	v, _, _ = rs.GetSigningConfig(ctx, "no_esign")
	if v != "0" {
		t.Fatalf("no_esign after set = %q, want 0", v)
	}

	// An absent enum key reports ok=false, not an error.
	if _, ok, err := rs.GetSigningConfig(ctx, "unset_key"); err != nil || ok {
		t.Fatalf("GetSigningConfig(unset): ok=%v err=%v, want ok=false", ok, err)
	}

	// A fresh enum key inserts.
	if err := rs.SetSigningConfig(ctx, "unset_key", "x"); err != nil {
		t.Fatalf("SetSigningConfig(new): %v", err)
	}
	v, ok, _ = rs.GetSigningConfig(ctx, "unset_key")
	if !ok || v != "x" {
		t.Fatalf("unset_key after set = %q ok=%v, want x true", v, ok)
	}
}
