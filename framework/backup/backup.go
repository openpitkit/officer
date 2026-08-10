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

// Package backup defines Pit Officer's realm-portable backup archive contract.
//
// An archive represents exactly one realm and can be restored into either an
// isolated single-realm database or a shared multi-realm database, in both
// directions, through the store's realm handle. Identity in the archive is
// strictly portable: dictionaries are addressed by their immutable code (plus a
// mutable title); machine records by their opaque external id; every cross-row
// link is expressed by the referenced row's code or external id, never by a
// surrogate key or an engine id. Engine ids are not serialized at all - they are
// re-assigned, collision-free, when the archive is restored, while code and
// external id values are preserved verbatim.
//
// The archive format carries an explicit version. Restore rejects mismatches
// instead of best-effort importing an older shape into the current schema.
package backup

import (
	"slices"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// FormatVersion is the current portable archive shape.
const FormatVersion = 1

// Section identifies a logical backup/restore group. A scope is a set of
// sections plus the entity selectors that narrow account-addressed rows.
type Section string

const (
	// SectionAccountsGroups carries the account-group and account dictionaries.
	SectionAccountsGroups Section = "accounts_groups"
	// SectionPositions carries per-(account, asset) balance snapshots.
	SectionPositions Section = "positions"
	// SectionRiskLimits carries the per-policy limit barriers.
	SectionRiskLimits Section = "risk_limits"
	// SectionMarketData carries market-data instances and their instruments.
	SectionMarketData Section = "market_data_settings"
	// SectionMarketDataQuotes carries the latest per-instrument quotes.
	SectionMarketDataQuotes Section = "market_data_quotes"
	// SectionGeneralSettings carries the MCP access overrides and signing config.
	SectionGeneralSettings Section = "general_settings"
	// SectionUserSettings carries the per-user UI settings.
	SectionUserSettings Section = "user_settings"
	// SectionActivityHistory carries adjustments, orders (with approvals),
	// order events and trades.
	SectionActivityHistory Section = "activity_history"
	// SectionAuditLog carries the append-only audit trail.
	SectionAuditLog Section = "audit_log"
)

// AllSections is the full ordered section list. The order is the restore order
// too: dictionaries first (assets and principals always travel so foreign keys
// resolve), then account-addressed facts, so a machine record never references a
// dictionary row that has not been inserted yet.
var AllSections = []Section{
	SectionAccountsGroups,
	SectionPositions,
	SectionRiskLimits,
	SectionMarketData,
	SectionMarketDataQuotes,
	SectionGeneralSettings,
	SectionUserSettings,
	SectionActivityHistory,
	SectionAuditLog,
}

// RestoreMode defines how incoming rows interact with the rows already in the
// target realm. The modes are retargeted to the portable identity model: a row
// "exists" when the target already holds a row with the same code (dictionaries)
// or external id (machine records).
type RestoreMode string

const (
	// RestoreModeReplaceAll clears the included sections in the target realm and
	// re-creates them from the archive. It is the move/round-trip mode: after a
	// replace-all restore the target realm holds exactly the archive's rows for
	// the included sections, with fresh surrogate and engine ids.
	RestoreModeReplaceAll RestoreMode = "replace_all"
	// RestoreModeOverwrite inserts archive rows, overwriting any target row that
	// shares the same portable identity (code or external id) and leaving the
	// rest of the target realm untouched.
	RestoreModeOverwrite RestoreMode = "overwrite"
	// RestoreModeInsertMissing inserts only the archive rows whose portable
	// identity is not already present in the target realm; existing rows are
	// skipped.
	RestoreModeInsertMissing RestoreMode = "insert_missing"
)

// EntitySelector narrows account-addressed data by account code and group code.
// An empty (or All) selector matches every account. Selectors address accounts
// and groups by their portable codes, never by surrogate or engine ids.
type EntitySelector struct {
	// Accounts is the set of account codes to include; empty matches all.
	Accounts []string `json:"accounts,omitempty"`
	// Groups is the set of group codes to include (an account in one of these
	// groups matches); empty matches all.
	Groups []string `json:"groups,omitempty"`
	// All forces every account-addressed row to match regardless of the code
	// sets.
	All bool `json:"all"`
}

// Scope describes which sections an archive carries and which account-addressed
// rows within them. The selectors are evaluated against the portable codes.
type Scope struct {
	// Sections is the explicit section set; ignored when All is set.
	Sections []Section `json:"sections,omitempty"`
	// Accounts narrows the account-addressed rows of every section except
	// positions.
	Accounts EntitySelector `json:"accounts,omitempty"`
	// Positions narrows the balance rows of the positions section independently.
	Positions EntitySelector `json:"positions,omitempty"`
	// All includes every section with no account narrowing.
	All bool `json:"all"`
}

// RestoreOptions configures a restore operation.
type RestoreOptions struct {
	// Scope selects the sections and entities of the archive to restore.
	Scope Scope `json:"scope"`
	// Mode is how incoming rows interact with existing rows.
	Mode RestoreMode `json:"mode"`
}

// RestoreSummary reports the restore outcome per section. RestartRequired is
// true only when the node actually replaced the engine because the restored
// delta crossed a lifecycle boundary the live adapter cannot publish online.
// Restarting the market-data manager is a separate control-plane concern and
// does not set this field.
type RestoreSummary struct {
	// Applied counts the rows written per section.
	Applied map[Section]int `json:"applied"`
	// Skipped counts the rows skipped per section (insert-missing collisions).
	Skipped map[Section]int `json:"skipped"`
	// RestartRequired reports that this restore actually replaced the engine.
	RestartRequired bool `json:"restartRequired"`
}

// NewSummary returns an initialized restore summary with non-nil count maps.
func NewSummary() RestoreSummary {
	return RestoreSummary{
		Applied: make(map[Section]int),
		Skipped: make(map[Section]int),
	}
}

// AddApplied increments the applied count for section by n (n <= 0 is a no-op).
// The receiver is a pointer so a zero-value summary lazily allocates its count
// map instead of panicking on a nil-map write.
func (s *RestoreSummary) AddApplied(section Section, n int) {
	if n <= 0 {
		return
	}
	if s.Applied == nil {
		s.Applied = make(map[Section]int)
	}
	s.Applied[section] += n
}

// AddSkipped increments the skipped count for section by n (n <= 0 is a no-op).
// The receiver is a pointer so a zero-value summary lazily allocates its count
// map instead of panicking on a nil-map write.
func (s *RestoreSummary) AddSkipped(section Section, n int) {
	if n <= 0 {
		return
	}
	if s.Skipped == nil {
		s.Skipped = make(map[Section]int)
	}
	s.Skipped[section] += n
}

// RealmLabel is the portable identity of the realm an archive represents,
// carried for labelling only. The archive holds exactly one realm; on restore
// the rows land into whichever realm the store handle is bound to, so this label
// records the source realm's code and title without forcing the target realm's
// identity. The realm's own external id is deliberately omitted: it is the realm
// row's machine identity in the source schema and is re-established by the target
// connector, not transplanted.
type RealmLabel struct {
	// Code is the source realm's immutable code.
	Code string `json:"code"`
	// Title is the source realm's mutable display title; may be empty.
	Title string `json:"title,omitempty"`
}

// Manifest carries archive metadata independent of any concrete database.
type Manifest struct {
	// FormatVersion is the portable archive shape version.
	FormatVersion int `json:"formatVersion"`
	// CreatedAt is when the archive was produced (UTC).
	CreatedAt time.Time `json:"createdAt"`
	// Source names the producing installation, for operator context only.
	Source string `json:"source"`
	// Realm is the portable identity of the realm this archive represents.
	Realm RealmLabel `json:"realm"`
	// Sections is the ordered list of sections the archive carries.
	Sections []Section `json:"sections"`
}

// Account is the portable archive form of an account dictionary row. It carries
// the immutable code, the mutable title, the optional group link by group code,
// and the operator fields. The engine account id is intentionally absent: the
// target connector assigns a fresh, collision-free engine id on restore while
// this code is preserved.
type Account struct {
	// Code is the immutable, operator-chosen account code.
	Code string `json:"code"`
	// Title is the mutable display name; may be empty.
	Title string `json:"title,omitempty"`
	// Pnl is the latest SpotFunds account-currency P&L snapshot.
	Pnl string `json:"pnl"`
	// PnlHaltReason records why the engine could not calculate account P&L.
	PnlHaltReason domain.PnlHaltReason `json:"pnlHaltReason,omitempty"`
	// Currency is the account-level realized P&L currency asset code.
	Currency string `json:"currency,omitempty"`
	// GroupCode links to the account's group by the group's code; empty means no
	// group.
	GroupCode string `json:"groupCode,omitempty"`
	// Notes is a free-form reference string.
	Notes string `json:"notes,omitempty"`
	// BlockReason is the reason the account was blocked; empty when not blocked.
	BlockReason string `json:"blockReason,omitempty"`
	// Blocked reports whether the account is kill-switched.
	Blocked bool `json:"blocked,omitempty"`
}

// AccountGroup is the portable archive form of an account-group dictionary row.
// The engine group id is intentionally absent: the target connector assigns a
// fresh, collision-free engine id on restore while this code is preserved.
type AccountGroup struct {
	// Code is the immutable, operator-chosen group code.
	Code string `json:"code"`
	// Title is the mutable display name; may be empty.
	Title string `json:"title,omitempty"`
	// Currency is the group-level realized P&L currency asset code.
	Currency string `json:"currency,omitempty"`
	// Notes is a free-form reference string.
	Notes string `json:"notes,omitempty"`
	// BlockReason is the reason the group was blocked; empty when not blocked.
	BlockReason string `json:"blockReason,omitempty"`
	// Blocked reports whether the group is kill-switched.
	Blocked bool `json:"blocked,omitempty"`
}

// SigningKey is the portable archive form of a signing key. The key_id UUID is
// the key's own external handle and is preserved; the private and public key
// material travel as raw bytes (JSON base64). The surrogate id is never carried.
type SigningKey struct {
	// CreatedAt is when the key was generated or imported (UTC).
	CreatedAt time.Time `json:"createdAt"`
	// KeyID is the key's own UUID handle.
	KeyID string `json:"keyId"`
	// Alg is the signing algorithm.
	Alg string `json:"alg"`
	// PublicKey is the raw public key.
	PublicKey []byte `json:"publicKey,omitempty"`
	// PrivateKey is the raw private key seed.
	PrivateKey []byte `json:"privateKey,omitempty"`
	// Active reports whether this is the current signing key.
	Active bool `json:"active,omitempty"`
}

// SigningConfigEntry is one portable signing-config key/value pair. The key is a
// hardcoded enum stored as text.
type SigningConfigEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// OrderRecord is a portable order row. It is the Phase A domain order, already
// portable (external id, code/external-id links and the opaque SDK lock blob; no
// surrogate or engine id). Signed attestations travel per-event: each portable
// OrderEvent carries its own 1:1 attestation, so they restore with the event
// they belong to.
type OrderRecord struct {
	// Order is the portable order row.
	Order domain.Order `json:"order"`
}

// ExecutionReportRecord is one portable execution-report request identity.
type ExecutionReportRecord struct {
	// ExternalID is the report's caller-visible ID.
	ExternalID domain.ExternalID `json:"id"`
	// Order is the caller-visible ID of the parent order.
	Order domain.ExternalID `json:"orderId"`
	// At is when Officer persisted the report.
	At time.Time `json:"at"`
}

// ExecutionReportEventLink preserves one report-to-event relationship without
// serializing either table's surrogate key.
type ExecutionReportEventLink struct {
	// Report is the caller-visible report ID.
	Report domain.ExternalID `json:"reportId"`
	// Event is the caller-visible event ID.
	Event domain.ExternalID `json:"eventId"`
}

// Data carries the portable rows of an archive. Dictionary sections are listed
// first so foreign keys resolve on import: assets and principals are always
// carried (every machine-record reference resolves against them), then groups
// and accounts, then the account-addressed facts. Every list uses portable
// identity; no list carries a surrogate key or an engine id.
type Data struct {
	// AssetClasses is the asset-class dictionary (by code). Always carried so the
	// asset->class foreign key resolves on import.
	AssetClasses []domain.AssetClass `json:"assetClasses,omitempty"`
	// Assets is the asset dictionary (by code). Always carried so fact foreign
	// keys resolve.
	Assets []domain.Asset `json:"assets,omitempty"`
	// Principals is the principal dictionary (by code). Always carried so
	// optional principal references resolve.
	Principals []domain.Principal `json:"principals,omitempty"`
	// Groups is the account-group dictionary (by code).
	Groups []AccountGroup `json:"groups,omitempty"`
	// DefaultGroupCurrency is the fallback tier in the account -> group ->
	// default currency cascade.
	DefaultGroupCurrency string `json:"defaultGroupCurrency,omitempty"`
	// Accounts is the account dictionary (by code).
	Accounts []Account `json:"accounts,omitempty"`
	// Balances are per-(account, asset) snapshots, linked by codes.
	Balances []domain.Balance `json:"balances,omitempty"`
	// RateLimits are rate-limit barriers, linked by scope+codes.
	RateLimits []domain.LimitRate `json:"rateLimits,omitempty"`
	// OrderSizeLimits are order-size barriers, linked by scope+codes.
	OrderSizeLimits []domain.LimitOrderSize `json:"orderSizeLimits,omitempty"`
	// SpotFundsPnlBoundsLimits are currency-valued SpotFunds P&L-bounds barriers,
	// linked by scope+codes.
	SpotFundsPnlBoundsLimits []domain.LimitSpotFundsPnlBounds `json:"spotFundsPnlBoundsLimits,omitempty"`
	// Adjustments are spot-funds adjustment records (by external id).
	Adjustments []domain.AccountAdjustmentRecord `json:"adjustments,omitempty"`
	// Orders are order records with their optional approvals (by external id).
	Orders []OrderRecord `json:"orders,omitempty"`
	// OrderEvents are order lifecycle events (by external id, linked to their
	// order's external id).
	OrderEvents []domain.OrderEvent `json:"orderEvents,omitempty"`
	// ExecutionReports are request identities linked to their parent orders.
	ExecutionReports []ExecutionReportRecord `json:"executionReports,omitempty"`
	// ExecutionReportEvents link reports to all events they produced.
	ExecutionReportEvents []ExecutionReportEventLink `json:"executionReportEvents,omitempty"`
	// Trades are per-fill trade rows (by external id, linked to their order's
	// external id).
	Trades []domain.Trade `json:"trades,omitempty"`
	// Audit is the append-only audit trail (by external id).
	Audit []domain.AuditRow `json:"audit,omitempty"`
	// MarketDataInstances are configured market-data sources (by external id).
	MarketDataInstances []domain.MarketDataInstance `json:"marketDataInstances,omitempty"`
	// MarketDataInstruments are per-instance instruments, linked by the instance
	// external id and asset codes.
	MarketDataInstruments []domain.MarketDataInstrument `json:"marketDataInstruments,omitempty"`
	// MarketDataQuotes are latest per-instrument quotes, linked by instance
	// external id and external symbol.
	MarketDataQuotes []domain.MarketDataQuote `json:"marketDataQuotes,omitempty"`
	// SigningKeys are the signing keypairs (by key_id).
	SigningKeys []SigningKey `json:"signingKeys,omitempty"`
	// SigningConfig is the global signing configuration (enum keys).
	SigningConfig []SigningConfigEntry `json:"signingConfig,omitempty"`
	// McpAccess is the per-command MCP enable/disable overrides.
	McpAccess map[string]bool `json:"mcpAccess,omitempty"`
	// UserSettings are the per-user UI settings.
	UserSettings []domain.UserSetting `json:"userSettings,omitempty"`
}

// Archive is the single JSON document copied between Officer realms. It is one
// realm's portable form: a labelling manifest plus the portable rows.
type Archive struct {
	// Manifest carries the metadata and the realm label.
	Manifest Manifest `json:"manifest"`
	// Data carries the portable rows.
	Data Data `json:"data"`
}

// NewArchive builds an archive from data and metadata, restricting the data to
// the rows the scope includes. There is no version field to stamp.
func NewArchive(
	createdAt time.Time,
	source string,
	realm RealmLabel,
	scope Scope,
	data Data,
) Archive {
	scope = scope.Normalize()
	return Archive{
		Manifest: Manifest{
			FormatVersion: FormatVersion,
			CreatedAt:     createdAt.UTC(),
			Source:        source,
			Realm:         realm,
			Sections:      scope.IncludedSections(),
		},
		Data: FilterData(data, scope),
	}
}

// Filename returns the conventional backup filename for createdAt.
func Filename(createdAt time.Time) string {
	return "pit-officer-backup-" +
		createdAt.UTC().Format("20060102T150405Z") + ".json"
}

// RuntimeSection reports whether restoring section changes live runtime state.
// The accounts+groups, positions, risk-limits and market-data sections are
// projected into the live engine; general/user settings, activity history and
// the audit log are observational. A runtime delta can usually be published
// online and does not by itself imply an engine replacement.
//
// The shared support dictionaries (asset classes, assets, principals) are NOT a
// runtime section: they travel with every restore for foreign-key resolution but
// are not part of the engine snapshot, so writing one never rebuilds the engine.
func RuntimeSection(section Section) bool {
	switch section {
	case SectionAccountsGroups,
		SectionPositions,
		SectionRiskLimits,
		SectionMarketData,
		SectionMarketDataQuotes:
		return true
	default:
		return false
	}
}

// TouchesRuntime reports whether restoring scope can change live runtime state.
// The node uses this to select its synchronization gate. The backend plans any
// market-data-manager lifecycle separately from the actual configuration delta.
//
// It evaluates the sections carried in scope as given. Passed the caller's raw
// requested scope it answers "did the caller ask for a runtime section"; passed
// scope.Normalize() it answers "could this restore write a runtime dictionary",
// since Normalize force-includes the accounts+groups (and market-data) parent
// sections an account-addressed restore lands to keep foreign keys resolving. The
// node decides the restore lock from the normalized scope so any runtime write
// runs under the exclusive gate. It then classifies the actual delta and replaces
// the engine only for residual lifecycle gaps.
func TouchesRuntime(scope Scope) bool {
	if scope.All {
		return true
	}
	for _, section := range normalizeSections(scope.Sections) {
		if RuntimeSection(section) {
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

// accountAddressedSections are the sections whose rows resolve against the
// accounts+groups dictionary; including any of them forces that dictionary to
// travel so the parent rows the facts reference always exist on restore.
var accountAddressedSections = []Section{
	SectionPositions,
	SectionRiskLimits,
	SectionActivityHistory,
	SectionAuditLog,
}

// Normalize trims and deduplicates the scope's section list and selectors, then
// force-includes the parent dictionary sections a scoped archive's rows resolve
// against: any account-addressed section pulls in accounts+groups, and the
// market-data quotes section pulls in the market-data instances section. This
// keeps a scoped archive self-resolving (its rows never reference a parent the
// archive omitted). It is a no-op when All is set, since All already carries
// every section.
func (s Scope) Normalize() Scope {
	s.Sections = normalizeSections(s.Sections)
	s.Accounts = s.Accounts.Normalize()
	s.Positions = s.Positions.Normalize()
	if !s.All {
		s.Sections = forceParentSections(s.Sections)
	}
	return s
}

// forceParentSections appends, in canonical AllSections order, the parent
// dictionary sections required by the included account-addressed and quote
// sections, leaving an already-complete or empty list untouched.
func forceParentSections(sections []Section) []Section {
	has := make(map[Section]bool, len(sections))
	for _, section := range sections {
		has[section] = true
	}
	if !has[SectionAccountsGroups] {
		for _, section := range accountAddressedSections {
			if has[section] {
				has[SectionAccountsGroups] = true
				break
			}
		}
	}
	if has[SectionMarketDataQuotes] {
		has[SectionMarketData] = true
	}
	if has[SectionActivityHistory] {
		has[SectionGeneralSettings] = true
	}
	out := make([]Section, 0, len(has))
	for _, section := range AllSections {
		if has[section] {
			out = append(out, section)
		}
	}
	return out
}

// Normalize trims and deduplicates the selector's code sets.
func (s EntitySelector) Normalize() EntitySelector {
	s.Accounts = normalizeStrings(s.Accounts)
	s.Groups = normalizeStrings(s.Groups)
	return s
}

// Empty reports whether no explicit account or group code filter is set.
func (s EntitySelector) Empty() bool {
	return len(s.Accounts) == 0 && len(s.Groups) == 0
}

// FilterData returns a copy of data holding only the rows scope includes.
// Dictionaries (assets, principals) always travel so machine-record foreign keys
// resolve; account-addressed rows are narrowed by the relevant selector.
func FilterData(data Data, scope Scope) Data {
	scope = scope.Normalize()
	groupByAccount := accountGroupMap(data.Accounts)
	out := Data{}

	// Asset classes, assets and principals are dictionaries every fact may
	// reference; they always travel so foreign keys resolve on import, regardless
	// of section.
	out.AssetClasses = append([]domain.AssetClass(nil), data.AssetClasses...)
	out.Assets = append([]domain.Asset(nil), data.Assets...)
	out.Principals = append([]domain.Principal(nil), data.Principals...)

	if scope.Included(SectionAccountsGroups) {
		out.Accounts = filterAccounts(data.Accounts, scope.Accounts)
		out.Groups = filterGroups(data.Groups, out.Accounts, scope.Accounts)
		out.DefaultGroupCurrency = data.DefaultGroupCurrency
	}
	if scope.Included(SectionPositions) {
		out.Balances = filterBalances(data.Balances, scope.Positions, groupByAccount)
	}
	if scope.Included(SectionRiskLimits) {
		out.RateLimits = filterRateLimits(data.RateLimits, scope.Accounts, groupByAccount)
		out.OrderSizeLimits = filterOrderSizeLimits(data.OrderSizeLimits, scope.Accounts, groupByAccount)
		out.SpotFundsPnlBoundsLimits = filterSpotFundsPnlBoundsLimits(
			data.SpotFundsPnlBoundsLimits,
			scope.Accounts,
			groupByAccount,
		)
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
	if scope.Included(SectionGeneralSettings) {
		if data.McpAccess != nil {
			out.McpAccess = make(map[string]bool, len(data.McpAccess))
			for command, enabled := range data.McpAccess {
				out.McpAccess[command] = enabled
			}
		}
		out.SigningConfig = append([]SigningConfigEntry(nil), data.SigningConfig...)
		out.SigningKeys = append([]SigningKey(nil), data.SigningKeys...)
	}
	if scope.Included(SectionUserSettings) && data.UserSettings != nil {
		out.UserSettings = append([]domain.UserSetting(nil), data.UserSettings...)
	}
	if scope.Included(SectionActivityHistory) {
		out.Adjustments = filterAdjustments(data.Adjustments, scope.Accounts, groupByAccount)
		out.Orders = filterOrders(data.Orders, scope.Accounts, groupByAccount)
		orderIDs := make(map[domain.ExternalID]bool, len(out.Orders))
		for _, rec := range out.Orders {
			orderIDs[rec.Order.ExternalID] = true
		}
		out.OrderEvents = filterOrderEvents(data.OrderEvents, orderIDs)
		out.ExecutionReports = filterExecutionReports(data.ExecutionReports, orderIDs)
		reportIDs := make(map[domain.ExternalID]bool, len(out.ExecutionReports))
		for _, report := range out.ExecutionReports {
			reportIDs[report.ExternalID] = true
		}
		eventIDs := make(map[domain.ExternalID]bool, len(out.OrderEvents))
		for _, event := range out.OrderEvents {
			eventIDs[event.ExternalID] = true
		}
		out.ExecutionReportEvents = filterExecutionReportEvents(
			data.ExecutionReportEvents,
			reportIDs,
			eventIDs,
		)
		out.Trades = filterTrades(data.Trades, orderIDs)
	}
	if scope.Included(SectionAuditLog) {
		out.Audit = filterAudit(data.Audit, scope.Accounts, groupByAccount)
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

// accountGroupMap maps each account code to its group code so a selector can
// match an account by the group it belongs to.
func accountGroupMap(accounts []Account) map[domain.AccountID]string {
	out := make(map[domain.AccountID]string, len(accounts))
	for _, account := range accounts {
		out[domain.AccountID(account.Code)] = account.GroupCode
	}
	return out
}

// selectorMatchesAccount reports whether the account code is included by the
// selector, by direct code match or by its group code.
func selectorMatchesAccount(
	selector EntitySelector,
	account domain.AccountID,
	groupByAccount map[domain.AccountID]string,
) bool {
	if selector.All || selector.Empty() {
		return true
	}
	if slices.Contains(selector.Accounts, account.String()) {
		return true
	}
	group := groupByAccount[account]
	return group != "" && slices.Contains(selector.Groups, group)
}

func filterAccounts(accounts []Account, selector EntitySelector) []Account {
	groupByAccount := accountGroupMap(accounts)
	out := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if selectorMatchesAccount(selector, domain.AccountID(account.Code), groupByAccount) {
			out = append(out, account)
		}
	}
	return out
}

// filterGroups keeps the groups referenced by the selected accounts (and any
// group named directly in the selector), so a restored account's group link
// always resolves.
func filterGroups(
	groups []AccountGroup,
	accounts []Account,
	selector EntitySelector,
) []AccountGroup {
	if selector.All || selector.Empty() {
		return append([]AccountGroup(nil), groups...)
	}
	needed := make(map[string]bool, len(selector.Groups)+len(accounts))
	for _, code := range selector.Groups {
		needed[code] = true
	}
	for _, account := range accounts {
		if account.GroupCode != "" {
			needed[account.GroupCode] = true
		}
	}
	out := make([]AccountGroup, 0, len(groups))
	for _, group := range groups {
		if needed[group.Code] {
			out = append(out, group)
		}
	}
	return out
}

func filterBalances(
	balances []domain.Balance,
	selector EntitySelector,
	groupByAccount map[domain.AccountID]string,
) []domain.Balance {
	out := make([]domain.Balance, 0, len(balances))
	for _, balance := range balances {
		if selectorMatchesAccount(selector, balance.Account, groupByAccount) {
			out = append(out, balance)
		}
	}
	return out
}

func filterRateLimits(
	limits []domain.LimitRate,
	selector EntitySelector,
	groupByAccount map[domain.AccountID]string,
) []domain.LimitRate {
	out := make([]domain.LimitRate, 0, len(limits))
	for _, limit := range limits {
		if matchLimitAccount(selector, limit.Account, groupByAccount) {
			out = append(out, limit)
		}
	}
	return out
}

func filterOrderSizeLimits(
	limits []domain.LimitOrderSize,
	selector EntitySelector,
	groupByAccount map[domain.AccountID]string,
) []domain.LimitOrderSize {
	out := make([]domain.LimitOrderSize, 0, len(limits))
	for _, limit := range limits {
		if matchLimitAccount(selector, limit.Account, groupByAccount) {
			out = append(out, limit)
		}
	}
	return out
}

func filterSpotFundsPnlBoundsLimits(
	limits []domain.LimitSpotFundsPnlBounds,
	selector EntitySelector,
	groupByAccount map[domain.AccountID]string,
) []domain.LimitSpotFundsPnlBounds {
	out := make([]domain.LimitSpotFundsPnlBounds, 0, len(limits))
	for _, limit := range limits {
		if matchSpotFundsPnlBoundsLimit(selector, limit, groupByAccount) {
			out = append(out, limit)
		}
	}
	return out
}

func matchSpotFundsPnlBoundsLimit(
	selector EntitySelector,
	limit domain.LimitSpotFundsPnlBounds,
	groupByAccount map[domain.AccountID]string,
) bool {
	if limit.Account != "" {
		return selectorMatchesAccount(selector, limit.Account, groupByAccount)
	}
	if limit.AccountGroup != "" {
		return selectorMatchesGroup(selector, limit.AccountGroup, groupByAccount)
	}
	return selector.All || selector.Empty()
}

func selectorMatchesGroup(
	selector EntitySelector,
	group string,
	groupByAccount map[domain.AccountID]string,
) bool {
	if selector.All || selector.Empty() {
		return true
	}
	if slices.Contains(selector.Groups, group) {
		return true
	}
	for _, account := range selector.Accounts {
		if groupByAccount[domain.AccountID(account)] == group {
			return true
		}
	}
	return false
}

// matchLimitAccount keeps scope-wide barriers (empty account axis) whenever the
// selector imposes no narrowing, and account-bound barriers when the account
// matches. This mirrors the old EAV behaviour on the typed barriers.
func matchLimitAccount(
	selector EntitySelector,
	account domain.AccountID,
	groupByAccount map[domain.AccountID]string,
) bool {
	if account == "" {
		return selector.All || selector.Empty()
	}
	return selectorMatchesAccount(selector, account, groupByAccount)
}

func filterAdjustments(
	adjustments []domain.AccountAdjustmentRecord,
	selector EntitySelector,
	groupByAccount map[domain.AccountID]string,
) []domain.AccountAdjustmentRecord {
	out := make([]domain.AccountAdjustmentRecord, 0, len(adjustments))
	for _, adjustment := range adjustments {
		if selectorMatchesAccount(selector, adjustment.Account, groupByAccount) {
			out = append(out, adjustment)
		}
	}
	return out
}

func filterOrders(
	orders []OrderRecord,
	selector EntitySelector,
	groupByAccount map[domain.AccountID]string,
) []OrderRecord {
	out := make([]OrderRecord, 0, len(orders))
	for _, rec := range orders {
		if selectorMatchesAccount(selector, rec.Order.Account, groupByAccount) {
			out = append(out, rec)
		}
	}
	return out
}

func filterOrderEvents(
	events []domain.OrderEvent,
	orderIDs map[domain.ExternalID]bool,
) []domain.OrderEvent {
	out := make([]domain.OrderEvent, 0, len(events))
	for _, event := range events {
		if orderIDs[event.Order] {
			out = append(out, event)
		}
	}
	return out
}

func filterExecutionReports(
	reports []ExecutionReportRecord,
	orderIDs map[domain.ExternalID]bool,
) []ExecutionReportRecord {
	out := make([]ExecutionReportRecord, 0, len(reports))
	for _, report := range reports {
		if orderIDs[report.Order] {
			out = append(out, report)
		}
	}
	return out
}

func filterExecutionReportEvents(
	links []ExecutionReportEventLink,
	reportIDs map[domain.ExternalID]bool,
	eventIDs map[domain.ExternalID]bool,
) []ExecutionReportEventLink {
	out := make([]ExecutionReportEventLink, 0, len(links))
	for _, link := range links {
		if reportIDs[link.Report] && eventIDs[link.Event] {
			out = append(out, link)
		}
	}
	return out
}

func filterTrades(
	trades []domain.Trade,
	orderIDs map[domain.ExternalID]bool,
) []domain.Trade {
	out := make([]domain.Trade, 0, len(trades))
	for _, trade := range trades {
		if orderIDs[trade.Order] {
			out = append(out, trade)
		}
	}
	return out
}

func filterAudit(
	rows []domain.AuditRow,
	selector EntitySelector,
	groupByAccount map[domain.AccountID]string,
) []domain.AuditRow {
	out := make([]domain.AuditRow, 0, len(rows))
	for _, row := range rows {
		if row.Account == "" {
			if selector.All || selector.Empty() {
				out = append(out, row)
			}
			continue
		}
		if selectorMatchesAccount(selector, row.Account, groupByAccount) {
			out = append(out, row)
		}
	}
	return out
}
