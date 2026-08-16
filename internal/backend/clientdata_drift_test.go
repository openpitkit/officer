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

package backend_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/store/sqlite"
)

const (
	driftBackup      = "backup"
	driftBusinessCSV = "business-csv"
	driftSchema      = "schema"
)

func clientDataDriftLock(t *testing.T, value string) []byte {
	t.Helper()
	price, err := param.NewPriceFromString(value)
	if err != nil {
		t.Fatalf("lock price %q: %v", value, err)
	}
	lock, err := pretrade.NewLockFromEntries([]pretrade.Entry{{
		PolicyGroupID: model.DefaultPolicyGroupID,
		Price:         price,
	}})
	if err != nil {
		t.Fatalf("NewLockFromEntries: %v", err)
	}
	payload, err := lock.MarshalMsgpack()
	if err != nil {
		t.Fatalf("MarshalMsgpack: %v", err)
	}
	return payload
}

type clientDataDrift struct {
	Surface string
	Entity  string
	Field   string
	Kind    string
	Detail  string
}

type clientDataAllow struct {
	Surface string
	Entity  string
	Field   string
	Kind    string
	Why     string
}

var clientDataAllowlist = []clientDataAllow{
	{
		Surface: driftBackup,
		Entity:  "accounts",
		Field:   "EffectiveCurrency",
		Kind:    "uncovered",
		Why:     "derived from account, group, and default currency cascade",
	},
	{
		Surface: driftBackup,
		Entity:  "accounts",
		Field:   "CurrencyOrigin",
		Kind:    "uncovered",
		Why:     "derived label for the winning currency cascade tier",
	},
	{
		Surface: driftBackup,
		Entity:  "accounts",
		Field:   "GroupCurrency",
		Kind:    "uncovered",
		Why:     "derived from the linked group row during account reads",
	},
	{
		Surface: driftBackup,
		Entity:  "accounts",
		Field:   "DefaultCurrency",
		Kind:    "uncovered",
		Why:     "derived from the reserved default group row during reads",
	},
	{
		Surface: driftBackup,
		Entity:  "accounts",
		Field:   "EngineAccountID",
		Kind:    "uncovered",
		Why:     "engine runtime id is reallocated by the target store",
	},
	{
		Surface: driftBackup,
		Entity:  "account-groups",
		Field:   "EngineGroupID",
		Kind:    "uncovered",
		Why:     "engine runtime id is reallocated by the target store",
	},
	{
		Surface: driftBackup,
		Entity:  "assets",
		Field:   "EngineAssetID",
		Kind:    "uncovered",
		Why:     "engine runtime id is reallocated by the target store",
	},
	{
		Surface: driftBackup,
		Entity:  "positions",
		Field:   "AccountCurrency",
		Kind:    "uncovered",
		Why:     "derived from the target account-group currency cascade on read",
	},
	{
		Surface: driftBackup,
		Entity:  "market-data-instruments",
		Field:   "BaseAssetID",
		Kind:    "uncovered",
		Why:     "engine runtime id is resolved from the target asset dictionary",
	},
	{
		Surface: driftBackup,
		Entity:  "market-data-instruments",
		Field:   "QuoteAssetID",
		Kind:    "uncovered",
		Why:     "engine runtime id is resolved from the target asset dictionary",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "accounts",
		Field:   "EffectiveCurrency",
		Kind:    "uncovered",
		Why:     "derived from account, group, and default currency cascade",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "accounts",
		Field:   "CurrencyOrigin",
		Kind:    "uncovered",
		Why:     "derived label for the winning currency cascade tier",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "accounts",
		Field:   "GroupCurrency",
		Kind:    "uncovered",
		Why:     "derived from the linked group row during account reads",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "accounts",
		Field:   "DefaultCurrency",
		Kind:    "uncovered",
		Why:     "derived from the reserved default group row during reads",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "accounts",
		Field:   "EngineAccountID",
		Kind:    "uncovered",
		Why:     "engine runtime id is not a portable business CSV column",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "account-groups",
		Field:   "EngineGroupID",
		Kind:    "uncovered",
		Why:     "engine runtime id is not a portable business CSV column",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "account_groups",
		Field:   "EngineGroupID",
		Kind:    "uncovered",
		Why:     "engine runtime id is not a portable business CSV column",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "positions",
		Field:   "UpdatedAt",
		Kind:    "uncovered",
		Why:     "position update timestamps are not part of the business export",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "positions",
		Field:   "AccountCurrency",
		Kind:    "uncovered",
		Why: "derived from the account's currency cascade, not independent " +
			"position state; the account row carries the currency it denominates in",
	},
	{
		Surface: driftBackup,
		Entity:  "orders",
		Field:   "CommissionSubtotals",
		Kind:    "uncovered",
		Why:     "derived read model aggregated from trades, not an independent backup field",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "orders",
		Field:   "CommissionSubtotals",
		Kind:    "uncovered",
		Why:     "derived read model aggregated from trades, not an independent CSV field",
	},
	{
		Surface: driftBusinessCSV,
		Entity:  "trades",
		Field:   "Commission",
		Kind:    "uncovered",
		Why:     "structured commission is split into commission_amount and commission_currency columns",
	},
	{
		Surface: driftSchema,
		Entity:  "*",
		Field:   "id",
		Kind:    "internal",
		Why:     "SQLite surrogate primary key is not a portable client field",
	},
	{
		Surface: driftSchema,
		Entity:  "*",
		Field:   "realm_id",
		Kind:    "internal",
		Why:     "single-realm store binding supplies the realm identity",
	},
	{
		Surface: driftSchema,
		Entity:  "balance",
		Field:   "updated_at",
		Kind:    "business-csv-uncovered",
		Why:     "the export omits the internal snapshot timestamp",
	},
	{
		Surface: driftSchema,
		Entity:  "order_record",
		Field:   "leaves_quantity",
		Kind:    "business-csv-uncovered",
		Why:     "orders are export-only business CSV rows and omit runtime leaves",
	},
	{
		Surface: driftSchema,
		Entity:  "order_record",
		Field:   "lock",
		Kind:    "business-csv-uncovered",
		Why:     "orders are export-only business CSV rows and omit opaque engine locks",
	},
	{
		Surface: driftBackup,
		Entity:  "adjustments",
		Field:   "adjustments.Accepted",
		Kind:    "optional-sentinel",
		Why:     "accepted/rejected adjustment variants are covered by separate sentinel rows",
	},
	{
		Surface: driftBackup,
		Entity:  "adjustments",
		Field:   "adjustments.Rejected",
		Kind:    "optional-sentinel",
		Why:     "accepted/rejected adjustment variants are covered by separate sentinel rows",
	},
	{
		Surface: driftBackup,
		Entity:  "accounts",
		Field:   "accounts.Pnl",
		Kind:    "optional-sentinel",
		Why:     "a halted account carries no P&L number; the pair is covered by separate sentinel accounts",
	},
	{
		Surface: driftBackup,
		Entity:  "accounts",
		Field:   "accounts.PnlHaltReason",
		Kind:    "optional-sentinel",
		Why:     "an account with a P&L number is not halted; the pair is covered by separate sentinel accounts",
	},
	{
		Surface: driftBackup,
		Entity:  "positions",
		Field:   "positions.RealizedPnl",
		Kind:    "optional-sentinel",
		Why:     "a halted realized P&L carries no number; the pair is covered by separate sentinel positions",
	},
	{
		Surface: driftBackup,
		Entity:  "positions",
		Field:   "positions.RealizedPnlHaltReason",
		Kind:    "optional-sentinel",
		Why:     "an authoritative realized P&L number is not halted; the pair is covered by separate sentinel positions",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.RejectCode",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.RejectScope",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.RejectPolicy",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.RejectReason",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.RejectDetails",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.FillQuantity",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.FillPrice",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.FillLockPrice",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.LeavesQuantity",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.OrderStatus",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.Commission",
		Kind:    "optional-sentinel",
		Why:     "event payload variants populate only fields owned by their event type",
	},
	{
		Surface: driftBackup,
		Entity:  "order-events",
		Field:   "order-events.Payload.ExecutionReport",
		Kind:    "optional-sentinel",
		Why:     "only execution-report events carry the audit-safe original request",
	},
	{
		Surface: driftBackup,
		Entity:  "spot-funds-pnl-bounds-limits",
		Field:   "spot-funds-pnl-bounds-limits.Account",
		Kind:    "optional-sentinel",
		Why:     "account and group scopes populate their own identity axis",
	},
	{
		Surface: driftBackup,
		Entity:  "spot-funds-pnl-bounds-limits",
		Field:   "spot-funds-pnl-bounds-limits.AccountGroup",
		Kind:    "optional-sentinel",
		Why:     "account and group scopes populate their own identity axis",
	},
}

type paritySpec struct {
	surface string
	entity  string
	source  reflect.Type
	target  reflect.Type
	fields  map[string]string
}

type csvColumnSpec struct {
	entity  string
	source  reflect.Type
	fields  map[string]string
	omitted map[string]string
}

type csvSentinelSpec struct {
	keyColumns      []string
	keyValues       []string
	requiredColumns []string
}

type schemaSurfaceSpec struct {
	backup      map[string]string
	businessCSV map[string]string
}

func (s schemaSurfaceSpec) hasPortableSpec() bool {
	return s.backup != nil || s.businessCSV != nil
}

type businessCSVEntityContract struct {
	entity     string
	table      string
	source     reflect.Type
	keyColumns []string
	keyValues  []string
	// omitColumns names business-CSV columns the primary sentinel row cannot
	// carry because another row in extraRows owns them. Only for genuinely
	// mutually exclusive columns; every column must stay required on some row,
	// which TestClientDataBusinessCSVSentinelRowsCoverEveryColumn enforces.
	omitColumns []string
	// extraRows are further sentinel rows for a table whose columns cannot all
	// be non-empty at once.
	extraRows []csvSentinelRow
}

// csvSentinelRow is one additional keyed sentinel row of an entity, identified
// by the contract's keyColumns, that deliberately leaves omitColumns empty.
type csvSentinelRow struct {
	keyValues   []string
	omitColumns []string
}

var backupParitySpecs = []paritySpec{
	{
		surface: driftBackup,
		entity:  "asset-classes",
		source:  reflect.TypeOf(domain.AssetClass{}),
		target:  reflect.TypeOf(domain.AssetClass{}),
	},
	{
		surface: driftBackup,
		entity:  "assets",
		source:  reflect.TypeOf(domain.Asset{}),
		target:  reflect.TypeOf(backup.Asset{}),
	},
	{
		surface: driftBackup,
		entity:  "principals",
		source:  reflect.TypeOf(domain.Principal{}),
		target:  reflect.TypeOf(domain.Principal{}),
	},
	{
		surface: driftBackup,
		entity:  "account-groups",
		source:  reflect.TypeOf(domain.AccountGroup{}),
		target:  reflect.TypeOf(backup.AccountGroup{}),
	},
	{
		surface: driftBackup,
		entity:  "accounts",
		source:  reflect.TypeOf(domain.Account{}),
		target:  reflect.TypeOf(backup.Account{}),
	},
	{
		surface: driftBackup,
		entity:  "positions",
		source:  reflect.TypeOf(domain.Balance{}),
		target:  reflect.TypeOf(backup.Balance{}),
	},
	{
		surface: driftBackup,
		entity:  "rate-limits",
		source:  reflect.TypeOf(domain.LimitRate{}),
		target:  reflect.TypeOf(domain.LimitRate{}),
	},
	{
		surface: driftBackup,
		entity:  "order-size-limits",
		source:  reflect.TypeOf(domain.LimitOrderSize{}),
		target:  reflect.TypeOf(domain.LimitOrderSize{}),
	},
	{
		surface: driftBackup,
		entity:  "spot-funds-pnl-bounds-limits",
		source:  reflect.TypeOf(domain.LimitSpotFundsPnlBounds{}),
		target:  reflect.TypeOf(domain.LimitSpotFundsPnlBounds{}),
	},
	{
		surface: driftBackup,
		entity:  "adjustments",
		source:  reflect.TypeOf(domain.AccountAdjustmentRecord{}),
		target:  reflect.TypeOf(domain.AccountAdjustmentRecord{}),
	},
	{
		surface: driftBackup,
		entity:  "adjustment-requests",
		source:  reflect.TypeOf(domain.AdjustmentRequest{}),
		target:  reflect.TypeOf(domain.AdjustmentRequest{}),
	},
	{
		surface: driftBackup,
		entity:  "adjustment-accepted-outcomes",
		source:  reflect.TypeOf(domain.AdjustmentOutcomeAccepted{}),
		target:  reflect.TypeOf(domain.AdjustmentOutcomeAccepted{}),
	},
	{
		surface: driftBackup,
		entity:  "adjustment-rejected-outcomes",
		source:  reflect.TypeOf(domain.AdjustmentOutcomeRejected{}),
		target:  reflect.TypeOf(domain.AdjustmentOutcomeRejected{}),
	},
	{
		surface: driftBackup,
		entity:  "orders",
		source:  reflect.TypeOf(domain.Order{}),
		target:  reflect.TypeOf(backup.OrderRecord{}),
		fields: map[string]string{
			"At":          "Order.At",
			"ExternalID":  "Order.ExternalID",
			"AmountValue": "Order.AmountValue",
			"Leaves":      "Order.Leaves",
			"Price":       "Order.Price",
			"Principal":   "Order.Principal",
			"BaseAsset":   "Order.BaseAsset",
			"QuoteAsset":  "Order.QuoteAsset",
			"Lock":        "Order.Lock",
			"Account":     "Order.Account",
			"Source":      "Order.Source",
			"Side":        "Order.Side",
			"AmountKind":  "Order.AmountKind",
			"Status":      "Order.Status",
			"DropCopy":    "Order.DropCopy",
		},
	},
	{
		surface: driftBackup,
		entity:  "order-events",
		source:  reflect.TypeOf(domain.OrderEvent{}),
		target:  reflect.TypeOf(domain.OrderEvent{}),
	},
	{
		surface: driftBackup,
		entity:  "order-event-payloads",
		source:  reflect.TypeOf(domain.OrderEventPayload{}),
		target:  reflect.TypeOf(domain.OrderEventPayload{}),
	},
	{
		surface: driftBackup,
		entity:  "event-attestations",
		source:  reflect.TypeOf(domain.EventAttestation{}),
		target:  reflect.TypeOf(domain.EventAttestation{}),
	},
	{
		surface: driftBackup,
		entity:  "trades",
		source:  reflect.TypeOf(domain.Trade{}),
		target:  reflect.TypeOf(domain.Trade{}),
	},
	{
		surface: driftBackup,
		entity:  "audit",
		source:  reflect.TypeOf(domain.AuditRow{}),
		target:  reflect.TypeOf(domain.AuditRow{}),
	},
	{
		surface: driftBackup,
		entity:  "market-data-instances",
		source:  reflect.TypeOf(domain.MarketDataInstance{}),
		target:  reflect.TypeOf(domain.MarketDataInstance{}),
	},
	{
		surface: driftBackup,
		entity:  "market-data-instruments",
		source:  reflect.TypeOf(domain.MarketDataInstrument{}),
		target:  reflect.TypeOf(backup.MarketDataInstrument{}),
	},
	{
		surface: driftBackup,
		entity:  "market-data-quotes",
		source:  reflect.TypeOf(domain.MarketDataQuote{}),
		target:  reflect.TypeOf(domain.MarketDataQuote{}),
	},
	{
		surface: driftBackup,
		entity:  "signing-keys",
		source:  reflect.TypeOf(domain.SigningKey{}),
		target:  reflect.TypeOf(backup.SigningKey{}),
	},
	{
		surface: driftBackup,
		entity:  "signing-config",
		source:  reflect.TypeOf(backup.SigningConfigEntry{}),
		target:  reflect.TypeOf(backup.SigningConfigEntry{}),
	},
	{
		surface: driftBackup,
		entity:  "user-settings",
		source:  reflect.TypeOf(domain.UserSetting{}),
		target:  reflect.TypeOf(domain.UserSetting{}),
	},
}

var businessCSVEntityContracts = []businessCSVEntityContract{
	{
		entity:     string(businesscsv.EntityAccountGroups),
		table:      "account_group",
		source:     reflect.TypeOf(domain.AccountGroup{}),
		keyColumns: []string{"code"},
		keyValues:  []string{"grp-1"},
	},
	{
		entity:     string(businesscsv.EntityAccounts),
		table:      "account",
		source:     reflect.TypeOf(domain.Account{}),
		keyColumns: []string{"code"},
		keyValues:  []string{"acc-1"},
		// A halted account carries no P&L number and an account with a number
		// is not halted, so the two columns are covered by one row each.
		omitColumns: []string{"pnl_halt_reason"},
		extraRows: []csvSentinelRow{{
			keyValues:   []string{"acc-halted"},
			omitColumns: []string{"pnl"},
		}},
	},
	{
		entity:     string(businesscsv.EntityPositions),
		table:      "balance",
		source:     reflect.TypeOf(domain.Balance{}),
		keyColumns: []string{"account_code", "asset"},
		keyValues:  []string{"acc-1", "USD"},
		// Realized P&L and its halt reason are mutually exclusive, like the
		// account-level accumulator above.
		omitColumns: []string{"realized_pnl_halt_reason"},
		extraRows: []csvSentinelRow{{
			keyValues:   []string{"acc-halted", "AAPL"},
			omitColumns: []string{"realized_pnl"},
		}},
	},
	{
		entity:     string(businesscsv.EntityOrders),
		table:      "order_record",
		source:     reflect.TypeOf(domain.Order{}),
		keyColumns: []string{"id"},
		keyValues:  []string{"order-sentinel-1"},
	},
	{
		entity:     string(businesscsv.EntityTrades),
		table:      "trade",
		source:     reflect.TypeOf(domain.Trade{}),
		keyColumns: []string{"order_id"},
		keyValues:  []string{"order-sentinel-1"},
	},
}

var expectedSchemaTableNames = []string{
	"account",
	"account_group",
	"adjustment",
	"adjustment_status",
	"asset",
	"asset_class",
	"attestation_alg",
	"attestation_mode",
	"attestation_request_type",
	"audit",
	"audit_action",
	"balance",
	"event_attestation",
	"execution_report",
	"execution_report_event",
	"limit_order_size",
	"limit_rate",
	"limit_spot_funds_pnl_bound",
	"market_data_instance",
	"market_data_instrument",
	"mcp_access",
	"order_amount_kind",
	"order_event",
	"order_event_type",
	"order_record",
	"order_side",
	"order_status",
	"principal",
	"realm",
	"signing_config",
	"signing_key",
	"source_kind",
	"trade",
	"user_setting",
}

var nonClientDataSchemaTables = map[string]string{
	"adjustment_status":        "closed-domain reference dictionary, not portable client data",
	"attestation_alg":          "closed-domain reference dictionary, not portable client data",
	"attestation_mode":         "closed-domain reference dictionary, not portable client data",
	"attestation_request_type": "closed-domain reference dictionary, not portable client data",
	"audit_action":             "closed-domain reference dictionary, not portable client data",
	"order_amount_kind":        "closed-domain reference dictionary, not portable client data",
	"order_event_type":         "closed-domain reference dictionary, not portable client data",
	"order_side":               "closed-domain reference dictionary, not portable client data",
	"order_status":             "closed-domain reference dictionary, not portable client data",
	"realm":                    "single-row realm identity metadata, not portable realm contents",
	"source_kind":              "closed-domain reference dictionary, not portable client data",
}

// schemaClientTables is the canonical client-data contract. Each SQLite column
// must be listed here exactly once for its table. A non-empty backup or
// businessCSV value names the portable field/column carrying it; an empty value
// means the surface intentionally does not carry that column and must be covered
// by an allowlist entry when the table participates in that surface.
var schemaClientTables = map[string]schemaSurfaceSpec{
	"asset": {
		backup: map[string]string{
			"code":     "Code",
			"title":    "Title",
			"class_id": "AssetClass",
		},
	},
	"asset_class": {
		backup: map[string]string{
			"code":  "Code",
			"title": "Title",
			"notes": "Notes",
		},
	},
	"principal": {
		backup: map[string]string{
			"code":  "Code",
			"title": "Title",
		},
	},
	"account_group": {
		backup: map[string]string{
			"code":              "Code",
			"title":             "Title",
			"currency_asset_id": "Currency",
			"notes":             "Notes",
			"blocked":           "Blocked",
			"block_reason":      "BlockReason",
		},
		businessCSV: map[string]string{
			"code":              "code",
			"title":             "title",
			"currency_asset_id": "currency",
			"notes":             "notes",
			"blocked":           "blocked",
			"block_reason":      "block_reason",
		},
	},
	"account": {
		backup: map[string]string{
			"code":              "Code",
			"title":             "Title",
			"group_id":          "GroupCode",
			"currency_asset_id": "Currency",
			"pnl":               "Pnl",
			"pnl_halt_reason":   "PnlHaltReason",
			"notes":             "Notes",
			"blocked":           "Blocked",
			"block_reason":      "BlockReason",
		},
		businessCSV: map[string]string{
			"code":              "code",
			"title":             "title",
			"group_id":          "group_code",
			"currency_asset_id": "currency",
			"pnl":               "pnl",
			"pnl_halt_reason":   "pnl_halt_reason",
			"notes":             "notes",
			"blocked":           "blocked",
			"block_reason":      "block_reason",
		},
	},
	"balance": {
		backup: map[string]string{
			"account_id":               "Account",
			"asset_id":                 "Asset",
			"available":                "Available",
			"held":                     "Held",
			"incoming":                 "Incoming",
			"average_entry_price":      "AverageEntryPrice",
			"realized_pnl":             "RealizedPnl",
			"realized_pnl_halt_reason": "RealizedPnlHaltReason",
			"updated_at":               "UpdatedAt",
		},
		businessCSV: map[string]string{
			"account_id":               "account_code",
			"asset_id":                 "asset",
			"available":                "available",
			"held":                     "held",
			"incoming":                 "incoming",
			"average_entry_price":      "average_entry_price",
			"realized_pnl":             "realized_pnl",
			"realized_pnl_halt_reason": "realized_pnl_halt_reason",
		},
	},
	"limit_rate": {
		backup: map[string]string{
			"scope":      "Scope",
			"account_id": "Account",
			"asset_id":   "Asset",
			"max_orders": "MaxOrders",
			"window":     "Window",
		},
	},
	"limit_order_size": {
		backup: map[string]string{
			"scope":        "Scope",
			"account_id":   "Account",
			"asset_id":     "Asset",
			"max_quantity": "MaxQuantity",
			"max_notional": "MaxNotional",
		},
	},
	"limit_spot_funds_pnl_bound": {
		backup: map[string]string{
			"scope":             "Scope",
			"account_id":        "Account",
			"account_group_id":  "AccountGroup",
			"currency_asset_id": "Currency",
			"lower_bound":       "LowerBound",
			"upper_bound":       "UpperBound",
		},
	},
	"adjustment": {
		backup: map[string]string{
			"external_id":  "ExternalID",
			"account_id":   "Account",
			"asset_id":     "Asset",
			"principal_id": "Principal",
			"at":           "At",
			"source_id":    "Source",
			"status_id":    "Accepted/Rejected",
			"request":      "Request",
			"outcome":      "Accepted/Rejected",
		},
	},
	"order_record": {
		backup: map[string]string{
			"external_id":     "ExternalID",
			"account_id":      "Account",
			"base_asset_id":   "BaseAsset",
			"quote_asset_id":  "QuoteAsset",
			"principal_id":    "Principal",
			"at":              "At",
			"source_id":       "Source",
			"side_id":         "Side",
			"amount_kind_id":  "AmountKind",
			"amount_value":    "AmountValue",
			"leaves_quantity": "Leaves",
			"price":           "Price",
			"status_id":       "Status",
			"drop_copy":       "DropCopy",
			"lock":            "Lock",
		},
		businessCSV: map[string]string{
			"external_id":     "id",
			"account_id":      "account_code",
			"base_asset_id":   "base_asset",
			"quote_asset_id":  "quote_asset",
			"principal_id":    "principal",
			"at":              "at",
			"source_id":       "source",
			"side_id":         "side",
			"amount_kind_id":  "amount_kind",
			"amount_value":    "amount_value",
			"leaves_quantity": "",
			"price":           "price",
			"status_id":       "status",
			"drop_copy":       "drop_copy",
			"lock":            "",
		},
	},
	"order_event": {
		backup: map[string]string{
			"external_id":  "ExternalID",
			"order_id":     "Order",
			"principal_id": "Principal",
			"at":           "At",
			"type_id":      "Type",
			"source_id":    "Source",
			"payload":      "Payload",
		},
	},
	"event_attestation": {
		backup: map[string]string{
			"event_id":        "OrderEvents.Attestation",
			"token":           "Attestation.Token",
			"signing_key_id":  "Attestation.KeyID",
			"alg_id":          "Attestation.Alg",
			"request_type_id": "Attestation.RequestType",
			"mode_id":         "Attestation.Mode",
			"issued_at":       "Attestation.IssuedAt",
		},
	},
	"execution_report": {
		backup: map[string]string{
			"external_id": "ExternalID",
			"order_id":    "Order",
			"at":          "At",
		},
	},
	"execution_report_event": {
		backup: map[string]string{
			"report_id": "Report",
			"event_id":  "Event",
		},
	},
	"trade": {
		backup: map[string]string{
			"external_id":         "ExternalID",
			"order_id":            "Order",
			"account_id":          "Account",
			"base_asset_id":       "BaseAsset",
			"quote_asset_id":      "QuoteAsset",
			"principal_id":        "Principal",
			"at":                  "At",
			"source_id":           "Source",
			"side_id":             "Side",
			"quantity":            "Quantity",
			"price":               "Price",
			"lock_price":          "LockPrice",
			"commission_amount":   "Commission.Amount",
			"commission_currency": "Commission.Currency",
		},
		businessCSV: map[string]string{
			"external_id":         "id",
			"order_id":            "order_id",
			"account_id":          "account_code",
			"base_asset_id":       "base_asset",
			"quote_asset_id":      "quote_asset",
			"principal_id":        "principal",
			"at":                  "at",
			"source_id":           "source",
			"side_id":             "side",
			"quantity":            "quantity",
			"price":               "price",
			"lock_price":          "lock_price",
			"commission_amount":   "commission_amount",
			"commission_currency": "commission_currency",
		},
	},
	"audit": {
		backup: map[string]string{
			"external_id":   "ExternalID",
			"account_code":  "Account",
			"account_title": "AccountTitle",
			"asset_code":    "Asset",
			"group_code":    "Group",
			"actor_code":    "Actor",
			"actor_title":   "ActorTitle",
			"at":            "At",
			"action_id":     "Action",
			"source_id":     "Source",
			"detail":        "Detail",
		},
	},
	"market_data_instance": {
		backup: map[string]string{
			"external_id": "ExternalID",
			"provider":    "Provider",
			"label":       "Label",
			"credentials": "Credentials",
			"enabled":     "Enabled",
		},
	},
	"market_data_instrument": {
		backup: map[string]string{
			"instance_id":     "Instance",
			"external_symbol": "ExternalSymbol",
			"base_asset_id":   "BaseAsset",
			"quote_asset_id":  "QuoteAsset",
			"enabled":         "Enabled",
			"manual_price":    "ManualPrice",
		},
	},
	"signing_key": {
		backup: map[string]string{
			"key_id":      "KeyID",
			"alg":         "Alg",
			"private_key": "PrivateKey",
			"public_key":  "PublicKey",
			"created_at":  "CreatedAt",
			"active":      "Active",
		},
	},
	"signing_config": {
		backup: map[string]string{
			"key":   "Key",
			"value": "Value",
		},
	},
	"mcp_access": {
		backup: map[string]string{
			"command": "map key",
			"enabled": "map value",
		},
	},
	"user_setting": {
		backup: map[string]string{
			"user_id":       "UserID",
			"setting_key":   "Key",
			"setting_value": "Value",
		},
	},
}

var businessCSVColumnSpecs = buildBusinessCSVColumnSpecs()

var businessCSVSentinelSpecs = buildBusinessCSVSentinelSpecs()

func TestClientDataBackupRoundTripDrift(t *testing.T) {
	ctx := context.Background()
	_, source := newClientDataDriftRealm(t, ctx)
	seedClientDataDriftRealm(t, ctx, source)

	sourceArchive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup source: %v", err)
	}

	var findings []clientDataDrift
	findings = append(findings, nonZeroBackupFindings(sourceArchive.Data)...)

	_, target := newClientDataDriftRealm(t, ctx)
	_, err = target.RestoreBackup(ctx, sourceArchive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	})
	if err != nil {
		t.Fatalf("RestoreBackup target: %v", err)
	}
	targetArchive, err := target.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup target: %v", err)
	}
	findings = append(findings, diffValue(
		driftBackup, "archive", "", sourceArchive.Data, targetArchive.Data,
	)...)
	reportClientDataDrift(t, findings)
}

func TestClientDataBusinessCSVExportDrift(t *testing.T) {
	ctx := context.Background()
	_, source := newClientDataDriftRealm(t, ctx)
	seedClientDataDriftRealm(t, ctx, source)

	var findings []clientDataDrift
	for _, entity := range []businesscsv.Entity{
		businesscsv.EntityAccountGroups,
		businesscsv.EntityAccounts,
		businesscsv.EntityPositions,
		businesscsv.EntityOrders,
		businesscsv.EntityTrades,
	} {
		body, err := encodeBusinessCSVFromStore(ctx, source, entity)
		if err != nil {
			t.Fatalf("encode business CSV %s: %v", entity, err)
		}
		findings = append(findings, csvNonZeroFindings(string(entity), body)...)
	}
	reportClientDataDrift(t, findings)
}

// TestClientDataBusinessCSVSentinelRowsCoverEveryColumn keeps the per-row
// omitColumns escape hatch honest: splitting a mutually exclusive pair across
// two sentinel rows is allowed, dropping a column from every row is not, since
// that would leave the column unexercised while the export drift check still
// passed.
func TestClientDataBusinessCSVSentinelRowsCoverEveryColumn(t *testing.T) {
	for _, contract := range businessCSVEntityContracts {
		surface := schemaClientTables[contract.table]
		if surface.businessCSV == nil {
			continue
		}
		covered := map[string]bool{}
		for _, spec := range businessCSVSentinelSpecs[contract.entity] {
			for _, column := range spec.requiredColumns {
				covered[column] = true
			}
		}
		for _, column := range surface.businessCSV {
			if column == "" || covered[column] {
				continue
			}
			t.Errorf(
				"business CSV %s column %q is required by no sentinel row",
				contract.entity, column,
			)
		}
	}
}

func encodeBusinessCSVFromStore(
	ctx context.Context,
	realm store.RealmStore,
	entity businesscsv.Entity,
) ([]byte, error) {
	const delimiter = businesscsv.DelimiterSemicolon
	switch entity {
	case businesscsv.EntityAccountGroups:
		groups, err := realm.ListGroups(ctx)
		if err != nil {
			return nil, err
		}
		filtered := groups[:0]
		for _, group := range groups {
			if group.Code != "" {
				filtered = append(filtered, group)
			}
		}
		return businesscsv.EncodeGroups(filtered, delimiter)
	case businesscsv.EntityAccounts:
		rows, err := realm.ListAccounts(ctx)
		if err != nil {
			return nil, err
		}
		return businesscsv.EncodeAccounts(rows, delimiter)
	case businesscsv.EntityPositions:
		rows, err := realm.ListBalances(ctx, "", "")
		if err != nil {
			return nil, err
		}
		return businesscsv.EncodePositions(rows, delimiter)
	case businesscsv.EntityOrders:
		rows, err := realm.ListAllOrders(ctx, "", "")
		if err != nil {
			return nil, err
		}
		return businesscsv.EncodeOrders(rows, delimiter, storedLockPrice)
	case businesscsv.EntityTrades:
		rows, err := realm.ListAllTrades(ctx, "", "")
		if err != nil {
			return nil, err
		}
		return businesscsv.EncodeTrades(rows, delimiter)
	default:
		return nil, fmt.Errorf("unknown business CSV entity %q", entity)
	}
}

func storedLockPrice(order domain.Order) string {
	if len(order.Lock) == 0 {
		return ""
	}
	lock, err := pretrade.NewLockFromMsgPack(order.Lock)
	if err != nil {
		return ""
	}
	prices, err := lock.Prices()
	if err != nil || len(prices) != 1 {
		return ""
	}
	return prices[0].String()
}

func TestClientDataStructParityDrift(t *testing.T) {
	var findings []clientDataDrift
	for _, spec := range backupParitySpecs {
		findings = append(findings, compareStructParity(spec)...)
	}
	for _, spec := range businessCSVColumnSpecs {
		findings = append(findings, compareCSVColumnParity(spec)...)
	}
	reportClientDataDrift(t, findings)
}

func TestClientDataSchemaCoverageDrift(t *testing.T) {
	tables, err := parseClientDataSchema()
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	findings := clientDataTableCompletenessFindings(tables)
	for table, columns := range tables {
		spec, ok := schemaClientTables[table]
		if !ok {
			continue
		}
		for _, column := range columns {
			if isAllowed(driftSchema, table, column, "internal") {
				continue
			}
			if isAllowed(driftSchema, table, column, "business-csv-uncovered") {
				continue
			}
			if spec.backup == nil || spec.backup[column] == "" {
				findings = append(findings, clientDataDrift{
					Surface: driftSchema,
					Entity:  table,
					Field:   column,
					Kind:    "backup-uncovered",
					Detail:  "schema column has no backup representation",
				})
			}
			if spec.businessCSV != nil && spec.businessCSV[column] == "" {
				findings = append(findings, clientDataDrift{
					Surface: driftSchema,
					Entity:  table,
					Field:   column,
					Kind:    "business-csv-uncovered",
					Detail:  "schema column has no business CSV representation",
				})
			}
		}
	}
	reportClientDataDrift(t, findings)
}

func TestClientDataSchemaParserVariants(t *testing.T) {
	tables, err := parseClientDataSchemaSQL(`
CREATE TABLE IF NOT EXISTS account (
    id INTEGER,
    code TEXT NOT NULL,
    PRIMARY KEY (id)
);
CREATE TABLE "quoted_table" (
    "quoted_column" TEXT,
    [display name] TEXT,
    ` + "`tick_column`" + ` TEXT,
    CHECK (quoted_column <> '')
);
CREATE TABLE [bracketed_table] (
    id INTEGER
);
CREATE TABLE ` + "`backtick_table`" + ` (
    id INTEGER
);
`)
	if err != nil {
		t.Fatalf("parse variant schema: %v", err)
	}
	want := map[string][]string{
		"account":         {"id", "code"},
		"quoted_table":    {"quoted_column", "display name", "tick_column"},
		"bracketed_table": {"id"},
		"backtick_table":  {"id"},
	}
	if !reflect.DeepEqual(tables, want) {
		t.Fatalf("tables mismatch:\nwant %#v\ngot  %#v", want, tables)
	}
}

func TestClientDataSchemaParserRejectsMalformedCreateTable(t *testing.T) {
	_, err := parseClientDataSchemaSQL("CREATE TABLE (\n    id INTEGER\n);\n")
	if err == nil {
		t.Fatalf("expected malformed CREATE TABLE to fail")
	}
}

func TestClientDataTableCompletenessFindings(t *testing.T) {
	tables := map[string][]string{
		"asset":              {"id", "code"},
		"realm":              {"id", "code"},
		"unregistered_table": {"id", "client_value"},
	}

	findings := clientDataTableCompletenessFindings(tables)
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %#v", findings)
	}
	finding := findings[0]
	if finding.Entity != "unregistered_table" ||
		finding.Kind != "table-uncovered" {
		t.Fatalf("unexpected finding: %#v", finding)
	}
}

func newClientDataDriftRealm(
	t *testing.T, ctx context.Context,
) (store.Store, store.RealmStore) {
	t.Helper()
	st, err := sqlite.New(t.TempDir() + "/client-data-drift.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	return st, realm
}

func seedClientDataDriftRealm(
	t *testing.T, ctx context.Context, rs store.RealmStore,
) {
	t.Helper()
	must(t, "CreateAssetClass equity", rs.CreateAssetClass(ctx, domain.AssetClass{
		Code:  "equity",
		Title: "Equity Sentinel",
		Notes: "asset class notes sentinel",
	}))
	if _, err := rs.CreateAsset(ctx, domain.Asset{
		Code:       "USD",
		Title:      "US Dollar Sentinel",
		AssetClass: "equity",
	}); err != nil {
		t.Fatalf("CreateAsset USD: %v", err)
	}
	if _, err := rs.CreateAsset(ctx, domain.Asset{
		Code:       "AAPL",
		Title:      "Apple Sentinel",
		AssetClass: "equity",
	}); err != nil {
		t.Fatalf("CreateAsset AAPL: %v", err)
	}
	mustAllowExists(t, "CreatePrincipal", rs.CreatePrincipal(ctx, domain.Principal{
		Code:  "operator",
		Title: "Operator Sentinel",
	}))
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code:        "grp-1",
		Title:       "Desk Sentinel",
		Currency:    "USD",
		Notes:       "group notes sentinel",
		BlockReason: "group block sentinel",
		Blocked:     true,
	}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	must(t, "SetGroupCurrency default", rs.SetGroupCurrency(ctx, "", "USD"))
	// Pnl and PnlHaltReason are mutually exclusive by design: a halted
	// accumulator has no numeric value, so the store writes pnl = NULL whenever
	// a halt reason is set. One account cannot carry both sentinels, so the
	// pair is split across two accounts and each surface covers the union.
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code:        "acc-1",
		Title:       "Account Sentinel",
		Currency:    "USD",
		Pnl:         "12.34",
		GroupCode:   "grp-1",
		Notes:       "account notes sentinel",
		BlockReason: "account block sentinel",
		Blocked:     true,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code:          "acc-halted",
		Title:         "Halted Account Sentinel",
		Currency:      "USD",
		PnlHaltReason: domain.PnlHaltReasonMissingFx,
		GroupCode:     "grp-1",
		Notes:         "halted account notes sentinel",
		BlockReason:   "halted account block sentinel",
		Blocked:       true,
	}); err != nil {
		t.Fatalf("CreateAccount halted: %v", err)
	}
	must(t, "UpsertBalance", rs.UpsertBalance(ctx, domain.Balance{
		Account:           "acc-1",
		Asset:             "USD",
		Available:         "123.45",
		Held:              "6.78",
		Incoming:          "9.01",
		RealizedPnl:       "2.34",
		AverageEntryPrice: "101.23",
		UpdatedAt:         time.Date(2026, 7, 8, 10, 11, 12, 0, time.UTC),
	}))
	must(t, "UpsertBalance halted", rs.UpsertBalance(ctx, domain.Balance{
		Account:               "acc-halted",
		Asset:                 "AAPL",
		Available:             "223.45",
		Held:                  "16.78",
		Incoming:              "19.01",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
		AverageEntryPrice:     "201.23",
		UpdatedAt:             time.Date(2026, 7, 8, 10, 12, 12, 0, time.UTC),
	}))
	must(t, "PutRateLimit", rs.PutRateLimit(ctx, domain.LimitRate{
		Scope:     domain.ScopeAccountAsset,
		Account:   "acc-1",
		Asset:     "AAPL",
		MaxOrders: 17,
		Window:    3 * time.Minute,
	}))
	must(t, "PutOrderSizeLimit", rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope:       domain.ScopeAccountAsset,
		Account:     "acc-1",
		Asset:       "AAPL",
		MaxQuantity: "18.5",
		MaxNotional: "2500.75",
	}))
	must(t, "PutSpotFundsPnlBoundsLimit", rs.PutSpotFundsPnlBoundsLimit(
		ctx,
		domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeAccount,
			Account:    "acc-1",
			Currency:   "USD",
			LowerBound: "-50.25",
			UpperBound: "100.75",
		},
	))
	must(t, "PutSpotFundsPnlBoundsLimit group", rs.PutSpotFundsPnlBoundsLimit(
		ctx,
		domain.LimitSpotFundsPnlBounds{
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: "grp-1",
			Currency:     "USD",
			LowerBound:   "-25.00",
			UpperBound:   "75.00",
		},
	))
	order, err := rs.CreateOrder(ctx, domain.Order{
		ExternalID:  "order-sentinel-1",
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Principal:   "operator",
		Source:      domain.SourcePanel,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "11.25",
		Leaves:      "4.25",
		Price:       "151.25",
		Status:      domain.OrderStatusFilled,
		DropCopy:    true,
		Lock:        clientDataDriftLock(t, "151.00"),
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	event, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:     order.ExternalID,
		Principal: "operator",
		Source:    domain.SourcePanel,
		Type:      domain.OrderEventFill,
		Payload: domain.OrderEventPayload{
			FillQuantity:   "7.00",
			FillPrice:      "151.25",
			FillLockPrice:  "151.00",
			LeavesQuantity: "4.25",
			OrderStatus:    string(domain.OrderStatusFilled),
			Commission:     &domain.Commission{Amount: "-0.30", Currency: "USDT"},
			Rejects: []domain.OrderReject{{
				Code:    "sentinel_reject",
				Scope:   "account",
				Policy:  "sentinel_policy",
				Reason:  "sentinel reject",
				Details: "sentinel reject details",
			}},
			ExecutionReport: domain.ExecutionReportRequestFromInput(
				domain.ExecutionReportInput{
					ExternalID:     "sentinel-report-id",
					BaseAsset:      "SENTINEL_BASE",
					QuoteAsset:     "SENTINEL_QUOTE",
					FillQuantity:   "7.00",
					FillPrice:      "151.25",
					LeavesQuantity: "4.25",
					LockPrice:      "151.00",
					Lock:           []byte{0x04, 0x05, 0x06},
					Commission:     &domain.Commission{Amount: "-0.30", Currency: "USDT"},
					Order:          order.ExternalID,
					Account:        "sentinel-account",
					Side:           domain.OrderSideBuy,
					OrderStatus:    domain.OrderStatusFilled,
					Force:          true,
				},
			),
		},
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	rejectEvent, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:     order.ExternalID,
		Principal: "operator",
		Source:    domain.SourcePanel,
		Type:      domain.OrderEventPreTradeRejected,
		Payload: domain.OrderEventPayload{
			RejectCode:    "limit_exceeded",
			RejectScope:   "account",
			RejectPolicy:  "order_size",
			RejectReason:  "order too large",
			RejectDetails: "sentinel reject details",
			Rejects: []domain.OrderReject{{
				Code:    "limit_exceeded",
				Scope:   "account",
				Policy:  "order_size",
				Reason:  "order too large",
				Details: "sentinel reject details",
			}},
		},
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent(reject): %v", err)
	}
	must(t, "UpsertSigningKey", rs.UpsertSigningKey(ctx, domain.SigningKey{
		CreatedAt:  time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC),
		KeyID:      "key-sentinel-1",
		Alg:        "ed25519",
		PublicKey:  bytes.Repeat([]byte{0x11}, 32),
		PrivateKey: bytes.Repeat([]byte{0x22}, 32),
		Active:     true,
	}))
	must(t, "PutEventAttestation", rs.PutEventAttestation(
		ctx, event.ExternalID, domain.EventAttestation{
			Token:       "token-sentinel",
			KeyID:       "key-sentinel-1",
			Alg:         "ed25519",
			RequestType: domain.AttestationRequestSubmit,
			Mode:        "immediate",
			IssuedAt:    "2026-07-08T10:01:00Z",
		},
	))
	must(t, "PutEventAttestation reject", rs.PutEventAttestation(
		ctx, rejectEvent.ExternalID, domain.EventAttestation{
			Token:       "token-reject-sentinel",
			KeyID:       "key-sentinel-1",
			Alg:         "ed25519",
			RequestType: domain.AttestationRequestSubmit,
			Mode:        "immediate",
			IssuedAt:    "2026-07-08T10:01:30Z",
		},
	))
	if _, err := rs.CreateTrade(ctx, domain.Trade{
		Order:      order.ExternalID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Principal:  "operator",
		Source:     domain.SourcePanel,
		Side:       domain.OrderSideBuy,
		Quantity:   "7.00",
		Price:      "151.25",
		LockPrice:  "151.00",
		Commission: &domain.Commission{Amount: "-0.42", Currency: "USD"},
	}); err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	if _, err := rs.AppendAdjustment(ctx, domain.AccountAdjustmentRecord{
		ExternalID: "adjustment-sentinel-1",
		Account:    "acc-1",
		Asset:      "USD",
		Principal:  "operator",
		Source:     domain.SourcePanel,
		Request: domain.AdjustmentRequest{
			Asset:                 "USD",
			AverageEntryPrice:     "101.23",
			Balance:               adjustmentAmount(domain.AdjustmentModeDelta, "5.50"),
			BalanceBounds:         adjustmentBounds("1", "200"),
			Held:                  adjustmentAmount(domain.AdjustmentModeDelta, "1.25"),
			HeldBounds:            adjustmentBounds("1", "20"),
			Incoming:              adjustmentAmount(domain.AdjustmentModeDelta, "2.25"),
			IncomingBounds:        adjustmentBounds("1", "30"),
			RealizedPnl:           "2.84",
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
		},
		Accepted: &domain.AdjustmentOutcomeAccepted{
			BalanceDelta:          "5.50",
			BalanceResult:         "128.95",
			HeldDelta:             "1.25",
			HeldResult:            "8.03",
			IncomingDelta:         "2.25",
			IncomingResult:        "11.26",
			RealizedPnlDelta:      "0.50",
			RealizedPnlResult:     "2.84",
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
			AverageEntryPrice:     "99.5",
		},
	}); err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}
	if _, err := rs.AppendAdjustment(ctx, domain.AccountAdjustmentRecord{
		ExternalID: "adjustment-reject-sentinel-1",
		Account:    "acc-1",
		Asset:      "USD",
		Principal:  "operator",
		Source:     domain.SourceAPI,
		Request: domain.AdjustmentRequest{
			Asset:                 "USD",
			AverageEntryPrice:     "102.34",
			Balance:               adjustmentAmount(domain.AdjustmentModeDelta, "6.50"),
			BalanceBounds:         adjustmentBounds("1", "200"),
			Held:                  adjustmentAmount(domain.AdjustmentModeDelta, "2.25"),
			HeldBounds:            adjustmentBounds("1", "20"),
			Incoming:              adjustmentAmount(domain.AdjustmentModeDelta, "3.25"),
			IncomingBounds:        adjustmentBounds("1", "30"),
			RealizedPnl:           "3.84",
			RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
		},
		Rejected: &domain.AdjustmentOutcomeRejected{
			Code:                  "limit_exceeded",
			Scope:                 "account",
			Policy:                "manual_adjustment",
			Reason:                "sentinel rejected adjustment",
			Details:               "rejected adjustment details sentinel",
			FailedAdjustmentIndex: 1,
		},
	}); err != nil {
		t.Fatalf("AppendAdjustment(rejected): %v", err)
	}
	must(t, "AppendAudit", rs.AppendAudit(ctx, store.AuditEntry{
		Actor:        "operator",
		ActorTitle:   "Operator Sentinel",
		Action:       domain.AuditActionCreateAccount,
		Account:      "acc-1",
		AccountTitle: "Account Sentinel",
		Asset:        "USD",
		Group:        "grp-1",
		Detail:       "audit detail sentinel",
		Source:       domain.SourcePanel,
	}))
	inst, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		ExternalID:  "md-instance-sentinel-1",
		Provider:    domain.MarketDataProviderBYO,
		Label:       "Manual Sentinel",
		Credentials: `{"token":"sentinel"}`,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	must(t, "UpsertMarketDataInstrument", rs.UpsertMarketDataInstrument(
		ctx, domain.MarketDataInstrument{
			Instance:       inst.ExternalID,
			ExternalSymbol: "AAPL/USD",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
			ManualPrice:    "151.50",
			Enabled:        true,
		},
	))
	must(t, "SetSigningConfig", rs.SetSigningConfig(ctx, "no_esign", "0"))
	must(t, "SetMcpAccess", rs.SetMcpAccess(ctx, "submit_order", true))
	must(t, "SetUserSetting", rs.SetUserSetting(
		ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen, "1",
	))
}

func buildBusinessCSVColumnSpecs() []csvColumnSpec {
	out := make([]csvColumnSpec, 0, len(businessCSVEntityContracts))
	for _, contract := range businessCSVEntityContracts {
		surface := schemaClientTables[contract.table]
		if surface.businessCSV == nil {
			continue
		}
		spec := csvColumnSpec{
			entity:  contract.entity,
			source:  contract.source,
			fields:  make(map[string]string),
			omitted: make(map[string]string),
		}
		for column, field := range surface.backup {
			if field == "" {
				continue
			}
			csvColumn, ok := surface.businessCSV[column]
			if ok && csvColumn != "" {
				spec.fields[field] = csvColumn
				continue
			}
			spec.omitted[field] = "not carried by the business CSV contract"
		}
		out = append(out, spec)
	}
	return out
}

func buildBusinessCSVSentinelSpecs() map[string][]csvSentinelSpec {
	out := make(map[string][]csvSentinelSpec, len(businessCSVEntityContracts))
	for _, contract := range businessCSVEntityContracts {
		surface := schemaClientTables[contract.table]
		if surface.businessCSV == nil {
			continue
		}
		columns := make([]string, 0, len(surface.businessCSV))
		for _, column := range surface.businessCSV {
			if column != "" {
				columns = append(columns, column)
			}
		}
		sort.Strings(columns)
		rows := append(
			[]csvSentinelRow{{
				keyValues:   contract.keyValues,
				omitColumns: contract.omitColumns,
			}},
			contract.extraRows...,
		)
		specs := make([]csvSentinelSpec, 0, len(rows))
		for _, row := range rows {
			specs = append(specs, csvSentinelSpec{
				keyColumns:      contract.keyColumns,
				keyValues:       row.keyValues,
				requiredColumns: withoutColumns(columns, row.omitColumns),
			})
		}
		out[contract.entity] = specs
	}
	return out
}

func withoutColumns(columns, omit []string) []string {
	if len(omit) == 0 {
		return columns
	}
	out := make([]string, 0, len(columns))
	for _, column := range columns {
		if !slices.Contains(omit, column) {
			out = append(out, column)
		}
	}
	return out
}

func compareStructParity(spec paritySpec) []clientDataDrift {
	targetFields := exportedFieldMap(spec.target)
	var findings []clientDataDrift
	for _, field := range exportedFields(spec.source) {
		if isAllowed(spec.surface, spec.entity, field.Name, "uncovered") {
			continue
		}
		targetName := field.Name
		var target reflect.StructField
		var ok bool
		if len(spec.fields) > 0 {
			targetName = spec.fields[field.Name]
			if targetName != "" {
				target, ok = fieldByPath(spec.target, targetName)
			}
		} else {
			target, ok = targetFields[field.Name]
		}
		if !ok {
			findings = append(findings, clientDataDrift{
				Surface: spec.surface,
				Entity:  spec.entity,
				Field:   field.Name,
				Kind:    "uncovered",
				Detail:  "domain/store field has no portable struct field",
			})
			continue
		}
		if !typesCompatible(field.Type, target.Type) {
			findings = append(findings, clientDataDrift{
				Surface: spec.surface,
				Entity:  spec.entity,
				Field:   field.Name,
				Kind:    "type-mismatch",
				Detail: fmt.Sprintf(
					"%s -> %s (%s)",
					field.Type,
					target.Type,
					targetName,
				),
			})
		}
	}
	return findings
}

func compareCSVColumnParity(spec csvColumnSpec) []clientDataDrift {
	var findings []clientDataDrift
	for _, field := range exportedFields(spec.source) {
		if isAllowed(driftBusinessCSV, spec.entity, field.Name, "uncovered") {
			continue
		}
		if spec.fields[field.Name] == "" {
			if spec.omitted[field.Name] != "" {
				continue
			}
			findings = append(findings, clientDataDrift{
				Surface: driftBusinessCSV,
				Entity:  spec.entity,
				Field:   field.Name,
				Kind:    "uncovered",
				Detail:  "domain/store field has no business CSV column",
			})
		}
	}
	return findings
}

func nonZeroBackupFindings(data backup.Data) []clientDataDrift {
	checks := []struct {
		entity string
		value  any
	}{
		{"asset-classes", data.AssetClasses},
		{"assets", data.Assets},
		{"principals", data.Principals},
		{"account-groups", data.Groups},
		{"accounts", data.Accounts},
		{"positions", data.Balances},
		{"rate-limits", data.RateLimits},
		{"order-size-limits", data.OrderSizeLimits},
		{"spot-funds-pnl-bounds-limits", data.SpotFundsPnlBoundsLimits},
		{"adjustments", data.Adjustments},
		{"orders", data.Orders},
		{"order-events", data.OrderEvents},
		{"trades", data.Trades},
		{"audit", data.Audit},
		{"market-data-instances", data.MarketDataInstances},
		{"market-data-instruments", data.MarketDataInstruments},
		{"signing-keys", data.SigningKeys},
		{"signing-config", data.SigningConfig},
		{"mcp-access", data.McpAccess},
		{"user-settings", data.UserSettings},
	}
	var findings []clientDataDrift
	for _, check := range checks {
		findings = append(findings, nonZeroFindings(
			driftBackup, check.entity, check.entity, reflect.ValueOf(check.value),
		)...)
	}
	return findings
}

func nonZeroFindings(
	surface, entity, path string, value reflect.Value,
) []clientDataDrift {
	walk := sentinelNonZeroWalk{
		surface: surface,
		entity:  entity,
		nonZero: map[string]bool{},
	}
	walk.visit(path, value)

	var findings []clientDataDrift
	for _, finding := range walk.zeros {
		normalized := normalizeSentinelPath(finding.Field)
		if isAllowed(surface, entity, normalized, "excluded-sentinel") {
			continue
		}
		if isAllowed(surface, entity, normalized, "optional-sentinel") &&
			walk.nonZero[normalized] {
			continue
		}
		findings = append(findings, finding)
	}
	return findings
}

type sentinelNonZeroWalk struct {
	surface string
	entity  string
	nonZero map[string]bool
	zeros   []clientDataDrift
}

func (w *sentinelNonZeroWalk) visit(path string, value reflect.Value) {
	if !value.IsValid() {
		w.recordZero(path, "invalid reflected value")
		return
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			w.recordZero(path, "sentinel value is nil")
			return
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			w.recordZero(path, "sentinel pointer is nil")
			return
		}
		w.recordNonZero(path)
		w.visit(path, value.Elem())
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			w.recordLeaf(path, value)
			return
		}
		if value.IsZero() {
			w.recordZero(path, "sentinel struct is zero/default")
		} else {
			w.recordNonZero(path)
		}
		valueType := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := valueType.Field(i)
			if field.IsExported() {
				w.visit(joinField(path, field.Name), value.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Len() == 0 {
			w.recordZero(path, "sentinel collection is empty")
			return
		}
		w.recordNonZero(path)
		for i := 0; i < value.Len(); i++ {
			w.visit(fmt.Sprintf("%s[%d]", path, i), value.Index(i))
		}
	case reflect.Map:
		if value.Len() == 0 {
			w.recordZero(path, "sentinel map is empty")
			return
		}
		w.recordNonZero(path)
		for _, key := range value.MapKeys() {
			keyPath := joinField(path, fmt.Sprintf("%v", key.Interface()))
			w.recordLeaf(joinField(path, "<key>"), key)
			w.visit(keyPath, value.MapIndex(key))
		}
	default:
		w.recordLeaf(path, value)
	}
}

func (w *sentinelNonZeroWalk) recordLeaf(path string, value reflect.Value) {
	if value.IsZero() {
		w.recordZero(path, "sentinel field is zero/default")
		return
	}
	w.recordNonZero(path)
}

func (w *sentinelNonZeroWalk) recordZero(path, detail string) {
	w.zeros = append(w.zeros, clientDataDrift{
		Surface: w.surface,
		Entity:  w.entity,
		Field:   path,
		Kind:    "uncovered",
		Detail:  detail,
	})
}

func (w *sentinelNonZeroWalk) recordNonZero(path string) {
	w.nonZero[normalizeSentinelPath(path)] = true
}

func csvNonZeroFindings(entity string, body []byte) []clientDataDrift {
	header, rows, err := readBusinessCSVRows(body)
	if err != nil {
		return []clientDataDrift{{
			Surface: driftBusinessCSV,
			Entity:  entity,
			Field:   "body",
			Kind:    "parse-error",
			Detail:  err.Error(),
		}}
	}
	specs := businessCSVSentinelSpecs[entity]
	if len(specs) == 0 {
		return nil
	}
	if len(rows) == 0 {
		return []clientDataDrift{{
			Surface: driftBusinessCSV,
			Entity:  entity,
			Field:   "rows",
			Kind:    "uncovered",
			Detail:  "CSV sentinel export has no data rows",
		}}
	}
	headerIndex := csvHeaderIndex(header)
	var findings []clientDataDrift
	for _, spec := range specs {
		row, ok := findCSVSentinelRow(rows, headerIndex, spec)
		if !ok {
			findings = append(findings, clientDataDrift{
				Surface: driftBusinessCSV,
				Entity:  entity,
				Field:   strings.Join(spec.keyColumns, "+"),
				Kind:    "uncovered",
				Detail:  "seeded sentinel row is missing from export",
			})
			continue
		}
		for _, column := range spec.requiredColumns {
			colIdx, ok := headerIndex[column]
			if !ok {
				findings = append(findings, clientDataDrift{
					Surface: driftBusinessCSV,
					Entity:  entity,
					Field:   column,
					Kind:    "uncovered",
					Detail:  "expected CSV column is missing",
				})
				continue
			}
			if colIdx >= len(row) || row[colIdx] == "" {
				findings = append(findings, clientDataDrift{
					Surface: driftBusinessCSV,
					Entity:  entity,
					Field:   column,
					Kind:    "uncovered",
					Detail:  "seeded sentinel row exported a zero/default value",
				})
			}
		}
	}
	return findings
}

func readBusinessCSVRows(body []byte) ([]string, [][]string, error) {
	reader := csv.NewReader(bytes.NewReader(body))
	reader.Comma = ';'
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, fmt.Errorf("empty CSV")
	}
	return records[0], records[1:], nil
}

func csvHeaderIndex(header []string) map[string]int {
	out := make(map[string]int, len(header))
	for idx, column := range header {
		out[column] = idx
	}
	return out
}

func findCSVSentinelRow(
	rows [][]string,
	headerIndex map[string]int,
	spec csvSentinelSpec,
) ([]string, bool) {
	for _, row := range rows {
		matches := true
		for i, column := range spec.keyColumns {
			colIdx, ok := headerIndex[column]
			if !ok || colIdx >= len(row) || row[colIdx] != spec.keyValues[i] {
				matches = false
				break
			}
		}
		if matches {
			return row, true
		}
	}
	return nil, false
}

func diffValue(
	surface, entity, path string, want, got any,
) []clientDataDrift {
	if reflect.DeepEqual(want, got) {
		return nil
	}
	return diffReflect(
		surface, entity, path, reflect.ValueOf(want), reflect.ValueOf(got),
	)
}

func diffReflect(
	surface, entity, path string, want, got reflect.Value,
) []clientDataDrift {
	if !want.IsValid() || !got.IsValid() {
		if want.IsValid() == got.IsValid() {
			return nil
		}
		return []clientDataDrift{{
			Surface: surface, Entity: entity, Field: path, Kind: "dropped",
			Detail: fmt.Sprintf("want valid=%v got valid=%v", want.IsValid(), got.IsValid()),
		}}
	}
	if want.Type() != got.Type() {
		return []clientDataDrift{{
			Surface: surface, Entity: entity, Field: path, Kind: "type-mismatch",
			Detail: fmt.Sprintf("%s -> %s", want.Type(), got.Type()),
		}}
	}
	if reflect.DeepEqual(want.Interface(), got.Interface()) {
		return nil
	}
	for want.Kind() == reflect.Pointer || want.Kind() == reflect.Interface {
		if want.IsNil() || got.IsNil() {
			return []clientDataDrift{{
				Surface: surface, Entity: entity, Field: path, Kind: "dropped",
				Detail: fmt.Sprintf("want nil=%v got nil=%v", want.IsNil(), got.IsNil()),
			}}
		}
		want = want.Elem()
		got = got.Elem()
	}
	switch want.Kind() {
	case reflect.Struct:
		var findings []clientDataDrift
		for i := 0; i < want.NumField(); i++ {
			field := want.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			next := joinField(path, field.Name)
			findings = append(findings, diffReflect(
				surface, entity, next, want.Field(i), got.Field(i),
			)...)
		}
		return findings
	case reflect.Slice, reflect.Array:
		if want.Len() != got.Len() {
			return []clientDataDrift{{
				Surface: surface, Entity: entity, Field: path, Kind: "dropped",
				Detail: fmt.Sprintf("want len=%d got len=%d", want.Len(), got.Len()),
			}}
		}
		var findings []clientDataDrift
		for i := 0; i < want.Len(); i++ {
			findings = append(findings, diffReflect(
				surface, entity, fmt.Sprintf("%s[%d]", path, i),
				want.Index(i), got.Index(i),
			)...)
		}
		return findings
	case reflect.Map:
		var findings []clientDataDrift
		for _, key := range want.MapKeys() {
			gotValue := got.MapIndex(key)
			if !gotValue.IsValid() {
				findings = append(findings, clientDataDrift{
					Surface: surface,
					Entity:  entity,
					Field:   joinField(path, fmt.Sprintf("%v", key.Interface())),
					Kind:    "dropped",
					Detail:  "map key missing after round-trip",
				})
				continue
			}
			findings = append(findings, diffReflect(
				surface, entity, joinField(path, fmt.Sprintf("%v", key.Interface())),
				want.MapIndex(key), gotValue,
			)...)
		}
		return findings
	default:
		return []clientDataDrift{{
			Surface: surface, Entity: entity, Field: path, Kind: "dropped",
			Detail: fmt.Sprintf("want=%v got=%v", want.Interface(), got.Interface()),
		}}
	}
}

func parseClientDataSchema() (map[string][]string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("resolve test file path")
	}
	path := filepath.Join(
		filepath.Dir(file), "..", "..", "framework", "store", "schema", "migrations", "0001_init.sql",
	)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tables, err := parseClientDataSchemaSQL(string(raw))
	if err != nil {
		return nil, err
	}
	if err := checkExpectedSchemaTables(tables); err != nil {
		return nil, err
	}
	return tables, nil
}

func parseClientDataSchemaSQL(raw string) (map[string][]string, error) {
	lines := strings.Split(raw, "\n")
	tables := map[string][]string{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		rest, ok := trimCreateTablePrefix(line)
		if !ok {
			continue
		}
		name, rest, ok := readSQLIdentifier(rest)
		if !ok {
			return nil, fmt.Errorf("extract table name from CREATE TABLE: %q", line)
		}
		if !strings.Contains(rest, "(") {
			for i++; i < len(lines); i++ {
				if strings.Contains(lines[i], "(") {
					break
				}
			}
		}
		var columns []string
		for i++; i < len(lines); i++ {
			colLine := strings.TrimSpace(lines[i])
			if colLine == ");" {
				break
			}
			if colLine == "" || strings.HasPrefix(colLine, "--") {
				continue
			}
			first, _, ok := readSQLIdentifier(strings.TrimSuffix(colLine, ","))
			if !ok ||
				strings.EqualFold(first, "PRIMARY") ||
				strings.EqualFold(first, "UNIQUE") ||
				strings.EqualFold(first, "CHECK") ||
				strings.EqualFold(first, "FOREIGN") {
				continue
			}
			columns = append(columns, first)
		}
		tables[name] = columns
	}
	return tables, nil
}

func trimCreateTablePrefix(line string) (string, bool) {
	const createTable = "CREATE TABLE"
	upper := strings.ToUpper(line)
	if !strings.HasPrefix(upper, createTable) {
		return "", false
	}
	rest := strings.TrimSpace(line[len(createTable):])
	const ifNotExists = "IF NOT EXISTS"
	if strings.HasPrefix(strings.ToUpper(rest), ifNotExists) {
		rest = strings.TrimSpace(rest[len(ifNotExists):])
	}
	return rest, true
}

func readSQLIdentifier(input string) (string, string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", false
	}
	switch input[0] {
	case '"', '`':
		end := strings.IndexByte(input[1:], input[0])
		if end < 0 {
			return "", "", false
		}
		end++
		name := input[1:end]
		return name, strings.TrimSpace(input[end+1:]), name != ""
	case '[':
		end := strings.IndexByte(input[1:], ']')
		if end < 0 {
			return "", "", false
		}
		end++
		name := input[1:end]
		return name, strings.TrimSpace(input[end+1:]), name != ""
	case '(':
		return "", "", false
	}
	end := len(input)
	for idx, r := range input {
		if r == '(' || r == ',' || r == ';' || r == ' ' || r == '\t' {
			end = idx
			break
		}
	}
	name := input[:end]
	return name, strings.TrimSpace(input[end:]), name != ""
}

func checkExpectedSchemaTables(tables map[string][]string) error {
	got := sortedMapKeys(tables)
	if slices.Equal(got, expectedSchemaTableNames) {
		return nil
	}
	return fmt.Errorf(
		"parsed schema tables changed: got %d %v, want %d %v",
		len(got),
		got,
		len(expectedSchemaTableNames),
		expectedSchemaTableNames,
	)
}

func clientDataTableCompletenessFindings(
	tables map[string][]string,
) []clientDataDrift {
	var findings []clientDataDrift
	for table := range tables {
		if reason := nonClientDataSchemaTables[table]; reason != "" {
			continue
		}
		if spec, ok := schemaClientTables[table]; ok && spec.hasPortableSpec() {
			continue
		}
		findings = append(findings, clientDataDrift{
			Surface: driftSchema,
			Entity:  table,
			Field:   "*",
			Kind:    "table-uncovered",
			Detail:  "schema table has no backup/CSV spec or non-client-data reason",
		})
	}
	return findings
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func exportedFieldMap(t reflect.Type) map[string]reflect.StructField {
	fields := map[string]reflect.StructField{}
	for _, field := range exportedFields(t) {
		fields[field.Name] = field
	}
	return fields
}

func exportedFields(t reflect.Type) []reflect.StructField {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var fields []reflect.StructField
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.IsExported() {
			fields = append(fields, field)
		}
	}
	return fields
}

func fieldByPath(t reflect.Type, path string) (reflect.StructField, bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	parts := strings.Split(path, ".")
	for idx, part := range parts {
		field, ok := t.FieldByName(part)
		if !ok || !field.IsExported() {
			return reflect.StructField{}, false
		}
		if idx == len(parts)-1 {
			return field, true
		}
		t = field.Type
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return reflect.StructField{}, false
		}
	}
	return reflect.StructField{}, false
}

func typesCompatible(source, target reflect.Type) bool {
	if source == target {
		return true
	}
	if source.Kind() == target.Kind() {
		return true
	}
	if source.Kind() == reflect.Slice && target.Kind() == reflect.Slice &&
		source.Elem().Kind() == target.Elem().Kind() {
		return true
	}
	return false
}

func isAllowed(surface, entity, field, kind string) bool {
	for _, allowed := range clientDataAllowlist {
		if allowed.Surface != surface || allowed.Kind != kind {
			continue
		}
		if allowed.Entity != "*" && allowed.Entity != entity {
			continue
		}
		if allowed.Field == "*" || allowed.Field == field {
			return true
		}
	}
	return false
}

func reportClientDataDrift(t *testing.T, findings []clientDataDrift) {
	t.Helper()
	filtered := findings[:0]
	for _, finding := range findings {
		if isAllowed(finding.Surface, finding.Entity, finding.Field, finding.Kind) {
			continue
		}
		filtered = append(filtered, finding)
	}
	if len(filtered) == 0 {
		return
	}
	sort.Slice(filtered, func(i, j int) bool {
		return slices.Compare(
			[]string{
				filtered[i].Surface,
				filtered[i].Entity,
				filtered[i].Field,
				filtered[i].Kind,
			},
			[]string{
				filtered[j].Surface,
				filtered[j].Entity,
				filtered[j].Field,
				filtered[j].Kind,
			},
		) < 0
	})
	t.Errorf("client-data export/import drift detected:")
	for _, finding := range filtered {
		t.Errorf(
			"%s -> %s -> %s -> %s: %s",
			finding.Surface,
			finding.Entity,
			finding.Field,
			finding.Kind,
			finding.Detail,
		)
	}
}

func joinField(prefix, field string) string {
	if prefix == "" {
		return field
	}
	return prefix + "." + field
}

func normalizeSentinelPath(path string) string {
	var out strings.Builder
	inIndex := false
	for _, r := range path {
		switch r {
		case '[':
			inIndex = true
		case ']':
			inIndex = false
		default:
			if !inIndex {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

func must(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func mustAllowExists(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("%s: %v", label, err)
	}
}

func adjustmentAmount(mode domain.AdjustmentAmountMode, value string) *domain.AdjustmentAmount {
	return &domain.AdjustmentAmount{Mode: mode, Value: value}
}

func adjustmentBounds(lower, upper string) *domain.AdjustmentBounds {
	return &domain.AdjustmentBounds{Lower: lower, Upper: upper}
}
