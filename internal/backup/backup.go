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

// Package backup defines Pit Officer's portable backup archive contract.
package backup

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// Format is the stable identifier of the portable Officer backup file family.
const Format = "openpit.officer.backup"

// CurrentFormatVersion is the newest archive format this build writes.
const CurrentFormatVersion = 1

// MinSupportedFormatVersion is the oldest archive format this build can read.
const MinSupportedFormatVersion = 1

// Section identifies a logical backup/restore group.
type Section string

const (
	SectionAccountsGroups   Section = "accounts_groups"
	SectionPositions        Section = "positions"
	SectionRiskLimits       Section = "risk_limits"
	SectionMarketData       Section = "market_data_settings"
	SectionMarketDataQuotes Section = "market_data_quotes"
	SectionGeneralSettings  Section = "general_settings"
	SectionActivityHistory  Section = "activity_history"
	SectionAuditLog         Section = "audit_log"
)

// AllSections is the full ordered section list.
var AllSections = []Section{
	SectionAccountsGroups,
	SectionPositions,
	SectionRiskLimits,
	SectionMarketData,
	SectionMarketDataQuotes,
	SectionGeneralSettings,
	SectionActivityHistory,
	SectionAuditLog,
}

// RestoreMode defines how incoming rows interact with existing rows.
type RestoreMode string

const (
	RestoreModeReplaceAll    RestoreMode = "replace_all"
	RestoreModeOverwrite     RestoreMode = "overwrite"
	RestoreModeInsertMissing RestoreMode = "insert_missing"
)

// EntitySelector narrows account-addressed data by account ids and groups.
type EntitySelector struct {
	Accounts []string `json:"accounts,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	All      bool     `json:"all"`
}

// Scope describes which parts of an archive are included.
type Scope struct {
	Sections  []Section      `json:"sections,omitempty"`
	Accounts  EntitySelector `json:"accounts,omitempty"`
	Positions EntitySelector `json:"positions,omitempty"`
	All       bool           `json:"all"`
}

// Archive is the single JSON file copied between Officer installations.
type Archive struct {
	Manifest Manifest `json:"manifest"`
	Data     Data     `json:"data"`
}

// Manifest carries archive metadata independent of the concrete database.
type Manifest struct {
	CreatedAt     time.Time `json:"createdAt"`
	Format        string    `json:"format"`
	Source        string    `json:"source"`
	SchemaVersion int       `json:"schemaVersion"`
	FormatVersion int       `json:"formatVersion"`
	Sections      []Section `json:"sections"`
}

// Data carries portable domain rows. It intentionally contains no SQL.
type Data struct {
	McpAccess             map[string]bool                  `json:"mcpAccess,omitempty"`
	Accounts              []domain.Account                 `json:"accounts,omitempty"`
	Groups                []domain.AccountGroup            `json:"groups,omitempty"`
	Limits                []domain.Limit                   `json:"limits,omitempty"`
	Balances              []domain.Balance                 `json:"balances,omitempty"`
	Adjustments           []domain.AccountAdjustmentRecord `json:"adjustments,omitempty"`
	Orders                []domain.Order                   `json:"orders,omitempty"`
	OrderEvents           []domain.OrderEvent              `json:"orderEvents,omitempty"`
	Trades                []domain.Trade                   `json:"trades,omitempty"`
	Audit                 []domain.AuditRow                `json:"audit,omitempty"`
	MarketDataInstances   []domain.MarketDataInstance      `json:"marketDataInstances,omitempty"`
	MarketDataInstruments []domain.MarketDataInstrument    `json:"marketDataInstruments,omitempty"`
	MarketDataQuotes      []domain.MarketDataQuote         `json:"marketDataQuotes,omitempty"`
}

// RestoreOptions configures a restore operation.
type RestoreOptions struct {
	Scope Scope       `json:"scope"`
	Mode  RestoreMode `json:"mode"`
}

// RestoreSummary reports restore outcome counts by section.
type RestoreSummary struct {
	Applied         map[Section]int `json:"applied"`
	Skipped         map[Section]int `json:"skipped"`
	RestartRequired bool            `json:"restartRequired"`
}

// NewSummary returns an initialized restore summary.
func NewSummary() RestoreSummary {
	return RestoreSummary{
		Applied: make(map[Section]int),
		Skipped: make(map[Section]int),
	}
}

// AddApplied increments the applied count for section.
func (s RestoreSummary) AddApplied(section Section, n int) {
	if n > 0 {
		s.Applied[section] += n
	}
}

// AddSkipped increments the skipped count for section.
func (s RestoreSummary) AddSkipped(section Section, n int) {
	if n > 0 {
		s.Skipped[section] += n
	}
}

// Filename returns the conventional backup filename for createdAt.
func Filename(createdAt time.Time) string {
	return "pit-officer-backup-" +
		createdAt.UTC().Format("20060102T150405Z") + ".json"
}

// NewArchive builds a current-version archive from data and metadata.
func NewArchive(
	createdAt time.Time,
	source string,
	schemaVersion int,
	scope Scope,
	data Data,
) Archive {
	scope = scope.Normalize()
	return Archive{
		Manifest: Manifest{
			CreatedAt:     createdAt.UTC(),
			Format:        Format,
			Source:        source,
			SchemaVersion: schemaVersion,
			FormatVersion: CurrentFormatVersion,
			Sections:      scope.IncludedSections(),
		},
		Data: FilterData(data, scope),
	}
}

// MigrateArchive upgrades archive to the current in-memory contract.
func MigrateArchive(archive Archive) (Archive, error) {
	if archive.Manifest.Format != Format {
		return Archive{}, fmt.Errorf(
			"backup: unsupported format %q: %w",
			archive.Manifest.Format, domain.ErrInvalid,
		)
	}
	version := archive.Manifest.FormatVersion
	if version < MinSupportedFormatVersion {
		return Archive{}, fmt.Errorf(
			"backup: unsupported format version %d: %w",
			version, domain.ErrInvalid,
		)
	}
	if version > CurrentFormatVersion {
		return Archive{}, fmt.Errorf(
			"backup: future format version %d: %w",
			version, domain.ErrInvalid,
		)
	}
	switch version {
	case 1:
		return archive, nil
	default:
		return Archive{}, fmt.Errorf(
			"backup: no migration path for format version %d: %w",
			version, domain.ErrInvalid,
		)
	}
}

// TouchesRuntime reports whether restoring scope requires rebuilding the live
// engine and reconnecting market-data sinks.
func TouchesRuntime(scope Scope) bool {
	scope = scope.Normalize()
	for _, section := range []Section{
		SectionAccountsGroups,
		SectionPositions,
		SectionRiskLimits,
		SectionMarketData,
		SectionMarketDataQuotes,
	} {
		if scope.Included(section) {
			return true
		}
	}
	return false
}

// Included reports whether section is included by scope.
func (s Scope) Included(section Section) bool {
	if s.All {
		return true
	}
	return slices.Contains(s.Sections, section)
}

// IncludedSections returns the ordered section list represented by scope.
func (s Scope) IncludedSections() []Section {
	if s.All {
		return append([]Section(nil), AllSections...)
	}
	out := make([]Section, 0, len(AllSections))
	for _, section := range AllSections {
		if s.Included(section) {
			out = append(out, section)
		}
	}
	return out
}

// Normalize trims and deduplicates scope values.
func (s Scope) Normalize() Scope {
	s.Sections = normalizeSections(s.Sections)
	s.Accounts = s.Accounts.Normalize()
	s.Positions = s.Positions.Normalize()
	return s
}

// Normalize trims and deduplicates selector values.
func (s EntitySelector) Normalize() EntitySelector {
	s.Accounts = normalizeStrings(s.Accounts)
	s.Groups = normalizeStrings(s.Groups)
	return s
}

// Empty reports whether no explicit account or group filter is set.
func (s EntitySelector) Empty() bool {
	return len(s.Accounts) == 0 && len(s.Groups) == 0
}

// FilterData returns the rows included by scope.
func FilterData(data Data, scope Scope) Data {
	scope = scope.Normalize()
	accountByID := accountMap(data.Accounts)
	out := Data{}

	if scope.Included(SectionAccountsGroups) {
		out.Accounts = filterAccounts(data.Accounts, scope.Accounts)
		out.Groups = filterGroups(data.Groups, out.Accounts, scope.Accounts)
	}
	if scope.Included(SectionPositions) {
		out.Balances = filterBalances(data.Balances, scope.Positions, accountByID)
	}
	if scope.Included(SectionRiskLimits) {
		out.Limits = filterLimits(data.Limits, scope.Accounts, accountByID)
	}
	if scope.Included(SectionMarketData) {
		out.MarketDataInstances = append([]domain.MarketDataInstance(nil),
			data.MarketDataInstances...)
		out.MarketDataInstruments = append([]domain.MarketDataInstrument(nil),
			data.MarketDataInstruments...)
	}
	if scope.Included(SectionMarketDataQuotes) {
		out.MarketDataQuotes = append([]domain.MarketDataQuote(nil),
			data.MarketDataQuotes...)
	}
	if scope.Included(SectionGeneralSettings) && data.McpAccess != nil {
		out.McpAccess = make(map[string]bool, len(data.McpAccess))
		for command, enabled := range data.McpAccess {
			out.McpAccess[command] = enabled
		}
	}
	if scope.Included(SectionActivityHistory) {
		out.Adjustments = filterAdjustments(data.Adjustments, scope.Accounts, accountByID)
		out.Orders = filterOrders(data.Orders, scope.Accounts, accountByID)
		orderIDs := make(map[int64]bool, len(out.Orders))
		for _, order := range out.Orders {
			orderIDs[order.ID] = true
		}
		out.OrderEvents = filterOrderEvents(data.OrderEvents, orderIDs)
		out.Trades = filterTrades(data.Trades, orderIDs)
	}
	if scope.Included(SectionAuditLog) {
		out.Audit = filterAudit(data.Audit, scope.Accounts, accountByID)
	}
	return out
}

func normalizeSections(in []Section) []Section {
	seen := make(map[Section]bool, len(in))
	out := make([]Section, 0, len(in))
	for _, section := range in {
		section = Section(strings.TrimSpace(string(section)))
		if section == "" || seen[section] {
			continue
		}
		seen[section] = true
		out = append(out, section)
	}
	return out
}

func normalizeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func accountMap(accounts []domain.Account) map[domain.AccountID]domain.Account {
	out := make(map[domain.AccountID]domain.Account, len(accounts))
	for _, account := range accounts {
		out[account.ID] = account
	}
	return out
}

func selectorMatchesAccount(
	selector EntitySelector,
	accountID domain.AccountID,
	accountByID map[domain.AccountID]domain.Account,
) bool {
	if selector.All || selector.Empty() {
		return true
	}
	for _, id := range selector.Accounts {
		if domain.AccountID(id) == accountID {
			return true
		}
	}
	account, ok := accountByID[accountID]
	if !ok || account.GroupID == "" {
		return false
	}
	return slices.Contains(selector.Groups, account.GroupID)
}

func filterAccounts(
	accounts []domain.Account,
	selector EntitySelector,
) []domain.Account {
	accountByID := accountMap(accounts)
	out := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if selectorMatchesAccount(selector, account.ID, accountByID) {
			out = append(out, account)
		}
	}
	return out
}

func filterGroups(
	groups []domain.AccountGroup,
	accounts []domain.Account,
	selector EntitySelector,
) []domain.AccountGroup {
	if selector.All || selector.Empty() {
		return append([]domain.AccountGroup(nil), groups...)
	}
	needed := make(map[string]bool, len(selector.Groups)+len(accounts))
	for _, groupID := range selector.Groups {
		needed[groupID] = true
	}
	for _, account := range accounts {
		if account.GroupID != "" {
			needed[account.GroupID] = true
		}
	}
	out := make([]domain.AccountGroup, 0, len(groups))
	for _, group := range groups {
		if needed[group.ID] {
			out = append(out, group)
		}
	}
	return out
}

func filterBalances(
	balances []domain.Balance,
	selector EntitySelector,
	accountByID map[domain.AccountID]domain.Account,
) []domain.Balance {
	out := make([]domain.Balance, 0, len(balances))
	for _, balance := range balances {
		if selectorMatchesAccount(selector, balance.Account, accountByID) {
			out = append(out, balance)
		}
	}
	return out
}

func filterLimits(
	limits []domain.Limit,
	selector EntitySelector,
	accountByID map[domain.AccountID]domain.Account,
) []domain.Limit {
	out := make([]domain.Limit, 0, len(limits))
	for _, limit := range limits {
		if limit.Target.Account == "" {
			if selector.All || selector.Empty() {
				out = append(out, limit)
			}
			continue
		}
		if selectorMatchesAccount(selector, limit.Target.Account, accountByID) {
			out = append(out, limit)
		}
	}
	return out
}

func filterAdjustments(
	adjustments []domain.AccountAdjustmentRecord,
	selector EntitySelector,
	accountByID map[domain.AccountID]domain.Account,
) []domain.AccountAdjustmentRecord {
	out := make([]domain.AccountAdjustmentRecord, 0, len(adjustments))
	for _, adjustment := range adjustments {
		if selectorMatchesAccount(selector, adjustment.Account, accountByID) {
			out = append(out, adjustment)
		}
	}
	return out
}

func filterOrders(
	orders []domain.Order,
	selector EntitySelector,
	accountByID map[domain.AccountID]domain.Account,
) []domain.Order {
	out := make([]domain.Order, 0, len(orders))
	for _, order := range orders {
		if selectorMatchesAccount(selector, order.Account, accountByID) {
			out = append(out, order)
		}
	}
	return out
}

func filterOrderEvents(
	events []domain.OrderEvent,
	orderIDs map[int64]bool,
) []domain.OrderEvent {
	out := make([]domain.OrderEvent, 0, len(events))
	for _, event := range events {
		if orderIDs[event.OrderID] {
			out = append(out, event)
		}
	}
	return out
}

func filterTrades(
	trades []domain.Trade,
	orderIDs map[int64]bool,
) []domain.Trade {
	out := make([]domain.Trade, 0, len(trades))
	for _, trade := range trades {
		if orderIDs[trade.OrderID] {
			out = append(out, trade)
		}
	}
	return out
}

func filterAudit(
	rows []domain.AuditRow,
	selector EntitySelector,
	accountByID map[domain.AccountID]domain.Account,
) []domain.AuditRow {
	out := make([]domain.AuditRow, 0, len(rows))
	for _, row := range rows {
		if row.Account == "" {
			if selector.All || selector.Empty() {
				out = append(out, row)
			}
			continue
		}
		if selectorMatchesAccount(selector, row.Account, accountByID) {
			out = append(out, row)
		}
	}
	return out
}
