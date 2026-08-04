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
	"context"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
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
	), nil
}

func (r *memoryRealm) RestoreBackup(
	ctx context.Context, archive backup.Archive, opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	if opts.Mode == "" {
		return backup.RestoreSummary{}, domain.ErrInvalid
	}
	data := archive.Data
	// Mirror the real store: the normalized scope drives whether runtime data can
	// land (Normalize force-includes the accounts+groups and market-data parents an
	// account-addressed restore lands). The node, not the store, classifies whether
	// the committed delta requires an engine replacement.
	writesRuntime := opts.Scope.All || backup.TouchesRuntime(opts.Scope.Normalize())
	if writesRuntime {
		previousAccounts := r.accounts
		previousGroups := r.groups
		// Runtime sections are replaced wholesale in the test store. This is
		// enough for LocalNode rollback/rebuild tests and keeps the fake honest
		// about the restart signal.
		r.restoreData(data)
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
		Assets:          make([]domain.Asset, 0, len(r.assets)),
		Principals:      make([]domain.Principal, 0, len(r.principals)),
		Groups:          make([]backup.AccountGroup, 0, len(r.groups)),
		Accounts:        make([]backup.Account, 0, len(r.accounts)),
		Balances:        make([]domain.Balance, 0, len(r.balances)),
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
		MarketDataInstances:   make([]domain.MarketDataInstance, 0, len(r.instances)),
		MarketDataInstruments: make([]domain.MarketDataInstrument, 0, len(r.instruments)),
		MarketDataQuotes:      make([]domain.MarketDataQuote, 0, len(r.quotes)),
		SigningKeys:           make([]backup.SigningKey, 0, len(r.signingKeys)),
		SigningConfig:         make([]backup.SigningConfigEntry, 0, len(r.signingConfig)),
		McpAccess:             make(map[string]bool, len(r.mcpAccess)),
		UserSettings:          make([]domain.UserSetting, 0, len(r.userSettings)),
	}
	for _, asset := range r.assets {
		data.Assets = append(data.Assets, asset)
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
			BlockReason: account.BlockReason, Blocked: account.Blocked,
		})
	}
	for _, balance := range r.balances {
		data.Balances = append(data.Balances, balance)
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
		data.MarketDataInstances = append(data.MarketDataInstances, instance)
	}
	for _, instrument := range r.instruments {
		data.MarketDataInstruments = append(data.MarketDataInstruments, instrument)
	}
	for _, quote := range r.quotes {
		data.MarketDataQuotes = append(data.MarketDataQuotes, quote)
	}
	for _, key := range r.signingKeys {
		data.SigningKeys = append(data.SigningKeys, backup.SigningKey{
			CreatedAt: key.CreatedAt, KeyID: key.KeyID, Alg: key.Alg,
			PublicKey: key.PublicKey, PrivateKey: key.PrivateKey,
			Active: key.Active,
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

func (r *memoryRealm) restoreData(data backup.Data) {
	defaultGroup, hadDefaultGroup := r.groups[""]
	r.assets = map[string]domain.Asset{}
	for _, asset := range data.Assets {
		r.assets[asset.Code] = asset
	}
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
			Blocked: account.Blocked, EngineAccountID: r.nextAccountID,
		}
	}
	r.balances = map[string]domain.Balance{}
	for _, balance := range data.Balances {
		r.balances[balanceKey(balance.Account, balance.Asset)] = balance
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
		r.instances[instance.ExternalID] = instance
	}
	r.instruments = map[string]domain.MarketDataInstrument{}
	for _, instrument := range data.MarketDataInstruments {
		r.instruments[instrumentKey(instrument.Instance, instrument.ExternalSymbol)] = instrument
	}
	r.quotes = map[string]domain.MarketDataQuote{}
	for _, quote := range data.MarketDataQuotes {
		r.quotes[instrumentKey(quote.Instance, quote.ExternalSymbol)] = quote
	}
	r.signingKeys = map[string]domain.SigningKey{}
	for _, key := range data.SigningKeys {
		r.signingKeys[key.KeyID] = domain.SigningKey{
			CreatedAt: key.CreatedAt, KeyID: key.KeyID, Alg: key.Alg,
			PublicKey: key.PublicKey, PrivateKey: key.PrivateKey,
			Active: key.Active,
		}
	}
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
	case backup.SectionMarketDataQuotes:
		return len(r.quotes)
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
