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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package domain

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// AuditDetailCode names the English template an audit detail line is rendered
// from. The panel renders the same code from its catalog entry
// audit:detail.<code> in the operator's language; the en entry repeats the
// template here word for word.
type AuditDetailCode string

// AuditDetailParams are the values a detail template's {{name}} placeholders
// are rendered with.
type AuditDetailParams map[string]string

// MarshalJSON writes no values as an empty object rather than null, the shape
// the store column and the wire share.
func (p AuditDetailParams) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]string(p))
}

// AuditDetailField is one " name=value" pair of the tail a detail line carries
// after its template.
type AuditDetailField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AuditDetailFields is the tail of a detail line, in the order it is written.
type AuditDetailFields []AuditDetailField

// MarshalJSON writes no tail as an empty array rather than null, the shape the
// store column and the wire share.
func (f AuditDetailFields) MarshalJSON() ([]byte, error) {
	if f == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]AuditDetailField(f))
}

// AuditDetail is the detail line of an audit row: the template code, the
// values and tail it was rendered with, and the English text rendered from
// them when the row was written. The text is kept as written, so a later
// template change never rewrites history. The zero value is a row with no
// detail.
type AuditDetail struct {
	Code   AuditDetailCode
	Params AuditDetailParams
	Fields AuditDetailFields
	Text   string
}

// Sentinel parameter values for an absent side of a transition. The panel
// shows them through its audit:detailValue catalog entries.
const (
	// AuditDetailNone is the reserved default group, which has no code.
	AuditDetailNone = "<none>"
	// AuditDetailUnset is an absent currency.
	AuditDetailUnset = "<unset>"
)

// Audit detail codes. A code whose wording differs by an outcome or by an
// optional part is split into one code per variant, so every template reads
// as a whole sentence in every language.
const (
	AuditDetailCreateAccount                   AuditDetailCode = "create_account"
	AuditDetailAutoCreateAccount               AuditDetailCode = "auto_create_account"
	AuditDetailSetAccountNotes                 AuditDetailCode = "set_account_notes"
	AuditDetailDeleteAccount                   AuditDetailCode = "delete_account"
	AuditDetailUpdateAccount                   AuditDetailCode = "update_account"
	AuditDetailRenameAccountNewRecord          AuditDetailCode = "rename_account_new_record"
	AuditDetailRenameAccountOldRecord          AuditDetailCode = "rename_account_old_record"
	AuditDetailBlockAccount                    AuditDetailCode = "block_account"
	AuditDetailBlockAccountReason              AuditDetailCode = "block_account_reason"
	AuditDetailUnblockAccount                  AuditDetailCode = "unblock_account"
	AuditDetailUnblockAccountReason            AuditDetailCode = "unblock_account_reason"
	AuditDetailEngineBlockOrder                AuditDetailCode = "engine_block_order"
	AuditDetailEngineBlockOrderDetails         AuditDetailCode = "engine_block_order_details"
	AuditDetailEngineBlockPolicy               AuditDetailCode = "engine_block_policy"
	AuditDetailEngineBlockPolicyDetails        AuditDetailCode = "engine_block_policy_details"
	AuditDetailSetAccountGroup                 AuditDetailCode = "set_account_group"
	AuditDetailSetAccountGroupCurrencyRefused  AuditDetailCode = "set_account_group_currency_refused"
	AuditDetailSetAccountCurrency              AuditDetailCode = "set_account_currency"
	AuditDetailSetAccountCurrencyRefused       AuditDetailCode = "set_account_currency_refused"
	AuditDetailSetGroupCurrency                AuditDetailCode = "set_group_currency"
	AuditDetailSetGroupCurrencyRefused         AuditDetailCode = "set_group_currency_refused"
	AuditDetailSetDefaultGroupCurrency         AuditDetailCode = "set_default_group_currency"
	AuditDetailSetDefaultGroupCurrencyRefused  AuditDetailCode = "set_default_group_currency_refused"
	AuditDetailCreateGroup                     AuditDetailCode = "create_group"
	AuditDetailSetGroupNotes                   AuditDetailCode = "set_group_notes"
	AuditDetailUpdateGroup                     AuditDetailCode = "update_group"
	AuditDetailRenameGroup                     AuditDetailCode = "rename_group"
	AuditDetailBlockGroup                      AuditDetailCode = "block_group"
	AuditDetailBlockGroupReason                AuditDetailCode = "block_group_reason"
	AuditDetailUnblockGroup                    AuditDetailCode = "unblock_group"
	AuditDetailUnblockGroupReason              AuditDetailCode = "unblock_group_reason"
	AuditDetailDeleteGroup                     AuditDetailCode = "delete_group"
	AuditDetailDeleteGroupDetachAccount        AuditDetailCode = "delete_group_detach_account"
	AuditDetailDeleteGroupCurrencyRefused      AuditDetailCode = "delete_group_currency_refused"
	AuditDetailCreateAsset                     AuditDetailCode = "create_asset"
	AuditDetailAutoCreateAsset                 AuditDetailCode = "auto_create_asset"
	AuditDetailUpdateAsset                     AuditDetailCode = "update_asset"
	AuditDetailRenameAssetNewRecord            AuditDetailCode = "rename_asset_new_record"
	AuditDetailRenameAssetOldRecord            AuditDetailCode = "rename_asset_old_record"
	AuditDetailDeleteAsset                     AuditDetailCode = "delete_asset"
	AuditDetailDeleteAssetResolverFailed       AuditDetailCode = "delete_asset_resolver_failed"
	AuditDetailDeleteAssetResolverReconciled   AuditDetailCode = "delete_asset_resolver_reconciled"
	AuditDetailCreateAssetClass                AuditDetailCode = "create_asset_class"
	AuditDetailUpdateAssetClass                AuditDetailCode = "update_asset_class"
	AuditDetailDeleteAssetClass                AuditDetailCode = "delete_asset_class"
	AuditDetailSetLimit                        AuditDetailCode = "set_limit"
	AuditDetailDeleteLimit                     AuditDetailCode = "delete_limit"
	AuditDetailCreateTradingConnection         AuditDetailCode = "create_trading_connection"
	AuditDetailUpdateTradingConnectionSettings AuditDetailCode = "update_trading_connection_settings"
	AuditDetailEnableTradingConnection         AuditDetailCode = "enable_trading_connection"
	AuditDetailDisableTradingConnection        AuditDetailCode = "disable_trading_connection"
	AuditDetailUpsertTradingInstrument         AuditDetailCode = "upsert_trading_instrument"
	AuditDetailDeleteTradingInstrument         AuditDetailCode = "delete_trading_instrument"
	AuditDetailSetTradingFeeAccount            AuditDetailCode = "set_trading_fee_account"
	AuditDetailClearTradingFeeAccount          AuditDetailCode = "clear_trading_fee_account"
	AuditDetailVenueFeeSkipped                 AuditDetailCode = "venue_fee_skipped"
	AuditDetailPutTradingAccess                AuditDetailCode = "put_trading_access"
	AuditDetailPutTradingAccessVenueAccount    AuditDetailCode = "put_trading_access_venue_account"
	AuditDetailRemoveTradingAccess             AuditDetailCode = "remove_trading_access"
	AuditDetailRemoveTradingAccessVenueAccount AuditDetailCode = "remove_trading_access_venue_account"
	AuditDetailCreateMarketDataInstance        AuditDetailCode = "create_market_data_instance"
	AuditDetailEnableMarketDataInstance        AuditDetailCode = "enable_market_data_instance"
	AuditDetailDisableMarketDataInstance       AuditDetailCode = "disable_market_data_instance"
	AuditDetailUpdateMarketDataSettings        AuditDetailCode = "update_market_data_instance_settings"
	AuditDetailDeleteMarketDataInstance        AuditDetailCode = "delete_market_data_instance"
	AuditDetailUpsertMarketDataInstrument      AuditDetailCode = "upsert_market_data_instrument"
	AuditDetailEnableMarketDataInstrument      AuditDetailCode = "enable_market_data_instrument"
	AuditDetailDisableMarketDataInstrument     AuditDetailCode = "disable_market_data_instrument"
	AuditDetailDeleteMarketDataInstrument      AuditDetailCode = "delete_market_data_instrument"
	AuditDetailSubmitOrderCommitted            AuditDetailCode = "submit_order_committed"
	AuditDetailSubmitOrderRejected             AuditDetailCode = "submit_order_rejected"
	AuditDetailExecutionReport                 AuditDetailCode = "execution_report"
	AuditDetailAdjustmentAccepted              AuditDetailCode = "adjustment_accepted"
	AuditDetailAdjustmentRejected              AuditDetailCode = "adjustment_rejected"
	AuditDetailAdjustmentRefAccepted           AuditDetailCode = "adjustment_ref_accepted"
	AuditDetailAdjustmentRefRejected           AuditDetailCode = "adjustment_ref_rejected"
	AuditDetailEnableMcpCommand                AuditDetailCode = "enable_mcp_command"
	AuditDetailDisableMcpCommand               AuditDetailCode = "disable_mcp_command"
	AuditDetailSetSetting                      AuditDetailCode = "set_setting"
	AuditDetailHydrate                         AuditDetailCode = "hydrate"
	AuditDetailExportBackup                    AuditDetailCode = "export_backup"
	AuditDetailRestoreBackup                   AuditDetailCode = "restore_backup"
	AuditDetailResetDatabase                   AuditDetailCode = "reset_database"
	AuditDetailRestartService                  AuditDetailCode = "restart_service"
	AuditDetailStopService                     AuditDetailCode = "stop_service"
	AuditDetailSetAccessModeOpen               AuditDetailCode = "set_access_mode_open"
	AuditDetailSetAccessModeToken              AuditDetailCode = "set_access_mode_token"
	AuditDetailIssueAPIToken                   AuditDetailCode = "issue_api_token"
	AuditDetailIssueAgentToken                 AuditDetailCode = "issue_agent_token"
	AuditDetailGenerateSigningKey              AuditDetailCode = "generate_signing_key"
	AuditDetailImportSigningKey                AuditDetailCode = "import_signing_key"
	AuditDetailEnableESign                     AuditDetailCode = "enable_esign"
	AuditDetailDisableESign                    AuditDetailCode = "disable_esign"
	AuditDetailSubmitAttestationFailed         AuditDetailCode = "submit_attestation_failed"
	AuditDetailIssueAttestation                AuditDetailCode = "issue_attestation"
	AuditDetailConfirmApproval                 AuditDetailCode = "confirm_approval"
	AuditDetailCancelApproval                  AuditDetailCode = "cancel_approval"
	AuditDetailExportBusinessCSV               AuditDetailCode = "export_business_csv"
	AuditDetailExportBusinessCSVUnfiltered     AuditDetailCode = "export_business_csv_unfiltered"
	AuditDetailActivityOrder                   AuditDetailCode = "activity_order"
	AuditDetailActivityAdjustmentAccepted      AuditDetailCode = "activity_adjustment_accepted"
	AuditDetailActivityAdjustmentRejected      AuditDetailCode = "activity_adjustment_rejected"
)

// auditDetailTemplates is the English template of every detail code.
var auditDetailTemplates = map[AuditDetailCode]string{
	AuditDetailCreateAccount:                   "Account {{account}} created",
	AuditDetailAutoCreateAccount:               "Account {{account}} created automatically by {{operation}}",
	AuditDetailSetAccountNotes:                 "Account {{account}} notes changed",
	AuditDetailDeleteAccount:                   "Account {{account}} deleted",
	AuditDetailUpdateAccount:                   "Account {{account}} updated",
	AuditDetailRenameAccountNewRecord:          "Account {{from}} → {{to}} updated (record under the new code)",
	AuditDetailRenameAccountOldRecord:          "Account {{from}} → {{to}} updated (record under the old code)",
	AuditDetailBlockAccount:                    "Account {{account}} blocked",
	AuditDetailBlockAccountReason:              "Account {{account}} blocked: {{reason}}",
	AuditDetailUnblockAccount:                  "Account {{account}} unblocked",
	AuditDetailUnblockAccountReason:            "Account {{account}} unblocked: {{reason}}",
	AuditDetailEngineBlockOrder:                "Engine blocked account {{account}} on order {{order}} ({{rejectCode}}): {{reason}}",
	AuditDetailEngineBlockOrderDetails:         "Engine blocked account {{account}} on order {{order}} ({{rejectCode}}): {{reason}} ({{details}})",
	AuditDetailEngineBlockPolicy:               "Engine blocked account {{account}} by policy {{enginePolicy}} ({{rejectCode}}): {{reason}}",
	AuditDetailEngineBlockPolicyDetails:        "Engine blocked account {{account}} by policy {{enginePolicy}} ({{rejectCode}}): {{reason}} ({{details}})",
	AuditDetailSetAccountGroup:                 "Account {{account}} group changed: {{from}} → {{to}}",
	AuditDetailSetAccountGroupCurrencyRefused:  "Account {{account}} group change refused (currency {{from}} → {{to}}): {{refusal}}",
	AuditDetailSetAccountCurrency:              "Account {{account}} currency changed: {{from}} → {{to}}",
	AuditDetailSetAccountCurrencyRefused:       "Account {{account}} currency change {{from}} → {{to}} refused: {{refusal}}",
	AuditDetailSetGroupCurrency:                "Group {{group}} currency changed: {{from}} → {{to}}",
	AuditDetailSetGroupCurrencyRefused:         "Group {{group}} currency change {{from}} → {{to}} refused: {{refusal}}",
	AuditDetailSetDefaultGroupCurrency:         "Default group currency changed: {{from}} → {{to}}",
	AuditDetailSetDefaultGroupCurrencyRefused:  "Default group currency change {{from}} → {{to}} refused: {{refusal}}",
	AuditDetailCreateGroup:                     "Group {{group}} created",
	AuditDetailSetGroupNotes:                   "Group {{group}} notes changed",
	AuditDetailUpdateGroup:                     "Group {{group}} updated",
	AuditDetailRenameGroup:                     "Group {{from}} → {{to}} updated",
	AuditDetailBlockGroup:                      "Group {{group}} blocked",
	AuditDetailBlockGroupReason:                "Group {{group}} blocked: {{reason}}",
	AuditDetailUnblockGroup:                    "Group {{group}} unblocked",
	AuditDetailUnblockGroupReason:              "Group {{group}} unblocked: {{reason}}",
	AuditDetailDeleteGroup:                     "Group {{group}} deleted; accounts detached: {{detachedAccounts}}, P&L bounds removed: {{destroyedSpotFundsPnlBounds}}",
	AuditDetailDeleteGroupDetachAccount:        "Group {{group}} deleted; account {{account}} detached",
	AuditDetailDeleteGroupCurrencyRefused:      "Group {{group}} deletion with currency {{currency}} refused: {{refusal}}",
	AuditDetailCreateAsset:                     "Asset {{asset}} created",
	AuditDetailAutoCreateAsset:                 "Asset {{asset}} created automatically by {{operation}}",
	AuditDetailUpdateAsset:                     "Asset {{asset}} updated",
	AuditDetailRenameAssetNewRecord:            "Asset {{from}} → {{to}} updated (record under the new code)",
	AuditDetailRenameAssetOldRecord:            "Asset {{from}} → {{to}} updated (record under the old code)",
	AuditDetailDeleteAsset:                     "Asset {{asset}} deleted",
	AuditDetailDeleteAssetResolverFailed:       "Asset {{asset}} deleted (removal from the code index failed)",
	AuditDetailDeleteAssetResolverReconciled:   "Asset {{asset}} deleted (removal from the code index failed, reconciled)",
	AuditDetailCreateAssetClass:                "Asset class {{class}} created",
	AuditDetailUpdateAssetClass:                "Asset class {{from}} → {{to}} updated",
	AuditDetailDeleteAssetClass:                "Asset class {{class}} deleted",
	AuditDetailSetLimit:                        "Limit set: {{policy}} ({{scope}})",
	AuditDetailDeleteLimit:                     "Limit deleted: {{policy}} ({{scope}})",
	AuditDetailCreateTradingConnection:         "Trading connection {{connection}} created ({{provider}}, {{connectionMode}})",
	AuditDetailUpdateTradingConnectionSettings: "Trading connection {{connection}} settings updated",
	AuditDetailEnableTradingConnection:         "Trading connection {{connection}} enabled",
	AuditDetailDisableTradingConnection:        "Trading connection {{connection}} disabled",
	AuditDetailUpsertTradingInstrument:         "Instrument {{connection}}/{{symbol}} saved",
	AuditDetailDeleteTradingInstrument:         "Instrument {{connection}}/{{symbol}} deleted",
	AuditDetailSetTradingFeeAccount:            "Fee account set for trading connection {{connection}}",
	AuditDetailClearTradingFeeAccount:          "Fee account cleared for trading connection {{connection}}",
	AuditDetailVenueFeeSkipped:                 "Venue fee skipped: connection {{connection}}, fee {{fee}}: {{reason}}",
	AuditDetailPutTradingAccess:                "Trading access granted: {{connection}}/{{subject}}",
	AuditDetailPutTradingAccessVenueAccount:    "Trading access granted: {{connection}}/{{subject}}/{{venueAccount}}",
	AuditDetailRemoveTradingAccess:             "Trading access revoked: {{connection}}/{{subject}}",
	AuditDetailRemoveTradingAccessVenueAccount: "Trading access revoked: {{connection}}/{{subject}}/{{venueAccount}}",
	AuditDetailCreateMarketDataInstance:        "Market-data source {{instance}} created",
	AuditDetailEnableMarketDataInstance:        "Market-data source {{instance}} enabled",
	AuditDetailDisableMarketDataInstance:       "Market-data source {{instance}} disabled",
	AuditDetailUpdateMarketDataSettings:        "Market-data source {{instance}} settings updated",
	AuditDetailDeleteMarketDataInstance:        "Market-data source {{instance}} deleted",
	AuditDetailUpsertMarketDataInstrument:      "Market-data instrument {{instance}}/{{symbol}} saved",
	AuditDetailEnableMarketDataInstrument:      "Market-data instrument {{instance}}/{{symbol}} enabled",
	AuditDetailDisableMarketDataInstrument:     "Market-data instrument {{instance}}/{{symbol}} disabled",
	AuditDetailDeleteMarketDataInstrument:      "Market-data instrument {{instance}}/{{symbol}} deleted",
	AuditDetailSubmitOrderCommitted:            "Order {{order}} submitted, account {{account}}, {{base}}/{{quote}}, {{side}}: committed",
	AuditDetailSubmitOrderRejected:             "Order {{order}} submitted, account {{account}}, {{base}}/{{quote}}, {{side}}: rejected",
	AuditDetailExecutionReport:                 "Execution report for order {{order}}, account {{account}}, {{base}}/{{quote}}: qty {{qty}}, remaining {{leavesQty}}, {{status}}, blocks: {{blocks}}",
	AuditDetailAdjustmentAccepted:              "Adjustment for account {{account}}, asset {{asset}}, type {{adjustmentType}}: accepted",
	AuditDetailAdjustmentRejected:              "Adjustment for account {{account}}, asset {{asset}}, type {{adjustmentType}}: rejected",
	AuditDetailAdjustmentRefAccepted:           "Adjustment for account {{account}}, asset {{asset}}, type {{adjustmentType}}, ID {{ref}}: accepted",
	AuditDetailAdjustmentRefRejected:           "Adjustment for account {{account}}, asset {{asset}}, type {{adjustmentType}}, ID {{ref}}: rejected",
	AuditDetailEnableMcpCommand:                "MCP command {{command}} enabled",
	AuditDetailDisableMcpCommand:               "MCP command {{command}} disabled",
	AuditDetailSetSetting:                      "Setting {{key}} changed to {{value}}",
	AuditDetailHydrate:                         "Engine state loaded: assets {{assets}}, accounts {{accounts}}, barriers {{barriers}}, groups {{groups}}, balances {{balances}}",
	AuditDetailExportBackup:                    "Backup of {{realm}} exported, sections: {{sections}}",
	AuditDetailRestoreBackup:                   "Backup of {{realm}} restored, mode: {{restoreMode}}",
	AuditDetailResetDatabase:                   "Database reset from scratch",
	AuditDetailRestartService:                  "Service restart requested",
	AuditDetailStopService:                     "Service stop requested",
	AuditDetailSetAccessModeOpen:               "No token",
	AuditDetailSetAccessModeToken:              "Token and password",
	AuditDetailIssueAPIToken:                   "API token",
	AuditDetailIssueAgentToken:                 "Agent token",
	AuditDetailGenerateSigningKey:              "Signing key {{key}} generated",
	AuditDetailImportSigningKey:                "Signing key {{key}} imported, format {{format}}",
	AuditDetailEnableESign:                     "eSign enabled",
	AuditDetailDisableESign:                    "eSign disabled",
	AuditDetailSubmitAttestationFailed:         "Attestation on submit failed: {{error}}",
	AuditDetailIssueAttestation:                "Attestation issued: order {{order}}, event {{event}}, submit request",
	AuditDetailConfirmApproval:                 "Approval {{approval}} confirmed, order {{order}}",
	AuditDetailCancelApproval:                  "Approval {{approval}} cancelled, order {{order}}, reason: {{reason}}",
	AuditDetailExportBusinessCSV:               "Business CSV exported: {{entity}}, delimiter {{delimiter}}, ZIP {{zip}}, filters {{filters}}",
	AuditDetailExportBusinessCSVUnfiltered:     "Business CSV exported: {{entity}}, delimiter {{delimiter}}, ZIP {{zip}}, no filters",
	AuditDetailActivityOrder:                   "{{side}} {{amount}} {{base}}/{{quote}} {{status}}",
	AuditDetailActivityAdjustmentAccepted:      "Adjustment {{asset}}: accepted",
	AuditDetailActivityAdjustmentRejected:      "Adjustment {{asset}}: rejected",
}

// auditDetailFieldNames lists every name a detail tail may carry. The panel
// labels each through its audit:detailField catalog entry.
var auditDetailFieldNames = map[string]bool{
	"account":              true,
	"account_group":        true,
	"asset":                true,
	"max_orders":           true,
	"window":               true,
	"max_quantity":         true,
	"max_notional":         true,
	"currency":             true,
	"lower_bound":          true,
	"upper_bound":          true,
	"reservationRemainder": true,
	"overfill":             true,
	"preReportReserveQty":  true,
	"forced":               true,
}

// NewAuditDetail renders code's template with params and appends fields as
// its tail. A detail that does not render - an unknown code, a missing or an
// extra parameter, an unknown field - carries no text, and Validate, which
// every audit write runs, reports why. Empty params and fields are kept as
// nil, the way a stored detail reads back.
func NewAuditDetail(
	code AuditDetailCode, params AuditDetailParams, fields ...AuditDetailField,
) AuditDetail {
	if len(params) == 0 {
		params = nil
	}
	if len(fields) == 0 {
		fields = nil
	}
	d := AuditDetail{Code: code, Params: params, Fields: fields}
	if text, err := d.render(); err == nil {
		d.Text = text
	}
	return d
}

// Validate reports whether d is either empty or the rendering of a known
// template with exactly its parameters and a known tail.
func (d AuditDetail) Validate() error {
	if d.Code == "" {
		if d.Text != "" || len(d.Params) != 0 || len(d.Fields) != 0 {
			return fmt.Errorf("audit detail without a code: %w", ErrInvalid)
		}
		return nil
	}
	text, err := d.render()
	if err != nil {
		return err
	}
	if d.Text != text {
		return fmt.Errorf(
			"audit detail %s: text %q is not its rendering %q: %w",
			d.Code, d.Text, text, ErrInvalid,
		)
	}
	return nil
}

func (d AuditDetail) render() (string, error) {
	template, ok := auditDetailTemplates[d.Code]
	if !ok {
		return "", fmt.Errorf("audit detail code %q: %w", d.Code, ErrInvalid)
	}
	var b strings.Builder
	used := make(map[string]bool, len(d.Params))
	rest := template
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}}")
		if end < 0 {
			return "", fmt.Errorf("audit detail %s: unterminated placeholder: %w", d.Code, ErrInvalid)
		}
		name := rest[start+2 : start+end]
		value, ok := d.Params[name]
		if !ok {
			return "", fmt.Errorf("audit detail %s: no value for %q: %w", d.Code, name, ErrInvalid)
		}
		used[name] = true
		b.WriteString(rest[:start])
		b.WriteString(value)
		rest = rest[start+end+2:]
	}
	for _, name := range slices.Sorted(maps.Keys(d.Params)) {
		if !used[name] {
			return "", fmt.Errorf("audit detail %s: no placeholder for %q: %w", d.Code, name, ErrInvalid)
		}
	}
	for _, field := range d.Fields {
		if !auditDetailFieldNames[field.Name] {
			return "", fmt.Errorf("audit detail %s: field %q: %w", d.Code, field.Name, ErrInvalid)
		}
		b.WriteByte(' ')
		b.WriteString(field.Name)
		b.WriteByte('=')
		b.WriteString(field.Value)
	}
	return b.String(), nil
}
