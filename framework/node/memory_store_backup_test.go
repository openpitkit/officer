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

package node

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (r *memoryRealm) ExportBackup(
	ctx context.Context, scope backup.Scope,
) (backup.Archive, error) {
	return backup.NewArchive(
		time.Now().UTC(),
		"memory",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		r.exportData(ctx),
		backup.CredentialFormPlaintext,
	), nil
}

func (r *memoryRealm) RestoreBackup(
	ctx context.Context, archive backup.Archive, opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	if opts.Mode == "" {
		return backup.RestoreSummary{}, domain.ErrInvalid
	}
	if err := backup.ValidateCredentialForm(archive.CredentialForm); err != nil {
		return backup.RestoreSummary{}, err
	}
	data := archive.Data
	if err := backup.ValidateAccountBlocks(data.Accounts); err != nil {
		return backup.RestoreSummary{}, err
	}
	// Mirror the real store: the normalized scope drives whether runtime data can
	// land (Normalize force-includes the accounts+groups and market-data parents an
	// account-addressed restore lands). The node, not the store, classifies whether
	// the committed delta requires an engine replacement.
	writesRuntime := opts.Scope.All || backup.TouchesRuntime(opts.Scope.Normalize())
	if writesRuntime {
		previousAccounts := r.accounts
		previousGroups := r.groups
		// Runtime sections replace data except assets, which restore by code to
		// match SQLite's non-pruning contract.
		r.restoreDataWithCredentialForm(data, opts.Mode, archive.CredentialForm)
		// The real connector updates dictionary rows by code and preserves their
		// stable engine ids in every restore mode. Keep the memory connector honest
		// about that identity contract so online-restore tests do not manufacture an
		// id change which SQLite never makes for a surviving code.
		for code, account := range r.accounts {
			if previous, ok := previousAccounts[code]; ok {
				account.EngineAccountID = previous.EngineAccountID
				r.accounts[code] = account
			}
		}
		for code, group := range r.groups {
			if code == "" {
				group.EngineGroupID = 0
				r.groups[code] = group
				continue
			}
			if previous, ok := previousGroups[code]; ok {
				group.EngineGroupID = previous.EngineGroupID
				r.groups[code] = group
			}
		}
	} else {
		r.restoreAssets(data.Assets, opts.Mode)
		if opts.Scope.Normalize().Included(backup.SectionGeneralSettings) {
			r.restoreSigningKeys(data.SigningKeys, opts.Mode)
			r.signingConfig = map[string]string{}
			for _, entry := range data.SigningConfig {
				r.signingConfig[entry.Key] = entry.Value
			}
		}
		for key := range r.mcpAccess {
			delete(r.mcpAccess, key)
		}
		for key, value := range data.McpAccess {
			r.mcpAccess[key] = value
		}
		for _, setting := range data.UserSettings {
			r.userSettings[settingKey(setting.UserID, setting.Key)] = setting
		}
	}
	summary := backup.NewSummary()
	for _, section := range opts.Scope.Normalize().IncludedSections() {
		count := r.sectionCount(ctx, section)
		summary.AddApplied(section, count)
	}
	return summary, nil
}

func (r *memoryRealm) exportData(context.Context) backup.Data {
	data := backup.Data{
		Assets:          make([]backup.Asset, 0, len(r.assets)),
		Principals:      make([]domain.Principal, 0, len(r.principals)),
		Groups:          make([]backup.AccountGroup, 0, len(r.groups)),
		Accounts:        make([]backup.Account, 0, len(r.accounts)),
		Balances:        make([]backup.Balance, 0, len(r.balances)),
		RateLimits:      make([]domain.LimitRate, 0, len(r.rateLimits)),
		OrderSizeLimits: make([]domain.LimitOrderSize, 0, len(r.orderSizeLimits)),
		SpotFundsPnlBoundsLimits: make(
			[]domain.LimitSpotFundsPnlBounds,
			0,
			len(r.spotFundsPnlBoundsLimits),
		),
		Adjustments:           append([]domain.AccountAdjustmentRecord(nil), r.adjustments...),
		Orders:                make([]backup.OrderRecord, 0, len(r.orders)),
		OrderEvents:           r.exportEvents(),
		Trades:                append([]domain.Trade(nil), r.trades...),
		Audit:                 append([]domain.AuditRow(nil), r.audit...),
		MarketDataInstances:   make([]backup.MarketDataInstance, 0, len(r.instances)),
		MarketDataInstruments: make([]backup.MarketDataInstrument, 0, len(r.instruments)),
		SigningKeys:           make([]backup.SigningKey, 0, len(r.signingKeys)),
		SigningConfig:         make([]backup.SigningConfigEntry, 0, len(r.signingConfig)),
		McpAccess:             make(map[string]bool, len(r.mcpAccess)),
		UserSettings:          make([]domain.UserSetting, 0, len(r.userSettings)),
	}
	for _, asset := range r.assets {
		data.Assets = append(data.Assets, backup.Asset{
			Code: asset.Code, Title: asset.Title, AssetClass: asset.AssetClass,
		})
	}
	for _, principal := range r.principals {
		data.Principals = append(data.Principals, principal)
	}
	for _, group := range r.groups {
		if group.Code == "" {
			data.DefaultGroupCurrency = group.Currency
			continue
		}
		data.Groups = append(data.Groups, backup.AccountGroup{
			Code: group.Code, Title: group.Title, Currency: group.Currency, Notes: group.Notes,
			BlockReason: group.BlockReason, Blocked: group.Blocked,
		})
	}
	for _, account := range r.accounts {
		data.Accounts = append(data.Accounts, backup.Account{
			Code: string(account.Code), Title: account.Title, Pnl: account.Pnl,
			PnlHaltReason: account.PnlHaltReason,
			Currency:      account.Currency,
			GroupCode:     account.GroupCode, Notes: account.Notes,
			BlockReason: account.BlockReason, BlockPolicy: account.BlockPolicy,
			BlockCode: account.BlockCode, BlockDetails: account.BlockDetails,
			Blocked: account.Blocked,
		})
	}
	for _, balance := range r.balances {
		data.Balances = append(data.Balances, backup.Balance{
			UpdatedAt:             balance.UpdatedAt,
			Available:             balance.Available,
			Held:                  balance.Held,
			Incoming:              balance.Incoming,
			RealizedPnl:           balance.RealizedPnl,
			RealizedPnlHaltReason: balance.RealizedPnlHaltReason,
			AverageEntryPrice:     balance.AverageEntryPrice,
			Asset:                 balance.Asset,
			Account:               balance.Account,
		})
	}
	for _, limit := range r.rateLimits {
		data.RateLimits = append(data.RateLimits, limit)
	}
	for _, limit := range r.orderSizeLimits {
		data.OrderSizeLimits = append(data.OrderSizeLimits, limit)
	}
	for _, limit := range r.spotFundsPnlBoundsLimits {
		data.SpotFundsPnlBoundsLimits = append(data.SpotFundsPnlBoundsLimits, limit)
	}
	for _, order := range r.orders {
		data.Orders = append(data.Orders, backup.OrderRecord{Order: order})
	}
	for _, instance := range r.instances {
		data.MarketDataInstances = append(data.MarketDataInstances,
			backup.MarketDataInstance{
				ExternalID:  instance.ExternalID,
				Provider:    instance.Provider,
				Label:       instance.Label,
				Credentials: []byte(instance.Credentials),
				Enabled:     instance.Enabled,
			},
		)
	}
	for _, instrument := range r.instruments {
		data.MarketDataInstruments = append(data.MarketDataInstruments,
			backup.MarketDataInstrument{
				Instance:       instrument.Instance,
				ExternalSymbol: instrument.ExternalSymbol,
				BaseAsset:      instrument.BaseAsset,
				QuoteAsset:     instrument.QuoteAsset,
				ManualPrice:    instrument.ManualPrice,
				Enabled:        instrument.Enabled,
			},
		)
	}
	for _, key := range r.signingKeys {
		data.SigningKeys = append(data.SigningKeys, backup.SigningKey{
			CreatedAt: key.CreatedAt, KeyID: key.KeyID, Alg: key.Alg,
			PublicKey: key.PublicKey, Active: key.Active,
		})
	}
	for key, value := range r.signingConfig {
		data.SigningConfig = append(data.SigningConfig,
			backup.SigningConfigEntry{Key: key, Value: value})
	}
	for key, value := range r.mcpAccess {
		data.McpAccess[key] = value
	}
	for _, setting := range r.userSettings {
		data.UserSettings = append(data.UserSettings, setting)
	}
	return data
}

func (r *memoryRealm) restoreAssets(
	assets []backup.Asset,
	mode backup.RestoreMode,
) {
	for _, asset := range assets {
		stored, ok := r.assets[asset.Code]
		if ok && mode == backup.RestoreModeInsertMissing {
			continue
		}
		if !ok {
			r.nextAssetID++
			stored.EngineAssetID = r.nextAssetID
		}
		r.assets[asset.Code] = domain.Asset{
			Code:          asset.Code,
			Title:         asset.Title,
			AssetClass:    asset.AssetClass,
			EngineAssetID: stored.EngineAssetID,
		}
	}
}

func (r *memoryRealm) restoreData(data backup.Data, mode backup.RestoreMode) {
	r.restoreDataWithCredentialForm(data, mode, backup.CredentialFormPlaintext)
}

func (r *memoryRealm) restoreDataWithCredentialForm(
	data backup.Data, mode backup.RestoreMode, credentialForm string,
) {
	defaultGroup, hadDefaultGroup := r.groups[""]
	previousAssets := r.assets
	r.assets = make(map[string]domain.Asset, len(previousAssets))
	for code, asset := range previousAssets {
		r.assets[code] = asset
	}
	r.restoreAssets(data.Assets, mode)
	r.principals = map[string]domain.Principal{}
	for _, principal := range data.Principals {
		r.principals[principal.Code] = principal
	}
	r.groups = map[string]domain.AccountGroup{}
	for _, group := range data.Groups {
		r.nextGroupID++
		r.groups[group.Code] = domain.AccountGroup{
			Code: group.Code, Title: group.Title, Currency: group.Currency, Notes: group.Notes,
			BlockReason: group.BlockReason, Blocked: group.Blocked,
			EngineGroupID: domain.EngineGroupID(r.nextGroupID),
		}
	}
	if hadDefaultGroup || data.DefaultGroupCurrency != "" {
		defaultGroup.Code = ""
		defaultGroup.Currency = data.DefaultGroupCurrency
		defaultGroup.EngineGroupID = 0
		r.groups[""] = defaultGroup
	}
	r.accounts = map[domain.AccountID]domain.Account{}
	for _, account := range data.Accounts {
		pnl := account.Pnl
		if pnl == "" {
			pnl = "0"
		}
		r.nextAccountID++
		code := domain.AccountID(account.Code)
		r.accounts[code] = domain.Account{
			Code: code, Title: account.Title, Pnl: pnl, PnlHaltReason: account.PnlHaltReason,
			Currency: account.Currency, GroupCode: account.GroupCode,
			Notes: account.Notes, BlockReason: account.BlockReason,
			BlockPolicy: account.BlockPolicy, BlockCode: account.BlockCode,
			BlockDetails: account.BlockDetails,
			Blocked:      account.Blocked, EngineAccountID: r.nextAccountID,
		}
	}
	r.balances = map[string]domain.Balance{}
	for _, balance := range data.Balances {
		r.balances[balanceKey(balance.Account, balance.Asset)] = domain.Balance{
			UpdatedAt:             balance.UpdatedAt,
			Available:             balance.Available,
			Held:                  balance.Held,
			Incoming:              balance.Incoming,
			RealizedPnl:           balance.RealizedPnl,
			RealizedPnlHaltReason: balance.RealizedPnlHaltReason,
			AverageEntryPrice:     balance.AverageEntryPrice,
			Asset:                 balance.Asset,
			Account:               balance.Account,
		}
	}
	r.rateLimits = map[string]domain.LimitRate{}
	for _, limit := range data.RateLimits {
		r.rateLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	}
	r.orderSizeLimits = map[string]domain.LimitOrderSize{}
	for _, limit := range data.OrderSizeLimits {
		r.orderSizeLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	}
	r.spotFundsPnlBoundsLimits = map[string]domain.LimitSpotFundsPnlBounds{}
	for _, limit := range data.SpotFundsPnlBoundsLimits {
		r.spotFundsPnlBoundsLimits[spotFundsPnlBoundsLimitKey(
			limit.Scope,
			limit.Account,
			limit.AccountGroup,
		)] = limit
	}
	r.adjustments = append([]domain.AccountAdjustmentRecord(nil), data.Adjustments...)
	r.orders = map[domain.ExternalID]domain.Order{}
	for _, record := range data.Orders {
		r.orders[record.Order.ExternalID] = record.Order
	}
	r.attestations = map[domain.ExternalID]domain.EventAttestation{}
	r.events = r.events[:0]
	for _, event := range data.OrderEvents {
		if event.Attestation != nil {
			r.attestations[event.ExternalID] = *event.Attestation
			event.Attestation = nil
		}
		r.events = append(r.events, event)
	}
	r.trades = append([]domain.Trade(nil), data.Trades...)
	r.audit = append([]domain.AuditRow(nil), data.Audit...)
	r.instances = map[domain.ExternalID]domain.MarketDataInstance{}
	for _, instance := range data.MarketDataInstances {
		credentials := ""
		enabled := false
		if credentialForm == backup.CredentialFormPlaintext {
			credentials = string(instance.Credentials)
			enabled = instance.Enabled
		}
		r.instances[instance.ExternalID] = domain.MarketDataInstance{
			ExternalID:  instance.ExternalID,
			Provider:    instance.Provider,
			Label:       instance.Label,
			Credentials: credentials,
			Enabled:     enabled,
		}
	}
	r.instruments = map[string]domain.MarketDataInstrument{}
	for _, instrument := range data.MarketDataInstruments {
		base, baseOK := r.assets[instrument.BaseAsset]
		quote, quoteOK := r.assets[instrument.QuoteAsset]
		stored := domain.MarketDataInstrument{
			Instance:       instrument.Instance,
			ExternalSymbol: instrument.ExternalSymbol,
			BaseAsset:      instrument.BaseAsset,
			QuoteAsset:     instrument.QuoteAsset,
			ManualPrice:    instrument.ManualPrice,
			Enabled:        instrument.Enabled,
		}
		if baseOK {
			stored.BaseAssetID = base.EngineAssetID
		}
		if quoteOK {
			stored.QuoteAssetID = quote.EngineAssetID
		}
		r.instruments[instrumentKey(instrument.Instance, instrument.ExternalSymbol)] = stored
	}
	r.restoreSigningKeys(data.SigningKeys, mode)
	r.signingConfig = map[string]string{}
	for _, entry := range data.SigningConfig {
		r.signingConfig[entry.Key] = entry.Value
	}
	r.mcpAccess = map[string]bool{}
	for key, value := range data.McpAccess {
		r.mcpAccess[key] = value
	}
	r.userSettings = map[string]domain.UserSetting{}
	for _, setting := range data.UserSettings {
		r.userSettings[settingKey(setting.UserID, setting.Key)] = setting
	}
}

func (r *memoryRealm) restoreSigningKeys(
	keys []backup.SigningKey, mode backup.RestoreMode,
) {
	target := r.signingKeys
	if mode == backup.RestoreModeReplaceAll {
		target = make(map[string]domain.SigningKey, len(keys))
	}
	for _, key := range keys {
		existing, exists := r.signingKeys[key.KeyID]
		if exists && mode == backup.RestoreModeInsertMissing {
			continue
		}
		if exists {
			existing.CreatedAt = key.CreatedAt
			existing.Alg = key.Alg
			existing.PublicKey = append([]byte(nil), key.PublicKey...)
			target[key.KeyID] = existing
			continue
		}
		target[key.KeyID] = domain.SigningKey{
			CreatedAt:  key.CreatedAt,
			KeyID:      key.KeyID,
			Alg:        key.Alg,
			PrivateKey: []byte{},
			PublicKey:  append([]byte(nil), key.PublicKey...),
			Active:     false,
		}
	}
	r.signingKeys = target
}

func (r *memoryRealm) sectionCount(_ context.Context, section backup.Section) int {
	switch section {
	case backup.SectionAccountsGroups:
		return len(r.groups) + len(r.accounts)
	case backup.SectionPositions:
		return len(r.balances)
	case backup.SectionRiskLimits:
		return len(r.rateLimits) + len(r.orderSizeLimits) + len(r.spotFundsPnlBoundsLimits)
	case backup.SectionMarketData:
		return len(r.instances) + len(r.instruments)
	case backup.SectionGeneralSettings:
		return len(r.mcpAccess) + len(r.signingConfig)
	case backup.SectionUserSettings:
		return len(r.userSettings)
	case backup.SectionActivityHistory:
		return len(r.adjustments) + len(r.orders) + len(r.events) + len(r.trades)
	case backup.SectionAuditLog:
		return len(r.audit)
	default:
		return 0
	}
}

func TestMemoryRealmRestoreAssetsHonorsMode(t *testing.T) {
	t.Parallel()
	modes := []struct {
		name              string
		mode              backup.RestoreMode
		wantExistingTitle string
		wantExistingClass string
	}{
		{
			name: "insert missing", mode: backup.RestoreModeInsertMissing,
			wantExistingTitle: "old title", wantExistingClass: "old-class",
		},
		{
			name: "overwrite", mode: backup.RestoreModeOverwrite,
			wantExistingTitle: "new title", wantExistingClass: "new-class",
		},
		{
			name: "replace all", mode: backup.RestoreModeReplaceAll,
			wantExistingTitle: "new title", wantExistingClass: "new-class",
		},
	}
	paths := []struct {
		name  string
		scope backup.Scope
	}{
		{
			name: "non runtime",
			scope: backup.Scope{Sections: []backup.Section{
				backup.SectionGeneralSettings,
			}},
		},
		{name: "runtime", scope: backup.Scope{All: true}},
	}
	for _, path := range paths {
		for _, mode := range modes {
			t.Run(path.name+"/"+mode.name, func(t *testing.T) {
				ctx := context.Background()
				realm := newMemoryStore(
					"restore-assets-" + path.name + "-" + string(mode.mode) + ".db",
				).realm
				for _, class := range []string{"old-class", "new-class"} {
					if err := realm.CreateAssetClass(
						ctx,
						domain.AssetClass{Code: class},
					); err != nil {
						t.Fatalf("CreateAssetClass(%s): %v", class, err)
					}
				}
				existing, err := realm.CreateAsset(ctx, domain.Asset{
					Code: "existing", Title: "old title", AssetClass: "old-class",
				})
				if err != nil {
					t.Fatalf("CreateAsset(existing): %v", err)
				}

				_, err = realm.RestoreBackup(
					ctx,
					testArchive(
						backup.Scope{Sections: []backup.Section{
							backup.SectionGeneralSettings,
						}},
						backup.Data{Assets: []backup.Asset{
							{
								Code: "existing", Title: "new title",
								AssetClass: "new-class",
							},
							{
								Code: "missing", Title: "missing title",
								AssetClass: "new-class",
							},
						}},
					),
					backup.RestoreOptions{Scope: path.scope, Mode: mode.mode},
				)
				if err != nil {
					t.Fatalf("RestoreBackup: %v", err)
				}

				gotExisting, ok, err := realm.GetAsset(ctx, existing.Code)
				if err != nil || !ok {
					t.Fatalf(
						"GetAsset(existing) = %+v, ok=%v, err=%v",
						gotExisting,
						ok,
						err,
					)
				}
				if gotExisting.Title != mode.wantExistingTitle ||
					gotExisting.AssetClass != mode.wantExistingClass ||
					gotExisting.EngineAssetID != existing.EngineAssetID {
					t.Fatalf(
						"existing asset = %+v, want title=%q class=%q id=%d",
						gotExisting,
						mode.wantExistingTitle,
						mode.wantExistingClass,
						existing.EngineAssetID,
					)
				}
				gotMissing, ok, err := realm.GetAsset(ctx, "missing")
				if err != nil || !ok {
					t.Fatalf(
						"GetAsset(missing) = %+v, ok=%v, err=%v",
						gotMissing,
						ok,
						err,
					)
				}
				if gotMissing.Title != "missing title" ||
					gotMissing.AssetClass != "new-class" ||
					gotMissing.EngineAssetID == 0 ||
					gotMissing.EngineAssetID == existing.EngineAssetID {
					t.Fatalf(
						"missing asset = %+v, want inserted with a fresh stable id",
						gotMissing,
					)
				}
			})
		}
	}
}

func TestLocalNodeRestoreRollbackPreservesActiveSigningPrivateMaterial(t *testing.T) {
	ctx := context.Background()
	real := newMemoryStore("restore-signing-rollback.db")
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	privateKey := bytes.Repeat([]byte{0x51}, 32)
	publicKey := bytes.Repeat([]byte{0x52}, 32)
	if err := realm.UpsertSigningKey(ctx, domain.SigningKey{
		KeyID: "target-active", Alg: "ed25519", PrivateKey: privateKey,
		PublicKey: publicKey, Active: true,
	}); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}
	wrapped := newRealmWrapStore(real, func(inner store.RealmStore) store.RealmStore {
		return &failRestoreAuditRealm{RealmStore: inner}
	})
	n := newTestNodeWithStore(t, wrapped, newFakeEngine(), failOnFatal(t))
	archive := backup.NewArchive(
		time.Now(), "test", backup.RealmLabel{Code: string(domain.DefaultRealm)},
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{SigningKeys: []backup.SigningKey{{
			KeyID: "incoming", Alg: "ed25519",
			PublicKey: bytes.Repeat([]byte{0x53}, 32), Active: true,
		}}},
		backup.CredentialFormPlaintext,
	)
	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want injected post-commit audit failure")
	}
	active, ok, err := realm.GetActiveSigningKey(ctx)
	if err != nil || !ok {
		t.Fatalf("GetActiveSigningKey after rollback: ok=%v err=%v", ok, err)
	}
	if active.KeyID != "target-active" || !active.Active ||
		!bytes.Equal(active.PrivateKey, privateKey) {
		t.Fatalf("active key after rollback = %+v, want byte-exact target material", active)
	}
	if _, err := realm.GetSigningKey(ctx, "incoming"); err == nil {
		t.Fatal("rollback retained signing key introduced by failed restore")
	}
}

func TestLocalNodeRestoreRollbackRefusesAfterReplaceAllPrunesSigningKey(t *testing.T) {
	ctx := context.Background()
	real := newMemoryStore("restore-signing-rollback-prune.db")
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	privateKey := bytes.Repeat([]byte{0x61}, 32)
	if err := realm.UpsertSigningKey(ctx, domain.SigningKey{
		KeyID:      "target-active",
		Alg:        "ed25519",
		PrivateKey: privateKey,
		PublicKey:  bytes.Repeat([]byte{0x62}, 32),
		Active:     true,
	}); err != nil {
		t.Fatalf("UpsertSigningKey: %v", err)
	}

	wrapped := newRealmWrapStore(real, func(inner store.RealmStore) store.RealmStore {
		return &failRestoreAuditRealm{RealmStore: inner}
	})
	engine := newFakeEngine()
	n := newTestNodeWithStore(t, wrapped, engine, failOnFatal(t))
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	archive := backup.NewArchive(
		time.Now(),
		"test",
		backup.RealmLabel{Code: "foreign-realm"},
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{SigningKeys: []backup.SigningKey{{
			KeyID:     "incoming",
			Alg:       "ed25519",
			PublicKey: bytes.Repeat([]byte{0x63}, 32),
			Active:    true,
		}}},
		backup.CredentialFormPlaintext,
	)
	_, _, err = n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want injected post-commit audit failure")
	}
	if !errors.Is(err, errRestoreAuditFailed) {
		t.Fatalf("RestoreBackup error = %v, want audit failure", err)
	}
	for _, want := range []string{
		"rollback not attempted",
		"target-active",
		"archive cannot carry",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RestoreBackup error = %v, want %q", err, want)
		}
	}
	if fatalErr == nil || !errors.Is(fatalErr, errRestoreAuditFailed) {
		t.Fatalf("fatal error = %v, want reconciliation with audit failure", fatalErr)
	}
	if !engine.running {
		t.Fatal("engine stopped for non-runtime failed restore")
	}

	if key, err := realm.GetSigningKey(ctx, "target-active"); err == nil {
		t.Fatalf("target signing key was rewritten by unsafe rollback: %+v", key)
	}
	incoming, err := realm.GetSigningKey(ctx, "incoming")
	if err != nil {
		t.Fatalf("incoming signing key missing after refused rollback: %v", err)
	}
	if incoming.Active || len(incoming.PrivateKey) != 0 {
		t.Fatalf("incoming signing key = %+v, want committed verify-only inactive key", incoming)
	}
}
