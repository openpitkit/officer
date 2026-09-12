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

package domain

// Stable reject-code names persisted by Officer. The native adapter maps the
// SDK enum onto these values; keeping the strings here lets portable archive
// validation use the same vocabulary without depending on a native engine.
const (
	RejectCodeMissingRequiredField            = "missing_required_field"
	RejectCodeInvalidFieldFormat              = "invalid_field_format"
	RejectCodeInvalidFieldValue               = "invalid_field_value"
	RejectCodeUnsupportedOrderType            = "unsupported_order_type"
	RejectCodeUnsupportedTimeInForce          = "unsupported_time_in_force"
	RejectCodeUnsupportedOrderAttribute       = "unsupported_order_attribute"
	RejectCodeDuplicateClientOrderID          = "duplicate_client_order_id"
	RejectCodeTooLateToEnter                  = "too_late_to_enter"
	RejectCodeExchangeClosed                  = "exchange_closed"
	RejectCodeUnknownInstrument               = "unknown_instrument"
	RejectCodeUnknownAccount                  = "unknown_account"
	RejectCodeUnknownVenue                    = "unknown_venue"
	RejectCodeUnknownClearingAccount          = "unknown_clearing_account"
	RejectCodeUnknownCollateralAsset          = "unknown_collateral_asset"
	RejectCodeInsufficientFunds               = "insufficient_funds"
	RejectCodeInsufficientMargin              = "insufficient_margin"
	RejectCodeInsufficientPosition            = "insufficient_position"
	RejectCodeCreditLimitExceeded             = "credit_limit_exceeded"
	RejectCodeRiskLimitExceeded               = "risk_limit_exceeded"
	RejectCodeOrderExceedsLimit               = "order_exceeds_limit"
	RejectCodeOrderQtyExceedsLimit            = "order_qty_exceeds_limit"
	RejectCodeOrderNotionalExceedsLimit       = "order_notional_exceeds_limit"
	RejectCodePositionLimitExceeded           = "position_limit_exceeded"
	RejectCodeConcentrationLimitExceeded      = "concentration_limit_exceeded"
	RejectCodeLeverageLimitExceeded           = "leverage_limit_exceeded"
	RejectCodeRateLimitExceeded               = "rate_limit_exceeded"
	RejectCodePnlKillSwitchTriggered          = "pnl_kill_switch_triggered"
	RejectCodeAccountBlocked                  = "account_blocked"
	RejectCodeAccountNotAuthorized            = "account_not_authorized"
	RejectCodeComplianceRestriction           = "compliance_restriction"
	RejectCodeInstrumentRestricted            = "instrument_restricted"
	RejectCodeJurisdictionRestriction         = "jurisdiction_restriction"
	RejectCodeWashTradePrevention             = "wash_trade_prevention"
	RejectCodeSelfMatchPrevention             = "self_match_prevention"
	RejectCodeShortSaleRestriction            = "short_sale_restriction"
	RejectCodeRiskConfigurationMissing        = "risk_configuration_missing"
	RejectCodeReferenceDataUnavailable        = "reference_data_unavailable"
	RejectCodeOrderValueCalculationFailed     = "order_value_calculation_failed"
	RejectCodeSystemUnavailable               = "system_unavailable"
	RejectCodeMarkPriceUnavailable            = "mark_price_unavailable"
	RejectCodeAccountAdjustmentBoundsExceeded = "account_adjustment_bounds_exceeded"
	RejectCodeArithmeticOverflow              = "arithmetic_overflow"
	RejectCodeCustom                          = "custom"
	RejectCodeOther                           = "other"
)

var knownRejectCodes = map[string]struct{}{
	RejectCodeMissingRequiredField:            {},
	RejectCodeInvalidFieldFormat:              {},
	RejectCodeInvalidFieldValue:               {},
	RejectCodeUnsupportedOrderType:            {},
	RejectCodeUnsupportedTimeInForce:          {},
	RejectCodeUnsupportedOrderAttribute:       {},
	RejectCodeDuplicateClientOrderID:          {},
	RejectCodeTooLateToEnter:                  {},
	RejectCodeExchangeClosed:                  {},
	RejectCodeUnknownInstrument:               {},
	RejectCodeUnknownAccount:                  {},
	RejectCodeUnknownVenue:                    {},
	RejectCodeUnknownClearingAccount:          {},
	RejectCodeUnknownCollateralAsset:          {},
	RejectCodeInsufficientFunds:               {},
	RejectCodeInsufficientMargin:              {},
	RejectCodeInsufficientPosition:            {},
	RejectCodeCreditLimitExceeded:             {},
	RejectCodeRiskLimitExceeded:               {},
	RejectCodeOrderExceedsLimit:               {},
	RejectCodeOrderQtyExceedsLimit:            {},
	RejectCodeOrderNotionalExceedsLimit:       {},
	RejectCodePositionLimitExceeded:           {},
	RejectCodeConcentrationLimitExceeded:      {},
	RejectCodeLeverageLimitExceeded:           {},
	RejectCodeRateLimitExceeded:               {},
	RejectCodePnlKillSwitchTriggered:          {},
	RejectCodeAccountBlocked:                  {},
	RejectCodeAccountNotAuthorized:            {},
	RejectCodeComplianceRestriction:           {},
	RejectCodeInstrumentRestricted:            {},
	RejectCodeJurisdictionRestriction:         {},
	RejectCodeWashTradePrevention:             {},
	RejectCodeSelfMatchPrevention:             {},
	RejectCodeShortSaleRestriction:            {},
	RejectCodeRiskConfigurationMissing:        {},
	RejectCodeReferenceDataUnavailable:        {},
	RejectCodeOrderValueCalculationFailed:     {},
	RejectCodeSystemUnavailable:               {},
	RejectCodeMarkPriceUnavailable:            {},
	RejectCodeAccountAdjustmentBoundsExceeded: {},
	RejectCodeArithmeticOverflow:              {},
	RejectCodeCustom:                          {},
	RejectCodeOther:                           {},
}

// KnownRejectCode reports whether code is one of the SDK reject codes Officer
// can restore as a typed account-block cause.
func KnownRejectCode(code string) bool {
	_, ok := knownRejectCodes[code]
	return ok
}
