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

package node

import (
	"strconv"

	"go.openpit.dev/officer/framework/domain"
)

// setRateLimitDetail renders a rate-limit upsert as a short human-readable audit
// detail, e.g. "Limit set: rate_limit (account) account=acc-1 max_orders=100
// window=1s".
func setRateLimitDetail(limit domain.LimitRate) domain.AuditDetail {
	target := LimitTarget{
		Policy:  domain.PolicyRateLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	fields := append(axesFields(target),
		domain.AuditDetailField{Name: "max_orders", Value: strconv.FormatUint(limit.MaxOrders, 10)},
		domain.AuditDetailField{Name: "window", Value: limit.Window.String()},
	)
	return domain.NewAuditDetail(domain.AuditDetailSetLimit, axesParams(target), fields...)
}

// setOrderSizeLimitDetail renders an order-size upsert as a short audit detail.
func setOrderSizeLimitDetail(limit domain.LimitOrderSize) domain.AuditDetail {
	target := LimitTarget{
		Policy:  domain.PolicyOrderSizeLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	fields := axesFields(target)
	if limit.MaxQuantity != "" {
		fields = append(fields, domain.AuditDetailField{Name: "max_quantity", Value: limit.MaxQuantity})
	}
	if limit.MaxNotional != "" {
		fields = append(fields, domain.AuditDetailField{Name: "max_notional", Value: limit.MaxNotional})
	}
	return domain.NewAuditDetail(domain.AuditDetailSetLimit, axesParams(target), fields...)
}

// setSpotFundsPnlBoundsLimitDetail renders a SpotFunds P&L-bounds upsert.
func setSpotFundsPnlBoundsLimitDetail(limit domain.LimitSpotFundsPnlBounds) domain.AuditDetail {
	target := LimitTarget{
		Policy:       domain.PolicySpotFundsPnlBoundsKillSwitch,
		Scope:        limit.Scope,
		Account:      limit.Account,
		AccountGroup: limit.AccountGroup,
	}
	fields := append(axesFields(target),
		domain.AuditDetailField{Name: "currency", Value: limit.Currency},
	)
	if limit.LowerBound != "" {
		fields = append(fields, domain.AuditDetailField{Name: "lower_bound", Value: limit.LowerBound})
	}
	if limit.UpperBound != "" {
		fields = append(fields, domain.AuditDetailField{Name: "upper_bound", Value: limit.UpperBound})
	}
	return domain.NewAuditDetail(domain.AuditDetailSetLimit, axesParams(target), fields...)
}

// deleteLimitDetail renders a barrier deletion as a short audit detail.
func deleteLimitDetail(target LimitTarget) domain.AuditDetail {
	return domain.NewAuditDetail(
		domain.AuditDetailDeleteLimit, axesParams(target), axesFields(target)...,
	)
}

// setAccountGroupDetail renders an account group change. The move is filed under
// each non-empty side, so the line names both: a reader who found the row under
// the origin group and one who found it under the destination group must both
// see where the account came from and where it went.
func setAccountGroupDetail(id domain.AccountID, prevGroupCode, groupCode string) domain.AuditDetail {
	return domain.NewAuditDetail(domain.AuditDetailSetAccountGroup, domain.AuditDetailParams{
		"account": id.String(),
		"from":    groupSideDetail(prevGroupCode),
		"to":      groupSideDetail(groupCode),
	})
}

// updateGroupDetail renders a group update. A renamed group is filed under both
// codes, so the line names both; a title-only update has no transition to state.
func updateGroupDetail(prevCode, code string) domain.AuditDetail {
	if prevCode == code {
		return domain.NewAuditDetail(
			domain.AuditDetailUpdateGroup, domain.AuditDetailParams{"group": code},
		)
	}
	return domain.NewAuditDetail(
		domain.AuditDetailRenameGroup,
		domain.AuditDetailParams{"from": prevCode, "to": code},
	)
}

// groupSideDetail renders one side of a group transition. The empty code is the
// reserved default group, which has no operator-facing code to name.
func groupSideDetail(code string) string {
	if code == "" {
		return domain.AuditDetailNone
	}
	return code
}

// adjustmentDetail renders one spot-funds adjustment, its type and its
// accept/reject disposition. ref is printed only when the producer supplied
// one (the source reference, such as a venue fee id); a store-minted id is not
// a reference and is left out.
func adjustmentDetail(
	id domain.AccountID, asset string, typ domain.AdjustmentType,
	ref domain.ExternalID, accepted bool,
) domain.AuditDetail {
	params := domain.AuditDetailParams{
		"account":        id.String(),
		"asset":          asset,
		"adjustmentType": string(typ),
	}
	if ref.IsZero() {
		code := domain.AuditDetailAdjustmentRejected
		if accepted {
			code = domain.AuditDetailAdjustmentAccepted
		}
		return domain.NewAuditDetail(code, params)
	}
	params["ref"] = ref.String()
	code := domain.AuditDetailAdjustmentRefRejected
	if accepted {
		code = domain.AuditDetailAdjustmentRefAccepted
	}
	return domain.NewAuditDetail(code, params)
}

// setMcpAccessDetail renders one MCP command access toggle.
func setMcpAccessDetail(command string, enabled bool) domain.AuditDetail {
	code := domain.AuditDetailDisableMcpCommand
	if enabled {
		code = domain.AuditDetailEnableMcpCommand
	}
	return domain.NewAuditDetail(code, domain.AuditDetailParams{"command": command})
}

// setSettingDetail renders one realm-wide setting write.
func setSettingDetail(key, value string) domain.AuditDetail {
	return domain.NewAuditDetail(
		domain.AuditDetailSetSetting, domain.AuditDetailParams{"key": key, "value": value},
	)
}

// marketDataInstanceToggleDetail renders one market-data instance enable/disable
// toggle.
func marketDataInstanceToggleDetail(id domain.ExternalID, enabled bool) domain.AuditDetail {
	code := domain.AuditDetailDisableMarketDataInstance
	if enabled {
		code = domain.AuditDetailEnableMarketDataInstance
	}
	return domain.NewAuditDetail(code, domain.AuditDetailParams{"instance": id.String()})
}

// marketDataInstrumentToggleDetail renders one market-data instrument
// enable/disable toggle.
func marketDataInstrumentToggleDetail(
	instance domain.ExternalID, externalSymbol string, enabled bool,
) domain.AuditDetail {
	code := domain.AuditDetailDisableMarketDataInstrument
	if enabled {
		code = domain.AuditDetailEnableMarketDataInstrument
	}
	return domain.NewAuditDetail(code, domain.AuditDetailParams{
		"instance": instance.String(),
		"symbol":   externalSymbol,
	})
}

// submitOrderDetail renders one order submission and its accept/reject
// disposition. The order is named by its opaque external id.
func submitOrderDetail(order domain.Order, accepted bool) domain.AuditDetail {
	code := domain.AuditDetailSubmitOrderRejected
	if accepted {
		code = domain.AuditDetailSubmitOrderCommitted
	}
	return domain.NewAuditDetail(code, domain.AuditDetailParams{
		"order":   order.ExternalID.String(),
		"account": order.Account.String(),
		"base":    order.BaseAsset,
		"quote":   order.QuoteAsset,
		"side":    string(order.Side),
	})
}

// executionReportDetail renders one execution report and how many account
// blocks it produced. extra is the tail a caller appends after the
// reservation remainder: the overfill and forced-terminal markers.
func executionReportDetail(
	in domain.ExecutionReportInput,
	reservationRemainder string,
	status domain.OrderStatus,
	blocks int,
	extra ...domain.AuditDetailField,
) domain.AuditDetail {
	fields := make([]domain.AuditDetailField, 0, 1+len(extra))
	if reservationRemainder != "" {
		fields = append(fields, domain.AuditDetailField{
			Name: "reservationRemainder", Value: reservationRemainder,
		})
	}
	fields = append(fields, extra...)
	return domain.NewAuditDetail(domain.AuditDetailExecutionReport, domain.AuditDetailParams{
		"order":     in.Order.String(),
		"account":   in.Account.String(),
		"base":      in.BaseAsset,
		"quote":     in.QuoteAsset,
		"qty":       in.FillQuantity,
		"leavesQty": in.LeavesQuantity,
		"status":    string(status),
		"blocks":    strconv.Itoa(blocks),
	}, fields...)
}

// forcedExecutionField marks an execution report that bypassed a terminal
// order state.
var forcedExecutionField = domain.AuditDetailField{Name: "forced", Value: "true"}

// blockDetail renders one operator-initiated account block. Re-blocking an
// operator-authored cause replaces account.block_reason, while a typed cause
// conflicts until unblocked, so the audit trail preserves every applied
// operator reason that the mutable account row no longer carries.
func blockDetail(id domain.AccountID, reason string) domain.AuditDetail {
	return blockReasonDetail(
		domain.AuditDetailBlockAccount, domain.AuditDetailBlockAccountReason,
		"account", id.String(), reason,
	)
}

// unblockDetail renders one operator-initiated account unblock. reason must be
// empty: an unblock is never justified by the reason of the block it lifts, and
// rendering that text would read as the operator's justification for unblocking.
func unblockDetail(id domain.AccountID, reason string) domain.AuditDetail {
	return blockReasonDetail(
		domain.AuditDetailUnblockAccount, domain.AuditDetailUnblockAccountReason,
		"account", id.String(), reason,
	)
}

// blockGroupDetail renders one operator-initiated group block.
func blockGroupDetail(code, reason string) domain.AuditDetail {
	return blockReasonDetail(
		domain.AuditDetailBlockGroup, domain.AuditDetailBlockGroupReason,
		"group", code, reason,
	)
}

// unblockGroupDetail renders one operator-initiated group unblock. reason
// follows unblockDetail and must be empty.
func unblockGroupDetail(code, reason string) domain.AuditDetail {
	return blockReasonDetail(
		domain.AuditDetailUnblockGroup, domain.AuditDetailUnblockGroupReason,
		"group", code, reason,
	)
}

// blockReasonDetail renders the bare block detail, or its reason variant when
// the operator gave a reason. The reason is validated as printable upstream,
// so it cannot forge an extra audit line here.
func blockReasonDetail(
	bare, withReason domain.AuditDetailCode, subject, code, reason string,
) domain.AuditDetail {
	if reason == "" {
		return domain.NewAuditDetail(bare, domain.AuditDetailParams{subject: code})
	}
	return domain.NewAuditDetail(withReason, domain.AuditDetailParams{
		subject:  code,
		"reason": reason,
	})
}

// engineBlockDetail renders one engine-initiated (kill-switch) account block,
// naming the triggering order by its external id and the engine's stable reject
// code and reason so an operator can see why the account was blocked.
func engineBlockDetail(order domain.ExternalID, block domain.AccountBlock) domain.AuditDetail {
	params := domain.AuditDetailParams{
		"account":    block.Account.String(),
		"order":      order.String(),
		"rejectCode": block.Code,
		"reason":     block.Reason,
	}
	if block.Details == "" {
		return domain.NewAuditDetail(domain.AuditDetailEngineBlockOrder, params)
	}
	params["details"] = block.Details
	return domain.NewAuditDetail(domain.AuditDetailEngineBlockOrderDetails, params)
}

// policyConfigurationBlockDetail identifies an engine block that arose while
// applying a live policy update, before any subsequent account work can run.
func policyConfigurationBlockDetail(policy string, block domain.AccountBlock) domain.AuditDetail {
	blockPolicy := block.Policy
	if blockPolicy == "" {
		blockPolicy = policy
	}
	params := domain.AuditDetailParams{
		"account":      block.Account.String(),
		"enginePolicy": blockPolicy,
		"rejectCode":   block.Code,
		"reason":       block.Reason,
	}
	if block.Details == "" {
		return domain.NewAuditDetail(domain.AuditDetailEngineBlockPolicy, params)
	}
	params["details"] = block.Details
	return domain.NewAuditDetail(domain.AuditDetailEngineBlockPolicyDetails, params)
}

// axesParams names the policy and scope of a typed barrier.
func axesParams(target LimitTarget) domain.AuditDetailParams {
	return domain.AuditDetailParams{
		"policy": target.Policy,
		"scope":  string(target.Scope),
	}
}

// axesFields renders the account, group and asset axes of a typed barrier.
func axesFields(target LimitTarget) []domain.AuditDetailField {
	fields := make([]domain.AuditDetailField, 0, 3)
	if target.Account != "" {
		fields = append(fields, domain.AuditDetailField{Name: "account", Value: target.Account.String()})
	}
	if target.AccountGroup != "" {
		fields = append(fields, domain.AuditDetailField{Name: "account_group", Value: target.AccountGroup})
	}
	if target.Asset != "" {
		fields = append(fields, domain.AuditDetailField{Name: "asset", Value: target.Asset})
	}
	return fields
}
