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

package sqlite

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
)

const sealingExposureWarning = "previously stored secrets must be considered exposed"

func TestSecretColumnsSealedRoundTrip(t *testing.T) {
	ctx := context.Background()
	key := mustStoreMasterKey(t, 0x11)
	_, rs := newTestStore(t, WithMasterKey(key))

	privateKey := []byte("signing-private-plaintext")
	signingKey := domain.SigningKey{
		KeyID:      "sealed-key",
		Alg:        "ed25519",
		PrivateKey: privateKey,
		PublicKey:  []byte("public"),
		Active:     true,
	}
	if err := rs.UpsertSigningKey(ctx, signingKey); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}

	credentials := `{"token":"market-data-plaintext"}`
	instance, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider:    domain.MarketDataProviderBYO,
		Label:       "sealed-feed",
		Credentials: credentials,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	gotKey, err := rs.GetSigningKey(ctx, signingKey.KeyID)
	if err != nil {
		t.Fatalf("GetSigningKey: %v", err)
	}
	if !bytes.Equal(gotKey.PrivateKey, privateKey) {
		t.Fatal("GetSigningKey did not return the original private key")
	}
	activeKey, ok, err := rs.GetActiveSigningKey(ctx)
	if err != nil || !ok {
		t.Fatalf("GetActiveSigningKey: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(activeKey.PrivateKey, privateKey) {
		t.Fatal("GetActiveSigningKey did not return the original private key")
	}

	gotInstance, ok, err := rs.GetMarketDataInstance(ctx, instance.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetMarketDataInstance: ok=%v err=%v", ok, err)
	}
	if gotInstance.Credentials != credentials {
		t.Fatal("GetMarketDataInstance did not return the original credentials")
	}
	instances, err := rs.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	if len(instances) != 1 || instances[0].Credentials != credentials {
		t.Fatal("ListMarketDataInstances did not return the original credentials")
	}
	enabled, err := rs.ListEnabledMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListEnabledMarketDataInstances: %v", err)
	}
	if len(enabled) != 1 || enabled[0].Credentials != credentials {
		t.Fatal("ListEnabledMarketDataInstances did not return the original credentials")
	}

	db := rs.(*realmStore).rawDB()
	var storedPrivateKey, storedCredentials []byte
	if err := db.QueryRowContext(
		ctx, `SELECT private_key FROM signing_key WHERE key_id = ?`, signingKey.KeyID,
	).Scan(&storedPrivateKey); err != nil {
		t.Fatalf("read stored private_key: %v", err)
	}
	if err := db.QueryRowContext(
		ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
		instance.ExternalID.Bytes(),
	).Scan(&storedCredentials); err != nil {
		t.Fatalf("read stored credentials: %v", err)
	}
	if bytes.Equal(storedPrivateKey, privateKey) {
		t.Fatal("stored private_key equals plaintext")
	}
	if bytes.Equal(storedCredentials, []byte(credentials)) {
		t.Fatal("stored credentials equal plaintext")
	}

	listedKeys, err := rs.ListSigningKeys(ctx)
	if err != nil {
		t.Fatalf("ListSigningKeys: %v", err)
	}
	if len(listedKeys) != 1 || listedKeys[0].PrivateKey != nil {
		t.Fatal("ListSigningKeys returned private material with a sealer installed")
	}
}

func TestSecretColumnsPlaintextRoundTripWithoutSealer(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	privateKey := []byte("plaintext-signing-key")
	if err := rs.UpsertSigningKey(ctx, domain.SigningKey{
		KeyID:      "plaintext-key",
		Alg:        "ed25519",
		PrivateKey: privateKey,
		PublicKey:  []byte("public"),
	}); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}
	credentials := `{"token":"plaintext-credentials"}`
	instance, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "plaintext-feed", Credentials: credentials,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	db := rs.(*realmStore).rawDB()
	var storedPrivateKey, storedCredentials []byte
	if err := db.QueryRowContext(
		ctx, `SELECT private_key FROM signing_key WHERE key_id = ?`, "plaintext-key",
	).Scan(&storedPrivateKey); err != nil {
		t.Fatalf("read stored private_key: %v", err)
	}
	if err := db.QueryRowContext(
		ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
		instance.ExternalID.Bytes(),
	).Scan(&storedCredentials); err != nil {
		t.Fatalf("read stored credentials: %v", err)
	}
	if !bytes.Equal(storedPrivateKey, privateKey) {
		t.Fatal("stored private_key changed without a sealer")
	}
	if !bytes.Equal(storedCredentials, []byte(credentials)) {
		t.Fatal("stored credentials changed without a sealer")
	}

	gotKey, err := rs.GetSigningKey(ctx, "plaintext-key")
	if err != nil || !bytes.Equal(gotKey.PrivateKey, privateKey) {
		t.Fatalf("GetSigningKey without sealer: err=%v", err)
	}
	gotInstance, ok, err := rs.GetMarketDataInstance(ctx, instance.ExternalID)
	if err != nil || !ok || gotInstance.Credentials != credentials {
		t.Fatalf("GetMarketDataInstance without sealer: ok=%v err=%v", ok, err)
	}
	listedKeys, err := rs.ListSigningKeys(ctx)
	if err != nil {
		t.Fatalf("ListSigningKeys: %v", err)
	}
	if len(listedKeys) != 1 || listedKeys[0].PrivateKey != nil {
		t.Fatal("ListSigningKeys returned private material without a sealer")
	}
}

func TestSecretColumnRejectsDifferentMasterKey(t *testing.T) {
	ctx := context.Background()
	firstKey := mustStoreMasterKey(t, 0x21)
	store, rs := newTestStore(t, WithMasterKey(firstKey))
	privateKey := []byte("private-key-must-not-be-returned")
	if err := rs.UpsertSigningKey(ctx, domain.SigningKey{
		KeyID:      "wrong-key-test",
		Alg:        "ed25519",
		PrivateKey: privateKey,
		PublicKey:  []byte("public"),
	}); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}

	secondKey := mustStoreMasterKey(t, 0x22)
	store.(*sqliteStore).setSealer(&secondKey)
	got, err := rs.GetSigningKey(ctx, "wrong-key-test")
	if err == nil {
		t.Fatal("GetSigningKey with a different master key returned nil error")
	}
	if len(got.PrivateKey) != 0 {
		t.Fatal("GetSigningKey with a different master key returned private material")
	}
	if !strings.Contains(err.Error(), "signing_key.private_key") {
		t.Fatalf("GetSigningKey error does not name the secret column: %v", err)
	}
	if strings.Contains(err.Error(), string(privateKey)) {
		t.Fatal("GetSigningKey error contains plaintext")
	}
}

func TestCredentialsRejectRowMove(t *testing.T) {
	ctx := context.Background()
	key := mustStoreMasterKey(t, 0x31)
	_, rs := newTestStore(t, WithMasterKey(key))

	first, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "first-feed", Credentials: "first-secret",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance(first): %v", err)
	}
	second, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "second-feed", Credentials: "second-secret",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance(second): %v", err)
	}

	db := rs.(*realmStore).rawDB()
	var moved []byte
	if err := db.QueryRowContext(
		ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
		first.ExternalID.Bytes(),
	).Scan(&moved); err != nil {
		t.Fatalf("read source credentials: %v", err)
	}
	if _, err := db.ExecContext(
		ctx, `UPDATE market_data_instance SET credentials = ? WHERE external_id = ?`,
		moved, second.ExternalID.Bytes(),
	); err != nil {
		t.Fatalf("move credentials: %v", err)
	}

	got, ok, err := rs.GetMarketDataInstance(ctx, second.ExternalID)
	if err == nil {
		t.Fatal("GetMarketDataInstance after row move returned nil error")
	}
	if ok || got.Credentials != "" {
		t.Fatal("GetMarketDataInstance after row move returned credentials")
	}
	if !strings.Contains(err.Error(), "market_data_instance.credentials") {
		t.Fatalf("GetMarketDataInstance error does not name the secret column: %v", err)
	}
}

func TestSigningKeyRejectsRowMove(t *testing.T) {
	ctx := context.Background()
	key := mustStoreMasterKey(t, 0x32)
	_, rs := newTestStore(t, WithMasterKey(key))

	first := domain.SigningKey{
		KeyID: "first-signing-key", Alg: "ed25519",
		PrivateKey: []byte("first-private-key"), PublicKey: []byte("first-public-key"),
	}
	if err := rs.UpsertSigningKey(ctx, first); err != nil {
		t.Fatalf("UpsertSigningKey(first): %v", err)
	}
	second := domain.SigningKey{
		KeyID: "second-signing-key", Alg: "ed25519",
		PrivateKey: []byte("second-private-key"), PublicKey: []byte("second-public-key"),
	}
	if err := rs.UpsertSigningKey(ctx, second); err != nil {
		t.Fatalf("UpsertSigningKey(second): %v", err)
	}

	db := rs.(*realmStore).rawDB()
	var moved []byte
	if err := db.QueryRowContext(
		ctx, `SELECT private_key FROM signing_key WHERE key_id = ?`, first.KeyID,
	).Scan(&moved); err != nil {
		t.Fatalf("read source private_key: %v", err)
	}
	if _, err := db.ExecContext(
		ctx, `UPDATE signing_key SET private_key = ? WHERE key_id = ?`, moved, second.KeyID,
	); err != nil {
		t.Fatalf("move private_key: %v", err)
	}

	got, err := rs.GetSigningKey(ctx, second.KeyID)
	if err == nil {
		t.Fatal("GetSigningKey after row move returned nil error")
	}
	if len(got.PrivateKey) != 0 {
		t.Fatal("GetSigningKey after row move returned private material")
	}
	if !strings.Contains(err.Error(), "signing_key.private_key") {
		t.Fatalf("GetSigningKey error does not name the secret column: %v", err)
	}
}

func TestCredentialsRemainReadableAfterRawLabelChange(t *testing.T) {
	ctx := context.Background()
	key := mustStoreMasterKey(t, 0x42)
	_, rs := newTestStore(t, WithMasterKey(key))
	instance, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "old-label", Credentials: "label-independent-secret",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	db := rs.(*realmStore).rawDB()
	if _, err := db.ExecContext(
		ctx, `UPDATE market_data_instance SET label = ? WHERE external_id = ?`,
		"new-label", instance.ExternalID.Bytes(),
	); err != nil {
		t.Fatalf("change label directly: %v", err)
	}

	got, ok, err := rs.GetMarketDataInstance(ctx, instance.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetMarketDataInstance after raw label change: ok=%v err=%v", ok, err)
	}
	if got.Label != "new-label" || got.Credentials != "label-independent-secret" {
		t.Fatal("credentials did not remain readable after raw label change")
	}
}

func TestUpdateMarketDataInstanceSettingsResealsAfterLabelChange(t *testing.T) {
	ctx := context.Background()
	key := mustStoreMasterKey(t, 0x41)
	_, rs := newTestStore(t, WithMasterKey(key))
	instance, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "old-label", Credentials: "old-secret",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	db := rs.(*realmStore).rawDB()
	var before []byte
	if err := db.QueryRowContext(
		ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
		instance.ExternalID.Bytes(),
	).Scan(&before); err != nil {
		t.Fatalf("read credentials before update: %v", err)
	}

	if err := rs.UpdateMarketDataInstanceSettings(
		ctx, instance.ExternalID, "new-label", "new-secret",
	); err != nil {
		t.Fatalf("UpdateMarketDataInstanceSettings: %v", err)
	}
	var after []byte
	if err := db.QueryRowContext(
		ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
		instance.ExternalID.Bytes(),
	).Scan(&after); err != nil {
		t.Fatalf("read credentials after update: %v", err)
	}
	if bytes.Equal(after, []byte("new-secret")) || bytes.Equal(after, before) {
		t.Fatal("updated credentials were not re-sealed")
	}

	got, ok, err := rs.GetMarketDataInstance(ctx, instance.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetMarketDataInstance after update: ok=%v err=%v", ok, err)
	}
	if got.Label != "new-label" || got.Credentials != "new-secret" {
		t.Fatal("updated label and credentials did not round-trip")
	}
}

func TestMigrateWithoutVerifierOrMasterKeyLeavesStorePlaintext(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	store := openSQLiteStoreForTest(t, path)

	if store.configuredMasterKey != nil {
		t.Fatal("store recorded an unconfigured master key")
	}
	if store.sealer.Load() != nil {
		t.Fatal("store installed a sealer before migration")
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if store.sealer.Load() != nil {
		t.Fatal("migration installed a sealer without a master key")
	}

	var stateRows int
	if err := store.currentDB().QueryRowContext(
		ctx, `SELECT COUNT(*) FROM secret_state`,
	).Scan(&stateRows); err != nil {
		t.Fatalf("count secret_state rows: %v", err)
	}
	if stateRows != 0 {
		t.Fatalf("secret_state rows = %d, want 0", stateRows)
	}
}

func TestMigrateWithMasterKeySealsAllExistingSecrets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	plaintextStore := openSQLiteStoreForTest(t, path)
	if err := plaintextStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate plaintext store: %v", err)
	}
	rawRealm, err := plaintextStore.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm plaintext store: %v", err)
	}

	signingKeys := []domain.SigningKey{
		{
			KeyID: "migration-signing-key-one", Alg: "ed25519",
			PrivateKey: []byte("migration-private-secret-one"), PublicKey: []byte("public-one"),
		},
		{
			KeyID: "migration-signing-key-two", Alg: "ed25519",
			PrivateKey: []byte("migration-private-secret-two"), PublicKey: []byte("public-two"),
		},
	}
	for _, signingKey := range signingKeys {
		if err := rawRealm.UpsertSigningKey(ctx, signingKey); err != nil {
			t.Fatalf("UpsertSigningKey(%s): %v", signingKey.KeyID, err)
		}
	}
	verifyOnlyKey := domain.SigningKey{
		KeyID: "migration-verify-only-key", Alg: "ed25519", PublicKey: []byte("verify-public"),
	}
	if err := rawRealm.UpsertSigningKey(ctx, verifyOnlyKey); err != nil {
		t.Fatalf("UpsertSigningKey(%s): %v", verifyOnlyKey.KeyID, err)
	}

	credentialValues := []string{
		`{"token":"migration-credential-secret-one"}`,
		`{"token":"migration-credential-secret-two"}`,
	}
	instances := make([]domain.MarketDataInstance, 0, len(credentialValues))
	for i, credentials := range credentialValues {
		instance, err := rawRealm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
			Provider:    domain.MarketDataProviderBYO,
			Label:       fmt.Sprintf("migration-feed-%d", i+1),
			Credentials: credentials,
		})
		if err != nil {
			t.Fatalf("CreateMarketDataInstance(%d): %v", i+1, err)
		}
		instances = append(instances, instance)
	}
	if err := plaintextStore.Close(); err != nil {
		t.Fatalf("close plaintext store: %v", err)
	}

	key := mustStoreMasterKey(t, 0x51)
	sealedStore := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	if sealedStore.configuredMasterKey == nil {
		t.Fatal("WithMasterKey did not record the configured key")
	}
	if sealedStore.sealer.Load() != nil {
		t.Fatal("WithMasterKey installed the sealer before migration")
	}
	logOutput := captureSlogForTest(t)
	if err := sealedStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate sealed store: %v", err)
	}
	if !strings.Contains(logOutput.String(), sealingExposureWarning) {
		t.Fatal("sealing existing secrets did not log the exposure warning")
	}
	if sealedStore.sealer.Load() == nil {
		t.Fatal("migration did not install the sealer")
	}

	var storedVerifier []byte
	var sealedAt, vacuumedAt string
	if err := sealedStore.currentDB().QueryRowContext(
		ctx,
		`SELECT key_verifier, sealed_at, vacuumed_at FROM secret_state WHERE singleton = 1`,
	).Scan(&storedVerifier, &sealedAt, &vacuumedAt); err != nil {
		t.Fatalf("read secret_state: %v", err)
	}
	expectedVerifier, err := secret.KeyVerifier(key)
	if err != nil {
		t.Fatalf("KeyVerifier: %v", err)
	}
	match, err := secret.KeyVerifiersEqual(storedVerifier, expectedVerifier)
	if err != nil {
		t.Fatalf("KeyVerifiersEqual: %v", err)
	}
	if !match {
		t.Fatal("stored verifier does not match configured master key")
	}
	if _, err := time.Parse(time.RFC3339Nano, sealedAt); err != nil {
		t.Fatalf("sealed_at is not RFC3339Nano: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, vacuumedAt); err != nil {
		t.Fatalf("vacuumed_at is not RFC3339Nano: %v", err)
	}

	for _, signingKey := range signingKeys {
		var stored []byte
		if err := sealedStore.currentDB().QueryRowContext(
			ctx, `SELECT private_key FROM signing_key WHERE key_id = ?`, signingKey.KeyID,
		).Scan(&stored); err != nil {
			t.Fatalf("read stored signing key %s: %v", signingKey.KeyID, err)
		}
		if bytes.Equal(stored, signingKey.PrivateKey) {
			t.Fatalf("signing key %s remained plaintext", signingKey.KeyID)
		}
	}
	var storedVerifyOnly []byte
	if err := sealedStore.currentDB().QueryRowContext(
		ctx, `SELECT private_key FROM signing_key WHERE key_id = ?`, verifyOnlyKey.KeyID,
	).Scan(&storedVerifyOnly); err != nil {
		t.Fatalf("read verify-only signing key: %v", err)
	}
	if len(storedVerifyOnly) == 0 {
		t.Fatal("verify-only private key remained empty plaintext")
	}
	for i, instance := range instances {
		var stored []byte
		if err := sealedStore.currentDB().QueryRowContext(
			ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
			instance.ExternalID.Bytes(),
		).Scan(&stored); err != nil {
			t.Fatalf("read stored market data credentials %d: %v", i+1, err)
		}
		if bytes.Equal(stored, []byte(credentialValues[i])) {
			t.Fatalf("market data credentials %d remained plaintext", i+1)
		}
	}

	sealedRealm, err := sealedStore.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm sealed store: %v", err)
	}
	for _, signingKey := range signingKeys {
		got, err := sealedRealm.GetSigningKey(ctx, signingKey.KeyID)
		if err != nil {
			t.Fatalf("GetSigningKey(%s): %v", signingKey.KeyID, err)
		}
		if !bytes.Equal(got.PrivateKey, signingKey.PrivateKey) {
			t.Fatalf("GetSigningKey(%s) did not return original private key", signingKey.KeyID)
		}
	}
	gotVerifyOnly, err := sealedRealm.GetSigningKey(ctx, verifyOnlyKey.KeyID)
	if err != nil {
		t.Fatalf("GetSigningKey(%s): %v", verifyOnlyKey.KeyID, err)
	}
	if len(gotVerifyOnly.PrivateKey) != 0 {
		t.Fatalf("verify-only private key length = %d, want 0", len(gotVerifyOnly.PrivateKey))
	}
	for i, instance := range instances {
		got, ok, err := sealedRealm.GetMarketDataInstance(ctx, instance.ExternalID)
		if err != nil || !ok {
			t.Fatalf("GetMarketDataInstance(%d): ok=%v err=%v", i+1, ok, err)
		}
		if got.Credentials != credentialValues[i] {
			t.Fatalf("GetMarketDataInstance(%d) did not return original credentials", i+1)
		}
	}

	if err := sealedStore.Close(); err != nil {
		t.Fatalf("close sealed store: %v", err)
	}
	databaseBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sealed database: %v", err)
	}
	for _, signingKey := range signingKeys {
		if bytes.Contains(databaseBytes, signingKey.PrivateKey) {
			t.Fatalf("sealed database retains plaintext for signing key %s", signingKey.KeyID)
		}
	}
	for i, credentials := range credentialValues {
		if bytes.Contains(databaseBytes, []byte(credentials)) {
			t.Fatalf("sealed database retains plaintext credentials %d", i+1)
		}
	}
}

func TestMigrateFreshSealedRealmDoesNotWarnOfExposure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	key := mustStoreMasterKey(t, 0x58)
	logOutput := captureSlogForTest(t)
	store := openSQLiteStoreForTest(t, path, WithMasterKey(key))

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate fresh sealed store: %v", err)
	}
	if strings.Contains(logOutput.String(), sealingExposureWarning) {
		t.Fatal("fresh sealed store logged an exposure warning")
	}
}

func TestMigrateExistingEmptyRealmWarnsOfExposure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	plaintextStore := openSQLiteStoreForTest(t, path)
	if err := plaintextStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate plaintext store: %v", err)
	}
	if err := plaintextStore.Close(); err != nil {
		t.Fatalf("close plaintext store: %v", err)
	}

	logOutput := captureSlogForTest(t)
	sealedStore := openSQLiteStoreForTest(
		t, path, WithMasterKey(mustStoreMasterKey(t, 0x5a)),
	)
	if err := sealedStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate existing empty store: %v", err)
	}
	if !strings.Contains(logOutput.String(), sealingExposureWarning) {
		t.Fatal("sealing an existing empty store did not log the exposure warning")
	}
}

func TestMigrateWarnsBeforeRecordingVacuumCompletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	plaintextStore := openSQLiteStoreForTest(t, path)
	if err := plaintextStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate plaintext store: %v", err)
	}
	plaintextRealm, err := plaintextStore.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm plaintext store: %v", err)
	}
	if _, err := plaintextRealm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "exposure-probe",
		Credentials: `{"token":"exposure-probe"}`,
	}); err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if _, err := plaintextStore.currentDB().ExecContext(ctx, `
		CREATE TRIGGER reject_vacuum_completion
		BEFORE UPDATE OF vacuumed_at ON secret_state
		WHEN NEW.vacuumed_at IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'vacuum completion blocked');
		END
	`); err != nil {
		t.Fatalf("create vacuum completion trigger: %v", err)
	}
	if err := plaintextStore.Close(); err != nil {
		t.Fatalf("close plaintext store: %v", err)
	}

	logOutput := captureSlogForTest(t)
	sealedStore := openSQLiteStoreForTest(
		t, path, WithMasterKey(mustStoreMasterKey(t, 0x5b)),
	)
	err = sealedStore.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "record sqlite sealing vacuum completion") {
		t.Fatalf("Migrate error = %v, want vacuum completion failure", err)
	}
	if !strings.Contains(logOutput.String(), sealingExposureWarning) {
		t.Fatal("sealing exposure warning was not logged before vacuum completion failed")
	}
}

func TestMigrateExistingDatabaseThroughDSNWarnsOfExposure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	plaintextStore := openSQLiteStoreForTest(t, path)
	if err := plaintextStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate plaintext store: %v", err)
	}
	if err := plaintextStore.Close(); err != nil {
		t.Fatalf("close plaintext store: %v", err)
	}

	logOutput := captureSlogForTest(t)
	sealedStore := openSQLiteStoreForTest(
		t, path+"?_txlock=immediate",
		WithMasterKey(mustStoreMasterKey(t, 0x5c)),
	)
	if sealedStore.databaseCreated {
		t.Fatal("DSN path for existing database was treated as newly created")
	}
	if err := sealedStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate existing store through DSN: %v", err)
	}
	if !strings.Contains(logOutput.String(), sealingExposureWarning) {
		t.Fatal("sealing existing store through DSN did not log the exposure warning")
	}
}

func TestMigrateMatchingKeyWarnsBeforeRecordingVacuumCompletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	key := mustStoreMasterKey(t, 0x5d)
	sealFreshDatabaseForTest(t, path, key)

	setup := openSQLiteStoreForTest(t, path)
	if _, err := setup.currentDB().ExecContext(
		ctx, `UPDATE secret_state SET vacuumed_at = NULL WHERE singleton = 1`,
	); err != nil {
		t.Fatalf("clear vacuum completion marker: %v", err)
	}
	if _, err := setup.currentDB().ExecContext(ctx, `
		CREATE TRIGGER reject_resumed_vacuum_completion
		BEFORE UPDATE OF vacuumed_at ON secret_state
		WHEN NEW.vacuumed_at IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'resumed vacuum completion blocked');
		END
	`); err != nil {
		t.Fatalf("create resumed vacuum completion trigger: %v", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}

	logOutput := captureSlogForTest(t)
	reopened := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	err := reopened.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "record sqlite sealing vacuum completion") {
		t.Fatalf("Migrate error = %v, want vacuum completion failure", err)
	}
	if !strings.Contains(logOutput.String(), sealingExposureWarning) {
		t.Fatal("resumed scrub warning was not logged before vacuum completion failed")
	}
}

func TestMigrateMatchingKeyCompletesOwedVacuumBeforeInstallingSealer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	key := mustStoreMasterKey(t, 0x59)
	sealFreshDatabaseForTest(t, path, key)

	setup := openSQLiteStoreForTest(t, path)
	if _, err := setup.currentDB().ExecContext(
		ctx, `CREATE TABLE scrub_probe (value BLOB NOT NULL)`,
	); err != nil {
		t.Fatalf("create scrub probe: %v", err)
	}
	if _, err := setup.currentDB().ExecContext(
		ctx, `INSERT INTO scrub_probe (value) VALUES (zeroblob(262144))`,
	); err != nil {
		t.Fatalf("populate scrub probe: %v", err)
	}
	if _, err := setup.currentDB().ExecContext(ctx, `DROP TABLE scrub_probe`); err != nil {
		t.Fatalf("drop scrub probe: %v", err)
	}
	if _, err := setup.currentDB().ExecContext(
		ctx, `UPDATE secret_state SET vacuumed_at = NULL WHERE singleton = 1`,
	); err != nil {
		t.Fatalf("clear vacuum completion marker: %v", err)
	}
	var freelistBefore int
	if err := setup.currentDB().QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(
		&freelistBefore,
	); err != nil {
		t.Fatalf("read freelist before restart: %v", err)
	}
	if freelistBefore == 0 {
		t.Fatal("scrub probe did not leave pages on the freelist")
	}
	if err := setup.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}

	logOutput := captureSlogForTest(t)
	reopened := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	if reopened.sealer.Load() != nil {
		t.Fatal("matching key installed the sealer before migration")
	}
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatalf("Migrate with owed vacuum: %v", err)
	}
	if reopened.sealer.Load() == nil {
		t.Fatal("owed vacuum completion did not install the sealer")
	}
	if !strings.Contains(logOutput.String(), sealingExposureWarning) {
		t.Fatal("resumed scrub did not log the exposure warning")
	}

	var vacuumedAt string
	if err := reopened.currentDB().QueryRowContext(
		ctx, `SELECT vacuumed_at FROM secret_state WHERE singleton = 1`,
	).Scan(&vacuumedAt); err != nil {
		t.Fatalf("read vacuum completion marker: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, vacuumedAt); err != nil {
		t.Fatalf("vacuumed_at is not RFC3339Nano: %v", err)
	}
	var freelistAfter int
	if err := reopened.currentDB().QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(
		&freelistAfter,
	); err != nil {
		t.Fatalf("read freelist after restart: %v", err)
	}
	if freelistAfter != 0 {
		t.Fatalf("freelist after resumed scrub = %d, want 0", freelistAfter)
	}
	if _, err := reopened.ForRealm(ctx, domain.DefaultRealm); err != nil {
		t.Fatalf("ForRealm after resumed scrub: %v", err)
	}
}

func TestMigrateSealedRealmWithMatchingMasterKeyInstallsSealer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	key := mustStoreMasterKey(t, 0x52)
	sealFreshDatabaseForTest(t, path, key)

	reopened := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	if reopened.sealer.Load() != nil {
		t.Fatal("WithMasterKey installed the sealer before migration")
	}
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatalf("Migrate with matching key: %v", err)
	}
	if reopened.sealer.Load() == nil {
		t.Fatal("matching verifier did not install the sealer")
	}
}

func TestMigrateSealedRealmWithoutMasterKeyRefuses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	key := mustStoreMasterKey(t, 0x53)
	sealFreshDatabaseForTest(t, path, key)

	reopened := openSQLiteStoreForTest(t, path)
	err := reopened.Migrate(ctx)
	if err == nil {
		t.Fatal("Migrate without the required master key returned nil error")
	}
	if !strings.Contains(err.Error(), "no master key was supplied") {
		t.Fatalf("Migrate without key returned the wrong error: %v", err)
	}
	if reopened.sealer.Load() != nil {
		t.Fatal("failed migration installed a sealer")
	}
}

func TestMigrateSealedRealmWithDifferentMasterKeyRefuses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	firstKey := mustStoreMasterKey(t, 0x54)
	sealFreshDatabaseForTest(t, path, firstKey)

	withoutKey := openSQLiteStoreForTest(t, path)
	withoutKeyErr := withoutKey.Migrate(ctx)
	if withoutKeyErr == nil {
		t.Fatal("Migrate without the required master key returned nil error")
	}
	if err := withoutKey.Close(); err != nil {
		t.Fatalf("close store without key: %v", err)
	}

	secondKey := mustStoreMasterKey(t, 0x55)
	withWrongKey := openSQLiteStoreForTest(t, path, WithMasterKey(secondKey))
	wrongKeyErr := withWrongKey.Migrate(ctx)
	if wrongKeyErr == nil {
		t.Fatal("Migrate with a different master key returned nil error")
	}
	if !strings.Contains(wrongKeyErr.Error(), "does not match") {
		t.Fatalf("Migrate with different key returned the wrong error: %v", wrongKeyErr)
	}
	if wrongKeyErr.Error() == withoutKeyErr.Error() {
		t.Fatal("missing-key and wrong-key errors are not distinguishable")
	}
	if withWrongKey.sealer.Load() != nil {
		t.Fatal("wrong-key migration installed a sealer")
	}
}

func TestMigrateSealingFailureRollsBackEverySecretAndVerifier(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	plaintextStore := openSQLiteStoreForTest(t, path)
	if err := plaintextStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate plaintext store: %v", err)
	}
	rawRealm, err := plaintextStore.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm plaintext store: %v", err)
	}
	privateKey := []byte("rollback-private-secret")
	if err := rawRealm.UpsertSigningKey(ctx, domain.SigningKey{
		KeyID: "rollback-signing-key", Alg: "ed25519",
		PrivateKey: privateKey, PublicKey: []byte("public"),
	}); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}
	if err := rawRealm.UpsertSigningKey(ctx, domain.SigningKey{
		KeyID: "", Alg: "ed25519",
		PrivateKey: []byte("invalid-row-private-secret"), PublicKey: []byte("public"),
	}); err != nil {
		t.Fatalf("UpsertSigningKey with empty row id: %v", err)
	}
	if err := plaintextStore.Close(); err != nil {
		t.Fatalf("close plaintext store: %v", err)
	}

	key := mustStoreMasterKey(t, 0x56)
	sealingStore := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	err = sealingStore.Migrate(ctx)
	if err == nil {
		t.Fatal("Migrate with an invalid secret row returned nil error")
	}
	if !strings.Contains(err.Error(), "row ID is empty") {
		t.Fatalf("Migrate returned the wrong error: %v", err)
	}

	var storedPrivateKey []byte
	if err := sealingStore.currentDB().QueryRowContext(
		ctx, `SELECT private_key FROM signing_key WHERE key_id = ?`, "rollback-signing-key",
	).Scan(&storedPrivateKey); err != nil {
		t.Fatalf("read signing key after rollback: %v", err)
	}
	if !bytes.Equal(storedPrivateKey, privateKey) {
		t.Fatal("sealing failure did not roll back the signing key rewrite")
	}
	var stateRows int
	if err := sealingStore.currentDB().QueryRowContext(
		ctx, `SELECT COUNT(*) FROM secret_state`,
	).Scan(&stateRows); err != nil {
		t.Fatalf("count secret_state rows after rollback: %v", err)
	}
	if stateRows != 0 {
		t.Fatalf("secret_state rows after rollback = %d, want 0", stateRows)
	}
	if sealingStore.sealer.Load() != nil {
		t.Fatal("failed sealing migration installed a sealer")
	}
}

func TestResetWithMasterKeyRecreatesSealedDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	key := mustStoreMasterKey(t, 0x57)
	store := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := store.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if store.sealer.Load() == nil {
		t.Fatal("Reset left the sealer uninstalled")
	}

	var storedVerifier []byte
	if err := store.currentDB().QueryRowContext(
		ctx, `SELECT key_verifier FROM secret_state WHERE singleton = 1`,
	).Scan(&storedVerifier); err != nil {
		t.Fatalf("read secret_state after Reset: %v", err)
	}
	expectedVerifier, err := secret.KeyVerifier(key)
	if err != nil {
		t.Fatalf("KeyVerifier: %v", err)
	}
	match, err := secret.KeyVerifiersEqual(storedVerifier, expectedVerifier)
	if err != nil {
		t.Fatalf("KeyVerifiersEqual: %v", err)
	}
	if !match {
		t.Fatal("Reset stored the wrong key verifier")
	}
}

func openSQLiteStoreForTest(t *testing.T, path string, opts ...Option) *sqliteStore {
	t.Helper()
	raw, err := New(path, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store, ok := raw.(*sqliteStore)
	if !ok {
		t.Fatalf("New returned %T, want *sqliteStore", raw)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func captureSlogForTest(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func sealFreshDatabaseForTest(t *testing.T, path string, key secret.MasterKey) {
	t.Helper()
	store := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate sealed fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close sealed fixture: %v", err)
	}
}

func mustStoreMasterKey(t *testing.T, fill byte) secret.MasterKey {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	key, err := secret.ParseMasterKey(encoded)
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	return key
}
