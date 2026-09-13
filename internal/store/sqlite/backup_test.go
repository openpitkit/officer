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

// Store-level backup/restore tests: the export -> restore round-trip on public
// identity, a realm moved isolated <-> shared in both directions, restore modes
// and selectors, dictionary-first foreign-key resolution, and engine-id
// reassignment. Engine replacement is classified by the node after restore.

package sqlite

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
	fwstore "go.openpit.dev/officer/framework/store"
)

// newRealmStore opens a fresh migrated store bound to realm and returns both the
// store and its realm handle.
func newRealmStore(t *testing.T, realm domain.RealmID) (fwstore.Store, fwstore.RealmStore) {
	return newRealmStoreWithOptions(t, realm)
}

func newRealmStoreWithOptions(
	t *testing.T, realm domain.RealmID, opts ...Option,
) (fwstore.Store, fwstore.RealmStore) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	s, err := New(path, realm, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rs, err := s.ForRealm(ctx, realm)
	if err != nil {
		t.Fatalf("ForRealm(%q): %v", realm, err)
	}
	return s, rs
}

// seedRealm fills rs with a representative realm: dictionaries, a balance, a
// limit, an order with an event/trade/adjustment, an audit row, market data and
// settings. It returns the created order's external id so callers can assert it
// is preserved across a round-trip.
func seedRealm(t *testing.T, ctx context.Context, rs fwstore.RealmStore) domain.ExternalID {
	t.Helper()
	if err := rs.CreateAssetClass(ctx, domain.AssetClass{Code: "equity", Title: "Equity"}); err != nil {
		t.Fatalf("CreateAssetClass: %v", err)
	}
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL", Title: "Apple", AssetClass: "equity"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "US Dollar"})
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator", Title: "Operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "grp-1", Title: "Desk"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Title: "Account 1", GroupCode: "grp-1", Notes: "primary",
		PnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.SetAccountBlock(ctx, domain.AccountBlock{
		Account: "acc-1",
		Policy:  "SpotFundsPolicy",
		Code:    domain.RejectCodePnlKillSwitchTriggered,
		Reason:  "lower bound breached: pnl -501 is below -500",
		Details: "account pnl -501 is below lower bound -500",
	}); err != nil {
		t.Fatalf("SetAccountBlock: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "1000", UpdatedAt: time.Now().UTC(),
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-1", Asset: "AAPL", MaxQuantity: "100",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
	if err := rs.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeAccount,
		Account:    "acc-1",
		Currency:   "USD",
		LowerBound: "-250",
		UpperBound: "500",
	}); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	order, err := rs.CreateOrder(ctx, domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Principal: "operator", Source: domain.SourcePanel, Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "10",
		Price: "150", Status: domain.OrderStatusFilled, DropCopy: true,
		ReservedQuantity: "0",
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourcePanel,
		Payload: domain.OrderEventPayload{FillQuantity: "10", FillPrice: "150"},
	}); err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if _, err := rs.CreateTrade(ctx, domain.Trade{
		Order: order.ExternalID, Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Source: domain.SourcePanel, Side: domain.OrderSideBuy, Quantity: "10", Price: "150",
		Commission: &domain.Commission{Amount: "-0.12", Currency: "USD"},
	}); err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	if _, err := rs.AppendAdjustment(ctx, domain.AccountAdjustmentRecord{
		Account: "acc-1", Asset: "USD", Source: domain.SourcePanel,
		Request:  domain.AdjustmentRequest{Asset: "USD"},
		Accepted: &domain.AdjustmentOutcomeAccepted{BalanceDelta: "1000", BalanceResult: "1000"},
	}); err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}
	if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
		Action: domain.AuditActionCreateAccount, Account: "acc-1",
		Actor: "operator", Source: domain.SourcePanel, Detail: "seed",
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	inst, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if err := rs.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance: inst.ExternalID, ExternalSymbol: "AAPLUSD",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	if err := rs.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	if err := rs.SetUserSetting(ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen, "1"); err != nil {
		t.Fatalf("SetUserSetting: %v", err)
	}
	return order.ExternalID
}

func snapshotBackupData(
	t *testing.T, ctx context.Context, rs fwstore.RealmStore,
) backup.Data {
	t.Helper()
	archive, err := rs.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	return archive.Data
}

func signingKeyForBackup(
	t *testing.T, keyID string, seedByte byte, active bool,
) backup.SigningKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = seedByte
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("Ed25519 private key returned a non-Ed25519 public key")
	}
	return backup.SigningKey{
		KeyID:     keyID,
		Alg:       fwsigning.AlgEd25519,
		PublicKey: append([]byte(nil), publicKey...),
		Active:    active,
	}
}

func TestBackupExportPreservesCredentialFormAndExcludesSigningPrivateKey(t *testing.T) {
	for _, test := range []struct {
		name   string
		sealed bool
	}{
		{name: "unsealed"},
		{name: "sealed", sealed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			var opts []Option
			if test.sealed {
				opts = append(opts, WithMasterKey(mustStoreMasterKey(t, 0x31)))
			}
			_, rs := newTestStore(t, opts...)

			seed := bytes.Repeat([]byte{0x47}, ed25519.SeedSize)
			privateKey := ed25519.NewKeyFromSeed(seed)
			publicKey := privateKey.Public().(ed25519.PublicKey)
			if err := rs.UpsertSigningKey(ctx, domain.SigningKey{
				KeyID:      "archive-key",
				Alg:        fwsigning.AlgEd25519,
				PrivateKey: seed,
				PublicKey:  publicKey,
				Active:     true,
			}); err != nil {
				t.Fatalf("UpsertSigningKey: %v", err)
			}
			credentials := []byte(`{"token":"archive-secret"}`)
			instance, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
				Provider:    domain.MarketDataProviderBYO,
				Label:       "archive-feed",
				Credentials: string(credentials),
				Enabled:     true,
			})
			if err != nil {
				t.Fatalf("CreateMarketDataInstance: %v", err)
			}

			archive, err := rs.ExportBackup(ctx, backup.Scope{All: true})
			if err != nil {
				t.Fatalf("ExportBackup: %v", err)
			}
			if len(archive.Data.MarketDataInstances) != 1 {
				t.Fatalf("market-data instances = %d, want 1", len(archive.Data.MarketDataInstances))
			}
			wantForm := backup.CredentialFormPlaintext
			if test.sealed {
				wantForm = backup.CredentialFormSealed
			}
			if archive.CredentialForm != wantForm {
				t.Fatalf("CredentialForm = %q, want %q", archive.CredentialForm, wantForm)
			}
			exported := archive.Data.MarketDataInstances[0]
			var storedCredentials []byte
			if err := rs.(*realmStore).rawDB().QueryRowContext(
				ctx,
				`SELECT credentials FROM market_data_instance WHERE external_id = ?`,
				instance.ExternalID.Bytes(),
			).Scan(&storedCredentials); err != nil {
				t.Fatalf("read stored credentials: %v", err)
			}
			if !bytes.Equal(exported.Credentials, storedCredentials) {
				t.Fatal("archive credentials differ from the database form")
			}
			if test.sealed && bytes.Equal(exported.Credentials, credentials) {
				t.Fatal("sealed archive contains plaintext credentials")
			}
			if !test.sealed && !bytes.Equal(exported.Credentials, credentials) {
				t.Fatal("unsealed archive does not contain plaintext credentials")
			}

			raw, err := json.Marshal(archive)
			if err != nil {
				t.Fatalf("marshal archive: %v", err)
			}
			if bytes.Contains(raw, []byte(`"privateKey"`)) ||
				bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(seed))) {
				t.Fatal("archive contains signing private key material")
			}
			if len(archive.Data.SigningKeys) != 1 ||
				!bytes.Equal(archive.Data.SigningKeys[0].PublicKey, publicKey) {
				t.Fatal("archive does not retain the signing public key")
			}
			var roundTrip backup.Archive
			if err := json.Unmarshal(raw, &roundTrip); err != nil {
				t.Fatalf("unmarshal archive: %v", err)
			}
			if !bytes.Equal(
				roundTrip.Data.MarketDataInstances[0].Credentials,
				exported.Credentials,
			) {
				t.Fatal("credential bytes changed across the archive JSON round-trip")
			}
		})
	}
}

func TestBackupSnapshotAccessorsRejectMovedSealedCredentials(t *testing.T) {
	ctx := context.Background()
	const firstCredentials = `{"token":"first"}`
	const secondCredentials = `{"token":"second"}`
	_, source := newRealmStoreWithOptions(
		t, domain.DefaultRealm, WithMasterKey(mustStoreMasterKey(t, 0x32)),
	)
	first, err := source.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "first",
		Credentials: firstCredentials,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance(first): %v", err)
	}
	second, err := source.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "second",
		Credentials: secondCredentials,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance(second): %v", err)
	}

	snapshot, cleanup, err := source.(*realmStore).backupSnapshot(ctx)
	if err != nil {
		t.Fatalf("backupSnapshot: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup backup snapshot: %v", err)
		}
	})
	snapshotSecond, found, err := snapshot.GetMarketDataInstance(ctx, second.ExternalID)
	if err != nil {
		t.Fatalf("read second credentials from snapshot: %v", err)
	}
	if !found {
		t.Fatal("second market-data instance missing from snapshot")
	}
	if snapshotSecond.Credentials != secondCredentials {
		t.Fatalf(
			"snapshot credentials = %q, want %q",
			snapshotSecond.Credentials,
			secondCredentials,
		)
	}

	var movedCredentials []byte
	if err := snapshot.rawDB().QueryRowContext(
		ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
		first.ExternalID.Bytes(),
	).Scan(&movedCredentials); err != nil {
		t.Fatalf("read first stored credentials: %v", err)
	}
	if _, err := snapshot.rawDB().ExecContext(
		ctx, `UPDATE market_data_instance SET credentials = ? WHERE external_id = ?`,
		movedCredentials, second.ExternalID.Bytes(),
	); err != nil {
		t.Fatalf("move sealed credentials in snapshot: %v", err)
	}

	if _, _, err := snapshot.GetMarketDataInstance(ctx, second.ExternalID); err == nil {
		t.Fatal("sealed backup snapshot accessor accepted ciphertext moved from another row")
	}
}

func TestBackupRestoreSealedCredentialsWithSameKey(t *testing.T) {
	ctx := context.Background()
	key := mustStoreMasterKey(t, 0x52)
	_, src := newRealmStoreWithOptions(
		t, domain.RealmID("source-realm"), WithMasterKey(key),
	)
	credentials := `{"token":"same-installation"}`
	instance, err := src.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider:    domain.MarketDataProviderBYO,
		Label:       "same-installation-feed",
		Credentials: credentials,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStoreWithOptions(
		t, domain.RealmID("target-realm"), WithMasterKey(key),
	)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if len(summary.MarketDataCredentialsUnavailable) != 0 {
		t.Fatalf("unavailable credentials = %v, want none", summary.MarketDataCredentialsUnavailable)
	}
	got, ok, err := dst.GetMarketDataInstance(ctx, instance.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetMarketDataInstance: ok=%v err=%v", ok, err)
	}
	if got.Credentials != credentials || !got.Enabled {
		t.Fatalf("restored instance = %+v, want enabled with readable credentials", got)
	}
	var storedCredentials []byte
	if err := dst.(*realmStore).rawDB().QueryRowContext(
		ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
		instance.ExternalID.Bytes(),
	).Scan(&storedCredentials); err != nil {
		t.Fatalf("read restored credentials: %v", err)
	}
	if bytes.Equal(storedCredentials, archive.Data.MarketDataInstances[0].Credentials) {
		t.Fatal("restore copied sealed credentials instead of opening and sealing them")
	}
}

func TestBackupRestoreUnavailableSealedCredentialsDisablesNamedInstance(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	_, src := newTestStore(t, WithMasterKey(mustStoreMasterKey(t, 0x63)))
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "AAPL"})
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "USD"})
	instance, err := src.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider:    domain.MarketDataProviderBYO,
		Label:       "transfer-feed",
		Credentials: `{"token":"source-only"}`,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if err := src.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "AAPL-USD",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	for _, test := range []struct {
		name string
		opts []Option
	}{
		{name: "no-key"},
		{name: "different-key", opts: []Option{
			WithMasterKey(mustStoreMasterKey(t, 0x64)),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, dst := newTestStore(t, test.opts...)
			summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
			})
			if err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			if !reflect.DeepEqual(
				summary.MarketDataCredentialsUnavailable,
				[]string{"transfer-feed"},
			) {
				t.Fatalf("unavailable credentials = %v, want transfer-feed",
					summary.MarketDataCredentialsUnavailable)
			}
			got, ok, err := dst.GetMarketDataInstance(ctx, instance.ExternalID)
			if err != nil || !ok {
				t.Fatalf("GetMarketDataInstance: ok=%v err=%v", ok, err)
			}
			if got.Enabled || got.Credentials != "" {
				t.Fatalf("restored unavailable instance = %+v, want disabled and empty", got)
			}
			instruments, err := dst.ListMarketDataInstruments(ctx, instance.ExternalID)
			if err != nil {
				t.Fatalf("ListMarketDataInstruments: %v", err)
			}
			if len(instruments) != 1 || instruments[0].ExternalSymbol != "AAPL-USD" {
				t.Fatalf("other archive rows did not restore: %+v", instruments)
			}
		})
	}
	if !strings.Contains(logs.String(), instance.ExternalID.String()) ||
		!strings.Contains(logs.String(), "open archived market-data credentials") {
		t.Fatalf("credential open failure log = %q, want instance id and wrapped error", logs.String())
	}
	if strings.Contains(logs.String(), "source-only") {
		t.Fatal("credential open failure log contains plaintext credentials")
	}
}

func TestBackupRestorePlaintextCredentialsIntoSealedDatabase(t *testing.T) {
	ctx := context.Background()
	_, src := newTestStore(t)
	credentials := `{"token":"plaintext-transfer"}`
	instance, err := src.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider:    domain.MarketDataProviderBYO,
		Label:       "plaintext-feed",
		Credentials: credentials,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.CredentialForm != backup.CredentialFormPlaintext {
		t.Fatalf("unsealed source credential form = %q, want plaintext", archive.CredentialForm)
	}

	_, dst := newTestStore(t, WithMasterKey(mustStoreMasterKey(t, 0x75)))
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, ok, err := dst.GetMarketDataInstance(ctx, instance.ExternalID)
	if err != nil || !ok || got.Credentials != credentials || !got.Enabled {
		t.Fatalf("restored plaintext credentials: instance=%+v ok=%v err=%v", got, ok, err)
	}
	var storedCredentials []byte
	if err := dst.(*realmStore).rawDB().QueryRowContext(
		ctx, `SELECT credentials FROM market_data_instance WHERE external_id = ?`,
		instance.ExternalID.Bytes(),
	).Scan(&storedCredentials); err != nil {
		t.Fatalf("read stored credentials: %v", err)
	}
	if bytes.Equal(storedCredentials, []byte(credentials)) {
		t.Fatal("plaintext archive credentials were not sealed in the target")
	}
}

func TestBackupRestoreProviderValidationMatchesCredentialForm(t *testing.T) {
	const credentials = `{"token":"registry-reject"}`
	for _, test := range []struct {
		name   string
		sealed bool
	}{
		{name: "plaintext"},
		{name: "sealed", sealed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			key := mustStoreMasterKey(t, 0x76)
			var sourceOpts []Option
			var targetOpts []Option
			if test.sealed {
				sourceOpts = append(sourceOpts, WithMasterKey(key))
				targetOpts = append(targetOpts, WithMasterKey(key))
			}
			_, src := newRealmStoreWithOptions(
				t, domain.RealmID("validation-source"), sourceOpts...,
			)
			instance, err := src.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
				Provider: domain.MarketDataProviderBYO, Label: "validation-feed",
				Credentials: credentials, Enabled: true,
			})
			if err != nil {
				t.Fatalf("CreateMarketDataInstance: %v", err)
			}
			archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
			if err != nil {
				t.Fatalf("ExportBackup: %v", err)
			}
			_, dst := newRealmStoreWithOptions(
				t, domain.RealmID("validation-target"), targetOpts...,
			)
			validated := false
			_, err = dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
				ValidateMarketDataInstance: func(got domain.MarketDataInstance) error {
					validated = true
					if got.Credentials != credentials {
						t.Fatalf("validated credentials = %q, want opened value", got.Credentials)
					}
					return domain.ErrInvalid
				},
			})
			if !validated || !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup validated=%v error=%v, want registry rejection", validated, err)
			}
			if _, ok, getErr := dst.GetMarketDataInstance(ctx, instance.ExternalID); getErr != nil || ok {
				t.Fatalf("rejected instance committed: ok=%v err=%v", ok, getErr)
			}
		})
	}
}

func TestBackupRestoreRejectsMissingOrUnknownCredentialForm(t *testing.T) {
	for _, form := range []string{"", "future"} {
		t.Run(form, func(t *testing.T) {
			ctx := context.Background()
			_, dst := newTestStore(t)
			archive := backup.NewArchive(
				time.Now(), "test", backup.RealmLabel{Code: "source"},
				backup.Scope{All: true}, backup.Data{}, form,
			)
			if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
			}); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup credential form %q = %v, want ErrInvalid", form, err)
			}
		})
	}
}

func TestBackupRestoreSigningKeyIsVerifyOnlyAndInactive(t *testing.T) {
	ctx := context.Background()
	_, src := newTestStore(t)
	seed := bytes.Repeat([]byte{0x29}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if err := src.UpsertSigningKey(ctx, domain.SigningKey{
		KeyID:      "verify-only-key",
		Alg:        fwsigning.AlgEd25519,
		PrivateKey: seed,
		PublicKey:  publicKey,
		Active:     true,
	}); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if len(archive.Data.SigningKeys) != 1 || !archive.Data.SigningKeys[0].Active {
		t.Fatalf("exported signing key metadata = %+v", archive.Data.SigningKeys)
	}

	_, dst := newTestStore(t, WithMasterKey(mustStoreMasterKey(t, 0x2a)))
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, err := dst.GetSigningKey(ctx, "verify-only-key")
	if err != nil {
		t.Fatalf("GetSigningKey: %v", err)
	}
	if got.Active || len(got.PrivateKey) != 0 || !bytes.Equal(got.PublicKey, publicKey) {
		t.Fatalf("restored signing key = %+v, want inactive public-only key", got)
	}
	if _, ok, err := dst.GetActiveSigningKey(ctx); err != nil || ok {
		t.Fatalf("GetActiveSigningKey after restore: ok=%v err=%v", ok, err)
	}
	var storedPrivateKey []byte
	var active bool
	if err := dst.(*realmStore).rawDB().QueryRowContext(
		ctx, `SELECT private_key, active FROM signing_key WHERE key_id = ?`,
		"verify-only-key",
	).Scan(&storedPrivateKey, &active); err != nil {
		t.Fatalf("read restored signing key: %v", err)
	}
	if len(storedPrivateKey) == 0 || active {
		t.Fatalf("stored signing key private length=%d active=%v, want sealed empty and inactive",
			len(storedPrivateKey), active)
	}
}

func TestBackupRestoreSelfPreservesActiveSigningKey(t *testing.T) {
	for _, mode := range []backup.RestoreMode{
		backup.RestoreModeOverwrite,
		backup.RestoreModeReplaceAll,
	} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t, WithMasterKey(mustStoreMasterKey(t, 0x2b)))
			seed := bytes.Repeat([]byte{0x2c}, ed25519.SeedSize)
			privateKey := ed25519.NewKeyFromSeed(seed)
			publicKey := privateKey.Public().(ed25519.PublicKey)
			if err := rs.UpsertSigningKey(ctx, domain.SigningKey{
				KeyID: "self-key", Alg: fwsigning.AlgEd25519,
				PrivateKey: seed, PublicKey: publicKey, Active: true,
			}); err != nil {
				t.Fatalf("UpsertSigningKey: %v", err)
			}
			archive, err := rs.ExportBackup(ctx, backup.Scope{
				Sections: []backup.Section{backup.SectionGeneralSettings},
			})
			if err != nil {
				t.Fatalf("ExportBackup: %v", err)
			}
			var before []byte
			if err := rs.(*realmStore).rawDB().QueryRowContext(
				ctx, `SELECT private_key FROM signing_key WHERE key_id = ?`, "self-key",
			).Scan(&before); err != nil {
				t.Fatalf("read private key before restore: %v", err)
			}
			if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: backup.Scope{All: true}, Mode: mode,
			}); err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			var after []byte
			var active bool
			if err := rs.(*realmStore).rawDB().QueryRowContext(
				ctx, `SELECT private_key, active FROM signing_key WHERE key_id = ?`, "self-key",
			).Scan(&after, &active); err != nil {
				t.Fatalf("read private key after restore: %v", err)
			}
			if !bytes.Equal(after, before) || !active {
				t.Fatalf("self restore changed active private material: equal=%v active=%v",
					bytes.Equal(after, before), active)
			}
			got, err := rs.GetSigningKey(ctx, "self-key")
			if err != nil || !bytes.Equal(got.PrivateKey, seed) {
				t.Fatalf("GetSigningKey after restore = %+v err=%v", got, err)
			}
		})
	}
}

func TestBackupRestoreKeepsTargetActiveWhenArchiveAddsActiveKey(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	target := signingKeyForBackup(t, "existing-active", 0x31, true)
	seed := bytes.Repeat([]byte{0x31}, ed25519.SeedSize)
	if err := rs.UpsertSigningKey(ctx, domain.SigningKey{
		CreatedAt: target.CreatedAt, KeyID: target.KeyID, Alg: target.Alg,
		PrivateKey: seed, PublicKey: target.PublicKey, Active: true,
	}); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}
	archive := backup.NewArchive(
		time.Now(), "test", backup.RealmLabel{Code: "source"},
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{SigningKeys: []backup.SigningKey{
			signingKeyForBackup(t, "new-active", 0x32, true),
		}},
		backup.CredentialFormPlaintext,
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeInsertMissing,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	active, ok, err := rs.GetActiveSigningKey(ctx)
	if err != nil || !ok || active.KeyID != target.KeyID ||
		!bytes.Equal(active.PrivateKey, seed) {
		t.Fatalf("active key after restore = %+v ok=%v err=%v", active, ok, err)
	}
	added, err := rs.GetSigningKey(ctx, "new-active")
	if err != nil || added.Active || len(added.PrivateKey) != 0 {
		t.Fatalf("archived active key restored as %+v err=%v, want verify-only inactive", added, err)
	}
}

func TestBackupRestoreRejectsSigningKeyIdentityConflict(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	target := signingKeyForBackup(t, "shared-id", 0x41, true)
	seed := bytes.Repeat([]byte{0x41}, ed25519.SeedSize)
	if err := rs.UpsertSigningKey(ctx, domain.SigningKey{
		KeyID: target.KeyID, Alg: target.Alg, PrivateKey: seed,
		PublicKey: target.PublicKey, Active: true,
	}); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}
	conflict := signingKeyForBackup(t, target.KeyID, 0x42, false)
	archive := backup.NewArchive(
		time.Now(), "test", backup.RealmLabel{Code: "source"},
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{SigningKeys: []backup.SigningKey{conflict}},
		backup.CredentialFormPlaintext,
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup = %v, want identity conflict", err)
	}
	got, err := rs.GetSigningKey(ctx, target.KeyID)
	if err != nil || !got.Active || !bytes.Equal(got.PrivateKey, seed) ||
		!bytes.Equal(got.PublicKey, target.PublicKey) {
		t.Fatalf("target key changed after rejected conflict: %+v err=%v", got, err)
	}
}

func mustCreateAsset(t *testing.T, ctx context.Context, rs fwstore.RealmStore, asset domain.Asset) {
	t.Helper()
	if _, err := rs.CreateAsset(ctx, asset); err != nil {
		t.Fatalf("CreateAsset(%s): %v", asset.Code, err)
	}
}

func seedGroupCurrencies(t *testing.T, ctx context.Context, rs fwstore.RealmStore) {
	t.Helper()
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "US Dollar"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "EUR", Title: "Euro"})
	if err := rs.SetGroupCurrency(ctx, "", "USD"); err != nil {
		t.Fatalf("SetGroupCurrency(default): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Title:    "Desk A",
		Currency: "EUR",
	}); err != nil {
		t.Fatalf("CreateGroup desk-a: %v", err)
	}
}

func TestBackupExportIncludesGroupCurrencies(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedGroupCurrencies(t, ctx, src)

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.Data.DefaultGroupCurrency != "USD" {
		t.Fatalf("DefaultGroupCurrency = %q, want USD",
			archive.Data.DefaultGroupCurrency)
	}
	if len(archive.Data.Groups) != 1 {
		t.Fatalf("groups = %v, want one real group", archive.Data.Groups)
	}
	if archive.Data.Groups[0].Code != "desk-a" ||
		archive.Data.Groups[0].Currency != "EUR" {
		t.Fatalf("exported group = %+v, want desk-a/EUR", archive.Data.Groups[0])
	}
	for _, group := range archive.Data.Groups {
		if group.Code == "" {
			t.Fatalf("archive contains default group row: %+v", group)
		}
	}
}

func TestBackupExportIncludesAccountCurrency(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "JPY"})
	if _, err := src.CreateAccount(ctx, domain.Account{
		Code: "acc-jpy", Currency: "JPY", Pnl: "123.45",
	}); err != nil {
		t.Fatalf("CreateAccount acc-jpy: %v", err)
	}

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if len(archive.Data.Accounts) != 1 {
		t.Fatalf("accounts = %v, want one account", archive.Data.Accounts)
	}
	if archive.Data.Accounts[0].Code != "acc-jpy" ||
		archive.Data.Accounts[0].Currency != "JPY" ||
		archive.Data.Accounts[0].Pnl != "123.45" {
		t.Fatalf("exported account = %+v, want acc-jpy/JPY/123.45",
			archive.Data.Accounts[0])
	}
}

func TestBackupArchiveRoundTripCarriesNoRuntimeIdentifiers(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup source: %v", err)
	}
	assertBackupDataHasNoRuntimeIdentifierType(t, reflect.TypeOf(archive.Data), "Data")
	assertBackupDataHasNoRuntimeIdentifierValue(t, reflect.ValueOf(archive.Data), "Data")

	raw, err := json.Marshal(archive)
	if err != nil {
		t.Fatalf("marshal archive: %v", err)
	}
	var transported backup.Archive
	if err := json.Unmarshal(raw, &transported); err != nil {
		t.Fatalf("unmarshal archive: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, transported, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup target: %v", err)
	}
	restored, err := dst.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup target: %v", err)
	}
	assertBackupDataHasNoRuntimeIdentifierValue(t, reflect.ValueOf(restored.Data), "Data")
}

func assertBackupDataHasNoRuntimeIdentifierType(
	t *testing.T, valueType reflect.Type, path string,
) {
	t.Helper()
	if runtimeIDType(valueType) {
		t.Fatalf("backup archive carries runtime identifier type at %s: %s", path, valueType)
	}
	switch valueType.Kind() {
	case reflect.Array, reflect.Pointer, reflect.Slice:
		assertBackupDataHasNoRuntimeIdentifierType(t, valueType.Elem(), path)
	case reflect.Map:
		assertBackupDataHasNoRuntimeIdentifierType(t, valueType.Key(), path+" key")
		assertBackupDataHasNoRuntimeIdentifierType(t, valueType.Elem(), path)
	case reflect.Struct:
		for index := 0; index < valueType.NumField(); index++ {
			field := valueType.Field(index)
			if field.IsExported() {
				assertBackupDataHasNoRuntimeIdentifierType(
					t, field.Type, path+"."+field.Name,
				)
			}
		}
	}
}

func assertBackupDataHasNoRuntimeIdentifierValue(
	t *testing.T, value reflect.Value, path string,
) {
	t.Helper()
	if runtimeIDType(value.Type()) {
		if !value.IsZero() {
			t.Fatalf("backup archive carries runtime identifier at %s: %v", path, value)
		}
		return
	}
	switch value.Kind() {
	case reflect.Array, reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			assertBackupDataHasNoRuntimeIdentifierValue(
				t, value.Index(index), path,
			)
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			assertBackupDataHasNoRuntimeIdentifierValue(t, key, path+" key")
			assertBackupDataHasNoRuntimeIdentifierValue(t, value.MapIndex(key), path)
		}
	case reflect.Pointer:
		if !value.IsNil() {
			assertBackupDataHasNoRuntimeIdentifierValue(t, value.Elem(), path)
		}
	case reflect.Struct:
		valueType := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := valueType.Field(index)
			if field.IsExported() {
				assertBackupDataHasNoRuntimeIdentifierValue(
					t, value.Field(index), path+"."+field.Name,
				)
			}
		}
	}
}

func runtimeIDType(valueType reflect.Type) bool {
	domainPackage := reflect.TypeOf(domain.EngineAssetID(0)).PkgPath()
	return valueType.PkgPath() == domainPackage &&
		strings.HasPrefix(valueType.Name(), "Engine") &&
		strings.HasSuffix(valueType.Name(), "ID")
}

func TestBackupExportUsesOneSQLiteSnapshotAcrossSettlementWrites(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	if err := src.SetAccountPnl(ctx, "acc-1", "10", ""); err != nil {
		t.Fatalf("SetAccountPnl(before): %v", err)
	}
	realm, ok := src.(*realmStore)
	if !ok {
		t.Fatalf("realm store type = %T, want *realmStore", src)
	}

	archive, err := realm.exportBackup(ctx, backup.Scope{All: true}, func() error {
		if err := src.SetAccountPnl(ctx, "acc-1", "20", ""); err != nil {
			return err
		}
		if err := src.UpsertBalance(ctx, domain.Balance{
			Account: "acc-1", Asset: "USD", Available: "900",
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
		order, err := src.CreateOrder(ctx, domain.Order{
			Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
			Principal: "operator", Source: domain.SourcePanel,
			Side: domain.OrderSideSell, AmountKind: domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "151", Status: domain.OrderStatusFilled,
		})
		if err != nil {
			return err
		}
		_, err = src.CreateTrade(ctx, domain.Trade{
			Order: order.ExternalID, Account: "acc-1",
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Principal: "operator", Source: domain.SourcePanel,
			Side: domain.OrderSideSell, Quantity: "1", Price: "151",
		})
		return err
	})
	if err != nil {
		t.Fatalf("exportBackup: %v", err)
	}
	if len(archive.Data.Accounts) != 1 || archive.Data.Accounts[0].Pnl != "10" {
		t.Fatalf("snapshot accounts = %+v, want pre-write P&L 10", archive.Data.Accounts)
	}
	var usd *backup.Balance
	for i := range archive.Data.Balances {
		if archive.Data.Balances[i].Account == "acc-1" &&
			archive.Data.Balances[i].Asset == "USD" {
			usd = &archive.Data.Balances[i]
			break
		}
	}
	if usd == nil || usd.Available != "1000" {
		t.Fatalf("snapshot USD balance = %+v, want pre-write 1000", usd)
	}
	if len(archive.Data.Trades) != 1 {
		t.Fatalf("snapshot trades = %d, want only pre-write trade", len(archive.Data.Trades))
	}

	account, ok, err := src.GetAccount(ctx, "acc-1")
	if err != nil || !ok || account.Pnl != "20" {
		t.Fatalf("live account = %+v ok=%v err=%v, want post-write P&L 20", account, ok, err)
	}
	liveUSD, ok, err := src.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok || liveUSD.Available != "900" {
		t.Fatalf("live USD balance = %+v ok=%v err=%v, want post-write 900", liveUSD, ok, err)
	}
	trades, err := src.ListAllTrades(ctx, "acc-1", "")
	if err != nil || len(trades) != 2 {
		t.Fatalf("live trades = %d err=%v, want two", len(trades), err)
	}
}

func TestBackupRestorePreservesGroupCurrencies(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedGroupCurrencies(t, ctx, src)

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatal("RestartRequired = true at store boundary, want node classification")
	}
	group, ok, err := dst.GetGroup(ctx, "desk-a")
	if err != nil || !ok {
		t.Fatalf("GetGroup desk-a: ok=%v err=%v", ok, err)
	}
	if group.Currency != "EUR" {
		t.Fatalf("restored group currency = %q, want EUR", group.Currency)
	}
	defaultGroup, ok, err := dst.GetGroup(ctx, "")
	if err != nil || !ok {
		t.Fatalf("GetGroup default: ok=%v err=%v", ok, err)
	}
	if defaultGroup.Currency != "USD" {
		t.Fatalf("restored default currency = %q, want USD",
			defaultGroup.Currency)
	}
	if defaultGroup.EngineGroupID != 0 {
		t.Fatalf("restored default engine id = %d, want reserved 0",
			defaultGroup.EngineGroupID)
	}
	groups, err := dst.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	for _, group := range groups {
		if group.Code == "" {
			t.Fatalf("ListGroups returned default row: %+v", group)
		}
	}
}

func TestBackupRestorePreservesAccountCurrency(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "JPY"})
	if _, err := src.CreateAccount(ctx, domain.Account{
		Code: "acc-jpy", Currency: "JPY", Pnl: "123.45",
	}); err != nil {
		t.Fatalf("CreateAccount acc-jpy: %v", err)
	}

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatal("RestartRequired = true at store boundary, want node classification")
	}
	account, ok, err := dst.GetAccount(ctx, "acc-jpy")
	if err != nil || !ok {
		t.Fatalf("GetAccount acc-jpy: ok=%v err=%v", ok, err)
	}
	if account.Currency != "JPY" ||
		account.EffectiveCurrency != "JPY" ||
		account.CurrencyOrigin != domain.CurrencyOriginAccount ||
		account.Pnl != "123.45" {
		t.Fatalf("restored account currency = %+v", account)
	}
}

func TestBackupRestorePreservesEmptyHaltedAccountPnl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	if _, err := src.CreateAccount(ctx, domain.Account{Code: "halted"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := src.SetAccountPnl(
		ctx, "halted", "", domain.PnlHaltReasonMissingFx,
	); err != nil {
		t.Fatalf("SetAccountPnl: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	account, ok, err := dst.GetAccount(ctx, "halted")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "" || account.PnlHaltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf(
			"restored account pnl = %q halt = %q, want empty missing_fx",
			account.Pnl,
			account.PnlHaltReason,
		)
	}
}

func TestBackupRestoreOverwriteClearsDefaultGroupCurrency(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "EUR", Title: "Euro"})
	if _, err := src.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Title:    "Desk A",
		Currency: "EUR",
	}); err != nil {
		t.Fatalf("CreateGroup source desk-a: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.Data.DefaultGroupCurrency != "" {
		t.Fatalf("DefaultGroupCurrency = %q, want empty",
			archive.Data.DefaultGroupCurrency)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	seedGroupCurrencies(t, ctx, dst)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup overwrite: %v", err)
	}
	if summary.RestartRequired {
		t.Fatal("RestartRequired = true at store boundary, want node classification")
	}
	defaultGroup, ok, err := dst.GetGroup(ctx, "")
	if err != nil || !ok {
		t.Fatalf("GetGroup default: ok=%v err=%v", ok, err)
	}
	if defaultGroup.Currency != "" {
		t.Fatalf("restored default currency = %q, want empty",
			defaultGroup.Currency)
	}
}

func TestBackupRestoreEmptyDefaultGroupCurrencyDoesNotCreateDefaultRow(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, src, domain.Asset{Code: "EUR", Title: "Euro"})
	if _, err := src.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Title:    "Desk A",
		Currency: "EUR",
	}); err != nil {
		t.Fatalf("CreateGroup source desk-a: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, ok, err := dst.GetGroup(ctx, ""); err != nil || ok {
		t.Fatalf("GetGroup default before restore: ok=%v err=%v", ok, err)
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup overwrite: %v", err)
	}
	if _, ok, err := dst.GetGroup(ctx, ""); err != nil || ok {
		t.Fatalf("GetGroup default after restore: ok=%v err=%v", ok, err)
	}
}

// TestBackupRoundTripIntoIsolatedRealm exports a seeded realm and restores it
// into a fresh isolated single-realm database, asserting public identity is
// preserved across every section and engine ids are freshly, validly assigned.
func TestBackupRoundTripIntoIsolatedRealm(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	if err := src.AppendAudit(ctx, fwstore.AuditEntry{
		Action: domain.AuditActionSubmitOrder, Account: "acc-1",
		OrderID: orderXID.String(), Verdict: "reject", RejectCode: "order_qty_exceeds_limit",
		Source: domain.SourceAPI,
	}); err != nil {
		t.Fatalf("AppendAudit(decision): %v", err)
	}

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	assertNoIDLeak(t, archive)

	_, dst := newRealmStore(t, domain.DefaultRealm)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatal("RestartRequired = true at store boundary, want node classification")
	}

	assertRealmsEqualOnPublicIdentity(t, ctx, src, dst, orderXID)
	restoredAudit, err := dst.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, row := range restoredAudit {
		if row.OrderID == orderXID.String() {
			found = true
			if row.Verdict != "reject" || row.RejectCode != "order_qty_exceeds_limit" {
				t.Fatalf("restored decision = %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("restored audit lost the structured order decision")
	}

	// Engine ids in the destination are valid and in range (freshly assigned).
	acc, ok, err := dst.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("dst GetAccount: ok=%v err=%v", ok, err)
	}
	if err := domain.ValidateEngineAccountID(acc.EngineAccountID); err != nil {
		t.Fatalf("restored engine account id invalid: %v", err)
	}
	grp, _, _ := dst.GetGroup(ctx, "grp-1")
	if err := domain.ValidateEngineGroupID(grp.EngineGroupID); err != nil {
		t.Fatalf("restored engine group id invalid: %v", err)
	}
}

func TestBackupRoundTripPreservesExecutionReportIdentityAndEventLinks(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	order, err := src.CreateOrder(ctx, domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Principal: "operator", Source: domain.SourceAPI, Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "2",
		Price: "151", Status: domain.OrderStatusSubmitted,
		ReservedQuantity: "0",
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	reportID := domain.ExternalID("backup-report-1")
	if _, err := src.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID:    &reportID,
		Order:       order.ExternalID,
		Account:     order.Account,
		OrderStatus: domain.OrderStatusPartiallyFilled,
		Events: []domain.OrderEvent{
			{Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI},
			{Order: order.ExternalID, Type: domain.OrderEventCommitted, Source: domain.SourceAPI},
		},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}

	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if len(archive.Data.ExecutionReports) != 1 {
		t.Fatalf("execution reports = %+v, want one", archive.Data.ExecutionReports)
	}
	if got := archive.Data.ExecutionReports[0]; got.ExternalID != reportID || got.Order != order.ExternalID {
		t.Fatalf("execution report = %+v, want id %q order %q", got, reportID, order.ExternalID)
	}
	if len(archive.Data.ExecutionReportEvents) != 2 {
		t.Fatalf("execution report links = %+v, want two", archive.Data.ExecutionReportEvents)
	}
	for _, link := range archive.Data.ExecutionReportEvents {
		if link.Report != reportID || link.Event.IsZero() {
			t.Fatalf("execution report link = %+v", link)
		}
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	restored, err := dst.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("restored ExportBackup: %v", err)
	}
	if len(restored.Data.ExecutionReports) != 1 ||
		restored.Data.ExecutionReports[0].ExternalID != reportID ||
		restored.Data.ExecutionReports[0].Order != order.ExternalID {
		t.Fatalf("restored execution reports = %+v", restored.Data.ExecutionReports)
	}
	if len(restored.Data.ExecutionReportEvents) != 2 {
		t.Fatalf("restored execution report links = %+v", restored.Data.ExecutionReportEvents)
	}
	wantEvents := map[domain.ExternalID]bool{}
	for _, link := range archive.Data.ExecutionReportEvents {
		wantEvents[link.Event] = true
	}
	for _, link := range restored.Data.ExecutionReportEvents {
		if link.Report != reportID || !wantEvents[link.Event] {
			t.Fatalf("restored execution report link = %+v", link)
		}
	}

	extraReportID := domain.ExternalID("target-only-report")
	if _, err := dst.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID:    &extraReportID,
		Order:       order.ExternalID,
		Account:     order.Account,
		OrderStatus: domain.OrderStatusCommitted,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventCommitted, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement target-only report: %v", err)
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("second RestoreBackup: %v", err)
	}
	pruned, err := dst.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("pruned ExportBackup: %v", err)
	}
	if len(pruned.Data.ExecutionReports) != 1 ||
		pruned.Data.ExecutionReports[0].ExternalID != reportID {
		t.Fatalf("reports after replace_all = %+v", pruned.Data.ExecutionReports)
	}
	if len(pruned.Data.ExecutionReportEvents) != 2 {
		t.Fatalf("report links after replace_all = %+v", pruned.Data.ExecutionReportEvents)
	}
}

func TestBackupRestoreOverwritePreservesTargetOnlyExecutionReportLinks(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderID := seedRealm(t, ctx, src)
	order, err := src.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatalf("GetOrder source: %v", err)
	}
	reportID := domain.ExternalID("overwrite-report")
	if _, err := src.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID:    &reportID,
		Order:       orderID,
		Account:     order.Order.Account,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: orderID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement source: %v", err)
	}
	archiveEvent, err := src.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: orderID, Type: domain.OrderEventCommitted, Source: domain.SourceAPI,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent source: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup source: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup initial: %v", err)
	}
	targetEvent, err := dst.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: orderID, Type: domain.OrderEventCommitted, Source: domain.SourceAPI,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent target: %v", err)
	}
	r := dst.(*realmStore)
	db, err := r.db()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	orderRowID, err := lookupOrderID(ctx, db, orderID)
	if err != nil {
		t.Fatalf("lookupOrderID: %v", err)
	}
	var reportRowID int64
	if err := db.QueryRowContext(
		ctx,
		`SELECT id FROM execution_report WHERE external_id = ?`,
		reportID.Bytes(),
	).Scan(&reportRowID); err != nil {
		t.Fatalf("resolve archived report: %v", err)
	}
	targetEventRowID, err := lookupOrderEventID(ctx, db, targetEvent.ExternalID)
	if err != nil {
		t.Fatalf("lookup target event: %v", err)
	}
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO execution_report_event (report_id, event_id) VALUES (?, ?)`,
		reportRowID,
		targetEventRowID,
	); err != nil {
		t.Fatalf("link target-only event: %v", err)
	}

	targetReportID := domain.ExternalID("target-only-report")
	result, err := db.ExecContext(
		ctx,
		`INSERT INTO execution_report (external_id, order_id, at) VALUES (?, ?, ?)`,
		targetReportID.Bytes(),
		orderRowID,
		nowStr(),
	)
	if err != nil {
		t.Fatalf("insert target-only report: %v", err)
	}
	targetReportRowID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("target-only report row id: %v", err)
	}
	archiveEventRowID, err := lookupOrderEventID(ctx, db, archiveEvent.ExternalID)
	if err != nil {
		t.Fatalf("lookup archived event: %v", err)
	}
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO execution_report_event (report_id, event_id) VALUES (?, ?)`,
		targetReportRowID,
		archiveEventRowID,
	); err != nil {
		t.Fatalf("link target-only report: %v", err)
	}

	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup overwrite: %v", err)
	}
	restored, err := dst.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup restored: %v", err)
	}
	links := make(map[string]bool, len(restored.Data.ExecutionReportEvents))
	for _, link := range restored.Data.ExecutionReportEvents {
		links[link.Report.String()+"\x00"+link.Event.String()] = true
	}
	if !links[reportID.String()+"\x00"+targetEvent.ExternalID.String()] {
		t.Fatal("overwrite dropped target-only event link from archived report")
	}
	if !links[targetReportID.String()+"\x00"+archiveEvent.ExternalID.String()] {
		t.Fatal("overwrite dropped target-only report link from archived event")
	}
}

func TestBackupRestoreRejectsCrossOrderExecutionReportLink(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	firstOrderID := seedRealm(t, ctx, src)
	first, err := src.GetOrder(ctx, firstOrderID)
	if err != nil {
		t.Fatalf("GetOrder first: %v", err)
	}
	second, err := src.CreateOrder(ctx, domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Principal: "operator", Source: domain.SourceAPI, Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
		Price: "150", Status: domain.OrderStatusSubmitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder second: %v", err)
	}
	firstReportID := domain.ExternalID("cross-order-report-a")
	if _, err := src.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID: &firstReportID, Order: firstOrderID, Account: first.Order.Account,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: firstOrderID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement first: %v", err)
	}
	secondReportID := domain.ExternalID("cross-order-report-b")
	if _, err := src.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID: &secondReportID, Order: second.ExternalID, Account: second.Account,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: second.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement second: %v", err)
	}
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	var secondEventID domain.ExternalID
	for _, event := range archive.Data.OrderEvents {
		if event.Order == second.ExternalID && event.Type == domain.OrderEventFill {
			secondEventID = event.ExternalID
		}
	}
	if secondEventID.IsZero() {
		t.Fatal("second order fill event missing from archive")
	}
	archive.Data.ExecutionReportEvents = []backup.ExecutionReportEventLink{{
		Report: firstReportID,
		Event:  secondEventID,
	}}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	_, err = dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
	}
	if _, err := dst.GetOrder(ctx, firstOrderID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed restore persisted first order: %v", err)
	}
}

func TestBackupRestoreOverwriteReconcilesReparentedReportLinks(t *testing.T) {
	ctx := context.Background()
	reportID := domain.ExternalID("reparented-report")

	_, dst := newRealmStore(t, domain.DefaultRealm)
	targetOrderID := seedRealm(t, ctx, dst)
	target, err := dst.GetOrder(ctx, targetOrderID)
	if err != nil {
		t.Fatalf("GetOrder target: %v", err)
	}
	if _, err := dst.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID: &reportID, Order: targetOrderID, Account: target.Order.Account,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: targetOrderID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement target: %v", err)
	}

	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	sourceOrder, err := src.CreateOrder(ctx, domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Principal: "operator", Source: domain.SourceAPI, Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
		Price: "151", Status: domain.OrderStatusSubmitted,
		ReservedQuantity: "0",
	})
	if err != nil {
		t.Fatalf("CreateOrder source: %v", err)
	}
	if _, err := src.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID: &reportID, Order: sourceOrder.ExternalID, Account: sourceOrder.Account,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: sourceOrder.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement source: %v", err)
	}
	activityScope := backup.Scope{
		Sections: []backup.Section{backup.SectionActivityHistory},
	}
	archive, err := src.ExportBackup(ctx, activityScope)
	if err != nil {
		t.Fatalf("ExportBackup source: %v", err)
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: activityScope, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup overwrite: %v", err)
	}

	restored, err := dst.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup restored: %v", err)
	}
	eventOrders := make(map[domain.ExternalID]domain.ExternalID)
	for _, event := range restored.Data.OrderEvents {
		eventOrders[event.ExternalID] = event.Order
	}
	var restoredReport backup.ExecutionReportRecord
	for _, report := range restored.Data.ExecutionReports {
		if report.ExternalID == reportID {
			restoredReport = report
		}
	}
	if restoredReport.Order != sourceOrder.ExternalID {
		t.Fatalf("restored report order = %q, want %q",
			restoredReport.Order, sourceOrder.ExternalID)
	}
	var linkCount int
	for _, link := range restored.Data.ExecutionReportEvents {
		if link.Report != reportID {
			continue
		}
		linkCount++
		if eventOrders[link.Event] != sourceOrder.ExternalID {
			t.Fatalf("restored report kept cross-order event link: %+v", link)
		}
	}
	if linkCount != 1 {
		t.Fatalf("restored report links = %d, want one source-order event", linkCount)
	}
}

func TestRestoreExecutionReportEventMissingReportIsNotFound(t *testing.T) {
	ctx := context.Background()
	_, realm := newRealmStore(t, domain.DefaultRealm)
	r := realm.(*realmStore)
	db, err := r.db()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	summary := backup.NewSummary()
	rt := restoreTx{
		tx:      tx,
		mode:    backup.RestoreModeOverwrite,
		summary: &summary,
	}
	err = rt.restoreExecutionReportEvent(ctx, backup.ExecutionReportEventLink{
		Report: domain.ExternalID("missing-report"),
		Event:  domain.ExternalID("missing-event"),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("restoreExecutionReportEvent = %v, want ErrNotFound", err)
	}
}

// TestBackupMoveIsolatedToShared restores a realm's archive into a SHARED
// database that already holds a different realm's rows, asserting no code
// collision and that the destination engine ids do not collide with the rows
// already present (they continue the destination's own sequence).
func TestBackupMoveIsolatedToShared(t *testing.T) {
	ctx := context.Background()

	// Source isolated realm.
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// Destination: a single-realm connector standing in for one realm of a shared
	// DB. It already holds prior rows with their own codes and engine ids; the
	// restore must not collide with them.
	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.CreateGroup(ctx, domain.AccountGroup{Code: "existing-grp"}); err != nil {
		t.Fatalf("dst CreateGroup: %v", err)
	}
	existingAcc, err := dst.CreateAccount(ctx, domain.Account{Code: "existing-acc"})
	if err != nil {
		t.Fatalf("dst CreateAccount: %v", err)
	}

	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup into shared: %v", err)
	}

	// Both the pre-existing and the restored account coexist with their codes.
	if _, ok, _ := dst.GetAccount(ctx, "existing-acc"); !ok {
		t.Fatal("pre-existing account lost after restore")
	}
	restored, ok, err := dst.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("restored account missing: ok=%v err=%v", ok, err)
	}
	// The restored account got a fresh engine id, distinct from the pre-existing
	// one (no engine-id leakage from the source).
	if restored.EngineAccountID == existingAcc.EngineAccountID {
		t.Fatalf("restored engine account id collided with pre-existing: %d",
			restored.EngineAccountID)
	}
	if err := domain.ValidateEngineAccountID(restored.EngineAccountID); err != nil {
		t.Fatalf("restored engine account id invalid: %v", err)
	}

	// The order's external id is preserved exactly.
	if _, err := dst.GetOrder(ctx, orderXID); err != nil {
		t.Fatalf("restored order by preserved external id: %v", err)
	}
}

// TestBackupMoveSharedToIsolated moves the SAME realm back out of the shared DB
// into a fresh isolated DB, completing the isolated <-> shared round-trip, and
// asserts the public identity survived both hops unchanged.
func TestBackupMoveSharedToIsolated(t *testing.T) {
	ctx := context.Background()

	_, origin := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, origin)
	first, err := origin.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("origin ExportBackup: %v", err)
	}

	// Land in a shared DB (with a neighbour realm row), then export again.
	_, shared := newRealmStore(t, domain.DefaultRealm)
	if _, err := shared.CreateAccount(ctx, domain.Account{Code: "neighbour"}); err != nil {
		t.Fatalf("shared CreateAccount: %v", err)
	}
	if _, err := shared.RestoreBackup(ctx, first, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("restore into shared: %v", err)
	}
	// Export ONLY the moved realm's account by selector, so the neighbour does not
	// travel back out.
	second, err := shared.ExportBackup(ctx, backup.Scope{
		All: false, Sections: backup.AllSections,
		Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}},
	})
	if err != nil {
		t.Fatalf("shared ExportBackup: %v", err)
	}

	// Land back into a fresh isolated DB.
	_, iso := newRealmStore(t, domain.DefaultRealm)
	if _, err := iso.RestoreBackup(ctx, second, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("restore into isolated: %v", err)
	}

	// The neighbour did not travel; acc-1 did, with its order external id intact.
	if _, ok, _ := iso.GetAccount(ctx, "neighbour"); ok {
		t.Fatal("neighbour realm row leaked into the isolated restore")
	}
	if _, ok, _ := iso.GetAccount(ctx, "acc-1"); !ok {
		t.Fatal("acc-1 missing after shared -> isolated move")
	}
	if _, err := iso.GetOrder(ctx, orderXID); err != nil {
		t.Fatalf("order external id not preserved across both hops: %v", err)
	}
}

// TestBackupRestoreInsertMissingSkipsExisting asserts the insert-missing mode
// skips rows whose portable identity is already present and reports the skip.
func TestBackupRestoreInsertMissingSkipsExisting(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	// Pre-create the asset under a different title; insert-missing must leave it.
	if _, err := dst.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Preexisting"}); err != nil {
		t.Fatalf("dst CreateAsset: %v", err)
	}
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeInsertMissing,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, _, _ := dst.GetAsset(ctx, "AAPL")
	if got.Title != "Preexisting" {
		t.Fatalf("insert-missing overwrote an existing asset: %+v", got)
	}
	if summary.Skipped[backup.SectionAccountsGroups] == 0 {
		t.Fatalf("expected a skipped dictionary row, summary = %+v", summary)
	}
}

// TestBackupRestoreOverwriteUpdatesExisting asserts overwrite updates a row that
// shares the portable identity rather than skipping it.
func TestBackupRestoreOverwriteUpdatesExisting(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	created, err := dst.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Stale"})
	if err != nil {
		t.Fatalf("dst CreateAsset: %v", err)
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, ok, err := dst.GetAsset(ctx, "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetAsset after restore: ok=%v err=%v", ok, err)
	}
	if got.Title != "Apple" || got.EngineAssetID != created.EngineAssetID {
		t.Fatalf("overwrite did not update existing asset: %+v", got)
	}
}

func TestBackupRestoreReplaceAllPrunesActivityBeforeSigningKeys(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	seedSigningKey(t, ctx, rs, "key-prune")
	order, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	event, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: order.ExternalID, Type: domain.OrderEventSubmitted, Source: domain.SourcePanel,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if err := rs.PutEventAttestation(ctx, event.ExternalID, domain.EventAttestation{
		Token:       "tok",
		KeyID:       "key-prune",
		Alg:         "ed25519",
		RequestType: domain.AttestationRequestSubmit,
		Mode:        "immediate",
		IssuedAt:    "2026-06-26T10:00:00Z",
	}); err != nil {
		t.Fatalf("PutEventAttestation: %v", err)
	}

	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{
			Sections: []backup.Section{
				backup.SectionActivityHistory,
				backup.SectionGeneralSettings,
			},
		},
		backup.Data{
			Accounts: []backup.Account{{Code: "acc-1"}},
		},
		backup.CredentialFormPlaintext,
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{
			Sections: []backup.Section{
				backup.SectionActivityHistory,
				backup.SectionGeneralSettings,
			},
		},
		Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if _, err := rs.GetOrder(ctx, order.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetOrder after restore = %v, want ErrNotFound", err)
	}
	if _, err := rs.GetSigningKey(ctx, "key-prune"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSigningKey after restore = %v, want ErrNotFound", err)
	}
}

func TestBackupRestoreReplaceAllUsesArchiveGroupForActivityPrune(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL", Title: "Apple"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "Dollar"})
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "live-g"}); err != nil {
		t.Fatalf("CreateGroup(live): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "archive-g"}); err != nil {
		t.Fatalf("CreateGroup(archive): %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", GroupCode: "live-g",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{
			Sections: []backup.Section{backup.SectionActivityHistory},
			Accounts: backup.EntitySelector{
				Groups: []string{"archive-g"},
			},
		},
		backup.Data{
			Groups: []backup.AccountGroup{{Code: "archive-g"}},
			Accounts: []backup.Account{
				{Code: "acc-1", GroupCode: "archive-g"},
			},
		},
		backup.CredentialFormPlaintext,
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{
			Sections: []backup.Section{backup.SectionActivityHistory},
			Accounts: backup.EntitySelector{
				Groups: []string{"archive-g"},
			},
		},
		Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if _, err := rs.GetOrder(ctx, order.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetOrder after archive-group restore = %v, want ErrNotFound", err)
	}
}

func TestRestoreRejectsUnknownAccountBlockCodeBeforeCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "keep", Title: "before"}); err != nil {
		t.Fatalf("CreateAccount(keep): %v", err)
	}
	scope := backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{Accounts: []backup.Account{
			{Code: "valid-first"},
			{
				Code: "malformed", Blocked: true, BlockPolicy: "FuturePolicy",
				BlockCode: "future_unknown_code", BlockReason: "risk",
			},
		}},
		backup.CredentialFormPlaintext,
	)
	_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope,
		Mode:  backup.RestoreModeOverwrite,
	})
	if !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), `unknown blockCode "future_unknown_code"`) {
		t.Fatalf("RestoreBackup() = %v, want loud unknown blockCode error", err)
	}
	if _, ok, getErr := rs.GetAccount(ctx, "valid-first"); getErr != nil || ok {
		t.Fatalf("valid row before malformed cause committed: ok %v, err %v", ok, getErr)
	}
	kept, ok, getErr := rs.GetAccount(ctx, "keep")
	if getErr != nil || !ok || kept.Title != "before" {
		t.Fatalf("preexisting row changed: account %+v, ok %v, err %v", kept, ok, getErr)
	}
}

// TestRestoreValidatesDictionaryCodes proves the restore path is behind the
// same code-validation seam as the live API. An archive is caller-supplied
// data, so without this a hand-crafted file could plant a code carrying a NUL,
// a newline, or an unbounded length straight into the dictionaries.
func TestRestoreValidatesDictionaryCodes(t *testing.T) {
	longCode := strings.Repeat("x", 10_000)
	cases := []struct {
		name string
		data backup.Data
	}{
		{"account newline", backup.Data{
			Accounts: []backup.Account{{Code: "acc\n1"}},
		}},
		{"account dot segment", backup.Data{
			Accounts: []backup.Account{{Code: ".."}},
		}},
		{"account oversized", backup.Data{
			Accounts: []backup.Account{{Code: longCode}},
		}},
		{"account block reason newline", backup.Data{
			Accounts: []backup.Account{{
				Code: "acc-1", Blocked: true,
				BlockReason: "risk\nblock account acc-2: forged",
			}},
		}},
		{"group nul", backup.Data{
			Groups: []backup.AccountGroup{{Code: "grp\x001"}},
		}},
		{"group dot segment", backup.Data{
			Groups: []backup.AccountGroup{{Code: "."}},
		}},
		{"group block reason newline", backup.Data{
			Groups: []backup.AccountGroup{{
				Code: "grp-1", Blocked: true,
				BlockReason: "risk\nblock group grp-2: forged",
			}},
		}},
		{"asset nul", backup.Data{
			Assets: []backup.Asset{{Code: "US\x00D"}},
		}},
		{"asset class dot segment", backup.Data{
			AssetClasses: []domain.AssetClass{{Code: ".."}},
		}},
		{"principal newline", backup.Data{
			Principals: []domain.Principal{{Code: "oper\nator"}},
		}},
		{"principal oversized", backup.Data{
			Principals: []domain.Principal{{Code: longCode}},
		}},
		{"principal invalid title", backup.Data{
			Principals: []domain.Principal{{
				Code: "operator", Title: "Desk\x00Operator",
			}},
		}},
		{"account invalid UTF-8", backup.Data{
			Accounts: []backup.Account{{Code: "acc\xff"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t)
			archive := backup.NewArchive(
				time.Now().UTC(),
				"test",
				backup.RealmLabel{Code: string(domain.DefaultRealm)},
				backup.Scope{All: true},
				tc.data,
				backup.CredentialFormPlaintext,
			)
			_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: backup.Scope{All: true},
				Mode:  backup.RestoreModeOverwrite,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
			}
		})
	}
}

// TestBackupRestoreRejectsRowWithoutSource proves an archive row without a
// source is rejected like any other write: the restore fails with ErrInvalid
// naming the record kind and rolls back, leaving the audit log empty.
func TestBackupRestoreRejectsRowWithoutSource(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{All: true},
		backup.Data{Audit: []domain.AuditRow{{
			ExternalID: mustExternalID(t), At: time.Now().UTC(),
			Action: domain.AuditActionCreateAccount,
		}}},
		backup.CredentialFormPlaintext,
	)
	_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup(audit row without source) = %v, want ErrInvalid", err)
	}
	if want := "audit source is required"; !strings.Contains(err.Error(), want) {
		t.Fatalf("RestoreBackup error = %q, want it to contain %q", err, want)
	}
	if n := countRows(t, ctx, rs.(*realmStore), "audit"); n != 0 {
		t.Fatalf("audit rows after rejected restore = %d, want 0", n)
	}
}

// TestListAuditOrdersSameSecondRowsOfDifferentPrecisionNewestFirst proves the
// store's own ORDER BY at DESC is chronological straight from SQL. Restore is
// the one write path where the caller supplies the row time: two audit rows in
// the same second, the earlier one with fewer significant fractional digits
// (the case a trimmed-zeros text form misorders), list newest first.
func TestListAuditOrdersSameSecondRowsOfDifferentPrecisionNewestFirst(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	base := time.Date(2026, time.September, 15, 10, 0, 0, 0, time.UTC)
	earlier := domain.AuditRow{
		ExternalID: mustExternalID(t), At: base.Add(123450 * time.Microsecond),
		Action: domain.AuditActionCreateAccount, Source: domain.SourceSystem,
	}
	later := domain.AuditRow{
		ExternalID: mustExternalID(t), At: base.Add(123456 * time.Microsecond),
		Action: domain.AuditActionCreateAccount, Source: domain.SourceSystem,
	}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{All: true},
		backup.Data{Audit: []domain.AuditRow{earlier, later}},
		backup.CredentialFormPlaintext,
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	rows, err := rs.ListAudit(ctx, 2)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListAudit len = %d, want 2", len(rows))
	}
	if rows[0].ExternalID != later.ExternalID || rows[1].ExternalID != earlier.ExternalID {
		t.Fatalf(
			"ListAudit order = [%v %v], want the later row (%v) first",
			rows[0].At, rows[1].At, later.At,
		)
	}
}

func TestBackupRestoreRejectsInvalidLimitsWithContext(t *testing.T) {
	scope := backup.Scope{Sections: []backup.Section{backup.SectionRiskLimits}}
	cases := []struct {
		name string
		data backup.Data
		want []string
	}{
		{
			name: "rate",
			data: backup.Data{RateLimits: []domain.LimitRate{
				{Scope: domain.ScopeBroker, MaxOrders: 1, Window: time.Second},
				{Scope: domain.ScopeAccount, Account: "acc-1", Window: time.Minute},
			}},
			want: []string{"rate_limit", `scope "account"`, `account "acc-1"`},
		},
		{
			name: "order-size",
			data: backup.Data{OrderSizeLimits: []domain.LimitOrderSize{
				{Scope: domain.ScopeBroker, MaxQuantity: "1"},
				{
					Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL",
					MaxNotional: "1",
				},
			}},
			want: []string{"order_size_limit", `scope "underlying_asset"`, `asset "AAPL"`},
		},
		{
			name: "spot-funds-pnl-bounds",
			data: backup.Data{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
				{Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1"},
				{
					Scope: domain.ScopeAccountGroup, AccountGroup: "desk-a",
					LowerBound: "-1",
				},
			}},
			want: []string{
				"spot_funds_pnl_bounds_kill_switch",
				`scope "account_group"`,
				`account group "desk-a"`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t)
			mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD"})
			before := snapshotBackupData(t, ctx, rs)
			archive := backup.NewArchive(
				time.Now().UTC(),
				"test",
				backup.RealmLabel{Code: string(domain.DefaultRealm)},
				scope,
				tc.data,
				backup.CredentialFormPlaintext,
			)
			_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: scope, Mode: backup.RestoreModeInsertMissing,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
				}
			}
			after := snapshotBackupData(t, ctx, rs)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("database changed after rejected restore:\n before=%+v\n after=%+v", before, after)
			}
		})
	}
}

func TestBackupRestoreRejectsInvalidBalanceAndRollsBack(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD"})
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	before := snapshotBackupData(t, ctx, rs)
	scope := backup.Scope{Sections: []backup.Section{backup.SectionPositions}}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{Balances: []backup.Balance{
			{Account: "acc-1", Asset: "USD", Available: "20"},
			{Account: "acc-1", Asset: "AAPL", Available: "not-a-decimal"},
		}},
		backup.CredentialFormPlaintext,
	)

	_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeOverwrite,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
	}
	for _, want := range []string{"restore balance", "acc-1", "AAPL", "available"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
		}
	}
	after := snapshotBackupData(t, ctx, rs)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("database changed after rejected restore:\n before=%+v\n after=%+v", before, after)
	}
}

func TestBackupRestoreRejectsNonCanonicalMarketDataStringsAndRollsBack(t *testing.T) {
	tests := []struct {
		name      string
		wantField string
		mutate    func(*backup.Data)
	}{
		{
			name:      "provider",
			wantField: "provider",
			mutate: func(data *backup.Data) {
				data.MarketDataInstances[1].Provider = " byo"
			},
		},
		{
			name:      "label",
			wantField: "label",
			mutate: func(data *backup.Data) {
				data.MarketDataInstances[1].Label = " manual "
			},
		},
		{
			name:      "credentials",
			wantField: "credentials",
			mutate: func(data *backup.Data) {
				data.MarketDataInstances[1].Credentials = []byte(" {} ")
			},
		},
		{
			name:      "external-symbol-whitespace-only",
			wantField: "external symbol",
			mutate: func(data *backup.Data) {
				data.MarketDataInstruments[0].ExternalSymbol = "   "
			},
		},
		{
			name:      "base-asset",
			wantField: "base asset",
			mutate: func(data *backup.Data) {
				data.MarketDataInstruments[0].BaseAsset = " AAPL"
			},
		},
		{
			name:      "quote-asset",
			wantField: "quote asset",
			mutate: func(data *backup.Data) {
				data.MarketDataInstruments[0].QuoteAsset = "USD "
			},
		},
		{
			name:      "manual-price",
			wantField: "manual price",
			mutate: func(data *backup.Data) {
				data.MarketDataInstruments[0].ManualPrice = " 100"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t)
			mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL"})
			mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD"})
			before := snapshotBackupData(t, ctx, rs)
			firstID := mustExternalID(t)
			badID := mustExternalID(t)
			scope := backup.Scope{
				Sections: []backup.Section{backup.SectionMarketData},
			}
			data := backup.Data{
				MarketDataInstances: []backup.MarketDataInstance{
					{
						ExternalID: firstID,
						Provider:   domain.MarketDataProviderBYO,
						Label:      "first",
					},
					{
						ExternalID: badID,
						Provider:   domain.MarketDataProviderBYO,
						Label:      "manual",
					},
				},
				MarketDataInstruments: []backup.MarketDataInstrument{{
					Instance:       badID,
					ExternalSymbol: "AAPL-USD",
					BaseAsset:      "AAPL",
					QuoteAsset:     "USD",
					ManualPrice:    "100",
				}},
			}
			test.mutate(&data)
			archive := backup.NewArchive(
				time.Now().UTC(),
				"test",
				backup.RealmLabel{Code: string(domain.DefaultRealm)},
				scope,
				data,
				backup.CredentialFormPlaintext,
			)

			_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: scope, Mode: backup.RestoreModeOverwrite,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
			}
			for _, want := range []string{"restore market-data", test.wantField} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
				}
			}
			after := snapshotBackupData(t, ctx, rs)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf(
					"database changed after rejected restore:\n before=%+v\n after=%+v",
					before,
					after,
				)
			}
		})
	}
}

func TestBackupRestoreRejectsZeroMarketDataInstanceIDAndRollsBack(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	before := snapshotBackupData(t, ctx, rs)
	scope := backup.Scope{Sections: []backup.Section{backup.SectionMarketData}}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{MarketDataInstances: []backup.MarketDataInstance{
			{
				ExternalID: mustExternalID(t),
				Provider:   domain.MarketDataProviderBYO,
				Label:      "first",
			},
			{
				Provider: domain.MarketDataProviderBYO,
				Label:    "zero-id",
			},
		}},
		backup.CredentialFormPlaintext,
	)

	_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeOverwrite,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
	}
	for _, want := range []string{
		string(backup.SectionMarketData), "zero-id", "external id",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
		}
	}
	after := snapshotBackupData(t, ctx, rs)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("database changed after rejected restore:\n before=%+v\n after=%+v", before, after)
	}
}

func TestBackupRestoreRejectsInvalidOrderLeavesAndRollsBack(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	before := snapshotBackupData(t, ctx, rs)
	firstID := mustExternalID(t)
	badID := mustExternalID(t)
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionActivityHistory},
	}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{Orders: []backup.OrderRecord{
			{Order: domain.Order{
				ExternalID: firstID, Account: "acc-1", BaseAsset: "AAPL",
				QuoteAsset: "USD", Principal: "operator", Source: domain.SourcePanel,
				Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
				AmountValue: "10", Leaves: "10", Price: "150",
				Status:           domain.OrderStatusSubmitted,
				ReservedQuantity: "0",
			}},
			{Order: domain.Order{
				ExternalID: badID, Account: "acc-1", BaseAsset: "AAPL",
				QuoteAsset: "USD", Principal: "operator", Source: domain.SourcePanel,
				Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
				AmountValue: "10", Leaves: "1e5", Price: "150",
				Status:           domain.OrderStatusSubmitted,
				ReservedQuantity: "0",
			}},
		}},
		backup.CredentialFormPlaintext,
	)

	_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeOverwrite,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want invalid leaves error")
	}
	for _, want := range []string{
		string(backup.SectionActivityHistory), badID.String(), "leaves", "plain decimal",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
		}
	}
	after := snapshotBackupData(t, ctx, rs)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("database changed after rejected restore:\n before=%+v\n after=%+v", before, after)
	}
}

func TestBackupRestorePreservesReservationSeparatelyFromReportedLeaves(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	input := sampleOrder()
	input.Status = domain.OrderStatusCommitted
	input.Leaves = "99.00"
	input.ReservedQuantity = "3.5"
	order, err := rs.CreateOrder(ctx, input)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	scope := backup.Scope{Sections: []backup.Section{backup.SectionActivityHistory}}
	archive, err := rs.ExportBackup(ctx, scope)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account: order.Account, Order: order.ExternalID,
		OrderStatus: domain.OrderStatusCommitted, ReservedQuantity: "1", Leaves: "77",
	}); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}
	options := backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeOverwrite}
	if _, err := rs.RestoreBackup(ctx, archive, options); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	detail, err := rs.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "99.00" || detail.Order.ReservedQuantity != "3.5" {
		t.Fatalf("reservation and reported leaves did not round-trip independently: %+v", detail.Order)
	}
	before := snapshotBackupData(t, ctx, rs)
	for _, invalid := range []string{"", "-1", "1e3", "not-a-number"} {
		archive.Data.Orders[0].Order.ReservedQuantity = invalid
		if _, err := rs.RestoreBackup(ctx, archive, options); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("RestoreBackup reservation %q: %v, want invalid", invalid, err)
		}
		if after := snapshotBackupData(t, ctx, rs); !reflect.DeepEqual(after, before) {
			t.Fatalf("invalid reservation %q changed the database", invalid)
		}
	}
}

func TestBackupRestoreRejectsInvalidSigningKeysAndRollsBack(t *testing.T) {
	tests := []struct {
		name string
		want string
		keys func(*testing.T) []backup.SigningKey
	}{
		{
			name: "algorithm",
			want: "algorithm",
			keys: func(t *testing.T) []backup.SigningKey {
				key := signingKeyForBackup(t, "bad-alg", 1, true)
				key.Alg = "rsa"
				return []backup.SigningKey{key}
			},
		},
		{
			name: "public-key-length",
			want: "public key length",
			keys: func(t *testing.T) []backup.SigningKey {
				key := signingKeyForBackup(t, "bad-public", 3, true)
				key.PublicKey = []byte("bad")
				return []backup.SigningKey{key}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t)
			before := snapshotBackupData(t, ctx, rs)
			scope := backup.Scope{
				Sections: []backup.Section{backup.SectionGeneralSettings},
			}
			archive := backup.NewArchive(
				time.Now().UTC(),
				"test",
				backup.RealmLabel{Code: string(domain.DefaultRealm)},
				scope,
				backup.Data{SigningKeys: test.keys(t)},
				backup.CredentialFormPlaintext,
			)

			_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: scope, Mode: backup.RestoreModeOverwrite,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
			}
			for _, want := range []string{
				string(backup.SectionGeneralSettings), "signing key", test.want,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
				}
			}
			after := snapshotBackupData(t, ctx, rs)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf(
					"database changed after rejected restore:\n before=%+v\n after=%+v",
					before,
					after,
				)
			}
		})
	}
}

func TestBackupRestoreReplaceAllKeepsLiveOnlyAccountInSelectedGroup(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL", Title: "Apple"})
	mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "Dollar"})
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "archive-g"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "live-only", GroupCode: "archive-g",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := sampleOrder()
	order.Account = "live-only"
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{
			Sections: []backup.Section{backup.SectionActivityHistory},
			Accounts: backup.EntitySelector{
				Groups: []string{"archive-g"},
			},
		},
		backup.Data{
			Groups: []backup.AccountGroup{{Code: "archive-g"}},
		},
		backup.CredentialFormPlaintext,
	)
	if _, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{
			Sections: []backup.Section{backup.SectionActivityHistory},
			Accounts: backup.EntitySelector{
				Groups: []string{"archive-g"},
			},
		},
		Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if _, err := rs.GetOrder(ctx, created.ExternalID); err != nil {
		t.Fatalf("GetOrder for live-only account after restore: %v", err)
	}
}

func TestBackupRestoreOverwriteKeepsTargetOnlyOrderChildren(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	full, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, full, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup full: %v", err)
	}
	before, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("GetOrder before partial restore: %v", err)
	}
	if len(before.Events) == 0 || len(before.Trades) == 0 {
		t.Fatalf("seeded order children missing before restore: events=%d trades=%d",
			len(before.Events), len(before.Trades))
	}

	partial := full
	partial.Data.OrderEvents = nil
	partial.Data.Trades = nil
	if _, err := dst.RestoreBackup(ctx, partial, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}); err != nil {
		t.Fatalf("RestoreBackup partial overwrite: %v", err)
	}

	after, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("GetOrder after partial restore: %v", err)
	}
	if len(after.Events) != len(before.Events) || len(after.Trades) != len(before.Trades) {
		t.Fatalf("partial overwrite dropped order children: before events=%d trades=%d after events=%d trades=%d",
			len(before.Events), len(before.Trades), len(after.Events), len(after.Trades))
	}
}

// TestBackupRestoreReplaceAllDeletesAbsentRows asserts that replace-all makes
// each restored section equal the archive: a row present in the target before
// the restore but absent from the archive is gone afterward, across every kind
// of row (dictionary, position, limit, activity, audit, market data, settings).
// overwrite and insert_missing leave such a row in place.
func TestBackupRestoreReplaceAllDeletesAbsentRows(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// seedExtra adds rows the archive does NOT carry: an extra account (with a
	// balance, a limit, an adjustment and an audit row), an extra group, an extra
	// market-data instance, an extra MCP override and an extra user setting.
	seedExtra := func(t *testing.T, rs fwstore.RealmStore) {
		t.Helper()
		// Assets the extra rows reference; the archive also carries them, so a
		// replace-all upsert keeps them (asset are shared support rows, not pruned).
		mustCreateAsset(t, ctx, rs, domain.Asset{Code: "USD", Title: "US Dollar"})
		mustCreateAsset(t, ctx, rs, domain.Asset{Code: "AAPL", Title: "Apple"})
		if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
			t.Fatalf("CreatePrincipal(extra): %v", err)
		}
		if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "extra-grp"}); err != nil {
			t.Fatalf("CreateGroup(extra): %v", err)
		}
		if _, err := rs.CreateAccount(ctx, domain.Account{Code: "extra-acc"}); err != nil {
			t.Fatalf("CreateAccount(extra): %v", err)
		}
		if err := rs.UpsertBalance(ctx, domain.Balance{
			Account: "extra-acc", Asset: "USD", Available: "7", UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("UpsertBalance(extra): %v", err)
		}
		if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
			Scope: domain.ScopeAccountUnderlyingAsset, Account: "extra-acc", Asset: "AAPL", MaxQuantity: "9",
		}); err != nil {
			t.Fatalf("PutOrderSizeLimit(extra): %v", err)
		}
		if _, err := rs.AppendAdjustment(ctx, domain.AccountAdjustmentRecord{
			Account: "extra-acc", Asset: "USD", Source: domain.SourcePanel,
			Request:  domain.AdjustmentRequest{Asset: "USD"},
			Accepted: &domain.AdjustmentOutcomeAccepted{BalanceDelta: "7", BalanceResult: "7"},
		}); err != nil {
			t.Fatalf("AppendAdjustment(extra): %v", err)
		}
		if err := rs.AppendAudit(ctx, fwstore.AuditEntry{
			Action: domain.AuditActionCreateAccount, Account: "extra-acc",
			Actor: "operator", Source: domain.SourcePanel, Detail: "extra",
		}); err != nil {
			t.Fatalf("AppendAudit(extra): %v", err)
		}
		if _, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
			Provider: domain.MarketDataProviderBYO, Label: "extra-md", Enabled: true,
		}); err != nil {
			t.Fatalf("CreateMarketDataInstance(extra): %v", err)
		}
		if err := rs.SetMcpAccess(ctx, "cancel_order", true); err != nil {
			t.Fatalf("SetMcpAccess(extra): %v", err)
		}
		if err := rs.SetUserSetting(ctx, "extra-user", domain.UserSettingWelcomeSeen, "1"); err != nil {
			t.Fatalf("SetUserSetting(extra): %v", err)
		}
	}

	// Non-replace modes leave the rows the archive does not carry untouched.
	for _, mode := range []backup.RestoreMode{
		backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing,
	} {
		_, dst := newRealmStore(t, domain.DefaultRealm)
		seedExtra(t, dst)
		if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: mode,
		}); err != nil {
			t.Fatalf("RestoreBackup(%s): %v", mode, err)
		}
		if _, ok, err := dst.GetAccount(ctx, "extra-acc"); err != nil || !ok {
			t.Fatalf("%s removed an extra account: ok=%v err=%v", mode, ok, err)
		}
		if _, ok, err := dst.GetGroup(ctx, "extra-grp"); err != nil || !ok {
			t.Fatalf("%s removed an extra group: ok=%v err=%v", mode, ok, err)
		}
		access, _ := dst.ListMcpAccess(ctx)
		if !access["cancel_order"] {
			t.Fatalf("%s removed an extra mcp override: %+v", mode, access)
		}
	}

	// Replace-all deletes every row the archive does not carry, and its cascade
	// removes the extra account's balance, limit, adjustment and audit rows.
	_, dst := newRealmStore(t, domain.DefaultRealm)
	seedExtra(t, dst)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(replace_all): %v", err)
	}
	if _, ok, err := dst.GetAccount(ctx, "extra-acc"); err != nil || ok {
		t.Fatalf("replace_all kept an absent account: ok=%v err=%v", ok, err)
	}
	if _, ok, err := dst.GetGroup(ctx, "extra-grp"); err != nil || ok {
		t.Fatalf("replace_all kept an absent group: ok=%v err=%v", ok, err)
	}
	if _, ok, err := dst.GetBalance(ctx, "extra-acc", "USD"); err != nil || ok {
		t.Fatalf("replace_all kept an absent balance: ok=%v err=%v", ok, err)
	}
	limits, err := dst.ListOrderSizeLimits(ctx, "extra-acc")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(limits) != 0 {
		t.Fatalf("replace_all kept an absent limit: %+v", limits)
	}
	adj, err := dst.ListAdjustments(ctx, "extra-acc", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(adj) != 0 {
		t.Fatalf("replace_all kept an absent adjustment: %+v", adj)
	}
	access, _ := dst.ListMcpAccess(ctx)
	if access["cancel_order"] {
		t.Fatalf("replace_all kept an absent mcp override: %+v", access)
	}
	settings, err := dst.ListUserSettings(ctx)
	if err != nil {
		t.Fatalf("ListUserSettings: %v", err)
	}
	for _, s := range settings {
		if s.UserID == "extra-user" {
			t.Fatalf("replace_all kept an absent user setting: %+v", s)
		}
	}
	instances, err := dst.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	for _, inst := range instances {
		if inst.Label == "extra-md" {
			t.Fatalf("replace_all kept an absent market-data instance: %+v", inst)
		}
	}
	// The archive's own rows survive, with their portable identity preserved.
	if _, ok, err := dst.GetAccount(ctx, "acc-1"); err != nil || !ok {
		t.Fatalf("replace_all dropped an archived account: ok=%v err=%v", ok, err)
	}
}

// TestBackupRestoreReplaceAllPreservesIdentityReassignsEngineIDs asserts that a
// replace-all that deletes an absent account still keeps the archived rows'
// external_id/code while assigning fresh, valid engine ids, and leaves no
// dangling foreign key (the order children resolve after the cascade).
func TestBackupRestoreReplaceAllPreservesIdentityReassignsEngineIDs(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// Destination holds a different account (absent from the archive) plus a stale
	// copy of the archived account under a different engine id.
	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.CreateAccount(ctx, domain.Account{Code: "absent-acc"}); err != nil {
		t.Fatalf("dst CreateAccount(absent): %v", err)
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(replace_all): %v", err)
	}

	if _, ok, err := dst.GetAccount(ctx, "absent-acc"); err != nil || ok {
		t.Fatalf("absent account survived replace_all: ok=%v err=%v", ok, err)
	}
	acc, ok, err := dst.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("archived account missing: ok=%v err=%v", ok, err)
	}
	if acc.Code != "acc-1" || acc.GroupCode != "grp-1" {
		t.Fatalf("archived account identity not preserved: %+v", acc)
	}
	if err := domain.ValidateEngineAccountID(acc.EngineAccountID); err != nil {
		t.Fatalf("restored engine account id invalid: %v", err)
	}
	// The order and its children resolve, so the cascade did not orphan or break a
	// foreign key, and the archived external id is preserved.
	detail, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 1 || len(detail.Trades) != 1 {
		t.Fatalf("order children not restored: events=%d trades=%d",
			len(detail.Events), len(detail.Trades))
	}
}

// TestBackupRestoreReplaceAllScopedToSelectedAccounts asserts a scoped
// replace-all replaces only the selected account's rows: an unselected account
// present before the restore survives, while the selected account's rows become
// exactly what the archive carries.
func TestBackupRestoreReplaceAllScopedToSelectedAccounts(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	// An unselected account that the archive does not carry; a scoped replace-all
	// of acc-1 must leave it alone.
	if _, err := dst.CreateAccount(ctx, domain.Account{Code: "other-acc"}); err != nil {
		t.Fatalf("dst CreateAccount(other): %v", err)
	}
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionAccountsGroups},
		Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}},
	}
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(scoped replace_all): %v", err)
	}
	if _, ok, err := dst.GetAccount(ctx, "other-acc"); err != nil || !ok {
		t.Fatalf("scoped replace_all removed an unselected account: ok=%v err=%v", ok, err)
	}
	if _, ok, err := dst.GetAccount(ctx, "acc-1"); err != nil || !ok {
		t.Fatalf("scoped replace_all missing the selected account: ok=%v err=%v", ok, err)
	}
}

func TestBackupRestoreReplaceAllPrunesSpotFundsGroupLimitForSelectedAccount(
	t *testing.T,
) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	if _, err := src.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}); err != nil {
		t.Fatalf("src CreateGroup(desk-a): %v", err)
	}
	if _, err := src.CreateAccount(ctx, domain.Account{
		Code: "acc-1", GroupCode: "desk-a",
	}); err != nil {
		t.Fatalf("src CreateAccount(acc-1): %v", err)
	}
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionRiskLimits},
		Accounts: backup.EntitySelector{Accounts: []string{"acc-1"}},
	}
	archive, err := src.ExportBackup(ctx, scope)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	mustCreateAsset(t, ctx, dst, domain.Asset{Code: "USD", Title: "US Dollar"})
	if _, err := dst.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}); err != nil {
		t.Fatalf("dst CreateGroup(desk-a): %v", err)
	}
	if _, err := dst.CreateGroup(ctx, domain.AccountGroup{Code: "desk-b"}); err != nil {
		t.Fatalf("dst CreateGroup(desk-b): %v", err)
	}
	if _, err := dst.CreateAccount(ctx, domain.Account{
		Code: "acc-1", GroupCode: "desk-a",
	}); err != nil {
		t.Fatalf("dst CreateAccount(acc-1): %v", err)
	}
	if err := dst.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:        domain.ScopeAccountGroup,
		AccountGroup: "desk-a",
		Currency:     "USD",
		LowerBound:   "-1000",
	}); err != nil {
		t.Fatalf("dst PutSpotFundsPnlBoundsLimit(desk-a): %v", err)
	}
	if err := dst.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:        domain.ScopeAccountGroup,
		AccountGroup: "desk-b",
		Currency:     "USD",
		LowerBound:   "-2000",
	}); err != nil {
		t.Fatalf("dst PutSpotFundsPnlBoundsLimit(desk-b): %v", err)
	}

	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup(scoped replace_all): %v", err)
	}

	limits, err := dst.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("spotFundsPnlBoundsLimits = %+v, want only desk-b", limits)
	}
	if limits[0].AccountGroup != "desk-b" {
		t.Fatalf("remaining spotFundsPnlBoundsLimit = %+v, want desk-b", limits[0])
	}
}

// TestBackupRestoreSelectorSubset restores only the general-settings section and
// asserts the store leaves restart classification to the node and does not pull
// in the account-addressed sections.
func TestBackupRestoreSelectorSubset(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatal("RestartRequired = true for a settings-only restore, want false")
	}
	// The MCP override landed; no account was restored.
	access, err := dst.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if access["submit_order"] != true {
		t.Fatalf("settings restore missing mcp override: %+v", access)
	}
	if _, ok, _ := dst.GetAccount(ctx, "acc-1"); ok {
		t.Fatal("settings-only restore pulled in an account")
	}
}

// TestBackupRestoreUnknownModeRejected asserts an empty/unknown mode is rejected
// with ErrInvalid and writes nothing.
func TestBackupRestoreUnknownModeRejected(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, _ := src.ExportBackup(ctx, backup.Scope{All: true})

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: "",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup(empty mode) error = %v, want ErrInvalid", err)
	}
	if _, ok, _ := dst.GetAccount(ctx, "acc-1"); ok {
		t.Fatal("rejected restore still wrote rows")
	}
}

// TestBackupRestoreResolvesForeignKeysDictionaryFirst restores an archive whose
// machine records reference dictionaries, asserting the dictionary-first order
// lets every cross-row link resolve (no dangling foreign key).
func TestBackupRestoreResolvesForeignKeysDictionaryFirst(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	orderXID := seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// The order's account/asset/principal links resolved (GetOrder joins them).
	detail, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Account != "acc-1" || detail.Order.BaseAsset != "AAPL" ||
		detail.Order.QuoteAsset != "USD" || detail.Order.Principal != "operator" {
		t.Fatalf("order links not resolved: %+v", detail.Order)
	}
	if len(detail.Events) != 1 || len(detail.Trades) != 1 {
		t.Fatalf("order children not restored: events=%d trades=%d",
			len(detail.Events), len(detail.Trades))
	}
	// The instrument's instance link resolved by external id.
	instruments, err := dst.ListMarketDataInstruments(ctx, instanceXID(t, ctx, dst))
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != 1 || instruments[0].ExternalSymbol != "AAPLUSD" {
		t.Fatalf("instrument not restored under its instance: %+v", instruments)
	}
}

// backupExportedTables is the set of user tables the portable backup carries
// (one per backup.Data section). Keep it in lockstep with backup.Data: a new
// exported table must appear here.
var backupExportedTables = map[string]bool{
	"asset":                      true,
	"asset_class":                true,
	"principal":                  true,
	"account_group":              true,
	"account":                    true,
	"balance":                    true,
	"limit_rate":                 true,
	"limit_order_size":           true,
	"limit_spot_funds_pnl_bound": true,
	"adjustment":                 true,
	"order_record":               true,
	"event_attestation":          true,
	"order_event":                true,
	"execution_report":           true,
	"execution_report_event":     true,
	"trade":                      true,
	"audit":                      true,
	"market_data_instance":       true,
	"market_data_instrument":     true,
	"signing_key":                true,
	"signing_config":             true,
	"mcp_access":                 true,
	"user_setting":               true,
}

// backupExcludedTables is the explicit allowlist of user tables the backup does
// NOT carry, with the reason each is intentionally absent.
var backupExcludedTables = map[string]bool{
	"realm":                    true, // realm identity row, re-established by the target connector
	"schema_migration":         true, // migration bookkeeping, owned by Migrate
	"secret_state":             true, // installation key verifier, invalid across archive boundaries
	"source_kind":              true, // immutable enum dictionary, seeded by Migrate
	"order_event_type":         true, // immutable enum dictionary, seeded by Migrate
	"order_side":               true, // immutable enum dictionary, seeded by Migrate
	"order_amount_kind":        true, // immutable enum dictionary, seeded by Migrate
	"order_status":             true, // immutable enum dictionary, seeded by Migrate
	"adjustment_status":        true, // immutable enum dictionary, seeded by Migrate
	"audit_action":             true, // immutable enum dictionary, seeded by Migrate
	"attestation_alg":          true, // immutable enum dictionary, seeded by Migrate
	"attestation_request_type": true, // immutable enum dictionary, seeded by Migrate
	"attestation_mode":         true, // immutable enum dictionary, seeded by Migrate
}

// TestBackupCompleteness introspects sqlite_master for every user table and
// asserts each is either exported by the backup or on the explicit exclusion
// allowlist, so a newly added table cannot silently escape the backup.
func TestBackupCompleteness(t *testing.T) {
	ctx := context.Background()
	_, rs := newRealmStore(t, domain.DefaultRealm)
	r := rs.(*realmStore)

	rows, err := r.rawDB().QueryContext(
		ctx,
		`SELECT name FROM sqlite_master
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	)
	if err != nil {
		t.Fatalf("introspect sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		if backupExportedTables[name] || backupExcludedTables[name] {
			continue
		}
		t.Errorf("table %q is neither exported by the backup nor on the exclusion allowlist", name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table names: %v", err)
	}
}

// TestBackupRestoreAtomicityLeavesRealmUnchanged forces a mid-restore failure (a
// replace_all whose archive carries an order referencing an unknown asset code)
// and asserts the whole transaction rolls back: the prune that replace_all runs
// first is undone, so a pre-existing account survives untouched.
func TestBackupRestoreAtomicityLeavesRealmUnchanged(t *testing.T) {
	ctx := context.Background()
	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.CreateAccount(ctx, domain.Account{Code: "keep-acc", Title: "Keep"}); err != nil {
		t.Fatalf("CreateAccount(keep): %v", err)
	}

	// The archive selects account+activity; restoreActivity will fail resolving
	// the order's unknown asset code AFTER the replace_all prune has run.
	scope := backup.Scope{
		Sections: []backup.Section{
			backup.SectionAccountsGroups,
			backup.SectionActivityHistory,
		},
	}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{
			Accounts: []backup.Account{{Code: "new-acc"}},
			Orders: []backup.OrderRecord{{
				Order: domain.Order{
					ExternalID: mustExternalID(t),
					Account:    "new-acc",
					BaseAsset:  "GHOST",
					QuoteAsset: "USD",
				},
			}},
		},
		backup.CredentialFormPlaintext,
	)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeReplaceAll,
	}); err == nil {
		t.Fatal("RestoreBackup succeeded, want a mid-restore failure")
	}

	// The pre-existing account survived: the prune was rolled back with the rest of
	// the failed transaction.
	acc, ok, err := dst.GetAccount(ctx, "keep-acc")
	if err != nil || !ok {
		t.Fatalf("pre-existing account lost after failed restore: ok=%v err=%v", ok, err)
	}
	if acc.Title != "Keep" {
		t.Fatalf("pre-existing account mutated by failed restore: %+v", acc)
	}
	// The partially-applied new account did not survive.
	if _, ok, _ := dst.GetAccount(ctx, "new-acc"); ok {
		t.Fatal("failed restore left a partially-applied account")
	}
}

// TestBackupRestoreRejectsInvalidCommission asserts a restore whose archived
// trade carries a malformed or one-sided commission fails with ErrInvalid and
// persists nothing, mirroring the CreateTrade write-time guard. Without it the
// bad row would land and later break the whole-page CommissionSubtotals rollup.
func TestBackupRestoreRejectsInvalidCommission(t *testing.T) {
	cases := []struct {
		name       string
		commission *domain.Commission
	}{
		{"malformed-amount", &domain.Commission{Amount: "not-decimal", Currency: "USD"}},
		{"amount-only", &domain.Commission{Amount: "-0.12"}},
		{"currency-only", &domain.Commission{Currency: "USD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, dst := seedOrderFixtures(t)
			orderXID := mustExternalID(t)
			scope := backup.Scope{
				Sections: []backup.Section{backup.SectionActivityHistory},
			}
			archive := backup.NewArchive(
				time.Now().UTC(),
				"test",
				backup.RealmLabel{Code: string(domain.DefaultRealm)},
				scope,
				backup.Data{
					Orders: []backup.OrderRecord{{Order: domain.Order{
						ExternalID:  orderXID,
						Account:     "acc-1",
						BaseAsset:   "AAPL",
						QuoteAsset:  "USD",
						Principal:   "operator",
						Source:      domain.SourcePanel,
						Side:        domain.OrderSideBuy,
						AmountKind:  domain.OrderAmountKindQuantity,
						AmountValue: "10",
						Price:       "150",
						Status:      domain.OrderStatusFilled,
					}}},
					Trades: []domain.Trade{{
						ExternalID: mustExternalID(t),
						Order:      orderXID,
						Account:    "acc-1",
						BaseAsset:  "AAPL",
						QuoteAsset: "USD",
						Source:     domain.SourcePanel,
						Side:       domain.OrderSideBuy,
						Quantity:   "10",
						Price:      "150",
						Commission: tc.commission,
					}},
				},
				backup.CredentialFormPlaintext,
			)
			if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: scope, Mode: backup.RestoreModeInsertMissing,
			}); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup(bad commission) = %v, want ErrInvalid", err)
			}
			// The failed restore rolled back atomically: no trade landed.
			if c := countRows(t, ctx, dst.(*realmStore), "trade"); c != 0 {
				t.Fatalf("trades after failed restore = %d, want 0", c)
			}
		})
	}
}

func TestBackupRestoreRejectsUnknownOrderStatusAndRollsBack(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	before := snapshotBackupData(t, ctx, rs)
	firstID := mustExternalID(t)
	badID := mustExternalID(t)
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionActivityHistory},
	}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{Orders: []backup.OrderRecord{
			{Order: domain.Order{
				ExternalID:       firstID,
				Account:          "acc-1",
				BaseAsset:        "AAPL",
				QuoteAsset:       "USD",
				Principal:        "operator",
				Source:           domain.SourcePanel,
				Side:             domain.OrderSideBuy,
				AmountKind:       domain.OrderAmountKindQuantity,
				AmountValue:      "10",
				ReservedQuantity: "0",
				Price:            "150",
				Status:           domain.OrderStatusSubmitted,
			}},
			{Order: domain.Order{
				ExternalID:       badID,
				Account:          "acc-1",
				BaseAsset:        "AAPL",
				QuoteAsset:       "USD",
				Principal:        "operator",
				Source:           domain.SourcePanel,
				Side:             domain.OrderSideBuy,
				AmountKind:       domain.OrderAmountKindQuantity,
				AmountValue:      "10",
				ReservedQuantity: "0",
				Price:            "150",
				Status:           domain.OrderStatus("unknown"),
			}},
		}},
		backup.CredentialFormPlaintext,
	)

	_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeOverwrite,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
	}
	for _, want := range []string{
		string(backup.SectionActivityHistory), "order", badID.String(), "status",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
		}
	}
	after := snapshotBackupData(t, ctx, rs)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("database changed after rejected restore:\n before=%+v\n after=%+v", before, after)
	}
}

func TestBackupRestoreRejectsInvalidAuditActionAndRollsBack(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	before := snapshotBackupData(t, ctx, rs)
	firstID := mustExternalID(t)
	badID := mustExternalID(t)
	scope := backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{Audit: []domain.AuditRow{
			{
				ExternalID: firstID,
				Action:     domain.AuditActionCreateAccount,
				Source:     domain.SourcePanel,
			},
			{
				ExternalID: badID,
				Action:     domain.AuditAction("unknown"),
				Source:     domain.SourcePanel,
			},
		}},
		backup.CredentialFormPlaintext,
	)

	_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeOverwrite,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
	}
	for _, want := range []string{
		string(backup.SectionAuditLog), "audit", badID.String(), "action",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RestoreBackup error = %q, want context %q", err, want)
		}
	}
	after := snapshotBackupData(t, ctx, rs)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("database changed after rejected restore:\n before=%+v\n after=%+v", before, after)
	}
}

func TestBackupRestoreRejectsInvalidAuditDecisionAndRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		action     domain.AuditAction
		verdict    string
		rejectCode string
		orderID    string
		context    string
	}{
		{
			name: "invalid verdict", action: domain.AuditActionSubmitOrder,
			verdict: "banana", orderID: "order-1", context: "verdict",
		},
		{
			name:    "reject code without reject verdict",
			action:  domain.AuditActionSubmitOrder,
			verdict: "accept", rejectCode: "order_size",
			orderID: "order-1", context: "reject code",
		},
		{
			name:    "verdict without order id",
			action:  domain.AuditActionSubmitOrder,
			verdict: "accept", context: "order id",
		},
		{
			name:    "order id without verdict",
			action:  domain.AuditActionSubmitDropCopy,
			orderID: "order-1", context: "verdict",
		},
		{
			name:    "unrelated action with order id",
			action:  domain.AuditActionCreateAccount,
			orderID: "order-1", context: "decision metadata",
		},
		{
			name:    "unrelated action with verdict",
			action:  domain.AuditActionCreateAccount,
			verdict: "accept", context: "decision metadata",
		},
		{
			name:       "unrelated action with reject code",
			action:     domain.AuditActionCreateAccount,
			rejectCode: "order_size", context: "decision metadata",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t)
			before := snapshotBackupData(t, ctx, rs)
			firstID := mustExternalID(t)
			badID := mustExternalID(t)
			scope := backup.Scope{
				Sections: []backup.Section{backup.SectionAuditLog},
			}
			archive := backup.NewArchive(
				time.Now().UTC(),
				"test",
				backup.RealmLabel{Code: string(domain.DefaultRealm)},
				scope,
				backup.Data{Audit: []domain.AuditRow{
					{
						ExternalID: firstID,
						Action:     domain.AuditActionCreateAccount,
						Source:     domain.SourcePanel,
					},
					{
						ExternalID: badID,
						Action:     tc.action,
						Source:     domain.SourcePanel,
						OrderID:    tc.orderID,
						Verdict:    tc.verdict,
						RejectCode: tc.rejectCode,
					},
				}},
				backup.CredentialFormPlaintext,
			)

			_, err := rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
				Scope: scope, Mode: backup.RestoreModeOverwrite,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RestoreBackup = %v, want ErrInvalid", err)
			}
			for _, want := range []string{
				string(backup.SectionAuditLog), "audit",
				badID.String(), tc.context,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf(
						"RestoreBackup error = %q, want context %q",
						err,
						want,
					)
				}
			}
			after := snapshotBackupData(t, ctx, rs)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf(
					"database changed after rejected restore:\n before=%+v\n after=%+v",
					before,
					after,
				)
			}
		})
	}
}

func TestBackupRestoreAcceptsValidAuditDecisionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry domain.AuditRow
	}{
		{
			name: "unrelated action",
			entry: domain.AuditRow{
				Action: domain.AuditActionCreateAccount,
			},
		},
		{
			name: "accepted order",
			entry: domain.AuditRow{
				Action:  domain.AuditActionSubmitOrder,
				OrderID: "opaque-accepted-order",
				Verdict: "accept",
			},
		},
		{
			name: "rejected drop copy without code",
			entry: domain.AuditRow{
				Action:  domain.AuditActionSubmitDropCopy,
				OrderID: "opaque-rejected-drop-copy",
				Verdict: "reject",
			},
		},
		{
			name: "rejected order with code",
			entry: domain.AuditRow{
				Action:  domain.AuditActionSubmitOrder,
				OrderID: "opaque-rejected-order",
				Verdict: "reject", RejectCode: "order_size",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t)
			entry := tc.entry
			entry.ExternalID = mustExternalID(t)
			entry.Source = domain.SourcePanel
			scope := backup.Scope{
				Sections: []backup.Section{backup.SectionAuditLog},
			}
			archive := backup.NewArchive(
				time.Now().UTC(),
				"test",
				backup.RealmLabel{Code: string(domain.DefaultRealm)},
				scope,
				backup.Data{Audit: []domain.AuditRow{entry}},
				backup.CredentialFormPlaintext,
			)

			if _, err := rs.RestoreBackup(
				ctx,
				archive,
				backup.RestoreOptions{
					Scope: scope, Mode: backup.RestoreModeOverwrite,
				},
			); err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			rows, err := rs.ListAudit(ctx, 10)
			if err != nil {
				t.Fatalf("ListAudit: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("restored audit rows = %+v, want one", rows)
			}
			got := rows[0]
			if got.ExternalID != entry.ExternalID ||
				got.Action != entry.Action ||
				got.OrderID != entry.OrderID ||
				got.Verdict != entry.Verdict ||
				got.RejectCode != entry.RejectCode {
				t.Fatalf("restored audit row = %+v, want %+v", got, entry)
			}
		})
	}
}

// TestBackupRestoreActivityOnlyForceIncludesSigningKey exports ONLY the activity
// section of a realm holding a signed order, then restores it into a fresh realm
// with the same scope. The signing key the event_attestation row references
// (RESTRICT) must be force-included via the parent general-settings section, so
// the foreign key resolves with no error.
func TestBackupRestoreActivityOnlyForceIncludesSigningKey(t *testing.T) {
	ctx, src := seedOrderFixtures(t)
	seedSigningKey(t, ctx, src, "key-activity")
	order, err := src.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	event, err := src.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: order.ExternalID, Type: domain.OrderEventSubmitted, Source: domain.SourcePanel,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if err := src.PutEventAttestation(ctx, event.ExternalID, domain.EventAttestation{
		Token:       "tok",
		KeyID:       "key-activity",
		Alg:         "ed25519",
		RequestType: domain.AttestationRequestSubmit,
		Mode:        "immediate",
		IssuedAt:    "2026-06-26T10:00:00Z",
	}); err != nil {
		t.Fatalf("PutEventAttestation: %v", err)
	}

	// Export only the activity section; Normalize force-includes the parent
	// dictionaries and general-settings (the signing keys) so the archive carries
	// the key the attestation references.
	archive, err := src.ExportBackup(ctx, backup.Scope{
		Sections: []backup.Section{backup.SectionActivityHistory},
	})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionActivityHistory}},
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup activity-only: %v", err)
	}

	// The signed order restored and its event attestation resolved the
	// force-included key.
	detail, err := dst.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder after activity-only restore: %v", err)
	}
	att := eventAttestation(t, detail, event.ExternalID)
	if att == nil || att.KeyID != "key-activity" {
		t.Fatalf("restored event attestation = %+v, want key-activity", att)
	}
}

// TestBackupRestoreAuditOnlyForceIncludedGroupDefersRestartClassification
// asserts that a restore whose requested scope names only the audit log can
// still land its force-included accounts/groups dictionary, while the store
// leaves engine replacement classification to the node.
func TestBackupRestoreAuditOnlyForceIncludedGroupDefersRestartClassification(
	t *testing.T,
) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	_, dst := newRealmStore(t, domain.DefaultRealm)
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}},
		Mode:  backup.RestoreModeInsertMissing,
	})
	if err != nil {
		t.Fatalf("RestoreBackup audit-only: %v", err)
	}
	if summary.RestartRequired {
		t.Fatalf("RestartRequired = true at store boundary, want node classification")
	}
	// The force-included group landed even though only the audit log was requested.
	if _, ok, err := dst.GetGroup(ctx, "grp-1"); err != nil || !ok {
		t.Fatalf("dst GetGroup(grp-1) ok=%v err=%v, want present", ok, err)
	}
}

// TestBackupRestoreAuditOnlyNoNewGroupStaysObservational asserts the restart
// signal stays off when an audit-only restore force-includes the accounts+groups
// dictionary but the target already holds every group, so no runtime row is
// written. The signal must follow the rows actually applied, not the mere
// presence of the force-included section.
func TestBackupRestoreAuditOnlyNoNewGroupStaysObservational(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// Restore the full archive first so the target already holds every dictionary,
	// then repeat an audit-only insert-missing restore: the force-included group
	// and account rows all collide and are skipped, so nothing runtime is written.
	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup seed full: %v", err)
	}
	summary, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}},
		Mode:  backup.RestoreModeInsertMissing,
	})
	if err != nil {
		t.Fatalf("RestoreBackup audit-only repeat: %v", err)
	}
	if summary.RestartRequired {
		t.Fatalf("RestartRequired = true for an audit-only restore that wrote no runtime row")
	}
}

func TestBackupRestoreValidatesManifestRealmCodeBeforeTransaction(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{name: "empty"},
		{name: "over_64_code_points", code: strings.Repeat("界", 65)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			_, dst := newRealmStore(t, domain.DefaultRealm)
			if err := dst.SetMcpAccess(ctx, "submit_order", true); err != nil {
				t.Fatalf("SetMcpAccess: %v", err)
			}
			scope := backup.Scope{
				Sections: []backup.Section{backup.SectionGeneralSettings},
			}
			archive := backup.NewArchive(
				time.Now(),
				"test",
				backup.RealmLabel{Code: test.code},
				scope,
				backup.Data{McpAccess: map[string]bool{"submit_order": false}},
				backup.CredentialFormPlaintext,
			)

			// A canceled context makes BeginTx fail. ErrInvalid must win, proving
			// manifest validation happens before the transaction is opened.
			restoreCtx, cancel := context.WithCancel(ctx)
			cancel()
			_, err := dst.RestoreBackup(restoreCtx, archive, backup.RestoreOptions{
				Scope: scope,
				Mode:  backup.RestoreModeReplaceAll,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf(
					"RestoreBackup(%s) error = %v, want manifest.realm.code ErrInvalid",
					test.name,
					err,
				)
			}
			if !strings.Contains(err.Error(), "manifest.realm.code") {
				t.Fatalf("RestoreBackup(%s) error = %v, want field name", test.name, err)
			}
			access, err := dst.ListMcpAccess(ctx)
			if err != nil {
				t.Fatalf("ListMcpAccess: %v", err)
			}
			if !access["submit_order"] {
				t.Fatalf("mcp access changed after rejected restore: %+v", access)
			}
		})
	}
}

// TestBackupRestoreFormatVersionRejected asserts an archive whose manifest format
// version mismatches the current one is rejected with ErrInvalid and writes
// nothing.
func TestBackupRestoreFormatVersionRejected(t *testing.T) {
	ctx := context.Background()
	_, src := newRealmStore(t, domain.DefaultRealm)
	seedRealm(t, ctx, src)
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	archive.Manifest.FormatVersion = backup.FormatVersion + 1

	_, dst := newRealmStore(t, domain.DefaultRealm)
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RestoreBackup(bad formatVersion) error = %v, want ErrInvalid", err)
	}
	if _, ok, _ := dst.GetAccount(ctx, "acc-1"); ok {
		t.Fatal("rejected restore still wrote rows")
	}
}

// mustExternalID mints a fresh external id for a test fixture.
func mustExternalID(t *testing.T) domain.ExternalID {
	t.Helper()
	xid, err := newExternalID()
	if err != nil {
		t.Fatalf("newExternalID: %v", err)
	}
	return xid
}

// instanceXID returns the single market-data instance's external id in rs.
func instanceXID(t *testing.T, ctx context.Context, rs fwstore.RealmStore) domain.ExternalID {
	t.Helper()
	instances, err := rs.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	return instances[0].ExternalID
}

// --- Assertions --------------------------------------------------------------

// assertNoIDLeak fails when the archive serializes any surrogate or engine id.
// Portable identity carries only codes and external ids; the archive account and
// group types have no engine-id field, so this checks the round-trippable JSON
// does not regress to leak one and that machine records expose external ids.
func assertNoIDLeak(t *testing.T, archive backup.Archive) {
	t.Helper()
	for _, rec := range archive.Data.Orders {
		if rec.Order.ExternalID.IsZero() {
			t.Fatal("exported order has zero external id")
		}
	}
	for _, a := range archive.Data.Accounts {
		if a.Code == "" {
			t.Fatal("exported account has empty code")
		}
	}
}

// assertRealmsEqualOnPublicIdentity compares the public identity of every section
// between src and dst: dictionaries by code, machine records by external id, and
// the money/quantity decimals through the domain value types.
func assertRealmsEqualOnPublicIdentity(
	t *testing.T, ctx context.Context, src, dst fwstore.RealmStore, orderXID domain.ExternalID,
) {
	t.Helper()

	srcAssets, _ := src.ListAssets(ctx)
	dstAssets, _ := dst.ListAssets(ctx)
	if !sameAssetCodes(srcAssets, dstAssets) {
		t.Fatalf("assets differ: %v vs %v", srcAssets, dstAssets)
	}

	srcAcc, _, _ := src.GetAccount(ctx, "acc-1")
	dstAcc, ok, err := dst.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("dst account: ok=%v err=%v", ok, err)
	}
	if srcAcc.Code != dstAcc.Code || srcAcc.GroupCode != dstAcc.GroupCode ||
		srcAcc.Title != dstAcc.Title || srcAcc.Notes != dstAcc.Notes ||
		srcAcc.PnlHaltReason != dstAcc.PnlHaltReason ||
		srcAcc.Blocked != dstAcc.Blocked || srcAcc.BlockReason != dstAcc.BlockReason ||
		srcAcc.BlockPolicy != dstAcc.BlockPolicy || srcAcc.BlockCode != dstAcc.BlockCode ||
		srcAcc.BlockDetails != dstAcc.BlockDetails {
		t.Fatalf("account public identity differs: %+v vs %+v", srcAcc, dstAcc)
	}

	srcBal, _, _ := src.GetBalance(ctx, "acc-1", "USD")
	dstBal, ok, _ := dst.GetBalance(ctx, "acc-1", "USD")
	if !ok || srcBal.Available != dstBal.Available ||
		srcBal.RealizedPnlHaltReason != dstBal.RealizedPnlHaltReason {
		t.Fatalf("balance differs: %+v vs %+v", srcBal, dstBal)
	}

	srcLimits, _ := src.ListOrderSizeLimits(ctx, "")
	dstLimits, _ := dst.ListOrderSizeLimits(ctx, "")
	if len(srcLimits) != len(dstLimits) || len(dstLimits) != 1 ||
		dstLimits[0].MaxQuantity != "100" || dstLimits[0].Account != "acc-1" {
		t.Fatalf("limits differ: %v vs %v", srcLimits, dstLimits)
	}
	srcSpotFunds, _ := src.ListSpotFundsPnlBoundsLimits(ctx, "")
	dstSpotFunds, _ := dst.ListSpotFundsPnlBoundsLimits(ctx, "")
	if len(srcSpotFunds) != len(dstSpotFunds) || len(dstSpotFunds) != 1 ||
		dstSpotFunds[0].Account != "acc-1" ||
		dstSpotFunds[0].Currency != "USD" ||
		dstSpotFunds[0].LowerBound != "-250" ||
		dstSpotFunds[0].UpperBound != "500" {
		t.Fatalf("spot funds pnl bounds differ: %v vs %v", srcSpotFunds, dstSpotFunds)
	}

	srcOrder, _ := src.GetOrder(ctx, orderXID)
	dstOrder, err := dst.GetOrder(ctx, orderXID)
	if err != nil {
		t.Fatalf("dst GetOrder by preserved external id: %v", err)
	}
	if srcOrder.Order.ExternalID != dstOrder.Order.ExternalID {
		t.Fatalf("order external id changed: %s vs %s",
			srcOrder.Order.ExternalID, dstOrder.Order.ExternalID)
	}
	if srcOrder.Order.AmountValue != dstOrder.Order.AmountValue ||
		srcOrder.Order.Price != dstOrder.Order.Price ||
		!srcOrder.Order.DropCopy || !dstOrder.Order.DropCopy {
		t.Fatalf("order decimals differ: %+v vs %+v", srcOrder.Order, dstOrder.Order)
	}
	if len(dstOrder.Events) != 1 || len(dstOrder.Trades) != 1 {
		t.Fatalf("order children not preserved: %+v", dstOrder)
	}
	if dstOrder.Events[0].ExternalID != srcOrder.Events[0].ExternalID {
		t.Fatalf("event external id changed: %s vs %s",
			dstOrder.Events[0].ExternalID, srcOrder.Events[0].ExternalID)
	}
	if dstOrder.Trades[0].ExternalID != srcOrder.Trades[0].ExternalID {
		t.Fatalf("trade external id changed: %s vs %s",
			dstOrder.Trades[0].ExternalID, srcOrder.Trades[0].ExternalID)
	}
	if dstOrder.Trades[0].Commission == nil ||
		dstOrder.Trades[0].Commission.Amount != "-0.12" ||
		dstOrder.Trades[0].Commission.Currency != "USD" {
		t.Fatalf("trade commission not preserved: %+v", dstOrder.Trades[0])
	}

	srcAdj, _ := src.ListAdjustments(ctx, "acc-1", "", 10)
	dstAdj, _ := dst.ListAdjustments(ctx, "acc-1", "", 10)
	if len(srcAdj) != 1 || len(dstAdj) != 1 ||
		srcAdj[0].ExternalID != dstAdj[0].ExternalID {
		t.Fatalf("adjustment external id not preserved: %v vs %v", srcAdj, dstAdj)
	}

	srcAudit, _ := src.ListAudit(ctx, 100)
	dstAudit, _ := dst.ListAudit(ctx, 100)
	if len(dstAudit) != len(srcAudit) {
		t.Fatalf("audit count differs: %d vs %d", len(srcAudit), len(dstAudit))
	}
	// The seeded audit row plus the create-* rows the seed CRUD emitted are all
	// machine records carried by external id; check the seed row is present.
	if !auditHasExternalID(dstAudit, srcAudit) {
		t.Fatal("audit external ids not preserved across restore")
	}

	srcUser, _ := src.ListUserSettings(ctx)
	dstUser, _ := dst.ListUserSettings(ctx)
	if len(srcUser) != len(dstUser) || len(dstUser) != 1 || dstUser[0].Value != "1" {
		t.Fatalf("user settings differ: %v vs %v", srcUser, dstUser)
	}
}

func sameAssetCodes(a, b []domain.Asset) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[x.Code] = true
	}
	for _, x := range b {
		if !set[x.Code] {
			return false
		}
	}
	return true
}

func auditHasExternalID(got, want []domain.AuditRow) bool {
	set := make(map[domain.ExternalID]bool, len(got))
	for _, row := range got {
		set[row.ExternalID] = true
	}
	for _, row := range want {
		if !set[row.ExternalID] {
			return false
		}
	}
	return true
}
